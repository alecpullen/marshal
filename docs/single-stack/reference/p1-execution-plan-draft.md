# Single Stack P1 — Layout and Keys: Execution Plan (draft, reference only)

> Early draft kept for reference. It is not maintained and may lag the phase
> spec, which is authoritative.

Spec: `../p1-layout-and-keys.md`
Feature spec: `../feature-spec.md`

Each task leaves the tree building, vetted and with all tests passing. Tasks
run in order. Each one names the files it may touch.

## Global Constraints

- Go only, TUI only. Do not modify `internal/app/session`, `internal/agent`,
  `internal/db`, `internal/pipeline` or `web/`. Allowed outside
  `internal/app/tui`: `internal/app/config` (Task 10 only),
  `internal/app/tui/changedfiles` (import path, Task 7) and `AGENTS.md`
  (Task 11).
- No new third-party dependencies.
- Every glyph comes from `internal/app/tui/glyph`. If a new one is needed
  (`▰`/`▱` for progress blocks), add it there with a comment, following the
  file's conventions: single-cell, Geometric Shapes block.
- Follow the transcript indentation contract (`gutterWidth`,
  `continuation()`, `nestedRail()`). No literal column arithmetic.
- Every height a renderer produces must come from the same plan value that
  the height-budget helper reads. Never write a separate row count that
  re-derives a renderer's output.
- Time comes from `m.now()` (injectable), never `time.Now()` directly, in
  any code a test must control.
- Match the surrounding comment density and naming. Comments explain *why*.
- Before finishing each task, run `gofmt -l ./internal`, `go vet ./...` and
  `go test ./internal/app/...`. All must be clean. Building needs
  `CGO_ENABLED=1`.

---

## Task 1: Ctrl+C double-press for stopping and quitting

