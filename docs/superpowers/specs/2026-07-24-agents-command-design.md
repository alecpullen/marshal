# `/agents` Command, Custom Agents & Roster Menu Design

> **Status:** Design — approved through brainstorming, pending implementation plan.
>
> **Scope:** Adds a `/agents` slash command and a purpose-built docked
> roster panel that unifies everything relevant to running multiple
> agents — role-to-model routing, **user-defined custom agents** that
> carry full per-agent config (prompt, tool scope, approval mode,
> context budget, max iterations) and can fill role slots, plus swarm/SDD
> run budgets — in one keyboard-first surface.

## Problem

Marshal's multi-agent routing is real but **configuration is scattered**
across five generic settings sections and there is no command that
orientates you around the agent roster:

- **Profiles** (`/settings` › Profiles) maps each role to a preset, but is
  a generic key/value collection browser — it shows "X (n roles)" and
  drills into one role at a time, with no view of the *resolved* provider
  or model, no indication of which roles are in swarm vs SDD, and no
  awareness of the fallback chain.
- **Model Presets** and **Agent** (default profile) live in two other
  sections; you bounce between three sections to wire "planner → reasoning".
- **Swarm** and **SDD** budgets are separate sections with no link to the
  roles they actually consume.
- The **SDD mode picker** (`/mode → sdd`) opens a *plan* picker, not an
  agents config — the early "SDD mode" surface never grew into a routing
  UI, so configuring the three SDD roles requires knowing they exist and
  hunting for them inside the Profiles drill.
- There is **no concept of a user-defined agent**. Today's `agent.run`
  subagents are all routed through one `RoleSubtask` with no per-agent
  prompt, tool scope, approval mode, or budget. You cannot define an
  "orchestrator that cannot edit, only dispatches subagents" or a
  "my-react-reviewer" with its own prompt and tool set.

The result: a user who wants to "give the reviewer a stronger model", run
the swarm on cheap local models, or define a custom agent that an
orchestrator dispatches by name has no single place to do it. `/agents`
is that place.

## Goal

`/agents` opens a docked **Roster Panel** that presents the agent cast as
one scannable table, lets you re-bind any role to a preset *or* a custom
agent inline, switch the active profile, **define and edit custom agents**
with full per-agent config, and tune the swarm/SDD run budgets — all from
one keyboard-first surface, persisted the same way `/settings` persists.
It is the multi-agent equivalent of `/models`: a focused, opinionated
view of one concern, rather than a generic config browser.

## Design Principles (tui-design skill)

1. **Keyboard-first, no new global bindings** — reuses the dock's existing
   keys (↑/↓ move, Enter act, Esc close). No new global keybinds.
2. **Spatial consistency** — the panel docks in the *same* slot as
   `/settings`, `/memory`, and the cast list (above input, below
   transcript). Fixed position, no rearrangement.
3. **Progressive disclosure** — the roster is the floor; per-role detail
   (context budget, local-only, fallback source) is one Enter away.
4. **Semantic color, never color-alone** — status glyphs (`●`/`◆`/`↩`/
   `legacy`/`⚠`) carry meaning in monochrome; color enhances.
5. **Drill-Down Stack** layout paradigm: the root is the cast table;
   Enter descends into a role, a custom agent, or a budget editor; Esc
   ascends. A breadcrumb title mirrors the settings browser.
6. **Single source of truth** — the panel reads and writes the *same*
   `config.Config` fields (and the new `CustomAgents` map) via the
   settings `Registry`/`state` machinery, so a change made here is
   immediately visible in `/settings` and vice-versa.

---

## Section 1 — Data Model

A `CustomAgent` is a superset of a `ModelPreset` binding: it points at a
preset for provider/model and layers prompt + tool scope + approval +
budget + iterations on top. Lives in `internal/llm/routing/types.go`
next to `ModelPreset`:

```go
type CustomAgent struct {
    Name          string         `toml:"name"`
    Preset        string         `toml:"preset"`           // required: which ModelPreset
    SystemPrompt  string         `toml:"system_prompt"`     // addendum appended after role base prompt
    ToolDenylist  []string       `toml:"tool_denylist"`    // tool names removed from role default; empty = inherit
    ApprovalMode  string         `toml:"approval_mode"`    // plan/default/edit/copilot/auto; "" = inherit session
    MaxIterations int            `toml:"max_iterations"`    // 0 = routing default for the role
    Context       ContextBudget  `toml:"context"`           // max_repo_context_tokens etc.
}
```

