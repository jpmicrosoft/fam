package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"foundry-agent-manager/internal/foundryid"
)

var (
	environmentSubscriptionPattern = regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
	)
	environmentLocationPattern  = regexp.MustCompile(`^[a-z0-9]{1,64}$`)
	environmentProjectIDPattern = regexp.MustCompile(
		`(?i)^/subscriptions/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/resourcegroups/([^/]+)/providers/microsoft\.cognitiveservices/accounts/([^/]+)/projects/([^/]+)$`,
	)
)

// EnvironmentCreateOptions describes one explicit local azd environment mutation.
type EnvironmentCreateOptions struct {
	Workspace       Workspace
	AZDPath         string
	Name            string
	SubscriptionID  string
	TenantID        string
	Location        string
	ProjectID       string
	ProjectEndpoint string
	ModelDeployment string
	Runner          Runner
	Record          Recorder
}

// EnvironmentCreateResult reports whether the selected local azd environment changed.
type EnvironmentCreateResult struct {
	Name       string          `json:"name" yaml:"name"`
	Created    bool            `json:"created" yaml:"created"`
	Reconciled bool            `json:"reconciled" yaml:"reconciled"`
	Configured []string        `json:"configured,omitempty" yaml:"configured,omitempty"`
	Commands   []CommandRecord `json:"commands" yaml:"commands"`
}

