package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestNativeProgressPreflightPreservesCallsAndWorkAccounting(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	var workCalls int
	if err := reg.Register(registry.Tool{Name: "noop.tool", Risk: registry.RiskReadOnly, Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		workCalls++
		return registry.ToolResult{Summary: "ok"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	// This canonical name collides with progress.update's sanitized alias.
	// The outgoing alias and incoming resolver must still identify the fixed
	// capability exactly.
	if err := reg.Register(registry.Tool{Name: "progress_update", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		t.Fatal("alias collision tool should not be invoked")
		return registry.ToolResult{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Inspecting the change", "Done."},
		ToolCalls: [][]schema.ToolCall{{
			{ID: "p1", Name: "progress_update", Args: json.RawMessage(`{"mode":"begin","headline":"Inspecting the change"}`)},
			{ID: "w1", Name: "noop_tool", Args: json.RawMessage(`{}`)},
		}, nil},
		FinishReasons: []string{"tool_calls", "stop"},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	if err := r.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if workCalls != 1 {
		t.Fatalf("work calls = %d, want 1", workCalls)
	}
	snapshot := state.ActivitySnapshot()
	if len(snapshot.ProgressRevisions) != 1 {
		t.Fatalf("progress revisions = %+v", snapshot.ProgressRevisions)
	}
	if len(snapshot.Narrations) != 1 || snapshot.Narrations[0].Source != "structured_progress" {
		t.Fatalf("narrations = %+v, want one structured narration without duplicate headline prose", snapshot.Narrations)
	}
	if len(state.AuditLog()) != 1 || state.AuditLog()[0].ToolName != "noop.tool" {
		t.Fatalf("audit log = %+v, metadata must not create audit evidence", state.AuditLog())
	}
	for _, message := range state.Messages() {
		if message.Final && message.ToolCallCount != 1 {
			t.Fatalf("final tool call count = %d, want one real work call", message.ToolCallCount)
		}
	}
	var sawPair bool
	for _, req := range p.Requests[1:] {
		for i := 0; i+2 < len(req.Messages); i++ {
			if req.Messages[i].Role == schema.RoleAssistant && len(req.Messages[i].ToolCalls) == 2 {
				sawPair = req.Messages[i+1].ToolCallID == "p1" && req.Messages[i+2].ToolCallID == "w1"
			}
		}
	}
	if !sawPair {
		t.Fatalf("provider history lost original call order/IDs: %+v", p.Requests[1].Messages)
	}
}

func TestNativeProgressMetadataOnlyDoesNotGroundCompletion(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "I cannot verify this."},
		ToolCalls:     [][]schema.ToolCall{{{ID: "p1", Name: "progress_update", Args: json.RawMessage(`{"mode":"begin","headline":"Checking"}`)}}, nil},
		FinishReasons: []string{"tool_calls", "stop"},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.NativeTools = true
	if err := r.Run(context.Background(), "change the code"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var salvagedUnverified bool
	for _, message := range state.Messages() {
		if message.Final && message.Salvaged && message.SalvageReason == string(reasonUnverified) {
			salvagedUnverified = true
		}
	}
	if !salvagedUnverified {
		t.Fatalf("messages = %+v, metadata must not satisfy grounding", state.Messages())
	}
	if got := state.ToolBudget().Used; got != 0 {
		t.Fatalf("metadata-only used tool budget = %d, want 0", got)
	}
}

func TestNativeProgressReplyPreservesMissingProviderID(t *testing.T) {
	state := newTestState(t)
	state.AddMessage(session.RoleUser, "status", session.ContentTypePlain)
	state.BeginActivityRun(state.Messages()[0].ID)
	response := state.BeginActivityResponse()
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Registry: reg, State: state}
	_, replies, work, _, accepted := r.preflightNativeProgress(context.Background(), []schema.ToolCall{{
		ID: "", Name: "progress_update", Args: json.RawMessage(`{"mode":"begin","headline":"Checking"}`),
	}}, response)
	if !accepted || len(work) != 0 || len(replies) != 1 || replies[0].Role != schema.RoleTool || replies[0].ToolCallID != "" {
		t.Fatalf("accepted=%v work=%+v replies=%+v, missing provider IDs must still receive one paired reply", accepted, work, replies)
	}
}

func TestNativeProgressAfterWorkIsRejectedWithoutChangingOwner(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(registry.Tool{Name: "noop.tool", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{Summary: "ok"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Working."},
		ToolCalls: [][]schema.ToolCall{{
			{ID: "w1", Name: "noop_tool", Args: json.RawMessage(`{}`)},
			{ID: "p1", Name: "progress_update", Args: json.RawMessage(`{"mode":"begin","headline":"Too late"}`)},
			{ID: "p2", Name: "progress.update", Args: json.RawMessage(`{"mode":"begin","headline":"Duplicate"}`)},
		}, nil},
		FinishReasons: []string{"tool_calls", "stop"},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	if err := r.Run(context.Background(), "do work"); err != nil {
		t.Fatal(err)
	}
	if len(state.ActivitySnapshot().ProgressRevisions) != 0 {
		t.Fatalf("misplaced progress mutated state: %+v", state.ActivitySnapshot().ProgressRevisions)
	}
	var paired bool
	for _, req := range p.Requests[1:] {
		for i, message := range req.Messages {
			if message.Role == schema.RoleAssistant && len(message.ToolCalls) == 3 && i+3 < len(req.Messages) {
				paired = req.Messages[i+1].ToolCallID == "w1" && req.Messages[i+2].ToolCallID == "p1" && req.Messages[i+3].ToolCallID == "p2"
			}
		}
	}
	if !paired {
		t.Fatalf("misplaced metadata replies were dropped or reordered: %+v", p.Requests[1].Messages)
	}
	if len(state.AuditLog()) != 1 || state.AuditLog()[0].Activity.NarrationID == "" {
		t.Fatalf("work owner/audit = %+v", state.AuditLog())
	}
}

func TestTruncatedNativeProgressResponseDoesNotMutateState(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"partial"},
		ToolCalls:     [][]schema.ToolCall{{{ID: "p1", Name: "progress_update", Args: json.RawMessage(`{"mode":"begin","headline":"Partial"}`)}}, nil},
		FinishReasons: []string{"length", "stop"},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	if err := r.Run(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	if got := len(state.ActivitySnapshot().ProgressRevisions); got != 0 {
		t.Fatalf("truncated response applied %d progress updates", got)
	}
}

func TestCanceledNativeProgressResponseDoesNotMutateState(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"partial"},
		ToolCalls:     [][]schema.ToolCall{{{ID: "p1", Name: "progress_update", Args: json.RawMessage(`{"mode":"begin","headline":"Canceled"}`)}}},
		FinishReasons: []string{"tool_calls"},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Run(ctx, "question"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
	if got := len(state.ActivitySnapshot().ProgressRevisions); got != 0 {
		t.Fatalf("canceled response applied %d progress updates", got)
	}
}

func TestNativeProgressMetadataOnlyRoundsHitOverheadLimit(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	calls := make([][]schema.ToolCall, maxOverheadTurns)
	reasons := make([]string, maxOverheadTurns+1)
	responses := make([]string, maxOverheadTurns+1)
	for i := range calls {
		calls[i] = []schema.ToolCall{{ID: "p" + json.Number(string(rune('a'+i))).String(), Name: "progress_update", Args: json.RawMessage(`{"mode":"begin","headline":"Checking"}`)}}
		reasons[i] = "tool_calls"
	}
	responses[maxOverheadTurns] = "I cannot verify this."
	reasons[maxOverheadTurns] = "stop"
	p := &agenttest.ScriptedProvider{Responses: responses, ToolCalls: calls, FinishReasons: reasons}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.NativeTools = true
	if err := r.Run(context.Background(), "change the code"); !errors.Is(err, ErrMaxIterationsExceeded) {
		t.Fatalf("Run error = %v, want overhead limit because metadata cannot open finalization", err)
	}
	if p.Calls != maxOverheadTurns {
		t.Fatalf("provider calls = %d, want overhead cap %d", p.Calls, maxOverheadTurns)
	}
	if got := state.ToolBudget().Used; got != 0 {
		t.Fatalf("metadata-only used tool budget = %d, want 0", got)
	}
	for _, message := range state.Messages() {
		if message.Final {
			t.Fatalf("metadata-only loop unexpectedly finalized: %+v", message)
		}
	}
}

func TestNativeProgressIsNotExecutedDuringFinalization(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(registry.Tool{Name: "noop.tool", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{Summary: "ok"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Working.", "Attempting metadata in finalization.", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "w1", Name: "noop_tool", Args: json.RawMessage(`{}`)}},
			{{ID: "p1", Name: "progress_update", Args: json.RawMessage(`{"mode":"begin","headline":"Must not apply"}`)}},
			nil,
		},
		FinishReasons: []string{"tool_calls", "tool_calls", "stop"},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.NativeTools = true
	r.MaxToolIterations = 1
	r.SetForceClass(string(ClassQuestion))
	if err := r.Run(context.Background(), "do work"); err != nil {
		t.Fatal(err)
	}
	if got := len(state.ActivitySnapshot().ProgressRevisions); got != 0 {
		t.Fatalf("finalization applied %d progress updates", got)
	}
	if len(p.Requests) < 2 || len(p.Requests[1].Tools) != 0 {
		t.Fatalf("finalization request still advertised tools: %+v", p.Requests)
	}
}

func TestGenericProgressCallIsRefusedWithoutWorkCredit(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"status","action":{"type":"tool_call","tool":"progress.update","args":{"mode":"begin","headline":"Not allowed here"}}}`,
		`{"rationale":"answer","action":{"type":"final","content":"Answer."}}`,
	}}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.NativeTools = false
	r.SetForceClass(string(ClassQuestion))
	if err := r.Run(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	if got := len(state.ActivitySnapshot().ProgressRevisions); got != 0 {
		t.Fatalf("generic progress call applied %d revisions", got)
	}
	if got := state.ToolBudget().Used; got != 0 {
		t.Fatalf("generic progress call used work budget = %d, want 0", got)
	}
	if len(state.AuditLog()) != 0 {
		t.Fatalf("generic progress call wrote audit evidence: %+v", state.AuditLog())
	}
}

func TestGenericProgressBatchOnlyDoesNotReceiveWorkCredit(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"status","actions":[{"type":"tool_call","tool":"progress.update","args":{"mode":"begin","headline":"Not allowed"}}]}`,
		`{"rationale":"answer","action":{"type":"final","content":"Answer."}}`,
	}}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.SetForceClass(string(ClassQuestion))
	if err := r.Run(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	if got := state.ToolBudget().Used; got != 0 {
		t.Fatalf("metadata-only batch used tool budget = %d, want 0", got)
	}
	if len(state.AuditLog()) != 0 || len(state.ActivitySnapshot().ProgressRevisions) != 0 {
		t.Fatalf("metadata-only batch created work evidence: audit=%+v progress=%+v", state.AuditLog(), state.ActivitySnapshot().ProgressRevisions)
	}
}

func TestGenericProgressBatchRetainsValidReadOnlySiblings(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	workCalls := 0
	if err := reg.Register(registry.Tool{Name: "noop.tool", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		workCalls++
		return registry.ToolResult{Summary: "ok"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"status plus work","actions":[{"type":"tool_call","tool":"progress.update","args":{"mode":"begin","headline":"Not allowed"}},{"type":"tool_call","tool":"noop.tool","args":{}}]}`,
		`{"rationale":"answer","action":{"type":"final","content":"Answer."}}`,
	}}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.SetForceClass(string(ClassQuestion))
	if err := r.Run(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	if workCalls != 1 || len(state.AuditLog()) != 1 || state.AuditLog()[0].ToolName != "noop.tool" {
		t.Fatalf("workCalls=%d audit=%+v, valid read-only sibling did not execute normally", workCalls, state.AuditLog())
	}
	if len(state.ActivitySnapshot().ProgressRevisions) != 0 {
		t.Fatalf("generic metadata batch mutated progress: %+v", state.ActivitySnapshot().ProgressRevisions)
	}
	for _, message := range state.Messages() {
		if message.Final && message.ToolCallCount != 1 {
			t.Fatalf("final tool call count=%d, want the one ordinary sibling", message.ToolCallCount)
		}
	}
}
