package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/session"
)

func TestHandleWorkspaceMsgRereadsGitInfo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initRailTestRepo(t, dir)

	m := Model{now: time.Now}
	m, cmd := m.handleWorkspaceMsg(workspaceMsg{activeRoot: dir})
	if cmd == nil {
		t.Fatal("expected a non-nil cmd (railBaseRefCmd) even without a subscription")
	}
	msg := cmd()
	rb, ok := msg.(railBaseRefMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want railBaseRefMsg", msg)
	}
	want, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if rb.ref != string(want[:len(want)-1]) {
		t.Errorf("railBaseRefMsg.ref = %q, want %q", rb.ref, string(want[:len(want)-1]))
	}
	if !m.gitInfo.InRepo || m.gitInfo.Branch != "main" {
		t.Fatalf("gitInfo = %+v, want branch main in repo", m.gitInfo)
	}
}

func TestHandleWorkspaceMsgRebasesRail(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initRailTestRepo(t, dir)

	wt := filepath.Join(dir, "wt")
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", "-b", "feat-x", wt).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write worktree a.txt: %v", err)
	}

	m := newTestModel(t)
	m.railWidth = 40 // enable the rail
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: wt, Branch: "feat-x"})

	mm, cmd := m.Update(workspaceMsg{activeRoot: wt})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("expected a non-nil cmd from workspaceMsg")
	}
	msg := cmd()
	rb, ok := msg.(railBaseRefMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want railBaseRefMsg", msg)
	}
	want, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if rb.ref != string(want[:len(want)-1]) {
		t.Fatalf("railBaseRefMsg.ref = %q, want %q", rb.ref, string(want[:len(want)-1]))
	}

	// Feed the railBaseRefMsg back through Update and assert the rail rebases.
	mm2, _ := m.Update(rb)
	m = mm2.(Model)
	if m.railBaseRef != rb.ref {
		t.Errorf("railBaseRef = %q, want %q", m.railBaseRef, rb.ref)
	}
	found := false
	for _, f := range m.railChanged {
		if f.Path == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("railChanged missing worktree-modified a.txt: %+v", m.railChanged)
	}
}

func TestHandleRailBaseRefEmptyRefKeepsBase(t *testing.T) {
	m := newTestModel(t)
	m.railBaseRef = "abc123"
	mm, cmd := m.handleRailBaseRef(railBaseRefMsg{ref: ""})
	m = mm
	if cmd != nil {
		t.Fatal("expected nil cmd")
	}
	if m.railBaseRef != "abc123" {
		t.Errorf("railBaseRef = %q, want abc123 preserved on empty ref", m.railBaseRef)
	}
}
