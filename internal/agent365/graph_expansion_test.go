package agent365

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

// ---------------------------------------------------------------------------
// Pagination / nextLink validation tests
// ---------------------------------------------------------------------------

func TestValidateNextLinkRejectsHTTP(t *testing.T) {
	_, err := validateNextLink("http://graph.microsoft.com/v1.0/servicePrincipals?$skiptoken=x")
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("expected HTTPS rejection, got %v", err)
	}
}

func TestValidateNextLinkRejectsWrongHost(t *testing.T) {
	_, err := validateNextLink("https://evil.com/v1.0/servicePrincipals")
	if err == nil || !strings.Contains(err.Error(), "unexpected host") {
		t.Fatalf("expected host rejection, got %v", err)
	}
}

func TestValidateNextLinkRejectsPort(t *testing.T) {
	_, err := validateNextLink("https://graph.microsoft.com:8443/v1.0/servicePrincipals")
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("expected port rejection, got %v", err)
	}
}

func TestValidateNextLinkRejectsUserinfo(t *testing.T) {
	_, err := validateNextLink("https://admin:pass@graph.microsoft.com/v1.0/servicePrincipals")
	if err == nil || !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("expected userinfo rejection, got %v", err)
	}
}

func TestValidateNextLinkRejectsNonV1Path(t *testing.T) {
	_, err := validateNextLink("https://graph.microsoft.com/beta/servicePrincipals")
	if err == nil || !strings.Contains(err.Error(), "/v1.0/") {
		t.Fatalf("expected v1.0 path rejection, got %v", err)
	}
}

func TestValidateNextLinkRejectsFragment(t *testing.T) {
	_, err := validateNextLink(
		"https://graph.microsoft.com/v1.0/servicePrincipals?$skiptoken=x#fragment",
	)
	if err == nil || !strings.Contains(err.Error(), "fragment") {
		t.Fatalf("expected fragment rejection, got %v", err)
	}
}

func TestValidateNextLinkAcceptsValid(t *testing.T) {
	u, err := validateNextLink("https://graph.microsoft.com/v1.0/servicePrincipals?$skiptoken=abc")
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/v1.0/servicePrincipals" {
		t.Fatalf("path = %q", u.Path)
	}
}

func TestValidateNextLinkEmpty(t *testing.T) {
	u, err := validateNextLink("")
	if err != nil || u != nil {
		t.Fatalf("empty should return nil, got %v %v", u, err)
	}
}

func TestValidatePaginationOptionsMutuallyExclusive(t *testing.T) {
	if err := ValidatePaginationOptions(PaginationOptions{All: true, Limit: 10}); err == nil {
		t.Fatal("expected error for --all + --limit")
	}
}

func TestValidatePaginationOptionsNegativeLimit(t *testing.T) {
	if err := ValidatePaginationOptions(PaginationOptions{Limit: -1}); err == nil {
		t.Fatal("expected error for negative limit")
	}
}

func TestPaginationFollowsNextLink(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{"id":"`+testObjectID+`","displayName":"A1","appId":"`+testAppID+`"}],`+
			`"@odata.nextLink":"https://graph.microsoft.com/v1.0/servicePrincipals/microsoft.graph.agentIdentity?$skiptoken=page2"}`),
		graphResponse(http.StatusOK, `{"value":[{"id":"11111111-1111-1111-1111-111111111111","displayName":"A2","appId":"`+testAppID+`"}]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	result, err := client.ListAgentIdentities(context.Background(), 100, PaginationOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 {
		t.Fatalf("count = %d, want 2", result.Count)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(transport.requests))
	}
}

func TestPaginationRejectsHostileNextLink(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{"id":"`+testObjectID+`","displayName":"A1","appId":"`+testAppID+`"}],`+
			`"@odata.nextLink":"https://evil.com/v1.0/steal?token=yes"}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	_, err := client.ListAgentIdentities(context.Background(), 100, PaginationOptions{All: true})
	if err == nil || errs.KindOf(err) != "security" {
		t.Fatalf("expected security error for hostile nextLink, got %v", err)
	}
}

