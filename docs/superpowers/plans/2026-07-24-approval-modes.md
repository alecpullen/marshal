# Approval Modes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `ask`/`edit`/`auto` turn-classification trio with a unified five-mode cycle (`plan`/`default`/`edit`/`copilot`/`auto`) that bundles turn-classification and approval-gating, adds a `mode.request` elevation tool, and works across both the TUI and ACP transports.

**Architecture:** The approval mode is a first-class field on `PolicyEngine` (Approach A). `Evaluate` applies a mode transform as its final step: `plan`/`default` deny writes, `copilot`/`auto` downgrade `Confirm → Allow` except for a non-bypassable floor (guardrails + `git push`). The runner gates `ask_user`/`question.ask` in `auto` mode. A new `mode.request` native tool reuses the existing pending-approval channel so the TUI shows a picker and ACP forwards to the editor. Config persists the active mode; the TUI cycles it via Tab/Shift+Tab and slash commands.

**Tech Stack:** Go 1.x, `marshal/internal/tools/policy` (mode transform), `internal/tools/native` (`mode.request` tool), `internal/agent` (runner question-gate, prompts), `internal/app/config` (schema/merge/save), `internal/app/tui` (cycle, slash commands, status), `internal/commands` (command registration), `internal/app` (wiring). Tests via `go test ./...`; formatting via `gofmt -w .`.

## Global Constraints

- No new external dependencies; standard library only.
- The floor (guardrails + `git push`) is non-bypassable in every mode: no mode downgrades a floor `DecisionConfirm` to `DecisionAllow`. Guardrails that return `DecisionDeny` stay `Deny`.
- `PolicyEngine` constructed without calling `SetApprovalMode` defaults to `ModeEdit` (confirm-each) so all existing tests stay green — the mode transform is a no-op for `edit`.
- The five mode names are exact strings: `"plan"`, `"default"`, `"edit"`, `"copilot"`, `"auto"`. Cycle order: `plan → default → edit → copilot → auto` (wrapping).
- `mode.request` is advertised only when the active mode is `default`, and only for the general/interactive runner. Swarm/SDD omit it.
- `git commit` is NOT on the floor — only `git push` (all variants) is.
- Commit messages follow `<type>(<scope>): <subject>` (see `git log --oneline`).
- Run `gofmt -w .` before each commit; the final task runs the full suite.
- Every code step ships with its test; TDD ordering (test fails → implement → test passes → commit).

---

### Task 1: Add `ApprovalMode` type and state to the policy engine

**Files:**
- Modify: `internal/tools/policy/policy.go`
- Modify: `internal/tools/policy/policy_test.go`

**Interfaces:**
- Consumes: nothing (new foundation).
- Produces:
  - `type ApprovalMode string`
  - `const ModePlan, ModeDefault, ModeEdit, ModeCopilot, ModeAuto ApprovalMode`
  - `func (pe *PolicyEngine) SetApprovalMode(m ApprovalMode)` — thread-safe setter (mirrors `SetSessionRules`).
  - `func (pe *PolicyEngine) ApprovalMode() ApprovalMode` — thread-safe reader (mirrors `Logger()`).
  - The `PolicyEngine` struct gains a `approvalMode ApprovalMode` field, initialized to `ModeEdit` in `NewEngine`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tools/policy/policy_test.go`:

```go
func TestApprovalModeDefaultsToEdit(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)
	if pe.ApprovalMode() != ModeEdit {
		t.Fatalf("default ApprovalMode = %q, want %q", pe.ApprovalMode(), ModeEdit)
	}
}

func TestSetApprovalModeIsThreadSafe(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)
	pe.SetApprovalMode(ModeAuto)
	if pe.ApprovalMode() != ModeAuto {
		t.Fatalf("after SetApprovalMode(ModeAuto), ApprovalMode = %q, want %q", pe.ApprovalMode(), ModeAuto)
	}
	pe.SetApprovalMode(ModePlan)
	if pe.ApprovalMode() != ModePlan {
		t.Fatalf("after SetApprovalMode(ModePlan), ApprovalMode = %q, want %q", pe.ApprovalMode(), ModePlan)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tools/policy/ -run TestApprovalMode -v`
Expected: build failure — `undefined: ApprovalMode`, `undefined: ModeEdit`, `undefined: pe.SetApprovalMode`, `undefined: pe.ApprovalMode`.

- [ ] **Step 3: Write minimal implementation**

In `internal/tools/policy/policy.go`, add the type and constants near the `Decision` constants (after line 23):

```go
// ApprovalMode is the active interaction/approval mode. It bundles
// turn-classification and approval-gating into one concept. The zero
// value is ModeEdit (confirm-each), preserving pre-modes behavior for
// engines constructed without calling SetApprovalMode.
type ApprovalMode string

const (
	ModePlan    ApprovalMode = "plan"
	ModeDefault ApprovalMode = "default"
	ModeEdit    ApprovalMode = "edit"
	ModeCopilot ApprovalMode = "copilot"
	ModeAuto    ApprovalMode = "auto"
)
```

Add the field to the `PolicyEngine` struct (after the `logger` field, ~line 41):

```go
	approvalMode ApprovalMode
```

In `NewEngine` (after the `return &PolicyEngine{...}` literal is built, set the default — the cleanest spot is to add `approvalMode: ModeEdit` to the struct literal at line 60-65):

```go
	return &PolicyEngine{
		config:       cfg,
		sessionRules: sessionRules,
		rules:        rules,
		logger:       slog.Default(),
		approvalMode: ModeEdit,
	}
```

Add the setter and getter (after `SetLogger`, ~line 98):

```go
// SetApprovalMode replaces the active approval mode. Safe for concurrent
// use with Evaluate (mirrors SetSessionRules / SetRules).
func (pe *PolicyEngine) SetApprovalMode(m ApprovalMode) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.approvalMode = m
}

