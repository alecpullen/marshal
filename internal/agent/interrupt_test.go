package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// registerInterruptTestTool registers a trivial read-only tool so a turn can
// execute a real tool call and populate the per-turn tool-audit buffer.
func registerInterruptTestTool(reg *registry.Registry) {
	reg.Register(registry.Tool{
		Name: "interrupt.test", Description: "test tool", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
}

// TestInterruptedTurnPersistsMarkerAndClearsAudit pins that an Esc-cancelled
// turn persists exactly one RoleUser marker containing the goal, that the
// marker replays via buildHistoryMessages, and that the dead turn's tool-audit
// buffer is cleared so it does not flush under the next turn's final message.
func TestInterruptedTurnPersistsMarkerAndClearsAudit(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","action":{"type":"tool_call","tool":"interrupt.test","args":{}}}`,
		},
		ToolCalls: [][]schema.ToolCall{{{ID: "tc1", Name: "interrupt.test", Args: json.RawMessage(`{}`)}}},
		Errs:      []error{nil, context.Canceled},
	}
	reg := registry.New()
	registerInterruptTestTool(reg)
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do the thing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}

	// Exactly one persisted RoleUser marker containing the goal.
	var markers []session.Message
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && strings.Contains(m.Content, "[turn interrupted]") {
			markers = append(markers, m)
		}
	}
	if len(markers) != 1 {
		t.Fatalf("persisted interrupt markers = %d, want exactly 1", len(markers))
	}
	if !strings.Contains(markers[0].Content, "do the thing") {
		t.Fatalf("marker does not contain the goal: %q", markers[0].Content)
	}
	if !strings.Contains(markers[0].Content, "The current request continues from this state.") {
		t.Fatalf("marker missing closing line: %q", markers[0].Content)
	}
	// The tool that ran this turn must be listed in the marker.
	if !strings.Contains(markers[0].Content, "interrupt.test") {
		t.Fatalf("marker missing completed tool: %q", markers[0].Content)
	}

	// The tool-audit buffer must be cleared so the dead turn's entries do not
	// flush under the next turn's final assistant message. TakeToolAuditForInterrupt
	// returns "" when the buffer is already empty, so a second call proves the
	// dead turn's entries were consumed by the marker.
	if again := state.TakeToolAuditForInterrupt(); again != "" {
		t.Fatalf("tool-audit buffer not cleared after interrupt, second take = %q", again)
	}

	// The marker replays via buildHistoryMessages.
	replayed := buildHistoryMessages(state.Messages(), 0, state.Generation(), map[int64][]db.ToolAuditEntry{}, "")
	found := false
	for _, m := range replayed {
		if m.Role == schema.RoleUser && strings.Contains(m.Content, "[turn interrupted]") {
			found = true
		}
	}
	if !found {
		t.Fatal("interrupt marker not replayed by buildHistoryMessages")
	}
}

// TestInterruptedTurnCarriesUndeliveredSteering pins that steering queued but
// not yet delivered to the model lands inside the interrupt marker, tagged as
// undelivered user feedback.
//
// The steering must be queued AFTER the final loop-top drain and BEFORE the
// cancellation surfaces: anything pushed before Run is legitimately delivered
// at the first loop-top (as an enveloped "[user steering, mid-turn]" wire
// message), so it must NOT appear in the marker. OnChat(idx 1) fires at the
// start of the second model call — after the last drain, before its cancel
// error is processed — which is exactly the undelivered window.
func TestInterruptedTurnCarriesUndeliveredSteering(t *testing.T) {
	var st *session.State
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","action":{"type":"tool_call","tool":"interrupt.test","args":{}}}`,
		},
		ToolCalls: [][]schema.ToolCall{{{ID: "tc1", Name: "interrupt.test", Args: json.RawMessage(`{}`)}}},
		Errs:      []error{nil, context.Canceled},
		OnChat: func(idx int, req schema.ChatRequest) {
			if idx == 1 {
				st.PushSteering("please use a different approach")
			}
		},
	}
	reg := registry.New()
	registerInterruptTestTool(reg)
	pol := policy.NewEngine(&config.Config{}, nil)
	st = newTestState(t)
	runner := NewRunner(p, reg, pol, st, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do the thing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}

	var marker string
	for _, m := range st.Messages() {
		if m.Role == session.RoleUser && strings.Contains(m.Content, "[turn interrupted]") {
			marker = m.Content
		}
	}
	if marker == "" {
		t.Fatal("no interrupt marker persisted")
	}
	if !strings.Contains(marker, "Undelivered user feedback: please use a different approach") {
		t.Fatalf("marker missing undelivered steering: %q", marker)
	}
}

// TestMidTurnSteeringDrainWrapsWireContent pins that a steering message
// drained at loop-top is wrapped with the envelope prefix on the model wire.
func TestMidTurnSteeringDrainWrapsWireContent(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	state.PushSteering("steer me")

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.Requests) == 0 {
		t.Fatal("no chat request captured")
	}
	found := false
	for _, m := range p.Requests[0].Messages {
		if m.Role == schema.RoleUser && strings.Contains(m.Content, "[user steering, mid-turn]: steer me") {
			found = true
		}
	}
	if !found {
		t.Fatal("steering message not wrapped with the envelope prefix on the wire")
	}
}

// --- Task 5: explicit turn stops have their own targets -------------------

