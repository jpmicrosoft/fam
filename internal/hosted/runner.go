package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxCommandOutput = 1 << 20

var (
	ErrHostedUnsupported = errors.New("Foundry Hosted Agents are unavailable in the selected Azure cloud")
	ErrMissingAZD        = errors.New("Azure Developer CLI executable was not found")
	ErrAZDTooOld         = errors.New("Azure Developer CLI version is too old")
	ErrMissingExtension  = errors.New("required Foundry azd extension is not installed")
	ErrAuthentication    = errors.New("Azure Developer CLI authentication check failed")
	ErrEnvironment       = errors.New("Azure Developer CLI environment check failed")
	ErrCommandFailed     = errors.New("Azure Developer CLI command failed")
	ErrOutputTooLarge    = errors.New("Azure Developer CLI output exceeded the safety limit")
	ErrInvalidStatus     = errors.New("Azure Developer CLI returned an invalid Hosted Agent status")
	ErrAgentNotDeployed  = errors.New("Hosted Agent is not deployed in the selected azd environment")
	ErrProjectEndpoint   = errors.New("Hosted Agent Foundry project endpoint could not be resolved")
	ErrProjectID         = errors.New("Hosted Agent Foundry project resource ID is not configured")
	ErrProjectAccess     = errors.New("Hosted Agent Foundry project access check failed")
	semverPattern        = regexp.MustCompile(`(?i)([0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?)`)
	doctorOneFailure     = regexp.MustCompile(`(?m)^\s*\d+\s+passed,\s+1\s+failed,\s+\d+\s+skipped\s*$`)
)

type LookPathFunc func(string) (string, error)

// DefaultLookPath resolves PATH first, then standard Windows azd installation
// locations that may not be visible until the shell is restarted.
func DefaultLookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err == nil && strings.TrimSpace(path) != "" {
		return path, nil
	}
	if !strings.EqualFold(name, "azd") {
		return "", err
	}
	for _, candidate := range standardAZDCandidates(runtime.GOOS, os.Getenv) {
		info, statErr := os.Stat(candidate)
		if statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", err
}

type Command struct {
	Phase         string
	Executable    string
	Args          []string
	Directory     string
	Environment   map[string]string
	CaptureStdout bool
	CaptureStderr bool
}

type Execution struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

type CommandRecord struct {
	Phase      string        `json:"phase" yaml:"phase"`
	Executable string        `json:"executable" yaml:"executable"`
	Args       []string      `json:"args" yaml:"args"`
	Directory  string        `json:"directory" yaml:"directory"`
	ExitCode   int           `json:"exitCode" yaml:"exitCode"`
	Duration   time.Duration `json:"duration" yaml:"duration"`
}

type Recorder func(CommandRecord) error

type Runner interface {
	Run(context.Context, Command) (Execution, error)
}

type ExecRunner struct{}

type PreflightOptions struct {
	Workspace        Workspace
	AZDPath          string
	Environment      string
	CheckEnvironment bool
	CheckProvision   bool
	Runner           Runner
	Record           Recorder
}

type PreflightResult struct {
	AZDPath            string          `json:"azdPath" yaml:"azdPath"`
	AZDVersion         string          `json:"azdVersion" yaml:"azdVersion"`
	AgentExtension     string          `json:"agentExtension" yaml:"agentExtension"`
	AgentExtensionVer  string          `json:"agentExtensionVersion,omitempty" yaml:"agentExtensionVersion,omitempty"`
	Authenticated      bool            `json:"authenticated" yaml:"authenticated"`
	EnvironmentChecked bool            `json:"environmentChecked" yaml:"environmentChecked"`
	Commands           []CommandRecord `json:"commands" yaml:"commands"`
}

// PreflightDiagnostic is one independently evaluated, read-only Hosted
// prerequisite. Error is retained for caller-side classification and is never
// serialized directly.
type PreflightDiagnostic struct {
	Name     string
	Status   string
	Details  string
	Observed string
	Required string
	Duration time.Duration
	Error    error
}

// PreflightDiagnostics contains partial tooling metadata and every independent
// check that could be completed without mutating Azure or local configuration.
type PreflightDiagnostics struct {
	Tooling PreflightResult
	Checks  []PreflightDiagnostic
}

type Status struct {
	ID               string            `json:"id" yaml:"id"`
	Name             string            `json:"name" yaml:"name"`
	Version          string            `json:"version" yaml:"version"`
	Status           string            `json:"status" yaml:"status"`
	Description      *string           `json:"description,omitempty" yaml:"description,omitempty"`
	AgentGUID        string            `json:"agent_guid,omitempty" yaml:"agentGuid,omitempty"`
	PlaygroundURL    string            `json:"playground_url,omitempty" yaml:"playgroundUrl,omitempty"`
	AgentEndpoints   map[string]string `json:"agent_endpoints,omitempty" yaml:"agentEndpoints,omitempty"`
	InstanceIdentity *Identity         `json:"instance_identity,omitempty" yaml:"instanceIdentity,omitempty"`
	Blueprint        *Identity         `json:"blueprint,omitempty" yaml:"blueprint,omitempty"`
}

type Identity struct {
	PrincipalID string `json:"principal_id,omitempty" yaml:"principalId,omitempty"`
	ClientID    string `json:"client_id,omitempty" yaml:"clientId,omitempty"`
}

