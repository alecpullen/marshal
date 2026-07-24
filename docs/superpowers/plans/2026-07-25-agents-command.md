# `/agents` Command, Custom Agents & Roster Menu — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `/agents` slash command and docked roster panel that unifies role→model routing, user-defined custom agents (full per-agent config that can fill role slots), and swarm/SDD run budgets in one keyboard-first surface.

**Architecture:** A `CustomAgent` config type supersedes `ModelPreset` as the bindable unit — it points at a preset and layers prompt + tool denylist + approval mode + budget + iterations. `AgentProfile.Roles` becomes a oneOf (`RoleBinding{Preset|CustomAgent}`). The static router resolves either. One factory implementation change (`roleRunnerSpec.newRunner`) applies agent overrides after building the base runner; `agent.run` gains an optional `agent` arg. The `/agents` panel reuses the settings `field`/`fieldList`/`picker` machinery for persistence.

**Tech Stack:** Go, Bubble Tea v2, lipgloss v2, pelletier/go-toml v2, existing `internal/llm/routing`, `internal/agent`, `internal/app/config`, `internal/app/tui/settings`.

## Global Constraints

- Build with `CGO_ENABLED=1` and a C toolchain (tree-sitter). Verify with `go build ./cmd/marshal`.
- Run all tests with `go test ./...`. Run a single package with `go test ./internal/<pkg>/...`.
- Format with `gofmt -w .`; vet with `go vet ./...`.
- No new global TUI keybindings. Reuse existing dock keys.
- Reuse semantic color slots only (`theme.Current()`); no hardcoded hex. Glyphs must carry meaning in monochrome.
- A `CustomAgent`'s `Preset` is required (by name); provider/model are never inline.
- `ToolDenylist` semantics: denylist (remove named tools from role default); empty = inherit.
- Config persistence is durable (project TOML), not session-only. Receipts land in the transcript.
- Bare-string TOML (`planner = "reasoning"`) must keep deserializing into `RoleBinding{Preset:"reasoning"}`.
- Token-tracking seam (origin/main): `agent.UsageObserver` is `func(schema.TokenUsage)`, `agent.Runner.Pricing` is `pricing.ModelPricing` (set via `pricing.Lookup(route.Preset)`), and `metricsRecorder(db, projectID, sessionID, logger) func(agent.TurnMetrics)` persists turns. `buildSubagentFactory` currently sets none of these — the subagent token gap is closed by Task 6, and the role-runner pricing gap by Task 5. `agent.UsageAggregator` exists but is not yet wired into any session; the plan does not depend on it.

---

## File Structure

**New**
- `internal/app/tui/agents/panel.go` — `RosterPanel` (`dock.Panel`): root cast table + drills + custom-agent config frame.
- `internal/app/tui/agents/panel_test.go` — render resolution, glyphs, drill, persist.

**Modified**
- `internal/llm/routing/types.go` — `CustomAgent`, `RoleBinding`, `AgentProfile.Roles` shape, `Route.CustomAgent`, `RoleBinding.UnmarshalTOML`.
- `internal/llm/routing/router.go` — `ResolveCustomAgent`, `ResolveRole` oneOf branch, `Cast` carries custom-agent binding.
- `internal/llm/routing/router_test.go` — new resolution cases.
- `internal/app/config/types.go` — `Config.CustomAgents`, `RoleBinding` round-trip helper.
- `internal/app/config/file_types.go` — `fileAgentProfiles` shape (`RoleBinding`), `fileCustomAgents`.
- `internal/app/config/save.go` — write `CustomAgents` + `RoleBinding`; `activePresetName` adapts to oneOf.
- `internal/app/config/load.go` / `merge.go` — read `CustomAgents` + `RoleBinding`.
- `internal/app/config/defaults.go` — empty `CustomAgents` default.
- `internal/app/config/routing.go` — pass `CustomAgents` into `routing.Config`.
- `internal/app/config/save_test.go`, `config_test.go` — migrate existing preset-only assertions + new.
- `internal/agent/runner.go` — `SystemPromptAddendum` field; `buildSystemPrompt` appends it.
- `internal/agent/prompts.go` — `BuildSystemPromptWithMode` gains an addendum parameter (threaded through the 4 call sites).
- `internal/agent/subagent.go` — `agent.run` optional `agent` arg; `SubagentRunnerFactory` becomes agent-aware.
- `internal/agent/registry_scope.go` (new small helper file) — `DenylistView`.
- `internal/app/app.go` — `roleRunnerSpec.newRunner` applies agent overrides; `buildSubagentFactory` becomes agent-aware.
- `internal/commands/commands.go` — register `/agents` (Workflows, TUIOnly).
- `internal/app/tui/commands_dispatch.go` — `"agents"` effect → `openAgentsRoster`.
- `internal/app/tui/model.go` — `openAgentsRoster(args)`, roster field wiring.
- `internal/app/tui/settings/sections.go` — add "Custom Agents" section for the generic browser.

---

## Task 1: `CustomAgent` + `RoleBinding` types

**Files:**
- Modify: `internal/llm/routing/types.go`
- Test: `internal/llm/routing/router_test.go` (added)

**Interfaces:**
- Produces: `routing.CustomAgent` struct, `routing.RoleBinding` struct (with `UnmarshalTOML`), `routing.Route.CustomAgent` field, `routing.AgentProfile.Roles` changed to `map[AgentRole]RoleBinding`.

- [ ] **Step 1: Write the failing test** for `RoleBinding` TOML round-trip.

Add to `internal/llm/routing/router_test.go`:

```go
func TestRoleBindingUnmarshalBareString(t *testing.T) {
	var b routing.RoleBinding
	if err := toml.Unmarshal([]byte(`"reasoning"`), &b); err != nil {
		t.Fatalf("bare string unmarshal: %v", err)
	}
	if b.Preset != "reasoning" || b.CustomAgent != "" {
		t.Fatalf("got %+v, want Preset=reasoning", b)
	}
}

func TestRoleBindingUnmarshalTableCustomAgent(t *testing.T) {
	var b routing.RoleBinding
	if err := toml.Unmarshal([]byte(`custom_agent = "my-reviewer"`), &b); err != nil {
		t.Fatalf("table unmarshal: %v", err)
	}
	if b.CustomAgent != "my-reviewer" || b.Preset != "" {
		t.Fatalf("got %+v, want CustomAgent=my-reviewer", b)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/routing/ -run TestRoleBindingUnmarshal -v`
Expected: FAIL — `routing.RoleBinding` not defined.

- [ ] **Step 3: Write minimal implementation**

In `internal/llm/routing/types.go`, replace the `AgentProfile.Roles` field and add the new types. The current `AgentProfile`:

```go
type AgentProfile struct {
	Name  string
	Roles map[AgentRole]string
}
```

becomes:

```go
// RoleBinding is a oneOf: exactly one of Preset or CustomAgent is set.
// A bare TOML string ("reasoning") decodes as Preset (see UnmarshalTOML).
type RoleBinding struct {
	Preset      string `toml:"preset,omitempty"`
	CustomAgent string `toml:"custom_agent,omitempty"`
}

// UnmarshalTOML accepts a bare string as Preset, or a table with
// preset/custom_agent. This preserves the pre-custom-agents TOML shape.
func (b *RoleBinding) UnmarshalTOML(v any) error {
	switch raw := v.(type) {
	case string:
		b.Preset = raw
		return nil
	default:
		// Use the standard struct decoder by re-marshalling/unmarshalling.
		data, err := toml.Marshal(map[string]any{"__rb": v})
		if err != nil {
			return err
		}
		var wrap struct {
			RB RoleBinding `toml:"__rb"`
		}
		if err := toml.Unmarshal(data, &wrap); err != nil {
			return err
		}
		*b = wrap.RB
		return nil
	}
}

type AgentProfile struct {
	Name  string
	Roles map[AgentRole]RoleBinding
}

// CustomAgent is a user-defined, named agent that layers prompt, tool
// denylist, approval mode, context budget, and iteration cap on top of a
// referenced ModelPreset. It can fill a role slot (via RoleBinding) or be
// dispatched ad-hoc by name (agent.run / /agents Run now).
type CustomAgent struct {
	Name          string         `toml:"name"`
	Preset        string         `toml:"preset"`
	SystemPrompt  string         `toml:"system_prompt,omitempty"`
	ToolDenylist  []string       `toml:"tool_denylist,omitempty"`
	ApprovalMode  string         `toml:"approval_mode,omitempty"`
	MaxIterations int            `toml:"max_iterations,omitempty"`
	Context       ContextBudget  `toml:"context,omitempty"`
}
```

Add the `toml` import `"github.com/pelletier/go-toml/v2"`. Add to `Route`:

```go
type Route struct {
	Role          AgentRole
	Profile       string
	Preset        ModelPreset
	ContextBudget ContextBudget
	Legacy        bool
	CustomAgent   *CustomAgent // nil unless resolved from a RoleBinding.CustomAgent
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/routing/ -run TestRoleBindingUnmarshal -v`
Expected: PASS.

