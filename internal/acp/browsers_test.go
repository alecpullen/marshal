package acp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/skills"
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

// browserHistoryDB opens a migrated database with one project, as the
// service's history tests build theirs.
func browserHistoryDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	return database, projectID
}

// browserWritePlan writes a plan file into dir's plans directory.
func browserWritePlan(t *testing.T, dir, name, body string) string {
	t.Helper()
	plansDir := filepath.Join(dir, ".marshal", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	path := filepath.Join(plansDir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

// browserRepo returns a repository with two commits on main and their SHAs.
func browserRepo(t *testing.T) (dir, base, head string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmdArgs := append([]string{"-C", dir}, args...)
		out, err := exec.Command("git", cmdArgs...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "first")
	base = run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.txt: %v", err)
	}
	run("commit", "-q", "-am", "second")
	head = run("rev-parse", "HEAD")
	return dir, base, head
}

// TestThePromptHistoryRoundTrips pins the two history calls over the wire
// against a real database: a saved prompt comes back newest first.
func TestThePromptHistoryRoundTrips(t *testing.T) {
	database, projectID := browserHistoryDB(t)
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0),
		session.Persistence{DB: database, SessionID: browserSession})
	conn, _ := startBrowserAgent(t, true, service.Deps{State: state, ProjectID: projectID}, nil)

	for _, text := range []string{"first", "second"} {
		err := callEmpty(t, conn, acpwire.MethodPromptHistorySave, acpwire.PromptSaveParams{
			SessionID: browserSession,
			Text:      text,
		})
		if err != nil {
			t.Fatalf("session/prompt_history_save(%q): %v", text, err)
		}
	}

	var res acpwire.PromptHistoryResult
	err := conn.Call(context.Background(), acpwire.MethodPromptHistory,
		acpwire.PromptHistoryParams{SessionID: browserSession, Limit: 10}, &res)
	if err != nil {
		t.Fatalf("session/prompt_history: %v", err)
	}
	if want := []string{"second", "first"}; !reflect.DeepEqual(res.Prompts, want) {
		t.Errorf("prompts = %v, want %v", res.Prompts, want)
	}
}

// TestMessagesSelectsOverTheWire checks the query crosses the call intact: a
// role filter with a cap answers the newest match and the total it cut.
func TestMessagesSelectsOverTheWire(t *testing.T) {
	state := seamState(t, browserSession)
	state.AddMessage(session.RoleUser, "one", session.ContentTypePlain)
	state.AddMessageFinal(session.RoleAssistant, "answer", session.ContentTypePlain)
	state.AddMessage(session.RoleUser, "two", session.ContentTypePlain)
	conn, _ := startBrowserAgent(t, true, service.Deps{State: state}, nil)

	var res acpwire.MessagePage
	err := conn.Call(context.Background(), acpwire.MethodMessages, acpwire.MessagesParams{
		SessionID: browserSession,
		Query:     acpwire.MessageQuery{Role: acpwire.RoleUser, Last: 1},
	}, &res)
	if err != nil {
		t.Fatalf("session/messages: %v", err)
	}
	want := []acpwire.HistoryMessage{{Role: acpwire.RoleUser, Content: "two"}}
	if !reflect.DeepEqual(res.Messages, want) {
		t.Errorf("messages = %+v, want %+v", res.Messages, want)
	}
	if res.Total != 2 {
		t.Errorf("total = %d, want 2", res.Total)
	}
}

