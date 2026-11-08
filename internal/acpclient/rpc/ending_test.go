package rpc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
)

// endWorkFrame builds the session/update notification carrying one end_work
// update.
func endWorkFrame(t *testing.T, sessionID string, work acpwire.EndWork) string {
	t.Helper()
	return updateFrame(t, sessionID, acpwire.EndWorkUpdate{Kind: acpwire.UpdateEndWork, Work: work})
}

// endWorkOf is a membership the tests recognize by its items' IDs, oldest
// first.
func endWorkOf(ids ...string) acpwire.EndWork {
	work := acpwire.EndWork{}
	for _, id := range ids {
		work.Items = append(work.Items, acpwire.EndWorkItem{ID: id, Label: "run " + id, Group: "g1"})
	}
	return work
}

// readActiveWork starts one ActiveWork read on its own goroutine, so the test
// can answer its call.
func readActiveWork(session *Session) <-chan acpwire.EndWork {
	out := make(chan acpwire.EndWork, 1)
	go func() { out <- session.ActiveWork() }()
	return out
}

// awaitActiveWork waits for one ActiveWork read's outcome.
func awaitActiveWork(t *testing.T, out <-chan acpwire.EndWork) acpwire.EndWork {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("ActiveWork did not return")
		return acpwire.EndWork{}
	}
}

// nextEndWork reads the client's next frame and checks it is session/end_work
// for session.
func nextEndWork(t *testing.T, a *scriptedAgent, session *Session) fakeRequest {
	t.Helper()
	req := nextMethod(t, a, acpwire.MethodEndWork)
	want := acpwire.ControlParams{SessionID: session.ID()}
	if got := decodeCall[acpwire.ControlParams](t, req); got != want {
		t.Fatalf("end_work params = %+v, want %+v", got, want)
	}
	return req
}

// firstActiveWork performs the mirror's first read, answering session/end_work
// with work, and returns what that read reported.
func firstActiveWork(t *testing.T, session *Session, a *scriptedAgent, work acpwire.EndWork) acpwire.EndWork {
	t.Helper()
	out := readActiveWork(session)
	req := nextEndWork(t, a, session)
	a.answer(t, req, work)
	return awaitActiveWork(t, out)
}

// TestActiveWorkFetchesOnItsFirstRead pins the two halves of the read: the
// first one asks the agent for the live membership, and every later one
// answers from the mirror.
func TestActiveWorkFetchesOnItsFirstRead(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	want := endWorkOf("w1", "w2")
	if got := firstActiveWork(t, session, a, want); !reflect.DeepEqual(got, want) {
		t.Fatalf("the first read = %+v, want %+v", got, want)
	}
	if got := session.ActiveWork(); !reflect.DeepEqual(got, want) {
		t.Fatalf("the second read = %+v, want %+v", got, want)
	}
	select {
	case req := <-a.reqs:
		t.Fatalf("the second read sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestActiveWorkInstallsTheUpdateThatPrecedesTheResponse pins the barrier: the
// agent answers session/end_work with the membership it had, so an update that
// reaches the client before the response is newer and must be kept.
func TestActiveWorkInstallsTheUpdateThatPrecedesTheResponse(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := readActiveWork(session)
	req := nextEndWork(t, a, session)
	newer := endWorkOf("w1", "w2", "w3")
	a.send(t, endWorkFrame(t, "s1", newer))
	a.answer(t, req, endWorkOf("w1"))

	if got := awaitActiveWork(t, out); !reflect.DeepEqual(got, newer) {
		t.Fatalf("the read = %+v, want %+v: the update that preceded the response is newer", got, newer)
	}
}

// TestAnEndWorkUpdateReplacesTheMembershipAndFiresChanged pins the end_work
// stream: an update replaces the mirrored membership and taps Changed.
func TestAnEndWorkUpdateReplacesTheMembershipAndFiresChanged(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	firstActiveWork(t, session, a, endWorkOf("w1"))
	changed := session.Changed()
	drainChanged(t, changed)

	newer := endWorkOf("w1", "w2")
	a.send(t, endWorkFrame(t, "s1", newer))
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not fire for the update")
	}
	if got := session.ActiveWork(); !reflect.DeepEqual(got, newer) {
		t.Fatalf("the mirror = %+v, want %+v", got, newer)
	}
}

// TestAFailedActiveWorkReadAsksAgain pins the failure: the read reports an
// empty membership, and the read after it asks the agent again.
func TestAFailedActiveWorkReadAsksAgain(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	failed := readActiveWork(session)
	req := nextEndWork(t, a, session)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "the session is gone",
	}))
	if got := awaitActiveWork(t, failed); !reflect.DeepEqual(got, acpwire.EndWork{}) {
		t.Fatalf("a failed read = %+v, want an empty membership", got)
	}

	want := endWorkOf("w1")
	if got := firstActiveWork(t, session, a, want); !reflect.DeepEqual(got, want) {
		t.Fatalf("the read after the failure = %+v, want %+v", got, want)
	}
}

