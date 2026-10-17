# Test isolation and credential access — implementation plan

Status: ready for review; implementation has not started.
Date: 2026-10-17.
Base: `main` at `16d0c04a5a357fae9250765c3d51beda5c07a3cd`.
Approved design: [Isolated tests and credential access](../feature-spec.md).
Authoring: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md) and [work-decomposition](../../../internal/skills/builtin/work-decomposition.md).
Execution after approval: [marshal-executing-plans](../../../internal/skills/builtin/marshal-executing-plans.md).

## Goal

Default tests use explicit test-owned configuration, credentials and services. They cannot inherit the developer's Marshal configuration or open the real keychain through the startup, provider, worker, MCP, probe or auth paths covered by this change.

## Non-goals

Changing production login behavior, token formats or secure-storage policy; disabling authentication in tests; a general network sandbox; latency/UI redesign; background execution lifetime changes; running real-service integration tests automatically.

## Assumptions and integration boundary

- The user approved the spec. This document is the implementation plan for review, not authorization to execute it.
- Background execution has been submitted separately. This base predates that implementation. Rebase onto its actual merged code before executing tasks that edit `app.go`/`runtime.go`; keep its service ownership, admission and shutdown contracts intact.
- Keep default production provider/worker OAuth-engine sharing. Injected test resolvers must have their own caches and never enter the production cache.
- Use constructor/options injection, not binary-name detection, a production test-mode branch, global auth disabling or per-test mutation of production factory globals.
- TUI code receives dependencies and submits existing actions; no new authentication policy or agent dependency belongs there. `web/` stays standard-library-only.
- Tests may use installed compilers and explicitly configured build/module caches as build resources. Isolating HOME must not cause subprocess Go fixtures to download dependencies; pass their cache/toolchain environment explicitly. Do not copy the developer's home/config into fixtures.
- Sequence all tasks below; several share files/interfaces. Each task includes tests, verifies independently and is committed before the next: `test-isolation: task N — <title>`.
- Existing declarations are source anchors checked at this base. Names marked **new** are contracts introduced by this plan. Recheck anchors after rebasing.
- Preserve unrelated `docs/fleet-cli/`, `internal/app/tui/config.toml` and the background-execution plan.

## New dependency contracts

The provider package will own an `AuthResolver` interface with `OpenStore(service)` and `CodexOAuthEngine(name)` methods. The default adapter delegates to the existing production factories/cache. A resolver built with an injected store factory owns separate store/engine caches.

Add `provider.NewFromConfigWithAuth` alongside the existing factory wrappers, with the existing session-aware arguments followed by an explicit `AuthResolver`. Existing public wrappers use the default adapter; injected paths call the explicit constructor.

Add `app.WithAuthResolver`, `Runtime.AuthResolver` and `routedProviderResolver.auth`. The same object goes to refresh workers, MCP managers and TUI/auth/probe dependencies, surviving clone/reload/new/resume paths. An explicitly supplied nil app resolver fails validation rather than silently selecting the real backend.

MCP uses a store-opening function, not a provider dependency. Add `CommandRuntime.OpenStore func() (credentials.Store, error)`. ACP's existing explicit `OpenStore` override remains supported; otherwise a session command obtains this runtime opener before considering the production default for legacy embedded callers. New UI dependency names are `tui.WithAuthResolver`, `connect.Opts.AuthResolver` and `settings.WithAuthResolver`.

## Task 1: Make credential constructor coverage hermetic

**Goal** — All default credential tests exercise fake backends, while the real round trip remains explicitly opt-in.

**Files**

- `internal/credentials/credentials.go`
- `internal/credentials/credentials_test.go`

**Steps**

1. At `New`, extract its existing body into a **new** private `newWithOpener(service string, open func(keyring.Config) (keyring.Keyring, error))`. Replace its `keyring.Open` invocation with the supplied opener. Preserve empty-service validation, secure backend whitelist and unavailable/nil-backend error behavior.
2. The public constructor becomes this verbatim wrapper:

   ```go
   func New(service string) (Store, error) {
   	return newWithOpener(service, keyring.Open)
   }
   ```

