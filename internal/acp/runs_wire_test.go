package acp

import (
	"context"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// startWireClient serves the control handlers over a pipe and dials a marshal
// client's host over it, so a test reads what the client itself holds: the
// mirrored status and the transcript it reads. It returns the host, because a
// test that ends the connection ends it.
func startWireClient(t *testing.T, setup controlSetup) (*rpc.Host, *rpc.Session, *controlAgent) {
	t.Helper()

	transport, a := serveControl(t, setup)
	ctx := context.Background()
	host, err := rpc.Dial(ctx, transport, rpc.DialOptions{SharedFilesystem: true})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	sess, err := host.Attach(ctx, controlSession)
	if err != nil {
		t.Fatalf("attach %q: %v", controlSession, err)
	}
	return host, sess, a
}

// awaitStatus polls the client's mirrored status until ok holds, and fails at
// the deadline: the mirror is fed by status updates, so a read may need a
// forwarder's pass.
func awaitStatus(t *testing.T, sess *rpc.Session, within time.Duration, what string, ok func(acpwire.SessionStatus) bool) acpwire.SessionStatus {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := sess.Status()
		if ok(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the status never showed %s within %v: it reads %+v", what, within, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// promptOverWire starts session/prompt on its own goroutine, so the call stays
// in flight while the test acts.
func promptOverWire(sess *rpc.Session, text string) <-chan controlReply {
	done := make(chan controlReply, 1)
	go func() {
		res, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: text})
		done <- controlReply{res: res, err: err}
	}()
	return done
}

// TestARunOutlivesItsConnection pins P4 D4 over the wire: a client whose
// connection ends leaves the run going, and the status still reports it.
func TestARunOutlivesItsConnection(t *testing.T) {
	runs := make(chan context.Context, 1)
	release := make(chan struct{})
	runner := newControlRunner()
	runner.setBody(func(ctx context.Context) error {
		runs <- ctx
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	host, sess, a := startWireClient(t, controlSetup{
		marshal: true,
		deps:    service.Deps{State: seamState(t, controlSession), Runner: func() service.Runner { return runner }},
	})

	promptOverWire(sess, "hold this turn open")
	runner.waitStart(t)
	var run context.Context
	select {
	case run = <-runs:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner never got its context")
	}

	// The client's connection ends.
	if err := host.Close(); err != nil {
		t.Fatalf("close the connection: %v", err)
	}

	// The run is the session's, so it goes on: its own context is untouched,
	// and the status the agent serves still carries it.
	time.Sleep(200 * time.Millisecond)
	if err := run.Err(); err != nil {
		t.Fatalf("the run's context ended with the connection: %v", err)
	}
	if flight := a.served.Status().Run; flight == nil || flight.Kind != acpwire.RunTurn {
		t.Fatalf("the status carries %+v, want the turn still in flight", flight)
	}

	// Releasing the runner finishes the run, and its ending is still recorded.
	close(release)
	waitForFrame(t, 2*time.Second, "the finished run in the status", func() bool {
		last := a.served.Status().LastRun
		return last != nil && last.Outcome == acpwire.RunDone
	})
}

// TestTheStatusCarriesTheRun pins that a marshal client sees the run in the
// status it holds, and then its ending: run with kind turn, then lastRun
// (P4 D4).
func TestTheStatusCarriesTheRun(t *testing.T) {
	release := make(chan struct{})
	runner := newControlRunner()
	runner.setBody(func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	_, sess, _ := startWireClient(t, controlSetup{
		marshal: true,
		deps:    service.Deps{State: seamState(t, controlSession), Runner: func() service.Runner { return runner }},
	})

	call := promptOverWire(sess, "hold this turn open")
	runner.waitStart(t)

	flight := awaitStatus(t, sess, 2*time.Second, "the run in flight", func(st acpwire.SessionStatus) bool {
		return st.Run != nil
	})
	if flight.Run.Kind != acpwire.RunTurn {
		t.Errorf("the run's kind = %q, want %q", flight.Run.Kind, acpwire.RunTurn)
	}
	if flight.LastRun != nil {
		t.Errorf("the status carries lastRun %+v while the run is in flight", flight.LastRun)
	}

	close(release)
	select {
	case r := <-call:
		if r.err != nil {
			t.Fatalf("session/prompt: %v", r.err)
		}
		if r.res.StopReason != acpwire.StopReasonEndTurn {
			t.Fatalf("stop reason = %q, want %q", r.res.StopReason, acpwire.StopReasonEndTurn)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt did not return")
	}

	ended := awaitStatus(t, sess, 2*time.Second, "the run's ending", func(st acpwire.SessionStatus) bool {
		return st.LastRun != nil
	})
	if ended.LastRun.Outcome != acpwire.RunDone {
		t.Errorf("LastRun.Outcome = %q, want %q", ended.LastRun.Outcome, acpwire.RunDone)
	}
	if ended.LastRun.Kind != acpwire.RunTurn {
		t.Errorf("LastRun.Kind = %q, want %q", ended.LastRun.Kind, acpwire.RunTurn)
	}
	if ended.Run != nil {
		t.Errorf("the status still carries run %+v after its ending", ended.Run)
	}
}

// TestTheCancelNoteReachesTheCaller pins P4 D6 over the wire: a user's stop
// writes the cancel note before the waiting call answers, so the client's next
// transcript read shows it.
func TestTheCancelNoteReachesTheCaller(t *testing.T) {
	runner := newControlRunner()
	runner.setBody(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	// A transcript read needs the transcript handlers, which the harness serves
	// when the setup names a broker.
	_, sess, _ := startWireClient(t, controlSetup{
		marshal: true,
		deps:    service.Deps{State: seamState(t, controlSession), Runner: func() service.Runner { return runner }},
		broker:  pubsub.NewBroker[session.Event](),
	})

	call := promptOverWire(sess, "hold this turn open")
	runner.waitStart(t)

	if err := sess.Stop(context.Background(), "turn stopped"); err != nil {
		t.Fatalf("session/stop: %v", err)
	}

	select {
	case r := <-call:
		if r.err != nil {
			t.Fatalf("session/prompt after the stop: %v", r.err)
		}
		if r.res.StopReason != acpwire.StopReasonCancelled {
			t.Fatalf("stop reason = %q, want %q", r.res.StopReason, acpwire.StopReasonCancelled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt did not answer after session/stop")
	}

	// The note is in the state before the response was written, so the client's
	// next read of the transcript carries it.
	const note = "Agent turn cancelled."
	if snap := sess.Stack(0, false); !stackHoldsText(snap, note) {
		t.Fatalf("the read after the stop does not hold %q", note)
	}
}
