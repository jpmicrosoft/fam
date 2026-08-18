package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func applyCommandExamples(root *cobra.Command) {
	root.Example = strings.Join([]string{
		"  foundry-agent-manager quickstart                          # create a manifest or workspace",
		"  foundry-agent-manager doctor -f agent.yaml --online       # check setup and Azure access",
		"  foundry-agent-manager prompt deploy -f agent.yaml --if-changed # deploy only when configuration changed",
	}, "\n")

	commands := root.Commands()
	if len(commandExamples) != len(commands) {
		panic(fmt.Sprintf(
			"command example catalog has %d entries for %d application commands",
			len(commandExamples),
			len(commands),
		))
	}
	for _, command := range commands {
		example, ok := commandExamples[command.Name()]
		if !ok {
			panic("missing command example for " + command.Name())
		}
		command.Example = formatCommandExamples(example)
	}
}

func formatCommandExamples(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for index, line := range lines {
		lines[index] = "  " + strings.TrimSpace(line)
	}
	return strings.Join(lines, "\n")
}

var commandExamples = map[string]string{
	"version":      `foundry-agent-manager version`,
	"tool-catalog": `foundry-agent-manager tool-catalog --output json`,
	"receipt-upload": `
foundry-agent-manager receipt-upload --file artifacts\deploy-receipt.json --receipt-log-endpoint https://my-dce.eastus-1.ingest.monitor.azure.com --receipt-log-dcr-id dcr-0123456789abcdef0123456789abcdef
foundry-agent-manager receipt-upload --file artifacts\deploy-receipt.json --receipt-log-endpoint https://my-dce.eastus-1.ingest.monitor.azure.com --receipt-log-dcr-id dcr-0123456789abcdef0123456789abcdef --output json`,
	"quickstart": `
foundry-agent-manager quickstart
foundry-agent-manager quickstart --type prompt --destination agent.yaml --name support-agent --model my-model --project-resource-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/my-account/projects/support-project --non-interactive
foundry-agent-manager quickstart --type hosted --destination hosted-agent --name support-agent --environment dev --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/my-account/projects/support --model support-model --location eastus2 --tenant-id 00000000-0000-0000-0000-000000000000 --bootstrap-environment --non-interactive
foundry-agent-manager quickstart --type hosted --source .\existing-agent --destination adopted-agent --name support-agent --non-interactive`,
	"doctor": `
foundry-agent-manager doctor
foundry-agent-manager doctor -f agent.yaml --online --fail-on-not-ready
foundry-agent-manager doctor --workspace C:\src\hosted-agent --environment prod --online --accept-preview --debug`,

	"autopilot-info": `foundry-agent-manager autopilot-info`,
	"autopilot-preflight": `
foundry-agent-manager autopilot-preflight --accept-preview --approve-sample-commit 0123456789abcdef0123456789abcdef01234567 --region eastus2 --allowed-region eastus2`,
	"autopilot-deploy": `
foundry-agent-manager autopilot-deploy --accept-preview --approve-sample-commit 0123456789abcdef0123456789abcdef01234567 --region eastus2 --allowed-region eastus2 --work-dir C:\src\hosted-autopilot --environment-name prod --receipt artifacts\autopilot-receipt.json`,

	"agent365-info":           `foundry-agent-manager agent365-info`,
	"agent365-blueprint-list": `foundry-agent-manager agent365-blueprint-list --limit 100`,
	"agent365-blueprint-show": `
foundry-agent-manager agent365-blueprint-show --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444
foundry-agent-manager agent365-blueprint-show --blueprint-object-id 08be1f79-37a1-49c0-b444-3075e74d1e8c --output json`,
	"agent365-blueprint-permissions": `
foundry-agent-manager agent365-blueprint-permissions --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444`,
	"agent365-blueprint-validate": `
foundry-agent-manager agent365-blueprint-validate --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --fail-on-invalid`,
	"agent365-blueprint-owners": `
foundry-agent-manager agent365-blueprint-owners --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --all`,
	"agent365-blueprint-sponsors": `
foundry-agent-manager agent365-blueprint-sponsors --blueprint-object-id 08be1f79-37a1-49c0-b444-3075e74d1e8c --all`,
	"agent365-blueprint-identities": `
foundry-agent-manager agent365-blueprint-identities --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --all`,
	"agent365-identity-list": `
foundry-agent-manager agent365-identity-list --limit 100
foundry-agent-manager agent365-identity-list --all --output json`,
	"agent365-identity-show": `
foundry-agent-manager agent365-identity-show --identity-object-id 11112222-bbbb-3333-cccc-4444dddd5555`,
	"agent365-blueprint-principal-list": `
foundry-agent-manager agent365-blueprint-principal-list --all`,
	"agent365-blueprint-principal-show": `
foundry-agent-manager agent365-blueprint-principal-show --principal-object-id 22223333-cccc-4444-dddd-5555eeee6666`,
	"agent365-binding-plan": `
foundry-agent-manager agent365-binding-plan --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 -f agent.yaml
foundry-agent-manager agent365-binding-plan --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"agent365-binding-status": `
foundry-agent-manager agent365-binding-status -f agent.yaml
foundry-agent-manager agent365-binding-status --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"agent365-observability-plan": `
foundry-agent-manager agent365-observability-plan --workspace C:\src\hosted-agent`,
	"agent365-observability-status": `
foundry-agent-manager agent365-observability-status --workspace C:\src\hosted-agent --environment prod --accept-preview --fail-on-not-ready
foundry-agent-manager agent365-observability-status --workspace C:\src\hosted-agent --accept-preview --identity-object-id 11112222-bbbb-3333-cccc-4444dddd5555`,
	"agent365-integration-status": `
foundry-agent-manager agent365-integration-status --account-id /subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account`,
	"agent365-integration-plan": `
foundry-agent-manager agent365-integration-plan --account-id /subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account --enabled=true`,
	"agent365-integration-set": `
foundry-agent-manager agent365-integration-set --account-id /subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account --enabled=true --yes --receipt artifacts\agent365-integration-receipt.json`,
	"agent365-publication-info": `foundry-agent-manager agent365-publication-info`,
	"agent365-publication-plan": `
foundry-agent-manager agent365-publication-plan -f agent.yaml
foundry-agent-manager agent365-publication-plan --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"agent365-publication-status": `
foundry-agent-manager agent365-publication-status -f agent.yaml --resolve-identity`,
	"agent365-publication-admin-handoff": `
foundry-agent-manager agent365-publication-admin-handoff --workspace C:\src\hosted-agent --environment prod --accept-preview --output json`,

	"hosted-info": `foundry-agent-manager hosted-info`,
	"hosted-adopt": `
foundry-agent-manager hosted-adopt --source .\existing-agent --destination adopted-agent --name support-agent --entry-point main.py
foundry-agent-manager hosted-adopt --source .\existing-agent --in-place --name support-agent --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project --model support-model --location eastus --bootstrap-environment`,
	"hosted-init": `
foundry-agent-manager hosted-init --destination hosted-agent --name support-agent --protocol responses`,
	"hosted-validate": `
foundry-agent-manager hosted-validate --workspace C:\src\hosted-agent`,
	"hosted-plan": `
foundry-agent-manager hosted-plan --workspace C:\src\hosted-agent --environment prod
foundry-agent-manager hosted-plan --workspace C:\src\hosted-agent --environment prod --provision --preview-provision`,
	"hosted-environment-create": `
foundry-agent-manager hosted-environment-create --workspace C:\src\hosted-agent --environment prod --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project --model-deployment support-model --location eastus
foundry-agent-manager hosted-environment-create --workspace C:\src\hosted-agent --environment prod --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project --model-deployment support-model --location eastus --tenant-id 00000000-0000-0000-0000-000000000000`,
	"hosted-preflight": `
foundry-agent-manager hosted-preflight --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-deploy": `
foundry-agent-manager hosted-deploy --workspace C:\src\hosted-agent --environment prod --accept-preview --if-changed
foundry-agent-manager hosted-deploy --workspace C:\src\hosted-agent --environment prod --accept-preview --provision --preview-provision --receipt artifacts\hosted-deploy-receipt.json`,
	"hosted-status": `
foundry-agent-manager hosted-status --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-show": `
foundry-agent-manager hosted-show --workspace C:\src\hosted-agent --environment prod --accept-preview
foundry-agent-manager hosted-show --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3`,
	"hosted-versions": `
foundry-agent-manager hosted-versions --workspace C:\src\hosted-agent --environment prod --accept-preview --include-drafts`,
	"hosted-diff": `
foundry-agent-manager hosted-diff --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-diagnose": `
foundry-agent-manager hosted-diagnose --workspace C:\src\hosted-agent --environment prod --accept-preview --debug`,
	"hosted-smoke": `
foundry-agent-manager hosted-smoke --workspace C:\src\hosted-agent --environment prod --accept-preview --protocol responses --prompt health-check
foundry-agent-manager hosted-smoke --workspace C:\src\hosted-agent --environment prod --accept-preview --protocol invocations --input-file requests\smoke.json`,
	"hosted-session-create": `
foundry-agent-manager hosted-session-create --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3 --receipt artifacts\session-create-receipt.json`,
	"hosted-session-list": `
foundry-agent-manager hosted-session-list --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-session-show": `
foundry-agent-manager hosted-session-show --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123`,
	"hosted-session-stop": `
foundry-agent-manager hosted-session-stop --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --receipt artifacts\session-stop-receipt.json`,
	"hosted-session-delete": `
# Preview the session deletion.
foundry-agent-manager hosted-session-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --dry-run
# Delete after reviewing the preview.
foundry-agent-manager hosted-session-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --yes --receipt artifacts\session-delete-receipt.json`,
	"hosted-session-file-upload": `
foundry-agent-manager hosted-session-file-upload --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --file data\input.csv --remote-path input.csv --receipt artifacts\file-upload-receipt.json`,
	"hosted-session-file-list": `
foundry-agent-manager hosted-session-file-list --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --remote-path .`,
	"hosted-session-file-download": `
foundry-agent-manager hosted-session-file-download --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --remote-path results\output.json --output-file downloads\output.json`,
	"hosted-session-file-delete": `
# Preview the sandbox file deletion.
foundry-agent-manager hosted-session-file-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --remote-path input.csv --dry-run
# Delete after reviewing the preview.
foundry-agent-manager hosted-session-file-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --remote-path input.csv --yes --receipt artifacts\file-delete-receipt.json`,
	"hosted-promote": `
foundry-agent-manager hosted-promote --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3 --receipt artifacts\hosted-promote-receipt.json
foundry-agent-manager hosted-promote --workspace C:\src\hosted-agent --environment prod --accept-preview --latest`,
	"hosted-rollback": `
foundry-agent-manager hosted-rollback --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 2 --receipt artifacts\hosted-rollback-receipt.json`,
	"hosted-prune": `
# Preview retained and deleted versions.
foundry-agent-manager hosted-prune --workspace C:\src\hosted-agent --environment prod --accept-preview --keep 3 --dry-run
# Delete after reviewing the preview.
foundry-agent-manager hosted-prune --workspace C:\src\hosted-agent --environment prod --accept-preview --keep 3 --yes --receipt artifacts\hosted-prune-receipt.json`,
	"hosted-delete-version": `
# Preview deletion of one immutable version.
foundry-agent-manager hosted-delete-version --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 2 --dry-run
# Delete after reviewing the preview.
foundry-agent-manager hosted-delete-version --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 2 --yes --receipt artifacts\hosted-delete-version-receipt.json`,
	"hosted-delete": `
# Preview deletion of the Hosted Agent and sessions.
foundry-agent-manager hosted-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --dry-run
# Delete after reviewing the preview.
foundry-agent-manager hosted-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --yes --receipt artifacts\hosted-delete-receipt.json`,
	"hosted-logs": `
foundry-agent-manager hosted-logs --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3 --session-id session-123 --max-lines 100`,
	"hosted-draft-deploy": `
foundry-agent-manager hosted-draft-deploy --workspace C:\src\hosted-agent --environment prod --accept-preview --description candidate --receipt artifacts\draft-receipt.json`,
	"hosted-disable": `
foundry-agent-manager hosted-disable --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-enable": `
foundry-agent-manager hosted-enable --workspace C:\src\hosted-agent --environment prod --accept-preview`,

	"init": `
foundry-agent-manager init -f agent.yaml --name support-agent --model my-model --project-resource-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/my-account/projects/support-project`,
	"validate":      `foundry-agent-manager validate -f agent.yaml`,
	"plan":          `foundry-agent-manager plan -f agent.yaml`,
	"compatibility": `foundry-agent-manager compatibility -f agent.yaml`,
	"project-create": `
foundry-agent-manager project-create -f agent.yaml --receipt artifacts\project-create-receipt.json`,
	"model-deployment-list": `
foundry-agent-manager model-deployment-list -f agent.yaml
foundry-agent-manager model-deployment-list -f agent.yaml --output json`,
	"model-deployment-show": `
foundry-agent-manager model-deployment-show -f agent.yaml
foundry-agent-manager model-deployment-show -f agent.yaml --deployment-name gpt-5-mini`,
	"model-deployment-plan": `
foundry-agent-manager model-deployment-plan -f agent.yaml
foundry-agent-manager model-deployment-plan -f agent.yaml --deployment-name gpt-5-mini --model-name gpt-5-mini --model-version 2025-08-07 --model-format OpenAI --sku-name GlobalStandard --capacity 10`,
	"model-deployment-create": `
foundry-agent-manager model-deployment-create -f agent.yaml
foundry-agent-manager model-deployment-create -f agent.yaml --deployment-name gpt-5-mini --model-name gpt-5-mini --model-version 2025-08-07 --model-format OpenAI --sku-name GlobalStandard --capacity 10 --output json`,
	"model-deployment-delete": `
foundry-agent-manager model-deployment-delete -f agent.yaml --deployment-name gpt-5-mini --dry-run
foundry-agent-manager model-deployment-delete -f agent.yaml --deployment-name gpt-5-mini --yes`,
	"preflight": `foundry-agent-manager preflight -f agent.yaml`,
	"deploy": `
foundry-agent-manager deploy -f agent.yaml --if-changed
foundry-agent-manager deploy -f agent.yaml --if-changed --output json --receipt artifacts\deploy-receipt.json`,
	"status": `foundry-agent-manager status -f agent.yaml`,
	"show": `
foundry-agent-manager show -f agent.yaml
foundry-agent-manager show -f agent.yaml --agent-version 3`,
	"endpoint-show": `foundry-agent-manager endpoint-show -f agent.yaml`,
	"endpoint-configure": `
foundry-agent-manager endpoint-configure -f agent.yaml --receipt artifacts\endpoint-configure-receipt.json`,
	"versions": `foundry-agent-manager versions -f agent.yaml`,
	"diff":     `foundry-agent-manager diff -f agent.yaml`,
	"smoke": `
foundry-agent-manager smoke -f agent.yaml --prompt health-check`,
	"disable": `foundry-agent-manager disable -f agent.yaml`,
	"enable":  `foundry-agent-manager enable -f agent.yaml`,
	"promote": `
foundry-agent-manager promote -f agent.yaml --agent-version 3 --receipt artifacts\promote-receipt.json
foundry-agent-manager promote -f agent.yaml --latest`,
	"rollback": `
# Preview routing to an earlier version.
foundry-agent-manager rollback -f agent.yaml --agent-version 2 --dry-run
# Apply after reviewing the preview.
foundry-agent-manager rollback -f agent.yaml --agent-version 2 --yes --receipt artifacts\rollback-receipt.json`,
	"prune": `
# Preview retained and deleted versions.
foundry-agent-manager prune -f agent.yaml --keep 3 --dry-run
# Delete after reviewing the preview.
foundry-agent-manager prune -f agent.yaml --keep 3 --yes`,
	"delete-version": `
# Preview deletion of one immutable version.
foundry-agent-manager delete-version -f agent.yaml --agent-version 2 --dry-run
# Delete after reviewing the preview.
foundry-agent-manager delete-version -f agent.yaml --agent-version 2 --yes`,
	"delete": `
# Preview deletion of the whole Prompt Agent.
foundry-agent-manager delete -f agent.yaml --dry-run
# Delete after reviewing the preview.
foundry-agent-manager delete -f agent.yaml --yes`,
	"decommission": `
# Preview deletion of the agent and APIM connection.
foundry-agent-manager decommission -f agent.yaml --dry-run
# Delete after reviewing the preview.
foundry-agent-manager decommission -f agent.yaml --yes`,

	"connection-list": `foundry-agent-manager connection-list -f agent.yaml`,
	"connection-show": `
foundry-agent-manager connection-show -f agent.yaml --connection search`,
	"connection-create": `
foundry-agent-manager connection-create -f agent.yaml --connection search --connection-type CognitiveSearch --target https://search.example.com --auth-type ApiKey --secret-env SEARCH_API_KEY --receipt artifacts\connection-create-receipt.json`,
	"connection-update": `
foundry-agent-manager connection-update -f agent.yaml --connection search --connection-type CognitiveSearch --target https://search.example.com --auth-type ApiKey --secret-env SEARCH_API_KEY --receipt artifacts\connection-update-receipt.json`,
	"connection-delete": `
foundry-agent-manager connection-delete -f agent.yaml --connection search --yes --receipt artifacts\connection-delete-receipt.json`,
	"api-center-list": `
foundry-agent-manager api-center-list -f agent.yaml --api-center-endpoint https://contoso.azure-apicenter.ms --search payments`,
	"api-center-show": `
foundry-agent-manager api-center-show -f agent.yaml --api-center-endpoint https://contoso.azure-apicenter.ms --server payments`,
	"logicapps-registration-plan": `
foundry-agent-manager logicapps-registration-plan -f agent.yaml --accept-preview --connector-name my-connector --mcp-server-name operations --mcp-server-description operations-connector --operation get-message`,
	"connector-list": `
foundry-agent-manager connector-list -f agent.yaml --accept-preview --search github`,
	"connector-show": `
foundry-agent-manager connector-show -f agent.yaml --accept-preview --connector-name github`,
	"connector-create": `
foundry-agent-manager connector-create -f agent.yaml --accept-preview --connection github --connector-name github --receipt artifacts\connector-create-receipt.json`,
	"connector-consent": `
foundry-agent-manager connector-consent -f agent.yaml --accept-preview --connection github --object-id 00000000-0000-0000-0000-000000000000 --tenant-id 00000000-0000-0000-0000-000000000000`,
	"connector-actions": `
foundry-agent-manager connector-actions -f agent.yaml --accept-preview --connector-name github --operation get-issue`,
	"connector-configure": `
foundry-agent-manager connector-configure -f agent.yaml --accept-preview --connection github --operation get-issue --operation create-issue --yes --receipt artifacts\connector-configure-receipt.json`,
	"connector-status": `
foundry-agent-manager connector-status -f agent.yaml --accept-preview --connection github`,
	"connector-wait": `
foundry-agent-manager connector-wait -f agent.yaml --accept-preview --connection github --connector-timeout 10m`,
	"connector-toolbox-deploy": `
foundry-agent-manager connector-toolbox-deploy -f agent.yaml --accept-preview --connection github --toolbox-name github-tools --if-changed --receipt artifacts\connector-toolbox-receipt.json`,
	"connector-delete": `
foundry-agent-manager connector-delete -f agent.yaml --accept-preview --connection github --yes --receipt artifacts\connector-delete-receipt.json`,

	"toolbox-validate": `foundry-agent-manager toolbox-validate -f agent.yaml --toolbox shared-tools`,
	"toolbox-plan":     `foundry-agent-manager toolbox-plan -f agent.yaml --toolbox shared-tools`,
	"toolbox-deploy": `
foundry-agent-manager toolbox-deploy -f agent.yaml --toolbox shared-tools --if-changed --receipt artifacts\toolbox-deploy-receipt.json`,
	"toolbox-status":   `foundry-agent-manager toolbox-status -f agent.yaml --toolbox shared-tools`,
	"toolbox-versions": `foundry-agent-manager toolbox-versions -f agent.yaml --toolbox shared-tools`,
	"toolbox-promote": `
# Preview the default-version change.
foundry-agent-manager toolbox-promote -f agent.yaml --toolbox shared-tools --toolbox-version 3 --dry-run
# Apply after reviewing the preview.
foundry-agent-manager toolbox-promote -f agent.yaml --toolbox shared-tools --toolbox-version 3 --yes --receipt artifacts\toolbox-promote-receipt.json`,
	"toolbox-delete-version": `
# Preview deletion of one non-default version.
foundry-agent-manager toolbox-delete-version -f agent.yaml --toolbox shared-tools --toolbox-version 2 --dry-run
# Delete after reviewing the preview.
foundry-agent-manager toolbox-delete-version -f agent.yaml --toolbox shared-tools --toolbox-version 2 --yes --receipt artifacts\toolbox-delete-version-receipt.json`,

	"skill-create": `
foundry-agent-manager skill-create -f agent.yaml --skill incident-response --path skills\incident-response.zip --accept-preview --default --receipt artifacts\skill-create-receipt.json`,
	"skill-list": `
foundry-agent-manager skill-list -f agent.yaml --accept-preview`,
	"skill-show": `
foundry-agent-manager skill-show -f agent.yaml --skill incident-response --accept-preview`,
	"skill-version-list": `
foundry-agent-manager skill-version-list -f agent.yaml --skill incident-response --accept-preview`,
	"skill-version-show": `
foundry-agent-manager skill-version-show -f agent.yaml --skill incident-response --version 3 --accept-preview`,
	"skill-set-default": `
foundry-agent-manager skill-set-default -f agent.yaml --skill incident-response --version 3 --accept-preview --receipt artifacts\skill-default-receipt.json`,
	"skill-delete": `
foundry-agent-manager skill-delete -f agent.yaml --skill incident-response --accept-preview --yes --receipt artifacts\skill-delete-receipt.json`,
	"skill-version-delete": `
foundry-agent-manager skill-version-delete -f agent.yaml --skill incident-response --version 2 --accept-preview --yes --receipt artifacts\skill-version-delete-receipt.json`,
	"skill-download": `
foundry-agent-manager skill-download -f agent.yaml --skill incident-response --version 3 --accept-preview --destination downloads\incident-response.zip`,

	"grounding-validate": `foundry-agent-manager grounding-validate -f agent.yaml --grounding knowledge`,
	"grounding-plan":     `foundry-agent-manager grounding-plan -f agent.yaml --grounding knowledge`,
	"grounding-sync": `
foundry-agent-manager grounding-sync -f agent.yaml --grounding knowledge --prune --yes --receipt artifacts\grounding-sync-receipt.json`,
	"grounding-status": `foundry-agent-manager grounding-status -f agent.yaml --grounding knowledge`,
	"grounding-delete-file": `
# Preview detaching one document.
foundry-agent-manager grounding-delete-file -f agent.yaml --grounding knowledge --file docs\policy.pdf --dry-run
# Detach after reviewing the preview.
foundry-agent-manager grounding-delete-file -f agent.yaml --grounding knowledge --file docs\policy.pdf --yes --receipt artifacts\grounding-file-delete-receipt.json`,
	"grounding-delete-store": `
# Preview deletion of the vector store.
foundry-agent-manager grounding-delete-store -f agent.yaml --grounding knowledge --dry-run
# Delete after reviewing the preview.
foundry-agent-manager grounding-delete-store -f agent.yaml --grounding knowledge --yes --receipt artifacts\grounding-store-delete-receipt.json`,

	"memory-store-validate": `foundry-agent-manager memory-store-validate -f agent.yaml --memory-store support-memory`,
	"memory-store-plan":     `foundry-agent-manager memory-store-plan -f agent.yaml --memory-store support-memory`,
	"memory-store-sync": `
foundry-agent-manager memory-store-sync -f agent.yaml --memory-store support-memory --accept-preview --receipt artifacts\memory-store-sync-receipt.json`,
	"memory-store-list": `
foundry-agent-manager memory-store-list -f agent.yaml --accept-preview`,
	"memory-store-show": `
foundry-agent-manager memory-store-show -f agent.yaml --memory-store support-memory --accept-preview`,
	"memory-store-delete": `
foundry-agent-manager memory-store-delete -f agent.yaml --memory-store support-memory --accept-preview --yes --receipt artifacts\memory-store-delete-receipt.json`,
	"memory-search": `
foundry-agent-manager memory-search -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --input password-reset`,
	"memory-update": `
foundry-agent-manager memory-update -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --input prefers-email --receipt artifacts\memory-update-receipt.json`,
	"memory-item-create": `
foundry-agent-manager memory-item-create -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --content prefers-email --kind user_profile --receipt artifacts\memory-item-create-receipt.json`,
	"memory-item-list": `
foundry-agent-manager memory-item-list -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --kind user_profile`,
	"memory-item-show": `
foundry-agent-manager memory-item-show -f agent.yaml --memory-store support-memory --accept-preview --memory-id memory-123`,
	"memory-item-update": `
foundry-agent-manager memory-item-update -f agent.yaml --memory-store support-memory --accept-preview --memory-id memory-123 --content prefers-chat --receipt artifacts\memory-item-update-receipt.json`,
	"memory-item-delete": `
foundry-agent-manager memory-item-delete -f agent.yaml --memory-store support-memory --accept-preview --memory-id memory-123 --yes --receipt artifacts\memory-item-delete-receipt.json`,
	"memory-scope-delete": `
foundry-agent-manager memory-scope-delete -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --yes --receipt artifacts\memory-scope-delete-receipt.json`,

	"publish-m365": `
foundry-agent-manager publish-m365 -f agent.yaml --publication publication.yaml --receipt artifacts\m365-publication-receipt.json`,
	"legacy-status": `
foundry-agent-manager legacy-status -f agent.yaml --application-name support-app --deployment-name support-deployment`,
	"legacy-deploy": `
foundry-agent-manager legacy-deploy -f agent.yaml --application-name support-app --deployment-name support-deployment --agent-version 3 --receipt artifacts\legacy-deploy-receipt.json
foundry-agent-manager legacy-deploy -f agent.yaml --application-name support-app --deployment-name support-deployment --agent-version 3 --route --yes`,
	"legacy-delete": `
# Preview deletion of compatibility resources.
foundry-agent-manager legacy-delete -f agent.yaml --application-name support-app --deployment-name support-deployment --dry-run
# Delete after reviewing the preview.
foundry-agent-manager legacy-delete -f agent.yaml --application-name support-app --deployment-name support-deployment --application --yes --receipt artifacts\legacy-delete-receipt.json`,
}
