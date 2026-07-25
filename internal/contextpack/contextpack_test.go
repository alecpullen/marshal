package contextpack

import (
	"strings"
	"testing"
	"time"
)

func TestEstimateTokensRoundsUpByFourRunes(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"abc", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"abcdefghi", 3},
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Fatalf("EstimateTokens(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestRenderUsesStableSectionFormat(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Source: "repo.card", Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: SectionPlan, Title: "Current Plan", Content: "1. Test\n2. Build", EstimatedTokens: 5},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 9},
	}

	rendered := Render(pack)
	for _, want := range []string{
		"Project context pack:",
		"## Repo Card",
		"Source: repo.card",
		"Estimated tokens: 4",
		"Project: marshal",
		"## Current Plan",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Render() missing %q:\n%s", want, rendered)
		}
	}
}

func TestEmptyPackRendersEmptyAndClonesSafely(t *testing.T) {
	var pack Pack
	if !pack.IsEmpty() {
		t.Fatal("zero Pack should be empty")
	}
	if rendered := Render(pack); rendered != "" {
		t.Fatalf("Render(empty) = %q, want empty", rendered)
	}

	pack = Pack{Sections: []Section{{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project"}}}
	clone := pack.Clone()
	clone.Sections[0].Content = "mutated"
	if pack.Sections[0].Content != "Project" {
		t.Fatalf("Clone did not protect section content: %#v", pack.Sections)
	}
}

func TestRefreshPlanInsertsBeforeSnippetsAndToolOutput(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: SectionFileSnippet, Title: "internal/app/app.go", Content: "package app", EstimatedTokens: 3},
			{Kind: SectionToolOutput, Title: "go.test", Content: "ok", EstimatedTokens: 1},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 8},
	}

	updated := RefreshPlan(pack, []string{"1. Inspect", "2. Patch"}, func() time.Time { return now })

	if len(updated.Sections) != 4 {
		t.Fatalf("len(updated.Sections) = %d, want 4: %#v", len(updated.Sections), updated.Sections)
	}
	wantKinds := []SectionKind{SectionRepoCard, SectionPlan, SectionFileSnippet, SectionToolOutput}
	for i, want := range wantKinds {
		if updated.Sections[i].Kind != want {
			t.Fatalf("section %d kind = %q, want %q", i, updated.Sections[i].Kind, want)
		}
	}
	if updated.GeneratedAt != now {
		t.Fatalf("GeneratedAt = %s, want %s", updated.GeneratedAt, now)
	}
}

func TestRefreshPlanRespectsMaxTokensAndMarksTruncated(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Content: strings.Repeat("r", 8), EstimatedTokens: 2},
			{Kind: SectionFileSnippet, Title: "internal/app/app.go", Content: strings.Repeat("s", 16), EstimatedTokens: 4},
		},
		TokenUsage: TokenUsage{MaxTokens: 8, EstimatedTokens: 6},
	}

	updated := RefreshPlan(pack, []string{strings.Repeat("p", 32)}, func() time.Time { return time.Unix(200, 0).UTC() })

	if updated.TokenUsage.EstimatedTokens > updated.TokenUsage.MaxTokens {
		t.Fatalf("estimated tokens %d exceeds max %d", updated.TokenUsage.EstimatedTokens, updated.TokenUsage.MaxTokens)
	}
	if !updated.TokenUsage.Truncated {
		t.Fatalf("TokenUsage.Truncated = false, want true")
	}
	if len(updated.Sections) != 2 {
		t.Fatalf("len(updated.Sections) = %d, want 2 after snippet skip: %#v", len(updated.Sections), updated.Sections)
	}
	if updated.Sections[0].Kind != SectionRepoCard || updated.Sections[1].Kind != SectionPlan {
		t.Fatalf("section kinds = %#v, want repo card then plan", updated.Sections)
	}
	if !strings.Contains(updated.Sections[1].Content, "...[truncated]") {
		t.Fatalf("plan content missing truncation marker: %q", updated.Sections[1].Content)
	}
}

