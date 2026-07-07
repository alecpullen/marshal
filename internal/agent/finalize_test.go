package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

func TestFinalizeProducesFlaggedCompletion(t *testing.T) {
	state := newTestState(t)
	prov := &scriptedProvider{responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"Here is my best answer."}}`,
	}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonExhausted)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Status != TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if got.SalvagedReason != "exhausted" {
		t.Fatalf("SalvagedReason = %q, want exhausted", got.SalvagedReason)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || last.Content != "Here is my best answer." {
		t.Fatalf("final message = %+v, want salvaged answer", last)
	}
}

func TestFinalizeSynthesizesWhenModelIgnoresDirective(t *testing.T) {
	state := newTestState(t)
	prov := &scriptedProvider{responses: []string{
		`{"rationale":"one more read","action":{"type":"tool_call","tool":"file.read","args":{"path":"x.go"}}}`,
	}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	task.Plan = []string{"Read x.go", "Patch it"}
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Status != TaskStatusCompleted || got.SalvagedReason != "stalled" {
		t.Fatalf("task = %+v, want completed+stalled", got)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || last.Content == "" {
		t.Fatalf("expected non-empty synthesized salvage message, got %+v", last)
	}
	if prov.calls != maxFinalizeAttempts {
		t.Fatalf("calls = %d, want %d (all retries exhausted before falling back)", prov.calls, maxFinalizeAttempts)
	}
}

// TestFinalizeRecoversAfterCorrection covers the fix for the "spinning
// forever" bug: a model that ignores FinalizationDirective on its first
// attempt but complies after finalizeCorrectionMessage must produce a real
// final answer, not a synthesized fallback stitched from raw tool-call JSON.
func TestFinalizeRecoversAfterCorrection(t *testing.T) {
	state := newTestState(t)
	prov := &scriptedProvider{responses: []string{
		`{"rationale":"one more read","action":{"type":"tool_call","tool":"file.read","args":{"path":"x.go"}}}`,
		`{"rationale":"ok, concluding","action":{"type":"final","content":"Here is the answer after correction."}}`,
	}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Summary != "Here is the answer after correction." {
		t.Fatalf("Summary = %q, want the model's second-attempt final content", got.Summary)
	}
	if prov.calls != 2 {
		t.Fatalf("calls = %d, want 2 (stop retrying once model complies)", prov.calls)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || last.Content != "Here is the answer after correction." {
		t.Fatalf("final message = %+v, want salvaged corrected answer", last)
	}
}

// TestExtractUsefulProseStripsToolCallEnvelope ensures synthesizeFallback never
// emits the raw tool_call JSON a non-compliant model returns, since that is
// the exact bug the user sees as "raw JSON dumped to the user".
func TestExtractUsefulProseStripsToolCallEnvelope(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "tool_call envelope returns rationale only",
			raw:  `{"rationale":"Still need to read x.go","action":{"type":"tool_call","tool":"file.read","args":{"path":"x.go"}}}`,
			want: "Still need to read x.go",
		},
		{
			name: "fenced tool_call envelope returns rationale only",
			raw:  "```json\n" + `{"rationale":"searching","action":{"type":"tool_call","tool":"repo.search","args":{}}}` + "\n```",
			want: "searching",
		},
		{
			name: "patch envelope returns rationale only",
			raw:  `{"rationale":"Applying fix","action":{"type":"patch","content":"diff"}}`,
			want: "Applying fix",
		},
		{
			name: "final envelope also returns rationale only",
			raw:  `{"rationale":"done","action":{"type":"final","content":"answer text"}}`,
			want: "done",
		},
		{
			name: "plain prose returned as-is",
			raw:  "Just some free-text from the model",
			want: "Just some free-text from the model",
		},
		{
			name: "tool_call with empty rationale returns empty",
			raw:  `{"rationale":"","action":{"type":"tool_call","tool":"repo.search","args":{}}}`,
			want: "",
		},
		{
			name: "non-JSON garbage returned as-is",
			raw:  "{not valid json",
			want: "{not valid json",
		},
		{
			name: "empty input returns empty",
			raw:  "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractUsefulProse(tc.raw)
			if got != tc.want {
				t.Fatalf("extractUsefulProse(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestFinalizeSynthesisDoesNotDumpRawToolCallJSON is the regression test for
// the "raw JSON dumped to the user" bug: when synthesizeFallback runs after
// all finalize attempts produced tool_call responses, the synthesized answer
// must NOT contain the action envelope.
func TestFinalizeSynthesisDoesNotDumpRawToolCallJSON(t *testing.T) {
	state := newTestState(t)
	toolCallJSON := `{"rationale":"I want to read x.go one more time","action":{"type":"tool_call","tool":"file.read","args":{"path":"x.go"}}}`
	prov := &scriptedProvider{responses: []string{toolCallJSON}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	task.Plan = []string{"Read x.go"}
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled)
	if err != nil {
		t.Fatalf("finalize err = %v", err)
	}
	if got.Status != TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged {
		t.Fatalf("last message not marked salvaged")
	}
	if strings.Contains(last.Content, `"tool_call"`) {
		t.Fatalf("synthesized output still contains tool_call envelope:\n%s", last.Content)
	}
	if strings.Contains(last.Content, `"file.read"`) {
		t.Fatalf("synthesized output still contains tool name:\n%s", last.Content)
	}
	if !strings.Contains(last.Content, "I want to read x.go one more time") {
		t.Fatalf("synthesized output missing rationale prose:\n%s", last.Content)
	}
}

// TestFinalizeEscalatesCorrectionMessageOnLastRetry ensures the second retry
// uses the stronger escalate warning, giving weaker models a better chance of
// complying before synthesis is triggered.
func TestFinalizeEscalatesCorrectionMessageOnLastRetry(t *testing.T) {
	state := newTestState(t)
	// Single canned response; scriptedProvider repeats it after scripts run
	// out, so all three finalize attempts see a tool_call.
	toolCallJSON := `{"rationale":"need more info","action":{"type":"tool_call","tool":"file.read","args":{"path":"y.go"}}}`
	prov := &scriptedProvider{responses: []string{toolCallJSON}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("go", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "go"}}

	if _, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled); err != nil {
		t.Fatalf("err = %v", err)
	}
	// After 3 chat calls, probe captured request messages should show the
	// final-warning escalation on the last retry (attempt index 2, which
	// was preceded by the penultimate retry's correction being appended).
	if len(prov.requests) < 3 {
		t.Fatalf("expected >=3 chat calls, got %d", len(prov.requests))
	}
	lastReq := prov.requests[len(prov.requests)-1]
	foundFinalWarn := false
	for _, m := range lastReq.Messages {
		if m.Role == schema.RoleSystem && strings.Contains(m.Content, "STOP.") {
			foundFinalWarn = true
			break
		}
	}
	if !foundFinalWarn {
		t.Fatalf("final-warning escalation not found in last request: %+v", lastReq)
	}
}

func TestFinalizeNativeUsesProseDirectly(t *testing.T) {
	state := newTestState(t)
	prov := &scriptedProvider{responses: []string{"This is the native prose answer."}}
	r := NewRunner(prov, nil, nil, state, "test-model")
	r.NativeTools = true

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonExhausted)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Status != TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if got.SalvagedReason != "exhausted" {
		t.Fatalf("SalvagedReason = %q, want exhausted", got.SalvagedReason)
	}
	if got.Summary != "This is the native prose answer." {
		t.Fatalf("Summary = %q, want prose answer", got.Summary)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || last.Content != "This is the native prose answer." {
		t.Fatalf("final message = %+v, want salvaged prose answer", last)
	}
	// Native mode must not have asked for a JSON envelope correction.
	for _, req := range prov.requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, `{"rationale"`) {
				t.Fatalf("native finalize emitted JSON-envelope correction: %q", m.Content)
			}
		}
	}
}

func TestFinalizeNativeRecoversAfterToolCall(t *testing.T) {
	state := newTestState(t)
	prov := &scriptedProvider{
		responses: []string{"Trying one more tool.", "Recovered prose answer."},
		toolCalls: [][]schema.ToolCall{
			{{ID: "call_1", Name: "file.read", Args: nil}},
			nil,
		},
	}
	r := NewRunner(prov, nil, nil, state, "test-model")
	r.NativeTools = true

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Summary != "Recovered prose answer." {
		t.Fatalf("Summary = %q, want recovered prose answer", got.Summary)
	}
	if prov.calls != 2 {
		t.Fatalf("calls = %d, want 2 (stop retrying once prose received)", prov.calls)
	}
	// The correction after the first tool-call attempt must be native vocabulary.
	foundCorrection := false
	for _, req := range prov.requests {
		for _, m := range req.Messages {
			if m.Role == schema.RoleSystem && strings.Contains(m.Content, "Do not call tools") {
				foundCorrection = true
			}
		}
	}
	if !foundCorrection {
		t.Fatal("native correction message not found")
	}
}

func TestFinalizeNativeSynthesizesWhenModelKeepsCallingTools(t *testing.T) {
	state := newTestState(t)
	prov := &scriptedProvider{
		responses: []string{"Need another read."},
		toolCalls: [][]schema.ToolCall{
			{{ID: "call_1", Name: "file.read", Args: nil}},
			{{ID: "call_1", Name: "file.read", Args: nil}},
			{{ID: "call_1", Name: "file.read", Args: nil}},
		},
	}
	r := NewRunner(prov, nil, nil, state, "test-model")
	r.NativeTools = true

	task := NewTask("do the thing", r.Now())
	task.Plan = []string{"Read the file"}
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Status != TaskStatusCompleted || got.SalvagedReason != "stalled" {
		t.Fatalf("task = %+v, want completed+stalled", got)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || last.Content == "" {
		t.Fatalf("expected non-empty synthesized salvage message, got %+v", last)
	}
	if strings.Contains(last.Content, `"file.read"`) {
		t.Fatalf("synthesized output still contains raw tool name:\n%s", last.Content)
	}
	if prov.calls != maxFinalizeAttempts {
		t.Fatalf("calls = %d, want %d (all retries exhausted before falling back)", prov.calls, maxFinalizeAttempts)
	}
}

func TestFinalizeNativeEscalatesCorrectionMessageOnLastRetry(t *testing.T) {
	state := newTestState(t)
	prov := &scriptedProvider{
		responses: []string{"Need another read."},
		toolCalls: [][]schema.ToolCall{
			{{ID: "call_1", Name: "file.read", Args: nil}},
			{{ID: "call_1", Name: "file.read", Args: nil}},
			{{ID: "call_1", Name: "file.read", Args: nil}},
		},
	}
	r := NewRunner(prov, nil, nil, state, "test-model")
	r.NativeTools = true

	task := NewTask("go", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "go"}}

	if _, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(prov.requests) < 3 {
		t.Fatalf("expected >=3 chat calls, got %d", len(prov.requests))
	}
	lastReq := prov.requests[len(prov.requests)-1]
	foundFinalWarn := false
	for _, m := range lastReq.Messages {
		if m.Role == schema.RoleSystem && strings.Contains(m.Content, "STOP.") {
			foundFinalWarn = true
			break
		}
	}
	if !foundFinalWarn {
		t.Fatalf("native final-warning escalation not found in last request: %+v", lastReq)
	}
}
