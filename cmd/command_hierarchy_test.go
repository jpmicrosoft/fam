package main

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestEveryFlatCommandMapsToEquivalentCanonicalCommand(t *testing.T) {
	root := rootCmd()
	for _, route := range commandRoutes {
		route := route
		t.Run(route.Legacy, func(t *testing.T) {
			legacy, remaining, err := root.Find([]string{route.Legacy})
			if err != nil || len(remaining) != 0 {
				t.Fatalf("flat compatibility command is missing: %v, remaining=%#v", err, remaining)
			}
			if !legacy.Hidden {
				t.Fatal("flat compatibility command must be hidden from discovery")
			}

			canonical, remaining, err := root.Find(route.Path)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("canonical command is missing: %v, remaining=%#v", err, remaining)
			}
			if canonical.Hidden {
				t.Fatal("canonical command must remain discoverable")
			}
			if got := legacyCommandName(canonical); got != route.Legacy {
				t.Fatalf("canonical command lost legacy identity: %q", got)
			}
			if legacy.Short != canonical.Short {
				t.Fatalf("short descriptions differ: %q != %q", legacy.Short, canonical.Short)
			}
			if (legacy.RunE == nil) != (canonical.RunE == nil) ||
				(legacy.PreRunE == nil) != (canonical.PreRunE == nil) {
				t.Fatal("canonical and compatibility commands do not share execution hooks")
			}

			legacyFlags := commandFlagContracts(legacy.Flags())
			canonicalFlags := commandFlagContracts(canonical.Flags())
			if len(legacyFlags) != len(canonicalFlags) {
				t.Fatalf("flag counts differ: %d != %d", len(legacyFlags), len(canonicalFlags))
			}
			for name, contract := range canonicalFlags {
				if legacyFlags[name] != contract {
					t.Errorf("flag --%s differs: %#v != %#v", name, legacyFlags[name], contract)
				}
			}

			canonicalInvocation := "fam " + strings.Join(route.Path, " ")
			if !strings.Contains(canonical.Example, canonicalInvocation) {
				t.Fatalf("canonical examples do not use %q:\n%s", canonicalInvocation, canonical.Example)
			}
			if !strings.Contains(legacy.Example, "fam "+route.Legacy) {
				t.Fatalf("compatibility examples do not use the flat command:\n%s", legacy.Example)
			}
		})
	}
}

func TestCanonicalAndCompatibilityCommandsExecuteIdentically(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	canonical := runCLI(t, "", "prompt", "plan", "-f", manifest)
	legacy := runCLI(t, "", "plan", "-f", manifest)
	if canonical.code != legacy.code ||
		canonical.stdout != legacy.stdout ||
		canonical.stderr != legacy.stderr {
		t.Fatalf(
			"canonical and compatibility execution differ:\ncanonical=%#v\nlegacy=%#v",
			canonical,
			legacy,
		)
	}
}

func TestRootDiscoveryShowsNamespacesAndHidesFlatCommands(t *testing.T) {
	help := runCLI(t, "", "--help")
	if help.code != 0 {
		t.Fatalf("root help failed: %s", help.stderr)
	}
	for _, namespace := range []string{
		"prompt",
		"hosted",
		"project",
		"connector",
		"toolbox",
		"skill",
		"grounding",
		"memory",
		"agent365",
		"autopilot",
	} {
		if !strings.Contains(help.stdout, namespace) {
			t.Errorf("root help omitted namespace %q:\n%s", namespace, help.stdout)
		}
	}
	for _, legacy := range []string{"hosted-deploy", "project-create", "memory-item-list"} {
		if strings.Contains(help.stdout, legacy) {
			t.Errorf("root help exposed compatibility command %q:\n%s", legacy, help.stdout)
		}
	}

	completion := runCLI(t, "", "__complete", "")
	candidates, _ := parseCompletionResult(t, completion)
	if !candidates["hosted"] || !candidates["prompt"] || !candidates["agent365"] {
		t.Fatalf("root completion omitted canonical namespaces:\n%s", completion.stdout)
	}
	if candidates["hosted-deploy"] || candidates["memory-item-list"] {
		t.Fatalf("root completion exposed flat compatibility aliases:\n%s", completion.stdout)
	}
}

type flagContract struct {
	Type      string
	Default   string
	Shorthand string
	Usage     string
}

func commandFlagContracts(flags *pflag.FlagSet) map[string]flagContract {
	contracts := map[string]flagContract{}
	flags.VisitAll(func(flag *pflag.Flag) {
		contracts[flag.Name] = flagContract{
			Type:      flag.Value.Type(),
			Default:   flag.DefValue,
			Shorthand: flag.Shorthand,
			Usage:     flag.Usage,
		}
	})
	return contracts
}
