package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/config"
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
