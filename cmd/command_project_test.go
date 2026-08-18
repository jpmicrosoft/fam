package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/receipt"
)

const projectCreateManifest = `apiVersion: foundry-agent-manager/v1
agent:
  name: project-agent
  model: model
  instructions: test
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
  location: eastus
`

func TestProjectCreateCreatesAndReconcilesProject(t *testing.T) {
	projectBody := `{
		"location":"eastus",
		"properties":{
			"endpoints":{
				"agents":"https://account.services.ai.azure.com/api/projects/project"
			}
		}
	}`
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/accounts/account/projects/project": routeSequence(
			route(http.StatusNotFound, `{"error":"missing"}`),
			route(http.StatusCreated, projectBody),
			route(http.StatusOK, projectBody),
		),
		"/accounts/account": route(http.StatusOK, `{"location":"eastus"}`),
		"/agents":           route(http.StatusOK, `{"data":[]}`),
	}}
	stubCredentialAndHTTP(t, httpClient)

	manifest := writeManifest(t, projectCreateManifest)
	receiptPath := filepath.Join(t.TempDir(), "project-create.json")
	command := commandWithFlags(t, "project-create", manifest, map[string]string{
		"output":                "json",
		"receipt":               receiptPath,
		"project-wait-timeout":  "1",
		"project-wait-interval": "0",
	})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := cmdProjectCreate(command, nil); err != nil {
		t.Fatal(err)
	}

	var result projectCreateResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.Ready {
		t.Fatalf("unexpected project result: %#v", result)
	}
	if result.Endpoint != "https://account.services.ai.azure.com/api/projects/project" {
		t.Fatalf("unexpected endpoint: %s", result.Endpoint)
	}
	if result.Location != "eastus" || result.Receipt != receiptPath {
		t.Fatalf("unexpected project metadata: %#v", result)
	}

	var operation receipt.ReceiptV2
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Operation != "project-create" || operation.Status != "succeeded" {
		t.Fatalf("unexpected receipt: %#v", operation)
	}
	if len(operation.Resources) != 1 ||
		operation.Resources[0].Kind != "foundryProject" ||
		!operation.Resources[0].CreatedByRun {
		t.Fatalf("unexpected project resource receipt: %#v", operation.Resources)
	}

	var putCount int
	for _, request := range httpClient.requests {
		if request.Method == http.MethodPut {
			putCount++
		}
	}
	if putCount != 1 {
		t.Fatalf("expected one project PUT, got %d", putCount)
	}
}

func TestProjectCreateIsIdempotentForExistingProject(t *testing.T) {
	projectBody := `{"location":"eastus"}`
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/accounts/account/projects/project": routeSequence(
			route(http.StatusOK, projectBody),
			route(http.StatusOK, projectBody),
		),
		"/agents": route(http.StatusOK, `{"data":[]}`),
	}}
	stubCredentialAndHTTP(t, httpClient)

	manifest := writeManifest(t, projectCreateManifest)
	command := commandWithFlags(t, "project-create", manifest, map[string]string{
		"output":                "json",
		"receipt":               filepath.Join(t.TempDir(), "project-create.json"),
		"project-wait-timeout":  "1",
		"project-wait-interval": "0",
	})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := cmdProjectCreate(command, nil); err != nil {
		t.Fatal(err)
	}
	var result projectCreateResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Created || !result.Ready {
		t.Fatalf("unexpected idempotent result: %#v", result)
	}
	for _, request := range httpClient.requests {
		if request.Method == http.MethodPut {
			t.Fatalf("existing project must not be mutated: %s", request.URL)
		}
	}
}

func TestProjectCreateRejectsInvalidResourceID(t *testing.T) {
	broken := `apiVersion: foundry-agent-manager/v1
agent:
  name: project-agent
  model: model
  instructions: test
project:
  resource_id: /subscriptions/bad/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct/projects/proj
`
	manifest := writeManifest(t, broken)
	command := commandWithFlags(t, "project-create", manifest, nil)
	err := cmdProjectCreate(command, nil)
	if err == nil || !strings.Contains(err.Error(), "not a valid UUID") {
		t.Fatalf("expected UUID error, got %v", err)
	}
}
