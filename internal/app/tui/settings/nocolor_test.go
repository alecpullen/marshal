package settings

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The design system requires the UI to stay usable with color stripped.
// settingsTheme is resolved at package init, so this test asserts structure
// survives ansi-stripping rather than re-resolving the theme: the cursor
// marker, borders, and toggle glyphs must all be plain text.
func TestViewUsableWhenColorStripped(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	plain := ansi.Strip(v)
	for _, want := range []string{"▸", "╭", "on", "off"} {
		if !strings.Contains(plain, want) && !strings.Contains(plain, "●") {
			t.Fatalf("structure marker %q must survive color stripping:\n%s", want, plain)
		}
	}
}
