// Package errors defines typed errors for foundry-agent-manager.
package errors

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
)

// FoundryAgentManagerError is the base error type for all foundry-agent-manager failures.
type FoundryAgentManagerError struct {
	Kind      string
	Message   string
	Cause     error
	NextSteps []string
}

// ReportedExitError returns a process status after a command has already
// written its complete result. It prevents a second error envelope from
// obscuring machine-readable diagnostic output.
type ReportedExitError struct {
	Code int
}

func (e *ReportedExitError) Error() string { return "command reported a non-success result" }

func (e *FoundryAgentManagerError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}
func (e *FoundryAgentManagerError) Unwrap() error { return e.Cause }

// AmbiguousMutationError marks a mutation whose server-side outcome is unknown.
type AmbiguousMutationError struct {
	Cause error
}

func (e *AmbiguousMutationError) Error() string { return e.Cause.Error() }
func (e *AmbiguousMutationError) Unwrap() error { return e.Cause }

// AmbiguousMutation marks an error that occurred after a mutation request may have reached Azure.
func AmbiguousMutation(err error) error {
	if err == nil {
		return nil
	}
	return &AmbiguousMutationError{Cause: err}
}

// IsAmbiguousMutation reports whether Azure may have committed a failed mutation request.
func IsAmbiguousMutation(err error) bool {
	var ambiguous *AmbiguousMutationError
	return stderrors.As(err, &ambiguous)
}

// ReportedExit requests a nonzero process status without additional output.
func ReportedExit(code int) error {
	if code <= 0 {
		code = 1
	}
	return &ReportedExitError{Code: code}
}

// ReportedExitCode identifies a result that was already rendered by the
// command and returns its requested process status.
func ReportedExitCode(err error) (int, bool) {
	var reported *ReportedExitError
	if !stderrors.As(err, &reported) {
		return 0, false
	}
	if reported.Code <= 0 {
		return 1, true
	}
	return reported.Code, true
}

func Manifest(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "manifest", Message: fmt.Sprintf(msg, args...)}
}

func Security(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "security", Message: fmt.Sprintf(msg, args...)}
}

func Config(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "config", Message: fmt.Sprintf(msg, args...)}
}

func ToolBuild(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "tool", Message: fmt.Sprintf(msg, args...)}
}

func Foundry(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "foundry", Message: fmt.Sprintf(msg, args...)}
}

func Auth(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "auth", Message: fmt.Sprintf(msg, args...)}
}

func Authorization(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{
		Kind:    "authorization",
		Message: fmt.Sprintf(msg, args...),
	}
}

// AuthorizationDenied records an HTTP 403 denial with the most specific
// action and scope Azure returned. The values are used only for remediation.
func AuthorizationDenied(
	action string,
	scope string,
	msg string,
	args ...any,
) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{
		Kind:      "authorization",
		Message:   fmt.Sprintf(msg, args...),
		NextSteps: authorizationRemediation(action, scope),
	}
}

func NotFound(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "not_found", Message: fmt.Sprintf(msg, args...)}
}

func Conflict(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "conflict", Message: fmt.Sprintf(msg, args...)}
}

func Transient(msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "transient", Message: fmt.Sprintf(msg, args...)}
}

func ManifestWrap(err error, msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "manifest", Message: fmt.Sprintf(msg, args...), Cause: err}
}

// SecurityWrap adds context to a containment or destination failure without
// downgrading its security kind, so the process still exits with the security
// exit code instead of a generic one.
func SecurityWrap(err error, msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "security", Message: fmt.Sprintf(msg, args...), Cause: err}
}

func FoundryWrap(err error, msg string, args ...any) *FoundryAgentManagerError {
	var typed *FoundryAgentManagerError
	if stderrors.As(err, &typed) && typed.Kind != "" {
		return &FoundryAgentManagerError{Kind: typed.Kind, Message: fmt.Sprintf(msg, args...), Cause: err}
	}
	return &FoundryAgentManagerError{Kind: "foundry", Message: fmt.Sprintf(msg, args...), Cause: err}
}

func AuthWrap(err error, msg string, args ...any) *FoundryAgentManagerError {
	return &FoundryAgentManagerError{Kind: "auth", Message: fmt.Sprintf(msg, args...), Cause: err}
}

// WithNextSteps attaches explicit operator remediation without changing the
// error kind, message, cause, or exit code.
func WithNextSteps(err error, steps ...string) error {
	if err == nil {
		return nil
	}
	cleaned := cleanSteps(steps)
	if len(cleaned) == 0 {
		return err
	}
	var typed *FoundryAgentManagerError
	if stderrors.As(err, &typed) {
		typed.NextSteps = cleaned
	}
	return err
}

