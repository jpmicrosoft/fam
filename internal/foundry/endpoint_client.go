package foundry

import (
	"context"
	"fmt"
	"net/http"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
)

const mergePatchContentType = "application/merge-patch+json"

// PatchAgentDetails atomically patches top-level endpoint and agent-card
// configuration. AgentCard is never nested inside AgentEndpoint.
func (c *Client) PatchAgentDetails(name string, details AgentDetailsPatch) error {
	return c.PatchAgentDetailsContext(context.Background(), name, details)
}

// PatchAgentDetailsContext atomically patches top-level configurable details.
func (c *Client) PatchAgentDetailsContext(
	ctx context.Context,
	name string,
	details AgentDetailsPatch,
) error {
	if details.AgentEndpoint == nil && details.AgentCard == nil {
		return errs.Config("agent details patch must include agent_endpoint or agent_card")
	}
	if details.AgentEndpoint != nil {
		if err := validateVersionSelector(details.AgentEndpoint.VersionSelector); err != nil {
			return err
		}
	}
	return c.patchAgentContext(ctx, name, details)
}

// PatchAgentEndpoint applies an RFC 7396 merge patch to agent_endpoint.
// Omitted endpoint siblings are preserved by the service.
func (c *Client) PatchAgentEndpoint(name string, endpoint AgentEndpointConfig) error {
	return c.PatchAgentEndpointContext(context.Background(), name, endpoint)
}

// PatchAgentEndpointContext applies an RFC 7396 merge patch to agent_endpoint.
func (c *Client) PatchAgentEndpointContext(
	ctx context.Context,
	name string,
	endpoint AgentEndpointConfig,
) error {
	return c.PatchAgentDetailsContext(ctx, name, AgentDetailsPatch{
		AgentEndpoint: &endpoint,
	})
}

// PatchVersionSelector applies a selector-only merge patch. A nil selector
// removes version_selector and restores the service default of @latest.
func (c *Client) PatchVersionSelector(name string, selector *VersionSelector) error {
	return c.PatchVersionSelectorContext(context.Background(), name, selector)
}

// PatchVersionSelectorContext applies a selector-only merge patch.
func (c *Client) PatchVersionSelectorContext(
	ctx context.Context,
	name string,
	selector *VersionSelector,
) error {
	if err := validateVersionSelector(selector); err != nil {
		return err
	}
	return c.patchAgentContext(ctx, name, map[string]any{
		"agent_endpoint": map[string]any{
			"version_selector": selector,
		},
	})
}

func validateVersionSelector(selector *VersionSelector) error {
	if selector == nil {
		return nil
	}
	resolution := ResolveVersionSelector(selector, "__latest__")
	if resolution.IsMalformed() {
		return errs.Config("invalid agent version selector: %s", resolution.Problem)
	}
	if resolution.Mode != SelectorPinned && resolution.Mode != SelectorLatest {
		return errs.Config(
			"invalid agent version selector: traffic splitting is not supported; " +
				"exactly one FixedRatio rule at 100 percent is required",
		)
	}
	return nil
}

// PinAgentVersion routes 100 percent of endpoint traffic to one version.
func (c *Client) PinAgentVersion(name, version string) error {
	return c.PinAgentVersionContext(context.Background(), name, version)
}

// PinAgentVersionContext routes 100 percent of endpoint traffic to one version.
func (c *Client) PinAgentVersionContext(ctx context.Context, name, version string) error {
	if version == "" || version == LatestAgentVersion {
		return errs.Config("a concrete agent version is required for pinning")
	}
	selector := &VersionSelector{
		VersionSelectionRules: []FixedRatioVersionSelectionRule{
			NewFixedRatioVersionSelectionRule(version, 100),
		},
	}
	return c.PatchVersionSelectorContext(ctx, name, selector)
}

// RestoreDefaultVersionSelector routes through the service's explicit @latest selector.
func (c *Client) RestoreDefaultVersionSelector(name string) error {
	return c.RestoreDefaultVersionSelectorContext(context.Background(), name)
}

// RestoreDefaultVersionSelectorContext routes through the service's explicit @latest selector.
func (c *Client) RestoreDefaultVersionSelectorContext(ctx context.Context, name string) error {
	selector := &VersionSelector{
		VersionSelectionRules: []FixedRatioVersionSelectionRule{
			NewFixedRatioVersionSelectionRule(LatestAgentVersion, 100),
		},
	}
	return c.PatchVersionSelectorContext(ctx, name, selector)
}

// RestoreLatestAgentVersionContext is an explicit alias for latest routing.
func (c *Client) RestoreLatestAgentVersionContext(ctx context.Context, name string) error {
	return c.RestoreDefaultVersionSelectorContext(ctx, name)
}

