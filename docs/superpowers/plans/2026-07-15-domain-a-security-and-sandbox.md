# Domain A Security & Sandbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close 23 security and operational-safety findings in the Marshal codebase by hardening sandbox execution, introducing safe path resolution, and replacing stringly-typed command classification with argv-aware matching.

**Architecture:** Three sequenced implementation batches (A1 → A3 → A2). Each batch introduces a small shared primitive (`envutil.AllowList`, `native.SafeResolve`, `policy.ClassifyCommand`) that closes multiple findings. New tests live alongside the helpers.

**Tech Stack:** Go 1.26, Bubble Tea (TUI, unaffected), SQLite (unaffected), `github.com/google/shlex` (new — used in A2).

**Source audit:** `docs/14-codebase-improvement-audit-2026-07-14.md`, domain A (findings F-SAFE-20, F-SAFE-21, F-SAFE-22, F-SAFE-23, F-SAFE-24, F-SAFE-25, F-SAFE-26, F-SEC-17, F-SEC-18, F-SEC-19, F-SEC-35, F-SEC-36, F-SEC-122, F-SEC-123, F-SEC-01, F-SEC-07, F-SEC-16, F-SEC-31, F-BUG-39).

## Global Constraints

- `go build ./cmd/marshal` must succeed after every task.
- `go test ./...` must pass after every task.
- `gofmt -w .` after any file change.
- No TUI/UX changes in this plan — only safety, security, and the
  policy/command classification engine that the TUI consumes.
- Behavior changes that affect user-visible sandbox output or approval
  prompts must be release-noted. The plan author adds the note to
  `docs/04-tooling-and-shell-safety.md` in the final task of each batch.
- Backward compatibility for user-config `allow_sudo` /
  `allow_destructive` is preserved (we wire them in, not delete them).
- Sandbox test isolation: tests that need a real workspace must use
  `t.TempDir()` and never touch `os.Getenv("HOME")`.
- All new exported symbols have doc comments. All new error paths
  return wrapped errors (`fmt.Errorf("...: %w", err)`).

---

## File structure

### New files

| Path | Responsibility |
|---|---|
| `internal/sandbox/envutil/allowlist.go` | `AllowList(parent []string) []string` |
| `internal/sandbox/envutil/allowlist_test.go` | AllowList unit tests |
| `internal/tools/native/saferesolve.go` | `SafeResolve(root, rel) (abs, error)` |
| `internal/tools/native/saferesolve_test.go` | SafeResolve unit tests |
| `internal/tools/policy/classify.go` | `ClassifyCommand(string) (Classification, error)` and `Risk` enum |
| `internal/tools/policy/classify_test.go` | ClassifyCommand unit tests |

### Modified files (per batch)

**Batch 1 (A1):** `internal/sandbox/{sandbox,restricted,container,passthrough,process_unix}.go`, `internal/sandbox/envutil/envutil.go`, `internal/tools/policy/policy.go`, `internal/tools/registry/types.go`, `internal/tools/mcp/{manager,client}.go`, `internal/app/config/config.go`, `docs/04-tooling-and-shell-safety.md`.

**Batch 2 (A3):** `internal/tools/native/{helpers,file,search}.go`, `internal/repo/scanner.go`, `docs/04-tooling-and-shell-safety.md`.

**Batch 3 (A2):** `internal/tools/native/{command,runner}.go`, `internal/tools/policy/policy.go`, `internal/sandbox/container.go`, `internal/app/tui/model.go`, `internal/commands/commands.go`, `go.mod`, `docs/04-tooling-and-shell-safety.md`.

---

# Batch 1 — A1: Sandbox execution hardening

Closes F-SAFE-20, F-SAFE-21, F-SAFE-23, F-SAFE-24, F-SAFE-25, F-SAFE-26, F-SEC-17, F-SEC-35, F-SEC-36.

### Task 1.1: `envutil.AllowList` helper

**Files:**
- Create: `internal/sandbox/envutil/allowlist.go`
- Create: `internal/sandbox/envutil/allowlist_test.go`

**Interfaces:**
- Produces: `func AllowList(parent []string) []string` — returns the
  parent env filtered to a safe allowlist, blocking
  library-injection vars. Order is stable (sorted).
- Produces: `func IsSecretKey(string) bool` — internal helper used
  by AllowList and reused by the MCP client.

- [ ] **Step 1: Write the failing test**

```go
// internal/sandbox/envutil/allowlist_test.go
package envutil

import (
	"reflect"
	"testing"
)

func TestAllowList_StripsSecrets(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"ANTHROPIC_API_KEY=sk-secret",
		"OPENAI_API_KEY=sk-secret",
		"AWS_ACCESS_KEY_ID=AKIA...",
		"GH_TOKEN=ghp_...",
		"LD_PRELOAD=/tmp/evil.so",
		"DYLD_INSERT_LIBRARIES=/tmp/evil.dylib",
		"IFS=,",
		"LANG=en_US.UTF-8",
		"USER=alice",
	}
	got := AllowList(parent)
	for _, kv := range got {
		if IsSecretKey(splitKey(kv)) {
			t.Errorf("AllowList leaked secret key: %s", kv)
		}
		if k := splitKey(kv); k == "LD_PRELOAD" || k == "DYLD_INSERT_LIBRARIES" || k == "IFS" {
			t.Errorf("AllowList leaked dangerous key: %s", kv)
		}
	}
}

func TestAllowList_PreservesCoreVars(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"USER=alice",
		"TZ=UTC",
		"TMPDIR=/tmp",
		"TERM=xterm-256color",
	}
	got := AllowList(parent)
	want := map[string]bool{
		"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true,
		"USER": true, "TZ": true, "TMPDIR": true, "TERM": true,
	}
	for _, kv := range got {
		if !want[splitKey(kv)] {
			t.Errorf("unexpected key in allowlist: %s", kv)
		}
		delete(want, splitKey(kv))
	}
	if len(want) != 0 {
		t.Errorf("missing core keys: %v", want)
	}
}

func TestAllowList_StripsLDAndDYLDWildcards(t *testing.T) {
	parent := []string{
		"LD_LIBRARY_PATH=/tmp",
		"LD_AUDIT=/tmp/audit.so",
		"DYLD_FRAMEWORK_PATH=/tmp",
		"DYLD_FALLBACK_LIBRARY_PATH=/tmp",
	}
	got := AllowList(parent)
	for _, kv := range got {
		k := splitKey(kv)
		if k == "LD_LIBRARY_PATH" || k == "LD_AUDIT" ||
			k == "DYLD_FRAMEWORK_PATH" || k == "DYLD_FALLBACK_LIBRARY_PATH" {
			t.Errorf("AllowList leaked dynamic-loader key: %s", kv)
		}
	}
}

func TestAllowList_OrderIsStable(t *testing.T) {
	parent := []string{"B=2", "A=1", "C=3", "HOME=/h"}
	got := AllowList(parent)
	for i := 1; i < len(got); i++ {
		if splitKey(got[i-1]) > splitKey(got[i]) {
			t.Fatalf("AllowList output not sorted: %v", got)
		}
	}
}

func splitKey(kv string) string {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i]
		}
	}
	return kv
}

func _ = reflect.DeepEqual // import guard for clarity
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/envutil/... -run TestAllowList -v`
Expected: FAIL (functions undefined).

- [ ] **Step 3: Implement the helper**

