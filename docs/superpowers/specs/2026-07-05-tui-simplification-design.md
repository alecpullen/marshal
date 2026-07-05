# TUI Simplification Design

**Date**: 2026-07-05
**Author**: Alec Pullen
**Status**: Implemented

## Summary

Replace Marshal's busy two-column dashboard with a single-column, transcript-centric TUI in the spirit of Claude Code (quiet, scannable, everything inline) with an Opencode-style status line. The right sidebar, top bar, panel borders, state strip, and key-help line are removed. Their information moves inline into the transcript or behind on-demand commands.

## Motivation

The current TUI shows too much persistent chrome at once:

- Top bar: decorative window dots, centered title, Ask/Plan/Auto/Swarm mode strip.
- Left column: bordered "Chat" panel with title/meta labels, a full-width colored state strip, a bordered input, a key-help line.
- Right sidebar (~1/3 of the width): bordered "inspector" with pill tabs (1 Plan / 2 Context / 3 Log).
- Bottom status bar: brand cell, mode, role, model@provider, locality, busy cell.

Almost none of it needs to be permanently visible. The plan matters when it changes; the context pack and tool log matter when asked for; agent state matters as one line, not three widgets.

## Relationship to in-flight work

This design **builds on top of** the two specs from earlier today, which are kept and must be committed before this work starts:

- `2026-07-05-inline-approval-and-state-indication-design.md` — inline approval panel in the transcript, live tool-call blocks with elapsed time, state-polling architecture (150ms tick, all state via `session.State`).
- `2026-07-05-rich-content-rendering-design.md` — content-type renderers (markdown, plan, diff, tool result, final answer).

This spec restyles those elements and removes the surrounding chrome; it does not change their architecture. The TUI remains rendering-only (CLAUDE.md constraint) and state-polled — no new channels or `tea.Msg` types.

## Design

### 1. Layout — single column

```
  ❯ fix the failing TUI layout tests

  ⏺ shell.run(go test ./internal/app/tui)
    ⎿ FAIL: TestLayoutBounds — line 17 exceeds width 80

  I found the render drift: geometry is recomputed during
  View. Patching the resize path now.

  ╭──────────────────────────────────────────────────────────╮
  │ ❯ Ask Marshal...                                          │
  ╰──────────────────────────────────────────────────────────╯
  auto · qwen2.5-coder:14b @ ollama · local · ctx 18k/32k   ⠋ go test · 4s
```

Top to bottom:

1. **Transcript** — borderless, full terminal width, scrollable viewport. No panel title, no border. Inline live blocks (active tool call, approval panel, streaming thinking box) render at the bottom of the viewport content, as the in-flight approval spec already defines.
2. **Input** — bordered box with `❯ ` prompt. Always focused. This is the only bordered element in the idle UI.
3. **Status line** — one row, subtle background (Opencode style):
   - Left cluster: `mode · model @ provider · locality · ctx <used>/<max>` (ctx segment shown only when a context pack exists).
   - Right cluster (right-aligned): current activity — `⠋ thinking`, `⠋ <tool label> · <elapsed>`, `⚠ approval`, a `✓ <label>` flash for ~2s after completion (existing `doneDisplayDuration` behavior), or empty when idle.

