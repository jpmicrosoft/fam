package errors

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
)

func TestExitCodeIsStableForEveryKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind string
		code int
	}{
		{name: "manifest", err: Manifest("bad manifest"), kind: "manifest", code: 2},
		{name: "config", err: Config("bad flag"), kind: "config", code: 3},
		{name: "security", err: Security("unapproved host"), kind: "security", code: 4},
		{name: "auth", err: Auth("no token"), kind: "auth", code: 5},
		{name: "authorization", err: Authorization("denied"), kind: "authorization", code: 5},
		{name: "not_found", err: NotFound("absent"), kind: "not_found", code: 6},
		{name: "conflict", err: Conflict("changed"), kind: "conflict", code: 7},
		{name: "transient", err: Transient("retry"), kind: "transient", code: 8},
		{name: "tool", err: ToolBuild("bad tool"), kind: "tool", code: 9},
		{name: "foundry", err: Foundry("service"), kind: "foundry", code: 10},
		{name: "untyped", err: stderrors.New("boom"), kind: "internal", code: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KindOf(tt.err); got != tt.kind {
				t.Fatalf("unexpected kind %q, want %q", got, tt.kind)
			}
			if got := ExitCode(tt.err); got != tt.code {
				t.Fatalf("unexpected exit code %d, want %d", got, tt.code)
			}
		})
	}
}

func TestReportedExitPreservesRenderedOutput(t *testing.T) {
	err := ReportedExit(12)
	code, ok := ReportedExitCode(err)
	if !ok || code != 12 || ExitCode(err) != 12 || KindOf(err) != "reported" {
		t.Fatalf("unexpected reported exit contract: ok=%t code=%d exit=%d kind=%s", ok, code, ExitCode(err), KindOf(err))
	}
}

func TestCancellationAndDeadlineOutrankTypedKinds(t *testing.T) {
	cancelled := FoundryWrap(fmt.Errorf("request failed: %w", context.Canceled), "call failed")
	if KindOf(cancelled) != "cancelled" || ExitCode(cancelled) != 130 {
		t.Fatalf("cancellation must map to exit 130, got %s/%d", KindOf(cancelled), ExitCode(cancelled))
	}
	deadline := FoundryWrap(fmt.Errorf("request failed: %w", context.DeadlineExceeded), "call failed")
	if KindOf(deadline) != "transient" || ExitCode(deadline) != 8 {
		t.Fatalf("deadline must map to exit 8, got %s/%d", KindOf(deadline), ExitCode(deadline))
	}
}

func TestWrappersPreserveKindAndCause(t *testing.T) {
	cause := stderrors.New("root cause")
	tests := map[string]*FoundryAgentManagerError{
		"manifest": ManifestWrap(cause, "context %d", 1),
		"foundry":  FoundryWrap(cause, "context %d", 2),
		"auth":     AuthWrap(cause, "context %d", 3),
		"security": SecurityWrap(cause, "context %d", 4),
	}
	for kind, err := range tests {
		t.Run(kind, func(t *testing.T) {
			if !IsKind(err, kind) {
				t.Fatalf("wrapper lost its kind: %#v", err)
			}
			if !stderrors.Is(err, cause) {
				t.Fatalf("wrapper lost its cause: %v", err)
			}
			if got := err.Error(); got == "" || got == cause.Error() {
				t.Fatalf("wrapper dropped its context: %q", got)
			}
		})
	}
}

func TestSecurityWrapKeepsTheSecurityExitCode(t *testing.T) {
	wrapped := SecurityWrap(Security("escapes the manifest directory"), "tool[0] (openapi)")
	if ExitCode(wrapped) != 4 {
		t.Fatalf("a wrapped containment failure must stay exit 4, got %d", ExitCode(wrapped))
	}
}

func TestFoundryWrapPreservesExistingTypedKind(t *testing.T) {
	wrapped := FoundryWrap(Config("scope is required"), "request failed")
	if KindOf(wrapped) != "config" || ExitCode(wrapped) != 3 {
		t.Fatalf("typed config failure must survive Foundry context, got %s/%d", KindOf(wrapped), ExitCode(wrapped))
	}
}

func TestAmbiguousMutationIsDetectedThroughWrapping(t *testing.T) {
	if AmbiguousMutation(nil) != nil {
		t.Fatal("a nil error must not become an ambiguous mutation")
	}
	inner := FoundryWrap(stderrors.New("connection reset"), "upsert failed")
	ambiguous := AmbiguousMutation(inner)
	if !IsAmbiguousMutation(ambiguous) {
		t.Fatal("expected the mutation to be reported as ambiguous")
	}
	if !IsAmbiguousMutation(fmt.Errorf("joined: %w", ambiguous)) {
		t.Fatal("ambiguity must survive additional wrapping")
	}
	if IsAmbiguousMutation(inner) {
		t.Fatal("a plain failure must not be reported as ambiguous")
	}
	if ambiguous.Error() != inner.Error() {
		t.Fatalf("ambiguity must not change the operator message: %q", ambiguous.Error())
	}
	if KindOf(ambiguous) != "foundry" {
		t.Fatalf("ambiguity must not change the error kind: %s", KindOf(ambiguous))
	}
}

func TestIsKindIgnoresUnrelatedErrors(t *testing.T) {
	if IsKind(stderrors.New("plain"), "security") {
		t.Fatal("an untyped error has no kind")
	}
	if IsKind(nil, "security") {
		t.Fatal("a nil error has no kind")
	}
}

