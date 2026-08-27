package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type hostedSmokeResult struct {
	Preview     bool                            `json:"preview" yaml:"preview"`
	Cloud       string                          `json:"cloud" yaml:"cloud"`
	Environment string                          `json:"environment,omitempty" yaml:"environment,omitempty"`
	AgentName   string                          `json:"agentName" yaml:"agentName"`
	Protocol    string                          `json:"protocol" yaml:"protocol"`
	Invocation  *foundry.HostedInvocationResult `json:"invocation" yaml:"invocation"`
}

type hostedSessionsResult struct {
	Preview     bool                    `json:"preview" yaml:"preview"`
	Cloud       string                  `json:"cloud" yaml:"cloud"`
	Environment string                  `json:"environment,omitempty" yaml:"environment,omitempty"`
	AgentName   string                  `json:"agentName" yaml:"agentName"`
	Sessions    []foundry.HostedSession `json:"sessions,omitempty" yaml:"sessions,omitempty"`
	Session     *foundry.HostedSession  `json:"session,omitempty" yaml:"session,omitempty"`
	Changed     bool                    `json:"changed,omitempty" yaml:"changed,omitempty"`
	DryRun      bool                    `json:"dryRun,omitempty" yaml:"dryRun,omitempty"`
	Receipt     string                  `json:"receipt,omitempty" yaml:"receipt,omitempty"`
}

type hostedSessionFilesResult struct {
	Preview     bool                        `json:"preview" yaml:"preview"`
	Cloud       string                      `json:"cloud" yaml:"cloud"`
	Environment string                      `json:"environment,omitempty" yaml:"environment,omitempty"`
	AgentName   string                      `json:"agentName" yaml:"agentName"`
	SessionID   string                      `json:"sessionId" yaml:"sessionId"`
	RemotePath  string                      `json:"remotePath,omitempty" yaml:"remotePath,omitempty"`
	LocalPath   string                      `json:"localPath,omitempty" yaml:"localPath,omitempty"`
	Bytes       int64                       `json:"bytes,omitempty" yaml:"bytes,omitempty"`
	Files       []foundry.HostedSessionFile `json:"files,omitempty" yaml:"files,omitempty"`
	Changed     bool                        `json:"changed,omitempty" yaml:"changed,omitempty"`
	DryRun      bool                        `json:"dryRun,omitempty" yaml:"dryRun,omitempty"`
	Receipt     string                      `json:"receipt,omitempty" yaml:"receipt,omitempty"`
}

type hostedLogsResult struct {
	Preview      bool                    `json:"preview" yaml:"preview"`
	Cloud        string                  `json:"cloud" yaml:"cloud"`
	Environment  string                  `json:"environment,omitempty" yaml:"environment,omitempty"`
	AgentName    string                  `json:"agentName" yaml:"agentName"`
	AgentVersion string                  `json:"agentVersion" yaml:"agentVersion"`
	SessionID    string                  `json:"sessionId" yaml:"sessionId"`
	Logs         foundry.HostedLogStream `json:"logs" yaml:"logs"`
}

