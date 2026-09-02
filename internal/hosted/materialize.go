package hosted

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundryid"

	"gopkg.in/yaml.v3"
)

// MaterializeRAIPolicy writes a deployment-only azure.yaml with the resolved
// policy ID. The returned function restores the exact original bytes.
func MaterializeRAIPolicy(workspace Workspace, policyID string) (func() error, error) {
	if workspace.Selected.RAIPolicy == nil ||
		!workspace.Selected.RAIPolicy.UnresolvedReference {
		return func() error { return nil }, nil
	}
	policy, err := foundryid.ParseRAIPolicyID(policyID)
	if err != nil {
		return nil, errs.Config("Hosted Agent RAI policy is invalid: %v", err)
	}
	if workspace.resolvedDocument == nil {
		return nil, errs.Config("Hosted workspace cannot render the resolved RAI policy")
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
	policies, ok := service["policies"].([]any)
	if !ok || len(policies) != 1 {
		return nil, errs.Manifest(
			"services.%s.policies must contain exactly one rai_policy entry",
			workspace.Selected.ServiceName,
		)
	}
	raiPolicy, ok := asMap(policies[0])
	if !ok ||
		!strings.EqualFold(getString(raiPolicy, "type"), "rai_policy") ||
		getString(raiPolicy, "raiPolicyName") != workspace.Selected.RAIPolicy.PolicyID {
		return nil, errs.Manifest(
			"services.%s.policies[0] no longer matches the validated RAI policy",
			workspace.Selected.ServiceName,
		)
	}
	raiPolicy["raiPolicyName"] = policy.String()

	rendered, err := yaml.Marshal(document)
	if err != nil {
		return nil, errs.Config("failed to render Hosted deployment configuration: %v", err)
	}
	original, err := os.ReadFile(workspace.AzureYAML)
	if err != nil {
		return nil, errs.Config("failed to read azure.yaml before Hosted deployment: %v", err)
	}
	info, err := os.Stat(workspace.AzureYAML)
	if err != nil {
		return nil, errs.Config("failed to inspect azure.yaml before Hosted deployment: %v", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errs.Security("azure.yaml must remain a regular file during Hosted deployment")
	}
	if err := writeFileAtomic(workspace.AzureYAML, rendered, info.Mode().Perm()); err != nil {
		return nil, errs.Config("failed to materialize the Hosted RAI policy for deployment: %v", err)
	}

	restored := false
	return func() error {
		if restored {
			return nil
		}
		if err := writeFileAtomic(workspace.AzureYAML, original, info.Mode().Perm()); err != nil {
			return fmt.Errorf("failed to restore azure.yaml after Hosted deployment: %w", err)
		}
		restored = true
		return nil
	}, nil
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
