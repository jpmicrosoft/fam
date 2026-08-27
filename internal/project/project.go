// Package project implements control-plane operations for Foundry projects.
package project

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"foundry-agent-manager/internal/arm"
	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// HTTPClient abstracts net/http for testing.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// State describes whether a Foundry project exists and how to reach it.
type State struct {
	Exists   bool   `json:"exists" yaml:"exists"`
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Location string `json:"location,omitempty" yaml:"location,omitempty"`
}

// EnsureProject creates the project if missing; returns (endpoint, created).
func EnsureProject(project *config.ProjectSpec, cred azcore.TokenCredential, httpClient HTTPClient) (string, bool, error) {
	return EnsureProjectContext(context.Background(), project, cred, httpClient)
}

// EnsureProjectContext creates the project if missing; returns (endpoint, created).
func EnsureProjectContext(ctx context.Context, project *config.ProjectSpec, cred azcore.TokenCredential, httpClient HTTPClient) (string, bool, error) {
	if err := config.ValidateARMRouting(project); err != nil {
		return "", false, err
	}
	for _, pair := range [][2]string{
		{"project.subscription_id", project.SubscriptionID},
		{"project.resource_group", project.ResourceGroup},
		{"project.account_name", project.AccountName},
		{"project.name", project.Name},
	} {
		if pair[1] == "" {
			return "", false, errs.Config("--ensure-project requires %s", pair[0])
		}
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}

	tk, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{project.ARMScope}})
	if err != nil {
		return "", false, errs.AuthWrap(err, "failed to get ARM token")
	}
	headers := map[string]string{
		"Authorization": "Bearer " + tk.Token,
		"Content-Type":  "application/json",
	}

	projectURL, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		project.APIVersion,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"projects", project.Name,
	)
	if err != nil {
		return "", false, errs.FoundryWrap(err, "failed to build project ARM URL")
	}

	endpoint := project.Endpoint
	if endpoint == "" && project.AccountEndpoint != "" && project.Name != "" {
		endpoint = strings.TrimRight(project.AccountEndpoint, "/") + "/api/projects/" + project.Name
	}

	// Check if exists
	resp, err := doHTTP(ctx, httpClient, "GET", projectURL, headers, nil)
	if err != nil {
		return "", false, errs.FoundryWrap(err, "project existence check failed")
	}
	defer resp.Body.Close()
	bodyData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, errs.FoundryWrap(err, "failed to read project existence response")
	}

	if resp.StatusCode == 200 {
		var payload struct {
			Location string `json:"location"`
		}
		if err := json.Unmarshal(bodyData, &payload); err != nil {
			return "", false, errs.FoundryWrap(err, "failed to parse existing project response")
		}
		if err := config.ValidateReportedProjectLocation(payload.Location, project.AllowedRegions); err != nil {
			return "", false, err
		}
		if endpoint == "" {
			endpoint, err = endpointFromARM(bodyData, project)
			if err != nil {
				return "", false, err
			}
		}
		return endpoint, false, nil
	}
	if resp.StatusCode != 404 {
		return "", false, httpx.ResponseError("ARM", "project existence check", resp, bodyData)
	}

	// Get account region
	accountRegion, err := getAccountRegion(ctx, project, headers, httpClient)
	if err != nil {
		return "", false, err
	}
	location := project.Location
	if location == "" {
		location = accountRegion
	} else if normalizeRegion(location) != normalizeRegion(accountRegion) {
		return "", false, errs.Config(
			"project.location %q does not match the Foundry account %q region %q; "+
				"account-based projects must be co-regional. Omit project.location to auto-use the account region (%q).",
			location, project.AccountName, accountRegion, accountRegion,
		)
	}
	if err := config.ValidateProjectLocation(location, project.AllowedRegions); err != nil {
		return "", false, err
	}

	// Create project
	body := map[string]interface{}{
		"location": location,
		"identity": map[string]interface{}{"type": "SystemAssigned"},
		"properties": map[string]interface{}{
			"displayName": defaultStr(project.DisplayName, project.Name),
			"description": defaultStr(project.Description, "Provisioned by Foundry Agent Manager."),
		},
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", false, errs.FoundryWrap(err, "failed to encode project creation request")
	}

	resp2, err := doHTTP(ctx, httpClient, "PUT", projectURL, headers, bodyJSON)
	if err != nil {
		return "", false, errs.AmbiguousMutation(errs.FoundryWrap(err, "project creation failed"))
	}
	defer resp2.Body.Close()
	bodyData2, err := io.ReadAll(resp2.Body)
	if err != nil {
		return "", false, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to read project creation response"),
		)
	}

	if resp2.StatusCode != 200 && resp2.StatusCode != 201 {
		responseErr := httpx.ResponseError("ARM", "project creation", resp2, bodyData2)
		if httpx.IsTransientStatus(resp2.StatusCode) {
			return "", false, errs.AmbiguousMutation(responseErr)
		}
		return "", false, responseErr
	}

	if endpoint == "" {
		endpoint, err = endpointFromARM(bodyData2, project)
		if err != nil {
			return "", false, errs.AmbiguousMutation(err)
		}
	}
	return endpoint, resp2.StatusCode == http.StatusCreated, nil
}

