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
	if got := m.renderActivityLane(); got != "" {
		t.Fatalf("no subagents must render nothing, got %q", got)
	}
	registerRunningSubagent(t, &m, "review")
	v := m.state.Subagents()[0]
	m.state.FinishSubagent(v.ID, "done", nil)
	if got := m.renderActivityLane(); got != "" {
		t.Fatalf("finished subagents must not render, got %q", got)
	}
}

func TestAgentLaneShowsRunningSubagents(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	plain := ansi.Strip(m.renderActivityLane())
	for _, want := range []string{"2 agents", "tests", "review"} {
		if !strings.Contains(plain, want) {
			t.Errorf("lane missing %q:\n%s", want, plain)
		}
	}
}

func TestAgentLaneSkipsPipelineCards(t *testing.T) {
	m := newTestModel(t)
	m.state.RegisterSubagent("pipeline role", nil) // Child nil: pipeline/SDD shares the parent state
	if got := m.renderActivityLane(); got != "" {
		t.Fatalf("pipeline cards are pinned by the run panel, not the lane; got %q", got)
	}
}

// laneRows must equal what the lane actually renders, or the height
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
		out := m.renderActivityLane()
		want := 0
		if out != "" {
			want = strings.Count(out, "\n")
		}
		if got := m.laneRows(); got != want {
			t.Fatalf("%d running: laneRows()=%d but lane rendered %d rows:\n%s", n, got, want, out)
		}
	}
}

func TestAgentLaneCapsWithOverflowRow(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 9; i++ {
		registerRunningSubagent(t, &m, "task")
	}
	out := m.renderActivityLane()
	if got := strings.Count(out, "\n"); got > laneMaxRows {
		t.Fatalf("lane rendered %d rows, cap is %d", got, laneMaxRows)
	}
	if !strings.Contains(ansi.Strip(out), "more") {
		t.Fatalf("expected an overflow row:\n%s", ansi.Strip(out))
	}
}

func TestAgentLaneHasSeparatorAndRail(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "reviewer")
	out := m.renderActivityLane()
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// Row 0 is the separator rule; row 1 the caption.
	sep := ansi.Strip(rows[0])
	if !strings.Contains(sep, "─") {
		t.Fatalf("lane must open with a separator rule, got %q", sep)
	}
	caption := ansi.Strip(rows[1])
	if !strings.Contains(caption, "1 agent") {
		t.Fatalf("caption must be count-first, got %q", caption)
	}
	if !strings.Contains(caption, "─") {
		t.Fatalf("caption must carry the divider rule on the same line, got %q", caption)
	}
	// Every row (including the separator and caption) carries the vertical rail.
	for i, r := range rows {
		if !strings.Contains(ansi.Strip(r), glyph.Rail) {
			t.Errorf("lane row %d has no rail: %q", i, ansi.Strip(r))
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
	out := m.renderActivityLane()
	want := 0
	if out != "" {
		want = strings.Count(out, "\n")
	}
	if got := m.laneRows(); got != want {
		t.Fatalf("laneRows()=%d but lane rendered %d rows:\n%s", got, want, ansi.Strip(out))
	}
}

func TestAgentLaneEmptyHasNoSeparator(t *testing.T) {
	m := newTestModel(t)
	if out := m.renderActivityLane(); out != "" {
		t.Fatalf("no running agents must render nothing, got %q", out)
	}
	if got := m.laneRows(); got != 0 {
		t.Fatalf("laneRows()=%d with no agents, want 0", got)
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

	laneRows := strings.Split(strings.TrimRight(m.renderActivityLane(), "\n"), "\n")
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

// The separator line's divider rule must not break the vertical rail.
func TestLaneSeparatorBridgesTheRail(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "reviewer")
	rows := strings.Split(strings.TrimRight(m.renderActivityLane(), "\n"), "\n")
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
	if !strings.Contains(ansi.Strip(m.renderActivityLane()), "⠋") {
		t.Fatalf("a running lane must show the spinner:\n%s", ansi.Strip(m.renderActivityLane()))
	}
}

// The lane renders a separator rule row, then a caption row, then the agent
// rows. The caption must carry both the count text and the rule; the first
// agent row follows on the next line.
func TestAgentLaneStructureHeaderThenRuleThenRows(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	plain := ansi.Strip(m.renderActivityLane())
	if !strings.Contains(plain, "2 agents") {
		t.Fatalf("header must be count-first and pluralized, got:\n%s", plain)
	}
	// Rows carry #-prefixed ids (the ids are global sequence numbers, so
	// only the "#" prefix is stable across runs).
	if !strings.Contains(plain, "#") {
		t.Fatalf("rows must carry #-prefixed ids, got:\n%s", plain)
	}
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	headerIdx, firstRowIdx := -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "2 agents"):
			headerIdx = i
		case strings.Contains(l, "#"):
			if firstRowIdx < 0 {
				firstRowIdx = i
			}
		}
	}
	if headerIdx < 0 || firstRowIdx < 0 {
		t.Fatalf("lane missing header/row:\n%s", plain)
	}
	// The rule lives on the separator line (row 0), not the caption line.
	if !strings.Contains(lines[0], "─") {
		t.Fatalf("separator line must carry the rule, got %q:\n%s", lines[0], plain)
	}
	// The first agent row must follow the header.
	if firstRowIdx <= headerIdx {
		t.Fatalf("expected header < first row, got header=%d row=%d:\n%s",
			headerIdx, firstRowIdx, plain)
	}
}

