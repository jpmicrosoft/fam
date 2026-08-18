package botservice

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	errs "foundry-agent-manager/internal/errors"
)

var (
	subscriptionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	resourceGroupPattern  = regexp.MustCompile(`^[\pL\pN_.()_-]+$`)
	botNamePattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	dnsNamePattern        = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	apiVersionPattern     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:-preview)?$`)
)

func validateARMOptions(options ARMOptions) error {
	endpoint, err := url.Parse(options.ARMEndpoint)
	if err != nil {
		return errs.Config("invalid ARM endpoint: %v", err)
	}
	if options.ARMEndpoint == "" ||
		endpoint.Scheme != "https" ||
		!strings.EqualFold(endpoint.Hostname(), "management.azure.com") ||
		endpoint.Port() != "" ||
		endpoint.User != nil ||
		endpoint.RawQuery != "" ||
		endpoint.Fragment != "" ||
		(endpoint.EscapedPath() != "" && endpoint.EscapedPath() != "/") {
		return errs.Config(
			"bot service supports AzureCloud ARM endpoint %q only; got %q",
			AzureCloudARMEndpoint,
			options.ARMEndpoint,
		)
	}
	if options.ARMScope != AzureCloudARMScope {
		return errs.Config(
			"bot service supports AzureCloud ARM scope %q only; got %q",
			AzureCloudARMScope,
			options.ARMScope,
		)
	}
	if err := ValidateSubscriptionID(options.SubscriptionID); err != nil {
		return err
	}
	if err := ValidateResourceGroup(options.ResourceGroup); err != nil {
		return err
	}
	if options.Credential == nil {
		return errs.Config("bot service ARM credential must not be nil")
	}
	return nil
}

// ValidateSubscriptionID validates an Azure subscription UUID as one ARM path segment.
func ValidateSubscriptionID(value string) error {
	if !subscriptionIDPattern.MatchString(value) {
		return errs.Config("subscription ID %q must be a UUID and a single ARM path segment", value)
	}
	return nil
}

// ValidateResourceGroup validates an Azure resource-group name as one ARM path segment.
func ValidateResourceGroup(value string) error {
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

// ValidateBotName validates a Microsoft.BotService/botServices resource name.
func ValidateBotName(value string) error {
	if len(value) < 2 || len(value) > 64 || !botNamePattern.MatchString(value) || hasControl(value) {
		return errs.Config(
			"bot name %q must be 2-64 characters, start with an ASCII letter or digit, and contain only letters, digits, underscores, periods, or hyphens",
			value,
		)
	}
	return nil
}

// ParseBotServiceARMID parses and validates a botServices ARM resource ID.
func ParseBotServiceARMID(value string) (ARMID, error) {
	if value == "" || strings.ContainsAny(value, "?#\\") || hasControl(value) {
		return ARMID{}, errs.Config("invalid bot service ARM ID %q", value)
	}
	parts := strings.Split(value, "/")
	if len(parts) != 9 || parts[0] != "" ||
		!strings.EqualFold(parts[1], "subscriptions") ||
		!strings.EqualFold(parts[3], "resourceGroups") ||
		!strings.EqualFold(parts[5], "providers") ||
		!strings.EqualFold(parts[6], "Microsoft.BotService") ||
		!strings.EqualFold(parts[7], "botServices") {
		return ARMID{}, errs.Config(
			"ARM ID %q must be /subscriptions/{subscription}/resourceGroups/{group}/providers/Microsoft.BotService/botServices/{bot}",
			value,
		)
	}
	parsed := ARMID{
		SubscriptionID: parts[2],
		ResourceGroup:  parts[4],
		BotName:        parts[8],
	}
	if err := ValidateSubscriptionID(parsed.SubscriptionID); err != nil {
		return ARMID{}, err
	}
	if err := ValidateResourceGroup(parsed.ResourceGroup); err != nil {
		return ARMID{}, err
	}
	if err := ValidateBotName(parsed.BotName); err != nil {
		return ARMID{}, err
	}
	return parsed, nil
}

// ParseARMID is a concise alias for ParseBotServiceARMID.
func ParseARMID(value string) (ARMID, error) {
	return ParseBotServiceARMID(value)
}

func validateBotSpec(spec BotSpec) (BotSpec, error) {
	if err := ValidateBotName(spec.Name); err != nil {
		return BotSpec{}, err
	}
	if strings.TrimSpace(spec.DisplayName) == "" || hasControl(spec.DisplayName) {
		return BotSpec{}, errs.Config("bot display name must not be empty or contain control characters")
	}
	endpoint, err := validateMessagingEndpoint(spec.Endpoint)
	if err != nil {
		return BotSpec{}, err
	}
	if !subscriptionIDPattern.MatchString(spec.MSAAppID) {
		return BotSpec{}, errs.Config("bot msaAppId %q must be a UUID", spec.MSAAppID)
	}
	if !subscriptionIDPattern.MatchString(spec.MSAAppTenantID) {
		return BotSpec{}, errs.Config("bot msaAppTenantId %q must be a UUID", spec.MSAAppTenantID)
	}
	spec.Endpoint = endpoint
	return spec, nil
}

func validateMessagingEndpoint(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errs.Config("invalid bot messaging endpoint %q: %v", value, err)
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme != "https" || parsed.User != nil || host == "" ||
		parsed.Port() != "" || parsed.Fragment != "" ||
		parsed.Path == "" || parsed.Path == "/" {
		return "", errs.Config(
			"bot endpoint %q must be an HTTPS URL with a public DNS host and non-root path, without credentials, port, or fragment",
			value,
		)
	}
	if parsed.RawQuery != "" {
		query := parsed.Query()
		versions, ok := query["api-version"]
		if !ok || len(query) != 1 || len(versions) != 1 ||
			!apiVersionPattern.MatchString(versions[0]) {
			return "", errs.Config(
				"bot endpoint %q may contain only one non-empty api-version query parameter",
				value,
			)
		}
	}
	if net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".local") ||
		!dnsNamePattern.MatchString(host) {
		return "", errs.Config("bot endpoint host %q must be a public DNS name", host)
	}
	parsed.Host = host
	return parsed.String(), nil
}

func validateReturnedBotID(id string, options ARMOptions, botName string) error {
	parsed, err := ParseBotServiceARMID(id)
	if err != nil {
		return errs.FoundryWrap(err, "Azure returned an invalid bot service resource ID")
	}
	if !strings.EqualFold(parsed.SubscriptionID, options.SubscriptionID) ||
		!strings.EqualFold(parsed.ResourceGroup, options.ResourceGroup) ||
		!strings.EqualFold(parsed.BotName, botName) {
		return errs.Conflict(
			"Azure returned bot resource ID %q outside the configured subscription, resource group, or bot name",
			id,
		)
	}
	return nil
}

func validateReturnedChannelID(id string, options ARMOptions, botName string) error {
	suffix := "/channels/MsTeamsChannel"
	if len(id) <= len(suffix) || !strings.EqualFold(id[len(id)-len(suffix):], suffix) {
		return errs.Foundry("Azure returned invalid Teams channel resource ID %q", id)
	}
	return validateReturnedBotID(id[:len(id)-len(suffix)], options, botName)
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func expectedBotID(options ARMOptions, botName string) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.BotService/botServices/%s",
		options.SubscriptionID,
		options.ResourceGroup,
		botName,
	)
}
