package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// statusSession is the session every status test serves.
const statusSession = "s1"

// statusAgent is the agent end of a status test: a service over one state, the
// connection's status manager, and the updates the client recorded.
type statusAgent struct {
	statuses *StatusManager
	state    *session.State

	// lookup is what the status manager's Lookup reports for the session.
	lookup atomic.Bool

	mu      sync.Mutex
	updates []acpwire.StatusUpdate
}

// record keeps one received session_status update.
func (a *statusAgent) record(u acpwire.StatusUpdate) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updates = append(a.updates, u)
}

// recorded returns the updates received so far, in arrival order.
func (a *statusAgent) recorded() []acpwire.StatusUpdate {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]acpwire.StatusUpdate, len(a.updates))
	copy(out, a.updates)
	return out
}

// count is how many updates the client has received.
func (a *statusAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.updates)
}

// notifyClient decodes a notification the agent sent, keeping each
// session_status update.
func (a *statusAgent) notifyClient(method string, params json.RawMessage) {
	if method != acpwire.MethodSessionUpdate {
		return
	}
	var env struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &env); err != nil {
		return
	}
	var peek struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(env.Update, &peek); err != nil {
		return
	}
	if peek.Kind != acpwire.UpdateSessionStatus {
		return
	}
	var u acpwire.StatusUpdate
	if err := json.Unmarshal(env.Update, &u); err != nil {
		return
	}
	a.record(u)
}

