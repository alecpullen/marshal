# Parent Integration and Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the parent safely integrate or discard an owned terminal child, retain truthful receipts across interruption, and verify the complete parallel-work journey.

**Architecture:** Parent-only tools address P2 workspace records by owner/task key and exact candidate SHA. A lifecycle service serializes targets, excludes active workspace users, revalidates live admission and journals git mutations before cleanup. Existing subagent/detail projections carry additive metadata; no new screen owns coordination.

**Tech Stack:** Go, existing SQLite store, git `--no-ff` merges, existing platform file-lock patterns, ACP and generated Dart contract.

**Spec:** [Approved specification](../feature-spec.md), §§4–6 and complete acceptance journey. Requires completed P1 and P2 and the [exact shared contracts](README.md).

## Global Constraints

- “A successful report does not mean its changes are present in the parent.”
- “Use a non-fast-forward merge to retain the child's commits.”
- “The target is the captured parent active checkout and branch.”
- “Child completion, failure, cancellation and parent turn completion do not delete the branch or worktree.”
- “Do not stash, reset, rebase, commit unrelated work or resolve conflicts silently.”
- “Apply the existing destructive-operation approval rules to discard.”
- The TUI remains rendering-only; `web/` remains standard-library-only; Flutter model/client/UI boundaries remain enforced.
- Tests use explicit temporary repositories/auth/transports and `scripts/test.sh`; no live services or credentials.

## Review Focus

- A finished model may leave a running detached command; root-use admission must cover queued, attached, detached and unknown executions (Task 1).
- Two parents or processes may target the same checkout; process-local serialization alone is insufficient (Task 1).
- An exact reviewed candidate may change before mutation, or the parent may switch branch; neither may be silently substituted (Task 2).
- Merge success followed by a DB/reply/cleanup failure must not trigger a second merge or delete uncertain source work (Task 2).
- Factory continuation and clients must distinguish executed, integrated, retained and unknown work without becoming lifecycle owners (Task 4).

---

## File structure and order

Task 1 supplies idle/target leases using existing command ownership. Task 2
adds durable mutation/reconciliation logic to `internal/subagentwork`. Task 3
wires parent tools and exact acceptance-boundary permission checks. Task 4
adds projections and proves the complete session/factory journey. Run in this
order; integration is serialized even when implementation was parallel.

### Task 1: Lease idle workspaces and serialize target mutations

**Files:** Create `internal/worktree/{targetlock,targetlock_unix,targetlock_windows,targetlock_other}.go`,
`internal/worktree/targetlock_test.go`,
`internal/app/subagent_idle.go`, `internal/app/subagent_idle_test.go`;
modify `internal/tools/native/{native,command,jobs_manager,git,file,search,diagnostics,workspace_setup,workspace_finish}.go`,
`internal/acp/worktree.go`, `internal/pipeline/{verify,ops,controller}.go`,
`internal/agent/subagent.go`, `internal/app/subagent_workspaces.go`; create
`internal/tools/native/workspace_idle_test.go`.

**Interfaces:** Produce README `AcquireWorkspaceUse`, `UseIdentity`,
`UseLease.MarkStarted/Release`, `Options.WorkspaceUse` and
`LaunchSpec.WorkspaceUses`, plus
`worktree.AcquireTargetLock(ctx context.Context, repository, root string) (func(),error)`;
`(*native.JobManager).AcquireIdleWorkspaces(ctx context.Context, roots []string) (func(),error)`;
and `(*subagentIdleGate).AcquireIdle(ctx context.Context, owner string, roots
[]string) (func(),error)`, implementing README `IdleGate`.

