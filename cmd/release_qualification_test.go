package main

// This file is a self-updating release-qualification suite: it dynamically
// walks the real rootCmd() tree instead of trusting any manually maintained
// command list, so release confidence for the whole CLI surface does not
// depend on someone remembering to update a list when a command or flag is
// added, renamed, or removed. Every test here either:
//
//   - Exercises the real, unmodified entrypoints (execute()/runCLI for
//     --help and shell completions; requireFlags-driven PreRunE for the
//     custom required-flag convention), or
//   - Uses cobra/pflag's own ParseFlags (never Execute or RunE) to prove a
//     flag or shorthand is structurally usable without ever running an
//     online or destructive command as a side effect.
//
// Coverage is structural/syntactic by design: it proves every command and
// flag is wired correctly and safely parseable, not that a command's
// business logic is correct (that is the job of the rest of the package's
// tests, which exercise cmdXxx functions against fakes).
//
// All tests here are deterministic (fixed representative values, sorted
// iteration order via CommandPath()/pflag's own lexicographic VisitAll),
// side-effect free (no filesystem, network, or environment access), and
// parallel-safe (every subtest builds its own fresh rootCmd() before
// mutating anything; nothing package-level is ever mutated).

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"foundry-agent-manager/internal/trust"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// isScaffoldingCommand reports whether cmd is one of Cobra's own
// automatically generated commands. A freshly constructed rootCmd() never
// has these (they are added lazily inside ExecuteC()), but qualifiedCommands
// filters them defensively so this suite keeps working correctly even if
// that Cobra implementation detail ever changes.
func isScaffoldingCommand(cmd *cobra.Command) bool {
	switch cmd.Name() {
	case "help", "completion", "__complete", "__completeNoDesc":
		return true
	default:
		return false
	}
}

// qualifiedCommands recursively discovers every non-scaffolding command
// reachable from root, sorted by CommandPath() for deterministic test
// ordering and naming. This is the single source of truth every test in this
// file uses instead of a manually maintained command list.
func qualifiedCommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			if isScaffoldingCommand(child) {
				continue
			}
			out = append(out, child)
			walk(child)
		}
	}
	walk(root)
	sort.Slice(out, func(i, j int) bool {
		return out[i].CommandPath() < out[j].CommandPath()
	})
	return out
}

// assertCommandMetadata implements the metadata half of item 1: every
// command must document itself (Use/Short) and either do something itself
// or exist purely to group subcommands.
func assertCommandMetadata(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	if strings.TrimSpace(cmd.Use) == "" {
		t.Errorf("%s: has an empty Use", cmd.CommandPath())
	}
	if strings.TrimSpace(cmd.Short) == "" {
		t.Errorf("%s: has an empty Short description", cmd.CommandPath())
	}
	if !cmd.Runnable() && !cmd.HasSubCommands() {
		t.Errorf("%s: has neither Run/RunE nor subcommands and can never do anything", cmd.CommandPath())
	}
}

// TestRootCommandMetadataIsSound covers the root command itself, which
// qualifiedCommands intentionally excludes (it enumerates children only).
func TestRootCommandMetadataIsSound(t *testing.T) {
	t.Parallel()
	assertCommandMetadata(t, rootCmd())
}

// TestNoDuplicateCommandPaths guards the "no duplicate command path" clause
// of item 1 across the entire dynamically discovered tree.
func TestNoDuplicateCommandPaths(t *testing.T) {
	t.Parallel()
	seen := map[string]int{}
	for _, cmd := range qualifiedCommands(rootCmd()) {
		seen[cmd.CommandPath()]++
	}
	if len(seen) == 0 {
		t.Fatal("no commands were discovered from rootCmd(); the command tree may be broken")
	}
	for path, count := range seen {
		if count > 1 {
			t.Errorf("command path %q is registered %d times", path, count)
		}
	}
}

// flagMentionedInHelp reports whether --name appears in help text as a real
// flag token, not merely as a substring of a longer flag name (for example
// --connection must not spuriously match inside --connection-api-version).
func flagMentionedInHelp(helpText, name string) bool {
	re := regexp.MustCompile(`(?m)--` + regexp.QuoteMeta(name) + `([\s,=]|$)`)
	return re.MatchString(helpText)
}

