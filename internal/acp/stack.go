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

	"marshal/internal/app/session"
	"marshal/internal/viewmodel"
)

// stackFlushInterval bounds the patch rate to five a second.
const stackFlushInterval = 200 * time.Millisecond

// StackParams is the session/stack request body.
type StackParams struct {
	SessionID  string `json:"sessionId"`
	SubagentID int64  `json:"subagentId,omitempty"`
}

// StackSnapshot is the full stack at Rev (foundation spec §5.1).
type StackSnapshot struct {
	SessionID  string               `json:"sessionId"`
	SubagentID int64                `json:"subagentId,omitempty"`
	Rev        uint64               `json:"rev"`
	Roots      []string             `json:"roots"`
	Nodes      []viewmodel.WireNode `json:"nodes"`
}

type stackPatch struct {
	Kind       string               `json:"kind"`
	SubagentID int64                `json:"subagentId,omitempty"`
	Rev        uint64               `json:"rev"`
	BaseRev    uint64               `json:"baseRev"`
	Roots      []string             `json:"roots"`
	Upsert     []viewmodel.WireNode `json:"upsert,omitempty"`
	Remove     []string             `json:"remove,omitempty"`
}

// stackProjector tracks the encoded nodes last sent to one session's client.
type stackProjector struct {
	mu        sync.Mutex
	active    bool
	dirty     bool
	rev       uint64
	sent      map[string][]byte
	sentOrder []string
	// idleCancel stops the idle flusher; set on a session's parent projector.
	idleCancel context.CancelFunc
	// final marks a subagent projector flushed after its subagent stopped
	// running; nothing more changes, so later ticks skip it.
	final bool
}

// stackIdleInterval bounds how stale an idle session's stack can be.
const stackIdleInterval = 500 * time.Millisecond

// stackKey names a projector: the session ID for the main transcript, and
// "<session>#<subagent>" for a subagent's own.
func stackKey(sessionID string, subagentID int64) string {
	if subagentID == 0 {
		return sessionID
	}
	return sessionID + "#" + strconv.FormatInt(subagentID, 10)
}

// diff requires p.mu. Unchanged trees leave the revision and sent state alone.
func (p *stackProjector) diff(tree viewmodel.WireTree) (stackPatch, bool) {
	patch := stackPatch{Kind: "stack_patch"}
	sent := make(map[string][]byte, len(tree.Nodes))
	order := make([]string, 0, len(tree.Nodes))
	for _, n := range tree.Nodes {
		encoded, _ := json.Marshal(n)
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
	return patch, true
}

func stackSnapshotOf(st *session.State, busy, drilled bool, now time.Time) viewmodel.Snapshot {
	active := st.ActiveToolCalls()
	inProgress := st.InProgress()
	return viewmodel.Snapshot{
		Items: st.Transcript(), Steps: st.Steps(), Todos: st.Todos(),
		ActiveTools: active, InProgress: inProgress,
		Busy:    busy || len(active) > 0 || len(inProgress.Reasoning) > 0,
		Drilled: drilled, RunningSubagent: st.HasRunningSubagent(), Now: now,
	}
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
func (m *TurnManager) notifyStackPatch(sessionID string, patch stackPatch) {
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
	p := m.stackFor(stackKey(sessionID, subagentID))
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active || (onlyDirty && !p.dirty) {
		return
	}
	patch, changed := p.diff(viewmodel.Project(viewmodel.Build(stackSnapshotOf(st, busy, drilled, time.Now()))))
	p.dirty = false
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
		cp := m.stackFor(k)
		cp.mu.Lock()
		skip := cp.final
		cp.mu.Unlock()
		if skip {
			continue
		}
		running := v.Status == session.SubagentRunning
		m.flushProjector(sessionID, id, v.Child, running, true, false)
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
	rt, ok := m.lookup(request.SessionID)
	if !ok || rt.State == nil {
		m.dropStacks(request.SessionID)
		return nil, serverErrorf("unknown session: %s", request.SessionID)
	}
	src, drilled, err := m.stackSource(rt, request.SubagentID)
	if err != nil {
		return nil, err
	}
	busy := m.stackTurnBusy(request.SessionID)
	if drilled {
		if v, ok := rt.State.Subagent(request.SubagentID); ok {
			busy = v.Status == session.SubagentRunning
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

// stackSnapshot builds the snapshot, activating the projector on first use.
func (m *TurnManager) stackSnapshot(request StackParams, rt *TurnRuntime, src *session.State, drilled, busy bool) (StackSnapshot, bool) {
	p := m.stackFor(stackKey(request.SessionID, request.SubagentID))
	p.mu.Lock()
	defer p.mu.Unlock()
	tree := viewmodel.Project(viewmodel.Build(stackSnapshotOf(src, busy, drilled, time.Now())))
	activated := false
	if !p.active {
		activated = true
		p.active = true
		p.final = false
		p.sent = make(map[string][]byte, len(tree.Nodes))
		p.sentOrder = make([]string, 0, len(tree.Nodes))
		p.rev = 1
		for _, n := range tree.Nodes {
			p.sent[n.ID], _ = json.Marshal(n)
			p.sentOrder = append(p.sentOrder, n.ID)
		}
	} else if patch, changed := p.diff(tree); changed {
		patch.SubagentID = request.SubagentID
		m.notifyStackPatch(request.SessionID, patch)
	}
	p.dirty = false
	m.watchRun(request.SessionID, rt.State)
	snapshot := StackSnapshot{SessionID: request.SessionID, SubagentID: request.SubagentID, Rev: p.rev, Roots: make([]string, 0, len(tree.Roots)), Nodes: make([]viewmodel.WireNode, 0, len(tree.Nodes))}
	snapshot.Roots = append(snapshot.Roots, tree.Roots...)
	snapshot.Nodes = append(snapshot.Nodes, tree.Nodes...)
	return snapshot, activated
}

// dropStacks stops the idle flusher and forgets every projector of a session.
func (m *TurnManager) dropStacks(sessionID string) {
	m.dropRun(sessionID)
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
	go func() {
		defer cancel()
		ticker := time.NewTicker(stackIdleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					return
				}
				m.markStackDirty(sessionID)
			case <-ticker.C:
				if m.HasActiveTurn(sessionID) {
					continue
				}
				cur, ok := m.lookup(sessionID)
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