- [ ] **Step 1: Write lease tests.**

  | Test | Assertions |
  |---|---|
  | `TestIdleLeaseIncludesDetachedAndQueuedCommands` | any admitted nonterminal execution at a root returns busy, including the insertion-to-start window; an unrelated root remains usable |
  | `TestIdleLeaseFreezesNewLaunches` | a root lease blocks/rejects new launches there until release; cancellation releases every reservation |
  | `TestIdleLeaseIncludesLiveSharedChildren` | a shared child still running on the integration target causes busy even between its tool calls |
  | `TestUnknownWorkspaceUseIsNotIdle` | unresolved retained execution evidence prevents cleanup; a directory existing is not liveness evidence |
  | `TestTargetLockCoordinatesProcessesAndAliases` | two subprocesses using canonical aliases of the same target cannot both hold the lease; another target can proceed |
  | `TestIdleLeaseLockOrderingAndRelease` | opposing root lists acquire in canonical sorted order; stopped waiter leaks no manager/write/file lock |
  | `TestIdleLeaseOtherRuntimeWriter` | another runtime on the same target holds a normal file-write claim; integration/discard is busy and cannot mutate |
  | `TestIdleLeaseOtherProcessCommand` | a subprocess owns a detached command claim at the source; another process cannot obtain lifecycle exclusivity until observed terminal release |
  | `TestIdleLeaseDirectGitRead` | held native git.status, which bypasses JobManager, prevents source cleanup until its direct command settles |
  | `TestIdleLeaseOwnerCrashRetainsUnknownCommand` | owner dies before terminal evidence; use note remains unknown and a new runtime refuses cleanup even if the lock descriptor was released |

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/worktree ./internal/tools/native ./internal/app ./internal/acp ./internal/pipeline -run 'IdleLease|UnknownWorkspaceUse|TargetLockCoordinates' -count=1`.
  Expected: missing root-use leases and cross-process target lock.

- [ ] **Step 3: Implement the adapters.** Within the existing command-manager
  mutex, check all admitted execution states at canonical roots and install
  root reservations atomically. `Launch` checks them before insertion;
  accepted facts expose cwd at insertion, not only after process start.
  Freeze roots without holding the manager mutex throughout git work.

  Implement README's shared/exclusive root-use protocol for **all** ordinary
  Marshal callers. Direct native file/search/git/diagnostic/setup paths take
  shared use leases for their bound and statically resolved git roots. Managed
  commands transfer leases to JobManager through terminal settlement, not tool
  return or detach. Children hold model-lifetime leases, including shared
  children in other runtimes. Record process-start and terminal evidence in
  per-root use notes; unknown orphan use blocks mutation rather than timing out
  into idle. This mirrors command ownership; it does not supervise processes.
  Use the same cross-process lock key for ordinary shared leases and lifecycle
  exclusive leases. Lifecycle internals inherit exclusivity and never attempt
  shared-to-exclusive upgrades. Unrestricted interpreter effects beyond known
  roots retain the specification's OS-sandbox non-goal.

  The app adapter first acquires the runtime's stable `WriteLock`, checks
  live children at the target, then freezes command roots and acquires sorted
  cross-process target locks. It includes persisted unknown command evidence
  after restart and releases in reverse order. Do not acquire owner authority
  while holding the manager mutex. Acquire live mutation authorization only
  after these workspace leases, matching normal write-gate-before-admission
  order. Direct pipeline verification/operation runners also acquire shared
  use for their actual cwd. Existing workspace.finish, ACP merge/discard and
  pipeline cleanup acquire the same exclusive source/target leases, without
  holding an ordinary shared lease across an upgrade; their internal git calls
  inherit the lease. Add `TestIdleLeasePipelineVerifierAndExistingFinish`.
  This local ordering supplements the shared cross-runtime claims; it
  cannot replace them. Before destructive mutation, inspect unknown use notes
  even after acquiring the OS lock, since a crashed owner may leave an orphan
  command behind.

  Store advisory lock files under the canonical common git directory's
  `marshal-target-locks`, keyed by a hash of the canonical target path. Follow
  the context-aware, nonblocking lock pattern in `internal/snapshot/lock.go`;
  implement Unix flock and Windows LockFileEx using existing platform modules.
  Shared root-use and exclusive target locks use this same file protocol.
  Other unsupported platforms return a capability error, never an unlocked
  success. Marshal locks coordinate Marshal processes; recheck git state and
  honor git's own locks for unrelated external writers.

- [ ] **Step 4: Verify.** Run the red command and
  `scripts/test.sh quick ./internal/worktree ./internal/tools/native ./internal/app ./internal/acp ./internal/pipeline -race -run 'IdleLease|UnknownWorkspaceUse|TargetLockCoordinates' -count=1`.
  Expected: pass with bounded barriers, no timing sleeps or ambient processes.
  Add platform compile coverage through the existing CI/build matrix.

- [ ] **Step 5: Commit.** Commit
  `feat: lease idle workspaces for owned child mutations`.

### Task 2: Integrate exact candidates and journal recovery

**Files:** Create `internal/subagentwork/{mutation,reconcile}.go`,
`internal/subagentwork/{mutation,reconcile}_test.go`;
create `internal/worktree/candidate.go`, `candidate_test.go`;
modify `internal/subagentwork/types.go`, `internal/worktree/git.go` and
`internal/db/subagent_workspaces_test.go` for journal/reopen fixtures.

**Interfaces:** Produce README `Mutation`, `Admission`, `MutationDeps`,
`PreparedMutation`, `MutationGit`, and `Service.PrepareMutation/Integrate/Discard/Reconcile`; add implementations
of `MergeExact(ctx,dir,candidate,message)`, `MergeState(dir)` and
`Parents(dir,commit)` to `worktree.CLIGitOps`. Service mutation requires a
non-nil `IdleGate` and `Admit`; tests inject deliberate real-authorizer
adapters or explicit deterministic leases. Never treat nil as approval.

- [ ] **Step 1: Write real-git and fault-injection tests.**

  | Test | Assertions |
  |---|---|
  | `TestIntegrateTargetsCapturedFeatureWorktree` | target merge has parents `[beforeTarget,candidate]`; original main checkout and unrelated untracked bytes are unchanged |
  | `TestIntegrateSeveralCandidatesFromSameBase` | serial integration accepts target advancement from the previous child and preserves both source histories |
  | `TestIntegrateRefusesSourceOrTargetDrift` | wrong owner, source HEAD, registration, repo, branch, dirty source/target/index, in-progress merge or untracked overwrite causes no mutation |
  | `TestIntegrateConflictRestoresOnlyOwnMerge` | target returns to exact clean pre-operation commit/index; both branches survive; reported conflict paths match git |
  | `TestIntegrateAlreadyPresentAndNoChanges` | repeated same candidate yields original receipt; no extra merge; `Candidate == Binding.BaseSHA` creates no empty merge |
  | `TestReconcileEveryMutationBoundary` | failures before/after intent, merge, result persistence, worktree removal and branch deletion recover one truthful result, never a second merge |
  | `TestCleanupFailureDoesNotFailIntegration` | merge remains successful with `CleanupState == "integrated_cleanup_pending"`; retry touches only remaining matching owned artifacts |
  | `TestDiscardIsExplicitAndPreservesDirtyWork` | expected clean abandoned branch can be removed after admission; changed SHA, dirty/untracked, busy, unknown or legacy ownership is retained |
  | `TestInterruptedModelIsNotResumedOrSuccessful` | reopen preserves available report/candidate and interrupted/unknown outcome; zero provider runs |

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/subagentwork ./internal/worktree ./internal/db -run 'Integrate|ReconcileEvery|CleanupFailure|DiscardIs|InterruptedModel' -count=1`.
  Expected: missing mutation methods/incorrect existing finish semantics.

