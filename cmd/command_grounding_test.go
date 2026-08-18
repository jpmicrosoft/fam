package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/foundry"
	groundingdomain "foundry-agent-manager/internal/grounding"
)

const groundingManifest = `apiVersion: foundry-agent-manager/v1
agent:
  name: grounded-agent
  model: model
  instructions: Answer from the managed documents.
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
grounding:
  vector_stores:
    - name: product-docs
      description: Product documentation.
      files:
        - path: knowledge/guide.txt
tools:
  - type: file_search
    vector_store: product-docs
`

func writeGroundingFixture(
	t *testing.T,
	contents string,
) (string, groundingdomain.VectorStore) {
	t.Helper()
	base := t.TempDir()
	documentPath := filepath.Join(base, "knowledge", "guide.txt")
	if err := os.MkdirAll(filepath.Dir(documentPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(documentPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(base, "agent.yaml")
	if err := os.WriteFile(manifest, []byte(groundingManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := config.LoadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := config.ResolveConfig(document)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := groundingdomain.Build(resolved.Grounding, base, true)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, definitions[0]
}

func managedStoreJSON(
	definition groundingdomain.VectorStore,
	id string,
	status string,
	desiredHash string,
) string {
	return fmt.Sprintf(
		`{"id":%q,"name":%q,"description":%q,"status":%q,"metadata":{"foundry_agent_manager":"true","logical_name":%q,"desired_hash":%q}}`,
		id,
		definition.Name,
		definition.Description,
		status,
		definition.Name,
		desiredHash,
	)
}

func attachmentJSON(
	id string,
	status string,
	pathHash string,
	sha string,
	filename string,
) string {
	return fmt.Sprintf(
		`{"id":%q,"vector_store_id":"vs-1","status":%q,"attributes":{"fam_managed":"true","fam_path_hash":%q,"fam_sha256":%q,"fam_filename":%q}}`,
		id,
		status,
		pathHash,
		sha,
		filename,
	)
}

func TestGroundingOfflineCommands(t *testing.T) {
	manifest, definition := writeGroundingFixture(t, "grounding content")
	validate := runCLI(t, "", "grounding-validate", "-f", manifest, "--output", "json")
	if validate.code != 0 {
		t.Fatalf("grounding-validate failed: %s", validate.stderr)
	}
	plan := runCLI(t, "", "grounding-plan", "-f", manifest, "--output", "json")
	if plan.code != 0 {
		t.Fatalf("grounding-plan failed: %s", plan.stderr)
	}
	var result groundingPlanResult
	if err := json.Unmarshal([]byte(plan.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.VectorStores) != 1 ||
		result.VectorStores[0].DesiredHash != definition.DesiredHash ||
		result.VectorStores[0].Files[0].SHA256 != definition.Files[0].SHA256 {
		t.Fatalf("unexpected grounding plan: %#v", result)
	}
}

func TestGroundingSyncCreatesUploadsAndFinalizesStore(t *testing.T) {
	manifest, definition := writeGroundingFixture(t, "grounding content")
	receiptPath := filepath.Join(t.TempDir(), "grounding-sync.json")
	created := managedStoreJSON(definition, "vs-1", "in_progress", "")
	completed := managedStoreJSON(
		definition,
		"vs-1",
		"completed",
		definition.DesiredHash,
	)
	attachment := attachmentJSON(
		"file-1",
		"completed",
		definition.Files[0].PathHash,
		definition.Files[0].SHA256,
		definition.Files[0].Filename,
	)
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/openai/v1/vector_stores": routeSequence(
			route(http.StatusOK, `{"data":[],"has_more":false}`),
			route(http.StatusCreated, created),
		),
		"/openai/v1/vector_stores/vs-1/files": routeSequence(
			route(http.StatusOK, `{"data":[],"has_more":false}`),
			route(http.StatusCreated, strings.Replace(attachment, `"completed"`, `"in_progress"`, 1)),
			route(http.StatusOK, `{"data":[`+attachment+`],"has_more":false}`),
		),
		"/openai/v1/files": route(
			http.StatusCreated,
			`{"id":"file-1","filename":"guide.txt","purpose":"assistants"}`,
		),
		"/openai/v1/vector_stores/vs-1/files/file-1": route(
			http.StatusOK,
			attachment,
		),
		"/openai/v1/vector_stores/vs-1": routeSequence(
			route(http.StatusOK, strings.Replace(completed, definition.DesiredHash, "", 1)),
			route(http.StatusOK, completed),
			route(http.StatusOK, completed),
		),
	}}
	stubCredentialAndHTTP(t, httpClient)
	run := runCLI(
		t,
		"",
		"grounding-sync",
		"-f",
		manifest,
		"--index-interval",
		"1ms",
		"--receipt",
		receiptPath,
		"--output",
		"json",
	)
	if run.code != 0 {
		t.Fatalf("grounding-sync failed: %s", run.stderr)
	}
	var result groundingSyncResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.Changed || result.Uploaded != 1 ||
		result.Detached != 0 || result.Status != "completed" {
		t.Fatalf("unexpected sync result: %#v", result)
	}
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("grounding receipt was not written: %v", err)
	}
}

func TestGroundingSyncIsNoOpWhenRemoteStateMatches(t *testing.T) {
	manifest, definition := writeGroundingFixture(t, "grounding content")
	completed := managedStoreJSON(
		definition,
		"vs-1",
		"completed",
		definition.DesiredHash,
	)
	attachment := attachmentJSON(
		"file-1",
		"completed",
		definition.Files[0].PathHash,
		definition.Files[0].SHA256,
		definition.Files[0].Filename,
	)
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/openai/v1/vector_stores": route(
			http.StatusOK,
			`{"data":[`+completed+`],"has_more":false}`,
		),
		"/openai/v1/vector_stores/vs-1/files": route(
			http.StatusOK,
			`{"data":[`+attachment+`],"has_more":false}`,
		),
		"/openai/v1/vector_stores/vs-1": routeSequence(
			route(http.StatusOK, completed),
			route(http.StatusOK, completed),
		),
	}}
	stubCredentialAndHTTP(t, httpClient)
	run := runCLI(
		t,
		"",
		"grounding-sync",
		"-f",
		manifest,
		"--index-interval",
		"1ms",
		"--receipt",
		filepath.Join(t.TempDir(), "receipt.json"),
		"--output",
		"json",
	)
	if run.code != 0 {
		t.Fatalf("grounding-sync failed: %s", run.stderr)
	}
	var result groundingSyncResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Created || result.Changed || result.Uploaded != 0 || result.Detached != 0 {
		t.Fatalf("synchronized state must be a no-op: %#v", result)
	}
	for _, request := range httpClient.requests {
		if request.Method != http.MethodGet {
			t.Fatalf("no-op synchronization issued a mutation: %s %s", request.Method, request.URL)
		}
	}
}