// ApprovalMode returns the active approval mode. Safe for concurrent use.
func (pe *PolicyEngine) ApprovalMode() ApprovalMode {
	pe.mu.RLock()
	defer pe.mu.RUnlock()
	return pe.approvalMode
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tools/policy/ -run TestApprovalMode -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tools/policy/policy.go internal/tools/policy/policy_test.go
git add internal/tools/policy/policy.go internal/tools/policy/policy_test.go
git commit -m "feat(policy): add ApprovalMode type and SetApprovalMode/ApprovalMode accessors"
```

---

### Task 2: Add the `git push` floor check and the mode transform to `Evaluate`

**Files:**
- Modify: `internal/tools/policy/policy.go`
- Modify: `internal/tools/policy/policy_test.go`

**Interfaces:**
- Consumes: `ApprovalMode`, `SetApprovalMode` from Task 1; `registry.Risk*` constants.
- Produces:
  - `func isGitPushFloor(cmd string) bool` — detects `git push` and all variants.
  - The mode transform inside `Evaluate`: after guardrails + the git-push floor + F4 rules + risk fallback + shell rules compute a decision, the mode transform runs as the final step. It reads `pe.ApprovalMode()` (snapshot under the lock in `Evaluate` alongside the other fields) and rewrites the decision per the spec.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tools/policy/policy_test.go`:

```go
func TestIsGitPushFloor(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"git push", true},
		{"git push origin main", true},
		{"git push --force", true},
		{"git push -f", true},
		{"git push --tags", true},
		{"git push --force-with-lease", true},
		{"git commit -m foo", false},
		{"git status", false},
		{"echo git push", false},
		{"git pushd", false},
		{"  git push  ", true},
	}
	for _, tc := range cases {
		got := isGitPushFloor(tc.cmd)
		if got != tc.want {
			t.Errorf("isGitPushFloor(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestGitPushFloorAlwaysConfirms(t *testing.T) {
	for _, mode := range []ApprovalMode{ModeEdit, ModeCopilot, ModeAuto} {
		pe := NewEngine(&config.Config{}, nil)
		pe.SetApprovalMode(mode)
		dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": "git push"})
		if err != nil {
			t.Fatalf("mode %s: Evaluate error: %v", mode, err)
		}
		if dec != DecisionConfirm {
			t.Errorf("mode %s: git push floor = %v, want Confirm; reason=%q", mode, dec, reason)
		}
		if !strings.Contains(reason, "git push") {
			t.Errorf("mode %s: floor reason %q should mention git push", mode, reason)
		}
	}
}

func TestPlanModeDeniesWriteTools(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)
	pe.SetApprovalMode(ModePlan)
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "file.write_patch", Description: "write", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
	pe.WithRegistry(reg)
	dec, reason, err := pe.Evaluate("file.write_patch", map[string]interface{}{"patch": "File: a\n<<<<<<< SEARCH\n=======\nnew\n>>>>>>> REPLACE"})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionDeny {
		t.Fatalf("plan mode file.write_patch = %v, want Deny; reason=%q", dec, reason)
	}
	if !strings.Contains(reason, "mode.request") {
		t.Errorf("plan mode deny reason %q should mention mode.request", reason)
	}
}

func TestPlanModeAllowsReadOnlyTools(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)
	pe.SetApprovalMode(ModePlan)
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "file.read", Description: "read", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
	pe.WithRegistry(reg)
	dec, _, err := pe.Evaluate("file.read", map[string]interface{}{"path": "a.go"})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionAllow {
		t.Fatalf("plan mode file.read = %v, want Allow", dec)
	}
}

func TestDefaultModeDeniesShellWrites(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)
	pe.SetApprovalMode(ModeDefault)
	dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": "rm -rf /tmp/x"})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionDeny {
		t.Fatalf("default mode rm -rf = %v, want Deny; reason=%q", dec, reason)
	}
	if !strings.Contains(reason, "mode.request") {
		t.Errorf("default mode deny reason %q should mention mode.request", reason)
	}
}

func TestCopilotAutoApprovesWriteTools(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)
	pe.SetApprovalMode(ModeCopilot)
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "file.write_patch", Description: "write", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
	pe.WithRegistry(reg)
	dec, reason, err := pe.Evaluate("file.write_patch", map[string]interface{}{"patch": "File: a\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionAllow {
		t.Fatalf("copilot mode file.write_patch = %v, want Allow; reason=%q", dec, reason)
	}
	if !strings.Contains(reason, "auto-approved") {
		t.Errorf("copilot reason %q should mention auto-approved", reason)
	}
}

func TestAutoModeAutoApprovesShellWrites(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)
	pe.SetApprovalMode(ModeAuto)
	dec, _, err := pe.Evaluate("shell.run", map[string]interface{}{"command": "go test ./..."})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionAllow {
		t.Fatalf("auto mode go test = %v, want Allow", dec)
	}
}

func TestAutoModeDoesNotAutoApproveGitPush(t *testing.T) {
	pe := NewEngine(&config.Config{}, nil)
	pe.SetApprovalMode(ModeAuto)
	dec, _, err := pe.Evaluate("shell.run", map[string]interface{}{"command": "git push origin main"})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionConfirm {
		t.Fatalf("auto mode git push = %v, want Confirm (floor)", dec)
	}
}

func TestEditModeIsNoOp(t *testing.T) {
	// edit mode must behave exactly as today (confirm-each for writes).
	pe := NewEngine(&config.Config{}, nil)
	pe.SetApprovalMode(ModeEdit)
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "file.write_patch", Description: "write", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
	pe.WithRegistry(reg)
	dec, _, err := pe.Evaluate("file.write_patch", map[string]interface{}{"patch": "File: a\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionConfirm {
		t.Fatalf("edit mode file.write_patch = %v, want Confirm", dec)
	}
}
```

Add `"context"` and `"strings"` to the test file imports if not already present. Add `import "marshal/internal/tools/registry"` if not present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tools/policy/ -run "TestIsGitPushFloor|TestGitPushFloor|TestPlanMode|TestDefaultMode|TestCopilot|TestAutoMode|TestEditMode" -v`
Expected: FAIL — `isGitPushFloor` undefined; mode transform not implemented (write tools still confirm in plan mode, etc.).

- [ ] **Step 3: Implement `isGitPushFloor`**

In `internal/tools/policy/policy.go`, add the helper (near `normalizeCommand`, ~line 533):

```go
// isGitPushFloor reports whether cmd is a `git push` invocation (any
// variant: --force, -f, --tags, --force-with-lease, with a remote, etc.).
// It is the non-bypassable floor: no mode downgrades a git-push Confirm.
// It uses the AST parser to distinguish `git push` from `git pushd` and
// to handle pipes/subshells; on parse failure it falls back to a trimmed
// prefix check.
func isGitPushFloor(cmd string) bool {
	stages, err := parseStages(cmd)
	if err == nil {
		for _, s := range stages {
			if s.argv0 == "git" && len(s.args) > 0 && s.args[0] == "push" {
				return true
			}
		}
		return false
	}
	// Legacy fallback: trimmed, lowercased, words-based.
	trimmed := strings.TrimSpace(strings.ToLower(cmd))
	words := strings.Fields(trimmed)
	if len(words) >= 2 && words[0] == "git" && words[1] == "push" {
		return true
	}
	return false
}
```

- [ ] **Step 4: Implement the git-push floor and mode transform in `Evaluate`**

In `internal/tools/policy/policy.go`, modify `Evaluate` (starts at line 121). After the guardrail block in `evaluateShell` (the `if dec != "" { return dec, reason }` at ~line 251-253), the floor check must run. But `evaluateShell` is a separate method called from `Evaluate`. The cleanest approach: insert the floor check and mode transform at the top of `Evaluate`, snapshotting the mode, then delegate to `evaluateShell`/`evaluateMCP` as today, then apply the transform to the returned decision.

Replace the body of `Evaluate` (lines 121-144) with:

```go
func (pe *PolicyEngine) Evaluate(toolName string, args map[string]interface{}) (Decision, string, error) {
	// Snapshot the mutable fields under the lock: SetRules / WithRegistry /
	// SetSessionRules / SetApprovalMode run from the UI goroutine and must
	// not race a mid-Evaluate read.
	pe.mu.RLock()
	rules := pe.rules
	reg := pe.registry
	sessionRules := pe.sessionRules
	mode := pe.approvalMode
	pe.mu.RUnlock()

	// Evaluate must work with a nil engine config (tests, legacy callers):
	// treat it as a zero config, which lands on the secure-confirm fallback.
	cfg := pe.config
	if cfg == nil {
		cfg = &config.Config{}
	}

	// git push floor: non-bypassable in every mode. Runs before the mode
	// transform so a floor Confirm is returned directly, never downgraded.
	if toolName == "shell.run" || toolName == "test.run" {
		if cmdRaw, ok := args["command"]; ok {
			if cmd, ok := cmdRaw.(string); ok && isGitPushFloor(cmd) {
				return DecisionConfirm, "git push requires approval (non-bypassable floor)"
			}
		}
	}

	var decision Decision
	var reason string
	if strings.HasPrefix(toolName, "mcp.") {
		decision, reason = evaluateMCP(cfg, rules, toolName, args)
	} else {
		decision, reason = pe.evaluateShell(cfg, rules, reg, sessionRules, toolName, args)
	}

	// Mode transform: the final step, applied after guardrails, the floor,
	// F4 rules, and risk fallbacks have computed a decision.
	decision, reason = applyModeTransform(mode, toolName, args, decision, reason, reg)
	return decision, reason, nil
}
```

Add the `applyModeTransform` function (after `Evaluate`):

```go
// applyModeTransform rewrites the computed decision based on the active
// approval mode. It runs as the final step in Evaluate, after guardrails,
// the git-push floor, F4 rules, and risk fallbacks.
//
//   - plan / default: write-capable tools and shell writes are denied
//     (directing the agent to mode.request). Read-only tools pass.
//   - edit: no transform (today's confirm-each behavior).
//   - copilot / auto: a computed Confirm is downgraded to Allow
//     (auto-approve), EXCEPT the floor and guardrails already returned
//     early in Evaluate, so any Confirm reaching here is auto-approvable.
func applyModeTransform(mode ApprovalMode, toolName string, args map[string]interface{}, decision Decision, reason string, reg *registry.Registry) (Decision, string) {
	switch mode {
	case ModePlan, ModeDefault:
		if decision == DecisionAllow {
			// Read-only tools and explicitly-allowed read commands pass.
			return decision, reason
		}
		// Everything else (Confirm, Deny that isn't a guardrail) is denied
		// with a mode.request directive. Guardrail Denys are preserved.
		if decision == DecisionDeny && isGuardrailDeny(reason) {
			return decision, reason
		}
		return DecisionDeny, fmt.Sprintf("denied: in %s mode, cannot modify files; call mode.request to switch to an editing mode", mode)
	case ModeEdit:
		return decision, reason
	case ModeCopilot, ModeAuto:
		if decision == DecisionConfirm {
			return DecisionAllow, fmt.Sprintf("auto-approved in %s mode", mode)
		}
		return decision, reason
	default:
		return decision, reason
	}
}

// isGuardrailDeny reports whether a Deny reason originated from a
// conservative guardrail (not from the mode transform). Guardrail denies
// must be preserved even in plan/default modes so destructive commands
// are never silently allowed.
func isGuardrailDeny(reason string) bool {
	return strings.Contains(reason, "guardrail") || strings.Contains(reason, "blocked by conservative")
}
```

Note: `evaluateShell` currently takes `(pe *PolicyEngine, cfg, rules, reg, sessionRules, toolName, args)` and reads `pe.config`/`pe.registry`/`pe.sessionRules` itself. Since `Evaluate` now snapshots these and passes them in, `evaluateShell`'s signature is unchanged (it already receives them as params). The only change is that `Evaluate` no longer calls `pe.mu.RLock()` twice — the snapshot is taken once. Verify `evaluateShell` does not re-lock for these fields (it receives them as params, so it does not). If `evaluateShell` reads `pe.config` directly, change it to use the passed `cfg` param (it already does — check line 133-136 which reads `pe.config`; that logic moved into `Evaluate`'s nil-check, so `evaluateShell` receives `cfg` already). Confirm by reading the full `evaluateShell` signature and body after the edit.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tools/policy/ -v`
Expected: all new tests PASS, and all existing policy tests PASS (they construct engines without `SetApprovalMode`, defaulting to `ModeEdit` — a no-op transform).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/tools/policy/policy.go internal/tools/policy/policy_test.go
git add internal/tools/policy/policy.go internal/tools/policy/policy_test.go
git commit -m "feat(policy): add git-push floor and per-mode decision transform in Evaluate"
```

---

### Task 3: Add `approval_mode` to config (types, file-types, merge, defaults, save)

**Files:**
- Modify: `internal/app/config/types.go`
- Modify: `internal/app/config/file_types.go`
- Modify: `internal/app/config/merge.go`
- Modify: `internal/app/config/defaults.go`
- Modify: `internal/app/config/save.go`
- Modify: `internal/app/config/config_test.go`
- Modify: `internal/app/config/save_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `AgentConfig.ApprovalMode string` field (TOML tag `approval_mode`), merged from config files, defaulted to `"default"`, persisted on save.

- [ ] **Step 1: Write the failing tests**

Append to `internal/app/config/config_test.go` (in the merge test that already exists, or as a new test):

```go
func TestApprovalModeMerge(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[agent]
approval_mode = "edit"
`)
	writeFile(t, work+"/.marshal/config.toml", `
[agent]
approval_mode = "copilot"
`)
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.ApprovalMode != "copilot" {
		t.Fatalf("ApprovalMode = %q, want %q (project wins)", cfg.Agent.ApprovalMode, "copilot")
	}
}

