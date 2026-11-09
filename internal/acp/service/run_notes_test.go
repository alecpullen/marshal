package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// systemNotes returns the transcript's system messages, in order.
func systemNotes(state *session.State) []string {
	var notes []string
	for _, msg := range state.Messages() {
		if msg.Role == session.RoleSystem {
			notes = append(notes, msg.Content)
		}
	}
	return notes
}

// TestAUserStopWritesTheCancelNote pins P4 D6: the service writes the cancel
// note, on the run's own goroutine, before the caller's call returns.
func TestAUserStopWritesTheCancelNote(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	b := newBlocker()
	fake.setBody(b.run)
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	// The call has its own context and never cancels it: only the stop ends
	// this run.
	done := make(chan acpwire.PromptResult, 1)
	go func() {
		res, _ := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
		done <- res
	}()
	b.waitStart(t)
	if err := sess.Stop(context.Background(), "the user stopped it"); err != nil {
		t.Fatalf("Stop = %v", err)
	}

	if res := <-done; res.StopReason != acpwire.StopReasonCancelled {
		t.Fatalf("StopReason = %q, want %q", res.StopReason, acpwire.StopReasonCancelled)
	}
	if got, want := systemNotes(state), []string{"Agent turn cancelled."}; !reflect.DeepEqual(got, want) {
		t.Errorf("system notes = %q, want %q", got, want)
	}
}

// TestAFailedRunWritesItsNote pins the failure note and its one-line sentence,
// and the authoring run's own wording.
func TestAFailedRunWritesItsNote(t *testing.T) {
	t.Run("turn", func(t *testing.T) {
		state := newState(t, "s1")
		fake := newFakeRunner()
		fake.err = errors.New("the provider said no\nand gave a reason")
		_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

		if _, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"}); err == nil {
			t.Fatal("Prompt = nil, want the runner's error")
		}
		if got, want := systemNotes(state), []string{"✘ Turn failed: the provider said no"}; !reflect.DeepEqual(got, want) {
			t.Errorf("system notes = %q, want %q", got, want)
		}
	})

	t.Run("author", func(t *testing.T) {
		state := newState(t, "s1")
		_, sess := New(Deps{
			State: state,
			AuthorPlan: func(context.Context, acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
				return acpwire.AuthoredPlan{}, errors.New("the plan was refused\nbecause it is empty")
			},
		})

		if _, err := sess.AuthorPlan(context.Background(), acpwire.AuthorPlanParams{Goal: "write it"}); err == nil {
			t.Fatal("AuthorPlan = nil, want the author's error")
		}
		if got, want := systemNotes(state), []string{"✘ Plan authoring failed: the plan was refused"}; !reflect.DeepEqual(got, want) {
			t.Errorf("system notes = %q, want %q", got, want)
		}
	})
}

// TestNoNoteForAnEndingOrAGate pins the runs that write nothing: the session's
// own endings, a cancellation nobody asked for, a gate, and a success.
func TestNoNoteForAnEndingOrAGate(t *testing.T) {
	rows := []struct {
		name string
		run  func(*testing.T) *session.State
	}{
		{
			name: "release",
			run: func(t *testing.T) *session.State {
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
				waitRunGone(t, sess)
				return state
			},
		},
		{
			name: "close",
			run: func(t *testing.T) *session.State {
				state := newState(t, "s1")
				fake := newFakeRunner()
				b := newBlocker()
				fake.setBody(b.run)
				_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})
				go func() {
					_, _ = sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
				}()
				b.waitStart(t)
				if err := sess.Close(context.Background()); err != nil {
					t.Fatalf("Close = %v", err)
				}
				waitRunGone(t, sess)
				return state
			},
		},
		{
			name: "cancelled without a stop",
			run: func(t *testing.T) *session.State {
				state := newState(t, "s1")
				fake := newFakeRunner()
				fake.err = context.Canceled
				_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})
				_, _ = sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
				waitRunGone(t, sess)
				return state
			},
		},
		{
			name: "gated",
			run: func(t *testing.T) *session.State {
				state := newState(t, "s1")
				_, sess := New(Deps{
					State: state,
					NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) {
						return &fakePlanRun{gates: 1}, nil
					},
				})
				_ = sess.StartSDD(context.Background(), acpwire.SDDParams{PlanPath: "plan.md"})
				waitRunGone(t, sess)
				return state
			},
		},
		{
			name: "done",
			run: func(t *testing.T) *session.State {
				state := newState(t, "s1")
				fake := newFakeRunner()
				_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})
				if _, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"}); err != nil {
					t.Fatalf("Prompt = %v", err)
				}
				return state
			},
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			state := row.run(t)
			if got := systemNotes(state); len(got) != 0 {
				t.Errorf("system notes = %q, want none", got)
			}
		})
	}
}

// TestTheNoteLandsWhenTheCallerLeft pins that the note does not depend on the
// caller still being there: it is written once the run has actually ended.
func TestTheNoteLandsWhenTheCallerLeft(t *testing.T) {
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
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Prompt after its caller left = %v, want context.Canceled", err)
	}

	if err := sess.Stop(context.Background(), "the user stopped it"); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	waitRunGone(t, sess)
	if got, want := systemNotes(state), []string{"Agent turn cancelled."}; !reflect.DeepEqual(got, want) {
		t.Errorf("system notes = %q, want %q", got, want)
	}
}

// TestARunNoteIsWrittenOnce pins that one run writes one note.
func TestARunNoteIsWrittenOnce(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	b := newBlocker()
	fake.setBody(b.run)
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	done := make(chan error, 1)
	go func() {
		_, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
		done <- err
	}()
	b.waitStart(t)
	if err := sess.Stop(context.Background(), "the user stopped it"); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Prompt = %v, want no error", err)
	}

	waitRunGone(t, sess)
	if got := systemNotes(state); len(got) != 1 {
		t.Errorf("system notes = %q, want exactly one", got)
	}
}
