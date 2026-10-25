package acpwire

// These are the JSON contract session/steer and session/steering_status
// already serve, duplicated field by field rather than aliasing a session
// type: the wire is an external client boundary, so a change to the session's
// own types must not silently change the bytes a client reads.

// SteeringState is the Send now snapshot of a session's running turn.
type SteeringState struct {
	Version       uint64 `json:"version"`
	TurnID        string `json:"turnId"`
	Active        bool   `json:"active"`
	Supported     bool   `json:"supported"`
	Held          bool   `json:"held"`
	OperationID   string `json:"operationId,omitempty"`
	OperationKind string `json:"operationKind,omitempty"`
	PendingCount  int    `json:"pendingCount"`
}

// SteeringOutcome is one settled effect an interrupted operation left behind.
type SteeringOutcome struct {
	Kind        string `json:"kind"`
	State       string `json:"state"`
	ToolCallID  string `json:"toolCallId,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
	AgentID     string `json:"agentId,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// SteeringReceipt is the state of one accepted Send now request.
type SteeringReceipt struct {
	Version     uint64            `json:"version"`
	RequestID   string            `json:"requestId"`
	TurnID      string            `json:"turnId"`
	Status      string            `json:"status"`
	OperationID string            `json:"operationId,omitempty"`
	Outcomes    []SteeringOutcome `json:"outcomes,omitempty"`
}

// SendNowRequest is one Send now submission.
type SendNowRequest struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
	RequestID string `json:"requestId"`
	Text      string `json:"text,omitempty"`
}

// The receipt statuses.
const (
	SteeringAccepted      = "accepted"
	SteeringInterrupting  = "interrupting"
	SteeringWaitingSafe   = "waiting_safe_boundary"
	SteeringWaitingResume = "waiting_for_resume"
	SteeringDelivered     = "delivered"
	SteeringUndelivered   = "undelivered"
)
