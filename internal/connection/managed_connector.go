package connection

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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

const (
	ConnectorCatalogEndpoint       = "https://eastus.api.azureml.ms/asset-gallery/v1.0/tools"
	ConnectorCatalogScope          = "https://ai.azure.com/.default"
	ConnectorCatalogRegistry       = "connectors-registry-prod-bl"
	ConnectorOperationsLocation    = "eastus"
	ConnectorOperationsAPIVersion  = "2016-06-01"
	DefaultConnectorRedirectURL    = "https://ai.azure.com/nextgen/authConsentPopup"
	managedConnectorPlaceholderURL = "https://placeholder"
)

var ManagedConnectorRegions = []string{
	"australiaeast",
	"brazilsouth",
	"canadacentral",
	"eastus2",
	"francecentral",
	"germanywestcentral",
	"japaneast",
	"norwayeast",
	"southafricanorth",
	"southcentralus",
	"spaincentral",
	"swedencentral",
	"switzerlandnorth",
	"westus3",
}

type ConnectorCatalogQuery struct {
	Search   string
	Name     string
	PageSize int
	Skip     int
}

type ConnectorCatalogPage struct {
	TotalCount int                     `json:"totalCount" yaml:"totalCount"`
	Skip       int                     `json:"skip" yaml:"skip"`
	PageSize   int                     `json:"pageSize" yaml:"pageSize"`
	Connectors []ConnectorCatalogEntry `json:"connectors" yaml:"connectors"`
}

type ConnectorCatalogEntry struct {
	EntityID    string                   `json:"entityId" yaml:"entityId"`
	Name        string                   `json:"name" yaml:"name"`
	Title       string                   `json:"title" yaml:"title"`
	Description string                   `json:"description,omitempty" yaml:"description,omitempty"`
	Publisher   string                   `json:"publisher,omitempty" yaml:"publisher,omitempty"`
	AuthType    string                   `json:"authType" yaml:"authType"`
	Actions     []ConnectorCatalogAction `json:"actions,omitempty" yaml:"actions,omitempty"`
}

