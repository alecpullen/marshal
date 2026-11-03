package inproc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
	"marshal/internal/watch"
)

// waitChanged fails unless Changed fires within 2 s.
func waitChanged(t *testing.T, sess *Session) {
	t.Helper()
	select {
	case _, ok := <-sess.Changed():
		if !ok {
			t.Fatalf("Changed closed, want a signal")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Changed did not fire within 2s")
	}
}

// waitClosed fails unless Changed reports closed within 2 s.
func waitClosed(t *testing.T, sess *Session) {
	t.Helper()
	select {
	case _, ok := <-sess.Changed():
		if ok {
			t.Fatalf("Changed delivered a signal, want closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Changed was not closed within 2s")
	}
}

// TestChangedFiresOnEachBroker pins that every broker the runtime supplies
// wakes the reader.
func TestChangedFiresOnEachBroker(t *testing.T) {
	cases := []struct {
		name    string
		deps    func(*session.State) Deps
		publish func(Deps)
	}{
		{
			name: "jobs",
			deps: func(st *session.State) Deps {
				return Deps{State: st, Jobs: pubsub.NewBroker[native.JobEvent]()}
			},
			publish: func(d Deps) { d.Jobs.Publish("job", native.JobEvent{}) },
		},
		{
			name: "watches",
			deps: func(st *session.State) Deps {
				return Deps{State: st, Watches: pubsub.NewBroker[watch.Event]()}
			},
			publish: func(d Deps) { d.Watches.Publish("watch", watch.Event{Name: "build"}) },
		},
		{
			name: "steering",
			deps: func(st *session.State) Deps {
				return Deps{State: st, Steering: pubsub.NewBroker[session.SteeringEvent]()}
			},
			publish: func(d Deps) { d.Steering.Publish("steering", session.SteeringEvent{}) },
		},
		{
			name: "workspace",
			deps: func(st *session.State) Deps {
				return Deps{State: st, Workspace: pubsub.NewBroker[session.WorkspaceEvent]()}
			},
			publish: func(d Deps) { d.Workspace.Publish("workspace", session.WorkspaceEvent{}) },
		},
		{
			name: "subagents",
			deps: func(st *session.State) Deps {
				return Deps{State: st, Subagents: pubsub.NewBroker[session.SubagentEvent]()}
			},
			publish: func(d Deps) { d.Subagents.Publish("subagent", session.SubagentEvent{}) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.deps(newState(t, "s1"))
			_, sess := New(d)
			tc.publish(d)
			waitChanged(t, sess)
		})
	}
}

// TestChangedHoldsOneSignal pins the coalescing buffer.
func TestChangedHoldsOneSignal(t *testing.T) {
	_, sess := New(Deps{State: newState(t, "s1")})
	if got := cap(sess.Changed()); got != 1 {
		t.Fatalf("cap(Changed()) = %d, want 1", got)
	}
}

// TestAResumeFireSetsThePendingResume pins the slot and its sequence: a resume
// fire replaces it, a fire without Resume leaves it, and the sequence rises.
func TestAResumeFireSetsThePendingResume(t *testing.T) {
	broker := pubsub.NewBroker[watch.Event]()
	_, sess := New(Deps{State: newState(t, "s1"), Watches: broker})

	broker.Publish("watch", watch.Event{Name: "build", Mode: watch.ModeRepeat, Resume: true})
	waitChanged(t, sess)
	if got, want := sess.Status().WatchResume, (&acpwire.WatchResume{Name: "build", Repeat: true, Seq: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("WatchResume = %+v, want %+v", got, want)
	}

	broker.Publish("watch", watch.Event{Name: "build", Mode: watch.ModeRepeat})
	waitChanged(t, sess)
	if got, want := sess.Status().WatchResume, (&acpwire.WatchResume{Name: "build", Repeat: true, Seq: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("WatchResume after a fire without Resume = %+v, want %+v", got, want)
	}

	broker.Publish("watch", watch.Event{Name: "lint", Mode: watch.ModeOnce, Resume: true})
	waitChanged(t, sess)
	if got, want := sess.Status().WatchResume, (&acpwire.WatchResume{Name: "lint", Repeat: false, Seq: 2}); !reflect.DeepEqual(got, want) {
		t.Fatalf("WatchResume = %+v, want %+v", got, want)
	}
}

// TestReceiptsKeepTheNewestRevisionPerRequest pins the per-request version rule
// and the turn-scoped newest-receipt read.
func TestReceiptsKeepTheNewestRevisionPerRequest(t *testing.T) {
	state := newState(t, "s1")
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{}, nil
	})
	turn, err := state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	broker := pubsub.NewBroker[session.SteeringEvent]()
	_, sess := New(Deps{State: state, Steering: broker})

	publish := func(r session.SteeringReceipt) {
		t.Helper()
		broker.Publish("steering", session.SteeringEvent{Receipt: &r})
		waitChanged(t, sess)
	}
	receipt := func(id string, version uint64, status session.SteeringStatus) session.SteeringReceipt {
		return session.SteeringReceipt{Version: version, RequestID: id, TurnID: turn, Status: status}
	}

	publish(receipt("req-a", 2, session.SteeringInterrupting))
	if got := sess.Status().Steering.Receipt; got == nil || got.Status != acpwire.SteeringInterrupting {
		t.Fatalf("receipt = %+v, want interrupting", got)
	}

	publish(receipt("req-a", 3, session.SteeringDelivered))
	if got := sess.Status().Steering.Receipt; got == nil || got.Status != acpwire.SteeringDelivered {
		t.Fatalf("receipt = %+v, want delivered", got)
	}

	publish(receipt("req-a", 2, session.SteeringInterrupting))
	if got := sess.Status().Steering.Receipt; got == nil || got.Status != acpwire.SteeringDelivered {
		t.Fatalf("receipt after a stale revision = %+v, want delivered", got)
	}

	publish(receipt("req-b", 1, session.SteeringAccepted))
	if got := sess.Status().Steering.Receipt; got == nil || got.RequestID != "req-a" || got.Status != acpwire.SteeringDelivered {
		t.Fatalf("receipt = %+v, want req-a delivered", got)
	}
}

// TestSwapClosesChanged pins that a swap closes the replaced session's signal,
// with or without a broker, and that the replacement still fires.
func TestSwapClosesChanged(t *testing.T) {
	broker := pubsub.NewBroker[session.SubagentEvent]()
	h, old := New(Deps{State: newState(t, "s1"), Subagents: broker})
	fresh := h.swap(newState(t, "s2"))

	waitClosed(t, old)

	broker.Publish("subagent", session.SubagentEvent{})
	waitChanged(t, fresh)

	// A session with no broker is closed by the swap too.
	h2, bare := New(Deps{State: newState(t, "s3")})
	h2.swap(newState(t, "s4"))
	waitClosed(t, bare)
}

// TestRunEndsWhenItsBrokersClose pins that the loop ends and closes Changed
// when every channel it held has closed.
func TestRunEndsWhenItsBrokersClose(t *testing.T) {
	broker := pubsub.NewBroker[native.JobEvent]()
	_, sess := New(Deps{State: newState(t, "s1"), Jobs: broker})

	broker.Close()
	waitClosed(t, sess)
}