// TestTheProjectReadsReachTheService checks the four project calls: the file
// index, plan discovery, the starter plan and the discard refusal.
func TestTheProjectReadsReachTheService(t *testing.T) {
	dir := t.TempDir()
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	browserWritePlan(t, dir, "candidate.md", "# Plan\n\n## Task 1: Do it\n")
	conn, _ := startBrowserAgent(t, true, service.Deps{
		State:     state,
		FileIndex: func() ([]string, error) { return []string{"a.go", "b.go"}, nil },
	}, nil)
	ctx := context.Background()

	var index acpwire.FileIndexResult
	if err := conn.Call(ctx, acpwire.MethodFileIndex, acpwire.ControlParams{SessionID: browserSession}, &index); err != nil {
		t.Fatalf("session/file_index: %v", err)
	}
	if want := []string{"a.go", "b.go"}; !reflect.DeepEqual(index.Paths, want) {
		t.Errorf("paths = %v, want %v", index.Paths, want)
	}

	var plans acpwire.PlansResult
	if err := conn.Call(ctx, acpwire.MethodPlans, acpwire.ControlParams{SessionID: browserSession}, &plans); err != nil {
		t.Fatalf("session/plans: %v", err)
	}
	names := make([]string, 0, len(plans.Plans))
	for _, plan := range plans.Plans {
		names = append(names, plan.Name)
	}
	if want := []string{"candidate.md"}; !reflect.DeepEqual(names, want) {
		t.Errorf("plans = %v, want %v", names, want)
	}

	var first acpwire.ScaffoldResult
	if err := conn.Call(ctx, acpwire.MethodSDDScaffold, acpwire.ControlParams{SessionID: browserSession}, &first); err != nil {
		t.Fatalf("session/sdd_scaffold: %v", err)
	}
	if first.Path == "" || first.Existed {
		t.Errorf("scaffold = %+v, want a new plan's path", first)
	}
	var second acpwire.ScaffoldResult
	if err := conn.Call(ctx, acpwire.MethodSDDScaffold, acpwire.ControlParams{SessionID: browserSession}, &second); err != nil {
		t.Fatalf("session/sdd_scaffold: %v", err)
	}
	if !second.Existed {
		t.Error("second scaffold Existed = false, want true")
	}

	outside := filepath.Join(dir, "outside.md")
	if err := os.WriteFile(outside, []byte("# Plan\n\n## Task 1: Do it\n"), 0o644); err != nil {
		t.Fatalf("write outside plan: %v", err)
	}
	err := callEmpty(t, conn, acpwire.MethodSDDDiscard, acpwire.PathParams{
		SessionID: browserSession,
		Path:      outside,
	})
	if err == nil {
		t.Fatal("session/sdd_discard(outside) = nil, want the service's refusal")
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Errorf("the outside file is gone after a refused discard: %v", statErr)
	}
}

// TestAMarshalDiffTakesTheHead proves the routed diff names the range's head:
// the answer is the diff between the two commits, and ACP's handler, which
// cannot name one, is not called.
func TestAMarshalDiffTakesTheHead(t *testing.T) {
	dir, base, head := browserRepo(t)
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	var acpCalled atomic.Bool
	others := map[string]Handler{
		acpwire.MethodDiff: func(context.Context, json.RawMessage) (any, error) {
			acpCalled.Store(true)
			return acpwire.DiffResult{Diff: "acp"}, nil
		},
	}
	conn, _ := startBrowserAgent(t, true, service.Deps{State: state}, others)

	var res acpwire.DiffResult
	err := conn.Call(context.Background(), acpwire.MethodDiff, acpwire.DiffCallParams{
		SessionID: browserSession,
		Base:      base,
		Head:      head,
	}, &res)
	if err != nil {
		t.Fatalf("session/diff: %v", err)
	}
	if acpCalled.Load() {
		t.Error("the ACP handler served a marshal client's session/diff")
	}
	if !strings.Contains(res.Diff, "-one") || !strings.Contains(res.Diff, "+two") {
		t.Errorf("diff = %q, want the change between the two commits", res.Diff)
	}
}

// TestAMarshalReadFileStaysInTheDirectory checks a marshal client's file read
// keeps the service's containment rule.
func TestAMarshalReadFileStaysInTheDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	conn, _ := startBrowserAgent(t, true, service.Deps{State: state}, nil)

	var res acpwire.FileContent
	err := conn.Call(context.Background(), acpwire.MethodFile, acpwire.PathParams{
		SessionID: browserSession,
		Path:      "a.txt",
	}, &res)
	if err != nil {
		t.Fatalf("session/file: %v", err)
	}
	if res.Content != "hello" {
		t.Errorf("content = %q, want %q", res.Content, "hello")
	}

	err = callEmpty(t, conn, acpwire.MethodFile, acpwire.PathParams{
		SessionID: browserSession,
		Path:      "../outside",
	})
	if err == nil {
		t.Fatal("session/file(../outside) = nil, want the service's refusal")
	}
}

