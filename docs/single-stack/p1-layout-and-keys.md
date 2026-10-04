# Single Stack P1 — Layout and Keys

Parent: `feature-spec.md` (feature spec, §5.1, §5.6–5.8, §5.11, §6.5–6.6)
Depends on: nothing. TUI only, with no session, agent or DB schema change.

## Goal

Ship the single-column layout and the safer keybindings using today's data.
Afterwards the screen is one column at every width, all live progress sits
in one now bar above the input, the rail's information is available on
demand, and neither Esc nor a single Ctrl+C can end work or the program.

## In scope

1. Remove the side rail and its layout code. Move the section renderers to
   a new `sessionsheet` package.
2. Session sheet: a docked panel on Ctrl+B.
3. Now bar: replaces the run bar, turn spinner, todo panel, live strip and
   activity lane.
4. Tasks panel on Ctrl+T, replacing the todo panel's expand/collapse cycle.
5. Status line: add a `±N files` segment, a Ctrl+C armed state, and updated
   hints.
6. Esc no longer cancels a turn. Ctrl+C needs a double press for both
   stopping a turn and quitting.
7. Remove the agent-lane keyboard cursor (F6). Its job is covered by
   Ctrl+F and, in P3, browse mode.
8. `[tui.side_panel]` is deprecated with a config diagnostic.

## Out of scope

- Steps, owners and any transcript restructuring (P2).
- Browse mode. In P1, Esc only closes things (P3 binds browse mode).
- Density ladder and inspector (P3).

## Behaviour

### B1. Layout

- `viewString` stacks, top to bottom: transcript frame, now bar, dock
  view, input area, status line.
  - There is no top bar and no `JoinHorizontal`.
  - `FullFrame` dock panels behave as today.
- `leftWidth == width` always. `railWidth` and `railHidden` are deleted.
- `resize()` computes the viewport height as
  `height - scrollHint - breadcrumb - nowBarRows - dockRows - inputAreaRows - statusLineRows`.
- `clipLeftColumn` stays as a safety net. The frame-invariant tests must
  pass with no clipping in every state they cover.

### B2. Now bar

The now bar is built by a pure plan function. `nowBarRows()` returns
`len(plan.rows)` from the same plan, so rendering and measuring cannot
disagree.

Row selection, in order:

1. **Progress row.** Only one source applies:
   - An SDD run is active or finished but not yet cleared: the existing
     `runPanelSummaryLine` / `runPanelFinishedLine` text, prefixed with the
     progress blocks `▰…▱`. Blocks are `DoneTasks/TotalTasks` scaled to
     `min(TotalTasks, 10)` cells.
   - A swarm run is active: `swarmStripText`.
   - A todo list exists with at least one unfinished item:
     `▰▰▰▱ done/total · <in-progress content, else next pending>`. When
     all todos are complete, the row disappears; today's `✓ N tasks done`
     line is dropped.
   - When busy, the row's glyph is the turn spinner frame and the turn
     elapsed time is right-aligned.
2. **Turn row.** Only when busy and there is no progress row. It shows
   today's turn-spinner text (`⠋ 1m12s · <pinned activity label>`, see
   `renderTurnSpinner` and `spinnerShowsLabel`).
3. **Actor rows**, in this order:
   - running subagents (`⧉ #id label · model[@provider] · elapsed`, as the
     lane does today)
   - the open browser session (`browserStripText`)
   - running jobs, then watches (existing `lanePlan` job and watch texts)
4. **Overflow row** `… N more` when the actors exceed the budget.

Budget:

- At most 4 rows.
- When `height < 30`, the bar shows exactly one summary row:
  `<progress text, truncated> · ⧉<n> ┆<n> ○<n>` with elapsed right-aligned.
  Counts that are zero are omitted.
- No live work and no finished-run summary: 0 rows.

Styling:

- Each row is prefixed by the dim `▍` rail (`chromeRailWidth`) and painted
  with `ChromeBG` (`paintLane`).
- No separator rule and no caption row. The rail marks the region on its
  own.

The finished-run summary row persists until the next user turn (current
behaviour, `keypress.go:547`).

### B3. Tasks panel (Ctrl+T)

- Ctrl+T opens a docked `docpanel` built from the todo list.
  - Title: `Tasks d/n`.
  - One row per todo, using the existing `todoLine` glyphs.
  - Footer: `esc close`.
- A second Ctrl+T or Esc closes it.
- With no todos, Ctrl+T shows a system notice `No task list in this
  session.` and opens nothing.