func TestPaginationStopsAtMaxPages(t *testing.T) {
	responses := make([]*http.Response, maxPages+1)
	for i := range responses {
		next := ""
		if i < maxPages {
			next = fmt.Sprintf(`,"@odata.nextLink":"https://graph.microsoft.com/v1.0/servicePrincipals/microsoft.graph.agentIdentity?$skiptoken=page%d"`, i+1)
		}
		id := fmt.Sprintf("%08d-0000-0000-0000-000000000000", i)
		responses[i] = graphResponse(http.StatusOK, fmt.Sprintf(`{"value":[{"id":"%s","displayName":"A%d","appId":"%s"}]%s}`, id, i, testAppID, next))
	}
	transport := &testHTTP{responses: responses}
	client, _ := NewClient(&testCredential{}, transport)
	result, err := client.ListAgentIdentities(context.Background(), 100, PaginationOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != maxPages {
		t.Fatalf("requests = %d, want %d (max pages cap)", len(transport.requests), maxPages)
	}
	if !result.Truncated {
		t.Fatal("should be truncated at max pages")
	}
}

func TestPaginationSinglePageBackwardCompatible(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{"id":"`+testObjectID+`","displayName":"A1","appId":"`+testAppID+`"}],`+
			`"@odata.nextLink":"https://graph.microsoft.com/v1.0/servicePrincipals/microsoft.graph.agentIdentity?$skiptoken=page2"}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	// Zero-value PaginationOptions = single page mode
	result, err := client.ListAgentIdentities(context.Background(), 100, PaginationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 {
		t.Fatalf("count = %d, want 1", result.Count)
	}
	if !result.Truncated {
		t.Fatal("should indicate truncated when nextLink present")
	}
	if len(transport.requests) != 1 {
		t.Fatal("single-page mode should not follow nextLink")
	}
}

// ---------------------------------------------------------------------------
// Agent Identity tests
// ---------------------------------------------------------------------------

func TestListAgentIdentities(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{
			"id":"`+testObjectID+`",
			"displayName":"My Agent",
			"appId":"`+testAppID+`",
			"accountEnabled":true,
			"servicePrincipalType":"AgentIdentity",
			"tags":["tag1"]
		}]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	result, err := client.ListAgentIdentities(context.Background(), 10, PaginationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || result.Identities[0].DisplayName != "My Agent" {
		t.Fatalf("unexpected result: %+v", result)
	}
	req := transport.requests[0]
	if !strings.Contains(req.URL.Path, "microsoft.graph.agentIdentity") {
		t.Fatalf("path = %q", req.URL.Path)
	}
}

