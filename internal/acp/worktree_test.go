package acp

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/worktree"
)

// newWorktreeTestState returns a session.State with no persistence, enough
// for Workspace()/SetWorkspace(). Named distinctly from skills_test.go's
// newTestState, which takes no arguments.
func newWorktreeTestState(t *testing.T, projectRoot string) *session.State {
	t.Helper()
	st := session.New(config.Default(), projectRoot, timeNowForTest(), session.Persistence{})
	st.SetWorkspace(session.Workspace{ProjectRoot: projectRoot, ActiveRoot: projectRoot})
	return st
}

func timeNowForTest() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) }

func TestIsolateSessionCreatesWorktreeAndSwitchesRoot(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.Refs["HEAD"] = "sha-base"
	git.Refs["main"] = "sha-base"
	git.AbbrevRef = "main"
	root := t.TempDir()
	st := newWorktreeTestState(t, root)

	got, err := isolateSession(git, st, root, IsolationParams{Branch: "feat/x"}, "ignored")
	if err != nil {
		t.Fatalf("isolateSession: %v", err)
	}
	if got.Branch != "feat/x" {
		t.Errorf("Branch = %q", got.Branch)
	}
	if got.BaseSha != "sha-base" {
		t.Errorf("BaseSha = %q, want sha-base", got.BaseSha)
	}
	// Required for merge: you cannot merge into a SHA.
	if got.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want main", got.TargetBranch)
	}
	if !strings.Contains(got.ActiveRoot, "feat-x") {
		t.Errorf("ActiveRoot = %q, want it to contain the slugged branch", got.ActiveRoot)
	}

	ws := st.Workspace()
	if ws.ActiveRoot != got.ActiveRoot || ws.Branch != "feat/x" {
		t.Errorf("session workspace not switched: %+v", ws)
	}
	// The project root must not move: .marshal/marshal.db lives there.
	if ws.ProjectRoot != root {
		t.Errorf("ProjectRoot = %q, must stay the project root", ws.ProjectRoot)
	}
}

func TestIsolateSessionDerivesBranchFromName(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.Refs["HEAD"] = "sha"
	git.AbbrevRef = "main"
	root := t.TempDir()
	st := newWorktreeTestState(t, root)

	got, err := isolateSession(git, st, root, IsolationParams{}, "Fix the Login Bug!")
	if err != nil {
		t.Fatalf("isolateSession: %v", err)
	}
	if got.Branch != "marshal/fix-the-login-bug" {
		t.Errorf("Branch = %q", got.Branch)
	}
}

func TestIsolateSessionHonoursBaseRef(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.Refs["HEAD"] = "sha-head"
	git.Refs["release"] = "sha-release"
	git.AbbrevRef = "main"
	root := t.TempDir()
	st := newWorktreeTestState(t, root)

	got, err := isolateSession(git, st, root, IsolationParams{Branch: "f", BaseRef: "release"}, "n")
	if err != nil {
		t.Fatalf("isolateSession: %v", err)
	}
	if got.BaseSha != "sha-release" {
		t.Errorf("BaseSha = %q, want sha-release", got.BaseSha)
	}
}

func TestSlugifyBranch(t *testing.T) {
	cases := map[string]string{
		"Fix the Login Bug!": "fix-the-login-bug",
		"  spaces   here  ":  "spaces-here",
		"CAPS/slash":         "caps-slash",
		"":                   "agent",
		"!!!":                "agent",
	}
	for in, want := range cases {
		if got := slugifyBranch(in); got != want {
			t.Errorf("slugifyBranch(%q) = %q, want %q", in, got, want)
		}
	}
}
