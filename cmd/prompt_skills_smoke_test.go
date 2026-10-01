package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"foundry-agent-manager/internal/foundry"
)

func nativeSmokeVersion(version string, native bool) foundry.AgentVersion {
	result := foundry.AgentVersion{
		Name: "base-agent", Version: version,
		Definition: map[string]interface{}{
			"kind": "prompt", "model": "base-model", "instructions": "base instructions",
		},
	}
	if native {
		result.Definition["skills"] = []map[string]string{{"name": "greeting", "version": "1"}}
	}
	return result
}

func nativeSmokeHTTP(t *testing.T, latest foundry.AgentVersion, rules []foundry.FixedRatioVersionSelectionRule, selected ...foundry.AgentVersion) *scriptedHTTP {
	t.Helper()
	agent := foundry.Agent{Name: "base-agent"}
	agent.Versions.Latest = latest
	if rules != nil {
		agent.AgentEndpoint = &foundry.AgentEndpointConfig{
			VersionSelector: &foundry.VersionSelector{VersionSelectionRules: rules},
		}
	}
	encode := func(value interface{}) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	client := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, encode(agent)),
		"/agents/base-agent/endpoint/protocols/openai/responses": route(http.StatusOK, `{"id":"response-1","output_text":"synthetic invocation"}`),
	}}
	for _, version := range selected {
		client.routes["/agents/base-agent/versions/"+version.Version] = route(http.StatusOK, encode(version))
	}
	return client
}

func TestNativePromptSmokeRemoteOnlySkillsGate(t *testing.T) {
	for _, declaration := range []struct{ name, value string }{
		{"omitted", ""}, {"explicit-empty", "  skills: []\n"},
	} {
		for _, routing := range []struct {
			name     string
			rules    []foundry.FixedRatioVersionSelectionRule
			selected []foundry.AgentVersion
		}{
			{name: "default-latest", selected: []foundry.AgentVersion{nativeSmokeVersion("3", true)}},
			{
				name:     "explicit-latest",
				rules:    []foundry.FixedRatioVersionSelectionRule{foundry.NewFixedRatioVersionSelectionRule("@latest", 100)},
				selected: []foundry.AgentVersion{nativeSmokeVersion("3", true)},
			},
			{
				name:     "pinned-not-latest",
				rules:    []foundry.FixedRatioVersionSelectionRule{foundry.NewFixedRatioVersionSelectionRule("2", 100)},
				selected: []foundry.AgentVersion{nativeSmokeVersion("2", true)},
			},
			{
				name: "split-native-second",
				rules: []foundry.FixedRatioVersionSelectionRule{
					foundry.NewFixedRatioVersionSelectionRule("1", 50),
					foundry.NewFixedRatioVersionSelectionRule("2", 50),
				},
				selected: []foundry.AgentVersion{nativeSmokeVersion("1", false), nativeSmokeVersion("2", true)},
			},
		} {
			for _, flags := range []struct {
				name string
				args []string
				want string
			}{
				{name: "neither", want: "--accept-preview"},
				{name: "preview-only", args: []string{"--accept-preview"}, want: "--experimental-native-skills"},
				{name: "experimental-only", args: []string{"--experimental-native-skills"}, want: "--accept-preview"},
				{name: "both", args: []string{"--accept-preview", "--experimental-native-skills"}},
			} {
				t.Run(declaration.name+"/"+routing.name+"/"+flags.name, func(t *testing.T) {
					manifest := writeManifest(t, nativePromptManifest(declaration.value))
					// The logical agent's latest summary deliberately omits Skills.
					client := nativeSmokeHTTP(t, nativeSmokeVersion("3", false), routing.rules, routing.selected...)
					stubCredentialAndHTTP(t, client)
					args := []string{"prompt", "smoke", "-f", manifest, "--prompt", "Use the greeting Skill."}
					run := runCLI(t, "", append(args, flags.args...)...)
					if flags.want != "" {
						if run.code == 0 || !strings.Contains(run.stderr, flags.want) {
							t.Fatalf("expected gate %q: %#v", flags.want, run)
						}
						requireNativeReadOnly(t, client)
					} else if run.code != 0 {
						t.Fatal(run.stderr)
					}
					if flags.name == "experimental-only" {
						if len(client.requests) != 0 {
							t.Fatal("invalid flags must be rejected before HTTP")
						}
						return
					}
					for _, version := range routing.selected {
						found := false
						for _, request := range client.requests {
							if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/versions/"+version.Version) {
								found = true
							}
						}
						if !found {
							t.Fatalf("selected immutable version %s was not inspected", version.Version)
						}
					}
					if flags.want == "" {
						last := client.requests[len(client.requests)-1]
						if len(client.requests) != len(routing.selected)+2 ||
							last.Method != http.MethodPost ||
							!strings.HasSuffix(last.URL.Path, "/endpoint/protocols/openai/responses") ||
							last.Header.Get("Foundry-Features") != "Skills=V1Preview" {
							t.Fatalf("unexpected invocation requests: %#v", client.requests)
						}
					}
				})
			}
		}
	}
}

