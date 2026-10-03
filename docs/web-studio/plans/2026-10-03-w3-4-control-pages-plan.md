# W3.4 · Library, Models, Usage, Watches — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w3-runs-and-control-design.md`](../specs/2026-10-03-w3-runs-and-control-design.md) §6.1, §6.4–§6.9
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Runs after:** W1, W2, [W3.1](2026-10-03-w3-1-engine-plan.md), [W3.2](2026-10-03-w3-2-bridge-control-plan.md) and [W3.3](2026-10-03-w3-3-runs-and-live-plan.md).
**Base:** the branch once W3.3 is complete. Code from before W1 was checked
on `2ddc09e`. Later names are as their plans define them.
**Plan slug:** `w3-4-control-pages`. Commit each task as
`w3-4-control-pages: task N — <title>`.

## Goal

This plan adds these pages:

| Page | Route |
|---|---|
| Library | `#library/skills\|plugins\|mcp\|memory` |
| Providers, Models & routing (with budgets) | `#settings/providers`, `#settings/models` |
| Usage & audit | `#usage?tab=cost\|audit\|disk` |
| Watches | `#watches` |

It also adds the New agent model chip and the old-route redirects.

## Non-goals

- Recipes (W5), memory scopes (W5), provider keys in the secrets vault
  (W4).

## Assumptions

- The W3.2 routes exist:
  - library: `/api/library/skills|plugins|memory…`;
  - models: `/api/models…`;
  - `/api/usage`;
  - budgets: `/api/budgets`, `POST /api/agents/{id}/budget/override`;
  - `/api/watches…`;
  - `/api/reroutes/{id}/undo`;
  - the `routing` spawn field.
- From W3.3: the `api.ts` `BudgetError` class, and the `budget`, `watch`
  and `reroute` handling in `fleet.ts`.
- Unchanged since before W1: `ClientsPanel.svelte` (no props),
  `DiskPanel.svelte`, `ActivityFeed.svelte`, and the `api.ts` functions
  `listAudit` and `getDiskUsage`.

---

## Task 1: API client for library, models, usage, budgets, watches

**Goal:** typed functions for every W3.2 control route.

**Files:**
- `web/ui/src/lib/api.ts`, `api.test.ts`

**Steps:**

1. Add the types. Field names follow W3.1 (engine shapes) and W3.2
   (bridge shapes):
   - `SkillEntry`, `SkillPreview`, `PluginEntry`, `PluginScan`;
   - `MemoryEntry`;
   - `ModelsConfig` (providers, presets, profiles, `defaultProfile`,
     `activePreset`, `roles`, `budgets`), `ProbeResult`;
   - `UsageReport`;
   - `Budgets`, `BudgetStatus`;
   - `WatchInfo` (with `samples`), `WatchSpec`, `OnTrip`.
2. Add one function per route. Map 501 to `'unsupported'` where a route
   proxies an agent method.
3. Tests: the URL and method for each function, plus the 501 mappings.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/api.test.ts
```

---

## Task 2: Library page

**Goal:** `#library/<tab>` with the Skills, Plugins, MCP and Memory tabs
(spec §6.4).

**Files:**
- `web/ui/src/views/Library.svelte` (new), `Library.test.ts` (new)
- `web/ui/src/lib/library/{SkillsTab,PluginsTab,MemoryTab}.svelte` (new)
- `web/ui/src/lib/ClientsPanel.svelte` (restyle only)
- `web/ui/src/App.svelte`, `Rail.svelte`, `routes.ts`

**Steps:**

1. Routes:
   - enable Library ◈ on the rail;
   - parse `#library/<tab>?project=`;
   - redirect `#clients` to `#library/mcp`.
2. `SkillsTab`:
   - A scope `Segmented` (Global or Project) with a project picker from
     `listProjects`.
   - A table of name, description, risk `Tag` and scope.
   - **Install** is a source field followed by **Preview**. The preview
     card shows the name, risk and description, with Confirm and Discard.
   - **Remove** asks for confirmation.
   - A 501 `project_library_unsupported` shows "Project skills are
     managed from this project's agents in container mode".
3. `PluginsTab`: the same flow, where the scan card shows the
   `contents` counts.
4. **MCP:** render `ClientsPanel` inside the tab, restyled with the W1
   tokens. Leave its logic untouched; `ClientsPanel.test.ts` must still
   pass.
