# W3.1 · Engine — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w3-runs-and-control-design.md`](../specs/2026-10-03-w3-runs-and-control-design.md) §4, §6.9
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** backend. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W2.1](2026-10-03-w2-1-session-backend-plan.md) (previous backend plan), with everything it depends on. Backend plans never depend on UI plans.
**Base:** a branch containing every plan listed under Runs after. Anchors into code from before
W1 were checked on `2ddc09e`. W1 and W2 names are as those plans define
them.
**Plan slug:** `w3-1-engine`. Commit each task as `w3-1-engine: task N — <title>`.

## Goal

The engine provides what the W3 pages need:

- plan dependencies;
- run detail and `run_progress` updates;
- `config/*` methods, a `[budgets]` section, and usage rows in telemetry;
- watch history and watch methods;
- a per-session routing override on `session/new`.

## Non-goals

- Bridge and UI work (W3.2–W3.4).
- Running tasks in parallel.

## Assumptions

- W1's stack flush points exist: the turn ticker, `finishTurn`, and the
  flush in `Stack`. So does W2.1's idle loop (`startStackIdle`). This plan
  hooks run progress into them.
- `SessionManager.Get(id)` (`internal/acp/session.go:624`) returns
  `*app.Runtime`. That type has:
  - `State`, `DB` (`any`), `ProjectID`;
  - `WatchManager *watch.Manager` (`internal/app/runtime.go:111`);
  - `WatchBroker` (`pubsub.Broker[watch.Event]`, cast with `must`, as in
    `app.go:2169`).
- The five known failing tests stay known.

---

## Task 1: `Depends on:` in plans

**Goal:** `ParsePlan` reads and validates task dependencies (spec §4.1).

**Files:**
- `internal/pipeline/plan.go` (`TaskSpec` `:24`, `ParsePlan` `:44`)
- `internal/pipeline/plan_test.go`

**Steps:**

1. Add `DependsOn []int` to `TaskSpec`.
2. In `ParsePlan`, after the tasks are collected (each `TaskSpec.Body`
   is known):
   - Scan each body for the first line matching
     `^\s*Depends on:\s*(.+?)\s*$`.
   - Split the list on commas and parse each item with `strconv.Atoi`.
   - Remove that line from `Body`, so implementer prompts don't change
     meaning.
   - A task without the line gets `[N-1]` (task 1 gets nil).
3. Validate:
   - A number that is not a task, or a task that depends on itself,
     returns `fmt.Errorf("plan %s: task %d: depends on unknown task %d", …)`.
   - Detect cycles with a DFS over `DependsOn`. A cycle returns
     `fmt.Errorf("plan %s: dependency cycle: %s", …)`, listing the cycle
     as `3 → 5 → 3`.
4. Tests:
   - explicit list;
   - default previous task;
   - task 1 with none;
   - unknown number;
   - self-dependency;
   - a three-node cycle;
   - the `Body` no longer contains the line.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/pipeline/ -run TestParsePlan -v && CGO_ENABLED=1 go test ./internal/pipeline/
