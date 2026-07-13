# TUI Light Theme Variants Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add light variants of the 4 existing dark themes (`warm-sunset`, `dracula`, `nord`, `catppuccin-mocha`). The user picks a theme + a `mode` (`light` or `dark`); the canonical light variants come from the community-maintained counterparts (Dracula Light, Nord Light, Catppuccin Latte, and a hand-rolled warm-sunset-light). Closes the "Light themes and auto-detect are out of scope" deferral from the just-merged `feature/tui-themes` batch.

**Architecture:** Two changes to `internal/app/tui/theme`:
1. Add 4 new 256-color palettes: `warmSunsetLight256`, `draculaLight256`, `nordLight256`, `catppuccinLatte256` (the official Catppuccin light theme is named "Latte").
2. Extend `LookupPreset` (or a new `LookupVariant` helper) so callers can resolve `(name, mode) → Theme`. A "mode" field is added to the public API: `LoadWithConfig(name, mode string, overrides PaletteOverrides) Theme`. The settings enum (`theme.Names()`) returns the canonical 4 dark names; a new `theme.LightNames()` returns the 4 light names. The settings TUI grows a "Mode" `kindEnum` row alongside "Theme".

The 16-color tier of each light theme is the same set of relative codes used in `warmSunset16` but with foreground/background swapped (per the existing convention from `warmSunset256` → `warmSunset16`). Auto-detect (light/dark from `OSC 11` terminal query) remains out of scope.

**Tech Stack:** Go 1.26.1, `charm.land/lipgloss/v2` (already in use), `image/color` for parsing. No new external dependencies.

**Assumes Milestone R + the prior `feature/tui-themes` batch are complete** (they are). The new public API extends the existing one in a backward-compatible way: the old `LoadWithConfig(name, overrides)` becomes a thin wrapper that calls `LoadWithConfig(name, "dark", overrides)`.

## Global Constraints

- **Backward compatible default:** when `mode` is empty, the active theme is the existing dark variant. Existing configs that don't set `tui.mode` continue to render dark.
- **NO_COLOR still wins.** Even with `mode = "light"`, `NO_COLOR=1` forces the monochrome theme.
- **The 4 light themes are community-canonical values.** Use the standard palettes:
  - **Dracula Light**: BG `#ffffff`, FG `#282a36`, accent `#ff79c6`, link `#8be9fd`.
  - **Nord Light**: BG `#eceff4`, FG `#2e3440`, accent `#5e81ac`.
  - **Catppuccin Latte**: BG `#eff1f5`, FG `#4c4f69`, accent `#8839ef` (mauve).
  - **Warm Sunset Light**: BG `#fef9f3`, FG `#3a2a1a`, accent `#a85d2c` (a hand-rolled warm light variant).
- **No new external dependencies.**
- **No comments unless asked.** Match existing gofmt style.
- **Verification:** `go test -count=1 ./internal/app/tui/theme/...` after every task; full gates before batch closeout.

## File Structure

**Modify:**
- `internal/app/tui/theme/theme.go` — extend `LoadWithConfig` with a `mode` parameter; add `ModeDark`/`ModeLight` constants; add `Names(mode string) []string` (or keep `Names()` returning the 4 canonical names and add `LightNames()`).
- `internal/app/tui/theme/presets.go` — add the 4 light 256-color palettes; extend `LookupPreset` to take a mode; add `presets` entries for the light variants.
- `internal/app/config/config.go` — add `Mode string` field to `TUIConfig`.
- `internal/app/tui/settings/frames_tui.go` (or wherever the Theme row landed in the prior batch) — add a `Mode` `kindEnum` row.
- `internal/app/tui/model.go` — pass `m.cfg.TUI.Mode` to `LoadWithConfig`.
- `docs/09-configuration-examples.md` — add `mode = "light"` example.
- `docs/11-roadmap-and-future-enhancements.md` — note that the light-variant slice of Feature #2 has now shipped.
- `docs/13-project-audit-2026-07-11.md` — append a new "Implementation batch — TUI light themes" section.

**Add tests:**
- `internal/app/tui/theme/presets_test.go` — 4 new light-theme tests.
- `internal/app/tui/theme/theme_test.go` — 3 new `LoadWithConfig` mode tests.

---

## Task 1: Light palettes

