# Hairline Gutter TUI Redesign ("Style B")

**Date:** 2026-07-19
**Status:** Approved direction, pending implementation plan
**Runnable mockup:** `docs/mockups/hairline-gutter-mockups.go` (`go run` it to see all styles in the Warm Sunset palette)

## Goal

Strip the TUI down to a single structural device — a one-column hairline
gutter — and delete every other piece of chrome that does not carry
information: the title bar, the input border box, the footer rule, the
separate hint row, panel borders, code-block borders, and in-transcript
labels. Idle chrome drops from 7 rows to 2. The transcript is the
interface; chrome exists only where state cannot live in content.

This was chosen over two alternatives that were mocked up alongside it:
"bare prose" (no gutter at all — rejected because long tool-heavy
sessions lose scannability) and "single line" (status merged into the
input row — rejected as too aggressive a discoverability cut).

## Design language

Three devices replace every border and label in the UI:

1. **The gutter** — column 2 of the frame (column 1 is a space). One
   glyph per line encodes who is speaking and what state the line is in.
2. **Background tint** — `bg.surface` replaces boxes for block content
   (code, diffs). Depth comes from background layering, not borders.
3. **Glyph + color state** — where a border color used to signal state
   (input focus, approval pending), a single glyph's color does it now.

### Gutter vocabulary

| Gutter | Color | Meaning |
| ------ | ----- | ------- |
| `❯` | AccentPrimary (coral), bold | User turn (echo in transcript, and the input prompt) |
| `·` | FGMuted | Tool event, system notice, state-change event |
| `▍` | AccentPrimary (coral) | Agent answer block (replaces `Response` label + left border) |
| `▍` | FGMuted | Expanded thinking block |
| `▍` | AccentSecondary (violet) | Focused dock panel (settings, picker, memory, connect) |
| `▍` | FGMuted | Unfocused dock panel |
| `✗` | StatusError | Failed tool call, provider error |
| `⚠` | StatusWarning | Pending approval line |
| `?` | AccentSecondary (violet) | Pending agent question |
| `✓` | StatusSuccess | Completed-with-result emphasis (sparingly; most completions are `·`) |

Rules:

- The gutter never exceeds one column. No double glyphs except the input
  row's `▍❯`.
- Color is never the only signal: the glyph itself distinguishes every
  case in NO_COLOR/monochrome mode (this is stronger than the current
  border-based mono story).
- Scanning the gutter column top to bottom must tell the story of the
  session: what I asked, what it ran, what failed, what it answered.

### Chrome after the redesign

```text
[transcript ......................................]  ← full height
[optional: live strip (swarm / sdd)]                 ← 0–1 rows, only while running
[optional: dock panel]                                ← only while open
 ▍❯ input                                             ← 1+ rows (grows with input)
  status-left ................. contextual hints      ← 1 row
```

Idle chrome: 2 rows (input + status). Removed entirely: title bar (1),
input border (2), footer rule (1), dedicated hint row (1).

## Per-widget mapping

### Frame chrome

| Widget | Today | Style B |
| ------ | ----- | ------- |
| Title bar (`renderTitleBar`) | 1 row: `● marshal` + dir/branch | **Deleted.** Dir joins status-line segments; branch segment already exists there. Brand appears only in the welcome banner. |
| Welcome banner (`renderWelcomeBanner`) | `chrome.Panel` box card | De-boxed: 2–3 plain lines printed into the transcript at startup (`● marshal · local-first coding agent`, dim CTA line). |
| Input area (`renderInputArea`) | Rounded box, border color = state (coral focus / mauve blur / teal pulse) | No box. Prompt is `▍❯`; the `▍` bar color carries the old border-color state: coral focused, muted unfocused, teal success pulse, warning approval, violet question. Multiline input continuation rows get a bare `▍`. |
| Footer rule + hint row (`renderHelpFooter`) | Full-width `─` rule + 1 hint row | **Deleted.** Hints merge into the right side of the status line (see below). `?` still prints the full cheatsheet to the transcript. |
| Status line (`renderStatusLine`) | Left segments + right activity | Keeps its collapse-by-priority logic. Right cluster becomes: contextual hints when idle (`Tab mode · / cmd · ? help`), activity/approval state when busy (unchanged). Mode-specific hint swaps reuse the `help.FooterHints` logic. |
| Activity strip | Inside the input box, duplicating the status-line right cluster | **Deleted.** Activity lives in the status-line right cluster only; the in-transcript active tool line already carries the spinner and elapsed time. |
| Completion popup | Inside the input box | Unchanged rows, rendered bare above the input row (no enclosing box). `▸` selection marker stays. |
| Scroll hint (`↑ scrolled — End to follow`) | Above viewport | Unchanged (already flat). |

