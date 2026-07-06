# Phase 5 Swarm Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the gap between the working Milestone-O swarm engine and full Phase 5 by adding a test-fix feedback loop (tester role), a swarm roster panel, and run-level budgets.

**Architecture:** The swarm `Orchestrator` (`internal/agent/swarm/orchestrator.go`) gains an implementer↔tester loop bounded by `max_fix_rounds`, a pluggable `TokenMeter`, and per-role tool caps. Live run state is published to `session.State` via a new `SwarmProgress` value (mirroring the existing `Activity` pattern) and rendered by a new TUI panel inserted above the input in the existing single-column view stack. Configuration lands under a new `[swarm]` TOML section.

**Tech Stack:** Go, Bubble Tea / lipgloss (TUI), go-toml/v2 (config), standard `testing`.

## Global Constraints

- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter dependency). Build with `go build ./cmd/marshal`.
- Format with `gofmt -w .` and vet with `go vet ./...` before every commit.
- `internal/agent` must not import `internal/knowledge` (existing rule); this plan adds no new cross-package imports beyond `swarm → contextpack` (already permitted transitively).
- The single-agent loop's external behavior must not change.
- Role string values are fixed and shared between `agent.AgentRole` and `routing.AgentRole`: `"planner"`, `"repo_scout"`, `"implementer"`, `"tester"`, `"reviewer"`.
- Read-only enforcement is structural (registry views), never prompt-only.

---

## File Structure

- Create `internal/agent/swarm/verdict.go` — parse the tester's `VERDICT: PASS/FAIL` line.
- Create `internal/agent/swarm/meter.go` — `TokenMeter` interface, `EstimateMeter`, `ProviderUsageMeter` stub.
- Create `internal/app/session/swarm_progress.go` — `SwarmProgress` type + `State` accessors.
- Create `internal/app/tui/swarm_panel.go` — roster panel renderer.
- Modify `internal/tools/registry/view.go` — add `TesterView`.
- Modify `internal/agent/swarm/prompts.go` — add `testerPrompt`, extend `implementerPrompt`.
- Modify `internal/agent/swarm/orchestrator.go` — `RegistryScope`, the loop, budgets, progress.
- Modify `internal/app/session/session.go` — add `swarmProgress` field to `State`.
- Modify `internal/app/config/config.go` — `SwarmConfig` struct, `Default()`, merge.
- Modify `internal/app/app.go` — construct orchestrator with budgets/meter, per-role caps, tester scope.
- Modify `internal/app/tui/view.go` and `internal/app/tui/model.go` — insert panel, reserve height, recompute on tick.

---

## Task 1: TesterView registry view

**Files:**
- Modify: `internal/tools/registry/view.go`
- Test: `internal/tools/registry/view_test.go`

**Interfaces:**
- Consumes: `Registry.List()`, `Registry.Register(Tool)`, `RiskReadOnly`, `RiskCommand` (existing).
- Produces: `func TesterView(src *Registry) *Registry` — a registry with only read-only and command-risk tools (read + test/shell execution, no source writes).

- [ ] **Step 1: Write the failing test**

Add to `internal/tools/registry/view_test.go`:

```go
func TestTesterViewAllowsReadAndCommandButNotWrites(t *testing.T) {
	src := New()
	mustRegister(src, Tool{Name: "file.read", Description: "read", Risk: RiskReadOnly, Handler: nopHandler})
	mustRegister(src, Tool{Name: "test.run", Description: "test", Risk: RiskCommand, Handler: nopHandler})
	mustRegister(src, Tool{Name: "patch.apply", Description: "patch", Risk: RiskWorkspaceWrite, Handler: nopHandler})
	mustRegister(src, Tool{Name: "fetch", Description: "net", Risk: RiskNetwork, Handler: nopHandler})

	view := TesterView(src)

	if _, ok := view.Lookup("file.read"); !ok {
		t.Error("TesterView should include read-only tools")
	}
	if _, ok := view.Lookup("test.run"); !ok {
		t.Error("TesterView should include command tools")
	}
	if _, ok := view.Lookup("patch.apply"); ok {
		t.Error("TesterView must exclude workspace-write tools")
	}
	if _, ok := view.Lookup("fetch"); ok {
		t.Error("TesterView must exclude network tools")
	}
}
```

Note: check the existing `view_test.go` helper name for registering tools (the existing `TestReadOnlyViewFiltersOutWriteTools` uses `mustRegister`). If its signature differs, match it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/registry/ -run TestTesterView -v`
Expected: FAIL with "undefined: TesterView".

- [ ] **Step 3: Write minimal implementation**

Append to `internal/tools/registry/view.go`:

```go
// TesterView returns a new Registry containing src's read-only and
// command-risk tools: the swarm tester needs to run tests and shell
// commands but must not modify source (docs/07 swarm safety). Write and
// network tools are filtered out so "does not modify source" is enforced
// structurally, not by prompt instruction.
func TesterView(src *Registry) *Registry {
	view := New()
	for _, tool := range src.List() {
		if tool.Risk == RiskReadOnly || tool.Risk == RiskCommand {
			_ = view.Register(tool)
		}
	}
	return view
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/registry/ -run TestTesterView -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tools/registry/
git add internal/tools/registry/view.go internal/tools/registry/view_test.go
git commit -m "feat(registry): add TesterView (read-only + command tools)"
```

---

## Task 2: Tester verdict parser

**Files:**
- Create: `internal/agent/swarm/verdict.go`
- Test: `internal/agent/swarm/verdict_test.go`

**Interfaces:**
- Produces: `func ParseVerdict(summary string) (pass bool, ok bool)` — `ok` is false when no `VERDICT:` line is present. `pass` is true only for an explicit `VERDICT: PASS`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/swarm/verdict_test.go`:

```go
package swarm

import "testing"

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantPass   bool
		wantOK     bool
	}{
		{"pass", "Ran go test.\nVERDICT: PASS", true, true},
		{"fail", "TestFoo failed at bar.go:42\nVERDICT: FAIL", false, true},
		{"lowercase", "verdict: pass", true, true},
		{"trailing spaces", "VERDICT:  PASS  ", true, true},
		{"no verdict", "tests look fine to me", false, false},
		{"garbage verdict", "VERDICT: MAYBE", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass, ok := ParseVerdict(tc.in)
			if pass != tc.wantPass || ok != tc.wantOK {
				t.Fatalf("ParseVerdict(%q) = (%v,%v), want (%v,%v)", tc.in, pass, ok, tc.wantPass, tc.wantOK)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/swarm/ -run TestParseVerdict -v`
Expected: FAIL with "undefined: ParseVerdict".

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/swarm/verdict.go`:

```go
package swarm

import "strings"

// ParseVerdict extracts the tester's PASS/FAIL verdict from its final
// answer. The tester is instructed to end with a "VERDICT: PASS" or
// "VERDICT: FAIL" line (mirroring the reviewer's APPROVE convention).
// ok is false when no recognisable verdict line is present, so the
// orchestrator can treat an ambiguous tester as "stop, do not loop".
func ParseVerdict(summary string) (pass bool, ok bool) {
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "verdict:") {
			continue
		}
		value := strings.TrimSpace(lower[len("verdict:"):])
		switch value {
		case "pass":
			return true, true
		case "fail":
			return false, true
		default:
			return false, false
		}
	}
	return false, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/swarm/ -run TestParseVerdict -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/swarm/