func cmdHostedSmoke(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	protocol, err := hostedSmokeProtocol(runtime, getFlag(cmd, "protocol"))
	if err != nil {
		return err
	}
	var body []byte
	switch protocol {
	case "responses":
		if getFlag(cmd, "input") != "" || getFlag(cmd, "input-file") != "" {
			return errs.Config("responses smoke uses --prompt; --input and --input-file are for invocations")
		}
		prompt := getFlag(cmd, "prompt")
		if strings.TrimSpace(prompt) == "" {
			return errs.Config("--prompt is required for responses smoke")
		}
		request := map[string]interface{}{
			"input":  prompt,
			"stream": false,
		}
		if sessionID := getFlag(cmd, "session-id"); sessionID != "" {
			request["agent_session_id"] = sessionID
		}
		if previous := getFlag(cmd, "previous-response-id"); previous != "" {
			request["previous_response_id"] = previous
		}
		if conversation := getFlag(cmd, "conversation-id"); conversation != "" {
			request["conversation"] = conversation
		}
		body, err = json.Marshal(request)
		if err != nil {
			return errs.Config("failed to encode responses smoke request: %v", err)
		}
	case "invocations":
		if getFlag(cmd, "prompt") != "" ||
			getFlag(cmd, "previous-response-id") != "" ||
			getFlag(cmd, "conversation-id") != "" {
			return errs.Config(
				"invocations smoke requires raw JSON from exactly one of --input or --input-file",
			)
		}
		input, inputFile := getFlag(cmd, "input"), getFlag(cmd, "input-file")
		if (input == "") == (inputFile == "") {
			return errs.Config("invocations smoke requires exactly one of --input or --input-file")
		}
		if inputFile != "" {
			body, err = netcheck.ReadContainedFile(
				runtime.Workspace.Root,
				inputFile,
				"--input-file",
			)
			if err != nil {
				return err
			}
		} else {
			body = []byte(input)
		}
		if !json.Valid(body) {
			return errs.Config("invocations smoke input must be valid JSON; payload shape is defined by the agent")
		}
	}
	invocation, err := runtime.Client.InvokeHostedContext(
		runtime.Context,
		runtime.Agent.Name,
		protocol,
		body,
		"application/json",
		getFlag(cmd, "session-id"),
		getFlag(cmd, "isolation-key"),
	)
	if err != nil {
		return err
	}
	if protocol == "responses" {
		sessionID := getFlag(cmd, "session-id")
		if sessionID == "" {
			sessionID = invocation.SessionID
		}
		invocation, err = resolveHostedMCPApprovals(
			cmd,
			invocation,
			func(
				ctx context.Context,
				previousResponseID string,
				decisions []foundry.MCPApprovalDecision,
			) (*foundry.HostedInvocationResult, error) {
				return runtime.Client.ContinueHostedApprovalsContext(
					ctx,
					runtime.Agent.Name,
					previousResponseID,
					decisions,
					sessionID,
					getFlag(cmd, "isolation-key"),
				)
			},
		)
		if err != nil {
			return err
		}
		if err := requireHostedResponsesSuccess(invocation); err != nil {
			return err
		}
	}
	result := hostedSmokeResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		Protocol:    protocol,
		Invocation:  invocation,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent smoke succeeded: name=%s protocol=%s session=%s response=%s",
		runtime.Agent.Name,
		protocol,
		emptyValue(invocation.SessionID),
		emptyValue(invocation.ResponseID),
	))
}

func requireHostedResponsesSuccess(invocation *foundry.HostedInvocationResult) error {
	if invocation == nil {
		return errs.Foundry("Hosted Agent invocation returned no result")
	}
	body, ok := invocation.Body.(map[string]interface{})
	if !ok {
		return errs.Foundry("Hosted Agent invocation returned an unsupported response body")
	}
	status, _ := body["status"].(string)
	if strings.EqualFold(strings.TrimSpace(status), "completed") {
		return nil
	}
	message := ""
	if serviceError, ok := body["error"].(map[string]interface{}); ok {
		code, _ := serviceError["code"].(string)
		detail, _ := serviceError["message"].(string)
		switch {
		case code != "" && detail != "":
			message = fmt.Sprintf("%s: %s", code, detail)
		case detail != "":
			message = detail
		case code != "":
			message = code
		}
	}
	if message == "" {
		message = "the response did not complete successfully"
	}
	return errs.Foundry(
		"Hosted Agent invocation returned status %q: %s",
		status,
		message,
	)
}

