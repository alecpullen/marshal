# P4 postmortem consent — implementation plan

Date: 2026-10-28. Status: executed 2026-10-28 — commits `8c37a029` (task 1)
and `e2997d16` (task 2), run sequentially as this plan requires.
Base: `6771532755828d8504623557194fedff02447076`.
Requirements: [triage item 5](../triage.md#p1--restore-trustworthy-decisions-and-prevent-data-loss)
and [the consent disclosure finding](../audit.md#consent-disclosure-can-be-clipped-or-omitted).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** the user sees the complete agent-analysis access disclosure and both
choices together before accepting the exit postmortem's agent pass.

**Non-goals:** change the postmortem policy, extraction, scoped runtime access,
analysis goal, default selection, the active-work warning, or other triage
items. This is the fourth implementation plan in sequence and addresses the
next **P1** triage item.

**Decisions and constraints:**

- Keep the two-stage question, existing Yes/No selection and result types.
  Do not introduce a forced read-through or change the default choice.
- At the agent-analysis stage the following two lines are mandatory, without
  truncation or omission, above both choices:

  ```text
  Agent analysis has system access and editing approval.
  It can access and edit files outside this workspace.
  ```

  The runtime temporarily grants Act + System during the analysis; the wording
  describes the capability rather than implying that the report is the only
  file reachable. Existing policy and sandbox behavior are unchanged.
- The disclosure and choices must fit together in the actual composed frame at
  80×24, 120×40 and 200×60, including transcript and live tray. There is no
  scroll-only disclosure at those sizes. In a panel too small to fit the full
  disclosure, question and choices, show a resize instruction and refuse the
  affirmative agent decision; `n` and Esc remain safe exits.
- At the agent stage an affirmative `y` or Enter is accepted only after that
  stage's complete content has been rendered visibly at the current geometry.
  Transitioning from stage zero, resizing, or replacing the panel invalidates
  the prior visibility. The parent must additionally gate affirmative keys
  while the terminal is below its supported 80×24 minimum, because the parent
  replaces the entire UI with the too-small screen and does not call panel
  `View` in that state. Ctrl+C and a safe No/Esc path remain available.
- Preserve the P0–P3 work, `NO_COLOR` usability, response ownership and
  `postmortempanel.DoneMsg` result handling. Do not rely on a color-only cue or
  log secrets. No external services or real credentials are needed.
- Existing panel tests directly send keys without rendering; update their
  fixtures to render the agent stage before affirmative decisions rather than
  weakening the visibility invariant. Keep the existing result semantics.

Tasks run sequentially and may be committed after their verification as
`p4-postmortem-consent: task N — <title>`. Stage only named paths; the working
tree has unrelated untracked work. Present this plan for review before running
it.

## Contracts

Task 1 adds this panel method for the parent resize integration in Task 2:

```go
// InvalidateVisibility prevents affirmative agent consent until the agent
// question is rendered again at the current geometry.
func (p *Panel) InvalidateVisibility()
```

The panel tracks visibility for the current stage only. A render at an
insufficient width or height, or a transition into the agent stage, clears it.
The method is idempotent and never changes the selected choice or the result.

## Task 1: Render and gate the complete disclosure

**Goal:** the panel never emits `ResultWithAgent` unless the full agent-stage
question, disclosure and both choices fit in its last current-stage render.

**Files:** `internal/app/tui/postmortempanel/postmortempanel.go`,
`internal/app/tui/postmortempanel/postmortempanel_test.go`.

**Steps:**

1. In `Panel.View`, replace the optional one-line explanation and blank
   separators with the exact two mandatory lines in Decisions. Use physical
   cell widths via `ansi.StringWidth` and the actual inner width of
   `chrome.PanelWithHints` (`pw-2`), not byte counts; account for the header
   and all required body rows. At widths/heights where the mandatory content
   fits, render the question, both lines and both choice rows, with no optional
   blank rows required. Set an agent-stage visibility flag only for that
   complete rendering. At insufficient geometry, clear the flag and render a
   short resize instruction within the given dimensions; do not silently drop
   disclosure. Keep the stage-zero question and its existing behavior.
2. In `Panel.Update`, clear agent-stage visibility whenever stage zero advances
   to stage one. Require the flag for stage-one `y`, `Y` or Enter when Yes is
   selected; if it is false leave the panel open and emit no result. No/`n`/
   Esc remain available even when the flag is false. Implement
   `InvalidateVisibility` from Contracts; navigation preserves the selection
   and does not itself make an undisclosed decision visible.
3. Update `TestStageOneYesRunsAgent`, `TestEnterConfirmsCursor` and
   `TestBothChoicesVisibleAndKeyboardReachableAtRealSizes` to render the
   agent stage before an affirmative decision. Keep existing No, Esc, cursor
   and result tests.
4. Add `TestAgentDisclosureAndChoicesFit`: render stage one at panel widths
   80, 120 and 200, heights 6, 9, 16 and 24, and under `NO_COLOR`; strip ANSI
   and assert both complete lines, question and both choices appear, while
   physical width and height remain within the supplied bounds. Add
   `TestAgentConsentRequiresCurrentVisibleDisclosure`: test unrendered stage,
   insufficient width and height, `InvalidateVisibility`, stage transition,
   subsequent sufficient render, and `y`/Enter; assert no positive result
   while hidden and `ResultWithAgent` after a complete render. Verify No and
   Esc remain distinct safe exits at every size.

**Verify:**

```bash
go test ./internal/app/tui/postmortempanel -count=1
gofmt -l internal/app/tui/postmortempanel
```

Expected: package tests pass and formatting prints no paths. Task 1 protects
the panel's own size budget; Task 2 handles the parent's too-small screen and
the composed frame.

## Task 2: Invalidate and gate consent on parent resize

**Goal:** the full TUI cannot accept agent consent while the panel is hidden
or its earlier rendering belongs to a previous geometry.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/model_test.go`,
`internal/app/tui/frame_geometry_test.go`.

**Steps:**

1. In the `tea.WindowSizeMsg` handling in `Model.Update`/its underlying
   update path, call `InvalidateVisibility` on the active
   `*postmortempanel.Panel` before using the new size. On the next visible
   render the panel recomputes the flag. In the existing top-level minimum-size
   routing, when this panel owns the dock and the terminal is below minimum,
   consume positive selection keys (`y`, `Y`, Enter) before the dock receives
   them; permit `n`, `N`, Esc and the existing Ctrl+C handling. Preserve
   runtime messages and `WindowSizeMsg` routing. Do not block unrelated
   postmortem policies or change the handling of an already-issued DoneMsg.
2. Add `TestPostmortemConsentVisibleInComposedFrame` to
   `frame_geometry_test.go`: open the real panel over a populated transcript
   and tray, drive stage zero to stage one, then render at 80×24, 120×40 and
   200×60. Assert the two full disclosure lines and both choices survive in
   `m.View().Content`, frame geometry is correct, and No/Yes lead to their
   existing results. Repeat the minimum-size case with `NO_COLOR`.
3. Add `TestPostmortemConsentResizeInvalidatesVisibility` to `model_test.go`:
   render stage one, resize to 40×12, press `y` and Enter, and assert the
   panel remains and neither extraction nor agent analysis starts. Resize back
   to 80×24; before rendering, an affirmative key remains blocked; after
   rendering it resolves once. Separately assert Esc/No can decline safely at
   the smaller geometry, and that a newly opened/replaced panel cannot inherit
   another panel's visibility. Use the existing `endTestController` and
   isolated postmortem helpers; assert controller calls and result state, not
   merely the returned command's existence.
4. Retain the current `ResultDeclined`, `ResultExtractOnly` and
   `ResultWithAgent` integration coverage and existing active-work warning,
   shutdown, Send now and prompt visibility tests. Do not modify tests that
   intentionally inject a `DoneMsg` as an already-made decision.

**Verify:**

```bash
go test ./internal/app/tui -run 'Test(PostmortemConsent|EndingSurfacesFitTheTerminal|EndStagesKeepFrameGeometryStable|ExitQuestionIsAskedOnce)' -count=1 -timeout 90s
go test ./internal/app/tui/postmortempanel ./internal/app/tui/dock -count=1
gofmt -l internal/app/tui/model.go internal/app/tui/model_test.go internal/app/tui/frame_geometry_test.go internal/app/tui/postmortempanel
```

Expected: tests pass and formatting emits no paths. The parent size gate is
separate from the panel's own height check: both must be tested through
`Model.Update` and the actual composed frame.

## Final verification and integration

After both tasks pass, run from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p4-postmortem ./cmd/marshal
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p4-postmortem go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Record actual results and compare any failures to the latest integrated
baseline in [triage](../triage.md); do not suppress or silently skip failures.
The scripted usability suite covers help, not this exit flow. Manually exercise
the two questions, 80×24 and a resize below minimum in an isolated scratch
session without invoking a real provider; record whether that was possible.
Update the one audit finding and triage item 5 only after verification, and
commit this plan and the named docs with the work.

## Plan self-review

- The panel render, decision gate, parent size gate, stage transition and
  resize invalidation each have an owning task and named tests. The decision
  remains escapable at insufficient geometry.
- The plan checks the composed frame, not merely the panel's isolated string,
  and preserves scoped runtime behavior and the existing safe outcomes.
- Anchors and existing tests were inspected at the stated base; no new
  implementation or prototype tests were run while authoring.
