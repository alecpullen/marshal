package tui

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// settleCalls serves m's call queue until it is idle, running each command
// and feeding each message back through Update, and returns the model. It
// fails t if the queue is not idle within five seconds.
func settleCalls(t *testing.T, m Model) Model {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !m.callsIdle() {
		if !time.Now().Before(deadline) {
			t.Fatal("the call queue did not settle within five seconds")
		}
		for _, msg := range runCallCmds(m.serveCalls()) {
			m = feedCallMsg(t, m, msg)
		}
	}
	return m
}

// feedCallMsg handles one message through Update and follows every command
// the update returns.
func feedCallMsg(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, next := m.Update(msg)
	m = mustModel(t, updated)
	for _, follow := range runCallCmds(next) {
		m = feedCallMsg(t, m, follow)
	}
	return m
}

// runCallCmds runs a command tree and returns every message its leaves
// produce. Unlike runCmdChain it has no timeout: a converted call under test
// blocks until the test releases it.
func runCallCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, sub := range batch {
			out = append(out, runCallCmds(sub)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// modelOf is the model Update returned, whichever form it took.
func mustModel(t *testing.T, updated tea.Model) Model {
	t.Helper()
	switch next := updated.(type) {
	case Model:
		return next
	case *Model:
		return *next
	}
	t.Fatalf("Update returned %T, not a Model", updated)
	return Model{}
}

// callRecorder records call order. Runs happen off the Update goroutine, so
// every access takes the lock (the tests run under -race).
type callRecorder struct {
	mu      sync.Mutex
	started []string
	handled []string
}

func (r *callRecorder) start(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, name)
}

func (r *callRecorder) handle(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handled = append(r.handled, name)
}

func (r *callRecorder) startedList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.started...)
}

func (r *callRecorder) handledList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.handled...)
}

// TestQueuedCallsRunInOrder proves the queue runs one call at a time, in the
// order the calls were made, and handles the results in that order too.
func TestQueuedCallsRunInOrder(t *testing.T) {
	m := newTestModel(t)
	rec := &callRecorder{}
	release := make(chan struct{})
	started := make(chan string, 2)

	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			rec.start("first")
			started <- "first"
			<-release
			return func(*Model) tea.Cmd {
				rec.handle("first")
				return nil
			}
		}
	})
	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			rec.start("second")
			started <- "second"
			return func(*Model) tea.Cmd {
				rec.handle("second")
				return nil
			}
		}
	})

	cmd := m.serveCalls()
	if cmd == nil {
		t.Fatal("serving the queue started no command")
	}
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(cmd) }()

	if got := <-started; got != "first" {
		t.Fatalf("the first call to start was %q, want first", got)
	}
	// The second call must not start while the first is in flight, however
	// long the first takes.
	time.Sleep(20 * time.Millisecond)
	select {
	case got := <-started:
		t.Fatalf("%q started while the first call was in flight", got)
	default:
	}
	if extra := m.serveCalls(); extra != nil {
		t.Fatal("serving the queue while a call was in flight started another")
	}

	close(release)
	msgs := <-done
	if len(msgs) != 1 {
		t.Fatalf("the first call produced %d messages, want 1", len(msgs))
	}
	// Handling the first result starts the second call from Update's
	// epilogue.
	m = feedCallMsg(t, m, msgs[0])
	if got := <-started; got != "second" {
		t.Fatalf("the second call to start was %q, want second", got)
	}
	m = settleCalls(t, m)

	if got, want := rec.startedList(), []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls started in order %v, want %v", got, want)
	}
	if got, want := rec.handledList(), []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("results handled in order %v, want %v", got, want)
	}
	if !m.callsIdle() {
		t.Fatal("the queue was not idle after both results were handled")
	}
}

// TestAQueuedCallReadsTheModelWhenItStarts proves a call reads the model when
// it reaches the head of the queue, not when it was made.
func TestAQueuedCallReadsTheModelWhenItStarts(t *testing.T) {
	m := newTestModel(t)
	m.rawWidth = 100

	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			return func(mm *Model) tea.Cmd {
				mm.rawWidth = 4242
				return nil
			}
		}
	})
	var saw int
	m.enqueueCall(func(mm *Model) callRun {
		// The start runs on the Update goroutine when the call reaches the
		// head, so it sees what the first result left behind.
		saw = mm.rawWidth
		return func(context.Context) callOutcome { return nil }
	})

	m = settleCalls(t, m)

	if saw != 4242 {
		t.Fatalf("the second call read rawWidth %d when it started, want 4242", saw)
	}
}

