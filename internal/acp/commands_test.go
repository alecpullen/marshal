package acp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
)

// newTestCommandState builds a session State through the package's constructor
// rather than as a struct literal. `&session.State{}` bypasses the scope
// numbering (it leaves scopeID at 0, so ScopeID() reports "s0" — the identity a
// State that was never numbered shares with every other one) and leaves
// nextMsgID at 0, both of which falsify ScopeID's documented promise that the
// token is always set and per-State unique. The constructor is what other tests
// in this package use (see skills_test.go's newTestState).
//
// EVERY runtime in this file is built with it, not only the tests that happen to
// read State today. A zero State is a state the package's own contract says
// cannot exist, and a test that hands one to the code under test is asserting
// something false about the input even when the assertion it is making is about
// something else. Keeping all of them identical also means the next test added
// here cannot inherit a state whose ScopeID silently collides with every other
// unnumbered one — the failure that made this helper necessary.
func newTestCommandState() *session.State {
	return session.New(config.Default(), "/tmp", time.Unix(100, 0), session.Persistence{})
}

func newTestCommandRegistry(t *testing.T) *commands.Registry {
	t.Helper()
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name:        "diff",
		Description: "show diff",
		Handler: func(state *session.State, args []string) commands.Result {
			return commands.Text("diff output")
		},
	}); err != nil {
		t.Fatalf("register diff: %v", err)
	}
	if err := reg.Register(commands.Command{
		Name:        "settings",
		Description: "open settings",
		TUIOnly:     true,
	}); err != nil {
		t.Fatalf("register settings: %v", err)
	}
	if err := reg.Register(commands.Command{
		Name:        "someplugincmd",
		Description: "plugin prompt command",
		PromptBody:  "do the thing",
	}); err != nil {
		t.Fatalf("register someplugincmd: %v", err)
	}
	if err := reg.Register(commands.Command{
		Name:        "find",
		Description: "search the transcript",
		Args:        "<phrase>",
		TUIOnly:     true,
	}); err != nil {
		t.Fatalf("register find: %v", err)
	}
	return reg
}

func TestCommandManagerCommandListReturnsKinds(t *testing.T) {
	reg := newTestCommandRegistry(t)
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: newTestCommandState(), Registry: reg}, true
		},
		HasActive: func(sessionID string) bool { return false },
	})

	raw, err := json.Marshal(map[string]any{"sessionId": "sess_1"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := mgr.CommandList(context.Background(), raw)
	if err != nil {
		t.Fatalf("CommandList: %v", err)
	}
	list, ok := res.(CommandListResult)
	if !ok {
		t.Fatalf("CommandList result type = %T, want CommandListResult", res)
	}

	kinds := map[string]string{}
	for _, c := range list.Commands {
		kinds[c.Name] = c.Kind
	}
	if kinds["diff"] != "headless" {
		t.Errorf(`kinds["diff"] = %q, want "headless"`, kinds["diff"])
	}
	if kinds["settings"] != "tui_only" {
		t.Errorf(`kinds["settings"] = %q, want "tui_only"`, kinds["settings"])
	}
	if kinds["someplugincmd"] != "prompt" {
		t.Errorf(`kinds["someplugincmd"] = %q, want "prompt"`, kinds["someplugincmd"])
	}
}

func TestCommandManagerCommandListRequiresSessionID(t *testing.T) {
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup:    func(sessionID string) (*CommandRuntime, bool) { return nil, false },
		HasActive: func(sessionID string) bool { return false },
	})
	_, err := mgr.CommandList(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("CommandList with no sessionId: got nil error, want an error")
	}
}

