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
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// --- fixtures --------------------------------------------------------------

// sendNowGateProvider serves canned envelope responses by call index and can
// hold or gate individual calls behind test-owned barriers, so a Send now
// submission can be placed inside a specific operation window deterministically.
// The call whose index equals holdCancel blocks until its request context is
// cancelled — the shape of a real interrupted request. A call listed in gated
// blocks until the test closes the channel; entered[idx] closes when the call
// reaches the provider (after its steering submission point).
type sendNowGateProvider struct {
	mu         sync.Mutex
	requests   []schema.ChatRequest
	calls      int
	responses  []string
	holdCancel int
	gated      map[int]chan struct{}
	entered    map[int]chan struct{}
	onChat     func(idx int, req schema.ChatRequest)
}

func (p *sendNowGateProvider) Name() string { return "send-now-gate" }

func (p *sendNowGateProvider) Models(context.Context) ([]schema.ModelInfo, error) { return nil, nil }

func (p *sendNowGateProvider) Capabilities(context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *sendNowGateProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.mu.Lock()
	idx := p.calls
	p.requests = append(p.requests, req)
	p.calls++
	hold := idx == p.holdCancel
	var gate, entered chan struct{}
	if ch, ok := p.gated[idx]; ok {
		gate = ch
	}
	if ch, ok := p.entered[idx]; ok {
		entered = ch
	}
	hook := p.onChat
	p.mu.Unlock()

	if hook != nil {
		hook(idx, req)
	}
	if entered != nil {
		close(entered)
	}

	events := make(chan schema.ChatEvent)
	go func() {
		defer close(events)
		if hold {
			<-ctx.Done()
			events <- schema.ChatEvent{Type: schema.ChatEventError, Err: ctx.Err()}
			return
		}
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				events <- schema.ChatEvent{Type: schema.ChatEventError, Err: ctx.Err()}
				return
			}
		}
		if idx < len(p.responses) {
			events <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.responses[idx]}
		}
		events <- schema.ChatEvent{Type: schema.ChatEventDone, FinishReason: "stop"}
	}()
	return events, nil
}

// countingTitleManager records how many turn-start title calls the runner made.
type countingTitleManager struct {
	calls int
}

func (m *countingTitleManager) OnUserTurn(context.Context, string) { m.calls++ }

// nilSteeringHandler is the interruption handler every RunTask-level steering
// test installs: the batch's operations settle without remote side effects.
func nilSteeringHandler(context.Context, session.OperationRef) (session.OperationStop, error) {
	return session.OperationStop{Wait: func(context.Context) ([]session.SteeringOutcome, error) {
		return nil, nil
	}}, nil
}

// countPersistedUserMessages counts RoleUser messages whose exact content
// equals content — the "one goal" projection.
func countPersistedUserMessages(state *session.State, content string) int {
	n := 0
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && m.Content == content {
			n++
		}
	}
	return n
}

// countMessagesContaining counts every persisted message carrying substr.
func countMessagesContaining(state *session.State, substr string) int {
	n := 0
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, substr) {
			n++
		}
	}
	return n
}

// awaitDone waits for a RunTask result, failing the test on a hang.
func awaitRunTask(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("RunTask never returned")
		return nil
	}
}

// submitNow is the accepted Send now the tests issue.
func submitNow(t *testing.T, state *session.State, turnID, requestID, text string) {
	t.Helper()
	if _, err := state.SubmitSendNow(session.SendNowRequest{
		TurnID:    turnID,
		RequestID: requestID,
		Text:      text,
	}); err != nil {
		t.Fatalf("SubmitSendNow %q: %v", requestID, err)
	}
}

// steeringEnvelopeIndexes returns the indexes of a steering envelope in a wire.
func steeringEnvelopeIndexes(msgs []schema.ChatMessage, text string) []int {
	var out []int
	envelope := steeringEnvelope(text)
	for i, m := range msgs {
		if m.Content == envelope {
			out = append(out, i)
		}
	}
	return out
}

// --- Task 9 step 9 ----------------------------------------------------------