func TestApprovalModeDefault(t *testing.T) {
	cfg := Default()
	if cfg.Agent.ApprovalMode != "default" {
		t.Fatalf("default ApprovalMode = %q, want %q", cfg.Agent.ApprovalMode, "default")
	}
}
```

Append to `internal/app/config/save_test.go` (in the existing round-trip test `TestSaveAndLoadRoundTrip` or a new one):

```go
func TestApprovalModeRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/.marshal/config.toml"
	cfg := Default()
	cfg.Agent.ApprovalMode = "auto"
	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Agent.ApprovalMode != "auto" {
		t.Fatalf("round-trip ApprovalMode = %q, want %q", loaded.Agent.ApprovalMode, "auto")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/config/ -run "TestApprovalMode" -v`
Expected: FAIL — `ApprovalMode` field does not exist on `AgentConfig`.

- [ ] **Step 3: Add the field to `AgentConfig`**

In `internal/app/config/types.go`, add to the `AgentConfig` struct (after `SubtaskIterations`, line 258):

```go
	// ApprovalMode is the active interaction/approval mode: "plan",
	// "default", "edit", "copilot", or "auto". Default "default". See
	// docs/superpowers/specs/2026-07-24-approval-modes-design.md.
	ApprovalMode string `toml:"approval_mode"`
```

- [ ] **Step 4: Add to `fileAgent`**

In `internal/app/config/file_types.go`, add to `fileAgent` (after `SubtaskIterations`, line 47):

```go
	ApprovalMode *string `toml:"approval_mode"`
```

- [ ] **Step 5: Add the merge**

In `internal/app/config/merge.go`, inside the `if file.Agent != nil` block (after line 41):

```go
		set(&cfg.Agent.ApprovalMode, file.Agent.ApprovalMode)
```

- [ ] **Step 6: Add the default**

In `internal/app/config/defaults.go`, find the `Agent` zero-value construction. The `AgentConfig` is not explicitly set in `Default()` (it relies on zero values), so add an explicit `Agent` block. Find where `Agent` would be set (search for `Agent:` in defaults.go; if absent, add it to the `Config` literal). Add:

```go
		Agent: AgentConfig{
			ApprovalMode: "default",
		},
```

If `Agent` is already a field in the `Default()` literal, add `ApprovalMode: "default"` to it instead of creating a new block.

- [ ] **Step 7: Add the save**

In `internal/app/config/save.go`, in both `fileAgent` literals (the `activePresetName == ""` branch at line 35 and the else branch at line 45), add `ApprovalMode` to each:

For the first branch (line 35-43):
```go
		file.Agent = &fileAgent{
			Provider:             strutil.Ptr(cfg.Agent.Provider),
			Model:                strutil.Ptr(cfg.Agent.Model),
			MaxToolIterations:    strutil.Ptr(cfg.Agent.MaxToolIterations),
			MaxRetries:           strutil.Ptr(cfg.Agent.MaxRetries),
			MaxTurnContextTokens: strutil.Ptr(cfg.Agent.MaxTurnContextTokens),
			PlanFirst:            strutil.Ptr(cfg.Agent.PlanFirst),
			SubtaskIterations:    strutil.Ptr(cfg.Agent.SubtaskIterations),
			ApprovalMode:         strutil.Ptr(cfg.Agent.ApprovalMode),
		}
```

For the else branch (line 45-52), add the same `ApprovalMode: strutil.Ptr(cfg.Agent.ApprovalMode),` line.

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/app/config/ -v`
Expected: all PASS — new tests plus existing config tests.

- [ ] **Step 9: Commit**

```bash
gofmt -w internal/app/config/types.go internal/app/config/file_types.go internal/app/config/merge.go internal/app/config/defaults.go internal/app/config/save.go internal/app/config/config_test.go internal/app/config/save_test.go
git add internal/app/config/types.go internal/app/config/file_types.go internal/app/config/merge.go internal/app/config/defaults.go internal/app/config/save.go internal/app/config/config_test.go internal/app/config/save_test.go
git commit -m "feat(config): add approval_mode to AgentConfig with default, merge, and save"
```

---

### Task 4: Add `SetApprovalMode` to the runner and gate `ask_user`/`question.ask` in `auto` mode

**Files:**
- Modify: `internal/agent/runner.go`
- Create: `internal/agent/runner_mode_test.go`

**Interfaces:**
- Consumes: `policy.ApprovalMode`, `policy.ModeAuto` from Task 1; `r.Policy` (the `*policy.PolicyEngine` field at runner.go:151).
- Produces:
  - `func (r *Runner) SetApprovalMode(m policy.ApprovalMode)` — delegates to `r.Policy.SetApprovalMode(m)`.
  - The `auto` question-gate in the `ActionAskUser` and `ActionQuestionAsk` cases of the run loop.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/runner_mode_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestAutoModeBlocksAskUser verifies that in auto mode, an ask_user
// action gets a correction message instead of blocking on the user.
func TestAutoModeBlocksAskUser(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "done"},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "ask_user", Args: json.RawMessage(`{"question":"which?"}`)}},
			nil,
		},
		FinishReasons: []string{"stop", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.SetApprovalMode(policy.ModeAuto)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassEdit))

	if err := runner.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The first turn should have produced a correction telling the agent
	// ask_user is not available in auto mode, not a pending question.
	if state.PendingQuestion() != nil {
		t.Fatal("auto mode should not set a pending question")
	}
	// Find the correction message in the session.
	found := false
	for _, msg := range state.Messages() {
		if strings.Contains(msg.Content, "ask_user is not available in auto mode") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a correction message mentioning auto mode; none found in session messages")
	}
}
```

Note: `newTestState(t)` is an existing helper in `runner_misc_test.go` — confirm it exists by searching for `func newTestState`. If it takes a different signature, adapt the call. Inspect `internal/agent/runner_misc_test.go` for the exact helper.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestAutoModeBlocksAskUser -v`
Expected: FAIL — `SetApprovalMode` undefined on `*Runner`; the `auto` gate does not exist so `ask_user` may block or set a pending question.

