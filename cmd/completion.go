package main

import (
	"fmt"
	"strings"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/cliout"
	"foundry-agent-manager/internal/trust"

	"github.com/spf13/cobra"
)

func configureCommandCompletions(root *cobra.Command) {
	mustRegisterFlagCompletion(root, "output", fixedCompletions(
		cobra.CompletionWithDesc(string(cliout.Text), "Human-readable output"),
		cobra.CompletionWithDesc(string(cliout.JSON), "JSON automation envelope"),
		cobra.CompletionWithDesc(string(cliout.YAML), "YAML automation envelope"),
	))
	mustRegisterFlagCompletion(root, "progress", fixedCompletions(
		cobra.CompletionWithDesc("auto", "Spinner on terminals and heartbeat lines in redirected text"),
		cobra.CompletionWithDesc("plain", "Stable elapsed-time log lines"),
		cobra.CompletionWithDesc("off", "Disable long-running operation progress"),
	))

	clouds := make([]cobra.Completion, 0, len(azcloud.Names()))
	for _, name := range azcloud.Names() {
		clouds = append(clouds, cobra.CompletionWithDesc(name, "Supported Azure cloud"))
	}
	mustRegisterFlagCompletion(root, "cloud", fixedCompletions(clouds...))

	var configure func(*cobra.Command)
	configure = func(command *cobra.Command) {
		if command.ValidArgsFunction == nil {
			command.ValidArgsFunction = cobra.NoFileCompletions
		}

		if command.Flags().Lookup("type") != nil {
			mustRegisterFlagCompletion(command, "type", fixedCompletions(
				cobra.CompletionWithDesc("prompt", "Prompt Agent manifest"),
				cobra.CompletionWithDesc("hosted", "Hosted Agent workspace"),
			))
		}
		if command.Flags().Lookup("protocol") != nil {
			mustRegisterFlagCompletion(command, "protocol", fixedCompletions(
				cobra.CompletionWithDesc("responses", "Responses API protocol"),
				cobra.CompletionWithDesc("invocations", "Invocations API protocol"),
			))
		}
		if command.Flags().Lookup("kind") != nil {
			mustRegisterFlagCompletion(command, "kind", fixedCompletions(
				cobra.CompletionWithDesc("user_profile", "User profile memory"),
				cobra.CompletionWithDesc("chat_summary", "Conversation summary memory"),
				cobra.CompletionWithDesc("procedural", "Procedural memory"),
			))
		}
		if command.Flags().Lookup("version-upgrade-option") != nil {
			mustRegisterFlagCompletion(command, "version-upgrade-option", fixedCompletions(
				cobra.CompletionWithDesc("OnceNewDefaultVersionAvailable", "Upgrade when a new default model version is available"),
				cobra.CompletionWithDesc("OnceCurrentVersionExpired", "Upgrade only when the current model version expires"),
				cobra.CompletionWithDesc("NoAutoUpgrade", "Keep the selected model version until changed explicitly"),
			))
		}
		if command.Flags().Lookup("enabled") != nil {
			mustRegisterFlagCompletion(command, "enabled", fixedCompletions(
				cobra.CompletionWithDesc("true", "Enable the requested setting"),
				cobra.CompletionWithDesc("false", "Disable the requested setting"),
			))
		}

		markCompletionPaths(command)
		for _, child := range command.Commands() {
			configure(child)
		}
	}
	for _, command := range root.Commands() {
		configure(command)
	}
}

func completeHelpTopics(
	command *cobra.Command,
	args []string,
	toComplete string,
) ([]cobra.Completion, cobra.ShellCompDirective) {
	target := command.Root()
	if len(args) > 0 {
		found, remaining, err := target.Find(args)
		if err != nil || found == nil || len(remaining) != 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		target = found
	}

	completions := make([]cobra.Completion, 0)
	for _, child := range target.Commands() {
		if !child.IsAvailableCommand() || !strings.HasPrefix(child.Name(), toComplete) {
			continue
		}
		completions = append(
			completions,
			cobra.CompletionWithDesc(child.Name(), child.Short),
		)
	}
	return completions, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}

func fixedCompletions(values ...cobra.Completion) cobra.CompletionFunc {
	return cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp)
}

func mustRegisterFlagCompletion(
	command *cobra.Command,
	flagName string,
	completion cobra.CompletionFunc,
) {
	if err := command.RegisterFlagCompletionFunc(flagName, completion); err != nil {
		panic(fmt.Sprintf(
			"register completion for %s --%s: %v",
			command.CommandPath(),
			flagName,
			err,
		))
	}
}

func markCompletionPaths(command *cobra.Command) {
	fileExtensions := map[string][]string{
		"credentials-file":        {"json"},
		"input-file":              {"json"},
		"items-file":              {"json"},
		"manifest":                {"yaml", "yml", "json"},
		"metadata-file":           {"json"},
		"publication":             {"yaml", "yml", "json"},
		"skill-instructions-file": {"md", "markdown"},
		"structured-inputs-file":  {"json"},
		trust.FlagPolicyFile:      {"yaml", "yml", "json"},
	}
	for flagName, extensions := range fileExtensions {
		if command.Flags().Lookup(flagName) == nil {
			continue
		}
		if err := command.MarkFlagFilename(flagName, extensions...); err != nil {
			panic(fmt.Sprintf(
				"mark filename completion for %s --%s: %v",
				command.CommandPath(),
				flagName,
				err,
			))
		}
	}

	for _, flagName := range []string{"workspace", "work-dir"} {
		if command.Flags().Lookup(flagName) == nil {
			continue
		}
		if err := command.MarkFlagDirname(flagName); err != nil {
			panic(fmt.Sprintf(
				"mark directory completion for %s --%s: %v",
				command.CommandPath(),
				flagName,
				err,
			))
		}
	}
}
