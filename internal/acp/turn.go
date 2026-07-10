package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// PromptTurnParams is the JSON-RPC body for session/prompt. The
// protocol-agnostic shape is enough to drive v1: a single prompt text and
// the target session id. Streaming deltas, tool-call selection, and image
// attachments are future work.
type PromptTurnParams struct {
	SessionID string `json:"sessionId"`
	Prompt    string `json:"prompt"`
}

// PromptTurnResult is the JSON-RPC result for session/prompt. v1 only
// signals successful end-of-turn; cancellation/exit reasons and final
// usage tokens are added when the cancel/finalize handlers land.
type PromptTurnResult struct {
	StopReason string `json:"stopReason"`
}

// RunnerFunc is the per-turn execution surface. Decoupling the turn
// manager from *agent.Runner keeps the ACP package free of the
// provider/tool/registry/policy dependency chain and lets unit tests stub
// turns in a few lines. The production adapter in run.go wraps
// *agent.Runner.Run.
type RunnerFunc func(ctx context.Context, prompt string) error

// TurnRuntime is the per-session slice of state the turn manager needs:
// the session id (for log/cancel tracking), the runner that drives one
// turn, and the event broker the session publishes to. The broker is
// captured by value (pointer) so the same subscriptions that drove the
// TUI also drive the ACP transport.
type TurnRuntime struct {
	SessionID string
	Run       RunnerFunc
	Events    *pubsub.Broker[session.Event]
}

// Lookup returns the runtime registered for an ACP session id. Mirrors
// SessionManager.Get; supplied separately so the turn manager does not
// own session lifecycle.
type Lookup func(sessionID string) (*TurnRuntime, bool)

// NotifyFunc is the JSON-RPC notification sink. Matches Server.Notify;
// passed as a value so the turn manager never calls a stale *Server after
// a transport restart.
type NotifyFunc func(method string, params any) error

// TurnManagerConfig wires a TurnManager to a SessionManager and an ACP
// Server without forcing a circular import. All fields are required: a
// nil Lookup or Notify causes NewTurnManager to panic, matching the
// "no silent nil" policy used elsewhere in the package.
type TurnManagerConfig struct {
	Lookup Lookup
	Notify NotifyFunc
	Perms  PermissionClient
}

// TurnManager dispatches session/prompt and session/cancel. Prompt turns
// are serial per session in v1 (F21 Known Risks: "Approval is currently a
// single pending slot"), so the active-turn map is guarded by a mutex but
// not segmented per session.
type TurnManager struct {
	lookup Lookup
	notify NotifyFunc
	perms  PermissionClient
	bridge *PermissionBridge

	activeTurnsMu sync.Mutex
	activeTurns   map[string]context.CancelFunc
}

func NewTurnManager(cfg TurnManagerConfig) *TurnManager {
	if cfg.Lookup == nil {
		panic("acp: TurnManagerConfig.Lookup is required")
	}
	if cfg.Notify == nil {
		panic("acp: TurnManagerConfig.Notify is required")
	}
	tm := &TurnManager{
		lookup:      cfg.Lookup,
		notify:      cfg.Notify,
		perms:       cfg.Perms,
		activeTurns: map[string]context.CancelFunc{},
	}
	if cfg.Perms != nil {
		tm.bridge = NewPermissionBridge(cfg.Perms)
	}
	return tm
}

// sessionEventToNotification maps the session-publisher event type to the
// ACP notification method. Anything we don't explicitly handle is dropped
// at this layer; later tasks will add tool_call, plan_update, etc.
func sessionEventToNotification(typ string) string {
	switch typ {
	case session.EventMessageAdded:
		return "session/message_added"
	case session.EventThinkingChanged:
		return "session/thinking_changed"
	case session.EventActivityChanged:
		return "session/activity_changed"
	case session.EventActiveToolChanged:
		return "session/active_tool_changed"
	case session.EventAuditAdded:
		return "session/audit_added"
	case session.EventPendingApprovalChanged:
		return "session/pending_approval_changed"
	case session.EventPendingQuestionChanged:
		return "session/pending_question_changed"
	}
	return ""
}

