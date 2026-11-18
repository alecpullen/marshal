# F9 — closing the gap between the mock and the Studio

Status: **draft for approval**, 2026-11-18. Authored against `walk-main` at
`b8bfefd8`. Spec of [the programme](programme.md) after F8: the Studio is
served at `/`, and the mock in [`mock/`](mock/) is still the design's
clickable reference.

The rules every screen follows are in [design-system.md](design-system.md);
this document adds no rule of its own.

## Context

F8 cut the Svelte Studio over to the Flutter one. Comparing
`mock/lib/` with `app/lib/src/ui/` screen by screen afterwards found nine
places where the shipped Studio is behind the reference. They are three
kinds of gap, and the kinds differ in what closing them costs.

**A surface that exists but nothing reaches.** The inspector (F4) and share
links (F7) were built and tested; no shipped screen shows them. The thread
header's Stop and Hold menu is drawn only when the shell passes callbacks it
never passes.

**An action only reachable from inside a thread, or from nowhere.** Starting
work needs a thread to open first; the narrow app bar and the sidebar have no
New; a repository cannot be connected; Automations has no New automation;
there are no shell-level keys.

**A surface thinner than the mock.** Overview lost its activity feed, its
per-project CI and spend columns and the detail on a running tile. A
project's Automations tab shows three read-only rows where the mock shows
schedules, watches and intake.

| # | Gap | Mock | App |
|---|---|---|---|
| 1 | The inspector never reaches a thread | `inspector.dart:17-48` | `shell.dart` passes no `inspector`; `thread_screen.dart:355` returns the agent's tabs only |
| 2 | Share is permanently disabled | `thread.dart:120` | `thread_header.dart:118` says "arrives in F7"; `access_model.dart:152` can already mint a link |
| 3 | The header's Stop/Hold menu never appears | `thread.dart:121` | `thread_header.dart:123` needs `onStop`/`onHold`; the shell passes neither |
| 4 | No way to connect a repository | `main.dart:240` | `sidebar.dart:112` has `Label('Projects')` with no control; `bridge_client.dart:684` `registerRepo` has no caller |
| 5 | No shell-level `⌘N`, `⌘1/⌘2/⌘3` | `main.dart:77-82` | `⌘N` exists only inside a focused thread (`thread_screen.dart:229`) |
| 6 | No New on the narrow app bar or in the sidebar | `main.dart:124`, `:186` | `shell.dart:196` app bar; `sidebar.dart:84-112` |
| 7 | Automations has no New automation and no counts | `places.dart:50-57` | `automations_place.dart:60` has labels only |
| 8 | Overview is thin | `overview.dart:54-130, 195-300` | `overview.dart:412-470` |
| 9 | A project's Automations tab is thin | `project.dart:126-134` | `project_page.dart:314-348` |

## Decisions

1. **The bridge decides what an action can do; the mock decides where it
   lives and what it says.** Where the two disagree the bridge wins and this
   document records the difference. Two matter here: connecting a
   repository registers one for issue intake rather than creating a project
   (`POST /api/projects` is dropped in [route-parity](route-parity.md)),
   and one Overview column cannot be filled at all (decision 3).
2. **Stop ships; Hold does not.** `POST /api/sessions/X/cancel` exists and
   `ComposerModel.stop()` already calls it. No route, client method or model
   call holds an agent — nothing in `web/bridge` or `bridge_client.dart` can
   do it. The header offers Stop, and hold becomes a bridge gap rather than
   dead UI. The mock's tooltip is reworded, not copied.
3. **Step progress is reported by the agent, and an agent turn has no
   total.** The mock draws `Segments(done, total, status)` on a running
   tile, but a turn's steps are discovered as the model narrates, not
   planned: nothing knows the total while the turn runs. What the agent can
   report is how many steps the turn has taken —
   `session.State.Steps()` (`internal/app/session/steps.go:121`) feeds
   `buildTelemetry` (`internal/acp/turn.go:1611`) — so an agent tile shows
   "step N" and a plan run's tile shows real done/total from the
   `run_progress` payload the bridge already carries. The contract gains
   `stepsDone`, not `stepsDone` + `stepsTotal` for agents.