func TestRefreshPlanPreservesUnknownSectionKinds(t *testing.T) {
	unknownKind := SectionKind("future_kind")
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: unknownKind, Title: "Future", Content: "future payload", Source: "future/source", Priority: 25, EstimatedTokens: 4},
			{Kind: SectionPlan, Title: "Current Plan", Content: "old plan", EstimatedTokens: 2},
			{Kind: SectionToolOutput, Title: "go.test", Content: "ok", EstimatedTokens: 1},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 11},
	}

	updated := RefreshPlan(pack, []string{"1. New plan"}, func() time.Time { return time.Unix(200, 0).UTC() })

	if len(updated.Sections) != 4 {
		t.Fatalf("len(updated.Sections) = %d, want 4: %#v", len(updated.Sections), updated.Sections)
	}
	if updated.Sections[1].Kind != unknownKind {
		t.Fatalf("updated.Sections[1].Kind = %q, want %q", updated.Sections[1].Kind, unknownKind)
	}
	if updated.Sections[1].Source != "future/source" || updated.Sections[1].Priority != 25 {
		t.Fatalf("unknown section metadata changed: %#v", updated.Sections[1])
	}
	if updated.Sections[2].Kind != SectionPlan {
		t.Fatalf("plan section missing after unknown section: %#v", updated.Sections)
	}
}

func TestRebudgetPreservesExistingPlanAndAppliesMaxTokens(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: SectionPlan, Title: "Current Plan", Content: "1. Keep this plan", EstimatedTokens: 5},
			{Kind: SectionFileSnippet, Title: "internal/app/app.go", Source: "internal/app/app.go:1-3", Content: "package app", EstimatedTokens: 3},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 12},
	}

	updated := Rebudget(pack, 24000, func() time.Time { return time.Unix(300, 0).UTC() })

	if updated.TokenUsage.MaxTokens != 24000 {
		t.Fatalf("MaxTokens = %d, want 24000", updated.TokenUsage.MaxTokens)
	}
	if len(updated.Sections) != 3 || updated.Sections[1].Kind != SectionPlan {
		t.Fatalf("sections = %#v, want plan preserved", updated.Sections)
	}
	if updated.Sections[1].Content != "1. Keep this plan" {
		t.Fatalf("plan content = %q", updated.Sections[1].Content)
	}
	if updated.Sections[2].Source != "internal/app/app.go:1-3" {
		t.Fatalf("snippet source = %q", updated.Sections[2].Source)
	}
}

func TestRefreshPlanWithBudgetUsesProvidedMaxTokens(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	}

	updated := RefreshPlanWithBudget(pack, []string{"1. Inspect"}, 24000, func() time.Time { return time.Unix(300, 0).UTC() })

	if updated.TokenUsage.MaxTokens != 24000 {
		t.Fatalf("MaxTokens = %d, want 24000", updated.TokenUsage.MaxTokens)
	}
	if len(updated.Sections) != 2 || updated.Sections[1].Kind != SectionPlan {
		t.Fatalf("sections = %#v, want repo card then plan", updated.Sections)
	}
}

func TestMergeMemoriesInsertsBeforePlanAndSnippets(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: SectionPlan, Title: "Current Plan", Content: "1. Inspect", EstimatedTokens: 3},
			{Kind: SectionFileSnippet, Title: "internal/app/app.go", Content: "package app", EstimatedTokens: 3},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 10},
	}

	memories := []MemoryNote{
		{Kind: "fact", Content: "Uses SQLite for persistence"},
		{Kind: "architecture", Content: "TUI built with Bubble Tea"},
	}

	updated := MergeMemories(pack, memories, 12000, func() time.Time { return now })

	if len(updated.Sections) != 4 {
		t.Fatalf("len(updated.Sections) = %d, want 4: %#v", len(updated.Sections), updated.Sections)
	}
	wantKinds := []SectionKind{SectionRepoCard, SectionMemory, SectionPlan, SectionFileSnippet}
	for i, want := range wantKinds {
		if updated.Sections[i].Kind != want {
			t.Fatalf("section %d kind = %q, want %q", i, updated.Sections[i].Kind, want)
		}
	}
	if !strings.Contains(updated.Sections[1].Content, "Uses SQLite for persistence") {
		t.Fatalf("memory section missing content: %q", updated.Sections[1].Content)
	}
	if !strings.Contains(updated.Sections[1].Content, "TUI built with Bubble Tea") {
		t.Fatalf("memory section missing content: %q", updated.Sections[1].Content)
	}
	if updated.GeneratedAt != now {
		t.Fatalf("GeneratedAt = %s, want %s", updated.GeneratedAt, now)
	}
}