**Files:**
- Modify: `internal/app/tui/theme/presets.go`
- Test: `internal/app/tui/theme/presets_test.go`

**Interfaces:**
- Produces: 4 new 256-color `Theme` values (`warmSunsetLight256`, `draculaLight256`, `nordLight256`, `catppuccinLatte256`) and corresponding entries in the `presets` map (keyed by `<name>-light`).

- [ ] **Step 1: Write the failing tests**

In `presets_test.go`, add 4 tests:

```go
func TestLookupPresetLightVariants(t *testing.T) {
    for _, name := range []string{"warm-sunset-light", "dracula-light", "nord-light", "catppuccin-latte"} {
        th, ok := LookupPreset(name)
        if !ok {
            t.Errorf("LookupPreset(%q) returned ok=false, want true", name)
        }
        if th.AccentPrimary == nil {
            t.Errorf("LookupPreset(%q).AccentPrimary is nil", name)
        }
    }
}

func TestLightVariantsAreDistinctFromDark(t *testing.T) {
    dark, _ := LookupPreset("dracula")
    light, _ := LookupPreset("dracula-light")
    if dark.BGBase == light.BGBase {
        t.Errorf("dracula and dracula-light have the same BGBase (%v); they should differ", dark.BGBase)
    }
    if dark.FGDefault == light.FGDefault {
        t.Errorf("dracula and dracula-light have the same FGDefault (%v); they should differ", dark.FGDefault)
    }
}

func TestOverridesApplyToLightVariant(t *testing.T) {
    base, _ := LookupPreset("catppuccin-latte")
    ov := PaletteOverrides{"accent_primary": "#8839ef"}
    got := ov.Apply(base)
    if got.AccentPrimary == base.AccentPrimary {
        t.Errorf("Overrides.Apply on light variant didn't change AccentPrimary")
    }
}

func TestModeIsPartOfPresetName(t *testing.T) {
    for _, name := range []string{"warm-sunset", "warm-sunset-light", "dracula", "dracula-light", "nord", "nord-light", "catppuccin-mocha", "catppuccin-latte"} {
        if _, ok := LookupPreset(name); !ok {
            t.Errorf("LookupPreset(%q) returned ok=false, want true", name)
        }
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/app/tui/theme/ -run 'TestLookupPresetLight|TestLightVariants|TestOverridesApplyToLight|TestModeIsPartOfPreset' -v`
Expected: FAIL — `LookupPreset("warm-sunset-light")` returns false.

- [ ] **Step 3: Add the 4 light palettes**

In `presets.go`, add after the existing `catppuccinMocha256`:

```go
var warmSunsetLight256 = Theme{
    FGDefault:       lipgloss.Color("243"),
    FGMuted:         lipgloss.Color("247"),
    BorderMuted:     lipgloss.Color("248"),
    FGEmphasis:      lipgloss.Color("236"),
    BGBase:          lipgloss.Color("255"),
    BGSurface:       lipgloss.Color("254"),
    BGSelection:     lipgloss.Color("180"),
    AccentPrimary:   lipgloss.Color("173"),
    AccentSecondary: lipgloss.Color("131"),
    AccentTertiary:  lipgloss.Color("214"),
    UserPrompt:      lipgloss.Color("240"),
    StatusError:     lipgloss.Color("160"),
    StatusWarning:   lipgloss.Color("172"),
    StatusSuccess:   lipgloss.Color("71"),
    StatusInfo:      lipgloss.Color("31"),
}

var draculaLight256 = Theme{
    FGDefault:       lipgloss.Color("236"),
    FGMuted:         lipgloss.Color("243"),
    BorderMuted:     lipgloss.Color("247"),
    FGEmphasis:      lipgloss.Color("236"),
    BGBase:          lipgloss.Color("255"),
    BGSurface:       lipgloss.Color("231"),
    BGSelection:     lipgloss.Color("225"),
    AccentPrimary:   lipgloss.Color("212"),
    AccentSecondary: lipgloss.Color("141"),
    AccentTertiary:  lipgloss.Color("214"),
    UserPrompt:      lipgloss.Color("240"),
    StatusError:     lipgloss.Color("160"),
    StatusWarning:   lipgloss.Color("172"),
    StatusSuccess:   lipgloss.Color("71"),
    StatusInfo:      lipgloss.Color("31"),
}

var nordLight256 = Theme{
    FGDefault:       lipgloss.Color("236"),
    FGMuted:         lipgloss.Color("243"),
    BorderMuted:     lipgloss.Color("247"),
    FGEmphasis:      lipgloss.Color("236"),
    BGBase:          lipgloss.Color("255"),
    BGSurface:       lipgloss.Color("231"),
    BGSelection:     lipgloss.Color("225"),
    AccentPrimary:   lipgloss.Color("67"),
    AccentSecondary: lipgloss.Color("110"),
    AccentTertiary:  lipgloss.Color("222"),
    UserPrompt:      lipgloss.Color("240"),
    StatusError:     lipgloss.Color("167"),
    StatusWarning:   lipgloss.Color("172"),
    StatusSuccess:   lipgloss.Color("107"),
    StatusInfo:      lipgloss.Color("31"),
}

var catppuccinLatte256 = Theme{
    FGDefault:       lipgloss.Color("238"),
    FGMuted:         lipgloss.Color("245"),
    BorderMuted:     lipgloss.Color("247"),
    FGEmphasis:      lipgloss.Color("237"),
    BGBase:          lipgloss.Color("255"),
    BGSurface:       lipgloss.Color("231"),
    BGSelection:     lipgloss.Color("225"),
    AccentPrimary:   lipgloss.Color("141"),
    AccentSecondary: lipgloss.Color("110"),
    AccentTertiary:  lipgloss.Color("222"),
    UserPrompt:      lipgloss.Color("240"),
    StatusError:     lipgloss.Color("167"),
    StatusWarning:   lipgloss.Color("172"),
    StatusSuccess:   lipgloss.Color("107"),
    StatusInfo:      lipgloss.Color("31"),
}
```

