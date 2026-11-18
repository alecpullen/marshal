# F9.3 — Overview and project parity

The plan for Batch C of [F9](../f9-parity.md): an agent turn reports how many
steps it has taken, the Overview gains the mock's activity feed, per-project
CI and spend, the running tile's detail, and a project's Automations tab lists
what it actually has.

- **Spec**: [../f9-parity.md](../f9-parity.md), Batch C and decisions 1, 3, 7.
- **Authored against**: `walk-main` at `b8bfefd8`.
- **Executed as**: one commit per task, `<plan-slug>: task N — <task title>`.
- **Verification runs on Linux**: `CGO_ENABLED=1 go test ./…` for the Go tasks,
  `cd app && flutter test …` for the Dart ones.

## Non-goals

- A total on an agent's step strip: a turn's steps are discovered, so a tile
  shows a step number and never a fraction (F9 decision 3). A plan run's
  done/total already comes from `run_progress` and is not touched here.
- A new bridge census endpoint: the Overview joins `audit`, `usage` and
  `pulls` client-side (F9 decision 7).
- A per-project "today" spend column: `getUsage` accepts `range=7d|30d` only
  (`web/bridge/usage.go:361`), so the column is seven-day spend and says so.
- Host slot counts, which the contract does not carry.

## Contracts

### Go

```go
// internal/app/session/steps.go

// TurnSteps is how many steps the turn in flight has taken: the trailing run
// of steps on the active branch that share the newest step's turn message.
// Zero when no step has begun since the last user message.
func (s *State) TurnSteps() int

// internal/acp/turn.go — buildTelemetry's payload gains one section:
//   "steps": map[string]any{"done": state.TurnSteps()},

// web/bridge/contract/agents.go — AgentStatus gains, after ChangedFiles:
//
// StepsDone is how many steps the current turn has taken. A turn's steps are
// discovered as the model narrates, so there is no total while it runs: a
// client shows a step number, never a fraction.
StepsDone int `json:"stepsDone,omitempty"`

// web/bridge/contract/stream.go — FleetDelta gains, after ChangedFiles:
StepsDone *int `json:"stepsDone,omitempty"`
```

### Dart

```dart
// app/lib/src/model/overview_model.dart

/// One project's work, as the Overview's table shows it (F2 decision 8), plus
/// what the pull index and the usage ledger say about it.
class ProjectSummary {
  const ProjectSummary({
    required this.id,
    required this.name,
    required this.needsYou,
    required this.running,
    required this.ready,
    required this.failed,
    required this.idle,
    this.ciReady = 0,
    this.ciFailing = 0,
    this.spendUSD = 0,
  });

  // …the seven F2 fields, unchanged…

  /// Ready pull requests from this project, and failing ones.
  final int ciReady;
  final int ciFailing;

  /// What this project's agents spent over the usage window, in dollars.
  final double spendUSD;
}

/// The Overview, projected from the store, the inbox and what the bridge
/// answered about pulls, spend and activity.
class OverviewModel {
  const OverviewModel({
    // …the seven F2 fields, unchanged…
    this.activity = const [],
    this.moreInProgress = 0,
    this.spendDays = const [],
    this.spendByRole = const [],
  });

  /// The newest audit events, most recent first, at most [overviewActivity].
  final List<AuditEvent> activity;

  /// Pull requests in progress beyond the ready and failing ones on screen.
  final int moreInProgress;

  /// The day series, oldest first: what the spend spark draws.
  final List<UsagePoint> spendDays;

  /// The window's spend by role, largest first: what the split names.
  final List<UsagePoint> spendByRole;
}

/// How many audit events the Overview shows.
const overviewActivity = 6;

OverviewModel buildOverview(
  FleetStore store,
  InboxModel inbox, {
  List<PullView> pulls = const [],
  UsageReport? usageByProject,
  UsageReport? usageByDay,
  UsageReport? usageByRole,
  List<AuditEvent> audit = const [],
});

// app/lib/src/model/thread_ref.dart — ThreadRef gains
//   final int? stepsDone;   // the turn's steps, from the fleet's report
// and threadFor copies AgentStatus.stepsDone into it.
```

## Task 1: The session counts the turn's steps

**Goal.** A session can answer how many steps the turn in flight has taken.

**Files.**
- `internal/app/session/steps.go`
- `internal/app/session/steps_test.go`

**Steps.**

1. Add `TurnSteps` to `steps.go`, beside `Steps()` (`:121`):

   ```go
   // TurnSteps is how many steps the turn in flight has taken: the trailing
   // run of steps on the active branch that share the newest step's turn
   // message. Zero when no step has begun since the last user message.
   func (s *State) TurnSteps() int {
       steps := s.Steps()
       if len(steps) == 0 {
           return 0
       }
       turn := steps[len(steps)-1].TurnMsgID
       n := 0
       for i := len(steps) - 1; i >= 0; i-- {
           if steps[i].TurnMsgID != turn {
               break
           }
           n++
       }
       return n
   }
   ```

   `Steps()` already returns only the active branch's steps, oldest first
   (`steps.go:121-135`), so the loop needs no branch logic of its own.

