# Milestone L Role-Based Model Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add static role-based model routing v1: model presets, agent profiles, static route resolution, active route display, local-only policy, and role-specific context budgets for the current single-agent runtime.

**Architecture:** A new `internal/llm/routing` package owns routing types and resolution. `internal/app/config` parses presets/profiles/budgets and preserves legacy `[agent]` fallback. `session.State` stores active route metadata for `tui.Model.View`. App wiring resolves a single active route, builds the configured provider, and passes the resolved model to `agent.Runner`.

**Tech Stack:** Go 1.26.1, standard library, existing config loader, provider factory, session/TUI, agent runner, and contextpack package. No new external dependencies.

## Global Constraints

- Keep Milestone L static; do not add dynamic escalation or multi-agent orchestration.
- Existing legacy `[agent] provider/model` configs must continue to work.
- Default context-pack budget remains `12000` estimated tokens unless a positive role budget overrides it.
- Local-only policy blocks remote presets when `privacy.remote_providers_allowed=false`.
- TUI route display is read-only.
- Routing package must not import app, TUI, provider factory, tools, database, or agent packages.
- `go test ./...` must pass.

---

## File Structure

- Create `internal/llm/routing/types.go` for `AgentRole`, `ModelPreset`, `AgentProfile`, `ContextBudget`, `TaskProfile`, `Route`, and config inputs.
- Create `internal/llm/routing/router.go` for `StaticRouter` and role resolution.
- Create `internal/llm/routing/router_test.go` for static router tests.
- Modify `internal/app/config/config.go` to parse model presets, agent profiles, and role context budgets.
- Modify `internal/app/config/config_test.go` to test parsing and merge behavior.
- Modify `internal/app/session/session.go` to store active route metadata.
- Modify `internal/app/session/session_test.go` to test active route copy-safe storage.
- Modify `internal/app/tui/model.go` to render active route metadata.
- Modify `internal/app/tui/model_test.go` to test route display and inactive route state.
- Modify `internal/contextpack/builder.go` and `internal/contextpack/contextpack_test.go` to support rebudgeting an existing pack.
- Modify `internal/agent/runner.go` to accept optional route resolver and role budget behavior per turn.
- Modify `internal/agent/runner_test.go` to test role/provider selection and context budget application.
- Modify `internal/app/app.go` to build providers/runners from routed config while preserving runner-disabled fallback.
- Modify `internal/app/app_test.go` to test app wiring with injected fake provider factory.
- Modify `docs/09-configuration-examples.md` if field names drift from existing examples.
- Modify `docs/10-mvp-implementation-checklist.md` after implementation passes.
- Create `docs/plans/2026-07-02-milestone-l-role-based-model-routing.md` as the task status table.

---

### Task 1: Add Static Routing Core

**Files:**
- Create: `internal/llm/routing/types.go`
- Create: `internal/llm/routing/router.go`
- Create: `internal/llm/routing/router_test.go`

**Interfaces:**
- Produces: `routing.AgentRole`, role constants, `routing.ModelPreset`, `routing.AgentProfile`, `routing.ContextBudget`, `routing.TaskProfile`, `routing.Route`, `routing.Config`, `routing.StaticRouter`, `routing.NewStaticRouter`, `(*StaticRouter).Resolve`.
- Consumes: standard library only.

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/routing/router_test.go`:

```go
package routing

import (
	"errors"
	"testing"
)

func testRouter() *StaticRouter {
	return NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		RemoteAllowed: false,
		Presets: map[string]ModelPreset{
			"fast": {
				Name:      "fast",
				Provider:  "ollama",
				Model:     "qwen2.5-coder:7b",
				LocalOnly: true,
			},
			"coder": {
				Name:      "coder",
				Provider:  "ollama",
				Model:     "qwen2.5-coder:14b",
				LocalOnly: true,
			},
			"remote": {
				Name:      "remote",
				Provider:  "openrouter",
				Model:     "anthropic/claude-sonnet-4",
				LocalOnly: false,
			},
		},
		Profiles: map[string]AgentProfile{
			"local_balanced": {
				Name: "local_balanced",
				Roles: map[AgentRole]string{
					RoleRepoScout:   "fast",
					RoleImplementer: "coder",
				},
			},
		},
		ContextBudgets: map[AgentRole]ContextBudget{
			RoleImplementer: {MaxRepoContextTokens: 48000, IncludeRawCode: true},
		},
		LegacyProvider: "legacy-provider",
		LegacyModel:    "legacy-model",
	})
}

