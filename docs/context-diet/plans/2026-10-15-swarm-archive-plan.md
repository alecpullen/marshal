# Swarm Archive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove swarm from main across every surface, keeping it recoverable on an `archive/swarm` branch.

**Architecture:**
- Archive first.
- Then move the two pieces the SDD pipeline and `agent.run` share:
  - `WriteLock` → `internal/agent`
  - the runner factory → `internal/pipeline`, typed over the pipeline's own scope
- Then delete outward from the core, so every commit builds: agent/app, TUI, ACP plus session and config, then the web bridge and UI.

**Tech Stack:** Go (cgo), Bubble Tea, ACP JSON-RPC, Svelte 5 + Vite + vitest, a stdlib-only web bridge.

**Spec:** `docs/context-diet/swarm-archive-design.md`

## Global Constraints

- **Base:** lands after Tier 2a (`docs/context-diet/plans/2026-10-14-tier2a-tool-surface-plan.md`). 2a makes `test.run` internal and has `TesterView` re-expose it; this plan deletes both.
  - Independent of Tier 2b apart from `internal/app/app.go` merge conflicts, so either order works. Anchors are quoted code text.
- **Keep:**
  - the castlist preflight panel and `routing.SDDCastRoles`
  - `repo_scout` as a live routing role (question-class turns route through it)
  - the `planner` and `tester` role names in `internal/llm/routing/types.go` and config
- **Old config files with a `[swarm]` section must keep loading.** The TOML loader ignores unknown keys; don't add strict decoding.
- **`web/` stays stdlib-only** (`TestWebIsStdlibOnly`).
- A change under `web/ui/src` is only visible after `cd web/ui && npm run build` and a `webbridge` rebuild. `web/bridge/static` is tracked, so rebuilt assets go in the same commit.
- **Deletion rule:** fix compile errors only by deleting or redirecting callers this plan names, or callers that exist only to serve a deleted swarm symbol. Never stub a deleted symbol back in.
- Build and test with `CGO_ENABLED=1`. Run `gofmt -w` on changed Go files.
- Known environmental failures on this machine: see the Tier 1 plan's Global Constraints.
- One commit per task, in `area: summary` style.

## Review Focus

1. **A user's `~/.config/marshal/config.toml` that still has `[swarm.budget]` keys must load without error.** Task 4 adds `TestConfigWithRetiredSwarmSectionLoads`.
2. **An older web bundle or ACP client that calls `session/swarm_start` must get a JSON-RPC "method not found" error promptly, not a hang.** Task 4 adds `TestHostRegistersNoSwarmMethods`; the generic unknown-method path is already covered by `TestServerUnknownMethod`.
3. **Two `agent.run` children writing concurrently must still be serialized by the shared write lock after it moves.** Task 2 adds `TestWriteLockSerializes`.
4. **The `/sdd` preflight must still open with the SDD cast and apply role overrides.** Task 3 adds `TestSDDPreflightStillUsesSDDCast`.
5. **The agents roster panel must still build for a profile that binds the retired `planner`/`tester` roles.** Task 3 adds `TestAgentsPanelBuildsWithRetiredRoleBindings`.

---

### Task 1: Create the archive branch

**Files:** none (git only).

- [ ] **Step 1: Confirm the base.** Run `git status --short` and `git log --oneline -1`. The tree must be clean, apart from untracked files that aren't part of this work, and `HEAD` must be the post-Tier-2a commit this plan starts from. Record that SHA.

- [ ] **Step 2: Create the branch.**

```bash
git branch archive/swarm HEAD
git log --oneline -1 archive/swarm
```

Expected: `archive/swarm` points at the recorded SHA.

- [ ] **Step 3: Ask before pushing.** Pushing is outward-facing. Ask the user: "Push `archive/swarm` (at <SHA>) to origin?" Only on a yes, run `git push origin archive/swarm` and record the remote SHA. On a no, note in the final report that the branch is local only.

There's nothing to commit for this task.

---

### Task 2: Move the shared pieces; delete the swarm core, `TesterView` and `test.run`

