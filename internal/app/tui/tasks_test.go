package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// scriptedTasks seeds a turn with n todos, each worked in stepsPer steps, and
// completes them one by one. When finish is true the turn ends with a final
// answer. The last task is left in progress otherwise.
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
			m.state.EndStep(id)
		}
		if i < n-1 || finish {
			todos[i].Status = "completed"
			todos[i].CompletedAt = time.Now()
			if err := m.state.SetTodos(append([]db.TodoItem(nil), todos...)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if finish {
		m.state.AddMessageFinal(session.RoleAssistant, "All done.", session.ContentTypePlain)
	}
}

func viewText(m *Model) string { return strings.Join(transcriptLines(m), "\n") }

func TestFinishedTasksFoldAndTheReceiptSummarisesTheTurn(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 60)
	scriptedTasks(t, &m, 3, 2, true)
	text := viewText(&m)
	for _, want := range []string{"1/3 Task number 1", "2/3 Task number 2", "3/3 Task number 3", "All done.", "3 tasks", "6 steps", "6 tools"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	// Six beats, all finished: all but the newest viewmodel.FoldKeep fold to
	// outline rows, which keep the narration headline and its call count but
	// hide the tool rows themselves.
	if got := strings.Count(text, "Read file"); got != viewmodel.FoldKeep {
		t.Errorf("Read file rendered %d times, want %d: a folded beat hides its tool rows:\n%s", got, viewmodel.FoldKeep, text)
	}
	for _, want := range []string{"Working on task 1 part 1. Details follow.", "1 tool"} {
		if !strings.Contains(text, want) {
			t.Errorf("a folded beat still renders %q:\n%s", want, text)
		}
	}
}

func TestRunningTurnFoldsDoneTasksAndKeepsTheLiveOneOpen(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 60)
	scriptedTasks(t, &m, 3, 2, false)
	text := viewText(&m)
	if strings.Contains(text, "Working on task 1 part 1. Details follow.                                    1 tool") {
		t.Errorf("want a folded outline row, not a full one:\n%s", text)
	}
	// The turn's newest beats stay open, and they are the ones carrying the
	// task-3 narration the model is working on.
	if !strings.Contains(text, "Working on task 3 part 1") || !strings.Contains(text, "Working on task 3 part 2") {
		t.Errorf("the newest beats of a running turn stay open:\n%s", text)
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
	// The failing beat is the newest of its turn, so it stays unfolded: a
	// beat only folds once the model has moved past it.
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

func TestOutlineShowsOneRowPerStep(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 80)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	for i := 0; i < 10; i++ {
		stepFixture(t, &m, session.Actor{}, fmt.Sprintf("Step %d does a thing. More words here.", i), readEvent("a.go"))
	}
	m.density = densityOutline
	text := viewText(&m)
	if got := strings.Count(text, "does a thing."); got != 10 {
		t.Fatalf("outline rows = %d, want 10:\n%s", got, text)
	}
	if strings.Contains(text, "a.go") {
		t.Fatalf("tool rows are hidden at outline:\n%s", text)
	}
	if !strings.Contains(text, "1 tool") {
		t.Fatalf("outline steps carry a tool count:\n%s", text)
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

func TestTasksDocShowsStepCountsAndWorkTime(t *testing.T) {
	todos := []db.TodoItem{
		{ID: "t1", Content: "Done one", Status: "completed"},
		{ID: "t2", Content: "Live one", Status: "in_progress"},
		{ID: "t3", Content: "Later", Status: "pending"},
	}
	doc := tasksDoc(todos, map[string]taskStat{"t1": {steps: 2, work: 3 * time.Minute}, "t2": {steps: 1, work: 90 * time.Second}})
	if got := doc.Rows[0].Detail; got != "2 steps · 3m00s" {
		t.Errorf("completed row detail = %q", got)
	}
	if got := doc.Rows[1].Detail; got != "1 step · 1m30s" {
		t.Errorf("in-progress row detail = %q", got)
	}
	if got := doc.Rows[2].Detail; got != "" {
		t.Errorf("pending row detail = %q", got)
	}
}

func TestNowBarClockIsTheInProgressTasksElapsedTime(t *testing.T) {
	now := time.Now()
	in := nowBarInput{
		Width: 80, Busy: true, Now: now, TurnStartedAt: now.Add(-10 * time.Minute),
		Todos: []db.TodoItem{
			{ID: "t1", Content: "Working", Status: "in_progress", StartedAt: now.Add(-42 * time.Second)},
			{ID: "t2", Content: "Next", Status: "pending"},
		},
		Band: viewmodel.BandInfo{Todos: true, Done: 0, Total: 2, Active: "Working", Next: []string{"Next"}},
	}
	_, _, elapsed := nowBarHead(in)
	if elapsed != "42s" {
		t.Fatalf("elapsed = %q, want the task's 42s rather than the turn's 10m", elapsed)
	}
	in.Todos[0].StartedAt = time.Time{}
	if _, _, elapsed = nowBarHead(in); !strings.Contains(elapsed, "10m") {
		t.Fatalf("with no task start the turn clock is used, got %q", elapsed)
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

// Golden rows for the todo chrome: exact text at 80 and 140 columns. The
// divider is the only task chrome left; durations are fixed through the todo
// timestamps so any row that carries one is deterministic.
func TestTodoDividerGolden(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	for _, width := range []int{80, 140} {
		m := newTestModel(t)
		m.resize(width, 60)
		m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
		_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Wire the parser", Status: "in_progress", StartedAt: start}, {ID: "t2", Content: "Test it", Status: "pending"}})
		id := m.state.BeginStep(session.Actor{})
		m.state.AddNarration(id, "Reading the parser. It is small.")
		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: "a", Args: []byte(`{"path":"p.go"}`), ResultSummary: "ok"})
		m.state.EndStep(id)
		_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Wire the parser", Status: "completed", StartedAt: start, CompletedAt: start.Add(3 * time.Minute)}, {ID: "t2", Content: "Test it", Status: "pending"}})
		lines := transcriptLines(&m)

		var divider string
		for _, l := range lines {
			if strings.Contains(l, "1/2 Wire the parser") {
				divider = strings.TrimRight(l, " ")
			}
		}
		if !strings.HasPrefix(divider, "   ── 1/2 Wire the parser ") {
			t.Errorf("w=%d divider = %q", width, divider)
		}
		if got := len([]rune(divider)); got != width {
			t.Errorf("w=%d divider should fill the frame, is %d wide", width, got)
		}
		// The todo is complete and the beat is the turn's newest, so it stays
		// open: the divider is followed by the beat's own row.
		if !strings.Contains(strings.Join(lines, "\n"), "Reading the parser.") {
			t.Errorf("w=%d the beat under the divider still renders:\n%s", width, strings.Join(lines, "\n"))
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

// The density matrix for one step with narration, reasoning and a tool call.
func TestDensityMatrix(t *testing.T) {
	build := func(d density) string {
		m := newTestModel(t)
		m.resize(120, 80)
		m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
		id := m.state.BeginStep(session.Actor{})
		m.state.AddNarration(id, "Reading the config. The second sentence is detail that only full shows in a wrapped block.")
		m.state.LogThinking(session.ThinkingEntry{Text: "private reasoning text", Duration: 2 * time.Second, StartedAt: time.Now(), StepID: id})
		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: "r",
			Args: []byte(`{"path":"cfg.go"}`), ResultSummary: "12 lines", ResultContent: "package cfg\nvar Secret = 1"})
		m.state.EndStep(id)
		m.density = d
		return viewText(&m)
	}
	outline, steps, full := build(densityOutline), build(densitySteps), build(densityFull)

	for _, c := range []struct {
		name, text string
		want       map[string]bool // substring -> expected present
	}{
		{"outline", outline, map[string]bool{"Reading the config.": true, "Read file": false, "thought for": false, "1 tool": true}},
		{"steps", steps, map[string]bool{"Reading the config.": true, "Read file": true, "thought for": true, "private reasoning text": false, "var Secret": false}},
		{"full", full, map[string]bool{"Reading the config.": true, "private reasoning text": true, "var Secret": true}},
	} {
		for sub, want := range c.want {
			if got := strings.Contains(c.text, sub); got != want {
				t.Errorf("%s: contains(%q) = %v, want %v\n%s", c.name, sub, got, want, c.text)
			}
		}
	}
}

func TestNowBarIgnoresATaskStartedBeforeThisTurn(t *testing.T) {
	now := time.Now()
	in := nowBarInput{
		Width: 80, Busy: true, Now: now, TurnStartedAt: now.Add(-5 * time.Second),
		Todos: []db.TodoItem{{ID: "t1", Content: "Carried over", Status: "in_progress", StartedAt: now.Add(-5 * time.Hour)}},
	}
	if _, _, elapsed := nowBarHead(in); strings.Contains(elapsed, "h") || strings.Contains(elapsed, "300m") {
		t.Fatalf("a carried-over task must not put hours on a new turn's clock, got %q", elapsed)
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

// Two todos can share their text; a todo.write-only step re-binds to the one
// it just started, not to a later twin.
func TestRebindPrefersTheTodoStartedNearestTheWrite(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 50)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	now := time.Now()
	todos := []db.TodoItem{
		{ID: "t1", Content: "Run the tests", Status: "completed", StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-50 * time.Minute)},
		{ID: "t2", Content: "Run the tests", Status: "pending"}, // a later twin, not started
	}
	_ = m.state.SetTodos(todos)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Starting the first test run. Here goes.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: now.Add(-time.Hour), ToolName: "todo.write", StepID: id, ToolCallID: "w",
		Args: []byte(`{"todos":[{"content":"Run the tests","status":"in_progress"}]}`)})
	m.state.EndStep(id)
	m.invalidateTranscript()
	m.refreshViewport()
	if m.taskStats["t1"].steps != 1 || m.taskStats["t2"].steps != 0 {
		t.Fatalf("taskStats = %v: the old step must stay with t1, not move under the later twin", m.taskStats)
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
	// Each todo's work sits under the divider naming it, in list order. Both
	// beats are finished, but they are the newest of the turn, so they stay
	// open; a beat only folds once the model has moved past it.
	read, first, write, second := at("1/4 Read"), at("Reading it."), at("2/4 Write"), at("Writing it.")
	if !(read < first && first < write && write < second) {
		t.Fatalf("order must be divider, work, next divider, next work:\n%s", text)
	}
	if strings.Contains(text, "3/4 Test") || strings.Contains(text, "4/4 Ship") {
		t.Errorf("waiting todos live in the band, not the transcript:\n%s", text)
	}

	// The band carries the progress: the head row holds the count and the
	// active item, the checklist holds the done count and what comes next.
	// The per-todo rows are gone — the transcript's dividers cover those.
	plain := stripANSI(nowBarOut(m))
	if !strings.Contains(plain, "1/4") || !strings.Contains(plain, "Write") {
		t.Errorf("band head should count progress and name the active todo:\n%s", plain)
	}
	if !strings.Contains(plain, "1 done") || !strings.Contains(plain, "next: Test") {
		t.Errorf("band checklist should hold the done count and the next waiting todo:\n%s", plain)
	}
	for _, gone := range []string{"✓ 1/4", "▸ 2/4", "· 3/4"} {
		if strings.Contains(plain, gone) {
			t.Errorf("the band must not re-render per-todo rows (%q):\n%s", gone, plain)
		}
	}
}

func TestBandNotShownWhenTheTurnNeverTouchedTheList(t *testing.T) {
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
	if plain := stripANSI(nowBarOut(m)); strings.Contains(plain, "done") || strings.Contains(plain, "next:") {
		t.Fatalf("the band must not show todo progress for an unrelated turn:\n%s", plain)
	}
}

// TestStripWindowCentersOnTheActiveTodo pinned the todo strip's row window.
// The strip is gone: the band shows one progress line and one checklist line
// (nowbar_test.go), and the full list opens in the Ctrl+T Tasks panel, which
// numbers every todo against the whole list (TestTasksDocShowsStepCounts).

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
			if !strings.Contains(lines[i+1], "Read file") {
				t.Errorf("a narration's calls sit directly under it:\n%s", text)
			}
			if !strings.HasPrefix(l, " · ") || regexp.MustCompile(`\d+s$`).MatchString(strings.TrimRight(l, " ")) {
				t.Errorf("settled steps are a quiet dot with no clock: %q", l)
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
			if strings.Contains(l, "Read file") {
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
	// A labelled beat's region opens on its divider, so the todo lines count
	// as that beat's start.
	wants := map[viewmodel.Kind][]string{
		viewmodel.KindStep: {"1/3 Read", "2/3 Write", "Writing the first half", "Writing the second half", "One thought", "Another thought"},
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
		t.Fatalf("expected regions for two labelled beats and three more, saw %d:\n%s", seen, strings.Join(lines, "\n"))
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