// PatchVersionSelectorAndGetContext patches routing and performs a separate GET
// so callers can verify the committed service state.
func (c *Client) PatchVersionSelectorAndGetContext(
	ctx context.Context,
	name string,
	selector *VersionSelector,
) (*Agent, error) {
	if err := c.PatchVersionSelectorContext(ctx, name, selector); err != nil {
		return nil, err
	}
	return c.GetAgentAfterPatchContext(ctx, name)
}

// PatchAgentEndpointAndGetContext patches endpoint configuration and performs a
// separate GET so callers can verify the committed service state.
func (c *Client) PatchAgentEndpointAndGetContext(
	ctx context.Context,
	name string,
	endpoint AgentEndpointConfig,
) (*Agent, error) {
	if err := c.PatchAgentEndpointContext(ctx, name, endpoint); err != nil {
		return nil, err
	}
	return c.GetAgentAfterPatchContext(ctx, name)
}

// PatchAgentDetailsAndGetContext atomically patches endpoint/card details and
// performs a separate GET for committed-state verification.
func (c *Client) PatchAgentDetailsAndGetContext(
	ctx context.Context,
	name string,
	details AgentDetailsPatch,
) (*Agent, error) {
	if err := c.PatchAgentDetailsContext(ctx, name, details); err != nil {
		return nil, err
	}
	return c.GetAgentAfterPatchContext(ctx, name)
}

// GetAgentAfterPatchContext retrieves committed state after a PATCH. It is also
// suitable for reconciling an ambiguous PATCH result.
func (c *Client) GetAgentAfterPatchContext(ctx context.Context, name string) (*Agent, error) {
	agent, err := c.GetAgentContext(ctx, name)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to verify patched agent %q", name)
	}
	if agent == nil {
		return nil, errs.NotFound("patched agent %q was not found during verification", name)
	}
	return agent, nil
}

func (c *Client) patchAgentContext(ctx context.Context, name string, body any) error {
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPatch,
		agentPath(name),
		body,
		requestOptions{
			contentType:     mergePatchContentType,
			suppressPreview: true,
		},
	)
	if err != nil {
		wrapped := errs.FoundryWrap(err, "failed to patch agent %q", name)
		if errs.IsAuthenticationOrAuthorization(err) {
			return wrapped
		}
		return errs.AmbiguousMutation(wrapped)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read patch response for agent %q", name),
		)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	responseErr := httpx.ResponseError("Foundry", fmt.Sprintf("patch agent %q", name), resp, data)
	if httpx.IsTransientStatus(resp.StatusCode) {
		return errs.AmbiguousMutation(responseErr)
	}
	return responseErr
}

// InvokeEndpoint sends a Responses request through the stable logical endpoint.
func (c *Client) InvokeEndpoint(name, prompt string) (*InvocationResult, error) {
	return c.InvokeEndpointContext(context.Background(), name, prompt)
}

// InvokeEndpointContext sends a Responses request through the stable logical
// endpoint instead of the project-level version-reference API.
func (c *Client) InvokeEndpointContext(
	ctx context.Context,
	name string,
	prompt string,
) (*InvocationResult, error) {
	return c.InvokeEndpointWithInputsContext(ctx, name, prompt, nil)
}

// InvokeEndpointWithInputsContext invokes the stable endpoint with optional
// runtime structured inputs.
func (c *Client) InvokeEndpointWithInputsContext(
	ctx context.Context,
	name string,
	prompt string,
	structuredInputs map[string]interface{},
) (*InvocationResult, error) {
	return c.InvokeEndpointWithOptionsContext(ctx, name, prompt, InvocationOptions{
		StructuredInputs: structuredInputs,
	})
}

func (c *Client) InvokeEndpointWithOptionsContext(
	ctx context.Context,
	name string,
	prompt string,
	options InvocationOptions,
) (*InvocationResult, error) {
	body := map[string]any{"input": prompt}
	if len(options.StructuredInputs) > 0 {
		body["structured_inputs"] = options.StructuredInputs
	}
	return c.invokeResponsesPathWithMemoryUser(
		ctx,
		name,
		agentPath(name)+"/endpoint/protocols/openai/responses",
		body,
		true,
		options.MemoryUserID,
	)
}

func (c *Client) ContinueEndpointWithApprovalsContext(
	ctx context.Context,
	name string,
	previousResponseID string,
	decisions []MCPApprovalDecision,
	options InvocationOptions,
) (*InvocationResult, error) {
	body, err := approvalContinuationBody(previousResponseID, decisions)
	if err != nil {
		return nil, err
	}
	return c.invokeResponsesPathWithMemoryUser(
		ctx,
		name,
		agentPath(name)+"/endpoint/protocols/openai/responses",
		body,
		true,
		options.MemoryUserID,
	)
}

// InvokeStableEndpointContext is an alias emphasizing stable endpoint routing.
func (c *Client) InvokeStableEndpointContext(
	ctx context.Context,
	name string,
	prompt string,
) (*InvocationResult, error) {
	return c.InvokeEndpointContext(ctx, name, prompt)
}
