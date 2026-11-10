# Agent roles — implementation plan

**Goal:** give the marshal agent selectable "roles" — behavior modes that
reshape a session — with the **coordinator** as the first built-in role: read
+ delegate, never writes, answering simple questions directly and delegating
substantive work to framework roles or ad-hoc subagents through the existing
`agent.run` family.

**Non-goals** (from the spec): touching `/swarm`, `/pipeline`, or the swarm
orchestrator; the web UI's own role picker (the contract field ships so it
*can* consume the role later); role composition chains; multiple simultaneous
roles; structural roster enforcement; changes to the model-consent gate's
policy, subagent depth/concurrency counters, or the policy engine; a
config-level default role in v1.

**Assumptions** (from the spec): the coordinator's exact tool-name set is
pinned by the structural test rather than enumerated in prose; the ACP
field/method identifiers are pinned here against `acpwire` (candidates named
in the spec); the roster is advisory — it is rendered into the persona *and*
used to describe the optional `role` parameter, while the tool scope is the
structural gate; the scope→registry mapping re-points at
`pipeline.RegistryScope` when the approved swarm archive
(`docs/context-diet/swarm-archive-design.md`) lands — until then the plan
uses `registry.ReadOnlyView`/`registry.TesterView` directly, which the
archive does not move.

**Spec:** `docs/agent-roles/feature-spec.md` (approved 2026-11-07; amended in
this plan for C5, the delegation-target mechanism, and the roster rendering).

**Base:** `main` @ `2d26190e`.

## Contracts

```go
// internal/app/config/roles.go (new file)
package config

// Role is one user- or built-in-defined agent behavior mode.
type Role struct {
	Persona string   `toml:"persona,omitempty"` // system-prompt section; empty = scope-only role
	Scope   string   `toml:"scope,omitempty"`   // enum: "" | full | read_only | tester | artifact_writer | fallback; "" = full
	Roster  []string `toml:"roster,omitempty"`  // advisory delegation targets: rendered into the persona and offered as agent.run's "role" enum
	Preset  string   `toml:"preset,omitempty"`  // model binding; empty = keep the session's model
}

// Config gains the field (internal/app/config/types.go, next to CustomAgents at types.go:22):
//   Roles map[string]Role `toml:"roles"`

// internal/agent/roles.go (new file)
package agent

import (
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/provider"
	"marshal/internal/tools/registry"
)

// ResolvedRole is a config role resolved against the active config: the
// built-in coordinator and user roles are indistinguishable by this point.
type ResolvedRole struct {
	Name    string
	Persona string
	Scope   string // the config enum value; "" means full
	Roster  []string
	Preset  string
}

// PersonaText is the role's system-prompt section: Persona, then a stable
// delegation-roster line when the role declares one. Deterministic — roster
// order is author order — so the section is byte-identical across turns and
// safe in the cached prefix.
func (r ResolvedRole) PersonaText() string

// ResolveRole resolves a role name against cfg. Built-in defaults are part
// of cfg (config.Default() ships them), so there is no second source.
// ok is false for an unknown or empty name.
func ResolveRole(cfg config.Config, name string) (ResolvedRole, bool)

// delegationTools are the four SubtaskScopeView excludes from a subtask
// child (subagent.go:132-150), re-added for a main-session role that may
// delegate. agent.await, agent.kill and agent.steer only work with a live
// parent turn, so they are main-session-only.
var delegationTools = []string{"agent.run", "agent.await", "agent.kill", "agent.steer"}

// RoleScopeView returns the registry view for a main-session role. It
// mirrors SubtaskScopeView: a fresh registry built from src.List().
//   read_only  -> registry.ReadOnlyView(src) + the delegation family
//   tester     -> registry.TesterView(src) + the delegation family
//   full/""    -> a copy of src
//   artifact_writer, fallback -> the full view (pipeline-child scopes; a
//                 main-session role with them gets everything). The mapping
//                 re-points at pipeline.RegistryScope when the approved
//                 swarm archive lands.
// Whenever role.Roster is non-empty and the view contains agent.run, that
// tool is replaced by delegationTool(...) so the role parameter validates.
// Returns nil when src is nil.
func RoleScopeView(src *registry.Registry, role ResolvedRole) *registry.Registry

// delegationTool returns base (agent.run) carrying role.Roster: the schema
// gains an optional "role" property enumerating the roster, and the
// description names the roster. Every other session keeps NewSubagentTool's
// schema byte-for-byte — a role-less session pays no schema tokens for a
// capability it cannot use. The result must be registered into a fresh
// registry (Register recompiles the schema; a copied Tool would carry the
// old validator and reject "role").
func delegationTool(base registry.Tool, roster []string) registry.Tool

// presetResolver is the optional extension of RouteResolver that can
// resolve a named model preset; routedProviderResolver implements it.
type presetResolver interface {
	ResolvePreset(name, class string) (routing.Route, provider.Provider, error)
}

// internal/agent/subagent.go gains:
//   func agentRunSchema(roles []string) json.RawMessage
//     // NewSubagentTool calls agentRunSchema(nil) — byte-identical to today's
//     // literal at subagent.go:241; delegationTool calls it with the roster.
//   agentRunArgs gains `Role string `json:"role"`` (decoded at subagent.go:577)
//   SubagentRequest gains:
//     ConfigRole string // a [roles.<x>] name; separate from Role routing.AgentRole

// internal/agent/runner.go gains:
//   turnRole     ResolvedRole // pinned at turn start
//   turnRoleOK   bool
//   turnRegistry *registry.Registry // set at turn start when the role changes the tool surface; nil otherwise
//   func (r *Runner) toolRegistry() *registry.Registry // turnRegistry when non-nil, else r.Registry
//   func (r *Runner) pinTurnRole()
//   func (r *Runner) clearTurnRole()
//   func (r *Runner) turnPersona() string // turnRole.PersonaText() when turnRoleOK, else ""

// internal/agent/prompts.go: SystemPromptOptions gains:
//   Persona string // the active role's PersonaText(); "" when no role

// internal/app/session: Event gains
//   Role string // the new role (EventActiveRoleChanged), alongside Held at session.go:94
// plus the event kind EventActiveRoleChanged and
//   func WithActiveRole(name string) Option

// internal/acpwire/turncalls.go gains (next to MethodSetEffort, turncalls.go:15):
//   MethodSetActiveRole = "session/set_active_role"
//   type ActiveRoleParams struct {
//       SessionID string `json:"sessionId"`
//       Role      string `json:"role"`
//   }
//   type ActiveRoleResult struct {
//       Role        string `json:"role"`
//       AppliesFrom string `json:"appliesFrom,omitempty"`
//   }

// internal/acpwire/status.go: SessionStatus gains (additive, omitempty):
//   ActiveRole        string `json:"activeRole,omitempty"`
//   ActiveRoleMissing bool   `json:"activeRoleMissing,omitempty"`
//     // the active name does not resolve in the agent's config (spec failure
//     // mode 3). Computed agent-side so the TUI renders a verdict, never policy.

// internal/acpclient.Session gains:
//   SetActiveRole(ctx context.Context, name string) (string, error)
//     // validates against resolved roles; "" clears; unknown non-empty name
//     // returns an error and changes nothing; returns the committed name.

// internal/acp/service: Deps gains
//   SetActiveRole func(name string) (applies string, err error)
// and Session gains the setter field + method.

// internal/app: runtime gains
//   func (rt *Runtime) SetSessionActiveRole(name string) (string, error) // next to SetSessionEffort, runtime.go:1076
//   func (rt *Runtime) ActiveRoleMissing() bool

// internal/db:
//   migrationColumns (db.go:45) gains {"agent_sessions", "active_role", "TEXT"}
//   func (db *DB) UpdateSessionActiveRole(sessionID, name string) error

// internal/commands: Result gains
//   Err error // non-nil marks the Result as a failure; Text stays the user-facing string
// and func Error(err error) Result

// internal/llm/routing: StaticRouter gains
//   func (r *StaticRouter) ResolvePreset(name string, asRole AgentRole) (Route, error)

// internal/app: routedProviderResolver gains
//   func (r *routedProviderResolver) ResolvePreset(name, class string) (routing.Route, provider.Provider, error)
```

