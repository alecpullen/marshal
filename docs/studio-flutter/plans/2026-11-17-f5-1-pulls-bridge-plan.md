# F5 plan 1: pull requests and CI in the bridge — implementation plan

Date: 2026-11-17. Status: ready for review.

Base: `main` at `11957d83`, which contains F0. Go only; it does not depend on
the Flutter plans and may run beside F2–F4. Followed by
[F5 plan 2](2026-11-17-f5-2-pulls-app-plan.md).

Approved design: [f5-pull-requests-and-ci.md](../f5-pull-requests-and-ci.md),
decisions 1–9.

**How this plan is written.** No implementation code; Contracts holds
declarations only. Everything here is under `web/`, which is standard
library only (`TestWebIsStdlibOnly`).

## Goal

- The bridge records every pull request it opens, observes its checks
  against the current head, and serves one index of them.
- A failing check can be dispatched to a worker, through the factory's
  repair for a factory item and through the CI fixer otherwise.

## Non-goals

- Any UI. Merging by rule. Pull requests Marshal did not open.

## Assumptions

- `(*Fleet).Exit` in `web/bridge/exit.go` opens an agent's pull request
  through `createPR`.
- The factory records its pull requests in `FactoryItem.PRs` and its
  observed CI in `FactoryCI`; the controller mutates items through
  `(*FactoryController).internalUpdate` and adds rounds with
  `FactoryGrant`.
- `web/bridge/cifixer.go` has `fixCI`, `startCIFix`, `finishCI`,
  `forbiddenChange` and `CIHistoryEntry`; its settings are
  `CIFixerSettings` in `automation.go`.
- `Forge` has `ListFailedChecks` and `CheckLog` and no call listing every
  check; `forge_github.go` and `forge_gitea.go` implement it.
- Fleet state is versioned by `workspaceVersion` (11) in `workspace.go`;
  audit entries are written with `(*Fleet).auditf`; fleet deltas are
  declared in `web/bridge/contract/stream.go`.

## Decisions this plan makes beyond the spec

- A record's id is `<repoId>#<number>`.
- The observer polls a record every 30 s while any check is pending and
  every 5 min otherwise, and never polls a record whose origin is a factory
  item.
- A user-requested fix uses the project's `CIFixerSettings` limits when
  set, else 30 minutes and 2 USD.
- Investigate-only runs the fix recipe with file writes refused by policy.
- "Send back" is out of this plan; F5 plan 2 steers the thread's own agent.

## Execution

- Branch `f5-1-pulls-bridge`. Commit each task as
  `f5-1-pulls-bridge: task N — <title>`, explicit paths only.
- `gofmt -w` every Go file touched. Regenerate both clients after contract
  changes.

## Contracts

```go
// web/bridge/contract/pulls.go
type PullOrigin string // "agent" | "factory" | "automation"
type PullState string  // "ready" | "failing" | "in_progress" | "being_fixed" | "checks_unavailable"
type CheckState string // "passed" | "failed" | "running" | "pending"

type PullCheck struct {
	Name    string     `json:"name"`
	State   CheckState `json:"state"`
	URL     string     `json:"url,omitempty"`
	LogTail string     `json:"logTail,omitempty"` // failed checks, detail route only
}

type PullRecord struct {
	ID          string      `json:"id"`
	RepoID      string      `json:"repoId"`
	ProjectRoot string      `json:"projectRoot"`
	Number      int         `json:"number"`
	URL         string      `json:"url"`
	Title       string      `json:"title"`
	HeadRef     string      `json:"headRef"`
	BaseRef     string      `json:"baseRef"`
	HeadSHA     string      `json:"headSha"`
	Draft       bool        `json:"draft"`
	Additions   int         `json:"additions"`
	Deletions   int         `json:"deletions"`
	Origin      PullOrigin  `json:"origin"`
	AgentID     string      `json:"agentId,omitempty"`
	FactoryItem string      `json:"factoryItemId,omitempty"`
	Automation  string      `json:"automation,omitempty"`
	State       PullState   `json:"state"`
	Checks      []PullCheck `json:"checks"`
	Reviews     string      `json:"reviews"`
	Unavailable string      `json:"checksUnavailable,omitempty"` // why
	Report      string      `json:"report,omitempty"`            // detail route only
	Fix         *PullFix    `json:"fix,omitempty"`
	ObservedAt  int64       `json:"observedAt"`
	UpdatedAt   int64       `json:"updatedAt"`
}

type PullFix struct {
	ID        string `json:"id"`
	Check     string `json:"check"`
	HeadSHA   string `json:"headSha"`
	Mode      string `json:"mode"` // "fix" | "investigate"
	AgentID   string `json:"agentId,omitempty"`
	StartedAt int64  `json:"startedAt"`
}

type PullFixRequest struct {
	Check   string          `json:"check"`
	Mode    string          `json:"mode,omitempty"`
	Note    string          `json:"note,omitempty"`
	Routing json.RawMessage `json:"routing,omitempty"`
}

type PullDelta struct {
	Pull    *PullRecord `json:"pull,omitempty"`
	Removed string      `json:"removed,omitempty"`
}
```

