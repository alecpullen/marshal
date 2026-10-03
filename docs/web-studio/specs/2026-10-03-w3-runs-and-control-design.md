# W3 · Runs & control — phase spec

Parent design: [`docs/web-studio/design.md`](../design.md) (§8.3, §8.7, §8.10, §8.13; the Library row of §7; §9 row W3).
Previous phases, assumed complete: [W1](2026-10-03-w1-foundation-design.md), [W2](2026-10-03-w2-session-and-ship-design.md).
Plans, executed in this order:
1. [W3.1 · Engine](../plans/2026-10-03-w3-1-engine-plan.md)
2. [W3.2 · Bridge: control agent, runs, library, models, usage, watches](../plans/2026-10-03-w3-2-bridge-control-plan.md)
3. [W3.3 · Runs page and Live wall](../plans/2026-10-03-w3-3-runs-and-live-plan.md)
4. [W3.4 · Library, Models, Usage, Watches](../plans/2026-10-03-w3-4-control-pages-plan.md)

## 1. Summary

W3 adds the pages that control the whole fleet rather than one session:

- **Runs:** plan and swarm runs in lanes, graph and timeline views.
- **Live wall:** every running agent at a glance.
- **Library:** skills, plugins, MCP clients and memory.
- **Models & routing:** providers, presets and role bindings.
- **Usage & budgets:** cost, tokens, the audit log and disk.
- **Watch monitors.**

Three engine and bridge foundations make those pages possible:

1. **A control agent.** A long-lived `marshal acp` that the bridge owns.
   It serves user-global operations (config, global skills and plugins,
   Studio-level watches) without depending on any project agent.
2. **Shared agent home directories.** In container mode, every agent and
   the control agent now share one user config directory and one data
   directory from the state volume. Without this, an edit to providers or
   global skills would not reach containerized agents. Each container
   also keeps its own SQLite DB, which is lost when it's removed, so usage
   and memory don't survive.
3. **Usage rows in telemetry.** The bridge keeps its own usage ledger
   across agents and enforces budgets from it.

## 2. What earlier phases left in place

| From | Used for |
|---|---|
| W1: the stack stream and the `session/update` kinds; the bridge's `EventLog.Broadcast` and fleet SSE; the UI rail (Runs, Live, Library, Usage and Watches entries disabled), `createStackStore` and the transcript renderers | Runs dock, Live wall tiles, enabling the rail entries |
| W2: the dock (`lib/dock/*`, `dock.ts`), the `Inspect` tab, `ErrUnsupported`/`Registry.call`, the `gate` fleet delta, New agent chips | The Runs dock, the model chip |

## 3. Scope

### In scope

- **Engine:**
  - `Depends on:` in plans;
  - the run-detail request and `run_progress` updates;
  - the `config/*` methods for the control agent;
  - usage rows in telemetry;
  - the watch methods and sample history;
  - fixing the pricing unit comments.
- **Bridge:**
  - the control agent and shared homes;
  - routes for runs, library, models, usage and budgets, and watches;
  - the `run`, `watch` and `budget` fleet deltas.
- **UI:**
  - the Runs list and run page (lanes, graph, timeline, dock);
  - the Live wall;
  - Library (skills, plugins, MCP, memory);
  - Models & routing;
  - Usage & audit (cost, audit, disk);
  - Watches;
  - the New agent model chip.

### Out of scope

| Item | Where |
|---|---|
| Running plan tasks in parallel | Design §8.7: serial order stays |
| Recipes in the Library | W5 |
| Memory scopes and promotion | W5 |
| Secrets for provider keys | W4. W3 stores keys exactly as the TUI does today (§5.3). |

## 4. Engine

### 4.1 `Depends on:` in plans

`pipeline.TaskSpec` (`internal/pipeline/plan.go:24`) gains
`DependsOn []int`. `ParsePlan` (`plan.go:44`) reads lines matching
`^Depends on:\s*(.+)$` inside a task's body:

- The list is comma-separated task numbers.
- Unknown numbers and cycles return an error naming the task, as a plan
  diagnostic.
- A task without the line depends on the previous task. Task 1 depends on
  nothing.

Execution order is unchanged (serial, in file order). Dependencies are
validated and reported only.

### 4.2 Run detail — `session/run`

