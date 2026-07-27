package sidepanel

import (
	"strings"
	"testing"
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
