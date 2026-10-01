package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"foundry-agent-manager/internal/agentdiff"
	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/skills"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
)

func requireNativePromptSkills(cmd *cobra.Command) error {
	return foundry.RequireNativePromptSkills(
		getBoolFlag(cmd, "accept-preview"),
		getBoolFlag(cmd, "experimental-native-skills"),
	)
}

func validateNativePromptSkillsFlags(cmd *cobra.Command) error {
	if getBoolFlag(cmd, "experimental-native-skills") {
		return requireNativePromptSkills(cmd)
	}
	return nil
}

func newPromptSkillsClient(cmd *cobra.Command, endpoint string, cfg *config.ResolvedConfig, credential azcore.TokenCredential, client foundry.HTTPClient) *foundry.Client {
	return foundry.NewClientWithOptions(endpoint, credential, client, foundry.ClientOptions{
		Scope:                     cfg.Cloud.FoundryScope,
		AllowPreview:              cfg.Agent.RAIPolicyID != "",
		NativePromptSkillsEnabled: getBoolFlag(cmd, "experimental-native-skills"),
	})
}

type promptSkillValidation struct {
	Reference skills.Reference
	SHA256    string
}

func promptSkillsSummary(references []skills.Reference, configured bool) string {
	if !configured {
		return "unmanaged (preserve remote native references)"
	}
	if len(references) == 0 {
		return "[] (clear native references)"
	}
	names := make([]string, 0, len(references))
	for _, reference := range references {
		names = append(names, reference.Name+"@"+reference.Version)
	}
	return strings.Join(names, ", ")
}

func promptSkillsDesired(cmd *cobra.Command, remote *foundry.Agent, desired agentdiff.Desired) (agentdiff.Desired, error) {
	effective, err := agentdiff.PreserveSkills(remote, desired)
	if err != nil {
		return agentdiff.Desired{}, errs.Foundry("cannot preserve native Prompt Skills: %v", err)
	}
	if effective.ManageSkills && !getBoolFlag(cmd, "accept-preview") {
		return agentdiff.Desired{}, errs.Config("native Prompt Skills attachment, removal, or preservation requires --accept-preview")
	}
	return effective, nil
}

// Local declarations do not describe the versions receiving endpoint traffic.
// Inspect exact selected versions even when the local list is omitted or empty.
func validateSmokePromptSkills(cmd *cobra.Command, client *foundry.Client, name string) error {
	ctx := commandContext(cmd)
	agent, err := client.GetAgentContext(ctx, name)
	if err != nil {
		return err
	}
	if agent == nil {
		return errs.NotFound("agent %q was not found", name)
	}
	if agent.Name != "" && agent.Name != name {
		return errs.Foundry("cannot inspect endpoint-selected native Prompt Skills for agent %q: lookup returned a different agent", name)
	}
	versions, err := agent.EffectiveActiveVersions()
	if err != nil {
		return errs.Foundry("cannot inspect endpoint-selected native Prompt Skills for agent %q: %v", name, err)
	}
	for _, version := range versions {
		if err := validateSmokePromptSkillsVersion(cmd, client, name, version); err != nil {
			return err
		}
	}
	return nil
}

func validateSmokePromptSkillsVersion(cmd *cobra.Command, client *foundry.Client, name, version string) error {
	if strings.TrimSpace(version) == "" || version == foundry.LatestAgentVersion {
		return errs.Foundry("cannot inspect endpoint-selected native Prompt Skills for agent %q: no immutable version resolved", name)
	}
	actual, err := client.GetAgentVersionContext(commandContext(cmd), name, version)
	if err != nil {
		return err
	}
	if actual == nil || actual.Version != version || (actual.Name != "" && actual.Name != name) ||
		actual.Draft || actual.Definition == nil {
		return errs.Foundry("cannot inspect native Prompt Skills on endpoint-selected agent %q version %q: missing or mismatched immutable definition", name, version)
	}
	references, _, err := agentdiff.NativeSkills(actual.Definition)
	if err != nil {
		return errs.Foundry("invalid native Prompt Skills on endpoint-selected agent %q version %q: %v", name, version, err)
	}
	if len(references) > 0 {
		return requireNativePromptSkills(cmd)
	}
	return nil
}

// validatePromptSkillContent is read-only and shared by diff and preflight.
// Lookups and content downloads use the same validated project client.
func validatePromptSkillContent(ctx context.Context, client *foundry.Client, references []skills.Reference) ([]promptSkillValidation, error) {
	validated := make([]promptSkillValidation, 0, len(references))
	for _, reference := range references {
		if err := skills.ValidateReference(reference); err != nil {
			return nil, err
		}
		version, err := client.GetSkillVersionContext(ctx, reference.Name, reference.Version)
		if err != nil {
			return nil, err
		}
		if version == nil {
			return nil, errs.NotFound("native Prompt Skill %q version %q does not exist in the selected project; publish it separately before attachment", reference.Name, reference.Version)
		}
		if version.Version != reference.Version || (version.Name != "" && version.Name != reference.Name) {
			return nil, errs.Foundry("native Prompt Skill lookup for %s@%s returned a different name or version", reference.Name, reference.Version)
		}
		archive, err := client.DownloadSkillContext(ctx, reference.Name, reference.Version)
		if err != nil {
			return nil, err
		}
		pkg, err := skills.ReadArchive(archive)
		if err != nil {
			return nil, fmt.Errorf("native Prompt Skill %s@%s: %w", reference.Name, reference.Version, err)
		}
		if pkg.Name != reference.Name {
			return nil, errs.Config("native Prompt Skill %s@%s contains SKILL.md for %q", reference.Name, reference.Version, pkg.Name)
		}
		validated = append(validated, promptSkillValidation{Reference: reference, SHA256: pkg.SHA256})
	}
	return validated, nil
}

func verifyPromptSkillsVersion(ctx context.Context, client *foundry.Client, name, version string, desired agentdiff.Desired) error {
	if !desired.ManageSkills {
		return nil
	}
	actual, err := client.GetAgentVersionContext(ctx, name, version)
	if err != nil {
		return err
	}
	if actual == nil || actual.Version != version || (actual.Name != "" && actual.Name != name) {
		return errs.Foundry("cannot verify native Prompt Skills on exact created agent %q version %q", name, version)
	}
	references, present, err := agentdiff.NativeSkills(actual.Definition)
	if err != nil {
		return errs.Foundry("native Prompt Skills readback failed for %s version %s: %v", name, version, err)
	}
	if desired.Harness != nil && !reflect.DeepEqual(actual.Definition["harness"], desired.Harness) {
		return errs.Foundry("native Prompt Skills harness readback mismatch for %s version %s; refusing an unintended runtime change", name, version)
	}
	if present && len(desired.Skills) == 0 && len(references) == 0 {
		return nil
	}
	if !present || !reflect.DeepEqual(references, desired.Skills) {
		return errs.Foundry("native Prompt Skills readback mismatch for %s version %s; the service omitted, reordered, or rewrote the pinned references", name, version)
	}
	return nil
}
