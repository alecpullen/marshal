# P5 generated-plan review and recovery — implementation plan

Date: 2026-10-28. Status: executed 2026-10-28 — commits `8ee41eef` (task 1)
and `2f7cff04` (task 2), run sequentially as this plan requires, with task 1
inline and task 2 delegated.
Base: `8a82aa18b3f72eb24e78b8a32eb4bc99dd25a0b5`.
Requirements: [triage item 6](../triage.md#p1--restore-trustworthy-decisions-and-prevent-data-loss),
[unreachable review diagnostics](../audit.md#plan-review-cannot-reach-overflow-diagnostics)
and [the conflicting recovery hint](../audit.md#the-plan-review-recovery-hint-conflicts-with-discard-behavior).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** a user can read all generated-plan inspection diagnostics, keep the
candidate for editing, and deliberately discard it with a reported outcome.

**Non-goals:** implement a plan editor, change `/sdd` execution or inspection,
change Full Auto authoring, change plan parsing or candidate path policy, or
address other triage items. This is the fifth implementation plan in sequence
and addresses the next **P1** triage item.

**Decisions and constraints:**

- Escape closes review and **keeps** the generated candidate, whether the
  inspection is clean or blocked. A transcript message names the retained path
  and says it can be edited before running `/sdd`. Enter still accepts a clean
  candidate and never starts the pipeline; blocked candidates cannot be
  accepted. A nil inspection remains blocked and can still be kept.
- `d` starts discard confirmation; a second consecutive `d` confirms it.
  Any other key disarms it. Escape while armed disarms and stays in the panel;
  a subsequent Escape keeps and closes. The prompt must say what is about to
  be deleted and that the action is distinct from keeping the file. A hidden
  or undersized panel cannot arm or confirm discard. Normal Ctrl+C behavior is
  unchanged.
- Discard targets only the active review's candidate using existing
  `sddauthor.RemoveCandidate` containment checks. An error keeps the panel
  open, preserves the candidate, disarms the action and shows the error in the
  panel so retry or keep is possible. Do not claim a file was removed when the
  removal failed. A queued message from a replaced/closed panel cannot act on
  the new panel.
- The dock remains docked. Wrap diagnostics and long paths to physical cell
  width, escape control characters from paths/diagnostics before rendering, and
  scroll the complete body with Up/Down and PgUp/PgDn. Show an overflow
  indicator and keep action hints reachable. At 80×24, 120×40 and 200×60, every
  diagnostic and its complete message can be reached without losing the
  candidate or accidentally accepting it. `NO_COLOR` must remain usable.
- Preserve the prompt-prefix and TUI/client boundaries, P0–P4 work, and test
  isolation. Use temp repositories/files; do not run real agents or editors.
  Stage only named paths; the checkout contains unrelated untracked work.

Tasks execute sequentially. Each task can be verified and committed as
`p5-plan-review: task N — <title>`. They share `sddreview`, so neither is
parallel-safe with the other. Review this plan before execution.

## Contracts

Task 1 changes the panel-to-host messages in `internal/app/tui/sddreview`:

```go
// KeepMsg asks the host to close this exact review without deleting its file.
type KeepMsg struct{ Panel *Panel }

// DiscardMsg asks the host to delete the candidate of this exact review.
type DiscardMsg struct{ Panel *Panel }

// SetError displays a failed discard in the active panel and disarms discard.
func (p *Panel) SetError(err error)

// InvalidateVisibility disarms discard when the geometry changes.
func (p *Panel) InvalidateVisibility()
```

The host acts only when `msg.Panel` is the currently docked panel; stale or
foreign messages are ignored. Keep the existing `AcceptMsg` semantics and
make its originating-panel identity explicit as well, so a delayed acceptance
cannot close a replacement review. These types carry no policy or runner logic.

## Task 1: Separate keeping from confirmed discard

**Goal:** leaving review preserves the file by default, while explicit
discard is scoped to the active review and reports failures.

**Files:** `internal/app/tui/sddreview/review.go`,
`internal/app/tui/sddreview/review_test.go`, `internal/app/tui/model.go`,
`internal/app/tui/sdd_authoring_test.go`, `internal/app/tui/model_test.go`.

**Steps:**

1. Replace `CancelMsg` with `KeepMsg` and `DiscardMsg` from Contracts. Give
   `AcceptMsg` a `Panel *Panel` field. In `Panel.Update`, Esc emits `KeepMsg`
   when unarmed; first `d` arms, second consecutive `d` emits `DiscardMsg`;
   any other key disarms, and Esc while armed only disarms. Keep Enter's
   `hasErrors` check. Show `d again to discard` and the candidate identity
   while armed, and show `esc keep · d discard` when unarmed. Add `SetError`
   and `InvalidateVisibility` as declared. Render the error and clear/disarm
   on a subsequent discard attempt; use `strutil.ShowControls` for any
   model-written path or error displayed.
2. In `Model.Update`, handle the three messages only if their panel pointer is
   the current `m.dock.Panel()`; ignore stale or foreign messages. `KeepMsg`
   closes the panel and adds a system message naming the retained candidate
   and the next step: edit the file and run `/sdd`. `DiscardMsg` calls
   `sddauthor.RemoveCandidate` with the active panel's path and current
   resolved plans directory; on error call `SetError`, keep the panel open and
   refresh, on success close it and report the removed path. Accept closes the
   panel and leaves the artifact, as today. Do not change
   `sddauthor.RemoveCandidate` or start a runner.
3. Make the panel's positive discard actions visibility-aware: only a render
   that shows the candidate identity, discard action and confirmation state at
   the current panel geometry permits its next `d`. Call
   `InvalidateVisibility` when `WindowSizeMsg` reaches the model. When the
   parent renders its below-80×24 size gate, consume `d`/`D` before the dock
   handles it; Esc remains able to keep the candidate, and Ctrl+C and runtime
   messages continue through their current paths. A resize disarms rather than
   carrying a destructive confirmation into the next geometry.
4. Update `TestEscEmitsCancel` and `TestSDDNewCancelRemovesOnlyCandidate` to
   assert Esc keeps the candidate. Update callers/tests constructing
   `AcceptMsg` to carry the active panel pointer. Add
   `TestDiscardRequiresTwoConsecutiveKeys`,
   `TestDiscardFailureKeepsPanelAndCandidate` and
   `TestStaleReviewMessagesCannotAffectReplacement`. Use a temp candidate,
   a sibling sentinel, and a path rejected by `RemoveCandidate` to force an
   error; assert file and panel state as well as visible error text. Add
   `TestDiscardHiddenOrResizedCannotConfirm` through `Model.Update` at 80×24,
   then below minimum and back; assert no removal until the visible fresh
   confirmation completes. Test clean and blocked inspections and `NO_COLOR`.

**Verify:**

```bash
go test ./internal/app/tui/sddreview ./internal/sddauthor -count=1
go test ./internal/app/tui -run 'Test(SDDNew|Discard|StaleReview)' -count=1 -timeout 90s
gofmt -l internal/app/tui/sddreview internal/app/tui/model.go internal/app/tui/sdd_authoring_test.go internal/app/tui/model_test.go
```

Expected: tests pass, no formatting paths are printed, and failing deletion is
reported without closing the panel. Update existing test expectations rather
than bypassing the visibility/identity checks.

## Task 2: Make every diagnostic reachable

**Goal:** the docked review displays all metadata and diagnostics, including
long messages, within its height budget and supports keyboard scrolling.

**Files:** `internal/app/tui/sddreview/review.go`,
`internal/app/tui/sddreview/review_test.go`,
`internal/app/tui/frame_geometry_test.go`.

**Steps:**

1. In `Panel.View`, build sanitized logical rows for candidate path, task and
   operation counts, strategy, and every diagnostic. Name severity in text
   (`error` / `warning`) and include task number or `plan` for task zero and
   line number when present. Wrap each row to the panel's real inner width
   using `ansi.Wrap` before computing physical rows. Never silently truncate a
   diagnostic or a path. Read the candidate path from
   `result.Request.PlanPath` even when inspection is nil, so the keep-for-edit
   instruction remains actionable. Remove the now-unnecessary dummy
   `strings.TrimSpace` reference; retain the import if wrapping or joining
   still uses it.
2. Add a private physical-row scroll offset and last visible body height to
   `Panel`. Up/Down move one row; PgUp/PgDn move a body page; Home/End jump to
   ends. Clamp offset on resize or changed content. Reserve the panel header,
   a persistent action/confirmation row, and an overflow-position row when
   needed before allocating body rows. Keep the selected discard confirmation
   visible and disarm it on navigation. Do not allow scrolling to trigger
   acceptance, keeping, or deletion. The header/footer hints must reflect
   actionable keys even when the frame is narrow.
3. Add `TestReviewPagesAllDiagnosticsAndLongPath`: a blocked inspection with
   multiple long, uniquely marked warnings/errors and a long candidate path,
   at 80×24's actual dock budget and at a larger size. Page from beginning to
   end, asserting every marker and suffix is reachable, the frame never
   exceeds width/height, Enter stays blocked and the artifact is untouched.
   Add `TestReviewScrollResizeAndNoColor` for reflow, top/bottom clamping,
   visible action hints and disarming after navigation. Include control-character
   input and assert no raw untrusted terminal escapes reach the view.
4. Add `TestGeneratedPlanReviewFitsComposedFrame` through the parent model in
   `frame_geometry_test.go`: populate a transcript and tray, open the real
   review and navigate at 80×24, 120×40 and 200×60. Assert bounded geometry,
   visible action controls, and reachable diagnostics after a resize.

**Verify:**

```bash
go test ./internal/app/tui/sddreview -count=1
go test ./internal/app/tui -run 'Test(GeneratedPlanReview|SDDNew|Discard|StaleReview)' -count=1 -timeout 90s
gofmt -l internal/app/tui/sddreview internal/app/tui/frame_geometry_test.go
```

Expected: tests pass, formatting emits no paths, and every diagnostic is
reachable at the supported minimum rather than only counted in the model.

## Final verification and integration

After both tasks pass, from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p5-plan-review ./cmd/marshal
go test ./internal/app/tui/... ./internal/sddauthor -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui internal/sddauthor
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p5-plan-review go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Record actual results and compare failures with the latest integrated baseline
in [triage](../triage.md); do not hide failures. The scripted usability suite
currently covers help, not this workflow; the real `Model.Update` and composed
frame tests above are required. If a controlling terminal is available,
exercise keep, deliberate discard, failure and diagnostic scrolling in a
scratch repository at 80×24; otherwise record that limitation. Update only
the two relevant audit findings and triage item 6 after verification, and
commit the named docs and plan with the work.

## Plan self-review

- Keep, explicit discard, failure, stale-message identity and hidden-size
  behavior are pinned before the scroll change; each task has its own tests and
  verify commands.
- Diagnostic wrapping is measured in physical rows and tested through the
  actual composed frame. No runner or real credential is involved.
- Existing anchors were inspected at the authored base. This plan has not been
  prototyped or executed; the user reviews it before implementation.