2. Tests in `steps_test.go`, using the file's existing state fixture helpers:
   - `TestTurnStepsCountsTheRunningTurn`: three steps in the newest turn and
     two in the one before; expect `3`.
   - `TestTurnStepsIsZeroBeforeAnyStep`: a fresh state; expect `0`.
   - `TestTurnStepsIgnoresAnotherBranchsTurn`: steps whose `TurnMsgID` is on
     another branch are not counted (build them as `Steps()` already filters,
     then assert the trailing run alone counts).

**Verify.** `CGO_ENABLED=1 go test ./internal/app/session -run TestTurnSteps -v`
— the three tests pass.

## Task 2: The step count reaches the Studio

**Goal.** A fleet agent carries `stepsDone`, end to end: telemetry, contract,
bridge, generated Dart.

**Files.**
- `internal/acp/turn.go`
- `web/bridge/contract/agents.go`
- `web/bridge/contract/stream.go`
- `web/bridge/fleetevents.go`
- `web/bridge/fleet.go`
- `web/bridge/contractwire_test.go`
- `web/bridge/fleetevents_test.go`
- `app/lib/src/client/contract.g.dart` (regenerated, never hand-edited)

**Steps.**

1. `turn.go`, in `buildTelemetry` (`:1611`), add to the payload map:

   ```go
   "steps": map[string]any{"done": state.TurnSteps()},
   ```
   The comment above the function says every top-level field is always
   present; this keeps that promise.

2. `contract/agents.go`: `AgentStatus` gains `StepsDone int
   \`json:"stepsDone,omitempty"\`` after `ChangedFiles`. `contract/stream.go`:
   `FleetDelta` gains `StepsDone *int \`json:"stepsDone,omitempty"\`` after
   `ChangedFiles`.

3. `fleetevents.go`: in the notification's decoded `Update` struct add
   ```go
   Steps struct {
       Done int `json:"done"`
   } `json:"steps"`
   ```
   and in `case "session_telemetry":` set
   ```go
   done := p.Update.Steps.Done
   d.Kind, d.ChangedFiles, d.ContextPct, d.StepsDone = "telemetry", &changed, &pct, &done
   ```
   (replacing the existing compound assignment at `:86-88`, keeping all four
   values). In `agentLive` (`:120`) add `stepsDone int`, and in
   `liveState.apply`'s `case "telemetry":` copy it when `d.StepsDone != nil`,
   beside `contextPct` and `changedFiles`. In `fleet.go`'s `Snapshot()`
   (`:2105`) set `StepsDone: live.stepsDone`.

