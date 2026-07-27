package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

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
		{Text: "plan", Description: "Plan a task", Kind: completionCommand},
		{Text: "help", Description: "Show help", Kind: completionCommand},
		{Text: "tools", Description: "List tools", Kind: completionCommand},
	}
	p := newCompletionPopup(items)
	p.update("pl")
	if !p.isVisible() {
		t.Fatal("popup should be visible after filtering")
	}
	matches := p.matches()
	if len(matches) != 1 || matches[0].Text != "plan" {
		t.Fatalf("matches = %v, want [plan]", matches)
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
	items := []completionItem{{Text: "plan", Description: "Plan", Kind: completionCommand}}
	p := newCompletionPopup(items)
	p.update("/zzz")
	if p.isVisible() {
		t.Fatal("popup should hide on no matches")
	}
}

func TestCompletionPopupEmptyQueryHides(t *testing.T) {
	items := []completionItem{{Text: "plan", Description: "Plan", Kind: completionCommand}}
	p := newCompletionPopup(items)
	p.update("pl")
	if !p.isVisible() {
		t.Fatal("popup should be visible after partial match")
	}
	p.update("")
	if p.isVisible() {
		t.Fatal("popup should hide on empty query")
	}
}

func TestCompletionPopupEscDismisses(t *testing.T) {
	items := []completionItem{{Text: "plan", Description: "Plan", Kind: completionCommand}}
	p := newCompletionPopup(items)
	p.update("pl")
	p.dismiss()
	if p.isVisible() {
		t.Fatal("popup should hide after dismiss")
	}
}

func TestCompletionPopupNavigation(t *testing.T) {
	items := []completionItem{
		{Text: "plan", Description: "Plan", Kind: completionCommand},
		{Text: "profile", Description: "Profile", Kind: completionCommand},
		{Text: "projects", Description: "Projects", Kind: completionCommand},
	}
	p := newCompletionPopup(items)
	p.update("p")
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

func TestCompletionPopupScrollsToKeepIndexVisible(t *testing.T) {
	// Build a list longer than the visible window so the scroll offset
	// has somewhere to go.
	items := make([]completionItem, 0, 20)
	for i := 0; i < 20; i++ {
		items = append(items, completionItem{
			Text: "cmd" + string(rune('a'+i)),
			Kind: completionCommand,
		})
	}
	p := newCompletionPopup(items)
	p.update("cmd")
	if p.index != 0 || p.viewOffset != 0 {
		t.Fatalf("initial state index=%d offset=%d, want 0/0", p.index, p.viewOffset)
	}
	// Move past the visible window (completionPopupMax == 8). The
	// selected index should remain in view via the viewOffset.
	for i := 0; i < 12; i++ {
		p.moveDown()
	}
	if p.index != 12 {
		t.Fatalf("index = %d, want 12", p.index)
	}
	if p.viewOffset+completionPopupMax <= p.index {
		t.Fatalf("viewOffset %d does not include index %d (max=%d)", p.viewOffset, p.index, completionPopupMax)
	}
	// Move back up — viewOffset should retreat with the index.
	for i := 0; i < 12; i++ {
		p.moveUp()
	}
	if p.index != 0 {
		t.Fatalf("index = %d, want 0", p.index)
	}
	if p.viewOffset != 0 {
		t.Fatalf("viewOffset = %d, want 0 after moving back to top", p.viewOffset)
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

func TestFuzzyScoreRecordsMatchIndices(t *testing.T) {
	idxs, ok := fuzzyMatchIndices("pl", "/plan")
	if !ok {
		t.Fatal("expected match")
	}
	if len(idxs) != 2 || idxs[0] != 1 || idxs[1] != 2 { // positions of p,l in "/plan"
		t.Fatalf("match indices = %v, want [1 2]", idxs)
	}
}

func TestFuzzyMatchIndicesUseRunePositions(t *testing.T) {
	idxs, ok := fuzzyMatchIndices("pl", "λ/plan")
	if !ok {
		t.Fatal("expected match")
	}
	if len(idxs) != 2 || idxs[0] != 2 || idxs[1] != 3 {
		t.Fatalf("match indices = %v, want [2 3]", idxs)
	}
}

func TestCompletionPopupShowsSlashPrefix(t *testing.T) {
	pop := newCompletionPopup([]completionItem{
		{Text: "plan", Description: "Plan a task", Kind: completionCommand},
	})
	pop.update("pl")
	m := Model{cmdPopup: pop, width: 80, leftWidth: 80}
	out := stripANSI(m.renderCompletionPopup())
	if !strings.Contains(out, "/plan") {
		t.Fatalf("expected /plan in popup, got %q", out)
	}
}

func TestInputAreaRowsHandlesWrappedContent(t *testing.T) {
	m := newTestModel(t)
	m.state.SetPendingApproval(&session.PendingToolCall{Command: strings.Repeat("x ", 200)})
	m.approvalModel = newApprovalModel(m.state.PendingApproval(), m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.state.HasBackup(), max(m.width-4, 30))
	rows := m.inputAreaRows()
	lineCount := len(strings.Split(m.approvalModel.View(), "\n"))
	if rows < lineCount {
		t.Fatalf("rows undercount: %d < %d", rows, lineCount)
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

func TestSetArgumentCompletion(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("/set shell")
	m.updateCompletionPopups()
	p := m.activeCompletionPopup()
	if p == nil || !p.isVisible() {
		t.Fatal("expected key completion popup for /set argument")
	}
	found := false
	for _, item := range p.matches() {
		if strings.Contains(item.Text, "shell.allow_network") {
			found = true
		}
	}
	if !found {
		t.Error("shell.allow_network not offered")
	}
}

func TestSetValueCompletionForToggle(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("/set shell.allow_network ")
	m.updateCompletionPopups()
	p := m.activeCompletionPopup()
	if p == nil || !p.isVisible() {
		t.Fatal("expected value completion popup")
	}
	texts := ""
	for _, item := range p.matches() {
		texts += item.Text + " "
	}
	if !strings.Contains(texts, "on") || !strings.Contains(texts, "off") {
		t.Errorf("toggle values not offered, got %q", texts)
	}
}

func TestSetCompletionReplacesCurrentToken(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("/set shell")
	m.updateCompletionPopups()
	for index, item := range m.setPopup.matches() {
		if item.Text == "shell.allow_network" {
			m.setPopup.index = index
			break
		}
	}
	if !m.acceptCompletion() {
		t.Fatal("expected to accept a setting key completion")
	}
	if got := m.input.Value(); got != "/set shell.allow_network " {
		t.Fatalf("input = %q, want %q", got, "/set shell.allow_network ")
	}

	m.input.SetValue("/set shell.allow_network on")
	m.updateCompletionPopups()
	if !m.acceptCompletion() {
		t.Fatal("expected to accept a setting value completion")
	}
	if got := m.input.Value(); got != "/set shell.allow_network on " {
		t.Fatalf("input = %q, want %q", got, "/set shell.allow_network on ")
	}
}
