package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// PromptTurnParams is the JSON-RPC body for session/prompt.
type PromptTurnParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// PromptTurnResult is the JSON-RPC result for session/prompt.
type PromptTurnResult struct {
	StopReason string `json:"stopReason"`
}

// RunnerFunc is the per-turn execution surface. Decoupling the turn
// manager from *agent.Runner keeps the ACP package free of the
// provider/tool/registry/policy dependency chain and lets unit tests stub
// turns in a few lines.
type RunnerFunc func(ctx context.Context, prompt string) error

// TurnRuntime is the per-session slice of state the turn manager needs.
type TurnRuntime struct {
	SessionID string
	BeginWork func(context.Context) (context.Context, func(), error)
	Run       RunnerFunc
	Events    *pubsub.Broker[session.Event]
	// SetMode applies a session-level approval mode (plan, default, edit,
	// copilot, auto). Nil means the runtime does not support mode switching.
	SetMode func(mode string) error
	// Steer enqueues a mid-turn steering message, consumed by the runner
	// at its next loop-top. Nil means the runtime does not support steering.
	Steer func(text string)
}

// Lookup returns the runtime registered for an ACP session id.
type Lookup func(sessionID string) (*TurnRuntime, bool)

// NotifyFunc is the JSON-RPC notification sink.
type NotifyFunc func(method string, params any) error

// TurnManagerConfig wires a TurnManager to external dependencies.
type TurnManagerConfig struct {
	Lookup    Lookup
	Notify    NotifyFunc
	Perms     PermissionClient
	Questions QuestionClient
}

// activeTurn tracks a single in-flight turn for one session. At most one
// activeTurn may exist per session ID; the slot is reserved atomically
// when PromptTurn starts and deleted when the Turn completes.
type activeTurn struct {
	cancel          context.CancelFunc
	done            chan struct{}
	clientCancelled atomic.Bool
}

// TurnManager dispatches session/prompt and session/cancel. At most one
// prompt may run per session; different sessions may run concurrently.
type TurnManager struct {
	lookup  Lookup
	notify  NotifyFunc
	perms   PermissionClient
	bridge  *PermissionBridge
	qbridge *QuestionBridge

	activeTurnsMu sync.Mutex
	activeTurns   map[string]*activeTurn
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
		activeTurns: map[string]*activeTurn{},
	}
	if cfg.Perms != nil {
		tm.bridge = NewPermissionBridge(cfg.Perms)
	}
	if cfg.Questions != nil {
		tm.qbridge = NewQuestionBridge(cfg.Questions)
	}
	return tm
}

// messageUpdate projects a session.Message into a session/update body.
// User messages become user_message_chunk; assistant and system messages
// become agent_message_chunk. The content is always {type:"text", text:…}.
//
// A salvaged message (e.g. one flagged "unverified" because the model never
// made a tool call despite being asked to — see the grounding check in
// agent.Runner.RunTask) is marked with a leading bracketed note, the same
// signal the TUI renders as a "salvaged" badge (transcript.go:
// renderFinalAnswer). ACP has no separate metadata channel for this in v1,
// so folding it into the plain text is the only way an ACP client sees it
// at all — without this, a fabricated-but-flagged answer and a genuine one
// are wire-identical to any ACP client.
func messageUpdate(msg session.Message) map[string]any {
	kind := "user_message_chunk"
	if msg.Role == session.RoleAssistant || msg.Role == session.RoleSystem {
		kind = "agent_message_chunk"
	}
	text := msg.Content
	if msg.Salvaged {
		note := "salvaged"
		if msg.SalvageReason != "" {
			note += " · " + msg.SalvageReason
		}
		text = "[" + note + "] " + text
	}
	return map[string]any{
		"kind": kind,
		"content": map[string]any{
			"type": "text",
			"text": text,
		},
	}
}

// turnProjection carries per-turn state used to project session events into
// wire updates: the accumulated thinking text (for deltas) and the most
// recent active tool call (for correlating tool_call_update with tool_call).
type turnProjection struct {
	lastThinking string
	lastToolID   string
	lastToolName string
}

// toolTextCap bounds args/output text in tool_call wire events.
const toolTextCap = 4096

// capToolText truncates s to toolTextCap bytes with a visible suffix.
func capToolText(s string) string {
	if len(s) <= toolTextCap {
		return s
	}
	return s[:toolTextCap] + "… (truncated)"
}

