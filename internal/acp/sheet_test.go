package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// sheetSession is the session every sheet test serves.
const sheetSession = "s1"

// sheetAgent is the agent end of a sheet test: a service over one state, the
// connection's transcript and sheet managers, and the sheet updates the client
// recorded.
type sheetAgent struct {
	sheets *SheetManager
	turns  *TurnManager
	state  *session.State
	broker *pubsub.Broker[session.Event]

	mu      sync.Mutex
	updates []acpwire.SheetUpdate
}

// sheetAgentConfig is what one sheet test serves.
type sheetAgentConfig struct {
	// dir is the state's working directory. Empty means a temporary one.
	dir string
	// delay slows every session/update frame, so a call that returns before
	// its update is written shows up as an ordering failure.
	delay time.Duration
	// marshal says the client declares the marshal capability.
	marshal bool
	// finder answers the service's callers lookups. Nil means it has none.
	finder service.ReferenceFinder
}

// record keeps one received sheet update.
func (a *sheetAgent) record(u acpwire.SheetUpdate) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updates = append(a.updates, u)
}

// recorded returns the sheet updates received so far, in arrival order.
func (a *sheetAgent) recorded() []acpwire.SheetUpdate {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]acpwire.SheetUpdate, len(a.updates))
	copy(out, a.updates)
	return out
}

// countFor is how many sheet updates the client received for subagentID.
func (a *sheetAgent) countFor(subagentID int64) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, u := range a.updates {
		if u.SubagentID == subagentID {
			n++
		}
	}
	return n
}

// notifyClient decodes a notification the agent sent, keeping each sheet
// update.
func (a *sheetAgent) notifyClient(method string, params json.RawMessage) {
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
	if peek.Kind != acpwire.UpdateSheet {
		return
	}
	var u acpwire.SheetUpdate
	if err := json.Unmarshal(env.Update, &u); err != nil {
		return
	}
	a.record(u)
}

// startSheetAgent serves the transcript handlers on the agent end of a pipe and
// returns a client connection that has completed initialize. The server is
// wired as registerHandlers wires it, with the sheet manager's forwarders
// behind the response barrier.
func startSheetAgent(t *testing.T, cfg sheetAgentConfig) (*rpc.Conn, *sheetAgent) {
	t.Helper()

	dir := cfg.dir
	if dir == "" {
		dir = t.TempDir()
	}
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{SessionID: sheetSession})
	_, sess := service.New(service.Deps{State: state, ReferenceFinder: cfg.finder})
	a := &sheetAgent{state: state, broker: pubsub.NewBroker[session.Event]()}
	lookup := func(id string) (*service.Session, bool) {
		if id != sheetSession {
			return nil, false
		}
		return sess, true
	}

	client := startTestAgent(t, func(srv *Server) {
		notify := func(method string, params any) error {
			if cfg.delay > 0 && method == acpwire.MethodSessionUpdate {
				time.Sleep(cfg.delay)
			}
			return srv.Notify(method, params)
		}
		a.turns = NewTurnManager(TurnManagerConfig{
			Lookup: func(id string) (*TurnRuntime, bool) {
				return &TurnRuntime{State: state, Events: a.broker}, id == sheetSession
			},
			Notify:  notify,
			Marshal: func() bool { return true },
		})
		a.sheets = NewSheetManager(SheetManagerConfig{
			Lookup:  lookup,
			Notify:  notify,
			Marshal: srv.marshalClient,
		})
		registerInitialize(srv)
		registerTranscript(srv, a.turns, a.sheets, lookup)
		srv.SetResponseBarrier(responseBarrier(a.sheets))
	})

	conn := rpc.NewConn(client, rpc.Handlers{Notify: a.notifyClient})
	t.Cleanup(func() {
		_ = conn.Close()
		a.sheets.Close()
		a.broker.Close()
		sess.Release()
	})

	p := acpwire.InitializeParams{ProtocolVersion: acpwire.MarshalProtocolVersion}
	if cfg.marshal {
		p.ClientCapabilities = &acpwire.ClientCapabilities{
			Marshal: &acpwire.MarshalCapability{ProtocolVersion: acpwire.MarshalProtocolVersion},
		}
	}
	var res acpwire.InitializeResult
	if err := conn.Call(context.Background(), "initialize", p, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return conn, a
}

// readSheet calls session/sheet and returns its result.
func readSheet(t *testing.T, conn *rpc.Conn, subagentID int64) acpwire.SessionSheet {
	t.Helper()
	var got acpwire.SessionSheet
	if err := conn.Call(context.Background(), acpwire.MethodSheet,
		acpwire.SheetParams{SessionID: sheetSession, SubagentID: subagentID}, &got); err != nil {
		t.Fatalf("session/sheet: %v", err)
	}
	return got
}

