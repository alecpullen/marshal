package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// scriptedAgent is the agent end of a piped connection the test drives: a read
// loop hands it each request the client sends, and the test writes the frames
// back in the order it chooses.
type scriptedAgent struct {
	client Transport
	agent  io.ReadWriteCloser
	reqs   chan fakeRequest
}

// startScripted starts the agent end of a pipe, reading the client's requests.
func startScripted(t *testing.T) *scriptedAgent {
	t.Helper()
	client, agent := Pipe()
	a := &scriptedAgent{client: client, agent: agent, reqs: make(chan fakeRequest, 32)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(agent)
		sc.Buffer(make([]byte, 0, 64*1024), MaxFrameBytes)
		for sc.Scan() {
			var req fakeRequest
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				return
			}
			a.reqs <- req
		}
	}()

	t.Cleanup(func() {
		_ = agent.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the scripted agent did not stop")
		}
	})
	return a
}

// next returns the next request the client sent.
func (a *scriptedAgent) next(t *testing.T) fakeRequest {
	t.Helper()
	select {
	case req := <-a.reqs:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("the client sent no frame")
		return fakeRequest{}
	}
}

// send writes one raw frame to the client.
func (a *scriptedAgent) send(t *testing.T, frame string) {
	t.Helper()
	if err := writeLine(a.agent, []byte(frame)); err != nil {
		t.Fatalf("write agent frame: %v", err)
	}
}

// answer responds to req with result.
func (a *scriptedAgent) answer(t *testing.T, req fakeRequest, result any) {
	t.Helper()
	a.send(t, resultFrame(t, string(req.ID), result))
}

// statusFrame builds the session/update notification carrying one
// session_status update.
func statusFrame(t *testing.T, sessionID string, status acpwire.SessionStatus) string {
	t.Helper()
	return updateFrame(t, sessionID, acpwire.StatusUpdate{Kind: acpwire.UpdateSessionStatus, Status: status})
}

// codedErrorFrame builds the -32000 response that carries a coded error, as
// the agent sends one.
func codedErrorFrame(t *testing.T, id string, coded acpwire.Error) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    -32000,
			"message": coded.Message,
			"data":    coded,
		},
	})
	if err != nil {
		t.Fatalf("marshal error frame: %v", err)
	}
	return string(body)
}

// dialResult is one Dial call's outcome.
type dialResult struct {
	host *Host
	err  error
}

// dialScripted starts a scripted agent, answers its initialize the way
// startFakeAgent's agents do, and returns the host.
func dialScripted(t *testing.T, opts DialOptions) (*Host, *scriptedAgent) {
	t.Helper()
	a := startScripted(t)

	out := make(chan dialResult, 1)
	go func() {
		host, err := Dial(context.Background(), a.client, opts)
		out <- dialResult{host: host, err: err}
	}()

	init := a.next(t)
	if got := string(init.Method); got != "initialize" {
		t.Fatalf("the first request's method = %q, want initialize", got)
	}
	a.answer(t, init, marshalAnswer("1.2.3"))

	select {
	case res := <-out:
		if res.err != nil {
			t.Fatalf("Dial: %v", res.err)
		}
		t.Cleanup(func() { _ = res.host.Close() })
		return res.host, a
	case <-time.After(5 * time.Second):
		t.Fatal("Dial did not return")
		return nil, nil
	}
}

// attachResult is one Attach call's outcome.
type attachResult struct {
	session *Session
	err     error
}

// startAttach starts one Attach on its own goroutine, so the test can answer it.
func startAttach(host *Host, sessionID string) <-chan attachResult {
	out := make(chan attachResult, 1)
	go func() {
		session, err := host.Attach(context.Background(), sessionID)
		out <- attachResult{session: session, err: err}
	}()
	return out
}

// awaitAttach waits for one Attach outcome.
func awaitAttach(t *testing.T, out <-chan attachResult) attachResult {
	t.Helper()
	select {
	case res := <-out:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("Attach did not return")
		return attachResult{}
	}
}

