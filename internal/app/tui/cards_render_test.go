package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// A collapsed row is one line. The transcript is a list of what happened, and
// a wrapped row costs the reader a line for a detail they did not ask for.
func TestCollapsedShellRowIsOneLine(t *testing.T) {
	code := 0
	ev := registry.AuditEvent{ToolName: "shell.run", CommandExitCode: &code,
		Args:          []byte(`{"command":"` + strings.Repeat("echo hello; ", 30) + `"}`),
		ResultContent: "x"}
	out := ansi.Strip(renderCallRow(ev, false, nil, 80, ""))
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 || !strings.Contains(out, "…") {
		t.Fatalf("collapsed row = %q", out)
	}
	// Expanded rows keep the wrap: that is where the reader asked for detail.
	if got := ansi.Strip(renderCallRow(ev, true, nil, 80, "")); strings.Count(got, "\n") < 2 {
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

	out, _ := renderStep(step(), &stepRenderCtx{workdir: root}, 100, false)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "⌂ pytest -q") {
		t.Fatalf("row = %q", plain)
	}
	if strings.Contains(plain, root) {
		t.Fatalf("the worktree path should be gone: %q", plain)
	}
	// With no workdir known, the command stays exactly as the model wrote it.
	out, _ = renderStep(step(), &stepRenderCtx{}, 100, false)
	if got := ansi.Strip(out); !strings.Contains(got, "cd "+root) {
		t.Fatalf("row = %q", got)
	}
}

// A background job's launch row is an ordinary call row: the verb, the command
// and the job it started, on one line.
func TestJobLaunchRowNamesTheJob(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "shell.run", ToolCallID: "c1", Timestamp: time.Unix(100, 0),
		Args:          []byte(`{"command":"pytest -q","background":true}`),
		ResultSummary: session.JobStartPrefix + "job-4",
		Notice: registry.NewCommandExecutionNotice(registry.CommandExecution{
			ExecutionID: "exec-4", DisplayID: "job-4", ConversationID: "conv",
			Attachment: registry.ExecutionAttachmentBackground, State: "running",
		})}
	out := ansi.Strip(renderCallRow(ev, false, nil, 80, ""))
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
		t.Fatalf("launch row is not one line:\n%s", out)
	}
	for _, want := range []string{"shell", "pytest -q", glyph.Job + " job-4", "started"} {
		if !strings.Contains(out, want) {
			t.Errorf("launch row %q missing %q", out, want)
		}
	}
}

func TestCompletionLine(t *testing.T) {
	done := &viewmodel.CompletionInfo{Source: viewmodel.CompletionJob, ID: "job-4", Label: "Harness tests",
		Outcome: viewmodel.OutcomeDone, Duration: 81 * time.Second}
	out := ansi.Strip(renderCompletion(done, false, 80, ""))
	if !strings.Contains(out, "job-4 Harness tests") || !strings.Contains(out, "done") || !strings.Contains(out, "1m") {
		t.Fatalf("completion = %q", out)
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), glyph.DisclosureCollapsed) {
		t.Fatalf("collapsed head must end with the disclosure glyph: %q", out)
	}

	failed := &viewmodel.CompletionInfo{Source: viewmodel.CompletionSubagent, ID: "#3", Label: "review",
		Outcome: viewmodel.OutcomeFailed, Failed: true, Summary: "budget exhausted"}
	// Collapsed, the summary line stands in for the report.
	collapsed := ansi.Strip(renderCompletion(failed, false, 80, ""))
	lines := strings.Split(strings.TrimRight(collapsed, "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "#3") || !strings.Contains(lines[0], "failed") {
		t.Fatalf("failed completion = %q", collapsed)
	}
	if !strings.Contains(lines[1], "budget exhausted") {
		t.Fatalf("summary line = %q", lines[1])
	}
	// Expanded, the report replaces the summary line.
	out = ansi.Strip(renderCompletion(failed, true, 80, ""))
	head := strings.Split(strings.TrimRight(out, "\n"), "\n")[0]
	if !strings.HasSuffix(head, glyph.DisclosureExpanded) {
		t.Fatalf("expanded head must end with the disclosure glyph: %q", head)
	}
}

// An expanded event is the issue report: what ran, where, how long, how far it
// got, and the output tail that says why it failed.
func TestCompletionReportShowsTheFailure(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	lines[12] = "--- FAIL: TestThresholdsForCoderModels/context_window"
	code := 1
	c := &viewmodel.CompletionInfo{
		Source: viewmodel.CompletionJob, ID: "job-3", Label: "Harness tests",
		Outcome: "exit 1", Failed: true, ExitCode: &code,
		Duration: 48 * time.Second, Measure: "210 passed, 2 failed",
		Command: "go test ./...", Cwd: "/home/a/proj",
		Output: strings.Join(lines, "\n"),
	}
	out := ansi.Strip(renderCompletion(c, true, 80, ""))
	for _, want := range []string{"outcome", "command", "cwd", "duration", "progress", "output"} {
		if !strings.Contains(out, want) {
			t.Errorf("report %q missing %q", out, want)
		}
	}
	// The failure line leads the output tail, on the key's line.
	tail := out[strings.Index(out, "output"):]
	first := strings.Split(strings.TrimPrefix(tail, "output"), "\n")[0]
	if !strings.Contains(first, "--- FAIL") {
		t.Fatalf("first output line = %q", first)
	}
	// The tail is bounded: at most ReportTailLines output lines show.
	if got := strings.Count(tail, "line "); got > viewmodel.ReportTailLines {
		t.Fatalf("%d output lines shown", got)
	}
}

// A subagent's event carries its task and the report it wrote.
func TestCompletionReportShowsASubagentsReport(t *testing.T) {
	c := &viewmodel.CompletionInfo{
		Source: viewmodel.CompletionSubagent, ID: "#3", Label: "review the config change",
		Outcome: viewmodel.OutcomeDone, Duration: 62 * time.Second,
		Report: "The config change is sound.",
	}
	out := ansi.Strip(renderCompletion(c, true, 80, ""))
	if !strings.Contains(out, "task") || !strings.Contains(out, "review the config change") {
		t.Fatalf("report = %q", out)
	}
	if !strings.Contains(out, "The config change is sound.") {
		t.Fatalf("report body = %q", out)
	}
}

// A job that ended with the session has no duration to report, and saying so
// is more honest than a zero.
func TestLostJobReportSaysItEndedWithTheSession(t *testing.T) {
	c := &viewmodel.CompletionInfo{
		Source: viewmodel.CompletionJob, ID: "job-7", Label: "Download",
		Outcome: viewmodel.OutcomeLost, Failed: true,
	}
	out := ansi.Strip(renderCompletion(c, true, 80, ""))
	if !strings.Contains(out, "ended with session") {
		t.Fatalf("report = %q", out)
	}
}