// TestAStepRunsAtTheHeadAndTheQueueMovesOn proves a step runs on the Update
// goroutine after the call ahead of it is handled, and that the call behind
// it starts in the same serveCalls.
func TestAStepRunsAtTheHeadAndTheQueueMovesOn(t *testing.T) {
	m := newTestModel(t)
	var order []string

	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			order = append(order, "first started")
			return func(*Model) tea.Cmd {
				order = append(order, "first handled")
				return nil
			}
		}
	})
	m.enqueueStep(func(*Model) tea.Cmd {
		order = append(order, "step")
		return nil
	})
	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			order = append(order, "second started")
			return func(*Model) tea.Cmd {
				order = append(order, "second handled")
				return nil
			}
		}
	})

	// One serveCalls starts the first call and stops at it: nothing behind it
	// runs yet, and a call's own run waits for the command.
	cmd := m.serveCalls()
	if len(order) != 0 {
		t.Fatalf("serving the queue ran %v before any call ran", order)
	}

	// Handling the first result runs the step and starts the second call, in
	// that order, in one epilogue.
	msgs := runCallCmds(cmd)
	if len(msgs) != 1 {
		t.Fatalf("the first call produced %d messages, want 1", len(msgs))
	}
	updated, next := m.Update(msgs[0])
	m = mustModel(t, updated)
	want := []string{"first started", "first handled", "step"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("after the first result the queue ran %v, want %v", order, want)
	}

	// The second call is in flight, its command in next: running it and
	// handling its result drains the queue.
	for _, msg := range runCallCmds(next) {
		updated, _ := m.Update(msg)
		m = mustModel(t, updated)
	}

	want = append(want, "second started", "second handled")
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("the queue ran %v, want %v", order, want)
	}
	if !m.callsIdle() {
		t.Fatal("the queue did not drain")
	}
}

// closeRecorder records that the session was closed on the way out.
type closeRecorder struct {
	acpclient.Session
	closed chan struct{}
	once   sync.Once
}

func (c *closeRecorder) Close(ctx context.Context) error {
	c.once.Do(func() { close(c.closed) })
	return c.Session.Close(ctx)
}

// TestAQuitWaitsForTheCallInFlight proves a quit cancels the call in flight,
// drops everything queued behind it, and finishes when its result arrives.
func TestAQuitWaitsForTheCallInFlight(t *testing.T) {
	host, sess := inproc.New(inproc.Deps{
		State: session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{}),
	})
	closed := make(chan struct{})
	m := New(host, &closeRecorder{Session: sess, closed: closed}, WithHomeDir(t.TempDir()))

	cancelled := make(chan struct{})
	m.enqueueCall(func(*Model) callRun {
		return func(ctx context.Context) callOutcome {
			<-ctx.Done()
			close(cancelled)
			return func(*Model) tea.Cmd { return nil }
		}
	})
	queuedStarted := false
	m.enqueueCall(func(*Model) callRun {
		queuedStarted = true
		return nil
	})

	cmd := m.serveCalls()
	if cmd == nil {
		t.Fatal("serving the queue started no command")
	}
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(cmd) }()

	// A quit does not finish while a call is in flight; it cancels the call
	// and waits for its result.
	if quit := m.finishQuit(); quit != nil {
		t.Fatal("finishQuit returned a command while a call was in flight")
	}
	if !m.callQuitWaiting {
		t.Fatal("finishQuit did not record that a quit is waiting")
	}
	<-cancelled

	msgs := <-done
	if len(msgs) != 1 {
		t.Fatalf("the in-flight call produced %d messages, want 1", len(msgs))
	}
	updated, quitCmd := m.Update(msgs[0])
	m = mustModel(t, updated)

	var sawQuit bool
	for _, msg := range runCallCmds(quitCmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			sawQuit = true
		}
	}
	if !sawQuit {
		t.Fatal("the quit did not finish when the in-flight result arrived")
	}
	if queuedStarted {
		t.Fatal("a call queued behind an in-flight call started after the quit")
	}
	if m.callQuitWaiting || m.callInFlight {
		t.Fatal("the quit left the queue or the in-flight mark set")
	}
	select {
	case <-closed:
	default:
		t.Fatal("the quit did not close the session")
	}
}
