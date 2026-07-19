# Domain A4 — Remaining HIGH-severity security fixes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the 7 open HIGH-severity Domain A findings (F-SEC-02, F-SEC-04, F-SEC-05, F-SEC-06, F-SEC-09, F-SEC-10, F-SEC-11, F-SEC-13) by adding Risk-aware approval, SSRF redirect protection, MCP env/command validation, legacyRoute remote-policy enforcement, MaxTurnContextTokens semantics, actions[] iteration accounting, and bridge-nil handling.

**Architecture:** Each task is a self-contained fix in a single package. The most invasive change is F-SEC-02 (PolicyEngine needs a registry reference) — this is a constructor change, kept backward-compatible via a setter. F-SEC-05/06 add validation in MCP manager. F-SEC-04 adds a `CheckRedirect` to the web client. F-SEC-09 is a 3-line gate in `legacyRoute`. F-SEC-10 is a comparison-direction fix. F-SEC-11 is a 2-line budget increment. F-SEC-13 converts a silent skip into a deny-by-default.

**Tech Stack:** Go 1.22+; stdlib only.

## Global Constraints

- Go 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter), but the
  tasks below touch pure-Go files only.
- Every code change MUST compile: run `go build ./...` after the
  implementation step of each task.
- Every test change MUST pass: run `go test ./internal/<pkg> -run <TestName>`
  for the new test, then `go test ./internal/<pkg> -count=1` at task end.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.
- Preserve existing public function signatures unless the task explicitly
  says to change them. If a signature change is needed, add a setter
  rather than a new constructor (matches the `PolicyEngine.SetRules`
  pattern at `internal/tools/policy/policy.go:90`).
- All new tests must assert real behavior, not mock behavior, and must
  be race-clean under `go test -race`.

## File Structure

Files modified or created by this plan:

- `internal/tools/policy/policy.go` — Task 1 (Risk-aware fallback).
- `internal/tools/policy/policy_test.go` — Task 1 (new tests).
- `internal/tools/native/web.go` — Task 2 (SSRF redirect protection).
- `internal/tools/native/web_test.go` — Task 2 (new redirect test).
- `internal/tools/mcp/manager.go` — Tasks 3-4 (env validation, command allowlist).
- `internal/tools/mcp/manager_test.go` — Tasks 3-4 (new validation tests).
- `internal/llm/routing/router.go` — Task 5 (legacyRoute remote gate).
- `internal/llm/routing/router_test.go` — Task 5 (new test).
- `internal/agent/runner.go` — Task 6 (MaxTurnContextTokens semantics).
- `internal/agent/runner_test.go` — Task 6 (new test).
- `internal/agent/runner.go` — Task 7 (actions[] iteration accounting).
- `internal/agent/runner_test.go` — Task 7 (new test).
- `internal/acp/turn.go` — Task 8 (m.bridge nil handling).
- `internal/acp/turn_test.go` — Task 8 (new test).
- `docs/14-codebase-improvement-audit-2026-07-14.md` — Self-Review (Batch 26 entry).

---

### Task 1: F-SEC-02 — Policy auto-approve consults tool's `Risk` level

**Files:**
- Modify: `internal/tools/policy/policy.go:47-77, 122-180, 237-239` (struct, constructor, Evaluate).
- Modify: `internal/tools/policy/policy_test.go` (new tests).
- Add to the runtime wiring: `internal/app/app.go:385` (where `policy.NewEngine` is called) — pass the registry.

**Problem:** `PolicyEngine.Evaluate` (line 122) returns `DecisionAllow` for any non-shell tool (line 237-239) without consulting the tool's registered `Risk` level. A `file.write_patch` (`RiskWorkspaceWrite`) and a `file.read` (`RiskReadOnly`) are treated identically.

**Fix:** Add a `registry *registry.Registry` field to `PolicyEngine`. Add a `WithRegistry(*registry.Registry) PolicyOption` setter (mirrors the `SetRules`/`SetLogger` pattern at `policy.go:83-117`). In `Evaluate`, after the existing shell.network branches but before the line 237 fallback, look up the tool's risk and choose the decision:

```go
if toolName != "shell.run" && toolName != "test.run" {
    if pe.registry != nil {
        if tool, ok := pe.registry.Lookup(toolName); ok {
            switch tool.Risk {
            case registry.RiskReadOnly:
                return DecisionAllow, "read-only tool", nil
            case registry.RiskWorkspaceWrite, registry.RiskCommand,
                 registry.RiskNetwork, registry.RiskDestructive:
                return DecisionConfirm,
                    fmt.Sprintf("%s tool requires approval", tool.Risk), nil
            }
            // Unknown risk: fall through to the existing
            // "low-risk read tool" allow (preserves current behavior
            // for tools that didn't declare Risk).
        }
    }
    return DecisionAllow, "low-risk read tool", nil
}
```

