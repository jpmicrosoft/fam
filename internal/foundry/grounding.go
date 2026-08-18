package foundry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
)

// OpenAIFile is a project file uploaded for assistants/file-search use.
type OpenAIFile struct {
	ID        string `json:"id" yaml:"id"`
	Filename  string `json:"filename" yaml:"filename"`
	Bytes     int64  `json:"bytes" yaml:"bytes"`
	CreatedAt int64  `json:"created_at,omitempty" yaml:"createdAt,omitempty"`
	Purpose   string `json:"purpose,omitempty" yaml:"purpose,omitempty"`
	Status    string `json:"status,omitempty" yaml:"status,omitempty"`
}

// VectorStore is a Foundry/OpenAI vector store used by file search.
type VectorStore struct {
	ID          string                 `json:"id" yaml:"id"`
	Name        string                 `json:"name" yaml:"name"`
	Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
	Status      string                 `json:"status,omitempty" yaml:"status,omitempty"`
	CreatedAt   int64                  `json:"created_at,omitempty" yaml:"createdAt,omitempty"`
	UsageBytes  int64                  `json:"usage_bytes,omitempty" yaml:"usageBytes,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	FileCounts  VectorStoreFileCounts  `json:"file_counts,omitempty" yaml:"fileCounts,omitempty"`
}

// VectorStoreFileCounts summarizes ingestion state.
type VectorStoreFileCounts struct {
	InProgress int `json:"in_progress,omitempty" yaml:"inProgress,omitempty"`
	Completed  int `json:"completed,omitempty" yaml:"completed,omitempty"`
	Failed     int `json:"failed,omitempty" yaml:"failed,omitempty"`
	Cancelled  int `json:"cancelled,omitempty" yaml:"cancelled,omitempty"`
	Total      int `json:"total,omitempty" yaml:"total,omitempty"`
}

// VectorStoreFile is one file attachment and its indexing state.
type VectorStoreFile struct {
	ID            string                 `json:"id" yaml:"id"`
	VectorStoreID string                 `json:"vector_store_id,omitempty" yaml:"vectorStoreId,omitempty"`
	Status        string                 `json:"status,omitempty" yaml:"status,omitempty"`
	UsageBytes    int64                  `json:"usage_bytes,omitempty" yaml:"usageBytes,omitempty"`
	Attributes    map[string]interface{} `json:"attributes,omitempty" yaml:"attributes,omitempty"`
	LastError     map[string]interface{} `json:"last_error,omitempty" yaml:"lastError,omitempty"`
}

func openAIPath(resource string) string {
	return "/openai/v1/" + strings.TrimLeft(resource, "/")
}

// ListVectorStoresContext returns all vector stores visible in the project.
func (c *Client) ListVectorStoresContext(ctx context.Context) ([]VectorStore, error) {
	var result []VectorStore
	path := openAIPath("vector_stores?limit=100")
	for {
		resp, err := c.doWithOptions(
			ctx,
			http.MethodGet,
			path,
			nil,
			requestOptions{suppressPreview: true, omitAPIVersion: true},
		)
		if err != nil {
			return nil, errs.FoundryWrap(err, "failed to list vector stores")
		}
		data, readErr := readBody(resp)
		resp.Body.Close()
		if readErr != nil {
			return nil, errs.FoundryWrap(readErr, "failed to read vector-store list response")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, httpx.ResponseError("Foundry", "list vector stores", resp, data)
		}
		var page struct {
			Data    []VectorStore `json:"data"`
			HasMore bool          `json:"has_more"`
			LastID  string        `json:"last_id"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse vector-store list response")
		}
		result = append(result, page.Data...)
		if !page.HasMore || page.LastID == "" {
			return result, nil
		}
		path = openAIPath("vector_stores?limit=100&after=" + url.QueryEscape(page.LastID))
	}
}

