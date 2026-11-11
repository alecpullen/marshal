# Bootstrap factory acceptance

Status: **local automated acceptance passed; live model and ARM64 OrbStack gates pending**. This record covers the implementation on the factory feature branch. It does not claim a production deployment or a self-development PR.

The disposable integration test `TestFactoryEndToEnd` runs the production controller and journal, real Git commits/pushes against a local bare remote, fake ACP sessions over the bridge transport, and the production GitHub adapter against an `httptest` API. It checks approved plan/spec bytes, a seeded draft PR before implementation, a failing check and reviewer finding, repair, fresh CI/review against the published SHA, one ready PR, two durable rounds, and human merge ownership. `TestFactoryEndToEndInterrupted` injects a lost successful response after each of spawn, seed, publish, create PR, implement, park, review, resume, and set ready. Subsequent controller ticks do not repeat a mutating effect. Focused recovery, ownership, cancelled-turn, ready-race, and journal-failure tests in `web/bridge/factory_*_test.go` cover the remaining failure branches.

## Local reproduction

Use an isolated checkout. The bridge Go integration test requires loopback sockets for its local GitHub server; no account credentials or live model endpoint are used.

```bash
export PATH=/workspace/toolchains/go/bin:$PATH
export GOCACHE=/tmp/factory-go-cache
export CGO_ENABLED=1
go test ./web/bridge -run '^TestFactoryEndToEnd' -count=1
go test ./... -count=1 -timeout 5m
go vet ./...
go test -race ./web/bridge ./cmd/webbridge -count=1
(cd web/ui && npm test && npm run build)
go build ./cmd/webbridge
```

Serializing the package test binaries with `-p=1` was tried and **rejected as the fix**. Locally it passes (and the four budget tests pass in isolation, ten runs each), but on GitHub's `ubuntu-latest` the CI run that already used `-p=1` still failed `TestStackRoundTripBudget` with `round trip: median 50.963111ms over 30 runs, first read 68.705206ms (budget 50ms)`. With no other package test binary running, that is an absolute budget breach on the runner rather than package-level contention, so the workflow keeps its original `go test ./... -count=1 -timeout 5m` form. No ACP budget threshold (2 ms, 10 ms or 50 ms) was loosened, no timing test was skipped or quarantined, and every package and every assertion still runs. Nothing in this branch touches the stack/projector path: `git diff ab935334..HEAD` over `internal/acp`, `internal/viewmodel` and `internal/acpwire` lists no stack, mirror or projector file, so the breach is pre-existing on CI hardware and not introduced here.

## Final merged-head automated acceptance: 2026-11-12

This run integrated `p4-8-refresh` and the factory branch before validating, so the result below is for the head that would merge after `p4-8-refresh`, not the older PR head alone.

- Locally validated head: `5271c9d839bbad1ad7bb6d7941663da8be6b655e`
- Pushed head (PR 57): `15d59a3505cc8723cf4b744e22c4108a149fef9d`
- Integrated branches: `main` `ab935334f59171d0b9d03acab595901023b7c44f`, `p4-8-refresh` `7ca77cf4ffadbd3283114093023a162191607ef5`, factory head `a596bdb8d7f22ad68fdff87af3babf08bf750be7`

The local gates below ran on their own with package execution serialized (`-p=1`); the workflow itself is unchanged and still runs `go test ./... -count=1 -timeout 5m`. The CI result further down is the authoritative gate.

| Command | Result |
|---|---|
| `CGO_ENABLED=1 go build ./cmd/marshal` | pass |
| `CGO_ENABLED=1 go build ./cmd/webbridge` | pass |
| `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 20m` | **exit 0**, every package `ok`, no `FAIL` |
| `CGO_ENABLED=1 go vet ./...` | pass |
| `go test -race ./web/bridge ./cmd/webbridge -count=1 -timeout 10m` | **exit 0**, `web/bridge` 121.9 s, `cmd/webbridge` 1.9 s, no race report |
| `go test ./web/bridge -run '^TestFactoryEndToEnd' -count=1` | pass: `TestFactoryEndToEnd` 2.54 s, `TestFactoryEndToEndInterrupted` 18.53 s (all nine injected-loss subtests) |
| `(cd web/ui && npm test && npm run build)` | 88 files / 933 tests pass; production build succeeds |
| `go test ./test/usability/... -run TestScripted -v -timeout 5m` | pass, 45.1 s |
| `git diff --check` | clean |

