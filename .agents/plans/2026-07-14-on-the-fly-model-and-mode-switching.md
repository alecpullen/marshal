# On-the-fly Model & Mode Switching — Implementation Plan

Date: 2026-07-14
Status: Draft (pre-implementation)

## Goal

Add two hotkey-driven switching behaviours to the main chat view:

1. **Mode cycling** via `Tab` (forward) and `Shift+Tab` (backward) through the
   three interaction modes: `auto → ask → edit → auto …`.
2. **Model cycling** via `Alt+M` (forward) and `Alt+Shift+M` (backward) through
   the configured model presets.

Both are **session-only**: nothing is written to config files. `/settings`
remains the persistence surface, and the existing `/ask`, `/edit`, `/auto`,
`/mode`, and `/model` commands keep working unchanged.

## Why these keys

- **Tab / Shift+Tab** is the standard "cycle mode" binding in AI coding agents
  (Claude Code, Crush, opencode). In Marshal, `Tab` currently *only* accepts a
  completion popup when one is visible (model.go:654). When no popup is up,
  `Tab` falls through to `m.input.Update` and inserts a literal tab into the
  textarea — which is almost never what the user wants. Repurposing the
  no-popup `Tab`/`Shift+Tab` as mode cycling is strictly better and matches
  user expectations from other agents.
- **Alt+M** is the only reliable Tab-modifier-free choice:
  - `Ctrl+Tab` is **not** in ultraviolet's key table — terminals either send
    `\t` (Ctrl+I → plain Tab) or swallow it (terminal app tab switching).
    Notably **Alt+Tab is the OS window/App switcher** on macOS/Windows/most
    Linux DEs and never reaches the TTY.
  - `Alt+M` is delivered by every terminal as `\x1bm` (ESC-prefix, see
    ultraviolet decoder.go:304 / :765 / :1061) and maps to `"alt+m"` via
    `Keystroke()`. It has no existing binding in the codebase and is mnemonic
    ("**m**odel").
  - `Alt+Shift+M` (`"shift+alt+m"`) is the backward direction and is likewise
    delivered reliably (decoder.go:702 / :867 / :944).

## Key representation (Bubble Tea v2 / ultraviolet)

All matching uses `msg.String()` (the established pattern in model.go:569):
- `Tab` → `"tab"` — `tea.KeyPressMsg{Code: tea.KeyTab}`
- `Shift+Tab` → `"shift+tab"` — `tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}`
- `Alt+M` → `"alt+m"` — `tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt}`
- `Alt+Shift+M` → `"shift+alt+m"` — `tea.KeyPressMsg{Code: 'm', Mod: tea.ModShift|tea.ModAlt}`

## Current state (context for the implementer)

- **Mode state** lives in `Model.forceMode` (`""`/`"ask"`/`"edit"`, where `""`
  means auto). Set by the `/ask`, `/edit`, `/auto`, `/mode` dispatch paths
  (model.go:1518-1551), which also call `runner.SetForceClass(...)`.
- **Model state** is driven by `switchModelPreset(name)` (model.go:1621),
  which builds a synthetic `"switched"` `AgentProfile` pointing every role at
  the chosen preset, then calls `configReloader`. The active preset is read
  from `m.state.ActiveRoute().Preset`.
- **Picker** (`/model`, `/mode`) and commands remain the explicit/visible
  surface; the hotkeys are the fast path.
- The persistent footer and `?` help overlay (help/help.go) list keybindings.
- The status line (status.go:69 `modeSegment`, :106 `route`) already reflects
  mode + model — no status-line changes needed for display.

## Design decisions

### D1 — Tab only cycles modes when no completion popup is open

`Tab` keeps its current "accept completion" meaning when a popup is visible
(preserves existing behaviour and the popup footer hint
`Tab/Enter accept`). When no popup is up, `Tab` cycles mode forward and
`Shift+Tab` cycles backward. This avoids a destructive change to the
completion UX and keeps the two concerns cleanly separated.

### D2 — Mode cycle order and wrap

Fixed order: `auto → ask → edit → (auto)`. `Shift+Tab` reverses it.
The empty-string sentinel (`""` = auto) is preserved internally; the cycle
helper maps `""` ↔ `"auto"` at the boundaries so the order is deterministic.

