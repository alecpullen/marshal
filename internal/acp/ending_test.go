package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
)

// endFake is the service package's fakeEndService, copied here so these handler
// tests drive the end flow without that package's tests. setWork changes the
// membership the fake reports, which is what an end forwarder follows.
type endFake struct {
	mu       sync.Mutex
	work     acpwire.EndWork
	decision acpwire.EndDecision
	params   []acpwire.EndAcceptParams
	goals    []string
	waits    int
	// emptyOnAccept makes AcceptEnd empty the membership before it answers, as
	// an acceptance that stops the work does.
	emptyOnAccept bool
}

func (f *endFake) ActiveWork() acpwire.EndWork {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.work
}

// setWork replaces the membership the fake reports.
func (f *endFake) setWork(work acpwire.EndWork) {
	f.mu.Lock()
	f.work = work
	f.mu.Unlock()
}

// setEmptyOnAccept makes the next acceptance stop the work before it answers.
func (f *endFake) setEmptyOnAccept(empty bool) {
	f.mu.Lock()
	f.emptyOnAccept = empty
	f.mu.Unlock()
}

func (f *endFake) AcceptEnd(params acpwire.EndAcceptParams) (acpwire.EndDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.params = append(f.params, params)
	if f.emptyOnAccept {
		f.work = acpwire.EndWork{}
	}
	return f.decision, nil
}

func (f *endFake) WaitForEnd(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits++
	return nil
}

func (f *endFake) RunExitAnalysis(ctx context.Context, goal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goals = append(f.goals, goal)
	return nil
}

// accepts returns the acceptance parameters the fake was given, in order.
func (f *endFake) accepts() []acpwire.EndAcceptParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]acpwire.EndAcceptParams(nil), f.params...)
}

// analysisGoals returns the goals RunExitAnalysis was given, in order.
func (f *endFake) analysisGoals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.goals...)
}

// waitCount returns the number of WaitForEnd calls.
func (f *endFake) waitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waits
}

// endSetup is what one end-flow test serves.
type endSetup struct {
	// marshal says whether the client declares the marshal capability.
	marshal bool
	deps    service.Deps
	// others is what registerEnds routes a client without the capability to.
	others map[string]Handler
	// delay slows every session/update frame, so a call that returns before its
	// update is written shows up as an ordering failure.
	delay time.Duration
}

// endAgent is the agent end of an end-flow test: the control agent's frame
// recorder over the connection's end and status managers.
type endAgent struct {
	*controlAgent
	ends *EndManager
	// resolve is the connection's Lookup, so a test can check the session is
	// still registered after a close.
	resolve func(sessionID string) (*service.Session, bool)
}

// endUpdates returns the end_work updates recorded for the session, in arrival
// order.
func (a *endAgent) endUpdates() []acpwire.EndWorkUpdate {
	var out []acpwire.EndWorkUpdate
	for _, f := range a.recorded() {
		if f.SessionID != controlSession {
			continue
		}
		var update acpwire.EndWorkUpdate
		if err := json.Unmarshal(f.Update, &update); err != nil {
			continue
		}
		if update.Kind == acpwire.UpdateEndWork {
			out = append(out, update)
		}
	}
	return out
}

// forwarderGone reports whether the manager holds no end forwarder and no fence
// for the session.
func (a *endAgent) forwarderGone(sessionID string) bool {
	a.ends.mu.Lock()
	_, live := a.ends.forwarders[sessionID]
	a.ends.mu.Unlock()
	if live {
		return false
	}
	a.ends.fences.mu.Lock()
	defer a.ends.fences.mu.Unlock()
	return len(a.ends.fences.fences[sessionID]) == 0
}