// Remediation returns optional stable next steps for structured error output.
func Remediation(err error) []string {
	if err == nil {
		return nil
	}
	if steps := explicitRemediation(err); len(steps) > 0 {
		return steps
	}

	message := strings.ToLower(err.Error())
	switch KindOf(err) {
	case "manifest":
		return []string{
			"Run foundry-agent-manager prompt validate -f <manifest> after correcting the reported field or contained file.",
		}
	case "auth":
		return []string{
			"Azure rejected the credential, or DefaultAzureCredential could not obtain one. Authenticate to AzureCloud with a supported credential.",
			"Verify the active tenant and token audience, or pass --tenant-id when the target resource belongs to a different tenant.",
		}
	case "authorization":
		return []string{
			"Azure authenticated the principal but denied the requested operation.",
			"Verify that the active principal has a least-privilege Azure RBAC role containing the required action at the target resource scope or an appropriate parent scope.",
			"If access was granted recently, refresh the Azure credential after RBAC propagation and retry.",
		}
	case "security":
		if strings.Contains(message, "not approved") ||
			strings.Contains(message, "destination host") ||
			strings.Contains(message, "managed-identity audience") {
			return []string{
				"Review the exact destination and approve only that host with the matching trust flag, environment variable, or trust policy file.",
				"Do not use wildcard or suffix approvals.",
			}
		}
	case "not_found":
		if strings.Contains(message, "model deployment") {
			return []string{
				"Verify agent.model exactly matches a deployment name available to the selected Foundry project.",
				"Define model_deployment, run foundry-agent-manager model deployment plan -f <manifest>, create it with foundry-agent-manager model deployment create -f <manifest>, then rerun foundry-agent-manager prompt preflight -f <manifest>.",
			}
		}
		if strings.Contains(message, "project") {
			return []string{
				"Verify project.name and the Foundry account coordinates in the manifest.",
				"Create the child project with foundry-agent-manager project create, or use foundry-agent-manager prompt deploy --ensure-project with complete ARM coordinates.",
			}
		}
	case "config":
		switch {
		case strings.Contains(message, "azureusgovernment"):
			return []string{
				"Use AzureCloud. Azure Government remains disabled until it can be live-qualified in a dedicated subscription.",
			}
		case strings.Contains(message, "project.account_endpoint") &&
			strings.Contains(message, "account origin"):
			return []string{
				"Set project.account_endpoint to the account origin only, for example https://<account>.services.ai.azure.com.",
				"Alternatively, put the full https://<account>.services.ai.azure.com/api/projects/<project> URL in project.endpoint.",
			}
		case strings.Contains(message, "project.endpoint") &&
			(strings.Contains(message, "foundry project") ||
				strings.Contains(message, "duplicated /api/projects")):
			return []string{
				"Set project.endpoint to exactly https://<account>.services.ai.azure.com/api/projects/<project>.",
				"Or omit project.endpoint and set project.account_endpoint to the account origin plus project.name.",
			}
		case strings.Contains(message, "azd environment") &&
			(strings.Contains(message, "does not exist") ||
				strings.Contains(message, "no default azd environment exists")):
			return []string{
				"Run foundry-agent-manager hosted environment create --workspace <workspace> --environment <environment>, or rerun with an existing environment name.",
				"Then rerun foundry-agent-manager hosted preflight with the same --environment value.",
			}
		case strings.Contains(message, "azd") || strings.Contains(message, "extension"):
			return []string{
				"Run foundry-agent-manager hosted info to see the required Azure Developer CLI and Hosted Agent extension versions.",
				"Install or select the required tooling, then rerun foundry-agent-manager hosted preflight.",
			}
		case strings.Contains(message, "requires project.subscription_id") ||
			strings.Contains(message, "requires complete project arm coordinates") ||
			strings.Contains(message, "require subscription_id, resource_group, account_name"):
			return []string{
				"Add project.subscription_id, project.resource_group, project.account_name, and project.name to the manifest.",
				"Run foundry-agent-manager prompt validate -f <manifest>, then retry the project or APIM operation.",
			}
		case strings.Contains(message, "--manifest is required"):
			return []string{
				"Pass -f <manifest>, or run foundry-agent-manager quickstart to create one.",
			}
		case strings.Contains(message, "unknown command"):
			return []string{
				"Run foundry-agent-manager --help to see all available commands.",
			}
		case strings.Contains(message, "unknown flag") || strings.Contains(message, "unknown shorthand flag"):
			return []string{
				"Check the flag name with foundry-agent-manager <command path> --help.",
			}
		case strings.Contains(message, "preview") && strings.Contains(message, "accept"):
			return []string{
				"Review the documented preview boundary, then rerun with --accept-preview if it is acceptable.",
			}
		}
	case "tool":
		if strings.Contains(message, "azd") || strings.Contains(message, "hosted") {
			return []string{
				"Run foundry-agent-manager hosted preflight to verify the pinned tooling contract before retrying.",
			}
		}
	}
	return nil
}

