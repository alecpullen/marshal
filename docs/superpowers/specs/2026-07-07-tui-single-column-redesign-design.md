# TUI Single-Column Redesign

**Date:** 2026-07-07
**Status:** Approved (design)

## Problem

The current TUI renders panels with solid background fills (`panelBgColor`,
`statusBarBgColor`). Because the Bubbles `textarea` pads short lines with raw,
unstyled spaces and closes SGR sequences before that pad, the fill color cannot
reach the panel borders. `view.go` works around this with `fillRowsToWidth`,
`contentWidth`, `inputFillStyle`, and a long explanatory comment. The result is
fragile and visibly broken: backgrounds do not fill their boxes evenly (see the
hollow/uneven fills in the reported screenshot).

## Goal

A simpler, single stacked-column TUI where colors render correctly by
construction, with a stylish warm palette and universal-Unicode iconography.

## Core principle

**No background fills anywhere.** Every panel is styled by *border color* and
*foreground/text color* only; the terminal's own background shows through. This
removes the entire class of "fill does not reach the border" bugs by deleting
the machinery that caused them.

This is a purely presentational change. No model logic, update loop, state, or
key handling changes.

## Layout (top → bottom, single column)

```
● marshal · local-first coding agent          header line (no box)
                                               (blank)
› you                                          transcript: borderless scrollback
  refactor the parser

● marshal
  I'll start by reading parser.go

  ⚙ read   parser.go · 240 lines
  ⚙ edit   parser.go · +18 −14  ✔

╭──────────────────────────────────────────╮  input: the ONLY bordered box
│ ❯ Ask Marshal…                            │  (rounded; coral border when focused)
╰──────────────────────────────────────────╯
 auto · deepseek-v4-flash @ go · local · ctx 4k/128k     ⣾ thinking   status row (no box)
```

## Palette — "Warm Sunset" (256-color)

| Role / state       | Icon | Color |
|--------------------|------|-------|
| you (user)         | `›`  | warm gray `246` |
| marshal            | `●`  | coral `209` |
| tool call          | `⚙`  | gold `214` |
| success            | `✔`  | teal `43` |
| warning / risk     | `⚠`  | orange `172` |
| error              | `✘`  | red `203` |
| border (focused)   |      | coral `209` |
| border (blurred)   |      | mauve `245` |
| dim / meta text    |      | gray `244` |

All icons are universal Unicode symbols (`● ⚙ ✔ ✘ ⚠ ❯ › ▸ ▾ ─ ·`) that render
in essentially every terminal and font — no Nerd Font dependency.

## Components

### 1. Header
One line, no box: coral `●` + `marshal` (bold) + dim ` · local-first coding
agent`. Replaces the current bordered welcome banner. Rendered once at the top
of the column.

### 2. Transcript
Borderless viewport (keep the existing `viewport` and scroll behavior). Only the
per-message rendering in `transcript.go` changes:
- **Role header** per message: icon + role name in the role color
  (`› you` in warm gray, `● marshal` in coral).
- **Body** indented two spaces under the header.
- **Tool activity**: compact line `⚙ <name>   <detail>  <✔|✘>` — gold icon/name,
  dim detail, teal/red status glyph on completion.
- **Thinking**: dim gray, italic.
- No background fill; foreground colors only.

### 3. Input
The single rounded-border box (`lipgloss.RoundedBorder()`), border coral `209`
when focused and mauve `245` when blurred, **no** `Background`. `❯` prompt in
coral. The activity strip and command-suggestion rows render inside the box as
plain unfilled rows (foreground styling only). The textarea keeps no background
style, so no fill padding is needed.

### 4. Approval panel
Reuses the input box. Risk/state surfaced with colored text and glyphs
(`⚠` orange for risky, `✘` red for blocked); no background fill. The editing
(`❯`) path is unchanged in behavior.

### 5. Status row
One borderless line (drop `statusBarBgColor` fill), rendered on terminal bg:
- **Left cluster**: `mode · model @ provider · local · ctx x/y` in dim gray,
  `·` separators. Swarm token count (`tokens x/y`) folds in as today.
- **Right cluster**: live activity in the state color — `⣾ thinking`,
  `⚙ <tool> · <elapsed>`, `✔ <done>`, `⚠ approval`, `✘ error`.
- The existing left/right justification and narrow-terminal truncation logic in
  `status.go` is kept; only the background fill and colors change.

### 6. Swarm panel
Rendered borderless (foreground styling only) or folded into the status row's
left cluster. Keeps its token-usage content; loses any box/fill.

## Scope

**Rewrite / modify:**
- `internal/app/tui/view.go` — remove `fillRowsToWidth`, `contentWidth`,
  `inputFillStyle` usage, and the textarea-pad workaround; borderless transcript
  and header; input box with no background.
- `internal/app/tui/model.go` — replace the palette/style block with the Warm
  Sunset palette; remove `panelBgColor`/`statusBarBgColor` fills and the
  textarea `Background(panelBgColor)` styles.
- `internal/app/tui/transcript.go` — new per-message icon/role rendering.
- `internal/app/tui/status.go` — drop background fill; apply new colors.
- `internal/app/tui/swarm_panel.go` — borderless, or fold into status.
- Header/welcome banner rendering.

**Unchanged:**
- All model logic, `Update`, `session.State`, key handling, command routing.

**Tests:** update assertions in `view_test.go`, `status_test.go`,
`transcript_test.go`, `swarm_panel_test.go`, and any `model_test.go` cases that
assert on `panelBgColor` / fill behavior to assert the new borderless structure
(border colors, icon prefixes, absence of background fill).

## Non-goals

- No changes to agent behavior, providers, or tools.
- No Nerd Font iconography.
- No new panels or features beyond the visual restructure.
