package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

func TestAccessActGrantNeverEnablesAuto(t *testing.T) {
	f := newAccessFixture(t) // Ask configured
	req := f.register(AccessIntent{Editing: true})

	result, err := f.state.ResolveAccess(choiceFor(req, req.Current, "edit"), func(req AccessRequest, _ AccessChoice) (AccessCommit, error) {
		target := req.Current
		target.Mode = "edit"
		return AccessCommit{Effective: target, Apply: func() {}}, nil
	})
	if err != nil || result.Status != AccessGranted {
		t.Fatalf("ResolveAccess = (%+v, %v), want a grant", result, err)
	}
	got := f.state.AccessState()
	if got.Mode != "act" || got.Auto {
		t.Fatalf("granted tuple = %+v, want Act with Auto off", got)
	}
}

func TestAccessSystemOnlyPreservesModeAndAuto(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.Mode = interaction.ModeAct
	cfg.Agent.Auto = false
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})

	call := &PendingToolCall{ID: "c", ResponseChan: make(chan UserApprovalDecision, 1)}
	req, waiting, err := s.RegisterAccess(context.Background(), call, AccessIntent{System: true})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%+v, %v, %v), want a published request", req, waiting, err)
	}
	result, err := s.ResolveAccess(choiceFor(req, req.Current, ""), func(req AccessRequest, _ AccessChoice) (AccessCommit, error) {
		target := req.Current
		target.System = true
		return AccessCommit{Effective: target, Apply: func() {}}, nil
	})
	if err != nil || result.Status != AccessGranted {
		t.Fatalf("ResolveAccess = (%+v, %v), want a grant", result, err)
	}
	got := s.AccessState()
	if got.Mode != "act" || got.Auto || !got.System {
		t.Fatalf("granted tuple = %+v, want Act+System with Auto off", got)
	}
}

func TestAccessCombinedGrantCommitsOneRevision(t *testing.T) {
	f := newAccessFixture(t) // Ask
	req := f.register(AccessIntent{Editing: true, System: true})
	before := req.Current.Revision

	result, err := f.state.ResolveAccess(choiceFor(req, req.Current, "edit"), func(req AccessRequest, _ AccessChoice) (AccessCommit, error) {
		target := req.Current
		target.Mode = "edit"
		target.System = true
		return AccessCommit{Effective: target, Apply: func() {}}, nil
	})
	if err != nil || result.Status != AccessGranted {
		t.Fatalf("ResolveAccess = (%+v, %v), want a grant", result, err)
	}
	got := f.state.AccessState()
	if got.Mode != "act" || got.Auto || !got.System {
		t.Fatalf("granted tuple = %+v, want Act/Auto-off/System", got)
	}
	if got.Revision != before+1 {
		t.Fatalf("revision = %d, want exactly one increment from %d", got.Revision, before)
	}
}

func TestAccessABAChoiceRequiresRefresh(t *testing.T) {
	f := newAccessFixture(t) // Ask
	req := f.register(AccessIntent{Editing: true})
	shown := req.Current

	// ABA: Ask -> Act -> Ask leaves the tuple equal but the revision newer.
	act := interaction.ModeAct
	if _, err := f.state.ApplyInteraction(interaction.Patch{Mode: &act}); err != nil {
		t.Fatalf("act: %v", err)
	}
	ask := interaction.ModeAsk
	if _, err := f.state.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("ask: %v", err)
	}

	result, err := f.state.ResolveAccess(choiceFor(req, shown, "edit"), func(AccessRequest, AccessChoice) (AccessCommit, error) {
		t.Fatal("a stale choice reached prepare")
		return AccessCommit{}, nil
	})
	if err != nil {
		t.Fatalf("ResolveAccess(stale) = %v, want nil", err)
	}
	if result.Status != AccessRefresh {
		t.Fatalf("ResolveAccess(stale) = %+v, want AccessRefresh", result)
	}
	if _, ok := f.state.AccessRequest(); !ok {
		t.Fatal("a refresh dropped the pending request")
	}
}

func TestAccessWorkspaceOrConfigRevisionRequiresRefresh(t *testing.T) {
	f := newAccessFixture(t)
	req := f.register(AccessIntent{Editing: true})
	shown := req.Current

	f.state.SetWorkspace(Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo/wt"})

	result, err := f.state.ResolveAccess(choiceFor(req, shown, "edit"), func(AccessRequest, AccessChoice) (AccessCommit, error) {
		t.Fatal("a stale choice reached prepare")
		return AccessCommit{}, nil
	})
	if err != nil {
		t.Fatalf("ResolveAccess(stale) = %v, want nil", err)
	}
	if result.Status != AccessRefresh {
		t.Fatalf("ResolveAccess(stale) = %+v, want AccessRefresh", result)
	}
	if _, ok := f.state.AccessRequest(); !ok {
		t.Fatal("a refresh dropped the pending request")
	}
}

// A permission grant runs the same owner transaction as a session rule, so an
// admission taken against the pre-grant snapshot is stale and must be retried
// rather than executed under the old policy.
func TestAddSessionPermissionRuleBumpsRevision(t *testing.T) {
	owner := interactionState(t)
	auth := owner.InteractionAuthority()
	before := auth.Snapshot()

	owner.AddSessionPermissionRule(PermissionGrant{Permission: "image.capture", Pattern: "active_window"})

	if got := owner.InteractionState().Revision; got <= before.Access.Revision {
		t.Fatalf("revision = %d, want > %d after a grant", got, before.Access.Revision)
	}
	if _, err := auth.BeginAdmission(context.Background(), before); !errors.Is(err, ErrAdmissionStale) {
		t.Fatalf("BeginAdmission(stale) = %v, want ErrAdmissionStale", err)
	}
}

func TestSessionPermissionRulesReturnsCopy(t *testing.T) {
	f := newAccessFixture(t)
	grant := PermissionGrant{Permission: "image.capture", Pattern: "active_window"}
	f.state.AddSessionPermissionRule(grant)

	got := f.state.SessionPermissionRules()
	if len(got) != 1 || got[0] != grant {
		t.Fatalf("SessionPermissionRules = %+v, want [%+v]", got, grant)
	}
	got[0].Pattern = "screen"
	if again := f.state.SessionPermissionRules(); again[0].Pattern != "active_window" {
		t.Fatalf("mutating the returned slice changed the session grants: %+v", again)
	}
}
