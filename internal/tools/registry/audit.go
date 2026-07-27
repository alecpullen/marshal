package registry

import (
	"encoding/json"
	"time"
)

type ApprovalState string

const (
	ApprovalNotRequired ApprovalState = "not_required"
	ApprovalPending     ApprovalState = "pending"
	ApprovalApproved    ApprovalState = "approved"
	ApprovalDenied      ApprovalState = "denied"
)

type AuditEvent struct {
	Timestamp       time.Time
	AgentRole       string
	Model           string
	ToolName        string
	Args            json.RawMessage
	Risk            RiskLevel
	Approval        ApprovalState
	ResultSummary   string
	ResultContent   string
	FilesChanged    []string
	CommandExitCode *int
	Error           string
	Sandbox         SandboxMeta
	Hooks           []HookMetadata
	// Duration is how long the tool call took. Zero for synthesised
	// events (rollback, cancellation) that did not run a tool.
	Duration time.Duration
	// OriginalArgs holds the user-approved args before any pre_tool_use hook
	// rewrite. Nil when no rewrite occurred.
	OriginalArgs json.RawMessage
	// Rewritten is true when a pre_tool_use hook rewrote the tool arguments
	// after user approval.
	Rewritten bool
}

// HookMetadata captures the per-tool audit trail for F20 lifecycle hooks.
// One entry per matched hook command (or per rewrite iteration); the TUI
// surfaces only the highest-signal decision from this slice.
type HookMetadata struct {
	Event      string `json:"event"`
	Command    string `json:"command,omitempty"`
	Decision   string `json:"decision,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Rewrote    bool   `json:"rewrote,omitempty"`
	FailedOpen bool   `json:"failed_open,omitempty"`
}

func NewAuditEvent(now time.Time, tool Tool, call ToolCall, result ToolResult, approval ApprovalState, err error) AuditEvent {
	event := AuditEvent{
		Timestamp:     now,
		ToolName:      call.Name,
		Args:          append(json.RawMessage(nil), call.Args...),
		Risk:          tool.Risk,
		Approval:      approval,
		ResultSummary: result.Summary,
		ResultContent: result.Content,
		FilesChanged:  append([]string(nil), result.FilesChanged...),
		Sandbox:       result.Sandbox,
	}
	if event.ToolName == "" {
		event.ToolName = tool.Name
	}
	if result.CommandExitCode != nil {
		exitCode := *result.CommandExitCode
		event.CommandExitCode = &exitCode
	}
	if err != nil {
		event.Error = err.Error()
	}
	return event
}
