// Package botservice implements Azure Bot Service control-plane operations.
package botservice

import (
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

const (
	// BotServiceAPIVersion is the pinned Microsoft.BotService/botServices API version.
	BotServiceAPIVersion = "2022-09-15"
	// ChannelsAPIVersion is the pinned Microsoft.BotService/botServices/channels API version.
	ChannelsAPIVersion = "2021-03-01"
	// ProviderAPIVersion is used only to read the provider registration state.
	ProviderAPIVersion = "2021-04-01"

	AzureCloudARMEndpoint = "https://management.azure.com"
	AzureCloudARMScope    = "https://management.azure.com/.default"
)

// HTTPClient abstracts net/http for tests and shared retry clients.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// ARMOptions contains the Azure Resource Manager dependencies and routing.
// This package intentionally supports AzureCloud only.
type ARMOptions struct {
	ARMEndpoint    string
	ARMScope       string
	SubscriptionID string
	ResourceGroup  string
	Credential     azcore.TokenCredential
	HTTPClient     HTTPClient
}

// ClientOptions is an alias retained for callers that describe these inputs as client options.
type ClientOptions = ARMOptions

// BotSpec is the desired Azure Bot resource.
type BotSpec struct {
	Name           string
	DisplayName    string
	Endpoint       string
	MSAAppID       string
	MSAAppTenantID string
	AllowUpdate    bool
}

// BotProperties is the managed subset of bot properties.
type BotProperties struct {
	DisplayName         string `json:"displayName"`
	Endpoint            string `json:"endpoint"`
	MSAAppID            string `json:"msaAppId"`
	MSAAppTenantID      string `json:"msaAppTenantId"`
	MSAAppType          string `json:"msaAppType"`
	PublicNetworkAccess string `json:"publicNetworkAccess"`
}

// SKU is an ARM resource SKU.
type SKU struct {
	Name string `json:"name"`
}

// BotState is the managed ARM representation of a bot.
type BotState struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Location   string        `json:"location"`
	Kind       string        `json:"kind"`
	SKU        SKU           `json:"sku"`
	Properties BotProperties `json:"properties"`
}

// ChannelProperties is the ARM channel envelope.
type ChannelProperties struct {
	ChannelName string `json:"channelName"`
}

// TeamsChannelState is the managed ARM representation of MsTeamsChannel.
type TeamsChannelState struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Location   string            `json:"location"`
	Properties ChannelProperties `json:"properties"`
}

// EnsureStatus reports the action performed by an ensure operation.
type EnsureStatus string

const (
	StatusCreated   EnsureStatus = "Created"
	StatusUpdated   EnsureStatus = "Updated"
	StatusUnchanged EnsureStatus = "Unchanged"
)

// EnsureResult reports the verified resource and whether it changed.
type EnsureResult struct {
	Status     EnsureStatus `json:"status"`
	ResourceID string       `json:"resourceId"`
}

// ARMID is a parsed Microsoft.BotService/botServices resource ID.
type ARMID struct {
	SubscriptionID string
	ResourceGroup  string
	BotName        string
}