// GetVectorStoreContext returns nil when a vector store does not exist.
func (c *Client) GetVectorStoreContext(
	ctx context.Context,
	vectorStoreID string,
) (*VectorStore, error) {
	resp, err := c.doWithOptions(
		ctx,
		http.MethodGet,
		openAIPath("vector_stores/"+url.PathEscape(vectorStoreID)),
		nil,
		requestOptions{suppressPreview: true, omitAPIVersion: true},
	)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to get vector store %q", vectorStoreID)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read vector store %q response", vectorStoreID)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("get vector store %q", vectorStoreID),
			resp,
			data,
		)
	}
	var result VectorStore
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse vector store %q response", vectorStoreID)
	}
	return &result, nil
}

// CreateVectorStoreContext creates an empty manager-owned vector store.
func (c *Client) CreateVectorStoreContext(
	ctx context.Context,
	name string,
	_ string,
	metadata map[string]interface{},
) (*VectorStore, error) {
	payload := map[string]interface{}{
		"name":     name,
		"metadata": metadata,
	}
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPost,
		openAIPath("vector_stores"),
		payload,
		requestOptions{suppressPreview: true, omitAPIVersion: true},
	)
	if err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to create vector store %q", name),
		)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read create response for vector store %q", name),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("create vector store %q", name),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var result VectorStore
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to parse create response for vector store %q", name),
		)
	}
	return &result, nil
}

// UpdateVectorStoreContext replaces manager-owned metadata after a successful sync.
func (c *Client) UpdateVectorStoreContext(
	ctx context.Context,
	vectorStoreID string,
	name string,
	_ string,
	metadata map[string]interface{},
) (*VectorStore, error) {
	payload := map[string]interface{}{
		"name":     name,
		"metadata": metadata,
	}
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPost,
		openAIPath("vector_stores/"+url.PathEscape(vectorStoreID)),
		payload,
		requestOptions{suppressPreview: true, omitAPIVersion: true},
	)
	if err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to update vector store %q", vectorStoreID),
		)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read update response for vector store %q", vectorStoreID),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("update vector store %q", vectorStoreID),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var result VectorStore
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to parse update response for vector store %q", vectorStoreID),
		)
	}
	return &result, nil
}

// DeleteVectorStoreContext deletes one vector store idempotently.
func (c *Client) DeleteVectorStoreContext(
	ctx context.Context,
	vectorStoreID string,
) (bool, error) {
	resp, err := c.doWithOptions(
		ctx,
		http.MethodDelete,
		openAIPath("vector_stores/"+url.PathEscape(vectorStoreID)),
		nil,
		requestOptions{suppressPreview: true, omitAPIVersion: true},
	)
	if err != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to delete vector store %q", vectorStoreID),
		)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read delete response for vector store %q", vectorStoreID),
		)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("delete vector store %q", vectorStoreID),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return false, errs.AmbiguousMutation(responseErr)
		}
		return false, responseErr
	}
	return true, nil
}

