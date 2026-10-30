# Test audit

Date: 2026-10-30. Audited commit: `5468c7823a6cb40e6c90047e778f433ba28686cb`.
Status: findings for triage; no product or test fixes made during this audit.

This audit prioritizes the TUI's test coverage and the known Go-suite
baseline, with additional concrete test-quality findings elsewhere in the
repository. It does not audit the web product or claim exhaustive inspection
of the repository's 908 Go test files. The linked locations were inspected
at the audited commit. “Reproduced” identifies an actual observation;
“inferred” identifies a coverage or flake risk derived from the code.

## Coverage and harness

### Pull requests do not run the normal Go suite or vet

`.github/workflows/usability.yml:3` runs on pull requests and pushes to main,
but its only test command is `go test ./test/usability/... -run TestScripted`.
`.github/workflows/release.yaml:3` runs `go test ./...` and `go vet ./...`
only on tag pushes. A change can merge without the unit, boundary and
integration tests that protect it running in CI. The existing known failures
must be addressed before making the full suite a reliable required PR gate;
do not hide them with blanket exclusions. **Evidence:** workflow inspection.

### The sole scripted real-binary TUI journey lacks a close assertion

`test/usability/scenario/scenarios.go:18` sends `?`, waits for Help, then
sends Escape. `test/usability/actor/scripted/scripted.go:40` returns success
as soon as the last action was emitted: there is no observation that Escape
closed Help. `test/usability/scenario/scenario.go:43` defines `Success`, but
the runner's `ActionDone` branch does not evaluate it. Thus the named
`TestScriptedHelpOpenClose` can pass if the final Escape has no effect.
Add an explicit post-action condition, exercise a deliberately failing
postcondition, and decide whether `Scenario.Success` should be implemented or
removed. **Evidence:** actor and runner control flow.

### The PTY harness does not model the current terminal screen

`test/usability/harness/harness.go:63` appends every output chunk to an
unbounded buffer, and `Snapshot` at `:158` returns the entire history.
`test/usability/screen/parse.go:8` strips a subset of escape sequences but
does not apply cursor movement, erasure, alternate-screen or redraw commands.
Past text can therefore satisfy a later `ScreenContains` or boolean state
predicate after it has disappeared; absence and current-frame assertions are
unreliable. Add a terminal-state model with fixtures for redraw, erase,
alternate screen and Unicode; keep raw output separately for diagnostics.
This is a prerequisite for trustworthy postconditions and resize journeys.
**Evidence:** snapshot and parser implementation.

### Core TUI workflows lack real-binary interaction coverage

The only scripted scenario is Help (`test/usability/usability_test.go:112`;
`scenario/scenarios.go:18`), configured at 80 rows. The substantial in-process
model tests added for TUI polish cover actual `Model.Update` and composed
frames, but do not validate the PTY/terminal decoding and child process end to
end for approval and sudo review, questions, plugin/skill management, paste,
80×24 overflow, resize, or cancellation. After repairing the screen oracle,
add a small set of deterministic real-binary journeys with test-owned provider
and filesystem fixtures, including negative outcomes and 80×24. Do not
duplicate every panel unit test or require a real model. The harness currently
has no resize or bracketed-paste action (`harness/harness.go:33,83`).

### Optional LLM repair scenario trusts the actor's success claim

`test/usability/usability_test.go:139` checks only `res.Success`. The runner
copies that from `actor.ActionDone.Success` (`scenario/scenario.go:130`), and
the LLM actor can report success without the fixture's tests being run or the
missing function being fixed. If kept as a regression test, independently run
the fixture's Go tests and inspect the result; otherwise label the scenario as
exploratory, not a correctness gate. This scenario is opt-in and is not part
of the PR scripted job.

### Usability setup and cleanup limit what the suite can prove

The generic suite silently skips `TestScriptedHelpOpenClose` when the binary
is missing (`usability_test.go:112`); the dedicated PR workflow does build it,
so that specific job is not silently skipped. A dedicated integration job
should fail if its required binary is absent. `Session.Close` kills the child
(`harness/harness.go:193`), so it cannot prove graceful exit, terminal
restoration or shutdown persistence. Reports use the shared fixed directory
`/tmp/marshal-usability` (`usability_test.go:120,151`), risking concurrent
report overwrites. Use per-run artifacts and add an explicit graceful-exit
path with bounded forced cleanup.