// EnsureEnvironment creates a missing local azd environment and verifies it by listing.
func EnsureEnvironment(
	ctx context.Context,
	options EnvironmentCreateOptions,
) (EnvironmentCreateResult, error) {
	result := EnvironmentCreateResult{Name: options.Name}
	if options.Runner == nil {
		return result, fmt.Errorf("%w: command runner is required", ErrCommandFailed)
	}
	if strings.TrimSpace(options.AZDPath) == "" {
		return result, ErrMissingAZD
	}
	if strings.TrimSpace(options.Workspace.Root) == "" {
		return result, fmt.Errorf("%w: workspace root is required", ErrEnvironment)
	}
	if options.Name == "" {
		return result, fmt.Errorf("%w: environment name is required", ErrEnvironment)
	}
	if err := ValidateEnvironmentName(options.Name); err != nil {
		return result, err
	}

	subscriptionID := strings.ToLower(strings.TrimSpace(options.SubscriptionID))
	if subscriptionID != "" && !environmentSubscriptionPattern.MatchString(subscriptionID) {
		return result, fmt.Errorf(
			"%w: subscription ID must be a UUID",
			ErrEnvironment,
		)
	}
	tenantID := strings.ToLower(strings.TrimSpace(options.TenantID))
	if tenantID != "" && !environmentSubscriptionPattern.MatchString(tenantID) {
		return result, fmt.Errorf(
			"%w: tenant ID must be a UUID",
			ErrEnvironment,
		)
	}
	location := strings.ToLower(strings.TrimSpace(options.Location))
	if location != "" && !environmentLocationPattern.MatchString(location) {
		return result, fmt.Errorf(
			"%w: location must be 1-64 lowercase letters or digits",
			ErrEnvironment,
		)
	}
	projectEndpoint := strings.TrimSpace(options.ProjectEndpoint)
	if projectEndpoint != "" {
		if err := validateProjectEndpoint(
			projectEndpoint,
			ReservedProjectEnv,
		); err != nil {
			return result, err
		}
	}
	projectID, err := validateEnvironmentSetting(
		"AZURE_AI_PROJECT_ID",
		options.ProjectID,
	)
	if err != nil {
		return result, err
	}
	if projectID != "" {
		match := environmentProjectIDPattern.FindStringSubmatch(projectID)
		if len(match) != 5 {
			return result, fmt.Errorf(
				"%w: project ID must be a Microsoft.CognitiveServices account project resource ID",
				ErrEnvironment,
			)
		}
		if subscriptionID != "" && !strings.EqualFold(match[1], subscriptionID) {
			return result, fmt.Errorf(
				"%w: project ID subscription must match --subscription-id",
				ErrEnvironment,
			)
		}
		if projectEndpoint != "" {
			parsedEndpoint, parseErr := url.Parse(projectEndpoint)
			if parseErr != nil {
				return result, fmt.Errorf(
					"%w: project endpoint could not be compared with the project ID: %v",
					ErrEnvironment,
					parseErr,
				)
			}
			endpointAccount := strings.TrimSuffix(
				strings.TrimSuffix(
					strings.ToLower(parsedEndpoint.Hostname()),
					".",
				),
				".services.ai.azure.com",
			)
			segments := strings.Split(
				strings.Trim(parsedEndpoint.EscapedPath(), "/"),
				"/",
			)
			endpointProject, unescapeErr := url.PathUnescape(segments[2])
			if unescapeErr != nil ||
				!strings.EqualFold(match[3], endpointAccount) ||
				!strings.EqualFold(match[4], endpointProject) {
				return result, fmt.Errorf(
					"%w: project ID account and project must match --project-endpoint",
					ErrEnvironment,
				)
			}
		}
	}
	modelDeployment, err := validateEnvironmentSetting(
		"AZURE_AI_MODEL_DEPLOYMENT_NAME",
		options.ModelDeployment,
	)
	if err != nil {
		return result, err
	}
	raiPolicyID := ""
	if options.Workspace.Selected.RAIPolicy != nil {
		project, parseErr := foundryid.ParseProjectID(projectID)
		if parseErr != nil {
			return result, fmt.Errorf(
				"%w: project ID is required to validate the Hosted Agent RAI policy: %v",
				ErrEnvironment,
				parseErr,
			)
		}
		switch {
		case options.Workspace.Selected.RAIPolicy.UnresolvedReference:
			policy, policyErr := project.Account().RAIPolicy(DefaultRAIPolicyName)
			if policyErr != nil {
				return result, fmt.Errorf("%w: could not derive the default RAI policy ID: %v", ErrEnvironment, policyErr)
			}
			raiPolicyID = policy.String()
		default:
			policy, policyErr := foundryid.ParseRAIPolicyID(options.Workspace.Selected.RAIPolicy.PolicyID)
			if policyErr != nil {
				return result, fmt.Errorf("%w: Hosted Agent RAI policy is invalid: %v", ErrEnvironment, policyErr)
			}
			if !policy.SameAccount(project.Account()) {
				return result, fmt.Errorf(
					"%w: Hosted Agent RAI policy account must match the Foundry project account",
					ErrEnvironment,
				)
			}
		}
	}

	run := func(phase string, args ...string) (Execution, error) {
		execution, record, err := execute(ctx, options.Runner, Command{
			Phase:         phase,
			Executable:    options.AZDPath,
			Args:          args,
			Directory:     options.Workspace.Root,
			Environment:   nonInteractiveEnvironment(),
			CaptureStdout: true,
			CaptureStderr: true,
		})
		result.Commands = append(result.Commands, record)
		if options.Record != nil {
			if recordErr := options.Record(record); recordErr != nil {
				return execution, recordErr
			}
		}
		return execution, err
	}

	before, err := run(
		"environment-list-before",
		"env", "list", "--output", "json", "--no-prompt",
	)
	if err != nil {
		return result, fmt.Errorf("%w: could not inspect azd environments: %v", ErrEnvironment, err)
	}
	exists, err := environmentExists(before.Stdout, options.Name)
	if err != nil {
		return result, err
	}
	if !exists {
		args := []string{"env", "new", options.Name, "--no-prompt"}
		if subscriptionID != "" {
			args = append(args, "--subscription", subscriptionID)
		}
		if location != "" {
			args = append(args, "--location", location)
		}
		_, createErr := run("environment-create", args...)
		if errors.Is(createErr, context.Canceled) ||
			errors.Is(createErr, context.DeadlineExceeded) ||
			errors.Is(createErr, ErrOutputTooLarge) {
			return result, createErr
		}

		after, verifyErr := run(
			"environment-list-after",
			"env", "list", "--output", "json", "--no-prompt",
		)
		if verifyErr != nil {
			return result, fmt.Errorf(
				"%w: azd environment creation could not be verified; rerun the command to reconcile: %v",
				ErrEnvironment,
				verifyErr,
			)
		}
		exists, err = environmentExists(after.Stdout, options.Name)
		if err != nil {
			return result, err
		}
		if !exists {
			if createErr != nil {
				return result, fmt.Errorf(
					"%w: azd env new failed and environment %q is still absent: %v",
					ErrEnvironment,
					options.Name,
					createErr,
				)
			}
			return result, fmt.Errorf(
				"%w: azd env new returned success but environment %q was not visible during verification",
				ErrEnvironment,
				options.Name,
			)
		}
		result.Created = true
		result.Reconciled = createErr != nil
	}

	settings := []struct {
		key   string
		value string
	}{
		{key: "AZURE_SUBSCRIPTION_ID", value: subscriptionID},
		{key: "AZURE_TENANT_ID", value: tenantID},
		{key: "AZURE_LOCATION", value: location},
		{key: "AZURE_AI_PROJECT_ID", value: projectID},
		{key: ReservedProjectEnv, value: projectEndpoint},
		{key: LegacyProjectEnv, value: projectEndpoint},
		{key: "AZURE_AI_MODEL_DEPLOYMENT_NAME", value: modelDeployment},
		{key: RAIPolicyEnv, value: raiPolicyID},
	}
	setArgs := []string{"env", "set"}
	for _, setting := range settings {
		if setting.value == "" {
			continue
		}
		setArgs = append(setArgs, setting.key+"="+setting.value)
		result.Configured = append(result.Configured, setting.key)
	}
	if len(result.Configured) > 0 {
		setArgs = append(
			setArgs,
			"--environment", options.Name,
			"--no-prompt",
		)
		if _, err := run("environment-configure", setArgs...); err != nil {
			return result, fmt.Errorf(
				"%w: could not configure azd environment %q: %v",
				ErrEnvironment,
				options.Name,
				err,
			)
		}
	}
	return result, nil
}

func validateEnvironmentSetting(name, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if len(value) > 1024 || strings.IndexFunc(value, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	}) >= 0 {
		return "", fmt.Errorf(
			"%w: %s must be at most 1024 printable characters",
			ErrEnvironment,
			name,
		)
	}
	return value, nil
}

func environmentExists(raw, expected string) (bool, error) {
	var environments []struct {
		Name      string `json:"Name"`
		IsDefault bool   `json:"IsDefault"`
	}
	if err := json.Unmarshal([]byte(raw), &environments); err != nil {
		return false, fmt.Errorf(
			"%w: azd env list returned invalid JSON: %v",
			ErrEnvironment,
			err,
		)
	}
	for _, environment := range environments {
		if (expected != "" && environment.Name == expected) ||
			(expected == "" && environment.IsDefault) {
			return true, nil
		}
	}
	return false, nil
}