**Preset is required, not inline.** A custom agent points at a named
`ModelPreset` for provider/model — it does not duplicate those fields.
One source of truth for "ollama/qwen3-coder"; `/models` keeps working.
If the named preset is deleted, the custom agent resolves to an error row
(like a dangling role→preset today).

**ToolDenylist is a denylist, not an allowlist.** It names tools removed
from the agent's role-default registry view; empty = inherit the role
default (all tools the role would have). This is deliberately permissive
and fits the motivating case — an "orchestrator" custom agent that
cannot edit (deny `file.write_patch`, `file.write`) but keeps everything
else and can still dispatch subagents via `agent.run`. A strict allowlist
is a possible future toggle; denylist is the v1 semantics. (See Open
Question 1 — resolved: denylist.)

**Role binding becomes a oneOf.** `AgentProfile.Roles` changes from
`map[AgentRole]string` (preset name) to hold *either* a preset name or a
custom-agent name:

```go
type RoleBinding struct {
    Preset      string `toml:"preset"`        // mutually exclusive
    CustomAgent string `toml:"custom_agent"`  // with Preset
}
type AgentProfile struct {
    Name  string
    Roles map[AgentRole]RoleBinding
}
```

**Config wiring** (`internal/app/config/types.go`): one new top-level map
and the `Roles` shape change:

```go
CustomAgents map[string]routing.CustomAgent `toml:"custom_agents"`
```

Defaults: empty map, empty `RoleBindings`. Today's preset-only configs
keep working because a `RoleBinding` with only `Preset` set is identical
to today's string.

**Migration**: old TOML (`planner = "reasoning"`) deserializes into
`RoleBinding{Preset:"reasoning"}` via a custom `RoleBinding.UnmarshalTOML`
that accepts a bare string as the preset. New TOML uses the table form:

```toml
[agent_profiles.local_balanced.roles.planner]
preset = "reasoning"
# or
custom_agent = "my-reviewer"
```

### Resolution

`StaticRouter` (`internal/llm/routing/router.go`) gains
`ResolveCustomAgent` and a `ResolveRole` oneOf branch:

```go
// ResolveCustomAgent resolves a named custom agent's preset + context,
// with the same fallback philosophy as ResolveRole: if the agent's own
// Preset is empty, fall back through the role it was invoked as.
func (r *StaticRouter) ResolveCustomAgent(name string, asRole AgentRole) (Route, error)
```

`Route` gains a `CustomAgent *CustomAgent` field (alongside the existing
`Preset`/`ContextBudget`/`Profile`/`Legacy`) so runner construction can
read the agent's overrides. `ResolveRole` change: when a role binding's
`CustomAgent` is set, it calls `ResolveCustomAgent(name, role)` so the
agent's `SystemPrompt`/`ToolDenylist`/`ApprovalMode`/`MaxIterations` flow
through. When `Preset` is set, today's path runs unchanged. When neither
is set, the existing implementer→legacy fallback runs. Custom agents
inherit the role fallback too: binding `my-reviewer` (prompt + tool
denylist only, no preset) to `reviewer` still falls back to the
implementer preset if `my-reviewer.Preset` is empty.

---

## Section 2 — Dispatch & Resolution

Two dispatch paths feed one construction seam.

### Construction seam: one factory *implementation* change

Today swarm/SDD use `RunnerFactory func(role agent.AgentRole, scope RegistryScope) (*Runner, error)`.
The factory lives in `internal/app` and resolves the role→route→provider
itself. The signature does **not** change. The implementation learns
about custom agents: it reads the resolved `Route.CustomAgent` (if any)
and applies the overrides after building the base Runner:

```go
// pseudo, in app's runner factory
runner := baseRunnerFromRoute(route, role, scope)
if agent := route.CustomAgent; agent != nil {
    if agent.SystemPrompt != ""  { runner.SystemPromptAddendum = agent.SystemPrompt }
    if len(agent.ToolDenylist) > 0 { runner.Registry = denylistView(runner.Registry, agent.ToolDenylist) }
    if agent.ApprovalMode != ""   { runner.SetApprovalMode(parseMode(agent.ApprovalMode)) }
    if agent.MaxIterations > 0     { runner.MaxToolIterations = agent.MaxIterations }
}
return runner, nil
```

`SystemPromptAddendum` is a small new `Runner` field (the prompt layer
appends it after the role base prompt). `denylistView` is a new helper
that filters the registry to remove the named tools — the inverse of
`SubtaskScopeView`. No other `Runner` changes.