- `todoPanelMode` and `cycleTodoPanelMode` are deleted. The `Ctrl+T tasks`
  hint shows whenever todos exist, as today.

### B4. Session sheet (Ctrl+B)

- **Package:** `internal/app/tui/sidepanel` is renamed to
  `internal/app/tui/sessionsheet` (`git mv`). Delete `rail.go`, `fit.go`
  and `geometry.go` and their tests. Keep `Section`, `Data`, every
  `section_*.go`, `row.go`, `style.go`, `filestats.go` and `telemetry.go`.
  The `changedfiles` package import path is updated.
- **Panel:** new `sessionsheet.Panel` implements `dock.Panel` with
  `Sizing() == dock.Docked`. It renders every relevant section, in the
  rail's order, as:
  - a `chrome.Header(title)` row
  - then `OneLine(d, width)`, or `Render(d, width, maxRows)` when the
    section is selected
  - only the selected section is expanded; the rest are one line each
- **Keys:**

| Key | Action |
|---|---|
| ↑/↓ | Move the selection |
| Enter | Emit `sessionsheet.RunCommandMsg{Command}`; the Model dispatches it and closes the panel |
| Esc or Ctrl+B | Close |

- **Enter command mapping:**

| Section | Command |
|---|---|
| context | `/context` |
| changed | `/diff` |
| worktrees | `/worktrees` |
| skills | `/skills` |
| tools | `/log` |
| sdd | `/run` |
| swarm | `/agents` |
| others | none (Enter does nothing) |

- **Data:** the panel holds a `func() sessionsheet.Data` supplied by the
  Model (`m.sheetData()`), so it shows live data on each render. Data that
  needs a DB query or git is cached on turn boundaries, as the rail's
  is today. Opening the sheet calls `refreshSheetCaches()` once.
- **`[tui.side_panel].hidden`:** still honoured as a filter on sheet
  sections. It is the only sub-key that keeps meaning.

### B5. Status line

- **New segment `±N files`:** N is `len(m.sheetChanged)`.
  - Priority 4.5, placed after ctx and before branch/dir. Use priority 4
    and order it after ctx in the slice.
  - Hidden when N is 0.
  - Rendered with `dimStyle`.
- **`sheetChanged` refresh:** recomputed on turn boundaries and workspace
  events. This is the existing `refreshRailChanged` with its
  `railEnabled()` gate removed.
- **Ctrl+C armed:**
  - The right cluster is replaced by `warningStyle` text: `Ctrl+C again to
    stop the turn` (busy) or `Ctrl+C again to quit` (idle).
  - The left mode segment is unchanged.
  - The armed state is never dropped for width. It is truncated last.
- **Footer hints (`help.FooterHints`):**
  - Busy: `Esc cancel` → `Ctrl+C stop`.
  - `RailEnabled`/`Ctrl+B rail` → always show `Ctrl+B session` when idle.
  - Add `Ctrl+T tasks` when todos exist (already present).

### B6. Esc

The Esc handler (`keypress.go:147–176`) becomes, in order:

1. If the completion popup is open, dismiss it (unchanged).
2. If the suggestion ghost is showing, dismiss it, whether busy or idle.
   Today this only happens when idle.
3. If drilled into a subagent, pop the drill (unchanged).
4. If a notice is visible, dismiss it, whether busy or idle.
5. Otherwise: consume the key and do nothing. **Never call
   `cancelTurn`.**

Dock panels, approval, question, skill gate and command edit keep their own
Esc handling, which is routed earlier.

### B7. Ctrl+C

Replace the top-of-`Update` guard (`model.go:1720–1745`):

- **State:** `ctrlCArmedAt time.Time` and `ctrlCArmedFor ctrlCAction`
  (`stopTurn` or `quit`), replacing `interruptArmed`.
- **On Ctrl+C:**
  - If armed for the action that matches the current state, and
    `now - armedAt <= 3s`: perform the action and disarm.
    - `stopTurn`: `cancelTurn()`, then add the system message `Turn
      stopped.`
    - `quit`: `beginShutdown(true)`.
  - Otherwise: arm. The action is `stopTurn` if `m.busy || m.agentCancel
    != nil`, else `quit`. Set `armedAt = now`.
- **Disarm** on any other `KeyPressMsg`, as today's `interruptArmed`
  handling does.
- **Expiry:** the 3-second window is checked on press and on render. A
  stale arm renders as disarmed. No timer message is needed: the spinner
  tick re-renders while busy, and the next key press handles the idle case.