func TestResolveQuestionUsesRepoScout(t *testing.T) {
	route, err := testRouter().Resolve(TaskProfile{Class: "question"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleRepoScout {
		t.Fatalf("Role = %q, want %q", route.Role, RoleRepoScout)
	}
	if route.Preset.Name != "fast" || route.Preset.Model != "qwen2.5-coder:7b" {
		t.Fatalf("Preset = %#v, want fast qwen2.5-coder:7b", route.Preset)
	}
	if route.Legacy {
		t.Fatal("Legacy = true, want false")
	}
}

func TestResolveEditUsesImplementerAndBudget(t *testing.T) {
	route, err := testRouter().Resolve(TaskProfile{Class: "edit"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleImplementer {
		t.Fatalf("Role = %q, want %q", route.Role, RoleImplementer)
	}
	if route.ContextBudget.MaxRepoContextTokens != 48000 || !route.ContextBudget.IncludeRawCode {
		t.Fatalf("ContextBudget = %#v", route.ContextBudget)
	}
}

func TestResolveFallsBackToImplementerForMissingRole(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		Presets: map[string]ModelPreset{
			"coder": {Name: "coder", Provider: "ollama", Model: "coder", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"local_balanced": {
				Name: "local_balanced",
				Roles: map[AgentRole]string{
					RoleImplementer: "coder",
				},
			},
		},
	})
	route, err := router.Resolve(TaskProfile{Class: "question"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleImplementer {
		t.Fatalf("Role = %q, want implementer fallback", route.Role)
	}
}

func TestResolveUsesLegacyWhenNoProfileRouteExists(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "missing",
		LegacyProvider: "ollama",
		LegacyModel:    "qwen2.5-coder:14b",
	})
	route, err := router.Resolve(TaskProfile{Class: "question"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if !route.Legacy {
		t.Fatal("Legacy = false, want true")
	}
	if route.Preset.Provider != "ollama" || route.Preset.Model != "qwen2.5-coder:14b" {
		t.Fatalf("legacy route preset = %#v", route.Preset)
	}
}

func TestResolveMissingProfileWithoutLegacyReturnsError(t *testing.T) {
	_, err := NewStaticRouter(Config{DefaultProfile: "missing"}).Resolve(TaskProfile{Class: "question"})
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("err = %v, want ErrProfileNotFound", err)
	}
}

func TestResolveMissingPresetReturnsError(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		Profiles: map[string]AgentProfile{
			"local_balanced": {Name: "local_balanced", Roles: map[AgentRole]string{RoleImplementer: "missing"}},
		},
	})
	_, err := router.Resolve(TaskProfile{Class: "edit"})
	if !errors.Is(err, ErrPresetNotFound) {
		t.Fatalf("err = %v, want ErrPresetNotFound", err)
	}
}

func TestResolveBlocksRemotePresetWhenRemoteDisabled(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "remote_profile",
		RemoteAllowed: false,
		Presets: map[string]ModelPreset{
			"remote": {Name: "remote", Provider: "openrouter", Model: "model", LocalOnly: false},
		},
		Profiles: map[string]AgentProfile{
			"remote_profile": {Name: "remote_profile", Roles: map[AgentRole]string{RoleImplementer: "remote"}},
		},
	})
	_, err := router.Resolve(TaskProfile{Class: "edit"})
	if !errors.Is(err, ErrRemoteProviderBlocked) {
		t.Fatalf("err = %v, want ErrRemoteProviderBlocked", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/llm/routing -v
```

Expected: FAIL because the package implementation does not exist.

- [ ] **Step 3: Add routing types**

Create `internal/llm/routing/types.go`:

```go
package routing

type AgentRole string

const (
	RoleRouter           AgentRole = "router"
	RoleKnowledge        AgentRole = "knowledge"
	RoleSummarizer       AgentRole = "summarizer"
	RoleRepoScout        AgentRole = "repo_scout"
	RoleTester           AgentRole = "tester"
	RolePlanner          AgentRole = "planner"
	RoleImplementer      AgentRole = "implementer"
	RoleReviewer         AgentRole = "reviewer"
	RoleSecurityReviewer AgentRole = "security_reviewer"
)

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

type Config struct {
	DefaultProfile string
	RemoteAllowed  bool
	Presets        map[string]ModelPreset
	Profiles       map[string]AgentProfile
	ContextBudgets map[AgentRole]ContextBudget
	LegacyProvider string
	LegacyModel    string
}
```

- [ ] **Step 4: Add static router**

Create `internal/llm/routing/router.go`:

```go
package routing

import (
	"errors"
	"fmt"
)

var (
	ErrProfileNotFound       = errors.New("routing: profile not found")
	ErrPresetNotFound        = errors.New("routing: preset not found")
	ErrRemoteProviderBlocked = errors.New("routing: remote provider blocked")
	ErrNoRoute               = errors.New("routing: no route available")
)

type StaticRouter struct {
	config Config
}

func NewStaticRouter(config Config) *StaticRouter {
	return &StaticRouter{config: config}
}

func (r *StaticRouter) Resolve(task TaskProfile) (Route, error) {
	role := roleForTaskClass(task.Class)
	route, err := r.resolveProfileRole(role)
	if err == nil {
		return route, nil
	}
	if role != RoleImplementer {
		if fallback, fallbackErr := r.resolveProfileRole(RoleImplementer); fallbackErr == nil {
			return fallback, nil
		}
	}
	if legacy, ok := r.legacyRoute(role); ok {
		return legacy, nil
	}
	return Route{}, err
}

func roleForTaskClass(class string) AgentRole {
	switch class {
	case "question":
		return RoleRepoScout
	case "edit", "command":
		return RoleImplementer
	default:
		return RoleImplementer
	}
}

func (r *StaticRouter) resolveProfileRole(role AgentRole) (Route, error) {
	profile, ok := r.config.Profiles[r.config.DefaultProfile]
	if !ok {
		return Route{}, fmt.Errorf("%w: %s", ErrProfileNotFound, r.config.DefaultProfile)
	}
	presetName, ok := profile.Roles[role]
	if !ok || presetName == "" {
		return Route{}, fmt.Errorf("%w: %s role %s", ErrPresetNotFound, profile.Name, role)
	}
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
		Profile:       profile.Name,
		Preset:        preset,
		ContextBudget: r.config.ContextBudgets[role],
	}, nil
}

func (r *StaticRouter) legacyRoute(role AgentRole) (Route, bool) {
	if r.config.LegacyProvider == "" || r.config.LegacyModel == "" {
		return Route{}, false
	}
	return Route{
		Role:    role,
		Profile: "legacy",
		Preset: ModelPreset{
			Name:      "legacy",
			Provider:  r.config.LegacyProvider,
			Model:     r.config.LegacyModel,
			LocalOnly: true,
		},
		Legacy: true,
	}, true
}
```

- [ ] **Step 5: Run routing tests**

Run:

```bash
go test ./internal/llm/routing -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/routing
git commit -m "feat(routing): add static role router"
```

---

### Task 2: Parse Routing Config

**Files:**
- Modify: `internal/app/config/config.go`
- Modify: `internal/app/config/config_test.go`

**Interfaces:**
- Consumes: `routing.ModelPreset`, `routing.AgentProfile`, `routing.ContextBudget`, `routing.AgentRole`.
- Produces: config fields `Models.Presets`, `AgentProfiles`, `Agents`.

- [ ] **Step 1: Write failing config tests**

Add import to `internal/app/config/config_test.go`:

```go
"marshal/internal/llm/routing"
```

Add import to `internal/app/config/config.go` later in implementation:

```go
"marshal/internal/llm/routing"
```

Append tests to `internal/app/config/config_test.go`:

```go
func TestLoadParsesRoutingConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
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
repo_scout = "coder"
implementer = "coder"
reviewer = "coder"

[agents.implementer.context]
max_repo_context_tokens = 48000
max_conversation_tokens = 8000
include_raw_code = true
include_summaries = true
include_symbols = true
include_diff = false
include_tests = true
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	preset := cfg.Models.Presets["coder"]
	if preset.Provider != "ollama" || preset.Model != "qwen2.5-coder:14b" || !preset.LocalOnly {
		t.Fatalf("preset coder = %#v", preset)
	}
	if preset.ContextWindow != 32768 || preset.MaxOutputTokens != 4096 || preset.Temperature != 0.1 || preset.TopP != 1.0 {
		t.Fatalf("preset numeric fields = %#v", preset)
	}
	profile := cfg.AgentProfiles["local_balanced"]
	if profile.Roles[routing.RoleRepoScout] != "coder" || profile.Roles[routing.RoleImplementer] != "coder" {
		t.Fatalf("profile roles = %#v", profile.Roles)
	}
	budget := cfg.Agents[routing.RoleImplementer].Context
	if budget.MaxRepoContextTokens != 48000 || budget.MaxConversationTokens != 8000 || !budget.IncludeRawCode || !budget.IncludeTests {
		t.Fatalf("budget = %#v", budget)
	}
}

func TestLoadRoutingConfigProjectOverridesGlobalByKey(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", `
[models.presets.coder]
provider = "ollama"
model = "global"
local_only = true

[agent_profiles.local_balanced]
implementer = "coder"

[agents.implementer.context]
max_repo_context_tokens = 12000
`)
	writeFile(t, work+"/.marshal/config.toml", `
[models.presets.coder]
provider = "lmstudio"
model = "project"
local_only = true

[models.presets.fast]
provider = "ollama"
model = "fast"
local_only = true

[agent_profiles.local_balanced]
repo_scout = "fast"
implementer = "coder"

[agents.implementer.context]
max_repo_context_tokens = 48000
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Models.Presets["coder"].Provider != "lmstudio" || cfg.Models.Presets["coder"].Model != "project" {
		t.Fatalf("coder preset = %#v", cfg.Models.Presets["coder"])
	}
	if cfg.Models.Presets["fast"].Model != "fast" {
		t.Fatalf("fast preset missing: %#v", cfg.Models.Presets)
	}
	if cfg.AgentProfiles["local_balanced"].Roles[routing.RoleRepoScout] != "fast" {
		t.Fatalf("profile = %#v", cfg.AgentProfiles["local_balanced"])
	}
	if cfg.Agents[routing.RoleImplementer].Context.MaxRepoContextTokens != 48000 {
		t.Fatalf("implementer budget = %#v", cfg.Agents[routing.RoleImplementer].Context)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/app/config -run 'TestLoadParsesRoutingConfig|TestLoadRoutingConfigProjectOverridesGlobalByKey' -v
```

Expected: FAIL because config fields do not exist.

- [ ] **Step 3: Add config structs**

Modify `internal/app/config/config.go`.

Add import:

```go
"marshal/internal/llm/routing"
```

Add fields to `Config`:

```go
Models        ModelsConfig                           `toml:"models"`
AgentProfiles map[string]routing.AgentProfile        `toml:"agent_profiles"`
Agents        map[routing.AgentRole]AgentRoleConfig  `toml:"agents"`
```

Add types:

```go
type ModelsConfig struct {
	Presets map[string]routing.ModelPreset `toml:"presets"`
}

type AgentRoleConfig struct {
	Context routing.ContextBudget `toml:"context"`
}
```

Add fields to `configFile`:

```go
Models *struct {
	Presets map[string]routing.ModelPreset `toml:"presets"`
} `toml:"models"`
AgentProfiles map[string]struct {
	Router           string `toml:"router"`
	Knowledge        string `toml:"knowledge"`
	Summarizer       string `toml:"summarizer"`
	RepoScout        string `toml:"repo_scout"`
	Tester           string `toml:"tester"`
	Planner          string `toml:"planner"`
	Implementer      string `toml:"implementer"`
	Reviewer         string `toml:"reviewer"`
	SecurityReviewer string `toml:"security_reviewer"`
} `toml:"agent_profiles"`
Agents map[routing.AgentRole]AgentRoleConfig `toml:"agents"`
```

Initialize maps in `Default()`:

```go
Models: ModelsConfig{Presets: map[string]routing.ModelPreset{}},
AgentProfiles: map[string]routing.AgentProfile{},
Agents: map[routing.AgentRole]AgentRoleConfig{},
```

- [ ] **Step 4: Add merge logic**

Add helper:

```go
func profileFromConfig(name string, in struct {
	Router           string `toml:"router"`
	Knowledge        string `toml:"knowledge"`
	Summarizer       string `toml:"summarizer"`
	RepoScout        string `toml:"repo_scout"`
	Tester           string `toml:"tester"`
	Planner          string `toml:"planner"`
	Implementer      string `toml:"implementer"`
	Reviewer         string `toml:"reviewer"`
	SecurityReviewer string `toml:"security_reviewer"`
}) routing.AgentProfile {
	roles := map[routing.AgentRole]string{}
	if in.Router != "" {
		roles[routing.RoleRouter] = in.Router
	}
	if in.Knowledge != "" {
		roles[routing.RoleKnowledge] = in.Knowledge
	}
	if in.Summarizer != "" {
		roles[routing.RoleSummarizer] = in.Summarizer
	}
	if in.RepoScout != "" {
		roles[routing.RoleRepoScout] = in.RepoScout
	}
	if in.Tester != "" {
		roles[routing.RoleTester] = in.Tester
	}
	if in.Planner != "" {
		roles[routing.RolePlanner] = in.Planner
	}
	if in.Implementer != "" {
		roles[routing.RoleImplementer] = in.Implementer
	}
	if in.Reviewer != "" {
		roles[routing.RoleReviewer] = in.Reviewer
	}
	if in.SecurityReviewer != "" {
		roles[routing.RoleSecurityReviewer] = in.SecurityReviewer
	}
	return routing.AgentProfile{Name: name, Roles: roles}
}
```

In `merge`, before providers or after providers, add:

```go
if file.Models != nil && file.Models.Presets != nil {
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = map[string]routing.ModelPreset{}
	}
	for name, preset := range file.Models.Presets {
		preset.Name = name
		cfg.Models.Presets[name] = preset
	}
}
if file.AgentProfiles != nil {
	if cfg.AgentProfiles == nil {
		cfg.AgentProfiles = map[string]routing.AgentProfile{}
	}
	for name, profile := range file.AgentProfiles {
		cfg.AgentProfiles[name] = profileFromConfig(name, profile)
	}
}
if file.Agents != nil {
	if cfg.Agents == nil {
		cfg.Agents = map[routing.AgentRole]AgentRoleConfig{}
	}
	for role, agentCfg := range file.Agents {
		cfg.Agents[role] = agentCfg
	}
}
```

- [ ] **Step 5: Run config tests**

Run:

```bash
go test ./internal/app/config -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/config/config.go internal/app/config/config_test.go
git commit -m "feat(config): parse model routing config"
```

---

### Task 3: Store And Display Active Route Metadata

**Files:**
- Modify: `internal/app/session/session.go`
- Modify: `internal/app/session/session_test.go`
- Modify: `internal/app/tui/model.go`
- Modify: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: `routing.Route`.
- Produces: `session.RouteInfo`, `State.SetActiveRoute`, `State.ActiveRoute`.

- [ ] **Step 1: Write failing session tests**

Add import to `internal/app/session/session_test.go`:

```go
"marshal/internal/llm/routing"
```

Append:

```go
func TestStateActiveRouteStoresCopies(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	route := RouteInfo{
		Role:      routing.RoleImplementer,
		Profile:   "local_balanced",
		Preset:    "coder",
		Provider:  "ollama",
		Model:     "qwen2.5-coder:14b",
		LocalOnly: true,
		Active:    true,
	}

	state.SetActiveRoute(route)
	route.Model = "mutated"

	got := state.ActiveRoute()
	if got.Model != "qwen2.5-coder:14b" || !got.Active {
		t.Fatalf("ActiveRoute() = %#v", got)
	}
}
```

- [ ] **Step 2: Write failing TUI tests**

Add import to `internal/app/tui/model_test.go`:

```go
"marshal/internal/llm/routing"
```

Append:

```go
func TestViewShowsInactiveRouteByDefault(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	view := model.View()
	if !strings.Contains(view, "Route: inactive") {
		t.Fatalf("View() missing inactive route:\n%s", view)
	}
}

func TestViewShowsActiveRoute(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetActiveRoute(session.RouteInfo{
		Role:      routing.RoleImplementer,
		Profile:   "local_balanced",
		Preset:    "coder",
		Provider:  "ollama",
		Model:     "qwen2.5-coder:14b",
		LocalOnly: true,
		Active:    true,
	})
	model := New(state)

	view := model.View()
	for _, want := range []string{
		"Route: role=implementer",
		"profile=local_balanced",
		"preset=coder",
		"provider=ollama",
		"model=qwen2.5-coder:14b",
		"local-only=true",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/app/session -run TestStateActiveRouteStoresCopies -v
go test ./internal/app/tui -run 'TestViewShowsInactiveRouteByDefault|TestViewShowsActiveRoute' -v
```

Expected: FAIL because route metadata APIs and display do not exist.

- [ ] **Step 4: Add session route state**

Modify `internal/app/session/session.go`.

Add import:

```go
"marshal/internal/llm/routing"
```

Add type:

```go
type RouteInfo struct {
	Role      routing.AgentRole
	Profile   string
	Preset    string
	Provider  string
	Model     string
	LocalOnly bool
	Legacy    bool
	Active    bool
}
```

Add field to `State`:

```go
activeRoute RouteInfo
```

Add methods:

```go
func (s *State) SetActiveRoute(route RouteInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeRoute = route
}

func (s *State) ActiveRoute() RouteInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeRoute
}
```

- [ ] **Step 5: Add TUI display**

Modify status section in `internal/app/tui/model.go`:

```go
	fmt.Fprintf(&b, "Status: project=%s cwd=%s local-only=%t\n",
		m.state.Config.Project.Name,
		m.state.WorkingDir,
		!m.state.Config.Privacy.RemoteProvidersAllowed,
	)
	route := m.state.ActiveRoute()
	if route.Active {
		fmt.Fprintf(&b, "Route: role=%s profile=%s preset=%s provider=%s model=%s local-only=%t\n\n",
			route.Role, route.Profile, route.Preset, route.Provider, route.Model, route.LocalOnly,
		)
	} else {
		fmt.Fprintf(&b, "Route: inactive\n\n")
	}
```

- [ ] **Step 6: Run session and TUI tests**

Run:

```bash
go test ./internal/app/session -v
go test ./internal/app/tui -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/session/session.go internal/app/session/session_test.go internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): show active model route"
```

---

### Task 4: Wire Routing Into Agent Runner

**Files:**
- Modify: `internal/contextpack/builder.go`
- Modify: `internal/contextpack/contextpack_test.go`
- Modify: `internal/agent/runner.go`
- Modify: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `routing.Route`, `routing.TaskProfile`, `session.RouteInfo`, `contextpack.RefreshPlan`, and `provider.Provider`.
- Produces: `contextpack.Rebudget`, `contextpack.RefreshPlanWithBudget`, runner route+provider resolution per turn, and role budget application.

- [ ] **Step 1: Write failing context-pack budget helper tests**

Append to `internal/contextpack/contextpack_test.go`:

```go
func TestRebudgetPreservesExistingPlanAndAppliesMaxTokens(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: SectionPlan, Title: "Current Plan", Content: "1. Keep this plan", EstimatedTokens: 5},
			{Kind: SectionFileSnippet, Title: "internal/app/app.go", Source: "internal/app/app.go:1-3", Content: "package app", EstimatedTokens: 3},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 12},
	}

	updated := Rebudget(pack, 24000, func() time.Time { return time.Unix(300, 0).UTC() })

	if updated.TokenUsage.MaxTokens != 24000 {
		t.Fatalf("MaxTokens = %d, want 24000", updated.TokenUsage.MaxTokens)
	}
	if len(updated.Sections) != 3 || updated.Sections[1].Kind != SectionPlan {
		t.Fatalf("sections = %#v, want plan preserved", updated.Sections)
	}
	if updated.Sections[1].Content != "1. Keep this plan" {
		t.Fatalf("plan content = %q", updated.Sections[1].Content)
	}
	if updated.Sections[2].Source != "internal/app/app.go:1-3" {
		t.Fatalf("snippet source = %q", updated.Sections[2].Source)
	}
}

