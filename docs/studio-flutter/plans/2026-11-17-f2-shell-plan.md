# F2 plan: shell, Overview and Inbox — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: `main` at `11957d83`, which contains F0. Runs after
[the F1 plan](2026-11-17-f1-client-plan.md), whose Contracts this plan
consumes; re-check those names in the tree before starting.

Approved design: [f2-shell-overview-inbox.md](../f2-shell-overview-inbox.md),
including "What F0 found". Finding:
[tray-and-window.md](../findings/tray-and-window.md). Rules:
[design-system.md](../design-system.md).

**How this plan is written.** No implementation code; Contracts holds
declarations only; steps are prose. The mock (`docs/studio-flutter/mock/lib/
main.dart`, `overview.dart`, `inbox.dart`) is the visual reference, built
here from the kit to the design system's scales.

## Goal

- A Studio window on macOS and Linux opened from the tray; the same Studio
  in a browser at `/next/`.
- Sidebar, Overview and Inbox on live data; approvals, questions and intake
  answered from their cards.

## Non-goals

- The thread screen, new-work dialog, pull requests, Automations, Library,
  Settings: each is a planned-state placeholder.
- Search. Notifications.

## Assumptions

- F1's `HostRegistry`, `BridgeClient`, `EventStream` and generated types
  exist as declared.
- The kit has `ThreadRow`, `AskCard`, `StateMessage`, `PageBody`, `Label`,
  `Meter`, `Spark`, `Segments`, `Box`, `Menu`, `Tip`
  (`app/lib/src/ui/kit/`), and screens register a `ScreenCase` in
  `app/test/support/screens.dart`.
- The fleet stream's deltas are `FleetDelta`, `InboxDelta`, `BudgetDelta`
  and `ProjectRemovedDelta` with a `DeltaKind`
  (`web/bridge/contract/stream.go`).
- `staticHandler` in `web/bridge/assets.go` serves the embedded Svelte build
  with an index fallback.
- `app/macos/Runner/Info.plist` sets `LSUIElement`;
  `app/linux/runner/my_application.cc` is the unmodified template.

## Decisions this plan makes beyond the spec

- A thread's status from an agent: needs you when it has a pending request;
  failed when its state is an error state; ready when it has finished with
  changes unshipped; running when a turn is in flight; idle otherwise. The
  mapping is one function and one table test.
- "Earlier" is every agent that is idle and last active more than a day ago.
- The Flutter web build is written to `web/bridge/static-next/` and embedded
  beside `static`; in a Go-only checkout the directory holds a placeholder
  index, as `static` does.
- The browser's place is the URL path under `/next/`: `/next/`,
  `/next/inbox`, `/next/project/<id>`, `/next/thread/<host>/<id>`.

## Execution

- Branch `f2-shell` from the branch F1 landed on.
- Commit each task as `f2-shell: task N — <title>`, explicit paths only.
- Before Task 1, add a line to `AGENTS.md`'s web control plane section: the
  Svelte Studio is frozen, bug fixes only.

## Contracts

```dart
// model/place.dart
enum PlaceKind { overview, inbox, pulls, project, thread, automations, library, settings }
class Place { PlaceKind kind; String? host; String? id; }
Place parsePlace(String path);   String formatPlace(Place place);

// model/thread_ref.dart
enum ThreadKind { agent, workItem, planRun }
enum ThreadStatus { needsYou, running, ready, failed, idle }
class ThreadRef { String host; String id; String project; String title;
  ThreadKind kind; ThreadStatus status; String headline; DateTime lastActive; }
ThreadStatus statusOfAgent(AgentStatus agent);

// model/fleet_store.dart — the only writer of fleet state
class FleetStore extends ChangeNotifier {
  FleetStore(HostRegistry hosts);
  List<AgentStatus> agents({String? host});
  List<ProjectStatus> projects({String? host});
  List<InboxItem> inbox({String? host});
  BudgetReport? budgets(String host);
  HostReach reach(String host);  DateTime? lastContact(String host);
  Future<void> refresh(String host);
}

// model/nav_model.dart
class NavModel { List<NavPlace> fixed; List<NavProject> projects; }
class NavPlace { PlaceKind kind; String label; int? count; bool active; bool planned; }
class NavProject { String id; String name; List<ThreadRef> current; int earlier; bool active; }
NavModel buildNavModel(Place place, FleetStore store);

// model/inbox_model.dart
enum AskKind { approval, question, intake }
enum AskState { waiting, sending, failed }
class Ask { String host; String id; AskKind kind; ThreadRef? thread; String title;
  String body; List<AskAction> actions; DateTime since; AskState state; String? error; }
class AskAction { String label; bool primary; }
class InboxModel extends ChangeNotifier {
  List<Ask> get asks;  Future<void> answer(Ask ask, AskAction action, {String? reply});
}

// model/overview_model.dart
class OverviewModel { List<Ask> needsYou; List<ThreadRef> running;
  List<ProjectSummary> projects; List<HostCapacity> capacity; SpendSummary? spend; }
OverviewModel buildOverview(FleetStore store, InboxModel inbox);
```

