# Studio controls that do nothing

A plan for Studio controls and settings that look like they work and don't.
Found by a code walk and by rendering every screen registered in
`app/test/support/screens.dart`, after the [visual fixes](2026-11-18-studio-visual-fixes-plan.md).
The **Findings** section is the spec.

- **Authored against**: `origin/main` at `958e2ec9`.
- **Runs after**: the visual fixes plan, because both edit `transcript.dart`,
  `thread_screen.dart` and `shell.dart`.
- **Executed as**: one commit per task, `studio-dead-controls: task N — <title>`.
- **Verification runs on Linux**: `cd app && flutter test …`. One check
  needs a browser, under **Needs a browser**.

## Findings

1. **Reply to a question does nothing outside the Inbox.** The Inbox opens a
   field for **Reply** and collects one answer per question for a
   multi-question ask (`app/lib/src/ui/inbox/inbox.dart:342-490`). The
   thread's pinned ask (`app/lib/src/ui/thread/transcript.dart:330-372`) and
   the Overview's card (`app/lib/src/ui/overview/overview.dart:292-299`)
   post the action alone. `InboxModel.answer` then builds an empty answer
   and returns without posting anything (`app/lib/src/model/inbox_model.dart:435-438, 513-516`).
   So **Reply** is dead in a thread and on the Overview, and so is
   **Answer** on a multi-question ask in a thread. A failed answer is also
   invisible in a thread: only the Inbox draws `ask.error` and **Try again**
   (`inbox.dart:493-520`).
2. **The composer's mode chip always says Ask.** Production never calls
   `ComposerModel.refreshAccess` (`app/lib/src/model/composer_model.dart:180`);
   the shell builds the composer and only sets whether it is running
   (`app/lib/src/ui/shell/shell.dart:1467-1471`). The chip shows the
   constructor's default, `'Ask'` (`composer_model.dart:30`), whatever the
   session holds. The rule waiting in `refreshAccess` is also wrong: it
   makes an `ask`-mode session read-only, but `ask` is plan and default mode
   (`internal/acp/interaction.go:124-125`), and those can still be prompted.
3. **Appearance only reaches Settings, and garbles it.** The root is
   `EmberApp(home: …)` with no brightness (`app/lib/main.dart:135`), so the
   window follows the system. Only `SettingsPlace` wraps itself in
   `EmberTheme(brightness: preferences.brightness)`
   (`app/lib/src/ui/settings/settings_place.dart:82-85`). When the choice
   differs from the system, Settings draws one theme's text on the other's
   background, and the rest of the Studio ignores the choice. This is
   reproducible: rendered under a dark root with a light system, Settings'
   headings are near-invisible.
4. **Density in Settings › General does nothing.** Every thread builds
   `ThreadViewState()` at its default (`shell.dart:1449`). `preferences.density`
   is read only by Settings.
5. **The address never follows navigation.** The page's URL is read once
   (`main.dart:156`, `shell.dart:761-771`) and never written again: `_go`
   (`shell.dart:905`) only sets state. So a reload returns to wherever the
   page was first opened, Back leaves the Studio, and a thread's link cannot
   be copied from the address bar. `Place` documents the opposite
   (`app/lib/src/model/place.dart:28-30`).
6. **Step numbers count rows, not steps.** `_Row` numbers a step
   `'${index + 1}'`, the row's index in the flattened list
   (`transcript.dart:436`). With the prompt above it, the first step reads
   **2**, and a step after a tool row and some narration reads **11**.
7. **A New button can be swallowed by the keyboard rule.** The sidebar's plus,
   the narrow app bar's New and a thread's own New all call
   `_newWorkFromKeys` (`shell.dart:855, 1099`). It returns early while any
   text field has focus (`shell.dart:1270-1271`). That guard exists for
   `⌘N`. A pointer tap on `Press` does not take focus
   (`app/lib/src/ui/kit/press.dart:115-119`), so a click can arrive while the
   composer still holds the keyboard.
8. **Spend is always drawn as a fault.** The Overview's spend is
   `Meter.fault` unconditionally (`overview.dart:502`), so $0.10 of a $10 cap
   is a red bar. design-system §5: a meter is `muted` unless it reports a
   fault.
9. **Shipping promises a phase that has shipped.** The ship sheet's pull
   request outcome says "Its checks and reviews arrive in F5"
   (`app/lib/src/ui/inspector/ship_sheet.dart:200-203`).

