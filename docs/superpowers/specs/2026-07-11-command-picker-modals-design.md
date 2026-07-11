# Command Picker Modals

**Date:** 2026-07-11
**Status:** Approved (build AFTER the settings TUI redesign)
**Depends on:** `docs/superpowers/plans/2026-07-11-settings-tui-redesign.md` being executed first — this design reuses components that plan creates.
**Scope:** `internal/app/tui/` (new `picker` and `chrome` packages, command handling in `model.go`), `internal/commands/` (one new command).

## Problem

Slash commands that take an argument force the user to know and type exact names:
`/model` needs an exact preset name, `/rewind` and `/branches` need numbers the user
has to look up first, and mode switching is spread across three commands
(`/ask`/`/edit`/`/auto`) with no way to see the current mode while choosing. There is
no argument completion — the completion popup covers command names only.

## Decisions (from brainstorming)

- **Commands covered:** `/model`, `/rewind`, `/branches`, and a new `/mode` picker.
  All share one reusable picker component.
- **Presentation:** a small centered modal, truly overlaid on the chat transcript
  (line-composited, transcript visible behind it) — not a full-screen replacement,
  not inline in the input area, not the completion popup.
- **`/model` persistence:** session-only (current behavior kept). The modal footer
  states this and points at `/settings` for permanent changes.
- **Sequencing:** built after the settings redesign so it can reuse that work's
  panel chrome and fuzzy matcher instead of duplicating them.

## Design

### 1. Shared component extraction (prerequisite refactor)

The settings redesign plan creates three helpers as `internal/app/tui/settings`
package-private code. The picker's implementation plan starts by promoting them to
shared packages (signatures unchanged apart from an explicit theme parameter where
they currently read the settings package var):

| From (settings pkg) | To | Exported as |
|---|---|---|
| `renderPanel` (`chrome.go`) | `internal/app/tui/chrome` | `chrome.Panel(title, content string, w, h int, focused bool, th theme.Theme) string` |
| `clipLines` (`fieldlist.go`) | `internal/app/tui/chrome` | `chrome.ClipLines(lines []string, focusLine, height int, th theme.Theme) string` |
| `fuzzyFilter` + `isSubsequence` (`search.go`) | `internal/app/tui/fuzzy` | `fuzzy.Rank(query string, haystacks []string) []int` — returns indices of matches in rank order (substring hits before subsequence hits, both case-insensitive) |

`settings` call sites switch to the shared packages in the same refactor; settings
tests must stay green. `fuzzy.Rank` is generalized to work on strings+indices so
both settings `searchHit` filtering and picker item filtering wrap it with their own
types.

### 2. Overlay compositor (new)

`chrome.Overlay(bg, panel string, width, height int) string` — splices a rendered
panel into a rendered background, centered, line by line: for each panel row,
`out = ansi.Cut(bgLine, 0, x) + panelRow + ansi.Cut(bgLine, x+panelWidth, width)`
(using `github.com/charmbracelet/x/ansi`, already a dependency). The transcript
stays visible around the modal. No dimming of the background — the panel border
provides separation (dimming styled content line-by-line is fragile; explicitly out
of scope).

The settings search/help overlays may adopt `chrome.Overlay` later; migrating them
is out of scope here.

### 3. Picker component (`internal/app/tui/picker`)

```go
type Item struct {
    Label  string // primary text, left-aligned ("qwen-coder")
    Detail string // secondary text, right-aligned, fg.muted ("ollama/qwen2.5")
    Badge  string // optional colored tag ("● now" in accent.primary, "local" in status.info)
    Group  string // optional group header ("ollama"); items with the same Group cluster under it
    Value  string // opaque result handed back on pick
}

func New(title, footer string, items []Item) *Model
func (m *Model) SetFilter(q string)          // pre-filter (e.g. "/model qw" with no exact match)
func (m *Model) Update(tea.Msg) tea.Cmd      // emits PickedMsg / CancelledMsg
func (m *Model) View(maxW, maxH int) string  // panel via chrome.Panel

type PickedMsg struct{ Value string }
type CancelledMsg struct{}
```

Interaction (fzf-style, filter-first):

- Printable characters and backspace edit the filter; matching is `fuzzy.Rank` over
  `Group + " " + Label + " " + Detail`.
- `↑`/`↓` move the cursor over items (group headers are skipped — they are labels,
  not rows). `j`/`k` are **not** movement keys here; they belong to the filter.
- `Enter` emits `PickedMsg{Value}` of the cursor item; `Esc` emits `CancelledMsg`.
- `New` positions the cursor on the first item whose `Badge` starts with `●`
  (the "current" item) when one exists, else the first item — so `/model` opens
  on the active preset and `/rewind` (items newest-first) opens on the most
  recent turn.
