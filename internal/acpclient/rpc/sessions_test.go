package rpc

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// TestCloseRetiresTheSession pins what a close does to the client's side: one
// session/close, and afterwards a retired session whose Changed is closed,
// whose reads answer the mirror's final values, and which a second close does
// not send for (decision 6).
func TestCloseRetiresTheSession(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	drainChanged(t, session.Changed())

	out := startErr(func() error { return session.Close(context.Background()) })
	req := nextMethod(t, a, acpwire.MethodSessionClose)
	want := acpwire.ControlParams{SessionID: "s1"}
	if got := decodeCall[acpwire.ControlParams](t, req); got != want {
		t.Fatalf("session/close params = %+v, want %+v", got, want)
	}
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case _, ok := <-session.Changed():
		if ok {
			t.Fatal("Changed fired instead of closing when the session was retired")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not close when the session was retired")
	}

	if got := session.Status().WorkingDir; got != "B" {
		t.Fatalf("Status().WorkingDir = %q after the close, want B: the reads keep the mirror's final values", got)
	}

	// A retired session is unregistered, so no update reaches it.
	a.send(t, statusFrame(t, "s1", acpwire.SessionStatus{WorkingDir: "C"}))
	pingScripted(t, host, a)
	if got := session.Status().WorkingDir; got != "B" {
		t.Fatalf("Status().WorkingDir = %q, want B: an update must not reach a retired session", got)
	}

	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("the second Close: %v", err)
	}
	select {
	case req := <-a.reqs:
		t.Fatalf("the second Close sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestACloseTheAgentReportsClosedRetiresIt pins the other success: an agent
// that answers session/close with CodeSessionClosed retires the session too,
// and the close after it sends nothing.
func TestACloseTheAgentReportsClosedRetiresIt(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	drainChanged(t, session.Changed())

	out := startErr(func() error { return session.Close(context.Background()) })
	req := nextMethod(t, a, acpwire.MethodSessionClose)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "the session is already closed",
	}))
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("Close of a session the agent reports closed = %v, want nil", err)
	}

	select {
	case _, ok := <-session.Changed():
		if ok {
			t.Fatal("Changed fired instead of closing when the session was retired")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not close for a session the agent reports closed")
	}
	if got := host.session("s1"); got != nil {
		t.Fatalf("the session the agent reports closed is still registered: %v", got)
	}

	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("the second Close: %v", err)
	}
	select {
	case req := <-a.reqs:
		t.Fatalf("the second Close sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestACloseInFlightIsNotSentAgain pins the guard: a Close that arrives while
// another one is still waiting for its answer sends nothing.
func TestACloseInFlightIsNotSentAgain(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	first := startErr(func() error { return session.Close(context.Background()) })
	req := nextMethod(t, a, acpwire.MethodSessionClose)

	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("the Close that arrived while one was in flight = %v, want nil", err)
	}
	select {
	case extra := <-a.reqs:
		t.Fatalf("the second Close sent %q", extra.Method)
	case <-time.After(250 * time.Millisecond):
	}

	a.answer(t, req, struct{}{})
	if err := awaitCall(t, first); err != nil {
		t.Fatalf("the first Close: %v", err)
	}
}

// TestACloseThatFailsCanBeTriedAgain pins that a refused close clears the
// in-flight flag, so the next one is sent.
func TestACloseThatFailsCanBeTriedAgain(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	first := startErr(func() error { return session.Close(context.Background()) })
	req := nextMethod(t, a, acpwire.MethodSessionClose)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeBusy,
		Message: "a turn is running",
	}))
	if err := awaitCall(t, first); err == nil {
		t.Fatal("the refused Close returned no error")
	}

	second := startErr(func() error { return session.Close(context.Background()) })
	req = nextMethod(t, a, acpwire.MethodSessionClose)
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, second); err != nil {
		t.Fatalf("the Close after a refused one: %v", err)
	}
}

// TestACloseThatFailsKeepsTheSession pins that only success and
// CodeSessionClosed retire a session: any other error is the caller's, and the
// session keeps its mirror and its open Changed.
func TestACloseThatFailsKeepsTheSession(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	drainChanged(t, session.Changed())

	out := startErr(func() error { return session.Close(context.Background()) })
	req := nextMethod(t, a, acpwire.MethodSessionClose)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeBusy,
		Message: "a turn is running",
	}))
	err := awaitCall(t, out)
	if !acpclient.IsCode(err, acpwire.CodeBusy) {
		t.Fatalf("Close error = %v, want an error with code %q", err, acpwire.CodeBusy)
	}

	if id, err := host.currentID(); err != nil || id != "s1" {
		t.Fatalf("currentID() = %q, %v after a failed close, want s1", id, err)
	}
	if got := session.Status().WorkingDir; got != "B" {
		t.Fatalf("Status().WorkingDir = %q after a failed close, want B", got)
	}
	select {
	case _, ok := <-session.Changed():
		if !ok {
			t.Fatal("Changed closed after a close the agent refused")
		}
	default:
	}
}

