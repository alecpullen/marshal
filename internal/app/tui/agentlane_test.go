package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/tools/native"
)

// registerRunningSubagent registers a running background child on the test
// model's session. The lane only tracks views with a live Child state;
// pipeline/SDD cards (Child == nil) are already pinned by the run panel.
func registerRunningSubagent(t *testing.T, m *Model, label string) {
	t.Helper()
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagent(label, child)
}

func TestAgentLaneEmptyWithNoRunningSubagents(t *testing.T) {
	m := newTestModel(t)
	if got := m.renderAgentLane(); got != "" {
		t.Fatalf("no subagents must render nothing, got %q", got)
	}
	registerRunningSubagent(t, &m, "review")
	v := m.state.Subagents()[0]
	m.state.FinishSubagent(v.ID, "done", nil)
	if got := m.renderAgentLane(); got != "" {
		t.Fatalf("finished subagents must not render, got %q", got)
	}
}

func TestAgentLaneShowsRunningSubagents(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	plain := ansi.Strip(m.renderAgentLane())
	for _, want := range []string{"2 agents", "tests", "review"} {
		if !strings.Contains(plain, want) {
			t.Errorf("lane missing %q:\n%s", want, plain)
		}
	}
}

func TestAgentLaneSkipsPipelineCards(t *testing.T) {
	m := newTestModel(t)
	m.state.RegisterSubagent("pipeline role", nil) // Child nil: pipeline/SDD shares the parent state
	if got := m.renderAgentLane(); got != "" {
		t.Fatalf("pipeline cards are pinned by the run panel, not the lane; got %q", got)
	}
}

// agentLaneRows must equal what the lane actually renders, or the height
// budget drifts and pushes the input area off the bottom of the frame.
func TestAgentLaneRowsMatchesRender(t *testing.T) {
	m := newTestModel(t)
	for _, n := range []int{0, 1, 2, 4, 9} {
		for _, v := range m.state.Subagents() {
			m.state.FinishSubagent(v.ID, "", nil)
		}
		for i := 0; i < n; i++ {
			registerRunningSubagent(t, &m, "task")
		}
		out := m.renderAgentLane()
		want := 0
		if out != "" {
			want = strings.Count(out, "\n")
		}
		if got := m.agentLaneRows(); got != want {
			t.Fatalf("%d running: agentLaneRows()=%d but lane rendered %d rows:\n%s", n, got, want, out)
		}
	}
}

func TestAgentLaneCapsWithOverflowRow(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 9; i++ {
		registerRunningSubagent(t, &m, "task")
	}
	out := m.renderAgentLane()
	// The lane carries a header and a divider rule, so a full lane is the
	// capped row budget plus one.
	if got := strings.Count(out, "\n"); got > agentLaneMaxRows+1 {
		t.Fatalf("lane rendered %d rows, cap is %d", got, agentLaneMaxRows+1)
	}
	if !strings.Contains(ansi.Strip(out), "more") {
		t.Fatalf("expected an overflow row:\n%s", ansi.Strip(out))
	}
}

func TestAgentLaneHasSeparatorAndRail(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "reviewer")
	out := m.renderAgentLane()
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// Header first, then the divider rule, then the agent rows.
	if !strings.Contains(ansi.Strip(rows[0]), "1 agent") {
		t.Fatalf("lane must open with the header, got %q", ansi.Strip(rows[0]))
	}
	if !strings.Contains(ansi.Strip(rows[1]), "─") {
		t.Fatalf("lane must carry a separator rule after the header, got %q", ansi.Strip(rows[1]))
	}
	for i, r := range rows[2:] {
		if !strings.Contains(ansi.Strip(r), glyph.Rail) {
			t.Errorf("lane row %d has no rail: %q", i+2, ansi.Strip(r))
		}
	}
}

// The height budget must still match exactly, or the input area is pushed
// off the bottom of the frame.
func TestAgentLaneRowsMatchesRenderAfterChrome(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 5; i++ {
		registerRunningSubagent(t, &m, fmt.Sprintf("agent-%d", i))
	}
	out := m.renderAgentLane()
	want := 0
	if out != "" {
		want = strings.Count(out, "\n")
	}
	if got := m.agentLaneRows(); got != want {
		t.Fatalf("agentLaneRows()=%d but lane rendered %d rows:\n%s", got, want, ansi.Strip(out))
	}
}

func TestAgentLaneEmptyHasNoSeparator(t *testing.T) {
	m := newTestModel(t)
	if out := m.renderAgentLane(); out != "" {
		t.Fatalf("no running agents must render nothing, got %q", out)
	}
	if got := m.agentLaneRows(); got != 0 {
		t.Fatalf("agentLaneRows()=%d with no agents, want 0", got)
	}
}

// The lane and the todo panel sit directly on top of each other; their
// rails must land in the same column or the stack looks broken.
func TestAgentLaneRailAlignsWithTodoPanel(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "reviewer")
	todos := []native.TodoItem{{Content: "a task", Status: native.TodoInProgress}}
	if err := m.state.SetTodos(todos); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}

	laneRows := strings.Split(strings.TrimRight(m.renderAgentLane(), "\n"), "\n")
	todoRows := strings.Split(strings.TrimRight(m.renderTodoPanel(), "\n"), "\n")
	if len(laneRows) == 0 || len(todoRows) == 0 {
		t.Fatal("expected both panels to render")
	}
	railCol := func(s string) int { return strings.Index(ansi.Strip(s), glyph.Rail) }
	// Compare the last lane row against the last todo row: both are body
	// rows, so any difference is real misalignment rather than a header.
	want := railCol(todoRows[len(todoRows)-1])
	got := railCol(laneRows[len(laneRows)-1])
	if want < 0 || got != want {
		t.Fatalf("lane rail at column %d, todo panel rail at column %d", got, want)
	}
}

