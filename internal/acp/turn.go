package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

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
}

// Lookup returns the runtime registered for an ACP session id.
type Lookup func(sessionID string) (*TurnRuntime, bool)

// NotifyFunc is the JSON-RPC notification sink.
type NotifyFunc func(method string, params any) error

// TurnManagerConfig wires a TurnManager to external dependencies.
type TurnManagerConfig struct {
	Lookup Lookup
	Notify NotifyFunc
	Perms  PermissionClient
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
	lookup Lookup
	notify NotifyFunc
	perms  PermissionClient
	bridge *PermissionBridge

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
	return tm
}

// messageUpdate projects a session.Message into a session/update body.
// User messages become user_message_chunk; assistant and system messages
// become agent_message_chunk. The content is always {type:"text", text:…}.
func messageUpdate(msg session.Message) map[string]any {
	kind := "user_message_chunk"
	if msg.Role == session.RoleAssistant || msg.Role == session.RoleSystem {
		kind = "agent_message_chunk"
	}
	return map[string]any{
		"kind": kind,
		"content": map[string]any{
			"type": "text",
			"text": msg.Content,
		},
	}
}

// eventToSessionUpdate projects a session event into a session/update
// envelope body. Returns (nil, false) for internal events that should not
// be forwarded. Thinking updates compute the delta from the previous value;
// the caller's *lastThinking is updated in place.
func eventToSessionUpdate(ev pubsub.Event[session.Event], lastThinking *string) (map[string]any, bool) {
	switch ev.Type {
	case session.EventMessageAdded:
		if ev.Payload.Message != nil {
			return messageUpdate(*ev.Payload.Message), true
		}
	case session.EventThinkingChanged:
		if ev.Payload.Thinking != nil {
			reasoning := ev.Payload.Thinking.Reasoning
			delta := reasoning
			if *lastThinking != "" && strings.HasPrefix(reasoning, *lastThinking) {
				delta = reasoning[len(*lastThinking):]
			}
			*lastThinking = reasoning
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
	}
	return nil, false
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

	// lastThinking tracks the full accumulated reasoning text for
	// computing thinking deltas. Reset per turn.
	var lastThinking string

	// forward dispatches one session event to the ACP client. Defined
	// once and used in both the main loop and the post-run drain.
	forward := func(ev pubsub.Event[session.Event]) {
		update, hasUpdate := eventToSessionUpdate(ev, &lastThinking)
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
		// synchronously in the forwarding loop.
		if ev.Type == session.EventPendingApprovalChanged &&
			ev.Payload.PendingApproval != nil &&
			m.bridge != nil {
			if err := m.bridge.Request(turnCtx, ev.Payload.PendingApproval); err != nil {
				slotCancel()
				subCancel()
			}
		}

		// Answer pending questions with unanswered immediately —
		// the ACP v1 transport does not support interactive questions.
		if ev.Type == session.EventPendingQuestionChanged &&
			ev.Payload.PendingQuestion != nil {
			pending := ev.Payload.PendingQuestion
			answers := make([]session.Answer, len(pending.Questions))
			for i, q := range pending.Questions {
				answers[i] = session.Answer{Question: q.Question, Answer: session.AnswerUnanswered}
			}
			select {
			case pending.ResponseChan <- answers:
			case <-turnCtx.Done():
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

// CancelAndWait cancels the active turn for the named session and blocks
// until the runner has fully completed. Returns nil if no turn is active
// (benign double-cancel).
func (m *TurnManager) CancelAndWait(ctx context.Context, sessionID string) error {
	m.activeTurnsMu.Lock()
	slot, ok := m.activeTurns[sessionID]
	m.activeTurnsMu.Unlock()
	if !ok || slot == nil {
		return nil
	}

	slot.clientCancelled.Store(true)
	slot.cancel()

	select {
	case <-slot.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