// TestADiffWithoutTheCapabilityReachesItsOtherHandler is V6 for the two routed
// project methods: a client that did not declare the capability reaches ACP's
// handler, unchanged.
func TestADiffWithoutTheCapabilityReachesItsOtherHandler(t *testing.T) {
	dir, base, head := browserRepo(t)
	var diffCalled, fileCalled atomic.Bool
	others := map[string]Handler{
		acpwire.MethodDiff: func(context.Context, json.RawMessage) (any, error) {
			diffCalled.Store(true)
			return acpwire.DiffResult{Diff: "acp"}, nil
		},
		acpwire.MethodFile: func(context.Context, json.RawMessage) (any, error) {
			fileCalled.Store(true)
			return acpwire.FileContent{Path: "acp.txt", Content: "acp"}, nil
		},
	}
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	conn, _ := startBrowserAgent(t, false, service.Deps{State: state}, others)
	ctx := context.Background()

	var diff acpwire.DiffResult
	if err := conn.Call(ctx, acpwire.MethodDiff, acpwire.DiffCallParams{
		SessionID: browserSession,
		Base:      base,
		Head:      head,
	}, &diff); err != nil {
		t.Fatalf("session/diff: %v", err)
	}
	if !diffCalled.Load() {
		t.Error("session/diff did not reach ACP's handler")
	}
	if diff.Diff != "acp" {
		t.Errorf("diff = %q, want ACP's %q", diff.Diff, "acp")
	}

	var file acpwire.FileContent
	if err := conn.Call(ctx, acpwire.MethodFile, acpwire.PathParams{
		SessionID: browserSession,
		Path:      "a.txt",
	}, &file); err != nil {
		t.Fatalf("session/file: %v", err)
	}
	if !fileCalled.Load() {
		t.Error("session/file did not reach ACP's handler")
	}
	if file.Content != "acp" {
		t.Errorf("content = %q, want ACP's %q", file.Content, "acp")
	}
}

