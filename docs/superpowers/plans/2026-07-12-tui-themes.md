# TUI Theme TOML Customization and Predefined Themes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close `docs/11-roadmap-and-future-enhancements.md` Feature #2. Let users override the active TUI palette via a `[tui]` TOML section, and ship three named themes (Dracula, Nord, Catppuccin Mocha) alongside the existing Warm Sunset default. Every renderer continues to reference `internal/app/tui/theme` semantic slots — the override applies to the slot values, not to the slot names.

**Architecture:** Two additions to `internal/app/tui/theme`:
1. A new `LoadFromConfig(name string, overrides PaletteOverrides) Theme` factory that picks a named preset and applies TOML-level overrides. The existing `Load()` becomes `LoadFromConfig("warm-sunset", nil)`.
2. A new `PaletteOverrides` struct with optional fields matching every slot in `Theme`. Nil fields mean "use the preset value". `theme.Load` consults `os.Getenv("MARSHAL_THEME")` first, then config, then defaults to "warm-sunset".

Three predefined themes live as `var dracula256 = Theme{...}` etc. alongside the existing `warmSunset256`. The 16-ANSI tier of each is the same set of relative colors used today; only the 256-color tier is per-theme.

`internal/app/config` gains a `TUIConfig` block with `Theme string` and `Palette map[string]string` (slot name → hex/256). The settings TUI gets a `kindEnum` row "Theme" with options `["warm-sunset", "dracula", "nord", "catppuccin-mocha"]`.

**Tech Stack:** Go 1.26.1, `charm.land/lipgloss/v2` (already in use), `image/color` for parsing hex strings. No new external dependencies.

**Assumes Milestone R settings redesign is complete** (it is: `internal/app/tui/settings/`, `theme/`, `model.go` are all in place).

## Global Constraints

- **No comments unless asked.** Match existing gofmt style.
- **Colors ONLY via `theme` slots.** Renderers must not import raw `lipgloss.Color("209")` literals after this batch — Task 6 audits and replaces any that remain.
- **Backward compatible default:** when the `[tui]` block is absent, the current `warmSunset256` + `warmSunset16` pair is the active theme. The current `theme.Load()` signature stays; new code path is `theme.LoadWithConfig(cfg)`.
- **Hex parsing** must accept the common forms: `#RRGGBB`, `RRGGBB`, `#RGB` (expand), and 256-color shorthand `0–255`. An unparseable entry is a config error surfaced at startup, not a panic.
- **`NO_COLOR` always wins.** Even with a TOML override, `NO_COLOR=1` forces the monochrome theme.
- **Light/dark auto-detect** is out of scope. The four themes are all dark. A future batch can add light variants.
- **Verification:** `go test ./internal/app/tui/theme/ ./internal/app/config/ ./internal/app/tui/settings/` after every task; full gates before batch closeout.

## File Structure

**Create:**
- `internal/app/tui/theme/presets.go` — `dracula256`, `nord256`, `catppuccinMocha256` `Theme` values; `PaletteOverrides` struct; `parseHex(s string) (color.Color, error)`.
- `internal/app/tui/theme/presets_test.go` — preset name lookup, override merging, hex parsing, NO_COLOR precedence.

**Modify:**
- `internal/app/tui/theme/theme.go` — add `LoadWithConfig(name string, overrides PaletteOverrides) Theme`; keep `Load()` as `LoadWithConfig("warm-sunset", nil)`; add `Names() []string` for the settings enum.
- `internal/app/config/config.go` — add `TUIConfig` block (`Theme string`, `Palette map[string]string`) and wire it into the top-level `Config` struct.
- `internal/app/config/save.go` — mirror the new field for round-tripping.
- `internal/app/tui/settings/frames_basic.go` — add a "Theme" `kindEnum` row on the appropriate section's root (the `interface` section, if it exists, or a new top-level "Interface" section; see Task 4).
- `internal/app/tui/model.go` — call `theme.LoadWithConfig(...)` instead of `theme.Load()` at startup, threading the active `TUIConfig`.
- `internal/app/tui/help/help.go` — add "theme" to the key list if the help overlay mentions theme.
- `docs/09-configuration-examples.md` — add a `[tui]` example block.
- `docs/11-roadmap-and-future-enhancements.md` — tick Feature #2 (move from "open" to a short "shipped" note with the commit range).
- `docs/13-project-audit-2026-07-11.md` — append a "Implementation batch — TUI themes" section (mirror the prior batch sections).

