# Test audit remediation roadmap

Date: 2026-10-30. Base: `5468c7823a6cb40e6c90047e778f433ba28686cb`.
Source: [test audit](audit.md). Status: P0–P5 implemented on this host; see
each stage for evidence and the [completion criteria](#completion-criteria) for
what remains unobserved.

This roadmap accounts for every finding in the audit. Each stage is planned
against the integrated result of the stage before it; the first executable
plan is [P0 test reliability](plans/2026-10-30-p0-test-reliability-plan.md).
The later stages are sequenced scopes and acceptance criteria, not yet
executor-ready implementation plans. Their designs need the evidence and
interfaces established by earlier work. The user approves each written plan
before execution, following the prior TUI-polish workflow.

## P0 — restore trustworthy test signals

**Status: executed.** Plan
[2026-10-30-p0-test-reliability](plans/2026-10-30-p0-test-reliability-plan.md)
ran as seven labelled commits on `p0-test-reliability`; evidence is recorded
per finding in the [audit](audit.md#verification-record).

Fix the reproduced LSP PID-file race, isolate the Git identity fixture and
standalone browser tests, assert session-list caching, make the access/reload
test accept and prove both legitimate orderings, and repair the watcher and
approval-activity false positives. The executable plan names the exact tasks,
tests and commands. This stage does not weaken policy checks or change the
snapshot store. It establishes a smaller, more interpretable failure baseline.

The remaining full-suite baseline after P1a is four named `internal/app`
failures (three shell admission-path fixtures and the prompt budget), owned by
P2, plus the intermittent process-group EPERM in `internal/snapshot`, owned by
P1b. Those four were re-confirmed at the P1b base commit inside an isolated
worktree. P2 corrected how they are described here: the three shell failures
were not path-dependent. Each fixture called a shell handler directly without
an authorization proof or without wiring command admission at all, so the
fail-closed path rejected a call that no authority had ever admitted; the
fixture wiring was missing, and the runtime behavior was correct. The prompt
failure had two independent causes: a 31-byte margin that a longer temporary
path could erase, and a tool-schema total that was 322 bytes over its own
15,000-byte ceiling regardless of any path.

## P1 — snapshot identity and cancellation safety

P1a — canonical identity, historical lookup and the recorder assertion — is
implemented by the
[2026-10-30 executable plan](plans/2026-10-30-p1a-snapshot-identity-plan.md) and
records its evidence in the [audit](audit.md#verification-record). One canonical
identity now governs capture, lookup, restore and maintenance; the original
spelling is a constrained, validated historical fallback, so previously written
data stays reachable without moving, merging or deleting a store.

P1b is implemented by the
[2026-10-31 executable plan](plans/2026-10-31-p1b-process-tree-safety-plan.md).
The process-group EPERM was a transient group-lifecycle race, not a surviving
writer: the signal-0 probe now retries EPERM to the existing deadline and only
ESRCH proves a group gone. A failed drain and its OS lock, process-local
semaphore and callbacks are retained for retry, a managed tree stays registered
with its platform handle until its disappearance is confirmed, the release
drain waits for an in-flight start, and a service operation now reports a
release failure instead of appearing successful. Repeated cancellation (30/30),
the previously failing ownership integration test and the whole
`internal/snapshot` package pass without the `TMPDIR` workaround, with the new
deterministic probe and ownership tests clean under `-race`. Windows job-handle
retention is implemented and the Windows build vets clean, but no Windows tests
were run on this host; that limitation is recorded in the
[audit](audit.md#process-group-cancellation-has-an-environment-sensitive-failure).
P1 closes with the P2 `internal/app` failures unchanged.

## P2 — close remaining baseline failures

**Status: implemented.** Plan
[2026-10-31-p2-admission-prompt-budget](plans/2026-10-31-p2-admission-prompt-budget-plan.md)
ran as five labelled commits on `p2-admission-prompt-budget` from base
`10191e9c`; evidence is in the [audit](audit.md#verification-record). The three
shell fixtures now dispatch through production admission with a real
authorization proof from the fixture's own authorizer, and the child-output
test asserts both halves of its claim — the child sees the output and the
parent does not. Both budget constants are unchanged: the prompt fixture pins
its working-directory input so a temporary path's length cannot move the
measurement, and three description trims took the schema total from 15,322 to
14,957 bytes while keeping the safety and workflow instructions they carry. On
macOS/darwin arm64 the focused tests pass at `-count=10`, `go test ./...`
completes without a failing package, and `go vet ./...`, the
`CGO_ENABLED=1 go build ./cmd/marshal` and `git diff --check` are clean. Linux
and Windows are supported platforms that were not exercised on the execution
host, so passing there remains a reported CI requirement rather than a claim.
One unrelated, load-sensitive `internal/sandbox` container fixture failure is
recorded in the audit rather than attributed to P2.

For each shell registry integration test, wire the production admission and
approval lifecycle in an isolated fixture and assert the original behavior;
do not bypass fail-closed policy. Resolve prompt-budget failures by normalizing
the path-dependent fixture and measuring per-tool schema contributions. Keep
the existing 9,000/15,000-byte budgets unless a reviewed design note explicitly
justifies a change; avoid silent threshold increases. Require the individual
app tests and full `go test ./...` to pass on supported CI platforms, with
remaining environment-specific failures documented rather than suppressed.

## P3 — make the real-binary usability harness trustworthy

**Status: complete.** P3a (terminal model and viewport snapshots) is
implemented on `p3a-terminal-screen` — see the [audit
finding](audit.md#the-pty-harness-does-not-model-the-current-terminal-screen).
P3b closed the rest as seven labelled commits on `p3b-usability-harness` from
base `e0d14b45`, following the
[2026-11-01 executable plan](plans/2026-11-01-p3b-usability-harness-plan.md);
the evidence is in the [audit](audit.md#verification-record). P4 and P5 are
implemented; see their sections below.

Replace accumulated-output-as-screen with an actual terminal state model;
retain a separately bounded raw diagnostic log. Cover cursor movement, erase,
alternate screen, incremental writes, cell width, resize and stale content.
Add an explicit post-action oracle, and either implement or remove the unused
`Scenario.Success` contract. Prove Help closes and that a deliberately broken
close fails. Give the harness explicit resize, bracketed-paste and bounded
graceful-shutdown actions; isolate home/cache/provider endpoints, report paths
and child environment; make the dedicated integration job fail on a missing
binary. Give the optional LLM repair scenario an independent fixture-test
oracle or relabel it exploratory. Do not build journeys on the current
historical-output parser.

P3a completed the first sentence, the movement/erase/alternate-screen/
incremental-write/cell-width/stale-content coverage, and a real-binary redraw
demonstration in `TestScriptedVisibleScreenRedraw`.

P3b completed the rest:

- **Postconditions.** Scripted steps gained a wait-only postcondition with
  conjunctive positive and negative screen matching; `HelpOpenClose` cannot
  report done until the viewport shows the prompt and no Help title, and a
  deliberately ineffective close is asserted to stay unfinished.
  `Scenario.Success`/`SuccessCriterion` were removed rather than implemented.
- **Harness operations.** `Paste` delivers exact bracketed-paste framing without
  Enter, `Resize` moves the PTY and the viewport model together in `1..65535`,
  and both are scripted actions (`ActionPaste`, `ActionResize`) with errors
  surfaced as scenario failures.
- **Shutdown.** `Session` owns one `cmd.Wait`; `Wait(ctx)` observes the real exit
  without killing the child, and idempotent `Close` performs bounded forced
  cleanup. `TestScriptedGracefulQuit` proves a normal `/quit` exit rather than a
  forced kill.
- **Isolation.** The child environment is constructed rather than inherited —
  home, XDG config/data/cache, Marshal dirs and `TERM` pinned inside a temp home,
  provider keys and `USABILITY_*` withheld — and its provider is a per-test
  `httptest.Server` whose request counter proves the scripted journeys make no
  requests.
- **CI and reports.** `Runner.WriteReport` requires a per-run directory;
  `USABILITY_REPORT_DIR` gives each CI run its own artifact subdirectory, and
  `USABILITY_REQUIRE_BINARY=1` makes the dedicated job fail instead of skipping
  when its binary is missing.
- **LLM scenario.** Relabelled `TestExploratoryLLMSubtractFix`: the actor's
  success claim is observational, no fixture `go test` is run as an oracle, and
  it stays opt-in and outside the scripted CI selection.

P3's remaining limitation is platform coverage: the work was verified on
macOS/darwin arm64 only, and no CI platform run was observed from the execution
host. P4 owns the 80×24 product journeys, the full-suite PR gate and the
remaining platform claims.

## P4 — expand real TUI journeys and gate changes in CI

**Status: implemented.** The journeys and the pull-request gate landed as eight
commits on `p4-real-tui-journeys` from P3b's merge `c6c79913`; the evidence,
including the two harness defects the journeys exposed, is in the
[audit](audit.md#verification-record). Four new real-binary journeys run at
80×24 against a test-owned OpenAI-compatible provider and a test-owned
filesystem: `TestScriptedApprovalDetailsAndDecision` (approval details, then
denial and execution with independent filesystem and provider oracles),
`TestScriptedQuestionsPartialSkip` (a completed answer survives a partial skip,
and a full skip fabricates nothing), `TestScriptedSkillRemoval` (cancel
preserves both skills byte-for-byte; confirm removes only the selected one) and
`TestScriptedPasteResizeDraft` (bracketed paste survives a resize round trip and
Backspace cancellation never submits). They sit alongside P3b's three. P5
remains open: nothing in this phase removed or renamed a test.

Using the repaired harness and test-owned deterministic providers, add a
small set of meaningful 80×24 real-binary journeys for approval details and
cancellation, multi-question partial skip, one skill/plugin management path,
and paste/resize. Assert both success and refusal or cancellation outcomes;
retain existing focused model tests rather than duplicating every unit case.
Run the normal Go suite and vet on pull requests and main pushes after P1–P2
have made the baseline reliable; do not silently quarantine baseline tests.
Keep the scripted usability job and archive per-run artifacts. Make the CI
definition and platform coverage explicit.

### P4 CI and platform scope

Pull requests and pushes to `main` run two independent jobs, both on
`ubuntu-latest`:

- **`full-suite`** (`.github/workflows/go-checks.yml`) runs
  `CGO_ENABLED=1 go test ./... -count=1 -timeout 5m` and then
  `CGO_ENABLED=1 go vet ./...`. It carries no package exclusions and no
  `continue-on-error`, so a failing package or a vet diagnostic fails the pull
  request. `-count=1` stops a cached result standing in for a run against the
  pull request's own code.
- **`scripted`** (`.github/workflows/usability.yml`) builds the binary and runs
  the real-binary `TestScripted*` journeys with `USABILITY_REQUIRE_BINARY=1`,
  uploading its per-run report artifacts.

Release tag pushes keep the checks in `release.yaml`; neither of these workflows
replaces them.

Platform coverage is explicit and deliberately narrow. Both jobs run on Linux,
and the journeys' terminal model was verified on macOS/darwin arm64 from the
execution host. macOS and Windows are supported platforms but are **not**
claimed as pull-request gates, and no hosted run is claimed until one is
observed. The load-sensitive `internal/sandbox` container fixture failure
recorded earlier stays a real failure: if it reproduces, it is diagnosed rather
than quarantined or excluded.

## P5 — remove misleading and redundant tests

**Status: implemented.** Plan
[2026-11-01-p5-test-quality](plans/2026-11-01-p5-test-quality-plan.md) ran as
six labelled test commits on `p5-test-quality` from base
`4f81a41b4d28bcdaa2e2e9d3f9ef24f675cfa39d`; the evidence is in the
[audit](audit.md#verification-record). No production code changed. Every
retained test now asserts its claimed outcome: the config deletion test's
logging-only sibling is removed and its surviving scenario asserts the real
update path, symbol attribution pins partial-tree tolerance and the canceled
parse fallback, the pre-patch test is renamed and made deterministic with an
explicit pre-invocation boundary, the container backend gains an executable
explicit-empty allowlist test in place of the source-comment check, the paste
table is renamed and narrowed with its duplicate case removed, and the two
simulated question transitions are replaced by one real-navigation test.
Behavior coverage was not traded for test count: the distinct finalization,
multi-select, collision and partial-skip tests all stay.

Two limitations are recorded, not fixed. External deletion of a *nondefault*
config value can still be overwritten by a later whole-struct save, which needs
a separate product decision about per-field dirtiness. And the patch path
remains a timestamp recheck followed by a separate write, so the pre-patch test
proves a stale read rather than atomicity.

Resolve the config-deletion and malformed-source tests' logging-only
assertions according to their documented contracts; if the behavior is not a
stable contract, remove or rename the test instead of cementing it. Rename
the pre-write patch test and make its writer deterministic; add a controlled
mid-operation race only if the production contract supports it. Replace the
source-comment allowlist test with a missing container behavioral test, or
delete it if existing tests cover that behavior. Narrow the paste inventory's
claim and remove only duplicate assertions already covered by P8; compare
the simulated question tests to P10 and retain distinct finalization tests.
The acceptance condition is that every retained test has an asserted purpose
and no behavior coverage is lost solely to reduce test count.

## Completion criteria

Met as of P5 on this host, to the extent recorded below:

- Every finding in [the audit](audit.md) has a status, evidence and either a
  fix or an explicit, reviewed disposition. **Met** — each finding now carries
  a status block, including the two P5-recorded limitations that are explicitly
  *not* fixed (nondefault config-value resurrection on a whole-struct save, and
  the non-atomic interval after the patch timestamp recheck).
- Full Go tests and vet pass in the documented supported CI environments;
  repeated flaky tests demonstrate their guarantees rather than relying on
  timing alone. External keychains, services and developer config remain
  outside default-suite dependencies. **Met on macOS/darwin arm64** —
  `go test ./...` (117 packages, no `FAIL`) and `go vet ./...` are clean, the
  P5 tests introduce no sleeps or goroutines, and the suite passed at the P5
  base commit as well. **Not observed** on Linux or Windows, so passing in the
  `ubuntu-latest` CI jobs is a requirement, not a claim.
- PR CI exercises the full suite, and real-binary scenarios have independent
  current-screen and outcome oracles. Document any terminal, platform or
  manual checks that could not be performed. **Defined, not observed** —
  `.github/workflows/go-checks.yml` runs the full suite and vet on pull
  requests with no exclusions, and P3b/P4 give the journeys current-screen and
  outcome oracles; no hosted run of either job was seen from the execution
  host, `actionlint` was unavailable to lint the workflow, and Windows was not
  exercised.