// waitForSheet polls cond until it reports true or timeout expires.
func waitForSheet(t *testing.T, timeout time.Duration, what string, cond func() bool) {
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

// requireSheetGit skips a test that shells out to git when git is not on PATH.
func requireSheetGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// sheetRepo returns a new git repository with a.txt ("one\n") committed on
// main, built the way the service's sheetRepo builds one.
func sheetRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// sheetEditEvent is the service's editEvent: a file.write_patch of
// transcript.go with the resolved symbol renderSubagentCard at line 584,
// column 5.
func sheetEditEvent(step session.StepID) registry.AuditEvent {
	return registry.AuditEvent{
		Timestamp:    time.Unix(10, 0),
		ToolName:     "file.write_patch",
		StepID:       step,
		ToolCallID:   "c1",
		FilesChanged: []string{"transcript.go"},
		Symbols: []registry.SymbolRef{{
			File: "transcript.go", Name: "renderSubagentCard",
			Kind: "function", Line: 584, Col: 5, Resolved: true,
		}},
	}
}

// logSheetEdit logs the edit on st's open step and returns its node key.
func logSheetEdit(t *testing.T, st *session.State) string {
	t.Helper()
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	ev := sheetEditEvent(st.BeginStep(session.Actor{Model: "qwen3"}))
	st.LogToolCall(ev)
	return viewmodel.ToolKey(ev)
}

// sheetFinder answers a callers lookup, as lsp.SymbolQuery does.
type sheetFinder struct {
	refs []string
	ok   bool
}

func (f sheetFinder) References(ctx context.Context, path string, line, col int) ([]string, bool) {
	return f.refs, f.ok
}

// TestTheFirstSheetArrivesBeforeTheResponse pins R2 for the first read: the
// call returns only after the update carrying the same sheet is written.
func TestTheFirstSheetArrivesBeforeTheResponse(t *testing.T) {
	conn, a := startSheetAgent(t, sheetAgentConfig{marshal: true})

	got := readSheet(t, conn, 0)
	if n := a.countFor(0); n != 1 {
		t.Fatalf("sheet updates for subagent 0 at return = %d, want exactly 1", n)
	}
	updates := a.recorded()
	if last := updates[len(updates)-1]; !reflect.DeepEqual(last.Sheet, got) {
		t.Fatalf("the update's sheet =\n%+v\nwant the result\n%+v", last.Sheet, got)
	}
}

// TestASheetChangeIsSentWithinTheInterval pins the mirror's freshness: a change
// nothing published reaches the client within the interval, and an unchanged
// sheet sends nothing.
func TestASheetChangeIsSentWithinTheInterval(t *testing.T) {
	conn, a := startSheetAgent(t, sheetAgentConfig{marshal: true})
	readSheet(t, conn, 0)

	a.state.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "sheet-probe"})
	waitForSheet(t, 500*time.Millisecond, "an update carrying the tool call", func() bool {
		for _, u := range a.recorded() {
			for _, tool := range u.Sheet.Tools {
				if tool.Name == "sheet-probe" {
					return true
				}
			}
		}
		return false
	})

	quiet := a.countFor(0)
	time.Sleep(3*sheetInterval + 50*time.Millisecond)
	if after := a.countFor(0); after != quiet {
		t.Fatalf("an unchanged sheet sent %d further updates", after-quiet)
	}
}

// TestRefreshSheetReturnsAfterItsSheet pins V3 for the sheet: a refresh call
// returns only after the update carrying the refreshed sheet is written.
func TestRefreshSheetReturnsAfterItsSheet(t *testing.T) {
	requireSheetGit(t)
	repo := sheetRepo(t)
	conn, a := startSheetAgent(t, sheetAgentConfig{dir: repo, delay: 50 * time.Millisecond, marshal: true})

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	var got acpwire.SessionSheet
	if err := conn.Call(context.Background(), acpwire.MethodSheet, acpwire.SheetParams{
		SessionID: sheetSession,
		Refresh:   &acpwire.SheetRefresh{Rebase: true},
	}, &got); err != nil {
		t.Fatalf("session/sheet with a refresh: %v", err)
	}

	found := false
	for _, u := range a.recorded() {
		for _, f := range u.Sheet.Changed {
			if f.Path == "a.txt" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no update at return lists a.txt as changed: %+v", a.recorded())
	}
}

// TestReleasingASubagentStopsItsSheet pins that session/stack_unsubscribe drops
// the subagent's sheet subscription with its stack, and the session's own
// sheet keeps flowing.
func TestReleasingASubagentStopsItsSheet(t *testing.T) {
	conn, a := startSheetAgent(t, sheetAgentConfig{marshal: true})
	readSheet(t, conn, 0)

	child := session.New(config.Default(), t.TempDir(), time.Unix(200, 0), session.Persistence{SessionID: "child"})
	view := a.state.RegisterSubagent("scout", child)
	if got := readSheet(t, conn, view.ID); got.SubagentID != view.ID {
		t.Fatalf("the child's sheet SubagentID = %d, want %d", got.SubagentID, view.ID)
	}

	var res json.RawMessage
	if err := conn.Call(context.Background(), acpwire.MethodStackUnsubscribe,
		acpwire.StackParams{SessionID: sheetSession, SubagentID: view.ID}, &res); err != nil {
		t.Fatalf("session/stack_unsubscribe: %v", err)
	}

	before := a.countFor(view.ID)
	child.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(30, 0), ToolName: "child-probe"})
	time.Sleep(300 * time.Millisecond)
	if after := a.countFor(view.ID); after != before {
		t.Fatalf("the released subagent's sheet sent %d further updates", after-before)
	}

	// The session's own sheet is still subscribed.
	a.state.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(40, 0), ToolName: "parent-probe"})
	waitForSheet(t, 500*time.Millisecond, "an update carrying the parent's call", func() bool {
		for _, u := range a.recorded() {
			if u.SubagentID != 0 {
				continue
			}
			for _, tool := range u.Sheet.Tools {
				if tool.Name == "parent-probe" {
					return true
				}
			}
		}
		return false
	})
}

