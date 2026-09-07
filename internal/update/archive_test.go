package update

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func TestChecksumsRequireOneExactValidFilename(t *testing.T) {
	name := "fam_1.2.3_windows_arm64.zip"
	hash := sha256.Sum256([]byte("fixture"))
	for _, separator := range []string{"  ", " *"} {
		data := []byte(fmt.Sprintf("%X%s%s\r\n", hash, separator, name))
		got, err := checksumFor(data, name)
		if err != nil || got != hash {
			t.Fatalf("valid checksum rejected: %v", err)
		}
	}
	line := fmt.Sprintf("%x  %s\n", hash, name)
	for _, test := range []struct{ name, data string }{
		{"missing", ""},
		{"duplicate", line + line},
		{"nonhex", strings.Repeat("g", 64) + "  " + name + "\n"},
		{"short", strings.Repeat("a", 63) + "  " + name + "\n"},
		{"long", strings.Repeat("a", 65) + "  " + name + "\n"},
		{"substring", strings.ReplaceAll(line, name, "prefix-"+name)},
		{"path", strings.ReplaceAll(line, name, "./"+name)},
		{"trailing", strings.ReplaceAll(line, name, name+" ")},
		{"bad-separator", fmt.Sprintf("%x\t%s\n", hash, name)},
		{"ambiguous", line + strings.Repeat("z", 64) + "  " + name},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := checksumFor([]byte(test.data), name); !errs.IsKind(err, "security") {
				t.Fatalf("unsafe checksum accepted: %v", err)
			}
		})
	}
}

func TestArchivesExtractOnlyOneVerifiedRootExecutable(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			u := testUpdater(t, "1.0.0", "", goos)
			want := []byte("non-executable synthetic payload")
			got, err := u.extract(context.Background(), makeArchive(t, goos, standardEntries(goos, want)))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("extract = %q, %v", got, err)
			}
		})
	}
}

func TestArchivesRejectTraversalSpecialEntriesAndDuplicates(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		tests := []struct {
			name string
			edit func([]archiveEntry) []archiveEntry
		}{
			{"traversal", func(e []archiveEntry) []archiveEntry { e[0].name = "../fam"; return e }},
			{"nested", func(e []archiveEntry) []archiveEntry { e[0].name = "dir/fam"; return e }},
			{"absolute", func(e []archiveEntry) []archiveEntry { e[0].name = "/fam"; return e }},
			{"backslash", func(e []archiveEntry) []archiveEntry { e[0].name = `..\fam.exe`; return e }},
			{"drive", func(e []archiveEntry) []archiveEntry { e[0].name = `C:\fam.exe`; return e }},
			{"ads", func(e []archiveEntry) []archiveEntry { e[0].name = "fam.exe:payload"; return e }},
			{"case-alias", func(e []archiveEntry) []archiveEntry { e[0].name = strings.ToUpper(e[0].name); return e }},
			{"duplicate-executable", func(e []archiveEntry) []archiveEntry { return append(e, e[0]) }},
			{"duplicate-license", func(e []archiveEntry) []archiveEntry { return append(e, e[1]) }},
			{"missing-executable", func(e []archiveEntry) []archiveEntry { return e[1:] }},
			{"missing-notices", func(e []archiveEntry) []archiveEntry { return e[:2] }},
			{"empty-executable", func(e []archiveEntry) []archiveEntry { e[0].data = nil; return e }},
			{"unknown-entry", func(e []archiveEntry) []archiveEntry {
				return append(e, archiveEntry{name: "unexpected", data: []byte("extra"), mode: 0600})
			}},
			{"symlink", func(e []archiveEntry) []archiveEntry {
				e[0].mode, e[0].typeflag, e[0].link, e[0].data = os.ModeSymlink|0777, tar.TypeSymlink, "elsewhere", nil
				return e
			}},
			{"directory", func(e []archiveEntry) []archiveEntry {
				e[0].mode, e[0].typeflag, e[0].data = os.ModeDir|0755, tar.TypeDir, nil
				return e
			}},
			{"fifo", func(e []archiveEntry) []archiveEntry {
				e[0].mode, e[0].typeflag, e[0].data = os.ModeNamedPipe|0600, tar.TypeFifo, nil
				return e
			}},
		}
		for _, test := range tests {
			t.Run(goos+"/"+test.name, func(t *testing.T) {
				u := testUpdater(t, "1.0.0", "", goos)
				entries := test.edit(standardEntries(goos, []byte("fixture")))
				if _, err := u.extract(context.Background(), makeArchive(t, goos, entries)); !errs.IsKind(err, "security") {
					t.Fatalf("unsafe archive accepted: %v", err)
				}
			})
		}
	}
}

