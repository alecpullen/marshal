# Agent roles — feature spec

Status: design approved in sections 2026-11-07 (five-section review, all
approved); this spec consolidates the approved design for implementation
planning. Planned 2026-11-10
(`plans/2026-11-10-agent-roles-plan.md`), which amends C5, decision 5 and
decision 6's mechanism as noted inline.

## Context

Marshal has exactly one agent persona: the general-purpose main agent. Every
session starts the same way, with the same tool surface and the same
delegation behaviour. Power users who want the agent to behave differently —
most notably as a *coordinator* that directs work instead of doing it — have
no way to say so.

Everything a roles feature needs to compose over already exists:

- **Routing roles** (`internal/llm/routing/types.go`): `AgentRole` constants,
  `AllRoles`, `FastRoles`, `EffectiveBinding`, and `ResolveRoleIfBound` — the
  model-binding layer. These are *model* bindings, not behaviour modes.
- **Delegation** (`internal/agent/subagent.go`): the `agent.run` tool
  (`NewSubagentTool`, `subagent.go:220`) plus the `agent.await`, `agent.kill`
  and `agent.steer` family — `SubtaskScopeView` (`subagent.go:117-138`)
  excludes exactly those four from a subtask child's registry, which is how
  we know the parent-facing steering machinery exists. Children resolve their
  own models through `SubagentRequest.Role` → `ResolveRoleIfBound`, and a
  model-consent gate (`SubagentModelResolver`, `WithSubagentParentModel`)
  already guards cost surprises.
- **Tool scoping** (`internal/agent/swarm/orchestrator.go:21-27`,
  `internal/app/app.go:1633+`): `RegistryScope` values and the
  `roleRunnerSpec.newRunner` scope→registry switch (`ScopeReadOnly` →
  `s.readOnlyReg`, `app.go:1629`), plus `SubtaskScopeView` as the
  filter-a-registry pattern.
- **The runner's turn-build seam** (`internal/agent/route.go:604`):
  `systemPromptMessage()` composes `BuildSystemPromptWithSystem` with
  `Role`, `Tools`, `Roster`, `Addendum` — the persona has an existing home.
- **The client seam**: P2 has landed. The TUI reaches the agent side only
  through `acpclient.Session` (`TestTUIStaysOnTheClientSeam`,
  `internal/app/tui/boundary_test.go:183-186`). Daemon work is at P4
  (`docs/daemon/p4-local-daemon.md`).

**On `/swarm`** (recorded because it shaped the scope boundary): an approved
archive design exists — `docs/context-diet/swarm-archive-design.md`, design
approved 2026-10-15, spec awaiting review, lands after Tier 2a. It deletes
swarm from main (orchestrator, `/swarm`, `session/swarm_start`, config, web
surfaces) and moves the shared pieces to their survivors:
`swarm.WriteLock` → `agent.WriteLock` (the single write gate for the parent
runner and every `agent.run` child) and `swarm.RunnerFactory` /
`swarm.RegistryScope` → `pipeline.RunnerFactory` typed over
`pipeline.RegistryScope`. Swarm is still live in main as of this spec — P2
just moved it through the session seam (`0ff0b0fe`) — so this feature stays
orthogonal to it, and its scope machinery is written to align with the
archive's survivor types rather than the package approved for deletion.

## Goals

- A **roles framework**: role definitions in config, built-in defaults plus
  user-defined roles, with persona, tool scope, delegation roster, and an
  optional model binding.
- **Session-mode lifetime**: selecting a role reshapes the session until
  switched; `/role <name>` and a chrome indicator drive it.
- The **coordinator** as the first built-in role: read + delegate, never
  writes, answers directly when context suffices, otherwise delegates to
  framework roles or ad-hoc subagents via the existing `agent.*` family.
- The role observable through the ACP seam so later surfaces (web UI) can
  consume it without a contract break.

## Non-goals

