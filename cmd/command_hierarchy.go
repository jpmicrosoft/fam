package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	legacyCommandAnnotation    = "foundry-agent-manager/legacy-command"
	canonicalCommandAnnotation = "foundry-agent-manager/canonical-command"
)

type commandRoute struct {
	Legacy string
	Path   []string
}

type namespaceMetadata struct {
	Short   string
	GroupID string
}

var commandRoutes = []commandRoute{
	{Legacy: "init", Path: []string{"prompt", "init"}},
	{Legacy: "validate", Path: []string{"prompt", "validate"}},
	{Legacy: "plan", Path: []string{"prompt", "plan"}},
	{Legacy: "compatibility", Path: []string{"prompt", "compatibility"}},
	{Legacy: "preflight", Path: []string{"prompt", "preflight"}},
	{Legacy: "deploy", Path: []string{"prompt", "deploy"}},
	{Legacy: "status", Path: []string{"prompt", "status"}},
	{Legacy: "show", Path: []string{"prompt", "show"}},
	{Legacy: "endpoint-show", Path: []string{"prompt", "endpoint", "show"}},
	{Legacy: "endpoint-configure", Path: []string{"prompt", "endpoint", "configure"}},
	{Legacy: "versions", Path: []string{"prompt", "versions", "list"}},
	{Legacy: "diff", Path: []string{"prompt", "diff"}},
	{Legacy: "smoke", Path: []string{"prompt", "smoke"}},
	{Legacy: "disable", Path: []string{"prompt", "disable"}},
	{Legacy: "enable", Path: []string{"prompt", "enable"}},
	{Legacy: "promote", Path: []string{"prompt", "promote"}},
	{Legacy: "rollback", Path: []string{"prompt", "rollback"}},
	{Legacy: "prune", Path: []string{"prompt", "versions", "prune"}},
	{Legacy: "delete-version", Path: []string{"prompt", "versions", "delete"}},
	{Legacy: "delete", Path: []string{"prompt", "delete"}},
	{Legacy: "decommission", Path: []string{"prompt", "decommission"}},
	{Legacy: "publish-m365", Path: []string{"prompt", "m365", "publish"}},
	{Legacy: "legacy-status", Path: []string{"prompt", "legacy", "status"}},
	{Legacy: "legacy-deploy", Path: []string{"prompt", "legacy", "deploy"}},
	{Legacy: "legacy-delete", Path: []string{"prompt", "legacy", "delete"}},

	{Legacy: "hosted-info", Path: []string{"hosted", "info"}},
	{Legacy: "hosted-adopt", Path: []string{"hosted", "adopt"}},
	{Legacy: "hosted-init", Path: []string{"hosted", "init"}},
	{Legacy: "hosted-validate", Path: []string{"hosted", "validate"}},
	{Legacy: "hosted-plan", Path: []string{"hosted", "plan"}},
	{Legacy: "hosted-environment-create", Path: []string{"hosted", "environment", "create"}},
	{Legacy: "hosted-preflight", Path: []string{"hosted", "preflight"}},
	{Legacy: "hosted-deploy", Path: []string{"hosted", "deploy"}},
	{Legacy: "hosted-status", Path: []string{"hosted", "status"}},
	{Legacy: "hosted-show", Path: []string{"hosted", "show"}},
	{Legacy: "hosted-versions", Path: []string{"hosted", "versions", "list"}},
	{Legacy: "hosted-diff", Path: []string{"hosted", "diff"}},
	{Legacy: "hosted-diagnose", Path: []string{"hosted", "diagnose"}},
	{Legacy: "hosted-smoke", Path: []string{"hosted", "smoke"}},
	{Legacy: "hosted-session-create", Path: []string{"hosted", "session", "create"}},
	{Legacy: "hosted-session-list", Path: []string{"hosted", "session", "list"}},
	{Legacy: "hosted-session-show", Path: []string{"hosted", "session", "show"}},
	{Legacy: "hosted-session-stop", Path: []string{"hosted", "session", "stop"}},
	{Legacy: "hosted-session-delete", Path: []string{"hosted", "session", "delete"}},
	{Legacy: "hosted-session-file-upload", Path: []string{"hosted", "session", "file", "upload"}},
	{Legacy: "hosted-session-file-list", Path: []string{"hosted", "session", "file", "list"}},
	{Legacy: "hosted-session-file-download", Path: []string{"hosted", "session", "file", "download"}},
	{Legacy: "hosted-session-file-delete", Path: []string{"hosted", "session", "file", "delete"}},
	{Legacy: "hosted-promote", Path: []string{"hosted", "promote"}},
	{Legacy: "hosted-rollback", Path: []string{"hosted", "rollback"}},
	{Legacy: "hosted-prune", Path: []string{"hosted", "versions", "prune"}},
	{Legacy: "hosted-delete-version", Path: []string{"hosted", "versions", "delete"}},
	{Legacy: "hosted-delete", Path: []string{"hosted", "delete"}},
	{Legacy: "hosted-logs", Path: []string{"hosted", "logs"}},
	{Legacy: "hosted-draft-deploy", Path: []string{"hosted", "draft", "deploy"}},
	{Legacy: "hosted-disable", Path: []string{"hosted", "disable"}},
	{Legacy: "hosted-enable", Path: []string{"hosted", "enable"}},

	{Legacy: "project-create", Path: []string{"project", "create"}},
	{Legacy: "connection-list", Path: []string{"project", "connection", "list"}},
	{Legacy: "connection-show", Path: []string{"project", "connection", "show"}},
	{Legacy: "connection-create", Path: []string{"project", "connection", "create"}},
	{Legacy: "connection-update", Path: []string{"project", "connection", "update"}},
	{Legacy: "connection-delete", Path: []string{"project", "connection", "delete"}},
	{Legacy: "model-deployment-list", Path: []string{"model", "deployment", "list"}},
	{Legacy: "model-deployment-show", Path: []string{"model", "deployment", "show"}},
	{Legacy: "model-deployment-plan", Path: []string{"model", "deployment", "plan"}},
	{Legacy: "model-deployment-create", Path: []string{"model", "deployment", "create"}},
	{Legacy: "model-deployment-delete", Path: []string{"model", "deployment", "delete"}},

	{Legacy: "agent365-info", Path: []string{"agent365", "info"}},
	{Legacy: "agent365-blueprint-list", Path: []string{"agent365", "blueprint", "list"}},
	{Legacy: "agent365-blueprint-show", Path: []string{"agent365", "blueprint", "show"}},
	{Legacy: "agent365-blueprint-permissions", Path: []string{"agent365", "blueprint", "permissions"}},
	{Legacy: "agent365-blueprint-validate", Path: []string{"agent365", "blueprint", "validate"}},
	{Legacy: "agent365-blueprint-owners", Path: []string{"agent365", "blueprint", "owners"}},
	{Legacy: "agent365-blueprint-sponsors", Path: []string{"agent365", "blueprint", "sponsors"}},
	{Legacy: "agent365-blueprint-identities", Path: []string{"agent365", "blueprint", "identities"}},
	{Legacy: "agent365-identity-list", Path: []string{"agent365", "identity", "list"}},
	{Legacy: "agent365-identity-show", Path: []string{"agent365", "identity", "show"}},
	{Legacy: "agent365-blueprint-principal-list", Path: []string{"agent365", "blueprint", "principal", "list"}},
	{Legacy: "agent365-blueprint-principal-show", Path: []string{"agent365", "blueprint", "principal", "show"}},
	{Legacy: "agent365-binding-plan", Path: []string{"agent365", "binding", "plan"}},
	{Legacy: "agent365-binding-status", Path: []string{"agent365", "binding", "status"}},
	{Legacy: "agent365-observability-plan", Path: []string{"agent365", "observability", "plan"}},
	{Legacy: "agent365-observability-status", Path: []string{"agent365", "observability", "status"}},
	{Legacy: "agent365-integration-status", Path: []string{"agent365", "integration", "status"}},
	{Legacy: "agent365-integration-plan", Path: []string{"agent365", "integration", "plan"}},
	{Legacy: "agent365-integration-set", Path: []string{"agent365", "integration", "set"}},
	{Legacy: "agent365-publication-info", Path: []string{"agent365", "publication", "info"}},
	{Legacy: "agent365-publication-plan", Path: []string{"agent365", "publication", "plan"}},
	{Legacy: "agent365-publication-status", Path: []string{"agent365", "publication", "status"}},
	{Legacy: "agent365-publication-admin-handoff", Path: []string{"agent365", "publication", "admin-handoff"}},

	{Legacy: "receipt-upload", Path: []string{"receipt", "upload"}},

	{Legacy: "api-center-list", Path: []string{"connector", "api-center", "list"}},
	{Legacy: "api-center-show", Path: []string{"connector", "api-center", "show"}},
	{Legacy: "logicapps-registration-plan", Path: []string{"connector", "logic-apps", "registration", "plan"}},
	{Legacy: "connector-list", Path: []string{"connector", "list"}},
	{Legacy: "connector-show", Path: []string{"connector", "show"}},
	{Legacy: "connector-create", Path: []string{"connector", "create"}},
	{Legacy: "connector-consent", Path: []string{"connector", "consent"}},
	{Legacy: "connector-actions", Path: []string{"connector", "actions"}},
	{Legacy: "connector-configure", Path: []string{"connector", "configure"}},
	{Legacy: "connector-status", Path: []string{"connector", "status"}},
	{Legacy: "connector-wait", Path: []string{"connector", "wait"}},
	{Legacy: "connector-toolbox-deploy", Path: []string{"connector", "toolbox", "deploy"}},
	{Legacy: "connector-delete", Path: []string{"connector", "delete"}},

	{Legacy: "toolbox-validate", Path: []string{"toolbox", "validate"}},
	{Legacy: "toolbox-plan", Path: []string{"toolbox", "plan"}},
	{Legacy: "toolbox-deploy", Path: []string{"toolbox", "deploy"}},
	{Legacy: "toolbox-status", Path: []string{"toolbox", "status"}},
	{Legacy: "toolbox-versions", Path: []string{"toolbox", "versions", "list"}},
	{Legacy: "toolbox-promote", Path: []string{"toolbox", "promote"}},
	{Legacy: "toolbox-delete-version", Path: []string{"toolbox", "versions", "delete"}},

	{Legacy: "skill-create", Path: []string{"skill", "create"}},
	{Legacy: "skill-list", Path: []string{"skill", "list"}},
	{Legacy: "skill-show", Path: []string{"skill", "show"}},
	{Legacy: "skill-version-list", Path: []string{"skill", "version", "list"}},
	{Legacy: "skill-version-show", Path: []string{"skill", "version", "show"}},
	{Legacy: "skill-set-default", Path: []string{"skill", "version", "set-default"}},
	{Legacy: "skill-delete", Path: []string{"skill", "delete"}},
	{Legacy: "skill-version-delete", Path: []string{"skill", "version", "delete"}},
	{Legacy: "skill-download", Path: []string{"skill", "download"}},

	{Legacy: "grounding-validate", Path: []string{"grounding", "validate"}},
	{Legacy: "grounding-plan", Path: []string{"grounding", "plan"}},
	{Legacy: "grounding-sync", Path: []string{"grounding", "sync"}},
	{Legacy: "grounding-status", Path: []string{"grounding", "status"}},
	{Legacy: "grounding-delete-file", Path: []string{"grounding", "file", "delete"}},
	{Legacy: "grounding-delete-store", Path: []string{"grounding", "store", "delete"}},

	{Legacy: "memory-store-validate", Path: []string{"memory", "store", "validate"}},
	{Legacy: "memory-store-plan", Path: []string{"memory", "store", "plan"}},
	{Legacy: "memory-store-sync", Path: []string{"memory", "store", "sync"}},
	{Legacy: "memory-store-list", Path: []string{"memory", "store", "list"}},
	{Legacy: "memory-store-show", Path: []string{"memory", "store", "show"}},
	{Legacy: "memory-store-delete", Path: []string{"memory", "store", "delete"}},
	{Legacy: "memory-search", Path: []string{"memory", "search"}},
	{Legacy: "memory-update", Path: []string{"memory", "update"}},
	{Legacy: "memory-item-create", Path: []string{"memory", "item", "create"}},
	{Legacy: "memory-item-list", Path: []string{"memory", "item", "list"}},
	{Legacy: "memory-item-show", Path: []string{"memory", "item", "show"}},
	{Legacy: "memory-item-update", Path: []string{"memory", "item", "update"}},
	{Legacy: "memory-item-delete", Path: []string{"memory", "item", "delete"}},
	{Legacy: "memory-scope-delete", Path: []string{"memory", "scope", "delete"}},

	{Legacy: "autopilot-info", Path: []string{"autopilot", "info"}},
	{Legacy: "autopilot-preflight", Path: []string{"autopilot", "preflight"}},
	{Legacy: "autopilot-deploy", Path: []string{"autopilot", "deploy"}},
}

