package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/agents"
)

// turnRecorder is a session whose run calls are recorded before the wrapped
// session makes them. Every other method is the wrapped session's.
type turnRecorder struct {
	acpclient.Session
	prompts   []acpwire.PromptParams
	stops     []string
	reviews   []acpwire.ReviewParams
	agentRuns []acpwire.AgentRunParams
	suggests  []string
	proposals int

	swarms     []acpwire.SwarmParams         // Task 3
	preflights []acpwire.PlanPreflightParams // Task 3
	sdds       []acpwire.SDDParams           // Task 3
	answers    []string                      // Task 3
	sddStops   int                           // Task 3
	authors    []acpwire.AuthorPlanParams    // plan authoring through the session
}

func (r *turnRecorder) Prompt(ctx context.Context, params acpwire.PromptParams) (acpwire.PromptResult, error) {
	r.prompts = append(r.prompts, params)
	return r.Session.Prompt(ctx, params)
}

func (r *turnRecorder) Stop(ctx context.Context, cause string) error {
	r.stops = append(r.stops, cause)
	return r.Session.Stop(ctx, cause)
}

func (r *turnRecorder) Review(ctx context.Context, params acpwire.ReviewParams) error {
	r.reviews = append(r.reviews, params)
	return r.Session.Review(ctx, params)
}

func (r *turnRecorder) RunAgent(ctx context.Context, params acpwire.AgentRunParams) error {
	r.agentRuns = append(r.agentRuns, params)
	return r.Session.RunAgent(ctx, params)
}

func (r *turnRecorder) Suggest(ctx context.Context, lastMessage string) (string, error) {
	r.suggests = append(r.suggests, lastMessage)
	return r.Session.Suggest(ctx, lastMessage)
}

func (r *turnRecorder) VerifyProposal(ctx context.Context) (acpwire.VerifyCommands, error) {
	r.proposals++
	return r.Session.VerifyProposal(ctx)
}

func (r *turnRecorder) StartSwarm(ctx context.Context, params acpwire.SwarmParams) error {
	r.swarms = append(r.swarms, params)
	return r.Session.StartSwarm(ctx, params)
}

func (r *turnRecorder) PlanPreflight(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
	r.preflights = append(r.preflights, params)
	return r.Session.PlanPreflight(ctx, params)
}

func (r *turnRecorder) StartSDD(ctx context.Context, params acpwire.SDDParams) error {
	r.sdds = append(r.sdds, params)
	return r.Session.StartSDD(ctx, params)
}

func (r *turnRecorder) AnswerSDDGate(ctx context.Context, answer string) error {
	r.answers = append(r.answers, answer)
	return r.Session.AnswerSDDGate(ctx, answer)
}

func (r *turnRecorder) StopSDD(ctx context.Context) error {
	r.sddStops++
	return r.Session.StopSDD(ctx)
}

func (r *turnRecorder) AuthorPlan(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
	r.authors = append(r.authors, params)
	return r.Session.AuthorPlan(ctx, params)
}

// testModelTurns builds a sized model over an adapter made from d whose
// session is rec, wrapped around the adapter's session.
func testModelTurns(t *testing.T, d inproc.Deps, rec *turnRecorder, opts ...Option) Model {
	t.Helper()
	host, sess := inproc.New(d)
	rec.Session = sess
	all := append([]Option{WithLegacyState(d.State)}, opts...)
	m := New(host, rec, all...)
	m.resize(80, 24)
	m.refreshViewport()
	return m
}

