package secret

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type credential struct {
	scope string
}

func (c *credential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if len(options.Scopes) > 0 {
		c.scope = options.Scopes[0]
	}
	return azcore.AccessToken{Token: "token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type client struct {
	request *http.Request
}

func (c *client) Do(req *http.Request) (*http.Response, error) {
	c.request = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"value":"vault-secret"}`)),
	}, nil
}

func TestResolveSources(t *testing.T) {
	value, err := Resolve(context.Background(), Options{
		Stdin: true,
		Input: strings.NewReader("stdin-secret\r\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if value.Secret != "stdin-secret" || value.Source != "stdin" {
		t.Fatalf("unexpected value: %#v", value)
	}
}

func TestResolveRejectsMultipleSources(t *testing.T) {
	_, err := Resolve(context.Background(), Options{
		Direct: "one",
		Stdin:  true,
	})
	if err == nil {
		t.Fatal("expected multiple secret sources to fail")
	}
}

func TestResolveStdinStripsUTF8BOM(t *testing.T) {
	value, err := Resolve(context.Background(), Options{
		Stdin: true,
		Input: bytes.NewReader(append(
			[]byte{0xef, 0xbb, 0xbf},
			[]byte("stdin-secret\r\n")...,
		)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if value.Secret != "stdin-secret" {
		t.Fatalf("unexpected BOM-normalized secret: %q", value.Secret)
	}
}

func TestResolveRejectsUTF16Stdin(t *testing.T) {
	_, err := Resolve(context.Background(), Options{
		Stdin: true,
		Input: bytes.NewReader([]byte{0xff, 0xfe, 's', 0, 'e', 0, 'c', 0}),
	})
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected UTF-16 config error, got %v", err)
	}
}

func TestResolveKeyVaultGovernment(t *testing.T) {
	cred := &credential{}
	httpClient := &client{}
	value, err := Resolve(context.Background(), Options{
		KeyVaultSecretURL: "https://vault.vault.usgovcloudapi.net/secrets/apim/version",
		Credential:        cred,
		HTTPClient:        httpClient,
		KeyVaultScope:     "https://vault.usgovcloudapi.net/.default",
		KeyVaultSuffixes:  []string{"vault.usgovcloudapi.net"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if value.Secret != "vault-secret" {
		t.Fatalf("unexpected secret value: %#v", value)
	}
	if cred.scope != "https://vault.usgovcloudapi.net/.default" {
		t.Fatalf("unexpected scope: %s", cred.scope)
	}
	if httpClient.request.URL.Query().Get("api-version") != keyVaultAPIVersion {
		t.Fatalf("unexpected api version: %s", httpClient.request.URL)
	}
}

func TestDefaultKeyVaultClientIsBoundedAndRefusesRedirects(t *testing.T) {
	client := defaultKeyVaultClient()
	if client.Timeout <= 0 || client.Timeout > time.Minute {
		t.Fatalf("the Key Vault fallback client must have a bounded timeout, got %v", client.Timeout)
	}
	if client.CheckRedirect == nil {
		t.Fatal("the Key Vault fallback client must refuse redirects")
	}
	request, err := http.NewRequest(http.MethodGet, "https://attacker.example/secrets/apim", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected a security error for a redirected token request, got %v", err)
	}
}

func TestKeyVaultResolutionUsesTheBoundedFallbackClient(t *testing.T) {
	if http.DefaultClient.Timeout != 0 {
		t.Skip("http.DefaultClient was reconfigured by another test")
	}
	if defaultKeyVaultClient().Timeout == http.DefaultClient.Timeout {
		t.Fatal("the Key Vault fallback must not inherit http.DefaultClient's unbounded timeout")
	}
}
