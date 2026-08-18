package hosted

import (
	"strings"
	"testing"
)

func TestRenderHostedAzureYAMLGuardrails(t *testing.T) {
	const customPolicy = "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/custom-policy"
	tests := []struct {
		name       string
		policyID   string
		noPolicy   bool
		want       string
		wantAbsent string
	}{
		{
			name: "default",
			want: "raiPolicyName: ${RAI_POLICY_ID}",
		},
		{
			name:     "custom",
			policyID: customPolicy,
			want:     "raiPolicyName: " + customPolicy,
		},
		{
			name:       "disabled",
			noPolicy:   true,
			wantAbsent: "policies:",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rendered, err := renderHostedAzureYAML(hostedAzureYAMLOptions{
				AgentName:            "test-agent",
				Source:               "src/test-agent",
				Protocol:             "responses",
				Runtime:              "python_3_13",
				EntryPoint:           "main.py",
				DependencyResolution: "remote_build",
				GuardrailPolicyID:    test.policyID,
				NoGuardrail:          test.noPolicy,
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.want != "" && !strings.Contains(rendered, test.want) {
				t.Fatalf("rendered azure.yaml omitted %q:\n%s", test.want, rendered)
			}
			if test.wantAbsent != "" && strings.Contains(rendered, test.wantAbsent) {
				t.Fatalf("rendered azure.yaml unexpectedly contains %q:\n%s", test.wantAbsent, rendered)
			}
		})
	}
}