// TestSendNowContinuesSameTurnThroughShellAndAwait pins the core loop
// behaviour at the checkpoint: a Send now accepted inside a tool batch is
// delivered INSIDE the same turn — same goal, same turn index — with the
// batch's complete tool results ahead of it on the wire, and a command the
// model issues afterwards runs in the very same turn, carrying both the
// steering and its result onward. Exactly one persisted copy of the user's
// text backs the wire, and its receipt is delivered.
func TestSendNowContinuesSameTurnThroughShellAndAwait(t *testing.T) {
	state := newTestState(t)
	state.SetOperationInterrupt(nilSteeringHandler)

	var submitErr error
	started := make(chan session.OperationRef, 1)
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "shell.cmd",
		Description: "runs a shell command",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			ref, ok := session.OperationFromContext(ctx)
			if !ok {
				submitErr = errors.New("shell batch ran outside a steering operation")
				return registry.ToolResult{Content: "shell: make test passed", Summary: "shell"}, nil
			}
			started <- ref
			_, submitErr = state.SubmitSendNow(session.SendNowRequest{
				TurnID:    ref.TurnID,
				RequestID: "req-shell",
				Text:      "also tail the logs",
			})
			return registry.ToolResult{Content: "shell: make test passed", Summary: "shell"}, nil
		},
	}); err != nil {
		t.Fatalf("Register shell.cmd: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name:        "await.status",
		Description: "waits for a background job",
		Risk:        registry.RiskReadOnly,
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Content: "await: healthy", Summary: "await"}, nil
		},
	}); err != nil {
		t.Fatalf("Register await.status: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"work","action":{"type":"tool_call","tool":"shell.cmd","args":{"command":"make test"}}}`,
			`{"rationale":"await","action":{"type":"tool_call","tool":"await.status","args":{}}}`,
			`{"rationale":"done","action":{"type":"final","content":"All done."}}`,
		},
	}
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.EnableSteering = true
	runner.MaxToolIterations = 5

	done := make(chan error, 1)
	go func() {
		_, err := runner.RunTask(context.Background(), "do the thing")
		done <- err
	}()
	if err := awaitRunTask(t, done); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if submitErr != nil {
		t.Fatalf("SubmitSendNow from the shell batch: %v", submitErr)
	}
	<-started // the reference was captured; nothing else synchronises on it

	// Same turn: one goal, one turn index — steering never opened a replacement.
	if got := state.TurnIndex(); got != 1 {
		t.Fatalf("turn index = %d, want 1", got)
	}
	if n := countPersistedUserMessages(state, "do the thing"); n != 1 {
		t.Fatalf("goal persisted %d times, want exactly 1", n)
	}
	// The next same-turn command ran: the provider saw the shell call, then the
	// post-steering await call, then the final.
	if len(p.Requests) != 3 {
		t.Fatalf("provider saw %d requests, want 3", len(p.Requests))
	}
	// Second request: the shell batch's real result, then the steering.
	toolIdx, steerIdxs := -1, steeringEnvelopeIndexes(p.Requests[1].Messages, "also tail the logs")
	for i, m := range p.Requests[1].Messages {
		if strings.Contains(m.Content, "shell: make test passed") {
			toolIdx = i
		}
	}
	if toolIdx < 0 || len(steerIdxs) != 1 {
		t.Fatalf("second request lost the tool result or steering: %v", p.Requests[1].Messages)
	}
	if steerIdxs[0] < toolIdx {
		t.Fatalf("steering (index %d) preceded the tool result (index %d)", steerIdxs[0], toolIdx)
	}
	// Third request: the earlier prefix is preserved, the command issued after
	// steering ran, and its result lands after the steering.
	afterIdxs := -1
	for i, m := range p.Requests[2].Messages {
		if strings.Contains(m.Content, "await: healthy") {
			afterIdxs = i
		}
	}
	if afterIdxs < 0 {
		t.Fatalf("the same-turn command issued after steering never ran: %v", p.Requests[2].Messages)
	}
	if got := steeringEnvelopeIndexes(p.Requests[2].Messages, "also tail the logs"); len(got) != 1 || got[0] > afterIdxs {
		t.Fatalf("steering not carried through the follow-up request: %v", p.Requests[2].Messages)
	}
	assertPrefixStable(t, p.Requests)

	// The transcript backs the wire with exactly one persisted copy, in the
	// enveloped wire form so restart replay matches byte-for-byte.
	if n := countSteeringMessages(state); n != 1 {
		t.Fatalf("persisted steering messages = %d, want exactly 1", n)
	}
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeSteering &&
			m.Content != "[user steering, mid-turn]: also tail the logs" {
			t.Fatalf("persisted steering content = %q, want the enveloped wire form", m.Content)
		}
	}
}

// TestSendNowStartupAndPlanFirstDoNotDuplicateGoal pins the startup and
// plan-first contracts: a Send now accepted during the plan-first planning
// call interrupts only that call — the title and classification work ran
// exactly once, initialization continued once (one turn index, one goal), the
// planning history is append-only (the retry planning call repeats the first
// attempt byte-for-byte with the steering appended at the end), and the
// appended steering tail reaches the main wire exactly once with exactly one
// persisted copy.
func TestSendNowStartupAndPlanFirstDoNotDuplicateGoal(t *testing.T) {
	state := newTestState(t)
	state.SetOperationInterrupt(nilSteeringHandler)

	title := &countingTitleManager{}
	classifierCalls := 0
	classifier := func(context.Context, string) (TaskClass, error) {
		classifierCalls++
		return ClassEdit, nil
	}

	planSteer := make(chan struct{})
	p := &sendNowGateProvider{
		responses: []string{
			// Call 0 is held open until its operation is interrupted; the retry
			// gets the plan text, then the main wire acts and concludes.
			"1. write a failing test\n2. make it pass",
			"1. write a failing test\n2. make it pass",
			`{"rationale":"act","action":{"type":"tool_call","tool":"plan.probe","args":{}}}`,
			`{"rationale":"done","action":{"type":"final","content":"Answered."}}`,
		},
		holdCancel: 0,
		entered:    map[int]chan struct{}{0: planSteer},
	}
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "plan.probe", Description: "probe", Risk: registry.RiskReadOnly,
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Content: "probe ok", Summary: "probe"}, nil
		},
	}); err != nil {
		t.Fatalf("Register plan.probe: %v", err)
	}

	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.EnableSteering = true
	runner.PlanFirst = true
	runner.TitleManager = title
	runner.Classifier = classifier
	runner.MaxToolIterations = 5
	goal := "why does the retry ladder exist?"

	done := make(chan error, 1)
	go func() {
		_, err := runner.RunTask(context.Background(), goal)
		done <- err
	}()

	// The planning call is in flight under its operation; accept the Send now
	// against it. The stream ends on the operation context, so the planning
	// interruption is deterministic.
	select {
	case <-planSteer:
	case <-time.After(5 * time.Second):
		t.Fatal("the planning call never reached the provider")
	}
	turnID := state.SteeringSnapshot().TurnID
	if turnID == "" {
		t.Fatal("no eligible steering turn was open for the planning call")
	}
	submitNow(t, state, turnID, "req-plan-steer", "also cover the timeout path")
	if err := awaitRunTask(t, done); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	if len(p.requests) != 4 {
		t.Fatalf("provider saw %d requests, want 4 (planning held, planning retry, tool step, final)", len(p.requests))
	}
	first, retry, main := p.requests[0].Messages, p.requests[1].Messages, p.requests[2].Messages

	// Planning history is append-only: the retry repeats the first attempt and
	// adds exactly the steering envelope at the end.
	if len(retry) != len(first)+1 {
		t.Fatalf("planning retry has %d messages, want %d + 1 appended steering", len(retry), len(first))
	}
	for i := range first {
		if !sameMessage(first[i], retry[i]) {
			t.Fatalf("planning retry rewrote message %d:\nwas: %.300q\nnow: %.300q", i, first[i].Content, retry[i].Content)
		}
	}
	if retry[len(retry)-1].Content != steeringEnvelope("also cover the timeout path") {
		t.Fatalf("appended planning tail = %.300q, want the steering envelope", retry[len(retry)-1].Content)
	}
	// The main wire repeats the shared prefix up to the goal, carries the
	// steering tail exactly once, and never re-appends the planning prompt.
	if len(main) < len(first) {
		t.Fatalf("main wire has %d messages, fewer than the planning prefix (%d)", len(main), len(first))
	}
	for i := 0; i < len(first)-1; i++ {
		if !sameMessage(first[i], main[i]) {
			t.Fatalf("main wire rewrote planning message %d:\nwas: %.300q\nnow: %.300q", i, first[i].Content, main[i].Content)
		}
	}
	if got := steeringEnvelopeIndexes(main, "also cover the timeout path"); len(got) != 1 {
		t.Fatalf("main wire carries the steering tail %d times, want exactly 1: %v", len(got), main)
	}
	for _, req := range p.requests {
		for _, m := range req.Messages {
			if m.Role == schema.RoleUser && m.Content == goal && countGoalCopies(req.Messages, goal) != 1 {
				t.Fatalf("a request republished the goal: %v", req.Messages)
			}
		}
	}

	// One goal, one turn index, one persisted copy per item.
	if got := state.TurnIndex(); got != 1 {
		t.Fatalf("turn index = %d, want 1", got)
	}
	if n := countPersistedUserMessages(state, goal); n != 1 {
		t.Fatalf("goal persisted %d times, want exactly 1", n)
	}
	if n := countSteeringMessages(state); n != 1 {
		t.Fatalf("persisted steering messages = %d, want exactly 1", n)
	}
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeSteering &&
			m.Content != "[user steering, mid-turn]: also cover the timeout path" {
			t.Fatalf("persisted steering content = %q, want the enveloped wire form", m.Content)
		}
	}
	plans := 0
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypePlan {
			plans++
		}
	}
	if plans != 1 {
		t.Fatalf("persisted plans = %d, want exactly 1", plans)
	}
	// Initialization continued exactly once through the interrupted planning
	// call: the title ran once and the classifier was asked once.
	if title.calls != 1 {
		t.Fatalf("title manager ran %d times, want exactly 1", title.calls)
	}
	if classifierCalls != 1 {
		t.Fatalf("classifier ran %d times, want exactly 1", classifierCalls)
	}
}