git add internal/agent/swarm/verdict.go internal/agent/swarm/verdict_test.go
git commit -m "feat(swarm): add tester VERDICT parser"
```

---

## Task 3: Token meter (estimate-based + provider stub)

**Files:**
- Create: `internal/agent/swarm/meter.go`
- Test: `internal/agent/swarm/meter_test.go`

**Interfaces:**
- Consumes: `agent.AgentRole` (type alias for string role), `contextpack.EstimateTokens(string) int`.
- Produces:
  - `type TokenMeter interface { Observe(role agent.AgentRole, promptTokens, completionTokens int); Total() int }`
  - `func NewEstimateMeter() *EstimateMeter`
  - `func (m *EstimateMeter) Observe(role agent.AgentRole, promptTokens, completionTokens int)`
  - `func (m *EstimateMeter) Total() int`
  - `func NewProviderUsageMeter() *ProviderUsageMeter` (dormant stub; delegates to an embedded EstimateMeter)
  - `func EstimateText(s string) int` (thin wrapper over `contextpack.EstimateTokens`, so the orchestrator can measure prompt/answer text without importing contextpack directly)

- [ ] **Step 1: Write the failing test**

Create `internal/agent/swarm/meter_test.go`:

```go
package swarm

import (
	"marshal/internal/agent"
	"testing"
)

func TestEstimateMeterAccumulates(t *testing.T) {
	m := NewEstimateMeter()
	if m.Total() != 0 {
		t.Fatalf("new meter Total = %d, want 0", m.Total())
	}
	m.Observe(agent.RolePlanner, 100, 50)
	m.Observe(agent.RoleImplementer, 200, 80)
	if got := m.Total(); got != 430 {
		t.Fatalf("Total = %d, want 430", got)
	}
}

func TestProviderUsageMeterIsDormantButUsable(t *testing.T) {
	var m TokenMeter = NewProviderUsageMeter()
	m.Observe(agent.RoleTester, 10, 5)
	if m.Total() != 15 {
		t.Fatalf("stub meter Total = %d, want 15 (delegates to estimate)", m.Total())
	}
}