// representativeFlagValue returns a safe, deterministic string that parses
// successfully for the given flag's declared pflag type. Every type actually
// registered anywhere in this CLI must be listed here (bool, string,
// stringArray, stringSlice, int, int64, duration, float64); uint/uint64 are
// included defensively even though nothing currently registers one. Any
// other type fails the test loudly instead of being silently skipped, so a
// future flag of a new type cannot ship without qualification-test support.
func representativeFlagValue(t *testing.T, cmdName string, f *pflag.Flag) string {
	t.Helper()
	switch f.Value.Type() {
	case "bool":
		return "true"
	case "string", "stringArray", "stringSlice":
		return "qualification-value"
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return "7"
	case "float32", "float64":
		return "1.5"
	case "duration":
		return "1500ms"
	default:
		t.Fatalf(
			"command %q flag --%s uses unsupported pflag type %q; add explicit qualification-test "+
				"support (representativeFlagValue and isFalsyDefault) before shipping it",
			cmdName, f.Name, f.Value.Type(),
		)
		return ""
	}
}

// isFalsyDefault reports whether a flag's default is the zero-ish value for
// its declared type, used to catch required flags with misleading non-empty
// defaults (item 4).
func isFalsyDefault(f *pflag.Flag) bool {
	switch f.Value.Type() {
	case "bool":
		return f.DefValue == "" || f.DefValue == "false"
	case "string", "stringArray", "stringSlice":
		return f.DefValue == "" || f.DefValue == "[]"
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64":
		return f.DefValue == "" || f.DefValue == "0"
	case "duration":
		return f.DefValue == "" || f.DefValue == "0s"
	default:
		return f.DefValue == ""
	}
}

// assertFlagsAreSound implements item 2 and half of item 3: every flag
// visible to cmd once merged by ParseFlags (local, inherited, and
// persistent) must be documented in --help, carry real usage text, have a
// default that round-trips through its own Value.Set, and accept a safe
// representative value of its declared pflag type. It uses ParseFlags only,
// never Execute/RunE, so no online or destructive command can ever run.
func assertFlagsAreSound(t *testing.T, cmdName string, cmd *cobra.Command, helpText string) {
	t.Helper()
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		t.Run("--"+f.Name, func(t *testing.T) {
			if strings.TrimSpace(f.Usage) == "" {
				t.Errorf("--%s has empty usage text", f.Name)
			}
			if !flagMentionedInHelp(helpText, f.Name) {
				t.Errorf("--%s does not appear in %q --help output", f.Name, cmdName)
			}
			if err := f.Value.Set(f.DefValue); err != nil {
				t.Errorf("--%s default %q does not round-trip through its own Value.Set: %v", f.Name, f.DefValue, err)
			}
			value := representativeFlagValue(t, cmdName, f)
			if err := cmd.ParseFlags([]string{"--" + f.Name + "=" + value}); err != nil {
				t.Errorf("--%s failed to parse representative value %q: %v", f.Name, value, err)
			}
		})
	})
}

// assertShorthandsAreSound implements item 3: every shorthand visible to cmd
// must be unique within its effective flag set. pflag itself panics if
// AddFlag ever merges two flags sharing a shorthand, so the merge above is
// wrapped in recover(); this is defense in depth via an explicit map, plus a
// parse check for every shorthand found.
func assertShorthandsAreSound(t *testing.T, cmdName string, cmd *cobra.Command) {
	t.Helper()
	byShorthand := map[string][]string{}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Shorthand == "" {
			return
		}
		byShorthand[f.Shorthand] = append(byShorthand[f.Shorthand], f.Name)
	})
	for shorthand, names := range byShorthand {
		if len(names) > 1 {
			t.Errorf("shorthand -%s is shared by multiple flags: %s", shorthand, strings.Join(names, ", "))
		}
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Shorthand == "" {
			return
		}
		t.Run("-"+f.Shorthand, func(t *testing.T) {
			value := representativeFlagValue(t, cmdName, f)
			if err := cmd.ParseFlags([]string{"-" + f.Shorthand + "=" + value}); err != nil {
				t.Errorf("shorthand -%s failed to parse representative value %q: %v", f.Shorthand, value, err)
			}
		})
	})
}

