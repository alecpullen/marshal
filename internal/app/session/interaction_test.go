package session

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

func interactionState(t *testing.T) *State {
	t.Helper()
	return New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
}

func TestInteractionSessionAtomicTransitions(t *testing.T) {
	s := interactionState(t)
	st := s.InteractionState()
	if st.Mode != interaction.ModeAct || st.Auto || st.System || st.Revision != 1 {
		t.Fatalf("fresh state = %+v, want act/false/false/1", st)
	}

	ask := interaction.ModeAsk
	next, err := s.ApplyInteraction(interaction.Patch{Mode: &ask})
	if err != nil {
		t.Fatalf("ApplyInteraction(ask): %v", err)
	}
	if next.Mode != interaction.ModeAsk || next.Auto || next.Revision != 2 {
		t.Fatalf("after ask = %+v, want ask/false/2", next)
	}

	on := true
	next, err = s.ApplyInteraction(interaction.Patch{Auto: &on})
	if err != nil {
		t.Fatalf("ApplyInteraction(auto): %v", err)
	}
	if next.Mode != interaction.ModeAct || !next.Auto || next.Revision != 3 {
		t.Fatalf("after auto = %+v, want act/true/3", next)
	}

	next, err = s.ApplyInteraction(interaction.Patch{System: &on})
	if err != nil {
		t.Fatalf("ApplyInteraction(system): %v", err)
	}
	if !next.System || next.Revision != 4 {
		t.Fatalf("after system = %+v, want system/4", next)
	}

	// A no-op patch does not increment the revision.
	next, err = s.ApplyInteraction(interaction.Patch{System: &on})
	if err != nil {
		t.Fatalf("ApplyInteraction(no-op): %v", err)
	}
	if next.Revision != 4 {
		t.Fatalf("no-op revision = %d, want 4", next.Revision)
	}
}

func TestInteractionRevisionDetectsABA(t *testing.T) {
	s := interactionState(t)
	r1 := s.InteractionState().Revision
	ask := interaction.ModeAsk
	if _, err := s.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	act := interaction.ModeAct
	if _, err := s.ApplyInteraction(interaction.Patch{Mode: &act}); err != nil {
		t.Fatalf("act: %v", err)
	}
	r2 := s.InteractionState().Revision
	if r2 <= r1 {
		t.Fatalf("ABA revision = %d, want > %d", r2, r1)
	}
}

func TestInteractionOverrideSurvivesUnrelatedRebase(t *testing.T) {
	s := interactionState(t)
	ask := interaction.ModeAsk
	if _, err := s.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	cfg := config.Default() // configured pair unchanged (act/false)
	next, err := s.RebaseInteractionConfig(cfg)
	if err != nil {
		t.Fatalf("RebaseInteractionConfig: %v", err)
	}
	if next.Mode != interaction.ModeAsk {
		t.Fatalf("rebased mode = %q, want ask (override survives)", next.Mode)
	}
}

func TestInteractionConfiguredAutoChangeClearsTupleOverride(t *testing.T) {
	s := interactionState(t)
	ask := interaction.ModeAsk
	if _, err := s.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	cfg := config.Default()
	cfg.Agent.Auto = true // configured pair changes to act/true
	next, err := s.RebaseInteractionConfig(cfg)
	if err != nil {
		t.Fatalf("RebaseInteractionConfig: %v", err)
	}
	if next.Mode != interaction.ModeAct || !next.Auto {
		t.Fatalf("rebased = %+v, want act/true (override dropped)", next)
	}
}

func TestInteractionInvalidRebasePreservesState(t *testing.T) {
	s := interactionState(t)
	before := s.InteractionState()
	cfg := config.Default()
	cfg.Agent.Mode = "bogus"
	if _, err := s.RebaseInteractionConfig(cfg); err == nil {
		t.Fatal("RebaseInteractionConfig accepted an invalid tuple")
	}
	after := s.InteractionState()
	if after != before {
		t.Fatalf("failed rebase changed state: %+v -> %+v", before, after)
	}
}

func TestInteractionNewAndResumeResetGrants(t *testing.T) {
	s := interactionState(t)
	if s.InteractionState().System {
		t.Fatal("fresh state has System on")
	}
	if s.InteractionState().Revision != 1 {
		t.Fatalf("fresh revision = %d, want 1", s.InteractionState().Revision)
	}
	s.SetSystemAccess(true)
	// A new conversation (resume) starts with no grants.
	s2 := interactionState(t)
	if s2.InteractionState().System {
		t.Fatal("resumed state restored a System grant")
	}
	if s2.InteractionState().Revision != 1 {
		t.Fatalf("resumed revision = %d, want 1", s2.InteractionState().Revision)
	}
}

func TestInteractionWorkspaceInvalidatesRevision(t *testing.T) {
	s := interactionState(t)
	r1 := s.InteractionState().Revision
	s.SetWorkspace(Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo/wt"})
	r2 := s.InteractionState().Revision
	if r2 <= r1 {
		t.Fatalf("workspace change revision = %d, want > %d", r2, r1)
	}
	// Re-setting the same active root does not invalidate.
	s.SetWorkspace(Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo/wt"})
	if got := s.InteractionState().Revision; got != r2 {
		t.Fatalf("unchanged workspace revision = %d, want %d", got, r2)
	}
}

func TestSessionRuleChangeInvalidatesAccessRevision(t *testing.T) {
	s := interactionState(t)
	r1 := s.InteractionState().Revision
	s.AddSessionRule("git status")
	r2 := s.InteractionState().Revision
	if r2 <= r1 {
		t.Fatalf("session-rule revision = %d, want > %d", r2, r1)
	}
}