var namespaceCatalog = map[string]namespaceMetadata{
	"prompt":                            {Short: "Create, deploy, and operate Prompt Agents.", GroupID: "prompt-lifecycle"},
	"prompt endpoint":                   {Short: "Inspect and configure stable Prompt Agent endpoints."},
	"prompt versions":                   {Short: "List and remove immutable Prompt Agent versions."},
	"prompt m365":                       {Short: "Publish Prompt Agent endpoints to Microsoft 365."},
	"prompt legacy":                     {Short: "Manage explicit legacy Agent Application compatibility resources."},
	"hosted":                            {Short: "Create, deploy, and operate Hosted Agents.", GroupID: "hosted"},
	"hosted environment":                {Short: "Manage local azd environments used by Hosted workspaces."},
	"hosted versions":                   {Short: "List and remove Hosted Agent versions."},
	"hosted session":                    {Short: "Manage Hosted Agent sessions."},
	"hosted session file":               {Short: "Manage files in Hosted Agent session sandboxes."},
	"hosted draft":                      {Short: "Manage preview Hosted Agent drafts."},
	"project":                           {Short: "Manage Foundry projects and project-scoped resources.", GroupID: "tools-integrations"},
	"project connection":                {Short: "Manage Foundry project connections."},
	"model":                             {Short: "Discover, validate, create, and delete Foundry model deployments.", GroupID: "models"},
	"model deployment":                  {Short: "Manage account-scoped Foundry model deployments through ARM."},
	"agent365":                          {Short: "Inventory, govern, integrate, observe, and plan Agent 365 adoption.", GroupID: "agent365"},
	"agent365 blueprint":                {Short: "Inspect and validate Microsoft Entra Agent ID blueprints."},
	"agent365 blueprint principal":      {Short: "Inspect tenant-local Agent ID blueprint principals."},
	"agent365 identity":                 {Short: "Inspect Microsoft Entra Agent ID identity inventory."},
	"agent365 binding":                  {Short: "Plan and inspect Foundry-to-blueprint identity correlation."},
	"agent365 observability":            {Short: "Plan and validate Hosted Agent 365 observability."},
	"agent365 integration":              {Short: "Inspect and manage account-wide Agent 365 data collection."},
	"agent365 publication":              {Short: "Plan publication, identity migration, and administrator handoff."},
	"receipt":                           {Short: "Publish preserved operation receipts to audit destinations.", GroupID: "getting-started"},
	"connector":                         {Short: "Discover, configure, and operate Foundry connectors.", GroupID: "tools-integrations"},
	"connector api-center":              {Short: "Browse Azure API Center MCP registry metadata."},
	"connector logic-apps":              {Short: "Plan Logic Apps connector integration."},
	"connector logic-apps registration": {Short: "Plan portal-managed Logic Apps registration."},
	"connector toolbox":                 {Short: "Create Toolboxes from connected managed connectors."},
	"toolbox":                           {Short: "Validate, deploy, and operate Foundry Toolboxes.", GroupID: "tools-integrations"},
	"toolbox versions":                  {Short: "List and remove immutable Toolbox versions."},
	"skill":                             {Short: "Create and manage Foundry Skills.", GroupID: "tools-integrations"},
	"skill version":                     {Short: "Inspect and manage immutable Skill versions."},
	"grounding":                         {Short: "Synchronize and operate document grounding.", GroupID: "tools-integrations"},
	"grounding file":                    {Short: "Manage files attached to grounding stores."},
	"grounding store":                   {Short: "Manage grounding vector stores."},
	"memory":                            {Short: "Manage preview memory stores and items.", GroupID: "tools-integrations"},
	"memory store":                      {Short: "Validate, synchronize, and inspect memory stores."},
	"memory item":                       {Short: "Create and manage explicit memory items."},
	"memory scope":                      {Short: "Manage memory isolation scopes."},
	"autopilot":                         {Short: "Inspect and deploy the pinned experimental Autopilot sample.", GroupID: "autopilot"},
}

