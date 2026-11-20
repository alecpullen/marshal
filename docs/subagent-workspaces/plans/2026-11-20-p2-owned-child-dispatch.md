# Owned Child Dispatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Default `agent.run` to an owned worktree, retain explicit shared mode, and return durable committed candidates with fixed actor bindings.

**Architecture:** A transport-independent workspace service allocates and inspects owned git worktrees, backed by owner-scoped SQLite records. Each child receives its own fixed session/tool binding while borrowing the conversation's command service and live policy. Dispatch completion and retained reports use the same stable task key.

**Tech Stack:** Go, SQLite through existing DB package, git worktrees and current setup/snapshot/command services; scripted model fixtures.

**Spec:** [Approved feature specification](../feature-spec.md), §§1–2, completion/retention portions of §§4–6. Requires completed P1; consume [shared contracts](README.md) verbatim.

## Global Constraints

- “Omitted means `"isolated"`; unknown or empty explicit values are rejected.”
- “The parent commits the changes its children need before dispatch.” This is instruction, not automatic staging or a whole-checkout cleanliness gate.
- “Shared children share files, not a mutable pointer to whichever checkout the parent currently uses.”
- “Dispatcher-bound children fail in their assigned workspace when it disappears.”
- “Children cannot call workspace rebinding, integration or disposal operations.”
- “Ordinary checks do not read developer config, credentials or running services.” Use `testenv.Run`, isolated auth and the production factory path.
- The TUI renders only; `web/` is standard-library-only. P3 owns lifecycle wire additions and parent integration tools.

## Review Focus

- Duplicate logical calls must reuse a task even when workspace creation succeeded before the reply was lost (Task 1 and Task 3).
- A parent already in a worktree must seed children from that worktree's commit, including reviewer `base_commit` overrides (Task 1).
- Shared-root switching must not redirect backups, diagnostics, snapshots or detached command rescans (Task 2).
- Native registration must not resurrect excluded tools or handlers that reload a wider registry (Task 2).
- Shutdown, setup failure, panic or partial completion must retain the candidate and settle the same end-gate work identity (Task 3).

---

## File structure and order

Create `internal/subagentwork/{types,store,service,inspect}.go` for ownership
without session/native imports. DB adapters belong in `internal/db`; factory
resource wiring belongs in `internal/app/subagent_workspaces.go`. Native tools
gain a distinct backup owner rather than overloading workspace state. The
existing `subagent.go` remains the tool entry point; put mode/record plumbing
in `internal/agent/subagent_workspace.go`.

### Task 1: Allocate durable owned workspaces from exact commits

**Files:** Create `internal/subagentwork/{types,store,service,inspect}.go` and
their `_test.go` files, `internal/subagentwork/main_test.go`, `internal/db/subagent_workspaces.go`,
`internal/db/subagent_workspaces_test.go`; modify `internal/db/migrations.go`,
`internal/worktree/git.go`; create `internal/worktree/identity.go`,
`internal/worktree/identity_test.go`. Add capability fixtures in
`internal/app/subagent_workspaces_test.go` and `internal/sandbox/container_test.go`;
modify `internal/tools/native/runner.go` and
`internal/sandbox/{restricted,passthrough,container}.go` for
`SupportsLinkedWorktrees() bool`. Extend `internal/worktree/worktree.go`
only where the existing creation/seeding seam requires it.

**Interfaces:** Produce README `Mode`, `Binding`, `Target`, `Verification`,
`Integration`, `Record`, `Store`, `PrepareRequest`, `Completion`, `Git`,
`NewService`, `Prepare`, `Complete`, `Inspect`, and the detected
`PrepareRequest.IsolatedSupported` / `native.LinkedWorktreeRunner` capability. Add `Record.SetupPlan
worktree.SetupPlan` for trusted hooks captured at allocation. Implement
`NewSubagentWorkspaceStore(database *DB) subagentwork.Store` inside package db. Provide
`subagentwork.NewMemoryStore() Store` for explicitly nonpersistent fixtures;
production persistent sessions must use the DB adapter.

