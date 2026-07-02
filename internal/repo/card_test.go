package repo

import (
	"strings"
	"testing"

	"marshal/internal/db"
)

func TestRenderRepoCard(t *testing.T) {
	files := []db.FileIndex{
		{Path: "main.go", Language: "go"},
		{Path: "cmd/marshal/main.go", Language: "go"},
		{Path: "README.md", Language: "markdown"},
	}
	out := RenderRepoCard("myproject", files)
	if !strings.Contains(out, "Project: myproject") {
		t.Errorf("expected project name in card:\n%s", out)
	}
	if !strings.Contains(out, "go: 2") {
		t.Errorf("expected go count in card:\n%s", out)
	}
	if !strings.Contains(out, "markdown: 1") {
		t.Errorf("expected markdown count in card:\n%s", out)
	}
	if !strings.Contains(out, "cmd/") {
		t.Errorf("expected cmd/ directory in card:\n%s", out)
	}
	if strings.Contains(out, "  ./") {
		t.Errorf("expected no ./ directory entry in card:\n%s", out)
	}
}