- [ ] **Step 3: Implement guarded mutations and journal.** Load by owner/key,
  acquire source/target idle leases, inspect exact ownership and status, then
  capture the target-root snapshot if configured. Before accepting mutation,
  recompute `PreparedMutation.EffectKey()` under the lease and pass it to
  `deps.Admit(ctx,effectKey)`, recheck source/target identity and current target HEAD,
  persist `Integration{State:"intent",ExpectedSource,BeforeTarget,OperationID,
  SnapshotHash}`, and commit/release that admission at the accepted mutation
  boundary. New target facts after evaluation require fresh admission. Never
  hold owner admission locks during the entire subprocess; accepted work keeps
  its exact facts as ordinary commands do.

  Merge the **SHA**, not a mutable branch name. Message trailer
  `Marshal-Subagent-Operation: <operation-id>` binds recovery to the journal.
  Persist `integrated` and `ResultCommit` before cleanup. Do not reuse
  `FinishBranch`: its squash/default-target/immediate-cleanup assumptions
  differ. For conflicts, abort only when `MergeState` and the recorded target
  prove this operation owns that merge; if state is uncertain, retain unknown
  and never abort someone else's operation. No reset-hard/stash/forced clean.

  Reconciliation under the same locks proves the exact source candidate is
  reachable from the recorded target and checks the recorded operation
  trailer/parents when identifying this tool's merge. Already-present source
  ancestry is a distinct idempotent success even when no new merge was needed.
  Changed target branch/repository is a refusal, not permission to follow it.
  A new candidate after a prior recorded integration is refused as a changed
  task candidate; dispatch follow-up work under a new key rather than replaying
  old success. A no-change review fork is marked `no_changes` without bringing
  its base candidate into the parent implicitly.

  Clean integrated sources may be removed, then their matching branch deleted
  only if it still points at the reviewed SHA. Journal each cleanup phase;
  absent artifacts after restart are reconciled with the same ownership proof.
  Explicit discard may force-delete the **verified clean owned branch** because
  it is intentionally unmerged, but never force-remove a worktree or dirty
  data. Failed/partial/stopped sources can be inspected and deliberately
  integrated only with their exact known candidate and the same checks;
  unknown execution/source use is never admitted. Preserve execution outcome.

  Startup `Reconcile` is observational: it can persist proven state but cannot
  abort merges or remove artifacts without a fresh per-call admission. A
  retry of the parent tool obtains that proof before pending cleanup. Missing
  already-cleaned artifacts yield the proven original receipt; they do not
  cause source inspection to invent an error or repeat the merge.

