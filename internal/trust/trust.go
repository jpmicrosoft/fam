// Package trust holds the operator-controlled approvals that gate credential-bearing
// and data-egress destinations.
//
// Manifests are untrusted input: a crafted manifest can name any APIM gateway,
// OpenAPI server, MCP server, or managed-identity audience. Cloud suffix checks
// (see internal/netcheck) only prove that a host belongs to an Azure service
// family, not that the operator intended to send this project's subscription key
// or managed-identity token there. Approvals therefore never come from the
// manifest; they come from CLI flags or environment variables owned by the
// operator or CI system, are exact (no wildcards), and are enforced before any
// Azure mutation or secret resolution.
package trust

import (
	"fmt"
	"net/url"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

// Environment variables that mirror the repeatable approval flags for CI use.
const (
	EnvAPIMHosts = "FOUNDRY_AGENT_MANAGER_TRUSTED_APIM_HOSTS"
	EnvToolHosts = "FOUNDRY_AGENT_MANAGER_TRUSTED_TOOL_HOSTS"
	EnvAudiences = "FOUNDRY_AGENT_MANAGER_TRUSTED_MANAGED_IDENTITY_AUDIENCES"
)

// Flag names of the repeatable approval flags.
const (
	FlagAPIMHost = "trusted-apim-host"
	FlagToolHost = "trusted-tool-host"
	FlagAudience = "trusted-managed-identity-audience"
)

// Options carries the raw approval values supplied by flags and environment variables.
type Options struct {
	APIMHosts []string
	ToolHosts []string
	Audiences []string

	// File, when non-nil, is a decoded trust policy file (see LoadPolicyFile)
	// whose approvals are merged with the fields above. It is carried
	// separately, rather than pre-merged by the caller, purely so a
	// validation failure can be attributed to the file and field it came
	// from instead of to a flag name.
	File *FileApprovals
}

// Approvals is an immutable, normalized set of operator approvals.
type Approvals struct {
	apimHosts map[string]struct{}
	toolHosts map[string]struct{}
	audiences map[string]struct{}
}

// New normalizes and validates operator approvals. Wildcards are rejected.
func New(options Options) (Approvals, error) {
	apimHosts, err := hostSet(options.APIMHosts, FlagAPIMHost, EnvAPIMHosts)
	if err != nil {
		return Approvals{}, err
	}
	toolHosts, err := hostSet(options.ToolHosts, FlagToolHost, EnvToolHosts)
	if err != nil {
		return Approvals{}, err
	}
	audiences, err := audienceSet(options.Audiences)
	if err != nil {
		return Approvals{}, err
	}
	if options.File != nil {
		fileAPIMHosts, err := fileHostSet(options.File.APIMHosts, options.File.Path, "apimHosts")
		if err != nil {
			return Approvals{}, err
		}
		fileToolHosts, err := fileHostSet(options.File.ToolHosts, options.File.Path, "toolHosts")
		if err != nil {
			return Approvals{}, err
		}
		fileAudiences, err := fileAudienceSet(options.File.Audiences, options.File.Path, "audiences")
		if err != nil {
			return Approvals{}, err
		}
		mergeSet(apimHosts, fileAPIMHosts)
		mergeSet(toolHosts, fileToolHosts)
		mergeSet(audiences, fileAudiences)
	}
	return Approvals{apimHosts: apimHosts, toolHosts: toolHosts, audiences: audiences}, nil
}

func mergeSet(dst, src map[string]struct{}) {
	for key := range src {
		dst[key] = struct{}{}
	}
}

// SplitList splits an environment variable into individual approval values.
func SplitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// RequireAPIMHost fails closed unless the APIM destination host is exactly approved.
func (a Approvals) RequireAPIMHost(rawURL, field string) error {
	return a.requireHost(a.apimHosts, rawURL, field, FlagAPIMHost, EnvAPIMHosts)
}

// RequireToolHost fails closed unless an external tool destination host is exactly approved.
func (a Approvals) RequireToolHost(rawURL, field string) error {
	return a.requireHost(a.toolHosts, rawURL, field, FlagToolHost, EnvToolHosts)
}

// RequireAudience fails closed unless a managed-identity audience is a built-in
// cloud default or is exactly approved by the operator.
func (a Approvals) RequireAudience(audience, field string, builtIn []string) error {
	normalized, err := normalizeAudience(audience, field)
	if err != nil {
		return err
	}
	for _, candidate := range builtIn {
		if builtInAudience, err := normalizeAudience(candidate, field); err == nil && builtInAudience == normalized {
			return nil
		}
	}
	if _, ok := a.audiences[normalized]; ok {
		return nil
	}
	return errs.Security(
		"%s: managed-identity audience %q is not an approved token audience. "+
			"Audience approvals come from --%s or %s and never from the manifest; "+
			"approve this exact audience only after confirming it should receive this project's identity token.",
		field, audience, FlagAudience, EnvAudiences,
	)
}

// ApprovedHostCount reports how many exact approvals were supplied, for diagnostics.
func (a Approvals) ApprovedHostCount() int {
	return len(a.apimHosts) + len(a.toolHosts)
}

func (a Approvals) requireHost(approved map[string]struct{}, rawURL, field, flag, env string) error {
	key, host, err := destinationKey(rawURL, field)
	if err != nil {
		return err
	}
	if _, ok := approved[key]; ok {
		return nil
	}
	return errs.Security(
		"%s: destination host %q is not approved for this deployment. "+
			"Destination approvals come from --%s or %s and never from the manifest; "+
			"approve this exact host only after confirming that you own it and that it should "+
			"receive this deployment's credentials and data.",
		field, host, flag, env,
	)
}

// destinationKey validates a manifest-supplied URL and returns its normalized
// comparison key plus the host for diagnostics.
func destinationKey(rawURL, field string) (string, string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", "", errs.Security("%s: destination URL is empty", field)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", "", errs.Security("%s: invalid destination URL %q: %v", field, rawURL, err)
	}
	if !parsed.IsAbs() {
		return "", "", errs.Security(
			"%s: destination %q must be an absolute https URL; relative destinations cannot be approved",
			field, rawURL,
		)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", "", errs.Security("%s: destination %q must use https://", field, rawURL)
	}
	if parsed.User != nil {
		return "", "", errs.Security("%s: destination %q must not embed credentials", field, rawURL)
	}
	host := normalizeHost(parsed.Hostname())
	if host == "" {
		return "", "", errs.Security("%s: destination %q has no host", field, rawURL)
	}
	if !isASCII(host) {
		return "", "", errs.Security(
			"%s: destination host %q is not ASCII; supply the punycode form so approvals cannot be spoofed",
			field, parsed.Hostname(),
		)
	}
	return joinHostPort(host, parsed.Port()), host, nil
}

