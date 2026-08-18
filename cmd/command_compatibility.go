package main

import (
	"fmt"
	"strings"

	"foundry-agent-manager/internal/compatibility"

	"github.com/spf13/cobra"
)

type compatibilityResult struct {
	Model         string                 `json:"model" yaml:"model"`
	Region        string                 `json:"region" yaml:"region"`
	Compatible    bool                   `json:"compatible" yaml:"compatible"`
	HasUnknowns   bool                   `json:"hasUnknowns" yaml:"hasUnknowns"`
	SourceURL     string                 `json:"sourceUrl" yaml:"sourceUrl"`
	SourceUpdated string                 `json:"sourceUpdated" yaml:"sourceUpdated"`
	Tools         []compatibility.Result `json:"tools" yaml:"tools"`
}

func cmdCompatibility(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return err
	}
	model := strings.TrimSpace(getFlag(cmd, "model-name"))
	if model == "" {
		model = resolved.Config.Agent.Model
	}
	region := strings.TrimSpace(getFlag(cmd, "region"))
	if region == "" {
		region = resolved.Config.Project.Location
	}
	result := compatibilityResult{
		Model:         model,
		Region:        region,
		Compatible:    true,
		SourceURL:     compatibility.SourceURL,
		SourceUpdated: compatibility.SourceUpdated,
	}
	for _, tool := range resolved.Config.Tools {
		check := compatibility.Check(model, region, fmt.Sprint(tool["type"]))
		result.Tools = append(result.Tools, check)
		if check.RegionStatus == compatibility.StatusUnsupported ||
			check.ModelStatus == compatibility.StatusUnsupported {
			result.Compatible = false
		}
		if check.RegionStatus == compatibility.StatusUnknown ||
			check.ModelStatus == compatibility.StatusUnknown {
			result.HasUnknowns = true
		}
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf(
			"compatibility: compatible=%t unknown=%t model=%s region=%s",
			result.Compatible,
			result.HasUnknowns,
			result.Model,
			result.Region,
		),
	)
}
