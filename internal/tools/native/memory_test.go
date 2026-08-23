package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func openMemoryDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

func callTool(t *testing.T, tool registry.Tool, args string) (registry.ToolResult, error) {
	t.Helper()
	return tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: tool.Name, Args: json.RawMessage(args)})
}

// newMemoryToolSet builds a toolSet with a real session state whose SessionID
// matches a created agent_sessions row, so SaveMemory's FK on
// source_session_id is satisfied.
func newMemoryToolSet(t *testing.T, database *db.DB, pid int64) *toolSet {
	t.Helper()
	sessID := "sess-mem"
	if err := database.CreateSession(sessID, pid, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: sessID})
	return &toolSet{db: database, projectID: pid, sessionState: state}
}

func TestMemoryWriteSaves(t *testing.T) {
	database := openMemoryDB(t)
	pid, err := database.GetOrCreateProject("/tmp/proj-mem", "proj-mem")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	ts := newMemoryToolSet(t, database, pid)
	tool := ts.memoryWriteTool()

	res, err := callTool(t, tool, `{"kind":"decision","content":"We use SQLite for persistence."}`)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Content, "saved") {
		t.Fatalf("result = %q, want a confirmation", res.Content)
	}
	memories, err := database.GetMemories(pid)
	if err != nil {
		t.Fatalf("GetMemories: %v", err)
	}
	if len(memories) != 1 || memories[0].Kind != "decision" || memories[0].Content != "We use SQLite for persistence." {
		t.Fatalf("memories = %+v", memories)
	}
}

func TestMemoryWriteValidatesArgs(t *testing.T) {
	database := openMemoryDB(t)
	pid, err := database.GetOrCreateProject("/tmp/proj-mem", "proj-mem")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	ts := newMemoryToolSet(t, database, pid)
	tool := ts.memoryWriteTool()

	if _, err := callTool(t, tool, `{"kind":"hunch","content":"x"}`); err == nil {
		t.Fatal("invalid kind: expected error")
	}
	if _, err := callTool(t, tool, `{"kind":"fact","content":"  "}`); err == nil {
		t.Fatal("empty content: expected error")
	}
	if _, err := callTool(t, tool, `{"kind":`); err == nil {
		t.Fatal("malformed JSON: expected error")
	}
}

func TestMemoryWriteDedupes(t *testing.T) {
	database := openMemoryDB(t)
	pid, err := database.GetOrCreateProject("/tmp/proj-mem2", "proj-mem2")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	ts := newMemoryToolSet(t, database, pid)
	tool := ts.memoryWriteTool()

	if _, err := callTool(t, tool, `{"kind":"fact","content":"The repo uses SQLite."}`); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// Whitespace/case variant of the same fact must not add a row (batch-2 dedup).
	if _, err := callTool(t, tool, `{"kind":"fact","content":"the  repo uses sqlite."}`); err != nil {
		t.Fatalf("second save: %v", err)
	}
	memories, err := database.GetMemories(pid)
	if err != nil {
		t.Fatalf("GetMemories: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1 (deduped)", len(memories))
	}
}

func TestMemoryWriteToolShape(t *testing.T) {
	ts := &toolSet{}
	tool := ts.memoryWriteTool()
	if tool.Name != "memory.write" {
		t.Fatalf("Name = %q", tool.Name)
	}
	if tool.Risk != registry.RiskWorkspaceWrite {
		t.Fatalf("Risk = %q, want workspace_write", tool.Risk)
	}
}
