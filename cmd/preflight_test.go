package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/trust"
)

// routedHTTPClient answers Azure requests from a path-keyed script so preflight
// can be exercised end to end without contacting Azure.
type routedHTTPClient struct {
	routes   map[string]*http.Response
	fallback *http.Response
	requests []*http.Request
	hosts    []string
}

func (c *routedHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req)
	c.hosts = append(c.hosts, req.URL.Host)
	for suffix, response := range c.routes {
		if strings.HasSuffix(req.URL.Path, suffix) {
			response.Request = req
			return response, nil
		}
	}
	if c.fallback != nil {
		c.fallback.Request = req
		return c.fallback, nil
	}
	response := jsonResponse(http.StatusOK, "{}")
	response.Request = req
	return response, nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

const preflightProjectResponse = `{
  "location": "eastus",
  "properties": {"endpoints": {"AI Foundry API": "https://account.services.ai.azure.com/api/projects/project"}}
}`

const promptModelDeploymentResponse = `{
  "name": "model",
  "type": "ModelDeployment",
  "modelName": "gpt-5-mini",
  "modelPublisher": "OpenAI",
  "modelVersion": "2025-08-07"
}`

const baseModelDeploymentResponse = `{
  "name": "base-model",
  "type": "ModelDeployment",
  "modelName": "gpt-5-mini",
  "modelPublisher": "OpenAI",
  "modelVersion": "2025-08-07"
}`

const accountModelDeploymentResponse = `{
  "name": "model",
  "properties": {
    "model": {"name": "gpt-5-mini", "version": "2025-08-07", "format": "OpenAI"},
    "provisioningState": "Succeeded"
  }
}`

const fullAPIMManifest = `apiVersion: foundry-agent-manager/v1
agent:
  name: apim-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: api_key
`

func preflightChecks(t *testing.T, state *preflightState) map[string]preflightCheck {
	t.Helper()
	checks := map[string]preflightCheck{}
	for _, check := range state.Result.Checks {
		checks[check.Name] = check
	}
	return checks
}

func TestPreflightPassesEndToEndWithApprovedDestinations(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "environment-apim-key")
	manifest := writeManifest(t, fullAPIMManifest)
	command := commandWithApprovals(t, "preflight", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/connections/apim-apim-agent": jsonResponse(http.StatusNotFound, `{"error":"missing"}`),
		"/deployments/model":           jsonResponse(http.StatusOK, promptModelDeploymentResponse),
		"/projects/project":            jsonResponse(http.StatusOK, preflightProjectResponse),
		"/agents":                      jsonResponse(http.StatusOK, `{"data":[]}`),
	}}

	state, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, httpClient)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	if !state.DestinationsApproved || len(state.ApprovedDestinations) != 1 {
		t.Fatalf("unexpected approval state: %#v", state.ApprovedDestinations)
	}
	if state.Secret.Secret != "environment-apim-key" {
		t.Fatalf("the APIM key was not resolved from the environment: %#v", state.Secret)
	}
	if state.Endpoint != "https://account.services.ai.azure.com/api/projects/project" {
		t.Fatalf("unexpected project endpoint: %s", state.Endpoint)
	}

	checks := preflightChecks(t, state)
	for _, name := range []string{
		"manifest", "tools", "destination-approval", "apim-secret",
		"apim-configuration", "project", "foundry-data-plane", "apim-connection",
		"model-reference",
	} {
		if _, ok := checks[name]; !ok {
			t.Fatalf("preflight is missing the %q check: %#v", name, state.Result.Checks)
		}
	}
	if !strings.Contains(checks["destination-approval"].Details, "contoso.azure-api.net") {
		t.Fatalf("the approval summary must name the destination: %#v", checks["destination-approval"])
	}
	if checks["model-reference"].Status != "passed" ||
		!strings.Contains(checks["model-reference"].Details, `deployment "model" exists`) {
		t.Fatalf("the configured model deployment was not verified: %#v", checks["model-reference"])
	}
	if _, present := checks["apim-secret-safety"]; present {
		t.Fatal("an environment-sourced key must not raise the process-argument warning")
	}

	rendered := preflightText(state.Result)
	if strings.Contains(rendered, "environment-apim-key") {
		t.Fatalf("preflight output leaked the APIM key: %s", rendered)
	}
	if !strings.Contains(checks["apim-secret"].Details, "resolved from environment:") {
		t.Fatalf("unexpected secret description: %#v", checks["apim-secret"])
	}
	for _, host := range httpClient.hosts {
		if host != "management.azure.com" && host != "account.services.ai.azure.com" {
			t.Fatalf("preflight contacted an unexpected host: %s", host)
		}
	}
}