func TestMergeMemoriesReplacesExistingMemorySection(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: SectionMemory, Title: "Project Memories", Content: "[fact] stale note", EstimatedTokens: 3},
			{Kind: SectionPlan, Content: "1. Inspect", EstimatedTokens: 3},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 10},
	}

	updated := MergeMemories(pack, []MemoryNote{{Kind: "fact", Content: "fresh note"}}, 12000, func() time.Time { return time.Unix(300, 0).UTC() })

	if len(updated.Sections) != 3 {
		t.Fatalf("len(updated.Sections) = %d, want 3: %#v", len(updated.Sections), updated.Sections)
	}
	if updated.Sections[1].Kind != SectionMemory || strings.Contains(updated.Sections[1].Content, "stale note") {
		t.Fatalf("memory section not replaced: %#v", updated.Sections[1])
	}
	if !strings.Contains(updated.Sections[1].Content, "fresh note") {
		t.Fatalf("memory section missing new content: %q", updated.Sections[1].Content)
	}
}

func TestMergeMemoriesEmptyIsNoOp(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	}

	updated := MergeMemories(pack, nil, 12000, func() time.Time { return time.Unix(300, 0).UTC() })

	if len(updated.Sections) != 1 || updated.Sections[0].Kind != SectionRepoCard {
		t.Fatalf("expected pack unchanged, got %#v", updated.Sections)
	}
}

func TestMergeMemoriesRespectsMaxTokensAndMarksTruncated(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Content: strings.Repeat("r", 8), EstimatedTokens: 2},
		},
		TokenUsage: TokenUsage{MaxTokens: 8, EstimatedTokens: 2},
	}

	updated := MergeMemories(pack, []MemoryNote{{Kind: "fact", Content: strings.Repeat("m", 64)}}, 8, func() time.Time { return time.Unix(300, 0).UTC() })

	if updated.TokenUsage.EstimatedTokens > updated.TokenUsage.MaxTokens {
		t.Fatalf("estimated tokens %d exceeds max %d", updated.TokenUsage.EstimatedTokens, updated.TokenUsage.MaxTokens)
	}
	if !updated.TokenUsage.Truncated {
		t.Fatal("TokenUsage.Truncated = false, want true")
	}
}

func TestPinFilesAppendsSections(t *testing.T) {
	pack := Pack{}
	pack = PinFiles(pack, []FileSnippet{{Path: "a.go", Content: "package a\n"}})
	if len(pack.Sections) != 1 {
		t.Fatalf("sections len = %d, want 1", len(pack.Sections))
	}
	if pack.Sections[0].Source != "a.go" {
		t.Fatalf("section source = %q, want a.go", pack.Sections[0].Source)
	}
	if pack.Sections[0].Priority != 100 {
		t.Fatalf("section priority = %d, want 100", pack.Sections[0].Priority)
	}
	if pack.Sections[0].EstimatedTokens == 0 {
		t.Fatal("pinned section should have non-zero EstimatedTokens")
	}
}

func TestPinFilesSurvivesRebudget(t *testing.T) {
	// A small budget that would normally drop a high-token file snippet
	// should NOT drop a pinned section. Pinned sections are appended
	// after the budget gate and tracked separately.
	pack := Pack{}
	big := strings.Repeat("a", 4000)
	pack = PinFiles(pack, []FileSnippet{{Path: "big.go", Content: big}})
	// The pack has 0 token usage (PinFiles is post-budget); sections
	// carry the full content.
	if len(pack.Sections) != 1 {
		t.Fatalf("sections len = %d, want 1", len(pack.Sections))
	}
	if pack.Sections[0].Content != big {
		t.Fatal("pinned section content was modified")
	}
}

