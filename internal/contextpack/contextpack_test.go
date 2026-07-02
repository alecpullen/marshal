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
