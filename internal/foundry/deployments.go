package foundry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
)

const maxModelDeploymentResponseBytes = int64(1024 * 1024)

// ModelDeployment is the read-only Foundry project representation of a deployed model.
type ModelDeployment struct {
	Name           string                 `json:"name" yaml:"name"`
	Type           string                 `json:"type" yaml:"type"`
	ModelName      string                 `json:"modelName" yaml:"modelName"`
	ModelPublisher string                 `json:"modelPublisher" yaml:"modelPublisher"`
	ModelVersion   string                 `json:"modelVersion" yaml:"modelVersion"`
	ConnectionName string                 `json:"connectionName,omitempty" yaml:"connectionName,omitempty"`
	Capabilities   map[string]interface{} `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

// GetModelDeploymentContext verifies that an exact deployment name is exposed
// through the selected Foundry project. It performs no model invocation.
func (c *Client) GetModelDeploymentContext(
	ctx context.Context,
	name string,
) (*ModelDeployment, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errs.Config("model deployment name is required")
	}
	resp, err := c.doWithOptions(
		ctx,
		http.MethodGet,
		"/deployments/"+url.PathEscape(name),
		nil,
		requestOptions{suppressPreview: true},
	)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to get model deployment %q", name)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.Body == nil {
		return nil, errs.Foundry("model deployment %q response omitted its body", name)
	}
	data, err := readBoundedBody(resp.Body, maxModelDeploymentResponseBytes)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read model deployment %q response", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("get model deployment %q", name),
			resp,
			data,
		)
	}
	var deployment ModelDeployment
	if err := json.Unmarshal(data, &deployment); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse model deployment %q response", name)
	}
	if deployment.Name == "" || deployment.Type != "ModelDeployment" {
		return nil, errs.Foundry(
			"model deployment %q response is missing the required name or ModelDeployment type",
			name,
		)
	}
	if deployment.Name != name {
		return nil, errs.Conflict(
			"Foundry returned model deployment name %q instead of the requested exact name %q",
			deployment.Name,
			name,
		)
	}
	return &deployment, nil
}
