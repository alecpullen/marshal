package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/app/tui/inspector"
)

// railFixtureRepo creates a real git repository with one committed file and one
// working-tree modification, so the snapshot path can be exercised end to end
// rather than against a stub.
func railFixtureRepo(t *testing.T) (dir, head string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	dir = t.TempDir()

	env := append(os.Environ(),
		"HOME="+dir,
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-none"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-none-system"),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.go")
	run("commit", "-q", "-m", "initial")
	head = run("rev-parse", "HEAD")

	// One working-tree modification, and one untracked file.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, head
}

// TestRefreshRailChangedKeepsAFullSnapshot pins the reason the model holds a
// Snapshot and not just the row list: a failure and a clean tree are different
// facts, and the row list cannot carry the difference.
func TestRefreshRailChangedKeepsAFullSnapshot(t *testing.T) {
	dir, head := railFixtureRepo(t)

	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(200, 60)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: dir})
	m.railBaseRef = head

	m.refreshRailChanged()

	if m.railSnapshot.Status != changedfiles.StatusOK {
		t.Fatalf("snapshot status = %q, want ok (err: %v)", m.railSnapshot.Status, m.railSnapshot.Err)
	}
	if m.railSnapshot.BaseOID == "" {
		t.Fatal("the snapshot did not record the resolved base OID")
	}
	if m.railSnapshot.Clean() {
		t.Fatalf("the snapshot reports a clean tree, but a.go was modified and untracked.txt added: %+v", m.railSnapshot.Files)
	}

	// The rows must agree with the snapshot they came from, or the rail and the
	// inspector would show different numbers for the same reading.
	rows := changedfiles.RailFiles(m.railSnapshot)
	if len(rows) != len(m.railChanged) {
		t.Fatalf("rail rows = %d, snapshot rows = %d: the cache disagrees with its own snapshot", len(m.railChanged), len(rows))
	}
	var paths []string
	for _, r := range m.railChanged {
		paths = append(paths, r.Path)
	}
	joined := strings.Join(paths, ",")
	if !strings.Contains(joined, "a.go") {
		t.Fatalf("rail rows %v do not include the modified a.go", paths)
	}
	if !strings.Contains(joined, "untracked.txt") {
		t.Fatalf("rail rows %v do not include the untracked file", paths)
	}
}

// TestRefreshRailChangedRecordsFailureRatherThanAnEmptyTree is the defect this
// task exists to fix: the old implementation returned nil on every error, so an
// unreadable repository rendered as a clean tree.
func TestRefreshRailChangedRecordsFailureRatherThanAnEmptyTree(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(200, 60)
	notARepo := t.TempDir() // not a git repository
	m.state.SetWorkspace(session.Workspace{ProjectRoot: notARepo, ActiveRoot: notARepo})
	m.railBaseRef = "HEAD"

	m.refreshRailChanged()

	if m.railSnapshot.Status == changedfiles.StatusOK {
		t.Fatalf("a non-repository reported status ok: %+v", m.railSnapshot)
	}
	if m.railSnapshot.Clean() {
		t.Fatal("a failed read reports itself as a clean tree; the two must be distinguishable")
	}
	if len(m.railChanged) != 0 {
		t.Fatalf("a failed read produced %d rail rows; the rail cannot express an error and must show none", len(m.railChanged))
	}
	if m.railSnapshot.Err == nil {
		t.Fatal("a failed read recorded no error to explain itself")
	}
}

// TestRefreshRailChangedStillRunsForADockPlacedInspector pins the narrow-terminal
// case: below the rail threshold the rail is absent, but the inspector is
// exactly where a diff is hardest to reach otherwise, so the snapshot must still
// be read.
func TestRefreshRailChangedStillRunsForADockPlacedInspector(t *testing.T) {
	dir, head := railFixtureRepo(t)

	m := newTestModel(t)
	// Below the rail threshold: the rail is NOT enabled.
	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(80, 24)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: dir})
	m.railBaseRef = head

	if m.railEnabled() {
		t.Fatalf("precondition: the rail is enabled at 80x24, so this is not testing the narrow case")
	}

	// With nothing showing changed files, the read is skipped entirely.
	m.refreshRailChanged()
	if m.railSnapshot.Status != "" {
		t.Fatalf("a snapshot was read with nothing needing it: %+v", m.railSnapshot)
	}

	// With the inspector open, it must be read even though the rail is absent.
	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	m.refreshRailChanged()
	if m.railSnapshot.Status != changedfiles.StatusOK {
		t.Fatalf("status = %q with the inspector open below the rail threshold, want ok", m.railSnapshot.Status)
	}
	if m.railSnapshot.Clean() {
		t.Fatal("the snapshot under the inspector reports a clean tree")
	}
}
