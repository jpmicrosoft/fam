package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"gopkg.in/yaml.v3"
)

func TestVersionsAreStableNumericAndOverflowSafe(t *testing.T) {
	for _, input := range []string{
		"0.0.0", "v1.2.3", "999.10.1", "18446744073709551615.0.0",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := parseVersion(input)
			if err != nil || got.String() != strings.TrimPrefix(input, "v") {
				t.Fatalf("parse = %v, %v", got, err)
			}
		})
	}
	for _, input := range []string{
		"", "unknown", "dev", "latest", "V1.2.3", "vv1.2.3", "1.2", "1.2.3.4",
		"1.2.3-preview", "1.2.3+build", "01.2.3", "1.02.3", "1.2.03",
		"-1.2.3", "1.+2.3", "1..3", " 1.2.3", "1.2.3\n",
		"18446744073709551616.0.0", strings.Repeat("9", 100) + ".0.0",
	} {
		t.Run("reject/"+input, func(t *testing.T) {
			if _, err := parseVersion(input); !errs.IsKind(err, "config") {
				t.Fatalf("expected config error, got %v", err)
			}
		})
	}
	for _, pair := range [][2]string{
		{"1.9.9", "1.10.0"}, {"9.99.99", "10.0.0"},
		{"4294967295.0.0", "4294967296.0.0"},
		{"18446744073709551614.0.0", "18446744073709551615.0.0"},
	} {
		a, err := parseVersion(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseVersion(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if a.compare(b) != -1 || b.compare(a) != 1 || a.compare(a) != 0 {
			t.Fatalf("incorrect comparison: %v", pair)
		}
	}
}

func TestNewRejectsUnsafeOptionsBeforeAccess(t *testing.T) {
	for _, options := range []Options{
		{CurrentVersion: "unknown"},
		{CurrentVersion: "1.0.0", Version: "0.9.0"},
		{CurrentVersion: "1.0.0", Version: "1.2.3+build"},
		{CurrentVersion: "1.0.0", Timeout: -1},
		{CurrentVersion: "1.0.0", Timeout: time.Hour + 1},
		{CurrentVersion: "1.0.0", Retries: -1},
		{CurrentVersion: "1.0.0", Retries: 11},
		{CurrentVersion: "1.0.0", RetryDelay: -1},
		{CurrentVersion: "1.0.0", RetryDelay: time.Minute + 1},
		{CurrentVersion: "1.0.0", Token: "synthetic\r\nsecret"},
	} {
		if _, err := New(options); !errs.IsKind(err, "config") {
			t.Fatalf("expected config error, got %v", err)
		}
	}
}

func TestCheckIsMetadataOnlyAndUsesCanonicalStatuses(t *testing.T) {
	for _, test := range []struct {
		current, wanted, target, status, endpoint string
		available                                 bool
	}{
		{"1.0.0", "", "1.1.0", "available", "latest", true},
		{"v1.1.0", "", "1.1.0", "up-to-date", "latest", false},
		{"1.2.0", "", "1.1.0", "newer-installed", "latest", false},
		{"1.0.0", "v1.1.0", "1.1.0", "available", "tags/v1.1.0", true},
		{"1.1.0", "1.1.0", "1.1.0", "up-to-date", "tags/v1.1.0", false},
	} {
		t.Run(test.status+"/"+test.wanted, func(t *testing.T) {
			u := testUpdater(t, test.current, test.wanted, runtime.GOOS)
			f := attachFixture(t, u, test.target, []byte("new fixture"))
			before := directoryNames(t, filepath.Dir(u.target))
			plan, err := u.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if plan.Result.Status != test.status || plan.Result.UpdateAvailable != test.available ||
				plan.Result.TargetVersion != test.target || plan.Result.Changed {
				t.Fatalf("unexpected result: %+v", plan.Result)
			}
			if got := f.requests(); !reflect.DeepEqual(got, []string{
				"/repos/jpmicrosoft/fam/releases/" + test.endpoint,
			}) {
				t.Fatalf("not metadata-only: %v", got)
			}
			if !test.available {
				if _, err := u.Apply(context.Background(), plan); err != nil {
					t.Fatal(err)
				}
				if len(f.requests()) != 1 {
					t.Fatal("no-op apply downloaded an asset")
				}
			}
			if after := directoryNames(t, filepath.Dir(u.target)); !reflect.DeepEqual(before, after) {
				t.Fatalf("check/no-op wrote files: %v", after)
			}
		})
	}
}

func TestCheckRejectsUnstableAmbiguousOrMissingMetadata(t *testing.T) {
	tests := []struct {
		name string
		edit func(*fixture)
	}{
		{"draft", func(f *fixture) { yes := true; f.release.Draft = &yes }},
		{"prerelease", func(f *fixture) { yes := true; f.release.Prerelease = &yes }},
		{"missing-draft", func(f *fixture) { f.release.Draft = nil }},
		{"missing-prerelease", func(f *fixture) { f.release.Prerelease = nil }},
		{"prerelease-tag", func(f *fixture) { f.release.TagName = "v1.1.0-rc.1" }},
		{"build-tag", func(f *fixture) { f.release.TagName = "v1.1.0+build" }},
		{"leading-zero", func(f *fixture) { f.release.TagName = "v01.1.0" }},
		{"unprefixed-tag", func(f *fixture) { f.release.TagName = "1.1.0" }},
		{"wrong-tag", func(f *fixture) { f.release.TagName = "v1.2.0" }},
		{"duplicate-archive", func(f *fixture) {
			f.release.Assets = append(f.release.Assets, asset{ID: 33, Name: f.release.Assets[0].Name})
		}},
		{"duplicate-checksum", func(f *fixture) {
			f.release.Assets = append(f.release.Assets, asset{ID: 33, Name: "SHA256SUMS"})
		}},
		{"duplicate-id", func(f *fixture) { f.release.Assets[1].ID = f.release.Assets[0].ID }},
		{"zero-id", func(f *fixture) { f.release.Assets[0].ID = 0 }},
		{"negative-id", func(f *fixture) { f.release.Assets[0].ID = -1 }},
		{"missing-archive", func(f *fixture) { f.release.Assets = f.release.Assets[1:] }},
		{"missing-checksum", func(f *fixture) { f.release.Assets = f.release.Assets[:1] }},
		{"duplicate-json", func(f *fixture) {
			f.metadata = []byte(`{"tag_name":"v1.1.0","draft":true,"draft":false,"prerelease":false}`)
		}},
		{"case-duplicate-json", func(f *fixture) {
			f.metadata = []byte(`{"tag_name":"v1.1.0","draft":true,"Draft":false,"prerelease":false}`)
		}},
		{"trailing-json", func(f *fixture) { f.metadata = []byte(`{} {}`) }},
		{"malformed-json", func(f *fixture) { f.metadata = []byte(`{"tag_name":`) }},
		{"overflow-id", func(f *fixture) {
			f.metadata = []byte(`{"tag_name":"v1.1.0","draft":false,"prerelease":false,"assets":[{"id":9223372036854775808,"name":"SHA256SUMS"}]}`)
		}},
		{"asset-count", func(f *fixture) {
			for i := 0; i < 129; i++ {
				f.release.Assets = append(f.release.Assets, asset{ID: int64(1000 + i), Name: strings.Repeat("x", i+1)})
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			u := testUpdater(t, "1.0.0", "1.1.0", runtime.GOOS)
			f := attachFixture(t, u, "1.1.0", []byte("new fixture"))
			test.edit(f)
			before := directoryNames(t, filepath.Dir(u.target))
			if _, err := u.Check(context.Background()); err == nil {
				t.Fatal("unsafe metadata accepted")
			}
			if len(f.requests()) != 1 || !reflect.DeepEqual(before, directoryNames(t, filepath.Dir(u.target))) {
				t.Fatal("rejected metadata caused asset access or writes")
			}
		})
	}
}

func TestMetadataURLsCannotSelectDownloadDestinations(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	payload := []byte("offline replacement")
	f := attachFixture(t, u, "1.1.0", payload)
	data, err := json.Marshal(f.release)
	if err != nil {
		t.Fatal(err)
	}
	f.metadata = bytes.ReplaceAll(data, []byte(`"id":`),
		[]byte(`"url":"https://evil.example/secret","browser_download_url":"file:///must-not-read","id":`))
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if got := f.requests(); !reflect.DeepEqual(got, []string{
		"/repos/jpmicrosoft/fam/releases/latest",
		"/repos/jpmicrosoft/fam/releases/assets/22",
		"/repos/jpmicrosoft/fam/releases/assets/11",
	}) {
		t.Fatalf("asset endpoints were not constructed from validated IDs: %v", got)
	}
}

func TestArchiveNamePreferenceAndLegacyFallback(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		for _, legacyOnly := range []bool{false, true} {
			u := testUpdater(t, "1.0.0", "", goos)
			f := attachFixture(t, u, "1.1.0", []byte("new fixture"))
			preferred := f.release.Assets[0].Name
			legacy := "foundry-agent-manager_" + strings.TrimPrefix(preferred, "fam_")
			f.release.Assets = append(f.release.Assets, asset{ID: 33, Name: legacy})
			want := preferred
			if legacyOnly {
				f.release.Assets = f.release.Assets[1:]
				want = legacy
				f.checksum = checksumFixture(f.archive, legacy)
			}
			plan, err := u.Check(context.Background())
			if err != nil || plan.Result.Asset != want {
				t.Fatalf("selection: plan=%+v err=%v", plan, err)
			}
			if _, err := u.Apply(context.Background(), plan); err != nil {
				t.Fatalf("selected archive could not be applied: %v", err)
			}
		}
	}
}

func TestApplyUsesPrivatePlanAuthority(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	payload := []byte("verified offline replacement - not an executable")
	attachFixture(t, u, "1.1.0", payload)
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan.Result = Result{Executable: filepath.Join(t.TempDir(), "must-not-exist"), Status: "up-to-date"}
	result, err := u.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "updated" || !result.Changed || result.Executable != u.target ||
		result.TargetVersion != "1.1.0" || !bytes.Equal(mustRead(t, u.target), payload) {
		t.Fatalf("incorrect replacement result: %+v", result)
	}
	if _, err := os.Stat(plan.Result.Executable); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("display data selected a filesystem target")
	}
	if _, err := u.Apply(context.Background(), plan); !errs.IsKind(err, "conflict") {
		t.Fatalf("replayed plan was not rejected: %v", err)
	}
}

