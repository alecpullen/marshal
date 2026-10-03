# W4.5 · UI: network inspector, projects, secrets — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w4-workspaces-design.md`](../specs/2026-10-03-w4-workspaces-design.md) §8.3–§8.5
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Runs after:** W1–W3, and W4.1–W4.4.
**Base:** the branch once W4.4 is complete. Names from earlier phases are
as their plans define them.
**Plan slug:** `w4-5-ui-network-projects`. Commit each task as
`w4-5-ui-network-projects: task N — <title>`.

## Goal

This plan adds:

- the network inspector, per workspace and per agent;
- blocked-request decisions in the inbox;
- the project overview (`#projects/<root>`);
- settings for secrets, credentials, repos and vault-backed provider keys.

## Non-goals

- Automations on the project page (W5).

## Assumptions

These bridge routes exist:

| Plan | Routes |
|---|---|
| W4.3 | `GET /api/network`, `POST /api/network/decisions`, `/api/secrets…`, `/api/credentials…`, `/api/repos…`, the fleet delta `network_block` |
| W4.2 | `/api/projects/settings`, `/api/projects/health` |
| W3.2 | `config/get` exposure through `GET /api/models`, `PUT /api/models/providers/{name}/key` |

The UI from earlier plans:
- Home's Needs-you list and `PendingActions` (W1, W3.3);
- the W3.4 Providers page;
- `ProjectsPanel.svelte` (existing; mounted on `#projects`).

---

## Task 1: API client and `network_block` handling

**Goal:** typed functions for network, secrets, credentials, repos and
project settings, plus fleet handling of `network_block`.

**Files:**
- `web/ui/src/lib/api.ts`, `api.test.ts`
- `web/ui/src/lib/fleet.ts`, `fleet.test.ts`

**Steps:**

1. Types:
   - `NetHostRow`, `NetRecord`, `NetAgentRow`;
   - `SecretsStatus`, `CredentialRow` (with `set`), `RepoRow`;
   - `ProjectSettings`, `ProjectHealth`.

   Add functions for every route in the Assumptions table.
2. In `fleet.ts`, `network_block` deltas push onto a `decisions` list
   keyed `(agentId, host)`. A later delta with the same key replaces the
   earlier one. `decideNetwork(agentId, host, decision)` removes the item
   after posting.
3. Tests: the URLs, plus the decision de-duplication.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/api.test.ts src/lib/fleet.test.ts
```

---

## Task 2: Network inspector

**Goal:** `#workspaces/<name>/network` and `#network?agent=<id>` show the
Hosts, Requests and By agent views (spec §8.3).

**Files:**
- `web/ui/src/views/Network.svelte` (new), `Network.test.ts` (new)
- `web/ui/src/App.svelte`, `routes.ts`

**Steps:**

