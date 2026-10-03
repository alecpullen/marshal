# W3.2 · Bridge: control agent, runs, library, models, usage, watches — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w3-runs-and-control-design.md`](../specs/2026-10-03-w3-runs-and-control-design.md) §5, §6.8, §6.9
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** backend. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W3.1](2026-10-03-w3-1-engine-plan.md) (previous backend plan), with everything it depends on. Backend plans never depend on UI plans.
**Base:** a branch containing every plan listed under Runs after. Bridge anchors were checked on
`2ddc09e`. W1 and W2 bridge additions (`ErrUnsupported`, `Registry.call`,
`isMethodNotFound`, `EventLog.Broadcast`, the `gate` delta, workspace v7)
are named as defined in those plans.
**Plan slug:** `w3-2-bridge-control`. Commit each task as
`w3-2-bridge-control: task N — <title>`.

## Goal

The bridge provides:

- a control agent, and shared config and data homes for containerized
  agents;
- routes for runs, library, models, usage, budgets and watches;
- the `run`, `watch` and `budget` fleet deltas;
- a roster proxy route for the Runs page;
- budget enforcement and the reroute action.

## Non-goals

- UI (W3.3, W3.4).
- Secrets for provider keys (W4).

## Assumptions

- W3.1 agents implement:
  - `session/run`, `run_progress`;
  - the `config/*` methods;
  - `usage` in telemetry;
  - `session/watch_*` and `watch` updates;
  - `routing` on `session/new`.
- The bridge stays standard library only (`TestWebIsStdlibOnly`).
- Test helpers checked on `2ddc09e`:
  - `testFleet` (`fleet_test.go:13`), `testFleetWithRunner`
    (`derive_test.go:105`);
  - `newTestServer` and `doReq` (`http_test.go:19/39`);
  - `testFleetWithAudit` and `hasEvent` (`audit_test.go:126/153`);
  - `helperCommand` (`child_test.go:48`);
  - the `commandRunner` seam (`container.go:71`).

---

## Task 1: Shared homes for containerized agents

**Goal:** every agent container mounts `home/config` read-only and
`home/data` read-write from the state volume, and sets
`XDG_CONFIG_HOME` and `MARSHAL_DATA_DIR` (spec §5.1).

**Files:**
- `web/bridge/container.go` (`ContainerConfig` `:34`, `buildRunArgs` `:138`), `container_test.go`
- `web/bridge/mounts.go` (`volumeMount` `:16`)
- `web/bridge/fleet.go` (`newRuntime` closure `:233-265`)

**Steps:**

1. Add a `readonly bool` parameter to `volumeMount`. When true, append
   `,readonly` to the `--mount` value. Both docker and podman accept it.
   Update every existing caller to pass `false`.
2. Add `HomeConfigSubpath`, `HomeDataSubpath string` and
   `HomeConfigWritable bool` to `ContainerConfig`, plus the constants
   `containerConfigDir = "/marshal/config"` and
   `containerDataDir = "/marshal/data"`.
3. In `buildRunArgs`, when `HomeConfigSubpath != ""`, add both mounts
   after the socket mount:
   - config with `readonly = !HomeConfigWritable`;
   - data read-write.

   Then set the two env vars. Insert them into the env map before the
   sorted `-e` loop, so the ordering stays deterministic.
4. In the `newRuntime` closure, set `HomeConfigSubpath: "home/config"`
   and `HomeDataSubpath: "home/data"` for agents. Create
   `<stateDir>/home/config` and `<stateDir>/home/data` with `0o700`
   before the first spawn. Do this when `stateDir` is a local path; with a
   named volume, the runtime creates the subpaths. If it doesn't, document
   that in the README.
5. Tests:
   - extend the `buildRunArgs` assertions: the two mounts, the
     `readonly` flag, and the env vars;
   - `TestContainerRunArgsRefuseHostEscapes` (`container_test.go:64`)
     still passes.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestContainer|TestBuildRunArgs' -v && go test ./