// countGoalCopies counts exact goal copies on one wire.
func countGoalCopies(msgs []schema.ChatMessage, goal string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == schema.RoleUser && m.Content == goal {
			n++
		}
	}
	return n
}

// TestSendNowFinalAnswerRaceRespectsAcceptedMessage pins the final-admission
// gate under a genuine race. The model's draft answer is in flight when two
// successive Send now submissions land; the gate barriers must lead to the
// observable outcome the contract promises: the stale draft is never
// published, the steering is delivered to the model BEFORE the answer the user
// has already moved past is published, every accepted submission is persisted
// exactly once with a delivered receipt, and — when the turn has closed — a
// late submission is rejected rather than silently dropped.
func TestSendNowFinalAnswerRaceRespectsAcceptedMessage(t *testing.T) {
	state := newTestState(t)
	state.SetOperationInterrupt(nilSteeringHandler)

	started := make(chan session.OperationRef, 1)
	release := make(chan struct{})
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "gate.tool", Description: "gate", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			if ref, ok := session.OperationFromContext(ctx); ok {
				started <- ref
			}
			<-release
			return registry.ToolResult{Content: "gate result", Summary: "gate"}, nil
		},
	}); err != nil {
		t.Fatalf("Register gate.tool: %v", err)
	}

	draftEntered := make(chan struct{})
	draftRelease := make(chan struct{})
	type receiptProbe struct {
		status session.SteeringStatus
		err    error
	}
	probeA := make(chan receiptProbe, 1)
	probeB := make(chan receiptProbe, 1)
	p := &sendNowGateProvider{
		holdCancel: -1,
		responses: []string{
			`{"rationale":"act","action":{"type":"tool_call","tool":"gate.tool","args":{}}}`,
			`{"rationale":"draft","action":{"type":"final","content":"Final draft v1"}}`,
			`{"rationale":"done","action":{"type":"final","content":"Final v2 after steering"}}`,
		},
		entered: map[int]chan struct{}{1: draftEntered},
		gated:   map[int]chan struct{}{1: draftRelease},
		onChat: func(idx int, req schema.ChatRequest) {
			if idx != 2 {
				return
			}
			// Both racing submissions were carried by real requests by now;
			// their receipts must read delivered.
			rec, err := state.SubmitSendNow(session.SendNowRequest{
				TurnID: state.SteeringSnapshot().TurnID, RequestID: "req-draft-steer",
				Text: "steer before the draft",
			})
			probeA <- receiptProbe{status: rec.Status, err: err}
			rec, err = state.SubmitSendNow(session.SendNowRequest{
				TurnID: state.SteeringSnapshot().TurnID, RequestID: "req-final-steer",
				Text: "racing steering",
			})
			probeB <- receiptProbe{status: rec.Status, err: err}
		},
	}
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.EnableSteering = true
	runner.MaxToolIterations = 5

	type runResult struct {
		task *Task
		err  error
	}
	done := make(chan runResult, 1)
	go func() {
		task, err := runner.RunTask(context.Background(), "do the thing")
		done <- runResult{task, err}
	}()

	// The tool batch is under its operation: the first submission is accepted
	// while the turn is still working, so it must be delivered before any final.
	var turnID string
	select {
	case ref := <-started:
		turnID = ref.TurnID
	case <-time.After(5 * time.Second):
		t.Fatal("the tool batch never started")
	}
	submitNow(t, state, turnID, "req-draft-steer", "steer before the draft")
	close(release)

	// The draft answer is now being produced behind the gate barrier. Submit
	// the second message INSIDE that window — it is accepted, so the draft's
	// publication must be refused and the text delivered first.
	select {
	case <-draftEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the draft answer never reached the provider")
	}
	submitNow(t, state, turnID, "req-final-steer", "racing steering")
	close(draftRelease)

	var got runResult
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunTask never returned")
	}
	if got.err != nil {
		t.Fatalf("RunTask: %v", got.err)
	}
	// The stale draft lost the race and was never published; the published
	// answer is the one the turn produced after the steering was delivered.
	if got.task.Status != TaskStatusCompleted || got.task.Summary != "Final v2 after steering" {
		t.Fatalf("task status = %v summary = %q, want the post-steering answer published", got.task.Status, got.task.Summary)
	}
	if n := countMessagesContaining(state, "Final draft v1"); n != 0 {
		t.Fatalf("the refused draft leaked into the transcript %d times", n)
	}

	// Both racing submissions were delivered inside the same turn: one
	// persisted copy each, on the wire before the published final.
	for _, req := range []struct{ id, text string }{
		{"req-draft-steer", "steer before the draft"},
		{"req-final-steer", "racing steering"},
	} {
		if got := steeringEnvelopeIndexes(p.requests[2].Messages, req.text); len(got) != 1 {
			t.Fatalf("wire does not carry %q exactly once: %v", req.text, p.requests[2].Messages)
		}
	}
	if n := countSteeringMessages(state); n != 2 {
		t.Fatalf("persisted steering messages = %d, want 2 (one per accepted submission)", n)
	}
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeSteering {
			if m.Content != "[user steering, mid-turn]: steer before the draft" &&
				m.Content != "[user steering, mid-turn]: racing steering" {
				t.Fatalf("persisted steering content = %q", m.Content)
			}
		}
	}
	if n := countPersistedUserMessages(state, "do the thing"); n != 1 {
		t.Fatalf("goal persisted %d times, want exactly 1", n)
	}
	if got := state.TurnIndex(); got != 1 {
		t.Fatalf("turn index = %d, want 1", got)
	}
	assertPrefixStable(t, p.requests)

	for _, probe := range []struct {
		name string
		ch   chan receiptProbe
	}{
		{"req-draft-steer", probeA},
		{"req-final-steer", probeB},
	} {
		select {
		case r := <-probe.ch:
			if r.err != nil {
				t.Fatalf("%s replay: %v", probe.name, r.err)
			}
			if r.status != session.SteeringDelivered {
				t.Fatalf("%s receipt status = %q, want delivered", probe.name, r.status)
			}
		case <-time.After(time.Second):
			t.Fatalf("receipt probe for %s never ran", probe.name)
		}
	}

	// The other legal race outcome: a submission that arrives after the gate
	// closed the turn must be REJECTED, with its text retained by the caller —
	// never silently dropped into a closed turn.
	if _, err := state.SubmitSendNow(session.SendNowRequest{
		TurnID: turnID, RequestID: "req-late", Text: "late draft",
	}); err == nil {
		t.Fatal("a Send now into a closed turn was accepted")
	}
	if n := countMessagesContaining(state, "late draft"); n != 0 {
		t.Fatalf("a rejected submission left %d transcript entries", n)
	}

	// Gate barrier, unit level: TryFinishSteeringTurn refuses a final while
	// accepted steering is outstanding and admits it once it is submitted.
	gateState := newTestState(t)
	ctx, ref := startSteeringTestTurn(t, gateState)
	_ = ctx
	gateState.FinishSteeringOperation(ref, nil)
	submitNow(t, gateState, ref.TurnID, "req-gate", "gate draft")
	if gateState.TryFinishSteeringTurn(ref.TurnID) {
		t.Fatal("final admission succeeded while accepted steering was outstanding")
	}
	batch, err := gateState.StageSteering(ref.TurnID)
	if err != nil {
		t.Fatalf("StageSteering: %v", err)
	}
	if len(batch.Messages) != 1 || batch.Messages[0].Text != "gate draft" {
		t.Fatalf("the refused publication did not retain the draft: %+v", batch)
	}
	ids := make([]string, 0, len(batch.Messages))
	for _, m := range batch.Messages {
		ids = append(ids, m.ID)
	}
	gateState.MarkSteeringSubmitted(ref.TurnID, ids)
	if !gateState.TryFinishSteeringTurn(ref.TurnID) {
		t.Fatal("final admission still refused after the steering was submitted")
	}
	gateState.EndSteeringTurn(ref.TurnID, "test cleanup")
}

