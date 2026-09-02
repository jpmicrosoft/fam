package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/trust"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
)

// stubCredentialAndHTTP installs a fake credential and a scripted HTTP client
// for the duration of a test, restoring the real factories on cleanup.
func stubCredentialAndHTTP(t *testing.T, http *scriptedHTTP) {
	t.Helper()
	origCred, origHTTP := newCredentialFn, newHTTPClientFn
	t.Cleanup(func() { newCredentialFn, newHTTPClientFn = origCred, origHTTP })
	newCredentialFn = func(_ *cobra.Command, _ azcloud.Profile) (azcore.TokenCredential, error) {
		return transactionCredential{}, nil
	}
	newHTTPClientFn = func(_ *cobra.Command) *httpx.RetryClient {
		return httpx.NewRetryClient(http, httpx.Options{Retries: 0})
	}
}

// scriptedHTTP returns pre-configured responses keyed by URL path suffix.
// It creates fresh response bodies for each request so the same route can be hit multiple times.
type scriptedHTTP struct {
	routes     map[string]scriptedRoute
	fallback   *scriptedRoute
	requests   []*http.Request
	routeCalls map[string]int
}

type scriptedRoute struct {
	status   int
	body     string
	err      error
	sequence []scriptedRoute
}

func (s *scriptedHTTP) Do(req *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, req)
	for suffix, route := range s.routes {
		if strings.HasSuffix(req.URL.Path, suffix) {
			if len(route.sequence) > 0 {
				if s.routeCalls == nil {
					s.routeCalls = map[string]int{}
				}
				index := s.routeCalls[suffix]
				if index >= len(route.sequence) {
					index = len(route.sequence) - 1
				}
				s.routeCalls[suffix]++
				route = route.sequence[index]
			}
			if route.err != nil {
				return nil, route.err
			}
			return &http.Response{
				StatusCode: route.status,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(route.body)),
				Request:    req,
			}, nil
		}
	}
	if s.fallback != nil {
		return &http.Response{
			StatusCode: s.fallback.status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(s.fallback.body)),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    req,
	}, nil
}

func route(status int, body string) scriptedRoute {
	return scriptedRoute{status: status, body: body}
}

func routeError(err error) scriptedRoute {
	return scriptedRoute{err: err}
}

func routeSequence(routes ...scriptedRoute) scriptedRoute {
	return scriptedRoute{sequence: routes}
}

func modelDeploymentRoute(name string) scriptedRoute {
	return route(http.StatusOK, fmt.Sprintf(
		`{"name":%q,"type":"ModelDeployment","modelName":"gpt-5-mini","modelPublisher":"OpenAI","modelVersion":"2025-08-07"}`,
		name,
	))
}

// ---------- status ----------

func TestCmdStatusHappyPath(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id": "agent-1", "name": "base-agent", "state": "active",
			"versions": {"latest": {"id":"v1","name":"base-agent","version":"3","status":"ready"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "status", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("status failed: %s", run.stderr)
	}
	var result statusResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, run.stdout)
	}
	if !result.Agent.Exists || result.Agent.LatestVersion != "3" || result.Agent.State != "active" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCmdStatusAgentNotFound(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusNotFound, `{"error":"not found"}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "status", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("status should succeed even when agent missing: %s", run.stderr)
	}
	var result statusResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, run.stdout)
	}
	if result.Agent.Exists {
		t.Fatalf("agent should not exist: %#v", result)
	}
}

func TestCmdStatusWithAPIM(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: apim-test
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: managed_identity
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/apim-test": route(http.StatusOK, `{
			"id": "agent-1", "name": "apim-test", "state": "active",
			"versions": {"latest": {"id":"v1","name":"apim-test","version":"1","status":"ready"}}
		}`),
		"/connections/apim-apim-test": route(http.StatusOK, `{
			"name": "apim-apim-test",
			"properties": {"target": "https://contoso.azure-api.net/agents/chat", "authType": "ProjectManagedIdentity"}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "status", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("status failed: %s", run.stderr)
	}
	var result statusResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.APIM == nil || !result.APIM.Exists || result.APIM.Target != "https://contoso.azure-api.net/agents/chat" {
		t.Fatalf("unexpected APIM status: %#v", result.APIM)
	}
}

func TestCmdStatusNoAPIMSkipsConnection(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: apim-test
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: managed_identity
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/apim-test": route(http.StatusOK, `{
			"id": "agent-1", "name": "apim-test", "state": "active",
			"versions": {"latest": {"id":"v1","name":"apim-test","version":"1","status":"ready"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "status", "-f", manifest, "--output", "json", "--no-apim")
	if run.code != 0 {
		t.Fatalf("status failed: %s", run.stderr)
	}
	var result statusResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.APIM != nil {
		t.Fatal("--no-apim should suppress APIM check")
	}
}

// ---------- show ----------

func TestCmdShowHappyPath(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id": "agent-1", "name": "base-agent", "state": "active",
			"versions": {"latest": {"id":"v1","name":"base-agent","version":"2","status":"ready"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "show", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("show failed: %s", run.stderr)
	}
	var result showResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Agent == nil {
		t.Fatal("agent should be populated")
	}
}

func TestCmdShowNotFound(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusNotFound, `{"error":"not found"}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "show", "-f", manifest, "--output", "json")
	if run.code == 0 {
		t.Fatal("show should fail for missing agent")
	}
	envelope := decodeErrorEnvelope(t, run)
	if envelope.Kind != "not_found" {
		t.Fatalf("unexpected error kind: %s", envelope.Kind)
	}
}

func TestCmdShowSpecificVersion(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions/5": route(http.StatusOK, `{
			"id": "v5", "name": "base-agent", "version": "5", "status": "ready",
			"definition": {}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "show", "-f", manifest, "--output", "json", "--agent-version", "5")
	if run.code != 0 {
		t.Fatalf("show --agent-version failed: %s", run.stderr)
	}
}

func TestCmdShowSpecificVersionNotFound(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions/99": route(http.StatusNotFound, `{"error":"missing"}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "show", "-f", manifest, "--output", "json", "--agent-version", "99")
	if run.code == 0 {
		t.Fatal("show should fail for missing version")
	}
	envelope := decodeErrorEnvelope(t, run)
	if envelope.Kind != "not_found" {
		t.Fatalf("unexpected error kind: %s", envelope.Kind)
	}
}

// ---------- versions ----------

func TestCmdVersionsHappyPath(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions": route(http.StatusOK, `{"data":[
			{"id":"v1","name":"base-agent","version":"1","status":"ready","created_at":100},
			{"id":"v2","name":"base-agent","version":"2","status":"ready","created_at":200}
		]}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "versions", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("versions failed: %s", run.stderr)
	}
	var result versionsResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(result.Versions))
	}
}

