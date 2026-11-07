package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"marshal/internal/acpwire"
)

// The mirror's pinned errors.
var (
	// errStacksUnbound is Stack's and Release's error before Bind.
	errStacksUnbound = errors.New("rpc: stacks are not bound to a connection")
	// errMainTranscriptRelease is Release's error for subagent 0.
	errMainTranscriptRelease = errors.New("rpc: the main transcript cannot be released")
)

// stackKey names one mirrored stack: a session and, when it is not 0, one of
// its subagents.
type stackKey struct {
	session  string
	subagent int64
}

// stackEntry is one mirrored stack: the last revision the mirror holds, its
// tree, and the fetch state.
type stackEntry struct {
	rev   uint64
	roots []string
	nodes map[string]acpwire.WireNode
	// order holds the nodes' IDs in first-arrival order.
	order []string

	// cached is the read order, reused until a change clears it.
	cached     []acpwire.WireNode
	orderValid bool

	// fetching says a session/stack fetch is in flight; queue holds the
	// patches that arrived while it was.
	fetching bool
	queue    []acpwire.StackPatch
}

// Stacks mirrors the transcripts one connection has read (P3 R3, R4).
type Stacks struct {
	mu      sync.Mutex
	conn    *Conn
	entries map[stackKey]*stackEntry
	changed map[string]chan struct{}
	// onChange, when set, runs beside every change's own Changed signal.
	onChange func(sessionID string)
}

// NewStacks returns an empty mirror. Bind it before the first read.
func NewStacks() *Stacks {
	return &Stacks{
		entries: make(map[stackKey]*stackEntry),
		changed: make(map[string]chan struct{}),
	}
}

// Bind sets the connection the mirror fetches through.
func (s *Stacks) Bind(c *Conn) {
	s.mu.Lock()
	s.conn = c
	s.mu.Unlock()
}

// OnChange sets a function the mirror calls, with the session's ID, after
// every change it applies to that session's stacks, beside its own Changed.
func (s *Stacks) OnChange(f func(sessionID string)) {
	s.mu.Lock()
	s.onChange = f
	s.mu.Unlock()
}

