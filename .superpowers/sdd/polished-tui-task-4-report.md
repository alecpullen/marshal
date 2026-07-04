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
