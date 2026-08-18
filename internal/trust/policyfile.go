package trust

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	errs "foundry-agent-manager/internal/errors"

	"gopkg.in/yaml.v3"
)

// FlagPolicyFile is the flag name for an optional trust policy file.
// EnvPolicyFile is its CI environment variable equivalent.
const (
	FlagPolicyFile = "trust-file"
	EnvPolicyFile  = "FOUNDRY_AGENT_MANAGER_TRUST_FILE"
)

// FileApprovals is the decoded, not-yet-validated content of a trust policy
// file: the same three approval categories as the repeatable flags, meant for
// a reviewable, version-controlled policy instead of a long flag or
// environment-variable list. New validates its entries with the same rules
// as the repeatable flags (no wildcards, ASCII only, exact host/audience).
type FileApprovals struct {
	Path      string
	APIMHosts []string
	ToolHosts []string
	Audiences []string
}

// policyFileFields lists the only recognized top-level trust policy file
// fields, so a typo (for example "apimHost") is rejected instead of silently
// ignored.
var policyFileFields = map[string]struct{}{
	"apimHosts": {},
	"toolHosts": {},
	"audiences": {},
}

// LoadPolicyFile reads and decodes a JSON or YAML trust policy file from an
// operator-supplied path.
//
// The path always comes from the --trust-file flag or the FOUNDRY_AGENT_MANAGER_TRUST_FILE
// environment variable, never from a manifest, so it is read directly, the
// same way the APIM secret file source is: it does not need the
// rooted-directory containment that manifest-referenced files require.
//
// Values are returned verbatim; New performs the same validation as the
// repeatable approval flags (no wildcards, ASCII only, exact host or
// audience) before any entry is trusted.
func LoadPolicyFile(path string) (FileApprovals, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileApprovals{}, errs.Config("failed to read trust policy file %s: %v", path, err)
	}
	raw, err := decodePolicyDocument(data)
	if err != nil {
		return FileApprovals{}, errs.Config("trust policy file %s is not valid JSON or YAML: %v", path, err)
	}
	if raw == nil {
		return FileApprovals{}, errs.Config("trust policy file %s must be a mapping at the top level", path)
	}

	var unknown []string
	for key := range raw {
		if _, ok := policyFileFields[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return FileApprovals{}, errs.Config(
			"trust policy file %s has unrecognized field(s) %s; expected only apimHosts, toolHosts, and/or audiences",
			path, strings.Join(unknown, ", "),
		)
	}

	apimHosts, err := policyFileList(raw, "apimHosts", path)
	if err != nil {
		return FileApprovals{}, err
	}
	toolHosts, err := policyFileList(raw, "toolHosts", path)
	if err != nil {
		return FileApprovals{}, err
	}
	audiences, err := policyFileList(raw, "audiences", path)
	if err != nil {
		return FileApprovals{}, err
	}
	return FileApprovals{Path: path, APIMHosts: apimHosts, ToolHosts: toolHosts, Audiences: audiences}, nil
}

// decodePolicyDocument tries JSON first, then YAML, mirroring
// internal/config.LoadManifest so both formats behave consistently across
// the CLI. gopkg.in/yaml.v3 decodes YAML mappings into map[string]interface{}
// directly, and also accepts plain JSON, so a single YAML decode is enough
// once JSON decoding has been tried.
func decodePolicyDocument(data []byte) (map[string]interface{}, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err == nil {
		return doc, nil
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// policyFileList reads an optional list-of-strings field from a decoded
// trust policy document, rejecting anything that is present but not a list
// of strings instead of silently coercing or ignoring it.
func policyFileList(raw map[string]interface{}, field, path string) ([]string, error) {
	value, ok := raw[field]
	if !ok || value == nil {
		return nil, nil
	}
	items, ok := value.([]interface{})
	if !ok {
		return nil, errs.Config("trust policy file %s: %s must be a list of strings", path, field)
	}
	result := make([]string, 0, len(items))
	for index, item := range items {
		str, ok := item.(string)
		if !ok {
			return nil, errs.Config("trust policy file %s: %s[%d] must be a string, not %T", path, field, index, item)
		}
		result = append(result, str)
	}
	return result, nil
}