```

---

## Task 2: Run detail — `session/run`

**Goal:** one request returns the full SDD or swarm run detail (spec §4.2).

**Files:**
- `internal/acp/run.go` (new), `run_test.go` (new)
- `internal/acp/turn.go` (keep the last swarm progress)
- `internal/acp/host.go`

**Steps:**

1. Define the wire types `RunDetail{Kind; SDD *SDDRun; Swarm *SwarmRun}`,
   `SDDRun`, `RunTask`, `RunStage` and `SwarmRun`, with the fields in
   spec §4.2 and camelCase JSON. `SDDRun` embeds the
   `SDDStatusResult` fields (`turn.go:1483`) by copying them, not by Go
   embedding, so the JSON stays flat.
2. `func buildSDDRun(st *session.State) (*SDDRun, error)`:
   - Read `st.SDDProgress()` (`session/sdd_progress.go:69`). If `PlanPath`
     is empty, return nil.
   - Get titles and dependencies from `pipeline.ParsePlan(PlanPath)`. On a
     parse error, fall back to `Tasks` titles and no dependencies.
   - Read done tasks and commits through
     `pipeline.Ledger{Path: LedgerPath}`: `CompletedTasks()` and
     `TaskCommit(n)` (`internal/pipeline/ledger.go:125/39`).
   - Fill stages and fix rounds from `st.RunEvents()` grouped by `TaskN`,
     using the mapping table in spec §4.2.
   - For the active task (`CurrentTask`), set the stage matching `Phase`
     to `active`. Map phases to stages by prefix: `implement…` →
     Implement, `verify…`/`gate…` → Verify, `review…` → Review,
     `commit…` → Commit. Any other phase leaves the stages unchanged.
   - Times come from `TaskTimings[n-1]`.
   - The gate comes from `st.SDDGate()` (`session.go:525`).
3. Keep the swarm result after it clears. Add `lastSwarm map[string]session.SwarmProgress`
   to `TurnManager`, under its own mutex. At every stack flush point, and
   in `SwarmStatus` (`turn.go:1449`), record `st.SwarmProgress()` when it
   has roles. `buildSwarmRun` returns the live progress when active,
   otherwise the last recorded one with `active:false`.
4. `func (m *TurnManager) Run(ctx, params) (any, error)` takes
   `sessionIDParams` and returns `RunDetail`:
   - `kind:"sdd"` when the SDD run is non-nil;
   - otherwise `"swarm"` when there is a swarm result;
   - otherwise `"none"`.
5. Register `session/run` and add the capability `runDetail`.
6. Tests:
   - an SDD fixture: a plan file in a temp dir, a ledger file with a
     `Task 1: complete (commits abc1234..def5678, review clean)` line
     (format `ledger.go:14`), progress on task 2 in the verify phase, and
     a `RunEventVerifyFailed` for task 2. Task 1 is done with the commit;
     task 2 has Verify active and `fixRounds` 1.
   - swarm: set progress, take the status, clear it, and the result is
     still there with `active:false`.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestRun' -v
```

---

## Task 3: `run_progress` updates

**Goal:** a changed run detail is pushed as `session/update {kind:"run_progress"}`
(spec §4.2).

**Files:**
- `internal/acp/run.go`, `run_test.go`, `internal/acp/stack.go`

**Steps:**

1. On `TurnManager`, add `runHashes map[string]uint64` and `runActive map[string]bool`.
   - `Run` and the first `Stack` call set `runActive[sid] = true`.
2. Add `func (m *TurnManager) flushRun(sessionID string, st *session.State)`:
   - Return if the session isn't active.
   - Build `RunDetail` and hash its `json.Marshal` with FNV-64a.
   - When the hash differs from `runHashes[sid]`, store it and call
     `m.notify("session/update", SessionUpdateParams{SessionID: sid, Update: map[string]any{"kind": "run_progress", "run": detail}})`.
   - A `kind:"none"` detail is never sent twice in a row.
3. Call `flushRun` at every parent stack flush point: the W1 ticker branch,
   `finishTurn`, `Stack`, and the W2.1 idle tick. Place each call next to
   W2.1's `flushChildStacks` calls.
4. Tests:
   - changing the SDD phase between two flushes emits exactly one
     `run_progress`;
   - an unchanged run emits nothing;
   - nothing is emitted before activation.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestRun|TestStack' -race -v
