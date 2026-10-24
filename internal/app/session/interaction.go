package session

import (
	"sync"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

// BeginExitAnalysisAccess grants Act + System for the runtime's open
// postmortem epoch. Ordinary work stays frozen. The returned cleanup restores
// the prior tuple even if shutdown begins while the pass is running.
func (s *State) BeginExitAnalysisAccess(scope *ExitAnalysisScope) (func(), error) {
	if s == nil || s.interactionOwner != nil {
		return nil, ErrNotPostmortemPhase
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if s.WorkPhase() != ExitPhasePostmortem || !s.ExitAnalysisOpen(scope) {
		return nil, ErrNotPostmortemPhase
	}
	if s.quiescingLocked() {
		return nil, ErrSessionQuiescing
	}
	if s.exitAnalysisAccess != nil {
		return nil, ErrExitAnalysisAlreadyOpen
	}
	previous := s.access
	next := previous
	next.Mode, next.System = interaction.ModeAct, true
	s.exitAnalysisAccess = scope
	if next != previous {
		next.Revision++
		s.access = next
		s.signalInteractionLocked()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.accessMu.Lock()
			defer s.accessMu.Unlock()
			s.exitAnalysisAccess = nil
			if s.access.Mode != previous.Mode || s.access.Auto != previous.Auto || s.access.System != previous.System {
				previous.Revision = s.access.Revision + 1
				s.access = previous
				s.signalInteractionLocked()
			}
		})
	}, nil
}

// InteractionChanged subscribes before a caller reads the live tuple. Closing
// the channel wakes pending prompts when a manual change or reload enables Auto.
func (s *State) InteractionChanged() <-chan struct{} {
	owner := s.ownerState()
	owner.accessMu.Lock()
	defer owner.accessMu.Unlock()
	if owner.accessChanged == nil {
		owner.accessChanged = make(chan struct{})
	}
	return owner.accessChanged
}

func (s *State) signalInteractionLocked() {
	if s.accessChanged != nil {
		close(s.accessChanged)
		s.accessChanged = nil
	}
}

// InteractionState returns the canonical effective tuple as one value read
// under a single lock, so a reader never sees a half-applied pair.
func (s *State) InteractionState() interaction.State {
	if s.interactionOwner != nil {
		return s.InteractionAuthority().Snapshot().Access
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	return s.access
}

// accessStateLocked returns the effective tuple as an AccessState. Callers
// must hold accessMu; it exists so the access gate can read the pair without
// re-entering the non-reentrant mutex. Mode carries the canonical value.
func (s *State) accessStateLocked() AccessState {
	return AccessState{
		Mode:     string(s.access.Mode),
		Auto:     s.access.Auto,
		System:   s.access.System,
		Revision: s.access.Revision,
	}
}

// ApplyInteraction applies one validated patch atomically and returns the
// committed tuple. A no-op patch does not increment the revision; a real
// change increments it exactly once. A mode/Auto patch records the whole pair
// as the session override.
func (s *State) ApplyInteraction(p interaction.Patch) (interaction.State, error) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if s.exitAnalysisAccess != nil {
		return s.access, ErrConversationEnding
	}
	// The quiescing gate is checked under workMu (never the other way round,
	// which would invert the documented lock order) so a change cannot land
	// during shutdown.
	s.workMu.Lock()
	quiescing := s.quiescing
	s.workMu.Unlock()
	if quiescing {
		return s.access, ErrSessionQuiescing
	}
	next, err := interaction.Apply(s.access, p)
	if err != nil {
		return s.access, err
	}
	if next == s.access {
		return s.access, nil
	}
	next.Revision = s.access.Revision + 1
	s.access = next
	s.signalInteractionLocked()
	if p.Mode != nil || p.Auto != nil {
		ov := interaction.Defaults{Mode: next.Mode, Auto: next.Auto}
		s.accessOverride = &ov
	}
	return next, nil
}

// InteractionForConfig reports the tuple the session would run with for an
// incoming configuration without mutating anything. The session override
// applies only while the configured pair is unchanged.
func (s *State) InteractionForConfig(cfg config.Config) (interaction.State, error) {
	d, err := cfg.InteractionDefaults()
	if err != nil {
		return interaction.State{}, err
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if s.exitAnalysisAccess != nil {
		return interaction.State{}, ErrConversationEnding
	}
	st := interaction.State{Mode: d.Mode, Auto: d.Auto, System: s.access.System, Revision: s.access.Revision}
	if d == s.configuredAccess && s.accessOverride != nil {
		st.Mode = s.accessOverride.Mode
		st.Auto = s.accessOverride.Auto
	}
	st.System = st.System || st.Auto
	return st, nil
}

// RebaseInteractionConfig installs a reloaded configuration and re-derives the
// effective tuple. An unchanged configured pair keeps the session override; a
// changed pair clears it. System is preserved either way. Validation runs
// before any mutation, so a failed rebase leaves everything unchanged.
func (s *State) RebaseInteractionConfig(cfg config.Config) (interaction.State, error) {
	d, err := cfg.InteractionDefaults()
	if err != nil {
		return s.InteractionState(), err
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if s.exitAnalysisAccess != nil {
		return s.access, ErrConversationEnding
	}
	if d != s.configuredAccess {
		s.accessOverride = nil
	}
	s.configuredAccess = d
	next := interaction.State{Mode: d.Mode, Auto: d.Auto, System: s.access.System, Revision: s.access.Revision + 1}
	if s.accessOverride != nil {
		next.Mode = s.accessOverride.Mode
		next.Auto = s.accessOverride.Auto
	}
	next.System = next.System || next.Auto
	s.access = next
	s.signalInteractionLocked()
	s.mu.Lock()
	s.Config = cfg
	s.mu.Unlock()
	return next, nil
}

// InvalidateInteraction increments the access revision without changing the
// tuple. Workspace and session-rule changes use it so a stale admission
// cannot cross the change.
func (s *State) InvalidateInteraction() interaction.State {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	s.access.Revision++
	s.signalInteractionLocked()
	return s.access
}
