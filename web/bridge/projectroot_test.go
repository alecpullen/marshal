package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitCheckout returns a temporary directory that reads as the top of a git
// checkout: the project gate and the isolation check look only for a .git
// entry, so a fixture project needs no git binary and no commit.
func gitCheckout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// A project Marshal offers is the top of a git checkout: the add route refuses
// anything else, so a plain directory can no longer be registered and sit in
// the composer as a target that cannot run.
func TestProjectAddRefusalRequiresAGitCheckout(t *testing.T) {
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := projectAddRefusal(checkout); err != nil {
		t.Fatalf("projectAddRefusal(checkout %q) = %v, want nil", checkout, err)
	}

	// A linked worktree carries a .git file rather than a directory, and it is
	// still a checkout.
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere/.git/worktrees/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := projectAddRefusal(worktree); err != nil {
		t.Fatalf("projectAddRefusal(worktree %q) = %v, want nil", worktree, err)
	}

	plain := t.TempDir()
	err := projectAddRefusal(plain)
	if err == nil {
		t.Fatalf("projectAddRefusal(%q) = nil, want a refusal", plain)
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("refusal = %q, want it to name the missing git repository", err)
	}
	if !strings.Contains(err.Error(), plain) {
		t.Errorf("refusal = %q, want it to name the root %q", err, plain)
	}
}