The only UI build diagnostic is the already-documented nonblocking chunk-size warning (`index` chunk 1,281.72 kB after minification); `web/ui` is unchanged by this work.

Focused evidence for the three repairs in this run:

| Command | Result |
|---|---|
| `CGO_ENABLED=1 go test ./internal/acp -run '^(TestNoteCallBudget\|TestStackBusyFlipBudget\|TestStackPatchApplyBudget\|TestStackRoundTripBudget)$' -count=10` | pass in 16.6 s, thresholds unchanged |
| `go test ./internal/lsp -run '^TestRescanResyncsOpenDocumentChangedOnDisk$' -count=100` | pass with both original assertions |
| `go test -race ./web/bridge -run '^TestRecipeRunMaxUSDSetsAPausingCapForTheRun$' -count=20 -timeout 5m` | pass in 2.5 s (previously hung to the 90 s deadline) |

### CI on the pushed head: full-suite RED

`scripted` passed (1 m 19 s). `full-suite` failed (7 m 1 s) on `15d59a35`, with two failures, neither of them disguisable and neither of them a threshold change:

| Failing test | Observed | Disposition |
|---|---|---|
| `TestStackRoundTripBudget` (`internal/acp`) | `median 50.963111ms over 30 runs, first read 68.705206ms (budget 50ms)` — the CI run that already used `-p=1` | Pre-existing on CI hardware. The test, `internal/acp/stack*.go`, `internal/viewmodel` and `internal/acpwire` are byte-identical to `main` across this branch (`git diff ab935334..HEAD` lists no stack, mirror or projector file), so this is an absolute budget breach on the runner, not contention and not a regression from this work. Fixing it belongs in `internal/acp` as a performance change to the projector path, or is a runner-capacity limit; it must not be fixed by loosening the 50 ms budget. |
| `TestASecondEmbeddingWaitsAndTakesOver` (`internal/app`) | `embedded_refresh_test.go:142: no runtime took the refresh lease within 2s` | Timing-sensitive lease test introduced by `p4-8-refresh`'s fixups commit `7ca77cf4`. It passes 5/5 locally. Not part of this plan's task scope; recorded as an open flake for `p4-8-refresh` to own. |

Because a required check is red, PR 57 is **not** marked ready in this run, and the plan's Task 5 step 4 / Task 8 step 4 cannot complete on this head. Locally the same architecture and tests are green, which is why the local table above is not the acceptance evidence for the gate.

### Independent review

An independent reviewer reviewed `origin/main...HEAD` against design §§3–7 with no blocker found and four MINOR findings, each recorded here with an explicit disposition rather than a silent waiver:

| Finding | Class | Disposition |
|---|---|---|
| `factory_controller.go:538` + `factory_recovery.go:158`: `Quiesce` journals `shutdown_uncertain` through `factoryDecision`, which sets `needs_input`, so a stopped item returns as `recovering` rather than `stopping` across a restart | MINOR | Accepted as a real deviation from design §7. Not fixed here: it is pre-existing factory behaviour outside this plan's task scope, and `needs_input`/`recovering` both still block dispatch and still accept a `stop` command. Left for a follow-up in the factory branch. |
| `factory_ownership.go:55`: `factoryInternalContext` is defined and never called, so `factoryOwnershipKey` is never set and the bypass guard cannot currently be reached | MINOR | Accepted. Unreachable today, so not a live hole, but it is an unused escape hatch in front of the human-merge guard. Deleting dead code in the factory design is out of scope for this plan; recorded for the factory owner. |
| `forge_github.go:59`: `retryAfterDuration` parses only the seconds form of `Retry-After`, not the HTTP-date form | MINOR | Accepted. A date-form secondary rate limit would park the item in `needs_input` instead of backing off. Out of this plan's scope; recorded for the factory owner. |
| `factory_controller.go`: a `tick` racing `Quiesce` can surface `ErrFactoryConflict`, logged as `"factory scheduler stopped"` on a clean shutdown | MINOR | Accepted as cosmetic. No state is resurrected: the store closes after `<-factoryDone`. Out of scope. |

