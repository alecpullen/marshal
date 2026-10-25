package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// scriptedTasks seeds a turn with n todos, each worked in stepsPer steps, and
// completes them one by one. When finish is true the turn ends with a final
// answer. The last task is left in progress otherwise.
//
// Completing a task logs the todo.write a real turn would: the receipt counts
// the items a turn's plan lines moved to completed, so a fixture that only
// called SetTodos would produce a receipt with no task count.
func scriptedTasks(t *testing.T, m *Model, n, stepsPer int, finish bool) {
	t.Helper()
	m.state.AddMessage(session.RoleUser, "build the thing", session.ContentTypePlain)
	todos := make([]db.TodoItem, n)
	for i := range todos {
		todos[i] = db.TodoItem{ID: fmt.Sprintf("t%d", i+1), Content: fmt.Sprintf("Task number %d", i+1), Status: "pending"}
	}
	for i := 0; i < n; i++ {
		todos[i].Status = "in_progress"
		todos[i].StartedAt = time.Now().Add(-30 * time.Second)
		if err := m.state.SetTodos(append([]db.TodoItem(nil), todos...)); err != nil {
			t.Fatal(err)
		}
		for s := 0; s < stepsPer; s++ {
			id := m.state.BeginStep(session.Actor{})
			m.state.AddNarration(id, fmt.Sprintf("Working on task %d part %d. Details follow.", i+1, s+1))
			m.state.LogToolCall(registry.AuditEvent{
				Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: fmt.Sprintf("c%d-%d", i, s),
				Args: []byte(`{"path":"x.go"}`), ResultSummary: "ok",
			})
			if s == stepsPer-1 && (i < n-1 || finish) {
				todos[i].Status = "completed"
				todos[i].CompletedAt = time.Now()
				if err := m.state.SetTodos(append([]db.TodoItem(nil), todos...)); err != nil {
					t.Fatal(err)
				}
				args, err := json.Marshal(map[string]any{"todos": todos})
				if err != nil {
					t.Fatal(err)
				}
				m.state.LogToolCall(registry.AuditEvent{
					Timestamp: time.Now(), ToolName: "todo.write", StepID: id, ToolCallID: fmt.Sprintf("w%d", i),
					Args: args, ResultSummary: fmt.Sprintf("tasks updated · %d items", len(todos)),
				})
			}
			m.state.EndStep(id)
		}
	}
	if finish {
		m.state.AddMessageFinal(session.RoleAssistant, "All done.", session.ContentTypePlain)
	}
}

func viewText(m *Model) string { return strings.Join(transcriptLines(m), "\n") }

func TestFinishedTasksStayOpenAndTheReceiptSummarisesTheTurn(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 60)
	scriptedTasks(t, &m, 3, 2, true)
	text := viewText(&m)
	for _, want := range []string{"▸ 1/3", "▸ 2/3", "▸ 3/3", "All done.", "3 tasks", "6 steps", "6 tools"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	// Six beats, all finished: nothing folds, so every beat keeps its row.
	if got := strings.Count(text, "read    x.go"); got != 6 {
		t.Errorf("the read row rendered %d times, want 6: history never folds:\n%s", got, text)
	}
	if !strings.Contains(text, "Working on task 1 part 1. Details follow.") {
		t.Errorf("the first beat keeps its narration:\n%s", text)
	}
}

func TestRunningTurnKeepsEveryBeatOpen(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 60)
	scriptedTasks(t, &m, 3, 2, false)
	text := viewText(&m)
	if got := strings.Count(text, "read    x.go"); got != 6 {
		t.Errorf("the read row rendered %d times, want 6: done tasks keep their rows:\n%s", got, text)
	}
	if !strings.Contains(text, "Working on task 3 part 1") || !strings.Contains(text, "Working on task 3 part 2") {
		t.Errorf("the task the model is working on is shown:\n%s", text)
	}
	if strings.Contains(text, "done ·") {
		t.Errorf("no receipt before the turn ends:\n%s", text)
	}
}

