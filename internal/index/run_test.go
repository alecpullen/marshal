package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	if err := os.WriteFile(filepath.Join(root, "c.rs"), []byte("fn top() {}\n"), 0o644); err != nil {
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
	var pyOK, tsOK, rustOK bool
	for _, s := range syms {
		if s.FilePath == "a.py" && s.Kind == "function" && s.Name == "top" && s.Source == "treesitter" {
			pyOK = true
		}
		if s.FilePath == "b.ts" && s.Kind == "function" && s.Name == "add" && s.Source == "treesitter" {
			tsOK = true
		}
		if s.FilePath == "c.rs" && s.Kind == "function" && s.Name == "top" && s.Source == "treesitter" {
			rustOK = true
		}
	}
	if !pyOK || !tsOK || !rustOK {
		t.Fatalf("missing treesitter symbols (py=%v, ts=%v, rust=%v) in %+v", pyOK, tsOK, rustOK, syms)
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

func TestRunReportsScanSkips(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	rep, err := Run(context.Background(), Deps{Root: root, DB: database, Embedder: nil}, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "scan skipped 1 entries") && strings.Contains(w, "(symlink)") {
			return
		}
	}
	t.Fatalf("no skip summary in warnings: %q", rep.Warnings)
}

// TestRunReportsSizeCapSkips covers the per-file size cap's end of the skip
// report: a file past MaxIndexableFileBytes (the 2 MiB default in production)
// is skipped by the scanner, and the pass surfaces that skip in its one-line
// summary, so the coverage loss is visible instead of silent.
func TestRunReportsSizeCapSkips(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 4096)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	rep, err := Run(context.Background(), Deps{Root: root, DB: database, Embedder: nil, MaxBytes: 1024}, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Files != 1 {
		t.Errorf("indexed %d files, want 1: the capped file must be left out of the index", rep.Files)
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "scan skipped 1 entries") &&
			strings.Contains(w, "big.txt") &&
			strings.Contains(w, "file too large") {
			return
		}
	}
	t.Fatalf("the per-file size-cap skip is not surfaced in the pass's warnings: %q", rep.Warnings)
}

func TestRunWarnsOnFilesystemRoot(t *testing.T) {
	if got, want := pathologicalIndexRoot("/"), "a filesystem root"; got != want {
		t.Errorf("pathologicalIndexRoot(%q) = %q, want %q", "/", got, want)
	}
	if got := pathologicalIndexRoot(t.TempDir()); got != "" {
		t.Errorf("pathologicalIndexRoot(temp dir) = %q, want empty", got)
	}
}

// paddedSource returns a syntactically valid Go file of exactly size bytes.
func paddedSource(t *testing.T, funcName string, size int) []byte {
	t.Helper()
	head := "package p\n\nfunc " + funcName + "() {}\n"
	pad := size - len(head) - len("//\n")
	if pad < 0 {
		t.Fatalf("size %d leaves no room for the file header", size)
	}
	return []byte(head + "//" + strings.Repeat("x", pad) + "\n")
}

// TestRunContentBudgetKeepsChunksOfOmittedFiles covers the pass's end of the
// content budget: a file past MaxTotalContentBytes is still indexed by path,
// but its bytes are not embedded, and a following pass must not read that
// absence of chunks as a deletion.
func TestRunContentBudgetKeepsChunksOfOmittedFiles(t *testing.T) {
	root := t.TempDir()
	// Two 1 KiB files against a 1 KiB budget: the first in walk order fits and
	// the second is indexed without its content.
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(root, name), paddedSource(t, name[:1], 1<<10), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	database := newTestDB(t)
	defer database.Close()
	pid := mustCreateProject(t, database, root)

	deps := Deps{
		DB:                   database,
		Root:                 root,
		MaxTotalContentBytes: 1 << 10,
		Embedder:             &fakeEmbedder{model: "m"},
	}

	rep, err := Run(context.Background(), deps, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The path of the budget-omitted file is indexed all the same.
	if rep.Files != 2 {
		t.Fatalf("Files = %d, want 2", rep.Files)
	}
	if rep.FilesEmbedded != 1 {
		t.Fatalf("FilesEmbedded = %d, want 1: only the file inside the content budget can be embedded", rep.FilesEmbedded)
	}
	state, err := database.ChunkedFiles(pid)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	if _, ok := state["a.go"]; !ok {
		t.Fatalf("a.go has no chunks after the first pass: %#v", state)
	}
	if _, ok := state["b.go"]; ok {
		t.Fatalf("b.go was chunked though its content was past the budget: %#v", state)
	}

	// The second pass sees the same files at the same hashes: the retained one
	// is skipped by hash and the omitted one keeps its (empty) chunk state, so
	// nothing is re-embedded and no file is purged.
	rep2, err := Run(context.Background(), deps, pid)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if rep2.FilesEmbedded != 0 {
		t.Errorf("second pass embedded %d files, want 0", rep2.FilesEmbedded)
	}
	after, err := database.ChunkedFiles(pid)
	if err != nil {
		t.Fatalf("ChunkedFiles after second pass: %v", err)
	}
	if len(after) != len(state) {
		t.Fatalf("chunk state changed across an unchanged pass: before %#v after %#v", state, after)
	}
	for path := range after {
		if _, ok := state[path]; !ok {
			t.Fatalf("%s acquired chunks in the second pass", path)
		}
	}
}
