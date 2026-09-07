// Package update verifies GitHub releases and replaces only the current FAM
// executable. Release data cannot choose credentials, destinations, or paths.
package update

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
)

// Options configures stable release selection and bounded GitHub requests.
// An empty Version selects latest. Zero Timeout and RetryDelay use five minutes
// and one second respectively; zero Retries disables retries.
type Options struct {
	CurrentVersion string
	Version        string
	Token          string
	Timeout        time.Duration
	Retries        int
	RetryDelay     time.Duration
}

// Result is display data, never authority to select a destination or artifact.
// Versions are canonical numeric major.minor.patch strings without a v prefix.
type Result struct {
	Status          string `json:"status" yaml:"status"`
	CurrentVersion  string `json:"currentVersion" yaml:"currentVersion"`
	TargetVersion   string `json:"targetVersion" yaml:"targetVersion"`
	Executable      string `json:"executable" yaml:"executable"`
	Asset           string `json:"asset" yaml:"asset"`
	BackupPath      string `json:"backupPath,omitempty" yaml:"backupPath,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable" yaml:"updateAvailable"`
	Changed         bool   `json:"changed" yaml:"changed"`
}

// Plan binds validated metadata to the updater and executable identity that
// checked it. Editing Result does not change Apply's behavior.
type Plan struct {
	Result Result

	owner    *Updater
	result   Result
	archive  asset
	checksum asset
	identity fileIdentity
}

// Updater has a dedicated GitHub transport, independent of Azure authentication.
type Updater struct {
	options  Options
	current  version
	wanted   version
	target   string
	goos     string
	arch     string
	identity fileIdentity
	client   *http.Client
	limits   budgets
	ops      commitOps
}

type budgets struct {
	metadata   int64
	checksum   int64
	archive    int64
	expanded   int64
	executable int64
	notice     int64
	entries    int
}

const maxOperationDuration = 30 * time.Minute

func defaultBudgets() budgets {
	return budgets{
		metadata: 1 << 20, checksum: 1 << 20, archive: 256 << 20,
		expanded: 512 << 20, executable: 256 << 20, notice: 8 << 20, entries: 32,
	}
}

// New validates options and captures the resolved current executable's identity.
// It performs no network requests or filesystem writes.
func New(options Options) (*Updater, error) {
	options, current, wanted, err := validateOptions(options)
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fileError("locate the running executable", err)
	}
	return newUpdater(options, current, wanted, executable, runtime.GOOS, runtime.GOARCH)
}

func validateOptions(options Options) (Options, version, version, error) {
	current, err := parseVersion(options.CurrentVersion)
	if err != nil {
		return options, version{}, version{}, errs.WithNextSteps(err,
			"Use a standalone FAM binary with a known stable config version before self-updating.")
	}
	var wanted version
	if options.Version != "" {
		wanted, err = parseVersion(options.Version)
		if err != nil {
			return options, current, wanted, err
		}
		if wanted.compare(current) < 0 {
			return options, current, wanted, errs.Config("update --version must not downgrade the installed version")
		}
	}
	if options.Timeout < 0 || options.Timeout > time.Hour ||
		options.Retries < 0 || options.Retries > 10 ||
		options.RetryDelay < 0 || options.RetryDelay > time.Minute {
		return options, current, wanted, errs.Config("update requires timeout in (0, 1h], retries in [0, 10], and retry delay in (0, 1m]; zero durations select defaults")
	}
	for _, c := range options.Token {
		if c <= ' ' || c >= 127 {
			return options, current, wanted, errs.Config("update token contains invalid header characters")
		}
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Minute
	}
	if options.RetryDelay == 0 {
		options.RetryDelay = time.Second
	}
	return options, current, wanted, nil
}