// startEndAgent serves the end-flow handlers and the status handlers on the
// agent end of a pipe, as startControlAgent serves the control manager, and
// returns a client connection that has completed initialize.
func startEndAgent(t *testing.T, setup endSetup) (*rpc.Conn, *endAgent) {
	t.Helper()

	host, served := service.New(setup.deps)
	a := &endAgent{controlAgent: &controlAgent{state: setup.deps.State, served: served}}
	a.lookup.Store(true)

	client, agent := rpc.Pipe()
	srv := NewServer(agent, agent, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	// The agent's end sets its own cap; neither end inherits the default (P3 R2).
	srv.MaxLineBytes = rpc.MaxFrameBytes
	registerInitialize(srv)

	notify := func(method string, params any) error {
		if setup.delay > 0 && method == acpwire.MethodSessionUpdate {
			time.Sleep(setup.delay)
		}
		return srv.Notify(method, params)
	}
	lookup := func(id string) (*service.Session, bool) {
		if id != controlSession || !a.lookup.Load() {
			return nil, false
		}
		return served, true
	}
	hostLookup := func(id string) (*service.Host, bool) {
		if id != controlSession || !a.lookup.Load() {
			return nil, false
		}
		return host, true
	}
	a.resolve = lookup

	a.statuses = NewStatusManager(StatusManagerConfig{
		Lookup:  lookup,
		Notify:  notify,
		Marshal: srv.marshalClient,
	})
	registerStatus(srv, a.statuses)
	a.ends = NewEndManager(EndManagerConfig{
		Lookup:     lookup,
		HostLookup: hostLookup,
		Notify:     notify,
		Marshal:    srv.marshalClient,
	})
	registerEnds(srv, a.ends, setup.others)
	srv.SetResponseBarrier(responseBarrier(a.statuses, a.ends))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()

	conn := rpc.NewConn(client, rpc.Handlers{Notify: a.notifyClient})

	t.Cleanup(func() {
		_ = conn.Close()
		a.ends.Close()
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
	if setup.marshal {
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

// endDB opens a migrated database in a temporary directory, as the service
// package's browserDB does.
func endDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

// TestTheFirstEndWorkArrivesBeforeTheResponse pins that session/end_work's
// first call starts the forwarder and that the barrier makes its update precede
// the response (P3 R2, decision 7).
func TestTheFirstEndWorkArrivesBeforeTheResponse(t *testing.T) {
	work := acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "op-1", Label: "agent turn"}}}
	fake := &endFake{work: work}
	conn, a := startEndAgent(t, endSetup{
		marshal: true,
		deps:    service.Deps{State: seamState(t, controlSession), End: fake},
	})

	var res acpwire.EndWork
	if err := conn.Call(context.Background(), acpwire.MethodEndWork, acpwire.ControlParams{
		SessionID: controlSession,
	}, &res); err != nil {
		t.Fatalf("session/end_work: %v", err)
	}

	// The call has returned, so its update must already be recorded, and be the
	// only one: nothing has changed since.
	updates := a.endUpdates()
	if len(updates) != 1 {
		t.Fatalf("recorded %d end_work updates before the response, want exactly 1", len(updates))
	}
	if !reflect.DeepEqual(updates[0].Work, work) {
		t.Errorf("the update carries %+v, want the service's %+v", updates[0].Work, work)
	}
	if !reflect.DeepEqual(res, work) {
		t.Errorf("session/end_work = %+v, want the service's %+v", res, work)
	}
}

// TestANewMembershipIsSentWithinTheInterval pins that the forwarder follows the
// membership by polling, and that an unchanged membership costs no update.
func TestANewMembershipIsSentWithinTheInterval(t *testing.T) {
	fake := &endFake{work: acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "op-1"}}}}
	conn, a := startEndAgent(t, endSetup{
		marshal: true,
		deps:    service.Deps{State: seamState(t, controlSession), End: fake},
	})

	// The first read starts the forwarder and installs the mirror.
	var res acpwire.EndWork
	if err := conn.Call(context.Background(), acpwire.MethodEndWork, acpwire.ControlParams{
		SessionID: controlSession,
	}, &res); err != nil {
		t.Fatalf("session/end_work: %v", err)
	}

	next := acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "cmd-2", Label: "queued command"}}}
	fake.setWork(next)

	waitForFrame(t, 500*time.Millisecond, "the new membership's update", func() bool {
		for _, update := range a.endUpdates() {
			if reflect.DeepEqual(update.Work, next) {
				return true
			}
		}
		return false
	})

	// Three quiet intervals send nothing: the encoding has not changed.
	before := len(a.endUpdates())
	time.Sleep(3 * endInterval)
	if after := len(a.endUpdates()); after != before {
		t.Errorf("recorded %d end_work updates over three quiet intervals, want %d", after, before)
	}
}