var canonicalPathsByLegacy = func() map[string][]string {
	paths := make(map[string][]string, len(commandRoutes))
	for _, route := range commandRoutes {
		if _, exists := paths[route.Legacy]; exists {
			panic("duplicate command hierarchy route for " + route.Legacy)
		}
		paths[route.Legacy] = append([]string(nil), route.Path...)
	}
	return paths
}()

func registerCommandHierarchy(root *cobra.Command) {
	original := make(map[string]*cobra.Command)
	for _, command := range root.Commands() {
		original[command.Name()] = command
	}

	routed := make(map[string]bool, len(commandRoutes))
	for _, route := range commandRoutes {
		source := original[route.Legacy]
		if source == nil {
			panic("command hierarchy references missing command " + route.Legacy)
		}
		if len(route.Path) < 2 {
			panic("command hierarchy route must include a namespace and leaf: " + route.Legacy)
		}

		legacyExample := source.Example
		alias := cloneCompatibilityCommand(source, route.Legacy, route.Path)

		root.RemoveCommand(source)
		source.Use = route.Path[len(route.Path)-1]
		source.GroupID = ""
		source.Annotations = cloneStringMap(source.Annotations)
		source.Annotations[legacyCommandAnnotation] = route.Legacy
		source.Annotations[canonicalCommandAnnotation] = strings.Join(route.Path, " ")
		source.Example = rewriteCommandExamples(legacyExample, route.Legacy, route.Path)

		parent := ensureNamespace(root, route.Path[:len(route.Path)-1])
		parent.AddCommand(source)
		root.AddCommand(alias)
		routed[route.Legacy] = true
	}

	for name := range original {
		if isCoreCommand(name) || routed[name] {
			continue
		}
		panic("application command is missing a hierarchy route: " + name)
	}
}