func TestGetAgentIdentity(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{
			"id":"`+testObjectID+`",
			"displayName":"My Agent",
			"servicePrincipalType":"AgentIdentity"
		}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	ai, err := client.GetAgentIdentity(context.Background(), testObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if ai.ID != testObjectID {
		t.Fatalf("id = %q", ai.ID)
	}
	path := transport.requests[0].URL.EscapedPath()
	expected := "/v1.0/servicePrincipals/" + testObjectID + "/microsoft.graph.agentIdentity"
	if path != expected {
		t.Fatalf("path = %q, want %q", path, expected)
	}
}

func TestGetAgentIdentityInvalidGUID(t *testing.T) {
	client, _ := NewClient(&testCredential{}, &testHTTP{})
	_, err := client.GetAgentIdentity(context.Background(), "not-a-guid")
	if err == nil || errs.KindOf(err) != "config" {
		t.Fatalf("expected config error, got %v", err)
	}
}

func TestListAgentIdentitiesForbiddenRemediation(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusForbidden, `{"error":{"code":"Authorization_RequestDenied","message":"denied"}}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	_, err := client.ListAgentIdentities(context.Background(), 10, PaginationOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	text := strings.Join(errs.Remediation(err), "\n")
	if !strings.Contains(text, "AgentIdentity.Read.All") {
		t.Fatalf("missing permission remediation: %s", text)
	}
	if !strings.Contains(text, "Agent ID Administrator") {
		t.Fatalf("missing role remediation: %s", text)
	}
}

func TestListAgentIdentitiesByBlueprint(t *testing.T) {
	blueprintID := "22222222-2222-2222-2222-222222222222"
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{
			"id":"`+testObjectID+`",
			"displayName":"Filtered Agent",
			"agentIdentityBlueprintId":"`+blueprintID+`"
		}]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	result, err := client.ListAgentIdentitiesByBlueprint(context.Background(), blueprintID, 10, PaginationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 {
		t.Fatalf("count = %d", result.Count)
	}
	filter := transport.requests[0].URL.Query().Get("$filter")
	if !strings.Contains(filter, blueprintID) {
		t.Fatalf("$filter = %q", filter)
	}
}

func TestAgentIdentityMalformedRemote(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{"id":"bad","displayName":"X"}]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	_, err := client.ListAgentIdentities(context.Background(), 10, PaginationOptions{})
	if err == nil || errs.KindOf(err) != "foundry" {
		t.Fatalf("expected foundry error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Blueprint Principal tests
// ---------------------------------------------------------------------------

func TestListBlueprintPrincipals(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{
			"id":"`+testObjectID+`",
			"displayName":"Blueprint SP",
			"appId":"`+testAppID+`",
			"servicePrincipalType":"Application"
		}]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	result, err := client.ListBlueprintPrincipals(context.Background(), 10, PaginationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 {
		t.Fatalf("count = %d", result.Count)
	}
	if !strings.Contains(transport.requests[0].URL.Path, "microsoft.graph.agentIdentityBlueprintPrincipal") {
		t.Fatalf("wrong path: %s", transport.requests[0].URL.Path)
	}
}

func TestGetBlueprintPrincipal(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{
			"id":"`+testObjectID+`",
			"displayName":"Blueprint SP"
		}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	bp, err := client.GetBlueprintPrincipal(context.Background(), testObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if bp.ID != testObjectID {
		t.Fatalf("id = %q", bp.ID)
	}
	expected := "/v1.0/servicePrincipals/" + testObjectID + "/microsoft.graph.agentIdentityBlueprintPrincipal"
	if transport.requests[0].URL.EscapedPath() != expected {
		t.Fatalf("path = %q", transport.requests[0].URL.EscapedPath())
	}
}

func TestBlueprintPrincipalForbiddenRemediation(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusForbidden, `{"error":{"code":"Authorization_RequestDenied","message":"denied"}}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	_, err := client.ListBlueprintPrincipals(context.Background(), 10, PaginationOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	text := strings.Join(errs.Remediation(err), "\n")
	if !strings.Contains(text, "AgentIdentityBlueprintPrincipal.Read.All") {
		t.Fatalf("missing permission: %s", text)
	}
}

// ---------------------------------------------------------------------------
// Owners / Sponsors tests
// ---------------------------------------------------------------------------

func TestListBlueprintOwners(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{
			"@odata.type":"#microsoft.graph.user",
			"id":"`+testObjectID+`",
			"displayName":"Owner User"
		}]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	owners, err := client.ListBlueprintOwners(context.Background(), testObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].DisplayName != "Owner User" {
		t.Fatalf("unexpected owners: %+v", owners)
	}
	path := transport.requests[0].URL.EscapedPath()
	if !strings.Contains(path, "/owners") || !strings.Contains(path, "agentIdentityBlueprint") {
		t.Fatalf("path = %q", path)
	}
	sel := transport.requests[0].URL.Query().Get("$select")
	if strings.Contains(sel, "mail") || strings.Contains(sel, "userPrincipalName") {
		t.Fatalf("$select requests sensitive fields: %s", sel)
	}
}

func TestListBlueprintSponsors(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{
			"@odata.type":"#microsoft.graph.servicePrincipal",
			"id":"33333333-3333-3333-3333-333333333333",
			"displayName":"Sponsor SP"
		}]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	sponsors, err := client.ListBlueprintSponsors(context.Background(), testObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sponsors) != 1 {
		t.Fatalf("sponsors = %d", len(sponsors))
	}
}

func TestOwnersForbiddenRemediation(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusForbidden, `{"error":{"code":"Authorization_RequestDenied","message":"denied"}}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	_, err := client.ListBlueprintOwners(context.Background(), testObjectID)
	text := strings.Join(errs.Remediation(err), "\n")
	if !strings.Contains(text, "AgentIdentityBlueprint.Read.All") {
		t.Fatalf("missing permission: %s", text)
	}
}

func TestSponsorsForbiddenRemediation(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusForbidden, `{"error":{"code":"Authorization_RequestDenied","message":"denied"}}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	_, err := client.ListBlueprintSponsors(context.Background(), testObjectID)
	text := strings.Join(errs.Remediation(err), "\n")
	if !strings.Contains(text, "Application.Read.All") {
		t.Fatalf("missing permission: %s", text)
	}
}

