# Domain G2 — `reloadAgentRuntime` Atomicity (F-XCUT-184 / F-BUG-15)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `reloadAgentRuntime` (F-BUG-15) atomic. If `buildAgentRunner` fails on the new config, leave the previous config/runner untouched and surface a TUI-visible error.

**Architecture:** Pre-validate the new config by attempting to build a "dry" runner from a deep copy of the candidate config. Only on dry-build success, atomically swap `state.Config` and replace the runtime. On failure, retain the prior config and runner, log a `Warn`, and emit a transient footer message via the existing `AddMessage` seam.

**Tech Stack:** Go 1.22+; stdlib only.

## Global Constraints

- Go version: 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter), but the
  tasks below touch pure-Go files only.
- Every code change MUST compile: run `go build ./...` after the
  implementation step.
- Every test change MUST pass: run `go test ./internal/app -run <TestName>`
  for the new test, then `go test ./internal/app -count=1` at task end.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.

## File Structure

Files modified or created by this plan:

- `internal/app/app.go` — `reloadAgentRuntime` rewritten as pre-validate + atomic swap.
- `internal/app/app_test.go` — new test `TestReloadAgentRuntimeRollsBackOnFailure`.
- (no new package, no new types).

---

### Task 1: `reloadAgentRuntime` validates before mutating

**Files:**
- Modify: `internal/app/app.go:776-842` (`reloadAgentRuntime` function).
- Modify: `internal/app/app_test.go` (add regression test).

**Problem:** `reloadAgentRuntime` does
`rt.State.Config = newConfig; runner, err := buildAgentRunner(...)`.
If `buildAgentRunner` returns an error, `state.Config` has already
been mutated. Subsequent turns run on the old runner with the new
config, mismatching provider/model settings.

**Fix:** Restructure the function so that
1. The new config is deep-copied first.
2. `buildAgentRunner` is called against the copy (not against the
   shared state).
3. On success, atomically assign the new config to `state.Config`
   and the new runner to the runtime slot.
4. On failure, leave both untouched and emit a `Warn` log + a
   transient TUI message.

**Implementation steps:**

- [ ] **Step 1: Find the existing function**

In `internal/app/app.go`, locate `reloadAgentRuntime` (around
line 776). Note the current order: it reads `newConfig`, assigns
it to `rt.State.Config`, then calls `buildAgentRunner`.

- [ ] **Step 2: Pre-validate**

Replace the function body so the order is:

```go
func (rt *Runtime) reloadAgentRuntime(newConfig config.Config) error {
    // 1. Validate the new config by trying to build a runner from
    //    a deep copy.
    candidate := newConfig // value type — copy is already local
    if err := candidate.Validate(); err != nil { // if Validate exists; otherwise skip
        return fmt.Errorf("validate new config: %w", err)
    }

    testRunner, err := buildAgentRunner(rt.State, candidate)
    if err != nil {
        slog.Default().Warn("reload: dry-run build failed; keeping previous config",
            "err", err)
        rt.State.AddMessage(session.RoleSystem,
            "Config reload failed; keeping previous settings.",
            session.ContentTypePlain)
        return err
    }

    // 2. Atomic swap.
    rt.State.Config = candidate
    rt.agentRunner = testRunner // or whatever the slot is named
    return nil
}
```

Adapt the slot name to whatever the existing runtime uses (it may
be `rt.runner`, `rt.primary`, etc.). The two assignments are
guaranteed to run in sequence under a single goroutine; for
cross-goroutine safety add a short mutex if one isn't already
present.

- [ ] **Step 3: Add the regression test**

In `internal/app/app_test.go`, add:

```go
func TestReloadAgentRuntimeRollsBackOnFailure(t *testing.T) {
    rt := newTestRuntime(t) // use the existing test seam
    originalConfig := rt.State.Config
    originalRunner := rt.agentRunner // whatever the field is

    // Build a config that buildAgentRunner will reject. The simplest
    // trigger is an obviously-bad provider that the registry will
    // reject on start.
    bad := originalConfig
    bad.Providers = []config.ProviderConfig{{Name: "", Type: ""}}

    err := rt.reloadAgentRuntime(bad)
    if err == nil {
        t.Fatalf("expected reload to fail with bad provider")
    }

    if !reflect.DeepEqual(rt.State.Config, originalConfig) {
        t.Fatalf("config was mutated despite build failure")
    }
    if rt.agentRunner != originalRunner {
        t.Fatalf("runner was replaced despite build failure")
    }
}
```

If `reloadAgentRuntime` is not currently exported or is lowercase
and not directly testable, add a small seam (e.g. an
`exportedReload` wrapper in `app_test.go`'s own package — Go's
`_test.go` files have access to unexported names in the same
package).

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app -run TestReloadAgentRuntimeRollsBackOnFailure -v
git add internal/app/app.go internal/app/app_test.go
git commit -m "fix(app): reloadAgentRuntime validates before mutating (F-XCUT-184)"
```

---

## Self-Review

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./internal/app -count=1
```

Update the audit doc with:

```markdown
### Batch 24 (G2 — reload atomicity): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-XCUT-184 | RESOLVED | `reloadAgentRuntime` dry-builds the runner from a copy of the new config before mutating `state.Config`. On failure the prior config and runner are preserved and a TUI footer message is shown. New test `TestReloadAgentRuntimeRollsBackOnFailure`. |
```

This also closes F-BUG-15 (the underlying HIGH finding).
