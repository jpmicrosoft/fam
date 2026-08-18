package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

const toolboxManifest = `apiVersion: foundry-agent-manager/v1
agent:
  name: toolbox-agent
  model: model
  instructions: Help.
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
toolboxes:
  - name: operations
    description: Operational tools.
    tools:
      - type: toolbox_search
`

func TestToolboxOfflineCommands(t *testing.T) {
	manifest := writeManifest(t, toolboxManifest)
	for _, command := range []string{"toolbox-validate", "toolbox-plan"} {
		run := runCLI(t, "", command, "-f", manifest, "--output", "json")
		if run.code != 0 {
			t.Fatalf("%s failed: %s", command, run.stderr)
		}
	}
}

func TestToolboxPreviewRequiresExplicitAcceptanceBeforeAzure(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: toolbox-agent
  model: model
  instructions: Help.
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
toolboxes:
  - name: operations
    description: Operational tools.
    tools:
      - type: reminder_preview
`)
	run := runCLI(t, "", "toolbox-deploy", "-f", manifest, "--output", "json")
	if run.code != 3 {
		t.Fatalf("preview deploy must fail before Azure: code=%d stderr=%s", run.code, run.stderr)
	}
	errorDetail := decodeErrorEnvelope(t, run)
	if errorDetail.Kind != "config" {
		t.Fatalf("unexpected error: %#v", errorDetail)
	}
}

func TestToolboxDeployRequiresExternalDestinationApprovalBeforeAzure(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: toolbox-agent
  model: model
  instructions: Help.
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
toolboxes:
  - name: operations
    description: Operational tools.
    tools:
      - type: a2a_preview
        project_connection_id: a2a-connection
        base_url: https://a2a.contoso.com
`)
	run := runCLI(
		t,
		"",
		"toolbox-deploy",
		"-f",
		manifest,
		"--accept-preview",
		"--output",
		"json",
	)
	if run.code != 4 {
		t.Fatalf(
			"external Toolbox destination must fail before Azure: code=%d stderr=%s",
			run.code,
			run.stderr,
		)
	}
}

func TestToolboxDeployCreatesStagedImmutableVersionAndReceipt(t *testing.T) {
	manifest := writeManifest(t, toolboxManifest)
	receiptPath := filepath.Join(t.TempDir(), "toolbox-receipt.json")
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/toolboxes/operations": routeSequence(
			route(http.StatusNotFound, `{"error":"not found"}`),
			route(http.StatusOK, `{"name":"operations","default_version":"1"}`),
		),
		"/toolboxes/operations/versions": routeSequence(
			route(http.StatusOK, `{"data":[],"has_more":false}`),
			route(http.StatusCreated, `{"id":"toolbox-version","name":"operations","version":"1"}`),
		),
	}}
	stubCredentialAndHTTP(t, httpClient)
	run := runCLI(
		t,
		"",
		"toolbox-deploy",
		"-f",
		manifest,
		"--receipt",
		receiptPath,
		"--output",
		"json",
	)
	if run.code != 0 {
		t.Fatalf("Toolbox deploy failed: %s", run.stderr)
	}
	var result toolboxDeployResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Version != "1" || result.Staged ||
		result.DefaultVersion != "1" {
		t.Fatalf("unexpected deploy result: %#v", result)
	}
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("receipt was not written: %v", err)
	}
	var create *http.Request
	for _, request := range httpClient.requests {
		if request.Method == http.MethodPost {
			create = request
			break
		}
	}
	if create == nil ||
		create.URL.Path != "/api/projects/project/toolboxes/operations/versions" {
		t.Fatalf("create request missing or incorrect: %#v", create)
	}
}

func TestToolboxPromoteAndDeleteProtectDefaultVersion(t *testing.T) {
	manifest := writeManifest(t, toolboxManifest)
	promoteHTTP := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/toolboxes/operations": routeSequence(
			route(http.StatusOK, `{"name":"operations","default_version":"1"}`),
			route(http.StatusOK, `{}`),
			route(http.StatusOK, `{"name":"operations","default_version":"2"}`),
		),
		"/toolboxes/operations/versions/2": route(
			http.StatusOK,
			`{"name":"operations","version":"2"}`,
		),
	}}
	stubCredentialAndHTTP(t, promoteHTTP)
	promoteReceipt := filepath.Join(t.TempDir(), "promote.json")
	run := runCLI(
		t,
		"",
		"toolbox-promote",
		"-f",
		manifest,
		"--toolbox-version",
		"2",
		"--yes",
		"--receipt",
		promoteReceipt,
		"--output",
		"json",
	)
	if run.code != 0 {
		t.Fatalf("Toolbox promote failed: %s", run.stderr)
	}
	var promoted toolboxMutationResult
	if err := json.Unmarshal([]byte(run.stdout), &promoted); err != nil {
		t.Fatal(err)
	}
	if !promoted.Changed || promoted.DefaultVersion != "2" {
		t.Fatalf("unexpected promotion: %#v", promoted)
	}

	deleteHTTP := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/toolboxes/operations": route(
			http.StatusOK,
			`{"name":"operations","default_version":"2"}`,
		),
		"/toolboxes/operations/versions/2": route(
			http.StatusOK,
			`{"name":"operations","version":"2"}`,
		),
	}}
	stubCredentialAndHTTP(t, deleteHTTP)
	run = runCLI(
		t,
		"",
		"toolbox-delete-version",
		"-f",
		manifest,
		"--toolbox-version",
		"2",
		"--yes",
		"--output",
		"json",
	)
	if run.code != 7 {
		t.Fatalf("default Toolbox version deletion must conflict: code=%d stderr=%s", run.code, run.stderr)
	}

	deleteHTTP = &scriptedHTTP{routes: map[string]scriptedRoute{
		"/toolboxes/operations": route(
			http.StatusOK,
			`{"name":"operations","default_version":"2"}`,
		),
		"/toolboxes/operations/versions/1": routeSequence(
			route(http.StatusOK, `{"name":"operations","version":"1"}`),
			route(http.StatusNoContent, ``),
			route(http.StatusNotFound, `{"error":"not found"}`),
		),
	}}
	stubCredentialAndHTTP(t, deleteHTTP)
	deleteReceipt := filepath.Join(t.TempDir(), "delete.json")
	run = runCLI(
		t,
		"",
		"toolbox-delete-version",
		"-f",
		manifest,
		"--toolbox-version",
		"1",
		"--yes",
		"--receipt",
		deleteReceipt,
		"--output",
		"json",
	)
	if run.code != 0 {
		t.Fatalf("non-default Toolbox version deletion failed: %s", run.stderr)
	}
}
