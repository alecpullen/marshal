package edit

import (
	"strings"
	"testing"
)

// --- ParseSearchReplace tests ---

func TestParseSearchReplace_BasicBlock(t *testing.T) {
	response := `
path/to/file.go
<<<<<<< SEARCH
old content
=======
new content
>>>>>>> REPLACE
`
	edits := ParseSearchReplace(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	e := edits[0]
	if e.Path != "path/to/file.go" {
		t.Errorf("path: got %q", e.Path)
	}
	if e.Search != "old content" {
		t.Errorf("search: got %q", e.Search)
	}
	if e.Replace != "new content" {
		t.Errorf("replace: got %q", e.Replace)
	}
}

func TestParseSearchReplace_BareMarkers(t *testing.T) {
	response := `main.go
<<<<<<<
alpha
=======
beta
>>>>>>>`
	edits := ParseSearchReplace(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Search != "alpha" {
		t.Errorf("search: got %q", edits[0].Search)
	}
	if edits[0].Replace != "beta" {
		t.Errorf("replace: got %q", edits[0].Replace)
	}
}

func TestParseSearchReplace_MultipleBlocks(t *testing.T) {
	response := `
a.go
<<<<<<< SEARCH
foo
=======
bar
>>>>>>> REPLACE

b.go
<<<<<<< SEARCH
baz
=======
qux
>>>>>>> REPLACE
`
	edits := ParseSearchReplace(response)
	if len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(edits))
	}
	if edits[0].Path != "a.go" {
		t.Errorf("first path: got %q", edits[0].Path)
	}
	if edits[1].Path != "b.go" {
		t.Errorf("second path: got %q", edits[1].Path)
	}
}

func TestParseSearchReplace_EmptySearch_CreateFile(t *testing.T) {
	response := `new.go
<<<<<<< SEARCH
=======
package main

func main() {}
>>>>>>> REPLACE
`
	edits := ParseSearchReplace(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Search != "" {
		t.Errorf("search should be empty, got %q", edits[0].Search)
	}
	if !strings.Contains(edits[0].Replace, "package main") {
		t.Errorf("replace should contain package main, got %q", edits[0].Replace)
	}
}

func TestParseSearchReplace_NoPath_Skipped(t *testing.T) {
	// No filename before the block — should be skipped.
	response := `<<<<<<< SEARCH
old
=======
new
>>>>>>> REPLACE
`
	edits := ParseSearchReplace(response)
	if len(edits) != 0 {
		t.Errorf("expected 0 edits when no path, got %d", len(edits))
	}
}

func TestParseSearchReplace_BlankLineBetweenPathAndMarker(t *testing.T) {
	// A blank line between the filename and the marker is OK — we walk back
	// past blank lines.
	response := `util.go

<<<<<<< SEARCH
old
=======
new
>>>>>>> REPLACE
`
	edits := ParseSearchReplace(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Path != "util.go" {
		t.Errorf("path: got %q", edits[0].Path)
	}
}

func TestParseSearchReplace_MultilineSearch(t *testing.T) {
	response := `main.go
<<<<<<< SEARCH
line one
line two
line three
=======
replaced
>>>>>>> REPLACE
`
	edits := ParseSearchReplace(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	want := "line one\nline two\nline three"
	if edits[0].Search != want {
		t.Errorf("search: got %q, want %q", edits[0].Search, want)
	}
}

// --- ApplyToContent tests ---

func TestApply_ExactMatch(t *testing.T) {
	current := "package main\n\nfunc Old() {}\n"
	e := &SearchReplaceEdit{
		Path:    "f.go",
		Search:  "func Old() {}",
		Replace: "func New() {}",
	}
	updated, ok := e.ApplyToContent(current)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(updated, "func New() {}") {
		t.Errorf("expected New in result: %q", updated)
	}
	if strings.Contains(updated, "func Old() {}") {
		t.Errorf("Old should be gone: %q", updated)
	}
}

func TestApply_EmptySearch_Overwrite(t *testing.T) {
	e := &SearchReplaceEdit{
		Path:    "f.go",
		Search:  "",
		Replace: "brand new content",
	}
	updated, ok := e.ApplyToContent("existing stuff")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if updated != "brand new content" {
		t.Errorf("got %q", updated)
	}
}

func TestApply_NotFound_ReturnsFalse(t *testing.T) {
	e := &SearchReplaceEdit{
		Path:    "f.go",
		Search:  "this text is not present",
		Replace: "replacement",
	}
	updated, ok := e.ApplyToContent("some other content")
	if ok {
		t.Error("expected ok=false when search not found")
	}
	if updated != "some other content" {
		t.Errorf("content should be unchanged: %q", updated)
	}
}

func TestApply_NormalisedWhitespace(t *testing.T) {
	// File has trailing spaces on lines; search text does not.
	current := "func Foo() {   \n\tbar()   \n}\n"
	e := &SearchReplaceEdit{
		Path:    "f.go",
		Search:  "func Foo() {\n\tbar()\n}",
		Replace: "func Foo() {\n\tbaz()\n}",
	}
	updated, ok := e.ApplyToContent(current)
	if !ok {
		t.Fatal("expected ok=true with normalised match")
	}
	if !strings.Contains(updated, "baz()") {
		t.Errorf("expected baz in result: %q", updated)
	}
}

func TestApply_TrailingNewlineAdded(t *testing.T) {
	// Replace text without trailing newline — should be added.
	e := &SearchReplaceEdit{
		Path:    "f.go",
		Search:  "old",
		Replace: "new",
	}
	updated, ok := e.ApplyToContent("old\n")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.HasSuffix(updated, "\n") {
		t.Errorf("expected trailing newline, got %q", updated)
	}
}

func TestApply_OnlyFirstOccurrenceReplaced(t *testing.T) {
	current := "a\na\na\n"
	e := &SearchReplaceEdit{
		Path:    "f.go",
		Search:  "a",
		Replace: "b",
	}
	updated, ok := e.ApplyToContent(current)
	if !ok {
		t.Fatal("expected ok=true")
	}
	// First "a" replaced, rest unchanged.
	if strings.Count(updated, "a") != 2 {
		t.Errorf("expected 2 remaining 'a', got %q", updated)
	}
}

// --- applyNormalised edge cases ---

func TestApplyNormalised_MatchAtStart(t *testing.T) {
	current := "line1\nline2\nline3\n"
	search := "line1\nline2"
	replace := "REPLACED\n"
	result, ok := applyNormalised(current, search, replace)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.HasPrefix(result, "REPLACED") {
		t.Errorf("expected REPLACED at start: %q", result)
	}
}

func TestApplyNormalised_MatchAtEnd(t *testing.T) {
	current := "line1\nline2\nline3"
	search := "line2\nline3"
	replace := "REPLACED"
	result, ok := applyNormalised(current, search, replace)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(result, "REPLACED") {
		t.Errorf("expected REPLACED: %q", result)
	}
	if !strings.HasPrefix(result, "line1") {
		t.Errorf("expected line1 preserved: %q", result)
	}
}

func TestApplyNormalised_NoMatch(t *testing.T) {
	current := "aaa\nbbb\nccc\n"
	_, ok := applyNormalised(current, "xxx\nyyy", "zzz")
	if ok {
		t.Error("expected ok=false")
	}
}
