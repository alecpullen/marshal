package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/activity"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestProjectNotebookActivityPriority(t *testing.T) {
	now := time.Unix(100, 0)
	cases := []struct {
		name string
		in   notebookActivityInput
		want string
	}{
		{"approval wins", notebookActivityInput{approval: true, question: true, activity: session.Activity{Kind: session.ActivityApproval, Label: "shell.run"}}, "Needs approval"},
		{"question", notebookActivityInput{question: true}, "Needs answer"},
		{"reconnect", notebookActivityInput{activity: session.Activity{Kind: session.ActivityReconnecting}}, "Reconnecting"},
		{"tool", notebookActivityInput{hasTool: true, tool: session.ActiveToolCall{Name: "shell.run", StartedAt: now}, now: now.Add(3 * time.Second)}, "Running tool"},
		{"generating", notebookActivityInput{busy: true, activity: session.Activity{Kind: session.ActivityThinking, Label: "PRIVATE THINKING"}}, "Generating"},
		{"children", notebookActivityInput{children: 2}, "Working"},
		{"jobs", notebookActivityInput{jobs: 1}, "Idle"},
		{"idle", notebookActivityInput{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectNotebookActivity(tc.in); got.status != tc.want {
				t.Fatalf("status = %q, want %q", got.status, tc.want)
			}
		})
	}
}

func TestNotebookActivityUsesCurrentActionAndFailureSupersedesIt(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "Inspect the parser", session.ContentTypePlain)
	boundary := state.Transcript()[0].Message.ID
	state.BeginActivityRun(boundary)
	response := state.BeginActivityResponse()
	headline, action := "Reviewing parser", "Checking the parser tests"
	response = state.BindActivityNarration(response, "The earlier prose said I was checking the parser tests.")
	state.AddNarrationMessage(response, "The earlier prose said I was checking the parser tests.")
	receipt, err := state.ApplyPublicProgress(response, activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: &headline, CurrentAction: &action})
	if err != nil {
		t.Fatal(err)
	}
	response = receipt.Owner
	state.SetActivity(session.Activity{Kind: session.ActivityThinking})
	m := New(state)
	m.notebookView = true
	m.busy = true
	m.resize(80, 24)
	if got := m.notebookActivitySnapshot().currentAction; got != action {
		t.Fatalf("current action = %q, want %q", got, action)
	}
	if row := stripANSI(m.renderNotebookActivity()); !strings.Contains(row, action) {
		t.Fatalf("pre-failure activity row = %q, want current action %q", row, action)
	}
	call := state.BeginActivityCall(response, "provider-call")
	state.LogToolCall(registry.AuditEvent{Activity: call, ToolName: "shell.run", Error: "exit status 1", CommandExitCode: intPtr(1)})
	failed := m.notebookActivitySnapshot()
	if failed.currentAction != "" || !failed.suppressNarration {
		t.Fatalf("failed intent snapshot = %+v, want a neutral fallback with stale narration suppressed", failed)
	}
	row := stripANSI(m.renderNotebookActivity())
	if !strings.Contains(row, "Generating") || strings.Contains(row, "Checking the parser") || strings.Contains(row, "Reviewing parser") || strings.Contains(row, "earlier prose") {
		t.Fatalf("activity row retained stale intent after the failure: %q", row)
	}
}

func intPtr(value int) *int { return &value }

func TestNotebookActivityDoesNotRenderThinkingLabel(t *testing.T) {
	m := newTestModel(t)
	m.notebookView = true
	m.busy = true
	m.state.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "RECOGNIZABLE PRIVATE THINKING SENTENCE"})
	got := stripANSI(m.renderNotebookActivity())
	if !strings.Contains(got, "Generating") {
		t.Fatalf("row = %q, want generating", got)
	}
	if strings.Contains(got, "RECOGNIZABLE PRIVATE THINKING SENTENCE") {
		t.Fatalf("private thinking leaked into row: %q", got)
	}
}

func TestNotebookActivityUsesDrilledChildJobCount(t *testing.T) {
	m := newTestModel(t)
	childState := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	child := m.state.RegisterSubagent("worker", childState)
	m.viewStack = append(m.viewStack, child)
	m.state.SetRunningJobsCount(5)
	m.jobCount = 5
	m.viewStack = nil
	if got := m.notebookActivitySnapshot().jobs; got != 5 {
		t.Fatalf("root jobs = %d, want root count 5", got)
	}
	m.viewStack = append(m.viewStack, child)

	if got := m.notebookActivitySnapshot().jobs; got != 0 {
		t.Fatalf("drilled child jobs = %d, want child's zero count", got)
	}
	childState.SetRunningJobsCount(2)
	if got := m.notebookActivitySnapshot().jobs; got != 2 {
		t.Fatalf("drilled child jobs = %d, want 2", got)
	}
	if got := projectNotebookActivity(m.notebookActivitySnapshot()); got.status != "Idle" || got.label != "2 background job(s) active" {
		t.Fatalf("child activity = %+v, want idle agent with two child jobs", got)
	}
}

func TestNotebookActivityMatchesParentChildQuestionToViewedChild(t *testing.T) {
	m := newTestModel(t)
	childState := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	child := m.state.RegisterSubagent("worker", childState)
	m.viewStack = append(m.viewStack, child)
	childState.SetActiveToolCall(session.ActiveToolCall{Name: "parent.ask"})
	m.state.PushChildQuestion(&session.PendingChildQuestion{ChildID: child.ID, Questions: []session.Question{{Question: "Which option?"}}})

	activity := projectNotebookActivity(m.notebookActivitySnapshot())
	if activity.status != "Needs answer" {
		t.Fatalf("matching child activity = %+v, want Needs answer above parent.ask", activity)
	}

	// A question queued by another child must not bleed into this child view.
	m.state.ResolveChildQuestion(m.state.PendingChildQuestion())
	m.state.PushChildQuestion(&session.PendingChildQuestion{ChildID: child.ID + 1})
	activity = projectNotebookActivity(m.notebookActivitySnapshot())
	if activity.status != "Running tool" || activity.label != "parent.ask" {
		t.Fatalf("unrelated child question activity = %+v, want child tool status", activity)
	}
}
