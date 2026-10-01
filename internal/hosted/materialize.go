package hosted

import (
	"os"
	"path/filepath"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundryid"

	"gopkg.in/yaml.v3"
)

// MaterializeRAIPolicy also adapts code entry points to the pinned azd contract.
func MaterializeRAIPolicy(workspace Workspace, policyID string) (func() error, error) {
	return MaterializeDeployment(workspace, policyID)
}

// MaterializeDeployment writes azd's filename-only entry point and the resolved
// policy ID. The returned function restores the exact original azure.yaml bytes
// or reports a conflict with durable recovery retained. See projectConfiguration
// for the shared-file projection limits.
func MaterializeDeployment(workspace Workspace, policyID string) (func() error, error) {
	return materializeConfiguration(workspace, policyID, true)
}

// MaterializeEntryPoint adapts code commands for read-only azd diagnostics.
func MaterializeEntryPoint(workspace Workspace) (func() error, error) {
	return materializeConfiguration(workspace, "", false)
}

func materializeConfiguration(workspace Workspace, policyID string, withPolicy bool) (func() error, error) {
	resolvePolicy := withPolicy && workspace.Selected.RAIPolicy != nil && workspace.Selected.RAIPolicy.UnresolvedReference
	if workspace.Selected.Code == nil && !resolvePolicy {
		return func() error { return nil }, nil
	}
	if workspace.resolvedDocument == nil {
		return nil, errs.Config("Hosted workspace cannot render the deployment configuration")
	}
	document := deepCloneMap(workspace.resolvedDocument)
	services, ok := asMap(document["services"])
	if !ok {
		return nil, errs.Manifest("azure.yaml must define a services mapping")
	}
	service, ok := asMap(services[workspace.Selected.ServiceName])
	if !ok {
		return nil, errs.Manifest(
			"services.%s must be a mapping",
			workspace.Selected.ServiceName,
		)
	}
	if code := workspace.Selected.Code; code != nil {
		codeDocument, ok := asMap(service["codeConfiguration"])
		if !ok {
			return nil, errs.Manifest("services.%s.codeConfiguration must be a mapping", workspace.Selected.ServiceName)
		}
		file, err := azdEntryPointFile(*code)
		if err != nil {
			return nil, err
		}
		codeDocument["entryPoint"] = file
	}
	if resolvePolicy {
		if err := materializePolicy(service, workspace.Selected, policyID); err != nil {
			return nil, err
		}
	}

	rendered, err := yaml.Marshal(document)
	if err != nil {
		return nil, errs.Config("failed to render Hosted deployment configuration: %v", err)
	}
	return projectConfiguration(workspace, rendered, defaultProjectionIO())
}

func materializePolicy(document map[string]any, service Service, policyID string) error {
	policy, err := foundryid.ParseRAIPolicyID(policyID)
	if err != nil {
		return errs.Config("Hosted Agent RAI policy is invalid: %v", err)
	}
	policies, ok := document["policies"].([]any)
	if !ok || len(policies) != 1 {
		return errs.Manifest("services.%s.policies must contain exactly one rai_policy entry", service.ServiceName)
	}
	raiPolicy, ok := asMap(policies[0])
	if !ok ||
		!strings.EqualFold(getString(raiPolicy, "type"), "rai_policy") ||
		getString(raiPolicy, "raiPolicyName") != service.RAIPolicy.PolicyID {
		return errs.Manifest("services.%s.policies[0] no longer matches the validated RAI policy", service.ServiceName)
	}
	raiPolicy["raiPolicyName"] = policy.String()
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".foundry-agent-manager-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceAtomicFile(tempPath, path); err != nil {
		return err
	}
	return syncAtomicDirectory(directory)
}
