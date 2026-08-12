package chrome

import (
	"testing"

	"marshal/internal/app/tui/theme"
)

// TestTertiaryStyleIsDimNotDarker pins F10: the codebase had 33 Bold sites
// and exactly one Faint, so tertiary text was de-emphasised by choosing a
// darker colour. That is what pushed FGMuted down the grey ramp until it
// failed AA. Dim is the correct tool and degrades better across terminals.
func TestTertiaryStyleIsDimNotDarker(t *testing.T) {
	theme.Reload(theme.LoadFor(false, "xterm-256color"))
	st := TertiaryStyle()
	if !st.GetFaint() {
		t.Error("TertiaryStyle is not faint, so it must be encoding emphasis as colour")
	}
	if st.GetForeground() != theme.Current().FGMuted {
		t.Error("TertiaryStyle must build on FGMuted, not introduce a third grey")
	}
}

// TestTertiaryStyleStaysReadableInMonochrome guards the NO_COLOR path: faint
// is an SGR attribute, not a colour, so it survives when colour does not.
func TestTertiaryStyleStaysReadableInMonochrome(t *testing.T) {
	theme.Reload(theme.LoadFor(true, "xterm-256color"))
	defer theme.Reload(theme.LoadFor(false, "xterm-256color"))
	if !TertiaryStyle().GetFaint() {
		t.Error("TertiaryStyle lost its dim attribute in monochrome mode")
	}
}