// startStatusAgent serves the status handlers on the agent end of a pipe and
// returns a client connection that has completed initialize. A nonzero delay
// slows every session/update frame, so a call that returns before its update is
// written shows up as an ordering failure.
func startStatusAgent(t *testing.T, delay time.Duration, marshal bool) (*rpc.Conn, *statusAgent) {
	t.Helper()

	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: statusSession})
	_, sess := service.New(service.Deps{State: state})
	a := &statusAgent{state: state}
	a.lookup.Store(true)

	client, agent := rpc.Pipe()
	srv := NewServer(agent, agent, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	// The agent's end sets its own cap; neither end inherits the default (P3 R2).
	srv.MaxLineBytes = rpc.MaxFrameBytes

	notify := func(method string, params any) error {
		if delay > 0 && method == acpwire.MethodSessionUpdate {
			time.Sleep(delay)
		}
		return srv.Notify(method, params)
	}
	registerInitialize(srv)
	a.statuses = NewStatusManager(StatusManagerConfig{
		Lookup: func(id string) (*service.Session, bool) {
			if id != statusSession || !a.lookup.Load() {
				return nil, false
			}
			return sess, true
		},
		Notify:  notify,
		Marshal: srv.marshalClient,
	})
	registerStatus(srv, a.statuses)
	srv.SetResponseBarrier(responseBarrier(nil, nil, nil, a.statuses))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()

	conn := rpc.NewConn(client, rpc.Handlers{Notify: a.notifyClient})

	t.Cleanup(func() {
		_ = conn.Close()
		a.statuses.Close()
		cancel()
		_ = agent.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})

	p := acpwire.InitializeParams{ProtocolVersion: acpwire.MarshalProtocolVersion}
	if marshal {
		p.ClientCapabilities = &acpwire.ClientCapabilities{
			Marshal: &acpwire.MarshalCapability{ProtocolVersion: acpwire.MarshalProtocolVersion},
		}
	}
	var res acpwire.InitializeResult
	if err := conn.Call(ctx, "initialize", p, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return conn, a
}

// readStatus calls session/status and returns its result.
func readStatus(t *testing.T, conn *rpc.Conn) acpwire.SessionStatus {
	t.Helper()
	var got acpwire.SessionStatus
	if err := conn.Call(context.Background(), acpwire.MethodStatus, acpwire.StatusParams{SessionID: statusSession}, &got); err != nil {
		t.Fatalf("session/status: %v", err)
	}
	return got
}

// waitForStatus polls cond until it reports true or timeout expires.
func waitForStatus(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// lastUpdate returns the last update the client received.
func lastUpdate(t *testing.T, a *statusAgent) acpwire.StatusUpdate {
	t.Helper()
	updates := a.recorded()
	if len(updates) == 0 {
		t.Fatal("the client received no session_status update")
	}
	return updates[len(updates)-1]
}

// TestTheFirstStatusArrivesBeforeTheResponse pins R2 for the first read: the
// call returns only after the update carrying the same status is written.
func TestTheFirstStatusArrivesBeforeTheResponse(t *testing.T) {
	conn, a := startStatusAgent(t, 0, true)

	got := readStatus(t, conn)
	if n := a.count(); n != 1 {
		t.Fatalf("updates at return = %d, want exactly 1", n)
	}
	if last := lastUpdate(t, a); !reflect.DeepEqual(last.Status, got) {
		t.Fatalf("the update's status = %+v, want the result %+v", last.Status, got)
	}
}

// TestAStatusChangeIsSentWithinTheInterval pins the mirror's freshness: a change
// nothing published reaches the client within the interval, and an unchanged
// status sends nothing.
func TestAStatusChangeIsSentWithinTheInterval(t *testing.T) {
	conn, a := startStatusAgent(t, 0, true)
	readStatus(t, conn)

	a.state.SetNotice(session.Notice{
		Category: session.NoticeTool,
		Severity: session.SeverityError,
		Message:  "boom",
	})
	waitForStatus(t, 500*time.Millisecond, "an update carrying the notice", func() bool {
		for _, u := range a.recorded() {
			if u.Status.Notice != nil && u.Status.Notice.Message == "boom" {
				return true
			}
		}
		return false
	})

	quiet := a.count()
	time.Sleep(3*statusInterval + 50*time.Millisecond)
	if after := a.count(); after != quiet {
		t.Fatalf("an unchanged status sent %d further updates", after-quiet)
	}
}

// TestANoticeCallReturnsAfterItsStatus pins V3: each notice call returns only
// after the update carrying its effect is written.
func TestANoticeCallReturnsAfterItsStatus(t *testing.T) {
	conn, a := startStatusAgent(t, 50*time.Millisecond, true)
	readStatus(t, conn)

	ctx := context.Background()
	var res map[string]any

	set := acpwire.NoticeSetParams{SessionID: statusSession, Notice: acpwire.Notice{
		Category: acpwire.NoticeTool,
		Severity: acpwire.NoticeError,
		Message:  "boom",
	}}
	if err := conn.Call(ctx, acpwire.MethodNoticeSet, set, &res); err != nil {
		t.Fatalf("session/notice_set: %v", err)
	}
	if last := lastUpdate(t, a); last.Status.Notice == nil || last.Status.Notice.Message != "boom" {
		t.Fatalf("the update after notice_set = %+v, want the notice", last.Status.Notice)
	}

	if err := conn.Call(ctx, acpwire.MethodNoticeClear, acpwire.NoticeClearParams{
		SessionID: statusSession,
		Category:  acpwire.NoticeTool,
	}, &res); err != nil {
		t.Fatalf("session/notice_clear: %v", err)
	}
	if last := lastUpdate(t, a); last.Status.Notice != nil {
		t.Fatalf("the update after notice_clear still carries %+v", last.Status.Notice)
	}

	if err := conn.Call(ctx, acpwire.MethodNoticeSet, set, &res); err != nil {
		t.Fatalf("session/notice_set: %v", err)
	}
	if err := conn.Call(ctx, acpwire.MethodNoticeDismiss, acpwire.StatusParams{SessionID: statusSession}, &res); err != nil {
		t.Fatalf("session/notice_dismiss: %v", err)
	}
	if last := lastUpdate(t, a); last.Status.Notice != nil {
		t.Fatalf("the update after notice_dismiss still carries %+v", last.Status.Notice)
	}
}

// TestAClientWithoutTheCapabilityGetsNoStatusUpdates pins V6: without the
// capability the status is served, the notice calls work, and no update is
// sent.
func TestAClientWithoutTheCapabilityGetsNoStatusUpdates(t *testing.T) {
	conn, a := startStatusAgent(t, 0, false)

	if got := readStatus(t, conn); got.WorkingDir == "" && got.ActiveRoute.Role == "" {
		t.Fatalf("session/status returned an empty status: %+v", got)
	}

	var res map[string]any
	if err := conn.Call(context.Background(), acpwire.MethodNoticeSet, acpwire.NoticeSetParams{
		SessionID: statusSession,
		Notice:    acpwire.Notice{Category: acpwire.NoticeTool, Severity: acpwire.NoticeError, Message: "boom"},
	}, &res); err != nil {
		t.Fatalf("session/notice_set: %v", err)
	}

	// Eight forwarder ticks: enough that a gating break would have sent several
	// updates, without making the case slower than it needs to be.
	time.Sleep(400 * time.Millisecond)
	if n := a.count(); n != 0 {
		t.Fatalf("a client without the capability received %d updates", n)
	}
}

// TestTheForwarderStopsWhenTheSessionGoes pins that a forwarder whose session
// disappears stops and drops its fence, so a later barrier answers at once.
func TestTheForwarderStopsWhenTheSessionGoes(t *testing.T) {
	conn, a := startStatusAgent(t, 0, true)
	readStatus(t, conn)

	a.lookup.Store(false)
	time.Sleep(3 * statusInterval)
	quiet := a.count()
	time.Sleep(3 * statusInterval)
	if after := a.count(); after != quiet {
		t.Fatalf("the forwarder sent %d updates after its session went", after-quiet)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	a.statuses.fenceSession(ctx, statusSession)
	// Half a second is generous for a fence that has nothing to wait for, and
	// still proves it did not run out the two-second deadline: the forwarder's
	// tick is 50 ms, so a fence that waited for one would also fail here.
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("the fence waited %v after the forwarder stopped", waited)
	}
}

// TestStatusRefusesBadParams pins the two refusals: a missing session id is
// invalid params, and an unknown session is a server error.
func TestStatusRefusesBadParams(t *testing.T) {
	conn, _ := startStatusAgent(t, 0, true)
	ctx := context.Background()
	var res any

	err := conn.Call(ctx, acpwire.MethodStatus, acpwire.StatusParams{}, &res)
	var jsonErr *rpc.Error
	if !errors.As(err, &jsonErr) || jsonErr.Code != invalidParams {
		t.Fatalf("session/status without a session id = %v, want invalid params", err)
	}

	err = conn.Call(ctx, acpwire.MethodStatus, acpwire.StatusParams{SessionID: "nope"}, &res)
	if !errors.As(err, &jsonErr) || jsonErr.Code != serverError {
		t.Fatalf("session/status for an unknown session = %v, want a server error", err)
	}

	err = conn.Call(ctx, acpwire.MethodNoticeSet, acpwire.NoticeSetParams{SessionID: "nope"}, &res)
	if !errors.As(err, &jsonErr) || jsonErr.Code != serverError {
		t.Fatalf("session/notice_set for an unknown session = %v, want a server error", err)
	}
}
