package secret

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func writeKeyFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "apim.key")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestExplicitSourcesOutrankTheEnvironmentFallback pins the documented
// precedence: an explicit source is always used, and the environment variable is
// only a fallback.
func TestExplicitSourcesOutrankTheEnvironmentFallback(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "environment-secret")
	keyFile := writeKeyFile(t, "file-secret\n")

	tests := []struct {
		name       string
		options    Options
		wantSecret string
		wantSource string
	}{
		{
			name:       "direct wins",
			options:    Options{Direct: "direct-secret"},
			wantSecret: "direct-secret",
			wantSource: "command line",
		},
		{
			name:       "file wins",
			options:    Options{File: keyFile},
			wantSecret: "file-secret",
			wantSource: "file:" + keyFile,
		},
		{
			name:       "stdin wins",
			options:    Options{Stdin: true, Input: strings.NewReader("stdin-secret\n")},
			wantSecret: "stdin-secret",
			wantSource: "stdin",
		},
		{
			name:       "environment fallback",
			options:    Options{},
			wantSecret: "environment-secret",
			wantSource: "environment:FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := Resolve(context.Background(), tt.options)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if value.Secret != tt.wantSecret || value.Source != tt.wantSource {
				t.Fatalf("unexpected resolution: %#v", value)
			}
			if strings.Contains(value.RedactedDescription(), tt.wantSecret) {
				t.Fatalf("the source description must not contain the secret: %q", value.RedactedDescription())
			}
		})
	}
}

func TestCustomEnvironmentVariableNameIsHonored(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "default-secret")
	t.Setenv("CI_APIM_KEY", "ci-secret")
	value, err := Resolve(context.Background(), Options{EnvironmentName: "CI_APIM_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if value.Secret != "ci-secret" || value.Source != "environment:CI_APIM_KEY" {
		t.Fatalf("unexpected resolution: %#v", value)
	}
}

func TestEveryPairOfExplicitSourcesIsRejected(t *testing.T) {
	keyFile := writeKeyFile(t, "file-secret")
	vaultURL := "https://vault.vault.azure.net/secrets/apim"
	sources := map[string]func(*Options){
		"direct":    func(o *Options) { o.Direct = "direct-secret" },
		"file":      func(o *Options) { o.File = keyFile },
		"stdin":     func(o *Options) { o.Stdin = true; o.Input = strings.NewReader("stdin") },
		"key vault": func(o *Options) { o.KeyVaultSecretURL = vaultURL },
	}
	names := []string{"direct", "file", "stdin", "key vault"}
	for i, first := range names {
		for _, second := range names[i+1:] {
			t.Run(first+"+"+second, func(t *testing.T) {
				options := Options{}
				sources[first](&options)
				sources[second](&options)
				_, err := Resolve(context.Background(), options)
				if err == nil || !errs.IsKind(err, "config") {
					t.Fatalf("expected mutually exclusive sources to fail, got %v", err)
				}
				if !strings.Contains(err.Error(), "mutually exclusive") {
					t.Fatalf("unexpected error: %v", err)
				}
			})
		}
	}
}

func TestMissingAndEmptySourcesAreReportedWithoutLeakingValues(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "")
	tests := map[string]Options{
		"no source at all":     {},
		"empty file":           {File: writeKeyFile(t, "\n")},
		"empty stdin":          {Stdin: true, Input: strings.NewReader("")},
		"unreadable file":      {File: filepath.Join(t.TempDir(), "missing.key")},
		"key vault without id": {KeyVaultSecretURL: "https://vault.vault.azure.net/secrets/apim"},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve(context.Background(), options)
			if err == nil || !errs.IsKind(err, "config") {
				t.Fatalf("expected a config error, got %v", err)
			}
		})
	}
}

func TestKeyVaultReferenceShapeIsValidated(t *testing.T) {
	tests := map[string]string{
		"not a vault host":  "https://attacker.example/secrets/apim",
		"http scheme":       "http://vault.vault.azure.net/secrets/apim",
		"missing segment":   "https://vault.vault.azure.net/secrets",
		"wrong collection":  "https://vault.vault.azure.net/keys/apim",
		"too many segments": "https://vault.vault.azure.net/secrets/apim/version/extra",
		"empty name":        "https://vault.vault.azure.net/secrets//version",
	}
	for name, reference := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve(context.Background(), Options{
				KeyVaultSecretURL: reference,
				Credential:        &credential{},
				HTTPClient:        &client{},
				KeyVaultScope:     "https://vault.azure.net/.default",
				KeyVaultSuffixes:  []string{"vault.azure.net"},
			})
			if err == nil {
				t.Fatalf("expected %q to be rejected", reference)
			}
		})
	}
}

func TestKeyVaultResolutionRequiresCloudContext(t *testing.T) {
	reference := "https://vault.vault.azure.net/secrets/apim"
	if _, err := Resolve(context.Background(), Options{
		KeyVaultSecretURL: reference,
		KeyVaultScope:     "https://vault.azure.net/.default",
		KeyVaultSuffixes:  []string{"vault.azure.net"},
	}); err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("a Key Vault read without a credential must fail, got %v", err)
	}
	if _, err := Resolve(context.Background(), Options{
		KeyVaultSecretURL: reference,
		Credential:        &credential{},
		KeyVaultSuffixes:  []string{"vault.azure.net"},
	}); err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("a Key Vault read without a cloud scope must fail, got %v", err)
	}
}

// failingVaultClient returns a non-200 Key Vault response.
type failingVaultClient struct {
	status int
	body   string
}

func (c *failingVaultClient) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: c.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(c.body)),
	}, nil
}

func TestKeyVaultErrorsMapToStableKinds(t *testing.T) {
	tests := []struct {
		status int
		kind   string
	}{
		{status: http.StatusForbidden, kind: "authorization"},
		{status: http.StatusNotFound, kind: "not_found"},
		{status: http.StatusTooManyRequests, kind: "transient"},
		{status: http.StatusBadRequest, kind: "foundry"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			_, err := Resolve(context.Background(), Options{
				KeyVaultSecretURL: "https://vault.vault.azure.net/secrets/apim",
				Credential:        &credential{},
				HTTPClient:        &failingVaultClient{status: tt.status, body: `{"error":"denied"}`},
				KeyVaultScope:     "https://vault.azure.net/.default",
				KeyVaultSuffixes:  []string{"vault.azure.net"},
			})
			if err == nil || !errs.IsKind(err, tt.kind) {
				t.Fatalf("expected kind %q, got %v", tt.kind, err)
			}
		})
	}
}

func TestKeyVaultSourceDescriptionOmitsTheSecretAndVersion(t *testing.T) {
	value, err := Resolve(context.Background(), Options{
		KeyVaultSecretURL: "https://vault.vault.azure.net/secrets/apim/0123456789abcdef",
		Credential:        &credential{},
		HTTPClient:        &client{},
		KeyVaultScope:     "https://vault.azure.net/.default",
		KeyVaultSuffixes:  []string{"vault.azure.net"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if value.Secret != "vault-secret" {
		t.Fatalf("unexpected secret resolution: %#v", value)
	}
	if strings.Contains(value.Source, "vault-secret") || strings.Contains(value.Source, "0123456789abcdef") {
		t.Fatalf("the source description must not carry the secret or version: %q", value.Source)
	}
	if got := value.RedactedDescription(); !strings.HasPrefix(got, "resolved from key-vault:") {
		t.Fatalf("unexpected redacted description: %q", got)
	}
	if (Value{}).RedactedDescription() != "" {
		t.Fatal("an unresolved secret has no description")
	}
}
