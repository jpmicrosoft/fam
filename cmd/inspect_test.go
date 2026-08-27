package main

import (
	"strings"
	"testing"
	"time"

	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestSafeAPIMStatusToleratesHostileOrMissingProperties(t *testing.T) {
	tests := []struct {
		name     string
		state    connection.State
		wantName string
		wantOK   bool
		target   string
		auth     string
	}{
		{
			name:     "absent connection uses the default name",
			state:    connection.State{Exists: false},
			wantName: "apim-agent",
		},
		{
			name: "typed properties are surfaced",
			state: connection.State{
				Exists: true,
				Name:   "apim-remote",
				Properties: map[string]interface{}{
					"target":   "https://contoso.azure-api.net/agents/chat",
					"authType": "ApiKey",
				},
			},
			wantName: "apim-remote",
			wantOK:   true,
			target:   "https://contoso.azure-api.net/agents/chat",
			auth:     "ApiKey",
		},
		{
			name: "non-string properties do not panic",
			state: connection.State{
				Exists:     true,
				Name:       "apim-remote",
				Properties: map[string]interface{}{"target": 42, "authType": []interface{}{"x"}},
			},
			wantName: "apim-remote",
			wantOK:   true,
		},
		{
			name:     "nil properties do not panic",
			state:    connection.State{Exists: true, Name: ""},
			wantName: "apim-agent",
			wantOK:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := safeAPIMStatus(tt.state, "apim-agent")
			if status == nil {
				t.Fatal("status must never be nil")
			}
			if status.Name != tt.wantName || status.Exists != tt.wantOK {
				t.Fatalf("unexpected status: %#v", status)
			}
			if status.Target != tt.target || status.AuthType != tt.auth {
				t.Fatalf("unexpected connection details: %#v", status)
			}
		})
	}
}

func apimSpecForDiff() *config.ApimSpec {
	return &config.ApimSpec{
		Enabled:              true,
		Target:               "https://contoso.azure-api.net/agents/chat",
		Auth:                 "api_key",
		ConnectionAPIVersion: config.DefaultConnectionAPIVersion,
		AllowedSuffixes:      []string{"azure-api.net"},
	}
}

func TestCompareAPIMConnectionDetectsManagedChangesWithoutTouchingCredentials(t *testing.T) {
	apim := apimSpecForDiff()
	desired, err := connection.BuildConnectionBody(apim, []string{"model"}, "the-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	properties, ok := desired["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("unexpected connection body shape")
	}

	t.Run("missing connection is a change", func(t *testing.T) {
		diff, err := compareAPIMConnection(connection.State{Exists: false}, apim, "apim-agent", []string{"model"})
		if err != nil {
			t.Fatal(err)
		}
		if !diff.Changed || len(diff.Fields) != 1 || !strings.Contains(diff.Fields[0], "missing") {
			t.Fatalf("unexpected diff: %#v", diff)
		}
	})

	t.Run("identical managed properties are unchanged", func(t *testing.T) {
		state := connection.State{Exists: true, Name: "apim-agent", Properties: properties}
		diff, err := compareAPIMConnection(state, apim, "apim-agent", []string{"model"})
		if err != nil {
			t.Fatal(err)
		}
		if diff.Changed {
			t.Fatalf("unchanged managed properties must not diff: %#v", diff)
		}
		if diff.Status == nil || diff.Status.AuthType != "ApiKey" {
			t.Fatalf("unexpected status: %#v", diff.Status)
		}
	})

	t.Run("a changed target is reported", func(t *testing.T) {
		changed := map[string]interface{}{}
		for key, value := range properties {
			changed[key] = value
		}
		changed["target"] = "https://other.azure-api.net/agents/chat"
		state := connection.State{Exists: true, Name: "apim-agent", Properties: changed}
		diff, err := compareAPIMConnection(state, apim, "apim-agent", []string{"model"})
		if err != nil {
			t.Fatal(err)
		}
		if !diff.Changed {
			t.Fatalf("a changed target must be reported: %#v", diff)
		}
		for _, field := range diff.Fields {
			if field == "credentials" {
				t.Fatal("the credential must never take part in the comparison")
			}
		}
	})

	t.Run("a credential-only difference is never a change", func(t *testing.T) {
		rotated, err := connection.BuildConnectionBody(apim, []string{"model"}, "a-different-key")
		if err != nil {
			t.Fatal(err)
		}
		rotatedProperties, ok := rotated["properties"].(map[string]interface{})
		if !ok {
			t.Fatal("unexpected connection body shape")
		}
		state := connection.State{Exists: true, Name: "apim-agent", Properties: rotatedProperties}
		diff, err := compareAPIMConnection(state, apim, "apim-agent", []string{"model"})
		if err != nil {
			t.Fatal(err)
		}
		if diff.Changed {
			t.Fatalf("a rotated key alone must not diff managed properties: %#v", diff)
		}
	})
}