**Removed:** top bar (dots, title, mode strip), right sidebar and pill tabs, chat panel border/title/meta, colored state strip (its content folds into the status line's right cluster; the inline blocks carry the detail), key-help line (discoverability via `/help`), the `MARSHAL` brand cell and role cell of the old status bar (role is visible in `model @ provider` context and via `/route`).

**Geometry:** two-column math (`leftWidth`/`rightWidth`, `minPanelWidth`, `totalHorizontalBorderGutter`) is deleted. New geometry: viewport width = terminal width minus small horizontal padding; viewport height = terminal height − input box height (3) − status line (1).

### 2. Transcript rendering

Symbol-bullet style, no role gutter. The `user/agent/tool/output` right-aligned colored labels are deleted.

- **User message:** `❯ ` prefix in accent color, message text bold-ish/normal. Wrapped continuation lines indent to align.
- **Agent prose:** plain rendered markdown, no prefix, no label. The final answer keeps its distinct rendering from the rich-content spec.
- **Tool call/result:** `⏺ tool(args-summary)` line, then a dim `⎿ result-summary` line. Collapsed by default — full output lives in the audit log, viewable with `/log`.
- **Plan:** when the agent produces a plan, it renders inline as a `⏺ Plan` block listing the steps (Claude Code todo-list style). Re-planning renders a new block. No persistent plan panel.
- **Thinking:** unchanged from in-flight spec — collapsed `⚙ thought for Ns` line, Ctrl+G toggles expansion; live streaming box while thinking.
- **Approval panel:** kept from in-flight spec, restyled to match the new look. It keeps its warning-colored border — deliberately the loudest element on screen.
- **System/notice messages:** dim, prefixed `· ` (e.g. command output, "Agent turn cancelled.").
- **Provider errors:** styled inline error message in the transcript (`✗ provider: <error>`) plus an error state in the status line — the current behavior of replacing the entire chat panel is removed.

### 3. Sidebar replacement — on-demand commands

- `/context` (existing command — extend as needed): prints a context-pack snapshot into the transcript: section list with per-section token counts and the `ctx used/max` totals.
- `/log` (new command): prints the last 15 audit-log events (time, tool, result summary) into the transcript.
- Plan needs no command: it renders inline when created (Section 2).

**Key changes:** `1/2/3` tab keys and Tab focus-cycling are removed — there are no panels to focus. Input is always focused; PgUp/PgDn and mouse wheel scroll the transcript viewport. The `r` rollback key is removed; the existing `/rollback` command covers it. Esc cancels an in-flight agent turn (same as `/stop`) and does nothing when idle; Ctrl+C quits. Unchanged: Ctrl+O settings overlay, Ctrl+K memory overlay, approval keys (Enter/d/e/a) while an approval is pending.

### 4. Code restructure

`internal/app/tui/model.go` is 1,351 lines doing layout, keys, commands, and rendering. Removing the sidebar is the moment to split it:

| File | Responsibility |
|---|---|
| `model.go` | `Model` struct, `Init`/`Update`, key handling, command dispatch, overlay switching (slimmed) |
| `view.go` | `View()`, single-column geometry, input area, fallback view |
| `transcript.go` | all message/content renderers, live blocks (absorbs `renderers.go`) |
| `status.go` | status line rendering |

Deleted code: `renderRightInfoPanel`, `renderSidebarTabs`, `renderPlanTab`, `renderContextTab`, `renderLogTab` (tab logic becomes `/context` and `/log` output), `renderModeStrip`, `renderStateStrip`, `renderKeyHelp`, the top bar, pill styles/`padPillHeight`, `renderPanel` (unless the approval block still uses it), and the two-column geometry fields.

Overlays (`settings/`, `memory/`) are untouched.

### 5. Behavior, error handling, testing

**Preserved behavior:** approval flow and keys, busy gating (one turn at a time), `/stop` cancellation (now also on Esc), streaming thinking, `✓ done` flash, notifications, `/rollback`, all registered commands.

**Testing:**
- Adapt existing layout/model tests to single-column geometry (no `leftWidth`/`rightWidth`).
- Renderer substring tests: user `❯ ` prefix, `⏺`/`⎿` tool lines, plan block, status line clusters (left content, right activity states), inline provider error.
- Command tests: `/log` prints audit events; `/context` prints pack sections.
- Bounds test: every rendered line fits the terminal width at small sizes (e.g. 80×24, 40×10 minimums).

## Out of scope

- Theming/config for colors (single built-in theme for now).
- Swarm activity panel (Phase 5 roadmap item).
- Any change to agent, session, or command-registry architecture beyond adding `/log`.

## Decisions log

1. Sidebar info → on-demand, Claude Code style (plan inline; `/context`, `/log` commands).
2. Builds on top of the in-flight inline-approval and rich-rendering work; commit that first.
3. Chrome → borderless transcript + bordered input + one slim status line; everything else removed.
4. Transcript → symbol bullets (`❯`, `⏺`, `⎿`), plain markdown prose, no role gutter.
5. Implementation → redesign + split `model.go` into `model.go`/`view.go`/`transcript.go`/`status.go`.
