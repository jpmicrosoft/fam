package apicenter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	RegistryPath      = "/workspaces/default/v0.1/servers"
	MaxRegistryBytes  = int64(8 << 20)
	apiCenterHostTail = ".azure-apicenter.ms"
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type RegistryResult struct {
	Endpoint      string      `json:"endpoint" yaml:"endpoint"`
	Authenticated bool        `json:"authenticated" yaml:"authenticated"`
	Search        string      `json:"search,omitempty" yaml:"search,omitempty"`
	Matches       int         `json:"matches" yaml:"matches"`
	Payload       interface{} `json:"payload" yaml:"payload"`
}

func RegistryURL(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", errs.Config("failed to parse API Center endpoint: %v", err)
	}
	if parsed.Scheme != "https" ||
		parsed.Hostname() == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return "", errs.Security(
			"API Center endpoint must be an absolute HTTPS origin without credentials, query, or fragment",
		)
	}
	host := strings.ToLower(parsed.Hostname())
	if !strings.HasSuffix(host, apiCenterHostTail) ||
		strings.TrimSuffix(host, apiCenterHostTail) == "" {
		return "", errs.Security(
			"API Center endpoint host must end in %s",
			apiCenterHostTail,
		)
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if path != "" && path != RegistryPath {
		return "", errs.Config(
			"API Center endpoint path must be empty or %s",
			RegistryPath,
		)
	}
	parsed.Path = RegistryPath
	parsed.RawPath = ""
	return parsed.String(), nil
}

func QueryRegistryContext(
	ctx context.Context,
	endpoint string,
	search string,
	tokenScope string,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (RegistryResult, error) {
	registryURL, err := RegistryURL(endpoint)
	if err != nil {
		return RegistryResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, registryURL, nil)
	if err != nil {
		return RegistryResult{}, errs.Config("failed to create API Center registry request: %v", err)
	}
	authenticated := strings.TrimSpace(tokenScope) != ""
	if authenticated {
		if credential == nil {
			return RegistryResult{}, errs.Config(
				"API Center authenticated discovery requires an Azure credential",
			)
		}
		if strings.ContainsAny(tokenScope, "\r\n\x00") {
			return RegistryResult{}, errs.Security("API Center token scope contains unsafe characters")
		}
		token, err := credential.GetToken(
			ctx,
			policy.TokenRequestOptions{Scopes: []string{tokenScope}},
		)
		if err != nil {
			return RegistryResult{}, errs.AuthWrap(
				err,
				"failed to acquire the explicitly configured API Center token scope",
			)
		}
		request.Header.Set("Authorization", "Bearer "+token.Token)
	}
	request.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return RegistryResult{}, errs.Transient("API Center registry request failed: %v", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxRegistryBytes+1))
	if err != nil {
		return RegistryResult{}, errs.Transient("failed to read API Center registry response: %v", err)
	}
	if int64(len(data)) > MaxRegistryBytes {
		return RegistryResult{}, errs.Security(
			"API Center registry response exceeds the %d byte limit",
			MaxRegistryBytes,
		)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RegistryResult{}, httpx.ResponseError(
			"Azure API Center",
			"list registry servers",
			response,
			data,
		)
	}
	var payload interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		return RegistryResult{}, errs.FoundryWrap(
			err,
			"failed to parse API Center registry response",
		)
	}
	filtered, matches := FilterPayload(payload, search)
	return RegistryResult{
		Endpoint:      registryURL,
		Authenticated: authenticated,
		Search:        strings.TrimSpace(search),
		Matches:       matches,
		Payload:       filtered,
	}, nil
}

func FilterPayload(payload interface{}, search string) (interface{}, int) {
	search = strings.ToLower(strings.TrimSpace(search))
	if search == "" {
		return payload, countRecords(payload)
	}
	return filterValue(payload, search)
}

func FindNamedRecords(payload interface{}, name string) []map[string]interface{} {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	var result []map[string]interface{}
	collectNamedRecords(payload, name, &result)
	return result
}

func filterValue(value interface{}, search string) (interface{}, int) {
	switch typed := value.(type) {
	case []interface{}:
		filtered := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			if matchesText(item, search) {
				filtered = append(filtered, item)
			}
		}
		return filtered, len(filtered)
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		matches := 0
		foundCollection := false
		for key, nested := range typed {
			if _, ok := nested.([]interface{}); ok {
				filtered, count := filterValue(nested, search)
				result[key] = filtered
				matches += count
				foundCollection = true
				continue
			}
			result[key] = nested
		}
		if foundCollection {
			return result, matches
		}
		if matchesText(typed, search) {
			return typed, 1
		}
		return map[string]interface{}{}, 0
	default:
		if matchesText(typed, search) {
			return typed, 1
		}
		return nil, 0
	}
}

func matchesText(value interface{}, search string) bool {
	data, err := json.Marshal(value)
	return err == nil && strings.Contains(strings.ToLower(string(data)), search)
}

func countRecords(value interface{}) int {
	switch typed := value.(type) {
	case []interface{}:
		return len(typed)
	case map[string]interface{}:
		total := 0
		for _, nested := range typed {
			if _, ok := nested.([]interface{}); ok {
				total += countRecords(nested)
			}
		}
		if total > 0 {
			return total
		}
		return 1
	default:
		return 1
	}
}

func collectNamedRecords(value interface{}, name string, result *[]map[string]interface{}) {
	switch typed := value.(type) {
	case []interface{}:
		for _, nested := range typed {
			collectNamedRecords(nested, name, result)
		}
	case map[string]interface{}:
		for _, key := range []string{"name", "id", "title", "displayName"} {
			if text, ok := typed[key].(string); ok && strings.EqualFold(strings.TrimSpace(text), name) {
				*result = append(*result, typed)
				return
			}
		}
		for _, nested := range typed {
			switch nested.(type) {
			case []interface{}, map[string]interface{}:
				collectNamedRecords(nested, name, result)
			}
		}
	}
}

func DescribeAuthentication(tokenScope string) string {
	if strings.TrimSpace(tokenScope) == "" {
		return "anonymous"
	}
	return fmt.Sprintf("Microsoft Entra scope %s", tokenScope)
}