// Notify applies session/update stack_patch updates and ignores every other
// notification. It is a Handlers.Notify, or part of one: it runs on the read
// goroutine and never blocks on the connection.
func (s *Stacks) Notify(method string, params json.RawMessage) {
	if method != acpwire.MethodSessionUpdate {
		return
	}
	var update acpwire.SessionUpdate
	if err := json.Unmarshal(params, &update); err != nil {
		return
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(update.Update, &kind); err != nil || kind.Kind != acpwire.UpdateStackPatch {
		return
	}
	var patch acpwire.StackPatch
	if err := json.Unmarshal(update.Update, &patch); err != nil {
		return
	}

	key := stackKey{session: update.SessionID, subagent: patch.SubagentID}

	s.mu.Lock()
	entry, ok := s.entries[key]
	if !ok {
		// A patch for a stack nobody read is dropped.
		s.mu.Unlock()
		return
	}
	if entry.fetching {
		entry.queue = append(entry.queue, patch)
		s.mu.Unlock()
		return
	}
	switch {
	case patch.Rev <= entry.rev:
		// A duplicate: the mirror already holds this revision or a later one.
		s.mu.Unlock()
		return
	case patch.BaseRev == entry.rev:
		entry.apply(patch)
		s.mu.Unlock()
	default:
		// A gap. The fetch runs on its own goroutine: Notify is the read
		// goroutine and must not block on the connection.
		entry.fetching = true
		s.mu.Unlock()
		s.refetch(key)
		return
	}
	s.signal(update.SessionID)
}

// Stack returns the mirrored transcript of a session (subagentID 0) or of one
// of its subagents. The first read of a stack fetches it with session/stack,
// which subscribes the connection; later reads come from the mirror without
// I/O. The nodes are in the agent's order, and shared with the mirror: a
// caller must not change them.
func (s *Stacks) Stack(ctx context.Context, sessionID string, subagentID int64) (acpwire.StackSnapshot, error) {
	key := stackKey{session: sessionID, subagent: subagentID}

	s.mu.Lock()
	conn := s.conn
	if conn == nil {
		s.mu.Unlock()
		return acpwire.StackSnapshot{}, errStacksUnbound
	}
	if entry, ok := s.entries[key]; ok {
		// A later read while the first fetch is in flight sees what it has,
		// which is nothing yet.
		snap := entry.snapshot(sessionID, subagentID)
		s.mu.Unlock()
		return snap, nil
	}
	// The entry exists before the request goes out, so patches arriving while
	// the fetch is in flight are queued rather than lost.
	entry := &stackEntry{
		nodes:    make(map[string]acpwire.WireNode),
		fetching: true,
	}
	s.entries[key] = entry
	s.mu.Unlock()

	var snap acpwire.StackSnapshot
	params := acpwire.StackParams{SessionID: sessionID, SubagentID: subagentID}
	if err := conn.Call(ctx, acpwire.MethodStack, params, &snap); err != nil {
		s.mu.Lock()
		if s.entries[key] == entry {
			delete(s.entries, key)
		}
		s.mu.Unlock()
		return acpwire.StackSnapshot{}, err
	}

	s.mu.Lock()
	installed, again := false, false
	if s.entries[key] == entry {
		again = entry.install(snap)
		installed = true
	}
	s.mu.Unlock()
	if installed {
		s.signal(sessionID)
	}
	if again {
		s.refetch(key)
	}
	return snap, nil
}

// Release forgets a subagent's mirrored stack and unsubscribes it with
// session/stack_unsubscribe.
func (s *Stacks) Release(ctx context.Context, sessionID string, subagentID int64) error {
	if subagentID == 0 {
		return errMainTranscriptRelease
	}
	s.mu.Lock()
	conn := s.conn
	if conn == nil {
		s.mu.Unlock()
		return errStacksUnbound
	}
	// Forget the entry first: a failed unsubscribe still leaves the mirror
	// consistent.
	delete(s.entries, stackKey{session: sessionID, subagent: subagentID})
	s.mu.Unlock()

	params := acpwire.StackParams{SessionID: sessionID, SubagentID: subagentID}
	return conn.Call(ctx, acpwire.MethodStackUnsubscribe, params, nil)
}

// Changed signals, with a buffer of one, after any stack of sessionID changes
// in the mirror.
func (s *Stacks) Changed(sessionID string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changedLocked(sessionID)
}

// changedLocked returns the session's signal channel, creating it on first
// use. The caller holds s.mu.
func (s *Stacks) changedLocked(sessionID string) chan struct{} {
	ch, ok := s.changed[sessionID]
	if !ok {
		ch = make(chan struct{}, 1)
		s.changed[sessionID] = ch
	}
	return ch
}

// signal taps a session's Changed channel without blocking, then hands the
// change to OnChange's function, if one is set.
func (s *Stacks) signal(sessionID string) {
	s.mu.Lock()
	ch := s.changedLocked(sessionID)
	onChange := s.onChange
	s.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
	if onChange != nil {
		onChange(sessionID)
	}
}

// refetch starts one session/stack fetch for a stack whose entry is already
// marked fetching, on its own goroutine.
func (s *Stacks) refetch(key stackKey) {
	go func() {
		s.mu.Lock()
		conn := s.conn
		entry, ok := s.entries[key]
		if !ok {
			s.mu.Unlock()
			return
		}
		if conn == nil {
			// Nothing can serve the fetch: forget the entry, as a failed fetch
			// does, so the next read fetches again instead of serving a stack
			// whose queued patches will never be applied.
			delete(s.entries, key)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		var snap acpwire.StackSnapshot
		params := acpwire.StackParams{SessionID: key.session, SubagentID: key.subagent}
		err := conn.Call(context.Background(), acpwire.MethodStack, params, &snap)

		s.mu.Lock()
		if s.entries[key] != entry {
			// Released or replaced while the fetch was in flight. Its snapshot
			// belongs to a mirror that no longer exists: installing it would
			// overwrite the entry that replaced this one, and resurrect the
			// one Release dropped.
			s.mu.Unlock()
			return
		}
		if err != nil {
			// The connection's end failed the fetch: forget the entry so the
			// next read fetches again.
			delete(s.entries, key)
			s.mu.Unlock()
			return
		}
		again := entry.install(snap)
		s.mu.Unlock()

		s.signal(key.session)
		if again {
			s.refetch(key)
		}
	}()
}

// install replaces the entry with snap, then drains the patches queued while
// the fetch was in flight. It reports whether the queue left a patch that
// does not chain and needs another fetch.
func (e *stackEntry) install(snap acpwire.StackSnapshot) bool {
	e.rev = snap.Rev
	e.roots = snap.Roots
	e.nodes = make(map[string]acpwire.WireNode, len(snap.Nodes))
	e.order = make([]string, 0, len(snap.Nodes))
	for _, node := range snap.Nodes {
		if _, seen := e.nodes[node.ID]; !seen {
			e.order = append(e.order, node.ID)
		}
		e.nodes[node.ID] = node
	}
	e.invalidate()
	e.fetching = false

	queue := e.queue
	e.queue = nil
	for i, patch := range queue {
		if patch.Rev <= e.rev {
			// The snapshot already carries it.
			continue
		}
		if patch.BaseRev != e.rev {
			// A second gap: keep this patch and the rest queued for the next
			// fetch.
			e.fetching = true
			e.queue = append([]acpwire.StackPatch(nil), queue[i:]...)
			return true
		}
		e.apply(patch)
	}
	return false
}

// apply applies one patch whose base is the entry's revision.
func (e *stackEntry) apply(patch acpwire.StackPatch) {
	for _, id := range patch.Remove {
		delete(e.nodes, id)
	}
	for _, node := range patch.Upsert {
		if _, seen := e.nodes[node.ID]; !seen {
			e.order = append(e.order, node.ID)
		}
		e.nodes[node.ID] = node
	}
	e.roots = patch.Roots
	e.rev = patch.Rev
	e.invalidate()
}

// invalidate clears the cached read order; every change does.
func (e *stackEntry) invalidate() {
	e.cached = nil
	e.orderValid = false
}

// snapshot returns the stack as a read sees it, rebuilding the read order when
// it changed since the last read.
func (e *stackEntry) snapshot(sessionID string, subagentID int64) acpwire.StackSnapshot {
	if !e.orderValid {
		e.cached = e.readOrder()
		e.orderValid = true
	}
	nodes := make([]acpwire.WireNode, len(e.cached))
	copy(nodes, e.cached)
	roots := make([]string, len(e.roots))
	copy(roots, e.roots)
	return acpwire.StackSnapshot{
		SessionID:  sessionID,
		SubagentID: subagentID,
		Rev:        e.rev,
		Roots:      roots,
		Nodes:      nodes,
	}
}

// readOrder walks the tree depth-first from each root through each node's
// children, visiting a node once, then appends every node the walk did not
// reach in first-arrival order. A child ID with no node is skipped.
func (e *stackEntry) readOrder() []acpwire.WireNode {
	ordered := make([]acpwire.WireNode, 0, len(e.nodes))
	visited := make(map[string]bool, len(e.nodes))
	var walk func(id string)
	walk = func(id string) {
		if visited[id] {
			return
		}
		node, ok := e.nodes[id]
		if !ok {
			return
		}
		visited[id] = true
		ordered = append(ordered, node)
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, root := range e.roots {
		walk(root)
	}
	for _, id := range e.order {
		if visited[id] {
			continue
		}
		walk(id)
	}
	return ordered
}
