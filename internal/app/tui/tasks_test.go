package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
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