// TestAnAcceptanceReturnsAfterItsUpdate pins decision 7's ordering for the rest
// of the end flow: a marshal client's session/end_accept returns only after the
// update its acceptance caused is written (P3 R2, R3).
func TestAnAcceptanceReturnsAfterItsUpdate(t *testing.T) {
	fake := &endFake{
		work:     acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "op-1", Label: "agent turn"}}},
		decision: acpwire.EndDecision{Accepted: true},
	}
	// Every session/update frame is slowed, so a call that returns before its
	// update is written shows up as an ordering failure.
	conn, a := startEndAgent(t, endSetup{
		marshal: true,
		delay:   50 * time.Millisecond,
		deps:    service.Deps{State: seamState(t, controlSession), End: fake},
	})

	var res acpwire.EndWork
	if err := conn.Call(context.Background(), acpwire.MethodEndWork, acpwire.ControlParams{
		SessionID: controlSession,
	}, &res); err != nil {
		t.Fatalf("session/end_work: %v", err)
	}
	if got := len(a.endUpdates()); got != 1 {
		t.Fatalf("recorded %d end_work updates before the acceptance, want 1", got)
	}

	// The acceptance stops the work before it answers.
	fake.setEmptyOnAccept(true)

	var decision acpwire.EndDecision
	if err := conn.Call(context.Background(), acpwire.MethodEndAccept, acpwire.EndAcceptCallParams{
		SessionID: controlSession,
		Accept:    acpwire.EndAcceptParams{Intent: acpwire.EndIntentExit},
	}, &decision); err != nil {
		t.Fatalf("session/end_accept: %v", err)
	}

	updates := a.endUpdates()
	if len(updates) < 2 {
		t.Fatalf("recorded %d end_work updates, want the acceptance's empty membership too", len(updates))
	}
	last := updates[len(updates)-1]
	if len(last.Work.Items) != 0 {
		t.Errorf("the last update before the response carries %+v, want the empty membership", last.Work)
	}
}

// TestTheEndCallsReachTheService pins that the four forwarding calls reach the
// session's service unchanged: the acceptance's params and decision, the wait,
// the exit analysis's goal, and the postmortem's path.
func TestTheEndCallsReachTheService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// testenv blanks these at package start; set them again so the report lands
	// under the HOME this test chose.
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	work := acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "op-1"}}}
	decision := acpwire.EndDecision{Expanded: []string{"cmd-2"}, Work: work}
	fake := &endFake{work: work, decision: decision}
	conn, _ := startEndAgent(t, endSetup{
		marshal: true,
		deps:    service.Deps{State: seamState(t, controlSession), End: fake},
	})

	params := acpwire.EndAcceptParams{
		Intent:           acpwire.EndIntentExit,
		DisplayedIDs:     []string{"op-1"},
		RequireDisplayed: true,
	}
	var got acpwire.EndDecision
	if err := conn.Call(context.Background(), acpwire.MethodEndAccept, acpwire.EndAcceptCallParams{
		SessionID: controlSession,
		Accept:    params,
	}, &got); err != nil {
		t.Fatalf("session/end_accept: %v", err)
	}
	if !reflect.DeepEqual(got, decision) {
		t.Errorf("session/end_accept = %+v, want the service's %+v", got, decision)
	}
	if accepts := fake.accepts(); !reflect.DeepEqual(accepts, []acpwire.EndAcceptParams{params}) {
		t.Errorf("the service was given %+v, want the call's params unchanged", accepts)
	}

	var raw json.RawMessage
	if err := conn.Call(context.Background(), acpwire.MethodEndWait, acpwire.ControlParams{
		SessionID: controlSession,
	}, &raw); err != nil {
		t.Fatalf("session/end_wait: %v", err)
	}
	if got := fake.waitCount(); got != 1 {
		t.Errorf("WaitForEnd calls = %d, want 1", got)
	}

	if err := conn.Call(context.Background(), acpwire.MethodExitAnalysis, acpwire.ExitAnalysisParams{
		SessionID: controlSession,
		Goal:      "explain the exit",
	}, &raw); err != nil {
		t.Fatalf("session/exit_analysis: %v", err)
	}
	if goals := fake.analysisGoals(); !reflect.DeepEqual(goals, []string{"explain the exit"}) {
		t.Errorf("RunExitAnalysis goals = %v, want the call's goal", goals)
	}

	var pm acpwire.PostmortemResult
	if err := conn.Call(context.Background(), acpwire.MethodPostmortem, acpwire.ControlParams{
		SessionID: controlSession,
	}, &pm); err != nil {
		t.Fatalf("session/postmortem: %v", err)
	}
	if !strings.HasPrefix(pm.Path, home) {
		t.Errorf("postmortem path = %q, want it under %q", pm.Path, home)
	}
}

