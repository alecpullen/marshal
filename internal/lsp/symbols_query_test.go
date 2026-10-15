package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
)

func newQueryHarness(t *testing.T, files map[string]string) (*SymbolQuery, *scripted, string) {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	c, s := newScripted(t)
	m := NewManager(root, map[string]ServerSpec{"go": {}}, nil)
	m.start = func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error) {
		return c, func() {}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Run(ctx)
	if !m.WaitReady(ctx, "go", 2*time.Second) {
		t.Fatal("harness server not ready")
	}
	q := NewSymbolQuery(NewHandle(m, nil, nil), nil, 0)
	q.wait = 200 * time.Millisecond
	return q, s, root
}

func symJSON(root, name, container, path string, line int) string {
	return fmt.Sprintf(`{"name":%q,"kind":6,"containerName":%q,"location":{"uri":%q,"range":{"start":{"line":%d,"character":0},"end":{"line":%d,"character":1}}}}`,
		name, container, toFileURI(root, path), line, line)
}

func TestDefinitionByQualifiedNameQueriesIdentifierColumn(t *testing.T) {
	src := "package a\n\ntype Runner struct{}\n\nfunc (r *Runner) RunTask() {}\n"
	q, s, root := newQueryHarness(t, map[string]string{"a/r.go": src})
	s.handle("workspace/symbol", "["+symJSON(root, "RunTask", "Runner", "a/r.go", 4)+"]")
	s.handle("textDocument/definition", `[{"uri":"`+toFileURI(root, "a/r.go")+`","range":{"start":{"line":4,"character":17},"end":{"line":4,"character":24}}}]`)
	res, err := q.Query(context.Background(), SymbolRequest{Op: "definition", Name: "Runner.RunTask"})
	if err != nil {
		t.Fatal(err)
	}
	var p struct{ Position Position }
	json.Unmarshal(s.lastRequest("textDocument/definition"), &p)
	if p.Position.Line != 4 || p.Position.Character != 17 {
		t.Fatalf("definition queried at %+v, want line 4 char 17 (the identifier)", p.Position)
	}
	if !strings.Contains(res.Content, "a/r.go:5") || !strings.Contains(res.Content, "func (r *Runner) RunTask()") {
		t.Fatalf("result = %q", res.Content)
	}
}

func TestAmbiguousNameListsCandidates(t *testing.T) {
	q, s, root := newQueryHarness(t, map[string]string{"a.go": "package a\nfunc Parse() {}\n", "b.go": "package a\nfunc Parse() {}\n"})
	s.handle("workspace/symbol", "["+symJSON(root, "Parse", "", "a.go", 1)+","+symJSON(root, "Parse", "", "b.go", 1)+"]")
	res, err := q.Query(context.Background(), SymbolRequest{Op: "definition", Name: "Parse"})
	if err != nil || res.Summary != "ambiguous" || !strings.Contains(res.Content, "a.go:2") || !strings.Contains(res.Content, "b.go:2") {
		t.Fatalf("ambiguous = %+v, %v", res, err)
	}
}

func TestUnknownNameOffersNearMatches(t *testing.T) {
	q, s, root := newQueryHarness(t, map[string]string{"a.go": "package a\nfunc ParseAll() {}\n"})
	s.handleFunc("workspace/symbol", func(p json.RawMessage) string {
		if strings.Contains(string(p), `"Parse"`) {
			return "[" + symJSON(root, "ParseAll", "", "a.go", 1) + "]"
		}
		return "[]"
	})
	res, _ := q.Query(context.Background(), SymbolRequest{Op: "definition", Name: "Parse"})
	if !strings.Contains(res.Content, "no symbol named Parse") || !strings.Contains(res.Content, "ParseAll") {
		t.Fatalf("unknown = %q", res.Content)
	}
}

func TestPathLineAddressingSkipsWorkspaceSymbol(t *testing.T) {
	q, s, root := newQueryHarness(t, map[string]string{"a.go": "package a\n\nvar total = sum(1, 2)\n"})
	s.handle("textDocument/references", `[{"uri":"`+toFileURI(root, "a.go")+`","range":{"start":{"line":2,"character":12}}}]`)
	res, err := q.Query(context.Background(), SymbolRequest{Op: "references", Name: "sum", Path: "a.go", Line: 3})
	if err != nil || s.lastRequest("workspace/symbol") != nil {
		t.Fatalf("path+line must not search symbols: %v", err)
	}
	var p struct{ Position Position }
	json.Unmarshal(s.lastRequest("textDocument/references"), &p)
	if p.Position.Line != 2 || p.Position.Character != 12 || !strings.Contains(res.Content, "a.go:3") {
		t.Fatalf("position %+v, result %q", p.Position, res.Content)
	}
}

// Review focus 3.
func TestIdentifierColumnCountsUTF16(t *testing.T) {
	if got := identifierColumn(`	x := "café"; y := sum(x)`, "sum"); got != 19 {
		t.Fatalf("identifierColumn = %d, want 19 (tab=1, é=1 UTF-16 unit)", got)
	}
	if identifierColumn("summary := 1", "sum") != -1 {
		t.Fatal("must match whole identifiers only")
	}
}

type fakeIndex struct{ syms []db.Symbol }

func (f fakeIndex) FindSymbols(_ int64, name, _ string, _ int) ([]db.Symbol, error) {
	var out []db.Symbol
	for _, s := range f.syms {
		if strings.Contains(s.Name, name) {
			out = append(out, s)
		}
	}
	return out, nil
}

// Review focus 4.
func TestQueryFallsBackToIndexWhenNotReady(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc Parse() {}\n"), 0o644)
	m := NewManager(root, map[string]ServerSpec{"go": {}}, nil)
	m.start = func(ctx context.Context, _ string, _ ServerSpec) (*Client, func(), error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	q := NewSymbolQuery(NewHandle(m, nil, nil), fakeIndex{syms: []db.Symbol{{Name: "Parse", Kind: "function", FilePath: "a.go", LineStart: 3}}}, 1)
	q.wait = 100 * time.Millisecond
	res, err := q.Query(context.Background(), SymbolRequest{Op: "definition", Name: "Parse"})
	if err != nil || !strings.Contains(res.Content, "a.go:3") || !strings.Contains(res.Content, "answered from index") {
		t.Fatalf("fallback = %+v, %v", res, err)
	}
	res, _ = q.Query(context.Background(), SymbolRequest{Op: "references", Name: "Parse"})
	if !strings.Contains(res.Content, "need a language server") {
		t.Fatalf("references without a server = %q", res.Content)
	}
}

func TestFindWithoutHandleUsesIndex(t *testing.T) {
	q := NewSymbolQuery(nil, fakeIndex{syms: []db.Symbol{{Name: "ParseAll", Kind: "function", FilePath: "x.go", LineStart: 9}}}, 1)
	res, err := q.Query(context.Background(), SymbolRequest{Op: "find", Name: "Parse"})
	if err != nil || !strings.Contains(res.Content, "x.go:9 function ParseAll") {
		t.Fatalf("find = %+v, %v", res, err)
	}
}
