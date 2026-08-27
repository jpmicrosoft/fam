// Package azcloud defines the AzureCloud environment supported by Foundry Agent Manager.
package azcloud

import (
	"fmt"
	"strings"

	errs "foundry-agent-manager/internal/errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

const (
	AzureCloud        = "AzureCloud"
	AzureUSGovernment = "AzureUSGovernment"
)

// Profile contains cloud-specific identity, control-plane, and data-plane settings.
type Profile struct {
	Name                     string
	SDK                      cloud.Configuration
	ARMEndpoint              string
	ARMScope                 string
	FoundryScope             string
	GraphEndpoint            string
	GraphScope               string
	KeyVaultScope            string
	MonitorIngestionScope    string
	FoundryEndpointSuffixes  []string
	APIMSuffixes             []string
	KeyVaultSuffixes         []string
	MonitorIngestionSuffixes []string
	StorageQueueSuffixes     []string
	FoundryRegions           []string
	OppositeAudienceHosts    []string
	// TrustedAudiences positively allow-lists the managed-identity token
	// audiences that are safe by construction in this cloud. Any other audience
	// requires an explicit operator approval at preflight/deploy time.
	TrustedAudiences []string
	FoundryPortal    string
	AzurePortal      string
	UnsupportedTools map[string]string
	Capabilities     Capabilities
}

// Capabilities records cloud-specific product availability. Callers must gate
// unsupported workflows before acquiring credentials or constructing URLs.
type Capabilities struct {
	StableAgentEndpoints bool
	M365Publishing       bool
	HostedAgents         bool
	HostedAutopilot      bool
	Agent365             bool
	LegacyApplications   bool
	Toolboxes            bool
}

// Resolve returns the supported AzureCloud profile.
func Resolve(name string) (Profile, error) {
	switch normalize(name) {
	case "", "azurecloud", "public", "commercial":
		return publicProfile(), nil
	case "azureusgovernment", "usgovernment", "government", "gov":
		return Profile{}, errs.Config(
			"%s is unsupported because this release has not been qualified against a dedicated Azure Government subscription; supported value is %s",
			AzureUSGovernment,
			AzureCloud,
		)
	default:
		return Profile{}, errs.Config(
			"unsupported Azure cloud %q; supported value is %s",
			name,
			AzureCloud,
		)
	}
}

// Names returns the canonical supported cloud names.
func Names() []string {
	return []string{AzureCloud}
}

// NewDefaultCredential creates a credential configured for the selected cloud.
func NewDefaultCredential(profile Profile, tenantID string) (*azidentity.DefaultAzureCredential, error) {
	options := &azidentity.DefaultAzureCredentialOptions{
		ClientOptions: azcore.ClientOptions{Cloud: profile.SDK},
		TenantID:      tenantID,
	}
	credential, err := azidentity.NewDefaultAzureCredential(options)
	if err != nil {
		return nil, errs.AuthWrap(err, "failed to create Azure credential for %s", profile.Name)
	}
	return credential, nil
}

func normalize(name string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", ""))
}

func publicProfile() Profile {
	return Profile{
		Name:                     AzureCloud,
		SDK:                      cloud.AzurePublic,
		ARMEndpoint:              "https://management.azure.com",
		ARMScope:                 "https://management.azure.com/.default",
		FoundryScope:             "https://ai.azure.com/.default",
		GraphEndpoint:            "https://graph.microsoft.com",
		GraphScope:               "https://graph.microsoft.com/.default",
		KeyVaultScope:            "https://vault.azure.net/.default",
		MonitorIngestionScope:    "https://monitor.azure.com/.default",
		FoundryEndpointSuffixes:  []string{"services.ai.azure.com", "cognitiveservices.azure.com", "openai.azure.com"},
		APIMSuffixes:             []string{"azure-api.net"},
		KeyVaultSuffixes:         []string{"vault.azure.net"},
		MonitorIngestionSuffixes: []string{"ingest.monitor.azure.com"},
		StorageQueueSuffixes:     []string{"queue.core.windows.net"},
		OppositeAudienceHosts:    []string{"azure.us", "usgovcloudapi.net", "microsoft.us"},
		TrustedAudiences:         []string{"https://cognitiveservices.azure.com"},
		FoundryPortal:            "https://ai.azure.com",
		AzurePortal:              "https://portal.azure.com",
		UnsupportedTools:         map[string]string{},
		Capabilities: Capabilities{
			StableAgentEndpoints: true,
			M365Publishing:       true,
			HostedAgents:         true,
			HostedAutopilot:      true,
			Agent365:             true,
			LegacyApplications:   true,
			Toolboxes:            true,
		},
	}
}

// String summarizes the cloud profile without exposing credentials.
func (p Profile) String() string {
	return fmt.Sprintf("%s (ARM=%s, Foundry scope=%s)", p.Name, p.ARMEndpoint, p.FoundryScope)
}