```

---

## Task 2: Control agent runtime

**Goal:** `Fleet` owns a lazily started control agent with a control
session, and can open cached per-project control sessions (spec §5.1).

**Files:**
- `web/bridge/control.go` (new), `control_test.go` (new)
- `web/bridge/fleet.go` (`Fleet` struct, `NewFleet` `:198`, shutdown path)

**Steps:**

1. Define
   `controlRuntime{mu; child *Child-or-transport; reg *Registry; sessionID string; projectSessions map[string]string; caps map[string]bool; started bool}`.
   - **Container mode:** build it the way the `newRuntime` closure builds
     an agent:
     - container name `marshal-control` (not `containerNameFor`);
     - the default runtime profile image;
     - `HomeConfigWritable: true`;
     - a `WorkSubpath` of `control`.
   - **Process mode:** use `&Child{MarshalBin: marshalBin}`, as the
     fallback does (`fleet.go:233-238`).
2. Add `func (f *Fleet) control(ctx) (*controlRuntime, error)`:
   - It starts the runtime once (`Open`, then `initialize`), stores the
     capabilities from the `initialize` result (`agentCapabilities` and
     `sessionCapabilities`), and opens the control session with
     `session/new {cwd: <agent path of state/control>, mcpServers: []}`.
   - It restarts on a lost connection. Watch `Wait()` in a goroutine and
     mark the runtime not started, so the next call restarts it.
3. Add `func (f *Fleet) controlCall(ctx, method string, params map[string]any, session bool) (json.RawMessage, error)`.
   - With `session` set, it adds `sessionId`.
   - It maps method-not-found to `ErrUnsupported{feature}`, where the
     feature is the part of the method name after `/`.
4. Add `func (f *Fleet) projectSession(ctx, root string) (string, error)`.
   - It opens and caches `session/new {cwd: agentPath(root)}` on the
     control agent.
   - In container mode, the root must be translatable through the
     project mounts. Otherwise it returns
     `ErrUnsupported{"project_library"}`.
5. Shut the control agent down with the fleet: find the fleet's stop path
   (`f.done` is closed there; see `StartPoller`, `poller.go:20`) and call
   `Kill`.
6. Tests:
   - process mode with `helperCommand("registry")`: the first call starts
     it, the second reuses it, and after the fake child exits the next
     call restarts it;
   - container mode: `commandRunner` captures `run` args containing
     `marshal-control` and the writable config mount;
   - project sessions are cached per root.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestControl' -race -v && go test ./
```

---

## Task 3: Runs routes and the `run` delta

**Goal:** `GET /api/runs`, `GET /api/runs/{agentId}`, `POST /api/runs`,
`POST /api/runs/{agentId}/answer`, and the fleet `run` delta (spec §5.2).

**Files:**
- `web/bridge/runs.go` (new), `runs_test.go` (new)
- `web/bridge/fleetevents.go` (`classifyNotification` `:22`, `fleetDelta`, `liveState`)
- `web/bridge/intake.go` (`startPlan` `:201`)
- `web/bridge/http.go`

**Steps:**

1. In `classifyNotification`, read `update.run` as `json.RawMessage`.
   - Map kind `run_progress` to `fleetDelta{Kind: "run", Run: raw}`. Add
     `Run json.RawMessage "run,omitempty"` to `fleetDelta`.
   - `liveState.apply` stores the latest `run` per session in
     `agentLive.run` and `runAt`.
2. Generalise `startPlan` into
   `func (f *Fleet) StartRun(ctx, agentID string, req RunRequest) error`,
   where `RunRequest{Kind, Plan, PlanPath, Goal}`.
   - SDD writes `Plan` to `planPathFor(a.Project, newAgentID())` when
     given, or uses `PlanPath` as is. It then sends `session/sdd_start`.
   - Swarm sends `session/swarm_start {goal}`.
   - Both run the request in a goroutine with `context.WithoutCancel`. A
     final error is recorded on `agentLive.runErr`.
   - `startPlan` becomes a thin wrapper, so intake keeps working. Run the
     existing intake tests to prove it.
3. Handlers:
   - `listRuns`: agents whose live state has a `run`. Returns
     `[{agentId, name, project, run, at}]`, newest first.
   - `getRun`: `RuntimeForSession`, then `session/run`. Returns 501 when
     unsupported.
   - `startRun`: body
     `{agentId?, project?, kind, plan?, planPath?, goal?}`.
     - Without `agentId`, spawn first through `f.Spawn(ctx, project, SpawnOptions{Name: …, Origin: OriginUI, Isolated: true})`.
     - `kind` is required: `sdd` needs `plan` or `planPath`, and `swarm`
       needs `goal`.
     - Returns 202 `{agentId}`.
   - `answerRun`: `session/sdd_answer {answer}`.
