package acpwire

import "encoding/json"

// ErrorCode names an agent-side error the TUI branches on (spec C2).
type ErrorCode string

const (
	// CodeQuiescing means the session is quiescing for an end or a swap.
	CodeQuiescing ErrorCode = "quiescing"
	// CodeNoTurnStop means no turn-stop service is attached.
	CodeNoTurnStop ErrorCode = "no_turn_stop"
	// CodeHumanGateRequired means the action needs a person's answer.
	CodeHumanGateRequired ErrorCode = "human_gate_required"
	// CodeKeyringUnavailable means the OS keychain cannot be reached.
	CodeKeyringUnavailable ErrorCode = "keyring_unavailable"
	// CodeAuthRequired means a provider or MCP server needs a login.
	CodeAuthRequired ErrorCode = "auth_required"
	// CodeProviderError means a provider rejected the call. Data carries its
	// fields, which plan 7 defines.
	CodeProviderError ErrorCode = "provider_error"
	// CodeStaleWorkCursor means the work cursor is older than the work snapshot.
	CodeStaleWorkCursor ErrorCode = "stale_work_cursor"
	// CodeRequestSettled means the request was already answered, cancelled or settled elsewhere.
	CodeRequestSettled ErrorCode = "request_settled"
	// CodeBusy means a turn is running.
	CodeBusy ErrorCode = "busy"
	// CodeSessionClosed means a swap replaced the session (spec C4).
	CodeSessionClosed ErrorCode = "session_closed"
	// CodeNoRunner means the session has no agent runner: its provider is not built.
	CodeNoRunner ErrorCode = "no_runner"
)

// Error is an agent-side error with a code.
type Error struct {
	Code    ErrorCode       `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error returns the message, or the code when there is no message.
func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}
