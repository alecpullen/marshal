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
	"marshal/internal/app/tui/inspector"
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

// The lane must report that running work exists, and it must reach the reader at
// a glance. Task 14 replaced the per-child rows with a count, so the LABELS are
// no longer on this row — a reader who wants to know which children are running
// opens the Agents tab, which the row names.
//
// This replaces TestAgentLaneShowsRunningSubagents. Keeping a version of it that
// demanded "tests" and "review" on the row would be demanding the behaviour the
// consolidation removes.
func TestAgentLaneShowsRunningWorkAsACount(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	plain := ansi.Strip(m.renderActivityLane())
	if !strings.Contains(plain, "2 agents") {
		t.Errorf("lane missing the running count:\n%s", plain)
	}
	if !strings.Contains(plain, "agents") {
		t.Errorf("lane does not name the surface that lists them:\n%s", plain)
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

// The lane used to cap its per-child rows and add an "… N more" overflow row.
// Task 14 consolidated the band to ONE count row, so the cap and the overflow
// row are gone — the count is always complete, because it is the only thing the
// row says.
//
// This test replaces TestAgentLaneCapsWithOverflowRow, which pinned the
// behaviour the consolidation removes.
func TestAgentLaneStaysOneRowWithManyChildren(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 9; i++ {
		registerRunningSubagent(t, &m, "task")
	}
	out := m.renderActivityLane()
	if got := strings.Count(out, "\n"); got > laneActivityRows {
		t.Fatalf("lane rendered %d rows with 9 children, want at most %d:\n%s",
			got, laneActivityRows, ansi.Strip(out))
	}
	// The count must be the FULL count: there is no overflow to hide it in.
	if !strings.Contains(ansi.Strip(out), "9 agents") {
		t.Fatalf("the count is not the full nine:\n%s", ansi.Strip(out))
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
	// The caption sits directly beneath the separator's full-width rule, so
	// it must be a plain label: a second ruled line here reads as a messy
	// double line. Matches the todo panel's plain "✓ N tasks done" summary.
	if strings.Contains(caption, "─") {
		t.Fatalf("caption must be a plain label without a rule (double line), got %q", caption)
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

// The lane renders a separator rule row, then ONE count row. There are no
// per-child rows after it, and no blank row between them: every row the lane
// occupies is subtracted from the transcript viewport, so a blank one costs a
// line of the conversation.
//
// This replaces TestAgentLaneStructureHeaderThenRuleThenRows, which asserted the
// per-child "#id" rows the consolidation removes.
func TestAgentLaneStructureIsRuleThenCount(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	plain := ansi.Strip(m.renderActivityLane())
	if !strings.Contains(plain, "2 agents") {
		t.Fatalf("the count must be first and pluralized, got:\n%s", plain)
	}
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) != laneActivityRows {
		t.Fatalf("the lane has %d rows, want %d:\n%s", len(lines), laneActivityRows, plain)
	}
	// No blank content row: an empty row is chrome the reader pays for and
	// cannot read.
	for i, l := range lines {
		if strings.TrimSpace(strings.TrimPrefix(l, glyph.Rail)) == "" {
			t.Fatalf("lane row %d is empty:\n%s", i, plain)
		}
	}
	if !strings.Contains(lines[laneActivityRows-1], "2 agents") {
		t.Fatalf("the count is not on the last row:\n%s", plain)
	}
	// The rule lives on the SEPARATOR line (row 0), not on the count line.
	if !strings.Contains(lines[0], "─") {
		t.Fatalf("separator line must carry the rule, got %q:\n%s", lines[0], plain)
	}
	if strings.Contains(lines[laneActivityRows-1], "─") {
		t.Fatalf("the count line must not carry the rule (this reads as a double line):\n%s", plain)
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

// Up/Down belong to the composer, not the agents lane. They used to move an
// invisible lane cursor whenever the input was empty, which meant a blank Up
// recalled no prompt history and a blank Enter drilled into a child agent.
// The lane stays reachable by click, and explicit Ctrl+F is the keyboard
// route into a running child's transcript.
func TestUpDownNeverDrillFromTheLane(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if len(m.viewStack) != 0 {
		t.Fatalf("arrows must not drill into the lane, viewStack=%d", len(m.viewStack))
	}

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.viewStack) != 0 {
		t.Fatalf("blank Enter after arrows must not drill, viewStack=%d", len(m.viewStack))
	}
}

// Up with a running child still recalls prompt history: the lane takeover is
// gone and the composer keeps its own key.
func TestUpRecallsHistoryWithRunningChild(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	m.history = []string{"previous prompt"}
	m.histIdx = -1

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.input.Value() != "previous prompt" {
		t.Fatalf("Up should recall prompt history, got %q", m.input.Value())
	}
}

// A blank Enter keeps its steering-drain behavior and must not drill.
func TestLaneBlankEnterPreservesSteeringDrain(t *testing.T) {
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

// Clicking a lane row is still the mouse route into a child transcript; the
// keyboard takeover is what was removed, not the lane itself.
// Task 14 changed what a lane click DOES: it opened one child's transcript
// directly, and now it opens the Agents tab that lists them all — which reaches
// the same information and more.
//
// This test replaces TestAgentLaneClickStillDrills. It is worth keeping the
// shape of the old assertion — "a click at this position is consumed and
// something opens" — because the failure it guards against is a band that
// reports itself clickable and then does nothing.
func TestAgentLaneClickOpensTheAgentsTab(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	registerRunningSubagent(t, &m, "tests")
	// The lane's rectangle is measured into the frame, and registering the
	// agent after the resize is what makes it appear; refresh so the band
	// matches what is now rendered.
	m.refreshViewport()

	top, _, ok := m.agentLaneBand()
	if !ok {
		t.Fatal("lane did not report a clickable band")
	}
	if len(m.agentLaneEntries()) == 0 {
		t.Fatal("expected a running lane entry")
	}
	// Either row of the band is the target now; the separator is row 0.
	if _, handled := m.handleAgentLaneClick(tea.MouseClickMsg{
		Button: tea.MouseLeft, X: 1, Y: top,
	}); !handled {
		t.Fatal("lane row click should be consumed")
	}
	if !m.inspector.isRendering() {
		t.Fatal("lane click should open the inspector")
	}
	if got := m.inspector.model.SelectedTab(); got != inspector.TabAgents {
		t.Fatalf("lane click opened %q, want the Agents tab", got)
	}
}

// The lane's separator row is built at full width and then re-truncated by
// chromeRailWidth to width-1, which ate the last cell of the rule and
// replaced it with an ellipsis. Assert the separator's width arithmetic, not
// just the absence of "…", so this stays a guard against the off-by-one
// itself.
//
// Only the separator is asserted to fill the row. The caption is now a
// short plain label (no rule), so it does not fill the width in the
// no-background test renderer; padding the caption band to width is
// PaintBand's job in production.
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
		// Row 0 is the separator (full-width rule); row 1 is the caption.
		if strings.Contains(rows[0], "…") {
			t.Errorf("w=%d: separator truncated: %q", w, rows[0])
		}
		if got := ansi.StringWidth(rows[0]); got != m.leftWidth {
			t.Errorf("w=%d: separator width = %d, want leftWidth %d", w, got, m.leftWidth)
		}
		// The caption must still fit on its one row without truncation.
		if strings.Contains(rows[1], "…") {
			t.Errorf("w=%d: caption truncated: %q", w, rows[1])
		}
	}
}

// The agent lane's count row must start its text at the same column as the todo
// panel's body rows, so the two panels look aligned when stacked.
//
// It used to compare the last AGENT row against the last todo row. The lane now
// has one content row, so the comparison is against that row — the gutter
// arithmetic being checked is the same either way.
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
	if len(laneRows) < laneActivityRows || len(todoRows) < 2 {
		t.Fatalf("need at least %d lane rows and 2 todo rows; lane=%d todo=%d",
			laneActivityRows, len(laneRows), len(todoRows))
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

// A dispatched subagent's model, provider and label are no longer on the lane
// row — Task 14 moved the per-child detail to the inspector's Agents tab, which
// is what the row now names.
//
// What still has to hold is that such a child IS counted and IS reachable, so
// this replaces TestAgentLaneRowShowsModel with the contract that survives.
func TestAgentLaneCountsADispatchedSubagent(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagentWithMeta("fleet-reviewer", child, session.SubagentMeta{
		Model: "glm-5.2", Provider: "zhipu",
	})
	plain := ansi.Strip(m.renderActivityLane())
	if !strings.Contains(plain, "1 agent") {
		t.Errorf("the dispatched child is not counted:\n%s", plain)
	}
	if !strings.Contains(plain, "agents") {
		t.Errorf("the lane does not name where the child's detail is:\n%s", plain)
	}
	// And the child must actually be reachable: the inspector is opened from
	// this row, so a count with nothing behind it would be a dead end.
	if got := len(m.lanePlan().agents); got != 1 {
		t.Fatalf("the plan carries %d agents, want the one child", got)
	}
}

// A child whose provider matches the parent's is still counted, exactly like any
// other: the count is about WORK, not about where the work is running.
//
// This replaces TestAgentLaneRowHidesOffParent, which asserted the collapse of
// the provider segment on a row that no longer exists.
func TestAgentLaneCountsASameProviderChild(t *testing.T) {
	m := newTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Provider: "zhipu"})
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagentWithMeta("fleet-reviewer", child, session.SubagentMeta{
		Model: "glm-5.2", Provider: "zhipu",
	})
	plain := ansi.Strip(m.renderActivityLane())
	if !strings.Contains(plain, "1 agent") {
		t.Fatalf("a same-provider child must still be counted:\n%s", plain)
	}
	// The provider never appears on this row at all now, for either case: the
	// row has one job, and it is the count.
	if strings.Contains(plain, "zhipu") {
		t.Fatalf("the lane row names a provider, which is per-child detail:\n%s", plain)
	}
}

// A subagent registered without meta is counted the same as one with meta. The
// row shape is now the count, so what must hold is that the count is complete
// and free of empty segments.
//
// This replaces TestAgentLaneRowWithoutMetaKeepsLegacyShape.
func TestAgentLaneRowWithoutMetaCountsTheSame(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "review")
	plain := ansi.Strip(m.renderActivityLane())
	if !strings.Contains(plain, "1 agent") {
		t.Fatalf("the child is not counted:\n%s", plain)
	}
	if strings.Contains(plain, "·  ·") || strings.Contains(plain, "  ·") {
		t.Fatalf("the row contains an empty segment:\n%s", plain)
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
