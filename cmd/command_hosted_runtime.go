package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/hosted"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type hostedRESTRuntime struct {
	Context         context.Context
	Profile         azcloud.Profile
	Workspace       hosted.Workspace
	Environment     string
	AZDPath         string
	Runner          hosted.Runner
	Tooling         hosted.PreflightResult
	Deployment      hosted.Status
	ProjectEndpoint string
	Client          *foundry.Client
	Agent           *foundry.Agent
}

func resolveHostedRESTRuntime(
	cmd *cobra.Command,
) (*hostedRESTRuntime, context.CancelFunc, error) {
	profile, workspace, err := resolveHostedWorkspace(cmd, true)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel, err := hostedExecutionContext(cmd)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*hostedRESTRuntime, context.CancelFunc, error) {
		cancel()
		return nil, nil, err
	}
	azdPath, err := hosted.ResolveAZD(profile.Name, hostedLookPathFn)
	if err != nil {
		return fail(hostedCommandError(err))
	}
	runner := newHostedRunner(cmd)
	tooling, err := hosted.CheckPreflight(ctx, hosted.PreflightOptions{
		Workspace:        workspace,
		AZDPath:          azdPath,
		Environment:      getFlag(cmd, "environment"),
		CheckEnvironment: true,
		Runner:           runner,
	})
	if err != nil {
		return fail(hostedCommandError(err))
	}
	status, _, err := hosted.ShowStatus(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		nil,
	)
	if err != nil {
		return fail(hostedCommandError(err))
	}
	endpoint, err := hosted.ResolveProjectEndpoint(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		nil,
	)
	if err != nil {
		return fail(hostedCommandError(err))
	}
	credential, err := newCredential(cmd, profile)
	if err != nil {
		return fail(err)
	}
	client := foundry.NewClientWithOptions(
		endpoint,
		credential,
		newHTTPClient(cmd),
		foundry.ClientOptions{Scope: profile.FoundryScope},
	)
	agent, err := client.GetAgentContext(ctx, status.Name)
	if err != nil {
		return fail(err)
	}
	if agent == nil {
		return fail(errs.NotFound(
			"deployed Hosted Agent %q was not found in the selected Foundry project",
			status.Name,
		))
	}
	if err := requireHostedAgentContext(ctx, client, agent, status.Version); err != nil {
		return fail(err)
	}
	return &hostedRESTRuntime{
		Context:         ctx,
		Profile:         profile,
		Workspace:       workspace,
		Environment:     getFlag(cmd, "environment"),
		AZDPath:         azdPath,
		Runner:          runner,
		Tooling:         tooling,
		Deployment:      status,
		ProjectEndpoint: endpoint,
		Client:          client,
		Agent:           agent,
	}, cancel, nil
}

func requireHostedAgentContext(
	ctx context.Context,
	client *foundry.Client,
	agent *foundry.Agent,
	fallbackVersion string,
) error {
	version := &agent.Versions.Latest
	if version.Version == "" && fallbackVersion != "" {
		resolved, err := client.GetAgentVersionContext(ctx, agent.Name, fallbackVersion)
		if err != nil {
			return err
		}
		if resolved != nil {
			version = resolved
		}
	}
	kind, _ := version.Definition["kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(kind), "hosted") {
		if kind == "" {
			kind = "<missing>"
		}
		return errs.Config(
			"remote agent %q is kind %s, not hosted; refusing to use Hosted lifecycle commands",
			agent.Name,
			kind,
		)
	}
	return nil
}

func requireHostedVersion(
	ctx context.Context,
	runtime *hostedRESTRuntime,
	version string,
) (*foundry.AgentVersion, error) {
	if strings.TrimSpace(version) == "" {
		return nil, errs.Config("--agent-version is required")
	}
	found, err := runtime.Client.GetAgentVersionContext(ctx, runtime.Agent.Name, version)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, errs.NotFound(
			"Hosted Agent %q version %s was not found",
			runtime.Agent.Name,
			version,
		)
	}
	kind, _ := found.Definition["kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(kind), "hosted") {
		return nil, errs.Config(
			"agent %q version %s is kind %q, not hosted",
			runtime.Agent.Name,
			version,
			kind,
		)
	}
	return found, nil
}