- [ ] **Step 4: Verify.** Run the red command and
  `scripts/test.sh quick ./internal/subagentwork ./internal/worktree ./internal/db -count=1`.
  Run `scripts/test.sh quick ./internal/subagentwork -race -run 'IntegrateSeveral|ReconcileEvery|CleanupFailure' -count=1`.
  Expected: every injected crash retains or proves work; no test enables live
  credentials or mutates the developer's checkout.

- [ ] **Step 5: Commit.** Commit
  `feat: integrate owned child candidates with durable recovery receipts`.

### Task 3: Expose parent-only tools with mutation-boundary admission

**Files:** Create `internal/agent/subagent_lifecycle.go`,
`internal/agent/subagent_lifecycle_test.go`;
modify `internal/tools/registry/types.go`, `internal/agent/{execute,subagent}.go`,
`internal/tools/policy/admission.go`,
`internal/app/{app,subagent_workspaces,subagent_idle}.go`;
create `internal/app/subagent_integration_test.go`; extend
`internal/agent/interaction_admission_test.go` and native scope tests.

**Interfaces:** Add README `Tool.OwnsAdmission`, `Tool.PrepareEffect`,
`policy.Call.EffectKey`,
`WorkspaceEffectManaged`, and the exact `NewSubagentIntegrateTool` /
`NewSubagentDiscardTool` constructor signatures. Normal tools preserve their
existing admission path. `MutationDeps.Admit` is constructed per call, using
the handler context's opaque proof, invocation ID and exact argument map.

