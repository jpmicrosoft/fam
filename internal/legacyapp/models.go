// Package legacyapp implements explicit ARM compatibility operations for
// legacy Foundry Agent Applications.
package legacyapp

import (
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

const (
	// APIVersion is the latest ARM preview accepted for Agent Applications.
	APIVersion = "2026-05-15-preview"

	DeploymentTypeManaged = "Managed"
	ProtocolResponses     = "Responses"
	ProtocolVersionV1     = "v1"
	RoutingProtocolFixed  = "FixedRatio"
)

// HTTPClient abstracts net/http for deterministic tests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Options selects one legacy application and deployment in one ARM cloud.
type Options struct {
	SubscriptionID  string
	ResourceGroup   string
	AccountName     string
	ProjectName     string
	ApplicationName string
	DeploymentName  string
	ARMEndpoint     string
	ARMScope        string
	Credential      azcore.TokenCredential
	HTTPClient      HTTPClient
	PollInterval    time.Duration
	MaxPollAttempts int
}

// ApplicationMetadata contains the writable application metadata managed here.
type ApplicationMetadata struct {
	DisplayName string            `json:"displayName,omitempty"`
	Description string            `json:"description,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// AgentReference identifies an agent represented by an application.
type AgentReference struct {
	AgentID   string `json:"agentId,omitempty"`
	AgentName string `json:"agentName,omitempty"`
}

// VersionedAgentReference identifies one immutable agent version.
type VersionedAgentReference struct {
	AgentID      string `json:"agentId,omitempty"`
	AgentName    string `json:"agentName,omitempty"`
	AgentVersion string `json:"agentVersion,omitempty"`
}

// ProtocolVersion identifies a protocol exposed by a deployment.
type ProtocolVersion struct {
	Protocol string `json:"protocol,omitempty"`
	Version  string `json:"version,omitempty"`
}

// TrafficRoutingRule assigns a percentage of application traffic.
type TrafficRoutingRule struct {
	DeploymentID      string `json:"deploymentId,omitempty"`
	Description       string `json:"description,omitempty"`
	RuleID            string `json:"ruleId,omitempty"`
	TrafficPercentage int    `json:"trafficPercentage"`
}

// TrafficRoutingPolicy is the application's fixed-ratio routing configuration.
type TrafficRoutingPolicy struct {
	Protocol string               `json:"protocol,omitempty"`
	Rules    []TrafficRoutingRule `json:"rules,omitempty"`
}

// ApplicationProperties is the explicit application state returned by ARM.
type ApplicationProperties struct {
	ApplicationMetadata
	Agents               []AgentReference      `json:"agents,omitempty"`
	BaseURL              string                `json:"baseUrl,omitempty"`
	TrafficRoutingPolicy *TrafficRoutingPolicy `json:"trafficRoutingPolicy,omitempty"`
	ProvisioningState    string                `json:"provisioningState,omitempty"`
}

// ApplicationState describes a legacy Agent Application.
type ApplicationState struct {
	Exists     bool                  `json:"exists"`
	ID         string                `json:"id,omitempty"`
	Name       string                `json:"name,omitempty"`
	ETag       string                `json:"etag,omitempty"`
	Properties ApplicationProperties `json:"properties,omitempty"`
}

// ManagedDeploymentSpec is the desired Managed Responses v1 deployment.
type ManagedDeploymentSpec struct {
	AgentID      string
	AgentName    string
	AgentVersion string
	DisplayName  string
	Description  string
	Tags         map[string]string
}

// DeploymentProperties is the explicit deployment state returned by ARM.
type DeploymentProperties struct {
	DeploymentType    string                    `json:"deploymentType,omitempty"`
	DeploymentID      string                    `json:"deploymentId,omitempty"`
	DisplayName       string                    `json:"displayName,omitempty"`
	Description       string                    `json:"description,omitempty"`
	Tags              map[string]string         `json:"tags,omitempty"`
	Agents            []VersionedAgentReference `json:"agents,omitempty"`
	Protocols         []ProtocolVersion         `json:"protocols,omitempty"`
	State             string                    `json:"state,omitempty"`
	ProvisioningState string                    `json:"provisioningState,omitempty"`
}

// DeploymentState describes one child agentDeployment.
type DeploymentState struct {
	Exists     bool                 `json:"exists"`
	ID         string               `json:"id,omitempty"`
	Name       string               `json:"name,omitempty"`
	ETag       string               `json:"etag,omitempty"`
	Properties DeploymentProperties `json:"properties,omitempty"`
}

// Change reports the outcome of a GET-before-PUT reconciliation.
type Change string

const (
	ChangeNone    Change = "none"
	ChangeCreated Change = "created"
	ChangeUpdated Change = "updated"
)

// ApplicationResult is the result of application reconciliation.
type ApplicationResult struct {
	Change Change
	State  ApplicationState
}

// DeploymentResult is the result of deployment reconciliation.
type DeploymentResult struct {
	Change Change
	State  DeploymentState
}

// RoutingResult is the result of routing all traffic to the selected deployment.
type RoutingResult struct {
	Change       Change
	DeploymentID string
	State        ApplicationState
}

// StatusResult contains application and deployment state for explicit legacy commands.
type StatusResult struct {
	Application ApplicationState
	Deployment  DeploymentState
}
