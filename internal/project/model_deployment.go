package project

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"foundry-agent-manager/internal/arm"
	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	modelDeploymentAPIVersion       = "2025-06-01"
	maxModelDeploymentResponseBytes = int64(1024 * 1024)
	maxModelDeploymentPages         = 100
	maxModelDeploymentItems         = 10000
)

// ModelDeploymentDesired is the exact desired ARM deployment configuration.
type ModelDeploymentDesired struct {
	Name                    string `json:"name" yaml:"name"`
	ModelName               string `json:"modelName" yaml:"modelName"`
	ModelVersion            string `json:"modelVersion" yaml:"modelVersion"`
	ModelFormat             string `json:"modelFormat" yaml:"modelFormat"`
	SKUName                 string `json:"skuName" yaml:"skuName"`
	Capacity                int    `json:"capacity" yaml:"capacity"`
	RAIPolicyName           string `json:"raiPolicyName,omitempty" yaml:"raiPolicyName,omitempty"`
	VersionUpgradeOption    string `json:"versionUpgradeOption,omitempty" yaml:"versionUpgradeOption,omitempty"`
	SpilloverDeploymentName string `json:"spilloverDeploymentName,omitempty" yaml:"spilloverDeploymentName,omitempty"`
}

// ModelDeploymentState is the ARM state of a model deployment on the parent account.
type ModelDeploymentState struct {
	Exists                  bool   `json:"exists" yaml:"exists"`
	ID                      string `json:"id,omitempty" yaml:"id,omitempty"`
	Name                    string `json:"name" yaml:"name"`
	ModelName               string `json:"modelName,omitempty" yaml:"modelName,omitempty"`
	ModelVersion            string `json:"modelVersion,omitempty" yaml:"modelVersion,omitempty"`
	ModelFormat             string `json:"modelFormat,omitempty" yaml:"modelFormat,omitempty"`
	SKUName                 string `json:"skuName,omitempty" yaml:"skuName,omitempty"`
	Capacity                int    `json:"capacity,omitempty" yaml:"capacity,omitempty"`
	RAIPolicyName           string `json:"raiPolicyName,omitempty" yaml:"raiPolicyName,omitempty"`
	VersionUpgradeOption    string `json:"versionUpgradeOption,omitempty" yaml:"versionUpgradeOption,omitempty"`
	SpilloverDeploymentName string `json:"spilloverDeploymentName,omitempty" yaml:"spilloverDeploymentName,omitempty"`
	ProvisioningState       string `json:"provisioningState,omitempty" yaml:"provisioningState,omitempty"`
}

// ModelCapacityConfig contains the live capacity constraints for one model SKU.
type ModelCapacityConfig struct {
	Minimum       int   `json:"minimum,omitempty" yaml:"minimum,omitempty"`
	Maximum       int   `json:"maximum,omitempty" yaml:"maximum,omitempty"`
	Step          int   `json:"step,omitempty" yaml:"step,omitempty"`
	Default       int   `json:"default,omitempty" yaml:"default,omitempty"`
	AllowedValues []int `json:"allowedValues,omitempty" yaml:"allowedValues,omitempty"`
}

// ModelSKU describes one live deployment SKU exposed by the model catalog.
type ModelSKU struct {
	Name       string              `json:"name" yaml:"name"`
	UsageName  string              `json:"usageName,omitempty" yaml:"usageName,omitempty"`
	Capacity   ModelCapacityConfig `json:"capacity" yaml:"capacity"`
	Deprecated string              `json:"deprecationDate,omitempty" yaml:"deprecationDate,omitempty"`
}

// ModelCatalogEntry describes one exact model version available to the account.
type ModelCatalogEntry struct {
	Name            string     `json:"name" yaml:"name"`
	Version         string     `json:"version" yaml:"version"`
	Format          string     `json:"format" yaml:"format"`
	Publisher       string     `json:"publisher,omitempty" yaml:"publisher,omitempty"`
	LifecycleStatus string     `json:"lifecycleStatus,omitempty" yaml:"lifecycleStatus,omitempty"`
	SKUs            []ModelSKU `json:"skus,omitempty" yaml:"skus,omitempty"`
}

// ModelQuotaState reports the live regional quota metric for the selected SKU.
type ModelQuotaState struct {
	Applicable    bool    `json:"applicable" yaml:"applicable"`
	UsageName     string  `json:"usageName,omitempty" yaml:"usageName,omitempty"`
	Status        string  `json:"status,omitempty" yaml:"status,omitempty"`
	Unit          string  `json:"unit,omitempty" yaml:"unit,omitempty"`
	Limit         float64 `json:"limit,omitempty" yaml:"limit,omitempty"`
	Current       float64 `json:"current,omitempty" yaml:"current,omitempty"`
	Available     float64 `json:"available,omitempty" yaml:"available,omitempty"`
	NextResetTime string  `json:"nextResetTime,omitempty" yaml:"nextResetTime,omitempty"`
}

// ModelRegionalCapacity reports deployable capacity for one model/SKU/region.
type ModelRegionalCapacity struct {
	Location  string  `json:"location" yaml:"location"`
	SKUName   string  `json:"skuName" yaml:"skuName"`
	Available float64 `json:"available" yaml:"available"`
}

// ModelDeploymentCheck is one live validation performed by model deployment plan.
type ModelDeploymentCheck struct {
	Name   string `json:"name" yaml:"name"`
	Status string `json:"status" yaml:"status"`
	Detail string `json:"detail" yaml:"detail"`
}

// ModelDeploymentPlan is the live, non-mutating decision for a desired deployment.
type ModelDeploymentPlan struct {
	Location         string                 `json:"location" yaml:"location"`
	Action           string                 `json:"action" yaml:"action"`
	Ready            bool                   `json:"ready" yaml:"ready"`
	Desired          ModelDeploymentDesired `json:"desired" yaml:"desired"`
	Existing         *ModelDeploymentState  `json:"existing,omitempty" yaml:"existing,omitempty"`
	Model            *ModelCatalogEntry     `json:"model,omitempty" yaml:"model,omitempty"`
	Quota            ModelQuotaState        `json:"quota" yaml:"quota"`
	RegionalCapacity ModelRegionalCapacity  `json:"regionalCapacity" yaml:"regionalCapacity"`
	Checks           []ModelDeploymentCheck `json:"checks" yaml:"checks"`
}

type modelDeploymentPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	SKU  struct {
		Name     string `json:"name"`
		Capacity int    `json:"capacity"`
	} `json:"sku"`
	Properties struct {
		Model struct {
			Name      string `json:"name"`
			Version   string `json:"version"`
			Format    string `json:"format"`
			Publisher string `json:"publisher"`
		} `json:"model"`
		RAIPolicyName           string `json:"raiPolicyName"`
		VersionUpgradeOption    string `json:"versionUpgradeOption"`
		SpilloverDeploymentName string `json:"spilloverDeploymentName"`
		ProvisioningState       string `json:"provisioningState"`
	} `json:"properties"`
}

type modelSKUPayload struct {
	Name            string `json:"name"`
	UsageName       string `json:"usageName"`
	DeprecationDate string `json:"deprecationDate"`
	Capacity        struct {
		Minimum       int   `json:"minimum"`
		Maximum       int   `json:"maximum"`
		Step          int   `json:"step"`
		Default       int   `json:"default"`
		AllowedValues []int `json:"allowedValues"`
	} `json:"capacity"`
}

type accountModelPayload struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Format          string            `json:"format"`
	Publisher       string            `json:"publisher"`
	LifecycleStatus string            `json:"lifecycleStatus"`
	SKUs            []modelSKUPayload `json:"skus"`
	BaseModel       struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Format    string `json:"format"`
		Publisher string `json:"publisher"`
	} `json:"baseModel"`
}

type usagePayload struct {
	Unit string `json:"unit"`
	Name struct {
		Value string `json:"value"`
	} `json:"name"`
	Limit         float64 `json:"limit"`
	CurrentValue  float64 `json:"currentValue"`
	NextResetTime string  `json:"nextResetTime"`
	Status        string  `json:"status"`
}