func TestGroundingSyncReplacesChangedDocument(t *testing.T) {
	manifest, definition := writeGroundingFixture(t, "new grounding content")
	oldSHA := sha256.Sum256([]byte("old grounding content"))
	oldAttachment := attachmentJSON(
		"file-old",
		"completed",
		definition.Files[0].PathHash,
		hex.EncodeToString(oldSHA[:]),
		definition.Files[0].Filename,
	)
	newAttachment := attachmentJSON(
		"file-new",
		"completed",
		definition.Files[0].PathHash,
		definition.Files[0].SHA256,
		definition.Files[0].Filename,
	)
	inProgress := managedStoreJSON(definition, "vs-1", "completed", "old-hash")
	completed := managedStoreJSON(
		definition,
		"vs-1",
		"completed",
		definition.DesiredHash,
	)
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/openai/v1/vector_stores": route(
			http.StatusOK,
			`{"data":[`+inProgress+`],"has_more":false}`,
		),
		"/openai/v1/vector_stores/vs-1/files": routeSequence(
			route(http.StatusOK, `{"data":[`+oldAttachment+`],"has_more":false}`),
			route(http.StatusCreated, strings.Replace(newAttachment, `"completed"`, `"in_progress"`, 1)),
			route(http.StatusOK, `{"data":[`+newAttachment+`],"has_more":false}`),
		),
		"/openai/v1/files": route(
			http.StatusCreated,
			`{"id":"file-new","filename":"guide.txt","purpose":"assistants"}`,
		),
		"/openai/v1/vector_stores/vs-1/files/file-new": route(
			http.StatusOK,
			newAttachment,
		),
		"/openai/v1/vector_stores/vs-1/files/file-old": route(
			http.StatusOK,
			`{}`,
		),
		"/openai/v1/files/file-old": route(
			http.StatusOK,
			`{}`,
		),
		"/openai/v1/vector_stores/vs-1": routeSequence(
			route(http.StatusOK, inProgress),
			route(http.StatusOK, completed),
			route(http.StatusOK, completed),
		),
	}}
	stubCredentialAndHTTP(t, httpClient)
	run := runCLI(
		t,
		"",
		"grounding-sync",
		"-f",
		manifest,
		"--index-interval",
		"1ms",
		"--delete-replaced-uploads",
		"--yes",
		"--receipt",
		filepath.Join(t.TempDir(), "receipt.json"),
		"--output",
		"json",
	)
	if run.code != 0 {
		t.Fatalf("grounding-sync failed: %s", run.stderr)
	}
	var result groundingSyncResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Uploaded != 1 || result.Detached != 1 ||
		result.Deleted != 1 {
		t.Fatalf("changed document was not replaced: %#v", result)
	}
}

