package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/viewmodel"
)

// stackFlushInterval is the turn's stack patch rate — five a second — for a
// client that did not declare the marshal capability.
const stackFlushInterval = 200 * time.Millisecond

// marshalStackInterval is the turn's stack patch rate for a client that
// declared the marshal capability.
const marshalStackInterval = 50 * time.Millisecond

// StackParams is the session/stack request body.
type StackParams = acpwire.StackParams

// stackProjector tracks the encoded nodes last sent to one session's client.
type stackProjector struct {
	mu        sync.Mutex
	active    bool
	dirty     bool
	rev       uint64
	sent      map[string][]byte
	sentOrder []string
	// sentRoots is the root list of the last patch or snapshot sent, so a
	// reader can compare against what the client actually holds.
	sentRoots []string
	// memo remembers each node's wire form and encoding from the projector's
	// last projection, so a flush re-projects and re-encodes only the nodes
	// that changed. It is used and kept under mu, like sent.
	memo viewmodel.WireMemo
	// idleCancel stops the idle flusher; set on a session's parent projector.
	idleCancel context.CancelFunc
	// final marks a subagent projector flushed after its subagent stopped
	// running; nothing more changes, so later ticks skip it.
	final bool
	// wakeAt is when the tree changes on elapsed time alone (the band's
	// stale tag appearing); a dirty-only flush still runs once it passes.
	wakeAt time.Time
}

// stackIdleInterval bounds how stale an idle session's stack can be for a
// client that did not declare the marshal capability.
const stackIdleInterval = 500 * time.Millisecond

// stackIntervals reports the turn and idle flush rates for this connection's
// client: the fast cadence for a marshal client, the rate-limited defaults
// otherwise. Each loop reads it once, where its ticker is created.
func (m *TurnManager) stackIntervals() (turn, idle time.Duration) {
	if m.isMarshal() {
		return marshalStackInterval, marshalStackInterval
	}
	return stackFlushInterval, stackIdleInterval
}

// stackKey names a projector: the session ID for the main transcript, and
// "<session>#<subagent>" for a subagent's own.
func stackKey(sessionID string, subagentID int64) string {
	if subagentID == 0 {
		return sessionID
	}
	return sessionID + "#" + strconv.FormatInt(subagentID, 10)
}

// encode is a node's JSON encoding from the projection the memo holds. The
// memo carries every turn node; the band is not its, so it is encoded here as
// the wire requires.
func (p *stackProjector) encode(n acpwire.WireNode) []byte {
	if data, ok := p.memo.Encoded(n.ID); ok {
		return data
	}
	data, _ := json.Marshal(n)
	return data
}

// diff requires p.mu. Unchanged trees leave the revision and sent state alone.
func (p *stackProjector) diff(tree acpwire.WireTree) (acpwire.StackPatch, bool) {
	patch := acpwire.StackPatch{Kind: acpwire.UpdateStackPatch}
	sent := make(map[string][]byte, len(tree.Nodes))
	order := make([]string, 0, len(tree.Nodes))
	for _, n := range tree.Nodes {
		encoded := p.encode(n)
		sent[n.ID] = encoded
		order = append(order, n.ID)
		if !bytes.Equal(encoded, p.sent[n.ID]) {
			patch.Upsert = append(patch.Upsert, n)
		}
	}
	for _, id := range p.sentOrder {
		if _, ok := sent[id]; !ok {
			patch.Remove = append(patch.Remove, id)
		}
	}
	if len(patch.Upsert) == 0 && len(patch.Remove) == 0 {
		return patch, false
	}
	patch.BaseRev = p.rev
	p.rev++
	patch.Rev = p.rev
	patch.Roots = tree.Roots
	p.sent, p.sentOrder = sent, order
	p.sentRoots = append([]string(nil), tree.Roots...)
	return patch, true
}

func (m *TurnManager) stackFor(key string) *stackProjector {
	m.stacksMu.Lock()
	defer m.stacksMu.Unlock()
	p := m.stacks[key]
	if p == nil {
		p = &stackProjector{}
		m.stacks[key] = p
	}
	return p
}

// stackSource resolves the state a stack request reads: the session's own, or
// a subagent's child transcript. drilled is true for the child, whose
// agent.run audits render normally.
func (m *TurnManager) stackSource(rt *TurnRuntime, subagentID int64) (st *session.State, drilled bool, err error) {
	if subagentID == 0 {
		return rt.State, false, nil
	}
	v, ok := rt.State.Subagent(subagentID)
	if !ok {
		return nil, false, invalidParamsError("unknown subagent: %d", subagentID)
	}
	if v.Child == nil {
		return nil, false, invalidParamsError("subagent has no separate transcript")
	}
	return v.Child, true, nil
}