// TestAMarshalCloseEndsTheConversationAndKeepsTheRuntime pins decision 4: a
// marshal client's session/close is served by the session's service, so the
// conversation ends and the runtime stays registered.
func TestAMarshalCloseEndsTheConversationAndKeepsTheRuntime(t *testing.T) {
	state := seamState(t, controlSession)
	var reached atomic.Int64
	others := map[string]Handler{
		acpwire.MethodSessionClose: func(ctx context.Context, params json.RawMessage) (any, error) {
			reached.Add(1)
			return map[string]any{}, nil
		},
	}
	conn, a := startEndAgent(t, endSetup{
		marshal: true,
		deps:    service.Deps{State: state, End: &endFake{}},
		others:  others,
	})

	responses := testApprovalChan()
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call-1",
		Name:         "shell.run",
		Command:      "rm -rf build",
		ResponseChan: responses,
	})

	var res map[string]any
	if err := conn.Call(context.Background(), acpwire.MethodSessionClose, acpwire.ControlParams{
		SessionID: controlSession,
	}, &res); err != nil {
		t.Fatalf("session/close: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("session/close = %v, want an empty object", res)
	}
	if got := reached.Load(); got != 0 {
		t.Errorf("the session manager's close ran %d times, want it never to run", got)
	}

	select {
	case d := <-responses:
		if d.Approved {
			t.Error("the pending approval was approved, want a denial")
		}
	default:
		t.Error("the pending approval was not settled by the close")
	}

	select {
	case <-state.Done():
	default:
		t.Error("the state is not shut down after the close")
	}

	// Nothing was unregistered: the runtime is still there for the embedding or
	// the host's CloseAll.
	if _, ok := a.resolve(controlSession); !ok {
		t.Error("the close unregistered the session, want the runtime kept")
	}
}

// TestACloseWithoutTheCapabilityReachesTheSessionManager pins R9's compatibility
// arm for the two moved methods: a client without the capability reaches the
// session manager's handlers (V6).
func TestACloseWithoutTheCapabilityReachesTheSessionManager(t *testing.T) {
	var closed, listed atomic.Int64
	others := map[string]Handler{
		acpwire.MethodSessionClose: func(ctx context.Context, params json.RawMessage) (any, error) {
			closed.Add(1)
			return map[string]any{}, nil
		},
		acpwire.MethodSessionList: func(ctx context.Context, params json.RawMessage) (any, error) {
			listed.Add(1)
			return map[string]any{"sessions": []any{}}, nil
		},
	}
	conn, a := startEndAgent(t, endSetup{
		marshal: false,
		deps:    service.Deps{State: seamState(t, controlSession)},
		others:  others,
	})

	var raw json.RawMessage
	if err := conn.Call(context.Background(), acpwire.MethodSessionClose, acpwire.ControlParams{
		SessionID: controlSession,
	}, &raw); err != nil {
		t.Fatalf("session/close: %v", err)
	}
	if err := conn.Call(context.Background(), acpwire.MethodSessionList, acpwire.ListSessionsParams{
		SessionID: controlSession,
		Cwd:       "/repo",
	}, &raw); err != nil {
		t.Fatalf("session/list: %v", err)
	}
	if got := closed.Load(); got != 1 {
		t.Errorf("the session manager's close ran %d times, want 1", got)
	}
	if got := listed.Load(); got != 1 {
		t.Errorf("the session manager's list ran %d times, want 1", got)
	}
	select {
	case <-a.state.Done():
		t.Error("the state was shut down for a client without the capability")
	default:
	}
}

// TestAMarshalListNamesItsCurrentSession pins that a marshal client's
// session/list is served by the service host of the session it names, over that
// session's database.
func TestAMarshalListNamesItsCurrentSession(t *testing.T) {
	database := endDB(t)
	cwd := "/repo"
	projectID, err := database.GetOrCreateProject(cwd, "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession("s1", projectID, "first session", time.Unix(1000, 0)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	state := session.New(config.Default(), cwd, time.Unix(500, 0),
		session.Persistence{DB: database, SessionID: "s1"})

	conn, _ := startEndAgent(t, endSetup{
		marshal: true,
		deps:    service.Deps{State: state, ProjectID: projectID},
	})

	var res acpwire.ListSessionsResult
	if err := conn.Call(context.Background(), acpwire.MethodSessionList, acpwire.ListSessionsParams{
		SessionID: controlSession,
		Cwd:       cwd,
		Limit:     10,
	}, &res); err != nil {
		t.Fatalf("session/list: %v", err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("session/list = %+v, want the one stored session", res.Sessions)
	}
	entry := res.Sessions[0]
	if entry.SessionID != "s1" {
		t.Errorf("sessionId = %q, want s1", entry.SessionID)
	}
	if entry.Title != "first session" {
		t.Errorf("title = %q, want %q", entry.Title, "first session")
	}
	// updatedAt travels as Unix milliseconds, not seconds or a formatted time.
	if want := time.Unix(1000, 0).UnixMilli(); entry.UpdatedAt != want {
		t.Errorf("updatedAt = %d, want %d (Unix milliseconds)", entry.UpdatedAt, want)
	}

	// A session with no database has no rows to list.
	bare, _ := startEndAgent(t, endSetup{
		marshal: true,
		deps:    service.Deps{State: seamState(t, controlSession)},
	})
	var missing acpwire.ListSessionsResult
	err = bare.Call(context.Background(), acpwire.MethodSessionList, acpwire.ListSessionsParams{
		SessionID: controlSession,
		Cwd:       cwd,
	}, &missing)
	if !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Fatalf("session/list with no database = %v, want CodeNoDatabase", err)
	}
}