## Task 1 — places, thread references, navigation model

**Files:** `app/lib/src/model/place.dart`, `thread_ref.dart`,
`nav_model.dart`, and their tests under `app/test/model/` (all new).

1. `parsePlace` and `formatPlace` are inverses; an unknown path is Overview.
2. `statusOfAgent` implements the mapping under Decisions.
3. `buildNavModel` returns the fixed places in the design's order, with the
   Inbox count from the store and `planned` true for pull requests,
   Automations, Library and Settings; projects in name order; each
   project's agents as `ThreadRef`s split into current and an earlier
   count.
4. Tests: `place round trips`, `unknown path is overview`,
   `agent status table`, `projects sort by name then id`,
   `earlier counts idle agents older than a day`,
   `inbox count follows the store`. Port the surviving cases of
   `web/ui/src/lib/nav/model.test.ts` and list in the commit which were
   dropped and why.

**Verify:** `cd app && flutter test test/model/`.

## Task 2 — the fleet store

**Files:** `app/lib/src/model/fleet_store.dart`,
`app/test/model/fleet_store_test.dart` (new).

1. Per host: fetch agents, projects, inbox and budgets on connect and on a
   stream gap; apply each delta kind as `web/ui/src/lib/fleet.ts` and
   `store.ts` do. An unknown delta kind is ignored.
2. A host that is not live keeps its last data; the store exposes its reach
   and last contact so views can mark rows stale.
3. Tests against F1's fake bridge: `snapshot populates`, one test per delta
   kind, `removed project drops its agents`, `unknown delta is ignored`,
   `gap refetches`, `stale host keeps its rows`.

**Verify:** `cd app && flutter test test/model/fleet_store_test.dart`.

## Task 3 — inbox and overview models

**Files:** `app/lib/src/model/inbox_model.dart`, `overview_model.dart`, and
tests (new).

1. `InboxModel` projects the store's inbox items and each agent's pending
   request into `Ask`s, oldest first. Actions: approval — Approve once,
   Always allow the derived pattern, Deny; question — its options, then
   Reply; intake — Start, Edit first, Decline.
2. `answer` posts through `BridgeClient`, sets `sending`, and on failure
   sets `failed` with the bridge's message and keeps the ask. The ask
   leaves only when the store no longer reports it.
3. `buildOverview` computes its sections from real items only.
4. Tests: `asks are oldest first`, `approval actions`, `question actions`,
   `intake actions`, `a failed answer keeps the ask`,
   `an ask leaves when the stream confirms`,
   `overview counts equal the store's`.

**Verify:** `cd app && flutter test test/model/`.

## Task 4 — the window and the tray

**Files:** `app/lib/main.dart`, `app/lib/src/ui/shell/window_host.dart`
(new), `app/lib/src/menu/model.dart`, `app/macos/Runner/
MainFlutterWindow.swift`, `app/macos/Runner/AppDelegate.swift`,
`app/linux/runner/my_application.cc`, `app/test/menu_model_test.dart`.

1. Implement the mechanism in the finding: a `marshal/window` method
   channel with `show`, `hide` and `quit`. On macOS it switches the
   activation policy and orders the window; closing hides. On Linux the
   window is not shown on the first frame, hides on close, is presented on
   `show`, and the application is held.
2. `window_host.dart` wraps the channel behind an interface so tests fake
   it.
3. The tray menu gains "Open Studio" as its first item, calling `show`.
   In the browser `main.dart` starts the Studio with no tray code.
