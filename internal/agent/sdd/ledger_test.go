package sdd

import (
	"testing"
)

func TestLedgerRoundTrip(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ledger := NewLedger(ws)

	entries := ledger.Read()
	if len(entries) != 0 {
		t.Fatalf("initial Read = %d entries, want 0", len(entries))
	}

	if err := ledger.Append(LedgerEntry{TaskNumber: 1, BaseSHA: "abc1234", HeadSHA: "def5678"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ledger.Append(LedgerEntry{TaskNumber: 2, BaseSHA: "def5678", HeadSHA: "ghi9012"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	entries = ledger.Read()
	if len(entries) != 2 {
		t.Fatalf("Read = %d entries, want 2", len(entries))
	}
	if entries[0].TaskNumber != 1 || entries[0].BaseSHA != "abc1234" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].TaskNumber != 2 {
		t.Errorf("entry 1 = %+v", entries[1])
	}
}

func TestLedgerEmptyFileReturnsNoEntries(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	ledger := NewLedger(ws)
	entries := ledger.Read()
	if len(entries) != 0 {
		t.Fatalf("Read = %d, want 0 for missing ledger", len(entries))
	}
}

func TestLedgerCompletedTasks(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ledger := NewLedger(ws)
	ledger.Append(LedgerEntry{TaskNumber: 1, BaseSHA: "aaa", HeadSHA: "bbb"})
	ledger.Append(LedgerEntry{TaskNumber: 3, BaseSHA: "ccc", HeadSHA: "ddd"})

	completed := ledger.CompletedTasks()
	if !completed[1] || !completed[3] {
		t.Fatalf("CompletedTasks = %v, want {1:true, 3:true}", completed)
	}
	if completed[2] {
		t.Errorf("CompletedTasks[2] = true, want false")
	}
}
