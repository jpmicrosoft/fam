package main

import (
	"fmt"

	"foundry-agent-manager/internal/connection"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/logicapps"

	"github.com/spf13/cobra"
)

func cmdLogicAppsRegistrationPlan(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	selected, err := cmd.Flags().GetStringSlice("operation")
	if err != nil {
		return errs.Config("failed to read --operation: %v", err)
	}
	selected = nonEmptyUnique(selected)
	if len(selected) == 0 {
		return errs.Config("logicapps-registration-plan requires at least one --operation")
	}
	ctx := commandContext(cmd)
	catalog, err := connection.GetConnectorCatalogContext(
		ctx,
		getFlag(cmd, "connector-name"),
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	operations := make([]connection.ConnectorOperation, 0, len(selected))
	for _, operationName := range selected {
		operation, err := connection.GetConnectorOperationContext(
			ctx,
			&runtime.Resolved.Config.Project,
			catalog.Name,
			operationName,
			runtime.Credential,
			runtime.HTTPClient,
		)
		if err != nil {
			return err
		}
		operations = append(operations, operation)
	}
	userParameters, err := cmd.Flags().GetStringSlice("user-parameter")
	if err != nil {
		return errs.Config("failed to read --user-parameter: %v", err)
	}
	modelParameters, err := cmd.Flags().GetStringSlice("model-parameter")
	if err != nil {
		return errs.Config("failed to read --model-parameter: %v", err)
	}
	plan, err := logicapps.BuildRegistrationPlan(
		logicapps.RegistrationOptions{
			ConnectorName:      catalog.Name,
			ConnectorAuthType:  catalog.AuthType,
			ServerName:         getFlag(cmd, "mcp-server-name"),
			ServerDescription:  getFlag(cmd, "mcp-server-description"),
			LogicAppResourceID: getFlag(cmd, "logic-app-resource-id"),
			UserParameters:     nonEmptyUnique(userParameters),
			ModelParameters:    nonEmptyUnique(modelParameters),
		},
		operations,
	)
	if err != nil {
		return err
	}
	return printResult(
		cmd,
		plan,
		fmt.Sprintf(
			"Logic Apps registration plan ready: connector=%s actions=%d automated=false\n  handoff: %s",
			plan.ConnectorName,
			len(plan.Actions),
			plan.FoundryPortalURL,
		),
	)
}
