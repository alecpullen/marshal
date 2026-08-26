package tui

import "testing"

// The deterministic rules are a classifier; test them as one.
func TestExtractSuggestion(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		want     string
		wantConf Confidence
	}{
		// Rule 1 — explicit yes/no questions. High: there is nothing a
		// model would improve here, so no LLM call is made.
		{
			name:     "should_i_question",
			msg:      "Should I proceed with the refactor?",
			want:     "yes",
			wantConf: ConfidenceHigh,
		},
		{
			name:     "y_n_question",
			msg:      "Do you want to continue (y/n)?",
			want:     "yes",
			wantConf: ConfidenceHigh,
		},
		{
			name:     "yes_no_question",
			msg:      "Would you like me to run the tests (yes/no)?",
			want:     "yes",
			wantConf: ConfidenceHigh,
		},
		{
			name:     "shall_i_is_claimed_by_rule_1",
			msg:      "Shall I open a pull request?",
			want:     "yes",
			wantConf: ConfidenceHigh,
		},
		{
			name:     "trailing_whitespace",
			msg:      "Should I continue?   ",
			want:     "yes",
			wantConf: ConfidenceHigh,
		},

		// Rule 2 — action proposals. Low: still a heuristic over free
		// text, so in "llm" mode the model gets to replace it.
		{
			name:     "want_me_to_proposal",
			msg:      "Want me to add a test for that?",
			want:     "yes, go ahead",
			wantConf: ConfidenceLow,
		},

		// B1 — either/or questions no longer produce a single word. They
		// fall through to the LLM, which reads the whole question.
		{
			name:     "either_or_single_word_options",
			msg:      "Would you prefer tabs or spaces?",
			want:     "",
			wantConf: ConfidenceNone,
		},
		{
			name:     "either_or_multi_word_options",
			msg:      "Should we use the accessor approach or blank the slots?",
			want:     "",
			wantConf: ConfidenceNone,
		},
		{
			name:     "either_or_markdown",
			msg:      "Do you want the report in markdown or pdf?",
			want:     "",
			wantConf: ConfidenceNone,
		},

		// B2 — "i can " no longer fires on ordinary prose. These three
		// used to yield "yes, go ahead" and suppress the LLM fallback.
		{
			name:     "declarative_i_can_see",
			msg:      "I can see why that test failed.",
			want:     "",
			wantConf: ConfidenceNone,
		},
		{
			name:     "declarative_i_cant_reproduce",
			msg:      "I can't reproduce it, but I can confirm the anchor moved.",
			want:     "",
			wantConf: ConfidenceNone,
		},
		{
			name:     "declarative_i_can_also_note",
			msg:      "That's done. I can also note the config was already correct.",
			want:     "",
			wantConf: ConfidenceNone,
		},
		{
			name:     "i_can_inside_a_question_is_not_a_proposal",
			msg:      "Do you know if I can retry?",
			want:     "",
			wantConf: ConfidenceNone,
		},

		// Non-matches.
		{
			name:     "no_question_no_proposal",
			msg:      "I've finished the refactor and all tests pass.",
			want:     "",
			wantConf: ConfidenceNone,
		},
		{
			name:     "empty_input",
			msg:      "",
			want:     "",
			wantConf: ConfidenceNone,
		},
		{
			name:     "question_mid_text_not_at_end",
			msg:      "Should I refactor? Anyway, I've done it.",
			want:     "",
			wantConf: ConfidenceNone,
		},
		{
			name:     "code_block_question_no_suggestion",
			msg:      "Here is the code:\n```\nif x > 0 { return }?\n```",
			want:     "",
			wantConf: ConfidenceNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conf := extractSuggestion(tt.msg)
			if conf != tt.wantConf {
				t.Fatalf("extractSuggestion(%q) confidence = %v, want %v", tt.msg, conf, tt.wantConf)
			}
			if got != tt.want {
				t.Fatalf("extractSuggestion(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}
