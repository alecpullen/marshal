package history

import (
	"strings"
	"testing"
)

func TestSummarizer_ShouldSummarize(t *testing.T) {
	s := NewSummarizer()

	// Empty history should not need summarization
	if s.ShouldSummarize([]Entry{}) {
		t.Error("empty history should not need summarization")
	}

	// Small history should not need summarization
	entries := make([]Entry, 10)
	for i := range entries {
		entries[i] = Entry{Tokens: 100}
	}
	if s.ShouldSummarize(entries) {
		t.Error("10 entries should not need summarization")
	}

	// Large history should need summarization
	entries = make([]Entry, 51)
	if !s.ShouldSummarize(entries) {
		t.Error("51 entries should need summarization")
	}

	// High token history should need summarization
	entries = make([]Entry, 10)
	for i := range entries {
		entries[i] = Entry{Tokens: 500}
	}
	if !s.ShouldSummarize(entries) {
		t.Error("5000 tokens should need summarization")
	}
}

func TestSummarizer_Summarize(t *testing.T) {
	s := NewSummarizer()

	entries := []Entry{
		{Role: "user", Content: "Add a new feature to main.go", Tokens: 100},
		{Role: "assistant", Content: "I'll help you add the feature to main.go", Tokens: 150},
		{Role: "user", Content: "Also update config.yaml", Tokens: 80},
		{Role: "assistant", Content: "Config.yaml has been updated", Tokens: 120},
	}

	summary := s.Summarize(entries)

	if summary.TotalEntries != 4 {
		t.Errorf("expected 4 entries, got %d", summary.TotalEntries)
	}

	if summary.TotalTokens == 0 {
		t.Error("expected non-zero token count")
	}

	// Should mention main.go
	if !strings.Contains(summary.SummaryText, "main.go") {
		t.Error("summary should mention main.go")
	}

	// Should mention config.yaml
	if !strings.Contains(summary.SummaryText, "config.yaml") {
		t.Error("summary should mention config.yaml")
	}
}

func TestExtractFileMentions(t *testing.T) {
	tests := []struct {
		text     string
		expected []string
	}{
		{
			text:     "Look at main.go for the issue",
			expected: []string{"main.go"},
		},
		{
			text:     "Update config.yaml and main.go",
			expected: []string{"config.yaml", "main.go"},
		},
		{
			text:     "No files here",
			expected: nil,
		},
		{
			text:     "Check out src/utils.go and test_test.go",
			expected: []string{"src/utils.go", "test_test.go"},
		},
	}

	for _, tc := range tests {
		files := extractFileMentions(tc.text)
		if len(files) != len(tc.expected) {
			t.Errorf("extractFileMentions(%q) = %v, expected %v", tc.text, files, tc.expected)
			continue
		}
		for i, f := range files {
			if f != tc.expected[i] {
				t.Errorf("extractFileMentions(%q)[%d] = %q, expected %q", tc.text, i, f, tc.expected[i])
			}
		}
	}
}

func TestHistory_AddAndCount(t *testing.T) {
	h := NewHistory()

	if h.Count() != 0 {
		t.Error("new history should be empty")
	}

	h.Add("user", "Hello")
	h.Add("assistant", "Hi there")
	h.Add("user", "How are you?")

	if h.Count() != 3 {
		t.Errorf("expected 3 entries, got %d", h.Count())
	}
}

func TestHistory_Clear(t *testing.T) {
	h := NewHistory()

	h.Add("user", "Hello")
	h.Add("assistant", "Hi")
	h.Clear()

	if h.Count() != 0 {
		t.Errorf("expected 0 entries after clear, got %d", h.Count())
	}
}

func TestHistory_Summary(t *testing.T) {
	h := NewHistory()

	h.Add("user", "Add feature to main.go")
	h.Add("assistant", "Done")
	h.Add("user", "Also update test.go")

	summary := h.Summary()

	if summary.TotalEntries != 3 {
		t.Errorf("expected 3 entries in summary, got %d", summary.TotalEntries)
	}

	if summary.TotalTokens == 0 {
		t.Error("expected non-zero token count")
	}
}
