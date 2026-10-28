package sessionsheet

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
)

func TestRulesSectionIdentity(t *testing.T) {
	s := RulesSection{}
	if s.ID() != "rules" {
		t.Errorf("ID = %q, want rules", s.ID())
	}
	if s.Priority() != 4 {
		t.Errorf("Priority = %d, want 4", s.Priority())
	}
	if s.Clippable() {
		t.Error("Clippable = true, want false")
	}
}

func TestRulesSectionRelevance(t *testing.T) {
	if (RulesSection{}).Relevant(Data{}) {
		t.Error("Relevant(no rules) = true, want false")
	}
	if !(RulesSection{}).Relevant(Data{Rules: []string{"go test"}}) {
		t.Error("Relevant(rules) = false, want true")
	}
}

func TestRulesSectionRender(t *testing.T) {
	d := Data{Rules: []string{"go test", "gofmt"}}
	got := StripANSI(strings.Join((RulesSection{}).Render(d, 30, 10), "\n"))
	if !strings.Contains(got, "go test") || !strings.Contains(got, "gofmt") {
		t.Errorf("Render missing rules:\n%s", got)
	}
}

func TestRulesSectionOneLine(t *testing.T) {
	d := Data{Rules: []string{"go test", "gofmt"}}
	if got := StripANSI((RulesSection{}).OneLine(d, 30)); !strings.Contains(got, "2") {
		t.Errorf("OneLine = %q, want the rule count", got)
	}
}

// detailPosRow matches the detail view's position indicator once its frame
// gutter and padding are stripped ("1–12/40").
var detailPosRow = regexp.MustCompile(`^\d+–\d+/\d+$`)

// frameContentRows returns a rendered frame's inner content rows with the
// gutters and right padding removed, so a test can assert on what a reader
// actually sees rather than on the border drawing.
func frameContentRows(view string) []string {
	rows := strings.Split(StripANSI(view), "\n")
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		row = strings.TrimRight(row, " ")
		if !strings.HasPrefix(row, "│") || !strings.HasSuffix(row, "│") {
			continue
		}
		r := []rune(row)
		if len(r) < 4 {
			continue
		}
		out = append(out, strings.TrimRight(string(r[2:len(r)-2]), " "))
	}
	return out
}

// isRuleContent reports whether a detail body row is a wrapped rule line
// rather than one of the view's own chrome rows.
func isRuleContent(row string) bool {
	trimmed := strings.TrimSpace(row)
	if trimmed == "" || trimmed == "↑ more" || trimmed == "↓ more" {
		return false
	}
	return !detailPosRow.MatchString(trimmed)
}

// TestRulesDetailShowsEveryRule scrolls the detail view through a short dock
// budget with real key updates and a mid-scroll resize, accumulating every
// physical line it renders: no rule may be unreachable or clipped away.
func TestRulesDetailShowsEveryRule(t *testing.T) {
	rules := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		// Every word names its rule, so no two rules share a physical line and
		// a set of rendered lines identifies exactly what was shown.
		words := make([]string, 0, 8)
		for j := 0; j < 8; j++ {
			words = append(words, fmt.Sprintf("arg-%02d-%d", i, j))
		}
		rules = append(rules, fmt.Sprintf("rule-%02d %s", i, strings.Join(words, " ")))
	}
	d := Data{Rules: rules}
	p := NewPanel([]Section{RulesSection{}}, func() Data { return d })

	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatalf("Enter on rules emitted %T, want the detail view", cmd())
	}
	if !p.rulesDetail {
		t.Fatal("Enter on rules did not open the detail view")
	}

	width, height := 40, 7
	inner := max(layout.PanelWidth(width)-3, 1)
	want := make(map[string]bool, len(rules)*3)
	for _, r := range rules {
		for _, line := range strings.Split(ansi.Wrap(" allow "+r, inner, ""), "\n") {
			want[strings.TrimRight(line, " ")] = true
		}
	}

	seen := map[string]bool{}
	record := func() {
		out := p.View(width, height)
		if got := strings.Count(out, "\n") + 1; got > height {
			t.Fatalf("detail frame rendered %d rows at height %d:\n%s", got, height, out)
		}
		for _, row := range frameContentRows(out) {
			if isRuleContent(row) {
				seen[row] = true
			}
		}
	}

	for i := 0; i < 2000; i++ {
		record()
		if !strings.Contains(StripANSI(p.View(width, height)), "↓ more") {
			break
		}
		before := p.rulesOffset
		if i < 3 {
			p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		} else {
			p.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		}
		if p.rulesOffset == before {
			t.Fatalf("detail stuck at offset %d with more content below", before)
		}
		// Resize mid-scroll: the offset must clamp against the new height
		// instead of drifting past the last page.
		switch i {
		case 3:
			height = 5
		case 6:
			height = 9
		case 9:
			height = 7
		}
	}

	if len(seen) != len(want) {
		missing := make([]string, 0, len(want))
		for line := range want {
			if !seen[line] {
				missing = append(missing, line)
			}
		}
		sort.Strings(missing)
		t.Fatalf("detail showed %d of %d physical lines; missing:\n%s",
			len(seen), len(want), strings.Join(missing, "\n"))
	}
}