### D3 — Model cycle order

`modelPickerItems()` (model.go:1650) already sorts presets by provider then
name. The cycle reuses that exact ordering so the hotkey, the `/model` picker,
and the `/model <name>` arg path all present models in the same order. The
cycle starts from the current active preset (matched by name, falling back
to index 0 when the active route isn't from a configured preset — e.g. the
legacy route).

### D4 — Single-preset and zero-preset edge cases

- **0 presets**: `Alt+M` is a no-op (matches the existing `/model` path which
  prints "No model presets configured…"). We print the same guidance message
  so the hotkey is discoverable, not silent.
- **1 preset**: `Alt+M` still calls `switchModelPreset` to the same preset so
  the confirmation message ("Switched to model: …") gives tactile feedback
  that the key works; no-op optimisation would feel broken.

### D5 — Reuse `switchModelPreset` and the mode dispatch

Both hotkeys funnel through the *existing* code paths to keep semantics in
one place:
- Mode hotkey → calls the same body as the `case "ask"/"edit"/"auto"` arms
  of `dispatchCommand` (extracted into a small `setMode(string)` helper).
- Model hotkey → calls `switchModelPreset(name)` unchanged.

This means the runner's `SetForceClass` and the config reloader are
exercised exactly as they are today; the hotkeys are thin triggers.

### D6 — No persistence

Hotkey switches are session-only, identical to `/model` and the existing
mode commands. `/settings` (Ctrl+O) remains the only path that writes to
`config.toml`. No new config fields are introduced.

### D7 — Help + footer updates

- Footer (help/help.go `Footer`): in the idle (non-busy, no-popup) branch,
  add `Tab`/`Shift+Tab` → "mode" and `Alt+M` → "model" hints. Keep the footer
  short (it already collapses hints); add the two new pairs alongside
  `Enter send`, `/ command`, `@ file`. To avoid overflow on 80-col terms,
  prefer showing `Tab mode` and `Alt+M model` only when not busy (busy already
  hides `/` and `@`), and rely on the `?` overlay for the full list.
- Help overlay (help/help.go `Overlay`): add four lines:
  - `  Tab            cycle mode (auto→ask→edit)`
  - `  Shift+Tab      cycle mode backward`
  - `  Alt+M          cycle model`
  - `  Alt+Shift+M     cycle model backward`

### D8 — Guard against cycling during a turn

Mode switches are safe mid-turn (they only set `ForceClass` for the *next*
turn), so `Tab`/`Shift+Tab` work while busy. Model switches call
`configReloader`, which in `app.go` rebuilds the runner; doing that while a
turn is in-flight is risky (the in-flight turn holds references that
`reloadAgentRuntime` swaps). To stay safe and simple: **`Alt+M` /
`Alt+Shift+M` are no-ops while `m.busy` is true**, with a short "Busy — model
switched after this turn" style system message so the key isn't silently
eaten. (Matches the existing `Ctrl+O` settings-block-while-busy UX.)

## Implementation tasks

### Task 1 — Extract `setMode` helper (model.go)

Refactor the bodies of the `case "ask"/"edit"/"auto"` arms in
`dispatchCommand` (model.go:1518-1540) into:

```go
// setMode applies an interaction mode ("ask", "edit", or "" for auto) for
// the next turn. Shared by the /ask, /edit, /auto, /mode commands and the
// Tab/Shift+Tab mode-cycling hotkeys.
func (m *Model) setMode(mode string) {
    if m.runner != nil {
        m.runner.SetForceClass(mode) // "" => auto (classifier runs)
    }
    m.forceMode = mode
}
```

The three `case` arms and the `/mode` picker-apply path call `m.setMode(...)`
instead of duplicating the two-line body. No behaviour change.

**Test:** existing `TestModePickerMarksCurrentAndApplies`,
`TestModeWithArgDispatchesDirectly` must still pass unchanged.

### Task 2 — Add `cycleMode` helper (model.go)