func ValidateCloud(cloudName string) error {
	if cloudName != "AzureCloud" {
		return fmt.Errorf("%w: %s; no commercial-cloud fallback is allowed", ErrHostedUnsupported, cloudName)
	}
	return nil
}

// ResolveAZD gates cloud availability before consulting PATH.
func ResolveAZD(cloudName string, lookPath LookPathFunc) (string, error) {
	if err := ValidateCloud(cloudName); err != nil {
		return "", err
	}
	if lookPath == nil {
		lookPath = DefaultLookPath
	}
	path, err := lookPath("azd")
	if err != nil || strings.TrimSpace(path) == "" {
		return "", ErrMissingAZD
	}
	return filepath.Clean(path), nil
}

func standardAZDCandidates(goos string, getenv func(string) string) []string {
	if goos != "windows" || getenv == nil {
		return nil
	}
	var candidates []string
	if localAppData := strings.TrimSpace(getenv("LOCALAPPDATA")); localAppData != "" {
		candidates = append(
			candidates,
			filepath.Join(localAppData, "Programs", "Azure Dev CLI", "azd.exe"),
		)
	}
	if programFiles := strings.TrimSpace(getenv("ProgramFiles")); programFiles != "" {
		candidates = append(
			candidates,
			filepath.Join(programFiles, "Azure Dev CLI", "azd.exe"),
		)
	}
	return candidates
}

func (ExecRunner) Run(ctx context.Context, command Command) (Execution, error) {
	started := time.Now()
	process := exec.CommandContext(ctx, command.Executable, command.Args...)
	process.Dir = command.Directory
	process.Env = commandEnvironment(os.Environ(), command.Environment, command.Executable)

	var stdout limitedBuffer
	var stderr limitedBuffer
	if command.CaptureStdout {
		process.Stdout = &stdout
	} else {
		process.Stdout = io.Discard
	}
	if command.CaptureStderr {
		process.Stderr = &stderr
	} else {
		process.Stderr = io.Discard
	}
	runErr := process.Run()
	execution := Execution{
		ExitCode: -1,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(started),
	}
	if process.ProcessState != nil {
		execution.ExitCode = process.ProcessState.ExitCode()
	}
	if stdout.truncated || stderr.truncated {
		return execution, ErrOutputTooLarge
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return execution, ctxErr
	}
	if runErr != nil {
		return execution, runErr
	}
	return execution, nil
}

// DiagnosePreflight evaluates independent Hosted prerequisites without
// stopping after the first failure. It runs only the same read-only inspection
// commands used by CheckPreflight.
func DiagnosePreflight(ctx context.Context, options PreflightOptions) PreflightDiagnostics {
	return diagnosePreflight(ctx, options, false)
}

