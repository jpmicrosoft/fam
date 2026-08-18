package agent365hosted

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSource(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestValidateModernPythonDistro(t *testing.T) {
	root := writeSource(t, map[string]string{
		"requirements.txt": "microsoft-opentelemetry\n",
		"main.py": `from microsoft.opentelemetry import use_microsoft_opentelemetry
use_microsoft_opentelemetry(enable_a365=True)`,
	})
	result, err := ValidateSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || !result.ModernDistro || result.Language != "python" {
		t.Fatalf("unexpected validation: %+v", result)
	}
}

func TestValidateLegacyPackageWarns(t *testing.T) {
	root := writeSource(t, map[string]string{
		"package.json": `{"dependencies":{"@microsoft/agents-a365-observability":"1.0.0"}}`,
		"index.ts":     `ObservabilityManager.configure(builder => builder);`,
	})
	result, err := ValidateSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || !result.LegacySDK || result.ModernDistro ||
		len(result.Warnings) == 0 {
		t.Fatalf("unexpected legacy validation: %+v", result)
	}
}

func TestValidateMissingConfiguration(t *testing.T) {
	root := writeSource(t, map[string]string{
		"agent.csproj": `<PackageReference Include="Microsoft.OpenTelemetry" />`,
	})
	result, err := ValidateSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || !result.PackageDetected || result.ConfigurationDetected {
		t.Fatalf("unexpected validation: %+v", result)
	}
}

func TestValidateDoesNotReadEnvironmentFiles(t *testing.T) {
	root := writeSource(t, map[string]string{
		".env": "microsoft-opentelemetry\nuse_microsoft_opentelemetry\n",
		"a365.generated.config.json": `{
			"agentBlueprintClientSecret":"must-not-read",
			"marker":"microsoft-opentelemetry use_microsoft_opentelemetry"
		}`,
		"main.py": "print('ready')\n",
	})
	result, err := ValidateSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.PackageDetected || result.ConfigurationDetected {
		t.Fatalf("environment file must not be evidence: %+v", result)
	}
}
