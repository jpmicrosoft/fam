package hostedautopilot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	RedactedValue          = "[REDACTED]"
	AdminApprovalStatement = "Administrator approval remains an external manual step."
)

var (
	ErrCommandRunnerRequired = errors.New("CommandRunner is required")
	ErrIsolatedDirectory     = errors.New("isolated directory must be an existing empty non-symlink directory")
	ErrCommandFailed         = errors.New("command failed")
	ErrCheckedOutCommit      = errors.New("checked-out commit does not match the reviewed sample commit")
	ErrSamplePathMissing     = errors.New("reviewed sample subdirectory is missing")
	ErrInvalidEnvironment    = errors.New("allow-listed environment is invalid")
)

var fullCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type OutputPolicy uint8

const (
	DiscardOutput OutputPolicy = iota
	CaptureStdout
)

type Command struct {
	Executable   string
	Args         []string
	Dir          string
	Environment  map[string]string
	OutputPolicy OutputPolicy
}

type CommandExecution struct {
	ExitCode int
	Stdout   string
}

// CommandRunner implementations must execute Command directly without a shell,
// honor OutputPolicy, and avoid logging command environment values or output.
type CommandRunner interface {
	Run(ctx context.Context, command Command) (CommandExecution, error)
}

type RunRequest struct {
	Preflight   PreflightOptions
	IsolatedDir string
	AllowedEnv  map[string]string
	Runner      CommandRunner
}

type CommandRecord struct {
	Executable  string
	Args        []string
	Directory   string
	Environment map[string]string
	ExitCode    int
}

type Result struct {
	RepositoryURL                string
	CommitSHA                    string
	SamplePath                   string
	Commands                     []CommandRecord
	AdminApprovalRemainsExternal bool
	AdminApproval                string
}

func Run(ctx context.Context, request RunRequest) (Result, error) {
	result := Result{
		RepositoryURL:                OfficialRepositoryURL,
		CommitSHA:                    ReviewedSampleCommit,
		SamplePath:                   SamplePath,
		AdminApprovalRemainsExternal: true,
		AdminApproval:                AdminApprovalStatement,
	}

	preflight, err := Preflight(request.Preflight)
	if err != nil {
		return result, err
	}
	if request.Runner == nil {
		return result, ErrCommandRunnerRequired
	}

	isolatedDir, err := validateIsolatedDirectory(request.IsolatedDir)
	if err != nil {
		return result, err
	}
	allowedEnv, err := validateAllowedEnvironment(request.AllowedEnv)
	if err != nil {
		return result, err
	}

	git := preflight.ResolvedExecutables["git"]
	gitCommands := []Command{
		{
			Executable: git,
			Args:       []string{"init", "--quiet"},
			Dir:        isolatedDir,
		},
		{
			Executable: git,
			Args:       []string{"remote", "add", "origin", OfficialRepositoryURL},
			Dir:        isolatedDir,
		},
		{
			Executable: git,
			Args:       []string{"fetch", "--quiet", "--depth=1", "--no-tags", "origin", ReviewedSampleCommit},
			Dir:        isolatedDir,
		},
		{
			Executable: git,
			Args:       []string{"checkout", "--quiet", "--detach", ReviewedSampleCommit},
			Dir:        isolatedDir,
		},
	}
	for _, command := range gitCommands {
		if _, err := runAndRecord(ctx, request.Runner, command, allowedEnv, &result); err != nil {
			return result, err
		}
	}

	headExecution, err := runAndRecord(ctx, request.Runner, Command{
		Executable:   git,
		Args:         []string{"rev-parse", "HEAD"},
		Dir:          isolatedDir,
		OutputPolicy: CaptureStdout,
	}, allowedEnv, &result)
	if err != nil {
		return result, err
	}

	head := strings.TrimSpace(headExecution.Stdout)
	if !fullCommitSHA.MatchString(head) {
		return result, fmt.Errorf("%w: git returned an invalid full commit SHA", ErrCheckedOutCommit)
	}
	if head != ReviewedSampleCommit {
		return result, fmt.Errorf(
			"%w: expected %s, got %s",
			ErrCheckedOutCommit,
			ReviewedSampleCommit,
			redactText(head, allowedEnv),
		)
	}

	sampleDir := filepath.Join(isolatedDir, filepath.FromSlash(SamplePath))
	sampleInfo, err := os.Lstat(sampleDir)
	if err != nil || !sampleInfo.IsDir() || sampleInfo.Mode()&os.ModeSymlink != 0 {
		return result, ErrSamplePathMissing
	}

	azd := preflight.ResolvedExecutables["azd"]
	workflow := []Command{
		{
			Executable:  azd,
			Args:        []string{"provision"},
			Dir:         sampleDir,
			Environment: copyEnvironment(allowedEnv),
		},
		{
			Executable:  azd,
			Args:        []string{"env", "get-values"},
			Dir:         sampleDir,
			Environment: copyEnvironment(allowedEnv),
		},
	}
	for _, command := range workflow {
		if _, err := runAndRecord(ctx, request.Runner, command, allowedEnv, &result); err != nil {
			return result, err
		}
	}

	return result, nil
}

