package acp

import (
	"context"
	"errors"
	"fmt"

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
// SessionID lets the bridge key the pending permission to its issuing
// session, so a session cancel or client disconnect can drain it.
type PermissionRequest struct {
	SessionID  string `json:"sessionId"`
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Command    string `json:"command,omitempty"`
	Risk       string `json:"risk,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Diff       string `json:"diff,omitempty"`
	// SkillGate tags the request as a skill-load gate (not a tool
	// permission), so client UIs can label it. Approve = allow once;
	// deny = sticky session deny.
	SkillGate bool `json:"skillGate,omitempty"`
	// ModeAccess carries the typed capability-request presentation for a
	// mode.request. It is additive: a client that ignores it still receives
	// the full disclosure in Reason.
	ModeAccess *ModeAccessRequest `json:"modeAccess,omitempty"`
}

// ModeAccessState is the effective access tuple as it appears on the wire.
// It deliberately duplicates the session type rather than aliasing it: the
// JSON contract is the integration boundary, and web/ must never import an
// internal Go type.
type ModeAccessState struct {
	Mode   string `json:"mode"`
	System bool   `json:"system"`
}

// ModeAccessRequest is the typed presentation of one capability request.
type ModeAccessRequest struct {
	RequestID      string          `json:"requestId"`
	SessionID      string          `json:"sessionId"`
	Actor          string          `json:"actor,omitempty"`
	StepID         int64           `json:"stepId,omitempty"`
	Reason         string          `json:"reason"`
	Current        ModeAccessState `json:"current"`
	MissingEditing bool            `json:"missingEditing"`
	MissingSystem  bool            `json:"missingSystem"`
}

// ModeAccessChoice is the typed answer to a ModeAccessRequest. The outer
// PermissionDecision.Approved remains the decision; this carries identity,
// the presented tuple and the selected editing variant only.
type ModeAccessChoice struct {
	RequestID string          `json:"requestId"`
	SessionID string          `json:"sessionId"`
	Current   ModeAccessState `json:"current"`
	Mode      string          `json:"mode,omitempty"`
}

// PermissionDecision is the JSON-RPC result for `session/request_permission`.
// Approved is required; Edited is populated when the editor approves a
// command after the user edited it (e.g. a shell command trimmed of
// dangerous flags).
type PermissionDecision struct {
	Approved bool   `json:"approved"`
	Edited   string `json:"edited,omitempty"`
	// ModeAccess is the typed capability choice, when the client supports it.
	ModeAccess *ModeAccessChoice `json:"modeAccess,omitempty"`
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
// the client returns an error. On success it returns the decision that was
// delivered to ResponseChan, so callers can act on it (e.g. applying a
// mode.request elevation). On context cancel the ResponseChan is left
// untouched (the runner's own select on ResponseChan vs ctx.Done decides
// what to do with the abandoned pending call).
func (b *PermissionBridge) Request(ctx context.Context, sessionID string, pending *session.PendingToolCall) (session.UserApprovalDecision, error) {
	if pending == nil {
		return session.UserApprovalDecision{}, ErrNilPending
	}
	// A typed access request is collected by CollectAccess and settled by the
	// runtime resolver. Delivering an ordinary approval on its channel would
	// acknowledge a capability nothing validated or applied.
	if pending.Access != nil {
		return session.UserApprovalDecision{}, errors.New("acp: access requests require CollectAccess and runtime resolution")
	}
	// A registered prompt (Key.ID set) settles through the session's canonical
	// registry: its event/display copy carries no channel of its own, and
	// nothing here may send to or close a copied one. Only an unregistered
	// legacy fixture keeps the direct-channel behavior.
	registered := pending.Key.ID != ""
	if !registered && pending.ResponseChan == nil {
		return session.UserApprovalDecision{}, ErrPendingMissingResponseChan
	}
	req := PermissionRequest{
		SessionID:  sessionID,
		ToolCallID: pending.ID,
		ToolName:   pending.Name,
		Command:    pending.Command,
		Risk:       pending.Risk,
		Reason:     pending.Reason,
		Diff:       pending.Diff,
	}
	decision, err := b.client.RequestPermission(ctx, req)
	if err != nil {
		return session.UserApprovalDecision{}, err
	}
	out := session.UserApprovalDecision{
		Approved: decision.Approved,
		Edited:   decision.Edited,
	}
	if registered {
		// Respond routes a registered prompt to the canonical record, which
		// arbitrates this answer against an accepted steering cancellation; a
		// canceled or superseded request can neither grant nor clear anything.
		pending.Respond(out)
		return out, nil
	}
	select {
	case pending.ResponseChan <- out:
	case <-ctx.Done():
		// Turn cancelled before we could deliver the decision; the runner
		// will see ctx.Err() and abandon the pending call.
		return session.UserApprovalDecision{}, ctx.Err()
	}
	return out, nil
}

// RequestSkillGate sends a skill-load gate request for the given pending
// gate prompt and blocks until a decision arrives, the context is
// cancelled, or the client errors. ACP clients cannot express the four
// TUI choices, so the decision maps: approved → SkillGateAllowOnce, denied
// → SkillGateDeny (sticky). On context cancel the ResponseChan is left
// untouched (the runner's own select decides what to do with the
// abandoned pending prompt).
func (b *PermissionBridge) RequestSkillGate(ctx context.Context, sessionID string, pending *session.PendingSkillGate) (session.SkillGateChoice, error) {
	if pending == nil {
		return 0, ErrNilPending
	}
	// A registered gate settles through the canonical registry; its event copy
	// carries no channel of its own. Only an unregistered legacy fixture keeps
	// the direct-channel behavior.
	registered := pending.Key.ID != ""
	if !registered && pending.ResponseChan == nil {
		return 0, ErrPendingMissingResponseChan
	}
	req := PermissionRequest{
		SessionID:  sessionID,
		ToolCallID: "skillgate-" + pending.Skill,
		ToolName:   "skill.load",
		Command:    pending.Skill,
		Risk:       "read_only",
		Reason:     pending.Reason,
		SkillGate:  true,
	}
	decision, err := b.client.RequestPermission(ctx, req)
	if err != nil {
		return 0, err
	}
	out := session.SkillGateDeny
	if decision.Approved {
		out = session.SkillGateAllowOnce
	}
	if registered {
		// Respond routes a registered gate to the canonical record: a canceled
		// gate is settled as interrupted (never a sticky denial) and a
		// superseded one cannot be answered here.
		pending.Respond(out)
		return out, nil
	}
	select {
	case pending.ResponseChan <- out:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return out, nil
}

// CollectAccess asks the client for a decision on a typed capability request
// and returns the choice. It is deliberately collect-only: it never writes to
// the pending tool call's channel, because the runtime resolver is what
// validates and applies the grant, and only the session's own settlement path
// may acknowledge one. The caller (the turn loop) hands the choice to the
// runtime and lets it settle the canonical request.
//
// The request carries both the typed modeAccess payload and a full textual
// disclosure in Reason, so a client that ignores the new field still shows the
// user what is being asked: the scope, the lifetime, and that sudo is not
// included.
func (b *PermissionBridge) CollectAccess(ctx context.Context, sessionID, toolCallID string, request session.AccessRequest) (session.AccessChoice, error) {
	if sessionID != request.SessionID || request.RequestID == "" {
		return session.AccessChoice{}, errors.New("acp: invalid access request identity")
	}
	scope := "System access; interaction mode stays unchanged."
	if request.MissingEditing {
		scope = "Editing capability."
		if request.MissingSystem {
			scope = "Editing capability and system access, as one bundle."
		}
	}
	disclosure := "This conversation only. Interaction mode, policy and sandbox still apply."
	if request.MissingSystem {
		disclosure += " Filesystem access outside the workspace and relaxed workspace shell guardrails; no sudo."
	}
	wire := &ModeAccessRequest{
		RequestID: request.RequestID, SessionID: request.SessionID,
		Actor: request.Actor, StepID: request.StepID, Reason: request.Reason,
		Current:        ModeAccessState{Mode: request.Current.Mode, System: request.Current.System},
		MissingEditing: request.MissingEditing, MissingSystem: request.MissingSystem,
	}
	decision, err := b.client.RequestPermission(ctx, PermissionRequest{
		SessionID: sessionID, ToolCallID: toolCallID, ToolName: "mode.request",
		Risk: "read_only",
		Reason: fmt.Sprintf("%s\nCurrent mode: %s; system access: %t.\n%s\n%s",
			request.Reason, request.Current.Mode, request.Current.System, scope, disclosure),
		ModeAccess: wire,
	})
	if err != nil {
		return session.AccessChoice{}, err
	}
	choice := session.AccessChoice{
		RequestID: request.RequestID, SessionID: request.SessionID,
		Current: request.Current, Approved: decision.Approved,
	}
	if decision.ModeAccess != nil {
		echoed := decision.ModeAccess
		if echoed.RequestID != request.RequestID || echoed.SessionID != request.SessionID {
			return session.AccessChoice{}, errors.New("acp: access choice identity mismatch")
		}
		// The choice is judged against the tuple the server actually
		// presented, never the client's echo of it. Trusting the echo would
		// let a client — buggy or hostile — report a tuple that was never
		// shown, which the session reads as "the state moved": a refresh
		// that re-asks against a state the client misdescribes again, and
		// so on without the user ever settling anything. choice.Current
		// keeps the presented snapshot; only the variant comes from the
		// echo.
		choice.Mode = echoed.Mode
	} else if choice.Approved && request.MissingEditing {
		choice.Mode = decision.Edited
		if choice.Mode == "" {
			choice.Mode = "edit"
		}
	}
	// Legacy Edited is ignored when no editing selector was presented.
	// Validation/application and canonical channel delivery belong to runtime.
	return choice, nil
}