1. Routes:
   - `#network?agent=` and `#workspaces/<name>/network` render `Network`
     with `{workspace?, agent?}`;
   - the session header (W4.4's workspace tag) gains a "Network" link
     (`#network?agent=<id>`).
2. **Hosts view:** a table of host, rule (a `Tag`: allowlisted, granted,
   open, injected, blocked), requests, bytes up and down, agents, and
   last seen. An injected host shows the badge "proxy can read". The
   table sorts by any column.
3. **Requests view:** the last 500 records, with time, agent, host,
   decision, bytes and duration. A filter input matches on host.
4. **By agent view:** per-agent totals, linking to that agent's session.
5. **Process mode:** when the `hosts` response's `processMode` is true
   (W4.3 Task 8), show the banner
   "Not isolated: agents run as processes, so the proxy is advisory".
6. Refresh every 10s while the page is open.
7. Tests: each view renders from fixtures; sorting; the injected badge;
   the banner.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Network.test.ts && npx svelte-check
```

---

## Task 3: Blocked-request decisions in the inbox

**Goal:** blocked requests appear in Home's Needs you, and on Live wall
tiles, with Block, Allow for this agent, and Add to workspace.

**Files:**
- `web/ui/src/views/Home.svelte`, `Home.test.ts`
- `web/ui/src/lib/inbox/NetworkDecision.svelte` (new)
- `web/ui/src/lib/live/Tile.svelte`

**Steps:**

1. `NetworkDecision.svelte`:
   - Shows "<agent> tried to reach <host>" with the workspace name, and
     three buttons that call `decideNetwork`.
   - For a Studio template, `add-to-workspace` shows a toast: "Added to
     <workspace> draft", with a link to the designer.
   - For a repo template, it opens a modal with the returned patch and a
     **Copy** button.
2. Home's Needs-you section adds the `decisions` list, oldest first.
3. Live wall: a tile whose agent has a pending decision gets the `warn`
   tint and shows the decision inline.
4. Tests:
   - a delta shows a decision;
   - each button posts the right body;
   - the repo-patch modal opens;
   - the tile tint appears.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Home.test.ts src/lib/inbox && npx svelte-check
```

---

## Task 4: Project overview

**Goal:** `#projects/<root>` shows defaults, intake, health, policy and
the session-sheet tab (spec §8.4).

**Files:**
- `web/ui/src/views/Project.svelte` (new), `Project.test.ts` (new)
- `web/ui/src/lib/ProjectsPanel.svelte` (rows link to the overview)
- `web/ui/src/App.svelte`, `routes.ts`

**Steps:**

1. `#projects/<encoded root>` renders `Project`. In `ProjectsPanel`, each
   project row becomes a link; its existing behaviour stays intact
   (`ProjectsPanel.test.ts` must pass).
2. **Defaults card** (`GET`/`PUT /api/projects/settings`):
   - workspace: a select over `GET /api/workspaces`;
   - model: the W3.4 routing chip component, reused;
   - mode;
   - isolation;
   - ship target.
3. **Intake card:** a repo select (`GET /api/repos`), labels as chips,
   and allowed MCP clients (from `listClients`) as checkboxes.
4. **Health card:** `GET /api/projects/health`, a status dot per check,
   and a **Refresh** button.
5. **Policy card:** the default workspace's `[policy]`, read-only, with
   an "Edit in designer" link.
6. **Session sheet tab:** the most recent agent of the project (from the
   fleet store), showing its telemetry sections: context %, changed
   files, tool stats and rules. `session_telemetry` carries these; W1's
   stack store already sees that event. Store the last telemetry per
   agent in `fleet.ts` from the `telemetry` delta. This tab is
   read-only.
7. Tests: the settings save posts the right body; health renders; the
   project link from `ProjectsPanel` works.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Project.test.ts src/lib/ProjectsPanel.test.ts && npx svelte-check
```

---

## Task 5: Settings — secrets, credentials, repos, vault-backed provider keys

**Goal:** the Settings sub-tabs Secrets, Credentials and Repos, plus
"Store in vault" on provider keys (spec §8.5).

**Files:**
- `web/ui/src/views/settings/{Secrets,Credentials,Repos}.svelte` (new), tests
- `web/ui/src/views/settings/Providers.svelte` (W3.4)

**Steps:**

1. **Secrets:**
   - backend status (`GET /api/secrets/status`): backend name and
     health;
   - a list of refs (`GET /api/secrets?prefix=`), with **Set** (a
     password input; `PUT`) and **Delete** (confirm);
   - values are never displayed;
   - with the `env` backend, Set is disabled with "Configure the local or
     OpenBao backend to store secrets".
2. **Credentials:**
   - a table of id, kind, ref or env var, user, and set state;
   - an add form whose kind select (pat, ssh, vault) reveals the right
     fields;
   - delete is refused with the server's message when a repo uses the
     credential.
3. **Repos:** a table and a form: id, URL, branch, forge (github or
   gitea), API base, credential (select), watch and label.
4. **Provider keys:** in W3.4's `Providers.svelte` **Set key** dialog,
   add "Store in vault (recommended)", checked by default when the
   backend isn't `env`.
   - **Checked:** `PUT /api/secrets/providers/<name>`, then save the
     provider with `api_key_env` cleared, through `PUT /api/models/providers`.
     The card shows "key: vault (injected by proxy)".
   - **Unchecked:** the W3.4 path (`PUT …/providers/{name}/key`).
5. Settings sub-tabs: Models, Providers, Secrets, Credentials, Repos,
   Tokens.
6. Tests:
   - values never render after save;
   - the env backend disables Set;
   - the credential form fields per kind;
   - a repo posts;
   - the vault option calls the secrets route, not the key route.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/settings && npx svelte-check
```

---

## Task 6: Rebuild static

**Goal:** the committed bundle includes W4.5.

**Steps:**

1. Run `cd web/ui && npm run build`.
2. Commit `web/bridge/static`.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../bridge && go test ./ -run 'TestAssets|TestWebIsStdlibOnly'
```

Check by hand:
- block a host, then decide "Allow for this agent";
- open the network inspector;
- set project defaults and spawn without choosing anything;
- add a vault credential and a repo, then push from a git-sourced agent.

---

## Final verification

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../.. && CGO_ENABLED=1 go test ./... ; go vet ./... ; gofmt -l .
```

Expected: the UI is clean, and Go fails only on the five known tests.

## Integration notes

- Every rail entry is now enabled.
- The `processMode` flag comes from W4.3 Task 8's `hosts` response.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. |
| Anchors verified? | `ProjectsPanel.svelte` and its test exist on `2ddc09e`. Other names are from W1–W4.4 as defined. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §8.3–§8.5. |
