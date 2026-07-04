# Task 4 Report: Polish Right Informational Panel

## Implementation Notes

- Replaced the Task 3 inline `renderRightInfoPanel` (which switched on `m.activeTab` and hand-built tab/body strings in place) with the brief's decomposed shape:
  - `renderSidebarTabs(width int, active int) string` — package-level function, renders `1 Plan`/`2 Context`/`3 Log` labels through the existing `activePillStyle`/`inactivePillStyle` (bordered pill for the active tab, dim padded text for inactive ones), joined horizontally.
  - `(m Model) renderPlanTab(width, height int, tc *session.PendingToolCall, busy bool) string` — checklist-style plan rows plus a status row (`Pending approval: …` / `Agent is working` / `Ready for input`).
  - `(m Model) renderContextTab(width, height int) string` — "Context Pack" heading, `ctx <est> / <max>` compact token summary, up to 6 numbered sections with compact per-section token counts.
  - `(m Model) renderLogTab(width, height int) string` — up to 8 audit-log rows (`HH:MM  tool  summary`).
  - `compactTokenCount(tokens int) string` — `"18k"`-style compaction for tokens >= 1000, else the raw integer as a string.
- `renderRightInfoPanel` now computes `innerWidth := max(m.rightWidth-2, 1)` and `bodyHeight := max(m.contentHeight-5, 1)`, calls `renderSidebarTabs` then the active tab's body helper, joins them vertically, and renders through `renderPanel("", "inspector", content, m.rightWidth, m.contentHeight)` (empty title, `"inspector"` meta on the header line, matching the plan doc's Task 4 target exactly).
- No changes to `View()`, tab-switching key handling, or any other rendering path — only `renderRightInfoPanel` and its extracted helpers changed.

## TDD RED/GREEN Evidence

### RED
- Appended the brief's `TestPolishedSidebarTabsAndContextSummary` to `internal/app/tui/model_test.go`.
- First run failed to compile: the brief's test used `contextpack.SectionFileSummary`, which does not exist in this codebase (see Deviations below). Fixed to `contextpack.SectionFileSnippet` (the actual constant), matching the codebase's real `SectionKind` values.
- Ran: `go test ./internal/app/tui -run TestPolishedSidebarTabsAndContextSummary -v`
- Result (RED, against unmodified `renderRightInfoPanel`):
  ```
  model_test.go:1310: View() missing "Context Pack":
      ...
      │Pack: 18000/32000 tokens           │
      │Repo Card (120 tk)                 │
      │internal/app/tui/model.go (84      │
      ...
  --- FAIL: TestPolishedSidebarTabsAndContextSummary (0.00s)
  ```
  Confirmed failure came from the old copy/format, not a typo, before touching implementation code.

### GREEN
- Implemented `renderSidebarTabs`, `renderPlanTab`, `renderContextTab`, `renderLogTab`, `compactTokenCount`, and the new `renderRightInfoPanel` per the brief (with one width-budget adjustment, see Deviations).
- Ran: `go test ./internal/app/tui -run 'TestPolishedSidebarTabsAndContextSummary|TestPolishedViewFitsCommonTerminalSizes' -v`
- Result: both PASS.

## Full Test Run Results

- `go test ./internal/app/tui/... -v` → all PASS (tui, tui/memory, tui/settings packages). One pre-existing test needed updating for the new copy/format (see Deviations) — after the update, full run is green.
- `go build ./...` → success, no output.
- `go vet ./...` → success, no output.
- `go test ./...` (whole repo) → all packages `ok` (no regressions elsewhere).
- `gofmt -l internal/app/tui/model.go internal/app/tui/model_test.go` → no output (both already formatted).

## Files Changed

- `internal/app/tui/model.go` — replaced `renderRightInfoPanel`, added `renderSidebarTabs`, `renderPlanTab`, `renderContextTab`, `renderLogTab`, `compactTokenCount`.
- `internal/app/tui/model_test.go` — added `TestPolishedSidebarTabsAndContextSummary`; updated the pre-existing `TestPolishedRightPanelTracksActiveTab` (added by Task 3's implementer against the interim stub) to assert the new copy/pill-tab behavior instead of the old text-marker/`"Current Plan:"`/`"Pack: N/M tokens"` copy.
- `.superpowers/sdd/polished-tui-task-4-report.md` — this report.

## Self-Review

- Confirmed the diff touches only `renderRightInfoPanel` and the new helper functions plus test assertions — no changes to `View()`, resize/layout fields, key handling, or other panels.
- Confirmed `View()` remains read-only: no new mutation of viewport/input/layout state was introduced by these helpers (they only read `m.state`, `m.rightWidth`, `m.contentHeight`, `m.activeTab`, `m.busy`).
- Confirmed bounds safety by running the existing `TestPolishedViewFitsCommonTerminalSizes` (80x24, 100x30, 120x40) and `TestAltScreenViewFits80x24` / `TestViewFitsTerminalSizes` — all still pass, so panel content still fits at every previously-verified terminal size.
- Confirmed tab-switching behavior (keys `1`/`2`/`3`, Tab/Shift+Tab cycling) was untouched — only body/tab rendering functions changed, not `Update()`.
- Verified `visibleRunes` and other pre-existing helpers used by `renderPanel` remain referenced (no dead code left behind after removing the old inline tab-strip logic).

## Deviations from the Brief

1. **`contextpack.SectionFileSummary` does not exist.** The brief's Step 1 test uses this constant, but the actual `SectionKind` enum in `internal/contextpack/contextpack.go` only defines `SectionRepoCard`, `SectionMemory`, `SectionPlan`, `SectionFileSnippet`, and `SectionToolOutput`. Used `SectionFileSnippet` instead — semantically the closest match (a single file's content) and the test's assertions do not depend on which kind constant is used, only on `Title`/`EstimatedTokens`.