- [ ] **Step 1: Write ownership and repository tests.**

  | Test | Assertions |
  |---|---|
  | `TestWorkspaceStoreOwnerAndInvocationIsolation` | same task spelling under different owners is isolated; duplicate owner/invocation returns `created == false`; stale CAS returns `ErrStaleRecord` |
  | `TestPrepareStartsAtActiveParentCommit` | main and parent worktree have different HEADs; `record.Binding.BaseSHA == parentHead`, child contains committed parent-only interface |
  | `TestPrepareExactReviewerBase` | explicit exact candidate SHA appears in child HEAD; `record.Target.Root == parentRoot` and target branch stay unchanged |
  | `TestPrepareDoesNotCopyOrCommitParentDirt` | unrelated parent modifications/untracked file remain byte-identical and absent from isolated child |
  | `TestPrepareDuplicateAndPartialCreation` | repeated invocation creates one branch; simulated failure after git add leaves an identifiable record and never allocates a second child |
  | `TestPrepareSharedAndUnsupportedModes` | nongit shared succeeds, nongit isolated refuses, shared with base override refuses, unknown mode refuses |
  | `TestPrepareRejectsUnsupportedLinkedWorktreeBackend` | current container capability refuses before git allocation/model start; restricted/passthrough permit host worktrees; zero capability does not imply support |
  | `TestInspectChecksRealRepositoryOwnership` | replaced branch, changed common directory or symlink pivot yields a recorded ownership error, not a candidate to mutate |
  | `TestCompletionKeepsPartialEvidence` | real candidate/commits/changed paths/dirty state are observed; failed/stopped/unknown outcome is not promoted to success |

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/subagentwork ./internal/db ./internal/worktree -run 'WorkspaceStore|Prepare|InspectChecks|CompletionKeeps' -count=1`.
  Expected: missing service/adapter or current project-root base mismatch.

- [ ] **Step 3: Implement the record and service.** Add a migration table
  `session_subagent_workspaces` with columns `owner_session_id TEXT`,
  `task_key TEXT`, `invocation_id TEXT`, `revision INTEGER`, `record_json TEXT`,
  primary key `(owner_session_id,task_key)`, unique owner/invocation, and owner
  foreign key to `agent_sessions`. Keep the whole record versioned at 1;
  reject unsupported versions rather than returning a blank record.

  `Create` is insert-if-absent; `CompareAndSwap` updates only the expected
  revision and returns the incremented record. Allocation writes its intent
  before git. Derive branch/path from stable task identity:
  `marshal/subagent/<task-key>` and the existing agent worktree directory,
  with task keys restricted to generated safe opaque text. Capture/recheck
  the parent checkout identity; resolve exact existing commit OIDs using
  argv and `^{commit}` verification. Use `EnsureWorktree`/seeding with the
  explicit base: git/seeding source is `req.ParentRoot`, storage directory
  comes from `worktree.AgentDir(req.ProjectRoot)`. Validate any reused path/branch against recorded identity,
  and retain setup plan/warnings. Do not infer ownership from a directory's
  location alone or auto-delete partial work. Shared mode records the captured
  root without git mutation. `Inspect` records true unknowns; `Complete`
  references the existing durable report message rather than duplicating its
  full body. Detect linked-worktree support in app from the actual backend and
  pass the trusted boolean into Prepare; current container isolation refuses
  rather than mounting extra git metadata or discovering failure after a model
  starts. No model call, command supervisor or transport is introduced.
  Implement README's instance-scoped record observer outside service/store
  locks, delivering deep copies only after a successful write.
  Detached parent HEAD is allowed for inspection/dispatch and recorded with
  an empty target branch; P3 integration refuses a detached target rather than
  selecting a branch. Test this with `TestPrepareDetachedParentIdentity`.
  The new package's `TestMain` calls `testenv.Run(m)`. Git fixtures set local
  test identity and never contact remotes or consult the developer's config.

- [ ] **Step 4: Run green.** Run the red command, then
  `scripts/test.sh quick ./internal/subagentwork ./internal/db ./internal/worktree -count=1`.
  Include reopen/migration coverage against a real temporary DB. Expected:
  existing work-view records remain readable and cross-owner lookup fails.

- [ ] **Step 5: Commit.** Commit
  `feat: retain owned subagent workspaces by stable task identity`.

### Task 2: Bind each production child to its own tools and resources

**Files:** Create `internal/app/subagent_workspaces.go`,
`internal/app/subagent_workspaces_test.go`; modify `internal/app/app.go`,
`internal/app/session/subagents.go`, `internal/agent/subagent.go`,
`internal/tools/native/{native,file}.go`, `internal/snapshot/rooted.go`.
Create `internal/tools/native/backup_owner_test.go`,
`internal/snapshot/bound_test.go`; extend
`internal/app/{app,subagent_settings}_test.go`,
`internal/agent/registry_scope_test.go` and native scope tests.

**Interfaces:** Add README `SubagentRequest.TaskKey/Binding/Target` and
`SubagentMeta.WorkKey`. Add `native.Options.BackupState *session.State`;
`(*toolSet).backupState() *session.State` falls back to its workspace state.
Add `(*snapshot.Rooted).Bound(root string) *snapshot.Rooted`, copying storage
policy/options while fixing the operation root. Add private
`bindSubagentWorkspace(parent, child *session.State, req agent.SubagentRequest,
opts native.Options) (native.Options,error)` in the new app file.
Produce `State.ApplySubagentWorkspace(record subagentwork.Record)` and
`State.SubagentWorkspaces() []subagentwork.Record`; snapshots are owner-scoped,
deep-copied and revision-ordered. Live subagent views join this metadata by
WorkKey; no second source of lifecycle truth is created.

- [ ] **Step 1: Write production-factory tests.** Build real parent/child
  registries; do not use the old empty-native-options factory shortcut.

  | Test | Assertions |
  |---|---|
  | `TestChildCannotRetargetParentOrSibling` | parent, child A and child B remain on their recorded roots; `workspace.worktree/finish` are absent; each write changes only its bound tree |
  | `TestSharedChildRetainsRootAfterParentSwitch` | child file/shell/authority/previews still use the original captured shared root |
  | `TestChildUndoAndSnapshotsUseBoundWorkspace` | isolated snapshot restore never changes parent/sibling; shared backup remains parent-visible while its recorded path stays fixed |
  | `TestChildLedgerDoesNotBorrowSiblingRead` | reading root-relative `file.go` in A does not authorize editing B's different copy; shared readers retain existing common-ledger behavior |
  | `TestChildDiagnosticsDoNotUseOtherRootLSP` | checker runs in child root; parent cached diagnostics are not reported as child validation |
  | `TestFinalChildRegistryCannotReloadExcludedTools` | lookup fails for every exclusion, even after native inheritance; no retained handler can load them indirectly |
  | `TestChildBoundRootRemovalFailsClosed` | removing assigned root returns a failure and creates no file in original project root |
  | `TestChildAuthorityStillTracksOwnerRevocation` | owner mode/System revocation remains effective under the child's fixed root |

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/app ./internal/tools/native ./internal/snapshot ./internal/agent -run 'ChildCannotRetarget|SharedChildRetains|ChildUndoAndSnapshots|ChildLedger|ChildDiagnostics|FinalChildRegistry|ChildBoundRootRemoval|ChildAuthorityStill' -count=1`.
  Expected: current shared mutable state and reintroduced question tool fail.

