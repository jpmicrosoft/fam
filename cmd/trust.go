package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/tools"
	"foundry-agent-manager/internal/trust"

	"github.com/spf13/cobra"
)

// addTrustFlags exposes the operator-controlled destination approvals.
//
// These approvals are intentionally not readable from the manifest: the
// manifest is untrusted input, so it must not be able to approve the host that
// receives this deployment's subscription key, managed-identity token, or data.
func addTrustFlags(command *cobra.Command) {
	command.Flags().StringArray(
		trust.FlagAPIMHost,
		nil,
		"Exact APIM gateway host trusted with this deployment's APIM credential "+
			"(repeatable; host or host:port; no wildcards). CI equivalent: "+trust.EnvAPIMHosts+".",
	)
	command.Flags().StringArray(
		trust.FlagToolHost,
		nil,
		"Exact external tool host (for example OpenAPI, MCP, A2A, Fabric IQ, or Work IQ) trusted with agent "+
			"requests and data (repeatable; host or host:port; no wildcards). CI equivalent: "+
			trust.EnvToolHosts+".",
	)
	command.Flags().StringArray(
		trust.FlagAudience,
		nil,
		"Exact managed-identity token audience trusted for this deployment, beyond the built-in "+
			"cloud default (repeatable; no wildcards). CI equivalent: "+trust.EnvAudiences+".",
	)
	command.Flags().String(
		trust.FlagPolicyFile,
		"",
		"Path to a JSON/YAML trust policy file with apimHosts, toolHosts, and/or audiences lists, "+
			"merged with the flags above (no wildcards). CI equivalent: "+trust.EnvPolicyFile+".",
	)
}

// trustApprovals merges repeatable flags, their environment equivalents, and
// an optional trust policy file into one set of operator approvals.
func trustApprovals(cmd *cobra.Command) (trust.Approvals, error) {
	options := trust.Options{
		APIMHosts: approvalValues(cmd, trust.FlagAPIMHost, trust.EnvAPIMHosts),
		ToolHosts: approvalValues(cmd, trust.FlagToolHost, trust.EnvToolHosts),
		Audiences: approvalValues(cmd, trust.FlagAudience, trust.EnvAudiences),
	}
	if path := policyFilePath(cmd); path != "" {
		file, err := trust.LoadPolicyFile(path)
		if err != nil {
			return trust.Approvals{}, err
		}
		options.File = &file
	}
	return trust.New(options)
}

// policyFilePath resolves --trust-file with its environment fallback, using
// the same flag-before-environment precedence as --cloud.
func policyFilePath(cmd *cobra.Command) string {
	if value := getFlag(cmd, trust.FlagPolicyFile); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(trust.EnvPolicyFile))
}

func approvalValues(cmd *cobra.Command, flag, env string) []string {
	values := append([]string(nil), getStringArrayFlag(cmd, flag)...)
	return append(values, trust.SplitList(os.Getenv(env))...)
}

// approveDestinations fails closed unless every credential-bearing or
// data-egress destination in the resolved manifest is explicitly approved.
//
// It runs before any Azure mutation and before an APIM subscription key is
// resolved, so a hostile manifest cannot cause a secret to be loaded for a
// destination the operator never approved. Cloud suffix checks still apply as
// an additional boundary; approvals never widen them.
func approveDestinations(cmd *cobra.Command, prepared *preparedAgent) ([]string, error) {
	approvals, err := trustApprovals(cmd)
	if err != nil {
		return nil, err
	}
	cfg := prepared.Resolved.Config
	var approved []string

	if prepared.APIMEnabled {
		target, err := cfg.Apim.ValidateResolvedTarget()
		if err != nil {
			return nil, err
		}
		if err := approvals.RequireAPIMHost(target, "apim.target"); err != nil {
			return nil, err
		}
		approved = append(approved, fmt.Sprintf("apim.target %s", destinationHost(target)))
		if cfg.Apim.Auth == "managed_identity" {
			if err := config.ValidateManagedIdentityAudience(
				cfg.Apim.Audience,
				cfg.Apim.BlockedAudienceHosts,
			); err != nil {
				return nil, err
			}
			if err := approvals.RequireAudience(
				cfg.Apim.Audience,
				"apim.audience",
				cfg.Cloud.TrustedAudiences,
			); err != nil {
				return nil, err
			}
			approved = append(approved, fmt.Sprintf("apim.audience %s", cfg.Apim.Audience))
		}
	}

	destinations, err := tools.Destinations(prepared.WireTools)
	if err != nil {
		return nil, err
	}
	toolApprovals, err := approveToolDestinations(cmd, cfg, destinations)
	if err != nil {
		return nil, err
	}
	approved = append(approved, toolApprovals...)
	return approved, nil
}

func approveToolDestinations(
	cmd *cobra.Command,
	cfg *config.ResolvedConfig,
	destinations []tools.Destination,
) ([]string, error) {
	approvals, err := trustApprovals(cmd)
	if err != nil {
		return nil, err
	}
	var approved []string
	for _, destination := range destinations {
		if reason := cfg.Cloud.UnsupportedTools[destination.Type]; reason != "" {
			return nil, errs.Config(
				"%s: %s tools are unavailable in %s: %s",
				destination.Field, destination.Type, cfg.Cloud.Name, reason,
			)
		}
		if tools.IsProjectToolboxEndpoint(destination.URL, cfg.Project.Endpoint) {
			approved = append(approved, fmt.Sprintf(
				"%s %s (same-project internal endpoint)",
				destination.Type,
				destinationHost(destination.URL),
			))
			continue
		}
		if err := approvals.RequireToolHost(destination.URL, destination.Field); err != nil {
			return nil, err
		}
		if destination.Audience != "" {
			if err := config.ValidateManagedIdentityAudience(
				destination.Audience,
				cfg.Cloud.OppositeAudienceHosts,
			); err != nil {
				return nil, err
			}
			if err := approvals.RequireAudience(
				destination.Audience,
				destination.Field+" audience",
				cfg.Cloud.TrustedAudiences,
			); err != nil {
				return nil, err
			}
		}
		approved = append(approved, fmt.Sprintf(
			"%s %s (auth=%s)",
			destination.Type,
			destinationHost(destination.URL),
			destination.AuthType,
		))
	}
	return approved, nil
}

// destinationHost returns a host for operator-facing output; approvals and flag
// values themselves are never echoed, so they cannot leak into receipts.
func destinationHost(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return "<invalid>"
	}
	return parsed.Host
}
