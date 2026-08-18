// Package secret resolves APIM credentials from explicit non-manifest sources.
package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/netcheck"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const keyVaultAPIVersion = "7.4"

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Options struct {
	Direct            string
	File              string
	Stdin             bool
	KeyVaultSecretURL string
	EnvironmentName   string
	Input             io.Reader
	Credential        azcore.TokenCredential
	HTTPClient        HTTPClient
	KeyVaultScope     string
	KeyVaultSuffixes  []string
}

type Value struct {
	Secret string `json:"-" yaml:"-"`
	Source string `json:"source" yaml:"source"`
}

// Resolve obtains a secret from exactly one explicit source or an environment fallback.
func Resolve(ctx context.Context, options Options) (Value, error) {
	explicit := 0
	if options.Direct != "" {
		explicit++
	}
	if options.File != "" {
		explicit++
	}
	if options.Stdin {
		explicit++
	}
	if options.KeyVaultSecretURL != "" {
		explicit++
	}
	if explicit > 1 {
		return Value{}, errs.Config(
			"APIM subscription key sources are mutually exclusive; choose one of the direct flag, file, stdin, or Key Vault",
		)
	}

	switch {
	case options.Direct != "":
		return nonEmpty(options.Direct, "command line")
	case options.File != "":
		data, err := os.ReadFile(options.File)
		if err != nil {
			return Value{}, errs.Config("failed to read APIM subscription key file %s: %v", options.File, err)
		}
		value, err := decodeTextSecret(data, "APIM subscription key file "+options.File)
		if err != nil {
			return Value{}, err
		}
		return nonEmpty(value, "file:"+options.File)
	case options.Stdin:
		if options.Input == nil {
			options.Input = os.Stdin
		}
		data, err := io.ReadAll(io.LimitReader(options.Input, 1024*1024))
		if err != nil {
			return Value{}, errs.Config("failed to read APIM subscription key from stdin: %v", err)
		}
		value, err := decodeTextSecret(data, "APIM subscription key from stdin")
		if err != nil {
			return Value{}, err
		}
		return nonEmpty(value, "stdin")
	case options.KeyVaultSecretURL != "":
		return resolveKeyVault(ctx, options)
	default:
		envName := options.EnvironmentName
		if envName == "" {
			envName = "FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY"
		}
		if value := os.Getenv(envName); value != "" {
			return Value{Secret: value, Source: "environment:" + envName}, nil
		}
		return Value{}, errs.Config(
			"apim.auth=api_key requires a subscription key from --apim-subscription-key, "+
				"--apim-subscription-key-file, --apim-subscription-key-stdin, "+
				"--apim-subscription-key-key-vault, or %s",
			envName,
		)
	}
}

func resolveKeyVault(ctx context.Context, options Options) (Value, error) {
	if options.Credential == nil {
		return Value{}, errs.Config("Key Vault secret resolution requires an Azure credential")
	}
	if options.KeyVaultScope == "" {
		return Value{}, errs.Config("Key Vault secret resolution requires a cloud-specific token scope")
	}
	validated, err := netcheck.ValidateKeyVaultURL(
		options.KeyVaultSecretURL,
		"--apim-subscription-key-key-vault",
		options.KeyVaultSuffixes,
	)
	if err != nil {
		return Value{}, err
	}
	secretURL, err := url.Parse(validated)
	if err != nil {
		return Value{}, errs.Config("invalid Key Vault secret URL: %v", err)
	}
	segments := strings.Split(strings.Trim(secretURL.Path, "/"), "/")
	if len(segments) < 2 || len(segments) > 3 || segments[0] != "secrets" || segments[1] == "" {
		return Value{}, errs.Config(
			"Key Vault reference must be https://<vault>/secrets/<name>[/<version>]",
		)
	}
	query := secretURL.Query()
	query.Set("api-version", keyVaultAPIVersion)
	secretURL.RawQuery = query.Encode()

	token, err := options.Credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{options.KeyVaultScope},
	})
	if err != nil {
		return Value{}, errs.AuthWrap(err, "failed to get Key Vault token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, secretURL.String(), nil)
	if err != nil {
		return Value{}, errs.Config("failed to create Key Vault request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	client := options.HTTPClient
	if client == nil {
		client = defaultKeyVaultClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return Value{}, errs.FoundryWrap(err, "failed to retrieve APIM subscription key from Key Vault")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Value{}, errs.FoundryWrap(err, "failed to read Key Vault response")
	}
	if resp.StatusCode != http.StatusOK {
		return Value{}, httpx.ResponseError("Key Vault", "get secret", resp, data)
	}
	var payload struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Value{}, errs.FoundryWrap(err, "failed to parse Key Vault response")
	}
	return nonEmpty(payload.Value, "key-vault:"+secretURL.Host+"/"+strings.Join(segments[:2], "/"))
}

// keyVaultRequestTimeout bounds the fallback client used when no shared HTTP
// client is supplied. http.DefaultClient has no timeout, so a hung or hostile
// endpoint could stall the deployment indefinitely while holding a token.
const keyVaultRequestTimeout = 30 * time.Second

// defaultKeyVaultClient returns a bounded, redirect-refusing client. Refusing
// redirects keeps the bearer token pinned to the validated Key Vault host.
func defaultKeyVaultClient() *http.Client {
	return &http.Client{
		Timeout: keyVaultRequestTimeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return errs.Security(
				"Key Vault redirected the secret request to %q; refusing to follow it with a bearer token",
				req.URL.Redacted(),
			)
		},
	}
}

func nonEmpty(value, source string) (Value, error) {
	if value == "" {
		return Value{}, errs.Config("APIM subscription key resolved from %s is empty", source)
	}
	return Value{Secret: value, Source: source}, nil
}

func trimLineEndings(value string) string {
	return strings.TrimRight(value, "\r\n")
}

func decodeTextSecret(data []byte, source string) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}),
		bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		return "", errs.Config("%s is UTF-16; save or pipe the secret as UTF-8", source)
	case bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}):
		data = data[3:]
	}
	if !utf8.Valid(data) {
		return "", errs.Config("%s is not valid UTF-8", source)
	}
	return trimLineEndings(string(data)), nil
}

// RedactedDescription returns a safe source description for receipts and output.
func (v Value) RedactedDescription() string {
	if v.Source == "" {
		return ""
	}
	return fmt.Sprintf("resolved from %s", v.Source)
}
