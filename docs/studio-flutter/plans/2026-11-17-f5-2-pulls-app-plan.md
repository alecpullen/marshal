# F5 plan 2: pull requests and CI in the app — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: branch `f0-foundations` at `52221495`. Runs after
[F5 plan 1](2026-11-17-f5-1-pulls-bridge-plan.md) and
[the F4 plan](2026-11-17-f4-inspector-plan.md). Re-check the names they
declare before starting.

Approved design: [f5-pull-requests-and-ci.md](../f5-pull-requests-and-ci.md),
decisions 10–15. Rules: [design-system.md](../design-system.md).

**How this plan is written.** No implementation code; Contracts holds
declarations only. The mock's `pulls.dart` is the visual reference.

## Goal

- The Pull requests place; pull requests on Overview, in the Inbox and at
  the head of their thread.
- Fix with a worker in one click.

## Non-goals

- Merge by rule. A CI dashboard.

## Assumptions

- The kit already has `PrCard` and `CheckStrip` with states built in F0;
  check their constructors in `app/lib/src/ui/kit/pr_card.dart` and extend
  them here only through design-system.md §10.
- F2's `FleetStore`, `InboxModel`, `OverviewModel`, `NavModel`; F3's thread
  screen; F4's `ChangesModel` and `InspectorState`.

## Decisions this plan makes beyond the spec

- "Send back" on a ready pull request steers the thread's own agent with
  the user's text when that agent is alive; otherwise the button is
  disabled with a tooltip saying the agent has ended.
- Merge posts the agent's existing merge route for an agent thread; for a
  factory item it posts the factory command of kind `merge_and_complete`
  (`FactoryCommand` in `web/bridge/factory_types.go`). Both confirm first,
  naming the pull request and its base branch.

## Execution

- Branch `f5-2-pulls-app`. Commit each task as
  `f5-2-pulls-app: task N — <title>`, explicit paths only.

## Contracts

```dart
// additions to BridgeClient
Future<List<PullRecord>> pulls();                         // GET /api/pulls
Future<PullRecord> pull(String id);                       // GET /api/pulls/{id}
Future<PullRecord> fixPull(String id, PullFixRequest r);  // POST /api/pulls/{id}/fix
Future<void> stopPullFix(String id);                      // POST /api/pulls/{id}/fix/stop

// model/pulls_store.dart
class PullsStore extends ChangeNotifier {
  PullsStore(HostRegistry hosts, FleetStore fleet);
  List<PullRecord> all({String? host});
  PullRecord? byId(String host, String id);
  List<PullRecord> forThread(ThreadRef thread);
}

// model/pulls_model.dart
enum PullGroup { ready, failing, beingFixed, inProgress, unavailable }
class PullView { PullRecord record; ThreadRef? thread; PullGroup group;
  String checkSummary; bool sending; String? error; }
class PullsModel extends ChangeNotifier {
  Map<PullGroup, List<PullView>> get groups;   // in PullGroup order
  int get readyCount;
  Future<void> fix(PullView pull, {String mode = 'fix', String? note});
  Future<void> stopFix(PullView pull);
  Future<void> setAutoFix(String projectRoot, bool on);
}
```

`AskKind` gains `pull`.

## Task 1 — client, store and model

**Files:** `app/lib/src/client/bridge_client.dart`,
`app/lib/src/model/pulls_store.dart`, `pulls_model.dart`,
`inbox_model.dart`, tests under `app/test/model/`.

