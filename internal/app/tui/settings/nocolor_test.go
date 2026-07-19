package settings

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/theme"
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

// With $NO_COLOR set, BrowserPanel.View must not emit ANSI escape sequences.
func TestViewNoANSIEscapesUnderNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	prev := theme.Current()
	theme.Reload(theme.Load())
	t.Cleanup(func() { theme.Reload(prev) })

	browser := NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), "")
	v := browser.View(80, 12)
	if strings.Contains(v, "\x1b[") {
		t.Fatalf("BrowserPanel.View emitted ANSI escapes under NO_COLOR:\n%q", v)
	}
}