func TestFailedBeatStaysOpen(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 60)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Make tests pass", Status: "in_progress", StartedAt: time.Now()}})
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Running the suite. It may fail.")
	exit := 1
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "test.run", StepID: id, ToolCallID: "x",
		Args: []byte(`{"command":"go test ./..."}`), ResultSummary: "exit 1", CommandExitCode: &exit})
	m.state.EndStep(id)
	_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Make tests pass", Status: "completed", StartedAt: time.Now(), CompletedAt: time.Now()}})
	text := viewText(&m)
	if !strings.Contains(text, "Running the suite") || !strings.Contains(text, "exit 1") {
		t.Fatalf("the failed beat must stay open:\n%s", text)
	}
}

func TestTodoWriteNeverRendersAsARow(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Planning the work. Three parts.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "todo.write", StepID: id, ToolCallID: "w",
		Args: []byte(`{"todos":[{"content":"Part one","status":"in_progress"}]}`), ResultSummary: "tasks updated · 1 items"})
	m.state.EndStep(id)
	text := viewText(&m)
	if strings.Contains(text, "todo.write") || strings.Contains(text, "tasks updated") {
		t.Fatalf("todo.write must not render:\n%s", text)
	}
	if !strings.Contains(text, "Planning the work.") {
		t.Fatalf("the narration still shows:\n%s", text)
	}
}

func TestDroppedTodoGetsNoDivider(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 40)
	scriptedTasks(t, &m, 2, 1, false)
	// The model rewrites its list without task 1, so its beats are bound to
	// a todo the list no longer holds: no divider names it, and the work
	// still renders.
	_ = m.state.SetTodos([]db.TodoItem{{ID: "t2", Content: "Task number 2", Status: "in_progress", StartedAt: time.Now()}})
	text := viewText(&m)
	if strings.Contains(text, "1/1 Task number 1") || strings.Contains(text, "dropped") {
		t.Fatalf("a removed todo gets no divider:\n%s", text)
	}
	if !strings.Contains(text, "Working on task 1 part 1") {
		t.Fatalf("the dropped todo's work still renders:\n%s", text)
	}
}

func TestPreTaskSessionsRenderWithoutHeadersOrErrors(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	_ = m.state.SetTodos([]db.TodoItem{{Content: "legacy todo without an id", Status: "in_progress"}})
	stepFixture(t, &m, session.Actor{}, "Old-style step. Nothing bound.", readEvent("a.go"))
	text := viewText(&m)
	if strings.Contains(text, "1/1") || strings.Contains(text, "dropped") {
		t.Fatalf("no task headers without todo ids:\n%s", text)
	}
	if !strings.Contains(text, "Old-style step.") {
		t.Fatalf("the step still renders:\n%s", text)
	}
}

func TestFailedRowShowsItsLastOutputLinesCollapsed(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	exit := 2
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Running the build. It may break.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "shell.run", StepID: id, ToolCallID: "b",
		Args: []byte(`{"command":"make"}`), ResultSummary: "exit 2", CommandExitCode: &exit,
		ResultContent: "line1\nline2\nline3\nline4\nboom: the real reason"})
	m.state.EndStep(id)
	text := viewText(&m)
	if !strings.Contains(text, "boom: the real reason") || !strings.Contains(text, "line3") {
		t.Fatalf("a failed row shows its last lines:\n%s", text)
	}
	if strings.Contains(text, "line1") {
		t.Fatalf("only the last 3 lines:\n%s", text)
	}
}