func sanitizeHostedAgent(agent *foundry.Agent) foundry.Agent {
	copy := *agent
	copy.Versions.Latest = sanitizeHostedVersion(agent.Versions.Latest)
	return copy
}

func sanitizeHostedVersion(version foundry.AgentVersion) foundry.AgentVersion {
	copy := version
	copy.Definition = sanitizeHostedMap(version.Definition)
	return copy
}

func sanitizeHostedMap(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}
	result := make(map[string]interface{}, len(source))
	for key, value := range source {
		lower := strings.ToLower(key)
		if lower == "environment_variables" ||
			lower == "environmentvariables" ||
			lower == "env" {
			if values, ok := value.(map[string]interface{}); ok {
				redacted := make(map[string]interface{}, len(values))
				for name := range values {
					redacted[name] = "<redacted>"
				}
				result[key] = redacted
				continue
			}
		}
		if strings.Contains(lower, "password") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "token") ||
			strings.HasSuffix(lower, "_key") {
			result[key] = "<redacted>"
			continue
		}
		switch typed := value.(type) {
		case map[string]interface{}:
			result[key] = sanitizeHostedMap(typed)
		case []interface{}:
			items := make([]interface{}, len(typed))
			for i, item := range typed {
				if object, ok := item.(map[string]interface{}); ok {
					items[i] = sanitizeHostedMap(object)
				} else {
					items[i] = item
				}
			}
			result[key] = items
		default:
			result[key] = value
		}
	}
	return result
}

func hostedSelectorState(agent *foundry.Agent) receipt.SelectorState {
	if agent == nil {
		return receipt.SelectorState{}
	}
	latest := agent.Versions.Latest.Version
	var selector *foundry.VersionSelector
	if agent.AgentEndpoint != nil {
		selector = agent.AgentEndpoint.VersionSelector
	}
	resolution := foundry.ResolveVersionSelector(selector, latest)
	raw, _ := json.Marshal(selector)
	activeVersion := ""
	if len(resolution.ActiveVersions) == 1 {
		activeVersion = resolution.ActiveVersions[0]
	}
	return receipt.SelectorState{
		Mode:          string(resolution.Mode),
		ActiveVersion: activeVersion,
		Raw:           raw,
	}
}

func newHostedOperationStore(
	cmd *cobra.Command,
	runtime *hostedRESTRuntime,
	operation string,
	desiredHash string,
) (*receipt.OperationStore, error) {
	path, err := hostedOperationReceiptPath(cmd, runtime.Workspace, operation)
	if err != nil {
		return nil, err
	}
	store, err := newManagedOperationStore(
		cmd,
		path,
		operation,
		runtime.Profile.Name,
		receipt.ManifestReference{
			Path:        runtime.Workspace.AzureYAML,
			Hash:        runtime.Workspace.Hash,
			DesiredHash: desiredHash,
		},
		receipt.ResourceReference{
			Name:     runtime.Workspace.Name,
			Endpoint: runtime.ProjectEndpoint,
		},
		runtime.Agent.Name,
	)
	if err != nil {
		return nil, err
	}
	store.Receipt.Metadata = custommetadata.MergeHosted(
		runtime.Workspace.Selected.Metadata,
		commandMetadata(cmd),
	)
	store.Receipt.Agent.ID = runtime.Agent.ID
	store.Receipt.Agent.LatestVersionBefore = runtime.Agent.Versions.Latest.Version
	store.Receipt.Agent.SelectorBefore = hostedSelectorState(runtime.Agent)
	if err := store.Save(); err != nil {
		return nil, err
	}
	return store, nil
}