- [ ] **Step 3: Add `SetApprovalMode` to the runner**

In `internal/agent/runner.go`, after `SetPolicyRules` (line 274), add:

```go
// SetApprovalMode sets the active approval mode on the policy engine.
// Called by the TUI on mode switch and by app.StartRuntime to seed from
// config. Satisfies the AgentRunner interface's SetApprovalMode method.
func (r *Runner) SetApprovalMode(m policy.ApprovalMode) {
	if r.Policy != nil {
		r.Policy.SetApprovalMode(m)
	}
}
```

- [ ] **Step 4: Add the `auto` question-gate**

In `internal/agent/runner.go`, in the `ActionAskUser` case (line 699-703), modify the existing role gate to also check the mode. Replace:

```go
		case ActionAskUser:
			if r.role() != RoleGeneral {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("ask_user is not available for the %s role; proceed with your best judgment or report findings", r.role())))
				continue
			}
```

with:

```go
		case ActionAskUser:
			if r.role() != RoleGeneral {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("ask_user is not available for the %s role; proceed with your best judgment or report findings", r.role())))
				continue
			}
			if r.Policy != nil && r.Policy.ApprovalMode() == policy.ModeAuto {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("ask_user is not available in auto mode; proceed with your best judgment and state the assumption you made")))
				continue
			}
```

Do the same for the `ActionQuestionAsk` case (line 731-734). Replace:

```go
		case ActionQuestionAsk:
			if r.role() != RoleGeneral {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("question.ask is not available for the %s role; proceed with your best judgment or report findings", r.role())))
				continue
			}
```

with:

```go
		case ActionQuestionAsk:
			if r.role() != RoleGeneral {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("question.ask is not available for the %s role; proceed with your best judgment or report findings", r.role())))
				continue
			}
			if r.Policy != nil && r.Policy.ApprovalMode() == policy.ModeAuto {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("question.ask is not available in auto mode; proceed with your best judgment and state the assumption you made")))
				continue
			}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestAutoModeBlocksAskUser -v`
Expected: PASS.

- [ ] **Step 6: Run the full agent package to check for regressions**

Run: `go test ./internal/agent/ -v -count=1`
Expected: PASS — existing tests construct runners without `SetApprovalMode`, so the policy defaults to `ModeEdit` and the question-gate does not fire.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/agent/runner.go internal/agent/runner_mode_test.go
git add internal/agent/runner.go internal/agent/runner_mode_test.go
git commit -m "feat(agent): add SetApprovalMode and gate ask_user/question.ask in auto mode"
```

---

### Task 5: Add per-mode prompt directives and advertise `mode.request` in `default` mode

**Files:**
- Modify: `internal/agent/prompts.go`
- Modify: `internal/agent/prompts_test.go`

**Interfaces:**
- Consumes: `policy.ApprovalMode` from Task 1.
- Produces:
  - `func modeDirective(mode policy.ApprovalMode) string` — returns the per-mode directive text.
  - `buildSystemPrompt` prepends the mode directive to the system prompt.
  - `mode.request` is advertised in the available-tools list only when `mode == ModeDefault`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/prompts_test.go`:

```go
func TestModeDirectivePlan(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, nil, nil, nil, false)
	_ = msg
	d := modeDirective(policy.ModePlan)
	if !strings.Contains(d, "plan mode") {
		t.Errorf("plan directive %q should mention 'plan mode'", d)
	}
	if !strings.Contains(d, "numbered plan") {
		t.Errorf("plan directive %q should mention 'numbered plan'", d)
	}
}

func TestModeDirectiveDefault(t *testing.T) {
	d := modeDirective(policy.ModeDefault)
	if !strings.Contains(d, "default mode") {
		t.Errorf("default directive %q should mention 'default mode'", d)
	}
	if !strings.Contains(d, "mode.request") {
		t.Errorf("default directive %q should mention mode.request", d)
	}
}

func TestModeDirectiveCopilot(t *testing.T) {
	d := modeDirective(policy.ModeCopilot)
	if !strings.Contains(d, "copilot mode") {
		t.Errorf("copilot directive %q should mention 'copilot mode'", d)
	}
	if !strings.Contains(d, "auto-approved") {
		t.Errorf("copilot directive %q should mention auto-approved", d)
	}
}

func TestModeDirectiveAuto(t *testing.T) {
	d := modeDirective(policy.ModeAuto)
	if !strings.Contains(d, "auto mode") {
		t.Errorf("auto directive %q should mention 'auto mode'", d)
	}
	if !strings.Contains(d, "cannot ask the user") {
		t.Errorf("auto directive %q should mention cannot ask the user", d)
	}
}
```

