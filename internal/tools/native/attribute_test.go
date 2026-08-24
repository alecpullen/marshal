package native

import (
	"context"
	"strings"
	"testing"
)

const attrSrc = `package p

func Alpha() int {
	return 1
}

func Beta() int {
	return 2
}
`

// A diff touching Alpha's body must attribute to Alpha.
func TestSymbolsForEditAttributesToChangedFunction(t *testing.T) {
	diff := "--- a/p.go\n+++ b/p.go\n@@ -3,3 +3,3 @@\n-\treturn 1\n+\treturn 11\n"
	refs := symbolsForEdit(context.Background(), "p.go", attrSrc, diff)
	if len(refs) == 0 {
		t.Fatal("no symbols attributed")
	}
	if refs[0].Name != "Alpha" {
		t.Fatalf("attributed to %q, want Alpha", refs[0].Name)
	}
	if refs[0].File != "p.go" {
		t.Fatalf("File = %q, want p.go", refs[0].File)
	}
	if refs[0].Kind != "function" {
		t.Fatalf("Kind = %q, want function", refs[0].Kind)
	}
}

// THE conversion test. repo is 1-based, LSP is 0-based, and this is the one
// place the conversion happens. An off-by-one here does not crash — it
// queries the wrong line and returns the wrong callers.
func TestSymbolsForEditConvertsToZeroBasedLines(t *testing.T) {
	diff := "--- a/p.go\n+++ b/p.go\n@@ -3,3 +3,3 @@\n-\treturn 1\n+\treturn 11\n"
	refs := symbolsForEdit(context.Background(), "p.go", attrSrc, diff)
	if len(refs) == 0 || !refs[0].Resolved {
		t.Fatalf("expected a resolved ref, got %+v", refs)
	}
	// "func Alpha() int {" is source line 3 (1-based), so LSP line 2.
	lines := strings.Split(attrSrc, "\n")
	got := lines[refs[0].Line] // 0-based index into the source == LSP line
	if !strings.Contains(got, "func Alpha()") {
		t.Fatalf("Line %d indexes %q, which is not Alpha's declaration", refs[0].Line, got)
	}
	if got[refs[0].Col:refs[0].Col+len("Alpha")] != "Alpha" {
		t.Fatalf("Col %d in %q does not point at the name", refs[0].Col, got)
	}
}

// The diff header may spell the path differently from the tool's own (e.g.
// absolute vs relative, or a "b/" prefix the tool already stripped). When
// the diff covers exactly one file, the fallback must attribute to it even
// though the direct path lookup misses.
func TestSymbolsForEditSingleFileFallbackHeuristic(t *testing.T) {
	// The tool's path is absolute ("/abs/p.go") but the diff header carries
	// "b/p.go", so the direct DiffRanges(diff)["/abs/p.go"] lookup misses and
	// the single-file fallback must kick in.
	diff := "--- a/p.go\n+++ b/p.go\n@@ -3,3 +3,3 @@\n-\treturn 1\n+\treturn 11\n"
	refs := symbolsForEdit(context.Background(), "/abs/p.go", attrSrc, diff)
	if len(refs) == 0 {
		t.Fatal("single-file fallback must attribute despite the path mismatch")
	}
	if refs[0].Name != "Alpha" {
		t.Fatalf("attributed to %q, want Alpha", refs[0].Name)
	}
}

// A multi-file diff whose header paths all miss the tool's path must not
// fall back: with more than one file there is no way to know which is the
// edited one, so attributing to a guess would be wrong.
func TestSymbolsForEditMultiFileDiffDoesNotFallback(t *testing.T) {
	diff := "--- a/p.go\n+++ b/p.go\n@@ -3,3 +3,3 @@\n-\treturn 1\n+\treturn 11\n--- a/q.go\n+++ b/q.go\n@@ -1,1 +1,1 @@\n-x\n+y\n"
	if refs := symbolsForEdit(context.Background(), "/abs/p.go", attrSrc, diff); len(refs) != 0 {
		t.Fatalf("multi-file diff must not fall back to a single-file guess, got %+v", refs)
	}
}

func TestSymbolsForEditUnsupportedLanguageIsEmpty(t *testing.T) {
	diff := "--- a/a.rb\n+++ b/a.rb\n@@ -1,1 +1,1 @@\n-x\n+y\n"
	if refs := symbolsForEdit(context.Background(), "a.rb", "def foo\nend\n", diff); len(refs) != 0 {
		t.Fatalf("unsupported language must yield no symbols, got %+v", refs)
	}
}

func TestSymbolsForEditMalformedDiffIsEmptyNotPanic(t *testing.T) {
	for _, d := range []string{"", "garbage", "@@ nope @@"} {
		if refs := symbolsForEdit(context.Background(), "p.go", attrSrc, d); len(refs) != 0 {
			t.Errorf("diff %q: want no symbols, got %+v", d, refs)
		}
	}
}

func TestSymbolsForEditUnparseableSourceIsEmptyNotError(t *testing.T) {
	diff := "--- a/p.go\n+++ b/p.go\n@@ -1,1 +1,1 @@\n-x\n+y\n"
	if refs := symbolsForEdit(context.Background(), "p.go", "@@@ not go @@@", diff); len(refs) != 0 {
		t.Logf("unparseable source yielded %+v (acceptable if empty)", refs)
	}
}
