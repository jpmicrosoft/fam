package logicapps

import (
	"testing"

	"foundry-agent-manager/internal/connection"
)

func TestBuildRegistrationPlanMapsParameterSources(t *testing.T) {
	plan, err := BuildRegistrationPlan(
		RegistrationOptions{
			ConnectorName:     "rss",
			ConnectorAuthType: "None",
			ServerName:        "rss-tools",
			ServerDescription: "Read selected feeds.",
			UserParameters:    []string{"list-items/feedUrl"},
			ModelParameters:   []string{"list-items/count"},
		},
		[]connection.ConnectorOperation{{
			Name: "list-items",
			InputsDefinition: connection.ConnectorInputsDefinition{
				Required: []string{"feedUrl"},
				Properties: map[string]map[string]interface{}{
					"feedUrl": {"type": "string"},
					"count":   {"type": "integer"},
					"unused":  {"type": "string"},
				},
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Automated || !plan.CreateLogicAppInPortal || len(plan.Actions) != 1 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	parameters := plan.Actions[0].Parameters
	if len(parameters) != 2 ||
		parameters[0].Name != "count" ||
		parameters[0].Source != "Model" ||
		parameters[1].Name != "feedUrl" ||
		parameters[1].Source != "User" {
		t.Fatalf("unexpected parameters: %#v", parameters)
	}
}

func TestBuildRegistrationPlanRejectsOAuth2AndUnknownParameter(t *testing.T) {
	operation := connection.ConnectorOperation{
		Name: "read",
		InputsDefinition: connection.ConnectorInputsDefinition{
			Properties: map[string]map[string]interface{}{"id": {"type": "string"}},
		},
	}
	if _, err := BuildRegistrationPlan(
		RegistrationOptions{
			ConnectorName: "mail", ConnectorAuthType: "OAuth2",
			ServerName: "mail-tools", ServerDescription: "Mail tools.",
		},
		[]connection.ConnectorOperation{operation},
	); err == nil {
		t.Fatal("expected OAuth2 rejection")
	}
	if _, err := BuildRegistrationPlan(
		RegistrationOptions{
			ConnectorName: "rss", ConnectorAuthType: "None",
			ServerName: "rss-tools", ServerDescription: "RSS tools.",
			UserParameters: []string{"read/missing"},
		},
		[]connection.ConnectorOperation{operation},
	); err == nil {
		t.Fatal("expected unknown parameter rejection")
	}
}