- [ ] **Step 1: Write boundary tests.**
  `TestLifecycleToolsRequireParentProof` asserts direct unapproved handlers
  mutate nothing. `TestLifecycleRevocationWhileQueued` revokes mode/rules
  while a task waits for a target lease and asserts no git mutation.
  `TestLifecycleOwnedSiblingNeedsNoSystem` integrates/discards a valid owned
  sibling without System but rejects arbitrary paths/owners/legacy records.
  `TestDiscardHonorsDestructivePolicy` pins Ask denial and configured exact
  confirmation/denial behavior. `TestLifecycleDoesNotDeadlockSharedWrite`
  parks an ordinary shared write and a lifecycle call and verifies bounded
  completion, including steering/end-gate cancellation. Assert every owned
  cleanup result and report contains the same task key and candidate.
  `TestLifecycleEffectKeyRejectsTargetHeadChange` asserts a target advancement
  after evaluation refuses the stale proof, then succeeds only after a new
  evaluation. Include detached target branch refusal in the drift cases.

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/agent ./internal/app -run 'Lifecycle|DiscardHonors' -count=1`.
  Expected: missing tools or inability to revalidate a consumed ordinary proof.

- [ ] **Step 3: Register and wire managed tools.** Mark only these managed
  tools as `OwnsAdmission`; `Runner.execute` passes their proof through instead
  of consuming it as an ordinary tool before the handler. Add no risk-based
  automatic bypass. Use the real authorizer's `BeginTool` inside the per-call
  admission closure, called after source/target locking by Task 2. Prepare the
  effect key from the record and actual source/target facts before evaluation;
  include it in `fingerprintCall`. At mutation, supply the key recomputed under
  the lease as `Call.EffectKey`. The same
  logical proof is committed exactly once; stale/denied decisions refuse
  rather than auto-approve. Admission still passes the conversation end gate.

  Their managed preflight captures a snapshot of the recorded target through
  the exact-root app callback, not the parent's potentially different current
  root. Keep existing snapshot warning behavior honest when capture cannot
  publish; never fabricate a snapshot hash or imply rollback coverage. If
  configured policy requires a capture before mutation, refusal is retained.
  Rescan the changed target root after integration. Register tools in ordinary
  parent/factory runtime registries; apply P2's final child filter afterward.
  Return structured retained records and useful refusals. No schema argument
  lets a model pick a free filesystem path or override ownership.

- [ ] **Step 4: Verify.** Run the red command, then
  `scripts/test.sh quick ./internal/agent ./internal/app ./internal/tools/policy ./internal/tools/registry ./internal/app/session -count=1`.
  Run `scripts/test.sh quick ./internal/agent ./internal/app -race -run 'Lifecycle|DiscardHonors' -count=1`.
  Expected: no privilege widening, frozen admission lock or child-visible
  lifecycle tool. Explicit System grants and normal parent workspace tools
  retain their existing behavior.

- [ ] **Step 5: Commit.** Commit
  `feat: expose parent-only subagent integration and discard tools`.

### Task 4: Project lifecycle evidence and prove the complete workflow

**Files:** Modify `internal/app/session/subagents.go`,
`internal/acpwire/{runs,stacknode}.go`, `internal/acp/service/runs.go`,
`internal/viewmodel/detail.go`, `internal/tools/registry/subagent_workspace.go`,
`web/bridge/contract/inspect.go`,
`internal/postmortem/postmortem.go`, `app/lib/src/model/inspect_model.dart`,
`internal/app/tui/transcript.go`, and
`app/lib/src/client/contract.g.dart` via generation. Create
`internal/acp/service/subagent_workspace_test.go`,
`internal/app/subagent_workflow_test.go`,
`web/bridge/factory_subagent_workspaces_test.go`,
`internal/postmortem/subagent_workspaces_test.go`;
extend `app/test/model/{inspect_model,stack_store}_test.dart`,
`internal/app/tui/spine_test.go` and contract fixture tests.
Update `docs/{ARCHITECTURE.md,tools/delegation.md,tools/workspace-interaction.md}`
and `internal/skills/builtin/{using-worktrees,dispatching-parallel-agents,marshal-executing-plans}.md`.
Read `docs/studio-flutter/design-system.md` before any necessary existing UI
kit/detail change; no new screen or screen-only orchestration is planned.

**Interfaces:** Add optional `Workspace *SubagentWorkspace` to
`acpwire.Subagent` and `CallDetail` and the independently defined bridge wire
counterpart `contract.CallDetail`. Define `contract.SubagentWorkspace` in
`inspect.go`; there is no need to invent an unused bridge status roster.
`SubagentWorkspace` has JSON fields `taskKey`, `mode`, `root`,
`branch`, `baseSha`, `candidateSha`, `targetRoot`, `targetBranch`, `execution`,
`integration`, `cleanup`, `reason`; `reason` and unknown candidate may be empty.
Its Go field types are strings. Session projection copies from the retained
record, never a live mutable parent workspace. Add
`postmortem.Report.SubagentWorkspaces []SubagentWorkspaceEvidence` with
owner/task identity, binding/candidate/target and execution/integration/cleanup
states; no full environment or duplicate full transcript.
Define `registry.SubagentWorkspaceEvidence` using the same primitive string
fields and JSON names as the wire type, plus `OwnerSessionID`. Define
`NoticeSubagentWorkspace = "subagent_workspace"` and
`(*ToolNotice).SubagentWorkspace() (SubagentWorkspaceEvidence,bool)` to decode
the live struct and persisted JSON forms as existing notices do. Registry must
not import the service, DB, or session to decode a notice. Join current retained state by task key for live
views; historical tool outcomes remain historical rather than being rewritten.

- [ ] **Step 1: Write the acceptance and projection tests.**

  | Test | Assertions |
  |---|---|
  | `TestParallelChildIntegrationJourney` | parent feature worktree commits interface, two scripted children genuinely overlap, return independent commits, exact-candidate reviewer runs, both integrate serially, combined tests pass, main/unrelated dirt unchanged |
  | `TestConflictCancelSharedAndRestartJourney` | conflict retains both branches; cancelled child preserves evidence; explicit shared writes stay in captured root; restart yields one receipt and zero model resurrection |
  | `TestFactoryWaitsForIntegrationAwareContinuation` | existing waiting-delegated/continuation stages observe child settlement, parent incorporates candidate reports and explicit integration outcome before factory final acceptance |
  | `TestWorkspaceProjectionUnknownAndCleanupPending` | optional legacy fields remain absent; known/unknown/not-integrated/cleanup-pending states survive ACP, bridge JSON and Dart parsing without success inference |
  | `TestPostmortemWorkspaceEvidence` | same durable task identity/candidate appears alongside command cwd evidence; no environments/secrets/full duplicate report bodies |

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/app ./internal/acp/service ./internal/postmortem ./web/bridge -run 'ParallelChildIntegrationJourney|ConflictCancelShared|FactoryWaitsForIntegrationAware|WorkspaceProjection|PostmortemWorkspace' -count=1`.
  Run `cd app && flutter test test/model/inspect_model_test.dart test/model/stack_store_test.dart` for unknown/
  optional-field parsing fixtures. Expected: missing fields and final journey.