```
→ { sessionId }
← { "kind": "sdd" | "swarm" | "none", "sdd": SDDRun?, "swarm": SwarmRun? }
```

**`SDDRun`** is `SDDStatusResult` (`turn.go:1483`) plus:

- `baseRef`, `ledgerPath`, `startedAt`, `endedAt`, `error`, `phaseStartedAt`;
- `tasks[]`, where each task has:
  - `n`, `title`, `dependsOn[]`;
  - `status` (`pending`, `active`, `done` or `failed`);
  - `startedAt`, `endedAt` (from `TaskTimings`);
  - `execType`;
  - `commit {base, head}` (from `Ledger.TaskCommit`);
  - `fixRounds`;
  - `stages`: one entry each for Implement, Verify, Review and Commit,
    each `{state: pending|active|done|failed|skipped, detail}`;
- `gate` (from `SDDGate`, `session.go:468`).

The per-task data comes from:

| Field | Source |
|---|---|
| Tasks and `dependsOn` | `ParsePlan(planPath)` |
| Done tasks and commits | `Ledger.CompletedTasks` and `Ledger.TaskCommit` (`ledger.go:125/39`) |
| Stages and fix rounds | The run events for that task (`session.RunEvents`, `TaskN`) |
| The active task's stage | `Phase` and `CurrentTask` |

How run events map to stages:

| Run event | Stage effect |
|---|---|
| `RunEventVerifyFailed` | Verify failed (an active task returns to Implement), and the fix-round count goes up |
| `RunEventGateSkipped` | Verify skipped |
| `RunEventReview` | Review done, or failed when the severity is blocking |
| `RunEventCommit` | Commit done |
| `RunEventTaskDone` | Every stage done |

**`SwarmRun`** is `SwarmStatusResult` (`turn.go:1438`) plus the finished
roles. `ClearSwarmProgress` wipes the progress at the end of a run, so the
TurnManager keeps the last non-empty `SwarmProgress` per session and
returns it with `active:false` after the run ends.

**`run_progress` update.** At each stack flush (W1 turn ticker, W2 idle
loop, `finishTurn`), the agent hashes the run detail. When the hash
changed since the last send, it emits
`session/update {kind:"run_progress", run}`.

- This needs no new tick; it rides the existing flush loop.
- It is sent only after a client has activated the stack or called
  `session/run`.
- The bridge stores these updates in the replay ring (W1's `Attach`
  routes only `stack_patch` to `Broadcast`). They are rare: at most one
  per phase change.

### 4.3 Config methods (control agent)

These methods take no `sessionId`. They read and write the agent's
user-global config at `config.UserConfigPath(home)` (`locations.go:23`),
and the bridge calls them only on the control agent.

| Method | Params | Result / effect |
|---|---|---|
| `config/get` | none | `providers` (name → `ProviderConfig` without `api_key`, plus `hasKey bool` and `keySource: "config"\|"env"\|"none"`); `presets` (name → `ModelPreset`); `profiles` (name → role → binding); `defaultProfile`; `activePreset`; `roles` (the `routing.AllRoles` names); `budgets` (§4.5) |
| `config/set_providers` | `{providers}` | `SaveUserConfigProviders` (`save.go:766`). Fields left out keep their stored values. `api_key` is never accepted here. |
| `config/set_provider_key` | `{name, key}` | `SaveUserConfigProviderAPIKey` (`save.go:700`) |
| `config/set_presets` | `{presets}` | `SaveUserConfigPresets` (`save.go:800`) |
| `config/set_routing` | `{profiles, defaultProfile, activePreset}` | `SaveUserConfigSection` (`save.go:101`) with `agent_profiles` and `profile` set |
| `config/probe_provider` | `{name}` or `{config}` | Lists models with `provider.NewFromConfig(...).Models(ctx)`, as `tui/probe` does. Returns `{models:[{id, contextWindow?}], error?}`. Times out after 15s. |
| `config/set_budgets` | `{budgets}` | Writes `[budgets]` (§4.5) through `SaveUserConfigSection` |

**When changes apply.** Saved config applies to agents and sessions started
afterwards. Agents that are already running keep their loaded config. The
UI says so on save.

**Capability:** `initialize` adds `"configAccess": {}` at the top level of
`agentCapabilities`, because it isn't per session.

### 4.4 Usage rows in telemetry