// TestDiscoveredCommandsAreStructurallySound is the primary release-
// qualification test. It walks every command qualifiedCommands discovers and
// proves (items 1-3):
//   - Use/Short are non-empty and the command is runnable or a group.
//   - `<command> --help` succeeds through the real execute() entrypoint used
//     everywhere else in this package (cli_test.go's runCLI), proving --help
//     is genuinely wired end-to-end.
//   - Every flag visible to the command is documented, has a valid default,
//     and accepts a safe representative value, using ParseFlags only.
//   - Every shorthand is unique and parses successfully.
func TestDiscoveredCommandsAreStructurallySound(t *testing.T) {
	t.Parallel()
	commands := qualifiedCommands(rootCmd())
	if len(commands) == 0 {
		t.Fatal("rootCmd() produced no commands; the command tree may be broken")
	}
	for _, cmd := range commands {
		cmd := cmd
		path := cmd.CommandPath()
		args := commandPathArgs(cmd)
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			assertCommandMetadata(t, cmd)

			help := runCLI(t, "", append(args, "--help")...)
			if help.code != 0 {
				t.Fatalf("%s --help exited %d: %s", path, help.code, help.stderr)
			}
			if !strings.Contains(help.stdout, "Usage:") || !strings.Contains(help.stdout, path) {
				t.Fatalf("%s --help did not print the expected usage banner:\n%s", path, help.stdout)
			}

			fresh := rootCmd()
			freshCmd, remaining, err := fresh.Find(args)
			if err != nil {
				t.Fatalf("%s: not found on a freshly constructed root: %v", path, err)
			}
			if len(remaining) != 0 {
				t.Fatalf("%s: command path was not fully resolved: %#v", path, remaining)
			}
			freshCmd.InitDefaultHelpFlag()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s: merging persistent flags panicked (likely a duplicate shorthand): %v", path, r)
					}
				}()
				if err := freshCmd.ParseFlags(nil); err != nil {
					t.Fatalf("%s: unexpected error parsing zero arguments: %v", path, err)
				}
			}()

			assertFlagsAreSound(t, path, freshCmd, help.stdout)
			assertShorthandsAreSound(t, path, freshCmd)
		})
	}
}

// requiredFlagPattern matches this repository's custom required-flag
// convention: requireFlags in root.go returns exactly
// errs.Config("--%s is required", name), whose Error() (internal/errors.go)
// is unwrapped, so the message is exactly "--name is required".
var requiredFlagPattern = regexp.MustCompile(`^--([A-Za-z0-9][A-Za-z0-9-]*) is required$`)

const maxDiscoverableRequiredFlags = 25

// discoverRequiredFlagNames reconstructs, without any hardcoded flag names,
// the ordered set of flags this repository's custom requireFlags convention
// enforces for one command (this codebase does not use Cobra's built-in
// MarkFlagRequired; see requireFlags in root.go). It repeatedly builds a
// fresh root, supplies every previously discovered value, and calls the
// command's PreRunE directly -- never Execute, RunE, or the root's
// PersistentPreRunE -- so no online or destructive command ever runs and no
// unrelated root-level validation (cloud/timeout/retry) interferes.
func discoverRequiredFlagNames(t *testing.T, path []string) []string {
	t.Helper()
	name := strings.Join(path, " ")
	var discovered []string
	seen := map[string]bool{}
	for i := 0; i < maxDiscoverableRequiredFlags; i++ {
		root := rootCmd()
		cmd, remaining, err := root.Find(path)
		if err != nil {
			t.Fatalf("%s: not found while discovering required flags: %v", name, err)
		}
		if len(remaining) != 0 {
			t.Fatalf("%s: command path was not fully resolved: %#v", name, remaining)
		}
		if cmd.PreRunE == nil {
			break
		}
		for _, discoveredName := range discovered {
			if err := cmd.Flags().Set(discoveredName, "qualification-value"); err != nil {
				t.Fatalf("%s: could not pre-set already-discovered required flag --%s: %v", name, discoveredName, err)
			}
		}
		err = cmd.PreRunE(cmd, nil)
		if err == nil {
			break
		}
		match := requiredFlagPattern.FindStringSubmatch(err.Error())
		if match == nil {
			t.Fatalf(
				"%s: PreRunE returned an error that does not match the \"--flag is required\" convention "+
					"used by requireFlags in root.go; update discoverRequiredFlagNames if the convention changed: %v",
				name, err,
			)
		}
		flagName := match[1]
		if seen[flagName] {
			t.Fatalf("%s: required-flag discovery looped on --%s without making progress", name, flagName)
		}
		seen[flagName] = true

		lookup := cmd.Flags().Lookup(flagName)
		if lookup == nil {
			t.Fatalf(
				"%s: requireFlags(...) references --%s, but no such flag is registered on this command "+
					"(likely a typo in a requireFlags call in root.go)",
				name, flagName,
			)
		}
		if lookup.Value.Type() != "string" {
			t.Fatalf(
				"%s: required flag --%s has pflag type %q, but getFlag (runtime.go) only ever reads required "+
					"flags via GetString; a non-string required flag could never be satisfied at runtime",
				name, flagName, lookup.Value.Type(),
			)
		}
		if lookup.DefValue != "" {
			t.Errorf(
				"%s: required flag --%s has a misleading non-empty default %q",
				name, flagName, lookup.DefValue,
			)
		}

		discovered = append(discovered, flagName)
	}
	return discovered
}

