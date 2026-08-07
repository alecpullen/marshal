package patch

import (
	"strings"
	"testing"
)

// Patch errors are read by a model, which must correct itself from them. The
// recorded failures show the old messages did not carry enough to act on:
// "no valid patches found in proposal" says nothing about what was expected,
// and models fell back to editing files through shell python instead.
func TestParseErrorsNameWhatIsMissing(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "unclosed search names the divider",
			input: "File: a.go\n<<<<<<< SEARCH\nold code\n",
			want:  []string{"unclosed SEARCH", "a.go", "======="},
		},
		{
			// A replacement present at EOF is repaired (see
			// TestParseRepairsReplaceClosedByEOF); only an empty one still
			// errors, because truncation and deletion look identical.
			name:  "unclosed empty replace names the terminator",
			input: "File: a.go\n<<<<<<< SEARCH\nold\n=======\n",
			want:  []string{"unclosed REPLACE", "a.go", ">>>>>>> REPLACE"},
		},
		{
			name:  "no markers describes the format",
			input: "Just some prose explaining an edit, with no blocks at all.\n",
			want:  []string{"File:", "<<<<<<< SEARCH", "=======", ">>>>>>> REPLACE"},
		},
		{
			name:  "unified diff is rejected with format guidance",
			input: "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n",
			want:  []string{"<<<<<<< SEARCH"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.input)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}

// A valid proposal must not be dragged into the new error paths.
func TestParseStillAcceptsValidProposals(t *testing.T) {
	patches, err := Parse("File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(patches) != 1 {
		t.Fatalf("got %d patches, want 1", len(patches))
	}
}