// TestListSessionsNamesTheCurrentSession pins session/list's frame and result,
// and that before any attach there is no session to name, so nothing is sent.
func TestListSessionsNamesTheCurrentSession(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})

	if rows, err := host.ListSessions(context.Background(), "/work", 5); !errors.Is(err, errNoSession) {
		t.Fatalf("ListSessions before any attach = %v, %v, want %v", rows, err, errNoSession)
	}
	select {
	case req := <-a.reqs:
		t.Fatalf("ListSessions before any attach sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}

	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	rows := []acpwire.SessionEntry{
		{SessionID: "s0", Title: "earlier", UpdatedAt: 10, MessageCount: 3},
		{SessionID: "s1", Title: "now"},
	}
	out := startCall(func() ([]acpwire.SessionEntry, error) {
		return host.ListSessions(context.Background(), "/work", 5)
	})
	req := nextMethod(t, a, acpwire.MethodSessionList)
	want := acpwire.ListSessionsParams{SessionID: session.ID(), Cwd: "/work", Limit: 5}
	if got := decodeCall[acpwire.ListSessionsParams](t, req); got != want {
		t.Fatalf("session/list params = %+v, want %+v", got, want)
	}
	a.answer(t, req, acpwire.ListSessionsResult{Sessions: rows})

	res := awaitValue(t, out)
	if res.err != nil {
		t.Fatalf("ListSessions: %v", res.err)
	}
	if !reflect.DeepEqual(res.value, rows) {
		t.Fatalf("ListSessions = %+v, want %+v", res.value, rows)
	}
}

// TestReplaceSessionAttachesTheNewOne pins the replacement: session/new names
// the current session as replaces, the new session attaches, the host's current
// session is the new one, and the old session is retired with its stacks
// forgotten (decisions 5 and 6).
func TestReplaceSessionAttachesTheNewOne(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	old := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	drainChanged(t, old.Changed())

	// A mirrored stack, so the replacement's forget has something to drop.
	reads := readStack(host.stacks, "s1", 0)
	stackReq := nextMethod(t, a, acpwire.MethodStack)
	a.answer(t, stackReq, turnSnapshot("s1", 0, 1, "step:1"))
	if read := <-reads; read.err != nil {
		t.Fatalf("read the old session's stack: %v", read.err)
	}
	drainChanged(t, old.Changed())

	out := startCall(func() (*Session, error) {
		return host.replaceSession(context.Background(), "continued")
	})
	req := nextMethod(t, a, acpwire.MethodSessionNew)
	want := acpwire.NewSessionParams{Replaces: "s1", Name: "continued"}
	if got := decodeCall[acpwire.NewSessionParams](t, req); got != want {
		t.Fatalf("session/new params = %+v, want %+v", got, want)
	}
	a.answer(t, req, acpwire.NewSessionResult{SessionID: "s2"})

	statusReq := nextMethod(t, a, acpwire.MethodStatus)
	if got := decodeCall[acpwire.StatusParams](t, statusReq); got.SessionID != "s2" {
		t.Fatalf("session/status named %q, want s2: the new session is attached", got.SessionID)
	}
	a.answer(t, statusReq, acpwire.SessionStatus{WorkingDir: "D"})

	res := awaitValue(t, out)
	if res.err != nil {
		t.Fatalf("replaceSession: %v", res.err)
	}
	if got := res.value.ID(); got != "s2" {
		t.Fatalf("the new session's ID = %q, want s2", got)
	}
	if got := res.value.Status().WorkingDir; got != "D" {
		t.Fatalf("the new session's Status().WorkingDir = %q, want D", got)
	}
	if id, err := host.currentID(); err != nil || id != "s2" {
		t.Fatalf("currentID() = %q, %v after a replacement, want s2", id, err)
	}

	select {
	case _, ok := <-old.Changed():
		if ok {
			t.Fatal("the old session's Changed fired instead of closing")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the old session's Changed did not close")
	}

	// A patch for the old ID reaches no mirror entry: the replacement forgot
	// the stacks of the session it replaced.
	a.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     2,
		BaseRev: 1,
		Roots:   []string{"turn:1"},
	}))
	pingScripted(t, host, a)

	host.stacks.mu.Lock()
	_, entry := host.stacks.entries[stackKey{session: "s1"}]
	_, changed := host.stacks.changed["s1"]
	host.stacks.mu.Unlock()
	if entry {
		t.Fatal("the old session's stack survived the replacement")
	}
	if changed {
		t.Fatal("the old session's stack Changed channel survived the replacement")
	}
}

