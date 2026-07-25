package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/db"
)

type fakeLSP struct{ lang string }

func (f fakeLSP) DocumentSymbols(_ context.Context, lang, filePath string, _ []byte) ([]db.Symbol, bool) {
	if lang != f.lang {
		return nil, false
	}
	return []db.Symbol{{FilePath: filePath, Kind: "function", Name: "L", Signature: "fn L", LineStart: 1, LineEnd: 1, Source: "lsp"}}, true
}

func TestRunPrefersLSPSymbols(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nfunc F(){}\n"), 0o644)
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	_, err := Run(context.Background(), Deps{DB: database, Root: root, LSP: fakeLSP{lang: "go"}}, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	syms, err := database.GetSymbols(pid, 0)
	if err != nil {
		t.Fatalf("GetSymbols: %v", err)
	}
	if len(syms) == 0 {
		t.Fatal("no symbols")
	}
	for _, s := range syms {
		if s.Source != "lsp" {
			t.Fatalf("expected source=lsp, got %#v", s)
		}
	}
}

func TestRunIndexesFilesSymbolsEmbeddings(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nfunc F(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	rep, err := Run(context.Background(), Deps{
		DB: database, Root: root, Embedder: &fakeEmbedder{model: "m"},
	}, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Files < 1 || rep.Symbols < 1 || rep.FilesEmbedded < 1 {
		t.Fatalf("report = %+v", rep)
	}

	// nil embedder still indexes files+symbols, skips embeddings.
	rep2, err := Run(context.Background(), Deps{DB: database, Root: root, Embedder: nil}, pid)
	if err != nil || rep2.Symbols < 1 || rep2.FilesEmbedded != 0 {
		t.Fatalf("nil-embedder report = %+v err=%v", rep2, err)
	}
}