// InspectModelDeploymentContext checks the parent Foundry account for a model
// deployment without creating a project, deployment, or inference request.
func InspectModelDeploymentContext(
	ctx context.Context,
	project *config.ProjectSpec,
	deploymentName string,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) (ModelDeploymentState, error) {
	if err := validateModelAccount(project, "model deployment inspection"); err != nil {
		return ModelDeploymentState{}, err
	}
	deploymentName = strings.TrimSpace(deploymentName)
	if deploymentName == "" {
		return ModelDeploymentState{}, errs.Config(
			"model deployment inspection requires agent.model or --deployment-name",
		)
	}
	requestURL, err := modelDeploymentURL(project, deploymentName)
	if err != nil {
		return ModelDeploymentState{}, err
	}
	resp, err := doModelARM(ctx, httpClient, http.MethodGet, requestURL, nil, nil, project, cred)
	if err != nil {
		return ModelDeploymentState{}, errs.FoundryWrap(err, "model deployment inspection failed")
	}
	data, err := readModelARMResponse(resp, "model deployment inspection")
	if err != nil {
		return ModelDeploymentState{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return ModelDeploymentState{Exists: false, Name: deploymentName}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return ModelDeploymentState{}, httpx.ResponseError(
			"ARM",
			"model deployment inspection",
			resp,
			data,
		)
	}
	state, err := decodeModelDeployment(data)
	if err != nil {
		return ModelDeploymentState{}, err
	}
	if !strings.EqualFold(state.Name, deploymentName) {
		return ModelDeploymentState{}, errs.Conflict(
			"ARM returned model deployment name %q instead of %q",
			state.Name,
			deploymentName,
		)
	}
	return state, nil
}

// ListModelDeploymentsContext lists account-level model deployments.
func ListModelDeploymentsContext(
	ctx context.Context,
	project *config.ProjectSpec,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) ([]ModelDeploymentState, error) {
	if err := validateModelAccount(project, "model deployment listing"); err != nil {
		return nil, err
	}
	requestURL, err := modelDeploymentsURL(project)
	if err != nil {
		return nil, err
	}
	var result []ModelDeploymentState
	for page := 0; requestURL != ""; page++ {
		if page >= maxModelDeploymentPages {
			return nil, errs.Foundry(
				"model deployment listing exceeded %d pages",
				maxModelDeploymentPages,
			)
		}
		resp, err := doModelARM(ctx, httpClient, http.MethodGet, requestURL, nil, nil, project, cred)
		if err != nil {
			return nil, errs.FoundryWrap(err, "model deployment listing failed")
		}
		data, err := readModelARMResponse(resp, "model deployment listing")
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, httpx.ResponseError("ARM", "model deployment listing", resp, data)
		}
		var payload struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"nextLink"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse model deployment list response")
		}
		for _, raw := range payload.Value {
			state, err := decodeModelDeployment(raw)
			if err != nil {
				return nil, err
			}
			result = append(result, state)
			if len(result) > maxModelDeploymentItems {
				return nil, errs.Foundry(
					"model deployment listing exceeded %d items",
					maxModelDeploymentItems,
				)
			}
		}
		requestURL, err = validateModelARMNextLink(project, payload.NextLink)
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

// PlanModelDeploymentContext validates exact catalog, SKU, quota, and regional
// capacity without mutating Azure.
func PlanModelDeploymentContext(
	ctx context.Context,
	project *config.ProjectSpec,
	desired ModelDeploymentDesired,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) (ModelDeploymentPlan, error) {
	if err := validateModelDesired(desired); err != nil {
		return ModelDeploymentPlan{}, err
	}
	if err := validateModelAccount(project, "model deployment planning"); err != nil {
		return ModelDeploymentPlan{}, err
	}
	plan := ModelDeploymentPlan{Desired: desired, Action: "create"}
	existing, err := InspectModelDeploymentContext(ctx, project, desired.Name, cred, httpClient)
	if err != nil {
		return ModelDeploymentPlan{}, err
	}
	if existing.Exists {
		plan.Existing = &existing
		drift := ModelDeploymentDrift(existing, desired)
		if len(drift) > 0 {
			return ModelDeploymentPlan{}, errs.Conflict(
				"model deployment %q already exists with different configuration: %s; explicit updates are not supported",
				desired.Name,
				strings.Join(drift, ", "),
			)
		}
		if existing.ProvisioningState != "Succeeded" {
			return ModelDeploymentPlan{}, errs.Conflict(
				"model deployment %q matches the requested configuration but provisioning state is %q",
				desired.Name,
				existing.ProvisioningState,
			)
		}
		plan.Action = "unchanged"
		plan.Ready = true
		plan.Checks = append(plan.Checks, ModelDeploymentCheck{
			Name:   "deployment",
			Status: "passed",
			Detail: "the existing deployment exactly matches the requested managed fields",
		})
		return plan, nil
	}
	plan.Checks = append(plan.Checks, ModelDeploymentCheck{
		Name:   "deployment",
		Status: "passed",
		Detail: "the deployment name is available for creation",
	})

	location, err := inspectModelAccountLocation(ctx, project, cred, httpClient)
	if err != nil {
		return ModelDeploymentPlan{}, err
	}
	plan.Location = location
	plan.Checks = append(plan.Checks, ModelDeploymentCheck{
		Name:   "account",
		Status: "passed",
		Detail: fmt.Sprintf(
			"Foundry account %s is in %s",
			project.AccountName,
			location,
		),
	})

	accountModels, err := listAccountModelsContext(ctx, project, cred, httpClient)
	if err != nil {
		return ModelDeploymentPlan{}, err
	}
	accountModel, ok := findCatalogModel(
		accountModels,
		desired.ModelName,
		desired.ModelVersion,
		desired.ModelFormat,
	)
	if !ok {
		return ModelDeploymentPlan{}, errs.NotFound(
			"model %s/%s format %s is not available to Foundry account %q",
			desired.ModelName,
			desired.ModelVersion,
			desired.ModelFormat,
			project.AccountName,
		)
	}
	plan.Model = &accountModel
	modelStatus := "passed"
	modelDetail := fmt.Sprintf(
		"exact model %s/%s format %s is available to the account",
		desired.ModelName,
		desired.ModelVersion,
		desired.ModelFormat,
	)
	if strings.EqualFold(accountModel.LifecycleStatus, "Deprecated") {
		return ModelDeploymentPlan{}, errs.Conflict(
			"model %s/%s is deprecated and cannot be planned for a new deployment",
			desired.ModelName,
			desired.ModelVersion,
		)
	}
	if strings.EqualFold(accountModel.LifecycleStatus, "Deprecating") {
		modelStatus = "warning"
		modelDetail += "; the catalog reports lifecycle status Deprecating"
	}
	plan.Checks = append(plan.Checks, ModelDeploymentCheck{
		Name:   "model-reference",
		Status: modelStatus,
		Detail: modelDetail,
	})

	locationModels, err := listLocationModelsContext(
		ctx,
		project,
		location,
		cred,
		httpClient,
	)
	if err != nil {
		return ModelDeploymentPlan{}, err
	}
	locationModel, ok := findCatalogModel(
		locationModels,
		desired.ModelName,
		desired.ModelVersion,
		desired.ModelFormat,
	)
	if !ok {
		return ModelDeploymentPlan{}, errs.NotFound(
			"model %s/%s format %s is not deployable in region %s",
			desired.ModelName,
			desired.ModelVersion,
			desired.ModelFormat,
			location,
		)
	}
	selectedSKU, ok := findModelSKU(locationModel.SKUs, desired.SKUName)
	if !ok {
		selectedSKU, ok = findModelSKU(accountModel.SKUs, desired.SKUName)
	}
	if !ok {
		return ModelDeploymentPlan{}, errs.NotFound(
			"SKU %q is not available for model %s/%s in %s",
			desired.SKUName,
			desired.ModelName,
			desired.ModelVersion,
			location,
		)
	}
	if err := validateRequestedCapacity(desired.Capacity, selectedSKU.Capacity); err != nil {
		return ModelDeploymentPlan{}, err
	}
	plan.Model.SKUs = []ModelSKU{selectedSKU}
	skuStatus := "passed"
	skuDetail := fmt.Sprintf(
		"SKU %s is advertised for the exact model version in %s",
		selectedSKU.Name,
		location,
	)
	if selectedSKU.Deprecated != "" {
		skuStatus = "warning"
		skuDetail += "; catalog deprecation date=" + selectedSKU.Deprecated
	}
	plan.Checks = append(plan.Checks,
		ModelDeploymentCheck{
			Name:   "sku",
			Status: skuStatus,
			Detail: skuDetail,
		},
		ModelDeploymentCheck{
			Name:   "capacity-shape",
			Status: "passed",
			Detail: capacityShapeDetail(desired.Capacity, selectedSKU.Capacity),
		},
	)

	quota, err := inspectModelQuotaContext(
		ctx,
		project,
		location,
		desired,
		selectedSKU,
		cred,
		httpClient,
	)
	if err != nil {
		return ModelDeploymentPlan{}, err
	}
	plan.Quota = quota
	quotaStatus := "not-applicable"
	quotaDetail := "the selected model SKU does not expose a Cognitive Services quota metric"
	if quota.Applicable {
		quotaStatus = "passed"
		quotaDetail = fmt.Sprintf(
			"quota %s has %.0f available units; %.0f requested",
			quota.UsageName,
			quota.Available,
			float64(desired.Capacity),
		)
	}
	plan.Checks = append(plan.Checks, ModelDeploymentCheck{
		Name:   "quota",
		Status: quotaStatus,
		Detail: quotaDetail,
	})

	regionalCapacity, err := inspectRegionalModelCapacityContext(
		ctx,
		project,
		location,
		desired,
		cred,
		httpClient,
	)
	if err != nil {
		return ModelDeploymentPlan{}, err
	}
	plan.RegionalCapacity = regionalCapacity
	plan.Checks = append(plan.Checks, ModelDeploymentCheck{
		Name:   "regional-capacity",
		Status: "passed",
		Detail: fmt.Sprintf(
			"%s reports %.0f available %s capacity units; %d requested",
			location,
			regionalCapacity.Available,
			desired.SKUName,
			desired.Capacity,
		),
	})

	if desired.RAIPolicyName != "" {
		if err := InspectRAIPolicyContext(
			ctx,
			project,
			desired.RAIPolicyName,
			cred,
			httpClient,
		); err != nil {
			return ModelDeploymentPlan{}, err
		}
		plan.Checks = append(plan.Checks, ModelDeploymentCheck{
			Name:   "rai-policy",
			Status: "passed",
			Detail: fmt.Sprintf("RAI policy %s exists on the account", desired.RAIPolicyName),
		})
	}
	if desired.SpilloverDeploymentName != "" {
		spillover, err := InspectModelDeploymentContext(
			ctx,
			project,
			desired.SpilloverDeploymentName,
			cred,
			httpClient,
		)
		if err != nil {
			return ModelDeploymentPlan{}, err
		}
		if !spillover.Exists {
			return ModelDeploymentPlan{}, errs.NotFound(
				"spillover model deployment %q does not exist",
				desired.SpilloverDeploymentName,
			)
		}
		if spillover.ProvisioningState != "Succeeded" {
			return ModelDeploymentPlan{}, errs.Conflict(
				"spillover model deployment %q is not ready: provisioning state %q",
				desired.SpilloverDeploymentName,
				spillover.ProvisioningState,
			)
		}
		plan.Checks = append(plan.Checks, ModelDeploymentCheck{
			Name:   "spillover",
			Status: "passed",
			Detail: fmt.Sprintf(
				"spillover deployment %s exists and is ready",
				desired.SpilloverDeploymentName,
			),
		})
	}
	plan.Ready = true
	return plan, nil
}

// CreateModelDeploymentContext creates only a missing deployment. It returns
// unchanged for an exact existing match and rejects every drifted deployment.
func CreateModelDeploymentContext(
	ctx context.Context,
	project *config.ProjectSpec,
	desired ModelDeploymentDesired,
	waitTimeout time.Duration,
	waitInterval time.Duration,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) (ModelDeploymentState, bool, error) {
	if err := validateModelDesired(desired); err != nil {
		return ModelDeploymentState{}, false, err
	}
	existing, err := InspectModelDeploymentContext(ctx, project, desired.Name, cred, httpClient)
	if err != nil {
		return ModelDeploymentState{}, false, err
	}
	if existing.Exists {
		if drift := ModelDeploymentDrift(existing, desired); len(drift) > 0 {
			return ModelDeploymentState{}, false, errs.Conflict(
				"model deployment %q already exists with different configuration: %s; delete it explicitly before recreating it",
				desired.Name,
				strings.Join(drift, ", "),
			)
		}
		if existing.ProvisioningState != "Succeeded" {
			return ModelDeploymentState{}, false, errs.Conflict(
				"model deployment %q exists in provisioning state %q",
				desired.Name,
				existing.ProvisioningState,
			)
		}
		return existing, false, nil
	}

	requestBody := struct {
		SKU struct {
			Name     string `json:"name"`
			Capacity int    `json:"capacity"`
		} `json:"sku"`
		Properties struct {
			Model struct {
				Format  string `json:"format"`
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"model"`
			RAIPolicyName           string `json:"raiPolicyName,omitempty"`
			VersionUpgradeOption    string `json:"versionUpgradeOption,omitempty"`
			SpilloverDeploymentName string `json:"spilloverDeploymentName,omitempty"`
		} `json:"properties"`
	}{}
	requestBody.SKU.Name = desired.SKUName
	requestBody.SKU.Capacity = desired.Capacity
	requestBody.Properties.Model.Format = desired.ModelFormat
	requestBody.Properties.Model.Name = desired.ModelName
	requestBody.Properties.Model.Version = desired.ModelVersion
	requestBody.Properties.RAIPolicyName = desired.RAIPolicyName
	requestBody.Properties.VersionUpgradeOption = desired.VersionUpgradeOption
	requestBody.Properties.SpilloverDeploymentName = desired.SpilloverDeploymentName
	body, err := json.Marshal(requestBody)
	if err != nil {
		return ModelDeploymentState{}, false, errs.FoundryWrap(
			err,
			"failed to encode model deployment creation request",
		)
	}
	requestURL, err := modelDeploymentURL(project, desired.Name)
	if err != nil {
		return ModelDeploymentState{}, false, err
	}
	resp, err := doModelARM(
		ctx,
		httpClient,
		http.MethodPut,
		requestURL,
		body,
		map[string]string{"If-None-Match": "*"},
		project,
		cred,
	)
	if err != nil {
		return ModelDeploymentState{}, false, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "model deployment %q creation failed", desired.Name),
		)
	}
	data, readErr := readModelARMResponse(resp, "model deployment creation")
	if readErr != nil {
		return ModelDeploymentState{}, false, errs.AmbiguousMutation(readErr)
	}
	if resp.StatusCode == http.StatusConflict ||
		resp.StatusCode == http.StatusPreconditionFailed {
		reconciled, inspectErr := InspectModelDeploymentContext(
			ctx,
			project,
			desired.Name,
			cred,
			httpClient,
		)
		if inspectErr == nil && reconciled.Exists {
			if drift := ModelDeploymentDrift(reconciled, desired); len(drift) == 0 &&
				reconciled.ProvisioningState == "Succeeded" {
				return reconciled, false, nil
			}
		}
		return ModelDeploymentState{}, false, httpx.ResponseError(
			"ARM",
			"model deployment creation",
			resp,
			data,
		)
	}
	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusCreated &&
		resp.StatusCode != http.StatusAccepted {
		responseErr := httpx.ResponseError(
			"ARM",
			"model deployment creation",
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return ModelDeploymentState{}, false, errs.AmbiguousMutation(responseErr)
		}
		return ModelDeploymentState{}, false, responseErr
	}
	state, err := waitForModelDeployment(
		ctx,
		project,
		desired.Name,
		false,
		waitTimeout,
		waitInterval,
		cred,
		httpClient,
	)
	if err != nil {
		return ModelDeploymentState{}, false, errs.AmbiguousMutation(err)
	}
	if drift := ModelDeploymentDrift(state, desired); len(drift) > 0 {
		return ModelDeploymentState{}, true, errs.Conflict(
			"created model deployment %q does not match the requested configuration: %s",
			desired.Name,
			strings.Join(drift, ", "),
		)
	}
	return state, true, nil
}

// DeleteModelDeploymentContext deletes one model deployment and waits until ARM
// confirms it is absent. Missing deployments are an idempotent no-op.
func DeleteModelDeploymentContext(
	ctx context.Context,
	project *config.ProjectSpec,
	deploymentName string,
	waitTimeout time.Duration,
	waitInterval time.Duration,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) (bool, error) {
	existing, err := InspectModelDeploymentContext(ctx, project, deploymentName, cred, httpClient)
	if err != nil {
		return false, err
	}
	if !existing.Exists {
		return false, nil
	}
	requestURL, err := modelDeploymentURL(project, deploymentName)
	if err != nil {
		return false, err
	}
	resp, err := doModelARM(ctx, httpClient, http.MethodDelete, requestURL, nil, nil, project, cred)
	if err != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "model deployment %q deletion failed", deploymentName),
		)
	}
	data, readErr := readModelARMResponse(resp, "model deployment deletion")
	if readErr != nil {
		return false, errs.AmbiguousMutation(readErr)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		return true, nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		responseErr := httpx.ResponseError(
			"ARM",
			"model deployment deletion",
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return false, errs.AmbiguousMutation(responseErr)
		}
		return false, responseErr
	}
	if _, err := waitForModelDeployment(
		ctx,
		project,
		deploymentName,
		true,
		waitTimeout,
		waitInterval,
		cred,
		httpClient,
	); err != nil {
		return false, errs.AmbiguousMutation(err)
	}
	return true, nil
}

// ModelDeploymentDrift returns managed fields that differ from the desired state.
func ModelDeploymentDrift(
	state ModelDeploymentState,
	desired ModelDeploymentDesired,
) []string {
	var drift []string
	compare := func(field, actual, expected string) {
		if !strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(expected)) {
			drift = append(drift, fmt.Sprintf("%s=%q (wanted %q)", field, actual, expected))
		}
	}
	compare("model.name", state.ModelName, desired.ModelName)
	compare("model.version", state.ModelVersion, desired.ModelVersion)
	compare("model.format", state.ModelFormat, desired.ModelFormat)
	compare("sku.name", state.SKUName, desired.SKUName)
	if state.Capacity != desired.Capacity {
		drift = append(drift, fmt.Sprintf(
			"sku.capacity=%d (wanted %d)",
			state.Capacity,
			desired.Capacity,
		))
	}
	if desired.RAIPolicyName != "" {
		compare("raiPolicyName", state.RAIPolicyName, desired.RAIPolicyName)
	}
	if desired.VersionUpgradeOption != "" {
		compare(
			"versionUpgradeOption",
			state.VersionUpgradeOption,
			desired.VersionUpgradeOption,
		)
	}
	if desired.SpilloverDeploymentName != "" {
		compare(
			"spilloverDeploymentName",
			state.SpilloverDeploymentName,
			desired.SpilloverDeploymentName,
		)
	}
	return drift
}

func inspectModelAccountLocation(
	ctx context.Context,
	project *config.ProjectSpec,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) (string, error) {
	requestURL, err := modelAccountURL(project)
	if err != nil {
		return "", err
	}
	resp, err := doModelARM(ctx, httpClient, http.MethodGet, requestURL, nil, nil, project, cred)
	if err != nil {
		return "", errs.FoundryWrap(err, "Foundry account inspection failed")
	}
	data, err := readModelARMResponse(resp, "Foundry account inspection")
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", httpx.ResponseError("ARM", "Foundry account inspection", resp, data)
	}
	var payload struct {
		Location string `json:"location"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", errs.FoundryWrap(err, "failed to parse Foundry account response")
	}
	if strings.TrimSpace(payload.Location) == "" {
		return "", errs.Foundry(
			"Foundry account %q response omitted its location",
			project.AccountName,
		)
	}
	return payload.Location, nil
}

func listAccountModelsContext(
	ctx context.Context,
	project *config.ProjectSpec,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) ([]ModelCatalogEntry, error) {
	requestURL, err := modelAccountModelsURL(project)
	if err != nil {
		return nil, err
	}
	return listCatalogPages(
		ctx,
		project,
		requestURL,
		"account model catalog",
		func(raw json.RawMessage) (ModelCatalogEntry, error) {
			var payload accountModelPayload
			if err := json.Unmarshal(raw, &payload); err != nil {
				return ModelCatalogEntry{}, err
			}
			return catalogEntryFromAccountModel(payload), nil
		},
		cred,
		httpClient,
	)
}

func listLocationModelsContext(
	ctx context.Context,
	project *config.ProjectSpec,
	location string,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) ([]ModelCatalogEntry, error) {
	requestURL, err := modelLocationModelsURL(project, location)
	if err != nil {
		return nil, err
	}
	return listCatalogPages(
		ctx,
		project,
		requestURL,
		"regional model catalog",
		func(raw json.RawMessage) (ModelCatalogEntry, error) {
			var payload struct {
				Model   accountModelPayload `json:"model"`
				SKUName string              `json:"skuName"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return ModelCatalogEntry{}, err
			}
			entry := catalogEntryFromAccountModel(payload.Model)
			if payload.SKUName != "" {
				if _, ok := findModelSKU(entry.SKUs, payload.SKUName); !ok {
					entry.SKUs = append(entry.SKUs, ModelSKU{Name: payload.SKUName})
				}
			}
			return entry, nil
		},
		cred,
		httpClient,
	)
}

func listCatalogPages(
	ctx context.Context,
	project *config.ProjectSpec,
	requestURL string,
	operation string,
	decode func(json.RawMessage) (ModelCatalogEntry, error),
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) ([]ModelCatalogEntry, error) {
	var result []ModelCatalogEntry
	for page := 0; requestURL != ""; page++ {
		if page >= maxModelDeploymentPages {
			return nil, errs.Foundry("%s exceeded %d pages", operation, maxModelDeploymentPages)
		}
		resp, err := doModelARM(ctx, httpClient, http.MethodGet, requestURL, nil, nil, project, cred)
		if err != nil {
			return nil, errs.FoundryWrap(err, "%s request failed", operation)
		}
		data, err := readModelARMResponse(resp, operation)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, httpx.ResponseError("ARM", operation, resp, data)
		}
		var pagePayload struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"nextLink"`
		}
		if err := json.Unmarshal(data, &pagePayload); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse %s response", operation)
		}
		for _, raw := range pagePayload.Value {
			entry, err := decode(raw)
			if err != nil {
				return nil, errs.FoundryWrap(err, "failed to parse %s item", operation)
			}
			if entry.Name == "" || entry.Version == "" || entry.Format == "" {
				return nil, errs.Foundry(
					"%s response omitted a model name, version, or format",
					operation,
				)
			}
			result = append(result, entry)
			if len(result) > maxModelDeploymentItems {
				return nil, errs.Foundry(
					"%s exceeded %d items",
					operation,
					maxModelDeploymentItems,
				)
			}
		}
		requestURL, err = validateModelARMNextLink(project, pagePayload.NextLink)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func inspectModelQuotaContext(
	ctx context.Context,
	project *config.ProjectSpec,
	location string,
	desired ModelDeploymentDesired,
	sku ModelSKU,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) (ModelQuotaState, error) {
	usageName := strings.TrimSpace(sku.UsageName)
	if usageName == "" && strings.EqualFold(desired.ModelFormat, "OpenAI") {
		usageName = "OpenAI." + desired.SKUName + "." + desired.ModelName
	}
	if usageName == "" {
		return ModelQuotaState{Applicable: false}, nil
	}
	requestURL, err := modelUsagesURL(project, location)
	if err != nil {
		return ModelQuotaState{}, err
	}
	var usages []usagePayload
	for page := 0; requestURL != ""; page++ {
		if page >= maxModelDeploymentPages {
			return ModelQuotaState{}, errs.Foundry(
				"model quota lookup exceeded %d pages",
				maxModelDeploymentPages,
			)
		}
		resp, err := doModelARM(ctx, httpClient, http.MethodGet, requestURL, nil, nil, project, cred)
		if err != nil {
			return ModelQuotaState{}, errs.FoundryWrap(err, "model quota lookup failed")
		}
		data, err := readModelARMResponse(resp, "model quota lookup")
		if err != nil {
			return ModelQuotaState{}, err
		}
		if resp.StatusCode != http.StatusOK {
			return ModelQuotaState{}, httpx.ResponseError("ARM", "model quota lookup", resp, data)
		}
		var payload struct {
			Value    []usagePayload `json:"value"`
			NextLink string         `json:"nextLink"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return ModelQuotaState{}, errs.FoundryWrap(
				err,
				"failed to parse model quota response",
			)
		}
		usages = append(usages, payload.Value...)
		if len(usages) > maxModelDeploymentItems {
			return ModelQuotaState{}, errs.Foundry(
				"model quota lookup exceeded %d items",
				maxModelDeploymentItems,
			)
		}
		requestURL, err = validateModelARMNextLink(project, payload.NextLink)
		if err != nil {
			return ModelQuotaState{}, err
		}
	}
	for _, usage := range usages {
		if !strings.EqualFold(usage.Name.Value, usageName) {
			continue
		}
		state := ModelQuotaState{
			Applicable:    true,
			UsageName:     usage.Name.Value,
			Status:        usage.Status,
			Unit:          usage.Unit,
			Limit:         usage.Limit,
			Current:       usage.CurrentValue,
			Available:     usage.Limit - usage.CurrentValue,
			NextResetTime: usage.NextResetTime,
		}
		if strings.EqualFold(state.Status, "Blocked") {
			return ModelQuotaState{}, errs.Conflict(
				"quota %s is blocked in %s",
				state.UsageName,
				location,
			)
		}
		if state.Available < float64(desired.Capacity) {
			return ModelQuotaState{}, errs.Conflict(
				"quota %s has %.0f available units in %s, but deployment %q requests %d",
				state.UsageName,
				state.Available,
				location,
				desired.Name,
				desired.Capacity,
			)
		}
		return state, nil
	}
	return ModelQuotaState{}, errs.NotFound(
		"quota metric %q was not returned for model %s/%s SKU %s in %s",
		usageName,
		desired.ModelName,
		desired.ModelVersion,
		desired.SKUName,
		location,
	)
}

func inspectRegionalModelCapacityContext(
	ctx context.Context,
	project *config.ProjectSpec,
	location string,
	desired ModelDeploymentDesired,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) (ModelRegionalCapacity, error) {
	requestURL, err := modelCapacityURL(project, location, desired)
	if err != nil {
		return ModelRegionalCapacity{}, err
	}
	resp, err := doModelARM(ctx, httpClient, http.MethodGet, requestURL, nil, nil, project, cred)
	if err != nil {
		return ModelRegionalCapacity{}, errs.FoundryWrap(
			err,
			"regional model capacity lookup failed",
		)
	}
	data, err := readModelARMResponse(resp, "regional model capacity lookup")
	if err != nil {
		return ModelRegionalCapacity{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return ModelRegionalCapacity{}, httpx.ResponseError(
			"ARM",
			"regional model capacity lookup",
			resp,
			data,
		)
	}
	var payload struct {
		Value []struct {
			Location   string `json:"location"`
			Properties struct {
				SKUName           string  `json:"skuName"`
				AvailableCapacity float64 `json:"availableCapacity"`
			} `json:"properties"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ModelRegionalCapacity{}, errs.FoundryWrap(
			err,
			"failed to parse regional model capacity response",
		)
	}
	for _, item := range payload.Value {
		if !strings.EqualFold(item.Properties.SKUName, desired.SKUName) {
			continue
		}
		if item.Properties.AvailableCapacity < float64(desired.Capacity) {
			return ModelRegionalCapacity{}, errs.Conflict(
				"regional capacity for model %s/%s SKU %s in %s is %.0f, but deployment %q requests %d",
				desired.ModelName,
				desired.ModelVersion,
				desired.SKUName,
				location,
				item.Properties.AvailableCapacity,
				desired.Name,
				desired.Capacity,
			)
		}
		return ModelRegionalCapacity{
			Location:  defaultModelString(item.Location, location),
			SKUName:   item.Properties.SKUName,
			Available: item.Properties.AvailableCapacity,
		}, nil
	}
	return ModelRegionalCapacity{}, errs.NotFound(
		"regional capacity was not returned for model %s/%s SKU %s in %s",
		desired.ModelName,
		desired.ModelVersion,
		desired.SKUName,
		location,
	)
}

// InspectRAIPolicyContext verifies that an account-level RAI policy exists.
func InspectRAIPolicyContext(
	ctx context.Context,
	project *config.ProjectSpec,
	name string,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) error {
	requestURL, err := modelRAIPolicyURL(project, name)
	if err != nil {
		return err
	}
	resp, err := doModelARM(ctx, httpClient, http.MethodGet, requestURL, nil, nil, project, cred)
	if err != nil {
		return errs.FoundryWrap(err, "RAI policy %q inspection failed", name)
	}
	data, err := readModelARMResponse(resp, "RAI policy inspection")
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return errs.NotFound("RAI policy %q does not exist on the Foundry account", name)
	}
	if resp.StatusCode != http.StatusOK {
		return httpx.ResponseError("ARM", "RAI policy inspection", resp, data)
	}
	return nil
}

func waitForModelDeployment(
	ctx context.Context,
	project *config.ProjectSpec,
	deploymentName string,
	wantAbsent bool,
	timeout time.Duration,
	interval time.Duration,
	cred azcore.TokenCredential,
	httpClient HTTPClient,
) (ModelDeploymentState, error) {
	if timeout <= 0 {
		return ModelDeploymentState{}, errs.Config(
			"model deployment wait timeout must be greater than zero",
		)
	}
	if interval <= 0 {
		return ModelDeploymentState{}, errs.Config(
			"model deployment wait interval must be greater than zero",
		)
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		state, err := InspectModelDeploymentContext(
			waitCtx,
			project,
			deploymentName,
			cred,
			httpClient,
		)
		if err != nil {
			return ModelDeploymentState{}, err
		}
		if wantAbsent && !state.Exists {
			return state, nil
		}
		if !wantAbsent && state.Exists {
			switch state.ProvisioningState {
			case "Succeeded":
				return state, nil
			case "Failed", "Canceled", "Disabled":
				return ModelDeploymentState{}, errs.Conflict(
					"model deployment %q entered provisioning state %q",
					deploymentName,
					state.ProvisioningState,
				)
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ModelDeploymentState{}, errs.Transient(
				"timed out after %s waiting for model deployment %q",
				timeout,
				deploymentName,
			)
		}
	}
}

func modelAccountURL(project *config.ProjectSpec) (string, error) {
	return modelARMURL(
		project,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
	)
}

func modelDeploymentsURL(project *config.ProjectSpec) (string, error) {
	return modelARMURL(
		project,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"deployments",
	)
}

func modelDeploymentURL(project *config.ProjectSpec, name string) (string, error) {
	return modelARMURL(
		project,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"deployments", name,
	)
}

func modelAccountModelsURL(project *config.ProjectSpec) (string, error) {
	return modelARMURL(
		project,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"models",
	)
}

func modelLocationModelsURL(project *config.ProjectSpec, location string) (string, error) {
	return modelARMURL(
		project,
		"subscriptions", project.SubscriptionID,
		"providers", "Microsoft.CognitiveServices",
		"locations", location,
		"models",
	)
}

func modelUsagesURL(project *config.ProjectSpec, location string) (string, error) {
	return modelARMURL(
		project,
		"subscriptions", project.SubscriptionID,
		"providers", "Microsoft.CognitiveServices",
		"locations", location,
		"usages",
	)
}

func modelCapacityURL(
	project *config.ProjectSpec,
	location string,
	desired ModelDeploymentDesired,
) (string, error) {
	raw, err := modelARMURL(
		project,
		"subscriptions", project.SubscriptionID,
		"providers", "Microsoft.CognitiveServices",
		"locations", location,
		"modelCapacities",
	)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to parse model capacity ARM URL")
	}
	query := parsed.Query()
	query.Set("modelFormat", desired.ModelFormat)
	query.Set("modelName", desired.ModelName)
	query.Set("modelVersion", desired.ModelVersion)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func modelRAIPolicyURL(project *config.ProjectSpec, name string) (string, error) {
	return modelARMURL(
		project,
		"subscriptions", project.SubscriptionID,
		"resourceGroups", project.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", project.AccountName,
		"raiPolicies", name,
	)
}

func modelARMURL(project *config.ProjectSpec, segments ...string) (string, error) {
	result, err := arm.ResourceURLForEndpoint(
		project.ARMEndpoint,
		modelDeploymentAPIVersion,
		segments...,
	)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to build model deployment ARM URL")
	}
	return result, nil
}

func doModelARM(
	ctx context.Context,
	httpClient HTTPClient,
	method string,
	requestURL string,
	body []byte,
	headers map[string]string,
	project *config.ProjectSpec,
	cred azcore.TokenCredential,
) (*http.Response, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{project.ARMScope},
	})
	if err != nil {
		return nil, errs.AuthWrap(err, "failed to get ARM token")
	}
	if strings.TrimSpace(token.Token) == "" {
		return nil, errs.Auth("ARM credential returned an empty token")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to create model deployment ARM request")
	}
	request.Header.Set("Authorization", "Bearer "+token.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	resp, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errs.Foundry("model deployment ARM request returned a nil response")
	}
	return resp, nil
}

func readModelARMResponse(resp *http.Response, operation string) ([]byte, error) {
	if resp == nil {
		return nil, errs.Foundry("%s returned a nil response", operation)
	}
	if resp.Body == nil {
		if resp.StatusCode == http.StatusNoContent {
			return nil, nil
		}
		return nil, errs.Foundry("%s response omitted its body", operation)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxModelDeploymentResponseBytes+1))
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read %s response", operation)
	}
	if int64(len(data)) > maxModelDeploymentResponseBytes {
		return nil, errs.Foundry(
			"%s response exceeds the %d-byte limit",
			operation,
			maxModelDeploymentResponseBytes,
		)
	}
	return data, nil
}

func decodeModelDeployment(data []byte) (ModelDeploymentState, error) {
	var payload modelDeploymentPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ModelDeploymentState{}, errs.FoundryWrap(
			err,
			"failed to parse model deployment response",
		)
	}
	if payload.Name == "" || payload.Properties.ProvisioningState == "" {
		return ModelDeploymentState{}, errs.Foundry(
			"model deployment response omitted its name or provisioning state",
		)
	}
	return ModelDeploymentState{
		Exists:                  true,
		ID:                      payload.ID,
		Name:                    payload.Name,
		ModelName:               payload.Properties.Model.Name,
		ModelVersion:            payload.Properties.Model.Version,
		ModelFormat:             payload.Properties.Model.Format,
		SKUName:                 payload.SKU.Name,
		Capacity:                payload.SKU.Capacity,
		RAIPolicyName:           payload.Properties.RAIPolicyName,
		VersionUpgradeOption:    payload.Properties.VersionUpgradeOption,
		SpilloverDeploymentName: payload.Properties.SpilloverDeploymentName,
		ProvisioningState:       payload.Properties.ProvisioningState,
	}, nil
}