// TestSendNowFullStopAndHardBudgetRetainUndelivered pins the two ending paths
// that must not produce a second delivery or a replacement turn. A full stop
// (Esc → cancelled turn context) surfaces exactly once in the interrupt
// marker, the queued text the turn already reached the submission point with is
// persisted exactly once and is not duplicated between the wire submission and
// the marker, and the follow-up queue stays empty. A genuine hard budget that
// prevents another request refuses the stale finalize instead of publishing an
// answer the user moved past, marks the task executing, leaves no failure and
// no marker, and retains the text exactly once in the follow-up queue.
func TestSendNowFullStopAndHardBudgetRetainUndelivered(t *testing.T) {
	const steerText = "hold this for later"
	const goal = "do the thing"

	t.Run("full stop", func(t *testing.T) {
		state := newTestState(t)
		state.SetOperationInterrupt(nilSteeringHandler)

		// The stop lands WHILE the batch is running, so the turn context is
		// already cancelled by the time the checkpoint stages the queued text.
		// The delivery request then carries it (the submission point persists
		// it once) and fails like a real cancelled request body would, so the
		// marker is written on top of a turn holding exactly one truthful copy.
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		reg := registry.New()
		if err := reg.Register(registry.Tool{
			Name: "stop.tool", Description: "stop", Risk: registry.RiskReadOnly,
			Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
				if ref, ok := session.OperationFromContext(ctx); ok {
					submitNow(t, state, ref.TurnID, "req-stop", steerText)
				}
				started <- struct{}{}
				<-release
				return registry.ToolResult{Content: "stopped work", Summary: "stopped"}, nil
			},
		}); err != nil {
			t.Fatalf("Register stop.tool: %v", err)
		}
		p := &agenttest.ScriptedProvider{
			Responses: []string{
				`{"rationale":"act","action":{"type":"tool_call","tool":"stop.tool","args":{}}}`,
			},
			Errs: []error{nil, context.Canceled},
		}
		runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
		runner.EnableSteering = true
		runner.MaxToolIterations = 5

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := runner.RunTask(ctx, goal)
			done <- err
		}()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("the batch never started")
		}
		cancel()
		close(release)

		if err := awaitRunTask(t, done); !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTask err = %v, want context.Canceled", err)
		}

		var markers []string
		for _, m := range state.Messages() {
			if strings.Contains(m.Content, "[turn interrupted]") {
				markers = append(markers, m.Content)
			}
		}
		if len(markers) != 1 {
			t.Fatalf("persisted interrupt markers = %d, want exactly 1", len(markers))
		}
		if !strings.Contains(markers[0], "Goal: "+goal) {
			t.Fatalf("marker does not name the goal: %q", markers[0])
		}
		// The queued text reached the submission point of the delivery request,
		// so it is persisted exactly once and the marker must not repeat it.
		if got := strings.Count(markers[0], "Undelivered user feedback: "+steerText); got != 0 {
			t.Fatalf("the marker duplicated the submitted copy %d times", got)
		}
		if n := countMessagesContaining(state, steerText); n != 1 {
			t.Fatalf("the steering text appears in %d messages, want exactly 1", n)
		}
		var steeringCopies int
		for _, m := range state.Messages() {
			if m.ContentType == session.ContentTypeSteering {
				steeringCopies++
				if m.Content != steeringEnvelope(steerText) {
					t.Fatalf("persisted steering content = %q, want the enveloped wire form", m.Content)
				}
			}
		}
		if steeringCopies != 1 {
			t.Fatalf("persisted steering copies = %d, want exactly 1", steeringCopies)
		}
		if q := state.SteeringQueue(); len(q) != 0 {
			t.Fatalf("follow-up queue duplicated the text after the marker: %v", q)
		}
		if got := state.TurnIndex(); got != 1 {
			t.Fatalf("turn index = %d, want 1 — the stop must not open a replacement turn", got)
		}
		if n := countPersistedUserMessages(state, goal); n != 1 {
			t.Fatalf("goal persisted %d times, want exactly 1", n)
		}
		if len(p.Requests) != 2 {
			t.Fatalf("provider saw %d requests, want 2 — no replacement turn ran", len(p.Requests))
		}
		finals := 0
		for _, m := range state.Messages() {
			if m.Role == session.RoleAssistant && m.ContentType == session.ContentTypeMarkdown {
				finals++
			}
		}
		if finals != 0 {
			t.Fatalf("a stopped turn published %d assistant answers", finals)
		}
	})

	t.Run("hard budget", func(t *testing.T) {
		state := newTestState(t)
		state.SetOperationInterrupt(nilSteeringHandler)

		reg := registry.New()
		if err := reg.Register(registry.Tool{
			Name: "budget.tool", Description: "budget", Risk: registry.RiskReadOnly,
			Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
				if ref, ok := session.OperationFromContext(ctx); ok {
					submitNow(t, state, ref.TurnID, "req-budget", steerText)
				}
				return registry.ToolResult{Content: "budget work", Summary: "budget"}, nil
			},
		}); err != nil {
			t.Fatalf("Register budget.tool: %v", err)
		}
		p := &agenttest.ScriptedProvider{
			Responses: []string{
				`{"rationale":"act","action":{"type":"tool_call","tool":"budget.tool","args":{}}}`,
				`{"rationale":"wrap","action":{"type":"final","content":"Whatever salvage said."}}`,
			},
		}
		runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
		runner.EnableSteering = true
		// The genuine hard limit: one tool batch exhausts the work budget, so
		// the loop top refuses another request BEFORE its checkpoint can
		// deliver the queued text.
		runner.MaxToolIterations = 1
		goal := "budget limit probe"

		task, err := runner.RunTask(context.Background(), goal)
		if !errors.Is(err, session.ErrSendNow) {
			t.Fatalf("RunTask err = %v, want ErrSendNow", err)
		}
		// The stale finalize answer was refused: the turn did not complete and
		// carries no summary or failure — it ends exactly as an interrupted
		// turn that owes the next one the user's text.
		if task.Status != TaskStatusExecuting {
			t.Fatalf("task status = %v, want executing", task.Status)
		}
		if task.Summary != "" {
			t.Fatalf("task summary = %q, want empty", task.Summary)
		}
		// No marker: the hard budget is not a stop.
		if n := countMessagesContaining(state, "[turn interrupted]"); n != 0 {
			t.Fatalf("a hard budget recorded %d interrupt markers", n)
		}
		// The text was never delivered and never duplicated: it lives exactly
		// once in the follow-up queue for the next turn.
		if n := countSteeringMessages(state); n != 0 {
			t.Fatalf("an undelivered entry was persisted as steering %d times", n)
		}
		if n := countMessagesContaining(state, steerText); n != 0 {
			t.Fatalf("undelivered text appears in %d transcript messages", n)
		}
		q := state.SteeringQueue()
		if len(q) != 1 || q[0] != steerText {
			t.Fatalf("follow-up queue = %v, want exactly [%s]", q, steerText)
		}
		// No replacement turn: same goal, same turn index, no extra request.
		if got := state.TurnIndex(); got != 1 {
			t.Fatalf("turn index = %d, want 1", got)
		}
		if n := countPersistedUserMessages(state, goal); n != 1 {
			t.Fatalf("goal persisted %d times, want exactly 1", n)
		}
		if len(p.Requests) != 2 {
			t.Fatalf("provider saw %d requests, want 2 (tool step + refused finalize)", len(p.Requests))
		}
		finals := 0
		for _, m := range state.Messages() {
			if m.Role == session.RoleAssistant && m.ContentType == session.ContentTypeMarkdown {
				finals++
			}
		}
		if finals != 0 {
			t.Fatalf("a refused finalize published %d assistant messages", finals)
		}
	})
}

