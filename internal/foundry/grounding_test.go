package foundry

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type captureHTTP struct {
	requests []*http.Request
	bodies   [][]byte
	routes   []*http.Response
}

func (c *captureHTTP) Do(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	c.requests = append(c.requests, req)
	c.bodies = append(c.bodies, body)
	response := c.routes[0]
	c.routes = c.routes[1:]
	response.Request = req
	return response, nil
}

func TestUploadProjectFileUsesAssistantsMultipartContract(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "guide.txt")
	if err := os.WriteFile(path, []byte("grounding content"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	httpClient := &captureHTTP{routes: []*http.Response{
		jsonResp(http.StatusCreated, map[string]interface{}{
			"id": "file-1", "filename": "guide.txt", "purpose": "assistants",
		}),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		httpClient,
		false,
	)
	uploaded, err := client.UploadFileContext(
		context.Background(),
		"guide.txt",
		file,
	)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.ID != "file-1" || uploaded.Purpose != "assistants" {
		t.Fatalf("unexpected upload response: %#v", uploaded)
	}

	request := httpClient.requests[0]
	if request.Method != http.MethodPost ||
		request.URL.Path != "/api/projects/project/openai/v1/files" ||
		request.URL.RawQuery != "" {
		t.Fatalf("unexpected upload request: %s %s", request.Method, request.URL.String())
	}
	if request.ContentLength != int64(len(httpClient.bodies[0])) {
		t.Fatalf(
			"content length mismatch: header=%d body=%d",
			request.ContentLength,
			len(httpClient.bodies[0]),
		)
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("unexpected content type %q: %v", request.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(bytes.NewReader(httpClient.bodies[0]), parameters["boundary"])
	parts := map[string]string{}
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			t.Fatal(partErr)
		}
		data, readErr := io.ReadAll(part)
		if readErr != nil {
			t.Fatal(readErr)
		}
		parts[part.FormName()] = string(data)
		if part.FormName() == "file" && part.FileName() != "guide.txt" {
			t.Fatalf("unexpected upload filename: %q", part.FileName())
		}
	}
	if parts["purpose"] != "assistants" || parts["file"] != "grounding content" {
		t.Fatalf("unexpected multipart fields: %#v", parts)
	}
}

func TestListVectorStoresFollowsPagination(t *testing.T) {
	httpClient := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{"id": "vs-1", "name": "one"},
			},
			"has_more": true,
			"last_id":  "vs-1",
		}),
		jsonResp(http.StatusOK, map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{"id": "vs-2", "name": "two"},
			},
			"has_more": false,
		}),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		httpClient,
		false,
	)
	stores, err := client.ListVectorStoresContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 2 || stores[0].ID != "vs-1" || stores[1].ID != "vs-2" {
		t.Fatalf("unexpected stores: %#v", stores)
	}
	if len(httpClient.requests) != 2 ||
		httpClient.requests[1].URL.Query().Get("after") != "vs-1" {
		t.Fatalf("pagination cursor was not sent: %#v", httpClient.requests)
	}
}

func TestCreateVectorStoreSendsSupportedManagedFields(t *testing.T) {
	httpClient := &captureHTTP{routes: []*http.Response{
		jsonResp(http.StatusCreated, map[string]interface{}{
			"id": "vs-1", "name": "docs", "status": "completed",
		}),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		httpClient,
		false,
	)
	_, err := client.CreateVectorStoreContext(
		context.Background(),
		"docs",
		"Product docs.",
		map[string]interface{}{"foundry_agent_manager": "true"},
	)
	if err != nil {
		t.Fatal(err)
	}
	body := string(httpClient.bodies[0])
	for _, fragment := range []string{
		`"name":"docs"`,
		`"foundry_agent_manager":"true"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("request body is missing %s: %s", fragment, body)
		}
	}
	if strings.Contains(body, "expires_after") {
		t.Fatalf("vector-store creation must not impose an expiration: %s", body)
	}
	if strings.Contains(body, `"description"`) {
		t.Fatalf("Foundry project vector stores reject description: %s", body)
	}
}

func TestGroundingPollingHandlesCompletionAndFailure(t *testing.T) {
	successHTTP := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]interface{}{
			"id": "file-1", "status": "in_progress",
		}),
		jsonResp(http.StatusOK, map[string]interface{}{
			"id": "file-1", "status": "completed",
		}),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		successHTTP,
		false,
	)
	file, err := client.WaitForVectorStoreFileContext(
		context.Background(),
		"vs-1",
		"file-1",
		time.Second,
		time.Millisecond,
	)
	if err != nil || file.Status != "completed" {
		t.Fatalf("unexpected polling result: %#v / %v", file, err)
	}

	failedHTTP := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]interface{}{
			"id": "file-1", "status": "failed",
			"last_error": map[string]interface{}{
				"code": "parse_error", "message": "unsupported content",
			},
		}),
	}}
	client = NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		failedHTTP,
		false,
	)
	_, err = client.WaitForVectorStoreFileContext(
		context.Background(),
		"vs-1",
		"file-1",
		time.Second,
		time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "parse_error") {
		t.Fatalf("expected indexing failure details, got %v", err)
	}
}
