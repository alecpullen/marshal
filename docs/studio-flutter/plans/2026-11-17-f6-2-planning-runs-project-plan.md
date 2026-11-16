# F6 plan 2: planning, drafts, plan runs and the project page — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: `main` at `11957d83`, which contains F0. Runs after
[F6 plan 1](2026-11-17-f6-1-work-items-plan.md).

Approved design: [f6-factory.md](../f6-factory.md), decisions 8–12. Rules:
[design-system.md](../design-system.md).

**How this plan is written.** No implementation code; Contracts holds
declarations only. Re-read the planner, working-draft, run and
project-settings handlers at the current `main` before Task 1.

## Goal

- "Plan first" opens a planning thread that hands off to the factory
  through one review sheet; "Work item" opens that sheet directly.
- Unfinished handoffs autosave and never overwrite silently.
- A plan run is a thread with lanes, graph and timeline.
- Each project has a page: board, defaults, health.

## Non-goals

- The workflow editor, voice, host placement, adopting independent work.

## Assumptions

- Planner routes under `/api/planners`: create, `{id}`, `prompt`, `cancel`,
  `events`, `context`, `requests/{requestId}`, `snapshots`,
  `snapshots/{snapshotId}`, `snapshots/{snapshotId}/handoff`.
- Working drafts under `/api/factory/working-drafts`: list, create, `{id}`
  (get, put, delete), `{id}/convert`, `installation`.
- Runs: `GET`/`POST /api/runs`, `GET /api/runs/{agentId}`,
  `POST /api/runs/{agentId}/answer`.
- Project: `GET`/`PUT /api/projects/settings` (conditional write with an
  ETag), `GET /api/projects/health`, `POST /api/repos/{id}/discover`.
- The Svelte logic to port: `lib/planner.ts`, `lib/working-drafts.ts`,
  `lib/runs/model.ts`, `views/Planner.svelte`, `views/FactoryNew.svelte`,
  `views/WorkingDrafts.svelte`, `views/Run.svelte`, `views/Project.svelte`.

## Decisions this plan makes beyond the spec

- A planning thread is `ThreadKind.agent` with a `planning` flag on
  `ThreadRef` until it is handed off, when its entry is replaced by the
  work item's. A planning thread with no project is listed under a "No
  project" group.
- A draft autosaves two seconds after the last edit and on closing the
  sheet.
- The graph view lays tasks out in dependency layers, left to right, with
  straight edges; there is no free dragging.

## Execution

- Branch `f6-2-planning-runs-project`. Commit each task as
  `f6-2-planning-runs-project: task N — <title>`, explicit paths only.

## Contracts

Contract files (each task that needs them): `planner.go`, `drafts.go`,
`runs.go`, `projectsettings.go`, declaring with today's JSON the types
`lib/planner.ts`, `lib/working-drafts.ts`, `lib/runs/model.ts` and the
project-settings and health parts of `lib/api.ts` hand-write.

```dart
// model/planning_model.dart
class PlanningModel extends ChangeNotifier {
  String plannerId; List<PlannerMessage> messages; PlannerSnapshot? latest;
  Future<void> prompt(String text); Future<void> cancel();
  Future<void> answer(String requestId, String answer);
  Future<PlannerSnapshot> snapshot();
}

// model/handoff_model.dart
enum FieldSource { inherited, overridden }
class HandoffField<T> { T value; FieldSource source; }
class HandoffModel extends ChangeNotifier {
  FactoryCreate draft; Map<String, FieldSource> sources; String? draftId;
  List<String> get blockers;           // what stops approval, in words
  String get consequence;              // what approving authorises
  void set(String field, Object value); void reset(String field);
  Future<FactoryItem> approve();
}

// model/drafts_model.dart
enum DraftConflict { none, changedElsewhere, deletedElsewhere }
class DraftsModel extends ChangeNotifier {
  Future<void> save(HandoffModel handoff);
  DraftConflict conflict; Future<void> keepMine(); Future<void> takeTheirs();
}

// model/run_model.dart
enum RunView { lanes, graph, timeline }
enum CellState { pending, running, passed, failed }
class RunTask { int number; String title; Map<String, CellState> cells;
  int fixRounds; String? commit; List<int> dependsOn; }
class RunModel extends ChangeNotifier { List<RunTask> tasks; RunView view; String? selectedCell; }

// model/project_model.dart
class ProjectModel extends ChangeNotifier {
  ProjectSettings settings; ProjectHealth? health; bool dirty; bool stale;
  Future<void> save(); Future<void> reload();
}
```

`ThreadKind.planRun` threads use the tab set Plan, Roles, Changes,
Terminal, Inspect.

## Task 1 — planning thread

