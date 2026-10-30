# P10 question answers — implementation plan

Date: 2026-10-30. Status: proposed; not executed.
Base: `c67c960a6431a5b4749b33abe09d1dca8e485ea3`.
Requirements: [triage item 11](../triage.md#p2--interaction-and-visibility),
including [skip losing prior answers](../audit.md#skipping-a-multi-question-form-discards-earlier-answers)
and [the other sentinel collision](../audit.md#a-legitimate-other-option-collides-with-the-custom-answer-sentinel).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** Escape preserves earlier completed answers, and a supplied option
named `other` remains distinct from the custom-answer choice.

**Non-goals:** change question transport, agent policy, input widgets, ACP
decline semantics, answer serialization, web UI, or unrelated TUI findings.
This is phase 10 of the plan sequence and addresses P2 triage item 11.

**Confirmed product decision:** the user chose “Keep completed answers”:
Escape on a multi-question form keeps completed earlier answers and marks the
current and remaining questions `session.AnswerUnanswered`. A question is
complete for this purpose once the form has advanced to a later question;
highlighting a default option, typing partial text, toggling an unsubmitted
multi-select, or entering the custom input does not finish the current
question. If a user navigates back, the focused question and every later
question are skipped, even if a later one was previously visited. If form
advancement is still queued when Escape arrives, the currently focused
question is skipped. Escape at the first question skips all questions.

**Other decisions:**

- Choice identity is internal, not a label or wire value. Give each supplied
  option an index-based string identity `option:<zero-based-index>` and the
  appended custom row the identity `custom`. The namespaces are disjoint even
  when a supplied label is `other`, `custom`, `option:0`, empty, or duplicated.
  Resolve the index back to its original label on finalization, preserving
  descriptions only in the visible row and preserving the current joined
  multi-answer format. The generated identities never leave the TUI.
- Name the appended row `Other… (custom answer)` so a supplied `Other…`
  label can be visually distinguished. If rendered keys collide, append
  ` (option N)` to each colliding supplied row, using its one-based source
  position, and repeat with a disambiguating suffix if necessary until all
  rendered keys (including the custom row) are unique. This also avoids huh's
  multi-select matching duplicate displayed keys together. The custom input is shown only when
  that row is selected; blank custom text retains the existing unanswered or
  omitted behavior. Normal form completion keeps its existing handling of
  otherwise empty answers.
- Do not copy live form bindings indiscriminately on Escape: huh binds values
  while navigating and typing, before submission. Find the active question
  by `form.GetFocusedField()` identity in `qm.fields`; finalize only the
  prefix preceding that question. When no focused field can be mapped and the
  form is not completed, fail closed to all unanswered. Let the form's normal
  completed state continue to finalize all questions.
- The parent continues to settle through the existing pending-question
  identity and canonical `ResolveQuestion` claim. Cancellation or replacement
  must not send partial answers to a different prompt. Preserve the hidden
  composer and its paste attachments, as established by P3.
- Use isolated tests and no real credentials or services. Preserve unrelated
  untracked files; stage named paths only. Tasks run sequentially and are
  independently verifiable.

## Contracts

No cross-package API or wire change. Task 1 adds private TUI helpers in
`internal/app/tui/question.go`:

```go
func questionOptionValue(index int) string
func questionOptionLabel(options []session.QuestionOption, value string) (string, bool)
```

The former returns `option:` plus the decimal index; the latter accepts only a
canonical, in-range generated option identity and returns its original label.
`questionOtherSentinel` becomes the internal value `custom`. Malformed and
out-of-range identities are never emitted as answers.

Task 2 adds the following private helpers, preserving the established
`session.Answer` shape:

```go
func (qm *questionModel) focusedQuestionIndex() (int, bool)
func (qm *questionModel) finalizeAnswer(i int)
```

`finalizeAnswer` contains the existing per-question encoding; full completion
calls it for every question. Skip starts with `UnansweredAnswers` and calls it
only for indices before the focused question. A blank or invalid prefix answer
remains `AnswerUnanswered` on skip rather than being invented.

## Task 1: Give the custom answer its own identity

**Goal:** listed options and the custom-answer control can never collide,
and answers still contain original labels or custom text.

**Files:** `internal/app/tui/question.go`,
`internal/app/tui/question_test.go`.

**Steps:**

1. Implement the index-based option helpers from Contracts. Update
   `buildQuestionOptions` to use `questionOptionValue(i)` for every supplied
   option and use the `custom` sentinel for the appended `Other… (custom
   answer)` row. Keep display labels and descriptions, adding only the
   collision suffix specified in Other decisions. Update
   `buildForm` hide functions to inspect the new sentinel.
2. Update `finalizeAnswers` to decode each selected supplied identity
   against that question's options; include only valid labels, in the
   existing selected order. Substitute trimmed custom text only for the
   custom identity. A malformed identity is ignored; it must not be emitted
   as user text. Do not alter `session.Question` or `session.Answer`.
3. Update existing tests in `question_test.go` that set `selects`/`multis`
   or expect labels as internal `huh.Option.Value` to use generated identities
   and assert the output labels remain unchanged.
4. Add `TestQuestionListedOtherDoesNotOpenCustomInput` and
   `TestQuestionMultiOtherAndCustomAreDistinct`, driving the actual huh form
   with `questionModel.Update` and the commands returned by it. Cover the
   literal `other`, `Other…`, `custom`, `option:0`, duplicate labels, listed
   descriptions, custom text, and blank custom text. Assert the visible
   selected row, custom-input visibility and final `Answers()` values; use
   both keyboard selection and multi-select keys. Add
   `TestQuestionInvalidOptionIdentityIsNotAnAnswer`.

**Verify:**

```bash
go test ./internal/app/tui -run '^Test(Question|BuildQuestion)' -count=1 -timeout 90s
gofmt -l internal/app/tui/question.go internal/app/tui/question_test.go
```

Expected: tests pass and formatting prints no paths. Tests must drive the
form's returned navigation messages, not only set the bound values directly.

## Task 2: Preserve completed answers on Escape

**Goal:** skipping a form returns earlier completed answers and unanswered
sentinels for the current and remaining questions.

**Files:** `internal/app/tui/question.go`,
`internal/app/tui/question_test.go`, `internal/app/tui/help/help.go`.

**Steps:**

1. Implement `focusedQuestionIndex` using the existing `fields` mapping and
   `focusedFieldFor`. Extract the per-index logic from `finalizeAnswers` into
   `finalizeAnswer(i)`, keeping normal full completion behavior. On an Escape
   key, reset `qm.answers` using `session.UnansweredAnswers`, determine the
   focused question and finalize only its preceding indices. For a blank or
   invalid prefix value, retain the unanswered sentinel. Set `done` and
   `aborted` as today. Keep unexpected huh `StateAborted` as an all-unanswered
   abort; only explicit Escape applies the confirmed skip behavior.
2. Update the question comment and `help.PromptKeys` Escape label from
   `skip` to `skip remaining` so the action is clear. Preserve its existing
   key routing and the parent `handleQuestion` settlement path.
3. Add `TestQuestionEscapeKeepsCompletedPrefix`: drive a real multi-question
   form through actual commands, complete free text, single choice, multi
   choice and custom answer in separate table cases, begin the next question,
   then Escape. Assert earlier exact answers and `AnswerUnanswered` for the
   current and later entries. Include an untouched default single selection,
   unsubmitted multi choices, partial custom text, Escape at the first
   question, the command-pending boundary, and unmapped focus fallback.
4. Add `TestQuestionEscapeAfterNavigatingBack` for the specified prefix
   behavior after returning to an earlier question. Add
   `TestQuestionCompletedFormStillFinalizesAll` for normal completion and
   preserve existing single-question skip behavior. Update any help tests for
   the new label and assert the hint remains legible at minimum width.

**Verify:**

```bash
go test ./internal/app/tui -run '^Test(Question|BuildQuestion|PromptKeys)' -count=1 -timeout 90s
go test ./internal/app/tui/help -count=1
gofmt -l internal/app/tui/question.go internal/app/tui/question_test.go internal/app/tui/help
```

Expected: tests pass and formatting prints no paths.

## Task 3: Verify settlement and draft preservation through the host

**Goal:** partial skip and the literal `other` answer reach the correct
question owner once, without disturbing the composer or cancellation rules.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/model_test.go`.

**Steps:**

1. Update the stale `handleQuestion` comment that says abort always sends all
   unanswered: document the explicit Escape behavior and retain the existing
   registry/legacy dispatch code.
2. Add `TestQuestionPartialSkipSettlesThroughModel`: create a registered
   `PendingQuestion` with a response channel, seed a composer draft and paste
   attachments, drive initialization, keyboard, navigation commands and Escape
   through `Model.Update`, and assert the channel receives exactly one
   ordered answer per question with the completed prefix. Assert the question
   slot clears and composer text, cursor and attachments remain unchanged.
   Exercise 80×24 and a wider frame.
3. Add `TestQuestionLiteralOtherSettlesThroughModel` for both single and
   multi selection: choose the actual supplied `other` through real form
   interaction, submit, and assert the response includes its label and no
   generated identity or spurious custom input.
4. Add `TestQuestionPartialSkipCannotSettleReplacement`: after a completed
   prefix, use the existing `RegisterQuestion`/`CancelPrompt` fixture to
   cancel the owner and install a replacement. Deliver any queued form
   navigation and Escape; assert no old answers reach the replacement, its
   prompt stays owned by its own request, and the composer stays intact.
   Preserve the existing P3 draft and replacement tests.

**Verify:**

```bash
go test ./internal/app/tui -run '^TestQuestion(PartialSkip|LiteralOther|Settlement|Cancellation)' -count=1 -timeout 90s
go test ./internal/app/tui/... -count=1 -timeout 2m
gofmt -l internal/app/tui/model.go internal/app/tui/model_test.go
```

Expected: tests pass and formatting prints no paths. No transport or agent
semantics are changed; the real registered-question path is required.

## Final verification and integration

After all tasks pass, run from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p10-question-answers ./cmd/marshal
go test ./internal/app/tui/... ./internal/app/session ./internal/tools/native ./internal/acp -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui/question.go internal/app/tui/question_test.go internal/app/tui/model.go internal/app/tui/model_test.go internal/app/tui/help
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p10-question-answers go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Formatting must print no paths. Compare full-suite failures against the latest
triage baseline and report actual results. The scripted usability suite is
help-only; do not claim it exercises the question form. If a controlling
terminal is available, manually exercise completion, custom and listed
`other`, Escape, resize and a multiplexer/SSH session at 80×24; otherwise
record those limitations. After verification update both audit findings and
triage item 11 with evidence and the confirmed skip contract; commit the plan
and named documentation with the implementation.

## Plan self-review

- The skip policy is explicitly confirmed by the user and distinguishes
  committed earlier questions from live input on the current question.
- Option identities are disjoint by construction and never leave the TUI;
  both individual and multiple selection are covered by real form navigation.
- The parent test exercises keyed settlement, stale-owner cancellation and
  P3 composer preservation. No source implementation occurred in planning.