**Files:**
- Create: `internal/agent/writelock.go`, `internal/agent/writelock_test.go` (port of `internal/agent/swarm/lock.go` and `lock_test.go`)
- Delete: `internal/agent/swarm/` (the whole package)
- Modify: `internal/pipeline/exec_context.go` (add `RunnerFactory`), `internal/pipeline/dispatch.go`:
  - `Factory RunnerFactory`
  - `exec` seam and `run`/`runExec` take `RegistryScope`
  - delete `pipelineScope`
  - `Implement` and `Review` use `ScopeFull`/`ScopeFallback`/`ScopeArtifactWriter`
- Modify: `internal/pipeline/dispatch_test.go`, `controller_test.go`, `integration_test.go` (swarm scope → pipeline scope)
- Modify: `internal/app/app.go`:
  - delete `buildSwarmRunner` and the `swarmOverrideFactory` closure
  - drop the `*swarm.Orchestrator` and `tui.SwarmOverrideFactory` results from `buildAgentRunner` / `buildAgentRunnerWithLock`, and every caller's destructuring
  - `roleRunnerSpec`: delete `testerReg`; `newRunner(role agent.AgentRole, scope pipeline.RegistryScope)`
  - the pipeline factory closure takes `pipeline.RegistryScope`
  - `*swarm.WriteLock` → `*agent.WriteLock`
  - delete the `tui.WithSwarmRunner` / `tui.WithSwarmOverrideFactory` options, the `SwarmRunner`/`SwarmOverrideFactory` fields of the session-swap result, and the reload code that copies `newSwarmRunner`/`newSwarmOverrideFactory`
- Modify: `internal/app/runtime.go`:
  - `Runtime`: delete `SwarmRunner`, `SwarmOverrideFactory`; `WriteLock *agent.WriteLock`
  - `NewSession`: drop the `*swarm.Orchestrator` and `tui.SwarmOverrideFactory` results
  - `&swarm.WriteLock{}` → `&agent.WriteLock{}`
- Modify: `internal/acp/host.go`. Delete the `swarmRunner` boxing block, from the comment `// Box rt.SwarmRunner (a concrete *swarm.Orchestrator) into the` through `swarmRunner = rt.SwarmRunner` and its closing brace, plus the `SwarmRunner:     swarmRunner,` field. Task 4 removes the ACP swarm methods; until then `SwarmStart` reports swarm unavailable.
- Modify: `internal/tools/registry/view.go` (delete `TesterView` and `testerTestRunTool`), `internal/tools/native/command.go` (delete `testRunTool`, its args type, and `defaultTestTimeout` if unused), `internal/tools/native/native.go` (remove `tools.testRunTool()` from `all`)
- Modify: the `test.run` name sets, keeping display-map labels for old transcripts:
  - `internal/tools/policy/policy.go`: `"shell.run", "test.run"` cases → `"shell.run"`
  - `internal/permissions/rules.go`: keep `"test.run"` mapping to `"shell"`, since persisted rules may name it; no change
  - `internal/agent/progress.go`: `isVerification` drops `e.name == "test.run" ||`
  - `internal/agent/toolargs.go`, `internal/viewmodel/tasks.go` `IsShellFamily`: leave `test.run` in place for history rendering
- Modify: `internal/agent/prompts.go` (delete the `RolePlanner`, `RoleTester` and `RoleRepoScout` entries from `roleAddenda`; keep the constants), `internal/agent/protocol.go:33` (comment: drop `and-swarm` from the docs path)
- Modify, the minimum so `internal/app/tui` stops importing the deleted package (the rest of the TUI is Task 3):
  - in `internal/app/tui/model.go`, delete the `SwarmOverrideFactory` type, the `RoleOverrideRunner` interface, the `swarmOverrideFactory` field, `WithSwarmOverrideFactory`, the `SessionSwapResult.SwarmOverrideFactory` field, the `restoreRunner` field and its uses (including the `CancelMsg` block that starts `if m.restoreRunner != nil {`), and the `castlist.StartMsg` block that starts `if ror, ok := run.runner.(RoleOverrideRunner); ok`
  - in `internal/app/tui/commands_dispatch.go`, delete the `if snap.SwarmOverrideFactory != nil { … }` copy