```go
// modeOrder is the canonical cycle order used by Tab/Shift+Tab.
// "" represents auto (the classifier-driven default).
var modeOrder = []string{"", "ask", "edit"}

// cycleMode advances (forward=true) or reverses the interaction mode,
// wrapping around. It applies the result via setMode and emits the same
// confirmation message the /<mode> commands use.
func (m *Model) cycleMode(forward bool) {
    cur := m.forceMode
    idx := indexOf(modeOrder, cur)
    if idx < 0 {
        idx = 0
    }
    step := 1
    if !forward {
        step = -1
    }
    next := modeOrder[(idx+step+len(modeOrder))%len(modeOrder)]
    m.setMode(next)
    // Reuse the command-handler confirmation messages so the transcript
    // looks identical whether the user pressed Tab or typed /ask.
    label := modeLabel(next)
    m.state.AddMessage(session.RoleSystem,
        fmt.Sprintf("Switched to %s mode.", label), session.ContentTypePlain)
    m.refreshViewport()
}
```

`modeLabel("")` returns `"Auto"`, etc. (`indexOf` is a tiny local helper or
slices.Index if available on the supported Go version).

### Task 3 — Wire `Tab` / `Shift+Tab` in the keypress switch (model.go)

In `Update`, `case tea.KeyPressMsg`, extend the existing `"tab"` arm (model.go:654)
and add `"shift+tab"`:

```go
case "tab":
    if m.acceptCompletion() {
        return m, nil
    }
    if m.state.PendingApproval() != nil || m.state.PendingQuestion() != nil {
        break // let these fall through to their handlers / textarea
    }
    m.cycleMode(true)
    return m, nil
case "shift+tab":
    if m.activeCompletionPopup() != nil {
        // Shift+Tab in a popup: keep current "ignore" behaviour (no
        // backward completion selection) so we don't fight the popup.
        return m, nil
    }
    if m.state.PendingApproval() != nil || m.state.PendingQuestion() != nil {
        break
    }
    m.cycleMode(false)
    return m, nil
```

Notes:
- `acceptCompletion()` already returns false when no popup is up, so the
  existing completion path is untouched.
- The approval/question guards preserve the current "Tab is ignored during
  approval" behaviour asserted by model_test.go:1044-1057.
- `Shift+Tab` during a popup is explicitly a no-op (preserves the existing
  assertion at model_test.go:1046) rather than cycling modes.

### Task 4 — Add `cycleModel` helper (model.go)

```go
// cycleModel advances (forward=true) or reverses the active model preset,
// wrapping around. Order matches modelPickerItems() (provider then name).
// Session-only: delegates to switchModelPreset.
func (m *Model) cycleModel(forward bool) {
    if m.busy {
        m.state.AddMessage(session.RoleSystem,
            "Busy — switch the model after this turn completes.",
            session.ContentTypePlain)
        m.refreshViewport()
        return
    }
    names := m.sortedPresetNames()
    if len(names) == 0 {
        m.state.AddMessage(session.RoleSystem,
            "No model presets configured. Add one in /settings → Model Presets.",
            session.ContentTypePlain)
        m.refreshViewport()
        return
    }
    cur := m.state.ActiveRoute().Preset
    idx := indexOf(names, cur)
    if idx < 0 {
        idx = 0 // legacy/unknown route → start at the first preset
    }
    step := 1
    if !forward {
        step = -1
    }
    target := names[(idx+step+len(names))%len(names)]
    m.switchModelPreset(target)
    m.refreshViewport()
}
```

`sortedPresetNames()` is extracted from the sorting logic in
`modelPickerItems()` (model.go:1656-1662) so the hotkey and the picker stay
in lock-step. `switchModelPreset` already emits its own confirmation
message, so `cycleModel` doesn't add a duplicate.

### Task 5 — Wire `Alt+M` / `Alt+Shift+M` (model.go)

In the keypress switch, after the existing global hotkeys:

```go
case "alt+m":
    m.cycleModel(true)
    return m, nil
case "shift+alt+m":
    m.cycleModel(false)
    return m, nil
```

`"shift+alt+m"` (not `"alt+shift+m"`) because ultraviolet prints modifiers in
fixed order ctrl→alt→shift→meta→… (key.go:414-431), and `msg.String()` falls
back to `Keystroke()` for modifier combos with no printable text.

