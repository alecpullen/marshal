package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/acptest"
	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// followingModel builds a model over a state whose runner is a blocking
// runner, so a run started through the session stays in the status until the
// test releases it.
func followingModel(t *testing.T, runner *blockingAgentRunner) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(1000, 0), session.Persistence{SessionID: unnamedSessionID})
	return testModelWith(t, service.Deps{State: state, Runner: runnerOf(runner)})
}

// deliverChange delivers a change the way the pump does: the model reads the
// session's status and acts on it. Its own command is returned unrun.
func deliverChange(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(changedMsg{ch: m.sess.Changed()})
	return asModel(t, updated), cmd
}

// startOtherRun starts a run through the session from a goroutine of the test
// — never through the model — so the model holds no run call of its own. It
// returns the run's ID once the status shows it.
func startOtherRun(t *testing.T, m Model, runner *blockingAgentRunner) string {
	t.Helper()
	go func() {
		_, _ = m.sess.Prompt(context.Background(), acpwire.PromptParams{Text: "other"})
	}()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the other run never reached the runner")
	}
	waitRunStart(t, m)
	return m.sess.Status().Run.ID
}

// waitRows polls until the transcript holds want rows containing text, so a
// test can read what the agent side wrote when a run ended.
func waitRows(t *testing.T, m Model, text string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for countContaining(stateOf(m).Messages(), text) != want {
		if time.Now().After(deadline) {
			t.Fatalf("transcript rows containing %q = %d, want %d",
				text, countContaining(stateOf(m).Messages(), text), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestARunTheTUIDidNotStartIsFollowed: a run in the status that this TUI never
// started is mirrored — the change makes the model busy and following.
func TestARunTheTUIDidNotStartIsFollowed(t *testing.T) {
	runner := &blockingAgentRunner{block: make(chan struct{}), started: make(chan struct{})}
	m := followingModel(t, runner)
	id := startOtherRun(t, m, runner)
	defer close(runner.block)

	m, cmd := deliverChange(t, m)
	if m.following != id {
		t.Fatalf("following = %q, want the run's ID %q", m.following, id)
	}
	if !m.busy {
		t.Fatal("a followed run must set the model busy")
	}
	if cmd == nil {
		t.Fatal("following a run returned no command")
	}
}

// TestTheEndOfAFollowedRunFinishesItLocally: once the status no longer shows
// the followed run, the model finishes it from the status alone — idle, with
// the success pulse and no note of its own.
func TestTheEndOfAFollowedRunFinishesItLocally(t *testing.T) {
	runner := &blockingAgentRunner{block: make(chan struct{}), started: make(chan struct{})}
	m := followingModel(t, runner)
	startOtherRun(t, m, runner)
	m, _ = deliverChange(t, m)
	if m.following == "" {
		t.Fatal("the change did not follow the run")
	}

	close(runner.block)
	waitRunClear(t, m)
	m, _ = deliverChange(t, m)

	if m.busy {
		t.Fatal("the end of a followed run left the model busy")
	}
	if m.following != "" {
		t.Fatalf("following = %q after the run ended, want none", m.following)
	}
	if !m.successPulse {
		t.Fatal("the end of a followed run did not set the success pulse")
	}
	if n := countContaining(stateOf(m).Messages(), "Turn failed"); n != 0 {
		t.Fatalf("the TUI wrote %d failure rows of its own, want none", n)
	}
	if notice, ok := stateOf(m).Notice(); ok {
		t.Fatalf("the end of a followed run set a notice: %+v", notice)
	}
}

// TestAFollowedFailureSetsTheNotice: a followed run that failed sets the
// provider notice and adds no second failure row — the agent side writes
// "✘ Turn failed: …" exactly once.
func TestAFollowedFailureSetsTheNotice(t *testing.T) {
	runner := &blockingAgentRunner{
		block:   make(chan struct{}),
		started: make(chan struct{}),
		err:     errors.New("the provider said no"),
	}
	m := followingModel(t, runner)
	startOtherRun(t, m, runner)
	m, _ = deliverChange(t, m)
	if m.following == "" {
		t.Fatal("the change did not follow the run")
	}

	close(runner.block)
	waitRunClear(t, m)
	waitRows(t, m, "Turn failed", 1)
	m, _ = deliverChange(t, m)

	if _, ok := stateOf(m).Notice(); !ok {
		t.Fatal("a followed failure did not set the provider notice")
	}
	if n := countContaining(stateOf(m).Messages(), "Turn failed"); n != 1 {
		t.Fatalf("\"Turn failed\" rows = %d, want exactly 1 (the agent's)", n)
	}
	if m.busy || m.following != "" {
		t.Fatalf("a followed failure left busy=%v following=%q", m.busy, m.following)
	}
}

// TestStoppingAFollowedRunIsTheTUIsOwnStop: cancelTurn while following stops
// the run through the session; the end clears cancelling and the steering
// queue and leaves no notice.
func TestStoppingAFollowedRunIsTheTUIsOwnStop(t *testing.T) {
	runner := &blockingAgentRunner{block: make(chan struct{}), started: make(chan struct{})}
	m := followingModel(t, runner)
	startOtherRun(t, m, runner)
	m, _ = deliverChange(t, m)
	if m.following == "" {
		t.Fatal("the change did not follow the run")
	}
	stateOf(m).PushSteering("queued while the run was in flight")

	if !m.cancelTurn() {
		t.Fatal("cancelTurn reported no turn in flight while following")
	}
	close(runner.block)
	waitRunClear(t, m)
	m, _ = deliverChange(t, m)

	if m.cancelling {
		t.Fatal("the end of the stopped followed run left the model cancelling")
	}
	if m.busy || m.following != "" {
		t.Fatalf("the stopped followed run left busy=%v following=%q", m.busy, m.following)
	}
	if queued := stateOf(m).SteeringQueue(); len(queued) != 0 {
		t.Fatalf("the steering queue still holds %q", queued)
	}
	if notice, ok := stateOf(m).Notice(); ok {
		t.Fatalf("stopping a followed run set a notice: %+v", notice)
	}
}

// TestTheTUIsOwnStoppingRunIsNotFollowed: a run the TUI started and stopped is
// its own. The runner ignores its context, so the run stays in the status
// while the finish message is handled; the model goes idle and records the run
// to ignore rather than following it.
func TestTheTUIsOwnStoppingRunIsNotFollowed(t *testing.T) {
	runner := &blockingAgentRunner{block: make(chan struct{}), started: make(chan struct{})}
	m := followingModel(t, runner)

	updated, cmd := m.startSessionRun("Cannot start work", false, func(ctx context.Context) error {
		_, err := m.sess.Prompt(ctx, acpwire.PromptParams{Text: "x"})
		return err
	})
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("startSessionRun returned no command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("startSessionRun cmd = %T, want a batch led by the run", cmd())
	}
	finished := make(chan tea.Msg, 1)
	go func() { finished <- batch[0]() }()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the model's own run never reached the runner")
	}
	waitRunStart(t, m)
	id := m.sess.Status().Run.ID

	if !m.cancelTurn() {
		t.Fatal("cancelTurn reported no in-flight turn")
	}
	// The runner ignores its context, so the stopped run is still in the
	// status; that is exactly the window decision 5 covers.
	if m.sess.Status().Run == nil {
		t.Fatal("the stopped run must still be in the status")
	}
	updated, _ = m.Update(agentFinishedMsg{err: context.Canceled, refusal: "Cannot start work"})
	m = asModel(t, updated)

	if m.ignoreRun != id {
		t.Fatalf("ignoreRun = %q, want the TUI's own run %q", m.ignoreRun, id)
	}
	if m.busy {
		t.Fatal("the TUI's own stopped run left the model busy")
	}
	m, _ = deliverChange(t, m)
	if m.busy {
		t.Fatal("a change followed the TUI's own stopping run")
	}

	close(runner.block)
	waitRunClear(t, m)
	m, _ = deliverChange(t, m)
	if m.ignoreRun != "" {
		t.Fatalf("ignoreRun = %q after the run cleared, want none", m.ignoreRun)
	}
	if m.busy {
		t.Fatal("the model went busy once the ignored run cleared")
	}
}

// TestARefusedStartFollowsTheRunThatHoldsTheSlot: a CodeBusy refusal of the
// model's own prompt is answered by following the run the session admitted —
// without waiting for a change.
func TestARefusedStartFollowsTheRunThatHoldsTheSlot(t *testing.T) {
	runner := &blockingAgentRunner{block: make(chan struct{}), started: make(chan struct{})}
	m := followingModel(t, runner)
	id := startOtherRun(t, m, runner)
	defer close(runner.block)

	updated, cmd := m.startPrompt("mine", false)
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("startPrompt returned no command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("startPrompt cmd = %T, want a batch led by the run", cmd())
	}
	msg := batch[0]()
	finished, ok := msg.(agentFinishedMsg)
	if !ok {
		t.Fatalf("the refused start reported %T, want agentFinishedMsg", msg)
	}
	if finished.err == nil {
		t.Fatal("the model's own prompt was not refused")
	}
	if code, ok := acpclient.CodeOf(finished.err); !ok || code != acpwire.CodeBusy {
		t.Fatalf("the refusal = %v (code %q), want %q", finished.err, code, acpwire.CodeBusy)
	}

	updated, followCmd := m.Update(finished)
	m = asModel(t, updated)
	if m.following != id {
		t.Fatalf("following = %q after the refusal, want the other run %q", m.following, id)
	}
	if !m.busy {
		t.Fatal("the refusal did not leave the model busy following the run")
	}
	if followCmd == nil {
		t.Fatal("following the run the refusal collided with returned no command")
	}
}

// TestAQuietWakeRefusalWritesNoNote: a follow-on start refused with CodeBusy is
// quiet; the same refusal on an ordinary start still names itself.
func TestAQuietWakeRefusalWritesNoNote(t *testing.T) {
	runner := &blockingAgentRunner{block: make(chan struct{}), started: make(chan struct{})}
	defer close(runner.block)
	m := followingModel(t, runner)
	m.busy = true // a run of this TUI's is in flight; nothing to follow here
	before := stateOf(m).Messages()

	busy := &acpwire.Error{Code: acpwire.CodeBusy, Message: "a turn is running"}
	updated, _ := m.Update(agentFinishedMsg{err: busy, refusal: "Cannot start work", quietBusy: true})
	m = asModel(t, updated)

	if m.busy {
		t.Fatal("a quiet refused start left the model busy")
	}
	if n := len(stateOf(m).Messages()); n != len(before) {
		t.Fatalf("a quiet refusal wrote %d notes, want none", n-len(before))
	}

	updated, _ = m.Update(agentFinishedMsg{err: busy, refusal: "Cannot start work"})
	m = asModel(t, updated)
	if n := countContaining(stateOf(m).Messages(), "Cannot start work"); n != 1 {
		t.Fatalf("\"Cannot start work\" rows = %d, want exactly 1", n)
	}
}

// TestANewModelDoesNotReplayAnOldWake: a model built over a session whose
// status already carries a watch resume never wakes for it.
func TestANewModelDoesNotReplayAnOldWake(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(1000, 0), session.Persistence{SessionID: unnamedSessionID})
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	host, sess := acptest.Open(t, service.Deps{State: state, Runner: runnerOf(runner)})
	// The status's resume is the one the service's noteResume published on a
	// watch fire; the stub carries the same value a real one would.
	stub := &stubSession{patch: func(st *acpwire.SessionStatus) {
		st.WatchResume = &acpwire.WatchResume{Name: "build", Repeat: true, Seq: 7}
	}}
	stub.Session = sess
	m := New(host, stub)

	m, _ = deliverChange(t, m)
	if m.busy {
		t.Fatal("a new model replayed a wake that fired before it existed")
	}
	assertNoWake(t, runner)
}
