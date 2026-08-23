package patch

import (
	"strings"
	"testing"
)

func TestLooksLikeUnifiedDiff(t *testing.T) {
	cases := []struct {
		name     string
		proposal string
		want     bool
	}{
		{"hunk header", "--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n ctx\n+added\n ctx2", true},
		{"header pair only", "--- a/x.go\n+++ b/x.go", true},
		{"header pair non-path", "--- legacy\n+++ modern", false},
		{"search replace", "File: x.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE", false},
		{"markdown divider in search", "File: x.md\n<<<<<<< SEARCH\ntitle\n=====\n=======\ntitle2\n=====\n>>>>>>> REPLACE", false},
		{"empty", "", false},
		{"prose", "I would change the following:", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeUnifiedDiff(tc.proposal); got != tc.want {
				t.Fatalf("looksLikeUnifiedDiff(%q) = %v, want %v", tc.proposal, got, tc.want)
			}
		})
	}
}

func TestParseUnifiedDiffSingleHunk(t *testing.T) {
	proposal := "--- a/app.go\n" +
		"+++ b/app.go\n" +
		"@@ -1,4 +1,4 @@\n" +
		" package main\n" +
		" \n" +
		"-\tprintln(\"hello\")\n" +
		"+\tprintln(\"patched\")\n" +
		" }\n"
	res, err := ParseRepairing(proposal)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(res.Patches))
	}
	fp := res.Patches[0]
	if fp.Path != "app.go" {
		t.Fatalf("path = %q, want %q", fp.Path, "app.go")
	}
	if len(fp.Chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(fp.Chunks))
	}
	wantSearch := "package main\n\n\tprintln(\"hello\")\n}"
	wantReplace := "package main\n\n\tprintln(\"patched\")\n}"
	if fp.Chunks[0].Search != wantSearch {
		t.Fatalf("search = %q, want %q", fp.Chunks[0].Search, wantSearch)
	}
	if fp.Chunks[0].Replace != wantReplace {
		t.Fatalf("replace = %q, want %q", fp.Chunks[0].Replace, wantReplace)
	}
	if len(res.Repairs) != 1 || !strings.Contains(res.Repairs[0], "app.go") {
		t.Fatalf("repairs = %#v, want one note naming app.go", res.Repairs)
	}

	// The converted chunk must validate and apply through the existing path.
	orig := "package main\n\n\tprintln(\"hello\")\n}\n"
	ok, err := ValidatePatch(orig, fp)
	if !ok || err != nil {
		t.Fatalf("ValidatePatch: ok=%v err=%v", ok, err)
	}
	if got, want := ApplyPatch(orig, fp), "package main\n\n\tprintln(\"patched\")\n}\n"; got != want {
		t.Fatalf("ApplyPatch = %q, want %q", got, want)
	}
}

func TestParseUnifiedDiffMultipleHunksSameFile(t *testing.T) {
	proposal := "--- a/x.go\n" +
		"+++ b/x.go\n" +
		"@@ -1,2 +1,2 @@\n" +
		"-alpha\n" +
		"+ALPHA\n" +
		" middle\n" +
		"@@ -10,2 +10,2 @@ func tailSection()\n" +
		" tail\n" +
		"-omega\n" +
		"+OMEGA\n"
	res, err := ParseRepairing(proposal)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(res.Patches))
	}
	chunks := res.Patches[0].Chunks
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if chunks[0].Search != "alpha\nmiddle" || chunks[0].Replace != "ALPHA\nmiddle" {
		t.Fatalf("chunk[0] = %#v", chunks[0])
	}
	if chunks[1].Search != "tail\nomega" || chunks[1].Replace != "tail\nOMEGA" {
		t.Fatalf("chunk[1] = %#v", chunks[1])
	}
}