- [ ] **Step 5: Run the package to catch the cascade of compile errors** (config package uses `profile.Roles[role]` as a string). Do not fix them yet — Task 2 does. Confirm the *test file* compiles: `go vet ./internal/llm/routing/`.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/routing/types.go internal/llm/routing/router_test.go
git commit -m "feat(routing): add CustomAgent + RoleBinding oneOf types"
```

---

## Task 2: Config layer migration (load/save/merge/defaults)

**Files:**
- Modify: `internal/app/config/types.go`, `file_types.go`, `save.go`, `merge.go`, `defaults.go`, `routing.go`
- Modify: `internal/app/config/save_test.go`, `config_test.go`

**Interfaces:**
- Consumes: `routing.RoleBinding`, `routing.CustomAgent` (Task 1).
- Produces: `config.Config.CustomAgents` field; `file.AgentProfiles` now `map[string]map[routing.AgentRole]routing.RoleBinding`; `routing.Config.CustomAgents` populated.

- [ ] **Step 1: Write failing tests for round-trip + bare-string migration**

In `internal/app/config/save_test.go`, update `TestSaveProjectConfigPreservesAgentProfiles` and `TestSaveProjectConfigWritesAgentProfiles` to assert the new `RoleBinding` shape, and add a bare-string load test. Replace the body of `TestSaveProjectConfigWritesAgentProfiles` (lines ~549-582) with:

```go
func TestSaveProjectConfigWritesAgentProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"fast": {Name: "fast", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer:  {Preset: "small"},
			routing.RoleSDDReviewer:  {CustomAgent: "large"},
		}},
	}
	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	file, err := loadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if file.AgentProfiles == nil {
		t.Fatal("agent_profiles not written")
	}
	if file.AgentProfiles["fast"] == nil {
		t.Fatal("profile fast not written")
	}
	if got := file.AgentProfiles["fast"][routing.RoleImplementer]; got.Preset != "small" {
		t.Fatalf("implementer = %+v, want Preset=small", got)
	}
	if got := file.AgentProfiles["fast"][routing.RoleSDDReviewer]; got.CustomAgent != "large" {
		t.Fatalf("sdd_reviewer = %+v, want CustomAgent=large", got)
	}
}

func TestLoadBareStringRoleBinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
[agent_profiles.mine]
planner = "reasoning"
implementer = "coder"
`), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := loaded.AgentProfiles["mine"]
	if p.Roles[routing.RolePlanner].Preset != "reasoning" {
		t.Fatalf("planner = %+v, want Preset=reasoning", p.Roles[routing.RolePlanner])
	}
	if p.Roles[routing.RoleImplementer].Preset != "coder" {
		t.Fatalf("implementer = %+v, want Preset=coder", p.Roles[routing.RoleImplementer])
	}
}
```

In `internal/app/config/config_test.go` line 700, update the assertion `cfg.AgentProfiles["local_balanced"].Roles[routing.RoleRepoScout] != "fast"` to `...Roles[routing.RoleRepoScout].Preset != "fast"`. Same for line 646 if it indexes by role.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/config/ -run 'TestSaveProjectConfigWritesAgentProfiles|TestLoadBareStringRoleBinding' -v`
Expected: FAIL — compile errors (file.AgentProfiles still `map[string]map[AgentRole]string`).

- [ ] **Step 3: Update `file_types.go`**

In `internal/app/config/file_types.go` line 216, change:

```go
AgentProfiles map[string]map[routing.AgentRole]string `toml:"agent_profiles"`
```

to:

```go
AgentProfiles map[string]map[routing.AgentRole]routing.RoleBinding `toml:"agent_profiles"`
// CustomAgents mirrors config.CustomAgents for the file layer.
CustomAgents  map[string]routing.CustomAgent                       `toml:"custom_agents"`
```

- [ ] **Step 4: Update `types.go`**

In `internal/app/config/types.go`, add to the `Config` struct (after `AgentProfiles`):

```go
CustomAgents map[string]routing.CustomAgent `toml:"custom_agents"`
```

- [ ] **Step 5: Update `defaults.go`**

In `internal/app/config/defaults.go` line 42, after `AgentProfiles: map[string]routing.AgentProfile{},` add:

```go
CustomAgents:   map[string]routing.CustomAgent{},
```

- [ ] **Step 6: Update `save.go`**

In `internal/app/config/save.go`, the block at lines 212-221 becomes:

```go
if file.AgentProfiles != nil || len(cfg.AgentProfiles) > 0 {
	file.AgentProfiles = map[string]map[routing.AgentRole]routing.RoleBinding{}
	for name, profile := range cfg.AgentProfiles {
		roles := profile.Roles
		if roles == nil {
			roles = map[routing.AgentRole]routing.RoleBinding{}
		}
		file.AgentProfiles[name] = roles
	}
}
if file.CustomAgents != nil || len(cfg.CustomAgents) > 0 {
	file.CustomAgents = map[string]routing.CustomAgent{}
	for name, a := range cfg.CustomAgents {
		ca := a
		ca.Name = name
		file.CustomAgents[name] = ca
	}
}
```

And `activePresetName` (lines 237-247) becomes — it must return the preset *or* the custom agent's preset:

```go
func activePresetName(cfg Config) string {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return ""
	}
	binding, ok := profile.Roles[routing.RoleImplementer]
	if !ok {
		return ""
	}
	if binding.CustomAgent != "" {
		if a, ok := cfg.CustomAgents[binding.CustomAgent]; ok {
			return a.Preset
		}
		return ""
	}
	return binding.Preset
}
```

- [ ] **Step 7: Update `merge.go`**

In `internal/app/config/merge.go` lines 80-87, the loop already copies `roles` into `routing.AgentProfile{Name: name, Roles: roles}` — `roles` is now `map[AgentRole]RoleBinding`, which matches the new `AgentProfile.Roles` type. No change needed to the loop body. After it, add:

```go
if file.CustomAgents != nil {
	if cfg.CustomAgents == nil {
		cfg.CustomAgents = map[string]routing.CustomAgent{}
	}
	for name, a := range file.CustomAgents {
		a.Name = name
		cfg.CustomAgents[name] = a
	}
}
```

- [ ] **Step 8: Update `routing.go`**

In `internal/app/config/routing.go`, add `CustomAgents` to the returned `routing.Config`:

```go
return routing.Config{
	DefaultProfile: c.Profile.Default,
	RemoteAllowed:  c.Privacy.RemoteProvidersAllowed,
	Presets:        c.Models.Presets,
	Profiles:       c.AgentProfiles,
	CustomAgents:   c.CustomAgents,
	ContextBudgets: contextBudgets,
	LegacyProvider: c.Agent.Provider,
	LegacyModel:    c.Agent.Model,
}
```

- [ ] **Step 9: Add `CustomAgents` to `routing.Config`**

In `internal/llm/routing/types.go`, add to `Config`:

```go
type Config struct {
	DefaultProfile string
	RemoteAllowed  bool
	Presets        map[string]ModelPreset
	Profiles       map[string]AgentProfile
	CustomAgents   map[string]CustomAgent
	ContextBudgets map[AgentRole]ContextBudget
	LegacyProvider string
	LegacyModel    string
}
```

- [ ] **Step 10: Run the failing tests**

Run: `go test ./internal/app/config/ -run 'TestSaveProjectConfigWritesAgentProfiles|TestLoadBareStringRoleBinding' -v`
Expected: PASS.

- [ ] **Step 11: Run the full config package to catch the remaining assertion updates**

Run: `go test ./internal/app/config/...`
Expected: PASS once the line 646/700 assertion edits from Step 1 are in place. Fix any other `Roles[role] != "string"` comparisons by appending `.Preset`.

- [ ] **Step 12: Commit**

```bash
git add internal/app/config/ internal/llm/routing/types.go
git commit -m "feat(config): CustomAgents map + RoleBinding round-trip + bare-string migration"
```

---

## Task 3: `ResolveCustomAgent` + `ResolveRole` oneOf branch

**Files:**
- Modify: `internal/llm/routing/router.go`
- Test: `internal/llm/routing/router_test.go`

**Interfaces:**
- Consumes: `routing.Config.CustomAgents`, `RoleBinding` (Tasks 1-2).
- Produces: `StaticRouter.ResolveCustomAgent(name, asRole) (Route, error)`; `Route.CustomAgent` populated when binding is a custom agent; `Cast` reflects it.

- [ ] **Step 1: Write failing tests**

Append to `internal/llm/routing/router_test.go`:

```go
func TestResolveCustomAgentWithPreset(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Presets: map[string]ModelPreset{"fast": {Provider: "ollama", Model: "qwen"}},
		Profiles: map[string]AgentProfile{"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
			RoleImplementer: {Preset: "fast"},
		}}},
		CustomAgents: map[string]CustomAgent{
			"my-scout": {Name: "my-scout", Preset: "fast", SystemPrompt: "be fast"},
		},
	})
	route, err := r.ResolveCustomAgent("my-scout", RoleSubtask)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if route.Preset.Model != "qwen" {
		t.Fatalf("model = %s, want qwen", route.Preset.Model)
	}
	if route.CustomAgent == nil || route.CustomAgent.SystemPrompt != "be fast" {
		t.Fatalf("CustomAgent not attached: %+v", route.CustomAgent)
	}
}

