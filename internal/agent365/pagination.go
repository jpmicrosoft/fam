package agent365

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

const (
	// maxPages is the hard cap on pages followed during pagination.
	maxPages = 50
	// maxTotalResults is the absolute cap on accumulated results.
	maxTotalResults = 5000
	// defaultPageSize is used when the caller does not specify $top.
	defaultPageSize = 100
)

// PaginationOptions controls paginated list behaviour.
type PaginationOptions struct {
	// Limit caps the total number of results returned. Zero means use the
	// single-page default (backward-compatible). Values above maxTotalResults
	// are clamped.
	Limit int
	// All requests all available results up to maxTotalResults.
	All bool
}

// effectiveLimit returns the resolved result cap.
func (p PaginationOptions) effectiveLimit() int {
	if p.All {
		return maxTotalResults
	}
	if p.Limit > 0 {
		if p.Limit > maxTotalResults {
			return maxTotalResults
		}
		return p.Limit
	}
	return 0 // zero signals single-page mode
}

// validateNextLink ensures a Graph @odata.nextLink is safe to follow.
func validateNextLink(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errs.Security("nextLink is not a valid URL")
	}
	if parsed.Scheme != "https" {
		return nil, errs.Security("nextLink uses non-HTTPS scheme %q", parsed.Scheme)
	}
	if parsed.Hostname() != "graph.microsoft.com" {
		return nil, errs.Security("nextLink targets unexpected host %q", parsed.Hostname())
	}
	if parsed.Port() != "" {
		return nil, errs.Security("nextLink specifies a port")
	}
	if parsed.User != nil {
		return nil, errs.Security("nextLink contains userinfo")
	}
	if parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errs.Security("nextLink contains a fragment or opaque URL component")
	}
	if !strings.HasPrefix(parsed.Path, "/v1.0/") {
		return nil, errs.Security("nextLink path does not start with /v1.0/: %q", parsed.Path)
	}
	return parsed, nil
}

// ValidatePaginationOptions checks that the options are within safe bounds.
func ValidatePaginationOptions(opts PaginationOptions) error {
	if opts.All && opts.Limit > 0 {
		return errs.Config("--all and --limit are mutually exclusive")
	}
	if opts.Limit < 0 {
		return errs.Config("--limit must be a positive integer")
	}
	if opts.Limit > maxTotalResults {
		return errs.Config("--limit exceeds maximum of %d", maxTotalResults)
	}
	return nil
}

// paginatedGetJSON fetches raw JSON pages following nextLink. It returns
// accumulated raw value arrays and whether more results existed beyond the cap.
// The caller is responsible for unmarshalling the value items.
func (c *Client) paginatedGetJSON(
	ctx context.Context,
	initialPath string,
	operation string,
	opts PaginationOptions,
) ([]json.RawMessage, bool, error) {
	if err := ValidatePaginationOptions(opts); err != nil {
		return nil, false, err
	}
	limit := opts.effectiveLimit()
	singlePage := limit == 0

	var all []json.RawMessage
	path := initialPath
	pages := 0

	for {
		var page struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"@odata.nextLink"`
		}
		if err := c.getJSON(ctx, path, operation, &page); err != nil {
			return nil, false, err
		}
		all = append(all, page.Value...)
		pages++

		if singlePage {
			return all, strings.TrimSpace(page.NextLink) != "", nil
		}

		if len(all) >= limit {
			truncated := strings.TrimSpace(page.NextLink) != "" || len(all) > limit
			all = all[:limit]
			return all, truncated, nil
		}

		if strings.TrimSpace(page.NextLink) == "" {
			break
		}

		if pages >= maxPages {
			return all, true, nil
		}

		parsed, err := validateNextLink(page.NextLink)
		if err != nil {
			return nil, false, err
		}
		// Convert absolute URL back to path+query for getJSON
		// (getJSON prepends Endpoint)
		path = parsed.RequestURI()
	}

	return all, false, nil
}