## Non-goals

- Switching mode from the composer. The modes are being reworked (three
  permissive modes and a denylist), so this plan makes the chip truthful and
  leaves it inert.
- Keeping Appearance and Density across reloads. General says the choices
  last as long as the app runs (`app/lib/src/model/general_model.dart:12-16`).
- A Settings section in the address. Settings' open section stays local
  state.

## Contracts

```dart
// app/lib/src/ui/inbox/answerable_ask.dart (new)

/// Posts an answer: the action, with the reply's words or one answer per
/// question when the ask needs them. The signature of InboxModel.answer.
typedef AnswerAsk = void Function(
  model.Ask ask,
  model.AskAction action, {
  String? reply,
  List<String>? answers,
});

/// One ask a person can answer where it is drawn: the AskCard, a reply
/// field, one field per question of a multi-question ask, and the failure
/// with Try again. The Inbox, the Overview and a thread's pinned ask all
/// draw this.
class AnswerableAsk extends StatefulWidget {
  const AnswerableAsk({
    super.key,
    required this.ask,
    required this.placement,
    required this.now,
    this.onAnswer,
    this.onOpenSource,
    this.onOpenInbox,
  });

  final model.Ask ask;
  final AskPlacement placement;
  final DateTime now;
  final AnswerAsk? onAnswer;
  final VoidCallback? onOpenSource;

  /// The Overview's way out for an ask it has no room to collect: a
  /// multi-question ask offers Open inbox instead of its fields.
  final VoidCallback? onOpenInbox;
}
```

```dart
// app/lib/src/ui/shell/shell.dart — Studio and _ThreadPlace gain
//   final Density density;   // what a thread opens at; Density.comfortable by default
```

## Task 1: A question is answered wherever its card is

**Goal.** Reply, Answer, sending and a failed answer behave the same in the
Inbox, on the Overview and in a thread.

**Files.** `app/lib/src/ui/inbox/answerable_ask.dart` (new),
`app/lib/src/ui/inbox/inbox.dart`, `app/lib/src/ui/overview/overview.dart`,
`app/lib/src/ui/thread/transcript.dart`, `app/lib/src/ui/thread/thread_screen.dart`,
`app/lib/src/ui/shell/shell.dart`, `app/test/ui/inbox/answerable_ask_test.dart`
(new), `app/test/ui/thread/asks_in_place_test.dart`, `app/test/ui/overview_test.dart`

**Steps.**
1. Create `AnswerableAsk` (Contracts) by moving the Inbox's per-card logic out
   of `_InboxState`: `_card` (`inbox.dart:293`), `_questionFields`, `_tap`,
   `_answerHint`, `_complete`, `_submit`, `_choose`, `_replyField`,
   `_sendReply`, `_failure` and `_retry`, with the controllers and the last
   action now held per card. Its source line, waiting words and
   `answeredInThread` rule stay as the Inbox draws them. For
   `AskPlacement.overview`, a multi-question ask offers one action,
   `Open inbox`, calling `onOpenInbox`, as `overview.dart:279-290` does
   today. A work item's ask (`answeredInThread`) keeps the Overview's
   `Open in the factory` (`overview.dart:266-278`) and the Inbox's empty
   action row.
2. The Inbox draws `AnswerableAsk(placement: AskPlacement.inbox)` per ask,
   keyed by `ValueKey('${ask.host}|${ask.id}')`, and drops what step 1 moved.
   The Overview draws it with `AskPlacement.overview`; the thread's
   `_PinnedAsk` draws it with `AskPlacement.thread` and keeps its padding.
3. Widen `onAnswer` to `AnswerAsk` on `Transcript` (`transcript.dart:89`),
   `_PinnedAsk` (`:337`), `ThreadScreen`, `_ThreadPlace` (`shell.dart:1436`)
   and the Overview. The shell already passes `widget.inbox.answer`, which
   has this signature.
4. Tests:
   - `answerable_ask_test.dart`: `'Reply opens a field and posts its words'`,
     `'a multi-question ask posts one answer per question'`,
     `'a failed answer shows why and tries the same answer again'`, and
     `'the Overview sends a multi-question ask to the Inbox'`.
   - `asks_in_place_test.dart`: `'Reply in a thread posts the words typed'`
     — the recorded call carries `reply`.
   - `overview_test.dart`: `'Reply on the Overview posts the words typed'`.

