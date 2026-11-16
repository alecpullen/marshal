package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/watch"
)

// wakeSession is the session every detached-wake test serves.
const wakeSession = "s1"

// wakeRunner is the scripted runner a detached wake starts. It hands back every
// goal it is given, then waits for done, so a test can read the run in the
// session's status while it is still in flight.
type wakeRunner struct {
	goals chan string

	mu      sync.Mutex
	release chan struct{}
	closed  bool
}

func newWakeRunner() *wakeRunner {
	return &wakeRunner{goals: make(chan string, 4), release: make(chan struct{})}
}

// Run records the goal and waits for the test to let it finish.
func (r *wakeRunner) Run(ctx context.Context, goal string) error {
	r.goals <- goal
	r.mu.Lock()
	release := r.release
	r.mu.Unlock()
	select {
	case <-release:
	case <-ctx.Done():
	}
	return nil
}

// done lets every run the wake started finish. It is idempotent.
func (r *wakeRunner) done() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.release)
}

// newWakeRuntime is one session on a viewers host: its runtime, carrying the
// hook the host binds, and its service over the scripted runner. resume is the
// config gate the hook reads.
func newWakeRuntime(t *testing.T, resume bool) (*app.Runtime, *service.Session, *wakeRunner) {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: wakeSession})
	state.Config.Watch.ResumeEnabled = resume
	runner := newWakeRunner()
	rt := &app.Runtime{SessionID: wakeSession, State: state, WatchResume: &app.WatchResumeHook{}}
	_, sess := service.New(service.Deps{
		State:  state,
		Runner: func() service.Runner { return runner },
	})
	t.Cleanup(func() {
		runner.done()
		sess.Release()
	})
	return rt, sess, runner
}

// newWakeHost is an agentHost whose run config asks for (or does not ask for)
// the detached binding, as RunWithViewers does.
func newWakeHost(t *testing.T, resumeDetached bool) *agentHost {
	t.Helper()
	h, err := newAgentHost(runConfig{
		startRuntime: func(context.Context, ...app.Option) (*app.Runtime, error) {
			return nil, errors.New("no runtime in tests")
		},
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		resumeDetached: resumeDetached,
	})
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}
	return h
}

// waitForWakeRun polls until the session reports the run the wake started.
func waitForWakeRun(t *testing.T, sess *service.Session, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess.Status().Run != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the session's status never showed %s", what)
}

