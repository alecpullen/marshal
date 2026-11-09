package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
)

// blocker is a run a test holds open: it signals its start, then waits for its
// release or for its own context, whichever ends it first.
type blocker struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	// ctx is the context the work was given. It is written once before started
	// closes, so a test that waited for the start reads it safely.
	ctx context.Context
}

// newBlocker builds a run nothing has released yet.
func newBlocker() *blocker {
	return &blocker{started: make(chan struct{}), release: make(chan struct{})}
}

// run is the work: it records its context, signals its start, and waits.
func (b *blocker) run(ctx context.Context) error {
	b.ctx = ctx
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitStart blocks until the work has begun, or fails the test.
func (b *blocker) waitStart(t *testing.T) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never started")
	}
}

// workCtx is the context the work was given.
func (b *blocker) workCtx() context.Context { return b.ctx }

// blockingPlanRun is a plan run whose Run is the test's blocking work.
type blockingPlanRun struct{ b *blocker }

func (p blockingPlanRun) Run(ctx context.Context) error { return p.b.run(ctx) }
func (p blockingPlanRun) Answer(string)                 {}

// waitRunGone polls until no run is in flight, and returns the status that
// showed it gone. A run whose ending is still writing is bounded, not waited
// on forever.
func waitRunGone(t *testing.T, sess *Session) acpwire.SessionStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := sess.Status()
		if st.Run == nil {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run never cleared: %+v", st.Run)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitContextDone polls until ctx has ended.
func waitContextDone(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for ctx.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the run's context never ended")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestACallerThatLeavesDoesNotStopTheTurn pins P4 D4: a turn runs on the
// session's context, so a call whose context ends answers at once with its own
// error and leaves the turn going. The status names it meanwhile.
func TestACallerThatLeavesDoesNotStopTheTurn(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	b := newBlocker()
	fake.setBody(b.run)
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
		done <- err
	}()
	b.waitStart(t)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Prompt after its caller left = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not answer once its call's context ended")
	}
	if err := b.workCtx().Err(); err != nil {
		t.Fatalf("the turn ended with its caller's context: %v", err)
	}

	flight := sess.Status().Run
	if flight == nil || flight.Kind != acpwire.RunTurn {
		t.Fatalf("Run = %+v, want a turn in flight", flight)
	}
	if !strings.HasPrefix(flight.ID, "s1") {
		t.Errorf("Run.ID = %q, want it to start with the session's ID", flight.ID)
	}

	close(b.release)
	after := waitRunGone(t, sess)
	if after.LastRun == nil {
		t.Fatal("LastRun is nil after the turn ended")
	}
	if after.LastRun.Outcome != acpwire.RunDone {
		t.Errorf("LastRun.Outcome = %q, want %q", after.LastRun.Outcome, acpwire.RunDone)
	}
	if after.LastRun.ID != flight.ID || after.LastRun.Kind != flight.Kind {
		t.Errorf("LastRun = %+v, want the same run as %+v", after.LastRun, flight)
	}
	if after.LastRun.EndedAt < after.LastRun.StartedAt {
		t.Errorf("LastRun ended (%d) before it started (%d)", after.LastRun.EndedAt, after.LastRun.StartedAt)
	}
}

// TestAStopEndsADetachedRun pins the other half: a stop, not the caller's
// context, is what ends a detached run.
func TestAStopEndsADetachedRun(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	b := newBlocker()
	fake.setBody(b.run)
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = sess.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
	}()
	b.waitStart(t)
	cancel()
	if err := b.workCtx().Err(); err != nil {
		t.Fatalf("the turn ended with its caller's context: %v", err)
	}

	if err := sess.Stop(context.Background(), "the user stopped it"); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	waitContextDone(t, b.workCtx())
	after := waitRunGone(t, sess)
	if after.LastRun == nil || after.LastRun.Outcome != acpwire.RunCancelled {
		t.Fatalf("LastRun = %+v, want a cancelled run", after.LastRun)
	}
}