func TestResolveCustomAgentFallsBackToRole(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Presets: map[string]ModelPreset{"impl": {Provider: "ollama", Model: "qwen"}},
		Profiles: map[string]AgentProfile{"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
			RoleImplementer: {Preset: "impl"},
		}}},
		CustomAgents: map[string]CustomAgent{
			"my-scout": {Name: "my-scout", Preset: ""}, // no preset, no role bound
		},
	})
	route, err := r.ResolveCustomAgent("my-scout", RoleSubtask)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if route.Preset.Model != "qwen" {
		t.Fatalf("fallback model = %s, want qwen (implementer)", route.Preset.Model)
	}
	if route.CustomAgent == nil || route.CustomAgent.Name != "my-scout" {
		t.Fatalf("CustomAgent not attached")
	}
}

func TestResolveCustomAgentMissing(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Profiles:      map[string]AgentProfile{"p": {Name: "p"}},
		CustomAgents:   map[string]CustomAgent{},
	})
	if _, err := r.ResolveCustomAgent("ghost", RoleSubtask); err == nil {
		t.Fatal("want error for missing agent")
	}
}

func TestResolveRoleCustomAgentBinding(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Presets: map[string]ModelPreset{"reason": {Provider: "ollama", Model: "qwen-reason"}},
		Profiles: map[string]AgentProfile{"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
			RoleReviewer:   {CustomAgent: "my-reviewer"},
			RoleImplementer: {Preset: "reason"},
		}}},
		CustomAgents: map[string]CustomAgent{
			"my-reviewer": {Name: "my-reviewer", Preset: "reason", SystemPrompt: "strict"},
		},
	})
	route, err := r.ResolveRole(RoleReviewer)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if route.CustomAgent == nil || route.CustomAgent.Name != "my-reviewer" {
		t.Fatalf("reviewer not bound to custom agent: %+v", route.CustomAgent)
	}
	if route.Preset.Model != "qwen-reason" {
		t.Fatalf("preset = %s, want qwen-reason", route.Preset.Model)
	}
}

