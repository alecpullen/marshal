package tui

import "testing"

func TestFuzzyScoreSubsequence(t *testing.T) {
	cases := []struct {
		query, target string
		wantMatched   bool
	}{
		{"pl", "/plan", true},
		{"plan", "/plan", true},
		{"px", "/plan", false},
		{"run", "internal/agent/runner.go", true},
		{"rn", "internal/agent/runner.go", true},
		{"runner", "internal/agent/runner.go", true},
		{"zzz", "internal/agent/runner.go", false},
		{"", "/plan", true}, // empty query always matches
	}
	for _, c := range cases {
		_, matched := fuzzyScore(c.query, c.target)
		if matched != c.wantMatched {
			t.Errorf("fuzzyScore(%q,%q) matched=%v, want %v", c.query, c.target, matched, c.wantMatched)
		}
	}
}

func TestFuzzyScoreRanksBetterMatchHigher(t *testing.T) {
	// "pl" should score "/plan" higher than "/help" (consecutive prefix match
	// vs scattered subsequence).
	sPlan, _ := fuzzyScore("pl", "/plan")
	sHelp, _ := fuzzyScore("pl", "/help")
	if sPlan <= sHelp {
		t.Errorf("expected /plan to outrank /help: %d vs %d", sPlan, sHelp)
	}
}

func TestFuzzyScoreEmptyQueryMatchesAll(t *testing.T) {
	// Empty query returns a valid match with a small score (length-based tie-breaker).
	s, ok := fuzzyScore("", "anything")
	if !ok {
		t.Fatal("empty query should match")
	}
	if s < 0 {
		t.Fatalf("score for empty query should be non-negative, got %d", s)
	}
}

func TestCompletionPopupFiltersAndSelects(t *testing.T) {
	items := []completionItem{
		{Text: "/plan", Description: "Plan a task", Kind: completionCommand},
		{Text: "/help", Description: "Show help", Kind: completionCommand},
		{Text: "/tools", Description: "List tools", Kind: completionCommand},
	}
	p := newCompletionPopup(items)
	p.update("/pl")
	if !p.isVisible() {
		t.Fatal("popup should be visible after filtering")
	}
	matches := p.matches()
	if len(matches) != 1 || matches[0].Text != "/plan" {
		t.Fatalf("matches = %v, want [/plan]", matches)
	}
	p.accept()
	if p.acceptedText != "/plan " {
		t.Fatalf("acceptedText = %q, want %q", p.acceptedText, "/plan ")
	}
	if p.isVisible() {
		t.Fatal("popup should be hidden after accept")
	}
}

func TestCompletionPopupNoMatchesHidden(t *testing.T) {
	items := []completionItem{{Text: "/plan", Description: "Plan", Kind: completionCommand}}
	p := newCompletionPopup(items)
	p.update("/zzz")
	if p.isVisible() {
		t.Fatal("popup should hide on no matches")
	}
}

func TestCompletionPopupEmptyQueryHides(t *testing.T) {
	items := []completionItem{{Text: "/plan", Description: "Plan", Kind: completionCommand}}
	p := newCompletionPopup(items)
	p.update("/p")
	if !p.isVisible() {
		t.Fatal("popup should be visible after partial match")
	}
	p.update("")
	if p.isVisible() {
		t.Fatal("popup should hide on empty query")
	}
}

func TestCompletionPopupEscDismisses(t *testing.T) {
	items := []completionItem{{Text: "/plan", Description: "Plan", Kind: completionCommand}}
	p := newCompletionPopup(items)
	p.update("/p")
	p.dismiss()
	if p.isVisible() {
		t.Fatal("popup should hide after dismiss")
	}
}

func TestCompletionPopupNavigation(t *testing.T) {
	items := []completionItem{
		{Text: "/plan", Description: "Plan", Kind: completionCommand},
		{Text: "/profile", Description: "Profile", Kind: completionCommand},
		{Text: "/projects", Description: "Projects", Kind: completionCommand},
	}
	p := newCompletionPopup(items)
	p.update("/p")
	if !p.isVisible() {
		t.Fatal("popup should be visible")
	}
	if p.index != 0 {
		t.Fatalf("initial index = %d, want 0", p.index)
	}
	p.moveDown()
	if p.index != 1 {
		t.Fatalf("after moveDown index = %d, want 1", p.index)
	}
	p.moveUp()
	if p.index != 0 {
		t.Fatalf("after moveUp index = %d, want 0", p.index)
	}
	p.moveUp() // wrap to last
	if p.index != 2 {
		t.Fatalf("after moveUp wrap index = %d, want 2", p.index)
	}
	p.moveDown() // wrap to 0
	if p.index != 0 {
		t.Fatalf("after moveDown wrap index = %d, want 0", p.index)
	}
}

func TestCompletionPopupFileKindAcceptedText(t *testing.T) {
	items := []completionItem{
		{Text: "internal/agent/runner.go", Description: "", Kind: completionFile},
	}
	p := newCompletionPopup(items)
	p.update("runner")
	p.accept()
	if p.acceptedText != "@internal/agent/runner.go " {
		t.Fatalf("acceptedText = %q, want %q", p.acceptedText, "@internal/agent/runner.go ")
	}
}

func TestCompletionPopupFileKindOmitsWhitespaceText(t *testing.T) {
	items := []completionItem{
		{Text: "docs/has space.md", Description: "", Kind: completionFile},
		{Text: "internal/agent/runner.go", Description: "", Kind: completionFile},
	}
	p := newCompletionPopup(items)
	p.update("space")
	if p.isVisible() {
		t.Fatalf("popup should not offer whitespace paths, got %#v", p.matches())
	}

	p.update("run")
	if !p.isVisible() {
		t.Fatal("popup should still offer normal paths")
	}
	matches := p.matches()
	if len(matches) != 1 || matches[0].Text != "internal/agent/runner.go" {
		t.Fatalf("matches = %#v, want only internal/agent/runner.go", matches)
	}
}
