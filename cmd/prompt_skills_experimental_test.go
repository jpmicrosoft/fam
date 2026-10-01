package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/receipt"
)

func nativePromptPinned(body string) string {
	return strings.Replace(body, `"versions":`, `"agent_endpoint":{"version_selector":{"version_selection_rules":[{"type":"FixedRatio","agent_version":"3","traffic_percentage":100}]}},"versions":`, 1)
}

func nativePromptVersionBody(t *testing.T, body string) string {
	t.Helper()
	var agent foundry.Agent
	if err := json.Unmarshal([]byte(body), &agent); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(agent.Versions.Latest)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNativePromptExperimentalDeployStagesExactReferences(t *testing.T) {
	for _, declaration := range []string{"", nativePromptSkillDeclaration, "  skills: []\n"} {
		t.Run(promptSkillsSummary(nil, declaration != "")+declaration, func(t *testing.T) {
			manifest := writeManifest(t, nativePromptManifest(declaration))
			client := nativePromptHTTP(t)
			before := nativePromptRemote("old instructions")
			after := strings.Replace(nativePromptRemote("base instructions"), `"version":"3"`, `"version":"4"`, 1)
			if declaration == "  skills: []\n" {
				after = strings.Replace(after, `"skills":[{"name":"greeting","version":"1"}]`, `"skills":[]`, 1)
			}
			client.routes["/agents/base-agent"] = routeSequence(
				route(http.StatusOK, before),
				route(http.StatusNoContent, ""),
				route(http.StatusOK, nativePromptPinned(before)),
				route(http.StatusOK, nativePromptPinned(after)),
			)
			client.routes["/agents/base-agent/versions"] = route(http.StatusOK, `{"id":"agent-1","name":"base-agent","version":"4"}`)
			client.routes["/agents/base-agent/versions/4"] = route(http.StatusOK, nativePromptVersionBody(t, after))
			client.routes["/openai/v1/responses"] = route(http.StatusOK, `{"id":"response-1","output_text":"synthetic native invocation"}`)
			stubCredentialAndHTTP(t, client)
			receiptPath := filepath.Join(t.TempDir(), "receipt.json")
			run := runCLI(t, "", "deploy", "-f", manifest, "--accept-preview", "--experimental-native-skills",
				"--smoke-test",
				"--receipt", receiptPath, "--output", "json")
			if run.code != 0 {
				t.Fatal(run.stderr)
			}
			var result deployResult
			if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
				t.Fatal(err)
			}
			if !result.Staged || result.Status != "staged" || result.ActiveVersion != "3" ||
				result.LatestVersion != "4" || result.Smoke == nil {
				t.Fatalf("experimental native deployment changed staging semantics: %#v", result)
			}
			wantCount := 1
			if declaration == "  skills: []\n" {
				wantCount = 0
			}
			if result.Skills == nil || len(*result.Skills) != wantCount {
				t.Fatalf("result lost native inventory: %#v", result)
			}
			var creates, readbacks int
			for _, request := range client.requests {
				if strings.Contains(request.URL.Path, "/agents/") || strings.Contains(request.URL.Path, "/openai/") {
					if request.Header.Get("Foundry-Features") != "Skills=V1Preview" {
						t.Fatalf("native operation omitted its required preview header: %s %#v", request.URL, request.Header)
					}
				}
				if strings.Contains(request.URL.Path, "/skills/") && request.Method != http.MethodGet {
					t.Fatalf("agent deployment must not mutate shared Skills: %s %s", request.Method, request.URL)
				}
				if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/agents/base-agent/versions/4") {
					readbacks++
				}
				if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/agents/base-agent/versions") {
					continue
				}
				creates++
				data, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				var body struct {
					Definition struct {
						Instructions string                   `json:"instructions"`
						Skills       []map[string]interface{} `json:"skills"`
						Harness      interface{}              `json:"harness"`
					} `json:"definition"`
				}
				if err := json.Unmarshal(data, &body); err != nil {
					t.Fatal(err)
				}
				if body.Definition.Instructions != "base instructions" || body.Definition.Harness != nil ||
					body.Definition.Skills == nil || len(body.Definition.Skills) != wantCount {
					t.Fatalf("unexpected native payload or fallback: %s", data)
				}
				for _, reference := range body.Definition.Skills {
					if len(reference) != 2 || reference["name"] != "greeting" || reference["version"] != "1" {
						t.Fatalf("unexpected native reference: %s", data)
					}
				}
			}
			if creates != 1 || readbacks != 1 {
				t.Fatalf("create/readback counts = %d/%d", creates, readbacks)
			}
			data, err := os.ReadFile(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "native-skills-readback") {
				t.Fatalf("receipt did not record exact verification: %s", data)
			}
		})
	}
}