// fakeStopService records the turn stops a runner's execution-control seam
// issues, so a test can assert the stop reached the service BEFORE the turn was
// cancelled and that ordinary completion never called it.
type fakeStopService struct {
	mu       sync.Mutex
	stops    []string
	causes   []string
	targets  []string
	stopErr  error
	targetEr error
}

func (f *fakeStopService) StopAttached(turnID, cause string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, turnID)
	f.causes = append(f.causes, cause)
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	return []string{"job-1"}, nil
}

func (f *fakeStopService) RequestStop(executionID, cause string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, executionID)
	return f.targetEr
}

func (f *fakeStopService) snapshot() (stops, causes, targets []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stops...), append([]string(nil), f.causes...), append([]string(nil), f.targets...)
}

// TestRunnerRegistersAndReleasesTurnStop pins the registration lifecycle: the
// runner registers the turn's stop group before its provider work, and a turn
// that finishes normally releases it WITHOUT ever requesting execution stop —
// that is the whole difference between "the turn ended" and "the turn was
// stopped".
func TestRunnerRegistersAndReleasesTurnStop(t *testing.T) {
	state := newTestState(t)
	svc := &fakeStopService{}
	state.SetTurnStopService(svc)

	p := &agenttest.ScriptedProvider{
		Responses: []string{`{"rationale":"done","action":{"type":"final","content":"ok"}}`},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.ExecutionControl = NewSessionExecutionControl(state)

	if _, err := r.RunTask(context.Background(), "finish normally"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if token := state.CurrentTurnStop(); token != nil {
		t.Fatalf("turn stop registration survived a normal finish: %q", token.TurnID())
	}
	stops, _, _ := svc.snapshot()
	if len(stops) != 0 {
		t.Fatalf("normal completion requested execution stop: %v", stops)
	}
}

// TestRunnerAdoptsPreRegisteredTurnStop proves a stop that lands before the turn
// registers is retained and adopted: the turn must not start work the user
// already asked to stop.
func TestRunnerAdoptsPreRegisteredTurnStop(t *testing.T) {
	state := newTestState(t)
	svc := &fakeStopService{}
	state.SetTurnStopService(svc)

	// The user's stop arrives before any registration exists.
	if _, err := state.RequestTurnStop("stop before start"); err != nil {
		t.Fatalf("RequestTurnStop: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: scriptRepeats(2, `{"rationale":"done","action":{"type":"final","content":"ok"}}`),
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.ExecutionControl = NewSessionExecutionControl(state)

	if _, err := r.RunTask(context.Background(), "should not run"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunTask after an early stop = %v, want context.Canceled", err)
	}
	if len(p.Requests) != 0 {
		t.Fatalf("a stopped turn still called the provider %d time(s)", len(p.Requests))
	}
}

// TestTurnStopReachesServiceBeforeCancellation pins contract 3's ordering: an
// explicit stop asks the command service to mark/stop the turn's attached
// commands, and the service's answer is what the caller acts on.
func TestTurnStopReachesServiceBeforeCancellation(t *testing.T) {
	state := newTestState(t)
	svc := &fakeStopService{}
	state.SetTurnStopService(svc)

	token, pending := state.BeginTurnStop("")
	if pending != "" {
		t.Fatalf("unexpected pending cause %q on a fresh registration", pending)
	}
	ids, err := state.RequestTurnStop("user pressed stop")
	if err != nil {
		t.Fatalf("RequestTurnStop: %v", err)
	}
	stops, causes, _ := svc.snapshot()
	if len(stops) != 1 || stops[0] != token.TurnID() {
		t.Fatalf("service stops = %v, want exactly the registered turn %q", stops, token.TurnID())
	}
	if len(causes) != 1 || causes[0] != "user pressed stop" {
		t.Fatalf("service causes = %v", causes)
	}
	if len(ids) != 1 || ids[0] != "job-1" {
		t.Fatalf("stopped execution ids = %v, want the service's answer", ids)
	}
	if !token.Stopped() || token.StopCause() != "user pressed stop" {
		t.Fatalf("token did not record the accepted stop: stopped=%v cause=%q", token.Stopped(), token.StopCause())
	}

	// A repeat stop reuses the first decision instead of asking twice.
	again, err := state.RequestTurnStop("second press")
	if err != nil {
		t.Fatalf("repeat RequestTurnStop: %v", err)
	}
	if len(again) != 1 || again[0] != "job-1" {
		t.Fatalf("repeat stop = %v, want the first decision", again)
	}
	stops, _, _ = svc.snapshot()
	if len(stops) != 1 {
		t.Fatalf("repeat stop reached the service %d times, want 1", len(stops))
	}
}

// TestPlainWaitCancellationStopsNothing pins the half of contract 3 that is easy
// to get wrong: cancelling an ordinary wait (Esc during a long call, a
// deadline, an await timeout) must NOT request process termination.
func TestPlainWaitCancellationStopsNothing(t *testing.T) {
	state := newTestState(t)
	svc := &fakeStopService{}
	state.SetTurnStopService(svc)

	p := &agenttest.ScriptedProvider{
		Responses: scriptRepeats(2, `{"rationale":"done","action":{"type":"final","content":"ok"}}`),
		Errs:      []error{context.Canceled},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.ExecutionControl = NewSessionExecutionControl(state)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.RunTask(ctx, "wait was cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunTask = %v, want context.Canceled", err)
	}
	stops, _, targets := svc.snapshot()
	if len(stops) != 0 || len(targets) != 0 {
		t.Fatalf("plain wait cancellation requested a stop: turns=%v executions=%v", stops, targets)
	}
}

// TestTurnStopWinsAgainstRegistrationReplacement covers the callback-replacement
// race the plan calls out: two operations register in quick succession, and the
// stop must reach the operation that is actually current, with the old
// registration's release unable to unregister the new one.
func TestTurnStopWinsAgainstRegistrationReplacement(t *testing.T) {
	state := newTestState(t)
	svc := &fakeStopService{}
	state.SetTurnStopService(svc)

	first, _ := state.BeginTurnStop("")
	second, _ := state.BeginTurnStop("")

	// The first operation finishing must NOT unregister the second.
	if state.EndTurnStop(first) {
		t.Fatal("a replaced registration cleared the current one")
	}
	if state.CurrentTurnStop() != second {
		t.Fatal("the newer registration is not current")
	}
	if _, err := state.RequestTurnStop("stop the current operation"); err != nil {
		t.Fatalf("RequestTurnStop: %v", err)
	}
	stops, _, _ := svc.snapshot()
	if len(stops) != 1 || stops[0] != second.TurnID() {
		t.Fatalf("stops = %v, want the current registration %q (not %q)", stops, second.TurnID(), first.TurnID())
	}
	if !state.EndTurnStop(second) {
		t.Fatal("the current registration did not clear on its own finish")
	}
	if state.CurrentTurnStop() != nil {
		t.Fatal("registration survived its own release")
	}
}

// TestExecutionOriginStampedOnHandlerContext pins step 3's stamping point: the
// handler sees the turn's origin, and the call/step identity is the one the
// audit row uses, so a persisted execution record pairs back to its call.
func TestExecutionOriginStampedOnHandlerContext(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)
	svc := &fakeStopService{}
	state.SetTurnStopService(svc)

	var got session.ExecutionOrigin
	var gotOK bool
	if err := reg.Register(registry.Tool{
		Name: "origin.probe", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			got, gotOK = session.ExecutionOriginFromContext(ctx)
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"probe","action":{"type":"tool_call","tool":"origin.probe","args":{}}}`,
			`{"rationale":"done","action":{"type":"final","content":"ok"}}`,
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.ExecutionControl = NewSessionExecutionControl(state)

	if _, err := r.RunTask(context.Background(), "probe the origin"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if !gotOK {
		t.Fatal("handler did not receive an execution origin")
	}
	if got.TurnID == "" {
		t.Fatal("origin has no turn identity; a stop could not target this command")
	}
	if got.ConversationID != state.SessionID() {
		t.Fatalf("origin conversation = %q, want %q", got.ConversationID, state.SessionID())
	}
	if got.ToolCallID == "" {
		t.Fatal("origin has no tool-call identity to pair the result with")
	}
	if got.StepID == "" {
		t.Fatal("origin has no step identity")
	}
}

// TestRejectedCallDoesNotReachHandler pins the other half of step 3: a call
// rejected before approval/hook validation (here: a pre_tool_use halt) must not
// dispatch the handler at all.
func TestRejectedCallDoesNotReachHandler(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)
	svc := &fakeStopService{}
	state.SetTurnStopService(svc)

	called := false
	if err := reg.Register(registry.Tool{
		Name: "origin.rejected", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			called = true
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{`{"rationale":"probe","action":{"type":"tool_call","tool":"origin.rejected","args":{"fail":true}}}`},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.HookRunner = haltHook{}
	r.ExecutionControl = NewSessionExecutionControl(state)

	_, _ = r.RunTask(context.Background(), "halt before the handler")
	if called {
		t.Fatal("handler ran despite a pre_tool_use halt")
	}
}

// TestHandlerFailureAuditsPartialResult pins the retained-evidence half of step
// 3: a handler that fails AFTER doing work returns a partial result, and that
// evidence (output, exit code, partial summary) must appear in the audit row and
// in the model-facing error instead of being replaced by an empty result.
func TestHandlerFailureAuditsPartialResult(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)

	if err := reg.Register(registry.Tool{
		Name: "partial.fail", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			exit := 143
			return registry.ToolResult{
					Summary:         "command was stopped",
					Content:         "partial output before the stop",
					CommandExitCode: &exit,
					Sandbox:         registry.SandboxMeta{KilledReason: "stopped by request"},
				},
				errors.New("command failed")
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"run","action":{"type":"tool_call","tool":"partial.fail","args":{}}}`,
			`{"rationale":"done","action":{"type":"final","content":"ok"}}`,
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if _, err := r.RunTask(context.Background(), "fail with partial evidence"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	var event *registry.AuditEvent
	for _, ev := range state.AuditLog() {
		if ev.ToolName == "partial.fail" {
			e := ev
			event = &e
		}
	}
	if event == nil {
		t.Fatal("no audit event for the failed call")
	}
	if !strings.Contains(event.ResultContent, "partial output before the stop") {
		t.Fatalf("audit discarded the partial output: %q", event.ResultContent)
	}
	if event.CommandExitCode == nil || *event.CommandExitCode != 143 {
		t.Fatalf("audit lost the real exit code: %v", event.CommandExitCode)
	}
	if !strings.Contains(event.ResultSummary, "command was stopped") {
		t.Fatalf("audit lost the partial summary: %q", event.ResultSummary)
	}

	// The model's failure message must carry the same evidence.
	found := false
	for _, req := range p.Requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "partial output before the stop") &&
				strings.Contains(m.Content, "exit code: 143") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("model-facing error did not carry the partial evidence")
	}
}

// TestInterruptedBatchKeepsValidResultPairs pins step 5: a pre-admission
// cancellation in a native batch still produces a result for its own call ID
// and its siblings' results survive, so the tool-call/result history stays
// valid.
func TestInterruptedBatchKeepsValidResultPairs(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)

	ran := 0
	if err := reg.Register(registry.Tool{
		Name: "batch.count", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			ran++
			return registry.ToolResult{Summary: "ok", Content: "sibling content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	state.SetHold(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true

	// Every call is refused admission by the cancelled context, so each gets a
	// not-started result of its own rather than the batch collapsing to one
	// batch-level failure.
	calls := []schema.ToolCall{
		{ID: "c1", Name: "batch.count", Args: json.RawMessage(`{}`)},
		{ID: "c2", Name: "batch.count", Args: json.RawMessage(`{}`)},
		{ID: "c3", Name: "batch.count", Args: json.RawMessage(`{}`)},
	}
	msgs, err := r.executeNativeToolCalls(ctx, calls)
	if err != nil {
		t.Fatalf("executeNativeToolCalls: %v", err)
	}

	byCall := map[string]string{}
	for _, m := range msgs {
		byCall[m.ToolCallID] = m.Content
	}
	for _, id := range []string{"c1", "c2", "c3"} {
		if _, ok := byCall[id]; !ok {
			t.Fatalf("call %s produced no result; pairing is broken (results: %v)", id, byCall)
		}
		if !strings.Contains(byCall[id], "not started") {
			t.Fatalf("call %s did not report a not-started outcome: %q", id, byCall[id])
		}
	}
	if ran != 0 {
		t.Fatalf("a refused-admission call still reached its handler %d time(s)", ran)
	}
}

// TestInterruptedBatchKeepsStartedSiblings pins the other half: a call that
// already started keeps its settled result even when a sibling fails, so the
// batch is not discarded wholesale.
func TestInterruptedBatchKeepsStartedSiblings(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)

	if err := reg.Register(registry.Tool{
		Name: "batch.started", Risk: registry.RiskReadOnly, Cacheable: true,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "started sibling content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name: "batch.fails", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, errors.New("command failed")
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true

	msgs, err := r.executeNativeToolCalls(context.Background(), []schema.ToolCall{
		{ID: "s1", Name: "batch.started", Args: json.RawMessage(`{}`)},
		{ID: "s2", Name: "batch.fails", Args: json.RawMessage(`{}`)},
		{ID: "s3", Name: "batch.started", Args: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("executeNativeToolCalls: %v", err)
	}
	byCall := map[string]string{}
	for _, m := range msgs {
		byCall[m.ToolCallID] = m.Content
	}
	if !strings.Contains(byCall["s1"], "started sibling content") {
		t.Fatalf("a started call lost its real result: %q", byCall["s1"])
	}
	if _, ok := byCall["s2"]; !ok {
		t.Fatalf("the failed call produced no settled result: %v", byCall)
	}
	if !strings.Contains(byCall["s3"], "started sibling content") {
		t.Fatalf("the call after the failure was discarded: %q", byCall["s3"])
	}
}

// TestInterruptedActionBatchKeepsValidResultPairs is the envelope-path twin of
// the native batch test: a cancelled serial action must not drop the sibling
// actions' results.
func TestInterruptedActionBatchKeepsValidResultPairs(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)

	if err := reg.Register(registry.Tool{
		Name: "batch.ok", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "sibling content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name: "batch.cancel", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, context.Canceled
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	msgs, err := r.executeActions(context.Background(), []ModelAction{
		{Type: ActionToolCall, Tool: "batch.ok", Args: json.RawMessage(`{}`), ToolCallID: "a1"},
		{Type: ActionToolCall, Tool: "batch.cancel", Args: json.RawMessage(`{}`), ToolCallID: "a2"},
	})
	if err != nil {
		t.Fatalf("executeActions: %v", err)
	}
	byCall := map[string]string{}
	for _, m := range msgs {
		byCall[m.ToolCallID] = m.Content
	}
	if _, ok := byCall["a1"]; !ok {
		t.Fatalf("sibling result dropped from the action batch: %v", byCall)
	}
	if _, ok := byCall["a2"]; !ok {
		t.Fatalf("cancelled action produced no result; pairing is broken: %v", byCall)
	}
}

// TestNormalChildCompletionDoesNotStopItsCommands pins the counterweight to the
// targeted child stop: a child that finishes on its own must never invoke the
// explicit-stop wrapper, so its completion requests no stop at all.
func TestNormalChildCompletionDoesNotStopItsCommands(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)
	svc := &fakeStopService{}
	state.SetTurnStopService(svc)

	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		return &Runner{State: state}, state, nil
	}
	exec := func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		return "child report", "", nil
	}
	if err := reg.Register(NewSubagentTool(factory, nil, reg, state, WithSubagentExec(exec))); err != nil {
		t.Fatalf("register agent.run: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{"", ""},
		ToolCalls: [][]schema.ToolCall{{
			{ID: "tc1", Name: "agent.run", Args: json.RawMessage(`{"prompt":"do it","description":"child"}`)},
		}, nil},
		FinishReasons: []string{"tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{ToolCalling: true},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.ExecutionControl = NewSessionExecutionControl(state)

	if _, err := r.RunTask(context.Background(), "delegate once"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, v := range state.Subagents() {
		if _, err := state.WaitSubagent(ctx, v.ID); err != nil {
			t.Fatalf("WaitSubagent: %v", err)
		}
	}
	stops, _, targets := svc.snapshot()
	if len(stops) != 0 || len(targets) != 0 {
		t.Fatalf("normal child completion requested a stop: turns=%v executions=%v", stops, targets)
	}
}

// --- Task 6: complete tool histories across interruption ------------------

// startSteeringTestTurn opens an eligible steering turn plus one tool
// operation on state and returns the operation context and its ref. It is the
// fixture every Task 6 steering test uses: admission checks read the operation
// from this context, and SubmitSendNow captures exactly this ref.
func startSteeringTestTurn(t *testing.T, state *session.State) (context.Context, session.OperationRef) {
	t.Helper()
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{Wait: func(context.Context) ([]session.SteeringOutcome, error) { return nil, nil }}, nil
	})
	turnID, err := state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn: %v", err)
	}
	ctx, ref, err := state.BeginSteeringOperation(context.Background(), turnID, session.OperationTools)
	if err != nil {
		t.Fatalf("BeginSteeringOperation: %v", err)
	}
	return ctx, ref
}

// steeringTestTool registers a read-only tool that records each launch.
func steeringTestTool(t *testing.T, reg *registry.Registry, name string, launched *atomic.Int32) {
	t.Helper()
	if err := reg.Register(registry.Tool{
		Name: name, Description: name, Risk: registry.RiskReadOnly,
		Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			if launched != nil {
				launched.Add(1)
			}
			return registry.ToolResult{Summary: "ran " + name, Content: "content " + name}, nil
		},
	}); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

// TestSteeringNativeBatchHasResultForEveryCall pins the native path's pairing
// contract under interruption: every declared call has exactly one result, in
// declared order, the calls that completed before the intent keep their real
// content, and the calls skipped after it are settled as not started without
// being launched.
func TestSteeringNativeBatchHasResultForEveryCall(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var launched atomic.Int32
	secondRunning := make(chan struct{})
	secondRelease := make(chan struct{})
	steeringTestTool(t, reg, "batch.first", &launched)
	if err := reg.Register(registry.Tool{
		Name: "batch.second", Description: "batch.second", Risk: registry.RiskReadOnly,
		Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			launched.Add(1)
			close(secondRunning)
			// Hold the call open until the interruption has been accepted, so
			// the third call's admission is deterministic rather than a race.
			<-secondRelease
			return registry.ToolResult{Summary: "second ran", Content: "second content"}, nil
		},
	}); err != nil {
		t.Fatalf("register second: %v", err)
	}
	steeringTestTool(t, reg, "batch.third", &launched)

	ctx, ref := startSteeringTestTurn(t, state)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	done := make(chan struct{})
	var msgs []schema.ChatMessage
	go func() {
		defer close(done)
		msgs, _ = r.executeNativeToolCalls(ctx, []schema.ToolCall{
			{ID: "c1", Name: "batch.first", Args: json.RawMessage(`{}`)},
			{ID: "c2", Name: "batch.second", Args: json.RawMessage(`{}`)},
			{ID: "c3", Name: "batch.third", Args: json.RawMessage(`{}`)},
		})
	}()

	// The first call has completed and the second is in flight; accept
	// steering and only then let the second finish, so the third is guaranteed
	// to be admitted after the intent exists.
	select {
	case <-secondRunning:
	case <-time.After(2 * time.Second):
		t.Fatal("the second call never launched")
	}
	if _, err := state.SubmitSendNow(session.SendNowRequest{TurnID: ref.TurnID, RequestID: "r1", Text: "stop the batch"}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	close(secondRelease)
	<-done

	if len(msgs) != 3 {
		t.Fatalf("results = %d messages, want one settled result per declared call: %+v", len(msgs), msgs)
	}
	byCall := map[string]string{}
	for _, m := range msgs {
		byCall[m.ToolCallID] = m.Content
	}
	for _, id := range []string{"c1", "c2", "c3"} {
		if _, ok := byCall[id]; !ok {
			t.Fatalf("call %s has no result; pairing is broken: %v", id, byCall)
		}
	}
	if !strings.Contains(byCall["c1"], "content batch.first") {
		t.Fatalf("started call lost its result: %q", byCall["c1"])
	}
	if launched.Load() < 2 {
		t.Fatalf("launched = %d, want the calls admitted before the intent to have run", launched.Load())
	}
	if !strings.Contains(byCall["c3"], "not started") && !strings.Contains(byCall["c3"], "interrupted") {
		t.Fatalf("skipped call was not settled as not started: %q", byCall["c3"])
	}
	// Every interrupted call records one outcome for this operation.
	if got := len(r.takeSteeringOutcomes(ref)); got == 0 {
		t.Fatal("no steering outcomes recorded for the interrupted operation")
	}
}

// TestSteeringEnvelopeBatchHasResultForEveryCall is the envelope twin: the
// actions[] path keeps one settled slot per action, in declared order, with the
// already-run results preserved and the unlaunched calls never started.
func TestSteeringEnvelopeBatchHasResultForEveryCall(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var launched atomic.Int32
	secondRunning := make(chan struct{})
	secondRelease := make(chan struct{})
	steeringTestTool(t, reg, "env.first", &launched)
	if err := reg.Register(registry.Tool{
		Name: "env.second", Description: "env.second", Risk: registry.RiskReadOnly,
		Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			launched.Add(1)
			close(secondRunning)
			// Hold the second action open so the third is still waiting for a
			// concurrency slot when the interruption lands.
			<-secondRelease
			return registry.ToolResult{Summary: "env second ran", Content: "env second content"}, nil
		},
	}); err != nil {
		t.Fatalf("register second: %v", err)
	}
	steeringTestTool(t, reg, "env.third", &launched)

	ctx, ref := startSteeringTestTurn(t, state)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	// One slot: the third action cannot be launched until the second finishes,
	// which pins the unlaunched-slot behaviour deterministically.
	r.MaxParallelActions = 1

	done := make(chan struct{})
	var msgs []schema.ChatMessage
	go func() {
		defer close(done)
		msgs, _ = r.executeActions(ctx, []ModelAction{
			{Type: ActionToolCall, Tool: "env.first", Args: json.RawMessage(`{}`), ToolCallID: "a1"},
			{Type: ActionToolCall, Tool: "env.second", Args: json.RawMessage(`{}`), ToolCallID: "a2"},
			{Type: ActionToolCall, Tool: "env.third", Args: json.RawMessage(`{}`), ToolCallID: "a3"},
		})
	}()
	select {
	case <-secondRunning:
	case <-time.After(2 * time.Second):
		t.Fatal("the second action never launched")
	}
	if _, err := state.SubmitSendNow(session.SendNowRequest{TurnID: ref.TurnID, RequestID: "r1", Text: "stop the batch"}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	close(secondRelease)
	<-done

	if len(msgs) != 3 {
		t.Fatalf("results = %d messages, want one settled result per declared action: %+v", len(msgs), msgs)
	}
	byCall := map[string]string{}
	for _, m := range msgs {
		byCall[m.ToolCallID] = m.Content
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		if _, ok := byCall[id]; !ok {
			t.Fatalf("action %s has no result; pairing is broken: %v", id, byCall)
		}
	}
	if !strings.Contains(byCall["a1"], "content env.first") {
		t.Fatalf("started action lost its result: %q", byCall["a1"])
	}
	if strings.Contains(byCall["a3"], "ran") && !strings.Contains(byCall["a3"], "not started") {
		t.Fatalf("an unlaunched action appears to have run: %q", byCall["a3"])
	}
}

// TestSteeringParallelSemaphoreStopsUnlaunchedCalls pins the semaphore
// boundary: workers admitted before the intent settle normally, and a call that
// was still waiting for a slot is settled without ever launching.
func TestSteeringParallelSemaphoreStopsUnlaunchedCalls(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var launched atomic.Int32
	var mu sync.Mutex
	started := make(chan struct{}, 8)
	for _, name := range []string{"sem.a", "sem.b", "sem.c"} {
		name := name
		if err := reg.Register(registry.Tool{
			Name: name, Description: name, Risk: registry.RiskReadOnly,
			Schema: json.RawMessage(`{"type":"object"}`),
			Handler: func(_ context.Context, _ registry.ToolCall) (registry.ToolResult, error) {
				launched.Add(1)
				started <- struct{}{}
				time.Sleep(200 * time.Millisecond)
				return registry.ToolResult{Summary: "ran " + name, Content: "content " + name}, nil
			},
		}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	ctx, ref := startSteeringTestTurn(t, state)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	// One slot only: the third call is guaranteed to be waiting on the
	// semaphore when the interruption lands.
	r.MaxParallelActions = 1

	done := make(chan struct{})
	var msgs []schema.ChatMessage
	go func() {
		defer close(done)
		msgs, _ = r.executeActions(ctx, []ModelAction{
			{Type: ActionToolCall, Tool: "sem.a", Args: json.RawMessage(`{}`), ToolCallID: "p1"},
			{Type: ActionToolCall, Tool: "sem.b", Args: json.RawMessage(`{}`), ToolCallID: "p2"},
			{Type: ActionToolCall, Tool: "sem.c", Args: json.RawMessage(`{}`), ToolCallID: "p3"},
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("no worker ever started")
	}
	if _, err := state.SubmitSendNow(session.SendNowRequest{TurnID: ref.TurnID, RequestID: "r1", Text: "stop"}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	<-done

	mu.Lock()
	launchedCount := launched.Load()
	mu.Unlock()
	if launchedCount >= 3 {
		t.Fatalf("launched = %d, want the call waiting on the semaphore never to run", launchedCount)
	}
	if len(msgs) != 3 {
		t.Fatalf("results = %d messages, want one settled result per action: %+v", len(msgs), msgs)
	}
	byCall := map[string]string{}
	for _, m := range msgs {
		byCall[m.ToolCallID] = m.Content
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if _, ok := byCall[id]; !ok {
			t.Fatalf("action %s has no result: %v", id, byCall)
		}
	}
}

// TestSteeringPartialToolErrorPreservesEvidence pins that a canceled call that
// genuinely produced output keeps that output and its error cause, and does not
// feed the repetitive-failure ladder: an intentional interruption is not a
// repeated-tool-failure signal.
func TestSteeringPartialToolErrorPreservesEvidence(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	running := make(chan struct{})
	release := make(chan struct{})
	if err := reg.Register(registry.Tool{
		Name: "partial.tool", Description: "partial.tool", Risk: registry.RiskReadOnly,
		Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			// The call is admitted and running before the interruption: that is
			// exactly the case whose partial evidence must survive.
			close(running)
			<-release
			exit := 3
			return registry.ToolResult{
				Summary:         "partial output",
				Content:         "line one\nline two",
				FilesChanged:    []string{"a.go"},
				CommandExitCode: &exit,
			}, context.Canceled
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, _ := startSteeringTestTurn(t, state)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	done := make(chan []schema.ChatMessage, 1)
	go func() {
		msgs, err := r.executeToolCall(ctx, ModelAction{Type: ActionToolCall, Tool: "partial.tool", Args: json.RawMessage(`{}`), ToolCallID: "pc1"})
		if err != nil {
			t.Errorf("executeToolCall: %v", err)
		}
		done <- msgs
	}()
	select {
	case <-running:
	case <-time.After(2 * time.Second):
		t.Fatal("the call never started")
	}
	if _, err := state.SubmitSendNow(session.SendNowRequest{
		TurnID: state.SteeringSnapshot().TurnID, RequestID: "r1", Text: "stop",
	}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	close(release)
	msgs := <-done

	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want one settled result", len(msgs))
	}
	content := msgs[0].Content
	for _, want := range []string{"line one", "line two", "exit code: 3", "steering"} {
		if !strings.Contains(strings.ToLower(content), strings.ToLower(want)) {
			t.Fatalf("settled result missing %q: %q", want, content)
		}
	}
	if strings.Contains(content, "failed:") {
		t.Fatalf("an intentional interruption was reported as a tool failure: %q", content)
	}
	// The files a partially executed call touched must survive on the audit
	// event, so the actual effects remain inspectable.
	log := state.AuditLog()
	if len(log) == 0 || len(log[len(log)-1].FilesChanged) == 0 {
		t.Fatalf("audit log lost the partial call's changed files: %+v", log)
	}
	// The failed-repeat ladder must not have counted this call.
	r.trackerMu.Lock()
	tier := r.tracker.failedRepeatTier("partial.tool", `{}`)
	r.trackerMu.Unlock()
	if tier != 0 {
		t.Fatalf("failed-repeat tier = %d, want 0 for an intentionally interrupted call", tier)
	}
}

// TestSteeringAgentRunDispatchKeepsIndependentHandles pins that a child
// dispatched before the interruption keeps running: the batch that dispatched it
// no longer waits, but the child's handle, its result slot and its eventual
// report all survive.
func TestSteeringAgentRunDispatchKeepsIndependentHandles(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()

	childRunning := make(chan struct{})
	childRelease := make(chan struct{})
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		return &Runner{State: state}, state, nil
	}
	exec := func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		close(childRunning)
		// The child's context is the session's, not the parent operation's: an
		// interruption of the parent batch must not cancel it.
		select {
		case <-childRelease:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
		return "child finished " + prompt, "", nil
	}
	if err := reg.Register(NewSubagentTool(factory, nil, reg, state, WithSubagentExec(exec))); err != nil {
		t.Fatalf("register agent.run: %v", err)
	}

	ctx, ref := startSteeringTestTurn(t, state)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	done := make(chan []schema.ChatMessage, 1)
	go func() {
		msgs, _ := r.executeNativeToolCalls(ctx, []schema.ToolCall{
			{ID: "tc1", Name: "agent.run", Args: json.RawMessage(`{"prompt":"do it","description":"child"}`)},
		})
		done <- msgs
	}()
	select {
	case <-childRunning:
	case <-time.After(5 * time.Second):
		t.Fatal("the child never started")
	}
	if _, err := state.SubmitSendNow(session.SendNowRequest{TurnID: ref.TurnID, RequestID: "r1", Text: "steer"}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	// The dispatch itself is synchronous and already returned a handle, so the
	// batch completes; the child keeps running.
	select {
	case msgs := <-done:
		if len(msgs) != 1 {
			t.Fatalf("results = %d, want the dispatch's single settled result", len(msgs))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the batch never returned")
	}
	if len(state.Subagents()) != 1 {
		t.Fatalf("subagents = %d, want the dispatched child still registered", len(state.Subagents()))
	}
	if len(state.SubagentReports()) != 0 {
		t.Fatal("the child was reported before it finished")
	}

	close(childRelease)
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, v := range state.Subagents() {
		if _, err := state.WaitSubagent(wctx, v.ID); err != nil {
			t.Fatalf("WaitSubagent: %v", err)
		}
	}
	// Exactly one report, for a child that ran to completion after the
	// interruption of its parent batch.
	deadline := time.After(2 * time.Second)
	for len(state.SubagentReports()) == 0 {
		select {
		case <-deadline:
			t.Fatal("the independent child's completion was never reported")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if got := len(state.SubagentReports()); got != 1 {
		t.Fatalf("reports = %d, want exactly one for the surviving child", got)
	}
}

// TestSteeringApprovalWinnerStillCannotLaunchStaleTool pins the arbitration
// order the spec requires: an approval claim that won first completes, and the
// tool it approved still cannot launch once the interruption has been accepted —
// the call is settled as not started instead.
func TestSteeringApprovalWinnerStillCannotLaunchStaleTool(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	executed := atomic.Int32{}
	if err := reg.Register(registry.Tool{
		Name: "stale.tool", Description: "stale.tool", Risk: registry.RiskReadOnly,
		Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			executed.Add(1)
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, _ := startSteeringTestTurn(t, state)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	// The approval is requested and answered normally; only after the grant is
	// applied does the interruption land.
	approvalDone := make(chan struct{})
	go func() {
		defer close(approvalDone)
		deadline := time.After(2 * time.Second)
		for {
			tc := state.PendingApproval()
			if tc != nil {
				tc.Respond(session.UserApprovalDecision{Approved: true})
				return
			}
			select {
			case <-deadline:
				return
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}()

	approved, _, err := r.requestApproval(ctx, registry.Tool{Name: "stale.tool", Risk: registry.RiskReadOnly}, "stale.tool", json.RawMessage(`{}`), map[string]interface{}{}, "why")
	if err != nil {
		t.Fatalf("requestApproval: %v", err)
	}
	if !approved {
		t.Fatal("the approval claim did not win before the interruption")
	}
	<-approvalDone

	// Now the interruption is accepted for the same operation: the already
	// approved call must not launch.
	if _, err := state.SubmitSendNow(session.SendNowRequest{
		TurnID: state.SteeringSnapshot().TurnID, RequestID: "r1", Text: "stop",
	}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}

	out, err := r.executeToolCall(ctx, ModelAction{Type: ActionToolCall, Tool: "stale.tool", Args: json.RawMessage(`{}`), ToolCallID: "st1"})
	if err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if executed.Load() != 0 {
		t.Fatal("a tool approved before the interruption launched under stale intent")
	}
	if len(out) != 1 || !strings.Contains(strings.ToLower(out[0].Content), "not started") {
		t.Fatalf("stale call was not settled as not started: %+v", out)
	}
}

// TestSteeringSnapshotCancelDoesNotWarnAsFailure pins the snapshot warning
// boundary under interruption: a capture canceled by the operation's own
// steering settles quietly, while a real capture failure and a root turn
// cancellation keep their "rollback protection is missing" warning. Suppressing
// the second would hide a genuine storage fault behind the user's keystroke.
func TestSteeringSnapshotCancelDoesNotWarnAsFailure(t *testing.T) {
	t.Run("steering cancellation is silent", func(t *testing.T) {
		state := newTestState(t)
		snap := &fakeWriteSnapshotter{err: context.Canceled}
		rec := &fakeSnapshotRecorder{}
		runner, _ := writeToolRunner(t, state, snap, rec)

		state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
			return session.OperationStop{Wait: func(context.Context) ([]session.SteeringOutcome, error) { return nil, nil }}, nil
		})
		turnID, err := state.BeginSteeringTurn(context.Background())
		if err != nil {
			t.Fatalf("BeginSteeringTurn: %v", err)
		}
		ctx, ref, err := state.BeginSteeringOperation(context.Background(), turnID, session.OperationTools)
		if err != nil {
			t.Fatalf("BeginSteeringOperation: %v", err)
		}
		if _, err := state.SubmitSendNow(session.SendNowRequest{TurnID: turnID, RequestID: "r1", Text: "stop"}); err != nil {
			t.Fatalf("SubmitSendNow: %v", err)
		}
		if !session.SteeringRequested(ctx) {
			t.Fatal("the operation does not report the accepted steering")
		}

		runner.snapshotCapture(ctx, SnapshotPhasePreWrite, []string{"target.go"})
		if got := len(snapshotWarningTranscript(state)); got != 0 {
			t.Fatalf("a steering-canceled capture produced %d warnings, want 0: %v",
				got, snapshotWarningTranscript(state))
		}
		state.FinishSteeringOperation(ref, nil)
		state.EndSteeringTurn(turnID, "done")
	})

	t.Run("a real capture failure still warns", func(t *testing.T) {
		state := newTestState(t)
		snap := &fakeWriteSnapshotter{err: errors.New("budget_exhausted: workspace budget exceeded")}
		rec := &fakeSnapshotRecorder{}
		runner, _ := writeToolRunner(t, state, snap, rec)

		state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
			return session.OperationStop{Wait: func(context.Context) ([]session.SteeringOutcome, error) { return nil, nil }}, nil
		})
		turnID, err := state.BeginSteeringTurn(context.Background())
		if err != nil {
			t.Fatalf("BeginSteeringTurn: %v", err)
		}
		ctx, ref, err := state.BeginSteeringOperation(context.Background(), turnID, session.OperationTools)
		if err != nil {
			t.Fatalf("BeginSteeringOperation: %v", err)
		}
		// Intent exists, but the failure is a real storage fault: it must stay
		// visible rather than being excused as the user's own interruption.
		if _, err := state.SubmitSendNow(session.SendNowRequest{TurnID: turnID, RequestID: "r1", Text: "stop"}); err != nil {
			t.Fatalf("SubmitSendNow: %v", err)
		}

		runner.snapshotCapture(ctx, SnapshotPhasePreWrite, []string{"target.go"})
		if got := len(snapshotWarningTranscript(state)); got == 0 {
			t.Fatal("a real capture failure was suppressed as if the user had stopped it")
		}
		state.FinishSteeringOperation(ref, nil)
		state.EndSteeringTurn(turnID, "done")
	})

	t.Run("a root cancellation still warns", func(t *testing.T) {
		state := newTestState(t)
		snap := &fakeWriteSnapshotter{err: context.Canceled}
		rec := &fakeSnapshotRecorder{}
		runner, _ := writeToolRunner(t, state, snap, rec)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if snapshotIsSteeringCancellation(ctx, context.Canceled) {
			t.Fatal("a plain turn cancellation was classified as steering")
		}
		runner.snapshotCapture(ctx, SnapshotPhasePreWrite, []string{"target.go"})
		if got := len(snapshotWarningTranscript(state)); got != 0 {
			t.Fatalf("a cancelled capture produced %d visible warnings, want 0: %v", got, snapshotWarningTranscript(state))
		}
	})
}
