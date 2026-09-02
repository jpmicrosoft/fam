package connection

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestListConnectorCatalogUsesDocumentedContract(t *testing.T) {
	credential := &recordingCredential{}
	client := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, `{
			"totalCount":1,
			"value":[{
				"entityId":"entity-1",
				"annotations":{"name":"github"},
				"properties":{
					"title":"GitHub",
					"description":"GitHub connector",
					"x-ms-connection-parameters":{"token":{"type":"oauthSetting"}},
					"actions":[{"name":"CreateIssue","title":"Create issue"}]
				}
			}]
		}`),
	}}
	result, err := ListConnectorCatalogContext(
		context.Background(),
		ConnectorCatalogQuery{Search: "git", PageSize: 25, Skip: 50},
		credential,
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalCount != 1 || len(result.Connectors) != 1 ||
		result.Connectors[0].Name != "github" ||
		result.Connectors[0].AuthType != "OAuth2" ||
		len(result.Connectors[0].Actions) != 1 {
		t.Fatalf("unexpected catalog result: %#v", result)
	}
	if len(credential.scopes) != 1 ||
		len(credential.scopes[0]) != 1 ||
		credential.scopes[0][0] != ConnectorCatalogScope {
		t.Fatalf("unexpected catalog token scopes: %#v", credential.scopes)
	}
	request := client.requests[0]
	if request.Method != http.MethodPost ||
		request.URL.String() != ConnectorCatalogEndpoint ||
		request.Header.Get("x-ms-user-agent") != "AzureMachineLearningWorkspacePortal/12.0" {
		t.Fatalf("unexpected catalog request: %s %s", request.Method, request.URL)
	}
	data, _ := io.ReadAll(request.Body)
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body["freeTextSearch"] != "*" ||
		int(body["pageSize"].(float64)) != 25 ||
		int(body["skip"].(float64)) != 50 {
		t.Fatalf("unexpected catalog body: %#v", body)
	}
	filters, ok := body["filters"].([]interface{})
	if !ok {
		t.Fatalf("unexpected catalog filters: %#v", body["filters"])
	}
	var foundSearch bool
	for _, raw := range filters {
		filter, _ := raw.(map[string]interface{})
		values, _ := filter["values"].([]interface{})
		if filter["field"] == "annotations/name" &&
			filter["operator"] == "contains" &&
			len(values) == 1 &&
			values[0] == "git" {
			foundSearch = true
		}
	}
	if !foundSearch {
		t.Fatalf("catalog request is missing the supported name search filter: %#v", filters)
	}
}

func TestManagedConnectorCreateUsesGatewayContract(t *testing.T) {
	client := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, `{
			"id":"/connections/github-conn",
			"name":"github-conn",
			"properties":{
				"authType":"OAuth2",
				"category":"RemoteTool",
				"target":"https://placeholder",
				"overallStatus":"Unauthenticated",
				"metadata":{
					"type":"gateway_connector",
					"toolEntityId":"entity-1",
					"connectionproperties":"{\"connectorName\":\"github\"}"
				}
			}
		}`),
	}}
	state, err := UpsertManagedConnectorContext(
		context.Background(),
		projectSpec(),
		"2025-04-01-preview",
		ManagedConnectorDefinition{
			Name: "github-conn", ConnectorName: "github", ToolEntityID: "entity-1",
		},
		&recordingCredential{},
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.Name != "github-conn" ||
		state.ConnectorName != "github" ||
		state.OverallStatus != "Unauthenticated" ||
		state.ActionsConfigured {
		t.Fatalf("unexpected managed connector state: %#v", state)
	}
	data, _ := io.ReadAll(client.requests[0].Body)
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	properties := body["properties"].(map[string]interface{})
	metadata := properties["metadata"].(map[string]interface{})
	if properties["authType"] != "OAuth2" ||
		properties["category"] != "RemoteTool" ||
		properties["connectorName"] != "github" ||
		properties["target"] != managedConnectorPlaceholderURL ||
		properties["peRequirement"] != "NotRequired" ||
		metadata["type"] != "gateway_connector" ||
		metadata["toolEntityId"] != "entity-1" ||
		metadata["connectionproperties"] != `{"connectorName":"github"}` {
		t.Fatalf("unexpected managed connector body: %#v", body)
	}
}

func TestConnectorConsentUsesActionAndReturnsHTTPSLink(t *testing.T) {
	client := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, `{"value":[{"link":"https://login.example.test/oauth?code=one"}]}`),
	}}
	result, err := CreateConnectorConsentLinkContext(
		context.Background(),
		projectSpec(),
		"2025-04-01-preview",
		"github-conn",
		ConnectorConsentRequest{
			ObjectID: "object-id", TenantID: "tenant-id",
			RedirectURL: DefaultConnectorRedirectURL,
		},
		&recordingCredential{},
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Link != "https://login.example.test/oauth?code=one" {
		t.Fatalf("unexpected consent result: %#v", result)
	}
	request := client.requests[0]
	if request.Method != http.MethodPost ||
		request.URL.Query().Get("action") != "listConsentLinks" ||
		request.URL.Query().Get("api-version") != "2025-04-01-preview" {
		t.Fatalf("unexpected consent request URL: %s", request.URL)
	}
	data, _ := io.ReadAll(request.Body)
	if !strings.Contains(string(data), `"parameterName":"token"`) ||
		!strings.Contains(string(data), `"objectId":"object-id"`) {
		t.Fatalf("unexpected consent body: %s", data)
	}
}