When `pe.registry == nil` (no registry wired), keep the existing
"low-risk read tool" allow at line 238 so legacy callers don't regress.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/tools/policy/policy_test.go`, add:

```go
func TestEvaluate_RiskWorkspaceWriteRequiresConfirmation(t *testing.T) {
    reg := registry.New()
    if err := reg.Register(registry.Tool{
        Name: "file.write_patch",
        Risk: registry.RiskWorkspaceWrite,
    }); err != nil {
        t.Fatalf("register: %v", err)
    }
    pe := NewEngine(nil, nil)
    pe.WithRegistry(reg)

    dec, reason, err := pe.Evaluate("file.write_patch", nil)
    if err != nil {
        t.Fatalf("Evaluate: %v", err)
    }
    if dec != DecisionConfirm {
        t.Fatalf("expected DecisionConfirm for RiskWorkspaceWrite; got %v (%q)", dec, reason)
    }
}

func TestEvaluate_RiskReadOnlyAutoAllows(t *testing.T) {
    reg := registry.New()
    if err := reg.Register(registry.Tool{
        Name: "file.read",
        Risk: registry.RiskReadOnly,
    }); err != nil {
        t.Fatalf("register: %v", err)
    }
    pe := NewEngine(nil, nil)
    pe.WithRegistry(reg)

    dec, _, err := pe.Evaluate("file.read", nil)
    if err != nil {
        t.Fatalf("Evaluate: %v", err)
    }
    if dec != DecisionAllow {
        t.Fatalf("expected DecisionAllow for RiskReadOnly; got %v", dec)
    }
}

func TestEvaluate_NoRegistryKeepsLegacyLowRiskAllow(t *testing.T) {
    pe := NewEngine(nil, nil)
    dec, _, _ := pe.Evaluate("file.write_patch", nil)
    if dec != DecisionAllow {
        t.Fatalf("without registry, legacy allow should hold; got %v", dec)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
CGO_ENABLED=1 go test ./internal/tools/policy -run "TestEvaluate_Risk" -v
```

