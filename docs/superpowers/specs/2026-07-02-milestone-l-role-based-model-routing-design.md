# Milestone L: Role-Based Model Routing v1 Design

## Goal

Milestone L adds static role-based model routing for Marshal's current single-agent runtime. Users can define model presets, map agent roles to presets through a selected profile, apply local-only policy, allocate role-specific context budgets, and see the active route in the TUI.

## Scope

In scope:

- Define `AgentRole`, `ModelPreset`, `AgentProfile`, `TaskProfile`, `ContextBudget`, and static route result types.
- Parse model presets, agent profiles, and role-specific context budgets from config.
- Keep legacy `[agent] provider/model` config as a compatibility fallback.
- Implement a deterministic static router.
- Select a role for each current single-agent turn.
- Wire the resolved route into provider creation and `agent.Runner`.
- Store active route metadata in session state.
- Show active role, provider, model, preset, and local-only status in the TUI.
- Apply role-specific repo context token budgets to context packs when available.

Out of scope:

- Dynamic escalation rules.
- Multiple concurrent agent roles.
- Separate planner, implementer, or reviewer model calls in one user turn.
- Swarm runtime behavior.
- Provider capability probing beyond static preset metadata.
- New provider implementations.

## Architecture

Add `internal/llm/routing` as the owner of role routing types and resolution logic. The package should not import the app, TUI, provider factory, tools, or database packages. It consumes plain config-shaped values and returns a concrete route.

The app layer creates a router from loaded config and privacy policy. For each current single-agent turn, Marshal resolves a role route, builds the configured provider, passes the resolved model to `agent.Runner`, and stores route metadata on `session.State` for display.

Milestone L is intentionally static. It creates the abstractions that future planner/reviewer/swarm work can reuse, but it does not change the runner into a multi-agent orchestrator.

## Roles

Define `AgentRole` as a string type with these v1 constants:

- `router`
- `knowledge`
- `summarizer`
- `repo_scout`
- `tester`
- `planner`
- `implementer`
- `reviewer`
- `security_reviewer`

For the current single-agent runtime, role selection should be conservative:

- `ClassQuestion` resolves as `repo_scout`.
- `ClassEdit` resolves as `implementer`.
- `ClassCommand` resolves as `implementer`.

If a selected profile does not define the chosen role, fall back to `implementer`. If that is also missing, fall back to legacy `[agent]`. Fallback to `implementer` applies only when the role is not mapped in the profile; it does **not** apply when the mapped preset is missing or when the preset is a remote provider blocked by `privacy.remote_providers_allowed=false`. Those cases return explicit errors so misconfiguration and policy violations are visible rather than silently switching roles.

## Config Shape

Add config support for:

```toml
[models.presets.coder]
provider = "ollama"
model = "qwen2.5-coder:14b"
context_window = 32768
max_output_tokens = 4096
temperature = 0.1
top_p = 1.0
tool_calling = "json"
reasoning_effort = "none"
local_only = true

[agent_profiles.local_balanced]
router = "tiny"
knowledge = "tiny"
summarizer = "tiny"
repo_scout = "fast"
tester = "fast"
planner = "coder"
implementer = "coder"
reviewer = "coder"
security_reviewer = "coder"

[agents.implementer.context]
max_repo_context_tokens = 48000
max_conversation_tokens = 8000
include_raw_code = true
include_summaries = true
include_symbols = true
include_diff = false
include_tests = false
```

Existing config remains valid:

```toml
[agent]
provider = "ollama"
model = "qwen2.5-coder:14b"
```

Legacy `[agent]` is used only when no usable selected profile and preset route exists.

## Core Types

`internal/llm/routing` should define:

```go
type AgentRole string

type ModelPreset struct {
	Name            string
	Provider        string
	Model           string
	ContextWindow   int
	MaxOutputTokens int
	Temperature     float64
	TopP            float64
	ToolCalling     string
	ReasoningEffort string
	LocalOnly       bool
}

type AgentProfile struct {
	Name  string
	Roles map[AgentRole]string
}

type ContextBudget struct {
	MaxRepoContextTokens int
	MaxConversationTokens int
	IncludeRawCode       bool
	IncludeSummaries     bool
	IncludeSymbols       bool
	IncludeDiff          bool
	IncludeTests         bool
}

type TaskProfile struct {
	Class string
}

type Route struct {
	Role          AgentRole
	Profile       string
	Preset        ModelPreset
	ContextBudget ContextBudget
	Legacy        bool
}
```

The `TaskProfile.Class` value should use the existing agent task class strings (`question`, `edit`, `command`) without importing `internal/agent`; app or runner code can map agent task classes to routing roles.

## Data Flow

1. `config.Load` parses providers, model presets, agent profiles, and agent context budgets from global and project config files.
2. App startup creates a routing config from loaded config.
3. For each agent turn, role selection maps the classified task to `repo_scout` or `implementer`.
4. The static router resolves the selected profile, role, preset, provider, local-only policy, and role budget.
5. The provider factory builds the resolved provider from `cfg.Providers[route.Preset.Provider]`.
6. `agent.Runner` receives `route.Preset.Model`.
7. `session.State` stores route metadata for TUI rendering.
8. When refreshing or creating context packs, the active route's `MaxRepoContextTokens` overrides the default context-pack budget when positive.

## Error Handling

Config syntax errors remain load errors.

Routing errors should be explicit and testable:

- unknown selected profile
- profile role mapped to missing preset
- preset mapped to missing provider
- preset uses a remote provider while `privacy.remote_providers_allowed=false`
- no routed profile and incomplete legacy `[agent]` fallback

Role fallback to `implementer` is intentionally narrow: it occurs only when the profile does not map the selected role. Missing presets and remote-provider blocks are surfaced as explicit errors rather than silently falling back.

At app startup, routing or provider-construction failure should keep the TUI usable with the runner disabled and a provider/status error recorded, rather than crashing the whole app. This preserves Marshal's local-first behavior when no model has been configured yet.

## TUI Behavior

The status area should show:

- active role
- provider
- model
- preset name
- whether the resolved route is local-only

When no runner route is available, the TUI should show a clear inactive route state instead of blank model fields.

## Testing

Add focused tests for:

- config parsing of `[models.presets]`, `[agent_profiles]`, and `[agents.<role>.context]`
- global/project config merge behavior for presets, profiles, and budgets
- static router happy path
- role fallback to `implementer`
- legacy `[agent]` fallback
- missing profile, preset, and provider errors
- remote provider blocked by privacy policy
- active route storage on `session.State`
- TUI route display
- runner/app wiring with fake provider config
- role budget applied to context-pack max tokens

## Acceptance Criteria

- `go test ./...` passes.
- Milestone L checklist is fully checked.
- `AgentRole`, `ModelPreset`, and `AgentProfile` are defined.
- A static router resolves roles to presets and providers.
- The active route is visible in the TUI.
- Local-only policy blocks remote presets when remote providers are disabled.
- Role-specific context budget can change context-pack max repo tokens.
- Existing legacy `[agent]` configs continue to work.
