# P1 approval and sudo review — implementation plan

Date: 2026-10-27. Status: proposed; execute after the P0 plan completes and
this plan is approved. Authored against `bdfc45902cb08fc3a12fc9f2d7f1f5ff37928127`;
the P0 work is running separately. Recheck the base and the named anchors after
integrating P0; do not overwrite its work or assume its commits already exist.
Requirements: [P1 triage item 2](../triage.md#p1--restore-trustworthy-decisions-and-prevent-data-loss)
and its linked [audit findings](../audit.md#interactive-approvals-omit-the-details-being-approved).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** a user can inspect the full operation and context before deciding on
an approval or sudo request, including at 80×24, and Escape cannot leave an
approval armed for accidental confirmation.

**Non-goals:** postmortem consent, question/skill-gate overflow, approval-edit
draft preservation, changes to authorization policy or tool execution, and
the other P1 items. The existing interaction-mode and access-panel contracts
remain intact.

**Interaction decisions:**

- Approval and sudo use a bounded review body above pinned controls. Wrap
  before windowing, retain every character of sanitized command, arguments,
  reason and diff, and expose overflow with `PgUp`/`PgDn` and visible position
  indicators. `Ctrl+U`/`Ctrl+D` continue to scroll the transcript; mouse
  transcript scrolling remains available. Prompt scrolling takes precedence
  over the current `scrollTranscript` handling of page keys only while an
  approval or sudo prompt is active.
- A user may decide without visiting every page; no forced read-through is
  introduced. All content must be reachable and the currently selected action
  and confirmation state must remain visible. Approval retains two Enter
  presses and the existing two-Escape denial gesture; the first Escape always
  disarms a pending Enter. Navigation and mnemonic selection disarm it too.
- Show a single selected action with its mnemonic and a position/count when
  choices do not fit; arrows and existing mnemonics still select all allowed
  choices, including rollback. Forced prompts keep the existing restricted
  choices. Never leave a selectable decision outside the visible control area.
- Sudo retains its masked field, first-keystroke handling, single Enter to
  authenticate, and single Escape to deny. Scrolling must never clear or expose
  the password. The request's literal command remains distinct from the
  sanitized display copy.
- Below the supported minimum size, or while a full-frame dock hides the
  prompt, do not accept an approval or sudo authentication. Continue allowing
  Ctrl+C and safe denial/cancellation. Recompute visibility on resize and
  prompt replacement; do not rely solely on an earlier render.
- Bounded review must account for the transcript, live tray, dock, console
  header and status. The body starts at its first row, not its tail. The
  selected action/password and its hint stay on screen; suppress or shrink
  nonessential tray/dock content as needed rather than clipping the prompt.
  A full-frame dock that currently owns input remains authoritative: defer the
  prompt until it is visible rather than stealing the dock or accepting hidden
  input.
- `shownToolCall` and `strutil.ShowControls` remain the display safety boundary;
  no untrusted terminal control sequence is emitted. Risk and Reason are
  separate fields even when both are populated. Render shell command, non-shell
  arguments/schema, requester/why, sandbox and diff where present. Use the
  original sanitized diff lines, wrapped rather than horizontally truncated, so
  every changed line and long-line suffix is reachable.

**Baseline:** the focused TUI package has a known unrelated failure in
`TestStatusLineUntrustedIsWarningColored` (expected ANSI 172 versus current
theme warning ANSI 215). Do not mask it or change theme behavior in this work;
record the actual full-suite results and distinguish any new failure.

Tasks execute sequentially. Each task is independently verified and committed
before the next as `p1-approval-sudo: task N — <title>`. No parallel-safe
markers: the tasks share the parent TUI package and the later tasks consume
the common review contract. Stage named files only; other work may be in flight.

## Contracts

Task 2 creates these package-private declarations in
`internal/app/tui/prompt_review.go`; Tasks 3–4 consume them:

```go
type promptReview struct {
    offset int // first visible wrapped display row
}

// Rows wraps sanitized logical lines to width, preserving blank lines.
func promptReviewRows(lines []string, width int) []string

// Window clamps offset and returns at most height body rows plus an
// indicator describing omitted rows. The indicator counts within height.
func (r *promptReview) Window(rows []string, height int) []string

// Page moves by one visible body page, clamped to the current wrapped rows.
func (r *promptReview) Page(rows []string, height, direction int)
```

`direction` is `-1` or `+1`. At least one content row remains visible whenever
the review has content and the prompt is actionable. Indicators are textual,
work without color, and do not contain user text. Resizing clamps the offset;
replacement of a pending request creates a new `promptReview` at offset zero.
All rows passed to the helper have already had untrusted controls escaped.

## Task 1: Disarm approval on Escape

**Goal:** starting denial cancels an armed action and the hints describe the
two-step gestures accurately.

**Files:** `internal/app/tui/approval.go`, `internal/app/tui/approval_test.go`,
`internal/app/tui/help/help.go`, `internal/app/tui/help/help_test.go`.

**Steps:**

1. In `approvalModel.Update`, clear `submitPending` on every Escape before
   handling the existing 1.5-second denial window. Keep the existing choice,
   mnemonic, and two-Escape behavior. Change `help.PromptKeys` approval hints
   to `Enter twice confirm` and `Esc twice deny`; keep navigation hints.
2. Add `TestApprovalEscDisarmsPendingSubmit`: Enter, Escape, Enter must leave
   the approval pending and newly armed; only a further Enter can confirm.
   Add `TestApprovalEscDisarmsEveryChoice` covering allow, deny, edit, always,
   session and rollback when present. Preserve the existing double-Escape and
   expired-denial tests, and add a footer assertion for both two-press hints.

**Verify:**

```bash
go test ./internal/app/tui -run 'Test(ApprovalEsc|EscRequiresDoublePressToDeny|EscAfterOtherKeyResetsPendingDeny)' -count=1
go test ./internal/app/tui/help -count=1
gofmt -l internal/app/tui/approval.go internal/app/tui/approval_test.go internal/app/tui/help
```

Expected: targeted tests pass and formatting emits no paths.

## Task 2: Add a bounded review window

**Goal:** a reusable prompt-body window preserves and pages all wrapped rows
within a fixed physical height.

**Files:** create `internal/app/tui/prompt_review.go` and
`internal/app/tui/prompt_review_test.go`.

**Steps:**

1. Implement the Contracts using `ansi.Wrap` with the existing
   `WrapBreakpoints`. Split physical lines after wrapping; do not truncate or
   discard long-line suffixes. Use a single indicator row when either side is
   omitted, e.g. `rows 4–8 of 20 · PgUp/PgDn`; reserve that row inside the
   specified height. For a one-row window show content rather than an
   indicator; the caller must separately indicate overflow in pinned hints.
   Page by `max(visible body rows, 1)` and clamp offsets to the latest rows.
2. Add `TestPromptReviewReachesEveryRow`: page a long, blank-line-containing
   input until each unique wrapped segment is visible; assert the output never
   exceeds its height. Add `TestPromptReviewResizeAndBounds` for narrow/wide
   reflow, one-row and oversized heights, both ends, and zero content. Add
   `TestPromptReviewUnicodeAndNoColor` for Unicode cell width and readable
   indicators under `NO_COLOR`. Test logical content with plain strings; the
   callers are responsible for control escaping.

**Verify:**

```bash
go test ./internal/app/tui -run '^TestPromptReview' -count=1
gofmt -l internal/app/tui/prompt_review.go internal/app/tui/prompt_review_test.go
```

Expected: tests pass and formatting emits no paths. This task adds an unused
helper only; the existing approval and sudo behavior changes in Tasks 3–4.

## Task 3: Make approval details reviewable and controls visible

**Goal:** the interactive approval shows the complete sanitized operation in a
bounded scrollable body and never accepts an invisible decision.

**Files:** `internal/app/tui/approval.go`, `internal/app/tui/model.go`,
`internal/app/tui/console.go`, `internal/app/tui/view.go`,
`internal/app/tui/livebox_view.go`, `internal/app/tui/approval_test.go`,
`internal/app/tui/model_test.go`.

**Steps:**

1. Extend `approvalModel` with a `promptReview`, an `approvalWhy`, and the
   current body height. Build review logical lines from `shownToolCall`: tool
   name, literal command for `shell.run` or arguments and nonempty schema for
   other tools, nonempty risk and reason separately, owner and why, applicable
   sandbox details, and the sanitized full diff. Wrap through Task 2; render
   the selected action and arm/deny notice in pinned rows below the body.
   Use a compact one-action selector with `n/total` and mnemonic so selected
   choices cannot be clipped. Keep `forcedPromptClass` restrictions and the
   existing `settleApproval` side effects.
2. Define `func (m Model) promptReviewBodyHeight(controlRows int) int` in
   `model.go`: reserve the status row, one console header, all pinned controls,
   at least one transcript row and one body row; cap the review body at half
   the terminal height. When a dock or live tray consumes that space, reduce
   the tray budget first and then defer confirmation if the body and controls
   cannot fit. Feed the exact same height to rendering and
   `inputChromeRows`; do not call `boxRows` from this helper, avoiding the
   existing `boxMaxRows` → `inputChromeRows` dependency cycle.
3. Route approval `PgUp`/`PgDn` to its review before `scrollTranscript` in
   `Model.Update`; keep transcript `Ctrl+U`/`Ctrl+D` and mouse handling. Do
   not intercept page keys while `editingCommand` is active. Reset review
   state when a different pending approval replaces the current one; reflow
   and clamp it on `WindowSizeMsg` without changing the selected action.
   Gate positive/edited decisions when the terminal is below minimum or the
   prompt is hidden behind a full-frame dock. Deny and Ctrl+C must still work.
4. Add real-update-path tests:
   - `TestApprovalReviewShowsOperationAndReason`: non-shell args, schema,
     requester, why, risk and cost-consent reason are all reachable, with
     unsafe controls escaped and no raw escape sequences from model content.
   - `TestApprovalReviewPagesFullCommandAndDiff`: at 80×24 use unique markers
     in a long shell command and long diff; page both ends and assert every
     marker can be seen without losing selection or controls.
   - `TestApprovalReviewResizeAndVisibility`: resize 120×40 → 80×24 → below
     minimum and back while a prompt, tray and a full-frame dock are present;
     no hidden approval is settled, no prompt row is clipped from the composed
     frame, and the original selection/decision remain correct.
   - `TestApprovalReviewKeepsTranscriptNavigation`: page keys scroll review;
     Ctrl+U/Ctrl+D and mouse gestures still scroll transcript. Exercise forced
     prompts, all choices, two-step confirmation and a replacement request.
   Update existing presentation assertions only where the pinned selector or
   wrapped detail intentionally changes; retain their behavioral coverage.

**Verify:**

```bash
go test ./internal/app/tui -run '^TestApproval(Review|Esc|Dialog|Navigates|Mnemonic|View|SubForm)' -count=1
go test ./internal/app/tui/help ./internal/app/tui/livebox -count=1
gofmt -l internal/app/tui/approval.go internal/app/tui/model.go internal/app/tui/console.go internal/app/tui/view.go internal/app/tui/livebox_view.go internal/app/tui/approval_test.go internal/app/tui/model_test.go
```

Expected: targeted tests and supporting packages pass; formatting emits no
paths. Do not suppress pre-existing test failures in the final suite.

## Task 4: Make sudo details reviewable without exposing the password

**Goal:** the full sudo command and context remain reachable while password
entry and denial stay visible and correctly routed.

**Files:** `internal/app/tui/sudoauth.go`, `internal/app/tui/model.go`,
`internal/app/tui/console.go`, `internal/app/tui/sudoauth_test.go`,
`internal/app/tui/model_test.go`.

**Steps:**

1. Give `sudoAuthModel` a Task 2 review window and body height from
   `promptReviewBodyHeight`. Wrap `strutil.ShowControls` output for command,
   reason, error, and applicable approval/step/unlock notices; pin the masked
   password field and its Enter/Escape hint below the window. Keep the typed
   bytes only in the existing field and decision, never in review content,
   transcript, logs or test failure output.
2. Route sudo `PgUp`/`PgDn` before `scrollTranscript`, leaving transcript
   Ctrl+U/Ctrl+D and mouse handling intact. Reset review state on a changed
   `PendingSudoAuth`, reflow on resize, and prevent authentication while the
   prompt is hidden or below minimum; allow Escape to deny and Ctrl+C. Preserve
   the existing canonical settlement and first-keystroke paths.
3. Add `TestSudoReviewPagesFullCommandAndReason` with long unique command and
   reason markers at 80×24; page to both ends while the input stays visible.
   Add `TestSudoReviewPreservesMaskedPasswordAcrossResizeAndScroll`, asserting
   correct submitted bytes without printing them and that rendered frames never
   contain the secret. Add `TestSudoReviewHiddenPromptCannotAuthenticate` for
   size gating, full-frame dock, cancellation and replacement. Keep existing
   sudo tests for first keystroke, masking, deny and mode isolation.

**Verify:**

```bash
go test ./internal/app/tui -run '^TestSudo(Auth|Review)' -count=1
gofmt -l internal/app/tui/sudoauth.go internal/app/tui/model.go internal/app/tui/console.go internal/app/tui/sudoauth_test.go internal/app/tui/model_test.go
```

Expected: sudo tests pass and formatting emits no paths.

## Final verification and integration

After P0 is integrated and Tasks 1–4 pass, run from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p1-approval-sudo ./cmd/marshal
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p1-approval-sudo go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Record actual results and any baseline failure separately; do not skip failing
tests. The scripted usability scenario covers help only, so the real `Model`
update/render tests above are essential. Also inspect 80×24 and 120×40 in a
real terminal with a scripted fake prompt, `NO_COLOR`, a long command and a
resize; verify page navigation, visible controls and masked sudo input. Do not
use real credentials or run the approved command in this check. Update the
relevant audit and triage entries with verified results and commit this plan
with the work, staging named paths only.

## Plan self-review

- The four P1 findings have an owning task and concrete verification; P0 and
  unrelated P1 work are excluded. The helper is introduced separately and its
  consumers are explicit.
- Height budgeting, page-key precedence, minimum-size/full-frame gating,
  prompt replacement, secret handling, forced choices, control sanitization and
  resize are pinned rather than left for an executor to invent.
- Anchors were checked against the authored base. No prototype or new tests
  have been executed; recheck against the integrated P0 head before execution.
