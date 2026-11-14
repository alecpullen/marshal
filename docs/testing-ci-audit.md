# Testing and CI audit

## What runs

Pull requests and pushes to `main` run three independent GitHub Actions jobs:

- The Go suite, without the Go test result cache and with package execution
  serialized, followed by `go vet`. CI excludes the 12 named wall-clock latency
  budget tests listed below. It includes `test/daemon`, bridge and ACP
  integration tests, architectural boundaries, and security-related behavior.
- Scripted usability scenarios against a freshly built Marshal binary and a
  test-owned provider. A missing binary is an error; reports are uploaded even
  when a test fails.
- The web UI's locked dependencies, Vitest suite and production build. The
  generated assets must match the committed files embedded by the bridge.

A version tag calls the same checks before GoReleaser creates a **draft**
release. Publishing the draft remains a manual step; container publishing is
not configured. CI does not configure GitHub branch protection: require the
three check jobs on `main` in repository settings if merges must be gated.

## Suite size and local feedback

The checkout contains 1,086 Go `*_test.go` files, approximately 9,700
named top-level tests and benchmarks, and 88 web test files. A run in the
cloud environment passed 933 UI tests in 134 seconds, including 56 seconds of
jsdom environment setup; the Go `internal/app/tui` package took 53.7 seconds
in the same uncached, serialized run. A subsequent run measured 86.6 seconds
for `web/bridge`, and 49.5 seconds for `internal/app/tui`. These measurements were made with other
work running and are not laptop benchmarks. These counts are
not a measure of redundant coverage. Many Go package tests exercise multiple
components, HTTP, subprocesses, persistence, or security boundaries; they cannot
safely be classified as unit tests from their paths or names. The existing
`integration` build tag includes tests requiring live providers and is not a
synonym for the deterministic integration coverage in the default suite.

Use the smallest relevant package during development, then the full suite
before a broad change or release. Go caches successful package results when
inputs are unchanged; `-count=1`, as used in CI, intentionally disables that
cache. A practical system-oriented local pass is:

```sh
go test ./test/daemon/...
CGO_ENABLED=1 go build ./cmd/marshal
USABILITY_REQUIRE_BINARY=1 go test ./test/usability/... -run TestScripted -timeout 5m
(cd web/ui && npm ci && npm test)
```

These commands are a fast feedback option, **not** a replacement for all
package tests: for example, policy, sandbox, database migrations, and wire
contracts also have important checks outside those directories. Do not delete
unit tests by category alone. First collect package and individual test timing
on the target laptop, identify duplicated assertions and slow sleeps/fixtures,
and remove or consolidate tests only after checking the behavior they protect.
For a cold and warm comparison, run `time go test ./...` twice; for uncached
package timings use `go test ./... -count=1 -json > test-results.json` and
inspect package `Elapsed` values. Do not compare parallel package elapsed
values by summing them as wall-clock time. Results depend on hardware, caches,
services and the Go version.

In the full Go run, `TestExitAnalysisAutomaticallyGrantsAndRestoresSystemAccess`
panicked in `act/success` at `internal/app/interaction_test.go:327`; the same
test passed alone and the full app package passed with a private temporary
directory and umask 022. Its cause and order dependence remain unconfirmed and need investigation before the suite can be treated as green.
The same run inherited umask 077: daemon service-file and pipeline mode
assertions expected 0644 and 0755 respectively and failed; both targeted tests
passed with a private temporary directory and umask 022. The scripted approval
deny scenario also failed when its fixture used shared `/tmp`; the approval
scenario passed when rerun with a private temporary directory and umask 022.
The two changes were made together, so the cause is not isolated.
A later full run also saw three `TestStartRuntime...` database-open failures;
all three passed when rerun together in isolation. Their cause remains unknown.
Four snapshot process-lifecycle tests also failed when the container's PID 1
left killed descendants as zombies. All four passed under a child-subreaper,
which reaped those descendants. The production fail-closed behavior is intact.

The TUI frame-invariant sweep renders every state across 24 size/theme
combinations. Its `-short` mode now keeps every state and both themes but uses
two widths at one height (four combinations); the default and CI retain all
24. In this cloud environment, isolated uncached runs took 23.8 seconds for
the full sweep and 1.2 seconds for the short sweep, with other work running.
Use `go test -short ./internal/app/tui` for quicker TUI feedback. Short mode
also skips other tests, including performance budgets; it is not a replacement
for the full suite.