// TestAClientWithoutTheCapabilityGetsNoSheetUpdates pins V6: without the
// capability the sheet is served and no update is sent.
func TestAClientWithoutTheCapabilityGetsNoSheetUpdates(t *testing.T) {
	conn, a := startSheetAgent(t, sheetAgentConfig{marshal: false})

	if got := readSheet(t, conn, 0); got.StartedAt == 0 {
		t.Fatalf("session/sheet returned an empty sheet: %+v", got)
	}

	time.Sleep(200 * time.Millisecond)
	if n := len(a.recorded()); n != 0 {
		t.Fatalf("a client without the capability received %d sheet updates", n)
	}
}

// TestSheetRefusesBadParams pins the two refusals: a missing session id is
// invalid params, and an unknown session is a server error.
func TestSheetRefusesBadParams(t *testing.T) {
	conn, _ := startSheetAgent(t, sheetAgentConfig{marshal: true})
	ctx := context.Background()
	var res any

	err := conn.Call(ctx, acpwire.MethodSheet, acpwire.SheetParams{}, &res)
	var jsonErr *rpc.Error
	if !errors.As(err, &jsonErr) || jsonErr.Code != invalidParams {
		t.Fatalf("session/sheet without a session id = %v, want invalid params", err)
	}

	err = conn.Call(ctx, acpwire.MethodSheet, acpwire.SheetParams{SessionID: "nope"}, &res)
	if !errors.As(err, &jsonErr) || jsonErr.Code != serverError {
		t.Fatalf("session/sheet for an unknown session = %v, want a server error", err)
	}
}

// TestCallersRefusesBadParams pins the refusals: a missing session id or node
// id is invalid params, and an unknown session is a server error.
func TestCallersRefusesBadParams(t *testing.T) {
	conn, _ := startSheetAgent(t, sheetAgentConfig{marshal: true})
	ctx := context.Background()
	var res any

	err := conn.Call(ctx, acpwire.MethodCallers, acpwire.CallersParams{}, &res)
	var jsonErr *rpc.Error
	if !errors.As(err, &jsonErr) || jsonErr.Code != invalidParams {
		t.Fatalf("session/callers without a session id = %v, want invalid params", err)
	}

	err = conn.Call(ctx, acpwire.MethodCallers, acpwire.CallersParams{SessionID: sheetSession}, &res)
	if !errors.As(err, &jsonErr) || jsonErr.Code != invalidParams {
		t.Fatalf("session/callers without a node id = %v, want invalid params", err)
	}

	err = conn.Call(ctx, acpwire.MethodCallers, acpwire.CallersParams{SessionID: "nope", NodeID: "tool:1"}, &res)
	if !errors.As(err, &jsonErr) || jsonErr.Code != serverError {
		t.Fatalf("session/callers for an unknown session = %v, want a server error", err)
	}
}

// TestCallersAnswersThroughTheService pins that session/callers answers with the
// service's refs, and with an empty result when the service has no finder.
func TestCallersAnswersThroughTheService(t *testing.T) {
	cases := []struct {
		name   string
		finder service.ReferenceFinder
		want   string
	}{
		{name: "a finder answers", finder: sheetFinder{refs: []string{"a.go:10"}, ok: true}, want: `{"refs":["a.go:10"]}`},
		{name: "no finder", want: `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, a := startSheetAgent(t, sheetAgentConfig{marshal: true, finder: tc.finder})
			nodeID := logSheetEdit(t, a.state)

			var raw json.RawMessage
			if err := conn.Call(context.Background(), acpwire.MethodCallers,
				acpwire.CallersParams{SessionID: sheetSession, NodeID: nodeID}, &raw); err != nil {
				t.Fatalf("session/callers: %v", err)
			}
			if got := string(raw); got != tc.want {
				t.Fatalf("session/callers = %s, want %s", got, tc.want)
			}
		})
	}
}