// attachScripted attaches sessionID, answering session/status with status.
func attachScripted(t *testing.T, host *Host, a *scriptedAgent, sessionID string, status acpwire.SessionStatus) *Session {
	t.Helper()
	out := startAttach(host, sessionID)
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodStatus {
		t.Fatalf("Attach sent %q, want %q", got, acpwire.MethodStatus)
	}
	a.answer(t, req, status)
	res := awaitAttach(t, out)
	if res.err != nil {
		t.Fatalf("Attach: %v", res.err)
	}
	return res.session
}

// pingScripted round-trips one call, so the read goroutine has dispatched every
// frame the agent sent before it.
func pingScripted(t *testing.T, host *Host, a *scriptedAgent) {
	t.Helper()
	out := make(chan error, 1)
	go func() { out <- host.conn.Call(context.Background(), "test/ping", nil, nil) }()
	req := a.next(t)
	if got := string(req.Method); got != "test/ping" {
		t.Fatalf("the client's next frame = %q, want test/ping", got)
	}
	a.answer(t, req, struct{}{})
	select {
	case err := <-out:
		if err != nil {
			t.Fatalf("ping call: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the ping did not return")
	}
}

// awaitCall waits for one call's outcome.
func awaitCall(t *testing.T, out <-chan error) error {
	t.Helper()
	select {
	case err := <-out:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not return")
		return nil
	}
}

// TestAttachInstallsTheUpdateThatPrecedesTheResponse pins the barrier: the
// agent answers session/status with the status it had, so an update that
// reaches the client before the response is newer and must be kept.
func TestAttachInstallsTheUpdateThatPrecedesTheResponse(t *testing.T) {
	host, a := dialScripted(t, DialOptions{SharedFilesystem: true})

	out := startAttach(host, "s1")
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodStatus {
		t.Fatalf("Attach sent %q, want %q", got, acpwire.MethodStatus)
	}
	var params acpwire.StatusParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode status params: %v", err)
	}
	if params.SessionID != "s1" {
		t.Fatalf("status params = %+v, want session s1", params)
	}

	a.send(t, statusFrame(t, "s1", acpwire.SessionStatus{WorkingDir: "A"}))
	a.answer(t, req, acpwire.SessionStatus{WorkingDir: "B"})

	res := awaitAttach(t, out)
	if res.err != nil {
		t.Fatalf("Attach: %v", res.err)
	}
	if got := res.session.Status().WorkingDir; got != "A" {
		t.Fatalf("Status().WorkingDir = %q, want A: the update that preceded the response is newer", got)
	}
}

func TestAttachInstallsTheResultWhenNoUpdateCame(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})

	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	if got := session.ID(); got != "s1" {
		t.Fatalf("ID() = %q, want s1", got)
	}
	if got := session.Status().WorkingDir; got != "B" {
		t.Fatalf("Status().WorkingDir = %q, want B", got)
	}
}

func TestAnUpdateReplacesTheStatusAndFiresChanged(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	changed := session.Changed()
	drainChanged(t, changed)

	a.send(t, statusFrame(t, "s1", acpwire.SessionStatus{WorkingDir: "C"}))
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not fire for the update")
	}
	if got := session.Status().WorkingDir; got != "C" {
		t.Fatalf("Status().WorkingDir = %q, want C", got)
	}
}

func TestAnUpdateForAnotherSessionIsDropped(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	changed := session.Changed()
	drainChanged(t, changed)

	a.send(t, statusFrame(t, "other", acpwire.SessionStatus{WorkingDir: "C"}))
	// The ping proves the read goroutine dispatched the update already.
	pingScripted(t, host, a)

	if got := session.Status().WorkingDir; got != "B" {
		t.Fatalf("Status().WorkingDir = %q, want B: an update for another session must be dropped", got)
	}
	select {
	case <-changed:
		t.Fatal("Changed fired for another session's update")
	default:
	}
}

