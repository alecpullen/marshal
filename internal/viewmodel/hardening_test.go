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

// BandOf walks a caller-built tree. Build only ever sets Job on a tool node
// with exactly one run, but a tree that has been rebuilt, filtered or merged
// can hold a job whose runs are gone: the band must report the card, not index
// an empty slice.
func TestBandOfSurvivesAJobCardWithNoRun(t *testing.T) {
	job := &Node{ID: NodeID{KindTool, "tool:1"}, Kind: KindTool,
		Job: &JobState{ID: "job-9", Command: "pytest \x1b]0;pwned\x07", Running: true}}
	b := BandOf(Snapshot{}, []*Node{job})
	if len(b.Entities) != 1 || b.Entities[0].Label != "job-9" {
		t.Fatalf("entities = %+v", b.Entities)
	}
	if !b.Entities[0].Started.IsZero() {
		t.Fatalf("a job with no run has no start time: %v", b.Entities[0].Started)
	}
	// The command is the model's own shell text and this is the row the band
	// shows; the window-title sequence must not ride along.
	if strings.Contains(b.Entities[0].Detail, "\x1b") {
		t.Fatalf("detail = %q", b.Entities[0].Detail)
	}
}

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
