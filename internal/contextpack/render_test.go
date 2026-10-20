package contextpack

import (
	"strings"
	"testing"
	"time"
)

func TestIsVolatile(t *testing.T) {
	volatile := []SectionKind{SectionTodos, SectionSemantic, SectionToolOutput}
	stable := []SectionKind{SectionRepoCard, SectionRepoMap, SectionMemory, SectionPlan, SectionFileSnippet, SectionSessionSummaries}
	for _, k := range volatile {
		if !IsVolatile(k) {
			t.Errorf("IsVolatile(%q) = false, want true", k)
		}
	}
	for _, k := range stable {
		if IsVolatile(k) {
			t.Errorf("IsVolatile(%q) = true, want false", k)
		}
	}
}

func TestRenderSplitsSectionsByVolatility(t *testing.T) {
	pack := Pack{Sections: []Section{
		{Kind: SectionRepoCard, Title: "Repo Card", Content: "CARD-MARKER"},
		{Kind: SectionMemory, Title: "Project Memories", Content: "MEMORY-MARKER"},
		{Kind: SectionSemantic, Title: "Relevant Code", Content: "SEMANTIC-MARKER"},
		{Kind: SectionTodos, Title: "Current Todos", Content: "TODO-MARKER"},
	}}

	stable := RenderStable(pack)
	volatile := RenderVolatile(pack)

	if !strings.HasPrefix(stable, "Project context pack:\n") {
		t.Fatalf("stable header missing:\n%s", stable)
	}
	if !strings.HasPrefix(volatile, "Current working state (reference only, refreshed every request; act on the latest user or system message above):\n") {
		t.Fatalf("volatile header missing:\n%s", volatile)
	}
	for _, want := range []string{"CARD-MARKER", "MEMORY-MARKER"} {
		if !strings.Contains(stable, want) || strings.Contains(volatile, want) {
			t.Errorf("%s must render only in the stable part", want)
		}
	}
	for _, want := range []string{"SEMANTIC-MARKER", "TODO-MARKER"} {
		if !strings.Contains(volatile, want) || strings.Contains(stable, want) {
			t.Errorf("%s must render only in the volatile part", want)
		}
	}
}

func TestRenderPartsEmptyWhenNothingToShow(t *testing.T) {
	onlyStable := Pack{Sections: []Section{{Kind: SectionRepoCard, Title: "Repo Card", Content: "x"}}}
	if got := RenderVolatile(onlyStable); got != "" {
		t.Errorf("RenderVolatile(stable-only pack) = %q, want empty", got)
	}
	onlyVolatile := Pack{Sections: []Section{{Kind: SectionTodos, Title: "Current Todos", Content: "x"}}}
	if got := RenderStable(onlyVolatile); got != "" {
		t.Errorf("RenderStable(volatile-only pack) = %q, want empty", got)
	}
	if got := RenderStable(Pack{}) + RenderVolatile(Pack{}); got != "" {
		t.Errorf("empty pack rendered %q, want empty", got)
	}
}

// The point of the split: rewriting the todo list must not change a single
// byte of the stable render, even when the pack is over budget and truncated
// (the truncation note's token estimate moves with every volatile change).
func TestRenderStableUnaffectedByVolatileChanges(t *testing.T) {
	clock := time.Unix(10_000, 0)
	now := func() time.Time { return clock }
	const budget = 400
	bigMap := strings.Repeat("internal/some/dir/\n", 400) // forces truncation
	base := MergeRepoSections(Pack{}, "Project: marshal", bigMap, budget, now)

	a := MergeTodos(base, []TodoItem{{Content: "read a", Status: "in_progress"}}, budget, now)

	clock = clock.Add(5 * time.Minute)
	b := MergeTodos(base, []TodoItem{{Content: "read a", Status: "completed"}, {Content: "read b", Status: "in_progress"}}, budget, now)

	if !a.TokenUsage.Truncated || !b.TokenUsage.Truncated {
		t.Fatalf("fixture must truncate the pack (got %v / %v)", a.TokenUsage.Truncated, b.TokenUsage.Truncated)
	}
	if RenderStable(a) != RenderStable(b) {
		t.Fatalf("stable render changed with volatile content:\n--- a\n%s\n--- b\n%s", RenderStable(a), RenderStable(b))
	}
	if RenderVolatile(a) == RenderVolatile(b) {
		t.Fatal("volatile render did not change; the fixture is not exercising anything")
	}
	if !strings.Contains(RenderVolatile(b), "Context pack truncated") {
		t.Errorf("truncation note must render in the volatile part:\n%s", RenderVolatile(b))
	}
	if strings.Contains(RenderStable(b), "Context pack truncated") || strings.Contains(RenderStable(b), "Estimated tokens") {
		t.Errorf("truncation note must not render in the stable part:\n%s", RenderStable(b))
	}
}

// TestRenderVolatileTruncationNoteNamesPackBudget pins the wording that stops
// the model reading a truncated *pack* as a truncated *context window* — the
// misleading signal behind the self-finalisation this change fixes. The note
// must keep "Context pack truncated" as its leading words so the existing
// assertions above still hold.
func TestRenderVolatileTruncationNoteNamesPackBudget(t *testing.T) {
	clock := time.Unix(10_000, 0)
	now := func() time.Time { return clock }
	const budget = 400
	bigMap := strings.Repeat("internal/some/dir/\n", 400) // forces truncation
	pack := MergeRepoSections(Pack{}, "Project: marshal", bigMap, budget, now)
	pack = MergeTodos(pack, []TodoItem{{Content: "read a", Status: "in_progress"}}, budget, now)

	if !pack.TokenUsage.Truncated {
		t.Fatal("fixture must truncate the pack")
	}

	volatile := RenderVolatile(pack)
	for _, want := range []string{
		"Context pack truncated",
		"400-token budget",
		"not the model's context window",
	} {
		if !strings.Contains(volatile, want) {
			t.Errorf("volatile render missing %q:\n%s", want, volatile)
		}
	}

	stable := RenderStable(pack)
	for _, unwanted := range []string{
		"Context pack truncated",
		"400-token budget",
		"not the model's context window",
	} {
		if strings.Contains(stable, unwanted) {
			t.Errorf("stable render must not contain %q:\n%s", unwanted, stable)
		}
	}
}
