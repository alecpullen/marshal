# P3 draft preservation and secret entry — implementation plan

Date: 2026-10-27. Status: executed 2026-10-27 — commits `120c0a24` (task 1),
`2a081201` (task 2), `02e137a5` (task 3) and `43af57e7` (task 4), run
sequentially as this plan requires, after the P2 plugin work was integrated
(`6a821e28`). A review pass followed.
Authored base: `e306ccbaa70aa5a7b2c2cc4a1068a6231748b21d`. The
[plugin safety plan](2026-10-27-p2-plugin-install-safety-plan.md) is running
separately; integrate its completed work and recheck the anchors and baseline
before executing this plan. Requirements: [triage item 4](../triage.md#p1--restore-trustworthy-decisions-and-prevent-data-loss)
and its linked [audit findings](../audit.md#doctor-repair-exposes-api-keys-and-discards-the-composer-draft).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** temporary question, approval-edit and doctor flows leave the user's
chat draft intact; doctor accepts API keys through a masked input; large paste
chips are inserted at the cursor.

**Non-goals:** change question skip semantics, provider credential storage
policy, Send now semantics, approval policy, other P1/P2 findings, or the
existing condensed-paste expansion format. This is the third implementation
plan in sequence; it addresses the next **P1** triage item.

**Decisions and constraints:**

- Keep the actual `m.input` textarea and `m.pastes` untouched while a question,
  approval edit or doctor secret field owns input. This preserves text, cursor,
  scroll position, focus intent, draft history and paste attachments without
  shallow-copying the textarea's internal mutable slices.
- Approval editing uses a separate textarea bound to the exact pending
  approval and owning session. It is never submitted as a chat prompt or
  recorded in prompt history. The existing confirmation and Send now rules
  remain; an interrupted, replaced or canceled request must not settle a
  replacement or restore stale edit text. Edited decisions go through the
  canonical settlement path when the request has a key.
- Doctor uses a dedicated `textfield.Model` with `textinput.EchoPassword` and
  `CharLimit = 0`. A pasted key is routed directly to this field before the
  chat paste-condensing branch, never to history, transcript, completion or
  Send now. Disable positive submission while the field is hidden or the
  terminal is below the supported minimum; Esc cancels. On save failure keep
  the masked field open for retry, show the error without the key, and leave
  the chat draft intact. On success retain the existing user-global save,
  reload and doctor-refresh behavior; close the field and clear its value.
- When Send now is invoked over an approval edit or doctor entry, it owns a
  separate composer and must never consume either value as steering. Restore
  the temporary surface only while it is still valid; the chat draft survives
  accepted and rejected Send now unless explicitly submitted as chat.
- A condensed paste is inserted as a complete chip at the current textarea
  cursor with `textarea.Model.InsertString`; preserve the existing threshold,
  chip format and expansion. Preflight the available character limit; if a
  complete chip cannot be inserted, leave the draft and attachments unchanged
  and display a retryable explanation. Never append an attachment for a
  missing or partial chip.
- Existing P1 approval/sudo visibility and P0 confirmation protections are
  invariants. Use `internal/testenv`-isolated fixtures and no real keys or
  endpoints. Stage only named files; other unrelated work may be present.

Tasks are sequential: all change the parent TUI package and later tests rely
on earlier behavior. Each task is independently verifiable and may be
committed after passing as `p3-drafts-secret: task N — <title>`. The user
reviews this plan before execution; do not start it during plugin-plan work.

## Contracts

Tasks 2 and 3 use independent temporary input models. Their fields are private
to `internal/app/tui`; no new exported cross-package API is required.

```go
// Task 2: created when the user chooses Edit for one pending approval.
type approvalEdit struct {
    input textarea.Model
    owner *session.State
    call  *session.PendingToolCall
}

// Task 3: created for a doctor repair and cleared on success/cancel.
type doctorSecret struct {
    provider string
    input    textfield.Model
}
```

`approvalEdit.call` is the original request, not the display copy; compare
request identity before settlement and retire the edit when that request is no
longer pending. Retain `editingCommand` only if existing callers need it;
otherwise derive the mode from the non-nil edit. The doctor field is never a
source for `m.input`, chat history or paste attachments.

## Task 1: Preserve the chat draft when a question settles

**Goal:** completing or skipping an inline question leaves the underlying
composer and all of its attachments unchanged.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/model_test.go`.

**Steps:**

1. In `handleQuestion`, remove the `resetInput` and hardcoded placeholder
   assignment after settlement; the question has its own form. Recalculate
   viewport height and refresh/invalidate as needed, without changing question
   answers, prompt registry settlement, textarea or attachments.
2. Add `TestQuestionSettlementPreservesComposer`: at 80×24 construct a
   multiline draft with a condensed paste and a cursor in the middle, then
   drive both answer and Escape through `Model.Update`. Assert text, cursor,
   focus, placeholder, attachments and subsequent expanded submission match
   the pre-question draft. Add `TestQuestionCancellationAndReplacementKeepDraft`
   for registry cancellation/Send now and a replacement question; no stale
   answer may clear or send the draft.

**Verify:**

```bash
go test ./internal/app/tui -run 'Test(QuestionSettlementPreservesComposer|QuestionCancellationAndReplacementKeepDraft|PendingQuestion)' -count=1
gofmt -l internal/app/tui/model.go internal/app/tui/model_test.go
```

Expected: tests pass and formatting prints no paths.

## Task 2: Isolate approval editing from the chat composer

**Goal:** editing, saving, canceling or interrupting an approval never
overwrites the user's chat draft or settles a different request.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/console.go`,
`internal/app/tui/model_test.go`, `internal/app/tui/send_now_composer_submit_test.go`.

**Steps:**

1. On `choiceEdit` in `handleApproval`, create an `approvalEdit` with a
   focused textarea, `CharLimit = 0`, the original owner/request and the full
   command or JSON args. Keep `m.input`, `m.pastes` and their cursor unchanged.
   Render this textarea in `inputSections` and count its actual height in
   `inputChromeRows` / resize logic; do not render the chat textarea while the
   edit is active. Apply typing and `tea.PasteMsg` to the edit textarea only.
2. Esc clears the edit and returns to the same pending approval; Enter with
   nonempty content settles the same still-pending request with `Edited` set,
   using `settleApproval` for canonical keyed requests and its existing legacy
   fallback. If the owner/request no longer matches, retire the edit without
   approving, clearing a replacement, or altering the draft. Retire it from
   `retireStalePromptModels` and on prompt replacement/cancellation. Preserve
   P1's hidden/minimum-size positive-decision guard. Update the existing edit
   tests that currently assert `m.input` contains the command to assert the
   separate editor instead.
3. Add `TestApprovalEditPreservesComposer` covering save and Esc with a
   multiline cursor and condensed attachment; assert exact eventual chat
   submission and no edit text in history. Add
   `TestApprovalEditStaleRequestCannotSettleReplacement` using keyed prompts,
   interruption and replacement. Add `TestApprovalEditSendNowKeepsBothDrafts`
   for accepted and rejected Send now, plus resize while editing; verify no
   accidental approval and no loss of the chat draft.

**Verify:**

```bash
go test ./internal/app/tui -run 'Test(ApprovalEdit|.*Approval.*|.*SendNow.*)' -count=1 -timeout 90s
gofmt -l internal/app/tui/model.go internal/app/tui/console.go internal/app/tui/model_test.go internal/app/tui/send_now_composer_submit_test.go
```

Expected: focused tests pass and formatting emits no paths.

## Task 3: Mask doctor key entry and preserve the chat draft

**Goal:** doctor repair uses an isolated, masked secret field and does not
discard or accidentally submit the draft.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/keypress.go`,
`internal/app/tui/console.go`, `internal/app/tui/model_test.go`; create
`internal/app/tui/doctor_secret.go` and
`internal/app/tui/doctor_secret_test.go`.

**Steps:**

1. On `doctorpanel.FixMsg`, close the doctor panel and initialize a focused
   `doctorSecret` with `textinput.EchoPassword`, `CharLimit = 0` and a
   provider-specific placeholder; do not reset or repurpose `m.input`. Render
   a dedicated labeled console section and budget its rows. Route keypresses
   and `tea.PasteMsg` to it before the normal textarea and large-paste branch;
   ordinary typing, `?`, completion and history must never receive the key.
   Gate positive submission when below minimum size or hidden; Esc cancels.
2. Move the existing save/reload logic from `handleKeypress` into a
   doctor-secret handler. Save through the same global config path and refresh
   the doctor diagnostics on success. On save failure show an error with no
   key material and keep the masked field for retry; distinguish a successful
   save followed by reload failure and avoid claiming the save was lost.
   Clear the field on success or cancel and restore the prior composer focus.
   Include the field in `surfaceOwnsInput`, so Alt+Enter uses the separate
   Send now composer; never treat the secret as steering. Replace the old
   `doctorFixProvider` / `savedInputPlaceholder` state and update
   `TestDoctorFixSavesKeyAndReloads` to type/paste through `Model.Update`.
3. Add `TestDoctorSecretMasksTypedAndPastedKey` for short and >1,500-byte
   paste, assert the exact saved value in an isolated home without printing
   secret bytes on failure, and assert it never appears in view, history,
   transcript or paste attachments. Add `TestDoctorSecretPreservesDraft` for
   save, cancel, failed save/retry, resize and Send now. Add
   `TestDoctorSecretHiddenCannotSubmit` for minimum-size and full-frame
   hiding; keep cancellation available.

**Verify:**

```bash
go test ./internal/app/tui -run 'Test(DoctorFix|DoctorSecret)' -count=1 -timeout 90s
go test ./internal/app/tui/doctorpanel ./internal/app/tui/textfield -count=1
gofmt -l internal/app/tui/doctor_secret.go internal/app/tui/doctor_secret_test.go internal/app/tui/model.go internal/app/tui/keypress.go internal/app/tui/console.go internal/app/tui/model_test.go
```

Expected: tests pass and formatting prints no paths. Never print test key
values in errors or logs.

## Task 4: Insert condensed paste at the current cursor

**Goal:** large pasted content expands at the selected insertion point without
partial chips or orphaned attachments.

**Files:** `internal/app/tui/paste.go`, `internal/app/tui/model.go`,
`internal/app/tui/paste_test.go`.

**Steps:**

1. Change `addPaste` to return a `tea.Cmd` for user feedback. Build the next
   chip, preflight `m.input.CharLimit` against `m.input.Length()` in runes,
   and insert with `m.input.InsertString` at the existing cursor. Append the
   attachment only after verifying the full chip was inserted; if capacity or
   the textarea's content-height guard prevents insertion, leave the existing
   draft/attachments unchanged and return `m.setFlash` explaining that the
   paste did not fit. Update the `tea.PasteMsg` branch to return that command.
   Keep small-paste behavior and the existing chip expansion semantics.
2. Add `TestLargePasteInsertsAtCursor` for start, middle, multiline and Unicode
   text, checking final expanded submission order and cursor after the chip.
   Add `TestLargePasteAtInputLimitIsAllOrNothing` for exactly enough and one
   rune too little, plus a content-height rejection; assert no partial chip or
   orphaned attachment and a visible failure notice. Add
   `TestMultiplePastesAndDraftPreservation` across a question and approval
   edit, checking attachment order and later expansion. Retain existing
   threshold and deleted-chip tests.

**Verify:**

```bash
go test ./internal/app/tui -run 'Test(ShouldCondensePaste|LargePaste|TwoLargePastes|SmallPaste|EnterExpandsPaste|EnterDropsDeletedPaste|MultiplePastes)' -count=1
gofmt -l internal/app/tui/paste.go internal/app/tui/model.go internal/app/tui/paste_test.go
```

Expected: focused tests pass and formatting emits no paths.

## Final verification and integration

After plugin work is integrated and Tasks 1–4 pass:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p3-drafts-secret ./cmd/marshal
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p3-drafts-secret go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Record actual results and compare failures to the latest integrated baseline;
do not hide failures. The current scripted usability suite covers help only,
so the real `Model.Update`/render tests above are the main regression evidence.
Manually inspect masked entry, multiline cursor placement, resize and a pending
question at 80×24 and 120×40 using isolated local fixtures and dummy keys.
Update only the four resolved audit findings and triage status after verification;
commit this plan and those named docs with the work.

## Plan self-review

- Each triage finding has an owning task and behavioral tests; the separate
  temporary inputs preserve the original composer without depending on a
  shallow textarea copy.
- Prompt identity, keyed settlement, Send now, hidden input, paste capacity,
  error/retry and secret leakage are explicitly covered.
- Paths and anchors were inspected at the authored base; the in-flight plugin
  work may change `model.go`, so recheck after integration. No prototype or
  new tests have been run for this plan.