func validateModelAccount(project *config.ProjectSpec, operation string) error {
	if err := config.ValidateARMRouting(project); err != nil {
		return err
	}
	for _, pair := range [][2]string{
		{"project.subscription_id", project.SubscriptionID},
		{"project.resource_group", project.ResourceGroup},
		{"project.account_name", project.AccountName},
	} {
		if strings.TrimSpace(pair[1]) == "" {
			return errs.Config("%s requires %s", operation, pair[0])
		}
	}
	return nil
}

func validateModelDesired(desired ModelDeploymentDesired) error {
	for _, required := range []struct {
		field string
		value string
	}{
		{field: "deployment name", value: desired.Name},
		{field: "model name", value: desired.ModelName},
		{field: "model version", value: desired.ModelVersion},
		{field: "model format", value: desired.ModelFormat},
		{field: "SKU name", value: desired.SKUName},
	} {
		if strings.TrimSpace(required.value) == "" {
			return errs.Config(
				"model deployment %s must not be empty",
				required.field,
			)
		}
	}
	if desired.Capacity <= 0 {
		return errs.Config("model deployment capacity must be greater than zero")
	}
	switch desired.VersionUpgradeOption {
	case "", "OnceNewDefaultVersionAvailable", "OnceCurrentVersionExpired", "NoAutoUpgrade":
	default:
		return errs.Config(
			"model deployment version upgrade option %q is invalid",
			desired.VersionUpgradeOption,
		)
	}
	if strings.EqualFold(desired.Name, desired.SpilloverDeploymentName) {
		return errs.Config("model deployment spillover name must differ from its deployment name")
	}
	return nil
}