// Golden rows for a beat: exact text at 80 and 140 columns. The beat that
// starts a todo carries its tag at the right edge of its head, each call hangs
// off the spine with its result at the right edge, and the plan line closes
// the beat where the list was written.
func TestBeatRowsGolden(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	edge := func(left, right string, width int) string {
		return left + strings.Repeat(" ", width-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
	}
	for _, width := range []int{80, 140} {
		m := newTestModel(t)
		m.resize(width, 60)
		m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
		_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Wire the parser", Status: "in_progress", StartedAt: start}, {ID: "t2", Content: "Test it", Status: "pending"}})
		id := m.state.BeginStep(session.Actor{})
		m.state.AddNarration(id, "Reading the parser. It is small.")
		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: "a", Args: []byte(`{"path":"p.go"}`), ResultSummary: "ok"})
		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now().Add(time.Millisecond), ToolName: "todo.write", StepID: id, ToolCallID: "w",
			Args:          []byte(`{"todos":[{"id":"t1","content":"Wire the parser","status":"completed"},{"id":"t2","content":"Test it","status":"in_progress"}]}`),
			ResultSummary: "tasks updated · 2 items"})
		m.state.EndStep(id)
		_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Wire the parser", Status: "completed", StartedAt: start, CompletedAt: start.Add(3 * time.Minute)}, {ID: "t2", Content: "Test it", Status: "in_progress", StartedAt: start.Add(3 * time.Minute)}})
		lines := transcriptLines(&m)

		head := -1
		for i, l := range lines {
			if strings.Contains(l, "Reading the parser.") {
				head = i
			}
		}
		if head < 0 || head+3 > len(lines) {
			t.Fatalf("w=%d beat missing:\n%s", width, strings.Join(lines, "\n"))
		}
		want := []string{
			edge(" ● Reading the parser. It is small.", "▸ 1/2", width),
			edge("   ├ read    p.go", "ok", width),
			"   └ plan    ✓ ▸  2/2  Test it",
		}
		for i, w := range want {
			if got := strings.TrimRight(lines[head+i], " "); got != w {
				t.Errorf("w=%d row %d = %q\nwant      %q", width, i, got, w)
			}
		}
	}
}

func TestReceiptGolden(t *testing.T) {
	for _, width := range []int{80, 140} {
		m := newTestModel(t)
		m.resize(width, 60)
		scriptedTasks(t, &m, 2, 2, true)
		var receipt string
		for _, l := range transcriptLines(&m) {
			if strings.Contains(l, "done ·") {
				receipt = strings.TrimRight(l, " ")
			}
		}
		if !strings.HasPrefix(receipt, " ✓ done · ") || !strings.Contains(receipt, "2 tasks · 4 steps · 4 tools") {
			t.Errorf("w=%d receipt = %q", width, receipt)
		}
	}
}

func TestSalvagedTurnReceiptSaysSo(t *testing.T) {
	r := &viewmodel.ReceiptInfo{Duration: time.Minute, Steps: 3, Tools: 4, Salvaged: true}
	if got := stripANSI(renderReceipt(r, 100)); !strings.Contains(got, "salvaged") || !strings.Contains(got, "!") && !strings.Contains(got, "⚠") {
		t.Fatalf("salvaged receipt = %q", got)
	}
}

// One step with narration, reasoning and a tool call, collapsed and with
// Ctrl+G's expand-all.
func TestExpandMatrix(t *testing.T) {
	build := func(all bool) string {
		m := newTestModel(t)
		m.resize(120, 80)
		m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
		id := m.state.BeginStep(session.Actor{})
		m.state.AddNarration(id, "Reading the config. The second sentence is detail that only full shows in a wrapped block.")
		m.state.LogThinking(session.ThinkingEntry{Text: "private reasoning text", Duration: 2 * time.Second, StartedAt: time.Now(), StepID: id})
		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: "r",
			Args: []byte(`{"path":"cfg.go"}`), ResultSummary: "12 lines", ResultContent: "package cfg\nvar Secret = 1"})
		m.state.EndStep(id)
		m.expandAll = all
		return viewText(&m)
	}
	collapsed, expanded := build(false), build(true)

	for _, c := range []struct {
		name, text string
		want       map[string]bool // substring -> expected present
	}{
		{"collapsed", collapsed, map[string]bool{"Reading the config.": true, "read    cfg.go": true, "thought 2s": true, "private reasoning text": false, "var Secret": false}},
		{"expanded", expanded, map[string]bool{"Reading the config.": true, "private reasoning text": true, "var Secret": true}},
	} {
		for sub, want := range c.want {
			if got := strings.Contains(c.text, sub); got != want {
				t.Errorf("%s: contains(%q) = %v, want %v\n%s", c.name, sub, got, want, c.text)
			}
		}
	}
}