### Transcript widgets

| Widget | Today | Style B |
| ------ | ----- | ------- |
| User message (`renderUserMessage`) | `›`-prefixed | `❯` coral gutter, user text in UserPrompt color. Continuation lines indent under the gutter. |
| Agent prose (`renderAgentMarkdown`) | Glamour markdown, 2-space margin | Unchanged (interim prose has no gutter; only final answers get the bar). |
| Final answer (`renderFinalAnswer`) | `Response` label + `│` left border | **Label deleted.** Coral `▍` gutter down the block. Salvage note becomes a dim first line inside the gutter. |
| Thinking, live (`renderThinkingBox`) | Spinner header + tail lines | Same content, `·` gutter on the header, muted `▍` on tail lines. |
| Thinking, summary (`renderThinkingSummary`) | `⚙ thought for Ns` line; expanded = indented block | Collapsed line unchanged. Expanded block gets muted `▍` gutter instead of bare indent. |
| Active tool call (`renderActiveToolCall`) | Spinner bullet + `$ cmd` + sandbox lines | Gutter `·` + spinner label. `$` and sandbox lines keep their dim indent under the gutter. |
| Completed tool call (`renderCompletedToolCall`) | `✔/✘ name done/failed` + summary line | Gutter `·` (success) or `✗` (failure): `· patch.apply config.go +12 -3 · hook rewrote`. Summary folds onto the head line when it fits; wraps indented when not. The word `done` is deleted (success is the unmarked case). |
| Tool result (`renderToolResultLine`) | `⏺` + `⎿` bullets | Gutter `·`, continuation lines dim-indented. `⏺/⎿` glyphs retired. |
| Code block (`renderCodeBlock`) | Rounded border | `bg.surface` tint, padded to a fixed inner width, indented. No border. Same treatment inside answers and diffs. |
| Plan block (`renderPlanBlock`) | `⏺ Plan` header | Gutter `·` + dim `plan` prefix word, body indented under it. |
| System notice (`renderSystemNotice`) | `·`-prefixed dim | Already style B. Becomes the shared treatment for state-change events (model/mode switches print here). |
| Provider error (`renderProviderError`) | Bold red block | `✗` gutter, first line red, detail lines dim. |
| Queued messages (`renderQueuedMessages`) | `Queued (Ctrl+X to clear):` header + `›` lines | Header deleted. Each line: dim `·` gutter + `queued: <text>`; hint moves to status-line right cluster while queue is non-empty. |
| Todos (`renderTodos`) | Checklist block | `·`/`✓` gutter per item, active item bold. No header. |

### Interaction widgets

| Widget | Today | Style B |
| ------ | ----- | ------- |
| Approval (`approvalModel` / `renderApprovalPanel`) | huh vertical form (5–6 rows) inside warning-bordered input box, `⚠ Approval needed` title | Flat, 2–3 lines, no box: line 1 `⚠` gutter + command/tool + risk + sandbox (`⚠ rm -rf build/ · shell · high risk · sandboxed`); line 2 horizontal selector `▸ allow  always  session  edit  deny  [rollback]` (selected bold coral, rest dim). **Logic unchanged:** same choices, two-step arm/submit, double-Esc deny, `e` edit flow. Patch approvals keep the diffview preview, rendered as a tinted block above the selector. |
| Question (`questionModel` / `renderQuestionPanel`) | `Marshal asks:` title + bullets + faint hint | `?` violet gutter per question line, no title. Answer input is the normal input row with the `▍` bar in violet. |
| Swarm panel (`renderSwarmPanel`) | Persistent multi-row panel (goal + per-role rows + tokens) | **Panel deleted.** (a) Live strip above input, 1 row: `⠹ swarm 3/5 · coder — reviewing diff`; (b) role completions print transcript events (`· architect done · 12k tokens`); (c) token totals stay a status-line segment (already exist). |
| SDD panel (`renderSDDPanel`) | Persistent phase/task panel | Same treatment: 1-row live strip (`⠹ sdd task 3/7 · implement parser`), phase transitions print `·` events, `task n/m` stays a status segment (already exists). |
| Browser bar (`renderBrowserBar`) | Dedicated row | Folds into the live strip / status segment (URL segment already exists in status). Dedicated row deleted. |