4. Regenerate the Dart module:
   `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.
   Do not edit `app/lib/src/client/contract.g.dart` by hand; the currency test
   owns it.

5. Tests.
   - `fleetevents_test.go`: `TestTelemetryCarriesSteps` asserts a telemetry
     notification with `"steps":{"done":3}` produces a delta whose
     `StepsDone` is 3 and that `live.get("s1").stepsDone` is 3 afterwards.
   - `contractwire_test.go`: add a `delta_steps` record beside `delta_activity`
     (`:261`) whose `FleetDelta` has `StepsDone: ptr(3)`.
   - `internal/acp`: the telemetry test that already asserts the payload's
     top-level keys gains `"steps"`.

**Verify.** `CGO_ENABLED=1 go test ./internal/acp ./web/bridge ./web/bridge/contract/... -run 'Telemetry|Steps|Contract' -v`
and then `CGO_ENABLED=1 go test ./web/bridge/contract/dartgen` (currency, without
`-update`). Both pass.

## Task 3: The Overview shows activity

**Goal.** The Overview has the mock's Activity card: the newest things that
happened, with who and when.

**Files.**
- `app/lib/src/model/overview_model.dart`
- `app/lib/src/ui/overview/overview.dart`
- `app/lib/src/ui/shell/shell.dart`
- `app/lib/main.dart`
- `app/test/model/overview_model_test.dart`
- `app/test/ui/overview_test.dart`

**Steps.**

1. `overview_model.dart`: `OverviewModel` gains `activity` and the
   `overviewActivity` constant; `buildOverview` takes `audit` and fills
   `activity` with the first `overviewActivity` events, newest first (the
   bridge answers newest first — `audit({limit: 50})`). No event is filtered:
   an audit feed that hides kinds is a second, wrong feed.

2. `_StudioState`: add `List<AuditEvent> _audit = const [];` and read it in
   `_reread` (`shell.dart:565`) for the first host:
   `_audit = await hosts.hosts.first.audit(limit: 20);` guarded so a failure
   leaves it empty rather than failing the screen. Pass it to `Shell` as
   `audit: _audit`.

3. `Shell` passes `audit` into `buildOverview(widget.store, widget.inbox,
   audit: widget.audit, …)` in `_content`'s Overview branch (`:624`).

4. `overview.dart`: a new rail section `Label('Activity')` after Automations
   (`:209-227`), drawing each event as a `RowItem` whose `name` is the event's
   `event` worded for a person and whose `description` is the reason or detail
   when present, `value` the relative age (`relativeAge` as the sidebar uses).
   Empty draws `StateMessage.empty(message: 'Nothing has happened yet.')`.

5. Tests: `overview_model_test.dart` `'activity is the newest first, capped'`
   (ten events in, `overviewActivity` out, order preserved);
   `overview_test.dart` `'the Activity card names an event'`.

**Verify.** `cd app && flutter test test/model/overview_model_test.dart test/ui/overview_test.dart`
— the new cases and the existing Overview tests pass.

## Task 4: The projects table carries CI and spend

**Goal.** Each project row says how many of its pull requests are ready or
failing and what it spent, and the Pull requests section says how many more
are in progress.

**Files.**
- `app/lib/src/model/overview_model.dart`
- `app/lib/src/ui/overview/overview.dart`
- `app/lib/src/ui/shell/shell.dart`
- `app/lib/main.dart`
- `app/test/model/overview_model_test.dart`
- `app/test/ui/overview_test.dart`

**Steps.**

1. `overview_model.dart`: `ProjectSummary` gains `ciReady`, `ciFailing` and
   `spendUSD` as Contracts declares, and `buildOverview` takes
   `pulls` and `usageByProject`. Fill them by joining on the project root:
   `PullView.project` for pulls (ready → `ciReady`, failing → `ciFailing`) and
   `UsagePoint.key` for `usageByProject.series` (`key` is the project root
   when the report was read `by=project`).
2. `OverviewModel` gains `moreInProgress`: every pull view that is neither
   ready nor failing.
3. `_StudioState` reads `_usageByProject = await host.usage(by: 'project')` in
   `_reread` beside the audit read, passes it to `Shell`, and `Shell` hands it
   to `buildOverview` with `pulls: widget.pulls?.views ?? const []`.
4. `overview.dart`: the projects table (`:401-428`) gains two values per row —
   `CI` from `ciReady`/`ciFailing` (`'3 ready · 1 failing'`, `'3 ready'`, or
   `'—'` when both are zero) and `Spend` as `'\$${spendUSD.toStringAsFixed(2)}
   this week'`. Under the Pull requests section (`:304-320`) add one line when
   `moreInProgress > 0`: `'$moreInProgress more in progress with checks
   passing or running'`, as the mock's `overview.dart:32-37` does.
5. Tests: `overview_model_test.dart` `'a project's pulls are counted by
   state'` and `'the project's spend comes from its usage point'`;
   `overview_test.dart` `'the projects table names CI and spend'` and
   `'more in progress is one line'`.

**Verify.** `cd app && flutter test test/model/overview_model_test.dart test/ui/overview_test.dart test/ui/pulls/`
— the new cases and the pull-request screens pass.

## Task 5: The spend rail draws its trend and its split

**Goal.** Today's spend carries a spark of the days behind it and the split
by role, as the mock's rail does.

**Files.**
- `app/lib/src/model/overview_model.dart`
- `app/lib/src/ui/overview/overview.dart`
- `app/lib/src/ui/shell/shell.dart`
- `app/lib/main.dart`
- `app/test/ui/overview_test.dart`

**Steps.**

1. `overview_model.dart`: `OverviewModel` gains `spendDays` and
   `spendByRole`; `buildOverview` takes `usageByDay` and `usageByRole` and
   fills them from `report.series` (day series oldest first as the bridge
   answers it; role series sorted by `costUSD` descending).
2. `_StudioState` reads both in `_reread` beside the other two usage reads and
   passes them through `Shell` to `buildOverview`.
3. `overview.dart`: the `Meter.fault('Today', …)` block (`:456-470`) gains,
   under it, a `RowSpark` of `[for (final p in spendDays) p.costUSD]` with
   `label: 'Daily spend'` (the kit widget `overview.dart:88-98` uses through
   `SectionPage` rows is `RowSpark`, as `watches_tab.dart:44` shows), and one
   line naming the split: `'Implementer 61% · reviewer 24% · planner 15%'`,
   computed from `spendByRole` as each role's share of the total, skipping
   roles below one percent, at most three.
4. Tests: `overview_test.dart` `'the spend rail draws the day spark'` and
   `'the split names the top three roles'`.