3. Change `TestNewNeverReturnsNilStore` to exercise successful fake, opener failure and nil-without-error returns through `newWithOpener`, using existing `errRing`. Assert service/allowed backends reach the opener and empty service never invokes it. Keep adapter/memory-store tests intact. Do not install a package-global opener replacement.
4. In `TestLiveKeyringRoundTrip` replace `os.Getenv("MARSHAL_CREDENTIALS_LIVE_TEST") == ""` with `os.Getenv("MARSHAL_CREDENTIALS_LIVE_TEST") != "1"`. Keep the public production constructor only in this explicit live path. Give its entry a unique per-invocation key under the dedicated test service so cleanup cannot delete another test's entry; cleanup deletes only that key.
5. Add fake-constructor assertions for unavailable storage and secure-only backend selection. All default success coverage must stay fake-backed even if the live switch is enabled.

**Verify**

```sh
MARSHAL_CREDENTIALS_LIVE_TEST=0 CGO_ENABLED=1 go test ./internal/credentials -count=1
MARSHAL_CREDENTIALS_LIVE_TEST=0 CGO_ENABLED=1 go test -race ./internal/credentials
MARSHAL_CREDENTIALS_LIVE_TEST=0 CGO_ENABLED=1 go test ./internal/credentials -run '^TestLiveKeyringRoundTrip$' -v
```

Expected: ordinary tests pass without opening any OS backend; the last command reports the live test skipped. Do not run the real opt-in during automated execution without a separate user request.

## Task 2: Isolate default home/config environments

**Goal** — Existing package test entry points default to a temporary home, and unrelated app tests use explicit fixtures.

**Files**

- `internal/testenv/home.go` — new; complete contents below.
- `internal/testenv/testenv_test.go`
- `internal/app/main_test.go`
- `internal/app/config/main_test.go`
- `internal/app/tui/main_test.go`
- `internal/app/tui/settings/main_test.go`
- `internal/app/tui/skills/main_test.go`
- `internal/app/tui/plugins/main_test.go`
- `internal/tools/native/main_test.go`
- `internal/plugins/main_test.go`
- `internal/app/app_test.go`
- `internal/acp/main_test.go` — new, complete contents below.
- `internal/llm/provider/main_test.go` — new, complete contents below.
- `internal/tools/mcp/main_test.go` — new, complete contents below.
- `internal/app/tui/connect/main_test.go` — new, complete contents below.
- `internal/app/tui/probe/main_test.go` — new, complete contents below.

**Steps**

1. Create `home.go` verbatim. It runs before package tests, restores state on return and preserves explicitly named live-test switches. Tests that deliberately change environment still do so in their own nonparallel scope.

   ```go
   package testenv

   import (
   	"fmt"
   	"os"
   	"testing"
   )

   var isolatedKeys = []string{
   	"HOME", "USERPROFILE",
   	"MARSHAL_CONFIG_DIR", "MARSHAL_DATA_DIR",
   	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
   	"OLLAMA_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY",
   	"OPENAI_API_KEY", "OPENCODE_API_KEY", "ANTHROPIC_API_KEY",
   	"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "KIMI_API_KEY",
   	"MISTRAL_API_KEY", "TOGETHER_API_KEY", "FIREWORKS_API_KEY",
   	"XAI_API_KEY", "CEREBRAS_API_KEY",
   }

   // IsolateHome changes process environment; call before parallel work.
   func IsolateHome() (func(), error) {
   	dir, err := os.MkdirTemp("", "marshal-test-home-")
   	if err != nil {
   		return nil, err
   	}
   	type priorValue struct {
   		value string
   		set   bool
   	}
   	prior := make(map[string]priorValue, len(isolatedKeys))
   	for _, key := range isolatedKeys {
   		value, set := os.LookupEnv(key)
   		prior[key] = priorValue{value: value, set: set}
   	}
   	cleanup := func() {
   		for _, key := range isolatedKeys {
   			previous := prior[key]
   			if previous.set {
   				_ = os.Setenv(key, previous.value)
   			} else {
   				_ = os.Unsetenv(key)
   			}
   		}
   		_ = os.RemoveAll(dir)
   	}
   	for _, key := range isolatedKeys {
   		value := ""
   		if key == "HOME" || key == "USERPROFILE" {
   			value = dir
   		}
   		if err := os.Setenv(key, value); err != nil {
   			cleanup()
   			return nil, err
   		}
   	}
   	return cleanup, nil
   }

   // Run isolates package defaults without disabling runtime behavior.
   func Run(m *testing.M) int {
   	cleanup, err := IsolateHome()
   	if err != nil {
   		fmt.Fprintln(os.Stderr, "isolate test home:", err)
   		return 1
   	}
   	defer cleanup()
   	return m.Run()
   }
   ```

