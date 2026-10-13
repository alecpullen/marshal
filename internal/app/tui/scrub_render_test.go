package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// strayEscapes returns the terminal escape sequences in s that the model, not
// the renderer, put there.
//
// A renderer colours its own output, so "no ESC byte" cannot be the assertion:
// a correct render of a job card is full of its own CSI styling, and a test
// that banned the byte would fail on correct output and could only be made to
// pass by stripping the render first — which is exactly what hides a leak. The
// sequences below are all ESC-introduced; the renderer's own styling is SGR
// (a CSI ending in 'm'), so anything else is the model's.
func strayEscapes(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		j := i + 1
		if j < len(s) && s[j] == '[' {
			j++
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
		}
		if j >= len(s) {
			out = append(out, s[i:])
			break
		}
		if s[j] != 'm' {
			out = append(out, s[i:j+1])
		}
		i = j
	}
	return out
}

// These renderers print model-authored text — a subagent's label, a todo's
// content, a job's command and output — directly to the user's terminal. An
// OSC 52 in any of them writes the clipboard; OSC 0 rewrites the window title.
//
// The assertions are deliberately made against the RAW render, never through
// ansi.Strip: Strip removes escape sequences, so a stripped assertion would
// pass whether or not the renderer leaked them.
func TestRenderersDropTerminalControlSequences(t *testing.T) {
	const esc = "\x1b]52;c;aGVsbG8=\x07"

	stray := func(what, rendered string) {
		t.Helper()
		if seqs := strayEscapes(rendered); len(seqs) != 0 {
			t.Fatalf("%s carried the model's own escapes %q: %q", what, seqs, rendered)
		}
	}

	marker := renderMarker(&viewmodel.MarkerInfo{Labels: []string{"#3 " + esc}}, 80)
	stray("marker", marker)

	divider := renderTodoDivider(&viewmodel.TodoLabel{Index: 3, Total: 9, Content: esc + "AccessibleModal"}, 80)
	stray("divider", divider)

	ev := registry.AuditEvent{ToolName: "shell.run", Args: []byte(`{"command":"pytest -q"}`),
		ResultSummary: "started background job job-4"}
	row := &viewmodel.Node{Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev},
		Job: &viewmodel.JobState{ID: "job-4", Command: "echo " + esc, Running: true}}
	stray("job card", renderJobCard(row, "⠋", time.Now(), false, 80, ""))

	// The expanded body is the job's own output: escaping it must not cost it
	// its lines.
	row.Job.Running, row.Job.Output = false, "line one\n"+esc+"\nline three"
	expanded := renderJobCard(row, "", time.Now(), true, 80, "")
	stray("expanded job output", expanded)
	if !strings.Contains(expanded, "line three") {
		t.Fatalf("the output's own lines must survive scrubbing: %q", expanded)
	}

	// The subagent card is a live region: it repaints on every spinner tick,
	// so a leak here is a leak many times a second.
	sub := session.SubagentView{ID: 3, Label: esc + "Task 1", Status: session.SubagentDone,
		CurrentTool: esc + "file.read", Summary: esc + "Finished the parser.\nThen the tests."}
	stray("subagent card", renderSubagentCard(sub, false, "", regionView{}, 80))
}

// A caller-built tree is not guaranteed to have the start call attached to a
// running job. The row must render with the elapsed time unknown rather than
// index an empty slice.
func TestJobCardToleratesAJobWithNoRunAttached(t *testing.T) {
	row := &viewmodel.Node{Kind: viewmodel.KindTool,
		Job: &viewmodel.JobState{ID: "job-9", Command: "pytest -q", Running: true}}
	out := renderJobCard(row, "⠋", time.Now(), false, 80, "")
	if !strings.Contains(out, "job-9") || !strings.Contains(out, "running") {
		t.Fatalf("card = %q", out)
	}
}
