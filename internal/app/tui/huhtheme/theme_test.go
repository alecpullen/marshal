package huhtheme_test

import (
	"testing"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/huhtheme"
)

func TestWarmSunsetReadsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	th := huhtheme.WarmSunset()
	styles := th(true)

	// With NO_COLOR, theme.Load() returns monochromeTheme where every slot
	// is lipgloss.NoColor{}. WarmSunset uses FGDefault for normal text, so
	// the Title foreground should be NoColor{}.
	fg := styles.Focused.Title.GetForeground()
	if fg == nil {
		t.Fatal("expected non-nil foreground (NoColor{}), got nil")
	}
	if _, ok := fg.(lipgloss.NoColor); !ok {
		t.Errorf("expected NoColor foreground when NO_COLOR is set, got %T", fg)
	}
}
