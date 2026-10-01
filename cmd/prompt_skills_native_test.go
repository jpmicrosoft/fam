package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/agentdiff"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/receipt"
	"foundry-agent-manager/internal/skills"
)

const nativePromptSkillDeclaration = "  skills:\n    - name: greeting\n      version: \"1\"\n"

func nativePromptManifest(declaration string) string {
	return strings.Replace(baseManifest, "  instructions: base instructions\n",
		"  instructions: base instructions\n"+declaration, 1)
}

// These fixtures are synthetic, not Azure service captures.
func nativePromptRemote(instructions string) string {
	return `{"id":"agent-1","name":"base-agent","versions":{"latest":{
		"name":"base-agent","version":"3","definition":{
			"kind":"prompt","model":"base-model","instructions":"` + instructions + `",
			"tools":[{"type":"code_interpreter"}],
			"skills":[{"name":"greeting","version":"1"}]
		}}}}`
}

func nativePromptArchive(t *testing.T, name string, supporting bool) string {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("---\nname: " + name + "\ndescription: Say hello\n---\nReply with a greeting.\n")); err != nil {
		t.Fatal(err)
	}
	if supporting {
		file, err := writer.Create("script.py")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("not executed")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func nativePromptHTTP(t *testing.T) *scriptedHTTP {
	t.Helper()
	return &scriptedHTTP{routes: map[string]scriptedRoute{
		"/projects/project":                   route(http.StatusOK, preflightProjectResponse),
		"/agents":                             route(http.StatusOK, `{"data":[]}`),
		"/deployments/base-model":             modelDeploymentRoute("base-model"),
		"/agents/base-agent":                  route(http.StatusOK, nativePromptRemote("base instructions")),
		"/skills/greeting/versions/1":         route(http.StatusOK, `{"name":"greeting","version":"1"}`),
		"/skills/greeting/versions/1/content": route(http.StatusOK, nativePromptArchive(t, "greeting", false)),
	}}
}

func requireNativeReadOnly(t *testing.T, client *scriptedHTTP) {
	t.Helper()
	for _, request := range client.requests {
		if request.Method != http.MethodGet {
			t.Fatalf("native gate/validation mutated Azure: %s %s", request.Method, request.URL)
		}
		if strings.Contains(request.URL.Path, "/skills/") &&
			request.Header.Get("Foundry-Features") != "Skills=V1Preview" {
			t.Fatalf("Skills resource lookup omitted its existing preview header: %#v", request.Header)
		}
	}
}

func TestNativePromptSkillsOfflineOutput(t *testing.T) {
	for _, declaration := range []string{"", "  skills: []\n", nativePromptSkillDeclaration} {
		manifest := writeManifest(t, nativePromptManifest(declaration))
		run := runCLI(t, "", "plan", "-f", manifest, "--output", "json")
		if run.code != 0 {
			t.Fatal(run.stderr)
		}
		var result planResult
		if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
			t.Fatal(err)
		}
		if result.SkillsConfigured != (declaration != "") {
			t.Fatalf("offline plan lost presence: %#v", result)
		}
		if declaration == nativePromptSkillDeclaration &&
			(len(result.Skills) != 1 || result.Skills[0].Version != "1") {
			t.Fatalf("offline plan lost immutable pins: %#v", result)
		}
		run = runCLI(t, "", "validate", "-f", manifest, "--output", "json")
		if run.code != 0 {
			t.Fatal(run.stderr)
		}
		var validation validateResult
		if err := json.Unmarshal([]byte(run.stdout), &validation); err != nil {
			t.Fatal(err)
		}
		if !validation.Valid || !strings.Contains(validation.SkillsValidation, "not evaluated") {
			t.Fatalf("offline validation must not imply native readiness: %#v", validation)
		}
	}
}

