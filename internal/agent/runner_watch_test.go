package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestRunPromptDrainsWatchReportsAtLoopTop guards the loop-top drain: a
// watch report pushed to the session queue before a turn runs must reach
// the model wire as a RoleUser message, and the queue must empty.
func TestRunPromptDrainsWatchReportsAtLoopTop(t *testing.T) {
	state := newTestState(t)
	// Simulate a watch that fired before the turn started.
	state.PushWatchReport("[watch build fired] kind=command interval=5s")

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"ok"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true

	if err := runner.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The report must appear as a RoleUser wire message.
	var found bool
	for _, req := range p.Requests {
		for _, m := range req.Messages {
			if m.Role == schema.RoleUser && strings.Contains(m.Content, "[watch build fired]") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("watch report not found in provider wire messages: %#v", p.Requests)
	}

	// The queue must be drained (empty) after the turn.
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("watch report queue after run = %v, want empty", got)
	}
}

// TestWatchReportQueueClearedAtTurnEnd guards the turn-end clear: a watch
// report pushed after the final loop-top drain is discarded so it is not
// double-delivered in the next turn (the persisted RoleUser message is the
// durable copy).
func TestWatchReportQueueClearedAtTurnEnd(t *testing.T) {
	state := newTestState(t)
	// Simulate a report pushed after the turn's final loop-top drain.
	state.PushWatchReport("[watch build fired] late report")
	if got := state.WatchReports(); len(got) != 1 {
		t.Fatalf("queue before clear = %v, want 1", got)
	}
	// ClearWatchReports is called by the runner's defer at turn end.
	state.ClearWatchReports()
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("queue after clear = %v, want empty", got)
	}
}
