package qa

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPromptWorkflowTemplateContract(t *testing.T) {
	template := repositoryFile(t, "docs", "ci-templates", "deploy-prompt.yml")
	requireText(t, template,
		"id-token: write",
		"azure/login@v2",
		"fam prompt validate",
		"fam prompt preflight",
		"fam prompt deploy",
		"--if-changed",
		"--output json",
		"--receipt",
		"actions/upload-artifact@v4",
		"FAM_INSTALL_TOKEN",
	)
	assertYAMLParses(t, template)
}

func TestHostedWorkflowTemplateContract(t *testing.T) {
	template := repositoryFile(t, "docs", "ci-templates", "deploy-hosted.yml")
	requireText(t, template,
		"id-token: write",
		"Azure/setup-azd@v2",
		"version: 1.32.0",
		"azd auth login --federated-credential-provider github",
		"azd extension install azure.ai.agents --version 1.0.0-beta.13",
		"azd env set FOUNDRY_PROJECT_ENDPOINT",
		"azd env set AZURE_AI_PROJECT_ENDPOINT",
		"azd env set AZURE_AI_MODEL_DEPLOYMENT_NAME",
		"fam hosted validate",
		"fam hosted plan",
		"fam hosted preflight",
		"args=(",
		"\n            hosted",
		"\n            deploy",
		`fam "${args[@]}"`,
		"--accept-preview",
		"--if-changed",
		"--provision --preview-provision",
		"actions/upload-artifact@v4",
	)
	for _, forbidden := range []string{
		"fam prompt validate \\",
		"fam prompt plan \\",
		"fam prompt preflight \\",
		"fam prompt deploy \\",
	} {
		if strings.Contains(template, forbidden) {
			t.Fatalf("Hosted workflow contains Prompt command %q", forbidden)
		}
	}
	assertYAMLParses(t, template)
}

func TestVSCodeSchemaAssociationsAreValidJSON(t *testing.T) {
	settings := repositoryFile(t, ".vscode", "settings.json")
	var document map[string]any
	if err := json.Unmarshal([]byte(settings), &document); err != nil {
		t.Fatalf("invalid .vscode/settings.json: %v", err)
	}
	schemas, ok := document["yaml.schemas"].(map[string]any)
	if !ok {
		t.Fatalf("yaml.schemas is missing: %#v", document)
	}
	for _, schema := range []string{
		"./schema/manifest.schema.json",
		"./schema/publication.schema.json",
	} {
		if _, exists := schemas[schema]; !exists {
			t.Errorf("missing schema association %q", schema)
		}
	}
	jsonSchemas, ok := document["json.schemas"].([]any)
	if !ok || len(jsonSchemas) < 2 {
		t.Fatalf("json.schemas must map both manifest types: %#v", document["json.schemas"])
	}
	for _, schema := range []string{
		"./schema/manifest.schema.json",
		"./schema/publication.schema.json",
	} {
		if !strings.Contains(settings, `"url": "`+schema+`"`) {
			t.Errorf("missing JSON schema association %q", schema)
		}
	}

	extensions := repositoryFile(t, ".vscode", "extensions.json")
	if !strings.Contains(extensions, "redhat.vscode-yaml") {
		t.Fatal("VS Code recommendations must include redhat.vscode-yaml")
	}
}

func assertYAMLParses(t *testing.T, content string) {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(content), &document); err != nil {
		t.Fatalf("invalid YAML template: %v", err)
	}
}
