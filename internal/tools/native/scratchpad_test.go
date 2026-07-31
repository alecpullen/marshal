package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestScratchpadWriteAndReadRoundtrip(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	writeTool := tools.scratchpadWriteTool()
	args, _ := json.Marshal(map[string]any{
		"key":     "audit",
		"content": "all checks passed",
		"format":  "text",
	})
	res, err := writeTool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if !strings.Contains(res.Summary, "audit") {
		t.Fatalf("summary = %q", res.Summary)
	}

	readTool := tools.scratchpadReadTool()
	readArgs, _ := json.Marshal(map[string]any{"key": "audit"})
	res, err = readTool.Handler(context.Background(), registry.ToolCall{Args: readArgs})
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if res.Content != "all checks passed" {
		t.Fatalf("content = %q, want %q", res.Content, "all checks passed")
	}
}

func TestScratchpadWriteDefaultsFormatToText(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	writeTool := tools.scratchpadWriteTool()
	args, _ := json.Marshal(map[string]any{
		"key":     "data",
		"content": "stuff",
	})
	_, err := writeTool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}

	entry, ok := state.ScratchpadEntry("data")
	if !ok {
		t.Fatal("entry not found")
	}
	if entry.Format != "text" {
		t.Fatalf("format = %q, want text", entry.Format)
	}
}

func TestScratchpadWriteOverwritesExisting(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	writeTool := tools.scratchpadWriteTool()
	args1, _ := json.Marshal(map[string]any{
		"key": "k", "content": "old", "format": "text",
	})
	writeTool.Handler(context.Background(), registry.ToolCall{Args: args1})

	args2, _ := json.Marshal(map[string]any{
		"key": "k", "content": "new", "format": "json",
	})
	writeTool.Handler(context.Background(), registry.ToolCall{Args: args2})

	entry, ok := state.ScratchpadEntry("k")
	if !ok {
		t.Fatal("entry not found")
	}
	if entry.Content != "new" {
		t.Fatalf("content = %q, want new", entry.Content)
	}
	if entry.Format != "json" {
		t.Fatalf("format = %q, want json", entry.Format)
	}
}

func TestScratchpadReadNotFound(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	readTool := tools.scratchpadReadTool()
	args, _ := json.Marshal(map[string]any{"key": "missing"})
	_, err := readTool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %v, want it to mention the key", err)
	}
}

func TestScratchpadListEmpty(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	listTool := tools.scratchpadListTool()
	res, err := listTool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage("{}")})
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if !strings.Contains(res.Content, "No scratchpad entries") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestScratchpadListShowsEntries(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	writeTool := tools.scratchpadWriteTool()
	args1, _ := json.Marshal(map[string]any{"key": "alpha", "content": "first", "format": "text"})
	writeTool.Handler(context.Background(), registry.ToolCall{Args: args1})
	args2, _ := json.Marshal(map[string]any{"key": "beta", "content": "second", "format": "json"})
	writeTool.Handler(context.Background(), registry.ToolCall{Args: args2})

	listTool := tools.scratchpadListTool()
	res, err := listTool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage("{}")})
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if !strings.Contains(res.Content, "alpha") || !strings.Contains(res.Content, "beta") {
		t.Fatalf("content = %q", res.Content)
	}
	if !strings.Contains(res.Summary, "2 scratchpad entries") {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestScratchpadDeleteRemovesEntry(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	writeTool := tools.scratchpadWriteTool()
	writeArgs, _ := json.Marshal(map[string]any{"key": "temp", "content": "data", "format": "text"})
	writeTool.Handler(context.Background(), registry.ToolCall{Args: writeArgs})

	deleteTool := tools.scratchpadDeleteTool()
	delArgs, _ := json.Marshal(map[string]any{"key": "temp"})
	res, err := deleteTool.Handler(context.Background(), registry.ToolCall{Args: delArgs})
	if err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if !strings.Contains(res.Summary, "temp") {
		t.Fatalf("summary = %q", res.Summary)
	}

	_, ok := state.ScratchpadEntry("temp")
	if ok {
		t.Fatal("entry should have been deleted")
	}
}

func TestScratchpadDeleteIdempotent(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	deleteTool := tools.scratchpadDeleteTool()
	delArgs, _ := json.Marshal(map[string]any{"key": "nonexistent"})
	_, err := deleteTool.Handler(context.Background(), registry.ToolCall{Args: delArgs})
	if err != nil {
		t.Fatalf("delete of nonexistent key should not error: %v", err)
	}
}

func TestScratchpadWriteRejectsEmptyKey(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	writeTool := tools.scratchpadWriteTool()
	args, _ := json.Marshal(map[string]any{"key": "", "content": "data", "format": "text"})
	_, err := writeTool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestScratchpadWriteRejectsEmptyContent(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}

	writeTool := tools.scratchpadWriteTool()
	args, _ := json.Marshal(map[string]any{"key": "k", "content": "   ", "format": "text"})
	_, err := writeTool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}
