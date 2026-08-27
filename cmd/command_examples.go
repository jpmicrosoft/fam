package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func applyCommandExamples(root *cobra.Command) {
	root.Example = strings.Join([]string{
		"  fam quickstart                          # create a manifest or workspace",
		"  fam doctor -f agent.yaml --online       # check setup and Azure access",
		"  fam prompt deploy -f agent.yaml --if-changed # deploy only when configuration changed",
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
	"version":      `fam version`,
	"tool-catalog": `fam tool-catalog --output json`,
	"receipt-upload": `
fam receipt-upload --file artifacts\deploy-receipt.json --receipt-log-endpoint https://my-dce.eastus-1.ingest.monitor.azure.com --receipt-log-dcr-id dcr-0123456789abcdef0123456789abcdef
fam receipt-upload --file artifacts\deploy-receipt.json --receipt-log-endpoint https://my-dce.eastus-1.ingest.monitor.azure.com --receipt-log-dcr-id dcr-0123456789abcdef0123456789abcdef --output json`,
	"quickstart": `
fam quickstart
fam quickstart --type prompt --destination agent.yaml --name support-agent --model my-model --project-resource-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/my-account/projects/support-project --non-interactive
fam quickstart --type hosted --destination hosted-agent --name support-agent --environment dev --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/my-account/projects/support --model support-model --location eastus2 --tenant-id 00000000-0000-0000-0000-000000000000 --bootstrap-environment --non-interactive
fam quickstart --type hosted --source .\existing-agent --destination adopted-agent --name support-agent --non-interactive`,
	"doctor": `
fam doctor
fam doctor -f agent.yaml --online --fail-on-not-ready
fam doctor --workspace C:\src\hosted-agent --environment prod --online --accept-preview --debug`,

	"autopilot-info": `fam autopilot-info`,
	"autopilot-preflight": `
fam autopilot-preflight --accept-preview --approve-sample-commit 0123456789abcdef0123456789abcdef01234567 --region eastus2 --allowed-region eastus2`,
	"autopilot-deploy": `
fam autopilot-deploy --accept-preview --approve-sample-commit 0123456789abcdef0123456789abcdef01234567 --region eastus2 --allowed-region eastus2 --work-dir C:\src\hosted-autopilot --environment-name prod --receipt artifacts\autopilot-receipt.json`,

	"agent365-info":           `fam agent365-info`,
	"agent365-blueprint-list": `fam agent365-blueprint-list --limit 100`,
	"agent365-blueprint-show": `
fam agent365-blueprint-show --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444
fam agent365-blueprint-show --blueprint-object-id 08be1f79-37a1-49c0-b444-3075e74d1e8c --output json`,
	"agent365-blueprint-permissions": `
fam agent365-blueprint-permissions --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444`,
	"agent365-blueprint-validate": `
fam agent365-blueprint-validate --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --fail-on-invalid`,
	"agent365-blueprint-owners": `
fam agent365-blueprint-owners --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --all`,
	"agent365-blueprint-sponsors": `
fam agent365-blueprint-sponsors --blueprint-object-id 08be1f79-37a1-49c0-b444-3075e74d1e8c --all`,
	"agent365-blueprint-identities": `
fam agent365-blueprint-identities --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --all`,
	"agent365-identity-list": `
fam agent365-identity-list --limit 100
fam agent365-identity-list --all --output json`,
	"agent365-identity-show": `
fam agent365-identity-show --identity-object-id 11112222-bbbb-3333-cccc-4444dddd5555`,
	"agent365-blueprint-principal-list": `
fam agent365-blueprint-principal-list --all`,
	"agent365-blueprint-principal-show": `
fam agent365-blueprint-principal-show --principal-object-id 22223333-cccc-4444-dddd-5555eeee6666`,
	"agent365-binding-plan": `
fam agent365-binding-plan --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 -f agent.yaml
fam agent365-binding-plan --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"agent365-binding-status": `
fam agent365-binding-status -f agent.yaml
fam agent365-binding-status --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"agent365-observability-plan": `
fam agent365-observability-plan --workspace C:\src\hosted-agent`,
	"agent365-observability-status": `
fam agent365-observability-status --workspace C:\src\hosted-agent --environment prod --accept-preview --fail-on-not-ready
fam agent365-observability-status --workspace C:\src\hosted-agent --accept-preview --identity-object-id 11112222-bbbb-3333-cccc-4444dddd5555`,
	"agent365-integration-status": `
fam agent365-integration-status --account-id /subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account`,
	"agent365-integration-plan": `
fam agent365-integration-plan --account-id /subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account --enabled=true`,
	"agent365-integration-set": `
fam agent365-integration-set --account-id /subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account --enabled=true --yes --receipt artifacts\agent365-integration-receipt.json`,
	"agent365-publication-info": `fam agent365-publication-info`,
	"agent365-publication-plan": `
fam agent365-publication-plan -f agent.yaml
fam agent365-publication-plan --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"agent365-publication-status": `
fam agent365-publication-status -f agent.yaml --resolve-identity`,
	"agent365-publication-admin-handoff": `
fam agent365-publication-admin-handoff --workspace C:\src\hosted-agent --environment prod --accept-preview --output json`,

	"hosted-info": `fam hosted-info`,
	"hosted-adopt": `
fam hosted-adopt --source .\existing-agent --destination adopted-agent --name support-agent --entry-point main.py
fam hosted-adopt --source .\existing-agent --in-place --name support-agent --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project --model support-model --location eastus --bootstrap-environment`,
	"hosted-init": `
fam hosted-init --destination hosted-agent --name support-agent --protocol responses`,
	"hosted-validate": `
fam hosted-validate --workspace C:\src\hosted-agent`,
	"hosted-plan": `
fam hosted-plan --workspace C:\src\hosted-agent --environment prod
fam hosted-plan --workspace C:\src\hosted-agent --environment prod --provision --preview-provision`,
	"hosted-environment-create": `
fam hosted-environment-create --workspace C:\src\hosted-agent --environment prod --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project --model-deployment support-model --location eastus
fam hosted-environment-create --workspace C:\src\hosted-agent --environment prod --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project --model-deployment support-model --location eastus --tenant-id 00000000-0000-0000-0000-000000000000`,
	"hosted-preflight": `
fam hosted-preflight --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-deploy": `
fam hosted-deploy --workspace C:\src\hosted-agent --environment prod --accept-preview --if-changed
fam hosted-deploy --workspace C:\src\hosted-agent --environment prod --accept-preview --provision --preview-provision --receipt artifacts\hosted-deploy-receipt.json`,
	"hosted-status": `
fam hosted-status --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-show": `
fam hosted-show --workspace C:\src\hosted-agent --environment prod --accept-preview
fam hosted-show --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3`,
	"hosted-versions": `
fam hosted-versions --workspace C:\src\hosted-agent --environment prod --accept-preview --include-drafts`,
	"hosted-diff": `
fam hosted-diff --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-diagnose": `
fam hosted-diagnose --workspace C:\src\hosted-agent --environment prod --accept-preview --debug`,
	"hosted-smoke": `
fam hosted-smoke --workspace C:\src\hosted-agent --environment prod --accept-preview --protocol responses --prompt health-check
fam hosted-smoke --workspace C:\src\hosted-agent --environment prod --accept-preview --protocol invocations --input-file requests\smoke.json`,
	"hosted-session-create": `
fam hosted-session-create --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3 --receipt artifacts\session-create-receipt.json`,
	"hosted-session-list": `
fam hosted-session-list --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-session-show": `
fam hosted-session-show --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123`,
	"hosted-session-stop": `
fam hosted-session-stop --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --receipt artifacts\session-stop-receipt.json`,
	"hosted-session-delete": `
# Preview the session deletion.
fam hosted-session-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --dry-run
# Delete after reviewing the preview.
fam hosted-session-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --yes --receipt artifacts\session-delete-receipt.json`,
	"hosted-session-file-upload": `
fam hosted-session-file-upload --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --file data\input.csv --remote-path input.csv --receipt artifacts\file-upload-receipt.json`,
	"hosted-session-file-list": `
fam hosted-session-file-list --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --remote-path .`,
	"hosted-session-file-download": `
fam hosted-session-file-download --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --remote-path results\output.json --output-file downloads\output.json`,
	"hosted-session-file-delete": `
# Preview the sandbox file deletion.
fam hosted-session-file-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --remote-path input.csv --dry-run
# Delete after reviewing the preview.
fam hosted-session-file-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id session-123 --remote-path input.csv --yes --receipt artifacts\file-delete-receipt.json`,
	"hosted-promote": `
fam hosted-promote --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3 --receipt artifacts\hosted-promote-receipt.json
fam hosted-promote --workspace C:\src\hosted-agent --environment prod --accept-preview --latest`,
	"hosted-rollback": `
fam hosted-rollback --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 2 --receipt artifacts\hosted-rollback-receipt.json`,
	"hosted-prune": `
# Preview retained and deleted versions.
fam hosted-prune --workspace C:\src\hosted-agent --environment prod --accept-preview --keep 3 --dry-run
# Delete after reviewing the preview.
fam hosted-prune --workspace C:\src\hosted-agent --environment prod --accept-preview --keep 3 --yes --receipt artifacts\hosted-prune-receipt.json`,
	"hosted-delete-version": `
# Preview deletion of one immutable version.
fam hosted-delete-version --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 2 --dry-run
# Delete after reviewing the preview.
fam hosted-delete-version --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 2 --yes --receipt artifacts\hosted-delete-version-receipt.json`,
	"hosted-delete": `
# Preview deletion of the Hosted Agent and sessions.
fam hosted-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --dry-run
# Delete after reviewing the preview.
fam hosted-delete --workspace C:\src\hosted-agent --environment prod --accept-preview --yes --receipt artifacts\hosted-delete-receipt.json`,
	"hosted-logs": `
fam hosted-logs --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3 --session-id session-123 --max-lines 100`,
	"hosted-draft-deploy": `
fam hosted-draft-deploy --workspace C:\src\hosted-agent --environment prod --accept-preview --description candidate --receipt artifacts\draft-receipt.json`,
	"hosted-disable": `
fam hosted-disable --workspace C:\src\hosted-agent --environment prod --accept-preview`,
	"hosted-enable": `
fam hosted-enable --workspace C:\src\hosted-agent --environment prod --accept-preview`,

	"init": `
fam init -f agent.yaml --name support-agent --model my-model --project-resource-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/my-account/projects/support-project`,
	"validate":      `fam validate -f agent.yaml`,
	"plan":          `fam plan -f agent.yaml`,
	"compatibility": `fam compatibility -f agent.yaml`,
	"project-create": `
fam project-create -f agent.yaml --receipt artifacts\project-create-receipt.json`,
	"model-deployment-list": `
fam model-deployment-list -f agent.yaml
fam model-deployment-list -f agent.yaml --output json`,
	"model-deployment-show": `
fam model-deployment-show -f agent.yaml
fam model-deployment-show -f agent.yaml --deployment-name gpt-5-mini`,
	"model-deployment-plan": `
fam model-deployment-plan -f agent.yaml
fam model-deployment-plan -f agent.yaml --deployment-name gpt-5-mini --model-name gpt-5-mini --model-version 2025-08-07 --model-format OpenAI --sku-name GlobalStandard --capacity 10`,
	"model-deployment-create": `
fam model-deployment-create -f agent.yaml
fam model-deployment-create -f agent.yaml --deployment-name gpt-5-mini --model-name gpt-5-mini --model-version 2025-08-07 --model-format OpenAI --sku-name GlobalStandard --capacity 10 --output json`,
	"model-deployment-delete": `
fam model-deployment-delete -f agent.yaml --deployment-name gpt-5-mini --dry-run
fam model-deployment-delete -f agent.yaml --deployment-name gpt-5-mini --yes`,
	"preflight": `fam preflight -f agent.yaml`,
	"deploy": `
fam deploy -f agent.yaml --if-changed
fam deploy -f agent.yaml --if-changed --output json --receipt artifacts\deploy-receipt.json`,
	"status": `fam status -f agent.yaml`,
	"show": `
fam show -f agent.yaml
fam show -f agent.yaml --agent-version 3`,
	"endpoint-show": `fam endpoint-show -f agent.yaml`,
	"endpoint-configure": `
fam endpoint-configure -f agent.yaml --receipt artifacts\endpoint-configure-receipt.json`,
	"versions": `fam versions -f agent.yaml`,
	"diff":     `fam diff -f agent.yaml`,
	"smoke": `
fam smoke -f agent.yaml --prompt health-check`,
	"disable": `fam disable -f agent.yaml`,
	"enable":  `fam enable -f agent.yaml`,
	"promote": `
fam promote -f agent.yaml --agent-version 3 --receipt artifacts\promote-receipt.json
fam promote -f agent.yaml --latest`,
	"rollback": `
# Preview routing to an earlier version.
fam rollback -f agent.yaml --agent-version 2 --dry-run
# Apply after reviewing the preview.
fam rollback -f agent.yaml --agent-version 2 --yes --receipt artifacts\rollback-receipt.json`,
	"prune": `
# Preview retained and deleted versions.
fam prune -f agent.yaml --keep 3 --dry-run
# Delete after reviewing the preview.
fam prune -f agent.yaml --keep 3 --yes`,
	"delete-version": `
# Preview deletion of one immutable version.
fam delete-version -f agent.yaml --agent-version 2 --dry-run
# Delete after reviewing the preview.
fam delete-version -f agent.yaml --agent-version 2 --yes`,
	"delete": `
# Preview deletion of the whole Prompt Agent.
fam delete -f agent.yaml --dry-run
# Delete after reviewing the preview.
fam delete -f agent.yaml --yes`,
	"decommission": `
# Preview deletion of the agent and APIM connection.
fam decommission -f agent.yaml --dry-run
# Delete after reviewing the preview.
fam decommission -f agent.yaml --yes`,

	"connection-list": `fam connection-list -f agent.yaml`,
	"connection-show": `
fam connection-show -f agent.yaml --connection search`,
	"connection-create": `
fam connection-create -f agent.yaml --connection search --connection-type CognitiveSearch --target https://search.example.com --auth-type ApiKey --secret-env SEARCH_API_KEY --receipt artifacts\connection-create-receipt.json`,
	"connection-update": `
fam connection-update -f agent.yaml --connection search --connection-type CognitiveSearch --target https://search.example.com --auth-type ApiKey --secret-env SEARCH_API_KEY --receipt artifacts\connection-update-receipt.json`,
	"connection-delete": `
fam connection-delete -f agent.yaml --connection search --yes --receipt artifacts\connection-delete-receipt.json`,
	"api-center-list": `
fam api-center-list -f agent.yaml --api-center-endpoint https://contoso.azure-apicenter.ms --search payments`,
	"api-center-show": `
fam api-center-show -f agent.yaml --api-center-endpoint https://contoso.azure-apicenter.ms --server payments`,
	"logicapps-registration-plan": `
fam logicapps-registration-plan -f agent.yaml --accept-preview --connector-name my-connector --mcp-server-name operations --mcp-server-description operations-connector --operation get-message`,
	"connector-list": `
fam connector-list -f agent.yaml --accept-preview --search github`,
	"connector-show": `
fam connector-show -f agent.yaml --accept-preview --connector-name github`,
	"connector-create": `
fam connector-create -f agent.yaml --accept-preview --connection github --connector-name github --receipt artifacts\connector-create-receipt.json`,
	"connector-consent": `
fam connector-consent -f agent.yaml --accept-preview --connection github --object-id 00000000-0000-0000-0000-000000000000 --tenant-id 00000000-0000-0000-0000-000000000000`,
	"connector-actions": `
fam connector-actions -f agent.yaml --accept-preview --connector-name github --operation get-issue`,
	"connector-configure": `
fam connector-configure -f agent.yaml --accept-preview --connection github --operation get-issue --operation create-issue --yes --receipt artifacts\connector-configure-receipt.json`,
	"connector-status": `
fam connector-status -f agent.yaml --accept-preview --connection github`,
	"connector-wait": `
fam connector-wait -f agent.yaml --accept-preview --connection github --connector-timeout 10m`,
	"connector-toolbox-deploy": `
fam connector-toolbox-deploy -f agent.yaml --accept-preview --connection github --toolbox-name github-tools --if-changed --receipt artifacts\connector-toolbox-receipt.json`,
	"connector-delete": `
fam connector-delete -f agent.yaml --accept-preview --connection github --yes --receipt artifacts\connector-delete-receipt.json`,

	"toolbox-validate": `fam toolbox-validate -f agent.yaml --toolbox shared-tools`,
	"toolbox-plan":     `fam toolbox-plan -f agent.yaml --toolbox shared-tools`,
	"toolbox-deploy": `
fam toolbox-deploy -f agent.yaml --toolbox shared-tools --if-changed --receipt artifacts\toolbox-deploy-receipt.json`,
	"toolbox-status":   `fam toolbox-status -f agent.yaml --toolbox shared-tools`,
	"toolbox-versions": `fam toolbox-versions -f agent.yaml --toolbox shared-tools`,
	"toolbox-promote": `
# Preview the default-version change.
fam toolbox-promote -f agent.yaml --toolbox shared-tools --toolbox-version 3 --dry-run
# Apply after reviewing the preview.
fam toolbox-promote -f agent.yaml --toolbox shared-tools --toolbox-version 3 --yes --receipt artifacts\toolbox-promote-receipt.json`,
	"toolbox-delete-version": `
# Preview deletion of one non-default version.
fam toolbox-delete-version -f agent.yaml --toolbox shared-tools --toolbox-version 2 --dry-run
# Delete after reviewing the preview.
fam toolbox-delete-version -f agent.yaml --toolbox shared-tools --toolbox-version 2 --yes --receipt artifacts\toolbox-delete-version-receipt.json`,

	"skill-create": `
fam skill-create -f agent.yaml --skill incident-response --path skills\incident-response.zip --accept-preview --default --receipt artifacts\skill-create-receipt.json`,
	"skill-list": `
fam skill-list -f agent.yaml --accept-preview`,
	"skill-show": `
fam skill-show -f agent.yaml --skill incident-response --accept-preview`,
	"skill-version-list": `
fam skill-version-list -f agent.yaml --skill incident-response --accept-preview`,
	"skill-version-show": `
fam skill-version-show -f agent.yaml --skill incident-response --version 3 --accept-preview`,
	"skill-set-default": `
fam skill-set-default -f agent.yaml --skill incident-response --version 3 --accept-preview --receipt artifacts\skill-default-receipt.json`,
	"skill-delete": `
fam skill-delete -f agent.yaml --skill incident-response --accept-preview --yes --receipt artifacts\skill-delete-receipt.json`,
	"skill-version-delete": `
fam skill-version-delete -f agent.yaml --skill incident-response --version 2 --accept-preview --yes --receipt artifacts\skill-version-delete-receipt.json`,
	"skill-download": `
fam skill-download -f agent.yaml --skill incident-response --version 3 --accept-preview --destination downloads\incident-response.zip`,

	"grounding-validate": `fam grounding-validate -f agent.yaml --grounding knowledge`,
	"grounding-plan":     `fam grounding-plan -f agent.yaml --grounding knowledge`,
	"grounding-sync": `
fam grounding-sync -f agent.yaml --grounding knowledge --prune --yes --receipt artifacts\grounding-sync-receipt.json`,
	"grounding-status": `fam grounding-status -f agent.yaml --grounding knowledge`,
	"grounding-delete-file": `
# Preview detaching one document.
fam grounding-delete-file -f agent.yaml --grounding knowledge --file docs\policy.pdf --dry-run
# Detach after reviewing the preview.
fam grounding-delete-file -f agent.yaml --grounding knowledge --file docs\policy.pdf --yes --receipt artifacts\grounding-file-delete-receipt.json`,
	"grounding-delete-store": `
# Preview deletion of the vector store.
fam grounding-delete-store -f agent.yaml --grounding knowledge --dry-run
# Delete after reviewing the preview.
fam grounding-delete-store -f agent.yaml --grounding knowledge --yes --receipt artifacts\grounding-store-delete-receipt.json`,

	"memory-store-validate": `fam memory-store-validate -f agent.yaml --memory-store support-memory`,
	"memory-store-plan":     `fam memory-store-plan -f agent.yaml --memory-store support-memory`,
	"memory-store-sync": `
fam memory-store-sync -f agent.yaml --memory-store support-memory --accept-preview --receipt artifacts\memory-store-sync-receipt.json`,
	"memory-store-list": `
fam memory-store-list -f agent.yaml --accept-preview`,
	"memory-store-show": `
fam memory-store-show -f agent.yaml --memory-store support-memory --accept-preview`,
	"memory-store-delete": `
fam memory-store-delete -f agent.yaml --memory-store support-memory --accept-preview --yes --receipt artifacts\memory-store-delete-receipt.json`,
	"memory-search": `
fam memory-search -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --input password-reset`,
	"memory-update": `
fam memory-update -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --input prefers-email --receipt artifacts\memory-update-receipt.json`,
	"memory-item-create": `
fam memory-item-create -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --content prefers-email --kind user_profile --receipt artifacts\memory-item-create-receipt.json`,
	"memory-item-list": `
fam memory-item-list -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --kind user_profile`,
	"memory-item-show": `
fam memory-item-show -f agent.yaml --memory-store support-memory --accept-preview --memory-id memory-123`,
	"memory-item-update": `
fam memory-item-update -f agent.yaml --memory-store support-memory --accept-preview --memory-id memory-123 --content prefers-chat --receipt artifacts\memory-item-update-receipt.json`,
	"memory-item-delete": `
fam memory-item-delete -f agent.yaml --memory-store support-memory --accept-preview --memory-id memory-123 --yes --receipt artifacts\memory-item-delete-receipt.json`,
	"memory-scope-delete": `
fam memory-scope-delete -f agent.yaml --memory-store support-memory --accept-preview --scope user-42 --yes --receipt artifacts\memory-scope-delete-receipt.json`,

	"publish-m365": `
fam publish-m365 -f agent.yaml --publication publication.yaml --receipt artifacts\m365-publication-receipt.json`,
	"legacy-status": `
fam legacy-status -f agent.yaml --application-name support-app --deployment-name support-deployment`,
	"legacy-deploy": `
fam legacy-deploy -f agent.yaml --application-name support-app --deployment-name support-deployment --agent-version 3 --receipt artifacts\legacy-deploy-receipt.json
fam legacy-deploy -f agent.yaml --application-name support-app --deployment-name support-deployment --agent-version 3 --route --yes`,
	"legacy-delete": `
# Preview deletion of compatibility resources.
fam legacy-delete -f agent.yaml --application-name support-app --deployment-name support-deployment --dry-run
# Delete after reviewing the preview.
fam legacy-delete -f agent.yaml --application-name support-app --deployment-name support-deployment --application --yes --receipt artifacts\legacy-delete-receipt.json`,
}