func newUpdater(options Options, current, wanted version, executable, goos, arch string) (*Updater, error) {
	if (goos != "windows" && goos != "linux" && goos != "darwin") ||
		(arch != "amd64" && arch != "arm64") {
		return nil, errs.Config("self-update supports windows, linux, and darwin on amd64 or arm64 only")
	}
	target, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fileError("resolve the running executable", err)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return nil, fileError("resolve the executable's absolute path", err)
	}
	u := &Updater{
		options: options, current: current, wanted: wanted, target: target,
		goos: goos, arch: arch, limits: defaultBudgets(), client: githubClient(),
		ops: nativeCommitOps(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	u.identity, err = identify(ctx, target, u.limits.executable)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// Check reads release metadata only. It does not download assets, create a
// lock, stage files, or modify the installation.
func (u *Updater) Check(ctx context.Context) (*Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, maxOperationDuration)
	defer cancel()
	if err := u.recheck(ctx, u.identity); err != nil {
		return nil, err
	}
	release, err := u.release(ctx)
	if err != nil {
		return nil, err
	}
	target, err := parseVersion(release.TagName)
	if err != nil {
		return nil, errs.Security("validated GitHub release has an invalid version")
	}
	archive, checksum, err := u.selectAssets(release, target)
	if err != nil {
		return nil, err
	}
	result := Result{
		Status: "available", CurrentVersion: u.current.String(),
		TargetVersion: target.String(), Executable: u.target, Asset: archive.Name,
		UpdateAvailable: target.compare(u.current) > 0,
	}
	switch target.compare(u.current) {
	case 0:
		result.Status = "up-to-date"
	case -1:
		if u.options.Version != "" {
			return nil, errs.Security("GitHub release would downgrade the installed version")
		}
		result.Status = "newer-installed"
	}
	if err := u.recheck(ctx, u.identity); err != nil {
		return nil, err
	}
	return &Plan{
		Result: result, result: result, owner: u, archive: archive,
		checksum: checksum, identity: u.identity,
	}, nil
}

// Apply downloads and verifies a checked release before taking the installation
// lock. Confirmation belongs to the caller. Once replacement begins, rollback
// completes independently of cancellation. Windows replacement is not crash
// atomic: a crash between renames requires manual recovery using recovery.json.
// A nonempty BackupPath identifies an original retained for recovery or cleanup
// after this process exits. The installation's lock file is never removed.
func (u *Updater) Apply(ctx context.Context, plan *Plan) (Result, error) {
	if plan == nil || plan.owner != u {
		return Result{}, errs.Security("update plan was not created by this updater")
	}
	result := plan.result
	ctx, cancel := context.WithTimeout(ctx, maxOperationDuration)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := u.recheck(ctx, plan.identity); err != nil {
		return result, err
	}
	if !result.UpdateAvailable {
		return result, nil
	}
	checksums, err := u.get(ctx, assetEndpoint(plan.checksum.ID), false, u.limits.checksum)
	if err != nil {
		return result, err
	}
	expected, err := checksumFor(checksums, plan.archive.Name)
	if err != nil {
		return result, err
	}
	archive, err := u.get(ctx, assetEndpoint(plan.archive.ID), false, u.limits.archive)
	if err != nil {
		return result, err
	}
	if sha256.Sum256(archive) != expected {
		return result, errs.Security("release archive SHA-256 does not match SHA256SUMS")
	}
	executable, err := u.extract(ctx, archive)
	if err != nil {
		return result, err
	}
	return u.install(ctx, plan, executable)
}

func (u *Updater) recheck(ctx context.Context, expected fileIdentity) error {
	actual, err := identify(ctx, u.target, u.limits.executable)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil || !expected.equal(actual) {
		return errs.WithNextSteps(errs.Conflict("installed executable changed since this updater started"),
			"Run the currently installed FAM executable again and request a fresh update check.")
	}
	return nil
}

func fileError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var typed *errs.FoundryAgentManagerError
	if errors.As(err, &typed) {
		return err
	}
	failure := errs.Config("cannot %s", operation)
	failure.Cause = err
	return errs.WithNextSteps(failure,
		"Ensure you own the installation directory and have permission to replace FAM; no elevation is performed.",
		"If a recovery directory remains, preserve its original executable until recovery is complete.")
}

func executableName(goos string) string {
	if goos == "windows" {
		return "fam.exe"
	}
	return "fam"
}

func archiveSuffix(goos, arch string) string {
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	return "_" + goos + "_" + arch + extension
}

func isSafeRootName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\:`) && name != "." && name != ".."
}