- [ ] **Step 3: Separate workspace and conversation resources.** Bind child
  state with `BindIsolatedWorkspace(req.Binding.Root)` before reading its
  authority. Both shared and isolated children use their own `WorkspaceState`
  and fixed `CommandDir`; only shared mode sets `BackupState` to the parent.
  Isolated ledgers use a stable namespace derived from owner/task key;
  shared mode reuses the parent's ledger and distinct actor identity. Audit
  backup consumers so none still use workspace ownership implicitly.

  Borrow the conversation command service; never allocate a second manager.
  Give snapshots an exact-root `Bound` view with unchanged limits/global
  maintenance. Child diagnostics use their bound root: do not borrow mutable
  parent LSP sync/index state; retain project-index symbols only under the
  explicitly project-scoped read contract. Use configured root-local checkers
  when no safe bound language-server lease exists. Add root-specific rescan
  routing for child command completion rather than rescanning whichever LSP
  the parent most recently selected.

  Apply `SubtaskScopeView` to the **fully assembled** registry, including
  `mode.request`, `tools.select`, rebinding/lifecycle tools and privileged
  execution in its exclusions. Point the child policy at that final registry.
  Capture immutable backup/snapshot paths in shared mode even if the parent
  later rebinds. Preserve copied MCP tools' existing session authority and
  child todo binding. Test fixtures with no production bindings must state
  their explicit shared binding instead of silently invoking a new fallback.

- [ ] **Step 4: Run green and races.** Run the red command and
  `scripts/test.sh quick ./internal/app ./internal/app/session ./internal/agent ./internal/tools/native ./internal/snapshot -count=1`.
  Then `scripts/test.sh quick ./internal/app ./internal/app/session -race -run 'ChildCannotRetarget|SharedChildRetains|ChildUndoAndSnapshots|ChildAuthorityStill' -count=1`.
  Expected: pass, including existing stop, approval forwarding and output
  isolation tests. Correct outdated tests that assumed child workspace tools
  intentionally mutate the parent.

- [ ] **Step 5: Commit.** Commit
  `fix: bind subagent tools authority and snapshots to one workspace`.

### Task 3: Cut over dispatch, retained lookup and parent instructions

**Files:** Create `internal/agent/subagent_workspace.go`,
`internal/agent/subagent_workspace_test.go`; modify
`internal/agent/{subagent,subagent_async,prompts}.go`,
`internal/app/{app,subagent_workspaces}.go`,
`internal/app/session/{subagents,subagent_reports}.go`;
extend `internal/agent/{subagent_async,subagent_settings}_test.go` and
`internal/app/subagent_workspaces_test.go`. Update
`internal/skills/builtin/{using-worktrees,dispatching-parallel-agents,marshal-executing-plans}.md`,
`docs/tools/delegation.md`, `docs/ARCHITECTURE.md`, and the existing
`docs/studio-fleet-factory/studio-worker-subagents-spec.md` current-behavior
paragraph with a link to this superseding workspace contract.

