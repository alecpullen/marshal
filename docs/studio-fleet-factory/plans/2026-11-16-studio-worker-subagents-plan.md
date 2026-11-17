# Studio worker subagents implementation plan

> **For agentic workers:** Use the repository's `marshal-executing-plans` workflow to execute these tasks sequentially. Do not implement until this plan is approved.

**Goal:** Make Marshal subagents usable and understandable in Studio Fleet and Factory, and ensure factory workers wait for their delegated work before advancing.

**Architecture:** Extend Marshal's session-owned child controls and ACP projection, then use those controls in the shared Studio session view. Factory owns approved settings, worker supervision and durable continuation; it does not become the owner of individual subagents. User steering is addressed to the owning session and child, and child authority stays bounded by the worker's.

**Tech stack:** Go, existing Marshal ACP client/service seam and SQLite/session persistence; standard-library-only `web/bridge`; Svelte 5 and the existing Studio session components.

**Spec:** [Studio worker subagents](../studio-worker-subagents-spec.md); [approved design](../studio-worker-subagents-design.md).

**Base:** `origin/main` at `ea41e45f`. The checkout is on a docs branch; implement from an up-to-date `main`, reconcile in-flight daemon/plan-revision changes, and do not include that branch's unrelated changes. These tasks build on existing code at the stated base, not on the separate completion PR.

**Non-goals:** nested subagents, a new worker runtime, a factory-only child execution system, direct Claude SDK integration, implying ACP providers support delegation, or changing the independent factory review gate. Real provider credentials and external services are not required for tests.

## Global constraints

- Missing override means inherit; explicit false is not missing. Drafts follow source changes; approval freezes the resolved settings and source versions.
- Existing sessions and old journals retain normal Marshal behavior. All controls respect session ownership and the parent's live authority.
- `web/` uses the standard library and its own wire types; never import `marshal/internal/...` there.
- Do not report a child stopped on cancellation acknowledgement, treat unknown work as idle, repeat an uncertain continuation, or claim an instruction was consumed merely because it was queued.
- Match TUI concepts while making Studio's mobile and keyboard interactions usable. Cross-provider capabilities are explicit; unsupported means unavailable.

## Review focus

- Child completes while direct steering is being accepted: task 1 pins rejection or exactly one queued instruction plus parent notice.
- Child IDs are session-local: tasks 1–2 verify two sessions can both contain child 1 without cross-delivery.
- Settings change between review and approval: tasks 3–5 verify a conflict and preserve frozen approved values.
- A child finishes after its parent final report: tasks 6–7 verify continuation and prohibit accepting the old report.
- Restart or lost response after dispatch: task 7 verifies no blind repeat and an explicit recovery state when evidence is insufficient.

## Shared contracts

The wire definitions below are additive. `internal/acpwire` and `web/bridge` own separate wire declarations because of the `web/` boundary. In the web types, use the same JSON property names and basic types. The per-session settings sent to `session/new` are resolved values; they never mutate project TOML. Stable IDs are scoped to the owning session and retained across retries.

```go
// package acpwire
const MethodSubagentSteer = "session/subagent_steer"

type SubagentSteerParams struct {
    SessionID  string `json:"sessionId"`
    SubagentID int64  `json:"subagentId"`
    RequestID  string `json:"requestId"`
    Message    string `json:"message"`
}
type SubagentSteerResult struct {
    RequestID string `json:"requestId"`
    Status    string `json:"status"` // accepted; model consumption is separate
}
type SubagentSettings struct {
    Enabled     bool   `json:"enabled"`
    Model       string `json:"model,omitempty"` // empty: normal child routing
    Concurrency int    `json:"concurrency"`
}
type SubagentCapabilities struct {
    Settings bool `json:"settings"`
    Steer    bool `json:"steer"`
    Stop     bool `json:"stop"`
    Observe  bool `json:"observe"`
}
```