func hostSet(values []string, flag, env string) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	for _, value := range values {
		key, err := normalizeApprovedHost(value, flag, env)
		if err != nil {
			return nil, err
		}
		result[key] = struct{}{}
	}
	return result, nil
}

func audienceSet(values []string) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	for _, value := range values {
		normalized, err := normalizeApprovedAudience(value)
		if err != nil {
			return nil, err
		}
		result[normalized] = struct{}{}
	}
	return result, nil
}

// fileHostSet validates trust-policy-file host entries with the same rules as
// hostSet, attributing failures to the file and field they came from.
func fileHostSet(values []string, path, field string) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	for index, value := range values {
		key, err := normalizeFileHost(value, path, field, index)
		if err != nil {
			return nil, err
		}
		result[key] = struct{}{}
	}
	return result, nil
}

// fileAudienceSet validates trust-policy-file audience entries with the same
// rules as audienceSet, attributing failures to the file and field they came from.
func fileAudienceSet(values []string, path, field string) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	for index, value := range values {
		normalized, err := normalizeFileAudience(value, path, field, index)
		if err != nil {
			return nil, err
		}
		result[normalized] = struct{}{}
	}
	return result, nil
}

// normalizeFileHost accepts only an exact host with an optional port, exactly
// like normalizeApprovedHost, but attributes failures to the trust policy
// file entry that produced them instead of to a flag.
func normalizeFileHost(value, path, field string, index int) (string, error) {
	label := fmt.Sprintf("trust policy file %s: %s[%d]", path, field, index)
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errs.Config("%s must not be empty", label)
	}
	if strings.Contains(trimmed, "*") || strings.HasPrefix(trimmed, ".") {
		return "", errs.Security(
			"%s %q: wildcard and suffix approvals are not supported; approve one exact host",
			label, value,
		)
	}
	if strings.Contains(trimmed, "://") || strings.ContainsAny(trimmed, "/@?# \t") {
		return "", errs.Config(
			"%s %q must be a bare host with an optional port, for example contoso.azure-api.net or contoso.example.com:8443",
			label, value,
		)
	}
	parsed, err := url.Parse("https://" + trimmed)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errs.Config("%s %q is not a valid host", label, value)
	}
	host := normalizeHost(parsed.Hostname())
	if host == "" {
		return "", errs.Config("%s %q is not a valid host", label, value)
	}
	if !isASCII(host) {
		return "", errs.Security(
			"%s %q must be ASCII; supply the punycode form so lookalike hosts cannot be approved",
			label, value,
		)
	}
	return joinHostPort(host, parsed.Port()), nil
}

