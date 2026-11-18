# F7 — Automations, Library and Settings

Status: **draft for approval**, 2026-11-17. Authored against `main` at
`f2ea2717`, ahead of F0–F6. Part of [the Studio programme](programme.md).

Read first: [design.md](design.md) §3, [design-system.md](design-system.md),
`docs/web-studio/design.md` §8.8–§8.15, the W3–W5 specs under
`docs/web-studio/specs/`, and in the Svelte client: `views/Watches.svelte`,
`views/Schedules.svelte`, `views/automations/`, `views/Library.svelte`,
`lib/library/`, `views/Settings.svelte`, `views/settings/`,
`views/workspaces/`, `views/Network.svelte`, `views/Usage.svelte`,
`lib/ClientsPanel.svelte`, `lib/DiskPanel.svelte`, `lib/StatusShare.svelte`,
with their tests.

**Before starting.** F2 has landed. This phase does not depend on F3–F6 and
may run beside them. Re-read the files above at the current `main`.

## Context

These three places hold everything that is not a piece of work: what starts
work without the user, what agents know and can use, and where work runs
and what it may touch. In the Svelte Studio they are a dozen pages. Here
they are three, each a set of tabs or sections over one pattern: a list of
rows that opens a detail.

Routes, by area:

- Automations: `/api/automations/review/…` (run, drafts, post,
  send-to-author, discard), `/api/automations/ci/history`,
  `/api/schedules`, `/api/watches` and `/api/reroutes/{id}/undo`,
  `/api/recipes`.
- Library: `/api/library/memory` with suggestions, confidence and promote
  (`web/bridge/library.go`); `/api/library/skills` with its preview,
  confirm and discard steps, and the plugin and MCP calls beside it in
  `lib/api.ts`; `/api/clients` with factory grants.
- Settings: `/api/daemon`; `/api/workspaces/…` (templates, drafts, builds
  with their event stream, publish, pool, shell, certificate rotation);
  `/api/repos`, `/api/credentials`, `/api/models/…` (providers, keys,
  presets, routing, probe); `/api/secrets`; `/api/network` with pending and
  decisions; `/api/usage`, `/api/audit`, `/api/disk`, `/api/prune`,
  `/api/budgets`; `/api/notifications`; `/api/status-links`; `/api/config`.

Two rules from the engine hold in every editing surface: providers and
model presets are saved to the user's global config only, and a secret's
value is never shown after it is written.

## Goal

- Nothing in the Svelte Studio lacks a home in the app.
- Every list here looks and behaves the same way.

## Non-goals

- New capabilities. A thing the bridge does not do is a disabled row.
- Host onboarding and the richer connection flows from the factory
  designs. Hosts lists what `marshal fleet servers` and the local daemon
  report; in a native build P8 plan 2 made adding, removing and signing in
  real, and a browser's add row stays planned (D22).
- Multiple users.

## Decisions

1. **Contract in slices**, as in F6: each area's types move into
   `web/bridge/contract` with the slice that builds it.
2. **One pattern.** Every section is `Rows` — name, one line of
   description, current value — and choosing a row opens its detail in a
   `Sheet` on desktop and a full page on the narrow layout. A section may
   add one primary action in its page header. No section invents another
   layout; the two exceptions are named in decisions 6 and 8.
3. **Saving.** Edits are explicit: a detail has Save and Cancel, shows
   pending while saving, and shows the bridge's refusal in place. A save
   the bridge rejects as stale keeps the user's edit and offers a reload.
   Leaving with unsaved edits asks first.
4. **Automations** has four tabs.
   - Bots: the review bot and CI fixer per project, each with its settings
     and recent history; review drafts open with Post, Edit, Send to the
     author's agent, and Discard, and a held draft becomes an `AskCard`
     kind in the Inbox. The CI fixer's page states its fixed
     rule — it never deletes, skips or disables a test — as text, not as a
     setting. Automatic fixing from F5 is set here too.
   - Schedules: list, create, edit, run now, delete.
   - Watches: source, condition, a day's `Spark`, the action on trip and
     the state; a tripped watch's detail shows what it did and Undo where
     the action was a reroute. Watches an agent started are listed and
     name the agent.
   - Recipes: built-in and the user's, with copy, edit and run.
