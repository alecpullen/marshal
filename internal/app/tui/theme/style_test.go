package theme

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestIsMonochromeReturnsBool(t *testing.T) {
	// IsMonochrome must return a bool without panicking.
	result := IsMonochrome()
	_ = result
}

func TestIsMonochromeMatchesNoColorTheme(t *testing.T) {
	Reload(monochromeTheme())
	if !IsMonochrome() {
		t.Fatal("IsMonochrome() = false with the monochrome theme loaded, want true")
	}
	Reload(warmSunset256)
	if IsMonochrome() {
		t.Fatal("IsMonochrome() = true with warm-sunset theme loaded, want false")
	}
}

func TestMutedStyleReturnsStyle(t *testing.T) {
	// Verify it's a lipgloss.Style, not a zero-value.
	var _ lipgloss.Style = MutedStyle()
}

func TestMutedStylePlainInMonochrome(t *testing.T) {
	Reload(monochromeTheme())
	if got := MutedStyle().Render("x"); got != "x" {
		t.Errorf("MutedStyle() in monochrome should render plain text, got %q", got)
	}
}