func TestRefreshPlanWithBudgetUsesProvidedMaxTokens(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	}

	updated := RefreshPlanWithBudget(pack, []string{"1. Inspect"}, 24000, func() time.Time { return time.Unix(300, 0).UTC() })

	if updated.TokenUsage.MaxTokens != 24000 {
		t.Fatalf("MaxTokens = %d, want 24000", updated.TokenUsage.MaxTokens)
	}
	if len(updated.Sections) != 2 || updated.Sections[1].Kind != SectionPlan {
		t.Fatalf("sections = %#v, want repo card then plan", updated.Sections)
	}
}
```

- [ ] **Step 2: Run context-pack tests to verify they fail**

Run:

```bash
go test ./internal/contextpack -run 'TestRebudget|TestRefreshPlanWithBudget' -v
```

Expected: FAIL because `Rebudget` and `RefreshPlanWithBudget` are undefined.

- [ ] **Step 3: Add context-pack budget helpers**

Modify `internal/contextpack/builder.go`.

Change `RefreshPlan` to delegate:

```go
func RefreshPlan(pack Pack, plan []string, now func() time.Time) Pack {
	maxTokens := pack.TokenUsage.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	return RefreshPlanWithBudget(pack, plan, maxTokens, now)
}
```

Add:

```go
func RefreshPlanWithBudget(pack Pack, plan []string, maxTokens int, now func() time.Time) Pack {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	generatedAt := pack.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	if now != nil {
		generatedAt = now().UTC()
	}

	planSection, hasPlan := newPlanSection(plan)
	sections := make([]Section, 0, len(pack.Sections)+1)
	insertedPlan := false
	for _, section := range pack.Sections {
		if section.Kind == SectionPlan {
			continue
		}
		if hasPlan && !insertedPlan && (section.Kind == SectionFileSnippet || section.Kind == SectionToolOutput) {
			sections = append(sections, planSection)
			insertedPlan = true
		}
		sections = append(sections, section)
	}
	if hasPlan && !insertedPlan {
		sections = append(sections, planSection)
	}

	return buildPackFromSections(sections, maxTokens, generatedAt)
}