// normalizeFileAudience accepts only an exact audience URI or App ID URI,
// exactly like normalizeApprovedAudience, but attributes failures to the
// trust policy file entry that produced them instead of to a flag.
func normalizeFileAudience(value, path, field string, index int) (string, error) {
	label := fmt.Sprintf("trust policy file %s: %s[%d]", path, field, index)
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errs.Config("%s must not be empty", label)
	}
	if strings.Contains(trimmed, "*") {
		return "", errs.Security(
			"%s %q: wildcard audiences are not supported; approve one exact audience",
			label, value,
		)
	}
	return normalizeAudience(trimmed, label)
}

// normalizeApprovedHost accepts only an exact host with an optional port.
func normalizeApprovedHost(value, flag, env string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errs.Config("--%s (or %s) must not contain empty values", flag, env)
	}
	if strings.Contains(trimmed, "*") || strings.HasPrefix(trimmed, ".") {
		return "", errs.Security(
			"--%s %q: wildcard and suffix approvals are not supported; approve one exact host",
			flag, value,
		)
	}
	if strings.Contains(trimmed, "://") || strings.ContainsAny(trimmed, "/@?# \t") {
		return "", errs.Config(
			"--%s %q must be a bare host with an optional port, for example contoso.azure-api.net or contoso.example.com:8443",
			flag, value,
		)
	}
	parsed, err := url.Parse("https://" + trimmed)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errs.Config("--%s %q is not a valid host", flag, value)
	}
	host := normalizeHost(parsed.Hostname())
	if host == "" {
		return "", errs.Config("--%s %q is not a valid host", flag, value)
	}
	if !isASCII(host) {
		return "", errs.Security(
			"--%s %q must be ASCII; supply the punycode form so lookalike hosts cannot be approved",
			flag, value,
		)
	}
	return joinHostPort(host, parsed.Port()), nil
}

// normalizeApprovedAudience accepts only an exact audience URI or App ID URI.
func normalizeApprovedAudience(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errs.Config("--%s (or %s) must not contain empty values", FlagAudience, EnvAudiences)
	}
	if strings.Contains(trimmed, "*") {
		return "", errs.Security(
			"--%s %q: wildcard audiences are not supported; approve one exact audience",
			FlagAudience, value,
		)
	}
	return normalizeAudience(trimmed, "--"+FlagAudience)
}

// normalizeAudience produces the comparison form of a managed-identity audience.
func normalizeAudience(value, field string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errs.Security("%s: managed-identity audience is empty", field)
	}
	lower := strings.ToLower(strings.TrimRight(trimmed, "/"))
	if strings.HasSuffix(lower, "/.default") {
		return "", errs.Config(
			"%s: managed-identity audience %q is an OAuth scope; use the resource URI without /.default",
			field, value,
		)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", errs.Security("%s: managed-identity audience %q is invalid: %v", field, value, err)
	}
	if parsed.User != nil {
		return "", errs.Security("%s: managed-identity audience %q must not embed credentials", field, value)
	}
	if !isASCII(lower) {
		return "", errs.Security(
			"%s: managed-identity audience %q must be ASCII so lookalike audiences cannot be approved",
			field, value,
		)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return lower, nil
	}
	normalized := *parsed
	normalized.Scheme = strings.ToLower(parsed.Scheme)
	normalized.Host = joinHostPort(normalizeHost(parsed.Hostname()), parsed.Port())
	normalized.Path = strings.TrimRight(parsed.Path, "/")
	return strings.ToLower(normalized.String()), nil
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(host), "."))
}

// joinHostPort treats an omitted port and the default https port as equivalent.
func joinHostPort(host, port string) string {
	if port == "" || port == "443" {
		return host
	}
	return host + ":" + port
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}