**Verify.** `cd app && flutter test test/ui/inbox test/ui/inbox_test.dart test/ui/overview_test.dart test/ui/thread test/states_test.dart test/tooltips_test.dart`
passes.

## Task 2: The mode chip says what the session holds

**Goal.** The composer's chip reads Ask, Act or `Act · Auto` from the
session, and mode never makes a composer read-only.

**Files.** `app/lib/src/model/composer_model.dart`, `app/lib/src/ui/shell/shell.dart`,
`app/test/model/composer_model_test.dart`, `app/test/ui/shell/keys_test.dart`

**Steps.**
1. `refreshAccess` (`composer_model.dart:180`) sets `_permissionMode` to
   `Ask` for `ask`, `Act` for `act`, and `Act · Auto` when `auto` is also
   set. It no longer touches `_readOnly` or `_readOnlyReason`. Rewrite its
   doc comment to say why: plan and default mode can still be prompted.
2. `_ThreadPlace.initState` (`shell.dart:1460`) starts
   `_composer?.refreshAccess()` without awaiting it. `didUpdateWidget`
   (`:1474`) does the same when the thread's status changes, because a turn
   that ends is when a mode change commits.
3. Tests in `composer_model_test.dart`: replace `'a session in ask mode is
   read-only, with its reason'` (`:162`) with `'a session in ask mode can
   still be prompted'`; add `'the chip names act with auto'`. In
   `keys_test.dart`, or the shell test that opens a thread with a fake
   client, add `'opening a thread reads its access'` — the fake records one
   `GET` of the access route.

**Verify.** `cd app && flutter test test/model/composer_model_test.dart test/ui/shell test/ui/thread/composer_test.dart`
passes.

## Task 3: Appearance and density reach the whole window

**Goal.** Settings › General's Appearance colours every screen, and its
Density is what a thread opens at.

**Files.** `app/lib/main.dart`, `app/lib/src/ui/settings/settings_place.dart`,
`app/lib/src/ui/shell/shell.dart`, `app/test/ui/settings_test.dart`,
`app/test/ui/shell_test.dart`

**Steps.**
1. In `main.dart:134-138`, move the `AnimatedBuilder` over `preferences`
   outside `EmberApp`, so the root is rebuilt with
   `EmberApp(brightness: preferences.brightness, home: Studio(…))`. Pass
   `density: preferences.density` to `Studio`.
2. Remove `SettingsPlace`'s own `EmberTheme` (`settings_place.dart:82-85`),
   keeping its `AnimatedBuilder` only if something else still needs it, and
   its `preferences` field if General still reads it from there.
3. `Studio` gains `density` (Contracts) and passes it to each
   `_ThreadPlace`, whose `_view` (`shell.dart:1449`) starts at it. A thread's
   own density switch still overrides it for that thread.
4. Tests:
   - `settings_test.dart`: `'Settings follows the window, not the system'` —
     an `EmberApp` with a dark brightness on a light system: a section
     heading's colour is the dark `fg`.
   - `shell_test.dart`: `'a thread opens at the chosen density'` — a
     `Studio` with `density: Density.full` opens a thread whose view state
     is `full`.

**Verify.** `cd app && flutter test test/ui/settings_test.dart test/ui/shell_test.dart test/ui/thread test/contrast_test.dart`
passes.

## Task 4: The address follows the place

**Goal.** On the web, each place the person opens is the page's address, so
reload, Back, Forward and a copied link all land on it.

**Files.** `app/lib/src/ui/shell/shell.dart`, `app/test/ui/shell/address_test.dart` (new)

**Steps.**
1. Only when the shell was given a `startPage` (the web build): in
   `initState`, call `SystemNavigator.selectMultiEntryHistory()` before
   `_followPage`, and register the state as a `WidgetsBindingObserver`
   (removed in `dispose`).
2. `_go` (`shell.dart:905`): after setting the place, when it differs from
   the last one written, call `SystemNavigator.routeInformationUpdated(uri:
   Uri.parse(formatPlace(next)))`, a push, not a replace.
3. Override `didPushRouteInformation`: take the path of the delivered
   `RouteInformation`, read it with `startPlace(path: …)`
   (`app/lib/src/model/route_translation.dart:51`), and set the place
   without writing the address again. When `_unsaved` reports unsaved input,
   ask through the same guard a click uses; a refused move writes the
   current place back with a replace. Return true.
