package trustpanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
)

func TestNumberKeysChooseDecision(t *testing.T) {
	cases := map[string]acpwire.TrustDecision{
		"1": acpwire.TrustPermanent,
		"2": acpwire.TrustSession,
		"3": acpwire.TrustDecline,
	}
	for key, want := range cases {
		var got acpwire.TrustDecision
		p := New("/repo", func(d acpwire.TrustDecision) { got = d })
		p.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		if got != want {
			t.Errorf("key %s: decision = %v, want %v", key, got, want)
		}
	}
}

func TestEscDeclines(t *testing.T) {
	var got acpwire.TrustDecision
	p := New("/repo", func(d acpwire.TrustDecision) { got = d })
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got != acpwire.TrustDecline {
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
	p := New("/repo", func(acpwire.TrustDecision) {})
	cmd := p.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if cmd == nil {
		t.Fatal("trust choice should quit the program for reload")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("trust choice produced %T, want tea.QuitMsg", cmd())
	}
}

func TestViewExplainsRisk(t *testing.T) {
	p := New("/repo", func(acpwire.TrustDecision) {})
	out := p.View(80, 10)
	for _, want := range []string{"config.toml", "Trust", "model selection", "/repo"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}