func TestPinFilesSkipsEmptyContent(t *testing.T) {
	pack := Pack{}
	pack = PinFiles(pack, []FileSnippet{{Path: "empty.go", Content: "  \n\t"}})
	if len(pack.Sections) != 0 {
		t.Fatalf("sections len = %d, want 0 (empty content should be skipped)", len(pack.Sections))
	}
}

func TestPinnedSectionSurvivesBudgetPressure(t *testing.T) {
	// A pinned section (Priority >= 100) must survive even when budget
	// is exhausted by a large non-pinned file snippet.
	sections := []Section{
		{Kind: SectionFileSnippet, Title: "big.go", Content: strings.Repeat("x", 4000), Priority: 30},
	}
	pack := buildPackFromSections(sections, 50, time.Now().UTC())
	pack = PinFiles(pack, []FileSnippet{{Path: "pinned.md", Content: "PINNED"}})

	found := false
	for _, sec := range pack.Sections {
		if sec.Title == "pinned.md" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("pinned section was dropped despite being pinned with Priority 100")
	}
}

func TestRebudgetPutsPinnedSectionsFirst(t *testing.T) {
	// Build a pack with a FileSnippet (regular) first, then PinFiles
	// appends a pinned section. After Rebudget (which calls
	// buildPackFromSections), the pinned section must be first.
	sections := []Section{
		{Kind: SectionFileSnippet, Title: "normal.go", Content: "normal", Priority: 30},
	}
	pack := buildPackFromSections(sections, 10_000, time.Now().UTC())
	pack = PinFiles(pack, []FileSnippet{{Path: "pinned.md", Content: "PINNED"}})
	// pack.Sections order is now: [file_snippet (30), pinned (100)]

	if len(pack.Sections) != 2 {
		t.Fatalf("sections len = %d, want 2", len(pack.Sections))
	}
	if pack.Sections[0].Priority != 30 {
		t.Fatalf("first section priority = %d, want 30", pack.Sections[0].Priority)
	}
	if pack.Sections[1].Priority != 100 {
		t.Fatalf("second section priority = %d, want 100", pack.Sections[1].Priority)
	}

	// Rebudget should reorder: pinned first, then regular.
	rebudgeted := Rebudget(pack, 10_000, nil)
	if len(rebudgeted.Sections) != 2 {
		t.Fatalf("rebudgeted sections len = %d, want 2", len(rebudgeted.Sections))
	}
	if rebudgeted.Sections[0].Priority != 100 {
		t.Errorf("rebudgeted first section priority = %d, want 100 (pinned first)", rebudgeted.Sections[0].Priority)
	}
	if rebudgeted.Sections[1].Priority != 30 {
		t.Errorf("rebudgeted second section priority = %d, want 30", rebudgeted.Sections[1].Priority)
	}
}

func TestMergeSemanticContext(t *testing.T) {
	pack := Pack{}
	snips := []FileSnippet{{Path: "a.go", StartLine: 1, EndLine: 2, Content: "func A(){}"}}
	got := MergeSemanticContext(pack, snips, DefaultMaxTokens, nil)

	var found *Section
	for i := range got.Sections {
		if got.Sections[i].Kind == SectionSemantic {
			found = &got.Sections[i]
		}
	}
	if found == nil || found.Priority != 35 || !strings.Contains(found.Content, "a.go") {
		t.Fatalf("semantic section = %#v", found)
	}

	// Empty snippets removes the section.
	got2 := MergeSemanticContext(got, nil, DefaultMaxTokens, nil)
	for _, s := range got2.Sections {
		if s.Kind == SectionSemantic {
			t.Fatal("empty snippets should remove the semantic section")
		}
	}
}

func TestTrimSectionContentHelper(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"empty", "", "", false},
		{"whitespace", "   \n\t  ", "", false},
		{"plain", "hello", "hello", true},
		{"padded", "  hello  \n", "hello", true},
		{"internal newline", "hello\nworld", "hello\nworld", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := trimSectionContent(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("trimSectionContent(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