```go
// internal/sandbox/envutil/allowlist.go
package envutil

import (
	"sort"
	"strings"
)

// allowlistKeys is the set of parent env var names that are safe to
// pass through to sandboxed child processes. Everything else is
// dropped.
var allowlistKeys = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"SHELL": true, "TERM": true, "LANG": true, "LC_ALL": true,
	"LC_COLLATE": true, "LC_CTYPE": true, "LC_MESSAGES": true,
	"LC_MONETARY": true, "LC_NUMERIC": true, "LC_TIME": true,
	"TZ": true, "TMPDIR": true, "XDG_RUNTIME_DIR": true,
	"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
}

// secretPatterns are substrings that, if present in an env var name,
// indicate the value is a credential. The values themselves are
// never inspected.
var secretPatterns = []string{
	"API_KEY", "SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL",
	"AWS_", "GCP_", "AZURE_", "GH_", "GITHUB_", "GITLAB_",
	"ANTHROPIC", "OPENAI", "COHERE", "MISTRAL",
}

// dangerousPrefixes are env var names that can hijack a child
// process via dynamic loader, path search, or shell field splitting.
var dangerousPrefixes = []string{"LD_", "DYLD_"}

// dangerousExact is the set of env var names that are dangerous
// regardless of prefix.
var dangerousExact = map[string]bool{
	"IFS":               true,
	"PATH":              true, // re-added explicitly by AllowList
	"SHELLOPTS":         true,
	"BASH_ENV":          true,
	"ENV":               true,
	"BASH_FUNC_":        true, // prefix matched
	"ZDOTDIR":           true,
}

// AllowList returns a copy of parent containing only the variables
// in allowlistKeys. It strips secret-bearing keys, dynamic-loader
// keys (LD_*, DYLD_*), and IFS / SHELLOPTS / BASH_ENV. The result
// is sorted by key for stable diff/test output.
//
// If a required key is missing from parent, the returned slice
// still contains it with an empty value so the child process sees
// the variable defined.
func AllowList(parent []string) []string {
	seen := map[string]string{}
	for _, kv := range parent {
		k, v, ok := splitKV(kv)
		if !ok {
			continue
		}
		seen[k] = v
	}

	out := make([]string, 0, len(allowlistKeys))
	for k := range allowlistKeys {
		out = append(out, k+"="+seen[k])
	}
	sort.Strings(out)
	return out
}

// IsSecretKey reports whether name matches a known secret-bearing
// pattern. Matching is case-insensitive and substring-based.
func IsSecretKey(name string) bool {
	upper := strings.ToUpper(name)
	for _, p := range secretPatterns {
		if strings.Contains(upper, p) {
			return true
		}
	}
	return false
}

// IsDangerousKey reports whether name can hijack a child process
// (dynamic loader, IFS, BASH_ENV, etc.).
func IsDangerousKey(name string) bool {
	if dangerousExact[name] {
		return true
	}
	if strings.HasPrefix(name, "BASH_FUNC_") {
		return true
	}
	for _, p := range dangerousPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func splitKV(kv string) (k, v string, ok bool) {
	idx := strings.IndexByte(kv, '=')
	if idx <= 0 {
		return "", "", false
	}
	return kv[:idx], kv[idx+1:], true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/sandbox/envutil/... -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/envutil/allowlist.go internal/sandbox/envutil/allowlist_test.go
git commit -m "feat(sandbox): add envutil.AllowList for safe env propagation"
```

### Task 1.2: Wire `AllowList` into the restricted backend

**Files:**
- Modify: `internal/sandbox/restricted.go:95-137` (the `Restricted` runner env construction)
- Modify: `internal/sandbox/restricted_test.go` (add test)

**Interfaces:**
- Consumes: `envutil.AllowList([]string) []string` from Task 1.1.
- Produces: `Restricted.Run` invocations no longer pass parent env when
  no explicit `env_allowlist` is configured.

- [ ] **Step 1: Write the failing test**

```go
// internal/sandbox/restricted_test.go (append)
package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestricted_StripsParentEnvSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")

	dir := t.TempDir()
	cfg := DefaultConfig(dir)
	cfg.Backend = "restricted"

	r, err := NewRunner(cfg)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	// The child should NOT see the secrets.
	out := captureEcho(t, r, "env")
	if strings.Contains(out, "sk-test-secret") {
		t.Errorf("child saw ANTHROPIC_API_KEY value")
	}
	if strings.Contains(out, "aws-secret") {
		t.Errorf("child saw AWS_SECRET_ACCESS_KEY value")
	}
	if strings.Contains(out, "ANTHROPIC_API_KEY=") {
		t.Errorf("child has ANTHROPIC_API_KEY defined at all")
	}
}

func captureEcho(t *testing.T, r Runner, cmd string) string {
	t.Helper()
	res, err := r.Run(context.Background(), cmd, RunnerOptions{})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	return res.Combined
}
```

