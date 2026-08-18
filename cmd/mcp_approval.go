package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"

	"github.com/spf13/cobra"
)

type mcpApprovalContinuation func(
	context.Context,
	string,
	[]foundry.MCPApprovalDecision,
) (*foundry.InvocationResult, error)

type hostedMCPApprovalContinuation func(
	context.Context,
	string,
	[]foundry.MCPApprovalDecision,
) (*foundry.HostedInvocationResult, error)

func resolveMCPApprovals(
	cmd *cobra.Command,
	initial *foundry.InvocationResult,
	continueInvocation mcpApprovalContinuation,
) (*foundry.InvocationResult, error) {
	if initial == nil {
		return nil, errs.Foundry("agent invocation returned no result")
	}
	approved, err := mcpApprovalAllowlist(cmd)
	if err != nil {
		return nil, err
	}
	rejectUnapproved := getBoolFlag(cmd, "reject-unapproved-mcp")
	maxRounds := getIntFlag(cmd, "max-mcp-approval-rounds")
	if maxRounds < 1 || maxRounds > 20 {
		return nil, errs.Config("--max-mcp-approval-rounds must be between 1 and 20")
	}
	result := initial
	var allDecisions []foundry.MCPApprovalDecision
	for round := 1; len(result.ApprovalRequests) > 0; round++ {
		if round > maxRounds {
			return nil, errs.Conflict(
				"MCP approval continuation exceeded %d rounds",
				maxRounds,
			)
		}
		decisions, err := buildMCPApprovalDecisions(
			result.ApprovalRequests,
			approved,
			rejectUnapproved,
		)
		if err != nil {
			return nil, err
		}
		next, err := continueInvocation(
			commandContext(cmd),
			result.ID,
			decisions,
		)
		if err != nil {
			return nil, err
		}
		allDecisions = append(allDecisions, decisions...)
		result = next
		result.ApprovalRounds = round
		result.ApprovalDecisions = append(
			[]foundry.MCPApprovalDecision(nil),
			allDecisions...,
		)
	}
	return result, nil
}

func resolveHostedMCPApprovals(
	cmd *cobra.Command,
	initial *foundry.HostedInvocationResult,
	continueInvocation hostedMCPApprovalContinuation,
) (*foundry.HostedInvocationResult, error) {
	if initial == nil {
		return nil, errs.Foundry("Hosted Agent invocation returned no result")
	}
	approved, err := mcpApprovalAllowlist(cmd)
	if err != nil {
		return nil, err
	}
	rejectUnapproved := getBoolFlag(cmd, "reject-unapproved-mcp")
	maxRounds := getIntFlag(cmd, "max-mcp-approval-rounds")
	if maxRounds < 1 || maxRounds > 20 {
		return nil, errs.Config("--max-mcp-approval-rounds must be between 1 and 20")
	}
	result := initial
	var allDecisions []foundry.MCPApprovalDecision
	for round := 1; len(result.ApprovalRequests) > 0; round++ {
		if round > maxRounds {
			return nil, errs.Conflict(
				"MCP approval continuation exceeded %d rounds",
				maxRounds,
			)
		}
		decisions, err := buildMCPApprovalDecisions(
			result.ApprovalRequests,
			approved,
			rejectUnapproved,
		)
		if err != nil {
			return nil, err
		}
		next, err := continueInvocation(
			commandContext(cmd),
			result.ResponseID,
			decisions,
		)
		if err != nil {
			return nil, err
		}
		allDecisions = append(allDecisions, decisions...)
		result = next
		result.ApprovalRounds = round
		result.ApprovalDecisions = append(
			[]foundry.MCPApprovalDecision(nil),
			allDecisions...,
		)
	}
	return result, nil
}

func buildMCPApprovalDecisions(
	requests []foundry.MCPApprovalRequest,
	approved map[string]struct{},
	rejectUnapproved bool,
) ([]foundry.MCPApprovalDecision, error) {
	decisions := make([]foundry.MCPApprovalDecision, 0, len(requests))
	var pending []string
	for _, request := range requests {
		reference := mcpApprovalReference(request.ServerLabel, request.ToolName)
		_, approve := approved[reference]
		if !approve && !rejectUnapproved {
			pending = append(
				pending,
				fmt.Sprintf(
					"%s arguments=%s",
					reference,
					formatMCPApprovalArguments(request.Arguments),
				),
			)
			continue
		}
		decisions = append(decisions, foundry.MCPApprovalDecision{
			ApprovalRequestID: request.ID,
			ServerLabel:       request.ServerLabel,
			ToolName:          request.ToolName,
			Approve:           approve,
		})
	}
	if len(pending) > 0 {
		sort.Strings(pending)
		return nil, errs.Security(
			"MCP tool approval is required; review the request and rerun with --approve-mcp-tool <server>/<tool>, or explicitly reject unmatched calls with --reject-unapproved-mcp: %s",
			strings.Join(pending, "; "),
		)
	}
	return decisions, nil
}

func mcpApprovalAllowlist(cmd *cobra.Command) (map[string]struct{}, error) {
	values := getStringArrayFlag(cmd, "approve-mcp-tool")
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		parts := strings.Split(value, "/")
		if len(parts) != 2 ||
			strings.TrimSpace(parts[0]) == "" ||
			strings.TrimSpace(parts[1]) == "" ||
			strings.ContainsAny(value, "\r\n\x00") {
			return nil, errs.Config(
				"--approve-mcp-tool %q must use the exact <server_label>/<tool_name> form",
				value,
			)
		}
		reference := mcpApprovalReference(parts[0], parts[1])
		if _, found := result[reference]; found {
			return nil, errs.Config("--approve-mcp-tool %q was provided more than once", value)
		}
		result[reference] = struct{}{}
	}
	return result, nil
}

func mcpApprovalReference(serverLabel, toolName string) string {
	return strings.TrimSpace(serverLabel) + "/" + strings.TrimSpace(toolName)
}

func formatMCPApprovalArguments(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "<unavailable>"
	}
	text := string(data)
	if len(text) > 2048 {
		return text[:2048] + "...[truncated]"
	}
	return text
}