// TestCustomRequiredFlagsAreDiscoverableAndSane implements item 4 for this
// repository's actual required-flag mechanism (requireFlags/getFlag in
// root.go/runtime.go), which predates and stands in for Cobra's own
// annotation-based required flags here.
func TestCustomRequiredFlagsAreDiscoverableAndSane(t *testing.T) {
	t.Parallel()
	for _, cmd := range qualifiedCommands(rootCmd()) {
		path := commandPathArgs(cmd)
		t.Run(cmd.CommandPath(), func(t *testing.T) {
			t.Parallel()
			discoverRequiredFlagNames(t, path)
		})
	}
}

// TestCobraRequiredFlagAnnotationsAreSane implements item 4's literal ask
// about Cobra's own required-flag annotation mechanism
// (cobra.BashCompOneRequiredFlag, set by MarkFlagRequired /
// MarkPersistentFlagRequired). No command in this repository uses it today
// (requireFlags in root.go is the convention that replaces it), so this test
// is intentionally vacuous right now; it exists so that the day any command
// adopts the built-in mechanism, its annotation is verified against the same
// rules as the custom convention above, without anyone having to remember to
// add coverage.
func TestCobraRequiredFlagAnnotationsAreSane(t *testing.T) {
	t.Parallel()
	for _, cmd := range qualifiedCommands(rootCmd()) {
		cmd := cmd
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			values, ok := f.Annotations[cobra.BashCompOneRequiredFlag]
			if !ok || len(values) == 0 || values[0] != "true" {
				return
			}
			if cmd.Flags().Lookup(f.Name) == nil {
				t.Errorf(
					"%s: --%s carries a required annotation but is not a resolvable local flag",
					cmd.CommandPath(), f.Name,
				)
			}
			if !isFalsyDefault(f) {
				t.Errorf(
					"%s: required flag --%s has a misleading non-empty default %q",
					cmd.CommandPath(), f.Name, f.DefValue,
				)
			}
		})
	}
}

// TestShellCompletionsCoverEveryDiscoveredCommand implements item 5. The
// generated bash/zsh/fish/powershell scripts are non-empty, but (verified by
// building and inspecting the real CLI binary) they do not literally embed
// command names: they all dynamically dispatch through Cobra's hidden
// __complete command at shell-completion time. So instead of brittle text
// scanning of generated scripts, this test asserts the equivalent, robust
// property they all actually rely on: `__complete` offers every visible
// immediate child at every namespace level.
func TestShellCompletionsCoverEveryDiscoveredCommand(t *testing.T) {
	t.Parallel()
	root := rootCmd()
	discovered := qualifiedCommands(root)
	if len(discovered) == 0 {
		t.Fatal("no commands were discovered; cannot validate completion coverage")
	}

	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		shell := shell
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			run := runCLI(t, "", "completion", shell)
			if run.code != 0 {
				t.Fatalf("completion %s exited %d: %s", shell, run.code, run.stderr)
			}
			if strings.TrimSpace(run.stdout) == "" {
				t.Fatalf("completion %s produced empty output", shell)
			}
		})
	}

	parents := []*cobra.Command{root}
	for _, command := range discovered {
		if command.HasSubCommands() {
			parents = append(parents, command)
		}
	}
	for _, parent := range parents {
		parent := parent
		t.Run(parent.CommandPath(), func(t *testing.T) {
			args := append([]string{"__complete"}, commandPathArgs(parent)...)
			args = append(args, "")
			complete := runCLI(t, "", args...)
			if complete.code != 0 {
				t.Fatalf("%s completion exited %d: %s", parent.CommandPath(), complete.code, complete.stderr)
			}

			offered := map[string]bool{}
			for _, line := range strings.Split(complete.stdout, "\n") {
				line = strings.TrimRight(line, "\r")
				if line == "" || strings.HasPrefix(line, ":") {
					continue
				}
				name := line
				if idx := strings.IndexByte(line, '\t'); idx >= 0 {
					name = line[:idx]
				}
				offered[name] = true
			}

			for _, child := range parent.Commands() {
				if !child.IsAvailableCommand() {
					continue
				}
				if !offered[child.Name()] {
					t.Errorf(
						"dynamic completion for %q does not offer visible child %q",
						parent.CommandPath(),
						child.Name(),
					)
				}
			}
		})
	}
}

