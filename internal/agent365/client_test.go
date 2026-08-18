package agent365

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	testAppID    = "00001111-aaaa-2222-bbbb-3333cccc4444"
	testObjectID = "08be1f79-37a1-49c0-b444-3075e74d1e8c"
)

type testCredential struct {
	options []policy.TokenRequestOptions
	err     error
}

func (c *testCredential) GetToken(
	_ context.Context,
	options policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	c.options = append(c.options, options)
	if c.err != nil {
		return azcore.AccessToken{}, c.err
	}
	return azcore.AccessToken{Token: "test-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type testHTTP struct {
	requests  []*http.Request
	responses []*http.Response
	err       error
}

func (h *testHTTP) Do(request *http.Request) (*http.Response, error) {
	h.requests = append(h.requests, request)
	if h.err != nil {
		return nil, h.err
	}
	if len(h.responses) == 0 {
		return nil, errors.New("unexpected request")
	}
	response := h.responses[0]
	h.responses = h.responses[1:]
	response.Request = request
	return response, nil
}

func graphResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestGetBlueprintByApplicationIDUsesSafeSelect(t *testing.T) {
	credential := &testCredential{}
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{
			"id":"`+testObjectID+`",
			"appId":"`+testAppID+`",
			"displayName":"Support Agent",
			"managerApplications":["14d82eec-204b-4c2f-b7e8-296a70dab67e"],
			"requiredResourceAccess":[]
		}]}`),
	}}
	client, err := NewClient(credential, transport)
	if err != nil {
		t.Fatal(err)
	}

	blueprint, err := client.GetBlueprint(context.Background(), BlueprintSelector{AppID: testAppID})
	if err != nil {
		t.Fatal(err)
	}
	if blueprint.ObjectID != testObjectID || blueprint.AppID != testAppID {
		t.Fatalf("unexpected blueprint: %+v", blueprint)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(transport.requests))
	}
	request := transport.requests[0]
	if request.URL.Scheme != "https" || request.URL.Host != "graph.microsoft.com" {
		t.Fatalf("unsafe Graph URL: %s", request.URL)
	}
	selectQuery := request.URL.Query().Get("$select")
	for _, forbidden := range []string{
		"keyCredentials", "passwordCredentials", "federatedIdentityCredentials",
	} {
		if strings.Contains(selectQuery, forbidden) {
			t.Fatalf("$select contains forbidden credential field %q: %s", forbidden, selectQuery)
		}
	}
	if filter := request.URL.Query().Get("$filter"); filter != "appId eq '"+testAppID+"'" {
		t.Fatalf("$filter = %q", filter)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("authorization = %q", got)
	}
	if len(credential.options) != 1 ||
		len(credential.options[0].Scopes) != 1 ||
		credential.options[0].Scopes[0] != Scope {
		t.Fatalf("unexpected token options: %+v", credential.options)
	}
}

func TestGetBlueprintByObjectIDUsesDocumentedCastRoute(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{
			"id":"`+testObjectID+`",
			"appId":"`+testAppID+`",
			"displayName":"Support Agent",
			"managerApplications":[],
			"requiredResourceAccess":[]
		}`),
	}}
	client, err := NewClient(&testCredential{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetBlueprint(
		context.Background(),
		BlueprintSelector{ObjectID: testObjectID},
	); err != nil {
		t.Fatal(err)
	}
	if got := transport.requests[0].URL.EscapedPath(); got !=
		"/v1.0/applications/"+testObjectID+"/microsoft.graph.agentIdentityBlueprint" {
		t.Fatalf("path = %q", got)
	}
}

func TestListInheritablePermissionsPreservesDocumentedModes(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[
			{"resourceAppId":"00000003-0000-0000-c000-000000000000",
			 "inheritableScopes":{"kind":"enumerated","scopes":["User.Read"]}},
			{"resourceAppId":"00000003-0000-0ff1-ce00-000000000000",
			 "inheritableScopes":{"kind":"allAllowed"}},
			{"resourceAppId":"a4294fb4-199a-45eb-b2bb-405ae558f61a",
			 "inheritableScopes":{"kind":"none"}}
		]}`),
	}}
	client, err := NewClient(&testCredential{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := client.ListInheritablePermissions(context.Background(), testObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(permissions) != 3 {
		t.Fatalf("permissions = %d, want 3", len(permissions))
	}
	result := Validate(Blueprint{AppID: testAppID}, permissions)
	if !result.Valid {
		t.Fatalf("all documented modes should validate: %+v", result.Checks)
	}
}

func TestForbiddenResponseIncludesGraphRemediation(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusForbidden, `{"error":{"code":"Authorization_RequestDenied","message":"denied"}}`),
	}}
	client, err := NewClient(&testCredential{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetBlueprint(context.Background(), BlueprintSelector{AppID: testAppID})
	if err == nil {
		t.Fatal("expected error")
	}
	if errs.KindOf(err) != "authorization" {
		t.Fatalf("kind = %s, want authorization_denied: %v", errs.KindOf(err), err)
	}
	text := strings.Join(errs.Remediation(err), "\n")
	for _, expected := range []string{
		"AgentIdentityBlueprint.Read.All",
		"Agent ID Administrator",
		"--tenant-id",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("error missing %q: %s", expected, text)
		}
	}
}

func TestInvalidSelectorDoesNotAcquireTokenOrSendRequest(t *testing.T) {
	credential := &testCredential{}
	transport := &testHTTP{}
	client, err := NewClient(credential, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetBlueprint(
		context.Background(),
		BlueprintSelector{AppID: "not-a-guid"},
	)
	if err == nil || errs.KindOf(err) != "config" {
		t.Fatalf("expected config error, got %v", err)
	}
	if len(credential.options) != 0 || len(transport.requests) != 0 {
		t.Fatal("invalid selector must fail before authentication or HTTP")
	}
}

func TestOversizedResponseFailsClosed(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, strings.Repeat("x", maxResponseBodyBytes+1)),
	}}
	client, err := NewClient(&testCredential{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListBlueprints(context.Background(), 10)
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("expected bounded response error, got %v", err)
	}
}

func TestMalformedRemoteBlueprintIsServiceError(t *testing.T) {
	transport := &testHTTP{responses: []*http.Response{
		graphResponse(http.StatusOK, `{"value":[{
			"id":"not-an-id",
			"appId":"`+testAppID+`",
			"displayName":"Support Agent",
			"managerApplications":[],
			"requiredResourceAccess":[]
		}]}`),
	}}
	client, err := NewClient(&testCredential{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetBlueprint(context.Background(), BlueprintSelector{AppID: testAppID})
	if err == nil || errs.KindOf(err) != "foundry" {
		t.Fatalf("expected foundry error for malformed remote data, got %v", err)
	}
}

func TestValidateDisabledBlueprintFails(t *testing.T) {
	result := Validate(Blueprint{
		AppID:                     testAppID,
		DisabledByMicrosoftStatus: "DisabledDueToViolationOfServicesAgreement",
	}, nil)
	if result.Valid {
		t.Fatal("disabled blueprint must fail validation")
	}
}
