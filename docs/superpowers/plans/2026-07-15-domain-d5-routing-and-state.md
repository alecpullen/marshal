# Domain D5 — Routing & State Lifecycle Audit Fixes

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` or `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Resolve four findings from `docs/14-codebase-improvement-audit-2026-07-14.md`: D1 followup (doc comment accuracy), F-BUG-73 (error specificity), F-POL-86 (legacy defaults), F-POL-87 (lossy-compaction divergence).

**Architecture:** Each task fixes one finding in isolation. Edits land in `runner.go`, `router.go`, and their tests. Build + test after every code change.

**Tech Stack:** Go 1.22+ (stdlib only).

---

## File Structure

Files modified or created by this plan:

- `internal/agent/runner.go` — Tasks 1, 4 (doc comment, summarize failure path).
- `internal/llm/routing/router.go` — Tasks 2, 3 (error precedence, legacy defaults).
- `internal/llm/routing/router_test.go` — Tasks 2, 3 tests.
- `internal/agent/runner_test.go` — Task 4 test.

---

## Global Constraints

- `CGO_ENABLED=1` required for build (tree-sitter), but the touched files are pure Go.
- Every code change MUST compile: `go build ./...` after each implementation step.
- Every test change MUST pass: `go test ./internal/<pkg> -run <TestName>` for the new test, then the full package suite.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.
- Preserve existing public function signatures.

---

### Task 1: D1 followup — Refine `MaxTurnContextTokens` doc comment

**Files:**
- Modify: `internal/agent/runner.go:128-132` (concurrency contract doc comment)

**Problem:** The D1 `F-CON-79` commit added a concurrency contract saying field values "are initialised once and never mutated by RunTask's internal logic". However, `resolveRoute` (runner.go:839-840) DOES mutate `r.MaxTurnContextTokens` monotonically (only grows) when the route-resolved context window exceeds the configured seed value.

**Fix:** Rephrase to acknowledge the monotonic-growth mutation without misleading the caller about sequential-reuse safety.

- [ ] **Step 1: Edit the doc comment**

In `internal/agent/runner.go`, find the concurrency contract block (lines 119-141, the paragraph listing persistent fields) and change the sentence:

```
//     are initialised once and never mutated by RunTask's internal logic.
```

to:

```
//     are initialised once; resolveRoute may grow MaxTurnContextTokens
//     (monotonically) when the route-resolved context window exceeds the
//     configured value. The seed persists across RunTask calls.
```

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Run the agent package tests to confirm no regression**