func Rebudget(pack Pack, maxTokens int, now func() time.Time) Pack {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	generatedAt := pack.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	if now != nil {
		generatedAt = now().UTC()
	}
	return buildPackFromSections(pack.Clone().Sections, maxTokens, generatedAt)
}
```

- [ ] **Step 4: Run context-pack tests**

Run:

```bash
go test ./internal/contextpack -v
```

Expected: PASS.

- [ ] **Step 5: Write failing runner tests**

Add imports to `internal/agent/runner_test.go`:

```go
"marshal/internal/llm/provider"
"marshal/internal/llm/routing"
```

Append tests:

```go
type scriptedRouteResolver struct {
	routes    []routing.Route
	providers []provider.Provider
	tasks     []routing.TaskProfile
}

func (r *scriptedRouteResolver) Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error) {
	r.tasks = append(r.tasks, task)
	if len(r.routes) == 0 {
		return routing.Route{}, nil, routing.ErrNoRoute
	}
	route := r.routes[0]
	r.routes = r.routes[1:]
	var p provider.Provider
	if len(r.providers) > 0 {
		p = r.providers[0]
		r.providers = r.providers[1:]
	}
	return route, p, nil
}

func TestRunResolvesQuestionRouteAndUpdatesModel(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"ok"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	resolver := &scriptedRouteResolver{
		routes: []routing.Route{{
		Role:    routing.RoleRepoScout,
		Profile: "local_balanced",
		Preset: routing.ModelPreset{Name: "fast", Provider: "ollama", Model: "fast-model", LocalOnly: true},
		}},
		providers: []provider.Provider{p},
	}
	runner := NewRunner(p, reg, pol, state, "fallback-model")
	runner.RouteResolver = resolver

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(resolver.tasks) != 1 || resolver.tasks[0].Class != "question" {
		t.Fatalf("resolved tasks = %#v", resolver.tasks)
	}
	if p.requests[0].Model != "fast-model" {
		t.Fatalf("request model = %q, want fast-model", p.requests[0].Model)
	}
	route := state.ActiveRoute()
	if route.Role != routing.RoleRepoScout || route.Model != "fast-model" || !route.Active {
		t.Fatalf("ActiveRoute = %#v", route)
	}
}