func TestCommandManagerCommandRunsHeadlessHandler(t *testing.T) {
	reg := newTestCommandRegistry(t)
	var gotArgs []string
	if err := reg.Register(commands.Command{
		Name: "echo",
		Handler: func(state *session.State, args []string) commands.Result {
			gotArgs = args
			return commands.Text("ok: " + args[0])
		},
	}); err != nil {
		t.Fatalf("register echo: %v", err)
	}

	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: newTestCommandState(), Registry: reg}, true
		},
		HasActive: func(sessionID string) bool { return false },
	})

	raw, _ := json.Marshal(CommandParams{SessionID: "sess_1", Name: "echo", Args: []string{"hello"}})
	res, err := mgr.Command(context.Background(), raw)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	cr, ok := res.(CommandResult)
	if !ok {
		t.Fatalf("Command result type = %T, want CommandResult", res)
	}
	if cr.Text != "ok: hello" {
		t.Errorf("Command result.Text = %q, want %q", cr.Text, "ok: hello")
	}
	if len(gotArgs) != 1 || gotArgs[0] != "hello" {
		t.Errorf("Handler received args = %v, want [hello]", gotArgs)
	}
}

func TestCommandManagerCommandSerializesDoc(t *testing.T) {
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "panel",
		Handler: func(state *session.State, args []string) commands.Result {
			return commands.Panel("Title", false, []commands.Row{
				{Header: "Section"},
				{Text: "row1", Detail: "d1", Desc: "desc1"},
				{Text: "parent", Children: []commands.Row{{Text: "child"}}},
			})
		},
	}); err != nil {
		t.Fatalf("register panel: %v", err)
	}

	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: newTestCommandState(), Registry: reg}, true
		},
		HasActive: func(sessionID string) bool { return false },
	})

	raw, _ := json.Marshal(CommandParams{SessionID: "sess_1", Name: "panel"})
	res, err := mgr.Command(context.Background(), raw)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	cr := res.(CommandResult)
	if cr.Doc == nil {
		t.Fatal("Command result.Doc is nil, want a populated WireDoc")
	}
	if cr.Doc.Title != "Title" {
		t.Errorf("Doc.Title = %q, want %q", cr.Doc.Title, "Title")
	}
	if len(cr.Doc.Rows) != 3 {
		t.Fatalf("len(Doc.Rows) = %d, want 3", len(cr.Doc.Rows))
	}
	if cr.Doc.Rows[2].Text != "parent" || len(cr.Doc.Rows[2].Children) != 1 || cr.Doc.Rows[2].Children[0].Text != "child" {
		t.Errorf("Doc.Rows[2] children not preserved: %+v", cr.Doc.Rows[2])
	}
}

func TestCommandManagerCommandRejectsTUIOnly(t *testing.T) {
	reg := newTestCommandRegistry(t)
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: newTestCommandState(), Registry: reg}, true
		},
		HasActive: func(sessionID string) bool { return false },
	})
	raw, _ := json.Marshal(CommandParams{SessionID: "sess_1", Name: "settings"})
	_, err := mgr.Command(context.Background(), raw)
	if err == nil {
		t.Fatal("Command(settings): got nil error, want an error (TUI-only)")
	}
}

func TestCommandManagerCommandRejectsUnknownName(t *testing.T) {
	reg := newTestCommandRegistry(t)
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: newTestCommandState(), Registry: reg}, true
		},
		HasActive: func(sessionID string) bool { return false },
	})
	raw, _ := json.Marshal(CommandParams{SessionID: "sess_1", Name: "does-not-exist"})
	_, err := mgr.Command(context.Background(), raw)
	if err == nil {
		t.Fatal("Command(does-not-exist): got nil error, want an error")
	}
}

func TestCommandManagerCommandRejectsDuringActiveTurn(t *testing.T) {
	reg := newTestCommandRegistry(t)
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: newTestCommandState(), Registry: reg}, true
		},
		HasActive: func(sessionID string) bool { return sessionID == "sess_busy" },
	})
	raw, _ := json.Marshal(CommandParams{SessionID: "sess_busy", Name: "diff"})
	_, err := mgr.Command(context.Background(), raw)
	if err == nil {
		t.Fatal("Command during active turn: got nil error, want an error")
	}
}

