package acp

import (
	"context"
	"os"
	"path/filepath"
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
