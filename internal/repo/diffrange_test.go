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
	want := map[string][]LineRange{"foo.go": {{Start: 10, End: 15}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDiffRangesMultipleHunksAndFiles(t *testing.T) {
	diff := `--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,3 @@
 x
@@ -20,1 +21,1 @@
 y
--- a/bar.go
+++ b/bar.go
@@ -5,0 +6,2 @@
 z
`
	got := DiffRanges(diff)
	want := map[string][]LineRange{
		"foo.go": {{Start: 1, End: 4}, {Start: 21, End: 22}},
		"bar.go": {{Start: 6, End: 8}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// "@@ -1,2 +3 @@" omits the post-image count, which means exactly one line.
func TestDiffRangesOmittedCountMeansOne(t *testing.T) {
	got := DiffRanges("+++ b/foo.go\n@@ -1,2 +3 @@\n")
	want := map[string][]LineRange{"foo.go": {{Start: 3, End: 4}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDiffRangesNewFile(t *testing.T) {
	got := DiffRanges("--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,4 @@\n")
	want := map[string][]LineRange{"new.go": {{Start: 1, End: 5}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
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