2. In the eight listed existing `main_test.go` files, replace the pair `testenv.SanitizeXDG()` / `os.Exit(m.Run())` with `os.Exit(testenv.Run(m))`, and update comments to describe the temporary default home. Keep `SanitizeXDG` itself available for its existing contract/tests.
   Create the additional package entry points with these complete contents:

   `internal/acp/main_test.go`:

   ```go
   package acp

   import (
   	"os"
   	"testing"

   	"marshal/internal/testenv"
   )

   func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
   ```

   `internal/llm/provider/main_test.go`:

   ```go
   package provider

   import (
   	"os"
   	"testing"

   	"marshal/internal/testenv"
   )

   func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
   ```

   `internal/tools/mcp/main_test.go`:

   ```go
   package mcp

   import (
   	"os"
   	"testing"

   	"marshal/internal/testenv"
   )

   func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
   ```

   `internal/app/tui/connect/main_test.go`:

   ```go
   package connect

   import (
   	"os"
   	"testing"

   	"marshal/internal/testenv"
   )

   func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
   ```

   `internal/app/tui/probe/main_test.go`:

   ```go
   package probe

   import (
   	"os"
   	"testing"

   	"marshal/internal/testenv"
   )

   func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
   ```

3. Extend `testenv_test.go` with a synthetic prior home/config/key environment, isolation assertions, retained explicit live switch and cleanup restoration/removal. Test an absent variable separately from an empty one. Do not use `t.Parallel` around process-environment changes.
4. At `TestRunCreatesDatabase` and `TestRunLogsToFileNotTerminal`, explicitly inject test homes and config. These tests assert DB/logging, so they need no configured remote/OAuth provider. Prefer `WithWorkingDir` over changing the process working directory. Keep actual loader coverage in loader-specific fixtures rather than accidentally removing it.
5. Add a loader regression with a synthetic ambient OAuth/MCP configuration and a separate injected home. Assert only fixture config loads. Use fake endpoints/dependencies where startup is involved; do not run the ambient configuration against the real credential backend.
6. Check subprocess fixtures affected by HOME redirection. Keep their explicit Go/module-cache environment or inject test-owned cache paths with dependencies already available; do not allow accidental downloads or depend on user Git signing/credential configuration.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/testenv ./internal/app/config ./internal/plugins
CGO_ENABLED=1 go test ./internal/app -run '^TestRunCreatesDatabase$|^TestRunLogsToFileNotTerminal$' -count=1
CGO_ENABLED=1 go test ./internal/acp ./internal/llm/provider ./internal/tools/mcp ./internal/app/tui/connect ./internal/app/tui/probe -run '^$'
```

Expected: helper restoration is correct; DB/logging tests use isolated explicit config; path-precedence tests retain their intended overrides.

## Task 3: Add isolated OAuth resolvers without changing production sharing

**Goal** — A test can construct providers with private credential/engine caches while production wrappers keep their current shared engine.

**Files**

- `internal/llm/provider/auth.go` — new; complete contents below.
- `internal/llm/provider/codex_oauth.go`
- `internal/llm/provider/factory.go`
- `internal/llm/provider/factory_test.go`

**Steps**

1. At `NewCodexOAuthEngine`, extract pure engine construction into **new** `NewCodexOAuthEngineWithStore(name string, store credentials.Store) (*oauth.Engine, error)`. Reject nil stores and preserve existing storage key, flow constants/scopes and no-browser startup behavior. The old constructor opens its existing store and delegates; `CodexOAuthEngine` retains its existing production cache.
2. Create `auth.go` verbatim. Its default adapter uses the existing global behavior; injected resolvers never call that adapter or modify its cache.

   ```go
   package provider

   import (
   	"fmt"
   	"sync"

   	"marshal/internal/credentials"
   	"marshal/internal/oauth"
   )

   type AuthResolver interface {
   	OpenStore(service string) (credentials.Store, error)
   	CodexOAuthEngine(name string) (*oauth.Engine, error)
   }

   type defaultAuthResolver struct{}

   func DefaultAuthResolver() AuthResolver { return defaultAuthResolver{} }

   func (defaultAuthResolver) OpenStore(service string) (credentials.Store, error) {
   	return credentials.New(service)
   }

   func (defaultAuthResolver) CodexOAuthEngine(name string) (*oauth.Engine, error) {
   	return CodexOAuthEngine(name)
   }

   type scopedAuthResolver struct {
   	mu      sync.Mutex
   	open    func(string) (credentials.Store, error)
   	stores  map[string]credentials.Store
   	engines map[string]*oauth.Engine
   }

   func NewAuthResolver(open func(string) (credentials.Store, error)) (AuthResolver, error) {
   	if open == nil {
   		return nil, fmt.Errorf("auth resolver: store factory is required")
   	}
   	return &scopedAuthResolver{
   		open: open, stores: make(map[string]credentials.Store),
   		engines: make(map[string]*oauth.Engine),
   	}, nil
   }

   func (r *scopedAuthResolver) storeLocked(service string) (credentials.Store, error) {
   	if store, ok := r.stores[service]; ok {
   		return store, nil
   	}
   	store, err := r.open(service)
   	if err != nil {
   		return nil, err
   	}
   	if store == nil {
   		return nil, fmt.Errorf("auth resolver: store factory returned nil: %w", credentials.ErrKeyringUnavailable)
   	}
   	r.stores[service] = store
   	return store, nil
   }

   func (r *scopedAuthResolver) OpenStore(service string) (credentials.Store, error) {
   	r.mu.Lock()
   	defer r.mu.Unlock()
   	return r.storeLocked(service)
   }

   func (r *scopedAuthResolver) CodexOAuthEngine(name string) (*oauth.Engine, error) {
   	r.mu.Lock()
   	defer r.mu.Unlock()
   	if engine, ok := r.engines[name]; ok {
   		return engine, nil
   	}
   	store, err := r.storeLocked("marshal")
   	if err != nil {
   		return nil, err
   	}
   	engine, err := NewCodexOAuthEngineWithStore(name, store)
   	if err != nil {
   		return nil, err
   	}
   	r.engines[name] = engine
   	return engine, nil
   }
   ```

3. At `NewFromConfigWithSession`, move the existing switch body into **new** `NewFromConfigWithAuth`, taking the same arguments followed by `auth AuthResolver`. Reject nil auth before use; only the Codex branch changes factory selection to `auth.CodexOAuthEngine(name)`. The existing session-aware wrapper delegates with `DefaultAuthResolver()`; `NewFromConfig` retains its signature. Non-OAuth provider behavior stays intact.
4. Extend existing factory tests with two resolvers using the same provider name but distinct memory stores/tokens; stable engine identity within one resolver; concurrent lookup opening once; missing/nil/error factory behavior; errors not cached as success; explicit factory use during Codex provider construction; and existing non-OAuth wrapper compatibility.
5. Use fake auth resolvers for explicit factory-selection tests and recording memory stores for sharing tests. Verify the default Codex adapter's straightforward delegation during source review; do not invoke the real production engine or seed/mutate its global cache to test that wrapper.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/llm/provider
CGO_ENABLED=1 go test -race ./internal/llm/provider -run 'Auth|FromConfig|Codex'
```

