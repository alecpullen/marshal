package db

import (
	"testing"
	"time"
)

// TestSaveAndLoadTurnToolAudit covers the round-trip used by the
// cross-turn ledger: each (session, message, seq) row is written and
// read back identically, ORDER BY seq is enforced on read, and a
// re-save replaces the previous rows.
func TestSaveAndLoadTurnToolAudit(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	const sessionID = "sess-audit"
	if err := db.CreateSession(sessionID, projectID, "ledger test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	msgID, err := db.SaveMessage(sessionID, "assistant", "final answer", "markdown", time.Now().UTC(), "", 0, true, 0)
	if err != nil {
		t.Fatalf("save message: %v", err)
	}

	entries := []ToolAuditEntry{
		{Seq: 1, Tool: "file.read", Summary: "a.go:1-80 (412t)", Ok: true, Tokens: 412},
		{Seq: 2, Tool: "file.write_patch", Summary: "b.go", Ok: true, Tokens: 80},
		{Seq: 3, Tool: "shell.run", Summary: "go test ./...", Ok: true, Tokens: 320},
		{Seq: 4, Tool: "agent.run", Summary: "review", Ok: true, Tokens: 120, Content: "Looks good."},
	}
	if err := db.SaveTurnToolAudit(sessionID, msgID, entries); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := db.LoadTurnToolAudit(sessionID, msgID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(entries))
	}
	for i, want := range entries {
		if got[i] != want {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want)
		}
	}

	// Re-save with different entries should replace (not append).
	if err := db.SaveTurnToolAudit(sessionID, msgID, []ToolAuditEntry{
		{Seq: 1, Tool: "shell.run", Summary: "go vet ./...", Ok: false, Tokens: 50},
	}); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	got, err = db.LoadTurnToolAudit(sessionID, msgID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "go vet ./..." || got[0].Ok {
		t.Fatalf("re-save did not replace previous rows: %+v", got)
	}

	// LoadAll flattens by message DB id.
	all, err := db.LoadAllTurnToolAudit(sessionID)
	if err != nil {
		t.Fatalf("loadall: %v", err)
	}
	if n := len(all[msgID]); n != 1 {
		t.Fatalf("loadall msg entries = %d, want 1", n)
	}
}
