package native

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// TestWorkspaceFinishEndToEndThroughIsolation drives the full production
// flow with real git: workspace.worktree isolates the session (recording
// BaseSha), a file is committed inside the worktree, and workspace.finish
// with NO arguments must merge it back via the recorded base — the path
// that broke when isolation never recorded BaseSha.
func TestWorkspaceFinishEndToEndThroughIsolation(t *testing.T) {
	reg, state, root := newWorktreeTestEnv(t)

	if _, err := invokeTool(t, reg, "workspace.worktree", `{"branch":"feat/x"}`); err != nil {
		t.Fatalf("workspace.worktree: %v", err)
	}
	ws := state.Workspace()
	if ws.BaseSha == "" {
		t.Fatal("workspace.worktree did not record BaseSha — workspace.finish would have no default target")
	}
	if ws.TargetBranch == "" {
		t.Fatal("workspace.worktree did not record TargetBranch")
	}

	// Commit real work inside the worktree.
	wtPath := ws.ActiveRoot
	if err := os.WriteFile(filepath.Join(wtPath, "feat.txt"), []byte("from agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitWt := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wtPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitWt("add", ".")
	gitWt("commit", "-m", "agent work")

	res, err := invokeTool(t, reg, "workspace.finish", `{}`)
	if err != nil {
		t.Fatalf("workspace.finish: %v", err)
	}
	if !strings.Contains(res.Content, "squash commit") {
		t.Fatalf("content = %q, want a squash commit report", res.Content)
	}
	// The project carries the agent's file, the worktree and branch are
	// gone, and the session is back at the root.
	if _, err := os.Stat(filepath.Join(root, "feat.txt")); err != nil {
		t.Errorf("feat.txt not merged into the project: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree still present: %v", err)
	}
	if ws := state.Workspace(); ws.ActiveRoot != root || ws.Branch != "" {
		t.Errorf("workspace after finish = %+v, want back at project root", ws)
	}
}

// finishWorktreePath is the agent-owned worktree path the fixtures isolate
// the session in.
func finishWorktreePath(root string) string {
	return filepath.Join(root, ".marshal", "worktrees", "feat-x")
}

// finishFixture registers the native tools against a session isolated in an
// agent-owned worktree, with git backed by the given fake.
func finishFixture(t *testing.T, root string, git *worktree.FakeGitOps) (*registry.Registry, *session.State) {
	t.Helper()
	st := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: st, GitOps: git}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	st.SetWorkspace(session.Workspace{
		ProjectRoot:  root,
		ActiveRoot:   finishWorktreePath(root),
		Branch:       "feat/x",
		BaseSha:      strings.Repeat("1", 40),
		TargetBranch: "main",
	})
	return reg, st
}

func TestWorkspaceFinishSuccessSquashDefault(t *testing.T) {
	root := t.TempDir()
	wtPath := finishWorktreePath(root)
	git := worktree.NewFakeGitOps()
	git.Heads[root] = strings.Repeat("1", 40) // project HEAD == recorded base
	reg, st := finishFixture(t, root, git)

	res, err := invokeTool(t, reg, "workspace.finish", `{}`)
	if err != nil {
		t.Fatalf("workspace.finish: %v", err)
	}
	if !strings.Contains(res.Content, "squash commit") || !strings.Contains(res.Content, "commit0") {
		t.Fatalf("content = %q, want squash commit SHA", res.Content)
	}
	if len(git.SquashMerges) != 1 || git.SquashMerges[0] != "feat/x" {
		t.Fatalf("SquashMerges = %v, want [feat/x]", git.SquashMerges)
	}
	if len(git.Merges) != 0 {
		t.Fatalf("Merges = %v, want none (squash is the default)", git.Merges)
	}
	if len(git.Removed) != 1 || git.Removed[0] != wtPath {
		t.Fatalf("Removed = %v, want [%s]", git.Removed, wtPath)
	}
	if len(git.Deleted) != 1 || git.Deleted[0] != "feat/x" || !git.DeletedForce[0] {
		t.Fatalf("Deleted = %v force = %v, want feat/x forced", git.Deleted, git.DeletedForce)
	}
	if ws := st.Workspace(); ws.ActiveRoot != root || ws.Branch != "" {
		t.Fatalf("workspace after finish = %+v, want back at project root", ws)
	}
}

func TestWorkspaceFinishNoFF(t *testing.T) {
	root := t.TempDir()
	git := worktree.NewFakeGitOps()
	git.Heads[root] = strings.Repeat("1", 40)
	reg, _ := finishFixture(t, root, git)

	res, err := invokeTool(t, reg, "workspace.finish", `{"squash":false}`)
	if err != nil {
		t.Fatalf("workspace.finish: %v", err)
	}
	if !strings.Contains(res.Content, "merge commit (--no-ff)") {
		t.Fatalf("content = %q, want --no-ff merge commit", res.Content)
	}
	if len(git.Merges) != 1 || git.Merges[0] != "feat/x" {
		t.Fatalf("Merges = %v, want [feat/x]", git.Merges)
	}
	if len(git.SquashMerges) != 0 {
		t.Fatalf("SquashMerges = %v, want none", git.SquashMerges)
	}
	if len(git.Deleted) != 1 || git.DeletedForce[0] {
		t.Fatalf("Deleted = %v force = %v, want plain -d", git.Deleted, git.DeletedForce)
	}
}

func TestWorkspaceFinishRefusesNotInWorktree(t *testing.T) {
	root := t.TempDir()
	st := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: st, GitOps: worktree.NewFakeGitOps()}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "workspace.finish", `{}`)
	if err == nil {
		t.Fatal("workspace.finish at the project root must error")
	}
	if !strings.Contains(err.Error(), "not in a worktree") {
		t.Fatalf("error = %v, want 'not in a worktree'", err)
	}
}

