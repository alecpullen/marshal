# Settings TUI Redesign

**Date:** 2026-07-11
**Status:** Approved
**Scope:** `internal/app/tui/settings/` (plus minor touch points in `internal/app/tui/model.go`/`view.go`)

## Problem

The first-draft settings TUI works but is janky on four fronts, all confirmed as pain
points:

1. **Looks flat** — no borders or background layering, so there is no visual hierarchy
   and no way to see where focus is. `huh` forms and hand-rolled list editors render
   with mismatched styles in the same pane.
2. **Confusing focus/keys** — three stacked focus systems (sidebar/pane in `model.go`,
   `mixedPane`'s inner Tab cycle, `huh`'s own field focus) make Tab, Esc, and h/l
   behave differently depending on depth. Returning to the sidebar only works when a
   pane is "at first focus".
3. **Hard to find settings** — 15 sections, no search.
4. **Editing feels clunky** — add/edit/delete flows differ between list, map, and
   sub-pane collections.

## Design

Layout paradigm: **bordered two-pane** (persistent multi-panel, lazygit-style), per the
tui-design skill. All widget machinery is fair game; `huh` is removed from the settings
package (it remains available elsewhere in the app).

### 1. Layout & visual system

- The settings screen stays a centered takeover, framed at
  `min(width-4, 100) × min(height-2, 32)`.
- **Sidebar**: 18 columns, rounded border, title `Settings` (`Settings ●` with a
  `status.warning` dot when the working copy is dirty). One row per section; the
  selected row gets `bg.selection` + bold + `▸` marker.
- **Detail pane**: border titled with the section name (`─ Shell ─`). Content renders
  in a real viewport; a `↓ more` hint appears when clipped. Section warnings render
  under the title in `status.warning` with `⚠`.
- **Focus indication**: focused panel border = `accent.primary`; unfocused =
  `border.muted` with dimmed text.
- **Narrow terminals** (< 70 cols): sidebar hides, pane title becomes `‹ Shell ›`,
  `h`/`l` page between sections.
- All colors come from `internal/app/tui/theme` semantic slots. Monochrome (`NO_COLOR`)
  stays usable via the cursor marker and bold.

### 2. Focus & keybinding model

Two focus levels only — sidebar ↔ pane — plus one rule: **Esc always goes up one
level** (inline edit → field list → sidebar → close, with unsaved confirm at the top).
Inside a pane there is a single flat field list; the inner Tab cycle and the
`firstFocuser`/`AtFirstFocus` mechanism are deleted.

| Context | Keys |
|---|---|
| Everywhere | `Ctrl+S` save · `/` search · `?` help · `Esc` up one level |
| Sidebar | `j/k/↑↓` move · `g/G` first/last · `Enter/l/→/Tab` into pane |
| Pane | `j/k/↑↓` move fields · `Space` toggle · `Enter` edit/drill in · `h/←/Shift+Tab` back to sidebar (from any row) · `a/e/d` on list fields |
| Editing a value | `Enter` apply · `Esc` cancel |

Drilling into a complex entry (MCP server, preset) pushes a sub-pane; the pane title
becomes a breadcrumb (`MCP › github › headers`).

### 3. Unified field widget (`fieldList`)

One widget replaces `huh` forms, `listStrings`, `mapEditor`, `mixedPane`, and
`collectionPane`: a vertical list of typed rows, all rendered identically.

Row types:

- **toggle** — `label …… on ●` / `off ○`; `Space` flips.
- **scalar** (string/int/float, optionally masked) — value right-aligned; `Enter`
  turns the value cell into an inline `textinput`; validation errors render under the
  row in `status.error` and block apply.
- **enum** — `←/→` cycle values (on enum rows these keys are consumed by the row;
  only `h`/`Shift+Tab` return to the sidebar); `Enter` opens a small picker.
- **list** — summary row (`Denylist    3 items`); `Enter` drills into an entry list
  with `a` add, `e/Enter` edit, `d` delete.
- **entry** — one item of a collection; `Enter` pushes a sub-pane that is itself a
  `fieldList`.

Rendering: `▸` cursor + `bg.selection` highlight on the active row; labels left,
values right-aligned in `accent.secondary` (dimmed when the pane is unfocused). The
focused row shows a one-line description beneath it in `fg.muted`.

Collections (Providers, Presets, MCP, Hooks, Permissions) become `fieldList`s of
entries with drill-down sub-panes — same look and keys at every depth. Local-copy
editing semantics are preserved: sub-pane edits commit on apply, discard on Esc.

### 4. Global search

`/` from anywhere opens a centered bordered overlay over the dimmed frame: a text
input plus fuzzy-matched results shown as `Section › Field`, matched characters
highlighted in `accent.primary`. `Enter` jumps — selects the section, focuses the
pane, moves the cursor to the field. `Esc` dismisses.

Backing this is a field registry: each section contributes field metadata (section id,
field id, title, hidden keywords — e.g. "api key" matches masked provider fields).

### 5. Footer, help & save feedback

- **Context-sensitive footer** (one line inside the frame) shows only what is
  actionable now: sidebar `[↵]open [/]search [^S]save [Esc]close [?]help`; toggle row
  adds `[Space]toggle`; editing shows `[↵]apply [Esc]cancel`. Dirty state prefixes
  `● unsaved` in `status.warning`.
- **`?` help** is an overlay panel over the dimmed frame (not a screen replacement),
  listing the full keymap for the current context.
- **Save**: `Ctrl+S` → `✓ saved` in `status.success`, cleared on next keypress.
  Failures render in `status.error` and persist until acted on. Esc with unsaved
  changes keeps the double-Esc confirm, styled as a warning bar in the footer.

### 6. Architecture

```
model.go      — frame, two-level focus, overlay routing (search/help), footer
fieldlist.go  — the fieldList widget: navigation, editing, drill-down stack
field.go      — typed rows: toggle, scalar, enum, list, entry
search.go     — field registry + search overlay
section_*.go  — declarative field specs per section (mostly data, little logic)
state.go      — unchanged: working copy vs snapshot, dirty()
```

Deleted: `pane.go` (`sectionPane`, `firstFocuser`), `mixed.go`, `collection.go`,
`composite.go`, `scalar.go` (huh glue), `liststrings.go`, `mapeditor.go`,
`masked.go` (absorbed as a scalar row option), `huhtheme` usage in settings.

Unchanged public surface: `New(cfg, workingDir, projectCfgPath)`, `SetSize`,
`Update`/`View`, `SavedMsg`/`CancelledMsg`, `Footer()`, `FocusedFieldTitle()`,
`BoolValue()` (parent status line and tests depend on these).

### 7. Testing

Keep the package's existing string-assertion test style:

- `fieldList`: navigation, toggle, inline edit + validation, drill-down/Esc stack.
- `Model`: focus transitions, Esc-level rule, dirty/confirm flow, narrow-mode paging.
- Search: registry completeness (every section registers fields), fuzzy jump lands on
  the right section/field.
- Per-section tests rewritten against field specs; existing `BoolValue`/
  `FocusedFieldTitle` behaviors preserved.
- View assertions: focused vs unfocused border colors, footer content per context,
  `NO_COLOR` output contains no SGR sequences.

## Out of scope

- Any change to config loading/saving semantics (`config.SaveProjectConfig`,
  merge order).
- Theme palette changes beyond consuming existing slots.
- Mouse support.