func ensureNamespace(root *cobra.Command, path []string) *cobra.Command {
	parent := root
	for index, segment := range path {
		var namespace *cobra.Command
		for _, child := range parent.Commands() {
			if child.Name() == segment && !child.Hidden {
				namespace = child
				break
			}
		}
		if namespace == nil {
			fullPath := strings.Join(path[:index+1], " ")
			metadata, ok := namespaceCatalog[fullPath]
			if !ok {
				panic("missing namespace metadata for " + fullPath)
			}
			namespace = &cobra.Command{
				Use:          segment,
				Short:        metadata.Short,
				GroupID:      metadata.GroupID,
				SilenceUsage: true,
				Example:      "  fam help " + fullPath,
			}
			parent.AddCommand(namespace)
		}
		parent = namespace
	}
	return parent
}

func cloneCompatibilityCommand(source *cobra.Command, legacy string, canonical []string) *cobra.Command {
	alias := &cobra.Command{
		Use:                        legacy,
		Aliases:                    append([]string(nil), source.Aliases...),
		SuggestFor:                 append([]string(nil), source.SuggestFor...),
		Short:                      source.Short,
		Long:                       source.Long,
		Example:                    source.Example,
		Args:                       source.Args,
		ArgAliases:                 append([]string(nil), source.ArgAliases...),
		BashCompletionFunction:     source.BashCompletionFunction,
		Deprecated:                 source.Deprecated,
		Hidden:                     true,
		Annotations:                cloneStringMap(source.Annotations),
		Version:                    source.Version,
		PersistentPreRun:           source.PersistentPreRun,
		PersistentPreRunE:          source.PersistentPreRunE,
		PreRun:                     source.PreRun,
		PreRunE:                    source.PreRunE,
		Run:                        source.Run,
		RunE:                       source.RunE,
		PostRun:                    source.PostRun,
		PostRunE:                   source.PostRunE,
		PersistentPostRun:          source.PersistentPostRun,
		PersistentPostRunE:         source.PersistentPostRunE,
		ValidArgs:                  append([]string(nil), source.ValidArgs...),
		ValidArgsFunction:          source.ValidArgsFunction,
		DisableFlagParsing:         source.DisableFlagParsing,
		DisableAutoGenTag:          source.DisableAutoGenTag,
		DisableFlagsInUseLine:      source.DisableFlagsInUseLine,
		DisableSuggestions:         source.DisableSuggestions,
		SuggestionsMinimumDistance: source.SuggestionsMinimumDistance,
		SilenceErrors:              source.SilenceErrors,
		SilenceUsage:               source.SilenceUsage,
		FParseErrWhitelist:         source.FParseErrWhitelist,
		TraverseChildren:           source.TraverseChildren,
	}
	alias.Annotations[legacyCommandAnnotation] = legacy
	alias.Annotations[canonicalCommandAnnotation] = strings.Join(canonical, " ")
	cloneFlagSet(source.Flags(), alias.Flags())
	return alias
}

