package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"foundry-agent-manager/internal/agentdiff"
	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/receipt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type transactionCredential struct{}

func (transactionCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type transactionHTTP struct {
	requests  []*http.Request
	responses []*http.Response
}

func (c *transactionHTTP) Do(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req)
	response := c.responses[0]
	c.responses = c.responses[1:]
	response.Request = req
	return response, nil
}

func transactionResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
	}
}

func transactionConfig(t *testing.T) *config.ResolvedConfig {
	t.Helper()
	profile, err := azcloud.Resolve(azcloud.AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	return &config.ResolvedConfig{
		Cloud: profile,
		Agent: config.AgentSpec{Name: "agent"},
		Project: config.ProjectSpec{
			Name:           "project",
			AccountName:    "account",
			ResourceGroup:  "rg",
			SubscriptionID: "sub",
			APIVersion:     config.DefaultProjectAPIVersion,
			ARMEndpoint:    profile.ARMEndpoint,
			ARMScope:       profile.ARMScope,
		},
		Apim: config.ApimSpec{
			Enabled:              true,
			Target:               "https://gateway.azure-api.net/agents/chat",
			Auth:                 "managed_identity",
			Audience:             "https://cognitiveservices.azure.com",
			ConnectionAPIVersion: config.DefaultConnectionAPIVersion,
		},
	}
}

func transactionConnectionResponse(t *testing.T, cfg *config.ResolvedConfig, name string, models []string) *http.Response {
	t.Helper()
	body, err := connection.BuildConnectionBody(&cfg.Apim, models, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]interface{}{
		"id":         "/connections/" + name,
		"name":       name,
		"properties": body["properties"],
	})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(data))),
	}
}

func TestDeploymentCompensationRemovesCreatedVersionAndConnection(t *testing.T) {
	cfg := transactionConfig(t)
	models := []string{"model"}
	base := &transactionHTTP{responses: []*http.Response{
		transactionResponse(http.StatusNoContent),
		transactionConnectionResponse(t, cfg, "apim-agent", models),
		transactionResponse(http.StatusNoContent),
	}}
	httpClient := httpx.NewRetryClient(base, httpx.Options{Retries: 0})
	store := receipt.New(
		filepath.Join(t.TempDir(), "receipt.json"),
		cfg.Cloud.Name,
		"agent.yaml",
		"manifest",
		"desired",
		cfg.Agent.Name,
	)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	client := foundry.NewClientWithOptions(
		"https://account.services.ai.azure.com/api/projects/project",
		transactionCredential{},
		httpClient,
		foundry.ClientOptions{Scope: cfg.Cloud.FoundryScope},
	)
	transaction := &deploymentTransaction{
		store:               store,
		cfg:                 cfg,
		credential:          transactionCredential{},
		httpClient:          httpClient,
		client:              client,
		agentVersionCreated: true,
		agentVersion:        "7",
		connectionCreated:   true,
		connectionName:      "apim-agent",
		connectionModels:    models,
		allowSharedRollback: true,
	}
	if err := transaction.compensate(); err != nil {
		t.Fatal(err)
	}
	if len(base.requests) != 3 {
		t.Fatalf("expected three compensation requests, got %d", len(base.requests))
	}
	if !strings.Contains(base.requests[0].URL.Path, "/agents/agent/versions/7") ||
		base.requests[1].Method != http.MethodGet ||
		base.requests[2].Method != http.MethodDelete ||
		!strings.Contains(base.requests[2].URL.Path, "/connections/apim-agent") {
		t.Fatalf("unexpected compensation requests: %s; %s; %s",
			base.requests[0].URL,
			base.requests[1].URL,
			base.requests[2].URL,
		)
	}
	if !store.Receipt.Agent.Compensated || !store.Receipt.APIM.Compensated {
		t.Fatalf("receipt did not record compensation: %#v", store.Receipt)
	}
}

func TestDeploymentCompensationRestoresManagedIdentityConnection(t *testing.T) {
	cfg := transactionConfig(t)
	models := []string{"model"}
	base := &transactionHTTP{responses: []*http.Response{
		transactionResponse(http.StatusNoContent),
		transactionConnectionResponse(t, cfg, "apim-agent", models),
		transactionResponse(http.StatusOK),
	}}
	httpClient := httpx.NewRetryClient(base, httpx.Options{Retries: 0})
	store := receipt.New(
		filepath.Join(t.TempDir(), "receipt.json"),
		cfg.Cloud.Name,
		"agent.yaml",
		"manifest",
		"desired",
		cfg.Agent.Name,
	)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	transaction := &deploymentTransaction{
		store:      store,
		cfg:        cfg,
		credential: transactionCredential{},
		httpClient: httpClient,
		client: foundry.NewClientWithOptions(
			"https://account.services.ai.azure.com/api/projects/project",
			transactionCredential{},
			httpClient,
			foundry.ClientOptions{Scope: cfg.Cloud.FoundryScope},
		),
		agentVersionCreated: true,
		agentVersion:        "8",
		connectionUpdated:   true,
		previousConnection: connection.State{
			Exists: true,
			Name:   "apim-agent",
			Properties: map[string]interface{}{
				"category":      "ApiManagement",
				"target":        "https://gateway.azure-api.net/old",
				"authType":      "ProjectManagedIdentity",
				"isSharedToAll": false,
				"credentials":   map[string]interface{}{},
			},
		},
		connectionName:      "apim-agent",
		connectionModels:    models,
		allowSharedRollback: true,
	}
	if err := transaction.compensate(); err != nil {
		t.Fatal(err)
	}
	if base.requests[1].Method != http.MethodGet || base.requests[2].Method != http.MethodPut {
		t.Fatalf("expected connection GET then restore PUT, got %s then %s",
			base.requests[1].Method,
			base.requests[2].Method,
		)
	}
	if !store.Receipt.APIM.Compensated {
		t.Fatalf("APIM restoration was not recorded: %#v", store.Receipt)
	}
}