func TestResolveRolePresetBindingUnchanged(t *testing.T) {
	r := NewStaticRouter(Config{
		DefaultProfile: "p",
		Presets: map[string]ModelPreset{"reason": {Provider: "ollama", Model: "qwen-reason"}},
		Profiles: map[string]AgentProfile{"p": {Name: "p", Roles: map[AgentRole]RoleBinding{
			RoleReviewer: {Preset: "reason"},
		}}},
	})
	route, err := r.ResolveRole(RoleReviewer)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if route.CustomAgent != nil {
		t.Fatalf("preset binding should not attach CustomAgent: %+v", route.CustomAgent)
	}
	if route.Preset.Model != "qwen-reason" {
		t.Fatalf("preset = %s, want qwen-reason", route.Preset.Model)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/routing/ -run 'TestResolveCustomAgent|TestResolveRole' -v`
Expected: FAIL — `ResolveCustomAgent` undefined; `resolveProfileRole` reads `profile.Roles[role]` as string.

- [ ] **Step 3: Implement `ResolveCustomAgent` and the oneOf branch**

In `internal/llm/routing/router.go`, replace `resolveProfileRole` (lines 75-100) to read the oneOf binding, and add `ResolveCustomAgent`. The new `resolveProfileRole`:

```go
func (r *StaticRouter) resolveProfileRole(role AgentRole) (Route, error) {
	profile, ok := r.config.Profiles[r.config.DefaultProfile]
	if !ok {
		return Route{}, fmt.Errorf("%w: %s", ErrProfileNotFound, r.config.DefaultProfile)
	}
	binding, ok := profile.Roles[role]
	if !ok || (binding.Preset == "" && binding.CustomAgent == "") {
		return Route{}, fmt.Errorf("%w: %s role %s", errRoleNotConfigured, profile.Name, role)
	}
	if binding.CustomAgent != "" {
		return r.resolveAgentBinding(binding.CustomAgent, role, profile.Name)
	}
	return r.resolvePresetBinding(binding.Preset, role, profile.Name)
}

func (r *StaticRouter) resolvePresetBinding(presetName string, role AgentRole, profileName string) (Route, error) {
	preset, ok := r.config.Presets[presetName]
	if !ok {
		return Route{}, fmt.Errorf("%w: %s", ErrPresetNotFound, presetName)
	}
	if preset.Name == "" {
		preset.Name = presetName
	}
	if !preset.LocalOnly && !r.config.RemoteAllowed {
		return Route{}, fmt.Errorf("%w: preset %s", ErrRemoteProviderBlocked, presetName)
	}
	return Route{
		Role:          role,
		Profile:       profileName,
		Preset:        preset,
		ContextBudget: r.config.ContextBudgets[role],
	}, nil
}

// ResolveCustomAgent resolves a named custom agent. If the agent's own
// Preset is empty, it falls back through the role it was invoked as
// (ResolveRole's implementer→legacy chain). The agent's overrides are
// attached as Route.CustomAgent so runner construction can apply them.
func (r *StaticRouter) ResolveCustomAgent(name string, asRole AgentRole) (Route, error) {
	agent, ok := r.config.CustomAgents[name]
	if !ok {
		return Route{}, fmt.Errorf("%w: custom agent %s", errCustomAgentNotFound, name)
	}
	agent.Name = name
	if agent.Preset != "" {
		profileName := r.config.DefaultProfile
		route, err := r.resolvePresetBinding(agent.Preset, asRole, profileName)
		if err != nil {
			return Route{}, err
		}
		route.CustomAgent = &agent
		if agent.Context.MaxRepoContextTokens > 0 {
			route.ContextBudget = agent.Context
		}
		return route, nil
	}
	// No preset: fall back through the role's resolution, but attach the agent.
	route, err := r.ResolveRole(asRole)
	if err != nil {
		return Route{}, err
	}
	route.CustomAgent = &agent
	return route, nil
}

func (r *StaticRouter) resolveAgentBinding(name string, role AgentRole, profileName string) (Route, error) {
	agent, ok := r.config.CustomAgents[name]
	if !ok {
		return Route{}, fmt.Errorf("%w: custom agent %s", errCustomAgentNotFound, name)
	}
	agent.Name = name
	if agent.Preset == "" {
		// Fall back through ResolveRole (implementer→legacy), attach agent.
		route, err := r.ResolveRole(role)
		if err != nil {
			return Route{}, err
		}
		route.CustomAgent = &agent
		route.Profile = profileName
		if agent.Context.MaxRepoContextTokens > 0 {
			route.ContextBudget = agent.Context
		}
		return route, nil
	}
	preset, ok := r.config.Presets[agent.Preset]
	if !ok {
		return Route{}, fmt.Errorf("%w: custom agent %s preset %s", ErrPresetNotFound, name, agent.Preset)
	}
	if preset.Name == "" {
		preset.Name = agent.Preset
	}
	if !preset.LocalOnly && !r.config.RemoteAllowed {
		return Route{}, fmt.Errorf("%w: custom agent %s", ErrRemoteProviderBlocked, name)
	}
	route := Route{
		Role:          role,
		Profile:       profileName,
		Preset:        preset,
		ContextBudget: r.config.ContextBudgets[role],
		CustomAgent:   &agent,
	}
	if agent.Context.MaxRepoContextTokens > 0 {
		route.ContextBudget = agent.Context
	}
	return route, nil
}
```

Add the new error var near the others:

```go
errCustomAgentNotFound = errors.New("routing: custom agent not found")
```

`ResolveRole` (lines 34-60) is unchanged — it already calls `resolveProfileRole`, which now branches.

`Cast` (lines 168-175) calls `ResolveRole`, which now returns `Route.CustomAgent` for custom-agent bindings — no change needed; the pre-flight display reads `Route.Preset`/`Route.CustomAgent`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/llm/routing/ -v`
Expected: PASS (all routing tests, new + existing).

- [ ] **Step 5: Commit**

```bash
git add internal/llm/routing/router.go internal/llm/routing/router_test.go
git commit -m "feat(routing): ResolveCustomAgent + RoleBinding oneOf resolution"
```

---

## Task 4: `SystemPromptAddendum` on `Runner` + `DenylistView`

**Files:**
- Modify: `internal/agent/runner.go`, `internal/agent/prompts.go`
- Create: `internal/agent/registry_scope.go`
- Test: `internal/agent/prompts_test.go`, `internal/agent/registry_scope_test.go`

**Interfaces:**
- Consumes: none new.
- Produces: `agent.Runner.SystemPromptAddendum` field; `BuildSystemPromptWithMode` and the 3 sibling builders accept an addendum; `agent.DenylistView(reg, names)` helper.

- [ ] **Step 1: Write failing test for the addendum in the prompt**

In `internal/agent/prompts_test.go`, add:

```go
func TestBuildSystemPromptWithAddendum(t *testing.T) {
	msg := BuildSystemPromptWithAddendum(RoleGeneral, dummyTools(), nil, nil, nil, false, policy.ModeEdit, "Be extra careful with diffs.")
	if !strings.Contains(msg.Content, "Be extra careful with diffs.") {
		t.Fatalf("addendum missing from prompt:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, baseRules) {
		t.Fatalf("base rules dropped when addendum present")
	}
}
```

And for the denylist:

```go
// in a new file internal/agent/registry_scope_test.go
package agent

import (
	"testing"

	"marshal/internal/tools/registry"
)

func TestDenylistViewRemovesNamedTools(t *testing.T) {
	src := registry.New()
	_ = src.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	_ = src.Register(registry.Tool{Name: "file.write", Risk: registry.RiskWrite})
	_ = src.Register(registry.Tool{Name: "shell.run", Risk: registry.RiskCommand})
	view := DenylistView(src, []string{"file.write", "shell.run"})
	names := toolNames(view)
	if len(names) != 1 || names[0] != "file.read" {
		t.Fatalf("got %v, want [file.read]", names)
	}
}

func TestDenylistViewEmptyInheritsAll(t *testing.T) {
	src := registry.New()
	_ = src.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	view := DenylistView(src, nil)
	if len(toolNames(view)) != 1 {
		t.Fatalf("empty denylist should inherit all, got %v", toolNames(view))
	}
}

func toolNames(r *registry.Registry) []string {
	var out []string
	for _, t := range r.List() {
		out = append(out, t.Name)
	}
	return out
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run 'TestBuildSystemPromptWithAddendum|TestDenylistView' -v`
Expected: FAIL — `BuildSystemPromptWithAddendum` undefined; `DenylistView` undefined.

- [ ] **Step 3: Implement `DenylistView`**

Create `internal/agent/registry_scope.go`:

```go
package agent

import "marshal/internal/tools/registry"

// DenylistView returns a new registry containing every tool in src
// except those whose Name is in deny. An empty denylist returns a copy
// of src (inherit-all semantics). Mirrors SubtaskScopeView's pattern.
func DenylistView(src *registry.Registry, deny []string) *registry.Registry {
	view := registry.New()
	if len(deny) == 0 {
		for _, tool := range src.List() {
			_ = view.Register(tool)
		}
		return view
	}
	denied := make(map[string]bool, len(deny))
	for _, n := range deny {
		denied[n] = true
	}
	for _, tool := range src.List() {
		if denied[tool.Name] {
			continue
		}
		_ = view.Register(tool)
	}
	return view
}
```

- [ ] **Step 4: Add the addendum to `buildSystemPrompt`**

In `internal/agent/prompts.go`, change `buildSystemPrompt` to accept an `addendum string` and append it after the role addendum. Change the signature and the existing builders to thread `""`:

```go
func buildSystemPrompt(role AgentRole, tools []registry.Tool, deferredTools []registry.Tool, skillIndex *skills.Index, activeSkills []string, nativeTools bool, mode policy.ApprovalMode, addendum string) schema.ChatMessage {
	rp, ok := roleAddenda[role]
	if !ok {
		rp = roleAddenda[RoleGeneral]
	}

	var b strings.Builder
	b.WriteString(baseIdentity)
	b.WriteString("\n\n")
	b.WriteString(baseEnvironment)
	b.WriteString("\n\n")
	b.WriteString(baseRules)
	b.WriteString(rp)
	if d := modeDirective(mode); d != "" {
		b.WriteString("\n\n")
		b.WriteString(d)
	}
	if addendum != "" {
		b.WriteString("\n\n## Agent Instructions\n\n")
		b.WriteString(addendum)
	}
	b.WriteString("\n\nAvailable tools:\n")
	// ... rest unchanged
}
```

Update the three wrappers to pass `""`:
- `BuildSystemPrompt(...)` → `return buildSystemPrompt(role, tools, nil, skillIndex, activeSkills, nativeTools, policy.ModeEdit, "")`
- `BuildSystemPromptWithDeferred(...)` → `... , policy.ModeEdit, "")`
- `BuildSystemPromptWithMode(...)` → `... , mode, "")`

Add the new builder:

```go
// BuildSystemPromptWithAddendum is BuildSystemPromptWithMode plus a
// custom-agent system-prompt addendum appended after the role addendum.
func BuildSystemPromptWithAddendum(role AgentRole, tools []registry.Tool, deferred []registry.Tool, skillIndex *skills.Index, activeSkills []string, nativeTools bool, mode policy.ApprovalMode, addendum string) schema.ChatMessage {
	return buildSystemPrompt(role, tools, deferred, skillIndex, activeSkills, nativeTools, mode, addendum)
}
```

- [ ] **Step 5: Add `SystemPromptAddendum` to `Runner` and thread it through the call sites**

In `internal/agent/runner.go`, add to the `Runner` struct (near `Role` at line 181):

```go
	// SystemPromptAddendum, when non-empty, is appended to the system
	// prompt after the role addendum. Set by custom-agent runner
	// construction (roleRunnerSpec.newRunner) when a custom agent is
	// bound to the role.
	SystemPromptAddendum string
```

Update the four `BuildSystemPromptWithMode(...)` call sites in `runner.go` (lines 413, 453, 530) and `rollover.go` (line 108), `handoff.go` (line 33) to pass `r.SystemPromptAddendum`:

```go
BuildSystemPromptWithAddendum(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools, r.Policy.ApprovalMode(), r.SystemPromptAddendum)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run 'TestBuildSystemPromptWithAddendum|TestDenylistView' -v`
Expected: PASS.

- [ ] **Step 7: Run the full agent package to catch any other prompt-builder call sites**

Run: `go test ./internal/agent/...`
Expected: PASS. If any test calls `BuildSystemPrompt*` with the old arity, fix by appending the addendum arg (`""` unless the test intends an addendum).

- [ ] **Step 8: Commit**

```bash
git add internal/agent/runner.go internal/agent/prompts.go internal/agent/registry_scope.go internal/agent/prompts_test.go internal/agent/registry_scope_test.go internal/agent/rollover.go internal/agent/handoff.go
git commit -m "feat(agent): SystemPromptAddendum + DenylistView for custom agents"
```

---

## Task 5: Runner factory applies custom-agent overrides + role-runner pricing

**Files:**
- Modify: `internal/app/app.go` (`roleRunnerSpec.newRunner`, `buildSubagentFactory`)
- Test: `internal/app/app_test.go` (added)

**Interfaces:**
- Consumes: `Route.CustomAgent`, `Runner.SystemPromptAddendum`, `DenylistView` (Tasks 1, 3, 4), `pricing.Lookup`, `Runner.Pricing` (token-tracking work on origin/main).
- Produces: role runners and subagent runners that honor custom-agent overrides (prompt, tool denylist, approval mode, max iterations) **and** carry `Pricing` so cost is non-zero. Closes the role-runner pricing gap left by the token-tracking work (only `buildAgentRunner` set `Pricing`; swarm/SDD role runners did not).

- [ ] **Step 1: Write failing test**

The agent package's runner tests use injected providers; app wiring is harder to unit-test directly, so test via the swarm orchestrator path with a custom-agent-bound role. In `internal/agent/swarm/orchestrator_test.go` (existing), add — or, simpler, test `roleRunnerSpec.newRunner` via a small app-level test. Add to `internal/app/app_test.go`:

```go
func TestRoleRunnerAppliesCustomAgentOverrides(t *testing.T) {
	cfg := config.Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			agentRoleImpl: {Preset: "fast"},
			routing.RoleReviewer: {CustomAgent: "strict"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "gpt-4o-mini"}, // priced in builtInTable
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"strict": {Name: "strict", Preset: "fast", SystemPrompt: "be strict", ToolDenylist: []string{"file.write_patch"}, ApprovalMode: "plan", MaxIterations: 5},
	}
	resolver := newTestResolver(cfg) // existing helper in app_test.go that builds a routedProviderResolver
	spec := roleRunnerSpec{
		cfg: cfg, resolver: resolver, reg: testRegistry(t), readOnlyReg: testRegistry(t),
		pol: policyEngine(t), state: testState(t),
	}
	runner, err := spec.newRunner(agentRoleReviewer, swarm.ScopeReadOnly)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if runner.SystemPromptAddendum != "be strict" {
		t.Fatalf("addendum = %q, want be strict", runner.SystemPromptAddendum)
	}
	if runner.MaxToolIterations != 5 {
		t.Fatalf("iterations = %d, want 5", runner.MaxToolIterations)
	}
	if _, ok := runner.Registry.Lookup("file.write_patch"); ok {
		t.Fatal("file.write_patch should be denylisted")
	}
	if runner.Pricing.InputPerMTokCents == 0 && runner.Pricing.OutputPerMTokCents == 0 {
		t.Fatal("Pricing should be set from the resolved preset (non-zero for a priced preset)")
	}
	// ApprovalMode is set on the policy engine, asserted indirectly: the
	// policy engine's mode equals policy.ModePlan.
}
```

Note: if `newTestResolver`/`testRegistry`/`testState`/`policyEngine` helpers do not exist in `app_test.go`, use the existing test setup helpers (search for `roleRunnerSpec{` usage in the test file). The exact helper names may differ; adapt to the existing ones. The test asserts the four override surfaces.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestRoleRunnerAppliesCustomAgentOverrides -v`
Expected: FAIL — overrides not applied (`SystemPromptAddendum` empty, `file.write_patch` still present).

- [ ] **Step 3: Implement override application in `newRunner`**

In `internal/app/app.go`, in `roleRunnerSpec.newRunner` (lines 702-743), after the existing `applyAgentLimits` block, **first** add the pricing line (origin/main's `newRunner` does not set `Pricing` — only `buildAgentRunner` does; role runners currently report zero cost), then the custom-agent overrides, before `return r, nil`:

```go
	r.Pricing = pricing.Lookup(route.Preset) // closes role-runner pricing gap
	if route.CustomAgent != nil {
		ca := route.CustomAgent
		if ca.SystemPrompt != "" {
			r.SystemPromptAddendum = ca.SystemPrompt
		}
		if len(ca.ToolDenylist) > 0 {
			r.Registry = agent.DenylistView(r.Registry, ca.ToolDenylist)
		}
		if ca.ApprovalMode != "" {
			r.SetApprovalMode(parseApprovalMode(ca.ApprovalMode))
		}
		if ca.MaxIterations > 0 {
			r.MaxToolIterations = ca.MaxIterations
		}
	}
	return r, nil
}
```

Add the `pricing` import to `internal/app/app.go` if absent (the token-tracking commit added it to `buildAgentRunner`'s scope; confirm it's imported at file level).

Add the helper (near `roleToolIterations`):

```go
func parseApprovalMode(s string) policy.ApprovalMode {
	switch strings.ToLower(s) {
	case "plan":
		return policy.ModePlan
	case "default":
		return policy.ModeDefault
	case "edit":
		return policy.ModeEdit
	case "copilot":
		return policy.ModeCopilot
	case "auto":
		return policy.ModeAuto
	}
	return policy.ModeDefault
}
```

(Verify the exact `policy.Mode*` constant names in `internal/tools/policy`; the approval-modes design doc references `plan/default/edit/copilot/auto`.)

- [ ] **Step 4: Make `buildSubagentFactory` agent-aware**

Change `agent.SubagentRunnerFactory` (Task 6) — for this task, only change `buildSubagentFactory`'s return to a closure that *can* receive an agent name. Since the factory signature changes in Task 6, do the minimal thing now: leave `buildSubagentFactory` returning the no-arg closure, and Task 6 revises both. Skip this step in Task 5; note in the commit.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/app/ -run TestRoleRunnerAppliesCustomAgentOverrides -v`
Expected: PASS (once the helper names in Step 1 are adapted to real ones).

- [ ] **Step 6: Run the full app package**

Run: `go test ./internal/app/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): role runner applies custom-agent overrides"
```

---

## Task 6: `agent.run` optional `agent` arg + agent-aware subagent factory (with token tracking)

**Files:**
- Modify: `internal/agent/subagent.go`, `internal/app/app.go`
- Test: `internal/agent/subagent_test.go` (added or existing)

**Interfaces:**
- Consumes: `routing.Config.CustomAgents`, `StaticRouter.ResolveCustomAgent` (Task 3), `pricing.Lookup`, `metricsRecorder`, `state.SetTurnUsage` (token-tracking work on origin/main).
- Produces: `agent.run` schema gains `agent`; `agentRunArgs.Agent` field; `SubagentRunnerFactory` becomes `func(agentName string) (*Runner, error)`. Closes the subagent token-tracking gap: today `buildSubagentFactory` sets no `Pricing`, `UsageObserver`, or `MetricsObserver`, so every `agent.run` child's tokens/cost are invisible to the parent turn and to `/context`.

- [ ] **Step 1: Write failing test**

In `internal/agent/subagent_test.go` (create if absent), add:

```go
func TestNewSubagentToolAgentArgResolves(t *testing.T) {
	called := ""
	factory := func(agentName string) (*Runner, error) {
		called = agentName
		// minimal runner with RunTaskFunc so no provider is needed
		r := &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}
		return r, nil
	}
	tool := NewSubagentTool(factory, registry.New(), session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	res, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d","agent":"my-scout"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if called != "my-scout" {
		t.Fatalf("factory called with %q, want my-scout", called)
	}
	if !strings.Contains(res.Summary, "subagent completed") {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestNewSubagentToolNoAgentArgStillWorks(t *testing.T) {
	factory := func(agentName string) (*Runner, error) {
		if agentName != "" {
			t.Fatalf("factory called with %q, want empty", agentName)
		}
		return &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}, nil
	}
	tool := NewSubagentTool(factory, registry.New(), session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	if _, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d"}`),
	}); err != nil {
		t.Fatalf("handler: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestNewSubagentToolAgentArg -v`
Expected: FAIL — `SubagentRunnerFactory` takes no arg; `agentRunArgs` has no `Agent` field.

- [ ] **Step 3: Change the factory signature and the tool**

In `internal/agent/subagent.go`:

```go
// SubagentRunnerFactory builds a fresh Runner bound to a fresh child
// session state. agentName is "" for an ad-hoc subtask (today's
// behavior) or the name of a configured custom agent to run as.
type SubagentRunnerFactory func(agentName string) (*Runner, error)
```

Add `Agent` to `agentRunArgs`:

```go
type agentRunArgs struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description"`
	Agent       string `json:"agent,omitempty"`
}
```

In the handler (lines 108-130), pass `args.Agent` to the factory:

```go
	child, err := factory(args.Agent)
```

Update the tool `Description` to mention the optional agent:

```go
Description: "Delegate a scoped subtask to a fresh child agent context and return its summary. Maximum depth: 1. Maximum concurrency: 2. Pass `agent` to run as a named custom agent (configured via /agents); omit for an ad-hoc read-only subtask. The child has no access to nested agent.run.",
```

Update the schema JSON to include `agent`:

```go
Schema: json.RawMessage(
	`{"type":"object","properties":{"prompt":{"type":"string","description":"The subtask description passed verbatim to the child agent."},"description":{"type":"string","description":"A short label for the subtask shown in the tool result summary."},"agent":{"type":"string","description":"Name of a configured custom agent to run as. Omit for an ad-hoc subtask."}},"required":["prompt","description"],"additionalProperties":false}`,
),
```

- [ ] **Step 4: Update `buildSubagentFactory` in `internal/app/app.go`**

Change `buildSubagentFactory` (lines 814-828) to accept `agentName`, resolve a custom agent when set, **and** wire token tracking. Today the factory sets no `Pricing`, `UsageObserver`, or `MetricsObserver`, so subagent tokens/cost are invisible to the parent turn and `/context`. The child's `UsageObserver` folds into the **parent** session's `state.SetTurnUsage` total (so `/context` reflects subagent work), and the child's `MetricsObserver` records against the **parent** session's `turn_metrics` (so subagent turns are queryable via `/history`). The child session stays separate (no double-counting: parent turns observed by parent runner, child turns by child runner, both to the parent session).

```go
func buildSubagentFactory(cfg config.Config, parentState *session.State, parentProvider provider.Provider, parentReg *registry.Registry, pol *policy.PolicyEngine, defaultModel string, router *routing.StaticRouter, database *db.DB, projectID int64) agent.SubagentRunnerFactory {
	subtaskIters := cfg.Agent.SubtaskIterations
	if subtaskIters <= 0 {
		subtaskIters = defaultSubtaskIterations
	}
	metricsObserver := metricsRecorder(database, projectID, parentState.SessionID(), parentState.Logger())
	return func(agentName string) (*agent.Runner, error) {
		childState := session.New(parentState.Config, parentState.WorkingDir, time.Now(), session.Persistence{}, session.WithDepth(parentState.SubagentDepth()+1))
		roReg := agent.SubtaskScopeView(parentReg)
		role := agent.RoleSubtask
		model := defaultModel
		var addendum string
		var pricingRates pricing.ModelPricing
		if agentName != "" && router != nil {
			route, err := router.ResolveCustomAgent(agentName, agent.RoleSubtask)
			if err != nil {
				return nil, fmt.Errorf("agent.run: %w", err)
			}
			model = route.Preset.Model
			pricingRates = pricing.Lookup(route.Preset)
			roReg = agent.SubtaskScopeView(parentReg)
			if route.CustomAgent != nil {
				ca := route.CustomAgent
				addendum = ca.SystemPrompt
				if len(ca.ToolDenylist) > 0 {
					roReg = agent.DenylistView(roReg, ca.ToolDenylist)
				}
				if ca.MaxIterations > 0 {
					subtaskIters = ca.MaxIterations
				}
			}
		}
		child := agent.NewRunner(parentProvider, roReg, pol, childState, model)
		child.Role = role
		child.MaxToolIterations = subtaskIters
		child.NativeTools = true
		child.SystemPromptAddendum = addendum
		child.Pricing = pricingRates
		child.MetricsObserver = metricsObserver
		// Fold the child's token usage into the parent session's running
		// total so /context and the session usage view include subagent
		// work. The child session is separate; this is additive to the
		// parent's own turns, not a double-count.
		child.UsageObserver = func(usage schema.TokenUsage) {
			parentState.SetTurnUsage(parentState.TurnUsageAsInt() + usage.PromptTokens + usage.CompletionTokens)
		}
		return child, nil
	}
}
```

`parentState.TurnUsageAsInt()` — if `session.State` does not expose the current usage as an int (it exposes `TurnUsage() (used, window int)`), use `used, _ := parentState.TurnUsage(); parentState.SetTurnUsage(used + ...)`. Adapt to the real accessor.

Update the call site (around line 502) to pass the new deps — `database` and `projectID` are already in `buildAgentRunner`'s scope:

```go
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	if err := reg.Register(agent.NewSubagentTool(
		buildSubagentFactory(cfg, state, resolvedProvider, reg, pol, route.Preset.Model, router, database, projectID),
		reg,
		state,
	)); err != nil {
```

Add imports to `internal/app/app.go` if absent: `"marshal/internal/llm/pricing"`, `"marshal/internal/llm/schema"` (schema may already be imported for the parent's UsageObserver).

- [ ] **Step 5: Run the failing test**

Run: `go test ./internal/agent/ -run TestNewSubagentToolAgentArg -v`
Expected: PASS.

- [ ] **Step 6: Add a subagent token-tracking test**

Add to `internal/app/app_test.go` (or a new `internal/app/subagent_tracking_test.go`), exercising `buildSubagentFactory` directly. The test asserts a child runner built with a named agent carries `Pricing`, `MetricsObserver`, and a `UsageObserver` that folds into the parent session's total:

```go
func TestSubagentFactoryWiresTokenTracking(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{"fast": {Provider: "ollama", Model: "gpt-4o-mini"}}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"my-scout": {Name: "my-scout", Preset: "fast"},
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	// Fake provider not needed: we call the factory closure, not RunTask.
	prov := (provider.Provider)(nil)
	reg := registry.New()
	factory := buildSubagentFactory(cfg, parentState, prov, reg, policyEngine(t), "fallback", router, nil, 1)
	child, err := factory("my-scout")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.Pricing.InputPerMTokCents == 0 && child.Pricing.OutputPerMTokCents == 0 {
		t.Fatal("child.Pricing should be resolved from the custom agent's preset (gpt-4o-mini is priced)")
	}
	if child.MetricsObserver == nil {
		t.Fatal("child.MetricsObserver should be set so subagent turns persist to turn_metrics")
	}
	if child.UsageObserver == nil {
		t.Fatal("child.UsageObserver should be set so subagent usage rolls up to the parent session")
	}
	// The UsageObserver folds into the parent session's running total.
	child.UsageObserver(schema.TokenUsage{PromptTokens: 100, CompletionTokens: 50})
	used, _ := parentState.TurnUsage()
	if used != 150 {
		t.Fatalf("parent usage after child observe = %d, want 150", used)
	}
}

func TestSubagentFactoryAdHocHasObserversToo(t *testing.T) {
	// The ad-hoc path (no agent name) must ALSO wire observers, closing
	// the gap for today's plain agent.run children, not just named agents.
	cfg := config.Default()
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	factory := buildSubagentFactory(cfg, parentState nil, reg, policyEngine(t), "fallback", router, nil, 1)
	child, err := factory("")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.UsageObserver == nil || child.MetricsObserver == nil {
		t.Fatal("ad-hoc subagent children must also carry UsageObserver + MetricsObserver")
	}
}
```

Note: `database` may be nil for the MetricsObserver test path — `metricsRecorder` returns a closure that calls `database.InsertTurnMetrics`; passing nil will panic when the observer fires. For the test, pass a real or stub `*db.DB` if your helpers provide one, or assert only `child.MetricsObserver != nil` and skip firing it. Adapt to the existing test helpers in `app_test.go`.

- [ ] **Step 7: Run the token-tracking test**

Run: `go test ./internal/app/ -run 'TestSubagentFactoryWiresTokenTracking|TestSubagentFactoryAdHocHasObserversToo' -v`
Expected: PASS.

- [ ] **Step 8: Run the full agent + app packages**

Run: `go test ./internal/agent/... ./internal/app/...`
Expected: PASS. If existing subagent tests call `NewSubagentTool` with the old factory arity, update them to `func(agentName string) (*Runner, error)`.

- [ ] **Step 9: Commit**

```bash
git add internal/agent/subagent.go internal/agent/subagent_test.go internal/app/app.go internal/app/app_test.go
git commit -m "feat(agent): agent.run optional custom-agent arg + subagent token tracking"
```

---

## Task 7: `/agents` slash command + dispatch

**Files:**
- Modify: `internal/commands/commands.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/model.go`
- Test: `internal/app/tui/model_test.go` (added)

**Interfaces:**
- Consumes: `openAgentsRoster` (Task 8 provides the panel).
- Produces: `/agents` command registration and TUI dispatch; `openAgentsRoster(args string)` method on `Model`.

- [ ] **Step 1: Write failing test**

In `internal/app/tui/model_test.go`, add:

```go
func TestAgentsCommandOpensRoster(t *testing.T) {
	m := newTestModel(t) // existing helper
	m.dispatchCommand("/agents")
	if !m.dock.IsOpen() {
		t.Fatal("/agents did not open the dock")
	}
	if _, ok := m.dock.Panel().(*agents.Panel); !ok {
		t.Fatalf("dock panel = %T, want *agents.Panel", m.dock.Panel())
	}
}

func TestAgentsCommandArgPreFilters(t *testing.T) {
	m := newTestModel(t)
	m.dispatchCommand("/agents planner")
	panel, ok := m.dock.Panel().(*agents.Panel)
	if !ok {
		t.Fatalf("dock panel = %T, want *agents.Panel", m.dock.Panel())
	}
	if panel.FilterValue() != "planner" {
		t.Fatalf("filter = %q, want planner", panel.FilterValue())
	}
}
```

`agents` is imported as `"marshal/internal/app/tui/agents"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run TestAgentsCommand -v`
Expected: FAIL — `agents.Panel` undefined; `/agents` not registered.

- [ ] **Step 3: Register the command**

In `internal/commands/commands.go`, add to the `commands` slice (in the Workflows group, after `sdd`):

```go
		{
			Name:        "agents",
			Description: "Configure the agent roster: roles, models, custom agents, swarm & SDD budgets",
			Group:       groupWorkflow,
			TUIOnly:     true,
		},
```

- [ ] **Step 4: Add the dispatch effect**

In `internal/app/tui/commands_dispatch.go`, add to `tuiCommandEffects`:

```go
		"agents": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			m.openAgentsRoster(strings.TrimSpace(strings.Join(args, " ")))
			m.refreshViewport()
			return m, nil
		},
```

- [ ] **Step 5: Add `openAgentsRoster` to `Model`**

In `internal/app/tui/model.go`, add (near `openSettingsBrowser` around line 412):

```go
func (m *Model) openAgentsRoster(arg string) {
	panel := agents.NewRosterPanel(m.state.Config, projectConfigPath(m.state.WorkingDir), arg, m.state.AgentRunner())
	m.dock.Open(panel)
}
```

`agents.NewRosterPanel` is produced by Task 8. Add the import `"marshal/internal/app/tui/agents"`. If `m.state.AgentRunner()` does not exist, pass `m.swarmRunner` (used for Run-now dispatch); the panel accepts an `AgentRunner` interface (may be nil — Run-now is disabled when nil).

- [ ] **Step 6: Run test to verify it fails on the panel** (command now registered, panel not built)

Run: `go test ./internal/app/tui/ -run TestAgentsCommand -v`
Expected: FAIL — `agents.NewRosterPanel` undefined (Task 8).

- [ ] **Step 7: Commit** (the panel lands in Task 8; commit the command + dispatch now to keep tasks reviewable)

```bash
git add internal/commands/commands.go internal/app/tui/commands_dispatch.go internal/app/tui/model.go
git commit -m "feat(commands): register /agents slash command + dispatch"
```

---

## Task 8: Roster panel — root cast table + profile switch + budget drills

**Files:**
- Create: `internal/app/tui/agents/panel.go`, `internal/app/tui/agents/panel_test.go`

**Interfaces:**
- Consumes: `routing.AllRoles`, `StaticRouter.Cast`, `routing.ResolveCustomAgent`, `cfg.CustomAgents`, the settings `state`/`Registry`/`fieldList`/`picker` machinery, `swarmFrame`/`sddFrame` builders.
- Produces: `agents.NewRosterPanel(cfg, cfgPath, filter, runner) *Panel`; `agents.Panel` implements `dock.Panel`; `Panel.FilterValue() string`.

- [ ] **Step 1: Write failing tests for resolution glyphs**

In `internal/app/tui/agents/panel_test.go`:

```go
package agents

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/dock"
	"marshal/internal/llm/routing"
)

