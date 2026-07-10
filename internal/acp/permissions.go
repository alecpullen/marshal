package acp

import (
	"context"
	"errors"

	"marshal/internal/app/session"
)

var (
	ErrNilPending                 = errors.New("acp: permission bridge: nil pending tool call")
	ErrPendingMissingResponseChan = errors.New("acp: permission bridge: pending tool call has no ResponseChan")
)

// serverPermissionClient is the production PermissionClient: it sends an
// outbound `session/request_permission` request to the connected editor
// via Server.Request and returns the decision.
type serverPermissionClient struct {
	server *Server
}

func (c *serverPermissionClient) RequestPermission(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
	var decision PermissionDecision
	if err := c.server.Request(ctx, "session/request_permission", req, &decision); err != nil {
		return PermissionDecision{}, err
	}
	return decision, nil
}

// PermissionClient is the outbound permission request surface used by
// PermissionBridge. In production this is backed by a JSON-RPC client that
// sends a `session/request_permission` request to the connected editor and
// waits for the matching response. The interface keeps the bridge free of
// the transport so tests can drive it with a fake.
type PermissionClient interface {
	RequestPermission(ctx context.Context, req PermissionRequest) (PermissionDecision, error)
}

// PermissionRequest is the JSON-RPC payload for `session/request_permission`.
// The fields mirror session.PendingToolCall; we keep them in a dedicated
// type so the wire shape does not depend on session-package internals.
type PermissionRequest struct {
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Command    string `json:"command,omitempty"`
	Risk       string `json:"risk,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Diff       string `json:"diff,omitempty"`
}

// PermissionDecision is the JSON-RPC result for `session/request_permission`.
// Approved is required; Edited is populated when the editor approves a
// command after the user edited it (e.g. a shell command trimmed of
// dangerous flags).
type PermissionDecision struct {
	Approved bool   `json:"approved"`
	Edited   string `json:"edited,omitempty"`
}

// PermissionBridge translates session.PendingToolCall into a
// PermissionRequest, asks the PermissionClient for a decision, and writes
// the resulting session.UserApprovalDecision back to the pending tool
// call's ResponseChan. Writing to ResponseChan is guarded by the supplied
// context so a turn cancel cannot deadlock the runner.
type PermissionBridge struct {
	client PermissionClient
}

func NewPermissionBridge(client PermissionClient) *PermissionBridge {
	if client == nil {
		panic("acp: NewPermissionBridge requires a non-nil PermissionClient")
	}
	return &PermissionBridge{client: client}
}

// Request sends a permission request for the given pending tool call and
// blocks until either a decision arrives, the context is cancelled, or
// the client returns an error. On context cancel the ResponseChan is left
// untouched (the runner's own select on ResponseChan vs ctx.Done decides
// what to do with the abandoned pending call).
func (b *PermissionBridge) Request(ctx context.Context, pending *session.PendingToolCall) error {
	if pending == nil {
		return ErrNilPending
	}
	if pending.ResponseChan == nil {
		return ErrPendingMissingResponseChan
	}
	req := PermissionRequest{
		ToolCallID: pending.ID,
		ToolName:   pending.Name,
		Command:    pending.Command,
		Risk:       pending.Risk,
		Reason:     pending.Reason,
		Diff:       pending.Diff,
	}
	decision, err := b.client.RequestPermission(ctx, req)
	if err != nil {
		return err
	}
	select {
	case pending.ResponseChan <- session.UserApprovalDecision{
		Approved: decision.Approved,
		Edited:   decision.Edited,
	}:
	case <-ctx.Done():
		// Turn cancelled before we could deliver the decision; the runner
		// will see ctx.Err() and abandon the pending call.
		return ctx.Err()
	}
	return nil
}