func TestApplyRejectsFabricatedAndForeignPlans(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	other := testUpdater(t, "1.0.0", "", runtime.GOOS)
	attachFixture(t, other, "1.1.0", []byte("new fixture"))
	foreign, err := other.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range []*Plan{nil, {Result: Result{UpdateAvailable: true}}, foreign} {
		if _, err := u.Apply(context.Background(), plan); !errs.IsKind(err, "security") {
			t.Fatalf("unowned plan accepted: %v", err)
		}
	}
}

func TestChecksumMismatchMakesNoFilesystemMutation(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	f := attachFixture(t, u, "1.1.0", []byte("new fixture"))
	f.archive[0] ^= 1
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := directoryNames(t, filepath.Dir(u.target))
	original := mustRead(t, u.target)
	result, err := u.Apply(context.Background(), plan)
	if !errs.IsKind(err, "security") || result.Changed {
		t.Fatalf("checksum mismatch accepted: %+v, %v", result, err)
	}
	if !bytes.Equal(original, mustRead(t, u.target)) ||
		!reflect.DeepEqual(before, directoryNames(t, filepath.Dir(u.target))) {
		t.Fatal("checksum failure mutated the filesystem")
	}
}

func TestStaleTargetIsRejectedEvenWithSameSizeAndTimestamp(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	f := attachFixture(t, u, "1.1.0", []byte("new fixture"))
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	modified := mustRead(t, u.target)
	modified[0] ^= 1
	mustWrite(t, u.target, modified, 0755)
	if err := os.Chtimes(u.target, u.identity.info.ModTime(), u.identity.info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Apply(context.Background(), plan); !errs.IsKind(err, "conflict") {
		t.Fatalf("stale plan accepted: %v", err)
	}
	if len(f.requests()) != 1 || !bytes.Equal(modified, mustRead(t, u.target)) {
		t.Fatal("stale updater downloaded or replaced the newer target")
	}
	if _, err := u.Check(context.Background()); !errs.IsKind(err, "conflict") {
		t.Fatalf("stale updater created another plan: %v", err)
	}
}

func TestConcurrentUpdatersDoNotReplaceAnAlreadyUpdatedTarget(t *testing.T) {
	first := testUpdater(t, "1.0.0", "", runtime.GOOS)
	second := updaterAt(t, first.target, "1.0.0", "", runtime.GOOS)
	attachFixture(t, first, "1.1.0", []byte("first verified fixture"))
	attachFixture(t, second, "1.2.0", []byte("second verified fixture"))
	a, err := first.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, item := range []struct {
		u    *Updater
		plan *Plan
	}{{first, a}, {second, b}} {
		workers.Add(1)
		go func(u *Updater, plan *Plan) {
			defer workers.Done()
			<-start
			_, err := u.Apply(context.Background(), plan)
			results <- err
		}(item.u, item.plan)
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errs.IsKind(err, "conflict") {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent outcome: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestCancellationBeforeCommitLeavesOriginal(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	attachFixture(t, u, "1.1.0", []byte("new fixture"))
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := mustRead(t, u.target)
	syncDir := u.ops.syncDir
	u.ops.syncDir = func(path string) error {
		cancel()
		return syncDir(path)
	}
	result, err := u.Apply(ctx, plan)
	if !errors.Is(err, context.Canceled) || result.Changed ||
		!bytes.Equal(original, mustRead(t, u.target)) {
		t.Fatalf("cancellation changed target: %+v, %v", result, err)
	}
	for _, name := range directoryNames(t, filepath.Dir(u.target)) {
		if strings.HasPrefix(name, ".fam-update-") {
			t.Fatal("canceled staging was not cleaned")
		}
	}
}

func TestResultUsesConventionalJSONAndYAMLTags(t *testing.T) {
	result := Result{Status: "available", CurrentVersion: "1.0.0", TargetVersion: "1.1.0", UpdateAvailable: true}
	for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
		data, err := marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"currentVersion", "targetVersion", "updateAvailable", "changed"} {
			if !bytes.Contains(data, []byte(key)) {
				t.Fatalf("missing conventional key %s: %s", key, data)
			}
		}
		if bytes.Contains(data, []byte("backupPath")) {
			t.Fatal("empty backupPath was not omitted")
		}
	}
}
