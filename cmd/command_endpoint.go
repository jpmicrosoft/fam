package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type endpointResult struct {
	Action                string                        `json:"action" yaml:"action"`
	Agent                 string                        `json:"agent" yaml:"agent"`
	Changed               bool                          `json:"changed" yaml:"changed"`
	LatestVersion         string                        `json:"latestVersion,omitempty" yaml:"latestVersion,omitempty"`
	ActiveVersions        []string                      `json:"activeVersions,omitempty" yaml:"activeVersions,omitempty"`
	SelectorMode          string                        `json:"selectorMode,omitempty" yaml:"selectorMode,omitempty"`
	InstanceIdentity      *foundry.AgentIdentity        `json:"instanceIdentity,omitempty" yaml:"instanceIdentity,omitempty"`
	ProtocolConfiguration foundry.ProtocolConfiguration `json:"protocolConfiguration,omitempty" yaml:"protocolConfiguration,omitempty"`
	AuthorizationSchemes  []foundry.AuthorizationScheme `json:"authorizationSchemes,omitempty" yaml:"authorizationSchemes,omitempty"`
	AgentCard             *foundry.AgentCard            `json:"agentCard,omitempty" yaml:"agentCard,omitempty"`
	Receipt               string                        `json:"receipt,omitempty" yaml:"receipt,omitempty"`
}

func cmdEndpointShow(cmd *cobra.Command, _ []string) error {
	runtime, err := lifecycleClient(cmd)
	if err != nil {
		return err
	}
	agent, err := runtime.client.GetAgentContext(commandContext(cmd), runtime.cfg.Agent.Name)
	if err != nil {
		return err
	}
	if agent == nil {
		return errs.NotFound("agent %q was not found", runtime.cfg.Agent.Name)
	}
	result, err := endpointResultFromAgent("show", agent)
	if err != nil {
		return err
	}
	return printResult(cmd, result, fmt.Sprintf(
		"agent endpoint: name=%s active=%s latest=%s selector=%s protocols=%s authorization=%s",
		result.Agent,
		emptyValue(strings.Join(result.ActiveVersions, ",")),
		emptyValue(result.LatestVersion),
		result.SelectorMode,
		emptyValue(strings.Join(protocolNames(result.ProtocolConfiguration), ",")),
		emptyValue(strings.Join(authorizationNames(result.AuthorizationSchemes), ",")),
	))
}