func TestRunAppliesRouteContextBudgetToExistingPack(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		"1. Inspect.\n2. Edit.",
		`{"rationale":"done","action":{"type":"final","content":"ok"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4}},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	resolver := &scriptedRouteResolver{
		routes: []routing.Route{{
		Role: routing.RoleImplementer,
		Preset: routing.ModelPreset{Name: "coder", Provider: "ollama", Model: "coder-model", LocalOnly: true},
		ContextBudget: routing.ContextBudget{MaxRepoContextTokens: 24000},
		}},
		providers: []provider.Provider{p},
	}
	runner := NewRunner(p, reg, pol, state, "fallback-model")
	runner.RouteResolver = resolver

	if err := runner.Run(context.Background(), "Add a test"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	pack := state.ContextPack()
	if pack.TokenUsage.MaxTokens != 24000 {
		t.Fatalf("pack max tokens = %d, want 24000", pack.TokenUsage.MaxTokens)
	}
}
```

Ensure `scriptedProvider` already records requests from Milestone K. If not, add `requests []schema.ChatRequest` and append in `Chat`.

- [ ] **Step 6: Run runner tests to verify they fail**

Run:

```bash
go test ./internal/agent -run 'TestRunResolvesQuestionRouteAndUpdatesModel|TestRunAppliesRouteContextBudgetToExistingPack' -v
```

Expected: FAIL because runner has no route resolver.

- [ ] **Step 7: Add runner route resolver**

Modify `internal/agent/runner.go`.

Add import:

```go
"marshal/internal/llm/routing"
```

Add interface:

```go
type RouteResolver interface {
	Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error)
}
```

Add field to `Runner`:

```go
RouteResolver RouteResolver
```

Add helper:

```go
func (r *Runner) resolveRoute(task *Task) routing.Route {
	if r.RouteResolver == nil {
		return routing.Route{}
	}
	route, resolvedProvider, err := r.RouteResolver.Resolve(routing.TaskProfile{Class: string(task.Class)})
	if err != nil {
		r.State.SetProviderError(err)
		return routing.Route{}
	}
	if resolvedProvider != nil {
		r.Provider = resolvedProvider
	}
	r.Model = route.Preset.Model
	r.State.SetActiveRoute(session.RouteInfo{
		Role:      route.Role,
		Profile:   route.Profile,
		Preset:    route.Preset.Name,
		Provider:  route.Preset.Provider,
		Model:     route.Preset.Model,
		LocalOnly: route.Preset.LocalOnly,
		Legacy:    route.Legacy,
		Active:    true,
	})
	if route.ContextBudget.MaxRepoContextTokens > 0 {
		pack := r.State.ContextPack()
		if !pack.IsEmpty() {
			pack = contextpack.Rebudget(pack, route.ContextBudget.MaxRepoContextTokens, r.Now)
			r.State.SetContextPack(pack)
		}
	}
	return route
}
```

Call after classification:

```go
route := r.resolveRoute(task)
_ = route
```

When refreshing plan after planning, use route budget:

```go
maxTokens := current.TokenUsage.MaxTokens
if route.ContextBudget.MaxRepoContextTokens > 0 {
	maxTokens = route.ContextBudget.MaxRepoContextTokens
}
updatedPack := contextpack.RefreshPlanWithBudget(current, task.Plan, maxTokens, r.Now)
```

- [ ] **Step 8: Run agent tests**

Run:

```bash
go test ./internal/contextpack -v
go test ./internal/agent -v
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/contextpack/builder.go internal/contextpack/contextpack_test.go internal/agent/runner.go internal/agent/runner_test.go
git commit -m "feat(agent): resolve model route per turn"
```

---

### Task 5: Wire Router And Provider Construction In App

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Consumes: `routing.Config`, `routing.StaticRouter`, `provider.NewFromConfig`, `native.RegisterAll`, `agent.NewRunner`, and `agent.RouteResolver`.
- Produces: app-level routed runner construction, per-turn provider/model switching, initial active route metadata, and graceful disabled fallback.

- [ ] **Step 1: Write failing app wiring tests**

Add import to `internal/app/app_test.go`:

```go
"strings"
```

Append:

```go
func TestRunDisplaysInactiveRouteWhenNoProviderConfigured(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	var view string
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			view = model.View()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(view, "Route: inactive") {
		t.Fatalf("view missing inactive route:\n%s", view)
	}
}

func TestRunDisplaysActiveLegacyRouteWhenAgentConfigured(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	cfg := config.Default()
	cfg.Agent.Provider = "ollama"
	cfg.Agent.Model = "qwen2.5-coder:14b"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "local"},
	}

	var view string
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return cfg, nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			view = model.View()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, want := range []string{
		"Route: role=implementer",
		"profile=legacy",
		"preset=legacy",
		"provider=ollama",
		"model=qwen2.5-coder:14b",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails or exposes current behavior**

Run:

```bash
go test ./internal/app -run 'TestRunDisplaysInactiveRouteWhenNoProviderConfigured|TestRunDisplaysActiveLegacyRouteWhenAgentConfigured' -v
```

Expected: the inactive test may pass after Task 3, and the active route test fails until app routing is wired.

- [ ] **Step 3: Add app routing helper**

Modify `internal/app/app.go` imports:

```go
"marshal/internal/agent"
"marshal/internal/llm/provider"
"marshal/internal/llm/routing"
"marshal/internal/tools/native"
"marshal/internal/tools/policy"
"marshal/internal/tools/registry"
```

Add helper:

```go
func routingConfigFromAppConfig(cfg config.Config) routing.Config {
	budgets := map[routing.AgentRole]routing.ContextBudget{}
	for role, agentCfg := range cfg.Agents {
		budgets[role] = agentCfg.Context
	}
	return routing.Config{
		DefaultProfile: cfg.Profile.Default,
		RemoteAllowed:  cfg.Privacy.RemoteProvidersAllowed,
		Presets:        cfg.Models.Presets,
		Profiles:       cfg.AgentProfiles,
		ContextBudgets: budgets,
		LegacyProvider: cfg.Agent.Provider,
		LegacyModel:    cfg.Agent.Model,
	}
}
```

Add type:

```go
type routedProviderResolver struct {
	router    *routing.StaticRouter
	cfg       config.Config
	providers map[string]provider.Provider
}

func newRoutedProviderResolver(cfg config.Config) *routedProviderResolver {
	return &routedProviderResolver{
		router:    routing.NewStaticRouter(routingConfigFromAppConfig(cfg)),
		cfg:       cfg,
		providers: map[string]provider.Provider{},
	}
}

func (r *routedProviderResolver) Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error) {
	route, err := r.router.Resolve(task)
	if err != nil {
		return routing.Route{}, nil, err
	}
	p, ok := r.providers[route.Preset.Provider]
	if ok {
		return route, p, nil
	}
	pc, ok := r.cfg.Providers[route.Preset.Provider]
	if !ok {
		return routing.Route{}, nil, fmt.Errorf("routing provider %q is not configured", route.Preset.Provider)
	}
	p, err = provider.NewFromConfig(route.Preset.Provider, pc)
	if err != nil {
		return routing.Route{}, nil, err
	}
	r.providers[route.Preset.Provider] = p
	return route, p, nil
}
```

Add helper:

```go
func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, logger *slog.Logger) (*agent.Runner, error) {
	resolver := newRoutedProviderResolver(cfg)
	route, p, err := resolver.Resolve(routing.TaskProfile{Class: "edit"})
	if err != nil {
		return nil, err
	}
	reg := registry.New()
	if err := native.RegisterAll(reg, native.Options{
		WorkspaceRoot: state.WorkingDir,
		TestCommand:   cfg.Commands.Test,
		SessionState:  state,
		DB:            database,
		ProjectID:     projectID,
	}); err != nil {
		return nil, err
	}
	pol := policy.NewEngine(&cfg, logger)
	runner := agent.NewRunner(p, reg, pol, state, route.Preset.Model)
	runner.RouteResolver = resolver
	state.SetActiveRoute(session.RouteInfo{
		Role:      route.Role,
		Profile:   route.Profile,
		Preset:    route.Preset.Name,
		Provider:  route.Preset.Provider,
		Model:     route.Preset.Model,
		LocalOnly: route.Preset.LocalOnly,
		Legacy:    route.Legacy,
		Active:    true,
	})
	return runner, nil
}
```

After `state := session.New(...)`, add:

```go
	var tuiOpts []tui.Option
	if runner, err := buildAgentRunner(ctx, cfg, state, database, projectID, logger); err == nil {
		tuiOpts = append(tuiOpts, tui.WithRunner(ctx, runner))
	} else {
		state.SetProviderError(err)
	}