// TestEachDetachingKindOutlivesItsCaller pins P4 D4's kinds: every detaching
// run but authoring keeps going when its caller's context ends.
func TestEachDetachingKindOutlivesItsCaller(t *testing.T) {
	rows := []struct {
		name string
		deps func(*testing.T, *blocker) Deps
		call func(context.Context, *Session) error
		kind acpwire.RunKind
	}{
		{
			name: "review",
			deps: func(t *testing.T, b *blocker) Deps {
				return Deps{
					State: newState(t, "s1"),
					Review: func(ctx context.Context, _ acpwire.ReviewParams) error {
						return b.run(ctx)
					},
				}
			},
			call: func(ctx context.Context, s *Session) error {
				return s.Review(ctx, acpwire.ReviewParams{})
			},
			kind: acpwire.RunReview,
		},
		{
			name: "agent",
			deps: func(t *testing.T, b *blocker) Deps {
				return Deps{
					State: newState(t, "s1"),
					RunAgent: func(ctx context.Context, _ acpwire.AgentRunParams) error {
						return b.run(ctx)
					},
				}
			},
			call: func(ctx context.Context, s *Session) error {
				return s.RunAgent(ctx, acpwire.AgentRunParams{Agent: "linter"})
			},
			kind: acpwire.RunAgent,
		},
		{
			name: "swarm",
			deps: func(t *testing.T, b *blocker) Deps {
				return Deps{
					State: newState(t, "s1"),
					Swarm: func(ctx context.Context, _ acpwire.SwarmParams) error {
						return b.run(ctx)
					},
				}
			},
			call: func(ctx context.Context, s *Session) error {
				return s.StartSwarm(ctx, acpwire.SwarmParams{Goal: "fix the build"})
			},
			kind: acpwire.RunSwarm,
		},
		{
			name: "sdd",
			deps: func(t *testing.T, b *blocker) Deps {
				return Deps{
					State: newState(t, "s1"),
					NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) {
						return blockingPlanRun{b: b}, nil
					},
				}
			},
			call: func(ctx context.Context, s *Session) error {
				return s.StartSDD(ctx, acpwire.SDDParams{PlanPath: "plan.md"})
			},
			kind: acpwire.RunSDD,
		},
		{
			name: "answer",
			deps: func(t *testing.T, _ *blocker) Deps {
				return Deps{State: newState(t, "s1")}
			},
			call: func(ctx context.Context, s *Session) error {
				return s.AnswerSDDGate(ctx, "yes")
			},
			kind: acpwire.RunSDD,
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			b := newBlocker()
			_, sess := New(row.deps(t, b))
			if row.name == "answer" {
				sess.mu.Lock()
				sess.paused = blockingPlanRun{b: b}
				sess.mu.Unlock()
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- row.call(ctx, sess) }()
			b.waitStart(t)
			cancel()

			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%s after its caller left = %v, want context.Canceled", row.name, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s did not answer once its caller left", row.name)
			}
			if err := b.workCtx().Err(); err != nil {
				t.Fatalf("the %s run ended with its caller's context: %v", row.name, err)
			}
			st := sess.Status()
			if st.Run == nil || st.Run.Kind != row.kind {
				t.Fatalf("Run = %+v, want a %q run in flight", st.Run, row.kind)
			}

			if err := sess.Stop(context.Background(), "settle the test"); err != nil {
				t.Fatalf("Stop = %v", err)
			}
			waitContextDone(t, b.workCtx())
			after := waitRunGone(t, sess)
			if after.LastRun == nil || after.LastRun.Outcome != acpwire.RunCancelled {
				t.Errorf("LastRun = %+v, want a cancelled run", after.LastRun)
			}
		})
	}
}