The reviewer independently confirmed all five Task 1 lifecycle requirements, the three replay/identity/parking priorities, the stdlib-only `web/` boundary, and each of the four fixes as correct and minimal.

Historical note: the earlier PR head failed the full suite on unchanged ACP budget, LSP rescan, pipeline, snapshot, native-tools and worktree packages, and its required race gate timed out in `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`. The LSP rescan fixture no longer depends on filesystem timestamp granularity, and the budget pause no longer applies its cancel synchronously on the caller's goroutine; both are resolved and covered by the passing local suite and the CI `scripted` job. The ACP budget failures were **not** resolved by serialization: `-p=1` was tried, rejected, and reverted, and the one surviving failure is recorded above as a CI-hardware budget breach. The earlier fixwave's focused regressions for same-SHA CI reruns, PR closure and external movement, exact ref/PR identity readiness, retargeting around the ready mutation, and external-closure cleanup remain in place.

Live provider/GitHub execution and the ARM64 OrbStack deployment gates are **not** part of these automated results and remain pending, as recorded below. Nothing here claims live, deployment, Podman or merge readiness.

## Opt-in live acceptance: pending

The required live gate needs an explicitly chosen GitHub repository and small approved feature, selected model/provider routes with credentials, a target branch and expected check identities (check-run name **and App ID**, or commit-status context), a bridge host/state directory, and an agreed native or published workspace. These target values were not provided to this local task. Do not use ambient developer credentials or create a remote branch/PR until they are supplied. Use a dedicated repo with a disposable feature request that intentionally needs one repair and a human question/stop.

1. Start the stable `webbridge` from a separate deployed checkout. Keep that binary and its state directory unchanged while the agent edits another branch. Browse `#factory`, create the item with the approved plan/spec, confirm expected checks, routing, native/workspace selection and five total rounds, then approve. Save the run ID and draft PR URL.
2. Observe the seeded docs commit and the first implementation commit. Arrange a failing CI/reviewer finding within the approved small feature; verify the repair prompt receives both, and save the repaired head SHA, passing check identities and reviewed base/head SHAs. Verify only one PR exists and no merge occurs.
3. Ask a real human question via `needs_input` or stop during a turn. Close and reopen the browser; the same item, decision and history must reappear. Stop/restart the stable bridge with the same `--state-dir` and `--workspace`; confirm replay and reconciliation before any new prompt. If an outcome is unknown, use the recovery procedure in the [webbridge operator guide](../../cmd/webbridge/README.md), recording the decision and any operator attestation.
4. Record the run ID, PR URL, base/seed/implementation/repair SHAs, expected check keys and observed conclusions, review head/base, human stop/question outcome, browser reopen result, restart result, and final PR state below. Omit tokens and provider secrets.

| Field | Live result |
|---|---|
| Run ID / PR URL | Pending |
| Base / seed / first implementation / repair SHA | Pending |
| Expected CI identities and observed results | Pending |
| Review base/head and findings | Pending |
| Human question or stop | Pending |
| Browser reopen / service restart reconciliation | Pending |
| Human merge status | Pending |

## ARM64 deployment gate: pending

OrbStack on the user's ARM64 host was unavailable to this checkout. From the target host, with the configured OrbStack runtime and the same explicit bridge configuration, validate: bridge path and agent mount both resolve the retained workspace; an idle worker is stopped and releases its slot; parked session/checkout survive bridge restart and resume; other running containers remain untouched. Capture `orb list`, the agent container state before/after park, checkout `git rev-parse HEAD`, saved session ID and reopened factory item history. Confirm the target paths using `webbridge --help` and the deployment's `--state-dir` / `--state-volume` settings before the test. Do not infer ARM64 support from the fake transport tests. Podman is a separate pending compatibility run.