func cleanSteps(steps []string) []string {
	cleaned := make([]string, 0, len(steps))
	seen := map[string]struct{}{}
	for _, step := range steps {
		step = strings.TrimSpace(step)
		if step == "" {
			continue
		}
		if _, exists := seen[step]; exists {
			continue
		}
		seen[step] = struct{}{}
		cleaned = append(cleaned, step)
	}
	return cleaned
}

func explicitRemediation(err error) []string {
	pending := []error{err}
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if current == nil {
			continue
		}
		if typed, ok := current.(*FoundryAgentManagerError); ok &&
			len(typed.NextSteps) > 0 {
			return append([]string(nil), typed.NextSteps...)
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			pending = append(pending, wrapped.Unwrap()...)
		case interface{ Unwrap() error }:
			pending = append(pending, wrapped.Unwrap())
		}
	}
	return nil
}

func authorizationRemediation(action string, scope string) []string {
	action = strings.TrimSpace(action)
	scope = strings.TrimSpace(scope)
	switch {
	case action != "" && scope != "":
		return []string{
			fmt.Sprintf(
				"Azure authenticated the principal but denied action %q at scope %q.",
				action,
				scope,
			),
			fmt.Sprintf(
				"Ask an Azure administrator to assign the active principal a least-privilege role containing action %q at scope %q or an appropriate parent scope.",
				action,
				scope,
			),
			"After the role assignment propagates, refresh the Azure credential and retry the command.",
		}
	case action != "":
		return []string{
			fmt.Sprintf(
				"Azure authenticated the principal but denied action %q.",
				action,
			),
			fmt.Sprintf(
				"Ask an Azure administrator to assign the active principal a least-privilege role containing action %q at the target resource scope.",
				action,
			),
			"After the role assignment propagates, refresh the Azure credential and retry the command.",
		}
	case scope != "":
		return []string{
			fmt.Sprintf(
				"Azure authenticated the principal but denied access at scope %q.",
				scope,
			),
			fmt.Sprintf(
				"Ask an Azure administrator to assign the active principal the required least-privilege role at scope %q or an appropriate parent scope.",
				scope,
			),
			"After the role assignment propagates, refresh the Azure credential and retry the command.",
		}
	default:
		return []string{
			"Azure authenticated the principal but denied the requested operation.",
			"Verify that the active principal has a least-privilege Azure RBAC role containing the required action at the target resource scope or an appropriate parent scope.",
			"If access was granted recently, refresh the Azure credential after RBAC propagation and retry.",
		}
	}
}

// IsKind checks if an error is a FoundryAgentManagerError of a given kind.
func IsKind(err error, kind string) bool {
	var e *FoundryAgentManagerError
	if stderrors.As(err, &e) {
		return e.Kind == kind
	}
	return false
}

// IsAuthenticationOrAuthorization reports failures caused by a rejected
// credential or by an authenticated principal lacking permission.
func IsAuthenticationOrAuthorization(err error) bool {
	return IsKind(err, "auth") || IsKind(err, "authorization")
}

// KindOf returns the stable error kind used by structured output.
func KindOf(err error) string {
	if _, ok := ReportedExitCode(err); ok {
		return "reported"
	}
	if stderrors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if stderrors.Is(err, context.DeadlineExceeded) {
		return "transient"
	}
	var e *FoundryAgentManagerError
	if stderrors.As(err, &e) && e.Kind != "" {
		return e.Kind
	}
	return "internal"
}

// ExitCode maps typed failures to stable process exit codes.
func ExitCode(err error) int {
	if code, ok := ReportedExitCode(err); ok {
		return code
	}
	switch KindOf(err) {
	case "cancelled":
		return 130
	case "manifest":
		return 2
	case "config":
		return 3
	case "security":
		return 4
	case "auth":
		return 5
	case "authorization":
		return 5
	case "not_found":
		return 6
	case "conflict":
		return 7
	case "transient":
		return 8
	case "tool":
		return 9
	case "foundry":
		return 10
	default:
		return 1
	}
}
