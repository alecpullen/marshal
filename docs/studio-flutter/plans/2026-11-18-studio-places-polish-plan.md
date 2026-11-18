# Studio places polish

A plan for the defects found across the Studio's places — Inbox, Overview,
Automations, Library, Settings and the factory's screens — by rendering
every screen in `app/test/support/screens.dart` at 1440 and 390 wide and
reading the code behind what looked wrong. The **Findings** section is the
spec.

- **Authored against**: `origin/main` at `958e2ec9`.
- **Runs after**: the [visual fixes](2026-11-18-studio-visual-fixes-plan.md)
  plan (both edit `inbox_model.dart`, `nav_row.dart`'s users and the gallery
  goldens) and the [dead controls](2026-11-18-studio-dead-controls-plan.md)
  plan (both edit `settings_place.dart` and the ask cards). Task 9 replaces
  the `Earlier · N` text the visual fixes plan tints `muted`.
- **Executed as**: one commit per task, `studio-places: task N — <title>`.
- **Verification runs on Linux**, where the gallery goldens are pinned.

## Findings

1. **An ask does not say which thread asked.** A card's source line is the
   feed, the project and the scope — `Fleet · marshal · Everyone`
   (`provenanceOf`, `app/lib/src/model/attention.dart:89-93`) — even when
   the ask names its thread. The card's own doc gives the intended line as
   `marshal › Reliable session recovery` (`app/lib/src/ui/kit/ask_card.dart`,
   `AskCard.source`).
2. **An empty list offers to read itself again.** `SectionPage`'s empty
   state falls back to `Read the section again` when no `emptyAction` is
   given (`app/lib/src/ui/sections/section_page.dart:149-153`), and no
   section gives one. So an empty Schedules, Skills or Secrets list offers a
   re-read beside a primary `New schedule`, `Add a skill` or `New secret`
   that the page already has (`createLabel`, `section_page.dart:105-112`).
   design-system §7: the empty state's one action is what fills it.
3. **Automations has two New buttons.** A tab with its own create —
   Schedules' `New schedule`, Watches' `New watch` — also shows the place's
   `New automation` menu beside the tabs
   (`app/lib/src/ui/automations/automations_place.dart:94-118`). The menu
   exists for the tabs that have no create of their own (F9 B4).
4. **Settings' section list has no gutter.** Its `NavRow`s sit directly in
   the column (`app/lib/src/ui/settings/settings_place.dart:134-139`), so the
   labels start 2 from the panel's edge and the selected fill touches it,
   unlike the sidebar's 8 gutter beside it.
5. **A planned state splits from its pill in a tall space.**
   `StateMessage.planned` is a `Row` that centres its pill against an
   `Expanded` column (`app/lib/src/ui/kit/state_message.dart:188-210`); given
   a tall box, the words sit at the top and the `Planned` pill floats halfway
   down the panel.
6. **A plan run's lanes touch their box.** `Lanes`' header and rows have only
   vertical padding (`app/lib/src/ui/kit/lanes.dart:86-104`) inside a
   `Box.flush` (`lanes.dart:179`), so each task's name sits on the left
   border.
7. **`1 rounds`.** A lane cell counts repair rounds without the singular
   (`app/lib/src/ui/factory/run_screen.dart:380`); the row beside it already
   gets it right (`run_screen.dart:306`).
8. **A work item's stage words are not under their stages.** `StageStrip`
   draws four equal segments, then the four words in a `Wrap` bunched at the
   left (`app/lib/src/ui/kit/stage_strip.dart:45-62`), so `Ready` sits under
   the first segment.
9. **A decision's answer field comes before its question.** The work item's
   `Your answer` field is drawn above the decision's card
   (`app/lib/src/ui/factory/item_asks.dart:155-169`).
10. **A plan is shown as raw Markdown.** The work item's Plan tab draws the
    plan in an `OutputBlock` (`app/lib/src/ui/factory/plan_tab.dart:42`), so
    headings read `## Task 1` in mono. The plan is Markdown, which
    `Narration` draws.
11. **The planner's question is the card's body.** A planning question's
    title is `Question from the planner` and the question is its detail
    (`app/lib/src/model/planning_model.dart:559-562`), while every other
    question card's title is the question itself. The card already says
    `Question` in its kind line.
