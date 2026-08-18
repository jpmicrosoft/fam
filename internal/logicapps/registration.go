package logicapps

import (
	"regexp"
	"sort"
	"strings"

	"foundry-agent-manager/internal/connection"
	errs "foundry-agent-manager/internal/errors"
)

const (
	FoundryPortalURL   = "https://ai.azure.com/"
	AzurePortalURL     = "https://portal.azure.com/"
	DocumentationURL   = "https://learn.microsoft.com/en-us/azure/logic-apps/add-agent-tools-connector-actions"
	registrationReason = "Microsoft documents this non-OAuth2 connector registration as a preview Foundry/Azure portal wizard and does not publish a registration mutation API."
)

var (
	serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$`)
	logicAppIDPattern = regexp.MustCompile(
		`(?i)^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\.Web/sites/[^/]+$`,
	)
)

type RegistrationOptions struct {
	ConnectorName      string
	ConnectorAuthType  string
	ServerName         string
	ServerDescription  string
	LogicAppResourceID string
	UserParameters     []string
	ModelParameters    []string
}

type Parameter struct {
	Name     string                 `json:"name" yaml:"name"`
	Required bool                   `json:"required" yaml:"required"`
	Source   string                 `json:"source" yaml:"source"`
	Schema   map[string]interface{} `json:"schema,omitempty" yaml:"schema,omitempty"`
}

type Action struct {
	Name        string      `json:"name" yaml:"name"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
	Parameters  []Parameter `json:"parameters,omitempty" yaml:"parameters,omitempty"`
}

type RegistrationPlan struct {
	Preview                bool     `json:"preview" yaml:"preview"`
	Automated              bool     `json:"automated" yaml:"automated"`
	Reason                 string   `json:"reason" yaml:"reason"`
	ConnectorName          string   `json:"connectorName" yaml:"connectorName"`
	ConnectorAuthType      string   `json:"connectorAuthType" yaml:"connectorAuthType"`
	MCPServerName          string   `json:"mcpServerName" yaml:"mcpServerName"`
	MCPServerDescription   string   `json:"mcpServerDescription" yaml:"mcpServerDescription"`
	LogicAppResourceID     string   `json:"logicAppResourceId,omitempty" yaml:"logicAppResourceId,omitempty"`
	CreateLogicAppInPortal bool     `json:"createLogicAppInPortal" yaml:"createLogicAppInPortal"`
	Actions                []Action `json:"actions" yaml:"actions"`
	FoundryPortalURL       string   `json:"foundryPortalUrl" yaml:"foundryPortalUrl"`
	AzurePortalURL         string   `json:"azurePortalUrl" yaml:"azurePortalUrl"`
	DocumentationURL       string   `json:"documentationUrl" yaml:"documentationUrl"`
	PortalSteps            []string `json:"portalSteps" yaml:"portalSteps"`
	UserValuesRequiredInUI []string `json:"userValuesRequiredInPortal,omitempty" yaml:"userValuesRequiredInPortal,omitempty"`
}