- Test: `internal/agent/writelock_test.go`, `internal/pipeline/dispatch_test.go`, `internal/app/app_test.go`

**Interfaces:**
- Produces:
  - `agent.WriteLock` with `Acquire() (release func())`. It's idempotent and implements `agent.WriteGate`.
  - `pipeline.RunnerFactory func(role agent.AgentRole, scope pipeline.RegistryScope) (*agent.Runner, error)`
  - `pipeline.Dispatcher.Factory` has type `pipeline.RunnerFactory`
  - `buildAgentRunner` returns `(*agent.Runner, *registry.Registry, *mcp.Manager, *snapshot.Rooted, *native.JobManager, func(), agent.SubagentRunnerFactory, *lsp.Handle, func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner, sddauthor.Factory, error)`
  - `Runtime.NewSession` returns `(*session.State, *agent.Runner, func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner, sddauthor.Factory, *registry.Registry, error)`

- [ ] **Step 1: Write the failing tests.** Create `internal/agent/writelock_test.go`. Port every test from `internal/agent/swarm/lock_test.go` (change the package to `agent` and `swarm.WriteLock` to `WriteLock`), then add:

```go
// Review focus 3: the shared gate still serializes concurrent writers.
func TestWriteLockSerializes(t *testing.T) {
	var l WriteLock
	release := l.Acquire()
	acquired := make(chan struct{})
	go func() {
		r := l.Acquire()
		close(acquired)
		r()
	}()
	select {
	case <-acquired:
		t.Fatal("second Acquire succeeded while the lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	release() // idempotent
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second Acquire never succeeded after release")
	}
}
```

  Add to `internal/pipeline/dispatch_test.go`:

```go
func TestDispatcherPassesPipelineScopes(t *testing.T) {
	var got []RegistryScope
	d := Dispatcher{exec: func(ctx context.Context, role agent.AgentRole, scope RegistryScope, prompt string) (string, error) {
		got = append(got, scope)
		return "", errors.New("stop")
	}}
	_, _ = d.Implement(context.Background(), agent.RoleSDDImplementer, "l", "p")
	d.FallbackAllowedFiles = []string{"a.go"}
	_, _ = d.Implement(context.Background(), agent.RoleSDDImplementer, "l", "p")
	_, _ = d.Review(context.Background(), agent.RoleSDDReviewer, "l", "p")
	want := []RegistryScope{ScopeFull, ScopeFallback, ScopeArtifactWriter}
	if len(got) != len(want) {
		t.Fatalf("scopes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scopes = %v, want %v", got, want)
		}
	}
}
```

  Add to `internal/app/app_test.go`:

```go
func TestRuntimeHasNoTestRunTool(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755)
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	if _, ok := rt.Runner.Registry.Lookup("test.run"); ok {
		t.Fatal("test.run must be gone with swarm")
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run TestWriteLock -count=1; CGO_ENABLED=1 go test ./internal/pipeline/ -run TestDispatcherPassesPipelineScopes -count=1; CGO_ENABLED=1 go test ./internal/app/ -run TestRuntimeHasNoTestRunTool -count=1`
Expected: compile failures (`WriteLock` undefined in `agent`; `exec` has the wrong scope type) and a `test.run` failure.

- [ ] **Step 3: Implement.**
  1. Create `internal/agent/writelock.go`:

```go
package agent

import "sync"

// WriteLock is the single write path shared by a parent runner, its
// agent.run children and pipeline role runners: at most one runner executes
// a non-read-only tool at a time. Read-only tools never touch it.
type WriteLock struct {
	mu sync.Mutex
}

// Acquire blocks until the lock is free and returns an idempotent release
// func. It implements WriteGate.
func (l *WriteLock) Acquire() (release func()) {
	l.mu.Lock()
	var once sync.Once
	return func() { once.Do(l.mu.Unlock) }
}
```

  2. In `internal/pipeline/exec_context.go`, after the `RegistryScope` constants, add:

