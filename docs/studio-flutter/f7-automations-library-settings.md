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
  report; adding one is a planned row.
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