func validateRequestedCapacity(capacity int, cfg ModelCapacityConfig) error {
	if len(cfg.AllowedValues) > 0 {
		for _, allowed := range cfg.AllowedValues {
			if capacity == allowed {
				return nil
			}
		}
		return errs.Config(
			"capacity %d is not allowed; supported values: %v",
			capacity,
			cfg.AllowedValues,
		)
	}
	if cfg.Minimum > 0 && capacity < cfg.Minimum {
		return errs.Config("capacity %d is below the live minimum %d", capacity, cfg.Minimum)
	}
	if cfg.Maximum > 0 && capacity > cfg.Maximum {
		return errs.Config("capacity %d exceeds the live maximum %d", capacity, cfg.Maximum)
	}
	if cfg.Step > 0 {
		base := cfg.Minimum
		if base <= 0 {
			base = cfg.Step
		}
		if capacity < base || (capacity-base)%cfg.Step != 0 {
			return errs.Config(
				"capacity %d does not follow the live increment %d from minimum %d",
				capacity,
				cfg.Step,
				base,
			)
		}
	}
	return nil
}

func capacityShapeDetail(capacity int, cfg ModelCapacityConfig) string {
	if len(cfg.AllowedValues) > 0 {
		return fmt.Sprintf("capacity %d is in the live allowed values %v", capacity, cfg.AllowedValues)
	}
	return fmt.Sprintf(
		"capacity %d satisfies live constraints minimum=%d maximum=%d step=%d",
		capacity,
		cfg.Minimum,
		cfg.Maximum,
		cfg.Step,
	)
}

