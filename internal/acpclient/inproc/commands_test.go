package inproc

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/commands"
)

// TestCommandsListsTheRegistry pins session/command_list's contents: name order,
// hidden commands included, with every field a client's dispatch reads.
func TestCommandsListsTheRegistry(t *testing.T) {
	reg := commands.New()
	handler := func(*session.State, []string) commands.Result { return commands.Text("ran") }
	entries := []commands.Command{
		{Name: "zeta", Description: "Zeta prompt", Group: "Chat", PromptBody: "the body"},
		{Name: "alpha", Description: "Alpha command", Args: "<x>", Group: "Chat", Handler: handler},
		{Name: "trust", Description: "Trust folder", Group: "Settings", TUIOnly: true, Handler: handler},
		{Name: "beta", Description: "Beta command", Hidden: true, Handler: handler},
	}
	for _, entry := range entries {
		if err := reg.Register(entry); err != nil {
			t.Fatalf("register %q: %v", entry.Name, err)
		}
	}
	_, sess := New(Deps{State: newState(t, "s1"), Commands: reg})

	got := sess.Commands()
	want := []acpwire.CommandInfo{
		{Name: "alpha", Description: "Alpha command", Args: "<x>", Group: "Chat", Kind: acpwire.CommandKindHeadless, HasHandler: true},
		{Name: "beta", Description: "Beta command", Kind: acpwire.CommandKindHeadless, Hidden: true, HasHandler: true},
		{Name: "trust", Description: "Trust folder", Group: "Settings", Kind: acpwire.CommandKindTUIOnly, TUIOnly: true, HasHandler: true},
		{Name: "zeta", Description: "Zeta prompt", Group: "Chat", Kind: acpwire.CommandKindPrompt, PromptBody: "the body"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Commands() =\n%+v\nwant\n%+v", got, want)
	}

	_, bare := New(Deps{State: newState(t, "s2")})
	if got := bare.Commands(); got != nil {
		t.Errorf("Commands() without a registry = %+v, want nil", got)
	}
}

// TestCommandRunsTheHandlerOverTheSession checks the handler gets the session's
// own state and the caller's args, and that its text comes back.
func TestCommandRunsTheHandlerOverTheSession(t *testing.T) {
	state := newState(t, "s1")
	var gotState *session.State
	var gotArgs []string
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "alpha",
		Handler: func(st *session.State, args []string) commands.Result {
			gotState = st
			gotArgs = args
			return commands.Text("done")
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, sess := New(Deps{State: state, Commands: reg})

	res, err := sess.Command(context.Background(), "alpha", []string{"x"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.Text != "done" {
		t.Errorf("Text = %q, want %q", res.Text, "done")
	}
	if gotState != state {
		t.Errorf("handler state = %p, want the session's %p", gotState, state)
	}
	if !reflect.DeepEqual(gotArgs, []string{"x"}) {
		t.Errorf("handler args = %v, want [x]", gotArgs)
	}
}

// TestCommandMintsActionIDs checks each row action of a returned panel carries
// an ID CommandAction runs, depth first in row order.
func TestCommandMintsActionIDs(t *testing.T) {
	state := newState(t, "s1")
	var innerState *session.State
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "panel",
		Handler: func(*session.State, []string) commands.Result {
			return commands.Panel("panel", false, []commands.Row{
				{
					Text:        "outer",
					ActionLabel: "run",
					Action: func(*session.State) commands.Result {
						return commands.Text("outer ran")
					},
				},
				{
					Text: "drill",
					Children: []commands.Row{{
						Text:        "inner",
						ActionLabel: "run",
						Action: func(st *session.State) commands.Result {
							innerState = st
							return commands.Text("inner ran")
						},
					}},
				},
			})
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, sess := New(Deps{State: state, Commands: reg})

	res, err := sess.Command(context.Background(), "panel", nil)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.Doc == nil || len(res.Doc.Rows) != 2 {
		t.Fatalf("Doc = %+v, want two rows", res.Doc)
	}
	if got := res.Doc.Rows[0].ActionID; got != "a1" {
		t.Errorf("outer row ActionID = %q, want a1", got)
	}
	children := res.Doc.Rows[1].Children
	if len(children) != 1 {
		t.Fatalf("drill row has %d children, want 1", len(children))
	}
	if got := children[0].ActionID; got != "a2" {
		t.Errorf("inner row ActionID = %q, want a2", got)
	}

	out, err := sess.CommandAction(context.Background(), "a2")
	if err != nil {
		t.Fatalf("CommandAction(a2): %v", err)
	}
	if out.Text != "inner ran" {
		t.Errorf("CommandAction(a2).Text = %q, want %q", out.Text, "inner ran")
	}
	if innerState != state {
		t.Errorf("action state = %p, want the session's %p", innerState, state)
	}
}

// TestAnActionsResultIsMintedToo checks an action's own result mints its rows'
// actions, so a panel can drill in as deep as it likes.
func TestAnActionsResultIsMintedToo(t *testing.T) {
	state := newState(t, "s1")
	var deepState *session.State
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "panel",
		Handler: func(*session.State, []string) commands.Result {
			return commands.Panel("first", false, []commands.Row{{
				Text:        "open",
				ActionLabel: "open",
				Action: func(*session.State) commands.Result {
					return commands.Panel("second", false, []commands.Row{{
						Text:        "deeper",
						ActionLabel: "run",
						Action: func(st *session.State) commands.Result {
							deepState = st
							return commands.Text("deep ran")
						},
					}})
				},
			}})
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, sess := New(Deps{State: state, Commands: reg})

	res, err := sess.Command(context.Background(), "panel", nil)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.Doc == nil || len(res.Doc.Rows) != 1 {
		t.Fatalf("Doc = %+v, want one row", res.Doc)
	}
	outer := res.Doc.Rows[0].ActionID
	if outer != "a1" {
		t.Fatalf("outer ActionID = %q, want a1", outer)
	}

	second, err := sess.CommandAction(context.Background(), outer)
	if err != nil {
		t.Fatalf("CommandAction(%s): %v", outer, err)
	}
	if second.Doc == nil || len(second.Doc.Rows) != 1 {
		t.Fatalf("second Doc = %+v, want one row", second.Doc)
	}
	inner := second.Doc.Rows[0].ActionID
	if inner == "" {
		t.Fatalf("the action's own result minted no ID")
	}
	if inner == outer {
		t.Errorf("inner ActionID = %q, want a new ID", inner)
	}

	out, err := sess.CommandAction(context.Background(), inner)
	if err != nil {
		t.Fatalf("CommandAction(%s): %v", inner, err)
	}
	if out.Text != "deep ran" {
		t.Errorf("Text = %q, want %q", out.Text, "deep ran")
	}
	if deepState != state {
		t.Errorf("action state = %p, want the session's %p", deepState, state)
	}
}

// TestOldActionsExpire checks the session keeps only the newest results' actions:
// an ID past the bound comes back stale instead of running.
func TestOldActionsExpire(t *testing.T) {
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "counter",
		Handler: func(*session.State, []string) commands.Result {
			return commands.Panel("counter", false, []commands.Row{{
				Text:        "run",
				ActionLabel: "run",
				Action:      func(*session.State) commands.Result { return commands.Text("ran") },
			}})
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, sess := New(Deps{State: newState(t, "s1"), Commands: reg})

	ids := make([]string, 0, maxActionResults+1)
	for i := 0; i < maxActionResults+1; i++ {
		res, err := sess.Command(context.Background(), "counter", nil)
		if err != nil {
			t.Fatalf("Command %d: %v", i, err)
		}
		if res.Doc == nil || len(res.Doc.Rows) != 1 {
			t.Fatalf("Command %d returned no action row", i)
		}
		ids = append(ids, res.Doc.Rows[0].ActionID)
	}

	if _, err := sess.CommandAction(context.Background(), ids[0]); !errors.Is(err, errStaleAction) {
		t.Errorf("CommandAction(%s) = %v, want errStaleAction", ids[0], err)
	}
	newest := ids[len(ids)-1]
	res, err := sess.CommandAction(context.Background(), newest)
	if err != nil {
		t.Fatalf("CommandAction(%s), the newest ID: %v", newest, err)
	}
	if res.Text != "ran" {
		t.Errorf("CommandAction(%s).Text = %q, want %q", newest, res.Text, "ran")
	}
}

// TestCommandEdgeCases pins the three ways a command call refuses or returns
// nothing: a TUI-only command has no handler to run, an unknown name is an
// error, and a session without a registry runs nothing.
func TestCommandEdgeCases(t *testing.T) {
	reg := commands.New()
	if err := reg.Register(commands.Command{Name: "trust", TUIOnly: true}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, sess := New(Deps{State: newState(t, "s1"), Commands: reg})

	// A TUI-only command without a handler is a client's business, not the
	// session's: no output, no error.
	res, err := sess.Command(context.Background(), "trust", nil)
	if err != nil {
		t.Fatalf("Command(trust) = %v, want no error", err)
	}
	if !reflect.DeepEqual(res, acpwire.CommandResult{}) {
		t.Errorf("Command(trust) = %+v, want an empty result", res)
	}

	if _, err := sess.Command(context.Background(), "nope", nil); err == nil || !strings.Contains(err.Error(), "/nope") {
		t.Errorf("Command(nope) = %v, want an error naming /nope", err)
	}

	_, bare := New(Deps{State: newState(t, "s2")})
	if _, err := bare.Command(context.Background(), "trust", nil); !errors.Is(err, errNoCommands) {
		t.Errorf("Command on a session without a registry = %v, want errNoCommands", err)
	}
}
