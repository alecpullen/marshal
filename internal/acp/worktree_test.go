package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/worktree"
)

// tempAbsDir returns an absolute path inside TMPDIR, which
// validateWorkingPaths accepts.
func tempAbsDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

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

// newMergeManager wires an isolated session plus a project root.
func newMergeManager(t *testing.T, git *worktree.FakeGitOps) *WorktreeManager {
	t.Helper()
	st := newWorktreeTestState(t, "/home/u/repo")
	st.SetWorkspace(session.Workspace{ProjectRoot: "/home/u/repo", ActiveRoot: "/home/u/repo/.marshal/worktrees/f", Branch: "f"})
	return NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})
}

func mergeCall(t *testing.T, m *WorktreeManager, body string) MergeResult {
	t.Helper()
	res, err := m.Merge(context.Background(), json.RawMessage(body))
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return res.(MergeResult)
}

func TestMergeRefusesDirtyWorktreeWithoutCommitMessage(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.Dirty = true
	git.AbbrevRef = "main"
	m := newMergeManager(t, git)

	got := mergeCall(t, m, `{"sessionId":"s1","targetBranch":"main"}`)
	if got.Merged || got.Reason != ReasonDirty {
		t.Fatalf("got %+v, want refusal with reason dirty", got)
	}
	if len(git.Merges) != 0 {
		t.Error("must not attempt a merge when refusing")
	}
}

func TestMergeCommitsDirtyWorktreeWhenGivenAMessage(t *testing.T) {
	git := worktree.NewFakeGitOps()
	// Only the worktree is dirty; the project root stays clean.
	git.DirtyDirs["/home/u/repo/.marshal/worktrees/f"] = true
	git.AbbrevRef = "main"
	m := newMergeManager(t, git)

	got := mergeCall(t, m, `{"sessionId":"s1","targetBranch":"main","commitMessage":"wip"}`)
	if !got.Merged {
		t.Fatalf("got %+v, want merged", got)
	}
	if len(git.Commits) == 0 {
		t.Error("expected CommitAll before merging")
	}
}

func TestMergeRefusesWhenProjectIsOnADifferentBranch(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.AbbrevRef = "other"
	m := newMergeManager(t, git)

	got := mergeCall(t, m, `{"sessionId":"s1","targetBranch":"main"}`)
	if got.Merged || got.Reason != ReasonTargetMoved {
		t.Fatalf("got %+v, want refusal with reason target_moved", got)
	}
}

// The merge target is fixed at isolation time and persisted. A caller that
// supplies a different target must be refused, even if the project happens
// to be on that branch.
func TestMergeRefusesCallerSuppliedTargetDifferentFromPersisted(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.AbbrevRef = "release" // the project is on release, which the caller asks for
	st := newWorktreeTestState(t, "/home/u/repo")
	st.SetWorkspace(session.Workspace{
		ProjectRoot: "/home/u/repo", ActiveRoot: "/home/u/repo/.marshal/worktrees/f",
		Branch: "f", TargetBranch: "main", // but isolation recorded main
	})
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})

	got := mergeCall(t, m, `{"sessionId":"s1","targetBranch":"release"}`)
	if got.Merged || got.Reason != ReasonTargetMoved {
		t.Fatalf("got %+v, want refusal with reason target_moved", got)
	}
	if len(git.Merges) != 0 {
		t.Error("must not merge into a target that differs from the persisted one")
	}
}

// The critical one: a conflicting merge must abort so the project is not
// left mid-merge.
func TestMergeAbortsOnConflict(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.AbbrevRef = "main"
	git.MergeErr = errors.New("CONFLICT (content): Merge conflict in a.go\nCONFLICT (content): Merge conflict in b.go")
	m := newMergeManager(t, git)

	got := mergeCall(t, m, `{"sessionId":"s1","targetBranch":"main"}`)
	if got.Merged || got.Reason != ReasonConflicts {
		t.Fatalf("got %+v, want refusal with reason conflicts", got)
	}
	if git.Aborts != 1 {
		t.Fatalf("Aborts = %d, want exactly 1 — a refused merge must never leave the project mid-merge", git.Aborts)
	}
	if len(got.Conflicts) != 2 {
		t.Errorf("Conflicts = %v, want both files", got.Conflicts)
	}
}

func TestMergeSuccessRemovesWorktreeDeletesBranchAndUnisolates(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.AbbrevRef = "main"
	st := newWorktreeTestState(t, "/home/u/repo")
	st.SetWorkspace(session.Workspace{ProjectRoot: "/home/u/repo", ActiveRoot: "/home/u/repo/.marshal/worktrees/f", Branch: "f"})
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})

	got := mergeCall(t, m, `{"sessionId":"s1","targetBranch":"main"}`)
	if !got.Merged {
		t.Fatalf("got %+v", got)
	}
	if len(git.Deleted) != 1 || git.Deleted[0] != "f" {
		t.Errorf("Deleted = %v, want the agent branch", git.Deleted)
	}
	if ws := st.Workspace(); ws.Branch != "" || ws.ActiveRoot != ws.ProjectRoot {
		t.Errorf("session must return to the project root, got %+v", ws)
	}
}

