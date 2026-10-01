package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/hosted"
	"foundry-agent-manager/internal/hostedskills"
	"foundry-agent-manager/internal/skillruntime"
	"foundry-agent-manager/internal/skills"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func registerHostedSkillCommands(root *cobra.Command) {
	commands := []struct {
		name, description string
		run               func(*cobra.Command, []string) error
	}{
		{"hosted-skill-attach", "Edit a local Hosted Skill declaration without deploying or publishing.", cmdHostedSkillAttach},
		{"hosted-skill-remove", "Remove a local Hosted Skill declaration without deleting shared Azure resources.", cmdHostedSkillRemove},
		{"hosted-skill-list", "List local Hosted Skill declarations without contacting Azure.", cmdHostedSkillList},
		{"hosted-skill-sync", "Read pinned Skills and write verified local runtime artifacts without deploying.", cmdHostedSkillSync},
	}
	for _, spec := range commands {
		command := &cobra.Command{
			Use: spec.name, Short: spec.description,
			Args: noArgs, RunE: spec.run, SilenceUsage: true,
		}
		addHostedWorkspaceFlags(command)
		switch spec.name {
		case "hosted-skill-attach", "hosted-skill-remove":
			command.Flags().String("skill", "", "Existing Foundry Skill name.")
			command.Flags().String("path", "", "Local Skill directory relative to the workspace.")
			if spec.name == "hosted-skill-attach" {
				command.Flags().String("version", "", "Explicit immutable Foundry Skill version.")
				command.Flags().String("mode", "", "Skill delivery mode: bundle (default for new declarations) or mcp.")
				command.Flags().String("language", "", "Application integration language: python or dotnet.")
				command.Flags().String("toolbox", "", "Same-project Toolbox for MCP Skills.")
				command.Flags().String("toolbox-version", "", "Immutable Toolbox version for MCP Skills.")
			}
		case "hosted-skill-sync":
			addHostedPreviewFlag(command)
		}
		root.AddCommand(command)
	}
}

func cmdHostedSkillList(cmd *cobra.Command, _ []string) error {
	_, workspace, err := resolveHostedWorkspace(cmd, false)
	if err != nil {
		return err
	}
	cfg, err := hostedskills.Load(workspace.Root, workspace.Selected.ServiceName)
	if err != nil {
		return err
	}
	return printResult(cmd, map[string]interface{}{
		"service": workspace.Selected.ServiceName, "managed": cfg != nil, "configuration": cfg,
	}, fmt.Sprintf("Hosted Skill declarations: service=%s managed=%t (local configuration only)",
		workspace.Selected.ServiceName, cfg != nil))
}

func cmdHostedSkillAttach(cmd *cobra.Command, _ []string) error {
	return editHostedSkill(cmd, false)
}

func cmdHostedSkillRemove(cmd *cobra.Command, _ []string) error {
	return editHostedSkill(cmd, true)
}