func validateIsolatedDirectory(path string) (string, error) {
	if path == "" {
		return "", ErrIsolatedDirectory
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", ErrIsolatedDirectory
	}
	info, err := os.Lstat(absolutePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrIsolatedDirectory
	}
	entries, err := os.ReadDir(absolutePath)
	if err != nil || len(entries) != 0 {
		return "", ErrIsolatedDirectory
	}
	return filepath.Clean(absolutePath), nil
}

func validateAllowedEnvironment(environment map[string]string) (map[string]string, error) {
	validated := make(map[string]string, len(environment))
	for key, value := range environment {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, ErrInvalidEnvironment
		}
		validated[key] = value
	}
	return validated, nil
}

func runAndRecord(
	ctx context.Context,
	runner CommandRunner,
	command Command,
	allowedEnv map[string]string,
	result *Result,
) (CommandExecution, error) {
	runnerCommand := command
	runnerCommand.Args = append([]string(nil), command.Args...)
	runnerCommand.Environment = copyEnvironment(command.Environment)
	execution, err := runner.Run(ctx, runnerCommand)
	exitCode := execution.ExitCode
	if err != nil && exitCode == 0 {
		exitCode = -1
	}

	result.Commands = append(result.Commands, CommandRecord{
		Executable:  command.Executable,
		Args:        append([]string(nil), command.Args...),
		Directory:   command.Dir,
		Environment: redactedEnvironment(command.Environment),
		ExitCode:    exitCode,
	})

	if err != nil {
		return execution, fmt.Errorf("%w: %s: %s", ErrCommandFailed, command.Executable, redactText(err.Error(), allowedEnv))
	}
	if execution.ExitCode != 0 {
		return execution, fmt.Errorf("%w: %s exited with status %d", ErrCommandFailed, command.Executable, execution.ExitCode)
	}
	return execution, nil
}

func copyEnvironment(environment map[string]string) map[string]string {
	if len(environment) == 0 {
		return nil
	}
	copied := make(map[string]string, len(environment))
	for key, value := range environment {
		copied[key] = value
	}
	return copied
}

func redactedEnvironment(environment map[string]string) map[string]string {
	if len(environment) == 0 {
		return nil
	}
	redacted := make(map[string]string, len(environment))
	for key := range environment {
		redacted[key] = RedactedValue
	}
	return redacted
}

func redactText(text string, environment map[string]string) string {
	values := make([]string, 0, len(environment))
	for _, value := range environment {
		if value != "" {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		return len(values[i]) > len(values[j])
	})
	for _, value := range values {
		text = strings.ReplaceAll(text, value, RedactedValue)
	}
	return text
}