// TestRulesDetailLiveDataAndEscape pins the detail view's lifecycle: it
// renders live data rather than a snapshot, Escape returns to the sheet
// without closing it, Ctrl+B still closes, and no panel state is mutated.
func TestRulesDetailLiveDataAndEscape(t *testing.T) {
	rules := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		rules = append(rules, fmt.Sprintf("rule-%02d %s", i, strings.Repeat("arg ", 20)))
	}
	orig := append([]string(nil), rules...)
	d := Data{Rules: rules}
	p := NewPanel([]Section{RulesSection{}}, func() Data { return d })

	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatalf("Enter on rules emitted %T, want the detail view", cmd())
	}
	if !p.rulesDetail {
		t.Fatal("Enter on rules did not open the detail view")
	}

	// Scroll to the end, then take the rules away from under the view.
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if p.rulesOffset == 0 {
		t.Fatalf("End left the detail at offset 0:\n%s", StripANSI(p.View(60, 6)))
	}
	d.Rules = nil
	out := StripANSI(p.View(60, 6))
	if !strings.Contains(out, "Nothing to show.") {
		t.Fatalf("detail with no rules did not say so:\n%s", out)
	}
	if strings.Contains(out, "rule-00") {
		t.Fatalf("detail kept a stale rule:\n%s", out)
	}
	if p.rulesOffset != 0 {
		t.Errorf("empty detail kept offset %d, want 0", p.rulesOffset)
	}

	// Live data flows back in without re-entering the view.
	d.Rules = orig
	if out := StripANSI(p.View(60, 6)); !strings.Contains(out, "rule-00") {
		t.Fatalf("detail did not pick the rules back up:\n%s", out)
	}

	// Escape returns to the sheet and emits nothing.
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd != nil {
		t.Fatalf("Esc in detail emitted %T, want a return to the sheet", cmd())
	}
	if p.rulesDetail {
		t.Fatal("Esc left the detail view open")
	}
	if p.rulesOffset != 0 {
		t.Errorf("returning to the sheet kept offset %d, want 0", p.rulesOffset)
	}
	out = StripANSI(p.View(60, 6))
	if strings.Contains(out, "↑↓ scroll") {
		t.Errorf("Esc did not return to the sheet:\n%s", out)
	}
	if !strings.Contains(out, "↵ open") {
		t.Errorf("the sheet after Esc does not advertise Enter:\n%s", out)
	}

	// Ctrl+B still closes outright, from inside the detail view too.
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	cmd := p.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+B in detail emitted no message")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("Ctrl+B emitted %T, want ClosedMsg", cmd())
	}

	if !reflect.DeepEqual(rules, orig) {
		t.Fatalf("detail mutated the rules: %v, want %v", rules, orig)
	}
	if len(p.sections) != 1 || p.sections[0].ID() != rulesSectionID {
		t.Fatalf("detail mutated the sections: %v", p.sections)
	}
}

