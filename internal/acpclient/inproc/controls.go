package inproc

import (
	"context"
	"errors"

	"marshal/internal/acpwire"
	"marshal/internal/interaction"
)

// errNoInteraction and errNoEffort are what SetInteraction and SetEffort
// return when Deps carried no setter.
var (
	errNoInteraction = errors.New("inproc: no interaction setter")
	errNoEffort      = errors.New("inproc: no effort setter")
)

// Steer queues text for the running turn to read at its next step. In process
// it queues even when no turn runs.
func (s *Session) Steer(ctx context.Context, text string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.state.PushSteering(text)
	return nil
}

// PopSteering removes and returns the oldest queued steering text. It reports
// false when the queue is empty, and on a closed session.
func (s *Session) PopSteering(ctx context.Context) (string, bool) {
	if s.closed.Load() {
		return "", false
	}
	return s.state.PopSteering()
}

// ClearSteering drops the queued steering text.
func (s *Session) ClearSteering(ctx context.Context) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.state.ClearSteering()
	return nil
}

// SetInteraction applies one interaction patch and returns the committed
// state. A nil field is left alone.
func (s *Session) SetInteraction(ctx context.Context, patch acpwire.InteractionPatch) (acpwire.Interaction, error) {
	if s.closed.Load() {
		return acpwire.Interaction{}, errSessionClosed
	}
	if s.setInteraction == nil {
		return acpwire.Interaction{}, errNoInteraction
	}
	p := interaction.Patch{Auto: patch.Auto, System: patch.System}
	if patch.Mode != nil {
		mode := interaction.Mode(*patch.Mode)
		p.Mode = &mode
	}
	state, err := s.setInteraction(p)
	if err != nil {
		return acpwire.Interaction{}, err
	}
	return interactionOf(state), nil
}

// SetEffort sets the session's effort override for the active route, or clears
// it for "", and returns when the change applies.
func (s *Session) SetEffort(ctx context.Context, level string) (string, error) {
	if s.closed.Load() {
		return "", errSessionClosed
	}
	if s.setEffort == nil {
		return "", errNoEffort
	}
	return s.setEffort(level)
}

// SetSudoUnlock turns sudo's own timestamp grace on or off for the session.
func (s *Session) SetSudoUnlock(ctx context.Context, on bool) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.state.SetSudoUnlockEnabled(on)
	return nil
}

// CancelSubagent asks the running subagent with that ID to stop.
func (s *Session) CancelSubagent(ctx context.Context, subagentID int64) bool {
	if s.closed.Load() {
		return false
	}
	return s.state.CancelSubagent(subagentID)
}

// ClearFinishedRun clears a finished run's progress and its event log.
func (s *Session) ClearFinishedRun(ctx context.Context) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.state.ClearSDDProgress()
	s.state.ClearRunEvents()
	return nil
}

// TakeWatchResume takes the watch-resume latch.
func (s *Session) TakeWatchResume(ctx context.Context) (name string, repeat bool, ok bool) {
	if s.closed.Load() {
		return "", false, false
	}
	return s.state.TakeWatchResume()
}
