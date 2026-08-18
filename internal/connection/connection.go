// Package connection manages Foundry APIM project connections via ARM REST.
package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"foundry-agent-manager/internal/arm"
	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/redact"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// HTTPClient abstracts net/http for testing.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// State contains the non-secret ARM representation of a project connection.
type State struct {
	Exists     bool                   `json:"exists" yaml:"exists"`
	Name       string                 `json:"name,omitempty" yaml:"name,omitempty"`
	ID         string                 `json:"id,omitempty" yaml:"id,omitempty"`
	Properties map[string]interface{} `json:"properties,omitempty" yaml:"properties,omitempty"`
}

// EnsureResult reports whether ARM confirmed creation with HTTP 201.
type EnsureResult struct {
	Name    string
	Created bool
}

// AuthType returns the connection authentication type reported by ARM.
func (s State) AuthType() string {
	authType, _ := s.Properties["authType"].(string)
	return authType
}

// Restorable reports whether a captured connection can be safely PUT back.
func (s State) Restorable() bool {
	if !s.Exists {
		return false
	}
	switch {
	case strings.EqualFold(s.AuthType(), "ProjectManagedIdentity"),
		strings.EqualFold(s.AuthType(), "ManagedIdentity"):
		return true
	default:
		return false
	}
}

// DefaultConnectionName returns the connection name to use.
func DefaultConnectionName(apim *config.ApimSpec, agentName string) string {
	if apim.ConnectionName != "" {
		return apim.ConnectionName
	}
	stem := apim.APIName
	if stem == "" {
		stem = agentName
	}
	return "apim-" + stem
}

// BuildStaticModels expands deployment-name strings into Foundry ModelInfo objects.
func BuildStaticModels(models []string) []map[string]interface{} {
	result := make([]map[string]interface{}, len(models))
	for i, name := range models {
		result[i] = map[string]interface{}{
			"name": name,
			"properties": map[string]interface{}{
				"model": map[string]interface{}{
					"name":    name,
					"version": "",
					"format":  "OpenAI",
				},
			},
		}
	}
	return result
}

// BuildConnectionBody constructs the ARM request body for the APIM connection.
func BuildConnectionBody(apim *config.ApimSpec, models []string, subscriptionKey string) (map[string]interface{}, error) {
	target, err := apim.ValidateResolvedTarget()
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, errs.Config("apim connection requires at least one model (set apim.models or agent.model).")
	}

	metadata := map[string]string{
		"deploymentInPath": fmt.Sprintf("%v", apim.DeploymentInPath),
	}
	if apim.InferenceAPIVersion != "" {
		metadata["inferenceAPIVersion"] = apim.InferenceAPIVersion
	}
	modelsJSON, err := json.Marshal(BuildStaticModels(models))
	if err != nil {
		return nil, errs.Config("failed to encode APIM model metadata: %v", err)
	}
	metadata["models"] = string(modelsJSON)

	properties := map[string]interface{}{
		"category":      "ApiManagement",
		"target":        target,
		"authType":      apim.ARMAuthType(),
		"isSharedToAll": apim.IsSharedToAll,
		"metadata":      metadata,
	}

	if apim.Auth == "api_key" {
		if subscriptionKey == "" {
			return nil, errs.Config(
				"apim.auth=api_key requires the APIM subscription key: pass --apim-subscription-key " +
					"or set FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY (never store it in the manifest).",
			)
		}
		properties["credentials"] = map[string]interface{}{"key": subscriptionKey}
	} else {
		properties["audience"] = apim.Audience
		properties["credentials"] = map[string]interface{}{}
	}

	return map[string]interface{}{"properties": properties}, nil
}

// EnsureAPIMConnection creates/updates the project's APIM model connection.
func EnsureAPIMConnection(apim *config.ApimSpec, project *config.ProjectSpec, connName string, models []string, subscriptionKey string, cred azcore.TokenCredential, httpClient HTTPClient) (string, error) {
	result, err := EnsureAPIMConnectionContext(
		context.Background(),
		apim,
		project,
		connName,
		models,
		subscriptionKey,
		cred,
		httpClient,
	)
	return result.Name, err
}