```go
// package bridge; used only for factory configuration and journals
type FactorySubagentOverrides struct {
    Enabled     *bool   `json:"enabled,omitempty"`
    Model       *string `json:"model,omitempty"` // explicit empty means normal routing
    Concurrency *int    `json:"concurrency,omitempty"`
}
type FactorySubagentSettings struct {
    Enabled     bool   `json:"enabled"`
    Model       string `json:"model,omitempty"`
    Concurrency int    `json:"concurrency"`
}
type FactoryDelegatedTurn struct {
    EffectID       string   `json:"effectId"`
    WorkerID       string   `json:"workerId"`
    SessionID      string   `json:"sessionId"`
    ParentTurnID   string   `json:"parentTurnId"`
    ChildIDs       []int64  `json:"childIds"`
    ContinuationID string   `json:"continuationId,omitempty"`
    State          string   `json:"state"` // waiting, prepared, dispatched, settled, uncertain
}
```

Store separate implementer/reviewer overrides on the draft and project factory defaults, and resolved `FactorySubagentSettings` on the approval revision. Persist `FactoryDelegatedTurn` beside the effect. The source version includes both Studio project settings and the Marshal session/defaults snapshot; the server must reject approval if either has changed since review. Only a turn known to have launched children enters `waiting`: unknown child identity or activity is recovery/unknown, never a settled empty set. `ContinuationID` is unique per iteration and bound to effect, worker session and parent turn; successive rounds use new IDs.

## Task 1: Session-owned direct child steering

**Goal:** A human can steer a live child exactly once per request and the parent sees the instruction.

**Files:** `internal/acpwire/turncalls.go`, `internal/app/session/subagents.go`, `internal/acp/service/turns.go`, `internal/acpclient/` session interface, `internal/acp/controls.go`, `internal/acpclient/rpc/turns.go`, and new tests in the corresponding existing packages.

**Steps:**

1. Add the `SubagentSteerParams`, `SubagentSteerResult`, and method constant from Contracts. Extend the session client/service seam with `SteerSubagent(ctx context.Context, request acpwire.SubagentSteerParams) (acpwire.SubagentSteerResult, error)`; wire `ControlManager` and the RPC client next to `SubagentCancel`. Resolve the session through the existing control manager and validate the request's session identity before looking up its child.
2. Add a session-owner operation that atomically checks live child identity/status, deduplicates `(session, request ID, child, message)`, records the user instruction in the child-visible transcript, enqueues it through existing `PushSteering`, and queues a parent-visible notice with child label and instruction. Conflicting replay fails; queue acceptance is not model consumption. Persist the receipt and notice with the session's existing durable storage so a retry after response loss/reconnect cannot enqueue twice. Coordinate with `FinishSubagent` so a finishing child cannot accept a message it will never drain.
3. Reuse the underlying owner operation from `agent.NewSubagentSteerTool` where practical without mislabeling parent-originated instructions as user-originated. Keep all actor authority and approval paths intact.
4. Add `TestUserSubagentSteerAcceptedAndNotifiesParent`, `TestUserSubagentSteerReplayAndConflict`, `TestUserSubagentSteerFinishRace`, `TestUserSubagentSteerSessionIsolation`, and a reconnect/replay test. Include unauthorized and blank inputs, and confirm accepted does not mean consumed.

**Verify:** `go test ./internal/app/session ./internal/agent ./internal/acp/... ./internal/acpclient/... -run 'Subagent|Steer'` passes.

## Task 2: Studio control transport and capability reporting

**Goal:** Studio can discover supported child controls and issue targeted requests without direct internal imports.

**Files:** `web/bridge/contract/viewers.go`, `web/bridge/http.go`, `web/bridge/http_test.go`, `internal/acp/session.go`, `internal/acp/controls.go`, `internal/acpwire/turncalls.go`, `web/ui/src/lib/stack.ts`, `web/ui/src/lib/session/AgentConversation.svelte`.

**Steps:**