func TestParseUnifiedDiffMultipleFiles(t *testing.T) {
	proposal := "diff --git a/a.go b/a.go\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/a.go\n" +
		"+++ b/a.go\n" +
		"@@ -1,1 +1,1 @@\n" +
		"-one\n" +
		"+1\n" +
		"diff --git a/b.go b/b.go\n" +
		"index 3333333..4444444 100644\n" +
		"--- a/b.go\n" +
		"+++ b/b.go\n" +
		"@@ -1,1 +1,1 @@\n" +
		"-two\n" +
		"+2\n"
	res, err := ParseRepairing(proposal)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 2 {
		t.Fatalf("patches = %d, want 2", len(res.Patches))
	}
	if res.Patches[0].Path != "a.go" || res.Patches[1].Path != "b.go" {
		t.Fatalf("paths = %q, %q", res.Patches[0].Path, res.Patches[1].Path)
	}
	if len(res.Repairs) != 2 {
		t.Fatalf("repairs = %#v, want one per file", res.Repairs)
	}
}

func TestParseUnifiedDiffNewFile(t *testing.T) {
	proposal := "--- /dev/null\n" +
		"+++ b/new.txt\n" +
		"@@ -0,0 +1,2 @@\n" +
		"+hello\n" +
		"+world\n"
	res, err := ParseRepairing(proposal)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 1 || res.Patches[0].Path != "new.txt" {
		t.Fatalf("patches = %#v", res.Patches)
	}
	ch := res.Patches[0].Chunks[0]
	if ch.Search != "" || ch.Replace != "hello\nworld" {
		t.Fatalf("chunk = %#v, want empty search", ch)
	}
}

func TestParseUnifiedDiffUnprefixedPaths(t *testing.T) {
	proposal := "--- old.go\n+++ old.go\n@@ -1,1 +1,1 @@\n-x\n+y\n"
	res, err := ParseRepairing(proposal)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 1 || res.Patches[0].Path != "old.go" {
		t.Fatalf("patches = %#v", res.Patches)
	}
}

func TestParseUnifiedDiffRemovedDashDashContent(t *testing.T) {
	// A removed line whose content starts with "-- " must stay as hunk body
	// unless the next line opens a real +++ header pair.
	proposal := "--- a/f.go\n" +
		"+++ b/f.go\n" +
		"@@ -1,2 +1,1 @@\n" +
		"--- deprecated flag\n" +
		" ctx\n"
	res, err := ParseRepairing(proposal)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if got := res.Patches[0].Chunks[0].Search; got != "-- deprecated flag\nctx" {
		t.Fatalf("search = %q, want the -- line kept as content", got)
	}
}

func TestParseUnifiedDiffRemovedDashDashFollowedByAdded(t *testing.T) {
	// A removed line "-- deprecated" (rendered "--- deprecated") followed by
	// an added line "++ new" (rendered "+++ new") must both stay as hunk
	// body — the ---/+++ pair lookahead must not mistake them for a file
	// header pair (regression: the edit was silently dropped).
	proposal := "--- a/f.go\n" +
		"+++ b/f.go\n" +
		"@@ -1,2 +1,2 @@\n" +
		"--- deprecated\n" +
		"+++ new\n" +
		" ctx\n"
	res, err := ParseRepairing(proposal)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(res.Patches))
	}
	ch := res.Patches[0].Chunks[0]
	if ch.Search != "-- deprecated\nctx" || ch.Replace != "++ new\nctx" {
		t.Fatalf("chunk = %#v, want both lines kept as content", ch)
	}
}

func TestParseUnifiedDiffAddedPlusPlusAtHunkEnd(t *testing.T) {
	// An added line whose content starts with "++ " (rendered "+++ foo") at
	// the end of a hunk, followed by the next hunk's @@ header, must stay as
	// content — not be mistaken for a new-file section header that drops the
	// line and fabricates a spurious file patch (regression).
	proposal := "--- a/x.go\n" +
		"+++ b/x.go\n" +
		"@@ -1,2 +1,3 @@\n" +
		" ctx\n" +
		"-removed\n" +
		"+++ foo\n" +
		"@@ -5,2 +5,2 @@\n" +
		" tail\n" +
		"-omega\n" +
		"+OMEGA\n"
	res, err := ParseRepairing(proposal)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 1 {
		t.Fatalf("patches = %d, want 1 (no spurious file patch)", len(res.Patches))
	}
	if res.Patches[0].Path != "x.go" {
		t.Fatalf("path = %q, want x.go", res.Patches[0].Path)
	}
	chunks := res.Patches[0].Chunks
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if chunks[0].Search != "ctx\nremoved" || chunks[0].Replace != "ctx\n++ foo" {
		t.Fatalf("chunk[0] = %#v, want the ++ foo line kept as added content", chunks[0])
	}
	if chunks[1].Search != "tail\nomega" || chunks[1].Replace != "tail\nOMEGA" {
		t.Fatalf("chunk[1] = %#v", chunks[1])
	}
}

