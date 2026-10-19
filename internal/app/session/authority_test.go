package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

func childState(t *testing.T, owner *State, ceiling interaction.Ceiling) *State {
	t.Helper()
	return New(config.Default(), "/repo", time.Unix(100, 0), Persistence{}, WithInteractionOwner(owner, ceiling))
}

func TestInteractionAdmissionStaleRevisionFails(t *testing.T) {
	owner := interactionState(t)
	auth := owner.InteractionAuthority()
	snap := auth.Snapshot()

	ask := interaction.ModeAsk
	if _, err := owner.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("ApplyInteraction: %v", err)
	}
	if _, err := auth.BeginAdmission(context.Background(), snap); !errors.Is(err, ErrAdmissionStale) {
		t.Fatalf("BeginAdmission = %v, want ErrAdmissionStale", err)
	}
}

func TestInteractionAdmissionLeaseSerializesModeChange(t *testing.T) {
	owner := interactionState(t)
	auth := owner.InteractionAuthority()
	lease, err := auth.BeginAdmission(context.Background(), auth.Snapshot())
	if err != nil {
		t.Fatalf("BeginAdmission: %v", err)
	}

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		ask := interaction.ModeAsk
		_, _ = owner.ApplyInteraction(interaction.Patch{Mode: &ask})
		close(done)
	}()
	<-started
	select {
	case <-done:
		t.Fatal("a mode change completed while the admission lease was held")
	case <-time.After(100 * time.Millisecond):
	}

	lease.Release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the mode change did not complete after the lease was released")
	}
}

func TestInteractionAdmissionReleaseIsIdempotent(t *testing.T) {
	owner := interactionState(t)
	auth := owner.InteractionAuthority()
	lease, err := auth.BeginAdmission(context.Background(), auth.Snapshot())
	if err != nil {
		t.Fatalf("BeginAdmission: %v", err)
	}
	lease.Release()
	lease.Release() // must not panic or double-unlock

	ask := interaction.ModeAsk
	if _, err := owner.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("ApplyInteraction after release: %v", err)
	}
}

func TestInteractionChildViewUsesCurrentParentCeiling(t *testing.T) {
	owner := interactionState(t)
	child := childState(t, owner, interaction.Ceiling{Act: true, Auto: true, System: true})

	ask := interaction.ModeAsk
	if _, err := owner.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("owner ask: %v", err)
	}
	snap := child.InteractionAuthority().Snapshot()
	if snap.Access.Mode != interaction.ModeAsk {
		t.Fatalf("child view mode = %q, want ask (live parent)", snap.Access.Mode)
	}
	if snap.OwnerSessionID != owner.SessionID() || snap.ActorSessionID != child.SessionID() {
		t.Fatalf("snapshot identities = %q/%q", snap.OwnerSessionID, snap.ActorSessionID)
	}
}

func TestInteractionNestedCeilingsOnlyNarrow(t *testing.T) {
	owner := interactionState(t)
	mid := childState(t, owner, interaction.Ceiling{Act: true, Auto: false, System: true})
	leaf := childState(t, mid, interaction.Ceiling{Act: true, Auto: true, System: true})

	// The leaf asks for Auto, but the mid ceiling forbids it.
	on := true
	if _, err := leaf.ApplyInteraction(interaction.Patch{Auto: &on}); err != nil {
		t.Fatalf("leaf auto: %v", err)
	}
	snap := leaf.InteractionAuthority().Snapshot()
	if snap.Access.Auto {
		t.Fatal("leaf view widened Auto past the mid ceiling")
	}
	if snap.OwnerSessionID != owner.SessionID() {
		t.Fatalf("leaf owner = %q, want the ultimate owner %q", snap.OwnerSessionID, owner.SessionID())
	}
}

func TestInteractionActorWorkspaceDoesNotRetargetOwner(t *testing.T) {
	owner := interactionState(t)
	child := childState(t, owner, interaction.Ceiling{Act: true, Auto: true, System: true})
	before := owner.InteractionState().Revision

	child.SetWorkspace(Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo/wt"})

	if got := owner.activeRoot(); got != "/repo" {
		t.Fatalf("owner active root = %q, want /repo (unchanged)", got)
	}
	if got := child.InteractionAuthority().Snapshot().WorkspaceRoot; got != "/repo/wt" {
		t.Fatalf("child workspace root = %q, want /repo/wt", got)
	}
	if got := owner.InteractionState().Revision; got <= before {
		t.Fatalf("owner revision = %d, want > %d (actor workspace invalidates)", got, before)
	}
}

func TestPolicySnapshotSharesAccessRevisionAndCurrentRules(t *testing.T) {
	owner := interactionState(t)
	owner.AddSessionRule("git status")

	cfg, rules, rev := owner.PolicySnapshot()
	if rev != owner.InteractionState().Revision {
		t.Fatalf("snapshot revision = %d, want %d", rev, owner.InteractionState().Revision)
	}
	if len(rules) != 1 || rules[0] != "git status" {
		t.Fatalf("snapshot rules = %v, want [git status]", rules)
	}
	// The config is a deep copy: mutating it does not touch the owner.
	cfg.Agent.Mode = interaction.ModeAsk
	if got := owner.ConfigSnapshot().Agent.Mode; got != interaction.ModeAct {
		t.Fatalf("owner config mutated through the snapshot: Mode = %q", got)
	}
}