func TestEstimateTextIsNonNegative(t *testing.T) {
	if EstimateText("") != 0 {
		t.Errorf("EstimateText(\"\") should be 0")
	}
	if EstimateText("some tokens here") <= 0 {
		t.Errorf("EstimateText of non-empty string should be > 0")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/swarm/ -run 'Meter|EstimateText' -v`
Expected: FAIL with "undefined: NewEstimateMeter".

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/swarm/meter.go`:

```go
package swarm

import (
	"sync"

	"marshal/internal/agent"
	"marshal/internal/contextpack"
)

// TokenMeter accumulates token consumption across a swarm run so the
// orchestrator can enforce a whole-run token ceiling. Observe is called
// once per role turn. Implementations must be safe for concurrent use:
// parallel scouts observe from separate goroutines.
type TokenMeter interface {
	Observe(role agent.AgentRole, promptTokens, completionTokens int)
	Total() int
}

// EstimateText estimates the token count of s using the same heuristic the
// context-pack builder uses, so budget accounting is consistent with the
// rest of Marshal.
func EstimateText(s string) int {
	return contextpack.EstimateTokens(s)
}

// EstimateMeter is the active default: it sums the prompt and completion
// token counts it is given (themselves derived from EstimateText). It is
// approximate but self-contained and identical across all local providers.
type EstimateMeter struct {
	mu    sync.Mutex
	total int
}

func NewEstimateMeter() *EstimateMeter { return &EstimateMeter{} }

func (m *EstimateMeter) Observe(_ agent.AgentRole, promptTokens, completionTokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total += promptTokens + completionTokens
}

func (m *EstimateMeter) Total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}

// ProviderUsageMeter is the seam for real provider `usage` token counts in
// a later milestone. It is wired but dormant: until the provider layer
// surfaces real usage, it delegates to estimation so behaviour is
// unchanged. Do NOT add provider-usage parsing in this cycle.
type ProviderUsageMeter struct {
	fallback *EstimateMeter
}

func NewProviderUsageMeter() *ProviderUsageMeter {
	return &ProviderUsageMeter{fallback: NewEstimateMeter()}
}

func (m *ProviderUsageMeter) Observe(role agent.AgentRole, promptTokens, completionTokens int) {
	m.fallback.Observe(role, promptTokens, completionTokens)
}

func (m *ProviderUsageMeter) Total() int { return m.fallback.Total() }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/swarm/ -run 'Meter|EstimateText' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/swarm/
git add internal/agent/swarm/meter.go internal/agent/swarm/meter_test.go
git commit -m "feat(swarm): add TokenMeter with estimate + dormant provider stub"
```

---

## Task 4: `[swarm]` config section

**Files:**
- Modify: `internal/app/config/config.go`
- Test: `internal/app/config/config_test.go`

**Interfaces:**
- Produces:
  - `type SwarmConfig struct { Budget SwarmBudgetConfig }` with TOML tag `swarm`.
  - `type SwarmBudgetConfig struct { MaxFixRounds int; MaxTotalTokens int; ToolIters map[string]int }`.
  - `Config.Swarm SwarmConfig` field.
  - Defaults: `MaxFixRounds = 3`, `MaxTotalTokens = 120000`, `ToolIters = {}`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/config/config_test.go`:

```go
func TestSwarmBudgetDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Swarm.Budget.MaxFixRounds != 3 {
		t.Errorf("MaxFixRounds default = %d, want 3", cfg.Swarm.Budget.MaxFixRounds)
	}
	if cfg.Swarm.Budget.MaxTotalTokens != 120000 {
		t.Errorf("MaxTotalTokens default = %d, want 120000", cfg.Swarm.Budget.MaxTotalTokens)
	}
}

func TestSwarmBudgetMergesFromFile(t *testing.T) {
	dir := t.TempDir()
	projectToml := `
[swarm.budget]
max_fix_rounds = 5
max_total_tokens = 90000
[swarm.budget.tool_iters]
implementer = 25
tester = 4
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(projectToml), 0o644); err != nil {
		t.Fatal(err)
	}
	// Match the loader helper used by existing tests in this file. Existing
	// tests write .marshal/config.toml under a working dir and call Load with
	// LoadOptions{WorkingDir: ...}. Mirror that exact setup here.
	// (See the existing project-config merge test in this file for the pattern.)
	cfg := loadProjectConfigForTest(t, dir, projectToml) // replace with the file's actual helper

	if cfg.Swarm.Budget.MaxFixRounds != 5 {
		t.Errorf("MaxFixRounds = %d, want 5", cfg.Swarm.Budget.MaxFixRounds)
	}
	if cfg.Swarm.Budget.MaxTotalTokens != 90000 {
		t.Errorf("MaxTotalTokens = %d, want 90000", cfg.Swarm.Budget.MaxTotalTokens)
	}
	if cfg.Swarm.Budget.ToolIters["implementer"] != 25 {
		t.Errorf("ToolIters[implementer] = %d, want 25", cfg.Swarm.Budget.ToolIters["implementer"])
	}
}
```

Note: this file already has project-config merge tests. Before writing, read `internal/app/config/config_test.go` and reuse its existing load helper and directory layout (`.marshal/config.toml` under a working dir) instead of the `loadProjectConfigForTest` placeholder above.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/config/ -run TestSwarmBudget -v`
Expected: FAIL (compile error: `cfg.Swarm` undefined).

- [ ] **Step 3: Write minimal implementation**

In `internal/app/config/config.go`:

Add the field to `Config` (after the `Tools` field):

```go
	Tools         ToolsConfig                           `toml:"tools"`
	Swarm         SwarmConfig                           `toml:"swarm"`
}
```

Add the types (near `AgentConfig`):

```go
// SwarmConfig holds run-level swarm settings. Budget bounds the multi-role
// swarm run: the implementer↔tester loop, per-role tool caps, and a
// whole-run token ceiling.
type SwarmConfig struct {
	Budget SwarmBudgetConfig `toml:"budget"`
}

type SwarmBudgetConfig struct {
	MaxFixRounds   int            `toml:"max_fix_rounds"`
	MaxTotalTokens int            `toml:"max_total_tokens"`
	ToolIters      map[string]int `toml:"tool_iters"` // per-role tool-iteration cap keyed by role string
}
```

In `Default()`, add to the returned `Config` literal (after `Tools: ...`):

```go
		Swarm: SwarmConfig{
			Budget: SwarmBudgetConfig{
				MaxFixRounds:   3,
				MaxTotalTokens: 120000,
				ToolIters:      map[string]int{},
			},
		},
```

Add the file-shape struct to `configFile` (after the `Agent` block):

```go
		Swarm *struct {
			Budget *struct {
				MaxFixRounds   *int           `toml:"max_fix_rounds"`
				MaxTotalTokens *int           `toml:"max_total_tokens"`
				ToolIters      map[string]int `toml:"tool_iters"`
			} `toml:"budget"`
		} `toml:"swarm"`
```

Add the merge block in `Load` (after the `file.Agent` merge block, before `file.Privacy`):

```go
	if file.Swarm != nil && file.Swarm.Budget != nil {
		b := file.Swarm.Budget
		if b.MaxFixRounds != nil {
			cfg.Swarm.Budget.MaxFixRounds = *b.MaxFixRounds
		}
		if b.MaxTotalTokens != nil {
			cfg.Swarm.Budget.MaxTotalTokens = *b.MaxTotalTokens
		}
		for role, iters := range b.ToolIters {
			if cfg.Swarm.Budget.ToolIters == nil {
				cfg.Swarm.Budget.ToolIters = map[string]int{}
			}
			cfg.Swarm.Budget.ToolIters[role] = iters
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/config/ -run TestSwarmBudget -v`
Expected: PASS. Then `go test ./internal/app/config/...` to confirm no regressions.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/config/
git add internal/app/config/config.go internal/app/config/config_test.go
git commit -m "feat(config): add [swarm.budget] section"
```

---

## Task 5: `session.SwarmProgress` state

**Files:**
- Create: `internal/app/session/swarm_progress.go`
- Modify: `internal/app/session/session.go` (add one field to `State`)
- Test: `internal/app/session/swarm_progress_test.go`

**Interfaces:**
- Produces:
  - `type SwarmRoleStatus string` with `SwarmRolePending`, `SwarmRoleActive`, `SwarmRoleDone`, `SwarmRoleFailed`.
  - `type SwarmRole struct { Name string; Status SwarmRoleStatus; Detail string }`.
  - `type SwarmProgress struct { Goal string; Active bool; Roles []SwarmRole }`.
  - `func (s *State) SetSwarmProgress(p SwarmProgress)`.
  - `func (s *State) SwarmProgress() SwarmProgress` (returns a deep copy).
  - `func (s *State) UpdateSwarmRole(name string, status SwarmRoleStatus, detail string)`.
  - `func (s *State) ClearSwarmProgress()`.

- [ ] **Step 1: Write the failing test**

Create `internal/app/session/swarm_progress_test.go`:

```go
package session

import (
	"sync"
	"testing"
)

func TestSwarmProgressSetAndCopy(t *testing.T) {
	s := New(nil) // match the existing constructor used by session_test.go
	p := SwarmProgress{
		Goal:   "add a test",
		Active: true,
		Roles: []SwarmRole{
			{Name: "planner", Status: SwarmRolePending},
			{Name: "implementer", Status: SwarmRolePending},
		},
	}
	s.SetSwarmProgress(p)

	got := s.SwarmProgress()
	got.Roles[0].Status = SwarmRoleDone // mutate the copy
	if s.SwarmProgress().Roles[0].Status != SwarmRolePending {
		t.Fatal("SwarmProgress() must return a copy; caller mutation leaked into state")
	}
}

func TestUpdateSwarmRole(t *testing.T) {
	s := New(nil)
	s.SetSwarmProgress(SwarmProgress{Active: true, Roles: []SwarmRole{
		{Name: "implementer", Status: SwarmRolePending},
	}})
	s.UpdateSwarmRole("implementer", SwarmRoleActive, "round 2/3")
	got := s.SwarmProgress().Roles[0]
	if got.Status != SwarmRoleActive || got.Detail != "round 2/3" {
		t.Fatalf("role = %+v, want active/round 2/3", got)
	}
}

func TestClearSwarmProgress(t *testing.T) {
	s := New(nil)
	s.SetSwarmProgress(SwarmProgress{Active: true, Roles: []SwarmRole{{Name: "planner"}}})
	s.ClearSwarmProgress()
	if s.SwarmProgress().Active {
		t.Fatal("ClearSwarmProgress should mark progress inactive")
	}
}

func TestSwarmProgressConcurrentUpdates(t *testing.T) {
	s := New(nil)
	s.SetSwarmProgress(SwarmProgress{Active: true, Roles: []SwarmRole{
		{Name: "scout-a", Status: SwarmRolePending},
		{Name: "scout-b", Status: SwarmRolePending},
	}})
	var wg sync.WaitGroup
	for _, name := range []string{"scout-a", "scout-b"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			s.UpdateSwarmRole(n, SwarmRoleDone, "")
		}(name)
	}
	wg.Wait()
	for _, r := range s.SwarmProgress().Roles {
		if r.Status != SwarmRoleDone {
			t.Fatalf("role %s not marked done", r.Name)
		}
	}
}
```

Note: `New(nil)` is a guess. Before writing, open `internal/app/session/session_test.go` and use the exact constructor those tests use (it may be `New(...)` with specific args or a helper). Match it verbatim.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/session/ -run Swarm -v`
Expected: FAIL with "undefined: SwarmProgress".

- [ ] **Step 3: Write minimal implementation**

Add the field to the `State` struct in `internal/app/session/session.go` (in the mutex-guarded block, near `activity`):

```go
	swarmProgress   SwarmProgress
```

Create `internal/app/session/swarm_progress.go`:

```go
package session

// SwarmRoleStatus is the lifecycle state of one role in a swarm run.
type SwarmRoleStatus string

const (
	SwarmRolePending SwarmRoleStatus = "pending"
	SwarmRoleActive  SwarmRoleStatus = "active"
	SwarmRoleDone    SwarmRoleStatus = "done"
	SwarmRoleFailed  SwarmRoleStatus = "failed"
)

// SwarmRole is one row in the swarm roster panel.
type SwarmRole struct {
	Name   string
	Status SwarmRoleStatus
	Detail string // e.g. "round 2/3" or "3/3" for scouts
}

// SwarmProgress is the live state of a swarm run, published by the
// orchestrator and rendered by the TUI roster panel. It mirrors the
// Activity pattern: the TUI reads a copy each frame; the orchestrator
// writes transitions. When Active is false the panel is hidden.
type SwarmProgress struct {
	Goal   string
	Active bool
	Roles  []SwarmRole
}

func (p SwarmProgress) clone() SwarmProgress {
	out := p
	out.Roles = append([]SwarmRole(nil), p.Roles...)
	return out
}

func (s *State) SetSwarmProgress(p SwarmProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swarmProgress = p.clone()
}

func (s *State) SwarmProgress() SwarmProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.swarmProgress.clone()
}

func (s *State) UpdateSwarmRole(name string, status SwarmRoleStatus, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.swarmProgress.Roles {
		if s.swarmProgress.Roles[i].Name == name {
			s.swarmProgress.Roles[i].Status = status
			s.swarmProgress.Roles[i].Detail = detail
			return
		}
	}
}

func (s *State) ClearSwarmProgress() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swarmProgress = SwarmProgress{}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/session/ -run Swarm -v` then `go test -race ./internal/app/session/ -run TestSwarmProgressConcurrentUpdates`
Expected: PASS, no race.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/session/
git add internal/app/session/swarm_progress.go internal/app/session/session.go internal/app/session/swarm_progress_test.go
git commit -m "feat(session): add SwarmProgress live run state"
```

---

## Task 6: Tester and implementer-feedback prompts

**Files:**
- Modify: `internal/agent/swarm/prompts.go`
- Test: `internal/agent/swarm/prompts_test.go`

**Interfaces:**
- Consumes: `*TaskState`, `TaskState.Render()`.
- Produces:
  - `func testerPrompt(ts *TaskState) string` — instructs running tests, no source edits, ending with `VERDICT: PASS`/`VERDICT: FAIL`.
  - `implementerPrompt(ts)` (existing) unchanged in signature but its text now tells the implementer to read any tester feedback in the shared state and fix failing tests.

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/swarm/prompts_test.go`:

```go
func TestTesterPromptDemandsVerdict(t *testing.T) {
	ts := NewTaskState("add a regression test")
	p := testerPrompt(ts)
	if !strings.Contains(p, "VERDICT:") {
		t.Error("tester prompt must instruct the model to emit a VERDICT line")
	}
	if !strings.Contains(strings.ToLower(p), "do not") && !strings.Contains(strings.ToLower(p), "not modify") {
		t.Error("tester prompt must forbid modifying source")
	}
	if !strings.Contains(p, "Shared task state") {
		t.Error("tester prompt must embed rendered task state")
	}
}

func TestImplementerPromptMentionsTesterFeedback(t *testing.T) {
	ts := NewTaskState("fix the bug")
	p := implementerPrompt(ts)
	if !strings.Contains(strings.ToLower(p), "test") {
		t.Error("implementer prompt should reference tests / tester feedback")
	}
}
```

Ensure `strings` is imported in the test file (existing prompt tests likely already import it).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/swarm/ -run 'TesterPrompt|ImplementerPrompt' -v`
Expected: FAIL with "undefined: testerPrompt".

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/swarm/prompts.go`, replace `implementerPrompt` and add `testerPrompt`:

```go
func implementerPrompt(ts *TaskState) string {
	return "You are the swarm implementer. Follow the plan and use the scout findings in the shared task state below. If the state contains tester feedback about failing tests, your job this round is to fix exactly those failures. Make the smallest change that accomplishes the goal, then run the narrowest useful validation. When done, respond with a final action summarising exactly what you changed.\n\n" + ts.Render()
}

func testerPrompt(ts *TaskState) string {
	return "You are the swarm tester. Run the project's tests for the change described in the shared task state below. Do not modify source files — only run tests and inspect output. Diagnose any failures: name the failing test and the minimal fix needed. End your final answer with a line reading exactly \"VERDICT: PASS\" if all relevant tests pass, or \"VERDICT: FAIL\" if any fail.\n\n" + ts.Render()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/swarm/ -run 'Prompt' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/swarm/
git add internal/agent/swarm/prompts.go internal/agent/swarm/prompts_test.go
git commit -m "feat(swarm): add tester prompt and tester-aware implementer prompt"
```

---

## Task 7: RunnerFactory registry-scope refactor (pure refactor)

**Files:**
- Modify: `internal/agent/swarm/orchestrator.go`
- Modify: `internal/app/app.go` (the factory in `buildSwarmRunner`)
- Modify: `internal/agent/swarm/orchestrator_test.go`, `internal/agent/swarm/provider_test.go` (fake factories)

**Interfaces:**
- Produces:
  - `type RegistryScope int` with `ScopeFull`, `ScopeReadOnly`, `ScopeTester`.
  - `type RunnerFactory func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error)` (replaces the `readOnly bool` parameter).
- This task changes signatures only. Behaviour is identical: every current `true` becomes `ScopeReadOnly`, every `false` becomes `ScopeFull`. `ScopeTester` is defined but not yet used until Task 8.

- [ ] **Step 1: Update the type and existing call sites**

In `internal/agent/swarm/orchestrator.go`, add above `RunnerFactory`:

```go
// RegistryScope selects which tool registry view a role's runner receives.
type RegistryScope int

const (
	ScopeFull     RegistryScope = iota // implementer: full registry (sole writer)
	ScopeReadOnly                      // planner, scouts, reviewer
	ScopeTester                        // tester: read-only + command execution
)
```

Change the `RunnerFactory` type:

```go
type RunnerFactory func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error)
```

Update the three existing calls in `Run`/`runRole`:
- Planner: `o.runRole(ctx, agent.RolePlanner, ScopeReadOnly, plannerPrompt(ts))`
- Scouts: `o.NewRunner(agent.RoleRepoScout, ScopeReadOnly)`
- Implementer: `o.runRole(ctx, agent.RoleImplementer, ScopeFull, implementerPrompt(ts))`
- Reviewer: `o.runRole(ctx, agent.RoleReviewer, ScopeReadOnly, reviewerPrompt(ts))`

Change `runRole`'s signature:

```go
func (o *Orchestrator) runRole(ctx context.Context, role agent.AgentRole, scope RegistryScope, prompt string) (*agent.Task, error) {
	runner, err := o.NewRunner(role, scope)
	if err != nil {
		return nil, err
	}
	return runner.RunTask(ctx, prompt)
}
```

- [ ] **Step 2: Update the real factory in app.go**

In `internal/app/app.go`, `buildSwarmRunner`, replace the factory signature and body's registry selection:

```go
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		route, p, err := resolver.ResolveRole(routing.AgentRole(role))
		if err != nil {
			return nil, err
		}
		toolReg := reg
		switch scope {
		case swarm.ScopeReadOnly:
			toolReg = readOnlyReg
		case swarm.ScopeTester:
			toolReg = testerReg
		}
		// ... rest unchanged ...
	}