4. Register the routes. Each new route checks budgets (Task 7) before
   starting work.
5. Tests:
   - the classifier maps `run_progress`;
   - list returns the agent after a fake `run_progress` notification;
   - start with a plan writes the file and sends `sdd_start` (fake child
     records it);
   - swarm sends `swarm_start`;
   - answer proxies;
   - the old intake plan path still works (existing `intake_test.go`).

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestRuns|TestIntake|TestClassify' -v && go test ./
```

---

## Task 4: Library routes

**Goal:** the skills, plugins and memory routes, backed by control and
project sessions (spec §5.2).

**Files:**
- `web/bridge/library.go` (new), `library_test.go` (new)
- `web/bridge/http.go`, `web/bridge/audit.go`

**Steps:**

1. A helper `librarySession(ctx, scope, project string) (string, error)`
   picks the session:
   - `global` uses the control session;
   - `project` uses `projectSession(root)`.

   The `scope` sent to ACP is the same string. The ACP side validates it
   (`internal/acp/skills.go:61`).
2. Skills:
   - `GET /api/library/skills?scope&project` → `session/skills_list`;
   - `POST /api/library/skills/preview {source}` →
     `session/skills_install_preview`, run on the control session (the
     preview is scope-free);
   - `POST …/confirm {stagingToken, scope, project?}` →
     `skills_install_confirm`, on the session that will own the skill;
   - `POST …/discard {stagingToken}`;
   - `DELETE /api/library/skills/{name}?scope&project` → `skills_remove`.
3. Plugins: the same four operations against the `plugins_*` methods
   (`plugins.go`). The parameter is `scanToken`, and the scan takes
   `{source, ref}`.
4. Memory, all on `projectSession(root)`:
   - `GET /api/library/memory?project` → `memory_list`;
   - `DELETE /api/library/memory/{id}?project` → `memory_delete`;
   - `POST /api/library/memory/{id}/confidence?project {confidence}` →
     `memory_set_confidence`.
5. Add these audit constants and log them on success:
   `skill_installed`, `skill_removed`, `plugin_installed`,
   `plugin_removed`, `memory_deleted`.
6. Tests, against a control agent backed by the registry helper child.
   Extend the helper's fake method table to answer these methods with
   fixed JSON, the way other bridge tests stub ACP replies.
   - each route proxies with the right session ID;
   - `project` scope in container mode without a mount returns 501;
   - audit entries are written.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestLibrary' -v && go test ./
```

---

## Task 5: Models routes and the spawn routing option

**Goal:** the `/api/models…` routes, and `routing` passed through spawn to
`session/new` (spec §5.2, §6.9).

**Files:**
- `web/bridge/models.go` (new), `models_test.go` (new)
- `web/bridge/http.go` (`spawnAgent` `:287`), `web/bridge/fleet.go` (`SpawnOptions` `:656`, session/new params `:863`)

**Steps:**

1. Routes, each a `controlCall` without a session:
   - `GET /api/models` → `config/get`;
   - `PUT /api/models/providers` → `config/set_providers`;
   - `PUT /api/models/providers/{name}/key {key}` →
     `config/set_provider_key`;
   - `PUT /api/models/presets` → `config/set_presets`;
   - `PUT /api/models/routing` → `config/set_routing`;
   - `POST /api/models/probe` → `config/probe_provider`.

   Each mutation writes the audit event `models_changed`, with the section
   name in `Detail`. The key route never logs the key.
2. Add `Routing json.RawMessage` to `SpawnOptions`. Have `spawnAgent`
   decode an optional `routing` field and pass it through. In the
   `session/new` params map (`fleet.go:863`), set `params["routing"]` when
   it's non-empty.