// The lane's rendered line count must always equal laneRows(), which
// the frame height budget relies on.
func TestAgentLaneRowsEqualsRenderedLineCount(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	out := m.renderActivityLane()
	// Count every newline including the trailing one, matching the existing
	// TestAgentLaneRowsMatchesRender convention.
	lines := strings.Count(out, "\n")
	if got := m.laneRows(); got != lines {
		t.Fatalf("laneRows()=%d but lane rendered %d lines:\n%s", got, lines, ansi.Strip(out))
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

// Review M2: typing any non-navigation key disarms the lane cursor, so a
// later blank Enter cannot drill from a stale cursor position.
func TestLaneCursorDisarmsOnTyping(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.laneCursorActive {
		t.Fatal("precondition: Down must arm the lane cursor")
	}

	// A printable key falls through to the textarea; it must disarm the
	// cursor on its way past.
	m = sendKey(m, tea.KeyPressMsg{Code: 'x'})
	if m.laneCursorActive {
		t.Fatal("typing must disarm laneCursorActive")
	}
	// And a subsequent blank Enter must not drill.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.viewStack) != 0 {
		t.Fatalf("stale cursor must not drill, viewStack=%d", len(m.viewStack))
	}
}

// The lane's separator and caption rows are built at full width and then
// re-truncated by chromeRailWidth to width-1, which ate the last cell of the
// rule and replaced it with an ellipsis. Assert the width arithmetic, not
// just the absence of "…", so this stays a guard against the off-by-one
// itself.
func TestAgentLaneHeaderFillsExactlyOneRow(t *testing.T) {
	for _, w := range []int{40, 60, 80, 100} {
		m := newTestModel(t)
		m.resize(w, 30)
		registerRunningSubagent(t, &m, "reviewer")

		out := m.renderActivityLane()
		if out == "" {
			t.Fatalf("w=%d: lane rendered nothing", w)
		}
		rows := strings.Split(out, "\n")
		// Row 0 is the separator, row 1 the caption.
		for i := 0; i < 2; i++ {
			if strings.Contains(rows[i], "…") {
				t.Errorf("w=%d: row %d truncated: %q", w, i, rows[i])
			}
			if got := ansi.StringWidth(rows[i]); got != m.leftWidth {
				t.Errorf("w=%d: row %d width = %d, want leftWidth %d", w, i, got, m.leftWidth)
			}
		}
	}
}

// The agent lane body rows must start their label text at the same column
// as the todo panel body rows, so the two panels look aligned when stacked.
func TestAgentLaneBodyTextAlignsWithTodoPanelBody(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	// A running subagent shows the spinner in its gutter; without it the
	// lane gutter collapses to 2 cells and cannot match the todo panel's
	// 3-cell gutter. Mirror TestAgentLaneShowsSpinnerWhileRunning.
	m.spinnerFrame = "⠋"
	registerRunningSubagent(t, &m, "reviewer")
	todos := []native.TodoItem{{Content: "a task", Status: native.TodoInProgress}}
	if err := m.state.SetTodos(todos); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}

	laneRows := strings.Split(strings.TrimRight(m.renderActivityLane(), "\n"), "\n")
	todoRows := strings.Split(strings.TrimRight(m.renderTodoPanel(), "\n"), "\n")
	if len(laneRows) < 3 || len(todoRows) < 2 {
		t.Fatalf("need at least 3 lane rows and 2 todo rows; lane=%d todo=%d", len(laneRows), len(todoRows))
	}

	// Find the text-start column (first non-space, non-rail char) in a body row.
	textStartCol := func(row string) int {
		s := ansi.Strip(row)
		// Skip the rail character (first non-space).
		col := 0
		// Skip leading spaces.
		for col < len(s) && s[col] == ' ' {
			col++
		}
		// Skip the rail character.
		railStr := glyph.Rail
		if strings.HasPrefix(s[col:], railStr) {
			col += len(railStr)
		}
		// Skip the gutter (space + glyph + space = 3 cells, but the first
		// space may already be consumed). Skip remaining spaces, one glyph,
		// then spaces again.
		for col < len(s) && s[col] == ' ' {
			col++
		}
		// Skip the status glyph / spinner (one character).
		if col < len(s) && s[col] != ' ' {
			col++
		}
		// Skip trailing space(s) after the glyph.
		for col < len(s) && s[col] == ' ' {
			col++
		}
		return col
	}

	laneBody := laneRows[len(laneRows)-1] // last row is a body row
	todoBody := todoRows[len(todoRows)-1] // last row is a body row
	laneCol := textStartCol(laneBody)
	todoCol := textStartCol(todoBody)
	if laneCol != todoCol {
		t.Fatalf("agent lane body text starts at col %d, todo panel body text starts at col %d\nlane: %q\ntodo: %q",
			laneCol, todoCol, ansi.Strip(laneBody), ansi.Strip(todoBody))
	}
}

