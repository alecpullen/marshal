package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

type fakeLSPQ struct {
	refs  []string
	ready bool
}

func (f fakeLSPQ) References(context.Context, string, int, int) ([]string, bool) {
	return f.refs, f.ready
}
func (f fakeLSPQ) Hover(context.Context, string, int, int) (string, bool) { return "sig", f.ready }
func (f fakeLSPQ) Definition(context.Context, string, int, int) ([]string, bool) {
	return f.refs, f.ready
}

func TestReferencesResolvesByName(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	if err := ts.db.SaveSymbols(ts.projectID, []db.Symbol{
		{FilePath: "a.go", Kind: "function", Name: "Foo", LineStart: 5, LineEnd: 7, Source: "lsp"},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}
	ts.lsp = fakeLSPQ{refs: []string{"a.go:5", "b.go:9"}, ready: true}

	res, err := ts.referencesTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Foo"}`)})
	if err != nil {
		t.Fatalf("references handler error: %v", err)
	}
	if !strings.Contains(res.Content, "b.go:9") {
		t.Fatalf("expected b.go:9 in references result, got: %q", res.Content)
	}
}

func TestReferencesUnknownSymbol(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	ts.lsp = fakeLSPQ{ready: true}
	res, err := ts.referencesTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Nope"}`)})
	if err != nil {
		t.Fatalf("references handler error: %v", err)
	}
	if !strings.Contains(res.Content, "no indexed symbol") {
		t.Fatalf("expected 'no indexed symbol' message, got: %q", res.Content)
	}
}

func TestReferencesAmbiguousSymbol(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	if err := ts.db.SaveSymbols(ts.projectID, []db.Symbol{
		{FilePath: "a.go", Kind: "function", Name: "Foo", LineStart: 5, LineEnd: 7, Source: "lsp"},
		{FilePath: "b.go", Kind: "function", Name: "Foo", LineStart: 3, LineEnd: 5, Source: "lsp"},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}
	ts.lsp = fakeLSPQ{refs: []string{"a.go:5"}, ready: true}

	res, err := ts.referencesTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Foo"}`)})
	if err != nil {
		t.Fatalf("references handler error: %v", err)
	}
	if !strings.Contains(res.Content, "ambiguous") {
		t.Fatalf("expected 'ambiguous' message, got: %q", res.Content)
	}
}

func TestReferencesDisambiguatesWithPath(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	if err := ts.db.SaveSymbols(ts.projectID, []db.Symbol{
		{FilePath: "a.go", Kind: "function", Name: "Foo", LineStart: 5, LineEnd: 7, Source: "lsp"},
		{FilePath: "b.go", Kind: "function", Name: "Foo", LineStart: 3, LineEnd: 5, Source: "lsp"},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}
	ts.lsp = fakeLSPQ{refs: []string{"b.go:3"}, ready: true}

	res, err := ts.referencesTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Foo","path":"b.go"}`)})
	if err != nil {
		t.Fatalf("references handler error: %v", err)
	}
	if !strings.Contains(res.Content, "b.go:3") {
		t.Fatalf("expected b.go:3 in references result, got: %q", res.Content)
	}
}

func TestReferencesNoLSP(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	if err := ts.db.SaveSymbols(ts.projectID, []db.Symbol{
		{FilePath: "a.go", Kind: "function", Name: "Foo", LineStart: 5, LineEnd: 7, Source: "lsp"},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}
	// lsp is nil — no language server available
	res, err := ts.referencesTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Foo"}`)})
	if err != nil {
		t.Fatalf("references handler error: %v", err)
	}
	if !strings.Contains(res.Content, "no language server") {
		t.Fatalf("expected 'no language server' message, got: %q", res.Content)
	}
}

func TestDefinitionResolvesByName(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	if err := ts.db.SaveSymbols(ts.projectID, []db.Symbol{
		{FilePath: "a.go", Kind: "function", Name: "Bar", LineStart: 10, LineEnd: 12, Source: "lsp"},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}
	ts.lsp = fakeLSPQ{refs: []string{"a.go:10"}, ready: true}

	res, err := ts.definitionTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Bar"}`)})
	if err != nil {
		t.Fatalf("definition handler error: %v", err)
	}
	if !strings.Contains(res.Content, "a.go:10") {
		t.Fatalf("expected a.go:10 in definition result, got: %q", res.Content)
	}
}

func TestHoverResolvesByName(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	if err := ts.db.SaveSymbols(ts.projectID, []db.Symbol{
		{FilePath: "a.go", Kind: "function", Name: "Baz", LineStart: 15, LineEnd: 17, Source: "lsp"},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}
	ts.lsp = fakeLSPQ{ready: true}

	res, err := ts.hoverTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Baz"}`)})
	if err != nil {
		t.Fatalf("hover handler error: %v", err)
	}
	if !strings.Contains(res.Content, "sig") {
		t.Fatalf("expected 'sig' in hover result, got: %q", res.Content)
	}
}

func TestHoverNoLSP(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	if err := ts.db.SaveSymbols(ts.projectID, []db.Symbol{
		{FilePath: "a.go", Kind: "function", Name: "Baz", LineStart: 15, LineEnd: 17, Source: "lsp"},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}
	// lsp is nil
	res, err := ts.hoverTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"symbol":"Baz"}`)})
	if err != nil {
		t.Fatalf("hover handler error: %v", err)
	}
	if !strings.Contains(res.Content, "no language server") {
		t.Fatalf("expected 'no language server' message, got: %q", res.Content)
	}
}