```

And add near `readOnlyReg := registry.ReadOnlyView(reg)`:

```go
	testerReg := registry.TesterView(reg)
```

- [ ] **Step 3: Update fake factories in tests**

In `internal/agent/swarm/orchestrator_test.go` and `internal/agent/swarm/provider_test.go`, change every fake factory literal from `func(role agent.AgentRole, readOnly bool) (*agent.Runner, error)` to `func(role agent.AgentRole, scope swarm.RegistryScope)` / `func(role agent.AgentRole, scope RegistryScope)` (match the package the test is in — these tests are `package swarm`, so use `RegistryScope`). Update any assertions that checked the `readOnly` bool to check `scope == ScopeReadOnly`.

- [ ] **Step 4: Build and run to verify green**

Run: `go build ./... && go test ./internal/agent/swarm/ ./internal/app/... -v`
Expected: PASS (pure refactor, no behaviour change).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/swarm/ internal/app/
git add internal/agent/swarm/orchestrator.go internal/app/app.go internal/agent/swarm/orchestrator_test.go internal/agent/swarm/provider_test.go
git commit -m "refactor(swarm): replace RunnerFactory readOnly bool with RegistryScope"
```

---

## Task 8: Orchestrator test-fix loop, budgets, and progress

**Files:**
- Modify: `internal/agent/swarm/orchestrator.go`
- Test: `internal/agent/swarm/orchestrator_test.go`

