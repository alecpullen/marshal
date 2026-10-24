package tui

import (
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
	out = ansi.Strip(renderCompletion(failed, true, 80, ""))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "#3") || !strings.Contains(lines[0], "failed") {
		t.Fatalf("failed completion = %q", out)
	}
	if !strings.Contains(lines[1], "budget exhausted") {
		t.Fatalf("summary line = %q", lines[1])
	}
	if !strings.HasSuffix(lines[0], glyph.DisclosureExpanded) {
		t.Fatalf("expanded head must end with the disclosure glyph: %q", lines[0])
	}
}