func hostedSmokeProtocol(runtime *hostedRESTRuntime, requested string) (string, error) {
	available := make(map[string]bool, len(runtime.Workspace.Selected.Protocols))
	for _, protocol := range runtime.Workspace.Selected.Protocols {
		available[protocol.Name] = true
	}
	if requested != "" {
		if requested == "invocations_ws" {
			return "", errs.Config(
				"invocations_ws uses a developer-defined WebSocket framing contract and is not supported by fam hosted smoke",
			)
		}
		if requested != "responses" && requested != "invocations" {
			return "", errs.Config("--protocol must be responses or invocations")
		}
		if !available[requested] {
			return "", errs.Config(
				"Hosted Agent service %q does not declare protocol %s",
				runtime.Workspace.Selected.ServiceName,
				requested,
			)
		}
		return requested, nil
	}
	switch {
	case available["responses"]:
		return "responses", nil
	case available["invocations"]:
		return "invocations", nil
	case available["invocations_ws"]:
		return "", errs.Config(
			"this Hosted Agent exposes only invocations_ws; use a WebSocket client that implements the agent's framing contract",
		)
	default:
		return "", errs.Config("this Hosted Agent does not expose responses or invocations")
	}
}

func cmdHostedSessionCreate(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	version := getFlag(cmd, "agent-version")
	if version != "" {
		found, err := requireHostedVersion(runtime.Context, runtime, version)
		if err != nil {
			return err
		}
		if !strings.EqualFold(found.Status, "active") {
			return errs.Config(
				"Hosted Agent %q version %s is %s, not active",
				runtime.Agent.Name,
				version,
				found.Status,
			)
		}
	}
	store, err := newHostedOperationStore(cmd, runtime, "hosted-session-create", "")
	if err != nil {
		return err
	}
	session, mutationErr := runtime.Client.CreateHostedSessionContext(
		runtime.Context,
		runtime.Agent.Name,
		version,
		getFlag(cmd, "isolation-key"),
	)
	if mutationErr != nil {
		_ = store.Complete("unknown", mutationErr)
		return releaseFailure(store.Path, mutationErr)
	}
	_ = store.AddResource(receipt.ResourceChange{
		Kind:   "hosted-session",
		Name:   session.ID(),
		Action: "create",
		Status: session.Status,
	})
	store.Receipt.Agent.Changed = false
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result := hostedSessionsResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		Session:     session,
		Changed:     true,
		Receipt:     store.Path,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent session created: name=%s session=%s status=%s",
		runtime.Agent.Name,
		session.ID(),
		session.Status,
	))
}

func cmdHostedSessionList(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	sessions, err := runtime.Client.ListHostedSessionsContext(
		runtime.Context,
		runtime.Agent.Name,
		getFlag(cmd, "isolation-key"),
	)
	if err != nil {
		return err
	}
	result := hostedSessionsResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		Sessions:    sessions,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent sessions: name=%s count=%d",
		runtime.Agent.Name,
		len(sessions),
	))
}

func cmdHostedSessionShow(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	sessionID := getFlag(cmd, "session-id")
	session, err := runtime.Client.GetHostedSessionContext(
		runtime.Context,
		runtime.Agent.Name,
		sessionID,
		getFlag(cmd, "isolation-key"),
	)
	if err != nil {
		return err
	}
	if session == nil {
		return errs.NotFound(
			"Hosted Agent %q session %q was not found",
			runtime.Agent.Name,
			sessionID,
		)
	}
	result := hostedSessionsResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		Session:     session,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent session: name=%s session=%s status=%s",
		runtime.Agent.Name,
		session.ID(),
		session.Status,
	))
}

