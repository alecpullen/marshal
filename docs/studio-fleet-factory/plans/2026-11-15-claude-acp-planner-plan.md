# Claude ACP planner and worker boundary implementation plan

**Goal:** Let a user plan with Claude in Studio and hand approved documents and optional selected workspace edits to a Marshal factory, using a worker boundary that can later host other ACP agents or a direct SDK backend.

**Spec:** [Claude planning in Studio and the worker boundary](../claude-acp-worker-design.md), approved 2026-11-15; [worker and planner direction](../worker-and-planner-integration.md).

**Base:** authored against `5821126b` plus the approved uncommitted design. The local `main` and P5 work have advanced since this checkout. Start the implementation branch from current `main`, include the approved design and this plan in the factory input, recheck the P5, preview-links and project-defaults branches before touching shared files, and preserve their contracts. Do not merge unrelated branches merely to reproduce this historical base.

**Scope:** First usable Studio-hosted Claude planner, ACP worker contract and adapter, reviewed factory handoff with optional selected edits; capability proof for planner, implementer and reviewer. Marshal remains the production factory worker. A production Claude factory-worker option, direct SDK backend, external-terminal MCP factory tools, arbitrary ACP agent catalog, cross-conversation transcript import and automatic planner delegation are out of scope.

**Assumptions:** `web/` remains stdlib-only. The bridge runs and owns local helper processes; a browser disconnect does not cancel a turn, but a bridge/process failure makes the turn outcome unknown. A user installs the standalone Claude ACP adapter and its paired Claude executable on the execution host; configure an absolute adapter path and explicitly provision supported API-key authentication on that host. No automatic download, interactive login, credential copying to the browser, or claim of subscription support. The Claude adapter speaks negotiated ACP v1; gate features on its advertised capabilities and confirmed mode. The default requested mode is `bypassPermissions`, and the restricted requested mode is `default`; select and confirm the requested mode before the first prompt. If a mode is unavailable or rejected, show the reason and do not silently fall back or dispatch. Managed/Claude policy may still restrict actions; display actual effective mode, not a claim of unrestricted OS access. Browser and model inputs are untrusted.

