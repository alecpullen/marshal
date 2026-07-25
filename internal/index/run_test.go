package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
