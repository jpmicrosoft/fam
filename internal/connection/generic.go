package connection

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"foundry-agent-manager/internal/arm"
	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/redact"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type Definition struct {
	Name          string
	Category      string
	Target        string
	AuthType      string
	Audience      string
	IsSharedToAll bool
	Metadata      map[string]string
	Credentials   map[string]interface{}
}

func (d Definition) Body() (map[string]interface{}, error) {
	for field, value := range map[string]string{
		"name": d.Name, "category": d.Category, "target": d.Target, "auth type": d.AuthType,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, errs.Config("connection %s is required", field)
		}
	}
	target, err := url.Parse(d.Target)
	if err != nil || !strings.EqualFold(target.Scheme, "https") || target.Hostname() == "" ||
		target.User != nil || target.Fragment != "" {
		return nil, errs.Config("connection target must be an absolute HTTPS URL without user info or fragments")
	}
	properties := map[string]interface{}{
		"category":      d.Category,
		"target":        target.String(),
		"authType":      d.AuthType,
		"isSharedToAll": d.IsSharedToAll,
		"credentials":   d.Credentials,
	}
	if len(d.Metadata) > 0 {
		properties["metadata"] = d.Metadata
	}
	if d.Audience != "" {
		properties["audience"] = d.Audience
	}
	return map[string]interface{}{"properties": properties}, nil
}

func UpsertContext(
	ctx context.Context,
	project *config.ProjectSpec,
	apiVersion string,
	definition Definition,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
	secrets ...string,
) (EnsureResult, error) {
	if err := validateGenericProject(project, "connection upsert"); err != nil {
		return EnsureResult{}, err
	}
	body, err := definition.Body()
	if err != nil {
		return EnsureResult{}, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return EnsureResult{}, errs.FoundryWrap(err, "failed to encode connection request")
	}
	requestURL, err := genericConnectionURL(project, apiVersion, definition.Name)
	if err != nil {
		return EnsureResult{}, err
	}
	resp, err := doARM(
		ctx,
		http.MethodPut,
		requestURL,
		data,
		project,
		credential,
		httpClient,
	)
	if err != nil {
		return EnsureResult{}, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "connection %q upsert failed", definition.Name),
		)
	}
	defer resp.Body.Close()
	responseData, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return EnsureResult{}, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read connection %q upsert response", definition.Name),
		)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		safe := responseData
		for _, secret := range secrets {
			safe = redact.Bytes(safe, secret)
		}
		responseErr := httpx.ResponseError("ARM", "connection upsert", resp, safe)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return EnsureResult{}, errs.AmbiguousMutation(responseErr)
		}
		return EnsureResult{}, responseErr
	}
	return EnsureResult{Name: definition.Name, Created: resp.StatusCode == http.StatusCreated}, nil
}

func GetContext(
	ctx context.Context,
	project *config.ProjectSpec,
	apiVersion string,
	name string,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (State, error) {
	if err := validateGenericProject(project, "connection inspection"); err != nil {
		return State{}, err
	}
	requestURL, err := genericConnectionURL(project, apiVersion, name)
	if err != nil {
		return State{}, err
	}
	resp, err := doARM(ctx, http.MethodGet, requestURL, nil, project, credential, httpClient)
	if err != nil {
		return State{}, errs.FoundryWrap(err, "connection %q inspection failed", name)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return State{}, errs.FoundryWrap(readErr, "failed to read connection %q response", name)
	}
	if resp.StatusCode == http.StatusNotFound {
		return State{Exists: false, Name: name}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return State{}, httpx.ResponseError("ARM", "connection inspection", resp, data)
	}
	state, err := decodeState(data)
	if err != nil {
		return State{}, err
	}
	return state, nil
}

func ListContext(
	ctx context.Context,
	project *config.ProjectSpec,
	apiVersion string,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) ([]State, error) {
	if err := validateGenericProject(project, "connection listing"); err != nil {
		return nil, err
	}
	requestURL, err := genericConnectionURL(project, apiVersion, "")
	if err != nil {
		return nil, err
	}
	var result []State
	for page := 0; requestURL != ""; page++ {
		if page >= 1000 {
			return nil, errs.Foundry("connection listing exceeded 1000 pages")
		}
		resp, err := doARM(ctx, http.MethodGet, requestURL, nil, project, credential, httpClient)
		if err != nil {
			return nil, errs.FoundryWrap(err, "connection listing failed")
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, errs.FoundryWrap(readErr, "failed to read connection list response")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, httpx.ResponseError("ARM", "connection listing", resp, data)
		}
		var payload struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"nextLink"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse connection list response")
		}
		for _, raw := range payload.Value {
			state, err := decodeState(raw)
			if err != nil {
				return nil, err
			}
			result = append(result, state)
		}
		requestURL, err = validateARMNextLink(project, payload.NextLink)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func DeleteContext(
	ctx context.Context,
	project *config.ProjectSpec,
	apiVersion string,
	name string,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (bool, error) {
	if err := validateGenericProject(project, "connection deletion"); err != nil {
		return false, err
	}
	requestURL, err := genericConnectionURL(project, apiVersion, name)
	if err != nil {
		return false, err
	}
	resp, err := doARM(ctx, http.MethodDelete, requestURL, nil, project, credential, httpClient)
	if err != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "connection %q deletion failed", name),
		)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read connection %q deletion response", name),
		)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	responseErr := httpx.ResponseError("ARM", "connection deletion", resp, data)
	if httpx.IsTransientStatus(resp.StatusCode) {
		return false, errs.AmbiguousMutation(responseErr)
	}
	return false, responseErr
}

func validateGenericProject(project *config.ProjectSpec, action string) error {
	if err := config.ValidateARMRouting(project); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"project.subscription_id": project.SubscriptionID,
		"project.resource_group":  project.ResourceGroup,
		"project.account_name":    project.AccountName,
		"project.name":            project.Name,
	} {
		if strings.TrimSpace(value) == "" {
			return errs.Config("%s requires %s", action, field)
		}
	}
	return nil
}

func genericConnectionURL(
	project *config.ProjectSpec,
	apiVersion string,
	name string,
) (string, error) {
	if apiVersion == "" {
		apiVersion = config.DefaultConnectionAPIVersion
	}
	segments := []string{
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"projects", project.Name,
		"connections",
	}
	if name != "" {
		segments = append(segments, name)
	}
	result, err := arm.ResourceURLForEndpoint(project.ARMEndpoint, apiVersion, segments...)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to build connection ARM URL")
	}
	return result, nil
}

func doARM(
	ctx context.Context,
	method string,
	requestURL string,
	body []byte,
	project *config.ProjectSpec,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (*http.Response, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	token, err := credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{project.ARMScope}})
	if err != nil {
		return nil, errs.AuthWrap(err, "failed to get ARM token")
	}
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to create ARM connection request")
	}
	request.Header.Set("Authorization", "Bearer "+token.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return httpClient.Do(request)
}

func decodeState(data []byte) (State, error) {
	var payload struct {
		ID         string                 `json:"id"`
		Name       string                 `json:"name"`
		Properties map[string]interface{} `json:"properties"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return State{}, errs.FoundryWrap(err, "failed to parse connection response")
	}
	delete(payload.Properties, "credentials")
	return State{
		Exists: true, Name: payload.Name, ID: payload.ID, Properties: payload.Properties,
	}, nil
}

func validateARMNextLink(project *config.ProjectSpec, raw string) (string, error) {
	next, err := arm.ValidateNextLink(project.ARMEndpoint, raw)
	if err != nil {
		return "", errs.Security("ARM connection %v", err)
	}
	return next, nil
}
