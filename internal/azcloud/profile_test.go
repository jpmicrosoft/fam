package azcloud

import (
	"strings"
	"testing"
)

func TestResolveAzureCloudProfile(t *testing.T) {
	for _, input := range []string{"", "AzureCloud", "public", "commercial"} {
		profile, err := Resolve(input)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", input, err)
		}
		if profile.Name != AzureCloud ||
			profile.ARMEndpoint != "https://management.azure.com" ||
			profile.ARMScope != "https://management.azure.com/.default" ||
			profile.FoundryScope != "https://ai.azure.com/.default" ||
			profile.MonitorIngestionScope != "https://monitor.azure.com/.default" {
			t.Fatalf("unexpected profile for %q: %#v", input, profile)
		}
		if len(profile.MonitorIngestionSuffixes) != 1 ||
			profile.MonitorIngestionSuffixes[0] != "ingest.monitor.azure.com" {
			t.Fatalf("unexpected Monitor ingestion suffixes for %q: %#v", input, profile)
		}
	}
}

func TestResolveRejectsAzureGovernmentAliases(t *testing.T) {
	for _, input := range []string{
		"AzureUSGovernment",
		"azure-us-government",
		"usgovernment",
		"government",
		"gov",
	} {
		_, err := Resolve(input)
		if err == nil {
			t.Fatalf("Resolve(%q) must reject unqualified Azure Government", input)
		}
		message := err.Error()
		if !strings.Contains(message, AzureUSGovernment) ||
			!strings.Contains(message, "dedicated Azure Government subscription") {
			t.Fatalf("Resolve(%q) returned an unclear error: %v", input, err)
		}
	}
}

func TestAzureCloudCapabilitiesAreComplete(t *testing.T) {
	profile, err := Resolve(AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Capabilities.StableAgentEndpoints ||
		!profile.Capabilities.M365Publishing ||
		!profile.Capabilities.HostedAgents ||
		!profile.Capabilities.HostedAutopilot ||
		!profile.Capabilities.LegacyApplications ||
		!profile.Capabilities.Toolboxes {
		t.Fatalf("AzureCloud capabilities are incomplete: %#v", profile.Capabilities)
	}
}

func TestUnknownCloudRejected(t *testing.T) {
	if _, err := Resolve("unknown-cloud"); err == nil {
		t.Fatal("expected unsupported cloud to fail")
	}
}

func TestTrustedManagedIdentityAudienceIsAzureCloudOnly(t *testing.T) {
	profile, err := Resolve(AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	const audience = "https://cognitiveservices.azure.com"
	if len(profile.TrustedAudiences) != 1 || profile.TrustedAudiences[0] != audience {
		t.Fatalf("AzureCloud must allow-list only %s, got %#v", audience, profile.TrustedAudiences)
	}
}