// TestAFailedReplacementKeepsTheCurrentSession pins R7: an agent that refuses
// the replacement leaves the current session attached, with its Changed open.
func TestAFailedReplacementKeepsTheCurrentSession(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	old := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	drainChanged(t, old.Changed())

	out := startCall(func() (*Session, error) {
		return host.replaceSession(context.Background(), "continued")
	})
	req := nextMethod(t, a, acpwire.MethodSessionNew)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeBusy,
		Message: "a turn is running",
	}))

	res := awaitValue(t, out)
	if !acpclient.IsCode(res.err, acpwire.CodeBusy) {
		t.Fatalf("replaceSession error = %v, want an error with code %q", res.err, acpwire.CodeBusy)
	}
	if res.value != nil {
		t.Fatalf("replaceSession returned %v alongside its error", res.value)
	}

	if id, err := host.currentID(); err != nil || id != "s1" {
		t.Fatalf("currentID() = %q, %v after a failed replacement, want s1", id, err)
	}
	if got := host.session("s1"); got != old {
		t.Fatalf("the current session after a failed replacement = %v, want the one it had", got)
	}
	if extra := host.session("s2"); extra != nil {
		t.Fatalf("a failed replacement attached %v", extra)
	}
	select {
	case _, ok := <-old.Changed():
		if !ok {
			t.Fatal("the current session's Changed closed after a failed replacement")
		}
	default:
	}
	select {
	case req := <-a.reqs:
		t.Fatalf("a failed replacement went on to send %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestNewSessionIsTheReplacement pins the interface's NewSession: it is the
// replacement, and a refusal answers a nil interface rather than a typed nil
// (decision 8).
func TestNewSessionIsTheReplacement(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := startCall(func() (acpclient.Session, error) {
		return host.NewSession(context.Background(), "continued")
	})
	req := nextMethod(t, a, acpwire.MethodSessionNew)
	want := acpwire.NewSessionParams{Replaces: "s1", Name: "continued"}
	if got := decodeCall[acpwire.NewSessionParams](t, req); got != want {
		t.Fatalf("session/new params = %+v, want %+v", got, want)
	}
	a.answer(t, req, acpwire.NewSessionResult{SessionID: "s2"})

	statusReq := nextMethod(t, a, acpwire.MethodStatus)
	if got := decodeCall[acpwire.StatusParams](t, statusReq); got.SessionID != "s2" {
		t.Fatalf("session/status named %q, want s2: the new session is attached", got.SessionID)
	}
	a.answer(t, statusReq, acpwire.SessionStatus{WorkingDir: "D"})

	res := awaitValue(t, out)
	if res.err != nil {
		t.Fatalf("NewSession: %v", res.err)
	}
	if res.value == nil {
		t.Fatal("NewSession returned a nil session")
	}
	if got := res.value.ID(); got != "s2" {
		t.Fatalf("NewSession's session ID = %q, want s2", got)
	}
	if got := res.value.Status().WorkingDir; got != "D" {
		t.Fatalf("NewSession's Status().WorkingDir = %q, want D", got)
	}
	if id, err := host.currentID(); err != nil || id != "s2" {
		t.Fatalf("currentID() = %q, %v after NewSession, want s2", id, err)
	}

	// A refusal answers a nil interface, not a typed nil a caller could not
	// tell from a session.
	failed := startCall(func() (acpclient.Session, error) {
		return host.NewSession(context.Background(), "again")
	})
	refused := nextMethod(t, a, acpwire.MethodSessionNew)
	a.send(t, codedErrorFrame(t, string(refused.ID), acpwire.Error{
		Code:    acpwire.CodeBusy,
		Message: "a turn is running",
	}))
	failure := awaitValue(t, failed)
	if !acpclient.IsCode(failure.err, acpwire.CodeBusy) {
		t.Fatalf("NewSession error = %v, want code %q", failure.err, acpwire.CodeBusy)
	}
	if failure.value != nil {
		t.Fatalf("NewSession returned %#v alongside its error, want a nil interface", failure.value)
	}
}