// TestASubmitPromptsThroughTheSession pins that a submitted prompt reaches the
// session, which owns the run.
func TestASubmitPromptsThroughTheSession(t *testing.T) {
	rec := &turnRecorder{}
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := testModelTurns(t, inproc.Deps{State: controlsState(t), Runner: runnerOf(runner)}, rec)

	m.input.SetValue("hello")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = asModel(t, updated)
	if !m.busy {
		t.Fatal("busy = false, want the submission to start a turn")
	}
	m = drainCmds(m, cmd)

	if len(rec.prompts) != 1 || rec.prompts[0] != (acpwire.PromptParams{Text: "hello"}) {
		t.Fatalf("prompts = %+v, want one {Text: hello}", rec.prompts)
	}
	select {
	case goal := <-runner.called:
		if goal != "hello" {
			t.Fatalf("runner goal = %q, want hello", goal)
		}
	case <-time.After(time.Second):
		t.Fatal("the runner was never invoked")
	}
	if m.busy {
		t.Fatal("busy = true, want the finished turn to leave the model idle")
	}
}

// TestAFollowUpIsAQueuedPrompt pins that the entry a finished turn delivers is
// sent as a queued prompt, so the session can put it back if it refuses.
func TestAFollowUpIsAQueuedPrompt(t *testing.T) {
	rec := &turnRecorder{}
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := testModelTurns(t, inproc.Deps{State: controlsState(t), Runner: runnerOf(runner)}, rec)
	m.state.PushSteering("later")

	cmd := m.flushQueuedSteering()
	if cmd == nil {
		t.Fatal("flushQueuedSteering returned nil with an entry queued")
	}
	m = drainCmds(m, cmd)

	want := acpwire.PromptParams{Text: "later", Queued: true}
	if len(rec.prompts) != 1 || rec.prompts[0] != want {
		t.Fatalf("prompts = %+v, want one %+v", rec.prompts, want)
	}
}

// TestARefusedFollowUpGoesBackOnTheQueue pins that a refused start loses
// nothing: the session puts the queued text back and the TUI writes the note.
func TestARefusedFollowUpGoesBackOnTheQueue(t *testing.T) {
	rec := &turnRecorder{}
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := testModelTurns(t, inproc.Deps{State: controlsState(t), Runner: runnerOf(runner)}, rec)
	m.state.PushSteering("later")
	m.state.BeginQuiesce()

	cmd := m.flushQueuedSteering()
	if cmd == nil {
		t.Fatal("flushQueuedSteering returned nil with an entry queued")
	}
	m = drainCmds(m, cmd)

	if q := m.state.SteeringQueue(); len(q) != 1 || q[0] != "later" {
		t.Fatalf("queue = %v, want the refused text back on it", q)
	}
	if m.busy {
		t.Fatal("busy = true, want the refused start to leave the model idle")
	}
	msgs := m.state.Messages()
	last := ""
	if len(msgs) > 0 {
		last = msgs[len(msgs)-1].Content
	}
	if last != "Cannot start work: session is quiescing" {
		t.Fatalf("last message = %q, want the refused-start note", last)
	}
}

// contextRunner blocks until its context is done, so a test can prove that a
// stop reached the run.
type contextRunner struct {
	started chan struct{}
	done    chan struct{}
}

func (r *contextRunner) Run(ctx context.Context, goal string) error {
	close(r.started)
	<-ctx.Done()
	close(r.done)
	return ctx.Err()
}

func (r *contextRunner) SetForceClass(string) {}
func (r *contextRunner) AnswerGate(string)    {}

// TestCancelTurnStopsThroughTheSession pins that a cancel stops the session's
// run, and the run's context ends.
func TestCancelTurnStopsThroughTheSession(t *testing.T) {
	rec := &turnRecorder{}
	runner := &contextRunner{started: make(chan struct{}), done: make(chan struct{})}
	m := testModelTurns(t, inproc.Deps{State: controlsState(t), Runner: runnerOf(runner)}, rec)

	updated, cmd := m.startPrompt("hello", false)
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("startPrompt returned no command")
	}
	// The batch leads with the run; execute it the way the runtime would.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("startPrompt cmd = %T, want a batch led by the run", cmd())
	}
	go func() { _ = batch[0]() }()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("the runner never started")
	}

	m.cancelTurn()

	if len(rec.stops) != 1 || rec.stops[0] != "turn stopped" {
		t.Fatalf("stops = %v, want one \"turn stopped\"", rec.stops)
	}
	select {
	case <-runner.done:
	case <-time.After(time.Second):
		t.Fatal("the run's context did not end after the stop")
	}
}

