package theme

import (
	"fmt"
	"image/color"
	"strconv"

	"charm.land/lipgloss/v2"
)

// Dracula 256-color palette based on the canonical Dracula theme.
var dracula256 = Theme{
	FGDefault:       lipgloss.Color("252"),
	FGMuted:         lipgloss.Color("244"),
	BorderMuted:     lipgloss.Color("245"),
	FGEmphasis:      lipgloss.Color("255"),
	BGBase:          lipgloss.Color("235"),
	BGSurface:       lipgloss.Color("236"),
	BGSelection:     lipgloss.Color("237"),
	AccentPrimary:   lipgloss.Color("212"),
	AccentSecondary: lipgloss.Color("141"),
	AccentTertiary:  lipgloss.Color("228"),
	UserPrompt:      lipgloss.Color("246"),
	StatusError:     lipgloss.Color("210"),
	StatusWarning:   lipgloss.Color("215"),
	StatusSuccess:   lipgloss.Color("84"),
	StatusInfo:      lipgloss.Color("117"),
}

// Nord 256-color palette based on the canonical Nord theme.
var nord256 = Theme{
	FGDefault:       lipgloss.Color("253"),
	FGMuted:         lipgloss.Color("240"),
	BorderMuted:     lipgloss.Color("240"),
	FGEmphasis:      lipgloss.Color("255"),
	BGBase:          lipgloss.Color("235"),
	BGSurface:       lipgloss.Color("237"),
	BGSelection:     lipgloss.Color("239"),
	AccentPrimary:   lipgloss.Color("110"),
	AccentSecondary: lipgloss.Color("67"),
	AccentTertiary:  lipgloss.Color("222"),
	UserPrompt:      lipgloss.Color("109"),
	StatusError:     lipgloss.Color("167"),
	StatusWarning:   lipgloss.Color("215"),
	StatusSuccess:   lipgloss.Color("143"),
	StatusInfo:      lipgloss.Color("139"),
}

// Catppuccin Mocha 256-color palette based on the canonical Catppuccin Mocha theme.
var catppuccinMocha256 = Theme{
	FGDefault:       lipgloss.Color("252"),
	FGMuted:         lipgloss.Color("242"),
	BorderMuted:     lipgloss.Color("240"),
	FGEmphasis:      lipgloss.Color("255"),
	BGBase:          lipgloss.Color("234"),
	BGSurface:       lipgloss.Color("236"),
	BGSelection:     lipgloss.Color("238"),
	AccentPrimary:   lipgloss.Color("147"),
	AccentSecondary: lipgloss.Color("110"),
	AccentTertiary:  lipgloss.Color("222"),
	UserPrompt:      lipgloss.Color("146"),
	StatusError:     lipgloss.Color("210"),
	StatusWarning:   lipgloss.Color("215"),
	StatusSuccess:   lipgloss.Color("150"),
	StatusInfo:      lipgloss.Color("115"),
}

var presets = map[string]Theme{
	"dracula":          dracula256,
	"nord":             nord256,
	"catppuccin-mocha": catppuccinMocha256,
	"Catppuccin Mocha": catppuccinMocha256,
}

// PaletteOverrides maps lower-snake-case slot names to color values
// (hex strings like "#FF0000" or 256-color indices like "123").
type PaletteOverrides map[string]string

// Apply returns a copy of base with matching overrides applied.
// Non-overridden slots keep the preset value.
func (o PaletteOverrides) Apply(base Theme) Theme {
	out := base
	slot := map[string]*color.Color{
		"fg_default":       &out.FGDefault,
		"fg_muted":         &out.FGMuted,
		"border_muted":     &out.BorderMuted,
		"fg_emphasis":      &out.FGEmphasis,
		"bg_base":          &out.BGBase,
		"bg_surface":       &out.BGSurface,
		"bg_selection":     &out.BGSelection,
		"accent_primary":   &out.AccentPrimary,
		"accent_secondary": &out.AccentSecondary,
		"accent_tertiary":  &out.AccentTertiary,
		"user_prompt":      &out.UserPrompt,
		"status_error":     &out.StatusError,
		"status_warning":   &out.StatusWarning,
		"status_success":   &out.StatusSuccess,
		"status_info":      &out.StatusInfo,
	}
	for key, val := range o {
		ptr, ok := slot[key]
		if !ok {
			continue
		}
		parsed, err := parseHex(val)
		if err != nil {
			continue
		}
		*ptr = parsed
	}
	return out
}

// LookupPreset returns the named preset theme and true, or a zero Theme and
// false if the name is unknown. Names are case-sensitive.
func LookupPreset(name string) (Theme, bool) {
	th, ok := presets[name]
	return th, ok
}

// parseHex accepts #RRGGBB, RRGGBB, #RGB (expanded to RRGGBB), and 256-color
// digit strings ("0"–"255"). It returns a color.Color or an error.
func parseHex(s string) (color.Color, error) {
	if s == "" {
		return nil, fmt.Errorf("empty string")
	}

	if s[0] == '#' {
		switch len(s) {
		case 4: // #RGB
			r, g, b := s[1], s[2], s[3]
			if !isHex(r) || !isHex(g) || !isHex(b) {
				return nil, fmt.Errorf("invalid hex digit in %q", s)
			}
			return lipgloss.Color("#" + string(r) + string(r) + string(g) + string(g) + string(b) + string(b)), nil
		case 7: // #RRGGBB
			for i := 1; i < 7; i++ {
				if !isHex(s[i]) {
					return nil, fmt.Errorf("invalid hex digit in %q", s)
				}
			}
			return lipgloss.Color(s), nil
		default:
			return nil, fmt.Errorf("invalid hex length: %q", s)
		}
	}

	// No '#' prefix.
	if len(s) == 6 {
		for i := 0; i < 6; i++ {
			if !isHex(s[i]) {
				return nil, fmt.Errorf("invalid hex digit in %q", s)
			}
		}
		return lipgloss.Color("#" + s), nil
	}

	// Try decimal 0–255.
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, fmt.Errorf("not a valid hex or decimal color: %q", s)
	}
	if n < 0 || n > 255 {
		return nil, fmt.Errorf("decimal color out of range (0–255): %d", n)
	}
	return lipgloss.Color(s), nil
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}