Only 24 Go test locations check `testing.Short()`, including some tagged live
tests, so `-short` is not a comprehensive fast-suite switch. The serialized
CI command (`-p=1`) trades speed for reduced concurrency; do not copy it as
the default local command without measuring on the laptop.

The first GitHub run exposed a race in `TestASecondEmbeddingWaitsAndTakesOver`:
its first runtime retried at the production 30-second interval after a probe
won the lease, but the test waited only two seconds. Both runtimes now retry
within the test deadline; the corrected test passed 20 consecutive runs.

The full suite omits real-provider tests and real keyring access by default;
see `AGENTS.md` for their explicit opt-in requirements. The ordinary suite
also skips usability scenarios when no binary exists, which is why CI has a
separate required-binary job.

## Laptop bridge investigation (2026-11-14)

After merging main at `fcebaa8b` into this branch, an uncached
`go test ./web/bridge -count=1 -json -timeout 10m` on darwin/arm64 with
Go 1.26.1 took 116.7 seconds. It reported 1,151 top-level test outcomes:
1,146 passed and five socket tests failed because the default macOS temporary
directory plus `t.TempDir`'s test-name component exceeded the Unix socket path
limit. The socket fixture now uses a short, unique `MkdirTemp` directory with
cleanup. The directory-creation test still starts with its nested directory
absent, and permission and peer-identity assertions are unchanged. The socket
subset passed three repeats with the original temporary-directory environment.

The slowest top-level groups in that baseline were:

| Test | Seconds | Investigation |
| --- | ---: | --- |
| `TestFactoryEndToEndInterrupted` | 17.03 | Fifteen independent real-Git interruption scenarios, originally serialized. |
| `TestChildStopDuringRestartReturns` | 2.47 | Intentionally widens the restart window to two seconds to exercise a stop/respawn race. |
| `TestFactoryEndToEnd` | 2.37 | Real Git delivery, repair, review and journal replay. |
| `TestScheduleHistoryWritesDoNotUndoAConcurrentEdit` | 1.78 | Thirty HTTP updates competing with background persisted history writes. |
| `TestFleetSpawnsChildLazilyPerProject` | 1.60 | Multiple real helper-process lifecycles. |

A separate runtime trace of the interruption group took 16.9 seconds;
13.5 seconds of its recorded syscall delay was process waiting, and
10.9 seconds passed through the production Git runner. Fixture repository
creation accounted for only about 1.2 seconds of recorded syscall delay.
These are cumulative trace attribution values, not additive wall-clock phases.
Replacing the Git layer with a fake or deleting recovery cases would weaken
the behavior being checked; optimizing seed setup alone has limited upside.

Each interruption scenario owns its repository, HTTP server, journal, fleet
and controller, so those subtests now use `t.Parallel`. Three focused runs
took 4.25, 4.50 and 4.45 seconds from the parent test's `run` event to its
`pass` event, roughly 74% less elapsed time than the serial trace run. All 15
scenarios and their assertions remain. Go's `-parallel` flag controls their
concurrency; package serialization with `-p=1` does not serialize subtests.
The parent's reported `Elapsed` field excludes time waiting for parallel
children and can be zero: use event timestamps or focused package elapsed
time when comparing this group. These laptop results do not predict the
speedup on a smaller shared runner.

