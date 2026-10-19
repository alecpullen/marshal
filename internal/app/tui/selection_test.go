package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// selectionFixture is a two-turn transcript with every row shape the copier
// handles: a wrapped prompt, a markdown answer with a list and code, a turn
// separator, a narrated step with thinking, file, group and failed shell
// rows, a subagent card and a notice.
func selectionFixture(t *testing.T) (Model, *fakeClipboard) {
	t.Helper()
	m := newTestModel(t)
	m.resize(80, 80)
	clip := &fakeClipboard{available: true}
	m.copyWriter = clip
	m.copyRemote = func() bool { return false }
	m.state.AddMessage(session.RoleUser, "first line of the prompt\nsecond line, indented below", session.ContentTypePlain)
	m.state.AddMessageFinal(session.RoleAssistant, "Summary:\n\n1. raised the timeout\n   - nested item\n\n```go\nfunc g() {\n\tif x {\n\t\treturn\n\t}\n}\n```", session.ContentTypeMarkdown)
	m.state.AddMessage(session.RoleUser, "next turn", session.ContentTypePlain)
	ts := time.Now()
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Looking at the failing test. Then:\n\n- check the fixture\n\n```go\nfunc f() {\n    return\n}\n```")
	m.state.LogThinking(session.ThinkingEntry{Text: "private reasoning text", Duration: 2 * time.Second, StartedAt: ts, StepID: id})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: id, ToolCallID: "c1", Timestamp: ts.Add(1 * time.Millisecond), Args: []byte(`{"path":"a.go"}`), ResultContent: "package a", ResultSummary: "a.go (5 lines)", Approval: registry.ApprovalNotRequired})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "search.text", StepID: id, ToolCallID: "c2", Timestamp: ts.Add(2 * time.Millisecond), Args: []byte(`{"query":"Timeout"}`), ResultSummary: "3 matches"})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "search.text", StepID: id, ToolCallID: "c3", Timestamp: ts.Add(3 * time.Millisecond), Args: []byte(`{"query":"hookTimeout"}`), ResultSummary: "1 match"})
	code := 1
	m.state.LogToolCall(registry.AuditEvent{ToolName: "shell.run", StepID: id, ToolCallID: "c4", Timestamp: ts.Add(4 * time.Millisecond), Args: []byte(`{"command":"go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 -v -timeout 30s"}`), CommandExitCode: &code, Error: "exit status 1", ResultContent: "--- FAIL: TestX (0.01s)\n    hooks_test.go:12: timeout\nFAIL"})
	child := newChildState(t)
	child.AddNarration(child.BeginStep(session.Actor{}), "Tracing the config loader. It has three layers.")
	m.state.RegisterSubagent("reviewer", child)
	m.state.EndStep(id)
	m.state.AddMessage(session.RoleSystem, "Model switched to gpt-x\nsecond notice line", session.ContentTypePlain)
	m.invalidateTranscript()
	m.refreshViewport()
	return m, clip
}

// selectionFixtureCopy is what selecting the whole fixture copies.
const selectionFixtureCopy = `first line of the prompt
second line, indented below

Summary:

1. raised the timeout
  • nested item


  func g() {
          if x {
                  return
          }
  }

next turn

Looking at the failing test. Then:

• check the fixture

  func f() {
      return
  }

thought for 2s
Read file · a.go (5 lines)
Search texts: ×2 · Timeout, hookTimeout

go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 -v -timeout 30s · exit 1 · exit status 1
--- FAIL: TestX (0.01s)
    hooks_test.go:12: timeout
FAIL

reviewer
Tracing the config loader.

Model switched to gpt-x
second notice line`

// lineOf returns the first content line containing sub.
func lineOf(t *testing.T, m *Model, sub string) int {
	t.Helper()
	for i, l := range m.contentLines {
		if strings.Contains(stripANSI(l), sub) {
			return i
		}
	}
	t.Fatalf("no content line contains %q", sub)
	return -1
}

// selectRows selects from..to as a finished drag would: anchored, with the
// text it covers recorded as copied.
func selectRows(t *testing.T, m *Model, from, to selPos) {
	t.Helper()
	a, okA := m.anchorAt(from)
	h, okH := m.anchorAt(to)
	if !okA || !okH {
		t.Fatalf("no anchors for %+v..%+v", from, to)
	}
	m.sel = &selection{anchor: a, head: h}
	f, l, _ := m.selRange()
	m.sel.copied = m.selectedText(f, l)
}

// thinkKeyOf returns the node of the fixture's thinking row.
func thinkKeyOf(t *testing.T, m *Model) viewmodel.NodeID {
	t.Helper()
	l := lineOf(t, m, "thought for 2s")
	target, ok := m.regionAt(l)
	if !ok {
		t.Fatal("no region on the thinking row")
	}
	return target.node
}

func TestSelectedTextOfTheRenderedTranscript(t *testing.T) {
	m, _ := selectionFixture(t)
	selectRows(t, &m, selPos{0, 0}, selPos{len(m.contentLines) - 1, 79})
	if got := m.sel.copied; got != selectionFixtureCopy {
		t.Fatalf("copied:\n%s\n--- want:\n%s", got, selectionFixtureCopy)
	}
}

func TestPaintHighlightsOnlyCopiedCells(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	painted := strings.Split(m.paintSelection(m.viewport.View()), "\n")[l-m.viewport.YOffset()]
	if !strings.Contains(painted, "\x1b[7m") {
		t.Fatalf("selected row has no reverse-video run: %q", painted)
	}
	// The gutter glyph sits before the highlight.
	if i, j := strings.Index(painted, "·"), strings.Index(painted, "\x1b[7m"); i < 0 || i > j {
		t.Fatalf("gutter glyph is inside the highlight: %q", painted)
	}
}

func TestSelectionFollowsItsTextWhenRowsChangeAbove(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	copied := m.sel.copied
	// Expanding the thinking row adds rows above the selection.
	m.toggleExpanded(thinkKeyOf(t, &m))
	m.invalidateTranscript()
	m.refreshViewport()
	if m.sel == nil {
		t.Fatal("rows changing above cleared the selection")
	}
	from, to, _ := m.selRange()
	if got := m.selectedText(from, to); got != copied {
		t.Fatalf("selection now covers %q, want %q", got, copied)
	}
}

func TestSelectionClearsWhenItsTextChanges(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "thought for 2s")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m.toggleExpanded(thinkKeyOf(t, &m))
	m.invalidateTranscript()
	m.refreshViewport()
	if m.sel != nil {
		t.Fatal("expanding the selected row must clear the selection")
	}
}

func TestSelectionClearsWhenMouseCaptureTurnsOff(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m.state.Config.TUI.MouseCapture = false
	m.invalidateTranscript()
	m.refreshViewport()
	if m.sel != nil {
		t.Fatal("a selection must not outlive mouse capture")
	}
}
