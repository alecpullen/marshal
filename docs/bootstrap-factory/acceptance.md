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

Local result on this branch: the targeted factory integration test passed; `web/bridge` passed in the full Go run; `go vet ./...`, 933 UI tests, the UI build, and `go build ./cmd/webbridge` passed. The UI build emitted Vite's nonblocking warning that some chunks exceed 500 kB after minification (largest generated JS chunk about 1.28 MB). The full Go suite failed in unchanged internal ACP, LSP, pipeline, snapshot, native tools and worktree packages. A narrow run at base commit reproduced all those failing tests except one native tools event test, which passed in that rerun. The overall full-suite result remains **FAIL**. The required full race gate timed out after 10 minutes in unchanged `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` under `web/bridge`; `cmd/webbridge` passed. A focused race run of `TestFactoryEndToEnd` passed (8.745 seconds, no race report). The full race gate is **FAIL**.

The final local fixwave added controller and effects regressions for same-SHA CI reruns, PR closure and external movement during checking/review, exact ref and PR identity readiness, and retargeting immediately before or after the ready mutation. Focused controller/effects tests passed; the local Git/GitHub integration group passed (9.573 seconds); and `go test -race ./web/bridge -run 'TestFactoryEndToEnd' -count=1 -timeout 5m` passed again after the final controller change (17.202 seconds, no race report). These focused results do not change the recorded full-suite or full-race failures above. Live provider, deployed host, Podman, and ARM64 gates remain pending.

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
