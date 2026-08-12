package tui

import (
	"strings"
	"testing"
)

func TestExtractSuggestion(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
		ok   bool
	}{
		{
			name: "should_i_question",
			msg:  "Should I proceed with the refactor?",
			want: "yes",
			ok:   true,
		},
		{
			name: "y_n_question",
			msg:  "Do you want to continue (y/n)?",
			want: "yes",
			ok:   true,
		},
		{
			name: "yes_no_question",
			msg:  "Would you like me to run the tests (yes/no)?",
			want: "yes",
			ok:   true,
		},
		{
			name: "shall_i_question_is_yes_no",
			msg:  "Shall I open a pull request?",
			want: "yes",
			ok:   true,
		},
		{
			name: "either_or_tabs",
			msg:  "Would you prefer tabs or spaces?",
			want: "tabs",
			ok:   true,
		},
		{
			name: "either_or_markdown",
			msg:  "Do you want the report in markdown or pdf?",
			want: "markdown",
			ok:   true,
		},
		{
			name: "want_me_to_proposal",
			msg:  "Want me to add a test for that?",
			want: "yes, go ahead",
			ok:   true,
		},
		{
			name: "i_can_proposal",
			msg:  "I can update the documentation for you.",
			want: "yes, go ahead",
			ok:   true,
		},
		{
			name: "no_question_no_proposal",
			msg:  "I've finished the refactor and all tests pass.",
			want: "",
			ok:   false,
		},
		{
			name: "empty_input",
			msg:  "",
			want: "",
			ok:   false,
		},
		{
			name: "question_mid_text_not_at_end",
			msg:  "Should I refactor? Anyway, I've done it.",
			want: "",
			ok:   false,
		},
		{
			name: "code_block_question_no_suggestion",
			msg:  "Here is the code:\n```\nif x > 0 { return }?\n```",
			want: "",
			ok:   false,
		},
		{
			name: "trailing_whitespace",
			msg:  "Should I continue?   ",
			want: "yes",
			ok:   true,
		},
		{
			name: "either_or_option_too_long",
			msg:  "Would you prefer " + strings.Repeat("long", 20) + " or the short one?",
			want: "",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractSuggestion(tt.msg)
			if ok != tt.ok {
				t.Fatalf("extractSuggestion(%q) ok = %v, want %v", tt.msg, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("extractSuggestion(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}