The exact 256-color values are non-load-bearing — the contract is that each light variant has a different `BGBase` from its dark counterpart (light = high 256 value like 255, dark = low like 234-236) and an appropriate accent.

- [ ] **Step 4: Register the light variants in the presets map**

Add 4 new entries to `presets`:

```go
"warm-sunset-light": warmSunsetLight256,
"dracula-light":     draculaLight256,
"nord-light":        nordLight256,
"catppuccin-latte":  catppuccinLatte256,
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -count=1 ./internal/app/tui/theme/ -v`
Expected: all 18+ tests PASS.

- [ ] **Step 6: Vet and format**

Run: `gofmt -w internal/app/tui/theme/presets.go internal/app/tui/theme/presets_test.go` and `go vet ./internal/app/tui/theme/`.
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/theme/presets.go internal/app/tui/theme/presets_test.go
git commit -m "feat(theme): add light variants for all four presets"
```

---

## Task 2: `LoadWithConfig` accepts a mode

**Files:**
- Modify: `internal/app/tui/theme/theme.go`
- Test: `internal/app/tui/theme/theme_test.go`

**Interfaces:**
- Produces: `LoadWithConfig(name, mode, overrides) Theme` with `ModeDark` / `ModeLight` constants. `Load()` becomes a thin wrapper calling `LoadWithConfig("warm-sunset", ModeDark, nil)`. `mode` is case-insensitive.

- [ ] **Step 1: Write the failing tests**

In `theme_test.go`, add 3 tests:

```go
func TestLoadWithConfigModeLight(t *testing.T) {
    t.Setenv("NO_COLOR", "")
    t.Setenv("TERM", "xterm-256color")
    th := LoadWithConfig("dracula", "light", nil)
    dark, _ := LookupPreset("dracula")
    if th.BGBase == dark.BGBase {
        t.Fatalf("light mode should produce a different BGBase; got %v (matches dark)", th.BGBase)
    }
    light, _ := LookupPreset("dracula-light")
    if th.BGBase != light.BGBase {
        t.Fatalf("light mode should resolve to dracula-light; got BGBase %v, want %v", th.BGBase, light.BGBase)
    }
}

func TestLoadWithConfigModeDefaultsToDark(t *testing.T) {
    t.Setenv("NO_COLOR", "")
    t.Setenv("TERM", "xterm-256color")
    th := LoadWithConfig("dracula", "", nil)
    dark, _ := LookupPreset("dracula")
    if th.BGBase != dark.BGBase {
        t.Fatalf("empty mode should default to dark; got %v, want %v", th.BGBase, dark.BGBase)
    }
}

