package session

import (
	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

// legacyApprovalMode projects the canonical tuple onto the retired five-mode
// spelling for unmigrated callers. It is a compatibility projection only.
func legacyApprovalMode(mode interaction.Mode, auto bool) string {
	switch mode {
	case interaction.ModeAsk:
		return "default"
	case interaction.ModeAct:
		if auto {
			return "auto"
		}
		return "copilot"
	default:
		return "default"
	}
}

// InteractionState returns the canonical effective tuple as one value read
// under a single lock, so a reader never sees a half-applied pair.
func (s *State) InteractionState() interaction.State {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	return s.access
}

// accessStateLocked returns the legacy projection of the effective tuple.
// Callers must hold accessMu; it exists so the access gate can read the pair
// without re-entering the non-reentrant mutex.
func (s *State) accessStateLocked() AccessState {
	return AccessState{
		Mode:     legacyApprovalMode(s.access.Mode, s.access.Auto),
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
	st := interaction.State{Mode: d.Mode, Auto: d.Auto, System: s.access.System, Revision: s.access.Revision}
	if d == s.configuredAccess && s.accessOverride != nil {
		st.Mode = s.accessOverride.Mode
		st.Auto = s.accessOverride.Auto
	}
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
	if d != s.configuredAccess {
		s.accessOverride = nil
	}
	s.configuredAccess = d
	next := interaction.State{Mode: d.Mode, Auto: d.Auto, System: s.access.System, Revision: s.access.Revision + 1}
	if s.accessOverride != nil {
		next.Mode = s.accessOverride.Mode
		next.Auto = s.accessOverride.Auto
	}
	s.access = next
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
	return s.access
}