4. Tests in `address_test.dart`, intercepting `SystemChannels.navigation`
   on the test binding's messenger:
   - `'opening a place writes its address'`: selecting Inbox records a
     `routeInformationUpdated` naming the Inbox's path, not a replace.
   - `'Back opens the place the address names'`: a pushed route for a
     thread's path opens that thread.
   - `'a handed place writes nothing'`: a `Studio` with no `startPage` (a
     test, the desktop) makes no navigation call.

**Verify.** `cd app && flutter test test/ui/shell test/ui/shell_test.dart` passes.

**Needs a browser.** Build the web Studio
(`cd app && flutter build web --base-href / --no-web-resources-cdn --output ../web/bridge/static`),
run it, open a thread, reload, then go Back and Forward: each lands where
the address says.

## Task 5: A step's number is its place among the steps

**Goal.** Steps are numbered 1, 2, 3 within their turn.

**Files.** `app/lib/src/ui/thread/transcript.dart`,
`app/test/ui/thread/transcript_test.dart`

**Steps.**
1. Add `int stepOrdinal(StackStore store, String nodeId)` beside
   `flattenRows` (`transcript.dart:36`): the 1-based position of `nodeId`
   among its parent's children whose kind is `step`.
2. `_Row` passes `number: '${stepOrdinal(store, nodeId)}'` (`:436`) and no
   longer needs `index` for it. Keep `index` only if something else reads
   it; otherwise remove the field.
3. Test `'steps are numbered among steps, not rows'`: a turn of a user
   message, narration, a step, a tool, narration and a step draws step
   numbers `1` and `2`.

**Verify.** `cd app && flutter test test/ui/thread` passes.

## Task 6: A New button always opens New

**Goal.** Only `⌘N` defers to a focused text field; every New button opens
the dialog.

**Files.** `app/lib/src/ui/shell/shell.dart`, `app/test/ui/shell/keys_test.dart`

**Steps.**
1. Split `_newWorkFromKeys` (`shell.dart:1270`) into `_newWork()`, which
   resolves the project and opens the dialog, and the key path, which returns
   while `_editingText()` and otherwise calls `_newWork()`.
2. The sidebar's and narrow app bar's `onNew` (`:855`) and the thread's
   `onNewWork` (`:1099`) call `_newWork`. `NewWorkIntent` (`:863-867`)
   keeps the key path. Correct the comment at `:1095-1098`.
3. Tests in `keys_test.dart`: `'New in the sidebar opens with the composer
   focused'` (focus the composer's field, tap the sidebar's plus: the
   dialog opens) and keep the existing test that `⌘N` typed in a field does
   nothing.

**Verify.** `cd app && flutter test test/ui/shell test/ui/shell_test.dart test/ui/new_work`
passes.

## Task 7: Spend is red only when it holds work back; shipping says where checks are

**Goal.** Two wordings and one colour that state something false stop doing
so.

**Files.** `app/lib/src/ui/overview/overview.dart`,
`app/lib/src/ui/inspector/ship_sheet.dart`, `app/test/ui/overview_test.dart`,
`app/test/ui/inspector/ship_test.dart`

**Steps.**
1. `overview.dart:502`: draw `Meter.fault` when `spend.blocked` or
   `spend.fraction >= 1`, else `Meter`, with the same label, value, fraction
   and tooltip.
2. `ship_sheet.dart:202`: the pull request outcome's line is `Its checks run
   on the forge; Pull requests shows them as they finish.`
3. Tests: `'spend under its cap is not a fault'` and `'spend that blocks work
   is a fault'` in `overview_test.dart`; `'an opened pull request says where
   its checks show'` in `ship_test.dart`.

**Verify.** `cd app && flutter test test/ui/overview_test.dart test/ui/inspector`
passes.

## Final verification

1. `cd app && dart format --set-exit-if-changed lib test && flutter analyze`.
2. `cd app && flutter test` on Linux, goldens included.
3. The browser check under task 4, then a thread with a waiting question:
   **Reply** opens a field in the thread and on the Overview, and the
   answer reaches the agent.

## Integration notes

- No Go and no contract change.
- Task 1 moves code; the Inbox's existing tests keep passing unchanged.
- Task 2 leaves `ComposerModel.selectMode` in place, unused in production,
  for the mode rework to wire.