func editHostedSkill(cmd *cobra.Command, remove bool) error {
	_, workspace, err := resolveHostedWorkspace(cmd, false)
	if err != nil {
		return err
	}
	name, local := getFlag(cmd, "skill"), getFlag(cmd, "path")
	if (name == "") == (local == "") {
		return errs.Config("specify exactly one of --skill or --path")
	}
	if !remove && local != "" {
		if getFlag(cmd, "version") != "" {
			return errs.Config("--version cannot be combined with a local --path")
		}
		if _, err := skills.LoadDirectory(workspace.Root, local); err != nil {
			return err
		}
	}
	if !remove && name != "" {
		if err := skills.ValidateReference(skills.Reference{Name: name, Version: getFlag(cmd, "version")}); err != nil {
			return err
		}
	}
	path, original, doc, err := readSkillConfiguration(filepath.Join(workspace.Root, "fam.skills.yaml"), !remove)
	if err != nil {
		return err
	}
	before, err := yaml.Marshal(doc)
	if err != nil {
		return errs.Manifest("failed to inspect Hosted Skills: %v", err)
	}
	root := doc.Content[0]
	if original == nil {
		yamlSet(root, "apiVersion", yamlString("foundry-agent-manager/skills/v1"))
	}
	services, err := yamlMapping(root, "services", !remove)
	if err != nil {
		return err
	}
	service, err := yamlMapping(services, workspace.Selected.ServiceName, !remove)
	if err != nil {
		return err
	}
	if !remove {
		for _, item := range []struct{ flag, key, fallback string }{
			{"mode", "mode", "bundle"}, {"language", "language", ""},
		} {
			value := getFlag(cmd, item.flag)
			if value != "" {
				yamlSet(service, item.key, yamlString(value))
			} else if yamlValue(service, item.key) == nil {
				value = item.fallback
				if item.key == "language" && workspace.Selected.Code != nil {
					value = "python"
					if strings.HasPrefix(workspace.Selected.Code.Runtime, "dotnet") {
						value = "dotnet"
					}
				}
				if value == "" {
					return errs.Config("--language is required when initializing container or image Skill declarations")
				}
				yamlSet(service, item.key, yamlString(value))
			}
		}
		toolboxName, toolboxVersion := getFlag(cmd, "toolbox"), getFlag(cmd, "toolbox-version")
		if getFlag(cmd, "mode") == "bundle" && toolboxName == "" && toolboxVersion == "" {
			for i := 0; i+1 < len(service.Content); i += 2 {
				if service.Content[i].Value == "toolbox" {
					service.Content = append(service.Content[:i], service.Content[i+2:]...)
					break
				}
			}
		}
		if toolboxName != "" || toolboxVersion != "" {
			if toolboxName == "" || toolboxVersion == "" {
				return errs.Config("--toolbox and --toolbox-version must be provided together")
			}
			toolbox := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			yamlSet(toolbox, "name", yamlString(toolboxName))
			yamlSet(toolbox, "version", yamlString(toolboxVersion))
			yamlSet(service, "toolbox", toolbox)
		}
	}
	sequence := yamlValue(service, "skills")
	if sequence == nil {
		if remove {
			return errs.Config("the selected service has no managed Skill declarations")
		}
		sequence = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		yamlSet(service, "skills", sequence)
	}
	if sequence.Kind != yaml.SequenceNode {
		return errs.Manifest("Hosted skills must be a sequence, not an alias or another YAML type")
	}
	field, target := "name", name
	if local != "" {
		field, target = "path", filepath.ToSlash(filepath.Clean(local))
	}
	matched := false
	for i, entry := range sequence.Content {
		value := yamlValue(entry, field)
		if value == nil || value.Value != target {
			continue
		}
		matched = true
		if remove {
			sequence.Content = append(sequence.Content[:i], sequence.Content[i+1:]...)
		} else if name != "" {
			yamlSet(entry, "version", yamlString(getFlag(cmd, "version")))
		}
		break
	}
	if !matched && !remove {
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlSet(entry, field, yamlString(target))
		if name != "" {
			yamlSet(entry, "version", yamlString(getFlag(cmd, "version")))
		}
		sequence.Content = append(sequence.Content, entry)
	}
	encoded, err := yaml.Marshal(doc)
	if err != nil {
		return errs.Manifest("failed to encode Hosted Skills: %v", err)
	}
	cfg, err := hostedskills.ParseService(encoded, workspace.Selected.ServiceName)
	if err != nil {
		return err
	}
	changed := !bytes.Equal(before, encoded)
	if changed {
		if err := saveSkillConfiguration(path, original, doc); err != nil {
			return err
		}
	}
	return printResult(cmd, map[string]interface{}{
		"service": workspace.Selected.ServiceName, "file": path, "configuration": cfg, "changed": changed,
	}, fmt.Sprintf("Hosted Skill declarations updated: service=%s changed=%t; run hosted skill sync before deploying",
		workspace.Selected.ServiceName, changed))
}

func cmdHostedSkillSync(cmd *cobra.Command, _ []string) error {
	profile, workspace, err := resolveHostedWorkspace(cmd, false)
	if err != nil {
		return err
	}
	cfg := workspace.Selected.Skills
	if cfg == nil {
		return errs.Config("the selected service has no Skill declaration; use hosted skill attach first")
	}
	runtimeFiles, err := skillruntime.Files(cfg.Language)
	if err != nil {
		return err
	}
	ctx, cancel, err := hostedExecutionContext(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	options := hostedskills.SyncOptions{
		Root: workspace.Root, SourceDirectory: workspace.Selected.SourceDirectory,
		Service: workspace.Selected.ServiceName, Config: cfg,
		ProjectEndpoint: workspace.Selected.ProjectEndpoint, Image: workspace.Selected.Image,
		RuntimeFiles: runtimeFiles,
	}
	remote := false
	for _, source := range cfg.Skills {
		remote = remote || source.Path == ""
	}
	if remote {
		if !getBoolFlag(cmd, "accept-preview") {
			return errs.Config("remote Skill synchronization requires --accept-preview; no Azure changes are made")
		}
		var azdPath string
		if options.ProjectEndpoint == "" {
			azdPath, err = hosted.ResolveAZD(profile.Name, hostedLookPathFn)
			if err != nil {
				return hostedCommandError(err)
			}
		}
		options.ProjectEndpoint, err = hosted.ResolveProjectEndpoint(
			ctx, newHostedRunner(cmd), azdPath, workspace, getFlag(cmd, "environment"), nil,
		)
		if err != nil {
			return hostedCommandError(err)
		}
		credential, err := newCredential(cmd, profile)
		if err != nil {
			return err
		}
		options.Client = foundry.NewClientWithOptions(options.ProjectEndpoint, credential, newHTTPClient(cmd),
			foundry.ClientOptions{Scope: profile.FoundryScope, AllowPreview: true})
	}
	artifact, err := hostedskills.Sync(ctx, options)
	if err != nil {
		return err
	}
	return printResult(cmd, artifact, fmt.Sprintf(
		"Hosted Skill artifacts synchronized: service=%s mode=%s count=%d; provider registration and runtime verification remain application responsibilities",
		workspace.Selected.ServiceName, cfg.Mode, len(artifact.Manifest.Skills),
	))
}

func validateHostedSkillArtifacts(workspace hosted.Workspace, endpoint string) (*hostedskills.Artifact, error) {
	workspace.Selected.ProjectEndpoint = endpoint
	return hosted.ValidateSkillArtifacts(workspace)
}