// eventToParams projects a session.Event into the params the ACP
// notification carries. The shape is intentionally small in v1: just
// enough for clients to render the right widget. Each field is
// independently nullable so a single object can carry any combination.
func eventToParams(ev session.Event) map[string]any {
	out := map[string]any{}
	if ev.Message != nil {
		out["message"] = map[string]any{
			"role":        string(ev.Message.Role),
			"content":     ev.Message.Content,
			"contentType": string(ev.Message.ContentType),
		}
	}
	if ev.Thinking != nil {
		out["thinking"] = map[string]any{
			"reasoning": ev.Thinking.Reasoning,
			"active":    ev.Thinking.Active,
		}
	}
	if ev.Activity != nil {
		out["activity"] = map[string]any{
			"kind":  string(ev.Activity.Kind),
			"label": ev.Activity.Label,
		}
	}
	if ev.ActiveTool != nil {
		out["activeTool"] = map[string]any{
			"name": ev.ActiveTool.Name,
			"args": ev.ActiveTool.Args,
		}
	}
	if ev.Audit != nil {
		out["audit"] = map[string]any{
			"toolName": ev.Audit.ToolName,
			"approval": string(ev.Audit.Approval),
			"errored":  ev.Audit.Error != "",
		}
	}
	if ev.PendingApproval != nil {
		out["pendingApproval"] = map[string]any{
			"id":   ev.PendingApproval.ID,
			"name": ev.PendingApproval.Name,
		}
	}
	if ev.PendingQuestion != nil {
		out["pendingQuestion"] = map[string]any{
			"count": len(ev.PendingQuestion.Questions),
		}
	}
	return out
}

// PromptTurn drives a single agent turn for the named session. It looks
// up the runtime, subscribes to its event broker, starts the runner in a
// goroutine, forwards session events to ACP notifications, and returns
// PromptTurnResult{StopReason:"end_turn"} on success. The runner is given
// a context tied to the caller's context and a cancel registered against
// the active-turn map, so session/cancel can interrupt an in-flight turn.
func (m *TurnManager) PromptTurn(ctx context.Context, params json.RawMessage) (any, error) {
	var p PromptTurnParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/prompt params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/prompt requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}

	turnCtx, cancel := context.WithCancel(ctx)
	m.activeTurnsMu.Lock()
	// v1 is serial per session. If a previous turn is still registered
	// (e.g. the user sent a second prompt before the first finished),
	// cancel it so the new turn can take over the slot.
	if prev, ok := m.activeTurns[p.SessionID]; ok && prev != nil {
		prev()
	}
	m.activeTurns[p.SessionID] = cancel
	m.activeTurnsMu.Unlock()
	defer func() {
		m.activeTurnsMu.Lock()
		delete(m.activeTurns, p.SessionID)
		m.activeTurnsMu.Unlock()
		cancel()
	}()

	subCtx, subCancel := context.WithCancel(turnCtx)
	defer subCancel()
	sub := rt.Events.Subscribe(subCtx)

	runErr := make(chan error, 1)
	go func() {
		runErr <- rt.Run(turnCtx, p.Prompt)
	}()

	// forward dispatches one session event to the ACP client: emit the
	// matching notification and, when a pending approval appears, drive
	// it through the permission bridge. Defined once and called from
	// both the main forwarding loop and the post-run drain so the two
	// sites cannot drift.
	forward := func(ev pubsub.Event[session.Event]) {
		method := sessionEventToNotification(ev.Type)
		if method == "" {
			return
		}
		_ = m.notify(method, eventToParams(ev.Payload))
		// When a pending approval appears, drive the editor's
		// decision through the permission bridge. The bridge writes
		// the resulting decision to the pending tool call's
		// ResponseChan, which the runner is blocked on. Done
		// synchronously here so we don't lose the turn context
		// before delivery; a nil PendingApproval is a clear signal
		// (no second bridge call per approval).
		if ev.Type == session.EventPendingApprovalChanged &&
			ev.Payload.PendingApproval != nil &&
			m.bridge != nil {
			_ = m.bridge.Request(turnCtx, ev.Payload.PendingApproval)
		}
	}

	forwarding := true
	var runErrVal error
	for forwarding {
		select {
		case <-ctx.Done():
			cancel()
			subCancel()
			<-runErr
			return nil, ctx.Err()
		case err := <-runErr:
			forwarding = false
			runErrVal = err
		case ev, ok := <-sub:
			if !ok {
				// The subscription channel closed (broker shutdown
				// or subCtx cancelled). Nil the channel to disable
				// this select case; otherwise a closed channel is
				// always ready and the loop would busy-wait.
				sub = nil
				continue
			}
			forward(ev)
		}
	}
	// Drain any remaining buffered events the runner emitted just before
	// returning. The session event broker is best-effort with a small
	// buffer, so for the events that did make it in we still want to
	// forward them to the client before the turn result is returned.
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				if runErrVal != nil {
					return nil, runErrVal
				}
				return PromptTurnResult{StopReason: "end_turn"}, nil
			}
			forward(ev)
		default:
			if runErrVal != nil {
				return nil, runErrVal
			}
			return PromptTurnResult{StopReason: "end_turn"}, nil
		}
	}
}

// Cancel interrupts the active turn for the named session. If no turn
// is running, Cancel returns success with a nil result so a benign
// double-cancel (e.g. the user clicks cancel after the turn ended) does
// not error.
func (m *TurnManager) Cancel(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/cancel params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/cancel requires sessionId")
	}
	m.activeTurnsMu.Lock()
	cancel, ok := m.activeTurns[p.SessionID]
	m.activeTurnsMu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
	return nil, nil
}
