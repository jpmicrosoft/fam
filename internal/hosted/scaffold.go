package hosted

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundryid"
	"foundry-agent-manager/internal/netcheck"

	"gopkg.in/yaml.v3"
)

type ScaffoldOptions struct {
	Destination                string
	AgentName                  string
	Protocol                   string
	GuardrailPolicyID          string
	NoGuardrail                bool
	BingGroundingConnection    string
	BingCustomSearchConnection string
	BingCustomSearchInstance   string
	ToolboxName                string
	Metadata                   map[string]string
}

type ScaffoldResult struct {
	Root                       string            `json:"root" yaml:"root"`
	AgentName                  string            `json:"agentName" yaml:"agentName"`
	Protocol                   string            `json:"protocol" yaml:"protocol"`
	GuardrailPolicyID          string            `json:"guardrailPolicyId,omitempty" yaml:"guardrailPolicyId,omitempty"`
	NoGuardrail                bool              `json:"noGuardrail,omitempty" yaml:"noGuardrail,omitempty"`
	BingGroundingConnection    string            `json:"bingGroundingConnection,omitempty" yaml:"bingGroundingConnection,omitempty"`
	BingCustomSearchConnection string            `json:"bingCustomSearchConnection,omitempty" yaml:"bingCustomSearchConnection,omitempty"`
	BingCustomSearchInstance   string            `json:"bingCustomSearchInstance,omitempty" yaml:"bingCustomSearchInstance,omitempty"`
	ToolboxName                string            `json:"toolboxName,omitempty" yaml:"toolboxName,omitempty"`
	Metadata                   map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Files                      []string          `json:"files" yaml:"files"`
}

type hostedAzureYAMLOptions struct {
	AgentName                  string
	Source                     string
	Protocol                   string
	Runtime                    string
	EntryPoint                 string
	DependencyResolution       string
	GuardrailPolicyID          string
	NoGuardrail                bool
	BingGroundingConnection    bool
	BingCustomSearchConnection bool
	Toolbox                    bool
	Metadata                   map[string]string
}

