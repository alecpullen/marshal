# TUI polish audit

Working inventory for later triage. This is an ongoing code and UX audit, not
an exhaustive list or an implementation plan. The web control plane is out of
scope. Severity and solutions are provisional until triage.

The highest-risk item found so far is [skill removal reconstructing the wrong
filesystem path](#skill-removal-reconstructs-the-wrong-filesystem-path): a
confirmed removal can escape the skill store. Approval and plugin-review
disclosure findings also deserve early attention.

## Findings

### Interactive approvals omit the details being approved

The interactive approval model computes a summary containing the tool
arguments, reason, requester and other context, but its own `View` renders a
different compact layout and never displays that summary. Non-shell approvals
therefore omit arguments and requester context. When Risk is nonempty, they
also omit Reason, including the reason on model-cost consent; when Risk is
empty, Reason is used as a fallback and can still be clipped. The fallback
renderer contains more detail, but is replaced during normal handling.
Users can confirm without seeing the full operation or the reason for consent.

- Evidence: `internal/app/tui/approval.go:205`, `:331` and `:412`;
  `internal/app/tui/console.go:216` and `internal/app/tui/model.go:3206`.
- Potential direction: show the complete relevant details in the interactive
  prompt and keep them accessible before confirmation.
- **Status (2026-10-27): fixed.** The interactive prompt no longer re-renders a
  compact layout of its own; `approvalModel.reviewLines` builds the body from
  `shownToolCall` and shows the requester line, the operation (literal command
  for `shell.run`, arguments and nonempty schema otherwise), the model's own
  "why", risk and reason as separate fields, sandbox isolation, and the
  sanitized full diff. `TestApprovalReviewShowsOperationAndReason` asserts each
  of them through the composed frame, including that a control character in the
  arguments is shown as a visible escape and that no raw SGR sequence reaches
  the frame.

### Long shell commands are clipped in the interactive approval prompt

The interactive approval view writes the shell command as a single line. The
console truncates each line to the frame width, with no way in this prompt to
view the remainder. A user can approve a command whose trailing arguments or
additional shell operations are not visible.

- Evidence: `internal/app/tui/approval.go:356` and
  `internal/app/tui/console.go:52`.
- Potential direction: wrap or provide a scrollable full command display,
  ensuring the complete command remains available before confirmation.
- **Status (2026-10-27): fixed.** The body is now a bounded, pageable window:
  `promptReviewRows` wraps with `ansi.Wrap` (never truncating), and
  `promptReview.Window`/`Page` expose every wrapped row within a fixed physical
  height with a textual `rows x–y of n · PgUp/PgDn` indicator. Pinned rows below
  the body carry the selected action and the arm/deny notice, so paging never
  hides the decision. `TestApprovalReviewPagesFullCommandAndDiff` pages a long
  command and a long diff at 80×24 and asserts every unique marker becomes
  visible with the selector still on screen.

### Oversized inline prompts can lose their first rows

Inline prompts can render more rows than the terminal has available. The final
frame drops surplus rows from the top, but transcript scrolling does not scroll
the prompt itself. In particular, a long sudo command or reason can push the
start of the command off screen while leaving the password field and Enter
action available. Long approval diffs and multi-question forms need the same
overflow review.

- Evidence: `internal/app/tui/sudoauth.go:72`,
  `internal/app/tui/model.go:3531`, and `internal/app/tui/view.go:121` and
  `:169`.
- Potential direction: constrain and scroll prompt content, keep decision
  controls reachable, and make the complete command inspectable before
  authentication.
- **Status (2026-10-27): fixed for approval and sudo.** `Model.promptReviewBodyHeight`
  budgets the review body from the frame after the status row, the scroll hint,
  the dock, one console header, the prompt's own pinned controls and the
  transcript floor, and caps it at half the terminal. The tray yields rather
  than the prompt: `boxMaxRows` already subtracts `inputChromeRows`, which now
  includes the bounded body. Both prompts page with `PgUp`/`PgDn` (taking
  precedence over transcript paging only while a prompt is up) and pin their
  controls below the body. A prompt that cannot fit — below the supported
  minimum, behind a full-frame dock, or with no body rows — accepts no positive
  decision; Escape still denies and Ctrl+C still works.
  `TestApprovalReviewResizeAndVisibility`, `TestSudoReviewPagesFullCommandAndReason`
  and `TestSudoReviewHiddenPromptCannotAuthenticate` cover the resize, gating,
  cancellation and replacement paths. Question and skill-gate overflow remain
  open (see P1 item 5 and P2 item 8).

### Escape does not disarm a pending approval

The first Enter arms the selected approval action, initially Allow. Pressing
Escape once starts a two-press deny gesture but leaves the earlier action
armed. If the user then presses Enter, the Escape warning is cleared and the
armed action is submitted immediately. The prompt header says “Esc deny”
without explaining that it takes two presses.

- Evidence: `internal/app/tui/approval.go:252` and
  `internal/app/tui/help/help.go:90`.
- Potential direction: disarm any pending action when Escape is pressed and
  make confirmation and denial gestures explicit in the displayed hints.
- **Status (2026-10-27): fixed.** Escape now clears `submitPending` before it
  opens the 1.5-second deny window, so the armed action is always the one the
  user last selected on screen; moving the selection with an arrow or a
  mnemonic disarms it too. The footer and the console header read
  `Enter twice confirm` and `Esc twice deny` instead of the single-press labels
  that made the first press look inert. `TestApprovalEscDisarmsPendingSubmit`,
  `TestApprovalEscDisarmsEveryChoice` (allow, deny, edit, always, session and
  rollback) and `TestApprovalNavigationDisarmsPendingSubmit` cover the gestures;
  the existing double-Escape and expired-window tests are unchanged.

### Consent disclosure can be clipped or omitted

The exit postmortem's agent-analysis prompt puts its explanation on one long
line. The panel frame truncates that line at ordinary terminal widths, and the
panel drops the explanation entirely when its height budget is tight. The
user can still select the default Yes without seeing that the agent may edit
a file at an absolute path outside the workspace.

- Evidence: `internal/app/tui/postmortempanel/postmortempanel.go:129` and
  `internal/app/tui/chrome/frame.go:85`.
- Potential direction: wrap the disclosure and ensure its essential content is
  visible before the choice can be confirmed, including at small sizes.
- Status (2026-10-28): fixed (tasks 1–2 of the
  [P4 postmortem consent plan](plans/2026-10-28-p4-postmortem-consent-plan.md),
  commits `8c37a029`, `e2997d16`). The agent-analysis stage renders two
  mandatory lines — `Agent analysis has system access and editing approval.`
  and `It can access and edit files outside this workspace.` — together with
  the question and both choices in one pass, or not at all: at a geometry that
  cannot hold all five rows the panel shows a resize instruction instead of
  dropping the disclosure, falling back to the bare choices only where not even
  the instruction fits (a geometry the dock's floor keeps out of reach), and
  the affirmative answer is refused while nothing is on screen. Consent is
  scoped to the geometry it was rendered at: entering the agent stage starts it
  ungated, a resize — including one that hides the frame below the supported
  minimum — or a replaced panel invalidates a rendering, and a render the
  disclosure no longer fits retracts it. `n`, `N` and `esc` answer at every
  size, including while an access request is queued behind the question, and
  Ctrl+C still interrupts. Covered by
  `TestAgentDisclosureAndChoicesFit`,
  `TestAgentConsentRequiresCurrentVisibleDisclosure`,
  `TestPostmortemConsentVisibleInComposedFrame` and
  `TestPostmortemConsentResizeInvalidatesVisibility`.

### Doctor repair exposes API keys and discards the composer draft

Selecting the doctor fix closes the panel, resets the normal chat input and
uses it to enter the API key. That input renders unmasked, exposing the key to
anyone viewing the terminal. An existing draft is also discarded.

- Evidence: `internal/app/tui/model.go:2374`,
  `internal/app/tui/console.go:139`, and `internal/app/tui/keypress.go:26`.
- Potential direction: use a dedicated masked secret entry and preserve the
  draft across the flow.
- Status (2026-10-27): fixed (task 3 of the
  [P3 drafts and secret entry plan](plans/2026-10-27-p3-drafts-and-secret-entry-plan.md)).
  The repair now opens its own `doctorSecret` field in
  `internal/app/tui/doctor_secret.go` (`textinput.EchoPassword`,
  `CharLimit = 0`), rendered as a dedicated console section and budgeted like
  any other prompt. Keypresses and pastes are routed to it before the composer
  and before the condensing branch, so `?`, completion, history and paste
  attachments never see key material, and the chat draft is never reset,
  re-purposed or submitted: `m.input` and `m.pastes` are untouched from the
  moment the repair opens. Only `esc` reaches a field that is below the
  supported minimum or behind an open panel; a failed save keeps the masked
  value for retry. Covered by `TestDoctorSecretMasksTypedAndPastedKey`,
  `TestDoctorSecretPreservesDraft`, `TestDoctorSecretHiddenCannotSubmit` and
  `TestDoctorFixSavesKeyAndReloads`.
  One deliberate change to the old flow: a successful save no longer re-runs
  and re-opens the `/doctor` panel. The panel is closed while a prompt owns the
  keys, so the user is returned to the chat with a system message naming the
  provider as the receipt; re-run `/doctor` to see the refreshed diagnosis.

### Inspector silently omits metadata

The inspector limits fields to half the panel height and silently stops
rendering them. Scrolling applies to section bodies, not the fields. Long
early values can hide later information such as approval, changed arguments,
call ID and risk, without indicating that anything is missing.

- Evidence: `internal/app/tui/inspector/inspector.go:172` and
  `internal/app/tui/browse_inspect.go:145`.
- Potential direction: make all metadata reachable and indicate overflow.
- **Status (2026-10-28): fixed.** The half-height field loop is gone. When
  fields exist the inspector prepends a view-only `metadata` tab to the
  displayed tab sequence, rendered in the same scrolling body as the section
  tabs, with every key and its full wrapped value. The tab is panel-local:
  `Detail.Sections` and `Detail()` are unchanged. Metadata carries a separate
  internal identity (a `tabRef.meta` flag) so a real section named `metadata`
  stays distinct, `Section()` reports `-1` while metadata is selected, and `y`
  copies the complete `key: value` text. Offsets are per displayed tab and
  clamped on resize, with muted `↑ more` / `↓ more` rows. Existing inspector
  tests were updated for the metadata-first selection. Evidenced by
  `TestInspectorMetadataTabReachesEveryField` (every key and full value
  reachable at 100×20 and 64×14, frame budget held at 80×8 and 40×6) and
  `TestInspectorMetadataAndSectionsRemainDistinct` (copying, no sections, no
  fields, a section named `metadata`, tab identity across `SetDetail`).

### Ctrl+X footer guidance can conflict or repeat

The busy footer always advertises Ctrl+X to clear the queue and can also
advertise it to stop a drilled-in agent. The handler prioritizes stopping the
agent, so the queue hint is misleading in that state. A nonempty queue can
also cause the clear-queue hint to be repeated.

- Evidence: `internal/app/tui/help/help.go:149` and
  `internal/app/tui/keypress.go:370`.
- Potential direction: generate hints from the same action precedence as the
  handler and avoid duplicates.
- Status (2026-10-30): fixed (all three tasks of the
  [P11 Ctrl+X guidance plan](plans/2026-10-30-p11-ctrl-x-guidance-plan.md)).
  The footer no longer decides for itself: `Model.ctrlXAction()` is the single
  decision the keyboard handler and the hint both read, and
  `help.FooterHints.CtrlXHint` carries it into the footer, replacing the
  independent `DrilledRunActive` flag and the separate `QueueNonEmpty` append
  (both removed). `Footer` renders at most one pair, only in the busy and
  browse branches, and never in the idle, prompt, edit, popup or `KeysInFrame`
  branches. The parent sets it to none while a dock, pending approval,
  question, skill gate, privileged-auth field, `/doctor` key field, approval
  editor or completion popup owns the key, and leaves clear-queue at none while
  the steering queue is empty; browse stays eligible because `handleBrowseKey`
  lets Ctrl chords through. A refused stop no longer falls through to the queue
  action: a stale running snapshot or a child without a cancel handle consumes
  the key, leaves the queue untouched and flashes `Agent could not be stopped;
  queue unchanged`. Width still sheds the whole pair and keeps `Ctrl+C stop`.
  Evidenced by `TestCtrlXStopsRunningSubagentWhileDrilled`,
  `TestCtrlXUndrilledClearsSteeringQueue`, `TestCtrlXFailedDrilledStopKeepsQueue`,
  `TestCtrlXCompletedDrillAndIdle`, `TestFooterCtrlXMatchesAction`,
  `TestStatusCtrlXHintMatchesDrillAndWidth`,
  `TestCtrlXDisplayedActionThroughModel` (200×50 and 80×24, browse, resize
  through 140/120/100/80) and
  `TestCtrlXStaleSubagentNeverClearsQueueThroughModel` (stale child, pending
  prompt, open dock).

### Large pastes ignore the composer cursor

Pastes of eight or more lines or over 1,500 bytes are condensed into a chip.
The chip is always appended to the end of the input and the cursor is moved
there, even when the user pasted in the middle of a draft. Small pastes use
the textarea's ordinary insertion behavior. This can change the intended order
of a prompt without an obvious indication of what happened.

- Evidence: `internal/app/tui/model.go:2817` and
  `internal/app/tui/paste.go:25` and `:39`.
- Potential direction: insert the chip at the current cursor position and
  test it in multiline drafts.
- Status (2026-10-27): fixed (task 4 of the
  [P3 drafts and secret entry plan](plans/2026-10-27-p3-drafts-and-secret-entry-plan.md)).
  `addPaste` inserts the complete chip with `textarea.Model.InsertString` at
  the current cursor and no longer moves the cursor to the end. It preflights
  the remaining `CharLimit` in runes and verifies the whole chip landed before
  recording the attachment, so a refused insert leaves the draft and the
  attachments unchanged and reports, with a status flash, whether the character
  or the line limit refused it; a partial chip or an attachment no chip can
  expand is no longer possible. Covered by `TestLargePasteInsertsAtCursor`,
  `TestLargePasteAtInputLimitIsAllOrNothing` and
  `TestMultiplePastesAndDraftPreservation`.

### Session sheet can hide standing approval rules

The Ctrl+B sheet gives the selected section only the rows left after the
other sections, often just one row at a short terminal height. The rules
section truncates its list to that budget; the sheet has no rule scrolling or
full-detail command for that section. Later standing approvals therefore
cannot be inspected through the sheet, and there is no overflow indication.

- Evidence: `internal/app/tui/sessionsheet/panel.go:41`, `:97` and `:142`;
  `internal/app/tui/sessionsheet/section_rules.go:20`.
- Potential direction: provide a scrollable or full-detail rules view and
  show when the summary is truncated. Make the Enter hint conditional on an
  actual action for the selected section.
- **Status (2026-10-28): fixed.** Enter on the selected RULES section opens a
  read-only detail mode inside the sheet: each complete rule is wrapped with
  `ansi.Wrap`, the offset is clamped against content derived from the current
  `p.data()` on every render (so rules that disappear show an empty message
  rather than a stale list), and Up/Down, PgUp/PgDn and Home/End scroll
  physical lines with a position row in the budget. Escape returns to the
  sheet; Ctrl+B still closes it; no rule is modified. The ordinary summary now
  reads `⚑ N session rules · ↵ details`, a clipped body reserves a row for
  `… N more — ↵ details`, and the general `↵ open` hint appears only when the
  selected section has a command or is rules. Evidenced by
  `TestRulesDetailShowsEveryRule`, `TestRulesDetailLiveDataAndEscape`,
  `TestSheetEnterHintMatchesSelectedAction`, `TestRulesDetailOverflowThemes`
  and `TestRulesDetailThroughModel`. Limitation: the detail mode is
  deliberately read-only — it cannot add or remove a rule.

### Model discovery can move the selection while the user is choosing

In the all-providers model picker, each asynchronous probe result or error
rebuilds the picker. The rebuild restores the filter text but not the selected
item; the new picker starts at its default selection. A result arriving just
before Enter can therefore cause the user to choose a different model.

- Evidence: `internal/app/tui/connect/connect.go:966` and `:1006`;
  `internal/app/tui/picker/picker.go:61`.
- Potential direction: preserve selection by stable item value across refreshes
  and define a visible fallback if the selected item disappears.
- Status (2026-10-29): fixed (tasks 1 and 2 of the
  [P9 model discovery and sign-in plan](plans/2026-10-29-p9-model-discovery-sign-in-plan.md),
  commits `db7f5e96`, `e139103a`). `picker.ReplaceItems` refreshes the rows in
  place, re-binding the cursor to the same `Item.Value` and the same
  custom-row/ordinary identity, so the filter text and its cursor survive and
  the selection never slides onto a neighbour. When the chosen row disappears
  the picker selects nothing, shows `Selected model is no longer available;
  choose another` inside its height budget, and Enter emits no pick until the
  user moves or edits the filter — including when a second result arrives first.
  `connect.rebuildModelPicker` now replaces items instead of the picker, and an
  all-provider `/models` result is applied only in the all-provider
  model-selection step. Evidenced by
  `TestReplaceItemsPreservesSelectionAndFilterCursor`,
  `TestReplaceItemsRequiresExplicitChoiceWhenMissing`,
  `TestDiscoveryKeepsSelectedProviderModel`,
  `TestDiscoveryRemovedSelectionRequiresChoice`,
  `TestAllProviderResultDoesNotReopenLaterWizardStep` and
  `TestModelSelectionSurvivesHostDiscovery` (composed host frames at 80×24 and
  140×40).

### Skipped provider probes can interrupt later wizard steps

Skipping a provider probe advances to model selection without canceling the
request. The result handler does not check whether the wizard is still waiting
for that probe. A late success can recreate the model picker or move the user
back from a subsequent step; a late failure can leave a stale error.

- Evidence: `internal/app/tui/connect/connect.go:966`, `:1218` and `:1237`.
- Potential direction: cancel or identify probe requests and ignore results
  that no longer belong to the active step.
- Status (2026-10-29): fixed (task 3 of the P9 plan, commit `fef23349`). A setup
  probe now completes as `connect.WizardProbeResult`, carrying the wizard
  instance and a monotonically increasing attempt generation; `skipProbe`,
  `cancel`, a retry and a replaced wizard all invalidate it, and
  `AcceptsWizardProbe` additionally requires the probing step and the exact
  provider. The host validates the live panel and the attempt before touching
  session discovery or the on-disk cache, so a late success can neither rebuild
  the picker nor move the user out of a later step, and a late failure cannot
  evict a good cached catalog. A raw `probe.ResultMsg` is no longer accepted as
  a setup completion. Evidenced by
  `TestSetupProbeRejectsSkippedAndSupersededResults` (skip, retry, a later
  summary step, and a replaced wizard) and
  `TestStaleWizardProbeCannotPopulateHostCache`.

### Answering a question discards an unrelated composer draft

The question form temporarily hides the normal composer and owns its own
fields, but completion or cancellation resets the composer and its condensed
paste attachments. A draft being prepared while an agent is running is lost
when its question is answered or skipped.

- Evidence: `internal/app/tui/console.go:196`,
  `internal/app/tui/model.go:3346` and `:3373`, and
  `internal/app/tui/paste.go:71`.
- Potential direction: preserve and restore the composer state across the
  question rather than clearing it.
- Status (2026-10-27): fixed (task 1 of the
  [P3 drafts and secret entry plan](plans/2026-10-27-p3-drafts-and-secret-entry-plan.md)).
  Settling a question (answer or Escape) no longer resets the composer or
  rewrites its placeholder: the form has its own fields, so the draft keeps its
  text, cursor, focus and paste attachments and still submits with its chips
  expanded. A replacement question is answered by a form built for it instead
  of the settled one, so a stale answer can no longer clear or send the draft.
  Covered by `TestQuestionSettlementPreservesComposer` and
  `TestQuestionCancellationAndReplacementKeepDraft`.

### Editing an approval overwrites the composer draft

Choosing Edit for a pending approval puts the command or arguments into the
main composer with `SetValue`, replacing any draft being prepared during the
turn. Both saving and canceling the edit then reset that input, including its
paste attachments.

- Evidence: `internal/app/tui/model.go:3148` and `:3280`.
- Potential direction: use a separate edit field or save and restore the
  complete composer state when the edit ends.
- Status (2026-10-27): fixed (task 2 of the
  [P3 drafts and secret entry plan](plans/2026-10-27-p3-drafts-and-secret-entry-plan.md)).
  Choosing Edit now opens an isolated `approvalEdit` with its own textarea
  (`CharLimit = 0`) bound to the exact owner and pending request; the chat
  composer, its cursor and its paste attachments are never written, so saving,
  cancelling, interrupting or replacing the request leaves the draft intact.
  A stale edit is retired before it can keypress into a replacement, and a
  keyed decision settles through the canonical registry claim, so an
  interrupted edit cannot approve the next request. The approval visibility
  indicator still shows while the edit is open. Covered by
  `TestApprovalEditPreservesComposer`,
  `TestApprovalEditStaleRequestCannotSettleReplacement` and
  `TestApprovalEditSendNowKeepsBothDrafts`.

### Skipping a multi-question form discards earlier answers

The footer labels Escape as “skip”, and the question model describes Escape as
marking the remaining questions unanswered. In fact, answers are finalized
only when the whole form completes; Escape returns the initially unanswered
array, including for questions the user already answered.

- Evidence: `internal/app/tui/help/help.go:86`,
  `internal/app/tui/question.go:29`, `:189` and `:328`, and
  `internal/app/tui/model.go:3361`.
- Potential direction: retain completed answers on skip, or clearly describe
  and confirm an action that discards the entire form.
- Status (2026-10-30): fixed. The confirmed contract is "keep completed
  answers", and only explicit Escape applies it: `questionModel` keeps the
  answer submitted for a question the form has already advanced past, and
  records `session.AnswerUnanswered` for the focused question and every later
  one. A question is complete only once the form moved on from it, so a
  highlighted default option, typed partial text, an unsubmitted multi-select
  toggle or an unsubmitted custom input does not finish it. After navigation
  back, that question and every later one are skipped even if visited; an
  advancement still queued when Escape arrives skips the focused question;
  Escape at the first question skips everything; and an unmapped focused field
  fails closed to all unused answers. An unexpected huh `StateAborted` remains
  an all-unanswered abort rather than a partial skip, and the footer and
  console hint read `Esc skip remaining`. Answer settlement is otherwise
  unchanged: one ordered answer per question through the existing
  pending-question identity and the canonical `ResolveQuestion` claim, so a
  cancelled or replaced prompt never receives another prompt's answers, and the
  hidden composer keeps its text, cursor and paste attachments. Evidenced by
  `TestQuestionEscapeKeepsCompletedPrefix` (free text, single choice, multi
  choice, custom answer, untouched default, escape at the first question,
  unsubmitted multi choices, partial custom text, queued advancement, unmapped
  focus), `TestQuestionEscapeAfterNavigatingBack`,
  `TestQuestionCompletedFormStillFinalizesAll`,
  `TestQuestionPartialSkipSettlesThroughModel`,
  `TestQuestionPartialSkipCannotSettleReplacement` and the
  `Esc skip remaining` assertion in `TestFooterQuestionShowsAnswer`; commit
  `p10-question-answers: task 2 — preserve completed answers on Escape`.

### A legitimate “other” option collides with the custom-answer sentinel

Question options use their labels as values, while the appended custom-answer
choice uses the literal value `other`. An option labelled exactly `other` is
therefore interpreted as the custom choice rather than as the chosen answer.
This affects both single- and multiple-choice questions.

- Evidence: `internal/app/tui/question.go:15`, `:167`, and `:215`;
  `internal/tools/native/question.go:215`.
- Potential direction: give the custom choice an identity that cannot collide
  with a supplied option value.
- Status (2026-10-30): fixed. Choice identity is internal and the two
  namespaces are disjoint: every supplied option carries the generated huh
  value `option:<zero-based index>` (`questionOptionValue`) and the appended
  custom row carries `custom` (`questionOtherSentinel`), so a supplied label of
  `other`, `Other…`, `custom`, `option:0`, empty or duplicated can no longer be
  read as the custom answer. `questionOptionLabel` decodes an identity back to
  its source label on finalisation and accepts only canonical in-range
  identities, so a malformed value is dropped instead of being recorded as if
  the user had typed it. The generated identities never leave the TUI:
  `session.Question`, `session.Answer` and the wire format are unchanged. The
  custom row is labelled `Other… (custom answer)`, and rendered keys that would
  collide — including duplicates, which huh's multi-select would otherwise
  toggle together — gain ` (option N)` from their one-based source position,
  repeated if still not unique, so every rendered row plus the custom row is
  unique; descriptions still affect only the rendered key, never the answer.
  Evidenced by `TestQuestionListedOtherDoesNotOpenCustomInput`,
  `TestQuestionMultiOtherAndCustomAreDistinct`,
  `TestQuestionInvalidOptionIdentityIsNotAnAnswer`,
  `TestQuestionPromptKeysFitMinimumWidth` and
  `TestQuestionLiteralOtherSettlesThroughModel`; commit
  `p10-question-answers: task 1 — give the custom answer its own identity`.

### Plan review cannot reach overflow diagnostics

The authored-plan review clips its diagnostics to the dock height with a fixed
focus at the first row. Its keyboard handler has no scrolling, so diagnostics
past the visible rows cannot be read there, even when one of them blocks
acceptance. Long diagnostic lines are also truncated by the panel frame.

- Evidence: `internal/app/tui/sddreview/review.go:52` and `:69`.
- Potential direction: make diagnostics scrollable and wrap long messages.
- Resolved 2026-10-28 (`2f7cff04`, task 2 of the
  [P5 plan](plans/2026-10-28-p5-plan-review-recovery-plan.md)): every
  diagnostic is built as a logical row naming its severity and location in
  text, wrapped to the panel's real inner width with `ansi.Wrap` (paths break
  on directory separators), and windowed by a physical-row scroll offset that
  Up/Down, PgUp/PgDn and Home/End move under an overflow-position row. The
  frame header, the persistent action row and — only when the body overflows —
  the position row are reserved first, so no diagnostic or path is silently
  truncated and the keys stay reachable. Tests page a blocked inspection from
  beginning to end at the real dock budget for 80×24, 120×40 and 200×60 and
  assert every marker and suffix is reachable, the frame never exceeds its
  budget, and Enter stays blocked.

### The plan-review recovery hint conflicts with discard behavior

When a generated candidate has errors, the review panel says to edit the plan
and run `/sdd`, but Escape is its only exit and discards the candidate. The
host ignores errors from removing it, so even the outcome of discard can be
unclear. A user who exits the panel to follow the edit instruction may find the
file gone.

- Evidence: `internal/app/tui/sddreview/review.go:52` and `:108`,
  `internal/app/tui/model.go:2687`.
- Potential direction: provide a keep-and-edit exit distinct from discard,
  and report removal failures.
- Resolved 2026-10-28 (`8ee41eef`, task 1 of the
  [P5 plan](plans/2026-10-28-p5-plan-review-recovery-plan.md)): Escape now
  emits a keep, and the host closes the review, keeps the file and adds a
  transcript message naming the retained path and the edit-then-`/sdd` next
  step. Discard is a deliberate two-press `d` confirmation that says which
  candidate it would delete, runs only against the active review's candidate
  through `sddauthor.RemoveCandidate`, and on failure keeps the panel open,
  preserves the file, disarms the action and renders the error instead of
  reporting a removal that did not happen. Panel-to-host messages carry their
  originating panel, so a delayed message cannot act on a replacement review,
  and both a resize and the parent's below-80×24 size gate disarm the
  confirmation.

### Preflight cast list can hide the selected role or blocking error

The preflight panel clips from the first row regardless of the selected role,
and its navigation changes the role without scrolling the list. At restricted
heights, moving to a later role can leave the selection off screen; blocking
errors or warnings below the visible region can also be unreachable even
though they prevent the run or affect the user's decision.

- Evidence: `internal/app/tui/castlist/castlist.go:319` and `:374`.
- Potential direction: scroll to the selected row and provide access to all
  diagnostic rows, including wrapped messages.
- **Status (2026-10-28): fixed.** `Panel.View` assembles the body as physical
  rows (metadata, strategy row, disabled-option reasons, every role row, the
  blocked banner) and records each role's first physical line. Error and
  warning text is wrapped with `ansi.Wrap` instead of truncated, so no
  diagnostic segment is lost. A panel-owned offset plus a `followSel` flag set
  by `moveCursor` bring the newly selected role into the window, and the offset
  is clamped on every render for resizes and data changes. PgUp/PgDn and
  Home/End page the body independently of the selection, muted `↑ more` /
  `↓ more` rows and a position row appear only when content overflows, and
  Enter's blocked check, strategy cycling, overrides and Escape are unchanged.
  Evidenced by `TestCastListSelectionVisibleAfterNavigation` and
  `TestCastListPagesEveryDiagnostic` (a non-role blocking error, a long later
  warning and a disabled-strategy reason accumulated through real `Update`
  paging). Limitation, by design: manual paging can move the window away from
  the selected role so diagnostics on non-role rows are reachable; Up/Down
  restores selected visibility and the override target is unaffected.

### Plan-run gate can hide the question it asks the user to answer

The gate panel places the question and report above the answer field, then
clips around the final input row. For long questions or reports, the start of
the question can be pushed out of view. The panel offers no scroll keys for
the hidden rows, but Enter still sends the answer and Escape stops the run.

- Evidence: `internal/app/tui/gatepanel/gatepanel.go:57` and `:80`.
- Potential direction: keep the answer input reachable while allowing the
  full question and relevant report to be reviewed.
- **Status (2026-10-28): fixed.** The six-line report cap and every truncation
  are gone: the complete question and report are wrapped with `ansi.Wrap` and
  `wrapBreakpoints` into a bounded scrollable body above a pinned input row.
  The offset and last body height live on the panel and are normalised on
  every render, so a resize or a longer report cannot park the window past the
  text. PgUp/PgDn and Home/End scroll the body
  before the text field; arrows and typing still reach the editor; a nonempty
  Enter still emits `AnswerMsg` and Escape still emits `StopMsg` after paging.
  A compact position row and `⇞⇟ page` hint advertise overflow within the
  budget, and when only one usable row exists the input keeps it with the
  overflow advertised in the frame hint. Evidenced by
  `TestGatePagesQuestionAndFullReport`, `TestGatePagingResizePreservesAnswer`,
  `TestGateOverflowThemes` and `TestGatePagingThroughModel` (composed frames at
  80×24 and 120×40, answer still reaching the existing gate handler).

### MCP sign-in fallback cannot expose a long authorization URL

The MCP authorization panel presents the URL as one line inside a frame that
truncates long lines. It has no copy or scrolling action, despite relying on
manual access to the displayed URL when opening a browser fails. Typical
authorization URLs can exceed the panel width, leaving the fallback unusable.

- Evidence: `internal/app/tui/mcpauth/panel.go:105`, `:121` and `:148`;
  `internal/app/tui/chrome/frame.go:85`.
- Potential direction: provide a keyboard action that copies the complete URL,
  as the provider sign-in flow does, and show whether copying succeeded.
- Status (2026-10-29): fixed (tasks 5 and 6 of the P9 plan, commits `aa8fffcf`
  and `c7c1ac5e`). The panel wraps the whole URL to its cell width inside a
  pageable body with PgUp/PgDn/Home/End and an above/below position indicator,
  keeps the copy and cancel hints visible, and renders control characters as
  one-cell placeholders without altering the copied bytes. `c` emits
  `CopyLinkMsg`, which the host honours only while the live panel still accepts
  it, and routes through the existing clipboard path: a confirmed local write
  flashes `Copied …`, while a failed or unavailable helper falls back to OSC 52
  and says so instead of claiming the terminal confirmed the copy. The
  `OpenBrowser` comment no longer claims the one-line display is sufficient for
  manual copy. Evidenced by `TestMCPAuthLongURLCanBePagedAndCopied`,
  `TestMCPAuthCopyWaitsForURLAndStopsAfterCancel`,
  `TestMCPAuthURLViewportClampsOnResize` and `TestMCPAuthCopyThroughModel`
  (injected clipboard and browser stub; no network).

### Plugin install can confirm a scan of a different source

After a plugin source is scanned, the Source field remains editable, but
changing it does not invalidate the scan or its Confirm action. Confirm uses
the previously scanned source and directory, even though the field now shows
the new value. This can install different executable plugin content from what
the user believes they selected.

- Evidence: `internal/app/tui/plugins/panel.go:257`, `:292` and `:434`.
- Potential direction: invalidate the scan whenever the source changes and
  bind the confirmation visibly to the exact scanned source.
- Status (2026-10-27): fixed. A scan is bound to the frame generation, the
  entered source and the scope it started with: editing either field, leaving
  the frame, or the dock releasing the panel invalidates the review, cancels
  the scan and deletes its clone, and a result only becomes the review when
  the panel claims it and the lease, generation, source and scope all still
  match. The confirmation now shows the normalized source and scope the scan
  read as their own rows. Covered by
  `TestScanReviewInvalidatedBySourceOrScope`,
  `TestAbandonedScanCannotAttachToReopenedFrame` and
  `TestPanelReplacementCleansUndeliveredScan`.

### Plugin review clips executable commands without a detail view

The scan review shows hook and MCP commands as read-only scalar rows. The
shared row renderer truncates long values, and these read-only rows cannot be
opened to inspect the rest. Confirm remains available even when a command's
suffix is hidden.

- Evidence: `internal/app/tui/plugins/panel.go:309`,
  `internal/app/tui/listpanel/fieldlist.go:545` and `:621`, and
  `internal/app/tui/chrome/row.go:92`.
- Potential direction: offer a wrapped or scrollable full-content view for
  every executable entry before installation.
- Status (2026-10-27): fixed. Hook, MCP server and MCP policy rows are now
  actions that open the entry's complete text in a wrapped, pageable view
  (PgUp/PgDn to page, Esc back to the same row), escaped with
  `strutil.ShowControls` so a control byte is shown rather than executed, with
  each MCP argument quoted separately so its boundaries stay visible. Enter in
  that view installs nothing: Confirm remains the only installation action.
  A follow-up review pass closed two gaps in that view: its title row escaped
  with `ShowControls` alone, which leaves newlines intact, so a plugin-supplied
  name could add a row the view never budgeted for — titles now go through
  `reviewTitle`, which flattens layout characters first — and an MCP server's
  `env`, `headers` and `trust` were never rendered at all, so the screen asked
  the user to authorise values it did not show. Covered by
  `TestReviewDetailShowsCompleteExecutableContent`,
  `TestReviewDetailResizeAndNoColor` (both at 80×24) and
  `TestMCPReviewShowsHiddenServerFieldsAndEscapesNames`.

### Shared field lists budget logical rows instead of display lines

`chrome.Row` can return a two-line stacked row, but `FieldList.View` adds it
as one element and uses element counts to window around the cursor. When
several values stack, the rendered output can exceed its height budget and a
parent frame can clip selected rows, errors, or controls at the bottom. This
shared engine is used by settings and several management panels.

- Evidence: `internal/app/tui/chrome/row.go:31`,
  `internal/app/tui/listpanel/fieldlist.go:589` and `:621`, and
  `internal/app/tui/chrome/chrome.go:41`.
- Potential direction: window physical rendered lines while preserving the
  selected field's visibility; verify with stacked rows at narrow widths.
- **Status (2026-10-28): fixed.** `FieldList.View` now splits every composed
  element into physical lines before recording its start and appending it, so a
  stacked `chrome.Row` costs the two rows it actually occupies. The focus line
  is tracked through that physical sequence: the add input while adding, the
  line holding the input during a scalar edit, the selected option during enum
  picking, otherwise the selected field's marker. The local `clipLines` wrapper
  was replaced by `windowLines`, a bounded physical-line window that returns
  empty for a non-positive height, renders the focus line alone at height 1,
  and reserves `↑ more` / `↓ more` only when an indicator fits alongside the
  focus. Shared `chrome.ClipLines` is unchanged, because unrelated callers
  depend on its current behaviour. Setters, row IDs and navigation are
  untouched. Evidenced by `TestFieldListPhysicalHeightAndFocus` (stacked rows,
  selection near both ends, header and description, heights 0/1/2, Unicode
  widths, resize), `TestFieldListFocusedEditorAndPickerVisible` (edit, a failed
  setter error, the add prompt, and an enum selection below the window) and
  `TestSkillPanelStackedFieldsKeepSelectionVisible`, which drives the real
  skills panel frame at narrow width.

### Plugin manager ignores bracketed paste

The plugin panel handles keypresses and asynchronous results but has no
`tea.PasteMsg` branch. Pasting a URL into the install Source field or a query
into the filter therefore does nothing in terminals that send bracketed
paste. The skills panel explicitly forwards paste to the relevant control.

- Evidence: `internal/app/tui/plugins/panel.go:888` (`Update`, where a
  `tea.PasteMsg` case is missing) and `internal/app/tui/skills/panel.go:504`.
- Potential direction: route paste to the active field or filter, and cover
  both paths through the panel's real update handler.
- Status (2026-10-28): fixed. `Panel.Update` gained the missing
  `tea.PasteMsg` branch: the full-content review consumes it, a drilled frame
  or an active edit receives it through `settings.FieldListUpdate`, and only an
  unedited root forwards it to the filter. Evidenced by
  `TestPluginPasteFollowsFocus` (root query, an idle Source row, an open edit, a
  non-text row and the full-content review, each asserting the other input does
  not move) and by `TestManagementPanelPasteAndEscapeThroughModel`, which sends
  a bracketed paste through `Model.Update` at 80×24 and 140×40 and asserts the
  visible control received it while the chat composer draft stayed unchanged.

### Escape leaves skill and plugin frames while editing a field

Both panels intercept Escape before the shared field editor sees it. When a
user is editing a Source field and presses Escape to cancel the edit, the
panel instead pops the entire install frame; the plugin panel also abandons
its scan. This differs from the shared editor's normal Escape behavior.

- Evidence: `internal/app/tui/skills/panel.go:477`,
  `internal/app/tui/plugins/panel.go:888` (`Update`, which pops the install
  frame before the field editor sees Escape), and
  `internal/app/tui/listpanel/fieldlist.go:408`.
- Potential direction: give an active edit first refusal on Escape, then pop
  the frame on a subsequent Escape.
- Status (2026-10-28): fixed. Both panels now ask the active list whether an
  edit (scalar editor, enum picker or add prompt) is open, and forward Escape
  to it before the disarm → frame-pop → close path; a cancellation runs no
  setter, so the committed value stands, and the plugin panel keeps its scan
  lease and generation until a later Escape actually leaves the frame.
  Evidenced by `TestEscapeCancelsSkillEditorBeforeLeavingFrame` (committed
  source, frame and filter unchanged across a cancelled edit and picker, then
  back, then closed), `TestSkillEscapePreservesRemovalConfirmationSafety`,
  `TestPluginEscapeCancelsEditBeforeScanAbandonment` (a cancelled Source edit
  keeps the lease and generation intact and a matching result can still be
  reviewed; committing a changed Source or Scope still invalidates the review)
  and the host-level `TestManagementPanelPasteAndEscapeThroughModel`.

### Agent editor misroutes paste and intercepts question marks

The agent roster sends every paste to its root filter, even when a picker or
an editable field in a drilled-in frame owns the keyboard. It also intercepts
`?` for the legend before forwarding input to a field editor. Pasting an
agent name or system prompt can change the hidden filter instead, and a
question mark cannot be typed into a scalar editor.

- Evidence: `internal/app/tui/agents/panel.go:612`, `:626` and `:661`.
- Potential direction: route text and paste to the focused editor or picker
  before applying roster shortcuts.
- Status (2026-10-28): fixed. `Panel.Update` keeps the picker overlay's first
  refusal on keys, then forwards `?` and Escape to the active list whenever a
  field edit, enum picker or add prompt is open, so `?` becomes field input and
  Escape cancels only that edit; `tea.PasteMsg` now goes to the picker overlay
  while one is open, to the drilled frame or active edit through
  `settings.FieldListUpdate`, and to the root filter only when neither owns
  focus. With nothing focused, `?` still toggles the roster legend. Evidenced by
  `TestAgentPasteFollowsFocusedControl` (root filter, idle and active scalar
  edits, the custom-name picker and a choice picker, each with its commit path
  and persistence receipt) and `TestAgentQuestionMarkAndEscapeRespectEditor`
  (`?` typed into a scalar and into a collection-add prompt, `?` reaching the
  picker, Escape cancelling without saving or popping, and the legend still
  toggling when nothing owns the key), plus the host-level picker case in
  `TestManagementPanelPasteAndEscapeThroughModel`.

### Skill and plugin removal confirmations remain armed after navigation

The first Remove activation sets an armed flag keyed only by name. Neither
panel disarms it when navigating away or leaving the detail frame. Returning
later can remove the item with one Enter, and same-name entries in different
scopes share the flag.

- Evidence: `internal/app/tui/skills/panel.go:202` and `:477`;
  `internal/app/tui/plugins/panel.go:166` and `:553`;
  `internal/app/tui/listpanel/fieldlist.go:167`.
- Potential direction: disarm on navigation and exit, and identify an item by
  both scope and name.
- Status (2026-10-27): fixed. Both panels key the armed flag on a private
  identity — scope plus the discovered source for skills, scope plus the
  lockfile name for plugins — register a `settings.SetFieldDisarm` callback on
  the Remove row, and call `DisarmCurrent` before Escape pops or closes the
  frame. The list engine's arrow keys already invoked a row's disarm, so
  leaving the row now clears it. Covered by
  `TestRemoveConfirmationDisarmsOnNavigationAndEscape` and
  `TestRemoveConfirmationIsScopedBySource` / `TestRemoveConfirmationIsScoped`.

### Leaving an in-flight plugin scan loses ownership of its result

A scan runs with a background context and creates a temporary clone. Leaving
the frame before the result arrives cannot discard a clone whose path has not
yet been returned. If the panel closes, the dock no longer routes the result
to it, leaving the clone behind; if the panel remains open, a late result can
attach to a different install flow.

- Evidence: `internal/app/tui/plugins/panel.go:356`, `:424`, `:553` and
  `:587`; `internal/app/tui/dock/dock.go:53`.
- Potential direction: retain or cancel ownership of each scan until cleanup,
  and correlate results with the active install operation.
- Status (2026-10-27): fixed. A scan publishes its temporary parent to a
  mutex-protected lease that cancels the clone and deletes the directory when
  the frame leaves, when the panel is replaced, or when the lease was closed
  before the publish arrived. Results carry that lease and a generation and can
  be claimed once, by the frame that started them, so a late result cannot
  attach to a different flow. The panel implements `dock.CloseAware`, so a
  released panel disposes of its scan even though its result is never
  delivered. Covered by `TestScanLeaseClosePublishClaimRaces`,
  `TestAbandonedScanCannotAttachToReopenedFrame` and
  `TestPanelReplacementCleansUndeliveredScan`.

### Leaving a plugin install can remove the clone during its copy

The install command copies the scanned clone asynchronously after removing the
existing destination. Navigating out far enough with Escape can concurrently
call `discardScan` on that same clone. (For an update opened from a detail
frame, the first Escape pops to that frame; a subsequent Escape cleans the
scan.) The copy can then fail or leave a partial destination after the prior
installation was already removed.

- Evidence: `internal/app/tui/plugins/panel.go:424`, `:434` and `:553`.
- Potential direction: transfer clone ownership to the installer and replace
  the destination atomically only after a successful staged copy.
- Status (2026-10-27): fixed. Confirming hands the clone to the install worker,
  which defers its own cleanup on every outcome and is its only owner from that
  point, so closing or replacing the panel no longer touches files it is
  copying. Installation stages the copy inside the store, rejects a stage whose
  hash differs from the reviewed one, moves an existing install to a unique
  same-store backup, renames the stage into place and commits a prepared
  lockfile last, restoring the backup on any ordinary failure. Covered by
  `TestInstallOwnsCloneAfterConfirm`, `TestConfirmRejectsChangedClone` and
  `TestInstallReviewed*`.

### Skill removal reconstructs the wrong filesystem path

Skill discovery accepts both top-level `.md` files and bundles whose directory
name need not equal the skill's frontmatter name. The TUI removes a path built
from the skill name without retaining the discovered source path. Removing a
single-file skill can report success while leaving it in place; a mismatched
bundle name can target another path.
The parsed frontmatter name is only checked for being nonempty, so a name
containing `..` can make the confirmed removal target escape the skill store
and recursively delete an unrelated path.

- Evidence: `internal/skills/loader.go:17` and `:46`,
  `internal/skills/skill.go:141`;
  `internal/app/tui/skills/panel.go:290`.
- Potential direction: retain the source identity and remove exactly the
  selected file or bundle, with the scope boundary validated.
- Status (2026-10-27): fixed. `skills.ScopedSkill` now carries the direct
  child of its store (`SourceName`) and its `Lstat` snapshot at discovery, and
  `skills.RemoveScopedSkill` validates that name (`filepath.IsLocal`, no
  separator, no nil snapshot, known scope), opens the store with `os.OpenRoot`,
  rejects symlinks, unsupported entry types, a missing child, and a child that
  is no longer `os.SameFile` as the snapshot, then calls
  `Root.RemoveAll(SourceName)`. The skills panel removes through that helper
  with the root chosen from the scope. The identity comparison detects a
  replacement before removal but is not an atomic lock; `os.Root` is the
  containment boundary if an entry changes mid-removal. Covered by
  `TestRemoveScopedSkill*`, `TestListScopesRecordsRemovalSource`, and
  `TestRemoveUsesDiscoveredSource` / `TestRemoveReportsStaleOrSymlinkSource`.

### Global skill installation ignores relocated config directories

The skills panel lists global skills under `config.UserDir`, which honors the
Marshal and XDG config overrides. Its install action instead uses `ScopeDir`,
which hardcodes `~/.config/marshal/skills`. With a relocated config directory,
an install can report success but not appear in the panel or runtime.

- Evidence: `internal/app/tui/skills/panel.go:93` and `:268`;
  `internal/skills/install.go:16`.
- Potential direction: resolve install and discovery through the same effective
  global directory.
- Status (2026-10-28): fixed. `runInstall` resolves its target from the
  panel's own effective roots — `p.globalSkillsDir()` for `global`,
  `p.projectSkillsDir()` for a trusted `project` — so an install lands in the
  directory both the panel and the loader read, including under
  `MARSHAL_CONFIG_DIR` and `XDG_CONFIG_HOME`. An unknown scope, or a project
  install in an untrusted folder, starts no command and reports the refusal on
  the active list. `skills.ScopeDir` is unchanged for its other callers; the
  panel no longer uses it. Covered by
  `TestInstallUsesEffectiveGlobalSkillsDir` and
  `TestInstallProjectScopeAndTrust`.

### Skill scope selection does not determine which version loads

The panel lists global and project skills separately, but Load now looks up
only the name in the runtime's merged index. A project skill overrides a
same-named global skill in that index, so choosing Load now on the global row
can load the project version without saying so.

- Evidence: `internal/app/tui/skills/panel.go:153`, `:186` and `:303`;
  `internal/skills/loader.go:46` and `:77`.
- Potential direction: make the effective version and precedence explicit or
  load the selected scoped identity.
- Status (2026-10-28): fixed. Load now re-derives the selection through
  `loadableSelection`, which re-runs `skills.ListScopes` with the panel's
  effective roots, matches scope plus discovered source name, verifies the
  source still has the same identity (`os.SameFile` against the `Lstat`
  snapshot) and the same parsed content as the selected value, and compares
  that content with the runtime index and the session's active-skill state.
  When the selection is not exactly what the session holds, the row reads as
  unavailable, Enter loads nothing, and the reason is shown. The panel claims
  only that the content matches the current session, never that the runtime
  loaded the selected scope, because the index carries no provenance and a
  matching body from another source is the same value. A matching value is
  loaded from a temporary one-entry index, so a concurrent index update cannot
  swap the body between validation and lookup. Covered by
  `TestLoadNowUsesMatchingSessionContent` and
  `TestLoadNowRevalidatesSelectedSource`.

### Skill list and runtime index diverge after install or removal

The list rescans disk, but Load now uses the runtime index captured when the
panel was created. Installation and removal refresh the list without updating
that index. A newly installed skill may be displayed but fail to load in the
current session, and removed or changed content can remain in the index.

- Evidence: `internal/app/tui/skills/panel.go:153`, `:186`, `:303` and
  `:449`; `internal/app/runtime.go:1761`.
- Potential direction: reconcile the runtime index after changes, or make a
  restart requirement explicit and prevent misleading current-session actions.
- Status (2026-10-28): fixed by making the restart requirement explicit rather
  than by reconciling the live index. The runtime index is a session-owned
  artifact that also carries verified plugin skills, and replacing it with a
  directory-only scan would drop those entries, so disk installs, edits and
  removals do not mutate it. Install and remove report that a restart applies
  the change, the panel carries a standing note that disk changes take effect
  after a restart and that project and plugin skills can shadow a global entry,
  and a newly installed or changed entry reads as unavailable to Load now until
  a new session rebuilds the index through `skills.LoadSkills`. Skill rows are
  identified by scope plus discovered source rather than name alone, so two
  entries sharing a name are two rows bound to their own sources. Covered by
  `TestRestartOnlySkillChangesDoNotMutateRuntimeIndex`,
  `TestSkillDiskChangeReportsRestart` and
  `TestDuplicateSkillRowsHaveDistinctIdentity`.

## Removal candidates

### Browse Enter contains an unreachable subagent-card branch

Browse-mode Enter calls `subagentAtCursor(true)` and would drill into a child
if it succeeded. That function explicitly returns false for `onlyCard`, because
the in-place card it referred to no longer exists. Its other caller passes
false and remains useful for the `f` action.

- Evidence: `internal/app/tui/browse.go:258`, `:283` and `:381`.
- Potential direction: remove the unreachable Enter branch and simplify the
  now-unnecessary parameter after checking intended keyboard discoverability.

### Plan review has an unused import retained by a dummy reference

`sddreview` imports `strings` solely for a package-level `var _ =
strings.TrimSpace`; neither is used by the panel. Both can be removed together
if no concurrent work intends to use them.

- Evidence: `internal/app/tui/sddreview/review.go:9` and `:130`.

### Transcript click target retains an unreachable subagent branch

The click target still carries a subagent pointer and the click handler has a
drill-in branch for it, but no region populates that field after the in-place
cards were removed. The source explicitly identifies this as unreachable.

- Evidence: `internal/app/tui/click.go:9` and `:155`.
- Potential direction: remove the unused field and branch if future card
  restoration is not planned; keep current transcript and tray drill paths.

## Verification observations

- `go test ./internal/app/tui/... -timeout 2m` had one failure:
  `TestStatusLineUntrustedIsWarningColored` expects ANSI 172 or 33, while the
  rendered warning uses ANSI 215. The test also fails in isolation. Determine
  whether the expected palette or the assertion is stale before changing it.
- Other TUI packages in that run passed. No manual terminal, screen reader,
  multiplexer, or full-suite verification has yet been performed for this audit.
- Built a fresh binary at `/private/tmp/marshal-tui-audit` and ran the scripted
  usability suite. Its help-open-and-close scenario passed; it does not cover
  the other TUI workflows recorded here.
- The later prompt and navigation findings are based on code-path inspection;
  reproduce them interactively and add focused regression coverage during triage.
- The browse branch above is a candidate for removal; no change has been made.

## Coverage and remaining verification

This inventory reflects code-path review of the main composer and prompt
routing; approvals, sudo, access, questions and postmortem; transcript browse,
inspector and copy; tray and mouse routing; provider discovery and sign-in;
settings, model options, agents, skills and plugins; session sheet; plan
preflight, plan review and run gates; and shared panel, list and frame layout.
Existing tests for resize, themes, monochrome rendering and keyboard paths were
also inspected, and the TUI package suite was run as noted above.

This is not a proof that every path is defect-free. The remaining verification
is primarily behavioral: reproduce the findings at 80×24 and larger sizes,
exercise live resize and long content, and check keyboard and mouse interaction
in real terminals, over SSH and in a multiplexer. Check light and dark themes,
16-color and `NO_COLOR` modes, and assistive technology manually. Expand the
real-binary scripted scenarios beyond opening help. Recheck async and failure
paths with controlled fixtures during triage, and examine other removal
candidates before deleting code.