func diagnosePreflight(
	ctx context.Context,
	options PreflightOptions,
	stopOnFailure bool,
) PreflightDiagnostics {
	result := PreflightDiagnostics{
		Tooling: PreflightResult{
			AZDPath:        options.AZDPath,
			AgentExtension: RequiredExtension,
		},
	}
	add := func(check PreflightDiagnostic) {
		result.Checks = append(result.Checks, check)
	}
	addBlocked := func(name, details, required string) {
		add(PreflightDiagnostic{
			Name:     name,
			Status:   "skipped",
			Details:  details,
			Required: required,
		})
	}
	shouldStop := func() bool {
		return stopOnFailure &&
			len(result.Checks) > 0 &&
			result.Checks[len(result.Checks)-1].Status == "failed"
	}
	run := func(command Command) (Execution, CommandRecord, error) {
		execution, record, err := execute(ctx, options.Runner, command)
		result.Tooling.Commands = append(result.Tooling.Commands, record)
		if options.Record != nil {
			if recordErr := options.Record(record); recordErr != nil {
				return execution, record, recordErr
			}
		}
		return execution, record, err
	}
	command := func(phase string, args ...string) Command {
		return Command{
			Phase:         phase,
			Executable:    options.AZDPath,
			Args:          args,
			Directory:     options.Workspace.Root,
			Environment:   nonInteractiveEnvironment(),
			CaptureStdout: true,
			CaptureStderr: true,
		}
	}
	if options.Runner == nil {
		add(PreflightDiagnostic{
			Name:    "runner",
			Status:  "failed",
			Details: "command runner is required",
			Error:   fmt.Errorf("%w: command runner is required", ErrCommandFailed),
		})
		return result
	}

	environmentValid := true
	if err := ValidateEnvironmentName(options.Environment); err != nil {
		environmentValid = false
		add(PreflightDiagnostic{
			Name:     "environment-name",
			Status:   "failed",
			Details:  err.Error(),
			Observed: options.Environment,
			Required: "1-128 safe azd environment-name characters",
			Error:    err,
		})
	} else {
		add(PreflightDiagnostic{
			Name:     "environment-name",
			Status:   "passed",
			Details:  "the selected azd environment name is safe",
			Observed: options.Environment,
			Required: "safe azd environment name",
		})
	}
	if shouldStop() {
		return result
	}

	versionExecution, versionRecord, versionErr := run(command("azd-version", "version"))
	azdTrusted := false
	if versionErr != nil {
		classified := fmt.Errorf("%w: %w", ErrCommandFailed, versionErr)
		add(PreflightDiagnostic{
			Name:     "azd-version",
			Status:   "failed",
			Details:  classified.Error(),
			Required: MinimumAZDVersion + " or later",
			Duration: versionRecord.Duration,
			Error:    classified,
		})
	} else if version, err := parseVersion(versionExecution.Stdout + "\n" + versionExecution.Stderr); err != nil {
		add(PreflightDiagnostic{
			Name:     "azd-version",
			Status:   "failed",
			Details:  err.Error(),
			Required: MinimumAZDVersion + " or later",
			Duration: versionRecord.Duration,
			Error:    err,
		})
	} else if compareVersions(version, MinimumAZDVersion) < 0 {
		classified := fmt.Errorf("%w: found %s, require %s or later", ErrAZDTooOld, version, MinimumAZDVersion)
		add(PreflightDiagnostic{
			Name:     "azd-version",
			Status:   "failed",
			Details:  classified.Error(),
			Observed: version,
			Required: MinimumAZDVersion + " or later",
			Duration: versionRecord.Duration,
			Error:    classified,
		})
	} else {
		result.Tooling.AZDVersion = version
		azdTrusted = true
		add(PreflightDiagnostic{
			Name:     "azd-version",
			Status:   "passed",
			Details:  "Azure Developer CLI version is supported",
			Observed: version,
			Required: MinimumAZDVersion + " or later",
			Duration: versionRecord.Duration,
		})
	}
	if shouldStop() {
		return result
	}
	if !azdTrusted {
		addBlocked(
			"agent-extension-installed",
			"extension inspection was blocked because the Azure Developer CLI version is not trusted",
			RequiredExtension+" "+RequiredExtensionVer,
		)
		addBlocked(
			"agent-extension-command",
			"extension execution was blocked because the Azure Developer CLI version is not trusted",
			RequiredExtensionVer,
		)
		addBlocked(
			"deploy-contract",
			"deploy contract inspection was blocked because the Azure Developer CLI version is not trusted",
			"supported azd deploy contract",
		)
		addBlocked(
			"status-contract",
			"Hosted Agent extension execution was blocked because the Azure Developer CLI version is not trusted",
			"reviewed Hosted Agent status contract",
		)
		if options.CheckProvision {
			addBlocked(
				"provision-contract",
				"provision contract inspection was blocked because the Azure Developer CLI version is not trusted",
				"supported azd provision contract",
			)
		} else {
			addBlocked(
				"provision-contract",
				"provision command inspection requires --check-provision",
				"",
			)
		}
		addBlocked(
			"azd-authentication",
			"authentication inspection was blocked because the Azure Developer CLI version is not trusted",
			"authenticated azd identity",
		)
		if options.CheckEnvironment {
			addBlocked(
				"azd-environment",
				"environment inspection was blocked because the Azure Developer CLI version is not trusted",
				"existing selected or default azd environment",
			)
		}
		return result
	}

	extensionsExecution, extensionsRecord, extensionsErr := run(command(
		"azd-extensions",
		"extension", "list", "--output", "json",
	))
	extensionPinned := false
	if extensionsErr != nil {
		classified := fmt.Errorf("%w: could not list installed extensions: %w", ErrCommandFailed, extensionsErr)
		add(PreflightDiagnostic{
			Name:     "agent-extension-installed",
			Status:   "failed",
			Details:  classified.Error(),
			Required: RequiredExtension + " " + RequiredExtensionVer,
			Duration: extensionsRecord.Duration,
			Error:    classified,
		})
	} else if extensions, err := parseExtensions(extensionsExecution.Stdout); err != nil {
		add(PreflightDiagnostic{
			Name:     "agent-extension-installed",
			Status:   "failed",
			Details:  err.Error(),
			Required: RequiredExtension + " " + RequiredExtensionVer,
			Duration: extensionsRecord.Duration,
			Error:    err,
		})
	} else if installedVersion, installed := extensions[RequiredExtension]; !installed {
		classified := fmt.Errorf("%w: %s", ErrMissingExtension, RequiredExtension)
		add(PreflightDiagnostic{
			Name:     "agent-extension-installed",
			Status:   "failed",
			Details:  classified.Error(),
			Observed: "not installed",
			Required: RequiredExtension + " " + RequiredExtensionVer,
			Duration: extensionsRecord.Duration,
			Error:    classified,
		})
	} else if installedVersion != RequiredExtensionVer {
		classified := fmt.Errorf(
			"%w: %s version %q is installed; require %s",
			ErrMissingExtension,
			RequiredExtension,
			installedVersion,
			RequiredExtensionVer,
		)
		add(PreflightDiagnostic{
			Name:     "agent-extension-installed",
			Status:   "failed",
			Details:  classified.Error(),
			Observed: installedVersion,
			Required: RequiredExtensionVer,
			Duration: extensionsRecord.Duration,
			Error:    classified,
		})
	} else {
		result.Tooling.AgentExtensionVer = installedVersion
		extensionPinned = true
		add(PreflightDiagnostic{
			Name:     "agent-extension-installed",
			Status:   "passed",
			Details:  "the reviewed Hosted Agent extension is installed",
			Observed: installedVersion,
			Required: RequiredExtensionVer,
			Duration: extensionsRecord.Duration,
		})
	}
	if shouldStop() {
		return result
	}

	extensionRuntimeTrusted := false
	if !extensionPinned {
		addBlocked(
			"agent-extension-command",
			"Hosted Agent extension execution was blocked because the installed extension is missing or not the reviewed version",
			RequiredExtensionVer,
		)
	} else {
		agentExecution, agentRecord, agentErr := run(command(
			"agent-extension-version",
			"ai", "agent", "version",
		))
		if agentErr != nil {
			classified := fmt.Errorf(
				"%w: %s command surface is unavailable: %w",
				ErrMissingExtension,
				RequiredExtension,
				agentErr,
			)
			add(PreflightDiagnostic{
				Name:     "agent-extension-command",
				Status:   "failed",
				Details:  classified.Error(),
				Required: RequiredExtensionVer,
				Duration: agentRecord.Duration,
				Error:    classified,
			})
		} else if version, err := parseVersion(agentExecution.Stdout + "\n" + agentExecution.Stderr); err != nil {
			add(PreflightDiagnostic{
				Name:     "agent-extension-command",
				Status:   "failed",
				Details:  err.Error(),
				Required: RequiredExtensionVer,
				Duration: agentRecord.Duration,
				Error:    err,
			})
		} else if version != RequiredExtensionVer {
			classified := fmt.Errorf(
				"%w: %s command reported version %q; require %s",
				ErrMissingExtension,
				RequiredExtension,
				version,
				RequiredExtensionVer,
			)
			add(PreflightDiagnostic{
				Name:     "agent-extension-command",
				Status:   "failed",
				Details:  classified.Error(),
				Observed: version,
				Required: RequiredExtensionVer,
				Duration: agentRecord.Duration,
				Error:    classified,
			})
		} else {
			result.Tooling.AgentExtensionVer = version
			extensionRuntimeTrusted = true
			add(PreflightDiagnostic{
				Name:     "agent-extension-command",
				Status:   "passed",
				Details:  "the Hosted Agent command surface reports the reviewed version",
				Observed: version,
				Required: RequiredExtensionVer,
				Duration: agentRecord.Duration,
			})
		}
	}
	if shouldStop() {
		return result
	}

	contractChecks := []struct {
		name   string
		args   []string
		tokens []string
	}{
		{
			name:   "deploy-contract",
			args:   []string{"deploy", "--help"},
			tokens: []string{"deploy <service>", "--no-prompt", "--environment"},
		},
	}
	if options.CheckProvision {
		contractChecks = append(contractChecks, struct {
			name   string
			args   []string
			tokens []string
		}{
			name:   "provision-contract",
			args:   []string{"provision", "--help"},
			tokens: []string{"azd provision", "--preview", "--no-prompt"},
		})
	}
	if extensionRuntimeTrusted {
		contractChecks = append(contractChecks, struct {
			name   string
			args   []string
			tokens []string
		}{
			name:   "status-contract",
			args:   []string{"ai", "agent", "show", "--help"},
			tokens: []string{"show [name]", "--output", "--no-prompt"},
		})
	} else {
		addBlocked(
			"status-contract",
			"Hosted Agent status contract inspection was blocked because the reviewed extension command surface was not proven",
			"show [name], --output, --no-prompt",
		)
	}
	for _, contract := range contractChecks {
		execution, record, err := run(command(contract.name, contract.args...))
		if err != nil {
			classified := fmt.Errorf("%w: could not inspect %s: %w", ErrCommandFailed, contract.name, err)
			add(PreflightDiagnostic{
				Name:     contract.name,
				Status:   "failed",
				Details:  classified.Error(),
				Required: strings.Join(contract.tokens, ", "),
				Duration: record.Duration,
				Error:    classified,
			})
			if shouldStop() {
				return result
			}
			continue
		}
		if err := requireHelpTokens(execution.Stdout+"\n"+execution.Stderr, contract.tokens...); err != nil {
			add(PreflightDiagnostic{
				Name:     contract.name,
				Status:   "failed",
				Details:  err.Error(),
				Required: strings.Join(contract.tokens, ", "),
				Duration: record.Duration,
				Error:    err,
			})
			if shouldStop() {
				return result
			}
			continue
		}
		add(PreflightDiagnostic{
			Name:     contract.name,
			Status:   "passed",
			Details:  "the required read-only command contract is available",
			Required: strings.Join(contract.tokens, ", "),
			Duration: record.Duration,
		})
	}
	if !options.CheckProvision {
		add(PreflightDiagnostic{
			Name:    "provision-contract",
			Status:  "skipped",
			Details: "provision command inspection requires --check-provision",
		})
	}

	authExecution, authRecord, authErr := run(command(
		"azd-auth",
		"auth", "status", "--no-prompt",
	))
	if authErr != nil {
		classified := fmt.Errorf("%w; run 'azd auth login' outside foundry-agent-manager: %w", ErrAuthentication, authErr)
		add(PreflightDiagnostic{
			Name:     "azd-authentication",
			Status:   "failed",
			Details:  classified.Error(),
			Required: "authenticated azd identity",
			Duration: authRecord.Duration,
			Error:    classified,
		})
	} else if strings.Contains(strings.ToLower(authExecution.Stdout+"\n"+authExecution.Stderr), "not logged in") {
		classified := fmt.Errorf("%w; run 'azd auth login' outside foundry-agent-manager", ErrAuthentication)
		add(PreflightDiagnostic{
			Name:     "azd-authentication",
			Status:   "failed",
			Details:  classified.Error(),
			Observed: "not logged in",
			Required: "authenticated azd identity",
			Duration: authRecord.Duration,
			Error:    classified,
		})
	} else {
		result.Tooling.Authenticated = true
		add(PreflightDiagnostic{
			Name:     "azd-authentication",
			Status:   "passed",
			Details:  "azd reports an authenticated identity",
			Observed: "authenticated",
			Required: "authenticated azd identity",
			Duration: authRecord.Duration,
		})
	}
	if shouldStop() {
		return result
	}

	if options.CheckEnvironment && environmentValid {
		environmentExecution, environmentRecord, environmentErr := run(command(
			"azd-environment",
			"env", "list", "--output", "json", "--no-prompt",
		))
		if environmentErr != nil {
			classified := fmt.Errorf("%w; select or create an azd environment outside foundry-agent-manager: %w", ErrEnvironment, environmentErr)
			add(PreflightDiagnostic{
				Name:     "azd-environment",
				Status:   "failed",
				Details:  classified.Error(),
				Observed: options.Environment,
				Required: "existing selected or default azd environment",
				Duration: environmentRecord.Duration,
				Error:    classified,
			})
		} else if err := requireEnvironment(environmentExecution.Stdout, options.Environment); err != nil {
			add(PreflightDiagnostic{
				Name:     "azd-environment",
				Status:   "failed",
				Details:  err.Error(),
				Observed: options.Environment,
				Required: "existing selected or default azd environment",
				Duration: environmentRecord.Duration,
				Error:    err,
			})
		} else {
			result.Tooling.EnvironmentChecked = true
			add(PreflightDiagnostic{
				Name:     "azd-environment",
				Status:   "passed",
				Details:  "the selected or default azd environment exists",
				Observed: options.Environment,
				Required: "existing selected or default azd environment",
				Duration: environmentRecord.Duration,
			})
		}
	} else if options.CheckEnvironment {
		add(PreflightDiagnostic{
			Name:    "azd-environment",
			Status:  "skipped",
			Details: "the environment lookup was skipped because the environment name is invalid",
		})
	}
	return result
}