func catalogEntryFromAccountModel(payload accountModelPayload) ModelCatalogEntry {
	entry := ModelCatalogEntry{
		Name:            payload.Name,
		Version:         payload.Version,
		Format:          payload.Format,
		Publisher:       payload.Publisher,
		LifecycleStatus: payload.LifecycleStatus,
	}
	for _, sku := range payload.SKUs {
		entry.SKUs = append(entry.SKUs, ModelSKU{
			Name:       sku.Name,
			UsageName:  sku.UsageName,
			Deprecated: sku.DeprecationDate,
			Capacity: ModelCapacityConfig{
				Minimum:       sku.Capacity.Minimum,
				Maximum:       sku.Capacity.Maximum,
				Step:          sku.Capacity.Step,
				Default:       sku.Capacity.Default,
				AllowedValues: append([]int(nil), sku.Capacity.AllowedValues...),
			},
		})
	}
	return entry
}

func findCatalogModel(
	models []ModelCatalogEntry,
	name string,
	version string,
	format string,
) (ModelCatalogEntry, bool) {
	for _, model := range models {
		if strings.EqualFold(model.Name, name) &&
			strings.EqualFold(model.Version, version) &&
			strings.EqualFold(model.Format, format) {
			return model, true
		}
	}
	return ModelCatalogEntry{}, false
}

func findModelSKU(skus []ModelSKU, name string) (ModelSKU, bool) {
	for _, sku := range skus {
		if strings.EqualFold(sku.Name, name) {
			return sku, true
		}
	}
	return ModelSKU{}, false
}

func validateModelARMNextLink(project *config.ProjectSpec, raw string) (string, error) {
	next, err := arm.ValidateNextLink(project.ARMEndpoint, raw)
	if err != nil {
		return "", errs.Security("model deployment pagination %v", err)
	}
	return next, nil
}

func defaultModelString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