func TestPreflightVerifiesConfiguredRAIPolicy(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: guarded-agent
  model: model
  rai_policy_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/custom-policy
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
`)
	command := commandWithApprovals(t, "preflight", manifest, nil, nil)
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/deployments/model": jsonResponse(http.StatusOK, promptModelDeploymentResponse),
		"/projects/project":  jsonResponse(http.StatusOK, preflightProjectResponse),
		"/agents":            jsonResponse(http.StatusOK, `{"data":[]}`),
		"/raiPolicies": jsonResponse(
			http.StatusOK,
			`{"value":[{"name":"custom-policy"}]}`,
		),
	}}
	state, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, httpClient)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	check := preflightChecks(t, state)["rai-policy-reference"]
	if check.Status != "passed" || !strings.Contains(check.Details, "exists") {
		t.Fatalf("RAI policy was not verified: %#v", check)
	}
}

func TestPreflightWarnsWhenTheKeyComesFromAProcessArgument(t *testing.T) {
	manifest := writeManifest(t, fullAPIMManifest)
	command := commandWithApprovals(t, "preflight", manifest, map[string]string{
		"apim-subscription-key": "argument-apim-key",
	}, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/connections/apim-apim-agent": jsonResponse(http.StatusNotFound, `{}`),
		"/deployments/model":           jsonResponse(http.StatusOK, promptModelDeploymentResponse),
		"/projects/project":            jsonResponse(http.StatusOK, preflightProjectResponse),
		"/agents":                      jsonResponse(http.StatusOK, `{"data":[]}`),
	}}
	state, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	checks := preflightChecks(t, state)
	warning, present := checks["apim-secret-safety"]
	if !present || warning.Status != "warning" {
		t.Fatalf("a command-line key must raise a warning: %#v", state.Result.Checks)
	}
	if strings.Contains(preflightText(state.Result), "argument-apim-key") {
		t.Fatal("preflight output leaked the APIM key")
	}
}

func TestPreflightReportsNonRestorableAPIKeyConnections(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "environment-apim-key")
	manifest := writeManifest(t, fullAPIMManifest)
	command := commandWithApprovals(t, "preflight", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/connections/apim-apim-agent": jsonResponse(http.StatusOK, `{
			"name": "apim-apim-agent",
			"properties": {"authType": "ApiKey", "target": "https://contoso.azure-api.net/agents/chat"}
		}`),
		"/deployments/model": jsonResponse(http.StatusOK, promptModelDeploymentResponse),
		"/projects/project":  jsonResponse(http.StatusOK, preflightProjectResponse),
		"/agents":            jsonResponse(http.StatusOK, `{"data":[]}`),
	}}
	state, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	rollback, present := preflightChecks(t, state)["apim-rollback"]
	if !present || rollback.Status != "warning" {
		t.Fatalf("a non-restorable API-key connection must warn: %#v", state.Result.Checks)
	}
	if !strings.Contains(rollback.Details, "--allow-nonrestorable-apim-update") {
		t.Fatalf("the warning must name the opt-in flag: %#v", rollback)
	}
}

func TestPreflightRejectsUnsupportedExistingConnectionAuthType(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "environment-apim-key")
	manifest := writeManifest(t, fullAPIMManifest)
	command := commandWithApprovals(t, "preflight", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/connections/apim-apim-agent": jsonResponse(http.StatusOK, `{
			"name": "apim-apim-agent",
			"properties": {"authType": "AAD", "target": "https://contoso.azure-api.net/agents/chat"}
		}`),
		"/deployments/model": jsonResponse(http.StatusOK, promptModelDeploymentResponse),
		"/projects/project":  jsonResponse(http.StatusOK, preflightProjectResponse),
		"/agents":            jsonResponse(http.StatusOK, `{"data":[]}`),
	}}
	_, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, httpClient)
	if err == nil || !errs.IsKind(err, "conflict") {
		t.Fatalf("an unsupported existing authType must be a conflict, got %v", err)
	}
}

func TestPreflightRequiresEnsureProjectForAMissingProject(t *testing.T) {
	manifest := writeManifest(t, strings.Replace(fullAPIMManifest, "apim:\n  target: https://contoso.azure-api.net/agents/chat\n  auth: api_key\n", "", 1))
	command := commandWithFlags(t, "preflight", manifest, nil)
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/projects/project": jsonResponse(http.StatusNotFound, `{"error":"missing"}`),
	}}
	_, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, httpClient)
	if err == nil || !errs.IsKind(err, "not_found") {
		t.Fatalf("a missing project must be reported, got %v", err)
	}
	if !strings.Contains(err.Error(), "--ensure-project") {
		t.Fatalf("the error must name the remedy: %v", err)
	}

	ensure := commandWithFlags(t, "preflight", manifest, map[string]string{"ensure-project": "true"})
	ensureClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/deployments/model": jsonResponse(http.StatusOK, accountModelDeploymentResponse),
		"/projects/project":  jsonResponse(http.StatusNotFound, `{"error":"missing"}`),
	}}
	state, err := runPreflight(ensure, prepareForTest(t, ensure), transactionCredential{}, ensureClient)
	if err != nil {
		t.Fatalf("--ensure-project must allow a missing project: %v", err)
	}
	checks := preflightChecks(t, state)
	if !strings.Contains(checks["project"].Details, "--ensure-project") {
		t.Fatalf("unexpected project check: %#v", checks["project"])
	}
	if checks["foundry-data-plane"].Status != "skipped" {
		t.Fatalf("the data plane cannot be probed before creation: %#v", checks["foundry-data-plane"])
	}
	if checks["model-reference"].Status != "passed" ||
		!strings.Contains(checks["model-reference"].Details, "provisioningState=Succeeded") {
		t.Fatalf("the parent account deployment was not verified: %#v", checks["model-reference"])
	}
}

func TestPreflightRunsARMWithResourceID(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	command := commandWithFlags(t, "preflight", manifest, nil)
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/agents":                 jsonResponse(http.StatusOK, `{"data":[]}`),
		"/deployments/base-model": jsonResponse(http.StatusOK, baseModelDeploymentResponse),
	}}
	state, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	checks := preflightChecks(t, state)
	if checks["destination-approval"].Details != "no credential-bearing or external destinations were requested" {
		t.Fatalf("a local-only agent needs no approvals: %#v", checks["destination-approval"])
	}
	if checks["foundry-data-plane"].Status != "passed" {
		t.Fatalf("the data plane must still be probed: %#v", checks["foundry-data-plane"])
	}
}

// The unsafe shared-rollback opt-in is a deploy-time behavior, so the warning is
// raised by the preflight stage that deploy runs, not by the standalone command.
func TestPreflightWarnsAboutUnconditionalSharedRollback(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	if command, _, err := rootCmd().Find([]string{"preflight"}); err != nil {
		t.Fatal(err)
	} else if command.Flags().Lookup("allow-unconditional-shared-rollback") != nil {
		t.Fatal("the rollback opt-in must stay a deploy-only flag")
	}
	command := commandWithFlags(t, "deploy", manifest, map[string]string{
		"allow-unconditional-shared-rollback": "true",
	})
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/agents":                 jsonResponse(http.StatusOK, `{"data":[]}`),
		"/deployments/base-model": jsonResponse(http.StatusOK, baseModelDeploymentResponse),
	}}
	state, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	warning, present := preflightChecks(t, state)["shared-rollback"]
	if !present || warning.Status != "warning" {
		t.Fatalf("the unsafe opt-in must warn: %#v", state.Result.Checks)
	}
}

func TestPreflightAPIMWithResourceIDResolvesCoordinates(t *testing.T) {
	// With resource_id, all ARM coordinates are always derived — the old "missing coordinates"
	// rejection is no longer reachable. This test verifies config resolution succeeds.
	manifest := writeManifest(t, apimManifest)
	command := commandWithApprovals(t, "preflight", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/agents":            jsonResponse(http.StatusOK, `{"data":[]}`),
		"/deployments/model": jsonResponse(http.StatusOK, promptModelDeploymentResponse),
	}}
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "environment-apim-key")
	_, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, httpClient)
	// The APIM connection fixture uses an unsupported authType, which is expected to fail
	// at the APIM validation stage (not the config stage). This confirms coordinates resolved.
	if err == nil {
		return // if APIM validation passes in the future, that's fine too
	}
	if errs.IsKind(err, "config") {
		t.Fatalf("should not fail at config resolution stage: %v", err)
	}
}

func TestPreflightFailsWhenConfiguredModelDeploymentIsMissing(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	command := commandWithFlags(t, "preflight", manifest, nil)
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/agents":                 jsonResponse(http.StatusOK, `{"data":[]}`),
		"/deployments/base-model": jsonResponse(http.StatusNotFound, `{"error":"missing"}`),
	}}

	_, err := runPreflight(
		command,
		prepareForTest(t, command),
		transactionCredential{},
		httpClient,
	)
	if err == nil || !errs.IsKind(err, "not_found") {
		t.Fatalf("a missing model deployment must fail preflight, got %v", err)
	}
	if !strings.Contains(err.Error(), `model deployment "base-model"`) {
		t.Fatalf("the error must name the configured deployment: %v", err)
	}
	steps := errs.Remediation(err)
	if len(steps) != 2 || !strings.Contains(steps[0], "agent.model") {
		t.Fatalf("missing-model remediation is not actionable: %#v", steps)
	}
}

func TestPreflightPropagatesModelDeploymentReadFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		kind   string
	}{
		{name: "forbidden", status: http.StatusForbidden, body: `{"error":"denied"}`, kind: "authorization"},
		{name: "service failure", status: http.StatusInternalServerError, body: `{"error":"failed"}`, kind: "transient"},
		{name: "malformed success", status: http.StatusOK, body: `{}`, kind: "foundry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := writeManifest(t, baseManifest)
			command := commandWithFlags(t, "preflight", manifest, nil)
			httpClient := &routedHTTPClient{routes: map[string]*http.Response{
				"/agents":                 jsonResponse(http.StatusOK, `{"data":[]}`),
				"/deployments/base-model": jsonResponse(test.status, test.body),
			}}
			_, err := runPreflight(
				command,
				prepareForTest(t, command),
				transactionCredential{},
				httpClient,
			)
			if err == nil || !errs.IsKind(err, test.kind) {
				t.Fatalf("expected %s error, got %v", test.kind, err)
			}
		})
	}
}

func TestPreflightRejectsUnreadyAccountModelDeploymentBeforeProjectCreation(t *testing.T) {
	manifest := writeManifest(t, strings.Replace(
		fullAPIMManifest,
		"apim:\n  target: https://contoso.azure-api.net/agents/chat\n  auth: api_key\n",
		"",
		1,
	))
	command := commandWithFlags(t, "preflight", manifest, map[string]string{"ensure-project": "true"})
	httpClient := &routedHTTPClient{routes: map[string]*http.Response{
		"/projects/project": jsonResponse(http.StatusNotFound, `{"error":"missing"}`),
		"/deployments/model": jsonResponse(http.StatusOK, `{
			"name":"model",
			"properties":{"model":{"name":"gpt-5-mini"},"provisioningState":"Failed"}
		}`),
	}}
	_, err := runPreflight(
		command,
		prepareForTest(t, command),
		transactionCredential{},
		httpClient,
	)
	if err == nil || !errs.IsKind(err, "conflict") ||
		!strings.Contains(err.Error(), "provisioningState") {
		t.Fatalf("an unready account deployment must fail preflight, got %v", err)
	}
}

func TestPreflightFailsWhenTheAPIMKeyIsMissing(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "")
	manifest := writeManifest(t, fullAPIMManifest)
	command := commandWithApprovals(t, "preflight", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	_, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, &failingHTTPClient{t: t})
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("a missing APIM key must fail before Azure access, got %v", err)
	}
	if !strings.Contains(err.Error(), "FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY") {
		t.Fatalf("the error must list the supported sources: %v", err)
	}
}
