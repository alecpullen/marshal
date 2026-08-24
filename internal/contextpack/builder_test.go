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

func TestMergeSessionSummaries(t *testing.T) {
	pack := MergeSessionSummaries(Pack{}, "Recent sessions (prior work):\n- a: did a", 0, nil)
	if len(pack.Sections) != 1 || pack.Sections[0].Kind != SectionSessionSummaries {
		t.Fatalf("sections = %+v", pack.Sections)
	}
	// Empty content removes the section.
	pack = MergeSessionSummaries(pack, "", 0, nil)
	if len(pack.Sections) != 0 {
		t.Fatalf("empty content must drop the section, got %+v", pack.Sections)
	}
}

func TestRebudgetRestoresTruncatedContent(t *testing.T) {
	long := strings.Repeat("alpha beta gamma delta ", 2000) // ~11k tokens
	sections := []Section{
		{Kind: SectionRepoMap, Title: "Directory Map", Priority: 11, Content: long},
	}

	// Squeeze it into a small budget: the section must be truncated.
	small := buildPackFromSections(sections, 1000, time.Now().UTC())
	if !small.TokenUsage.Truncated {
		t.Fatal("expected the small budget to truncate the section")
	}
	if len(small.Sections) != 1 {
		t.Fatalf("sections = %d, want 1", len(small.Sections))
	}
	if !strings.Contains(small.Sections[0].Content, "...[truncated]") {
		t.Fatal("expected a truncation marker in the small pack")
	}

	// Now rebudget the ALREADY-TRUNCATED pack upward. The original text
	// must come back — otherwise raising a model's budget can never
	// recover context that an earlier smaller budget threw away.
	big := Rebudget(small, 60_000, nil)
	if big.TokenUsage.Truncated {
		t.Error("expected no truncation at the larger budget")
	}
	if got := big.Sections[0].Content; got != long {
		t.Errorf("content not restored: got %d runes, want %d", len([]rune(got)), len([]rune(long)))
	}
}

func TestOversizedSectionCannotStarveTheRest(t *testing.T) {
	huge := strings.Repeat("directory listing entry ", 5000) // ~30k tokens
	sections := []Section{
		{Kind: SectionRepoMap, Title: "Directory Map", Priority: 11, Content: huge},
		{Kind: SectionMemory, Title: "Project Memories", Priority: 15,
			Content: "We chose SQLite over Postgres for project persistence."},
		{Kind: SectionTodos, Title: "Current Todos", Priority: 45,
			Content: "- Fix the skill budget bug"},
	}

	pack := buildPackFromSections(sections, 12_000, time.Now().UTC())

	rendered := Render(pack)
	for _, probe := range []string{"SQLite over Postgres", "Fix the skill budget bug"} {
		if !strings.Contains(rendered, probe) {
			t.Errorf("%q was starved out of the pack by the oversized map", probe)
		}
	}

	// The map must be present but capped at its share.
	var mapTokens int
	for _, s := range pack.Sections {
		if s.Kind == SectionRepoMap {
			mapTokens = s.EstimatedTokens
		}
	}
	if mapTokens == 0 {
		t.Fatal("repo map was dropped entirely; it should be truncated, not evicted")
	}
	if share := 12_000 / sectionShareDenominator; mapTokens > share {
		t.Errorf("repo map used %d tokens, want <= %d (its share)", mapTokens, share)
	}
}

func TestBudgetIsAllocatedInPriorityOrder(t *testing.T) {
	// Scratchpad (50) is inserted FIRST but is the least foundational.
	// Memory (15) is inserted last. With only enough budget for one of
	// them, Memory must win on priority, not on insertion order.
	filler := strings.Repeat("x ", 2000) // ~1000 tokens each
	sections := []Section{
		{Kind: SectionScratchpad, Title: "Scratchpad", Priority: 50, Content: filler},
		{Kind: SectionMemory, Title: "Project Memories", Priority: 15, Content: "MEMORY-MARKER " + filler},
	}

	pack := buildPackFromSections(sections, 1200, time.Now().UTC())

	if !strings.Contains(Render(pack), "MEMORY-MARKER") {
		t.Error("memory (priority 15) lost its budget to scratchpad (priority 50)")
	}
}