func TestNormalizeJSONMapCanonicalizesNumbersAndNestedValues(t *testing.T) {
	got := normalizeJSONMap(map[string]interface{}{
		"isSharedToAll": false,
		"count":         1,
		"metadata":      map[string]string{"deploymentInPath": "false"},
	})
	if got["isSharedToAll"] != false {
		t.Fatalf("unexpected boolean normalization: %#v", got)
	}
	if number, ok := got["count"].(float64); !ok || number != 1 {
		t.Fatalf("unexpected numeric normalization: %#v", got["count"])
	}
	metadata, ok := got["metadata"].(map[string]interface{})
	if !ok || metadata["deploymentInPath"] != "false" {
		t.Fatalf("unexpected nested normalization: %#v", got["metadata"])
	}
	if len(normalizeJSONMap(nil)) != 0 {
		t.Fatal("a nil map must normalize to an empty map")
	}
}

func TestDestinationHostNeverEchoesTheFullDestination(t *testing.T) {
	tests := map[string]string{
		"https://contoso.azure-api.net/agents/chat?key=abc": "contoso.azure-api.net",
		"https://api.contoso.com:8443/orders":               "api.contoso.com:8443",
		"not a url":                                         "<invalid>",
		"":                                                  "<invalid>",
		"/relative":                                         "<invalid>",
		"https://":                                          "<invalid>",
	}
	for rawURL, want := range tests {
		if got := destinationHost(rawURL); got != want {
			t.Fatalf("destinationHost(%q) = %q, want %q", rawURL, got, want)
		}
	}
}

func TestPreflightTextRendersEveryCheckWithoutTrailingBlankLines(t *testing.T) {
	text := preflightText(preflightResult{
		Ready:           true,
		Cloud:           "AzureCloud",
		Agent:           "sample-agent",
		ProjectEndpoint: "https://acct.services.ai.azure.com/api/projects/p",
		Checks: []preflightCheck{
			{Name: "manifest", Status: "passed", Details: "schema is valid"},
			{Name: "destination-approval", Status: "passed", Details: "1 operator-approved destination(s)"},
			{Name: "model-reference", Status: "passed", Details: "deployment exists"},
		},
	})
	for _, want := range []string{
		"preflight ready: agent=sample-agent cloud=AzureCloud",
		"project: https://acct.services.ai.azure.com/api/projects/p",
		"manifest:",
		"destination-approval:",
		"model-reference:",
		"passed",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("preflight text is missing %q:\n%s", want, text)
		}
	}
	if strings.HasSuffix(text, "\n") {
		t.Fatalf("preflight text must not end with a newline: %q", text)
	}
	if strings.Contains(preflightText(preflightResult{Ready: true, Agent: "a", Cloud: "c"}), "project:") {
		t.Fatal("an unresolved endpoint must not print an empty project line")
	}
}

func TestProjectWaitDurationsRejectNonFiniteAndOutOfRangeValues(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	valid := map[string]string{"project-wait-timeout": "90", "project-wait-interval": "2.5"}
	timeout, interval, err := projectWaitDurations(commandWithFlags(t, "deploy", manifest, valid))
	if err != nil {
		t.Fatal(err)
	}
	if timeout != 90*time.Second || interval != 2500*time.Millisecond {
		t.Fatalf("unexpected durations: %v / %v", timeout, interval)
	}
	for _, flags := range []map[string]string{
		{"project-wait-timeout": "0"},
		{"project-wait-timeout": "-1"},
		{"project-wait-timeout": "NaN"},
		{"project-wait-timeout": "+Inf"},
		{"project-wait-timeout": "1e30"},
		{"project-wait-interval": "-0.5"},
		{"project-wait-interval": "NaN"},
		{"project-wait-interval": "1e30"},
	} {
		if _, _, err := projectWaitDurations(commandWithFlags(t, "deploy", manifest, flags)); err == nil {
			t.Fatalf("expected %v to be rejected", flags)
		}
	}
}

func TestLifecycleTextHelpersAreExplicitAboutAbsentValues(t *testing.T) {
	if emptyValue("") != "<none>" || emptyValue("7") != "7" {
		t.Fatal("unexpected empty-value rendering")
	}
	if apimConfirmationSuffix("") != "" {
		t.Fatal("an absent connection must not appear in the confirmation prompt")
	}
	if got := apimConfirmationSuffix("apim-agent"); !strings.Contains(got, "apim-agent") {
		t.Fatalf("unexpected confirmation suffix: %q", got)
	}
	if existingConnectionName(connection.State{Exists: false}, "apim-agent") != "" {
		t.Fatal("a missing connection must not be named as a deletion target")
	}
	if existingConnectionName(connection.State{Exists: true}, "apim-agent") != "apim-agent" {
		t.Fatal("an existing connection must be named as a deletion target")
	}
	if !strings.Contains(projectAction(true, "https://x/api/projects/p"), "created") {
		t.Fatal("project creation must be reported")
	}
	if !strings.Contains(projectAction(false, "https://x/api/projects/p"), "already existed") {
		t.Fatal("an existing project must not be reported as created")
	}
}

