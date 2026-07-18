# Inline Interaction System — folding full-screen TUI views into the transcript flow

**Date:** 2026-07-19
**Status:** Approved design, pending implementation plan

## Problem

Marshal currently has four surfaces that take the user out of the conversation:

- `settingsOpen` — full-screen settings tree (16 sections, ~30 files under `internal/app/tui/settings/`)
- `memoryOpen` — full-screen knowledge/memory browser
- `connectOpen` — full-screen provider/model connect view (`/connect`, `/models`, Ctrl+P)
- `helpOpen` — full-screen help overlay (`?`)

Each is a modal *place you go*: the transcript disappears, spatial context is lost, and every screen has its own navigation idiom. Meanwhile the picker modal (`/model`, `/mode`, `/branches`, `/rewind`) and the inline approval/question components prove the app already supports lightweight, transcript-adjacent interaction.

## Goal

Zero full-screen takeovers. One consistent interaction system with three tiers:

1. **Transcript prints** — durable output (help cheatsheet, setting-change receipts, memory detail) rendered as styled blocks in the transcript, scrolling away naturally.
2. **Docked panels** — interactive surfaces (settings browser, connect, memory browser, pickers) rendered in a single slot *above the input area*, fzf/atuin style. The transcript stays visible above; the panel borrows rows from the transcript viewport.
3. **Command-first entry** — `/set key value` with rich autocomplete does most settings work without any panel at all.

The persistent footer and status line are unchanged.

## Decisions already made

| Question | Decision |
| --- | --- |
| Settings UX | Command-first (`/set`) + compact docked browser for discovery |
| Help UX | Transcript print only; footer keeps the essentials; no overlay |
| Scope | All four screens fold in (settings, help, connect, memory) |
| Panel placement | Docked above the input, not centered overlay |
| Architecture | Unified dock host (Approach A) — one panel slot, one interface, shared chrome |

## Architecture

### 1. `internal/app/tui/dock` — the panel host

A new package owning the "docked panel above the input" slot.

```go
// Panel is anything the dock can host.
type Panel interface {
    Update(tea.Msg) tea.Cmd
    // View renders at the given width, using at most maxHeight rows
    // (borders included). The dock clips if a panel misbehaves.
    View(width, maxHeight int) string
}

// CloseMsg is emitted by panels (or the host on Esc) to close the dock.
type CloseMsg struct{}
```

Host responsibilities:

- **Single slot.** Opening a panel while another is open replaces it. `tui.Model` holds one `dockPanel Panel` field (nil = closed), replacing `settingsOpen`, `memoryOpen`, `connectOpen`, `helpOpen`, and `pickerModel`/`pickerCommand`.
- **Height budget.** Panel gets `min(panelNaturalHeight, 40% of frame height)`, minimum 6 rows; the transcript viewport shrinks by the same amount while the dock is open. Input area, footer, and status line never move.
- **Width.** Panels span `min(72, width-2)`, left-aligned with the input box, mirroring the completion popup.
- **Chrome.** Reuse `chrome.Panel` (rounded border, title, theme-aware). `chrome.Overlay` centered placement is retired once all callers migrate.
- **Key routing.** When the dock is open, `tui.Model.Update` forwards key events to the panel first. Esc closes the dock if the panel did not consume it (panels with internal stacks — settings drill-down — consume Esc to pop a level, and emit `CloseMsg` at the root).
- **Focus.** The input textarea blurs while a dock panel is open; closing restores focus and any draft text.

The existing `picker.Model` becomes the first `Panel` implementation — it already has `Update(tea.Msg) tea.Cmd` and `View(maxW, maxH) string`, so this is an interface-conformance change plus placement, not a rewrite. `PickedMsg`/`CancelledMsg` handling in `model.go` is unchanged.

### 2. Settings registry — one flat source of truth

Extract the field definitions out of the screen-bound `frame` builders into a flat, screen-independent registry:

```go
// internal/app/tui/settings/registry.go (same package; reuses field, setters, validation)
type Registry struct { fields []*field /* keyed by field.id */ }

func BuildRegistry(cfg *config.Config) *Registry
func (r *Registry) Lookup(key string) (*field, bool)
func (r *Registry) Keys() []string                  // sorted dotted keys for completion
func (r *Registry) Match(query string) []*field      // fuzzy, reuses fuzzy.Rank
```

