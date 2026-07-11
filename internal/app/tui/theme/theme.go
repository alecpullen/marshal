// Package theme defines marshal's semantic color slots and resolves them to
// the terminal's color tier (256-color, 16-ANSI, or monochrome when
// $NO_COLOR is set). Every TUI renderer references these slots rather than
// raw color codes, so a single Load() call at startup retunes the whole
// interface. See the TUI design system: "Never hardcode hex values in
// widget code. Always reference semantic slots."
package theme

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// Theme is the set of semantic color slots consumed by every renderer.
// Each slot maps a meaning (primary accent, muted text, error) to a
// color.Color, which is set to NoColor{} (no SGR emitted) in monochrome
// mode when $NO_COLOR is set.
type Theme struct {
	FGDefault       color.Color
	FGMuted         color.Color
	BorderMuted     color.Color
	FGEmphasis      color.Color
	BGBase          color.Color
	BGSurface       color.Color
	BGSelection     color.Color
	AccentPrimary   color.Color
	AccentSecondary color.Color
	AccentTertiary  color.Color // amber/gold (214/3) — tool-call names, tertiary accents
	UserPrompt      color.Color // medium grey (246/7) — user message prefix
	StatusError     color.Color
	StatusWarning   color.Color
	StatusSuccess   color.Color
	StatusInfo      color.Color
}

// warmSunset256 is the default dark-theme palette (Warm Sunset) resolved
// to 256-color ANSI codes.
var warmSunset256 = Theme{
	FGDefault:       lipgloss.Color("252"),
	FGMuted:         lipgloss.Color("244"),
	BorderMuted:     lipgloss.Color("245"),
	FGEmphasis:      lipgloss.Color("255"),
	BGBase:          lipgloss.Color("235"),
	BGSurface:       lipgloss.Color("237"),
	BGSelection:     lipgloss.Color("60"),
	AccentPrimary:   lipgloss.Color("209"),
	AccentSecondary: lipgloss.Color("175"),
	AccentTertiary:  lipgloss.Color("214"),
	UserPrompt:      lipgloss.Color("246"),
	StatusError:     lipgloss.Color("203"),
	StatusWarning:   lipgloss.Color("172"),
	StatusSuccess:   lipgloss.Color("43"),
	StatusInfo:      lipgloss.Color("43"),
}

// warmSunset16 maps the Warm Sunset palette onto the 16-ANSI relative set
// so the terminal theme controls the actual appearance.
var warmSunset16 = Theme{
	FGDefault:       lipgloss.Color("7"),
	FGMuted:         lipgloss.Color("8"),
	BorderMuted:     lipgloss.Color("8"),
	FGEmphasis:      lipgloss.Color("15"),
	BGBase:          lipgloss.Color("0"),
	BGSurface:       lipgloss.Color("8"),
	BGSelection:     lipgloss.Color("4"),
	AccentPrimary:   lipgloss.Color("5"),
	AccentSecondary: lipgloss.Color("5"),
	AccentTertiary:  lipgloss.Color("3"),
	UserPrompt:      lipgloss.Color("7"),
	StatusError:     lipgloss.Color("1"),
	StatusWarning:   lipgloss.Color("3"),
	StatusSuccess:   lipgloss.Color("2"),
	StatusInfo:      lipgloss.Color("6"),
}

// monochromeTheme returns a Theme where every slot is lipgloss.NoColor{},
// so lipgloss emits no color SGR sequences — the interface stays usable
// through layout and symbols, exactly as the design system requires ("If
// you removed all color, the interface should still be usable").
func monochromeTheme() Theme {
	return Theme{
		FGDefault:       lipgloss.NoColor{},
		FGMuted:         lipgloss.NoColor{},
		BorderMuted:     lipgloss.NoColor{},
		FGEmphasis:      lipgloss.NoColor{},
		BGBase:          lipgloss.NoColor{},
		BGSurface:       lipgloss.NoColor{},
		BGSelection:     lipgloss.NoColor{},
		AccentPrimary:   lipgloss.NoColor{},
		AccentSecondary: lipgloss.NoColor{},
		AccentTertiary:  lipgloss.NoColor{},
		UserPrompt:      lipgloss.NoColor{},
		StatusError:     lipgloss.NoColor{},
		StatusWarning:   lipgloss.NoColor{},
		StatusSuccess:   lipgloss.NoColor{},
		StatusInfo:      lipgloss.NoColor{},
	}
}

// LoadFor resolves the color tier from explicit flags. noColor true
// forces monochrome; term drives 256 vs 16 fallback. Tests use this
// directly; production code calls Load().
func LoadFor(noColor bool, term string) Theme {
	if noColor {
		return monochromeTheme()
	}
	if strings.Contains(term, "256color") {
		return warmSunset256
	}
	return warmSunset16
}

// Load reads the environment and returns the active Theme.
func Load() Theme {
	return LoadFor(os.Getenv("NO_COLOR") != "", os.Getenv("TERM"))
}