```go
// RunnerFactory builds a role-bound runner for one dispatch. Implementations
// must return a fresh Runner per call.
type RunnerFactory func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error)
```

     Replace the comment line `// mirroring swarm.RegistryScope for the pipeline's simpler read-only /` and its continuation with `// for the pipeline's read-only / full / artifact-writer / fallback distinction.`
  3. In `internal/pipeline/dispatch.go`:
     - `Factory swarm.RunnerFactory` → `Factory RunnerFactory`
     - every `swarm.RegistryScope` → `RegistryScope`
     - `swarm.ScopeFull`/`ScopeFallback`/`ScopeArtifactWriter` → the unqualified pipeline constants
     - in `runExec`, replace `regScope := pipelineScope(scope)` with `regScope := scope`
     - delete `pipelineScope` and the `swarm` import

     Update the three pipeline test files the same way.
  4. Apply the `app.go`, `runtime.go` and `acp/host.go` edits from the Files list. In `roleRunnerSpec.newRunner`, the scope switch becomes:

```go
	toolReg := s.reg
	if scope == pipeline.ScopeReadOnly {
		toolReg = s.readOnlyReg
	}
```

  5. `git rm -r internal/agent/swarm`.
  6. Delete `TesterView`, `testerTestRunTool`, `testRunTool` and the name-set entries as listed. Delete tests whose only subject is `test.run` or `TesterView`.
  7. In `internal/tools/registry/view_test.go`, delete Tier 2a's `TestTesterViewExposesInternalTestRun`.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/agent/... ./internal/pipeline/ ./internal/tools/... ./internal/app/ ./internal/acp/ ./internal/app/tui/ -count=1`
