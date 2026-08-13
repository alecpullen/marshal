package acp

import (
	"context"
	"encoding/json"
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

func TestParseNumstat(t *testing.T) {
	got := parseNumstat("3\t1\tmain.go\n0\t7\told.go\n-\t-\timage.png\n")
	if len(got) != 3 {
		t.Fatalf("len = %d: %+v", len(got), got)
	}
	if got[0] != (DiffFile{Path: "main.go", Added: 3, Removed: 1}) {
		t.Errorf("[0] = %+v", got[0])
	}
	if got[1] != (DiffFile{Path: "old.go", Added: 0, Removed: 7}) {
		t.Errorf("[1] = %+v", got[1])
	}
	// Binary files report "-" and must not be dropped or crash.
	if got[2].Path != "image.png" || got[2].Added != 0 {
		t.Errorf("[2] = %+v", got[2])
	}
}

// newDiffManager wires a WorktreeManager over one isolated fake session.
func newDiffManager(t *testing.T, git *worktree.FakeGitOps) (*WorktreeManager, string) {
	t.Helper()
	st := newWorktreeTestState(t, "/home/u/repo")
	st.SetWorkspace(session.Workspace{ProjectRoot: "/home/u/repo", ActiveRoot: "/home/u/repo/.marshal/worktrees/f", Branch: "f", BaseSha: "sha-base"})
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(id string) (*WorktreeRuntime, bool) {
			if id != "s1" {
				return nil, false
			}
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})
	return m, "s1"
}

func TestDiffReturnsFileStatsWithoutPath(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.DiffStatOut = "2\t0\ta.go\n"
	m, id := newDiffManager(t, git)

	res, err := m.Diff(context.Background(), json.RawMessage(`{"sessionId":"`+id+`"}`))
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	out := res.(DiffResult)
	if len(out.Files) != 1 || out.Files[0].Path != "a.go" {
		t.Fatalf("Files = %+v", out.Files)
	}
	if out.Diff != "" {
		t.Errorf("Diff must be empty without a path, got %q", out.Diff)
	}
}

func TestDiffReturnsUnifiedDiffForOnePath(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.DiffStatOut = "2\t0\ta.go\n"
	git.DiffOut = "--- a/a.go\n+++ b/a.go\n@@\n+x\n"
	m, id := newDiffManager(t, git)

	res, err := m.Diff(context.Background(), json.RawMessage(`{"sessionId":"`+id+`","path":"a.go"}`))
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if out := res.(DiffResult); !strings.Contains(out.Diff, "+x") {
		t.Fatalf("Diff = %q", out.Diff)
	}
}

func TestDiffRejectsNonIsolatedSession(t *testing.T) {
	git := worktree.NewFakeGitOps()
	st := newWorktreeTestState(t, "/home/u/repo") // at the project root, not isolated
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})
	if _, err := m.Diff(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("expected an error for a session that is not isolated")
	}
}

func TestDiffRejectsUnknownSession(t *testing.T) {
	git := worktree.NewFakeGitOps()
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git:    git,
		Lookup: func(string) (*WorktreeRuntime, bool) { return nil, false },
	})
	if _, err := m.Diff(context.Background(), json.RawMessage(`{"sessionId":"nope"}`)); err == nil {
		t.Fatal("expected an error for an unknown session")
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