`isolatedHomeEnv` pins Marshal/XDG config and data paths and drops provider
keys, but inherits `USERPROFILE` and `XDG_CACHE_HOME`
(`usability_test.go:70`); cross-platform isolation should be tested with
poisoned ambient variables. Its provider points to `localhost:11434` (`:42`),
which is not a test-owned service. The current Help journey does not establish
that it makes a request; new probe or inference scenarios need a controlled
endpoint before they can be considered hermetic.

## Baseline failures and flake candidates

### Snapshot service and catalog disagree on symlinked roots

**Reproduced:** `go test ./internal/snapshot -run
'^TestServiceStoreDirMatchesManagerCatalog$' -count=1` fails under the macOS
default temporary root, but passes with `TMPDIR=/private/tmp`. The manager
canonicalizes the project root before deriving a store identity while the
service hashes the raw root. `/var` and `/private/var` can therefore address
different stores for the same project. Other failing discovery, retention,
large-file, and startup-maintenance fixtures also pass under the canonical
temporary root. This is a potential production identity bug, not just a test
expectation: investigate one canonical identity and migration/discovery of
existing raw-hash and legacy stores. Do not bake the temporary-directory
workaround into the tests as the fix. Inspect `internal/snapshot/service.go`,
the manager/catalog implementation, and
`TestServiceStoreDirMatchesManagerCatalog`.

**Resolved (P1a, plan `2026-10-30-p1a-snapshot-identity-plan.md`):** one
canonical identity now governs the service, its prechecks and its captures.
`Service.Workspace()` and `StoreDir()` report the canonical catalog's hash, and
the raw spelling survives only as the one known historical identity. Exact
lookup, diff and restore try the canonical catalog first and then that
identity's validated v2 catalog or pre-v2 bare repository; maintenance covers
both validated catalogs under the existing store lock. Nothing is moved,
merged or deleted implicitly, and a workspace with no store is never
bootstrapped merely to be maintained. The fixtures now derive their expected
identity from the canonical catalog rather than the raw temporary path, so no
test depends on a `TMPDIR` override.

### Git fixtures inherit the developer's identity

**Reproduced:** `TestCLIGitOpsCommitAllWithoutARepoIdentity` fails with the
ambient Git configuration and passes with `GIT_CONFIG_GLOBAL=/dev/null` and
`GIT_CONFIG_NOSYSTEM=1`. Its repository has no local identity, but Git can
still discover a global identity, so the expected failure is not isolated.
Give the Git subprocess explicit fixture-owned config and add both poisoned
global-config and no-identity cases. Inspect `internal/worktree/*test.go`.

**Status (P0, fixed):** `TestCLIGitOpsCommitAllWithoutARepoIdentity` now points
`GIT_CONFIG_GLOBAL` and `GIT_CONFIG_SYSTEM` at separate nonexistent per-test
paths, sets `GIT_CONFIG_NOSYSTEM=1` and `GIT_CONFIG_COUNT=0`, and unsets
`GIT_AUTHOR_NAME`/`GIT_AUTHOR_EMAIL`/`GIT_COMMITTER_NAME`/
`GIT_COMMITTER_EMAIL` before building the fixture. A new
`TestCLIGitOpsNoIdentityIgnoresPoisonedGlobal` installs a synthetic global
identity, proves a control `git config user.email` sees it, then proves the
fixture guard sees no effective identity and that the fallback author is
neither the synthetic name nor email. Evidence: both tests pass 5/5 focused
runs and `go test ./internal/worktree` passes; the real global Git
configuration is never written.

### Standalone browser tests depend on an external website

`internal/tools/desktop/browser/standalone_test.go:77,117` navigates to
`https://example.com`; the read test expects site content that can change and
the screenshot test ignores its navigation error. Prior baseline runs failed
on content mismatch. Use a test-owned HTTP server and assert navigation
before screenshot. Keep genuine external/browser integration checks explicit
and opt-in. This also addresses the default-suite isolation contract in
`docs/test-isolation/feature-spec.md`.