```

Replace final TUI construction:

```go
return runOpts.programRunner(ctx, tui.New(state, tuiOpts...), stdout)
```

Do not return route/provider errors from `Run`; keep TUI usable.

- [ ] **Step 4: Run app tests**

Run:

```bash
go test ./internal/app -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): wire static model router"
```

---

### Task 6: Final Docs, Checklist, And Verification

**Files:**
- Modify: `docs/09-configuration-examples.md`
- Modify: `docs/10-mvp-implementation-checklist.md`
- Create: `docs/plans/2026-07-02-milestone-l-role-based-model-routing.md`

**Interfaces:**
- Consumes: all prior tasks.
- Produces: completed Milestone L checklist and task status table.

- [ ] **Step 1: Run full tests before docs completion**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Confirm config examples match implementation**

Review `docs/09-configuration-examples.md`. If the implemented TOML keys match existing examples, make no change. If they drifted, update the examples to match actual config field names.

- [ ] **Step 3: Update MVP checklist**

In `docs/10-mvp-implementation-checklist.md`, change Milestone L to:

```markdown
## Milestone L: Role-based model routing v1

- [x] Define `AgentRole`
- [x] Define `ModelPreset`
- [x] Define `AgentProfile`
- [x] Implement static router
- [x] Show active model in TUI
- [x] Add local-only flag
- [x] Add role-specific context budget
```

- [ ] **Step 4: Add task status doc**

Create `docs/plans/2026-07-02-milestone-l-role-based-model-routing.md`:

```markdown
| Task | Status | Details |
| --- | --- | --- |
| Task 1: Add Static Routing Core | completed | Added routing types, roles, static resolver, local-only policy, legacy fallback, and unit tests |
| Task 2: Parse Routing Config | completed | Added model preset, agent profile, and role context budget config parsing and merge tests |
| Task 3: Store And Display Active Route Metadata | completed | Added session route metadata and TUI route display |
| Task 4: Wire Routing Into Agent Runner | completed | Added per-turn route resolution, model selection, and context budget application |
| Task 5: Wire Router And Provider Construction In App | completed | Built routed runner from app config while preserving disabled fallback |
| Task 6: Final Docs, Checklist, And Verification | completed | Verified tests and marked Milestone L complete |
```

- [ ] **Step 5: Run full tests again**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Check status**

Run:

```bash
git status --short
```

Expected: only docs files from this task are modified or untracked.

- [ ] **Step 7: Commit**

```bash
git add docs/09-configuration-examples.md docs/10-mvp-implementation-checklist.md docs/plans/2026-07-02-milestone-l-role-based-model-routing.md
git commit -m "docs: mark Milestone L routing complete"
```

- [ ] **Step 8: Final verification**

Run:

```bash
go test ./...
git status --short
```

Expected: tests pass and only pre-existing untracked files remain.