5. `MemoryTab`:
   - A project picker, then a table of kind, content, confidence and
     source session.
   - Confidence is a `<select>` over `tentative`, `confirmed` and
     `stale`, which calls the confidence route.
   - Delete asks for confirmation.
6. Tests:
   - each tab renders from mocked APIs;
   - the preview → confirm flow posts the token and scope;
   - a confidence change posts.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Library.test.ts src/lib/ClientsPanel.test.ts && npx svelte-check
```

---

## Task 3: Providers and Models & routing pages

**Goal:** `#settings/providers` and `#settings/models`, including budgets
(spec §6.5).

**Files:**
- `web/ui/src/views/settings/{Providers,Models}.svelte` (new), tests
- `web/ui/src/lib/models/routingMatrix.ts` (new), `routingMatrix.test.ts` (new)
- `web/ui/src/lib/models/providerTemplates.ts` (new)

**Steps:**

1. `providerTemplates.ts` lists the provider types with their default base
   URLs, copied from `internal/llm/provider/templates.go`: read the file
   and list every template's name, type and base URL. A comment names the
   Go source to keep in sync.
2. `Providers.svelte`:
   - **Cards:** type, base URL, the key state (from config, from env, or
     none), and a health dot. **Probe** calls `POST /api/models/probe`
     and shows the model count or the error.
   - **Add provider:** pick a template, edit the name and base URL, then
     save with `PUT /api/models/providers`, sending only that provider.
   - **Set key:** a password input calling
     `PUT …/providers/{name}/key`. The value is cleared from memory after
     the call.
   - **Remove:** sends `{[name]: null}`.
3. `routingMatrix.ts`:
   - `matrix(cfg)` builds rows of roles and columns of profiles, each
     cell holding the preset name or custom agent;
   - `setCell(cfg, profile, role, preset)` returns the new `profiles`
     object for `PUT /api/models/routing`.
   - Tests: matrix shape, `setCell` immutability, and that the default
     profile is kept.
4. `Models.svelte`:
   - a presets table, edited inline and saved with `PUT …/presets`;
   - the routing matrix: each cell is a preset `<select>`, a profile
     switcher picks the column set, and **Set default** sets
     `defaultProfile`;
   - **Budgets:** daily and per-agent USD inputs plus action selects,
     saved with `PUT /api/budgets`. The current spend comes from
     `GET /api/budgets`.
   - A toast on every save: "Applies to agents started from now".
5. Settings routes and rail: ⚙ opens `#settings/models`, with sub-tabs
   Models, Providers and Tokens. Tokens is the existing `#clients`
   content, linked from here as well.
6. Tests:
   - editing a matrix cell sends the right body;
   - adding a provider from a template;
   - a budget save;
   - the key input never appears in a rendered snapshot after saving.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/models src/views/settings && npx svelte-check
```

---

## Task 4: Usage & audit page

**Goal:** `#usage?tab=cost|audit|disk` (spec §6.6).

**Files:**
- `web/ui/src/views/Usage.svelte` (new), `Usage.test.ts` (new)
- `web/ui/src/lib/usage/{CostTab,BarChart}.svelte` (new)
- `web/ui/src/App.svelte`, `Rail.svelte`, `routes.ts`

**Steps:**

1. Routes: enable Usage ∿ on the rail. Redirect `#disk` to
   `#usage?tab=disk`, and `#activity` to `#usage?tab=audit`.
2. `CostTab`:
   - **Tiles:** spend, tokens, agent hours, PRs shipped.
   - **Chart:** `BarChart.svelte`, an inline-SVG cost-by-day chart with
     the daily cap drawn as a horizontal line when it is set.
   - **Breakdown:** tables by project, role and model, each a separate
     `getUsage({by})` call.
   - **Controls:** a range `Segmented` (7 or 30 days).
   - **Budget state:** spent against the cap, and a paused-agents list
     with an **Override** button that calls
     `POST /api/agents/{id}/budget/override`.
3. **Audit tab:** the existing `ActivityFeed` with an event-type filter.
   Add a `filter?: string` prop to `ActivityFeed`, with the default
   behaviour unchanged.
4. **Disk tab:** `DiskPanel` as is.
5. Tests:
   - tiles and bars render from a fixture;
   - the cap line is drawn;
   - override posts;
   - the redirects work.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Usage.test.ts src/lib/DiskPanel.test.ts && npx svelte-check
```

---

## Task 5: Watches page

**Goal:** `#watches` with the table, detail and New watch form, including
reroute (spec §6.7–§6.8).