func TestCmdVersionsEmpty(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions": route(http.StatusOK, `{"data":[]}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "versions", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("versions failed: %s", run.stderr)
	}
	var result versionsResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Versions) != 0 {
		t.Fatalf("expected 0 versions, got %d", len(result.Versions))
	}
}

// ---------- smoke ----------

func TestCmdSmokeHappyPath(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/responses": route(http.StatusOK, `{"id":"resp-1","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "smoke", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("smoke failed: %s", run.stderr)
	}
	var result smokeResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "resp-1" || result.OutputText != "hello" {
		t.Fatalf("unexpected smoke result: %#v", result)
	}
}

// ---------- diff ----------

func TestCmdDiffNoChange(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id": "agent-1", "name": "base-agent", "state": "active",
			"versions": {"latest": {"id":"v1","name":"base-agent","version":"2","status":"ready",
				"definition": {"kind":"prompt","model":"base-model","instructions":"base instructions","tools":[{"type":"code_interpreter"}]}
			}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "diff", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("diff failed: %s", run.stderr)
	}
	var result fullDiffResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatalf("expected no change: %#v", result)
	}
}

func TestCmdDiffDetectsChange(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id": "agent-1", "name": "base-agent", "state": "active",
			"versions": {"latest": {"id":"v1","name":"base-agent","version":"1","status":"ready",
				"definition": {"model":"old-model","instructions":"old instructions","tools":[]}
			}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "diff", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("diff failed: %s", run.stderr)
	}
	var result fullDiffResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("expected change to be detected")
	}
}

func TestCmdDiffWithAPIM(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: apim-test
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: managed_identity
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/apim-test": route(http.StatusOK, `{
			"id": "agent-1", "name": "apim-test", "state": "active",
			"versions": {"latest": {"id":"v1","name":"apim-test","version":"1","status":"ready",
				"definition": {"model":"apim-apim-test/model","instructions":"help","tools":[]}
			}}
		}`),
		"/connections/apim-apim-test": route(http.StatusNotFound, `{"error":"missing"}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "diff", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("diff failed: %s", run.stderr)
	}
	var result fullDiffResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.APIM == nil || !result.APIM.Changed {
		t.Fatal("missing APIM connection should register as a change")
	}
}

// ---------- disable / enable ----------

func TestCmdDisableHappyPath(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/disable": route(http.StatusNoContent, ""),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "disable", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("disable failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "disable" || !result.Changed {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCmdEnableHappyPath(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/enable": route(http.StatusNoContent, ""),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "enable", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("enable failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "enable" || !result.Changed {
		t.Fatalf("unexpected result: %#v", result)
	}
}

// ---------- prune ----------

func TestCmdPruneKeepBoundary(t *testing.T) {
	manifest := writeManifest(t, baseManifest)

	// --keep=0 is rejected locally
	http := &scriptedHTTP{}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "prune", "-f", manifest, "--output", "json", "--yes", "--keep", "0")
	if run.code == 0 {
		t.Fatal("--keep=0 should be rejected")
	}
	envelope := decodeErrorEnvelope(t, run)
	if envelope.Kind != "config" {
		t.Fatalf("unexpected error kind: %s", envelope.Kind)
	}
}

func TestCmdPruneDryRun(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions": route(http.StatusOK, `{"data":[
			{"id":"v3","name":"base-agent","version":"3","status":"ready","created_at":300},
			{"id":"v2","name":"base-agent","version":"2","status":"ready","created_at":200},
			{"id":"v1","name":"base-agent","version":"1","status":"ready","created_at":100}
		]}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "prune", "-f", manifest, "--output", "json", "--dry-run", "--keep", "1")
	if run.code != 0 {
		t.Fatalf("prune dry-run failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || len(result.Versions) != 2 {
		t.Fatalf("unexpected prune dry-run result: %#v", result)
	}
}

func TestCmdPruneNothingToPrune(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions": route(http.StatusOK, `{"data":[
			{"id":"v1","name":"base-agent","version":"1","status":"ready","created_at":100}
		]}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "prune", "-f", manifest, "--output", "json", "--yes", "--keep", "1")
	if run.code != 0 {
		t.Fatalf("prune failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("nothing to prune should report unchanged")
	}
}

func TestCmdPruneExecutes(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions":   route(http.StatusOK, `{"data":[{"id":"v2","name":"base-agent","version":"2","status":"ready","created_at":200},{"id":"v1","name":"base-agent","version":"1","status":"ready","created_at":100}]}`),
		"/agents/base-agent/versions/1": route(http.StatusNoContent, ""),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "prune", "-f", manifest, "--output", "json", "--yes", "--keep", "1")
	if run.code != 0 {
		t.Fatalf("prune failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(result.Versions) != 1 || result.Versions[0] != "1" {
		t.Fatalf("unexpected prune result: %#v", result)
	}
}

// ---------- delete-version ----------