func TestDeploymentCompensationReportsUnknownMutation(t *testing.T) {
	cfg := transactionConfig(t)
	store := receipt.New(
		filepath.Join(t.TempDir(), "receipt.json"),
		cfg.Cloud.Name,
		"agent.yaml",
		"manifest",
		"desired",
		cfg.Agent.Name,
	)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	transaction := &deploymentTransaction{
		store:              store,
		cfg:                cfg,
		uncertainMutations: []string{"agent-version"},
	}
	err := transaction.compensate()
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("expected manual reconciliation error, got %v", err)
	}
	last := store.Receipt.Steps[len(store.Receipt.Steps)-1]
	if last.Action != "reconcile-agent-version" || last.Status != "required" {
		t.Fatalf("receipt did not record required reconciliation: %#v", last)
	}
}

func TestDeploymentCompensationAfterAmbiguousVersionCreationLeavesRoutingPinned(t *testing.T) {
	cfg := transactionConfig(t)
	base := &transactionHTTP{}
	httpClient := httpx.NewRetryClient(base, httpx.Options{Retries: 0})
	store := receipt.New(
		filepath.Join(t.TempDir(), "receipt.json"),
		cfg.Cloud.Name,
		"agent.yaml",
		"manifest",
		"desired",
		cfg.Agent.Name,
	)
	transaction := &deploymentTransaction{
		store: store,
		cfg:   cfg,
		client: foundry.NewClientWithOptions(
			"https://account.services.ai.azure.com/api/projects/project",
			transactionCredential{},
			httpClient,
			foundry.ClientOptions{Scope: cfg.Cloud.FoundryScope},
		),
		agentExistedBefore:   true,
		agentCreateAttempted: true,
		agentCreateAmbiguous: true,
		selectorChanged:      true,
		activeVersionBefore:  "2",
		// The previous selector was @latest. Restoring it after an ambiguous
		// create could route production to an unknown candidate.
		selectorBefore: nil,
	}

	err := transaction.compensate()

	if err == nil || !strings.Contains(err.Error(), "remains pinned") {
		t.Fatalf("expected manual routing reconciliation, got %v", err)
	}
	if len(base.requests) != 0 {
		t.Fatalf("ambiguous creation must not restore @latest or issue any routing PATCH: %#v", base.requests)
	}
	var reconciliationRecorded bool
	for _, step := range store.Receipt.Steps {
		if step.Action == "reconcile-version-selector" && step.Status == "required" {
			reconciliationRecorded = true
		}
	}
	if !reconciliationRecorded {
		t.Fatalf("receipt must require manual routing reconciliation: %#v", store.Receipt.Steps)
	}
}