// TestSendNowHeldTurnWaitsForExplicitResume pins the hold contract: a Send now
// accepted on a held turn interrupts the current operation but delivers
// NOTHING while the hold lasts — the receipt reads waiting_for_resume and the
// transcript stays clean — and the text reaches the model exactly once, in a
// real request, once the user explicitly resumes.
func TestSendNowHeldTurnWaitsForExplicitResume(t *testing.T) {
	state := newTestState(t)
	state.SetOperationInterrupt(nilSteeringHandler)

	const steerText = "deliver me on resume"
	var submitErr error
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "hold.tool", Description: "hold", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			ref, ok := session.OperationFromContext(ctx)
			if !ok {
				return registry.ToolResult{Content: "held result", Summary: "held"}, nil
			}
			// Hold the session first, then interrupt the operation: the receipt
			// must land on waiting_for_resume, not a delivered or safe-boundary
			// state, because the checkpoint will wait for the explicit resume.
			state.SetHold(true)
			_, submitErr = state.SubmitSendNow(session.SendNowRequest{
				TurnID:    ref.TurnID,
				RequestID: "req-hold",
				Text:      steerText,
			})
			return registry.ToolResult{Content: "held result", Summary: "held"}, nil
		},
	}); err != nil {
		t.Fatalf("Register hold.tool: %v", err)
	}

	requestsSeen := atomic.Int32{}
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"act","action":{"type":"tool_call","tool":"hold.tool","args":{}}}`,
			`{"rationale":"done","action":{"type":"final","content":"Resumed and done."}}`,
		},
		OnChat: func(int, schema.ChatRequest) { requestsSeen.Add(1) },
	}
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.EnableSteering = true
	runner.MaxToolIterations = 5

	done := make(chan error, 1)
	go func() {
		_, err := runner.RunTask(context.Background(), "do the thing")
		done <- err
	}()

	// Wait until the turn is parked on the hold: the batch has been
	// interrupted and settled, and the checkpoint is waiting in WaitUnheld.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if state.Held() && requestsSeen.Load() == 1 && len(state.SteeringQueue()) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the turn never parked on the hold: held=%v requests=%d queue=%v",
				state.Held(), requestsSeen.Load(), state.SteeringQueue())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// It must STAY parked: the hold is not released, nothing is delivered and
	// the queued text is not duplicated.
	time.Sleep(150 * time.Millisecond)
	if !state.Held() {
		t.Fatal("the hold flag was cleared without an explicit resume")
	}
	if requestsSeen.Load() != 1 {
		t.Fatalf("requests while parked = %d, want 1 — the parked turn may not call the provider", requestsSeen.Load())
	}
	if n := countSteeringMessages(state); n != 0 {
		t.Fatalf("steering delivered while held: %d persisted copies", n)
	}
	if q := state.SteeringQueue(); len(q) != 1 || q[0] != steerText {
		t.Fatalf("held queue = %v, want exactly [%s]", q, steerText)
	}
	snap := state.SteeringSnapshot()
	if !snap.Held || snap.PendingCount != 1 {
		t.Fatalf("snapshot held=%v pending=%d, want held=true pending=1", snap.Held, snap.PendingCount)

	}
	// The receipt reads waiting_for_resume while the hold lasts.
	waitingSeen := false
	for start := time.Now(); time.Since(start) < 2*time.Second; {
		rec, err := state.SubmitSendNow(session.SendNowRequest{
			TurnID: snap.TurnID, RequestID: "req-hold", Text: steerText,
		})
		if err != nil {
			t.Fatalf("receipt replay: %v", err)
		}
		if rec.Status == session.SteeringWaitingResume {
			waitingSeen = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waitingSeen {
		t.Fatal("the held receipt never read waiting_for_resume")
	}

	// The explicit resume is what releases the checkpoint.
	state.SetHold(false)
	if err := awaitRunTask(t, done); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if submitErr != nil {
		t.Fatalf("SubmitSendNow from the held batch: %v", submitErr)
	}
	// On resume the text was submitted exactly once, in a real request, after
	// the interrupted batch's result.
	if len(p.Requests) != 2 {
		t.Fatalf("provider saw %d requests after resume, want 2", len(p.Requests))
	}
	resultIdx, steerIdxs := -1, steeringEnvelopeIndexes(p.Requests[1].Messages, steerText)
	for i, m := range p.Requests[1].Messages {
		if strings.Contains(m.Content, "held result") {
			resultIdx = i
		}
	}
	if resultIdx < 0 || len(steerIdxs) != 1 || steerIdxs[0] < resultIdx {
		t.Fatalf("resumed request lost the result/steering order: %v", p.Requests[1].Messages)
	}
	if n := countSteeringMessages(state); n != 1 {
		t.Fatalf("persisted steering messages = %d, want exactly 1", n)
	}
	if q := state.SteeringQueue(); len(q) != 0 {
		t.Fatalf("queue not empty after resume: %v", q)
	}
	if got := state.TurnIndex(); got != 1 {
		t.Fatalf("turn index = %d, want 1 — the resume must not open a replacement turn", got)
	}
}

// TestSendNowPromptPrefixAndToolPairing pins the wire contracts around the
// steering checkpoint, in both transports: every request preserves the stable
// prefix the previous request already sent (assertPrefixStable), every tool
// call declared on the wire has exactly one paired result, and — at the request
// where steering is appended — all batched call IDs are paired and their
// results complete BEFORE the user's steering arrives.
func TestSendNowPromptPrefixAndToolPairing(t *testing.T) {
	const steerText = "pair the calls before steering me"

	t.Run("native", func(t *testing.T) {
		state := newTestState(t)
		state.SetOperationInterrupt(nilSteeringHandler)

		reg := registry.New()
		for _, name := range []string{"pair.one", "pair.two", "pair.three", "pair.four"} {
			name := name
			if err := reg.Register(registry.Tool{
				Name: name, Description: name, Risk: registry.RiskReadOnly,
				Schema: json.RawMessage(`{"type":"object"}`),
				Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
					// The single fourth batch is where the Send now is issued,
					// so the multi-call pairing batch before it runs untouched.
					if name == "pair.four" {
						if ref, ok := session.OperationFromContext(ctx); ok {
							submitNow(t, state, ref.TurnID, "req-pair-steer", steerText)
						}
					}
					return registry.ToolResult{Content: "ran " + name, Summary: name}, nil
				},
			}); err != nil {
				t.Fatalf("Register %s: %v", name, err)
			}
		}
		p := &agenttest.ScriptedProvider{
			ProviderCaps: schema.ProviderCapabilities{ToolCalling: true},
			Responses:    []string{"", "", "", "All paired."},
			ToolCalls: [][]schema.ToolCall{
				{{ID: "c1", Name: "pair.one", Args: json.RawMessage(`{}`)}},
				{
					{ID: "c2", Name: "pair.two", Args: json.RawMessage(`{}`)},
					{ID: "c3", Name: "pair.three", Args: json.RawMessage(`{}`)},
				},
				{{ID: "c4", Name: "pair.four", Args: json.RawMessage(`{}`)}},
			},
			FinishReasons: []string{"tool_calls", "tool_calls", "tool_calls", "stop"},
		}
		runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
		runner.EnableSteering = true
		runner.NativeTools = true
		runner.MaxToolIterations = 6

		if err := runner.Run(context.Background(), "check the pair tools"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(p.Requests) != 4 {
			t.Fatalf("provider saw %d requests, want 4", len(p.Requests))
		}
		assertPrefixStable(t, p.Requests)
		assertWirePairingBeforeSteering(t, p.Requests[3].Messages, []string{"c1", "c2", "c3", "c4"}, steerText)
		if n := countSteeringMessages(state); n != 1 {
			t.Fatalf("persisted steering messages = %d, want exactly 1", n)
		}
	})

	t.Run("envelope", func(t *testing.T) {
		state := newTestState(t)
		state.SetOperationInterrupt(nilSteeringHandler)

		reg := registry.New()
		for _, name := range []string{"pair.one", "pair.two", "pair.three", "pair.four"} {
			name := name
			if err := reg.Register(registry.Tool{
				Name: name, Description: name, Risk: registry.RiskReadOnly,
				Schema: json.RawMessage(`{"type":"object"}`),
				Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
					if name == "pair.four" {
						if ref, ok := session.OperationFromContext(ctx); ok {
							submitNow(t, state, ref.TurnID, "req-pair-steer", steerText)
						}
					}
					return registry.ToolResult{Content: "ran " + name, Summary: name}, nil
				},
			}); err != nil {
				t.Fatalf("Register %s: %v", name, err)
			}
		}
		p := &agenttest.ScriptedProvider{Responses: []string{
			`{"rationale":"one","action":{"type":"tool_call","tool":"pair.one","args":{}}}`,
			`{"rationale":"both","actions":[{"type":"tool_call","tool":"pair.two","args":{}},{"type":"tool_call","tool":"pair.three","args":{}}]}`,
			`{"rationale":"last","action":{"type":"tool_call","tool":"pair.four","args":{}}}`,
			`{"rationale":"done","action":{"type":"final","content":"Enveloped."}}`,
		}}
		runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
		runner.EnableSteering = true
		runner.MaxToolIterations = 6

		if err := runner.Run(context.Background(), "check the pair tools"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(p.Requests) != 4 {
			t.Fatalf("provider saw %d requests, want 4", len(p.Requests))
		}
		assertPrefixStable(t, p.Requests)

		// Envelope mode derives its call IDs and strips them from the wire, so
		// the pairing contract here is positional: every declared batch action
		// has exactly one settled result in the transcript's wire, and every
		// one of them precedes the steering envelope.
		wire := p.Requests[3].Messages
		steerIdxs := steeringEnvelopeIndexes(wire, steerText)
		if len(steerIdxs) != 1 {
			t.Fatalf("wire carries the steering %d times, want exactly 1", len(steerIdxs))
		}
		steerIdx := steerIdxs[0]
		for _, name := range []string{"pair.one", "pair.two", "pair.three", "pair.four"} {
			count := 0
			for i, m := range wire {
				if i >= steerIdx {
					break
				}
				if strings.Contains(m.Content, "ran "+name) {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("envelope wire carries %d results for %s before the steering, want exactly 1", count, name)
			}
		}
		if n := countSteeringMessages(state); n != 1 {
			t.Fatalf("persisted steering messages = %d, want exactly 1", n)
		}
	})
}

// assertWirePairingBeforeSteering checks, on one native wire, that every
// declared call ID has exactly one assistant declaration and one tool result,
// and that each result precedes the steering envelope.
func assertWirePairingBeforeSteering(t *testing.T, msgs []schema.ChatMessage, wantIDs []string, steerText string) {
	t.Helper()
	steerIdxs := steeringEnvelopeIndexes(msgs, steerText)
	if len(steerIdxs) != 1 {
		t.Fatalf("wire carries the steering %d times, want exactly 1", len(steerIdxs))
	}
	steerIdx := steerIdxs[0]
	declared := map[string]int{}
	results := map[string]int{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			declared[c.ID]++
		}
		if m.ToolCallID != "" {
			results[m.ToolCallID]++
		}
	}
	for _, id := range wantIDs {
		if declared[id] != 1 {
			t.Fatalf("call %s is declared %d times on the wire, want exactly 1", id, declared[id])
		}
		if results[id] != 1 {
			t.Fatalf("call %s has %d results on the wire, want exactly 1 (pairing broken)", id, results[id])
		}
	}
	for i, m := range msgs {
		if m.ToolCallID != "" && i >= steerIdx {
			t.Fatalf("result for %s lands after the steering envelope (index %d)", m.ToolCallID, steerIdx)
		}
	}
}

// --- envelope actions[] batch operation scope -------------------------------

// TestSendNowEnvelopeBatchCapturesOperation pins the tool operation scope of
// the envelope actions[] BATCH dispatch: a multi-action batch runs its tool
// calls under a live steering operation, and a Send now submitted DURING the
// batch captures that operation, so the batch is interruptible and the text is
// delivered at the next checkpoint inside the same turn.
//
// A batch dispatched on the bare turn context leaves its handlers without an
// operation ref — the submission misses the running work entirely — and that
// is the regression this test exists to catch: every other submission lands
// during a single envelope tool call or a native batch, so those suites stay
// green through the revert.
func TestSendNowEnvelopeBatchCapturesOperation(t *testing.T) {
	const steerText = "steer me mid-batch"
	state := newTestState(t)
	state.SetOperationInterrupt(nilSteeringHandler)

	var (
		mu             sync.Mutex
		ran            = map[string]bool{}
		withoutOp      []string
		batchSubmitErr error
	)
	reg := registry.New()
	for _, name := range []string{"pair.one", "pair.two", "pair.three", "pair.four"} {
		name := name
		if err := reg.Register(registry.Tool{
			Name: name, Description: name, Risk: registry.RiskReadOnly,
			Schema: json.RawMessage(`{"type":"object"}`),
			Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
				ref, hasOp := session.OperationFromContext(ctx)
				mu.Lock()
				ran[name] = true
				if !hasOp {
					withoutOp = append(withoutOp, name)
				}
				mu.Unlock()
				// The Send now is submitted from INSIDE the multi-action batch
				// — the window no other submission in the suite covers.
				if name == "pair.two" && hasOp {
					_, batchSubmitErr = state.SubmitSendNow(session.SendNowRequest{
						TurnID:    ref.TurnID,
						RequestID: "req-batch-steer",
						Text:      steerText,
					})
				}
				return registry.ToolResult{Content: "ran " + name, Summary: name}, nil
			},
		}); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	// The middle step is the only multi-action batch: pair.one and pair.four
	// are single envelope tool calls, whose own dispatch scope is covered
	// elsewhere.
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"first","action":{"type":"tool_call","tool":"pair.one","args":{}}}`,
		`{"rationale":"batch","actions":[{"type":"tool_call","tool":"pair.two","args":{}},{"type":"tool_call","tool":"pair.three","args":{}}]}`,
		`{"rationale":"follow","action":{"type":"tool_call","tool":"pair.four","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"After the batch steering."}}`,
	}}
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.EnableSteering = true
	runner.MaxToolIterations = 6

	done := make(chan error, 1)
	go func() {
		_, err := runner.RunTask(context.Background(), "run a batch, then steer it")
		done <- err
	}()
	if err := awaitRunTask(t, done); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	if batchSubmitErr != nil {
		t.Fatalf("SubmitSendNow from inside the batch: %v", batchSubmitErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if !ran["pair.two"] {
		t.Fatal("the envelope batch never ran pair.two")
	}
	if len(withoutOp) != 0 {
		t.Fatalf("envelope batch tools ran without a steering operation: %v — the actions[] batch must dispatch under a tool operation scope", withoutOp)
	}

	// Same turn: the steering was captured during the batch and delivered at
	// the checkpoint that follows it, before the same-turn follow-up action.
	if len(p.Requests) != 4 {
		t.Fatalf("provider saw %d requests, want 4 (tool step, batch step, post-steering step, final)", len(p.Requests))
	}
	wire := p.Requests[2].Messages
	twoIdx := -1
	for i, m := range wire {
		if strings.Contains(m.Content, "ran pair.two") {
			twoIdx = i
		}
	}
	if twoIdx < 0 {
		t.Fatalf("the batch step's result is missing from the post-steering request: %v", wire)
	}
	steerIdxs := steeringEnvelopeIndexes(wire, steerText)
	if len(steerIdxs) != 1 {
		t.Fatalf("wire carries the steering %d times, want exactly 1: %v", len(steerIdxs), wire)
	}
	if steerIdxs[0] < twoIdx {
		t.Fatalf("steering (index %d) preceded the batch's result (index %d)", steerIdxs[0], twoIdx)
	}
	assertPrefixStable(t, p.Requests)
	if got := state.TurnIndex(); got != 1 {
		t.Fatalf("turn index = %d, want 1 — the batch steering must not open a replacement turn", got)
	}
	if n := countPersistedUserMessages(state, "run a batch, then steer it"); n != 1 {
		t.Fatalf("goal persisted %d times, want exactly 1", n)
	}
	if n := countSteeringMessages(state); n != 1 {
		t.Fatalf("persisted steering messages = %d, want exactly 1", n)
	}
}

// --- runner-level settle-then-continue for envelope questions ---------------

// TestSendNowCancelledQuestionSettlesAndTurnContinues pins the settle-then-
// continue contract at the runner level: when an accepted Send now cancels an
// envelope question.ask wait, the cancel surfaces as the distinct steering
// cause (not a turn failure), the canceled prompt is settled rather than left
// hanging, and the loop continues to the checkpoint, which delivers the
// user's text in the SAME turn. A regression that fails the turn on the
// canceled wait would surface here as a RunTask error and no delivered
// steering.
func TestSendNowCancelledQuestionSettlesAndTurnContinues(t *testing.T) {
	state := newTestState(t)
	state.SetOperationInterrupt(nilSteeringHandler)

	const steerText = "never mind, just ship"
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"confirm","action":{"type":"question.ask","questions":[{"question":"Ship it?"}]}}`,
		`{"rationale":"done","action":{"type":"final","content":"Continued after the canceled question."}}`,
	}}
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.EnableSteering = true
	runner.MaxToolIterations = 5

	type runResult struct {
		task *Task
		err  error
	}
	done := make(chan runResult, 1)
	go func() {
		task, err := runner.RunTask(context.Background(), "ask, then steer")
		done <- runResult{task, err}
	}()

	// The question round-trip is running under its tool operation; accept the
	// Send now against it. The interruption worker settles the prompt
	// canonically and cancels the wait's context with the steering cause.
	deadline := time.Now().Add(5 * time.Second)
	for state.PendingQuestion() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the envelope question.ask never registered its prompt")
		}
		time.Sleep(2 * time.Millisecond)
	}
	turnID := state.SteeringSnapshot().TurnID
	if turnID == "" {
		t.Fatal("no eligible steering turn was open for the question wait")
	}
	submitNow(t, state, turnID, "req-question-steer", steerText)

	got := <-done
	if got.err != nil {
		t.Fatalf("RunTask err = %v, want the turn to continue past the canceled question", got.err)
	}

	// The turn was NOT failed, and the steering reached the model in the same
	// turn: the post-cancel request carries the canceled-question note
	// followed by exactly one steering envelope.
	if len(p.Requests) != 2 {
		t.Fatalf("provider saw %d requests, want 2 (question step, post-steering step)", len(p.Requests))
	}
	wire := p.Requests[1].Messages
	noteIdx := -1
	for i, m := range wire {
		if strings.Contains(m.Content, "The questions were canceled") {
			noteIdx = i
		}
	}
	if noteIdx < 0 {
		t.Fatalf("the wire never recorded that the question was canceled: %v", wire)
	}
	steerIdxs := steeringEnvelopeIndexes(wire, steerText)
	if len(steerIdxs) != 1 {
		t.Fatalf("wire carries the steering %d times, want exactly 1: %v", len(steerIdxs), wire)
	}
	if steerIdxs[0] < noteIdx {
		t.Fatalf("steering (index %d) preceded the canceled-question note (index %d)", steerIdxs[0], noteIdx)
	}
	assertPrefixStable(t, p.Requests)

	// The canceled prompt is settled: nothing left hanging for a late answer.
	if state.PendingQuestion() != nil {
		t.Fatal("the canceled question was left pending")
	}
	// Same turn, exactly once: one goal, one steering copy, one answer.
	if got := state.TurnIndex(); got != 1 {
		t.Fatalf("turn index = %d, want 1", got)
	}
	if n := countPersistedUserMessages(state, "ask, then steer"); n != 1 {
		t.Fatalf("goal persisted %d times, want exactly 1", n)
	}
	if n := countSteeringMessages(state); n != 1 {
		t.Fatalf("persisted steering messages = %d, want exactly 1", n)
	}
	if q := state.SteeringQueue(); len(q) != 0 {
		t.Fatalf("queue not empty after delivery: %v", q)
	}
	// The loop-top finalize ran: the answer produced after the steering was
	// delivered is what the turn published.
	if got.task == nil || got.task.Status != TaskStatusCompleted ||
		!strings.Contains(got.task.Summary, "Continued after the canceled question.") {
		t.Fatalf("task status = %v summary = %q, want the post-steering answer published", got.task.Status, got.task.Summary)
	}
}