```

---

## Task 4: `[budgets]` config section

**Goal:** the user config can hold budgets, which load and save like other
user-global sections (spec §4.5).

**Files:**
- `internal/app/config/types.go` (`Config` `:10`), `defaults.go` (`Default` `:9`)
- `internal/app/config/save.go` (`writeSections` `:184`)
- `internal/app/config/*_test.go`

**Steps:**

1. Add `BudgetsConfig{DailyUSD float64 "daily_usd"; PerAgentUSD float64 "per_agent_usd"; OnDailyCap string "on_daily_cap"; OnAgentCap string "on_agent_cap"}`.
   Add `Budgets BudgetsConfig "toml:\"budgets\""` to `Config`.
2. Defaults:
   - `0`/`0` (no caps);
   - `OnDailyCap: "warn"`, `OnAgentCap: "warn"`;
   - a validator that rewrites unknown actions to `warn` and adds a
     diagnostic, the same way the other enum fields in `config` report
     bad values.
3. Mark the section user-global only, the way `[providers]` is (AGENTS.md
   "Config loading"):
   - project files never set it;
   - a trusted project file that has it gets the same hoist-or-diagnostic
     treatment. Follow how `Providers` is handled in the merge or load
     code: `grep -n "Providers" internal/app/config/*.go` finds the
     user-global guard.
4. In `writeSections`, write `[budgets]` when it differs from the
   defaults.
5. Tests:
   - load from TOML;
   - defaults;
   - an invalid action falls back with a diagnostic;
   - a project-level `[budgets]` is not applied;
   - a save round-trip.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/app/config/ -v -run 'Budget' && CGO_ENABLED=1 go test ./internal/app/config/
```

---

## Task 5: `config/*` methods

**Goal:** the control agent can read and write user-global providers,
presets, routing and budgets, and probe providers (spec §4.3).

**Files:**
- `internal/acp/config.go` (new), `config_test.go` (new)
- `internal/acp/host.go`

**Steps:**

1. Define
   `ConfigManager{home string; load func() (config.Config, error); probe func(ctx, name string, pc config.ProviderConfig) ([]ModelEntry, error)}`.
   - The constructor takes the home directory and a loader; `host.go`
     passes the same loader the session manager uses for the user config.
   - The default `probe` uses `provider.NewFromConfig` and `Models(ctx)`,
     as `internal/app/tui/probe/probe.go:31` does. Copy its call
     arguments, but drop the Bubble Tea wrapper.
2. Handlers. Every save goes to `config.UserConfigPath(home)`
   (`locations.go:23`).
   - `Get`: loads the config and returns the shape in spec §4.3.
     - Strip `api_key`.
     - `hasKey` is true when `api_key != ""`, or when `api_key_env` is set
       and present in the environment.
     - `keySource` is `"config"`, `"env"` or `"none"`.
     - `roles` lists `routing.AllRoles` (`routing/types.go:95`).
     - `profiles` is `cfg.AgentProfiles`; `defaultProfile` and
       `activePreset` come from `cfg.Profile`.
   - `SetProviders`:
     - Merge the incoming providers over the loaded ones, field by field
       for existing names. A `null` value deletes a provider.
     - Never accept `api_key`: return `invalidParamsError` if it's
       present.
     - Save with `SaveUserConfigProviders` (`save.go:766`).
   - `SetProviderKey`: `SaveUserConfigProviderAPIKey` (`save.go:700`).
   - `SetPresets`: validate that each preset's `provider` exists, then
     `SaveUserConfigPresets` (`save.go:800`).
   - `SetRouting`:
     - Validate that every binding names an existing preset or custom
       agent, and that `defaultProfile` exists.
     - Set `cfg.AgentProfiles` and `cfg.Profile` on the loaded config, then
       `SaveUserConfigSection(path, cfg)` (`save.go:101`).
   - `SetBudgets`: validate the actions, set `cfg.Budgets`, and save with
     `SaveUserConfigSection`.
   - `ProbeProvider`:
     - Takes `{name}` (look the provider up) or `{config}`.
     - Uses a 15s timeout.
     - On failure, returns `{models: [], error: err.Error()}`.
3. Register `config/get`, `config/set_providers`, `config/set_provider_key`,
   `config/set_presets`, `config/set_routing`, `config/set_budgets` and
   `config/probe_provider` in `registerHandlers`. Add `"configAccess": {}`
   to `agentCapabilities` (not `sessionCapabilities`).
4. Tests, each against a temp home (`t.Setenv("XDG_CONFIG_HOME", dir)`):
   - get after a set round-trips;
   - `api_key` is never returned, and is rejected in `set_providers`;
   - `keySource` is `env` when only the env var is set;
   - routing to an unknown preset is rejected;
   - budgets round-trip;
   - the probe uses a fake that returns models, and a fake that fails.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestConfig|TestRunInitializeCapabilities' -v
```

---

## Task 6: Usage rows in telemetry, and the pricing unit

**Goal:** `session_telemetry` carries new `turn_metrics` rows as `usage`,
with `costUsd`, and the pricing unit is pinned (spec §4.4).

**Files:**
- `internal/acp/turn.go` (`buildTelemetry` `:1176`), `turn_test.go`
- `internal/llm/pricing/pricing.go`, `prices.go`, `pricing_test.go`

**Steps:**

1. Pricing:
   - Add `TestEstimateCostCentsUnit`: a preset priced at a known rate,
     1,000,000 prompt tokens, and the expected result per the
     `EstimateCostCents` (`pricing.go:41`) doc comment (1/10000 USD).
   - Correct the contradicting comments on `ModelPricing` (`prices.go:8`)
     and `Lookup` to state the same unit.
   - If the test reveals that the code and the doc disagree, fix the doc,
     not the arithmetic, because stored `turn_metrics` rows already use
     the code's unit.
2. Add `usageHW map[string]int64` to `TurnManager`, under its own mutex.
   This is the per-session high-water mark of `turn_metrics` IDs.
3. Change `buildTelemetry(sessionID, state)` to also take the runtime's
   DB and project ID. Callers already hold `rt`; the DB is `rt.DB`,
   asserted to `*db.DB` as in `host.go:360`, and the project ID is
   `rt.ProjectID`.
   - Call `db.RecentTurnMetricsForSession(projectID, sessionID, 200)`
     (`internal/db/turnmetrics.go:231`).
   - Keep rows with an ID above the high-water mark, oldest first, and map
     them to the spec's `usage` row with
     `costUsd = float64(EstimatedCostCents) / 10000`.
   - Advance the high-water mark.
   - With a nil DB, `usage` is omitted.
4. Tests:
   - insert two metrics rows (`db.InsertTurnMetrics`, `turnmetrics.go:91`)
     for the session, then build telemetry: it has two rows;
   - build again with nothing new: no rows;
   - insert a third: it has one row;
   - `costUsd` matches.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/llm/pricing/ ./internal/acp/ -run 'TestEstimateCostCentsUnit|TestTelemetry' -v
```

---

## Task 7: Watch sample history

**Goal:** each watch keeps its last 288 samples as numbers, plus whether
the sample tripped (spec §4.6).

**Files:**
- `internal/watch/watch.go` (`Info` `:82`, `fire` `:482`, the sampling loop), `condition.go`
- `internal/watch/*_test.go`

**Steps:**

1. Add `SamplePoint{At time.Time; Value float64; Tripped bool}` and
   `Info.Samples []SamplePoint`.
2. On the internal watch record, keep a ring of `maxSamples = 288`
   points. Append one point each time a sample is evaluated against its
   condition (find the place that sets `LastSample`).
   - `Value` comes from a new
     `func sampleValue(cond string, s Sample) float64` in `condition.go`:
     - an `exit_code` condition gives `float64(ExitCode)`;
     - a `json <path> <op> <number>` condition gives the extracted value,
       parsed as a float, or 0;
     - anything else gives 1 when tripped, else 0.
   - `Tripped` is the condition result.
3. `List` and `Status` copy the ring into `Info.Samples`, oldest first.
4. Tests:
   - the ring caps at 288;
   - `sampleValue` covers each condition form;
   - `List` returns copies (mutating them doesn't affect the manager).

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/watch/ -race -v -run 'Sample|List'
```

---

## Task 8: Watch methods and `watch` updates over ACP

**Goal:** `session/watch_list`, `session/watch_start`,
`session/watch_stop`, and `session/update {kind:"watch"}` (spec §4.6).

**Files:**
- `internal/acp/watch.go` (new), `watch_test.go` (new)
- `internal/acp/host.go`

**Steps:**

1. Define
   `WatchManagerACP{lookup func(string) (*watch.Manager, *pubsub.Broker[watch.Event], bool); notify NotifyFunc}`.
   - In `host.go`, build it with a lookup through `manager.Get(id)` that
     returns `rt.WatchManager` and the cast `rt.WatchBroker`.
2. Handlers:
   - `List`: returns `{watches: m.List()}`, mapped to camelCase JSON
     including `samples`. `At` is in Unix ms.
   - `Start`: decodes `{sessionId, spec}` into a JSON form of `watch.Spec`
     (`watch.go:63`): name, kind, command, jobId, path, condition, mode,
     notify, resume, `intervalMs`. Default `Owner` to `"studio"` when
     unset. Call `Start(spec)` (`watch.go:254`) and return `{id, name}`.
     Pass the manager's limit errors (`MaxWatches`, `MinInterval`) back
     with `invalidParamsError`.
   - `Stop`: `Stop(id)` (`watch.go:531`).
3. Forwarding. The first `watch_list` or `watch_start` for a session
   subscribes once, per session, to its watch broker with
   `Subscribe(ctx)`. It forwards each event as
   `session/update {kind:"watch", event:{watchId, name, kind, state, sample, owner, mode}}`
   (`watch.Event`, `watch.go:119`). The subscription is cancelled when the
   session goes away; reuse W2.1's idle loop exit, which already detects
   that.
4. Register the three methods and add the capability `watchAccess`.
5. Tests:
   - start a `file` watch on a temp file, list it, change the file, and a
     `watch` update arrives;
   - stop it;
   - an over-limit start errors.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestWatch' -race -v
```

---

## Task 9: Per-session routing on `session/new`

**Goal:** `session/new` accepts `routing: {profile?, overrides?}` and the
session resolves routes with it (spec §6.9).

**Files:**
- `internal/acp/session.go` (`sessionParams` `:121`, the `Create` path)
- `internal/app/app.go` (`routedProviderResolver.withRoleOverrides` `:303`, and the runtime constructor the ACP session manager calls)
- tests in `internal/acp/session_test.go` and `internal/app/*_test.go`

**Steps:**

1. Add `Routing *RoutingParams "routing,omitempty"` to `sessionParams`,
   where `RoutingParams{Profile string; Overrides map[string]string}`.
   Validate that:
   - the profile exists in the loaded config's `AgentProfiles`;
   - each override key is a known `routing.AgentRole`;
   - each value is a known preset.

   Bad values return `invalidParamsError`.
2. Thread the parameters into runtime construction. Find how `Create`
   builds a runtime for a new session (the session manager's runtime
   factory; follow `NewSessionManager`'s config fields).
   - `Profile` sets the loaded config's `Profile.Default` before the
     resolver is built.
   - `Overrides` wrap the resolver with `withRoleOverrides`, the same
     helper the per-run SDD and swarm overrides use (`app.go:1094`,
     `:1393`).
   - Expose this as a functional option on the factory, so the TUI path
     is untouched.
3. Tests:
   - `session/new` with an override for `implementer` →
     `session/agents_roster` (`memory.go:199`) reports the overridden
     preset for that role;
   - an unknown preset is rejected;
   - no `routing` behaves exactly as before.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ ./internal/app/ -run 'Routing|Roster' -v
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
go vet ./...
gofmt -l .
```

Expected: only the five known failures, and no `gofmt` output.

## Integration notes

- **New capabilities:** `runDetail`, `watchAccess` (session) and
  `configAccess` (agent). W3.2 checks them.
- **`Depends on:` lines** are stripped from task bodies before prompts are
  built. Existing plans without them behave exactly as before.
- **`usage` in telemetry** is additive. Older bridges ignore unknown keys.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. Each has a focused `go test`. |
| Anchors verified? | Checked on `2ddc09e`: `TaskSpec`/`ParsePlan` (`plan.go:24/44`), `SDDStatusResult` (`turn.go:1483`), `SwarmStatus` (`:1449`), `sessionIDParams` (`:1424`), `buildTelemetry` (`:1176`), `SDDProgress()`, `SDDGate()`, the `Ledger` methods, `RunEvents`, `config.Config` (`types.go:10`), `writeSections` (`save.go:184`), the save functions (`:101/700/766/800`), `UserConfigPath`, `routing.AllRoles` (`:95`)/`FastRoles` (`:50`), `WithRoleOverride` (`router.go:383`), `withRoleOverrides` (`app.go:303`, used at `:1094/:1393`), `sessionParams` (`session.go:121`), `SessionManager.Get` (`:624`), `app.Runtime.WatchManager`/`WatchBroker`, `watch.Info`/`Spec`/`Start`/`Stop`/`Event`, `RecentTurnMetricsForSession`/`InsertTurnMetrics`, `EstimateCostCents`, the `host.go:360` DB cast, `probe.Provider` (`:31`). |
| Code compilable in isolation? | No verbatim code. Every step is prose with exact names. |
| Placeholders? | None. Task 4 step 3 and Task 9 step 2 tell the executor to locate a code path by a named search, which is a judgment step, not a TBD. |
| Matches the spec? | §4 and §6.9. |
