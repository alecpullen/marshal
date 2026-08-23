package index

import (
	"context"
	"errors"
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

func TestRunHonoursContextCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nfunc F(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Deps{DB: database, Root: root}, pid)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRunEmitsProgress(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nfunc F(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	var msgs []string
	_, err := Run(context.Background(), Deps{
		DB:         database,
		Root:       root,
		Embedder:   &fakeEmbedder{model: "m"},
		OnProgress: func(msg string) { msgs = append(msgs, msg) },
	}, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatalf("expected progress messages, got none")
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

func TestRunIndexesPythonAndTypeScriptSymbols(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def top():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.ts"), []byte("function add(a, b) { return a + b; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	_, err := Run(context.Background(), Deps{DB: database, Root: root}, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	syms, err := database.GetSymbols(pid, 0)
	if err != nil {
		t.Fatalf("GetSymbols: %v", err)
	}
	var pyOK, tsOK bool
	for _, s := range syms {
		if s.FilePath == "a.py" && s.Kind == "function" && s.Name == "top" && s.Source == "treesitter" {
			pyOK = true
		}
		if s.FilePath == "b.ts" && s.Kind == "function" && s.Name == "add" && s.Source == "treesitter" {
			tsOK = true
		}
	}
	if !pyOK || !tsOK {
		t.Fatalf("missing treesitter symbols (py=%v, ts=%v) in %+v", pyOK, tsOK, syms)
	}
}

func TestRunPrefersLSPForPythonToo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def top():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	_, err := Run(context.Background(), Deps{DB: database, Root: root, LSP: fakeLSP{lang: "python"}}, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	syms, err := database.GetSymbols(pid, 0)
	if err != nil {
		t.Fatalf("GetSymbols: %v", err)
	}
	for _, s := range syms {
		if s.Source != "lsp" {
			t.Fatalf("expected LSP to win for python, got %#v", s)
		}
	}
}