// eventToSessionUpdate projects a session event into a session/update
// envelope body. Returns (nil, false) for internal events that should not
// be forwarded. Thinking updates compute the delta from the previous value;
// tool_call/tool_call_update come from active-tool and audit events. The
// caller's projection is updated in place.
func eventToSessionUpdate(ev pubsub.Event[session.Event], proj *turnProjection) (map[string]any, bool) {
	switch ev.Type {
	case session.EventMessageAdded:
		if ev.Payload.Message != nil {
			return messageUpdate(*ev.Payload.Message), true
		}
	case session.EventThinkingChanged:
		if ev.Payload.Thinking != nil {
			reasoning := ev.Payload.Thinking.Reasoning
			delta := reasoning
			if proj.lastThinking != "" && strings.HasPrefix(reasoning, proj.lastThinking) {
				delta = reasoning[len(proj.lastThinking):]
			}
			proj.lastThinking = reasoning
			if delta != "" {
				return map[string]any{
					"kind": "agent_thought_chunk",
					"content": map[string]any{
						"type": "text",
						"text": delta,
					},
				}, true
			}
			return nil, false
		}
	case session.EventActiveToolChanged:
		if ev.Payload.ActiveTool != nil {
			atc := ev.Payload.ActiveTool
			id := fmt.Sprintf("%s-%d", atc.Name, atc.StartedAt.UnixNano())
			proj.lastToolID = id
			proj.lastToolName = atc.Name
			return map[string]any{
				"kind":       "tool_call",
				"toolCallId": id,
				"toolName":   atc.Name,
				"args":       capToolText(atc.Args),
				"status":     "running",
			}, true
		}
	case session.EventAuditAdded:
		if ev.Payload.Audit != nil {
			ae := ev.Payload.Audit
			status := "done"
			if ae.Error != "" {
				status = "error"
			}
			id := proj.lastToolID
			if id == "" || ae.ToolName != proj.lastToolName {
				id = fmt.Sprintf("%s-%d", ae.ToolName, ae.Timestamp.UnixNano())
			}
			output := ae.ResultContent
			if output == "" {
				output = ae.ResultSummary
			}
			if status == "error" && output == "" {
				output = ae.Error
			}
			return map[string]any{
				"kind":       "tool_call_update",
				"toolCallId": id,
				"status":     status,
				"output":     capToolText(output),
			}, true
		}
	}
	return nil, false
}

// HasActiveTurn reports whether sessionID currently has an in-flight
// prompt turn. Used by CommandManager to reject session/command while a
// turn is running, the same way the TUI disables command dispatch while
// the agent is busy.
func (m *TurnManager) HasActiveTurn(sessionID string) bool {
	m.activeTurnsMu.Lock()
	defer m.activeTurnsMu.Unlock()
	_, ok := m.activeTurns[sessionID]
	return ok
}