// EnsureAPIMConnectionContext creates/updates the project's APIM model connection.
func EnsureAPIMConnectionContext(ctx context.Context, apim *config.ApimSpec, project *config.ProjectSpec, connName string, models []string, subscriptionKey string, cred azcore.TokenCredential, httpClient HTTPClient) (EnsureResult, error) {
	if err := config.ValidateARMRouting(project); err != nil {
		return EnsureResult{}, err
	}
	for _, pair := range [][2]string{
		{"project.subscription_id", project.SubscriptionID},
		{"project.resource_group", project.ResourceGroup},
		{"project.account_name", project.AccountName},
		{"project.name", project.Name},
	} {
		if pair[1] == "" {
			return EnsureResult{}, errs.Config("apim connection requires %s", pair[0])
		}
	}

	body, err := BuildConnectionBody(apim, models, subscriptionKey)
	if err != nil {
		return EnsureResult{}, err
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}

	tk, tkErr := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{project.ARMScope}})
	if tkErr != nil {
		return EnsureResult{}, errs.AuthWrap(tkErr, "failed to get ARM token")
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return EnsureResult{}, errs.FoundryWrap(err, "failed to encode APIM connection request")
	}
	connectionURL, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		apim.ConnectionAPIVersion,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"projects", project.Name,
		"connections", connName,
	)
	if err != nil {
		return EnsureResult{}, errs.FoundryWrap(err, "failed to build APIM connection ARM URL")
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", connectionURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return EnsureResult{}, errs.FoundryWrap(err, "failed to create APIM connection request")
	}
	req.Header.Set("Authorization", "Bearer "+tk.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return EnsureResult{}, errs.AmbiguousMutation(errs.FoundryWrap(err, "APIM connection upsert failed"))
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return EnsureResult{}, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to read APIM connection upsert response"),
		)
	}

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		responseErr := httpx.ResponseError(
			"ARM",
			"APIM connection upsert",
			resp,
			redact.Bytes(respBody, subscriptionKey),
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return EnsureResult{}, errs.AmbiguousMutation(responseErr)
		}
		return EnsureResult{}, responseErr
	}
	return EnsureResult{Name: connName, Created: resp.StatusCode == http.StatusCreated}, nil
}