**Interfaces:**
- Consumes: `ParseVerdict` (Task 2), `TokenMeter`/`NewEstimateMeter`/`EstimateText` (Task 3), `testerPrompt` (Task 6), `ScopeTester` (Task 7), `session.State.SetSwarmProgress/UpdateSwarmRole/ClearSwarmProgress` and the `SwarmRole*` constants (Task 5).
- Produces: `Orchestrator` gains exported fields:
  - `MaxFixRounds int` (0 → default 1, i.e. run tester once, no retry)
  - `MaxTotalTokens int` (0 → unlimited)
  - `NewMeter func() TokenMeter` (nil → `NewEstimateMeter`)
  - Behaviour: pipeline `planner → scouts → [implementer → tester]* → reviewer` with budget checks between roles and live `SwarmProgress` updates.

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/swarm/orchestrator_test.go`. These use the existing fake-runner infrastructure in that file (a factory returning runners whose `RunTask` yields scripted `*agent.Task` summaries). If the current fakes cannot script per-role summaries, extend them; do not invent a new harness if one exists.

```go
// helper: build an orchestrator whose roles return canned summaries keyed by role,
// with the tester returning a scripted sequence of summaries across rounds.
func TestSwarmLoopStopsOnPass(t *testing.T) {
	state := newTestState(t) // reuse this file's existing state constructor helper
	testerSummaries := []string{"ran tests\nVERDICT: PASS"}
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"1. do the thing"},
		agent.RoleRepoScout:   {"found files"},
		agent.RoleImplementer: {"made the change"},
		agent.RoleTester:      testerSummaries,
		agent.RoleReviewer:    {"APPROVE looks good"},
	})
	o.MaxFixRounds = 3

	if err := o.Run(context.Background(), "add a test"); err != nil {
		t.Fatal(err)
	}
	// implementer runs exactly once when tests pass first round.
	if got := o.callCount(agent.RoleImplementer); got != 1 {
		t.Errorf("implementer ran %d times, want 1", got)
	}
	if got := o.callCount(agent.RoleTester); got != 1 {
		t.Errorf("tester ran %d times, want 1", got)
	}
}

func TestSwarmLoopRetriesOnFailThenPasses(t *testing.T) {
	state := newTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"plan"},
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"attempt 1", "attempt 2"},
		agent.RoleTester:      {"fail\nVERDICT: FAIL", "pass\nVERDICT: PASS"},
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxFixRounds = 3

	if err := o.Run(context.Background(), "fix bug"); err != nil {
		t.Fatal(err)
	}
	if got := o.callCount(agent.RoleImplementer); got != 2 {
		t.Errorf("implementer ran %d times, want 2 (one retry)", got)
	}
	if got := o.callCount(agent.RoleTester); got != 2 {
		t.Errorf("tester ran %d times, want 2", got)
	}
}

func TestSwarmLoopStopsAtMaxRounds(t *testing.T) {
	state := newTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"plan"},
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"a", "b", "c", "d"},
		agent.RoleTester:      {"f\nVERDICT: FAIL", "f\nVERDICT: FAIL", "f\nVERDICT: FAIL", "f\nVERDICT: FAIL"},
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxFixRounds = 2

	if err := o.Run(context.Background(), "hopeless"); err != nil {
		t.Fatal(err)
	}
	// 2 rounds => implementer twice, tester twice, then reviewer regardless.
	if got := o.callCount(agent.RoleImplementer); got != 2 {
		t.Errorf("implementer ran %d times, want 2 (capped)", got)
	}
	if got := o.callCount(agent.RoleReviewer); got != 1 {
		t.Errorf("reviewer ran %d times, want 1 (always runs)", got)
	}
}

func TestSwarmUnparseableVerdictStopsLoop(t *testing.T) {
	state := newTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"plan"},
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"change"},
		agent.RoleTester:      {"I could not tell"}, // no VERDICT line
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxFixRounds = 3

	if err := o.Run(context.Background(), "ambiguous"); err != nil {
		t.Fatal(err)
	}
	if got := o.callCount(agent.RoleImplementer); got != 1 {
		t.Errorf("implementer ran %d times, want 1 (no loop on unparseable verdict)", got)
	}
}

func TestSwarmTokenCeilingStopsAfterCurrentRole(t *testing.T) {
	state := newTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {strings.Repeat("word ", 500)}, // large summary → big estimate
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"change"},
		agent.RoleTester:      {"pass\nVERDICT: PASS"},
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxTotalTokens = 10 // tiny ceiling: tripped after planner

	if err := o.Run(context.Background(), "big"); err != nil {
		t.Fatal(err)
	}
	// Ceiling hit after planner → scouts/implementer/tester skipped, reviewer/summary still reached.
	if got := o.callCount(agent.RoleImplementer); got != 0 {
		t.Errorf("implementer ran %d times, want 0 (budget exhausted)", got)
	}
}

func TestSwarmPublishesProgress(t *testing.T) {
	state := newTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"plan"},
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"change"},
		agent.RoleTester:      {"pass\nVERDICT: PASS"},
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxFixRounds = 1
	_ = o.Run(context.Background(), "x")
	// After a completed run, progress is cleared (inactive).
	if state.SwarmProgress().Active {
		t.Error("progress should be cleared after run completes")
	}
}
```

Note: `newScriptedOrchestrator`, `newTestState`, and `callCount` should be built on top of this file's existing fake factory. If the existing tests already track call counts and script summaries, reuse those helpers and adapt names. The point of each test is the assertion, not the helper.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/swarm/ -run TestSwarm -v`
Expected: FAIL (loop/budget behaviour not implemented; `MaxFixRounds` field undefined).