## Task 1: Config layer — the `[roles]` table

**Goal:** `[roles]` parses from the user and project layers (project
overrides user per role name), legacy preset references are rewritten like
every other preset reference, invalid entries produce diagnostics (including
unknown roster entries), and the built-in coordinator resolves from
`config.Default()`.

**Files:** `internal/app/config/roles.go` (new), `internal/app/config/types.go`,
`internal/app/config/file_types.go`, `internal/app/config/defaults.go`,
`internal/app/config/merge.go`, `internal/app/config/diagnostics.go`,
`internal/app/config/roles_test.go` (new).

**Steps:**

1. Create `internal/app/config/roles.go` with the `Role` struct from
   Contracts (verbatim above).
2. In `internal/app/config/types.go`, add `Roles map[string]Role` to the
   runtime `Config` struct immediately after the `CustomAgents` field
   (`types.go:22`), with tag `toml:"roles"`. Plain map, nil = absent.
3. In `internal/app/config/file_types.go`, add
   `Roles map[string]fileRole \`toml:"roles"\`` to the config-file struct
   next to `CustomAgents` (`file_types.go:387`), and define `fileRole` next
   to `fileCustomAgent` (`file_types.go:81`), mirroring its
   presence-preserving shape: `Persona string`, `Scope string`,
   `Roster []string`, `Preset string`, all `omitempty`.
4. In `internal/app/config/merge.go`, add the role merge immediately after
   the `file.CustomAgents` block (`merge.go:174-180`), following it exactly —
   nil-map init, then a per-key loop into `cfg`:

   ```go
   if file.Roles != nil {
       if cfg.Roles == nil {
           cfg.Roles = map[string]Role{}
       }
       for name, r := range file.Roles {
           cfg.Roles[name] = r.runtime()
       }
   }
   ```

   `merge` runs once per layer — `merge(&cfg, userNorm)` at `layers.go:121`,
   then `merge(&cfg, projectNorm)` at `layers.go:175` — so the per-key loop
   alone yields project-over-user override for a same-named role, with no
   extra logic. State that explicitly in the code comment: **a project role
   replaces the user role of the same name wholesale**; a project entry that
   sets only `scope` drops the user entry's persona. This matches
   `custom_agents` and is the documented contract, not an accident. Unknown
   keys inside a role are ignored because `fileRole` has no field to decode
   them into. `fileRole.runtime()` is the analogue of `fileCustomAgent`'s
   (`merge.go:179` calls it): define `func (r fileRole) runtime() Role` next
   to `fileRole` in `file_types.go`, returning `Role{Persona: r.Persona,
   Scope: r.Scope, Roster: r.Roster, Preset: r.Preset}`.
5. In `internal/app/config/merge.go`, extend `rewritePresetReferences`
   (`merge.go:495-519`) with a roles loop, mirroring the `cfg.CustomAgents`
   loop at `merge.go:511-518`:

   ```go
   for name, role := range cfg.Roles {
       if role.Preset != "" {
           if canon, ok := renames[role.Preset]; ok {
               role.Preset = canon
               cfg.Roles[name] = role
           }
       }
   }
   ```

   This is required, not optional: the roles merge at step 4 runs *before*
   the `rewritePresetReferences` call (`merge.go:183-187`), and that
   function's comment scopes it to "agent profiles and custom agents". Without
   this loop, a role whose `preset` names a legacy-renamed preset resolves to
   a stale key and step 7's unknown-preset diagnostic fires on config that
   otherwise loads fine.
6. In `internal/app/config/defaults.go`, add to `Default()` next to
   `CustomAgents: map[string]routing.CustomAgent{}` (`defaults.go:59`):

   ```go
   Roles: map[string]Role{
       "coordinator": {
           Persona: coordinatorPersona,
           Scope:   "read_only",
           Roster:  []string{"repo_scout", "implementer", "tester", "reviewer"},
       },
   },
   ```

   and define `const coordinatorPersona` in the same file as:

   ```go
   const coordinatorPersona = `You are the coordinator. You direct work; you do not perform it yourself.

   Hard rule: never write files and never run commands yourself. Delegate every substantive task to a subagent with agent.run.

   Delegation protocol: call agent.run once per task, naming a role from your roster with its "role" parameter (repo_scout for finding code, implementer for changes, tester for running tests, reviewer for review) or omitting it for an ad-hoc subtask. Each call takes a prompt and a short description; the result is a summary you synthesize. Use agent.await, agent.kill and agent.steer to manage work you dispatched in the background.

   Answer directly when the question is answerable from context you already hold — questions about this conversation, or summaries of what your subagents reported. Do not delegate a question you can answer without new information.

   Output contract: finish with a synthesis that names what was delegated, what each subagent reported back, and what (if anything) remains.`
   ```

