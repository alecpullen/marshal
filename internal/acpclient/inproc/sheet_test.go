package inproc

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/tools/registry"
)

// TestSheetCarriesTheSessionsActivity pins every in-memory value the sheet
// reads: the tool-call log, the files it touched, the rules, the skills, the
// budget, the loaded tools, the sandbox and the context pack.
func TestSheetCarriesTheSessionsActivity(t *testing.T) {
	state := newState(t, "s1")
	state.LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(10, 0),
		ToolName:  "file.read",
		Args:      json.RawMessage(`{"path":"a.go"}`),
		Duration:  10 * time.Millisecond,
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp:    time.Unix(20, 0),
		ToolName:     "file.write_patch",
		FilesChanged: []string{"b.go"},
		Duration:     30 * time.Millisecond,
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(30, 0),
		ToolName:  "shell.run",
		Error:     "exit 1",
		Duration:  50 * time.Millisecond,
	})
	state.AddSessionRule("go test")
	state.ActivateSkill("tdd")
	state.SetToolBudget(session.ToolBudget{Used: 3, Max: 100})
	state.AddLoadedToolNames([]string{"web.fetch"})
	state.SetSandboxInfo(session.SandboxInfo{Backend: "restricted"})
	state.Config.Tools.Shell.AllowNetwork = true
	state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 3400},
		Sections: []contextpack.Section{
			{Title: "repo map", EstimatedTokens: 2400},
			{Title: "todos"},
		},
	})

	_, sess := New(Deps{State: state})
	got := sess.Sheet(0)
	want := acpwire.SessionSheet{
		StartedAt: 100000,
		Context: acpwire.SheetContext{
			MaxTokens:       12000,
			EstimatedTokens: 3400,
			Sections: []acpwire.SheetContextSection{
				{Title: "repo map", EstimatedTokens: 2400},
				{Title: "todos"},
			},
		},
		Tools: []acpwire.SheetTool{
			{Name: "file.read", Calls: 1, SlowestMs: 10},
			{Name: "file.write_patch", Calls: 1, SlowestMs: 30},
			{Name: "shell.run", Calls: 1, Errors: 1, SlowestMs: 50},
		},
		Files: []acpwire.SheetFile{
			{Path: "b.go", Edits: 1, LastAt: 20000},
			{Path: "a.go", Reads: 1, LastAt: 10000},
		},
		Rules:       []string{"go test"},
		Skills:      []string{"tdd"},
		ToolBudget:  acpwire.SheetToolBudget{Used: 3, Max: 100},
		LoadedTools: 1,
		Sandbox:     acpwire.SheetSandbox{Backend: "restricted", AllowNetwork: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sheet(0) =\n%+v\nwant\n%+v", got, want)
	}
}

// TestSheetReadsASubagentsOwnActivity pins that a subagent's ID reads the
// child's activity, not the parent's, and that only the session's own sheet
// carries a context pack.
func TestSheetReadsASubagentsOwnActivity(t *testing.T) {
	parent := newState(t, "parent")
	parent.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "parent-probe"})
	parent.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 100},
		Sections:   []contextpack.Section{{Title: "repo map", EstimatedTokens: 100}},
	})

	child := session.New(config.Default(), t.TempDir(), time.Unix(200, 0), session.Persistence{SessionID: "child"})
	child.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(20, 0), ToolName: "child-probe"})
	child.AddSessionRule("child rule")
	view := parent.RegisterSubagent("scout", child)

	_, sess := New(Deps{State: parent})
	got := sess.Sheet(view.ID)

	if got.SubagentID != view.ID {
		t.Errorf("SubagentID = %d, want the view's %d", got.SubagentID, view.ID)
	}
	if want := []acpwire.SheetTool{{Name: "child-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
		t.Errorf("Tools = %+v, want only the child's %+v", got.Tools, want)
	}
	if want := []string{"child rule"}; !reflect.DeepEqual(got.Rules, want) {
		t.Errorf("Rules = %v, want %v", got.Rules, want)
	}
	if got.StartedAt != 200000 {
		t.Errorf("StartedAt = %d, want the child's 200000", got.StartedAt)
	}
	if !reflect.DeepEqual(got.Context, acpwire.SheetContext{}) {
		t.Errorf("Context = %+v, want empty for a subagent", got.Context)
	}
}

// TestSheetFindsANestedSubagent pins that the search descends into a child's
// own subagents.
func TestSheetFindsANestedSubagent(t *testing.T) {
	parent := newState(t, "parent")
	child := newState(t, "child")
	parent.RegisterSubagent("scout", child)

	grandchild := newState(t, "grandchild")
	grandchild.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(30, 0), ToolName: "grandchild-probe"})
	grandView := child.RegisterSubagent("scout-of-scout", grandchild)

	_, sess := New(Deps{State: parent})
	got := sess.Sheet(grandView.ID)

	if got.SubagentID != grandView.ID {
		t.Errorf("SubagentID = %d, want the grandchild's %d", got.SubagentID, grandView.ID)
	}
	if want := []acpwire.SheetTool{{Name: "grandchild-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
		t.Errorf("Tools = %+v, want the grandchild's %+v", got.Tools, want)
	}
}

// TestSheetReadsAnUnknownSubagentAsTheSession pins that an ID naming no child
// session, and a card registered with no child, both read as the session's own
// sheet.
func TestSheetReadsAnUnknownSubagentAsTheSession(t *testing.T) {
	parent := newState(t, "parent")
	parent.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "parent-probe"})
	card := parent.RegisterSubagent("pipeline", nil)

	_, sess := New(Deps{State: parent})
	for _, id := range []int64{999999, card.ID} {
		got := sess.Sheet(id)
		if got.SubagentID != 0 {
			t.Errorf("Sheet(%d).SubagentID = %d, want 0", id, got.SubagentID)
		}
		if want := []acpwire.SheetTool{{Name: "parent-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
			t.Errorf("Sheet(%d).Tools = %+v, want the parent's %+v", id, got.Tools, want)
		}
	}
}

// TestSheetOfAReplacedSessionKeepsItsFinalValues pins C4 for the sheet: a
// session a swap replaced keeps answering with its own state's values.
func TestSheetOfAReplacedSessionKeepsItsFinalValues(t *testing.T) {
	oldState := newState(t, "s1")
	oldState.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "final-probe"})
	h, old := New(Deps{State: oldState})
	h.Swap(newState(t, "s2"))

	got := old.Sheet(0)
	if want := []acpwire.SheetTool{{Name: "final-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
		t.Errorf("closed session Sheet(0).Tools = %+v, want its final %+v", got.Tools, want)
	}
}