func BuildRegistrationPlan(
	options RegistrationOptions,
	operations []connection.ConnectorOperation,
) (RegistrationPlan, error) {
	if strings.EqualFold(strings.TrimSpace(options.ConnectorAuthType), "OAuth2") {
		return RegistrationPlan{}, errs.Config(
			"connector %q uses OAuth2; use the automated connector-* lifecycle instead of the non-OAuth2 Logic Apps registration plan",
			options.ConnectorName,
		)
	}
	if !serverNamePattern.MatchString(options.ServerName) {
		return RegistrationPlan{}, errs.Config(
			"MCP server name %q must be 1-64 alphanumeric, dot, underscore, or hyphen characters and start and end with an alphanumeric character",
			options.ServerName,
		)
	}
	if strings.TrimSpace(options.ServerDescription) == "" ||
		strings.ContainsAny(options.ServerDescription, "\x00") {
		return RegistrationPlan{}, errs.Config("MCP server description is required")
	}
	if options.LogicAppResourceID != "" &&
		!logicAppIDPattern.MatchString(strings.TrimSpace(options.LogicAppResourceID)) {
		return RegistrationPlan{}, errs.Config(
			"--logic-app-resource-id must identify one Microsoft.Web/sites resource",
		)
	}
	if len(operations) == 0 {
		return RegistrationPlan{}, errs.Config("at least one connector action is required")
	}
	userSources, err := parameterReferences(options.UserParameters, "--user-parameter")
	if err != nil {
		return RegistrationPlan{}, err
	}
	modelSources, err := parameterReferences(options.ModelParameters, "--model-parameter")
	if err != nil {
		return RegistrationPlan{}, err
	}
	for reference := range userSources {
		if _, found := modelSources[reference]; found {
			return RegistrationPlan{}, errs.Config(
				"parameter %q cannot use both Model and User sources",
				reference,
			)
		}
	}

	known := make(map[string]struct{})
	actions := make([]Action, 0, len(operations))
	var userValues []string
	for _, operation := range operations {
		action := Action{
			Name:        operation.Name,
			Description: firstNonEmpty(operation.Description, operation.Summary),
		}
		required := make(map[string]struct{}, len(operation.InputsDefinition.Required))
		for _, name := range operation.InputsDefinition.Required {
			required[name] = struct{}{}
		}
		names := make([]string, 0, len(operation.InputsDefinition.Properties))
		for name := range operation.InputsDefinition.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			reference := operation.Name + "/" + name
			known[reference] = struct{}{}
			_, isRequired := required[name]
			_, useUser := userSources[reference]
			_, useModel := modelSources[reference]
			if !isRequired && !useUser && !useModel {
				continue
			}
			source := "Model"
			if useUser {
				source = "User"
				userValues = append(userValues, reference)
			}
			action.Parameters = append(action.Parameters, Parameter{
				Name:     name,
				Required: isRequired,
				Source:   source,
				Schema:   operation.InputsDefinition.Properties[name],
			})
		}
		actions = append(actions, action)
	}
	for reference := range userSources {
		if _, found := known[reference]; !found {
			return RegistrationPlan{}, errs.Config(
				"--user-parameter %q does not match a selected action parameter",
				reference,
			)
		}
	}
	for reference := range modelSources {
		if _, found := known[reference]; !found {
			return RegistrationPlan{}, errs.Config(
				"--model-parameter %q does not match a selected action parameter",
				reference,
			)
		}
	}
	sort.Strings(userValues)
	return RegistrationPlan{
		Preview:                true,
		Automated:              false,
		Reason:                 registrationReason,
		ConnectorName:          options.ConnectorName,
		ConnectorAuthType:      options.ConnectorAuthType,
		MCPServerName:          options.ServerName,
		MCPServerDescription:   options.ServerDescription,
		LogicAppResourceID:     strings.TrimSpace(options.LogicAppResourceID),
		CreateLogicAppInPortal: strings.TrimSpace(options.LogicAppResourceID) == "",
		Actions:                actions,
		FoundryPortalURL:       FoundryPortalURL,
		AzurePortalURL:         AzurePortalURL,
		DocumentationURL:       DocumentationURL,
		PortalSteps: []string{
			"Open the agent in the preview Foundry portal.",
			"Select Tools, Connect a tool, Catalog, Registry, then Logic app connectors.",
			"Select the connector and choose Create to open the Azure portal registration wizard.",
			"Enter the planned MCP server name and description, then select or create the Standard logic app.",
			"Create or sign in to the connector connection, select the planned actions, and apply the planned Model/User parameter sources.",
			"Select Register and wait for the portal to return to the Foundry agent.",
		},
		UserValuesRequiredInUI: userValues,
	}, nil
}

func parameterReferences(values []string, flag string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		parts := strings.Split(value, "/")
		if len(parts) != 2 ||
			strings.TrimSpace(parts[0]) == "" ||
			strings.TrimSpace(parts[1]) == "" ||
			strings.ContainsAny(value, "\r\n\x00") {
			return nil, errs.Config(
				"%s %q must use the exact <operation>/<parameter> form",
				flag,
				value,
			)
		}
		reference := strings.TrimSpace(parts[0]) + "/" + strings.TrimSpace(parts[1])
		if _, found := result[reference]; found {
			return nil, errs.Config("%s %q was provided more than once", flag, value)
		}
		result[reference] = struct{}{}
	}
	return result, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