Expected: injected caches remain private; concurrent calls share one engine/store per resolver; ordinary factory compatibility holds.

## Task 4: Make MCP credential construction instance-scoped

**Goal** — MCP managers can exercise real OAuth logic through an explicit store dependency without mutating package globals.

**Files**

- `internal/tools/mcp/manager.go`
- `internal/tools/mcp/manager_test.go`
- `internal/acp/commands.go`
- `internal/acp/mcpauth.go`
- `internal/acp/mcpauth_test.go`

**Steps**

1. At `ManagerOption`/`NewManager`, introduce **new** `WithCredentialFactory(func(string) (credentials.Store, error))` and retain it on each manager. Default construction uses `credentials.New`. An explicitly supplied nil opener fails the OAuth construction path without falling back to the OS.
2. At `oauthEngine`, use that instance opener. Remove `oauthCredentialsNew` and migrate tests currently saving/restoring that global to the new option. Keep per-server errors, storage keys, token refresh behavior and plaintext prohibition.
3. Extend MCP manager tests with concurrent independent managers, missing tokens, unavailable storage, successful token use/refresh and cancellation, all using memory stores plus test-owned HTTP servers.
4. Extend `CommandRuntime` with an optional store opener supplied by its owning runtime. In headless auth, preserve precedence: explicit `CommandManagerConfig.OpenStore` override, runtime opener, then existing production fallback for legacy embedded callers. Once an injected opener is selected, its error propagates; never retry against real credentials.
5. Extend existing ACP auth tests for that precedence, injected failure and separate session stores. Keep authorization URL notifications and browserless headless flow unchanged.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/tools/mcp ./internal/acp -run 'OAuth|MCP|Auth|Store'
CGO_ENABLED=1 go test -race ./internal/tools/mcp ./internal/acp -run 'OAuth|MCP|Auth|Store'
```

Expected: managers/auth commands use their selected fake stores; no global factory races or real-store fallback on errors.

## Task 5: Carry auth dependencies through app lifecycle and workers

**Goal** — Startup, provider construction, workers and session rebuilds share the explicitly selected auth resolver.

**Files**

- `internal/app/app.go`
- `internal/app/runtime.go`
- `internal/app/app_test.go`
- `internal/app/runtime_test.go`
- `internal/llm/provider/oauthworker/worker_test.go`
- `internal/acp/host.go`
- `internal/acp/host_test.go`
- `internal/acp/session_test.go`

**Steps**

1. At app `options` and `StartRuntime`/`startRuntime`, add **new** `WithAuthResolver(provider.AuthResolver)`. Resolve the default once at the outer construction boundary; distinguish omitted from explicitly nil injection and reject the latter. Retain the resolver on `Runtime`, including the inactive-provider startup path.
2. Extend `routedProviderResolver`, `newRoutedProviderResolver`, `withSessionID` and `withRoleOverrides` to preserve the resolver. `providerFor` calls `NewFromConfigWithAuth`. Thread it through `buildAgentRunnerWithLock` and every caller/factory, preserving background-execution ownership changes when rebasing.
3. Make `buildOAuthWorkers` take the same resolver and call it for each configured OAuth provider. Keep workers real and supervised in auth tests. The provider and its refresh worker must hold the exact same engine; keep an injectable worker's existing unrelated semantics.
4. Construct MCP managers with `WithCredentialFactory(auth.OpenStore)`. Retain auth through `reloadAgentRuntime`, `Runtime.NewSession` and the `Run` resume/restart loop; candidates and failures must not silently use production defaults. Do not clear a custom resolver's cache during an ordinary config reload.
5. At ACP host `CommandRuntime` construction, provide the runtime's opener to Task 4's field. Existing `SessionManagerConfig.Options` can carry `app.WithAuthResolver` in tests. Keep control-plane config tests using their existing explicit probe callback rather than invoking a production provider factory.
6. Extend app/runtime tests with a counting/denying resolver for non-auth fixtures and real memory-backed engines for auth fixtures. Cover startup → reload → new → resumed session, failed rebuild, inactive-route recovery and two parallel runtimes sharing a provider name without tokens crossing.
7. Verify provider/worker engine identity, then exercise a synthetic token read and refresh against a test-owned server. Use channels/barriers or existing worker clock/sleep seams; do not wait for production retry intervals. Extend worker tests only where composition adds an uncovered assertion.
8. Turn the diagnostic ambient-config probe into durable regression coverage using these explicit seams: record that unisolated synthetic config would request credentials, then prove fixture-isolated startup does not. Both cases use fake stores/endpoints and never real developer data.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app ./internal/llm/provider/oauthworker ./internal/acp
CGO_ENABLED=1 go test -race ./internal/app ./internal/acp -run 'Auth|OAuth|Reload|Runtime|Resume|NewSession'
```

