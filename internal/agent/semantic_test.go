package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/contextpack"
	"marshal/internal/retrieval"
)

type fakeSource struct{ cands []retrieval.Candidate }

func (f fakeSource) Name() string { return "semantic" }
func (f fakeSource) Retrieve(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
	return f.cands, nil
}

func TestRetrieveSemanticContext(t *testing.T) {
	src := fakeSource{cands: []retrieval.Candidate{{FilePath: "a.go", StartLine: 1, EndLine: 2, Content: "func A(){}"}}}
	snips := retrieveSemanticContext(context.Background(), "find A", src)
	if len(snips) != 1 || snips[0].Path != "a.go" {
		t.Fatalf("snips = %#v", snips)
	}

	// nil source → nil snippets (graceful-off).
	if got := retrieveSemanticContext(context.Background(), "x", nil); got != nil {
		t.Fatalf("nil source should yield nil, got %#v", got)
	}

	// Empty snippets produce no semantic section.
	pack := contextpack.MergeSemanticContext(contextpack.Pack{}, retrieveSemanticContext(context.Background(), "x", nil), contextpack.DefaultMaxTokens, nil)
	for _, s := range pack.Sections {
		if s.Kind == contextpack.SectionSemantic {
			t.Fatal("expected no semantic section for nil source")
		}
	}
}

// recordingSource captures query text and returns scripted candidates.
type recordingSource struct {
	queries []string
	cands   []retrieval.Candidate
}

func (r *recordingSource) Name() string { return "semantic" }
func (r *recordingSource) Retrieve(_ context.Context, q retrieval.Query) ([]retrieval.Candidate, error) {
	r.queries = append(r.queries, q.Text)
	return r.cands, nil
}

func TestRequeryTrackerCountsNewPathsOnce(t *testing.T) {
	tr := newSemanticRequeryTracker()
	tr.note([]string{"a.go", "b.go"})
	tr.note([]string{"a.go"}) // dup — not counted again
	if len(tr.pending) != 2 {
		t.Fatalf("pending = %v, want [a.go b.go]", tr.pending)
	}
}

func TestMaybeRequerySemanticFiresAtThreshold(t *testing.T) {
	src := &recordingSource{}
	r := &Runner{State: newTestState(t)}
	r.semTracker = newSemanticRequeryTracker()
	r.semTracker.note([]string{"internal/agent/runner.go", "internal/agent/chat.go"})

	// Below threshold: no query.
	r.maybeRequerySemantic(context.Background(), "improve the runner", src, 0)
	if len(src.queries) != 0 {
		t.Fatalf("query fired below threshold: %v", src.queries)
	}

	// Third new path: one query, goal + basenames.
	r.semTracker.note([]string{"internal/agent/route.go"})
	r.maybeRequerySemantic(context.Background(), "improve the runner", src, 0)
	if len(src.queries) != 1 {
		t.Fatalf("queries = %v, want exactly 1", src.queries)
	}
	q := src.queries[0]
	for _, want := range []string{"improve the runner", "runner.go", "chat.go", "route.go"} {
		if !strings.Contains(q, want) {
			t.Fatalf("query %q missing %q", q, want)
		}
	}
	// Pending consumed: next call with no new paths is a no-op.
	r.maybeRequerySemantic(context.Background(), "improve the runner", src, 0)
	if len(src.queries) != 1 {
		t.Fatalf("queries = %v, want still 1 (pending consumed)", src.queries)
	}
}

func TestMaybeRequerySemanticDropsSeenPaths(t *testing.T) {
	src := &recordingSource{cands: []retrieval.Candidate{
		{FilePath: "old.go", Content: "dup"},   // must be dropped
		{FilePath: "new.go", Content: "fresh"}, // must survive
	}}
	r := &Runner{State: newTestState(t)}
	r.semTracker = newSemanticRequeryTracker()
	// "old.go" is already represented in the pack via an earlier snippet merge.
	r.semTracker.addSnippets([]contextpack.FileSnippet{{Path: "old.go", Content: "x"}})
	r.semTracker.note([]string{"p1", "p2", "p3"})
	r.maybeRequerySemantic(context.Background(), "goal", src, 0)
	for _, s := range r.semTracker.snippets {
		if s.Path == "old.go" && s.Content == "dup" {
			t.Fatal("duplicate snippet for seen path was merged")
		}
	}
	found := false
	for _, s := range r.semTracker.snippets {
		if s.Path == "new.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("fresh snippet was not merged")
	}
}

func TestMaybeRequerySemanticNilSourceNoop(t *testing.T) {
	r := &Runner{State: newTestState(t)}
	r.semTracker = newSemanticRequeryTracker()
	r.semTracker.note([]string{"a", "b", "c"})
	r.maybeRequerySemantic(context.Background(), "goal", nil, 0) // must not panic
	if len(r.semTracker.pending) != 0 {
		t.Fatal("pending must be consumed even when the source is nil")
	}
}