// ---------------------------------------------------------------------------
// Permission resolution tests
// ---------------------------------------------------------------------------

func TestResolvePermissions(t *testing.T) {
	resourceAppID := "00000003-0000-0000-c000-000000000000"
	scopeID := "e1fe6dd8-ba31-4d61-89e7-88639da4683d"
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, fmt.Sprintf(`{"value":[{
			"id":"sp-id","displayName":"Microsoft Graph","appId":"%s",
			"oauth2PermissionScopes":[{"id":"%s","value":"User.Read","adminConsentDisplayName":"Sign in and read user profile"}],
			"appRoles":[]
		}]}`, resourceAppID, scopeID)),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	resolved, err := client.ResolvePermissions(context.Background(), Blueprint{
		AppID: testAppID,
		RequiredResourceAccess: []RequiredResourceAccess{{
			ResourceAppID:  resourceAppID,
			ResourceAccess: []ResourceAccess{{ID: scopeID, Type: "Scope"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved = %d", len(resolved))
	}
	if resolved[0].PermissionValue != "User.Read" {
		t.Fatalf("value = %q", resolved[0].PermissionValue)
	}
	if resolved[0].ResourceDisplayName != "Microsoft Graph" {
		t.Fatalf("resource = %q", resolved[0].ResourceDisplayName)
	}
}

func TestResolvePermissionsForbiddenRemediation(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusForbidden, `{"error":{"code":"Authorization_RequestDenied","message":"denied"}}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	_, err := client.ResolvePermissions(context.Background(), Blueprint{
		AppID: testAppID,
		RequiredResourceAccess: []RequiredResourceAccess{{
			ResourceAppID:  "00000003-0000-0000-c000-000000000000",
			ResourceAccess: []ResourceAccess{{ID: "e1fe6dd8-ba31-4d61-89e7-88639da4683d", Type: "Scope"}},
		}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	text := strings.Join(errs.Remediation(err), "\n")
	if !strings.Contains(text, "Application.Read.All") {
		t.Fatalf("missing permission: %s", text)
	}
}

// ---------------------------------------------------------------------------
// Observability tests
// ---------------------------------------------------------------------------

func TestResolveObservabilityServicePrincipal(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{
			"id":"`+testObjectID+`",
			"displayName":"Agent365Observability",
			"appId":"`+testAppID+`"
		}]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	sp, err := client.ResolveObservabilityServicePrincipal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sp.DisplayName != "Agent365Observability" {
		t.Fatalf("displayName = %q", sp.DisplayName)
	}
	filter := transport.requests[0].URL.Query().Get("$filter")
	if !strings.Contains(filter, "Agent365Observability") {
		t.Fatalf("$filter = %q", filter)
	}
}

func TestResolveObservabilityNotFound(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	_, err := client.ResolveObservabilityServicePrincipal(context.Background())
	if err == nil || errs.KindOf(err) != "not_found" {
		t.Fatalf("expected not_found, got %v", err)
	}
}

func TestListAppRoleAssignments(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{
			"id":"assign-1",
			"appRoleId":"8f71190c-00c8-461d-a63b-f74abde9ba52",
			"principalId":"`+testObjectID+`",
			"resourceId":"44444444-4444-4444-4444-444444444444",
			"resourceDisplayName":"Agent365Observability"
		}]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	assignments, err := client.ListAppRoleAssignments(context.Background(), testObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 || assignments[0].AppRoleID != ObservabilityAppRoleID {
		t.Fatalf("unexpected: %+v", assignments)
	}
	path := transport.requests[0].URL.EscapedPath()
	if !strings.Contains(path, "/appRoleAssignments") {
		t.Fatalf("path = %q", path)
	}
	if transport.requests[0].Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", transport.requests[0].Method)
	}
}

func TestHasObservabilityAssignment(t *testing.T) {
	resourceID := "44444444-4444-4444-4444-444444444444"
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, fmt.Sprintf(`{"value":[
			{"id":"a1","appRoleId":"00000000-0000-0000-0000-000000000000","principalId":"%s","resourceId":"33333333-3333-3333-3333-333333333333"},
			{"id":"a2","appRoleId":"%s","principalId":"%s","resourceId":"%s"}
		]}`, testObjectID, ObservabilityAppRoleID, testObjectID, resourceID)),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	found, assignment, err := client.HasObservabilityAssignment(context.Background(), testObjectID, resourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || assignment == nil {
		t.Fatal("expected to find assignment")
	}
	if assignment.AppRoleID != ObservabilityAppRoleID {
		t.Fatalf("appRoleId = %q", assignment.AppRoleID)
	}
}

func TestHasObservabilityAssignmentNotFound(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[]}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	found, assignment, err := client.HasObservabilityAssignment(
		context.Background(), testObjectID, "55555555-5555-5555-5555-555555555555",
	)
	if err != nil {
		t.Fatal(err)
	}
	if found || assignment != nil {
		t.Fatal("expected not found")
	}
}

// ---------------------------------------------------------------------------
// GET-only behavior verification
// ---------------------------------------------------------------------------

func TestAllRequestsAreGETOnly(t *testing.T) {
	// Run several operations and verify every request is GET.
	responses := []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{"id":"`+testObjectID+`","displayName":"X","appId":"`+testAppID+`"}]}`),
		graphResponse(http.StatusOK, `{"id":"`+testObjectID+`","displayName":"X"}`),
		graphResponse(http.StatusOK, `{"value":[{"id":"`+testObjectID+`","displayName":"X"}]}`),
		graphResponse(http.StatusOK, `{"value":[]}`),
		graphResponse(http.StatusOK, `{"value":[{"@odata.type":"#microsoft.graph.user","id":"`+testObjectID+`","displayName":"O"}]}`),
	}
	transport := &testHTTP{responses: responses}
	client, _ := NewClient(&testCredential{}, transport)
	ctx := context.Background()

	_, _ = client.ListAgentIdentities(ctx, 10, PaginationOptions{})
	_, _ = client.GetAgentIdentity(ctx, testObjectID)
	_, _ = client.ListBlueprintPrincipals(ctx, 10, PaginationOptions{})
	_, _ = client.ListAppRoleAssignments(ctx, testObjectID)
	_, _ = client.ListBlueprintOwners(ctx, testObjectID)

	for i, req := range transport.requests {
		if req.Method != http.MethodGet {
			t.Fatalf("request %d: method = %q, want GET", i, req.Method)
		}
		if req.Body != nil && req.Body != http.NoBody {
			body, _ := io.ReadAll(req.Body)
			if len(body) > 0 {
				t.Fatalf("request %d has a body", i)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Secret exclusion verification
// ---------------------------------------------------------------------------

func TestIdentitySelectExcludesSecrets(t *testing.T) {
	for _, field := range []string{"keyCredentials", "passwordCredentials", "federatedIdentityCredentials"} {
		if strings.Contains(identitySelect, field) {
			t.Fatalf("identitySelect contains %q", field)
		}
	}
}

func TestBlueprintPrincipalSelectExcludesSecrets(t *testing.T) {
	for _, field := range []string{"keyCredentials", "passwordCredentials", "federatedIdentityCredentials"} {
		if strings.Contains(blueprintPrincipalSelect, field) {
			t.Fatalf("blueprintPrincipalSelect contains %q", field)
		}
	}
}

// ---------------------------------------------------------------------------
// 401 remediation for identity operations
// ---------------------------------------------------------------------------

func TestIdentityUnauthorizedRemediation(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusUnauthorized, `{"error":{"code":"InvalidAuthenticationToken","message":"expired"}}`),
	}}
	client, _ := NewClient(&testCredential{}, transport)
	_, err := client.GetAgentIdentity(context.Background(), testObjectID)
	if err == nil {
		t.Fatal("expected error")
	}
	text := strings.Join(errs.Remediation(err), "\n")
	if !strings.Contains(text, "Sign in") {
		t.Fatalf("missing sign-in remediation: %s", text)
	}
}