func (m *TurnManager) markStackDirty(sessionID string) {
	m.stacksMu.Lock()
	p := m.stacks[sessionID]
	m.stacksMu.Unlock()
	if p != nil {
		p.mu.Lock()
		p.dirty = true
		p.mu.Unlock()
	}
}

// notifyStackPatch adapts the patch to ACP's existing map-shaped envelope.
// Optional lists are omitted exactly as the patch's JSON tags require.
func (m *TurnManager) notifyStackPatch(sessionID string, patch acpwire.StackPatch) {
	update := map[string]any{
		"kind": patch.Kind, "rev": patch.Rev, "baseRev": patch.BaseRev, "roots": patch.Roots,
	}
	if patch.SubagentID != 0 {
		update["subagentId"] = patch.SubagentID
	}
	if len(patch.Upsert) > 0 {
		update["upsert"] = patch.Upsert
	}
	if len(patch.Remove) > 0 {
		update["remove"] = patch.Remove
	}
	if err := m.notify("session/update", SessionUpdateParams{SessionID: sessionID, Update: update}); err != nil {
		slog.Default().Warn("acp: stack patch notify failed", "session_id", sessionID, "error", err)
	}
}

func (m *TurnManager) flushStack(sessionID string, st *session.State, busy bool) {
	m.flushProjector(sessionID, 0, st, busy, false, false)
	m.flushChildStacks(sessionID, st)
	m.flushRun(sessionID, st)
}

// flushDirtyStack avoids rebuilding the tree on idle ticks during a turn.
func (m *TurnManager) flushDirtyStack(sessionID string, st *session.State) {
	m.flushProjector(sessionID, 0, st, true, false, true)
	m.flushChildStacks(sessionID, st)
	m.flushRun(sessionID, st)
}

// flushProjector diffs one projector against st and notifies on change.
func (m *TurnManager) flushProjector(sessionID string, subagentID int64, st *session.State, busy, drilled, onlyDirty bool) {
	m.flushProjectorFor(m.stackFor(stackKey(sessionID, subagentID)), sessionID, subagentID, st, busy, drilled, onlyDirty)
}

// flushProjectorFor is flushProjector for a projector the caller resolved. An
// unsubscribe can delete a projector between the resolution and the flush, so
// a flush never resolves one itself: doing so would recreate a projector
// nobody subscribes to and leave it in the map.
func (m *TurnManager) flushProjectorFor(p *stackProjector, sessionID string, subagentID int64, st *session.State, busy, drilled, onlyDirty bool) {
	// Resolve the client's flag before taking the projector's lock, so the two
	// mutexes are never nested here in the same order a flip takes them.
	busy = m.projectBusy(sessionID, busy)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := st.Now()
	due := !p.wakeAt.IsZero() && !now.Before(p.wakeAt)
	if !p.active || (onlyDirty && !p.dirty && !due) {
		return
	}
	proj := p.memo.ProjectState(st, busy, now)
	patch, changed := p.diff(proj.Tree)
	p.dirty = false
	p.wakeAt = proj.Band.StaleAt
	if changed {
		patch.SubagentID = subagentID
		m.notifyStackPatch(sessionID, patch)
	}
}

// flushChildStacks flushes every active subagent projector of the session and
// drops those whose subagent no longer has a transcript.
func (m *TurnManager) flushChildStacks(sessionID string, st *session.State) {
	prefix := sessionID + "#"
	m.stacksMu.Lock()
	var keys []string
	for k := range m.stacks {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	m.stacksMu.Unlock()
	for _, k := range keys {
		id, err := strconv.ParseInt(strings.TrimPrefix(k, prefix), 10, 64)
		if err != nil {
			continue
		}
		v, ok := st.Subagent(id)
		if !ok || v.Child == nil {
			m.stacksMu.Lock()
			delete(m.stacks, k)
			m.stacksMu.Unlock()
			continue
		}
		m.stacksMu.Lock()
		cp := m.stacks[k]
		m.stacksMu.Unlock()
		if cp == nil {
			// Unsubscribed since the keys were listed: do not recreate it.
			continue
		}
		cp.mu.Lock()
		skip := cp.final
		cp.mu.Unlock()
		if skip {
			continue
		}
		running := v.Status == session.SubagentRunning
		m.flushProjectorFor(cp, sessionID, id, v.Child, running, true, false)
		if !running {
			cp.mu.Lock()
			cp.final = true
			cp.mu.Unlock()
		}
	}
}

// stackTurnBusy distinguishes an unfinished runner from the reserved slot.
// HasActiveTurn deliberately keeps excluding duplicate prompts until cleanup.
func (m *TurnManager) stackTurnBusy(sessionID string) bool {
	m.activeTurnsMu.Lock()
	defer m.activeTurnsMu.Unlock()
	slot := m.activeTurns[sessionID]
	return slot != nil && !slot.settled.Load()
}

// Stack handles session/stack, flushing changes before answering an active view.
func (m *TurnManager) Stack(ctx context.Context, params json.RawMessage) (any, error) {
	var request StackParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, fmt.Errorf("acp: parse session/stack params: %w", err)
		}
	}
	if request.SessionID == "" {
		return nil, fmt.Errorf("acp: session/stack requires sessionId")
	}
	rt, ok := m.readLookup()(request.SessionID)
	if !ok || rt.State == nil {
		m.dropStacks(request.SessionID)
		return nil, serverErrorf("unknown session: %s", request.SessionID)
	}
	src, drilled, err := m.stackSource(rt, request.SubagentID)
	if err != nil {
		return nil, err
	}
	busy := m.projectBusy(request.SessionID, m.stackTurnBusy(request.SessionID))
	if drilled {
		if v, ok := rt.State.Subagent(request.SubagentID); ok {
			busy = m.projectBusy(request.SessionID, v.Status == session.SubagentRunning)
		}
	}
	snapshot, activated := m.stackSnapshot(request, rt, src, drilled, busy)
	if activated {
		// The idle loop belongs to the session's main projector but serves
		// every projector, so any activation makes sure it is running.
		m.ensureStackIdle(request.SessionID, rt)
	}
	return snapshot, nil
}

