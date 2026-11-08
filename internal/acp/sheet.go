package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
)

// sheetInterval is how often a sheet forwarder compares each subscribed
// sheet with the last one it sent (P3 R3).
const sheetInterval = 50 * time.Millisecond

// SheetManagerConfig wires one connection's sheet handlers.
type SheetManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// Notify sends sheet updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// SheetManager serves session/sheet for one connection, and runs one sheet
// forwarder per session a marshal client reads a sheet of.
type SheetManager struct {
	lookup  func(sessionID string) (*service.Session, bool)
	notify  NotifyFunc
	marshal func() bool

	mu         sync.Mutex
	forwarders map[string]*sheetForwarder
	closed     bool

	// fences holds this connection's live sheet forwarders, by session, so a
	// response barrier can confirm their updates are written (P3 R2).
	fences fenceSet
}

// sheetEnvelope is the session/update body a sheet update travels in. It is a
// dedicated struct, not SessionUpdateParams, because the latter's Update is a
// map[string]any: a sheet carried through a map would not keep its typed
// fields.
type sheetEnvelope struct {
	SessionID string              `json:"sessionId"`
	Update    acpwire.SheetUpdate `json:"update"`
}

// sheetForwarder tracks one session's sheet forwarder goroutine.
type sheetForwarder struct {
	cancel context.CancelFunc
	fence  *fence

	// mu guards subs, the subagent IDs this connection reads, each with the
	// encoding of the last sheet sent for it (nil before the first send).
	// A forwarder never sends while holding it.
	mu   sync.Mutex
	subs map[int64][]byte
}

// NewSheetManager builds one connection's sheet handlers.
func NewSheetManager(cfg SheetManagerConfig) *SheetManager {
	return &SheetManager{
		lookup:     cfg.Lookup,
		notify:     cfg.Notify,
		marshal:    cfg.Marshal,
		forwarders: map[string]*sheetForwarder{},
	}
}

// Sheet handles session/sheet: it returns the sheet of the subagent the call
// names, after the requested refresh, and for a marshal client it subscribes
// the connection to that sheet (P3 R3).
func (m *SheetManager) Sheet(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SheetParams
	if err := decodeParams(params, &p, acpwire.MethodSheet); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if p.Refresh != nil {
		// The service's error travels as it is, so its code reaches the client
		// as error data.
		if err := sess.RefreshSheet(ctx, *p.Refresh); err != nil {
			return nil, err
		}
	}
	if m.isMarshal() {
		m.subscribe(p.SessionID, p.SubagentID)
	}
	return sess.Sheet(p.SubagentID), nil
}

// Release drops the subscription to one subagent's sheet. The session's own
// sheet (0) is never released.
func (m *SheetManager) Release(sessionID string, subagentID int64) {
	if subagentID == 0 {
		return
	}
	m.mu.Lock()
	f := m.forwarders[sessionID]
	m.mu.Unlock()
	if f == nil {
		return
	}
	f.mu.Lock()
	delete(f.subs, subagentID)
	f.mu.Unlock()
}

