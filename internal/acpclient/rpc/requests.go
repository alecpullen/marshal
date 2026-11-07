package rpc

import (
	"context"
	"time"

	"marshal/internal/acpwire"
)

// pendingCallTimeout bounds the first read's session/pending. Pending takes no
// context, so it needs a bound of its own.
const pendingCallTimeout = 5 * time.Second

// pendingMirror is the session's mirror of its open requests: one entry,
// created by its first read and refreshed by each pending_requests update.
type pendingMirror struct {
	state acpwire.PendingState
	// updated says an update arrived, so the first read's result, which the
	// agent computed before sending it, is stale.
	updated bool
	// ready is closed once the first read's call has settled, and err is its
	// outcome, so a caller that arrives while the call is in flight waits for
	// it instead of sending a second session/pending.
	ready chan struct{}
	err   error
}

// settle records the first read's outcome and releases every caller waiting
// on it.
func (p *pendingMirror) settle(err error) {
	p.err = err
	close(p.ready)
}

// Pending is the mirrored open requests: no I/O once the mirror holds a value,
// and never a read the agent has to answer. Its slices are shared with the
// mirror, so a caller must not change them.
func (s *Session) Pending() acpwire.PendingRequests {
	ctx, cancel := context.WithTimeout(context.Background(), pendingCallTimeout)
	defer cancel()
	entry, err := s.pendingEntry(ctx)
	if err != nil {
		return acpwire.PendingRequests{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return entry.state.Requests
}

// ReconcileAccess reads the mirror as Pending does, fetching it with ctx when
// this is the mirror's first read, and returns the typed access request it
// holds.
func (s *Session) ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool) {
	entry, err := s.pendingEntry(ctx)
	if err != nil {
		return acpwire.AccessRequest{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.state.Access == nil {
		return acpwire.AccessRequest{}, false
	}
	return *entry.state.Access, true
}

// pendingEntry returns the request mirror, fetching it with ctx when this is
// its first read. On a failed read the entry is removed, so the next read asks
// again, as a failed Attach's is.
func (s *Session) pendingEntry(ctx context.Context) (*pendingMirror, error) {
	s.mu.Lock()
	if entry := s.pending; entry != nil {
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
	entry := &pendingMirror{ready: make(chan struct{})}
	s.pending = entry
	s.mu.Unlock()

	var state acpwire.PendingState
	params := acpwire.PendingParams{SessionID: s.id}
	if err := s.conn.Call(ctx, acpwire.MethodPending, params, &state); err != nil {
		// The entry goes even when an update arrived first: a later read asks
		// again, where keeping it would answer every later read with this
		// error for good.
		s.mu.Lock()
		if s.pending == entry {
			s.pending = nil
		}
		s.mu.Unlock()
		entry.settle(err)
		return nil, err
	}

	s.installPending(entry, state)
	entry.settle(nil)
	return entry, nil
}

// installPending sets the mirrored state from session/pending's result, unless
// an update already arrived: that update is newer than the result the agent
// computed before sending it.
func (s *Session) installPending(entry *pendingMirror, state acpwire.PendingState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != entry || entry.updated {
		return
	}
	entry.state = state
}

// applyPending replaces the mirrored state with an update's and fires Changed.
// An update for a session that has not read its requests is dropped: the first
// read asks the agent, and gets it. It runs on the connection's read goroutine
// and never blocks on it.
func (s *Session) applyPending(state acpwire.PendingState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return
	}
	s.pending.state = state
	s.pending.updated = true
	s.signalLocked()
}

// AnswerApproval answers the approval with that key.
func (s *Session) AnswerApproval(ctx context.Context, key string, answer acpwire.ApprovalAnswer) error {
	params := acpwire.AnswerApprovalParams{SessionID: s.id, Key: key, Answer: answer}
	return s.conn.Call(ctx, acpwire.MethodAnswerApproval, params, nil)
}

// AnswerQuestion answers the question with that key, one answer per question
// in order.
func (s *Session) AnswerQuestion(ctx context.Context, key string, answers []acpwire.Answer) error {
	params := acpwire.AnswerQuestionParams{SessionID: s.id, Key: key, Answers: answers}
	return s.conn.Call(ctx, acpwire.MethodAnswerQuestion, params, nil)
}

// AnswerSkillGate answers the skill gate with that key.
func (s *Session) AnswerSkillGate(ctx context.Context, key string, choice acpwire.SkillGateChoice) error {
	params := acpwire.AnswerSkillGateParams{SessionID: s.id, Key: key, Choice: choice}
	return s.conn.Call(ctx, acpwire.MethodAnswerSkillGate, params, nil)
}

// AnswerSudo answers the sudo prompt with that key. The password travels only
// in its params, and the caller's slice is zeroed when the call returns,
// whatever its outcome.
func (s *Session) AnswerSudo(ctx context.Context, key string, answer acpwire.SudoAnswer) error {
	defer zeroBytes(answer.Password)
	params := acpwire.AnswerSudoParams{SessionID: s.id, Key: key, Answer: answer}
	return s.conn.Call(ctx, acpwire.MethodAnswerSudo, params, nil)
}

// AnswerAccess answers the access request the choice names and returns how it
// settled.
func (s *Session) AnswerAccess(ctx context.Context, choice acpwire.AccessChoice) (acpwire.AccessResult, error) {
	params := acpwire.AnswerAccessParams{SessionID: s.id, Choice: choice}
	var result acpwire.AccessResult
	if err := s.conn.Call(ctx, acpwire.MethodAnswerAccess, params, &result); err != nil {
		return acpwire.AccessResult{}, err
	}
	return result, nil
}

// Rollback restores the last backup and logs the rollback as an audit row in
// the transcript.
func (s *Session) Rollback(ctx context.Context) error {
	params := acpwire.PendingParams{SessionID: s.id}
	return s.conn.Call(ctx, acpwire.MethodRollback, params, nil)
}

// zeroBytes overwrites every byte of b. The slice is the caller's, and it
// escaped into the call's params, so the stores cannot be elided.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