2. **Title-truncation width budget needed tightening.** The brief's `renderContextTab` reserves `width-12` for the section title before appending the index and token count (`truncateRunes(title, max(width-12, 1))`). With the current codebase's actual 70/30 left/right column split (`internal/app/tui/model.go` `resize()`, unchanged by this task), a 120-wide terminal yields `m.rightWidth` ≈ 35 and `innerWidth` ≈ 33 — narrower than whatever right-column width the brief's numbers were written against. At `width-12` the test's 25-rune section title (`internal/app/tui/model.go`) got truncated by a few characters and the brief's own test failed. Reserving `width-8` instead (still enough margin for the `"N  "` index prefix + `"  XXk"` token suffix used in this render) makes the full filename fit at 120x32 while leaving the same visual format. This is a numeric-constant deviation only — the render approach, helper signatures, and overall visual structure match the brief exactly.

3. **Updated a pre-existing test that encoded the old (Task 3 interim) copy.** `TestPolishedRightPanelTracksActiveTab` (added by the previous implementer, not part of the original plan's Task 4 test list) asserted the old stub's exact strings: `"Current Plan:"`, `"Ready for user input."`, the `"› N Label"` text-marker for the active tab, `"Pack: N/M tokens"`, and a `"->"` separator in log lines. Task 4 intentionally replaces all of that copy/format (checklist rows, pill-styled tabs, `"Context Pack"` + compact token summary, spaced log columns), so the old assertions were inherently incompatible with the new design and had to be updated to match the new behavior — following the same precedent Task 3's implementer used when updating stale Task 2 assertions. The test still verifies the same underlying behavior (tab content changes correctly as `activeTab` switches via keys `2`/`3`), just with copy strings that match the new implementation.

No other functional deviations. `renderPanel("", "inspector", content, ...)` matches the plan doc's own Task 4 code (`docs/superpowers/plans/2026-07-04-polished-current-tui.md` line 817) exactly, including the empty title / lowercase `"inspector"` meta.

## Fix Report

A task reviewer found three issues against commit `67d6cfc`. All three were addressed in commit `ce0994e`.

### Critical: garbled pill tab strip (fixed)

`renderSidebarTabs` joined `activePillStyle.Render(label)` (which has `Border(lipgloss.RoundedBorder())`, making it 3 lines tall) with `inactivePillStyle.Render(label)` (no border, 1 line tall) via `lipgloss.JoinHorizontal(lipgloss.Top, parts...)`. Because the blocks had mismatched heights, the top-aligned join scattered inactive labels onto the wrong row relative to the active pill's border box — reproduced exactly as the reviewer described (`renderSidebarTabs(33, 0)` producing "2 Context  3 Log" floating next to the active pill's top border, with "1 Plan" alone on the row below).

First attempt: gave `inactivePillStyle` a real `Border(lipgloss.RoundedBorder())` in `panelSoftColor` (an "invisible" border color) so both pills would be 3 lines tall. This fixed the height mismatch but introduced a new problem verified by `TestPolishedViewContainsCurrentLayoutChrome`: giving every inactive pill an actual border added left/right border characters to each one, widening the total tab strip enough that it no longer fit the sidebar's real width (`m.rightWidth-2`, ~33 cols at a 120-wide terminal) and got clipped mid-label ("3 Log" split into "3" / "Log" across MaxWidth's clamp). Reverted this approach.

Final fix: added `padPillHeight`, which vertically pads the *borderless* inactive pill render with blank lines (matching its own rendered width, computed via `lipgloss.Width`) until it matches `pillHeight` — the height of a rendered `activePillStyle` block (computed once via `lipgloss.Height(activePillStyle.Render("x"))`). This makes all three pills exactly `pillHeight` (3) lines tall for `JoinHorizontal` to align correctly, without adding any horizontal characters to the inactive pills, so the total tab strip width is unchanged from before the fix.