**Research references:** [Claude ACP adapter](https://github.com/agentclientprotocol/claude-agent-acp), [ACP session setup](https://agentclientprotocol.com/protocol/v1/session-setup), [Claude Agent SDK authentication](https://code.claude.com/docs/en/agent-sdk/overview). Pin the actual installed adapter version and capability evidence in the proof record; do not encode undocumented adapter extensions as universal ACP.

## Contracts and global behavior

Names and JSON shapes below are the contracts between tasks; use new bridge-local files and types, without importing `internal/...` into `web/`.

```go
// web/bridge/worker_backend.go
// Unsupported methods return ErrWorkerUnsupported; an uncertain dispatched
// turn returns ErrWorkerUncertain, never success or an automatic retry.
type WorkerCapabilities struct {
    Prompt bool `json:"prompt"`
    Cancel bool `json:"cancel"`
    PermissionRequests bool `json:"permissionRequests"`
    SessionLoad bool `json:"sessionLoad"`
    WorkspaceEdits bool `json:"workspaceEdits"`
    Modes []string `json:"modes"`
}
type WorkerSessionSpec struct {
    WorkerID string `json:"workerId"`
    ProjectRoot string `json:"projectRoot"`
    WorkspaceID string `json:"workspaceId"`
    CWD string `json:"-"`
    Mode string `json:"mode"`
}
type WorkerTurnResult struct { StopReason string `json:"stopReason"` }
type WorkerBackend interface {
    Start(context.Context, WorkerSessionSpec) (string, WorkerCapabilities, error)
    Prompt(context.Context, string, string) (WorkerTurnResult, error)
    Cancel(context.Context, string) error
    Close(context.Context, string) error
}
```

The backend exposes agent notifications and requests through an instance-owned event/request sink passed to its constructor, with stable session and request IDs, ordered event sequence, bounded message content and explicit cancellation/settlement. Requests are answered once; disconnecting a browser never answers or cancels them. The planner service is the sole owner of each process/session and exposes a client-neutral journal/replay API; the interface above deliberately does not imply a generic session-load capability until a backend proves it.

```go
// web/bridge/planner_types.go
// Paths below are repository-relative slash paths, never arbitrary host paths.
type PlannerArtifact struct { Kind string `json:"kind"`; Content string `json:"content"`; SHA256 string `json:"sha256"` }
type SelectedFile struct { Path string `json:"path"`; Operation string `json:"operation"`; BaseSHA256 string `json:"baseSha256,omitempty"`; ContentSHA256 string `json:"contentSha256,omitempty"`; Executable bool `json:"executable"` }
type EditSnapshot struct { BaseCommit string `json:"baseCommit"`; Files []SelectedFile `json:"files"`; SHA256 string `json:"sha256"` }
```

Use file-level selection in v1: create, modify and delete regular files, including explicitly selected untracked regular files. A rename is a selected delete plus create. Do not follow symlinks or include symlinks, submodules, directories, devices, ignored untracked files, `.git`, `.marshal`, `.env` and `.env.*` files, `*.pem` and `*.key` files, or paths outside the checkout; report exclusions and reject attempts to select them. Enforce 1 MiB per file, 10 MiB total and 100 files; retain binary bytes and executable bit. Refuse files changing during capture: compare identity, size and content on a second read, and require user to retry if changed. A model may edit concurrently, so capture does not claim an atomic filesystem snapshot; the user reviews the resulting frozen bytes and their hashes before confirmation. Store bytes by SHA-256 in a private bridge-owned content-addressed store with atomic write, fsync, directory permissions 0700 and file permissions 0600. Snapshot hash is SHA-256 of canonical JSON containing the base commit and ordered file metadata. Do not use the agent workspace as the durable store. Never place raw file contents or credentials in event logs. These filename exclusions do not detect every secret; show selected contents for user review and make no claim of automatic secret detection. Fail a handoff if blobs are missing or corrupt.

Handoff reuses `FactoryCreate`/`FactoryApproval`: add an optional `editSnapshot` to both, copy it on approval, and include its canonical hash in a **versioned** approval input digest. Preserve the legacy `FactoryPlanHash` rule when `editSnapshot` is absent, so old journals and approvals replay unchanged. Creation writes artifacts and selected blobs durably before the factory draft references them; an interrupted create is reconciled by request ID and content hash. Failed draft creation may leave unreachable blobs; a later explicit maintenance task may collect them, never during a running factory. The user sees exact artifact text, each selected file's diff and snapshot hash before submitting, then uses existing factory approval. Changes after capture do not affect the draft.

Before seed, require `EditSnapshot.BaseCommit == item.BaseSHA` and verify every blob and its base file hash against the factory checkout. With no snapshot, current seed behavior is unchanged. With a snapshot, apply only the selected bytes/deletions under the already-owned clean factory branch, with symlink-safe traversal and a preflight for the entire set before mutation. Seed plan, spec and selected edits in the existing seed commit; verify the resulting Git tree contains the intended bytes and no unselected changes. If base, paths, files, hashes, or preflight disagree, stop before mutation and ask the user; if an interruption occurs after mutation begins, persist an uncertain result and use existing recovery instead of blind reapply. User selection is not consent to push/merge outside the existing factory workflow.

## Task 1: Worker backend seam and fake

**Goal:** Represent backend capabilities and session ownership without changing existing Marshal factory execution.

**Files:** Create `web/bridge/worker_backend.go`, `web/bridge/worker_backend_test.go`.

**Steps:** Define the WorkerBackend, WorkerSessionSpec, WorkerCapabilities and WorkerTurnResult contracts above, `ErrWorkerUnsupported` and `ErrWorkerUncertain`; define an instance-scoped `workerEventSink` that accepts `(sessionID string, sequence uint64, kind string, data json.RawMessage)` and `workerRequestSink` that accepts `(ctx context.Context, sessionID string, requestID string, method string, params json.RawMessage) (json.RawMessage, error)`. Add an in-process fake in the test file. Tests: `TestWorkerCapabilitiesArePerSession`, `TestWorkerRequestsSettleOnce`, `TestWorkerUnknownOutcomeIsNotSuccess`, `TestWorkerSinkIsInstanceScoped`. No global factory or live endpoints.

**Verify:** `go test ./web/bridge -run '^TestWorker' -count=1` and `go test ./web/bridge -run TestWebIsStdlibOnly -count=1` pass. Commit `claude-planner: task 1 — worker seam`.

## Task 2: ACP worker adapter

**Goal:** Drive a configured Claude ACP adapter over stdio with negotiated capabilities and confirmed access mode.

**Files:** Create `web/bridge/worker_acp.go`, `web/bridge/worker_acp_test.go`; modify `web/bridge/child.go` and `web/bridge/transport.go` only where necessary for general ACP framing/process lifecycle, preserving existing Marshal behavior.

**Steps:** Implement `newACPWorkerBackend(transport agentTransport, events workerEventSink, requests workerRequestSink) WorkerBackend` with ACP v1 `initialize`, `session/new` (absolute cwd and explicit empty `mcpServers`), mode negotiation and `session/set_mode`, prompt, notification cancel and close. Use `Child` request/notification handling and `newProcessTransport` without its implicit `marshal acp` argument; verify selected mode before prompt. Use the adapter's capability response and session mode list rather than guessing. Emit text, tool activity, mode changes, permissions and questions without dropping IDs; preserve unknown supported updates as bounded opaque events. `session/request_permission` and supported elicitation requests go to the request sink; unsupported client requests receive an explicit protocol error. Make `Prompt` fail as uncertain when transport dies after dispatch, and never restart/reissue a prompt automatically. On close settle pending requests as canceled once. Do not log env or secret material.

Add a scripted in-process newline JSON-RPC fake agent, no live network: `TestACPWorkerHandshakeAndMode`, `TestACPWorkerModeUnavailable`, `TestACPWorkerPermissionAndQuestion`, `TestACPWorkerPromptCancel`, `TestACPWorkerTransportLossIsUncertain`, `TestACPWorkerCloseSettlesRequests`, `TestACPWorkerNoAutomaticTurnReplay`. Test simultaneous sessions do not cross-route requests. Use a context deadline in every fake to prevent hangs.

**Verify:** `go test ./web/bridge -run '^(TestACPWorker|TestChild)' -count=1` and `go test -race ./web/bridge -run '^TestACPWorker' -count=1` pass. Commit `claude-planner: task 2 — ACP worker backend`.

## Task 3: Workspace and planner service

**Goal:** Create a persistent planner identity and independently owned ACP session in a new or user-selected project workspace.

**Files:** Create `web/bridge/planner_types.go`, `web/bridge/planner_store.go`, `web/bridge/planner_service.go`, `web/bridge/planner_service_test.go`; modify `web/bridge/http.go` only to register routes, and `web/bridge/worker_backend.go` only for sink wiring.

**Steps:** Persist a planner record keyed by random ID with project root, backend ID, workspace ID, adapter session ID, requested and confirmed mode, lifecycle state and event sequence; use bridge `stateDir` and atomic 0600 storage. Implement `PlannerService` as the sole owner of backends and durable state. Default creation makes a separate Git checkout at the registered project base using existing bridge Git/workspace mechanisms; existing workspace selection uses an ID returned by the bridge, never a browser-supplied filesystem path. Report occupancy as `free`, `in_use`, or `unknown`: show active known owners; a stale/unreachable owner is unknown. An existing `in_use`/`unknown` selection requires the explicit `proceedShared` field; it does not block the user. No ownership claim may be inferred from a missing record. New sessions default to permissive mode; reject an unconfirmed mode before accepting a prompt. No shell invocation is built by concatenating paths or user input.

Routes: `POST /api/planners` with `{projectRoot, repoId, workspaceId?, proceedShared?, mode?}`; `GET /api/planners/{id}`; `POST /api/planners/{id}/prompt` with `{requestId,text}`; `POST /api/planners/{id}/cancel`; `POST /api/planners/{id}/requests/{requestId}` with the chosen response; `GET /api/planners/{id}/events?after=<sequence>`; and `DELETE /api/planners/{id}` for explicit end. Resolve and validate project trust and workspace ownership on the server. Store dispatch intent before prompt and deduplicate request IDs; stream/replay bounded events without persisting secret environment. Closing a client connection does not stop a session. On bridge restart mark in-flight turns `unknown`, never resend, and advertise session resume only after actual `loadSession` support and successful replay; otherwise offer a new planner. Persist answers/decisions and artifact references in the planner record; do not pretend this is a complete cross-project knowledge store.

Tests: `TestPlannerSeparateWorkspaceDefault`, `TestPlannerSharedWorkspaceRequiresExplicitProceed`, `TestPlannerUnknownOccupancy`, `TestPlannerInvalidProjectOrWorkspace`, `TestPlannerDisconnectDoesNotCancel`, `TestPlannerRequestSettlement`, `TestPlannerRestartDoesNotReplay`, `TestPlannerModeNotConfirmedBlocksPrompt`, `TestPlannerConcurrentPromptsRejected`, `TestPlannerEventsReplayAndBounded`. Use temp dirs and fake backend.

**Verify:** `go test ./web/bridge -run '^TestPlanner' -count=1` and `go test -race ./web/bridge -run '^TestPlanner' -count=1` pass. Commit `claude-planner: task 3 — planner service`.

## Task 4: Immutable artifact and edit capture

**Goal:** Capture user-selected files and artifact contents into a durable, reviewable immutable snapshot.

**Files:** Create `web/bridge/planner_snapshot.go`, `web/bridge/planner_snapshot_test.go`; modify `web/bridge/planner_types.go`, `web/bridge/planner_service.go` and `web/bridge/planner_service_test.go`.

**Steps:** Add `POST /api/planners/{id}/snapshots` accepting exact plan/spec text and a list of repository-relative selected paths, with empty selection as documents-only. The server resolves paths from the registered planner workspace and base commit, applies all contract limits and exclusions, double-reads selected files, builds file-level diffs against the base and persists immutable content-addressed blobs and metadata. `GET /api/planners/{id}/snapshots/{snapshotId}` returns artifact text, metadata, per-file diffs and content hashes for review; do not return absolute host paths. Snapshot creation does not create or approve a factory item. Link a bounded project briefing to the planner record: current approved plan/spec, recorded decisions and unresolved questions where available, with source IDs; expose absent sources as absent rather than inventing facts. Provide `GET /api/planners/{id}/context` and let the user supply additional bounded text in the prompt; do not include environment variables or credentials.

Tests: `TestPlannerSnapshotSelectedOnly`, `TestPlannerSnapshotUntrackedAndDelete`, `TestPlannerSnapshotRejectsUnsafePathsAndSymlinks`, `TestPlannerSnapshotLimits`, `TestPlannerSnapshotChangingFileRejected`, `TestPlannerSnapshotPersistsAfterWorkspaceRemoval`, `TestPlannerSnapshotBinaryAndExecutable`, `TestPlannerSnapshotCorruptionDetected`, `TestPlannerContextDistinguishesApprovedAndProposal`. Test concurrent edits and cancellation with injected reader hooks rather than real user files.

**Verify:** `go test ./web/bridge -run '^TestPlannerSnapshot|^TestPlannerContext' -count=1` and `go test -race ./web/bridge -run '^TestPlannerSnapshot' -count=1` pass. Commit `claude-planner: task 4 — capture immutable handoff`.

## Task 5: Factory draft and seed selected edits

**Goal:** Bind the reviewed snapshot to a factory draft and safely apply only that approved selection to the factory-owned branch.

**Files:** `web/bridge/factory_types.go`, `web/bridge/factory_store.go`, `web/bridge/factory_controller.go`, `web/bridge/factory_git.go`, `web/bridge/factory_http.go`, `web/bridge/factory_*_test.go` as needed; `web/bridge/planner_service.go` and its test only for draft handoff wiring.

**Steps:** Extend the existing factory types with optional `editSnapshot`; add `POST /api/planners/{id}/snapshots/{snapshotId}/handoff` accepting a normal `FactoryCreate` configuration and request ID. Verify the immutable snapshot belongs to the named planner/project/repository, and call the existing create path with its exact artifact bytes and snapshot. Extend approval hashing with an explicitly versioned digest for snapshot-bearing approvals only; preserve existing no-snapshot journals, request replay and `FactoryPlanHash` behavior. Bind snapshot to approval revision, clone slices rather than aliasing, and never recompute from a live planner workspace. Update `factoryApplyCommand` and `validateFactoryItem` consistently for approve and replacement retry; a replacement lacking a snapshot drops it only when explicitly submitted as such, while route-only retries preserve it.

At `factoryGit.Seed`, verify base and all snapshot content, exclusions, paths and expected old hashes before any filesystem mutation. Write using symlink-safe primitives and stage only intended files; commit artifacts plus selected edits via the existing session commit. Check final tree and clean status. Treat partially applied/interrupted seed as uncertain through the existing effect journal and manual reconciliation; do not delete unselected files. Tests: `TestFactoryLegacyApprovalReplay`, `TestFactorySnapshotApprovalHash`, `TestFactorySnapshotHandoffDedup`, `TestFactorySnapshotSeedSelectedOnly`, `TestFactorySnapshotBaseConflict`, `TestFactorySnapshotCorruptBlob`, `TestFactorySnapshotSymlinkRace`, `TestFactorySnapshotInterruptedSeed`, `TestFactorySnapshotRouteRetryPreservesSelection`, and `TestFactorySnapshotNoPlannerWorkspaceNeeded`. Use disposable repositories and fake ACP; do not change existing CI/PR/review gates.

**Verify:** `go test ./web/bridge -run '^(TestFactorySnapshot|TestFactoryLegacyApprovalReplay|TestFactoryEndToEnd|TestPlannerSnapshot)' -count=1` and `go test ./web/bridge -run TestWebIsStdlibOnly -count=1` pass. Commit `claude-planner: task 5 — bind and seed selected edits`.

## Task 6: Studio planner and handoff

**Goal:** Provide the approved phone-friendly planner and reviewable handoff using the service APIs.

**Files:** Create `web/ui/src/views/Planner.svelte`, `web/ui/src/views/Planner.test.ts`, `web/ui/src/lib/planner.ts`; modify `web/ui/src/lib/api.ts`, `web/ui/src/lib/routes.ts`, `web/ui/src/App.svelte`, `web/ui/src/views/FactoryNew.svelte` and their existing tests.

**Steps:** Add typed client methods for Task 3–5 routes; route from project/factory into planner creation. Show requested/confirmed mode at start and during session, default permissive, restricted option, and explicit failure when unavailable. Default to separate workspace; for an existing `in_use` or `unknown` workspace present the owner/uncertainty, a separate-workspace action and explicit proceed action. Render event stream, tool progress, user questions and permission choices with reconnect/replay by sequence; use a single active prompt and retain unsent input on failure. On client disconnect show disconnected, retry reads without retrying a dispatched prompt; on unknown outcome offer a fresh session with the project context, not an automatic resend. Provide context summary and expand/add controls.

Provide editable plan/spec, review exact captured artifact contents, file list and individual file-level diffs; default no files selected. Capturing, reviewing, confirming and draft creation are distinct actions; show base, hash, rejected paths and conflict errors. Handoff opens the actual factory draft for the existing approval; preserve current factory creation behavior for manual input. Keep existing tabs/routes, avoid P5 inbox and relay code. Use semantic labels, keyboard operation, and 390px width without horizontal overflow. Tests: `showsPermissiveModeAndRestrictedChoice`, `warnsButPermitsSharedWorkspace`, `rendersQuestionsAndAnswersOnce`, `disconnectDoesNotResend`, `showsUnknownAndFreshSession`, `contextCanBeExpanded`, `documentsOnlyDefault`, `reviewSelectedFilesBeforeDraft`, `snapshotErrorRetainsInput`, and existing FactoryNew tests.

**Verify:** `cd web/ui && npm test -- src/views/Planner.test.ts src/views/FactoryNew.test.ts` passes; `npm run check` has no new errors relative to recorded current-main baseline. Commit `claude-planner: task 6 — Studio planner`.

## Task 7: Capability proof and integration record

**Goal:** Verify the complete planner and measure whether ACP meets factory-role needs before any production Claude worker claim.

**Files:** Create `docs/studio-fleet-factory/claude-acp-capability-report.md`, `web/bridge/worker_acp_integration_test.go`; update the design and this plan's implementation record. Any helper fixtures must live under `web/bridge/testdata/`.

**Steps:** Build a deterministic fake-ACP integration test for session setup, both modes, permission/question routing, accepted prompt surviving browser disconnect, unknown result after process loss, snapshot review, factory draft and selected-edit seed. Add disposable implementer/reviewer exercises against the WorkerBackend and existing factory result parser: correct revision, expected role report, bad/missing report rejected, cancellation and lost acknowledgement uncertain. These exercises do not register a production Claude factory backend. Do not contact a live provider in default tests.

Write the capability report as a table of planner and factory role requirements with `pass`, `fail`, `untested`, evidence, adapter version and proposed SDK comparison. Real Claude authentication, permissive/restricted actions, process resume, actual local host platform and implementer/reviewer execution require an explicitly configured disposable project and provider credentials: run only if available and explicitly scoped; otherwise mark untested, do not manufacture evidence or block unrelated local verification. Compare each actual gap against the direct SDK only after checking its documented capability and terms; do not implement the SDK backend without a separate decision. Check install/version diagnostics, authentication errors and no secret leakage.

**Verify:** `go test ./web/bridge -run '^(TestACPWorker|TestPlanner|TestFactorySnapshot)' -count=1`; `go test ./web/bridge -run TestWebIsStdlibOnly -count=1`; `cd web/ui && npm test && npm run check && npm run build`; `CGO_ENABLED=1 go build ./cmd/marshal`; `go build ./cmd/webbridge`; `go test ./...`; `go vet ./...`; `git diff --check`; `gofmt -l` on changed Go files yields no output. Record baseline failures separately. Exercise actual Studio at desktop and 390px, including shared-workspace proceed, approvals, disconnect and both handoffs. Build the embedded SPA before browser testing. Commit `claude-planner: task 7 — verify and record capability`.

## Integration and recovery notes

- Before execution, reconcile file and API overlap with current P5, preview-links and project-defaults branches. Use current `main`; the base hash above is evidence of planning, not an instruction to reset a branch.
- Handoff selection and user approval are separate. A seed never executes from mutable workspace bytes, and a detached command or unknown prompt is not evidence of completion.
- A factory using this plan must include the approved design, plan and capability-report output; follow existing factory branch and review policies rather than pushing or merging autonomously.
- The user approved this plan on 2026-11-15; execution follows the factory handoff. If live proof is unavailable, report that limitation and the untested capabilities explicitly.