func cmdEndpointConfigure(cmd *cobra.Command, _ []string) error {
	runtime, err := lifecycleClient(cmd)
	if err != nil {
		return err
	}
	if !runtime.cfg.Endpoint.Configured {
		return errs.Config("manifest endpoint configuration is required")
	}
	agent, err := runtime.client.GetAgentContext(commandContext(cmd), runtime.cfg.Agent.Name)
	if err != nil {
		return err
	}
	if agent == nil {
		return errs.NotFound("agent %q was not found", runtime.cfg.Agent.Name)
	}
	before := agent.VersionSelectorResolution()
	if before.IsMalformed() {
		return errs.Foundry(
			"agent %q has a malformed version selector: %s",
			runtime.cfg.Agent.Name,
			before.Problem,
		)
	}
	patch := endpointPatch(runtime.cfg, agent)
	changed := !endpointPatchMatches(agent, patch)
	path, err := releaseReceiptPath(cmd, runtime, "endpoint-configure")
	if err != nil {
		return err
	}
	store, err := newManagedOperationStore(
		cmd,
		path,
		"endpoint-configure",
		runtime.cfg.Cloud.Name,
		receipt.ManifestReference{
			Path: runtime.resolved.ManifestPath,
			Hash: runtime.resolved.ManifestHash,
		},
		receipt.ResourceReference{Name: runtime.cfg.Project.Name, Endpoint: runtime.endpoint},
		runtime.cfg.Agent.Name,
	)
	if err != nil {
		return err
	}
	store.Receipt.Agent.ID = agent.ID
	store.Receipt.Agent.LatestVersionBefore = agent.Versions.Latest.Version
	store.Receipt.Agent.ActiveVersionBefore = strings.Join(before.ActiveVersions, ",")
	store.Receipt.Agent.SelectorBefore = selectorReceipt(before)
	store.Receipt.Agent.Changed = changed
	if !changed {
		store.Receipt.Agent.LatestVersionAfter = agent.Versions.Latest.Version
		store.Receipt.Agent.ActiveVersionAfter = strings.Join(before.ActiveVersions, ",")
		store.Receipt.Agent.SelectorAfter = selectorReceipt(before)
		if err := store.Complete("unchanged", nil); err != nil {
			return err
		}
		result, err := endpointResultFromAgent("configure", agent)
		if err != nil {
			return err
		}
		result.Receipt = store.Path
		return printResult(cmd, result, fmt.Sprintf(
			"agent endpoint unchanged: name=%s\n  receipt: %s",
			result.Agent,
			result.Receipt,
		))
	}
	if err := store.AddStep(
		"endpoint-configure",
		"started",
		"updating protocols, authorization schemes, and optional agent card",
	); err != nil {
		return err
	}
	patchErr := runtime.client.PatchAgentDetailsContext(
		commandContext(cmd),
		runtime.cfg.Agent.Name,
		patch,
	)
	if patchErr != nil && !errs.IsAmbiguousMutation(patchErr) {
		_ = store.Complete("failed", patchErr)
		return releaseFailure(store.Path, patchErr)
	}
	verified, verifyErr := runtime.client.GetAgentAfterPatchContext(
		commandContext(cmd),
		runtime.cfg.Agent.Name,
	)
	if verifyErr != nil {
		combined := errors.Join(patchErr, verifyErr)
		_ = store.Complete("unknown", combined)
		return releaseFailure(store.Path, combined)
	}
	after := verified.VersionSelectorResolution()
	if after.IsMalformed() ||
		after.Mode != before.Mode ||
		strings.Join(after.ActiveVersions, ",") != strings.Join(before.ActiveVersions, ",") ||
		!endpointPatchMatches(verified, patch) {
		mismatch := errs.Conflict(
			"agent endpoint configuration did not converge or routing changed unexpectedly",
		)
		combined := errors.Join(patchErr, mismatch)
		_ = store.Complete("unknown", combined)
		return releaseFailure(store.Path, combined)
	}
	store.Receipt.Agent.LatestVersionAfter = verified.Versions.Latest.Version
	store.Receipt.Agent.ActiveVersionAfter = strings.Join(after.ActiveVersions, ",")
	store.Receipt.Agent.SelectorAfter = selectorReceipt(after)
	details := "endpoint configuration verified without changing active routing"
	status := "succeeded"
	if patchErr != nil {
		details = "ambiguous PATCH reconciled from committed endpoint state"
		status = "succeeded-reconciled"
	}
	if err := store.AddStep("endpoint-configure", "succeeded", details); err != nil {
		return err
	}
	if err := store.Complete(status, nil); err != nil {
		return err
	}
	result, err := endpointResultFromAgent("configure", verified)
	if err != nil {
		return err
	}
	result.Changed = true
	result.Receipt = store.Path
	return printResult(cmd, result, fmt.Sprintf(
		"agent endpoint configured: name=%s protocols=%s authorization=%s\n  receipt: %s",
		result.Agent,
		strings.Join(protocolNames(result.ProtocolConfiguration), ","),
		strings.Join(authorizationNames(result.AuthorizationSchemes), ","),
		result.Receipt,
	))
}

func endpointPatch(cfg *config.ResolvedConfig, current *foundry.Agent) foundry.AgentDetailsPatch {
	protocols := foundry.NewProtocolConfiguration(cfg.Endpoint.Protocols...)
	currentSchemes := make(map[string]foundry.AuthorizationScheme)
	if current != nil && current.AgentEndpoint != nil {
		for protocol := range current.AgentEndpoint.ProtocolConfiguration {
			if !protocols.Has(protocol) {
				protocols[protocol] = jsonNull
			}
		}
		for _, scheme := range current.AgentEndpoint.AuthorizationSchemes {
			currentSchemes[scheme.Type] = scheme
		}
	}
	endpoint := &foundry.AgentEndpointConfig{
		ProtocolConfiguration: protocols,
		AuthorizationSchemes: make(
			[]foundry.AuthorizationScheme,
			0,
			len(cfg.Endpoint.AuthorizationSchemes),
		),
	}
	for _, scheme := range cfg.Endpoint.AuthorizationSchemes {
		if existing, ok := currentSchemes[scheme]; ok {
			endpoint.AuthorizationSchemes = append(endpoint.AuthorizationSchemes, existing)
			continue
		}
		endpoint.AuthorizationSchemes = append(
			endpoint.AuthorizationSchemes,
			foundry.AuthorizationScheme{Type: scheme},
		)
	}
	patch := foundry.AgentDetailsPatch{AgentEndpoint: endpoint}
	card := cfg.Endpoint.AgentCard
	if card.Version != "" || card.Description != "" || len(card.Skills) > 0 {
		agentCard := &foundry.AgentCard{
			Version:     card.Version,
			Description: card.Description,
			Skills:      make([]foundry.AgentCardSkill, 0, len(card.Skills)),
		}
		for _, skill := range card.Skills {
			agentCard.Skills = append(agentCard.Skills, foundry.AgentCardSkill{
				ID:          skill.ID,
				Name:        skill.Name,
				Description: skill.Description,
				Tags:        append([]string(nil), skill.Tags...),
				Examples:    append([]string(nil), skill.Examples...),
			})
		}
		patch.AgentCard = agentCard
	}
	return patch
}

