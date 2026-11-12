# Testing and CI audit

## What runs

Pull requests and pushes to `main` run three independent GitHub Actions jobs:

- The full Go suite, without the Go test result cache and with package execution
  serialized, followed by `go vet`. This includes `test/daemon`, bridge and ACP
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

The full suite omits real-provider tests and real keyring access by default;
see `AGENTS.md` for their explicit opt-in requirements. The ordinary suite
also skips usability scenarios when no binary exists, which is why CI has a
separate required-binary job.

## Performance and security

The default Go suite already contains wall-clock performance budgets. In one
shared cloud run with concurrent work, `TestStackBusyFlipBudget`,
`TestStackPatchApplyBudget`, and `TestStackRoundTripBudget` exceeded their
10 ms, 2 ms, and 50 ms median budgets respectively. This is an observed
failure, not proof of a regression: runner load was uncontrolled. They remain
in CI and can fail there; investigate isolated baselines before moving or
changing these tests. A second run also exceeded two of these budgets.

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
