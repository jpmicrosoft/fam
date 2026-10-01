package foundry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"

	errs "foundry-agent-manager/internal/errors"
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

func TestSkillDownloadExactStreamingBounds(t *testing.T) {
	const limit = 64
	for _, length := range []int64{-1, 0, limit} {
		for _, count := range []int{limit - 1, limit, limit + 1, limit + 10} {
			reader := strings.NewReader(strings.Repeat("x", count))
			resp := &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: length,
				Body:          io.NopCloser(reader),
			}
			data, err := readSkillDownload(resp, "docs", limit)
			if count <= limit {
				if err != nil || len(data) != count {
					t.Fatalf("length=%d: %d bytes must pass without truncation: %v", length, count, err)
				}
			} else {
				if !errs.IsKind(err, "foundry") || data != nil {
					t.Fatalf("length=%d: %d bytes must be rejected: %v", length, count, err)
				}
				if reader.Len() != count-limit-1 {
					t.Fatal("download must stop after the guard plus one byte")
				}
			}
		}
	}
}

func TestDownloadSkillRejectsDeclaredOverflowAndClosesBody(t *testing.T) {
	if maxSkillDownloadBytes != 256<<20 {
		t.Fatal("the existing 256 MiB FAM download guard must not change")
	}
	body := &skillDownloadTestBody{Reader: iotest.ErrReader(errors.New("body must not be read"))}
	mock := &mockHTTP{responses: []*http.Response{{
		StatusCode:    http.StatusOK,
		ContentLength: maxSkillDownloadBytes + 1,
		Body:          body,
	}}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	data, err := client.DownloadSkillContext(context.Background(), "docs", "")
	if !errs.IsKind(err, "foundry") || !strings.Contains(err.Error(), "safety guard") || data != nil || !body.closed {
		t.Fatalf("declared overflow must fail before reading and close the response: %v", err)
	}
	if mock.requests[0].URL.Path != "/api/projects/p/skills/docs/content" {
		t.Fatalf("unexpected default download path: %s", mock.requests[0].URL.Path)
	}
}

func TestSkillDownloadPreservesHTTPDiagnosticsOnOverflow(t *testing.T) {
	for status, kind := range map[int]string{
		http.StatusForbidden: "authorization", http.StatusNotFound: "not_found",
		http.StatusTooManyRequests: "transient", http.StatusBadRequest: "foundry",
	} {
		reader := strings.NewReader("denied: " + strings.Repeat("x", 100))
		resp := &http.Response{
			StatusCode:    status,
			ContentLength: maxSkillDownloadBytes + 1,
			Header:        http.Header{"X-Ms-Request-Id": []string{"synthetic-request"}},
			Body:          io.NopCloser(reader),
		}
		data, err := readSkillDownload(resp, "docs", 64)
		if !errs.IsKind(err, kind) || data != nil ||
			!strings.Contains(err.Error(), "denied:") ||
			!strings.Contains(err.Error(), "synthetic-request") ||
			!strings.Contains(err.Error(), "truncated") {
			t.Fatalf("HTTP %d must retain diagnostics on overflow: %v", status, err)
		}
		if reader.Len() != 108-65 {
			t.Fatal("error response must also be bounded")
		}
	}
}

func TestSkillDownloadPreservesReadErrors(t *testing.T) {
	failure := errors.New("synthetic read failure")
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		resp := &http.Response{
			StatusCode: status,
			Header:     http.Header{"X-Ms-Request-Id": []string{"synthetic-request"}},
			Body: io.NopCloser(io.MultiReader(
				strings.NewReader("partial body"),
				iotest.ErrReader(failure),
			)),
		}
		data, err := readSkillDownload(resp, "docs", 64)
		if !errors.Is(err, failure) || data != nil {
			t.Fatalf("read error must not be swallowed or return content: %v", err)
		}
		if status == http.StatusForbidden && (!errs.IsKind(err, "authorization") ||
			!strings.Contains(err.Error(), "synthetic-request")) {
			t.Fatalf("read errors must not erase HTTP diagnostics: %v", err)
		}
	}
}

type skillDownloadTestBody struct {
	io.Reader
	closed bool
}

func (body *skillDownloadTestBody) Close() error {
	body.closed = true
	return nil
}
