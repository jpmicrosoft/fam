// Package netcheck validates manifest-controlled URLs and file paths.
//
// Several manifest fields flow into token-authenticated HTTP sinks:
//   - project.endpoint / project.account_endpoint receive an AAD bearer token.
//   - apim.target / apim.gateway_url become the target of a connection that stores
//     the APIM subscription key.
//
// Left unvalidated, a crafted manifest could redirect credentials to an attacker host.
// These helpers host-pin URLs to an Azure allow-list and contain file reads to the
// manifest directory.
package netcheck

import (
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

const (
	apimEnv = "FOUNDRY_AGENT_MANAGER_ALLOWED_APIM_SUFFIXES"
)

// MaxContainedFileBytes bounds manifest-referenced files (instructions, OpenAPI
// specs) so a hostile or accidental large file cannot exhaust memory.
const MaxContainedFileBytes = 8 << 20

// MaxGroundingFileBytes is the documented Foundry file-search upload limit.
const MaxGroundingFileBytes int64 = 512 << 20

var defaultEndpointSuffixes = []string{
	"services.ai.azure.com",
	"cognitiveservices.azure.com",
	"openai.azure.com",
}

var defaultAPIMSuffixes = []string{
	"azure-api.net",
}

func suffixes(envVar string, defaults []string) []string {
	merged := make([]string, len(defaults))
	copy(merged, defaults)
	extra := os.Getenv(envVar)
	if extra != "" {
		for _, s := range strings.Split(extra, ",") {
			s = strings.TrimSpace(s)
			s = strings.ToLower(s)
			s = strings.TrimLeft(s, ".")
			if s != "" {
				merged = append(merged, s)
			}
		}
	}
	// de-dupe preserving order
	seen := map[string]bool{}
	result := merged[:0]
	for _, s := range merged {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

func hostAllowed(host string, suffixList []string) bool {
	host = strings.ToLower(strings.TrimRight(host, "."))
	for _, s := range suffixList {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// ValidateHTTPSHost checks that a URL uses https with an allow-listed host.
func ValidateHTTPSHost(rawURL, field, envVar string, defaults []string) (string, error) {
	return validateHTTPSHost(rawURL, field, suffixes(envVar, defaults), envVar)
}

func validateHTTPSHost(rawURL, field string, allowed []string, extensionEnv string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", errs.Security("%s: invalid URL %q: %v", field, rawURL, err)
	}
	if u.Scheme != "https" {
		scheme := u.Scheme
		if scheme == "" {
			scheme = "no"
		}
		return "", errs.Security("%s: must use https:// (got %s-scheme URL %q)", field, scheme, rawURL)
	}
	if u.User != nil {
		return "", errs.Security("%s: URL must not embed credentials (%q)", field, rawURL)
	}
	host := u.Hostname()
	if !hostAllowed(host, allowed) {
		extension := ""
		if extensionEnv != "" {
			extension = "; extend via " + extensionEnv
		}
		return "", errs.Security(
			"%s: host %q is not an allowed Azure host (allowed suffixes: %s%s). Rejected to avoid sending credentials to an untrusted host.",
			field, host, strings.Join(allowed, ", "), extension,
		)
	}
	return rawURL, nil
}

// ValidateFoundryEndpoint host-pins a Foundry account/project endpoint.
func ValidateFoundryEndpoint(rawURL, field string) (string, error) {
	return validateHTTPSHost(rawURL, field, defaultEndpointSuffixes, "")
}

// ValidateFoundryEndpointForSuffixes validates an endpoint for a selected Azure cloud.
func ValidateFoundryEndpointForSuffixes(rawURL, field string, allowedSuffixes []string) (string, error) {
	return validateHTTPSHost(rawURL, field, allowedSuffixes, "")
}

// ValidateAPIMTarget host-pins an APIM gateway/target URL.
func ValidateAPIMTarget(rawURL, field string) (string, error) {
	return ValidateAPIMTargetForSuffixes(rawURL, field, defaultAPIMSuffixes)
}

// ValidateAPIMTargetForSuffixes validates an APIM target for a selected Azure cloud.
func ValidateAPIMTargetForSuffixes(rawURL, field string, allowedSuffixes []string) (string, error) {
	validated, err := ValidateHTTPSHost(rawURL, field, apimEnv, allowedSuffixes)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(validated)
	host := u.Hostname()
	if containsSuffix(allowedSuffixes, "azure-api.net") && hostAllowed(host, []string{"azure-api.us"}) {
		return "", errs.Security("%s: host %q belongs to Azure Government, not Azure public cloud", field, host)
	}
	return validated, nil
}

// ValidateKeyVaultURL validates a Key Vault secret URL for a selected Azure cloud.
func ValidateKeyVaultURL(rawURL, field string, allowedSuffixes []string) (string, error) {
	return validateHTTPSHost(rawURL, field, allowedSuffixes, "")
}

// ValidateStorageQueueEndpointForSuffixes validates an Azure Function queue endpoint.
func ValidateStorageQueueEndpointForSuffixes(rawURL, field string, allowedSuffixes []string) (string, error) {
	return validateHTTPSHost(rawURL, field, allowedSuffixes, "")
}

// ValidateMonitorIngestionEndpointForSuffixes validates a base DCR or DCE logs
// ingestion endpoint before a bearer token or receipt data is sent.
func ValidateMonitorIngestionEndpointForSuffixes(
	rawURL string,
	field string,
	allowedSuffixes []string,
) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	validated, err := validateHTTPSHost(rawURL, field, allowedSuffixes, "")
	if err != nil {
		return "", err
	}
	u, err := url.Parse(validated)
	if err != nil {
		return "", errs.Security("%s: invalid URL %q: %v", field, rawURL, err)
	}
	if u.Path != "" && u.Path != "/" {
		return "", errs.Security("%s: must be a base Logs ingestion endpoint without a path", field)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errs.Security("%s: must not contain a query string or fragment", field)
	}
	return strings.TrimRight(validated, "/"), nil
}

func containsSuffix(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimLeft(value, "."), expected) {
			return true
		}
	}
	return false
}

// ValidateRelativeFileReference rejects absolute and parent-traversing paths.
func ValidateRelativeFileReference(relative, field string) error {
	isDrivePath := len(relative) >= 2 &&
		((relative[0] >= 'A' && relative[0] <= 'Z') || (relative[0] >= 'a' && relative[0] <= 'z')) &&
		relative[1] == ':'
	if filepath.IsAbs(relative) || strings.HasPrefix(relative, "/") || strings.HasPrefix(relative, `\`) || isDrivePath || strings.Contains(relative, "..") {
		return errs.Security(
			"%s: %q must be a relative path without absolute prefixes or '..' traversal.",
			field, relative,
		)
	}
	return nil
}

// RequireContainedFile resolves relative under base and ensures it does not escape.
// Blocks absolute paths, drive-letter paths, ".." traversal and symlink escapes.
//
// The returned path is only safe to display. Reading it with os.ReadFile is
// racy (check-then-open); use ReadContainedFile instead.
func RequireContainedFile(base, relative, field string) (string, error) {
	if err := ValidateRelativeFileReference(relative, field); err != nil {
		return "", err
	}

	baseResolved, baseReal, err := containedBase(base, field)
	if err != nil {
		return "", err
	}

	candidate, err := filepath.Abs(filepath.Join(baseResolved, relative))
	if err != nil {
		return "", errs.Security("%s: cannot resolve path: %v", field, err)
	}
	candidateReal, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		candidateReal = candidate
	}

	rel, err := filepath.Rel(baseReal, candidateReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errs.Security(
			"%s: %q resolves outside the manifest directory (%s); absolute paths, '..' traversal, and symlink escapes are not allowed.",
			field, relative, baseResolved,
		)
	}
	return candidateReal, nil
}

// containedBase resolves the base directory both literally and through symlinks.
func containedBase(base, field string) (string, string, error) {
	baseResolved, err := filepath.Abs(base)
	if err != nil {
		return "", "", errs.Security("%s: cannot resolve base directory: %v", field, err)
	}
	baseReal, err := filepath.EvalSymlinks(baseResolved)
	if err != nil {
		baseReal = baseResolved
	}
	return baseResolved, baseReal, nil
}

// ReadContainedFile reads a manifest-relative file without a check-then-open race.
//
// The path shape is validated first (no absolute prefixes, drive letters, or
// ".." traversal), then the read happens through os.Root: the kernel resolves
// every path component against a directory handle, so a symlink, Windows
// junction, or directory swapped in after validation cannot redirect the read
// outside the manifest directory. Reads are bounded by MaxContainedFileBytes.
func ReadContainedFile(base, relative, field string) ([]byte, error) {
	file, info, err := OpenContainedFile(base, relative, field, MaxContainedFileBytes)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, MaxContainedFileBytes+1))
	if err != nil {
		return nil, errs.Config("%s: failed to read %q: %v", field, relative, err)
	}
	if len(data) > MaxContainedFileBytes {
		return nil, errs.Config(
			"%s: %q exceeds the %d byte limit",
			field, relative, int64(MaxContainedFileBytes),
		)
	}
	if int64(len(data)) != info.Size() {
		return nil, errs.Security(
			"%s: %q changed while it was being read; retry with a stable file",
			field,
			relative,
		)
	}
	return data, nil
}

// OpenContainedFile opens a manifest-relative regular file through os.Root so
// callers can stream large inputs without weakening path containment.
func OpenContainedFile(
	base string,
	relative string,
	field string,
	maxBytes int64,
) (*os.File, fs.FileInfo, error) {
	if err := ValidateRelativeFileReference(relative, field); err != nil {
		return nil, nil, err
	}
	if _, err := RequireContainedFile(base, relative, field); err != nil {
		return nil, nil, err
	}
	_, baseReal, err := containedBase(base, field)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(baseReal)
	if err != nil {
		return nil, nil, errs.Security("%s: cannot open the manifest directory safely: %v", field, err)
	}

	file, err := root.Open(filepath.ToSlash(filepath.Clean(relative)))
	_ = root.Close()
	if err != nil {
		return nil, nil, containedReadError(err, relative, field)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, errs.Config("%s: cannot inspect %q: %v", field, relative, err)
	}
	if info.IsDir() {
		file.Close()
		return nil, nil, errs.Config("%s: %q is a directory", field, relative)
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errs.Config("%s: %q is not a regular file", field, relative)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		file.Close()
		return nil, nil, errs.Config(
			"%s: %q is %d bytes which exceeds the %d byte limit",
			field, relative, info.Size(), maxBytes,
		)
	}
	return file, info, nil
}

// WriteContainedFileExclusive writes a new workspace-relative file through
// os.Root so symlink and junction swaps cannot redirect the destination.
func WriteContainedFileExclusive(
	base string,
	relative string,
	field string,
	data []byte,
	maxBytes int64,
) (string, error) {
	if int64(len(data)) > maxBytes {
		return "", errs.Config("%s: output exceeds the %d byte limit", field, maxBytes)
	}
	if err := ValidateRelativeFileReference(relative, field); err != nil {
		return "", err
	}
	displayPath, err := RequireContainedFile(base, relative, field)
	if err != nil {
		return "", err
	}
	_, baseReal, err := containedBase(base, field)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(baseReal)
	if err != nil {
		return "", errs.Security("%s: cannot open the workspace directory safely: %v", field, err)
	}
	defer root.Close()
	rootPath := filepath.ToSlash(filepath.Clean(relative))
	file, err := root.OpenFile(rootPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", containedWriteError(err, relative, field)
	}
	remove := true
	defer func() {
		if remove {
			_ = root.Remove(rootPath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", errs.Config("%s: failed to write %q: %v", field, relative, err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", errs.Config("%s: failed to flush %q: %v", field, relative, err)
	}
	if err := file.Close(); err != nil {
		return "", errs.Config("%s: failed to close %q: %v", field, relative, err)
	}
	remove = false
	return displayPath, nil
}

func containedWriteError(err error, relative, field string) error {
	switch {
	case errors.Is(err, fs.ErrExist):
		return errs.Config("%s: %q already exists; choose a new output path", field, relative)
	case errors.Is(err, fs.ErrNotExist):
		return errs.Config("%s: the parent directory for %q does not exist", field, relative)
	case errors.Is(err, fs.ErrPermission):
		return errs.Config("%s: %q cannot be written: %v", field, relative, err)
	default:
		return errs.Security(
			"%s: %q could not be created inside the workspace (%v); "+
				"absolute paths, '..' traversal, symlink, and junction escapes are not allowed.",
			field,
			relative,
			err,
		)
	}
}

// containedReadError fails closed: anything other than a plain missing file or
// permission problem is treated as a containment failure.
func containedReadError(err error, relative, field string) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errs.Config("%s: %q does not exist in the manifest directory", field, relative)
	case errors.Is(err, fs.ErrPermission):
		return errs.Config("%s: %q cannot be read: %v", field, relative, err)
	default:
		return errs.Security(
			"%s: %q could not be opened inside the manifest directory (%v); "+
				"absolute paths, '..' traversal, symlink, and junction escapes are not allowed.",
			field, relative, err,
		)
	}
}
