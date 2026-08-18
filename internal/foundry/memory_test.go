package foundry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCreateMemoryStoreUsesPreviewContract(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{jsonResp(200, map[string]interface{}{
		"id":   "store-1",
		"name": "assistant-memory",
		"definition": map[string]interface{}{
			"kind":            "default",
			"chat_model":      "chat",
			"embedding_model": "embed",
		},
	})}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, true)
	result, err := client.CreateMemoryStoreContext(context.Background(), MemoryStore{
		Name: "assistant-memory",
		Definition: MemoryStoreDefinition{
			ChatModel:      "chat",
			EmbeddingModel: "embed",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "store-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	request := mock.requests[0]
	if request.Method != http.MethodPost ||
		request.URL.Path != "/api/projects/p/memory_stores" ||
		request.URL.Query().Get("api-version") != MemoryAPIVersion ||
		request.Header.Get("Foundry-Features") != "" {
		t.Fatalf("unexpected request: %s %s %#v", request.Method, request.URL, request.Header)
	}
	data, _ := io.ReadAll(request.Body)
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	definition := body["definition"].(map[string]interface{})
	if definition["kind"] != "default" {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestListMemoryItemsUsesCurrentRESTBodyContract(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{jsonResp(200, map[string]interface{}{
		"data": []interface{}{map[string]interface{}{
			"memory_id": "memory-1",
			"scope":     "user-1",
			"kind":      "user_profile",
			"content":   "Prefers concise answers.",
		}},
	})}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	items, err := client.ListMemoryItemsContext(
		context.Background(),
		"assistant-memory",
		"user-1",
		"user_profile",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].MemoryID != "memory-1" {
		t.Fatalf("unexpected items: %#v", items)
	}
	request := mock.requests[0]
	if request.Method != http.MethodPost ||
		request.URL.Path != "/api/projects/p/memory_stores/assistant-memory/items:list" {
		t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
	}
	data, _ := io.ReadAll(request.Body)
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body["scope"] != "user-1" ||
		body["kind"] != "user_profile" ||
		body["limit"] != float64(100) {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestUpdateMemoriesPollsSameProjectOperation(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		{
			StatusCode: http.StatusAccepted,
			Header: http.Header{
				"Operation-Location": []string{
					"https://acct.services.ai.azure.com/api/projects/p/memory_stores/assistant-memory/updates/update-1",
				},
			},
			Body: io.NopCloser(strings.NewReader(`{"update_id":"update-1","status":"queued"}`)),
		},
		jsonResp(200, map[string]interface{}{"update_id": "update-1", "status": "completed"}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	result, err := client.UpdateMemoriesContext(
		context.Background(),
		"assistant-memory",
		map[string]interface{}{"scope": "user-1", "items": []interface{}{}},
		time.Second,
		time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || len(mock.requests) != 2 {
		t.Fatalf("unexpected update result: %#v requests=%d", result, len(mock.requests))
	}
}