func cloneFlagSet(source, target *pflag.FlagSet) {
	target.SortFlags = source.SortFlags
	source.VisitAll(func(flag *pflag.Flag) {
		switch flag.Value.Type() {
		case "bool":
			value, err := source.GetBool(flag.Name)
			mustCloneFlagValue(flag, err)
			target.BoolP(flag.Name, flag.Shorthand, value, flag.Usage)
		case "string":
			value, err := source.GetString(flag.Name)
			mustCloneFlagValue(flag, err)
			target.StringP(flag.Name, flag.Shorthand, value, flag.Usage)
		case "stringArray":
			value, err := source.GetStringArray(flag.Name)
			mustCloneFlagValue(flag, err)
			target.StringArrayP(flag.Name, flag.Shorthand, value, flag.Usage)
		case "stringSlice":
			value, err := source.GetStringSlice(flag.Name)
			mustCloneFlagValue(flag, err)
			target.StringSliceP(flag.Name, flag.Shorthand, value, flag.Usage)
		case "int":
			value, err := source.GetInt(flag.Name)
			mustCloneFlagValue(flag, err)
			target.IntP(flag.Name, flag.Shorthand, value, flag.Usage)
		case "int64":
			value, err := source.GetInt64(flag.Name)
			mustCloneFlagValue(flag, err)
			target.Int64P(flag.Name, flag.Shorthand, value, flag.Usage)
		case "float64":
			value, err := source.GetFloat64(flag.Name)
			mustCloneFlagValue(flag, err)
			target.Float64P(flag.Name, flag.Shorthand, value, flag.Usage)
		case "duration":
			value, err := source.GetDuration(flag.Name)
			mustCloneFlagValue(flag, err)
			target.DurationP(flag.Name, flag.Shorthand, value, flag.Usage)
		default:
			panic(fmt.Sprintf(
				"cannot clone command flag --%s with unsupported type %q",
				flag.Name,
				flag.Value.Type(),
			))
		}

		cloned := target.Lookup(flag.Name)
		cloned.DefValue = flag.DefValue
		cloned.NoOptDefVal = flag.NoOptDefVal
		cloned.Deprecated = flag.Deprecated
		cloned.Hidden = flag.Hidden
		cloned.ShorthandDeprecated = flag.ShorthandDeprecated
		cloned.Annotations = cloneStringSliceMap(flag.Annotations)
	})
}