func TestNativePromptSmokeIgnoresUnselectedSkills(t *testing.T) {
	for _, empty := range []bool{false, true} {
		manifest := writeManifest(t, baseManifest)
		selected := nativeSmokeVersion("2", false)
		if empty {
			selected.Definition["skills"] = []interface{}{}
		}
		client := nativeSmokeHTTP(t, nativeSmokeVersion("3", true),
			[]foundry.FixedRatioVersionSelectionRule{
				foundry.NewFixedRatioVersionSelectionRule("2", 100),
				foundry.NewFixedRatioVersionSelectionRule("@latest", 0),
			}, selected)
		stubCredentialAndHTTP(t, client)
		run := runCLI(t, "", "smoke", "-f", manifest)
		if run.code != 0 {
			t.Fatal(run.stderr)
		}
		if len(client.requests) != 3 || client.requests[2].Method != http.MethodPost {
			t.Fatalf("ordinary smoke did not inspect only its selected version: %#v", client.requests)
		}
		for _, request := range client.requests {
			if request.Header.Get("Foundry-Features") != "" ||
				strings.HasSuffix(request.URL.Path, "/versions/3") {
				t.Fatalf("unselected native version affected ordinary smoke: %s %#v", request.URL, request.Header)
			}
		}
	}
}

func TestNativePromptSmokeFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, path, want string
		response         scriptedRoute
	}{
		{"missing-agent", "/agents/base-agent", "was not found", route(http.StatusNotFound, "{}")},
		{"forbidden-agent", "/agents/base-agent", "403", route(http.StatusForbidden, "{}")},
		{"unresolved-routing", "/agents/base-agent", "selector", route(http.StatusOK, `{"name":"base-agent"}`)},
		{"malformed-routing", "/agents/base-agent", "selector", route(http.StatusOK, `{"name":"base-agent","agent_endpoint":{"version_selector":{}}}`)},
		{"unresolved-latest", "/agents/base-agent", "no immutable version resolved", route(http.StatusOK, `{"name":"base-agent","versions":{"latest":{"version":"@latest"}}}`)},
		{"blank-latest", "/agents/base-agent", "no immutable version resolved", route(http.StatusOK, `{"name":"base-agent","versions":{"latest":{"version":" "}}}`)},
		{"wrong-agent", "/agents/base-agent", "cannot inspect", route(http.StatusOK, `{"name":"other","versions":{"latest":{"version":"3"}}}`)},
		{"missing-version", "/versions/2", "cannot inspect", route(http.StatusNotFound, "{}")},
		{"forbidden-version", "/versions/2", "403", route(http.StatusForbidden, "{}")},
		{"invalid-json", "/versions/2", "parse", route(http.StatusOK, "{")},
		{"wrong-version", "/versions/2", "cannot inspect", route(http.StatusOK, `{"name":"base-agent","version":"3","definition":{}}`)},
		{"wrong-version-agent", "/versions/2", "cannot inspect", route(http.StatusOK, `{"name":"other","version":"2","definition":{}}`)},
		{"missing-definition", "/versions/2", "cannot inspect", route(http.StatusOK, `{"name":"base-agent","version":"2"}`)},
		{"draft", "/versions/2", "cannot inspect", route(http.StatusOK, `{"name":"base-agent","version":"2","draft":true,"definition":{}}`)},
		{"null-skills", "/versions/2", "native Prompt Skills", route(http.StatusOK, `{"version":"2","definition":{"skills":null}}`)},
		{"object-skills", "/versions/2", "native Prompt Skills", route(http.StatusOK, `{"version":"2","definition":{"skills":{}}}`)},
		{"unpinned-skills", "/versions/2", "native Prompt Skills", route(http.StatusOK, `{"version":"2","definition":{"skills":[{"name":"greeting","version":"latest"}]}}`)},
		{"unknown-skill-fields", "/versions/2", "native Prompt Skills", route(http.StatusOK, `{"version":"2","definition":{"skills":[{"name":"greeting","version":"1","extra":true}]}}`)},
		{"duplicate-skills", "/versions/2", "native Prompt Skills", route(http.StatusOK, `{"version":"2","definition":{"skills":[{"name":"greeting","version":"1"},{"name":"greeting","version":"2"}]}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := writeManifest(t, nativePromptManifest("  skills: []\n"))
			client := nativeSmokeHTTP(t, nativeSmokeVersion("3", false),
				[]foundry.FixedRatioVersionSelectionRule{foundry.NewFixedRatioVersionSelectionRule("2", 100)})
			client.routes[test.path] = test.response
			stubCredentialAndHTTP(t, client)
			run := runCLI(t, "", "smoke", "-f", manifest, "--accept-preview", "--experimental-native-skills")
			if run.code == 0 || !strings.Contains(run.stderr, test.want) {
				t.Fatalf("expected %q: %#v", test.want, run)
			}
			requireNativeReadOnly(t, client)
		})
	}
}
