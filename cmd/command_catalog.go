package main

import (
	"fmt"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/tools"

	"github.com/spf13/cobra"
)

type cloudToolCatalogEntry struct {
	tools.CatalogEntry `yaml:",inline"`
	Available          bool   `json:"available" yaml:"available"`
	UnavailableReason  string `json:"unavailableReason,omitempty" yaml:"unavailableReason,omitempty"`
}

type remoteCatalogStatus struct {
	ProgrammaticDiscovery bool   `json:"programmaticDiscovery" yaml:"programmaticDiscovery"`
	Status                string `json:"status" yaml:"status"`
	Reason                string `json:"reason" yaml:"reason"`
}

type toolCatalogResult struct {
	Cloud         string                  `json:"cloud" yaml:"cloud"`
	Direct        []cloudToolCatalogEntry `json:"direct" yaml:"direct"`
	Toolbox       []cloudToolCatalogEntry `json:"toolbox" yaml:"toolbox"`
	HostedRuntime []cloudToolCatalogEntry `json:"hostedRuntime" yaml:"hostedRuntime"`
	RemoteCatalog remoteCatalogStatus     `json:"remoteCatalog" yaml:"remoteCatalog"`
}

func cmdToolCatalog(cmd *cobra.Command, _ []string) error {
	profile, err := azcloud.Resolve(selectedCloudName(cmd, ""))
	if err != nil {
		return err
	}
	result := toolCatalogResult{
		Cloud: profile.Name,
		RemoteCatalog: remoteCatalogStatus{
			ProgrammaticDiscovery: profile.Name == azcloud.AzureCloud,
			Status:                "preview",
			Reason:                "OAuth2 managed connector discovery is available through connector-list/connector-show; explicit Azure API Center registry metadata discovery is available through api-center-list/api-center-show.",
		},
	}
	if profile.Name != azcloud.AzureCloud {
		result.RemoteCatalog.Status = "not-available"
		result.RemoteCatalog.Reason = "Managed connector catalog discovery is not verified outside AzureCloud."
	}
	for _, entry := range tools.DirectCatalog() {
		result.Direct = append(result.Direct, catalogEntryForCloud(profile, entry, false))
	}
	for _, entry := range tools.ToolboxCatalog() {
		result.Toolbox = append(result.Toolbox, catalogEntryForCloud(profile, entry, true))
	}
	for _, entry := range tools.HostedRuntimeCatalog() {
		result.HostedRuntime = append(
			result.HostedRuntime,
			hostedCatalogEntryForCloud(profile, entry),
		)
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf(
			"tool catalog: direct=%d toolbox=%d hosted_runtime=%d remote_catalog=%s",
			len(result.Direct),
			len(result.Toolbox),
			len(result.HostedRuntime),
			result.RemoteCatalog.Status,
		),
	)
}

func catalogEntryForCloud(
	profile azcloud.Profile,
	entry tools.CatalogEntry,
	toolbox bool,
) cloudToolCatalogEntry {
	reason := profile.UnsupportedTools[entry.Type]
	if reason == "" && entry.WireType != entry.Type {
		reason = profile.UnsupportedTools[entry.WireType]
	}
	if toolbox && !profile.Capabilities.Toolboxes {
		reason = "Foundry Toolbox availability is not verified in " + profile.Name
	}
	return cloudToolCatalogEntry{
		CatalogEntry:      entry,
		Available:         reason == "",
		UnavailableReason: reason,
	}
}

func hostedCatalogEntryForCloud(
	profile azcloud.Profile,
	entry tools.CatalogEntry,
) cloudToolCatalogEntry {
	reason := profile.UnsupportedTools[entry.Type]
	if reason == "" && !profile.Capabilities.HostedAgents {
		reason = "Foundry Hosted Agent availability is not verified in " + profile.Name
	}
	return cloudToolCatalogEntry{
		CatalogEntry:      entry,
		Available:         reason == "",
		UnavailableReason: reason,
	}
}