// Close stops every forwarder. The connection's cleanup calls it.
func (m *SheetManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	cancels := make([]context.CancelFunc, 0, len(m.forwarders))
	for id, f := range m.forwarders {
		cancels = append(cancels, f.cancel)
		delete(m.forwarders, id)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// fenceSession waits until the session's sheet forwarder has sent every
// subscribed sheet as it is now, where it differs from the last one sent.
func (m *SheetManager) fenceSession(ctx context.Context, sessionID string) {
	m.fences.wait(ctx, sessionID)
}

// isMarshal reports whether the connection's client declared the marshal
// capability.
func (m *SheetManager) isMarshal() bool {
	return m.marshal != nil && m.marshal()
}

// service resolves a session's client-seam service. An empty id is invalid
// params and an unknown session is a server error.
func (m *SheetManager) service(sessionID string) (*service.Session, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, invalidParamsError("sessionId is required")
	}
	sess, ok := m.lookupService(sessionID)
	if !ok || sess == nil {
		return nil, serverErrorf("unknown session: %s", sessionID)
	}
	return sess, nil
}

// lookupService resolves a session's service without reporting why it failed.
func (m *SheetManager) lookupService(sessionID string) (*service.Session, bool) {
	if m.lookup == nil {
		return nil, false
	}
	return m.lookup(sessionID)
}

// subscribe adds subagentID to the session's subscription set, starting the
// session's forwarder on the first subscription. A subscription to an ID the
// forwarder already holds changes nothing.
func (m *SheetManager) subscribe(sessionID string, subagentID int64) {
	f := m.ensureForwarder(sessionID)
	if f == nil {
		return
	}
	f.mu.Lock()
	if _, ok := f.subs[subagentID]; !ok {
		f.subs[subagentID] = nil
	}
	f.mu.Unlock()
}

// ensureForwarder starts one sheet forwarder for a session, unless the
// connection is closed or the session already has one. Its fence is registered
// before its goroutine starts, so a barrier that follows cannot miss it.
func (m *SheetManager) ensureForwarder(sessionID string) *sheetForwarder {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if f, ok := m.forwarders[sessionID]; ok {
		return f
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &sheetForwarder{cancel: cancel, fence: newFence(), subs: map[int64][]byte{}}
	m.forwarders[sessionID] = f
	m.fences.add(sessionID, f.fence)
	go m.runForwarder(sessionID, f, ctx)
	return f
}

// runForwarder keeps the session's subscribed sheets current: every
// sheetInterval, and whenever its fence is asked, it reads each subscribed
// sheet and sends the ones whose encoding differs from the last one sent. It
// stops when the connection closes or the session goes away.
func (m *SheetManager) runForwarder(sessionID string, f *sheetForwarder, ctx context.Context) {
	defer func() {
		m.mu.Lock()
		// Retire only this forwarder's own registration. A later forwarder for
		// the same session must keep the map entry its subscriptions are on.
		if m.forwarders[sessionID] == f {
			delete(m.forwarders, sessionID)
		}
		m.mu.Unlock()
		m.fences.remove(sessionID, f.fence)
		f.fence.close()
	}()

	// send resolves the service, then sends every subscribed sheet whose
	// encoding differs from the last one sent for it. It reports whether the
	// session still exists.
	send := func() bool {
		// Resolve the service on every pass, so a runtime replaced on load or
		// resume is followed.
		sess, ok := m.lookupService(sessionID)
		if !ok || sess == nil {
			return false
		}
		for _, id := range f.ids() {
			sheet := sess.Sheet(id)
			encoded, err := json.Marshal(sheet)
			if err != nil {
				continue
			}
			if !f.changed(id, encoded) {
				continue
			}
			// Never send under the mutex: the sink belongs to the connection.
			if err := m.notify(acpwire.MethodSessionUpdate, sheetEnvelope{
				SessionID: sessionID,
				Update: acpwire.SheetUpdate{
					Kind:       acpwire.UpdateSheet,
					SubagentID: id,
					Sheet:      sheet,
				},
			}); err != nil {
				continue
			}
			f.record(id, encoded)
		}
		return true
	}

	ticker := time.NewTicker(sheetInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		case answer := <-f.fence.requests():
			alive := send()
			close(answer)
			if !alive {
				return
			}
		}
	}
}

// ids copies the subscribed subagent IDs in ascending order.
func (f *sheetForwarder) ids() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, 0, len(f.subs))
	for id := range f.subs {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// changed reports whether encoded differs from the last sheet sent for id. A
// released ID counts as unchanged, so a release mid-pass sends nothing.
func (f *sheetForwarder) changed(id int64, encoded []byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	last, ok := f.subs[id]
	if !ok {
		return false
	}
	return !bytes.Equal(last, encoded)
}

// record keeps encoded as the last sheet sent for id, when the ID is still
// subscribed.
func (f *sheetForwarder) record(id int64, encoded []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[id]; ok {
		f.subs[id] = encoded
	}
}

// callersHandler serves session/callers through the session's service.
func callersHandler(lookup func(sessionID string) (*service.Session, bool)) Handler {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p acpwire.CallersParams
		if err := decodeParams(params, &p, acpwire.MethodCallers); err != nil {
			return nil, invalidParamsError("%v", err)
		}
		if strings.TrimSpace(p.SessionID) == "" {
			return nil, invalidParamsError("sessionId is required")
		}
		if strings.TrimSpace(p.NodeID) == "" {
			return nil, invalidParamsError("nodeId is required")
		}
		var sess *service.Session
		if lookup != nil {
			var ok bool
			sess, ok = lookup(p.SessionID)
			if !ok {
				sess = nil
			}
		}
		if sess == nil {
			return nil, serverErrorf("unknown session: %s", p.SessionID)
		}
		refs, err := sess.Callers(ctx, p.NodeID)
		if err != nil {
			return nil, err
		}
		return acpwire.CallersResult{Refs: refs}, nil
	}
}

// registerTranscript registers session/stack, session/stack_node,
// session/stack_busy, session/stack_unsubscribe (which also releases the
// subagent's sheet), session/sheet and session/callers on srv.
func registerTranscript(srv *Server, turns *TurnManager, sheets *SheetManager, lookup func(sessionID string) (*service.Session, bool)) {
	srv.Handle(acpwire.MethodStack, turns.Stack)
	srv.Handle(acpwire.MethodStackNode, turns.StackNode)
	srv.Handle(acpwire.MethodStackBusy, turns.StackBusy)
	srv.Handle(acpwire.MethodSheet, sheets.Sheet)
	srv.Handle(acpwire.MethodCallers, callersHandler(lookup))
	srv.Handle(acpwire.MethodStackUnsubscribe, func(ctx context.Context, params json.RawMessage) (any, error) {
		// Decode once, before the release: a decode that ran after the stack
		// was already gone would report invalid params for a call whose
		// subscription had been dropped.
		var p acpwire.StackParams
		if err := decodeParams(params, &p, acpwire.MethodStackUnsubscribe); err != nil {
			return nil, invalidParamsError("%v", err)
		}
		res, err := turns.StackUnsubscribe(ctx, params)
		// The stack is gone either way, so this connection no longer reads its
		// sheet: a refused unsubscribe must not leave the forwarder polling a
		// subtree the client has already forgotten.
		sheets.Release(p.SessionID, p.SubagentID)
		if err != nil {
			return nil, err
		}
		return res, nil
	})
}
