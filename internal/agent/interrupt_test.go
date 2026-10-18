package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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