// TestAnAuthoringRunEndsWithItsCaller pins the exception: an authoring run is
// its caller's, because only that caller reads its result.
func TestAnAuthoringRunEndsWithItsCaller(t *testing.T) {
	state := newState(t, "s1")
	b := newBlocker()
	_, sess := New(Deps{
		State: state,
		AuthorPlan: func(ctx context.Context, _ acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
			return acpwire.AuthoredPlan{}, b.run(ctx)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := sess.AuthorPlan(ctx, acpwire.AuthorPlanParams{Goal: "write it"})
		done <- err
	}()
	b.waitStart(t)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("AuthorPlan after its caller left = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AuthorPlan did not answer once its caller left")
	}
	after := waitRunGone(t, sess)
	if after.LastRun == nil || after.LastRun.Outcome != acpwire.RunCancelled {
		t.Fatalf("LastRun = %+v, want a cancelled run", after.LastRun)
	}
	if after.LastRun.Kind != acpwire.RunAuthor {
		t.Errorf("LastRun.Kind = %q, want %q", after.LastRun.Kind, acpwire.RunAuthor)
	}
}

// TestAQueuedTextIsPutBackWhenItsCallerLeft pins that the put-back belongs to
// the run, not the call: a caller that is already gone still gets its text
// back.
func TestAQueuedTextIsPutBackWhenItsCallerLeft(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	if _, err := state.RequestTurnStop("early"); err != nil {
		t.Fatalf("RequestTurnStop: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "kept", Queued: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Prompt after its caller left = %v, want context.Canceled", err)
	}

	waitRunGone(t, sess)
	if got := state.SteeringQueue(); !reflect.DeepEqual(got, []string{"kept"}) {
		t.Errorf("SteeringQueue() = %v, want the skipped text back", got)
	}
	if got := fake.goalsSeen(); len(got) != 0 {
		t.Errorf("runner goals = %v, want none", got)
	}
}

// TestOutcomesAreRecorded pins P4 D4's outcome rules and the one outcome that
// records an error.
func TestOutcomesAreRecorded(t *testing.T) {
	rows := []struct {
		name    string
		deps    func(*testing.T) Deps
		call    func(context.Context, *Session) error
		outcome acpwire.RunOutcome
		code    acpwire.ErrorCode
		// message is the sentence of an uncoded error: set it to expect one,
		// and leave both code and message empty to expect no error at all.
		message string
	}{
		{
			name: "done",
			deps: func(t *testing.T) Deps {
				fake := newFakeRunner()
				return Deps{State: newState(t, "s1"), Runner: func() Runner { return fake }}
			},
			call: func(ctx context.Context, s *Session) error {
				_, err := s.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
				return err
			},
			outcome: acpwire.RunDone,
		},
		{
			name: "failed",
			deps: func(t *testing.T) Deps {
				fake := newFakeRunner()
				fake.err = &provider.ProviderError{Provider: "local", StatusCode: 500, Body: "boom"}
				return Deps{State: newState(t, "s1"), Runner: func() Runner { return fake }}
			},
			call: func(ctx context.Context, s *Session) error {
				_, err := s.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
				return err
			},
			outcome: acpwire.RunFailed,
			code:    acpwire.CodeProviderError,
		},
		{
			// A plain error is not coded, so the wire keeps its sentence and
			// no code: decision 5's wrapping branch.
			name: "failed uncoded",
			deps: func(t *testing.T) Deps {
				fake := newFakeRunner()
				fake.err = errors.New("no provider is configured")
				return Deps{State: newState(t, "s1"), Runner: func() Runner { return fake }}
			},
			call: func(ctx context.Context, s *Session) error {
				_, err := s.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
				return err
			},
			outcome: acpwire.RunFailed,
			message: "no provider is configured",
		},
		{
			name: "cancelled",
			deps: func(t *testing.T) Deps {
				fake := newFakeRunner()
				fake.err = context.Canceled
				return Deps{State: newState(t, "s1"), Runner: func() Runner { return fake }}
			},
			call: func(ctx context.Context, s *Session) error {
				_, err := s.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
				return err
			},
			outcome: acpwire.RunCancelled,
		},
		{
			name: "gated",
			deps: func(t *testing.T) Deps {
				return Deps{
					State: newState(t, "s1"),
					NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) {
						return &fakePlanRun{gates: 1}, nil
					},
				}
			},
			call: func(ctx context.Context, s *Session) error {
				return s.StartSDD(ctx, acpwire.SDDParams{PlanPath: "plan.md"})
			},
			outcome: acpwire.RunGated,
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, sess := New(row.deps(t))
			_ = row.call(context.Background(), sess)

			after := waitRunGone(t, sess)
			if after.LastRun == nil {
				t.Fatal("LastRun is nil after the run ended")
			}
			if after.LastRun.Outcome != row.outcome {
				t.Fatalf("LastRun.Outcome = %q, want %q", after.LastRun.Outcome, row.outcome)
			}
			if row.code == "" && row.message == "" {
				if after.LastRun.Error != nil {
					t.Errorf("LastRun.Error = %+v, want none", after.LastRun.Error)
				}
				return
			}
			if after.LastRun.Error == nil {
				t.Fatal("LastRun.Error is nil, want the run's error")
			}
			if after.LastRun.Error.Code != row.code {
				t.Errorf("LastRun.Error.Code = %q, want %q", after.LastRun.Error.Code, row.code)
			}
			if row.message != "" && after.LastRun.Error.Message != row.message {
				t.Errorf("LastRun.Error.Message = %q, want %q", after.LastRun.Error.Message, row.message)
			}
		})
	}
}