// TestSheetEnterHintMatchesSelectedAction pins that the sheet only advertises
// Enter where Enter does something: the rules detail view, or a section with
// a slash command.
func TestSheetEnterHintMatchesSelectedAction(t *testing.T) {
	const width, height = 100, 20

	withRules := NewPanel([]Section{RulesSection{}}, func() Data {
		return Data{Rules: []string{"go test *", "gofmt -l ."}}
	})
	if out := StripANSI(withRules.View(width, height)); !strings.Contains(out, "↵ open") {
		t.Errorf("rules sheet omits the Enter hint:\n%s", out)
	}
	if cmd := withRules.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Errorf("Enter on rules emitted %T, want the detail view and no message", cmd())
	}
	if !withRules.rulesDetail {
		t.Error("Enter on rules did not open the detail view")
	}

	actionable := NewPanel([]Section{ContextSection{}}, func() Data {
		return Data{Context: acpwire.SheetContext{
			MaxTokens:       200000,
			EstimatedTokens: 1200,
			Sections:        []acpwire.SheetContextSection{{Title: "System prompt", EstimatedTokens: 900}},
		}}
	})
	if out := StripANSI(actionable.View(width, height)); !strings.Contains(out, "↵ open") {
		t.Errorf("actionable sheet omits the Enter hint:\n%s", out)
	}
	cmd := actionable.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on an actionable section emitted no message")
	}
	got, ok := cmd().(RunCommandMsg)
	if !ok || got.Command != "context" {
		t.Fatalf("Enter on context emitted %#v, want RunCommandMsg{context}", cmd())
	}

	inert := NewPanel([]Section{RepoSection{}}, func() Data { return Data{} })
	if out := StripANSI(inert.View(width, height)); strings.Contains(out, "↵ open") {
		t.Errorf("a section with no action advertises ↵ open:\n%s", out)
	}
	if cmd := inert.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Errorf("Enter on a section with no action emitted %T, want nothing", cmd())
	}
	if inert.rulesDetail {
		t.Error("Enter on a section with no action opened the rules detail view")
	}
}

// TestRulesDetailOverflowThemes pins that the rules detail view keeps its
// meaning without colour. The same paged scenario runs under monochrome, a dark
// preset and a light preset: after an ANSI strip the rule text, its allow
// label, the way back and the position row all survive, and every frame fits
// the dock budget it was given.
func TestRulesDetailOverflowThemes(t *testing.T) {
	prev := theme.Current()
	t.Cleanup(func() { theme.Reload(prev) })

	rules := make([]string, 0, 24)
	for i := 1; i <= 24; i++ {
		rules = append(rules, fmt.Sprintf("RULE-%02d %s",
			i, strings.Repeat("allow the shell to run go test and gofmt here ", 2)))
	}

	for _, tc := range []struct {
		name string
		th   theme.Theme
	}{
		{"monochrome", theme.LoadFor(true, "xterm-256color")},
		{"dark", theme.LoadWithConfig("warm-sunset", theme.ModeDark, theme.DepthFlat, nil)},
		{"light", theme.LoadWithConfig("warm-sunset-light", theme.ModeLight, theme.DepthFlat, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			theme.Reload(tc.th)

			panel := NewPanel([]Section{RulesSection{}}, func() Data { return Data{Rules: rules} })
			if cmd := panel.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
				t.Fatalf("%s: entering the rules detail view emitted %T, want nothing", tc.name, cmd())
			}

			// Short budget so the rules overflow and the indicator is in play.
			const width, height = 80, 12
			var frames []string
			for i := 0; i < 100; i++ {
				frame := panel.View(width, height)
				lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
				if len(lines) > height {
					t.Fatalf("%s: frame is %d rows, budget is %d:\n%s",
						tc.name, len(lines), height, StripANSI(frame))
				}
				plain := StripANSI(frame)
				frames = append(frames, plain)
				if strings.Contains(plain, "RULE-24") {
					break
				}
				panel.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
			}

			joined := strings.Join(frames, "\n")
			if !strings.Contains(joined, "RULE-24") {
				t.Fatalf("%s: paging never reached the last rule:\n%s", tc.name, joined)
			}
			if !strings.Contains(joined, "allow the shell") {
				t.Errorf("%s: rules lost their allow label once colour is gone", tc.name)
			}
			if !strings.Contains(joined, "esc back") {
				t.Errorf("%s: the detail view never says how to go back:\n%s", tc.name, joined)
			}
			foundPos := false
			for _, f := range frames {
				for _, row := range frameContentRows(f) {
					if detailPosRow.MatchString(strings.TrimSpace(row)) {
						foundPos = true
					}
				}
			}
			if !foundPos {
				t.Errorf("%s: no readable position row without colour:\n%s", tc.name, joined)
			}
		})
	}
}
