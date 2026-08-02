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
