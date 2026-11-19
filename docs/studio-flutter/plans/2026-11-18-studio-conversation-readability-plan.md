# Studio conversation readability

A plan for a thread's conversation, where everything that is not the agent
talking is drawn as if it were. Found by rendering a transcript with every
node kind at each density. The **Findings** section is the spec.

- **Authored against**: `origin/main` at `958e2ec9`.
- **Runs after**: the [visual fixes](2026-11-18-studio-visual-fixes-plan.md)
  plan, whose task 6 adds the kit's `Notice`, and the
  [dead controls](2026-11-18-studio-dead-controls-plan.md) plan, which also
  edits `transcript.dart`.
- **Executed as**: one commit per task, `studio-conversation: task N — <title>`.
- **Verification runs on Linux**, where the gallery goldens are pinned.

## Findings

1. **Thinking reads as the agent speaking.** A `thinking` node is
   `Narration` (`app/lib/src/ui/thread/transcript.dart:508-511`): the reading
   face, open, at every density, in the same column as the agent's replies.
2. **Run events and job completions read as prose.** A `runEvent` joins its
   kind, title, body and detail with newlines into `Narration`
   (`transcript.dart:512-527`), so `review · Reviewed task 2 / Looks fine /
   round 1` is three lines of reading text. A `completion` is
   `shell.run · done · the suite passed` as `Narration`
   (`transcript.dart:486-507`), the tool's wire name included.
3. **The plan row hides its progress.** A `plan` node is a `LiveRow` of
   `activeText` alone when there is one (`transcript.dart:528-534`), so
   `1 of 3` shows only when nothing is active.
4. **The receipt touches its neighbours.** The receipt line
   (`transcript.dart:535-548`) has only the row's `Space.s4` under it, so it
   sits against the last tool row above and the pinned ask below.
5. **The review head is wider than the conversation.** The ready pull
   request's head spans the whole column (`app/lib/src/ui/thread/thread_screen.dart:455-466`)
   while the transcript and, after the visual fixes, the composer stop at
   `Layout.reading`.
6. **A phone cuts the title for the headline.** The header gives the
   headline an `Expanded` beside a `Flexible` title
   (`app/lib/src/ui/thread/thread_header.dart:84-104`); at 390 wide the title
   is cut to `Clean up the test …` so that half a headline fits.

## Non-goals

- Changing what the agent projects (`internal/viewmodel`).
- Thinking in the TUI.

## Contracts

```dart
// app/lib/src/ui/kit/notice.dart — as the visual fixes plan declares it,
// with the doc comment widened by task 1:
/// Something in a conversation that is not the agent talking to the person:
/// a harness notice, the agent's thinking, a run event, a finished job.
class Notice extends StatefulWidget {
  const Notice({super.key, required this.text});
  final String text;
}
```

## Task 1: Thinking, run events and finished jobs are notices

**Goal.** Only the agent's replies are drawn in the reading style; the
rest is a `Notice`, one quiet line with its detail folded.

**Files.** `docs/studio-flutter/design-system.md`, `app/lib/src/ui/kit/notice.dart`,
`app/lib/src/ui/thread/transcript.dart`, `app/test/ui/thread/transcript_test.dart`

**Steps.**
1. design-system.md §5: widen the `Notice` row (added by the visual fixes)
   to "a harness notice, the agent's thinking, a run event or a finished
   job"; `Narration` stays "the agent's prose" alone. Widen `Notice`'s doc
   comment to match.
2. In `transcript.dart`, draw:
   - `thinking`: a `Notice` whose first line is `Thinking` and whose
     remaining lines are the thinking text, so it starts folded. At outline
     density it is hidden.
   - `runEvent`: a `Notice` whose first line is the kind with its first
     letter in capitals, then ` · ` and the title when there is one; the
     body (full density only) and the detail follow as lines.
   - `completion`: a `Notice` whose first line is the label, a space and the
     outcome, then ` · ` and the summary when there is one; the error, when
     there is one, is the second line. The full-density `OutputBlock`
     beneath it stays.
3. Tests in `transcript_test.dart`: `'thinking is a folded notice'`,
   `'a run event leads with its kind as a notice'` (replacing `'a run event
   leads with its kind'`, `:304`, with the same assertion about order), and
   `'a finished job is a notice with its error second'` (replacing `'a
   completion shows its error only when it carries one'`, `:256`). Update
   `'renders each node kind with the row for it'` (`:28`): it expects one
   `Narration`, and three `Notice`s.

**Verify.** `cd app && flutter test test/ui/thread test/kit/notice_test.dart`
passes.

## Task 2: The plan row counts, the receipt has room

**Goal.** The plan row always says how far it has got, and the receipt
stands apart from the rows around it.

**Files.** `app/lib/src/ui/thread/transcript.dart`, `app/test/ui/thread/transcript_test.dart`

**Steps.**
1. `plan` (`transcript.dart:528-534`): the headline is
   `${done} of ${total}`, followed by ` · ` and the active text when there is
   one.
2. `receipt` (`:535-548`): pad it `Space.s8` above and `Space.s12` below.
3. Tests: `'the plan row counts while a task is active'`; `'the receipt
   stands apart'` (the receipt's top is at least `Space.s8` below the row
   above it).

**Verify.** `cd app && flutter test test/ui/thread` passes.

## Task 3: The review head sits in the reading column

**Goal.** The ready pull request's head is as wide as the conversation
under it.

**Files.** `app/lib/src/ui/thread/thread_screen.dart`,
`app/test/ui/pulls/review_head_test.dart`

**Steps.**
1. Around the head at `thread_screen.dart:455-466`, centre it and cap its
   width at `Layout.reading`, keeping its `Layout.inspector` height cap and
   its `Layout.pageSide` padding outside the width cap, as the transcript's
   rows are capped (`transcript.dart:262-268`).
2. Test `'the review head is as wide as the reading column'` at 1440 wide.

**Verify.** `cd app && flutter test test/ui/thread test/overflow_test.dart` passes.

## Task 4: On a phone the title has the line

**Goal.** Below `Layout.narrow` the header's second line is the dot and the
title; the headline is not drawn.

**Files.** `app/lib/src/ui/thread/thread_header.dart`,
`app/test/ui/thread/thread_header_test.dart` (created by the visual fixes plan)

**Steps.**
1. In `ThreadHeader.build`, when `MediaQuery.sizeOf(context).width <
   Layout.narrow`, draw no headline; the title is `Expanded` instead of
   `Flexible`.
2. Test `'a phone gives the title the whole line'`: at 390 wide, no headline
   text is found and the title is not cut for a 30-character title.

**Verify.** `cd app && flutter test test/ui/thread test/overflow_test.dart test/tooltips_test.dart`
passes.

## Final verification

1. `cd app && dart format --set-exit-if-changed lib test && flutter analyze`.
2. `cd app && flutter test` on Linux, goldens included: no kit component
   changes shape, so the gallery goldens are unchanged unless the visual
   fixes plan's are still to be regenerated.

## Integration notes

- No Go or contract change.
- `Notice` is not given a label parameter: each kind's first line names it
  in words, which keeps the kit component to one shape.
