# P-E the tray — implementation plan

Date: 2026-10-25. Status: ready for review.
Base: `main` at `fc0bceba` (P-D merged) or later, with the round-5 spec edits committed (see Execution).
- Every anchor below was checked at `fc0bceba`. Line numbers are there to find the code fast; the function names are the anchors.
- Two throwaway probes ran in a scratch worktree, and neither is part of this plan:
  - rendering the live part in the viewport again (Task 2's first step) broke exactly the nine tests Task 2 lists;
  - Task 1's narrower re-wrap broke no test.

Approved design: [feature-spec.md](../feature-spec.md) §3 (round 5 rows), §5.2 (beat head), §5.5 (the tray), §5.7 (status line), §5.8 (keys), §6.3 (the P-E bullet), §6.5, §7 (P-E), §8 (P-E) and §9. The spec was updated on 2026-10-25 with the decisions below.
Design record and frames: [pinned-work.md](../pinned-work.md) (the Decision section).
Previous phase: [P-C plan](2026-10-24-p-c-live-box-plan.md), which built what this phase reshapes.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy. "Restore `TestX` from `d7d9b39c`" means replace the function with its body at that commit (`git show d7d9b39c:<file>`), unchanged.

**Goal:** after testing, the live box reads as a panel that the rest of the TUI does not have. P-E turns it into the tray:
- the step in progress goes back to the end of the transcript and settles where it was drawn;
- the tray above the input holds the plan-run row, running rows, finished rows held until the next prompt, and the full plan list, with no frame, rule, header or layout modes;
- the turn clock moves to the status line;
- a beat head's meta never takes a line of its own;
- `[tui.live_box]` is retired and Ctrl+T is unbound.

**Non-goals:**
- **No projection change.** `viewmodel.SplitLive` stays because the append-only property tests (`internal/viewmodel/history_test.go`) are defined over it. After this plan it has no production caller, and that is expected.
- **No rename.** The package keeps the name `livebox`, and the model keeps `boxInput`, `boxLayout`, `boxRows`, `boxMaxRows`, `boxTop`, `handleBoxClick` and the `box:` browse IDs. Doc comments say "the tray". This keeps the diff to behaviour.
- **No change** to the running-row shapes (§5.6), the expanded card, `/work`, the web, or the progress skill.
- **No key design.** Ctrl+T is simply unbound.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/pe-suite`. Create it first: `mkdir -p /private/tmp/pe-suite`.
- **Pre-existing failures** at `fc0bceba`, from a full run with `-timeout 20m`:
  - `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree`, `TestSubagentShellOutputDoesNotReachParent`, `TestAccessTUIOptionsResolveWithoutConfigReload` and `TestRuntimeNewAndResumedSessionResetAccess`;
  - `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`;
  - `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`;
  - `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`;
  - flaky under full-suite load, passing alone (none failed in this run): `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`), and `TestACPChildApprovalUsesOwningSessionRegistry` (`internal/acp`);
  - `web/bridge` is slow under load (about 2.5 minutes alone). It passed in this run under `-timeout 20m`, which every full run below uses.

  A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`.

**Decisions this plan makes beyond the spec** (all now recorded in the spec):
1. **The held window is a timestamp.** The model gains `heldSince`, set when the model is built and again at each of the three places a turn starts (`m.turnStartedAt = m.now()` in `startAgentRun`, in `startSDDAuthoring`, and in the `review` command's effect). A finished item whose `EndedAt` is after `heldSince` is held. A resumed session therefore starts with nothing held, and sending any prompt drops every held row.
2. **A finished plan is one row until the next prompt, tracked by fingerprint.** Todo items set without `todo.write` carry no `CompletedAt`, so a time rule would hide them. At each turn start, if the band's list is finished, the model records its fingerprint in `planDismissed`. The tray leaves out a finished list with that fingerprint. The row is the box's finished idle row, `✓ Plan N/N done`.
3. **A held row takes its words from the completion event.** During each refresh the TUI indexes completion nodes by `CompletionInfo.WorkKey`, which is the same key as the work item's `TaskKey` (both come from `viewmodel.WorkKey`). The held row shows the event's outcome, summary and duration, so the tray and history always agree. Until the event exists, the row falls back to the item's state (`completed` → `done`, `failed` → `failed`, `stopped` → `stopped`, anything else → `outcome unknown`) with no summary.
4. **Order is by start time, and a row never moves.** Rows sort by `StartedAt`, then `TaskKey`. This is `runningOrder` without its blocked-first rule, so a row stays put when it becomes blocked. When a shed step caps the rows, blocked rows are kept first, and the kept rows still draw in start order.
5. **The plan-run row has no action.** It is information, as the box's top-border state was. It is not a browse stop, and a click on it is consumed and does nothing.
6. **Turn state sits right after the mode cue, and the stop hint survives narrow frames.**
   - `renderStatusLine` never drops the first segment (the mode), so the turn state is the next segment, after the `untrusted` cue when there is one, at priority 0.
   - The status line's busy hints already include `Ctrl+C stop`, but `renderStatusLine` drops the whole hint cluster when it does not fit, and at 96 columns while busy it does not. The box's border always showed the hint, so the tray must not lose it.
   - A new `FooterHints.StopOnly` makes `help.Footer` return only `Ctrl+C stop` while busy. `renderStatusLine` tries it as the last fallback before dropping the cluster.
7. **The head re-wraps only when it must.** A narration that fits beside its meta is drawn as today. One that does not is wrapped again, narrower by the meta's width plus two. The old meta-only line stays only as a fallback for a first line with no break point.
8. **A stale plan gets one more entry.** The box put `stale 4m` in the plan header, which the tray does not have. The plan's entries gain a final `stale <age>` entry in the warning colour, with no target.
9. **Tray rows sit under the glyph column.** History draws its glyph in column 1. Running and held rows put their `▌` edge in column 0 and their glyph in column 1, as today. The plan rows and the finished-plan row are indented one cell, so their marks sit in column 1 too.
10. **The compact row keeps P-C's format without the turn state:** the kind counts, then `plan D/T` with done over total, joined by single spaces.

**Execution:**
- Branch first: `git switch -c p-e-tray` from `main`.
- If `git status` shows the round-5 docs uncommitted (`docs/tui-simplification/feature-spec.md`, `README.md`, `pinned-work.md`, `pinned-work.html` and this plan), commit them first as explicit paths, with the message `p-e-tray: record the round-5 design, spec and plan`.
- Run the tasks in order. Run each task's Verify and commit it as `p-e-tray: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- "Delete test X" means delete the whole function and its doc comment. "Extend test X" means keep its assertions and add the ones the step names.

## Contracts

**`internal/app/tui/livebox`, after Task 4.** These declarations replace today's `Input`, `Row` and `Layout`:

```go
type Input struct {
	Width     int                           // the column the tray spans
	MaxRows   int                           // the most rows the tray may take
	Now       time.Time
	Spinner   string                        // the turn spinner frame; "" draws glyph.Running
	Run       *RunRow                       // the plan-run row; nil when no run is active
	Work      []viewmodel.WorkItem          // running items only (viewmodel.WorkActive)
	Held      []HeldItem                    // finished items held until the next prompt
	Previous  map[string]*progress.Snapshot
	Headlines map[string]string             // agent TaskKey → the child's newest narration headline
	Expanded  string                        // TaskKey of the one open card
	Plan      []PlanItem
	Stale     time.Duration                 // > 0 when the plan is stale
	Cursor    Target                        // the browse cursor when it is in the tray
}

type RunRow struct {
	Title     string    // the SDD plan's name, or "swarm"
	Summary   string    // the run summary, composed by the TUI
	StartedAt time.Time // the turn's start: the row's clock
}

type HeldItem struct {
	Item     viewmodel.WorkItem // the finished item; its TaskKey and StartedAt keep its slot
	Outcome  string             // "done", "exit 1", "stopped", "lost", "failed" or "outcome unknown"
	Failed   bool
	Summary  string             // the completion event's summary line; "" when none
	Duration time.Duration
}

type Row struct {
	Text   string // styled foreground only
	Tint   bool   // running work: painted with the surface colour
	Cursor bool   // the browse cursor's row: painted with the selection colour
	Target Target
	Also   []RowStop
}

type Layout struct {
	Rows  []Row
	Shed  int      // shed steps applied, 0–6
	Stops []Target // browse stops in row order: running and held rows, the more row, plan items
}
```

**Removed from `livebox` in Task 4:** `Mode`, `Stacked`, `Split`, `Folded`, `ParseMode`, `Next`, `SplitMinWidth`, `RightWidth`, `InnerWidth`, `LeftWidth`, `Live`, `RowFrame` and its three values, `TargetLive`, `Input.Busy`, `Input.State`, `Input.Hints`, `Input.Live`, `Input.Mode`, `Row.Frame`, `Row.Right`, `Layout.Mode`. `PlanItem`, `Target`, `TargetKind` (minus `TargetLive`), `RowStop`, `Bar`, `Blocks`, `BarCells`, `PlanWindow`, `CardRows` and `RunningCap` stay.

**`internal/app/tui` model fields.**

```go
heldSince     time.Time                // a finished item that ended after this is held (decision 1)
planDismissed string                   // fingerprint of the finished list seen at the last turn start (decision 2)
completions   map[string]completionRef // completion events by WorkKey, rebuilt on every refresh (decision 3)

type completionRef struct {
	node viewmodel.NodeID
	info *viewmodel.CompletionInfo
}

func (m Model) turnStateSegment() string // "" unless busy; see the strings below
```

`liveBoxMode` and `live` are deleted.

**Strings the tests pin:**

| Where | Text |
|---|---|
| Stop hint (status line, narrow) | `Ctrl+C stop`, the existing busy-hint pair alone |
| Turn state (status line) | `<spinner> working <elapsed>`, plus ` · <label>` when `spinnerShowsLabel` and the pinned label is not empty. With no spinner frame yet (the first 200 ms) it is `working <elapsed>`, as `spinnerLabel` already makes it. |
| Shed step 2 row | `✓ <N> finished` (the `✓` in the success colour, the rest muted) |
| More row | `+<N> more · /work` (unchanged) |
| Finished plan | `✓ Plan <D>/<T> done` (unchanged from `idleRow`) |
| Stale plan entry | `stale <age>`, in the warning colour, `<age>` from `formatElapsed` |
| Compact row | e.g. `⧉2 ┆2 ○1 plan 2/4` |
| Config diagnostic | Path `tui.live_box`, warning, message `tui.live_box.layout is ignored — the live box became the tray, which has one layout` |

## Task 1: The beat head keeps its meta on the first line

**Goal:** a narration that wraps keeps its todo tag and other meta on its first line, so no meta-only line appears under the head.

**Files:** `internal/app/tui/steps.go`, `internal/app/tui/steps_test.go`.

**Steps:**

1. **The re-wrap (`steps.go`, the narrated branch of `renderStep`, lines 161–180).** Today the narration renders once at `nestedContentWidth(width)`, `rightMeta` picks the meta, and when the first line and the meta do not fit side by side (`metaFits` false) the meta is written on a line of its own after the narration.
   - Keep the first render. After `rightMeta`, when the meta is not empty and the first line plus two cells plus the meta is wider than `width - gutterWidth`, render the narration again with the same `renderMarkdown`/`renderPlainProse` pair at `max(cw - metaW - 2, 1)`, and take the first line from that render.
   - Compute `metaFits` as today, from the new first line. The meta-only line after the narration stays, now reached only when even the narrower first line has no room (decision 7).
   - Do not change `rightMeta`, the meta's candidates, or the headless head.
2. **Test (`steps_test.go`).** Add `TestWrappedNarrationKeepsItsMetaOnTheFirstLine`.
   - Build a model as `todoStack` (`todo_band_test.go`) does, but with one beat under the in-progress todo `t1` whose narration is 150 characters of plain prose, and with `m.busy` false.
   - At each of `m.resize(80, 30)` and `m.resize(96, 34)`, read `transcriptLines`. The line holding the narration's first words ends, after trimming spaces, with `▸ 1/4`.
   - No line, trimmed, equals `▸ 1/4`.
   - A second beat with a 30-character narration under the same todo stays on one line with no tag (later beats under one todo carry none, §5.2).

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/ -run 'TestWrappedNarration|TestNarrated|Beat|Head' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/...
```

Expected: the new test passes, and the package fails only on the Assumptions list.

## Task 2: The step in progress goes back into the transcript

**Goal:** the viewport renders history and the live part together, as it did before P-C; the box loses its live part.

**Files:**
- `internal/app/tui/viewport_render.go`, `internal/app/tui/livebox_view.go`, `internal/app/tui/click.go`, `internal/app/tui/model.go`;
- tests: `livebox_view_test.go`, `model_test.go`, `region_scroll_test.go`, `spine_test.go`, `steps_test.go`, `toolgroup_test.go`, `tasks_test.go`.

**Steps:**

1. **`refreshViewport` (`viewport_render.go`).**
   - Delete the `history, live := viewmodel.SplitLive(turns, snap.Busy)` line (84) and its comment.
   - The turn loop (167) ranges over `turns` again, as it did at `d7d9b39c`.
   - Delete the loop over `live` that only collected seen and tree entries (196–202), and the `m.live = m.renderLive(...)` line (203). The live nodes are now ordinary turn children, so the turn loop renders them, collects their browse items and indexes them.
   - Drop the `livebox` import if nothing else in the file uses it.
2. **`livebox_view.go`.**
   - Delete `renderLive` and `lastToolChild`, and the file-head paragraph that says the live part never enters the viewport.
   - `boxInput` stops setting `Live`. (The field goes in Task 4; until then it is the zero value.)
   - `scrollToNode`'s comment loses "The live part has no viewport region any more: it is in the box."
3. **`model.go`.** Delete the `live livebox.Live` field and its comment.
4. **`click.go`.**
   - Delete `wheelOverBoxReasoning` and the branch at the top of `scrollLiveRegionAt` that calls it. The reasoning row is a viewport region again, so the existing content-line path scrolls it.
   - Keep `setRegionOffset`.
   - In `handleBoxClick` (`livebox_view.go`), delete the `livebox.TargetLive` case.
5. **Tests.**
   - **Restore from `d7d9b39c`** (each read the live part from the box; at `d7d9b39c` they read the viewport, and the probe confirmed they pass unchanged):
     - `TestPolishedTranscriptReflowsAfterResize` (`model_test.go`);
     - `TestLiveThinkingDrawsBelowTheSpine` (`spine_test.go`);
     - `TestRunningToolRowInsideStepExpandsOnClickEvenWithGlobalExpand` (`steps_test.go`);
     - `TestRunningToolRendersBelowGroupThenFolds` (`toolgroup_test.go`);
     - `TestRegionScrollRepaintsTheBox` (`region_scroll_test.go`) becomes `TestRegionScrollRepaintsViewport`, its `d7d9b39c` name and body.
   - **Delete** `TestBoxClickOnALiveCallExpandsIt` and `TestWheelOverTheBoxReasoningScrollsIt` (`livebox_view_test.go`). The restored viewport tests cover both.
   - **Replace** `TestLiveBeatRendersInTheBoxNotTheViewport` with `TestLiveBeatRendersInTheViewport`, using the same scene: while busy, `Reading the parser.` is in `m.viewport.GetContent()` and not in `boxOut(m)`; after the turn ends it is still in the viewport, on the same line index it had while busy.
   - **`TestTodoDividersOrderTheWorkAndTheBandCarriesProgress` (`tasks_test.go`):**
     - the live beat is history now, so assert `Writing it.` is in `transcriptLines`, on a line ending `▸ 2/4`;
     - drop `Writing it.` from the negative check, which keeps `3/4`, `4/4` and `──`;
     - leave the box assertions for Task 4.
   - Remove imports the edits leave unused (`livebox` in `model_test.go`, `registry` in `livebox_view_test.go`).

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/ -run 'Live|Region|Reflows|RunningTool|TodoDividers|Spine|Settled' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/... ./internal/viewmodel/...
go vet ./internal/app/tui/...
```

Expected: the named tests pass, including `TestSettledStepsAreNotReRenderedOnSpinnerTicks`. Both packages fail only on the Assumptions list.

## Task 3: The turn state moves to the status line

**Goal:** while busy, the status line carries the spinner, `working`, the elapsed time and the activity label. The box still draws its border state until Task 4 removes it.

**Files:** `internal/app/tui/status.go`, `internal/app/tui/help/help.go`, `internal/app/tui/view_test.go`, `internal/app/tui/model_test.go`, `internal/app/tui/help/help_test.go`.

**Steps:**

1. **`turnStateSegment` (`status.go`).** It returns "" unless `m.busy` and `m.turnStartedAt` is set. Otherwise it is the Contracts' text, built as `boxInput` builds `in.State` today in its default case:
   - `spinnerLabel(m.turnSpinnerFrame(), "working "+formatElapsed(elapsed))`;
   - then ` · ` and the pinned label when `spinnerShowsLabel(act.Kind)` and `m.state.PinnedSpinnerLabel(act)` is not empty.

   During an SDD run or a swarm it still says `working`; the run's summary becomes the tray's plan-run row in Task 6.

   The segment's plain text is exactly that. When it is drawn, the spinner glyph is in the primary accent and the rest uses `dimStyle()`.
2. **`statusLeftSegments`.** Insert the segment, when not empty, right after the `untrusted` segment (or right after the mode segment when trusted), with priority 0. Update the priority list in the function's doc comment.
3. **The stop hint (decision 6).**
   - `help.FooterHints` gains `StopOnly bool`, documented as "while busy, the footer is only the stop hint".
   - `Footer` returns `pair("Ctrl+C", "stop")` alone when `Busy` and `StopOnly` are both set.
   - In `renderStatusLine`, after the two existing fallbacks and before `right = ""`: when `m.busy`, try `footerHints()` with `StopOnly` set.
4. **Tests.** Each of these read the turn state from the box. Point them at the status line instead:
   - **`model_test.go`:** `TestTurnSpinnerDropsStreamingThinkingLabels`, `TestTurnSpinnerKeepsStableActivityLabels`, `TestTurnSpinnerLabelDwellHoldsOldLabel` and `TestTurnSpinnerLabelDwellResetsOnKindChange` read `stripANSI(m.turnStateSegment())` where they read `boxOut(m)`.
   - **`view_test.go`:**
     - `TestTurnSpinnerShowsActivityLabel` and `TestTurnSpinnerSpansPhaseGaps` read `stripANSI(m.turnStateSegment())`.
     - `TestTurnSpinnerShowsElapsedDirectlyAboveInput` becomes `TestTurnStateShowsInTheStatusLine`. The segment equals `⠋ working 12s`, with no label. The frame's last line (the status line) contains it. The box-border assertions go.
     - `TestTodoProgressRowCarriesTurnElapsed`: the `⠹` and `24s` checks move from the box's top line to the frame's last line. Its plan-header checks are Task 4's.
   - **Add `TestStatusLineKeepsTheTurnStateWhenNarrow`.** At 80 and at 96 columns, busy, with an activity label and a long route identity, the rendered status line still holds `working` and `Ctrl+C stop`, and the mode segment is still first.
   - **Add `TestFooterStopOnly` (`help/help_test.go`).** `Footer(FooterHints{Busy: true, StopOnly: true})` is exactly the `Ctrl+C stop` pair. `StopOnly` without `Busy` changes nothing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/ ./internal/app/tui/help/ -run 'TurnSpinner|TurnState|StatusLine|TodoProgress|Footer' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/...
```

Expected: the named tests pass, apart from the baseline's `TestStatusLineUntrustedIsWarningColored`. The package fails only on the Assumptions list.

## Task 4: The tray

**Goal:** `livebox` plans and renders the tray: no frame, no section headers, no modes, no live part, with the new shed order. The TUI stops using the removed API, and Ctrl+T is unbound. Held rows and the plan-run row get their data in Tasks 5 and 6; this task makes the package draw them.

**Files:**
- `internal/app/tui/livebox/livebox.go`, `box.go`, `plan.go`, `running.go`, their tests, and `testdata/`;
- `internal/app/tui/livebox_view.go`, `view.go`, `keypress.go`, `model.go`, `status.go`, `help/help.go`;
- tests: `keypress_test.go`, `help/help_test.go`, `view_test.go`, `tasks_test.go`, `livebox_view_test.go`.

**Steps:**

1. **Types (`livebox.go`).** Apply the Contracts' declarations and removals. The package doc says it plans and renders the tray: the plan-run row, running and held rows, and the plan.
2. **`Plan` (`box.go`).**
   - The tray has rows when `Run` is set, or `Work`, `Held` or `Plan` is not empty; otherwise it has none.
   - The unshed layout is, top to bottom:
     - the plan-run row;
     - the running and held rows, merged and ordered as decision 4 says, with the open card in place of its row;
     - the more row when a cap trims them;
     - the plan.
   - The plan is the plan rows (step 5), or the finished-plan row when every item is completed.
   - Shed steps (`shedSteps = 6`), each applied in order and the tray rebuilt until it fits `MaxRows`:
     1. The plan becomes its one-row form (step 5).
     2. The held rows fold into one `✓ N finished` row after the running rows. It has no target.
     3. Running rows cap at `RunningCap`, with the more row.
     4. The open card collapses to its row.
     5. Running rows cap at 1, with the more row.
     6. One compact row (decision 10).
   - Keep the `MaxRows < 1` floor and the step loop's shape. Delete `buildState`'s `mode`, `liveShort` and `liveOff`; `drawnMode`; `liveLines`; `topLine`; `sectionHeader`; the four split helpers; `bottomLine`.
   - `finish` collects stops as today. The plan-run row and the `✓ N finished` row carry no target, so they are not stops.
3. **`Render`.** Each row is truncated to `width` and padded to exactly `width` cells. A cursor row is band-painted with `BGSelection`, and a tinted row with `BGSurface`, as the inner rows are today. There is no outline. Keep the width floor that makes every line one width.
4. **Rows (`running.go`).**
   - **Width.** Running rows and cards take the full width.
   - **Split.** Remove the `split` parameter from `runningRow`, `measure`, `measureText`, `progressText` and `fractionText`, with the split branch in `fractionText`.
   - **`heldRow(h HeldItem, in Input, w int) Row`:**
     - the `▌` edge, glyph and id in the kind's colour, laid out as `runningRow` does;
     - the title, muted;
     - then the outcome word, in the success colour for `done`, the warning colour for `outcome unknown`, and the error colour otherwise;
     - then ` · ` and the summary, muted, when there is a summary.
     - On the right, the duration from `formatElapsed`, muted, then `✓` in the success colour, `⚠` in the warning colour for `outcome unknown`, or `✗` in the error colour.
     - It is not tinted. Its target is `TargetWork` with the item's `TaskKey`.
   - **`runRow(r RunRow, in Input, w int) Row`:** `runningRow`'s layout with `glyph.Running` and the id `run` in the primary accent, the title, the summary as the measurement, and the elapsed time since `StartedAt` with the spinner on the right. It is tinted and has no target.
5. **Plan (`plan.go`).**
   - `planStacked` becomes `planRows(in Input, w int) []Row`: the same two columns, with no header returned. Each row is indented one cell (decision 9). A finished list is not drawn by it.
   - `planEntries` appends the stale entry when `in.Stale > 0` (decision 8).
   - `planFolded` becomes `planLine(in Input, w int) Row`, the one-row form, with no border characters. It holds, indented one cell:
     - the marks (`planMarks`, blocks past `planMarksMax`);
     - two spaces, the position, two spaces, and the active item's text. The position is the in-progress item's `N/M`, else done/total.
     - the stale entry's text last, when stale.

     It truncates on a cell boundary. It has no target.
   - `idleRow` keeps only its finished form, renamed `finishedRow`, indented one cell, with no target.
   - Delete `planHeader`, `planSplit` and `wrapCells`.
6. **`compactRow`** drops the state part and keeps the counts and `plan D/T` (decision 10).
7. **TUI callers.**
   - `boxInput` (`livebox_view.go`) stops setting `Busy`, `State`, `Hints`, `Mode` and `Live`. Task 3 already moved the state to the status line.
   - `handleBoxClick` reads the target at column `msg.X`: there is no `│ ` outline any more, so drop the `-2`.
   - `model.go`: delete the `liveBoxMode` field and its initialiser.
   - `keypress.go`: delete the whole `case "ctrl+t"` and the `livebox` import. The key then reaches the input in every state, as it already did whenever the input held text.
   - `help/help.go`: delete `PlanActive` and its `Ctrl+T plan` hint. `status.go` stops setting it in `footerHints`.
   - `view.go`: the box comments say "the tray".
8. **Tests in `livebox`.**
   - **`box_test.go`:**
     - `busyInput` loses `Busy`, `State`, `Hints`, `Live` and `Mode`, and gains one held item: `k6`, a finished command `job-0` that started before `k1`, with outcome `done` and summary `ok`.
     - Rewrite `TestShedOrder` for the six steps. The unshed tray is:
       - one held row, first in start order;
       - `k1`'s open card (five rows) and the other four running rows;
       - two plan rows.

       That makes 1 + 5 + 4 + 2 = 12 rows. Each step's assertion names its row:
       1. the one-line plan;
       2. `✓ 1 finished`;
       3. `+2 more · /work`, with the blocked `k5` among the three kept;
       4. no `↵ collapse`;
       5. one running row (`k5`) with `+4 more · /work`;
       6. one row equal to `⧉2 ┆2 ○1 plan 2/4`.
     - `TestBoxNeverExceedsMaxRows` and `TestPlanBudgetBelowOneStillSheds` drop their mode loop.
     - `TestTargetsMatchRows` expects the held row's stop first, then the running rows, the more row, and the plan items.
     - `TestEveryRowIsExactlyTheWidth` drops its modes.
     - `TestRenderNarrowerThanTheOutlineStaysConsistent` becomes `TestRenderStaysOneWidthWhenNarrow`, checking that every line keeps the floor width.
     - `TestCursorRowPaintsTheSelectionBand` and `TestOneExpandedCard` stay.
     - `TestBoxEmptyWhenNothingLiveAndNoPlan` becomes `TestTrayEmptyWithNothingToPin`.
     - Delete `TestLivePartDrawsTheBoxWithoutTheTurnClock`, `TestSplitSkippedBelowNinety` and `TestHintsDropBeforeTheState`.
     - `TestIdlePlanIsOnePlainRow` becomes `TestIdlePlanDrawsItsRows`: with no work, a 4-item plan is two plan rows, each starting with one space.
   - **Add to `box_test.go`:**
     - `TestHeldRowKeepsItsSlot`: a held item between two running items by start time draws between them, untinted, with `done · ok`, its duration and `✓`.
     - `TestRowsDoNotMoveWhenOneBlocks`: blocking the middle of three running items leaves the order unchanged when uncapped. Capped at 1, the blocked one is kept.
     - `TestRunRowComesFirstAndIsNoStop`: with `Run` set, row 0 holds `run`, the title and the summary, and no stop names it.
     - `TestNoFrameRows`: no rendered line of `busyInput` contains `╭`, `├─`, `╰` or `│`.
   - **`plan_test.go`:**
     - `TestPlanWindowAroundTheActiveItem`, `TestShortPlanShowsEveryItem`, `TestStackedPlanUsesTwoColumns` and `TestStackedRowsKeepBothColumnTargets` call `planRows`.
     - `TestFinishedPlanHasNoItemRows` becomes `TestFinishedPlanIsOneRow`, checking `finishedRow` and that `Plan` draws only that row for a finished list.
     - `TestFoldedBorderUsesBlocksPastTwelve` becomes `TestPlanLineUsesBlocksPastTwelve`.
     - `TestFoldedBorderClosesAtNarrowWidth` becomes `TestPlanLineFitsNarrowWidths`: `planLine` is never wider than the width from 10 to 40 cells.
     - `TestIdleRowSaysDoneOrActive` keeps its done case, for `finishedRow`.
     - Delete `TestSplitPaneWrapsALongTodo`.
     - Add `TestStalePlanAddsAnEntry`.
   - **`running_test.go`:** the shape helper drops `split`. Delete `TestSplitRowKeepsOnlyBarAndPercent`, `TestNextSkipsSplitWhenNarrow` and `TestParseModeDefaultsToStacked`.
   - **`frames_test.go`:**
     - Frames `busy`, `held`, `idle`, `card` and `compact` at 80×24, 96×34 and 120×40.
     - `busy` is `frameInput` without its live part, mode, state or hints. `held` adds one held item. `idle` has one running download and the plan. `card` is today's `expandedInput`. `compact` is `busy` with `MaxRows` 1.
     - The `rows-` frames keep their shapes, and add `rows-held-done`, `rows-held-exit-1` and `rows-plan-run`.
     - Delete every old file under `testdata/`, regenerate with `-update`, and review each new frame against §5.5 and §5.6 and the Decision frames in `pinned-work.md` before committing.
9. **Tests in `internal/app/tui`.** A search of the package's tests for the outline characters and the removed API at `fc0bceba` found these. Any other failure in this task means a test read the box's outline or modes; fix it the same way and name it in the commit message.
   - **`keypress_test.go`:** `TestCtrlTCyclesThePlanLayout` becomes `TestCtrlTIsUnbound`. On an idle model with an empty input, Ctrl+T opens no dock panel and sets no flash.
   - **`help/help_test.go`:** `TestFooterShowsPlanToggleWhenPlanActive` becomes `TestFooterHasNoCtrlT`: no footer state shows `Ctrl+T`.
   - **`view_test.go`:**
     - `TestNoBlankRowAboveTheBox` becomes `TestNoBlankRowAboveTheTray`. Its row at `m.boxTop()` is the first plan row (it holds `first task`), in place of the `╭─`/`working` check.
     - `TestTodoProgressRowCarriesTurnElapsed` drops its `▰▱▱▱`/`1/4` header checks and asserts that the tray's rows hold `second task`.
   - **`tasks_test.go`:**
     - `TestLeftoverPlanIsOneIdleRow` becomes `TestLeftoverPlanShowsItsRows`: `boxOut` is one row holding `· A` and `· B`.
     - `TestTodoDividersOrderTheWorkAndTheBandCarriesProgress`: the box assertions become "the tray names `Write`", with no `1/4` header check.
   - **`livebox_view_test.go`:** `TestBoxStateShowsTheSDDRun` waits for Task 6; until then, delete its border assertion and keep its clock check against `turnStateSegment`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/livebox/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/...
grep -rn 'livebox\.\(Mode\|Stacked\|Split\|Folded\|ParseMode\|Next\|LeftWidth\|InnerWidth\|TargetLive\|Live\b\)' internal/ ; echo "exit $?"
go vet ./internal/app/tui/... && gofmt -l internal/
```

Expected:
- `livebox` passes;
- the TUI packages fail only on the Assumptions list;
- the grep prints nothing (exit 1);
- vet is clean, and `gofmt -l` prints nothing.

## Task 5: Finished items hold their row until the next prompt

**Goal:** a finished job or subagent keeps its tray row until a turn starts. The row shows the same outcome and summary as its completion event, and Enter or a click on it scrolls to that event. A finished plan shows one row until the next prompt.

**Files:**
- `internal/app/tui/model.go`, `viewport_render.go`, `livebox_view.go`, `browse.go`, `commands_dispatch.go`;
- tests: `livebox_view_test.go`, `browse_test.go`, `model_test.go`.

**Steps:**

1. **Fields (`model.go`).** Add the Contracts' fields.
   - `New` sets `heldSince` from `m.now()` after the options run, so an injected clock is used.
   - At each of the three turn starts, right after `m.turnStartedAt = m.now()`:
     - `model.go:4486` in `startAgentRun`;
     - `model.go:4635` in `startSDDAuthoring`;
     - `commands_dispatch.go:469` in the `review` command's effect.

     At each one:
     - set `heldSince` to the same instant;
     - when every item of `m.band.Tasks` is completed, set `planDismissed` to the list's fingerprint.

     Put both in one helper, `startHeldWindow()`, called from all three sites.
   - The fingerprint joins each task's ID, content and status.
2. **Completion index (`viewport_render.go`).** Right after `m.band = viewmodel.BandOf(...)`, before the content-signature check, rebuild `m.completions`. Walk every node of `turns`, and keep each `KindCompletion` node with a non-nil `Completion` and a non-empty `WorkKey`.
3. **`boxInput` (`livebox_view.go`).**
   - **Held.** For each snapshot item that is not `WorkActive` but is in a terminal state (`completed`, `failed`, `stopped`, `unknown`) and is not a foreground-attached command (`WorkActive`'s first rule), add a `HeldItem` when `EndedAt.After(m.heldSince)`. Its outcome, summary, failure and duration come from `m.completions[it.TaskKey]`, else decision 3's fallback, with the duration `EndedAt − StartedAt`.
   - **Plan.** Leave the plan empty when every task is completed and the fingerprint equals `planDismissed`.
4. **Enter and click on a held row.** In `handleBoxClick` and `enterBoxStop`, a `TargetWork` key that is not a running item (`workItemByKey` finds it not `WorkActive`) scrolls to `m.completions[key].node` with `scrollToNode`, and does not toggle a card. A running item toggles its card, as today.
5. **Tests.**
   - **`livebox_view_test.go`:**
     - Add `TestFinishedItemHoldsItsRowUntilTheNextPrompt`. Set `m.heldSince` and the items' times explicitly, so the test does not depend on the wall clock.
       - A running command that finishes after `heldSince` keeps its row, untinted, with `done`.
       - Calling `startHeldWindow()` (as a prompt does) drops it.
       - An item that ended before `heldSince` is never shown.
     - Add `TestHeldRowMatchesItsCompletionEvent`.
       - Record a job exit with `m.state.AddJobExit`, as `click_test.go` does, with an execution ID, exit code 1 and an output line.
       - Build the matching snapshot item's `TaskKey` with `viewmodel.WorkKey` from the exit's conversation, epoch and execution ID, as `completion.go` builds the event's key.
       - The held row's outcome (`exit 1`) and summary equal the completion event's.
     - Add `TestFinishedPlanStaysUntilTheNextPrompt`. A finished list is one `✓ Plan 2/2 done` row; after `startHeldWindow()` it is gone; a new open list shows again.
   - **`browse_test.go`:** add `TestBrowseEnterOnAHeldRowScrollsToItsEvent`.
     - Start from `finishedSubagentFixture`, and add 40 filler messages so the event can scroll out of view.
     - Set a terminal snapshot item whose `TaskKey` is the view's `WorkKey` (minted by `RegisterSubagent`), with `EndedAt` after `heldSince`.
     - Enter on its tray stop puts the completion event's region in view, and opens no card (`workExpanded` stays "").
   - **`model_test.go`:** `TestFinishedPlanIsOneIdleRow` stays as it is. Its list finished without a prompt since, so it is still drawn.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/ -run 'Held|FinishedPlan|FinishedItem|Browse' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/...
```

Expected: the named tests pass, and the package fails only on the Assumptions list.

## Task 6: The plan-run row

**Goal:** an active SDD run or swarm is the tray's first row.

**Files:** `internal/app/tui/livebox_view.go`, `internal/app/tui/run_panel_test.go`, `internal/app/tui/livebox_view_test.go`.

**Steps:**

1. **`boxInput`.** While busy (`m.busy` and `m.turnStartedAt` set):
   - an active SDD run sets `Run` to:
     - title: the plan's name, else `plan run`;
     - summary: `runPanelSummaryText(sdd, now, 60)`;
     - start: `m.turnStartedAt`;
   - else an active swarm sets it to: title `swarm`, summary `swarmStripText(swarm)`, the same start.

   These are the two cases `boxInput` turns into `in.State` today.
2. **Tests.**
   - `TestSDDRunShowsInTheBoxBorder` (`run_panel_test.go`) becomes `TestSDDRunIsTheTraysFirstRow`: the tray's first row holds `run` and `task `.
   - `TestBoxStateShowsTheSDDRun` (`livebox_view_test.go`) is deleted, since the new test covers it and the clock is the status line's (Task 3).
   - Add `TestSwarmIsTheTraysFirstRow`, as the SDD test, with `swarm`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/ -run 'SDDRun|Swarm' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pe-suite go test ./internal/app/tui/...
```

## Task 7: Retire `[tui.live_box]`

**Goal:** the setting still loads but does nothing, warns once, is never saved, and is gone from `/settings`. This is the `[tui.transcript]` treatment.

**Files:**
- `internal/app/config/types.go`, `defaults.go`, `file_types.go`, `merge.go`, `save.go`, `layers.go`, `diagnostics.go`, and their tests;
- `internal/app/tui/settings/frames_tui.go` and its test.

**Steps:**

1. **Config.**
   - Delete `TUIConfig.LiveBox` and `LiveBoxConfig` (`types.go`), the default (`defaults.go`), the merge (`merge.go`) and the save (`save.go`).
   - `fileLiveBox` stays, with a comment saying it mirrors the retired `[tui.live_box]` table as `fileTranscript` does.
2. **Diagnostic.**
   - `Layers` gains `LiveBoxIgnoredBy []LayerID`, filled by `liveBoxIgnoredBy(user, project)`, written as `transcriptIgnoredBy` is: a layer counts when it sets `layout`.
   - `Diagnose` adds a step after step 11 with the Contracts' path and message, as step 11 does. Renumber the later step comments.
3. **Settings.** Delete the `tui.live_box.layout` field from `interfaceFrame`.
4. **Tests.**
   - Delete `TestTUILiveBoxLayoutDefaultsToStacked`, `TestTUILiveBoxLayoutRoundTrips` and `TestInterfaceFrameHasPlanLayoutEnum`.
   - Add `TestDiagnoseWarnsOnceForRetiredLiveBoxKey`, as `TestDiagnoseWarnsOnceForRetiredTranscriptKeys` does:
     - both layers set the key, and there is exactly one diagnostic, naming both;
     - a save does not write `live_box`;
     - defaults produce none.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pe-suite go test ./internal/app/config/... ./internal/app/tui/settings/...
grep -rn 'LiveBox\b\|LiveBoxConfig\|TUI.LiveBox' --include='*.go' internal/ ; echo "exit $?"
```

Expected: both packages pass, and the grep prints only the `fileLiveBox` mirror and the diagnostic helper.

## Task 8: Verification, docs and changelog

**Goal:** the phase is verified by the suite and in a real session, and the docs describe what shipped.

**Files:** `README.md`, `CHANGELOG.md`, `docs/tui-simplification/feature-spec.md`.

**Steps:**

1. **Full verification.**

   ```bash
   cd /Users/alecpullen/projects/marshal
   TMPDIR=/private/tmp/pe-suite CGO_ENABLED=1 go test -timeout 20m ./... 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
   go vet ./... && gofmt -l .
   ```

   Expected: every failure is on the Assumptions list. vet is clean, and `gofmt -l .` prints nothing.
2. **A real session.**
   - Build with `CGO_ENABLED=1 go build -o /private/tmp/pe-suite/marshal ./cmd/marshal`.
   - In a scratch git repository, ask for a short plan, a background command that runs about a minute (`sleep 60; echo done`), and a subagent task.
   - Confirm:
     - the step in progress streams at the end of the transcript and stays where it was when it settles;
     - the tray has no border or rule, with running rows above the two-column plan;
     - the status line shows `working` and the elapsed time while busy;
     - a wrapped narration keeps its `▸ N/M` on its first line;
     - when the command ends, its row stays, untinted, with `done`, until you send the next prompt;
     - Ctrl+T does nothing on an empty input.
   - Record what was seen in the commit message. If something differs, record it and do not change code in this task.
   - Delete the binary afterwards.
3. **README (`### The TUI`).** Replace the paragraph that starts "While the agent works, a box above the input". The new text says:
   - the step in progress streams at the end of the transcript;
   - a tray above the input pins running background work (subagents, jobs, downloads and tests with their progress bars), finished work until your next prompt, and the plan;
   - the status line shows the turn's clock;
   - `/work` lists all work.

   It does not mention Ctrl+T or `[tui.live_box]`.
4. **CHANGELOG.** The live box never shipped in a release, so edit its unreleased entry instead of adding a removal.
   - Retitle the `**The live box**` group under Changed to `**The tray**`.
   - Rewrite its bullet: running work, finished work until the next prompt, and the plan are pinned in a tray above the input, with measured bars, rates and ETAs. The step in progress stays in the transcript, and the turn's clock is in the status line.
   - Add a second bullet: a wrapped narration keeps its todo tag on its first line.
5. **Spec.** Set the status line to say P-E landed, with this plan's commit range.
6. **Commit** `README.md`, `CHANGELOG.md` and `docs/tui-simplification/` as explicit paths. Merge `p-e-tray` only when the user asks.

**Verify:** step 1's commands. `git status --short` shows only the unrelated untracked docs that were there before.

## Integration notes

- **Nothing else is in flight on these files.** P-D merged before this plan. Daemon P2 plan 3, which moves the renderer onto `WireNode`, has not started; when it does, it ports the tray rather than the box.
- **The web is untouched.** It reads the band and the work snapshot over ACP and never read the box's layout.
- **`TestStatusLineUntrustedIsWarningColored`** is in the baseline. Task 3 touches the status line; if that test starts passing or fails differently, say so in the commit message, but it is not this phase's to fix.

## Self-review

- **Cold-executor test.**
  - Every type and string a test pins is in Contracts. Each step names the function it changes, and the tests to restore, rewrite, delete or add are named one by one.
  - The two judgment steps (reviewing the regenerated frames, and the real session) each have a checklist.
- **Contracts.** The `livebox` types are used by Tasks 4 to 6, the model fields by Task 5, `turnStateSegment` by Task 3, and the diagnostic string by Task 7.
- **Anchors**, all checked at `fc0bceba`:
  - `refreshViewport`'s `SplitLive` call and its two loops;
  - `renderLive`, `lastToolChild`, `boxInput`, `handleBoxClick`, `scrollToPlanItem`, `scrollToNode`, `reconcileWorkExpansion`, `workItemByKey`, `enterBoxStop`, `boxStopAtCursor` and `boxAgentAtCursor`;
  - `scrollLiveRegionAt`, `wheelOverBoxReasoning` and `setRegionOffset`;
  - the step renderer's head block and `rightMeta`;
  - `statusLeftSegments`, `renderStatusLine`'s first-segment rule and its hint-cluster fallbacks, and `help.Footer`'s busy hints (`Ctrl+C stop`) and `PlanActive`;
  - `turnSpinnerFrame`, `spinnerLabel`, `spinnerShowsLabel` and `PinnedSpinnerLabel`;
  - the three `m.turnStartedAt = m.now()` sites, and `New`'s `now` and option order;
  - `viewmodel.WorkActive`, the terminal states, `WorkSnapshot` publishing every active item and the 32 newest terminal ones, `CompletionInfo.WorkKey`, and `viewmodel.WorkKey` as the source of both keys;
  - `BandTask.CompletedAt`, and `SetTodos` storing items verbatim;
  - `runningOrder`, `planWindowAt`, `planStacked`, `planFolded`, `idleRow`, `compactRow` and `Render`;
  - the config's `LiveBox` plumbing, `transcriptIgnoredBy`, Diagnose step 11, and `TestDiagnoseWarnsOnceForRetiredTranscriptKeys`;
  - the README paragraph and the CHANGELOG group.
- **Scope.** Eight tasks. The largest is Task 4, the package rewrite, whose test list is enumerated. The projection, the web and the running-row shapes do not change.
- **Parallel safety.** No task is marked parallel-safe.
- **Placeholders.** None.
- **Agreement with the spec.** §5.5, §5.7 and §5.8 were brought in line with decisions 4 to 10 when this plan was written.