7. In `internal/app/config/diagnostics.go`, add role validation immediately
   after the profile-binding block (`diagnostics.go:91-120`), reusing its
   shape (`SeverityError`, `layers.ProvenanceOf(path).SetBy.String()`). Three
   checks per role:
   - `roles.<name>.scope` — scope is set and not one of `full`, `read_only`,
     `tester`, `artifact_writer`, `fallback`.
   - `roles.<name>.preset` — preset is set but absent from
     `cfg.Models.Presets`.
   - `roles.<name>.roster` — a roster entry that resolves to nothing: not a
     key of `cfg.Roles`, not a key of `cfg.CustomAgents`, and not a member of
     `routing.AllRoles`. This is C1's requirement and it is cheap: the
     roster is rendered into the persona as free text, so a typo would
     otherwise be silent until the model called a name that does not resolve.
8. Write `internal/app/config/roles_test.go` under `testenv.Run` covering:
   parse of a `[roles.x]` table from both layers; project-over-user override
   for a same-named role (asserting the whole-struct replacement from step
   4); the legacy-preset rewrite from step 5; all three diagnostics; and
   `Default().Roles["coordinator"]` resolving with scope `read_only`, a
   four-entry roster, and a non-empty persona.

**Verify:** `go test ./internal/app/config/...` — all pass.

## Task 2: Role resolution, the scope view, and the delegation variant

**Goal:** `agent.ResolveRole`, `agent.RoleScopeView` and `agent.delegationTool`
exist and are unit-tested, including the property that a role-less session's
`agent.run` schema is unchanged.

**Files:** `internal/agent/roles.go` (new), `internal/agent/subagent.go`,
`internal/agent/roles_test.go` (new), `internal/agent/subagent_test.go`
(existing — add cases).

**Steps:**