func cmdHostedSessionStop(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	sessionID := getFlag(cmd, "session-id")
	isolationKey := getFlag(cmd, "isolation-key")
	before, err := runtime.Client.GetHostedSessionContext(
		runtime.Context,
		runtime.Agent.Name,
		sessionID,
		isolationKey,
	)
	if err != nil {
		return err
	}
	if before == nil {
		return errs.NotFound(
			"Hosted Agent %q session %q was not found",
			runtime.Agent.Name,
			sessionID,
		)
	}
	if hostedSessionStopped(before.Status) {
		result := hostedSessionsResult{
			Preview:     true,
			Cloud:       runtime.Profile.Name,
			Environment: runtime.Environment,
			AgentName:   runtime.Agent.Name,
			Session:     before,
		}
		return printResult(cmd, result, fmt.Sprintf(
			"Hosted Agent session already stopped: name=%s session=%s",
			runtime.Agent.Name,
			sessionID,
		))
	}
	store, err := newHostedOperationStore(cmd, runtime, "hosted-session-stop", "")
	if err != nil {
		return err
	}
	mutationErr := runtime.Client.StopHostedSessionContext(
		runtime.Context,
		runtime.Agent.Name,
		sessionID,
		isolationKey,
	)
	after, verifyErr := runtime.Client.GetHostedSessionContext(
		runtime.Context,
		runtime.Agent.Name,
		sessionID,
		isolationKey,
	)
	reconciled := mutationErr != nil
	if verifyErr != nil || after == nil || !hostedSessionStopped(after.Status) {
		if mutationErr != nil {
			_ = store.Complete("unknown", mutationErr)
			return releaseFailure(store.Path, mutationErr)
		}
		if verifyErr != nil {
			_ = store.Complete("unknown", verifyErr)
			return releaseFailure(store.Path, errs.AmbiguousMutation(verifyErr))
		}
		verificationErr := errs.Foundry(
			"Hosted Agent %q session %q did not verify as stopped",
			runtime.Agent.Name,
			sessionID,
		)
		_ = store.Complete("unknown", verificationErr)
		return releaseFailure(store.Path, verificationErr)
	}
	_ = store.AddResource(receipt.ResourceChange{
		Kind:           "hosted-session",
		Name:           sessionID,
		Action:         "stop",
		Status:         after.Status,
		Reconciliation: fmt.Sprintf("reconciled=%t", reconciled),
	})
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result := hostedSessionsResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		Session:     after,
		Changed:     true,
		Receipt:     store.Path,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent session stopped: name=%s session=%s reconciled=%t",
		runtime.Agent.Name,
		sessionID,
		reconciled,
	))
}

func hostedSessionStopped(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "idle", "stopped":
		return true
	default:
		return false
	}
}

func cmdHostedSessionDelete(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	sessionID := getFlag(cmd, "session-id")
	isolationKey := getFlag(cmd, "isolation-key")
	before, err := runtime.Client.GetHostedSessionContext(
		runtime.Context,
		runtime.Agent.Name,
		sessionID,
		isolationKey,
	)
	if err != nil {
		return err
	}
	dryRun := getBoolFlag(cmd, "dry-run")
	result := hostedSessionsResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		Session:     before,
		Changed:     before != nil,
		DryRun:      dryRun,
	}
	if before == nil {
		return printResult(cmd, result, fmt.Sprintf(
			"Hosted Agent session %q not found (nothing to delete)",
			sessionID,
		))
	}
	if dryRun {
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would delete Hosted Agent session %s",
			sessionID,
		))
	}
	if err := confirmDestructive(
		cmd,
		fmt.Sprintf("Delete Hosted Agent %q session %q and its persisted files?", runtime.Agent.Name, sessionID),
	); err != nil {
		return err
	}
	store, err := newHostedOperationStore(cmd, runtime, "hosted-session-delete", "")
	if err != nil {
		return err
	}
	_, mutationErr := runtime.Client.DeleteHostedSessionContext(
		runtime.Context,
		runtime.Agent.Name,
		sessionID,
		isolationKey,
	)
	after, verifyErr := runtime.Client.GetHostedSessionContext(
		runtime.Context,
		runtime.Agent.Name,
		sessionID,
		isolationKey,
	)
	if verifyErr != nil || after != nil {
		if mutationErr != nil {
			_ = store.Complete("unknown", mutationErr)
			return releaseFailure(store.Path, mutationErr)
		}
		if verifyErr != nil {
			ambiguous := errs.AmbiguousMutation(verifyErr)
			_ = store.Complete("unknown", ambiguous)
			return releaseFailure(store.Path, ambiguous)
		}
		ambiguous := errs.AmbiguousMutation(errs.Foundry(
			"Hosted Agent %q session %q still exists after delete",
			runtime.Agent.Name,
			sessionID,
		))
		_ = store.Complete("unknown", ambiguous)
		return releaseFailure(store.Path, ambiguous)
	}
	_ = store.AddResource(receipt.ResourceChange{
		Kind:   "hosted-session",
		Name:   sessionID,
		Action: "delete",
		Status: "deleted",
	})
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result.Receipt = store.Path
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent session deleted: name=%s session=%s",
		runtime.Agent.Name,
		sessionID,
	))
}

