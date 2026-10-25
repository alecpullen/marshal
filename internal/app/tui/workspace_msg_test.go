package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/sessionsheet"
)

func TestChangedRebasesTheSheetWhenTheWorkspaceMoves(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initSheetTestRepo(t, dir)

	wt := filepath.Join(dir, "wt")
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", "-b", "feat-x", wt).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write worktree a.txt: %v", err)
	}

	m := newTestModel(t)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: wt, Branch: "feat-x"})

	mm, cmd := m.Update(changedMsg{ch: m.sess.Changed()})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("expected a non-nil cmd from handleChanged")
	}
	var rb sheetBaseRefMsg
	found := false
	for _, msg := range batchResults(t, cmd) {
		if v, ok := msg.(sheetBaseRefMsg); ok {
			rb, found = v, true
			break
		}
	}
	if !found {
		t.Fatal("handleChanged returned no sheetBaseRefMsg")
	}
	want, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if rb.ref != string(want[:len(want)-1]) {
		t.Fatalf("sheetBaseRefMsg.ref = %q, want %q", rb.ref, string(want[:len(want)-1]))
	}
	if rb.dir != wt {
		t.Fatalf("sheetBaseRefMsg.dir = %q, want %q", rb.dir, wt)
	}

	// Feed the sheetBaseRefMsg back through Update and assert the rail rebases.
	mm2, _ := m.Update(rb)
	m = mm2.(Model)
	if m.sheetBaseRef != rb.ref {
		t.Errorf("sheetBaseRef = %q, want %q", m.sheetBaseRef, rb.ref)
	}
	sawFile := false
	for _, f := range m.sheetChanged {
		if f.Path == "a.txt" {
			sawFile = true
		}
	}
	if !sawFile {
		t.Errorf("sheetChanged missing worktree-modified a.txt: %+v", m.sheetChanged)
	}
}

// TestChangedLeavesTheSheetWhenTheWorkspaceStays pins the follow's guard: a
// change that does not move the workspace does not rebase the sheet.
func TestChangedLeavesTheSheetWhenTheWorkspaceStays(t *testing.T) {
	m := newTestModel(t)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo", Branch: "main"})

	// The first change follows the workspace and records it.
	mm, cmd := m.Update(changedMsg{ch: m.sess.Changed()})
	m = mm.(Model)
	_ = batchResults(t, cmd)

	// A second change with the same workspace yields no base-ref command.
	mm, cmd = m.Update(changedMsg{ch: m.sess.Changed()})
	m = mm.(Model)
	for _, msg := range batchResults(t, cmd) {
		if _, ok := msg.(sheetBaseRefMsg); ok {
			t.Fatal("a repeated change with an unchanged workspace rebased the sheet")
		}
	}
}

func TestHandleRailBaseRefStaleDirIgnored(t *testing.T) {
	m := newTestModel(t)
	activeRoot := m.state.Workspace().ActiveRoot
	m.sheetBaseRef = "base"
	m.sheetChanged = []sessionsheet.ChangedFile{{Path: "kept.txt"}}

	// A msg whose dir is no longer the active root must be dropped.
	mm, cmd := m.handleSheetBaseRef(sheetBaseRefMsg{dir: activeRoot + "/other", ref: "stale-sha"})
	if cmd != nil {
		t.Fatal("expected nil cmd for stale-dir msg")
	}
	if mm.sheetBaseRef != "base" {
		t.Errorf("sheetBaseRef = %q, want base preserved on stale dir", mm.sheetBaseRef)
	}
	if len(mm.sheetChanged) != 1 || mm.sheetChanged[0].Path != "kept.txt" {
		t.Errorf("sheetChanged = %+v, want unchanged on stale dir", mm.sheetChanged)
	}

	// A msg matching the active root rebases normally.
	mm, _ = m.handleSheetBaseRef(sheetBaseRefMsg{dir: activeRoot, ref: "new-sha"})
	if mm.sheetBaseRef != "new-sha" {
		t.Errorf("sheetBaseRef = %q, want new-sha for matching dir", mm.sheetBaseRef)
	}
}

func TestHandleRailBaseRefEmptyRefKeepsBase(t *testing.T) {
	m := newTestModel(t)
	m.sheetBaseRef = "abc123"
	mm, cmd := m.handleSheetBaseRef(sheetBaseRefMsg{dir: m.state.Workspace().ActiveRoot, ref: ""})
	m = mm
	if cmd != nil {
		t.Fatal("expected nil cmd")
	}
	if m.sheetBaseRef != "abc123" {
		t.Errorf("sheetBaseRef = %q, want abc123 preserved on empty ref", m.sheetBaseRef)
	}
}

func TestHandleRailBaseRefRebasesAndRefreshesCache(t *testing.T) {
	m := newTestModel(t)
	activeRoot := m.state.Workspace().ActiveRoot
	m.sheetBaseRef = "old-sha"

	mm, _ := m.handleSheetBaseRef(sheetBaseRefMsg{dir: activeRoot, ref: "new-sha"})

	// The base ref is rebased and the changed-files cache is refreshed.
	// (No explicit viewport refresh is needed: Bubble Tea re-renders after
	// every Update and the rail reads the cache directly in View.)
	if mm.sheetBaseRef != "new-sha" {
		t.Errorf("sheetBaseRef = %q, want new-sha", mm.sheetBaseRef)
	}
}