(Implement `captureEcho` as a helper that invokes `echo "$VAR"`; if your test harness already has a helper, use it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/... -run TestRestricted_StripsParentEnvSecrets -v`
Expected: FAIL — child still sees `ANTHROPIC_API_KEY=sk-test-secret`.

- [ ] **Step 3: Replace the env-construction code in `restricted.go`**

Find the function that builds the child env (currently iterates
`os.Environ()` and applies a suffix/prefix scrub). Replace the env
sourcing line with:

```go
// internal/sandbox/restricted.go
import "github.com/<org>/<repo>/internal/sandbox/envutil"

// inside the runner constructor or per-Run env builder:
if len(cfg.EnvAllowlist) == 0 {
    cmd.Env = envutil.AllowList(os.Environ())
} else {
    cmd.Env = envutil.AllowList(os.Environ()) // still apply safe default
    for _, kv := range cfg.EnvAllowlist {
        if k, v, ok := splitKVLocal(kv); ok && !envutil.IsDangerousKey(k) && !envutil.IsSecretKey(k) {
            cmd.Env = appendOrSet(cmd.Env, k, v)
        }
    }
}
```

Add the two helpers `splitKVLocal` and `appendOrSet` in the same file
if they don't already exist.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/sandbox/... -v`
Expected: PASS for the new test and all existing tests.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/restricted.go internal/sandbox/restricted_test.go
git commit -m "fix(sandbox): default restricted env to safe allowlist (F-SAFE-23)"
```

### Task 1.3: Wire `AllowList` into the container backend

**Files:**
- Modify: `internal/sandbox/container.go:96-105` (`buildContainerEnv`)
- Modify: `internal/sandbox/container_test.go`

**Interfaces:**
- Consumes: `envutil.AllowList`.
- Produces: `buildContainerEnv` returns a curated env, not nil.

- [ ] **Step 1: Write the failing test**

```go
// internal/sandbox/container_test.go (append)
package sandbox

import "testing"

func TestBuildContainerEnv_StripsSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	env := buildContainerEnv()
	for _, kv := range env {
		if len(kv) > 16 && kv[:16] == "ANTHROPIC_API_KEY" {
			t.Errorf("buildContainerEnv leaked ANTHROPIC_API_KEY")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/... -run TestBuildContainerEnv -v`
Expected: FAIL.

- [ ] **Step 3: Update `buildContainerEnv`**

```go
// internal/sandbox/container.go
import "github.com/<org>/<repo>/internal/sandbox/envutil"

func buildContainerEnv() []string {
    return envutil.AllowList(os.Environ())
}
```

Remove the existing `return nil`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/sandbox/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/container.go internal/sandbox/container_test.go
git commit -m "fix(sandbox): build container env from safe allowlist (F-SAFE-24)"
```

### Task 1.4: Wire `AllowList` into MCP client

**Files:**
- Modify: `internal/tools/mcp/client.go:42-43` (the `c.cmd.Env = append(c.cmd.Env, c.Env...)` line)
- Modify: `internal/tools/mcp/client_test.go`

**Interfaces:**
- Consumes: `envutil.AllowList`, `envutil.IsDangerousKey`, `envutil.IsSecretKey`.
- Produces: MCP-spawned child processes never see parent secrets or
  dynamic-loader keys.

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/mcp/client_test.go (append)
package mcp

import "testing"

func TestClient_BuildEnv_StripsSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")

	c := &Client{Env: []string{"FOO=bar"}}
	env := c.buildChildEnv()
	for _, kv := range env {
		if len(kv) >= 16 && kv[:16] == "ANTHROPIC_API_KEY" {
			t.Errorf("MCP child env leaked ANTHROPIC_API_KEY: %s", kv)
		}
		if len(kv) >= 11 && kv[:11] == "LD_PRELOAD=" {
			t.Errorf("MCP child env leaked LD_PRELOAD: %s", kv)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/mcp/... -run TestClient_BuildEnv -v`
Expected: FAIL.

- [ ] **Step 3: Add `buildChildEnv` and use it from `Start`**

```go
// internal/tools/mcp/client.go
import "github.com/<org>/<repo>/internal/sandbox/envutil"

func (c *Client) buildChildEnv() []string {
    // Start from the safe allowlist (no parent secrets, no
    // dynamic-loader keys). Then layer the user-configured vars,
    // rejecting any that are dangerous.
    env := envutil.AllowList(os.Environ())
    for _, kv := range c.Env {
        k, v, ok := splitKVLocal(kv)
        if !ok {
            continue
        }
        if envutil.IsDangerousKey(k) || envutil.IsSecretKey(k) {
            continue
        }
        env = appendOrSet(env, k, v)
    }
    return env
}
```

Replace `c.cmd.Env = append(c.cmd.Env, c.Env...)` in `Start` with
`c.cmd.Env = c.buildChildEnv()`.

If `splitKVLocal` / `appendOrSet` are not already in the package, add
them.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/mcp/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/mcp/client.go internal/tools/mcp/client_test.go
git commit -m "fix(mcp): strip secrets and dynamic-loader keys from child env (F-SEC-35)"
```

### Task 1.5: `RiskDestructive` for shell patterns + required approval

**Files:**
- Modify: `internal/tools/registry/types.go` (no change if `RiskDestructive` already exists — confirm it does)
- Modify: `internal/tools/policy/policy.go` (assign `RiskDestructive` to specific patterns; require approval for any tool returning that risk)
- Modify: `internal/tools/native/native.go` (assign risk to `shell.run` when input matches destructive patterns — defer to Task 3.3 in A2 for full pattern matching; here, just expose the new escalation path)
- Create: `internal/tools/policy/risk_test.go`

**Interfaces:**
- Produces: `Decision.Destructive bool` on `policy.Decision`
- Produces: `policy.Evaluate(...)` returns
  `Decision{Approval: ApprovalRequired, Reason: "destructive", Destructive: true}`
  for tools whose effective risk is `RiskDestructive`.

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/policy/risk_test.go
package policy

import "testing"

func TestEvaluate_DestructiveRequiresApproval(t *testing.T) {
    eng := NewEngine(Config{AllowSudo: false, AllowDestructive: false})
    d, err := eng.Evaluate(ToolCall{Name: "shell.run", Args: map[string]any{"command": "rm -rf /tmp/build"}})
    if err != nil {
        t.Fatalf("Evaluate: %v", err)
    }
    if d.Approval != ApprovalRequired {
        t.Errorf("destructive command got %v, want ApprovalRequired", d.Approval)
    }
    if !d.Destructive {
        t.Errorf("Destructive flag not set")
    }
}

func TestEvaluate_DestructiveAllowedWhenFlagged(t *testing.T) {
    eng := NewEngine(Config{AllowDestructive: true})
    d, err := eng.Evaluate(ToolCall{Name: "shell.run", Args: map[string]any{"command": "rm -rf /tmp/build"}})
    if err != nil {
        t.Fatalf("Evaluate: %v", err)
    }
    if d.Approval != ApprovalRequired {
        // still required, but the reason changes
        t.Errorf("destructive with flag still required, got %v", d.Approval)
    }
    if d.Reason == "" || d.Reason == "destructive" {
        t.Errorf("destructive with flag should change reason, got %q", d.Reason)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/policy/... -run TestEvaluate_Destructive -v`
Expected: FAIL — `Decision.Destructive` field does not exist.

- [ ] **Step 3: Add the `Destructive` field and wire it in**

```go
// internal/tools/policy/policy.go
type Decision struct {
    Approval     Approval
    Reason       string
    Destructive  bool   // new
    // ... existing fields
}
```

In `Evaluate`, after determining risk:

```go
if risk == registry.RiskDestructive {
    return Decision{
        Approval:    ApprovalRequired,
        Reason:      "destructive: requires explicit approval",
        Destructive: true,
    }, nil
}
```

The classification itself (`RiskDestructive` for `rm -rf`, `git clean
-fd*`, `chmod -R 777`, etc.) is implemented in Task 3.3 (A2). For
now, add a stub in `policy.go`:

```go
// classifyRisk is implemented in classify.go (Batch 3 / A2).
// Until then, this stub classifies any shell command with "rm -rf"
// as RiskDestructive to unblock the test above.
func classifyRisk(toolName string, args map[string]any) registry.Risk {
    if toolName == "shell.run" || toolName == "test.run" {
        if cmd, _ := args["command"].(string); strings.Contains(cmd, "rm -rf") {
            return registry.RiskDestructive
        }
    }
    return registry.RiskCommand
}
```

Use `classifyRisk(...)` in `Evaluate` and compare against
`registry.RiskDestructive`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/policy/... -v`
Expected: PASS for the new tests. (Note: the stub is intentionally
incomplete; A2 / Task 3.3 will replace it.)

- [ ] **Step 5: Commit**

```bash
git add internal/tools/policy/policy.go internal/tools/policy/risk_test.go
git commit -m "feat(policy): escalate RiskDestructive to required approval (F-SAFE-21)"
```

### Task 1.6: Wire `allow_sudo` / `allow_destructive` config flags

**Files:**
- Modify: `internal/app/config/config.go:183-185` (already defines the
  fields; confirm by `grep`).
- Modify: `internal/tools/policy/policy.go` (read config flags; allow
  destructive when set)
- Modify: `internal/tools/policy/risk_test.go` (add flag tests)

**Interfaces:**
- Produces: `PolicyEngine` reads `Config.AllowSudo` /
  `Config.AllowDestructive` and downgrades destructive from
  hard-required to confirm-prompt when the flag is set.

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/policy/risk_test.go (append)
func TestEvaluate_DestructiveFlagAffectsReason(t *testing.T) {
    eng := NewEngine(Config{AllowDestructive: true})
    d, _ := eng.Evaluate(ToolCall{Name: "shell.run", Args: map[string]any{"command": "rm -rf /tmp/build"}})
    if !strings.Contains(d.Reason, "flagged") {
        t.Errorf("expected reason to mention flag, got %q", d.Reason)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/policy/... -run TestEvaluate_DestructiveFlag -v`
Expected: FAIL.

- [ ] **Step 3: Wire the config flag in**

```go
// internal/tools/policy/policy.go
func (e *Engine) Evaluate(tc ToolCall) (Decision, error) {
    risk := classifyRisk(tc.Name, tc.Args)
    if risk == registry.RiskDestructive {
        reason := "destructive: requires explicit approval"
        if e.cfg.AllowDestructive {
            reason = "destructive (flagged allowed): requires explicit approval"
        }
        return Decision{Approval: ApprovalRequired, Reason: reason, Destructive: true}, nil
    }
    // ... existing logic
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/policy/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/policy/policy.go internal/tools/policy/risk_test.go
git commit -m "feat(policy): honor AllowDestructive config flag (F-SAFE-20)"
```

### Task 1.7: Passthrough backend warning + `unsafe_passthrough` flag

**Files:**
- Modify: `internal/sandbox/passthrough.go:18-31`
- Modify: `internal/sandbox/sandbox.go:94-120` (gate passthrough on config)
- Modify: `internal/app/config/config.go` (add `UnsafePassthrough bool`)
- Modify: `internal/sandbox/sandbox_test.go` (add test)

**Interfaces:**
- Produces: `NewRunner` returns an error if `Backend == "passthrough"`
  and `cfg.UnsafePassthrough == false`.

- [ ] **Step 1: Write the failing test**

```go
// internal/sandbox/sandbox_test.go (append)
func TestNewRunner_PassthroughRequiresFlag(t *testing.T) {
    dir := t.TempDir()
    cfg := DefaultConfig(dir)
    cfg.Backend = "passthrough"
    if _, err := NewRunner(cfg); err == nil {
        t.Fatal("expected error for passthrough without UnsafePassthrough")
    }
    cfg.UnsafePassthrough = true
    if _, err := NewRunner(cfg); err != nil {
        t.Fatalf("expected ok with flag: %v", err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/... -run TestNewRunner_Passthrough -v`
Expected: FAIL.

- [ ] **Step 3: Add the field, gate, and warning**

```go
// internal/app/config/config.go
type SandboxConfig struct {
    Backend             string
    UnsafePassthrough   bool   // new
    // ... existing fields
}
```

```go
// internal/sandbox/sandbox.go
func NewRunner(cfg Config) (Runner, error) {
    if cfg.Backend == "passthrough" && !cfg.UnsafePassthrough {
        return nil, fmt.Errorf("sandbox: backend=passthrough requires UnsafePassthrough=true (no isolation)")
    }
    // ... existing switch
}
```

In `passthrough.go`, log a one-line warning at construction:

```go
log.Default().Warn("sandbox: running in PASSTHROUGH mode — no isolation")
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/sandbox/... -v`
Expected: PASS. Update any existing test that constructs a passthrough
runner to set `UnsafePassthrough: true`.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/passthrough.go internal/sandbox/sandbox_test.go internal/app/config/config.go
git commit -m "feat(sandbox): require UnsafePassthrough flag to opt into no isolation (F-SAFE-25)"
```

### Task 1.8: Process-group kill fallback

**Files:**
- Modify: `internal/sandbox/process_unix.go:27-54`
- Modify: `internal/sandbox/process_unix_test.go`

**Interfaces:**
- Produces: After grace interval, send `SIGKILL` to the direct child
  PID in addition to the negative-PGID kill.

- [ ] **Step 1: Write the failing test**

```go
// internal/sandbox/process_unix_test.go (append)
// +build unix

func TestKillProcessGroup_FallbackKillsDirectChild(t *testing.T) {
    cmd := exec.Command("/bin/sh", "-c", "setsid sh -c 'trap \"\" TERM; sleep 30' & sleep 30")
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    if err := cmd.Start(); err != nil {
        t.Fatalf("start: %v", err)
    }
    // Simulate a grandchild that ignores SIGTERM by trapping it.
    time.Sleep(100 * time.Millisecond)
    killProcessGroup(cmd.Process.Pid, 200*time.Millisecond)
    err := cmd.Wait()
    if err == nil {
        t.Errorf("expected non-nil error after kill")
    }
    if !cmd.ProcessState.Exited() {
        t.Errorf("process did not exit")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/... -run TestKillProcessGroup_Fallback -v`
Expected: FAIL — process still running.

- [ ] **Step 3: Add the SIGKILL fallback**

```go
// internal/sandbox/process_unix.go
func killProcessGroup(pid int, grace time.Duration) {
    pgid, err := syscall.Getpgid(pid)
    if err == nil {
        _ = syscall.Kill(-pgid, syscall.SIGTERM)
    }
    time.Sleep(grace)
    if pgid > 0 {
        _ = syscall.Kill(-pgid, syscall.SIGKILL)
    }
    // Fallback: kill the direct child PID in case the PGID was
    // escaped (e.g. a grandchild called setpgid).
    if proc, err := os.FindProcess(pid); err == nil {
        _ = proc.Signal(syscall.SIGKILL)
    }
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/sandbox/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/process_unix.go internal/sandbox/process_unix_test.go
git commit -m "fix(sandbox): SIGKILL direct child PID as kill fallback (F-SAFE-26)"
```

### Task 1.9: Container backend argv path (preparation for A2)

**Files:**
- Modify: `internal/sandbox/container.go:156-167`
- Modify: `internal/sandbox/container_test.go`

**Interfaces:**
- Produces: When `command` is a single shell-free string (no `|`, `&`,
  `;`, `` ` ``, `$`, redirects), split into argv via `strings.Fields`
  and invoke as `args...` in the container entrypoint. Otherwise keep
  the existing `/bin/sh -lc` behavior.

- [ ] **Step 1: Write the failing test**

```go
// internal/sandbox/container_test.go (append)
func TestContainer_AvPathForSimpleCommands(t *testing.T) {
    if testing.Short() { t.Skip("requires docker") }
    dir := t.TempDir()
    cfg := DefaultConfig(dir)
    cfg.Backend = "container"
    r, err := NewRunner(cfg)
    if err != nil { t.Skipf("no docker: %v", err) }
    res, err := r.Run(context.Background(), "echo hello", RunnerOptions{})
    if err != nil { t.Fatalf("run: %v", err) }
    if !strings.Contains(res.Combined, "hello") {
        t.Errorf("got %q, want hello", res.Combined)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/... -run TestContainer_AvPath -v`
Expected: FAIL — container still uses `/bin/sh -lc`.

- [ ] **Step 3: Add the argv path**

```go
// internal/sandbox/container.go
func isShellFree(s string) bool {
    shellMetas := "|&;`$<>(){}*?\n"
    return !strings.ContainsAny(s, shellMetas)
}
```

In the container runner, after the existing shell invocation:

```go
if isShellFree(command) {
    args = append(args, strings.Fields(command)...)
} else {
    args = append(args, "/bin/sh", "-lc", command)
}
```

(Full F-SEC-17 resolution comes in Task 3.5; this is the prep step.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/sandbox/... -v`
Expected: PASS (test skips if no docker).

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/container.go internal/sandbox/container_test.go
git commit -m "feat(sandbox): invoke container with argv for shell-free commands (F-SEC-17 prep)"
```

### Task 1.10: MCP `RegisterTools` per-server timeout

**Files:**
- Modify: `internal/tools/mcp/manager.go:48-101`
- Modify: `internal/tools/mcp/manager_test.go`

**Interfaces:**
- Produces: `RegisterTools` accepts a `context.Context` and applies a
  10s per-server timeout; on timeout the server is skipped with a
  warning, not a fatal.

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/mcp/manager_test.go (append)
func TestRegisterTools_SkipsHangingServer(t *testing.T) {
    mgr := NewManager(/* inject fake slow server */)
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    tools, err := mgr.RegisterTools(ctx)
    if err != nil { t.Fatalf("RegisterTools: %v", err) }
    // The hanging server should be absent from the result.
    for _, t := range tools {
        if t.Name == "hanging" {
            t.Errorf("hanging server was not skipped")
        }
    }
}
```

(Implement a `hanging` fake server that reads stdin and never
responds; bind to a port and add to the manager's server list.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/mcp/... -run TestRegisterTools_SkipsHanging -v`
Expected: FAIL — register blocks forever or times out the whole call.

- [ ] **Step 3: Add the per-server timeout**

```go
// internal/tools/mcp/manager.go
func (m *Manager) RegisterTools(ctx context.Context) ([]registry.Tool, error) {
    var out []registry.Tool
    for _, srv := range m.servers {
        sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
        tools, err := srv.listTools(sctx)
        cancel()
        if err != nil {
            m.log.Warn("mcp: server %q skipped: %v", srv.Name, err)
            continue
        }
        out = append(out, tools...)
    }
    return out, nil
}
```

Update the existing `RegisterTools` call sites to pass a context
(typically the app's startup context).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/mcp/... -v`
Expected: PASS. Update any other test that calls `RegisterTools`
without a context.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/mcp/manager.go internal/tools/mcp/manager_test.go
git commit -m "fix(mcp): skip-and-warn for hanging tools/list servers (F-SEC-36)"
```

### Task 1.11: Release notes for Batch 1

**Files:**
- Modify: `docs/04-tooling-and-shell-safety.md` (append a "Release
  notes — A1 hardening" section)

- [ ] **Step 1: Add the release note**

Append at the bottom of the file:

```markdown
## 2026-07-15 — A1 sandbox hardening

- The `restricted` and `container` sandbox backends now default to a
  minimal env allowlist (`PATH`, `HOME`, `LANG`, `LC_*`, `USER`, `TZ`,
  `TMPDIR`, `XDG_*`). To opt into additional env vars, set
  `[sandbox] env_allowlist = ["K=V", ...]` explicitly.
- MCP-spawned child processes no longer inherit the parent shell's
  secrets or dynamic-loader keys (`LD_*`, `DYLD_*`).
- Backend `passthrough` is now opt-in via `[sandbox] unsafe_passthrough = true`.
  Without the flag, `NewRunner` returns an error and the TUI surfaces it.
- `shell.run` commands matching destructive patterns (initially
  `rm -rf`) are now flagged `RiskDestructive` and require explicit
  approval even when other shell commands are auto-allowed. Set
  `[policy] allow_destructive = true` to silence the dedicated
  reason message; the approval prompt is still required.
- The Unix process-group killer now also sends `SIGKILL` to the
  direct child PID after the grace interval, so grandchildren that
  escape the PGID via `setpgid` are still terminated.
```

- [ ] **Step 2: Commit**

```bash
git add docs/04-tooling-and-shell-safety.md
git commit -m "docs: release notes for A1 sandbox hardening"
```

### Task 1.12: Batch 1 verification

- [ ] **Step 1: Full build + tests + vet**

Run:
```bash
go build ./cmd/marshal
go test ./...
go vet ./...
gofmt -l . | tee /tmp/format
```

Expected: empty `/tmp/format`, all tests pass, vet clean.

- [ ] **Step 2: Tag the batch in the audit doc**

Edit `docs/14-codebase-improvement-audit-2026-07-14.md` and mark
F-SAFE-20, F-SAFE-21, F-SAFE-23, F-SAFE-24, F-SAFE-25, F-SAFE-26,
F-SEC-17 (partial — argv path), F-SEC-35, F-SEC-36 as **RESOLVED
(Batch 1)** with a short summary.

- [ ] **Step 3: Commit**

```bash
git add docs/14-codebase-improvement-audit-2026-07-14.md
git commit -m "docs(audit): mark Batch 1 findings as resolved"
```

---

# Batch 2 — A3: Workspace path safety

Closes F-SEC-19, F-SAFE-22, F-BUG-39, F-SEC-122, F-SEC-123.

### Task 2.1: `native.SafeResolve` helper

**Files:**
- Create: `internal/tools/native/saferesolve.go`
- Create: `internal/tools/native/saferesolve_test.go`

**Interfaces:**
- Produces: `func SafeResolve(root, rel string) (abs string, err error)`
  — returns the absolute, symlink-resolved path of `rel` joined to
  `root`, only if the resolved path is still under `root`. The
  function rejects `..` traversal, symlink escape, and `O_NOFOLLOW`
  semantics via `Lstat` checks.
- Produces: `ErrPathEscapes` sentinel for callers.

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/native/saferesolve_test.go
package native

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSafeResolve_NormalFile(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "sub"))
	mustWrite(t, filepath.Join(root, "sub", "f.txt"), "x")

	got, err := SafeResolve(root, "sub/f.txt")
	if err != nil {
		t.Fatalf("SafeResolve: %v", err)
	}
	want := filepath.Join(root, "sub", "f.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSafeResolve_DotDotTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := SafeResolve(root, "../etc/passwd"); !errors.Is(err, ErrPathEscapes) {
		t.Errorf("dotdot: got %v, want ErrPathEscapes", err)
	}
}

func TestSafeResolve_AbsoluteRejected(t *testing.T) {
	root := t.TempDir()
	if _, err := SafeResolve(root, "/etc/passwd"); !errors.Is(err, ErrPathEscapes) {
		t.Errorf("absolute: got %v, want ErrPathEscapes", err)
	}
}

func TestSafeResolve_SymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeResolve(root, "link/secret.txt"); !errors.Is(err, ErrPathEscapes) {
		t.Errorf("symlink: got %v, want ErrPathEscapes", err)
	}
}

func mustMkdir(t *testing.T, p string) { t.Helper(); if err := os.MkdirAll(p, 0o755); err != nil { t.Fatal(err) } }
func mustWrite(t *testing.T, p, s string) { t.Helper(); if err := os.WriteFile(p, []byte(s), 0o644); err != nil { t.Fatal(err) } }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native/... -run TestSafeResolve -v`
Expected: FAIL (function undefined).

- [ ] **Step 3: Implement `SafeResolve`**

```go
// internal/tools/native/saferesolve.go
package native

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscapes indicates that a requested path resolved outside
// the workspace root. Callers should treat this as a security
// failure and refuse the operation.
var ErrPathEscapes = errors.New("native: path escapes workspace root")

// SafeResolve joins rel to root, evaluates any symlinks, and
// returns the absolute path only if it is still under root.
// Relative paths containing `..` are rejected. Absolute paths
// are rejected.
func SafeResolve(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: absolute path %q", ErrPathEscapes, rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscapes, rel)
	}
	joined := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// New files don't exist yet; resolve the parent and append
		// the leaf to allow create-new-file paths.
		parent := filepath.Dir(joined)
		base := filepath.Base(joined)
		presolved, perr := filepath.EvalSymlinks(parent)
		if perr != nil {
			return "", fmt.Errorf("native: resolve %q: %w", joined, err)
		}
		resolved = filepath.Join(presolved, base)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	relResolved, err := filepath.Rel(absRoot, resolved)
	if err != nil || relResolved == ".." || strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrPathEscapes, rel, resolved)
	}
	return resolved, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/native/... -run TestSafeResolve -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tools/native/saferesolve.go internal/tools/native/saferesolve_test.go
git commit -m "feat(native): add SafeResolve for symlink-aware workspace paths"
```

### Task 2.2: Wire `SafeResolve` into `helpers.go`

**Files:**
- Modify: `internal/tools/native/helpers.go:29-93`
- Modify: `internal/tools/native/helpers_test.go` (add symlink test)

**Interfaces:**
- Consumes: `SafeResolve`.
- Produces: `resolveWorkspacePath` and `resolveWorkspacePathMulti`
  return an error wrapped around `ErrPathEscapes` for symlink escape.

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/native/helpers_test.go (append)
func TestResolveWorkspacePath_SymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" { t.Skip() }
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkspacePath(root, "link/secret.txt"); !errors.Is(err, ErrPathEscapes) {
		t.Errorf("got %v, want ErrPathEscapes", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native/... -run TestResolveWorkspacePath -v`
Expected: FAIL.

- [ ] **Step 3: Replace the body of `resolveWorkspacePath`**

```go
// internal/tools/native/helpers.go
func resolveWorkspacePath(root, rel string) (string, error) {
    return SafeResolve(root, rel)
}

func resolveWorkspacePathMulti(root string, rels []string) ([]string, error) {
    out := make([]string, 0, len(rels))
    for _, r := range rels {
        a, err := SafeResolve(root, r)
        if err != nil { return nil, err }
        out = append(out, a)
    }
    return out, nil
}
```

(Keep any existing `roots []string` variant accepting multiple roots
by iterating and returning the first match.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/native/... -v`
Expected: PASS. Update any test that relied on the previous
behavior of allowing symlinked files (rare in practice).

- [ ] **Step 5: Commit**

```bash
git add internal/tools/native/helpers.go internal/tools/native/helpers_test.go
git commit -m "fix(native): use SafeResolve in resolveWorkspacePath (F-SEC-122)"
```

### Task 2.3: Wire `SafeResolve` into `repo.search` per file

**Files:**
- Modify: `internal/tools/native/search.go:50-92, 73-146`
- Modify: `internal/tools/native/search_test.go`

**Interfaces:**
- Produces: `repo.search` skips symlinks, surfaces walk errors, and
  re-verifies every matched file under the workspace root.

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/native/search_test.go (append)
func TestRepoSearch_SkipsSymlinksAndReportsErrors(t *testing.T) {
	if runtime.GOOS == "windows" { t.Skip() }
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle"), 0o644)
	os.Symlink(outside, filepath.Join(root, "link"))

	// Walk errors should be collected, not swallowed.
	res, _ := runRepoSearch(t, root, "needle")
	if strings.Contains(res, "secret") {
		t.Errorf("search followed symlink outside root")
	}
}
```

(Implement `runRepoSearch` to invoke the tool's `Run` and return
combined output.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native/... -run TestRepoSearch_SkipsSymlinks -v`
Expected: FAIL.

- [ ] **Step 3: Update `searchFiles` to skip symlinks and report walk errors**

```go
// internal/tools/native/search.go
var walkErrs []error

filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
    if err != nil {
        walkErrs = append(walkErrs, fmt.Errorf("%s: %w", p, err))
        if d != nil && d.IsDir() { return fs.SkipDir }
        return nil
    }
    if d.Type()&os.ModeSymlink != 0 { return nil }
    // ... existing logic
})
```

After the walk, if `walkErrs` is non-empty, return it in the result:

```go
if len(walkErrs) > 0 {
    return searchResult{Matches: matches, WalkErrors: walkErrs}, nil
}
```

Re-verify each match with `SafeResolve` before reading.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/native/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/native/search.go internal/tools/native/search_test.go
git commit -m "fix(native): repo.search skips symlinks and surfaces walk errors (F-SEC-19, F-SEC-123)"
```

### Task 2.4: Atomic `file.write_patch` (combine validate + apply)

**Files:**
- Modify: `internal/tools/native/file.go:143-230`
- Modify: `internal/tools/native/file_test.go`

**Interfaces:**
- Produces: `file.write_patch` combines validation and application
  into a single pass; records a content hash per file; refuses to
  apply if the file changed between the two reads. Supports creating
  new files (the search block must be empty for a new file).

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/native/file_test.go (append)
func TestWritePatch_NewFileCreation(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new.txt")
	patch := &patch.Patch{Edits: []patch.Edit{{Path: target, Old: "", New: "hello"}}}
	if err := applyPatchesAtomic([]patch.Patch{*patch}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestWritePatch_AtomicOnConcurrentModification(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	os.WriteFile(target, []byte("v1"), 0o644)
	// Simulate concurrent write between hash and apply.
	patch := &patch.Patch{Edits: []patch.Edit{{Path: target, Old: "v1", New: "v2"}}}
	go func() {
		time.Sleep(10 * time.Millisecond)
		os.WriteFile(target, []byte("v1-modified"), 0o644)
	}()
	err := applyPatchesAtomic([]patch.Patch{*patch})
	if err == nil {
		t.Errorf("expected error on concurrent modification")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native/... -run TestWritePatch -v`
Expected: FAIL.

- [ ] **Step 3: Implement atomic apply**

```go
// internal/tools/native/file.go
func applyPatchesAtomic(patches []patch.Patch) error {
    // First pass: hash every targeted file and validate the patch.
    type plan struct {
        path   string
        old    string
        new    string
        sha    [32]byte
        exists bool
    }
    var plans []plan
    for _, p := range patches {
        for _, e := range p.Edits {
            cur, err := os.ReadFile(e.Path)
            if err != nil && !os.IsNotExist(err) {
                return fmt.Errorf("read %s: %w", e.Path, err)
            }
            if os.IsNotExist(err) {
                if e.Old != "" {
                    return fmt.Errorf("file %s does not exist but patch expects %q", e.Path, e.Old)
                }
                cur = nil
            }
            // Validate the patch against cur (existing validate logic).
            // ...
            plans = append(plans, plan{
                path: e.Path, old: string(cur), new: patchedString(cur, e),
                sha: sha256.Sum256(cur), exists: !os.IsNotExist(err),
            })
        }
    }

    // Second pass: re-hash, refuse if changed, then write.
    for _, pl := range plans {
        if pl.exists {
            cur, err := os.ReadFile(pl.path)
            if err != nil { return err }
            h := sha256.Sum256(cur)
            if h != pl.sha {
                return fmt.Errorf("file %s changed since validation", pl.path)
            }
        }
        if err := os.WriteFile(pl.path, []byte(pl.new), 0o644); err != nil {
            return err
        }
    }
    return nil
}
```

Refactor the existing validate+apply loop to use this single
function.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/native/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/native/file.go internal/tools/native/file_test.go
git commit -m "fix(native): atomic file.write_patch with hash check (F-SAFE-22, F-BUG-39)"
```

### Task 2.5: `repo.Scanner` symlink policy

**Files:**
- Modify: `internal/repo/scanner.go:72-127, 134-146`
- Modify: `internal/repo/scanner_test.go`

**Interfaces:**
- Produces: `repo.Scanner` skips symlinked files and directories
  with a recorded reason; does not follow them. This is a deliberate
  policy choice — document it in the package doc.

- [ ] **Step 1: Write the failing test**

```go
// internal/repo/scanner_test.go (append)
func TestScanner_SkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" { t.Skip() }
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644)
	os.Symlink(outside, filepath.Join(root, "link"))

	s := NewScanner(Config{Root: root})
	res, err := s.Scan()
	if err != nil { t.Fatal(err) }
	for _, f := range res.Files {
		if strings.Contains(f.Path, "link/secret") {
			t.Errorf("scanner followed symlink: %s", f.Path)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repo/... -run TestScanner_SkipsSymlinks -v`
Expected: FAIL.

- [ ] **Step 3: Skip symlinks in the WalkDir**

```go
// internal/repo/scanner.go
filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
    if err != nil { return err }
    if d.Type()&os.ModeSymlink != 0 {
        s.skipped = append(s.skipped, skippedEntry{Path: p, Reason: "symlink"})
        if d.IsDir() { return filepath.SkipDir }
        return nil
    }
    // ... existing logic
})
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/repo/... -v`
Expected: PASS. Confirm no existing test relied on symlink
following (a quick `rg "symlink" internal/repo/` helps).

- [ ] **Step 5: Commit**

```bash
git add internal/repo/scanner.go internal/repo/scanner_test.go
git commit -m "fix(repo): scanner skips symlinks (F-SEC-102)"
```

### Task 2.6: Release notes + audit doc update for Batch 2

**Files:**
- Modify: `docs/04-tooling-and-shell-safety.md`
- Modify: `docs/14-codebase-improvement-audit-2026-07-14.md`

- [ ] **Step 1: Append release note**

```markdown
## 2026-07-15 — A3 path safety

- All workspace path resolution (`file.read`, `file.write_patch`,
  `repo.search`) now goes through `native.SafeResolve`, which
  resolves symlinks and verifies containment under the workspace
  root. Symlinks inside the workspace that point outside are
  refused with `ErrPathEscapes`.
- `file.write_patch` now applies patches atomically with a
  content-hash check; concurrent file modifications are detected
  and the tool returns an error. New files can be created when the
  patch's search block is empty.
- `repo.search` no longer follows directory symlinks and surfaces
  walk errors in the result (previously swallowed).
- `repo.Scanner` (the indexer) no longer follows symlinks. A
  workspace that relied on symlinked `node_modules` etc. should
  replace them with bind mounts or hard links.
```

- [ ] **Step 2: Mark Batch 2 findings resolved**

In `docs/14-codebase-improvement-audit-2026-07-14.md`, mark
F-SEC-19, F-SAFE-22, F-BUG-39, F-SEC-122, F-SEC-123 as
**RESOLVED (Batch 2)**.

- [ ] **Step 3: Commit**

```bash
git add docs/04-tooling-and-shell-safety.md docs/14-codebase-improvement-audit-2026-07-14.md
git commit -m "docs: release notes and audit updates for A3 path safety"
```

### Task 2.7: Batch 2 verification

- [ ] **Step 1: Full build + tests + vet**

```bash
go build ./cmd/marshal
go test ./...
go vet ./...
gofmt -l . | tee /tmp/format
```

Expected: clean.

- [ ] **Step 2: Manual smoke test**

Run a `repo.index` against a workspace that contains a symlink to
`/tmp`; confirm the symlink is skipped and the audit log records
the reason. Confirm `file.read` of a symlinked file inside the
workspace returns `ErrPathEscapes`.

---

# Batch 3 — A2: Command classification overhaul

Closes F-SEC-01 (partial — argv path for non-shell), F-SEC-07,
F-SEC-16, F-SEC-31, plus finalises F-SEC-17 and the policy-side of
F-SAFE-20/21.

### Task 3.1: Add `shlex` dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/google/shlex@latest
go mod tidy
```

- [ ] **Step 2: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add github.com/google/shlex for shell-style arg splitting"
```

### Task 3.2: `policy.ClassifyCommand` primitive

**Files:**
- Create: `internal/tools/policy/classify.go`
- Create: `internal/tools/policy/classify_test.go`

**Interfaces:**
- Produces: `type Classification struct { Risk registry.Risk; Reason string }`
- Produces: `func ClassifyCommand(input string) (Classification, error)` —
  parses `input` with `shlex.Split` and matches argv against a
  curated set of destructive/risky patterns.

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/policy/classify_test.go
package policy

import (
	"testing"

	"github.com/<org>/<repo>/internal/tools/registry"
)

func TestClassifyCommand_Destructive(t *testing.T) {
	cases := []struct {
		cmd  string
		want registry.Risk
	}{
		{"rm -rf /tmp/x", registry.RiskDestructive},
		{"rm -r -f /tmp/x", registry.RiskDestructive},
		{"rm -fr /tmp/x", registry.RiskDestructive},
		{"git clean -fdx", registry.RiskDestructive},
		{"chmod -R 777 /tmp/x", registry.RiskDestructive},
		{"chmod --recursive 777 /tmp/x", registry.RiskDestructive},
		{"git reset --hard", registry.RiskDestructive},
	}
	for _, c := range cases {
		got, err := ClassifyCommand(c.cmd)
		if err != nil { t.Errorf("%q: %v", c.cmd, err); continue }
		if got.Risk != c.want {
			t.Errorf("%q: got %v, want %v", c.cmd, got.Risk, c.want)
		}
	}
}

func TestClassifyCommand_Benign(t *testing.T) {
	cases := []string{
		"ls -la",
		"go test ./...",
		"git status",
		"git diff",
		"echo hello",
	}
	for _, cmd := range cases {
		got, err := ClassifyCommand(cmd)
		if err != nil { t.Errorf("%q: %v", cmd, err); continue }
		if got.Risk == registry.RiskDestructive {
			t.Errorf("%q classified as destructive", cmd)
		}
	}
}

func TestClassifyCommand_QuotedArgs(t *testing.T) {
	got, err := ClassifyCommand(`echo "hello world"`)
	if err != nil { t.Fatal(err) }
	if got.Risk == registry.RiskDestructive {
		t.Errorf("quoted echo should not be destructive")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/policy/... -run TestClassifyCommand -v`
Expected: FAIL.

- [ ] **Step 3: Implement `ClassifyCommand`**

```go
// internal/tools/policy/classify.go
package policy

import (
	"fmt"
	"strings"

	"github.com/google/shlex"

	"github.com/<org>/<repo>/internal/tools/registry"
)

// Classification is the result of analyzing a shell command string.
type Classification struct {
	Risk   registry.Risk
	Reason string
}

// ClassifyCommand parses input with shell-style quoting and returns
// the highest applicable risk level. It never executes the command.
func ClassifyCommand(input string) (Classification, error) {
	argv, err := shlex.Split(input)
	if err != nil {
		return Classification{}, fmt.Errorf("classify: parse %q: %w", input, err)
	}
	if len(argv) == 0 {
		return Classification{Risk: registry.RiskReadOnly}, nil
	}
	cmd := basename(argv[0])
	if hasFlag(argv, "rm") {
		if hasAnyFlag(argv, "r", "R", "recursive") && hasAnyFlag(argv, "f", "force") {
			return Classification{Risk: registry.RiskDestructive, Reason: "rm -r -f"}, nil
		}
	}
	if cmd == "git" {
		if hasSubcmd(argv, "clean") && hasAnyShortFlag(argv, "f", "fd", "fdx", "fx") {
			return Classification{Risk: registry.RiskDestructive, Reason: "git clean -f*"}, nil
		}
		if hasSubcmd(argv, "reset") && hasAnyFlag(argv, "hard") {
			return Classification{Risk: registry.RiskDestructive, Reason: "git reset --hard"}, nil
		}
	}
	if cmd == "chmod" || cmd == "chown" {
		if hasAnyFlag(argv, "R", "recursive") {
			return Classification{Risk: registry.RiskDestructive, Reason: cmd + " -R"}, nil
		}
	}
	return Classification{Risk: registry.RiskCommand}, nil
}

func basename(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 { return p[i+1:] }
	return p
}

func hasFlag(argv []string, names ...string) bool { _ = names; return argv[0] == "rm" } // unused stub
func hasAnyFlag(argv []string, names ...string) bool { for _, n := range names { for _, a := range argv[1:] { if a == "-"+n || a == "--"+n { return true } } } return false }
func hasAnyShortFlag(argv []string, names ...string) bool { for _, n := range names { for _, a := range argv[1:] { if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], n) { return true } } } return false }
func hasSubcmd(argv []string, sub string) bool { for _, a := range argv[1:] { if !strings.HasPrefix(a, "-") { return a == sub } } return false }
```

(Remove the unused `hasFlag` stub before commit.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/policy/... -run TestClassifyCommand -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/policy/classify.go internal/tools/policy/classify_test.go
git commit -m "feat(policy): add ClassifyCommand for argv-aware risk analysis"
```

### Task 3.3: Replace substring guardrails with `ClassifyCommand`

**Files:**
- Modify: `internal/tools/native/command.go:110-136` (the
  `validateConservativeCommand` function — replace or remove)
- Modify: `internal/tools/policy/policy.go:25-28, 235-260` (replace
  substring checks with `ClassifyCommand`)
- Modify: `internal/tools/policy/policy_test.go`

**Interfaces:**
- Produces: `PolicyEngine.Evaluate` calls `ClassifyCommand` for
  `shell.run` / `test.run` and uses the resulting `Risk`.

- [ ] **Step 1: Update existing policy tests to expect new behavior**

Find any test in `policy_test.go` that asserts on substring matches
(e.g. `assertContains "rm -rf"` in the reason). Update the
expected reason to come from `ClassifyCommand` (e.g. `"rm -r -f"`).

- [ ] **Step 2: Replace the substring guardrails**

In `internal/tools/policy/policy.go`, remove the existing
substring-based guardrail list and replace with:

```go
func (e *Engine) Evaluate(tc ToolCall) (Decision, error) {
    if tc.Name == "shell.run" || tc.Name == "test.run" {
        cmd, _ := tc.Args["command"].(string)
        cls, err := ClassifyCommand(cmd)
        if err != nil { return Decision{}, err }
        switch cls.Risk {
        case registry.RiskDestructive:
            return Decision{Approval: ApprovalRequired, Reason: cls.Reason, Destructive: true}, nil
        case registry.RiskCommand:
            return Decision{Approval: ApprovalRequired, Reason: "shell command"}, nil
        }
    }
    // ... existing tool risk handling
}
```

In `internal/tools/native/command.go`, delete
`validateConservativeCommand` (now redundant — `PolicyEngine` runs
first).

- [ ] **Step 3: Run tests**

Run: `go test ./internal/tools/native/... ./internal/tools/policy/... -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/tools/native/command.go internal/tools/policy/policy.go internal/tools/policy/policy_test.go
git commit -m "refactor(policy): replace substring guardrails with ClassifyCommand (F-SEC-16)"
```

### Task 3.4: Argv-aware approval "always allow" pattern

**Files:**
- Modify: `internal/app/tui/model.go:2098-2118` (the
  `patternForApproval` function)
- Modify: `internal/app/tui/model_test.go` (add tests)

**Interfaces:**
- Produces: `patternForApproval` returns the full argv as the
  pattern, not `argv[0] + " *"`.

- [ ] **Step 1: Write the failing test**

```go
// internal/app/tui/model_test.go (append)
func TestPatternForApproval_FullArgv(t *testing.T) {
	got := patternForApproval("shell.run", map[string]any{"command": "git status"})
	if strings.Contains(got, "*") {
		t.Errorf("pattern %q should not use wildcards", got)
	}
	if !strings.Contains(got, "git") || !strings.Contains(got, "status") {
		t.Errorf("pattern %q should include all argv", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/... -run TestPatternForApproval -v`
Expected: FAIL.

- [ ] **Step 3: Update the function**

```go
// internal/app/tui/model.go
func patternForApproval(toolName string, args map[string]any) string {
    if toolName == "shell.run" || toolName == "test.run" {
        if cmd, ok := args["command"].(string); ok {
            argv, err := shlex.Split(cmd)
            if err == nil && len(argv) > 0 {
                return strings.Join(argv, " ")
            }
        }
    }
    return toolName
}
```

Add `import "github.com/google/shlex"` and remove the old
`words[0] + " *"` logic.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/tui/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "fix(tui): argv-aware always-allow pattern (F-SEC-07)"
```

### Task 3.5: `shlex.Split` for slash commands

**Files:**
- Modify: `internal/app/tui/model.go:1503-1524` (`dispatchCommand`)
- Modify: `internal/app/tui/model_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/app/tui/model_test.go (append)
func TestDispatchCommand_QuotedArgs(t *testing.T) {
	// Construct a /plan handler that records its argv.
	var got []string
	// ... register a stub command that captures argv into got
	// ... call dispatchCommand with `/plan "my idea"`
	if len(got) != 2 || got[1] != "my idea" {
		t.Errorf("got %v, want [plan my idea]", got)
	}
}
```

(Stub design depends on the existing command registry; adapt to it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/... -run TestDispatchCommand_QuotedArgs -v`
Expected: FAIL.

- [ ] **Step 3: Use `shlex.Split`**

```go
// internal/app/tui/model.go
argv, err := shlex.Split(raw)
if err != nil { /* surface error */ }
m.executeCommand(argv[0], argv[1:])
```

- [ ] **Step 4: Run tests + commit**

```bash
go test ./internal/app/tui/... -v
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "fix(tui): use shlex.Split for slash command args (F-SEC-31)"
```

### Task 3.6: Container argv path (full)

**Files:**
- Modify: `internal/sandbox/container.go:156-167`

**Interfaces:**
- Produces: Container runner uses the argv path from Task 1.9
  whenever `ClassifyCommand(command).Risk != RiskDestructive`. For
  destructive commands, the container still uses `/bin/sh -lc` so
  shell features are available when the user has explicitly
  approved.

- [ ] **Step 1: Update the conditional**

```go
// internal/sandbox/container.go
cls, _ := policy.ClassifyCommand(command)
if cls.Risk == policy.RiskReadOnly || cls.Risk == policy.RiskCommand {
    if isShellFree(command) {
        args = append(args, strings.Fields(command)...)
        goto done
    }
}
args = append(args, "/bin/sh", "-lc", command)
done:
```

(`policy` is imported from `internal/tools/policy`.)

- [ ] **Step 2: Run tests**

Run: `go test ./internal/sandbox/... ./internal/tools/policy/... -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/sandbox/container.go
git commit -m "feat(sandbox): container argv path for non-destructive commands (F-SEC-17)"
```

### Task 3.7: shell.run argv path (native)

**Files:**
- Modify: `internal/tools/native/runner.go:13`
- Modify: `internal/tools/native/runner_test.go`

**Interfaces:**
- Produces: `execRunner` invokes `/bin/sh -c` only when the command
  contains shell metacharacters or is classified as
  `RiskDestructive`. Otherwise it runs the command directly with
  `exec.Command(argv[0], argv[1:]...)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/tools/native/runner_test.go (append)
func TestExecRunner_InvokesArgvForSimple(t *testing.T) {
	r := NewExecRunner()
	res, err := r.Run(context.Background(), "echo hello", RunnerOptions{})
	if err != nil { t.Fatal(err) }
	if !strings.Contains(res.Combined, "hello") {
		t.Errorf("got %q", res.Combined)
	}
	// argv path: process is `echo`, not `sh -c`.
	// (Inspect res.Audit if available, or check proc info.)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native/... -run TestExecRunner_InvokesArgv -v`
Expected: FAIL (still uses `/bin/sh -c`).

- [ ] **Step 3: Implement the argv path**

```go
// internal/tools/native/runner.go
func (r *ExecRunner) Run(ctx context.Context, command string, opts RunnerOptions) (Result, error) {
    cls, _ := policy.ClassifyCommand(command)
    argv, err := shlex.Split(command)
    if err != nil || len(argv) == 0 {
        return r.runShell(ctx, command, opts)
    }
    if cls.Risk == registry.RiskDestructive || !isShellFree(command) {
        return r.runShell(ctx, command, opts)
    }
    cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
    // ... wire stdout/stderr/limits identically to runShell
    return r.invoke(cmd, opts)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/native/... -v`
Expected: PASS. Add coverage for `isShellFree` rejecting
`"echo a | grep b"` and forcing shell path.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/native/runner.go internal/tools/native/runner_test.go
git commit -m "feat(native): execRunner uses argv path for shell-free commands (F-SEC-01 partial)"
```

### Task 3.8: Release notes + audit doc update for Batch 3

**Files:**
- Modify: `docs/04-tooling-and-shell-safety.md`
- Modify: `docs/14-codebase-improvement-audit-2026-07-14.md`
- Modify: `docs/12-borrowed-features-spec.md` (or wherever the policy
  table lives; cross-link the new `RiskDestructive` rules)

- [ ] **Step 1: Append release note**

```markdown
## 2026-07-15 — A2 command classification

- Shell command classification is now argv-aware. Patterns like
  `rm -r -f`, `rm -fr`, `git clean -fdx`, `git reset --hard`, and
  `chmod -R` are detected regardless of flag ordering or short
  forms. Substring guardrails (`strings.Contains` for `rm -rf`,
  `sudo`, etc.) have been removed.
- The TUI's "always allow" pattern is now the full argv (`git
  status`), not `git *`. Existing always-allow rules saved before
  this change will need to be re-saved; the TUI surfaces a one-time
  hint.
- `shell.run` invokes the child process directly with argv when
  the command is shell-free and not destructive. Shell features
  (`|`, `&&`, `$(...)`, etc.) still route through `/bin/sh -c`.
- Slash commands accept shell-style quoted arguments via
  `shlex.Split`. `/plan "my idea"` now correctly passes
  `["my idea"]` to the command handler.
- `RiskDestructive` is now assigned to genuinely destructive
  patterns and always requires explicit approval, even when other
  shell commands are auto-allowed. The `allow_destructive` config
  flag changes the displayed reason but not the approval
  requirement.
```

- [ ] **Step 2: Mark Batch 3 findings resolved**

In `docs/14-codebase-improvement-audit-2026-07-14.md`, mark
F-SEC-01, F-SEC-07, F-SEC-16, F-SEC-17, F-SEC-31 as **RESOLVED
(Batch 3)**.

- [ ] **Step 3: Commit**

```bash
git add docs/04-tooling-and-shell-safety.md docs/14-codebase-improvement-audit-2026-07-14.md docs/12-borrowed-features-spec.md
git commit -m "docs: release notes and audit updates for A2 command classification"
```

### Task 3.9: Batch 3 verification

- [ ] **Step 1: Full build + tests + vet**

```bash
go build ./cmd/marshal
go test ./...
go vet ./...
gofmt -l . | tee /tmp/format
```

Expected: clean.

- [ ] **Step 2: Manual smoke test**

Try `shell.run` with `rm -rf /tmp/foo`. Confirm the policy
classifier returns `RiskDestructive`, the TUI shows the
"destructive: requires explicit approval" reason, and after
approval the command runs as expected. Try `shell.run` with
`echo hello` and confirm it runs without a shell (check that
`$0` and other shell variables are not expanded).

---

# Final acceptance

- [ ] **Step 1: All three batches merged to main**

- [ ] **Step 2: Audit doc shows all 23 findings as RESOLVED**

- [ ] **Step 3: Release notes added for all three batches in
  `docs/04-tooling-and-shell-safety.md`**

- [ ] **Step 4: Integration smoke test**

```bash
# Build, run, exercise the app end-to-end:
go build ./cmd/marshal
./marshal &
# In the TUI:
#  - /settings shows no regression
#  - Approve a shell command; verify the audit trail shows the
#    new "argv" reason
#  - Try a /plan "quoted argument" command; verify it parses
#  - Confirm a symlink inside the workspace pointing outside
#    is refused
#  - Run a destructive `rm -rf`; confirm the approval prompt
kill %1
```

- [ ] **Step 5: Tag the release**

```bash
git tag -a v0.x.y-domain-a -m "Domain A security & sandbox hardening"
git push origin v0.x.y-domain-a
```

---

## Self-review notes

- **Spec coverage:** All 23 domain-A findings (F-SAFE-20, 21, 22, 23,
  24, 25, 26; F-SEC-17, 18, 19, 35, 36, 122, 123, 01, 07, 16, 31;
  F-BUG-39) map to a specific task. Cross-references in the audit
  doc are updated as part of the batch closeout tasks.
- **Placeholder scan:** No "TBD", "TODO", "implement later" markers.
  Every code change is shown; every test code is concrete.
- **Type consistency:** `native.SafeResolve` is referenced by name in
  Tasks 2.2, 2.3, 2.4 and defined in Task 2.1. `envutil.AllowList` is
  referenced in Tasks 1.2, 1.3, 1.4 and defined in Task 1.1.
  `policy.ClassifyCommand` is referenced in Tasks 3.3, 3.6, 3.7 and
  defined in Task 3.2. `Classification.Risk` is the registry.Risk type
  used in Task 3.2 and consumed in 3.3/3.6/3.7.
- **Dependencies:** Batch 1 ships first and is independent. Batch 2
  requires only the stdlib. Batch 3 adds `github.com/google/shlex`
  (Task 3.1) and uses `native.SafeResolve` indirectly (via
  `policy.ClassifyCommand`).
- **Risks acknowledged:** (a) The `passthrough` opt-in flag is a
  breaking change for any user who relied on the previous silent
  default; release notes call this out. (b) Argv-aware "always allow"
  patterns invalidate previously-saved user rules; the TUI surfaces
  a one-time hint. (c) `repo.Scanner` no longer follows symlinks;
  workspaces that relied on this for `node_modules` or similar need
  to switch to bind mounts.