// TestTheTurnEndLeavesTheActivityToTheSession pins that the TUI no longer
// resets the activity itself: the session's run path does.
func TestTheTurnEndLeavesTheActivityToTheSession(t *testing.T) {
	rec := &turnRecorder{}
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := testModelTurns(t, inproc.Deps{State: controlsState(t), Runner: runnerOf(runner)}, rec)
	m.state.SetActivity(session.Activity{Kind: session.ActivityThinking})

	m, _ = m.handleAgentFinished(agentFinishedMsg{})

	if got := m.state.Activity().Kind; got != session.ActivityThinking {
		t.Fatalf("activity = %q, want the session's own value left alone", got)
	}
}

// TestReviewGoesThroughTheSession pins that /review starts on the session's run
// path, with the base and the range kept apart.
func TestReviewGoesThroughTheSession(t *testing.T) {
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{
		State:    controlsState(t),
		Runner:   runnerOf(&testAgentRunner{}),
		Review:   func(context.Context, acpwire.ReviewParams) error { return nil },
		Commands: setupCmdReg(t),
	}, rec)

	updated, cmd := m.dispatchCommand("/review --base main src")
	m = asModel(t, updated)
	if !m.busy {
		t.Fatal("busy = false, want /review to start a run")
	}
	m = drainCmds(m, cmd)

	want := acpwire.ReviewParams{Focus: "src", Base: "main"}
	if len(rec.reviews) != 1 || rec.reviews[0] != want {
		t.Fatalf("reviews = %+v, want one %+v", rec.reviews, want)
	}
	if m.busy {
		t.Fatal("busy = true, want the finished review to leave the model idle")
	}
}

// TestReviewRefusesABaseAndARangeBeforeStarting pins the refusal's side: it
// happens before anything starts, so no work and no stop group are taken.
func TestReviewRefusesABaseAndARangeBeforeStarting(t *testing.T) {
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{
		State:    controlsState(t),
		Runner:   runnerOf(&testAgentRunner{}),
		Review:   func(context.Context, acpwire.ReviewParams) error { return nil },
		Commands: setupCmdReg(t),
	}, rec)

	updated, cmd := m.dispatchCommand("/review --base=main main...HEAD focus")
	m = asModel(t, updated)

	if cmd != nil {
		t.Fatal("a refused review returned a command")
	}
	if m.busy {
		t.Fatal("busy = true, want the refused review to leave the model idle")
	}
	if len(rec.reviews) != 0 {
		t.Fatalf("reviews = %+v, want none", rec.reviews)
	}
	msgs := m.state.Messages()
	last := ""
	if len(msgs) > 0 {
		last = msgs[len(msgs)-1].Content
	}
	if last != "review: --base and an explicit range are mutually exclusive" {
		t.Fatalf("last message = %q, want the refusal note", last)
	}
}