- Cursor row: `▸` marker + `bg.selection`, same row treatment as the settings
  fieldList. Group headers render in `accent.primary`. Matched filter characters
  are not individually highlighted (keep it simple; the list is short).
- The list windows to the available height with `chrome.ClipLines`; the panel is
  `min(64, width-8)` wide and at most ~14 rows tall, centered.
- Empty filter result renders "no matches" in `fg.muted`; Enter does nothing.
- All colors via `theme` slots; usable under `NO_COLOR` (marker + layout carry it).

Footer (inside the panel, bottom line): `[↑↓] move [↵] pick [Esc] cancel` plus the
per-command note (e.g. "session only — /settings to persist").

### 4. TUI integration (`internal/app/tui/model.go`)

New model state: `pickerModel *picker.Model`, `pickerAction func(value string) tea.Cmd`.

- When `pickerModel != nil`, all key messages route to it (same precedence slot as
  the other overlays; settings/memory overlays take priority if somehow both are
  open, which command flow prevents).
- `picker.PickedMsg` → run `pickerAction(value)`, clear both fields, refresh
  viewport. `picker.CancelledMsg` → clear both fields, no message added.
- `View()`: when the picker is open, render the normal view first, then
  `chrome.Overlay(normalView, pickerView, width, height)`.
- The `?` help overlay and footer hints gain nothing — the picker carries its own
  footer, per the tui-design contextual-footer rule.

### 5. Command behavior

One shared rule: **bare command opens the picker; an argument that resolves exactly
keeps today's direct behavior; an argument that doesn't resolve opens the picker
pre-filtered with that text instead of erroring.**

- **`/model`** — items: every `cfg.Models.Presets` entry. `Group` = provider,
  `Label` = preset name, `Detail` = `provider/model`, `Badge` = `● now` when the
  preset equals `state.ActiveRoute().Preset`, plus `local` when `LocalOnly`.
  Groups and items sorted alphabetically. Picking runs the existing switch logic
  (synthetic "switched" profile via `configReloader`) — unchanged, session-only.
  Zero presets configured → no picker; system message pointing at `/settings`.
- **`/rewind`** — items: prior user turns, newest first. `Label` = `turn N`,
  `Detail` = first ~50 chars of the message. Picking runs the existing rewind
  logic for that turn. **Behavior change:** bare `/rewind` currently rewinds to
  the last turn immediately; it now opens the picker with the last turn
  pre-selected, so Enter-Enter reproduces the old behavior with a confirmation
  step. `/rewind 3` still rewinds directly.
- **`/branches`** — items: one per leaf, `Label` = `branch N`, `Detail` =
  `leaf <id>`, `Badge` = `● now` on the current leaf. Picking switches.
  **Behavior change:** bare `/branches` opens the picker instead of printing the
  text list (the picker shows the same information). `/branches 2` still switches
  directly.
- **`/mode`** (new command, registered in `internal/commands/commands.go`) —
  three fixed items: Ask, Edit, Auto, each with a one-line `Detail` describing the
  mode and `● now` on the active one (`m.forceMode`: "ask"/"edit"/"" for auto).
  Picking applies the same logic as the existing `/ask`/`/edit`/`/auto` handlers,
  which all remain registered and unchanged.

Implementation shape: the `executeCommand` switch cases build `[]picker.Item` +
set `pickerAction` to a closure over the existing case logic. No changes to the
`commands.Registry` contract (`Handler func(state, args) string`); pickers are a
TUI concern, consistent with "the TUI is responsible for rendering only" — the
action closures call the same state/services the switch cases already do.

### 6. Testing

- `picker`: filter narrows and re-ranks; group headers skipped by cursor; Enter
  emits `PickedMsg` with the right `Value`; Esc emits `CancelledMsg`; pre-filter
  via `SetFilter`; empty-result Enter is a no-op; windowing at small heights.
- `chrome`: `Overlay` splices at the right columns with styled (SGR-containing)
  background lines; `Panel`/`ClipLines`/`fuzzy.Rank` keep their settings tests
  green after extraction (moved, not rewritten).
- `tui/model_test.go`: bare `/model` opens the picker with the active preset
  badged; `/model <exact>` bypasses it; `/model zzz` opens pre-filtered; picking
  a preset calls `configReloader` with the synthetic profile; Esc leaves no
  system message; `/mode` marks the current mode; `/rewind` picker lists turns
  newest-first.

## Out of scope

- Persisting `/model` choices to config (settings TUI owns persistence).
- Background dimming, mouse support, argument completion in the `/` popup.
- Migrating settings search/help overlays to `chrome.Overlay`.
- Pickers for commands not listed (`/export`, `/rename`, …) — the component makes
  them cheap to add later.