### Path 1 — Model dispatch via `agent.run`

`agent.run`'s schema gains an optional `agent` field:

```json
{"agent": {"type":"string", "description":"Name of a configured custom agent to run as. Omit for an ad-hoc subtask (today's behavior)."}}
```

The `SubagentRunnerFactory` today returns a plain `RoleSubtask` runner.
It becomes agent-aware: when `agent` is set, the factory resolves the
custom agent (via the static router), builds the runner with that
agent's overrides and role (defaulting to `RoleSubtask` if the agent
isn't role-bound, so a free-floating custom agent still works for ad-hoc
dispatch). The `agent.run` handler passes `agent` to the factory.

Depth/concurrency guards (`EnterSubagent`) are unchanged — a custom
agent is still one child context, max depth 1, max concurrency 2.

### Path 2 — User dispatch via `/agents`

`/agents` panel renders each custom agent as a row in the "Custom
Agents" group. Enter on one opens a small action menu: **Run now** (ad-hoc,
prompts for a goal inline like `/swarm`) and **Edit** (drill into its
config). "Run now" dispatches through the same `AgentRunner` plumbing as
`/swarm` — `openRunPreflight`-style cast list then `startAgentRun` — but
the runner is built by the factory with the custom agent bound to
`RoleSubtask`. No new runtime.

### What stays unchanged

- `swarm.RunnerFactory` and `sdd.RunnerFactory` signatures — they already
  take `role`+`scope`; the factory *implementation* in `internal/app` is
  the only thing that learns about custom agents.
- `agent.run`'s depth/concurrency guards.
- The pre-flight cast list (`openRunPreflight`) — it already calls
  `routing.Cast`, which resolves custom-agent-bound roles transparently.
- `/models`, `/route`, `/settings`.

### Error cases (surfaced as row state in `/agents`, not crashes)

- Custom agent's `Preset` missing → row shows `⚠ preset gone` (like a
  dangling role→preset today).
- Custom agent bound to a role but `RemoteAllowed=false` and preset is
  remote → `⚠ remote blocked`.
- `agent.run` called with an unknown agent name → tool returns an error
  to the model (`unknown custom agent "x"`); model recovers.
- `agent.run` called with a custom agent whose preset is gone → tool
  returns the resolution error; model recovers.

---

## Section 3 — The `/agents` TUI Panel

A docked `dock.Panel` in the same slot as `/settings` and `/memory`,
reusing the settings `field`/`fieldList`/`paneStack`/`picker` machinery
for persistence. **Config-first** (run status stays in the transcript as
swarm/SDD already do).

### Root layout — three groups

```
┌─ Agents ─────────────────────────────────────────────────┐
│ Profile: local_balanced          ↵ to switch              │
│                                                            │
│ ── Roles ──                                                 │
│  Role            Bound to       Provider/Model    Source  │
│  ▸ planner       reasoning      ollama/qwen3-coder ●      │
│    repo scout    my-scout       ollama/qwen2.5     ◆ impl  │
│    implementer   coder          ollama/qwen3-coder ●      │
│    sdd implementer  (unset)     ollama/qwen3-coder ↩ impl │
│    ...                                                      │
│                                                            │
│ ── Custom Agents ──                                         │
│  ▸ my-scout       fast · −2 tools · copilot       ↵ run    │
│    my-reviewer    reasoning · default            ↵ run    │
│                                                            │
│ ── Run budgets ──                                           │
│  Swarm: 3 fix rounds · 120k tokens            ↵ edit      │
│  SDD:   3 fix rounds · worktree on            ↵ edit      │
│                                                            │
│ 14 roles · 2 agents · /filter · ↵ act · Esc close          │
└────────────────────────────────────────────────────────────┘
```

**Groups** (with section headers, the settings-browser pattern):
1. **Roles** — one row per `routing.AllRoles`, grouped *within* by
   workflow (Swarm roles, SDD roles, Housekeeping:
   router/knowledge/summarizer/title/subtask), each header a
   `kindHeader` row.
2. **Custom Agents** — one row per `cfg.CustomAgents` key. Free-floating
   agents (not bound to any role) appear here too. The summary shows
   `preset · −N tools · mode` where `−N` is the denylist size (tools
   removed from the role default); `−0`/omitted means no tools denied.
3. **Run budgets** — Swarm + SDD, re-hosting the existing
   `swarmFrame`/`sddFrame` builders.

