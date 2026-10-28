# P7 TUI layout and overflow — implementation plan

Date: 2026-10-28. Status: executed (tasks 1–6); verification recorded in
[triage item 8](../triage.md#p2--interaction-and-visibility).
Base: `eefacc86`.
Requirements: [triage item 8](../triage.md#p2--interaction-and-visibility),
specifically the audit findings for [field lists](../audit.md#shared-field-lists-budget-logical-rows-instead-of-display-lines),
[preflight](../audit.md#preflight-cast-list-can-hide-the-selected-role-or-blocking-error),
[plan-run gates](../audit.md#plan-run-gate-can-hide-the-question-it-asks-the-user-to-answer),
[inspector metadata](../audit.md#inspector-silently-omits-metadata), and
[session rules](../audit.md#session-sheet-can-hide-standing-approval-rules).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** within the available physical terminal rows, keep the current
selection or answer control visible and make overflow information reachable.

**Non-goals:** change approvals, strategy selection, gate answers, session
rules, persistence, tool policy, web UI, or triage items 9–14. This is phase 7
in the sequence and addresses one P2 triage group.

**Assumptions and decisions:**

- The host's minimum terminal size remains 80×24; a docked panel may receive
  much less than 24 rows. Respect the actual `maxHeight` passed to each panel.
  For less than two usable body rows, show the available content without
  claiming invisible content is visible; don't panic. Preserve existing host
  behavior below minimum size.
- Count display **lines** after rendering and wrapping, not slice elements or
  bytes. Count width in terminal cells via the existing ANSI-aware utilities.
  Overflow indicators consume the same budget as content. Recompute on resize.
- Use page navigation to reach long content. Keep hints contextual and do not
  steal typing, picker keys, answer submission, or existing cancel actions.
  Do not add a requirement to read every row before submitting or starting.
- Shared list selection scrolls automatically; preflight selection scrolls
  when it changes, but manual paging can move away from it so diagnostics on
  non-role rows are reachable. The current role remains the override target.
- Inspector metadata gets a distinct `metadata` tab before ordinary sections,
  using the existing inspector section navigation and scrolling. This avoids
  competing independent scroll areas and keeps all existing detail data.
- Session rules get a read-only rules detail mode within the sheet: Enter on
  rules opens it; Escape returns to the sheet. Existing Enter commands for
  other sections stay unchanged. No rule is modified by this mode.
- Use local fixtures and isolated test environment conventions; preserve
  unrelated untracked files. All tasks are sequential and independently
  verifiable; commit named files only after each task passes.

## Contracts

No cross-task exported type or API is needed. Each task owns one panel
package and its tests. `dock.Panel`, `sessionsheet.Section`, `inspector.Detail`,
`gatepanel.AnswerMsg` and `castlist.StartMsg` stay unchanged. Do not change
`chrome.ClipLines` globally: several unrelated callers depend on its existing
behavior, including its treatment of non-positive heights.

## Task 1: Budget shared field lists by physical line

**Goal:** field-list views fit their assigned height and keep the focused
field or active editor visible even when rendered rows stack.

**Files:** `internal/app/tui/listpanel/fieldlist.go`,
`internal/app/tui/listpanel/fieldlist_test.go`; add a consumer regression in
`internal/app/tui/skills/panel_test.go`.

**Steps:**

1. In `FieldList.View`, split every rendered element into physical lines
   before recording its start and appending it, including stacked `chrome.Row`
   output, headers, errors, descriptions, inline options and the add prompt.
   Track the focus line from that physical sequence. During scalar edit focus
   the line containing the input; during collection add focus the add input;
   during inline enum picking focus the selected option, otherwise focus the
   selected field's marker. Keep setters, row IDs and navigation unchanged.
2. Replace the local `clipLines` wrapper with a local bounded physical-line
   window: height <= 0 returns empty, height 1 shows the focus line without
   indicators, and height >= 2 reserves an indicator only when it fits along
   with the focus. Keep the focus visible, clamp all slices, use `↑ more` /
   `↓ more` where space permits, and never exceed height. When a stacked
   field cannot fully fit, prioritize the focus line. Do not change shared
   `chrome.ClipLines`.
3. Add `TestFieldListPhysicalHeightAndFocus` covering several stacked rows,
   selection near both ends, header and description, height 0/1/2, Unicode
   cell widths, and resize. Add `TestFieldListFocusedEditorAndPickerVisible`
   covering edit, failed setter error, add prompt, and an enum selection below
   the window. Assert physical height and real `Update` navigation, and that
   the displayed focus corresponds to the selected action. Add
   `TestSkillPanelStackedFieldsKeepSelectionVisible` using the real skills
   panel frame at narrow width to exercise the shared component through a
   consumer, including one action after navigation.

**Verify:**

```bash
go test ./internal/app/tui/listpanel ./internal/app/tui/skills -count=1
gofmt -l internal/app/tui/listpanel internal/app/tui/skills
```

Expected: tests pass; gofmt prints no paths.

## Task 2: Make preflight selection and diagnostics reachable

**Goal:** the selected role is visible after navigation and all preflight
warnings and blocking errors can be read before starting.

**Files:** `internal/app/tui/castlist/castlist.go`,
`internal/app/tui/castlist/castlist_test.go`.

**Steps:**

1. In `Panel.View`, build physical rows for metadata, strategy options,
   role rows and diagnostics. Wrap full error/warning text to available cell
   width rather than truncating it; retain the labels, badges and blocked
   indicator. Record each role's first physical line. Keep a panel-owned
   offset, last body height and a flag set by `moveCursor` to bring the new
   selected role into the window. Clamp offset after resize and data changes.
2. In `Update`, add PgUp/PgDn and Home/End to scroll the body independently
   of the selected role; Up/Down continue to select roles and restore selected
   visibility. Picker receives all keys while open as today. Show a position
   or above/below indicator and contextual paging hints whenever content
   overflows, within the height budget. Enter's blocked check, strategy
   cycling, overrides and Escape remain unchanged.
3. Add `TestCastListSelectionVisibleAfterNavigation` for many roles with
   multiline diagnostics and resize, including override selection. Add
   `TestCastListPagesEveryDiagnostic` for a non-role blocking error, a long
   later warning and disabled-strategy reason; navigate via actual `Update`
   and accumulate frames to prove every wrapped segment is reachable. Assert
   Enter is blocked until existing conditions clear and the current override
   target remains correct after manual paging.

**Verify:**

```bash
go test ./internal/app/tui/castlist -count=1
gofmt -l internal/app/tui/castlist
```

Expected: tests pass; gofmt prints no paths.

## Task 3: Keep the plan-run question and report reviewable

**Goal:** the question and full report are reachable while the answer field
and its submit/cancel behavior stay available.

**Files:** `internal/app/tui/gatepanel/gatepanel.go`,
`internal/app/tui/gatepanel/gatepanel_test.go`.

**Steps:**

1. Replace the six-line report cap and truncation in `Panel.View` with
   ANSI-aware wrapping of the complete question and report. Lay out a bounded
   scrollable body above a pinned input line; begin at the question's first
   line. Store scroll offset and last viewport height on the panel, clamp on
   resize and show a compact position/overflow indicator in the body budget.
   If there is only one usable row, prioritize the input and indicate overflow
   in the frame hint where possible.
2. Route PgUp/PgDn and Home/End to body scrolling before the text field;
   leave ordinary arrows and text input to the editor. Preserve nonempty
   Enter => `AnswerMsg` and Escape => `StopMsg`, even after paging. Advertise
   paging together with answer and stop when there is room.
3. Add `TestGatePagesQuestionAndFullReport` with long Unicode question and
   report including lines after the former cap. Accumulate frames through
   actual key updates, assert start and end are reachable, and assert input
   remains visible. Add `TestGatePagingResizePreservesAnswer` to type, page,
   resize in both directions, submit the exact answer and separately cancel.
   Cover constrained dock heights and enforce physical frame height.

**Verify:**

```bash
go test ./internal/app/tui/gatepanel -count=1
gofmt -l internal/app/tui/gatepanel
```

Expected: tests pass; gofmt prints no paths.

## Task 4: Expose all inspector metadata

**Goal:** every metadata key and full wrapped value is reachable without
displacing or changing the existing section bodies.

**Files:** `internal/app/tui/inspector/inspector.go`,
`internal/app/tui/inspector/inspector_test.go`.

**Steps:**

1. Remove the half-height field loop in `Panel.View`. When fields exist,
   prepend a view-only metadata tab to the displayed section sequence, with
   each key and its full value wrapped to the available cell width. Keep the
   subject header and render metadata in the same scrolling body as other
   tabs. The tab is panel-local: do not alter `Detail.Sections` or `Detail()`.
   Initialize selection to metadata when present; on `SetDetail`, preserve the
   selected tab by name where possible and reset offsets as before. Handle
   absent fields/sections and duplicate names by reserving a separate internal
   identity for metadata rather than matching the label alone.
2. Route Tab/Shift+Tab, scroll and copy to the selected displayed tab.
   Copying metadata returns its complete key/value text; copying an existing
   section still returns its original body. Keep `Section()` reporting the
   original section index when one is selected, and use -1 for metadata;
   update existing inspector tests that assume the first ordinary tab starts
   selected when fields exist. Show a scroll
   position or above/below marker and clamp offsets on resize; retain `n`,
   `p`, `o`, Escape and existing ordinary section behavior.
3. Add `TestInspectorMetadataTabReachesEveryField` for many wrapped values
   including late approval, changed arguments, call ID and risk, with page
   navigation and resize. Add `TestInspectorMetadataAndSectionsRemainDistinct`
   for copying, no sections, no fields, a section named metadata, preserved
   tab identity across `SetDetail`, and existing section copy/navigation.
   Assert every frame remains within its allocated height.

**Verify:**

```bash
go test ./internal/app/tui/inspector ./internal/app/tui -count=1 -timeout 2m
gofmt -l internal/app/tui/inspector
```

Expected: tests pass; gofmt prints no paths.

## Task 5: Provide a full session-rules view

**Goal:** all standing rules are readable and the sheet advertises only
actions that the selected section actually supports.

**Files:** `internal/app/tui/sessionsheet/panel.go`,
`internal/app/tui/sessionsheet/section_rules.go`,
`internal/app/tui/sessionsheet/main_test.go`,
`internal/app/tui/sessionsheet/section_rules_test.go`.

**Steps:**

1. In `Panel.Update`, Enter on the selected rules section enters an internal
   read-only detail mode; in that mode Escape returns to the sheet, and
   Up/Down, PgUp/PgDn, Home/End scroll physical lines. Ctrl+B still closes the
   sheet. Derive content from the current `p.data()` each render, so disappearing
   rules show an empty message instead of a stale snapshot; clamp the offset
   on resize and data changes. Other sections retain `commandFor` behavior.
2. In `Panel.View`, render each complete `allow` rule wrapped in the detail
   mode, with an overflow position indicator and scroll/back hints in the
   available budget. In the ordinary sheet, make the rules summary indicate
   when entries are omitted and say Enter opens details. Show the general
   `↵ open` hint only when the selected section has a command or is rules.
   Keep the section interface and its consumers unchanged.
3. Add `TestRulesDetailShowsEveryRule` with many long rules and a short dock
   budget, accumulating wrapped content through real key updates and resize.
   Add `TestRulesDetailLiveDataAndEscape` covering disappearing rules,
   return to sheet, Ctrl+B close, and no mutation. Add
   `TestSheetEnterHintMatchesSelectedAction` for rules, an actionable section,
   and a section without an action; assert both hint and emitted message.

**Verify:**

```bash
go test ./internal/app/tui/sessionsheet -count=1
gofmt -l internal/app/tui/sessionsheet
```

Expected: tests pass; gofmt prints no paths.

## Task 6: Verify composed routing and theme behavior

**Goal:** the host routes navigation to the visible gate and rules detail,
and overflow remains understandable without color.

**Files:** `internal/app/tui/model_test.go`,
`internal/app/tui/sessionsheet_wiring_test.go`,
`internal/app/tui/gatepanel/gatepanel_test.go`,
`internal/app/tui/sessionsheet/section_rules_test.go`.

**Steps:**

1. Add `TestGatePagingThroughModel` using the existing model and gate fixtures:
   open a long-question gate, render at 80×24 and 120×40, send paging and
   resize messages through `Model.Update`, and assert the composed frame
   reaches both ends while input remains visible and the answer reaches the
   existing gate handler. Assert Escape retains the stop behavior.
2. Add `TestRulesDetailThroughModel` using the existing session-sheet wiring
   fixtures: open the sheet with enough rules to overflow, select rules, enter
   details, scroll and resize through `Model.Update`, assert late content in
   the composed frame, then Escape back to the sheet. Assert no slash-command
   message or rule mutation occurs when opening details.
3. Add `TestGateOverflowThemes` and `TestRulesDetailOverflowThemes` in the
   corresponding panel test files. Run the overflow scenario with the
   existing theme-selection helpers for monochrome, a dark theme and a light
   theme; after stripping ANSI, assert labels and position indicators carry
   meaning and every frame fits. Restore theme state after each test.

**Verify:**

```bash
go test ./internal/app/tui -run 'Test(GatePagingThroughModel|RulesDetailThroughModel)' -count=1
go test ./internal/app/tui/gatepanel ./internal/app/tui/sessionsheet -run 'Test(GateOverflowThemes|RulesDetailOverflowThemes)' -count=1
gofmt -l internal/app/tui/model_test.go internal/app/tui/sessionsheet_wiring_test.go internal/app/tui/gatepanel internal/app/tui/sessionsheet
```

Expected: tests pass; gofmt prints no paths.

## Final verification and integration

After all tasks pass, run from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p7-layout ./cmd/marshal
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui/listpanel internal/app/tui/skills internal/app/tui/castlist internal/app/tui/gatepanel internal/app/tui/inspector internal/app/tui/sessionsheet
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p7-layout go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Record actual failures and compare against the baseline in triage. If a
controlling terminal is available, manually exercise keyboard and mouse,
resize, and at least one multiplexer/SSH environment; otherwise record the
missing environments. The current scripted usability suite is help-only, so
do not claim it exercised these panels. After verification, update the five
audit findings and triage item 8 with evidence and limitations, and commit the
plan and named documentation with the implementation.

## Plan self-review

- Five findings have separate owners and runnable package checks; the shared
  field-list fix precedes panel-specific work.
- Physical height, focus, Unicode wrapping, resize, long diagnostics and
  actual update paths are explicitly covered. Existing decisions and side
  effects remain owned by their current paths.
- The full suite has known baseline failures; compare and report instead of
  suppressing them. No implementation has been performed during planning.