// browserPluginRepo returns a local plugin repository with one command,
// committed, the way the service's plugin tests build theirs.
func browserPluginRepo(t *testing.T) (src, commit string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	src = filepath.Join(t.TempDir(), "hello-plugin")
	run := func(args ...string) {
		t.Helper()
		cmdArgs := append([]string{"-C", src}, args...)
		if out, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	const command = "+++\nname = \"hello\"\ndescription = \"says hello\"\n+++\necho hello\n"
	if err := os.WriteFile(filepath.Join(src, "commands", "hello.md"), []byte(command), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("add", "-A")
	run("commit", "-m", "initial")
	out, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return src, strings.TrimSpace(string(out))
}

// TestAMarshalMemoryListIsTheProjectsOwn pins decision 1 for the memory
// browser: a marshal client reads the project's own rows, oldest first, in
// Unix milliseconds, and without a database both calls answer CodeNoDatabase.
func TestAMarshalMemoryListIsTheProjectsOwn(t *testing.T) {
	database, projectID := browserHistoryDB(t)
	otherID, err := database.GetOrCreateProject("/other", "other")
	if err != nil {
		t.Fatalf("GetOrCreateProject other: %v", err)
	}
	if err := database.CreateSession(browserSession, projectID, "", time.Unix(1000, 0)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := database.CreateSession("s9", otherID, "", time.Unix(1000, 0)); err != nil {
		t.Fatalf("CreateSession other: %v", err)
	}
	created := time.Unix(4000, 0).UTC()
	if err := database.SaveMemory(projectID, "fact", "the repo uses sqlite.", browserSession, created); err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	if err := database.SaveMemory(otherID, "fact", "another project's memory.", "s9", created); err != nil {
		t.Fatalf("SaveMemory other: %v", err)
	}

	state := session.New(config.Default(), "/repo", time.Unix(500, 0),
		session.Persistence{DB: database, SessionID: browserSession})
	conn, _ := startBrowserAgent(t, true, service.Deps{State: state, ProjectID: projectID}, nil)
	ctx := context.Background()

	var res acpwire.MemoriesResult
	if err := conn.Call(ctx, acpwire.MethodMemoryList, acpwire.ControlParams{SessionID: browserSession}, &res); err != nil {
		t.Fatalf("session/memory_list: %v", err)
	}
	if len(res.Memories) != 1 {
		t.Fatalf("memories = %+v, want the project's one row", res.Memories)
	}
	entry := res.Memories[0]
	if entry.Content != "the repo uses sqlite." {
		t.Errorf("content = %q, want the saved text", entry.Content)
	}
	if want := acpwire.Millis(created); entry.CreatedAt != want {
		t.Errorf("createdAt = %d, want %d", entry.CreatedAt, want)
	}

	if err := callEmpty(t, conn, acpwire.MethodMemoryDelete, acpwire.MemoryDeleteParams{
		SessionID: browserSession,
		ID:        entry.ID,
	}); err != nil {
		t.Fatalf("session/memory_delete: %v", err)
	}
	var after acpwire.MemoriesResult
	if err := conn.Call(ctx, acpwire.MethodMemoryList, acpwire.ControlParams{SessionID: browserSession}, &after); err != nil {
		t.Fatalf("session/memory_list after delete: %v", err)
	}
	if len(after.Memories) != 0 {
		t.Errorf("memories after delete = %+v, want none", after.Memories)
	}

	bare, _ := startBrowserAgent(t, true, service.Deps{State: seamState(t, browserSession)}, nil)
	if err := callEmpty(t, bare, acpwire.MethodMemoryList, acpwire.ControlParams{SessionID: browserSession}); !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Errorf("session/memory_list without a database = %v, want CodeNoDatabase", err)
	}
	if err := callEmpty(t, bare, acpwire.MethodMemoryDelete, acpwire.MemoryDeleteParams{SessionID: browserSession, ID: 1}); !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Errorf("session/memory_delete without a database = %v, want CodeNoDatabase", err)
	}
}

// TestAMarshalPluginScanCarriesTheContents checks the plugin browser end to
// end: the scan's contents and token, the install, the listing and the remove.
func TestAMarshalPluginScanCarriesTheContents(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	state := session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{SessionID: browserSession})
	conn, _ := startBrowserAgent(t, true, service.Deps{State: state, HomeDir: home}, nil)
	src, commit := browserPluginRepo(t)
	ctx := context.Background()

	var scan acpwire.PluginScan
	err := conn.Call(ctx, acpwire.MethodPluginsInstallScan, acpwire.PluginScanParams{
		SessionID: browserSession,
		Source:    src,
	}, &scan)
	if err != nil {
		t.Fatalf("session/plugins_install_scan: %v", err)
	}
	if scan.Token == "" {
		t.Fatal("the scan carries no token")
	}
	if scan.Commit != commit {
		t.Errorf("commit = %q, want the repository's %q", scan.Commit, commit)
	}
	if len(scan.Contents.Commands) != 1 || scan.Contents.Commands[0].Name != "hello" {
		t.Errorf("contents = %+v, want the hello command", scan.Contents)
	}

	err = callEmpty(t, conn, acpwire.MethodPluginsInstallConfirm, acpwire.PluginInstallParams{
		SessionID: browserSession,
		ScanToken: scan.Token,
		Scope:     "global",
	})
	if err != nil {
		t.Fatalf("session/plugins_install_confirm: %v", err)
	}

	var list acpwire.PluginsResult
	if err := conn.Call(ctx, acpwire.MethodPluginsList, acpwire.ControlParams{SessionID: browserSession}, &list); err != nil {
		t.Fatalf("session/plugins_list: %v", err)
	}
	if len(list.Plugins) != 1 || list.Plugins[0].Name != scan.Name {
		t.Fatalf("plugins = %+v, want the installed %q", list.Plugins, scan.Name)
	}

	err = callEmpty(t, conn, acpwire.MethodPluginsRemove, acpwire.PluginRemoveParams{
		SessionID: browserSession,
		Name:      scan.Name,
		Scope:     "global",
	})
	if err != nil {
		t.Fatalf("session/plugins_remove: %v", err)
	}
	var after acpwire.PluginsResult
	if err := conn.Call(ctx, acpwire.MethodPluginsList, acpwire.ControlParams{SessionID: browserSession}, &after); err != nil {
		t.Fatalf("session/plugins_list after remove: %v", err)
	}
	if len(after.Plugins) != 0 {
		t.Errorf("plugins after remove = %+v, want none", after.Plugins)
	}
}