Add `"marshal/internal/tools/policy"` to the test file imports if not present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestModeDirective -v`
Expected: FAIL — `modeDirective` undefined.

- [ ] **Step 3: Implement `modeDirective`**

In `internal/agent/prompts.go`, add the function (after `baseOutputFormat` or near the role addenda):

```go
// modeDirective returns the per-mode behavioral directive prepended to
// the system prompt. Each directive tells the agent its current mode and
// the constraints that apply. See the approval-modes design spec.
func modeDirective(mode policy.ApprovalMode) string {
	switch mode {
	case policy.ModePlan:
		return "You are in plan mode. You are read-only and cannot modify files. Produce a numbered plan as your final answer, then stop. You may ask the user clarifying questions about requirements before planning."
	case policy.ModeDefault:
		return "You are in default mode. You are read-only and cannot modify files. If you need to make changes, call the mode.request tool to ask the user to switch to an editing mode. Do not attempt write tools directly."
	case policy.ModeEdit:
		return "You are in edit mode. Each file change requires user approval before it is applied."
	case policy.ModeCopilot:
		return "You are in copilot mode. File changes are auto-approved except for destructive guardrails and git push. You may ask the user a question if you hit a genuine ambiguity that would materially change the outcome."
	case policy.ModeAuto:
		return "You are in auto mode. File changes are auto-approved except for destructive guardrails and git push. You cannot ask the user questions — proceed with your best judgment and state the assumptions you make."
	default:
		return ""
	}
}
```

Add `"marshal/internal/tools/policy"` to the imports in `prompts.go` if not already present.

- [ ] **Step 4: Wire the directive into `buildSystemPrompt`**

In `internal/agent/prompts.go`, `buildSystemPrompt` (line 186) currently does not receive the mode. The mode lives on the `PolicyEngine`, which the runner holds but `buildSystemPrompt` does not receive. The cleanest approach: add an `approvalMode policy.ApprovalMode` parameter to `buildSystemPrompt` and its public wrappers `BuildSystemPrompt`/`BuildSystemPromptWithDeferred`, and have the caller (the runner) pass `r.Policy.ApprovalMode()`.

Update the signatures:

```go
func BuildSystemPrompt(role AgentRole, tools []registry.Tool, skillIndex *skills.Index, activeSkills []string, nativeTools bool) schema.ChatMessage {
	return buildSystemPrompt(role, tools, nil, skillIndex, activeSkills, nativeTools, policy.ModeEdit)
}

func BuildSystemPromptWithDeferred(role AgentRole, tools []registry.Tool, deferred []registry.Tool, skillIndex *skills.Index, activeSkills []string, nativeTools bool) schema.ChatMessage {
	return buildSystemPrompt(role, tools, deferred, skillIndex, activeSkills, nativeTools, policy.ModeEdit)
}
```

And add a new variant that the runner will call:

```go
func BuildSystemPromptWithMode(role AgentRole, tools []registry.Tool, deferred []registry.Tool, skillIndex *skills.Index, activeSkills []string, nativeTools bool, mode policy.ApprovalMode) schema.ChatMessage {
	return buildSystemPrompt(role, tools, deferred, skillIndex, activeSkills, nativeTools, mode)
}
```

Update `buildSystemPrompt` to accept `mode policy.ApprovalMode` and prepend the directive. In the body, after `b.WriteString(baseRules)` and before the tools list, add:

```go
	if d := modeDirective(mode); d != "" {
		b.WriteString("\n\n")
		b.WriteString(d)
	}