func TestRosterRendersPresetBinding(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{"reason": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RolePlanner: {Preset: "reason"},
		}},
	}
	cfg.Profile.Default = "local_balanced"
	p := NewRosterPanel(cfg, "", "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "●") {
		t.Fatalf("preset binding should show ● glyph:\n%s", view)
	}
}

func TestRosterRendersCustomAgentBinding(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{"reason": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleReviewer: {CustomAgent: "strict"},
		}},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"strict": {Name: "strict", Preset: "reason"},
	}
	cfg.Profile.Default = "local_balanced"
	p := NewRosterPanel(cfg, "", "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "◆") {
		t.Fatalf("custom-agent binding should show ◆ glyph:\n%s", view)
	}
}

func TestRosterRendersFallbackGlyph(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{"impl": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "impl"},
		}},
	}
	cfg.Profile.Default = "local_balanced"
	p := NewRosterPanel(cfg, "", "", nil)
	view := p.View(80, 24)
	if !containsGlyph(view, "↩") {
		t.Fatalf("unset role should show ↩ fallback glyph:\n%s", view)
	}
}

func TestRosterFilterPreFill(t *testing.T) {
	cfg := config.Default()
	p := NewRosterPanel(cfg, "", "planner", nil)
	if p.FilterValue() != "planner" {
		t.Fatalf("filter = %q, want planner", p.FilterValue())
	}
}