// ListVectorStoreFilesContext returns every file attached to a vector store.
func (c *Client) ListVectorStoreFilesContext(
	ctx context.Context,
	vectorStoreID string,
) ([]VectorStoreFile, error) {
	var result []VectorStoreFile
	base := "vector_stores/" + url.PathEscape(vectorStoreID) + "/files"
	path := openAIPath(base + "?limit=100")
	for {
		resp, err := c.doWithOptions(
			ctx,
			http.MethodGet,
			path,
			nil,
			requestOptions{suppressPreview: true, omitAPIVersion: true},
		)
		if err != nil {
			return nil, errs.FoundryWrap(err, "failed to list files in vector store %q", vectorStoreID)
		}
		data, readErr := readBody(resp)
		resp.Body.Close()
		if readErr != nil {
			return nil, errs.FoundryWrap(readErr, "failed to read vector-store file list response")
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, httpx.ResponseError(
				"Foundry",
				fmt.Sprintf("list files in vector store %q", vectorStoreID),
				resp,
				data,
			)
		}
		var page struct {
			Data    []VectorStoreFile `json:"data"`
			HasMore bool              `json:"has_more"`
			LastID  string            `json:"last_id"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse vector-store file list response")
		}
		result = append(result, page.Data...)
		if !page.HasMore || page.LastID == "" {
			return result, nil
		}
		path = openAIPath(base + "?limit=100&after=" + url.QueryEscape(page.LastID))
	}
}

// GetVectorStoreFileContext returns nil when an attachment does not exist.
func (c *Client) GetVectorStoreFileContext(
	ctx context.Context,
	vectorStoreID string,
	fileID string,
) (*VectorStoreFile, error) {
	path := openAIPath(
		"vector_stores/" + url.PathEscape(vectorStoreID) +
			"/files/" + url.PathEscape(fileID),
	)
	resp, err := c.doWithOptions(
		ctx,
		http.MethodGet,
		path,
		nil,
		requestOptions{suppressPreview: true, omitAPIVersion: true},
	)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to get vector-store file %q", fileID)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read vector-store file %q response", fileID)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("get vector-store file %q", fileID),
			resp,
			data,
		)
	}
	var result VectorStoreFile
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse vector-store file %q response", fileID)
	}
	return &result, nil
}

// AttachVectorStoreFileContext attaches an uploaded file with reconciliation attributes.
func (c *Client) AttachVectorStoreFileContext(
	ctx context.Context,
	vectorStoreID string,
	fileID string,
	attributes map[string]interface{},
) (*VectorStoreFile, error) {
	path := openAIPath("vector_stores/" + url.PathEscape(vectorStoreID) + "/files")
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPost,
		path,
		map[string]interface{}{
			"file_id":    fileID,
			"attributes": attributes,
		},
		requestOptions{suppressPreview: true, omitAPIVersion: true},
	)
	if err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to attach file %q to vector store %q", fileID, vectorStoreID),
		)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read attach response for file %q", fileID),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("attach file %q to vector store %q", fileID, vectorStoreID),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var result VectorStoreFile
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to parse attach response for file %q", fileID),
		)
	}
	return &result, nil
}

// DeleteVectorStoreFileContext detaches one file idempotently.
func (c *Client) DeleteVectorStoreFileContext(
	ctx context.Context,
	vectorStoreID string,
	fileID string,
) (bool, error) {
	path := openAIPath(
		"vector_stores/" + url.PathEscape(vectorStoreID) +
			"/files/" + url.PathEscape(fileID),
	)
	resp, err := c.doWithOptions(
		ctx,
		http.MethodDelete,
		path,
		nil,
		requestOptions{suppressPreview: true, omitAPIVersion: true},
	)
	if err != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to detach file %q from vector store %q", fileID, vectorStoreID),
		)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read detach response for file %q", fileID),
		)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("detach file %q from vector store %q", fileID, vectorStoreID),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return false, errs.AmbiguousMutation(responseErr)
		}
		return false, responseErr
	}
	return true, nil
}

// UploadFileContext streams a local document through a secured temporary
// multipart body. Upload POSTs are never retried automatically.
func (c *Client) UploadFileContext(
	ctx context.Context,
	filename string,
	source io.Reader,
) (*OpenAIFile, error) {
	temp, err := os.CreateTemp("", "foundry-agent-manager-upload-*.multipart")
	if err != nil {
		return nil, errs.Config("failed to create secured upload body: %v", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	defer temp.Close()
	if err := temp.Chmod(0o600); err != nil {
		return nil, errs.Config("failed to secure upload body: %v", err)
	}
	writer := multipart.NewWriter(temp)
	if err := writer.WriteField("purpose", "assistants"); err != nil {
		return nil, errs.Config("failed to encode upload purpose: %v", err)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, errs.Config("failed to encode upload filename: %v", err)
	}
	if _, err := io.Copy(part, source); err != nil {
		return nil, errs.Config("failed to stream %q into upload body: %v", filename, err)
	}
	if err := writer.Close(); err != nil {
		return nil, errs.Config("failed to finalize upload body: %v", err)
	}
	info, err := temp.Stat()
	if err != nil {
		return nil, errs.Config("failed to inspect upload body: %v", err)
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return nil, errs.Config("failed to rewind upload body: %v", err)
	}
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPost,
		openAIPath("files"),
		rawRequestBody{reader: temp, contentLength: info.Size()},
		requestOptions{
			contentType:     writer.FormDataContentType(),
			omitAPIVersion:  true,
			suppressPreview: true,
		},
	)
	if err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to upload file %q", filename),
		)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read upload response for %q", filename),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("upload file %q", filename),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var result OpenAIFile
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to parse upload response for %q", filename),
		)
	}
	return &result, nil
}

// GetFileContext returns nil when an uploaded file does not exist.
func (c *Client) GetFileContext(ctx context.Context, fileID string) (*OpenAIFile, error) {
	resp, err := c.doWithOptions(
		ctx,
		http.MethodGet,
		openAIPath("files/"+url.PathEscape(fileID)),
		nil,
		requestOptions{suppressPreview: true, omitAPIVersion: true},
	)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to get uploaded file %q", fileID)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read uploaded file %q response", fileID)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("get uploaded file %q", fileID),
			resp,
			data,
		)
	}
	var result OpenAIFile
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse uploaded file %q response", fileID)
	}
	return &result, nil
}

// DeleteFileContext deletes an uploaded project file idempotently.
func (c *Client) DeleteFileContext(ctx context.Context, fileID string) (bool, error) {
	resp, err := c.doWithOptions(
		ctx,
		http.MethodDelete,
		openAIPath("files/"+url.PathEscape(fileID)),
		nil,
		requestOptions{suppressPreview: true, omitAPIVersion: true},
	)
	if err != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to delete uploaded file %q", fileID),
		)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read delete response for uploaded file %q", fileID),
		)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("delete uploaded file %q", fileID),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return false, errs.AmbiguousMutation(responseErr)
		}
		return false, responseErr
	}
	return true, nil
}

// WaitForVectorStoreFileContext waits until one attachment is indexed.
func (c *Client) WaitForVectorStoreFileContext(
	ctx context.Context,
	vectorStoreID string,
	fileID string,
	timeout time.Duration,
	interval time.Duration,
) (*VectorStoreFile, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		found, err := c.GetVectorStoreFileContext(waitCtx, vectorStoreID, fileID)
		if err != nil {
			return nil, err
		}
		if found != nil {
			switch strings.ToLower(found.Status) {
			case "completed":
				return found, nil
			case "failed", "cancelled":
				detail, _ := json.Marshal(found.LastError)
				return nil, errs.Foundry(
					"vector-store file %q entered %s state: %s",
					fileID,
					found.Status,
					strings.TrimSpace(string(detail)),
				)
			}
		}
		if err := waitInterval(waitCtx, interval); err != nil {
			return nil, errs.Transient(
				"timed out waiting for vector-store file %q to finish indexing: %v",
				fileID,
				err,
			)
		}
	}
}

// WaitForVectorStoreContext waits until the vector store is ready for search.
func (c *Client) WaitForVectorStoreContext(
	ctx context.Context,
	vectorStoreID string,
	timeout time.Duration,
	interval time.Duration,
) (*VectorStore, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		found, err := c.GetVectorStoreContext(waitCtx, vectorStoreID)
		if err != nil {
			return nil, err
		}
		if found != nil {
			switch strings.ToLower(found.Status) {
			case "completed":
				return found, nil
			case "expired":
				return nil, errs.Conflict("vector store %q has expired", vectorStoreID)
			}
		}
		if err := waitInterval(waitCtx, interval); err != nil {
			return nil, errs.Transient(
				"timed out waiting for vector store %q to become ready: %v",
				vectorStoreID,
				err,
			)
		}
	}
}

func waitInterval(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
