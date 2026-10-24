package viewmodel

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

// These pin the hardening pass: exported helpers that Build happens to call
// with well-formed input, but that other callers reach with trees Build did
// not make. None of them may panic, and the text they carry is model-authored,
// so none of it may reach a terminal as an escape sequence.

// A completion event's text is model-authored — a subagent's label and report,
// a job's command and output — so the node is scrubbed where it is built.
func TestCompletionNodeScrubsModelText(t *testing.T) {
	esc := "\x1b[31mred\x1b[0m"
	n := completionNode(CompletionInfo{
		Source: CompletionSubagent, ID: "#3", Label: esc, Summary: esc,
		Output: esc, Report: esc,
	}, "sub:3")
	if n.ID.Kind != KindCompletion || n.ID.Key != "completion:sub:3" {
		t.Fatalf("id = %v", n.ID)
	}
	for name, got := range map[string]string{
		"label": n.Completion.Label, "summary": n.Completion.Summary,
		"output": n.Completion.Output, "report": n.Completion.Report,
	} {
		if strings.Contains(got, "\x1b") {
			t.Errorf("%s = %q, want the escape sequence gone", name, got)
		}
	}
}

// A beat's header is picked from its narration. A caller-built tree can hold a
// nil entry, and a header must never be the thing that panics a renderer.
func TestStepHeadlineSkipsNilNarration(t *testing.T) {
	si := &StepInfo{Narration: []*session.Message{nil, {Content: "Parser first. Then the rest."}}}
	head, rest := StepHeadline(si)
	if head != "Parser first." || rest != "Then the rest." {
		t.Fatalf("head = %q, rest = %q", head, rest)
	}
	if h, r := StepHeadline(&StepInfo{Narration: []*session.Message{nil}}); h != "" || r != "" {
		t.Fatalf("a beat of nil narration is headless, got %q / %q", h, r)
	}
}