type ConnectorCatalogAction struct {
	Name        string `json:"name" yaml:"name"`
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type ManagedConnectorDefinition struct {
	Name            string
	ConnectorName   string
	ToolEntityID    string
	MCPServerConfig string
}

type ManagedConnectorState struct {
	Exists            bool   `json:"exists" yaml:"exists"`
	Name              string `json:"name" yaml:"name"`
	ID                string `json:"id,omitempty" yaml:"id,omitempty"`
	ConnectorName     string `json:"connectorName,omitempty" yaml:"connectorName,omitempty"`
	ToolEntityID      string `json:"toolEntityId,omitempty" yaml:"toolEntityId,omitempty"`
	OverallStatus     string `json:"overallStatus,omitempty" yaml:"overallStatus,omitempty"`
	Target            string `json:"target,omitempty" yaml:"target,omitempty"`
	ActionsConfigured bool   `json:"actionsConfigured" yaml:"actionsConfigured"`
	MCPServerConfig   string `json:"-" yaml:"-"`
}

type ConnectorConsentRequest struct {
	ObjectID    string
	TenantID    string
	RedirectURL string
}

type ConnectorConsentResult struct {
	Connection string `json:"connection" yaml:"connection"`
	ObjectID   string `json:"objectId" yaml:"objectId"`
	TenantID   string `json:"tenantId" yaml:"tenantId"`
	Link       string `json:"link" yaml:"link"`
}

type ConnectorOperation struct {
	Name             string                    `json:"name" yaml:"name"`
	Summary          string                    `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description      string                    `json:"description,omitempty" yaml:"description,omitempty"`
	IsWebhook        bool                      `json:"isWebhook" yaml:"isWebhook"`
	IsNotification   bool                      `json:"isNotification" yaml:"isNotification"`
	InputsDefinition ConnectorInputsDefinition `json:"inputsDefinition,omitempty" yaml:"inputsDefinition,omitempty"`
}

type ConnectorInputsDefinition struct {
	Properties map[string]map[string]interface{} `json:"properties,omitempty" yaml:"properties,omitempty"`
	Required   []string                          `json:"required,omitempty" yaml:"required,omitempty"`
}

type connectorCatalogPayload struct {
	TotalCount int                        `json:"totalCount"`
	Value      []connectorCatalogRawEntry `json:"value"`
}

type connectorCatalogRawEntry struct {
	EntityID    string                 `json:"entityId"`
	Annotations map[string]interface{} `json:"annotations"`
	Properties  map[string]interface{} `json:"properties"`
}

func ListConnectorCatalogContext(
	ctx context.Context,
	query ConnectorCatalogQuery,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (ConnectorCatalogPage, error) {
	if query.PageSize == 0 {
		query.PageSize = 100
	}
	if query.PageSize < 1 || query.PageSize > 100 {
		return ConnectorCatalogPage{}, errs.Config("connector catalog page size must be between 1 and 100")
	}
	if query.Skip < 0 {
		return ConnectorCatalogPage{}, errs.Config("connector catalog skip must be zero or greater")
	}
	search := strings.TrimSpace(query.Search)
	if search == "" {
		search = "*"
	}
	filters := []map[string]interface{}{
		{"field": "entityContainerId", "operator": "eq", "values": []string{ConnectorCatalogRegistry}},
		{"field": "type", "operator": "eq", "values": []string{"tools"}},
		{"field": "kind", "operator": "eq", "values": []string{"Versioned"}},
		{"field": "labels", "operator": "eq", "values": []string{"latest"}},
	}
	if name := strings.TrimSpace(query.Name); name != "" {
		filters = append(filters, map[string]interface{}{
			"field": "annotations/name", "operator": "eq", "values": []string{name},
		})
	}
	body, err := json.Marshal(map[string]interface{}{
		"freeTextSearch":          search,
		"filters":                 filters,
		"includeTotalResultCount": true,
		"pageSize":                query.PageSize,
		"skip":                    query.Skip,
	})
	if err != nil {
		return ConnectorCatalogPage{}, errs.FoundryWrap(err, "failed to encode connector catalog query")
	}
	resp, err := doConnectorCatalog(ctx, body, credential, httpClient)
	if err != nil {
		return ConnectorCatalogPage{}, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return ConnectorCatalogPage{}, errs.FoundryWrap(readErr, "failed to read connector catalog response")
	}
	if resp.StatusCode != http.StatusOK {
		return ConnectorCatalogPage{}, httpx.ResponseError("Foundry Tools Catalog", "list connectors", resp, data)
	}
	var payload connectorCatalogPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ConnectorCatalogPage{}, errs.FoundryWrap(err, "failed to parse connector catalog response")
	}
	result := ConnectorCatalogPage{
		TotalCount: payload.TotalCount,
		Skip:       query.Skip,
		PageSize:   query.PageSize,
		Connectors: make([]ConnectorCatalogEntry, 0, len(payload.Value)),
	}
	for _, raw := range payload.Value {
		entry, err := normalizeConnectorCatalogEntry(raw)
		if err != nil {
			return ConnectorCatalogPage{}, err
		}
		result.Connectors = append(result.Connectors, entry)
	}
	return result, nil
}

func GetConnectorCatalogContext(
	ctx context.Context,
	name string,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (ConnectorCatalogEntry, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ConnectorCatalogEntry{}, errs.Config("connector catalog name is required")
	}
	page, err := ListConnectorCatalogContext(ctx, ConnectorCatalogQuery{
		Name: name, PageSize: 1,
	}, credential, httpClient)
	if err != nil {
		return ConnectorCatalogEntry{}, err
	}
	if len(page.Connectors) == 0 {
		return ConnectorCatalogEntry{}, errs.NotFound("connector %q was not found in the Foundry Tools Catalog", name)
	}
	if !strings.EqualFold(page.Connectors[0].Name, name) {
		return ConnectorCatalogEntry{}, errs.Foundry(
			"connector catalog returned %q for exact lookup %q",
			page.Connectors[0].Name,
			name,
		)
	}
	return page.Connectors[0], nil
}

func UpsertManagedConnectorContext(
	ctx context.Context,
	project *config.ProjectSpec,
	apiVersion string,
	definition ManagedConnectorDefinition,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (ManagedConnectorState, error) {
	if err := validateGenericProject(project, "managed connector upsert"); err != nil {
		return ManagedConnectorState{}, err
	}
	body, err := managedConnectorBody(definition)
	if err != nil {
		return ManagedConnectorState{}, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return ManagedConnectorState{}, errs.FoundryWrap(err, "failed to encode managed connector request")
	}
	requestURL, err := genericConnectionURL(project, apiVersion, definition.Name)
	if err != nil {
		return ManagedConnectorState{}, err
	}
	resp, err := doARM(ctx, http.MethodPut, requestURL, data, project, credential, httpClient)
	if err != nil {
		return ManagedConnectorState{}, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "managed connector %q upsert failed", definition.Name),
		)
	}
	defer resp.Body.Close()
	responseData, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return ManagedConnectorState{}, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read managed connector %q response", definition.Name),
		)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		responseErr := httpx.ResponseError("ARM", "managed connector upsert", resp, responseData)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return ManagedConnectorState{}, errs.AmbiguousMutation(responseErr)
		}
		return ManagedConnectorState{}, responseErr
	}
	state, err := decodeState(responseData)
	if err != nil {
		return ManagedConnectorState{}, errs.AmbiguousMutation(err)
	}
	if state.Name == "" {
		state.Name = definition.Name
	}
	return managedConnectorState(state)
}

func GetManagedConnectorContext(
	ctx context.Context,
	project *config.ProjectSpec,
	apiVersion string,
	name string,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (ManagedConnectorState, error) {
	state, err := GetContext(ctx, project, apiVersion, name, credential, httpClient)
	if err != nil {
		return ManagedConnectorState{}, err
	}
	if !state.Exists {
		return ManagedConnectorState{Exists: false, Name: name}, nil
	}
	return managedConnectorState(state)
}

func ListConnectorOperationsContext(
	ctx context.Context,
	project *config.ProjectSpec,
	connectorName string,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) ([]ConnectorOperation, error) {
	if err := validateGenericProject(project, "connector operation listing"); err != nil {
		return nil, err
	}
	requestURL, err := connectorOperationsURL(project, connectorName, "")
	if err != nil {
		return nil, err
	}
	resp, err := doARM(ctx, http.MethodGet, requestURL, nil, project, credential, httpClient)
	if err != nil {
		return nil, errs.FoundryWrap(err, "connector %q operation listing failed", connectorName)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, errs.FoundryWrap(readErr, "failed to read connector operation list")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError("ARM", "list connector operations", resp, data)
	}
	var payload struct {
		Value []connectorOperationPayload `json:"value"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse connector operation list")
	}
	result := make([]ConnectorOperation, 0, len(payload.Value))
	for _, item := range payload.Value {
		operation, err := normalizeConnectorOperation(item)
		if err != nil {
			return nil, err
		}
		if operation.IsWebhook || operation.IsNotification {
			continue
		}
		result = append(result, operation)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func GetConnectorOperationContext(
	ctx context.Context,
	project *config.ProjectSpec,
	connectorName string,
	operationName string,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (ConnectorOperation, error) {
	if err := validateGenericProject(project, "connector operation inspection"); err != nil {
		return ConnectorOperation{}, err
	}
	requestURL, err := connectorOperationsURL(project, connectorName, operationName)
	if err != nil {
		return ConnectorOperation{}, err
	}
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return ConnectorOperation{}, errs.FoundryWrap(err, "failed to parse connector operation URL")
	}
	query := parsed.Query()
	query.Set("$expand", "properties/inputsDefinition")
	parsed.RawQuery = query.Encode()
	resp, err := doARM(ctx, http.MethodGet, parsed.String(), nil, project, credential, httpClient)
	if err != nil {
		return ConnectorOperation{}, errs.FoundryWrap(
			err,
			"connector %q operation %q inspection failed",
			connectorName,
			operationName,
		)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return ConnectorOperation{}, errs.FoundryWrap(readErr, "failed to read connector operation response")
	}
	if resp.StatusCode == http.StatusNotFound {
		return ConnectorOperation{}, errs.NotFound(
			"operation %q was not found for connector %q",
			operationName,
			connectorName,
		)
	}
	if resp.StatusCode != http.StatusOK {
		return ConnectorOperation{}, httpx.ResponseError("ARM", "get connector operation", resp, data)
	}
	var payload connectorOperationPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ConnectorOperation{}, errs.FoundryWrap(err, "failed to parse connector operation response")
	}
	operation, err := normalizeConnectorOperation(payload)
	if err != nil {
		return ConnectorOperation{}, err
	}
	if operation.IsWebhook || operation.IsNotification {
		return ConnectorOperation{}, errs.Config(
			"connector operation %q is a trigger and cannot be registered as an agent-callable action",
			operationName,
		)
	}
	return operation, nil
}

func CreateConnectorConsentLinkContext(
	ctx context.Context,
	project *config.ProjectSpec,
	apiVersion string,
	connectionName string,
	options ConnectorConsentRequest,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (ConnectorConsentResult, error) {
	if err := validateGenericProject(project, "connector consent"); err != nil {
		return ConnectorConsentResult{}, err
	}
	for field, value := range map[string]string{
		"object id": options.ObjectID,
		"tenant id": options.TenantID,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
			return ConnectorConsentResult{}, errs.Config("connector consent %s is required", field)
		}
	}
	if strings.TrimSpace(options.RedirectURL) == "" {
		options.RedirectURL = DefaultConnectorRedirectURL
	}
	if err := validateHTTPSValue(options.RedirectURL, "connector consent redirect URL"); err != nil {
		return ConnectorConsentResult{}, err
	}
	requestURL, err := genericConnectionURL(project, apiVersion, connectionName)
	if err != nil {
		return ConnectorConsentResult{}, err
	}
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return ConnectorConsentResult{}, errs.FoundryWrap(err, "failed to parse connector consent URL")
	}
	query := parsed.Query()
	query.Set("action", "listConsentLinks")
	parsed.RawQuery = query.Encode()
	body, err := json.Marshal(map[string]interface{}{
		"parameters": []map[string]string{{
			"objectId": options.ObjectID, "parameterName": "token",
			"redirectUrl": options.RedirectURL, "tenantId": options.TenantID,
		}},
	})
	if err != nil {
		return ConnectorConsentResult{}, errs.FoundryWrap(err, "failed to encode connector consent request")
	}
	resp, err := doARM(ctx, http.MethodPost, parsed.String(), body, project, credential, httpClient)
	if err != nil {
		return ConnectorConsentResult{}, errs.FoundryWrap(err, "connector consent link request failed")
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return ConnectorConsentResult{}, errs.FoundryWrap(readErr, "failed to read connector consent response")
	}
	if resp.StatusCode != http.StatusOK {
		return ConnectorConsentResult{}, httpx.ResponseError("ARM", "create connector consent link", resp, data)
	}
	var payload struct {
		Value []struct {
			Link string `json:"link"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ConnectorConsentResult{}, errs.FoundryWrap(err, "failed to parse connector consent response")
	}
	if len(payload.Value) == 0 || strings.TrimSpace(payload.Value[0].Link) == "" {
		return ConnectorConsentResult{}, errs.Foundry("connector consent response did not contain a link")
	}
	if err := validateHTTPSValue(payload.Value[0].Link, "connector consent link"); err != nil {
		return ConnectorConsentResult{}, errs.SecurityWrap(err, "ARM returned an unsafe consent link")
	}
	return ConnectorConsentResult{
		Connection: connectionName,
		ObjectID:   options.ObjectID,
		TenantID:   options.TenantID,
		Link:       payload.Value[0].Link,
	}, nil
}

func SupportsManagedConnectorRegion(region string) bool {
	region = strings.ToLower(strings.TrimSpace(region))
	for _, supported := range ManagedConnectorRegions {
		if region == supported {
			return true
		}
	}
	return false
}

func ManagedConnectorMCPTool(state ManagedConnectorState) (map[string]interface{}, error) {
	if !state.Exists ||
		!state.ActionsConfigured ||
		!strings.EqualFold(state.OverallStatus, "Connected") {
		return nil, nil
	}
	if state.Target == managedConnectorPlaceholderURL {
		return nil, errs.Foundry(
			"managed connector %q is Connected but still reports the placeholder target",
			state.Name,
		)
	}
	if err := validateHTTPSValue(state.Target, "managed connector MCP target"); err != nil {
		return nil, errs.SecurityWrap(err, "ARM returned an unsafe managed connector target")
	}
	return map[string]interface{}{
		"type":                  "mcp",
		"server_label":          state.Name,
		"server_url":            state.Target,
		"require_approval":      "always",
		"project_connection_id": state.Name,
	}, nil
}

func BuildManagedConnectorMCPConfig(
	connectionName string,
	connectorName string,
	description string,
	operations []ConnectorOperation,
) (string, error) {
	if strings.TrimSpace(connectionName) == "" || strings.TrimSpace(connectorName) == "" {
		return "", errs.Config("connection and connector names are required for MCP configuration")
	}

	if len(operations) == 0 {
		return "", errs.Config("at least one connector operation is required")
	}
	type agentParameter struct {
		Name     string                 `json:"name"`
		Required bool                   `json:"required"`
		Schema   map[string]interface{} `json:"schema"`
	}
	type mcpOperation struct {
		Name            string           `json:"name"`
		DisplayName     string           `json:"displayName"`
		Description     string           `json:"description"`
		UserParameters  []interface{}    `json:"userParameters"`
		AgentParameters []agentParameter `json:"agentParameters"`
	}
	seen := make(map[string]struct{}, len(operations))
	normalized := append([]ConnectorOperation(nil), operations...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Name < normalized[j].Name })
	builtOperations := make([]mcpOperation, 0, len(normalized))
	for _, operation := range normalized {
		if strings.TrimSpace(operation.Name) == "" {
			return "", errs.Config("connector operation name is required")
		}
		if _, found := seen[operation.Name]; found {
			return "", errs.Config("connector operation %q was selected more than once", operation.Name)
		}
		seen[operation.Name] = struct{}{}
		required := make(map[string]struct{}, len(operation.InputsDefinition.Required))
		for _, name := range operation.InputsDefinition.Required {
			required[name] = struct{}{}
		}
		parameterNames := make([]string, 0, len(operation.InputsDefinition.Properties))
		for name := range operation.InputsDefinition.Properties {
			parameterNames = append(parameterNames, name)
		}
		sort.Strings(parameterNames)
		parameters := make([]agentParameter, 0, len(parameterNames))
		for _, name := range parameterNames {
			source := operation.InputsDefinition.Properties[name]
			schema := make(map[string]interface{})
			parameterType, ok := source["type"].(string)
			if !ok || strings.TrimSpace(parameterType) == "" {
				return "", errs.Config(
					"connector operation %q parameter %q has no documented JSON type",
					operation.Name,
					name,
				)
			}
			schema["type"] = parameterType
			if value, ok := source["description"].(string); ok && value != "" {
				schema["description"] = value
			}
			if value, ok := source["title"].(string); ok && value != "" {
				schema["x-ms-summary"] = value
			} else if value, ok := source["x-ms-summary"].(string); ok && value != "" {
				schema["x-ms-summary"] = value
			}
			_, isRequired := required[name]
			parameters = append(parameters, agentParameter{
				Name: name, Required: isRequired, Schema: schema,
			})
		}
		displayName := operation.Summary
		if displayName == "" {
			displayName = operation.Name
		}
		builtOperations = append(builtOperations, mcpOperation{
			Name: operation.Name, DisplayName: displayName,
			Description: operation.Description, UserParameters: []interface{}{},
			AgentParameters: parameters,
		})
	}
	payload := map[string]interface{}{
		"description": description,
		"state":       "Enabled",
		"connectors": []interface{}{map[string]interface{}{
			"name":           connectorName,
			"connectionName": connectionName,
			"displayName":    connectorName,
			"description":    description,
			"operations":     builtOperations,
		}},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to encode managed connector MCP configuration")
	}
	return string(data), nil
}

func normalizeConnectorCatalogEntry(raw connectorCatalogRawEntry) (ConnectorCatalogEntry, error) {
	entry := ConnectorCatalogEntry{
		EntityID:    strings.TrimSpace(raw.EntityID),
		Name:        stringMapValue(raw.Annotations, "name"),
		Title:       stringMapValue(raw.Properties, "title"),
		Description: stringMapValue(raw.Properties, "description"),
		Publisher:   connectorPublisher(raw.Properties),
		AuthType:    connectorAuthType(raw.Properties),
		Actions:     connectorCatalogActions(raw.Properties),
	}
	if entry.EntityID == "" || entry.Name == "" {
		return ConnectorCatalogEntry{}, errs.Foundry("connector catalog entry is missing entityId or annotations.name")
	}
	if entry.Title == "" {
		entry.Title = entry.Name
	}
	return entry, nil
}

func connectorAuthType(properties map[string]interface{}) string {
	parameters, _ := properties["x-ms-connection-parameters"].(map[string]interface{})
	result := "None"
	for _, raw := range parameters {
		parameter, _ := raw.(map[string]interface{})
		switch strings.ToLower(stringMapValue(parameter, "type")) {
		case "oauthsetting":
			return "OAuth2"
		case "securestring":
			result = "CustomKeys"
		}
	}
	return result
}

func connectorPublisher(properties map[string]interface{}) string {
	for _, key := range []string{"publisher", "publisherName", "brandColor"} {
		if key == "brandColor" {
			continue
		}
		if value := stringMapValue(properties, key); value != "" {
			return value
		}
	}
	return ""
}

func connectorCatalogActions(properties map[string]interface{}) []ConnectorCatalogAction {
	items, _ := properties["actions"].([]interface{})
	result := make([]ConnectorCatalogAction, 0, len(items))
	for _, raw := range items {
		action, _ := raw.(map[string]interface{})
		name := stringMapValue(action, "name")
		if name == "" {
			name = stringMapValue(action, "operationId")
		}
		if name == "" {
			continue
		}
		title := stringMapValue(action, "title")
		if title == "" {
			title = stringMapValue(action, "summary")
		}
		result = append(result, ConnectorCatalogAction{
			Name: name, Title: title, Description: stringMapValue(action, "description"),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func managedConnectorBody(definition ManagedConnectorDefinition) (map[string]interface{}, error) {
	for field, value := range map[string]string{
		"name": definition.Name, "connector name": definition.ConnectorName,
		"tool entity id": definition.ToolEntityID,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
			return nil, errs.Config("managed connector %s is required", field)
		}
	}
	connectionProperties, err := json.Marshal(map[string]string{
		"connectorName": definition.ConnectorName,
	})
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to encode managed connector properties")
	}
	metadata := map[string]interface{}{
		"type":                 "gateway_connector",
		"toolEntityId":         definition.ToolEntityID,
		"connectionproperties": string(connectionProperties),
	}
	if strings.TrimSpace(definition.MCPServerConfig) != "" {
		var validate interface{}
		if err := json.Unmarshal([]byte(definition.MCPServerConfig), &validate); err != nil {
			return nil, errs.Config("managed connector MCP configuration is invalid JSON: %v", err)
		}
		metadata["mcpserverConfigProperties"] = definition.MCPServerConfig
	}
	return map[string]interface{}{
		"properties": map[string]interface{}{
			"authType":      "OAuth2",
			"category":      "RemoteTool",
			"connectorName": definition.ConnectorName,
			"target":        managedConnectorPlaceholderURL,
			"credentials":   map[string]interface{}{},
			"peRequirement": "NotRequired",
			"metadata":      metadata,
		},
	}, nil
}

func managedConnectorState(state State) (ManagedConnectorState, error) {
	if !state.Exists {
		return ManagedConnectorState{Exists: false, Name: state.Name}, nil
	}
	properties := state.Properties
	if !strings.EqualFold(stringMapValue(properties, "category"), "RemoteTool") ||
		!strings.EqualFold(stringMapValue(properties, "authType"), "OAuth2") {
		return ManagedConnectorState{}, errs.Config(
			"connection %q is not an OAuth2 RemoteTool managed connector",
			state.Name,
		)
	}
	metadata, _ := properties["metadata"].(map[string]interface{})
	if !strings.EqualFold(stringMapValue(metadata, "type"), "gateway_connector") {
		return ManagedConnectorState{}, errs.Config(
			"connection %q is not a gateway_connector managed MCP server",
			state.Name,
		)
	}
	configValue := stringMapValue(metadata, "mcpserverConfigProperties")
	connectorName := stringMapValue(properties, "connectorName")
	if connectorName == "" {
		var connectionProperties map[string]interface{}
		if raw := stringMapValue(metadata, "connectionproperties"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &connectionProperties); err != nil {
				return ManagedConnectorState{}, errs.FoundryWrap(
					err,
					"connection %q has invalid connector metadata",
					state.Name,
				)
			}
			connectorName = stringMapValue(connectionProperties, "connectorName")
		}
	}
	if connectorName == "" {
		return ManagedConnectorState{}, errs.Foundry(
			"connection %q is missing its managed connector name",
			state.Name,
		)
	}
	return ManagedConnectorState{
		Exists:            true,
		Name:              state.Name,
		ID:                state.ID,
		ConnectorName:     connectorName,
		ToolEntityID:      stringMapValue(metadata, "toolEntityId"),
		OverallStatus:     stringMapValue(properties, "overallStatus"),
		Target:            stringMapValue(properties, "target"),
		ActionsConfigured: strings.TrimSpace(configValue) != "",
		MCPServerConfig:   configValue,
	}, nil
}

type connectorOperationPayload struct {
	Name       string `json:"name"`
	Properties struct {
		Summary          string                    `json:"summary"`
		Description      string                    `json:"description"`
		IsWebhook        bool                      `json:"isWebhook"`
		IsNotification   bool                      `json:"isNotification"`
		InputsDefinition ConnectorInputsDefinition `json:"inputsDefinition"`
	} `json:"properties"`
}

func normalizeConnectorOperation(payload connectorOperationPayload) (ConnectorOperation, error) {
	if strings.TrimSpace(payload.Name) == "" {
		return ConnectorOperation{}, errs.Foundry("connector operation response is missing a name")
	}
	if payload.Properties.InputsDefinition.Properties == nil {
		payload.Properties.InputsDefinition.Properties = map[string]map[string]interface{}{}
	}
	return ConnectorOperation{
		Name:             payload.Name,
		Summary:          payload.Properties.Summary,
		Description:      payload.Properties.Description,
		IsWebhook:        payload.Properties.IsWebhook,
		IsNotification:   payload.Properties.IsNotification,
		InputsDefinition: payload.Properties.InputsDefinition,
	}, nil
}

func connectorOperationsURL(
	project *config.ProjectSpec,
	connectorName string,
	operationName string,
) (string, error) {
	connectorName = strings.TrimSpace(connectorName)
	if connectorName == "" || strings.ContainsRune(connectorName, '\x00') {
		return "", errs.Config("connector name is required")
	}
	segments := []string{
		"subscriptions", project.SubscriptionID,
		"providers", "Microsoft.Web",
		"locations", ConnectorOperationsLocation,
		"managedApis", connectorName,
		"apiOperations",
	}
	if operationName != "" {
		segments = append(segments, operationName)
	}
	result, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		ConnectorOperationsAPIVersion,
		segments...,
	)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to build connector operation URL")
	}
	return result, nil
}

func doConnectorCatalog(
	ctx context.Context,
	body []byte,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (*http.Response, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	token, err := credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{ConnectorCatalogScope},
	})
	if err != nil {
		return nil, errs.AuthWrap(err, "failed to get Foundry Tools Catalog token")
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		ConnectorCatalogEndpoint,
		strings.NewReader(string(body)),
	)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to create connector catalog request")
	}
	request.Header.Set("Authorization", "Bearer "+token.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-ms-user-agent", "AzureMachineLearningWorkspacePortal/12.0")
	return httpClient.Do(request)
}

func validateHTTPSValue(raw string, field string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil ||
		!parsed.IsAbs() ||
		!strings.EqualFold(parsed.Scheme, "https") ||
		parsed.Hostname() == "" ||
		parsed.User != nil {
		return errs.Config("%s must be an absolute HTTPS URL without embedded credentials", field)
	}
	return nil
}

func stringMapValue(source map[string]interface{}, key string) string {
	value, _ := source[key].(string)
	return strings.TrimSpace(value)
}
