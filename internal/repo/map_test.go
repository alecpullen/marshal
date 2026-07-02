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
	out := RenderDirectoryMap(files, 100)
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
	out := RenderDirectoryMap(files, 2)
	if !strings.Contains(out, "... (3 more files)") {
		t.Errorf("expected truncation marker in map:\n%s", out)
	}
}
