package foundry

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func TestCreateSkillInlineUsesPreviewHeaderAndDocumentedBody(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{jsonResp(200, map[string]interface{}{
		"id": "version-1", "skill_id": "skill-1", "name": "summarizer", "version": "1",
	})}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	result, err := client.CreateSkillInlineContext(
		context.Background(),
		"summarizer",
		SkillInlineContent{Description: "Summarize documents.", Instructions: "Read and summarize."},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	request := mock.requests[0]
	if request.Method != http.MethodPost ||
		request.URL.Path != "/api/projects/p/skills/summarizer/versions" ||
		request.URL.Query().Get("api-version") != apiVersion ||
		request.Header.Get("Foundry-Features") != skillsPreviewHeader {
		t.Fatalf("unexpected request: %s %s %#v", request.Method, request.URL, request.Header)
	}
	data, _ := io.ReadAll(request.Body)
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body["default"] != true {
		t.Fatalf("unexpected body: %#v", body)
	}
	inline := body["inline_content"].(map[string]interface{})
	if inline["description"] != "Summarize documents." ||
		inline["instructions"] != "Read and summarize." {
		t.Fatalf("unexpected inline content: %#v", inline)
	}
}

func TestCreateSkillFromFilesUsesMultipartFilePaths(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{jsonResp(200, map[string]interface{}{
		"id": "version-1", "skill_id": "skill-1", "name": "docs", "version": "1",
	})}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	_, err := client.CreateSkillFromFilesContext(
		context.Background(),
		"docs",
		[]SkillFile{
			{Name: "SKILL.md", Data: []byte("# Skill")},
			{Name: "references/example.txt", Data: []byte("example")},
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := mock.requests[0]
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("unexpected content type %q: %v", request.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(request.Body, parameters["boundary"])
	var names []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() == "files" {
			_, disposition, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, disposition["filename"])
		}
	}
	if strings.Join(names, ",") != "SKILL.md,references/example.txt" {
		t.Fatalf("unexpected uploaded names: %#v", names)
	}
}

func TestDownloadSkillUsesZipAcceptHeader(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/zip"}},
		Body:       io.NopCloser(strings.NewReader("zip-data")),
	}}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	data, err := client.DownloadSkillContext(context.Background(), "docs", "2")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "zip-data" ||
		mock.requests[0].Header.Get("Accept") != "application/zip" ||
		mock.requests[0].Header.Get("Foundry-Features") != skillsPreviewHeader {
		t.Fatalf("unexpected download request or data")
	}
}