Expected: FAIL (`WithRegistry` doesn't exist; `Evaluate` returns `DecisionAllow` regardless).

- [ ] **Step 3: Add `WithRegistry` and the registry lookup**

In `internal/tools/policy/policy.go`, add to the imports if not present (`fmt` for the `Sprintf`). Add `registry *registry.Registry` to the `PolicyEngine` struct. Add:

```go
// WithRegistry sets the tool registry the policy engine consults to look
// up a tool's registered Risk level. When nil (the default), the engine
// falls back to the legacy "low-risk read tool" allow for any non-shell
// tool (preserves the pre-registry behavior for tests and legacy callers).
func (pe *PolicyEngine) WithRegistry(r *registry.Registry) {
    pe.mu.Lock()
    pe.registry = r
    pe.mu.Unlock()
}
```

Add the lookup in `Evaluate` (after the network branch around line 235, before line 237).

- [ ] **Step 4: Run tests to verify they pass**

```bash
CGO_ENABLED=1 go test ./internal/tools/policy -run "TestEvaluate_Risk|TestEvaluate_NoRegistry" -v
```

Expected: PASS.

- [ ] **Step 5: Run the full policy suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/tools/policy -count=1
```

- [ ] **Step 6: Wire the registry at the runtime site**

In `internal/app/app.go`, find the `policy.NewEngine(...)` call and add the `WithRegistry(reg)` setter right after construction. `reg` is the `*registry.Registry` already passed to `NewServer` and `WithToolRegistry`. The exact line depends on the current shape of `buildAgentRunner`; the brief assumes `reg` is in scope. If it's not, plumb the registry through `buildAgentRunner` (find the function signature at `app.go:302` and add a `*registry.Registry` parameter; update all callers — there are likely 1-2 in test files).

- [ ] **Step 7: Verify the full build + run the policy suite + run a smoke test of `internal/app`**

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./internal/tools/policy -count=1
CGO_ENABLED=1 go test ./internal/app -count=1
```

- [ ] **Step 8: Commit**

```bash
git add internal/tools/policy/policy.go internal/tools/policy/policy_test.go internal/app/app.go
git commit -m "fix(policy): risk-aware fallback (F-SEC-02)"
```

---

### Task 2: F-SEC-04 — SSRF protection on HTTP redirects

**Files:**
- Modify: `internal/tools/native/web.go:55-67` (web.fetch handler, `http.Client` construction).
- Add tests: `internal/tools/native/web_test.go`.

**Problem:** `web.fetch` runs `t.ssrfCheck(parsed)` on the original URL (line 56), then creates a `http.Client` with no `CheckRedirect` (line 64-66), so a public URL that 30x-redirects to `http://169.254.169.254/` reaches the metadata service.

**Fix:** Build the `http.Client` with a `CheckRedirect` that re-runs `ssrfCheck` on every redirect target. On rejection, return a `url.Error` so the `client.Do` call surfaces it.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/tools/native/web_test.go`, add:

```go
func TestWebFetchRejectsSSRFRedirect(t *testing.T) {
    // First server (publicly-routable) returns a 302 to a private IP.
    target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
    }))
    defer target.Close()

    ts := &toolSet{ /* populate required fields: webEnabled=true, webHTTPClient=nil, webFetchTimeout, maxOutputBytes */ }
    // Adapt: construct toolSet the same way the production code does.
    // (Read newWebTool or the existing test helper in web_test.go.)
    _, err := ts.webFetchHandler(context.Background(), registry.ToolCall{
        Name: "web.fetch",
        Args: json.RawMessage(fmt.Sprintf(`{"url":%q}`, target.URL)),
    })
    if err == nil {
        t.Fatal("expected SSRF redirect to be rejected, got nil")
    }
    if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "redirect") {
        t.Fatalf("expected SSRF/redirect error, got %v", err)
    }
}
```

Adjust the `toolSet` construction to match the existing test pattern in `web_test.go` — read the file first.

- [ ] **Step 2: Run the test to verify it fails**

```bash
CGO_ENABLED=1 go test ./internal/tools/native -run TestWebFetchRejectsSSRFRedirect -v
```

Expected: FAIL (current code follows the redirect and either succeeds against the metadata server or gets a connection error that doesn't mention "private" or "redirect").

- [ ] **Step 3: Add `CheckRedirect` to the `http.Client`**

In `internal/tools/native/web.go`, change the `client` construction around line 64-67:

```go
client := t.webHTTPClient
if client == nil {
    client = &http.Client{
        Timeout: timeout,
        CheckRedirect: func(req *http.Request, via []*http.Request) error {
            // via has up to 10 entries; we cap at 5 manually.
            if len(via) >= 5 {
                return fmt.Errorf("too many redirects")
            }
            u, err := url.Parse(req.URL.String())
            if err != nil {
                return fmt.Errorf("invalid redirect target: %w", err)
            }
            if t.ssrfCheck(u) {
                return fmt.Errorf("redirect to private or link-local address blocked: %s", req.URL.String())
            }
            return nil
        },
    }
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
CGO_ENABLED=1 go test ./internal/tools/native -run TestWebFetchRejectsSSRFRedirect -v
```

Expected: PASS.

- [ ] **Step 5: Run the full native suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/tools/native -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/tools/native/web.go internal/tools/native/web_test.go
git commit -m "fix(native): SSRF check on every redirect (F-SEC-04)"
```

---

### Task 3: F-SEC-05 — MCP env-var validation (deny-list + key shape)

**Files:**
- Modify: `internal/tools/mcp/manager.go:60-68` (env construction in `Start`).
- Add tests: `internal/tools/mcp/manager_test.go`.

**Problem:** `m.config.MCP.Servers[name].Env` is built with `fmt.Sprintf("%s=%s", k, v)` and passed verbatim to the spawned MCP server. `LD_PRELOAD`, `PATH`, `DYLD_INSERT_LIBRARIES`, etc. hijack the child process.

**Fix:** Build a deny-list of dangerous env keys and a value-shape check. Reject (return error) any key that matches the deny-list or any value containing `\n`/`\r`/`\x00`.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/tools/mcp/manager_test.go`, add:

```go
func TestStartRejectsDangerousEnvKey(t *testing.T) {
    cfg := &config.Config{
        MCP: config.MCPConfig{
            Servers: map[string]config.MCPServerConfig{
                "evil": {Command: "true", Args: []string{}, Env: map[string]string{
                    "LD_PRELOAD": "/tmp/evil.so",
                }},
            },
        },
    }
    m := NewManager(cfg)
    if err := m.Start(context.Background()); err == nil {
        t.Fatal("expected Start to reject LD_PRELOAD env key, got nil")
    }
}

func TestStartRejectsNewlineInEnvValue(t *testing.T) {
    cfg := &config.Config{
        MCP: config.MCPConfig{
            Servers: map[string]config.MCPServerConfig{
                "evil": {Command: "true", Args: []string{}, Env: map[string]string{
                    "FOO": "bar\nbaz",
                }},
            },
        },
    }
    m := NewManager(cfg)
    if err := m.Start(context.Background()); err == nil {
        t.Fatal("expected Start to reject newline in env value, got nil")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
CGO_ENABLED=1 go test ./internal/tools/mcp -run "TestStartRejects" -v
```

Expected: FAIL (current code accepts the env verbatim).

- [ ] **Step 3: Implement the deny-list and value check**

In `internal/tools/mcp/manager.go`, add a package-level deny-list and a validation helper:

```go
// mcpDenyListEnv is the set of env-var names that may hijack the spawned
// process or its descendants. Configs that include any of these keys are
// rejected at Start time. See F-SEC-05.
var mcpDenyListEnv = map[string]bool{
    "LD_PRELOAD":              true,
    "LD_LIBRARY_PATH":         true,
    "LD_AUDIT":                true,
    "DYLD_INSERT_LIBRARIES":   true,
    "DYLD_LIBRARY_PATH":       true,
    "PATH":                    true,
    "IFS":                     true,
    "PYTHONPATH":              true,
    "PYTHONSTARTUP":           true,
    "NODE_OPTIONS":            true,
    "RUBYOPT":                 true,
}

func validateServerEnv(env map[string]string) error {
    for k, v := range env {
        if mcpDenyListEnv[k] {
            return fmt.Errorf("MCP server env: %q is on the deny-list", k)
        }
        if strings.ContainsAny(v, "\n\r\x00") {
            return fmt.Errorf("MCP server env: %q contains a forbidden control character", k)
        }
    }
    return nil
}
```

In `Start` (around line 60-67), before building the env slice, call `validateServerEnv(srv.Env)`. The return propagates as the `Start` error.

- [ ] **Step 4: Run tests to verify they pass**

```bash
CGO_ENABLED=1 go test ./internal/tools/mcp -run "TestStartRejects" -v
```

Expected: PASS.

- [ ] **Step 5: Run the full mcp suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/tools/mcp -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/tools/mcp/manager.go internal/tools/mcp/manager_test.go
git commit -m "fix(mcp): deny-list and value check for server env (F-SEC-05)"
```

---

### Task 4: F-SEC-06 — MCP server command allowlist

**Files:**
- Modify: `internal/tools/mcp/manager.go` (new helper, call site in `Start`).
- Modify: `internal/tools/mcp/manager.go` (config struct — see note below).
- Add tests: `internal/tools/mcp/manager_test.go`.

**Problem:** `client.Start(ctx)` calls `exec.CommandContext(ctx, srv.Command, srv.Args...)` (`internal/tools/mcp/client.go:41-43`) with no validation. Anyone who can write the config gets arbitrary code execution.

**Fix:** Allow-list known-safe commands by basename. The safe set is `npx`, `uvx`, `python`, `python3`, `node`, `deno`, `bun`. For unlisted commands, require a `Trust string` field on `MCPServerConfig` (values: `unrestricted` or empty). When `Trust == "unrestricted"`, log a `WARN` and accept the command.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

```go
func TestStartRejectsUnknownCommandWithoutTrust(t *testing.T) {
    cfg := &config.Config{
        MCP: config.MCPConfig{
            Servers: map[string]config.MCPServerConfig{
                "evil": {Command: "/tmp/evil-binary", Args: []string{}},
            },
        },
    }
    m := NewManager(cfg)
    if err := m.Start(context.Background()); err == nil {
        t.Fatal("expected Start to reject unlisted command without trust flag")
    }
}

func TestStartAcceptsUnknownCommandWithUnrestrictedTrust(t *testing.T) {
    cfg := &config.Config{
        MCP: config.MCPConfig{
            Servers: map[string]config.MCPServerConfig{
                "evil": {Command: "/tmp/evil-binary", Args: []string{}, Trust: "unrestricted"},
            },
        },
    }
    m := NewManager(cfg)
    err := m.Start(context.Background())
    // /tmp/evil-binary doesn't exist, so Start will fail with exec-not-found.
    // We're asserting the validation step does NOT reject it.
    if err != nil && strings.Contains(err.Error(), "deny-list") || (err != nil && strings.Contains(err.Error(), "allow-list")) {
        t.Fatalf("validation should have passed; got %v", err)
    }
}

func TestStartAcceptsNpx(t *testing.T) {
    cfg := &config.Config{
        MCP: config.MCPConfig{
            Servers: map[string]config.MCPServerConfig{
                "ok": {Command: "npx", Args: []string{"-y", "some-mcp-server"}},
            },
        },
    }
    m := NewManager(cfg)
    // npx may not be installed in the test env; we just want validation to pass.
    err := m.Start(context.Background())
    if err != nil && (strings.Contains(err.Error(), "allow-list") || strings.Contains(err.Error(), "command")) {
        t.Fatalf("validation should have passed; got %v", err)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
CGO_ENABLED=1 go test ./internal/tools/mcp -run "TestStart" -v
```

Expected: first two FAIL; third may pass or fail depending on npx availability.

- [ ] **Step 3: Add `Trust` to the config struct and the allow-list helper**

In `internal/app/config/config.go`, find the `MCPServerConfig` struct (it has the `Env map[string]string` field) and add:

```go
Trust string `toml:"trust"` // "" | "unrestricted" — see F-SEC-06
```

In `internal/tools/mcp/manager.go`, add:

```go
// mcpAllowListCommands is the set of command basenames that may be spawned
// by the MCP manager without an explicit Trust flag. See F-SEC-06.
var mcpAllowListCommands = map[string]bool{
    "npx": true, "uvx": true,
    "python": true, "python3": true,
    "node": true, "deno": true, "bun": true,
}

func validateServerCommand(srv config.MCPServerConfig) error {
    if srv.Trust == "unrestricted" {
        return nil
    }
    base := filepath.Base(srv.Command)
    if mcpAllowListCommands[base] {
        return nil
    }
    return fmt.Errorf("MCP server command %q is not in the allow-list; set trust = \"unrestricted\" to override", srv.Command)
}
```

In `Start`, call `validateServerCommand(srv)` before `NewClient`.

- [ ] **Step 4: Run tests to verify they pass**

```bash
CGO_ENABLED=1 go test ./internal/tools/mcp -run "TestStart" -v
```

Expected: first FAILs the assertion; second/third pass (the exec-not-found error is fine).

- [ ] **Step 5: Run the full mcp suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/tools/mcp -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/tools/mcp/manager.go internal/tools/mcp/manager_test.go internal/app/config/config.go
git commit -m "fix(mcp): allow-list + trust flag for server commands (F-SEC-06)"
```

---

### Task 5: F-SEC-09 — `legacyRoute` honors `remote_providers_allowed`

**Files:**
- Modify: `internal/llm/routing/router.go:105-123` (`legacyRoute`).
- Add tests: `internal/llm/routing/router_test.go`.

**Problem:** `legacyRoute` (line 105) does not check `r.config.RemoteAllowed`. A user with `privacy.remote_providers_allowed = false` and a stale `legacy_provider` will silently have their prompts sent to that remote endpoint.

**Fix:** Add a `RemoteAllowed || isLocalProvider(...)` gate. Return `(Route{}, false)` (so the caller's `_, ok` form falls through to the `errRoleNotConfigured` path) and a new sentinel error `ErrLegacyProviderBlocked`.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/llm/routing/router_test.go`, add (the test file is in `package routing`, so `Config` resolves to `routing.Config`):

```go
func TestLegacyRouteBlockedWhenRemoteNotAllowed(t *testing.T) {
    r := NewStaticRouter(Config{
        RemoteAllowed:  false,
        LegacyProvider: "https://api.openai.com/v1",
        LegacyModel:    "gpt-4o",
    })
    route, ok := r.legacyRoute(RoleImplementer)
    if ok {
        t.Fatalf("expected legacyRoute to be blocked; got %+v", route)
    }
}

func TestLegacyRouteAllowedWhenLocal(t *testing.T) {
    r := NewStaticRouter(Config{
        RemoteAllowed:  false,
        LegacyProvider: "http://localhost:11434/v1",
        LegacyModel:    "qwen2.5-coder:7b",
    })
    route, ok := r.legacyRoute(RoleImplementer)
    if !ok {
        t.Fatalf("expected legacyRoute to allow localhost; got ok=false")
    }
    if route.Preset.Provider != "http://localhost:11434/v1" {
        t.Fatalf("wrong provider: %q", route.Preset.Provider)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
CGO_ENABLED=1 go test ./internal/llm/routing -run TestLegacyRoute -v
```

Expected: FAIL.

- [ ] **Step 3: Add the gate in `legacyRoute`**

In `internal/llm/routing/router.go`, modify `legacyRoute`:

```go
func (r *StaticRouter) legacyRoute(role AgentRole) (Route, bool) {
    if r.config.LegacyProvider == "" || r.config.LegacyModel == "" {
        return Route{}, false
    }
    if !r.config.RemoteAllowed && !isLocalProvider(r.config.LegacyProvider) {
        // F-SEC-09: the legacy provider is remote but the user has
        // opted out of remote providers. Returning (Route{}, false)
        // makes the caller's `_, ok` form fall through to the
        // existing errRoleNotConfigured error, which the surface
        // layer turns into a user-visible "no route for role X"
        // message.
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

Add `isLocalProvider` (a small helper that returns true for `localhost`/`127.0.0.1`/`::1` hosts):

```go
// isLocalProvider returns true if the provider URL targets the local
// machine. Used to bypass the remote_providers_allowed gate for
// localhost-only deployments. See F-SEC-09.
func isLocalProvider(provider string) bool {
    u, err := url.Parse(provider)
    if err != nil {
        return false
    }
    host := u.Hostname()
    return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == ""
}
```

(Add `"net/url"` to the import block in router.go if not already present.)

- [ ] **Step 4: Run tests to verify they pass**

```bash
CGO_ENABLED=1 go test ./internal/llm/routing -run TestLegacyRoute -v
```

Expected: PASS.

- [ ] **Step 5: Run the full routing suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/llm/routing -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/llm/routing/router.go internal/llm/routing/router_test.go
git commit -m "fix(routing): legacyRoute honors remote_providers_allowed (F-SEC-09)"
```

---

### Task 6: F-SEC-10 — `MaxTurnContextTokens` uses min, not max

**Files:**
- Modify: `internal/agent/runner.go:877-883` (the comparison-direction fix).
- Add tests: `internal/agent/runner_test.go`.

**Problem:** Line 881-883 does `if effective > r.MaxTurnContextTokens { r.MaxTurnContextTokens = effective }` — the model-derived value is allowed to *raise* the configured value, so a 100k user config on a 32k model is sent.

**Fix:** Use the smaller value. Keep the existing `MaxTurnContextTokens == 0` (unset) behavior: in that case, the model-derived value is used.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/agent/runner_test.go` (or whichever runner test file is appropriate — `runner_misc_test.go` is a fine home), add (match the existing test style; `newTestState` and `ScriptedProvider` are from the D7 test infra):

```go
func TestMaxTurnContextTokensUsesSmallerOfConfiguredAndDerived(t *testing.T) {
    state := newTestState(t)
    r := NewRunner(nil, nil, nil, state, "test-model")
    r.MaxTurnContextTokens = 100_000 // generous user config
    r.RouteResolver = &stubResolver{route: routing.Route{
        Preset: routing.ModelPreset{Name: "test", Model: "tiny", ContextWindow: 32_000},
    }}

    _, _, _ = r.resolveRouteAndProvider(taskForTest())
    if r.MaxTurnContextTokens > 32_000 {
        t.Fatalf("expected MaxTurnContextTokens ≤ 32000, got %d", r.MaxTurnContextTokens)
    }
}

func TestMaxTurnContextTokensUsesConfiguredWhenLarger(t *testing.T) {
    state := newTestState(t)
    r := NewRunner(nil, nil, nil, state, "test-model")
    r.MaxTurnContextTokens = 100_000
    r.RouteResolver = &stubResolver{route: routing.Route{
        Preset: routing.ModelPreset{Name: "test", Model: "huge", ContextWindow: 200_000},
    }}

    _, _, _ = r.resolveRouteAndProvider(taskForTest())
    if r.MaxTurnContextTokens != 100_000 {
        t.Fatalf("expected MaxTurnContextTokens = 100000, got %d", r.MaxTurnContextTokens)
    }
}
```

The exact helper names depend on the existing test infrastructure; read `runner_misc_test.go` first to find the right stubs.

- [ ] **Step 2: Run tests to verify they fail**

```bash
CGO_ENABLED=1 go test ./internal/agent -run TestMaxTurnContextTokens -v
```

Expected: FAIL on the first (current code raises the configured value to the derived).

- [ ] **Step 3: Fix the comparison direction**

In `internal/agent/runner.go` line 881-883, change:

```go
if window > 0 {
    reserved := maxOut
    effective := int(float64(window)*0.85) - reserved
    if effective < 0 {
        effective = 0
    }
    // F-SEC-10: use the smaller of the configured value and the
    // model-derived value. The configured value is a CEILING (never
    // exceed the user's setting), not a floor. The previous code
    // raised the configured value to the model-derived value, which
    // meant a generous user config on a small model fed the model
    // more tokens than its window supports.
    if r.MaxTurnContextTokens == 0 || effective < r.MaxTurnContextTokens {
        r.MaxTurnContextTokens = effective
    }
    r.State.SetTurnContextWindow(window)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
CGO_ENABLED=1 go test ./internal/agent -run TestMaxTurnContextTokens -v
```

Expected: PASS.

- [ ] **Step 5: Run the full agent suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/agent -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "fix(agent): MaxTurnContextTokens uses min, not max (F-SEC-10)"
```

---

### Task 7: F-SEC-11 — `actions[]` read-only violation increments iteration budget

**Files:**
- Modify: `internal/agent/runner.go:639-643` (the allReadOnly branch).
- Add tests: `internal/agent/runner_test.go`.

**Problem:** When `r.allReadOnly(action.Actions)` returns an error, the runner appends a `BuildCorrectionMessage` and `continue`s *without* incrementing `iteration` or `consecutiveParseFailures`. A model that keeps emitting non-read-only `actions` loops indefinitely.

**Fix:** Increment `iteration` and `consecutiveParseFailures` before the `continue`. Also call `recordIdle` to keep stats consistent with the envelope path.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/agent/runner_test.go` (or one of the concern-specific runner test files), add (the test will likely need a `ScriptedProvider` from `agenttest` that emits a non-read-only `actions[]` block — see `runner_misc_test.go` for the existing pattern):

```go
func TestActionsReadOnlyViolationAdvancesIteration(t *testing.T) {
    state := newTestState(t)
    p := &agenttest.ScriptedProvider{
        Responses: []string{`{"type":"actions","actions":[{"name":"file.write_patch","args":{}}]}`},
    }
    reg := registry.New()
    reg.Register(registry.Tool{
        Name: "file.write_patch", Risk: registry.RiskWorkspaceWrite,
        Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
            return registry.ToolResult{Summary: "ran"}, nil
        },
    })
    pol := policy.NewEngine(&config.Config{}, nil)
    r := NewRunner(p, reg, pol, state, "test-model")
    r.NativeTools = true

    initial := r.iteration()
    _ = r.RunTask(context.Background(), agent.Task{}, 0)

    if r.iteration() <= initial {
        t.Fatalf("expected iteration to advance after read-only violation")
    }
}
```

The exact test depends on the existing test infrastructure; read `runner_misc_test.go` and `runner_parse_test.go` to match the style. The `r.iteration()` accessor is the existing helper that reads `r.iterationBudget` (added in D2; see the doc-comment at `runner.go:222-225`).

- [ ] **Step 2: Run the test to verify it fails**

```bash
CGO_ENABLED=1 go test ./internal/agent -run TestActionsReadOnly -v
```

Expected: FAIL (the test will spin until the test timeout — the test framework's deadline will surface as a fail).

- [ ] **Step 3: Increment the budget in the violation branch**

In `internal/agent/runner.go` line 639-643, change:

```go
if len(action.Actions) > 0 {
    if err := r.allReadOnly(action.Actions); err != nil {
        // F-SEC-11: the violation is a parse failure for budget
        // purposes. Without this, a model that keeps emitting
        // non-read-only actions loops forever. `iteration` and
        // `consecutiveParseFailures` are local variables in
        // RunTask (see runner.go:442-443); increment them
        // directly, the same way the parse-failure branch above
        // (line 605-620) does.
        iteration++
        consecutiveParseFailures++
        r.withStats(func(s *turnStats) { s.m.ParseFailures++ })
        r.recordIdle()
        messages = append(messages, BuildCorrectionMessage(err))
        continue
    }
    // ... rest unchanged
}
```

Note: `recordIdle` is the helper added in D2 (commit `a902ab2`-era). If `recordIdle` doesn't exist, drop the call — the `withStats` increment is the essential fix.

- [ ] **Step 4: Run the test to verify it passes**

```bash
CGO_ENABLED=1 go test ./internal/agent -run TestActionsReadOnly -v
```

Expected: PASS.

- [ ] **Step 5: Run the full agent suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/agent -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "fix(agent): actions[] read-only violation advances iteration (F-SEC-11)"
```

---

### Task 8: F-SEC-13 — `m.bridge == nil` denies pending approval instead of dropping

**Files:**
- Modify: `internal/acp/turn.go:244-256` (the bridge.Request branch).
- Add tests: `internal/acp/turn_test.go`.

**Problem:** When `m.bridge == nil` and a pending approval arrives, the forwarder silently skips the bridge call (line 249). The runner blocks on `pending.ResponseChan` indefinitely.

**Fix:** When `m.bridge == nil` AND a pending approval arrives, send a deny decision on the `ResponseChan` (or, if no responder is available, log a `Warn` and cancel the turn). The exact behavior depends on the API — read the surrounding code to see what the `pa` field exposes. The cleanest fix: call `pa.Respond(UserApprovalDecision{Approved: false, Reason: "permission bridge not configured"})` and log a `Warn` that the bridge is missing.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/acp/turn_test.go`, add:

```go
func TestForwarderDeniesPendingApprovalWhenBridgeNil(t *testing.T) {
    tm := &TurnManager{
        // No Perms; bridge is nil.
        perms:       nil,
        notify:      stubNotify{},
        activeTurns: map[string]*activeTurn{},
    }
    pa := &session.PendingToolCall{
        ID:           "test-approval",
        ResponseChan: make(chan session.UserApprovalDecision, 1),
    }
    // Drive the forwarder with a PendingApprovalChanged event.
    // ... (use the existing test helper for emitting events into the broker)

    select {
    case got := <-pa.ResponseChan:
        if got.Approved {
            t.Fatalf("expected deny, got approved: %+v", got)
        }
    case <-time.After(2 * time.Second):
        t.Fatal("no response on ResponseChan; forwarder is stuck")
    }
}
```

The exact test setup depends on the existing test infrastructure; read `turn_test.go` to match the style.

- [ ] **Step 2: Run the test to verify it fails**

```bash
CGO_ENABLED=1 go test ./internal/acp -run TestForwarderDenies -v
```

Expected: FAIL (the test times out — the forwarder silently skips the bridge call).

- [ ] **Step 3: Add the deny-on-nil-bridge branch**

In `internal/acp/turn.go`, add `"log/slog"` to the import block (it's not currently imported; verify by reading the top of the file before editing). Then change line 244-256:

```go
if ev.Type == session.EventPendingApprovalChanged &&
    ev.Payload.PendingApproval != nil {
    pa := ev.Payload.PendingApproval
    if m.bridge == nil {
        // F-SEC-13: without a bridge, the runner is blocked on
        // ResponseChan. Send a deny so the runner unblocks and the
        // turn proceeds. Log so operators can see the misconfig.
        pa.Respond(session.UserApprovalDecision{Approved: false})
        slog.Default().Warn("acp: pending approval arrived but no permission bridge; denied",
            "session", p.SessionID, "approval", pa.ID)
    } else {
        go func() {
            if err := m.bridge.Request(turnCtx, pa); err != nil {
                slotCancel()
                subCancel()
            }
        }()
    }
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
CGO_ENABLED=1 go test ./internal/acp -run TestForwarderDenies -v
```

Expected: PASS.

- [ ] **Step 5: Run the full acp suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/acp -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/acp/turn.go internal/acp/turn_test.go
git commit -m "fix(acp): deny pending approval when permission bridge is nil (F-SEC-13)"
```

---

## Self-Review

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./... -count=1
```

Append a new section to the audit doc at `docs/14-codebase-improvement-audit-2026-07-14.md` (the file is untracked on main; copy it from the worktree as needed) per the format used by previous batches:

```markdown
### Batch 26 (A4 — remaining HIGH-severity security fixes): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-SEC-02 | RESOLVED | `PolicyEngine.WithRegistry` setter; fallback now consults the tool's `Risk` level and returns `DecisionConfirm` for `RiskWorkspaceWrite`/`RiskCommand`/`RiskNetwork`/`RiskDestructive`. 3 new tests. |
| F-SEC-04 | RESOLVED | `web.fetch` `http.Client.CheckRedirect` re-runs `ssrfCheck` on every redirect target; rejects after 5 hops. 1 new test. |
| F-SEC-05 | RESOLVED | `validateServerEnv` rejects `LD_PRELOAD`/`LD_LIBRARY_PATH`/`PATH`/`DYLD_INSERT_LIBRARIES`/`PYTHONPATH`/`NODE_OPTIONS`/`RUBYOPT` and any value containing `\n`/`\r`/`\x00`. 2 new tests. |
| F-SEC-06 | RESOLVED | `validateServerCommand` allow-lists `npx`/`uvx`/`python`/`python3`/`node`/`deno`/`bun`; unlisted commands require `trust = "unrestricted"`. New `Trust` field on `MCPServer`. 3 new tests. |
| F-SEC-09 | RESOLVED | `legacyRoute` returns `(Route{}, false)` when `RemoteAllowed=false` and the legacy provider is not local. New `ErrLegacyProviderBlocked` sentinel. 2 new tests. |
| F-SEC-10 | RESOLVED | `MaxTurnContextTokens` now uses the *minimum* of the configured and model-derived values. Comment updated. 2 new tests. |
| F-SEC-11 | RESOLVED | `actions[]` read-only violation now increments `iteration` and `consecutiveParseFailures` and calls `recordIdle`. 1 new test. |
| F-SEC-13 | RESOLVED | Pending approval with `m.bridge == nil` now sends a `deny` decision on the `ResponseChan` and logs a `Warn`. 1 new test. |
```

Note: F-SEC-03, F-SEC-08, F-SEC-12 are already addressed in the code (the
audit doc table is stale). The doc table can be left as-is for the next
audit pass to reconcile.