func mustCloneFlagValue(flag *pflag.Flag, err error) {
	if err != nil {
		panic(fmt.Sprintf("read default for --%s while cloning compatibility command: %v", flag.Name, err))
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source)+2)
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneStringSliceMap(source map[string][]string) map[string][]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string][]string, len(source))
	for key, value := range source {
		cloned[key] = append([]string(nil), value...)
	}
	return cloned
}

func rewriteCommandExamples(example, legacy string, canonical []string) string {
	return strings.ReplaceAll(
		example,
		"fam "+legacy,
		"fam "+strings.Join(canonical, " "),
	)
}

func isCoreCommand(name string) bool {
	switch name {
	case "version", "quickstart", "doctor", "tool-catalog":
		return true
	default:
		return false
	}
}

func legacyCommandName(command *cobra.Command) string {
	if command != nil && command.Annotations != nil {
		if name := command.Annotations[legacyCommandAnnotation]; name != "" {
			return name
		}
	}
	if command == nil {
		return ""
	}
	return command.Name()
}

func canonicalCommandArgs(legacy string) []string {
	path := canonicalPathsByLegacy[legacy]
	if len(path) == 0 {
		return []string{legacy}
	}
	return append([]string(nil), path...)
}

func canonicalCommandText(legacy string) string {
	return "fam " + strings.Join(canonicalCommandArgs(legacy), " ")
}

func commandPathArgs(command *cobra.Command) []string {
	if command == nil {
		return nil
	}
	parts := strings.Fields(command.CommandPath())
	if len(parts) == 0 {
		return nil
	}
	return parts[1:]
}