func TestTheCallersNotifySeesEveryNotification(t *testing.T) {
	notified := make(chan string, 4)
	host, a := dialScripted(t, DialOptions{Handlers: Handlers{
		Notify: func(method string, params json.RawMessage) { notified <- method },
	}})

	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	a.send(t, statusFrame(t, "s1", acpwire.SessionStatus{WorkingDir: "C"}))
	a.send(t, `{"jsonrpc":"2.0","method":"test/unrelated","params":{"value":1}}`)

	// The routed update reached the mirror, and the caller's Notify sees both
	// notifications, in arrival order.
	select {
	case <-session.Changed():
	case <-time.After(2 * time.Second):
		t.Fatal("the status update did not reach the mirror")
	}
	for _, want := range []string{acpwire.MethodSessionUpdate, "test/unrelated"} {
		select {
		case got := <-notified:
			if got != want {
				t.Fatalf("Notify saw %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Notify never saw %q", want)
		}
	}
}

func TestAttachingTwiceSendsOneCall(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	first := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	second, err := host.Attach(context.Background(), "s1")
	if err != nil {
		t.Fatalf("the second Attach: %v", err)
	}
	if second != first {
		t.Fatal("the second Attach returned a different Session")
	}
	if got := second.Status().WorkingDir; got != "B" {
		t.Fatalf("Status().WorkingDir = %q, want B", got)
	}

	// Nothing went out: the agent has no second session/status to answer.
	select {
	case req := <-a.reqs:
		t.Fatalf("the second Attach sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestAConcurrentAttachWaitsForTheFirst pins Attach's contract for a second
// caller: it gets the first caller's value, not a mirror the agent has not
// filled yet.
func TestAConcurrentAttachWaitsForTheFirst(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})

	first := startAttach(host, "s1")
	req := a.next(t)

	// The second caller arrives while the first call is still in flight.
	second := startAttach(host, "s1")
	select {
	case res := <-second:
		t.Fatalf("the second Attach returned %+v before the first settled", res)
	case <-time.After(100 * time.Millisecond):
	}

	a.answer(t, req, acpwire.SessionStatus{WorkingDir: "B"})

	res := awaitAttach(t, first)
	if res.err != nil {
		t.Fatalf("the first Attach: %v", res.err)
	}
	other := awaitAttach(t, second)
	if other.err != nil {
		t.Fatalf("the second Attach: %v", other.err)
	}
	if other.session != res.session {
		t.Fatal("the second Attach returned a different Session")
	}
	if got := other.session.Status().WorkingDir; got != "B" {
		t.Fatalf("the second Attach's Status().WorkingDir = %q, want B", got)
	}
}

// TestAFailedAttachReportsItsErrorToAConcurrentCaller pins that a refusal is
// shared rather than hidden: the waiting caller gets the error, and a later
// Attach retries instead of inheriting it.
func TestAFailedAttachReportsItsErrorToAConcurrentCaller(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})

	first := startAttach(host, "missing")
	req := a.next(t)
	second := startAttach(host, "missing")
	select {
	case res := <-second:
		t.Fatalf("the second Attach returned %+v before the first settled", res)
	case <-time.After(100 * time.Millisecond):
	}

	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "unknown session",
	}))

	res := awaitAttach(t, first)
	if !acpclient.IsCode(res.err, acpwire.CodeSessionClosed) {
		t.Fatalf("the first Attach error = %v, want code %q", res.err, acpwire.CodeSessionClosed)
	}
	other := awaitAttach(t, second)
	if !acpclient.IsCode(other.err, acpwire.CodeSessionClosed) {
		t.Fatalf("the second Attach error = %v, want code %q", other.err, acpwire.CodeSessionClosed)
	}
	if other.session != nil {
		t.Fatalf("the second Attach returned a session alongside its error")
	}

	// The entry is gone, so a later Attach retries: the agent sees a second
	// session/status for the same id.
	retry := startAttach(host, "missing")
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodStatus {
		t.Fatalf("the retry sent %q, want %q", got, acpwire.MethodStatus)
	}
	a.answer(t, req, acpwire.SessionStatus{WorkingDir: "B"})
	if res := awaitAttach(t, retry); res.err != nil {
		t.Fatalf("the retried Attach: %v", res.err)
	}
}

// TestAttachAfterTheConnectionEndedFails pins a dead connection's uniform
// behavior: an id that was attached fails like one that was not, instead of
// returning a closed session as if the attach had succeeded.
func TestAttachAfterTheConnectionEndedFails(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Changed closes after the attached session is unregistered.
	select {
	case <-session.Changed():
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not close when the connection ended")
	}

	got, err := host.Attach(context.Background(), "s1")
	if err == nil {
		t.Fatalf("Attach after the connection ended returned %v, want an error", got)
	}
}