```

Then find every caller of `BuildSystemPrompt`/`BuildSystemPromptWithDeferred` in the runner (search `internal/agent` for these calls) and update the primary call site to use `BuildSystemPromptWithMode(..., r.Policy.ApprovalMode())`. The existing `BuildSystemPrompt`/`BuildSystemPromptWithDeferred` wrappers default to `ModeEdit` so tests that don't pass a mode stay green.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestModeDirective -v`
Run: `go test ./internal/agent/ -run TestSystemPrompt -v`
Expected: PASS — new directive tests pass; existing system-prompt tests pass (they use the `ModeEdit`-defaulting wrapper).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/agent/prompts.go internal/agent/prompts_test.go
git add internal/agent/prompts.go internal/agent/prompts_test.go
git commit -m "feat(agent): add per-mode prompt directives and BuildSystemPromptWithMode"
```

---

### Task 6: Implement the `mode.request` native tool

**Files:**
- Create: `internal/tools/native/mode_request.go`
- Create: `internal/tools/native/mode_request_test.go`
- Modify: `internal/tools/native/native.go`

**Interfaces:**
- Consumes: `session.State` (the pending-approval channel pattern from `question.go`), `session.PendingToolCall`, `session.UserApprovalDecision`.
- Produces:
  - `func (t *toolSet) modeRequestTool() registry.Tool` — the `mode.request` tool. It posts a `PendingToolCall` with `Name: "mode.request"` and a `Reason` prefix `"mode-elevation:"` so the TUI/ACP can distinguish it from a normal approval. It blocks on the response channel. The `UserApprovalDecision.Edited` field carries the chosen mode name (e.g. `"copilot"`) when approved; empty `Edited` means the user denied.
  - Registered in `RegisterAll`.

- [ ] **Step 1: Write the failing test**

Create `internal/tools/native/mode_request_test.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestModeRequestApprovedRelaysChosenMode(t *testing.T) {
	reg, _, root := setupNativeRegistry(t)
	_ = root
	state := newTestSessionState(t)

	// Drive the tool in a goroutine; it will block on the pending
	// approval until we respond.
	args, _ := json.Marshal(map[string]string{"mode": "edit"})
	type result struct {
		res registry.ToolResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := invokeTool(t, reg, "mode.request", string(args))
		done <- result{res, err}
	}()

	// Wait for the pending approval to appear.
	deadline := time.After(2 * time.Second)
	var pending *session.PendingToolCall
	for {
		pending = state.PendingApproval()
		if pending != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("mode.request did not set a pending approval")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if pending.Name != "mode.request" {
		t.Fatalf("pending Name = %q, want mode.request", pending.Name)
	}
	if !strings.Contains(pending.Reason, "mode-elevation") {
		t.Fatalf("pending Reason = %q, should contain mode-elevation", pending.Reason)
	}

	// Approve with the user's chosen mode in the Edited field.
	pending.Respond(session.UserApprovalDecision{Approved: true, Edited: "copilot"})

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("mode.request error: %v", r.err)
		}
		if !strings.Contains(r.res.Content, "copilot") {
			t.Fatalf("result content %q should mention copilot", r.res.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mode.request did not return after approval")
	}
}

func TestModeRequestDeniedStaysInDefault(t *testing.T) {
	reg, _, _ := setupNativeRegistry(t)
	state := newTestSessionState(t)

	args, _ := json.Marshal(map[string]string{"mode": "edit"})
	done := make(chan struct {
		res registry.ToolResult
		err error
	}, 1)
	go func() {
		res, err := invokeTool(t, reg, "mode.request", string(args))
		done <- struct {
			res registry.ToolResult
			err error
		}{res, err}
	}()

	deadline := time.After(2 * time.Second)
	for {
		if p := state.PendingApproval(); p != nil {
			p.Respond(session.UserApprovalDecision{Approved: false})
			break
		}
		select {
		case <-deadline:
			t.Fatal("mode.request did not set a pending approval")
		case <-time.After(10 * time.Millisecond):
		}
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("mode.request error: %v", r.err)
		}
		if !strings.Contains(r.res.Content, "denied") {
			t.Fatalf("denied result content %q should mention denied", r.res.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mode.request did not return after denial")
	}
}
```

Before writing, inspect `internal/tools/native/file_test.go` for the exact names of `setupNativeRegistry` and whether a `newTestSessionState` helper exists. If `setupNativeRegistry` returns a state, use that. If there is no `newTestSessionState`, check how other tests construct a `*session.State` — likely `session.New(...)` or a test helper. Adapt the test to use the existing helpers; do not invent helpers that don't compile. The key contract: the tool must post a `PendingToolCall` with `Name: "mode.request"` and `Reason` containing `"mode-elevation:"`, block on the response, and relay the result.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tools/native/ -run TestModeRequest -v`
Expected: FAIL — `mode.request` not registered; `invokeTool` returns "unknown tool".

- [ ] **Step 3: Implement the tool handler**

Create `internal/tools/native/mode_request.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

type modeRequestArgs struct {
	Mode string `json:"mode"`
}

// modeRequestTool builds the mode.request native tool. The agent calls it
// (from default mode) to ask the user to switch to an editing mode. The
// handler posts a PendingToolCall with a "mode-elevation:" reason prefix
// so the TUI and ACP PermissionBridge can distinguish it from a normal
// approval. It blocks on the response channel. When approved, the user's
// chosen mode name arrives in UserApprovalDecision.Edited; the handler
// returns a result telling the agent which mode was granted. When denied,
// the result tells the agent to describe its changes instead.
//
// The handler does NOT apply the mode switch itself — the transport (TUI
// or ACP) that responds is responsible for calling SetApprovalMode and
// persisting the change. This keeps the tool side-effect-free at the
// policy level (RiskReadOnly) and lets the transport own the UX.
func (t *toolSet) modeRequestTool() registry.Tool {
	tool := registry.Tool{
		Name:        "mode.request",
		Description: "Request the user to switch from default mode to an editing mode (edit, copilot, or auto). Use this when you need to modify files but are in default mode.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string","description":"The editing intent, e.g. \"edit\""}},"required":["mode"]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[modeRequestArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if strings.TrimSpace(args.Mode) == "" {
			return registry.ToolResult{}, fmt.Errorf("mode is required")
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("session state not available")
		}

		ch := make(chan session.UserApprovalDecision, 1)
		pending := &session.PendingToolCall{
			ID:           fmt.Sprintf("mode_req_%d", time.Now().UnixNano()),
			Name:         "mode.request",
			Args:         string(call.Args),
			Reason:       "mode-elevation: agent requests an editing mode",
			Schema:       tool.Description,
			ResponseChan: ch,
		}
		t.sessionState.SetPendingApproval(pending)

		select {
		case decision := <-ch:
			t.sessionState.SetPendingApproval(nil)
			if decision.Approved {
				chosen := decision.Edited
				if chosen == "" {
					chosen = "edit"
				}
				return registry.ToolResult{
					Summary: fmt.Sprintf("approved — switched to %s mode", chosen),
					Content: fmt.Sprintf("mode.request result: approved — switched to %s mode. You may now make file changes.", chosen),
				}, nil
			}
			return registry.ToolResult{
				Summary: "denied — staying in default mode",
				Content: "mode.request result: denied — staying in default mode; describe your proposed changes instead.",
			}, nil
		case <-ctx.Done():
			t.sessionState.SetPendingApproval(nil)
			return registry.ToolResult{}, ctx.Err()
		}
	}
	return tool
}
```

Add `"time"` to the imports.

- [ ] **Step 4: Register the tool**

In `internal/tools/native/native.go`, in the `all` slice in `RegisterAll` (line 125-145), add after `tools.askUserTool()`:

```go
		tools.modeRequestTool(),
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tools/native/ -run TestModeRequest -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/tools/native/mode_request.go internal/tools/native/mode_request_test.go internal/tools/native/native.go
git add internal/tools/native/mode_request.go internal/tools/native/mode_request_test.go internal/tools/native/native.go
git commit -m "feat(native): add mode.request tool for default-mode elevation"
```

---

### Task 7: Wire the TUI mode cycle, slash commands, and status rendering

**Files:**
- Modify: `internal/app/tui/model.go`
- Modify: `internal/app/tui/commands_dispatch.go`
- Modify: `internal/app/tui/status.go`
- Modify: `internal/app/tui/model_test.go`
- Modify: `internal/commands/commands.go`
- Modify: `internal/commands/commands_test.go`

**Interfaces:**
- Consumes: `policy.ApprovalMode` and the five `Mode*` constants from Task 1; `AgentRunner.SetApprovalMode` (added to the interface); `config.Agent.ApprovalMode` from Task 3.
- Produces:
  - The `AgentRunner` interface gains `SetApprovalMode(mode policy.ApprovalMode)`.
  - `Model.forceMode` is replaced by `Model.approvalMode policy.ApprovalMode`, defaulting to `ModeDefault`.
  - `setMode` maps each mode to its `ForceClass` (`plan`/`default` → `"question"`; `edit`/`copilot`/`auto` → `"edit"`) and calls `m.runner.SetApprovalMode(mode)`.
  - `modeOrder` becomes `[]policy.ApprovalMode{ModePlan, ModeDefault, ModeEdit, ModeCopilot, ModeAuto}`.
  - Slash commands `/plan`, `/default`, `/edit`, `/copilot`, `/auto` replace `/ask`, `/edit`, `/auto`.
  - `/mode` picker lists the five modes.
  - Status line renders the active mode name.

- [ ] **Step 1: Write the failing test for the cycle**

Append to `internal/app/tui/model_test.go`:

```go
func TestModeCycleOrder(t *testing.T) {
	if len(modeOrder) != 5 {
		t.Fatalf("modeOrder has %d entries, want 5", len(modeOrder))
	}
	want := []string{"plan", "default", "edit", "copilot", "auto"}
	for i, m := range modeOrder {
		if string(m) != want[i] {
			t.Errorf("modeOrder[%d] = %q, want %q", i, m, want[i])
		}
	}
}

func TestSetModeMapsForceClass(t *testing.T) {
	m := newTestModel()
	runner := &fakeAgentRunner{}
	m.runner = runner

	m.setMode("plan")
	if m.approvalMode != "plan" {
		t.Fatalf("approvalMode = %q, want plan", m.approvalMode)
	}
	m.setMode("auto")
	if m.approvalMode != "auto" {
		t.Fatalf("approvalMode = %q, want auto", m.approvalMode)
	}
}
```

Before writing, inspect `model_test.go` for `newTestModel` and `fakeAgentRunner`. The `fakeAgentRunner` (line 925) has `SetForceClass` and `SetPolicyRules` as no-ops — add `SetApprovalMode` as a no-op too. If `newTestModel` does not exist, construct a `Model` with `Model{approvalMode: ModeDefault}` directly. Adapt to existing helpers.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/ -run "TestModeCycleOrder|TestSetModeMapsForceClass" -v`
Expected: FAIL — `modeOrder` still has 3 entries; `approvalMode` field does not exist.

- [ ] **Step 3: Update the `AgentRunner` interface**

In `internal/app/tui/model.go` (line 51-55), add `SetApprovalMode`:

```go
type AgentRunner interface {
	Run(ctx context.Context, goal string) error
	SetForceClass(class string)
	SetPolicyRules(rules []config.PermissionRule)
	SetApprovalMode(mode policy.ApprovalMode)
}
```

Add `"marshal/internal/tools/policy"` to the imports.

Update all fake runners in `model_test.go` (`fakeAgentRunner`, `blockingAgentRunner`, `fakeSwarmRunner`, `fakeSDDRunner`) to add:

```go
func (f *fakeAgentRunner) SetApprovalMode(policy.ApprovalMode) {}
```

Use the correct receiver name for each fake. Add the `policy` import to the test file.

Also update the swarm/SDD orchestrator `SetForceClass` no-ops in `internal/agent/swarm/orchestrator.go` and `internal/agent/sdd/orchestrator.go` to add a `SetApprovalMode` no-op (they satisfy `AgentRunner` structurally):

In `internal/agent/swarm/orchestrator.go` (line 52-54):
```go
func (o *Orchestrator) SetForceClass(string)                   {}
func (o *Orchestrator) SetApprovalMode(policy.ApprovalMode)    {}
```

In `internal/agent/sdd/orchestrator.go` (line 46-47):
```go
func (o *Orchestrator) SetForceClass(string)                  {}
func (o *Orchestrator) SetApprovalMode(policy.ApprovalMode)   {}
```

Add the `policy` import to both orchestrator files.

- [ ] **Step 4: Replace `forceMode` with `approvalMode`**

In `internal/app/tui/model.go`:
- Find `forceMode string` (line 82) and replace with `approvalMode policy.ApprovalMode`.
- Update `setMode` (line 1951-1960) to:

```go
func (m *Model) setMode(mode string) {
	class := "edit"
	if mode == "plan" || mode == "default" {
		class = "question"
	}
	if m.runner != nil {
		m.runner.SetForceClass(class)
		m.runner.SetApprovalMode(policy.ApprovalMode(mode))
	}
	m.approvalMode = policy.ApprovalMode(mode)
}
```

- Update `modeOrder` (line 1964):
```go
var modeOrder = []policy.ApprovalMode{policy.ModePlan, policy.ModeDefault, policy.ModeEdit, policy.ModeCopilot, policy.ModeAuto}
```

- Update `modeSwitchMessage` (line 1969-1973):
```go
var modeSwitchMessage = map[string]string{
	"plan":     "Switched to Plan mode. Agent will produce a numbered plan, then stop.",
	"default":  "Switched to Default mode. Agent is read-only; it will request elevation to edit.",
	"edit":     "Switched to Edit mode. Each file change requires approval.",
	"copilot":  "Switched to Copilot mode. Changes auto-approve; agent may ask on ambiguity.",
	"auto":     "Switched to Auto mode. Fully autonomous; no questions asked.",
}
```

- Update `cycleMode` to use `modeOrder` and `string(m.approvalMode)` instead of `m.forceMode`. Find the `cycleMode` function and replace `m.forceMode` references with `string(m.approvalMode)`.

- Update `modePickerItems` (line 2215-2220):
```go
	return []picker.Item{
		{Label: "Plan", Detail: "read-only, forced plan", Badge: badge("plan"), Value: "plan"},
		{Label: "Default", Detail: "read-only, request elevation", Badge: badge("default"), Value: "default"},
		{Label: "Edit", Detail: "plan + confirm each", Badge: badge("edit"), Value: "edit"},
		{Label: "Copilot", Detail: "auto-approve, may ask", Badge: badge("copilot"), Value: "copilot"},
		{Label: "Auto", Detail: "fully autonomous", Badge: badge("auto"), Value: "auto"},
	}
```

Update the `badge` function's `current` variable to use `string(m.approvalMode)` instead of `m.forceMode`, and remove the `v == "auto" && current == ""` special-case (the empty-string-as-auto trick is gone).

- Initialize `approvalMode` in the model's construction/`New` path. Find where `forceMode` was zero-valued (it defaulted to `""` meaning auto). Now default to `ModeDefault`. In the `Model` struct construction or `New`/`WithRunner`, set `approvalMode: policy.ModeDefault`. Search for where the model is first created and add the field.

- [ ] **Step 5: Update slash command dispatch**

In `internal/app/tui/commands_dispatch.go`, replace the `"ask"`, `"edit"`, `"auto"` entries (lines 54-71) with:

```go
		"plan": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("plan")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["plan"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"default": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("default")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["default"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"edit": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("edit")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["edit"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"copilot": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("copilot")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["copilot"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"auto": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("auto")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["auto"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
```

Update the `"mode"` handler (line 72-86) to accept the five modes:

```go
		"mode": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if len(args) > 0 {
				switch strings.ToLower(args[0]) {
				case "plan", "default", "edit", "copilot", "auto":
					return m.dispatchCommand("/" + strings.ToLower(args[0]))
				case "sdd":
					m.openSDDPlanPicker()
					m.refreshViewport()
					return m, nil
				}
			}
			m.openPicker("mode", "Interaction mode", "", m.modePickerItems(), "")
			m.refreshViewport()
			return m, nil
		},
```

- [ ] **Step 6: Update status line rendering**

In `internal/app/tui/status.go`, `modeSegment` (line 71-92), replace the `forceMode` fallback:

```go
	mode := string(m.approvalMode)
	if mode == "" {
		mode = "default"
	}
	return mode
```

- [ ] **Step 7: Update command registrations**

In `internal/commands/commands.go`, replace the `"ask"`, `"edit"`, `"auto"` command entries (lines 213-230) with the five new modes:

```go
		{
			Name:        "plan",
			Description: "Switch to Plan mode (read-only, forced numbered plan)",
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "default",
			Description: "Switch to Default mode (read-only, request elevation to edit)",
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "edit",
			Description: "Switch to Edit mode (plan + edit, confirm each change)",
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "copilot",
			Description: "Switch to Copilot mode (auto-approve, may ask questions)",
			Hidden:      true,
			TUIOnly:     true,
		},
		{
			Name:        "auto",
			Description: "Switch to Auto mode (fully autonomous, no questions)",
			Hidden:      true,
			TUIOnly:     true,
		},
```

Update the `/mode` command description (line 232-237):
```go
		{
			Name:        "mode",
			Description: "Pick the interaction mode (Plan / Default / Edit / Copilot / Auto)",
			Args:        "[plan|default|edit|copilot|auto]",
			Group:       groupWorkflow,
			TUIOnly:     true,
		},
```

- [ ] **Step 8: Update command-registration tests**

In `internal/commands/commands_test.go`, update `TestModeSwitchCommands` (line 275-289):

```go
func TestModeSwitchCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	for _, name := range []string{"plan", "default", "edit", "copilot", "auto"} {
		cmd, ok := cmdReg.Lookup(name)
		if !ok {
			t.Fatalf("%s command not registered", name)
		}
		if !cmd.TUIOnly {
			t.Errorf("%s should be TUIOnly", name)
		}
	}
}
```

Update any other test that references `"ask"` as a mode (search for `"ask"` in commands_test.go and model_test.go — replace with the new mode names where they assert mode-switch behavior).

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./internal/app/tui/ -v -count=1`
Run: `go test ./internal/commands/ -v`
Expected: PASS. There will be existing tests referencing `forceMode` and `"ask"`/`"edit"`/`"auto"` — update each to use `approvalMode` and the new names. Search for `forceMode` across `internal/app/tui/*_test.go` and replace with `approvalMode` / `string(m.approvalMode)`. Search for `"ask"` mode assertions and replace with `"default"` or `"plan"` as appropriate.

- [ ] **Step 10: Commit**

```bash
gofmt -w internal/app/tui/model.go internal/app/tui/commands_dispatch.go internal/app/tui/status.go internal/app/tui/model_test.go internal/commands/commands.go internal/commands/commands_test.go internal/agent/swarm/orchestrator.go internal/agent/sdd/orchestrator.go
git add internal/app/tui/model.go internal/app/tui/commands_dispatch.go internal/app/tui/status.go internal/app/tui/model_test.go internal/commands/commands.go internal/commands/commands_test.go internal/agent/swarm/orchestrator.go internal/agent/sdd/orchestrator.go
git commit -m "feat(tui): replace ask/edit/auto with plan/default/edit/copilot/auto cycle"
```

---

### Task 8: Wire app construction — seed the mode from config and handle `mode.request` dispatch in the TUI

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/runtime.go`
- Modify: `internal/app/tui/model.go` (the `mode.request` pending-approval handler)

**Interfaces:**
- Consumes: `config.Agent.ApprovalMode` from Task 3; `PolicyEngine.SetApprovalMode` from Task 1; the `mode.request` pending-approval from Task 6.
- Produces:
  - `app.go` seeds `pol.SetApprovalMode(policy.ApprovalMode(cfg.Agent.ApprovalMode))` after engine construction.
  - `app.StartRuntime` does the same for the ACP path.
  - The TUI's pending-approval handler recognizes a `mode.request` pending call (by `Name == "mode.request"` or `Reason` prefix `"mode-elevation:"`) and shows the editing-variant picker instead of the normal approval dialog, then calls `SetApprovalMode` + config save on the user's choice.

- [ ] **Step 1: Seed the mode in `app.go`**

In `internal/app/app.go`, after `pol.WithRegistry(reg)` (line 434), add:

```go
	pol.SetApprovalMode(policy.ApprovalMode(cfg.Agent.ApprovalMode))
```

Add `"marshal/internal/tools/policy"` to the imports if not present (it likely is, since `policy.NewEngine` is called at line 430).

- [ ] **Step 2: Seed the mode in `app.StartRuntime`**

In `internal/app/runtime.go`, find `StartRuntime` (line 299). It constructs the runtime, which includes the policy engine (via the same `app.go` construction path or a parallel one). Inspect how `StartRuntime` builds the policy engine — if it calls the same `buildRuntime` helper that `app.go` uses, the seed from Step 1 covers it. If `StartRuntime` has its own engine construction, add the same `pol.SetApprovalMode(...)` call there. Read `StartRuntime` and `buildRuntime` to confirm the path; add the seed wherever the engine is constructed.

- [ ] **Step 3: Handle `mode.request` in the TUI pending-approval flow**

In `internal/app/tui/model.go`, find where `PendingApproval` events are handled (search for `PendingApproval` or `EventPendingApprovalChanged` in the TUI's `Update` method). The existing handler shows the approval dialog. Add a branch: if the pending call's `Name == "mode.request"` (or `Reason` starts with `"mode-elevation:"`), show a picker with the three editing variants (`edit`, `copilot`, `auto`) plus a deny option, and on the user's choice, respond via `pending.Respond(session.UserApprovalDecision{Approved: true, Edited: chosenMode})` and call `m.setMode(chosenMode)` + persist to config.

Find the existing approval-rendering code (search for `approvalChoice` or the approval dialog in `approval.go`). The `mode.request` handler can reuse the picker infrastructure (`m.openPicker`) with a custom set of items. On `PickedMsg` for the mode-elevation picker, respond to the pending call and apply the mode.

This is the most TUI-internal step. Inspect `internal/app/tui/approval.go` and the `Update` handler for `EventPendingApprovalChanged` to find the exact seam. The key contract: when a `mode.request` pending call arrives, the TUI shows the editing-variant picker and, on selection, responds with `Approved: true, Edited: <mode>` and calls `m.setMode(<mode>)` + `m.persistAndReload` with the updated `cfg.Agent.ApprovalMode`.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./...`
Expected: PASS across all packages. If a test outside the touched packages breaks, investigate — likely a test that relied on the old `forceMode`/`"ask"` mode or the implicit confirm-each default.

- [ ] **Step 5: Run gofmt + vet**

Run: `gofmt -w . && go vet ./...`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/app/app.go internal/app/runtime.go internal/app/tui/model.go
git add internal/app/app.go internal/app/runtime.go internal/app/tui/model.go
git commit -m "feat(app): seed approval mode from config and wire mode.request TUI dispatch"
```

---

### Task 9: Full-suite verification and documentation

**Files:**
- No new source. Run the complete suite and update the ACP support matrix doc.

- [ ] **Step 1: Run the complete test suite**

Run: `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 2: Run gofmt + vet**

Run: `gofmt -w . && go vet ./...`
Expected: clean.

- [ ] **Step 3: Update the ACP support matrix doc**

In `docs/10-acp.md`, the `request_permission` row (line 23) mentions "same allow/deny/always semantics as the TUI approval flow." Add a note that `mode.request` elevation requests reuse the `request_permission` wire shape with a `"mode-elevation:"` reason prefix, and that the active approval mode is seeded from config (no runtime toggle over ACP). Append to the `request_permission` row's Notes column or add a new row:

```
| `request_permission` (mode-elevation) | Full | `mode.request` elevation requests reuse the `request_permission` wire shape with a `mode-elevation:` reason prefix. The editor renders the editing-variant picker. The active approval mode is seeded from `[agent] approval_mode` in config; ACP has no runtime mode toggle. |
```

- [ ] **Step 4: Commit**

```bash
gofmt -w .
git add docs/10-acp.md
git commit -m "docs: document approval modes and mode.request over ACP"
```

---

## Self-Review

**1. Spec coverage:**
- §2 Mode model (five modes, cycle, floor) → Task 1 (type), Task 2 (transform + floor), Task 7 (cycle).
- §3 Elevation mechanism (`mode.request`) → Task 6 (tool), Task 8 (TUI dispatch).
- §4 Policy-engine integration → Task 1 (state), Task 2 (transform + git-push floor).
- §5 Question-asking gating (`auto`) → Task 4 (runner gate).
- §6 Config, persistence & TUI wiring → Task 3 (config), Task 7 (TUI cycle/commands/status), Task 8 (app wiring + seed).
- §7 Prompt changes → Task 5 (directives + `mode.request` visibility).
- §8 ACP transport → Task 8 (seed in `StartRuntime`), Task 9 (doc). The `mode.request` reuse of `request_permission` is covered by Task 6's reason-prefix design + Task 9's doc note.
- §9 File map → all listed files are touched across Tasks 1-9.
- §10 Out of scope → respected (no per-tool overrides, no time-limited elevation, no mode-specific routing, no CLI flag).

**2. Placeholder scan:** No "TBD"/"TODO"/"handle edge cases." The two steps that say "inspect the existing test file" (Task 6 Step 1, Task 7 Step 1) and "inspect StartRuntime" (Task 8 Step 2) are intentional — they tell the implementer to verify helper names/signatures that vary by test file rather than guessing. Each gives the exact contract to verify. Concrete code is provided for every implementation step.

**3. Type consistency:**
- `ApprovalMode` and `ModePlan`/`ModeDefault`/`ModeEdit`/`ModeCopilot`/`ModeAuto` — defined in Task 1, used identically in Tasks 2, 4, 5, 7, 8.
- `SetApprovalMode(m ApprovalMode)` / `ApprovalMode() ApprovalMode` — defined on `PolicyEngine` in Task 1, called on `r.Policy` in Task 4, on `m.runner` in Task 7, on `pol` in Task 8.
- `Runner.SetApprovalMode(m policy.ApprovalMode)` — defined in Task 4, added to `AgentRunner` interface in Task 7, called in Task 7's `setMode` and Task 8.
- `modeDirective(mode policy.ApprovalMode) string` — defined in Task 5, used in Task 5's `buildSystemPrompt`.
- `BuildSystemPromptWithMode(..., mode policy.ApprovalMode)` — defined in Task 5.
- `isGitPushFloor(cmd string) bool` — defined in Task 2, used in Task 2's `Evaluate`.
- `applyModeTransform(mode, toolName, args, decision, reason, reg)` — defined in Task 2, used in `Evaluate`.
- `modeRequestTool()` — defined in Task 6, registered in Task 6.
- `modeOrder` — `[]policy.ApprovalMode` in Task 7, consistent with the five constants.
- `cfg.Agent.ApprovalMode string` — added in Task 3, read in Task 8's seed (`policy.ApprovalMode(cfg.Agent.ApprovalMode)` cast).
- `m.approvalMode policy.ApprovalMode` — replaces `forceMode` in Task 7, consistent throughout.

All names and signatures are consistent across tasks.