func containsGlyph(s string, g string) bool {
	return strings.Contains(s, g)
}
```

(Add `"strings"` to the test imports.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/agents/ -v`
Expected: FAIL — package doesn't exist.

- [ ] **Step 3: Implement `panel.go`**

Create `internal/app/tui/agents/panel.go`. This is the largest file; it reuses the settings machinery. Structure:

```go
// Package agents renders the /agents roster panel: a resolved cast table
// for the routing roles + custom agents + swarm/SDD budgets, edited in
// place via the settings field/picker machinery.
package agents

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/routing"
)

// Panel is the docked /agents roster. It owns a settings.state (so it
// reuses the field/picker/persistence machinery) and a flat fieldList
// built from the resolved cast.
type Panel struct {
	state    *settings.State
	cfgPath  string
	filter   textinput.Model
	list     *settings.FieldList
	runner   dock.AgentRunner // for Run-now; may be nil
}

var _ dock.Panel = (*Panel)(nil)

// NewRosterPanel builds the roster panel pre-filtered by `arg` (a role
// or agent name). runner is used for Run-now dispatch (may be nil).
func NewRosterPanel(cfg config.Config, cfgPath, arg string, runner dock.AgentRunner) *Panel {
	st := settings.NewState(cfg)
	f := textinput.New()
	f.SetVirtualCursor(true)
	f.Focus()
	f.SetValue(arg)
	f.CursorEnd()
	p := &Panel{state: st, cfgPath: cfgPath, filter: f, runner: runner}
	p.list = settings.NewFieldList(p.matchedFields)
	return p
}

func (p *Panel) FilterValue() string { return p.filter.Value() }
```

