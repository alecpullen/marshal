package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentDirCreatesSelfIgnoringDir(t *testing.T) {
	root := t.TempDir()
	dir, err := AgentDir(root)
	if err != nil {
		t.Fatalf("AgentDir: %v", err)
	}
	want := filepath.Join(root, ".marshal", "worktrees")
	if dir != want {
		t.Fatalf("AgentDir = %q, want %q", dir, want)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("dir not created: info=%v err=%v", info, err)
	}
	gi, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || string(gi) != "*\n" {
		t.Fatalf(".gitignore = %q, err=%v; want \"*\\n\"", gi, err)
	}
	// Idempotent: a second call succeeds and leaves .gitignore alone.
	if _, err := AgentDir(root); err != nil {
		t.Fatalf("second AgentDir: %v", err)
	}
}