// TestAClientWithoutTheCapabilityGetsNoEndWork pins V6: a client without the
// capability reads the membership from its answer and is never sent an
// end_work update, because no forwarder is started for it.
func TestAClientWithoutTheCapabilityGetsNoEndWork(t *testing.T) {
	work := acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "op-1"}}}
	fake := &endFake{work: work}
	conn, a := startEndAgent(t, endSetup{
		marshal: false,
		deps:    service.Deps{State: seamState(t, controlSession), End: fake},
	})

	var res acpwire.EndWork
	if err := conn.Call(context.Background(), acpwire.MethodEndWork, acpwire.ControlParams{
		SessionID: controlSession,
	}, &res); err != nil {
		t.Fatalf("session/end_work: %v", err)
	}
	if !reflect.DeepEqual(res, work) {
		t.Errorf("session/end_work = %+v, want the service's %+v", res, work)
	}

	time.Sleep(200 * time.Millisecond)
	if updates := a.endUpdates(); len(updates) != 0 {
		t.Errorf("recorded %d end_work updates for a client without the capability, want none", len(updates))
	}
}

// TestTheEndForwarderStopsWhenTheSessionGoes pins that a forwarder follows its
// lookup off the air: a session that goes away takes the forwarder and its
// fence with it.
func TestTheEndForwarderStopsWhenTheSessionGoes(t *testing.T) {
	fake := &endFake{work: acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "op-1"}}}}
	conn, a := startEndAgent(t, endSetup{
		marshal: true,
		deps:    service.Deps{State: seamState(t, controlSession), End: fake},
	})

	var res acpwire.EndWork
	if err := conn.Call(context.Background(), acpwire.MethodEndWork, acpwire.ControlParams{
		SessionID: controlSession,
	}, &res); err != nil {
		t.Fatalf("session/end_work: %v", err)
	}
	if a.forwarderGone(controlSession) {
		t.Fatal("the forwarder is not running after the first read")
	}

	a.lookup.Store(false)

	waitForFrame(t, 500*time.Millisecond, "the end forwarder to stop", func() bool {
		return a.forwarderGone(controlSession)
	})
}

// TestTheEndCallsRefuseBadParams pins the two refusals every end call shares: a
// missing session id is invalid params, and an unknown one is the
// unknown-session error.
func TestTheEndCallsRefuseBadParams(t *testing.T) {
	conn, _ := startEndAgent(t, endSetup{
		marshal: true,
		deps:    service.Deps{State: seamState(t, controlSession), End: &endFake{}},
	})

	methods := []struct {
		name   string
		method string
	}{
		{"end_work", acpwire.MethodEndWork},
		{"end_accept", acpwire.MethodEndAccept},
		{"end_wait", acpwire.MethodEndWait},
		{"exit_analysis", acpwire.MethodExitAnalysis},
		{"postmortem", acpwire.MethodPostmortem},
		{"close", acpwire.MethodSessionClose},
		{"list", acpwire.MethodSessionList},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var res json.RawMessage
			err := conn.Call(context.Background(), m.method, json.RawMessage(`{}`), &res)
			var rpcErr *rpc.Error
			if err == nil || !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
				t.Fatalf("err = %v, want the invalid-params refusal", err)
			}
			err = conn.Call(context.Background(), m.method, json.RawMessage(`{"sessionId":"nope"}`), &res)
			if err == nil || !strings.Contains(err.Error(), "unknown session") {
				t.Fatalf("err = %v, want the unknown-session refusal", err)
			}
		})
	}
}