Run: `go test ./internal/agent -count=1`
Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/runner.go
git commit -m "docs(agent): refine MaxTurnContextTokens doc for monotonic-growth mutation (D1 followup)"
```

---

### Task 2: F-BUG-73 — Return the more specific error in `ResolveRole`

**Files:**
- Modify: `internal/llm/routing/router.go:33-54`
- Test: `internal/llm/routing/router_test.go`

**Problem:** When both the implementer fallback and `legacyRoute` fail, `ResolveRole` returns the *first* call's `errRoleNotConfigured`, not the more informative `fallbackErr`. The `fallbackErr` is in scope only inside the implementer-fallback `if` block; after that block exits, the code returns the primary `err`.

**Fix:** Capture `fallbackErr` outside the inner `if` block and return it at the final fallback line when it is non-nil.

- [ ] **Step 1: Read the current code to confirm the structure**

Read `internal/llm/routing/router.go:33-54`.

- [ ] **Step 2: Write the failing test**

Append to `internal/llm/routing/router_test.go`:

```go
func TestResolveRoleReturnsFallbackErrorOnExhaustion(t *testing.T) {
    // Primary role is unconfigured (errRoleNotConfigured), implementer
    // fallback also unconfigured, no legacy provider. Both errors are
    // "no configured route" errors; the returned error should be the
    // implementer fallback's error (more informative because it
    // identifies which role was missing), not the primary's.
    router := NewStaticRouter(Config{
        DefaultProfile: "default",
        Presets: map[string]ModelPreset{
            "m1": {Provider: "ollama", Model: "m1", LocalOnly: true},
        },
        Profiles: map[string]AgentProfile{
            "default": {
                Name: "default",
                Roles: map[AgentRole]string{
                    RoleImplementer: "m1",
                },
            },
        },
        // No LegacyProvider → legacyRoute returns false.
    })

    // RoleRepoScout is not in any profile → resolveProfileRole returns
    // errRoleNotConfigured. RoleImplementer IS configured, so the fallback
    // succeeds. This doesn't test the exhaustion path — we need BOTH
    // primary and fallback to fail and legacy to be absent.
    //
    // Set up a router where BOTH repo_scout and implementer are missing
    // presets (ErrPresetNotFound is not a "no configured route" error at
    // line 39 but IS a "no configured route" for isNoConfiguredRoute at
    // line 47 — wait, no. Let me re-check.)
    //
    // Actually, ErrPresetNotFound is NOT in isNoConfiguredRoute's check:
    //   func isNoConfiguredRoute(err error) bool {
    //       return errors.Is(err, ErrProfileNotFound) || errors.Is(err, errRoleNotConfigured)
    //   }
    // So PresetNotFound would fall through and be returned early.
    //
    // The only way both errors pass isNoConfiguredRoute is if both are
    // errRoleNotConfigured. Since they are the same sentinel, the
    // returned error (err vs fallbackErr) is the same wrapped value.
    // To differentiate, make the primary errRoleNotConfigured and the
    // fallback ErrProfileNotFound (both are "no configured route" per
    // isNoConfiguredRoute, so fallback does NOT early-return at line 47).
    //
    // Config: profile "default" has no roles for repo_scout (→ errRoleNotConfigured).
    // Profile also has no roles for implementer (→ ErrProfileNotFound because
    // the default profile has no Roles for implementer).
    // No legacy → return fallback's ErrProfileNotFound.

    router2 := NewStaticRouter(Config{
        DefaultProfile: "default",
        Presets: map[string]ModelPreset{
            "m1": {Provider: "ollama", Model: "m1", LocalOnly: true},
        },
        Profiles: map[string]AgentProfile{
            "default": {
                Name:  "default",
                Roles: map[AgentRole]string{},
            },
        },
    })

    _, err := router2.ResolveRole(RoleRepoScout)
    // The primary role (repo_scout) is not in any role map → errRoleNotConfigured.
    // The fallback to implementer: implementer is also not in the role map →
    // errRoleNotConfigured (same as primary). Legacy is absent.
    // Both are errRoleNotConfigured (same sentinel). To create different errors,
    // we need a more exotic setup.
    //
    // Cleaner approach: directly test the scenario where fallbackErr differs.
    // Use a profile where implementer's preset is missing (ErrPresetNotFound)
    // but repo_scout is unconfigured (errRoleNotConfigured).
    _ = err
    _ = router2
}
```

Hmm, this test setup is getting complicated. Let me rethink.

The core scenario is:
1. Primary role (e.g. `RoleRepoScout`) returns `errRoleNotConfigured` — no entry in the profile's Roles map.
2. `role != RoleImplementer`, so implementer fallback is tried.
3. Implementer fallback returns an error that `isNoConfiguredRoute(err)` — could be `errRoleNotConfigured` (not in Roles map) or `ErrProfileNotFound` (default profile missing).
4. Legacy route is absent (no LegacyProvider/LegacyModel).
5. Current code returns the primary's error; fix returns the fallback's error.

The simplest way to make these two errors different:
- Primary: `errRoleNotConfigured` (role not in profile's role map)
- Fallback: `ErrProfileNotFound` (default profile doesn't exist — but wait, the profile IS found because we're in `resolveProfileRole(RoleImplementer)`, which looks up `r.config.Profiles[r.config.DefaultProfile]`. If the default profile doesn't exist, that's `ErrProfileNotFound`.)

Wait, `isNoConfiguredRoute` returns true for both `ErrProfileNotFound` and `errRoleNotConfigured`. So if the implementer fallback gets `ErrProfileNotFound` because the default profile doesn't exist, `isNoConfiguredRoute(fallbackErr)` returns true, and we fall through to legacy. If legacy fails, we return the primary's error.

But there's a subtlety: the primary call `r.resolveProfileRole(role)` already succeeded in finding the default profile (otherwise it would have returned `ErrProfileNotFound` and we'd return at line 39). The primary returned `errRoleNotConfigured` because the role wasn't in the profile's role map.

For the implementer fallback, `r.resolveProfileRole(RoleImplementer)` uses the SAME profile, so it wouldn't fail with `ErrProfileNotFound` either.

So the only way to get different errors is to have the primary fail with `errRoleNotConfigured` and the fallback fail with a DIFFERENT no-configured-route error. But looking at it more carefully, `ErrProfileNotFound` requires the default profile to be missing entirely, which would also make the primary call fail with `ErrProfileNotFound` (not errRoleNotConfigured).

Hmm. Let me think about this differently. The code at line 41 checks:
```go
if role != RoleImplementer && errors.Is(err, errRoleNotConfigured) {
```

So the implementer fallback is only attempted when the primary error is specifically `errRoleNotConfigured` (not `ErrProfileNotFound`).

Inside the fallback:
```go
fallback, fallbackErr := r.resolveProfileRole(RoleImplementer)
```

This could fail with:
1. `ErrProfileNotFound` — same as primary's profile was found, so this shouldn't happen
2. `errRoleNotConfigured` — implementer not in the same profile's role map (same as primary)
3. `ErrPresetNotFound` — preset name in the role map doesn't exist in config.Presets

Wait, `isNoConfiguredRoute` doesn't include `ErrPresetNotFound`! So if the implementer's preset doesn't exist, the code returns `fallbackErr` (ErrPresetNotFound) at line 47! That's the early return that's already correct.

And `isNoConfiguredRoute` doesn't include `ErrRemoteProviderBlocked` either. So those would also be returned early.

So the only scenario where both errors reach line 53 is when both the primary AND the implementer produce errors that `isNoConfiguredRoute` returns true for, which means both are either `ErrProfileNotFound` or `errRoleNotConfigured`.

But as I noted, if the default profile doesn't exist, both calls would get `ErrProfileNotFound`, which is the same error. And if the profile exists but the roles aren't there, both get `errRoleNotConfigured`, also the same.

So in practice, the two errors are ALWAYS the same sentinel when both reach line 53. The fix of returning `fallbackErr` vs `err` makes no practical difference for this case.

BUT the audit says:
> F-BUG-73 — `ResolveRole` returns the wrong error after fallback exhaustion
> When both the implementer fallback and `legacyRoute` fail, `ResolveRole` returns the *first* call's `errRoleNotConfigured`, not the more informative `fallbackErr`.

The audit says `fallbackErr` is "more informative". Let me look at the actual wrapped errors:

```go
return Route{}, fmt.Errorf("%w: %s role %s", errRoleNotConfigured, profile.Name, role)
```

So the wrapped errors would be:
- Primary: `errRoleNotConfigured: local_balanced role repo_scout`
- Fallback: `errRoleNotConfigured: local_balanced role implementer`

Both contain different role names. The fallback's error saying "implementer" is more useful for debugging because it tells you the fallback role also wasn't found.

So the fix is still correct: return the fallback's error because it has a more specific message (mentions the implementer role vs the primary role).

Let me structure the test differently. I'll set up a config where:
1. The profile has role mappings for both repo_scout and implementer
2. But their presets don't exist (to trigger ErrPresetNotFound for implementer, which would be caught and returned early)

No, this doesn't work either because ErrPresetNotFound is NOT in isNoConfiguredRoute, so it returns early.

OK let me think about what can produce different errors...

Actually, let me re-read the code flow:

```
Line 33: route, err := r.resolveProfileRole(role)
Line 34: if err == nil { return route, nil }
Line 38: if !isNoConfiguredRoute(err) { return Route{}, err }
Line 41: if role != RoleImplementer && errors.Is(err, errRoleNotConfigured) {
Line 42:   fallback, fallbackErr := r.resolveProfileRole(RoleImplementer)
```

At line 41, there's an additional check: `errors.Is(err, errRoleNotConfigured)`. This means even if the error is `ErrProfileNotFound` (which IS a "no configured route" error per `isNoConfiguredRoute`), the implementer fallback is NOT attempted for `ErrProfileNotFound` — only for `errRoleNotConfigured`.

So when we reach line 42, we know:
- `err` is `errRoleNotConfigured` (wrapped with profile and role name)
- `role != RoleImplementer`
- `isNoConfiguredRoute(err)` returned true (it does for errRoleNotConfigured)

Inside the fallback:
```go
fallback, fallbackErr := r.resolveProfileRole(RoleImplementer)
```

This can produce:
1. nil (success) → returns the implementer route
2. Any error

For the error to reach line 50 (and subsequently 53), it must pass `isNoConfiguredRoute(fallbackErr)` check at line 46.

`isNoConfiguredRoute` returns true for:
- `errRoleNotConfigured`
- `ErrProfileNotFound`

So fallbackErr could be either of these. In practice, both would be "not found in the same profile" errors. But the actual wrapped messages differ:
- Primary: `errRoleNotConfigured: <profile.name> role <primaryRole>`
- Fallback: `errRoleNotConfigured: <profile.name> role implementer`

So returning `fallbackErr` gives you a message about the implementer role not being found in the profile, which is more informative than the primary role. The content of the errors differs in the role name.

To test this, I can simply check that the returned error mentions the implementer role:

```go
func TestResolveRoleReturnsFallbackErrorOnExhaustion(t *testing.T) {
    // Both repo_scout and implementer are unconfigured in the profile,
    // no legacy provider. The returned error should mention the implementer
    // role (the fallback error) rather than the primary role.
    router := NewStaticRouter(Config{
        DefaultProfile: "default",
        Presets: map[string]ModelPreset{
            "m1": {Provider: "ollama", Model: "m1", LocalOnly: true},
        },
        Profiles: map[string]AgentProfile{
            "default": {
                Name:  "default",
                Roles: map[AgentRole]string{},  // no roles configured
            },
        },
    })
    
    _, err := router.ResolveRole(RoleRepoScout)
    if err == nil {
        t.Fatal("expected error, got nil")
    }
    // After the fix, the error should mention the implementer role fallback,
    // not the repo_scout role.
    if !strings.Contains(err.Error(), "implementer") {
        t.Fatalf("error should mention implementer (fallback): %v", err)
    }
    if strings.Contains(err.Error(), "repo_scout") {
        t.Fatalf("error should not mention repo_scout (primary): %v", err)
    }
}
```

Actually wait, that's a bit too specific. The exact wording depends on how the errors are wrapped. Let me keep it simpler and just check that the error isn't the wrapping around "repo_scout":

```go
func TestResolveRoleReturnsFallbackErrorOnExhaustion(t *testing.T) {
    router := NewStaticRouter(Config{
        DefaultProfile: "default",
        Presets: map[string]ModelPreset{
            "m1": {Provider: "ollama", Model: "m1", LocalOnly: true},
        },
        Profiles: map[string]AgentProfile{
            "default": {
                Name:  "default",
                Roles: map[AgentRole]string{},  // no roles at all
            },
        },
    })
    
    // RoleRepoScout is not in the profile → errRoleNotConfigured
    // RoleImplementer is also not in the profile → errRoleNotConfigured (same error type)
    // No legacy provider → fall through to line 53
    // After fix: returns fallbackErr (implementer-related), not err (repo_scout-related)
    _, err := router.ResolveRole(RoleRepoScout)
    if err == nil {
        t.Fatal("expected error, got nil")
    }
    // With the old code, the error mentions repo_scout.
    // With the fix, the error should mention implementer.
    if !strings.Contains(err.Error(), "implementer") {
        t.Fatalf("returned error should reference implementer (fallback), got: %v", err)
    }
}
```

Let me verify: with the old code, `ResolveRole(RoleRepoScout)`:
1. `resolveProfileRole(RoleRepoScout)` returns `errRoleNotConfigured: default role repo_scout`
2. `!isNoConfiguredRoute(err)` → false (it IS a no-configured-route error)
3. `role != RoleImplementer && errors.Is(err, errRoleNotConfigured)` → true, enter fallback
4. `resolveProfileRole(RoleImplementer)` returns `errRoleNotConfigured: default role implementer`
5. `isNoConfiguredRoute(fallbackErr)` → true, fall through
6. `legacyRoute` returns false (no legacy provider)
7. Returns `Route{}, err` → `errRoleNotConfigured: default role repo_scout`

With the fix:
7. Returns `Route{}, fallbackErr` → `errRoleNotConfigured: default role implementer`

The error message changes from "repo_scout" to "implementer". Good, the test will work.

Wait, but I also need to check: does the test need `strings` package imported? Let me check the existing imports in router_test.go.

Looking at the existing imports:
```go
import (
    "errors"
    "testing"
)
```

I need to add `"strings"` for the test.

- [ ] **Step 2: Write the test**
- [ ] **Step 3: Run the new test to verify it fails with old code**
  Expected: FAIL — the error says "repo_scout" not "implementer"
- [ ] **Step 4: Implement the fix**
- [ ] **Step 5: Re-run test to verify it passes**
- [ ] **Step 6: Run full routing package tests**
- [ ] **Step 7: Commit**

```bash
git add internal/llm/routing/router.go internal/llm/routing/router_test.go
git commit -m "fix(routing): return fallback error when both ResolveRole paths fail (F-BUG-73)"
```

---

### Task 3: F-POL-86 — Add default ContextBudget to legacyRoute

**Files:**
- Modify: `internal/llm/routing/router.go:100-114`
- Test: `internal/llm/routing/router_test.go`

**Problem:** `legacyRoute` returns a `Route` with empty `ContextBudget`. Downstream code at `runner.go:346` passes `route.ContextBudget.MaxRepoContextTokens` to `mergeMemories`, which treats 0 as "use the existing pack's max" (a valid fallback). But other context-budget-derived behaviours (rebudget at `runner.go:816-821`) silently skip when `MaxRepoContextTokens == 0`, meaning the legacy route never triggers a context-pack rebudget.

**Fix:** Add a sane default `ContextBudget` to the legacy route.

- [ ] **Step 1: Read the current code**

Read `internal/llm/routing/router.go:100-114`.

- [ ] **Step 2: Write the failing test**

Append to `internal/llm/routing/router_test.go`:

```go
func TestLegacyRouteHasSaneDefaults(t *testing.T) {
    // A router with only LegacyProvider/LegacyModel set returns a legacy
    // route with non-zero ContextBudget values.
    router := NewStaticRouter(Config{
        DefaultProfile: "missing",
        LegacyProvider: "ollama",
        LegacyModel:    "qwen2.5-coder:7b",
    })
    route, err := router.Resolve(TaskProfile{Class: "edit"})
    if err != nil {
        t.Fatalf("Resolve: %v", err)
    }
    if !route.Legacy {
        t.Fatal("expected legacy route")
    }
    if route.ContextBudget.MaxRepoContextTokens == 0 {
        t.Fatal("legacy route ContextBudget.MaxRepoContextTokens is 0, want > 0")
    }
    if route.ContextBudget.MaxConversationTokens == 0 {
        t.Fatal("legacy route ContextBudget.MaxConversationTokens is 0, want > 0")
    }
}
```

Wait, this test calls `Resolve(TaskProfile{Class: "edit"})`, which calls `ResolveRole(RoleImplementer)`. Since the default profile "missing" doesn't exist, `resolveProfileRole(RoleImplementer)` returns `ErrProfileNotFound`. 

Check: `role != RoleImplementer && errors.Is(err, errRoleNotConfigured)` — false because the error is `ErrProfileNotFound`, not `errRoleNotConfigured`.

So it falls through to `legacyRoute(RoleImplementer)`, which the new Route struct will return with ContextBudget set.

This should work. Let me verify with the existing behavior: `TestResolveUsesLegacyWhenNoProfileRouteExists` already tests a similar scenario.

```go
func TestResolveUsesLegacyWhenNoProfileRouteExists(t *testing.T) {
    router := NewStaticRouter(Config{
        DefaultProfile: "missing",
        LegacyProvider: "ollama",
        LegacyModel:    "qwen2.5-coder:14b",
    })
    route, err := router.Resolve(TaskProfile{Class: "question"})
    ...
}
```

Let me also check if there's an existing test for the ContextBudget of a legacy route that expects 0. Since `TestResolveUsesLegacyWhenNoProfileRouteExists` doesn't check ContextBudget, this should be safe.

Actually wait, I should also check: does the existing test `TestResolveUsesLegacyWhenNoProfileRouteExists` check `route.ContextBudget`? Let me re-read it:

```go
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
    if route.Preset.LocalOnly {
        t.Fatal("legacy preset LocalOnly = true, want false")
    }
}
```

No, it doesn't check ContextBudget. So adding a non-zero ContextBudget won't break this test.

- [ ] **Step 2: Write the test**
- [ ] **Step 3: Run the new test to verify it fails**
  Expected: FAIL — `MaxRepoContextTokens` and `MaxConversationTokens` are both 0
- [ ] **Step 4: Implement the fix**

In `internal/llm/routing/router.go`, modify `legacyRoute` to include a default `ContextBudget`:

```go
func (r *StaticRouter) legacyRoute(role AgentRole) (Route, bool) {
	if r.config.LegacyProvider == "" || r.config.LegacyModel == "" {
		return Route{}, false
	}
	return Route{
		Role:    role,
		Profile: "legacy",
		Preset: ModelPreset{
			Name:     "legacy",
			Provider: r.config.LegacyProvider,
			Model:    r.config.LegacyModel,
		},
		ContextBudget: ContextBudget{
			MaxRepoContextTokens:  8000,
			MaxConversationTokens: 4000,
		},
		Legacy: true,
	}, true
}
```

- [ ] **Step 5: Re-run test to verify it passes**
  Run: `go test ./internal/llm/routing -run 'TestLegacyRouteHasSaneDefaults' -v`
  Expected: PASS

- [ ] **Step 6: Run full routing package tests**
  Run: `go test ./internal/llm/routing -count=1`
  Expected: all tests PASS

- [ ] **Step 7: Run full agent package tests to confirm no downstream regression**
  Run: `go test ./internal/agent -count=1`
  Expected: all tests PASS

- [ ] **Step 8: Commit**

```bash
git add internal/llm/routing/router.go internal/llm/routing/router_test.go
git commit -m "fix(routing): add default ContextBudget to legacy route (F-POL-86)"
```

---

### Task 4: F-POL-87 — Surface error instead of lossy compaction on summarization failure

**Files:**
- Modify: `internal/agent/runner.go:465-474`
- Test: `internal/agent/runner_test.go`

**Problem:** When `summarizeAndContinue` returns an error, the code calls `compactMessages(messages, ...)` in-place. The compacted slice is sent to the model, but the session's message list (used by `buildHistoryMessages`) remains unchanged. On the next iteration, `buildHistoryMessages` replays the *uncompacted* prior transcript alongside the now-compacted live messages — the model sees duplicated content.

**Fix:** Replace the lossy `compactMessages` fallback with a turn-terminating error that also surfaces a user-visible message in the transcript.

- [ ] **Step 1: Read the current code**

Read `internal/agent/runner.go:465-474`.

- [ ] **Step 2: Write the failing test**

Append to `internal/agent/runner_test.go`:

```go
func TestSummarizeAndContinueFailureSkipsLossyFallback(t *testing.T) {
    // Set up a runner where the context budget is tiny and the summarization
    // call fails (provider error). The previous behaviour was to fall back to
    // compactMessages (lossy), which led to transcript duplication. The fixed
    // behaviour must terminate the turn with a clear error.
    p := &scriptedProvider{
        responses: []string{
            `{"rationale":"need big data","action":{"type":"tool_call","tool":"big.tool","args":{}}}`,
            // summarization call from summarizeAndContinue — return an error
            "",
        },
        errs: []error{nil, errors.New("simulated summarization failure")},
    }
    reg := registry.New()
    reg.Register(registry.Tool{
        Name: "big.tool", Description: "big tool", Risk: registry.RiskReadOnly,
        Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
            // Return enough content to exceed the token budget.
            return registry.ToolResult{Summary: "big", Content: strings.Repeat("big data ", 500)}, nil
        },
    })
    pol := policy.NewEngine(&config.Config{}, nil)
    state := newTestState(t)
    runner := NewRunner(p, reg, pol, state, "test-model")
    runner.SetForceClass(string(ClassQuestion))
    runner.MaxTurnContextTokens = 500 // tiny budget

    task, err := runner.RunTask(context.Background(), "process the big data")
    if err == nil {
        t.Fatal("expected RunTask to return error on summarization failure, got nil")
    }
    if !strings.Contains(err.Error(), "summarization") {
        t.Fatalf("error should mention summarization failure, got: %v", err)
    }
    if task.Status != TaskStatusFailed {
        t.Fatalf("task status = %v, want TaskStatusFailed", task.Status)
    }
}
```

- [ ] **Step 3: Run the new test to verify it fails with the OLD code**

Run: `go test ./internal/agent -run 'TestSummarizeAndContinueFailureSkipsLossyFallback' -v`
Expected: FAIL — the runner successfully falls back to compaction and continues (doesn't error).

Wait, actually, with the old code the behavior is:
1. `chatWithRetry` calls the model
2. Model responds with tool_call → tool executes, returning large content
3. Next iteration: context overflow detected
4. `summarizeAndContinue` called → provider returns error
5. `compactMessages` fallback → messages compacted in-place
6. `chatWithRetry` called again with compacted messages
7. Model responds with final answer (because after the tool result, the model knows enough)

With the old code, would the test actually FAIL? Let me think... The old behavior is to fall back to compactMessages and continue. The model would get the compacted messages and would need to respond. But the test only has 2 responses scripted (the first tool call, and then... nothing for the third call).

Actually, looking at the `scriptedProvider`:
```go
responses: []string{
    `{"rationale":"need big data","action":{"type":"tool_call","tool":"big.tool","args":{}}}`,
    "",
},
errs: []error{nil, errors.New("simulated summarization failure")},
```

The first call returns the tool_call response (responses[0]).
The second call (summarizeAndContinue) returns errs[1] (error).
After the fallback, the third call would try to use responses — but there are only 2 responses. The `scriptedProvider` behavior when `idx >= len(responses)` is to use the last response. So `responses[len(responses)-1]` = `""` (empty string). And no err for idx=2, so the Chat call succeeds but returns an empty string.

Wait, the test has `errs: []error{nil, errors.New("...")}`. For idx=2, `errs[2]` doesn't exist, so no error. The response would be `""` (empty, last response). The runner would then see an empty response and increment `consecutiveEmpty`.

Actually, this is complex. Let me simplify the test. Instead of trying to verify that the old code FAILS, let me just verify that the fixed code returns the expected error. I'll verify both the old and new behavior in the test.

Actually, I can just test the new behavior explicitly. The test verifies that with the fix:
1. The context overflow triggers summarization
2. Summarization fails
3. The turn is terminated with a clear error

Let me make sure the test setup is correct. The issue is that `summarizeAndContinue` calls `r.chatWithRetryNoNativeTools`, which uses `chatWithRetry` with retries. So the first summarization call (idx=1) fails with the error, BUT chatWithRetry retries. The second retry (idx=2) doesn't have an err in errs, so it succeeds with the last response... 

Hmm, this is getting complicated. Let me think about how the retry works.

`chatWithRetryNoNativeTools` calls `chatWithRetryWithNativeTools` which tries up to `r.MaxRetries + 1` (default 3) times. But on each retry, it calls `chatOnce`, which calls `p.Chat(ctx, req)`. The `scriptedProvider.Chat` uses `idx := p.calls` and increments `p.calls` each time. So retries consume additional indices.

Wait, does it? Let me re-read:
```go
func (p *scriptedProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
    idx := p.calls
    p.requests = append(p.requests, req)
    p.calls++
```

Yes, each call to Chat increments p.calls. So if chatWithRetry retries 3 times, it consumes 3 additional indices in the responses/errs arrays.

This makes it tricky to test with a scriptedProvider because the exact number of retries affects which indices are used.

Let me simplify: I'll set `runner.MaxRetries = 0` so there are no retries, making the test deterministic:

```go
runner.MaxRetries = 0
```

With MaxRetries=0, chatWithRetry tries only once. So:
- idx=0: tool_call response
- idx=1: summarizeAndContinue → err (simulated failure)
- No more calls because the runner terminates the turn.

Wait, but `chatWithRetryNoNativeTools` is called inside `summarizeAndContinue`, not inside the main loop. So:
- The main loop's `chatWithRetry` at line 476 calls Chat.
- idx=0: tool_call response returned. Tool executes.
- Back in the loop, context is over budget.
- `summarizeAndContinue` calls `chatWithRetryNoNativeTools`, which calls Chat.
- idx=1: err returned. `summarizeAndContinue` returns err.
- In the old code: fallback to compactMessages. Then `chatWithRetry` at line 476 again.
- idx=2: ??? (depends on what's left in responses)

This is messy. Let me make the test even simpler by making the error scenario complete:

```go
p := &scriptedProvider{
    responses: []string{
        `{"rationale":"need big data","action":{"type":"tool_call","tool":"big.tool","args":{}}}`,
    },
    errs: []error{nil, errors.New("simulated summarization failure")},
}
runner.MaxRetries = 0
```

With MaxRetries=0:
- idx=0: tool_call → succeeds
- idx=1: summarizeAndContinue → err (failure)
- Old code: compactMessages fallback → continue loop → chatWithRetry → idx=2: No more errs, no more responses → uses last response "" → empty response → increment empty counter → eventually stall/finalize
- New code: fail the turn

Actually, with the fix, the test should see err returned from RunTask. Let me just verify that:
1. The fix makes RunTask return an error containing "summarization"
2. The task status is Failed

The test might pass even with the OLD code (if the loop eventually fails for a different reason), but the key is that the error message should be about summarization. Actually no, the old code would NOT return a summarization error — it would silently continue with compacted messages and eventually succeed or fail for a different reason.

Let me design the test more carefully. I need the test to FAIL with old code and PASS with new code.

With old code:
- idx=0: tool_call response → loop continues
- idx=1: error (summarize) → compactMessages fallback → continue loop
- idx=2: no err, last response "" → empty message → consecutiveEmpty increments → eventually finalizes

The old code might either succeed (finalize) or fail with a different error. It won't fail with a "summarization" error. So the test assertion `if !strings.Contains(err.Error(), "summarization")` would fail even with old code (because err might be nil or a different error).

Wait, actually the old code might succeed and return nil error. Let me think...

If the test only has 1 scripted response (tool call) and then an error on idx=1, after the compact fallback, the loop continues. idx=2 has no response and no error, so Chat returns success with empty content. The model goes empty → consecutiveEmpty → maybe finalize → returns success (nil error).

So with the old code, err would be nil, and `if err == nil { t.Fatal(...) }` would trigger. The test FAILS.

With the new code, err would be non-nil and contain "summarization". The test PASSES.

This works! Let me now also handle the empty idx=2 gracefully. Since I set `MaxRetries=0`, idx=1 is the only attempt for the summarization call. But then the loop would continue and try idx=2. To avoid needing more responses, I could add a dummy final response:

Actually, looking at the old code path more carefully - after compact fallback, `messages = compactMessages(...)` replaces messages. Then the loop goes back to line 476 `chatWithRetry`. This would call Chat with idx=2. At idx=2, there are no more errs or responses in the arrays. The `scriptedProvider` handles this:
- `idx < len(p.responses)` → false (len=1)
- `len(p.responses) > 0` → true
- `content = p.responses[len(p.responses)-1]` → `""` (empty string)

So the model returns empty text → consecutiveEmpty → finalize (if < 2) or continue with force tool call → eventually reaches max iterations and fails.

Actually, let me add enough responses to make the old code path reachable but still predictable. Or better yet, let me use `errors.New` for the error check so the error message is specific.

Let me simplify: just add one more response for the post-compaction call. This way the old code would succeed (take the final path) while the new code returns the summarization error.

```go
p := &scriptedProvider{
    responses: []string{
        `{"rationale":"need big data","action":{"type":"tool_call","tool":"big.tool","args":{}}}`,
        "",  // summarizeAndContinue call — will be overridden by err
        `{"rationale":"done","action":{"type":"final","content":"done"}}`,  // post-compaction fallback
    },
    errs: []error{nil, errors.New("simulated summarization failure")},
}
```

With old code:
- idx=0: tool_call
- idx=1: error (summarize) → compactMessages → continue loop
- idx=2: final response → RunTask returns success (nil err) with Summary="done"

With new code:
- idx=0: tool_call
- idx=1: error (summarize) → fail the turn → RunTask returns error

Test with old code: err is nil → `t.Fatal("expected error, got nil")` fires → FAIL ✓
Test with new code: err is non-nil, contains "summarization" → PASS ✓

This should work! Let me also account for retries. With MaxRetries=0, there are no retries. Let me set that explicitly:

```go
runner.MaxRetries = 0
```

Wait, but `summarizeAndContinue` calls `r.chatWithRetryNoNativeTools` which calls `chatWithRetryWithNativeTools` which uses `r.MaxRetries`. So with MaxRetries=0, the summarization call only tries once and fails immediately. Good.

Actually wait, `summarizeAndContinue` is a method on Runner, not called through the main loop. So `r.MaxRetries` is used. With `runner.MaxRetries = 0`, it won't retry. 

But there's another issue: `chatWithRetry` (the main loop's) uses `MaxRetries` too. With `MaxRetries=0`:
- idx=0: tool_call → succeeds (no retry needed)
- idx=1: summarize → err → fail
- (no idx=2 because we've failed)

For the old code:
- idx=0: tool_call → succeeds
- idx=1: summarize → err → compactMessages fallback
- idx=2: chatWithRetry calls Chat → `scriptedProvider.Chat` → idx=2, responses[2] = `{"rationale":"done",...}` → succeeds

Good, this should work. But wait, with MaxRetries=0 and the main chatWithRetry:
```go
func (r *Runner) chatWithRetryWithNativeTools(...) {
    attempts := r.MaxRetries + 1
    // attempts = 1
    for i := 0; i < attempts; i++ {
        res, err := r.chatOnce(ctx, p, model, messages, includeNativeTools)
        if err == nil { return res, nil }
        lastErr = err
    }
    return chatResult{}, lastErr
}
```

With MaxRetries=0, attempts=1. So each call to chatWithRetry makes exactly 1 Chat call. 

So the index mapping is:
- Main loop, iteration 1: `chatWithRetry` → idx=0 → tool_call response → tool executes → context overflow
- `summarizeAndContinue` → `chatWithRetryNoNativeTools` → `chatWithRetryWithNativeTools` → `chatOnce` → idx=1 → err
- Old code: compactMessages → continue loop, iteration 2: `chatWithRetry` → idx=2 → final response
- New code: fail turn

This is perfect. Let me write the test now.

Actually, one more thing. With the old code, `compactMessages(messages, ...)` is called. This returns a new slice. Then messages = compactMessages(...). Then the loop continues and calls `chatWithRetry`. This would consume idx=2 from the scripted provider.

But wait, in `chatWithRetryWithNativeTools`, it calls `chatOnce`, which internally calls `BuildSystemPromptWithDeferred` and adds messages, etc. But does it also call `p.Chat`? Yes, through `chatOnce`. Let me verify:

Actually, let me not overthink this. The test should work. Let me proceed.

- [ ] **Step 2: Write the test**
- [ ] **Step 3: Run the new test to verify it fails with OLD code**
- [ ] **Step 4: Implement the fix**

In `internal/agent/runner.go`, replace lines 469-473:

```go
} else {
    // Summarization failed (transport error or empty text): fall
    // back to lossy in-place compaction rather than aborting the turn.
    messages = compactMessages(messages, r.MaxTurnContextTokens, compactKeepRecentMessages)
}
```

with:

```go
} else {
    r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Context window exceeded and summarization failed: %s. The turn is being terminated to prevent transcript corruption.", serr), session.ContentTypePlain)
    return task, r.fail(task, fmt.Errorf("context overflow and summarization failed: %w", serr))
}
```

Add `"fmt"` to imports if not present (it is already imported in runner.go).

- [ ] **Step 5: Re-run test to verify it passes**
- [ ] **Step 6: Run full agent package tests**
- [ ] **Step 7: Build the project**
- [ ] **Step 8: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "fix(agent): terminate turn on summarization failure instead of lossy compaction (F-POL-87)"
```

---

### Final Verification

Run: `go test ./...`
Expected: all tests PASS.

---

## Self-Review

1. **Spec coverage:**
   - D1 followup → Task 1 (doc comment refinement).
   - F-BUG-73 → Task 2 (fallback error precedence).
   - F-POL-86 → Task 3 (default ContextBudget for legacy route).
   - F-POL-87 → Task 4 (error path instead of lossy compaction).

2. **Edge cases:**
   - Task 2: The error returned changes from the primary role's wrapping to the fallback role's wrapping. Both are `errRoleNotConfigured` sentinel; the difference is the wrapped role name. The test asserts the role name in the error string.
   - Task 3: The default values (8000/4000) are conservative and will not break any existing test that doesn't check ContextBudget on legacy routes. The existing `TestResolveUsesLegacyWhenNoProfileRouteExists` doesn't check ContextBudget.
   - Task 4: The old `TestSummarizeAndContinueErrorsOnEmptySummary` (handoff_test.go) tests that `summarizeAndContinue` returns an error on empty summary — this is a unit test of the function itself, not the caller's fallback path. It remains correct. No existing test exercises the lossy-compaction fallback path, so the behavior change is safe.

3. **No TODOs or TBDs.**