func TestWorkspaceFinishRefusesProjectDirty(t *testing.T) {
	root := t.TempDir()
	git := worktree.NewFakeGitOps()
	git.Heads[root] = strings.Repeat("1", 40)
	git.DirtyDirs[root] = true
	reg, _ := finishFixture(t, root, git)

	res, err := invokeTool(t, reg, "workspace.finish", `{}`)
	if err != nil {
		t.Fatalf("workspace.finish: %v", err)
	}
	if !strings.Contains(res.Content, "project_dirty") {
		t.Fatalf("content = %q, want the project_dirty reason", res.Content)
	}
	if len(git.SquashMerges) != 0 || len(git.Merges) != 0 {
		t.Fatalf("merge attempted despite dirty project: squash=%v merge=%v", git.SquashMerges, git.Merges)
	}
}

func TestWorkspaceFinishRefusesDirtyWorktree(t *testing.T) {
	root := t.TempDir()
	git := worktree.NewFakeGitOps()
	git.Heads[root] = strings.Repeat("1", 40)
	git.DirtyDirs[finishWorktreePath(root)] = true
	reg, _ := finishFixture(t, root, git)

	res, err := invokeTool(t, reg, "workspace.finish", `{}`)
	if err != nil {
		t.Fatalf("workspace.finish: %v", err)
	}
	if !strings.Contains(res.Content, "dirty") {
		t.Fatalf("content = %q, want the dirty refusal", res.Content)
	}
}

func TestWorkspaceFinishMessageCommitsDirtyWorktree(t *testing.T) {
	root := t.TempDir()
	git := worktree.NewFakeGitOps()
	git.Heads[root] = strings.Repeat("1", 40)
	git.DirtyDirs[finishWorktreePath(root)] = true
	reg, _ := finishFixture(t, root, git)

	res, err := invokeTool(t, reg, "workspace.finish", `{"message":"wip"}`)
	if err != nil {
		t.Fatalf("workspace.finish: %v", err)
	}
	if !strings.Contains(res.Content, "squash commit") {
		t.Fatalf("content = %q, want a merged result", res.Content)
	}
	// FinishBranch reuses the message for both the worktree commit and the
	// squash commit when one is given.
	if len(git.Commits) != 2 || git.Commits[0] != "wip" || git.Commits[1] != "wip" {
		t.Fatalf("Commits = %v, want [wip, wip]", git.Commits)
	}
}

func TestWorkspaceFinishTargetArgResolvedToSHA(t *testing.T) {
	root := t.TempDir()
	target := strings.Repeat("3", 40)
	git := worktree.NewFakeGitOps()
	git.Heads[root] = target // project HEAD matches the resolved target
	git.Refs["main"] = target
	reg, _ := finishFixture(t, root, git)

	res, err := invokeTool(t, reg, "workspace.finish", `{"target":"main"}`)
	if err != nil {
		t.Fatalf("workspace.finish: %v", err)
	}
	if !strings.Contains(res.Content, "squash commit") {
		t.Fatalf("content = %q, want a merged result", res.Content)
	}
	found := false
	for _, c := range git.Calls() {
		if c == "RevParse" {
			found = true
		}
	}
	if !found {
		t.Fatal("target argument was not resolved via RevParse")
	}
}