3. Tests:
   - each route proxies;
   - the key body is never written to the audit log;
   - spawn with `routing` sends it in `session/new` (the fake child
     records the params).

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestModels|TestSpawn' -v && go test ./
```

---

## Task 6: Usage ledger and `GET /api/usage`

**Goal:** telemetry `usage` rows are written to a monthly JSONL ledger and
aggregated (spec §5.3).

**Files:**
- `web/bridge/usage.go` (new), `usage_test.go` (new)
- `web/bridge/fleet.go` (`attachClassifier`, the telemetry path near `:1125`)

**Steps:**

1. Define `UsageRow` with JSON fields: the engine row's fields, plus
   `agentId`, `project` and `origin`.
2. Add `UsageLog{dir string; mu; seen map[string]struct{}; order []string}`:
   - `NewUsageLog(stateDir)` opens `<state>/usage`.
   - `Append(rows []UsageRow)` writes to `YYYY-MM.jsonl`, chosen by each
     row's `startedAt` month. It dedupes on `agentId+":"+id`, with an
     LRU of 10,000 keys.
   - `Range(from, to time.Time) ([]UsageRow, error)` reads the needed
     months.

   Model it on `AuditLog` (`audit.go:65/75/125`).
3. In the fleet's notification classifier (`attachClassifier`), when a
   `session_telemetry` update carries `usage`, decode it, add the agent
   ID, project and origin from the agent, call `Append`, then call the
   budget check (Task 7).
4. `GET /api/usage?range=7d|30d&by=day|project|role|model` aggregates as
   in spec §5.3:
   - `agentHours` is the sum of `durationMs` / 3.6e6;
   - `prsShipped` counts audit `push` events in the range whose `Detail`
     contains a PR URL. Read them with `AuditLog.Tail(maxAuditTail)` and
     filter by `TS`.
5. Tests:
   - append and dedup;
   - month rollover;
   - aggregation by each `by` value;
   - `prsShipped` from an audit fixture.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestUsage' -v && go test ./
```

---

## Task 7: Budgets

**Goal:** the daily and per-agent caps are enforced with `warn`, `block`
and `pause` actions, plus 429s, an override, and the `budget` delta
(spec §5.4).

**Files:**
- `web/bridge/budget.go` (new), `budget_test.go` (new)
- `web/bridge/http.go` (`spawnAgent`, `prompt`, `startRun`), `web/bridge/audit.go`

**Steps:**

1. Add `budgetState{mu; cfg Budgets; daySpend map[string]float64; agentSpend map[string]float64; blockedDay string; paused map[string]bool; overridden map[string]bool}`.
   - `Budgets` mirrors the engine's `[budgets]` keys in camelCase.
   - Load it from `config/get` on first use, and on
     `PUT /api/budgets` → `config/set_budgets`, which writes the audit
     event `budgets_changed`.
   - Seed today's spend and per-agent totals from `UsageLog.Range` on
     first load.
2. `func (f *Fleet) checkBudgets(row UsageRow)` adds the row's spend.
   - Daily over the cap: `warn` appends a `budget` delta; `block` also
     sets `blockedDay` to today.
   - Agent over its cap, and not overridden: `warn` appends a delta;
     `pause` sets `paused[agent]` and calls `rt.reg.Cancel` (`registry.go:227`).
3. `func (f *Fleet) budgetGate(agentID string) error` returns
   `ErrBudget{Scope}` when the day is blocked or the agent is paused.
   - Call it at the top of `spawnAgent`, `prompt` and `startRun`.
   - `writeErr` maps `ErrBudget` to 429
     `{"error":"budget_exceeded","scope":…}`.
4. `POST /api/agents/{id}/budget/override` sets `overridden[id]`, clears
   `paused[id]`, and writes the audit event `budget_override`.