**Files:** `web/bridge/contract/planner.go`, fixtures, generated files,
`app/lib/src/model/planning_model.dart`,
`app/lib/src/ui/factory/planning_screen.dart`, tests, `screens.dart`.

1. Move the planner types into the contract.
2. The conversation in the centre from the planner's event stream; the
   evolving plan in the inspector from the latest snapshot; the planner's
   requests as asks in place.
3. "Plan first" in the new-work dialog is enabled and opens this thread.
4. Tests: `prompt posts`, `a request is an ask`, `snapshot fills the
   plan`, `plan first opens a planning thread`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 2 — the handoff review

**Files:** `app/lib/src/model/handoff_model.dart`,
`app/lib/src/ui/factory/handoff_sheet.dart`, new-work dialog, tests.

1. One `Sheet` with, in order: scope, repository, environment, models by
   role, expected checks, round allowance, planner authority, and the
   consequence of approving. Each field shows whether it is inherited from
   the project or overridden, with Reset.
2. `blockers` lists what stops approval in plain words; Approve is disabled
   while it is non-empty and its tooltip says why.
3. "Hand to the factory" on a planning thread and "Work item" in the
   new-work dialog both open it. Approving posts `POST /api/factory/items`
   and opens the work-item thread.
4. Tests: `fields default from the project`, `an override is marked`,
   `reset returns to inherited`, `approve is blocked with a reason`,
   `approve creates the item and opens its thread`.

**Verify:** `cd app && flutter test`.

## Task 3 — working drafts

**Files:** `web/bridge/contract/drafts.go`, fixtures, generated files,
`app/lib/src/model/drafts_model.dart`,
`app/lib/src/ui/factory/draft_conflict.dart`, `handoff_sheet.dart`, tests.

1. Autosave per Decisions; unfinished drafts are listed in the new-work
   dialog under a Drafts tab.
2. A save the bridge refuses as changed shows both versions side by side
   with Keep mine and Take theirs; a draft deleted elsewhere offers Save as
   new and Discard.
3. Tests ported from `working-drafts.test.ts` where the rule survives,
   including `autosaves after two seconds`, `a stale save shows both`,
   `keep mine overwrites with the new version`,
   `take theirs replaces the form`, `deleted elsewhere offers save as new`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 4 — plan runs

**Files:** `web/bridge/contract/runs.go`, fixtures, generated files,
`app/lib/src/model/run_model.dart`, `docs/studio-flutter/design-system.md`,
`app/lib/src/ui/kit/lanes.dart`, `task_graph.dart`, gallery,
`app/lib/src/ui/factory/run_screen.dart`, tests, `screens.dart`.

1. Runs appear in the tree as `ThreadKind.planRun`.
2. Add `Lanes` and `TaskGraph` to the kit by §10. Lanes: tasks by
   Implement, Verify, Review, Commit. Graph per Decisions. Timeline: one
   bar per role over time using `Segments`' colours.
3. Selecting a cell shows that role's conversation through F3's thread
   screen; a run's question is an ask.
4. Tests: `cells follow task state`, `graph layers follow depends on`,
   `a cycle-free plan without depends-on is a chain`,
   `selecting a cell opens the role's transcript`, `answer posts`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 5 — the project page

**Files:** `web/bridge/contract/projectsettings.go`, fixtures, generated
files, `app/lib/src/model/project_model.dart`,
`app/lib/src/ui/project/project_page.dart`, `board.dart`,
`defaults_tab.dart`, `health_tab.dart`, tests, `screens.dart`.

1. Tabs Work, Automations, Defaults, Health. Work: the project's items in
   four stage columns (stacked under 760), then its agents and runs.
   Automations: a read-only summary with a link, until F7.
2. Defaults: agent defaults and factory settings with inheritance shown
   live; Save sends the conditional write; a stale save keeps the edit,
   says so, and offers Reload; leaving with unsaved edits asks first.
3. Health: gate runnable, mirror freshness, index, retained workspaces,
   forge connection, and Run discovery with its report.
4. Tests: `board columns follow stage`, `a stale save keeps the edit`,
   `unsaved edits ask before leaving`, `role overrides follow the agent
   default and reset`, `discovery shows its report`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 6 — end to end, record, full verification

**Files:** `app/integration_test/f6_2_test.dart`, the spec.

1. On the fixture bridge: plan first, hand off, approve; and separately
   start and follow a plan run to its first commit.
2. Implementation record.

**Verify:** `cd app && flutter analyze && flutter test && flutter test
integration_test -d linux && dart format --output=none --set-exit-if-changed
lib test`.

## Done when

- A planning conversation becomes a work item through one sheet, from which
  the user can state scope, repository, environment, models, checks,
  allowance, authority and what approval authorises.
- A draft edited on two devices is never silently overwritten.
- A plan run is followed and steered task by task.
- All three choices in the new-work dialog work.