Expected: `go build ./...` succeeds, because the minimal TUI deletions in the Files list remove the last `swarm` import. Tests pass, apart from the known environmental failures. `/swarm` still exists in the TUI at this point, but has no runner and reports swarm unavailable; Task 3 removes it.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "agent: archive swarm; WriteLock moves to agent, runner factory to pipeline; drop TesterView and test.run"
```

---

### Task 3: Remove swarm from the TUI and commands

**Files:**
- Modify: `internal/commands/commands.go`:
  - delete the `/swarm` command entry (`Name: "swarm"`)
  - in the `/agents` description, `swarm & SDD budgets` → `SDD budgets`
- Modify: `internal/app/tui/commands_dispatch.go` (delete the `"swarm":` dispatch entry and the `snap.SwarmRunner`/`snap.SwarmOverrideFactory` copies)
- Modify: `internal/app/tui/model.go`:
  - delete the `SessionSwapResult.SwarmRunner` field, the `swarmRunner` field and `WithSwarmRunner`. Task 2 already removed the override types and hooks.
  - in `buildCustomAgentRunner`, delete the swarm fallback (`if m.customAgentFactory == nil && m.swarmRunner != nil { return m.swarmRunner }`) and its doc sentence
  - in the `castlist.StartMsg` handler, delete the swarm half of the comment above the SDD override rebuild
  - in `openRunPreflight`:
    - start with `roles := routing.SDDCastRoles` and `title := "Start plan run?"`
    - delete the swarm `meta` lines that read `m.state.Config.Swarm.Budget`; `meta` starts empty
    - drop the `if kind == "sdd" {` wrapper so its body runs unconditionally
    - fix the doc comment to `opens the cast list panel for an SDD run`
  - the comment `kind is "sdd" or "swarm"` → `kind is "sdd"`
- Modify: `internal/app/tui/nowbar.go` (`Swarm` input field, `srcSwarm`, its case), `internal/app/tui/livestrip.go` (delete `swarmStripText`), `internal/app/tui/status.go` (delete the `SwarmProgress` tokens segment and the `swarm tokens=6` priority note; renumber nothing)
- Delete: `internal/app/tui/sessionsheet/section_swarm.go`, `section_swarm_test.go`. Modify `sessionsheet/panel.go` (remove `SwarmSection{}` and the `"swarm"` case) and `sessionsheet/section.go` (delete the `Swarm` field)
- Modify: `internal/app/tui/settings/sections.go` (delete the `swarm` section), `frames_basic.go` (delete `swarmFrame`/`SwarmFrame` and the swarm budget fields), `reset.go` (delete the `"swarm"` case), `state.go` (delete the `Swarm.Budget.ToolIters` clone line)
- Modify: `internal/app/tui/agents/panel.go`:
  - replace the "Swarm Roles" header and list with an "Agent Roles" header listing `routing.RoleImplementer`, `routing.RoleReviewer` and `routing.RoleRepoScout`
  - delete the `roster.swarm_budget` drill field
  - fix the package and stack comments that mention swarm
- Modify: `internal/llm/routing/router.go` (delete `SwarmCastRoles`), `internal/llm/routing/router_test.go`
- Test: `internal/commands/commands_test.go`, `internal/app/tui/model_test.go`, `internal/app/tui/agents/panel_test.go`, plus the TUI tests that referenced swarm (`status_test.go`, `nowbar_test.go`, `listpanel/*_test.go`, `textfield/paste_test.go`)

**Interfaces:**
- Consumes: Task 2 (no `swarm` package).
- Produces: no `/swarm` command and no swarm types in `internal/app/tui`. The preflight panel serves `sdd` only.

- [ ] **Step 1: Write the failing tests.** Add to `internal/commands/commands_test.go`. Build the registry exactly as the file's first test that looks up a command does (copy its first few lines). Then:

```go
func TestSwarmCommandIsGone(t *testing.T) {
	// reg built as in the file's first command-lookup test
	if _, ok := reg.Lookup("swarm"); ok {
		t.Fatal("/swarm must not be registered")
	}
}
```

  Add to `internal/app/tui/model_test.go`:

```go
// Review focus 4.
func TestSDDPreflightStillUsesSDDCast(t *testing.T) {
	m := newTestModel(t)
	m.openRunPreflight("sdd", nil, "plan.md")
	panel, ok := m.dock.Panel().(*castlist.Panel)
	if !ok {
		t.Fatalf("preflight panel not open: %T", m.dock.Panel())
	}
	view := panel.View(120, 40)
	if !strings.Contains(view, "Start plan run?") || !strings.Contains(view, string(routing.RoleSDDImplementer)) {
		t.Fatalf("preflight is not the SDD cast:\n%s", view)
	}
	if strings.Contains(view, string(routing.RolePlanner)) {
		t.Fatalf("preflight still lists swarm roles:\n%s", view)
	}
}
```

  If the castlist view renders role display names rather than the raw role strings, assert on the display name that `openRunPreflight` builds for `RoleSDDImplementer` (read it from the function's row construction).

  Add to `internal/app/tui/agents/panel_test.go`:

```go
// Review focus 5.
func TestAgentsPanelBuildsWithRetiredRoleBindings(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Models.Presets = map[string]routing.ModelPreset{"reason": {Provider: "ollama", Model: "qwen"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RolePlanner: {Preset: "reason"}, routing.RoleTester: {Preset: "reason"},
		}},
	}
	cfg.Profile.Default = "p"
	view := NewRosterPanel(cfg, "", "", nil).View(100, 60)
	if strings.Contains(view, "Swarm") {
		t.Fatalf("roster still shows swarm:\n%s", view)
	}
	if !strings.Contains(view, "Agent Roles") {
		t.Fatalf("roster missing the Agent Roles header:\n%s", view)
	}
}
```

  `TestRosterRendersPresetBinding` binds `RolePlanner` and expects a `●` glyph. After this task the planner row is no longer listed, so change that test to bind `RoleImplementer` instead.

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/commands/ ./internal/app/tui/ ./internal/app/tui/agents/ -run 'SwarmCommandIsGone|SDDPreflightStillUsesSDDCast|RetiredRoleBindings' -count=1`
Expected: FAIL (`/swarm` registered; the roster shows "Swarm").

- [ ] **Step 3: Delete,** following the Files list and the Deletion rule. Delete swarm-only test cases in the listed TUI test files, and keep their non-swarm cases.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/commands/ ./internal/app/tui/... ./internal/llm/routing/ -count=1`
Expected: PASS, apart from the known `TestStatusLineUntrustedIsWarningColored`.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "tui: remove /swarm, swarm progress, the swarm session-sheet section and swarm budgets"
```

---

### Task 4: Remove swarm from ACP, session state and config

**Files:**
- Modify: `internal/acp/host.go`:
  - delete `srv.Handle("session/swarm_start", turns.SwarmStart)` and `srv.Handle("session/swarm_status", turns.SwarmStatus)`
  - delete `"swarmDispatch": map[string]any{},` from the advertised session capabilities
- Modify: `internal/acp/turn.go`:
  - delete `SwarmStartParams`, `SwarmCastEntry`, `SwarmTurnResult`, the `SwarmRunner` field, `SwarmStart`, `SwarmStatus` and the `lastSwarm` map
  - fix the `AgentRunner` doc comment that names `*swarm.Orchestrator`
- Modify: `internal/acp/rundetail.go` (delete `SwarmRun`, `buildSwarmRun`, `ClearSwarmProgress` and the swarm branch of the run-detail build; `kind` is `"sdd"` or `"none"`), `internal/acp/memory.go` (delete the `SwarmBudget` field and its assignment; fix the comment `plus swarm/SDD budgets`)
- Delete: `internal/app/session/swarm_progress.go`, `swarm_progress_test.go`. Modify `internal/app/session/session.go` (delete the `swarmProgress` field and the comment line naming swarm role runners)
- Modify: `internal/app/config/types.go` (delete `Swarm SwarmConfig`, `SwarmConfig`, `SwarmBudgetConfig`), `file_types.go` (delete `fileSwarm`, `fileSwarmBudget` and the `Swarm` field), `defaults.go`, `merge.go`, `save.go`, `copy.go` (the swarm lines)
- Modify: `internal/acp/turn_test.go`, `rundetail_test.go`, `memory_test.go`, `internal/app/config/config_test.go`, `save_test.go` (delete swarm cases)
- Test: `internal/acp/host_test.go`, `internal/app/config/config_test.go`

**Interfaces:**
- Consumes: Tasks 2 and 3.
- Produces: no swarm ACP methods, types or capability flag; no `SwarmProgress`; no `config.SwarmConfig`.

- [ ] **Step 1: Write the failing tests.** Add to `internal/acp/host_test.go`:

```go
// Review focus 2: old clients calling swarm methods get method-not-found.
func TestHostRegistersNoSwarmMethods(t *testing.T) {
	h, err := newAgentHost(runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return nil, errors.New("no runtime in tests")
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}
	srv := NewServer(strings.NewReader(""), io.Discard)
	var alive atomic.Bool
	h.registerHandlers(srv, &alive)
	for _, m := range []string{"session/swarm_start", "session/swarm_status"} {
		if _, ok := srv.handlers[m]; ok {
			t.Errorf("%s still registered", m)
		}
	}
	if _, ok := srv.handlers["session/sdd_start"]; !ok {
		t.Fatal("session/sdd_start must stay registered")
	}
}
```

  Add `strings` and `sync/atomic` to that file's imports if they're missing.

  Add to `internal/app/config/config_test.go`:

```go
// Review focus 1.
func TestConfigWithRetiredSwarmSectionLoads(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "marshal")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[swarm.budget]\nmax_fix_rounds = 2\nmax_total_tokens = 1000\n"), 0o644)
	if _, err := Load(LoadOptions{HomeDir: home, WorkingDir: t.TempDir(), SkipProjectConfig: true}); err != nil {
		t.Fatalf("config with a retired [swarm] section must load: %v", err)
	}
}
```

  If the package resolves the user dir differently (`MARSHAL_CONFIG_DIR`/XDG), write the file where `UserConfigPath(home)` points: `os.MkdirAll(filepath.Dir(UserConfigPath(home)), 0o755)`.

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/acp/ -run TestHostRegistersNoSwarmMethods -count=1; CGO_ENABLED=1 go test ./internal/app/config/ -run TestConfigWithRetiredSwarmSectionLoads -count=1`
Expected: the ACP test FAILS (methods registered). The config test passes today. That's expected: it guards Step 3's deletion of the struct.

- [ ] **Step 3: Delete,** following the Files list and the Deletion rule.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/acp/ ./internal/app/... -count=1`
Expected: PASS, apart from the known environmental failures. Both new tests pass.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "acp: remove session/swarm_start, swarm run detail and swarm budgets; drop swarm session state and config"
```

---

### Task 5: Remove swarm from the web bridge and UI

**Files:**
- Modify: `web/bridge/runs.go`:
  - delete `RunSwarm` and the `Goal` field of `RunRequest`
  - in `StartRun` and `validateRunRequest`, delete the `case RunSwarm:` branches; the default message becomes `kind must be %q` with `RunSDD`
  - delete the `if req.Kind == RunSwarm { params["goal"] = … }` branch so the SDD path is unconditional
  - in `runName`, delete the swarm branch so it always returns `"SDD run"`
  - fix the doc comments `execute a plan or a swarm goal` → `execute a plan`, and `starts an SDD or swarm run` → `starts an SDD run`
- Modify: `web/bridge/recipes.go` (`case RecipePrompt, RunSDD:`; message `kind must be prompt or sdd`), `web/bridge/reciperun.go` (`case RunSDD:` with `rr.Plan = prompt` unconditional), `web/bridge/registry.go` (comment `the SDD or swarm run detail` → `the SDD run detail`)
- Modify: `web/bridge/runs_test.go`, `reciperun_test.go`, `budget_test.go`, `reviewfix_test.go` (delete or convert swarm cases)
- Modify, in `web/ui/src`:
  - `lib/api.ts`: delete `SwarmRole` and `SwarmRun`; `RunDetail.kind: 'sdd' | 'none'`; remove `swarm?:`; `RunRequest.kind: 'sdd'` and remove `goal?:`; remove `Roster.swarmBudget`; `RecipeKind = 'prompt' | 'sdd'`
  - `lib/library/RecipeEditor.svelte`: `KINDS = ['prompt', 'sdd']`
  - `lib/live/Tile.svelte`: the tag shows SDD progress only
  - `lib/runs/Timeline.svelte`: delete the `(a swarm)` comment clause
  - `lib/runs/model.ts`: `kind: 'sdd'`; delete the swarm branch of `summarize`
  - `lib/runs/NewRunModal.svelte`: delete the kind selector and goal textarea; the description becomes `Start a plan run.`
  - `views/Run.svelte`: delete the `swarm` derivation and its `{:else if swarm}` block; token fallbacks use `sdd` only
  - `views/Live.svelte`: `No agents are running a plan or swarm.` → `No agents are running a plan.`
- Modify: `web/ui/src/lib/api.test.ts`, `views/Runs.test.ts`, `lib/live/wall.test.ts`, `views/Live.test.ts`, `views/Run.test.ts` (delete swarm cases)
- Rebuild: `web/bridge/static/` (tracked)
- Test: `web/bridge/runs_test.go`, `web/bridge/recipes_test.go` (create the test there if absent)

**Interfaces:**
- Consumes: Task 4 (the ACP method no longer exists).
- Produces: the bridge rejects `kind: "swarm"` for runs and recipes; the UI has no swarm kind.

- [ ] **Step 1: Write the failing tests.** Add to `web/bridge/runs_test.go`:

```go
func TestRunRequestRejectsSwarmKind(t *testing.T) {
	err := validateRunRequest(RunRequest{Kind: "swarm"})
	if !errors.Is(err, errInvalidRun) {
		t.Fatalf("validateRunRequest(swarm) = %v, want errInvalidRun", err)
	}
}
```

  Add to `web/bridge/recipes_test.go` (or the existing recipe test file: `grep -ln 'func TestRecipe' web/bridge/*_test.go`):

```go
func TestRecipeRejectsSwarmKind(t *testing.T) {
	err := Recipe{Name: "nightly", Kind: "swarm"}.Validate()
	if !errors.Is(err, ErrInvalidRecipe) || !strings.Contains(err.Error(), "prompt or sdd") {
		t.Fatalf("Validate(swarm) = %v", err)
	}
}
```

  If `Recipe.Validate` requires other fields to reach the kind check, set them to the valid values the file's existing passing recipe test uses.

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `go test ./web/bridge/ -run 'RejectsSwarmKind' -count=1`
Expected: FAIL (swarm is still a valid kind).

- [ ] **Step 3: Implement** the bridge and UI edits in the Files list. Then:

```bash
cd web/ui && npm run test -- --run && npm run build && cd ../..
go build ./cmd/webbridge
```

  `npm run build` writes `web/bridge/static/`. Expected: vitest passes and the build succeeds.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `go test ./web/... -count=1 && go test ./web/bridge/ -run TestWebIsStdlibOnly -count=1`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add -A web
git commit -m "web: remove the swarm run and recipe kind from the bridge and UI"
```

---

### Task 6: Docs and full verification

**Files:**
- Modify: `AGENTS.md`:
  - delete the line `internal/agent/swarm/    multi-agent orchestration, lock, verdict`
  - change `slash commands (/plan, /sdd, /swarm, /settings, …)` to `slash commands (/plan, /sdd, /settings, …)`
- Modify: `README.md`:
  - delete the `/swarm <goal>` paragraph (it starts with `` `/swarm <goal>` runs a planner ``)
  - change the commands-table cell `` `/swarm` `/sdd` `/run` `/review` `` to `` `/sdd` `/run` `/review` ``
  - change `swarm and SDD pipelines` to `the SDD pipeline`
- Modify: `docs/ARCHITECTURE.md` (Agent row: `` `internal/agent/` `` and `Single-agent loop.`, plus any other swarm mention found by `grep -n -i swarm docs/ARCHITECTURE.md`)
- Modify: `CHANGELOG.md` (`## [Unreleased]`, `### Removed`)
- Modify: `docs/context-diet/swarm-archive-design.md` (append `## Result` with the archive SHA and whether it was pushed)

- [ ] **Step 1: Update the docs** as listed. In `CHANGELOG.md`, under `### Removed`, add:

```markdown
- Swarm (`/swarm`, ACP `session/swarm_start` and `session/swarm_status`, the
  web UI's swarm run and recipe kind, `[swarm]` config). It is preserved on
  the `archive/swarm` branch. Old `[swarm]` config keys are ignored.
- The internal `test.run` tool and the swarm tester registry view.
```

- [ ] **Step 2: Check for stray references.**

```bash
grep -rn -i 'swarm' --include='*.go' internal cmd web/bridge | grep -v _test.go
grep -rn -i 'swarm' web/ui/src
```

Expected: no hits other than:
- the display-map labels for old transcripts, if any mention swarm tools
- `routing.RoleTester`/`RolePlanner` comments
- the CHANGELOG

Fix any other hit by deleting it.

- [ ] **Step 3: Full verification.**

```bash
gofmt -l . | grep -v '^\.home/' ; CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./... 2>&1 | tail -60
CGO_ENABLED=1 go build -o marshal ./cmd/marshal && go test ./test/usability/... -run TestScripted -timeout 5m
cd web/ui && npm run test -- --run && npm run build && cd ../.. && go test ./web/... -count=1
```

Expected: `gofmt -l` prints nothing, `go vet` is clean, and everything passes apart from the known environmental failures. `git status --short web/bridge/static` shows no uncommitted asset changes.

- [ ] **Step 4: Commit.**

```bash
git add AGENTS.md README.md docs CHANGELOG.md
git commit -m "docs: record the swarm archive"
```

## Integration notes

- **Order:** 1 → 2 → 3 → 4 → 5 → 6. Each task's commit builds: Task 2 removes exactly the TUI override hooks that imported the `swarm` package; the rest of the TUI follows in Task 3.
- **Recovery:** `git checkout archive/swarm -- internal/agent/swarm` restores the orchestrator source for reuse. Its wiring has to be redone against the then-current `pipeline.RunnerFactory` and `agent.WriteLock`.