5. **Library** has four tabs: Skills, Plugins, MCP, Memory. MCP lists
   servers and clients; a client's detail holds its token, its confirmation
   rule and its factory grants per project. Memory shows scope, provenance
   and confidence; a suggested promotion to a wider scope is an action the
   user approves, never automatic.
6. **Environments** is the first exception to the one pattern. Its list is
   `Rows`; its detail is the designer: layers on the left — base image,
   toolchains, packages, mounts, shared files, secrets, network, resources,
   setup — and the source on the right, two views of one document that stay
   in step. A draft is built before it is published; a build's log
   streams; the side panel shows size, start times, the warm pool and what
   the gate can run. Importing a `devcontainer.json` starts a draft.
7. **Settings sections**, in this order: Hosts, Environments, Connections,
   Models and routing, Secrets, Network, Usage and audit, Notifications,
   Access, General.
   - Connections: forges with the credential's kind and what it permits,
     and model providers with health. An SSH-only forge credential says
     plainly that it can push but cannot open pull requests or read checks.
   - Models and routing: role to model, presets, budgets and what happens
     at the cap. A note states these save to the user's global config.
   - Secrets: references and where each is used; values write-only.
   - Network: hosts with rule, request count and last seen, filterable by
     environment and agent. A pending request (`/api/network/pending`)
     becomes an `AskCard` kind in this phase — allow for this agent, add to
     the environment, block — and appears in the Inbox and in its thread.
   - Usage and audit: cost and tokens by project, role and model; the audit
     log; disk with prune, which confirms and names what is removed.
   - Access: the Studio token, MCP clients (the same list as Library's),
     and status links. Share on a thread's header, drawn disabled since F3,
     now creates a status link.
   - General: appearance, transcript density default, the keyboard map, and
     the local daemon's state with the tray's own commands.
8. **Usage** is the second exception: its detail may use a chart. The kit
   gains one chart component by the design system's §10, with `fg` for a
   single series and no more than the three status colours otherwise.
9. **Planned rows.** Where a designed capability does not exist — adding a
   host, Signal and Element notifications, the workflow editor — the row is
   drawn disabled with one line saying it is planned.

## Work

Suggested pull requests, in order. Each begins with its contract types.

1. **The pattern.** `Rows` detail-in-sheet, the save and stale-save
   behaviour, and unsaved-edit guarding, as shared code with tests.
2. **Automations: schedules and recipes.**
3. **Automations: watches.**
4. **Automations: bots.** Including review drafts.
5. **Library.**
6. **Settings: connections, models and routing.**
7. **Settings: secrets and network.**
8. **Settings: environments list and builds.**
9. **Settings: the environment designer.**
10. **Settings: usage, audit and disk.** With the chart component.
11. **Settings: hosts, notifications, access, general.** Including status
    links from the thread header.

## Tests

- `web/bridge/contract`: fixtures per moved type.
- `app/test/model/`: one model and test per area; in particular the
  stale-save rule, write-only secrets, global-only model saves, the
  promotion approval, and the layer-and-source synchronisation in the
  designer.
- `app/test/ui/`: one file per pull request above, each registering with
  the tooltip, states and overflow tests.
- A parity check: a checked-in list of every route the Svelte client calls,
  each mapped to the Dart method that calls it or marked deliberately
  dropped with a reason. A test fails if a route is in neither column.

## Done when

- The parity list has no unmapped route.
- Every section handles loading, empty, stale, error and unavailable.
- A secret's value cannot be read back anywhere in the app.
- A provider or preset edited from a project's context is saved to the
  user's global config.
- An environment can be imported, edited in either view, built, published
  and used by a new agent.

## Open questions

- Whether the environment designer's source view needs a real code editor
  or a plain monospace field with validation. Recommended: the plain field.
- Whether Access should show the MCP clients or only link to Library, to
  avoid one list in two places.

## Implementation record (F7 plan 1, 2026-11-17)

Branch `studio/f7-1-automations-library`, off `studio/f2-shell`. Every task
landed as one commit, `f7-1-automations-library: task N — <title>`.

### What landed

| Task | What it is | Where |
|---|---|---|
| 1 | The list pattern: `SectionModel` (save, stale save with reload, unsaved edits), `SectionPage` (a `PageBody`, optional tabs, `Rows`, the detail in a `Sheet` at 760 and a pushed page below), and the one guard the shell consults | `app/lib/src/model/section_model.dart`, `app/lib/src/ui/sections/{section_page,detail_host,unsaved_guard}.dart`, `app/lib/src/ui/shell/shell.dart` |
| 2 | Schedules and recipes: their contract slices, both generated clients, their typed routes, their models, and the Automations place (Bots, Schedules, Watches, Recipes) | `web/bridge/contract/{schedules,recipes}.go`, `app/lib/src/model/{schedules,recipes}_model.dart`, `app/lib/src/ui/automations/*` |
| 3 | Watches: the contract slice, the model, the tab (with its day's `Spark`), and the Overview's Automations card for the watches that tripped | `web/bridge/contract/watches.go`, `app/lib/src/model/watches_model.dart`, `app/lib/src/ui/automations/watches_tab.dart`, `app/lib/src/ui/overview/overview.dart` |
| 4 | Bots: the contract slice, the model, the two bots per project, the held review with Post / Edit / Send to the author's agent / Discard, and `review_draft` asks in the Inbox | `web/bridge/contract/automations.go`, `app/lib/src/model/{bots_model,inbox_model}.dart`, `app/lib/src/ui/automations/{bots_tab,review_draft_sheet}.dart` |
| 5 | Library: the contract slices, both models, the place and its four tabs — Skills with preview and confirm, Plugins with scan and confirm, MCP with clients, their tokens and their factory grants, Memory with provenance, confidence and the approved promotion | `web/bridge/contract/{library,clients}.go`, `app/lib/src/model/{library,clients}_model.dart`, `app/lib/src/ui/library/*` |
| 6 | This record and the full verification | `docs/studio-flutter/f7-automations-library-settings.md` |

Every list is a `SectionPage`: Bots, Schedules, Watches and Recipes in
Automations; Skills, Plugins, MCP and Memory in Library. Each handles
loading, empty, stale, error and unavailable, and each is registered in
`app/test/support/screens.dart` with all five.

### Verification

| Task | Verify command | Result |
|---|---|---|
| 1 | `cd app && flutter test` | **pass** (614) |
| 2 | `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter test` | go **pass**; flutter **pass** (738) |
| 3 | `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter test` | go **pass**; flutter **pass** (802) |
| 4 | `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter test` | go **pass**; flutter **pass** (867) |
| 5 | `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter test` | go **pass**; flutter **pass** (1111) |
| 6 | `cd app && flutter analyze && flutter test && dart format --output=none --set-exit-if-changed lib test`; `CGO_ENABLED=1 go test ./web/... -count=1`; `cd web/ui && npm run check && npm test` | analyze **pass** (no issues), test **pass** (1111), format **pass** (0 files changed); go **pass**; `npm run check` reports **5 errors and 5 warnings, unchanged from base** — the errors in `fleet.test.ts`, `AgentConversation.svelte`, `Run.svelte`, `Review.test.ts` and `Runs.test.ts`, the warnings in `IssuePicker.svelte:7`, `FactoryNew.svelte:265`, `DraftApproval.svelte:87`, `WorkingDraftConflict.svelte:90` and `WorkingDrafts.svelte:87` (Svelte-5 initial-value reactivity), all pre-existing and untouched by this branch, which changes nothing under `web/ui/src` but `lib/contract.ts`; `npm test` **pass** (120 files, 1355 tests) |

The currency tests for both generators pass without `-update`
(`TestDartContractIsCurrent`, `TestTypeScriptContractIsCurrent`), so
`app/lib/src/client/contract.g.dart` and `web/ui/src/lib/contract.ts` are
current.

Environment-blocked, not failures: `flutter test --platform chrome` (no
working browser host on this machine), `flutter build linux --debug` (no
CMake/GTK3 development headers), and the `npm run check` set above, which
predates this branch.

### Blocked

- **Undo a trip's reroute.** `POST /api/reroutes/{id}/undo` takes the id of
  the reroute record a trip made, and the wire now carries it: a watch row
  with a reroute still to undo holds `lastReroute` (`{ "id", "at" }`,
  `contract.LastReroute`), which the bridge's merge fills in from its
  in-memory reroute log while the record exists and an undo is still
  possible. `internal/acp/watch.go` is untouched: the record is the bridge's,
  so the merge adds it as the row is listed. An untripped watch, one whose
  record is gone (a bridge restart), or one already undone has no
  `lastReroute`, and the detail keeps `Undo the reroute` disabled with a line
  saying there is nothing to undo. When it is present the button is enabled
  and wired to `WatchesModel.undoReroute`, pinned by
  `the detail undoes the reroute the watch carries` and
  `the undo stays disabled for a watch with no reroute record`
  (`app/test/ui/watches_test.dart`), and by
  `TestWatchesListCarriesTheRerouteItsUndoNeeds` (`web/bridge/watches_test.go`),
  which follows the listed id through the undo route.
- **Task 4's automatic-fixing switch.** F5 plan 1 is not in this branch, so
  the CI fixer's detail draws the planned row the plan asks for. It is a
  one-line swap for the switch when F5 lands.

### Decisions this plan left open

- **Bot settings are read-only here.** `/api/projects/settings` carries the
  whole project document, so the contract adds `ProjectAutomations`: the one
  key these screens read. Writing half that document (and its version) is
  the settings slice's job, not this one's.
- **`MCPClient` omits `tokenHash`.** The bridge's struct sends it and
  `lib/api.ts` never declares it; a hash has no place in a UI contract, and
  the token itself is only ever in `CreateClientResult`. `Schedule` keeps
  `ownerId`, which the wire does send.
- **`ApprovalAuthority` is an enum** mirroring the wire's `none`,
  `user_directed` and `standing`, so a grant's authority is not a free
  string in Dart.
- **The review draft opens inside the bot's sheet**, not in a second one
  (§5: one sheet per screen). `review_draft_sheet.dart` holds that view,
  including its in-place delete confirmation.
- **`model/AskKind.reviewDraft` draws as the kit's existing
  `AskKind.review`** ("Review draft"); the design system gains no kind.
- **Editing a draft edits its summary and its findings' titles.** The
  bridge's PUT accepts bodies too; a finding-by-finding prose editor is a
  later surface, and the section says what it does.
- **Kit additions (§10 in one commit each, with the document, the gallery
  specimen and regenerated Linux goldens):** `Field.multiline` (a prompt's
  prose), `RowItem.spark` with `RowSpark` (a watch's day), and
  `SectionPage`'s `createLabel`/`blank`, `readOnly` and `header`/`footer`.
  `ConfirmAction` lives in `ui/automations/` rather than the kit: it is the
  destructive confirmation two sections share, built only from kit pieces.
- **`SectionModel` grew `store` and `replaceItems`** so a section action can
  put what the bridge answered where it belongs (a copied recipe, a posted
  draft, a bot whose held reviews changed) without reaching into the list.
- **Fixture constructors.** Every section model has a `.fixture(rows)`
  constructor, so the gallery and the enforcement tests draw real sections
  with no bridge; a write on one throws rather than pretending.
- **The places are built but not yet reachable from the shell.**
  `AutomationsPlace` and `LibraryPlace` are real screens, registered and
  held to §7/§9/§11, and `Studio` now owns the one unsaved-edit guard the
  shell consults, but `Studio._content` still draws the planned place for
  both. Wiring them needs a per-host `BridgeClient` the composition root
  does not have: `FleetStore` keeps `owner.client` private and `HostView`
  deliberately omits it, so exposing one means changing
  `app/lib/src/model/fleet_store.dart`, which is outside this plan's files.
  Their tabs take the state the shell knows, so the wiring is one
  constructor each once that provider exists.
- **Tests drive a scripted in-process `Transport`** for widget tests: the
  widget binding answers every real HTTP request with 400, so `FakeBridge`
  stays where it works (model-level `test()` cases) and a widget test
  scripts the routes it expects.


## Implementation record (F7 plan 2, 2026-11-17)

Branch `studio/f7-2-settings`, off `studio/f7-1-automations-library`. Every
task landed as one commit, `f7-2-settings: task N — <title>`.

### What landed

| Task | What it is | Where |
|---|---|---|
| 1 | The Settings frame: the ten sections in the design's order, the section list beside the open one (above it under 760), the planned state for a section this step does not build, and appearance taking effect through `EmberTheme`. Hosts (discovered, read-only, "Add a host" planned) and General (appearance, transcript density, the keyboard map and the local daemon's document) | `app/lib/src/ui/settings/{settings_place,placeholder,hosts_section,general_section}.dart`, `app/lib/src/model/{hosts,general}_model.dart` |
| 2 | Connections (forges with what their credential permits, providers with health and Probe) and Models and routing (roles, presets, the caps and what happens at them, write-only provider keys), on the transcribed `connections.go` and `models.go` | `web/bridge/contract/{connections,models}.go`, `app/lib/src/model/{connections,models}_model.dart`, `app/lib/src/ui/settings/{connections,models}_section.dart` |
| 3 | Secrets (references, where each is used, a value written and never read) and Network (hosts with rule, requests and last seen, filtered by environment and agent). A blocked request is an ask of kind `network`, answered Allow for this agent / Add to the environment / Block | `web/bridge/contract/{secrets,network}.go`, `app/lib/src/model/{secrets,network}_model.dart`, `app/lib/src/ui/settings/{secrets,network}_section.dart`, `app/lib/src/model/inbox_model.dart` |
| 4 | The environment gallery as rows (contents in one line, version, use), the import that starts a draft, and a template's builds with a log streamed from its event route | `web/bridge/contract/environments.go`, `app/lib/src/model/environments_model.dart`, `app/lib/src/ui/settings/{environments_section,builds_view}.dart` |
| 5 | The designer: layers and source as two views of one document, a chosen layer selecting its range, one layer rewritten without touching another, Save draft, Build with its log, Publish gated on a passing build, and the panel's size, start times, warm pool and gate | `app/lib/src/model/environment_model.dart`, `app/lib/src/ui/settings/{environment_designer,layer_list}.dart`, `Field.mono` in the kit |
| 6 | Usage, the audit log and the disk. The kit gains `Chart` by the design system's §10 (bars, `fg` for one series, at most `ok`/`warn`/`err`, no legend beyond direct labels), with the gallery, its goldens and the §10 entry. Prune confirms and names what goes | `app/lib/src/ui/kit/chart.dart`, `docs/studio-flutter/design-system.md`, `web/bridge/contract/usage.go`, `app/lib/src/model/usage_model.dart`, `app/lib/src/ui/settings/usage_section.dart` |
| 7 | Notifications (webhooks and Send a test, Signal and Element planned) and Access (the Studio token, where the MCP clients are, status links listed, made and revoked). Share on a thread's header now has a status link to hand out | `web/bridge/contract/{notifications,statuslinks}.go`, `app/lib/src/model/{notifications,access}_model.dart`, `app/lib/src/ui/settings/{notifications,access}_section.dart` |
| 8 | The route-parity table and its test, and this record | `docs/studio-flutter/route-parity.md`, `app/test/route_parity_test.dart` |

Every section is a `SectionPage` and is registered in
`app/test/support/screens.dart` with all five states (the designer is
registered as its own screen). The two exceptions the design allows are the
designer (decision 6) and the chart above Usage's rows (decision 8).

### Choices this plan left open

- **Hosts and General are `SectionModel`s like every other section.** The
  plan's one pattern applies to all ten, so a read-only section still says
  what a row is and what a save would write (nothing, in these two).
- **General's appearance and density are this window's.** The bridge's
  `/api/config` route is not mapped in this build, so the section says so
  rather than implying it stored the choice. The daemon row reads
  `GET /api/daemon`, the same document the tray parses.
- **The designer's layers are read from the source, not from the bridge's
  parse.** One local scan of the table headers keeps the two views in step on
  every keystroke; a save hands the source to the bridge, whose diagnostics
  replace the scan's silence. The plan's decision that the source is a
  monospace `Field` needed one kit addition (`Field.mono`).
- **A table written more than once is one layer, one range per block.** The
  scan keeps every `[[mounts]]` header, not only the first: a layer owns each
  block its table is written as, `textOf` reads them in the document's order,
  and an edit is written back at the first block and takes the rest with it.
  The source view can select only one span, so a table declared in several
  places selects from its first line to its last.
- **Access points at Library's MCP tab with a row, not a link that navigates.**
  Settings is not wired to the shell's `PlaceNavigation` yet (F7 plan 1 left
  Automations and Library in the same position), so the row names where the
  clients are and what they hold.
- **`Chart` is bars only.** The plan allowed bars or one line; a line needs a
  scale and axis furniture the kit has no tokens for, so the one series is a
  row of labelled bars.
- **Two `dartgen` bugs were fixed with fixture rules.** A map of structs
  encoded its values from an undeclared `value` instead of `entry.value`, and
  a field whose name is a Dart keyword (`extends`) was rejected rather than
  taking a trailing underscore. Both are pinned by
  `web/bridge/contract/tsgen/testdata/rules` and its generated modules.

### Verification

| Task | Verify command | Result |
|---|---|---|
| 1 | `cd app && flutter test` | **pass** (1863) |
| 2 | `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter test` | go **pass**; flutter **pass** (1987) |
| 3 | `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter test` | go **pass**; flutter **pass** (2110) |
| 4 | `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter test` | go **pass**; flutter **pass** (2173) |
| 5 | `cd app && flutter test` | **pass** (2237) |
| 6 | `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter test` | go **pass**; flutter **pass** (2300) |
| 7 | `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter test` | go **pass**; flutter **pass** (2424) |
| 8 | `cd app && flutter analyze && flutter test && dart format --output=none --set-exit-if-changed lib test`; `CGO_ENABLED=1 go test ./web/... -count=1`; `cd web/ui && npm run check && npm test` | see below |

`flutter test --platform chrome`, `flutter test integration_test -d linux` and
`flutter build linux --debug` are environment-blocked on this host: there is
no browser, no display and no CMake/GTK3. Two `dartgen` fixes are described
above; they were needed to express the contract in Dart, not to quiet a test.

### Unfinished, and what later plans rely on

- Settings is not yet wired into the shell's place dispatch: the sections are
  built and registered, and `PlaceKind.settings` still draws the planned
  message until the shell passes a `SettingsPlace` its navigation. The
  designer's and Environments' data come from the bridge, so the wiring is the
  remaining step.
- Access's pointer to Library's MCP tab becomes a real navigation when that
  seam exists.
- Share on a thread's header has its status link (`AccessModel.share`); the
  shell passes the callback when it builds the thread place's header.
- `route-parity.md` is the table F8 requires to be complete.