func TestCmdDeleteVersionDryRun(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions/5": route(http.StatusOK, `{"id":"v5","name":"base-agent","version":"5","status":"ready","definition":{}}`),
		"/agents/base-agent/versions": route(http.StatusOK, `{"data":[
			{"id":"v6","name":"base-agent","version":"6","created_at":600},
			{"id":"v5","name":"base-agent","version":"5","created_at":500}
		]}`),
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1","name":"base-agent","versions":{"latest":{"version":"6"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "delete-version", "-f", manifest, "--output", "json", "--dry-run", "--agent-version", "5")
	if run.code != 0 {
		t.Fatalf("delete-version dry-run failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || !result.Changed {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCmdDeleteVersionNotFound(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions/99": route(http.StatusNotFound, `{"error":"missing"}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "delete-version", "-f", manifest, "--output", "json", "--yes", "--agent-version", "99")
	if run.code != 0 {
		t.Fatalf("delete-version should succeed when version missing: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("missing version should report unchanged")
	}
}

func TestCmdDeleteVersionExecutes(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions/5": route(http.StatusOK, `{"id":"v5","name":"base-agent","version":"5","status":"ready","definition":{}}`),
		"/agents/base-agent/versions": route(http.StatusOK, `{"data":[
			{"id":"v6","name":"base-agent","version":"6","created_at":600},
			{"id":"v5","name":"base-agent","version":"5","created_at":500}
		]}`),
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1","name":"base-agent","versions":{"latest":{"version":"6"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	// Use --yes to confirm
	run := runCLI(t, "", "delete-version", "-f", manifest, "--output", "json", "--yes", "--agent-version", "5")
	if run.code != 0 {
		t.Fatalf("delete-version failed: %s", run.stderr)
	}
}

// ---------- delete ----------

func TestCmdDeleteDryRun(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1","name":"base-agent","state":"active",
			"versions":{"latest":{"id":"v1","name":"base-agent","version":"1","status":"ready"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "delete", "-f", manifest, "--output", "json", "--dry-run")
	if run.code != 0 {
		t.Fatalf("delete dry-run failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || !result.Changed {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCmdDeleteNotFound(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusNotFound, `{"error":"not found"}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "delete", "-f", manifest, "--output", "json", "--yes")
	if run.code != 0 {
		t.Fatalf("delete should succeed for missing agent: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("missing agent should report not changed")
	}
}

func TestCmdDeleteExecutesWithConfirmation(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1","name":"base-agent","state":"active",
			"versions":{"latest":{"id":"v1","name":"base-agent","version":"1","status":"ready"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "delete", "-f", manifest, "--output", "json", "--yes")
	if run.code != 0 {
		t.Fatalf("delete failed: %s", run.stderr)
	}
}

func TestCmdDeleteRequiresConfirmation(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1","name":"base-agent","state":"active",
			"versions":{"latest":{"id":"v1","name":"base-agent","version":"1","status":"ready"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	// Without --yes, with structured output, must require --yes
	run := runCLI(t, "", "delete", "-f", manifest, "--output", "json")
	if run.code == 0 {
		t.Fatal("delete should require --yes in structured output mode")
	}
	envelope := decodeErrorEnvelope(t, run)
	if envelope.Kind != "config" {
		t.Fatalf("unexpected error kind: %s", envelope.Kind)
	}
}

// ---------- decommission ----------

func TestCmdDecommissionDryRun(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: doomed
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: managed_identity
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/doomed": route(http.StatusOK, `{
			"id":"agent-1","name":"doomed","state":"active",
			"versions":{"latest":{"id":"v1","name":"doomed","version":"1","status":"ready"}}
		}`),
		"/connections/apim-doomed": route(http.StatusOK, `{
			"name":"apim-doomed",
			"properties":{"target":"https://contoso.azure-api.net/agents/chat","authType":"ProjectManagedIdentity"}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "decommission", "-f", manifest, "--output", "json", "--dry-run")
	if run.code != 0 {
		t.Fatalf("decommission dry-run failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || !result.Changed || result.APIM == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCmdDecommissionNoAPIM(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: doomed
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: managed_identity
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/doomed": route(http.StatusNotFound, `{"error":"not found"}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "decommission", "-f", manifest, "--output", "json", "--yes", "--no-apim")
	if run.code != 0 {
		t.Fatalf("decommission failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.APIM != "" {
		t.Fatal("--no-apim should suppress APIM teardown")
	}
}

func TestCmdDecommissionNothingToDelete(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: doomed
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: managed_identity
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/doomed":           route(http.StatusNotFound, `{"error":"not found"}`),
		"/connections/apim-doomed": route(http.StatusNotFound, `{"error":"missing"}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "decommission", "-f", manifest, "--output", "json", "--yes")
	if run.code != 0 {
		t.Fatalf("decommission failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("nothing to decommission should not report changed")
	}
}

func TestCmdDecommissionExecutes(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: doomed
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: managed_identity
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/doomed": route(http.StatusOK, `{
			"id":"agent-1","name":"doomed","state":"active",
			"versions":{"latest":{"id":"v1","name":"doomed","version":"1","status":"ready"}}
		}`),
		"/connections/apim-doomed": route(http.StatusOK, `{
			"name":"apim-doomed",
			"properties":{"target":"https://contoso.azure-api.net/agents/chat","authType":"ProjectManagedIdentity"}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "decommission", "-f", manifest, "--output", "json", "--yes")
	if run.code != 0 {
		t.Fatalf("decommission failed: %s", run.stderr)
	}
	var result lifecycleResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("decommission should report changed")
	}
}

// ---------- deploy: if-changed skip ----------

func TestCmdDeployIfChangedSkips(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "")
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents":                 route(http.StatusOK, `{"data":[]}`),
		"/deployments/base-model": modelDeploymentRoute("base-model"),
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1","name":"base-agent","state":"active",
			"versions":{"latest":{"id":"v1","name":"base-agent","version":"2","status":"ready",
				"definition":{"kind":"prompt","model":"base-model","instructions":"base instructions","tools":[{"type":"code_interpreter"}]}
			}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "deploy", "-f", manifest, "--output", "json", "--if-changed")
	if run.code != 0 {
		t.Fatalf("deploy --if-changed failed: %s", run.stderr)
	}
	var result deployResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.Status != "unchanged" {
		t.Fatalf("expected unchanged, got: %#v", result)
	}
}

// ---------- deploy: success path ----------

func TestCmdDeploySuccess(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "")
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents":                     route(http.StatusOK, `{"data":[]}`),
		"/deployments/base-model":     modelDeploymentRoute("base-model"),
		"/agents/base-agent/versions": route(http.StatusOK, `{"id":"agent-1","name":"base-agent","version":"3","status":"ready"}`),
		"/agents/base-agent": routeSequence(
			route(http.StatusNotFound, `{"error":"not found"}`),
			route(http.StatusOK, `{"id":"agent-1","name":"base-agent","versions":{"latest":{"version":"3"}}}`),
			route(http.StatusNoContent, ``),
			route(http.StatusOK, `{
				"id":"agent-1","name":"base-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[
					{"type":"FixedRatio","agent_version":"3","traffic_percentage":100}
				]}},
				"versions":{"latest":{"version":"3"}}
			}`),
		),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "deploy", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("deploy failed: %s", run.stderr)
	}
	var result deployResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Status != "succeeded" {
		t.Fatalf("expected succeeded, got: %#v", result)
	}
	if result.Agent == nil || result.Agent.Version != "3" {
		t.Fatalf("expected agent result with version 3: %#v", result)
	}
	if result.Receipt == "" {
		t.Fatal("receipt path must be populated")
	}
}

func TestCmdSmokeContinuesExplicitMCPApproval(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/responses": routeSequence(
			route(http.StatusOK, `{
				"id":"resp-1",
				"output":[{
					"type":"mcp_approval_request",
					"id":"approval-1",
					"server_label":"github",
					"name":"create_issue",
					"arguments":"{\"title\":\"Test\"}"
				}]
			}`),
			route(http.StatusOK, `{"id":"resp-2","output_text":"created"}`),
		),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(
		t,
		"",
		"smoke",
		"-f",
		manifest,
		"--output",
		"json",
		"--approve-mcp-tool",
		"github/create_issue",
	)
	if run.code != 0 {
		t.Fatalf("smoke approval continuation failed: %s", run.stderr)
	}
	var result smokeResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "resp-2" || result.OutputText != "created" {
		t.Fatalf("unexpected smoke result: %#v", result)
	}
	if len(http.requests) != 2 {
		t.Fatalf("expected initial and continuation requests, got %d", len(http.requests))
	}
	body, err := io.ReadAll(http.requests[1].Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"previous_response_id":"resp-1"`) ||
		!strings.Contains(string(body), `"approval_request_id":"approval-1"`) {
		t.Fatalf("unexpected continuation body: %s", body)
	}
}

func TestCmdSmokeStopsForUnapprovedMCPRequest(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/responses": route(http.StatusOK, `{
			"id":"resp-1",
			"output":[{
				"type":"mcp_approval_request",
				"id":"approval-1",
				"server_label":"github",
				"name":"delete_repository",
				"arguments":"{\"repository\":\"important\"}"
			}]
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "smoke", "-f", manifest)
	if run.code == 0 || !strings.Contains(run.stderr, "--approve-mcp-tool") {
		t.Fatalf("unapproved MCP request did not fail closed: code=%d stderr=%s", run.code, run.stderr)
	}
}

// ---------- deploy: smoke-test-after-deploy ----------

func TestCmdDeploySmokeTest(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "")
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents":                     route(http.StatusOK, `{"data":[]}`),
		"/deployments/base-model":     modelDeploymentRoute("base-model"),
		"/agents/base-agent/versions": route(http.StatusOK, `{"id":"agent-1","name":"base-agent","version":"3","status":"ready"}`),
		"/agents/base-agent": routeSequence(
			route(http.StatusNotFound, `{"error":"not found"}`),
			route(http.StatusOK, `{"id":"agent-1","name":"base-agent","versions":{"latest":{"version":"3"}}}`),
			route(http.StatusNoContent, ``),
			route(http.StatusOK, `{
				"id":"agent-1","name":"base-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[
					{"type":"FixedRatio","agent_version":"3","traffic_percentage":100}
				]}},
				"versions":{"latest":{"version":"3"}}
			}`),
		),
		"/responses": route(http.StatusOK, `{"id":"resp-1","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "deploy", "-f", manifest, "--output", "json", "--smoke-test")
	if run.code != 0 {
		t.Fatalf("deploy with smoke-test failed: %s", run.stderr)
	}
	var result deployResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Smoke == nil || result.Smoke.ID != "resp-1" {
		t.Fatalf("expected smoke result: %#v", result)
	}
}

// ---------- deploy: APIM connection update ----------

func TestCmdDeployAPIMConnectionUpdate(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: apim-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: managed_identity
`)
	// Connection exists but target differs → triggers update
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents":                     route(http.StatusOK, `{"data":[]}`),
		"/deployments/model":          modelDeploymentRoute("model"),
		"/agents/apim-agent/versions": route(http.StatusOK, `{"id":"agent-1","name":"apim-agent","version":"2","status":"ready"}`),
		"/agents/apim-agent": routeSequence(
			route(http.StatusNotFound, `{"error":"not found"}`),
			route(http.StatusOK, `{"id":"agent-1","name":"apim-agent","versions":{"latest":{"version":"2"}}}`),
			route(http.StatusNoContent, ``),
			route(http.StatusOK, `{
				"id":"agent-1","name":"apim-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[
					{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}
				]}},
				"versions":{"latest":{"version":"2"}}
			}`),
		),
		"/connections/apim-apim-agent": route(http.StatusOK, `{"name":"apim-apim-agent","properties":{"target":"https://contoso.azure-api.net/OLD","authType":"ProjectManagedIdentity","category":"ApiManagement","isSharedToAll":false,"credentials":{}}}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "deploy", "-f", manifest, "--output", "json",
		"--trusted-apim-host", "contoso.azure-api.net")
	if run.code != 0 {
		t.Fatalf("deploy failed: %s", run.stderr)
	}
	var result deployResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.APIMAction != "updated" {
		t.Fatalf("expected APIM action 'updated', got %q", result.APIMAction)
	}
}

func TestCmdDeployRejectsActiveAPIMConnectionUpdateWithoutExplicitOptIn(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: apim-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: managed_identity
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents":            route(http.StatusOK, `{"data":[]}`),
		"/deployments/model": modelDeploymentRoute("model"),
		"/agents/apim-agent": route(http.StatusOK, `{
			"id":"agent-1","name":"apim-agent",
			"agent_endpoint":{"version_selector":{"version_selection_rules":[
				{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}
			]}},
			"versions":{"latest":{"version":"2","definition":{"kind":"prompt","model":"old-model","instructions":"old"}}}
		}`),
		"/connections/apim-apim-agent": route(http.StatusOK, `{
			"name":"apim-apim-agent",
			"properties":{"target":"https://contoso.azure-api.net/old","authType":"ProjectManagedIdentity","category":"ApiManagement","isSharedToAll":false,"credentials":{}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "deploy", "-f", manifest, "--output", "json", "--trusted-apim-host", "contoso.azure-api.net")
	if run.code == 0 {
		t.Fatal("shared active APIM connection update must require explicit opt-in")
	}
	if !strings.Contains(run.stderr, "allow-active-apim-update") {
		t.Fatalf("rejection must name the explicit opt-in: %s", run.stderr)
	}
	for _, request := range http.requests {
		if request.Method == "PATCH" || request.Method == "POST" || request.Method == "PUT" {
			t.Fatalf("APIM safety rejection must happen before mutation: %s %s", request.Method, request.URL)
		}
	}
}

func TestCmdDeployStagesCandidateBehindPinnedActiveVersion(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "")
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents":                 route(http.StatusOK, `{"data":[]}`),
		"/deployments/base-model": modelDeploymentRoute("base-model"),
		"/agents/base-agent/versions": route(
			http.StatusOK,
			`{"id":"agent-1","name":"base-agent","version":"3","status":"ready"}`,
		),
		"/agents/base-agent": routeSequence(
			route(http.StatusOK, `{
				"id":"agent-1","name":"base-agent",
				"versions":{"latest":{"version":"2","definition":{
					"kind":"prompt","model":"old-model","instructions":"old instructions"
				}}}
			}`),
			route(http.StatusNoContent, ``),
			route(http.StatusOK, `{
				"id":"agent-1","name":"base-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[
					{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}
				]}},
				"versions":{"latest":{"version":"2"}}
			}`),
			route(http.StatusOK, `{
				"id":"agent-1","name":"base-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[
					{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}
				]}},
				"versions":{"latest":{"version":"3"}}
			}`),
		),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "deploy", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("staged deploy failed: %s", run.stderr)
	}
	var result deployResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "staged" || !result.Staged ||
		result.ActiveVersion != "2" || result.LatestVersion != "3" ||
		result.Agent == nil || result.Agent.Version != "3" {
		t.Fatalf("candidate was not safely staged: %#v", result)
	}
	var patchIndex, createIndex = -1, -1
	requestSummary := make([]string, 0, len(http.requests))
	for index, request := range http.requests {
		requestSummary = append(requestSummary, request.Method+" "+request.URL.String())
		if request.Method == "PATCH" && strings.HasSuffix(request.URL.Path, "/agents/base-agent") {
			patchIndex = index
		}
		if request.Method == "POST" && strings.HasSuffix(request.URL.Path, "/agents/base-agent/versions") {
			createIndex = index
		}
	}
	if patchIndex < 0 || createIndex < 0 || patchIndex >= createIndex {
		t.Fatalf("current latest was not pinned before candidate creation: %#v", requestSummary)
	}
}

func TestCmdPromotePinsVerifiedVersion(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions/3": route(
			http.StatusOK,
			`{"id":"v3","name":"base-agent","version":"3","definition":{}}`,
		),
		"/agents/base-agent": routeSequence(
			route(http.StatusOK, `{
				"id":"agent-1","name":"base-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[
					{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}
				]}},
				"versions":{"latest":{"version":"3"}}
			}`),
			route(http.StatusNoContent, ``),
			route(http.StatusOK, `{
				"id":"agent-1","name":"base-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[
					{"type":"FixedRatio","agent_version":"3","traffic_percentage":100}
				]}},
				"versions":{"latest":{"version":"3"}}
			}`),
		),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(
		t,
		"",
		"promote",
		"-f",
		manifest,
		"--agent-version",
		"3",
		"--output",
		"json",
	)
	if run.code != 0 {
		t.Fatalf("promote failed: %s", run.stderr)
	}
	var result releaseResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.ActiveVersion != "3" || result.SelectorMode != "pinned" {
		t.Fatalf("unexpected promotion result: %#v", result)
	}
}

func TestCmdDeleteVersionRejectsActiveVersion(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions/5": route(
			http.StatusOK,
			`{"id":"v5","name":"base-agent","version":"5","definition":{}}`,
		),
		"/agents/base-agent/versions": route(
			http.StatusOK,
			`{"data":[{"id":"v5","name":"base-agent","version":"5","created_at":500}]}`,
		),
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1","name":"base-agent","versions":{"latest":{"version":"5"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(
		t,
		"",
		"delete-version",
		"-f",
		manifest,
		"--agent-version",
		"5",
		"--yes",
		"--output",
		"json",
	)
	if run.code != 7 || !strings.Contains(run.stderr, `"kind": "conflict"`) {
		t.Fatalf("expected conflict, code=%d stderr=%s", run.code, run.stderr)
	}
}

func TestCmdPublishM365EndToEnd(t *testing.T) {
	const subscription = "11111111-2222-3333-4444-555555555555"
	const tenant = "99999999-8888-7777-6666-555555555555"
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: m365-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/`+subscription+`/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
`)
	publicationPath := filepath.Join(t.TempDir(), "publication.yaml")
	if err := os.WriteFile(publicationPath, []byte(`apiVersion: foundry-agent-manager/publication/v1
microsoft365:
  bot_service:
    name: support-bot
    tenant_id: `+tenant+`
  publish_scope: Tenant
  app_version: 1.0.0
  agent_display_name: Support Agent
  short_description: Support agent
  full_description: Handles support requests.
  developer_name: Contoso
`), 0o600); err != nil {
		t.Fatal(err)
	}
	botID := "/subscriptions/" + subscription +
		"/resourceGroups/agents-rg/providers/Microsoft.BotService/botServices/support-bot"
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/m365-agent": route(http.StatusOK, `{
			"id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"name":"m365-agent",
			"instance_identity":{
				"principal_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
				"client_id":"bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
			},
			"agent_endpoint":{"version_selector":{"version_selection_rules":[
				{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}
			]}},
			"versions":{"latest":{"version":"2"}}
		}`),
		"/providers/Microsoft.BotService": route(
			http.StatusOK,
			`{"namespace":"Microsoft.BotService","registrationState":"Registered"}`,
		),
		"/botServices/support-bot": routeSequence(
			route(http.StatusNotFound, `{}`),
			route(http.StatusCreated, `{}`),
			route(http.StatusOK, `{
				"id":"`+botID+`","name":"support-bot","location":"global","kind":"azurebot",
				"sku":{"name":"F0"},
				"properties":{
					"displayName":"Support Agent",
					"endpoint":"https://account.services.ai.azure.com/api/projects/project/agents/m365-agent/endpoint/protocols/activityProtocol?api-version=2025-05-15-preview",
					"msaAppId":"bbbbbbbb-cccc-dddd-eeee-ffffffffffff",
					"msaAppTenantId":"`+tenant+`",
					"msaAppType":"SingleTenant",
					"publicNetworkAccess":"Disabled"
				}
			}`),
		),
		"/botServices/support-bot/channels/MsTeamsChannel": routeSequence(
			route(http.StatusNotFound, `{}`),
			route(http.StatusCreated, `{}`),
			route(http.StatusOK, `{
				"id":"`+botID+`/channels/MsTeamsChannel",
				"name":"MsTeamsChannel","location":"global",
				"properties":{"channelName":"MsTeamsChannel"}
			}`),
		),
		"/agents/m365-agent/microsoft365/publish": route(
			http.StatusOK,
			`{"titleId":"title-123"}`,
		),
	}}
	stubCredentialAndHTTP(t, http)
	receiptPath := filepath.Join(t.TempDir(), "m365-publish.json")
	run := runCLI(
		t,
		"",
		"publish-m365",
		"-f",
		manifest,
		"--publication",
		publicationPath,
		"--receipt",
		receiptPath,
		"--output",
		"json",
	)
	if run.code != 0 {
		t.Fatalf("publish-m365 failed: %s", run.stderr)
	}
	var result publishM365Result
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.TitleID != "title-123" ||
		result.Status != "succeeded-pending-external-actions" ||
		!result.AdminApprovalRequired ||
		result.BotServiceAction != "created" || result.TeamsChannelAction != "created" {
		t.Fatalf("unexpected M365 result: %#v", result)
	}
	receiptData, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(receiptData), `"status": "succeeded-pending-external-actions"`) ||
		!strings.Contains(string(receiptData), `"status": "pending-admin-approval"`) {
		t.Fatalf("receipt did not preserve the external approval state: %s", receiptData)
	}
}

func TestCmdPublishM365RequiresIdentityClientIDBeforeBotMutation(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: m365-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/m365-agent": route(http.StatusOK, `{
			"name":"m365-agent",
			"instance_identity":{"principal_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
			"agent_endpoint":{"version_selector":{"version_selection_rules":[
				{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}
			]}},
			"versions":{"latest":{"version":"2"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(
		t,
		"",
		"publish-m365",
		"-f",
		manifest,
		"--publication",
		filepath.Join("..", "examples", "publication.example.yaml"),
		"--output",
		"json",
	)
	if run.code != 7 || !strings.Contains(run.stderr, "instance_identity.client_id") {
		t.Fatalf("missing client ID must fail before Bot Service mutation: code=%d stderr=%s", run.code, run.stderr)
	}
	for _, request := range http.requests {
		if strings.Contains(request.URL.Path, "/providers/Microsoft.BotService") ||
			strings.Contains(request.URL.Path, "/botServices/") {
			t.Fatalf("missing client ID reached Bot Service: %s", request.URL)
		}
	}
}

// ---------- endpoint configuration and release safety ----------

func TestCmdEndpointConfigurePreservesPinnedRoutingAndVerifiesCard(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: endpoint-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
endpoint:
  protocols: [activity]
  authorization_schemes:
    - type: BotServiceRbac
  agent_card:
    version: 1.0.0
    description: Support agent
    skills:
      - id: support
        name: Support
        description: Handles support requests
        tags: [help]
`)
	before := `{
		"id":"agent-1","name":"endpoint-agent",
		"agent_endpoint":{"version_selector":{"version_selection_rules":[
			{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}
		]},"protocol_configuration":{"responses":{}},"authorization_schemes":[{
			"type":"Entra",
			"isolation_key_source":{"kind":"Entra","future_identity_source":1},
			"future_authorization_field":"keep"
		}]},
		"versions":{"latest":{"version":"3"}}
	}`
	after := `{
		"id":"agent-1","name":"endpoint-agent",
		"agent_endpoint":{"version_selector":{"version_selection_rules":[
			{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}
		]},"protocol_configuration":{"responses":{},"activity":{}},"authorization_schemes":[{
			"type":"Entra",
			"isolation_key_source":{"kind":"Entra","future_identity_source":1},
			"future_authorization_field":"keep"
		},{"type":"BotServiceRbac"}]},
		"agent_card":{"version":"1.0.0","description":"Support agent","skills":[{"id":"support","name":"Support","description":"Handles support requests","tags":["help"]}]},
		"versions":{"latest":{"version":"3"}}
	}`
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/endpoint-agent": routeSequence(
			route(http.StatusOK, before),
			route(http.StatusNoContent, ""),
			route(http.StatusOK, after),
		),
	}}
	stubCredentialAndHTTP(t, http)
	receiptPath := filepath.Join(t.TempDir(), "endpoint-receipt.json")
	run := runCLI(t, "", "endpoint-configure", "-f", manifest, "--receipt", receiptPath, "--output", "json")
	if run.code != 0 {
		t.Fatalf("endpoint-configure failed: %s", run.stderr)
	}
	var result endpointResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.SelectorMode != "pinned" ||
		strings.Join(result.ActiveVersions, ",") != "2" ||
		result.AgentCard == nil || result.AgentCard.Version != "1.0.0" {
		t.Fatalf("endpoint configuration did not preserve and verify state: %#v", result)
	}
	if result.Receipt != receiptPath {
		t.Fatalf("unexpected receipt path %q", result.Receipt)
	}

	var patchBody []byte
	for _, request := range http.requests {
		if request.Method == "PATCH" {
			var err error
			patchBody, err = io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if patchBody == nil {
		t.Fatal("endpoint-configure did not PATCH the agent")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(patchBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["agent_card"] == nil || body["agent_endpoint"] == nil {
		t.Fatalf("endpoint and card must be top-level PATCH siblings: %#v", body)
	}
	var endpoint map[string]json.RawMessage
	if err := json.Unmarshal(body["agent_endpoint"], &endpoint); err != nil {
		t.Fatal(err)
	}
	if _, changedRouting := endpoint["version_selector"]; changedRouting {
		t.Fatalf("endpoint configuration must not patch routing: %s", body["agent_endpoint"])
	}
	if !strings.Contains(string(body["agent_endpoint"]), `"activity"`) ||
		!strings.Contains(string(body["agent_card"]), `"support"`) {
		t.Fatalf("PATCH omitted requested endpoint/card settings: %#v", body)
	}
	if !strings.Contains(string(body["agent_endpoint"]), `"isolation_key_source"`) ||
		!strings.Contains(string(body["agent_endpoint"]), `"future_authorization_field":"keep"`) {
		t.Fatalf("PATCH dropped server-managed authorization fields: %s", body["agent_endpoint"])
	}
}

func TestCmdEndpointConfigureReconcilesAmbiguousPatchWithoutRoutingChange(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: endpoint-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
endpoint:
  protocols: [activity]
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/endpoint-agent": routeSequence(
			route(http.StatusOK, `{
				"name":"endpoint-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}]}},
				"versions":{"latest":{"version":"3"}}
			}`),
			route(http.StatusServiceUnavailable, `{"error":"busy"}`),
			route(http.StatusOK, `{
				"name":"endpoint-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}]},"protocol_configuration":{"responses":{},"activity":{}},"authorization_schemes":[{"type":"Entra"}]},
				"versions":{"latest":{"version":"3"}}
			}`),
		),
	}}
	stubCredentialAndHTTP(t, http)
	receiptPath := filepath.Join(t.TempDir(), "endpoint-reconciled.json")
	run := runCLI(t, "", "endpoint-configure", "-f", manifest, "--receipt", receiptPath, "--output", "json")
	if run.code != 0 {
		t.Fatalf("ambiguous endpoint PATCH should reconcile: %s", run.stderr)
	}

	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"succeeded-reconciled"`) ||
		!strings.Contains(string(data), "reconciled from committed endpoint state") {
		t.Fatalf("receipt did not document reconciliation and routing safety: %s", data)
	}
	patches := 0
	for _, request := range http.requests {
		if request.Method == "PATCH" {
			patches++
		}
	}

	if patches != 1 {
		t.Fatalf("ambiguous endpoint PATCH must not be retried automatically; got %d PATCHes", patches)
	}
}

func TestCmdEndpointConfigureRemovesProtocolsMissingFromManifest(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: endpoint-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
endpoint:
  protocols: [responses]
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/endpoint-agent": routeSequence(
			route(http.StatusOK, `{
				"name":"endpoint-agent",
				"agent_endpoint":{
					"version_selector":{"version_selection_rules":[{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}]},
					"protocol_configuration":{"responses":{},"mcp":{}},
					"authorization_schemes":[{"type":"Entra"}]
				},
				"versions":{"latest":{"version":"3"}}
			}`),
			route(http.StatusNoContent, ""),
			route(http.StatusOK, `{
				"name":"endpoint-agent",
				"agent_endpoint":{
					"version_selector":{"version_selection_rules":[{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}]},
					"protocol_configuration":{"responses":{}},
					"authorization_schemes":[{"type":"Entra"}]
				},
				"versions":{"latest":{"version":"3"}}
			}`),
		),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "endpoint-configure", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("endpoint protocol removal failed: %s", run.stderr)
	}
	var patchBody []byte
	for _, request := range http.requests {
		if request.Method == "PATCH" {
			var err error
			patchBody, err = io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if patchBody == nil || !strings.Contains(string(patchBody), `"mcp":null`) {
		t.Fatalf("removed protocol must be sent as an explicit merge-patch null: %s", patchBody)
	}
}

func TestCmdRollbackPinsConcretePriorVersionAfterYesAndVerification(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions/1": route(http.StatusOK, `{"id":"v1","name":"base-agent","version":"1","definition":{}}`),
		"/agents/base-agent": routeSequence(
			route(http.StatusOK, `{
				"id":"agent-1","name":"base-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}]}},
				"versions":{"latest":{"version":"3"}}
			}`),
			route(http.StatusNoContent, ""),
			route(http.StatusOK, `{
				"id":"agent-1","name":"base-agent",
				"agent_endpoint":{"version_selector":{"version_selection_rules":[{"type":"FixedRatio","agent_version":"1","traffic_percentage":100}]}},
				"versions":{"latest":{"version":"3"}}
			}`),
		),
	}}
	stubCredentialAndHTTP(t, http)
	receiptPath := filepath.Join(t.TempDir(), "rollback.json")
	run := runCLI(t, "", "rollback", "-f", manifest, "--agent-version", "1", "--yes", "--receipt", receiptPath, "--output", "json")
	if run.code != 0 {
		t.Fatalf("rollback failed: %s", run.stderr)
	}
	var result releaseResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.ActiveVersion != "1" || result.SelectorMode != "pinned" {
		t.Fatalf("rollback did not verify pinned prior version: %#v", result)
	}
	patches := 0
	for _, request := range http.requests {
		if request.Method != "PATCH" {
			continue
		}
		patches++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"agent_version":"1"`) ||
			strings.Contains(string(body), `"version_selector":null`) {
			t.Fatalf("rollback must pin the concrete version, got %s", body)
		}
	}
	if patches != 1 {
		t.Fatalf("expected one verified routing PATCH, got %d", patches)
	}
}

func TestCmdRollbackRequiresConfirmationWithoutYes(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent/versions/1": route(http.StatusOK, `{"id":"v1","name":"base-agent","version":"1","definition":{}}`),
		"/agents/base-agent": route(http.StatusOK, `{
			"name":"base-agent",
			"agent_endpoint":{"version_selector":{"version_selection_rules":[{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}]}},
			"versions":{"latest":{"version":"3"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "rollback", "-f", manifest, "--agent-version", "1", "--output", "json")
	if run.code == 0 {
		t.Fatal("rollback without --yes must not proceed in structured mode")
	}
	for _, request := range http.requests {
		if request.Method == "PATCH" {
			t.Fatalf("rollback prompted for confirmation but still patched: %s", request.URL)
		}
	}
}

// ---------- legacy and experimental gates ----------

func TestCmdLegacyStatusOrchestratesExplicitResourcesWithoutAzure(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: legacy-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
`)
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/applications/legacy-app": route(http.StatusOK, `{
			"name":"legacy-app","properties":{"displayName":"Legacy app"}
		}`),
		"/applications/legacy-app/agentDeployments/legacy-deployment": route(http.StatusOK, `{
			"name":"legacy-deployment","properties":{"deploymentId":"deployment-id","state":"Running"}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "legacy-status", "-f", manifest, "--application-name", "legacy-app", "--deployment-name", "legacy-deployment", "--output", "json")
	if run.code != 0 {
		t.Fatalf("legacy-status failed: %s", run.stderr)
	}
	var result legacyStatusResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Application.Exists || !result.Deployment.Exists ||
		result.Deployment.Properties.DeploymentID != "deployment-id" {
		t.Fatalf("unexpected legacy status: %#v", result)
	}
	if len(http.requests) != 2 {
		t.Fatalf("legacy status should orchestrate exactly two ARM reads, got %d", len(http.requests))
	}
}

func TestCmdAutopilotInfoAndPreflightSafetyGatesAvoidToolExecution(t *testing.T) {
	info := runCLI(t, "", "autopilot-info", "--output", "json")
	if info.code != 0 {
		t.Fatalf("autopilot-info failed: %s", info.stderr)
	}
	var result autopilotInfoResult
	if err := json.Unmarshal([]byte(info.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Experimental || result.PromptAgentSupported ||
		result.ReviewedCommit == "" {
		t.Fatalf("autopilot info did not expose the safety boundary: %#v", result)
	}

	tests := []struct {
		name     string
		args     []string
		want     string
		wantCode int
	}{
		{"preview not accepted", append([]string{"autopilot-preflight", "--output", "json", "--approve-sample-commit", "a2de504ff6b69149bd40d89edd1c86dc11c6af57", "--region", "approved-region", "--allowed-region", "approved-region"}, []string{}...), "not explicitly accepted", 3},
		{"unapproved commit", []string{"autopilot-preflight", "--output", "json", "--accept-preview", "--approve-sample-commit", strings.Repeat("0", 40), "--region", "approved-region", "--allowed-region", "approved-region"}, "not explicitly approved", 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runCLI(t, "", test.args...)
			if run.code != test.wantCode || !strings.Contains(run.stderr, test.want) {
				t.Fatalf("expected safety gate %q, code=%d stderr=%s", test.want, run.code, run.stderr)
			}
		})
	}
}

func TestCmdPublishM365RejectsDefaultLatestRoutingBeforeBotMutation(t *testing.T) {
	const subscription = "11111111-2222-3333-4444-555555555555"
	const tenant = "99999999-8888-7777-6666-555555555555"
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: m365-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
`)
	publicationPath := filepath.Join(t.TempDir(), "publication.yaml")
	if err := os.WriteFile(publicationPath, []byte(`apiVersion: foundry-agent-manager/publication/v1
microsoft365:
  bot_service:
    name: support-bot
    tenant_id: `+tenant+`
    display_name: Support Agent
  publish_scope: Shared
  app_version: 1.0.0
  agent_display_name: Support Agent
  short_description: Support
  full_description: Support agent
  developer_name: Contoso
`), 0o600); err != nil {
		t.Fatal(err)
	}
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/m365-agent": route(http.StatusOK, `{
			"id":"agent-id","name":"m365-agent",
			"instance_identity":{
				"principal_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
				"client_id":"bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
			},
			"versions":{"latest":{"version":"2"}}
		}`),
	}}
	stubCredentialAndHTTP(t, http)
	run := runCLI(t, "", "publish-m365", "-f", manifest, "--publication", publicationPath, "--output", "json")
	if run.code != 7 || !strings.Contains(run.stderr, "must pin all traffic") {
		t.Fatalf("expected pinned-version conflict, code=%d stderr=%s", run.code, run.stderr)
	}
	if len(http.requests) != 1 || http.requests[0].Method != "GET" {
		t.Fatalf("publish must stop before Bot Service mutation: %#v", http.requests)
	}
}

// ---------- publication ambiguity ----------

func TestCmdPublishM365AmbiguousResponseDoesNotRetryAndLeavesUnknownReceipt(t *testing.T) {
	const subscription = "11111111-2222-3333-4444-555555555555"
	const tenant = "99999999-8888-7777-6666-555555555555"
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: m365-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/`+subscription+`/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
`)
	publicationPath := filepath.Join(t.TempDir(), "publication.yaml")
	if err := os.WriteFile(publicationPath, []byte(`apiVersion: foundry-agent-manager/publication/v1
microsoft365:
  bot_service:
    name: support-bot
    tenant_id: `+tenant+`
  publish_scope: Shared
  app_version: 1.0.0
  agent_display_name: Support Agent
  short_description: Support agent
  full_description: Handles support requests.
  developer_name: Contoso
`), 0o600); err != nil {
		t.Fatal(err)
	}
	botID := "/subscriptions/" + subscription + "/resourceGroups/agents-rg/providers/Microsoft.BotService/botServices/support-bot"
	http := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/m365-agent": route(http.StatusOK, `{
			"id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","name":"m365-agent",
			"instance_identity":{
				"principal_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
				"client_id":"bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
			},
			"agent_endpoint":{"version_selector":{"version_selection_rules":[{"type":"FixedRatio","agent_version":"2","traffic_percentage":100}]}},
			"versions":{"latest":{"version":"2"}}
		}`),
		"/providers/Microsoft.BotService": route(http.StatusOK, `{"namespace":"Microsoft.BotService","registrationState":"Registered"}`),
		"/botServices/support-bot": route(http.StatusOK, `{
			"id":"`+botID+`","name":"support-bot","location":"global","kind":"azurebot","sku":{"name":"F0"},
			"properties":{"displayName":"Support Agent","endpoint":"https://account.services.ai.azure.com/api/projects/project/agents/m365-agent/endpoint/protocols/activityProtocol?api-version=2025-05-15-preview","msaAppId":"bbbbbbbb-cccc-dddd-eeee-ffffffffffff","msaAppTenantId":"`+tenant+`","msaAppType":"SingleTenant","publicNetworkAccess":"Disabled"}
		}`),
		"/botServices/support-bot/channels/MsTeamsChannel": route(http.StatusOK, `{
			"id":"`+botID+`/channels/MsTeamsChannel","name":"MsTeamsChannel","location":"global","properties":{"channelName":"MsTeamsChannel"}
		}`),
		"/agents/m365-agent/microsoft365/publish": route(http.StatusServiceUnavailable, `{"error":"busy"}`),
	}}
	stubCredentialAndHTTP(t, http)
	receiptPath := filepath.Join(t.TempDir(), "m365-unknown.json")
	run := runCLI(t, "", "publish-m365", "-f", manifest, "--publication", publicationPath, "--receipt", receiptPath, "--output", "json")
	if run.code == 0 {
		t.Fatal("ambiguous M365 publication must not report success")
	}
	publishes := 0
	for _, request := range http.requests {
		if request.Method == "POST" && strings.HasSuffix(request.URL.Path, "/microsoft365/publish") {
			publishes++
		}
	}
	if publishes != 1 {
		t.Fatalf("ambiguous M365 publish must not retry automatically; got %d POSTs", publishes)
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"status": "unknown"`) ||
		!strings.Contains(string(data), `"kind": "microsoft365-publication"`) ||
		!strings.Contains(string(data), "inspect the Foundry publication state and Microsoft 365 catalogs before retrying") {
		t.Fatalf("ambiguous publication receipt lacks reconciliation state: %s", data)
	}
}

// Ensure unused imports don't accumulate.
var (
	_ = fmt.Sprintf
	_ = bytes.Buffer{}
	_ foundry.HTTPClient
	_ = trust.FlagAPIMHost
)
