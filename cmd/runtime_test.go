package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/spf13/cobra"
)

type endpointCredential struct{}

func (endpointCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type endpointHTTP struct {
	response *http.Response
	requests []*http.Request
}

func (c *endpointHTTP) Do(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req)
	return c.response, nil
}

func endpointConfig(t *testing.T, cloudName string) *config.ResolvedConfig {
	t.Helper()
	profile, err := azcloud.Resolve(cloudName)
	if err != nil {
		t.Fatal(err)
	}
	return &config.ResolvedConfig{
		Cloud: profile,
		Project: config.ProjectSpec{
			Name:           "project",
			AccountName:    "account",
			ResourceGroup:  "rg",
			SubscriptionID: "sub",
			APIVersion:     config.DefaultProjectAPIVersion,
			ARMEndpoint:    profile.ARMEndpoint,
			ARMScope:       profile.ARMScope,
			AllowedRegions: append([]string(nil), profile.FoundryRegions...),
		},
	}
}

func TestResolveProjectEndpointReportsMissingProject(t *testing.T) {
	cfg := endpointConfig(t, azcloud.AzureCloud)
	httpClient := &endpointHTTP{response: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":"missing"}`)),
	}}
	_, err := resolveProjectEndpoint(&cobra.Command{}, cfg, endpointCredential{}, httpClient)
	if err == nil || !errs.IsKind(err, "not_found") {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

func TestStructuredDestructiveConfirmationDoesNotWritePrompt(t *testing.T) {
	command, _, err := rootCmd().Find([]string{"delete"})
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Root().PersistentFlags().Set("output", "json"); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetIn(strings.NewReader(""))
	err = confirmDestructive(command, "Delete agent?")
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected a config error, got %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("structured confirmation wrote non-JSON prompt text: %q", stderr.String())
	}
}
