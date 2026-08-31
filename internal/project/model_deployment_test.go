package project

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"
)

func TestInspectModelDeploymentContextReturnsAccountDeployment(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, map[string]interface{}{
			"name": "chat-prod",
			"properties": map[string]interface{}{
				"model": map[string]interface{}{
					"name": "gpt-5-mini", "version": "2025-08-07", "format": "OpenAI",
				},
				"provisioningState": "Succeeded",
			},
		}),
	}}
	credential := &recordingCredential{}
	state, err := InspectModelDeploymentContext(
		context.Background(),
		baseProject(),
		"chat-prod",
		credential,
		httpClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists ||
		state.Name != "chat-prod" ||
		state.ModelName != "gpt-5-mini" ||
		state.ProvisioningState != "Succeeded" {
		t.Fatalf("unexpected deployment state: %#v", state)
	}
	request := httpClient.requests[0]
	if request.Method != http.MethodGet ||
		!strings.HasSuffix(request.URL.Path, "/accounts/acct/deployments/chat-prod") ||
		request.URL.Query().Get("api-version") != modelDeploymentAPIVersion {
		t.Fatalf("unexpected model deployment request: %s %s", request.Method, request.URL)
	}
}

func TestInspectModelDeploymentContextReportsMissing(t *testing.T) {
	state, err := InspectModelDeploymentContext(
		context.Background(),
		baseProject(),
		"missing",
		&recordingCredential{},
		&recordingHTTPClient{responses: []*http.Response{
			response(http.StatusNotFound, map[string]interface{}{"error": "missing"}),
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.Exists || state.Name != "missing" {
		t.Fatalf("unexpected missing state: %#v", state)
	}
}

func TestInspectRAIPolicyContextListsSystemManagedPolicies(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, map[string]interface{}{
			"value": []interface{}{
				map[string]interface{}{"name": "Microsoft.Default"},
				map[string]interface{}{"name": "Microsoft.DefaultV2"},
			},
		}),
	}}
	err := InspectRAIPolicyContext(
		context.Background(),
		baseProject(),
		"Microsoft.DefaultV2",
		&recordingCredential{},
		httpClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httpClient.requests[0]
	if request.Method != http.MethodGet ||
		!strings.HasSuffix(request.URL.Path, "/accounts/acct/raiPolicies") ||
		request.URL.Query().Get("api-version") != modelDeploymentAPIVersion {
		t.Fatalf("unexpected RAI policy list request: %s %s", request.Method, request.URL)
	}
}

func TestInspectRAIPolicyContextFollowsPagination(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, map[string]interface{}{
			"value": []interface{}{
				map[string]interface{}{"name": "Microsoft.Default"},
			},
			"nextLink": "https://management.azure.com/subscriptions/sub/resourceGroups/rg%20with%20space/providers/Microsoft.CognitiveServices/accounts/acct/raiPolicies?api-version=2025-06-01&$skiptoken=next",
		}),
		response(http.StatusOK, map[string]interface{}{
			"value": []interface{}{
				map[string]interface{}{"name": "microsoft.defaultv2"},
			},
		}),
	}}
	err := InspectRAIPolicyContext(
		context.Background(),
		baseProject(),
		"Microsoft.DefaultV2",
		&recordingCredential{},
		httpClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(httpClient.requests) != 2 ||
		httpClient.requests[1].URL.Query().Get("$skiptoken") != "next" {
		t.Fatalf("unexpected RAI policy pagination requests: %#v", httpClient.requests)
	}
}

func TestInspectRAIPolicyContextReportsMissing(t *testing.T) {
	err := InspectRAIPolicyContext(
		context.Background(),
		baseProject(),
		"custom",
		&recordingCredential{},
		&recordingHTTPClient{responses: []*http.Response{
			response(http.StatusOK, map[string]interface{}{
				"value": []interface{}{
					map[string]interface{}{"name": "Microsoft.DefaultV2"},
				},
			}),
		}},
	)
	if err == nil || !errs.IsKind(err, "not_found") {
		t.Fatalf("missing RAI policy must return not_found, got %v", err)
	}
}