**Status (P0, fixed):** both tests now navigate to an `httptest` server serving
a static page titled `Marshal fixture` with body `owned browser fixture`, assert
the navigation error before inspecting anything, assert the exact title and the
body text, and decode the screenshot with `image/png`. No public-internet URL
remains in the file, and the short-mode and missing-driver behaviour is
unchanged. Evidence: with the Playwright driver installed both tests ran and
passed; neither took the missing-driver skip.

### Access reload race test assumes both orders grant access

**Reproduced:** `TestRuntimeAccessSerializesWithReloadAndQuiesce` failed four
of five focused runs in the audit environment. At
`internal/app/runtime_test.go:1816`, both goroutine results are discarded and
the test requires the final access grant regardless of order. Config reload
increments the revision (`internal/app/session/interaction.go:170`), and an
approval against a changed revision can correctly return `AccessRefresh`
(`internal/app/session/session.go:1363`). Replace the race-only assertion
with deterministic grant-first and reload-first cases, check returned status
and errors, and verify refresh requires a fresh decision. Do not weaken the
revision check to make the test pass.

**Status (P0, fixed):** the discarded-result race block is replaced by
deterministic `grant before reload` and `reload before grant` subtests that
assert every returned status and error. The reload-first case proves
`AccessRefresh` delivers no decision, keeps the request pending with an
advanced current pair, and yields exactly one grant once the refreshed request
is approved. Evidence: 20/20 focused runs and 10/10 in the final pass, with the
runner and session agreeing on the Act-with-system tuple.

### Prompt budget is exceeded and depends on fixture path length

`internal/app/prompt_budget_test.go:19` limits system prompt bytes to 9,000
and schemas to 15,000 while constructing the runtime with `t.TempDir()`.
One audit run reported 9,014 prompt bytes and 15,322 schema bytes; an earlier
baseline recorded 8,969 prompt bytes. Confirm the intended budget and isolate
or normalize machine-specific path length before comparing runs. Treat the
schema overage as a real budget decision to triage, not an excuse to simply
raise the threshold. Measure what contributes to the bytes.

**Status (P2, fixed with both ceilings unchanged):** the failure had two
independent causes, and only one of them was path length. At the P2 base
commit `10191e9c` the focused fixture measured 8,970 prompt bytes and **15,322
schema bytes** across 23 model-facing tools: the schema was 322 bytes over its
own 15,000-byte ceiling regardless of any workspace path. The prompt's 31-byte
margin was the path-dependent half — `t.TempDir()` varies per run and machine.
`TestDefaultPromptBudget` still starts the real `StartRuntime` with its
temporary working and home directories and still checks the same ceilings,
banned strings and registry contents; only the `WorkingDir` handed to
`agent.BuildSystemPromptWithSystem` is now the fixed `/marshal-budget-fixture`
string, so the prompt measurement excludes incidental temporary-path length
rather than working-directory content. The new
`TestBudgetFixtureWorkingDirIsStable` proves that input reaches the prompt
verbatim and that changing it changes the result, so the fixture measures a
real path instead of stripping it. The schema overage was closed by trimming
three descriptions — `shell.run`'s tool description, its `progress` property
description, and `transcript_read`'s description — while keeping the safety
and workflow instructions those strings carry; `test.run` and every other
schema are untouched. Neither constant moved. Measured after the edits: 8,951
prompt bytes and 14,957 schema bytes.

### Shell integration fixtures need admission-path diagnosis

Three known `internal/app/app_test.go` shell integration failures exercise
direct handler calls with missing approval or command admission. The
fail-closed path in `internal/tools/native/command.go:342` is relevant, but
the correct fixture wiring versus any runtime regression must be established
per test before changing assertions. Exercise the production admission path
with isolated fixtures; do not bypass the policy boundary merely to obtain a
green test.