`dock.AgentRunner` — if that interface does not exist on `dock`, define a minimal one in `agents` or accept `tui.AgentRunner` via the caller (the caller is `Model`, which can pass itself). Simplest: accept `any` and type-assert at Run-now time, OR have the caller pass a `func(goal string) tea.Cmd` dispatch closure. To avoid a circular import (`agents` → `tui`), define the dispatch as a closure:

```go
// DispatchFn starts an agent run for `goal`. nil disables Run-now.
type DispatchFn func(goal string) tea.Cmd

type Panel struct {
	state   *settings.State
	cfgPath string
	filter  textinput.Model
	list    *settings.FieldList
	dispatch DispatchFn
}

func NewRosterPanel(cfg config.Config, cfgPath, arg string, dispatch DispatchFn) *Panel {
	// ... dispatch stored instead of runner
}
```

And in Task 7's `openAgentsRoster`, pass a closure that calls `m.startAgentRun`:

```go
func (m *Model) openAgentsRoster(arg string) {
	dispatch := func(goal string) tea.Cmd {
		// Run-now: build a runner bound to the custom agent (RoleSubtask)
		// via the existing factory, then startAgentRun. For v1, reuse
		// m.swarmRunner plumbing if the agent is unbound; full custom-
		// agent Run-now wiring is Task 9.
		// Minimal v1: prompt for goal inline, dispatch through a new
		// custom-agent runner. See Task 9.
		return nil
	}
	m.dock.Open(agents.NewRosterPanel(m.state.Config, projectConfigPath(m.state.WorkingDir), arg, dispatch))
}
```

Run-now is fleshed out in Task 9; Task 8 leaves the closure as a stub that emits a "Run now wired in Task 9" system message when invoked, so the panel is testable without it.

The rest of `panel.go` builds the matched-field list from `routing.AllRoles` + `cfg.CustomAgents` + the budget drills. Each role row is a `kindPicker` field whose `pickOptions` lists presets (`●`) + custom agents (`◆`) + unset. The `desc` carries the resolved provider/model + source glyph. The exact field construction mirrors `frames_profiles.go`'s `rolePresetField` (read that file first). The budget rows drill into `swarmFrame`/`sddFrame` via `settings`'s exported builders — if those are unexported, add thin exported wrappers in `settings`:

In `internal/app/tui/settings/frames_basic.go`, export:

```go
func SwarmFrame(s *State) *Frame { return swarmFrame(s) }
func SDDFrame(s *State) *Frame    { return sddFrame(s) }
```

(Where `State`/`Frame` are the exported names for `state`/`frame`; rename or alias as the package already exposes them — check `settings/state.go` for the exported surface.)

Because `settings` field types may be unexported, the cleanest integration is to have `agents` build its own `fieldList` by calling *exported* helpers from `settings`. If the field machinery is entirely unexported (it is — `field`, `fieldList`, `paneStack` are lowercase), then `agents` cannot reuse it without exporting. **Decision:** export the minimal set from `settings`: `Field`, `FieldList`, `PaneStack`, `State`, `Frame`, `NewState`, `NewFieldList`, plus the frame builders. This is a one-time export pass in `settings` (rename `field`→`Field`, etc., or add exported type aliases). Do that rename as the first sub-step of Task 8.

- [ ] **Step 4: Export the settings machinery** (rename lowercase → exported across `settings`)

Rename in `internal/app/tui/settings/`:
- `state` → `State` (file `state.go`), `frame` → `Frame`, `field` → `Field`, `fieldList` → `FieldList`, `paneStack` → `PaneStack`.
- `newFrame` → `NewFrame`, `newFieldList` → `NewFieldList`, `newPaneStack` → `NewPaneStack`.
- Export the frame builders: `SwarmFrame`, `SDDFrame`, `ProfilesFrame`, `PresetsFrame`, `RolePresetField` (or the helper `agents` needs).

