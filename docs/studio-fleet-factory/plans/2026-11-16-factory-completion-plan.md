# Factory work-item completion and linked PRs

Goal: deliver explicit work-item completion and reopening, same-repository PR links, and durable ordered merge-and-complete actions in Studio.

Specification: [factory completion](../factory-completion-spec.md); approved [product design](../factory-completion-design.md). Authored against `main` at `87c41ad9`; rebase and reconcile in-flight factory changes before execution. The current worktree may not contain this base. Tasks are sequential and each should be committed as `factory-completion: task N — <title>` after verification.

Non-goals: parent features, cross-repository linking, automatic completion on merge, new MCP permissions, workspace deletion, new planner sessions, and new forge implementations beyond GitHub. Assume existing Studio authentication and project authorization; do not treat an agent, MCP client or an assessment as user authorization. The planner adapter is optional. If no adapter is wired, show unavailable. Do not delay completion for planner work.

## Contracts

These declarations define the shared behavior; JSON tags are part of the API. Add them to `web/bridge/factory_completion_types.go` (new). Existing `FactoryPR`, `Repo`, `Credential`, and `FactoryItem` are in package `bridge`. JSON objects are omitted when absent, and empty completion status from old journals means `open`. Consumers must not mutate past records.

```go
package bridge

import (
    "context"
    "time"
)

type FactoryPRLink struct {
    RepoID string `json:"repoId"`
    PR FactoryPR `json:"pr"`
    Origin string `json:"origin"` // factory or user
    ObservedAt time.Time `json:"observedAt"`
    Fresh bool `json:"fresh"`
}
type FactoryAssessment struct {
    Status string `json:"status"` // pending, available, failed, unavailable
    Summary string `json:"summary,omitempty"`
    Source string `json:"source,omitempty"`
    InputDigest string `json:"inputDigest"`
    ObservedAt time.Time `json:"observedAt"`
}
type FactoryCompletionRecord struct {
    ID string `json:"id"`
    At time.Time `json:"at"`
    Actor string `json:"actor"`
    Goal string `json:"goal"`
    Criteria string `json:"criteria"`
    Assessment *FactoryAssessment `json:"assessment,omitempty"`
    Links []FactoryPRLink `json:"links"`
    RunChoice string `json:"runChoice,omitempty"` // continue or request_stop
}
type FactoryMergeStep struct {
    RepoID string `json:"repoId"`
    Number int `json:"number"`
    NodeID string `json:"nodeId"`
    HeadSHA string `json:"headSha"`
    BaseRef string `json:"baseRef"`
    Strategy string `json:"strategy"`
    State string `json:"state"` // unattempted, prepared, dispatched, confirmed, failed, uncertain
    Result string `json:"result,omitempty"`
}
type FactoryMergeBatch struct {
    ID string `json:"id"`
    RequestID string `json:"requestId"`
    State string `json:"state"` // active, stopped, complete
    CompleteAfter bool `json:"completeAfter"`
    RunChoice string `json:"runChoice,omitempty"`
    Steps []FactoryMergeStep `json:"steps"`
}
type FactoryCompletion struct {
    Status string `json:"status"` // open or complete; empty means open
    Links []FactoryPRLink `json:"links,omitempty"`
    Assessment *FactoryAssessment `json:"assessment,omitempty"`
    Records []FactoryCompletionRecord `json:"records,omitempty"`
    Batch *FactoryMergeBatch `json:"batch,omitempty"`
    StopState string `json:"stopState,omitempty"` // requested, stopped, failed, unknown
}
type FactoryPRMergeRequest struct {
    Number int
    NodeID string
    HeadSHA string
    BaseRef string
    Strategy string
}
type FactoryPRMerger interface {
    MergeFactoryPR(context.Context, Repo, FactoryPRMergeRequest, Credential) error
}
type FactoryCompletionAssessor interface {
    AssessFactoryCompletion(context.Context, FactoryItem, []FactoryPRLink) (FactoryAssessment, error)
}
```