func TestNativePromptExperimentalInvocationGate(t *testing.T) {
	const prompt = "Use the attached greeting Skill to return its verification marker."
	for _, test := range []struct {
		name      string
		args      []string
		ok        bool
		canonical bool
	}{
		{name: "neither-opt-in"},
		{name: "preview-only", args: []string{"--accept-preview"}},
		{name: "experimental-only", args: []string{"--experimental-native-skills"}},
		{name: "both-opt-ins", args: []string{"--accept-preview", "--experimental-native-skills"}, ok: true},
		{name: "canonical-neither-opt-in", canonical: true},
		{name: "canonical-preview-only", args: []string{"--accept-preview"}, canonical: true},
		{name: "canonical-experimental-only", args: []string{"--experimental-native-skills"}, canonical: true},
		{name: "canonical-both-opt-ins", args: []string{"--accept-preview", "--experimental-native-skills"}, ok: true, canonical: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := writeManifest(t, nativePromptManifest(nativePromptSkillDeclaration))
			version := nativeSmokeVersion("3", true)
			client := nativeSmokeHTTP(t, version, nil, version)
			stubCredentialAndHTTP(t, client)
			args := []string{"smoke", "-f", manifest, "--prompt", prompt}
			if test.canonical {
				args = append([]string{"prompt"}, args...)
			}
			args = append(args, test.args...)
			run := runCLI(t, "", args...)
			if (run.code == 0) != test.ok {
				t.Fatalf("unexpected native invocation gate result: %#v", run)
			}
			if !test.ok {
				if len(client.requests) != 0 {
					t.Fatalf("invocation gate must precede mutation: %#v", client.requests)
				}
				return
			}
			if len(client.requests) != 3 {
				t.Fatalf("native invocation must inspect routing and the selected version: %#v", client.requests)
			}
			for index, request := range client.requests {
				if request.Header.Get("Foundry-Features") != "Skills=V1Preview" ||
					(index < 2 && request.Method != http.MethodGet) {
					t.Fatalf("unexpected native inspection/invocation: %s %s %#v", request.Method, request.URL, request.Header)
				}
			}
			request := client.requests[2]
			if request.Method != http.MethodPost ||
				!strings.HasSuffix(request.URL.Path, "/agents/base-agent/endpoint/protocols/openai/responses") ||
				request.URL.RawQuery != "api-version=v1" {
				t.Fatalf("unexpected native stable invocation: %s %s", request.Method, request.URL)
			}
			var body map[string]interface{}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["input"] != prompt {
				t.Fatalf("native smoke must not inject Skill content or runtime overrides: %#v", body)
			}
		})
	}
}

func TestNativePromptExperimentalFailuresRetainCompensationSemantics(t *testing.T) {
	for _, test := range []struct {
		name              string
		createStatus      int
		readbackBody      string
		wantStatus        string
		wantDelete        int
		wantSelectorPatch int
	}{
		{name: "silently-omitted-skills", createStatus: http.StatusOK, readbackBody: `{"name":"base-agent","version":"4","definition":{}}`, wantStatus: "failed-compensated", wantDelete: 1, wantSelectorPatch: 2},
		{name: "known-rejection", createStatus: http.StatusBadRequest, wantStatus: "failed-compensated", wantSelectorPatch: 2},
		{name: "known-preview-rejection", createStatus: http.StatusForbidden, wantStatus: "failed-compensated", wantSelectorPatch: 2},
		{name: "ambiguous-create", createStatus: http.StatusServiceUnavailable, wantStatus: "failed-partial", wantSelectorPatch: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := writeManifest(t, nativePromptManifest(nativePromptSkillDeclaration))
			client := nativePromptHTTP(t)
			before := nativePromptRemote("old instructions")
			client.routes["/agents/base-agent"] = routeSequence(
				route(http.StatusOK, before),
				route(http.StatusNoContent, ""),
				route(http.StatusOK, nativePromptPinned(before)),
				route(http.StatusNoContent, ""),
				route(http.StatusOK, before),
			)
			client.routes["/agents/base-agent/versions"] = route(test.createStatus, `{"name":"base-agent","version":"4"}`)
			client.routes["/agents/base-agent/versions/4"] = routeSequence(
				route(http.StatusOK, test.readbackBody),
				route(http.StatusNoContent, ""),
			)
			stubCredentialAndHTTP(t, client)
			path := filepath.Join(t.TempDir(), "receipt.json")
			run := runCLI(t, "", "deploy", "-f", manifest, "--accept-preview", "--experimental-native-skills", "--receipt", path)
			if run.code == 0 {
				t.Fatalf("failed native operation was reported as success: %#v", run)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var recorded receipt.Receipt
			if err := json.Unmarshal(data, &recorded); err != nil {
				t.Fatal(err)
			}
			if recorded.Status != test.wantStatus {
				t.Fatalf("unexpected terminal receipt: %s", data)
			}
			var deletes, patches int
			for _, request := range client.requests {
				if strings.Contains(request.URL.Path, "/agents/") && request.Header.Get("Foundry-Features") != "Skills=V1Preview" {
					t.Fatalf("native operation or compensation omitted its required preview header: %s %#v", request.URL, request.Header)
				}
				if request.Method == http.MethodDelete {
					deletes++
					if !strings.HasSuffix(request.URL.Path, "/agents/base-agent/versions/4") {
						t.Fatalf("compensation must not delete shared Skills: %s", request.URL)
					}
				}
				if request.Method == http.MethodPatch {
					patches++
				}
			}
			if deletes != test.wantDelete || patches != test.wantSelectorPatch {
				t.Fatalf("unexpected compensation requests: delete=%d selector-patch=%d", deletes, patches)
			}
		})
	}
}