// isManifestCommand reports whether cmd was built by newManifestCommand: it
// structurally requires a "manifest" flag with the "f" shorthand, matching
// that constructor's exact wiring rather than guessing from the command
// name.
func isManifestCommand(cmd *cobra.Command) bool {
	manifestFlag := cmd.Flags().Lookup("manifest")
	return manifestFlag != nil && manifestFlag.Shorthand == "f"
}

// isTrustEnforcingCommand reports whether cmd registers all three trust
// approval flags added by addTrustFlags (trust.go): a command that has some
// but not all of them would be a bug in addTrustFlags's own wiring, which
// always adds all three together.
func isTrustEnforcingCommand(cmd *cobra.Command) bool {
	for _, name := range []string{trust.FlagAPIMHost, trust.FlagToolHost, trust.FlagAudience} {
		if cmd.Flags().Lookup(name) == nil {
			return false
		}
	}
	return true
}

// manifestCommandParityExceptions lists commands intentionally present in
// cli_test.go's allCommands even though isManifestCommand reports false for
// them. Today that is exactly "tool-catalog", which allCommands includes
// only for its --help coverage in TestEveryCommandHelpSucceeds even though it
// takes no --manifest flag.
var manifestCommandParityExceptions = map[string]bool{
	"tool-catalog": true,
}

// TestManifestCommandInventoryMatchesLegacyCommandList implements item 6 for
// cli_test.go's allCommands: rather than trusting that manually maintained
// list, it structurally discovers every manifest command (isManifestCommand)
// and asserts the two sets agree, so drift becomes a build failure instead of
// a silent gap in release coverage (this caught allCommands missing
// "api-center-list", "api-center-show", "logicapps-registration-plan", and
// "connector-toolbox-deploy", now fixed alongside this test).
func TestManifestCommandInventoryMatchesLegacyCommandList(t *testing.T) {
	t.Parallel()
	root := rootCmd()

	discovered := map[string]bool{}
	for _, cmd := range qualifiedCommands(root) {
		if isManifestCommand(cmd) {
			discovered[legacyCommandName(cmd)] = true
		}
	}

	listed := map[string]bool{}
	for _, name := range allCommands {
		listed[name] = true
	}

	for name := range discovered {
		if !listed[name] && !manifestCommandParityExceptions[name] {
			t.Errorf(
				"command %q takes -f/--manifest (a manifest command) but is missing from cli_test.go's allCommands",
				name,
			)
		}
	}
	for name := range listed {
		if manifestCommandParityExceptions[name] {
			continue
		}
		if !discovered[name] {
			t.Errorf(
				"cli_test.go's allCommands lists %q, but it is not a real manifest command (or does not exist)",
				name,
			)
		}
	}
}

// TestTrustEnforcingCommandInventoryMatchesLegacyCommandList implements item
// 6 for the security-relevant enforcingCommands list in cli_test.go: it
// structurally discovers every command that registers all three trust
// approval flags (isTrustEnforcingCommand) and asserts parity, so a future
// command that silently gains or loses trust flags cannot go untracked the
// way "connector-toolbox-deploy" previously did (now fixed alongside this
// test).
func TestTrustEnforcingCommandInventoryMatchesLegacyCommandList(t *testing.T) {
	t.Parallel()
	root := rootCmd()

	discovered := map[string]bool{}
	for _, cmd := range qualifiedCommands(root) {
		if isTrustEnforcingCommand(cmd) {
			discovered[legacyCommandName(cmd)] = true
		}
	}

	listed := map[string]bool{}
	for _, name := range enforcingCommands {
		listed[name] = true
	}

	for name := range discovered {
		if !listed[name] {
			t.Errorf(
				"command %q registers all three trust approval flags but is missing from cli_test.go's enforcingCommands",
				name,
			)
		}
	}
	for name := range listed {
		if !discovered[name] {
			t.Errorf(
				"cli_test.go's enforcingCommands lists %q, but it does not register all three trust approval flags",
				name,
			)
		}
	}
}
