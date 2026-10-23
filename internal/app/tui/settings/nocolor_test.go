package settings

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/theme"
)

// The design system requires the UI to stay usable with color stripped.
// This test asserts structure survives ansi-stripping rather than
// re-resolving the theme: the cursor marker, borders, and toggle glyphs must
// all be plain text.
func TestViewUsableWhenColorStripped(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.AllowNetwork = false
	browser := NewBrowser(cfg, filepath.Join(t.TempDir(), "config.toml"), "shell")
	v := browser.View(80, 12)
	plain := ansi.Strip(v)
	// The cursor/border structure and the plain-text toggle glyph must both
	// survive stripping; the seeded row renders "off".
	for _, want := range []string{"▸", "▍", "off"} {
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
	// The selection style deliberately emits a bold SGR escape under NO_COLOR
	// (marker + bold weight carry the cursor). Tolerate that one sequence but
	// flag any other escape.
	v = strings.ReplaceAll(strings.ReplaceAll(v, "\x1b[1m", ""), "\x1b[m", "")
	if strings.Contains(v, "\x1b[") {
		t.Fatalf("BrowserPanel.View emitted ANSI escapes under NO_COLOR (bold selection allowed):\n%q", v)
	}
}
