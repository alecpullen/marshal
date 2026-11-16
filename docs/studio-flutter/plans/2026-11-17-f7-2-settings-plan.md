# F7 plan 2: Settings — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: branch `f0-foundations` at `52221495`. Runs after
[F7 plan 1](2026-11-17-f7-1-automations-library-plan.md).

Approved design:
[f7-automations-library-settings.md](../f7-automations-library-settings.md),
decisions 6–9. Rules: [design-system.md](../design-system.md).

**How this plan is written.** No implementation code; Contracts holds
declarations only. Re-read the handlers named below at the current `main`
before Task 1.

## Goal

- Settings, with its ten sections, on `SectionPage`.
- The environment designer.
- A route-parity list showing nothing in the Svelte client is unmapped.

## Non-goals

- Host onboarding. Multiple users.

## Assumptions

- Routes: `/api/repos`, `/api/credentials`; `/api/models` with
  `providers`, `providers/{name}/key`, `presets`, `routing`, `probe`;
  `/api/secrets` with `status` and `{ref...}`; `/api/network` with
  `pending` and `decisions`; `/api/workspaces` with `{name}`, `draft`,
  `builds`, `builds/{n}/events`, `publish`, `pool`, `patch`, `diff`,
  `shell`, `ca/rotate`; `/api/usage`, `/api/audit`, `/api/disk`,
  `/api/prune`, `/api/budgets`; `/api/notifications` with `test`;
  `/api/status-links`; `/api/daemon`; `/api/config`.
- The Svelte views under `views/settings/`, `views/workspaces/`,
  `views/Network.svelte`, `views/Usage.svelte`, `lib/DiskPanel.svelte`,
  `lib/StatusShare.svelte`.
- F1's `HostRegistry` lists hosts.

## Decisions this plan makes beyond the spec

- The designer's source view is a monospace `Field` with validation from
  the bridge on save, not a code editor.
- Access links to Library's MCP tab for clients; it does not list them
  twice.
- The usage chart is one kit component, `Chart`: bars or one line, `fg`
  for a single series, at most `ok`, `warn`, `err` otherwise, no legend
  beyond direct labels.

## Execution

- Branch `f7-2-settings`. Commit each task as
  `f7-2-settings: task N — <title>`, explicit paths only.

## Contracts

Contract files, each added by the task that needs it: `connections.go`,
`models.go`, `secrets.go`, `network.go`, `environments.go`, `usage.go`,
`notifications.go`, `statuslinks.go`, declaring with today's JSON what
`lib/api.ts` hand-writes for these routes.

```dart
// model/environment_model.dart
enum LayerKind { base, toolchains, packages, mounts, files, secrets, network, resources, setup }
class Layer { LayerKind kind; String summary; int sourceStart; int sourceEnd; }
class EnvironmentModel extends ChangeNotifier {
  String name; String source; List<Layer> layers; LayerKind? selected;
  void editSource(String next); void editLayer(LayerKind kind, Object value);
  Future<void> saveDraft(); Future<int> build(); Stream<String> buildLog(int n);
  Future<void> publish();
}
```

`AskKind` gains `network`. Settings' sections, in order: hosts,
environments, connections, models, secrets, network, usage, notifications,
access, general.

## Task 1 — the Settings frame; hosts and general

**Files:** `app/lib/src/ui/settings/settings_place.dart`,
`hosts_section.dart`, `general_section.dart`, `placeholder.dart`, tests,
`screens.dart`.

1. A two-pane place: sections on the left (a list above the detail under
   760), the section on the right. Sections not built yet show the planned
   state.
2. Hosts: each host from `HostRegistry` with kind, reach and last contact;
   "Add a host" is a planned row. General: appearance (system, light,
   dark), default transcript density, the keyboard map, and the local
   daemon's state from the tray's status document.
3. Tests: `sections in the specified order`, `an unreachable host shows
   when it was last seen`, `add a host is planned`,
   `appearance changes the theme`.

**Verify:** `cd app && flutter test`.

## Task 2 — connections; models and routing

**Files:** `web/bridge/contract/connections.go`, `models.go`, fixtures,
generated files, two models, `connections_section.dart`,
`models_section.dart`, tests.

1. Connections: forges with credential kind and what it permits — an SSH
   credential says it can push but cannot open pull requests or read
   checks — and providers with health and Probe.