// A task carried over from an earlier turn must not show the idle hours since
// then: its clock is the time its steps ran.
func TestTaskClockIgnoresIdleTimeBetweenTurns(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	long := time.Now().Add(-5 * time.Hour)
	_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Carried over", Status: "in_progress", StartedAt: long}})
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Picking it back up. Briefly.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: "a", Args: []byte(`{"path":"x"}`), ResultSummary: "ok"})
	m.state.EndStep(id)
	text := viewText(&m)
	if strings.Contains(text, "5h") || strings.Contains(text, "300m") {
		t.Fatalf("header clock counted the idle time:\n%s", text)
	}
}

func stepUnder(m *Model, narration, call string) {
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, narration)
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: call,
		Args: []byte(`{"path":"x.go"}`), ResultSummary: "ok"})
	m.state.EndStep(id)
}

func TestTodoDividersOrderTheWorkAndTheBandCarriesProgress(t *testing.T) {
	m := todoStack(t)

	lines := transcriptLines(&m)
	text := strings.Join(lines, "\n")
	at := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("missing %q:\n%s", sub, text)
		return -1
	}
	// The first beat of each todo carries its tag, in list order, on the
	// head of the work itself. Every beat is history now, so both draw in
	// the transcript.
	first := at("Reading it.")
	if !strings.HasSuffix(strings.TrimRight(lines[first], " "), "▸ 1/4") {
		t.Fatalf("each todo's first beat carries its tag:\n%s", text)
	}
	second := at("Writing it.")
	if !strings.HasSuffix(strings.TrimRight(lines[second], " "), "▸ 2/4") {
		t.Fatalf("the second todo's first beat carries its tag:\n%s", text)
	}
	if strings.Contains(text, "3/4") || strings.Contains(text, "4/4") || strings.Contains(text, "──") {
		t.Errorf("waiting todos live in the plan section, and history has no dividers:\n%s", text)
	}

	// The box's plan section carries the progress: the header counts the
	// list, and the item rows name the todo in progress.
	box := stripANSI(boxOut(m))
	if !strings.Contains(box, "1/4") {
		t.Errorf("plan header should count progress:\n%s", box)
	}
	if !strings.Contains(box, "Write") {
		t.Errorf("plan should name the active todo:\n%s", box)
	}
	for _, r := range m.boxLayout().Rows {
		if r.Target.Kind != livebox.TargetPlan {
			continue
		}
		for _, gone := range []string{"✓ 1/4", "▸ 2/4", "· 3/4"} {
			if strings.Contains(stripANSI(r.Text), gone) {
				t.Errorf("the plan must not re-render per-todo rows (%q):\n%s", gone, stripANSI(r.Text))
			}
		}
	}
}

// TestLeftoverPlanIsOneIdleRow: a list the turn never touched is still the
// plan, so an idle session shows it as the one plain idle row rather than
// hiding it.
func TestLeftoverPlanIsOneIdleRow(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "A", Status: "pending"}, {ID: "t2", Content: "B", Status: "pending"}})
	m.state.AddMessage(session.RoleUser, "unrelated", session.ContentTypePlain)
	stepUnder(&m, "Just reading. Nothing else.", "a")
	m.invalidateTranscript()
	m.refreshViewport()
	if m.band.Todos {
		t.Fatalf("leftover todos must not put progress in the band on an unrelated turn: %+v", m.band)
	}
	if got := stripANSI(boxOut(m)); got != "▸ Plan 0/2 · A" {
		t.Fatalf("box = %q, want the single idle row naming the plan", got)
	}
}

// TestStripWindowCentersOnTheActiveTodo pinned the todo strip's row window.
// The strip is gone: the live box's plan band carries the progress
// (TestTodoDividersOrderTheWorkAndTheBandCarriesProgress), and the full list
// opens in the Ctrl+T Tasks panel, which numbers every todo against the whole
// list (TestTasksDocShowsStepCounts).

func TestTransientNarrationFlowsWithoutHeadersWhenThereAreNoTodos(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 60)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	stepUnder(&m, "First look. At the lexer.", "a")
	stepUnder(&m, "Second look. At the parser.", "b")
	text := viewText(&m)
	if regexp.MustCompile(`\d/\d`).MatchString(text) || strings.Contains(text, "─────") {
		t.Fatalf("no todo list means no task chrome:\n%s", text)
	}
	lines := transcriptLines(&m)
	for i, l := range lines {
		if strings.Contains(l, "First look.") {
			if !strings.Contains(lines[i+1], "└ read") {
				t.Errorf("a narration's calls hang directly under it:\n%s", text)
			}
			if !strings.HasPrefix(l, " ● ") || regexp.MustCompile(`\d+s$`).MatchString(strings.TrimRight(l, " ")) {
				t.Errorf("settled beats are a muted ● with no clock: %q", l)
			}
		}
	}
}

