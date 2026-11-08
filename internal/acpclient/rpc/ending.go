package rpc

import (
	"context"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// endCallTimeout bounds the first read's session/end_work. ActiveWork takes no
// context, so it needs a bound of its own.
const endCallTimeout = 5 * time.Second

// endMirror is the session's mirror of the work an end would stop: one entry,
// created by its first read and refreshed by each end_work update.
type endMirror struct {
	work acpwire.EndWork
	// updated says an update arrived, so the first read's result, which the
	// agent computed before sending it, is stale.
	updated bool
}

// ActiveWork is the work an end would stop, oldest first: no I/O once the
// mirror holds a value, and never a read the agent has to answer. Its slices
// are shared with the mirror, so a caller must not change them.
func (s *Session) ActiveWork() acpwire.EndWork {
	entry, err := s.endEntry()
	if err != nil {
		return acpwire.EndWork{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return entry.work
}

// endEntry returns the end mirror, fetching it when this is its first read. On
// a failed read the entry is removed, so the next read asks again.
func (s *Session) endEntry() (*endMirror, error) {
	s.mu.Lock()
	if entry := s.end; entry != nil {
		s.mu.Unlock()
		return entry, nil
	}
	// The entry is installed before the call goes out, so an update that
	// arrives while the call waits is applied instead of dropped.
	entry := &endMirror{}
	s.end = entry
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), endCallTimeout)
	defer cancel()
	var work acpwire.EndWork
	params := acpwire.ControlParams{SessionID: s.id}
	if err := s.conn.Call(ctx, acpwire.MethodEndWork, params, &work); err != nil {
		// The entry goes even when an update arrived first: a later read asks
		// again, where keeping it would answer every later read with this
		// error for good.
		s.mu.Lock()
		if s.end == entry {
			s.end = nil
		}
		s.mu.Unlock()
		return nil, err
	}

	s.installEnd(entry, work)
	return entry, nil
}

// installEnd sets the mirrored membership from session/end_work's result,
// unless an update already arrived: that update is newer than the result the
// agent computed before sending it.
func (s *Session) installEnd(entry *endMirror, work acpwire.EndWork) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end != entry || entry.updated {
		return
	}
	entry.work = work
}

// applyEnd replaces the mirrored membership with an update's and fires Changed.
// An update for a session that has not read its membership is dropped: the
// first read asks the agent, and gets it. It runs on the connection's read
// goroutine and never blocks on it.
func (s *Session) applyEnd(work acpwire.EndWork) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end == nil {
		return
	}
	s.end.work = work
	s.end.updated = true
	s.signalLocked()
}

// AcceptEnd accepts one end of the session and returns its outcome: accepted,
// or the membership to show and ask about again.
func (s *Session) AcceptEnd(ctx context.Context, params acpwire.EndAcceptParams) (acpwire.EndDecision, error) {
	call := acpwire.EndAcceptCallParams{SessionID: s.id, Accept: params}
	var result acpwire.EndDecision
	if err := s.conn.Call(ctx, acpwire.MethodEndAccept, call, &result); err != nil {
		return acpwire.EndDecision{}, err
	}
	return result, nil
}

// WaitForEnd waits for the session's end to settle.
func (s *Session) WaitForEnd(ctx context.Context) error {
	params := acpwire.ControlParams{SessionID: s.id}
	return s.conn.Call(ctx, acpwire.MethodEndWait, params, nil)
}

// RunExitAnalysis runs the session's exit analysis over the goal.
func (s *Session) RunExitAnalysis(ctx context.Context, goal string) error {
	params := acpwire.ExitAnalysisParams{SessionID: s.id, Goal: goal}
	return s.conn.Call(ctx, acpwire.MethodExitAnalysis, params, nil)
}

// Close ends the session's conversation and retires it: after a successful
// close the session is unregistered from its host and its Changed is closed,
// so its reads answer the mirror's final values and its calls reach the agent,
// which answers CodeSessionClosed (decision 6). An agent that reports the
// session already closed retires it too, and any other error leaves it as it
// was. Closing a session that is already closed sends nothing.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil
	}

	params := acpwire.ControlParams{SessionID: s.id}
	if err := s.conn.Call(ctx, acpwire.MethodSessionClose, params, nil); err != nil {
		if !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
			return err
		}
	}
	if s.host != nil {
		s.host.retire(s)
	}
	return nil
}

// Postmortem returns the path of the session's exit postmortem report.
func (s *Session) Postmortem(ctx context.Context) (string, error) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.PostmortemResult
	if err := s.conn.Call(ctx, acpwire.MethodPostmortem, params, &result); err != nil {
		return "", err
	}
	return result.Path, nil
}
