package rpc

import (
	"context"
	"time"

	"marshal/internal/acpwire"
)

// workCallTimeout bounds the first read's session/work_list. Work takes no
// context, so it needs a bound of its own.
const workCallTimeout = 5 * time.Second

// workMirror is the session's mirror of its work: one entry, created by its
// first read and refreshed by each work_snapshot update.
type workMirror struct {
	state acpwire.WorkState
	// updated says an update arrived, so the first read's result, which the
	// agent computed before sending it, is stale.
	updated bool
	// ready is closed once the first read's call has settled, and err is its
	// outcome, so a caller that arrives while the call is in flight waits for
	// it instead of sending a second session/work_list.
	ready chan struct{}
	err   error
}

// settle records the first read's outcome and releases every caller waiting on
// it.
func (w *workMirror) settle(err error) {
	w.err = err
	close(w.ready)
}

// Work is the mirrored work projection, and whether the session serves work at
// all: no I/O once the mirror holds a value, and never a read the agent has to
// answer. Its slices are shared with the mirror, so a caller must not change
// them.
func (s *Session) Work() (acpwire.WorkSnapshot, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), workCallTimeout)
	defer cancel()
	entry, err := s.workEntry(ctx)
	if err != nil {
		return acpwire.WorkSnapshot{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return entry.state.Snapshot, entry.state.Exists
}

// workEntry returns the work mirror, fetching it with ctx when this is its
// first read. On a failed read the entry is removed, so the next read asks
// again, as a failed Pending's is.
func (s *Session) workEntry(ctx context.Context) (*workMirror, error) {
	s.mu.Lock()
	if entry := s.work; entry != nil {
		s.mu.Unlock()
		select {
		case <-entry.ready:
			if entry.err != nil {
				return nil, entry.err
			}
			return entry, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// The entry is installed before the call goes out, so an update that
	// arrives while the call waits is applied instead of dropped.
	entry := &workMirror{ready: make(chan struct{})}
	s.work = entry
	s.mu.Unlock()

	var state acpwire.WorkState
	params := acpwire.WorkListParams{SessionID: s.id}
	if err := s.conn.Call(ctx, acpwire.MethodWorkList, params, &state); err != nil {
		// The entry goes even when an update arrived first: a later read asks
		// again, where keeping it would answer every later read with this
		// error for good.
		s.mu.Lock()
		if s.work == entry {
			s.work = nil
		}
		s.mu.Unlock()
		entry.settle(err)
		return nil, err
	}

	s.installWork(entry, state)
	entry.settle(nil)
	return entry, nil
}

// installWork sets the mirrored state from session/work_list's result, unless
// an update already arrived: that update is newer than the result the agent
// computed before sending it.
func (s *Session) installWork(entry *workMirror, state acpwire.WorkState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.work != entry || entry.updated {
		return
	}
	entry.state = state
}

// applyWork replaces the mirrored state with an update's and fires Changed. An
// update for a session that has not read its work is dropped: the first read
// asks the agent, and gets it. It runs on the connection's read goroutine and
// never blocks on it.
func (s *Session) applyWork(state acpwire.WorkState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.work == nil {
		return
	}
	s.work.state = state
	s.work.updated = true
	s.signalLocked()
}

// ListWork lists one page of the session's attention or history view.
func (s *Session) ListWork(ctx context.Context, query acpwire.WorkQuery) (acpwire.WorkPage, error) {
	params := acpwire.WorkListParams{
		SessionID: s.id,
		View:      query.View,
		Query:     query.Query,
		Cursor:    query.Cursor,
		Limit:     query.Limit,
	}
	var result acpwire.WorkPage
	if err := s.conn.Call(ctx, acpwire.MethodWorkList, params, &result); err != nil {
		return acpwire.WorkPage{}, err
	}
	return result, nil
}

// InspectWork returns the bounded detail of the item with that task key. raw
// asks for the output as captured, without display cleaning.
func (s *Session) InspectWork(ctx context.Context, taskKey string, raw bool) (acpwire.WorkDetail, error) {
	params := acpwire.WorkTargetParams{SessionID: s.id, TaskKey: taskKey, Raw: raw}
	var result acpwire.WorkDetail
	if err := s.conn.Call(ctx, acpwire.MethodWorkInspect, params, &result); err != nil {
		return acpwire.WorkDetail{}, err
	}
	return result, nil
}

// StopWork asks the owner of the item with that task key to stop it, giving
// reason.
func (s *Session) StopWork(ctx context.Context, taskKey, reason string) (acpwire.WorkStopResult, error) {
	params := acpwire.WorkTargetParams{SessionID: s.id, TaskKey: taskKey, Reason: reason}
	var result acpwire.WorkStopResult
	if err := s.conn.Call(ctx, acpwire.MethodWorkStop, params, &result); err != nil {
		return acpwire.WorkStopResult{}, err
	}
	return result, nil
}