func CheckPreflight(ctx context.Context, options PreflightOptions) (PreflightResult, error) {
	if options.Runner == nil {
		return PreflightResult{}, fmt.Errorf("%w: command runner is required", ErrCommandFailed)
	}
	if err := ValidateEnvironmentName(options.Environment); err != nil {
		return PreflightResult{}, err
	}
	diagnostics := diagnosePreflight(ctx, options, true)
	for _, check := range diagnostics.Checks {
		if check.Status == "failed" && check.Error != nil {
			return diagnostics.Tooling, check.Error
		}
	}
	return diagnostics.Tooling, nil
}

func requireEnvironment(raw, expected string) error {
	exists, err := environmentExists(raw, expected)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if expected == "" {
		return fmt.Errorf(
			"%w: no default azd environment exists; select or create one outside foundry-agent-manager",
			ErrEnvironment,
		)
	}
	return fmt.Errorf(
		"%w: azd environment %q does not exist; create it outside foundry-agent-manager",
		ErrEnvironment,
		expected,
	)
}

func RunProvision(
	ctx context.Context,
	runner Runner,
	azdPath string,
	workspace Workspace,
	environment string,
	preview bool,
	record Recorder,
) ([]CommandRecord, error) {
	records := make([]CommandRecord, 0, 2)
	run := func(command Command) error {
		_, commandRecord, err := execute(ctx, runner, command)
		records = append(records, commandRecord)
		if record != nil {
			if recordErr := record(commandRecord); recordErr != nil {
				return recordErr
			}
		}
		return err
	}
	if preview {
		args := appendEnvironment([]string{"provision", "--preview", "--no-prompt"}, environment)
		if err := run(Command{
			Phase:       "provision-preview",
			Executable:  azdPath,
			Args:        args,
			Directory:   workspace.Root,
			Environment: nonInteractiveEnvironment(),
		}); err != nil {
			return records, fmt.Errorf("%w during provision preview: %w", ErrCommandFailed, err)
		}
	}
	args := appendEnvironment([]string{"provision", "--no-prompt"}, environment)
	if err := run(Command{
		Phase:       "provision",
		Executable:  azdPath,
		Args:        args,
		Directory:   workspace.Root,
		Environment: nonInteractiveEnvironment(),
	}); err != nil {
		return records, fmt.Errorf("%w during provision: %w", ErrCommandFailed, err)
	}
	return records, nil
}

