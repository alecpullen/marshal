# F6 plan 1: work items — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: branch `f0-foundations` at `52221495`. Runs after
[the F4 plan](2026-11-17-f4-inspector-plan.md) and
[F5 plan 2](2026-11-17-f5-2-pulls-app-plan.md). Followed by
[F6 plan 2](2026-11-17-f6-2-planning-runs-project-plan.md).

Approved design: [f6-factory.md](../f6-factory.md), decisions 1–7, 13, 14.
Rules: [design-system.md](../design-system.md).

**How this plan is written.** No implementation code; Contracts holds
declarations only. **The factory is under active development: before Task
1, re-read `web/bridge/factory_types.go` and `factory_controller.go` at the
current `main` and correct any name below that has changed.** The mock's
work-item screens are illustrative; build what the contract describes.

## Goal

- A factory work item is a thread: stage strip, activity, asks, and an
  inspector with Plan, Agents, Checks, Changes and Pull requests.
- The user approves, decides, recovers, completes, stops and steers from
  that thread.

## Non-goals

- Planning, handoff, drafts, plan runs, the project page (plan 2).
- Changing factory behaviour.

## Assumptions

- `FactoryItem` (`web/bridge/factory_types.go`) carries `State`, `Stage`,
  `Draft`, `Approvals`, `Proposals`, `Grants`, `Rounds`, `Workers`, `PRs`,
  `Decision`, `History`, `Completion` and `Version`.
- States seen in the controller: draft, queued, preparing, implementing,
  reviewing, checking, repairing, needs_input, recovering, ready, stopping,
  stopped, closed.
- `FactoryCommand` has `Kind`, `RequestID`, `ExpectedVersion`,
  `DecisionID`, `Answer`, `Reason`, `AdditionalRounds`, the two routings
  and `Revision`. Kinds include `approve`, `answer`, `retry`, `recover`,
  `stop`, `complete`, `merge_and_complete`, `reopen`, `link_pr`,
  `refresh_pr_evidence` and the ten `revision_*` kinds.
- Routes: `GET`/`POST /api/factory/items`, `GET /api/factory/items/{id}`,
  `POST /api/factory/items/{id}/commands`.
- The Svelte logic to port: `lib/factory.ts`, `lib/factoryStore.ts`,
  `lib/studio/model.ts`, `lib/factory/`, `lib/FactoryRevision.svelte`.

## Decisions this plan makes beyond the spec

- The factory types move to `web/bridge/contract/factory.go` by declaring
  them there and aliasing the bridge's names to them, one file at a time,
  as `registry.go` aliases `Decision`. No field or JSON key changes.
- Stage for the strip: Plan for draft, queued, preparing; Implement for
  implementing, repairing; Review for reviewing, checking; Ready for ready.
  needs_input and recovering keep the stage of the item's last round.
  Stopping, stopped and closed are words beside the strip.
- A command is sent with the item's `Version` as `ExpectedVersion` and a
  fresh `RequestID`; a version conflict reloads the item and shows "This
  changed while you were deciding" with the action still available.
- The factory store polls every 5 s while any item is not closed or
  stopped, and every 60 s otherwise; it is the only poller.

## Execution

- Branch `f6-1-work-items`. Commit each task as
  `f6-1-work-items: task N — <title>`, explicit paths only.
- Regenerate both clients after any contract change.

## Contracts

```dart
// additions to BridgeClient
Future<List<FactoryItem>> factoryItems();
Future<FactoryItem> factoryItem(String id);
Future<FactoryItem> factoryCommand(String id, FactoryCommand command);

// model/factory_store.dart — the only reader and poller of items
class FactoryStore extends ChangeNotifier {
  List<FactoryItem> items({String? host, String? projectRoot});
  FactoryItem? byId(String host, String id);
  Set<String> get workerAgentIds;          // every worker of every item, ever
  Future<FactoryItem> command(String host, String id, FactoryCommand c);
}

// model/work_item_model.dart
enum Stage { plan, implement, review, ready }
enum ItemFilter { current, needsYou, ready, history }
Stage stageOf(FactoryItem item);   String? endStateWord(FactoryItem item);
ThreadStatus statusOfItem(FactoryItem item);
bool matches(FactoryItem item, ItemFilter filter);
ThreadRef threadOfItem(String host, FactoryItem item);
class ActivityEntry { String who; String text; String? detail; DateTime at; }
List<ActivityEntry> activityOf(FactoryItem item);
List<Ask> asksOf(String host, FactoryItem item);  // draft, decision, recovery, completion
```

`AskKind` gains `approval_draft`, `decision`, `recovery`, `completion`.
`InspectorTab` gains `plan`, `agents`, `checks`, `pulls`; the tab set is
chosen by `ThreadKind`.

## Task 1 — factory types into the contract