// A dispatched subagent carries its model in the lane row, and its provider
// when that provider differs from the parent's active route.
func TestAgentLaneRowShowsModel(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagentWithMeta("fleet-reviewer", child, session.SubagentMeta{
		Model: "glm-5.2", Provider: "zhipu",
	})
	plain := ansi.Strip(m.renderActivityLane())
	for _, want := range []string{"fleet-reviewer", "glm-5.2", "@ zhipu"} {
		if !strings.Contains(plain, want) {
			t.Errorf("lane missing %q:\n%s", want, plain)
		}
	}
}

// When the child's provider matches the parent's active route, the provider
// collapses away — the model alone is shown.
func TestAgentLaneRowHidesOffParent(t *testing.T) {
	m := newTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Provider: "zhipu"})
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagentWithMeta("fleet-reviewer", child, session.SubagentMeta{
		Model: "glm-5.2", Provider: "zhipu",
	})
	plain := ansi.Strip(m.renderActivityLane())
	if !strings.Contains(plain, "glm-5.2") {
		t.Fatalf("lane must show the model:\n%s", plain)
	}
	if strings.Contains(plain, " @ ") {
		t.Fatalf("same-provider child must not show the provider:\n%s", plain)
	}
}

// A subagent registered without meta keeps the legacy #ID Label Elapsed
// shape, with no empty segments.
func TestAgentLaneRowWithoutMetaKeepsLegacyShape(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "review")
	plain := ansi.Strip(m.renderActivityLane())
	if !strings.Contains(plain, "#") {
		t.Fatalf("row must carry the #-prefixed id:\n%s", plain)
	}
	if !strings.Contains(plain, "review") {
		t.Fatalf("row must carry the label:\n%s", plain)
	}
	if !strings.Contains(plain, "s") {
		t.Fatalf("row must carry an elapsed seconds suffix:\n%s", plain)
	}
	if strings.Contains(plain, "·  ·") {
		t.Fatalf("row must not contain empty segments:\n%s", plain)
	}
}

// The composed row must never exceed the lane width, even with a very long
// label plus model.
func TestAgentLaneRowFitsWidth(t *testing.T) {
	m := newTestModel(t)
	m.resize(40, 24)
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagentWithMeta(strings.Repeat("x", 200), child, session.SubagentMeta{
		Model: "glm-5.2", Provider: "zhipu",
	})
	out := m.renderActivityLane()
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if n := ansi.StringWidth(line); n > m.leftWidth {
			t.Fatalf("lane row width %d exceeds leftWidth %d:\n%s", n, m.leftWidth, ansi.Strip(line))
		}
	}
}