Expected: dependencies survive all lifecycle transitions; provider/worker sharing remains correct; independent runtimes and ambient fixtures stay isolated.

## Task 6: Inject auth into TUI login, discovery and settings probes

**Goal** — TUI auth/probe paths use the runtime's selected dependencies and can be tested without keychain or browser access.

**Files**

- `internal/app/tui/model.go`
- `internal/app/tui/model_test.go`
- `internal/app/tui/mcp.go`
- `internal/app/tui/mcp_test.go`
- `internal/app/tui/connect/connect.go`
- `internal/app/tui/connect/connect_test.go`
- `internal/app/tui/probe/probe.go`
- `internal/app/tui/probe/probe_test.go`
- `internal/app/tui/settings/browser.go`
- `internal/app/tui/settings/browser_test.go`
- `internal/app/tui/settings/state.go`
- `internal/app/tui/settings/frames_collections.go`
- `internal/app/tui/settings/frames_profiles.go`
- `internal/app/app.go`

**Steps**

1. Add a TUI option storing the resolved `provider.AuthResolver` and wire it from `app.Run`. Existing standalone constructors choose the production adapter only when no dependency was supplied. Pass the same object to `openConnect`, `openModels`, `probeProviders` and the settings browser.
2. At `connect.Opts`/`New` and `enterOAuthLogin`, consume that resolver. Capability/model probes call the explicit auth-aware provider constructor. Add an injected browser opener used by `oauthLogin`; production retains `mcpauth.OpenBrowser` and tests supply a stub. Do not modify the login interaction or grant policy.
3. Add an explicit-auth probe entry point beside `probe.Provider`; retain the current wrapper for production/legacy callers. Use it in `runProbe`, parent model discovery and settings collection/profile discovery. Probe timeouts, remote-provider gates and limit-discovery behavior remain intact.
4. At `BrowserOption`/`NewBrowser`, retain auth on the settings `state` and carry it through frame rebuilds/clones. `probeProviderFor` and the calls in `frames_collections.go`/`frames_profiles.go` must use it. Otherwise a settings probe could bypass an isolated app's dependency.
5. At `handleMCPCommand` use the selected store opener, and provide a browser stub seam equivalent to connect. Denials/unavailable storage propagate unchanged; an injected error must never trigger a real-store fallback.
6. Update OAuth-route tests such as `TestRemoteGateRoutesOAuthTemplateToLogin` to supply explicit memory/failing resolvers, including tests that only construct an engine without running its command. Add executed-command tests with test-owned OAuth servers/stub browser openers and assert the opener's URL instead of launching a browser.
7. Extend model/connect/probe/settings/MCP tests for injection propagation, token lookup, missing/error behavior and changed/rebuilt panels retaining dependencies. Keep TUI code free of agent imports and new authentication policy.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app/tui/... ./internal/app
CGO_ENABLED=1 go test -race ./internal/app/tui/... -run 'OAuth|Auth|MCP|Provider|Probe'
```

Expected: auth and discovery commands remain functional through fake resources; constructing or executing ordinary UI tests opens neither real keychain nor browser.

## Task 7: Close remaining default-suite leaks and enforce the boundary

**Goal** — Default-suite auth/config/endpoint entry points have explicit isolated fixtures and regressions against accidental fallbacks.

**Files**

- `internal/acp/config_test.go`
- `internal/acp/host_test.go`
- `internal/acp/session_test.go`
- `internal/llm/provider/factory_test.go`
- `internal/tools/mcp/manager_test.go`
- `internal/app/app_test.go`
- `internal/app/runtime_test.go`
- `internal/app/tui/connect/connect_test.go`
- `test/usability/usability_test.go`
- `AGENTS.md`
- `docs/test-isolation/feature-spec.md`

**Steps**

1. Audit default-build test paths, not just URL strings. Construction-only fixtures may contain real-shaped endpoint names without making requests. Actual HTTP/model/token discovery must use owned servers/transports; a loopback service supplied by the developer is not a fixture.
2. At ACP `NewConfigManager` tests, supply the existing probe callback or test-owned endpoints. Host/session tests inject home/auth through their existing starter/options seams. Extend regressions so these tests cannot inherit real user config or reach the production default OAuth cache.
3. Inspect current environment-gated provider integration files and their `integration` build tags. Keep them excluded from ordinary builds; document their explicit invocation without running them. Preserve the exact live-keyring switch established in Task 1.
4. At usability `isolatedHomeEnv`, preserve its existing explicit Marshal/XDG paths and test-owned scripted providers. Strip template credential variables or supply synthetic values if its child environment currently inherits them. Preserve explicit binary and Go cache/toolchain settings; do not rely on a developer model daemon.
5. Add counting/failing-factory assertions across the patched construction paths: no credentials expected in ordinary DB/logging/control fixtures; expected fake reads in auth fixtures; injected errors stay local; same-name test instances remain independent under shuffled/race runs. Do not add a brittle blanket ban on every production `credentials.New` reference; production defaults still need the secure backend.
6. Add test guidance to AGENTS.md: isolated testenv/app fixtures, explicit auth resolver, owned endpoints, no package-global factory mutation for concurrent paths, and explicit live-test selection. Keep the approved spec current with exact API names and environment behavior.
7. Record the final audit in the spec with paths and evidence. A missing popup during one run is insufficient: use fake dependency counts, lifecycle identity assertions and a source audit of remaining production-default call sites.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app ./internal/acp ./internal/llm/provider ./internal/tools/mcp
CGO_ENABLED=1 go test -shuffle=on -count=2 ./internal/app ./internal/acp ./internal/llm/provider ./internal/tools/mcp
CGO_ENABLED=1 go test ./web/bridge -run '^TestWebIsStdlibOnly$'
```

