package knowledge

import (
	"errors"
	"testing"
)

func TestParseExtractionValid(t *testing.T) {
	raw := `{"session_summary":"Fixed the login bug.","memories":[{"kind":"fact","content":"Uses SQLite for persistence"},{"kind":"architecture","content":"TUI built with Bubble Tea"}],"file_summaries":{"internal/foo/bar.go":"Handles login validation"}}`

	extraction, err := ParseExtraction(raw)
	if err != nil {
		t.Fatalf("ParseExtraction returned error: %v", err)
	}
	if extraction.SessionSummary != "Fixed the login bug." {
		t.Fatalf("SessionSummary = %q", extraction.SessionSummary)
	}
	if len(extraction.Memories) != 2 {
		t.Fatalf("len(Memories) = %d, want 2: %#v", len(extraction.Memories), extraction.Memories)
	}
	if extraction.Memories[0].Kind != "fact" || extraction.Memories[0].Content != "Uses SQLite for persistence" {
		t.Fatalf("Memories[0] = %#v", extraction.Memories[0])
	}
	if extraction.FileSummaries["internal/foo/bar.go"] != "Handles login validation" {
		t.Fatalf("FileSummaries = %#v", extraction.FileSummaries)
	}
}

func TestParseExtractionStripsMarkdownFence(t *testing.T) {
	raw := "```json\n{\"session_summary\":\"s\",\"memories\":[],\"file_summaries\":{}}\n```"

	extraction, err := ParseExtraction(raw)
	if err != nil {
		t.Fatalf("ParseExtraction returned error: %v", err)
	}
	if extraction.SessionSummary != "s" {
		t.Fatalf("SessionSummary = %q, want %q", extraction.SessionSummary, "s")
	}
}

func TestParseExtractionDefaultsMissingKindToFact(t *testing.T) {
	raw := `{"session_summary":"s","memories":[{"content":"no kind given"}],"file_summaries":{}}`

	extraction, err := ParseExtraction(raw)
	if err != nil {
		t.Fatalf("ParseExtraction returned error: %v", err)
	}
	if len(extraction.Memories) != 1 || extraction.Memories[0].Kind != "fact" {
		t.Fatalf("Memories = %#v, want kind defaulted to fact", extraction.Memories)
	}
}

func TestParseExtractionSkipsBlankMemoryContent(t *testing.T) {
	raw := `{"session_summary":"s","memories":[{"kind":"fact","content":"   "}],"file_summaries":{}}`

	extraction, err := ParseExtraction(raw)
	if err != nil {
		t.Fatalf("ParseExtraction returned error: %v", err)
	}
	if len(extraction.Memories) != 0 {
		t.Fatalf("Memories = %#v, want empty (blank content skipped)", extraction.Memories)
	}
}

func TestParseExtractionRejectsNoJSONObject(t *testing.T) {
	_, err := ParseExtraction("I don't know what happened.")
	if !errors.Is(err, ErrNoExtractionFound) {
		t.Fatalf("err = %v, want ErrNoExtractionFound", err)
	}
}

func TestParseExtractionRejectsMalformedJSON(t *testing.T) {
	_, err := ParseExtraction(`{"session_summary": "s", "memories": [`)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}