**Status (P2, fixed — the fail-closed path was correct and stayed correct):**
all three fixtures now dispatch through production admission instead of
stubbing it. A test-only helper, `testCommandProof`, evaluates the exact call
against the fixture's real `policy.Authorizer`, rejects a deny or a missing
capability, approves only a `DecisionConfirm`, and returns the proof context
one launch needs; every attempt takes a fresh proof because a proof is
consumed at commit. `TestSubagentChildRegistryBorrowsConversationService`
configures normal Act access and dispatches through the child runner's
authorizer, asserting first that a proof-less direct call returns
`policy.ErrApprovalRequired` and launches nothing. Its running-count
observation is now deterministic: a test-owned command touches a start marker
and blocks until the test creates a release marker, so the parent-owned count
is read while the command is provably running rather than after a fast `echo`
may have settled, and cleanup releases the command even on failure.
`TestPipelineRegistryCommandsCaptureDispatchWorktree` passes a real
`policy.NewEngine` so the factory's existing child admission wiring is
installed, proves the unapproved call never enters the fake runner, and then
runs exactly one admitted invocation through a proof bound to the handler's
invocation, command, dispatch working directory and workspace root.
`TestSubagentShellOutputDoesNotReachParent` wires
`native.Options.CommandAdmission` to a real authorizer over the child's own
engine, registry and live authority, gives the child an active tool call whose
ID matches the execution origin, and now asserts both halves of its claim: the
child's active call received `marshal-scope-probe` and the parent's blocked
`agent.await` call did not. No admission was weakened and no runtime behavior
changed; the only production edits in P2 were the three description strings
above.

### Process-group cancellation has an environment-sensitive failure

**Reproduced:** `TestRunManagedCancellationKillsTree` failed six of ten runs
with `probe process group ... operation not permitted` in the sandbox and six
of ten again outside the sandbox. The same symptom appeared in the P11
baseline, so the workspace sandbox alone does not explain it. Whether this
reflects macOS, process-group lifetime or a production bug remains unresolved.
Instrument an owned child tree and record whether any descendant survives.
Do not interpret EPERM as success or weaken the cleanup assertion without
proving the process outcome.

**Status (P1b, fixed):** EPERM is a transient group-lifecycle race, not
evidence of a surviving writer. Planning reproduced it twice in 100 attempts
with an isolated parent/grandchild pair after SIGKILL and direct-child reap;
the same group returned ESRCH 50 ms later both times, and `ps` found no group
members at that later observation. A single-child comparison returned ESRCH in
20/20 runs. `waitForGroupGone` now retries EPERM from the signal-0 group probe
until `processTreeKillTimeout` at the existing 5 ms interval: only ESRCH proves
the group gone, a nil probe means still present, any other error returns
immediately, and a persistent EPERM at the deadline is returned wrapping EPERM.
Ownership is retained whenever exit is unproven. `StoreLock.Release` keeps a
failed drain, the OS lock and the process-local semaphore for retry, runs
drains newest-registration-first, removes only the ones that succeeded,
serialises concurrent releases, and runs a drain registered mid-release before
unlocking. A managed tree carries a lifecycle
(starting/live/gone/unknown/not-started): the release drain waits for an
in-flight start, keeps the tree and its platform handle until disappearance is
confirmed, and a leader that exits normally no longer proves its descendants
exited. `Service.withStore` joins a release failure with the operation's result
instead of only logging it.

Evidence: `TestRunManagedCancellationKillsTree` 30/30, the previously failing
`TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock`, and the whole
`internal/snapshot` package pass without the `TMPDIR` workaround. The new
deterministic `TestWaitForGroupGone*` cases and the ownership tests
(`TestStoreLock*`, `TestManagerRetainsUnknownTreeAndLock`,
`TestManagerReleaseWaitsForInFlightTreeStart`,
`TestManagerRetainsTreeWhenLeaderExitsBeforeWriter`,
`TestManagerConcurrentReleaseKeepsUncertainTree`,
`TestManagerRetainsUnknownTreeForEveryLaunchPath`, `TestServiceWithStore*`)
pass under `-race`. Windows job-handle retention is implemented — `ownedTree`
closes the handle only after a confirmed wait — and `GOOS=windows go vet
./internal/snapshot` is clean, but no Windows tests were executed on this host.

### LSP cancellation fixture can cancel before its PID is written

