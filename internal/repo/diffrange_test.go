package repo

import (
	"reflect"
	"testing"
)

func TestDiffRangesSingleHunk(t *testing.T) {
	diff := `--- a/foo.go
+++ b/foo.go
@@ -10,3 +10,5 @@ func Foo() {
 	a
+	b
+	c
 	d
`
	got := DiffRanges(diff)
	// Post-image: context "a" is line 10, added "b" is 11, added "c" is 12,
	// context "d" is 13. Only the added lines are attributed.
	want := map[string][]LineRange{"foo.go": {{Start: 11, End: 13}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDiffRangesMultipleHunksAndFiles(t *testing.T) {
	diff := `--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,3 @@
 x
+new
@@ -20,1 +21,1 @@
 y
--- a/bar.go
+++ b/bar.go
@@ -5,0 +6,2 @@
 z
+added
`
	got := DiffRanges(diff)
	want := map[string][]LineRange{
		"foo.go": {{Start: 2, End: 3}},
		"bar.go": {{Start: 7, End: 8}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// "@@ -1,2 +3 @@" omits the post-image count, which means exactly one line.
func TestDiffRangesOmittedCountMeansOne(t *testing.T) {
	got := DiffRanges("+++ b/foo.go\n@@ -1,2 +3 @@\n+new\n")
	want := map[string][]LineRange{"foo.go": {{Start: 3, End: 4}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDiffRangesNewFile(t *testing.T) {
	got := DiffRanges("--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,4 @@\n+a\n+b\n+c\n+d\n")
	want := map[string][]LineRange{"new.go": {{Start: 1, End: 5}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Context lines are never attributed, so a hunk that sits near a function
// boundary does not over-attribute to an adjacent symbol.
func TestDiffRangesContextLinesExcluded(t *testing.T) {
	diff := `--- a/foo.go
+++ b/foo.go
@@ -1,5 +1,5 @@
 func Alpha() {
 	keep
+	changed
 	keep
 }
`
	got := DiffRanges(diff)
	// Post-image: "func Alpha() {" is line 1, "keep" is 2, added "changed"
	// is 3, "keep" is 4, "}" is 5. Only line 3 is attributed.
	want := map[string][]LineRange{"foo.go": {{Start: 3, End: 4}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A hunk with only context lines (a pure move or a no-op) attributes
// nothing rather than swallowing the whole hunk.
func TestDiffRangesContextOnlyHunkAttributesNothing(t *testing.T) {
	got := DiffRanges("+++ b/foo.go\n@@ -1,3 +1,3 @@\n a\n b\n c\n")
	if len(got) != 0 {
		t.Fatalf("context-only hunk must attribute nothing, got %v", got)
	}
}

// A pure deletion has a zero post-image count. It must still produce a
// range, or removing a whole block would attribute to nothing at all.
func TestDiffRangesPureDeletionStillAttributes(t *testing.T) {
	got := DiffRanges("+++ b/foo.go\n@@ -10,5 +9,0 @@\n")
	want := map[string][]LineRange{"foo.go": {{Start: 9, End: 10}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A deletion at the very start of a file reports post-image line 0, which
// is not a real line. It must clamp rather than emit a zero range.
func TestDiffRangesDeletionAtFileStartClamps(t *testing.T) {
	got := DiffRanges("+++ b/foo.go\n@@ -1,3 +0,0 @@\n")
	want := map[string][]LineRange{"foo.go": {{Start: 1, End: 2}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A file deleted outright targets /dev/null and must not become a key.
func TestDiffRangesDeletedFileIgnored(t *testing.T) {
	if got := DiffRanges("--- a/gone.go\n+++ /dev/null\n@@ -1,5 +0,0 @@\n"); len(got) != 0 {
		t.Fatalf("deleted file must produce no ranges, got %v", got)
	}
}

func TestDiffRangesMalformedInputIsEmptyNotPanic(t *testing.T) {
	for _, in := range []string{"", "not a diff", "@@ garbage @@", "+++ b/x.go\n@@ -a,b +c,d @@\n", "@@ -1,1 +1,1 @@\n"} {
		if got := DiffRanges(in); len(got) != 0 {
			t.Errorf("input %q: expected no ranges, got %v", in, got)
		}
	}
}