// TestTheEndCallsSendTheirMethods pins the four end calls: each sends its
// method with the session's ID and its params, and returns and decodes its
// result.
func TestTheEndCallsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	accept := acpwire.EndAcceptParams{
		Intent:           acpwire.EndIntentExit,
		DisplayedIDs:     []string{"w1"},
		RequireDisplayed: true,
	}
	accepted := startCall(func() (acpwire.EndDecision, error) {
		return session.AcceptEnd(context.Background(), accept)
	})
	req := nextMethod(t, a, acpwire.MethodEndAccept)
	wantAccept := acpwire.EndAcceptCallParams{SessionID: "s1", Accept: accept}
	if got := decodeCall[acpwire.EndAcceptCallParams](t, req); !reflect.DeepEqual(got, wantAccept) {
		t.Fatalf("end_accept params = %+v, want %+v", got, wantAccept)
	}
	decision := acpwire.EndDecision{Accepted: true, Work: endWorkOf("w1")}
	a.answer(t, req, decision)
	acceptedEnd := awaitValue(t, accepted)
	if acceptedEnd.err != nil {
		t.Fatalf("AcceptEnd: %v", acceptedEnd.err)
	}
	if !reflect.DeepEqual(acceptedEnd.value, decision) {
		t.Fatalf("AcceptEnd = %+v, want %+v", acceptedEnd.value, decision)
	}

	out := startErr(func() error { return session.WaitForEnd(context.Background()) })
	req = nextMethod(t, a, acpwire.MethodEndWait)
	wantWait := acpwire.ControlParams{SessionID: "s1"}
	if got := decodeCall[acpwire.ControlParams](t, req); got != wantWait {
		t.Fatalf("end_wait params = %+v, want %+v", got, wantWait)
	}
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("WaitForEnd: %v", err)
	}

	out = startErr(func() error { return session.RunExitAnalysis(context.Background(), "ship it") })
	req = nextMethod(t, a, acpwire.MethodExitAnalysis)
	wantAnalysis := acpwire.ExitAnalysisParams{SessionID: "s1", Goal: "ship it"}
	if got := decodeCall[acpwire.ExitAnalysisParams](t, req); got != wantAnalysis {
		t.Fatalf("exit_analysis params = %+v, want %+v", got, wantAnalysis)
	}
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("RunExitAnalysis: %v", err)
	}

	report := startCall(func() (string, error) { return session.Postmortem(context.Background()) })
	req = nextMethod(t, a, acpwire.MethodPostmortem)
	wantReport := acpwire.ControlParams{SessionID: "s1"}
	if got := decodeCall[acpwire.ControlParams](t, req); got != wantReport {
		t.Fatalf("postmortem params = %+v, want %+v", got, wantReport)
	}
	a.answer(t, req, acpwire.PostmortemResult{Path: "/tmp/postmortem.md"})
	posted := awaitValue(t, report)
	if posted.err != nil {
		t.Fatalf("Postmortem: %v", posted.err)
	}
	if posted.value != "/tmp/postmortem.md" {
		t.Fatalf("Postmortem = %q, want /tmp/postmortem.md", posted.value)
	}
}