- Touching `/swarm`, `/pipeline`, the swarm orchestrator, or the pipeline
  dispatcher — and neither accelerating nor delaying the approved swarm
  archive. When the archive lands, this feature's scope mapping re-points at
  `pipeline.RegistryScope`; nothing else changes.
- The web UI's own role picker. The contract field ships so it *can* consume
  the role; its surface is a follow-on.
- Role composition/inheritance chains; multiple simultaneous roles; role
  inheritance into in-flight subagents.
- Structural roster enforcement. The roster is declared in config and
  rendered into the persona; the **tool scope** is the structural gate.
- Changes to the model-consent gate, subagent depth/concurrency counters, or
  the policy engine.
- A config-level default role in v1 (see Assumptions).
- Persona templating beyond a plain string.

## Decisions

Settled in interrogation, with the trade-off that decided each:

1. **Framework, not a built-in set.** Roles are a new `[roles]` TOML table
   parallel to `[custom_agents]` (`internal/app/config/file_types.go:387`),
   with built-in defaults in `config.Default()`. Trade-off: full config
   surface (parsing, merge, diagnostics, panel editing) over the smallest
   possible coordinator hack.
2. **Project-local roles allowed.** Only `[providers]` and
   `[models.presets]` are user-global-only (`hoist.go:29-44`, AGENTS.md);
   `[roles]` follows `custom_agents` and may live in a trusted project's
   config. No hoisting.
3. **Session mode, not per-goal dispatch.** A role is sticky for the
   session. Trade-off: needs persisted session state and an ACP field, but
   "the agent *is* a coordinator" is the requested experience; `/swarm`-style
   per-goal dispatch already exists separately.
4. **Structural scoping.** The coordinator's registry view provably lacks
   file-write and shell tools — reusing the `ScopeReadOnly` construction from
   `newRunner` (`app.go:1629`), extended with the delegation family. A
   prompt-advisory contract was rejected: the model could still edit files.
5. **Reuse the delegation machinery.** `agent.run`/`await`/`kill`/`steer`
   keep their existing mechanics and parent steering of a running child.
   The one addition is scope-conditional: a role that declares a `roster`
   gains an optional `role` parameter on `agent.run` (see C5), so the
   capability is declared and validated for the agents that have it, and
   every other session's schema is unchanged byte-for-byte.
6. **Delegation targets compose.** Framework roles (resolved through
   `ResolveRoleIfBound`) *and* ad-hoc subagents. A user-defined role can be
   a delegation target with its own persona, scope, and binding.
   Mechanism: `SubagentRequest.ConfigRole` carries a `[roles.<x>]` name
   alongside the routing `Role`; the child resolves it through `ResolveRole`
   and applies the persona as the construction-time `Addendum` — legitimate
   here because a child's role is fixed for its single task, unlike the
   per-turn parent persona (C4) — and the scope through `RoleScopeView`.
7. **Optional model binding, default unbound.** The schema keeps a `preset`
   field; the built-in coordinator ships unbound so `/role` changes
   behaviour, never cost. A bound role switches the model at the turn
   boundary through the existing resolution path.
8. **Approach A — role applied at the runner's turn-build seam.** Session
   state gains a persisted `ActiveRole`; each turn the runner resolves it and
   applies persona + view + binding. Rejected: a role wrapper runner
   (forwards a large Runner surface, fights history-preserved switching) and
   persona-only composition (weakens decision 4).
9. **C6-style additive ACP field** (`activeRole`) plus a set method in the
   `session/*` family, per the P2 precedent that existing messages gain
   optional fields only. Sequenced with the P4 plans (below).
10. **Coordinator vertical slice.** Full framework scaffolding, one built-in
    role.
11. **Switch at next turn boundary, history preserved.** The runner pins the
    resolved role once at turn start (the `r.promptAccess`/`promptAccessMu`
    precedent, `route.go:590-596`); mid-turn rebuild sites
    (`runner.go:1384`, `runner.go:1534`) reuse the pin, so a mid-turn
    `/role` never rewrites `messages[0]`. One prefix-cache invalidation at
    the boundary is the accepted cost.