The journal retains `FactoryCompletion` as an additive field on `FactoryItem`. A store update is the atomic boundary for a status, batch or effect transition; the external forge call is never inside that transaction. Confirmation contains a request ID, expected item version, exact ordered steps and run choice; the server creates the reviewed evidence from fresh server-side observations, rejects a mismatch, and journals the authorization before dispatch. Reusing a request ID with different command bytes is a conflict. Use existing `FactoryStore.Update` for version and idempotency enforcement. `FactoryPRMerger` is optional; unsupported is an error, not a simulated success. A provider error after dispatch is uncertain until observed. A positive response still needs a forge read proving merged state and reviewed head. Do not automatically dispatch more work after restart until the preceding dispatched step is reconciled.

## Task 1: Persist explicit completion and reopening

**Goal:** Old and new items have a durable business completion status independent of execution state.

**Files:** new `web/bridge/factory_completion_types.go`, new `web/bridge/factory_completion_test.go`; `web/bridge/factory_types.go`, `web/bridge/factory_store.go`, `web/bridge/factory_controller.go`.

**Steps:**
1. Add the contracts above and an optional `Completion *FactoryCompletion` field to `FactoryItem`. Treat nil or empty status as open without rewriting legacy records. Extend `validateFactoryItem` for valid new states while retaining existing legacy replay behavior.
2. Extend `factoryApplyCommandFrom` with user-originated completion and reopen commands, bound to expected version and request ID through `FactoryController.command` and `FactoryStore.Update`. Reject MCP principals for these new commands. Completing snapshots the goal, approved criteria/spec, current assessment, links, actor and time into a new record; allow open PRs or unknown evidence. Reopening appends a history event, preserves all records and does not spawn or stop a worker. Reject duplicate state transitions without an identical replay.
3. Add `TestCompletionLegacyReplay`, `TestCompletionWithOpenPRs`, `TestCompletionReopenPreservesHistory`, and `TestCompletionConcurrentAndRequestReplay` to cover missing fields, independent execution state, no spawned work and version/request conflicts.

**Verify:** `go test ./web/bridge -run 'TestCompletion'` passes, including replay after closing and reopening a temporary store.

## Task 2: Link and observe PRs

**Goal:** Users can attach multiple verified same-repository PRs and see fresh or explicitly stale evidence.

**Files:** `web/bridge/factory_completion_types.go`, new `web/bridge/factory_completion_links.go`, new `web/bridge/factory_completion_links_test.go`, `web/bridge/factory_controller.go`, `web/bridge/factory_http.go`.

**Steps:**
1. Add a versioned user-only link command accepting a number or URL. Resolve the configured `FactoryConfig.RepoID` through the existing factory forge path and `FactoryForge.GetFactoryPR`; reject URLs from another configured repository before querying. Require a nonempty immutable `NodeID`, validate repository and number, and deduplicate by repo and node ID. Persist origin `user` and the observed timestamp. Never store credentials.
2. In `AdvanceFactory` and the `FactoryController.tick` observation path, associate the existing owned delivery PR as `factory`, deduplicating an already linked identity without altering `factoryPRMatches`, `FactoryItem.PRs` or delivery ownership. Refresh evidence on demand; a failed fetch retains the previous snapshot with `Fresh=false`.
3. Add `TestCompletionLinksSameRepo`, `TestCompletionLinksRejectCrossRepo`, `TestCompletionLinksDeduplicateDelivery`, and `TestCompletionLinkUnavailableIsStale`. Exercise version conflicts and immutable identity changes.

**Verify:** `go test ./web/bridge -run 'TestCompletionLink'` passes; existing `go test ./web/bridge -run 'TestFactory'` remains green.

## Task 3: Forge merge capability

**Goal:** GitHub can attempt an explicitly reviewed PR merge through a capability that other forges may decline.

**Files:** `web/bridge/factory_forge.go`, `web/bridge/forge_github_factory.go`, new `web/bridge/forge_github_merge_test.go`.

**Steps:**
1. Add an unsupported error and a `FactoryPRMerger` capability lookup alongside `FactoryForgeFor`. Implement `MergeFactoryPR` on `githubForge` using its existing authenticated request machinery and GitHub's merge endpoint. Send the reviewed head SHA and selected strategy; never retry a mutation automatically or bypass protections. Reject invalid strategy and empty reviewed SHA.
2. Use `GetFactoryPR` before and after the mutation to check node ID, head SHA, base ref and merged state; propagate rejection and transport errors distinctly to the batch coordinator. A timeout after sending is not proof of failure. Verify GitHub response mapping distinguishes merged from closed-unmerged.
3. Add `TestGitHubFactoryMergeRequest`, `TestGitHubFactoryMergeRejected`, `TestGitHubFactoryMergeUncertain`, and `TestFactoryMergeUnsupported` using `httptest` and isolated credentials; assert exact SHA, strategy, authentication handling and no second request after an ambiguous failure.