func TestChangedClosesWhenTheConnectionEnds(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case _, ok := <-session.Changed():
		if ok {
			t.Fatal("Changed fired instead of closing when the connection ended")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not close when the connection ended")
	}
}

func TestNoticeCallsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := make(chan error, 1)
	notice := acpwire.Notice{Category: acpwire.NoticeProvider, Severity: acpwire.NoticeError, Message: "boom"}
	go func() { out <- session.SetNotice(context.Background(), notice) }()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodNoticeSet {
		t.Fatalf("SetNotice sent %q, want %q", got, acpwire.MethodNoticeSet)
	}
	var setParams acpwire.NoticeSetParams
	if err := json.Unmarshal(req.Params, &setParams); err != nil {
		t.Fatalf("decode notice_set params: %v", err)
	}
	if setParams.SessionID != "s1" || setParams.Notice != notice {
		t.Fatalf("notice_set params = %+v, want session s1 and %+v", setParams, notice)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("SetNotice: %v", err)
	}

	go func() { out <- session.ClearNotice(context.Background(), acpwire.NoticeProvider) }()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodNoticeClear {
		t.Fatalf("ClearNotice sent %q, want %q", got, acpwire.MethodNoticeClear)
	}
	var clearParams acpwire.NoticeClearParams
	if err := json.Unmarshal(req.Params, &clearParams); err != nil {
		t.Fatalf("decode notice_clear params: %v", err)
	}
	if clearParams.SessionID != "s1" || clearParams.Category != acpwire.NoticeProvider {
		t.Fatalf("notice_clear params = %+v, want session s1 and category %q", clearParams, acpwire.NoticeProvider)
	}
	// A coded error comes back as the *acpwire.Error the connection maps it to.
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "the session is gone",
	}))
	err := awaitCall(t, out)
	if !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("ClearNotice error = %v, want an error with code %q", err, acpwire.CodeSessionClosed)
	}

	go func() { out <- session.DismissNotice(context.Background()) }()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodNoticeDismiss {
		t.Fatalf("DismissNotice sent %q, want %q", got, acpwire.MethodNoticeDismiss)
	}
	var dismissParams acpwire.StatusParams
	if err := json.Unmarshal(req.Params, &dismissParams); err != nil {
		t.Fatalf("decode notice_dismiss params: %v", err)
	}
	if dismissParams.SessionID != "s1" {
		t.Fatalf("notice_dismiss params = %+v, want session s1", dismissParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("DismissNotice: %v", err)
	}
}

// TestAFailedAttachLeavesNoCurrentSession pins that the current session moves
// only when an Attach succeeds. The entry exists the moment the first Attach
// registers it, so a caller that waits on a call that then fails must not leave
// host calls naming a session the agent refused (decision 1).
func TestAFailedAttachLeavesNoCurrentSession(t *testing.T) {
	host, _ := dialScripted(t, DialOptions{})

	// The entry a first Attach registers before its call settles, settled with
	// the failure: exactly what a concurrent caller waits on.
	failed := &Session{
		id:      "s1",
		conn:    host.conn,
		stacks:  host.stacks,
		changed: make(chan struct{}, 1),
		ready:   make(chan struct{}),
		sheets:  make(map[int64]*sheetEntry),
	}
	failed.settle(errors.New("session/status: boom"))
	host.mu.Lock()
	host.sessions["s1"] = failed
	host.mu.Unlock()

	if _, err := host.Attach(context.Background(), "s1"); err == nil {
		t.Fatal("Attach on a session whose call failed succeeded")
	}
	if id, err := host.currentID(); !errors.Is(err, errNoSession) {
		t.Fatalf("currentID() = %q, %v after a failed Attach, want %v", id, err, errNoSession)
	}
}

// TestClosingTheConnectionClearsTheCurrentSession pins that a host call after
// the connection ends asks for a session instead of naming a dead one.
func TestClosingTheConnectionClearsTheCurrentSession(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	if _, err := host.currentID(); err != nil {
		t.Fatalf("currentID() = %v after an Attach, want a session", err)
	}

	_ = host.Close()
	host.closeSessions()
	if id, err := host.currentID(); !errors.Is(err, errNoSession) {
		t.Fatalf("currentID() = %q, %v after the connection ended, want %v", id, err, errNoSession)
	}
}
