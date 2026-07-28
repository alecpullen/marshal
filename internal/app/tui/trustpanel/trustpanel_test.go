package trustpanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/trust"
)

func TestNumberKeysChooseDecision(t *testing.T) {
	cases := map[string]trust.Decision{
		"1": trust.DecisionTrustPermanent,
		"2": trust.DecisionTrustSession,
		"3": trust.DecisionDontTrust,
	}
	for key, want := range cases {
		var got trust.Decision
		p := New("/repo", func(d trust.Decision) { got = d })
		p.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		if got != want {
			t.Errorf("key %s: decision = %v, want %v", key, got, want)
		}
	}
}

func TestEscDeclines(t *testing.T) {
	var got trust.Decision
	p := New("/repo", func(d trust.Decision) { got = d })
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got != trust.DecisionDontTrust {
		t.Fatalf("esc decision = %v, want dont_trust", got)
	}
	if cmd == nil {
		t.Fatal("esc should emit ClosedMsg")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("esc produced %T, want ClosedMsg", cmd())
	}
}

func TestTrustChoiceQuitsForReload(t *testing.T) {
	p := New("/repo", func(trust.Decision) {})
	cmd := p.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if cmd == nil {
		t.Fatal("trust choice should quit the program for reload")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("trust choice produced %T, want tea.QuitMsg", cmd())
	}
}

func TestViewExplainsRisk(t *testing.T) {
	p := New("/repo", func(trust.Decision) {})
	out := p.View(80, 10)
	for _, want := range []string{"config.toml", "Trust", "providers"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}