func TestDeploymentCompensationRefusesChangedAPIMConnection(t *testing.T) {
	cfg := transactionConfig(t)
	base := &transactionHTTP{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{
			"name": "apim-agent",
			"properties": {
				"target": "https://other.azure-api.net/agents/chat",
				"authType": "ProjectManagedIdentity"
			}
		}`)),
	}}}
	httpClient := httpx.NewRetryClient(base, httpx.Options{Retries: 0})
	store := receipt.New(
		filepath.Join(t.TempDir(), "receipt.json"),
		cfg.Cloud.Name,
		"agent.yaml",
		"manifest",
		"desired",
		cfg.Agent.Name,
	)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	transaction := &deploymentTransaction{
		store:               store,
		cfg:                 cfg,
		credential:          transactionCredential{},
		httpClient:          httpClient,
		connectionCreated:   true,
		connectionName:      "apim-agent",
		connectionModels:    []string{"model"},
		allowSharedRollback: true,
	}
	err := transaction.compensate()
	if err == nil || !strings.Contains(err.Error(), "refusing compensation") {
		t.Fatalf("expected concurrent-change protection, got %v", err)
	}
	if len(base.requests) != 1 || base.requests[0].Method != http.MethodGet {
		t.Fatalf("changed connection should not be deleted: %#v", base.requests)
	}
}

func TestDeploymentCompensationDefaultsSharedResourcesToManual(t *testing.T) {
	cfg := transactionConfig(t)
	store := receipt.New(
		filepath.Join(t.TempDir(), "receipt.json"),
		cfg.Cloud.Name,
		"agent.yaml",
		"manifest",
		"desired",
		cfg.Agent.Name,
	)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	transaction := &deploymentTransaction{
		store:             store,
		cfg:               cfg,
		connectionCreated: true,
		connectionName:    "apim-agent",
	}
	err := transaction.compensate()
	if err == nil || !strings.Contains(err.Error(), "manual reconciliation") {
		t.Fatalf("expected manual shared-resource reconciliation, got %v", err)
	}
	last := store.Receipt.Steps[len(store.Receipt.Steps)-1]
	if last.Action != "reconcile-apim" || last.Status != "required" {
		t.Fatalf("unexpected reconciliation receipt step: %#v", last)
	}
}

func TestDeploymentFailureNamesTheReceiptAndRecordsTerminalStatus(t *testing.T) {
	tests := []struct {
		name            string
		compensationErr error
		wantStatus      string
	}{
		{name: "compensated", compensationErr: nil, wantStatus: "failed-compensated"},
		{name: "partial", compensationErr: errors.New("APIM connection requires manual reconciliation"), wantStatus: "failed-partial"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receipt.json")
			store := receipt.New(path, "AzureCloud", "agent.yaml", "manifest", "desired", "agent")
			deployErr := errs.Foundry("create agent version failed (500)")

			err := deploymentFailure(store, deployErr, tt.compensationErr)
			if err == nil {
				t.Fatal("a failed deployment must return an error")
			}
			if !strings.Contains(err.Error(), "deployment receipt: "+path) {
				t.Fatalf("the failure must name the receipt path: %v", err)
			}
			if !strings.Contains(err.Error(), deployErr.Error()) {
				t.Fatalf("the failure must keep the original cause: %v", err)
			}
			if tt.compensationErr != nil && !strings.Contains(err.Error(), tt.compensationErr.Error()) {
				t.Fatalf("the failure must keep the compensation outcome: %v", err)
			}
			if errs.KindOf(err) != "foundry" || errs.ExitCode(err) != 10 {
				t.Fatalf("naming the receipt must not change the error kind or exit code: %s/%d",
					errs.KindOf(err), errs.ExitCode(err))
			}
			if store.Receipt.Status != tt.wantStatus {
				t.Fatalf("unexpected receipt status %q, want %q", store.Receipt.Status, tt.wantStatus)
			}
			if store.Receipt.CompletedAt == nil {
				t.Fatal("a terminal receipt must record a completion time")
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("the receipt must be persisted: %v", readErr)
			}
			if !strings.Contains(string(data), tt.wantStatus) {
				t.Fatalf("the persisted receipt is missing the terminal status: %s", data)
			}
			if strings.Contains(string(data), "deployment receipt:") {
				t.Fatalf("the receipt must not reference itself: %s", data)
			}
		})
	}
}

func TestDeploymentFailureRedactsRegisteredSecrets(t *testing.T) {
	// The last secret contains characters that encoding/json escapes, so it also
	// covers the escaped-encoding sweep in the receipt store.
	for _, secret := range []string{"super-secret-apim-key", `k<e>y&"1"/2`, "key with spaces"} {
		t.Run(secret, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receipt.json")
			store := receipt.New(path, "AzureCloud", "agent.yaml", "manifest", "desired", "agent")
			store.RegisterSecret(secret)
			if err := store.AddStep("apim-connection", "failed", "rejected key "+secret); err != nil {
				t.Fatal(err)
			}

			err := deploymentFailure(
				store,
				errs.Foundry("ARM rejected credentials {\"key\":\"%s\"}", secret),
				nil,
			)
			if err == nil {
				t.Fatal("expected a failure")
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(data), secret) {
				t.Fatalf("the receipt must never persist a registered credential: %s", data)
			}
			escaped, marshalErr := json.Marshal(secret)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if bare := string(escaped[1 : len(escaped)-1]); strings.Contains(string(data), bare) {
				t.Fatalf("the receipt must not persist the JSON-escaped credential: %s", data)
			}
			if !strings.Contains(string(data), "redacted") {
				t.Fatalf("the receipt must record the redaction: %s", data)
			}
		})
	}
}

func TestPromptVersionMetadataPreservesUnmanagedRemoteValues(t *testing.T) {
	remote := &foundry.Agent{}
	remote.Versions.Latest.Metadata = map[string]interface{}{
		"owner":       "platform",
		"environment": "production",
	}
	metadata, err := promptVersionMetadata(agentdiff.Desired{}, remote)
	if err != nil {
		t.Fatal(err)
	}
	if metadata["owner"] != "platform" || metadata["environment"] != "production" {
		t.Fatalf("unexpected preserved metadata: %#v", metadata)
	}

	managed, err := promptVersionMetadata(agentdiff.Desired{
		ManageMetadata: true,
		Metadata:       map[string]string{"owner": "operations"},
	}, remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 1 || managed["owner"] != "operations" {
		t.Fatalf("managed metadata did not replace remote values: %#v", managed)
	}
}