func Scaffold(options ScaffoldOptions) (ScaffoldResult, error) {
	if !agentNamePattern.MatchString(options.AgentName) {
		return ScaffoldResult{}, errs.Config(
			"--name %q is invalid; use a 1-63 character Hosted Agent name containing letters, digits, or internal hyphens",
			options.AgentName,
		)
	}
	switch options.Protocol {
	case "responses", "invocations":
	default:
		return ScaffoldResult{}, errs.Config("--protocol must be responses or invocations")
	}
	if options.NoGuardrail && strings.TrimSpace(options.GuardrailPolicyID) != "" {
		return ScaffoldResult{}, errs.Config(
			"--guardrail-policy-id and --no-guardrail cannot be used together",
		)
	}
	if strings.TrimSpace(options.GuardrailPolicyID) != "" {
		policy, err := foundryid.ParseRAIPolicyID(options.GuardrailPolicyID)
		if err != nil {
			return ScaffoldResult{}, errs.Config("--guardrail-policy-id is invalid: %v", err)
		}
		options.GuardrailPolicyID = policy.String()
	}
	if options.BingGroundingConnection != "" &&
		!validBingGroundingConnectionName(options.BingGroundingConnection) {
		return ScaffoldResult{}, errs.Config(
			"--bing-grounding-connection must be a non-empty project connection name without surrounding whitespace or line breaks",
		)
	}
	if err := custommetadata.Validate(options.Metadata); err != nil {
		return ScaffoldResult{}, err
	}
	if (options.BingCustomSearchConnection == "") != (options.BingCustomSearchInstance == "") {
		return ScaffoldResult{}, errs.Config(
			"--bing-custom-search-connection and --bing-custom-search-instance must be provided together",
		)
	}
	if options.BingCustomSearchConnection != "" &&
		!validBingConnectionValue(options.BingCustomSearchConnection) {
		return ScaffoldResult{}, errs.Config(
			"--bing-custom-search-connection must be a non-empty project connection name without surrounding whitespace or line breaks",
		)
	}
	if options.BingCustomSearchInstance != "" &&
		!validBingConnectionValue(options.BingCustomSearchInstance) {
		return ScaffoldResult{}, errs.Config(
			"--bing-custom-search-instance must be a non-empty Bing Custom Search instance name without surrounding whitespace or line breaks",
		)
	}
	if options.ToolboxName != "" && !toolboxNamePattern.MatchString(options.ToolboxName) {
		return ScaffoldResult{}, errs.Config(
			"--toolbox-name %q is not a valid Foundry Toolbox name",
			options.ToolboxName,
		)
	}
	if options.ToolboxName != "" && options.Protocol != "responses" {
		return ScaffoldResult{}, errs.Config(
			"--toolbox-name requires --protocol responses so approval-gated calls can use the Responses continuation contract",
		)
	}
	if err := netcheck.ValidateRelativeFileReference(options.Destination, "--destination"); err != nil {
		return ScaffoldResult{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ScaffoldResult{}, errs.Config("failed to resolve the current directory: %v", err)
	}
	destination, err := netcheck.RequireContainedFile(cwd, options.Destination, "--destination")
	if err != nil {
		return ScaffoldResult{}, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return ScaffoldResult{}, errs.Config(
			"--destination %q already exists; Hosted scaffolding requires a new directory",
			options.Destination,
		)
	} else if !os.IsNotExist(err) {
		return ScaffoldResult{}, errs.Config("failed to inspect --destination %q: %v", options.Destination, err)
	}
	parent := filepath.Dir(destination)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return ScaffoldResult{}, errs.Config("the parent directory for --destination must already exist: %v", err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return ScaffoldResult{}, errs.Security("the parent directory for --destination must be a real directory")
	}
	cwdReal, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return ScaffoldResult{}, errs.Security("failed to resolve the current directory safely: %v", err)
	}
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return ScaffoldResult{}, errs.Security("failed to resolve the destination parent safely: %v", err)
	}
	parentRelative, err := filepath.Rel(cwdReal, parentReal)
	if err != nil ||
		parentRelative == ".." ||
		strings.HasPrefix(parentRelative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(parentRelative) {
		return ScaffoldResult{}, errs.Security("the destination parent escapes the current directory")
	}
	temp, err := os.MkdirTemp(parent, ".foundry-agent-manager-init-*")
	if err != nil {
		return ScaffoldResult{}, errs.Config("failed to create a temporary scaffold directory: %v", err)
	}
	defer os.RemoveAll(temp)

	sourceRelative := filepath.Join("src", options.AgentName)
	requirements := []string{
		"agent-framework-core==1.13.0",
		"agent-framework-foundry==1.10.4",
		"agent-framework-foundry-hosting==1.0.0b260730",
		"azure-identity",
		"python-dotenv",
	}
	envExample := []string{
		"AZURE_AI_MODEL_DEPLOYMENT_NAME=<model-deployment-name>",
	}
	if options.BingGroundingConnection != "" || options.BingCustomSearchConnection != "" {
		requirements = []string{
			"agent-framework-core==1.13.0",
			"agent-framework-foundry==1.10.4",
			"agent-framework-foundry-hosting==1.0.0b260730",
			"aiohttp",
			"azure-ai-projects",
			"azure-identity",
			"python-dotenv",
		}
	}
	if options.BingGroundingConnection != "" {
		envExample = append(
			envExample,
			"BING_GROUNDING_CONNECTION_NAME="+options.BingGroundingConnection,
		)
	}
	if options.BingCustomSearchConnection != "" {
		envExample = append(
			envExample,
			"BING_CUSTOM_SEARCH_CONNECTION_NAME="+options.BingCustomSearchConnection,
			"BING_CUSTOM_SEARCH_INSTANCE_NAME="+options.BingCustomSearchInstance,
		)
	}
	if options.ToolboxName != "" {
		envExample = append(
			envExample,
			"TOOLBOX_NAME="+options.ToolboxName,
			"TOOLBOX_APPROVAL_MODE=always_require",
		)
	}
	requirements = append(requirements, "")
	envExample = append(envExample, "")
	azureYAML, err := scaffoldAzureYAML(
		options.AgentName,
		options.Protocol,
		options.GuardrailPolicyID,
		options.NoGuardrail,
		options.BingGroundingConnection != "",
		options.BingCustomSearchConnection != "",
		options.ToolboxName != "",
		options.Metadata,
	)
	if err != nil {
		return ScaffoldResult{}, err
	}
	files := map[string]string{
		"azure.yaml": azureYAML,
		filepath.Join(sourceRelative, "main.py"): scaffoldPython(
			options.Protocol,
			options.BingGroundingConnection != "",
			options.BingCustomSearchConnection != "",
			options.ToolboxName != "",
		),
		filepath.Join(sourceRelative, "requirements.txt"): strings.Join(requirements, "\n"),
		filepath.Join(sourceRelative, ".agentignore"): strings.Join([]string{
			".venv/",
			"__pycache__/",
			"*.pyc",
			".env",
			"eval.yaml",
			".agent_configs/",
			".foundry/",
			"",
		}, "\n"),
		filepath.Join(sourceRelative, ".env.example"): strings.Join(envExample, "\n"),
	}
	ordered := []string{
		"azure.yaml",
		filepath.Join(sourceRelative, "main.py"),
		filepath.Join(sourceRelative, "requirements.txt"),
		filepath.Join(sourceRelative, ".agentignore"),
		filepath.Join(sourceRelative, ".env.example"),
	}
	for _, relative := range ordered {
		path := filepath.Join(temp, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return ScaffoldResult{}, errs.Config("failed to create scaffold directory for %q: %v", relative, err)
		}
		if err := os.WriteFile(path, []byte(files[relative]), 0o600); err != nil {
			return ScaffoldResult{}, errs.Config("failed to write scaffold file %q: %v", relative, err)
		}
	}
	if _, err := LoadWorkspace(temp, options.AgentName); err != nil {
		return ScaffoldResult{}, errs.Config("generated Hosted Agent scaffold failed validation: %v", err)
	}
	if err := os.Rename(temp, destination); err != nil {
		return ScaffoldResult{}, errs.Config("failed to finalize Hosted Agent scaffold: %v", err)
	}
	for i := range ordered {
		ordered[i] = filepath.ToSlash(ordered[i])
	}
	return ScaffoldResult{
		Root:                       destination,
		AgentName:                  options.AgentName,
		Protocol:                   options.Protocol,
		GuardrailPolicyID:          options.GuardrailPolicyID,
		NoGuardrail:                options.NoGuardrail,
		BingGroundingConnection:    options.BingGroundingConnection,
		BingCustomSearchConnection: options.BingCustomSearchConnection,
		BingCustomSearchInstance:   options.BingCustomSearchInstance,
		ToolboxName:                options.ToolboxName,
		Metadata:                   custommetadata.Clone(options.Metadata),
		Files:                      ordered,
	}, nil
}

