package contextpack

import (
	"strings"
	"testing"
	"time"
)

func TestNewMemorySectionRanksAndCaps(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(24 * time.Hour)
	memories := []MemoryNote{
		{Kind: "fact", Content: "stale fact", Confidence: "stale", UpdatedAt: newer},         // dropped
		{Kind: "fact", Content: "tentative fact", Confidence: "tentative", UpdatedAt: newer}, // rank 4
		{Kind: "decision", Content: "confirmed decision", Confidence: "confirmed", UpdatedAt: old},
		{Kind: "architecture", Content: "confirmed arch", Confidence: "confirmed", UpdatedAt: old}, // rank 1
		{Kind: "fact", Content: "confirmed fact new", Confidence: "confirmed", UpdatedAt: newer},   // before rank 5
		{Kind: "fact", Content: "confirmed fact old", Confidence: "confirmed", UpdatedAt: old},
		{Kind: "mystery", Content: "unknown kind", Confidence: "", UpdatedAt: newer}, // last
	}
	section, ok := newMemorySection(memories)
	if !ok {
		t.Fatal("newMemorySection returned ok=false, want true")
	}
	want := "[architecture] confirmed arch\n" +
		"[decision] confirmed decision\n" +
		"[fact] confirmed fact new\n" +
		"[fact] confirmed fact old\n" +
		"[fact] tentative fact\n" +
		"[mystery] unknown kind"
	if section.Content != want {
		t.Fatalf("section content =\n%s\nwant:\n%s", section.Content, want)
	}
}

func TestNewMemorySectionCapsAtTwenty(t *testing.T) {
	memories := make([]MemoryNote, 25)
	for i := range memories {
		memories[i] = MemoryNote{Kind: "fact", Content: strings.Repeat("x", 10) + string(rune('a'+i)), Confidence: "tentative", UpdatedAt: time.Unix(int64(i), 0)}
	}
	section, ok := newMemorySection(memories)
	if !ok {
		t.Fatal("newMemorySection returned ok=false, want true")
	}
	if n := len(strings.Split(section.Content, "\n")); n != 20 {
		t.Fatalf("memory lines = %d, want 20", n)
	}
}
