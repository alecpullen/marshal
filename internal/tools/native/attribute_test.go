package native

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
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

// TestSymbolsForEditToleratesPartialSyntaxErrors pins the actual contract for
// malformed source: repo.ExtractSymbols explicitly permits a partial tree
// around syntax errors, so a broken declaration later in the file must not
// cost the caller the symbols it can still see. The earlier version of this
// test asserted nothing at all (its only check logged), which neither proved
// tolerance nor proved failure.
func TestSymbolsForEditToleratesPartialSyntaxErrors(t *testing.T) {
	// A valid file plus one explicitly malformed declaration appended.
	source := attrSrc + "\nfunc Broken( {\n"
	diff := "--- a/p.go\n+++ b/p.go\n@@ -3,3 +3,3 @@\n-\treturn 1\n+\treturn 11\n"
	refs := symbolsForEdit(context.Background(), "p.go", source, diff)

	var alpha *registry.SymbolRef
	for i := range refs {
		if refs[i].Name == "Alpha" {
			alpha = &refs[i]
			break
		}
	}
	if alpha == nil {
		t.Fatalf("malformed trailing declaration must not hide Alpha, got %+v", refs)
	}
	if alpha.File != "p.go" {
		t.Fatalf("File = %q, want p.go", alpha.File)
	}
	if alpha.Kind != "function" {
		t.Fatalf("Kind = %q, want function", alpha.Kind)
	}
}

// TestSymbolsForEditCancelledParseReturnsNoSymbols exercises the parse-failure
// fallback rather than treating ordinary syntax recovery as a parse error: a
// canceled context makes the parser itself fail, which must degrade to no
// symbols (never an error, never a panic).
//
// The source is deliberately large. tree-sitter polls the context only at
// parse-progress boundaries, so a small file finishes before cancellation is
// ever observed and the call would succeed — the fixture would then pass
// without exercising the failure path at all. The control call below proves
// the same source and diff do attribute to Alpha when parsing succeeds, so a
// vacuous pass cannot hide here.
func TestSymbolsForEditCancelledParseReturnsNoSymbols(t *testing.T) {
	var b strings.Builder
	b.WriteString(attrSrc)
	const padding = 2000
	for i := 0; i < padding; i++ {
		b.WriteString("\nfunc Pad() int {\n\treturn 0\n}\n")
	}
	source := b.String()
	diff := "--- a/p.go\n+++ b/p.go\n@@ -3,3 +3,3 @@\n-\treturn 1\n+\treturn 11\n"

	if refs := symbolsForEdit(context.Background(), "p.go", source, diff); len(refs) == 0 {
		t.Fatal("fixture precondition: without cancellation the source must still attribute to Alpha")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	refs := symbolsForEdit(ctx, "p.go", source, diff)
	if len(refs) != 0 {
		t.Fatalf("a canceled parse must yield no symbols, got %+v", refs)
	}
}