12. **Earlier threads cannot be reached from the sidebar.** A project's
    threads that are closed, or idle for more than a day (`isEarlier`,
    `app/lib/src/model/nav_model.dart:108-111`), are counted and dropped:
    `NavProject` keeps only the count (`nav_model.dart:237`), and the sidebar
    draws `Earlier · N` as plain text (`app/lib/src/ui/shell/sidebar.dart:207-218`).
    With a host chosen in the switcher, the count is every host's
    (`sidebar.dart:174`), and a project whose only threads on that host are
    earlier disappears (`sidebar.dart:166`). An earlier thread opened from
    elsewhere is never shown as the open row.

## Non-goals

- The Models and routing section's flat list of roles, presets, keys and
  caps; regrouping it is a design question.
- Settings' second column of navigation beside the sidebar.
- The planning screen's `… unknown` chips, which state what has not been
  read on purpose (design-system §7).

## Contracts

```dart
// app/lib/src/model/nav_model.dart — NavProject, after task 9
class NavProject {
  const NavProject({
    required this.id,
    required this.name,
    required this.current,
    this.earlierThreads = const [],
    this.active = false,
  });

  final String id;
  final String name;
  final List<ThreadRef> current;

  /// Its threads that are closed, or idle for more than a day, most recently
  /// active first.
  final List<ThreadRef> earlierThreads;

  /// How many earlier threads it has.
  int get earlier => earlierThreads.length;

  final bool active;
}
```

Every other task changes one file's behaviour behind its existing API.

## Task 1: An ask names the thread that asked

**Goal.** A card whose ask has a thread leads its source line with
`<project> › <thread title>`; the feed and scope follow.

**Files.** `app/lib/src/model/inbox_model.dart`, `app/test/model/inbox_model_test.dart`,
`app/test/ui/inbox_test.dart`, `app/test/ui/overview_test.dart`,
`app/test/ui/sections_test.dart`

**Steps.**
1. `Ask.provenance` (`inbox_model.dart:267`): when `thread` is not null, it
   is `projectName(thread.project)`, ` › `, the thread's title, ` · `, the
   source's label, ` · ` and the scope's label. Without a thread it stays
   `provenanceOf(source, project, scope)`. The card's one line ellipsizes at
   its end, so the thread stays readable when the line is long.
2. Update the tests that find a card by its source text: `inbox_test.dart:351`
   and `:392-394`, `overview_test.dart:283`, `sections_test.dart:226`. Each
   fixture ask with a thread now reads `<project> › <title> · Fleet ·
   Everyone`; keep the assertion that no card has a blank source line.
3. Add `'a thread’s ask leads with the thread'` to `inbox_model_test.dart`.

**Verify.** `cd app && flutter test test/model test/ui/inbox_test.dart test/ui/overview_test.dart test/ui/sections_test.dart`
passes.

## Task 2: An empty list offers what fills it

**Goal.** An empty section's one action is its create, when it has one.

**Files.** `app/lib/src/ui/sections/section_page.dart`,
`app/lib/src/ui/automations/watches_tab.dart`,
`app/lib/src/ui/settings/environments_section.dart`,
`app/test/ui/sections_test.dart`

**Steps.**
1. In `SectionPage._content` (`section_page.dart:149-153`), the empty action
   is `emptyAction` when given; else, when the page has `createLabel` and
   `blank`, an action with the create's label that opens the blank, as the
   header's primary does; else `Read the section again`.
2. Watches (`watches_tab.dart:37`) passes `emptyAction` as `New watch`,
   opening the same form its header button opens. Environments
   (`environments_section.dart:40`) passes `Import devcontainer.json`, opening
   the same import.
3. Tests in `sections_test.dart`: `'an empty list with a create offers the
   create'` (a section with `createLabel` and no rows: the empty state's
   button opens the blank detail) and `'an empty list without one reads
   again'`.

**Verify.** `cd app && flutter test test/ui/sections_test.dart test/ui/automations_test.dart test/ui/library_test.dart test/ui/settings_environments_test.dart test/states_test.dart`
passes.

## Task 3: Automations has one New per tab

**Goal.** The `New automation` menu is shown only on a tab that has no
create of its own.

**Files.** `app/lib/src/ui/automations/automations_place.dart`,
`app/test/ui/automations_test.dart`

**Steps.**
1. In `_AutomationsPlaceState.build` (`automations_place.dart:87`), pass the
   `Menu` as `trailing` only while the open tab is Bots (`0`) or Recipes
   (`3`); otherwise `trailing` is null. Update the comment at `:96-98` to say
   so.