// PromptTurn drives a single agent turn for the named session. It looks
// up the runtime, normalises the prompt, reserves the per-session slot,
// registers runtime work, subscribes to the event broker with terminal
// delivery, starts the runner in a goroutine, forwards session events
// as session/update notifications, and returns PromptTurnResult on
// success.
//
// If a turn is already running for the same session, the duplicate is
// rejected with a serverError (-32000) and the first turn is unaffected.
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

	// Normalise content blocks into a flat prompt string.
	prompt, err := normalizePrompt(p.Prompt)
	if err != nil {
		return nil, err
	}

	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}

	// Create a slot context before making the slot visible so that
	// CancelAndWait never observes a slot without a cancel function.
	slotCtx, slotCancel := context.WithCancel(ctx)
	slot := &activeTurn{
		cancel: slotCancel,
		done:   make(chan struct{}),
	}

	// Reserve the per-session slot.
	m.activeTurnsMu.Lock()
	if _, exists := m.activeTurns[p.SessionID]; exists {
		m.activeTurnsMu.Unlock()
		slotCancel()
		return nil, serverErrorf("session %s already has an active turn", p.SessionID)
	}
	m.activeTurns[p.SessionID] = slot
	m.activeTurnsMu.Unlock()

	// Cleanup: cancel slot, remove from map, close done channel.
	defer func() {
		slotCancel()
		m.activeTurnsMu.Lock()
		if m.activeTurns[p.SessionID] == slot {
			delete(m.activeTurns, p.SessionID)
		}
		m.activeTurnsMu.Unlock()
		close(slot.done)
	}()

	// Register runtime work. If the session is quiescing, return
	// requestCancelled without starting the runner.
	turnCtx, finish, err := rt.BeginWork(slotCtx)
	if err != nil {
		return nil, &jsonRPCError{Code: requestCancelled, Message: err.Error()}
	}
	defer finish()

	subCtx, subCancel := context.WithCancel(turnCtx)
	defer subCancel()
	sub := rt.Events.Subscribe(subCtx, pubsub.WithTerminal[session.Event]())

	runErr := make(chan error, 1)
	go func() {
		runErr <- rt.Run(turnCtx, prompt)
	}()

	// proj carries per-turn projection state: the accumulated thinking
	// text (for computing deltas) and the most recent active tool call
	// (for correlating tool_call_update with tool_call).
	proj := &turnProjection{}

	// turnAnswered guards the pending-question send so the unanswered
	// answer is delivered at most once per question identity (F-BUG-51).
	var turnAnswered sync.Map

	// forward dispatches one session event to the ACP client. Defined
	// once and used in both the main loop and the post-run drain.
	forward := func(ev pubsub.Event[session.Event]) {
		update, hasUpdate := eventToSessionUpdate(ev, proj)
		if hasUpdate {
			if notifyErr := m.notify("session/update", SessionUpdateParams{
				SessionID: p.SessionID,
				Update:    update,
			}); notifyErr != nil {
				// Treat notify error as fatal.
				slotCancel()
				subCancel()
			}
		}

		// Drive pending approvals through the permission bridge
		// in a goroutine so the forwarder never blocks on the
		// bridge (F-CON-54).
		if ev.Type == session.EventPendingApprovalChanged &&
			ev.Payload.PendingApproval != nil {
			pa := ev.Payload.PendingApproval
			if m.bridge == nil {
				// F-SEC-13: without a bridge, the runner is blocked on
				// ResponseChan. Send a deny so the runner unblocks and the
				// turn proceeds. Log so operators can see the misconfig.
				pa.Respond(session.UserApprovalDecision{Approved: false})
				slog.Default().Warn("acp: pending approval arrived but no permission bridge; denied",
					"session", p.SessionID, "approval", pa.ID)
			} else {
				go func() {
					decision, err := m.bridge.Request(turnCtx, pa)
					if err != nil {
						slotCancel()
						subCancel()
						return
					}
					// mode.request elevation: the tool contract makes the
					// responding transport responsible for applying the mode
					// (internal/tools/native/mode_request.go). Apply it and
					// broadcast mode_changed so every attached client stays
					// in sync. An empty Edited defaults to "edit", matching
					// the tool handler.
					if pa.Name == "mode.request" && decision.Approved {
						chosen := decision.Edited
						if chosen == "" {
							chosen = "edit"
						}
						if rt.SetMode != nil {
							if err := rt.SetMode(chosen); err != nil {
								slog.Default().Warn("acp: apply mode elevation",
									"session", p.SessionID, "mode", chosen, "err", err)
							} else {
								_ = m.notify("session/update", SessionUpdateParams{
									SessionID: p.SessionID,
									Update:    map[string]any{"kind": "mode_changed", "mode": chosen},
								})
							}
						}
					}
				}()
			}
		}

		// Drive pending questions through the question bridge in a
		// goroutine so the forwarder never blocks on the client (mirrors
		// the permission bridge, F-CON-54). Without a bridge, preserve the
		// pre-extension behavior: auto-answer Unanswered. The turn-scoped
		// sync.Map guards against duplicate delivery when the event
		// re-fires (F-BUG-51).
		if ev.Type == session.EventPendingQuestionChanged &&
			ev.Payload.PendingQuestion != nil {
			pending := ev.Payload.PendingQuestion
			if _, loaded := turnAnswered.LoadOrStore(pending.ResponseChan, true); loaded {
				return
			}
			if m.qbridge == nil {
				pending.Respond(session.UnansweredAnswers(pending.Questions))
			} else {
				go func() {
					qctx, cancel := context.WithTimeout(turnCtx, questionWait)
					defer cancel()
					if err := m.qbridge.Ask(qctx, p.SessionID, pending); err != nil {
						slog.Default().Warn("acp: question bridge failed; answering Unanswered",
							"session", p.SessionID, "err", err)
						pending.Respond(session.UnansweredAnswers(pending.Questions))
					}
				}()
			}
		}
	}

	forwarding := true
	var runErrVal error
	for forwarding {
		select {
		case <-turnCtx.Done():
			// Turn cancelled (client cancel or parent shutdown).
			subCancel()
			err = <-runErr
			runErrVal = err
			forwarding = false
		case err = <-runErr:
			forwarding = false
			runErrVal = err
		case ev, ok := <-sub:
			if !ok {
				sub = nil
				continue
			}
			forward(ev)
			// If forwarding encountered a fatal error, cancel the
			// turn and wait for the runner.
			if turnCtx.Err() != nil {
				subCancel()
				err = <-runErr
				runErrVal = err
				forwarding = false
			}
		}
	}

	// Drain remaining buffered events from the terminal subscription.
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				return resultOrError(runErrVal, slot)
			}
			forward(ev)
		default:
			return resultOrError(runErrVal, slot)
		}
	}
}