func TestInspectModelDeploymentContextRejectsMalformedAndOversizedResponses(t *testing.T) {
	tests := []struct {
		name     string
		response *http.Response
	}{
		{
			name:     "missing provisioning state",
			response: response(http.StatusOK, map[string]interface{}{"name": "model"}),
		},
		{
			name: "oversized",
			response: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					strings.Repeat("x", int(maxModelDeploymentResponseBytes)+1),
				)),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := InspectModelDeploymentContext(
				context.Background(),
				baseProject(),
				"model",
				&recordingCredential{},
				&recordingHTTPClient{responses: []*http.Response{test.response}},
			)
			if err == nil || !errs.IsKind(err, "foundry") {
				t.Fatalf("malformed response must fail closed, got %v", err)
			}
		})
	}
}

func TestListModelDeploymentsContextFollowsSafePagination(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, map[string]interface{}{
			"value":    []interface{}{deploymentPayload("first", "gpt-5-mini", "2025-08-07", "GlobalStandard", 10)},
			"nextLink": "https://management.azure.com/subscriptions/sub/resourceGroups/rg%20with%20space/providers/Microsoft.CognitiveServices/accounts/acct/deployments?api-version=2025-06-01&$skiptoken=next",
		}),
		response(http.StatusOK, map[string]interface{}{
			"value": []interface{}{deploymentPayload("second", "gpt-4.1", "2025-04-14", "Standard", 1)},
		}),
	}}
	deployments, err := ListModelDeploymentsContext(
		context.Background(),
		baseProject(),
		&recordingCredential{},
		httpClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 2 || deployments[0].Name != "first" || deployments[1].Name != "second" {
		t.Fatalf("unexpected deployments: %#v", deployments)
	}
	if len(httpClient.requests) != 2 ||
		httpClient.requests[1].URL.Query().Get("$skiptoken") != "next" {
		t.Fatalf("unexpected pagination requests: %#v", httpClient.requests)
	}
}

func TestPlanModelDeploymentContextValidatesCatalogQuotaAndCapacity(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: modelPlanResponses(
		10,
		2,
		20,
		15,
	)}
	plan, err := PlanModelDeploymentContext(
		context.Background(),
		baseProject(),
		baseModelDeploymentDesired(),
		&recordingCredential{},
		httpClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "create" || !plan.Ready || plan.Location != "eastus" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if len(plan.Checks) != 7 {
		t.Fatalf("expected seven live checks, got %#v", plan.Checks)
	}
	if plan.Checks[5].Name != "quota" || plan.Checks[5].Status != "passed" {
		t.Fatalf("quota was not validated: %#v", plan.Checks)
	}
	if plan.Checks[6].Name != "regional-capacity" || plan.Checks[6].Status != "passed" {
		t.Fatalf("regional capacity was not validated: %#v", plan.Checks)
	}
}

func TestPlanModelDeploymentContextReturnsUnchangedWithoutCatalogCalls(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, deploymentPayload(
			"gpt-5-mini",
			"gpt-5-mini",
			"2025-08-07",
			"GlobalStandard",
			10,
		)),
	}}
	plan, err := PlanModelDeploymentContext(
		context.Background(),
		baseProject(),
		baseModelDeploymentDesired(),
		&recordingCredential{},
		httpClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "unchanged" || !plan.Ready || len(httpClient.requests) != 1 {
		t.Fatalf("unexpected unchanged plan: %#v requests=%d", plan, len(httpClient.requests))
	}
}

func TestPlanModelDeploymentContextRejectsExistingDrift(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, deploymentPayload(
			"gpt-5-mini",
			"gpt-5-mini",
			"2025-08-07",
			"GlobalStandard",
			20,
		)),
	}}
	_, err := PlanModelDeploymentContext(
		context.Background(),
		baseProject(),
		baseModelDeploymentDesired(),
		&recordingCredential{},
		httpClient,
	)
	if err == nil || !errs.IsKind(err, "conflict") || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("expected explicit drift rejection, got %v", err)
	}
}

