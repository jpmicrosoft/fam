package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/cliout"
	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/project"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func getFlag(cmd *cobra.Command, name string) string {
	value, _ := flagSet(cmd, name).GetString(name)
	return value
}

func getBoolFlag(cmd *cobra.Command, name string) bool {
	value, _ := flagSet(cmd, name).GetBool(name)
	return value
}

func getFloatFlag(cmd *cobra.Command, name string) float64 {
	value, _ := flagSet(cmd, name).GetFloat64(name)
	return value
}

func getIntFlag(cmd *cobra.Command, name string) int {
	value, _ := flagSet(cmd, name).GetInt(name)
	return value
}

func getInt64Flag(cmd *cobra.Command, name string) int64 {
	value, _ := flagSet(cmd, name).GetInt64(name)
	return value
}

// getStringArrayFlag reads a repeatable flag verbatim.
//
// pflag's GetStringArray round-trips the values through CSV, which silently
// discards a lone empty value. Approval flags must be read exactly as supplied
// so an empty or malformed approval is rejected with an actionable error
// instead of vanishing and surfacing later as "destination not approved".
func getStringArrayFlag(cmd *cobra.Command, name string) []string {
	flag := flagSet(cmd, name).Lookup(name)
	if flag == nil {
		return nil
	}
	if slice, ok := flag.Value.(pflag.SliceValue); ok {
		return slice.GetSlice()
	}
	value, _ := flagSet(cmd, name).GetStringArray(name)
	return value
}

func getDurationFlag(cmd *cobra.Command, name string) time.Duration {
	value, _ := flagSet(cmd, name).GetDuration(name)
	return value
}

func flagSet(cmd *cobra.Command, name string) *pflag.FlagSet {
	if cmd.Flags().Lookup(name) != nil {
		return cmd.Flags()
	}
	if cmd.InheritedFlags().Lookup(name) != nil {
		return cmd.InheritedFlags()
	}
	if cmd.Root().PersistentFlags().Lookup(name) != nil {
		return cmd.Root().PersistentFlags()
	}
	return cmd.Flags()
}

func outputFormat(cmd *cobra.Command) (cliout.Format, error) {
	return cliout.ParseFormat(getFlag(cmd, "output"))
}

func printerFor(cmd *cobra.Command, errorsToStderr bool) (cliout.Printer, error) {
	format, err := outputFormat(cmd)
	if err != nil {
		return cliout.Printer{}, err
	}
	writer := cmd.OutOrStdout()
	if errorsToStderr {
		writer = cmd.ErrOrStderr()
	}
	return cliout.Printer{
		Format: format,
		Out:    writer,
		Quiet:  getBoolFlag(cmd, "quiet"),
	}, nil
}

func printResult(cmd *cobra.Command, value interface{}, text string) error {
	printer, err := printerFor(cmd, false)
	if err != nil {
		return errs.Config("%v", err)
	}
	if err := printer.Print(value, text); err != nil {
		return fmt.Errorf("failed to write command output: %w", err)
	}
	return nil
}

func verbosef(cmd *cobra.Command, format string, values ...interface{}) {
	if !getBoolFlag(cmd, "verbose") && !getBoolFlag(cmd, "debug") {
		return
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", values...)
}

func debugf(cmd *cobra.Command, format string, values ...interface{}) {
	if !getBoolFlag(cmd, "debug") {
		return
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "debug: "+format+"\n", values...)
}

func commandContext(cmd *cobra.Command) context.Context {
	if cmd.Context() != nil {
		return cmd.Context()
	}
	return context.Background()
}

// newHTTPClientFn and newCredentialFn are the runtime factory functions used by
// every online command. Tests override these via t.Cleanup-restored assignment
// in _test.go files to inject fakes without weakening production validation.
var newHTTPClientFn = newHTTPClientReal
var newCredentialFn = newCredentialReal

func newHTTPClient(cmd *cobra.Command) *httpx.RetryClient {
	return newHTTPClientFn(cmd)
}

func newHTTPClientReal(cmd *cobra.Command) *httpx.RetryClient {
	return newHTTPClientWithTransport(cmd, nil)
}

func newAPICenterHTTPClient(cmd *cobra.Command) *httpx.RetryClient {
	return newHTTPClientWithTransport(cmd, apiCenterTransport())
}

func newHTTPClientWithTransport(
	cmd *cobra.Command,
	transport http.RoundTripper,
) *httpx.RetryClient {
	base := &http.Client{
		Timeout:   getDurationFlag(cmd, "request-timeout"),
		Transport: transport,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return errs.Security(
				"Azure redirected an authenticated request to %q; refusing to forward the bearer token",
				req.URL.Redacted(),
			)
		},
	}
	return httpx.NewRetryClient(base, httpx.Options{
		Retries:   getIntFlag(cmd, "retry-count"),
		BaseDelay: getDurationFlag(cmd, "retry-delay"),
		MaxDelay:  30 * time.Second,
		Trace: func(event httpx.TraceEvent) {
			if !getBoolFlag(cmd, "debug") {
				return
			}
			status := "transport-error"
			if event.StatusCode != 0 {
				status = fmt.Sprintf("status=%d", event.StatusCode)
			}
			retry := ""
			if event.RetryDelay > 0 {
				retry = fmt.Sprintf(" retry-in=%s", event.RetryDelay)
			}
			debugf(
				cmd,
				"http method=%s host=%s path=%s attempt=%d/%d %s duration=%s%s",
				event.Method,
				event.Host,
				event.Path,
				event.Attempt,
				event.MaxAttempts,
				status,
				event.Duration.Round(time.Millisecond),
				retry,
			)
		},
	})
}

func apiCenterTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	tlsConfig.MinVersion = tls.VersionTLS12
	tlsConfig.Renegotiation = tls.RenegotiateOnceAsClient
	transport.TLSClientConfig = tlsConfig
	return transport
}

func newCredential(cmd *cobra.Command, profile azcloud.Profile) (azcore.TokenCredential, error) {
	return newCredentialFn(cmd, profile)
}

func newCredentialReal(cmd *cobra.Command, profile azcloud.Profile) (azcore.TokenCredential, error) {
	return azcloud.NewDefaultCredential(profile, getFlag(cmd, "tenant-id"))
}

func newFoundryClient(endpoint string, cfg *config.ResolvedConfig, cred azcore.TokenCredential, client foundry.HTTPClient) *foundry.Client {
	return foundry.NewClientWithOptions(endpoint, cred, client, foundry.ClientOptions{
		Scope:        cfg.Cloud.FoundryScope,
		AllowPreview: cfg.Agent.RAIPolicyID != "",
	})
}

func resolveProjectEndpoint(
	cmd *cobra.Command,
	cfg *config.ResolvedConfig,
	credential azcore.TokenCredential,
	httpClient project.HTTPClient,
) (string, error) {
	if cfg.Project.Endpoint != "" {
		return cfg.RequireProjectEndpoint()
	}
	if !hasProjectCoordinates(cfg.Project) {
		return cfg.RequireProjectEndpoint()
	}
	state, err := project.InspectProjectContext(
		commandContext(cmd),
		&cfg.Project,
		credential,
		httpClient,
	)
	if err != nil {
		return "", err
	}
	if !state.Exists {
		return "", errs.NotFound("Foundry project %q does not exist", cfg.Project.Name)
	}
	if err := config.ValidateProjectLocation(state.Location, cfg.Project.AllowedRegions); err != nil {
		return "", err
	}
	endpoint, err := validateProjectEndpoint(cfg, state.Endpoint)
	if err != nil {
		return "", err
	}
	cfg.Project.Endpoint = endpoint
	return endpoint, nil
}

func validateProjectEndpoint(cfg *config.ResolvedConfig, endpoint string) (string, error) {
	return netcheck.ValidateFoundryEndpointForSuffixes(
		endpoint,
		"project.endpoint",
		cfg.Cloud.FoundryEndpointSuffixes,
	)
}

func confirmDestructive(cmd *cobra.Command, action string) error {
	if getBoolFlag(cmd, "yes") || getBoolFlag(cmd, "dry-run") {
		return nil
	}
	format, err := outputFormat(cmd)
	if err != nil {
		return errs.Config("%v", err)
	}
	if format != cliout.Text {
		return errs.Config("--yes is required for destructive operations with --output %s", format)
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s\nType \"yes\" to continue: ", action)
	reader := bufio.NewReader(cmd.InOrStdin())
	answer, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return errs.Config("failed to read confirmation: %v", err)
	}
	if strings.TrimSpace(strings.ToLower(answer)) != "yes" {
		return errs.Config("operation cancelled; rerun with --yes for non-interactive use")
	}
	return nil
}
