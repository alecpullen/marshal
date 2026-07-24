# ACP Stress Test Bug Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the four bugs surfaced by the 2026-07-24 ACP stress test: (1) nil-pointer panic in `PromptTurn` when the agent runner failed to build, (2) headless ACP silently dropping project-local config via the trust gate, (3) trust store timestamp parse failure swallowed by `IsTrusted`, (4) provider tool-name truncation (kimi-k2.7-code returns `read` for `file.read`) causing every native tool call to fail as "unknown tool."

**Architecture:** Each fix is independent and ships as its own task with its own test cycle. Bug 1 adds a nil-guard in the ACP `Lookup` closure that returns a `-32000` server error referencing the stored provider error instead of panicking. Bug 2 adds a `HeadlessResolver` that trusts the cwd (with a logged warning) when no stored trust exists, wired into the ACP `Run` path via the existing `WithTrustResolver` option. Bug 3 makes `Store.Load` tolerant of missing timezone offsets by parsing with a permissive layout fallback. Bug 4 adds a tool-name normalization step in `executeNativeToolCalls` that maps a truncated name (e.g. `read`) back to the unique registered tool whose name ends in that segment (e.g. `file.read`), with an ambiguity guard that rejects non-unique suffixes.

**Tech Stack:** Go, `modernc.org/sqlite`, standard `testing`, the existing `internal/acp`, `internal/trust`, `internal/agent`, and `internal/tools/registry` packages.

## Global Constraints

- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter dependency). Test with `go test ./...`.
- ACP error responses use the `serverError` code (`-32000`) via `serverErrorf` (`internal/acp/protocol.go:92`); the wire message is opaque (`wireError` in `server.go:436`), the full error is logged server-side.
- The trust `Resolver` interface (`internal/trust/trust.go:121`) has two methods: `Resolve(workingDir string, hasProjectConfig bool) (Decision, error)` and `Record(workingDir string, decision Decision) error`. Any new resolver must implement both.
- `app.WithTrustResolver(r trust.Resolver) Option` already exists (`internal/app/app.go:138`) and is the injection seam for the ACP `Run` path.
- Tool names are dotted strings (`file.read`, `shell.run`, `git.status`); the registry stores them as map keys (`internal/tools/registry/registry.go:17`). `Registry.Lookup(name)` returns `(Tool, bool)`.
- `session.State.ProviderError()` returns the last provider-level error set by `SetProviderError` (`internal/app/session/session.go:686`); `app.Runtime.State` is the `*session.State` (`internal/app/runtime.go:61`).
- New config fields use the existing pointer-field merge pattern (`*string`/`*bool` in `file_types.go`, `set(&dst, src)` in `merge.go`).
- No new external dependencies. All fixes use the standard library and existing internal packages.

---

## File Structure

- Modify: `internal/acp/run.go` — inject a `HeadlessResolver` into `StartRuntime` via `WithTrustResolver` (Bug 2); pass the provider error into the `Lookup` closure for the nil-guard (Bug 1).
- Modify: `internal/acp/turn.go` — no code change needed; the nil-guard lives in the `Lookup` closure in `run.go` (Bug 1).
- Modify: `internal/acp/run_test.go` — test that a prompt against a runtime with no runner returns a `-32000` error instead of panicking (Bug 1).
- Create: `internal/trust/headless.go` — `HeadlessResolver` wrapping a `*Store`, trusting the cwd when no stored record exists, delegating to stored trust when one exists (Bug 2).
- Create: `internal/trust/headless_test.go` — `HeadlessResolver` tests (Bug 2).
- Modify: `internal/trust/trust.go` — make `Store.Load` tolerant of missing timezone offsets in `trusted_at` (Bug 3).
- Modify: `internal/trust/trust_test.go` — test that a store with a zoneless `trusted_at` loads without error (Bug 3).
- Modify: `internal/agent/execute.go` — add `normalizeToolName` and call it in `executeNativeToolCalls` before dispatch (Bug 4).
- Modify: `internal/agent/runner_misc_test.go` — test that a truncated tool name (`read`) resolves to `file.read` when unambiguous (Bug 4).