4. **The shell composes the inspector.** `ThreadScreen` keeps drawing what
   it is given; the shell, which holds the host and the agent id, builds the
   `InspectorState` and each tab's body. This is what F4's own note says the
   host does, and it keeps `ui/thread/` free of client calls.
5. **One entry point for starting work.** `_openNewWork` and
   `_openNewWorkItem` in `shell.dart` stay the only doors: the Overview's and
   project page's New, the sidebar's plus, the app bar's plus, `⌘N` and the
   empty transcript all call them.
6. **Keys belong to the shell**, one `Shortcuts`/`Actions` above the place,
   and are ignored while a text field has focus so typing a `⌘N` into a
   composer is not a key press.
7. **Overview joins what the client already fetches.** `audit()`,
   `usage()` and `pulls()` are read on the Overview's own refresh and joined
   by project; there is no new census endpoint.
8. **Only an agent thread has an inspector.** A work item and a plan run are
   their own screens (F6), which already show those tabs in place, and the
   shell routes both away from `_ThreadPlace`. `model/inspector_state.dart`
   still declares the set each kind would show (its `tabsForThread`), so a
   screen that adopts one gets it; what changes is that `thread_screen.dart`
   reads that function instead of keeping its own copy, which today answers
   "none" for the two factory kinds while the model says otherwise.

## Batch A — surfaces that exist but are unreachable

### A1. The inspector reaches the shell

The shell builds an `InspectorState` per open agent thread and passes it with
a body builder to `ThreadScreen`. The set is the one F4 built: Changes,
Files, Terminal, Preview, Inspect, each body the tab widget the inspector
directory already holds — `ChangesTab`, `FilesTab`, `TerminalTab`,
`PreviewTab`, `InspectTab` — given the models their constructors take
(`ChangesModel`, `FilesModel`, `TerminalSession`, `PreviewModel`,
`InspectModel`), all built from the thread's agent id and the host's client.
The models outlive a rebuild, so one holder per open thread owns them; the
tab widgets stay stateless about where their data comes from.

`thread_screen.dart` takes its set from `tabsForThread` rather than its own
switch, so the model and the screen cannot disagree again.