5. `GET /api/budgets` returns the config plus the current spends.
6. Tests:
   - daily block returns 429 on spawn and prompt;
   - raising the cap lifts the block;
   - the agent pause cancels the turn (the fake child records
     `session/cancel`);
   - override lifts the pause;
   - warn only emits a delta;
   - the day changes at UTC midnight (inject the clock).

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestBudget' -race -v && go test ./
```

---

## Task 8: Watches routes, the `watch` delta, and reroute

**Goal:** watch list, create and stop for agents and the Studio; the
`watch` fleet delta; the reroute action with undo (spec §5.2, §6.8).

**Files:**
- `web/bridge/watches.go` (new), `watches_test.go` (new)
- `web/bridge/fleetevents.go`, `web/bridge/workspace.go`, `web/bridge/http.go`, `web/bridge/audit.go`

**Steps:**

1. Classifier: map `session/update` kind `watch` to
   `fleetDelta{Kind: "watch", Watch: raw}`. For control-agent
   notifications, set `SessionID` to `"studio"`. The control agent's child
   gets its own `OnNotification`, which appends to the fleet log.
2. Routes:
   - `GET /api/watches` merges `session/watch_list` from the control
     session (owner `studio`) and from every running agent that has the
     `watchAccess` capability, called in parallel with a 3s timeout each.
     Returns `[{agentId|"studio", ...info}]`.
   - `POST /api/watches {agentId?, spec, onTrip?}` → `session/watch_start`
     on the chosen session.
   - `DELETE /api/watches/{owner}/{id}` → `session/watch_stop`.
   - Audit `watch_started` and `watch_stopped`.
3. Reroute:
   - **Rules.** `onTrip.reroute {role, preset}` is allowed only on Studio
     watches. Store it in `fleet.json` as `watchRules` (watch ID →
     rule), which bumps the workspace to version 8, the same way W2.1
     added `reviews`.
   - **Applying.** On a `watch` delta from `studio` with state `fired`
     and a rule:
     1. Read the current binding with `config/get`.
     2. Apply the new one through `config/set_routing` on the active
        profile.
     3. Record `Reroute{ID, WatchID, Role, From, To, At}` in memory.
     4. Append the fleet delta
        `{kind:"reroute", reroute}`.
     5. Audit `models_changed` with `Reason: "watch:<name>"`.
   - **Undo.** `POST /api/reroutes/{id}/undo` restores `From` and audits.
4. Tests:
   - list merges the Studio's and agents' watches;
   - start and stop proxy;
   - a fired Studio watch with a rule triggers `set_routing` with the new
     preset, then undo restores the old one;
   - the v7 → v8 migration.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestWatches|TestReroute|TestWorkspace' -v && go test ./ && go vet ./
```

---

## Task 9: Roster proxy route

**Goal:** `GET /api/sessions/{id}/roster` proxies `session/agents_roster`,
for the Runs page's roles legend (W3.3).

**Files:**
- `web/bridge/registry.go`, `web/bridge/http.go`, `web/bridge/http_test.go`

**Steps:**

1. Add `func (r *Registry) Roster(ctx, id string) (json.RawMessage, error)`,
   one line on W2.1's `Registry.call` with method `session/agents_roster`
   (`internal/acp/memory.go:199`) and feature `roster`.
2. Register `GET /api/sessions/{id}/roster` → `sessionRoster`, which copies
   W1.1's `sessionStack` handler: `registryForSession`, then the call,
   then the raw JSON.
3. Tests: pass-through, 404 for an unknown session, and 501
   `roster_unsupported`, using the same server helper as W1.1's
   `TestSessionStackRoute`.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestSessionRoster' -v && go test ./
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./...
cd web/bridge && go test ./... -race && go vet ./...
cd ../.. && CGO_ENABLED=1 go test ./... ; gofmt -l .
```

Expected:
- the bridge passes under `-race`, including `TestWebIsStdlibOnly`;
- the repo-wide run fails only on the five known tests;
- `gofmt -l .` prints nothing.

## Integration notes

- **Upgrade order.** Containers started before Task 1 don't have the
  shared homes. They pick them up on their next spawn; there is no live
  migration.
- **Container naming.** The control container is named `marshal-control`.
  `listAgentContainers` filters on `marshal-agent-`, so reattach logic
  doesn't confuse the two.
- **Budgets** depend on usage rows, which need a W3.1 agent. Older agents
  report no spend.
- **Workspace version** moves to 8 (`watchRules`).

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. Each has `go test -run` targets. |
| Anchors verified? | Checked on `2ddc09e`: `ContainerConfig`/`buildRunArgs`/`commandRunner`/`TestContainerRunArgsRefuseHostEscapes` (`container.go:34/138/71`, `container_test.go:64`), `volumeMount` (`mounts.go:16`), the `newRuntime` closure and process fallback (`fleet.go:233-265`), `NewFleet` (`:198`), `SpawnOptions` (`:656`), the session/new params (`:863-874`), `classifyNotification`/`fleetDelta`/`liveState` (`fleetevents.go:9/22/77-100`), `startPlan` (`intake.go:201`), `Registry.Cancel` (`registry.go:227`), `AuditLog` (`audit.go:65/75/125`), `spawnAgent` (`http.go:287`), `StartPoller`/`f.done` (`poller.go:20`). W1/W2 symbols are as defined. |
| Code compilable in isolation? | No verbatim code. Every step is prose. |
| Placeholders? | None. |
| Matches the spec? | §5, §6.8, §6.9. |
