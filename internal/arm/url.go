// Package arm contains shared Azure Resource Manager request helpers.
package arm

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	Endpoint = "https://management.azure.com"
	Scope    = "https://management.azure.com/.default"
)

// ResourceURL builds an escaped ARM resource URL with an api-version query.
func ResourceURL(apiVersion string, segments ...string) (string, error) {
	return ResourceURLForEndpoint(Endpoint, apiVersion, segments...)
}

// ResourceURLForEndpoint builds an escaped ARM resource URL for the selected cloud.
func ResourceURLForEndpoint(endpoint, apiVersion string, segments ...string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("ARM endpoint must not be empty")
	}
	if apiVersion == "" {
		return "", fmt.Errorf("ARM api-version must not be empty")
	}
	escaped := make([]string, len(segments))
	for i, segment := range segments {
		escaped[i] = url.PathEscape(segment)
	}
	resourceURL, err := url.Parse(strings.TrimRight(endpoint, "/") + "/" + strings.Join(escaped, "/"))
	if err != nil {
		return "", fmt.Errorf("failed to build ARM resource URL: %w", err)
	}
	query := resourceURL.Query()
	query.Set("api-version", apiVersion)
	resourceURL.RawQuery = query.Encode()
	return resourceURL.String(), nil
}

// ValidateNextLink accepts only absolute pagination links on the configured ARM origin.
func ValidateNextLink(endpoint, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	next, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("ARM nextLink is invalid: %w", err)
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("ARM endpoint is invalid: %w", err)
	}
	if !strings.EqualFold(next.Scheme, base.Scheme) ||
		!strings.EqualFold(next.Host, base.Host) ||
		next.User != nil ||
		next.Fragment != "" {
		return "", fmt.Errorf("ARM nextLink changed origin")
	}
	return next.String(), nil
}
