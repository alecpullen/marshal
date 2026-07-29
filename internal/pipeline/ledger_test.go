package pipeline

import (
	"path/filepath"
	"testing"
)

func newTestLedger(t *testing.T) Ledger {
	t.Helper()
	return Ledger{Path: filepath.Join(t.TempDir(), "progress.md")}
}

func TestLedgerMarkCompleteAndResume(t *testing.T) {
	l := newTestLedger(t)
	if err := l.MarkComplete(1, "abcdef1234567890", "1234567890abcdef"); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if err := l.MarkComplete(2, "aaaaaaa1111111", "bbbbbbb2222222"); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	done, err := l.CompletedTasks()
	if err != nil {
		t.Fatalf("CompletedTasks: %v", err)
	}
	if !done[1] || !done[2] {
		t.Errorf("CompletedTasks = %v, want tasks 1 and 2", done)
	}
	if done[3] {
		t.Errorf("task 3 reported complete, never marked")
	}
	lines, err := l.Tail(1)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	want := "Task 2: complete (commits aaaaaaa..bbbbbbb, review clean)"
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("Tail(1) = %v, want [%q]", lines, want)
	}
}

func TestLedgerCompletedTasksMissingFile(t *testing.T) {
	l := newTestLedger(t)
	done, err := l.CompletedTasks()
	if err != nil {
		t.Fatalf("CompletedTasks on missing file: %v", err)
	}
	if len(done) != 0 {
		t.Errorf("CompletedTasks = %v, want empty", done)
	}
}

func TestLedgerMinors(t *testing.T) {
	l := newTestLedger(t)
	if err := l.Note("Task 1: implementer concern: file is getting large"); err != nil {
		t.Fatalf("Note: %v", err)
	}
	if err := l.RecordMinor(1, "magic number 100 should be a constant"); err != nil {
		t.Fatalf("RecordMinor: %v", err)
	}
	if err := l.RecordMinor(2, "comment typo"); err != nil {
		t.Fatalf("RecordMinor: %v", err)
	}
	minors, err := l.Minors()
	if err != nil {
		t.Fatalf("Minors: %v", err)
	}
	if len(minors) != 2 {
		t.Fatalf("len(Minors) = %d, want 2: %v", len(minors), minors)
	}
	if minors[0] != "Task 1 (minor): magic number 100 should be a constant" {
		t.Errorf("Minors[0] = %q", minors[0])
	}
}