// The divider must not break the vertical rail.
func TestLaneSeparatorBridgesTheRail(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "reviewer")
	rows := strings.Split(strings.TrimRight(m.renderAgentLane(), "\n"), "\n")
	sep := ansi.Strip(rows[1])
	if !strings.HasPrefix(sep, glyph.Rail) {
		t.Fatalf("separator must start with the rail so the vertical line is continuous, got %q", sep)
	}
	if !strings.Contains(sep, "─") {
		t.Fatalf("separator must still carry the rule, got %q", sep)
	}
}

func TestAgentLaneShowsSpinnerWhileRunning(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "reviewer")
	m.spinnerFrame = "⠋"
	if !strings.Contains(ansi.Strip(m.renderAgentLane()), "⠋") {
		t.Fatalf("a running lane must show the spinner:\n%s", ansi.Strip(m.renderAgentLane()))
	}
}

// The lane renders header first, then the divider rule, then the agent
// rows. The rule must sit between the header and the first "#"-prefixed row.
func TestAgentLaneStructureHeaderThenRuleThenRows(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	plain := ansi.Strip(m.renderAgentLane())
	if !strings.Contains(plain, "2 agents") {
		t.Fatalf("header must be count-first and pluralized, got:\n%s", plain)
	}
	// Rows carry #-prefixed ids (the ids are global sequence numbers, so
	// only the "#" prefix is stable across runs).
	if !strings.Contains(plain, "#") {
		t.Fatalf("rows must carry #-prefixed ids, got:\n%s", plain)
	}
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	headerIdx, ruleIdx, firstRowIdx := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "2 agents"):
			headerIdx = i
		case strings.Contains(l, "─"):
			ruleIdx = i
		case strings.Contains(l, "#"):
			if firstRowIdx < 0 {
				firstRowIdx = i
			}
		}
	}
	if headerIdx < 0 || ruleIdx < 0 || firstRowIdx < 0 {
		t.Fatalf("lane missing header/rule/row:\n%s", plain)
	}
	if !(headerIdx < ruleIdx && ruleIdx < firstRowIdx) {
		t.Fatalf("expected header < rule < first row, got header=%d rule=%d row=%d:\n%s",
			headerIdx, ruleIdx, firstRowIdx, plain)
	}
}

// The lane's rendered line count must always equal agentLaneRows(), which
// the frame height budget relies on.
func TestAgentLaneRowsEqualsRenderedLineCount(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	out := m.renderAgentLane()
	// Count every newline including the trailing one, matching the existing
	// TestAgentLaneRowsMatchesRender convention.
	lines := strings.Count(out, "\n")
	if got := m.agentLaneRows(); got != lines {
		t.Fatalf("agentLaneRows()=%d but lane rendered %d lines:\n%s", got, lines, ansi.Strip(out))
	}
}

// F6: with an empty input and running children, Down moves the lane cursor
// and arms it; Enter then drills into the selected subagent.
func TestLaneCursorDownThenEnterDrills(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.laneCursor != 1 {
		t.Fatalf("laneCursor = %d, want 1 after two Downs", m.laneCursor)
	}
	if !m.laneCursorActive {
		t.Fatal("laneCursorActive must be set after navigating the lane")
	}

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.viewStack) != 1 {
		t.Fatalf("Enter must drill into the selected subagent, viewStack=%d", len(m.viewStack))
	}
	if m.laneCursor != 0 || m.laneCursorActive {
		t.Fatalf("after drill laneCursor=%d active=%v, want 0/false", m.laneCursor, m.laneCursorActive)
	}
}

// F6: Down clamps at the last lane row rather than wrapping.
func TestLaneCursorClampsAtLastRow(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")

	for i := 0; i < 5; i++ {
		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.laneCursor != 1 {
		t.Fatalf("laneCursor = %d, want 1 (clamped at last row)", m.laneCursor)
	}
}

// F6: a blank Enter with no lane navigation must keep the existing
// steering-drain behavior and must not drill.
func TestLaneCursorBlankEnterPreservesSteeringDrain(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	m.state.PushSteering("first follow-up")
	m.state.PushSteering("second follow-up")
	m.queuedCount = 2

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.viewStack) != 0 {
		t.Fatalf("blank Enter must not drill, viewStack=%d", len(m.viewStack))
	}
	if m.laneCursorActive {
		t.Fatal("laneCursorActive must stay false without lane navigation")
	}
	if len(m.state.SteeringQueue()) != 1 {
		t.Fatalf("steering queue = %v, want 1 remaining (drain preserved)", m.state.SteeringQueue())
	}
	if m.state.SteeringQueue()[0] != "second follow-up" {
		t.Fatalf("remaining = %q, want %q", m.state.SteeringQueue()[0], "second follow-up")
	}
	messages := m.state.Messages()
	if len(messages) != 1 || messages[0].Content != "first follow-up" {
		t.Fatalf("follow-up not submitted; messages = %v", messages)
	}
}