// TestAWakeWithNoScreenStartsARun is L7's agent half: a session on a viewers
// host, resume on and no marshal viewer, resumes itself through its own
// service when a watch fires.
func TestAWakeWithNoScreenStartsARun(t *testing.T) {
	h := newWakeHost(t, true)
	rt, sess, runner := newWakeRuntime(t, true)

	if h.manager.detachedResume == nil {
		t.Fatal("a viewers host installed no detached resume")
	}
	// The binder is called where the service builder calls it.
	h.manager.detachedResume(rt, wakeSession, sess)

	rt.WatchResume.Invoke(watch.Report{WatchID: "w1", Name: "ci", Mode: watch.ModeOnce, Resume: true})

	select {
	case goal := <-runner.goals:
		if !strings.Contains(goal, `Watch "ci" fired`) {
			t.Fatalf("the wake's goal = %q, want it to name the watch", goal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a wake with no screen started no run")
	}
	waitForWakeRun(t, sess, "the run the wake started")
}

// TestAWakeWithAScreenIsTheScreens pins the other half of decision 2: a marshal
// viewer is the session's screen, and it resumes the watch itself.
func TestAWakeWithAScreenIsTheScreens(t *testing.T) {
	h := newWakeHost(t, true)
	rt, sess, runner := newWakeRuntime(t, true)
	h.manager.detachedResume(rt, wakeSession, sess)

	// A marshal connection has subscribed to the session's status.
	release := h.viewers.add(wakeSession)
	defer release()

	rt.WatchResume.Invoke(watch.Report{WatchID: "w1", Name: "ci", Mode: watch.ModeOnce, Resume: true})
	select {
	case goal := <-runner.goals:
		t.Fatalf("a wake with a screen on the session started %q, want nothing", goal)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestTheViewerCountFollowsTheConnection pins decision 1: a marshal
// connection's session/status counts one viewer of the session, and the
// connection's cleanup counts none.
func TestTheViewerCountFollowsTheConnection(t *testing.T) {
	_, sess := func() (*service.Host, *service.Session) {
		state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: wakeSession})
		return service.New(service.Deps{State: state})
	}()
	viewers := newMarshalViewers()
	marshal := &atomic.Bool{}
	marshal.Store(true)
	viewersOf := NewStatusManager(StatusManagerConfig{
		Lookup: func(sessionID string) (*service.Session, bool) {
			return sess, sessionID == wakeSession
		},
		Notify:  func(string, any) error { return nil },
		Marshal: marshal.Load,
		Viewers: viewers,
	})

	params, err := json.Marshal(acpwire.StatusParams{SessionID: wakeSession})
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	if _, err := viewersOf.Status(context.Background(), params); err != nil {
		t.Fatalf("session/status: %v", err)
	}
	if !viewers.any(wakeSession) {
		t.Fatal("a marshal connection's session/status counted no viewer")
	}

	// A client that declared no capability never subscribes, so it is no one's
	// screen.
	marshal.Store(false)
	plain := NewStatusManager(StatusManagerConfig{
		Lookup: func(sessionID string) (*service.Session, bool) {
			return sess, sessionID == wakeSession
		},
		Notify:  func(string, any) error { return nil },
		Marshal: marshal.Load,
		Viewers: newMarshalViewers(),
	})
	if _, err := plain.Status(context.Background(), params); err != nil {
		t.Fatalf("session/status without the capability: %v", err)
	}
	if plain.viewers.any(wakeSession) {
		t.Fatal("a client without the capability counted as a viewer")
	}

	// The connection's cleanup takes its addition back.
	viewersOf.Close()
	if viewers.any(wakeSession) {
		t.Fatal("the viewer count survived the connection's cleanup")
	}
}

// TestAWakeDuringARunStartsNothing pins the run check: a run in flight drains
// its own reports, so the wake starts nothing.
func TestAWakeDuringARunStartsNothing(t *testing.T) {
	h := newWakeHost(t, true)
	rt, sess, runner := newWakeRuntime(t, true)
	h.manager.detachedResume(rt, wakeSession, sess)

	rt.WatchResume.Invoke(watch.Report{Name: "ci", Mode: watch.ModeRepeat, Resume: true})
	select {
	case <-runner.goals:
	case <-time.After(2 * time.Second):
		t.Fatal("the first wake started no run")
	}
	waitForWakeRun(t, sess, "the first wake's run")

	// The second fire arrives while the run is still in flight.
	rt.WatchResume.Invoke(watch.Report{Name: "ci", Mode: watch.ModeRepeat, Resume: true})
	select {
	case goal := <-runner.goals:
		t.Fatalf("a wake during a run started %q, want nothing", goal)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestAWakeWithResumeOffStartsNothing pins the config gate.
func TestAWakeWithResumeOffStartsNothing(t *testing.T) {
	h := newWakeHost(t, true)
	rt, sess, runner := newWakeRuntime(t, false)
	h.manager.detachedResume(rt, wakeSession, sess)

	rt.WatchResume.Invoke(watch.Report{Name: "ci", Mode: watch.ModeOnce, Resume: true})
	select {
	case goal := <-runner.goals:
		t.Fatalf("a wake with resume off started %q, want nothing", goal)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestABridgeBindingReplacesTheDetachedOne pins decision 2's replacement: a
// non-marshal connection's turn lookup binds the hook to its own turn manager,
// and the detached resume no longer runs.
func TestABridgeBindingReplacesTheDetachedOne(t *testing.T) {
	h := newWakeHost(t, true)
	rt, sess, runner := newWakeRuntime(t, true)
	h.manager.detachedResume(rt, wakeSession, sess)

	goals := make(chan string, 4)
	tm := newResumeTestManager(t, rt.State, goals, nil)
	alive := &atomic.Bool{}
	alive.Store(true)
	bindWatchResume(rt.WatchResume, tm, wakeSession, alive, func() bool { return false })

	rt.WatchResume.Invoke(watch.Report{Name: "ci", Mode: watch.ModeOnce, Resume: true})
	select {
	case goal := <-goals:
		if !strings.Contains(goal, `Watch "ci" fired`) {
			t.Fatalf("the turn manager's goal = %q, want it to name the watch", goal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the bridge's binding started no turn")
	}
	select {
	case goal := <-runner.goals:
		t.Fatalf("the detached resume still ran after the bridge bound the hook: %q", goal)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestAHostWithoutViewersBindsNothing pins that a stdio host leaves the hook as
// it is today: nothing binds a session's wake when the host serves no viewers.
func TestAHostWithoutViewersBindsNothing(t *testing.T) {
	h := newWakeHost(t, false)
	if h.manager.detachedResume != nil {
		t.Fatal("a host without viewers installed a detached resume")
	}

	rt, _, runner := newWakeRuntime(t, true)
	// No connection bound this hook and the host bound nothing either, so the
	// fire does what it did before: nothing.
	rt.WatchResume.Invoke(watch.Report{Name: "ci", Mode: watch.ModeOnce, Resume: true})
	select {
	case goal := <-runner.goals:
		t.Fatalf("a host without viewers started %q, want nothing", goal)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestTheServiceBuilderMakesTheDetachedBinding pins where decision 2's binding
// is made: the builder that constructs a session's service, once per session,
// and only on a host that serves viewers.
func TestTheServiceBuilderMakesTheDetachedBinding(t *testing.T) {
	// A stdio host binds nothing at that seam.
	if stdio := newWakeHost(t, false); stdio.manager.detachedResume != nil {
		t.Fatal("a stdio host bound a detached resume")
	}

	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: func(context.Context, ...app.Option) (*app.Runtime, error) {
			return nil, errors.New("no runtime in tests")
		},
	})
	m.SetTurnCanceller(noopCancel())
	bound := make(chan string, 2)
	m.detachedResume = func(_ *app.Runtime, sessionID string, _ *service.Session) {
		bound <- sessionID
	}
	installRuntime(m, wakeSession, &app.Runtime{State: serviceState(t, wakeSession)})

	if _, ok := m.Service(wakeSession); !ok {
		t.Fatal("Service reported false for a published runtime")
	}
	select {
	case id := <-bound:
		if id != wakeSession {
			t.Fatalf("the builder bound %q, want %q", id, wakeSession)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the service builder bound no detached resume")
	}

	// Once per session: the pair is built once and reused.
	if _, ok := m.Service(wakeSession); !ok {
		t.Fatal("Service reported false for a published runtime")
	}
	select {
	case id := <-bound:
		t.Fatalf("the service builder bound %q a second time", id)
	case <-time.After(300 * time.Millisecond):
	}
}
