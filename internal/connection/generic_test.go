package connection

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDefinitionRejectsNonHTTPSTarget(t *testing.T) {
	_, err := (Definition{
		Name: "search", Category: "CognitiveSearch", Target: "http://search.example.test",
		AuthType: "ApiKey",
	}).Body()
	if err == nil {
		t.Fatal("expected non-HTTPS connection target to fail")
	}
}

func TestUpsertContextUsesGenericARMContract(t *testing.T) {
	credential := &recordingCredential{}
	client := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusCreated, `{"name":"search"}`),
	}}
	result, err := UpsertContext(
		context.Background(),
		projectSpec(),
		"2025-04-01-preview",
		Definition{
			Name: "search", Category: "CognitiveSearch",
			Target: "https://search.example.test", AuthType: "ApiKey",
			Credentials: map[string]interface{}{"key": "secret-value"},
			Metadata:    map[string]string{"index": "docs"},
		},
		credential,
		client,
		"secret-value",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Name != "search" {
		t.Fatalf("unexpected result: %#v", result)
	}
	request := client.requests[0]
	if request.Method != http.MethodPut ||
		!strings.Contains(request.URL.Path, "/projects/project/connections/search") ||
		request.URL.Query().Get("api-version") != "2025-04-01-preview" {
		t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
	}
	data, _ := io.ReadAll(request.Body)
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	properties := body["properties"].(map[string]interface{})
	if properties["category"] != "CognitiveSearch" ||
		properties["authType"] != "ApiKey" ||
		properties["target"] != "https://search.example.test" {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestGetContextNeverReturnsCredentialValues(t *testing.T) {
	client := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, `{
			"id":"/connections/search",
			"name":"search",
			"properties":{
				"category":"CognitiveSearch",
				"target":"https://search.example.test",
				"authType":"ApiKey",
				"credentials":{"key":"must-not-leak"}
			}
		}`),
	}}
	result, err := GetContext(
		context.Background(),
		projectSpec(),
		"",
		"search",
		&recordingCredential{},
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Exists {
		t.Fatalf("unexpected state: %#v", result)
	}
	if _, exists := result.Properties["credentials"]; exists {
		t.Fatalf("credentials leaked into connection state: %#v", result.Properties)
	}
}

func TestListContextFollowsSameOriginPagination(t *testing.T) {
	client := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, `{
			"value":[{"name":"one","properties":{"credentials":{"key":"hidden"}}}],
			"nextLink":"https://management.azure.com/next?page=2"
		}`),
		response(http.StatusOK, `{
			"value":[{"name":"two","properties":{}}]
		}`),
	}}
	result, err := ListContext(
		context.Background(),
		projectSpec(),
		"",
		&recordingCredential{},
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || len(client.requests) != 2 ||
		client.requests[1].URL.Query().Get("page") != "2" {
		t.Fatalf("unexpected pagination result: %#v requests=%d", result, len(client.requests))
	}
	if _, exists := result[0].Properties["credentials"]; exists {
		t.Fatalf("credentials leaked from paginated result: %#v", result[0])
	}
}
