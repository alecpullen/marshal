package acp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/commands"
)

// browserSession is the session every browser test serves.
const browserSession = "s1"

// startBrowserAgent serves the browser handlers on the agent end of a pipe and
// returns a client connection that has completed initialize with (or without)
// the marshal capability, and the service it serves.
func startBrowserAgent(t *testing.T, marshal bool, deps service.Deps, others map[string]Handler) (*rpc.Conn, *service.Session) {
	t.Helper()

	_, served := service.New(deps)
	client, agent := rpc.Pipe()
	srv := NewServer(agent, agent, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	// The agent's end sets its own cap; neither end inherits the default (P3 R2).
	srv.MaxLineBytes = rpc.MaxFrameBytes
	registerInitialize(srv)
	browsers := NewBrowserManager(BrowserManagerConfig{
		Lookup: func(id string) (*service.Session, bool) {
			return served, id == browserSession
		},
	})
	registerBrowsers(srv, browsers, others)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()

	conn := rpc.NewConn(client, rpc.Handlers{})

	t.Cleanup(func() {
		_ = conn.Close()
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
	return conn, served
}

// callEmpty sends a call whose result the test does not read.
func callEmpty(t *testing.T, conn *rpc.Conn, method string, params any) error {
	t.Helper()
	var res struct{}
	return conn.Call(context.Background(), method, params, &res)
}

// TestANoteReachesTheTranscript checks session/note appends the note on the
// session's service and that only system and user are accepted.
func TestANoteReachesTheTranscript(t *testing.T) {
	state := seamState(t, browserSession)
	conn, _ := startBrowserAgent(t, true, service.Deps{State: state}, nil)

	err := callEmpty(t, conn, acpwire.MethodNote, acpwire.NoteParams{
		SessionID: browserSession,
		Note:      acpwire.Note{Text: "hi", Role: acpwire.RoleUser, ContentType: acpwire.ContentTypeMarkdown},
	})
	if err != nil {
		t.Fatalf("session/note: %v", err)
	}
	msgs := state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Role != session.RoleUser {
		t.Errorf("role = %q, want %q", msgs[0].Role, session.RoleUser)
	}
	if msgs[0].Content != "hi" {
		t.Errorf("content = %q, want %q", msgs[0].Content, "hi")
	}

	err = callEmpty(t, conn, acpwire.MethodNote, acpwire.NoteParams{
		SessionID: browserSession,
		Note:      acpwire.Note{Text: "hi", Role: acpwire.RoleAssistant},
	})
	if err == nil || !strings.Contains(err.Error(), "a note's role must be system or user") {
		t.Fatalf("session/note(assistant) = %v, want the service's sentence", err)
	}
}

// TestAMarshalCommandRunsOnTheService proves decision 1 for the commands: a
// marshal client's session/command and session/command_action reach the
// session's service, and the ACP handler is not called.
func TestAMarshalCommandRunsOnTheService(t *testing.T) {
	state := seamState(t, browserSession)
	reg := commands.New()
	err := reg.Register(commands.Command{
		Name: "panel",
		Handler: func(*session.State, []string) commands.Result {
			return commands.Panel("panel", false, []commands.Row{{
				Text:        "row",
				ActionLabel: "run",
				Action: func(*session.State) commands.Result {
					return commands.Text("ran")
				},
			}})
		},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	var acpCalled atomic.Bool
	others := map[string]Handler{
		acpwire.MethodCommand: func(context.Context, json.RawMessage) (any, error) {
			acpCalled.Store(true)
			return acpwire.CommandResult{Text: "acp"}, nil
		},
	}
	conn, _ := startBrowserAgent(t, true, service.Deps{State: state, Commands: reg}, others)

	ctx := context.Background()
	var res acpwire.CommandResult
	if err := conn.Call(ctx, acpwire.MethodCommand, acpwire.CommandCallParams{SessionID: browserSession, Name: "panel"}, &res); err != nil {
		t.Fatalf("session/command: %v", err)
	}
	if acpCalled.Load() {
		t.Error("the ACP handler served a marshal client's session/command")
	}
	if res.Doc == nil || len(res.Doc.Rows) != 1 {
		t.Fatalf("result = %+v, want a doc with one row", res)
	}
	id := res.Doc.Rows[0].ActionID
	if id == "" {
		t.Fatal("the row action carries no ID")
	}

	var action acpwire.CommandResult
	if err := conn.Call(ctx, acpwire.MethodCommandAction, acpwire.CommandActionParams{SessionID: browserSession, ID: id}, &action); err != nil {
		t.Fatalf("session/command_action: %v", err)
	}
	if action.Text != "ran" {
		t.Errorf("action text = %q, want %q", action.Text, "ran")
	}
}

// TestAStaleActionKeepsItsSentence checks an ID the session no longer keeps
// reaches the client as the service's sentence.
func TestAStaleActionKeepsItsSentence(t *testing.T) {
	conn, _ := startBrowserAgent(t, true, service.Deps{State: seamState(t, browserSession)}, nil)

	err := callEmpty(t, conn, acpwire.MethodCommandAction, acpwire.CommandActionParams{
		SessionID: browserSession,
		ID:        "gone",
	})
	if err == nil || !strings.Contains(err.Error(), "that action is no longer available") {
		t.Fatalf("session/command_action(gone) = %v, want the service's sentence", err)
	}
}

// TestTheCommandListIsTheServices checks a marshal client reads the registry
// itself: name order and kinds unrewritten, and an empty list rather than an
// error when the session has no registry.
func TestTheCommandListIsTheServices(t *testing.T) {
	reg := commands.New()
	handler := func(*session.State, []string) commands.Result { return commands.Text("ran") }
	for _, entry := range []commands.Command{
		{Name: "zeta", Description: "Zeta prompt", PromptBody: "the body"},
		{Name: "alpha", Description: "Alpha command", Handler: handler},
	} {
		if err := reg.Register(entry); err != nil {
			t.Fatalf("register %q: %v", entry.Name, err)
		}
	}
	conn, _ := startBrowserAgent(t, true, service.Deps{State: seamState(t, browserSession), Commands: reg}, nil)

	var res acpwire.CommandListResult
	if err := conn.Call(context.Background(), acpwire.MethodCommandList, acpwire.ControlParams{SessionID: browserSession}, &res); err != nil {
		t.Fatalf("session/command_list: %v", err)
	}
	if len(res.Commands) != 2 {
		t.Fatalf("commands = %+v, want two", res.Commands)
	}
	if res.Commands[0].Name != "alpha" || res.Commands[0].Kind != acpwire.CommandKindHeadless {
		t.Errorf("first = %+v, want alpha headless", res.Commands[0])
	}
	if res.Commands[1].Name != "zeta" || res.Commands[1].Kind != acpwire.CommandKindPrompt {
		t.Errorf("second = %+v, want zeta prompt", res.Commands[1])
	}

	bare, _ := startBrowserAgent(t, true, service.Deps{State: seamState(t, browserSession)}, nil)
	var empty acpwire.CommandListResult
	if err := bare.Call(context.Background(), acpwire.MethodCommandList, acpwire.ControlParams{SessionID: browserSession}, &empty); err != nil {
		t.Fatalf("session/command_list without a registry = %v, want no error", err)
	}
	if len(empty.Commands) != 0 {
		t.Errorf("commands without a registry = %+v, want none", empty.Commands)
	}
}

// TestACommandWithoutTheCapabilityReachesItsOtherHandler is V6 for the two
// routed command methods: a client that did not declare the capability
// reaches ACP's handler, unchanged.
func TestACommandWithoutTheCapabilityReachesItsOtherHandler(t *testing.T) {
	state := seamState(t, browserSession)
	reg := commands.New()
	handler := func(*session.State, []string) commands.Result { return commands.Text("service") }
	if err := reg.Register(commands.Command{Name: "alpha", Handler: handler}); err != nil {
		t.Fatalf("register: %v", err)
	}
	var commandCalled, listCalled atomic.Bool
	others := map[string]Handler{
		acpwire.MethodCommand: func(context.Context, json.RawMessage) (any, error) {
			commandCalled.Store(true)
			return acpwire.CommandResult{Text: "acp"}, nil
		},
		acpwire.MethodCommandList: func(context.Context, json.RawMessage) (any, error) {
			listCalled.Store(true)
			return acpwire.CommandListResult{Commands: []acpwire.CommandInfo{{
				Name: "acp-only",
				Kind: acpwire.CommandKindPrompt,
			}}}, nil
		},
	}
	conn, _ := startBrowserAgent(t, false, service.Deps{State: state, Commands: reg}, others)

	ctx := context.Background()
	var res acpwire.CommandResult
	if err := conn.Call(ctx, acpwire.MethodCommand, acpwire.CommandCallParams{SessionID: browserSession, Name: "alpha"}, &res); err != nil {
		t.Fatalf("session/command: %v", err)
	}
	if !commandCalled.Load() {
		t.Error("session/command did not reach ACP's handler")
	}
	if res.Text != "acp" {
		t.Errorf("text = %q, want ACP's %q", res.Text, "acp")
	}

	var list acpwire.CommandListResult
	if err := conn.Call(ctx, acpwire.MethodCommandList, acpwire.ControlParams{SessionID: browserSession}, &list); err != nil {
		t.Fatalf("session/command_list: %v", err)
	}
	if !listCalled.Load() {
		t.Error("session/command_list did not reach ACP's handler")
	}
	if len(list.Commands) != 1 || list.Commands[0].Name != "acp-only" {
		t.Errorf("commands = %+v, want ACP's acp-only entry", list.Commands)
	}
}

// TestTheNoteAndCommandCallsRefuseBadParams pins the two refusals every browser
// call shares: a missing session ID is invalid params, and an unknown one is
// the unknown-session error.
func TestTheNoteAndCommandCallsRefuseBadParams(t *testing.T) {
	conn, _ := startBrowserAgent(t, true, service.Deps{State: seamState(t, browserSession)}, nil)

	cases := []struct {
		method string
		params func(sessionID string) any
	}{
		{acpwire.MethodNote, func(id string) any {
			return acpwire.NoteParams{SessionID: id, Note: acpwire.Note{Text: "hi"}}
		}},
		{acpwire.MethodCommand, func(id string) any {
			return acpwire.CommandCallParams{SessionID: id, Name: "alpha"}
		}},
		{acpwire.MethodCommandList, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodCommandAction, func(id string) any {
			return acpwire.CommandActionParams{SessionID: id, ID: "a1"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.method+" without a session ID", func(t *testing.T) {
			// The server writes a JSON-RPC error's code as its fixed string, so
			// the invalid-params refusal arrives as "invalid params" (F-SEC-37).
			err := callEmpty(t, conn, tc.method, tc.params(""))
			if err == nil || err.Error() != "invalid params" {
				t.Fatalf("%s = %v, want the invalid-params refusal", tc.method, err)
			}
		})
		t.Run(tc.method+" for an unknown session", func(t *testing.T) {
			// A server error's sentence is client-safe, so it survives the
			// server's rewrite with "server error: " in front of it.
			err := callEmpty(t, conn, tc.method, tc.params("nope"))
			if err == nil || !strings.Contains(err.Error(), "unknown session") {
				t.Fatalf("%s = %v, want the unknown-session refusal", tc.method, err)
			}
		})
	}
}
