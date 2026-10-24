package session

import (
	"errors"
	"fmt"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

func TestExitAnalysisAccessIsTemporaryAndFreezesPermissionChanges(t *testing.T) {
	for _, initial := range []interaction.State{
		{Mode: interaction.ModeAsk},
		{Mode: interaction.ModeAct, System: true},
		{Mode: interaction.ModeAct, Auto: true, System: true},
	} {
		t.Run(fmt.Sprint(initial.Mode, initial.Auto), func(t *testing.T) {
			s := interactionState(t)
			if _, err := s.ApplyInteraction(interaction.Patch{Mode: &initial.Mode, Auto: &initial.Auto, System: &initial.System}); err != nil {
				t.Fatal(err)
			}
			before := s.InteractionState()
			if _, err := s.BeginExitAnalysisAccess(nil); !errors.Is(err, ErrNotPostmortemPhase) {
				t.Fatalf("grant outside postmortem = %v", err)
			}
			s.SetWorkPhase(ExitPhasePostmortem)
			scope, err := s.OpenExitAnalysis()
			if err != nil {
				t.Fatal(err)
			}
			restore, err := s.BeginExitAnalysisAccess(scope)
			if err != nil {
				t.Fatal(err)
			}
			defer restore()
			granted := s.InteractionState()
			if granted.Mode != interaction.ModeAct || !granted.System || granted.Auto != before.Auto {
				t.Fatalf("postmortem access = %+v", granted)
			}
			ask := interaction.ModeAsk
			if _, err := s.ApplyInteraction(interaction.Patch{Mode: &ask}); !errors.Is(err, ErrConversationEnding) {
				t.Fatalf("concurrent mode change = %v", err)
			}
			cfg := config.Default()
			cfg.Agent.Mode = interaction.ModeAsk
			if _, err := s.InteractionForConfig(cfg); !errors.Is(err, ErrConversationEnding) {
				t.Fatalf("concurrent reload preview = %v", err)
			}
			if _, err := s.RebaseInteractionConfig(cfg); !errors.Is(err, ErrConversationEnding) {
				t.Fatalf("concurrent config rebase = %v", err)
			}
			s.SetSystemAccess(false)
			if got := s.InteractionState(); got != granted {
				t.Fatalf("temporary grant changed: %+v", got)
			}
			// Cleanup also works after shutdown closes ordinary mutation admission.
			s.BeginQuiesce()
			s.CloseExitAnalysis(scope)
			restore()
			after := s.InteractionState()
			if after.Mode != before.Mode || after.Auto != before.Auto || after.System != before.System {
				t.Fatalf("postmortem leaked access: %+v -> %+v", before, after)
			}
			restore()
			if s.InteractionState() != after {
				t.Fatal("cleanup was not idempotent")
			}
		})
	}
}
