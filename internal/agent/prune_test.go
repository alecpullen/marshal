package agent

import (
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

// TestPruneStaleToolOutputs_SupersededRead: file.read of a.go appears
// twice in the same turn; the older copy must be stubbed, the newer
// must survive. Exactly one output is pruned.
func TestPruneStaleToolOutputs_SupersededRead(t *testing.T) {
	longRead := strings.Repeat("x", 5000) // > pruneMinSizeDefault
	wire := []schema.ChatMessage{
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "file.read", Args: jsonRaw(`{"path":"a.go","start":1,"end":80}`)},
		}},
		{Role: schema.RoleTool, ToolCallID: "c1", Content: longRead},
		// A later assistant + tool result for the same path: supersedes.
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "c2", Name: "file.read", Args: jsonRaw(`{"path":"a.go","start":80,"end":160}`)},
		}},
		{Role: schema.RoleTool, ToolCallID: "c2", Content: longRead},
	}
	got, pruned := pruneStaleToolOutputs(wire, pruneMinSizeDefault)
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if !strings.HasPrefix(got[1].Content, "[pruned stale output of file.read(a.go):") {
		t.Fatalf("expected c1 result to be stubbed, got %q", got[1].Content)
	}
	if got[3].Content != longRead {
		t.Fatalf("the newer c2 result must survive intact; got %q", got[3].Content)
	}
}

// TestPruneStaleToolOutputs_NothingSuperseded: a single, non-repeated
// shell.run result must be left alone — non-read-class tools never
// collide, even when called once (which would be the case anyway).
func TestPruneStaleToolOutputs_NothingSuperseded(t *testing.T) {
	// Two unique shell.run calls must both survive (each gets a unique
	// key because their raw args differ). And a single file.read with
	// no successor must also survive.
	shellOut := strings.Repeat("ok\n", 500)
	readOut := strings.Repeat("a", 5000)
	wire := []schema.ChatMessage{
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "s1", Name: "shell.run", Args: jsonRaw(`{"command":"go test ./..."}`)}}},
		{Role: schema.RoleTool, ToolCallID: "s1", Content: shellOut},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "s2", Name: "shell.run", Args: jsonRaw(`{"command":"go vet ./..."}`)}}},
		{Role: schema.RoleTool, ToolCallID: "s2", Content: shellOut},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "r1", Name: "file.read", Args: jsonRaw(`{"path":"b.go"}`)}}},
		{Role: schema.RoleTool, ToolCallID: "r1", Content: readOut},
	}
	got, pruned := pruneStaleToolOutputs(wire, pruneMinSizeDefault)
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0 (nothing superseded)", pruned)
	}
	if got[1].Content != shellOut || got[3].Content != shellOut || got[5].Content != readOut {
		t.Fatalf("outputs were rewritten unexpectedly")
	}
}

// TestPruneStaleToolOutputs_ShortIsKept even when superseded — short
// results cost little to retain and may carry error context.
func TestPruneStaleToolOutputs_ShortIsKept(t *testing.T) {
	wire := []schema.ChatMessage{
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "file.read", Args: jsonRaw(`{"path":"a.go"}`)}}},
		{Role: schema.RoleTool, ToolCallID: "c1", Content: "tiny old read"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c2", Name: "file.read", Args: jsonRaw(`{"path":"a.go"}`)}}},
		{Role: schema.RoleTool, ToolCallID: "c2", Content: strings.Repeat("x", 5000)},
	}
	_, pruned := pruneStaleToolOutputs(wire, pruneMinSizeDefault)
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0 (old result is below minSize)", pruned)
	}
}

// TestPruneStaleToolOutputs_DifferentPathsKept: same tool, different
// paths are distinct keys and must both survive.
func TestPruneStaleToolOutputs_DifferentPathsKept(t *testing.T) {
	a := strings.Repeat("a", 5000)
	b := strings.Repeat("b", 5000)
	wire := []schema.ChatMessage{
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "file.read", Args: jsonRaw(`{"path":"a.go"}`)}}},
		{Role: schema.RoleTool, ToolCallID: "c1", Content: a},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c2", Name: "file.read", Args: jsonRaw(`{"path":"b.go"}`)}}},
		{Role: schema.RoleTool, ToolCallID: "c2", Content: b},
	}
	_, pruned := pruneStaleToolOutputs(wire, pruneMinSizeDefault)
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0 (different paths)", pruned)
	}
}