Expected: ordinary tests work regardless of test order and synthetic ambient settings; live tests remain opt-in; web boundary remains intact.

## Task 8: Verify the full suite and prepare integration

**Goal** — The isolated default suite, lifecycle regressions and production build pass with reviewable evidence.

**Files**

- `docs/test-isolation/feature-spec.md`
- `docs/test-isolation/plans/2026-10-17-test-isolation-plan.md`

**Steps**

1. Reconcile the approved spec's eleven acceptance checks with the tests added in Tasks 1–7. Confirm that neither production auth disabling nor plaintext fallback was introduced and that background-execution ownership survived the shared-file edits.
2. In an isolated feature checkout, inspect the feature diff before formatting. Use the actual rebased base commit for changed-file selection; the commands below use this document's authored base and must be updated if the branch is rebased.
3. Build a dedicated test binary before the full suite, since usability tests execute a real Marshal binary. Keep the normal environment free of explicit live-test opt-ins. Use no real credentials or external model requests during verification.
4. Run the checks below, record outcomes and any genuine pre-existing failures, and resolve material failures before claiming completion. Repeat only checks justified by subsequent changes.
5. Request the repository-required reviewer subagent for the completed nontrivial code change; address material findings. Keep public docs with implementation commits, stage exact files and preserve unrelated work. Merge/push requires the user's chosen integration workflow.