// A merge that succeeds but then fails to remove the worktree must still
// return the session to the project root: the merge is done, and leaving the
// session claiming isolation in a worktree that is already merged away would
// be worse than reporting the cleanup failure.
func TestMergeCleanupFailureStillUnisolates(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.AbbrevRef = "main"
	git.RemoveErr = errors.New("git refuses: dirty worktree")
	st := newWorktreeTestState(t, "/home/u/repo")
	st.SetWorkspace(session.Workspace{ProjectRoot: "/home/u/repo", ActiveRoot: "/home/u/repo/.marshal/worktrees/f", Branch: "f"})
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})

	raw, err := m.Merge(context.Background(), json.RawMessage(`{"sessionId":"s1","targetBranch":"main"}`))
	if err == nil {
		t.Fatal("expected an error when worktree removal fails after a successful merge")
	}
	got, ok := raw.(MergeResult)
	if !ok {
		t.Fatalf("Merge returned %T, want MergeResult", raw)
	}
	if !got.Merged {
		t.Fatalf("Merged = false, want true — the merge itself succeeded")
	}
	if ws := st.Workspace(); ws.Branch != "" || ws.ActiveRoot != ws.ProjectRoot {
		t.Errorf("session must return to the project root even when cleanup fails, got %+v", ws)
	}
}

func TestDiscardRemovesWorktreeAndForceDeletesBranch(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.WorktreeBranches["/home/u/repo/.marshal/worktrees/f"] = "f"
	st := newWorktreeTestState(t, "/home/u/repo")
	st.SetWorkspace(session.Workspace{ProjectRoot: "/home/u/repo", ActiveRoot: "/home/u/repo/.marshal/worktrees/f", Branch: "f"})
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})

	if _, err := m.Discard(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if len(git.Deleted) != 1 || git.Deleted[0] != "f" {
		t.Errorf("Deleted = %v", git.Deleted)
	}
	if ws := st.Workspace(); ws.Branch != "" || ws.ActiveRoot != ws.ProjectRoot {
		t.Errorf("session must return to the project root, got %+v", ws)
	}
}

// Discard must refuse to remove a worktree it cannot prove it owns: the
// recorded path is not under the agent worktree dir, is not a registered
// worktree, or is attached to a different branch.
func TestDiscardRefusesWorktreeOutsideAgentDir(t *testing.T) {
	git := worktree.NewFakeGitOps()
	st := newWorktreeTestState(t, "/home/u/repo")
	st.SetWorkspace(session.Workspace{ProjectRoot: "/home/u/repo", ActiveRoot: "/home/u/other", Branch: "f"})
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})
	if _, err := m.Discard(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("expected an error for a worktree outside the agent dir")
	}
	if len(git.Deleted) != 0 {
		t.Errorf("must not delete a branch when refusing, Deleted = %v", git.Deleted)
	}
}

func TestDiscardRefusesUnregisteredWorktree(t *testing.T) {
	git := worktree.NewFakeGitOps()
	// No entry in WorktreeBranches → WorktreeBranch errors.
	st := newWorktreeTestState(t, "/home/u/repo")
	st.SetWorkspace(session.Workspace{ProjectRoot: "/home/u/repo", ActiveRoot: "/home/u/repo/.marshal/worktrees/f", Branch: "f"})
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})
	if _, err := m.Discard(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("expected an error for an unregistered worktree")
	}
	if len(git.Deleted) != 0 {
		t.Errorf("must not delete a branch when refusing, Deleted = %v", git.Deleted)
	}
}

func TestDiscardRefusesWrongBranchAttached(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.WorktreeBranches["/home/u/repo/.marshal/worktrees/f"] = "other"
	st := newWorktreeTestState(t, "/home/u/repo")
	st.SetWorkspace(session.Workspace{ProjectRoot: "/home/u/repo", ActiveRoot: "/home/u/repo/.marshal/worktrees/f", Branch: "f"})
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})
	if _, err := m.Discard(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("expected an error when the worktree is attached to a different branch")
	}
	if len(git.Deleted) != 0 {
		t.Errorf("must not delete a branch when refusing, Deleted = %v", git.Deleted)
	}
}

func TestDiscardRejectsNonIsolatedSession(t *testing.T) {
	git := worktree.NewFakeGitOps()
	st := newWorktreeTestState(t, "/home/u/repo")
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git: git,
		Lookup: func(string) (*WorktreeRuntime, bool) {
			return &WorktreeRuntime{State: st, ProjectRoot: "/home/u/repo"}, true
		},
	})
	if _, err := m.Discard(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("expected an error for a session that is not isolated")
	}
}

func TestPruneReportsUnknownWorktreesWithoutDeletingThem(t *testing.T) {
	git := worktree.NewFakeGitOps()
	git.Worktrees = []string{"/home/u/repo/.marshal/worktrees/known", "/home/u/repo/.marshal/worktrees/orphan"}
	m := NewWorktreeManager(WorktreeManagerConfig{
		Git:            git,
		KnownWorktrees: func(string) []string { return []string{"/home/u/repo/.marshal/worktrees/known"} },
	})

	res, err := m.Prune(context.Background(), json.RawMessage(`{"cwd":"`+tempAbsDir(t)+`"}`))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	out := res.(PruneResult)
	if len(out.Unknown) != 1 || !strings.HasSuffix(out.Unknown[0], "orphan") {
		t.Fatalf("Unknown = %v, want just the orphan", out.Unknown)
	}
	// An unknown worktree may hold someone's uncommitted work.
	if len(git.Deleted) != 0 {
		t.Error("prune must never delete a branch")
	}
	for _, c := range git.Calls() {
		if c == "WorktreeRemove" {
			t.Fatal("prune must never remove an unknown worktree")
		}
	}
}

func TestPruneRejectsRelativeCwd(t *testing.T) {
	m := NewWorktreeManager(WorktreeManagerConfig{Git: worktree.NewFakeGitOps()})
	if _, err := m.Prune(context.Background(), json.RawMessage(`{"cwd":"rel/path"}`)); err == nil {
		t.Fatal("expected an error for a relative cwd")
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