func TestCommandManagerCommandListUnknownSession(t *testing.T) {
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup:    func(sessionID string) (*CommandRuntime, bool) { return nil, false },
		HasActive: func(sessionID string) bool { return false },
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "no_such_session"})
	_, err := mgr.CommandList(context.Background(), raw)
	if err == nil {
		t.Fatal("CommandList for unknown session: got nil error, want an error")
	}
}

func TestCommandManagerCommandRejectsMalformedParams(t *testing.T) {
	reg := newTestCommandRegistry(t)
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: newTestCommandState(), Registry: reg}, true
		},
		HasActive: func(sessionID string) bool { return false },
	})
	_, err := mgr.Command(context.Background(), json.RawMessage(`{"sessionId": `))
	if err == nil {
		t.Fatal("Command with malformed params JSON: got nil error, want an error")
	}
}

func TestCommandManagerCommandRejectsNilRegistry(t *testing.T) {
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			// The runtime is otherwise well-formed — the point is the nil
			// Registry — so the State comes from the constructor like every
			// other one here.
			return &CommandRuntime{State: newTestCommandState(), Registry: nil}, true
		},
		HasActive: func(sessionID string) bool { return false },
	})
	raw, _ := json.Marshal(CommandParams{SessionID: "sess_1", Name: "diff"})
	_, err := mgr.Command(context.Background(), raw)
	if err == nil {
		t.Fatal("Command with nil Registry: got nil error, want an error")
	}
}

// /find is TUIOnly with no Handler and no manager-owned headless
// implementation (supportsHeadless is keyed on the fixed
// headlessCommandNames map, which contains only "mcp"), so the documented
// rule in mcpauth.go — a command is headless-capable only via TUIOnly AND a
// handler, or a manager implementation — must reject it as TUI-only rather
// than route it anywhere. The rejection is the contract the /find reviewer
// finding asks to pin: /find's result is a position in the rendered
// transcript, which an ACP client has no way to consume.
func TestCommandManagerCommandRejectsFindAsTUIOnly(t *testing.T) {
	reg := newTestCommandRegistry(t)
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: newTestCommandState(), Registry: reg}, true
		},
		HasActive: func(sessionID string) bool { return false },
	})
	// The command_list catalog must also agree: /find is offered as
	// tui_only, never as headless — a client must not even be tempted to
	// run it.
	craw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := mgr.CommandList(context.Background(), craw)
	if err != nil {
		t.Fatalf("CommandList: %v", err)
	}
	for _, c := range res.(CommandListResult).Commands {
		if c.Name == "find" && (c.Kind == "headless" || c.Kind == "prompt") {
			t.Errorf("command_list kind for /find = %q, want \"tui_only\"", c.Kind)
		}
	}
	if mgr.supportsHeadless("find") {
		t.Error("supportsHeadless(find) = true, want false")
	}
	raw, _ := json.Marshal(CommandParams{SessionID: "sess_1", Name: "find", Args: []string{"needle"}})
	_, err = mgr.Command(context.Background(), raw)
	if err == nil {
		t.Fatal("Command(find): got nil error, want the TUI-only rejection")
	}
	if !strings.Contains(err.Error(), "not available over ACP") || !strings.Contains(err.Error(), "TUI-only") {
		t.Errorf("Command(find) error = %q, want the TUI-only rejection sentence (headless routing would produce a different failure)", err.Error())
	}
}

func TestCommandManagerCommandRejectsPromptBodyOnly(t *testing.T) {
	reg := newTestCommandRegistry(t)
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: newTestCommandState(), Registry: reg}, true
		},
		HasActive: func(sessionID string) bool { return false },
	})
	raw, _ := json.Marshal(CommandParams{SessionID: "sess_1", Name: "someplugincmd"})
	_, err := mgr.Command(context.Background(), raw)
	if err == nil {
		t.Fatal("Command(someplugincmd): got nil error, want an error (prompt-body command has no headless handler)")
	}
}
