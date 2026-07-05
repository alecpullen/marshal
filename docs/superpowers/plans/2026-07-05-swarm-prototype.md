# Milestone O: First Swarm Prototype — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Marshal's first swarm: a sequential Planner → parallel Repo Scouts → Implementer → Reviewer pipeline sharing one task-state blackboard, with a write lock so only one agent can modify the workspace at a time, triggered by a `/swarm <goal>` TUI command.

**Architecture:** A new `internal/agent/swarm` package holds the shared `TaskState`, the `WriteLock`, role prompts, and the `Orchestrator`. The existing `agent.Runner` is reused for every role turn with three small extensions: a `Role` field (role-specific system prompt), a `RunTask` method (returns the finished `*Task` so the orchestrator can read each role's final summary), and a `WriteGate` hook (serialises non-read-only tool execution). Read-only roles get a filtered registry view so write tools are invisible and un-callable. Per-role model presets come from the existing routing config via a new `ResolveRole` method.

**Tech Stack:** Go, existing packages only (no new dependencies). Bubble Tea TUI untouched except command dispatch.

**Spec:** `docs/07-agent-runtime-and-swarm.md` (sections "Swarm philosophy" through "First swarm milestone") and the Milestone O checklist in `docs/10-mvp-implementation-checklist.md:146-155`. The Tester role, agent activity panel, agent budgets, and specialist routing are Phase 5 features *outside* Milestone O — do not build them.

## Global Constraints

- Build requires CGO: `CGO_ENABLED=1 go build ./cmd/marshal` (tree-sitter dependency).
- Before every commit: `gofmt -w .` and `go vet ./...` must be clean.
- The TUI renders only — no routing, policy, or prompt logic in `internal/app/tui` (CLAUDE.md). The `/swarm` case in `dispatchCommand` may only dispatch, exactly like the existing message-submit path.
- Local-first: no assumption of remote providers; routing fallbacks must keep working with `remote_providers_allowed = false`.
- Swarm safety rules (docs/07): many agents may read at once; only one agent may write at a time; agents never talk to each other except through shared task state.
- Package import direction: `swarm` imports `agent`; `agent` must NOT import `swarm` (the `WriteGate` interface lives in `agent` for this reason).
- Tests must pass under the race detector: run swarm-package tests with `go test -race ./internal/agent/...`.
- Commit only the files each task names — the working tree may contain unrelated changes.

## Pre-flight

The working tree currently has unrelated uncommitted modifications to `internal/app/session/session.go`, `internal/app/tui/model.go`, `internal/db/db.go`, and `.marshal/skills/systematic-debugging.md`. Task 9 modifies `internal/app/tui/model.go`, which is one of those dirty files. **Before starting Task 1, ask the user to commit or stash the pre-existing changes.** If they cannot, stop and report rather than committing mixed changes.

---

### Task 1: Shared task state (`swarm.TaskState`)

**Files:**
- Create: `internal/agent/swarm/state.go`
- Test: `internal/agent/swarm/state_test.go`

**Interfaces:**
- Consumes: nothing (leaf package file).
- Produces: `swarm.NewTaskState(goal string) *TaskState`; methods `SetPlan(plan []string)`, `Plan() []string`, `AddFinding(f Finding)`, `Findings() []Finding`, `AddPatchNote(note string)`, `SetFinalSummary(s string)`, `FinalSummary() string`, `Render() string`; type `Finding{Agent, Area, Content string}`. Later tasks (6, 7) call all of these with exactly these names.

Scope note: docs/07's shared-state JSON also lists `constraints`, `files_in_scope`, `test_results`, `open_questions`. Nothing in Milestone O writes those (the Tester role is Phase 5), so they are deliberately omitted — YAGNI. Add them when a role needs them.

- [ ] **Step 1: Write the failing tests**

```go
// internal/agent/swarm/state_test.go
package swarm

import (
	"strings"
	"sync"
	"testing"
)

func TestTaskStateRenderIncludesPopulatedSectionsOnly(t *testing.T) {
	ts := NewTaskState("fix the parser")

	rendered := ts.Render()
	if !strings.Contains(rendered, "Goal: fix the parser") {
		t.Fatalf("Render missing goal, got:\n%s", rendered)
	}
	for _, absent := range []string{"Plan:", "Findings:", "Changes made:", "Review:"} {
		if strings.Contains(rendered, absent) {
			t.Fatalf("Render should omit empty section %q, got:\n%s", absent, rendered)
		}
	}

	ts.SetPlan([]string{"1. read parser.go", "2. patch it"})
	ts.AddFinding(Finding{Agent: "repo_scout", Area: "tests", Content: "parser_test.go covers Parse"})
	ts.AddPatchNote("fixed off-by-one in Parse")
	ts.SetFinalSummary("APPROVE")

	rendered = ts.Render()
	for _, want := range []string{
		"Plan:", "1. read parser.go",
		"Findings:", "[repo_scout/tests] parser_test.go covers Parse",
		"Changes made:", "fixed off-by-one in Parse",
		"Review:", "APPROVE",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Render missing %q, got:\n%s", want, rendered)
		}
	}
}

func TestTaskStateAccessorsReturnCopies(t *testing.T) {
	ts := NewTaskState("goal")
	ts.SetPlan([]string{"step"})
	plan := ts.Plan()
	plan[0] = "mutated"
	if got := ts.Plan()[0]; got != "step" {
		t.Fatalf("Plan() must return a copy; internal state became %q", got)
	}
}

func TestTaskStateIsSafeForConcurrentWrites(t *testing.T) {
	ts := NewTaskState("goal")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ts.AddFinding(Finding{Agent: "repo_scout", Area: "code", Content: "x"})
			_ = ts.Render()
		}()
	}
	wg.Wait()
	if got := len(ts.Findings()); got != 8 {
		t.Fatalf("Findings length = %d, want 8", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/swarm/ -v`
Expected: FAIL to compile — `undefined: NewTaskState`, `undefined: Finding`.

- [ ] **Step 3: Write the implementation**

```go
// internal/agent/swarm/state.go
package swarm

import (
	"fmt"
	"strings"
	"sync"
)

// Finding is one observation an agent recorded in shared task state.
type Finding struct {
	Agent   string // role that produced it, e.g. "repo_scout"
	Area    string // focus area, e.g. "tests"
	Content string
}

// TaskState is the shared blackboard for one swarm run (docs/07, "Shared
// task state"). Agents never talk to each other directly: the orchestrator
// writes role outputs here and each role reads the whole state via Render.
// All methods are safe for concurrent use — parallel repo scouts write
// findings from separate goroutines.
type TaskState struct {
	mu           sync.Mutex
	goal         string
	plan         []string
	findings     []Finding
	patchNotes   []string
	finalSummary string
}

func NewTaskState(goal string) *TaskState {
	return &TaskState{goal: goal}
}

func (ts *TaskState) SetPlan(plan []string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.plan = append([]string(nil), plan...)
}

func (ts *TaskState) Plan() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.plan...)
}

func (ts *TaskState) AddFinding(f Finding) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.findings = append(ts.findings, f)
}

func (ts *TaskState) Findings() []Finding {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]Finding(nil), ts.findings...)
}

func (ts *TaskState) AddPatchNote(note string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.patchNotes = append(ts.patchNotes, note)
}

func (ts *TaskState) SetFinalSummary(s string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.finalSummary = s
}

func (ts *TaskState) FinalSummary() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.finalSummary
}

// Render produces the markdown block injected into every role prompt.
// Empty sections are omitted so early roles (planner) see a compact state.
func (ts *TaskState) Render() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	var b strings.Builder
	b.WriteString("## Shared task state\n\n")
	b.WriteString("Goal: " + ts.goal + "\n")
	if len(ts.plan) > 0 {
		b.WriteString("\nPlan:\n")
		for _, step := range ts.plan {
			b.WriteString("- " + step + "\n")
		}
	}
	if len(ts.findings) > 0 {
		b.WriteString("\nFindings:\n")
		for _, f := range ts.findings {
			b.WriteString(fmt.Sprintf("- [%s/%s] %s\n", f.Agent, f.Area, f.Content))
		}
	}
	if len(ts.patchNotes) > 0 {
		b.WriteString("\nChanges made:\n")
		for _, note := range ts.patchNotes {
			b.WriteString("- " + note + "\n")
		}
	}
	if ts.finalSummary != "" {
		b.WriteString("\nReview:\n" + ts.finalSummary + "\n")
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/agent/swarm/ -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/swarm/state.go internal/agent/swarm/state_test.go
git commit -m "feat(swarm): add shared task state blackboard"
```

---

### Task 2: Read-only registry view

**Files:**
- Create: `internal/tools/registry/view.go`
- Test: `internal/tools/registry/view_test.go`

**Interfaces:**
- Consumes: `registry.Registry` (existing: `New()`, `Register(Tool) error`, `Lookup(name)`, `List()`), `registry.RiskReadOnly`.
- Produces: `registry.ReadOnlyView(src *Registry) *Registry` — used by Task 8's runner factory. Because scouts/planner/reviewer runners hold this view, write tools are both absent from their system-prompt tool list (Runner renders `Registry.List()`) and un-callable (`Lookup` misses → existing "unknown tool" error path).

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/registry/view_test.go
package registry

import (
	"context"
	"testing"
)

func nopHandler(ctx context.Context, call ToolCall) (ToolResult, error) {
	return ToolResult{Summary: "ok"}, nil
}

func TestReadOnlyViewFiltersOutWriteTools(t *testing.T) {
	src := New()
	mustRegister := func(tool Tool) {
		t.Helper()
		if err := src.Register(tool); err != nil {
			t.Fatalf("Register(%s): %v", tool.Name, err)
		}
	}
	mustRegister(Tool{Name: "file.read", Description: "read", Risk: RiskReadOnly, Handler: nopHandler})
	mustRegister(Tool{Name: "file.write_patch", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler})
	mustRegister(Tool{Name: "shell.run", Description: "shell", Risk: RiskCommand, Handler: nopHandler})

	view := ReadOnlyView(src)

	if _, ok := view.Lookup("file.read"); !ok {
		t.Fatal("read-only tool missing from view")
	}
	if _, ok := view.Lookup("file.write_patch"); ok {
		t.Fatal("workspace_write tool must not be in read-only view")
	}
	if _, ok := view.Lookup("shell.run"); ok {
		t.Fatal("command tool must not be in read-only view")
	}
	if got := len(view.List()); got != 1 {
		t.Fatalf("view.List() has %d tools, want 1", got)
	}
	// Source registry is untouched.
	if got := len(src.List()); got != 3 {
		t.Fatalf("source registry mutated: %d tools, want 3", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/registry/ -run TestReadOnlyView -v`
Expected: FAIL to compile — `undefined: ReadOnlyView`.

- [ ] **Step 3: Write the implementation**

```go
// internal/tools/registry/view.go
package registry

// ReadOnlyView returns a new Registry containing only src's read-only
// tools. Swarm roles that must not modify the workspace (planner, repo
// scout, reviewer) are given this view: write tools disappear from their
// system-prompt tool list and Lookup fails for them, so read-only access
// is enforced structurally rather than by prompt instructions.
func ReadOnlyView(src *Registry) *Registry {
	view := New()
	for _, tool := range src.List() {
		if tool.Risk == RiskReadOnly {
			// Tools were valid when registered with src; re-registering
			// the same Tool value cannot fail.
			_ = view.Register(tool)
		}
	}
	return view
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tools/registry/ -v`
Expected: PASS (all registry tests, including the new one).

- [ ] **Step 5: Commit**

```bash
git add internal/tools/registry/view.go internal/tools/registry/view_test.go
git commit -m "feat(registry): add ReadOnlyView for read-only swarm roles"
```

---

### Task 3: Role-aware Runner + RunTask

**Files:**
- Modify: `internal/agent/prompts.go` (add `RoleRepoScout` const + addendum, ~lines 13-55)
- Modify: `internal/agent/runner.go` (add `Role` field, `role()` helper, `RunTask`; three `BuildSystemPrompt` call sites at lines 141, 162, 175)
- Test: `internal/agent/prompts_test.go`, `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: existing `BuildSystemPrompt(role AgentRole, ...)`, `agent.Task` (fields `Status`, `Summary`), `TaskStatusCompleted`.
- Produces: `agent.RoleRepoScout AgentRole = "repo_scout"` (string value must match `routing.RoleRepoScout` — Task 8 converts between the two types by string cast); `Runner.Role AgentRole` field (zero value keeps current `RoleGeneral` behaviour); `Runner.RunTask(ctx context.Context, goal string) (*Task, error)` with `Run` becoming a thin wrapper. Tasks 4, 7, 8 depend on these exact names.

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/prompts_test.go`:

```go
func TestBuildSystemPromptRepoScoutRole(t *testing.T) {
	msg := BuildSystemPrompt(RoleRepoScout, nil, nil, nil)
	if !strings.Contains(msg.Content, "repo scout") {
		t.Fatalf("repo scout system prompt missing role focus:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, "tool_call, final") {
		t.Fatalf("repo scout system prompt missing allowed actions:\n%s", msg.Content)
	}
}
```

Add to `internal/agent/runner_test.go` (uses the existing `scriptedProvider` and `newTestState` helpers already in that file):

```go
func TestRunnerUsesConfiguredRoleInSystemPrompt(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale": "done", "action": {"type": "final", "content": "review complete"}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.Role = RoleReviewer
	runner.SetForceClass("question")

	if err := runner.Run(context.Background(), "review the diff"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.requests) == 0 {
		t.Fatal("provider was never called")
	}
	system := p.requests[0].Messages[0].Content
	if !strings.Contains(system, "You are a reviewer") {
		t.Fatalf("system prompt did not use reviewer role:\n%s", system)
	}
}

func TestRunTaskReturnsCompletedTaskWithSummary(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale": "done", "action": {"type": "final", "content": "all findings recorded"}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.SetForceClass("question")

	task, err := runner.RunTask(context.Background(), "scout the repo")
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("task.Status = %q, want %q", task.Status, TaskStatusCompleted)
	}
	if task.Summary != "all findings recorded" {
		t.Fatalf("task.Summary = %q, want final content", task.Summary)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run 'TestBuildSystemPromptRepoScoutRole|TestRunnerUsesConfiguredRole|TestRunTaskReturns' -v`
Expected: FAIL to compile — `undefined: RoleRepoScout`, `runner.Role undefined`, `runner.RunTask undefined`.

- [ ] **Step 3: Implement prompts change**

In `internal/agent/prompts.go`, add to the const block (after `RoleReviewer`):

```go
	RoleRepoScout   AgentRole = "repo_scout"
```

Add to the `roleAddenda` map (after the `RoleReviewer` entry):

```go
	RoleRepoScout: {
		focus:          "You are a repo scout. Inspect the repository with read-only tools and report findings for your assigned focus area: relevant file paths, symbols, code paths, and risks. Do not modify anything. Be concise and concrete.",
		allowedActions: []string{"tool_call", "final"},
		example:        `{"rationale": "Locate the parser implementation before reporting findings.", "action": {"type": "tool_call", "tool": "repo.search", "args": {"query": "func Parse"}}}`,
	},
```

- [ ] **Step 4: Implement runner changes**

In `internal/agent/runner.go`:

1. Add to the `Runner` struct (after `SkillIndex *skills.Index`):

```go
	// Role selects the system-prompt role addendum. Zero value behaves as
	// RoleGeneral, so existing single-agent construction is unchanged.
	// Swarm sub-runners set this to planner/repo_scout/implementer/reviewer.
	Role AgentRole
```

2. Add the helper (near `SetForceClass`):

```go
func (r *Runner) role() AgentRole {
	if r.Role == "" {
		return RoleGeneral
	}
	return r.Role
}
```

3. Replace all three `BuildSystemPrompt(RoleGeneral, ...)` call sites (in `Run` at the initial messages build, the post-plan rebuild, and the skills-changed rebuild inside the loop) with `BuildSystemPrompt(r.role(), ...)` — same remaining arguments.

4. Split `Run` into `Run` + `RunTask`. `Run` keeps its doc comment and becomes:

```go
func (r *Runner) Run(ctx context.Context, goal string) error {
	_, err := r.RunTask(ctx, goal)
	return err
}

// RunTask is Run plus access to the finished Task, so orchestrators (the
// swarm) can read a role's final summary and status without re-parsing
// the session transcript.
func (r *Runner) RunTask(ctx context.Context, goal string) (*Task, error) {
	// ... entire previous body of Run, with return values adjusted:
}
```

Inside the moved body adjust every return:
- `return r.fail(task, err)` → `return task, r.fail(task, err)` (all occurrences)
- the `ActionAnswer, ActionFinal` case's `return nil` → `return task, nil`
- the trailing `return ErrMaxIterationsExceeded` → `return task, ErrMaxIterationsExceeded`
- the `executeActions`/`executeToolCall` error returns `return r.fail(task, execErr)` → `return task, r.fail(task, execErr)` (and the same for the `err` variant)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/ -v`
Expected: PASS — the two new tests plus every pre-existing runner/prompt test (the `Run` wrapper must not break them).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/agent && go vet ./internal/agent/
git add internal/agent/prompts.go internal/agent/runner.go internal/agent/prompts_test.go internal/agent/runner_test.go
git commit -m "feat(agent): role-aware runner with RunTask and repo scout prompt"
```

---

### Task 4: Write lock (`WriteGate` hook + `swarm.WriteLock`)

**Files:**
- Modify: `internal/agent/runner.go` (add `WriteGate` interface + field; acquire in `executeToolCall` just before `tool.Handler`)
- Create: `internal/agent/swarm/lock.go`
- Test: `internal/agent/runner_test.go`, `internal/agent/swarm/lock_test.go`

**Interfaces:**
- Consumes: `Runner.RunTask` (Task 3), `registry.RiskReadOnly`.
- Produces: `agent.WriteGate interface { Acquire() (release func()) }`; `Runner.WriteGate WriteGate` field; `swarm.WriteLock` struct with method `Acquire() (release func())` implementing `agent.WriteGate`. Task 8's factory sets one shared `*swarm.WriteLock` on every swarm runner.

- [ ] **Step 1: Write the failing agent-side test**

Add to `internal/agent/runner_test.go`:

```go
type recordingGate struct {
	mu           sync.Mutex
	acquisitions int
}

func (g *recordingGate) Acquire() (release func()) {
	g.mu.Lock()
	g.acquisitions++
	return g.mu.Unlock
}

func TestWriteGateAcquiredForWriteToolsOnly(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "fs.touch", Description: "write something", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "touched"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(registry.Tool{
		Name: "fs.peek", Description: "read something", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "peeked"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	p := &scriptedProvider{responses: []string{
		`{"rationale": "read", "action": {"type": "tool_call", "tool": "fs.peek", "args": {}}}`,
		`{"rationale": "write", "action": {"type": "tool_call", "tool": "fs.touch", "args": {}}}`,
		`{"rationale": "done", "action": {"type": "final", "content": "done"}}`,
	}}
	gate := &recordingGate{}
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	runner.SetForceClass("question")
	runner.WriteGate = gate

	if err := runner.Run(context.Background(), "touch the file"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gate.acquisitions != 1 {
		t.Fatalf("gate acquired %d times, want 1 (write tool only)", gate.acquisitions)
	}
}
```

(Add `"sync"` to the test file's imports if not present.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestWriteGateAcquired -v`
Expected: FAIL to compile — `runner.WriteGate undefined`.

- [ ] **Step 3: Implement the WriteGate hook**

In `internal/agent/runner.go`:

1. Add near the `RouteResolver` interface definition:

```go
// WriteGate serialises non-read-only tool execution across concurrently
// running Runners. The swarm sets one shared gate on every role runner so
// that "only one agent may write files at a time" (docs/07 swarm safety)
// holds even if a future orchestration mode overlaps role turns.
type WriteGate interface {
	// Acquire blocks until the gate is free and returns its release func.
	Acquire() (release func())
}
```

2. Add to the `Runner` struct (after `Role AgentRole`):

```go
	WriteGate WriteGate
```

3. In `executeToolCall`, immediately before `call := registry.ToolCall{...}` / `result, execErr := tool.Handler(ctx, call)`:

```go
	if r.WriteGate != nil && tool.Risk != registry.RiskReadOnly {
		release := r.WriteGate.Acquire()
		defer release()
	}
```

- [ ] **Step 4: Run agent tests**

Run: `go test ./internal/agent/ -run TestWriteGateAcquired -v`
Expected: PASS.

- [ ] **Step 5: Write the failing swarm-side serialization test**

This test needs a scripted provider inside the swarm package; create the shared test helper file now (Task 7 reuses it):

```go
// internal/agent/swarm/provider_test.go
package swarm

import (
	"context"
	"sync"

	"marshal/internal/llm/schema"
)

// scriptedProvider mirrors the fake in internal/agent/runner_test.go: it
// returns pre-canned responses in call order and repeats the last one when
// the script runs out. Safe for concurrent Chat calls.
type scriptedProvider struct {
	mu        sync.Mutex
	responses []string
	calls     int
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) { return nil, nil }

func (p *scriptedProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}

func (p *scriptedProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *scriptedProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.mu.Unlock()

	content := ""
	switch {
	case idx < len(p.responses):
		content = p.responses[idx]
	case len(p.responses) > 0:
		content = p.responses[len(p.responses)-1]
	}

	ch := make(chan schema.ChatEvent, 2)
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: content}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
	close(ch)
	return ch, nil
}
```

Then the lock test:

```go
// internal/agent/swarm/lock_test.go
package swarm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func newLockTestState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func TestWriteLockSerialisesConcurrentWriters(t *testing.T) {
	var active int32
	var overlapped atomic.Bool

	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "fs.touch", Description: "write", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			if atomic.AddInt32(&active, 1) > 1 {
				overlapped.Store(true)
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&active, -1)
			return registry.ToolResult{Summary: "touched"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	script := []string{
		`{"rationale": "write", "action": {"type": "tool_call", "tool": "fs.touch", "args": {}}}`,
		`{"rationale": "done", "action": {"type": "final", "content": "done"}}`,
	}
	state := newLockTestState(t)
	lock := &WriteLock{}

	newWriter := func() *agent.Runner {
		r := agent.NewRunner(&scriptedProvider{responses: script}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.SetForceClass("question")
		r.WriteGate = lock
		return r
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := newWriter().Run(context.Background(), "touch"); err != nil {
				t.Errorf("Run: %v", err)
			}
		}()
	}
	wg.Wait()

	if overlapped.Load() {
		t.Fatal("two write-tool executions overlapped; WriteLock must serialise them")
	}
}
```

- [ ] **Step 6: Run to verify it fails**

Run: `go test -race ./internal/agent/swarm/ -run TestWriteLock -v`
Expected: FAIL to compile — `undefined: WriteLock`.

- [ ] **Step 7: Implement WriteLock**

```go
// internal/agent/swarm/lock.go
package swarm

import "sync"

// WriteLock is the swarm's single write path: one shared instance is set
// as the WriteGate on every role runner, so at most one agent executes a
// non-read-only tool at a time (docs/07 swarm safety rules). Read-only
// tools never touch it, so parallel scouts are unaffected.
type WriteLock struct {
	mu sync.Mutex
}

// Acquire blocks until the lock is free and returns the release func.
// It implements agent.WriteGate.
func (l *WriteLock) Acquire() (release func()) {
	l.mu.Lock()
	return l.mu.Unlock
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test -race ./internal/agent/swarm/ ./internal/agent/ -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
gofmt -w internal/agent && go vet ./internal/agent/...
git add internal/agent/runner.go internal/agent/runner_test.go internal/agent/swarm/lock.go internal/agent/swarm/lock_test.go internal/agent/swarm/provider_test.go
git commit -m "feat(swarm): write lock serialising all non-read-only tool execution"
```

---

### Task 5: Role-based route resolution (`routing.ResolveRole`)

**Files:**
- Modify: `internal/llm/routing/router.go`
- Test: `internal/llm/routing/router_test.go`

**Interfaces:**
- Consumes: existing `resolveProfileRole`, `legacyRoute`, `isNoConfiguredRoute`.
- Produces: `(*StaticRouter).ResolveRole(role AgentRole) (Route, error)` with the same implementer→legacy fallback chain `Resolve` has today; `Resolve` becomes a wrapper. Task 8 calls `ResolveRole`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/llm/routing/router_test.go` (reuse the config-construction style of the existing tests in that file):

```go
func TestResolveRoleReturnsConfiguredPreset(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "default",
		RemoteAllowed:  false,
		Presets: map[string]ModelPreset{
			"local-small": {Provider: "ollama", Model: "small", LocalOnly: true},
			"local-big":   {Provider: "ollama", Model: "big", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"default": {
				Name: "default",
				Roles: map[AgentRole]string{
					RolePlanner:     "local-big",
					RoleImplementer: "local-small",
				},
			},
		},
	})

	route, err := router.ResolveRole(RolePlanner)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if route.Role != RolePlanner || route.Preset.Model != "big" {
		t.Fatalf("route = %+v, want planner on model big", route)
	}
}

func TestResolveRoleFallsBackToImplementerForUnconfiguredRole(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "default",
		Presets: map[string]ModelPreset{
			"local-small": {Provider: "ollama", Model: "small", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"default": {
				Name:  "default",
				Roles: map[AgentRole]string{RoleImplementer: "local-small"},
			},
		},
	})

	route, err := router.ResolveRole(RoleRepoScout)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if route.Preset.Model != "small" {
		t.Fatalf("route.Preset.Model = %q, want implementer fallback \"small\"", route.Preset.Model)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/routing/ -run TestResolveRole -v`
Expected: FAIL to compile — `router.ResolveRole undefined`.

- [ ] **Step 3: Implement**

In `internal/llm/routing/router.go`, replace the body of `Resolve` and add `ResolveRole`:

```go
func (r *StaticRouter) Resolve(task TaskProfile) (Route, error) {
	return r.ResolveRole(roleForTaskClass(task.Class))
}

// ResolveRole resolves a route for an explicit agent role, with the same
// fallback chain Resolve uses: configured role preset → implementer
// preset → legacy provider. The swarm orchestrator uses this to give each
// role its own model preset (asymmetric local swarm, docs/07).
func (r *StaticRouter) ResolveRole(role AgentRole) (Route, error) {
	route, err := r.resolveProfileRole(role)
	if err == nil {
		return route, nil
	}
	if !isNoConfiguredRoute(err) {
		return Route{}, err
	}
	if role != RoleImplementer && errors.Is(err, errRoleNotConfigured) {
		fallback, fallbackErr := r.resolveProfileRole(RoleImplementer)
		if fallbackErr == nil {
			return fallback, nil
		}
		if !isNoConfiguredRoute(fallbackErr) {
			return Route{}, fallbackErr
		}
	}
	if legacy, ok := r.legacyRoute(role); ok {
		return legacy, nil
	}
	return Route{}, err
}
```

(The fallback logic is moved verbatim from the old `Resolve`; only the role now arrives as a parameter. Note: the fallback route keeps `Role = RoleImplementer` from `resolveProfileRole` — same behaviour `Resolve` has today for class-based fallbacks.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/llm/routing/ -v`
Expected: PASS — new tests and all pre-existing `Resolve` tests (behaviour must be unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/llm/routing/router.go internal/llm/routing/router_test.go
git commit -m "feat(routing): expose ResolveRole for per-role swarm presets"
```

---

### Task 6: Swarm role prompts

**Files:**
- Create: `internal/agent/swarm/prompts.go`
- Test: `internal/agent/swarm/prompts_test.go`

**Interfaces:**
- Consumes: `TaskState.Render()` (Task 1).
- Produces: `ScoutFocus{Area, Instruction string}`, `DefaultScoutFocuses []ScoutFocus` (3 entries: code, tests, docs — the docs/07 "parallel research" split), and unexported `plannerPrompt(ts *TaskState) string`, `scoutPrompt(ts *TaskState, focus ScoutFocus) string`, `implementerPrompt(ts *TaskState) string`, `reviewerPrompt(ts *TaskState) string`. Task 7's orchestrator calls all four.

- [ ] **Step 1: Write the failing tests**

```go
// internal/agent/swarm/prompts_test.go
package swarm

import (
	"strings"
	"testing"
)

func TestRolePromptsEmbedSharedTaskState(t *testing.T) {
	ts := NewTaskState("fix flaky TestParse")
	ts.SetPlan([]string{"1. reproduce", "2. fix"})

	prompts := map[string]string{
		"planner":     plannerPrompt(ts),
		"scout":       scoutPrompt(ts, DefaultScoutFocuses[0]),
		"implementer": implementerPrompt(ts),
		"reviewer":    reviewerPrompt(ts),
	}
	for name, prompt := range prompts {
		if !strings.Contains(prompt, "Goal: fix flaky TestParse") {
			t.Errorf("%s prompt missing shared task state:\n%s", name, prompt)
		}
	}
	if !strings.Contains(prompts["scout"], DefaultScoutFocuses[0].Area) {
		t.Error("scout prompt missing its focus area")
	}
	if !strings.Contains(prompts["planner"], "numbered plan") {
		t.Error("planner prompt missing plan instruction")
	}
	if !strings.Contains(prompts["reviewer"], "git.diff") {
		t.Error("reviewer prompt should point at git.diff")
	}
}

func TestDefaultScoutFocusesCoverCodeTestsDocs(t *testing.T) {
	if len(DefaultScoutFocuses) != 3 {
		t.Fatalf("len(DefaultScoutFocuses) = %d, want 3", len(DefaultScoutFocuses))
	}
	areas := map[string]bool{}
	for _, f := range DefaultScoutFocuses {
		areas[f.Area] = true
	}
	for _, want := range []string{"code", "tests", "docs"} {
		if !areas[want] {
			t.Fatalf("missing scout focus %q", want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/swarm/ -run 'TestRolePrompts|TestDefaultScoutFocuses' -v`
Expected: FAIL to compile — `undefined: plannerPrompt`, `undefined: DefaultScoutFocuses`.

- [ ] **Step 3: Implement**

```go
// internal/agent/swarm/prompts.go
package swarm

import "fmt"

// ScoutFocus is one repo scout's assigned inspection area (docs/07,
// "Parallel research": Scout A code, Scout B tests, Scout C docs).
type ScoutFocus struct {
	Area        string
	Instruction string
}

var DefaultScoutFocuses = []ScoutFocus{
	{Area: "code", Instruction: "Find the implementation files, packages, and symbols most relevant to the goal."},
	{Area: "tests", Instruction: "Find the existing tests that cover the behaviour the goal touches, and how they are run."},
	{Area: "docs", Instruction: "Find documentation, configuration, and build files related to the goal."},
}

func plannerPrompt(ts *TaskState) string {
	return "You are the swarm planner. Read the shared task state below and produce a numbered plan of 3-7 steps for accomplishing the goal. Steps must be concrete and verifiable. Respond with a final action whose content is only the numbered plan, one step per line.\n\n" + ts.Render()
}

func scoutPrompt(ts *TaskState, focus ScoutFocus) string {
	return fmt.Sprintf("You are a repo scout assigned the focus area %q. %s\n\nUse read-only tools to inspect the repository. When done, respond with a final action whose content lists your findings: relevant file paths, symbols, and anything risky or surprising. Be concise.\n\n%s", focus.Area, focus.Instruction, ts.Render())
}

func implementerPrompt(ts *TaskState) string {
	return "You are the swarm implementer. Follow the plan and use the scout findings in the shared task state below. Make the smallest change that accomplishes the goal, then run the narrowest useful validation. When done, respond with a final action summarising exactly what you changed.\n\n" + ts.Render()
}

func reviewerPrompt(ts *TaskState) string {
	return "You are the swarm reviewer. Inspect the changes made for the goal below — start with git.diff, then read the touched files as needed. Identify bugs, risks, or missed cases. Respond with a final action containing your review: either APPROVE with a one-line justification, or a list of concrete issues.\n\n" + ts.Render()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/swarm/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/swarm/prompts.go internal/agent/swarm/prompts_test.go
git commit -m "feat(swarm): role prompts embedding shared task state"
```

---

### Task 7: Sequential orchestrator with parallel scouts

**Files:**
- Create: `internal/agent/swarm/orchestrator.go`
- Test: `internal/agent/swarm/orchestrator_test.go`

**Interfaces:**
- Consumes: `agent.Runner.RunTask`, `agent.RolePlanner/RoleRepoScout/RoleImplementer/RoleReviewer` (Task 3), `TaskState` (Task 1), prompts (Task 6), `session.State.AddMessage(session.RoleSystem, text, session.ContentTypePlain|ContentTypeMarkdown)`.
- Produces: `swarm.RunnerFactory func(role agent.AgentRole, readOnly bool) (*agent.Runner, error)`; `swarm.New(state *session.State, factory RunnerFactory) *Orchestrator`; `(*Orchestrator).Run(ctx context.Context, goal string) error` and `(*Orchestrator).SetForceClass(string)` (no-op) — together these satisfy `tui.AgentRunner`, which Tasks 8-9 rely on.

Known v1 limitation (accept, do not fix here): each role turn calls `session.State.AddMessage(RoleUser, prompt)` via `RunTask`, so full role prompts appear in the transcript, and parallel scouts interleave their streaming/activity updates. That is transparent-but-noisy, which is fine for a prototype; a dedicated agent activity panel is Phase 5.

- [ ] **Step 1: Write the failing tests**

```go
// internal/agent/swarm/orchestrator_test.go
package swarm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

type factoryCall struct {
	role     agent.AgentRole
	readOnly bool
}

// newScriptedFactory returns a RunnerFactory whose runners answer with a
// single scripted final action per role, and records every factory call.
func newScriptedFactory(state *session.State, finals map[agent.AgentRole]string, calls *[]factoryCall, mu *sync.Mutex) RunnerFactory {
	return func(role agent.AgentRole, readOnly bool) (*agent.Runner, error) {
		mu.Lock()
		*calls = append(*calls, factoryCall{role: role, readOnly: readOnly})
		mu.Unlock()
		response := `{"rationale": "done", "action": {"type": "final", "content": "` + finals[role] + `"}}`
		r := agent.NewRunner(&scriptedProvider{responses: []string{response}}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.Role = role
		r.SetForceClass("question")
		return r, nil
	}
}

func TestOrchestratorRunsRolesInSequenceAndPublishesTaskState(t *testing.T) {
	state := newLockTestState(t)
	var mu sync.Mutex
	var calls []factoryCall
	finals := map[agent.AgentRole]string{
		agent.RolePlanner:     "1. reproduce\\n2. fix",
		agent.RoleRepoScout:   "parser.go is the hot spot",
		agent.RoleImplementer: "patched parser.go",
		agent.RoleReviewer:    "APPROVE: change is minimal",
	}
	o := New(state, newScriptedFactory(state, finals, &calls, &mu))

	if err := o.Run(context.Background(), "fix the parser"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantOrder := []factoryCall{
		{agent.RolePlanner, true},
		{agent.RoleRepoScout, true},
		{agent.RoleRepoScout, true},
		{agent.RoleRepoScout, true},
		{agent.RoleImplementer, false},
		{agent.RoleReviewer, true},
	}
	if len(calls) != len(wantOrder) {
		t.Fatalf("factory called %d times, want %d: %+v", len(calls), len(wantOrder), calls)
	}
	for i, want := range wantOrder {
		if calls[i] != want {
			t.Fatalf("factory call %d = %+v, want %+v", i, calls[i], want)
		}
	}

	messages := state.Messages()
	final := messages[len(messages)-1]
	for _, want := range []string{"Swarm complete", "1. reproduce", "parser.go is the hot spot", "patched parser.go", "APPROVE"} {
		if !strings.Contains(final.Content, want) {
			t.Fatalf("final swarm message missing %q:\n%s", want, final.Content)
		}
	}
}

// barrierProvider blocks every Chat call until `parties` calls have
// arrived, proving the callers run concurrently. If they run sequentially
// the first call never unblocks and the test times out.
type barrierProvider struct {
	mu      sync.Mutex
	arrived int
	parties int
	release chan struct{}
	final   string
}

func (p *barrierProvider) Name() string                                        { return "barrier" }
func (p *barrierProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) { return nil, nil }
func (p *barrierProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}
func (p *barrierProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}
func (p *barrierProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.mu.Lock()
	p.arrived++
	if p.arrived == p.parties {
		close(p.release)
	}
	p.mu.Unlock()

	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	ch := make(chan schema.ChatEvent, 2)
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.final}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
	close(ch)
	return ch, nil
}

func TestOrchestratorRunsScoutsInParallel(t *testing.T) {
	state := newLockTestState(t)
	scoutBarrier := &barrierProvider{
		parties: len(DefaultScoutFocuses),
		release: make(chan struct{}),
		final:   `{"rationale": "done", "action": {"type": "final", "content": "found"}}`,
	}
	factory := func(role agent.AgentRole, readOnly bool) (*agent.Runner, error) {
		var r *agent.Runner
		if role == agent.RoleRepoScout {
			r = agent.NewRunner(scoutBarrier, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		} else {
			r = agent.NewRunner(&scriptedProvider{responses: []string{
				`{"rationale": "done", "action": {"type": "final", "content": "ok"}}`,
			}}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		}
		r.Role = role
		r.SetForceClass("question")
		r.MaxRetries = 0
		return r, nil
	}
	o := New(state, factory)

	done := make(chan error, 1)
	go func() { done <- o.Run(context.Background(), "goal") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orchestrator deadlocked: scouts did not run in parallel")
	}
}

func TestOrchestratorContinuesWhenAScoutFails(t *testing.T) {
	state := newLockTestState(t)
	scoutCount := 0
	var mu sync.Mutex
	factory := func(role agent.AgentRole, readOnly bool) (*agent.Runner, error) {
		response := `{"rationale": "done", "action": {"type": "final", "content": "ok"}}`
		p := &scriptedProvider{responses: []string{response}}
		if role == agent.RoleRepoScout {
			mu.Lock()
			scoutCount++
			failing := scoutCount == 1
			mu.Unlock()
			if failing {
				// Malformed forever -> RunTask exhausts iterations and errors.
				p = &scriptedProvider{responses: []string{"not json at all"}}
			}
		}
		r := agent.NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.Role = role
		r.SetForceClass("question")
		r.MaxToolIterations = 2
		r.MaxRetries = 0
		return r, nil
	}
	o := New(state, factory)

	if err := o.Run(context.Background(), "goal"); err != nil {
		t.Fatalf("Run should tolerate scout failure, got: %v", err)
	}
	messages := state.Messages()
	final := messages[len(messages)-1].Content
	if !strings.Contains(final, "scout failed") {
		t.Fatalf("final message should record the failed scout:\n%s", final)
	}
}

func TestOrchestratorAbortsWhenPlannerFails(t *testing.T) {
	state := newLockTestState(t)
	var mu sync.Mutex
	var calls []factoryCall
	factory := func(role agent.AgentRole, readOnly bool) (*agent.Runner, error) {
		mu.Lock()
		calls = append(calls, factoryCall{role: role, readOnly: readOnly})
		mu.Unlock()
		r := agent.NewRunner(&scriptedProvider{responses: []string{"garbage"}}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.Role = role
		r.SetForceClass("question")
		r.MaxToolIterations = 2
		r.MaxRetries = 0
		return r, nil
	}
	o := New(state, factory)

	if err := o.Run(context.Background(), "goal"); err == nil {
		t.Fatal("Run should fail when the planner fails")
	}
	for _, c := range calls {
		if c.role != agent.RolePlanner {
			t.Fatalf("no role beyond planner should run, but factory built %q", c.role)
		}
	}
	_ = errors.New // keep import if unused after edits
}
```

(Remove the `_ = errors.New` line and the `errors` import if unused once the file compiles.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/agent/swarm/ -run TestOrchestrator -v`
Expected: FAIL to compile — `undefined: New`, `undefined: RunnerFactory`.

- [ ] **Step 3: Implement the orchestrator**

```go
// internal/agent/swarm/orchestrator.go
package swarm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"marshal/internal/agent"
	"marshal/internal/app/session"
)

// RunnerFactory builds a role-specific agent.Runner. readOnly selects the
// read-only registry view. Implementations must return a fresh Runner per
// call: Runner tracks per-turn state (call history, loop nudge), so
// instances cannot be shared between concurrent scouts.
type RunnerFactory func(role agent.AgentRole, readOnly bool) (*agent.Runner, error)

// Orchestrator drives the Milestone O sequential swarm
// (docs/07, "First swarm milestone"):
//
//	planner → parallel read-only repo scouts → implementer → reviewer
//
// sharing one TaskState blackboard. It satisfies the TUI's AgentRunner
// interface so /swarm dispatch reuses the existing agent-turn plumbing.
type Orchestrator struct {
	State        *session.State
	NewRunner    RunnerFactory
	ScoutFocuses []ScoutFocus
}

func New(state *session.State, factory RunnerFactory) *Orchestrator {
	return &Orchestrator{State: state, NewRunner: factory, ScoutFocuses: DefaultScoutFocuses}
}

// SetForceClass satisfies tui.AgentRunner. Swarm roles fix their own task
// classes, so forcing has no effect.
func (o *Orchestrator) SetForceClass(string) {}

func (o *Orchestrator) Run(ctx context.Context, goal string) error {
	ts := NewTaskState(goal)
	o.announce("Swarm run started: planner → repo scouts → implementer → reviewer.")

	// 1. Planner (read-only): produces the shared plan.
	o.announce("Swarm: planner")
	plannerTask, err := o.runRole(ctx, agent.RolePlanner, true, plannerPrompt(ts))
	if err != nil {
		o.announce("Swarm aborted: planner failed.")
		return err
	}
	ts.SetPlan(planLines(plannerTask.Summary))

	// 2. Repo scouts (read-only, parallel). Runners are constructed before
	// the goroutines start so the factory is never called concurrently.
	focuses := o.focuses()
	o.announce(fmt.Sprintf("Swarm: %d repo scouts (parallel, read-only)", len(focuses)))
	type scoutJob struct {
		focus  ScoutFocus
		runner *agent.Runner
	}
	jobs := make([]scoutJob, 0, len(focuses))
	for _, focus := range focuses {
		runner, err := o.NewRunner(agent.RoleRepoScout, true)
		if err != nil {
			o.announce("Swarm aborted: could not build repo scout.")
			return err
		}
		jobs = append(jobs, scoutJob{focus: focus, runner: runner})
	}
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(j scoutJob) {
			defer wg.Done()
			task, err := j.runner.RunTask(ctx, scoutPrompt(ts, j.focus))
			if err != nil {
				ts.AddFinding(Finding{Agent: "repo_scout", Area: j.focus.Area, Content: "scout failed: " + err.Error()})
				return
			}
			ts.AddFinding(Finding{Agent: "repo_scout", Area: j.focus.Area, Content: task.Summary})
		}(job)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 3. Implementer: the only writer. Its runner holds the full registry
	// and the shared WriteGate (set by the factory).
	o.announce("Swarm: implementer")
	implTask, err := o.runRole(ctx, agent.RoleImplementer, false, implementerPrompt(ts))
	if err != nil {
		o.announce("Swarm aborted: implementer failed.")
		return err
	}
	ts.AddPatchNote(implTask.Summary)

	// 4. Reviewer (read-only). A reviewer failure is reported, not fatal:
	// the implementer's work is already in the working tree.
	o.announce("Swarm: reviewer")
	reviewTask, err := o.runRole(ctx, agent.RoleReviewer, true, reviewerPrompt(ts))
	if err != nil {
		ts.SetFinalSummary("Reviewer failed: " + err.Error())
	} else {
		ts.SetFinalSummary(reviewTask.Summary)
	}

	o.State.AddMessage(session.RoleSystem, "Swarm complete.\n\n"+ts.Render(), session.ContentTypeMarkdown)
	return nil
}

func (o *Orchestrator) runRole(ctx context.Context, role agent.AgentRole, readOnly bool, prompt string) (*agent.Task, error) {
	runner, err := o.NewRunner(role, readOnly)
	if err != nil {
		return nil, err
	}
	return runner.RunTask(ctx, prompt)
}

func (o *Orchestrator) focuses() []ScoutFocus {
	if len(o.ScoutFocuses) > 0 {
		return o.ScoutFocuses
	}
	return DefaultScoutFocuses
}

func (o *Orchestrator) announce(text string) {
	o.State.AddMessage(session.RoleSystem, text, session.ContentTypePlain)
}

// planLines splits the planner's final answer into trimmed non-empty lines.
func planLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/agent/swarm/ -v`
Expected: PASS (all swarm tests: state, lock, prompts, orchestrator).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/swarm && go vet ./internal/agent/swarm/
git add internal/agent/swarm/orchestrator.go internal/agent/swarm/orchestrator_test.go
git commit -m "feat(swarm): sequential orchestrator with parallel read-only scouts"
```

---

### Task 8: App wiring (role resolver + swarm runner factory)

**Files:**
- Modify: `internal/app/app.go` (`routedProviderResolver` at lines ~95-130, `buildAgentRunner` at lines ~147-196, `Run` call site at lines ~263-278)

**Interfaces:**
- Consumes: `routing.(*StaticRouter).ResolveRole` (Task 5), `registry.ReadOnlyView` (Task 2), `swarm.New`/`swarm.RunnerFactory`/`swarm.WriteLock` (Tasks 4, 7), `agent.Runner` fields from Tasks 3-4.
- Produces: `(*routedProviderResolver).ResolveRole(role routing.AgentRole) (routing.Route, provider.Provider, error)`; `buildAgentRunner` returns an extra `*swarm.Orchestrator`; `Run` passes it to the TUI via `tui.WithSwarmRunner` (added in Task 9 — see build note in Step 4).

- [ ] **Step 1: Make the provider cache concurrency-safe and add ResolveRole**

In `internal/app/app.go`, change `routedProviderResolver` (add `"sync"` to imports if missing):

```go
type routedProviderResolver struct {
	router    *routing.StaticRouter
	cfg       config.Config
	mu        sync.Mutex // guards providers; swarm may resolve roles from concurrent paths
	providers map[string]provider.Provider
}

func (r *routedProviderResolver) Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error) {
	route, err := r.router.Resolve(task)
	if err != nil {
		return routing.Route{}, nil, err
	}
	p, err := r.providerFor(route)
	if err != nil {
		return routing.Route{}, nil, err
	}
	return route, p, nil
}

// ResolveRole is Resolve for an explicit swarm role instead of a task class.
func (r *routedProviderResolver) ResolveRole(role routing.AgentRole) (routing.Route, provider.Provider, error) {
	route, err := r.router.ResolveRole(role)
	if err != nil {
		return routing.Route{}, nil, err
	}
	p, err := r.providerFor(route)
	if err != nil {
		return routing.Route{}, nil, err
	}
	return route, p, nil
}

func (r *routedProviderResolver) providerFor(route routing.Route) (provider.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.providers[route.Preset.Provider]; ok {
		return existing, nil
	}
	providerConfig, ok := r.cfg.Providers[route.Preset.Provider]
	if !ok {
		return nil, fmt.Errorf("routing provider %q is not configured", route.Preset.Provider)
	}
	p, err := provider.NewFromConfig(route.Preset.Provider, providerConfig)
	if err != nil {
		return nil, err
	}
	r.providers[route.Preset.Provider] = p
	return p, nil
}
```

(This replaces the current inline body of `Resolve`; `newRoutedProviderResolver` is unchanged.)

- [ ] **Step 2: Build the swarm orchestrator in buildAgentRunner**

Change the signature and tail of `buildAgentRunner`:

```go
func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, error) {
```

Every existing `return nil, nil, err` inside it becomes `return nil, nil, nil, err`. Before the final return, add:

```go
	swarmRunner := buildSwarmRunner(ctx, cfg, state, reg, pol, resolver, database, projectID, skillIndex)
	return runner, reg, swarmRunner, nil
```

Add the new function below `buildAgentRunner` (import `"marshal/internal/agent/swarm"`):

```go
// buildSwarmRunner wires the Milestone O swarm: every role runner shares
// the session state, policy engine, and one WriteLock; read-only roles get
// the filtered registry view; each role's provider/model comes from the
// routing profile via ResolveRole (falling back to the implementer preset
// for unconfigured roles).
func buildSwarmRunner(ctx context.Context, cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index) *swarm.Orchestrator {
	readOnlyReg := registry.ReadOnlyView(reg)
	gate := &swarm.WriteLock{}
	memory := &dbMemoryProvider{db: database}

	factory := func(role agent.AgentRole, readOnly bool) (*agent.Runner, error) {
		// agent.AgentRole and routing.AgentRole share string values
		// ("planner", "repo_scout", "implementer", "reviewer").
		route, p, err := resolver.ResolveRole(routing.AgentRole(role))
		if err != nil {
			return nil, err
		}
		toolReg := reg
		if readOnly {
			toolReg = readOnlyReg
		}
		r := agent.NewRunner(p, toolReg, pol, state, route.Preset.Model)
		r.Role = role
		r.WriteGate = gate
		r.SkillIndex = skillIndex
		r.MemoryProvider = memory
		r.ProjectID = projectID
		r.RequestTimeout = 60 * time.Second
		// Swarm role prompts embed the shared plan, so skip the per-turn
		// classify/plan pass (class "question" bypasses planning).
		r.SetForceClass("question")
		if route.Preset.ToolCalling == "json" && p.Capabilities(ctx).JSONMode {
			r.ResponseFormat = &schema.ResponseFormat{Type: "json_object"}
		}
		if cfg.Agent.MaxToolIterations > 0 {
			r.MaxToolIterations = cfg.Agent.MaxToolIterations
		}
		if cfg.Agent.MaxRetries > 0 {
			r.MaxRetries = cfg.Agent.MaxRetries
		}
		return r, nil
	}
	return swarm.New(state, factory)
}
```

- [ ] **Step 3: Update the Run() call site**

In `Run` (around line 263):

```go
	var runner *agent.Runner
	var toolReg *registry.Registry
	var swarmRunner *swarm.Orchestrator
	runner, toolReg, swarmRunner, err = buildAgentRunner(ctx, cfg, state, database, projectID, skillIndex)
```

Do NOT add the `tuiOpts` append here — `tui.WithSwarmRunner` does not exist until Task 9, which adds the single line `tuiOpts = append(tuiOpts, tui.WithSwarmRunner(ctx, swarmRunner))` to this block. The unused `swarmRunner` variable is still consumed by that future line; if the compiler complains about it being unused at this point, add a temporary `_ = swarmRunner` and remove it in Task 9.

- [ ] **Step 4: Verify it builds and existing app tests pass**

Run: `go build ./... && go test ./internal/app/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app && go vet ./internal/app/...
git add internal/app/app.go
git commit -m "feat(app): wire swarm orchestrator with per-role routing and shared write lock"
```

---

### Task 9: `/swarm` command and TUI dispatch

**Files:**
- Modify: `internal/commands/commands.go` (add `swarm` entry to `RegisterAll`)
- Modify: `internal/app/tui/model.go` (add `swarmRunner` field, `WithSwarmRunner` option, `case "swarm":` in `dispatchCommand`; plus the `tuiOpts` line deferred from Task 8)
- Test: `internal/commands/commands_test.go`, `internal/app/tui/model_test.go` (create if absent — check first; TUI tests may live in another `_test.go` file in that package)

**Interfaces:**
- Consumes: `tui.AgentRunner` (existing interface — `Run(ctx, goal) error`, `SetForceClass(string)`), satisfied by `*swarm.Orchestrator` from Task 7; `runAgentCmd`, `tickCmd` (existing).
- Produces: `tui.WithSwarmRunner(ctx context.Context, runner AgentRunner) Option`; `/swarm <goal>` command.

- [ ] **Step 1: Write the failing commands test**

Add to `internal/commands/commands_test.go` (match the file's existing style for RegisterAll setup):

```go
func TestRegisterAllIncludesSwarmCommand(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	cmd, ok := cmdReg.Lookup("swarm")
	if !ok {
		t.Fatal("swarm command not registered")
	}
	if cmd.Args != "<goal>" {
		t.Fatalf("swarm Args = %q, want \"<goal>\"", cmd.Args)
	}
	// The handler is a no-op; the TUI special-cases dispatch like /ask.
	if got := cmd.Handler(nil, []string{"fix", "bug"}); got != "" {
		t.Fatalf("swarm handler returned %q, want empty", got)
	}
}
```

- [ ] **Step 2: Write the failing TUI test**

Add to the TUI package tests (`internal/app/tui/model_test.go`, or the existing model test file if one exists — check with `ls internal/app/tui/*_test.go` and match its fixture style):

```go
type fakeSwarmRunner struct {
	mu    sync.Mutex
	goals []string
}

func (f *fakeSwarmRunner) Run(ctx context.Context, goal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goals = append(f.goals, goal)
	return nil
}

func (f *fakeSwarmRunner) SetForceClass(string) {}

func TestSwarmCommandDispatchesGoalToSwarmRunner(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	fake := &fakeSwarmRunner{}
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatal(err)
	}
	model := New(state,
		WithCommandRegistry(cmdReg),
		WithSwarmRunner(context.Background(), fake),
	)

	_, cmd := model.dispatchCommand("/swarm add a regression test")
	if cmd == nil {
		t.Fatal("dispatchCommand returned nil cmd")
	}
	if !model.busy {
		t.Fatal("model should be busy while the swarm runs")
	}

	// Execute the batched commands; one of them runs the swarm.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", msg)
	}
	for _, sub := range batch {
		if sub != nil {
			_ = sub()
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.goals) != 1 || fake.goals[0] != "add a regression test" {
		t.Fatalf("swarm runner goals = %v, want [\"add a regression test\"]", fake.goals)
	}
}

func TestSwarmCommandWithoutGoalShowsUsage(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatal(err)
	}
	model := New(state,
		WithCommandRegistry(cmdReg),
		WithSwarmRunner(context.Background(), &fakeSwarmRunner{}),
	)

	_, _ = model.dispatchCommand("/swarm")
	messages := state.Messages()
	last := messages[len(messages)-1]
	if !strings.Contains(last.Content, "Usage: /swarm <goal>") {
		t.Fatalf("expected usage message, got %q", last.Content)
	}
	if model.busy {
		t.Fatal("model must not be busy after a usage error")
	}
}
```

Note on the `model.dispatchCommand` call: `New` returns a `Model` value and `dispatchCommand` has a pointer receiver, so assign `model := New(...)` and call `model.dispatchCommand(...)` on the addressable variable. Imports needed: `context`, `strings`, `sync`, `time`, `testing`, `tea "github.com/charmbracelet/bubbletea"`, `"marshal/internal/app/config"`, `"marshal/internal/app/session"`, `"marshal/internal/commands"`, `"marshal/internal/tools/registry"` — trim to whatever the existing test file already imports.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/commands/ ./internal/app/tui/ -run 'Swarm' -v`
Expected: FAIL to compile — `undefined: WithSwarmRunner`, and the commands test fails with "swarm command not registered".

- [ ] **Step 4: Implement**

1. `internal/commands/commands.go` — add to the command list in `RegisterAll` (after the `auto` entry, matching the surrounding literal style):

```go
		{
			Name:        "swarm",
			Description: "Run a goal through the swarm (planner → scouts → implementer → reviewer)",
			Args:        "<goal>",
			Handler:     func(state *session.State, args []string) string { return "" },
		},
```

2. `internal/app/tui/model.go` — add the field to `Model` (after `runner AgentRunner`):

```go
	swarmRunner    AgentRunner
```

Add the option (after `WithRunner`):

```go
// WithSwarmRunner configures the TUI to route /swarm <goal> submissions
// through runner (the swarm orchestrator). ctx follows the same rules as
// WithRunner's: pass the cancellable program context so Ctrl+C and /stop
// cancel an in-flight swarm run.
func WithSwarmRunner(ctx context.Context, runner AgentRunner) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.swarmRunner = runner
	}
}
```

Add to the `switch cmd.Name` in `dispatchCommand` (after the `auto` case):

```go
	case "swarm":
		goal := strings.TrimSpace(strings.Join(args, " "))
		if goal == "" {
			m.state.AddMessage(session.RoleSystem, "Usage: /swarm <goal>", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		if m.swarmRunner == nil {
			m.state.AddMessage(session.RoleSystem, "Swarm is not available (agent failed to initialise).", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		if m.busy {
			return m, nil
		}
		m.busy = true
		agentCtx, cancel := context.WithCancel(m.ctx)
		m.agentCancel = cancel
		return m, tea.Batch(runAgentCmd(agentCtx, m.swarmRunner, goal), tickCmd())
```

3. `internal/app/app.go` — add the line deferred from Task 8, immediately after the `tui.WithRunner(ctx, runner)` append:

```go
		tuiOpts = append(tuiOpts, tui.WithSwarmRunner(ctx, swarmRunner))
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/commands/ ./internal/app/... && go build ./...`
Expected: PASS, clean build (the Task 8 wiring now compiles end to end).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/commands internal/app && go vet ./...
git add internal/commands/commands.go internal/commands/commands_test.go internal/app/tui/model.go internal/app/tui/model_test.go internal/app/app.go
git commit -m "feat(tui): /swarm command dispatching to the swarm orchestrator"
```

(If `internal/app/tui/model.go` still carries unrelated pre-existing modifications at this point, stop and ask the user before committing it.)

---

### Task 10: Docs, checklist, and final verification

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md:146-155` (tick Milestone O boxes)
- Modify: `CLAUDE.md` (milestone status line + package list)

- [ ] **Step 1: Tick the Milestone O checklist**

In `docs/10-mvp-implementation-checklist.md`, change every `- [ ]` under `## Milestone O: First swarm prototype` to `- [x]`:

```markdown
## Milestone O: First swarm prototype

- [x] Shared task state
- [x] Planner role
- [x] Repo Scout role
- [x] Implementer role
- [x] Reviewer role
- [x] Sequential orchestration
- [x] Read-only parallel scout experiment
- [x] Write lock
```

- [ ] **Step 2: Update CLAUDE.md**

In the Architecture section, update the milestone status sentence to state that Milestones A through O are complete (swarm prototype included) and that MCP/plugin support remains. Add one line to the package list after `internal/agent/`:

```
internal/agent/swarm/                 — shared task state, write lock, swarm orchestrator
```

- [ ] **Step 3: Full verification**

Run each; all must pass:

```bash
gofmt -l .            # expect: no output
go vet ./...          # expect: no output
CGO_ENABLED=1 go build ./cmd/marshal
go test ./...
go test -race ./internal/agent/... ./internal/app/tui/
```

- [ ] **Step 4: Commit**

```bash
git add docs/10-mvp-implementation-checklist.md CLAUDE.md
git commit -m "docs: mark Milestone O (first swarm prototype) complete"
```

---

## Self-review notes

- **Spec coverage:** Shared task state → Task 1; Planner/Repo Scout/Implementer/Reviewer roles → Tasks 3, 6, 7 (prompts + role runners + orchestration); Sequential orchestration → Task 7; Read-only parallel scout experiment → Tasks 2, 6, 7 (three parallel scouts on `ReadOnlyView` registries); Write lock → Task 4; user entry point → Task 9; per-role model presets (docs/07 asymmetric swarm) → Tasks 5, 8.
- **Deliberately out of scope (Phase 5, not Milestone O):** Tester role, agent activity panel, agent budgets, debate/review mode, specialist routing, task-state persistence to SQLite.
- **Known accepted limitations:** role prompts appear verbatim in the transcript; parallel scouts interleave activity-spinner updates; the reviewer sees the diff via `git.diff` rather than a structured patch list.
