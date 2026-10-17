# Isolated tests and credential access

Status: implemented.
Date: 2026-10-17.
Source baseline: `16d0c04a`.
Implementation: complete; see [Implementation record](#implementation-record).

## Purpose

Running Marshal's normal test suite must not depend on the developer's saved Marshal configuration, credentials or running services. In particular, ordinary tests must not read or write the real OS credential store or provoke its authorization dialogs.

The user approved this specification, keeping the delivery separate from latency tracing and the system-access approval UI.

## Evidence

The original investigation is in `.docs-archive/triage/2026-10-15-testing-observations.md`, section 2. The postmortem supplement found no direct record of the dialogs; their absence is not evidence that the issue did not occur.

Current source still contains these paths:

- `internal/app/main_test.go` calls `testenv.SanitizeXDG`, which clears config/data path overrides but leaves the process home available.
- `TestRunCreatesDatabase` and `TestRunLogsToFileNotTerminal` in `internal/app/app_test.go` call production `Run` without an isolated home or an injected config loader.
- `buildOAuthWorkers` in `internal/app/app.go` constructs a refresh worker for configured OAuth Codex providers. Supplying an unrelated test worker does not suppress this construction.
- Provider construction also calls `provider.CodexOAuthEngine`. Replacing only refresh-worker construction would leave this second credential path.
- `oauthworker.Worker.Run` loads stored tokens through its engine; that reaches the credential store's `Get` operation.
- `TestNewNeverReturnsNilStore` in `internal/credentials/credentials_test.go` calls the real backend-opening constructor despite its comment describing the test as hermetic.
- A real keyring round-trip test already exists behind `MARSHAL_CREDENTIALS_LIVE_TEST`.

A temporary Go-overlay probe reproduced the config-to-credential-read path without accessing the real keychain. It supplied a synthetic ambient home containing an OAuth provider, ran the production config-loading/app path, and replaced the OS credential factory with a recording in-memory store. The refresh worker called that store's `Get` before the injected TUI runner returned. The focused probe passed.

This proves an unintended dependency path in tests. It does not establish which exact invocation generated the user's macOS dialogs. The implementation must verify isolation through controlled credential factories rather than intentionally triggering those dialogs.

## Scope

Included:

- Shared test setup for isolated home, config, data and relevant environment.
- Explicit configuration in tests that are not testing the production loader.
- Instance-scoped credential/OAuth injection through app, provider, worker and MCP construction paths exercised by ordinary tests.
- Isolation across startup, reload, new/resumed sessions and test instances.
- Hermetic credential-constructor unit tests and explicit live integration coverage.
- Regressions showing the default test paths do not consult real credentials or incidental developer services.

Separate deliveries:

- Background command lifetime and shutdown behavior, already submitted for execution.
- Startup/tool latency tracing, optimization and narration diagnosis.
- The system-access approval surface, Send now and running-task UI.
- Changing login flows, token formats, provider configuration or production credential storage policy.
- A general operating-system network sandbox or a new cross-platform test runner.

## Default-suite contract

The boundary for `go test ./...` is:

| Resource | Ordinary test behavior |
| --- | --- |
| User home/config/data | Test-owned temporary directories or explicit injected fixtures. |
| Saved provider and MCP definitions | Explicit test configuration; no inheritance from real user config. |
| OS credential store | No opening, reading, writing or deletion. |
| OAuth credentials | Synthetic tokens in memory or an explicit failing test store. |
| HTTP services | In-process transports or test-owned loopback servers. |
| Local services such as Ollama/MCP daemons | Test-owned fixtures; an installed or already-running service is not assumed. |
| Files, Git repositories and subprocesses | Test-owned fixtures using the test's explicit paths and environment. |
| Real services/keychain integration | Explicitly invoked integration coverage, excluded from ordinary runs. |

Tests may exercise failures such as unavailable credential storage, missing tokens and failed refreshes with fakes. They must not silently skip normal authentication logic to avoid touching the system.

This is an isolation contract, not a claim that setting `HOME` alone isolates the keychain or that a loopback address necessarily belongs to the test.

## Design

### Shared environment setup

Extend the existing `internal/testenv` foundation and app test helpers rather than duplicating ad hoc setup.

The setup creates a test-owned home and corresponding config/data locations, neutralizes inherited Marshal/XDG path overrides, and removes or explicitly supplies provider credential environment variables relevant to the fixture. Existing tests that deliberately exercise path/environment precedence can override that setup within their own scope.

Use explicit home/working-directory options at app boundaries. Tests of config loading use actual fixture files under those temporary roots. Tests of database creation, logging or unrelated runtime behavior can use `WithConfigLoader` with explicit config.

Keep isolation compatible with package test processes and parallel tests. Avoid unsynchronized mutations of global environment, current working directory or factories. Helpers that change process state must restore it and cannot be used concurrently with incompatible tests.

Path setup does not substitute for credential injection.

### Credential and OAuth dependencies

Provide an explicit dependency path from runtime construction to the credential/OAuth consumers it builds. Reuse existing functional-option patterns and `credentials.Store`/`NewMemStore`.

The dependency must cover both provider creation and refresh workers. For a given provider in one runtime, they must receive the same OAuth engine or shared token-refresh coordination so proactive and inline refresh cannot race rotated tokens.

Tests receive isolated resolver/store instances. Injected engines must not enter the production global engine cache, and two test runtimes with the same provider name must not share synthetic tokens accidentally.

The injected dependencies persist through config reload and session rebuild/resume. These paths must not fall back to the real factory after a successful isolated startup.

For MCP, reuse the existing ACP `OpenStore` seam and provide equivalent instance-scoped dependencies where managers or TUI auth construction currently reach directly for the credential factory. This changes dependency plumbing, not the authorization interaction or capability policy.

Prefer instance-scoped dependencies over mutable package-global factory substitutions for paths that may execute concurrently. Keep any low-level constructor seam narrowly scoped to its own unit tests and restore it reliably.

A test that does not expect credential activity supplies a factory that fails on attempted access. An auth-focused test supplies a recording in-memory store and asserts the intended calls. Both must operate before any real backend is opened.

### Exercise the real wiring with fake resources

Normal tests still run authentication and worker behavior when that is what they test:

- Provider construction, token lookup and proactive refresh use the injected shared engine.
- Missing/expired tokens and unavailable storage produce their real error behavior.
- Refresh requests use a test-owned server or transport.
- Worker start, cancellation and shutdown are observed through explicit synchronization.
- MCP authentication uses test-owned endpoints and a stub browser opener where needed.

Do not add a production test-mode branch that disables authentication or all background workers. Do not fix the symptom by swallowing credential errors or assuming every test should construct a successful empty store.

### Credential package and live integration coverage

Unit tests for `credentials.New` exercise constructor success/failure through a fake keyring opener, including unavailable backend, empty service and invalid opener returns. The success case must not call the real `keyring.Open`. Existing store adapter tests remain fake-backed.

Retain the existing live-test entry point:

```sh
MARSHAL_CREDENTIALS_LIVE_TEST=1 go test ./internal/credentials/...
```

Treat exactly `1` as the explicit opt-in. The live test uses a dedicated test service/key, preserves production credential entries and cleans up only what it created. Ordinary tests remain fake-backed even when live coverage is selected.

Other tests requiring real services must also declare and document their explicit integration invocation. Do not introduce a broad single switch that makes the whole unit suite consult the developer's configuration or credentials.

### Production behavior

Default application construction continues to use secure OS credential storage. It retains the existing errors when secure storage is unavailable and does not fall back to plaintext.

The injected path must preserve token-refresh sharing and existing production authentication behavior. Changing the production ownership of all OAuth engines is unnecessary unless required to maintain those invariants; isolate test instances without redesigning authentication.

No secrets, real config contents or token values are needed in test diagnostics. Report the attempted dependency and operation using synthetic fixture identities.

## Verification

Acceptance checks:

1. A simulated ambient home contains OAuth providers and MCP definitions; an isolated database/logging test does not load them or open a credential backend.
2. A config-loader test reads only its fixture layers, including intentional environment-precedence overrides.
3. Provider construction and its refresh worker use the same injected engine; a recording memory store proves the expected synthetic token lookup.
4. Unexpected credential construction fails through the injected test boundary before reaching the OS.
5. Startup, reload, new session and resumed session all retain the injected dependencies.
6. Two test runtimes using the same provider name cannot observe each other's tokens or cached engines; test order does not change results.
7. Missing tokens, unavailable stores, failed refreshes, successful refreshes and worker shutdown remain covered without real credentials.
8. MCP manager/auth paths exercised by tests use injected stores and test-owned HTTP/browser dependencies.
9. Credential constructor unit tests open no real backend. The live test runs only on explicit opt-in and cleans its dedicated entry.
10. A configured local endpoint supplied by the developer is not used incidentally; networked tests use test-owned servers/transports.
11. Focused tests and the full ordinary suite pass on macOS without keychain interaction and on CI without a credential service. Race checks cover the changed construction and factory-sharing paths.

Use barriers/channels to establish worker and reload ordering. Do not use the absence of a popup during one run as the only proof of isolation.

Run the narrow affected packages first, then the normal Go suite, appropriate race checks, vet and a cgo build. The execution plan will name exact files and commands after review.

## Relationship to the background-execution delivery

This feature should make the new lifecycle regressions safe to run without ambient credentials. It must preserve the background-execution design's runtime ownership and reload contracts.

Both deliveries may touch app construction and rebuild wiring. Implement against the actual merged state and coordinate those shared files; do not replace its execution service or restore old reload cleanup while adding credential injection.

The two features remain independently specified: one determines how user work lives and stops; this one determines which resources tests are allowed to access.

## Implementation record

Implemented on branch `test-isolation`, based on `c31240a3` (which contains the
plan's `16d0c04a` baseline). One commit per plan task, in order:

| Task | Commit subject |
| --- | --- |
| 1 | `test-isolation: task 1 — Make credential constructor coverage hermetic` |
| 2 | `test-isolation: task 2 — Isolate default home/config environments` |
| 3 | `test-isolation: task 3 — Add isolated OAuth resolvers without changing production sharing` |
| 4 | `test-isolation: task 4 — Make MCP credential construction instance-scoped` |
| 5 | `test-isolation: task 5 — Carry auth dependencies through app lifecycle and workers` |
| 6 | `test-isolation: task 6 — Inject auth into TUI login, discovery and settings probes` |
| 7 | `test-isolation: task 7 — Close remaining default-suite leaks and enforce the boundary` |

### API names as implemented

- `internal/credentials`: `newWithOpener(service, open)` (private); `New` is the
  `keyring.Open` wrapper. Live switch is exactly `MARSHAL_CREDENTIALS_LIVE_TEST=1`,
  service `marshal-credentials-selftest`, per-invocation key.
- `internal/testenv`: `IsolateHome() (func(), error)` and `Run(m *testing.M) int`.
  `SanitizeXDG` is retained for its existing contract.
- `internal/llm/provider`: `AuthResolver`, `DefaultAuthResolver()`,
  `NewAuthResolver(open func(string) (credentials.Store, error))`,
  `NewCodexOAuthEngineWithStore(name, store)`,
  `NewFromConfigWithAuth(name, pc, dataDir, remoteLimitDiscovery, thinkingBudgetMargin, sessionID, auth)`.
- `internal/tools/mcp`: `WithCredentialFactory(func(string) (credentials.Store, error))`;
  the `oauthCredentialsNew` package global is gone.
- `internal/acp`: `CommandRuntime.OpenStore`; precedence is
  `CommandManagerConfig.OpenStore` → `CommandRuntime.OpenStore` → `credentials.New("marshal")`.
- `internal/app`: `WithAuthResolver(provider.AuthResolver)`, `Runtime.AuthResolver`,
  `ErrAuthResolverRequired`.
- `internal/app/tui`: `WithAuthResolver`, `WithOpenBrowser`, `probe.ProviderWithAuth`,
  `connect.Opts.Auth`/`OpenBrowser`, `settings.WithAuthResolver`.

### Acceptance checks

| # | Check | Evidence |
| --- | --- | --- |
| 1 | Ambient OAuth/MCP config is not loaded by an isolated DB/logging test | `TestLoaderIsolationIgnoresAmbientUserConfig` (`internal/app/app_test.go`) plants a synthetic ambient config and proves a control load *would* merge it; `TestFixtureStartupDoesNotTouchAmbientCredentialConfig` (`internal/app/runtime_test.go`) |
| 2 | Loader reads only its fixture layers | Same test's injected-home load; `internal/app/config` fixtures |
| 3 | Provider and refresh worker share one injected engine | `TestOAuthWorkerSharesEngineAndRefreshesAgainstOwnedServer`, `assertProviderWorkerShareEngine`; `TestRunPersistsRotatedTokenForInlineReads` (`oauthworker`) |
| 4 | Unexpected credential construction fails before the OS | `unmappedAuthResolver` fails closed on omitted injection; `TestWithAuthResolverRejectsExplicitNil`; `denyingAuthResolver` in `factory_test.go` |
| 5 | Startup, reload, new and resumed sessions retain the dependency | App lifecycle tests; `TestSessionResumeAndNewRetainInjectedAuthResolver` (`internal/acp/session_test.go`) |
| 6 | Same-name runtimes cannot observe each other's tokens | `TestAuthResolverKeepsEnginesAndTokensPrivatePerResolver`; `TestManagersUseIndependentCredentialStoresConcurrently`; `-shuffle=on -count=2` runs |
| 7 | Missing tokens, unavailable stores, failed/successful refresh, shutdown | `internal/tools/mcp` OAuth fixtures; `TestOAuthEngineErrorsStayPerServer`; `oauthworker` tests |
| 8 | MCP manager/auth use injected stores and owned HTTP/browser | `TestCommandManagerMCPAuthUsesRuntimeStore`, `…RuntimeStoreFailurePropagates`, `…SeparateSessionStores`; `TestMCPAuthAuthorizeCommandUsesStubBrowser` |
| 9 | Constructor unit tests open no real backend; live test opt-in and self-cleaning | `TestNewNeverReturnsNilStore` (fake opener), `TestNewWithOpenerUnavailableBackend`, `TestNewWithOpenerSelectsSecureBackendsOnly`; `TestLiveKeyringRoundTrip` skips unless `=1` |
| 10 | A developer-supplied local endpoint is not used incidentally | Task 7 audit below; `newConfigTestManager` now installs a failing test-owned probe |
| 11 | Focused and full ordinary suites pass without keychain interaction | Task 8 verification run |

### Task 7 audit

Default-build test paths were audited for real credential, config and endpoint
reach. One actionable finding:

- `internal/acp/config_test.go` — `newConfigTestManager` passed a nil probe to
  `NewConfigManager`, which installs production's `provider.NewFromConfig` +
  `p.Models(ctx)`. The fixture providers include `http://localhost:11434`, so a
  test that did not override `m.probe` would have probed whatever the developer
  had listening there. The shared fixture now installs a failing test-owned
  probe.

Everything else was already isolated by Tasks 1–6: ACP host/session tests inject
`app.WithAuthResolver` with a memory-backed `countingStoreResolver` and assert
zero unrequested opens; `factory_test.go` uses `httptest` handlers and
`credentials.NewMemStore()` and asserts the production `codexEngines` cache stays
untouched; `manager_test.go` routes every OAuth path through
`WithCredentialFactory`; `app_test.go`/`runtime_test.go` carry the ambient-config
regressions. Real-shaped URLs (`https://api.anthropic.com`, `localhost:11434`)
appear only as constructor inputs that make no request.

`test/usability/usability_test.go` now also strips the ambient provider API-key
variables and `MARSHAL_CREDENTIALS_LIVE_TEST` from the child environment, keeping
its explicit Marshal/XDG paths, binary path and Go cache settings.

The environment-gated provider integration files
(`internal/llm/provider/{integration,anthropic_integration,ollama_native_integration}_test.go`)
all carry `//go:build integration`, are absent from an untagged `go test -list`,
and appear only under `-tags=integration`. They were not run.

### Deviations from the plan

- **Background execution was not merged.** The plan's rebase instruction was a
  no-op: `reliable-background-execution` is not an ancestor of the base, so the
  `app.go`/`runtime.go` edits were made against the current tree.
- **Omitted injection fails closed.** The plan required rejecting an explicitly
  nil resolver. `newRoutedProviderResolver`, `buildAgentRunnerWithLock` and
  `buildOAuthWorkers` additionally treat an *omitted* resolver as
  `unmappedAuthResolver` rather than `DefaultAuthResolver()`. Only
  `resolveAuthResolver` (reached from `Run`/`StartRuntime`) selects the
  production default, so a missed thread surfaces as a failure instead of
  silently reopening the keychain.
- **`internal/app/tui/model_options.go`** was added to Task 6's file set: its
  `probeThinkingOptions` called `probe.Provider` directly and would otherwise
  have bypassed the injected resolver on a model-discovery path.
- **Task 7 step 5 is partial.** The counting/failing-factory assertions it asks
  for largely already existed from Tasks 1–6 and were verified by reading rather
  than duplicated. One gap is recorded rather than filled: `internal/app/tui/connect`
  has no counting assertion that non-OAuth tests never *call* the default
  resolver. It is only reached on the OAuth path, so this is a nicety, not a hole.

## Review boundary

The detailed default-suite boundary, instance-scoped injection and retention of the existing explicit live-keyring test are approved and implemented. The implementation plan is a separate reviewable artifact; see the implementation record above for what landed and where it deviates.