// TestADiscardedScanCannotBeInstalled keeps the discard's effect: a token it
// deleted cannot install.
func TestADiscardedScanCannotBeInstalled(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	state := session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{SessionID: browserSession})
	conn, _ := startBrowserAgent(t, true, service.Deps{State: state, HomeDir: home}, nil)
	src, _ := browserPluginRepo(t)
	ctx := context.Background()

	var scan acpwire.PluginScan
	err := conn.Call(ctx, acpwire.MethodPluginsInstallScan, acpwire.PluginScanParams{
		SessionID: browserSession,
		Source:    src,
	}, &scan)
	if err != nil {
		t.Fatalf("session/plugins_install_scan: %v", err)
	}
	if err := callEmpty(t, conn, acpwire.MethodPluginsInstallDiscard, acpwire.PluginDiscardParams{
		SessionID: browserSession,
		ScanToken: scan.Token,
	}); err != nil {
		t.Fatalf("session/plugins_install_discard: %v", err)
	}

	err = callEmpty(t, conn, acpwire.MethodPluginsInstallConfirm, acpwire.PluginInstallParams{
		SessionID: browserSession,
		ScanToken: scan.Token,
		Scope:     "global",
	})
	if err == nil {
		t.Fatal("confirming a discarded scan = nil, want the service's refusal")
	}
}

// TestAMarshalSkillIsKeyedByItsSource pins the skill browser's key: a skill is
// listed, loaded and removed by its store and source, never by its name.
func TestAMarshalSkillIsKeyedByItsSource(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	store := filepath.Join(config.UserDir(home), "skills")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatalf("create the skill store: %v", err)
	}
	const body = "+++\nname = \"alpha\"\ndescription = \"global fixture\"\n+++\n\n# alpha\n"
	path := filepath.Join(store, "alpha.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	parsed, err := skills.Parse(body)
	if err != nil {
		t.Fatalf("parse skill: %v", err)
	}
	idx := skills.NewIndex()
	idx.Set("alpha", parsed)

	state := session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{SessionID: browserSession})
	conn, _ := startBrowserAgent(t, true, service.Deps{
		State:      state,
		HomeDir:    home,
		SkillIndex: func() *skills.Index { return idx },
	}, nil)
	ctx := context.Background()

	var list acpwire.SkillsResult
	if err := conn.Call(ctx, acpwire.MethodSkillsList, acpwire.ControlParams{SessionID: browserSession}, &list); err != nil {
		t.Fatalf("session/skills_list: %v", err)
	}
	if len(list.Skills) != 1 {
		t.Fatalf("skills = %+v, want the one global entry", list.Skills)
	}
	entry := list.Skills[0]
	if entry.Scope != "global" || entry.Source != "alpha.md" || entry.Name != "alpha" {
		t.Errorf("entry = %+v, want the global alpha.md keyed by its source", entry)
	}

	var loaded acpwire.SkillNameResult
	err = conn.Call(ctx, acpwire.MethodSkillsLoad, acpwire.SkillParams{
		SessionID: browserSession,
		Scope:     entry.Scope,
		Source:    entry.Source,
	}, &loaded)
	if err != nil {
		t.Fatalf("session/skills_load: %v", err)
	}
	if loaded.Name != "alpha" {
		t.Errorf("loaded name = %q, want alpha", loaded.Name)
	}
	if !state.HasActiveSkill("alpha") {
		t.Error("the skill was not loaded into the session")
	}

	err = callEmpty(t, conn, acpwire.MethodSkillsRemove, acpwire.SkillParams{
		SessionID: browserSession,
		Scope:     entry.Scope,
		Source:    entry.Source,
	})
	if err != nil {
		t.Fatalf("session/skills_remove: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the skill's source is still there after remove: %v", statErr)
	}
}