12. **Orthogonal to swarm in v1**, aligned with its approved archive:
    coexistence now; the scope mapping re-points at the archive's survivor
    types when it lands.

## Design

### Scope & architecture

In scope: the `[roles]` table; the built-in `coordinator`
(persona = director/delegator; scope = read-only + delegation; unbound
model; roster = `repo_scout`, `implementer`, `tester`, `reviewer`, plus
ad-hoc); `session.State.ActiveRole` with a persistence migration; the runner
turn-build seam; `/role <name>`; the chrome indicator; a roles section in the
agents panel for authoring; one additive ACP session field.

Architectural shape: one new persisted session field, one new config table,
one registry-view construction, one turn-build branch. No new runner type,
no new dispatch path, no seam change beyond one additive field.

Flow: role definition (config) → resolved role (config + routing profile) →
applied at turn-build (persona + view + binding) → observable via session
state → exposed by the ACP field and TUI surfaces.

### Components & data flow

- **C1 — Config schema** (`internal/app/config`): `[roles]` in
  `file_types.go`; merge in `merge.go` (project layer overrides user for a
  same-named role, per the `custom_agents` pattern); validation in
  `diagnostics.go` (roster entries must name known roles/custom agents;
  `preset` must exist). Built-in coordinator in `config.Default()`.
- **C2 — Role resolution** (`internal/agent`, alongside the runner seam —
  verified import direction: `internal/agent` already imports
  `internal/app/config` in production code at `prompts.go:9`, `route.go:7`,
  `snapshot_warning.go:12`): name → resolved role (definition from config;
  model from the preset, unbound = keep the session's model; scope as a
  scope value). Reuses the profile machinery; no new routing.
- **C3 — Session state** (`internal/app/session`): `ActiveRole` with
  `SetActiveRole()`/`ActiveRole()`, persisted by a `migrations.go` entry so a
  resumed or P4-reattached session restores its role.
- **C4 — Runner turn-build seam** (`internal/agent`): at turn start resolve
  `State.ActiveRole`; compose the persona into
  `BuildSystemPromptWithSystem` as a new persona option in
  `SystemPromptOptions` — deliberately *not* the construction-time
  `Addendum` field (`runner.go:371`), which is fixed at runner build while
  the persona is resolved per turn from session state; narrow the registry
  via a `RoleScopeView` mirroring `SubtaskScopeView`; resolve the binding
  only when set. Pin the resolved role for the turn (decision 11).
- **C5 — Delegation**: unchanged — the `agent.*` family and the existing
  `agentRoster()` rendering.
- **C6 — Commands** (`internal/commands`): `/role <name>` with a `Handler`
  (`types.go:17`); no-arg opens the TUI picker.
- **C7 — TUI** (`internal/app/tui`): chrome indicator reading `ActiveRole`
  via `acpclient.Session`; a roles section in the agents panel mirroring the
  `cfg.CustomAgents` section (`panel.go:200-340`); `/role` dispatch and
  picker. No direct agent-side imports.
- **C8 — ACP seam** (`internal/acpwire` + `internal/acp`): one additive
  optional session field plus a set method, over the in-process adapter
  first; sequenced with the P4 plans.

`/role coordinator` happy path: user types it → TUI → `Session.SetActiveRole`
→ agent side validates the name against resolved roles, writes
`State.ActiveRole`, persists → next turn boundary the runner applies
persona + scope + binding, prefix cache invalidates once → chrome, the ACP
field, and (later) the web UI observe `ActiveRole`.

Resume/reattach: the persisted `ActiveRole` loads with the session; the
reattached turn builds with the same persona and scope. No seam change —
just the migrated column.

### Error handling

1. **Invalid role config** (malformed entry, unknown preset, unknown roster
   entry): `Diagnostic` with `SeverityError` at `roles.<name>.<field>`,
   mirroring the profile/custom_agent precedent (`diagnostics.go:104-118`).
   An unresolvable name is treated as absent; the session still runs.
