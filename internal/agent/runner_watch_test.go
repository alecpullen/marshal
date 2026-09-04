package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
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
	state.PushWatchReport("w1", "[watch build fired] kind=command interval=5s")

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

// TestRunPromptPersistsDrainedWatchReport guards the drain-time
// persistence: a watch report drained at loop-top is persisted as a
// RoleUser ContentTypeWatchReport message (the durable copy that
// buildHistoryMessages replays across restart), in addition to reaching
// the model wire.
func TestRunPromptPersistsDrainedWatchReport(t *testing.T) {
	state := newTestState(t)
	state.PushWatchReport("w1", "[watch build fired] kind=command interval=5s")

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

	// The drained report must be persisted as a RoleUser watch_report
	// message in the transcript.
	var persisted bool
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && m.ContentType == session.ContentTypeWatchReport &&
			strings.Contains(m.Content, "[watch build fired]") {
			persisted = true
		}
	}
	if !persisted {
		t.Fatalf("drained watch report not persisted as ContentTypeWatchReport: %#v", state.Messages())
	}

	// The queue must be drained (empty) after the turn.
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("watch report queue after run = %v, want empty", got)
	}
}

// TestWatchReportQueueClearedAtTurnEnd guards the turn-end clear: a watch
// report pushed after the final loop-top drain is drained-and-persisted at
// turn end (not silently dropped), so it survives as a replayable message
// and the queue is empty for the next turn.
func TestWatchReportQueueClearedAtTurnEnd(t *testing.T) {
	state := newTestState(t)
	// Simulate a report pushed after the turn's final loop-top drain.
	state.PushWatchReport("w1", "[watch build fired] late report")
	if got := state.WatchReports(); len(got) != 1 {
		t.Fatalf("queue before clear = %v, want 1", got)
	}
	// ClearWatchReports is called by the runner's defer at turn end.
	state.ClearWatchReports()
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("queue after clear = %v, want empty", got)
	}
}

// TestRunPromptPersistsTurnEndResidual guards the turn-end residual
// handling: a watch report pushed mid-turn AFTER the final loop-top drain
// (here, during the first Chat call) is drained-and-persisted at turn end
// as a RoleUser ContentTypeWatchReport message, not silently dropped.
func TestRunPromptPersistsTurnEndResidual(t *testing.T) {
	state := newTestState(t)

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"ok"}}`,
	}}
	// Push the report during the first Chat call — i.e. after the first
	// loop-top drain has already run (empty) and before the turn ends.
	p.OnChat = func(idx int, req schema.ChatRequest) {
		if idx == 0 {
			state.PushWatchReport("w1", "[watch build fired] late mid-turn report")
		}
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true

	if err := runner.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The residual must be persisted as a RoleUser watch_report message.
	var persisted bool
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && m.ContentType == session.ContentTypeWatchReport &&
			strings.Contains(m.Content, "late mid-turn report") {
			persisted = true
		}
	}
	if !persisted {
		t.Fatalf("turn-end residual not persisted as ContentTypeWatchReport: %#v", state.Messages())
	}

	// The queue must be empty after the turn (drained by the defer).
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("watch report queue after run = %v, want empty", got)
	}
}