func TestNativePromptPreflightRequiresAcceptance(t *testing.T) {
	for _, declaration := range []string{"", "  skills: []\n", nativePromptSkillDeclaration} {
		manifest := writeManifest(t, nativePromptManifest(declaration))
		command := commandWithFlags(t, "preflight", manifest, nil)
		client := nativePromptHTTP(t)
		_, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, client)
		if err == nil || !strings.Contains(err.Error(), "--accept-preview") {
			t.Fatalf("native references must require acceptance, including preservation: %v", err)
		}
		requireNativeReadOnly(t, client)
	}
}

func TestNativePromptPreflightContentAndContractErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		path     string
		response scriptedRoute
		want     string
	}{
		{name: "valid-content-contract-gated", want: "transport is gated"},
		{name: "missing", path: "/skills/greeting/versions/1", response: route(http.StatusNotFound, "{}"), want: "does not exist"},
		{name: "forbidden", path: "/skills/greeting/versions/1", response: route(http.StatusForbidden, "{}"), want: "403"},
		{name: "wrong-version", path: "/skills/greeting/versions/1", response: route(http.StatusOK, `{"name":"greeting","version":"2"}`), want: "different name or version"},
		{name: "wrong-name", path: "/skills/greeting/versions/1/content", response: route(http.StatusOK, nativePromptArchive(t, "other", false)), want: "contains SKILL.md for"},
		{name: "supporting-file", path: "/skills/greeting/versions/1/content", response: route(http.StatusOK, nativePromptArchive(t, "greeting", true)), want: "instructions-only"},
		{name: "malformed-archive", path: "/skills/greeting/versions/1/content", response: route(http.StatusOK, "not a ZIP"), want: "cannot read skill ZIP"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := writeManifest(t, nativePromptManifest(nativePromptSkillDeclaration))
			command := commandWithFlags(t, "preflight", manifest, map[string]string{"accept-preview": "true"})
			client := nativePromptHTTP(t)
			if test.path != "" {
				client.routes[test.path] = test.response
			}
			_, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, client)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
			requireNativeReadOnly(t, client)
		})
	}
}

func TestNativePromptPreflightRequiresExistingProject(t *testing.T) {
	manifest := writeManifest(t, nativePromptManifest(nativePromptSkillDeclaration))
	command := commandWithFlags(t, "preflight", manifest, map[string]string{
		"accept-preview": "true", "ensure-project": "true",
	})
	client := nativePromptHTTP(t)
	client.routes["/projects/project"] = route(http.StatusNotFound, "{}")
	_, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, client)
	if err == nil || !strings.Contains(err.Error(), "existing project") {
		t.Fatalf("native references must not implicitly create their project: %v", err)
	}
	requireNativeReadOnly(t, client)
}

func TestNativePromptPreservationCannotDropMissingReference(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	client := nativePromptHTTP(t)
	client.routes["/skills/greeting/versions/1"] = route(http.StatusNotFound, "{}")
	stubCredentialAndHTTP(t, client)
	run := runCLI(t, "", "deploy", "-f", manifest, "--accept-preview", "--if-changed")
	if run.code == 0 || !strings.Contains(run.stderr, "does not exist") {
		t.Fatalf("unmanaged remote refs must never be silently dropped: %#v", run)
	}
	requireNativeReadOnly(t, client)
}