**Goal:** a single Ctrl+C never stops a turn or quits.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/status.go`,
`internal/app/tui/help/help.go`, new `internal/app/tui/keys_cancel_test.go`,
existing tests that assert the old Ctrl+C behaviour (`interrupt_test.go`,
`model_test.go`).

**Changes:**
1. Replace `interruptArmed bool` with:
   - `ctrlCArmedAt time.Time`
   - `ctrlCArmedFor ctrlCAction`, where `type ctrlCAction int` has values
     `ctrlCNone`, `ctrlCStop` and `ctrlCQuit`
   - `const ctrlCWindow = 3 * time.Second`
2. Rewrite the guard at the top of `Update` (around `model.go:1720`):
   - Work out `want`: `ctrlCStop` if `m.busy || m.agentCancel != nil`, else
     `ctrlCQuit`.
   - If `m.ctrlCArmedFor == want` and `m.now().Sub(m.ctrlCArmedAt) <=
     ctrlCWindow`, fire and disarm:
     - stop: `cancelTurn()` plus the system message `Turn stopped.`
     - quit: `return m, m.beginShutdown(true)`
   - Otherwise arm: set `ctrlCArmedFor = want` and `ctrlCArmedAt = m.now()`.
   - Return `m, nil`.
   - Any other `KeyPressMsg` disarms. Keep the existing allow-list of
     background messages that must not disarm.
3. Add `func (m Model) ctrlCArmed() ctrlCAction`. It returns `ctrlCNone`
   when the window has expired.
4. `renderStatusLine`: when `ctrlCArmed() != ctrlCNone`, replace `right`
   with `warningStyle().Render("Ctrl+C again to stop the turn")` or
   `"Ctrl+C again to quit"`. It is never dropped by the fit loop: treat it
   the way pending-approval is treated.
5. `help.Footer` busy branch: `pair("Esc", "cancel")` → `pair("Ctrl+C",
   "stop")`.

**Tests (`keys_cancel_test.go`, injected `now`):**
- Busy: first press does not call the cancel func and `busy` stays true;
  the status line contains `Ctrl+C again to stop the turn`.
- Busy: second press within 3 s calls cancel.
- Busy: second press at 3.1 s re-arms and does not cancel.
- Idle: first press returns no `tea.Quit`; second press within 3 s returns
  the shutdown command.
- Busy arm, then the turn finishes, then a second press: arms for quit and
  does not quit.
- Arm, press `a`, press Ctrl+C: re-arms and does not fire.

**Done when:** the tests pass. The old tests asserting
first-press-interrupt or idle-first-press-quit are updated to the new
semantics, not deleted.

## Task 2: Esc never cancels a turn

**Files:** `internal/app/tui/keypress.go`, `internal/app/tui/help/help.go`,
`internal/app/tui/keys_cancel_test.go`, plus tests asserting Esc-cancel.

**Changes:**
1. In the `case "esc":` branch, keep popup dismiss and drill pop. Make
   notice dismissal unconditional (drop the `!m.busy` guard). Replace the
   trailing `m.resetHistoryNav(); m.cancelTurn()` with
   `m.resetHistoryNav(); return *m, nil, true`.
2. In the suggestion pre-routing block, make the Esc dismissal
   unconditional (drop `if !m.busy`).
3. Update comments that still say "Esc … cancel". The Ctrl+X comment in
   the drill branch should now say Ctrl+C (double) stops the whole turn.

**Tests:**
- Busy, nothing open: Esc leaves `busy` true and does not call the cancel
  func.
- Busy with a notice visible: Esc dismisses the notice.
- Busy and drilled in: Esc pops the drill and does not cancel.

**Done when:** `grep -n "cancelTurn" internal/app/tui/keypress.go` shows
no call inside the Esc case. The tests pass.

## Task 3: Now bar plan and renderer (not wired yet)

**Files:** new `internal/app/tui/nowbar.go`, new
`internal/app/tui/nowbar_test.go`, `internal/app/tui/glyph/glyph.go` (add
`ProgressFull = "▰"`, `ProgressEmpty = "▱"`).

**Changes:**
1. Define `nowBarInput` as a value struct with:
   - `SDD session.SDDProgress`
   - `Swarm session.SwarmProgress`
   - `Todos []native.TodoItem`
   - `Agents []session.SubagentView` (running, with a child, in registry
     order)
   - `ParentProvider string`
   - `Browser session.BrowserInfo`
   - `JobTexts []string` and `WatchTexts []string` (pre-rendered, as
     `lanePlan` builds them)
   - `Busy bool`, `TurnStartedAt time.Time`, `Spinner string`,
     `ActivityLabel string` (already filtered through `spinnerShowsLabel`)
   - `Now time.Time`, `Width int`, `Height int`
2. Define `nowBarPlan` with:
   - `rows []string` (rendered, unpainted)
   - `agents []session.SubagentView`, which are the agent rows shown, in
     row order (for clicks)
   - `agentRowStart int`, the row index of the first agent row
3. Implement `planNowBar(in nowBarInput) nowBarPlan` following spec B2
   exactly:
   - the progress-row source order (SDD, then swarm, then todos)
   - the turn row only when busy with no progress row
   - actor rows (agents, browser, jobs, watches)
   - a 4-row cap with a `… N more` row
   - when `Height < 30`, one summary row
   - every row passed through `chromeRailWidth(…, dimColor, Width-1)`
4. Add `progressBlocks(done, total, maxCells int) string`, using rounding,
   not truncation (see `formatPercent`).
5. Implement `renderNowBar(p nowBarPlan, width int) string`. It joins the
   rows and applies `paintLane`. It returns `""` for zero rows.
6. Reuse `runPanelSummaryLine`, `runPanelFinishedLine`, `swarmStripText`,
   `browserStripText`, `laneItem`, `formatElapsed` and the job/watch row
   builders already in `lane.go`. Move a builder into a shared helper only
   if it is currently inlined in `lanePlan`.

**Tests (table-driven):**
- Cases: idle (0 rows); busy with no todos (turn row); todos 2/4 busy;
  todos all done (0 rows); SDD active; SDD finished; swarm; 2 agents + 1
  job; 6 actors (overflow); Height 24 (1 summary row); browser open.
- For each case, assert `len(plan.rows)`, the key substrings, and
  `lipgloss.Height(renderNowBar(...)) == len(plan.rows)`.

**Done when:** the tests pass and nothing calls the code from `View` yet.

## Task 4: Wire the now bar in and delete the five surfaces

**Files:** `internal/app/tui/view.go`, `internal/app/tui/model.go`,
`internal/app/tui/todos.go`, `internal/app/tui/livestrip.go`,
`internal/app/tui/agentlane.go`, `internal/app/tui/lane.go`,
`internal/app/tui/run_panel.go`, `internal/app/tui/click.go`, and the tests
for these files.

**Changes:**
1. Add `func (m Model) nowBarInput() nowBarInput` and
   `func (m Model) nowBarPlan() nowBarPlan`. Include the existing
   job/watch row builders from `lanePlan`.
2. `viewString`:
   - Remove `topBar` and the `renderTurnSpinner`, `renderTodoPanel`,
     `renderLiveStrip` and `renderActivityLane` rows.
   - Insert `renderNowBar(m.nowBarPlan(), m.leftWidth)` after the
     transcript frame.
   - `leftHeight` no longer subtracts a top bar.
3. `resize()`: replace `todoPanelRows`, `runPanelRows`, `liveStripRows`,
   `laneRows` and `turnSpinnerRows` with a single `m.nowBarRows()`, which
   is `len(m.nowBarPlan().rows)`.
4. Delete these functions:
   - `renderTurnSpinner`, `turnSpinnerRows`
   - `renderTodoPanel`, `todoPanelRows`, `renderTodoPanelBody`,
     `todoOneLine`, `todoPanelBudget`
   - `renderLiveStrip`, `liveStripRows`, `stripShowsBrowser`
   - `renderActivityLane`, `laneRows`, `laneSeparator`, `renderLane`
   - `renderRunPanel` (the method), `runPanelRows`, `runPanelBar`

   Keep `todoLine`, `todoProgress`, `runPanelSummaryLine`,
   `runPanelFinishedLine`, `paintLane` and `laneItem`. Check
   `ShouldShowStatusURL` and keep its behaviour by reading `in.Browser`
   directly.
5. `click.go`: the lane hit-test (around line 109) maps clicks inside the
   now-bar rows to `plan.agents[row - plan.agentRowStart]` and drills in.
   The frame order comment is updated.
6. Delete tests for removed functions. Port their assertions that still
   apply (for example SDD finished-line content) to `nowbar_test.go` or
   view tests.

**Tests:**
- `frame_invariants_test.go` and `frame_width_test.go` pass for every
  state.
- Add a case with SDD active + todos + 2 subagents at 80×24.
- Clicking an agent row in the now bar drills into that subagent.

**Done when:** `grep -rn "renderTodoPanel\|renderActivityLane\|renderLiveStrip\|renderTurnSpinner\|runPanelRows" internal/app/tui --include=*.go`
returns nothing. All tests pass.

## Task 5: Remove the agent-lane keyboard cursor (F6)

**Files:** `internal/app/tui/keypress.go`, `internal/app/tui/model.go`,
`internal/app/tui/agentlane.go`, `internal/app/tui/click.go`, plus tests.

**Changes:**
1. Delete the `laneCursor` and `laneCursorActive` fields and every
   reference to them: the up/down/enter branches in `keypress.go`, the
   disarm block near the top of `handleKeypress`, and the click handler
   reset.
2. Delete `agentLaneEntries` and `lanePlan` if nothing uses them after
   Task 4. Fold any still-needed job/watch text builders into
   `nowbar.go`. Delete `agentlane.go` if it is empty.
3. Up/down on an empty input now only recall prompt history, as when no
   lane is shown today.

**Tests:** remove the lane-cursor cases in `agentlane_test.go`. Add one
case: with a running subagent and empty input, ↑ recalls history and does
not select an agent.

**Done when:** `grep -rn laneCursor internal/` returns nothing. All tests
pass.

## Task 6: Tasks panel on Ctrl+T

**Files:** `internal/app/tui/keypress.go`, `internal/app/tui/todos.go`,
`internal/app/tui/model.go`, `internal/app/tui/help/help.go`, plus tests.

**Changes:**
1. Add `func tasksDoc(todos []native.TodoItem) commands.Doc`.
   - Title: `Tasks d/n`.
   - Rows: `todoLine`'s glyph and text as plain `commands.Row` text. Check
     how `docpanel` renders rows and keep styling consistent.
   - Footer: `esc close`.
2. Ctrl+T (with `readlineShortcutAvailable()`):
   - Panel already open: close it. Track it with `m.tasksPanelOpen` or by
     panel identity.
   - Todos exist: `m.openDocPanel(&doc)`.
   - Otherwise: system notice `No task list in this session.`
3. Delete `todoPanelMode`, `todoPanelExpanded`/`Collapsed`/`Hidden`,
   `todoPanelModeCount`, `cycleTodoPanelMode`, `todoPanelMaxRows`,
   `todoPanelMaxVisibleItems`, and `todosDismissed`/`todosSig` if unused.

**Tests:** Ctrl+T with 3 todos opens a dock panel whose view contains all 3
contents. A second Ctrl+T closes it. Ctrl+T with no todos opens nothing and
adds the notice.

**Done when:** the tests pass and `grep -rn todoPanelMode internal/`
returns nothing.

## Task 7: Remove the rail and rename `sidepanel` to `sessionsheet`

**Files:** `internal/app/tui/sidepanel/*` (moved), `internal/app/tui/model.go`,
`internal/app/tui/view.go`, `internal/app/tui/status.go`,
`internal/app/tui/keypress.go`, `internal/app/tui/changedfiles/changedfiles.go`,
`internal/app/tui/sidepanel_wiring_test.go`, and other tests that touch
the rail.

**Changes:**
1. `git mv internal/app/tui/sidepanel internal/app/tui/sessionsheet` and
   change the package clause to `package sessionsheet`. Update the package
   doc comment to describe a docked session sheet.
2. Delete `rail.go`, `fit.go`, `geometry.go` and their tests
   (`rail_test.go`, `fit_test.go`, `geometry_test.go`, and any test
   exercising `Rail`).
3. Update imports across `internal/` (`changedfiles` uses `ChangedFile`).
4. In `Model`:
   - Delete `railWidth`, `railHidden`, `rail`, `rebuildRail` and
     `railEnabled`.
   - Rename the caches: `railRepoStats`, `railTurns`, `railTotals`,
     `railBaseRef`, `railChanged`, `railFleet` → `sheetRepoStats`,
     `sheetTurns`, `sheetTotals`, `sheetBaseRef`, `sheetChanged`,
     `sheetFleet`.
   - Rename `refreshRail*` → `refreshSheet*` and remove their
     `railEnabled()` gates.
   - Rename `railData`/`childRailData` → `sheetData`/`childSheetData`.
   - Rename `railBaseRefMsg` and its handler accordingly.
5. `resize()`: `m.leftWidth = width`. Delete the `sidepanel.Geometry`
   call.
6. `viewString`: delete the rail `JoinHorizontal` block.
7. `WindowSizeMsg`: delete the `wasRailEnabled` refresh logic.
8. Ctrl+B: temporarily a no-op that returns handled. Task 8 binds it.
9. `help.FooterHints.RailEnabled`: delete the field and its hint. Task 8
   adds the session hint.

**Tests:** delete `sidepanel_wiring_test.go`. Section unit tests move with
the package and keep passing. Frame tests at 140 and 200 columns assert
that the transcript spans the full width.

**Done when:** `grep -rn "sidepanel\|railWidth\|railEnabled" internal/ --include=*.go`
returns nothing. All tests pass.

## Task 8: Session sheet panel on Ctrl+B

**Files:** new `internal/app/tui/sessionsheet/panel.go` and
`panel_test.go`, `internal/app/tui/model.go`,
`internal/app/tui/keypress.go`, `internal/app/tui/help/help.go`, new
`internal/app/tui/sessionsheet_wiring_test.go`.

**Changes:**
1. `sessionsheet.Panel` implements `dock.Panel` with `Sizing() ==
   dock.Docked`. `NewPanel(sections []Section, data func() Data) *Panel`.
   - `View(width, maxHeight)`:
     - For each section where `Relevant(d)` is true, in order: a
       `chrome.Header(Title)` row, then the body.
     - The body is `OneLine` for unselected sections and `Render(d, w,
       budget)` for the selected one.
     - The selected section gets the rows left after every other section
       has one line.
     - Clip to `maxHeight` while keeping the selected section visible.
     - Footer hint: `↑↓ section · ↵ open · esc close`.
   - `Update`:
     - ↑/↓ move the selection over relevant sections.
     - Enter emits `RunCommandMsg{Command: commandFor(section.ID())}` when
       the section has a command (mapping in spec B4).
     - Esc emits `CloseMsg{}`.
   - Use the existing dock close convention if one exists; check how
     `docpanel` and `picker` close.
2. Define the section list once: `sessionsheet.DefaultSections()`, in the
   former rail order. Apply the `[tui.side_panel].hidden` filter in the
   Model.
3. Model:
   - Ctrl+B: if the sheet is open, close it. Otherwise call
     `refreshSheetCaches()` (turns, changed, fleet), then open the panel
     with `m.sheetData`. While drilled into a child, use `childSheetData`.
   - Handle `RunCommandMsg`: close the dock and call
     `m.dispatchCommand("/" + cmd)`.
4. Footer: when idle and not busy, show `Ctrl+B session`.

**Tests:**
- Panel: rendering with 3 relevant sections, selection movement, Enter
  emitting the right command, Esc closing, the hidden filter.
- Wiring: Ctrl+B opens the panel and runs the cache refresh exactly once;
  Enter on "changed" dispatches `/diff`.

**Done when:** the tests pass and the frame invariants still hold with the
sheet open at 80×24.

## Task 9: `±N files` status segment

**Files:** `internal/app/tui/status.go`, `internal/app/tui/status_test.go`.

**Changes:** in `statusLeftSegments`, after the ctx segment, append
`statusSeg{text: dimStyle().Render(fmt.Sprintf("±%d files", n)), priority: 4}`
when `n := len(m.sheetChanged); n > 0`. Use `file` (singular) when n == 1.
Update the priority doc comment.

**Tests:** the segment appears with 3 changed files at 140 columns. It is
absent with 0. At 80 columns it is dropped before the route and mode
segments.

**Done when:** the tests pass.

## Task 10: Deprecate `[tui.side_panel]`

**Files:** `internal/app/config/diagnostics.go`, its test,
`internal/app/config/types.go` (doc comment only), and the settings
registry under `internal/app/tui/settings` (remove fields for
`enabled/min_width/width_pct/min_cols/max_cols`).

**Changes:**
1. `Diagnose` emits one warning-severity diagnostic when any loaded layer
   sets `enabled`, `min_width`, `width_pct`, `min_cols` or `max_cols`
   under `[tui.side_panel]`. Use the message in spec B8 and match the
   existing diagnostic struct and severity. Find out how `Diagnose` sees
   per-layer raw keys (`Layers`). If raw-key presence is unavailable,
   compare against `Default()` instead.
2. Update the `SidePanelConfig` doc comment: only `Hidden` is honoured, as
   the session sheet's section filter.
3. Remove the settings-browser entries for the dead keys. Keep `hidden` if
   it has an entry.

**Tests:** a config with `[tui.side_panel] enabled = true` yields the
diagnostic. The defaults yield none.

**Done when:** the tests pass.

## Task 11: Help text, docs and final invariant guard

**Files:** `internal/commands` (the `/help` cheatsheet source; find it with
`grep -rn "Ctrl+G\|ctrl+g" internal/commands`),
`internal/app/tui/help/*`, `internal/app/tui/view.go`,
`internal/app/tui/frame_invariants_test.go`, `AGENTS.md`.

**Changes:**
1. `/help` cheatsheet:
   - `Esc` = close or dismiss.
   - `Ctrl+C` = stop the turn (press twice), quit when idle (press twice).
   - `Ctrl+T` = task list.
   - `Ctrl+B` = session sheet.
   - Remove any rail or lane-cursor lines.
2. Add the test hook `var clipLeftColumnHook func(trimmed int)`, called by
   `clipLeftColumn` when it trims. `frame_invariants_test.go` sets it and
   fails on any trim across every covered state.
3. `AGENTS.md` architecture tree:
   - Replace the `internal/app/tui/sidepanel/` line with
     `internal/app/tui/sessionsheet/ — session sheet (Ctrl+B): read-only
     sections in a docked panel`.
   - In the "Check here before building something" sentence, replace "the
     side rail" with "the session sheet".

**Done when:** `go vet ./...` and `go test ./...` pass. A manual run (`go
run ./cmd/marshal` at 80×24 and 200×50) shows one column, the now bar while
working, and the Ctrl+C arming text.
