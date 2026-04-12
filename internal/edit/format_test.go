package edit_test

import (
	"strings"
	"testing"

	"github.com/alec/marshal/internal/edit"
)

func TestFormatFor_WholeFile(t *testing.T) {
	f := edit.FormatFor("wholefile")
	if f.Name() != "wholefile" {
		t.Errorf("Name: got %q, want %q", f.Name(), "wholefile")
	}
}

func TestFormatFor_SearchReplace(t *testing.T) {
	f := edit.FormatFor("search-replace")
	if f.Name() != "search-replace" {
		t.Errorf("Name: got %q, want %q", f.Name(), "search-replace")
	}
}

func TestFormatFor_Udiff(t *testing.T) {
	f := edit.FormatFor("udiff")
	if f.Name() != "udiff" {
		t.Errorf("Name: got %q, want %q", f.Name(), "udiff")
	}
}

func TestFormatFor_Default(t *testing.T) {
	f := edit.FormatFor("unknown-format")
	if f.Name() != "wholefile" {
		t.Errorf("default should be wholefile, got %q", f.Name())
	}
}

func TestWholeFileFormat_Parse(t *testing.T) {
	f := edit.WholeFileFormat{}
	response := `path/to/file.go
` + "```go\npackage main\n\nfunc Hello() {}\n```"

	edits := f.Parse(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Path != "path/to/file.go" {
		t.Errorf("Path: got %q, want %q", edits[0].Path, "path/to/file.go")
	}
	if edits[0].Search != "" {
		t.Error("Search should be empty for whole-file")
	}
	// ParseWhole preserves newlines including trailing newline from fenced block
	if !strings.Contains(edits[0].Replace, "package main") {
		t.Errorf("Replace should contain 'package main', got %q", edits[0].Replace)
	}
}

func TestWholeFileFormat_Apply(t *testing.T) {
	f := edit.WholeFileFormat{}
	e := edit.Edit{Path: "test.go", Search: "", Replace: "new content"}

	updated, ok := f.Apply("old content", e)
	if !ok {
		t.Error("expected Apply to succeed")
	}
	if updated != "new content" {
		t.Errorf("Apply: got %q, want %q", updated, "new content")
	}
}

func TestSearchReplaceFormat_Parse(t *testing.T) {
	f := edit.SearchReplaceFormat{}
	response := `path/to/file.go
<<<<<<< SEARCH
old line
=======
new line
>>>>>>> REPLACE`

	edits := f.Parse(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Path != "path/to/file.go" {
		t.Errorf("Path: got %q", edits[0].Path)
	}
	if edits[0].Search != "old line" {
		t.Errorf("Search: got %q, want %q", edits[0].Search, "old line")
	}
	if edits[0].Replace != "new line" {
		t.Errorf("Replace: got %q, want %q", edits[0].Replace, "new line")
	}
}

func TestSearchReplaceFormat_Apply(t *testing.T) {
	f := edit.SearchReplaceFormat{}
	e := edit.Edit{Path: "test.go", Search: "hello", Replace: "world"}

	updated, ok := f.Apply("hello there", e)
	if !ok {
		t.Error("expected Apply to succeed")
	}
	// ApplyToContent normalizes and adds trailing newlines
	if !strings.Contains(updated, "world") {
		t.Errorf("Apply: expected result to contain 'world', got %q", updated)
	}
}

func TestSearchReplaceFormat_Apply_CreateFile(t *testing.T) {
	f := edit.SearchReplaceFormat{}
	e := edit.Edit{Path: "new.go", Search: "", Replace: "package main"}

	updated, ok := f.Apply("", e)
	if !ok {
		t.Error("expected Apply to succeed for empty Search (create file)")
	}
	if updated != "package main" {
		t.Errorf("Apply: got %q", updated)
	}
}

func TestAllFormats(t *testing.T) {
	formats := edit.AllFormats()
	if len(formats) != 3 {
		t.Errorf("expected 3 formats, got %d", len(formats))
	}
	names := make(map[string]bool)
	for _, f := range formats {
		names[f.Name()] = true
	}
	if !names["wholefile"] {
		t.Error("missing wholefile format")
	}
	if !names["search-replace"] {
		t.Error("missing search-replace format")
	}
	if !names["udiff"] {
		t.Error("missing udiff format")
	}
}