func TestNativePromptDeploymentUnchangedPreservesReferencesAndReceipt(t *testing.T) {
	for _, declaration := range []string{"", nativePromptSkillDeclaration} {
		t.Run(promptSkillsSummary(nil, declaration != ""), func(t *testing.T) {
			manifest := writeManifest(t, nativePromptManifest(declaration))
			client := nativePromptHTTP(t)
			stubCredentialAndHTTP(t, client)
			receiptPath := filepath.Join(t.TempDir(), "receipt.json")
			run := runCLI(t, "", "deploy", "-f", manifest, "--accept-preview", "--if-changed", "--receipt", receiptPath, "--output", "json")
			if run.code != 0 {
				t.Fatal(run.stderr)
			}
			var result deployResult
			if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
				t.Fatal(err)
			}
			if result.Changed || result.Status != "unchanged" || result.Skills == nil ||
				len(*result.Skills) != 1 || (*result.Skills)[0].Version != "1" {
				t.Fatalf("unchanged deploy lost references: %#v", result)
			}
			data, err := os.ReadFile(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			var recorded receipt.Receipt
			if err := json.Unmarshal(data, &recorded); err != nil {
				t.Fatal(err)
			}
			if recorded.DesiredHash == "" || !strings.Contains(string(data), "greeting@1") ||
				!strings.Contains(string(data), "sha256=") {
				t.Fatalf("receipt must preserve pins and content provenance: %s", data)
			}
			requireNativeReadOnly(t, client)
		})
	}
}

func TestNativePromptDeploymentGatesPreservationRemovalAndSmokeBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name         string
		declaration  string
		instructions string
		extra        []string
	}{
		{name: "preserve", instructions: "old instructions"},
		{name: "remove", declaration: "  skills: []\n", instructions: "base instructions"},
		{name: "smoke-unchanged", instructions: "base instructions", extra: []string{"--smoke-test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := writeManifest(t, nativePromptManifest(test.declaration))
			client := nativePromptHTTP(t)
			client.routes["/agents/base-agent"] = route(http.StatusOK, nativePromptRemote(test.instructions))
			stubCredentialAndHTTP(t, client)
			args := []string{"deploy", "-f", manifest, "--accept-preview", "--if-changed", "--ensure-project"}
			args = append(args, test.extra...)
			run := runCLI(t, "", args...)
			if run.code == 0 || !strings.Contains(run.stderr, "transport is gated") {
				t.Fatalf("native transport must remain gated: %#v", run)
			}
			requireNativeReadOnly(t, client)
		})
	}
}

func TestNativePromptDiffUsesReadOnlyContentValidation(t *testing.T) {
	for _, missing := range []bool{false, true} {
		manifest := writeManifest(t, nativePromptManifest(nativePromptSkillDeclaration))
		client := nativePromptHTTP(t)
		if missing {
			client.routes["/skills/greeting/versions/1"] = route(http.StatusNotFound, "{}")
		}
		stubCredentialAndHTTP(t, client)
		run := runCLI(t, "", "diff", "-f", manifest, "--accept-preview", "--output", "json")
		if missing {
			if run.code == 0 || !strings.Contains(run.stderr, "does not exist") {
				t.Fatalf("diff silently ignored missing pinned content: %#v", run)
			}
		} else {
			if run.code != 0 {
				t.Fatal(run.stderr)
			}
			var result fullDiffResult
			if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
				t.Fatal(err)
			}
			if result.Changed || result.Agent.CurrentHash != result.Agent.DesiredHash {
				t.Fatalf("equal native references created drift: %#v", result)
			}
		}
		requireNativeReadOnly(t, client)
	}
}

func TestNativePromptReadbackClearMustBeExplicit(t *testing.T) {
	for _, test := range []struct {
		body string
		ok   bool
	}{
		{`{"name":"agent","version":"4","definition":{"skills":[]}}`, true},
		{`{"name":"agent","version":"4","definition":{}}`, false},
		{`{"name":"agent","version":"4","definition":{"skills":[{"name":"greeting","version":"1"}]}}`, false},
	} {
		httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
			"/agents/agent/versions/4": route(http.StatusOK, test.body),
		}}
		client := foundry.NewClient("https://account.services.ai.azure.com/api/projects/project", transactionCredential{}, httpClient, false)
		err := verifyPromptSkillsVersion(context.Background(), client, "agent", "4", agentdiff.Desired{ManageSkills: true})
		if (err == nil) != test.ok {
			t.Fatalf("clear readback = %v; response = %s", err, test.body)
		}
	}
}