**Source glyphs** (monochrome-safe, glyph before color), read from
`StaticRouter.Cast(AllRoles)` + `ResolveCustomAgent`:
- `●` bound directly to a preset (accent)
- `◆` bound to a custom agent (accent secondary — distinct from preset
  so the binding type is visible)
- `↩ impl` unset, fell back to implementer (muted)
- `legacy` fell back to `agent.provider/model` (warning)
- `⚠` unresolved/error (error); the error string goes in the row `desc`

### Row actions (Enter, context-sensitive)

| Row kind          | Enter action                                                         |
|-------------------|----------------------------------------------------------------------|
| Role              | **Bind** picker: list presets + custom agents + "(unset — fallback)". Picking writes the oneOf `RoleBinding`. Presets show `●`, agents show `◆`. |
| Role → drill (→)  | Per-role context budget frame (`Agents[role].Context`). |
| Custom Agent      | Action menu: **Run now** / **Edit**. Edit drills into the agent's config frame. |
| Custom Agent (Run)| Dispatches via `openRunPreflight` + `startAgentRun` (same plumbing as `/swarm`), bound to `RoleSubtask`. |
| Profile header    | **Switch** picker of `AgentProfiles` keys + "New profile…" (`SetAllowCustom`). |
| Swarm/SDD         | Drills into the existing budget frames. |

### Custom agent config frame (drill from a custom-agent row)

Re-hosts fields the way `presetsFrame` does, against a `settings.state`:

```
my-scout
  Preset          fast              ↵ pick
  System prompt   "Focus on..."     ↵ edit
  Tool denylist    file.write, file.write_patch  ↵ edit (comma list, validated against registry)
  Approval mode   copilot           ←→ cycle
  Max iterations  0 (inherit)       ↵ edit
  Context         8000 tokens        ↵ edit
```

`ToolDenylist` editing is a new string-list field kind that validates
names against the live `registry.Registry.List()` (red row on invalid
name). Empty list shows "(inherit role default — no tools denied)".

### Filter & nav

- `/` fuzzy-matches role names, agent names, presets, providers (reuses
  `fuzzy.Rank` + `textinput`).
- `↑/↓` move, `→`/`Tab` drill, `←`/`Esc` ascend, `Esc` at root closes.
- Footer is context-sensitive (per-row-kind): role row → `↵ bind · →
  detail · Esc back`; custom-agent row → `↵ run/edit · Esc back`; budget
  row → `↵ edit · Esc back`.
- `?` overlays a 6-line legend for the source glyphs (the one novel
  vocabulary; progressive disclosure tier 2).

### Responsive

- 80×24 floor. Under ~60 cols, Provider/Model collapses into the cursor
  row's `desc` (the settings browser's pattern), keeping Role + Bound-to
  visible.
- No new color slots — reuses `AccentPrimary` (●), `AccentSecondary` (◆),
  `FGMuted` (↩/legacy/desc), `StatusWarning`/`StatusError` (⚠). Monochrome
  → glyphs + bold.

### Persistence

Same path as the settings browser: `flushChanges` →
`config.SaveProjectConfig` → emits `settings.ChangedMsg` → `Model`'s
handler calls `persistAndReload` → router rebuilt, swarm/SDD runners pick
up new bindings on next dispatch. **Durable** (not session-only like
`/model`); receipts land in the transcript (e.g.
`profiles.local_balanced.planner → my-reviewer`).

---

## Section 4 — Testing

Following existing patterns (`castlist_test.go`, `settings/browser_test.go`,
`swarm/orchestrator_test.go`, `subagent` tests):

**Routing**
- `ResolveCustomAgent`: agent with preset → resolves; agent with empty
  preset + role → role fallback; agent preset missing → error; remote
  preset blocked → error.
- `ResolveRole` oneOf: binding `CustomAgent` → resolves through agent;
  binding `Preset` → today's path; both empty → fallback chain unchanged.
- `RoleBinding` TOML round-trip: bare string `"reasoning"` →
  `RoleBinding{Preset:"reasoning"}`; table `{custom_agent="my-reviewer"}`
  → that; back to TOML.
- `Route.CustomAgent` populated only when binding is a custom agent.

**Config migration**
- Old config TOML (preset-only) loads into new shape with no changes;
  `RoutingConfig()` still yields a working `routing.Config`.

**Runner construction (factory)**
- Custom agent bound to role → runner gets `SystemPromptAddendum`,
  denylisted registry, approval mode, max iterations. Each override
  applied iff non-empty/non-zero.
- `ToolDenylist` with an invalid tool name → row red, save blocked
  (validation in the panel, not at runtime).