---

## Task 1: Preset palettes and `PaletteOverrides`

**Files:**
- Create: `internal/app/tui/theme/presets.go`
- Test: `internal/app/tui/theme/presets_test.go`

**Interfaces:**
- Produces: `var dracula256, nord256, catppuccinMocha256 Theme`; `type PaletteOverrides map[string]string`; `func LookupPreset(name string) (Theme, bool)`; `func (o PaletteOverrides) Apply(base Theme) Theme`; `func parseHex(s string) (color.Color, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/app/tui/theme/presets_test.go`:

```go
package theme

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLookupPresetKnownNames(t *testing.T) {
	for _, name := range []string{"warm-sunset", "dracula", "nord", "catppuccin-mocha"} {
		th, ok := LookupPreset(name)
		if !ok {
			t.Fatalf("LookupPreset(%q) not found", name)
		}
		if th.AccentPrimary == (color.Color(nil)) {
			t.Fatalf("LookupPreset(%q) has empty AccentPrimary", name)
		}
	}
}

func TestLookupPresetUnknownReturnsFalse(t *testing.T) {
	if _, ok := LookupPreset("not-a-real-theme"); ok {
		t.Fatal("LookupPreset(unknown) should return false")
	}
}

func TestOverridesApplySlot(t *testing.T) {
	base := warmSunset256
	override := PaletteOverrides{"accent_primary": "#ff00ff"}
	got := override.Apply(base)
	if got.AccentPrimary != lipgloss.Color("#ff00ff") {
		t.Fatalf("AccentPrimary = %v, want #ff00ff", got.AccentPrimary)
	}
	if got.FGDefault != base.FGDefault {
		t.Fatalf("FGDefault changed by override that only touched accent_primary: %v", got.FGDefault)
	}
}

func TestParseHexForms(t *testing.T) {
	cases := []struct {
		in   string
		want color.RGBA
	}{
		{"#ff8800", color.RGBA{R: 0xff, G: 0x88, B: 0x00, A: 0xff}},
		{"ff8800", color.RGBA{R: 0xff, G: 0x88, B: 0x00, A: 0xff}},
		{"#f80", color.RGBA{R: 0xff, G: 0x88, B: 0x00, A: 0xff}},
		{"42", lipgloss.Color("42")},
	}
	for _, c := range cases {
		got, err := parseHex(c.in)
		if err != nil {
			t.Errorf("parseHex(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseHex(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseHexInvalidErrors(t *testing.T) {
	if _, err := parseHex("not-a-color"); err == nil {
		t.Fatal("expected error for unparseable input")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/app/tui/theme/ -run 'TestLookupPreset|TestOverrides|TestParseHex' -v`
Expected: FAIL — `undefined: LookupPreset`, `PaletteOverrides`, `parseHex`.

- [ ] **Step 3: Implement presets and overrides**

Create `internal/app/tui/theme/presets.go`. The exact RGB values for the three new presets are non-load-bearing — they must be *internally consistent* (each theme is its own palette) and round-trip through `parseHex`. Use canonical community values:

- **Dracula**: BG `#282a36`, FG `#f8f8f2`, muted `#6272a4`, accent `#bd93f9`, success `#50fa7b`, error `#ff5555`, warning `#f1fa8c`, info `#8be9fd`.
- **Nord**: BG `#2e3440`, FG `#eceff4`, muted `#4c566a`, accent `#88c0d0`, success `#a3be8c`, error `#bf616a`, warning `#ebcb8b`, info `#5e81ac`.
- **Catppuccin Mocha**: BG `#1e1e2e`, FG `#cdd6f4`, muted `#6c7086`, accent `#cba6f7`, success `#a6e3a1`, error `#f38ba8`, warning `#f9e2af`, info `#89b4fa`.