**Observed in the elevated full-suite run and once in ten focused runs:**
`TestStartServerCancelledAfterInit` failed because `server.pid` existed but
was empty (`internal/lsp/manager_test.go:106`). Its stub redirects `echo $$`
to the file (`:57`), while the cancellation goroutine checks only `os.Stat`
(`:74`); the shell can create the file before writing the PID, so cancellation
can kill the stub in that window. Wait for a complete valid PID or use an
explicit readiness handshake before cancellation, and retain the assertion
that the owned child is reaped. The test also treats every signal-0 error as
proof of exit (`:108`); distinguish `ESRCH` from permission or other errors so
an inaccessible live process cannot be reported as reaped. The empty PID does
not itself prove an LSP process leak.

**Status (P0, fixed):** a bounded poll now reads and parses a positive PID and
hands it to the test over a buffered channel before cancelling, so a readiness
failure fails the test instead of parsing a possibly empty file afterwards.
Both cancellation tests prove exit through `syscall.ESRCH` or
`os.ErrProcessDone` only; EPERM and any other signal error fail with context.
Evidence: 30/30 focused runs and the package pass.

### Empty git-operation recording can fail spuriously

`internal/app/snapshot_warning_test.go:480` splits the recorded output after
trimming newlines. When no command was recorded, splitting the empty string
produces one empty element and the test reports a non-read-only command `""`.
Treat an empty recording explicitly and assert the recorder observed the
expected baseline before checking the commands. The same test also passes
with a canonical temporary root, so keep this assertion issue separate from
the snapshot identity mismatch.

**Resolved (P1a):** the log is normalized into zero entries when it is empty and
one entry per non-empty line otherwise, before and after `rt.Close`, and an
empty baseline now fails with the fixture's log path. Fixing the assertion
exposed a second defect: `Rooted.svc()` built each per-root `Service` without the
rooted Git binary, so the recorder never observed capture. That seam now threads
through `snapshot.WithServiceGitBinary`, and the test proves the recorder saw
`Track` before asserting anything about shutdown.

## Tests to improve, consolidate, or remove

### Session-list cache test never opens a database

`internal/acp/lister_test.go:167` creates `.marshal/` but not the database.
`ListSessions` returns early when the database path is missing
(`internal/acp/lister.go:76`); the second call only logs a timing warning and
never asserts handle reuse. Its name and comment claim cache coverage it does
not provide. Replace it with a seeded database and an observable reuse/open
assertion, or remove it only if the existing handle-level tests prove the
same public behavior.

**Status (P0, fixed):** the test now seeds a migrated per-cwd database with
project `sess_alpha`, closes the seeding handle, calls `ListSessions` twice
with the same absolute cwd, asserts the seeded session is returned each time,
and asserts `l.cache[cwd].db` is the same non-nil handle after both calls. The
timing-warning branch is gone. Evidence: the focused run and the package pass.

### Several named regression tests do not assert their claimed outcomes

- `internal/app/config/save_test.go:1075`: the deleted-skills-key test logs
  either outcome instead of enforcing its name or its comment's documented
  stale-config behavior. Decide the desired contract, then assert it or rename
  and remove the non-test; avoid accidentally freezing a known undesirable
  behavior as a requirement.
- `internal/tools/native/attribute_test.go:101`: the test named “empty not
  error” logs nonempty symbols without failing. Decide whether partial parser
  results are allowed and assert that contract, or remove/rename the case.
- `internal/agent/runner_misc_test.go:845`: the approval-activity test logs
  when the activity is correct but never fails when it is wrong; fixed sleeps
  also make its timing sensitive. Await a real pending approval with a bounded
  synchronization point, assert the activity and propagate runner errors.
  **Fixed (P0):** the first sleep is replaced by a bounded wait for both a
  pending approval and `ActivityApproval` while watching the runner result, the
  denial is answered, the result is read from a buffered channel with a
  deadline, and both the observed activity and the final idle activity are
  asserted. The error is no longer logged from the goroutine. Evidence: 20/20
  focused runs and the package pass.
- `internal/index/watcher_test.go:68`: the recovery test performs only one
  failing callback and then cancels, so a watcher that exits after that error
  could pass. Trigger a second change after the first failure and assert the
  second callback. The fixed 60 ms startup sleep at `:20` is a scheduling risk;
  no intermittent failure was established in this audit.
  **Fixed (P0):** `Watcher` gained an unexported, nil-by-default `ready func()`
  instance hook that `Run` calls once the watch set is installed. Tests use it
  as a readiness signal instead of the sleep, write two distinct files, require
  invocation one and then invocation two, and assert the watcher had not exited
  in between. Evidence: 20/20 focused runs and `go test -race ./internal/index`.

