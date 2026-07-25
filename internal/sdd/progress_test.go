package sdd

import (
	"strings"
	"testing"
)

func TestProgressAppendCreatesFileAndLine(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	var p Progress
	if err := p.Append(ws, "T1", "DISPATCH_IMPL", "branch", "sdd/T1", "commit", "abc123"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	lines, err := p.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "T1") || !strings.Contains(lines[0], "DISPATCH_IMPL") {
		t.Fatalf("line = %q, want to contain T1 and DISPATCH_IMPL", lines[0])
	}
	if !strings.Contains(lines[0], "branch=sdd/T1") || !strings.Contains(lines[0], "commit=abc123") {
		t.Fatalf("line = %q, want kv pairs", lines[0])
	}
	// ISO timestamp prefix check: 4-digit year, hyphen, etc. Loose check.
	if len(lines[0]) < 20 {
		t.Fatalf("line = %q, too short to contain an ISO timestamp", lines[0])
	}
}

func TestProgressAppendIsIdempotentAndAppends(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	var p Progress
	for i := 0; i < 3; i++ {
		if err := p.Append(ws, "T1", "DECOMPOSE", "k", "v"); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := p.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3 (append, not truncate)", len(lines))
	}
}

func TestProgressReadEmptyWhenNoFile(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	var p Progress
	lines, err := p.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	if lines != nil && len(lines) != 0 {
		t.Fatalf("expected nil/empty, got %v", lines)
	}
}

func TestProgressAppendDropsTrailingKey(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	var p Progress
	if err := p.Append(ws, "T1", "MERGED", "sha", "abc123", "orphan"); err != nil {
		t.Fatal(err)
	}
	lines, _ := p.Read(ws)
	// "orphan" is dropped because there is no value for it.
	if strings.Contains(lines[0], "orphan") {
		t.Fatalf("line = %q, should not contain the dropped trailing key", lines[0])
	}
}

func TestProgressTailReturnsLastN(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	var p Progress
	for i := 0; i < 5; i++ {
		if err := p.Append(ws, "T1", "EVENT", "i", string(rune('0'+i))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := p.Tail(ws, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Tail len = %d, want 2", len(got))
	}
	// The last two should be i=3 and i=4.
	if !strings.Contains(got[0], "i=3") || !strings.Contains(got[1], "i=4") {
		t.Fatalf("Tail = %v, want lines with i=3 and i=4", got)
	}
}