4. Tests: `Open Studio is the first menu item`,
   `closing the window hides and does not quit` (against the fake).

**Verify:** `cd app && flutter analyze && flutter test && flutter build
linux --debug`. On Linux, run the built app: no window at launch; the tray
item opens it; closing it leaves the tray item.

## Task 5 — the shell

**Files:** `app/lib/src/ui/shell/shell.dart`, `sidebar.dart`,
`host_switcher.dart`, `placeholder.dart`, `app/test/ui/shell_test.dart`,
`app/test/support/screens.dart`.

1. Sidebar, host switcher (a `Menu` of hosts with reach), the content area
   switching on `Place`, the drawer below 760 and the app bar it needs.
2. Places not built yet show `StateMessage` in the planned state naming the
   phase that brings them. The search field is disabled with a tooltip.
3. Register the shell as a `ScreenCase` with all five states.
4. Tests: `selecting a row changes the place`, `drawer below 760`,
   `planned places say so`, `an unreachable host is listed with its age`.

**Verify:** `cd app && flutter test` (the enforcement tests run with it).

## Task 6 — Overview

**Files:** `app/lib/src/ui/overview/overview.dart`,
`app/test/ui/overview_test.dart`, `screens.dart`.

1. Needs you (top three asks, link to Inbox), Running now (tiles), Projects
   (table), and the side rail: capacity, spend, activity. The rail moves
   below the main column under 1080.
2. Register with all five states.
3. Tests: `shows the three oldest asks`, `tile opens its thread`,
   `stale host rows carry their age`, `empty fleet shows one action`.

**Verify:** `cd app && flutter test`.

## Task 7 — Inbox

**Files:** `app/lib/src/ui/inbox/inbox.dart`,
`app/test/ui/inbox_test.dart`, `screens.dart`.

1. Filters All, Blocking, To look at. Each ask is an `AskCard`; choosing an
   action calls `InboxModel.answer` and the card shows sending, then leaves
   or shows the failure with Retry.
2. Tests: `approve once posts the decision`, `deny posts a denial`,
   `reply posts free text`, `a failure shows retry`, `filters partition`.

**Verify:** `cd app && flutter test`.

## Task 8 — `/next/`

**Files:** `web/bridge/assets.go`, `web/bridge/assets_test.go`,
`web/bridge/static-next/index.html` (placeholder),
`.github/workflows/go-checks.yml`, `.github/workflows/release.yaml`,
`AGENTS.md`, one link in `web/ui/src/App.svelte`.

1. Embed `static-next` and serve it under `/next/` with the same index
   fallback, registered so no `/api/` route is shadowed.
2. CI and the release build run `flutter build web --base-href /next/` into
   `static-next` before `go build ./cmd/webbridge`. Document the command in
   `AGENTS.md`.
3. Tests: `TestNextServesItsIndex`, `TestNextFallsBackToIndex`,
   `TestNextDoesNotShadowAPI`.
4. Record the `webbridge` binary size before and after in the spec.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -run 'Next|Assets|StdlibOnly'
-count=1`; `cd web/ui && npm run check && npm test`.

## Task 9 — end to end, record, full verification

**Files:** `app/integration_test/f2_test.dart` (new),
`docs/studio-flutter/f2-shell-overview-inbox.md`.

1. Start the fixture bridge as `scripts/e2e/run_browser_checks.sh` does,
   start an agent that asks for approval, drive the app to approve it, and
   assert the agent leaves the needs-you state.
2. Add the implementation record to the spec.

**Verify:** `cd app && flutter analyze && flutter test && flutter test
integration_test -d linux && dart format --output=none --set-exit-if-changed
lib test`; `CGO_ENABLED=1 go test ./web/... -count=1`.

## Done when

- Sidebar, Overview and Inbox agree with `marshal fleet` on a local daemon
  and on a server.
- An approval answered in the app unblocks its agent.
- A stopped host's rows are marked stale, then unavailable, and remain.
- `/next/` does the same at phone width.

## Needs a Mac

- Launch the app: menu-bar item, no Dock icon; "Open Studio" shows the
  window and a Dock icon; the red button hides it and removes the Dock
  icon; the app does not quit. Steps are in the finding.

## What later plans rely on

- The Contracts above; `ThreadRef` gains kinds in F6.
- `placeholder.dart` is where each later phase replaces a planned place.
