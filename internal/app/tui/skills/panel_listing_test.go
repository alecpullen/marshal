package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/skills"
)

// TestFailedListingIsVisibleInsteadOfAnEmptyStore: a listing the session
// cannot produce says so instead of rendering as an empty store. The agent
// side fails closed on a store it cannot scan, and drawing that as "No skills
// installed" told the user their store was empty when it was unreadable.
func TestFailedListingIsVisibleInsteadOfAnEmptyStore(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(config.UserDir(home), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	// A regular file where the global skills store belongs: ReadDir fails with
	// ENOTDIR for every user, root included, so the failure is deterministic.
	global := filepath.Join(config.UserDir(home), "skills")
	if err := os.WriteFile(global, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write %s: %v", global, err)
	}

	p, _, _ := testPanel(t, home, work, true, skills.NewIndex())
	if !strings.Contains(p.ActiveList().ErrMsg, "skills unavailable") {
		t.Fatalf("error row = %q, want the failed listing named", p.ActiveList().ErrMsg)
	}
	if p.entries != nil {
		t.Fatal("a failed listing kept entries")
	}
}

// TestListingRecoveryClearsTheErrorRow: a later listing that succeeds must not
// leave the failure on screen, which would read as a store still unavailable.
func TestListingRecoveryClearsTheErrorRow(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(config.UserDir(home), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	global := filepath.Join(config.UserDir(home), "skills")
	if err := os.WriteFile(global, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write %s: %v", global, err)
	}

	p, _, _ := testPanel(t, home, work, true, skills.NewIndex())
	if p.ActiveList().ErrMsg == "" {
		t.Fatal("the failed listing left no error row to clear")
	}

	// Repair the store and refresh the way the panel does after an action.
	if err := os.Remove(global); err != nil {
		t.Fatalf("remove %s: %v", global, err)
	}
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatalf("recreate %s: %v", global, err)
	}
	p.reload()

	if p.ActiveList().ErrMsg != "" {
		t.Fatalf("error row = %q after a successful listing, want it cleared", p.ActiveList().ErrMsg)
	}
	if p.entries == nil {
		t.Fatal("a successful listing left the panel without entries")
	}
}