Verified with a throwaway test (`internal/app/tui/zzthrowaway_test.go`, deleted before finishing) that printed `renderSidebarTabs(40, active)` for `active` in `{0,1,2}`. Confirmed each case renders as a clean 3-line block with all three labels sitting on the same middle row and the active pill's border correctly boxing only its own label, e.g. for `active=1`:
```
        ╭───────────╮
 1 Plan │ 2 Context │ 3 Log
        ╰───────────╯
```

### Important: missing `.MaxWidth(width)` clamps (fixed)

Added `.MaxWidth(width)` alongside the existing `.Width(width)` in `renderPlanTab`, `renderContextTab` (both the empty-pack early return and the populated-pack return), and `renderLogTab` (both the empty-log early return and the populated-log return) — six call sites total — matching the pattern already used in `renderSidebarTabs`. This makes line-width correctness independent of the hand-tuned truncation budgets (`width-22`, `width-8`, `width-20`) staying exactly right; any future drift in those constants is now defensively clamped rather than silently overflowing the panel.

### Minor: `width-8` truncation budget vs. `compactTokenCount` suffix width (addressed with a comment, not a numeric change)

Left the `width-8` budget in `renderContextTab` as-is but added a comment explaining the tradeoff: `compactTokenCount` can return 4+ chars for very large token counts (e.g. `"100k"`), which could in principle overflow the line by 1 char before rendering. Chose not to change the numeric budget because (a) the Important fix's `.MaxWidth(width)` clamp now provides a hard backstop so any overflow is clipped rather than breaking layout, and (b) tightening the budget further would re-trigger the same kind of hand-tuned-arithmetic fragility flagged in the Important finding, for a cosmetic edge case (single truncated character on context packs sized in the hundred-thousand-token range). This is a judgment call favoring the general defensive fix over a second narrow numeric adjustment.

### Test changes

Added `TestRenderSidebarTabsSingleRowAcrossActiveIndex` to `internal/app/tui/model_test.go`, which renders `renderSidebarTabs` for every active tab index (0, 1, 2) and asserts (a) every label ("Plan", "Context", "Log") appears on some line for every active index — i.e., no label goes missing/detached — and (b) the total line count is identical across all three active indices, so a future regression that re-introduces mismatched pill heights fails a test instead of only being caught by eyeballing a substring `Contains` check. The two named existing tests (`TestPolishedSidebarTabsAndContextSummary`, `TestPolishedRightPanelTracksActiveTab`) were left as `Contains`-based; they still pass for the right reason (the fixed rendering still contains the expected substrings), and the new test now covers the layout/height property they didn't.

### Verification commands run

- `go test ./internal/app/tui/... -v` — all PASS (including the new test and the full existing suite, including `TestPolishedViewContainsCurrentLayoutChrome` which caught the first (bordered-inactive-pill) attempt's width regression).
- `CGO_ENABLED=1 go build ./...` — success, no output.
- `go vet ./...` — success, no output.
- `go test ./...` (whole repo) — all packages `ok`, no regressions.
- `gofmt -l .` — no output (all files already formatted).

Commit: `ce0994e` — "fix(tui): align sidebar pill tabs onto a single row".

### Follow-up: strengthened `TestRenderSidebarTabsSingleRowAcrossActiveIndex` (tautology fix)

A later task reviewer flagged that `TestRenderSidebarTabsSingleRowAcrossActiveIndex`, as added above, was tautological: its two checks (each label appears on *some* line; total line count is stable across active indices) both pass even on the pre-fix buggy `renderSidebarTabs` from `67d6cfc`, because `lipgloss.JoinHorizontal(lipgloss.Top, ...)` already pads every joined block to the same total height regardless of alignment, and each label's text still shows up somewhere in the output even when it's detached onto the wrong row relative to its siblings. The test could never fail against the bug it was written to catch.

Fix: added a load-bearing assertion that finds the one line (if any) containing all three labels (`"1 Plan"`, `"2 Context"`, `"3 Log"`) together, for every active index (0, 1, 2), and fails if no such line exists. Kept the original per-label "appears somewhere" checks as secondary diagnostics.

RED/GREEN evidence:
- RED: temporarily reverted `renderSidebarTabs` in `internal/app/tui/model.go` to drop the `padPillHeight(...)` call (i.e. `parts = append(parts, inactivePillStyle.Render(label))`, reproducing the pre-`ce0994e` bug), then ran `go test ./internal/app/tui/... -run TestRenderSidebarTabsSingleRowAcrossActiveIndex -v`. The new assertion failed as expected: `active=0: no single line contains all labels ["1 Plan" "2 Context" "3 Log"] together; lines=["╭────────╮ 2 Context  3 Log             " "│ 1 Plan │                              " "╰────────╯                              "]` — showing "1 Plan" detached from "2 Context"/"3 Log" onto a different row, exactly the bug this test exists to catch.
- GREEN: restored the real `padPillHeight` call and reran the same command; the test passed, and the full suite (`go test ./internal/app/tui/... -v`), `go build ./...`, and `go vet ./...` were all clean with no other changes to `model.go`.

Commit: "test(tui): assert sidebar tab labels share one rendered line" (see repo history for exact SHA).
