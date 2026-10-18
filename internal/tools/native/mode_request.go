package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

type modeRequestArgs struct {
	Mode   *string `json:"mode,omitempty"`
	System bool    `json:"system,omitempty"`
	Reason string  `json:"reason,omitempty"`
}

func accessToolResult(result session.AccessResult) (registry.ToolResult, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return registry.ToolResult{}, err
	}
	return registry.ToolResult{
		Summary: fmt.Sprintf("%s — mode %s, system access %t",
			result.Status, result.Effective.Mode, result.Effective.System),
		Content: string(body),
	}, nil
}

// The runtime commits access before delivering a typed result. This tool
// never treats an untyped Approved/Edited response as a capability grant.
func (t *toolSet) modeRequestTool() registry.Tool {
	tool := registry.Tool{
		Name:        "mode.request",
		Description: "Request missing editing capability and/or system access. A mode hint does not choose the user's mode. System access is subject to mode, policy and sandbox; no sudo.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string","enum":["edit","copilot","auto"]},"system":{"type":"boolean"},"reason":{"type":"string","maxLength":400}},"anyOf":[{"required":["mode"]},{"required":["system"],"properties":{"system":{"const":true}}}],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[modeRequestArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Mode != nil && *args.Mode != "edit" && *args.Mode != "copilot" && *args.Mode != "auto" {
			return registry.ToolResult{}, fmt.Errorf("mode must be edit, copilot, or auto")
		}
		if args.Mode == nil && !args.System {
			return registry.ToolResult{}, fmt.Errorf("request an editing mode or system access")
		}
		if utf8.RuneCountInString(args.Reason) > 400 {
			return registry.ToolResult{}, fmt.Errorf("reason exceeds 400 characters")
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("session state not available")
		}
		if t.sessionState.SubagentDepth() > 0 {
			return registry.ToolResult{}, fmt.Errorf("mode.request is top-level only; ask the parent agent to request access")
		}
		if !t.accessRequests {
			return registry.ToolResult{}, fmt.Errorf("access request resolver is not available")
		}
		reason := strings.TrimSpace(args.Reason)
		if reason == "" {
			switch {
			case args.Mode != nil && args.System:
				reason = "The agent requests editing capability and system access."
			case args.System:
				reason = "The agent requests system access."
			default:
				reason = "The agent requests editing capability."
			}
		}
		ch := make(chan session.UserApprovalDecision, 1)
		pending := &session.PendingToolCall{
			ID: call.ID, Name: tool.Name, Args: string(call.Args),
			Reason: reason, Schema: tool.Description, ResponseChan: ch,
		}
		for _, active := range t.sessionState.ActiveToolCalls() {
			if active.ToolCallID == call.ID && active.Name == tool.Name {
				pending.StepID = active.StepID
				break
			}
		}
		request, waiting, err := t.sessionState.RegisterAccess(ctx, pending, session.AccessIntent{
			Editing: args.Mode != nil, System: args.System, Reason: reason,
		})
		if err != nil {
			return registry.ToolResult{}, err
		}
		if !waiting {
			return accessToolResult(session.AccessResult{
				SessionID: request.SessionID, Status: session.AccessAvailable,
				Effective: request.Current,
			})
		}
		var decision session.UserApprovalDecision
		var ok bool
		select {
		case decision, ok = <-ch:
		case <-ctx.Done():
			// If a grant already crossed its acceptance point, CancelAccess
			// waits for that commit; the queued applied result wins.
			t.sessionState.CancelAccess(request.RequestID, session.AccessCanceled, ctx.Err().Error())
			decision, ok = <-ch
		}
		if !ok || decision.Access == nil {
			return registry.ToolResult{}, fmt.Errorf("access request ended without a committed result")
		}
		result := *decision.Access
		if result.RequestID != request.RequestID || result.SessionID != request.SessionID {
			return registry.ToolResult{}, fmt.Errorf("access result identity mismatch")
		}
		if decision.Approved != (result.Status == session.AccessGranted) {
			return registry.ToolResult{}, fmt.Errorf("access result was not committed")
		}
		switch result.Status {
		case session.AccessGranted, session.AccessDenied, session.AccessAvailable:
			return accessToolResult(result)
		case session.AccessCanceled:
			// User steering is the distinct cause here: the operation was
			// interrupted, so reporting a plain turn cancellation would blame the
			// turn for a deliberate steer and reporting a denial would claim the
			// user refused a request that was never answered.
			if session.IsSteeringCancellation(ctx, ctx.Err()) {
				return registry.ToolResult{}, session.ErrSendNow
			}
			if ctx.Err() != nil {
				return registry.ToolResult{}, ctx.Err()
			}
			return registry.ToolResult{}, fmt.Errorf("access request canceled: %s", result.Message)
		default:
			return registry.ToolResult{}, fmt.Errorf("access request failed: %s", result.Message)
		}
	}
	return tool
}
