package session

import (
	"context"
	"errors"
	"sync"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

// ErrAdmissionStale reports that the snapshot an admission was requested for
// no longer matches the live owner state. The caller must re-evaluate and
// retry rather than execute.
var ErrAdmissionStale = errors.New("session: admission context changed")

// WithInteractionOwner binds a child actor to its ultimate owning State and an
// immutable ceiling. Ancestry is flattened and ceilings are intersected at
// construction, so no view can widen its parent.
func WithInteractionOwner(owner *State, ceiling interaction.Ceiling) Option {
	return func(s *State) {
		if owner == nil {
			return
		}
		ultimate := owner.ownerState()
		s.interactionOwner = ultimate
		s.interactionCeiling = interaction.Intersect(ceiling, ultimate.effectiveCeiling())
	}
}

// ownerState returns the ultimate owning State: itself for a root, the
// captured owner for a child.
func (s *State) ownerState() *State {
	if s.interactionOwner != nil {
		return s.interactionOwner
	}
	return s
}

// effectiveCeiling is the immutable ceiling this state may run within.
func (s *State) effectiveCeiling() interaction.Ceiling {
	if s.interactionOwner == nil {
		return interaction.Ceiling{Act: true, Auto: true, System: true}
	}
	return s.interactionCeiling
}

// activeRoot reads the actor's current active workspace root.
func (s *State) activeRoot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspace.ActiveRoot
}

// InteractionAuthority returns the live authority view for this actor. It
// reads the owner's current tuple and the actor's own workspace, bounded by
// the immutable ceiling.
func (s *State) InteractionAuthority() interaction.Authority {
	return &stateAuthority{actor: s}
}

// PolicySnapshot returns deep copies of the owner's configuration and session
// rules together with the access revision, all under one owner transaction.
func (s *State) PolicySnapshot() (config.Config, []string, uint64) {
	owner := s.ownerState()
	owner.accessMu.Lock()
	defer owner.accessMu.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	cfg := config.Clone(owner.Config)
	rules := append([]string(nil), owner.sessionRules...)
	return cfg, rules, owner.access.Revision
}

type stateAuthority struct {
	actor *State
}

// Snapshot returns the actor's current bounded view.
func (a *stateAuthority) Snapshot() interaction.Snapshot {
	actor := a.actor
	actor.accessMu.Lock()
	actorSystem := actor.access.System
	actor.accessMu.Unlock()

	owner := actor.ownerState()
	owner.accessMu.Lock()
	defer owner.accessMu.Unlock()
	return a.snapshotLocked(owner, actorSystem)
}

// snapshotLocked builds the bounded snapshot. Callers must hold the owner's
// accessMu.
func (a *stateAuthority) snapshotLocked(owner *State, actorSystem bool) interaction.Snapshot {
	access := interaction.Bound(owner.access, a.actor.interactionCeiling)
	// System in a ceiling is permission to request, not a grant: the actor
	// must also have asked for it explicitly.
	if !actorSystem {
		access.System = false
	}
	return interaction.Snapshot{
		OwnerSessionID: owner.sessionID,
		ActorSessionID: a.actor.sessionID,
		WorkspaceRoot:  a.actor.activeRoot(),
		Access:         access,
	}
}

// BeginAdmission validates the expected snapshot against the live owner state
// and returns an idempotent lease that holds the owner gate. A mismatch
// unlocks and fails closed.
func (a *stateAuthority) BeginAdmission(ctx context.Context, expected interaction.Snapshot) (interaction.AdmissionLease, error) {
	actor := a.actor
	actor.accessMu.Lock()
	actorSystem := actor.access.System
	actor.accessMu.Unlock()

	owner := actor.ownerState()
	owner.accessMu.Lock()
	if err := ctx.Err(); err != nil {
		owner.accessMu.Unlock()
		return nil, err
	}
	owner.workMu.Lock()
	quiescing := owner.quiescing
	owner.workMu.Unlock()
	if quiescing {
		owner.accessMu.Unlock()
		return nil, ErrSessionQuiescing
	}
	if current := a.snapshotLocked(owner, actorSystem); current != expected {
		owner.accessMu.Unlock()
		return nil, ErrAdmissionStale
	}
	return &admissionLease{owner: owner}, nil
}

// admissionLease holds the owner's access gate until Release. Release is
// idempotent and unlocks even after a failed commit.
type admissionLease struct {
	owner *State
	once  sync.Once
}

func (l *admissionLease) Release() {
	l.once.Do(func() { l.owner.accessMu.Unlock() })
}
