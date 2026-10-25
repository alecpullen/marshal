package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
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
	batchResults(t, cmd)

	sawFile := false
	for _, f := range m.sess.Sheet(0).Changed {
		if f.Path == "a.txt" {
			sawFile = true
		}
	}
	if !sawFile {
		t.Errorf("Sheet(0).Changed missing worktree-modified a.txt: %+v", m.sess.Sheet(0).Changed)
	}
}

// TestChangedLeavesTheSheetWhenTheWorkspaceStays pins the follow's guard: a
// change that does not move the workspace does not rebase the sheet.
func TestChangedLeavesTheSheetWhenTheWorkspaceStays(t *testing.T) {
	stub := &stubSession{}
	var got []acpwire.SheetRefresh
	stub.onRefreshSheet = func(req acpwire.SheetRefresh) { got = append(got, req) }
	m := testModelStub(seamState(t, "s1"), stub)
	m.resize(100, 30)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo", Branch: "main"})

	// The first change follows the workspace and records it.
	mm, cmd := m.Update(changedMsg{ch: m.sess.Changed()})
	m = mm.(Model)
	_ = batchResults(t, cmd)
	seen := m.workspaceSeen
	if want := []acpwire.SheetRefresh{{Rebase: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the first change recorded %+v, want %+v", got, want)
	}

	// A second change with the same workspace yields no rebase.
	got = nil
	mm, cmd = m.Update(changedMsg{ch: m.sess.Changed()})
	m = mm.(Model)
	// The guard is asserted on state, not only on the collected batch: a
	// regression that issued a slow rebase could otherwise be hidden by
	// batchResults' deadline.
	if m.sess.Status().Workspace != seen {
		t.Fatalf("the workspace moved between changes: %+v, want %+v", m.sess.Status().Workspace, seen)
	}
	if m.workspaceSeen != seen {
		t.Fatalf("workspaceSeen = %+v, want the unchanged %+v", m.workspaceSeen, seen)
	}
	_ = batchResults(t, cmd)
	if len(got) != 0 {
		t.Fatalf("a repeated change with an unchanged workspace rebased the sheet: %+v", got)
	}
}