**Files:** `web/bridge/contract/factory.go` (new),
`web/bridge/factory_types.go`, `factory_revision_types.go`,
`factory_completion_types.go`, `test/contract/factory.json`,
`web/ui/src/lib/factory.ts`, both generated files.

1. Declare in the contract every type `GET /api/factory/items/{id}` returns
   and `FactoryCommand` with its payloads; alias the bridge's names.
2. Record `factory.json` from the fixture bridge: one item in each of
   draft, needs_input with a decision, ready, and closed with a
   completion.
3. `factory.ts` re-exports the generated types and keeps its functions.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd web/ui && npm
run check && npm test`; `cd app && flutter test test/client/`.

## Task 2 — store and model

**Files:** `app/lib/src/model/factory_store.dart`, `work_item_model.dart`,
`thread_ref.dart`, `nav_model.dart`, `inbox_model.dart`, tests.

1. Implement Contracts. `activityOf` turns `History` into entries naming
   the planner, implementer, reviewer or the user in words.
2. `buildNavModel` lists work items under their project and omits any agent
   in `workerAgentIds`.
3. `InboxModel` includes `asksOf` for every item.
4. Tests: `stage and status for every state` (table),
   `an unknown state is shown raw under Plan`, `filters` (ported from
   `studio/model.test.ts`), `a worker is not an independent agent`,
   `a historical worker is not an independent agent`,
   `one ask per pending draft, decision, recovery, completion`,
   `a version conflict reloads`.

**Verify:** `cd app && flutter test test/model/`.

## Task 3 — the work-item thread, read-only

**Files:** `docs/studio-flutter/design-system.md`,
`app/lib/src/ui/kit/stage_strip.dart`, `activity_row.dart`, gallery,
`app/lib/src/ui/factory/work_item_screen.dart`, `plan_tab.dart`,
`agents_tab.dart`, `checks_tab.dart`, `pulls_tab.dart`, tests,
`screens.dart`.

1. Add `StageStrip` and `ActivityRow` to the kit by §10.
2. The centre: asks pinned at the top, a ready or failing pull request as
   in F5, then the activity. The inspector: Plan (goal, scope, the approved
   revision's settings in words, documents), Agents (each `FactoryWorker`
   with role and state), Checks (observed CI for the current head and
   review results), Changes (F4's tab on the item's branch), Pull requests
   (`PrCard` per `PullsStore.forThread`).
3. Register with all five states.
4. Tests: `strip shows the stage`, `end state is a word, not a stage`,
   `activity names who acted`, one per tab.

**Verify:** `cd app && flutter test`.

## Task 4 — asks and commands

**Files:** `app/lib/src/ui/factory/item_asks.dart`, `recovery_form.dart`,
`work_item_screen.dart`, tests.

1. Draft approval: the plan's summary and Approve and start. Decision: each
   option with its consequence, and a free answer. Recovery: the diagnosis
   and previous attempts, with the model, continue or fresh, and extra
   rounds inline. Completion: the assessment with Complete, Merge and
   complete, Reopen.
2. Stop is in the header's overflow `Menu` and confirms.
3. Every action sends one `FactoryCommand` through the store and shows
   pending until the item changes.
4. Tests: one per ask kind posting the right `Kind` and fields;
   `recovery sends routing and rounds`, `stop confirms`,
   `a conflict keeps the action available`.

**Verify:** `cd app && flutter test`.

## Task 5 — agent drill-in, steering, revisions

**Files:** `agents_tab.dart`, `work_item_screen.dart`,
`app/lib/src/ui/factory/revision_sheet.dart`,
`app/lib/src/model/revision_model.dart`, tests.

1. Opening a worker swaps the centre for F3's thread screen on that
   worker's session, with Back to activity.
2. The composer names its recipient and defaults to the planner when the
   item has one; an undeliverable message is shown queued with Edit and
   Cancel.
3. "Propose a change" on the Plan tab opens a `Sheet` showing proposed
   against approved, and sends `revision_propose`; the model drives the
   remaining `revision_*` kinds as `FactoryRevision.svelte` does.
4. Tests: `drill-in shows the worker's transcript`, `back returns`,
   `recipient defaults to the planner`, `a queued message can be
   cancelled`, `propose sends the revision`.

**Verify:** `cd app && flutter test`.

## Task 6 — end to end, record, full verification

**Files:** `app/integration_test/f6_1_test.dart`, the spec.

1. On the fixture bridge: a draft item is approved, raises a decision that
   is answered, repairs once and reaches ready.
2. Implementation record.

**Verify:** `cd app && flutter analyze && flutter test && flutter test
integration_test -d linux && dart format --output=none --set-exit-if-changed
lib test`; `CGO_ENABLED=1 go test ./web/... -count=1`.

## Done when

- A work item is driven from draft to ready in its thread, with one
  decision and one repair, and no other page.
- No factory worker appears as an independent agent.

## What later plans rely on

- `FactoryStore`, `work_item_model.dart`, `StageStrip`, and the tab set by
  thread kind.
