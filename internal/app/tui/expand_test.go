package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

func TestIsExpandedFollowsExpandAllUntilToggled(t *testing.T) {
	m := newTestModel(t)
	key := thinkID(time.Unix(100, 0))

	if m.isExpanded(key) {
		t.Fatal("expected collapsed by default")
	}

	m.expandAll = true
	if !m.isExpanded(key) {
		t.Fatal("expected expanded once expand-all is on")
	}

	m.toggleExpanded(key)
	if m.isExpanded(key) {
		t.Fatal("expected the node's own choice to win over expand-all")
	}

	m.expandAll = false
	if m.isExpanded(key) {
		t.Fatal("expected the node's own choice (still collapsed) to persist")
	}
}

func TestCtrlGClearsPerNodeChoices(t *testing.T) {
	m := newTestModel(t)
	key := thinkID(time.Unix(100, 0))
	m.toggleExpanded(key) // collapsed -> expanded
	if !m.isExpanded(key) {
		t.Fatal("precondition: the choice should read expanded")
	}

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+g was not handled")
	}
	mm := asModel(t, updated)

	// Expand-all turned on and the choice was cleared, so the node simply
	// follows expand-all.
	if !mm.expandAll || !mm.isExpanded(key) {
		t.Fatal("expected the node to follow expand-all")
	}
	if len(mm.expanded) != 0 {
		t.Fatalf("expanded = %v, want cleared", mm.expanded)
	}
}

func TestCtrlGClearsActiveToolChoices(t *testing.T) {
	m := newTestModel(t)
	keyA := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:a"}
	keyB := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:b"}
	m.setExpanded(keyA, true)
	m.setExpanded(keyB, true)
	if !m.isToolExpanded(keyA, true) || !m.isToolExpanded(keyB, true) {
		t.Fatal("precondition: both choices should be set")
	}

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+g was not handled")
	}
	mm := asModel(t, updated)

	if len(mm.expanded) != 0 {
		t.Fatalf("expanded = %v, want cleared", mm.expanded)
	}
	// A running call ignores expand-all: it stays collapsed.
	if mm.isToolExpanded(keyA, true) || mm.isToolExpanded(keyB, true) {
		t.Fatal("expected all active-tool choices to be cleared")
	}
}

// Expanding a beat opens every row in it that has no choice of its own; a
// row's own choice still wins.
func TestExpandingABeatOpensItsRows(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 60)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Reading two files.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: "a",
		Args: []byte(`{"path":"a.go"}`), ResultSummary: "1 line", ResultContent: "alpha body"})
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "git.status", StepID: id, ToolCallID: "b",
		ResultSummary: "clean", ResultContent: "beta body"})
	m.state.EndStep(id)
	m.refreshViewport()
	if text := viewText(&m); strings.Contains(text, "alpha body") || strings.Contains(text, "beta body") {
		t.Fatalf("rows start collapsed:\n%s", text)
	}

	beat := viewmodel.NodeID{Kind: viewmodel.KindStep, Key: "step:1"}
	m.toggleExpanded(beat)
	m.invalidateTranscript()
	if text := viewText(&m); !strings.Contains(text, "alpha body") || !strings.Contains(text, "beta body") {
		t.Fatalf("an expanded beat opens its rows:\n%s", text)
	}

	m.toggleExpanded(viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:1:a"})
	m.invalidateTranscript()
	if text := viewText(&m); strings.Contains(text, "alpha body") || !strings.Contains(text, "beta body") {
		t.Fatalf("collapsing one row leaves the others open:\n%s", text)
	}
}

// A beat's reasoning shows when that beat is expanded, and only that beat's.
func TestRefreshViewportShowsReasoningPerBeat(t *testing.T) {
	m := newTestModel(t)
	ts1 := time.Unix(300, 0)
	ts2 := time.Unix(301, 0)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	for i, ts := range []time.Time{ts1, ts2} {
		id := m.state.BeginStep(session.Actor{})
		m.state.AddNarration(id, fmt.Sprintf("Beat %d.", i+1))
		m.state.LogThinking(session.ThinkingEntry{Text: []string{"reasoning one", "reasoning two"}[i], Duration: time.Second, StartedAt: ts, StepID: id})
		m.state.EndStep(id)
	}
	m.invalidateTranscript()
	m.refreshViewport()

	content := m.viewport.GetContent()
	if strings.Contains(content, "reasoning one") || strings.Contains(content, "reasoning two") {
		t.Fatalf("expected both beats' reasoning collapsed by default, got: %s", content)
	}

	m.toggleExpanded(beatWithThinking(&m, ts1))
	m.invalidateTranscript()
	m.refreshViewport()

	content = m.viewport.GetContent()
	if !strings.Contains(content, "reasoning one") {
		t.Fatal("expected the clicked item's reasoning to be visible")
	}
	if strings.Contains(content, "reasoning two") {
		t.Fatal("expected the other item to remain collapsed")
	}
}