func TestNativePromptReadbackFailureCompensatesOnlyCandidate(t *testing.T) {
	cfg := transactionConfig(t)
	base := &transactionHTTP{responses: []*http.Response{
		jsonResponse(http.StatusOK, `{"name":"agent","version":"4","definition":{}}`),
		transactionResponse(http.StatusNoContent),
	}}
	httpClient := httpx.NewRetryClient(base, httpx.Options{Retries: 0})
	client := foundry.NewClient("https://account.services.ai.azure.com/api/projects/project", transactionCredential{}, httpClient, false)
	store := receipt.New(filepath.Join(t.TempDir(), "receipt.json"), cfg.Cloud.Name, "agent.yaml", "manifest", "desired", cfg.Agent.Name)
	transaction := &deploymentTransaction{
		store: store, cfg: cfg, client: client,
		agentVersionCreated: true, agentVersion: "4", agentExistedBefore: true,
	}
	deployErr := verifyPromptSkillsVersion(context.Background(), client, "agent", "4", agentdiff.Desired{
		ManageSkills: true, Skills: []skills.Reference{{Name: "greeting", Version: "1"}},
	})
	if deployErr == nil {
		t.Fatal("missing native references must fail deployment")
	}
	compensationErr := transaction.compensate()
	if compensationErr != nil {
		t.Fatal(compensationErr)
	}
	if err := deploymentFailure(store, deployErr, compensationErr); err == nil {
		t.Fatal("compensation must not turn a failed native readback into success")
	}
	if store.Receipt.Status != "failed-compensated" || !store.Receipt.Agent.Compensated {
		t.Fatalf("candidate compensation not recorded: %#v", store.Receipt)
	}
	if len(base.requests) != 2 || base.requests[1].Method != http.MethodDelete ||
		!strings.HasSuffix(base.requests[1].URL.Path, "/agents/agent/versions/4") {
		t.Fatalf("only the known candidate, never shared Skills, may be deleted: %#v", base.requests)
	}
}

func TestNativePromptReadbackRequiresExactVersionAndReferences(t *testing.T) {
	desired := agentdiff.Desired{
		ManageSkills: true,
		Skills:       []skills.Reference{{Name: "greeting", Version: "1"}, {Name: "review", Version: "2"}},
	}
	for _, test := range []struct {
		name string
		body string
		ok   bool
	}{
		{"exact", `{"name":"agent","version":"4","definition":{"skills":[{"name":"greeting","version":"1"},{"name":"review","version":"2"}]}}`, true},
		{"missing", `{"name":"agent","version":"4","definition":{}}`, false},
		{"rewritten", `{"name":"agent","version":"4","definition":{"skills":[{"name":"greeting","version":"9"},{"name":"review","version":"2"}]}}`, false},
		{"reordered", `{"name":"agent","version":"4","definition":{"skills":[{"name":"review","version":"2"},{"name":"greeting","version":"1"}]}}`, false},
		{"wrong-version", `{"name":"agent","version":"5","definition":{"skills":[{"name":"greeting","version":"1"},{"name":"review","version":"2"}]}}`, false},
		{"unpinned", `{"name":"agent","version":"4","definition":{"skills":[{"name":"greeting"}]}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
				"/agents/agent/versions/4": route(http.StatusOK, test.body),
			}}
			client := foundry.NewClient("https://account.services.ai.azure.com/api/projects/project", transactionCredential{}, httpClient, false)
			err := verifyPromptSkillsVersion(context.Background(), client, "agent", "4", desired)
			if (err == nil) != test.ok {
				t.Fatalf("exact native readback: %v", err)
			}
			if err != nil && !errs.IsKind(err, "foundry") {
				t.Fatalf("readback must surface a Foundry error: %v", err)
			}
			if len(httpClient.requests) != 1 || !strings.HasSuffix(httpClient.requests[0].URL.Path, "/versions/4") {
				t.Fatalf("verification must read the exact created immutable version: %#v", httpClient.requests)
			}
		})
	}
}