2. Tests: `'Schedules has one New'` (on Schedules, `New automation` is not
   found and `New schedule` is) and keep `'New automation starts a
   schedule'` (`:264`), which opens on Bots.

**Verify.** `cd app && flutter test test/ui/automations_test.dart test/overflow_test.dart`
passes.

## Task 4: Settings' list has the sidebar's gutter

**Goal.** Settings' section rows sit 8 in from the panel's edges.

**Files.** `app/lib/src/ui/settings/settings_place.dart`, `app/test/ui/settings_test.dart`

**Steps.**
1. Wrap the section rows (`settings_place.dart:134-139`) in horizontal
   padding of `Space.s8`, the sidebar's gutter (`app/lib/src/ui/shell/sidebar.dart`,
   its fixed rows' padding).
2. Test `'the section list has a gutter'`: the selected row's fill starts
   `Space.s8` from the list's left edge.

**Verify.** `cd app && flutter test test/ui/settings_test.dart test/overflow_test.dart`
passes.

## Task 5: A planned state keeps its pill beside its words

**Goal.** `StateMessage.planned` draws its words and its pill on one line at
the top, whatever the height it is given.

**Files.** `app/lib/src/ui/kit/state_message.dart`, `app/test/kit/state_message_test.dart`

**Steps.**
1. In the planned branch (`state_message.dart:188`), align the `Row`'s
   children to the start of the cross axis and give the inner `Column`
   `MainAxisSize.min`.
2. Test `'planned keeps its pill beside its words in a tall box'`: in a
   400 × 800 box, the pill's top is within `Space.s8` of the name's top.

**Verify.** `cd app && flutter test test/kit/state_message_test.dart test/gallery_test.dart`
passes on Linux; the gallery's planned specimen is unchanged.

## Task 6: The factory's strips and lanes line up

**Goal.** Stage words sit under their stages, lanes have an inset, and one
round is one round.

**Files.** `app/lib/src/ui/kit/stage_strip.dart`, `app/lib/src/ui/kit/lanes.dart`,
`app/lib/src/ui/factory/run_screen.dart`, `app/test/kit/stage_strip_test.dart`
(new), `app/test/kit/lanes_test.dart`, `app/test/ui/factory/run_screen_test.dart`,
`app/test/goldens/` (regenerated)

**Steps.**
1. `StageStrip` (`stage_strip.dart:48`): replace the `Wrap` of words with a
   `Row` of one `Expanded` per stage, each holding its word, separated by the
   same `Space.s4` gaps `Segments` draws (`app/lib/src/ui/kit/segments.dart:42`),
   so each word starts under its segment. The end word, when there is one,
   goes on its own line beneath, in the same style.
2. `Lanes._header` and `_row` (`lanes.dart:86, 100`): horizontal padding of
   `Space.s16`, the inset `Rows` uses in a `Box.flush`.
3. `run_screen.dart:380`: `1 round`, else `N rounds`.
4. Regenerate the goldens on Linux: `cd app && flutter test --update-goldens test/gallery_test.dart`.
5. Tests: `stage_strip_test.dart` `'each stage word starts under its
   segment'`; `lanes_test.dart` `'a row’s name is inset from the box'`;
   `run_screen_test.dart` `'one repair round is singular in its cell'`.

**Verify.** `cd app && flutter test test/kit test/ui/factory test/gallery_test.dart test/overflow_test.dart`
passes on Linux.

## Task 7: A work item reads in order

**Goal.** A decision's answer field follows its question, and the plan is
drawn as Markdown.

**Files.** `app/lib/src/ui/factory/item_asks.dart`, `app/lib/src/ui/factory/plan_tab.dart`,
`app/test/ui/factory/item_asks_test.dart`, `app/test/ui/factory/work_item_test.dart`

**Steps.**
1. In `_card` (`item_asks.dart:150`), draw the decision's `Field` after the
   `AskCard`, `Space.s8` below it, instead of before.
2. `plan_tab.dart:42`: draw the plan with `Narration(text: plan)`.
3. Tests: `'the answer field follows its decision'` in `item_asks_test.dart`;
   `'the plan is drawn as Markdown'` in `work_item_test.dart` (a plan with
   `## Task 1` shows the heading text `Task 1` and no `##`).

