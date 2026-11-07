package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// childApprovalKey is the identity of one child-owned approval: the child State
// that owns the prompt registry entry plus its canonical prompt key. Two events
// carrying the same pair describe the same request, so the collector asks the
// client once.
type childApprovalKey struct {
	owner *session.State
	key   session.PromptKey
}

// childApprovals collects ordinary approvals forwarded from child actors
// (pipeline and background subagents) for the lifetime of one captured runtime,
// not the lifetime of a single root turn. A child can outlive the turn that
// launched it, so its approval must stay answerable after that turn ends.
//
// It is deliberately narrow: it processes only ApprovalSource-tagged events, it
// makes no policy decision, and it settles through the owning session's
// canonical registry (Pending.Respond routes there), so a late reply can never
// clear a newer request.
type childApprovals struct {
	tm        *TurnManager
	sessionID string
	state     *session.State
	ctx       context.Context
	cancel    context.CancelFunc

	mu   sync.Mutex
	seen map[childApprovalKey]bool
}

// ensureChildApprovals starts the session's child-only approval collector if it
// is not already running for this exact runtime. It is idempotent: Lookup runs
// on every RPC, and only the first call for a captured runtime starts a
// subscriber. A replaced runtime (a new State for the same session id) stops
// the old collector and starts a fresh one.
func (m *TurnManager) ensureChildApprovals(sessionID string, rt *TurnRuntime) {
	if rt == nil || rt.State == nil || rt.Events == nil {
		return
	}
	// A marshal client's requests are the request mirror's (P3 R5): the
	// mirror shows the request, and nothing here may answer it, deny it or
	// fail it.
	if m.isMarshal() {
		return
	}
	m.childMu.Lock()
	defer m.childMu.Unlock()
	if existing, ok := m.childApprovals[sessionID]; ok {
		if existing.state == rt.State {
			return
		}
		// The session was replaced: the old collector answers for a State that
		// no longer owns the session, so it must not keep asking.
		existing.stop()
		delete(m.childApprovals, sessionID)
	}
	c := newChildApprovals(m, sessionID, rt)
	m.childApprovals[sessionID] = c
	c.start(rt.Events)
}

// CloseChildApprovals stops every child approval collector this connection
// started. It is the connection-teardown half of the collector's lifetime; the
// session's own shutdown ends the other half.
func (m *TurnManager) CloseChildApprovals() {
	m.childMu.Lock()
	collectors := m.childApprovals
	m.childApprovals = map[string]*childApprovals{}
	m.childMu.Unlock()
	for _, c := range collectors {
		c.stop()
	}
}

func newChildApprovals(tm *TurnManager, sessionID string, rt *TurnRuntime) *childApprovals {
	parent := rt.State.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &childApprovals{
		tm:        tm,
		sessionID: sessionID,
		state:     rt.State,
		ctx:       ctx,
		cancel:    cancel,
		seen:      map[childApprovalKey]bool{},
	}
}

// start subscribes to the runtime's existing broker. The subscription is
// terminal: a dropped approval would leave a child waiting forever, and the
// collector's own loop only claims a key and dispatches, so it never blocks a
// publisher for long.
func (c *childApprovals) start(broker *pubsub.Broker[session.Event]) {
	events := broker.Subscribe(c.ctx, pubsub.WithTerminal[session.Event]())
	go c.run(events)
}

func (c *childApprovals) stop() { c.cancel() }

func (c *childApprovals) run(events <-chan pubsub.Event[session.Event]) {
	for {
		select {
		case <-c.ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			c.handle(ev)
		}
	}
}

// handle processes one event. Only ApprovalSource-tagged approvals are ours: a
// root approval belongs to the turn that raised it, and every other event type
// is not an approval at all.
func (c *childApprovals) handle(ev pubsub.Event[session.Event]) {
	if ev.Type != session.EventPendingApprovalChanged {
		return
	}
	src := ev.Payload.ApprovalSource
	if src == nil || src.Owner == nil || src.Pending == nil {
		return
	}
	// A typed capability request is not an ordinary approval: it is collected
	// and settled by the runtime's own access path, never here.
	if src.Pending.Access != nil {
		return
	}
	if !c.claim(src.Owner, src.Key) {
		return
	}
	c.collect(src)
}

// claim records one owner+key identity and reports whether this event is the
// first to describe it.
func (c *childApprovals) claim(owner *session.State, key session.PromptKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := childApprovalKey{owner: owner, key: key}
	if c.seen[k] {
		return false
	}
	c.seen[k] = true
	return true
}

// collect asks the client for one child approval and settles it through the
// owning registry. The bridge call runs in its own goroutine so the collector
// never blocks on the client.
func (c *childApprovals) collect(src *session.ApprovalSource) {
	pending := src.Pending
	if c.tm.bridge == nil {
		// Fail closed: without a responder the child would wait forever. Settle
		// this exact owner/key without approval and log the misconfiguration.
		pending.Respond(session.UserApprovalDecision{Approved: false})
		slog.Default().Warn("acp: child approval arrived but no permission bridge; denied",
			"session", c.sessionID, "actor", src.Actor, "approval", pending.ID)
		return
	}
	go func() {
		// The owning child's operation context, not the root turn's: a steering
		// interruption of the child's own operation ends this one RPC.
		original := childPromptContext(src.Owner, src.Key)
		rpcCtx, cleanup := promptRPCContext(c.ctx, original)
		defer cleanup()
		// The canonical pending is what the bridge settles, so Respond routes
		// through the owning registry. The actor attribution is presentation
		// only and never changes which request is answered.
		bridge := NewPermissionBridge(actorPermissionClient{inner: c.tm.bridge.client, actor: src.Actor})
		if _, err := bridge.Request(rpcCtx, c.sessionID, pending); err != nil {
			if childSteeringEndedPrompt(src.Owner, src.Key, original, err) {
				// The child's operation interruption already settled this
				// request canonically; the collector continues.
				return
			}
			// Teardown or a transport failure: settle this exact owner/key
			// without approval so the child unblocks. A late reply for this key
			// cannot clear a newer request — Respond routes through the owning
			// registry, which matches the key it names.
			pending.Respond(session.UserApprovalDecision{Approved: false})
		}
	}()
}

// actorPermissionClient attributes every request it forwards to one child
// actor, so the presented reason names who is asking. It is a presentation
// wrapper only: the decision still settles through the canonical pending.
type actorPermissionClient struct {
	inner PermissionClient
	actor string
}

func (c actorPermissionClient) RequestPermission(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
	if c.actor != "" {
		req.Reason = fmt.Sprintf("subagent %q: %s", c.actor, req.Reason)
	}
	return c.inner.RequestPermission(ctx, req)
}

// childPromptContext returns the owning operation context of a child prompt
// key, or nil when the child has no live record for it.
func childPromptContext(owner *session.State, key session.PromptKey) context.Context {
	if owner == nil || key.ID == "" {
		return nil
	}
	return owner.PromptContext(key)
}

// childSteeringEndedPrompt reports whether err is the cancellation of a child
// prompt RPC because the operation that owned the prompt was interrupted by
// user steering. Such an error ends only that one RPC: the child's session
// already settled the canonical request.
func childSteeringEndedPrompt(owner *session.State, key session.PromptKey, original context.Context, err error) bool {
	if owner != nil && key.ID != "" {
		if errors.Is(owner.PromptCause(key), session.ErrSendNow) {
			return true
		}
	}
	return session.IsSteeringCancellation(original, err)
}