func TestBuildMetadataIncludesOptionalBuildStamps(t *testing.T) {
	oldCommit, oldDate := config.BuildCommit, config.BuildDate
	t.Cleanup(func() { config.BuildCommit, config.BuildDate = oldCommit, oldDate })

	config.BuildCommit, config.BuildDate = "", ""
	bare := buildMetadata()
	if bare != "fam "+config.Version {
		t.Fatalf("unexpected bare metadata: %q", bare)
	}
	config.BuildCommit, config.BuildDate = "abc1234", "2026-08-03T12:00:00Z"
	stamped := buildMetadata()
	for _, want := range []string{config.Version, "commit=abc1234", "built=2026-08-03T12:00:00Z"} {
		if !strings.Contains(stamped, want) {
			t.Fatalf("metadata %q is missing %q", stamped, want)
		}
	}
}

func TestPrimeOutputFlagSelectsTheErrorRendererBeforeParsing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "long form", args: []string{"validate", "--output", "json"}, want: "json"},
		{name: "long attached", args: []string{"validate", "--output=yaml"}, want: "yaml"},
		{name: "short form", args: []string{"validate", "-o", "json"}, want: "json"},
		{name: "short attached", args: []string{"validate", "-ojson"}, want: "json"},
		{name: "short equals", args: []string{"validate", "-o=yaml"}, want: "yaml"},
		{name: "last wins", args: []string{"validate", "-o", "yaml", "--output", "json"}, want: "json"},
		{name: "after terminator is ignored", args: []string{"validate", "--", "--output", "json"}, want: "text"},
		{name: "dangling value is ignored", args: []string{"validate", "--output"}, want: "text"},
		{name: "absent", args: []string{"validate"}, want: "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := rootCmd()
			primeOutputFlag(root, tt.args)
			got, err := root.PersistentFlags().GetString("output")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("primeOutputFlag(%v) selected %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFlagLookupFallsBackToInheritedAndPersistentFlags(t *testing.T) {
	root := rootCmd()
	command, _, err := root.Find([]string{"deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if set := flagSet(command, "manifest"); set.Lookup("manifest") == nil {
		t.Fatal("a local flag must resolve on the command itself")
	}
	if set := flagSet(command, "output"); set.Lookup("output") == nil {
		t.Fatal("a persistent root flag must resolve from a subcommand")
	}
	if set := flagSet(command, "does-not-exist"); set.Lookup("does-not-exist") != nil {
		t.Fatal("an unknown flag must not resolve")
	}
	if getFlag(command, "does-not-exist") != "" ||
		getBoolFlag(command, "does-not-exist") ||
		getIntFlag(command, "does-not-exist") != 0 ||
		getFloatFlag(command, "does-not-exist") != 0 ||
		getDurationFlag(command, "does-not-exist") != 0 ||
		getStringArrayFlag(command, "does-not-exist") != nil {
		t.Fatal("unknown flags must read as zero values instead of panicking")
	}
}

func TestCommandContextFallsBackToBackground(t *testing.T) {
	if commandContext(&cobra.Command{}) == nil {
		t.Fatal("a command without a context must still get one")
	}
}

func TestVerboseDiagnosticsGoToStderrOnlyWhenRequested(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	quiet := commandWithFlags(t, "deploy", manifest, nil)
	var quietOut strings.Builder
	quiet.SetErr(&quietOut)
	verbosef(quiet, "ensuring project %s", "p")
	if quietOut.Len() != 0 {
		t.Fatalf("diagnostics must be silent without --verbose: %q", quietOut.String())
	}

	loud := commandWithFlags(t, "deploy", manifest, map[string]string{"verbose": "true"})
	var loudOut strings.Builder
	loud.SetErr(&loudOut)
	verbosef(loud, "ensuring project %s", "p")
	if !strings.Contains(loudOut.String(), "ensuring project p") {
		t.Fatalf("--verbose must write diagnostics to stderr: %q", loudOut.String())
	}

	debug := commandWithFlags(t, "deploy", manifest, map[string]string{"debug": "true"})
	var debugOut strings.Builder
	debug.SetErr(&debugOut)
	verbosef(debug, "debug implies verbose")
	debugf(debug, "safe detail")
	if !strings.Contains(debugOut.String(), "debug implies verbose") ||
		!strings.Contains(debugOut.String(), "debug: safe detail") {
		t.Fatalf("--debug must enable verbose and debug diagnostics: %q", debugOut.String())
	}
}

// primeOutputFlag accepts any type exposing PersistentFlags, so assert the root
// command keeps satisfying that contract.
var _ interface{ PersistentFlags() *pflag.FlagSet } = (*cobra.Command)(nil)
