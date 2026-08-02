package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/permissions"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestParallelReadOnlyApprovalsAreSerialized guards the single
// State.PendingApproval slot: a parallel batch of two read-only tools that
// both require approval must not race to overwrite the slot. Before the fix,
// the second SetPendingApproval would clobber the first, whose ResponseChan
// the TUI never sees, causing the first goroutine to time out.
func TestParallelReadOnlyApprovalsAreSerialized(t *testing.T) {
	state := newTestState(t)

	var calls atomic.Int32
	reg := registry.New()
	for _, name := range []string{"readonly.a", "readonly.b"} {
		name := name
		if err := reg.Register(registry.Tool{
			Name:        name,
			Description: name,
			Schema:      json.RawMessage(`{"type":"object"}`),
			Risk:        registry.RiskReadOnly,
			Handler: func(_ context.Context, _ registry.ToolCall) (registry.ToolResult, error) {
				calls.Add(1)
				return registry.ToolResult{Summary: "ran " + name, Content: "result " + name}, nil
			},
		}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	pol := policy.NewEngine(&config.Config{}, nil)
	pol.SetRules([]permissions.Rule{
		{Permission: "readonly.a", Pattern: "readonly.a", Action: permissions.ActionAsk},
		{Permission: "readonly.b", Pattern: "readonly.b", Action: permissions.ActionAsk},
	})

	r := NewRunner(nil, reg, pol, state, "test-model")
	r.ApprovalTimeout = 200 * time.Millisecond // fail fast if an approval is lost

	// Answer every pending approval as it appears. We track the last tool we
	// answered so a stale read of the same PendingToolCall does not count as a
	// second answer before the next approval is installed.
	done := make(chan struct{})
	go func() {
		defer close(done)
		answered := 0
		var lastName string
		for answered < 2 {
			ptc := state.PendingApproval()
			if ptc == nil || ptc.Name == lastName {
				time.Sleep(time.Millisecond)
				continue
			}
			// Small delay so a race would have time to overwrite the slot.
			time.Sleep(20 * time.Millisecond)
			ptc.Respond(session.UserApprovalDecision{Approved: true})
			lastName = ptc.Name
			answered++
		}
	}()

	actions := []ModelAction{
		{Type: ActionToolCall, Tool: "readonly.a", Args: json.RawMessage(`{}`)},
		{Type: ActionToolCall, Tool: "readonly.b", Args: json.RawMessage(`{}`)},
	}
	msgs, err := r.executeActions(context.Background(), actions)
	<-done
	if err != nil {
		t.Fatalf("executeActions error: %v", err)
	}

	if calls.Load() != 2 {
		t.Fatalf("expected both tools to execute, got %d executions", calls.Load())
	}
	joined := strings.Join([]string{msgs[0].Content, msgs[1].Content}, "")
	if !strings.Contains(joined, "result readonly.a") || !strings.Contains(joined, "result readonly.b") {
		t.Fatalf("missing tool results: %v", msgs)
	}
}
