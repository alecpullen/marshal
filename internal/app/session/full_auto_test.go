package session

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

func TestFullAutoGrantsSystemWithoutAccessPrompt(t *testing.T) {
	for _, source := range []string{"manual", "configured", "reload"} {
		t.Run(source, func(t *testing.T) {
			cfg := config.Default()
			if source == "configured" {
				cfg.Agent.Auto = true
			}
			s := New(cfg, t.TempDir(), time.Now(), Persistence{})
			if source == "manual" {
				on := true
				if _, err := s.ApplyInteraction(interaction.Patch{Auto: &on}); err != nil {
					t.Fatal(err)
				}
			} else if source == "reload" {
				cfg.Agent.Auto = true
				if _, err := s.RebaseInteractionConfig(cfg); err != nil {
					t.Fatal(err)
				}
			}
			if got := s.InteractionAuthority().Snapshot().Access; !got.Auto || !got.System {
				t.Fatalf("Full Auto authority = %+v, want Auto and System", got)
			}
			if !s.SystemAccess() || s.PendingApproval() != nil {
				t.Fatal("Full Auto must supply system access without a prompt")
			}
		})
	}
}

func TestFullAutoChildCannotWidenSystemCeiling(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.Auto = true
	owner := New(cfg, t.TempDir(), time.Now(), Persistence{})
	child := New(cfg, t.TempDir(), time.Now(), Persistence{}, WithInteractionOwner(owner, interaction.Ceiling{Act: true, Auto: true}))
	if child.SystemAccess() {
		t.Fatal("Full Auto widened a child's System ceiling")
	}
	if got := child.InteractionAuthority().Snapshot().Access; !got.Auto || got.System {
		t.Fatalf("child access = %+v, want Full Auto bounded without System", got)
	}
}
