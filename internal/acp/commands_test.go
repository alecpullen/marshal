package acp

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/commands"
)

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
	return reg
}

func TestCommandManagerCommandListReturnsKinds(t *testing.T) {
	reg := newTestCommandRegistry(t)
	mgr := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			return &CommandRuntime{State: &session.State{}, Registry: reg}, true
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
			return &CommandRuntime{State: &session.State{}, Registry: reg}, true
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
			return &CommandRuntime{State: &session.State{}, Registry: reg}, true
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
			return &CommandRuntime{State: &session.State{}, Registry: reg}, true
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
			return &CommandRuntime{State: &session.State{}, Registry: reg}, true
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
			return &CommandRuntime{State: &session.State{}, Registry: reg}, true
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
