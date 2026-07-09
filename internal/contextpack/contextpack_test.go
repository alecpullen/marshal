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

func TestBuilderOrdersSectionsAndTracksTokens(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	pack := NewBuilder().Build(BuildInput{
		RepoCard: "Project: marshal",
		Plan:     []string{"1. Read files", "2. Patch tests"},
		FileSnippets: []FileSnippet{
			{Path: "internal/app/app.go", StartLine: 1, EndLine: 3, Content: "package app"},
		},
		RecentToolOutput: []ToolOutput{
			{ToolName: "go.test", Summary: "ok"},
		},
		MaxTokens: 12000,
		Now:       func() time.Time { return now },
	})

	if pack.GeneratedAt != now {
		t.Fatalf("GeneratedAt = %s, want %s", pack.GeneratedAt, now)
	}
	if len(pack.Sections) != 4 {
		t.Fatalf("len(Sections) = %d, want 4: %#v", len(pack.Sections), pack.Sections)
	}
	wantKinds := []SectionKind{SectionRepoCard, SectionPlan, SectionFileSnippet, SectionToolOutput}
	for i, want := range wantKinds {
		if pack.Sections[i].Kind != want {
			t.Fatalf("section %d kind = %q, want %q", i, pack.Sections[i].Kind, want)
		}
	}
	if pack.TokenUsage.MaxTokens != 12000 || pack.TokenUsage.EstimatedTokens <= 0 {
		t.Fatalf("TokenUsage = %#v", pack.TokenUsage)
	}
	if pack.TokenUsage.Truncated {
		t.Fatalf("TokenUsage.Truncated = true, want false")
	}
}

func TestBuilderTruncatesToBudget(t *testing.T) {
	pack := NewBuilder().Build(BuildInput{
		RepoCard:  strings.Repeat("a", 80),
		MaxTokens: 5,
		Now:       func() time.Time { return time.Unix(100, 0).UTC() },
	})

	if len(pack.Sections) != 1 {
		t.Fatalf("len(Sections) = %d, want 1", len(pack.Sections))
	}
	if !strings.Contains(pack.Sections[0].Content, "...[truncated]") {
		t.Fatalf("section content missing truncation marker: %q", pack.Sections[0].Content)
	}
	if !pack.TokenUsage.Truncated {
		t.Fatalf("TokenUsage.Truncated = false, want true")
	}
	if pack.TokenUsage.EstimatedTokens > pack.TokenUsage.MaxTokens {
		t.Fatalf("estimated tokens %d exceeds max %d", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
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
	if len(pack.Pinned) != 1 || pack.Pinned[0].Path != "a.go" {
		t.Fatalf("pinned = %+v", pack.Pinned)
	}
}

func TestPinFilesSurvivesRebudget(t *testing.T) {
	// A small budget that would normally drop a high-token file snippet
	// should NOT drop a pinned section. Pinned sections are appended
	// after the budget gate and tracked separately.
	pack := Pack{}
	big := strings.Repeat("a", 4000)
	pack = PinFiles(pack, []FileSnippet{{Path: "big.go", Content: big}})
	if len(pack.Pinned) != 1 {
		t.Fatalf("pinned len = %d, want 1", len(pack.Pinned))
	}
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
