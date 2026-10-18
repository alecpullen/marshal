package session

import (
	"context"
	"errors"
)

var ErrSendNow = errors.New("operation interrupted by user steering")

type OperationRef struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
	ID        string `json:"operationId"`
}
type OperationKind string

const (
	OperationModel OperationKind = "model"
	OperationTools OperationKind = "tools"
	OperationSetup OperationKind = "setup"
)

type SteeringStatus string

const (
	SteeringAccepted      SteeringStatus = "accepted"
	SteeringInterrupting  SteeringStatus = "interrupting"
	SteeringWaitingSafe   SteeringStatus = "waiting_safe_boundary"
	SteeringWaitingResume SteeringStatus = "waiting_for_resume"
	SteeringDelivered     SteeringStatus = "delivered"
	SteeringUndelivered   SteeringStatus = "undelivered"
)

type SteeringMessage struct {
	ID   string
	Text string
}
type SteeringNote struct {
	ID   string
	Text string
}
type SteeringBatch struct {
	Messages []SteeringMessage
	Notes    []SteeringNote
}
type SendNowRequest struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
	RequestID string `json:"requestId"`
	Text      string `json:"text,omitempty"`
}
type SteeringSnapshot struct {
	Version       uint64        `json:"version"`
	TurnID        string        `json:"turnId"`
	Active        bool          `json:"active"`
	Supported     bool          `json:"supported"`
	Held          bool          `json:"held"`
	OperationID   string        `json:"operationId,omitempty"`
	OperationKind OperationKind `json:"operationKind,omitempty"`
	PendingCount  int           `json:"pendingCount"`
}
type SteeringOutcome struct {
	Kind        string `json:"kind"`
	State       string `json:"state"`
	ToolCallID  string `json:"toolCallId,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
	AgentID     string `json:"agentId,omitempty"`
	Summary     string `json:"summary,omitempty"`
}
type SteeringReceipt struct {
	Version     uint64            `json:"version"`
	RequestID   string            `json:"requestId"`
	TurnID      string            `json:"turnId"`
	Status      SteeringStatus    `json:"status"`
	OperationID string            `json:"operationId,omitempty"`
	Outcomes    []SteeringOutcome `json:"outcomes,omitempty"`
}

// Begin has already closed old-operation admission and requested stops
// before it returns. Wait joins those exact executions outside state locks.
type OperationStop struct {
	Wait func(context.Context) ([]SteeringOutcome, error)
}
type OperationInterrupt func(context.Context, OperationRef) (OperationStop, error)

type PromptKind string

const (
	PromptApproval PromptKind = "approval"
	PromptQuestion PromptKind = "question"
	PromptSkill    PromptKind = "skill"
	PromptSudo     PromptKind = "sudo"
)

type PromptKey struct {
	ID        string
	Kind      PromptKind
	Operation OperationRef
}
