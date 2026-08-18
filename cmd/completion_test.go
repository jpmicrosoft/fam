package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestHelpTopicCompletionOffersMatchingCommands(t *testing.T) {
	run := runCLI(t, "", "__complete", "help", "hos")
	candidates, directive := parseCompletionResult(t, run)

	if !candidates["hosted"] {
		t.Errorf("help topic completion missing hosted namespace:\n%s", run.stdout)
	}
	if candidates["quickstart"] {
		t.Errorf("help topic completion ignored prefix filtering:\n%s", run.stdout)
	}
	requireNoFileCompletion(t, directive)

	nested := runCLI(t, "", "__complete", "help", "hosted", "d")
	nestedCandidates, nestedDirective := parseCompletionResult(t, nested)
	for _, expected := range []string{"delete", "deploy", "diagnose", "diff", "disable", "draft"} {
		if !nestedCandidates[expected] {
			t.Errorf("nested help completion missing %q:\n%s", expected, nested.stdout)
		}
	}
	requireNoFileCompletion(t, nestedDirective)
}

func TestKnownFlagValuesHaveStaticCompletions(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected []string
	}{
		{
			name:     "output",
			args:     []string{"__complete", "quickstart", "--output", ""},
			expected: []string{"text", "json", "yaml"},
		},
		{
			name:     "cloud",
			args:     []string{"__complete", "quickstart", "--cloud", ""},
			expected: []string{"AzureCloud"},
		},
		{
			name:     "quickstart type",
			args:     []string{"__complete", "quickstart", "--type", ""},
			expected: []string{"prompt", "hosted"},
		},
		{
			name:     "hosted protocol",
			args:     []string{"__complete", "hosted", "smoke", "--protocol", ""},
			expected: []string{"responses", "invocations"},
		},
		{
			name:     "memory kind",
			args:     []string{"__complete", "memory", "item", "create", "--kind", ""},
			expected: []string{"user_profile", "chat_summary", "procedural"},
		},
		{
			name:     "Agent 365 integration enabled",
			args:     []string{"__complete", "agent365", "integration", "set", "--enabled="},
			expected: []string{"true", "false"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := runCLI(t, "", tt.args...)
			candidates, directive := parseCompletionResult(t, run)
			for _, expected := range tt.expected {
				if !candidates[expected] {
					t.Errorf("completion missing %q:\n%s", expected, run.stdout)
				}
			}
			requireNoFileCompletion(t, directive)
		})
	}
}

func TestNoArgumentCommandsSuppressFilenameCompletion(t *testing.T) {
	run := runCLI(t, "", "__complete", "quickstart", "")
	_, directive := parseCompletionResult(t, run)
	requireNoFileCompletion(t, directive)
}

func TestPathFlagsHaveCompletionAnnotations(t *testing.T) {
	root := rootCmd()
	tests := []struct {
		command    []string
		flag       string
		annotation string
		values     []string
	}{
		{[]string{"prompt", "validate"}, "manifest", cobra.BashCompFilenameExt, []string{"yaml", "yml", "json"}},
		{[]string{"prompt", "m365", "publish"}, "publication", cobra.BashCompFilenameExt, []string{"yaml", "yml", "json"}},
		{[]string{"prompt", "deploy"}, "structured-inputs-file", cobra.BashCompFilenameExt, []string{"json"}},
		{[]string{"doctor"}, "workspace", cobra.BashCompSubdirsInDir, nil},
		{[]string{"autopilot", "deploy"}, "work-dir", cobra.BashCompSubdirsInDir, nil},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.command, " ")+" --"+tt.flag, func(t *testing.T) {
			command, _, err := root.Find(tt.command)
			if err != nil {
				t.Fatal(err)
			}
			flag := command.Flags().Lookup(tt.flag)
			if flag == nil {
				t.Fatalf("%s is missing --%s", strings.Join(tt.command, " "), tt.flag)
			}
			values, ok := flag.Annotations[tt.annotation]
			if !ok {
				t.Fatalf("%s --%s is missing completion annotation %q", strings.Join(tt.command, " "), tt.flag, tt.annotation)
			}
			for _, expected := range tt.values {
				if !containsString(values, expected) {
					t.Errorf("%s --%s completion missing %q: %#v", strings.Join(tt.command, " "), tt.flag, expected, values)
				}
			}
		})
	}
}

func parseCompletionResult(
	t *testing.T,
	run cliRun,
) (map[string]bool, cobra.ShellCompDirective) {
	t.Helper()
	if run.code != 0 {
		t.Fatalf("completion exited %d: %s", run.code, run.stderr)
	}

	candidates := map[string]bool{}
	var directive cobra.ShellCompDirective
	for _, line := range strings.Split(strings.TrimSpace(run.stdout), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, ":") {
			value, err := strconv.Atoi(strings.TrimPrefix(line, ":"))
			if err != nil {
				t.Fatalf("invalid completion directive %q", line)
			}
			directive = cobra.ShellCompDirective(value)
			continue
		}
		if index := strings.IndexByte(line, '\t'); index >= 0 {
			line = line[:index]
		}
		if line != "" {
			candidates[line] = true
		}
	}
	return candidates, directive
}

func requireNoFileCompletion(t *testing.T, directive cobra.ShellCompDirective) {
	t.Helper()
	if directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatalf("completion directive %d allows unrelated filename suggestions", directive)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