// StackUnsubscribe handles session/stack_unsubscribe, stopping the projection
// of one subagent's stack for this client. The session's own transcript
// (subagent 0) has no subscription to drop, so that is an invalid request.
func (m *TurnManager) StackUnsubscribe(ctx context.Context, params json.RawMessage) (any, error) {
	var request StackParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, invalidParamsError("parse session/stack_unsubscribe params: %v", err)
		}
	}
	if request.SessionID == "" {
		return nil, invalidParamsError("session/stack_unsubscribe requires sessionId")
	}
	if request.SubagentID == 0 {
		return nil, invalidParamsError("session/stack_unsubscribe requires subagentId")
	}
	m.stacksMu.Lock()
	delete(m.stacks, stackKey(request.SessionID, request.SubagentID))
	m.stacksMu.Unlock()
	return struct{}{}, nil
}

// stackSnapshot builds the snapshot, activating the projector on first use.
func (m *TurnManager) stackSnapshot(request StackParams, rt *TurnRuntime, src *session.State, drilled, busy bool) (acpwire.StackSnapshot, bool) {
	p := m.stackFor(stackKey(request.SessionID, request.SubagentID))
	p.mu.Lock()
	defer p.mu.Unlock()
	// The band rides along as an extra node. It is not in roots, so a
	// transcript walk never sees it, but the projector carries it to web
	// clients like any other node.
	tree := p.memo.ProjectState(src, busy, src.Now()).Tree
	activated := false
	if !p.active {
		activated = true
		p.active = true
		p.final = false
		p.sent = make(map[string][]byte, len(tree.Nodes))
		p.sentOrder = make([]string, 0, len(tree.Nodes))
		p.rev = 1
		for _, n := range tree.Nodes {
			p.sent[n.ID] = p.encode(n)
			p.sentOrder = append(p.sentOrder, n.ID)
		}
		p.sentRoots = append([]string(nil), tree.Roots...)
	} else if patch, changed := p.diff(tree); changed {
		patch.SubagentID = request.SubagentID
		m.notifyStackPatch(request.SessionID, patch)
	}
	p.dirty = false
	m.watchRun(request.SessionID, rt.State)
	snapshot := acpwire.StackSnapshot{SessionID: request.SessionID, SubagentID: request.SubagentID, Rev: p.rev, Roots: make([]string, 0, len(tree.Roots)), Nodes: make([]acpwire.WireNode, 0, len(tree.Nodes))}
	snapshot.Roots = append(snapshot.Roots, tree.Roots...)
	snapshot.Nodes = append(snapshot.Nodes, tree.Nodes...)
	return snapshot, activated
}

// dropStacks stops the idle flusher and forgets every projector of a session.
func (m *TurnManager) dropStacks(sessionID string) {
	m.dropRun(sessionID)
	m.busyMu.Lock()
	delete(m.clientBusy, sessionID)
	delete(m.busySet, sessionID)
	m.busyMu.Unlock()
	prefix := sessionID + "#"
	m.stacksMu.Lock()
	defer m.stacksMu.Unlock()
	for k, p := range m.stacks {
		if k == sessionID || strings.HasPrefix(k, prefix) {
			p.mu.Lock()
			if p.idleCancel != nil {
				p.idleCancel()
			}
			p.mu.Unlock()
			delete(m.stacks, k)
		}
	}
}

// readLookup is the lookup the transcript reads use: Reads when the config
// gave one, Lookup otherwise. Resolving it per call keeps a caller that swaps
// Lookup after construction reaching the same lookups the prompts do (P3 R4).
func (m *TurnManager) readLookup() Lookup {
	if m.reads != nil {
		return m.reads
	}
	return m.lookup
}