func TestConnectorOperationsFilterTriggersAndExpandSchema(t *testing.T) {
	client := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, `{"value":[
			{"name":"CreateIssue","properties":{"summary":"Create issue"}},
			{"name":"IssueCreated","properties":{"summary":"Issue created","isWebhook":true}},
			{"name":"Notification","properties":{"isNotification":true}},
			{"name":"ScheduledTrigger","properties":{"summary":"When an event occurs","trigger":"batch"}}
		]}`),
		response(http.StatusOK, `{
			"name":"CreateIssue",
			"properties":{
				"summary":"Create issue",
				"description":"Creates an issue.",
				"inputsDefinition":{
					"required":["title"],
					"properties":{
						"title":{"type":"string","title":"Issue title","description":"Title text"},
						"body":{"type":"string","description":"Body text"}
					}
				}
			}
		}`),
	}}
	operations, err := ListConnectorOperationsContext(
		context.Background(),
		projectSpec(),
		"github",
		&recordingCredential{},
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].Name != "CreateIssue" {
		t.Fatalf("unexpected operations: %#v", operations)
	}
	detail, err := GetConnectorOperationContext(
		context.Background(),
		projectSpec(),
		"github",
		"CreateIssue",
		&recordingCredential{},
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.InputsDefinition.Properties) != 2 ||
		client.requests[1].URL.Query().Get("$expand") != "properties/inputsDefinition" {
		t.Fatalf("unexpected operation detail: %#v request=%s", detail, client.requests[1].URL)
	}
}

func TestConnectorOperationRejectsTriggerMetadata(t *testing.T) {
	client := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, `{
			"name":"OnNewFeed",
			"properties":{
				"summary":"When a feed item is published",
				"trigger":"batch",
				"isWebhook":false,
				"isNotification":false
			}
		}`),
	}}
	_, err := GetConnectorOperationContext(
		context.Background(),
		projectSpec(),
		"rss",
		"OnNewFeed",
		&recordingCredential{},
		client,
	)
	if err == nil || !strings.Contains(err.Error(), "is a trigger") {
		t.Fatalf("expected trigger rejection, got %v", err)
	}
}

func TestBuildManagedConnectorMCPConfigUsesCompleteSortedAllowlist(t *testing.T) {
	configValue, err := BuildManagedConnectorMCPConfig(
		"github-conn",
		"github",
		"GitHub actions",
		[]ConnectorOperation{
			{
				Name: "CreateIssue", Summary: "Create issue", Description: "Creates an issue.",
				InputsDefinition: ConnectorInputsDefinition{
					Required: []string{"title"},
					Properties: map[string]map[string]interface{}{
						"title": {"type": "string", "title": "Issue title", "description": "Title text"},
						"body":  {"type": "string", "description": "Body text"},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		State      string `json:"state"`
		Connectors []struct {
			ConnectionName string `json:"connectionName"`
			Operations     []struct {
				Name            string `json:"name"`
				AgentParameters []struct {
					Name     string                 `json:"name"`
					Required bool                   `json:"required"`
					Schema   map[string]interface{} `json:"schema"`
				} `json:"agentParameters"`
			} `json:"operations"`
		} `json:"connectors"`
	}
	if err := json.Unmarshal([]byte(configValue), &payload); err != nil {
		t.Fatal(err)
	}
	parameters := payload.Connectors[0].Operations[0].AgentParameters
	if payload.State != "Enabled" ||
		payload.Connectors[0].ConnectionName != "github-conn" ||
		len(parameters) != 2 ||
		parameters[0].Name != "body" ||
		parameters[1].Name != "title" ||
		!parameters[1].Required ||
		parameters[1].Schema["x-ms-summary"] != "Issue title" {
		t.Fatalf("unexpected MCP configuration: %s", configValue)
	}
}

func TestManagedConnectorRegionSupportIsPinned(t *testing.T) {
	if !SupportsManagedConnectorRegion("EastUS2") ||
		SupportsManagedConnectorRegion("eastus") ||
		SupportsManagedConnectorRegion("") {
		t.Fatal("managed connector region support does not match the documented list")
	}
}

func TestManagedConnectorMCPToolRequiresConnectedConfiguredState(t *testing.T) {
	tool, err := ManagedConnectorMCPTool(ManagedConnectorState{
		Exists: true, Name: "github-conn", OverallStatus: "Connected",
		ActionsConfigured: true,
		Target:            "https://app-01.eastus.logic.azure.com/api/connectorGateways/env/mcpServerConfigs/github-conn/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tool["type"] != "mcp" ||
		tool["server_label"] != "github-conn" ||
		tool["project_connection_id"] != "github-conn" ||
		tool["require_approval"] != "always" {
		t.Fatalf("unexpected MCP tool: %#v", tool)
	}
	notReady, err := ManagedConnectorMCPTool(ManagedConnectorState{
		Exists: true, Name: "github-conn", OverallStatus: "Unauthenticated",
	})
	if err != nil || notReady != nil {
		t.Fatalf("unready connector produced a tool: %#v err=%v", notReady, err)
	}
}