- **Arming:** adds no transcript message. Only the status line shows it.
- **Edge case:** if the turn ends between the two presses, the second press
  sees an arm for `stopTurn` while the state is idle. This does not match,
  so it re-arms for `quit`. A turn finishing never turns a stop into a quit.
- **Unchanged:** the huh forms' Quit binding stays disabled, so the parent
  guard still sees Ctrl+C first. `app.go`'s OS signal handling is
  untouched.

### B8. Removed behaviour

- **Agent-lane cursor (F6):**
  - Delete `laneCursor`, `laneCursorActive` and the up/down/enter branches
    in `keypress.go` that use them.
  - Delete `agentLaneEntries` if nothing else uses it. Clicks on now bar
    agent rows still drill in: `click.go` maps now-bar rows to subagent
    views through `nowBarPlan.agents`, in the same order.
- **Config diagnostic:** `config.Diagnose` emits a warning for any layer
  that sets `[tui.side_panel]` keys other than `hidden`:
  `tui.side_panel.enabled/min_width/width_pct/min_cols/max_cols are ignored
  — the side rail was removed; use Ctrl+B for the session sheet`.
- **Config fields:** the fields stay in `SidePanelConfig` so existing
  config files still parse. Settings-browser fields for the removed keys
  are deleted.

## Acceptance criteria

1. **Width:** at 80, 100, 140 and 200 columns the frame is one column. No
   rail renders at any width, and the transcript spans the full width.
2. **No live work:** with no busy turn, no todos, no subagents/jobs/watches
   and no run, the now bar has 0 rows. The transcript ends directly above
   the input.
3. **Plan progress:** a turn with 4 todos (2 done, 1 in progress) shows
   one progress row `▰▰▱▱ 2/4 · <in-progress>`, with the spinner glyph and
   elapsed time while busy.
4. **Actor overflow:** with 2 running subagents and 1 job, actor rows show
   below the progress row, to a maximum of 4 rows total, with `… N more`
   for overflow.
5. **Short frame:** at height 24–29 the now bar is at most 1 row.
6. **Frame invariant:** every state covered by `frame_invariants_test.go`
   satisfies `lipgloss.Height(view) == height` with no `clipLeftColumn`
   trimming. Add a test hook that asserts `clipLeftColumn` was a no-op.
7. **Esc:** while busy, Esc with nothing to close does not cancel the turn
   (`agentCancel` is not called and `busy` stays true).
8. **Ctrl+C while busy:** one press arms only, and the status line shows
   `Ctrl+C again to stop the turn`. A second press within 3 s cancels. A
   second press after 3 s re-arms instead.
9. **Ctrl+C while idle:** one press arms and does not quit. A second press
   within 3 s quits.
10. **Disarm:** any other key between the two presses disarms.
11. **Ctrl+B:** opens the session sheet. Enter on "changed" runs `/diff`.
    Esc closes it.
12. **Ctrl+T:** opens the Tasks panel listing every todo.
13. **Changed files:** `±N files` appears on the status line after a turn
    that changes files, at widths where it fits.
14. **Deprecated config:** a config with `[tui.side_panel] enabled = true`
    loads and produces one diagnostic in `/doctor`.
15. `go vet ./...` and `go test ./...` pass. No file under
    `internal/app/tui` imports `sidepanel`.

## Test plan

- **Replace** `sidepanel_wiring_test.go` with `sessionsheet_wiring_test.go`:
  open/close, Enter dispatch, hidden filter, cache refresh on open.
- **New** `nowbar_test.go`:
  - row selection matrix: idle, busy, todos, SDD active/finished, swarm,
    browser, subagents, jobs, watches, overflow, short frame
  - `nowBarRows == lipgloss.Height(render)` for every case
- **New** `keys_cancel_test.go`:
  - Ctrl+C arm, fire, expire and disarm, busy and idle, using an
    injected `now`
  - Esc never cancels
- **Delete or rewrite:** todo panel mode tests, lane cursor tests
  (`agentlane_test.go` cursor cases), run panel top-bar placement tests,
  rail geometry tests.
- **Update** `frame_*_test.go`, `view_test.go`, `status_test.go`,
  `help_test.go` and `click_test.go` for the new layout and hints.

## Risks

- **Discoverability of the new stop key.** Mitigation:
  - The busy footer hint shows `Ctrl+C stop`.
  - The first idle Ctrl+C press after upgrading shows `Ctrl+C again to
    quit`, which teaches the double-press pattern.
  - The `/help` text is updated.
- **Users who relied on the rail at full width** lose the always-visible
  view. Mitigation: the session sheet is one key away. `ctx %` and
  `±files` stay on the status line.
