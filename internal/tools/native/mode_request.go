package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

type modeRequestArgs struct {
	Mode string `json:"mode"`
}

// modeRequestTool builds the mode.request native tool. The agent calls it
// (from default mode) to ask the user to switch to an editing mode. The
// handler posts a PendingToolCall with a "mode-elevation:" reason prefix
// so the TUI and ACP PermissionBridge can distinguish it from a normal
// approval. It blocks on the response channel. When approved, the user's
// chosen mode name arrives in UserApprovalDecision.Edited; the handler
// returns a result telling the agent which mode was granted. When denied,
// the result tells the agent to describe its changes instead.
//
// The handler does NOT apply the mode switch itself — the transport (TUI
// or ACP) that responds is responsible for calling SetApprovalMode and
// persisting the change. This keeps the tool side-effect-free at the
// policy level (RiskReadOnly) and lets the transport own the UX.
func (t *toolSet) modeRequestTool() registry.Tool {
	tool := registry.Tool{
		Name:        "mode.request",
		Description: "Request the user to switch from default mode to an editing mode (edit, copilot, or auto). Use this when you need to modify files but are in default mode.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string","description":"The editing intent, e.g. \"edit\""}},"required":["mode"]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[modeRequestArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if strings.TrimSpace(args.Mode) == "" {
			return registry.ToolResult{}, fmt.Errorf("mode is required")
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("session state not available")
		}

		ch := make(chan session.UserApprovalDecision, 1)
		pending := &session.PendingToolCall{
			ID:           fmt.Sprintf("mode_req_%d", time.Now().UnixNano()),
			Name:         "mode.request",
			Args:         string(call.Args),
			Reason:       "mode-elevation: agent requests an editing mode",
			Schema:       tool.Description,
			ResponseChan: ch,
		}
		t.sessionState.SetPendingApproval(pending)

		select {
		case decision := <-ch:
			t.sessionState.SetPendingApproval(nil)
			if decision.Approved {
				chosen := decision.Edited
				if chosen == "" {
					chosen = "edit"
				}
				return registry.ToolResult{
					Summary: fmt.Sprintf("approved — switched to %s mode", chosen),
					Content: fmt.Sprintf("mode.request result: approved — switched to %s mode. You may now make file changes.", chosen),
				}, nil
			}
			return registry.ToolResult{
				Summary: "denied — staying in default mode",
				Content: "mode.request result: denied — staying in default mode; describe your proposed changes instead.",
			}, nil
		case <-ctx.Done():
			t.sessionState.SetPendingApproval(nil)
			return registry.ToolResult{}, ctx.Err()
		}
	}
	return tool
}
