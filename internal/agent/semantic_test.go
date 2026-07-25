package agent

import (
	"context"
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