A new `DeltaKind` value `"pull"` and a new `InboxItemKind` value `"pull"`
(a fix that could not finish). Routes: `GET /api/pulls`,
`GET /api/pulls/{id}`, `POST /api/pulls/{id}/fix`,
`POST /api/pulls/{id}/fix/stop`, and `autoFix bool` on the project's
automation settings beside `CIFixerSettings`.

```go
// web/bridge/forge.go
// ListChecks returns every check on ref with its state.
ListChecks(ctx context.Context, repo Repo, ref string, cred Credential) ([]PullCheck, error)
// ErrChecksUnavailable is returned when the credential cannot call the API.
var ErrChecksUnavailable = errors.New("checks unavailable")
```

## Task 1 — contract types and the record store

**Files:** `web/bridge/contract/pulls.go`, `stream.go`, `inbox.go`,
`web/bridge/pulls.go` (new), `pulls_test.go` (new), `workspace.go`,
`test/contract/pulls.json`, both generated files.

1. Declare the Contracts types and the two new enum values.
2. `pulls.go` holds the store on `Fleet`: `recordPull`, `pull`, `pulls`,
   `updatePull`, `removePull`, persisted in fleet state.
   `workspaceVersion` becomes 12; a version 11 file loads with no records.
3. Tests: `TestPullRecordRoundTripsThroughFleetState`,
   `TestVersion11StateLoadsWithNoPulls`, `TestRecordPullIsIdempotentByID`.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge ./web/bridge/contract/...
-run 'Pull|Version11|Fixtures|Current|StdlibOnly' -count=1`.

## Task 2 — writers and backfill

**Files:** `web/bridge/exit.go`, `factory_controller.go`, `cifixer.go`,
the review bot's file that opens a pull request, `pulls.go`,
`pulls_test.go`.

1. After `createPR` succeeds in `Exit`, call `recordPull` with origin
   agent. Where the factory first stores a `FactoryPR`, record with origin
   factory and the item id. Where `finishCI` opens a pull request, record
   with origin automation `ci-fixer`; likewise the review bot.
2. `backfillPulls` runs once at start: for each agent with an exit pull
   request URL and each factory item with open `PRs`, record what is
   missing.
3. Tests: `TestExitRecordsThePull`, `TestFactoryRecordsThePullOnce`,
   `TestCIFixerRecordsThePull`, `TestBackfillAddsOnlyMissingRecords`.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -run 'Pull|Backfill|Exit'
-count=1`.

## Task 3 — `ListChecks`

**Files:** `web/bridge/forge.go`, `forge_github.go`, `forge_gitea.go`,
their tests and recorded responses.

1. Implement per forge from the same endpoints `ListFailedChecks` uses,
   without the failure filter, mapping to `CheckState`.