**Verify:** `go test ./web/bridge -run 'TestGitHubFactoryMerge|TestFactoryMergeUnsupported'` passes without live GitHub access.

## Task 4: Durable ordered batches and recovery

**Goal:** A confirmed ordered batch merges at most one reviewed PR at a time and survives failures and restarts without blind retries.

**Files:** new `web/bridge/factory_completion_batch.go`, new `web/bridge/factory_completion_batch_test.go`, `web/bridge/factory_controller.go`, `web/bridge/factory_store.go`.

**Steps:**
1. Add a user-only command that validates fresh linked identities, item version, order, strategy, exact heads and bases; persists `FactoryMergeBatch` and authorization before any external effect. Allow merge-only or complete-after, and reject concurrent active batches. Require explicit run choice when active or unknown. Reject a stale confirmation rather than silently adapting it.
2. Drive one step at a time: persist `prepared`, then `dispatched` before the remote call; read the forge and persist `confirmed`, `failed` or `uncertain`. A known already-merged reviewed head may be confirmed with external/unknown provenance. Any failure, mismatch or uncertainty stops the batch; later entries stay unattempted. Never turn a closed-unmerged PR into success. Keep journal and forge mutations separate.
3. During controller recovery, reconcile every dispatched or uncertain entry by reading authoritative PR state and head. If proof is insufficient, stop and surface uncertainty; never resend automatically. Resume a confirmed all-success batch by recording its already-authorized completion exactly once, including after a crash between final merge and completion persistence. A stopped batch requires a new explicit user decision and fresh evidence for retry or remaining merges. Merge-only batches never complete items.
4. Add `TestCompletionBatchOrder`, `TestCompletionBatchStopsOnFailure`, `TestCompletionBatchChangedHead`, `TestCompletionBatchLostResponse`, `TestCompletionBatchRecoveryBoundaries`, `TestCompletionBatchExternalMerge`, `TestCompletionBatchConcurrentConfirmation`, and `TestCompletionBatchMergeOnly`. Inject crashes before and after each journal and forge boundary, and assert no duplicate calls or unauthorized completion.

**Verify:** `go test ./web/bridge -run 'TestCompletionBatch'` passes with fake forges and temporary journals.

## Task 5: Active run and delivery closure

**Goal:** Completion honors the user's run choice while retaining the factory's safe handling of closed delivery PRs.

**Files:** `web/bridge/factory_controller.go`, `web/bridge/factory_workers.go`, `web/bridge/factory_completion_types.go`, new `web/bridge/factory_completion_run_test.go`.

**Steps:**
1. In `factoryBeginClosure` and both closure paths in `AdvanceFactory` and `FactoryController.tick`, preserve current stop/cleanup behavior for external closure. For a combined action that merged its own delivery PR and explicitly chose continue, permit the in-flight effect to settle, retain its checkout and outputs, block new dispatch/publish against the closed PR, and expose a delivery decision needed. Do not open a replacement PR or alter business completion on later events.
2. For request-stop, persist stop intent for this item only and use existing cancellation/observation paths. Update `StopState` to stopped only when observed, or failed/unknown on failure; retain completion if cancellation fails. Continue without merging the delivery PR preserves ordinary controller behavior. Reopen never starts a worker.
3. Add `TestCompletionContinueRun`, `TestCompletionContinueAfterDeliveryMerge`, `TestCompletionStopObserved`, `TestCompletionStopFailure`, and `TestCompletionExternalClosureUnchanged`, including restart while stopping and unrelated active items.

**Verify:** `go test ./web/bridge -run 'TestCompletionContinue|TestCompletionStop|TestCompletionExternalClosure|TestFactory'` passes.

## Task 6: Optional completion assessment

**Goal:** An available assessment is tied to its inputs, and its absence never prevents completion.

**Files:** new `web/bridge/factory_completion_assessment.go`, new `web/bridge/factory_completion_assessment_test.go`, `web/bridge/factory_completion_types.go`, `web/bridge/factory_http.go`.

