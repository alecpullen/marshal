package tui

import (
	"testing"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
)

// TestThemeStylesResolveColors guards the nil-color capture bug: these used
// to be package-level vars initialized before loadTheme ran, permanently
// capturing nil colors. They must be built lazily after loadTheme.
func TestThemeStylesResolveColors(t *testing.T) {
	loadTheme(config.TUIConfig{})
	styles := map[string]lipgloss.Style{
		"warningStyle":      warningStyle(),
		"errorStyle":        errorStyle(),
		"statusOkStyle":     statusOkStyle(),
		"statusBusyStyle":   statusBusyStyle(),
		"promptPrefixStyle": promptPrefixStyle(),
		"toolBulletStyle":   toolBulletStyle(),
	}
	for name, st := range styles {
		if st.GetForeground() == nil {
			t.Errorf("%s foreground is nil — style captured theme colors before loadTheme", name)
		}
	}
}
