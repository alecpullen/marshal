package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// failingPatchTool registers a file.write_patch that always fails with a
// search-block-not-found error, so executeToolCall's failure path (and the
// tier-3 forced region injection) can be exercised without a real workspace.
func failingPatchTool(t *testing.T, reg *registry.Registry) {
	t.Helper()
	tool := registry.Tool{
		Name:   "file.write_patch",
		Risk:   registry.RiskWorkspaceWrite,
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	tool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, fmt.Errorf("search block not found in target.go")
	}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register file.write_patch: %v", err)
	}
}

func patchArgs(t *testing.T, patchText string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatalf("marshal patch args: %v", err)
	}
	return raw
}

// TestFailedPatchInjectsOnDiskRegionAtTier3 drives three identical failing
// patches and asserts the third tool result carries the on-disk nearest region
// for the target file.
func TestFailedPatchInjectsOnDiskRegionAtTier3(t *testing.T) {
	state := newTestState(t)
	dir := state.WorkingDir
	content := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "target.go"), []byte(content), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	reg := registry.New()
	failingPatchTool(t, reg)
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	patchText := "File: target.go\n<<<<<<< SEARCH\nthis line is not on disk\n=======\nreplacement\n>>>>>>> REPLACE\n"
	args := patchArgs(t, patchText)

	var last []schema.ChatMessage
	for i := 0; i < 3; i++ {
		msgs, err := r.executeToolCall(context.Background(), ModelAction{
			Type: ActionToolCall,
			Tool: "file.write_patch",
			Args: args,
		})
		if err != nil {
			t.Fatalf("executeToolCall %d: %v", i, err)
		}
		last = msgs
	}
	if len(last) != 1 {
		t.Fatalf("expected 1 message, got %d", len(last))
	}
	got := last[0].Content
	if !strings.Contains(got, "current on-disk content near the target") {
		t.Fatalf("3rd failure did not inject the on-disk region:\n%s", got)
	}
	if !strings.Contains(got, "println(\"hello\")") {
		t.Fatalf("injected region does not show on-disk bytes:\n%s", got)
	}
}

// TestFailedPatchRegionInjectionIsBounded uses a SEARCH block longer than the
// 300-line cap so the injected region must be truncated.
func TestFailedPatchRegionInjectionIsBounded(t *testing.T) {
	state := newTestState(t)
	dir := state.WorkingDir

	var file strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&file, "line %03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(file.String()), 0o644); err != nil {
		t.Fatalf("write big: %v", err)
	}

	var search strings.Builder
	for i := 0; i < 320; i++ {
		fmt.Fprintf(&search, "absent-token-%03d\n", i)
	}
	patchText := "File: big.txt\n<<<<<<< SEARCH\n" + search.String() + "=======\nreplacement\n>>>>>>> REPLACE\n"
	args := patchArgs(t, patchText)

	reg := registry.New()
	failingPatchTool(t, reg)
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	var last []schema.ChatMessage
	for i := 0; i < 3; i++ {
		msgs, err := r.executeToolCall(context.Background(), ModelAction{
			Type: ActionToolCall,
			Tool: "file.write_patch",
			Args: args,
		})
		if err != nil {
			t.Fatalf("executeToolCall %d: %v", i, err)
		}
		last = msgs
	}
	got := last[0].Content
	if !strings.Contains(got, "current on-disk content near the target") {
		t.Fatalf("no region injected:\n%s", got)
	}
	if !strings.Contains(got, "more lines omitted") {
		t.Fatalf("region injection was not bounded:\n%s", got)
	}
}