func scaffoldAzureYAML(
	name,
	protocol,
	guardrailPolicyID string,
	noGuardrail bool,
	bingGrounding,
	bingCustomSearch,
	toolbox bool,
	metadata map[string]string,
) (string, error) {
	return renderHostedAzureYAML(hostedAzureYAMLOptions{
		AgentName:                  name,
		Source:                     filepath.ToSlash(filepath.Join("src", name)),
		Protocol:                   protocol,
		Runtime:                    "python_3_13",
		EntryPoint:                 "main.py",
		DependencyResolution:       "remote_build",
		GuardrailPolicyID:          guardrailPolicyID,
		NoGuardrail:                noGuardrail,
		BingGroundingConnection:    bingGrounding,
		BingCustomSearchConnection: bingCustomSearch,
		Toolbox:                    toolbox,
		Metadata:                   metadata,
	})
}

func renderHostedAzureYAML(options hostedAzureYAMLOptions) (string, error) {
	toolEnvironment := ""
	if options.BingGroundingConnection {
		toolEnvironment += "      BING_GROUNDING_CONNECTION_NAME: ${BING_GROUNDING_CONNECTION_NAME}\n"
	}
	if options.BingCustomSearchConnection {
		toolEnvironment += "      BING_CUSTOM_SEARCH_CONNECTION_NAME: ${BING_CUSTOM_SEARCH_CONNECTION_NAME}\n"
		toolEnvironment += "      BING_CUSTOM_SEARCH_INSTANCE_NAME: ${BING_CUSTOM_SEARCH_INSTANCE_NAME}\n"
	}
	if options.Toolbox {
		toolEnvironment += "      TOOLBOX_NAME: ${TOOLBOX_NAME}\n"
		toolEnvironment += "      TOOLBOX_APPROVAL_MODE: ${TOOLBOX_APPROVAL_MODE}\n"
	}
	metadataBlock := ""
	if len(options.Metadata) > 0 {
		encoded, err := yaml.Marshal(map[string]interface{}{"metadata": options.Metadata})
		if err != nil {
			return "", errs.Config("failed to encode Hosted Agent metadata: %v", err)
		}
		for _, line := range strings.Split(strings.TrimRight(string(encoded), "\n"), "\n") {
			metadataBlock += "    " + line + "\n"
		}
	}
	policiesBlock := ""
	if !options.NoGuardrail {
		policyID := strings.TrimSpace(options.GuardrailPolicyID)
		if policyID == "" {
			policyID = "${RAI_POLICY_ID}"
		}
		policiesBlock = fmt.Sprintf(
			"    policies:\n      - type: rai_policy\n        raiPolicyName: %s\n",
			policyID,
		)
	}
	return fmt.Sprintf(`name: %s-workspace

services:
  ai-project:
    host: azure.ai.project

  %s:
    host: azure.ai.agent
    kind: hosted
    name: %s
    project: %s
    uses:
      - ai-project
    codeConfiguration:
      runtime: %s
      entryPoint: %s
      dependencyResolution: %s
%s
%s
    protocols:
      - protocol: %s
        version: %s
    env:
      AZURE_AI_MODEL_DEPLOYMENT_NAME: ${AZURE_AI_MODEL_DEPLOYMENT_NAME}
%s
    container:
      resources:
        cpu: "1"
        memory: 2Gi
`,
		options.AgentName,
		options.AgentName,
		options.AgentName,
		options.Source,
		options.Runtime,
		options.EntryPoint,
		options.DependencyResolution,
		metadataBlock,
		policiesBlock,
		options.Protocol,
		DefaultProtocolVer,
		toolEnvironment,
	), nil
}