func TestParseUnifiedDiffNoOpHunkRejected(t *testing.T) {
	proposal := "--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@\n line one\n line two\n"
	_, err := ParseRepairing(proposal)
	if err == nil || !strings.Contains(err.Error(), "no effective changes") {
		t.Fatalf("err = %v, want context-only complaint", err)
	}
}

func TestParseUnifiedDiffDeletionRejected(t *testing.T) {
	proposal := "--- a/old.txt\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-bye\n"
	_, err := ParseRepairing(proposal)
	if err == nil || !strings.Contains(err.Error(), "deletion") || !strings.Contains(err.Error(), "old.txt") {
		t.Fatalf("err = %v, want deletion teaching error naming old.txt", err)
	}
}

func TestParseUnifiedDiffHunkBeforeHeader(t *testing.T) {
	_, err := ParseRepairing("@@ -1,1 +1,1 @@\n-old\n+new\n")
	if err == nil || !strings.Contains(err.Error(), "before any +++ file header") {
		t.Fatalf("err = %v, want hunk-before-header teaching error", err)
	}
}

func TestParseUnifiedDiffElisionRejected(t *testing.T) {
	for _, proposal := range []string{
		"--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,3 @@\n ctx\n ...\n+added\n",
		"--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n ctx\n...\n+added\n",
		"--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@\n // rest of file\n-x\n",
	} {
		_, err := ParseRepairing(proposal)
		if err == nil || !strings.Contains(err.Error(), "elided lines") {
			t.Fatalf("proposal %q: err = %v, want elision teaching error", proposal, err)
		}
	}
}

func TestParseUnifiedDiffRenameRejected(t *testing.T) {
	proposal := "diff --git a/x.go b/y.go\nrename from x.go\nrename to y.go\n"
	_, err := ParseRepairing(proposal)
	if err == nil || !strings.Contains(err.Error(), "renames via unified diff are not supported") {
		t.Fatalf("err = %v, want rename teaching error", err)
	}
}

func TestParseUnifiedDiffMixedFormatsRejected(t *testing.T) {
	proposal := "--- a/x.go\n" +
		"+++ b/x.go\n" +
		"@@ -1,1 +1,1 @@\n" +
		"-old\n" +
		"+new\n" +
		"File: other.go\n" +
		"<<<<<<< SEARCH\n" +
		"a\n" +
		"=======\n" +
		"b\n" +
		">>>>>>> REPLACE\n"
	_, err := ParseRepairing(proposal)
	if err == nil || !strings.Contains(err.Error(), "mixed formats") {
		t.Fatalf("err = %v, want mixed-format teaching error", err)
	}
}

func TestParseUnifiedDiffNoHunks(t *testing.T) {
	_, err := ParseRepairing("--- a/x.go\n+++ b/x.go\n")
	if err == nil || !strings.Contains(err.Error(), "no unified diff hunks found") {
		t.Fatalf("err = %v, want no-hunks teaching error", err)
	}
}

func TestParseRepairingKeepsDiffTextInsideSearchBlocks(t *testing.T) {
	// A search/replace proposal whose SEARCH content happens to contain a
	// "--- ..." line must not be routed to the diff parser.
	proposal := "File: d.txt\n<<<<<<< SEARCH\n--- a/legacy\n=======\n--- a/modern\n>>>>>>> REPLACE"
	res, err := ParseRepairing(proposal)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 1 || res.Patches[0].Chunks[0].Search != "--- a/legacy" {
		t.Fatalf("patches = %#v, want one search/replace chunk", res.Patches)
	}
}