Implement `parseHex` to accept `#RRGGBB`, `RRGGBB`, `#RGB` (expand), and 256-color digits; reject anything else with an error. Implement `Apply(base)` to iterate the map and set matching slots on a copy of `base`. The slot-name map is fixed: `accent_primary` → `AccentPrimary`, `fg_default` → `FGDefault`, etc.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/app/tui/theme/ -v`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/app/tui/theme/ && gofmt -w internal/app/tui/theme/presets.go internal/app/tui/theme/presets_test.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/theme/presets.go internal/app/tui/theme/presets_test.go
git commit -m "feat(theme): add Dracula, Nord, Catppuccin Mocha presets and palette overrides"
```

---

## Task 2: `LoadWithConfig` and `Names`

**Files:**
- Modify: `internal/app/tui/theme/theme.go`
- Test: `internal/app/tui/theme/theme_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/theme/theme_test.go`:

```go
func TestLoadWithConfigUnknownFallsBackToDefault(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	th := LoadWithConfig("not-a-theme", nil)
	if th.AccentPrimary != warmSunset256.AccentPrimary {
		t.Fatalf("unknown theme should fall back to warm-sunset")
	}
}

func TestLoadWithConfigNoColorWins(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	th := LoadWithConfig("dracula", nil)
	// Every slot is NoColor{} in monochrome; AccentPrimary must be empty.
	if th.AccentPrimary != (color.Color(lipgloss.NoColor{})) {
		t.Fatalf("NO_COLOR must force monochrome even with a named theme")
	}
}

func TestNamesContainsExpected(t *testing.T) {
	ns := Names()
	for _, want := range []string{"warm-sunset", "dracula", "nord", "catppuccin-mocha"} {
		found := false
		for _, n := range ns {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Names() missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/app/tui/theme/ -run 'TestLoadWithConfig|TestNames' -v`
Expected: FAIL — `undefined: LoadWithConfig`, `undefined: Names`.

- [ ] **Step 3: Add the new API**

In `internal/app/tui/theme/theme.go`:

- Refactor `Load` to delegate to `LoadWithConfig("warm-sunset", nil)` (so the public `Load()` signature is unchanged).
- Implement `LoadWithConfig(name string, overrides PaletteOverrides) Theme`:

```go
func LoadWithConfig(name string, overrides PaletteOverrides) Theme {
	if os.Getenv("NO_COLOR") != "" {
		return monochromeTheme()
	}
	preset, ok := LookupPreset(name)
	if !ok {
		preset = warmSunset256
	}
	if strings.Contains(os.Getenv("TERM"), "256color") ||
		strings.Contains(os.Getenv("TERM"), "kitty") ||
		strings.Contains(os.Getenv("TERM"), "wezterm") {
		return overrides.Apply(preset)
	}
	return overrides.Apply(warmSunset16)
}
```

- Implement `Names() []string` returning the sorted preset names.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/app/tui/theme/ -v`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/app/tui/theme/ && gofmt -w internal/app/tui/theme/theme.go internal/app/tui/theme/theme_test.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/theme/theme.go internal/app/tui/theme/theme_test.go
git commit -m "feat(theme): add LoadWithConfig and Names for runtime theme selection"
```

---

## Task 3: `[tui]` TOML config block

**Files:**
- Modify: `internal/app/config/config.go`
- Modify: `internal/app/config/save.go` (if round-tripping is in scope)
- Test: `internal/app/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/app/config/config_test.go`:

```go
func TestTUITomlRoundTrip(t *testing.T) {
	in := `
[tui]
theme = "dracula"
palette = { accent_primary = "#ff00ff", fg_default = "#cdd6f4" }
`
	var cfg Config
	if err := toml.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.TUI.Theme != "dracula" {
		t.Fatalf("Theme = %q, want dracula", cfg.TUI.Theme)
	}
	if got := cfg.TUI.Palette["accent_primary"]; got != "#ff00ff" {
		t.Fatalf("Palette[accent_primary] = %q, want #ff00ff", got)
	}
}

func TestTUIDefaultsAreEmpty(t *testing.T) {
	cfg := Default()
	if cfg.TUI.Theme != "" {
		t.Fatalf("default TUI.Theme = %q, want empty", cfg.TUI.Theme)
	}
	if cfg.TUI.Palette != nil {
		t.Fatalf("default TUI.Palette = %v, want nil", cfg.TUI.Palette)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/app/config/ -run 'TestTUI' -v`
Expected: FAIL — `cfg.TUI undefined`.