// resultOrError maps the finished-turn state to a return value.
func resultOrError(runErr error, slot *activeTurn) (any, error) {
	if slot.clientCancelled.Load() {
		return PromptTurnResult{StopReason: "cancelled"}, nil
	}
	if runErr != nil {
		return nil, runErr
	}
	return PromptTurnResult{StopReason: "end_turn"}, nil
}

// SetModeParams is the JSON-RPC body for session/set_mode.
type SetModeParams struct {
	SessionID string `json:"sessionId"`
	Mode      string `json:"mode"`
}

// validApprovalModes are the session-level modes an ACP client may request.
var validApprovalModes = map[string]bool{
	"plan":    true,
	"default": true,
	"edit":    true,
	"copilot": true,
	"auto":    true,
}

// SetMode handles session/set_mode: it applies the requested approval mode
// to the session's runtime and broadcasts a mode_changed session/update so
// every attached client stays in sync.
func (m *TurnManager) SetMode(ctx context.Context, params json.RawMessage) (any, error) {
	var p SetModeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/set_mode params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/set_mode requires sessionId")
	}
	if !validApprovalModes[p.Mode] {
		return nil, invalidParamsError("invalid mode %q: want one of plan, default, edit, copilot, auto", p.Mode)
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, serverErrorf("unknown session: %s", p.SessionID)
	}
	if rt.SetMode == nil {
		return nil, serverErrorf("session %s does not support mode switching", p.SessionID)
	}
	if err := rt.SetMode(p.Mode); err != nil {
		return nil, serverErrorf("set mode: %v", err)
	}
	if err := m.notify("session/update", SessionUpdateParams{
		SessionID: p.SessionID,
		Update:    map[string]any{"kind": "mode_changed", "mode": p.Mode},
	}); err != nil {
		return nil, err
	}
	return map[string]any{"mode": p.Mode}, nil
}

// SteerParams is the JSON-RPC body for session/steer.
type SteerParams struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

// Steer handles session/steer: it enqueues a steering message into the
// session's steering queue. Steering requires an active turn — when the
// turn has ended, the client should send session/prompt instead, so a
// steer against an idle session is an explicit error rather than silently
// queued.
func (m *TurnManager) Steer(ctx context.Context, params json.RawMessage) (any, error) {
	var p SteerParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/steer params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/steer requires sessionId")
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, invalidParamsError("session/steer requires non-empty text")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, serverErrorf("unknown session: %s", p.SessionID)
	}
	m.activeTurnsMu.Lock()
	_, active := m.activeTurns[p.SessionID]
	m.activeTurnsMu.Unlock()
	if !active {
		return nil, serverErrorf("session %s has no active turn; send session/prompt instead", p.SessionID)
	}
	if rt.Steer == nil {
		return nil, serverErrorf("session %s does not support steering", p.SessionID)
	}
	rt.Steer(p.Text)
	return map[string]any{}, nil
}

// Cancel is the notification handler for session/cancel. It marks the
// active turn for the named session as client-cancelled, cancels its
// context, and returns immediately without waiting for the runner.
//
// Returns nil, nil. The caller dispatches this as a JSON-RPC notification;
// the return value is discarded and no response is sent to the client.
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
	slot, ok := m.activeTurns[p.SessionID]
	m.activeTurnsMu.Unlock()
	if ok && slot != nil {
		slot.clientCancelled.Store(true)
		slot.cancel()
	}
	return nil, nil
}

// cancelWait is the fallback timeout for CancelAndWait. Exported as a
// package-level var so tests can override it without slowing the suite.
var cancelWait = 30 * time.Second

// questionWait bounds how long a turn blocks waiting for a client to answer
// a question. On expiry the question resolves to the Unanswered sentinel so
// a dead client can never wedge a turn. Package-level var so tests can
// override it without slowing the suite.
var questionWait = 30 * time.Second

// CancelAndWait cancels the active turn for the named session and blocks
// until the runner has fully completed. Returns nil if no turn is active
// (benign double-cancel). The wait is bounded by cancelWait even when the
// caller's context never cancels (F-BUG-50).
func (m *TurnManager) CancelAndWait(ctx context.Context, sessionID string) error {
	m.activeTurnsMu.Lock()
	slot, ok := m.activeTurns[sessionID]
	m.activeTurnsMu.Unlock()
	if !ok || slot == nil {
		return nil
	}

	slot.clientCancelled.Store(true)
	slot.cancel()

	timer := time.NewTimer(cancelWait)
	defer timer.Stop()
	select {
	case <-slot.done:
		return nil
	case <-timer.C:
		return fmt.Errorf("acp: CancelAndWait timed out after %v waiting for slot %s", cancelWait, sessionID)
	case <-ctx.Done():
		return ctx.Err()
	}
}
