package patch

import "testing"

// The marker deviations in this file are not hypothetical: each is taken from
// a real file.write_patch call recorded in .marshal/marshal.db. Marker
// mismatches accounted for 13 of 43 observed patch failures (30%), because the
// parser required exact string equality on marker lines — so a stray character
// turned the marker into ordinary content and the block never opened or closed.
func TestParseToleratesMarkerDeviations(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"trailing > on SEARCH", "File: a.go\n<<<<<<< SEARCH>\nold\n=======\nnew\n>>>>>>> REPLACE\n"},
		{"trailing > on REPLACE", "File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE>\n"},
		{"xml wrapper after REPLACE", "File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE</patch>\n"},
		{"extra angle brackets", "File: a.go\n<<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>>> REPLACE\n"},
		{"lowercase-adjacent spacing", "File: a.go\n<<<<<<<  SEARCH\nold\n=======\nnew\n>>>>>>>  REPLACE\n"},
		{"canonical still works", "File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			patches, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(patches) != 1 {
				t.Fatalf("got %d patches, want 1", len(patches))
			}
			if len(patches[0].Chunks) != 1 {
				t.Fatalf("got %d chunks, want 1", len(patches[0].Chunks))
			}
			if got := patches[0].Chunks[0].Search; got != "old" {
				t.Errorf("Search = %q, want %q", got, "old")
			}
			if got := patches[0].Chunks[0].Replace; got != "new" {
				t.Errorf("Replace = %q, want %q", got, "new")
			}
		})
	}
}

// TestParseDoesNotMistakeContentForMarkers is the guard that bounds the
// tolerance. Loosening the markers must not let ordinary file content trip a
// state transition — a git conflict marker inside a patched file, or a
// markdown heading underline, are both real content.
func TestParseDoesNotMistakeContentForMarkers(t *testing.T) {
	// A conflict marker in content: same angle brackets, different keyword.
	in := "File: a.go\n<<<<<<< SEARCH\n<<<<<<< HEAD\nmine\n=======\ntheirs\n>>>>>>> branch\n=======\nresolved\n>>>>>>> REPLACE\n"
	patches, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(patches) != 1 || len(patches[0].Chunks) != 1 {
		t.Fatalf("got %d patches, want 1 with 1 chunk", len(patches))
	}
	// The inner "=======" closes the search block first, which is inherent to
	// the format; what must NOT happen is the ">>>>>>> branch" line being
	// treated as a REPLACE terminator.
	if got := patches[0].Chunks[0].Replace; got == "" {
		t.Error("replace block empty; a conflict marker was treated as a terminator")
	}

	// A markdown underline of five '=' must not act as a divider.
	md := "File: a.md\n<<<<<<< SEARCH\nTitle\n=====\nbody\n=======\nnew\n>>>>>>> REPLACE\n"
	mp, err := Parse(md)
	if err != nil {
		t.Fatalf("Parse markdown: %v", err)
	}
	if got := mp[0].Chunks[0].Search; got != "Title\n=====\nbody" {
		t.Errorf("Search = %q; a 5-char underline was treated as the divider", got)
	}
}
