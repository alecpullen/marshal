package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHandleWorkspaceMsgRereadsGitInfo(t *testing.T) {
	// gitinfo reads .git/HEAD via plain file I/O, so a fabricated HEAD is
	// enough — no real git repo needed.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/feat-x\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	m := Model{now: time.Now}
	m, cmd := m.handleWorkspaceMsg(workspaceMsg{activeRoot: dir})
	if cmd != nil {
		t.Fatal("expected nil cmd without a subscription")
	}
	if !m.gitInfo.InRepo || m.gitInfo.Branch != "feat-x" {
		t.Fatalf("gitInfo = %+v, want branch feat-x in repo", m.gitInfo)
	}
}
