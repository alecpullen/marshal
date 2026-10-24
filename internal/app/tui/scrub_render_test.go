package tui

import (
	"strings"
	"testing"

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

	completion := renderCompletion(&viewmodel.CompletionInfo{Source: viewmodel.CompletionSubagent, ID: "#3", Label: esc}, false, 80, "")
	stray("completion", completion)

	// Expanded, the report prints the job's command and output tail — the same
	// model-authored text, one layer deeper.
	expanded := renderCompletion(&viewmodel.CompletionInfo{
		Source:  viewmodel.CompletionJob,
		ID:      "job-1",
		Label:   esc,
		Command: "make " + esc,
		Summary: esc,
		Output:  "line one\n" + esc + "\nline three",
	}, true, 80, "")
	stray("expanded completion", expanded)
	if !strings.Contains(expanded, "line three") {
		t.Fatalf("expanded completion dropped the output's last line: %q", expanded)
	}
}
