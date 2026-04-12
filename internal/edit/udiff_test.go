package edit

import (
	"strings"
	"testing"
)

// --- ParseUdiff tests ---

func TestParseUdiff_BasicFile(t *testing.T) {
	response := `--- a/main.go
+++ b/main.go
@@ -1,4 +1,4 @@
 package main

-func Old() {}
+func New() {}
`
	edits := ParseUdiff(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Path != "main.go" {
		t.Errorf("path: got %q", edits[0].Path)
	}
	if len(edits[0].Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(edits[0].Hunks))
	}
}

func TestParseUdiff_MultipleFiles(t *testing.T) {
	response := `--- a/a.go
+++ b/a.go
@@ -1,1 +1,1 @@
-old a
+new a
--- a/b.go
+++ b/b.go
@@ -1,1 +1,1 @@
-old b
+new b
`
	edits := ParseUdiff(response)
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

func TestParseUdiff_FencedBlock(t *testing.T) {
	response := "```diff\n--- a/f.go\n+++ b/f.go\n@@ -1 +1 @@\n-old\n+new\n```\n"
	edits := ParseUdiff(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d: %+v", len(edits), edits)
	}
	if edits[0].Path != "f.go" {
		t.Errorf("path: got %q", edits[0].Path)
	}
}

func TestParseUdiff_MultipleHunks(t *testing.T) {
	response := `--- a/big.go
+++ b/big.go
@@ -1,3 +1,3 @@
 package main
-funcA()
+funcB()
@@ -10,3 +10,3 @@
 // comment
-oldCode()
+newCode()
`
	edits := ParseUdiff(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if len(edits[0].Hunks) != 2 {
		t.Errorf("expected 2 hunks, got %d", len(edits[0].Hunks))
	}
}

func TestParseUdiff_PathWithoutBPrefix(t *testing.T) {
	response := `--- a/pkg/util.go
+++ pkg/util.go
@@ -1 +1 @@
-old
+new
`
	edits := ParseUdiff(response)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Path != "pkg/util.go" {
		t.Errorf("path: got %q", edits[0].Path)
	}
}

// --- ApplyToContent tests ---

func TestUdiffApply_SimpleReplace(t *testing.T) {
	current := "package main\n\nfunc Old() {}\n"
	e := &UdiffEdit{
		Path: "main.go",
		Hunks: []Hunk{{
			Lines: []HunkLine{
				{Op: ' ', Text: "package main"},
				{Op: ' ', Text: ""},
				{Op: '-', Text: "func Old() {}"},
				{Op: '+', Text: "func New() {}"},
			},
		}},
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

func TestUdiffApply_AdditionOnly(t *testing.T) {
	current := "line1\nline2\n"
	e := &UdiffEdit{
		Path: "f.go",
		Hunks: []Hunk{{
			Lines: []HunkLine{
				{Op: ' ', Text: "line1"},
				{Op: '+', Text: "inserted"},
				{Op: ' ', Text: "line2"},
			},
		}},
	}
	updated, ok := e.ApplyToContent(current)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(updated, "inserted") {
		t.Errorf("expected inserted in result: %q", updated)
	}
}

func TestUdiffApply_DeletionOnly(t *testing.T) {
	current := "keep\nremove\nkeep\n"
	e := &UdiffEdit{
		Path: "f.go",
		Hunks: []Hunk{{
			Lines: []HunkLine{
				{Op: ' ', Text: "keep"},
				{Op: '-', Text: "remove"},
				{Op: ' ', Text: "keep"},
			},
		}},
	}
	updated, ok := e.ApplyToContent(current)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if strings.Contains(updated, "remove") {
		t.Errorf("remove should be gone: %q", updated)
	}
	if strings.Count(updated, "keep") < 2 {
		t.Errorf("both keeps should remain: %q", updated)
	}
}

func TestUdiffApply_NotFound_ReturnsFalse(t *testing.T) {
	current := "some content\n"
	e := &UdiffEdit{
		Path: "f.go",
		Hunks: []Hunk{{
			Lines: []HunkLine{
				{Op: '-', Text: "this line does not exist"},
				{Op: '+', Text: "replacement"},
			},
		}},
	}
	updated, ok := e.ApplyToContent(current)
	if ok {
		t.Error("expected ok=false")
	}
	if updated != current {
		t.Errorf("content should be unchanged: %q", updated)
	}
}

func TestUdiffApply_NormalisedWhitespace(t *testing.T) {
	// File has trailing spaces; diff does not.
	current := "func Foo() {   \n\treturn 1   \n}\n"
	e := &UdiffEdit{
		Path: "f.go",
		Hunks: []Hunk{{
			Lines: []HunkLine{
				{Op: ' ', Text: "func Foo() {"},
				{Op: '-', Text: "\treturn 1"},
				{Op: '+', Text: "\treturn 2"},
				{Op: ' ', Text: "}"},
			},
		}},
	}
	updated, ok := e.ApplyToContent(current)
	if !ok {
		t.Fatal("expected ok=true with normalised match")
	}
	if !strings.Contains(updated, "return 2") {
		t.Errorf("expected return 2: %q", updated)
	}
}

func TestUdiffApply_MultipleHunks(t *testing.T) {
	current := "line1\nline2\nline3\nline4\nline5\n"
	e := &UdiffEdit{
		Path: "f.go",
		Hunks: []Hunk{
			{Lines: []HunkLine{
				{Op: '-', Text: "line1"},
				{Op: '+', Text: "LINE1"},
			}},
			{Lines: []HunkLine{
				{Op: '-', Text: "line5"},
				{Op: '+', Text: "LINE5"},
			}},
		},
	}
	updated, ok := e.ApplyToContent(current)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(updated, "LINE1") || !strings.Contains(updated, "LINE5") {
		t.Errorf("both replacements should appear: %q", updated)
	}
}

// --- findSequence tests ---

func TestFindSequence_Found(t *testing.T) {
	hay := []string{"a", "b", "c", "d"}
	idx := findSequence(hay, []string{"b", "c"}, false)
	if idx != 1 {
		t.Errorf("expected 1, got %d", idx)
	}
}

func TestFindSequence_NotFound(t *testing.T) {
	hay := []string{"a", "b", "c"}
	idx := findSequence(hay, []string{"x", "y"}, false)
	if idx != -1 {
		t.Errorf("expected -1, got %d", idx)
	}
}

func TestFindSequence_NormalisedMatch(t *testing.T) {
	hay := []string{"a   ", "b\t", "c"}
	idx := findSequence(hay, []string{"a", "b"}, true)
	if idx != 0 {
		t.Errorf("expected 0, got %d", idx)
	}
}
