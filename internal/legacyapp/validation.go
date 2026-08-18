package legacyapp

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	errs "foundry-agent-manager/internal/errors"
)

var (
	subscriptionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	resourceGroupPattern  = regexp.MustCompile(`^[\pL\pN_.()_-]+$`)
	accountNamePattern    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	resourceNamePattern   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$`)
	versionPattern        = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,126}[A-Za-z0-9])?$`)
)

var armClouds = map[string]string{
	"https://management.azure.com": "https://management.azure.com/.default",
}

func validateOptions(options Options) (Options, error) {
	endpoint, err := canonicalARMEndpoint(options.ARMEndpoint)
	if err != nil {
		return Options{}, err
	}
	expectedScope := armClouds[endpoint]
	if options.ARMScope != expectedScope {
		return Options{}, errs.Config(
			"ARM endpoint %q requires token scope %q; got %q",
			endpoint,
			expectedScope,
			options.ARMScope,
		)
	}
	options.ARMEndpoint = endpoint

	if err := validateSubscriptionID(options.SubscriptionID); err != nil {
		return Options{}, err
	}
	if err := validateResourceGroup(options.ResourceGroup); err != nil {
		return Options{}, err
	}
	if !accountNamePattern.MatchString(options.AccountName) || hasControl(options.AccountName) {
		return Options{}, errs.Config(
			"account name %q must be 1-64 lowercase letters, digits, or hyphens; it must begin and end with a letter or digit",
			options.AccountName,
		)
	}
	for label, value := range map[string]string{
		"project name":     options.ProjectName,
		"application name": options.ApplicationName,
		"deployment name":  options.DeploymentName,
	} {
		if !resourceNamePattern.MatchString(value) || hasControl(value) {
			return Options{}, errs.Config(
				"%s %q must be 1-64 ASCII letters, digits, periods, underscores, or hyphens and must be one ARM path segment",
				label,
				value,
			)
		}
	}
	if options.Credential == nil {
		return Options{}, errs.Config("legacy application ARM credential must not be nil")
	}
	return options, nil
}

func canonicalARMEndpoint(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errs.Config("invalid ARM endpoint %q: %v", value, err)
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") {
		return "", errs.Config("ARM endpoint %q must be a supported HTTPS ARM origin", value)
	}
	endpoint := "https://" + strings.ToLower(parsed.Hostname())
	if _, ok := armClouds[endpoint]; !ok {
		return "", errs.Config("unsupported ARM endpoint %q for legacy Agent Applications", value)
	}
	return endpoint, nil
}

func validateSubscriptionID(value string) error {
	if !subscriptionIDPattern.MatchString(value) {
		return errs.Config("subscription ID %q must be a UUID and a single ARM path segment", value)
	}
	return nil
}

func validateResourceGroup(value string) error {
	if len(value) < 1 || len(value) > 90 ||
		!resourceGroupPattern.MatchString(value) ||
		strings.HasSuffix(value, ".") ||
		hasControl(value) {
		return errs.Config(
			"resource group %q must be 1-90 valid Azure characters, one ARM path segment, and not end in '.'",
			value,
		)
	}
	return nil
}

func validateManagedDeployment(spec ManagedDeploymentSpec, deploymentName string) (ManagedDeploymentSpec, error) {
	spec.AgentID = strings.TrimSpace(spec.AgentID)
	if spec.AgentID == "" || hasControl(spec.AgentID) {
		return ManagedDeploymentSpec{}, errs.Config("agent ID must be a non-empty string without control characters")
	}
	if !resourceNamePattern.MatchString(spec.AgentName) || hasControl(spec.AgentName) {
		return ManagedDeploymentSpec{}, errs.Config(
			"agent name %q must be 1-64 ASCII letters, digits, periods, underscores, or hyphens",
			spec.AgentName,
		)
	}
	if !versionPattern.MatchString(spec.AgentVersion) || hasControl(spec.AgentVersion) {
		return ManagedDeploymentSpec{}, errs.Config(
			"agent version %q must be 1-128 ASCII letters, digits, periods, underscores, or hyphens",
			spec.AgentVersion,
		)
	}
	if spec.DisplayName == "" {
		spec.DisplayName = deploymentName
	}
	if err := validateMetadata(spec.DisplayName, spec.Description, spec.Tags, "deployment"); err != nil {
		return ManagedDeploymentSpec{}, err
	}
	return spec, nil
}

func normalizeApplicationMetadata(metadata ApplicationMetadata, applicationName string) (ApplicationMetadata, error) {
	if metadata.DisplayName == "" {
		metadata.DisplayName = applicationName
	}
	if err := validateMetadata(metadata.DisplayName, metadata.Description, metadata.Tags, "application"); err != nil {
		return ApplicationMetadata{}, err
	}
	return metadata, nil
}

func validateMetadata(displayName, description string, tags map[string]string, resource string) error {
	if len(displayName) > 200 || strings.TrimSpace(displayName) == "" || hasControl(displayName) {
		return errs.Config("%s display name must be 1-200 characters without control characters", resource)
	}
	if len(description) > 1024 || hasControl(description) {
		return errs.Config("%s description must be at most 1024 characters without control characters", resource)
	}
	if len(tags) > 50 {
		return errs.Config("%s tags must contain at most 50 entries", resource)
	}
	for key, value := range tags {
		if key == "" || len(key) > 512 || len(value) > 256 || hasControl(key) || hasControl(value) {
			return errs.Config("%s tag %q has an invalid key or value", resource, key)
		}
	}
	return nil
}

func validateReturnedApplicationID(id string, options Options) error {
	expected := []string{
		"subscriptions", options.SubscriptionID,
		"resourceGroups", options.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", options.AccountName,
		"projects", options.ProjectName,
		"applications", options.ApplicationName,
	}
	return validateReturnedID(id, expected, "application")
}

func validateReturnedDeploymentID(id string, options Options) error {
	expected := []string{
		"subscriptions", options.SubscriptionID,
		"resourceGroups", options.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", options.AccountName,
		"projects", options.ProjectName,
		"applications", options.ApplicationName,
		"agentDeployments", options.DeploymentName,
	}
	return validateReturnedID(id, expected, "agent deployment")
}

func validateReturnedID(id string, expected []string, resource string) error {
	if id == "" {
		return nil
	}
	if strings.ContainsAny(id, "?#\\") || hasControl(id) {
		return errs.Foundry("Azure returned invalid %s resource ID %q", resource, id)
	}
	parts := strings.Split(id, "/")
	if len(parts) != len(expected)+1 || parts[0] != "" {
		return errs.Foundry("Azure returned invalid %s resource ID %q", resource, id)
	}
	for i, want := range expected {
		if !strings.EqualFold(parts[i+1], want) {
			return errs.Conflict(
				"Azure returned %s resource ID %q outside the configured legacy application",
				resource,
				id,
			)
		}
	}
	return nil
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func expectedResourceID(options Options, includeDeployment bool) string {
	id := fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.CognitiveServices/accounts/%s/projects/%s/applications/%s",
		options.SubscriptionID,
		options.ResourceGroup,
		options.AccountName,
		options.ProjectName,
		options.ApplicationName,
	)
	if includeDeployment {
		id += "/agentDeployments/" + options.DeploymentName
	}
	return id
}
