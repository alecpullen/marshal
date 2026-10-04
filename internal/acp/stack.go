package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/viewmodel"
)

// stackFlushInterval bounds the patch rate to five a second.
const stackFlushInterval = 200 * time.Millisecond

// StackParams is the session/stack request body.
type StackParams struct {
	SessionID string `json:"sessionId"`
}

// StackSnapshot is the full stack at Rev (foundation spec §5.1).
type StackSnapshot struct {
	SessionID string               `json:"sessionId"`
	Rev       uint64               `json:"rev"`
	Roots     []string             `json:"roots"`
	Nodes     []viewmodel.WireNode `json:"nodes"`
}

type stackPatch struct {
	Kind    string               `json:"kind"`
	Rev     uint64               `json:"rev"`
	BaseRev uint64               `json:"baseRev"`
	Roots   []string             `json:"roots"`
	Upsert  []viewmodel.WireNode `json:"upsert,omitempty"`
	Remove  []string             `json:"remove,omitempty"`
}

// stackProjector tracks the encoded nodes last sent to one session's client.
type stackProjector struct {
	mu        sync.Mutex
	active    bool
	dirty     bool
	rev       uint64
	sent      map[string][]byte
	sentOrder []string
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

func stackSnapshotOf(st *session.State, busy bool, now time.Time) viewmodel.Snapshot {
	active := st.ActiveToolCalls()
	inProgress := st.InProgress()
	return viewmodel.Snapshot{
		Items: st.Transcript(), Steps: st.Steps(), Todos: st.Todos(),
		ActiveTools: active, InProgress: inProgress,
		Busy:    busy || len(active) > 0 || len(inProgress.Reasoning) > 0,
		Drilled: false, RunningSubagent: st.HasRunningSubagent(), Now: now,
	}
}

func (m *TurnManager) stackFor(sessionID string) *stackProjector {
	m.stacksMu.Lock()
	defer m.stacksMu.Unlock()
	p := m.stacks[sessionID]
	if p == nil {
		p = &stackProjector{}
		m.stacks[sessionID] = p
	}
	return p
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
	p := m.stackFor(sessionID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active {
		return
	}
	patch, changed := p.diff(viewmodel.Project(viewmodel.Build(stackSnapshotOf(st, busy, time.Now()))))
	p.dirty = false
	if changed {
		m.notifyStackPatch(sessionID, patch)
	}
}

// flushDirtyStack avoids rebuilding the tree on idle ticks during a turn.
func (m *TurnManager) flushDirtyStack(sessionID string, st *session.State) {
	p := m.stackFor(sessionID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active || !p.dirty {
		return
	}
	patch, changed := p.diff(viewmodel.Project(viewmodel.Build(stackSnapshotOf(st, true, time.Now()))))
	p.dirty = false
	if changed {
		m.notifyStackPatch(sessionID, patch)
	}
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
		m.stacksMu.Lock()
		delete(m.stacks, request.SessionID)
		m.stacksMu.Unlock()
		return nil, serverErrorf("unknown session: %s", request.SessionID)
	}
	p := m.stackFor(request.SessionID)
	p.mu.Lock()
	defer p.mu.Unlock()
	tree := viewmodel.Project(viewmodel.Build(stackSnapshotOf(rt.State, m.HasActiveTurn(request.SessionID), time.Now())))
	if !p.active {
		p.active = true
		p.sent = make(map[string][]byte, len(tree.Nodes))
		p.sentOrder = make([]string, 0, len(tree.Nodes))
		p.rev = 1
		for _, n := range tree.Nodes {
			p.sent[n.ID], _ = json.Marshal(n)
			p.sentOrder = append(p.sentOrder, n.ID)
		}
	} else if patch, changed := p.diff(tree); changed {
		m.notifyStackPatch(request.SessionID, patch)
	}
	p.dirty = false
	snapshot := StackSnapshot{SessionID: request.SessionID, Rev: p.rev, Roots: make([]string, 0, len(tree.Roots)), Nodes: make([]viewmodel.WireNode, 0, len(tree.Nodes))}
	snapshot.Roots = append(snapshot.Roots, tree.Roots...)
	snapshot.Nodes = append(snapshot.Nodes, tree.Nodes...)
	return snapshot, nil
}
