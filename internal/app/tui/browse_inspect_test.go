package tui

import (
	"testing"
	"time"

	"marshal/internal/viewmodel"
)

// The inspector is where the whole retained output lives: the report in the
// transcript is a bounded tail, and `i` is how a reader gets the rest.
func TestInspectorShowsACompletionsFullOutput(t *testing.T) {
	code := 1
	output := "line one\nline two\nline three"
	c := &viewmodel.CompletionInfo{
		Source: viewmodel.CompletionJob, ID: "job-3", Label: "Harness tests",
		Outcome: "exit 1", Failed: true, ExitCode: &code,
		Duration: 48 * time.Second, Output: output,
	}
	m := newTestModel(t)
	d := m.completionDetail(c)
	if d.Title != "job-3" || d.Subject != "Harness tests" {
		t.Fatalf("detail = %+v", d)
	}
	var got string
	for _, s := range d.Sections {
		if s.Name == "output" {
			got = s.Body
		}
	}
	if got != viewmodel.ScrubBlock(output) {
		t.Fatalf("output section = %q", got)
	}
	found := false
	for _, f := range d.Fields {
		if f.Key == "exit" && f.Value == "1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no exit field: %+v", d.Fields)
	}
}

// `y` on an event copies its head line and the output behind it, so a failure
// can be pasted into an issue without retyping the command.
func TestCopyTakesTheCompletionHeadAndOutput(t *testing.T) {
	c := &viewmodel.CompletionInfo{
		Source: viewmodel.CompletionJob, ID: "job-3", Label: "Harness tests",
		Outcome: "exit 1", Failed: true, Duration: 48 * time.Second,
		Measure: "210 passed, 2 failed",
		Output:  "--- FAIL: TestThresholds",
	}
	m := newTestModel(t)
	n := &viewmodel.Node{
		ID:         viewmodel.NodeID{Kind: viewmodel.KindCompletion, Key: "completion:job-3"},
		Kind:       viewmodel.KindCompletion,
		Completion: c,
	}
	want := "job-3 · Harness tests · exit 1 · 210 passed, 2 failed · 48s\n\n--- FAIL: TestThresholds"
	if got := m.nodeCopyText(n); got != want {
		t.Fatalf("copy = %q, want %q", got, want)
	}
}