// TestTheSkillSettingsReachTheService checks the three settings calls: the user
// config's autoload list, the session's gate and a cleared decision.
func TestTheSkillSettingsReachTheService(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	userConfig := filepath.Join(home, "config.toml")
	state := session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{SessionID: browserSession})
	conn, _ := startBrowserAgent(t, true, service.Deps{
		State:          state,
		HomeDir:        home,
		UserConfigPath: userConfig,
	}, nil)

	err := callEmpty(t, conn, acpwire.MethodSkillsAutoload, acpwire.SkillAutoloadParams{
		SessionID: browserSession,
		Name:      "alpha",
		On:        true,
	})
	if err != nil {
		t.Fatalf("session/skills_autoload: %v", err)
	}
	raw, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatalf("read the user config: %v", err)
	}
	if !strings.Contains(string(raw), "alpha") {
		t.Errorf("the user config does not hold alpha:\n%s", raw)
	}

	if err := callEmpty(t, conn, acpwire.MethodSkillGateSet, acpwire.SkillGateParams{
		SessionID: browserSession,
		On:        true,
	}); err != nil {
		t.Fatalf("session/skill_gate_set: %v", err)
	}
	if !state.SkillGateEnabled() {
		t.Error("the session's gate is off after session/skill_gate_set(on)")
	}

	state.SkillGateRecordDeny("alpha")
	if got := state.SkillGateDecisions(); len(got) != 1 {
		t.Fatalf("gate decisions = %+v, want the recorded deny", got)
	}
	if err := callEmpty(t, conn, acpwire.MethodSkillGateClear, acpwire.SkillGateClearParams{
		SessionID: browserSession,
		Skill:     "alpha",
	}); err != nil {
		t.Fatalf("session/skill_gate_clear: %v", err)
	}
	if got := state.SkillGateDecisions(); len(got) != 0 {
		t.Errorf("gate decisions after clear = %+v, want none", got)
	}
}

// TestABrowserWithoutTheCapabilityReachesItsOtherHandler is V6 for the ten
// routed browser methods: a client that did not declare the capability reaches
// ACP's handler, unchanged.
func TestABrowserWithoutTheCapabilityReachesItsOtherHandler(t *testing.T) {
	routed := []struct {
		method string
		params func(id string) any
	}{
		{acpwire.MethodMemoryList, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodMemoryDelete, func(id string) any {
			return acpwire.MemoryDeleteParams{SessionID: id, ID: 1}
		}},
		{acpwire.MethodPluginsList, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodPluginsInstallScan, func(id string) any {
			return acpwire.PluginScanParams{SessionID: id, Source: "src"}
		}},
		{acpwire.MethodPluginsInstallConfirm, func(id string) any {
			return acpwire.PluginInstallParams{SessionID: id, ScanToken: "t", Scope: "global"}
		}},
		{acpwire.MethodPluginsInstallDiscard, func(id string) any {
			return acpwire.PluginDiscardParams{SessionID: id, ScanToken: "t"}
		}},
		{acpwire.MethodPluginsRemove, func(id string) any {
			return acpwire.PluginRemoveParams{SessionID: id, Name: "n", Scope: "global"}
		}},
		{acpwire.MethodSkillsList, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodSkillsLoad, func(id string) any {
			return acpwire.SkillParams{SessionID: id, Scope: "global", Source: "s.md"}
		}},
		{acpwire.MethodSkillsRemove, func(id string) any {
			return acpwire.SkillParams{SessionID: id, Scope: "global", Source: "s.md"}
		}},
	}

	called := map[string]*atomic.Bool{}
	others := map[string]Handler{}
	for _, tc := range routed {
		flag := &atomic.Bool{}
		called[tc.method] = flag
		others[tc.method] = func(context.Context, json.RawMessage) (any, error) {
			flag.Store(true)
			return struct{}{}, nil
		}
	}
	conn, _ := startBrowserAgent(t, false, service.Deps{State: seamState(t, browserSession)}, others)

	for _, tc := range routed {
		t.Run(tc.method, func(t *testing.T) {
			if err := callEmpty(t, conn, tc.method, tc.params(browserSession)); err != nil {
				t.Fatalf("%s: %v", tc.method, err)
			}
			if !called[tc.method].Load() {
				t.Errorf("%s did not reach ACP's handler", tc.method)
			}
		})
	}
}

