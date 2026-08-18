// Package agent365arm manages Microsoft Agent 365 data collection on a
// Microsoft.CognitiveServices/accounts resource through Azure Resource Manager.
package agent365arm

import (
	"context"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

const (
	DefaultAPIVersion = "2026-03-15-preview"
	APIVersion        = DefaultAPIVersion

	AzureCloudARMEndpoint = "https://management.azure.com"
	AzureCloudARMScope    = "https://management.azure.com/.default"
)

// Options identifies one Foundry account and its AzureCloud ARM dependencies.
type Options struct {
	SubscriptionID string
	ResourceGroup  string
	AccountName    string
	ARMEndpoint    string
	ARMScope       string
	APIVersion     string
	Credential     azcore.TokenCredential
	HTTPClient     HTTPClient
}

// HTTPClient is the transport dependency used by the ARM client.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// A365Status is the read-only licensing and tenant-consent status returned by
// ARM. Values unknown to this client are preserved as-is.
type A365Status string

const (
	A365StatusEnabled     A365Status = "Enabled"
	A365StatusDisabled    A365Status = "Disabled"
	A365StatusNotLicensed A365Status = "NotLicensed"
)

// Known reports whether the status is defined by the current ARM contract.
func (s A365Status) Known() bool {
	switch s {
	case A365StatusEnabled, A365StatusDisabled, A365StatusNotLicensed:
		return true
	default:
		return false
	}
}

// ResponseMetadata contains safe ARM response fields useful for reconciliation.
type ResponseMetadata struct {
	StatusCode      int    `json:"statusCode,omitempty"`
	RequestID       string `json:"requestId,omitempty"`
	ClientRequestID string `json:"clientRequestId,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	ETag            string `json:"etag,omitempty"`
	RetryAfter      string `json:"retryAfter,omitempty"`
}

// AccountStatus is the Agent 365 state returned for the selected Foundry account.
type AccountStatus struct {
	ID                        string           `json:"id"`
	Name                      string           `json:"name"`
	Location                  string           `json:"location"`
	ETag                      string           `json:"etag,omitempty"`
	A365LoggingEnabled        bool             `json:"a365LoggingEnabled"`
	A365LoggingEnabledPresent bool             `json:"a365LoggingEnabledPresent"`
	A365Status                A365Status       `json:"a365Status,omitempty"`
	Response                  ResponseMetadata `json:"response"`
}

// LoggingMatches reports whether ARM explicitly returned the requested flag.
func (s AccountStatus) LoggingMatches(enabled bool) bool {
	return s.A365LoggingEnabledPresent && s.A365LoggingEnabled == enabled
}

// CollectionActive distinguishes the writable logging flag from the read-only
// licensing and tenant-consent state.
func (s AccountStatus) CollectionActive() bool {
	return s.LoggingMatches(true) && s.A365Status == A365StatusEnabled
}

// PlanAction is the change a set command would perform.
type PlanAction string

const (
	PlanNoChange PlanAction = "none"
	PlanEnable   PlanAction = "enable"
	PlanDisable  PlanAction = "disable"
)

// Plan describes the current flag and the requested change.
type Plan struct {
	Current          AccountStatus `json:"current"`
	RequestedEnabled bool          `json:"requestedEnabled"`
	ChangeRequired   bool          `json:"changeRequired"`
	Action           PlanAction    `json:"action"`
}

// MutationOutcome describes how far a PATCH operation progressed.
type MutationOutcome string

const (
	MutationNotStarted         MutationOutcome = "not_started"
	MutationVerified           MutationOutcome = "verified"
	MutationRejected           MutationOutcome = "rejected"
	MutationAmbiguous          MutationOutcome = "ambiguous"
	MutationVerificationFailed MutationOutcome = "verification_failed"
)

// MutationResult preserves PATCH and verification state even when an error is
// returned, so callers can reconcile an ambiguous mutation.
type MutationResult struct {
	RequestedEnabled bool             `json:"requestedEnabled"`
	IfMatch          string           `json:"ifMatch,omitempty"`
	Outcome          MutationOutcome  `json:"outcome"`
	Patch            ResponseMetadata `json:"patch"`
	Verified         *AccountStatus   `json:"verified,omitempty"`
}

// GetStatus is a convenience wrapper for command packages.
func GetStatus(ctx context.Context, options Options) (AccountStatus, error) {
	client, err := NewClient(options)
	if err != nil {
		return AccountStatus{}, err
	}
	return client.GetStatus(ctx)
}

// PlanLogging is a convenience wrapper for command packages.
func PlanLogging(ctx context.Context, options Options, enabled bool) (Plan, error) {
	client, err := NewClient(options)
	if err != nil {
		return Plan{}, err
	}
	return client.Plan(ctx, enabled)
}

// SetLogging is a convenience wrapper for command packages.
func SetLogging(
	ctx context.Context,
	options Options,
	enabled bool,
	ifMatch string,
) (MutationResult, error) {
	client, err := NewClient(options)
	if err != nil {
		return MutationResult{
			RequestedEnabled: enabled,
			Outcome:          MutationNotStarted,
		}, err
	}
	return client.SetLogging(ctx, enabled, ifMatch)
}
