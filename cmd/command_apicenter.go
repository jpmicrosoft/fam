package main

import (
	"fmt"

	"foundry-agent-manager/internal/apicenter"
	errs "foundry-agent-manager/internal/errors"

	"github.com/spf13/cobra"
)

func cmdAPICenterList(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return err
	}
	credential, err := newCredential(cmd, resolved.Config.Cloud)
	if err != nil {
		return err
	}
	result, err := apicenter.QueryRegistryContext(
		commandContext(cmd),
		getFlag(cmd, "api-center-endpoint"),
		getFlag(cmd, "search"),
		getFlag(cmd, "api-center-token-scope"),
		credential,
		newHTTPClient(cmd),
	)
	if err != nil {
		return err
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf(
			"API Center registry servers: matches=%d authentication=%s",
			result.Matches,
			apicenter.DescribeAuthentication(getFlag(cmd, "api-center-token-scope")),
		),
	)
}

func cmdAPICenterShow(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return err
	}
	credential, err := newCredential(cmd, resolved.Config.Cloud)
	if err != nil {
		return err
	}
	result, err := apicenter.QueryRegistryContext(
		commandContext(cmd),
		getFlag(cmd, "api-center-endpoint"),
		"",
		getFlag(cmd, "api-center-token-scope"),
		credential,
		newHTTPClient(cmd),
	)
	if err != nil {
		return err
	}
	name := getFlag(cmd, "server")
	matches := apicenter.FindNamedRecords(result.Payload, name)
	if len(matches) == 0 {
		return errs.NotFound("API Center registry server %q was not found", name)
	}
	if len(matches) > 1 {
		return errs.Conflict(
			"API Center registry returned %d exact identity matches for %q",
			len(matches),
			name,
		)
	}
	return printResult(
		cmd,
		map[string]interface{}{
			"endpoint":      result.Endpoint,
			"authenticated": result.Authenticated,
			"server":        matches[0],
		},
		fmt.Sprintf("API Center registry server: %s", name),
	)
}