- [ ] **Step 3: Rewrite `Run` and add fields**

Replace the `Orchestrator` struct and `Run` in `internal/agent/swarm/orchestrator.go`:

```go
type Orchestrator struct {
	State        *session.State
	NewRunner    RunnerFactory
	ScoutFocuses []ScoutFocus

	MaxFixRounds   int              // max implementer↔tester rounds (0 → 1)
	MaxTotalTokens int              // whole-run token ceiling (0 → unlimited)
	NewMeter       func() TokenMeter // nil → NewEstimateMeter
}

func New(state *session.State, factory RunnerFactory) *Orchestrator {
	return &Orchestrator{State: state, NewRunner: factory, ScoutFocuses: DefaultScoutFocuses}
}

func (o *Orchestrator) SetForceClass(string) {}

func (o *Orchestrator) maxRounds() int {
	if o.MaxFixRounds < 1 {
		return 1
	}
	return o.MaxFixRounds
}

func (o *Orchestrator) newMeter() TokenMeter {
	if o.NewMeter != nil {
		return o.NewMeter()
	}
	return NewEstimateMeter()
}

// overBudget reports whether the token ceiling is set and exceeded.
func (o *Orchestrator) overBudget(meter TokenMeter) bool {
	return o.MaxTotalTokens > 0 && meter.Total() >= o.MaxTotalTokens
}

func (o *Orchestrator) Run(ctx context.Context, goal string) error {
	ts := NewTaskState(goal)
	meter := o.newMeter()

	// Initialise the roster panel.
	o.State.SetSwarmProgress(session.SwarmProgress{
		Goal:   goal,
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "planner", Status: session.SwarmRolePending},
			{Name: "scouts", Status: session.SwarmRolePending},
			{Name: "implementer", Status: session.SwarmRolePending},
			{Name: "tester", Status: session.SwarmRolePending},
			{Name: "reviewer", Status: session.SwarmRolePending},
		},
	})
	defer o.State.ClearSwarmProgress()

	o.announce("Swarm run started.")

	// 1. Planner (read-only).
	o.State.UpdateSwarmRole("planner", session.SwarmRoleActive, "")
	plannerTask, err := o.runRole(ctx, agent.RolePlanner, ScopeReadOnly, plannerPrompt(ts))
	if err != nil {
		o.State.UpdateSwarmRole("planner", session.SwarmRoleFailed, "")
		o.announce("Swarm aborted: planner failed.")
		return err
	}
	ts.SetPlan(planLines(plannerTask.Summary))
	o.observe(meter, agent.RolePlanner, plannerPrompt(ts), plannerTask.Summary)
	o.State.UpdateSwarmRole("planner", session.SwarmRoleDone, "")

	// 2. Repo scouts (read-only, parallel).
	if !o.overBudget(meter) {
		focuses := o.focuses()
		o.State.UpdateSwarmRole("scouts", session.SwarmRoleActive, fmt.Sprintf("0/%d", len(focuses)))
		type scoutJob struct {
			focus  ScoutFocus
			runner *agent.Runner
			prompt string
		}
		jobs := make([]scoutJob, 0, len(focuses))
		for _, focus := range focuses {
			runner, err := o.NewRunner(agent.RoleRepoScout, ScopeReadOnly)
			if err != nil {
				o.State.UpdateSwarmRole("scouts", session.SwarmRoleFailed, "")
				o.announce("Swarm aborted: could not build repo scout.")
				return err
			}
			jobs = append(jobs, scoutJob{focus: focus, runner: runner, prompt: scoutPrompt(ts, focus)})
		}
		var wg sync.WaitGroup
		var done int32
		for _, job := range jobs {
			wg.Add(1)
			go func(j scoutJob) {
				defer wg.Done()
				task, err := j.runner.RunTask(ctx, j.prompt)
				if err != nil {
					ts.AddFinding(Finding{Agent: "repo_scout", Area: j.focus.Area, Content: "scout failed: " + err.Error()})
				} else {
					ts.AddFinding(Finding{Agent: "repo_scout", Area: j.focus.Area, Content: task.Summary})
					o.observe(meter, agent.RoleRepoScout, j.prompt, task.Summary)
				}
				n := atomic.AddInt32(&done, 1)
				o.State.UpdateSwarmRole("scouts", session.SwarmRoleActive, fmt.Sprintf("%d/%d", n, len(jobs)))
			}(job)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		o.State.UpdateSwarmRole("scouts", session.SwarmRoleDone, fmt.Sprintf("%d/%d", len(jobs), len(jobs)))
	} else {
		o.State.UpdateSwarmRole("scouts", session.SwarmRoleDone, "skipped (budget)")
	}

	// 3. Implementer ↔ tester loop (implementer is the only writer).
	rounds := o.maxRounds()
	for round := 1; round <= rounds; round++ {
		if o.overBudget(meter) {
			break
		}
		o.State.UpdateSwarmRole("implementer", session.SwarmRoleActive, fmt.Sprintf("round %d/%d", round, rounds))
		implPrompt := implementerPrompt(ts)
		implTask, err := o.runRole(ctx, agent.RoleImplementer, ScopeFull, implPrompt)
		if err != nil {
			o.State.UpdateSwarmRole("implementer", session.SwarmRoleFailed, "")
			o.announce("Swarm aborted: implementer failed.")
			return err
		}
		ts.AddPatchNote(implTask.Summary)
		o.observe(meter, agent.RoleImplementer, implPrompt, implTask.Summary)
		o.State.UpdateSwarmRole("implementer", session.SwarmRoleDone, fmt.Sprintf("round %d/%d", round, rounds))

		if o.overBudget(meter) {
			break
		}

		o.State.UpdateSwarmRole("tester", session.SwarmRoleActive, fmt.Sprintf("round %d/%d", round, rounds))
		testPrompt := testerPrompt(ts)
		testTask, err := o.runRole(ctx, agent.RoleTester, ScopeTester, testPrompt)
		if err != nil {
			// A tester failure is a finding, not fatal: proceed to review.
			ts.AddFinding(Finding{Agent: "tester", Area: "tests", Content: "tester failed: " + err.Error()})
			o.State.UpdateSwarmRole("tester", session.SwarmRoleFailed, "")
			break
		}
		ts.AddFinding(Finding{Agent: "tester", Area: "tests", Content: testTask.Summary})
		o.observe(meter, agent.RoleTester, testPrompt, testTask.Summary)

		pass, ok := ParseVerdict(testTask.Summary)
		if pass || !ok {
			// Pass, or ambiguous verdict → stop looping.
			o.State.UpdateSwarmRole("tester", session.SwarmRoleDone, fmt.Sprintf("round %d/%d", round, rounds))
			break
		}
		// Explicit FAIL with rounds remaining → loop; the tester's diagnosis
		// is already in task state for the next implementer round.
		o.State.UpdateSwarmRole("tester", session.SwarmRoleDone, fmt.Sprintf("round %d/%d FAIL", round, rounds))
	}

	// 4. Reviewer (read-only). Always runs; a failure is reported, not fatal.
	o.State.UpdateSwarmRole("reviewer", session.SwarmRoleActive, "")
	reviewPrompt := reviewerPrompt(ts)
	reviewTask, err := o.runRole(ctx, agent.RoleReviewer, ScopeReadOnly, reviewPrompt)
	if err != nil {
		ts.SetFinalSummary("Reviewer failed: " + err.Error())
		o.State.UpdateSwarmRole("reviewer", session.SwarmRoleFailed, "")
	} else {
		ts.SetFinalSummary(reviewTask.Summary)
		o.observe(meter, agent.RoleReviewer, reviewPrompt, reviewTask.Summary)
		o.State.UpdateSwarmRole("reviewer", session.SwarmRoleDone, "")
	}

	summary := ts.Render()
	if o.MaxTotalTokens > 0 {
		summary += fmt.Sprintf("\n\n_Token budget: ~%d / %d (estimated)._", meter.Total(), o.MaxTotalTokens)
	}
	o.State.AddMessage(session.RoleSystem, "Swarm complete.\n\n"+summary, session.ContentTypeMarkdown)
	o.announce("Swarm run complete.")
	return nil
}

// observe records estimated token usage for one role turn.
func (o *Orchestrator) observe(meter TokenMeter, role agent.AgentRole, prompt, answer string) {
	meter.Observe(role, EstimateText(prompt), EstimateText(answer))
}
```