func scaffoldPython(protocol string, bingGrounding, bingCustomSearch, toolbox bool) string {
	server := "ResponsesHostServer"
	if protocol == "invocations" {
		server = "InvocationsHostServer"
	}
	hostingImports := server
	if toolbox {
		hostingImports += ", FoundryToolbox"
	}
	projectImport := ""
	toolSetup := ""
	agentTools := ""
	if bingGrounding || bingCustomSearch {
		projectImport = "from azure.ai.projects import AIProjectClient\n"
		toolSetup = `    project = AIProjectClient(
        endpoint=project_endpoint,
        credential=credential,
    )
`
	}
	if toolbox || bingGrounding || bingCustomSearch {
		toolSetup += "    tools = []\n"
		agentTools = "        tools=tools,\n"
	}
	if toolbox {
		toolSetup += `    toolbox = FoundryToolbox(credential)
    toolbox_approval_mode = os.getenv("TOOLBOX_APPROVAL_MODE", "always_require")
    if toolbox_approval_mode not in {"always_require", "never_require"}:
        raise ValueError(
            "TOOLBOX_APPROVAL_MODE must be always_require or never_require"
        )
    toolbox.approval_mode = toolbox_approval_mode
    tools.append(toolbox)
`
	}
	if bingGrounding {
		toolSetup += `    connection = project.connections.get(
        os.environ["BING_GROUNDING_CONNECTION_NAME"]
    )
    tools.append(
        FoundryChatClient.get_bing_grounding_tool(
            connection_id=connection.id,
        )
    )
`
	}
	if bingCustomSearch {
		toolSetup += `    custom_search_connection = project.connections.get(
        os.environ["BING_CUSTOM_SEARCH_CONNECTION_NAME"]
    )
    tools.append(
        FoundryChatClient.get_bing_custom_search_tool(
            connection_id=custom_search_connection.id,
            instance_name=os.environ["BING_CUSTOM_SEARCH_INSTANCE_NAME"],
        )
    )
`
	}
	return fmt.Sprintf(`import os

from agent_framework import Agent
from agent_framework.foundry import FoundryChatClient
from agent_framework_foundry_hosting import %s
%sfrom azure.identity import DefaultAzureCredential
from dotenv import load_dotenv

load_dotenv()


def main() -> None:
    project_endpoint = os.environ["FOUNDRY_PROJECT_ENDPOINT"]
    credential = DefaultAzureCredential()
%s
    client = FoundryChatClient(
        project_endpoint=project_endpoint,
        model=os.environ["AZURE_AI_MODEL_DEPLOYMENT_NAME"],
        credential=credential,
    )
    agent = Agent(
        client=client,
        instructions="You are a helpful assistant. Keep your answers concise.",
%s        default_options={"store": False},
    )
    %s(agent).run()


if __name__ == "__main__":
    main()
`, hostingImports, projectImport, toolSetup, agentTools, server)
}