### Task 6 — Update help footer (help/help.go)

In `Footer`, idle (non-busy, no-popup) branch, add the two new pairs. To
respect the 80-col budget, show `Tab` mode + `Alt+M` model and let
`Shift+Tab` / `Alt+Shift+M` live only in the `?` overlay:

```go
segs = append(segs,
    pair("Tab", "mode"),
    pair("Alt+M", "model"),
    pair("/", "command"),
    pair("@", "file"),
)
```

(Busy branch stays `Esc cancel · Ctrl+X clear queue` — modes still cycle
while busy via Tab, but we don't add the hint to keep the busy footer
focused on interrupt actions. The model hotkey is disabled while busy per
D8, so it must NOT be hinted there.)

### Task 7 — Update help overlay (help/help.go `Overlay`)

Insert after the existing `Tab accept completion` line:

```
  Tab            cycle mode (auto→ask→edit) · accept completion
  Shift+Tab      cycle mode backward
  Alt+M          cycle model
  Alt+Shift+M    cycle model backward
```

(Reword the existing `Tab accept completion` line to note the dual role, so
the overlay doesn't list `Tab` twice with conflicting meanings.)

### Task 8 — Tests (model_test.go)

Add focused unit tests mirroring the existing mode-picker tests:

1. `TestTabCyclesModeForward` — start in auto, press `Tab` (no popup),
   assert `forceMode == "ask"`; press again → `"edit"`; again → `""` (auto).
   Assert the confirmation system message appears.
2. `TestShiftTabCyclesModeBackward` — start in `"edit"`, `Shift+Tab` →
   `"ask"`; again → `""`; again → `"edit"` (wrap).
3. `TestTabAcceptsCompletionWhenPopupOpen` — type `/`, press `Tab` while the
   cmd popup is visible, assert the completion is accepted and `forceMode`
   is unchanged (guards the D1 guard).
4. `TestTabIgnoredDuringApproval` — pending approval + `Tab` leaves
   `forceMode` unchanged (asserts the existing behaviour is preserved).
5. `TestAltMCyclesModelForward` — configure 2 presets, set active route to
   the first, press `Alt+M`, assert `configReloader` called with the second
   preset's profile and the active route updates.
6. `TestAltShiftMCyclesModelBackward` — same setup, `Alt+Shift+M` → first
   preset (wrap from index 0 to last).
7. `TestAltMNoPresetsShowsGuidance` — 0 presets, `Alt+M` emits the "No model
   presets configured…" system message and does not panic.
8. `TestAltMBlockedWhileBusy` — `m.busy = true`, `Alt+M` emits the "Busy —"
   message and does NOT call `configReloader`.
9. `TestShiftTabDuringPopupIsNoOp` — popup visible + `Shift+Tab` does not
   cycle mode (asserts D1/D3 guard).

All tests use the existing `modelTestState` / `WithConfigReloader` /
`sendKey` helpers already in model_test.go.

## Out of scope (explicit non-goals)

- Persisting mode/model hotkey choices to config (stays session-only).
- Changing the `/model` or `/mode` picker UX.
- A visual "model switcher" inline popover (the picker already covers the
  visible-surface need; the hotkey is the fast path).
- Remapping `Ctrl+M` (docs/06 lists it for model switching, but it's not
  wired and `Enter` is Ctrl+M in terminals — keeping it free is safer).
- Backward direction for Tab-based model switching (not possible —
  Alt+Shift+M covers it).

## Risk / verification

- `go vet ./...` and `go test ./internal/app/tui/... ./internal/commands/...`
  must pass.
- The existing approval/popup tests (model_test.go:1044, :1363, :1394) must
  remain green — they assert Tab/Shift+Tab behaviour during forms/popups,
  which the guards in Task 3 preserve.
- Manual smoke test in a real terminal (iTerm2/Terminal.app/Alacritty):
  - `Tab` cycles modes and the status line updates.
  - `Shift+Tab` cycles backward.
  - `Alt+M` switches models with a visible confirmation line.
  - `Alt+Shift+M` cycles backward.
  - `/` then `Tab` still accepts a command completion (regression check).