### Concurrent-patch test does not exercise a mid-patch race

`internal/tools/native/file_test.go:1484` uses 10/20 ms sleeps, logs a
background writer failure, and intentionally waits for the modification
before invoking `file.write_patch`. It can test detection of a file changed
since reading, but its name claims atomicity under a concurrent modification
during patching. Make the existing pre-write test deterministic and name it
accordingly; add a controlled mid-operation interleaving only if that is a
supported guarantee, with an explicit synchronization seam and asserted
writer result.

### Source-comment assertion is a deletion candidate

`internal/sandbox/sandbox_test.go:369` checks for the words
`Explicit-empty allowlist` in source files and skips if a file cannot be read.
It tests wording, not sandbox behavior. Restricted-backend behavior tests
already exist (`sandbox_test.go:167`, `restricted_env_floor_test.go:39`);
verify container-backend behavioral coverage, then remove the comment test or
replace the missing backend coverage with executable assertions.

### Paste and question tests overstate or duplicate coverage

`internal/app/tui/textfield/paste_test.go:20` calls its table “every TUI
component” but omits plugin routing, and the skills install case calls the
field list directly instead of `Panel.Update`. P8 added stronger focused
panel/update tests. Rename and narrow the table or remove the redundant case
after confirming equivalent coverage; do not rely on its title as an inventory.

`internal/app/tui/question_test.go:388,414` simulates form completion and
claims to cover custom-input visibility without driving real transitions.
P10 added interactive question regressions later in the same file. Compare
their assertions, keep distinct finalization coverage, and remove or rename
only truly redundant simulated cases; do not delete behavior coverage based
solely on test names.

## Verification record

- **P0 execution (`p0-test-reliability` branch, seven labelled commits on
  2026-10-30):** the seven fixes above were applied and verified. Focused
  repeats pass: LSP cancellation 30/30, access/reload 20/20 (plus 10/10 in the
  final pass), watcher 20/20 and race-clean, approval activity 20/20. Each
  affected package (`internal/lsp`, `internal/worktree`,
  `internal/tools/desktop/browser`, `internal/acp`, `internal/index`,
  `internal/agent`) passes in full, as do `internal/app/tui/...`,
  `go vet ./...`, `gofmt -l` over the eight touched files, `git diff --check`
  and the scripted usability journey against a freshly built binary.
- **P1a execution (`p1a-snapshot-identity` branch, four labelled commits on
  2026-10-30):** the canonical-identity, historical-lookup, validated-maintenance
  and recorder fixes above were applied and verified. `go test
  ./internal/snapshot -count=1 -timeout 3m` passes against the default temporary
  root with no `TMPDIR` override, as do the shutdown and startup-maintenance
  `internal/app` tests, `internal/app/tui/...`, `CGO_ENABLED=1 go build
  ./cmd/marshal`, `go vet ./...`, `gofmt -l` over the touched files,
  `git diff --check` and the scripted usability journey against the freshly
  built binary. This was exercised on macOS/darwin only.
- The post-P1a `go test ./... -count=1 -timeout 5m` leaves four named
  `internal/app` failures — the three shell admission-path fixtures and the
  prompt budget — which belong to P2, plus one intermittent
  `TestRunManagedCancellationKillsTree` EPERM in `internal/snapshot` that passes
  when the package is run alone and remains P1b. P1a deliberately neither
  investigated nor weakened that fail-closed process-tree guarantee, so the
  suite is not claimed clean while P1b and P2 are open.
- P1a's recorded limitation: a workspace launched by a canonical path cannot
  discover an arbitrary historical *legacy* alias, because a pre-v2 bare
  repository's hash alone reveals no root provenance. Previously known raw paths
  remain discoverable when that same path is supplied, and v2 manifests whose
  matching roots correspond to neither known hash are left untouched for
  manager-wide discovery.