1. Create `internal/agent/roles.go` with the Contracts declarations verbatim:
   `ResolvedRole`, `PersonaText`, `ResolveRole`, `delegationTools`,
   `RoleScopeView`, `delegationTool`, `presetResolver`.
   - `ResolveRole(cfg, name)`: empty name → `ok=false`. Look up
     `cfg.Roles[name]`; missing → `ok=false`. Otherwise return
     `ResolvedRole{Name: name, Persona: r.Persona, Scope: r.Scope,
     Roster: append([]string(nil), r.Roster...), Preset: r.Preset}`.
   - `PersonaText()`: return `Persona` when `len(Roster) == 0`; otherwise
     `Persona + "\n\nDelegation roster: " + strings.Join(Roster, ", ") + "."`
     (with the leading separator omitted when `Persona` is empty). No clock,
     no counts, no per-turn data — this lands in the cached prefix.
   - `RoleScopeView(src, role)`: return nil when `src == nil` (see Task 4
     step 1 for why this guard is load-bearing).
     Build the view by iterating `src.List()` exactly as the existing views do
     (`view.go:19`, `:54`), skipping `agent.run` whenever a variant will
     replace it, then registering the variant. Concretely:
     - base := `registry.ReadOnlyView(src)` for `read_only`, `registry.TesterView(src)`
       for `tester`, else a fresh `registry.New()` copy of `src.List()`
       (all of it, including `Deferred`/`Internal` entries, matching
       `ReadOnlyView`'s treatment).
     - for each name in `delegationTools`: if the scope is `read_only` or
       `tester` (where the base deliberately lacks them), `if t, ok :=
       src.Lookup(name); ok`, register `t` — except `agent.run`, which is
       registered below.
     - if `len(role.Roster) > 0` and `src.Lookup("agent.run")` succeeds,
       register `delegationTool(baseTool, role.Roster)` in place of the plain
       tool. For `full`/`artifact_writer`/`fallback` the copy step skips
       `agent.run` so this is a first registration, never a duplicate.
     - return the view.
   - Import direction is verified: `internal/agent` already imports
     `internal/app/config` in production code (`prompts.go:9`, `route.go:7`,
     `snapshot_warning.go:12`).
2. In `internal/agent/subagent.go`, extract the schema literal at
   `subagent.go:241` into `agentRunSchema(roles []string) json.RawMessage`:
   `roles == nil` produces the current bytes exactly; otherwise the properties
   map gains
   `"role":{"type":"string","description":"delegation role name","enum":[...]}`
   built from `roles`. Do not keep two literals — one function with two call
   sites is what guarantees the byte-identical claim. `NewSubagentTool` passes
   `nil`.
3. Same file: add `Role string \`json:"role"\`` to `agentRunArgs` (decoded at
   `subagent.go:577`), add `ConfigRole string` to `SubagentRequest`
   (`subagent.go:57`, beside `Role routing.AgentRole`), and thread it: the
   handler builds `SubagentRequest{..., ConfigRole: args.Role}`, and the
   model-consent preview call at `subagent.go:286` becomes
   `cfg.resolver(SubagentRequest{Role: <existing>, ConfigRole: args.Role,
   Agent: args.Agent, Model: args.Model})`. The resolver must see the
   config role or a role-bound child is previewed against the wrong model.
4. `delegationTool(base, roster)`: copy `base`, replace `Schema` with
   `agentRunSchema(roster)`, and extend `Description` with one sentence naming
   the roster (`"Delegation roles: a, b, c."`) and the `role` parameter. Keep
   the rest of the description — concurrency count, async contract — but
   **rewrite** the trailing "not a role binding from the agent profiles table"
   clause to "role names a configured agent role, not an agent-profiles
   binding". The old wording stays technically true once `role` names a
   `[roles.x]` entry, but sitting immediately next to a parameter literally
   called `role` it reads as a contradiction; the replacement disambiguates
   the two namespaces (`routing.AgentRole` vs `[roles.x]`) rather than denying
   that the one the model can now see exists.
   **Do not** copy `validator` (`registry/types.go:51`): it is unexported and
   compiled from the *old* schema, so a copied Tool would reject `role`.
   `Register` recompiles it (`registry.go:28`).
5. Write `internal/agent/roles_test.go` and extend `subagent_test.go`
   covering:
   - unknown name → `ok=false`; a config role resolves field-for-field;
     `PersonaText()` includes the roster line and is byte-stable across two
     calls;
   - `RoleScopeView(src, coordinator)` excludes a registered `file.write`-class
     tool (register a fake tool with `RiskWorkspaceWrite`) and includes
     `agent.run`, `agent.await`, `agent.kill`, `agent.steer`;
   - `RoleScopeView(src, {Scope: "full"})` contains every tool `src` does;
   - `"tester"` includes `test.run` and excludes `file.write`;
   - `RoleScopeView(nil, role)` returns nil;
   - a role with a roster produces an `agent.run` schema that **accepts**
     `{"prompt":"x","description":"y","role":"tester"}` and **rejects** an
     unlisted role (assert through `ValidateArgs`, which uses the compiled
     validator — this is the test that catches a copied-struct bug);
   - `NewSubagentTool(...).Schema` is byte-identical to `agentRunSchema(nil)`
     and does **not** accept a `role` property (the no-extra-tokens claim).
   - `NewSubagentTool(...).Description` still carries the old clause verbatim
     (`not a role binding from the agent profiles table`), while
     `delegationTool(base, []string{"tester"}).Description` contains
     `not an agent-profiles binding` and **not** the old clause — the rewrite
     is variant-only.

**Verify:** `go test ./internal/agent/...` — all pass. This is deliberately
the whole package, not `-run 'Role'`: the risks in this task are a name
collision, a changed `agent.run` schema, and a registry-view regression, and
none of those are in functions with "Role" in the name. The cache-prefix
guards (`cacheprefix_test.go:71`, `:152`) run here too; they must be green
before Task 4 touches the prompt.

## Task 3: Session state and persistence

**Goal:** `ActiveRole` is set/get on session state with a typed event payload,
persisted through a column, and restored on resume; a failed persistence write
never blocks the live switch.

**Files:** `internal/app/session/active_role.go` (new),
`internal/app/session/session.go`, `internal/db/db.go`,
`internal/db/sessions.go`, `internal/app/runtime.go`,
`internal/app/session/active_role_test.go` (new),
`internal/db/sessions_active_role_test.go` (new).

**Steps:**

1. In `internal/app/session/session.go`, add `Role string` to `Event`
   (`session.go:77`) with a comment naming `EventActiveRoleChanged`, beside
   `Held` (`session.go:94`), and add the `EventActiveRoleChanged` kind next to
   the other event constants. Without the field the event carries no payload
   and "the event fires with the new role" is unassertable.
2. Create `internal/app/session/active_role.go` following the `hold.go`
   pattern exactly: `ActiveRole()` reads under `s.mu`; `SetActiveRole(name)`
   writes under `s.mu`, is a no-op when the value is unchanged, and on change
   publishes `EventActiveRoleChanged` with `Event{Role: name}` *after*
   releasing the lock (as `SetHold` does). Add `func WithActiveRole(name
   string) Option` next to `WithSubagentMaxConcurrency` (`session.go:905`).
3. In `internal/db/db.go`, add `{"agent_sessions", "active_role", "TEXT"}` to
   `migrationColumns` (`db.go:45`), beside the existing `agent_sessions`
   entries (`db.go:69-74`). The applying loop at `db.go:114-131`
   introspects each table once, skips present columns, and issues
   `ALTER TABLE agent_sessions ADD COLUMN active_role TEXT`, so it is
   idempotent and needs no bespoke migration file. **Nullable, no DEFAULT** —
   this matches the spec ("nullable `active_role TEXT`") and the
   `active_root` precedent (`db.go:70`); the earlier draft's
   `NOT NULL DEFAULT ''` would have contradicted the spec for no gain.
4. In `internal/db/sessions.go`: add `ActiveRole string` to `Session`
   (`sessions.go:27`, next to `ActiveRoot` at `:37`); add `active_role` to
   `GetSession`'s SELECT list (`sessions.go:66`) and scan it into a
   `sql.NullString`, assigning `s.ActiveRole = r.String` exactly as
   `ActiveRoot` is assigned at `sessions.go:99`. Do **not** add it to
   `CreateSession`'s INSERT (`sessions.go:53`): the column is nullable and a
   new session has no role, so NULL is the correct initial value and
   `CreateSession`'s signature stays untouched.
5. Add `func (db *DB) UpdateSessionActiveRole(sessionID, name string) error`
   to `internal/db/sessions.go`, mirroring `UpdateSessionWorkspace`
   (`sessions.go:195`). Passing `""` stores NULL-or-empty consistently; state
   the choice in the comment (write `""`; `GetSession` reads both as unset).
6. In `internal/app/runtime.go`: on the resume path, pass the stored value
   into state construction — at `runtime.go:1721` the `storedSession` is
   already in scope; at the `session.New` call (`runtime.go:1778`) add
   `session.WithActiveRole(storedSession.ActiveRole)`. The new-session branch
   leaves it unset. Also add `func (rt *Runtime) SetSessionActiveRole(name
   string) (string, error)` beside `SetSessionEffort` (`runtime.go:1076`):
   validate with `agent.ResolveRole(rt.config, name)` (empty clears; unknown
   non-empty name returns an error and changes nothing), call
   `state.SetActiveRole`, persist via `db.UpdateSessionActiveRole`
   fail-open (log and continue, spec failure mode 5), and return the committed
   name. And `func (rt *Runtime) ActiveRoleMissing() bool`: true when the
   state's name is non-empty and does not resolve.
7. Write tests. `internal/app/session/active_role_test.go`: set/get, no-op
   re-set publishes nothing, and a change publishes `EventActiveRoleChanged`
   **carrying the new name** (subscribe and assert `Event.Role`).
   `internal/db/sessions_active_role_test.go`: the column exists after
   `Open`, re-running the column pass is a no-op, `UpdateSessionActiveRole` +
   `GetSession` round-trips a value **and** round-trips the unset state as
   empty.
8. Add the resume test the spec's §Testing.4 requires, in
   `internal/app/runtime_test.go` (existing suite): open a DB, write a
   session row with `active_role = "coordinator"`, build the runtime through
   the production resume path, and assert `state.ActiveRole() ==
   "coordinator"`. Task 3's DB round-trip does not exercise
   `runtime.go:1778`, which is where a dropped `WithActiveRole` option would
   actually hide.

**Verify:** `go test ./internal/app/session/... ./internal/db/...
./internal/app/ -run 'ActiveRole|Role|Resume'` — all pass.

## Task 4: Runner turn-build seam

**Goal:** with an active role, each turn's system prompt carries the persona
(and its roster line), the registry view is structurally narrowed, a bound
role's preset switches the turn's model, and the resolved role is pinned for
the turn so a mid-turn `/role` cannot rewrite `messages[0]`.

**Ordering constraint (read before editing):** `RunTask` resolves the route at
`runner.go:1229` — `turnProvider, turnModel, route := r.resolveRoute(ctx,
task)` — and only pins prompt access at `runner.go:1297`. The role pin must
therefore happen at the **top of `RunTask`** (`runner.go:1049`), before
`resolveRoute`. Pinning it where `pinPromptAccess` sits, as the previous draft
did, leaves `turnRoleOK` false at binding time for every turn (it is cleared
at turn end, so it is never carried in), and the binding step below could
never fire.

**Files:** `internal/agent/runner.go`, `internal/agent/route.go`,
`internal/agent/prompts.go`, `internal/agent/execute.go`,
`internal/agent/chat.go`, `internal/agent/calibration.go`,
`internal/agent/protocol.go`, `internal/agent/rollover.go`,
`internal/agent/roles.go` (from Task 2),
`internal/agent/runner_roles_test.go` (new).

**Steps:**

1. `internal/agent/runner.go`:
   - Add the fields from Contracts near the other per-turn state (the comment
     at `runner.go:221` lists the per-turn fields).
   - `toolRegistry()`: return `r.turnRegistry` when non-nil, else `r.Registry`.
   - `pinTurnRole()`: read `r.State.ActiveRole()`; on empty or unknown name
     (`ResolveRole` → `ok=false`, spec failure mode 3) call `clearTurnRole()`
     and return; else set `turnRole`/`turnRoleOK` and set `turnRegistry =
     RoleScopeView(r.Registry, role)` **only when** `r.Registry != nil` and
     the role changes the tool surface — scope `read_only`/`tester`, or a
     non-empty `Roster` (which rewrites `agent.run`'s schema even at full
     scope). Otherwise leave `turnRegistry` nil so the turn pays for no copy.
     The `r.Registry != nil` guard is load-bearing: `RoleScopeView(nil, ...)`
     returns nil by contract, but pinning nil would make `toolRegistry()`
     return nil where today it returns the real (nil) registry and the
     existing nil guards at `calibration.go:29`, `protocol.go:234` and
     `rollover.go:112` still behave.
   - `clearTurnRole()`: zero all three fields.
   - `turnPersona()`: `turnRole.PersonaText()` when `turnRoleOK`, else `""`.
   - In `RunTask` (`runner.go:1049`), call
     `r.pinTurnRole()` as the first statement, immediately followed by
     `defer r.clearTurnRole()`. The defer must be adjacent to the pin, not
     folded into the existing per-turn defer at `runner.go:1262-1266`: that
     defer is registered *after* `resolveRoute` (line 1229) and after the
     route-limits block, so a return or error between the pin and line 1262
     would leak a role into the next turn.
   - Copy the new fields in the clone at `runner.go:677` next to
     `r.Registry = other.Registry` (`turnRegistry` copies; `turnRole`/
     `turnRoleOK` copy by value).
2. `internal/agent/prompts.go`: add `Persona string` to `SystemPromptOptions`
   (`prompts.go:627`); in `buildSystemPrompt` (`prompts.go:716`) write the
   persona as its own section **between the `systemAccessDirective` block
   (`prompts.go:751`) and the `if !nativeTools { b.WriteString("\n\nAvailable
   tools:\n") }` block (`prompts.go:754`)**, only when non-empty. That
   position is deliberate and must not be improvised: everything after it
   (tool list at 754, narration at 824, output format and
   `renderRoleAddendum` at 842) either varies per turn or precedes the
   output-format tail, whereas the persona is static per role and belongs in
   the cacheable head next to the other mode directives. Keep it byte-stable:
   no clock, no counts, no roster lookup at prompt-build time — the roster
   line already arrives inside `Persona` via `PersonaText()`. Do not touch
   `agentRoster()` (`prompts.go:294`, `:302`), which renders custom agents,
   presets and discovered models — an unrelated concept that the spec's C5
   line used to conflate.
3. `internal/agent/route.go`: in `systemPromptMessage`
   (`route.go:604-616`), pass `Persona: r.turnPersona()` and change
   `Tools: r.Registry.List()` / `Deferred: r.Registry.ListDeferred()` to
   `r.toolRegistry()`.
4. Change the remaining `r.Registry` dispatch sites to `r.toolRegistry()`:
   `execute.go:627`, `:725`, `:1328`, `:1455`, `:1465`, `:1704`;
   `chat.go:1139`, `:1147`; `calibration.go:29`; `protocol.go:234-237`;
   `rollover.go:112-115`. At the three nil *guards* (`calibration.go:29`,
   `protocol.go:234`, `rollover.go:112`) keep the comparison to a nil registry
   — i.e. `if reg := r.toolRegistry(); reg != nil` — so the guard keeps its
   original meaning rather than becoming dead code. `runner.go:677` is the
   clone: leave `r.Registry` there; the pin is per-turn state, not
   construction state.
   Note the accepted limitation in a comment at `toolRegistry()`:
   `turnRegistry` is a snapshot taken at pin time, so a tool registered or
   `Replace`d mid-turn is invisible to a scoped role session for that turn.
   That is the same trade-off as the persona pin, and it resolves at the next
   turn boundary.
5. Model binding: inside `resolveRoute` (`route.go:117`), immediately after
   `route, resolvedProvider, err := r.RouteResolver.Resolve(string(task.Class))`
   succeeds (`route.go:124`) and **before** the `SetActiveRoute`/window/limits
   block (`route.go:152`), substitute the bound preset for this turn only:

   ```go
   if r.turnRoleOK && r.turnRole.Preset != "" {
       if pr, ok := r.RouteResolver.(presetResolver); ok {
           if br, bp, berr := pr.ResolvePreset(r.turnRole.Preset, string(task.Class)); berr == nil && bp != nil {
               route, resolvedProvider = br, bp
           }
       }
   }
   ```

   This is now valid because step 1 pins the role at the top of `RunTask`,
   which runs before this call. Hooking inside `resolveRoute` rather than
   after its return is deliberate: `SetActiveRoute` (`route.go:152`), the
   context window, max output, thinking and pricing are all derived from
   `route` further down the same function, and they must describe the model
   the turn actually runs. An unbound role changes nothing. When resolution
   fails, the session route stands and no notice is set — fail-open, because
   the binding is advisory configuration, not a gate.

   The two supporting pieces, both verified:
   - `internal/llm/routing`: add `func (r *StaticRouter) ResolvePreset(name
     string, asRole AgentRole) (Route, error)` next to `ResolveExplicitModel`
     (`router.go:215`), delegating to the existing `resolvePresetBinding`
     (`router.go:183`) as
     `r.resolvePresetBinding(name, asRole, r.config.DefaultProfile)`. No new
     routing logic — this keeps the spec's "no new routing machinery"
     constraint honest.
   - `internal/app/app.go`: add `ResolvePreset(name, class string)
     (routing.Route, provider.Provider, error)` to `routedProviderResolver`
     (`app.go:312`), next to its `ResolveRole` method (`app.go:477`):
     `r.router.ResolvePreset(name, classRole)` mapping the class the way
     `Resolve` does (`app.go:464`) and falling back to `routing.RoleImplementer`
     when it does not map, then `r.providerFor(route)` (`app.go:489`).
     `*routedProviderResolver` and the test fake `staticResolver`
     (`runner_testhelpers_test.go:129`) are the only two types assigned to
     `Runner.RouteResolver`, so `staticResolver` gains the method too (return
     the route/provider it already holds).
6. Delegation targets: the roster names a `[roles.x]` entry, and decision 6
   makes those real delegation targets. Thread it end to end:
   - the runtime resolver built at `app.go:2163` (`modelResolver :=
     agent.SubagentModelResolver(func(req agent.SubagentRequest) ...)`) reads
     `req.ConfigRole`: when it is non-empty, `agent.ResolveRole(cfg, name)`,
     and when the resolved role has a `Preset`, resolve that preset through
     the same `routedProviderResolver` used for the preview. That is what
     makes a role-bound child's model preview *and* run correct.
   - the child runner construction (`internal/app/app.go` factory around
     `app.go:2149`) applies the resolved role: persona as the
     construction-time `Addendum` — legitimate here because a child's role is
     fixed for its single task, whereas C4 forbids `Addendum` for the parent
     because the parent resolves per turn — and scope via
     `RoleScopeView(parentReg, role)`. An unresolvable `ConfigRole` is a tool
     error (`agent.run: unknown role %q`), not a silent downgrade, because the
     schema's `enum` already narrowed the legal values.
   - A child never receives the delegation family: `SubtaskScopeView`
     (`subagent.go:132`) still strips all four, so depth and concurrency
     counters are untouched.
7. Write `internal/agent/runner_roles_test.go` covering, in order:
   - with `ActiveRole` = coordinator, one `RunTask` turn yields
     `turnPersona()` equal to the coordinator persona **plus** its roster line,
     and `turnRegistry()` excludes a registered write tool while including
     `agent.run`;
   - the pin happens before the route resolution: drive a turn with a bound
     role and assert the turn's route is the bound preset's (this is the
     regression test for the ordering bug — it fails if the pin moves back
     below `resolveRoute`);
   - a mid-turn `SetActiveRole` after the pin leaves the turn's `messages[0]`
     unchanged (assert `systemPromptMessage()` bytes are stable across the
     mid-turn write) and the next turn picks up the new role;
   - an unbound role leaves the route model untouched;
   - an unknown `ActiveRole` degrades to no persona and the full registry;
   - `clearTurnRole()` has run when the turn returns (assert on the next
     entry that `turnRoleOK` is false before the pin).
8. Add the cache-invalidation test the spec's §Testing.3 requires: in this
   same file (it may use `cacheprefix_test.go`'s harness), run a turn, switch
   role, run a second turn, and assert the prefix cache is invalidated
   **exactly once** — i.e. `messages[0]` differs between turn 1 and turn 2 and
   is stable across the steps *within* turn 2.

**Verify:** `go test ./internal/agent/...` — all pass, and
`TestPromptPrefixStableAcrossSteps` +
`TestPromptPrefixStableAcrossStepsWithReasoning`
(`cacheprefix_test.go:71`, `:152`) pass **unchanged**.

## Task 5: The `/role` command

**Goal:** `/role <name>` validates against resolved roles, sets the session
role, and reports honestly when a turn is in flight; `/role` with no arg lists
available roles; an unknown name is a distinguishable failure.

**Files:** `internal/commands/types.go`, `internal/commands/commands.go`,
`internal/commands/commands_test.go` (existing — add cases).

**Steps:**

1. In `internal/commands/types.go`, add `Err error` to `Result`
   (`types.go:20-29`) and `func Error(err error) Result`, which returns
   `Result{Text: err.Error(), Err: err}`. Without it an unknown role is
   textually indistinguishable from success, which is what spec failure mode
   2 ("returns an error Result") actually asks for. `Text` keeps `Err` nil;
   `Panel` keeps `Err` nil. Nothing constructs `Result` positionally, so this
   is additive; the TUI's existing `Result.Text` rendering is unchanged.
2. In `internal/commands/commands.go`, inside `RegisterAll`'s command slice
   (`commands.go:51`), add next to the other Chat commands:

   ```go
   {
       Name:        "role",
       Description: "Set this session's agent role (applies from the next turn)",
       Args:        "[role-name]",
       Group:       groupChat,
       Handler:     roleCommandHandler,
   },
   ```

3. Add `roleCommandHandler` in the same file:

   ```go
   func roleCommandHandler(state *session.State, args []string) Result {
       if len(args) == 0 {
           names := make([]string, 0, len(state.Config.Roles))
           for name := range state.Config.Roles {
               names = append(names, name)
           }
           sort.Strings(names)
           if len(names) == 0 {
               return Text("No roles are configured.")
           }
           return Text("Roles: " + strings.Join(names, ", ") + "\nUsage: /role <name>")
       }
       name := args[0]
       if _, ok := agent.ResolveRole(state.Config, name); !ok {
           return Error(fmt.Errorf("unknown role: %s", name))
       }
       state.SetActiveRole(name)
       if db := state.DB(); db != nil && state.SessionID() != "" {
           if err := db.UpdateSessionActiveRole(state.SessionID(), name); err != nil {
               state.Logger().Warn("persist active role failed", "error", err)
           }
       }
       if state.Activity().Kind != session.ActivityIdle {
           return Text(fmt.Sprintf("Role set to %s; applies from the next turn.", name))
       }
       return Text(fmt.Sprintf("Role set to %s.", name))
   }
   ```

   The persistence write is fail-open per spec failure mode 5 (live switch
   applies; failure logged). Import direction is verified: `internal/commands`
   does not import `internal/agent` today and `internal/agent` does not
   import `internal/commands`, so the new import is acyclic.
4. Add tests: valid set writes `ActiveRole`; unknown name returns a `Result`
   with `Err != nil` **and** leaves `ActiveRole` empty (assert the field, not
   the string); no-arg lists the configured roles (register a role via
   `state.Config.Roles` in the test); the busy-turn wording differs from the
   idle wording.

**Verify:** `go test ./internal/commands/...` — all pass.

## Task 6: ACP seam — the `activeRole` fields and set method

**Goal:** the active role rides the client seam — an additive
`SessionStatus.activeRole` (+ `activeRoleMissing`) field and a
`Session.SetActiveRole` method — with every implementer of the interface
updated in the same task.

**Files:** `internal/acpwire/turncalls.go`, `internal/acpwire/status.go`,
`internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`,
`internal/acpclient/rpc/sessions.go`, `internal/acp/acptest/acptest.go`,
`internal/acp/service/controls.go`, `internal/acp/service/service.go`,
`internal/acp/controls.go`, `internal/acp/host.go`,
`internal/app/servicedeps.go`, plus tests.

**Why this list is longer than the interface change:** three types assert
`acpclient.Session` (`rpc/sessions.go:84`, `acptest/acptest.go:403`,
`service/service.go:295`), so adding a method to the interface without all
three breaks compilation of the TUI test tree. And a new control needs the
full `SetEffort` surface, which is the precedent to copy end to end:
constant (`turncalls.go:15`), params/result types (`turncalls.go:102`, `:109`),
client method (`rpc/turns.go:134`), method-table row (`methods.go:68`),
service delegate (`controls.go:69`), `Deps` field (`service.go:70`) + setter
field (`service.go:205-208`), RPC handler (`acp/controls.go:265`) and its
registration (`acp/controls.go:599`), and the client-visible map
(`acp/host.go:457`).

**Steps:**

1. `internal/acpwire/turncalls.go`: add `MethodSetActiveRole =
   "session/set_active_role"` and the `ActiveRoleParams`/`ActiveRoleResult`
   types from Contracts, beside `MethodSetEffort` (`turncalls.go:15`) and
   `EffortParams` (`:102`) / `EffortResult` (`:109`).
2. `internal/acpwire/status.go`: add to `SessionStatus` (`status.go:10`) the
   two fields from Contracts. The wire-field rules test
   (`internal/acpwire/rules_test.go`) must pass unchanged — optional additive
   fields are exactly its C6 case.
3. `internal/acpclient/acpclient.go`: add to the `Session` interface
   (`acpclient.go:194`), with the doc-comment style its neighbors use
   (`// ACP: session/set_active_role (new, additive)`):

   ```go
   // SetActiveRole sets the session's agent role, or clears it for "".
   // An unknown non-empty name returns an error and changes nothing.
   SetActiveRole(ctx context.Context, name string) (string, error)
   ```

4. `internal/acpclient/methods.go`: add the row
   `{Iface: "Session", Name: "SetActiveRole", ACP: "session/set_active_role", Exists: true}`
   next to `SetEffort` (`methods.go:68`). Both
   `TestMethodTableCoversTheInterfaces` and
   `TestACPClientMethodTableMatchesTheServer` require the matching server
   side, so steps 5 and 8 land in the same commit.
5. `internal/acpclient/rpc/sessions.go`: implement it beside the other
   session calls, exactly like `SetEffort` (`rpc/turns.go:134`):

   ```go
   func (s *Session) SetActiveRole(ctx context.Context, name string) (string, error) {
       params := acpwire.ActiveRoleParams{SessionID: s.id, Role: name}
       var result acpwire.ActiveRoleResult
       if err := s.conn.Call(ctx, acpwire.MethodSetActiveRole, params, &result); err != nil {
           return "", err
       }
       return result.Role, nil
   }
   ```

6. `internal/acp/acptest/acptest.go`: implement it in the test agent
   (`acptest.go:403` holds the assertion) against its own state, so the TUI
   suite compiles and can drive a role switch.
7. `internal/acp/service/controls.go`: add, following `SetEffort`
   (`controls.go:69`) exactly — closed-session check, nil-callback check,
   delegate:

   ```go
   func (s *Session) SetActiveRole(ctx context.Context, name string) (string, error) {
       if s.closed.Load() {
           return "", errSessionClosed
       }
       if s.setActiveRole == nil {
           return "", errNoActiveRole
       }
       return s.setActiveRole(name)
   }
   ```

   Add `errNoActiveRole` beside `errNoEffort` (`controls.go:15`).
8. `internal/acp/service/service.go`: add the
   `SetActiveRole func(name string) (applies string, err error)` field to
   `Deps` (`service.go:66-70`) and the `setActiveRole func(string) (string,
   error)` field beside `setEffort` (`service.go:205-208`); wire it through.
   In `Status()` (`service.go:360`), add
   `ActiveRole: st.ActiveRole(),` and
   `ActiveRoleMissing: st.ActiveRoleMissing(),` beside `ActiveRoute:
   routeOf(st.ActiveRoute())` (`service.go:397`).
9. `internal/acp/controls.go` + `internal/acp/host.go`: add the
   `set_active_role` handler next to `set_effort` (`acp/controls.go:265`
   decodes with `decodeParams`), register it with `srv.Handle`
   (`acp/controls.go:599`), and add it to the client-visible method map
   (`acp/host.go:457`).
10. `internal/app/servicedeps.go`: wire `SetActiveRole: rt.SetSessionActiveRole`
    beside `SetEffort: rt.SetSessionEffort` (`servicedeps.go:66`). This is the
    concrete setter Task 3 step 6 defines; `rt.ActiveRoleMissing` is what
    `ActiveRoleMissing()` above reads, so the TUI never decides resolvability
    (that would be policy on the wrong side of the seam).
11. Tests: `SetActiveRole` over the in-process adapter sets and clears;
    unknown name returns an error with `ActiveRole` unchanged; `Status()`
    carries `activeRole` with `omitempty` (absent when unset) and flips
    `activeRoleMissing` when the name does not resolve; the RPC-level
    `set_active_role` round-trips (`internal/acp/controls_test.go`, following
    the `MethodSetEffort` cases at `:1262`); the method-table tests pass.

**Verify:** `go test ./internal/acpwire/... ./internal/acpclient/...
./internal/acp/... ./internal/app/ -run 'Role|MethodTable|Status'` — all pass.

## Task 7: TUI surfaces and the scripted journey

**Goal:** the active role is visible and selectable in the TUI — a status-line
indicator, a no-arg picker, and an agents-panel roles section for authoring —
all through `acpclient.Session`, plus the scripted `/role` journey the spec
requires.

**Files:** `internal/app/tui/status.go`,
`internal/app/tui/commands_dispatch.go`, `internal/app/tui/agents/panel.go`,
`test/usability/role_journey_test.go` (new), plus tests.

**Steps:**

1. `internal/app/tui/status.go`: in `renderStatusLine` (`status.go:186`), next
   to the existing `st.ActiveRoute`/`st.Effort` segments (`status.go:302`,
   `:314`), render the active role when `st.ActiveRole != ""` (e.g.
   `role:coordinator`, in the same style), and the `role:<name>?` form when
   `st.ActiveRoleMissing` is true. Read the verdict from the status field —
   **never** re-resolve the name against the TUI's config: that would put
   policy on the rendering side of `TestTUIStaysOnTheClientSeam`.
2. `internal/app/tui/commands_dispatch.go`: add a `tuiCommandEffects` entry
   for `"role"` in the map literal at `commands_dispatch.go:251`. No-arg opens
   a picker listing the roles from `m.sess.Config()` — the same call the file
   already makes at `commands_dispatch.go:320` and the same source
   `NewRosterPanel` receives (`model.go:977`); name the call site in the code
   comment rather than "the config the session exposes". With an arg, call
   `m.sess.SetActiveRole(ctx, args[0])` and render the returned error or the
   committed name as a system note. The registry `Handler` from Task 5 stays
   the headless path; this entry is the interactive path, per the file's own
   split comment (`commands_dispatch.go:20-24`).
3. `internal/app/tui/agents/panel.go`: add a "Roles" section to the roster
   panel mirroring the custom-agents section (`panel.go:200-340`): list
   `cfg.Roles` with persona/scope/roster/preset, and allow editing through the
   panel's existing save path (`sess.SaveProjectConfig`, `panel.go:694`).
4. Add `test/usability/role_journey_test.go`, modelled on
   `test/usability/skills_journey_test.go`: build a fixture home with a
   `[roles.<name>]` entry, `/role <name>` in a live session, assert the status
   line shows the role, `agent`-side status agrees, `/role nosuchrole` reports
   an error and leaves the indicator unchanged, and `/role` with no argument
   opens the picker. This is spec §Testing.7; Task 8's re-run of the existing
   `-run TestScripted` set does not author it.
5. Tests: the status line renders the role segment when set, omits it when
   unset, and shows the `?` form when `ActiveRoleMissing`; the picker lists
   roles; the panel section renders; `TestTUIStaysOnTheClientSeam`
   (`boundary_test.go:183-186`) still passes (no new direct agent-side
   imports).

**Verify:** `go test ./internal/app/tui/...` — all pass, including the
boundary test; then `go test ./test/usability/... -run 'TestScriptedRole' -v
-timeout 5m` against a freshly built binary.

## Task 8: Final verification

**Goal:** the whole feature is proven green across the repository.

**Steps:**

1. `CGO_ENABLED=1 go build ./...`.
2. `go test ./...` — full suite.
3. `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`, then
   `go test ./test/usability/... -run TestScripted -v -timeout 5m` (the
   harness execs the freshly built binary; AGENTS.md).
4. `go vet ./...` and `gofmt -l .` (no output expected).
5. Daemon non-interference spot check: `go test ./internal/acp/...
   ./internal/app/... ./internal/daemon/...` — the P4-affected packages pass
   unchanged.

**Verify:** every command above exits 0.

## Integration notes

- Task 1 ∥ Task 3 are parallel-safe (disjoint packages, no consumed
  output). Task 5 ∥ Task 6 are parallel-safe (commands vs
  acpwire/acpclient/acp/service). All other tasks run sequentially:
  2 needs 1; 4 needs 1+2+3; 5 needs 2+3; 6 needs 3; 7 needs 1+3+5+6.
- Commit per task as `<plan-slug>: task N — <task title>`.
- When the approved swarm archive lands, re-point `RoleScopeView`'s
  `artifact_writer`/`fallback` arms and the `scope` enum's meaning at
  `pipeline.RegistryScope`; nothing else in this plan changes.
- The spec is uncommitted per repo convention (specs commit with the work
  they describe); this plan follows the same rule and commits with task 1.
  The spec is amended alongside this plan (C5's delegation description, the
  roster rendering, and decision 6's mechanism) so the two do not disagree at
  review time.

## Plan revisions (2026-11-10, after adversarial review)

Recorded so a reviewer can see what moved and why. Every anchor below was
re-verified against the tree, not carried over.

1. **Delegation targets are now implemented, not deferred** (spec decision 6).
   `SubagentRequest` gains `ConfigRole`, `agent.run`'s schema gains an optional
   `role` enum **only for roles that declare a roster**, and the child applies
   that role's persona and scope. Nothing is added to a role-less session's
   schema, so ordinary sessions pay no context for a capability they lack. The
   literal "parse an undeclared parameter" variant was rejected:
   `additionalProperties:false` (`subagent.go:241`) plus `ValidateArgs` means
   an undeclared `role` is rejected, not ignored.
2. **The roster is rendered, and validated.** `ResolvedRole.PersonaText()`
   appends a deterministic roster line; the C1 diagnostic for unknown roster
   entries is restored. Dropping the field would have cost six spec
   amendments and the only cheap typo check.
3. **The role pin moved to the top of `RunTask`** (before `resolveRoute`), with
   its own defer. The previous placement could never bind a preset.
4. **The persona section has a fixed anchor** between the system-access block
   and the tool list. The previous text described a position that does not
   exist (`renderRoleAddendum` runs at the end of the function).
5. **Persistence uses `migrationColumns`**, nullable, not a bespoke migration
   file with `NOT NULL DEFAULT ''`.
6. **The ACP task names every implementer** of `acpclient.Session` and the full
   `SetEffort`-shaped control surface.
7. **`Result.Err` exists**, so "unknown role" is a failure and not just a
   string.
8. **The runtime setter is named** (`rt.SetSessionActiveRole`, wired at
   `servicedeps.go:66`), with `ActiveRoleMissing` computed agent-side.
9. **`rewritePresetReferences` covers roles**, and whole-struct per-layer
   replacement is documented.
10. **Five missing spec tests are now task steps**: boundary cache
    invalidation (4.8), resume restores the role (3.8), the scripted journey
    (7.4), roster diagnostics (1.7), and delegation targets (4.6).
11. **`agent.run`'s trailing disclaimer is rewritten, not kept.** The old
    clause — "not a role binding from the agent profiles table" — stayed
    technically true once `role` named a `[roles.x]` entry, but placed beside
    a parameter literally called `role` it reads as a contradiction. Task 2
    step 4 now rewrites it to "role names a configured agent role, not an
    agent-profiles binding", which disambiguates `routing.AgentRole` from
    `[roles.x]` instead of denying one exists. The description is a `Tool`
    field, not part of the schema, so `NewSubagentTool` keeps today's text
    byte-for-byte; only `delegationTool` rewrites that clause.
