// Package foundryid provides strongly validated parsing and derivation for
// Microsoft.CognitiveServices Foundry resource IDs (projects and accounts).
//
// All callers should use this package instead of inline regex parsing.
package foundryid

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// uuidPattern matches a lowercase canonical UUID.
var uuidPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

// ProjectID represents a parsed Foundry project resource ID.
type ProjectID struct {
	SubscriptionID string // lowercase UUID
	ResourceGroup  string // original casing
	AccountName    string // original casing
	ProjectName    string // original casing
}

// AccountID represents a parsed Foundry account resource ID.
type AccountID struct {
	SubscriptionID string // lowercase UUID
	ResourceGroup  string // original casing
	AccountName    string // original casing
}

// RAIPolicyID represents a parsed Foundry account RAI policy resource ID.
type RAIPolicyID struct {
	SubscriptionID string
	ResourceGroup  string
	AccountName    string
	PolicyName     string
}

// ParseProjectID parses and validates a Microsoft.CognitiveServices project resource ID.
func ParseProjectID(raw string) (ProjectID, error) {
	if err := validateNoControlChars(raw); err != nil {
		return ProjectID{}, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ProjectID{}, fmt.Errorf("project resource ID must not be empty")
	}
	if !strings.HasPrefix(raw, "/") {
		return ProjectID{}, fmt.Errorf("project resource ID must start with /")
	}

	// Normalize: split on / and skip the leading empty string.
	parts := strings.Split(raw, "/")
	if parts[0] == "" {
		parts = parts[1:]
	}

	// Expected: subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.CognitiveServices/accounts/<acct>/projects/<proj>
	// That is 10 segments.
	if len(parts) != 10 {
		if len(parts) == 8 &&
			strings.EqualFold(parts[0], "subscriptions") &&
			strings.EqualFold(parts[2], "resourceGroups") &&
			strings.EqualFold(parts[4], "providers") &&
			strings.EqualFold(parts[5], "Microsoft.CognitiveServices") &&
			strings.EqualFold(parts[6], "accounts") {
			return ProjectID{}, fmt.Errorf(
				"the supplied value is a Foundry account resource ID; append /projects/<project> to identify the Foundry project",
			)
		}
		return ProjectID{}, fmt.Errorf(
			"project resource ID must have exactly 10 path segments (/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.CognitiveServices/accounts/<acct>/projects/<proj>); got %d",
			len(parts),
		)
	}
	if !strings.EqualFold(parts[0], "subscriptions") {
		return ProjectID{}, fmt.Errorf("project resource ID segment 1 must be 'subscriptions', got %q", parts[0])
	}
	sub := strings.ToLower(parts[1])
	if !uuidPattern.MatchString(sub) {
		return ProjectID{}, fmt.Errorf("project resource ID subscription %q is not a valid UUID", parts[1])
	}
	if !strings.EqualFold(parts[2], "resourceGroups") {
		return ProjectID{}, fmt.Errorf("project resource ID segment 3 must be 'resourceGroups', got %q", parts[2])
	}
	rg := parts[3]
	if err := validateSegment(rg, "resource group"); err != nil {
		return ProjectID{}, err
	}
	if !strings.EqualFold(parts[4], "providers") {
		return ProjectID{}, fmt.Errorf("project resource ID segment 5 must be 'providers', got %q", parts[4])
	}
	if !strings.EqualFold(parts[5], "Microsoft.CognitiveServices") {
		return ProjectID{}, fmt.Errorf("project resource ID provider must be 'Microsoft.CognitiveServices', got %q", parts[5])
	}
	if !strings.EqualFold(parts[6], "accounts") {
		return ProjectID{}, fmt.Errorf("project resource ID segment 7 must be 'accounts', got %q", parts[6])
	}
	acct := parts[7]
	if err := validateSegment(acct, "account name"); err != nil {
		return ProjectID{}, err
	}
	if !strings.EqualFold(parts[8], "projects") {
		return ProjectID{}, fmt.Errorf("project resource ID segment 9 must be 'projects', got %q", parts[8])
	}
	proj := parts[9]
	if err := validateSegment(proj, "project name"); err != nil {
		return ProjectID{}, err
	}

	return ProjectID{
		SubscriptionID: sub,
		ResourceGroup:  rg,
		AccountName:    acct,
		ProjectName:    proj,
	}, nil
}

