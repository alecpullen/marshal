# F6 plan 2: planning, drafts, plan runs and the project page — implementation record

Date: 2026-11-17. Branch `studio/f6-2-planning-runs`, off `studio/f6-1-work-items`.
Plan: [2026-11-17-f6-2-planning-runs-project-plan.md](plans/2026-11-17-f6-2-planning-runs-project-plan.md).
Design: [f6-factory.md](f6-factory.md) decisions 8–12.

This records what the plan produced, the decisions it left open that were
settled here, the deviations from its letter, and what is not done.

## What exists

- **The contract.** `web/bridge/contract/projectsettings.go` declares a
  project's settings document, its health report and its discovery report —
  `ProjectSettings`, `ProjectIntake`, `FactoryDefaults`, `ProjectHealth`,
  `MirrorHealth`, `WorkspaceHealth`, `VerifyCommands`, `DiscoveryReport` and
  its parts. `web/bridge` aliases every name to it (`workspace.go`,
  `factory_types.go`, `projects.go`, `discovery.go`), so the journal's bytes are
  unchanged. The `Automations`, `ReviewBotSettings` and `CIFixerSettings` types
  moved beside them (`contract/automations.go`). `test/contract/
  projectsettings.json` records a settings document, a health report and a
  discovery report; both generated clients are regenerated and current, and both
  fixture round trips compare them.
- **The client.** `BridgeClient` reads and writes a project's settings with the
  ETag that names them (`projectSettings`, `putProjectSettings` with
  `If-Match`), reads its health (`projectHealth`), runs discovery
  (`discoverRepo`) and lists runs (`runs`, `GET /api/runs`). `Request` carries
  route headers and both transports forward them, which is what `If-Match`
  needed. `answerRun` posts the answer a run's question takes.
- **The model.** `model/project_model.dart` holds a project's settings and the
  ETag they were read at, its health, its runs, its last discovery report and
  its forge automations. A save is conditional and one write; a stale refusal
  keeps the edit, says so and offers a reload. `ProjectDraft` is a typed view of
  the document that keeps every member this build does not edit, so saving one
  field never drops another. The model implements `UnsavedEditsSource`, which
  the shell's one guard consults.
- **The page.** `ui/project/project_page.dart` (Work, Automations, Defaults,
  Health) with `board.dart`, `defaults_tab.dart`, `health_tab.dart` and
  `status.dart`. Work is the factory's board of the project's items in four
  stage columns (stacked under the reading width), then its agents and its plan
  runs. Defaults edits the agent defaults and the factory defaults with the
  role inheritance shown on the row that follows it. Health reports the gate,
  the mirrors, the retained workspaces, the trust, the default workspace and the
  forge, and runs discovery into a report.
- **The run from a list.** A run a project page lists opens its own thread: the
  digest the list carried builds the run's lanes, graph and timeline, and the
  question it stopped on is answered through the app's `answerRunFor` seam.
- **The end-to-end.** `test/ui/factory/f6_2_e2e_test.dart` drives a plan through
  the planner's journal and a frozen snapshot, one review sheet, an approval
  posted over the fixture bridge, and the item the store opens as a work item's
  thread; and, separately, a run through its first commit, a gate that is
  answered, and the project page that lists it.

## Where it is wired

- `PlaceKind.project` draws `_ProjectPlace` through `Studio.projectFor(host,
  root)`; the sidebar's project row already opened that place.
- The board's item rows open the item's thread; an agent row opens the agent's
  thread; a run row opens the run's plan-run place, with the digest the list
  carried (`_RunPlace` and `RunModel.fromDigest`).
- The Automations tab links to the existing Automations place.
- The page watches its model with the shell's `UnsavedGuard` while it is
  mounted, so leaving an edited page asks first.
- `main.dart` wires `projectFor` from the host's client, filtering `/api/runs`
  to the project, and `answerRunFor` to `BridgeClient.answerRun`.