func RunDeploy(
	ctx context.Context,
	runner Runner,
	azdPath string,
	workspace Workspace,
	environment string,
	record Recorder,
) (CommandRecord, error) {
	args := []string{"deploy", workspace.Selected.ServiceName, "--no-prompt"}
	args = appendEnvironment(args, environment)
	execution, commandRecord, err := execute(ctx, runner, Command{
		Phase:         "deploy",
		Executable:    azdPath,
		Args:          args,
		Directory:     workspace.Root,
		Environment:   nonInteractiveEnvironment(),
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if record != nil {
		if recordErr := record(commandRecord); recordErr != nil {
			return commandRecord, recordErr
		}
	}
	if err != nil {
		if classified := classifyProjectAccessOutput(execution.Stdout + "\n" + execution.Stderr); classified != nil {
			return commandRecord, fmt.Errorf("%w during agent deployment: %w", ErrCommandFailed, classified)
		}
		return commandRecord, fmt.Errorf("%w during agent deployment: %w", ErrCommandFailed, err)
	}
	return commandRecord, nil
}

// RunDoctor executes azd's read-only Hosted Agent diagnostics with the same
// azd identity and workspace context that deployment will use.
func RunDoctor(
	ctx context.Context,
	runner Runner,
	azdPath string,
	workspace Workspace,
	environment string,
	record Recorder,
) (CommandRecord, error) {
	args := appendEnvironment(
		[]string{"ai", "agent", "doctor", "--no-prompt"},
		environment,
	)
	execution, commandRecord, err := execute(ctx, runner, Command{
		Phase:         "doctor",
		Executable:    azdPath,
		Args:          args,
		Directory:     workspace.Root,
		Environment:   nonInteractiveEnvironment(),
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if record != nil {
		if recordErr := record(commandRecord); recordErr != nil {
			return commandRecord, recordErr
		}
	}

	// Classify output regardless of exit code to prevent fail-open when the
	// extension returns exit 0 but skipped critical RBAC checks.
	output := execution.Stdout + "\n" + execution.Stderr
	if classified := classifyProjectAccessOutput(output); classified != nil {
		return commandRecord, classified
	}

	// Require affirmative evidence that the project-role check passed.
	// Without this, a skipped check (e.g. missing AZURE_AI_PROJECT_ID) that
	// does not surface in classifyProjectAccessOutput would be accepted.
	if !doctorConfirmsProjectRoleVerified(output) {
		return commandRecord, fmt.Errorf(
			"%w: azd requires AZURE_AI_PROJECT_ID in the selected environment to verify the deployment identity",
			ErrProjectID,
		)
	}

	if err != nil {
		if doctorReportsOnlyUndeployed(output) {
			return commandRecord, nil
		}
		return commandRecord, fmt.Errorf(
			"%w during Hosted Agent diagnostics: %w",
			ErrCommandFailed,
			err,
		)
	}
	return commandRecord, nil
}

// doctorConfirmsProjectRoleVerified returns true only when the doctor output
// contains affirmative evidence that the project-role validation executed and
// passed (was not skipped).
func doctorConfirmsProjectRoleVerified(output string) bool {
	normalized := strings.ToLower(output)
	// The extension prints this line only when the RBAC check actually ran
	// and the identity has the required role. A skipped check appends
	// "-- skipped" which this match intentionally excludes.
	hasRoleLine := strings.Contains(normalized, "developer has required role on foundry project")
	if !hasRoleLine {
		return false
	}
	// Reject if the line indicates the check was skipped.
	return !strings.Contains(normalized, "developer has required role on foundry project -- skipped") &&
		!strings.Contains(normalized, "developer has required role on foundry project --skipped")
}

func classifyProjectAccessOutput(output string) error {
	normalized := strings.ToLower(output)
	projectEndpointName := strings.ToLower(ReservedProjectEnv)
	if strings.Contains(normalized, projectEndpointName+" is not set") ||
		strings.Contains(normalized, projectEndpointName+" not set") ||
		strings.Contains(normalized, "missing "+projectEndpointName) {
		return fmt.Errorf(
			"%w: azd requires %s in the selected environment",
			ErrProjectEndpoint,
			ReservedProjectEnv,
		)
	}
	if strings.Contains(normalized, "foundry returned http 403") ||
		strings.Contains(normalized, "http status code 403") ||
		strings.Contains(normalized, "status code: 403") ||
		strings.Contains(normalized, "403 forbidden") {
		return fmt.Errorf(
			"%w: azd received HTTP 403 from the selected Foundry project; its authenticated tenant or RBAC assignments do not authorize deployment",
			ErrProjectAccess,
		)
	}
	projectIDName := strings.ToLower("AZURE_AI_PROJECT_ID")
	if strings.Contains(normalized, projectIDName+" is not set") ||
		strings.Contains(normalized, projectIDName+" not set") ||
		strings.Contains(normalized, "missing "+projectIDName) {
		return fmt.Errorf(
			"%w: azd requires AZURE_AI_PROJECT_ID in the selected environment to verify the deployment identity",
			ErrProjectID,
		)
	}
	return nil
}

func doctorReportsOnlyUndeployed(output string) bool {
	normalized := strings.ToLower(output)
	return strings.Contains(normalized, "foundry project endpoint reachable") &&
		strings.Contains(normalized, "developer has required role on foundry project") &&
		strings.Contains(normalized, "agents have not been deployed") &&
		doctorOneFailure.MatchString(normalized)
}

func ShowStatus(
	ctx context.Context,
	runner Runner,
	azdPath string,
	workspace Workspace,
	environment string,
	record Recorder,
) (Status, CommandRecord, error) {
	args := []string{
		"ai", "agent", "show", workspace.Selected.ServiceName,
		"--output", "json",
		"--no-prompt",
	}
	args = appendEnvironment(args, environment)
	execution, commandRecord, err := execute(ctx, runner, Command{
		Phase:         "status",
		Executable:    azdPath,
		Args:          args,
		Directory:     workspace.Root,
		Environment:   nonInteractiveEnvironment(),
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if record != nil {
		if recordErr := record(commandRecord); recordErr != nil {
			return Status{}, commandRecord, recordErr
		}
	}
	if err != nil {
		output := execution.Stdout + "\n" + execution.Stderr
		if strings.Contains(output, "agent name could not be resolved from azd environment") ||
			strings.Contains(output, "agent version could not be resolved from azd environment") {
			return Status{}, commandRecord, fmt.Errorf("%w", ErrAgentNotDeployed)
		}
		return Status{}, commandRecord, fmt.Errorf(
			"%w during status reconciliation: %w",
			ErrCommandFailed,
			err,
		)
	}
	var status Status
	if err := json.Unmarshal([]byte(execution.Stdout), &status); err != nil {
		return Status{}, commandRecord, fmt.Errorf("%w: %v", ErrInvalidStatus, err)
	}
	if status.Name == "" || status.Version == "" || status.Status == "" {
		return Status{}, commandRecord, fmt.Errorf(
			"%w: name, version, and status are required",
			ErrInvalidStatus,
		)
	}
	return status, commandRecord, nil
}

// ResolveProjectEndpoint returns the validated Foundry project endpoint without
// requesting the azd environment's complete value set.
func ResolveProjectEndpoint(
	ctx context.Context,
	runner Runner,
	azdPath string,
	workspace Workspace,
	environment string,
	record Recorder,
) (string, error) {
	if endpoint := strings.TrimSpace(workspace.Selected.ProjectEndpoint); endpoint != "" {
		if err := validateProjectEndpoint(endpoint, "selected Hosted Agent project endpoint"); err != nil {
			return "", err
		}
		return endpoint, nil
	}
	for _, name := range []string{ReservedProjectEnv, LegacyProjectEnv} {
		args := appendEnvironment(
			[]string{"env", "get-value", name, "--no-prompt"},
			environment,
		)
		execution, commandRecord, err := execute(ctx, runner, Command{
			Phase:         "project-endpoint",
			Executable:    azdPath,
			Args:          args,
			Directory:     workspace.Root,
			Environment:   nonInteractiveEnvironment(),
			CaptureStdout: true,
			CaptureStderr: true,
		})
		if record != nil {
			if recordErr := record(commandRecord); recordErr != nil {
				return "", recordErr
			}
		}
		if err != nil {
			if errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, ErrOutputTooLarge) {
				return "", err
			}
			continue
		}
		endpoint := strings.TrimSpace(execution.Stdout)
		if endpoint == "" {
			continue
		}
		if err := validateProjectEndpoint(
			endpoint,
			"azd environment "+name,
		); err != nil {
			return "", err
		}
		return endpoint, nil
	}
	return "", fmt.Errorf(
		"%w; set %s in the selected azd environment",
		ErrProjectEndpoint,
		ReservedProjectEnv,
	)
}

// ResolveServiceEnvironment resolves only the azd values explicitly referenced
// by the selected Hosted service. It never requests the complete environment.
func ResolveServiceEnvironment(
	ctx context.Context,
	runner Runner,
	azdPath string,
	workspace Workspace,
	environment string,
	record Recorder,
) (map[string]string, error) {
	if len(workspace.Selected.Environment) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(workspace.Selected.Environment))
	for name := range workspace.Selected.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	resolved := make(map[string]string, len(names))
	for _, name := range names {
		value := workspace.Selected.Environment[name]
		if !envReferencePattern.MatchString(value) {
			resolved[name] = value
			continue
		}
		referenceName := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
		resolvedValue, err := ResolveEnvironmentValue(
			ctx,
			runner,
			azdPath,
			workspace,
			environment,
			referenceName,
			record,
		)
		if err != nil {
			return nil, err
		}
		resolved[name] = resolvedValue
	}
	return resolved, nil
}

// ResolveEnvironmentValue reads one validated azd environment value without
// exposing or enumerating any unrelated values.
func ResolveEnvironmentValue(
	ctx context.Context,
	runner Runner,
	azdPath string,
	workspace Workspace,
	environment string,
	name string,
	record Recorder,
) (string, error) {
	if !envNamePattern.MatchString(name) {
		return "", fmt.Errorf("%w: invalid environment variable name %q", ErrEnvironment, name)
	}
	args := appendEnvironment(
		[]string{"env", "get-value", name, "--no-prompt"},
		environment,
	)
	execution, commandRecord, err := execute(ctx, runner, Command{
		Phase:         "environment-value",
		Executable:    azdPath,
		Args:          args,
		Directory:     workspace.Root,
		Environment:   nonInteractiveEnvironment(),
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if record != nil {
		if recordErr := record(commandRecord); recordErr != nil {
			return "", recordErr
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, ErrOutputTooLarge) {
			return "", err
		}
		return "", fmt.Errorf("%w: failed to resolve %s from the selected azd environment: %w", ErrEnvironment, name, err)
	}
	return strings.TrimRight(execution.Stdout, "\r\n"), nil
}

func execute(ctx context.Context, runner Runner, command Command) (Execution, CommandRecord, error) {
	command.Args = append([]string(nil), command.Args...)
	command.Environment = copyEnvironment(command.Environment)
	execution, err := runner.Run(ctx, command)
	record := CommandRecord{
		Phase:      command.Phase,
		Executable: command.Executable,
		Args:       append([]string(nil), command.Args...),
		Directory:  command.Directory,
		ExitCode:   execution.ExitCode,
		Duration:   execution.Duration,
	}
	if err != nil && execution.ExitCode == 0 {
		record.ExitCode = -1
	}
	return execution, record, err
}

func parseVersion(output string) (string, error) {
	match := semverPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("%w: could not parse version output", ErrCommandFailed)
	}
	return match[1], nil
}

func compareVersions(left, right string) int {
	leftParts := versionParts(left)
	rightParts := versionParts(right)
	for i := range leftParts {
		if leftParts[i] < rightParts[i] {
			return -1
		}
		if leftParts[i] > rightParts[i] {
			return 1
		}
	}
	leftPrerelease := prereleaseVersion(left)
	rightPrerelease := prereleaseVersion(right)
	switch {
	case leftPrerelease == "" && rightPrerelease != "":
		return 1
	case leftPrerelease != "" && rightPrerelease == "":
		return -1
	case leftPrerelease != "" && rightPrerelease != "":
		return comparePrerelease(leftPrerelease, rightPrerelease)
	}
	return 0
}

func versionParts(version string) [3]int {
	var result [3]int
	core := strings.FieldsFunc(version, func(r rune) bool { return r == '-' || r == '+' })[0]
	parts := strings.Split(core, ".")
	for i := 0; i < len(result) && i < len(parts); i++ {
		result[i], _ = strconv.Atoi(parts[i])
	}
	return result
}

func prereleaseVersion(version string) string {
	core := strings.SplitN(version, "+", 2)[0]
	_, prerelease, found := strings.Cut(core, "-")
	if !found {
		return ""
	}
	return prerelease
}

func comparePrerelease(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for i := 0; i < len(leftParts) && i < len(rightParts); i++ {
		leftNumber, leftErr := strconv.Atoi(leftParts[i])
		rightNumber, rightErr := strconv.Atoi(rightParts[i])
		switch {
		case leftErr == nil && rightErr == nil && leftNumber < rightNumber:
			return -1
		case leftErr == nil && rightErr == nil && leftNumber > rightNumber:
			return 1
		case leftErr == nil && rightErr != nil:
			return -1
		case leftErr != nil && rightErr == nil:
			return 1
		case leftParts[i] < rightParts[i]:
			return -1
		case leftParts[i] > rightParts[i]:
			return 1
		}
	}
	switch {
	case len(leftParts) < len(rightParts):
		return -1
	case len(leftParts) > len(rightParts):
		return 1
	default:
		return 0
	}
}

func parseExtensions(output string) (map[string]string, error) {
	result := make(map[string]string)
	var value any
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		return nil, fmt.Errorf("%w: extension list did not return valid JSON", ErrCommandFailed)
	}
	var list []any
	switch typed := value.(type) {
	case []any:
		list = typed
	case map[string]any:
		for _, key := range []string{"extensions", "value", "items"} {
			if candidate, ok := typed[key].([]any); ok {
				list = candidate
				break
			}
		}
	}
	if list == nil {
		return nil, fmt.Errorf("%w: extension list JSON has an unsupported shape", ErrCommandFailed)
	}
	for _, raw := range list {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := firstString(item, "id", "name", "extensionId")
		if id == "" {
			continue
		}
		if installedVersion, present := item["installedVersion"]; present {
			version, _ := installedVersion.(string)
			if version != "" {
				result[id] = version
			}
			continue
		}
		result[id] = firstString(item, "version")
	}
	return result, nil
}

func firstString(document map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := document[name].(string); ok {
			return value
		}
	}
	return ""
}

func requireHelpTokens(output string, tokens ...string) error {
	for _, token := range tokens {
		if !strings.Contains(output, token) {
			return fmt.Errorf(
				"%w: installed azd command contract is missing %q",
				ErrCommandFailed,
				token,
			)
		}
	}
	return nil
}

func appendEnvironment(args []string, environment string) []string {
	if environment == "" {
		return args
	}
	return append(args, "--environment", environment)
}

func nonInteractiveEnvironment() map[string]string {
	return map[string]string{"AZD_NON_INTERACTIVE": "true"}
}

func commandEnvironment(base []string, overrides map[string]string, executable string) []string {
	effective := make(map[string]string, len(overrides)+1)
	for key, value := range overrides {
		effective[key] = value
	}
	executableDirectory := filepath.Dir(executable)
	if executableDirectory != "" && executableDirectory != "." {
		pathValue := environmentValue(effective, "PATH")
		if pathValue == "" {
			pathValue = environmentValueFromList(base, "PATH")
		}
		if pathValue == "" {
			effective["PATH"] = executableDirectory
		} else {
			effective["PATH"] = executableDirectory + string(os.PathListSeparator) + pathValue
		}
	}
	return mergeEnvironment(base, effective)
}

func environmentValue(values map[string]string, name string) string {
	for key, value := range values {
		if environmentKey(key) == environmentKey(name) {
			return value
		}
	}
	return ""
}

func environmentValueFromList(values []string, name string) string {
	for _, entry := range values {
		key, value, found := strings.Cut(entry, "=")
		if found && environmentKey(key) == environmentKey(name) {
			return value
		}
	}
	return ""
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[environmentKey(key)] = key + "=" + value
		}
	}
	for key, value := range overrides {
		values[environmentKey(key)] = key + "=" + value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func environmentKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func copyEnvironment(environment map[string]string) map[string]string {
	if len(environment) == 0 {
		return nil
	}
	result := make(map[string]string, len(environment))
	for key, value := range environment {
		result[key] = value
	}
	return result
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.truncated {
		return len(data), nil
	}
	remaining := maxCommandOutput - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.buffer.Write(data[:remaining])
		b.truncated = true
		return len(data), nil
	}
	return b.buffer.Write(data)
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}