// ParseAccountID parses and validates a Microsoft.CognitiveServices account resource ID.
func ParseAccountID(raw string) (AccountID, error) {
	if err := validateNoControlChars(raw); err != nil {
		return AccountID{}, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AccountID{}, fmt.Errorf("account resource ID must not be empty")
	}
	if !strings.HasPrefix(raw, "/") {
		return AccountID{}, fmt.Errorf("account resource ID must start with /")
	}

	parts := strings.Split(raw, "/")
	if parts[0] == "" {
		parts = parts[1:]
	}

	// Expected: subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.CognitiveServices/accounts/<acct>
	// That is 8 segments.
	if len(parts) != 8 {
		return AccountID{}, fmt.Errorf(
			"account resource ID must have exactly 8 path segments (/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.CognitiveServices/accounts/<acct>); got %d",
			len(parts),
		)
	}
	if !strings.EqualFold(parts[0], "subscriptions") {
		return AccountID{}, fmt.Errorf("account resource ID segment 1 must be 'subscriptions', got %q", parts[0])
	}
	sub := strings.ToLower(parts[1])
	if !uuidPattern.MatchString(sub) {
		return AccountID{}, fmt.Errorf("account resource ID subscription %q is not a valid UUID", parts[1])
	}
	if !strings.EqualFold(parts[2], "resourceGroups") {
		return AccountID{}, fmt.Errorf("account resource ID segment 3 must be 'resourceGroups', got %q", parts[2])
	}
	rg := parts[3]
	if err := validateSegment(rg, "resource group"); err != nil {
		return AccountID{}, err
	}
	if !strings.EqualFold(parts[4], "providers") {
		return AccountID{}, fmt.Errorf("account resource ID segment 5 must be 'providers', got %q", parts[4])
	}
	if !strings.EqualFold(parts[5], "Microsoft.CognitiveServices") {
		return AccountID{}, fmt.Errorf("account resource ID provider must be 'Microsoft.CognitiveServices', got %q", parts[5])
	}
	if !strings.EqualFold(parts[6], "accounts") {
		return AccountID{}, fmt.Errorf("account resource ID segment 7 must be 'accounts', got %q", parts[6])
	}
	acct := parts[7]
	if err := validateSegment(acct, "account name"); err != nil {
		return AccountID{}, err
	}

	return AccountID{
		SubscriptionID: sub,
		ResourceGroup:  rg,
		AccountName:    acct,
	}, nil
}

// ParseRAIPolicyID parses and validates a Microsoft.CognitiveServices RAI policy resource ID.
func ParseRAIPolicyID(raw string) (RAIPolicyID, error) {
	if err := validateNoControlChars(raw); err != nil {
		return RAIPolicyID{}, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RAIPolicyID{}, fmt.Errorf("RAI policy resource ID must not be empty")
	}
	if !strings.HasPrefix(raw, "/") {
		return RAIPolicyID{}, fmt.Errorf("RAI policy resource ID must start with /")
	}
	parts := strings.Split(raw, "/")
	if parts[0] == "" {
		parts = parts[1:]
	}
	if len(parts) != 10 {
		return RAIPolicyID{}, fmt.Errorf(
			"RAI policy resource ID must have exactly 10 path segments (/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.CognitiveServices/accounts/<acct>/raiPolicies/<policy>); got %d",
			len(parts),
		)
	}
	if !strings.EqualFold(parts[0], "subscriptions") {
		return RAIPolicyID{}, fmt.Errorf("RAI policy resource ID segment 1 must be 'subscriptions', got %q", parts[0])
	}
	subscriptionID := strings.ToLower(parts[1])
	if !uuidPattern.MatchString(subscriptionID) {
		return RAIPolicyID{}, fmt.Errorf("RAI policy resource ID subscription %q is not a valid UUID", parts[1])
	}
	if !strings.EqualFold(parts[2], "resourceGroups") {
		return RAIPolicyID{}, fmt.Errorf("RAI policy resource ID segment 3 must be 'resourceGroups', got %q", parts[2])
	}
	if err := validateSegment(parts[3], "resource group"); err != nil {
		return RAIPolicyID{}, err
	}
	if !strings.EqualFold(parts[4], "providers") {
		return RAIPolicyID{}, fmt.Errorf("RAI policy resource ID segment 5 must be 'providers', got %q", parts[4])
	}
	if !strings.EqualFold(parts[5], "Microsoft.CognitiveServices") {
		return RAIPolicyID{}, fmt.Errorf("RAI policy resource ID provider must be 'Microsoft.CognitiveServices', got %q", parts[5])
	}
	if !strings.EqualFold(parts[6], "accounts") {
		return RAIPolicyID{}, fmt.Errorf("RAI policy resource ID segment 7 must be 'accounts', got %q", parts[6])
	}
	if err := validateSegment(parts[7], "account name"); err != nil {
		return RAIPolicyID{}, err
	}
	if !strings.EqualFold(parts[8], "raiPolicies") {
		return RAIPolicyID{}, fmt.Errorf("RAI policy resource ID segment 9 must be 'raiPolicies', got %q", parts[8])
	}
	if err := validateSegment(parts[9], "policy name"); err != nil {
		return RAIPolicyID{}, err
	}
	return RAIPolicyID{
		SubscriptionID: subscriptionID,
		ResourceGroup:  parts[3],
		AccountName:    parts[7],
		PolicyName:     parts[9],
	}, nil
}

