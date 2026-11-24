// internal/tools/native/scratch_roots_test.go — scratch-root reach: the
// temporary directory is ordinary working space, the home directory is not.
package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/tools/registry"
)

// scratchToolset builds a toolset over root with the given scratch roots.
// A nil list keeps the default (the operating system's temporary directory);
// an explicit list replaces it.
func scratchToolset(t *testing.T, root string, scratch []string) (*registry.Registry, *session.State) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "filetrack.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   filetrack.New(database.SQLDB(), "test-session"),
		SessionState:  state,
		ScratchRoots:  scratch,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return reg, state
}

// TestFileToolsReachTempScratchWithoutSystem pins that the temporary
// directory is ordinary working space: a session stages build output, test
// fixtures and throwaway scripts there as a matter of course, and reaching it
// must not need a System grant.
func TestFileToolsReachTempScratchWithoutSystem(t *testing.T) {
	root := t.TempDir()
	reg, _ := scratchToolset(t, root, nil)

	scratch := t.TempDir()
	target := filepath.Join(scratch, "notes.md")
	args, err := json.Marshal(map[string]string{"path": target, "content": "scratch body\n"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write", string(args)); err != nil {
		t.Fatalf("file.write into the temporary directory without System: %v", err)
	}
	readArgs, err := json.Marshal(map[string]string{"path": target})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := invokeTool(t, reg, "file.read", string(readArgs))
	if err != nil {
		t.Fatalf("file.read from the temporary directory without System: %v", err)
	}
	if !strings.Contains(res.Content, "scratch body") {
		t.Fatalf("Content = %q, want the scratch body", res.Content)
	}
}

// TestFileToolsRefuseOutsideEveryRootWithoutScratch pins the other half: with
// no scratch root granted, a path outside the workspace is refused, and the
// refusal names the capability that would allow it rather than reading like a
// malformed path.
func TestFileToolsRefuseOutsideEveryRootWithoutScratch(t *testing.T) {
	root := t.TempDir()
	reg, _ := scratchToolset(t, root, []string{})

	target := filepath.Join(t.TempDir(), "notes.md")
	args, err := json.Marshal(map[string]string{"path": target, "content": "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = invokeTool(t, reg, "file.write", string(args))
	if err == nil {
		t.Fatal("file.write outside every root returned nil error")
	}
	if !strings.Contains(err.Error(), "System access") {
		t.Fatalf("error should name the missing capability, got: %v", err)
	}
}

// TestScratchRootDoesNotReachTheHomeDirectory pins that the home directory is
// never scratch space, wherever it happens to live. A temporary directory
// that contains the home directory — as it does in a container or a test
// sandbox — must not become a way to read a private key.
func TestScratchRootDoesNotReachTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	secret := filepath.Join(home, ".ssh", "id_rsa")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(secret, []byte("PRIVATE KEY\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	root := t.TempDir()
	reg, _ := scratchToolset(t, root, nil)

	args, err := json.Marshal(map[string]string{"path": secret})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.read", string(args)); err == nil {
		t.Fatal("file.read of a home-directory key returned nil error, want rejection")
	}
}

// TestAgentWorktreeAreaStaysWritableInsideAWorktree pins the worktree case: a
// session that has moved into a worktree holds the project checkout as a
// read-only reference, but Marshal's own worktree area stays writable, so
// the agent can still stage work in a sibling worktree.
func TestAgentWorktreeAreaStaysWritableInsideAWorktree(t *testing.T) {
	project := t.TempDir()
	worktree := filepath.Join(project, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	reg, state := scratchToolset(t, project, nil)
	state.SetWorkspace(session.Workspace{ProjectRoot: project, ActiveRoot: worktree, Branch: "feat-x"})

	sibling := filepath.Join(project, ".marshal", "worktrees", "feat-y", "notes.md")
	args, err := json.Marshal(map[string]string{"path": sibling, "content": "sibling\n"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write", string(args)); err != nil {
		t.Fatalf("file.write into the agent worktree area: %v", err)
	}

	// The project checkout itself stays a read-only reference from inside a
	// worktree: the worktree is the write target.
	checkout := filepath.Join(project, "README.md")
	checkoutArgs, err := json.Marshal(map[string]string{"path": checkout, "content": "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write", string(checkoutArgs)); err == nil {
		t.Fatal("file.write into the project checkout from inside a worktree returned nil error")
	}
}