func cmdHostedSessionFileUpload(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	sessionID := getFlag(cmd, "session-id")
	localPath := getFlag(cmd, "file")
	remotePath := getFlag(cmd, "remote-path")
	if remotePath == "" {
		remotePath = filepath.Base(filepath.Clean(localPath))
	}
	remotePath, err = validateHostedRemotePath(remotePath, false)
	if err != nil {
		return err
	}
	file, info, err := netcheck.OpenContainedFile(
		runtime.Workspace.Root,
		localPath,
		"--file",
		foundry.MaxHostedSessionFileBytes,
	)
	if err != nil {
		return err
	}
	defer file.Close()
	store, err := newHostedOperationStore(cmd, runtime, "hosted-session-file-upload", "")
	if err != nil {
		return err
	}
	if err := runtime.Client.UploadHostedSessionFileContext(
		runtime.Context,
		runtime.Agent.Name,
		sessionID,
		remotePath,
		file,
		info.Size(),
		getFlag(cmd, "isolation-key"),
	); err != nil {
		_ = store.Complete("unknown", err)
		return releaseFailure(store.Path, err)
	}
	_ = store.AddResource(receipt.ResourceChange{
		Kind:   "hosted-session-file",
		Name:   remotePath,
		Action: "upload",
		Status: "uploaded",
	})
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result := hostedSessionFilesResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		SessionID:   sessionID,
		RemotePath:  remotePath,
		LocalPath:   localPath,
		Bytes:       info.Size(),
		Changed:     true,
		Receipt:     store.Path,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted session file uploaded: session=%s path=%s bytes=%d",
		sessionID,
		remotePath,
		info.Size(),
	))
}

func cmdHostedSessionFileList(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	remotePath, err := validateHostedRemotePath(getFlag(cmd, "remote-path"), true)
	if err != nil {
		return err
	}
	files, err := runtime.Client.ListHostedSessionFilesContext(
		runtime.Context,
		runtime.Agent.Name,
		getFlag(cmd, "session-id"),
		remotePath,
		getFlag(cmd, "isolation-key"),
	)
	if err != nil {
		return err
	}
	result := hostedSessionFilesResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		SessionID:   getFlag(cmd, "session-id"),
		RemotePath:  remotePath,
		Files:       files,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted session files: session=%s path=%s count=%d",
		result.SessionID,
		remotePath,
		len(files),
	))
}