func TestPlanModelDeploymentContextRejectsInsufficientQuota(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: modelPlanResponses(
		10,
		15,
		20,
		15,
	)}
	_, err := PlanModelDeploymentContext(
		context.Background(),
		baseProject(),
		baseModelDeploymentDesired(),
		&recordingCredential{},
		httpClient,
	)
	if err == nil || !errs.IsKind(err, "conflict") || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("expected quota validation failure, got %v", err)
	}
}

func TestPlanModelDeploymentContextUsesFineTunedModelIdentity(t *testing.T) {
	responses := modelPlanResponses(10, 0, 20, 15)
	model := map[string]interface{}{
		"name": "fine-tuned-support", "version": "1", "format": "OpenAI",
		"baseModel": map[string]interface{}{
			"name": "gpt-4.1", "version": "2025-04-14", "format": "OpenAI",
		},
		"skus": catalogSKUs(10),
	}
	accountCatalog := map[string]interface{}{"value": []interface{}{model}}
	regionalCatalog := map[string]interface{}{"value": []interface{}{map[string]interface{}{
		"model": model, "skuName": "GlobalStandard",
	}}}
	responses[2] = response(http.StatusOK, accountCatalog)
	responses[3] = response(http.StatusOK, regionalCatalog)
	desired := baseModelDeploymentDesired()
	desired.Name = "fine-tuned-support"
	desired.ModelName = "fine-tuned-support"
	desired.ModelVersion = "1"
	plan, err := PlanModelDeploymentContext(
		context.Background(),
		baseProject(),
		desired,
		&recordingCredential{},
		&recordingHTTPClient{responses: responses},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || plan.Desired.ModelName != "fine-tuned-support" {
		t.Fatalf("fine-tuned identity was not preserved: %#v", plan)
	}
}

func TestPlanModelDeploymentContextValidatesOptionalDependencies(t *testing.T) {
	responses := modelPlanResponses(10, 0, 20, 15)
	responses = append(
		responses,
		response(http.StatusOK, map[string]interface{}{
			"value": []interface{}{
				map[string]interface{}{"name": "StrictPolicy"},
			},
		}),
		response(http.StatusOK, deploymentPayload(
			"support-spillover",
			"gpt-5-mini",
			"2025-08-07",
			"GlobalStandard",
			10,
		)),
	)
	desired := baseModelDeploymentDesired()
	desired.RAIPolicyName = "StrictPolicy"
	desired.SpilloverDeploymentName = "support-spillover"
	plan, err := PlanModelDeploymentContext(
		context.Background(),
		baseProject(),
		desired,
		&recordingCredential{},
		&recordingHTTPClient{responses: responses},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready ||
		plan.Checks[len(plan.Checks)-2].Name != "rai-policy" ||
		plan.Checks[len(plan.Checks)-1].Name != "spillover" {
		t.Fatalf("optional dependencies were not validated: %#v", plan.Checks)
	}
}

func TestCreateModelDeploymentContextUsesCreateOnlyAndWaitsForSuccess(t *testing.T) {
	responses := []*http.Response{
		response(http.StatusNotFound, map[string]interface{}{"error": "missing"}),
		response(http.StatusAccepted, map[string]interface{}{}),
		response(http.StatusOK, deploymentPayload(
			"gpt-5-mini",
			"gpt-5-mini",
			"2025-08-07",
			"GlobalStandard",
			10,
		)),
	}
	httpClient := &recordingHTTPClient{responses: responses}
	state, created, err := CreateModelDeploymentContext(
		context.Background(),
		baseProject(),
		baseModelDeploymentDesired(),
		time.Second,
		time.Millisecond,
		&recordingCredential{},
		httpClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !created || state.ProvisioningState != "Succeeded" {
		t.Fatalf("unexpected create result: created=%t state=%#v", created, state)
	}
	put := httpClient.requests[1]
	if put.Method != http.MethodPut || put.Header.Get("If-None-Match") != "*" {
		t.Fatalf("create must use a create-only conditional request: %s %#v", put.Method, put.Header)
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(put.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	sku, ok := payload["sku"].(map[string]interface{})
	if !ok || sku["name"] != "GlobalStandard" || sku["capacity"] != float64(10) {
		t.Fatalf("unexpected create payload: %#v", payload)
	}
}

func TestDeleteModelDeploymentContextWaitsForAbsence(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusOK, deploymentPayload(
			"gpt-5-mini",
			"gpt-5-mini",
			"2025-08-07",
			"GlobalStandard",
			10,
		)),
		response(http.StatusAccepted, map[string]interface{}{}),
		response(http.StatusNotFound, map[string]interface{}{"error": "missing"}),
	}}
	deleted, err := DeleteModelDeploymentContext(
		context.Background(),
		baseProject(),
		"gpt-5-mini",
		time.Second,
		time.Millisecond,
		&recordingCredential{},
		httpClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted || len(httpClient.requests) != 3 || httpClient.requests[1].Method != http.MethodDelete {
		t.Fatalf("unexpected delete result: deleted=%t requests=%#v", deleted, httpClient.requests)
	}
}

func baseModelDeploymentDesired() ModelDeploymentDesired {
	return ModelDeploymentDesired{
		Name:         "gpt-5-mini",
		ModelName:    "gpt-5-mini",
		ModelVersion: "2025-08-07",
		ModelFormat:  "OpenAI",
		SKUName:      "GlobalStandard",
		Capacity:     10,
	}
}

func modelPlanResponses(
	defaultCapacity int64,
	currentQuota int64,
	quotaLimit int64,
	availableCapacity int64,
) []*http.Response {
	model := map[string]interface{}{
		"name":    "gpt-5-mini",
		"version": "2025-08-07",
		"format":  "OpenAI",
		"skus":    catalogSKUs(defaultCapacity),
	}
	accountCatalog := map[string]interface{}{"value": []interface{}{model}}
	regionalCatalog := map[string]interface{}{"value": []interface{}{map[string]interface{}{
		"model": model, "skuName": "GlobalStandard",
	}}}
	return []*http.Response{
		response(http.StatusNotFound, map[string]interface{}{"error": "missing"}),
		response(http.StatusOK, map[string]interface{}{"location": "eastus"}),
		response(http.StatusOK, accountCatalog),
		response(http.StatusOK, regionalCatalog),
		response(http.StatusOK, map[string]interface{}{
			"value": []interface{}{map[string]interface{}{
				"name":         map[string]interface{}{"value": "OpenAI.GlobalStandard.gpt-5-mini"},
				"currentValue": currentQuota,
				"limit":        quotaLimit,
			}},
		}),
		response(http.StatusOK, map[string]interface{}{
			"value": []interface{}{map[string]interface{}{
				"location": "eastus",
				"properties": map[string]interface{}{
					"skuName": "GlobalStandard", "availableCapacity": availableCapacity,
				},
			}},
		}),
	}
}

func catalogSKUs(defaultCapacity int64) []interface{} {
	return []interface{}{map[string]interface{}{
		"name":      "GlobalStandard",
		"usageName": "OpenAI.GlobalStandard.gpt-5-mini",
		"capacity": map[string]interface{}{
			"default": defaultCapacity,
			"minimum": 1,
			"maximum": 100,
			"step":    1,
		},
	}}
}

func deploymentPayload(
	name string,
	modelName string,
	modelVersion string,
	skuName string,
	capacity int64,
) map[string]interface{} {
	return map[string]interface{}{
		"id":   "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct/deployments/" + name,
		"name": name,
		"sku": map[string]interface{}{
			"name": skuName, "capacity": capacity,
		},
		"properties": map[string]interface{}{
			"model": map[string]interface{}{
				"name": modelName, "version": modelVersion, "format": "OpenAI",
			},
			"provisioningState": "Succeeded",
		},
	}
}