This formalizes what `search.go#buildRegistry` already does informally. Every leaf field keeps its canonical dotted id (`shell.allow_network`, `sandbox.backend`, …), kind (toggle/scalar/enum/secret/picker), description, options, validator, and masked flag. Collection frames (MCP servers, hooks, permissions, presets) remain drill-based and are reachable from the browser panel, not from `/set` (see Non-goals).

**Persistence model changes from transactional to immediate.** The full-screen settings view edited a working copy and saved on Ctrl+S. The new model: each applied change validates, mutates `state.Config`, saves via `config.SaveProjectConfig` immediately, and prints a receipt. `configdiff` is reused to render the old → new receipt text (it already handles secret masking via `isSecretPath`). A save failure prints an error receipt and leaves the in-memory value applied so the user can retry with `/set`.

Writes target the project-local `.marshal/config.toml`, matching current behavior. Global-config writes are out of scope.

### 3. `/set` command

| Invocation | Behavior |
| --- | --- |
| `/set` | Opens the docked settings browser (same as `/settings`) |
| `/set <partial>` | Opens the browser pre-filtered to the query |
| `/set <key>` (exact) | Prints the current value, type, and allowed values to the transcript |
| `/set <key> <value>` | Validates via the registry, applies, saves, prints a receipt |

Receipts are single styled transcript lines:

```
✓ shell.allow_network: off → on · .marshal/config.toml
✗ agent.max_turns: "abc" — must be an integer ≥ 1
```

**Completion.** The completion popup (`completions.go`) gains two context-aware sources: after `/set ` it completes registry keys (fuzzy, showing current value as the detail column); after an exact key it completes values for toggles (`on`/`off`) and enums (from `options()`). Scalars get no value completion.