`session_telemetry` (`turn.go:1176`) gains a `usage` array. Each row
mirrors a `turn_metrics` record written since the last telemetry for that
session:

- `id`, `startedAt`, `durationMs`;
- `role`, `provider`, `model`;
- `promptTokens`, `completionTokens`, `reasoningTokens`,
  `cacheReadTokens`, `cacheWriteTokens`;
- `costUsd`, a float equal to `estimated_cost_cents / 10000`.

The rows are read with `db.RecentTurnMetricsForSession`
(`turnmetrics.go:231`), through the `rt.DB` handle the memory lookup uses
(`host.go:360`), filtered to IDs above a per-session high-water mark held
on `TurnManager`.

**Pricing units.** `EstimateCostCents` documents its result as 1/10000 USD,
but nearby comments say "cents per million". W3 pins the unit with a test
(1M prompt tokens at a known price gives the expected value) and corrects
the comments in `internal/llm/pricing`. The wire field `costUsd` removes
the ambiguity for clients.

### 4.5 Budgets config

This is a new `[budgets]` section in the user config, so it is user-global
only:

```toml
[budgets]
daily_usd = 25.0          # 0 = no cap
per_agent_usd = 5.0       # 0 = no cap
on_daily_cap = "block"    # "warn" | "block"
on_agent_cap = "pause"    # "warn" | "pause"
```

The engine only stores this section. The bridge enforces it (§5.4), because
spend spans agents.

### 4.6 Watches over ACP

`internal/watch` gains sample history. `Info` (`watch.go:82`) adds
`Samples []SamplePoint{At time.Time; Value float64; Tripped bool}`, a ring
of the last 288 samples:

| Condition | `Value` |
|---|---|
| `exit_code` | The exit code |
| `json` with a numeric operand | The extracted number |
| Anything else | 1 when tripped, else 0 |

New ACP methods. Watches are per runtime, so the Studio-level ones live on
the control agent's control session.

| Method | Params | Result |
|---|---|---|
| `session/watch_list` | `{sessionId}` | `{watches: [WatchInfo + samples]}` |
| `session/watch_start` | `{sessionId, spec}` | `{id}`, through `Manager.Start` (`watch.go:254`) with `Owner` set to `"studio"` |
| `session/watch_stop` | `{sessionId, id}` | `{}` |

The watch `Event` broker is surfaced as `session/update {kind:"watch", event}`.
It is wired through the runtime's `Deps.OnEvent` (`watch.go:141`), chained
after the existing handler.

**Capability:** `watchAccess`.

## 5. Bridge

### 5.1 Control agent and shared homes

`Fleet` gains a control runtime, started lazily on first use, which also
restarts if it dies. Its relationship to project agents, by mode:

| Mode | Control agent | Project agents |
|---|---|---|
| **Process mode** (no container runtime) | A `marshal acp` child (`Child{MarshalBin}`) with the bridge's own `HOME`. It shares config with every agent. | Unchanged |
| **Container mode** | A container named `marshal-control`. The state volume subpath `home/config` is mounted read-write at `/marshal/config`, and `home/data` at `/marshal/data`. Its env sets `XDG_CONFIG_HOME=/marshal/config` and `MARSHAL_DATA_DIR=/marshal/data`. | Mount `home/config` read-only, plus `home/data` read-write, with the same env (`locations.go` honours both). |

In container mode this also means:

- Project agents' memories and usage persist across containers, which W5
  also relies on.
- SQLite runs in WAL mode on the shared local volume, which is safe for
  several processes on one host.

**Control session.** The bridge opens one session on the control agent,
`session/new` with cwd `<state>/control` (`/work/control` in containers),
and reopens it after a restart. Global library operations and Studio
watches use this session.

**Project scope.** Project-scoped library operations open one session per
project root on the control agent, cached:

- in process mode, with cwd set to the root;
- in container mode, with the root mounted through the existing project
  mounts.

A git-sourced project has no local root, so its project-scoped library is
managed from its agents' sessions and is not shown on the Library page.

### 5.2 Routes