### Dock panels — `chrome.Panel` rewrite

All dock panels (settings browser, picker, memory, connect) and the
welcome card render through `chrome.Panel(title, content, w, h, focused,
theme)`. The signature and all call sites are unchanged; only the body
is rewritten:

- Rounded box → `▍` gutter column on every content row.
- Title-in-border → bold header line inside the gutter (breadcrumbs like
  `settings › shell` render there; panestack logic untouched).
- `focused` → gutter color (violet focused, muted unfocused) instead of
  border color.
- Height: no top/bottom border rows, so panels gain 2 content rows
  within the same `dock.MaxRows` budget. `chrome.ClipLines` unchanged.
- Width: no right edge. Content is truncated to width but no longer
  padded out to a box edge; usable width grows by ~4 columns.
- Key-hint footers (`12 settings · [↵] edit · [Esc] close`): keys move
  right-aligned onto the header line; counts stay as a trailing dim line
  only where load-bearing.
- The settings browser's internal `─` rule under the filter input is
  deleted (the gutter frames the panel; the divider is redundant).
- huh-rendered bodies (inline editors) inherit the new frame; the
  `huhtheme` selection styles get a consistency pass only.

## Height accounting

`view.go` constants change: `titleBarRows` 1→0, `inputBorderRows` 2→0,
`footerRows`/`commandBarRows` collapse into `statusLineRows` (1).
`model.go`'s input-height math (currently `rows := inputBorderRows`)
follows. The viewport gains 5 rows at idle. All row-budget logic keyed
off these constants must be audited in the same change — no hardcoded
copies of the old values may survive.

## Degradation and accessibility

- **NO_COLOR / monochrome:** every gutter case is glyph-distinct
  (`❯ · ▍ ✗ ⚠ ?`), so meaning survives with zero color. This replaces
  `chrome.Panel`'s special mono branch.
- **16-color terminals:** the theme layer already maps slots; the gutter
  uses only themed slots, no raw values.
- **Glyph safety:** `▍` (U+258D), `❯`, `·`, `⚠`, `✗`, `✓`, `?` — all
  single-width, in the box-drawing/blocks/common-punctuation ranges that
  render on Windows Terminal, tmux, and zellij. No new emoji beyond the
  existing `🌐` (which stays only in the status segment).
- **Copy/paste:** with no right borders, selecting transcript text no
  longer captures box edges — a small quality win for a mouse-selection
  TUI. The 2-column gutter prefix is still captured; acceptable and
  unchanged from the current `│` border behavior.

## Non-goals

- No change to keybindings, modes, or interaction logic (approval
  semantics, question flow, completion behavior are re-rendered, not
  redesigned).
- No theme/palette changes; all colors come from existing slots.
- Not adopting style C (status stays its own row; hints stay visible).
- No changes to ACP, agent runtime, or session state shapes. The only
  session-facing change is *when* swarm/SDD progress is surfaced
  (events vs. panel), not the progress data itself.

## Phasing

Three independently shippable phases, riskiest-last:

1. **Chrome & dock** — delete title bar, footer rule, and hint row;
   merge hints into the status line; de-box the input area (state moves
   to the `▍❯` prompt); rewrite `chrome.Panel` (settings, picker,
   memory, connect, welcome come along for free); update height
   constants. Pure-render change, no logic.
2. **Transcript widgets** — gutter treatments for user/answer/thinking/
   tool/plan/error/queued/todos; code-block tint; approval and question
   flat rendering (render-only rewrite of `approvalModel.View` /
   `questionModel.View`).
3. **Panels → events** — replace swarm/SDD persistent panels and the
   browser bar with the live strip + transcript events. Touches
   event-emission points, not just rendering; smallest visual surface
   but the only phase with non-render changes.

Each phase updates the view tests it breaks (tests asserting `Response`,
box glyphs, footer content, and panel borders) in the same change.