1. Expose capability bits in the existing session status/capability response; Marshal reports only controls actually registered. Add `session/subagent_steer` to the relevant viewer policy alongside existing subagent cancel, respecting the existing distinction between read-only and authorized control connections. An older or other ACP backend without the capability remains unavailable; never infer support from an unrelated session.
2. Add authenticated session-scoped HTTP bridge operations forwarding steering and cancellation to the owning ACP session, following the existing session control route conventions in `web/bridge/http.go`. Reuse the existing ACP subagent-cancel control rather than making a second cancellation mechanism. Surface conflicts, unsupported, connection loss and unknown delivery separately.
3. Add bridge tests for authorization, malformed IDs, two sessions with identical child IDs, unsupported methods and lost replies. Add a UI transport test that retains the same request ID on a retry after an uncertain response and does not assert consumption on acceptance.

**Verify:** `go test ./web/bridge ./internal/acp/... -run 'Subagent|Viewer'` and `cd web/ui && npm run check` pass.

## Task 3: Per-session settings capability

**Goal:** A Marshal session reports its ordinary subagent defaults and enforces an optional resolved session-specific snapshot.

**Files:** `internal/acpwire/` session creation wire types, `internal/acp/session.go`, `internal/acp/service/` runtime session creation, `internal/app/runtime.go`, `internal/app/app.go`, `internal/app/session/session.go`, `internal/agent/subagent.go`, and corresponding package tests.

**Steps:**

1. Add `SubagentSettings` and `SubagentCapabilities` from Contracts to the session creation/defaults wire types. Derive ordinary defaults from the same effective project config and route resolution as `buildAgentRunnerWithLock`, including the runtime's concurrency limit. Report a version/digest of those inputs and capability availability to Studio. An empty model means normal child routing, not the worker model.
2. Accept an optional resolved snapshot at session creation and bind it to that session only. Make the `agent.run` handler enforce disabled and concurrency settings, and use the configured model only when set; preserve interaction ceiling and existing per-call policy. Do not write configuration files or change other sessions. Reject invalid limits and unsupported settings before starting an approved worker.
3. Add tests for defaults and legacy omitted settings, disabled delegation, limits, normal and explicit routing, reviewer authority, two simultaneous sessions with different settings, and runtime restart/reload retaining the session snapshot.

**Verify:** `go test ./internal/app/... ./internal/agent/... ./internal/acp/... -run 'Subagent|SessionSettings'` passes.

## Task 4: Inheritance and approval snapshot

**Goal:** Factory drafts follow defaults, explicit overrides survive changes, and approvals freeze the resolved settings.

**Files:** `web/bridge/factory_types.go`, `web/bridge/factory_defaults.go`, `web/bridge/factory_http.go`, `web/bridge/factory_store.go`, `web/bridge/factory_controller.go`, `web/bridge/factory_defaults_http_test.go`, `web/bridge/factory_controller_test.go`.

**Steps:**

1. Add the factory settings and overrides from Contracts to `FactoryDefaults`, `FactoryConfig`/draft, and `FactoryApproval`. Extend `factoryMergeDefaults` and `factoryApplyProjectAgentDefaults` to resolve each role/field using runtime defaults → project factory override → item override, retaining pointers and sources. Do not collapse false or an explicit empty model into missing. Keep the original draft overrides, not just resolved values.
2. Extend draft resolution to refresh inherited fields against current versions; explicit overrides remain fixed. Approval validates the settings versions displayed for review and atomically persists the resolved role settings with the approval. Default changes after approval have no effect on the active revision. Reject unsupported enforcement instead of silently proceeding; preserve legacy journals with absent settings.
3. Add tests named `TestFactorySubagentDefaultsInheritance`, `TestFactorySubagentExplicitFalseAndReset`, `TestFactorySubagentApprovalRace`, `TestFactorySubagentSnapshotStable`, `TestFactorySubagentUnsupported`, and `TestFactorySubagentLegacyReplay`.

**Verify:** `go test ./web/bridge -run 'TestFactorySubagent|TestFactoryDefaults'` passes.

## Task 5: Apply approved settings to workers

**Goal:** Each spawned or recovered factory worker uses exactly its role's approved snapshot.

**Files:** `web/bridge/fleet.go`, `web/bridge/factory_workers.go`, `web/bridge/factory_ownership.go`, `web/bridge/factory_workers_test.go`, `web/bridge/factory_ownership_test.go`.

