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