2. **`/role <unknown>`**: the Handler validates against resolved roles
   before touching state; returns an error Result (the standard command
   convention; headless callers see the same string). No state change.
3. **Role vanishes while active**: resolver returns `ok=false`; next turn
   falls back to the default persona and full scope; the chrome indicator
   renders the stale name flagged missing. The next valid `/role` or config
   reload repairs it.
4. **Switch mid-turn**: the turn-start pin (decision 11) means the current
   turn keeps its persona; the switch applies at the next turn start. The
   Handler reads the session `Activity` to word feedback honestly ("set;
   applies from the next turn" when busy). The hold mechanism is not used —
   a switch never pauses the agent.
5. **Persistence failure**: fail-open — the in-memory switch applies so the
   live session is never blocked; the failure is logged through
   `session.Persistence.Logger`; worst case a resume lands on the default
   persona.
6. **Untrusted project**: project-local roles load only through the trust
   resolver, like every other project-config section.
7. **ACP set with unknown name**: service-side validation returns an
   acpwire error code; state unchanged.
8. **Switch during in-flight delegation**: children keep their own roles
   (`SubagentRequest.Role`); a parent switch never propagates. Documented;
   inheritance is a non-goal.
9. **Empty persona**: valid — a scope-only role is legal; the built-in
   coordinator ships a persona.
10. **Cache invalidation on switch**: one prefix rebuild at the boundary;
    accepted cost, no failure path.

### Testing

1. **Config** (`internal/app/config`, under `testenv.Run`): parsing; merge;
   diagnostics; the built-in coordinator resolves from `config.Default()`.
2. **Role resolution**: bound vs. unbound; unknown name → `ok=false`.
3. **Runner turn-build** (`internal/agent`) — the critical set:
   - persona composes into the system prompt when a role is active;
   - narrowing is structural — the coordinator's view provably excludes
     `file.write`;
   - a mid-turn `ActiveRole` write leaves the current turn's `messages[0]`
     untouched; the next turn picks it up;
   - `TestPromptPrefixStableAcrossSteps` and
     `TestPromptPrefixStableAcrossStepsWithReasoning`
     (`cacheprefix_test.go:71`, `:152`) pass **unchanged** — per the
     context-limits spec they are the guard, not something to adapt;
   - a boundary switch invalidates the prefix cache exactly once.
4. **Session state**: `ActiveRole` set/get; persistence round-trip through
   the migration; resume restores the role.
5. **Commands**: `/role` valid switch; unknown name → error Result, no state
   change; no-arg picker path.
6. **ACP seam**: the additive field obeys the wire-field rules; round-trip
   through the in-process adapter; unknown name → error code, state
   unchanged.
7. **TUI**: `TestTUIStaysOnTheClientSeam` still passes; chrome indicator;
   panel roles section; a scripted `/role` journey in `test/usability/`.
8. **Daemon non-interference**: the P4-affected packages' suites still pass;
   `TestWebIsStdlibOnly` unaffected (no `web/` changes in v1).

Validation: the narrow per-package `go test` runs, then
`go test ./test/usability/... -run TestScripted -v -timeout 5m` against a
freshly built `./marshal`, then `go vet ./...` and `gofmt -w .`.

### Sequencing, migration, schema

**Sequencing with the daemon work:**
- The embedded path (config, session state, runner seam, `/role`, chrome,
  panel) lands as its own work with zero seam changes — it cannot interfere
  with P4 by construction.
- The ACP field + set method is one small additive commit. If a P4 plan is
  in flight when it is ready, it sequences after that plan.
- The migration is one additive column in its own commit; it does not alter
  existing columns, so P4's session-persistence work is unaffected.

**Migration:** one nullable `active_role` column on `agent_sessions`
(`migrations.go:30` owns the schema). Empty = default persona; no backfill.
Resume with empty behaves exactly as today; resume with a name resolves it
or degrades per failure mode 3.

**Coordinator persona contract** (required sections, modeled on the swarm
prompt precedent in `swarm/prompts.go`):

1. Identity: you are the coordinator; you direct work, you do not perform
   it.
2. Hard rule: never write files or run commands yourself — delegate.
3. Delegation protocol: how to call `agent.run` with a roster role (or
   ad-hoc), and what roster names resolve to.
4. Answer-directly rule: questions answerable from context already held are
   answered directly, not delegated.
5. Output contract: a final synthesis naming what was delegated, what came
   back, and what (if anything) remains.

**Config schema shape:**

```toml
[roles.coordinator]        # built-in default; user definitions follow the same shape
persona = "You are the coordinator..."   # optional; scope-only roles omit it
scope = "read_only"        # enum: full | read_only | tester | artifact_writer | fallback
roster = ["repo_scout", "implementer", "tester", "reviewer"]  # advisory; rendered into the persona
preset = ""                # optional model binding; empty = keep the session's model
```

**Scope mapping seam:** the config enum maps to the concrete Go scope type at
the resolution seam, not in the schema. Today that is `swarm.RegistryScope`
via `newRunner`; the plan pins the mapping against the tree at execution
time and re-points it at `pipeline.RegistryScope` when the approved swarm
archive lands (decision 12). Role scopes are main-session scopes: `read_only`
selects the same read-only registry construction the swarm's `ScopeReadOnly`
roles receive (`newRunner`, `app.go:1629`), and the main-session role view
then extends it with the delegation family — the same four tools
`SubtaskScopeView` excludes from subtask children, since a main-session role
has a live user and may delegate. The write-gate story (`agent.WriteLock` as
the single gate for the parent and every `agent.run` child) is inherited
from the archive's survivor design and needs no roles-specific change.

## Interfaces and persisted formats

- **TOML**: `[roles.<name>]` — `persona` (string, optional), `scope` (enum:
  `full | read_only | tester | artifact_writer | fallback`), `roster`
  (list of strings, optional), `preset` (string, optional).
- **Session state**: `session.State.ActiveRole string` with
  `ActiveRole()`/`SetActiveRole(name)`; persisted as
  `agent_sessions.active_role TEXT` (nullable; empty = unset).
- **ACP**: one optional additive field (working name `activeRole`) on the
  session wire type — the plan picks among the existing session types
  (`acpwire/status.go:10` `SessionStatus`, `acpwire/stack.go:323`
  `SessionUpdate`, `acpwire/sheet.go:9` `SessionSheet`) — plus a set method
  in the `session/*` family following the `internal/acp/turn.go` precedent,
  with service-side validation returning an acpwire error code on unknown
  names.
- **Runner seam**: the persona composes through
  `BuildSystemPromptWithSystem` (`route.go:604`) as a new persona option in
  `SystemPromptOptions`; the registry view is built by a `RoleScopeView`
  mirroring `SubtaskScopeView` (`subagent.go:117-138`); the resolved role is
  pinned per turn following the `r.promptAccess`/`promptAccessMu` precedent
  (`route.go:590-596`).

## Assumptions

Choices made without an explicit user answer, flagged for review:

- **No config-level default role in v1.** Sessions start with no role; the
  user sets one with `/role`. A config default can be added later without
  schema change.
- The coordinator's exact tool-name set is pinned by the structural test
  (design §Testing.3) rather than enumerated in prose.
- The web UI role picker is a follow-on consuming the same field; not part
  of v1.
- The ACP field/method identifiers are pinned by the plan against the
  existing `acpwire` types (candidates named above).
- The roster is advisory (rendered into the persona); the tool scope is the
  structural gate.
- The swarm relationship framing (orthogonal now, aligned with the approved
  archive later) reflects `docs/context-diet/swarm-archive-design.md`; the
  archive's own spec review is out of this feature's scope.

## Open questions

None. Every fork was settled in interrogation or the five-section design
approval.

## Handoff

After user review of this spec, planning goes to `marshal-writing-plans`
(inline, non-SDD). The plan's first task should re-verify the scope-mapping
seam and the `acpwire` field placement against the tree, since both were
named at the design level.
