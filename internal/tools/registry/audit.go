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
	FilesChanged    []string
	CommandExitCode *int
	Error           string
}

func NewAuditEvent(now time.Time, tool Tool, call ToolCall, result ToolResult, approval ApprovalState, err error) AuditEvent {
	event := AuditEvent{
		Timestamp:     now,
		ToolName:      call.Name,
		Args:          append(json.RawMessage(nil), call.Args...),
		Risk:          tool.Risk,
		Approval:      approval,
		ResultSummary: result.Summary,
		FilesChanged:  append([]string(nil), result.FilesChanged...),
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