| Route | Backing |
|---|---|
| `GET /api/runs` | Agents with a stored `run` digest, newest first |
| `GET /api/runs/{agentId}` | `session/run` |
| `POST /api/runs` `{agentId?, project?, kind:"sdd"\|"swarm", plan?, planPath?, goal?}` | With no `agentId`, spawns one (as `spawnAgent`) on `project`. Then: SDD writes the plan with `planPathFor` and calls `session/sdd_start` (generalising `Fleet.startPlan`, `intake.go:201`); swarm calls `session/swarm_start` with `goal`. Both run in a goroutine, because the calls block until the run ends. Returns 202 `{agentId}`. |
| `POST /api/runs/{agentId}/answer` `{answer}` | `session/sdd_answer` |
| `GET /api/library/skills?scope=global\|project&project=` | `session/skills_list` on the control or project session |
| `POST /api/library/skills/preview` `{source}`, `…/confirm` `{stagingToken, scope, project?}`, `…/discard` `{stagingToken}`, `DELETE /api/library/skills/{name}?scope=&project=` | The `skills_*` methods (`internal/acp/skills.go`) |
| `GET /api/library/plugins…`, plus scan, confirm, discard and delete | The `plugins_*` methods |
| `GET /api/library/memory?project=`, `DELETE …/{id}`, `POST …/{id}/confidence` `{confidence}` | The `memory_*` methods on the project session |
| `GET /api/models` | `config/get` |
| `PUT /api/models/providers`, `PUT /api/models/providers/{name}/key`, `PUT /api/models/presets`, `PUT /api/models/routing`, `POST /api/models/probe` | `config/set_*`, `config/probe_provider` |
| `GET /api/usage?range=7d\|30d&by=day\|project\|role\|model` | The usage ledger (§5.3) |
| `GET /api/budgets`, `PUT /api/budgets` | `config/get` `budgets` and `config/set_budgets` |
| `GET /api/watches`, `POST /api/watches` `{agentId?, spec}`, `DELETE /api/watches/{agentId\|studio}/{id}` | `session/watch_*` on the agent's session, or on the control session for `studio` |

**Fleet SSE:**

| Delta | Carries | From |
|---|---|---|
| `run` | `{agentId, run}` | `run_progress` updates |
| `watch` | `{agentId\|"studio", event}` | `watch` updates |
| `budget` | `{scope: "daily"\|"agent", agentId?, spentUsd, capUsd, action}` | Budget checks (§5.4) |
| `reroute` | `{id, watch, role, from, to}` | A watch tripping its reroute action (§6.8). The inbox shows it with an Undo button (`POST /api/reroutes/{id}/undo`). |

Every mutating route writes an audit entry. The new audit events are
`skill_installed`, `skill_removed`, `plugin_installed`, `plugin_removed`,
`memory_deleted`, `models_changed`, `budgets_changed`, `watch_started` and
`watch_stopped`.

### 5.3 Usage ledger

The bridge appends each telemetry `usage` row to
`<state>/usage/YYYY-MM.jsonl`, the same rotation scheme as the audit log.

- Each row adds `agentId`, `project` and `origin`.
- Rows are deduplicated by `(agentId, id)` against an in-memory set of the
  last 10,000 keys.
- `GET /api/usage` aggregates the current and previous month's files.

The response is
`{range, totals: {costUsd, promptTokens, completionTokens, agentHours, prsShipped}, series: [{key, costUsd, tokens}]}`:

| Field | Source |
|---|---|
| `agentHours` | The sum of `durationMs` |
| `prsShipped` | `push` audit events with a PR URL in the range |

### 5.4 Budgets

On each new usage row the bridge sums today's spend (UTC day) and the
agent's total spend, then compares both with the stored budgets, which it
reads from `config/get` at startup and on `PUT /api/budgets`.

| Cap reached | Action | Behaviour |
|---|---|---|
| Daily | `warn` | A `budget` delta |
| Daily | `block` | A `budget` delta, and `POST /api/agents`, `POST /api/sessions/{id}/prompt` and `POST /api/runs` return `429 {"error":"budget_exceeded", "scope":"daily"}` until the next UTC day or until the cap is raised |
| Per agent | `warn` | A `budget` delta |
| Per agent | `pause` | A `budget` delta, the agent's turn is cancelled (`Registry.Cancel`), and its prompts return 429 until the cap is raised or `POST /api/agents/{id}/budget/override` is called (audited) |

## 6. Web UI

### 6.1 Rail and routes

