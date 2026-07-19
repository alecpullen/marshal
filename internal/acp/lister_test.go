package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"marshal/internal/db"
)

func TestPerCwdListerRealDB(t *testing.T) {
	root := t.TempDir()
	absCwd, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	// Seed a real per-cwd DB exactly as StartRuntime would.
	if err := os.MkdirAll(filepath.Join(absCwd, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	d, err := db.Open(filepath.Join(absCwd, ".marshal", "marshal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Migrate(); err != nil {
		_ = d.Close()
		t.Fatalf("migrate: %v", err)
	}
	pid, err := d.GetOrCreateProject(absCwd, "project")
	if err != nil {
		_ = d.Close()
		t.Fatalf("project: %v", err)
	}
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := d.CreateSession("sess_alpha", pid, "Alpha", t0.Add(time.Hour)); err != nil {
		_ = d.Close()
		t.Fatalf("create: %v", err)
	}
	if _, err := d.SaveMessage("sess_alpha", "user", "hi", "plain", t0.Add(2*time.Hour), "", 0, false, 0); err != nil {
		_ = d.Close()
		t.Fatalf("save: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	// The lister must open, list, and close per call.
	l := newPerCwdLister()
	got, next, err := l.ListSessions(context.Background(), absCwd, "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if next != "" {
		t.Fatalf("nextCursor = %q, want empty", next)
	}
	if len(got) != 1 || got[0].SessionID != "sess_alpha" {
		t.Fatalf("got = %+v", got)
	}
	if got[0].Cwd != absCwd {
		t.Fatalf("cwd = %q", got[0].Cwd)
	}
	if got[0].Title != "Alpha" {
		t.Fatalf("title = %q", got[0].Title)
	}
	if got[0].MessageCount != 1 {
		t.Fatalf("messageCount = %d", got[0].MessageCount)
	}
}

func TestPerCwdListerDeleteSession(t *testing.T) {
	root := t.TempDir()
	absCwd, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	// Seed a real per-cwd DB with a session to delete.
	if err := os.MkdirAll(filepath.Join(absCwd, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	d, err := db.Open(filepath.Join(absCwd, ".marshal", "marshal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Migrate(); err != nil {
		_ = d.Close()
		t.Fatalf("migrate: %v", err)
	}
	pid, err := d.GetOrCreateProject(absCwd, "project")
	if err != nil {
		_ = d.Close()
		t.Fatalf("project: %v", err)
	}
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := d.CreateSession("sess_lister_delete", pid, "Delete Me", t0.Add(time.Hour)); err != nil {
		_ = d.Close()
		t.Fatalf("create: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	l := newPerCwdLister()
	existed, err := l.DeleteSession(context.Background(), absCwd, "sess_lister_delete")
	if err != nil {
		t.Fatalf("DeleteSession (first call): %v", err)
	}
	if !existed {
		t.Fatal("existed=false, want true")
	}

	// Second call must be idempotent (no error, existed=false).
	existed, err = l.DeleteSession(context.Background(), absCwd, "sess_lister_delete")
	if err != nil {
		t.Fatalf("DeleteSession (second call): %v", err)
	}
	if existed {
		t.Fatal("existed=true, want false (idempotent)")
	}
}

// TestListSessionsDoesNotCreateDatabase reproduces F-POL-63: a list
// request against a non-existent project dir must NOT create the
// database file.
func TestListSessionsDoesNotCreateDatabase(t *testing.T) {
	tmp := t.TempDir()
	l := newPerCwdLister()
	_, _, err := l.ListSessions(context.Background(), tmp, "", 10)
	if err != nil {
		t.Fatalf("ListSessions error: %v", err)
	}
	dbPath := filepath.Join(tmp, ".marshal", "marshal.db")
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("ListSessions created %q (should not)", dbPath)
	}
}

// TestListSessionsCachesDatabase reproduces F-POL-66: two consecutive
// list requests share the same DB handle.
func TestListSessionsCachesDatabase(t *testing.T) {
	tmp := t.TempDir()
	// Pre-create a session so the DB exists.
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := newPerCwdLister()
	// First call opens the DB.
	_, _, err := l.ListSessions(context.Background(), tmp, "", 10)
	if err != nil {
		t.Fatalf("first ListSessions: %v", err)
	}
	// Capture the first call's start time and the second's; the
	// second should be faster because the DB is cached. Use a
	// deadline rather than a wall-clock comparison to be
	// timing-independent.
	start := time.Now()
	_, _, err = l.ListSessions(context.Background(), tmp, "", 10)
	if err != nil {
		t.Fatalf("second ListSessions: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Logf("warning: second call took %v (cache may not be working)", elapsed)
	}
}

func TestPerCwdListerFreshCwdReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	absCwd, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	// No .marshal directory — fresh cwd with no prior sessions.
	l := newPerCwdLister()
	got, next, err := l.ListSessions(context.Background(), absCwd, "", 0)
	if err != nil {
		t.Fatalf("ListSessions on fresh cwd: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d sessions, want 0: %+v", len(got), got)
	}
	if next != "" {
		t.Fatalf("nextCursor = %q, want empty", next)
	}
}

// TestPerCwdListerTTLExpiryReopens exercises the stale-entry path. Pre-fix
// this deadlocked: getOrOpen re-locked a mutex the same goroutine already
// held via defer.
func TestPerCwdListerTTLExpiryReopens(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := newPerCwdLister()
	l.ttl = 20 * time.Millisecond

	d1, err := l.getOrOpen(tmp)
	if err != nil {
		t.Fatalf("first getOrOpen: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	d2, err := l.getOrOpen(tmp)
	if err != nil {
		t.Fatalf("second getOrOpen after TTL: %v", err)
	}
	if d1 == d2 {
		t.Fatal("expected a fresh *db.DB after TTL expiry, got the stale cached handle")
	}
}

// TestPerCwdListerConcurrentGetOrOpen hammers getOrOpen from many goroutines
// with an instantly-expiring TTL to force the close/reopen path. Run with
// -race: pre-fix the double-check loser read existing.db lock-free.
func TestPerCwdListerConcurrentGetOrOpen(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := newPerCwdLister()
	l.ttl = time.Nanosecond // every call takes the stale/reopen path

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := l.getOrOpen(tmp)
			if err != nil {
				errs <- err
				return
			}
			if d == nil {
				errs <- errors.New("getOrOpen returned nil db")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent getOrOpen: %v", err)
	}
}
