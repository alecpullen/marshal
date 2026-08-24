package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func TestRecallToolEnabled_DisabledWhenRolloverOff(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: false, RecallToolEnabled: "always"}
	if recallToolEnabled(cfg) {
		t.Error("recallToolEnabled = true, want false (rollover disabled)")
	}
}

func TestRecallToolEnabled_Never(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "never"}
	if recallToolEnabled(cfg) {
		t.Error("recallToolEnabled = true, want false (never)")
	}
}

func TestRecallToolEnabled_Always(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "always"}
	if !recallToolEnabled(cfg) {
		t.Error("recallToolEnabled = false, want true (always)")
	}
}

func TestRecallToolEnabled_AutoContextPercent(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "auto", Policy: "context_percent"}
	if !recallToolEnabled(cfg) {
		t.Error("recallToolEnabled = false, want true (auto + context_percent)")
	}
}

func TestRecallToolEnabled_AutoTurnCount(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "auto", Policy: "turn_count"}
	if !recallToolEnabled(cfg) {
		t.Error("recallToolEnabled = false, want true (auto + turn_count)")
	}
}

func TestRecallToolEnabled_AutoCallerCheckpoint(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "auto", Policy: "caller_checkpoint"}
	if recallToolEnabled(cfg) {
		t.Error("recallToolEnabled = true, want false (auto + caller_checkpoint)")
	}
}

func TestRecallToolEnabled_UnknownRecallToolValue(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "unknown"}
	if recallToolEnabled(cfg) {
		t.Error("recallToolEnabled = true, want false (unknown value)")
	}
}

func TestNewRecallTool_DisabledReturnsError(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: false, RecallToolEnabled: "always"}
	tool := NewRecallTool(nil, cfg)
	result, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "recall_history",
		Args: json.RawMessage(`{"query":"test"}`),
	})
	if err == nil {
		t.Fatal("expected error for disabled tool, got nil")
	}
	if !strings.Contains(err.Error(), "disabled by rollover config") {
		t.Fatalf("error = %q, want 'disabled by rollover config'", err.Error())
	}
	if result.Content != "" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
}

func TestNewRecallTool_EmptyQueryReturnsError(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "always"}
	tool := NewRecallTool(nil, cfg)
	result, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "recall_history",
		Args: json.RawMessage(`{"query":""}`),
	})
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("error = %q, want 'query is required'", err.Error())
	}
	if result.Content != "" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
}

func TestNewRecallTool_NameAndDescription(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "always"}
	tool := NewRecallTool(nil, cfg)
	if tool.Name != "recall_history" {
		t.Fatalf("Name = %q, want %q", tool.Name, "recall_history")
	}
	if tool.Description == "" {
		t.Fatal("Description is empty")
	}
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("Risk = %q, want %q", tool.Risk, registry.RiskReadOnly)
	}
	if len(tool.Schema) == 0 {
		t.Fatal("Schema is empty")
	}
}

func TestNewRecallTool_InvalidArgsReturnsError(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "always"}
	tool := NewRecallTool(nil, cfg)
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "recall_history",
		Args: json.RawMessage(`not json`),
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// newTestRecallDB creates an in-memory SQLite DB with a session and a
// generation, then archives a single turn with the given content. It returns
// the open DB (caller must Close) and a tool wired to it.
func newTestRecallDB(t *testing.T, content string) (*db.DB, registry.Tool) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/test/repo", "test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "recall-trunc-test-session"
	if err := database.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	now := time.Now().UTC()
	gen := db.Generation{
		ID:        "recall-trunc-gen",
		SessionID: sessionID,
		Seq:       1,
		StartedAt: now,
	}
	if err := database.BeginGeneration(gen); err != nil {
		t.Fatalf("BeginGeneration failed: %v", err)
	}
	turns := []db.ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: content, ToolCalls: "", CreatedAt: now},
	}
	if err := database.ArchiveTurns(gen.ID, turns, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns failed: %v", err)
	}
	cfg := config.RolloverConfig{Enabled: true, RecallToolEnabled: "always"}
	tool := NewRecallTool(database, cfg)
	return database, tool
}

func TestRecallHistory_TruncatesOversizedHits(t *testing.T) {
	// One enormous archived turn would otherwise blow the window the tool
	// exists to protect. The handler must cap per-hit excerpts and the
	// overall result.
	giantContent := strings.Repeat("giant tool output line\n", 5000)
	database, tool := newTestRecallDB(t, giantContent)
	defer database.Close()

	result, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "recall_history",
		Args: json.RawMessage(`{"query":"giant"}`),
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if len(result.Content) > recallMaxChars+1000 {
		t.Fatalf("result length = %d, want <= %d (bounded)", len(result.Content), recallMaxChars+1000)
	}
	if !strings.Contains(result.Content, "[truncated]") {
		t.Fatalf("result should contain the '[truncated]' marker for an oversized hit; got:\n%s", result.Content)
	}
}