**Verify.** `cd app && flutter test test/ui/factory` passes.

## Task 8: The planner's question is the card's title

**Goal.** A planning question card's title is the question; its body is
empty.

**Files.** `app/lib/src/model/planning_model.dart`, `app/test/model/planning_model_test.dart`

**Steps.**
1. Where a request is recorded (`planning_model.dart:559-562`), a question's
   `title` is its question line when `_questionLine(payload)` is not empty,
   else `Question from the planner`, and its `detail` is empty when the
   title took the question. A permission keeps `Permission requested` and
   its command as detail.
2. Update the `PlannerRequest.title` doc comment (`:74`).
3. Test `'a planner’s question is its card’s title'`.

**Verify.** `cd app && flutter test test/model/planning_model_test.dart test/ui/factory/planning_test.dart`
passes.

## Task 9: Earlier threads open from the sidebar

**Goal.** `Earlier · N` is a disclosure row: it shows the project's earlier
threads beneath it, each opening its thread, and it is open whenever one of
them is the open thread.

**Files.** `docs/studio-flutter/design-system.md`, `app/lib/src/model/nav_model.dart`,
`app/lib/src/ui/shell/sidebar.dart`, `app/test/support/screens.dart`,
`app/test/model/nav_model_test.dart`, `app/test/ui/shell_test.dart`

**Steps.**
1. `NavProject` becomes as Contracts declares: `earlier` is a getter over
   the new `earlierThreads`. In `_ProjectBuilder.build`
   (`nav_model.dart:228`), collect the threads `isEarlier` keeps out of
   `current`, sorted as `current` is (most recently active, then id), and
   pass them as `earlierThreads`. Update `NavProject.toString` (`:86`).
2. `Sidebar._tree` (`sidebar.dart:161`): under a chosen host, filter
   `earlierThreads` to that host as `current` is, and keep a project when
   either list has one of the host's threads.
3. Replace the `Earlier · N` text (`sidebar.dart:207-218`) with a `NavRow`:
   label `Earlier`, `count` the number of earlier threads, `indent: true`,
   `expanded` whether the group is open, and both `onToggle` and `onTap`
   toggling it. The sidebar's state holds the open groups as a set of
   project ids, like `_closed` (`sidebar.dart:63`), and starts empty. A
   group is also drawn open while one of its threads is the open place
   (`_isOpen`, `:223`), so the open row is always visible.
4. When the group is open, draw each earlier thread beneath it as the
   current threads are drawn — a `ThreadRow` that opens its thread, marked
   selected when it is open — with left padding `Space.s32`, one step deeper
   than the current threads, since they sit under the `Earlier` row.
5. design-system.md, the F2 `NavRow` paragraph: a project's `Earlier` row
   is a `NavRow` with a disclosure chevron and the count of the threads it
   holds, and the threads it opens are `ThreadRow`s.
6. `screens.dart:250-263`: the shell fixture's project replaces `earlier: 12`
   with two earlier threads made by `_shellThread`, idle, so the shell
   screen still draws the row.
7. Tests:
   - `nav_model_test.dart`: in `'earlier counts idle agents older than a
     day'` (`:136`) also assert the two threads' ids, most recent first; in
     `'a closed session counts toward earlier'` (`:340`) assert the closed
     session is in `earlierThreads`.
   - `shell_test.dart`: `'Earlier opens its threads and a thread opens'`
     (tap `Earlier`, then an earlier thread's title: the place is that
     thread); `'an open earlier thread shows its group open'` (a `Studio`
     opened on an earlier thread draws its row without a tap);
     `'a chosen host keeps its own earlier threads'` (with two hosts, the
     count and the rows are only the chosen host's, and a project whose
     only threads there are earlier is still listed).

**Verify.** `cd app && flutter test test/model/nav_model_test.dart test/ui/shell_test.dart test/tooltips_test.dart test/overflow_test.dart test/states_test.dart`
passes.

## Final verification

1. `cd app && dart format --set-exit-if-changed lib test && flutter analyze`.
2. `cd app && flutter test` on Linux, goldens included.

## Integration notes

- No Go or contract change.
- Task 1 changes text other tests find by; step 2 lists every one found by
  `grep -rn "Everyone'" app/test` at `958e2ec9`.
- Tasks 5 and 6 change kit components without changing their API, so
  design-system.md needs no edit; task 6's golden regeneration covers both.