- `ToolDenylist` with a valid tool → that tool removed from the
  runner's registry; the agent cannot call it.
- No custom agent → runner identical to today (regression guard).

**agent.run dispatch**
- `agent` arg absent → today's `RoleSubtask` behavior (existing tests
  pass unchanged).
- `agent` arg names a custom agent → factory builds with that agent's
  overrides, role `RoleSubtask` (or the agent's bound role if it has
  one).
- Unknown agent name → tool returns error to model.
- Depth/concurrency guards unchanged when `agent` set.

**User dispatch via `/agents`**
- Enter on custom-agent row → action menu → Run now → `openRunPreflight`
  cast list → `startAgentRun`. Cast list shows the custom agent's
  resolved preset.

**Panel render** (`panel_test.go`)
- Resolution glyphs: `●`/`◆`/`↩ impl`/`legacy`/`⚠` for the right configs.
- Bind picker offers presets + custom agents + unset; picking writes
  `RoleBinding` and emits a receipt.
- Profile switch; budget drill re-hosts `swarmFrame`/`sddFrame`.
- Narrow width (60 cols): Provider/Model collapses to `desc`.
- `/agents planner` and `/agents my-reviewer` pre-filter + cursor to the
  row.

---

## New + Modified Files

### New

```
internal/app/tui/agents/
  panel.go          — RosterPanel: dock.Panel, root table + drills
  panel_test.go     — render resolution, fallback glyphs, drill, persist
```

### Modified

```
internal/llm/routing/types.go       — CustomAgent, RoleBinding, Route.CustomAgent
internal/llm/routing/router.go      — ResolveCustomAgent, ResolveRole oneOf branch
internal/app/config/types.go        — CustomAgents map, Roles shape,
                                       RoleBinding bare-string unmarshaler
internal/app/config/defaults.go     — empty CustomAgents map default
internal/app/config/routing.go      — pass CustomAgents into routing.Config
internal/app/app.go (or runtime)    — factory impl applies agent overrides after
                                       base runner build; denylistView helper
internal/agent/runner.go            — SystemPromptAddendum field; prompt layer appends
internal/agent/subagent.go          — agent.run optional "agent" arg; factory resolves
                                       custom agent; agentRunArgs gains Agent field
internal/commands/commands.go       — register /agents (Workflows, TUIOnly)
internal/app/tui/commands_dispatch.go — "agents" effect → openAgentsRoster
internal/app/tui/model.go           — openAgentsRoster(args), roster field
internal/app/tui/settings/sections.go — add "Custom Agents" section so the generic
                                       browser reaches them (parity with /agents)
```

`internal/agent/swarm/orchestrator.go` and
`internal/agent/sdd/orchestrator.go` are **not** modified (the factory
implementation handles custom agents); they gain test coverage for
custom-agent-bound roles.

---

## Open Questions (resolved during brainstorming)

1. **`ToolDenylist` semantics** — denylist (remove named tools from role
   default) vs. strict allowlist. **Resolved: denylist.** More permissive,
   fits the "orchestrator that cannot edit, only dispatches subagents"
   case. Strict allowlist is a possible future toggle.

2. **Custom agent name collision with preset names** — a custom agent and
   a preset could share a name. The bind picker lists them disambiguated
   by glyph (`●` preset / `◆` agent), and `RoleBinding` stores *which
   kind*, so no ambiguity at resolution. **Resolved: allow collisions,
   disambiguate by glyph + stored kind.**

3. **Free-floating custom agents** — a custom agent not bound to any role.
   Usable via `agent.run` by name and `/agents` Run now, but swarm/SDD
   never dispatch it (they use role bindings). **Resolved: allowed.**

4. **`/agents <name>` argument** — `/agents my-reviewer` filters + cursors
   to that custom agent's row (analogous to `/agents planner` → planner
   row). **Resolved: filter + cursor**, consistent with the role case.

5. **Inline preset creation from a bind picker** — when no presets exist,
   the picker offers "Add a preset first" → opens `/connect` with
   return-to-roster. Reuses the existing `connectReturnFilter` mechanism.
   **Resolved: /connect return**, not an inline new-preset row.

## Future (out of scope)

- Strict `ToolAllowlist` toggle on `CustomAgent` (v1 ships denylist only).
- Per-custom-agent token budget caps (today's swarm/SDD budgets remain
  the only budget control).
- Custom agents as a first-class `/mode` entry (today `/mode` is
  per-turn interaction modes; custom agents are a dispatch concept).