## Decisions this record makes

- **The screens take a projection, not the wire.** `ui/` may not import
  `client/` (`test/boundary_test.dart`), so the model exposes `ProjectRun`,
  `ProjectHealthView`, `ProjectMirror`, `ProjectWorkspaceHealth`,
  `DiscoveryView`/`DiscoveryRow` and `ProjectAutomations`, and the page and its
  tabs draw those. The board takes `WorkItemView` and the agents take
  `ThreadRef`, which the shell builds from the one factory store (F6 decision
  14).
- **`ProjectSettingsVersion` is a client type, not a contract one.** The ETag is
  a header, never part of the body, so the pair lives in `bridge_client.dart`
  beside the two routes that answer with it. The contract's declarations are
  today's JSON unchanged.
- **One inherited JSON tag was restored, not renamed.** The work inherited from
  the interrupted run declared `FactoryDefaults`'s poll interval as
  `json:"cipollSeconds"`, while the bridge's own declaration has always been
  `json:"ciPollSeconds"`. The bridge byte-compares replayed journals, so the
  tag was put back and both clients regenerated; the generated Dart and
  TypeScript keep the original key.
- **The run list route is the bridge's own.** `GET /api/runs` already answered
  with each agent's last digest; the client gained the two methods (list and
  answer) it lacked, and no route or shape changed.
- **The Health tab has no separate index row.** `ProjectHealth` carries no index
  field; what the bridge reports as the index's evidence — the mirrors'
  freshness and the retained workspaces — is what the tab draws. Inventing a row
  would state something the bridge never said.
- **The Automations tab is a summary and a link.** The place exists (F7), so the
  tab states what the project configured and offers the way there rather than
  embedding a second editor.
- **The end-to-end is a model-level flow test**, for the reason F6 plan 1
  recorded: `integration_test` needs a Linux device toolchain this machine does
  not have, and a widget test cannot host the flow, because a request posted
  from a tapped callback runs in the test's fake-async zone where the fixture
  bridge's real HTTP never completes. `test/ui/factory/f6_2_e2e_test.dart`
  drives both flows; the drawing and the actions are covered by
  `planning_test.dart`, `handoff_sheet_test.dart`, `run_model_test.dart`,
  `project_model_test.dart` and `project_page_test.dart`.

## Not done

- **Expected checks are not editable from the project page.** A
  `FactoryCheckKey` names a kind, a name and an app id, and the page has no
  forge route to read the third from; the handoff sheet is where checks are
  chosen today.
- **The factory role subagent overrides** (`implementerSubagents`,
  `reviewerSubagents`) and a factory default's `workspace`/`hostId` are
  preserved by a save but not surfaced: the page edits the fields the bridge's
  own project surface edits.
- **Model profiles are free text**, because the client has no models route:
  the row accepts a profile name and the bridge resolves it.
- **The mode picker offers `ask` and `act` only.** The record keeps any other
  stored value, but this build does not offer one.

## Environment notes

- `flutter test integration_test -d linux` and `flutter build linux` cannot run
  here (no CMake/GTK3/display). The flows are driven by widget and model tests
  instead, as above.
- `flutter test --platform chrome` cannot run here (no browser).
- The gallery goldens are Linux-pinned and unchanged by this plan (no kit
  component was added).
- `web/ui`'s `npm run check` has the pre-existing five errors and five warnings
  the task brief names, and no new ones.

## Verification

- `CGO_ENABLED=1 go test ./web/... -count=1` — passes, including the bridge's
  journal-replay tests, the contract fixture round trip and both generated
  clients' currency tests.
- `cd app && flutter analyze` — no issues.
- `cd app && flutter test` — passes, including the enforcement tests (design
  rules, tooltips, states, overflow, gallery goldens, boundary) and the
  project's model, page and end-to-end tests.
- `dart format --output=none --set-exit-if-changed lib test` — clean.
