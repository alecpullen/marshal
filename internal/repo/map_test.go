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

func TestRenderDirectoryMapIncludesSummaries(t *testing.T) {
	files := []db.FileIndex{
		{Path: "cmd/marshal/main.go", Language: "go"},
		{Path: "internal/agent/runner.go", Language: "go", Summary: "Agent turn loop and runner orchestration."},
		{Path: "internal/agent/chat.go", Language: "go", Summary: "Streaming chat plumbing.\nSecond line is collapsed."},
	}
	out := RenderDirectoryMap(files, nil, 0)
	if !strings.Contains(out, "runner.go — Agent turn loop and runner orchestration.") {
		t.Fatalf("map missing summary suffix:\n%s", out)
	}
	if !strings.Contains(out, "chat.go — Streaming chat plumbing. Second line is collapsed.") {
		t.Fatalf("summary newlines must be collapsed:\n%s", out)
	}
	if strings.Contains(out, "main.go —") {
		t.Fatalf("file without summary must not get a suffix:\n%s", out)
	}
}

func TestRenderDirectoryMapTruncatesLongSummaries(t *testing.T) {
	long := strings.Repeat("word ", 40) // 200 chars
	files := []db.FileIndex{{Path: "a/x.go", Summary: long}}
	out := RenderDirectoryMap(files, nil, 0)
	if strings.Contains(out, long) {
		t.Fatalf("summary was not truncated:\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("truncated summary must carry an ellipsis:\n%s", out)
	}
}

func TestIsExportedNamePerLanguage(t *testing.T) {
	cases := []struct {
		lang, name string
		want       bool
	}{
		{"go", "Foo", true},
		{"go", "foo", false},
		{"python", "public_func", true},
		{"python", "_private", false},
		{"typescript", "add", true},
		{"javascript", "arrow", true},
		{"rust", "top", true},
		{"ruby", "whatever", false}, // unknown language: conservative Go rule
		{"", "Foo", true},
	}
	for _, tc := range cases {
		if got := isExportedName(tc.lang, tc.name); got != tc.want {
			t.Errorf("isExportedName(%q, %q) = %v, want %v", tc.lang, tc.name, got, tc.want)
		}
	}
}

func TestGroupExportedSymbolsPerLanguage(t *testing.T) {
	byFile := groupExportedSymbols([]db.Symbol{
		{FilePath: "a.py", Kind: "function", Name: "public_func"},
		{FilePath: "a.py", Kind: "function", Name: "_private"},
		{FilePath: "a.py", Kind: "import", Name: "os"},
		{FilePath: "b.ts", Kind: "function", Name: "add"},
	})
	if got := byFile["a.py"]; len(got) != 1 || got[0].Name != "public_func" {
		t.Fatalf("a.py exported = %+v, want only public_func", got)
	}
	if got := byFile["b.ts"]; len(got) != 1 || got[0].Name != "add" {
		t.Fatalf("b.ts exported = %+v, want only add", got)
	}
}