Add imports: `"sync/atomic"` (and keep `fmt`, `sync`, `context`, `strings`).

Reduce `announce`: it now only posts the three milestone lines above (started / complete / aborted); no per-role lines. Leave the `announce` helper itself unchanged.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/agent/swarm/ -run TestSwarm -v`
Expected: PASS, no race. Then `go test ./internal/agent/swarm/` for the full package.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/swarm/
git add internal/agent/swarm/orchestrator.go internal/agent/swarm/orchestrator_test.go
git commit -m "feat(swarm): test-fix loop, token budget, and live progress"
```

---

## Task 9: Wire budgets, meter, and per-role tool caps in app.go

**Files:**
- Modify: `internal/app/app.go` (`buildSwarmRunner`)
- Test: `internal/app/app_test.go` (extend an existing swarm-wiring test if present; otherwise a focused unit test on a small helper — see Step 1)

**Interfaces:**
- Consumes: `cfg.Swarm.Budget` (Task 4), `swarm.NewEstimateMeter`/`swarm.NewProviderUsageMeter` (Task 3), `Orchestrator.MaxFixRounds/MaxTotalTokens/NewMeter` (Task 8).
- Produces: an `Orchestrator` configured from `cfg.Swarm.Budget`, and per-role tool caps applied in the factory.

- [ ] **Step 1: Write the failing test**

Add a small pure helper and test it (keeps this testable without a live TUI). In `internal/app/app.go` add:

```go
// roleToolIterations returns the per-role tool-iteration cap, falling back
// to the agent-wide cap when no role-specific value is configured.
func roleToolIterations(cfg config.Config, role agent.AgentRole) int {
	if n, ok := cfg.Swarm.Budget.ToolIters[string(role)]; ok && n > 0 {
		return n
	}
	return cfg.Agent.MaxToolIterations
}
```

In `internal/app/app_test.go`:

```go
func TestRoleToolIterationsFallsBack(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.MaxToolIterations = 16
	cfg.Swarm.Budget.ToolIters = map[string]int{"implementer": 25}

	if got := roleToolIterations(cfg, agent.RoleImplementer); got != 25 {
		t.Errorf("implementer cap = %d, want 25 (role-specific)", got)
	}
	if got := roleToolIterations(cfg, agent.RoleTester); got != 16 {
		t.Errorf("tester cap = %d, want 16 (fallback to agent default)", got)
	}
}
```

Ensure `app_test.go` imports `marshal/internal/agent` and `marshal/internal/app/config` (add if missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestRoleToolIterations -v`
Expected: FAIL with "undefined: roleToolIterations".

- [ ] **Step 3: Implement wiring**

In `buildSwarmRunner`, inside the factory, replace the `MaxToolIterations` block:

```go
		if cap := roleToolIterations(cfg, role); cap > 0 {
			r.MaxToolIterations = cap
		}
```

At the end of `buildSwarmRunner`, replace `return swarm.New(state, factory)` with:

```go
	o := swarm.New(state, factory)
	o.MaxFixRounds = cfg.Swarm.Budget.MaxFixRounds
	o.MaxTotalTokens = cfg.Swarm.Budget.MaxTotalTokens
	o.NewMeter = func() swarm.TokenMeter { return swarm.NewEstimateMeter() }
	return o
```

(The `roleToolIterations` helper from Step 1 is also added.)

- [ ] **Step 4: Run test + build**

Run: `go build ./... && go test ./internal/app/ -run TestRoleToolIterations -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): wire swarm budgets, meter, and per-role tool caps"
```

---

## Task 10: Swarm roster panel in the TUI

**Files:**
- Create: `internal/app/tui/swarm_panel.go`
- Modify: `internal/app/tui/view.go` (insert panel into stack)
- Modify: `internal/app/tui/model.go` (reserve height; recompute on tick)
- Test: `internal/app/tui/swarm_panel_test.go`

**Interfaces:**
- Consumes: `session.SwarmProgress`, `SwarmRole`, `SwarmRoleStatus` constants (Task 5); `m.state.SwarmProgress()`.
- Produces:
  - `func renderSwarmPanel(p session.SwarmProgress, spinnerFrame string, width int) string`.
  - `const swarmPanelRows = 8` (fixed reservation: title + 5 role rows + 2 border rows).
  - `func (m Model) swarmPanelRows() int` → `swarmPanelRows` when `m.state.SwarmProgress().Active`, else `0`.

- [ ] **Step 1: Write the failing test**

Create `internal/app/tui/swarm_panel_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestRenderSwarmPanelShowsRolesAndStatus(t *testing.T) {
	p := session.SwarmProgress{
		Goal:   "add a regression test",
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "planner", Status: session.SwarmRoleDone},
			{Name: "scouts", Status: session.SwarmRoleDone, Detail: "3/3"},
			{Name: "implementer", Status: session.SwarmRoleActive, Detail: "round 2/3"},
			{Name: "tester", Status: session.SwarmRolePending},
			{Name: "reviewer", Status: session.SwarmRolePending},
		},
	}
	out := renderSwarmPanel(p, "*", 60)

	for _, want := range []string{"add a regression test", "planner", "implementer", "round 2/3", "tester", "reviewer"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q\n%s", want, out)
		}
	}
}

