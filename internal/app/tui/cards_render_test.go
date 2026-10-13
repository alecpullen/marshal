package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

func TestJobCardIsOneLineAndSettles(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "shell.run", Args: []byte(`{"command":"cd /w && pytest -q --junitxml=/tmp/a.xml 2>&1 | tail -40"}`), ResultSummary: "started background job job-4"}
	row := &viewmodel.Node{Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev},
		Job: &viewmodel.JobState{ID: "job-4", Command: "pytest -q --junitxml=/tmp/a.xml 2>&1 | tail -40", Running: true}}
	out := ansi.Strip(renderJobCard(row, "⠋", time.Now(), false, 60, ""))
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 || !strings.Contains(out, "job-4") || !strings.Contains(out, "running") {
		t.Fatalf("running card = %q", out)
	}
	row.Job.Running, row.Job.ExitCode, row.Job.Duration = false, 1, 122*time.Second
	out = ansi.Strip(renderJobCard(row, "", time.Now(), false, 60, ""))
	if !strings.Contains(out, "exit 1") || !strings.Contains(out, "2m") {
		t.Fatalf("settled card = %q", out)
	}
}

// A collapsed row is one line. The transcript is a list of what happened, and
// a wrapped row costs the reader a line for a detail they did not ask for.
func TestCollapsedShellRowIsOneLine(t *testing.T) {
	code := 0
	ev := registry.AuditEvent{ToolName: "shell.run", CommandExitCode: &code,
		Args:          []byte(`{"command":"` + strings.Repeat("echo hello; ", 30) + `"}`),
		ResultContent: "x"}
	out := ansi.Strip(renderCompletedToolCall(ev, false, nil, 80))
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 || !strings.Contains(out, "…") {
		t.Fatalf("collapsed row = %q", out)
	}
	// Expanded rows keep the wrap: that is where the reader asked for detail.
	if got := ansi.Strip(renderCompletedToolCall(ev, true, nil, 80)); strings.Count(got, "\n") < 2 {
		t.Fatalf("expanded row should wrap:\n%s", got)
	}
}

// A command that changes into the session's working tree says so at the front
// of every shell row; the path is long and already implied by the row's
// context, so it collapses to ⌂.
func TestShellRowElidesTheWorktree(t *testing.T) {
	root := "/home/a/proj/.marshal/worktrees/x"
	step := func() *viewmodel.Node {
		ev := registry.AuditEvent{ToolName: "shell.run", ToolCallID: "c1", Timestamp: time.Unix(100, 0),
			ResultSummary: "ok", Args: []byte(`{"command":"cd ` + root + ` && pytest -q"}`)}
		row := &viewmodel.Node{ID: viewmodel.ToolID(ev), Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev}}
		return &viewmodel.Node{ID: viewmodel.NodeID{Kind: viewmodel.KindStep, Key: "step:1"}, Kind: viewmodel.KindStep,
			Step: &viewmodel.StepInfo{Step: session.Step{ID: 1}}, Children: []*viewmodel.Node{row}}
	}

	out, _ := renderStep(step(), &stepRenderCtx{workdir: root}, 100, densitySteps)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "⌂ pytest -q") {
		t.Fatalf("row = %q", plain)
	}
	if strings.Contains(plain, root) {
		t.Fatalf("the worktree path should be gone: %q", plain)
	}
	// With no workdir known, the command stays exactly as the model wrote it.
	out, _ = renderStep(step(), &stepRenderCtx{}, 100, densitySteps)
	if got := ansi.Strip(out); !strings.Contains(got, "cd "+root) {
		t.Fatalf("row = %q", got)
	}
}

// The job card's command gets the same treatment: it is the same shell text.
func TestJobCardElidesTheWorktree(t *testing.T) {
	root := "/home/a/proj/.marshal/worktrees/x"
	ev := registry.AuditEvent{ToolName: "shell.run", Args: []byte(`{"command":"cd ` + root + ` && pytest -q"}`),
		ResultSummary: "started background job job-4"}
	row := &viewmodel.Node{Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev},
		Job: &viewmodel.JobState{ID: "job-4", Command: "cd " + root + " && pytest -q", Running: true}}
	out := ansi.Strip(renderJobCard(row, "⠋", time.Now(), false, 100, root))
	if !strings.Contains(out, "⌂ pytest -q") || strings.Contains(out, root) {
		t.Fatalf("card = %q", out)
	}
}

func TestMarkerLine(t *testing.T) {
	m := &viewmodel.MarkerInfo{Labels: []string{"#3 Pass 1 Task 1"}, Dur: 81 * time.Second}
	if out := ansi.Strip(renderMarker(m, 80)); !strings.Contains(out, "#3 Pass 1 Task 1 done") || !strings.Contains(out, "1m") {
		t.Fatalf("marker = %q", out)
	}
	m = &viewmodel.MarkerInfo{Labels: []string{"#3", "#5"}, Failed: true}
	if out := ansi.Strip(renderMarker(m, 80)); !strings.Contains(out, "#3 #5") {
		t.Fatalf("merged marker = %q", out)
	}
}

func TestRunningSubagentCardHasNoPaddingOrFooter(t *testing.T) {
	v := session.SubagentView{ID: 3, Label: "Pass 1 Task 1", Status: session.SubagentRunning, StartedAt: time.Now()}
	out := ansi.Strip(renderSubagentCard(v, false, "⠋", regionView{minRows: 6}, 80))
	if strings.Contains(out, "ctrl+f") {
		t.Fatalf("no drill-in footer:\n%s", out)
	}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(strings.Trim(l, "▌┃│ ")) == "" {
			t.Fatalf("no blank padding rows:\n%s", out)
		}
	}
}

func TestSettledSubagentSummaryHasNoMarkdownHeading(t *testing.T) {
	v := session.SubagentView{ID: 2, Label: "Task 2", Status: session.SubagentDone, Summary: "## Task 2 complete — AccessibleModal\n\nDetails."}
	if out := ansi.Strip(renderSubagentCard(v, false, "", regionView{}, 80)); strings.Contains(out, "##") {
		t.Fatalf("markdown leaked:\n%s", out)
	}
}

func TestStripMarkdownHeadingKeepsHashText(t *testing.T) {
	for in, want := range map[string]string{
		"## Task 2 complete": "Task 2 complete",
		"# Done":             "Done",
		"#3 done":            "#3 done",
		"####### seven":      "####### seven",
		"plain":              "plain",
	} {
		if got := stripMarkdownHeading(in); got != want {
			t.Errorf("stripMarkdownHeading(%q) = %q, want %q", in, got, want)
		}
	}
}