// String returns the canonical resource ID string for a ProjectID.
func (p ProjectID) String() string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.CognitiveServices/accounts/%s/projects/%s",
		p.SubscriptionID, p.ResourceGroup, p.AccountName, p.ProjectName,
	)
}

// AccountID returns the parent account identifier.
func (p ProjectID) Account() AccountID {
	return AccountID{
		SubscriptionID: p.SubscriptionID,
		ResourceGroup:  p.ResourceGroup,
		AccountName:    p.AccountName,
	}
}

// AccountResourceID returns the parent account resource ID string.
func (p ProjectID) AccountResourceID() string {
	return p.Account().String()
}

// AccountEndpoint derives the AzureCloud account endpoint.
func (p ProjectID) AccountEndpoint() string {
	return "https://" + p.AccountName + ".services.ai.azure.com"
}

// ProjectEndpoint derives the AzureCloud project endpoint with URL-escaped project name.
func (p ProjectID) ProjectEndpoint() string {
	return p.AccountEndpoint() + "/api/projects/" + url.PathEscape(p.ProjectName)
}

// MatchesProjectEndpoint reports whether endpoint identifies this AzureCloud
// Foundry project.
func (p ProjectID) MatchesProjectEndpoint(endpoint string) bool {
	expected := strings.TrimSuffix(p.ProjectEndpoint(), "/")
	actual := strings.TrimSuffix(strings.TrimSpace(endpoint), "/")
	return strings.EqualFold(actual, expected)
}

// String returns the canonical resource ID string for an AccountID.
func (a AccountID) String() string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.CognitiveServices/accounts/%s",
		a.SubscriptionID, a.ResourceGroup, a.AccountName,
	)
}

// RAIPolicy returns an account-scoped RAI policy resource ID.
func (a AccountID) RAIPolicy(name string) (RAIPolicyID, error) {
	if err := validateSegment(name, "policy name"); err != nil {
		return RAIPolicyID{}, err
	}
	return RAIPolicyID{
		SubscriptionID: a.SubscriptionID,
		ResourceGroup:  a.ResourceGroup,
		AccountName:    a.AccountName,
		PolicyName:     name,
	}, nil
}

// Account returns the parent account identifier.
func (r RAIPolicyID) Account() AccountID {
	return AccountID{
		SubscriptionID: r.SubscriptionID,
		ResourceGroup:  r.ResourceGroup,
		AccountName:    r.AccountName,
	}
}

// String returns the canonical resource ID string for an RAI policy.
func (r RAIPolicyID) String() string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.CognitiveServices/accounts/%s/raiPolicies/%s",
		r.SubscriptionID, r.ResourceGroup, r.AccountName, r.PolicyName,
	)
}

// SameAccount reports whether two account-scoped identifiers select the same account.
func (r RAIPolicyID) SameAccount(account AccountID) bool {
	return strings.EqualFold(r.SubscriptionID, account.SubscriptionID) &&
		strings.EqualFold(r.ResourceGroup, account.ResourceGroup) &&
		strings.EqualFold(r.AccountName, account.AccountName)
}

// AccountEndpoint derives the AzureCloud account endpoint.
func (a AccountID) AccountEndpoint() string {
	return "https://" + a.AccountName + ".services.ai.azure.com"
}

// IsUUID returns whether the string is a valid lowercase UUID.
func IsUUID(s string) bool {
	return uuidPattern.MatchString(strings.ToLower(strings.TrimSpace(s)))
}

func validateSegment(segment, label string) error {
	if segment == "" {
		return fmt.Errorf("resource ID %s segment must not be empty", label)
	}
	if strings.TrimSpace(segment) != segment {
		return fmt.Errorf("resource ID %s segment must not have leading/trailing whitespace", label)
	}
	for _, r := range segment {
		if unicode.IsControl(r) {
			return fmt.Errorf("resource ID %s segment contains control characters", label)
		}
	}
	// Reject segments that are just dots/spaces or contain path traversal.
	if segment == "." || segment == ".." {
		return fmt.Errorf("resource ID %s segment must not be %q", label, segment)
	}
	return nil
}

func validateNoControlChars(s string) error {
	for _, r := range s {
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return fmt.Errorf("resource ID contains control characters")
		}
	}
	return nil
}