1. The store loads the index per host and applies `pull` deltas.
2. `PullsModel` groups by `PullState`, builds the one-line check summary
   ("9 of 9 checks passed", "1 failing: <name>", "<n> of <m> passed, rest
   running", "Checks unavailable: <why>"), and links each record to its
   thread.
3. `fix` sets `sending`, posts, and on failure keeps the view with the
   bridge's message. A 409 shows the running fix, not an error.
4. `InboxModel` maps inbox items of kind `pull` to asks with actions Open,
   Try again and Dismiss.
5. Tests: `groups follow state`, `summary for each state`,
   `delta updates a record`, `a moved head shows no old checks`,
   `fix posts the failing check`, `a refused fix shows the running one`,
   `a failed outcome becomes an ask`.

**Verify:** `cd app && flutter test test/model/`.

## Task 2 — the card and the fix control

**Files:** `docs/studio-flutter/design-system.md`,
`app/lib/src/ui/kit/pr_card.dart`, `app/lib/src/ui/pulls/fix_control.dart`,
gallery, `app/test/kit/pr_card_test.dart`,
`app/test/ui/pulls/fix_dispatch_test.dart`, goldens.

1. Bring `PrCard` to the spec's five states: ready, failing (failing check
   name and log tail), being fixed ("A worker is on it", Watch, Stop), in
   progress, checks unavailable (the reason). Update design-system.md §5 if
   a variant is added.
2. `fix_control.dart`: the primary "Fix with a worker" button and a caret
   `Menu` with exactly Investigate only, Fix with a note, Choose the
   worker, Always fix failures automatically in this project. Each item has
   one line of explanation. Fix with a note opens a `Sheet` with one
   `Field`.
3. Tests: one per card state; `one click dispatches with no further
   input`, `the card shows sending then being fixed`, `stop clears it`,
   `the caret has exactly four items`,
   `auto-fix changes the project setting`.

**Verify:** `cd app && flutter test`.

## Task 3 — the Pull requests place

**Files:** `app/lib/src/ui/pulls/pulls_place.dart`,
`app/lib/src/ui/shell/placeholder.dart`, `app/lib/src/model/nav_model.dart`,
`app/test/ui/pulls/pulls_place_test.dart`, `screens.dart`.

1. Groups in `PullGroup` order, each under a `Label` with its count; empty
   groups are omitted.
2. The sidebar row is no longer planned and carries `readyCount`.
3. Register with all five states.
4. Tests: `groups in order`, `empty groups are omitted`,
   `sidebar count is the ready count`.

**Verify:** `cd app && flutter test`.

## Task 4 — review head, Overview, Inbox, Changes footer

**Files:** `app/lib/src/ui/thread/review_head.dart`, `thread_screen.dart`,
`app/lib/src/ui/overview/overview.dart`, `app/lib/src/ui/inbox/inbox.dart`,
`app/lib/src/ui/inspector/changes_tab.dart`, tests.

1. A thread with a ready pull request opens with the review head: title and
   head, each check by name, reviews, the report when present, then Review
   changes, Merge, Open on forge, Send back. Review changes expands the
   inspector on Changes. A thread with a failing pull request opens with
   that `PrCard`.
2. Overview shows ready and failing pull requests under Needs you, with a
   link to the place.
3. The Inbox gains a Ready filter listing ready pull requests.
4. The Changes footer shows the pull request's state once shipped.
5. Tests: `ready thread opens with the review head`,
   `review changes expands the inspector`,
   `failing thread opens with the card`,
   `overview lists ready and failing`, `inbox ready filter`,
   `send back steers the agent`, `send back is disabled when it has ended`.

**Verify:** `cd app && flutter test`.

## Task 5 — end to end, record, full verification

**Files:** `app/integration_test/f5_test.dart`, the spec.

1. On the fixture bridge and forge: an agent ships a pull request whose
   check fails; the app dispatches a fix; the fix is pushed; the check
   passes; the card reaches ready.
2. Implementation record.

**Verify:** `cd app && flutter analyze && flutter test && flutter test
integration_test -d linux && dart format --output=none --set-exit-if-changed
lib test`.

## Done when

- A failing check is dispatched in one click and the user is asked nothing.
- The card moves from failing to being fixed to in progress to ready
  without a refresh.
- Checks for an older head are never shown.

## What later plans rely on

- `PullsStore.forThread`, used by work-item threads in F6.