---

## Task 1: Guard against nil runner in ACP PromptTurn

**Files:**
- Modify: `internal/acp/run.go:90-107` (the `Lookup` closure)
- Test: `internal/acp/run_test.go`

**Interfaces:**
- Consumes: `app.Runtime.State.ProviderError() error` (`internal/app/session/session.go:686`); `serverErrorf` (`internal/acp/protocol.go:92`).
- Produces: a `Lookup` closure that returns `(*TurnRuntime, false)` when `rt.Runner == nil`, so `PromptTurn` returns the "unknown session" error path. The stored provider error is included in the error message so the ACP client sees the root cause.

- [ ] **Step 1: Write the failing test**

Add to `internal/acp/run_test.go`. This test builds a `runConfig` whose `startRuntime` returns a `*app.Runtime` with a nil `Runner` (and nil `State`), then sends `initialize` → `session/new` → `session/prompt` and asserts the prompt response is a `-32000` error (not a panic).

```go
func TestRunPromptTurnNilRunnerReturnsError(t *testing.T) {
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"sess_nil","prompt":[{"type":"text","text":"hi"}]}}` + "\n",
	)
	out := &bytes.Buffer{}

	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			// Runner and State are both nil — simulates a
			// buildAgentRunner failure where the error was swallowed.
			return &app.Runtime{SessionID: "sess_nil"}, nil
		},
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
		shutdown:     0,
	}

	// This must not panic. If it panics, the test fails via the panic.
	done := make(chan error, 1)
	go func() { done <- runWithConfig(context.Background(), in, out, cfg) }()

	select {
	case err := <-done:
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatal("runWithConfig did not return")
	}

	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var promptResp *Response
	for scan.Scan() {
		var resp Response
		if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
			continue
		}
		if resp.ID != nil && string(*resp.ID) == "3" {
			promptResp = &resp
			break
		}
	}
	if promptResp == nil {
		t.Fatalf("no response for id=3; output=%q", out.String())
	}
	if promptResp.Error == nil {
		t.Fatalf("expected error response for nil runner, got result: %+v", promptResp.Result)
	}
	if promptResp.Error.Code != serverError {
		t.Fatalf("error code = %d, want %d (serverError)", promptResp.Error.Code, serverError)
	}
}
```

No new imports are needed — `run_test.go` already has `"bufio"`, `"bytes"`, `"context"`, `"encoding/json"`, `"strings"`, `"testing"`, `"time"`, `"marshal/internal/app"`. The `serverError` constant and `Response` type are in the same `acp` package.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/acp/ -run TestRunPromptTurnNilRunnerReturnsError -v`
Expected: FAIL — the test goroutine panics (nil pointer dereference at `turn.go:219`), so `runWithConfig` either crashes the test binary or times out. The panic output will mention `turn.go:219`.

- [ ] **Step 3: Add the nil-guard in the Lookup closure**

In `internal/acp/run.go`, replace the `Lookup` closure (lines 91-107) with a version that returns `false` when `rt.Runner == nil`, including the provider error in a logged warning. The `PromptTurn` handler already returns `fmt.Errorf("acp: unknown session: %s", p.SessionID)` when `Lookup` returns `false`, which becomes a `-32000` server error on the wire.

```go
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			rt, ok := manager.Get(sessionID)
			if !ok || rt == nil {
				return nil, false
			}
			if rt.Runner == nil {
				// buildAgentRunner failed (route resolution, sandbox, MCP
				// start, etc.) and the error was stored via
				// state.SetProviderError. Returning false makes PromptTurn
				// return a -32000 "unknown session" error to the client
				// instead of panicking on the nil RunnerFunc.
				reason := "agent runner not built"
				if rt.State != nil {
					if perr := rt.State.ProviderError(); perr != nil {
						reason = perr.Error()
					}
				}
				log.Warn("acp: session has no runner; rejecting prompt",
					"session", sessionID, "reason", reason)
				return nil, false
			}
			var run RunnerFunc
			run = rt.Runner.Run
			evBroker, _ := rt.EventBroker.(*pubsub.Broker[session.Event])
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: rt.BeginWork,
				Run:       run,
				Events:    evBroker,
			}, true
		},
```