**Command handler plumbing.** `commands.Handler` returns a string today; opening a panel needs a side effect. `/set`, `/settings`, `/connect`, `/memory` follow the same pattern the picker commands already use: intercepted in `tui.Model` before generic dispatch (or via a typed command-result message if that seam already exists when implementation starts — the plan should pick whichever matches `model.go`'s current interception style).

### 4. Docked settings browser

A new `settings.BrowserPanel` implementing `dock.Panel`, replacing the full-screen `settings.Model`:

- **Default view: search-first flat list.** A filter input at top (like the picker), listing matching registry fields as `key · current value` rows. Empty query shows sections as group headers (picker's `Group` rendering).
- **Editing in place.** Enter on a row activates its editor inside the panel: toggles flip on Space/Enter; enums show their options as a temporary sub-list; scalars/secrets open a one-line text input under the row (reusing `fieldList`'s inline-edit and `masked.go` behavior). Every apply saves immediately and prints a receipt to the transcript — the panel stays open for further edits.
- **Collections drill down.** Entering a collection row (MCP servers, hooks, permissions, presets) pushes the existing `frame`/`fieldList` machinery onto a stack inside the panel (reusing `panestack.go` logic at dock size). Esc pops; breadcrumb shows in the panel title (`Settings › MCP › github`).
- **Reset-to-defaults and delete idioms** (`d`, confirm-on-second-press) carry over from `fieldlist.go` unchanged.

What gets deleted: the full-screen chrome (`chrome.go`, the 3-pane layout in `model.go`, `help.go` inside settings, `sections.go`'s pane-stack wiring, the Ctrl+S transaction and `SavedMsg` flow, `search.go`'s overlay). What survives: `field.go`, `setters.go`, `validation.go`, `masked.go`, `fieldlist.go`, `configdiff.go`, the `frames_*.go` field definitions (now feeding the registry), and their tests.

### 5. Help → transcript print

- `/help` prints a styled cheatsheet block into the transcript: essential keys, then commands grouped by area, using theme colors and the two-column alignment already used in the footer. `/help <command>` prints that command's detail (description, args, examples).
- `?` on an empty input triggers the same print (with text in the input, `?` is just a character, as today).
- `help.Overlay` and `helpOpen` are deleted. `help.Rows`/footer rendering is untouched.

### 6. Connect → docked panel

`connect.Model` gains the `dock.Panel` interface and renders at dock width/height instead of centered full-frame. Its internal flow (provider list → auth → model list) is unchanged; `connectOpen` and its `lipgloss.Place` branch are deleted. Ctrl+P and `/connect`/`/models` open it in the dock.

### 7. Memory → docked browser + transcript prints

- `/memory` opens a docked, fuzzy-searchable list of memory entries (picker-style rows: title, age, kind badge).
- Enter on an entry **prints the full entry into the transcript** as a styled block and closes the panel — reading is a transcript activity, not a panel activity.
- `d` on an entry deletes with the confirm-on-second-press idiom; a receipt prints to the transcript.
- The full-screen `memory.Model` browsing chrome is deleted; its data-access layer is reused by the panel.

### 8. `tui.Model` and `view.go` simplification

`viewString()` loses all four full-screen branches. The frame becomes one unconditional row stack:

```
title bar
transcript (shrinks while dock is open)
[swarm panel] [browser bar] [sdd panel]
[dock panel]          ← the only conditional interactive layer
input area
help footer
status line
```

`chrome.Overlay` (center-compositing) is removed with its last caller. Four booleans + two picker fields collapse into `dockPanel Panel`.

## Error handling

- **Validation:** field validators run before any write; failures render inline under the field (panel) or as a `✗` receipt (`/set`). No partial writes.
- **Save failures** (permissions, disk): `✗` receipt with the OS error; in-memory config keeps the applied value.
- **Terminal too small:** the existing `minTerminalWidth/Height` gate is unchanged; the dock's 40% cap plus 6-row minimum keeps panels usable at 80×24 (≈9 panel rows available).
- **NO_COLOR / 16-color:** panels and receipts use theme slots only; `✓`/`✗` glyphs carry meaning without color. Existing `nocolor_test.go` coverage extends to the new panel.

## Testing

- **Registry:** unique keys across all sections; every field reachable from the old frames appears in the registry (parity test walks `sectionSpecs` and asserts count/ids match); table tests for setters/validators (existing `setters_test.go`, `validation_test.go` carry over).
- **`/set`:** parse table (`/set`, `/set partial`, `/set key`, `/set key value`, bad key, bad value); receipt golden strings; completion source tests (key completion after `/set `, value completion for enum/toggle).
- **Dock host:** height budget at 80×24 / 120×40; replace-on-open; Esc close vs. panel-consumed Esc; input blur/restore with draft preserved.
- **Panels:** settings browser filter/edit/drill/receipt flow; connect and memory smoke tests at dock size (pattern from `smoke_pickers_test.go`).
- **View:** `view_test.go` updated — no full-screen branches; dock row appears between transcript and input; transcript height shrinks correctly.

## Migration order

Each step lands green and is independently shippable:

1. **Dock host + picker rehosted.** New `dock` package; picker moves from centered `chrome.Overlay` to the dock. Full-screen views untouched.
2. **Settings registry + `/set` + receipts.** Registry extracted from frames; `/set` with completion ships while the full-screen settings view still exists (both write through the same setters).
3. **Docked settings browser.** `BrowserPanel` ships; `/settings` and `/set` (no args) open it; full-screen settings view and its chrome deleted.
4. **Help.** `/help` and `?` print to transcript; `help.Overlay` deleted.
5. **Connect.** Rehosted in dock; `connectOpen` deleted.
6. **Memory.** Docked browser + transcript prints; full-screen memory view deleted.
7. **Cleanup.** Remove `chrome.Overlay`, dead `Open` booleans, `lipgloss.Place` full-frame branches; final `view_test.go` pass.

## Non-goals

- Writing to the global `~/.config/marshal/config.toml` from `/set` (project-local only, matching today).
- `/set` addressing into collections (`mcp.servers.github.url`) — collections are edited via the browser's drill-down. May follow later once the registry proves out.
- A general command palette / omnibox (Approach C — explicitly rejected for now).
- Theming changes, footer changes, or transcript rendering changes beyond the new receipt/cheatsheet block styles.