This is a mechanical rename. Run `go test ./internal/app/tui/settings/...` after to confirm nothing broke (the package's own tests use the old names — update them in the same pass).

- [ ] **Step 5: Build the matched-field list**

Implement `matchedFields()` to return `[]*settings.Field`:
1. A profile-header row (`kindEnum`-like) whose Enter opens a picker of profile names + "New…".
2. For each role in `routing.AllRoles` (grouped: swarm roles, SDD roles, housekeeping), a `kindPicker` field whose `pickOptions` lists presets (badge `●`), custom agents (badge `◆`), and "(unset — fallback)" (badge `clear`). `pickOnPick` writes the `RoleBinding`. `desc` is `resolvedProvider/resolvedModel · <glyph> <source>`.
3. A "Custom Agents" header + one row per `cfg.CustomAgents` key, `kindDrill` into a config frame (built in Task 9) plus a Run-now action.
4. "Run budgets" header + two drill rows calling `settings.SwarmFrame`/`settings.SDDFrame`.

Use `routing.NewStaticRouter(cfg.RoutingConfig()).Cast(routing.AllRoles)` to compute the resolved provider/model + source for the `desc`.

- [ ] **Step 6: Implement `Update` and `View`**

`Update` mirrors `settings.BrowserPanel.Update`: handle `picker.PickedMsg`/`picker.CancelledMsg`, forward keys to the filter or the active list/drill, call `flushChanges` (the settings persistence path — expose `settings.FlushChanges` or replicate the small save block). `View` renders the title "Agents", the filter input, the list, and a contextual footer, via `chrome.PanelWithHints`.

For persistence, reuse `settings`'s save by exposing a `settings.Save(cfgPath, cfg) error` thin wrapper over `config.SaveProjectConfig`, and emit `settings.ChangedMsg` so `Model` reloads.

- [ ] **Step 7: Run the failing tests**

Run: `go test ./internal/app/tui/agents/ -v`
Expected: PASS for the three glyph tests + the filter test.

- [ ] **Step 8: Run the broader TUI suite**

Run: `go test ./internal/app/tui/...`
Expected: PASS (the settings export rename should not change behavior).

- [ ] **Step 9: Commit**

```bash
git add internal/app/tui/agents/ internal/app/tui/settings/
git commit -m "feat(tui): /agents roster panel (resolved cast + profiles + budgets)"
```

---

## Task 9: Custom-agent config frame + Run-now dispatch

**Files:**
- Modify: `internal/app/tui/agents/panel.go`, `internal/app/tui/model.go`
- Test: `internal/app/tui/agents/panel_test.go`

**Interfaces:**
- Consumes: `cfg.CustomAgents`, `settings.Field` builders, `Model.startAgentRun` (Task 7 closure).
- Produces: the custom-agent config drill (preset picker, system prompt, tool denylist, approval mode, max iterations, context); Run-now dispatches a custom agent.

- [ ] **Step 1: Write failing tests**

In `internal/app/tui/agents/panel_test.go`:

```go
func TestCustomAgentDrillEditsPreset(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{"fast": {Provider: "ollama", Model: "qwen"}}
	cfg.CustomAgents = map[string]routing.CustomAgent{"my-scout": {Name: "my-scout", Preset: "fast"}}
	p := NewRosterPanel(cfg, "", "", nil)
	// Navigate to the custom-agents group, drill into my-scout, edit preset.
	// Assert cfg.CustomAgents["my-scout"].Preset changed after a pick.
	// (Use the panel's exported test seam — e.g. a helper that drives a
	// pick on a named field id.)
}

func TestToolDenylistValidation(t *testing.T) {
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{"x": {Name: "x", Preset: "p"}}
	p := NewRosterPanel(cfg, "", "", nil)
	// Setting ToolDenylist to ["nonexistent_tool"] marks the row invalid.
	if p.validateToolDenylist([]string{"nonexistent_tool"}) == nil {
		t.Fatal("invalid tool name should fail validation")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/agents/ -run 'TestCustomAgentDrillEditsPreset|TestToolDenylistValidation' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the custom-agent config frame**

In `panel.go`, add a builder that returns a `*settings.Frame` for a named custom agent, with fields:
- Preset: `kindPicker` over `cfg.Models.Presets` (like `rolePresetField`).
- System prompt: `kindScalar` text edit.
- Tool denylist: a new `kindStringList` (or `kindScalar` with comma-split + validation) — validate names against the live registry passed into `NewRosterPanel` (add a `registry *registry.Registry` arg).
- Approval mode: `kindEnum` over `["plan","default","edit","copilot","auto"]`.
- Max iterations: `kindScalar` int.
- Context: `max_repo_context_tokens` int.

Add `validateToolDenylist(names []string) error` that checks each name against the registry.

- [ ] **Step 4: Wire Run-now dispatch**

In `panel.go`, the custom-agent row's Run-now action calls the `dispatch` closure with a goal. For v1, Run-now first opens an inline goal prompt (a small `textinput` or a `picker` with `SetAllowCustom`) — when the user enters a goal and confirms, the closure is invoked. The closure (set in `openAgentsRoster`, Task 7) builds a runner bound to the custom agent (via the agent-aware subagent factory from Task 6, run as `RoleSubtask`) and calls `m.startAgentRun(runner, goal)`.

Update `openAgentsRoster` in `internal/app/tui/model.go` to pass a real closure:

```go
func (m *Model) openAgentsRoster(arg string) {
	dispatch := func(agentName, goal string) tea.Cmd {
		return func() tea.Msg {
			// Build a runner for the named custom agent via the app's
			// agent-aware factory (Task 6), then startAgentRun.
			// For v1, reuse the swarm runner path: the factory resolves
			// the agent's overrides. This requires exposing a
			// "build single custom-agent runner" helper from app.go.
			runner := m.buildCustomAgentRunner(agentName)
			return startAgentRunMsg{runner: runner, goal: goal}
		}
	}
	m.dock.Open(agents.NewRosterPanel(m.state.Config, projectConfigPath(m.state.WorkingDir), arg, dispatch, m.toolReg))
}
```

Add `buildCustomAgentRunner(name) AgentRunner` to `Model` (or app) that uses the agent-aware subagent factory with the named agent. Handle the `startAgentRunMsg` in `Model.Update` to call `startAgentRun`. (If the existing `agentFinishedMsg`/`startAgentRun` plumbing needs a new msg type, add `type startAgentRunMsg struct{ runner AgentRunner; goal string }` and route it to `startAgentRun`.)

- [ ] **Step 5: Run tests**

Run: `go test ./internal/app/tui/agents/ -v`
Expected: PASS.

- [ ] **Step 6: Run full TUI + app**

Run: `go test ./internal/app/... ./internal/app/tui/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/agents/ internal/app/tui/model.go
git commit -m "feat(tui): custom-agent config frame + Run-now dispatch"
```

---

## Task 10: `Custom Agents` settings section + help legend + final integration

**Files:**
- Modify: `internal/app/tui/settings/sections.go`, `internal/app/tui/help/help.go`, `internal/commands/commands.go` (help text)
- Test: `internal/app/tui/settings/sections_test.go`, `internal/commands/commands_test.go`

**Interfaces:**
- Consumes: all prior tasks.
- Produces: a "Custom Agents" section in the generic settings browser (parity); `?` legend entries for the roster; `/help` lists `/agents`.

- [ ] **Step 1: Add the settings section**

In `internal/app/tui/settings/sections.go`, add to `sectionList()` (after "Profiles"):

```go
		{id: "custom_agents", title: "Custom Agents", root: customAgentsFrame},
```

Add `customAgentsFrame(s *state) *frame` in a new file `internal/app/tui/settings/frames_custom_agents.go`, mirroring `presetsFrame`: an entries drill over `cfg.CustomAgents` with add/yank/paste, each entry's frame exposing the same fields as the `/agents` custom-agent config frame (Task 9). To avoid duplication, extract the per-agent field builder into a shared exported helper `settings.CustomAgentFields(s, name) []*Field` and call it from both the settings frame and the `/agents` panel.

- [ ] **Step 2: Add the `?` legend**

In `internal/app/tui/help/help.go`, add roster legend entries (or handle `?` within the `agents.Panel` itself by rendering a small overlay). Simplest: the panel renders its own legend on `?` (it already owns `Update`). Add to `Panel.Update`:

```go
		case "?":
			p.showLegend = !p.showLegend
			return nil
```

And render the legend (6 lines: `● preset bound`, `◆ custom agent bound`, `↩ impl fallback`, `legacy`, `⚠ unresolved`, `← / → drill`) at the top of `View` when `showLegend`.

- [ ] **Step 3: Verify `/help` lists `/agents`**

The `RegisterAll` change in Task 7 already adds it to the Workflows group; `/help` auto-lists non-hidden commands. Add a test in `internal/commands/commands_test.go`:

```go
func TestAgentsCommandListed(t *testing.T) {
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatal(err)
	}
	c, ok := reg.Lookup("agents")
	if !ok {
		t.Fatal("/agents not registered")
	}
	if c.Group != "Workflows" {
		t.Fatalf("group = %q, want Workflows", c.Group)
	}
}
```

- [ ] **Step 4: Run the full suite + build**

Run: `go test ./...`
Run: `go build ./cmd/marshal`
Run: `gofmt -w . && go vet ./...`
Expected: all PASS, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/settings/ internal/app/tui/agents/ internal/commands/
git commit -m "feat: Custom Agents settings section + roster legend + /help"
```

---

## Self-Review

**Spec coverage:**
- §1 Data Model → Tasks 1, 2 (types, config wiring, migration). ✓
- §2 Dispatch & Resolution → Tasks 3 (router), 5 (factory), 6 (agent.run). ✓
- §3 TUI Panel → Tasks 7 (command), 8 (panel), 9 (config frame + Run-now), 10 (legend). ✓
- §4 Testing → embedded as TDD steps in every task. ✓

**Placeholder scan:** No TBD/TODO. The one area left to the implementer's judgment is the exact test-helper names in `app_test.go` (Task 5 Step 1) — flagged with "adapt to the existing ones" because the helper inventory wasn't fully enumerated; this is unavoidable without running a deeper read, and the instruction is explicit about what to adapt.

**Type consistency:** `RoleBinding{Preset|CustomAgent}`, `Route.CustomAgent *CustomAgent`, `ResolveCustomAgent(name, asRole)`, `DenylistView(src, deny)`, `SystemPromptAddendum string`, `SubagentRunnerFactory func(agentName string) (*Runner, error)`, `DispatchFn func(agentName, goal string) tea.Cmd` — used consistently across tasks. The `dispatch` signature shifts from `func(goal string)` (Task 8) to `func(agentName, goal string)` (Task 9); Task 9 Step 4 updates the `NewRosterPanel` call site, and Task 8's stub closure is replaced. The signature is finalized in Task 9.

**Note for the implementer:** Task 8's settings export rename (`field`→`Field` etc.) is the riskiest mechanical step — it touches every file in `internal/app/tui/settings/`. Do it as one atomic commit and run `go test ./internal/app/tui/settings/...` immediately after. If the package's internal coupling makes a full export infeasible, the fallback is to keep the machinery unexported and build the `agents` panel *inside* the `settings` package as `settings.RosterPanel`, re-exported via `agents` as a type alias. That avoids the rename but couples the two — prefer the export rename unless it balloons.