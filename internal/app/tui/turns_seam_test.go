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