func cmdHostedSessionFileDownload(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	remotePath, err := validateHostedRemotePath(getFlag(cmd, "remote-path"), false)
	if err != nil {
		return err
	}
	maxBytes := getInt64Flag(cmd, "max-bytes")
	var buffer bytes.Buffer
	written, err := runtime.Client.DownloadHostedSessionFileContext(
		runtime.Context,
		runtime.Agent.Name,
		getFlag(cmd, "session-id"),
		remotePath,
		&buffer,
		maxBytes,
		getFlag(cmd, "isolation-key"),
	)
	if err != nil {
		return err
	}
	output, err := netcheck.WriteContainedFileExclusive(
		runtime.Workspace.Root,
		getFlag(cmd, "output-file"),
		"--output-file",
		buffer.Bytes(),
		maxBytes,
	)
	if err != nil {
		return err
	}
	result := hostedSessionFilesResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		SessionID:   getFlag(cmd, "session-id"),
		RemotePath:  remotePath,
		LocalPath:   output,
		Bytes:       written,
		Changed:     true,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted session file downloaded: session=%s path=%s output=%s bytes=%d",
		result.SessionID,
		remotePath,
		output,
		written,
	))
}

func cmdHostedSessionFileDelete(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	remotePath, err := validateHostedRemotePath(getFlag(cmd, "remote-path"), false)
	if err != nil {
		return err
	}
	result := hostedSessionFilesResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		SessionID:   getFlag(cmd, "session-id"),
		RemotePath:  remotePath,
		Changed:     true,
		DryRun:      getBoolFlag(cmd, "dry-run"),
	}
	if result.DryRun {
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would delete Hosted session file %s from session %s",
			remotePath,
			result.SessionID,
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Delete Hosted session file %q from session %q?",
		remotePath,
		result.SessionID,
	)); err != nil {
		return err
	}
	store, err := newHostedOperationStore(cmd, runtime, "hosted-session-file-delete", "")
	if err != nil {
		return err
	}
	if err := runtime.Client.DeleteHostedSessionFileContext(
		runtime.Context,
		runtime.Agent.Name,
		result.SessionID,
		remotePath,
		getFlag(cmd, "isolation-key"),
	); err != nil {
		_ = store.Complete("unknown", err)
		return releaseFailure(store.Path, err)
	}
	_ = store.AddResource(receipt.ResourceChange{
		Kind:   "hosted-session-file",
		Name:   remotePath,
		Action: "delete",
		Status: "deleted",
	})
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result.Receipt = store.Path
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted session file deleted: session=%s path=%s",
		result.SessionID,
		remotePath,
	))
}

func cmdHostedLogs(cmd *cobra.Command, _ []string) error {
	duration := getDurationFlag(cmd, "duration")
	requestTimeout := getDurationFlag(cmd, "request-timeout")
	if duration >= requestTimeout {
		return errs.Config(
			"--request-timeout (%s) must be greater than --duration (%s) for Hosted log streaming",
			requestTimeout,
			duration,
		)
	}
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	version := getFlag(cmd, "agent-version")
	if _, err := requireHostedVersion(runtime.Context, runtime, version); err != nil {
		return err
	}
	logs, err := runtime.Client.StreamHostedLogsContext(
		runtime.Context,
		runtime.Agent.Name,
		version,
		getFlag(cmd, "session-id"),
		getIntFlag(cmd, "max-lines"),
		getInt64Flag(cmd, "max-bytes"),
		duration,
	)
	if err != nil {
		return err
	}
	result := hostedLogsResult{
		Preview:      true,
		Cloud:        runtime.Profile.Name,
		Environment:  runtime.Environment,
		AgentName:    runtime.Agent.Name,
		AgentVersion: version,
		SessionID:    getFlag(cmd, "session-id"),
		Logs:         logs,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent logs: name=%s version=%s session=%s events=%d truncated=%t timed-out=%t",
		runtime.Agent.Name,
		version,
		result.SessionID,
		len(logs.Events),
		logs.Truncated,
		logs.TimedOut,
	))
}

func validateHostedRemotePath(value string, allowRoot bool) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" && allowRoot {
		return ".", nil
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') {
		return "", errs.Config("Hosted session file path must be a non-empty relative path")
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		if allowRoot {
			return cleaned, nil
		}
		return "", errs.Config("Hosted session file path must identify a file")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, ":") {
		return "", errs.Security("Hosted session file path %q escapes the session file root", value)
	}
	return cleaned, nil
}
