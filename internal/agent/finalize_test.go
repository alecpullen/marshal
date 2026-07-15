package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/llm/schema"
)

func TestFinalizeProducesFlaggedCompletion(t *testing.T) {
	state := newTestState(t)
	prov := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"Here is my best answer."}}`,
	}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonExhausted, nil)
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
	prov := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"one more read","action":{"type":"tool_call","tool":"file.read","args":{"path":"x.go"}}}`,
	}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	task.Plan = []string{"Read x.go", "Patch it"}
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled, nil)
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
	if prov.Calls != maxFinalizeAttempts {
		t.Fatalf("calls = %d, want %d (all retries exhausted before falling back)", prov.Calls, maxFinalizeAttempts)
	}
}

// TestFinalizeRecoversAfterCorrection covers the fix for the "spinning
// forever" bug: a model that ignores FinalizationDirective on its first
// attempt but complies after finalizeCorrectionMessage must produce a real
// final answer, not a synthesized fallback stitched from raw tool-call JSON.
func TestFinalizeRecoversAfterCorrection(t *testing.T) {
	state := newTestState(t)
	prov := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"one more read","action":{"type":"tool_call","tool":"file.read","args":{"path":"x.go"}}}`,
		`{"rationale":"ok, concluding","action":{"type":"final","content":"Here is the answer after correction."}}`,
	}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled, nil)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Summary != "Here is the answer after correction." {
		t.Fatalf("Summary = %q, want the model's second-attempt final content", got.Summary)
	}
	if prov.Calls != 2 {
		t.Fatalf("calls = %d, want 2 (stop retrying once model complies)", prov.Calls)
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
	prov := &agenttest.ScriptedProvider{Responses: []string{toolCallJSON}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	task.Plan = []string{"Read x.go"}
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled, nil)
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
	// Single canned response; agenttest.ScriptedProvider repeats it after scripts run
	// out, so all three finalize attempts see a tool_call.
	toolCallJSON := `{"rationale":"need more info","action":{"type":"tool_call","tool":"file.read","args":{"path":"y.go"}}}`
	prov := &agenttest.ScriptedProvider{Responses: []string{toolCallJSON}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("go", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "go"}}

	if _, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	// After 3 chat calls, probe captured request messages should show the
	// final-warning escalation on the last retry (attempt index 2, which
	// was preceded by the penultimate retry's correction being appended).
	if len(prov.Requests) < 3 {
		t.Fatalf("expected >=3 chat calls, got %d", len(prov.Requests))
	}
	lastReq := prov.Requests[len(prov.Requests)-1]
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
	prov := &agenttest.ScriptedProvider{Responses: []string{"This is the native prose answer."}}
	r := NewRunner(prov, nil, nil, state, "test-model")
	r.NativeTools = true

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonExhausted, nil)
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
	for _, req := range prov.Requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, `{"rationale"`) {
				t.Fatalf("native finalize emitted JSON-envelope correction: %q", m.Content)
			}
		}
	}
}

func TestFinalizeNativeRecoversAfterToolCall(t *testing.T) {
	state := newTestState(t)
	prov := &agenttest.ScriptedProvider{
		Responses: []string{"Trying one more tool.", "Recovered prose answer."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_1", Name: "file.read", Args: nil}},
			nil,
		},
	}
	r := NewRunner(prov, nil, nil, state, "test-model")
	r.NativeTools = true

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled, nil)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Summary != "Recovered prose answer." {
		t.Fatalf("Summary = %q, want recovered prose answer", got.Summary)
	}
	if prov.Calls != 2 {
		t.Fatalf("calls = %d, want 2 (stop retrying once prose received)", prov.Calls)
	}
	// The correction after the first tool-call attempt must be native vocabulary.
	foundCorrection := false
	for _, req := range prov.Requests {
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
	prov := &agenttest.ScriptedProvider{
		Responses: []string{"Need another read."},
		ToolCalls: [][]schema.ToolCall{
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

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled, nil)
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
	if prov.Calls != maxFinalizeAttempts {
		t.Fatalf("calls = %d, want %d (all retries exhausted before falling back)", prov.Calls, maxFinalizeAttempts)
	}
}

func TestFinalizeNativeEscalatesCorrectionMessageOnLastRetry(t *testing.T) {
	state := newTestState(t)
	prov := &agenttest.ScriptedProvider{
		Responses: []string{"Need another read."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_1", Name: "file.read", Args: nil}},
			{{ID: "call_1", Name: "file.read", Args: nil}},
			{{ID: "call_1", Name: "file.read", Args: nil}},
		},
	}
	r := NewRunner(prov, nil, nil, state, "test-model")
	r.NativeTools = true

	task := NewTask("go", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "go"}}

	if _, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(prov.Requests) < 3 {
		t.Fatalf("expected >=3 chat calls, got %d", len(prov.Requests))
	}
	lastReq := prov.Requests[len(prov.Requests)-1]
	foundFinalWarn := false
	for _, m := range lastReq.Messages {
		if m.Role == schema.RoleSystem && strings.Contains(m.Content, "STOP.") && strings.Contains(m.Content, "Do NOT call tools") {
			foundFinalWarn = true
			break
		}
	}
	if !foundFinalWarn {
		t.Fatalf("native final-warning escalation not found in last request: %+v", lastReq)
	}
}

// TestFinalizeReasonEmptySynthesizesFallback verifies that finalizing with
// reasonEmpty produces a flagged completion whose synthesized fallback uses
// the "model stopped producing output" preamble.
func TestFinalizeReasonEmptySynthesizesFallback(t *testing.T) {
	state := newTestState(t)
	// The model ignores the finalize directive entirely across all attempts;
	// finalize must synthesize a fallback rather than dump empty output.
	prov := &agenttest.ScriptedProvider{Responses: []string{""}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	task.Plan = []string{"Read x.go"}
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonEmpty, nil)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Status != TaskStatusCompleted || got.SalvagedReason != "empty" {
		t.Fatalf("task = %+v, want completed+empty", got)
	}
	if !strings.Contains(got.Summary, "stopped producing output") {
		t.Fatalf("Summary = %q, want the reasonEmpty preamble", got.Summary)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || !strings.Contains(last.Content, "stopped producing output") {
		t.Fatalf("final message = %+v, want salvaged empty-reason answer", last)
	}
}

// TestFinalizeNativePairsToolCallsWithToolResults verifies that when a native
// finalize attempt returns tool calls, the retry history contains the assistant
// message with ToolCalls followed by exactly one role:tool result message for
// each tool_call_id before the next model turn.
func TestFinalizeNativePairsToolCallsWithToolResults(t *testing.T) {
	state := newTestState(t)
	prov := &agenttest.ScriptedProvider{
		Responses: []string{"Need another read.", "Recovered prose answer."},
		ToolCalls: [][]schema.ToolCall{
			{
				{ID: "call_1", Name: "file.read", Args: nil},
				{ID: "call_2", Name: "repo.search", Args: nil},
			},
			nil,
		},
	}
	r := NewRunner(prov, nil, nil, state, "test-model")
	r.NativeTools = true

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled, nil)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Summary != "Recovered prose answer." {
		t.Fatalf("Summary = %q, want recovered prose answer", got.Summary)
	}
	if len(prov.Requests) < 2 {
		t.Fatalf("expected >=2 chat calls, got %d", len(prov.Requests))
	}

	// Inspect the second request (the retry after the non-compliant tool-call
	// attempt). It should contain the assistant tool-call message, then one
	// role:tool result per tool_call_id, then the correction system message.
	retryReq := prov.Requests[1]
	for i, m := range retryReq.Messages {
		if m.Role != schema.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		wantIDs := make(map[string]struct{})
		for _, call := range m.ToolCalls {
			wantIDs[call.ID] = struct{}{}
		}
		gotIDs := make(map[string]struct{})
		for j := i + 1; j < len(retryReq.Messages); j++ {
			next := retryReq.Messages[j]
			if next.Role == schema.RoleTool {
				gotIDs[next.ToolCallID] = struct{}{}
				if next.Content != nativeToolCallDisabledReply {
					t.Fatalf("role:tool result content = %q, want %q", next.Content, nativeToolCallDisabledReply)
				}
				continue
			}
			// Once we hit another assistant or system message, the tool results
			// for this assistant turn must have already been emitted.
			break
		}
		if len(gotIDs) != len(wantIDs) {
			t.Fatalf("assistant message tool_call_ids %v but saw role:tool results for %v", wantIDs, gotIDs)
		}
		for id := range wantIDs {
			if _, ok := gotIDs[id]; !ok {
				t.Fatalf("missing role:tool result for tool_call_id %q in request: %+v", id, retryReq)
			}
		}
	}
}