// InspectProjectContext checks for a project without creating it.
func InspectProjectContext(ctx context.Context, project *config.ProjectSpec, cred azcore.TokenCredential, httpClient HTTPClient) (State, error) {
	if err := config.ValidateARMRouting(project); err != nil {
		return State{}, err
	}
	for _, pair := range [][2]string{
		{"project.subscription_id", project.SubscriptionID},
		{"project.resource_group", project.ResourceGroup},
		{"project.account_name", project.AccountName},
		{"project.name", project.Name},
	} {
		if pair[1] == "" {
			return State{}, errs.Config("project inspection requires %s", pair[0])
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{project.ARMScope}})
	if err != nil {
		return State{}, errs.AuthWrap(err, "failed to get ARM token")
	}
	projectURL, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		project.APIVersion,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"projects", project.Name,
	)
	if err != nil {
		return State{}, errs.FoundryWrap(err, "failed to build project ARM URL")
	}
	resp, err := doHTTP(ctx, httpClient, http.MethodGet, projectURL, map[string]string{
		"Authorization": "Bearer " + token.Token,
	}, nil)
	if err != nil {
		return State{}, errs.FoundryWrap(err, "project inspection failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return State{}, errs.FoundryWrap(err, "failed to read project inspection response")
	}
	if resp.StatusCode == http.StatusNotFound {
		return State{Exists: false}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return State{}, httpx.ResponseError("ARM", "project inspection", resp, data)
	}
	endpoint := project.Endpoint
	if endpoint == "" {
		endpoint, err = endpointFromARM(data, project)
		if err != nil {
			return State{}, err
		}
	}
	var payload struct {
		Location string `json:"location"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return State{}, errs.FoundryWrap(err, "failed to parse project inspection response")
	}
	if err := config.ValidateReportedProjectLocation(payload.Location, project.AllowedRegions); err != nil {
		return State{}, err
	}
	return State{Exists: true, Endpoint: endpoint, Location: payload.Location}, nil
}

// DeleteProjectContext deletes a project created by the current deployment. Idempotent.
func DeleteProjectContext(ctx context.Context, project *config.ProjectSpec, cred azcore.TokenCredential, httpClient HTTPClient) (bool, error) {
	if err := config.ValidateARMRouting(project); err != nil {
		return false, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{project.ARMScope}})
	if err != nil {
		return false, errs.AuthWrap(err, "failed to get ARM token")
	}
	projectURL, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		project.APIVersion,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"projects", project.Name,
	)
	if err != nil {
		return false, errs.FoundryWrap(err, "failed to build project ARM URL")
	}
	resp, err := doHTTP(ctx, httpClient, http.MethodDelete, projectURL, map[string]string{
		"Authorization": "Bearer " + token.Token,
	}, nil)
	if err != nil {
		return false, errs.FoundryWrap(err, "project deletion failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, errs.FoundryWrap(err, "failed to read project deletion response")
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	return false, httpx.ResponseError("ARM", "project deletion", resp, data)
}

func getAccountRegion(ctx context.Context, project *config.ProjectSpec, headers map[string]string, httpClient HTTPClient) (string, error) {
	accountURL, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		project.APIVersion,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
	)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to build Foundry account ARM URL")
	}

	resp, err := doHTTP(ctx, httpClient, "GET", accountURL, headers, nil)
	if err != nil {
		return "", errs.FoundryWrap(err, "could not read Foundry account %q", project.AccountName)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to read Foundry account %q response", project.AccountName)
	}

	if resp.StatusCode != 200 {
		return "", httpx.ResponseError("ARM", "Foundry account region lookup", resp, data)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", errs.FoundryWrap(err, "could not parse Foundry account %q response", project.AccountName)
	}
	region, _ := result["location"].(string)
	if region == "" {
		return "", errs.Foundry("Foundry account %q response contained no location", project.AccountName)
	}
	return region, nil
}

func endpointFromARM(data []byte, project *config.ProjectSpec) (string, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", errs.FoundryWrap(err, "project response did not contain valid JSON")
	}
	props, _ := payload["properties"].(map[string]interface{})
	if props != nil {
		endpoints, _ := props["endpoints"].(map[string]interface{})
		// ARM can return several data-plane endpoints. Map iteration order is
		// randomized, so select by sorted key to keep the chosen endpoint (and
		// therefore every receipt and diagnostic) reproducible.
		names := make([]string, 0, len(endpoints))
		for name := range endpoints {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if s, ok := endpoints[name].(string); ok && strings.Contains(s, "/api/projects/") {
				return s, nil
			}
		}
	}
	if project.AccountEndpoint != "" && project.Name != "" {
		return strings.TrimRight(project.AccountEndpoint, "/") + "/api/projects/" + project.Name, nil
	}
	return "", errs.Foundry(
		"could not determine the data-plane endpoint for project %q; set project.endpoint or project.account_endpoint",
		project.Name,
	)
}

func doHTTP(ctx context.Context, client HTTPClient, method, requestURL string, headers map[string]string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		return nil, err
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return client.Do(req)
}

func normalizeRegion(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, " ", ""))
}

func defaultStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
