# Test audit P0 — trustworthy baseline tests

Date: 2026-10-30. Status: executed — see the commits and verification record in
the [audit](../audit.md#verification-record).
Base: `5468c7823a6cb40e6c90047e778f433ba28686cb`.
Requirements: [test audit](../audit.md) and
[remediation roadmap](../roadmap.md#p0--restore-trustworthy-test-signals).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** fix reproducible fixture contamination and false-positive tests so
the remaining full-suite failures can be diagnosed from reliable signals.

**Non-goals:** change snapshot identity or process-group behavior, revise
prompt budgets, bypass command admission, implement the PTY screen model,
enable a full-suite PR gate, or change web UI. Those findings remain owned by
later stages in the roadmap. No real credentials, browser, external service or
developer Git configuration may be used as a fixture.

**Assumptions and decisions:**

- Keep production semantics intact. A test that exposes a new product defect
  is a finding to report, not a reason to weaken the assertion.
- Use the existing local Go/SQLite fixtures, `httptest`, and per-test env
  overrides; do not use package-global mutable factories. Theme and user
  environment isolation follow `docs/test-isolation/feature-spec.md`.
- A missing Playwright driver continues to use the existing explicit skip;
  an installed-but-broken browser is a failure. Tests must not fetch the
  public internet, and no new live integration dependency is introduced.
- Keep the test audit and roadmap in the working tree; include them in the
  eventual named documentation commit. Preserve unrelated untracked files.
  Tasks are sequential and independently verifiable. The full suite is
  expected to remain red until later roadmap stages.

## Contracts

No exported or cross-task type, function or wire contract is added. All
changes are test-only except a private, instance-scoped watcher readiness seam
in Task 6. Its exact declaration is local to that task and is not used by
another task.

## Task 1: Eliminate the LSP PID readiness race

**Goal:** cancellation occurs only after the stub published a valid PID, and
the test proves the owned process exited.

**Files:** `internal/lsp/manager_test.go`.

**Steps:**

1. In `TestStartServerCancelledAfterInit`, replace the `os.Stat(pidFile)`
   readiness check with a bounded loop that reads and parses a positive PID;
   send the parsed PID over a buffered channel before calling `cancel`. On
   read/parse timeout, report the error through the channel and cancel to
   avoid leaving the child running. The main test must read that result and
   fail on a readiness error rather than parsing a possibly empty file after
   cancellation. Preserve the no-registration and cleanup assertions.
2. In this test and the adjacent post-init cancellation test, treat only
   `syscall.ESRCH` or `os.ErrProcessDone` as proof the PID is gone; an EPERM
   or any other signal error fails with context. A successful signal-0 probe
   is still a leak. Do not change `processAlive` for unrelated callers unless
   the tests need the same explicit handling.

**Verify:**

```bash
go test ./internal/lsp -run '^TestStartServerCancelledAfterInit' -count=30 -timeout 90s
go test ./internal/lsp -count=1 -timeout 90s
gofmt -l internal/lsp/manager_test.go
```

Expected: tests pass repeatedly; formatting prints no paths.

## Task 2: Isolate the Git identity fixture

**Goal:** the no-repository-identity test is unaffected by the developer's
Git identity and still verifies the production commit fallback.

**Files:** `internal/worktree/git_test.go`.

**Steps:**

1. In `TestCLIGitOpsCommitAllWithoutARepoIdentity`, use `t.Setenv` to point
   `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_SYSTEM` at separate nonexistent paths
   under `t.TempDir`, set `GIT_CONFIG_NOSYSTEM=1` and `GIT_CONFIG_COUNT=0`,
   and unset `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME` and
   `GIT_COMMITTER_EMAIL` (empty values can themselves make Git reject an
   identity). Use a helper that restores the original environment in cleanup.
   Set these before `initRepoWithoutIdentity`. Do not
   change the user's actual Git config or the production Git adapter. Do not
   mark the test parallel while it changes process environment.
2. Add `TestCLIGitOpsNoIdentityIgnoresPoisonedGlobal`: create a synthetic
   global config containing an identity, install it in the ambient test env,
   assert a control `git config user.email` sees that synthetic identity,
   then run the same isolated fixture logic with its per-test config paths.
   Verify the guard sees no effective identity, `CommitAll` succeeds via its
   existing fallback, and the resulting author is not the synthetic identity.
   Refactor a private test helper if needed to avoid duplicating the setup.

**Verify:**

```bash
go test ./internal/worktree -run '^TestCLIGitOps(CommitAllWithoutARepoIdentity|NoIdentityIgnoresPoisonedGlobal)$' -count=5 -timeout 60s
go test ./internal/worktree -count=1 -timeout 90s
gofmt -l internal/worktree/git_test.go
```

Expected: tests pass with or without developer Git identity; formatting prints
no paths. Test-owned paths must not modify a real global Git configuration.

## Task 3: Replace the public browser fixture

**Goal:** standalone navigation and screenshot tests use deterministic,
test-owned content.

**Files:** `internal/tools/desktop/browser/standalone_test.go`.

**Steps:**

1. In both `TestStandaloneBackendNavigateAndRead` and
   `TestStandaloneBackendScreenshot`, start an `httptest.NewServer` serving a
   static HTML page with title `Marshal fixture` and body `owned browser
   fixture`. Defer its close. Navigate to `server.URL`; assert navigation
   errors before inspecting title, readable text or screenshot.
2. In the read test, assert the exact title and that readable text includes
   the fixture body. In the screenshot test, assert nonempty valid PNG data
   using the standard PNG decoder, in addition to the existing size check.
   Retain the existing short-mode and missing-driver behavior and no external
   internet dependency.

**Verify:**

```bash
go test ./internal/tools/desktop/browser -run '^TestStandaloneBackend(NavigateAndRead|Screenshot)$' -count=1 -v -timeout 90s
gofmt -l internal/tools/desktop/browser/standalone_test.go
```

Expected: tests pass with an installed driver, or report the existing
missing-driver skip; formatting prints no paths. Record which occurred.

## Task 4: Assert session-list cache reuse

**Goal:** the cache regression exercises a real database and proves the same
handle is reused across public list calls.

**Files:** `internal/acp/lister_test.go`.

**Steps:**

1. Replace `TestListSessionsCachesDatabase`'s directory-only fixture with an
   actual migrated DB and a seeded project/session, as in
   `TestPerCwdListerRealDB`; close the seeding handle. Use the same absolute
   cwd for seeding and both `ListSessions` calls. Close the lister in cleanup.
2. After the first list call, read the cached handle under `l.mu` and assert
   that it exists and the session appears. After the second, assert the same
   handle remains cached and the same session is returned. Remove the
   elapsed-time/log-only check; retain the existing absent-database and
   close/reopen tests.

**Verify:**

```bash
go test ./internal/acp -run '^Test(ListSessionsCachesDatabase|ListSessionsDoesNotCreateDatabase|PerCwdLister)' -count=1 -timeout 90s
gofmt -l internal/acp/lister_test.go
```

Expected: tests pass; formatting prints no paths.

## Task 5: Test both access/reload orderings

**Goal:** the access test checks valid serialized outcomes rather than
assuming a stale approval must always be granted.

**Files:** `internal/app/runtime_test.go`.

**Steps:**

1. Preserve the first part of
   `TestRuntimeAccessSerializesWithReloadAndQuiesce`, which verifies that
   `reloadMu` blocks access resolution. Replace only the final race block
   that discards both errors and unconditionally demands a grant with two
   deterministic subtests using `newAccessTestRuntime`, `registerAccessCh`,
   `approve`, and `reloadAgentRuntime`.
2. In `grant before reload`, resolve the original request, assert
   `session.AccessGranted`, nil error and one affirmative response, then
   reload and assert effective Act/System access and runner `ForceClass`.
   In `reload before grant`, reload first, submit the original choice, assert
   `session.AccessRefresh`, nil error, no response and a still-pending
   request; obtain the refreshed `state.AccessRequest()`, approve that
   current request, and assert a single grant and matching effective runner
   state. Capture and assert every operation's returned error. Do not alter
   the revision or approval logic.

**Verify:**

```bash
go test ./internal/app -run '^TestRuntimeAccessSerializesWithReloadAndQuiesce$' -count=20 -timeout 90s
go test ./internal/app -run '^TestRuntimeAccess' -count=1 -timeout 90s
gofmt -l internal/app/runtime_test.go
```

Expected: both orderings pass consistently and formatting prints no paths.

## Task 6: Prove a watcher continues after an error

**Goal:** the test observes a second callback after the first fails, with a
deterministic readiness signal.

**Files:** `internal/index/watcher.go`, `internal/index/watcher_test.go`.

**Steps:**

1. Add an unexported per-instance optional field `ready func()` to
   `Watcher`; invoke it synchronously in `Run` immediately after
   `w.addRecursive(fsw, w.root)` returns and before entering the select loop.
   A nil hook does nothing. Tests set it before starting the watcher; do not
   introduce a package global or modify normal watcher behavior.
2. Change `startWatcher` and `TestWatcherContinuesAfterRunError` to await a
   closed readiness channel with a bounded timeout instead of sleeping 60 ms.
   In the error test, have the callback send its invocation number to a
   buffered channel and return an error. Write one file, wait for invocation
   one, then write a distinct second file and wait for invocation two. Assert
   the watcher did not exit before the second callback; cancel and assert it
   terminates. Use `t.Cleanup` to cancel/join on early failures.
3. Keep the coalescing test's existing bounded debounce observation; propagate
   its file-write errors instead of discarding them. This task does not claim
   an fsnotify event must be delivered under an unsupported filesystem.

**Verify:**

```bash
go test ./internal/index -run '^TestWatcher' -count=20 -timeout 90s
go test -race ./internal/index -count=1 -timeout 90s
gofmt -l internal/index/watcher.go internal/index/watcher_test.go
```

Expected: tests pass with no race and formatting prints no paths.

## Task 7: Assert approval activity instead of logging it

**Goal:** the runner test fails if approval activity is never published.

**Files:** `internal/agent/runner_misc_test.go`.

**Steps:**

1. In `TestRunnerSetsActivityDuringApproval`, replace the first fixed sleep
   with a bounded wait for both `state.PendingApproval()` and
   `state.Activity().Kind == session.ActivityApproval`, while monitoring the
   runner result channel; registration precedes publication of the activity,
   so waiting only for the pending slot would introduce another race. Fail
   immediately if the runner exits early and fail on timeout with the observed
   activity. Assert both conditions before answering.
   Return the goroutine's error through a buffered channel instead of logging
   from it. Use a cleanup cancellation so failures cannot leak the goroutine.
2. Respond using the existing denial, wait for the runner result with a
   deadline, and assert the expected completion/error and final idle activity.
   Remove the second fixed sleep and preserve the fixture's explicit confirm
   rule. If the asserted live activity is not published, report the product
   defect rather than weakening the assertion.

**Verify:**

```bash
go test ./internal/agent -run '^TestRunnerSetsActivityDuringApproval$' -count=20 -timeout 90s
go test ./internal/agent -count=1 -timeout 90s
gofmt -l internal/agent/runner_misc_test.go
```

Expected: tests pass consistently and formatting prints no paths.

## Final verification and integration

After all tasks pass, run from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-test-p0 ./cmd/marshal
go test ./internal/lsp ./internal/worktree ./internal/tools/desktop/browser ./internal/acp ./internal/index ./internal/agent -count=1 -timeout 2m
go test ./internal/app -run '^TestRuntimeAccessSerializesWithReloadAndQuiesce$' -count=10 -timeout 90s
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -count=1 -timeout 5m
go vet ./...
gofmt -l internal/lsp/manager_test.go internal/worktree/git_test.go internal/tools/desktop/browser/standalone_test.go internal/acp/lister_test.go internal/app/runtime_test.go internal/index/watcher.go internal/index/watcher_test.go internal/agent/runner_misc_test.go
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-test-p0 go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Formatting must print no paths. Use an environment that permits test-owned
loopback listeners; record any missing browser driver rather than claiming
that its tests ran. Compare the full-suite failures to the 24 named failures
in [the audit](../audit.md#verification-record) and distinguish fixed,
remaining and newly discovered failures. Remaining snapshot, prompt-budget,
shell-admission and process-group findings are expected until later stages;
do not suppress them. Update only addressed audit findings with actual evidence
and update the roadmap stage status after verification. Commit named files,
the audit and roadmap with the work.

## Plan self-review

- Seven independently verifiable tasks target concrete reproducible fixture
  failures or tests with missing assertions. The only production seam is a
  nil-by-default watcher readiness callback, scoped to an instance.
- The remaining audit findings are mapped to later roadmap stages; this plan
  does not claim they are fixed or that the full suite will already pass.
- The user must review this plan before execution. No source changes were
  made during planning.