2. A credential of kind `ssh` or `none` returns `ErrChecksUnavailable`.
3. Tests per forge: `…ListChecksMapsEveryState`,
   `…ListChecksWithSSHCredentialIsUnavailable`.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -run 'ListChecks' -count=1`.

## Task 4 — observer, derived state, index and delta

**Files:** `web/bridge/pulls_observe.go` (new), `pulls_http.go` (new),
`http.go`, `fleetevents.go`, tests.

1. `observePulls` polls per Decisions through `GetPR` and `ListChecks`. A
   moved head replaces `HeadSHA` and clears `Checks` before the new ones
   are stored. A closed or merged pull request removes the record.
2. A factory item's record copies the item's `FactoryCI` and `FactoryPR`
   whenever the controller updates them; it is never polled.
3. `derivePullState`: being_fixed when `Fix` is set; checks_unavailable
   when `Unavailable` is set; failing when any check failed; ready when all
   passed and it is not a draft, or the factory item is ready; otherwise
   in_progress.
4. Serve the two GET routes; the detail route adds failed checks' log tails
   through `CheckLog` and the factory item's narrated report. Every change
   emits a `pull` delta.
5. Tests: `TestMovedHeadClearsChecks`, `TestFactoryPullIsNotPolled`,
   `TestClosedPullIsRemoved`, one `TestDerivePullState…` per state,
   `TestPullsIndexListsOpenRecords`, `TestPullDetailCarriesLogTail`,
   `TestPullChangeEmitsDelta`.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -run 'Pull|MovedHead'
-count=1`.

## Task 5 — fix dispatch

**Files:** `web/bridge/pulls_fix.go` (new), `pulls_http.go`, `cifixer.go`,
`factory_controller.go`, tests.

1. `POST …/fix` validates that the named check is failing on the record's
   current head, refuses with 409 and the running fix's id when `Fix` is
   set, stores `PullFix`, audits with the caller, and returns the record.
2. Factory origin: issue the item's repair through the controller; when its
   rounds are exhausted, add a `FactoryGrant` of one round with reason
   "requested by the user".
3. Other origins: call `startCIFix` against the record's head branch with
   push to that branch, the limits under Decisions, the note appended to
   the recipe's prompt, and `forbiddenChange` unchanged.
4. If the head has moved when the worker is about to start, cancel and
   clear `Fix`.
5. `…/fix/stop` stops the worker and clears `Fix`; audited.
6. Outcomes: a pushed fix clears `Fix`. Did not reproduce, gave up,
   forbidden change, stopped, and every investigate-only result add an
   inbox item of kind `pull` carrying the check, what was tried and the
   finding.
7. Tests: `TestFixRefusesACheckThatIsNotFailing`,
   `TestSecondFixIsRefusedWithTheRunningID`,
   `TestFactoryPullFixUsesTheRepairLoop`,
   `TestExhaustedRoundsGrantOne`, `TestAgentPullFixRunsTheFixer`,
   `TestFixIsCancelledWhenTheHeadMoved`,
   `TestAFixDeletingATestIsRefused`, `TestStopClearsTheFix`,
   one `TestFixOutcome…BecomesAnAsk` per outcome, `TestFixIsAudited`.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -run 'Fix' -count=1`.

## Task 6 — automatic fixing, record, full verification

**Files:** `web/bridge/automation.go`, `pulls_observe.go`, tests,
`docs/studio-flutter/f5-pull-requests-and-ci.md`.

1. `autoFix` on the project's automation settings, default false. When
   true, the observer dispatches a fix for the first failing check of a
   non-factory record, once per head.
2. Tests: `TestAutoFixDispatchesOncePerHead`,
   `TestAutoFixIsOffByDefault`, `TestAutoFixLeavesFactoryPullsAlone`.
3. Implementation record in the spec.

**Verify:** `CGO_ENABLED=1 go test ./web/... -count=1 && go vet ./web/...`;
`cd web/ui && npm run check`; `cd app && flutter analyze`.

## Done when

- Pull requests from an agent, a factory item and the CI fixer appear in
  `GET /api/pulls` with checks for their current heads.
- A fix dispatched on a failing check pushes to the same pull request, and
  a fix that would delete a test is refused and becomes an inbox item.
- A new commit on a pull request clears its old checks at once.

## What later plans rely on

- The Contracts above, and the `pull` delta and inbox kinds.
