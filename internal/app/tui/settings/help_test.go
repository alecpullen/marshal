package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestHelpOverlayOpensAndCloses(t *testing.T) {
	m := New(config.Default(), t.TempDir(), t.TempDir()+"/config.toml")
	m.SetSize(100, 32)
	m, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	v := m.View()
	for _, want := range []string{"Settings keys", "search", "toggle", "save"} {
		if !strings.Contains(v, want) {
			t.Fatalf("help overlay missing %q:\n%s", want, v)
		}
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.overlay != overlayNone {
		t.Fatal("esc should close help")
	}
}