// TestTheBrowserCallsRefuseBadParams pins the same two refusals for the memory,
// plugin and skill calls.
func TestTheBrowserCallsRefuseBadParams(t *testing.T) {
	conn, _ := startBrowserAgent(t, true, service.Deps{State: seamState(t, browserSession)}, nil)

	cases := []struct {
		method string
		params func(sessionID string) any
	}{
		{acpwire.MethodMemoryList, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodMemoryDelete, func(id string) any {
			return acpwire.MemoryDeleteParams{SessionID: id, ID: 1}
		}},
		{acpwire.MethodPluginsList, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodPluginsInstallScan, func(id string) any {
			return acpwire.PluginScanParams{SessionID: id, Source: "src"}
		}},
		{acpwire.MethodPluginsInstallConfirm, func(id string) any {
			return acpwire.PluginInstallParams{SessionID: id, ScanToken: "t", Scope: "global"}
		}},
		{acpwire.MethodPluginsInstallDiscard, func(id string) any {
			return acpwire.PluginDiscardParams{SessionID: id, ScanToken: "t"}
		}},
		{acpwire.MethodPluginsRemove, func(id string) any {
			return acpwire.PluginRemoveParams{SessionID: id, Name: "n", Scope: "global"}
		}},
		{acpwire.MethodSkillsList, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodSkillsLoad, func(id string) any {
			return acpwire.SkillParams{SessionID: id, Scope: "global", Source: "s.md"}
		}},
		{acpwire.MethodSkillsInstall, func(id string) any {
			return acpwire.SkillInstallParams{SessionID: id, Source: "s", Scope: "global"}
		}},
		{acpwire.MethodSkillsRemove, func(id string) any {
			return acpwire.SkillParams{SessionID: id, Scope: "global", Source: "s.md"}
		}},
		{acpwire.MethodSkillsAutoload, func(id string) any {
			return acpwire.SkillAutoloadParams{SessionID: id, Name: "alpha", On: true}
		}},
		{acpwire.MethodSkillGateSet, func(id string) any {
			return acpwire.SkillGateParams{SessionID: id, On: true}
		}},
		{acpwire.MethodSkillGateClear, func(id string) any {
			return acpwire.SkillGateClearParams{SessionID: id, Skill: "alpha"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.method+" without a session ID", func(t *testing.T) {
			err := callEmpty(t, conn, tc.method, tc.params(""))
			if err == nil || err.Error() != "invalid params" {
				t.Fatalf("%s = %v, want the invalid-params refusal", tc.method, err)
			}
		})
		t.Run(tc.method+" for an unknown session", func(t *testing.T) {
			err := callEmpty(t, conn, tc.method, tc.params("nope"))
			if err == nil || !strings.Contains(err.Error(), "unknown session") {
				t.Fatalf("%s = %v, want the unknown-session refusal", tc.method, err)
			}
		})
	}
}

func TestTheProjectCallsRefuseBadParams(t *testing.T) {
	conn, _ := startBrowserAgent(t, true, service.Deps{State: seamState(t, browserSession)}, nil)

	cases := []struct {
		method string
		params func(sessionID string) any
	}{
		{acpwire.MethodPromptHistory, func(id string) any {
			return acpwire.PromptHistoryParams{SessionID: id, Limit: 5}
		}},
		{acpwire.MethodPromptHistorySave, func(id string) any {
			return acpwire.PromptSaveParams{SessionID: id, Text: "hi"}
		}},
		{acpwire.MethodMessages, func(id string) any {
			return acpwire.MessagesParams{SessionID: id, Query: acpwire.MessageQuery{Role: acpwire.RoleUser}}
		}},
		{acpwire.MethodFileIndex, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodIntelligence, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodPlans, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodSDDDiscard, func(id string) any {
			return acpwire.PathParams{SessionID: id, Path: "x.md"}
		}},
		{acpwire.MethodSDDScaffold, func(id string) any {
			return acpwire.ControlParams{SessionID: id}
		}},
		{acpwire.MethodDiff, func(id string) any {
			return acpwire.DiffCallParams{SessionID: id, Base: "a", Head: "b"}
		}},
		{acpwire.MethodFile, func(id string) any {
			return acpwire.PathParams{SessionID: id, Path: "a.txt"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.method+" without a session ID", func(t *testing.T) {
			err := callEmpty(t, conn, tc.method, tc.params(""))
			if err == nil || err.Error() != "invalid params" {
				t.Fatalf("%s = %v, want the invalid-params refusal", tc.method, err)
			}
		})
		t.Run(tc.method+" for an unknown session", func(t *testing.T) {
			err := callEmpty(t, conn, tc.method, tc.params("nope"))
			if err == nil || !strings.Contains(err.Error(), "unknown session") {
				t.Fatalf("%s = %v, want the unknown-session refusal", tc.method, err)
			}
		})
	}
}