// setClientBusy records the busy flag this connection's stacks of the session
// are projected with, and reports whether it changed (P3 R4).
func (m *TurnManager) setClientBusy(sessionID string, busy bool) bool {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	if m.busySet[sessionID] && m.clientBusy[sessionID] == busy {
		return false
	}
	m.clientBusy[sessionID] = busy
	m.busySet[sessionID] = true
	return true
}

// projectBusy is the busy flag a stack of the session is projected with: the
// one the client set, or rule when it set none.
func (m *TurnManager) projectBusy(sessionID string, rule bool) bool {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	if !m.busySet[sessionID] {
		return rule
	}
	return m.clientBusy[sessionID]
}

// StackBusy handles session/stack_busy: it sets the session's busy flag for
// this connection and, when it changed, re-projects every active stack of
// the session with it and sends their patches before answering.
func (m *TurnManager) StackBusy(ctx context.Context, params json.RawMessage) (any, error) {
	var request acpwire.StackBusyParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, invalidParamsError("parse session/stack_busy params: %v", err)
		}
	}
	if request.SessionID == "" {
		return nil, invalidParamsError("session/stack_busy requires sessionId")
	}
	rt, ok := m.readLookup()(request.SessionID)
	if !ok || rt.State == nil {
		return nil, serverErrorf("unknown session: %s", request.SessionID)
	}
	if !m.setClientBusy(request.SessionID, request.Busy) {
		return struct{}{}, nil
	}
	// A flip reaches a subagent whose projector has settled: clear final on
	// every one of the session's, then flush them with the new flag.
	prefix := request.SessionID + "#"
	m.stacksMu.Lock()
	var subs []*stackProjector
	for k, p := range m.stacks {
		if strings.HasPrefix(k, prefix) {
			subs = append(subs, p)
		}
	}
	main := m.stacks[request.SessionID]
	m.stacksMu.Unlock()
	for _, p := range subs {
		p.mu.Lock()
		p.final = false
		p.mu.Unlock()
	}
	// Flush only a projector that exists: creating one would leave a
	// subscription nobody asked for in the map.
	if main != nil {
		m.flushProjectorFor(main, request.SessionID, 0, rt.State, false, false, false)
	}
	m.flushChildStacks(request.SessionID, rt.State)
	return struct{}{}, nil
}

// ensureStackIdle starts the session's idle flusher unless one is running:
// session events mark the main stack dirty and a ticker flushes it and every
// subagent stack while no turn runs.
func (m *TurnManager) ensureStackIdle(sessionID string, rt *TurnRuntime) {
	if rt.Events == nil {
		return
	}
	p := m.stackFor(sessionID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idleCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.idleCancel = cancel
	events := rt.Events.Subscribe(ctx)
	// The fence is registered before the loop starts, so a read that arrives
	// as soon as this call returns already has a fence to ask (P3 R2).
	idleFence := newFence()
	m.fences.add(sessionID, idleFence)
	go func() {
		defer func() {
			m.fences.remove(sessionID, idleFence)
			idleFence.close()
			cancel()
		}()
		_, idleInterval := m.stackIntervals()
		ticker := time.NewTicker(idleInterval)
		defer ticker.Stop()
		// serveFence answers one fence request: it marks the stack dirty for
		// the events already buffered, then does exactly what a tick does when
		// no turn is running, so a read after a call sees the change.
		serveFence := func(answer chan struct{}) {
		buffered:
			for n := len(events); n > 0; n-- {
				select {
				case _, ok := <-events:
					if !ok {
						break buffered
					}
					m.markStackDirty(sessionID)
				case <-ctx.Done():
					break buffered
				}
			}
			if !m.HasActiveTurn(sessionID) {
				cur, ok := m.readLookup()(sessionID)
				if !ok || cur.State == nil {
					// The session is gone, so stop, exactly as a tick does.
					// Answer first: the deferred close is the loop's own.
					close(answer)
					m.dropStacks(sessionID)
					return
				}
				m.flushProjector(sessionID, 0, cur.State, false, false, true)
				m.flushChildStacks(sessionID, cur.State)
				m.flushRun(sessionID, cur.State)
			}
			close(answer)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					return
				}
				m.markStackDirty(sessionID)
			case answer := <-idleFence.requests():
				serveFence(answer)
			case <-ticker.C:
				if m.HasActiveTurn(sessionID) {
					continue
				}
				cur, ok := m.readLookup()(sessionID)
				if !ok || cur.State == nil {
					m.dropStacks(sessionID)
					return
				}
				m.flushProjector(sessionID, 0, cur.State, false, false, true)
				m.flushChildStacks(sessionID, cur.State)
				m.flushRun(sessionID, cur.State)
			}
		}
	}()
}