// TestRunNowStartsOnTheLiveModel pins decision 10: the roster's Run-now asks
// the live model to start the run, so the turn's busy flag, its cancellation
// and its transcript all land where the user sees them.
func TestRunNowStartsOnTheLiveModel(t *testing.T) {
	rec := &turnRecorder{}
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := testModelTurns(t, inproc.Deps{
		State:    controlsState(t),
		Runner:   runnerOf(runner),
		RunAgent: func(context.Context, acpwire.AgentRunParams) error { return nil },
		Commands: setupCmdReg(t),
	}, rec)

	// Open the roster the way Update does, and keep the model it returns.
	opened, _ := tuiCommandEffects["agents"](&m, []string{})
	m = asModel(t, opened)

	panel, ok := m.dock.Panel().(*agents.Panel)
	if !ok {
		t.Fatalf("dock panel = %T, want the agents roster", m.dock.Panel())
	}
	cmd := panel.RunNow("linter")
	if cmd == nil {
		t.Fatal("RunNow returned no command")
	}

	updated, runCmd := m.Update(cmd())
	m = asModel(t, updated)
	if !m.busy {
		t.Fatal("busy = false, want the live model to start the run")
	}
	if m.agentCancel == nil {
		t.Fatal("agentCancel = nil, want the run's cancel on the live model")
	}
	m = drainCmds(m, runCmd)

	want := acpwire.AgentRunParams{Agent: "linter"}
	if len(rec.agentRuns) != 1 || rec.agentRuns[0] != want {
		t.Fatalf("agent runs = %+v, want one %+v", rec.agentRuns, want)
	}
}

// TestRunNowWhileBusyStartsNothing pins that a Run-now request landing during a
// turn is dropped rather than queued behind it.
func TestRunNowWhileBusyStartsNothing(t *testing.T) {
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{
		State:    controlsState(t),
		Runner:   runnerOf(&testAgentRunner{}),
		RunAgent: func(context.Context, acpwire.AgentRunParams) error { return nil },
	}, rec)
	m.busy = true

	updated, cmd := m.Update(runAgentMsg{agent: "linter"})
	m = asModel(t, updated)

	if cmd != nil {
		t.Fatal("a Run-now during a turn returned a command")
	}
	if len(rec.agentRuns) != 0 {
		t.Fatalf("agent runs = %+v, want none", rec.agentRuns)
	}
}

// TestTheSuggestionGoesThroughTheSession pins that the fallback asks the
// session's model, with the last assistant message.
func TestTheSuggestionGoesThroughTheSession(t *testing.T) {
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{
		State:  controlsState(t),
		Runner: runnerOf(&testAgentRunner{}),
		Suggest: func(context.Context, string) (string, error) {
			return "yes, go ahead", nil
		},
	}, rec)
	m.state.Config.TUI.Suggestions = "llm"
	m.state.AddMessageFinalWithUsage(session.RoleAssistant,
		"I've finished the refactor and all tests pass.", session.ContentTypeMarkdown, 0, "")
	m.busy = true

	m, cmd := m.handleAgentFinished(agentFinishedMsg{})
	m = drainCmds(m, cmd)

	if len(rec.suggests) != 1 || rec.suggests[0] != "I've finished the refactor and all tests pass." {
		t.Fatalf("suggests = %+v, want the last assistant message", rec.suggests)
	}
	if m.suggestion != "yes, go ahead" {
		t.Fatalf("suggestion = %q, want the session's answer", m.suggestion)
	}
}

// TestTheProposalGoesThroughTheSession pins that the verify proposal is a
// session call, driven exactly as /sdd's preflight drives it.
func TestTheProposalGoesThroughTheSession(t *testing.T) {
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{
		State:  controlsState(t),
		Runner: runnerOf(&testAgentRunner{}),
		VerifyProposal: func(context.Context) (acpwire.VerifyCommands, error) {
			return acpwire.VerifyCommands{Build: "make build", Test: "make test"}, nil
		},
		Commands: setupCmdReg(t),
	}, rec)

	// Open the /sdd preflight on a manifest-less repo so the gate is unknown.
	m.openRunPreflight("sdd", "plan.md", &acpwire.PlanPreflight{})

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'f'})
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("expected a proposal command from pressing f")
	}
	updated, _ = m.Update(cmd())
	m = asModel(t, updated)

	if rec.proposals != 1 {
		t.Fatalf("proposals = %d, want 1", rec.proposals)
	}
	if m.pendingRun == nil || m.pendingRun.verifyBuild != "make build" || m.pendingRun.verifyTest != "make test" {
		t.Fatalf("proposal not stashed: %+v", m.pendingRun)
	}
}
