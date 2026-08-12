package chrome

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
)

// TestSelectionStyleAlwaysPairsForeground pins F6: three panels set a
// selection background with no foreground, leaving whatever colour the row
// already had on the band — as low as 1.53:1.
func TestSelectionStyleAlwaysPairsForeground(t *testing.T) {
	theme.Reload(theme.LoadFor(false, "xterm-256color"))
	st := SelectionStyle()
	// lipgloss returns NoColor{} — not nil — for an unset colour, so compare
	// against that. A nil check here would silently always pass.
	if st.GetForeground() == (lipgloss.NoColor{}) {
		t.Error("SelectionStyle sets no foreground")
	}
	if st.GetBackground() == (lipgloss.NoColor{}) {
		t.Error("SelectionStyle sets no background")
	}
	if st.GetForeground() != theme.Current().FGEmphasis {
		t.Errorf("SelectionStyle foreground = %v, want FGEmphasis %v",
			st.GetForeground(), theme.Current().FGEmphasis)
	}
	if st.GetBackground() != theme.Current().BGSelection {
		t.Errorf("SelectionStyle background = %v, want BGSelection %v",
			st.GetBackground(), theme.Current().BGSelection)
	}
}

// TestSelectionMarkersAreEqualWidth pins column alignment: the selected and
// unselected markers must occupy the same number of cells or every row below
// a moving cursor shifts sideways.
func TestSelectionMarkersAreEqualWidth(t *testing.T) {
	if lipgloss.Width(SelectionMarker) != lipgloss.Width(BlankMarker) {
		t.Errorf("SelectionMarker is %d cells, BlankMarker is %d",
			lipgloss.Width(SelectionMarker), lipgloss.Width(BlankMarker))
	}
}

// TestSelectionSurvivesMonochrome pins F11: under NO_COLOR the band carries
// no colour, so the marker is the only thing left identifying the cursor.
func TestSelectionSurvivesMonochrome(t *testing.T) {
	theme.Reload(theme.LoadFor(true, "xterm-256color"))
	defer theme.Reload(theme.LoadFor(false, "xterm-256color"))

	row := SelectionMarker + "some row"
	if !strings.HasPrefix(row, SelectionMarker) {
		t.Fatal("marker missing")
	}
	if strings.TrimSpace(SelectionMarker) == "" {
		t.Error("SelectionMarker is blank, so a selected row is invisible without colour")
	}
}
