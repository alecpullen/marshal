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
	"sort"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
)

var (
	current   Theme
	mu        sync.RWMutex
	listeners []func(Theme)
)

// Current returns the currently active theme. It is safe to call from any
// goroutine. The zero value is returned before the first call to Reload.
func Current() Theme {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Subscribe registers fn to be called (in the same goroutine as Reload)
// every time the theme is reloaded. The callback receives the new Theme.
func Subscribe(fn func(Theme)) {
	mu.Lock()
	defer mu.Unlock()
	listeners = append(listeners, fn)
}

// Reload sets the current theme and notifies all subscribers. It is safe
// to call from any goroutine.
//
// We copy the listener slice under the lock and call each listener
// outside the lock to avoid holding the mutex during potentially slow
// listener callbacks. This is safe because the copy is a snapshot —
// concurrent additions/removals won't affect this iteration.
func Reload(th Theme) {
	mu.Lock()
	current = th
	fns := make([]func(Theme), len(listeners))
	copy(fns, listeners)
	mu.Unlock()
	for _, fn := range fns {
		fn(th)
	}
}

// Theme is the set of semantic color slots consumed by every renderer.
// Each slot maps a meaning (primary accent, muted text, error) to a
// color.Color, which is set to NoColor{} (no SGR emitted) in monochrome
// mode when $NO_COLOR is set.
type Theme struct {
	FGDefault   color.Color
	FGMuted     color.Color
	BorderMuted color.Color
	FGEmphasis  color.Color
	// BGBase is a reference value, not a render target: marshal deliberately
	// never paints it, so the terminal's own background (and any
	// transparency or background image) shows through. BGSurface and
	// BGSelection are derived relative to it, and contrast_test.go validates
	// foregrounds against a range of plausible terminal backgrounds rather
	// than against this value. Zero call sites outside this package is
	// correct, not dead code.
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
	FGMuted:         lipgloss.Color("248"),
	BorderMuted:     lipgloss.Color("246"),
	FGEmphasis:      lipgloss.Color("255"),
	BGBase:          lipgloss.Color("235"),
	BGSurface:       lipgloss.Color("236"),
	BGSelection:     lipgloss.Color("238"),
	AccentPrimary:   lipgloss.Color("209"),
	AccentSecondary: lipgloss.Color("177"),
	AccentTertiary:  lipgloss.Color("183"),
	UserPrompt:      lipgloss.Color("250"),
	StatusError:     lipgloss.Color("211"),
	StatusWarning:   lipgloss.Color("214"),
	StatusSuccess:   lipgloss.Color("43"),
	StatusInfo:      lipgloss.Color("81"),
}

// warmSunset16 maps the Warm Sunset palette onto the 16-ANSI relative set
// so the terminal theme controls the actual appearance.
//
// The four neutrals (0 < 8 < 7 < 15 by conventional lightness) are spent
// deliberately: backgrounds take 0 and 8, foregrounds take 7 and 15. Any
// other arrangement collapses a foreground onto a background, which does not
// dim that text — it deletes it.
var warmSunset16 = Theme{
	FGDefault:       lipgloss.Color("15"),
	FGMuted:         lipgloss.Color("7"),
	BorderMuted:     lipgloss.Color("7"),
	FGEmphasis:      lipgloss.Color("15"),
	BGBase:          lipgloss.Color("0"),
	BGSurface:       lipgloss.Color("8"),
	BGSelection:     lipgloss.Color("4"),
	AccentPrimary:   lipgloss.Color("5"),
	AccentSecondary: lipgloss.Color("6"),
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
	if supports256(term) {
		return warmSunset256
	}
	return warmSunset16
}

// supports256 reports whether TERM advertises a 256-color palette.
func supports256(term string) bool {
	return strings.Contains(term, "256color") ||
		strings.Contains(term, "kitty") ||
		strings.Contains(term, "wezterm")
}

// Mode constants for LoadWithConfig.
const (
	ModeDark  = "dark"
	ModeLight = "light"
)

// Load reads the environment and returns the active Theme.
func Load() Theme {
	return LoadWithConfig("warm-sunset", ModeDark, nil)
}

// LoadWithConfig returns a Theme selected by name and mode (light/dark) with
// optional overrides. If NO_COLOR is set the monochrome theme is returned
// unconditionally. An unknown name falls back to warm-sunset. When the
// terminal does not indicate 256-color support the 16-color variant is used.
// Mode comparison is case-insensitive; empty mode defaults to ModeDark.
func LoadWithConfig(name string, mode string, overrides PaletteOverrides) Theme {
	if os.Getenv("NO_COLOR") != "" {
		return monochromeTheme()
	}

	if mode == "" {
		mode = ModeDark
	}
	mode = strings.ToLower(mode)

	lookupName := name
	if mode == ModeLight {
		lookupName = name + "-light"
	}

	base, ok := LookupPreset(lookupName)
	if !ok {
		base, lookupName = warmSunset256, "warm-sunset"
	}

	if !supports256(os.Getenv("TERM")) {
		base = sixteenFor(lookupName, base)
	}

	if overrides != nil {
		base = overrides.Apply(base)
	}

	return base
}

// presets16 holds hand-tuned 16-color tables. A preset with an entry here
// uses it verbatim; every other preset is derived by downsampling. Hand-tuning
// wins because a person can weigh which distinctions matter most in a palette
// this small, which nearest-color matching cannot.
var presets16 = map[string]Theme{
	"warm-sunset": warmSunset16,
}

// sixteenFor returns the 16-color rendering of a preset: the hand-tuned table
// when one exists, otherwise a downsample of the 256-color original.
//
// Previously every preset fell back to warmSunset16 regardless of name, so
// choosing any other theme on a non-256-color terminal silently did nothing.
//
// separatePairs runs over hand-tuned tables as well as downsampled ones. A
// hand-tuned table is written by a person and can collide just as easily; on
// an already-correct table the pass makes no changes.
func sixteenFor(name string, base Theme) Theme {
	if t, ok := presets16[name]; ok {
		separatePairs(&t)
		return t
	}
	return downsampleTo16(base)
}

// Names returns the list of preset names sorted lexicographically for a
// stable settings-enum ordering.
func Names() []string {
	nn := make([]string, 0, len(presets))
	for k := range presets {
		nn = append(nn, k)
	}
	sort.Strings(nn)
	return nn
}