The first race-detector attempt with Go 1.26.1 stalled in a forked child at
full CPU and was stopped. This is consistent with the documented
[Darwin race-runtime fork bug](https://github.com/golang/go/issues/79804),
whose [Go 1.26 backport](https://github.com/golang/go/issues/79806) targets
1.26.5. Retrying with the already installed Go 1.27.0 exposed two real
races: the lost-response HTTP fixture read its handler's draft/mutation
state without synchronization, and `Registry.lookup` exposed a live session
entry after releasing its mutex while the fleet inbox read `Busy`.
The fixture now snapshots its HTTP state under its mutex; registry lookups
copy entries under the registry mutex. A deterministic regression test failed
before the registry fix because beginning a turn changed a previously returned
entry. Both factory groups and that regression passed three race-detector
repeats after the fixes (28.8 seconds total):

```sh
GOTOOLCHAIN=go1.27.0 CGO_ENABLED=1 go test -race ./web/bridge \
  -run '^Test(FactoryEndToEnd|FactoryEndToEndInterrupted|RegistryLookupKeepsSessionStateSnapshot)$' \
  -count=3 -timeout 3m
```

This uses a newer toolchain for verification only; the module's Go requirement
and CI configuration are unchanged.

The subsequent full-suite timing pass identified
`TestEscapeCancelsSkillEditorBeforeLeavingFrame` as another avoidable delay:
10.62 seconds, almost all of the skills panel package's 11.25 seconds.
Its input helpers executed the cursor's 530 ms blink command synchronously
after each of 19 characters and one paste. Typing and paste already update the
text synchronously, so these helpers now deliver those same events without
waiting for cursor ticks. Action commands still execute through `press`, and
the edit, paste, cancellation, committed-value, picker and navigation
assertions are unchanged. All three repeats reported the test below the
JSON log's 0.01-second elapsed precision; the entire skills package passed
three repeats in 0.86 seconds:

```sh
CGO_ENABLED=1 go test ./internal/app/tui/skills -count=3 -json -timeout 2m
```

The full Go 1.26.1 pass used a freshly built binary, required the scripted
usability tests to run, and used a private short temporary directory. It ran
`go test -p=1 ./... -count=1 -json -timeout 20m` without the CI latency-test
exclusions: 123 test packages passed, six packages had no tests, and 9,825
top-level tests passed with ten skips. Its first-to-last JSON event interval
was 478.3 seconds, excluding the preceding binary build. The skills-helper
change was made after that package had completed and was verified separately
by the three full-package repeats above.

The largest package times in that pass were:

| Package | Seconds |
| --- | ---: |
| `web/bridge` | 104.35 |
| `internal/snapshot` | 83.31 |
| `test/usability` | 47.89 |
| `internal/app/tui` | 32.46 |
| `internal/acp` | 22.09 |
| `internal/tools/native` | 18.31 |
| `test/usability/scenario` | 16.86 |

The bridge interruption group took 4.60 seconds including its parallel
children in this full pass. Compared with the initial bridge baseline, the
package saved about 12 seconds, with the socket failures repaired rather
than skipped. This is a single-run comparison, not a regression threshold.
The remaining snapshot costs are spread across real Git and persistence
fixtures: its slowest cases were multiprocess workspaces under tiny budgets
(8.30 seconds), creating 100,001 files to exercise the production entry bound
(7.60 seconds), and crash recovery preserving every published hash
(7.23 seconds). Those are explicit safety scenarios; further work should
profile their common fixtures before deciding on scheduling or coverage
changes. The large capture-bound test already skips under `-short`.

Final checks also passed `go vet ./...` and three interruption-group repeats
with `-parallel=2` (9.44–10.32 seconds each). The smaller concurrency limit
preserves all scenarios and models a shared runner more closely, but it is
still a laptop measurement rather than a hosted result.

## Performance and security

The default Go suite already contains wall-clock performance budgets. In one
shared cloud run with concurrent work, `TestStackBusyFlipBudget`,
`TestStackPatchApplyBudget`, and `TestStackRoundTripBudget` exceeded their
10 ms, 2 ms, and 50 ms median budgets respectively. This is an observed
failure, not proof of a regression: runner load was uncontrolled. On the first
GitHub Actions run all three also failed (10.73 ms, 2.57 ms and 69.09 ms).
The 12 named latency-budget tests are excluded from CI and remain available
on the developer laptop. Run them without `-short` and with other intensive
work stopped:

```sh
CGO_ENABLED=1 go test -p=1 ./internal/acp ./internal/app/tui -count=1 -timeout 20m \
  -run '^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget|AKeyPressOverThePipeBudget)$'
```

Results vary with the laptop, power mode and load. These tests retain their
existing absolute budgets; a failure warrants investigation and comparison
with a repeatable local baseline. The default `go test ./...` still runs them.

Existing ACP benchmarks (`go test ./internal/acp -run '^$' -bench .`) can
track trends on a controlled machine. Shared runners vary in load and CPU, so
absolute latency thresholds there would be noisy rather than deterministic.
Before making performance a merge gate, establish a representative workload,
a dedicated runner, repeated baselines, and an agreed regression budget.

Deterministic security behavior belongs in the ordinary suite: retain tests for
permission rules, sandbox boundaries, authentication, and secret handling.
`go vet` adds static checks but is not a security audit. Dependency advisory
scans such as `govulncheck` or `npm audit` change as vulnerability databases
change; run them separately on a schedule and triage findings, rather than
claiming they are reproducible offline or silently making them release gates.
A future security job should pin scanner versions, define severity and
exception policy, and consider private dependency and fork permissions.