- The post-fix `go test ./... -count=1 -timeout 5m` still fails, as the roadmap
  expects before P1/P2: 20 named tests remain across `internal/app` (6) and
  `internal/snapshot` (14). Every one maps to an audit finding still open —
  shell admission-path fixtures (3), prompt budget (1), empty git-operation
  recording (1), snapshot maintenance (1), and the snapshot identity/retention
  family (14). The three P0 baseline failures in `internal/worktree`,
  `internal/tools/desktop/browser` and the LSP flake are gone; the audit's
  seventh `internal/app` failure did not reproduce, consistent with its own
  note that the process-group and LSP counts are intermittent.
- `TestStatusLineUntrustedIsWarningColored` passes on the audited commit after
  P12; the earlier literal-color failure is no longer open.
- Focused browse drill-in and Enter regression tests pass.
- A sandboxed `go test ./... -count=1 -timeout 5m` encountered denied Go-cache
  reads and denied loopback binds; those errors are environmental and are not
  evidence of product regressions.
- The elevated `go test ./... -count=1 -timeout 5m` completed with exit 1:
  all `internal/app/tui/...` packages passed; 24 named tests failed across
  `internal/app` (7), `internal/lsp` (1), `internal/snapshot` (14),
  `internal/tools/desktop/browser` (1), and `internal/worktree` (1). The
  process-group and LSP failures are intermittent, so these counts describe
  this run, not a permanent failure count. Full output is in the local
  `/private/tmp/marshal-test-audit-full-unsandboxed.log`.
- `TestStartServerCancelledAfterInit -count=10` failed once with an empty PID.
  `TestRunManagedCancellationKillsTree -count=10` failed six times in an
  elevated run with EPERM; the latter failure is not limited to the sandbox.
- `go list` found four packages without their own Go test files: the
  `internal/agent/agenttest` and `test/usability/actor` support packages,
  `internal/app/tui/glyph`, and `internal/watch/watchctx`. This is an
  inventory, not by itself evidence of missing behavior coverage; importing
  packages can test their behavior.
- **P2 execution (`p2-admission-prompt-budget` branch, five labelled commits on
  2026-10-31, base `10191e9c`):** the three admission-path fixtures and the
  prompt budget were repaired without changing policy, admission, budgets or
  any runtime behavior other than three tool-description strings. Commits:
  `f34880e3` (child registry admission), `cc643a7f` (pipeline dispatch
  admission), `a70b056b` (child-output isolation, positive and negative),
  `1d306fca` (fixed budget working directory plus a stability test),
  `5fd0ba6f` (schema description trims). Host: `Darwin 25.4.0 arm64`,
  `go1.26.1 darwin/arm64`, `CGO_ENABLED=1`. Results:
  `go test ./internal/app -run
  '^(TestSubagentChildRegistryBorrowsConversationService|TestPipelineRegistryCommandsCaptureDispatchWorktree|TestSubagentShellOutputDoesNotReachParent|TestDefaultPromptBudget|TestBudgetFixtureWorkingDirIsStable)$'
  -count=10 -timeout 2m` passes, as does each task's own focused repeat before
  its commit (every one of them `-count=10`, the first two also `-count=1` on
  the untouched base where they failed by name). `go test ./... -count=1
  -timeout 5m` completes with no `FAIL` line and `go vet ./...`,
  `CGO_ENABLED=1 go build ./cmd/marshal` and `git diff --check` are clean. The
  measured budget at `5fd0ba6f` is 8,951 system-prompt bytes (ceiling 9,000) and
  14,957 tool-schema bytes (ceiling 15,000) across 23 model-facing tools — the
  365-byte schema reduction came from descriptions alone.
- One full-suite run during P2 failed `TestContainerRunWithFakeRuntime` in
  `internal/sandbox` with `signal: terminated` after 5.05s. It is unrelated to
  this work: the fixture gives its fake container runtime a 5-second
  per-command `Timeout`, it passes standalone in 1.36s, and it did not recur in
  two subsequent full-suite runs on the same commit. It is recorded here as a
  load-sensitive fixture, not attributed to P2 and not masked.
- P2's platform limitation: every check above ran on macOS/darwin arm64 only.
  Linux and Windows are supported platforms that were **not** exercised on
  this execution host, so no claim is made that they pass; the same suite still
  has to run there before P2 is called clean across supported CI platforms.