func hostedOperationReceiptPath(
	cmd *cobra.Command,
	workspace hosted.Workspace,
	operation string,
) (string, error) {
	if path := getFlag(cmd, "receipt"); path != "" {
		if filepath.IsAbs(path) {
			return filepath.Clean(path), nil
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", errs.Config("failed to resolve --receipt path: %v", err)
		}
		return filepath.Clean(absolute), nil
	}
	return receipt.OperationPath(
		workspace.AzureYAML,
		operation,
		workspace.Selected.AgentName,
		nowUTC(),
	), nil
}

type hostedDeploymentComparison struct {
	Changed         bool
	Reasons         []string
	ReceiptPath     string
	ExpectedVersion string
	RemoteVersion   string
}

func compareHostedDeployment(
	runtime *hostedRESTRuntime,
	snapshot hosted.DeploymentSnapshot,
) (hostedDeploymentComparison, error) {
	comparison := hostedDeploymentComparison{
		Changed:       true,
		RemoteVersion: runtime.Deployment.Version,
	}
	found, path, err := latestHostedDeployReceipt(runtime)
	if err != nil {
		return comparison, err
	}
	if found == nil {
		comparison.Reasons = append(comparison.Reasons, "no matching successful Hosted deployment receipt was found")
		return comparison, nil
	}
	comparison.ReceiptPath = path
	comparison.ExpectedVersion = found.Receipt.Agent.LatestVersionAfter
	if comparison.ExpectedVersion == "" {
		comparison.ExpectedVersion = found.Receipt.Agent.CreatedVersion
	}
	if found.Receipt.Manifest.DesiredHash != snapshot.Hash {
		comparison.Reasons = append(comparison.Reasons, "the deployable workspace snapshot changed")
	}
	if comparison.ExpectedVersion == "" ||
		comparison.ExpectedVersion != runtime.Deployment.Version {
		comparison.Reasons = append(
			comparison.Reasons,
			"the remote latest version no longer matches the recorded deployment",
		)
	}
	if !activeHostedStatus(runtime.Deployment.Status) {
		comparison.Reasons = append(
			comparison.Reasons,
			"the remote latest version is not active or idle",
		)
	}
	if len(comparison.Reasons) == 0 {
		comparison.Changed = false
	}
	return comparison, nil
}

func latestHostedDeployReceipt(
	runtime *hostedRESTRuntime,
) (*receipt.OperationStore, string, error) {
	directory := filepath.Join(
		filepath.Dir(runtime.Workspace.AzureYAML),
		".foundry-agent-manager",
		"receipts",
	)
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", errs.Config("failed to list Hosted deployment receipts: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() > entries[j].Name()
	})
	manifestPath := filepath.Clean(runtime.Workspace.AzureYAML)
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		checked++
		if checked > 1000 {
			break
		}
		path := filepath.Join(directory, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, "", errs.Config("failed to inspect Hosted deployment receipt %q: %v", path, err)
		}
		if info.Size() > 1<<20 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", errs.Config("failed to read Hosted deployment receipt %q: %v", path, err)
		}
		var record receipt.ReceiptV2
		if json.Unmarshal(data, &record) != nil {
			continue
		}
		if record.SchemaVersion != receipt.SchemaVersionV2 ||
			record.Operation != "hosted-deploy" ||
			!strings.HasPrefix(record.Status, "succeeded") ||
			record.Cloud != runtime.Profile.Name ||
			filepath.Clean(record.Manifest.Path) != manifestPath ||
			record.Project.Endpoint != runtime.ProjectEndpoint ||
			record.Agent.Name != runtime.Agent.Name {
			continue
		}
		return &receipt.OperationStore{Path: path, Receipt: record}, path, nil
	}
	return nil, "", nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

func hostedRuntimeSummary(runtime *hostedRESTRuntime) string {
	return fmt.Sprintf(
		"name=%s version=%s status=%s state=%s",
		runtime.Agent.Name,
		runtime.Deployment.Version,
		runtime.Deployment.Status,
		runtime.Agent.State,
	)
}
