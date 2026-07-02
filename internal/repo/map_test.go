package repo

import (
	"strings"
	"testing"

	"marshal/internal/db"
)

func TestRenderDirectoryMap(t *testing.T) {
	files := []db.FileIndex{
		{Path: "cmd/marshal/main.go", Language: "go"},
		{Path: "internal/app/app.go", Language: "go"},
		{Path: "internal/db/db.go", Language: "go"},
		{Path: "README.md", Language: "markdown"},
	}
	out := RenderDirectoryMap(files, nil, 100)
	for _, want := range []string{"cmd/", "internal/", "README.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in map:\n%s", want, out)
		}
	}
}

func TestRenderDirectoryMapTruncates(t *testing.T) {
	files := []db.FileIndex{
		{Path: "a.go", Language: "go"},
		{Path: "b.go", Language: "go"},
		{Path: "c.go", Language: "go"},
		{Path: "d.go", Language: "go"},
		{Path: "e.go", Language: "go"},
	}
	out := RenderDirectoryMap(files, nil, 2)
	if !strings.Contains(out, "... (3 more files)") {
		t.Errorf("expected truncation marker in map:\n%s", out)
	}
}

func TestRenderDirectoryMapShowsExportedSymbols(t *testing.T) {
	files := []db.FileIndex{
		{Path: "internal/repo/scanner.go", Language: "go"},
	}
	symbols := []db.Symbol{
		{FilePath: "internal/repo/scanner.go", Kind: "type", Name: "Scanner", LineStart: 1, LineEnd: 3},
		{FilePath: "internal/repo/scanner.go", Kind: "function", Name: "NewScanner", LineStart: 5, LineEnd: 7},
	}
	out := RenderDirectoryMap(files, symbols, 100)
	if !strings.Contains(out, "scanner.go (Scanner, NewScanner)") {
		t.Errorf("expected inline exported symbols in map:\n%s", out)
	}
}

func TestRenderDirectoryMapExcludesUnexportedAndImports(t *testing.T) {
	files := []db.FileIndex{
		{Path: "scanner.go", Language: "go"},
	}
	symbols := []db.Symbol{
		{FilePath: "scanner.go", Kind: "function", Name: "hashFile", LineStart: 1, LineEnd: 3},
		{FilePath: "scanner.go", Kind: "import", Name: "fmt", LineStart: 1, LineEnd: 1},
	}
	out := RenderDirectoryMap(files, symbols, 100)
	if strings.Contains(out, "(") {
		t.Errorf("expected no inline suffix for unexported/import-only file:\n%s", out)
	}
}
