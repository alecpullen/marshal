package settings

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
)

// The design system requires the UI to stay usable with color stripped.
// This test asserts structure survives ansi-stripping rather than
// re-resolving the theme: the cursor marker, borders, and toggle glyphs must
// all be plain text.
func TestViewUsableWhenColorStripped(t *testing.T) {
	browser := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "shell")
	browser.list.SetCursor(2) // shell.allow_network
	browser.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	v := browser.View(80, 12)
	plain := ansi.Strip(v)
	for _, want := range []string{"▸", "╭", "on", "off"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("structure marker %q must survive color stripping:\n%s", want, plain)
		}
	}
}