func TestRenderSwarmPanelInactiveIsEmpty(t *testing.T) {
	if out := renderSwarmPanel(session.SwarmProgress{Active: false}, "*", 60); out != "" {
		t.Errorf("inactive panel should render empty, got %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/ -run SwarmPanel -v`
Expected: FAIL with "undefined: renderSwarmPanel".

- [ ] **Step 3: Implement the panel renderer**

Create `internal/app/tui/swarm_panel.go`:

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"marshal/internal/app/session"
)

const swarmPanelRows = 8 // title + 5 role rows + top/bottom border

func statusGlyph(status session.SwarmRoleStatus, spinnerFrame string) string {
	switch status {
	case session.SwarmRoleDone:
		return "✔"
	case session.SwarmRoleActive:
		return spinnerFrame
	case session.SwarmRoleFailed:
		return "✗"
	default:
		return "○"
	}
}

// renderSwarmPanel draws the swarm roster. Returns "" when no run is active
// so the caller can omit it from the view stack entirely.
func renderSwarmPanel(p session.SwarmProgress, spinnerFrame string, width int) string {
	if !p.Active {
		return ""
	}
	inner := max(width-4, 1)

	var b strings.Builder
	title := truncateRunes("Swarm: "+p.Goal, inner)
	b.WriteString(promptPrefixStyle.Render(title))
	for _, r := range p.Roles {
		b.WriteString("\n")
		line := fmt.Sprintf("%s %s", statusGlyph(r.Status, spinnerFrame), r.Name)
		if r.Detail != "" {
			line += "   " + r.Detail
		}
		b.WriteString(truncateRunes(line, inner))
	}

	return inputBoxStyle.Width(max(width-2, 1)).Render(b.String())
}
```

Note: reuse whatever styles the file's neighbours use (`inputBoxStyle`, `promptPrefixStyle`, `truncateRunes`, `max` all already exist in package `tui` — confirm names while implementing and match them). If `inputBoxStyle` adds its own border rows that change the height, adjust `swarmPanelRows` so the reserved height matches the rendered height (verify with the height test in Step 4).

- [ ] **Step 4: Insert into the view stack and reserve height**

In `internal/app/tui/view.go`, change `View()`:

```go
	transcript := m.renderTranscriptFrame()
	rows := []string{transcript}
	if panel := renderSwarmPanel(m.state.SwarmProgress(), m.spinnerFrame, m.width); panel != "" {
		rows = append(rows, panel)
	}
	rows = append(rows, m.renderInputArea(), m.renderStatusLine(m.width))
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
```

In `internal/app/tui/model.go`, add the reservation helper:

```go
func (m Model) swarmPanelRows() int {
	if m.state.SwarmProgress().Active {
		return swarmPanelRows
	}
	return 0
}
```

Update the viewport height formula in `resize` (currently model.go:193):

```go
	m.viewport.Height = max(height-transcriptFrameRows-m.swarmPanelRows()-m.inputAreaRows()-statusLineRows, 1)
```

Make the tick handler recompute the layout so the panel's appearance/disappearance is reflected mid-run. Find the `tickMsg` case in `Update` (the one that returns `tickCmd()` around model.go:236) and, before it returns, add:

```go
		m.resize(m.width, m.height)
		m.refreshViewport()
```

Also clear layout on completion: in the `agentFinishedMsg` case (model.go ~213), after `m.busy = false`, add:

```go
		m.resize(m.width, m.height)
```

(`ClearSwarmProgress` is already called by the orchestrator's deferred cleanup, so `SwarmProgress().Active` is false by the time this recomputes.)

- [ ] **Step 5: Verify height reservation with a test**

Add to `internal/app/tui/swarm_panel_test.go`:

```go
func TestSwarmPanelReservesHeight(t *testing.T) {
	// A model with an active swarm run must reserve swarmPanelRows so the
	// transcript viewport shrinks by exactly that amount.
	// Build a Model via the package's existing test constructor, set a size,
	// read viewport height with no run, then activate a run and re-resize.
	m := newTestModel(t) // reuse this package's existing model test helper
	m.resize(80, 40)
	before := m.viewport.Height

	m.state.SetSwarmProgress(session.SwarmProgress{Active: true, Roles: []session.SwarmRole{{Name: "planner"}}})
	m.resize(80, 40)
	after := m.viewport.Height

	if before-after != swarmPanelRows {
		t.Fatalf("viewport shrank by %d, want %d", before-after, swarmPanelRows)
	}
}
```

Use the existing model constructor from `internal/app/tui/model_test.go` (it already builds a `Model` with a `session.State`; reuse it rather than constructing by hand). Run:

`go test ./internal/app/tui/ -run Swarm -v`
Expected: PASS. If `before-after` differs from `swarmPanelRows`, adjust the constant to the rendered height and re-run.

- [ ] **Step 6: Manual smoke check**

Run: `go build ./cmd/marshal` — expect exit 0. (A full interactive smoke of `/swarm` requires a running local model and a real TTY; note it for the reviewer but do not block the task on it.)

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/app/tui/
git add internal/app/tui/swarm_panel.go internal/app/tui/view.go internal/app/tui/model.go internal/app/tui/swarm_panel_test.go
git commit -m "feat(tui): swarm roster panel above input with reserved height"
```

---

## Task 11: Full verification and checklist bookkeeping

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md`

- [ ] **Step 1: Run the whole suite (with race)**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all packages PASS. Then `go test -race ./internal/agent/swarm/ ./internal/app/session/`.

- [ ] **Step 2: Tick Milestone O and note Phase 5 polish**

In `docs/10-mvp-implementation-checklist.md`, change all eight `## Milestone O` items from `- [ ]` to `- [x]`, and append below the section:

```markdown
## Phase 5 polish (post-Milestone O)

- [x] Tester role integrated as an implementer↔tester feedback loop
- [x] Swarm roster activity panel
- [x] Run-level budgets (max fix rounds, per-role tool caps, token ceiling)
- [ ] Real provider `usage` token metering (later milestone; ProviderUsageMeter stub in place)
```

- [ ] **Step 3: Commit**

```bash
git add docs/10-mvp-implementation-checklist.md
git commit -m "docs: mark Milestone O complete, record Phase 5 polish"
```

---

## Self-Review Notes

- **Spec coverage:** Component 1 (tester loop) → Tasks 1,2,6,7,8. Component 2 (activity panel) → Tasks 5,10. Component 3 (budgets) → Tasks 3,4,8,9. Config schema → Task 4. Out-of-scope real-usage parsing → only the `ProviderUsageMeter` stub (Task 3); explicitly deferred in Task 11 checklist. Bookkeeping follow-up → Task 11.
- **Type consistency:** `RegistryScope`/`ScopeReadOnly`/`ScopeTester`/`ScopeFull` (Tasks 7,8,9); `TokenMeter`/`NewEstimateMeter`/`EstimateText`/`NewProviderUsageMeter` (Tasks 3,8,9); `SwarmProgress`/`SwarmRole`/`SwarmRole{Pending,Active,Done,Failed}` and `SetSwarmProgress`/`UpdateSwarmRole`/`ClearSwarmProgress` (Tasks 5,8,10); `ParseVerdict` (Tasks 2,8); `roleToolIterations` (Task 9). Names are used identically across producing and consuming tasks.
- **Known adaptation points flagged inline:** test-helper/constructor names in existing test files (`New(...)` for session, model constructor in `model_test.go`, config load helper, swarm fake-factory helpers) must be matched to the real code when implementing — the plan calls this out at each such spot rather than inventing signatures.
```