// TestARefusalLeavesTheRunsAlone pins that a refused start sets neither field:
// it never became a run.
func TestARefusalLeavesTheRunsAlone(t *testing.T) {
	t.Run("busy", func(t *testing.T) {
		state := newState(t, "s1")
		fake := newFakeRunner()
		b := newBlocker()
		fake.setBody(b.run)
		_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

		done := make(chan error, 1)
		go func() {
			_, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "first"})
			done <- err
		}()
		b.waitStart(t)
		first := sess.Status().Run
		if first == nil {
			t.Fatal("Run is nil while a turn is in flight")
		}

		_, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "second"})
		if !acpclient.IsCode(err, acpwire.CodeBusy) {
			t.Fatalf("a second Prompt = %v, want code %q", err, acpwire.CodeBusy)
		}
		st := sess.Status()
		if st.Run == nil || st.Run.ID != first.ID {
			t.Fatalf("Run = %+v after the refusal, want %+v", st.Run, first)
		}
		if st.LastRun != nil {
			t.Errorf("a refused start recorded a last run: %+v", st.LastRun)
		}

		close(b.release)
		if err := <-done; err != nil {
			t.Fatalf("the first Prompt = %v, want no error", err)
		}
	})

	t.Run("quiescing", func(t *testing.T) {
		state := newState(t, "s1")
		fake := newFakeRunner()
		_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})
		state.BeginQuiesce()

		_, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "no"})
		if !acpclient.IsCode(err, acpwire.CodeQuiescing) {
			t.Fatalf("a Prompt on a quiescing session = %v, want code %q", err, acpwire.CodeQuiescing)
		}
		st := sess.Status()
		if st.Run != nil || st.LastRun != nil {
			t.Errorf("Run = %+v, LastRun = %+v, want neither", st.Run, st.LastRun)
		}
	})
}

// TestAReleaseEndsARunWithoutAUserStop pins that releasing the service still
// ends its run, and records it as cancelled.
func TestAReleaseEndsARunWithoutAUserStop(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	b := newBlocker()
	fake.setBody(b.run)
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	go func() {
		_, _ = sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
	}()
	b.waitStart(t)
	sess.Release()

	waitContextDone(t, b.workCtx())
	after := waitRunGone(t, sess)
	if after.LastRun == nil || after.LastRun.Outcome != acpwire.RunCancelled {
		t.Fatalf("LastRun = %+v, want a cancelled run", after.LastRun)
	}
}

// TestASwapEndsADetachedRun pins that a session a swap replaced still ends its
// run: it goes on the session's context, which the close cancels.
func TestASwapEndsADetachedRun(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	b := newBlocker()
	fake.setBody(b.run)
	host, sess := New(Deps{
		State:  state,
		Runner: func() Runner { return fake },
		NewSession: func(name string) (*session.State, error) {
			return newState(t, name), nil
		},
	})

	go func() {
		_, _ = sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
	}()
	b.waitStart(t)

	if _, err := host.NewSession(context.Background(), "s2"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	waitContextDone(t, b.workCtx())
	after := waitRunGone(t, sess)
	if after.LastRun == nil || after.LastRun.Outcome != acpwire.RunCancelled {
		t.Fatalf("LastRun = %+v, want a cancelled run", after.LastRun)
	}
}