func TestGroundingSyncRefusesRemovedFilesWithoutPrune(t *testing.T) {
	manifest, definition := writeGroundingFixture(t, "grounding content")
	completed := managedStoreJSON(definition, "vs-1", "completed", "old-hash")
	current := attachmentJSON(
		"file-1",
		"completed",
		definition.Files[0].PathHash,
		definition.Files[0].SHA256,
		definition.Files[0].Filename,
	)
	stalePath := sha256.Sum256([]byte("knowledge/removed.txt"))
	stale := attachmentJSON(
		"file-old",
		"completed",
		hex.EncodeToString(stalePath[:]),
		"stale-sha",
		"removed.txt",
	)
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/openai/v1/vector_stores": route(
			http.StatusOK,
			`{"data":[`+completed+`],"has_more":false}`,
		),
		"/openai/v1/vector_stores/vs-1/files": route(
			http.StatusOK,
			`{"data":[`+current+`,`+stale+`],"has_more":false}`,
		),
	}}
	stubCredentialAndHTTP(t, httpClient)
	run := runCLI(
		t,
		"",
		"grounding-sync",
		"-f",
		manifest,
		"--receipt",
		filepath.Join(t.TempDir(), "receipt.json"),
		"--output",
		"json",
	)
	if run.code != 7 {
		t.Fatalf("removed managed files must require --prune: %d / %s", run.code, run.stderr)
	}
	if detail := decodeErrorEnvelope(t, run); !strings.Contains(detail.Message, "--prune --yes") {
		t.Fatalf("prune refusal omitted remediation: %#v", detail)
	}
}

func TestManagedGroundingResolutionBlocksIncompleteFilesAndRejectsDuplicates(t *testing.T) {
	manifest, definition := writeGroundingFixture(t, "grounding content")
	command := commandWithFlags(t, "deploy", manifest, nil)
	prepared := prepareForTest(t, command)
	stale := managedStoreJSON(definition, "vs-1", "completed", "old-hash")
	client := foundry.NewClient(
		prepared.Resolved.Config.Project.Endpoint,
		transactionCredential{},
		&scriptedHTTP{routes: map[string]scriptedRoute{
			"/openai/v1/vector_stores": route(
				http.StatusOK,
				`{"data":[`+stale+`],"has_more":false}`,
			),
			"/openai/v1/vector_stores/vs-1/files": route(
				http.StatusOK,
				`{"data":[],"has_more":false}`,
			),
		}},
		false,
	)
	err := resolvePreparedManagedGrounding(context.Background(), client, prepared)
	if err == nil || !strings.Contains(err.Error(), "grounding sync") {
		t.Fatalf("stale managed grounding must block deployment, got %v", err)
	}

	command = commandWithFlags(t, "deploy", manifest, nil)
	prepared = prepareForTest(t, command)
	completed := managedStoreJSON(
		definition,
		"vs-1",
		"completed",
		definition.DesiredHash,
	)
	current := attachmentJSON(
		"file-1",
		"completed",
		definition.Files[0].PathHash,
		definition.Files[0].SHA256,
		definition.Files[0].Filename,
	)
	client = foundry.NewClient(
		prepared.Resolved.Config.Project.Endpoint,
		transactionCredential{},
		&scriptedHTTP{routes: map[string]scriptedRoute{
			"/openai/v1/vector_stores": route(
				http.StatusOK,
				`{"data":[`+completed+`],"has_more":false}`,
			),
			"/openai/v1/vector_stores/vs-1/files": route(
				http.StatusOK,
				`{"data":[`+current+`],"has_more":false}`,
			),
		}},
		false,
	)
	if err := resolvePreparedManagedGrounding(
		context.Background(),
		client,
		prepared,
	); err != nil {
		t.Fatal(err)
	}
	wire := prepared.WireTools[0].(map[string]interface{})
	ids, _ := wire["vector_store_ids"].([]interface{})
	if len(ids) != 1 || ids[0] != "vs-1" {
		t.Fatalf("logical grounding was not resolved for deployment: %#v", wire)
	}

	command = commandWithFlags(t, "deploy", manifest, nil)
	prepared = prepareForTest(t, command)
	duplicate := strings.Replace(completed, `"vs-1"`, `"vs-2"`, 1)
	client = foundry.NewClient(
		prepared.Resolved.Config.Project.Endpoint,
		transactionCredential{},
		&scriptedHTTP{routes: map[string]scriptedRoute{
			"/openai/v1/vector_stores": route(
				http.StatusOK,
				`{"data":[`+completed+`,`+duplicate+`],"has_more":false}`,
			),
		}},
		false,
	)
	err = resolvePreparedManagedGrounding(context.Background(), client, prepared)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate logical stores must fail closed, got %v", err)
	}
}