func endpointResultFromAgent(action string, agent *foundry.Agent) (endpointResult, error) {
	selector := agent.VersionSelectorResolution()
	if selector.IsMalformed() {
		return endpointResult{}, errs.Foundry(
			"agent %q has a malformed version selector: %s",
			agent.Name,
			selector.Problem,
		)
	}
	result := endpointResult{
		Action:           action,
		Agent:            agent.Name,
		LatestVersion:    agent.Versions.Latest.Version,
		ActiveVersions:   append([]string(nil), selector.ActiveVersions...),
		SelectorMode:     string(selector.Mode),
		InstanceIdentity: agent.InstanceIdentity,
		AgentCard:        agent.AgentCard,
	}
	if agent.AgentEndpoint != nil {
		result.ProtocolConfiguration = agent.AgentEndpoint.ProtocolConfiguration
		result.AuthorizationSchemes = agent.AgentEndpoint.AuthorizationSchemes
	}
	return result, nil
}

func endpointPatchMatches(agent *foundry.Agent, patch foundry.AgentDetailsPatch) bool {
	if agent == nil {
		return false
	}
	if patch.AgentEndpoint != nil {
		if agent.AgentEndpoint == nil {
			return false
		}
		desiredProtocols := make(map[string]struct{})
		for protocol, configuration := range patch.AgentEndpoint.ProtocolConfiguration {
			if protocolPatchRemoves(configuration) {
				continue
			}
			desiredProtocols[protocol] = struct{}{}
		}
		if len(agent.AgentEndpoint.ProtocolConfiguration) != len(desiredProtocols) {
			return false
		}
		for protocol := range desiredProtocols {
			if !agent.AgentEndpoint.ProtocolConfiguration.Has(protocol) {
				return false
			}
		}
		if !authorizationSchemesMatch(
			agent.AgentEndpoint.AuthorizationSchemes,
			patch.AgentEndpoint.AuthorizationSchemes,
		) {
			return false
		}
	}
	if patch.AgentCard != nil && !reflect.DeepEqual(agent.AgentCard, patch.AgentCard) {
		return false
	}
	return true
}

var jsonNull = json.RawMessage("null")

func protocolPatchRemoves(configuration json.RawMessage) bool {
	trimmed := bytes.TrimSpace(configuration)
	return len(trimmed) == 0 || bytes.Equal(trimmed, jsonNull)
}

func authorizationSchemesMatch(actual, desired []foundry.AuthorizationScheme) bool {
	if len(actual) != len(desired) {
		return false
	}
	actualByType := make(map[string]foundry.AuthorizationScheme, len(actual))
	for _, scheme := range actual {
		if _, duplicate := actualByType[scheme.Type]; duplicate {
			return false
		}
		actualByType[scheme.Type] = scheme
	}
	for _, scheme := range desired {
		current, ok := actualByType[scheme.Type]
		if !ok || !jsonValuesEqual(current, scheme) {
			return false
		}
	}
	return true
}

func jsonValuesEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	var leftValue, rightValue any
	if json.Unmarshal(leftJSON, &leftValue) != nil ||
		json.Unmarshal(rightJSON, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func protocolNames(configuration foundry.ProtocolConfiguration) []string {
	result := make([]string, 0, len(configuration))
	for protocol := range configuration {
		result = append(result, protocol)
	}
	sort.Strings(result)
	return result
}

func authorizationNames(schemes []foundry.AuthorizationScheme) []string {
	result := make([]string, 0, len(schemes))
	for _, scheme := range schemes {
		result = append(result, scheme.Type)
	}
	sort.Strings(result)
	return result
}
