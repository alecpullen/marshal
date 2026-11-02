package inproc

import (
	"context"
	"errors"

	"marshal/internal/acpwire"
	"marshal/internal/viewmodel"
)

// errNoWork is what a work call returns on a session that serves no work.
var errNoWork = errors.New("inproc: the session serves no work")

// Work is the session's live work projection (C8), and whether the session
// serves work at all. It does not block. A closed session keeps answering with
// the newest snapshot it kept (C4).
func (s *Session) Work() (acpwire.WorkSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workSnap, s.work != nil
}

// ListWork lists one page of the session's attention or history view. A cursor
// older than the work history comes back as CodeStaleWorkCursor.
func (s *Session) ListWork(ctx context.Context, query acpwire.WorkQuery) (acpwire.WorkPage, error) {
	if s.closed.Load() {
		return acpwire.WorkPage{}, errSessionClosed
	}
	if s.work == nil {
		return acpwire.WorkPage{}, errNoWork
	}
	page, err := s.work.ListWork(ctx, viewmodel.WorkQuery{
		ConversationID: s.state.SessionID(),
		View:           query.View,
		Query:          query.Query,
		Cursor:         query.Cursor,
		Limit:          query.Limit,
	})
	if err != nil {
		if errors.Is(err, viewmodel.ErrWorkStaleCursor) {
			return acpwire.WorkPage{}, &acpwire.Error{
				Code:    acpwire.CodeStaleWorkCursor,
				Message: err.Error(),
			}
		}
		return acpwire.WorkPage{}, err
	}
	return viewmodel.WorkPageOf(page), nil
}

// InspectWork returns the bounded detail of the item with that task key. raw
// asks for the output as captured, without display cleaning.
func (s *Session) InspectWork(ctx context.Context, taskKey string, raw bool) (acpwire.WorkDetail, error) {
	if s.closed.Load() {
		return acpwire.WorkDetail{}, errSessionClosed
	}
	if s.work == nil {
		return acpwire.WorkDetail{}, errNoWork
	}
	detail, err := s.work.InspectWork(ctx, viewmodel.WorkTarget{
		ConversationID: s.state.SessionID(),
		TaskKey:        taskKey,
	}, raw)
	return viewmodel.WorkDetailOf(detail), err
}

// StopWork asks the owner of the item with that task key to stop it, giving
// reason. The result describes the request, and comes back with the error when
// the request is refused, so a refusal's summary reaches the caller.
func (s *Session) StopWork(ctx context.Context, taskKey, reason string) (acpwire.WorkStopResult, error) {
	if s.closed.Load() {
		return acpwire.WorkStopResult{}, errSessionClosed
	}
	if s.work == nil {
		return acpwire.WorkStopResult{}, errNoWork
	}
	result, err := s.work.StopWork(ctx, viewmodel.WorkTarget{
		ConversationID: s.state.SessionID(),
		TaskKey:        taskKey,
	}, reason)
	return viewmodel.WorkStopResultOf(result), err
}

// seedWork keeps the service's current snapshot and binds its conversation.
func (s *Session) seedWork(snap viewmodel.WorkSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workConv = snap.ConversationID
	s.workSnap = viewmodel.WorkSnapshotOf(snap)
}

// absorbWork keeps snap unless it names another conversation than the bound
// one, or is older than the kept snapshot of the same conversation. It reports
// whether it kept snap.
func (s *Session) absorbWork(snap viewmodel.WorkSnapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workConv != "" && s.workConv != snap.ConversationID {
		return false
	}
	if snap.ConversationID == s.workConv && snap.Revision < s.workSnap.Revision {
		return false
	}
	s.workSnap = viewmodel.WorkSnapshotOf(snap)
	return true
}