func TestTarRejectsHardLinksAndHiddenExtensionRecords(t *testing.T) {
	for _, entry := range []archiveEntry{
		{name: "fam", typeflag: tar.TypeLink, link: "LICENSE", mode: 0755},
		{name: strings.Repeat("long", 100), data: []byte("hidden GNU/PAX header"), mode: 0755},
	} {
		u := testUpdater(t, "1.0.0", "", "linux")
		entries := standardEntries("linux", []byte("fixture"))
		entries[0] = entry
		if _, err := u.extract(context.Background(), makeArchive(t, "linux", entries)); !errs.IsKind(err, "security") {
			t.Fatalf("link or extension accepted: %v", err)
		}
	}
}

func TestArchivesEnforceExecutableNoticeTotalAndCountBudgets(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		for _, test := range []struct {
			name string
			edit func(*Updater)
		}{
			{"executable", func(u *Updater) { u.limits.executable = 6 }},
			{"notice", func(u *Updater) { u.limits.notice = 2 }},
			{"total", func(u *Updater) { u.limits.expanded = 6 }},
			{"count", func(u *Updater) { u.limits.entries = 2 }},
		} {
			t.Run(goos+"/"+test.name, func(t *testing.T) {
				u := testUpdater(t, "1.0.0", "", goos)
				test.edit(u)
				data := makeArchive(t, goos, standardEntries(goos, []byte("fixture")))
				if _, err := u.extract(context.Background(), data); !errs.IsKind(err, "security") {
					t.Fatalf("budget bypassed: %v", err)
				}
			})
		}
	}
}

func TestZIPValidatesCRCsIncludingSkippedNoticesAndTruncation(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", "windows")
	original := makeArchive(t, "windows", standardEntries("windows", []byte("payload-for-crc")))
	for _, needle := range []string{"payload-for-crc", "offline license fixture", "offline notices fixture"} {
		data := bytes.Clone(original)
		index := bytes.Index(data, []byte(needle))
		if index < 0 {
			t.Fatal("fixture payload missing")
		}
		data[index] ^= 1
		if _, err := u.extract(context.Background(), data); !errs.IsKind(err, "security") {
			t.Fatalf("CRC corruption ignored for %s: %v", needle, err)
		}
	}
	for _, length := range []int{0, 10, len(original) / 2, len(original) - 1} {
		if _, err := u.extract(context.Background(), original[:length]); !errs.IsKind(err, "security") {
			t.Fatalf("truncated ZIP accepted at %d: %v", length, err)
		}
	}
}

func TestZIPRejectsForgedCountsBeforeCentralDirectoryAllocation(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", "windows")
	original := makeArchive(t, "windows", standardEntries("windows", []byte("fixture")))
	for _, count := range []uint16{0, 1, 32, 65535} {
		data := bytes.Clone(original)
		end := len(data) - 22
		binary.LittleEndian.PutUint16(data[end+8:], count)
		binary.LittleEndian.PutUint16(data[end+10:], count)
		if _, err := u.extract(context.Background(), data); !errs.IsKind(err, "security") {
			t.Fatalf("forged central-directory count accepted: %d %v", count, err)
		}
	}
}

func TestTarExhaustsGzipAndRejectsMissingTerminatorsOrTrailingData(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", "linux")
	raw := tarBytes(t, standardEntries("linux", []byte("payload")))
	valid := gzipBytes(t, raw)
	badCRC := bytes.Clone(valid)
	badCRC[len(badCRC)-8] ^= 1
	tests := map[string][]byte{
		"bad-gzip-crc":        badCRC,
		"truncated-gzip":      valid[:len(valid)-1],
		"missing-terminators": gzipBytes(t, raw[:len(raw)-1024]),
		"one-terminator":      gzipBytes(t, raw[:len(raw)-512]),
		"trailing-tar":        gzipBytes(t, append(bytes.Clone(raw), []byte("unexpected")...)),
		"second-member":       append(bytes.Clone(valid), gzipBytes(t, []byte("unexpected"))...),
		"trailing-gzip-junk":  append(bytes.Clone(valid), []byte("junk")...),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := u.extract(context.Background(), data); !errs.IsKind(err, "security") {
				t.Fatalf("invalid tar/gzip accepted: %v", err)
			}
		})
	}
	u.limits.expanded = int64(len(raw))
	oversizedPadding := gzipBytes(t, append(bytes.Clone(raw), make([]byte, 512)...))
	if _, err := u.extract(context.Background(), oversizedPadding); !errs.IsKind(err, "security") {
		t.Fatalf("padding evaded expanded budget: %v", err)
	}
}

func TestArchiveReadHonorsCancellation(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		u := testUpdater(t, "1.0.0", "", goos)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := u.extract(ctx, makeArchive(t, goos, standardEntries(goos, []byte("fixture"))))
		if err != context.Canceled {
			t.Fatalf("cancellation ignored: %v", err)
		}
	}
}