**Steps:**

1. Extend `SpawnOptions` with the optional resolved session settings. In `SpawnFactoryWorker`, select the approved implementer or reviewer snapshot and pass it through `Fleet.spawn` to `session/new`; do not change non-factory spawns. Send the same snapshot when factory ownership restoration recreates a worker session in `factory_ownership.go`.
2. Bind the snapshot and approval revision to the saved worker identity. Refuse an unsupported runtime or a recovery session whose applied settings cannot be confirmed; do not silently fall back to global settings or mutate the other role's session.
3. Add `TestFactoryWorkerAppliesApprovedSubagents`, `TestFactoryWorkerSubagentSettingsAreRoleScoped`, and `TestFactoryWorkerSubagentSettingsRecovery` covering initial spawn, changed defaults, old items, restart and unavailable capability.

**Verify:** `go test ./web/bridge -run 'TestFactoryWorker|TestFactoryOwnership'` passes.

## Task 6: Observe child work and gate results

**Goal:** A factory turn with pending or unknown delegated work cannot advance on an early report.

**Files:** `web/bridge/factory_types.go`, `web/bridge/factory_workers.go`, `web/bridge/factory_controller.go`, `web/bridge/factory_workers_test.go`, `web/bridge/factory_controller_test.go`.

**Steps:**

1. Extend `FactoryObservation` with effect/session-scoped child identities and terminal states, obtained via the existing session status/work projection. Preserve the separate `session/end_work` check for safe parking; do not infer all child details from its boolean idle result. Missing session, unsupported projection and ambiguous identity are unknown.
2. Extend `factoryWorkers.Execute` and `FactoryController.finishDispatch` to persist `FactoryDelegatedTurn` in waiting state whenever a worker turn launched children not yet proven settled. Keep the original effect and candidate identity; do not pass its early report to `CompleteFactoryEffect`. Project waiting and child status to the item for Studio. For a turn that launched no child, retain the existing normal path.
3. Add `TestFactoryWaitsForDelegatedWork`, `TestFactoryUnknownChildStateDoesNotAdvance`, `TestFactoryNoChildrenUnchanged`, and `TestFactoryEarlyReportNotAccepted`; test different worker sessions with identical child IDs, failed children and child work from an earlier turn.

**Verify:** `go test ./web/bridge -run 'TestFactoryWaitsForDelegated|TestFactoryUnknownChild|TestFactoryNoChildren|TestFactoryEarlyReport'` passes.

## Task 7: Durable parent continuation and cancellation

**Goal:** Once relevant children settle, one recoverable continuation produces the factory report without duplicating work.

**Files:** `web/bridge/factory_controller.go`, `web/bridge/factory_recovery.go`, `web/bridge/factory_workers.go`, `web/bridge/factory_types.go`, `web/bridge/factory_recovery_test.go`, `web/bridge/factory_integration_test.go`.

**Steps:**

1. In `FactoryController.tick`, when all children of the waiting effect are observably terminal, append a `prepared` continuation with a fresh ID; transition to `dispatched` in the store before sending a prompt to the same session. Prompt the parent to incorporate reports, assess failed or stopped children and emit a fresh report for the original effect/candidate. Never treat the earlier final message as the continuation's result.
2. Extend `finishDispatch` and `recoveryOutcome` to bind accepted final evidence to the continuation ID, worker/session identity and candidate. If the continuation launches children, repeat the waiting cycle. If it needs input, use existing factory decision handling. Do not accept a report while new children remain running or unknown.
3. On restart reconcile prepared/dispatched continuation against the same session's turn and transcript. Dispatch prepared only when dispatch is provably absent; accept dispatched only on matching fresh proof. Otherwise mark uncertain and expose the existing recovery decision instead of retrying blindly. Respect stop requests: request cancellation of the worker's children and confirm terminal outcomes before reporting stopped; preserve unrelated sessions and checkouts.
4. Add `TestFactoryContinuationAfterChildSettles`, `TestFactoryContinuationChildFailure`, `TestFactoryContinuationRepeatsForNewChild`, `TestFactoryContinuationRecoveryBoundaries`, `TestFactoryContinuationUncertain`, and `TestFactoryStopIncludesOwnedChildren`. Cover crash immediately before/after each journal write and prompt, and child completion while the parent is idle.