2. Models and routing: role to model, presets, budgets and the behaviour at
   the cap. A line states that these save to the user's global config. A
   provider key is write-only.
3. Tests: `an ssh credential states its limits`, `probe reports health`,
   `routing saves to the global config`, `a key cannot be read back`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 3 — secrets and network

**Files:** `web/bridge/contract/secrets.go`, `network.go`, fixtures,
generated files, two models, `secrets_section.dart`,
`network_section.dart`, `inbox_model.dart`, tests.

1. Secrets: references and where each is used; a value can be written and
   never read.
2. Network: hosts with rule, request count and last seen, filtered by
   environment and agent. A pending request is an ask of kind `network`
   with Allow for this agent, Add to the environment, Block; it appears in
   the Inbox and in its thread.
3. Tests: `a secret value is never rendered`,
   `a pending request is an ask`, one per network decision posting the
   right choice.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 4 — environments: list and builds

**Files:** `web/bridge/contract/environments.go`, fixtures, generated
files, `app/lib/src/model/environments_model.dart`,
`environments_section.dart`, `builds_view.dart`, tests.

1. The gallery as `Rows`: name, contents in one line, version and how many
   projects use it. "Import devcontainer.json" starts a draft.
2. A template's builds with their state; a build's log streams from its
   event route.
3. Tests: `rows show version and use`, `import starts a draft`,
   `a build log streams`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 5 — the environment designer

**Files:** `app/lib/src/model/environment_model.dart`,
`app/lib/src/ui/settings/environment_designer.dart`, `layer_list.dart`,
tests.

1. Layers on the left, source on the right; below 1080, a switch between
   them. Selecting a layer highlights its source range; editing either side
   updates the other.
2. Save draft, Build (log streams), Publish. The side panel shows size,
   start times, the warm pool and what the gate can run.
3. Tests: `selecting a layer selects its source range`,
   `editing source updates the layers`,
   `editing a layer rewrites only its range`,
   `publish is disabled until a build passes`.

**Verify:** `cd app && flutter test`.

## Task 6 — usage, audit, disk

**Files:** `docs/studio-flutter/design-system.md`,
`app/lib/src/ui/kit/chart.dart`, gallery, goldens,
`web/bridge/contract/usage.go`, fixtures, generated files, a model,
`usage_section.dart`, tests.

1. Add `Chart` to the kit by §10.
2. Cost and tokens by project, role and model over a range; the audit log
   as rows with filter; disk with Prune, which confirms and names what is
   removed.
3. Overview's spend and capacity cards link here.
4. Tests: `grouping changes the series`, `audit filters`,
   `prune confirms and names what goes`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 7 — notifications and access

**Files:** `web/bridge/contract/notifications.go`, `statuslinks.go`,
fixtures, generated files, two models, `notifications_section.dart`,
`access_section.dart`, `app/lib/src/ui/thread/thread_header.dart`, tests.

1. Notifications: where Marshal reaches the user, with Send a test; Signal
   and Element are planned rows.
2. Access: the Studio token, a link to Library's MCP tab, and status links
   (list, create, revoke). Share on the thread header now creates a status
   link and offers to copy it.
3. Tests: `send a test posts`, `revoke confirms`,
   `share creates a link for the thread`.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1`; `cd app && flutter
test`.

## Task 8 — route parity, record, full verification

**Files:** `docs/studio-flutter/route-parity.md` (new),
`app/test/route_parity_test.dart` (new), the spec.

1. `route-parity.md` is a table of every method and path the Svelte
   client's `lib/api.ts`, `lib/factory.ts`, `lib/planner.ts`,
   `lib/working-drafts.ts` and `lib/sse.ts` call, each with the
   `BridgeClient` method that calls it, or "dropped" and why.
2. `route_parity_test.dart` reads the table and fails when a row has
   neither, and when a named `BridgeClient` method does not exist in
   `bridge_client.dart`.
3. Implementation record.

**Verify:** `cd app && flutter analyze && flutter test && dart format
--output=none --set-exit-if-changed lib test`; `CGO_ENABLED=1 go test
./web/... -count=1`; `cd web/ui && npm run check && npm test`.

## Done when

- The parity table has no unmapped route.
- A secret's value cannot be read back anywhere.
- An environment is imported, edited in either view, built, published and
  used by a new agent.

## What later plans rely on

- `route-parity.md`, which F8 requires to be complete.