- [ ] **Step 3: Add the `TUIConfig` block**

In `internal/app/config/config.go`:

- Add to the `Config` struct:
```go
TUI TUIConfig `toml:"tui"`
```
- Add the new types:
```go
type TUIConfig struct {
	Theme   string            `toml:"theme"`
	Palette map[string]string `toml:"palette"`
}
```

If `save.go` mirrors the struct for round-tripping, add the equivalent `tuiFile` struct and include it in the file-mirror type used by the save path. If mirroring isn't done automatically, the implementer should ASK before extending the save path.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/app/config/ -v`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/app/config/ && gofmt -w internal/app/config/config.go internal/app/config/config_test.go internal/app/config/save.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/app/config/config.go internal/app/config/config_test.go internal/app/config/save.go
git commit -m "feat(config): add [tui] block for theme name and palette overrides"
```

---

## Task 4: Settings TUI integration

**Files:**
- Modify: `internal/app/tui/model.go` — call `theme.LoadWithConfig` at startup, threading the active `TUIConfig`.
- Modify: `internal/app/tui/settings/frames_basic.go` — add a "Theme" `kindEnum` row.

**Interfaces:**
- Produces: A new `kindEnum` field "Theme" in the settings (root or interface section) with options sourced from `theme.Names()`. Selecting a preset writes `s.cfg.TUI.Theme = name`. Selecting the value triggers a re-`LoadWithConfig` so the active theme updates live; the existing renderer path is unchanged because every slot is referenced, not hardcoded.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/tui/settings/frames_basic_test.go` (or a new test file in the settings package):

```go
func TestThemeFieldEnumListsPresets(t *testing.T) {
	st := newState(config.Default())
	rows := interfaceFrame(st).list.Rows()
	var themeRow *field
	for _, r := range rows {
		if r.title == "Theme" {
			themeRow = r
			break
		}
	}
	if themeRow == nil {
		t.Fatal("settings must have a Theme row")
	}
	if themeRow.kind != kindEnum {
		t.Fatalf("Theme row kind = %v, want kindEnum", themeRow.kind)
	}
	opts := themeRow.options()
	if len(opts) < 4 {
		t.Fatalf("theme options = %v, want at least 4", opts)
	}
}
```