The `log` variable is already in scope (`run.go:39-42`: `log := cfg.logger; if log == nil { log = slog.Default() }`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/acp/ -run TestRunPromptTurnNilRunnerReturnsError -v`
Expected: PASS — the prompt response has `Error.Code == -32000`.

Run: `go test ./internal/acp/ -v`
Expected: PASS for all existing tests plus the new one.

Run: `go build ./...`
Expected: builds.

- [ ] **Step 5: Commit**

```bash
git add internal/acp/run.go internal/acp/run_test.go
git commit -m "fix(acp): reject prompt instead of panicking when runner is nil

When buildAgentRunner fails, the runtime's Runner is nil and the error is
stored via state.SetProviderError. The ACP Lookup closure now returns
false (logged with the provider error) so PromptTurn returns a -32000
server error instead of dereferencing a nil RunnerFunc."
```

---

## Task 2: HeadlessResolver for ACP trust

**Files:**
- Create: `internal/trust/headless.go`
- Create: `internal/trust/headless_test.go`
- Modify: `internal/acp/run.go:126-132` (the `Run` function)

**Interfaces:**
- Consumes: `trust.Resolver` interface (`internal/trust/trust.go:121`); `trust.Store` (`internal/trust/trust.go:27`); `trust.ConfigHashFor` (`internal/trust/trust.go:77`); `app.WithTrustResolver` (`internal/app/app.go:138`); `app.WithWorkingDir` (`internal/app/app.go:148`).
- Produces: `HeadlessResolver` struct implementing `trust.Resolver`; `NewHeadlessResolver(store *Store, logger *slog.Logger) *HeadlessResolver`; wired into `acp.Run` so headless ACP sessions load project-local config when the cwd has no stored trust record.

- [ ] **Step 1: Write the failing tests**

Create `internal/trust/headless_test.go`:

```go
package trust

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestHeadlessResolverTrustsUntrustedProject(t *testing.T) {
	dir := t.TempDir()
	marshalDir := filepath.Join(dir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marshalDir, "config.toml"), []byte("[project]\nname = \"x\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir())
	r := NewHeadlessResolver(store, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	decision, err := r.Resolve(dir, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision != DecisionTrustSession {
		t.Fatalf("decision = %s, want %s (untrusted headless project should be trusted for session)", decision, DecisionTrustSession)
	}
}

func TestHeadlessResolverHonorsStoredPermanentTrust(t *testing.T) {
	dir := t.TempDir()
	marshalDir := filepath.Join(dir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marshalDir, "config.toml"), []byte("[project]\nname = \"x\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir())
	abs, _ := filepath.Abs(dir)
	hash, _ := ConfigHashFor(dir)
	if err := store.SetTrust(abs, true, hash); err != nil {
		t.Fatal(err)
	}
	r := NewHeadlessResolver(store, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	decision, err := r.Resolve(dir, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision != DecisionTrustPermanent {
		t.Fatalf("decision = %s, want %s (stored permanent trust should win)", decision, DecisionTrustPermanent)
	}
}

func TestHeadlessResolverRePromptsOnConfigChangeByDistrusting(t *testing.T) {
	dir := t.TempDir()
	marshalDir := filepath.Join(dir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marshalDir, "config.toml"), []byte("[project]\nname = \"v1\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir())
	abs, _ := filepath.Abs(dir)
	hash, _ := ConfigHashFor(dir)
	if err := store.SetTrust(abs, true, hash); err != nil {
		t.Fatal(err)
	}
	// Change the config so the stored hash no longer matches.
	if err := os.WriteFile(filepath.Join(marshalDir, "config.toml"), []byte("[project]\nname = \"v2\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewHeadlessResolver(store, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	decision, err := r.Resolve(dir, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Config changed since trust was recorded. Headless cannot prompt, so
	// it falls back to session trust (not DontTrust) so the project config
	// still loads but the change is visible in the log warning.
	if decision != DecisionTrustSession {
		t.Fatalf("decision = %s, want %s (changed config should degrade to session trust, not dont_trust)", decision, DecisionTrustSession)
	}
}

func TestHeadlessResolverNoProjectConfig(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(t.TempDir())
	r := NewHeadlessResolver(store, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	decision, err := r.Resolve(dir, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision != DecisionDontTrust {
		t.Fatalf("decision = %s, want %s (no project config)", decision, DecisionDontTrust)
	}
}

func TestHeadlessResolverRecordIsNoOp(t *testing.T) {
	store := NewStore(t.TempDir())
	r := NewHeadlessResolver(store, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	// Record must not persist anything — headless trust is session-scoped
	// and must not silently grant permanent trust to an unattended cwd.
	if err := r.Record(t.TempDir(), DecisionTrustSession); err != nil {
		t.Fatalf("Record: %v", err)
	}
	recs, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("headless Record persisted %d records, want 0", len(recs))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/trust/ -run TestHeadlessResolver -v`
Expected: FAIL — `undefined: NewHeadlessResolver`.

- [ ] **Step 3: Write the implementation**

Create `internal/trust/headless.go`:

```go
package trust

import (
	"log/slog"
	"path/filepath"
)

// HeadlessResolver is a trust resolver for non-interactive transports
// (ACP over stdio, CI, scripted runs). It never prompts: when no stored
// trust record exists for the cwd, it returns DecisionTrustSession so
// the project-local config loads, with a logged warning. When a stored
// permanent-trust record exists and the config hash matches, it returns
// DecisionTrustPermanent (same as TerminalResolver). When the stored
// hash no longer matches (config changed since trust was recorded), it
// degrades to DecisionTrustSession rather than silently re-extending
// permanent trust — the change is visible in the log.
//
// Record is a no-op: headless trust is session-scoped and must not
// persist permanent trust for an unattended working directory.
type HeadlessResolver struct {
	store  *Store
	logger *slog.Logger
}

// NewHeadlessResolver constructs a HeadlessResolver backed by the given
// store. A nil logger defaults to slog.Default().
func NewHeadlessResolver(store *Store, logger *slog.Logger) *HeadlessResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &HeadlessResolver{store: store, logger: logger}
}

func (r *HeadlessResolver) Resolve(workingDir string, hasProjectConfig bool) (Decision, error) {
	abs, _ := filepath.Abs(workingDir)
	if !hasProjectConfig {
		return DecisionDontTrust, nil
	}
	trusted, err := r.store.IsTrusted(abs)
	if err != nil {
		// Store error (corrupted, unreadable): fall through to session
		// trust so a broken store never blocks a headless session from
		// loading its config. The error is logged, not returned, because
		// returning it would make config.Load fail hard.
		r.logger.Warn("trust: headless resolver: store error, trusting for session",
			"dir", abs, "error", err)
		return DecisionTrustSession, nil
	}
	if trusted {
		currentHash, hashErr := ConfigHashFor(workingDir)
		if hashErr != nil {
			r.logger.Warn("trust: headless resolver: cannot hash config, trusting for session",
				"dir", abs, "error", hashErr)
			return DecisionTrustSession, nil
		}
		storedHash, _ := r.store.StoredConfigHash(abs)
		if storedHash == currentHash {
			return DecisionTrustPermanent, nil
		}
		// Config changed since permanent trust was recorded. Degrade to
		// session trust so the config still loads, but log the mismatch
		// so the change is visible. Unlike TerminalResolver, we cannot
		// re-prompt, so DontTrust would silently drop the config — the
		// exact bug this resolver exists to fix.
		r.logger.Warn("trust: headless resolver: config changed since permanent trust, degrading to session",
			"dir", abs)
		return DecisionTrustSession, nil
	}
	// No stored trust record. Trust for this session only and log it so
	// the operator knows the project config was loaded without an explicit
	// trust grant.
	r.logger.Warn("trust: headless resolver: no stored trust, trusting project config for session",
		"dir", abs)
	return DecisionTrustSession, nil
}

// Record is a no-op. Headless trust is session-scoped: persisting
// permanent trust for an unattended cwd would be a security hole.
func (r *HeadlessResolver) Record(workingDir string, decision Decision) error {
	return nil
}

// Ensure the type satisfies the Resolver interface at compile time.
var _ Resolver = (*HeadlessResolver)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/trust/ -run TestHeadlessResolver -v`
Expected: PASS for all five tests.

Run: `go test ./internal/trust/ -v`
Expected: PASS for all existing tests plus the new ones.

- [ ] **Step 5: Wire HeadlessResolver into acp.Run**

In `internal/acp/run.go`, the `Run` function (lines 126-132) currently passes no trust resolver, so `startRuntime` falls back to `NewTerminalResolver`. Add `WithTrustResolver` so headless ACP uses the `HeadlessResolver`.

Replace the `Run` function:

```go
func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	log := logging.New(stderr, slog.LevelInfo)
	dataDir := filepath.Join(homeDir(), ".local", "share", "marshal")
	trustStore := trust.NewStore(dataDir)
	return runWithConfig(ctx, stdin, stdout, runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			opts = append(opts, app.WithTrustResolver(trust.NewHeadlessResolver(trustStore, log)))
			return app.StartRuntime(ctx, opts...)
		},
		lister:   newPerCwdLister(),
		shutdown: connectionShutdownTimeout,
		logger:   log,
	})
}
```

Add these imports to `run.go`: `"path/filepath"`, `"marshal/internal/trust"`. The `homeDir` function does not exist yet — check if `os.UserHomeDir` is used elsewhere in the package. If not, inline it:

```go
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("acp: find home directory: %w", err)
	}
	dataDir := filepath.Join(home, ".local", "share", "marshal")
```

Add `"os"` and `"fmt"` to the imports if not present (check the existing import block — `run.go` currently imports `"context"`, `"encoding/json"`, `"errors"`, `"io"`, `"log/slog"`, `"time"`, `"marshal/internal/app"`, `"marshal/internal/app/logging"`, `"marshal/internal/app/session"`, `"marshal/internal/pubsub"`). Add `"fmt"`, `"os"`, `"path/filepath"`, `"marshal/internal/trust"`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go build ./...`
Expected: builds.

Run: `go test ./internal/acp/ -v`
Expected: PASS for all tests. The existing tests inject their own `startRuntime` via `runConfig`, so they are unaffected by the `Run` change.

Run: `go test ./internal/trust/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/trust/headless.go internal/trust/headless_test.go internal/acp/run.go
git commit -m "fix(trust): headless ACP loads project config instead of silently dropping it

TerminalResolver returns DontTrust when stdin is not a TTY, so every
headless ACP session ignored .marshal/config.toml and ran with defaults.
HeadlessResolver trusts the cwd for the session (logged) when no stored
record exists, honors stored permanent trust when the hash matches, and
degrades to session trust on a config change. Record is a no-op so
unattended sessions never persist permanent trust."
```

---

## Task 3: Tolerate zoneless timestamps in trust store

**Files:**
- Modify: `internal/trust/trust.go:35-48` (`Store.Load`)
- Test: `internal/trust/trust_test.go`

**Interfaces:**
- Consumes: `encoding/json`, `time`.
- Produces: `Store.Load` that parses `trusted_at` with a permissive fallback when the standard RFC3339 layout fails on a missing timezone offset.

- [ ] **Step 1: Write the failing test**

Add to `internal/trust/trust_test.go`:

```go
func TestStoreLoadToleratesZonelessTimestamp(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	// Hand-write a store with a trusted_at that lacks a timezone offset,
	// as an external tool or older version might produce.
	zoneless := `{"some/path":{"trusted":true,"config_hash":"abc","trusted_at":"2026-07-24T15:40:50"}}`
	if err := os.WriteFile(filepath.Join(dir, "trust.json"), []byte(zoneless), 0600); err != nil {
		t.Fatal(err)
	}
	recs, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v (zoneless trusted_at should not fail)", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if !recs["some/path"].Trusted {
		t.Fatalf("record not trusted: %+v", recs["some/path"])
	}
}

func TestStoreIsTrustedToleratesZonelessTimestamp(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	zoneless := `{"some/path":{"trusted":true,"config_hash":"abc","trusted_at":"2026-07-24T15:40:50"}}`
	if err := os.WriteFile(filepath.Join(dir, "trust.json"), []byte(zoneless), 0600); err != nil {
		t.Fatal(err)
	}
	ok, err := store.IsTrusted("some/path")
	if err != nil {
		t.Fatalf("IsTrusted: %v (zoneless trusted_at should not fail)", err)
	}
	if !ok {
		t.Fatal("IsTrusted = false, want true (zoneless timestamp should not block trust)")
	}
}
```

Check the existing import block in `trust_test.go` — it already has `"os"`, `"path/filepath"`, `"testing"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/trust/ -run TestStoreLoadToleratesZonelessTimestamp -v`
Expected: FAIL — `Load: parse trust store: parsing time "2026-07-24T15:40:50" as "2006-01-02T15:04:05Z07:00": cannot parse "" as "Z07:00"`.

Run: `go test ./internal/trust/ -run TestStoreIsTrustedToleratesZonelessTimestamp -v`
Expected: FAIL — `IsTrusted` swallows the `Load` error and returns `false`, so the test fails on `IsTrusted = false, want true`.

- [ ] **Step 3: Write the implementation**

The issue is that `time.Time` JSON unmarshal requires RFC3339 with a zone. The fix uses a custom `Record` type with a `trusted_at` field that tries RFC3339 first, then a zoneless layout fallback. Replace the `Load` function in `internal/trust/trust.go`:

```go
// loadRecord is the on-disk shape with a permissive trusted_at parser.
// It accepts both RFC3339 (with timezone) and a zoneless layout so a
// store written by an external tool or older version does not fail to
// load.
type loadRecord struct {
	Trusted    bool   `json:"trusted"`
	ConfigHash string `json:"config_hash,omitempty"`
	TrustedAt  flexTime `json:"trusted_at"`
}

// flexTime is a time.Time wrapper that unmarshals from RFC3339 with a
// zone, falling back to a zoneless layout when the zone is absent.
type flexTime struct {
	time.Time
}

func (f *flexTime) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	// Try the standard RFC3339 layout first (with timezone).
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		f.Time = t
		return nil
	}
	// Fallback: zoneless RFC3339-ish layout. Treat as UTC.
	t, err = time.Parse("2006-01-02T15:04:05", s)
	if err == nil {
		f.Time = t.UTC()
		return nil
	}
	return fmt.Errorf("parse time %q: %w", s, err)
}

func (s *Store) Load() (map[string]Record, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Record{}, nil
		}
		return nil, fmt.Errorf("read trust store: %w", err)
	}
	var raws map[string]loadRecord
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, fmt.Errorf("parse trust store: %w", err)
	}
	records := make(map[string]Record, len(raws))
	for k, v := range raws {
		records[k] = Record{
			Trusted:    v.Trusted,
			ConfigHash: v.ConfigHash,
			TrustedAt:  v.TrustedAt.Time,
		}
	}
	return records, nil
}
```

Add `"strings"` to the import block of `trust.go` if not present (it currently imports `"crypto/sha256"`, `"encoding/hex"`, `"encoding/json"`, `"fmt"`, `"os"`, `"path/filepath"`, `"time"`). Add `"strings"`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/trust/ -run TestStoreLoadToleratesZonelessTimestamp -v`
Expected: PASS.

Run: `go test ./internal/trust/ -run TestStoreIsTrustedToleratesZonelessTimestamp -v`
Expected: PASS.

Run: `go test ./internal/trust/ -v`
Expected: PASS for all existing tests plus the new ones. The existing `TestStoreLoad` / `TestStoreSave` tests use `time.Now()` (which includes a zone), so they still pass via the RFC3339 path.

- [ ] **Step 5: Commit**

```bash
git add internal/trust/trust.go internal/trust/trust_test.go
git commit -m "fix(trust): tolerate zoneless trusted_at in trust store

Store.Load used time.Time JSON unmarshal which requires RFC3339 with a
timezone offset. A store written by an external tool or older version
with a zoneless trusted_at failed to parse, and IsTrusted swallowed the
error and returned false — silently treating a trusted project as
untrusted. flexTime tries RFC3339 first, then a zoneless layout (parsed
as UTC) so both forms load."
```

---

## Task 4: Normalize truncated tool names from providers

**Files:**
- Modify: `internal/agent/execute.go:392-423` (`executeNativeToolCalls`)
- Test: `internal/agent/runner_misc_test.go`

**Interfaces:**
- Consumes: `registry.Registry` (`internal/tools/registry/registry.go`); `schema.ToolCall` (`internal/llm/schema/chat.go:26`).
- Produces: `normalizeToolName(reg *registry.Registry, name string) string` that maps a truncated name (`read`) to the unique registered tool whose name ends in `.read` (`file.read`); returns the input unchanged when the name is already registered or the suffix is ambiguous.

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/runner_misc_test.go`. This test registers `file.read` and `shell.run`, then sends a native tool call with the truncated name `read` and asserts it resolves to `file.read` (not "unknown tool").

```go
func TestRunNativeTruncatedToolNameResolves(t *testing.T) {
	reg := registry.New()
	called := ""
	if err := reg.Register(registry.Tool{
		Name:        "file.read",
		Description: "read a file",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			called = call.Name
			return registry.ToolResult{Summary: "ok", Content: "file contents"}, nil
		},
	}); err != nil {
		t.Fatalf("Register file.read: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name:        "shell.run",
		Description: "run a command",
		Risk:        registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register shell.run: %v", err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_1", Name: "read", Args: json.RawMessage(`{"path":"go.mod"}`)}},
		},
	}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "read go.mod"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if called != "file.read" {
		t.Fatalf("handler called with name %q, want %q (truncated 'read' should resolve to 'file.read')", called, "file.read")
	}
}

func TestRunNativeTruncatedToolNameAmbiguousFails(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "file.read",
		Description: "read a file",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register file.read: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name:        "repo.read",
		Description: "read repo",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register repo.read: %v", err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_1", Name: "read", Args: json.RawMessage(`{}`)}},
		},
	}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "read something"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	// The ambiguous 'read' should NOT resolve to either tool; the handler
	// must not be called. Instead the model gets an "unknown tool" error
	// message and produces a final answer.
	foundUnknown := false
	for _, msg := range p.Requests[1].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "call_1" && strings.Contains(msg.Content, "unknown tool") {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Fatalf("ambiguous 'read' should produce 'unknown tool' error, not silently resolve; messages: %#v", p.Requests[1].Messages)
	}
}
```

Check the existing import block in `runner_misc_test.go` — it already has `"context"`, `"encoding/json"`, `"strings"`, `"testing"`, `"marshal/internal/llm/schema"`, `"marshal/internal/agent/agenttest"`, `"marshal/internal/app/config"`, `"marshal/internal/tools/registry"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestRunNativeTruncatedToolNameResolves -v`
Expected: FAIL — `handler called with name "", want "file.read"` (the handler is never called because `read` is not registered; the model gets "unknown tool").

Run: `go test ./internal/agent/ -run TestRunNativeTruncatedToolNameAmbiguousFails -v`
Expected: PASS already (ambiguous names already fail as "unknown tool" — this is the guard test ensuring the fix doesn't break ambiguity).

- [ ] **Step 3: Write the implementation**

In `internal/agent/execute.go`, add a `normalizeToolName` function and call it in `executeNativeToolCalls` before dispatching each call. Add after the `executeNativeToolCalls` function (after line 423):

```go
// normalizeToolName maps a provider-returned tool name back to a
// registered tool name when the provider truncated the namespace prefix.
// Some models (e.g. kimi-k2.7-code) return "read" for a tool registered
// as "file.read", stripping the segment before the dot. This function
// matches the returned name against the suffix of every registered tool:
// if exactly one registered tool ends in ".<name>", it returns that
// tool's full name. If the name is already registered, or zero/multiple
// tools match, the name is returned unchanged (the caller's "unknown
// tool" path handles the latter two).
func normalizeToolName(reg *registry.Registry, name string) string {
	if reg == nil {
		return name
	}
	if _, ok := reg.Lookup(name); ok {
		return name
	}
	suffix := "." + name
	var match string
	count := 0
	for _, tool := range reg.List() {
		if strings.HasSuffix(tool.Name, suffix) {
			match = tool.Name
			count++
		}
	}
	if count == 1 {
		return match
	}
	return name
}
```

Add `"strings"` to the import block of `execute.go` if not present. Check the existing imports — `execute.go` already imports `"strings"` and `"marshal/internal/tools/registry"`, so no new imports are needed.

Then modify `executeNativeToolCalls` to call it. Replace the dispatch section (lines 411-416):

```go
		resultMsgs, err := r.executeToolCall(ctx, ModelAction{
			Type:       ActionToolCall,
			Tool:       call.Name,
			Args:       call.Args,
			ToolCallID: call.ID,
		})
```

with:

```go
		normalized := normalizeToolName(r.Registry, call.Name)
		resultMsgs, err := r.executeToolCall(ctx, ModelAction{
			Type:       ActionToolCall,
			Tool:       normalized,
			Args:       call.Args,
			ToolCallID: call.ID,
		})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestRunNativeTruncatedToolNameResolves -v`
Expected: PASS — `called == "file.read"`.

Run: `go test ./internal/agent/ -run TestRunNativeTruncatedToolNameAmbiguousFails -v`
Expected: PASS — ambiguous `read` still produces "unknown tool".

Run: `go test ./internal/agent/ -run TestRunNative -v`
Expected: PASS for all native tool tests including the existing `TestRunNativeUnknownToolAnswersToolCallIDWithError` (which uses `missing.tool` — not a suffix of any registered tool, so `normalizeToolName` returns it unchanged and the "unknown tool" path fires as before).

Run: `go build ./...`
Expected: builds.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/execute.go internal/agent/runner_misc_test.go
git commit -m "fix(agent): normalize truncated tool names from providers

Some models (e.g. kimi-k2.7-code) return the last segment of a dotted
tool name as the function name (read for file.read, run for shell.run),
causing every native tool call to fail as 'unknown tool'.
normalizeToolName matches the returned name against the suffix of every
registered tool and remaps it when the suffix is unambiguous. Ambiguous
or unmatched names pass through unchanged so the existing 'unknown tool'
error path fires."
```

---

## Final verification

- [ ] Run `gofmt -w .` and `go vet ./...`.
- [ ] Run the full suite: `go test ./...` — expected PASS.
- [ ] Confirm the ACP nil-runner path returns a `-32000` error (Task 1 test).
- [ ] Confirm a headless ACP session with a project config loads it (Task 2: `HeadlessResolver` returns `DecisionTrustSession` for an un-trusted cwd).
- [ ] Confirm a trust store with a zoneless `trusted_at` loads without error (Task 3 tests).
- [ ] Confirm a truncated tool name (`read`) resolves to `file.read` when unambiguous, and fails as "unknown tool" when ambiguous (Task 4 tests).