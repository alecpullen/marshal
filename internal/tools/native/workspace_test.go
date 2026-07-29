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
)

// initGitRepo creates a real git repository with one commit at dir.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "init")
}

func newWorktreeTestEnv(t *testing.T) (*registry.Registry, *session.State, string) {
	t.Helper()
	root := t.TempDir()
	// Resolve symlinks so paths match what git worktree list --porcelain
	// returns (macOS /var -> /private/var).
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	initGitRepo(t, root)
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return reg, state, root
}

func TestWorkspaceWorktreeCreatesAndRebinds(t *testing.T) {
	reg, state, root := newWorktreeTestEnv(t)
	res, err := invokeTool(t, reg, "workspace.worktree", `{"branch":"feat/x"}`)
	if err != nil {
		t.Fatalf("workspace.worktree: %v", err)
	}
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	ws := state.Workspace()
	if ws.ActiveRoot != wt || ws.Branch != "feat/x" || ws.ProjectRoot != root {
		t.Fatalf("Workspace() = %+v, want worktree %q on feat/x", ws, wt)
	}
	if info, err := os.Stat(wt); err != nil || !info.IsDir() {
		t.Fatalf("worktree dir missing: %v", err)
	}
	if state.WorkingDir != root {
		t.Fatalf("WorkingDir = %q, want unchanged project root", state.WorkingDir)
	}
	if !strings.Contains(res.Content, wt) || !strings.Contains(res.Content, "feat/x") {
		t.Fatalf("result content %q does not state path and branch", res.Content)
	}
	// The worktrees dir is self-ignoring.
	if gi, err := os.ReadFile(filepath.Join(root, ".marshal", "worktrees", ".gitignore")); err != nil || string(gi) != "*\n" {
		t.Fatalf("worktrees .gitignore = %q, %v", gi, err)
	}
}

func TestWorkspaceWorktreeWritePatchLandsInWorktree(t *testing.T) {
	reg, state, root := newWorktreeTestEnv(t)
	if _, err := invokeTool(t, reg, "workspace.worktree", `{"branch":"feat/x"}`); err != nil {
		t.Fatalf("workspace.worktree: %v", err)
	}
	wt := state.Workspace().ActiveRoot
	if _, err := invokeTool(t, reg, "file.write_patch", `{"patch": "File: new.txt\n<<<<<<< SEARCH\n=======\nhello\n>>>>>>> REPLACE"}`); err != nil {
		t.Fatalf("file.write_patch: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wt, "new.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("worktree new.txt = %q, %v; want hello", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("project root new.txt exists or stat failed unexpectedly: %v", err)
	}
}

func TestWorkspaceWorktreeReturnToRoot(t *testing.T) {
	reg, state, root := newWorktreeTestEnv(t)
	if _, err := invokeTool(t, reg, "workspace.worktree", `{"branch":"feat/x"}`); err != nil {
		t.Fatalf("enter: %v", err)
	}
	res, err := invokeTool(t, reg, "workspace.worktree", `{}`)
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	ws := state.Workspace()
	if ws.ActiveRoot != root || ws.Branch != "" {
		t.Fatalf("Workspace() = %+v, want project root with cleared branch", ws)
	}
	if !strings.Contains(res.Content, root) {
		t.Fatalf("result content %q does not state the project root", res.Content)
	}
	// The worktree is NOT removed on return (spec §7).
	if info, err := os.Stat(filepath.Join(root, ".marshal", "worktrees", "feat-x")); err != nil || !info.IsDir() {
		t.Fatalf("worktree dir was removed on return: %v", err)
	}
}

func TestWorkspaceWorktreeReusesExistingBranch(t *testing.T) {
	reg, state, root := newWorktreeTestEnv(t)
	for i := 0; i < 2; i++ {
		if _, err := invokeTool(t, reg, "workspace.worktree", `{"branch":"feat/x"}`); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if got := state.Workspace().ActiveRoot; got != wt {
		t.Fatalf("ActiveRoot = %q, want reused %q", got, wt)
	}
}

func TestWorkspaceWorktreeBranchWithoutWorktree(t *testing.T) {
	reg, _, root := newWorktreeTestEnv(t)
	cmd := exec.Command("git", "branch", "stale")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}
	_, err := invokeTool(t, reg, "workspace.worktree", `{"branch":"stale"}`)
	if err == nil || !strings.Contains(err.Error(), "already exists but has no worktree") {
		t.Fatalf("err = %v, want the branch-without-worktree guard", err)
	}
}

func TestWorkspaceWorktreeRequiresSession(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	if _, err := invokeTool(t, reg, "workspace.worktree", `{"branch":"feat/x"}`); err == nil {
		t.Fatal("want error when no session is wired")
	}
}
