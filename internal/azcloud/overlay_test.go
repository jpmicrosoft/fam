package azcloud

import (
	"strings"
	"testing"
)

func TestPublicProfileKeepsAzureCloudValues(t *testing.T) {
	profile, err := Resolve(AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	if got := profile.SDK.ActiveDirectoryAuthorityHost; got != "https://login.microsoftonline.com/" {
		t.Fatalf("AzureCloud must authenticate against login.microsoftonline.com, got %q", got)
	}
	if len(profile.UnsupportedTools) != 0 {
		t.Fatalf("AzureCloud supports every implemented tool: %#v", profile.UnsupportedTools)
	}
	if len(profile.FoundryRegions) != 0 {
		t.Fatalf("AzureCloud does not pin Foundry regions: %#v", profile.FoundryRegions)
	}
	values := append([]string{
		profile.ARMEndpoint,
		profile.ARMScope,
		profile.FoundryScope,
		profile.KeyVaultScope,
		profile.MonitorIngestionScope,
		profile.FoundryPortal,
		profile.AzurePortal,
	}, profile.APIMSuffixes...)
	values = append(values, profile.StorageQueueSuffixes...)
	values = append(values, profile.MonitorIngestionSuffixes...)
	for _, value := range values {
		for _, host := range []string{
			"usgovcloudapi.net",
			"azure.us",
			"azure-api.us",
			"microsoftonline.us",
		} {
			if strings.Contains(strings.ToLower(value), host) {
				t.Errorf("AzureCloud profile value %q contains Government host %q", value, host)
			}
		}
	}
}

func TestAzureCloudBlocksGovernmentAudienceHosts(t *testing.T) {
	profile, err := Resolve(AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"azure.us", "usgovcloudapi.net", "microsoft.us"} {
		if !contains(profile.OppositeAudienceHosts, host) {
			t.Errorf("AzureCloud must block Government audience host %q: %#v", host, profile.OppositeAudienceHosts)
		}
	}
}

func TestNamesListsOnlyAzureCloud(t *testing.T) {
	names := Names()
	if len(names) != 1 || names[0] != AzureCloud {
		t.Fatalf("unexpected supported cloud names: %#v", names)
	}
}

func TestProfileStringSummarizesRoutingWithoutCredentials(t *testing.T) {
	profile, err := Resolve(AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	summary := profile.String()
	for _, want := range []string{AzureCloud, profile.ARMEndpoint, profile.FoundryScope} {
		if !strings.Contains(summary, want) {
			t.Fatalf("profile summary %q is missing %q", summary, want)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