func TestBackToBackNarrationReadsAsOneText(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 60)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	for _, n := range []string{"One thought. Here.", "Another thought. There."} {
		id := m.state.BeginStep(session.Actor{})
		m.state.AddNarration(id, n)
		m.state.EndStep(id)
	}
	lines := transcriptLines(&m)
	a, b := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "One thought.") {
			a = i
		}
		if strings.Contains(l, "Another thought.") {
			b = i
		}
	}
	if a < 0 || b != a+1 {
		t.Fatalf("narration-only steps run on without a blank line:\n%s", strings.Join(lines, "\n"))
	}
}

func TestMergedToolRunStaysOneRowAsItGrows(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 60)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Reading a few files. All small.")
	count := func() int {
		n := 0
		for _, l := range transcriptLines(&m) {
			if strings.Contains(l, "read    f0.go") {
				n++
			}
		}
		return n
	}
	for i := 0; i < 4; i++ {
		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now().Add(time.Duration(i) * time.Millisecond), ToolName: "file.read", StepID: id, ToolCallID: fmt.Sprintf("r%d", i),
			Args: []byte(fmt.Sprintf(`{"path":"f%d.go"}`, i)), ResultSummary: "ok"})
		if i > 0 && count() != 1 {
			t.Fatalf("after %d calls the run is %d rows, want 1", i+1, count())
		}
	}
}

// Tight joins shift every block after them up a line; the click regions and
// browse stops are computed alongside, and must still land on the rows they
// name.
func TestTightJoinsKeepClickAndBrowsePositionsOnTheirRows(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 60)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	todos := []db.TodoItem{
		{ID: "t1", Content: "Read", Status: "in_progress", StartedAt: time.Now()},
		{ID: "t2", Content: "Write", Status: "pending"},
		{ID: "t3", Content: "Test", Status: "pending"},
	}
	_ = m.state.SetTodos(append([]db.TodoItem(nil), todos...))
	stepUnder(&m, "Reading it. Slowly.", "a")
	todos[0].Status, todos[0].CompletedAt = "completed", time.Now()
	todos[1].Status, todos[1].StartedAt = "in_progress", time.Now()
	_ = m.state.SetTodos(append([]db.TodoItem(nil), todos...))
	stepUnder(&m, "Writing the first half. Carefully.", "b")
	stepUnder(&m, "Writing the second half. Quickly.", "c")
	for _, n := range []string{"One thought. Here.", "Another thought. There."} {
		id := m.state.BeginStep(session.Actor{})
		m.state.AddNarration(id, n)
		m.state.EndStep(id)
	}

	lines := transcriptLines(&m)
	wants := map[viewmodel.Kind][]string{
		viewmodel.KindStep: {"Reading it.", "Writing the first half", "Writing the second half", "One thought", "Another thought"},
	}
	seen := 0
	for _, r := range m.nodeRegions {
		kinds, ok := wants[r.target.node.Kind]
		if !ok || r.startLine >= len(lines) {
			continue
		}
		hit := false
		for _, w := range kinds {
			if strings.Contains(lines[r.startLine], w) {
				hit = true
			}
		}
		if !hit {
			t.Errorf("region %v starts at line %d = %q, which is not one of %v", r.target.node, r.startLine, lines[r.startLine], kinds)
		}
		seen++
	}
	if seen < 5 {
		t.Fatalf("expected regions for the five beats, saw %d:\n%s", seen, strings.Join(lines, "\n"))
	}
	for _, it := range m.browseItems {
		if it.kind != viewmodel.KindStep {
			continue
		}
		if it.start >= len(lines) || strings.TrimSpace(lines[it.start]) == "" {
			t.Errorf("browse stop %v starts on a blank or missing line %d", it.id, it.start)
		}
	}
}