**Steps:**
1. Add an explicitly requested assessment path backed by the optional `FactoryCompletionAssessor`. With no adapter return `unavailable`; do not start a planner. Compute a stable digest of the approved goal and criteria plus PR identities, heads and states; save pending/result/failure and source with the digest. Never record a late result as current after its input changes.
2. Return freshness derived from the current digest; present stale results as stale. Completion may proceed for any assessment state, and its record snapshots the state and digest.
3. Add `TestCompletionAssessmentUnavailable`, `TestCompletionAssessmentStale`, and `TestCompletionAssessmentDoesNotGate`, using a deterministic injected fake and no network.

**Verify:** `go test ./web/bridge -run 'TestCompletionAssessment'` passes.

## Task 7: Studio API and contract

**Goal:** The Studio client can link, review, merge, complete and reopen through authenticated versioned operations.

**Files:** `web/bridge/factory_http.go`, `web/bridge/factory_types.go`, `web/bridge/factory_http_test.go`, `web/ui/src/lib/factory.ts`.

**Steps:**
1. Extend `factoryRoutes` and `factoryAPI.Command` using existing route and error conventions for link, evidence refresh, assessment, merge-only, merge-and-complete, complete and reopen. Require user-originated Studio authority and project scope; do not add MCP capability mappings. Expose status, history, batch steps, stale evidence and actual stop outcome; do not leak credentials.
2. Extend `FactoryCommand` and the existing handwritten `FactoryItem` and `FactoryCommand` TypeScript interfaces in `web/ui/src/lib/factory.ts`. Implement typed request helpers there, passing stable request IDs, expected item versions and the evidence reviewed by the user. Factory wire types currently live in `factory.ts`, not the generated fleet contract.
3. Add HTTP tests for authorization, cross-project denial, stale version, duplicate IDs, unsupported forge and ambiguous outcome; include a test that existing MCP grants cannot invoke the new actions.

**Verify:** `go test ./web/bridge -run 'TestFactory.*Completion|TestFactory.*Merge'` and `cd web/ui && npm run check` pass.

## Task 8: Completion and recovery UX

**Goal:** Users can make and understand each decision on a phone or desktop.

**Files:** `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/lib/factory/PullRequestsView.svelte`, `web/ui/src/lib/factory/ActivityTimeline.svelte`, `web/ui/src/lib/factory.ts`, new `web/ui/src/lib/factory/CompletionPanel.svelte`, new `web/ui/src/lib/factory/CompletionPanel.test.ts`, `web/ui/src/views/FactoryItem.test.ts`.

**Steps:**
1. Add the completion panel to the item view with business status distinct from execution status, goal/criteria, assessment availability/freshness, PR links and observations. Add link-by-number-or-URL and explicit completion, merge-only, merge-and-complete and reopen actions. Use existing component and request conventions.
2. Make PR selection and ordering keyboard accessible, show exact ordered revisions and strategy in confirmation, and show open/unavailable PRs and unknown criteria without disabling standalone completion. When the run is active or unknown, require a continue/stop choice; show stop outcome separately. Show stale evidence and require refresh before merging.
3. Render confirmed, failed/uncertain and unattempted batch steps; provide explicit fresh retry, remaining-batch or complete-with-open-PRs actions. History distinguishes user completion/reopen from PR state and later run events. On small screens preserve readable order, focus management and reachable controls.
4. Add component tests for mobile-compatible ordering and confirmation, completion with open PRs, active-run choice, stale assessment, partial failure, uncertainty and reopen without starting work.

**Verify:** `cd web/ui && npm run check && npm test -- --run` passes. Inspect desktop and narrow phone layouts with the existing web test setup; record any visual checks that could not be run.

## Final verification and integration

Run `go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`, and `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent`. Run `cd web/ui && npm run check && npm test -- --run && npm run build`, then `go build ./cmd/webbridge` from the repository root so embedded assets are current. Run `go test ./web/bridge -run TestWebIsStdlibOnly` and check Go formatting on changed files with `gofmt -l`. Exercise a fake-forge end-to-end scenario spanning link, ordered partial merge, restart, explicit recovery, completion and reopen; verify a continued run and a confirmed stop. Report live GitHub and actual planner coverage separately, and reconcile any factory plan-revision or daemon changes merged since the specified base before integration.
