package native

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/lsp"
	"marshal/internal/tools/registry"
)

type fakeQuerier struct{ got lsp.SymbolRequest }

func (f *fakeQuerier) Query(_ context.Context, req lsp.SymbolRequest) (lsp.SymbolResult, error) {
	f.got = req
	return lsp.SymbolResult{Summary: "1 reference", Content: "a.go:3"}, nil
}

func TestSymbolsToolPassesRequestThrough(t *testing.T) {
	q := &fakeQuerier{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, Symbols: q}); err != nil {
		t.Fatal(err)
	}
	res, err := invokeTool(t, reg, "symbols", `{"name":"Runner.RunTask","op":"references","path":"a.go","line":3}`)
	if err != nil || res.Content != "a.go:3" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if q.got != (lsp.SymbolRequest{Op: "references", Name: "Runner.RunTask", Path: "a.go", Line: 3}) {
		t.Fatalf("request = %+v", q.got)
	}
}

func TestRetiredSymbolToolsAreGone(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, Symbols: &fakeQuerier{}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"symbols.find", "definition", "references", "hover", "diagnostics.check"} {
		if _, ok := reg.Lookup(name); ok {
			t.Errorf("%s still registered", name)
		}
	}
	tool, _ := reg.Lookup("repo.search")
	if strings.Contains(string(tool.Schema), `"kind"`) {
		t.Error("repo.search still has a kind parameter")
	}
}