- [ ] **Step 3: Implement additive projections and workflow wording.** Keep
  workspace evidence typed and independently defined across the ACP/bridge
  boundary. Reuse existing generic tool detail/report views; do not turn
  numeric-handle absence into a fake live child after restart. Preserve the
  current factory wait/continue owner and add only the evidence necessary for
  the same worker to make its explicit integration decision. The factory
  cannot itself merge child branches by interpreting a report.
  The existing Inspect model and TUI generic tool body append readable workspace,
  candidate and integration/cleanup facts to their existing output details;
  both use the optional typed payload and preserve legacy output unchanged.
  Add no new layout or screen. Cover pending/failed/unknown copy in those
  existing model/body tests, not just JSON round trips.

  Update the shipped controller brief: commit prerequisites, dispatch isolated
  independent tasks, await and inspect exact candidates, send source review to
  `base_commit`, integrate accepted candidates serially, validate the combined
  tree, then finish the outer task. Explain shared-mode limits, retained dirty
  failures and explicit discard. Regenerate Dart using
  `scripts/test.sh quick ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.
  Any existing UI copy needed to distinguish lifecycle states uses the kit
  and tokens and receives its existing component tests; model/UI boundaries
  remain intact.

- [ ] **Step 4: Verify the release gate.** Run the red commands, then
  `scripts/test.sh quick ./internal/subagentwork ./internal/worktree ./internal/db ./internal/app/... ./internal/agent/... ./internal/tools/... ./internal/acp/... ./internal/snapshot ./internal/postmortem ./web/bridge/... -count=1`.
  Run `scripts/test.sh quick ./internal/app ./internal/agent ./internal/subagentwork -race -run 'ParallelChildIntegrationJourney|ConflictCancelShared|Lifecycle|ConcurrentChildren' -count=1`.
  Run `cd app && flutter test test/boundary_test.dart test/model/inspect_model_test.dart test/model/stack_store_test.dart`,
  `CGO_ENABLED=1 go build ./cmd/marshal ./cmd/webbridge`, and
  `go vet ./internal/subagentwork ./internal/worktree ./internal/agent ./internal/app/...`.
  Expected: pass, including `TestWebIsStdlibOnly`, TUI client-seam checks,
  Dart currency and all new full-boundary regressions. If a UI component
  changed, also run its required design-system tests. Live-model/SSH/provider
  checks remain separate and opt-in.

- [ ] **Step 5: Commit.** Commit
  `feat: surface subagent workspace lifecycle and verify parent integration`.

## Completion evidence

Record actual verification commands/results and the commits for P1–P3 in the
PR. Complete the plan self-check against every specification acceptance row.
Request an independent whole-branch review before integration. Do not claim
that writing these plans implemented or tested the runtime behavior.
