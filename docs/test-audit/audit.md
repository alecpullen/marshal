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

**Status: fixed by P4.** Two jobs run on pull requests and pushes to `main`:
`full-suite` in `.github/workflows/go-checks.yml`
(`CGO_ENABLED=1 go test ./... -count=1 -timeout 5m` and
`CGO_ENABLED=1 go vet ./...`) and P3b's `scripted` journey job in
`.github/workflows/usability.yml`. The full-suite job carries no package
exclusions and no `continue-on-error`, so a failing package or vet diagnostic
fails the pull request. The fix was possible because
P0–P2 removed the known baseline failures rather than hiding them. No hosted
run of either job was observed from the execution host (see the
[verification record](#verification-record)).

### The sole scripted real-binary TUI journey lacks a close assertion

**Status: fixed by P3b.** `HelpOpenClose` now ends with a wait-only
postcondition step, and `Scenario.Success`/`SuccessCriterion` are removed
rather than implemented: success is the actor's own `ActionDone.Success`, so a
scripted scenario proves its outcome through its own last step.

The original defect: `test/usability/scenario/scenarios.go:18` sent `?`, waited
for Help, then sent Escape. `test/usability/actor/scripted/scripted.go:40`
returned success as soon as the last action was emitted: there was no
observation that Escape closed Help. `test/usability/scenario/scenario.go:43`
defined `Success`, but the runner's `ActionDone` branch never evaluated it.
Thus the named `TestScriptedHelpOpenClose` could pass if the final Escape had no
effect. **Evidence:** actor and runner control flow.

What P3b established (`p3b-usability-harness`: `b9a92244`):

- `scripted.Step` gained `WaitOnly` and `WaitFor.ScreenNotContains`, and
  `matchesWaitFor` enforces every populated predicate conjunctively. A
  wait-only step advances only when the *current* viewport satisfies all of
  them; an unmatched one stays on its step and yields a no-op. Validation
  rejects a step with none or several of `Send`/`SendKey`/`WaitOnly`, and a
  wait-only step with no condition, instead of silently choosing one.
- `HelpOpenClose`'s third step requires `ScreenContains: "Type a question"` and
  `ScreenNotContains: "Help"` — the same markers as the P3a redraw test. A close
  that does nothing can therefore never report success.
- `TestScriptedWaitOnlyPostcondition` covers unmatched, matched and completed
  steps; `TestScriptedRejectsInvalidWaitOnly` covers empty and conflicting
  actions; `TestLoadWaitOnlyPostcondition` covers JSON decoding; and
  `TestHelpScriptDoesNotCompleteWithoutClose` drives the real `HelpOpenClose`
  actor through the open step and a deliberately ineffective close, asserting
  repeated no-ops and no done, then a closed viewport and done.
- `TestScriptedHelpOpenClose` remains the real-binary positive check, now under
  a bounded 90-second context so a broken close fails with the runner's
  recorded viewport instead of hanging.

### The PTY harness does not model the current terminal screen

**Status: fixed for P3a** — the harness now keeps a bounded terminal model and
snapshots describe the visible viewport; the items below that P3a does *not*
cover remain open (see the roadmap).

The original defect: `test/usability/harness/harness.go:63` appended every
output chunk to an unbounded buffer, and `Snapshot` returned the entire
history. `test/usability/screen/parse.go:8` stripped a subset of escape
sequences but did not apply cursor movement, erasure, alternate-screen or
redraw commands. Past text could therefore satisfy a later `ScreenContains` or
boolean state predicate after it had disappeared; absence and current-frame
assertions were unreliable. **Evidence:** snapshot and parser implementation.

What P3a established (`p3a-terminal-screen`: `c9515db1`, `a493e645`,
`f899e577`):

- `test/usability/harness/terminal.go` decodes PTY bytes incrementally with
  `github.com/charmbracelet/x/ansi` into a bounded cell viewport. It implements
  cursor addressing and movement, erase display/line, save/restore cursor,
  scrolling, CR/LF/BS/TAB, cell widths for wide and combining graphemes, the
  alternate screen (`?47`/`?1047`/`?1049`), DECAWM autowrap (`?7`) and resize.
  Cell-changing controls it does not model latch a sticky error instead of
  yielding a silently wrong screen; `Snapshot.Err` carries it.
- `Session` feeds each chunk to the model once under `Session.mu`, keeps only
  the last 1 MiB of raw bytes as a diagnostic tail, and returns the model's
  viewport from `Snapshot`. `Output` returns that tail.
- `screen.Parse` propagates the model error and consumes `Content`/`Lines`
  directly; the regex `StripANSI` helper was removed as it had no other
  caller.
- The scenario runner fails a run whose screen cannot be decoded, treats a
  viewport of blank rows as not-yet-rendered, and records a bounded raw tail
  in failure diagnostics without asserting on it.

Verified with:

```sh
CGO_ENABLED=1 go build ./cmd/marshal
go test ./test/usability/... -run 'Test(Scripted|Terminal|Session|Parse|Runner)' -count=1 -v -timeout 5m
go test -race ./test/usability/harness ./test/usability/screen ./test/usability/scenario -count=1 -timeout 2m
go test ./... -count=1 -timeout 5m
go vet ./...
```

Measured result: `TestScriptedVisibleScreenRedraw` opens Help on the real
binary, observes the Help title in the viewport, sends Escape, and then
observes a viewport with the title absent and the prompt present; it fails on
any terminal-model error. `TestSessionSnapshotIsCurrent` shows the same
transition against a helper subprocess (the marker leaves the viewport while
remaining in the raw tail), and `TestVisibleScreenAbsencePredicateNeedsTheClose`
proves the absence predicate cannot pass while Help is still drawn.

Running the real TUI surfaced escape sequences the first model rejected; each
was implemented to its actual (cell-neutral or cell-changing) effect with a
regression fixture rather than silenced: OSC 10/11/12 colour queries, DA1
(`CSI c`) and DA2/XTVERSION queries, DECRQM (`CSI ? … $ p`) and DECRPM
responses, modifyOtherKeys (`CSI > 4 m`) and the kitty keyboard protocol
(`CSI = … u`) are ignored as cell-neutral; DECAWM (`?7l`) is modelled because
it changes where the next cell lands. Run on macOS only.

Still open from this finding (owned by the remaining P3 plans): an explicit
post-action Help oracle and the `Scenario.Success` disposition, harness resize
and bracketed-paste actions, bounded graceful shutdown, fixture isolation and
report paths, CI enforcement that the binary exists, and the optional LLM
oracle. P4 journeys must wait for those.

**P4 addendum.** The 80×24 journeys found three more sequences the model
rejected, all standard and all emitted by the real renderer at that size: RI
(`ESC M`), DECSTBM (`CSI r`) and SU/SD (`CSI Ps S` / `CSI Ps T`). Each is now
modelled to its actual effect — cursor up one line, a scrolling region bounded
by two rows, and a region-scoped scroll by N lines — with
`TestTerminalReverseIndex` and `TestTerminalScrollRegion` pinning them. Two
fixtures had used DECSTBM as their example of a sequence the model rejects
(`harness/terminal_test.go`'s unknown-control test and
`scenario/scenario_test.go`'s decode-error test); both now use IL (`CSI L`),
which stays unmodelled and is still cell-changing. Nothing was silenced: the
model still fails loudly on an unmodelled sequence.

### Core TUI workflows lack real-binary interaction coverage

The only scripted scenario is Help (`test/usability/usability_test.go:112`;
`scenario/scenarios.go:18`), configured at 80 rows. The substantial in-process
model tests added for TUI polish cover actual `Model.Update` and composed
frames, but do not validate the PTY/terminal decoding and child process end to
end for approval and sudo review, questions, plugin/skill management, paste,
80×24 overflow, resize, or cancellation. After repairing the screen oracle,
add a small set of deterministic real-binary journeys with test-owned provider
and filesystem fixtures, including negative outcomes and 80×24. Do not
duplicate every panel unit test or require a real model. P3b added the harness
actions such a journey needs — `Paste` writes the exact `ESC [ 200 ~` … `ESC [
201 ~` framing without Enter, `Resize` moves the PTY and the viewport model
together within `1..65535`, and both are reachable from a scripted step
(`ActionPaste`, `ActionResize`) and exercised by `TestSessionPasteFraming`,
`TestSessionResize`, `TestRunnerPasteAndResize` and the invalid-size path. The
journeys themselves remain P4's scope.

**Status: implemented by P4.** Four new real-binary journeys now drive the
built binary at 80×24 in an isolated home, workspace and provider:
`TestScriptedApprovalDetailsAndDecision` (the approval shows the exact command,
and denial and execution are distinguished by the marker file's bytes and by
the tool-role message the provider received), `TestScriptedQuestionsPartialSkip`
(a completed answer survives a partial skip; a full skip sends no fabricated
answer), `TestScriptedSkillRemoval` (cancel leaves both skills byte-for-byte
intact; confirm removes only the selected one) and
`TestScriptedPasteResizeDraft` (bracketed paste lands inline, survives a
100×30 round trip, and Backspace cancellation never submits). Together with
P3b's three they give seven `TestScripted*` journeys, each selected by the
dedicated CI job. Evidence, including the two harness defects these journeys
exposed, is in the [verification record](#verification-record).

### Optional LLM repair scenario trusts the actor's success claim

**Status: relabelled exploratory by P3b.** No independent oracle was added, and
the finding's "otherwise label the scenario as exploratory, not a correctness
gate" branch was taken deliberately: running the fixture's tests would make an
opt-in model run a correctness gate, which is not what this scenario is for.

The original defect: `test/usability/usability_test.go:139` checked only
`res.Success`. The runner copies that from `actor.ActionDone.Success`
(`scenario/scenario.go:130`), and the LLM actor can report success without the
fixture's tests being run or the missing function being fixed. **Evidence:**
test and runner implementation.

What P3b established (`p3b-usability-harness`: `24bbb400`):

- `TestLLMSubtractFix` is now `TestExploratoryLLMSubtractFix`, with scenario
  name `exploratory_subtract_fix`. Its doc comment and the package docs state
  that the actor's claimed success is observational, that the fixture's `go
  test` is never run as an oracle, and that a pass must not be cited as
  evidence of correctness. The `SubtractFix` scenario comment says the same.
- The explicit `USABILITY_LLM_MODEL` opt-in is preserved, and the test remains
  outside the scripted CI selection (`-run TestScripted` does not start an LLM
  actor); it skips without the opt-in.
- An actor failure is still reported as a failure of the exploratory run.
  Reporting stays isolated per run through Task 6's `ReportDir` mechanism, so a
  concurrent scripted run cannot overwrite its artifacts.

### Usability setup and cleanup limit what the suite can prove

**Status: fixed by P3b** for the setup, cleanup, report-path and CI-gate parts.
The provider question is answered for the scripted journeys: they are proven to
make zero requests, and the endpoint is a test-owned server rather than
`localhost:11434`.

The original defect: the generic suite silently skipped
`TestScriptedHelpOpenClose` when the binary was missing
(`usability_test.go:112`); the dedicated PR workflow did build it, so that
specific job was not silently skipped. A dedicated integration job should fail
if its required binary is absent. `Session.Close` killed the child
(`harness/harness.go:193`), so it could not prove graceful exit, terminal
restoration or shutdown persistence. Reports used the shared fixed directory
`/tmp/marshal-usability` (`usability_test.go:120,151`), risking concurrent
report overwrites. **Evidence:** harness and test implementation.

What P3b established (`p3b-usability-harness`: `431f8ed0`, `86f28641`,
`5447c8ac`, `1b7c0ced`):

- `Session` now owns exactly one `cmd.Wait` goroutine. `Wait(ctx)` observes that
  result without killing the child or closing the PTY, returning the context
  error on cancellation; `Close` is idempotent, kills only a child that has not
  exited, treats `os.ErrProcessDone` as "already exited", closes the descriptor
  before draining the reader, and bounds both the reap and the read with its own
  deadline. Repeated and concurrent `Wait`/`Close` are safe (proven under
  `-race`), and the final snapshot survives teardown.
- `TestScriptedGracefulQuit` proves a real normal exit: it waits for the prompt,
  sends `/quit` and Enter, and requires `Wait` to return nil well before its
  deadline, with `Close` used only as cleanup. The child config pins
  `[postmortem] non_exit = "never"` so the interactive postmortem cannot
  interpose.
- Reports are per run: `Runner.WriteReport` refuses an empty `ReportDir`, and
  `reportDir(t)` allocates a unique directory under `USABILITY_REPORT_DIR`
  (CI) or `t.TempDir()` (local). CI sets `USABILITY_REPORT_DIR` under
  `runner.temp` and uploads that path with `if: always()`.
- `requireBinary(t)` is the single availability gate. With
  `USABILITY_REQUIRE_BINARY=1` — set by the dedicated CI job — a missing,
  irregular or non-executable binary fails and names the resolved path;
  otherwise only the "never built" cases skip. Other stat errors fail in both
  modes. `TestRequireBinarySubprocess` exercises both branches in a subprocess.
- The child environment is built rather than inherited. `HOME`, `USERPROFILE`,
  `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `MARSHAL_CONFIG_DIR` and
  `MARSHAL_DATA_DIR` are pinned inside the test's temp home with exactly one
  effective value each (ambient copies are dropped, not merged), `TERM` is the
  deterministic `xterm-256color`, provider keys and the live-keyring opt-in are
  dropped, and every `USABILITY_*` variable is withheld. PATH and platform
  loader variables still pass through so the child can start.
- The provider is a per-test `httptest.Server` recorded by a request counter;
  the child's `base_url` is its URL plus `/v1`. `TestIsolatedHomeEnv` poisons all
  pinned variables, provider keys and harness knobs, then asserts the overrides
  land in the temp home, no sensitive key survives, and the configured endpoint
  is the fixture. The Help, quit and redraw tests assert zero requests, so
  hermeticity is proven rather than assumed.

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

**Status (P5, fixed) for the two remaining bullets.** Both now fail when the
behavior their name claims breaks.

The deleted-skills-key test in `internal/app/config/save_test.go` is **removed**:
its only final check logged either outcome, and asserting the resurrection its
comment described would have frozen a known limitation as a regression
contract. Its sibling `TestSaveProjectConfigDoesNotResurrectDeletedKeys` keeps
scenario 1 (an absent default `max_tool_iterations` stays absent) and its
scenario 2 now seeds a file that really contains `[skills] max_active = 3`,
asserts the load, sets the running value to 5, then **parses** the saved TOML
and asserts the key still exists with 5 and reloads as 5. The plan's original
scenario — reset the running value to the default and expect the file to hold
the default — does not match the code: `writeSections` emits `[skills]` only
when the merged value differs from `Default()`, so a reset leaves the loaded
on-disk value (3) in place. That was confirmed with a throwaway probe, and the
user ruled for asserting the real update path rather than encoding either
stale outcome.

**Open product decision — recorded, deliberately not fixed by P5.** External
deletion of a *nondefault* config value can still be overwritten by a later
whole-struct save: `SaveProjectConfig` has no per-field dirty tracking, so a
key the user removed from the file is indistinguishable from one they changed
in the UI, and the merged non-default value is written back. Fixing it needs
per-field dirtiness or a reload-before-save, which is a config-persistence
change and outside this stage's non-goals. No test asserts either outcome in
the meantime.

The malformed-source nonassertion in `internal/tools/native/attribute_test.go`
became two tests. `TestSymbolsForEditToleratesPartialSyntaxErrors` appends an
explicitly broken declaration to the valid source and requires the diff's
`Alpha` symbol to come back with `File: "p.go"` and `Kind: "function"`, pinning
`repo.ExtractSymbols`' documented partial-tree tolerance instead of demanding
an empty result. `TestSymbolsForEditCancelledParseReturnsNoSymbols` exercises
the parse-failure fallback: it needs a source large enough for tree-sitter to
reach a cancellation poll (a few hundred functions — a tiny file parses to
completion before the context is ever observed), and it first proves the same
source and diff attribute to `Alpha` with a live context so the empty result
after cancellation cannot pass vacuously.
`TestSymbolsForEditMalformedDiffIsEmptyNotPanic` and the unsupported-language
case are unchanged.

### Concurrent-patch test does not exercise a mid-patch race

`internal/tools/native/file_test.go:1484` uses 10/20 ms sleeps, logs a
background writer failure, and intentionally waits for the modification
before invoking `file.write_patch`. It can test detection of a file changed
since reading, but its name claims atomicity under a concurrent modification
during patching. Make the existing pre-write test deterministic and name it
accordingly; add a controlled mid-operation interleaving only if that is a
supported guarantee, with an explicit synchronization seam and asserted
writer result.

**Status (P5, fixed).** Renamed to `TestWritePatch_RejectsFileChangedSinceRead`
and made deterministic: a real database-backed `filetrack` service and a real
`file.read`, then a synchronous, error-checked external `os.WriteFile` and
`os.Chtimes` to a known time two seconds after `ft.LastReadTime`, with
`os.Stat` proving the resulting mtime is later. The goroutine and both sleeps
are gone, and the tracker lookup resolves the path with
`filepath.EvalSymlinks` because the tool records the canonical path (on macOS
`t.TempDir()` lives behind `/var` → `/private/var`). The test asserts the
`changed on disk` error **and** that the file still holds `v1-modified\n`, so a
rejected patch provably wrote nothing.

The test and its comment state the boundary explicitly: this is a
pre-invocation stale-read check. `internal/tools/native/file.go` performs a
timestamp recheck and then a separate write — not an atomic
compare-and-swap — so a non-atomic interval remains after the last recheck. No
mid-operation interleaving test was added, because that guarantee does not
exist to test and inventing it was a non-goal.

### Source-comment assertion is a deletion candidate

`internal/sandbox/sandbox_test.go:369` checks for the words
`Explicit-empty allowlist` in source files and skips if a file cannot be read.
It tests wording, not sandbox behavior. Restricted-backend behavior tests
already exist (`sandbox_test.go:167`, `restricted_env_floor_test.go:39`);
verify container-backend behavioral coverage, then remove the comment test or
replace the missing backend coverage with executable assertions.

**Status (P5, fixed).**
`TestContainerExplicitEmptyEnvAllowlistPassesNoParentEnv` now covers the
container backend at the argument boundary: with
`Config{EnvAllowlist: []string{}}` and `PATH`, `HOME`, a harmless
`MARSHAL_TEST` and a secret-like variable set in the environment, `buildArgs`
must emit no `-e`/`--env` entry, while the image and the `-lc`-wrapped command
must still be present so a truncated or malformed result cannot pass. It sits
beside `TestContainerNilEnvAllowlistDefaults` (nil → `AllowList` defaults) and
`TestContainerBuildArgs_DropsSecretAndDangerousAllowlistKeys` (nonempty,
filtered), so the three cases stay distinguished. The restricted backend keeps
its own executable test, `TestRestrictedEmptyAllowlistDeniesEnvInversion`,
because its explicit-empty PATH behavior legitimately differs from the
container backend's. `TestExplicitEmptyAllowlistDocumented` is deleted; the
production documentation comments in `restricted.go` and `container.go` are
untouched.

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

**Status (P5, fixed).** The paste table is renamed
`TestPasteReachesSelectedTextInputs`, and its comment now says it samples the
picker, memory, agents, connect, settings and skills filter paths plus the
settings field-edit input — explicitly not a complete inventory and not a
panel-routing contract. The `skills install source input` case is removed
because it called `settings.FieldListUpdate` directly rather than
`skills.Panel.Update`; the `skills panel filter` case and the independent
settings field-edit case stay, and the install source editor keeps real-panel
coverage through `TestEscapeCancelsSkillEditorBeforeLeavingFrame` (which sends
paste through `Panel.Update`) and `TestPluginPasteFollowsFocus`.

`TestQuestionSingleChoiceNavigation` replaces the two simulated question tests
with real form navigation driven through the existing `questionStart`,
`questionForm`, `questionMoveDown` and `questionType` helpers, so every command
the form returns is executed. Its `listed` subtest navigates to `green`, presses
Enter, and requires completion with `green`, the custom field never focused and
the custom value empty; its `custom` subtest navigates to the appended `Other…`
row, presses Enter, requires the custom field focused and the form not
completed, types `  magenta  ` and requires the trimmed answer `magenta`.
`TestQuestionPremadePickSkipsCustomInput` and
`TestQuestionOtherPickRevealsCustomInput` are deleted. The distinct
finalization cases (`TestQuestionFinalizeSingleChoice`,
`TestQuestionFinalizeOtherWithText`,
`TestQuestionFinalizeOtherBlankStaysUnanswered`,
`TestQuestionSubFormMarksDoneWithoutDispatching`) and P10's
`TestQuestionListedOtherDoesNotOpenCustomInput`,
`TestQuestionMultiOtherAndCustomAreDistinct`,
`TestQuestionEscapeKeepsCompletedPrefix` and
`TestQuestionCompletedFormStillFinalizesAll` are retained unchanged.

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
- **P3b execution (`p3b-usability-harness` branch, seven labelled commits on
  2026-11-01, base `e0d14b45`):** the remaining P3 harness work is implemented
  and verified on `Darwin 26.4.1 arm64`, `go1.26.1 darwin/arm64`,
  `CGO_ENABLED=1`. Commits: `b9a92244` (scripted postconditions),
  `431f8ed0` (harness paste and resize), `0a9d2dfb` (scripted paste/resize
  actions), `86f28641` (bounded graceful shutdown), `5447c8ac` (isolated child
  environment and provider), `1b7c0ced` (per-run reports, required binary),
  `24bbb400` (exploratory LLM label). The baseline was captured first: the
  union of every task's verification command passed at `e0d14b45`, so no
  pre-existing failure was inherited.
  Final verification, all clean on this host:
  `CGO_ENABLED=1 go build ./cmd/marshal`;
  `USABILITY_REQUIRE_BINARY=1 go test ./test/usability/... -count=1 -timeout 5m`
  (every usability package `ok`, with the real-binary tests executing rather
  than skipping); `go test -race ./test/usability/harness
  ./test/usability/screen ./test/usability/scenario
  ./test/usability/actor/scripted -count=1 -timeout 3m`;
  `go test ./... -count=1 -timeout 5m` with no `FAIL` line;
  `go vet ./...`; and `git diff --check`. The opt-in
  `TestExploratoryLLMSubtractFix` skipped as expected and was not enabled to
  make the suite pass.
  The real-binary Help scenario was repeated with `-count=3` after the
  deterministic `TERM` change, and the required-binary gate was re-checked by
  hand: with `USABILITY_REQUIRE_BINARY=1` and a missing path it fails naming the
  resolved path, and a temporary `USABILITY_REPORT_DIR` produced one
  `usability-*` subdirectory holding its own JSON, benchmark and Markdown
  artifacts.
  One deviation from the plan is recorded with the user's ruling: pinning
  `TERM=xterm-256color` made the renderer emit ECH (`CSI Ps X`), which the P3a
  terminal model rejected as unsupported, so the harness gained that one CSI
  handler plus `TestTerminalEraseCharacters` instead of weakening the
  deterministic `TERM` to a reduced renderer. `TERM=dumb` would have passed but
  exercised less of the renderer than a real developer terminal.
  Platform limitation: macOS/darwin arm64 only. The GitHub Actions job now
  carries the require-binary and report-directory settings, but no CI platform
  was exercised from this host, so no claim is made that the workflow itself
  passed.
- **P4 execution (`p4-real-tui-journeys` branch, eight commits on 2026-11-01,
  base `c6c79913` — P3b's merge — on `Darwin arm64`, `go1.26.1 darwin/arm64`,
  `CGO_ENABLED=1`):** the planned tasks landed as `2237fc7d` (provider fixture),
  `609a2f96` (approval details and decision), `5314aa22` (questions partial
  skip), `48cd27ca` (skill removal and cancellation), `8523c0b6` (paste and
  resize) and `fa0ce65b` (pull-request full-suite and vet gate), plus two
  commits the journeys forced: `b4d2497e` (terminal model gains RI, DECSTBM and
  SU/SD) and `21384b4b` (the scenario decode-error fixture moves off DECSTBM,
  which the model now implements, onto IL). The baseline was captured first:
  the union of every task's verification command passed at `c6c79913`, so no
  pre-existing failure was inherited.
  Every named journey executed rather than skipped:
  `TestScriptedApprovalDetailsAndDecision` (10.28s),
  `TestScriptedQuestionsPartialSkip` (10.33s), `TestScriptedSkillRemoval`
  (5.99s), `TestScriptedPasteResizeDraft` (4.62s), with P3b's
  `TestScriptedHelpOpenClose` (4.42s), `TestScriptedVisibleScreenRedraw`
  (4.36s) and `TestScriptedGracefulQuit` (4.18s).
  `USABILITY_REQUIRE_BINARY=1 go test ./test/usability/... -run '^TestScripted'
  -count=3 -v -timeout 5m` passed all three iterations against the freshly
  built binary, with reports written to separate per-run directories under a
  fresh `USABILITY_REPORT_DIR=/tmp/marshal-p4-usability-20261101-181028` (four
  `usability-*` run directories). `USABILITY_LLM_MODEL` was left unset, so
  `TestExploratoryLLMSubtractFix` stayed skipped.
  `go test -race ./test/usability/... -count=1 -timeout 5m` passed;
  `CGO_ENABLED=1 go vet ./...`, `gofmt -l` over every changed Go file and
  `git diff --check` were clean; `CGO_ENABLED=1 go test ./... -count=1
  -timeout 5m` passed with exit 0 when run alone.
  Recorded honestly, not quarantined: one full-suite run executed
  **concurrently** with the scripted and `-race` suites exited 1, and its
  failing package scrolled out of the captured tail before it could be named.
  It did not reproduce — a serial run produced no `FAIL` line at all, and a
  second serial run reporting its own exit code gave 0 — so it is treated as
  load-sensitive pending a hosted run, and nothing was excluded or retried into
  passing.
  Two deviations from the plan are recorded, both because the plan's literal
  strings do not match the code and the plan's own non-goals forbid changing
  behaviour to suit a test. First, the approval screen identifies a shell call
  as "Agent wants to run:" plus the command rather than by the literal tool name
  `shell.run` (`DisplayToolName` renders it "Run command" in the transcript), so
  the journey asserts that user-visible identity plus "Reason: confirmed by
  rule", which is what proves the workspace's exact `[tools.shell.confirm]`
  entry fired rather than `rm`'s destructive default. Second, `question.ask`
  results are built by `executeNativeQuestionAsk`
  (`internal/agent/execute.go`) as `- "question": "answer"` lines, and a form
  with every answer skipped is collapsed into one decline sentence that carries
  no pairs; the journey asserts those exact lines in order and, for the full
  skip, the decline message plus the absence of any answer line, which is a
  stronger statement than three `Unanswered` pairs.
  Platform and CI limitations: exercised on macOS/darwin arm64 only. The two
  pull-request jobs are defined for `ubuntu-latest` — `full-suite` in
  `.github/workflows/go-checks.yml` and P3b's `scripted` job — but no hosted run
  was observed from this host, no `actionlint` was available to lint the new
  workflow, and Windows was not exercised. None of these is claimed as passing.
- **P5 execution (`p5-test-quality` branch, six labelled test commits plus the
  record's own commit, on 2026-11-01,
  base `4f81a41b4d28bcdaa2e2e9d3f9ef24f675cfa39d`, on `Darwin 25.4.0 arm64`,
  `go1.26.1 darwin/arm64`, `CGO_ENABLED=1`):** no production code was changed.
  Commits: `c7b11a8c` (config deleted-key test), `4d83a394` (deterministic
  pre-patch test), `42224e3e` (container explicit-empty allowlist), `b8c63f25`
  (symbol attribution and cancelled parse), `f71bd58e` (paste table scope),
  `b39c9ccc` (real question navigation).
  The baseline was captured first: `CGO_ENABLED=1 go test ./... -count=1
  -timeout 15m` exited 0 at the base commit with no failing package, so no
  pre-existing failure was inherited and no failure in the final run can be
  explained away as baseline.
  Each task passed its own focused repeats before its commit:
  `go test ./internal/app/config -run
  '^TestSaveProjectConfigDoesNotResurrectDeletedKeys$' -count=3` and the whole
  package; `go test ./internal/tools/native -run '^TestSymbolsForEdit' -count=3`
  (all eight named tests execute) plus
  `go test ./internal/repo -run '^TestExtractSymbolsToleratesSyntaxError$'`;
  `go test ./internal/tools/native -run
  '^TestWritePatch_(RejectsFileChangedSinceRead|ChangedOnDiskErrorIncludesCurrentContent)$'
  -count=20` and the same test `-count=3` under `-race`; the four named
  `internal/sandbox` tests `-count=3` plus the whole package; the three named
  paste/panel tests `-count=3 -v`; and the nine named question tests
  (the verify pattern's nine alternatives, `CompletedFormStillFinalizesAll`
  among them) `-count=3 -v` plus `go test ./internal/app/tui -count=1`.
  Final gates, all clean on this host: `CGO_ENABLED=1 go build ./cmd/marshal`;
  `USABILITY_REQUIRE_BINARY=1 go test ./test/usability/... -run '^TestScripted'
  -count=1 -v -timeout 5m`, where all seven real-binary journeys executed rather
  than skipped (`TestScriptedApprovalDetailsAndDecision` 12.08s,
  `TestScriptedQuestionsPartialSkip` 10.33s, `TestScriptedSkillRemoval` 6.04s,
  `TestScriptedPasteResizeDraft` 4.67s, `TestScriptedHelpOpenClose` 4.47s,
  `TestScriptedVisibleScreenRedraw` 4.41s, `TestScriptedGracefulQuit` 4.18s);
  `go test ./internal/app/config ./internal/tools/native ./internal/sandbox
  ./internal/app/tui/... -count=1 -timeout 5m`; `CGO_ENABLED=1 go test ./...
  -count=1 -timeout 5m` with exit 0, 117 `ok` packages and no `FAIL` line;
  `CGO_ENABLED=1 go vet ./...`; and `git diff --check`.
  Three deviations are recorded rather than smoothed over. First, the plan's
  Task 1 scenario 2 cannot pass as written (see the config finding above): a
  reset to the default leaves the on-disk value untouched because the section
  is only written when it differs from `Default()`. A probe confirmed it, and
  the user ruled for asserting the real update path with a non-default value.
  Second, the plan's Task 2 canceled-context fixture does not reach a parser
  error with a tiny file — tree-sitter polls the context only at parse-progress
  boundaries — so the test uses a source large enough to reach one, with a
  live-context control call proving the same fixture still attributes to
  `Alpha`; no package-global parser hook was added. Third, the Task 3 tracker
  lookup resolves the path with `filepath.EvalSymlinks`, because the tool
  records the canonical path and macOS `t.TempDir()` sits behind
  `/var` → `/private/var`.
  Recorded limitation, not fixed: external deletion of a nondefault config value
  can still be overwritten by a later whole-struct save and needs a separate
  product decision; the patch test proves a pre-invocation stale read, not
  atomicity, and a non-atomic interval remains after the last timestamp check.
  The opt-in `TestExploratoryLLMSubtractFix` stayed skipped and was not enabled
  to make anything pass.
  Platform limitations: every check above ran on macOS/darwin arm64 only. The
  `full-suite` and `scripted` jobs are defined for `ubuntu-latest` but **no
  hosted run was observed from this host**; Linux and Windows are supported
  platforms that were not exercised, so passing there remains a CI requirement
  rather than a claim. No unrelated failing or load-sensitive fixture was
  observed in the final runs.