(If `interfaceFrame` doesn't exist, the implementer should pick the most natural existing section — most likely the "interface" / "ui" root — and ASK if it's not obvious.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/app/tui/settings/ -run TestThemeFieldEnumListsPresets -v`
Expected: FAIL — no Theme row.

- [ ] **Step 3: Add the Theme row and the live-reload hook**

In `internal/app/tui/settings/frames_basic.go`, add a `Theme` `kindEnum` row in the appropriate section with `options: theme.Names`. The setter writes to `s.cfg.TUI.Theme`.

In `internal/app/tui/model.go`, change the startup theme call from `theme.Load()` to `theme.LoadWithConfig(m.cfg.TUI.Theme, theme.PaletteOverrides(m.cfg.TUI.Palette))`. Add a re-load path when the config is updated (typically the existing `reloadTheme` or equivalent hook in `model.go`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/app/tui/settings/ -v`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/app/tui/... && gofmt -w internal/app/tui/model.go internal/app/tui/settings/frames_basic.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/settings/frames_basic.go internal/app/tui/settings/frames_basic_test.go
git commit -m "feat(tui): add Theme enum to settings and live-reload via LoadWithConfig"
```

---

## Task 5: Audit raw color literals

**Files:**
- Modify: any renderer file that still hardcodes `lipgloss.Color("…")`

- [ ] **Step 1: Find offenders**

Run: `grep -rn 'lipgloss\.Color("[0-9]' internal/app/tui/ | grep -v theme/`
Expected: zero matches after this task.

- [ ] **Step 2: Replace each with a theme slot**

For each match, replace the literal with the matching semantic slot from `theme.Theme`. Common mappings:
- `lipgloss.Color("209")` → `theme.AccentPrimary`
- `lipgloss.Color("244")` → `theme.FGMuted`
- `lipgloss.Color("255")` → `theme.FGEmphasis`
- `lipgloss.Color("203")` → `theme.StatusError`
- `lipgloss.Color("172")` → `theme.StatusWarning`
- `lipgloss.Color("43")` → `theme.StatusSuccess`
- `lipgloss.Color("246")` → `theme.UserPrompt`

Renderers that already reference theme slots need no change. Renderers that define package-level `var coralColor = lipgloss.Color("209")` style constants should be removed and the slot used directly.

- [ ] **Step 3: Run the test suite**

Run: `go test -count=1 ./internal/app/tui/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/app/tui/
git commit -m "refactor(tui): replace remaining raw color literals with theme slots"
```

---

## Task 6: Docs + audit log

**Files:**
- Modify: `docs/09-configuration-examples.md`
- Modify: `docs/11-roadmap-and-future-enhancements.md`
- Modify: `docs/13-project-audit-2026-07-11.md`

- [ ] **Step 1: Add the `[tui]` example**

In `docs/09-configuration-examples.md`, add a new section:

```toml
# TUI palette and theme
[tui]
theme = "dracula"                              # warm-sunset | dracula | nord | catppuccin-mocha
palette = { accent_primary = "#bd93f9" }      # optional per-slot overrides (slot name → hex or 256)
```

- [ ] **Step 2: Mark Feature #2 shipped**

In `docs/11-roadmap-and-future-enhancements.md`, replace Feature #2 ("Visual TUI Themes") with a short "shipped" note pointing to the audit doc and the commit range.

- [ ] **Step 3: Add the audit-doc batch section**

In `docs/13-project-audit-2026-07-11.md`, append a new section mirroring the prior batch sections:

```markdown
## Implementation batch — TUI themes

The TUI palette and theme selection were extended to support TOML-driven
customization and four named themes (warm-sunset, dracula, nord,
catppuccin-mocha) on branch `feature/tui-themes`:

```
<commit> feat(theme): add Dracula, Nord, Catppuccin Mocha presets and palette overrides
<commit> feat(theme): add LoadWithConfig and Names for runtime theme selection
<commit> feat(config): add [tui] block for theme name and palette overrides
<commit> feat(tui): add Theme enum to settings and live-reload via LoadWithConfig
<commit> refactor(tui): replace remaining raw color literals with theme slots
```

### What changed

- `internal/app/tui/theme/presets.go` ships three new dark palettes plus a `PaletteOverrides` merge helper.
- `internal/app/tui/theme.LoadWithConfig(name, overrides)` selects a named theme and applies per-slot overrides; the old `Load()` is preserved as a thin wrapper.
- `internal/app/config` gains a `[tui]` block (`theme`, `palette`).
- Settings gets a `Theme` `kindEnum` row; selecting a theme live-reloads the active palette.

### Unchanged

- `NO_COLOR` always forces monochrome, even with a named theme.
- Light themes and auto-detect are out of scope for this batch.
```

Use the real commit hashes after the batch lands.

- [ ] **Step 4: Commit**

```bash
git add docs/09-configuration-examples.md docs/11-roadmap-and-future-enhancements.md docs/13-project-audit-2026-07-11.md
git commit -m "docs(tui): document theme presets and palette override TOML"
```

---

## Batch closeout

After Task 6, run the full verification gates:

```bash
gofmt -w .
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```

Update the `## Dated resolution note` section of `docs/13-project-audit-2026-07-11.md` with a one-paragraph entry citing the actual commit range and branch.

---

## Self-Review

**Spec coverage:**
- Four named themes ship (warm-sunset default + dracula + nord + catppuccin-mocha); `Names()` exposes them to the settings enum.
- `PaletteOverrides` accepts per-slot hex / 256-color entries; `Apply` merges them onto a preset.
- `LoadWithConfig` is the new entry point; the old `Load()` is a wrapper so no other call site needs to change.
- `NO_COLOR` precedence is preserved.
- All renderers reference `theme` slots (Task 5 audits any stragglers).

**Type consistency:**
- `PaletteOverrides` is `map[string]string`; slot names are stable and lower_snake_case.
- `TUIConfig` is a regular TOML struct; round-tripping is handled by the existing config-mirror pattern.

**Placeholder scan:** No TBDs. The implementer may need to ASK about the right settings section for the `Theme` row (Task 4) and about extending `save.go` (Task 3) — both have explicit ASK guidance in the steps.