**Files:**
- `web/ui/src/views/Watches.svelte` (new), `Watches.test.ts` (new)
- `web/ui/src/lib/watches/{Sparkline,WatchForm,WatchDetail}.svelte` (new), `spark.ts` (new), `spark.test.ts` (new)

**Steps:**

1. `spark.ts`:
   - `sparkPath(samples, w, h)` returns an SVG path for the last 24h of
     samples;
   - `thresholdOf(condition)` returns the numeric operand for `json` and
     `exit_code` conditions, otherwise `null`.
   - Tests: scaling with constant values, an empty input, and threshold
     parsing.
2. `Watches.svelte`:
   - Load `listWatches()` and update it from `watch` deltas.
   - **Table:**
     - owner: agent name, or "Studio" with a badge;
     - kind and source;
     - condition;
     - sparkline;
     - trip action: notify, resume, or "reroute role → preset";
     - state.
   - Clicking a row opens `WatchDetail`.
3. `WatchDetail` shows:
   - a larger chart, with a threshold line when the condition has a
     numeric operand;
   - the tripped points, marked;
   - the last sample text;
   - the owner agent link;
   - a **Stop** button.
4. `WatchForm`:
   - **Owner:** Studio, or an agent picker over running agents.
   - **Fields:** name, kind (command, job or file), then the
     kind-specific field, condition, mode, interval in seconds (minimum
     2), notify and resume.
   - **Reroute:** shown only for a Studio owner. Pick a role from
     `config.roles` and a preset from `config.presets`.
   - **Submit** calls `createWatch`. Limit errors from the agent show
     inline.
5. **Home notices:** `reroute` notices from W3.3's `notices` list render on
   Home with an **Undo** button (`undoReroute`).
6. Tests:
   - table rendering;
   - the form posts the spec, including `onTrip.reroute` only for Studio;
   - Undo on a notice calls the route.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Watches.test.ts src/lib/watches && npx svelte-check
```

---

## Task 6: New agent model chip

**Goal:** `#new` offers a routing profile or a preset, and passes it as
`routing` (spec §6.9).

**Files:**
- `web/ui/src/views/NewAgent.svelte`, `NewAgent.test.ts`
- `web/ui/src/lib/newagent/newAgent.ts`, `newAgent.test.ts`

**Steps:**

1. Load `getModels()` once. The Model chip popover has two groups:
   - **Profiles:** sets `routing.profile`.
   - **Presets:** `routing.overrides` maps every role in `config.roles`
     except the fast roles to that preset.
     - The fast-role list mirrors `routing.FastRoles`: `router`, `title`,
       `summarizer` and `repo_scout`.
     - Put it in `newAgent.ts`, with a comment naming
       `internal/llm/routing/types.go:50`.
2. The default is "Default profile", which sends no `routing`. The
   last-used choice is remembered with the other chips.
3. `spawnAgent` sends `routing`. Add it to the `api.ts` `SpawnRequest`
   type.
4. Tests:
   - choosing a preset sends overrides for the non-fast roles only;
   - choosing a profile sends `routing.profile`;
   - the default sends nothing.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/NewAgent.test.ts src/lib/newagent
```

---

## Task 7: Rebuild static

**Goal:** the committed bundle includes W3.4.

**Steps:**

1. Run `cd web/ui && npm run build`.
2. Commit `web/bridge/static`.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../bridge && go test ./ -run 'TestAssets|TestWebIsStdlibOnly'
```

Check by hand:
- add a provider and probe it;
- change a role binding and spawn an agent, then check its roster;
- set a tiny daily cap and confirm spawning is blocked;
- create a Studio watch with reroute and trip it;
- install a skill globally.

---

## Final verification

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../.. && CGO_ENABLED=1 go test ./... ; go vet ./... ; gofmt -l .
```

Expected: the UI is clean, and Go fails only on the five known tests.

## Integration notes

- Every rail entry except Workspaces ▦ is now enabled. W4 enables
  Workspaces.
- Provider keys still go to the user config through
  `SaveUserConfigProviderAPIKey`, as the TUI does today. W4 adds vault
  references for them.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. |
| Anchors verified? | Checked on `2ddc09e`: `ClientsPanel.svelte` (no props, and its test file), `ActivityFeed.svelte`, `DiskPanel.svelte` and its test, `listAudit`/`getDiskUsage` (`api.ts:370/376`), `internal/llm/provider/templates.go`, `routing.FastRoles` (`types.go:50`). Later names are as defined. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §6.1, §6.4–§6.9. |