func TestLoadWithConfigModeCaseInsensitive(t *testing.T) {
    t.Setenv("NO_COLOR", "")
    t.Setenv("TERM", "xterm-256color")
    a := LoadWithConfig("dracula", "LIGHT", nil)
    b := LoadWithConfig("dracula", "light", nil)
    if a.BGBase != b.BGBase {
        t.Fatalf("LIGHT and light should be equivalent; got %v vs %v", a.BGBase, b.BGBase)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/app/tui/theme/ -run 'TestLoadWithConfigMode' -v`
Expected: FAIL — `LoadWithConfig` has the wrong signature (3 args, not 2).

- [ ] **Step 3: Add the mode parameter**

In `theme.go`:

- Add constants:
```go
const (
    ModeDark  = "dark"
    ModeLight = "light"
)
```

- Update `Load` to be:
```go
func Load() Theme {
    return LoadWithConfig("warm-sunset", ModeDark, nil)
}
```

- Update `LoadWithConfig` signature and implementation:
```go
func LoadWithConfig(name, mode string, overrides PaletteOverrides) Theme {
    if os.Getenv("NO_COLOR") != "" {
        return monochromeTheme()
    }
    if mode == "" {
        mode = ModeDark
    }
    lookupName := name
    if mode == ModeLight {
        // Map the 4 canonical names to their -light variants.
        switch name {
        case "warm-sunset":
            lookupName = "warm-sunset-light"
        case "dracula":
            lookupName = "dracula-light"
        case "nord":
            lookupName = "nord-light"
        case "catppuccin-mocha":
            lookupName = "catppuccin-latte"
        }
    }
    preset, ok := LookupPreset(lookupName)
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

Note: The 16-color tier is still `warmSunset16` for ALL themes/modes in this batch. A future batch can add per-theme 16-color variants.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/app/tui/theme/ -v`
Expected: all tests PASS.

- [ ] **Step 5: Update all existing call sites of `LoadWithConfig`**

The signature change breaks the prior batch's call sites. Update them to pass `ModeDark` as the second arg:

```bash
grep -rn "LoadWithConfig(" internal/ --include="*.go" | grep -v _test.go
```

For each match, add `, ModeDark` as the new second argument.

- [ ] **Step 6: Run all tests to confirm no regression**

Run: `go test -count=1 ./...`
Expected: all packages pass (only `ModeDark` is the new arg everywhere).

- [ ] **Step 7: Vet and format**

Run: `gofmt -w .` and `go vet ./...`.
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/app/tui/theme/theme.go internal/app/tui/theme/theme_test.go
# (plus the updated call sites)
git commit -m "feat(theme): add mode parameter to LoadWithConfig for light variants"
```

---

## Task 3: `[tui]` config gets `mode`

**Files:**
- Modify: `internal/app/config/config.go`
- Test: `internal/app/config/config_test.go`

**Interfaces:**
- Produces: `TUIConfig.Mode string` round-trips through TOML. Empty default.

- [ ] **Step 1: Write the failing test**

In `config_test.go`, add:

```go
func TestTUIModeRoundTrip(t *testing.T) {
    in := `
[tui]
mode = "light"
`
    var cfg Config
    if err := toml.Unmarshal([]byte(in), &cfg); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if cfg.TUI.Mode != "light" {
        t.Fatalf("Mode = %q, want light", cfg.TUI.Mode)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/app/config/ -run 'TestTUIMode' -v`
Expected: FAIL — `cfg.TUI.Mode undefined`.

- [ ] **Step 3: Add the field**

In `config.go`, extend `TUIConfig`:

```go
type TUIConfig struct {
    Theme   string            `toml:"theme"`
    Mode    string            `toml:"mode"`
    Palette map[string]string `toml:"palette"`
}
```

If the file-mirror pattern is used in this package (`fileTUI` from the prior batch), add `Mode` to the mirror too.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/app/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/config/config.go internal/app/config/config_test.go
git commit -m "feat(config): add tui.mode for light/dark theme variant selection"
```

---

## Task 4: Settings TUI gets a `Mode` row

**Files:**
- Modify: `internal/app/tui/settings/frames_tui.go` (or wherever the Theme row lives from the prior batch)
- Modify: `internal/app/tui/model.go` (the startup `LoadWithConfig` call now passes `m.cfg.TUI.Mode`)

- [ ] **Step 1: Read the existing Theme row**

Open `frames_tui.go` to see the `enumField` for Theme. Mirror its shape for Mode.

- [ ] **Step 2: Add a Mode enumField**

```go
enumField("tui.mode", "Mode", []string{"dark", "light"},
    func() string { return s.cfg.TUI.Mode },
    func(v string) { s.cfg.TUI.Mode = v }),
```

The setter writes to `s.cfg.TUI.Mode`.

- [ ] **Step 3: Wire startup**

In `model.go`, change the `LoadWithConfig` call to pass the mode:

```go
activeTheme = theme.LoadWithConfig(tui.Theme, tui.Mode, theme.PaletteOverrides(tui.Palette))
```

- [ ] **Step 4: Run settings + TUI tests**

Run: `go test -count=1 ./internal/app/tui/...`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/frames_tui.go internal/app/tui/model.go
git commit -m "feat(tui): add Mode enum to settings and thread to LoadWithConfig"
```

---

## Task 5: Docs + audit log

**Files:**
- Modify: `docs/09-configuration-examples.md`
- Modify: `docs/11-roadmap-and-future-enhancements.md`
- Modify: `docs/13-project-audit-2026-07-11.md`

- [ ] **Step 1: Update config example**

In `docs/09-configuration-examples.md`, find the `[tui]` example block from the prior batch and add `mode`:

```toml
[tui]
theme = "dracula"
mode  = "light"        # or "dark" (default)
palette = { accent_primary = "#ff79c6" }
```

- [ ] **Step 2: Update Feature #2 note in the roadmap**

In `docs/11-roadmap-and-future-enhancements.md`, the SHIPPED note from the prior batch should now read:

```markdown
## 2. Visual TUI Themes — SHIPPED (light variants in the "TUI light themes" batch)

Four dark themes (`warm-sunset`, `dracula`, `nord`, `catppuccin-mocha`) plus four light variants (`warm-sunset-light`, `dracula-light`, `nord-light`, `catppuccin-latte`). `mode = "light" | "dark"` selects between them. Auto-detect (OSC 11) remains out of scope.
```

- [ ] **Step 3: Add the audit-doc batch section**

Append to `docs/13-project-audit-2026-07-11.md`:

```markdown
## Implementation batch — TUI light themes

The deferred light-variant slice of `docs/11` Feature #2 was closed by the
following commits on branch `feature/tui-light-themes`:

```
<commit> feat(theme): add light variants for all four presets
<commit> feat(theme): add mode parameter to LoadWithConfig for light variants
<commit> feat(config): add tui.mode for light/dark theme variant selection
<commit> feat(tui): add Mode enum to settings and thread to LoadWithConfig
```

### What changed

- `internal/app/tui/theme/presets.go` ships four new light 256-color palettes.
- `LoadWithConfig(name, mode, overrides)` selects a variant based on `mode` (`"dark"` default, `"light"` switches to the `-light` map keys).
- `[tui]` config block gains a `mode` field.
- Settings TUI gets a `Mode` `kindEnum` row that writes to `s.cfg.TUI.Mode`.

### Unchanged

- `NO_COLOR` still forces monochrome, even with `mode = "light"`.
- Auto-detect (OSC 11 query) is out of scope.
- The 16-color tier is still `warmSunset16` for all themes in this batch.
```

- [ ] **Step 4: Commit**

```bash
git add docs/09-configuration-examples.md docs/11-roadmap-and-future-enhancements.md docs/13-project-audit-2026-07-11.md
git commit -m "docs(tui): document light theme variants and mode"
```

---

## Batch closeout

After Task 5, run the full verification gates:

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
- 4 light variants ship (`warm-sunset-light`, `dracula-light`, `nord-light`, `catppuccin-latte`), each with a distinct `BGBase` from its dark counterpart.
- `mode` parameter is case-insensitive; empty defaults to dark.
- `NO_COLOR` precedence is preserved.
- The existing 4 dark themes are unchanged.
- Backward compatible: existing call sites pass `ModeDark`.

**Type consistency:**
- `ModeDark` / `ModeLight` are string constants. Call sites that don't set `mode` get the default.

**Placeholder scan:** No TBDs. The 16-color tier for light themes is a known limitation (still uses `warmSunset16`); a future batch can add per-theme 16-color light variants.