// GetAPIMConnectionContext returns the current project connection without exposing credentials.
func GetAPIMConnectionContext(ctx context.Context, apim *config.ApimSpec, project *config.ProjectSpec, connName string, cred azcore.TokenCredential, httpClient HTTPClient) (State, error) {
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
			return State{}, errs.Config("apim connection inspection requires %s", pair[0])
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{project.ARMScope}})
	if err != nil {
		return State{}, errs.AuthWrap(err, "failed to get ARM token")
	}
	connectionURL, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		apim.ConnectionAPIVersion,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"projects", project.Name,
		"connections", connName,
	)
	if err != nil {
		return State{}, errs.FoundryWrap(err, "failed to build APIM connection ARM URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, connectionURL, nil)
	if err != nil {
		return State{}, errs.FoundryWrap(err, "failed to create APIM connection inspection request")
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return State{}, errs.FoundryWrap(err, "APIM connection inspection failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return State{}, errs.FoundryWrap(err, "failed to read APIM connection inspection response")
	}
	if resp.StatusCode == http.StatusNotFound {
		return State{Exists: false, Name: connName}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return State{}, httpx.ResponseError("ARM", "APIM connection inspection", resp, data)
	}
	var payload struct {
		ID         string                 `json:"id"`
		Name       string                 `json:"name"`
		Properties map[string]interface{} `json:"properties"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return State{}, errs.FoundryWrap(err, "failed to parse APIM connection inspection response")
	}
	return State{
		Exists:     true,
		Name:       payload.Name,
		ID:         payload.ID,
		Properties: payload.Properties,
	}, nil
}

// RestoreAPIMConnectionContext restores the managed properties captured before an update.
func RestoreAPIMConnectionContext(ctx context.Context, apim *config.ApimSpec, project *config.ProjectSpec, state State, cred azcore.TokenCredential, httpClient HTTPClient) error {
	if err := config.ValidateARMRouting(project); err != nil {
		return err
	}
	if !state.Exists {
		return errs.Config("cannot restore an APIM connection that did not previously exist")
	}
	if !state.Restorable() {
		return errs.Config(
			"the previous APIM connection cannot be restored because its authentication material is unavailable or unsupported",
		)
	}
	properties := managedConnectionProperties(state.Properties)
	target, _ := properties["target"].(string)
	var validationErr error
	if len(apim.AllowedSuffixes) > 0 {
		_, validationErr = netcheck.ValidateAPIMTargetForSuffixes(
			target,
			"previous APIM connection target",
			apim.AllowedSuffixes,
		)
	} else {
		_, validationErr = netcheck.ValidateAPIMTarget(target, "previous APIM connection target")
	}
	if validationErr != nil {
		return validationErr
	}
	if audience, _ := properties["audience"].(string); audience != "" {
		if err := config.ValidateManagedIdentityAudience(audience, apim.BlockedAudienceHosts); err != nil {
			return err
		}
	}
	bodyJSON, err := json.Marshal(map[string]interface{}{"properties": properties})
	if err != nil {
		return errs.FoundryWrap(err, "failed to encode APIM connection restore request")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{project.ARMScope}})
	if err != nil {
		return errs.AuthWrap(err, "failed to get ARM token")
	}
	connectionURL, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		apim.ConnectionAPIVersion,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"projects", project.Name,
		"connections", state.Name,
	)
	if err != nil {
		return errs.FoundryWrap(err, "failed to build APIM connection ARM URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, connectionURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return errs.FoundryWrap(err, "failed to create APIM connection restore request")
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return errs.FoundryWrap(err, "APIM connection restore failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return errs.FoundryWrap(err, "failed to read APIM connection restore response")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return httpx.ResponseError("ARM", "APIM connection restore", resp, data)
	}
	return nil
}

// DeleteAPIMConnection deletes the project's APIM model connection. Idempotent.
func DeleteAPIMConnection(apim *config.ApimSpec, project *config.ProjectSpec, connName string, cred azcore.TokenCredential, httpClient HTTPClient) (bool, error) {
	return DeleteAPIMConnectionContext(context.Background(), apim, project, connName, cred, httpClient)
}

// DeleteAPIMConnectionContext deletes the project's APIM model connection. Idempotent.
func DeleteAPIMConnectionContext(ctx context.Context, apim *config.ApimSpec, project *config.ProjectSpec, connName string, cred azcore.TokenCredential, httpClient HTTPClient) (bool, error) {
	if err := config.ValidateARMRouting(project); err != nil {
		return false, err
	}
	for _, pair := range [][2]string{
		{"project.subscription_id", project.SubscriptionID},
		{"project.resource_group", project.ResourceGroup},
		{"project.account_name", project.AccountName},
		{"project.name", project.Name},
	} {
		if pair[1] == "" {
			return false, errs.Config("apim connection teardown requires %s", pair[0])
		}
	}

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}

	tk, tkErr := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{project.ARMScope}})
	if tkErr != nil {
		return false, errs.AuthWrap(tkErr, "failed to get ARM token")
	}

	connectionURL, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		apim.ConnectionAPIVersion,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"projects", project.Name,
		"connections", connName,
	)
	if err != nil {
		return false, errs.FoundryWrap(err, "failed to build APIM connection ARM URL")
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", connectionURL, nil)
	if err != nil {
		return false, errs.FoundryWrap(err, "failed to create APIM connection delete request")
	}
	req.Header.Set("Authorization", "Bearer "+tk.Token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return false, errs.FoundryWrap(err, "APIM connection delete failed")
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, errs.FoundryWrap(err, "failed to read APIM connection delete response")
	}

	if resp.StatusCode == 200 || resp.StatusCode == 204 {
		return true, nil
	}
	if resp.StatusCode == 404 {
		return false, nil
	}
	return false, httpx.ResponseError("ARM", "APIM connection delete", resp, respBody)
}

func managedConnectionProperties(properties map[string]interface{}) map[string]interface{} {
	managed := map[string]interface{}{}
	for _, key := range []string{
		"category",
		"target",
		"authType",
		"isSharedToAll",
		"metadata",
		"audience",
		"credentials",
	} {
		if value, ok := properties[key]; ok {
			managed[key] = value
		}
	}
	return managed
}