Enable Live (`#live`), Runs (`#runs`), Library (`#library/<tab>`), Watches
(`#watches`) and Usage (`#usage?tab=`) on the rail. Settings gains Models &
routing (`#settings/models`) and Providers (`#settings/providers`).
`#clients` redirects to `#library/mcp`, and `#disk` and `#activity`
redirect to `#usage?tab=disk|audit`.

### 6.2 Runs

**List (`#runs`):**
- one row per run: agent, plan name or goal, a progress bar, the phase,
  elapsed time, and the gate if one is open;
- filters: running, finished, needs you;
- a **New run** button opens a modal: project or agent, then kind, then
  a plan (paste, or a path in the project) or a goal.

**Run page (`#runs/<agentId>?view=lanes|graph|timeline`):** a header with
the plan name, branch, progress, phase and tokens, plus the open gate's
question with an answer box (`POST …/answer`).

The **lanes** view:
- One row per task: `n`, title, a dependency chip.
- Four stage cells: Implement, Verify, Review, Commit. Each shows a state
  glyph and colour, the fix-round count, and the commit's short SHA.
- A roles legend (from the W2 roster data in `session/agents_roster`)
  shows each role's model and token share.

The **graph** view:
- An SVG layered DAG: the layer is the longest path from a root, and order
  within a layer is by task number.
- Nodes show `n`, the title (truncated), the role of the active stage, the
  state and progress.
- Edges come from `dependsOn`.
- The critical path is the chain with the longest summed task duration,
  using elapsed time for unfinished tasks. It is drawn dashed in accent.

The **timeline** view:
- per-role bars, built from the stack's step nodes (`step.role`,
  `startedAt`, `endedAt`) over the run's time range;
- below them, cumulative tokens over time, plotted from the `run` delta
  samples the page has seen plus the run's start and current totals.

**Dock:** the W2 dock is reused.
- Selecting a cell or node filters the session's stack to steps whose
  `step.role` matches that stage's role (`sdd_implementer`,
  `sdd_reviewer` and so on) and whose `startedAt` falls within the task's
  window.
- The Inspect tab works on those steps.

### 6.3 Live wall (`#live`)

- A grid of tiles, 3 or 4 across depending on width, 12 per page.
- Each tile shows:
  - name, project and elapsed time;
  - progress segments from the agent's todo tasks (the stack's task
    nodes);
  - the last three step headlines at outline density, from a per-tile
    `createStackStore`;
  - chips for model, workspace (W4 fills it in) and gate.
- Only visible tiles hold stack stores. They are created when a tile
  scrolls into view and disposed when it leaves.
- A needs-you tile is tinted `warn`. It shows the pending approval or
  question with the inbox's inline actions (W1 `Home`).
- Clicking a tile opens `#chat/<id>?dock=collapsed`.
- Filters: project, and Runs only (agents with a `run` digest).

### 6.4 Library (`#library/skills|plugins|mcp|memory`)

| Tab | Content |
|---|---|
| **Skills** | A scope switch (Global, or a project picker). A list of name, description, risk tag and scope. Install by pasting a source: preview (name, risk, contents), then Confirm or Discard. Remove, with a confirm. |
| **Plugins** | The same flow; the scan preview shows the contents summary counts. |
| **MCP** | The existing `ClientsPanel` content, restyled. |
| **Memory** | A project picker, then a table of kind, content, confidence (an editable select) and source session. Delete with a confirm. |

### 6.5 Models & routing (`#settings/models`, `#settings/providers`)

**Providers page:**
- a card per provider: type, base URL, the key state (from config, from
  env, or none), and a health dot from `POST /api/models/probe`;
- **Add provider** uses the templates (the provider `type` list matches
  `internal/llm/provider/templates.go`: ollama, openai, groq, …);
- **Set key** shows a password field.

**Models page:**
- a presets table: name, provider, model, context window, local only,
  pricing;
- a routing matrix: roles down the side, the active profile's bindings
  across, each cell a preset select;
- a profile switcher, and set default;
- **Budgets:** the daily and per-agent caps with their actions.

Saving shows "Applies to agents started from now".

### 6.6 Usage & audit (`#usage?tab=cost|audit|disk`)

