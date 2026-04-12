package edit_test

import (
	"testing"

	"github.com/alec/marshal/internal/edit"
)

func TestParseWhole_SingleFile(t *testing.T) {
	response := `I'll add the function:

main.go
` + "```go" + `
package main

func Hello() string { return "hello" }
` + "```"

	edits := edit.ParseWhole(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Path != "main.go" {
		t.Errorf("path: %q", edits[0].Path)
	}
	if edits[0].Content != "package main\n\nfunc Hello() string { return \"hello\" }\n" {
		t.Errorf("content: %q", edits[0].Content)
	}
}

func TestParseWhole_MultipleFiles(t *testing.T) {
	response := `
main.go
` + "```go" + `
package main
` + "```" + `

internal/foo/foo.go
` + "```go" + `
package foo
` + "```"

	edits := edit.ParseWhole(response)
	if len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %d: %v", len(edits), edits)
	}
	if edits[0].Path != "main.go" {
		t.Errorf("edit[0].Path = %q", edits[0].Path)
	}
	if edits[1].Path != "internal/foo/foo.go" {
		t.Errorf("edit[1].Path = %q", edits[1].Path)
	}
}

func TestParseWhole_NoFilename(t *testing.T) {
	// A code block with no preceding filename is ignored.
	response := "Here is some code:\n" + "```go\npackage main\n```"
	edits := edit.ParseWhole(response)
	if len(edits) != 0 {
		t.Errorf("expected 0 edits for block with no filename, got %d", len(edits))
	}
}

func TestParseWhole_SubdirPath(t *testing.T) {
	response := "cmd/server/main.go\n```go\npackage main\n```"
	edits := edit.ParseWhole(response)
	if len(edits) != 1 || edits[0].Path != "cmd/server/main.go" {
		t.Errorf("got %v", edits)
	}
}

func TestParseWhole_BlankLinesBetween(t *testing.T) {
	// Blank lines between the filename and fence should still work.
	response := "main.go\n\n```go\npackage main\n```"
	edits := edit.ParseWhole(response)
	if len(edits) != 1 {
		t.Errorf("expected 1 edit, got %d", len(edits))
	}
}

func TestParseWhole_EmptyResponse(t *testing.T) {
	edits := edit.ParseWhole("")
	if len(edits) != 0 {
		t.Errorf("expected 0 edits, got %d", len(edits))
	}
}