Widths are the frame's: docked beside the transcript, expanded to take the
window, collapsed to a rail; below `Layout.medium` the frame is a sheet
(`_inspectorBelowBreakpoint`, `thread_screen.dart:367`). `⌘\` and `i` begin
working because the guard in `thread_screen.dart:376` stops rejecting them.

### A2. Share

The shell passes `onShare`, which calls the existing share path in
`AccessModel` (a status link for the thread's agent, at the TTL the Settings
screen offers), then shows the URL with a copy action, an expiry and a
revoke. The header's tooltip stops promising F7. A link that cannot be
minted says why — a thread with no agent, or a bridge that refused — rather
than leaving a dead button.

### A3. Stop, and no Hold

The shell passes `onStop` to the thread place, calling the composer model's
stop. `Hold` is not passed: `thread_header.dart` already omits an item whose
callback is null, so the menu shows Stop alone. A note beside the menu
records why (decision 2), so the omission is a choice a reader can find.

## Batch B — start work from anywhere

### B1. Connect a repository

The plus beside `Label('Projects')` opens a sheet that registers a
repository: URL, forge, and an optional credential reference, through
`registerRepo`. It says it registers a repository for issue intake, because
that is what it does; a project appears when the daemon discovers one, and
the sheet says that too. On success it offers discovery for the new repo,
which is the step that makes issues appear.

### B2. Keys

One `Shortcuts` above the place: `⌘N` opens the new-work dialog for the
project in view (the thread's, else the first project, else a picker), and
`⌘1/⌘2/⌘3` go to Overview, Inbox and Pull requests. Each is suppressed
while a text field has focus. The existing in-thread `⌘N` keeps working and
resolves to the same call.

### B3. New where it is missing

The narrow app bar gains the plus beside the place title, and the sidebar a
plus beside its search field; both call the same entry point as the page
headers. The dialog needs a project only when it is opened without one, so
the picker is the fallback rather than a disabled button.

### B4. New automation, and counts

The Automations header gains "New automation": one menu whose choices are
the kinds that have a create flow — a schedule (the schedules tab's editor),
a watch (`showWatchForm`, already behind "New watch"), a recipe (copy from
an existing one, or a new one if the recipes tab has it). Each tab carries
its count, from the model that already loads it.

## Batch C — Overview and project page parity

### C1. Overview

- **Activity**: the last few audit events, with the actor, the action and
  the relative time, as the mock's Activity card.
- **Projects table**: per project, running and needs-you counts (already
  there), plus CI state joined from `pulls()` and today's spend joined from
  `usage()`, which already returns a series.
- **Running tiles**: activity text, changed files, a context meter, and —
  after the contract change in C3 — "step N".
- **Pull requests**: "N more in progress with checks passing or running"
  under the ready and failing cards, from the pulls groups the Pull-requests
  screen already computes.
- **Spend**: the day's total against the budget (there today) with the
  series drawn as a spark, and the per-role split when `usage(by: 'role')`
  is read.

### C2. A project's Automations tab

The tab lists what the project actually has: its schedules, its watches and
its issue intake, from `projectAutomations()`, `schedules()` and
`watches()`, each row linking to the editor that already exists. The three
forge rows stay above them. Nothing becomes editable here that is not
editable on the Automations place; the tab is a view with doors.

### C3. Step progress, end to end

1. **Agent**: `buildTelemetry` adds a `steps` section — the count of steps
   on the active branch for the current turn, from `State.Steps()`.
2. **Contract**: `FleetDelta.StepsDone` and `AgentStatus.StepsDone`, both
   `json:"stepsDone,omitempty"`, with the Dart module regenerated.
3. **Bridge**: the classifier decodes `steps` from `session_telemetry` into
   the delta, `agentLive` keeps it, `Snapshot()` reports it.
4. **Studio**: the Dart `AgentStatus` carries it, and a running tile reads
   "step N".

There is no total, and the tile says so by showing a step number rather
than a fraction (decision 3).

## Testing

- **The design system binds every UI task.** Screens are built from the
  kit, colours and sizes from `tokens.dart`, every icon-only control has a
  tooltip, and every screen is registered in `app/test/support/screens.dart`
  so `design_rules_test.dart`, `tooltips_test.dart`, `states_test.dart` and
  `overflow_test.dart` cover it.
- **The boundaries hold.** `ui/` still imports no `client/`; the inspector is
  composed from the shell. `web/` stays standard library only.
- **Route parity is checked, not narrated.** The rows for `registerRepo` and
  `createStatusLink` already exist in
  [route-parity](route-parity.md); the tasks give them callers, and
  `app/test/route_parity_test.dart` keeps the table honest.
- **The contract is generated.** A field added in C3 fails
  `TestDartContractIsCurrent` until the module is regenerated, and
  `contractwire_test.go` records the wire shape.
- **Widgets are tested against a fake client**, never a daemon; the Overview
  and Automations tests use the existing fixtures. Nothing reads the
  developer's config, keychain or running daemon.
- **One end-to-end case per batch**: an inspector opened and a diff read; a
  repository registered from the sidebar; an Overview whose activity and CI
  columns agree with the fleet.

## Non-goals

- **Host onboarding.** It is being planned separately; the host switcher
  stays read-only and Settings › Hosts keeps its planned row.
- **Hold an agent**, which needs a bridge action that does not exist.
- **Search (`⌘K`)**, attach and dictate, which the phase documents defer.
- **Starting an agent from an issue** (`POST /api/repos/X/issues/X/spawn` is
  dropped), **starting a run** (`POST /api/runs`), **resuming a stored
  session**, and the **workspace test shell** — all `dropped:` rows in
  route-parity.
- **Creating a project** from the app, and **budget override** for a paused
  agent.

## Open questions

None. Each gap has a mechanism, and the two places the mock could not be
followed (Hold, and a total on an agent tile) are recorded as decisions.

## Execution

Three plans, executed in order, each one commit per task:

1. [Reachable surfaces](plans/2026-11-18-f9-1-reachable-surfaces-plan.md) —
   the inspector, share and stop.
2. [Start work from anywhere](plans/2026-11-18-f9-2-start-from-anywhere-plan.md)
   — connect a repository, keys, New in the shell, New automation.
3. [Overview and project parity](plans/2026-11-18-f9-3-overview-parity-plan.md)
   — the step-progress contract field, the Overview's joins, and a project's
   Automations tab.

Batches A and B are independent and may run in either order; C's contract
task (C3.1–C3.3) must land before the tile that reads it.