- **Cost:**
  - totals tiles: spend, tokens, agent hours, PRs shipped;
  - a cost-by-day bar chart;
  - breakdown tables by project, role and model;
  - a range switch (7 or 30 days);
  - the budget state, with cap lines on the chart.
- **Audit:** the existing audit list, filterable by event.
- **Disk:** the existing `DiskPanel`.

### 6.7 Watches (`#watches`)

The **table** shows, for each watch:
- the owner (agent name or Studio);
- the source (command, job or file);
- the condition;
- a 24h sparkline from `samples`;
- the trip action (notify, resume, or reroute, §6.8);
- the state.

Agent-started watches are marked with the agent that started them.

The **detail** pane shows:
- a chart with the threshold line, where the condition has a number;
- the last sample and the trip history (the tripped samples);
- the affected agent.

The **New watch** form (Studio or a chosen agent) has: name, kind,
command, job or path, condition, mode, interval, notify, resume, and
reroute.

### 6.8 Reroute action

A Studio watch can carry `onTrip: {reroute: {role, preset}}`. This is held
by the bridge, not the engine. When that watch's `watch` event shows
`fired`:

1. The bridge applies the binding through `config/set_routing` on the
   active profile.
2. It writes the audit event `models_changed`, with the reason
   `watch:<name>`.
3. It raises an inbox item "Rerouted <role> to <preset> because <watch>
   tripped", with an Undo that restores the previous binding.

Like any config change, it affects agents started afterwards.

### 6.9 New agent model chip

`#new` gains a Model chip, which offers either a routing profile or a
preset for the main roles.

1. **Choices:** the list comes from `config/get`.
2. **Spawn request:** the choice goes to `POST /api/agents` as
   `routing: {profile?, overrides?: {role: preset}}`.
3. **`session/new`:** the bridge forwards the same object. Add a
   `Routing *RoutingParams` field to `sessionParams`
   (`internal/acp/session.go:121`).
4. **Agent side:**
   - `profile` replaces `Profile.Default` for that session's resolver;
   - `overrides` go through `routedProviderResolver.withRoleOverrides`
     (`internal/app/app.go:303`), which calls
     `routing.Config.WithRoleOverride` (`router.go:383`).
5. **Resolving "main roles":** the chip's single preset applies to every
   role in `routing.AllRoles` except `routing.FastRoles` (router, title,
   summarizer and repo scout keep their cheap bindings).

## 7. Testing

| Layer | Tests |
|---|---|
| pipeline | `Depends on:` parsing: list, the default previous task, an unknown task, a cycle |
| acp | `session/run` for SDD (tasks, stages from events, commits from a ledger fixture) and swarm (kept after clear). `run_progress` is emitted only on change. Every `config/*` method round-trips against a temp `XDG_CONFIG_HOME`, and `api_key` is never returned. Usage rows have a high-water mark and are not repeated. Watch list, start, stop, the samples ring and the `watch` update. The `session/new` `routing` override. |
| pricing | The unit test pinning `EstimateCostCents` |
| bridge | Control agent: lazy start, restart, session reuse, and the container mounts and env in `buildRunArgs` (via `commandRunner`). Every route, with 404/501. Usage ledger: append, dedup, aggregate. Budget block, warn and pause, including the 429s and the override. Reroute and undo. Audit entries. |
| UI | Runs: the lanes reducer from a run fixture, the DAG layering and critical path, timeline bars. Live wall paging and tile store disposal. Library flows with mocked APIs. Models: the routing matrix edits produce the right PUT. Usage aggregation rendering. Watches: sparkline scaling, the form. The New agent model chip. |

## 8. Acceptance criteria

1. Starting an SDD run from `#runs` shows live lanes. A failed verify
   shows on the right task and stage, and the gate question can be
   answered there.
2. A plan with `Depends on:` lines draws the right graph and critical
   path, and a cyclic plan is rejected with a message.
3. Editing a role binding on `#settings/models` changes the route that
   the next spawned agent shows in its roster.
4. In container mode, a provider added in the Studio is visible to the
   next containerized agent.
5. The daily cap in `block` mode stops new spawns and prompts with a
   visible reason. Raising the cap lifts the block.
6. A Studio watch with a reroute action applies and audits the change when
   it trips, and Undo restores it.
7. The Live wall shows 12 tiles per page with live headlines. Tiles that
   need you can be answered inline.
8. All suites pass as in earlier phases.
