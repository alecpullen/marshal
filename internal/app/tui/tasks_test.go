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
	if strings.Contains(text, "Working on task 1 part 1") {
		t.Errorf("a finished task must be folded to one row:\n%s", text)
	}
	if !strings.Contains(text, "2 steps") || !strings.Contains(text, "▹") {
		t.Errorf("folded rows carry step count and the expand mark:\n%s", text)
	}
}

func TestRunningTurnFoldsDoneTasksAndKeepsTheLiveOneOpen(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 60)
	scriptedTasks(t, &m, 3, 2, false)
	text := viewText(&m)
	if strings.Contains(text, "Working on task 1 part 1") || strings.Contains(text, "Working on task 2 part 1") {
		t.Errorf("completed tasks fold while the turn runs:\n%s", text)
	}
	if !strings.Contains(text, "Working on task 3 part 1") {
		t.Errorf("the in-progress task stays open:\n%s", text)
	}
	if strings.Contains(text, "done ·") {
		t.Errorf("no receipt before the turn ends:\n%s", text)
	}
}

func TestFailedTaskStaysOpen(t *testing.T) {
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
	if !strings.Contains(text, "Running the suite") {
		t.Fatalf("a completed task with an unresolved failure must stay open:\n%s", text)
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

func TestDroppedTaskRendersUnderADroppedHeader(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 40)
	scriptedTasks(t, &m, 2, 1, false)
	// The model rewrites its list without task 1.
	_ = m.state.SetTodos([]db.TodoItem{{ID: "t2", Content: "Task number 2", Status: "in_progress", StartedAt: time.Now()}})
	text := viewText(&m)
	if !strings.Contains(text, "dropped") {
		t.Fatalf("a removed todo's steps render under a dropped header:\n%s", text)
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

// Golden rows for the task chrome: exact text at 80 and 140 columns. Durations
// are fixed through the todo timestamps so the rows are deterministic.
func TestTaskChromeGolden(t *testing.T) {
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

		var folded string
		for _, l := range lines {
			if strings.Contains(l, "Wire the parser") {
				folded = strings.TrimRight(l, " ")
			}
		}
		want := " ✓ 1/2 Wire the parser"
		// The clock is the steps' working time, which a test cannot pin, so it
		// is matched rather than compared.
		tail := regexp.MustCompile(`1 step · 1 tool( · \d+s)? ▹$`)
		if !strings.HasPrefix(folded, want) || !tail.MatchString(folded) {
			t.Errorf("w=%d folded row = %q, want prefix %q and a step/tool/clock tail", width, folded, want)
		}
		if got := len([]rune(folded)); got > width {
			t.Errorf("w=%d folded row is %d wide", width, got)
		}

		// Unfold: the open header is a rule ending in the task's duration.
		m.foldTasks = false
		var header string
		for _, l := range transcriptLines(&m) {
			if strings.Contains(l, "1/2 Wire the parser") {
				header = strings.TrimRight(l, " ")
			}
		}
		if !strings.HasPrefix(header, " ✓ 1/2 Wire the parser ─") {
			t.Errorf("w=%d open header = %q", width, header)
		}
		if got := len([]rune(header)); got != width {
			t.Errorf("w=%d open header should fill the frame, is %d wide", width, got)
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

func TestTodoStackPinsEveryTodoAboveTheTranscriptAndFoldsFinishedWork(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 60)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	todos := []db.TodoItem{
		{ID: "t1", Content: "Read", Status: "in_progress", StartedAt: time.Now()},
		{ID: "t2", Content: "Write", Status: "pending"},
		{ID: "t3", Content: "Test", Status: "pending"},
		{ID: "t4", Content: "Ship", Status: "pending"},
	}
	_ = m.state.SetTodos(append([]db.TodoItem(nil), todos...))
	stepUnder(&m, "Reading it. Slowly.", "a")
	todos[0].Status, todos[0].CompletedAt = "completed", time.Now()
	todos[1].Status, todos[1].StartedAt = "in_progress", time.Now()
	_ = m.state.SetTodos(append([]db.TodoItem(nil), todos...))
	stepUnder(&m, "Writing it. Quickly.", "b")

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
	done, open, step := at("1/4 Read"), at("2/4 Write"), at("Writing it.")
	if !(done < open && open < step) {
		t.Fatalf("order must be folded row, open header, then its work:\n%s", text)
	}
	if strings.Contains(text, "Reading it.") {
		t.Errorf("the finished todo's work is folded away:\n%s", text)
	}
	if open != done+1 || step != open+1 {
		t.Errorf("folded row, open header and first step sit together:\n%s", text)
	}
	if strings.Contains(text, "3/4 Test") || strings.Contains(text, "4/4 Ship") {
		t.Errorf("waiting todos live in the pinned band, not the transcript:\n%s", text)
	}

	// The strip is the first thing in the frame and holds the work done and
	// under way; the waiting todos stack in the band below the transcript.
	frame := strings.Split(stripANSI(m.viewString()), "\n")
	rowOf := func(sub string) int {
		for i, l := range frame {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("frame is missing %q:\n%s", sub, strings.Join(frame, "\n"))
		return -1
	}
	for i, w := range []string{"✓ 1/4 Read", "▸ 2/4 Write"} {
		if i >= len(frame) || !strings.Contains(frame[i], w) {
			t.Fatalf("frame row %d should hold %q:\n%s", i, w, strings.Join(frame[:min(8, len(frame))], "\n"))
		}
	}
	if first, last := rowOf("3/4 Test"), rowOf("4/4 Ship"); !(first < last) {
		t.Errorf("the band lists the waiting todos in order (rows %d, %d)", first, last)
	}
	if strip, band := rowOf("1/4 Read"), rowOf("3/4 Test"); !(strip < band) {
		t.Errorf("the strip sits above the band (rows %d, %d)", strip, band)
	}
	if m.todoStripRows() != 2 {
		t.Errorf("strip rows = %d, want 2", m.todoStripRows())
	}
}

func TestTodoStripNotShownWhenTheTurnNeverTouchedTheList(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "A", Status: "pending"}, {ID: "t2", Content: "B", Status: "pending"}})
	m.state.AddMessage(session.RoleUser, "unrelated", session.ContentTypePlain)
	stepUnder(&m, "Just reading. Nothing else.", "a")
	transcriptLines(&m)
	if m.todoStripRows() != 0 {
		t.Fatalf("leftover todos must not pin a strip on an unrelated turn, rows = %d", m.todoStripRows())
	}
}

func TestStripWindowCentersOnTheActiveTodo(t *testing.T) {
	var todos []db.TodoItem
	for i := 1; i <= 10; i++ {
		st := "pending"
		switch {
		case i <= 4:
			st = "completed"
		case i == 5:
			st = "in_progress"
		}
		todos = append(todos, db.TodoItem{ID: fmt.Sprintf("t%d", i), Content: fmt.Sprintf("item %d", i), Status: st})
	}
	v := stripWindow(todos, 6)
	if len(v.rows) != 6 {
		t.Fatalf("rows = %d, want 6", len(v.rows))
	}
	if v.rows[0].text != "3 done" || v.rows[1].index != 4 || v.rows[2].index != 5 || v.rows[len(v.rows)-1].text != "+3 more" {
		t.Fatalf("window = %+v", v.rows)
	}
	// On a short terminal (three rows) the active todo must still be shown,
	// not the one before it.
	short := stripWindow(todos, 3)
	var shown bool
	for _, r := range short.rows {
		if r.todo != nil && r.todo.ID == "t5" {
			shown = true
		}
	}
	if !shown || len(short.rows) > 3 {
		t.Fatalf("a 3-row window must include the active todo and stay in budget: %+v", short.rows)
	}
	for _, maxRows := range []int{3, 4, 5, 6} {
		for active := 1; active <= 10; active++ {
			var l []db.TodoItem
			for i := 1; i <= 10; i++ {
				st := "pending"
				if i < active {
					st = "completed"
				} else if i == active {
					st = "in_progress"
				}
				l = append(l, db.TodoItem{ID: fmt.Sprintf("t%d", i), Content: "x", Status: st})
			}
			w := stripWindow(l, maxRows)
			found := false
			for _, r := range w.rows {
				if r.todo != nil && r.index == active {
					found = true
				}
			}
			if !found || len(w.rows) > maxRows {
				t.Errorf("maxRows=%d active=%d: rows=%d found=%v", maxRows, active, len(w.rows), found)
			}
		}
	}
	if got := stripWindow(todos[:3], 6); len(got.rows) != 3 {
		t.Fatalf("a short list shows whole, got %d rows", len(got.rows))
	}
}

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
	wants := map[viewmodel.Kind][]string{
		viewmodel.KindTask: {"1/3 Read", "2/3 Write"},
		viewmodel.KindStep: {"Writing the first half", "Writing the second half", "One thought", "Another thought"},
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
	if seen < 6 {
		t.Fatalf("expected regions for two tasks and four steps, saw %d:\n%s", seen, strings.Join(lines, "\n"))
	}
	for _, it := range m.browseItems {
		if it.kind != viewmodel.KindTask && it.kind != viewmodel.KindStep {
			continue
		}
		if it.start >= len(lines) || strings.TrimSpace(lines[it.start]) == "" {
			t.Errorf("browse stop %v starts on a blank or missing line %d", it.id, it.start)
		}
	}
}