**Verify**

```sh
git diff --name-only -z 16d0c04a -- '*.go' | xargs -0 gofmt -w
git diff --name-only -z 16d0c04a -- '*.go' | xargs -0 gofmt -l
CGO_ENABLED=1 go build -o /private/tmp/marshal-test-isolation ./cmd/marshal
MARSHAL_CREDENTIALS_LIVE_TEST=0 USABILITY_MARSHAL_BINARY=/private/tmp/marshal-test-isolation CGO_ENABLED=1 go test ./...
MARSHAL_CREDENTIALS_LIVE_TEST=0 CGO_ENABLED=1 go test -race ./internal/credentials ./internal/testenv ./internal/llm/provider ./internal/llm/provider/oauthworker ./internal/tools/mcp ./internal/app ./internal/app/tui/... ./internal/acp
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
git diff --check
```

Expected: formatting check prints nothing; full default tests, targeted races, vet and cgo build pass without OS credential interaction or incidental services. Do not enable live integration simply to make a unit test pass.

## Integration notes

- Tasks 3–6 are interface/file-coupled; execute sequentially. Task 1 makes low-level credential verification safe before broader suite runs. Do not run the entire TUI suite before Task 6 has replaced its default OAuth-engine construction in tests.
- Rebase/adapt `buildAgentRunnerWithLock` and lifecycle tests against the background-execution delivery instead of restoring its old signatures or manager cleanup.
- Retain production public factory wrappers and shared OAuth engine behavior. Injected instances must never fall back to global credentials after an error or during rebuild.
- The spec is approved; this plan remains uncommitted for review. Implementation has not started.

## Plan authoring checks

- Checked existing file/declaration anchors, new-file locations, relative links and all eight task structures against the stated base.
- Compiled the embedded credential wrapper and exercised its planned private opener through a temporary overlay with a fake backend.
- Compiled/exercised the embedded home helper, including environment restoration, and compiled the five added test entry points through temporary overlays.
- Compiled the embedded scoped resolver against the planned pure engine constructor and passed concurrent sharing/isolation checks under the race detector.
- These checks did not edit production source or access the real keychain. Full implementation tests and repository verification remain execution-time acceptance checks.
