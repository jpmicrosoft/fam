package main

import (
	"testing"
)

func TestConnectorToolboxDefinitionBuildsApprovalGatedMCPTool(t *testing.T) {
	definition, err := connectorToolboxDefinition(
		"operations",
		"",
		"logic-apps",
		map[string]interface{}{
			"type":                  "mcp",
			"server_label":          "logic-apps",
			"server_url":            "https://connector.example/mcp",
			"require_approval":      "always",
			"project_connection_id": "logic-apps",
		},
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Name != "operations" ||
		definition.Description != "Managed connector logic-apps" ||
		len(definition.Tools) != 1 {
		t.Fatalf("unexpected definition: %#v", definition)
	}
	tool := definition.Tools[0].(map[string]interface{})
	if tool["require_approval"] != "always" ||
		tool["project_connection_id"] != "logic-apps" {
		t.Fatalf("unexpected managed MCP tool: %#v", tool)
	}
}

func TestSameToolboxConnectionTarget(t *testing.T) {
	expected := "https://acct.services.ai.azure.com/api/projects/project/toolboxes/operations/mcp"
	if !sameToolboxConnectionTarget(
		"HTTPS://ACCT.SERVICES.AI.AZURE.COM:443/api/projects/project/toolboxes/operations/mcp/",
		expected,
	) {
		t.Fatal("equivalent Toolbox targets should match")
	}
	for _, target := range []string{
		"https://acct.services.ai.azure.com/api/projects/project/toolboxes/other/mcp",
		expected + "?api-version=v1",
		"",
	} {
		if sameToolboxConnectionTarget(target, expected) {
			t.Fatalf("unexpected Toolbox target match: %q", target)
		}
	}
}
