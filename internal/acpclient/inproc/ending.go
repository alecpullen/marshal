package inproc

import (
	"context"
	"errors"
	"fmt"
	"os"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/postmortem"
)

// EndService is the runtime's end of a conversation: the work an end would
// stop, the acceptance that freezes it, the settlement that stops and joins
// it, and the exit analysis.
type EndService interface {
	ActiveWork() acpwire.EndWork
	AcceptEnd(params acpwire.EndAcceptParams) (acpwire.EndDecision, error)
	WaitForEnd(ctx context.Context) error
	RunExitAnalysis(ctx context.Context, goal string) error
}

var (
	errNoExitAnalysis = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no exit analysis"}
	errNoNewSession   = errors.New("inproc: new sessions are not available")
)

// ActiveWork is the work an end of the session would stop. A closed session's
// work settled before it closed, so empty is its final value; a session with no
// end service has no work to warn about.
func (s *Session) ActiveWork() acpwire.EndWork {
	if s.closed.Load() || s.end == nil {
		return acpwire.EndWork{}
	}
	return s.end.ActiveWork()
}

// AcceptEnd accepts an end of the session for params' intent. A session with no
// end service has nothing to check or freeze, so it accepts at once.
func (s *Session) AcceptEnd(ctx context.Context, params acpwire.EndAcceptParams) (acpwire.EndDecision, error) {
	if s.closed.Load() {
		return acpwire.EndDecision{}, errSessionClosed
	}
	if s.end == nil {
		return acpwire.EndDecision{Accepted: true}, nil
	}
	return s.end.AcceptEnd(params)
}

// WaitForEnd stops and joins the accepted end's work. A session with no end
// service has no work to settle.
func (s *Session) WaitForEnd(ctx context.Context) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if s.end == nil {
		return nil
	}
	return s.end.WaitForEnd(ctx)
}

// RunExitAnalysis runs the exit postmortem's agent pass with goal. A session
// with no end service has no runtime to run it on.
func (s *Session) RunExitAnalysis(ctx context.Context, goal string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if s.end == nil {
		return errNoExitAnalysis
	}
	return s.end.RunExitAnalysis(ctx, goal)
}

// Postmortem writes the session's postmortem report and returns the path it
// wrote. It reports the same errors writePostmortemFor does, so the seam and
// the TUI's exit path name the same failure.
func (s *Session) Postmortem(ctx context.Context) (string, error) {
	if s.closed.Load() {
		return "", errSessionClosed
	}
	report, err := postmortem.Build(s.state, s.state.DB())
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	slug := postmortem.ProjectSlug(s.state.WorkingDir)
	return postmortem.Write(report, home, slug, s.state.SessionID())
}

// Close ends the session: it cancels the run in flight, settles every pending
// request as shut down, and shuts the session down.
//
// Close stops no command group: the accepted end's settlement already did. The
// state's shutdown and the change signal come after the slot's cancel, and the
// signal is closed outside the lock because closeSignal takes it.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		return nil
	}
	s.closed.Store(true)
	s.paused = nil
	slot := s.turn
	if slot != nil {
		slot.stopped = true
	}
	s.mu.Unlock()

	if slot != nil {
		slot.cancel()
	}
	s.state.ResolvePendingForShutdown()
	s.state.Shutdown()
	s.cancel()
	s.closeSignal()
	return nil
}

// NewSession creates a session in the runtime and makes it the current one. A
// runtime with no session factory has none to create, and a failure leaves the
// current session as it was.
func (h *Host) NewSession(ctx context.Context, name string) (acpclient.Session, error) {
	h.mu.Lock()
	newSession := h.deps.NewSession
	h.mu.Unlock()
	if newSession == nil {
		return nil, errNoNewSession
	}
	state, err := newSession(name)
	if err != nil {
		return nil, err
	}
	return h.swap(state), nil
}