**Verify.** `cd app && flutter test test/ui/overview_test.dart test/ui/settings_usage_test.dart`
— the Overview and the usage screen both pass.

## Task 6: A running tile says which step it is on

**Goal.** A running agent's Overview tile says `step N`, from the fleet's own
report.

**Files.**
- `app/lib/src/model/thread_ref.dart`
- `app/lib/src/ui/overview/overview.dart`
- `app/test/model/thread_ref_test.dart`
- `app/test/ui/overview_test.dart`

**Steps.**

1. `thread_ref.dart`: `ThreadRef` gains `final int? stepsDone;`, set from
   `AgentStatus.stepsDone` in `threadFor` beside `activity` (`:152`). Null
   means the agent has not reported one, which the tile draws as nothing.
2. `overview.dart`: the running tile (`:352-400`) gains, in its existing
   detail line, `'step ${t.stepsDone}'` when the thread has steps, beside the
   activity text it already shows — never as a fraction, and never as a
   fabricated total (F9 decision 3).
3. Tests: `thread_ref_test.dart` `'a thread carries the turn's steps'`;
   `overview_test.dart` `'a running tile names its step'` and `'a thread with
   no step reports nothing'`.

**Verify.** `cd app && flutter test test/model/thread_ref_test.dart test/ui/overview_test.dart`
— the new cases and every thread-ref test pass.

## Task 7: A project's Automations tab lists what it has

**Goal.** The project page's Automations tab shows the project's schedules,
the watches its agents started and its issue intake, each opening the editor
that owns it.

**Files.**
- `app/lib/src/model/project_model.dart`
- `app/lib/src/ui/project/project_page.dart`
- `app/test/model/project_model_test.dart`
- `app/test/ui/project/project_page_test.dart`

**Steps.**

1. `project_model.dart`: read the project's schedules and watches in the same
   load that already reads `projectAutomations` (`bridge_client.dart:387`):
   `schedules().where((s) => s.project == root)` — `Schedule.project` is on the
   wire (`contract.g.dart:12455`) — and `watches().where((w) => w.agentID` is
   an agent of this project`)`, resolving the agent-to-project mapping from
   the `FleetStore` the project model already holds. Expose them as
   `automationSchedules` and `automationWatches`, empty when the read fails,
   with the failure already reported by the existing state.
2. `project_page.dart`: in `_automations` (`:296-350`), under the three forge
   rows, add `Label('Schedules')` with one `RowItem` per schedule (name, its
   cron, `Paused`/last result) and `Label('Watches')` with one per watch
   (name, what it watches, its state), each row's `onTap` opening the
   Automations place at the matching tab through `onOpenAutomations`
   (widened to take an optional tab index), and a `Label('Issue intake')` row
   naming the watched repo and label when the project has one. Each section
   omitted entirely when it is empty, so the tab does not grow three empty
   headings.
3. Tests: `project_model_test.dart` `'the project's schedules are its own'`
   (two schedules, one of another project); `project_page_test.dart`
   `'a project lists its schedules and watches'` and `'a tab section with
   nothing is absent'`.

**Verify.** `cd app && flutter test test/model/project_model_test.dart test/ui/project/project_page_test.dart`
— the new cases and the existing project-page tests pass.

## Final verification

1. `CGO_ENABLED=1 go test ./internal/app/session ./internal/acp ./web/bridge/...`
   — the session, ACP and bridge suites, including the contract currency test.
2. `CGO_ENABLED=1 go build ./cmd/webbridge` and `CGO_ENABLED=1 go vet ./...`.
3. `cd app && flutter analyse`.
4. `cd app && flutter test` — the whole Dart suite, including
   `design_rules_test.dart`, `tooltips_test.dart`, `states_test.dart` and
   `boundary_test.dart`.
5. `cd app && flutter build web --base-href / --no-web-resources-cdn --output ../web/bridge/static`
   and `scripts/dev-daemon.sh` last, since the restart ends running turns.
6. Manual: with two agents running on one project, Overview's activity feed
   matches `marshal fleet` and the projects table's CI agrees with the Pull
   requests screen.

## Integration notes

- **The contract change is generated.** `app/lib/src/client/contract.g.dart`
  is never hand-edited; `TestDartContractIsCurrent` fails until the `-update`
  run in Task 2 commits the regenerated module.
- **A missing field is not a zero.** `stepsDone` is `omitempty` on both sides
  and nullable in Dart: an agent that has not reported steps draws nothing,
  rather than "step 0".
- **The Overview reads more, not more often.** The three usage reads and the
  audit read happen in `_reread`, which the fleet's own refresh already calls;
  no timer is added.
- **`ui/` stays off the client.** The Overview model takes already-fetched
  values; `_StudioState` does the reading, as it does for pulls today.
