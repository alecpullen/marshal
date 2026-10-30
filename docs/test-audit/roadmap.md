# Test audit remediation roadmap

Date: 2026-10-30. Base: `5468c7823a6cb40e6c90047e778f433ba28686cb`.
Source: [test audit](audit.md). Status: proposed.

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

## P4 — expand real TUI journeys and gate changes in CI

Using the repaired harness and test-owned deterministic providers, add a
small set of meaningful 80×24 real-binary journeys for approval details and
cancellation, multi-question partial skip, one skill/plugin management path,
and paste/resize. Assert both success and refusal or cancellation outcomes;
retain existing focused model tests rather than duplicating every unit case.
Run the normal Go suite and vet on pull requests and main pushes after P1–P2
have made the baseline reliable; do not silently quarantine baseline tests.
Keep the scripted usability job and archive per-run artifacts. Make the CI
definition and platform coverage explicit.

## P5 — remove misleading and redundant tests

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

- Every finding in [the audit](audit.md) has a status, evidence and either a
  fix or an explicit, reviewed disposition.
- Full Go tests and vet pass in the documented supported CI environments;
  repeated flaky tests demonstrate their guarantees rather than relying on
  timing alone. External keychains, services and developer config remain
  outside default-suite dependencies.
- PR CI exercises the full suite, and real-binary scenarios have independent
  current-screen and outcome oracles. Document any terminal, platform or
  manual checks that could not be performed.