func TestRemediationSupportsExplicitAndDefaultSteps(t *testing.T) {
	explicit := WithNextSteps(Config("custom failure"), " first ", "first", "second")
	if got := Remediation(explicit); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("unexpected explicit remediation: %#v", got)
	}

	auth := Remediation(Auth("credential unavailable"))
	if len(auth) != 2 || !strings.Contains(auth[0], "AzureCloud") {
		t.Fatalf("unexpected auth remediation: %#v", auth)
	}

	if got := Remediation(Conflict("remote state changed")); len(got) != 0 {
		t.Fatalf("unexpected conflict remediation: %#v", got)
	}
}

func TestAuthorizationRemediationIncludesAzureActionAndScope(t *testing.T) {
	action := "Microsoft.CognitiveServices/accounts/deployments/write"
	scope := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct"
	err := AuthorizationDenied(
		action,
		scope,
		"ARM deployment creation failed (403)",
	)
	steps := Remediation(err)
	if len(steps) != 3 ||
		!strings.Contains(steps[0], action) ||
		!strings.Contains(steps[0], scope) ||
		!strings.Contains(steps[1], "least-privilege") ||
		!strings.Contains(steps[2], "propagates") {
		t.Fatalf("authorization remediation is incomplete: %#v", steps)
	}
	if !IsAuthenticationOrAuthorization(err) ||
		!IsAuthenticationOrAuthorization(Auth("no token")) {
		t.Fatal("access-error classification omitted authentication or authorization")
	}
}

func TestWrappedAuthorizationPreservesSpecificRemediation(t *testing.T) {
	inner := AuthorizationDenied(
		"Microsoft.Resources/subscriptions/resourceGroups/read",
		"/subscriptions/sub/resourceGroups/rg",
		"Azure denied the request",
	)
	wrapped := FoundryWrap(inner, "project inspection failed")
	joined := stderrors.Join(wrapped, fmt.Errorf("operation receipt: receipt.json"))
	steps := Remediation(joined)
	if len(steps) == 0 ||
		!strings.Contains(steps[0], "Microsoft.Resources/subscriptions/resourceGroups/read") {
		t.Fatalf("wrapped authorization remediation was lost: %#v", steps)
	}
}

func TestSecurityRemediationOnlyTargetsExplicitApprovals(t *testing.T) {
	if got := Remediation(Security("the destination parent escapes the current directory")); len(got) != 0 {
		t.Fatalf("path containment remediation = %#v, want none", got)
	}
	if got := Remediation(Security("endpoint points to an untrusted commercial lookalike")); len(got) != 0 {
		t.Fatalf("untrusted endpoint remediation = %#v, want none", got)
	}
	if got := Remediation(Security("destination host example.com is not approved")); len(got) == 0 {
		t.Fatal("explicit destination approval failure should include remediation")
	}
}

func TestRemediationForUnknownCommandAndFlag(t *testing.T) {
	unknownCmd := Config("unknown command %q for %q", "foobar", "foundry-agent-manager")
	if got := Remediation(unknownCmd); len(got) == 0 || !strings.Contains(got[0], "--help") {
		t.Fatalf("unknown command should suggest --help, got %#v", got)
	}
	unknownFlag := Config("unknown flag: --bogus")
	if got := Remediation(unknownFlag); len(got) == 0 || !strings.Contains(got[0], "--help") {
		t.Fatalf("unknown flag should suggest --help, got %#v", got)
	}
}

func TestRemediationForProjectEndpointShape(t *testing.T) {
	account := Config(
		`project.account_endpoint %q is a project endpoint, not an account origin`,
		"https://account.services.ai.azure.com/api/projects/default",
	)
	accountSteps := Remediation(account)
	if len(accountSteps) != 2 ||
		!strings.Contains(accountSteps[0], "account origin") ||
		!strings.Contains(accountSteps[1], "project.endpoint") {
		t.Fatalf("unexpected account endpoint remediation: %#v", accountSteps)
	}

	project := Config("project.endpoint contains duplicated /api/projects/<project> paths")
	projectSteps := Remediation(project)
	if len(projectSteps) != 2 ||
		!strings.Contains(projectSteps[0], "/api/projects/<project>") ||
		!strings.Contains(projectSteps[1], "project.account_endpoint") {
		t.Fatalf("unexpected project endpoint remediation: %#v", projectSteps)
	}
}

func TestMissingAZDEnvironmentRemediationIsSpecific(t *testing.T) {
	got := Remediation(Config(
		`Azure Developer CLI environment check failed: azd environment "dev" does not exist; create it outside foundry-agent-manager`,
	))
	if len(got) != 2 ||
		!strings.Contains(got[0], "hosted environment create") ||
		!strings.Contains(got[1], "hosted preflight") {
		t.Fatalf("missing environment remediation is not actionable: %#v", got)
	}
	for _, step := range got {
		if strings.Contains(step, "hosted info") {
			t.Fatalf("missing environment was misclassified as a tooling problem: %#v", got)
		}
	}
}

func TestMissingProjectCoordinatesRemediationIsSpecific(t *testing.T) {
	got := Remediation(Config(
		"project-create requires project.subscription_id, project.resource_group, project.account_name, and project.name",
	))
	if len(got) != 2 ||
		!strings.Contains(got[0], "project.subscription_id") ||
		!strings.Contains(got[1], "validate") {
		t.Fatalf("missing project-coordinate remediation is not actionable: %#v", got)
	}
}

func TestMissingModelDeploymentRemediationIsSpecific(t *testing.T) {
	got := Remediation(NotFound(
		`model deployment "chat-prod" does not exist in the Foundry project "project"`,
	))
	if len(got) != 2 ||
		!strings.Contains(got[0], "agent.model") ||
		!strings.Contains(got[1], "prompt preflight") {
		t.Fatalf("missing model deployment remediation is not actionable: %#v", got)
	}
}
