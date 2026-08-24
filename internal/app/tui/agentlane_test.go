package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{"agents 2", "tests", "review"} {
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
	// The lane carries an opening separator, so a full lane is the capped
	// row budget plus one.
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
	if !strings.Contains(ansi.Strip(rows[0]), "─") {
		t.Fatalf("lane must open with a separator rule, got %q", ansi.Strip(rows[0]))
	}
	for i, r := range rows[1:] {
		if !strings.Contains(ansi.Strip(r), glyph.Rail) {
			t.Errorf("lane row %d has no rail: %q", i+1, ansi.Strip(r))
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
	sep := ansi.Strip(rows[0])
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
