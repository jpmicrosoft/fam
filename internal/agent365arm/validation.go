package agent365arm

import (
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	errs "foundry-agent-manager/internal/errors"
)

var (
	subscriptionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	resourceGroupPattern  = regexp.MustCompile(`^[\pL\pN_.()_-]+$`)
	accountNamePattern    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	apiVersionPattern     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:-preview)?$`)
)

func validateOptions(options Options) (Options, error) {
	endpoint, err := canonicalARMEndpoint(options.ARMEndpoint)
	if err != nil {
		return Options{}, err
	}
	if options.ARMScope != AzureCloudARMScope {
		return Options{}, errs.Config(
			"Agent 365 ARM endpoint %q requires bearer scope %q; got %q",
			endpoint,
			AzureCloudARMScope,
			options.ARMScope,
		)
	}
	options.ARMEndpoint = endpoint
	if options.APIVersion == "" {
		options.APIVersion = DefaultAPIVersion
	}
	if !apiVersionPattern.MatchString(options.APIVersion) {
		return Options{}, errs.Config(
			"Agent 365 ARM api-version %q must be a date or date-preview value",
			options.APIVersion,
		)
	}
	apiDate := strings.TrimSuffix(options.APIVersion, "-preview")
	if _, err := time.Parse("2006-01-02", apiDate); err != nil {
		return Options{}, errs.Config(
			"Agent 365 ARM api-version %q does not contain a valid date",
			options.APIVersion,
		)
	}
	if err := ValidateCoordinates(
		options.SubscriptionID,
		options.ResourceGroup,
		options.AccountName,
	); err != nil {
		return Options{}, err
	}
	if options.Credential == nil {
		return Options{}, errs.Config("Agent 365 ARM credential must not be nil")
	}
	return options, nil
}

// ValidateCoordinates checks the three Foundry account ARM path segments
// without creating a credential or HTTP client.
func ValidateCoordinates(subscriptionID, resourceGroup, accountName string) error {
	if !subscriptionIDPattern.MatchString(subscriptionID) {
		return errs.Config(
			"subscription ID %q must be a UUID and one ARM path segment",
			subscriptionID,
		)
	}
	if len(resourceGroup) < 1 || len(resourceGroup) > 90 ||
		!resourceGroupPattern.MatchString(resourceGroup) ||
		strings.HasSuffix(resourceGroup, ".") ||
		hasControl(resourceGroup) {
		return errs.Config(
			"resource group %q must be 1-90 valid Azure characters, one ARM path segment, and not end in '.'",
			resourceGroup,
		)
	}
	if !accountNamePattern.MatchString(accountName) ||
		hasControl(accountName) {
		return errs.Config(
			"account name %q must be 1-64 lowercase letters, digits, or hyphens; it must begin and end with a letter or digit",
			accountName,
		)
	}
	return nil
}

func canonicalARMEndpoint(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errs.Config("invalid Agent 365 ARM endpoint %q: %v", value, err)
	}
	if value == "" ||
		parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Hostname(), "management.azure.com") ||
		parsed.Port() != "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		(parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") {
		return "", errs.Config(
			"Agent 365 supports the exact AzureCloud ARM origin %q only; got %q",
			AzureCloudARMEndpoint,
			value,
		)
	}
	return AzureCloudARMEndpoint, nil
}

func validateIfMatch(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 1024 || hasControl(value) {
		return "", errs.Config("If-Match must be at most 1024 characters without control characters")
	}
	return value, nil
}

func validateReturnedAccountID(id string, options Options) error {
	if id == "" || strings.ContainsAny(id, "?#\\") || hasControl(id) {
		return errs.Foundry("Azure returned invalid Foundry account resource ID %q", id)
	}
	parts := strings.Split(id, "/")
	expected := []string{
		"subscriptions", options.SubscriptionID,
		"resourceGroups", options.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", options.AccountName,
	}
	if len(parts) != len(expected)+1 || parts[0] != "" {
		return errs.Foundry("Azure returned invalid Foundry account resource ID %q", id)
	}
	for index, want := range expected {
		if !strings.EqualFold(parts[index+1], want) {
			return errs.Conflict(
				"Azure returned Foundry account resource ID %q outside the configured account",
				id,
			)
		}
	}
	return nil
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
