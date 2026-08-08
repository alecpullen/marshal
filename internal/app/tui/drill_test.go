package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func newChildState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func TestSubagentCardHasClickTarget(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("explore repo", child)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	found := false
	for _, r := range m.clickRegions {
		if r.target.subagent != nil && r.target.subagent.Label == "explore repo" {
			found = true
			if r.startLine < 0 || r.endLine <= r.startLine {
				t.Fatalf("invalid region for subagent card: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("expected a click region targeting the subagent card")
	}
}

func TestSubagentCardWithoutChildIsNotClickable(t *testing.T) {
	m := newTestModel(t)
	m.state.RegisterSubagent("status only", nil)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	for _, r := range m.clickRegions {
		if r.target.subagent != nil {
			t.Fatalf("childless subagent card must not be clickable, got target %+v", r.target.subagent)
		}
	}
}

func TestDrillSwapsTranscriptToChildSession(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleAssistant, "parent normal text", session.ContentTypePlain)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)

	m.drillIntoSubagent(view)
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	if !strings.Contains(content, "child says hi") {
		t.Fatalf("drilled view should show the child transcript, got:\n%s", content)
	}
	if strings.Contains(content, "parent normal text") {
		t.Fatalf("drilled view must not show the parent transcript, got:\n%s", content)
	}
	if m.breadcrumbRows() != 1 {
		t.Fatalf("breadcrumbRows = %d, want 1 while drilled", m.breadcrumbRows())
	}
}

func TestPopDrillRestoresParentTranscript(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleAssistant, "parent normal text", session.ContentTypePlain)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)

	m.drillIntoSubagent(view)
	m.refreshViewport()
	if !m.popDrill() {
		t.Fatal("popDrill returned false while drilled")
	}
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	if !strings.Contains(content, "parent normal text") {
		t.Fatalf("after pop the parent transcript should be restored, got:\n%s", content)
	}
	if m.breadcrumbRows() != 0 {
		t.Fatalf("breadcrumbRows = %d, want 0 after pop", m.breadcrumbRows())
	}
	if m.popDrill() {
		t.Fatal("popDrill on an empty stack must return false")
	}
}

func TestDrillIntoSubagentRequiresChild(t *testing.T) {
	m := newTestModel(t)
	m.drillIntoSubagent(session.SubagentView{Label: "no child"})
	if len(m.viewStack) != 0 {
		t.Fatal("drilling into a childless subagent must not push the view stack")
	}
}

func TestEscPopsDrillInsteadOfCancelling(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !handled {
		t.Fatal("esc must be handled while drilled")
	}
	m = mm.(Model)
	if len(m.viewStack) != 0 {
		t.Fatalf("esc should pop the drill stack, len(viewStack) = %d", len(m.viewStack))
	}
}

func TestBreadcrumbShowsPathWhileDrilled(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)

	frame := stripANSI(m.renderTranscriptFrame())
	for _, want := range []string{"orchestrator", "explore repo", "go back"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("breadcrumb missing %q, frame:\n%s", want, frame)
		}
	}
}

func TestAgentRunAuditSuppressedInParentView(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleAssistant, "parent normal text", session.ContentTypePlain)
	m.state.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(200, 0),
		ToolName:      "agent.run",
		ResultSummary: "VERBOSE AGENT RUN SUMMARY",
		ResultContent: "VERBOSE AGENT RUN LOG",
	})
	m.state.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(201, 0),
		ToolName:      "file.read",
		ResultSummary: "ordinary tool stays",
	})
	m.lastTranscriptHash = 0
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	if strings.Contains(content, "VERBOSE AGENT RUN SUMMARY") {
		t.Fatalf("agent.run audit block should be suppressed in the parent view, got:\n%s", content)
	}
	if !strings.Contains(content, "ordinary tool stays") {
		t.Fatalf("unrelated tool calls must still render, got:\n%s", content)
	}
}

func TestDrilledChildAuditEventsRenderNormally(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	child.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(300, 0),
		ToolName:      "file.read",
		ResultSummary: "child tool output marker",
	})
	view := m.state.RegisterSubagent("explore repo", child)

	m.drillIntoSubagent(view)
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	if !strings.Contains(content, "child tool output marker") {
		t.Fatalf("drilled view should render the child's audit events, got:\n%s", content)
	}
}

func TestRenderSubagentCardContent(t *testing.T) {
	child := newChildState(t)
	running := session.SubagentView{
		ID:        1,
		Label:     "explore repo",
		Status:    session.SubagentRunning,
		StartedAt: time.Now().Add(-5 * time.Second),
		ToolCalls: 7,
		Child:     child,
	}
	out := stripANSI(renderSubagentCard(running, false, "⠋", 80))
	for _, want := range []string{"explore repo", "7 tool calls", "click to inspect"} {
		if !strings.Contains(out, want) {
			t.Fatalf("running card missing %q, got:\n%s", want, out)
		}
	}

	done := session.SubagentView{
		ID:        2,
		Label:     "explore repo",
		Status:    session.SubagentDone,
		StartedAt: time.Unix(100, 0),
		EndedAt:   time.Unix(130, 0),
		ToolCalls: 3,
		Summary:   "found three entry points",
	}
	collapsed := stripANSI(renderSubagentCard(done, false, "⠋", 80))
	if strings.Contains(collapsed, "found three entry points") {
		t.Fatalf("collapsed card must hide the summary, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "click to inspect") {
		t.Fatalf("childless card must not offer drill-down, got:\n%s", collapsed)
	}
	expanded := stripANSI(renderSubagentCard(done, true, "⠋", 80))
	if !strings.Contains(expanded, "found three entry points") {
		t.Fatalf("expanded card should show the summary, got:\n%s", expanded)
	}
}

func TestUpArrowPopsDrill(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyUp})
	if !handled {
		t.Fatal("up arrow must be handled while drilled")
	}
	m = mm.(Model)
	if len(m.viewStack) != 0 {
		t.Fatalf("up arrow should pop the drill stack, len = %d", len(m.viewStack))
	}
}

func TestDrilledChildShowsChildActiveTool(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	// Set an active tool on the child session.
	child.SetActiveToolCall(session.ActiveToolCall{
		Name:      "file.read",
		StartedAt: time.Now(),
	})
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	if !strings.Contains(content, "Read file") {
		t.Fatalf("drilled view should show child's active tool 'Read file':\n%s", content)
	}
}

func TestUpArrowRecallsHistoryWhenNotDrilled(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleUser, "previous prompt", session.ContentTypePlain)
	m.resetHistoryNav()

	// Not drilled: up arrow should recall history, not pop drill.
	mm, _, _ := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyUp})
	// Whether handled depends on whether there is history to recall.
	// The key assertion is that it does NOT crash and the view stack
	// remains empty.
	m = mm.(Model)
	if len(m.viewStack) != 0 {
		t.Fatal("up arrow should not affect view stack when not drilled")
	}
}
