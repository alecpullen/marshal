package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/sidepanel"
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
	if rb.dir != wt {
		t.Fatalf("railBaseRefMsg.dir = %q, want %q", rb.dir, wt)
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

func TestHandleRailBaseRefStaleDirIgnored(t *testing.T) {
	m := newTestModel(t)
	m.railWidth = 40 // enable the rail
	activeRoot := m.state.Workspace().ActiveRoot
	m.railBaseRef = "base"
	m.railChanged = []sidepanel.ChangedFile{{Path: "kept.txt"}}

	// A msg whose dir is no longer the active root must be dropped.
	mm, cmd := m.handleRailBaseRef(railBaseRefMsg{dir: activeRoot + "/other", ref: "stale-sha"})
	if cmd != nil {
		t.Fatal("expected nil cmd for stale-dir msg")
	}
	if mm.railBaseRef != "base" {
		t.Errorf("railBaseRef = %q, want base preserved on stale dir", mm.railBaseRef)
	}
	if len(mm.railChanged) != 1 || mm.railChanged[0].Path != "kept.txt" {
		t.Errorf("railChanged = %+v, want unchanged on stale dir", mm.railChanged)
	}

	// A msg matching the active root rebases normally.
	mm, _ = m.handleRailBaseRef(railBaseRefMsg{dir: activeRoot, ref: "new-sha"})
	if mm.railBaseRef != "new-sha" {
		t.Errorf("railBaseRef = %q, want new-sha for matching dir", mm.railBaseRef)
	}
}

func TestHandleRailBaseRefEmptyRefKeepsBase(t *testing.T) {
	m := newTestModel(t)
	m.railBaseRef = "abc123"
	mm, cmd := m.handleRailBaseRef(railBaseRefMsg{dir: m.state.Workspace().ActiveRoot, ref: ""})
	m = mm
	if cmd != nil {
		t.Fatal("expected nil cmd")
	}
	if m.railBaseRef != "abc123" {
		t.Errorf("railBaseRef = %q, want abc123 preserved on empty ref", m.railBaseRef)
	}
}

func TestHandleRailBaseRefRebasesAndRefreshesCache(t *testing.T) {
	m := newTestModel(t)
	m.railWidth = 40 // enable the rail so refreshRailChanged runs
	activeRoot := m.state.Workspace().ActiveRoot
	m.railBaseRef = "old-sha"

	mm, _ := m.handleRailBaseRef(railBaseRefMsg{dir: activeRoot, ref: "new-sha"})

	// The base ref is rebased and the changed-files cache is refreshed.
	// (No explicit viewport refresh is needed: Bubble Tea re-renders after
	// every Update and the rail reads the cache directly in View.)
	if mm.railBaseRef != "new-sha" {
		t.Errorf("railBaseRef = %q, want new-sha", mm.railBaseRef)
	}
}