**Verify:** `go test ./web/bridge -run 'TestFactoryContinuation|TestFactoryStopIncludesOwnedChildren|TestFactoryRecovery'` passes.

## Task 8: Shared session UI and factory overview

**Goal:** Fleet and Factory users can follow, steer and stop children through a consistent desktop and mobile interface.

**Files:** `web/ui/src/lib/session/AgentConversation.svelte`, `web/ui/src/views/Chat.test.ts`, `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/lib/factory/ActivityTimeline.svelte`, `web/ui/src/lib/factory.ts`, new `web/ui/src/lib/session/SubagentControls.svelte`, new `web/ui/src/lib/session/SubagentControls.test.ts`, `web/ui/src/views/FactoryItem.test.ts`.

**Steps:**

1. Extend the existing `AgentConversation` drill/breadcrumb path with child task, parent, status, report and controls. Use the task 2 capability and operations; keep a request ID through retries, show queue acceptance separately from model consumption, and show stop pending until observed terminal. Make unsupported/unavailable/unknown states explicit. Preserve focus and scroll on return.
2. Add a compact child hierarchy under each factory worker, with links to that worker's shared conversation. Mark waiting-for-delegated-work and distinguish implementer review children from the independent factory reviewer. Fleet uses the same shared conversation component without factory-specific assumptions.
3. Add component tests for parent → child → parent navigation, instruction and parent notice, pending/failed/unknown controls, same-number children in different sessions, inaccessible transcript, keyboard navigation and narrow-screen presentation. Test that a factory waiting state stays visible during reconnect.

**Verify:** `cd web/ui && npm run check && npm test -- --run` passes. Inspect the Fleet and Factory paths at desktop and phone widths, recording any visual verification not performed.

## Task 9: Settings UI and end-to-end proof

**Goal:** Users can review and change inherited subagent settings and a real Marshal factory worker can delegate through the complete flow.

**Files:** `web/ui/src/views/Project.svelte`, `web/ui/src/views/FactoryNew.svelte`, `web/ui/src/lib/factory/DraftApproval.svelte`, `web/ui/src/lib/factory/config.ts`, `web/ui/src/lib/factory.ts`, their existing tests, and new `web/bridge/factory_subagent_e2e_test.go`.

**Steps:**

1. Add per-role enablement, model and concurrency controls to the existing project settings and work-item draft/approval surfaces. Display inherited values and sources, reset-to-inherit, resolved snapshot and source version. Show unsupported backend settings, approval conflicts and the frozen approved revision. Keep explicit item overrides when defaults refresh.
2. Add UI tests for source changes, explicit false, reset, different roles, approval conflict, active-run freeze, mobile labels and unsupported capability.
3. Build an isolated real Marshal ACP session using a deterministic scripted provider and disposable Git repository; run actual factory implementer and reviewer turns invoking `agent.run`. Verify child observation and authority, direct steering and parent notice, waiting, automatic continuation, fresh report, independent review and no unrelated worker cancellation. Do not replace the agent runtime with a fake factory effects adapter in this test.

**Verify:** `cd web/ui && npm run check && npm test -- --run`; from the root `go test ./web/bridge -run TestFactorySubagentRealSession -v -timeout 5m`. Both pass without credentials or live services.

## Final verification and integration

Run `go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`, and `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent`. Run `cd web/ui && npm run check && npm test -- --run && npm run build`, followed by `go build ./cmd/webbridge` from the repository root; check changed Go files with `gofmt -l`. Run `go test ./web/bridge -run TestWebIsStdlibOnly` and `go test ./internal/app/tui -run TestTUIStaysOnTheClientSeam`. Re-run the isolated real-session scenario through restart and cancellation, and report live model, actual phone and external ACP backend coverage separately. Commit each verified task as `studio-worker-subagents: task N — <title>`.