**Interfaces:** Produce README `WithSubagentWorkspaceService`,
`WithIsolatedWorktreeSupport`, and
`WithAwaitWorkspaces`. `agentRunArgs.Workspace *string` distinguishes omitted
from explicit empty; `BaseCommit string` carries `base_commit`.
`agentAwaitArgs.TaskKey string` adds a mutually exclusive retained lookup mode.
Keep live numeric handles and existing job/all/any semantics unchanged.

- [ ] **Step 1: Add dispatch and lifecycle tests.**

  | Test | Assertions |
  |---|---|
  | `TestRunWorkspaceModeValidation` | omitted mode is isolated; explicit shared works; empty/unknown or shared+base fail before allocation/provider calls |
  | `TestDuplicateRunDoesNotStartSecondChild` | same logical invocation returns its original receipt, one work key/worktree/card/end-gate item and one provider run; ambiguous prior start returns unknown, not another run |
  | `TestRunReceiptAndCompletionShareWorkspaceIdentity` | receipt, card, report and DB refer to the same preallocated key, roots, target and observed candidate |
  | `TestRunSetupFailureRetainsOwnedArtifacts` | denied/nonzero required hook yields failed dispatch with ownership evidence; no child model starts and no branch is silently deleted |
  | `TestRunPanicCancelAndPartialRetainCandidate` | all completion paths persist available evidence before announcing terminal status; execution outcome remains truthful |
  | `TestAwaitTaskKeySurvivesRestart` | owner can inspect retained report/candidate, live child is not revived, another owner cannot query it |
  | `TestConcurrentChildrenHaveIndependentCommits` | two barriers hold genuine concurrent children; same-path edits and git indexes stay distinct, original checkout stays unchanged |
  | `TestCommitFirstAndSharedInstructions` | shipped instructions require parent commits, allow isolated child commits, reserve shared commits to parent and explain explicit retained-branch integration before P3 |

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/agent ./internal/app ./internal/app/session -run 'RunWorkspace|DuplicateRun|RunReceipt|RunSetupFailure|RunPanicCancel|AwaitTaskKey|ConcurrentChildrenHave|CommitFirstAndShared' -count=1`.
  Expected: absent workspace arguments, lost task key or mutable-root failures.

- [ ] **Step 3: Implement dispatch and cutover in one task commit.** Allocate
  the task key before factory/worktree creation. Derive deduplication from the
  existing execution origin/logical invocation, never prompt text or model
  call IDs alone. Use store intent/CAS before admitting the child model run;
  duplicate requests observe a definite existing record or unknown start.
  Pass the recorded binding to the factory and preserve `meta.WorkKey` during
  registration. After registering the card/approval forwarding and stop group,
  run required setup under the child's bounded actor using P1's setup path,
  then invoke the model. A failed setup consumes no model tokens.

  All goroutine completion and panic paths persist report and `Complete`
  evidence before `FinishSubagent` releases waiters, while always releasing
  creation slots/end work. Detached commands remain conversation-owned and
  are not inferred terminal from model completion. Mark old active records
  interrupted/unknown on actual process restart; config reload must retain
  the existing child/record/service, not mistake it for a restart. Add retained
  key lookup without changing live waits. Wire one service/store instance into
  runtime generation rebuilds; do not use mutable package-global factories.

  Update the schema, tool descriptions, core prompt and shipped skills in the
  same commit as the omitted-mode default. Parent commits prerequisites and
  selects shared explicitly for small suitable tasks. Isolated children commit
  scoped work and return actual candidate evidence; shared children leave
  commits to the parent. Until P3, instructions describe retained branches and
  existing explicit git integration honestly, without naming unavailable new
  lifecycle tools. Existing user overrides cannot make registry-enforced
  forbidden operations available.

- [ ] **Step 4: Verify green.** Run the red command, then
  `scripts/test.sh quick ./internal/subagentwork ./internal/worktree ./internal/db ./internal/app/... ./internal/agent/... ./internal/tools/native ./internal/snapshot -count=1`.
  Race check: `scripts/test.sh quick ./internal/agent ./internal/app -race -run 'DuplicateRun|ConcurrentChildrenHave|RunPanicCancel' -count=1`.
  Expected: pass. The fixture parent is itself a real feature worktree and
  commits an interface before dispatch; unrelated dirt is preserved.

- [ ] **Step 5: Commit.** Commit
  `feat: default delegation to owned worktrees with explicit shared mode`.

## Handoff

P2 ends with retained workspace records and committed candidates, not a claim
that child execution changed the parent branch. Give P3 the actual P2 commit,
the README signatures and current store migration version. Failed or unknown
work remains inspectable; no cleanup is inferred from a finished model.
