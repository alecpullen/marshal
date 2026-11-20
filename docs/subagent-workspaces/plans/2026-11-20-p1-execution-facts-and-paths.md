# Execution Facts and Paths Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make tool resolution, command approval, process launch and workspace guidance agree on the actual workspace.

**Architecture:** Native tools prepare canonical execution facts through registry metadata. Policy and launch consume that preparation; a shared path resolver checks one resolved target against operation-specific root grants. Existing session authority and command ownership remain authoritative.

**Tech Stack:** Go, existing Bubble Tea/ACP runtime, SQLite-backed fixtures, git, existing sandbox backends; no new dependencies.

**Spec:** [Approved feature specification](../feature-spec.md), §§2–3 and applicable acceptance cases. Exact shared signatures: [plan contracts](README.md).

## Global Constraints

- “Never rewrite the cache-stable prompt prefix mid-turn.”
- “The TUI remains rendering-only, `web/` remains standard-library-only.”
- “Ordinary checks do not read developer config, credentials or running services.”
- “`scripts/test.sh` is the test entry point; use the affected packages and uncached runs.”
- “An absolute spelling alone does not require System.” Scope and explicit rules still apply.
- Keep ordinary shell timeout values as wait budgets; setup-hook deadlines retain their distinct meaning.
- This plan does not switch `agent.run` to isolated mode; P2 owns that cutover.

## Review Focus

- Container shell environment differs from its host launcher environment; policy must not infer image values from host variables (Task 2).
- A tester view strips command overrides; preparation must analyze the stripped command too (Task 2).
- A new file beneath a symlinked parent must be checked against its final permitted write root (Task 1).
- A parent switching roots during approval must cause re-evaluation, while an accepted launch retains its captured cwd (Task 3).
- Patch previews and repair hints must read the same worktree copy that file tools edit (Task 4).

---

## File structure and order

Task 1 owns `internal/pathutil/grants.go` and native path adapters. Task 2 owns
registry command metadata and backend environment capture. Task 3 joins that
metadata to policy/admission/launch, including setup. Task 4 updates prompts,
previews and acceptance checks. Execute sequentially: Tasks 2–4 consume earlier
interfaces. Extend focused files rather than adding more orchestration to
`internal/app/app.go`.

### Task 1: Resolve one target against explicit read and write grants

**Files:** Create `internal/pathutil/grants.go`, `grants_test.go`; modify
`internal/tools/native/{native,helpers,saferesolve,file,search}.go`,
`internal/tools/registry/{types,view}.go`, `internal/app/app.go` (pipeline root
grants); extend `internal/tools/native/{helpers,saferesolve,scope}_test.go` and
`internal/tools/registry/view_test.go`. Create
`internal/tools/native/workspace_paths_test.go`.

**Interfaces:** Consume existing `pathutil.ResolveAbsolute` and native System
and home-read exception checks. Produce `RootGrant`, `ResolveGrantedPath`,
`Options.PathGrants` and `Tool.ResolvePath` with the signatures in README.
Keep existing resolver entry points as adapters so callers migrate together.

- [ ] **Step 1: Write the failing tests.** Use real temp sibling worktrees;
  assertions below are the exact behavioral checks, using standard `testing`.

  | Test | Assertions |
  |---|---|
  | `TestResolveGrantedSiblingTarget` | `got == filepath.Join(project,".marshal/worktrees/b/new.txt")` when resolving `../b/new.txt` from `a` with an appropriate project grant |
  | `TestReadGrantDoesNotPermitWrite` | read succeeds; `errors.Is(writeErr, pathutil.ErrOutsideRoots)` |
  | `TestAbsoluteContainedWrite` | absolute and relative spelling resolve to the same canonical writable path without System |
  | `TestNewTailAndSymlinkGrant` | absent parent tail resolves under the permitted real root; a symlink into an ungranted root fails |
  | `TestSingleWorktreePrefixIsDiagnosed` | write returns a useful relative suggestion; `os.IsNotExist(statNestedErr)` |
  | `TestLiteralNestedWorktreePathSurvives` | a genuinely existing nested path remains addressable; no automatic rewriting |
  | `TestScopedViewsCannotEscapeWithAbsolutePath` | artifact/fallback views reject canonical targets outside their declared files/alias, including `@alias/../` and absolute spellings |

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/pathutil ./internal/tools/native ./internal/tools/registry -run 'Granted|GrantDoes|AbsoluteContained|NewTail|SingleWorktreePrefix|LiteralNested|ScopedViewsCannot' -count=1`
  must fail on the missing resolver or current incorrect resolution.

- [ ] **Step 3: Implement the resolver and adapters.** Define
  `pathutil.ErrOutsideRoots` and the README signatures. Select a named alias
  first when present, resolve one target from that root or primary, then check
  that target against the requested operation's canonical grants. Preserve
  nearest-existing-ancestor handling and Marshal-owned read exceptions. Add
  write-safe absolute paths; never turn project reference roots into implicit
  write roots. Resolve scoped-view checks through the tool's resolver rather
  than trusting raw lexical prefixes. Diagnose the joined duplicated path only
  when its literal entry does not exist. Set `ResolvePath` on file, search,
  image and relevant write tools through their existing constructors.

- [ ] **Step 4: Run green.** Run the red command, then
  `scripts/test.sh quick ./internal/pathutil ./internal/tools/native ./internal/tools/registry -count=1`.
  Expected: all pass, including existing home-read/symlink pivot and artifact
  scope tests. Update obsolete absolute-write rejection assertions only where
  the approved contained-path behavior changed.

- [ ] **Step 5: Commit.** Stage only this task's listed files and tests;
  commit `fix: resolve workspace targets against operation-specific grants`.

### Task 2: Capture the real command and backend environment

**Files:** Create `internal/tools/registry/command.go`, `command_test.go` and
`internal/tools/native/command_prepare.go`, `command_prepare_test.go`; modify
`internal/tools/registry/{types,registry,view}.go`,
`internal/tools/native/{native,command,runner,jobs_manager}.go`,
`internal/sandbox/{restricted,passthrough,container,execute}.go` and their
focused tests. Update command-service test fixtures in
`internal/tools/native/{admission,jobs,activeroot}_test.go` as needed.

**Interfaces:** Produce `CommandFacts`, `CommandPreparer`,
`Tool.PrepareCommand`, policy-compatible environment capture, and
`CommandRequest.Environment`/`LaunchSpec.Environment` from README. Introduce
`func (t *toolSet) prepareCommand(ctx context.Context, call registry.ToolCall)
(registry.CommandFacts,error)` in `command_prepare.go`.

- [ ] **Step 1: Write tests.**

  | Test | Assertions |
  |---|---|
  | `TestPrepareCommandCapturesActiveRoot` | `facts.Dir == facts.WorkspaceRoot == activeRoot`, including a bound dispatch and removed interactive root recovery |
  | `TestPrepareConfiguredTestCommand` | absent override gives the owner's current configured command; caller override is honored only by ordinary `test.run` |
  | `TestTesterPreparationIgnoresOverride` | both prepared bytes and handler bytes equal the configured command despite a supplied override |
  | `TestCommandMetadataSurvivesRegistryViews` | cloned tools retain preparation/resolution callbacks and copy returned slice/map facts |
  | `TestCapturedEnvironmentSurvivesAmbientChange` | change a fixture variable after capture; runner receives the captured value, not the new value |
  | `TestRestrictedCaptureUsesEffectiveAllowlist` | denied variables are absent from both captured environment and known analyzer facts |
  | `TestContainerCaptureSeparatesShellAndLauncher` | shell `-e` inputs use captured filtered values; host runtime-only variables are not advertised as known image shell values |
  | `TestEmptyCapturedEnvironmentIsNotInheritance` | non-nil empty environment stays empty; nil direct-run input retains legacy behavior |

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/tools/registry ./internal/tools/native ./internal/sandbox -run 'Prepare|Preparation|CommandMetadata|CapturedEnvironment|RestrictedCapture|ContainerCapture|EmptyCaptured' -count=1`.
  Expected: missing metadata/capture API failures.

- [ ] **Step 3: Implement capture.** Use the native toolset's actual command
  root, grants and test-command source. Validate/normalize the command once.
  `testerTestRunTool` wraps preparation as well as execution to strip its
  override. Production ordinary backends implement
  `CaptureCommandEnvironment`; restricted/container capture honors their
  existing filters, and container `buildArgs` consumes the captured shell
  environment without confusing it with `buildContainerEnv` for the runtime
  CLI. Local `PWD` facts correspond to the captured cwd. Unknown container or
  shell startup values stay unknown. Do not serialize full environments into
  messages, audits or fingerprints. Custom production runners without a safe
  capture implementation return an explicit unsupported preparation error;
  test fakes implement the interface or inject deliberate fixture values.

- [ ] **Step 4: Run green.** Run the red command and
  `scripts/test.sh quick ./internal/tools/registry ./internal/tools/native ./internal/sandbox -count=1`.
  Expected: pass without changing resource limits, network flags or the
  privileged runner's separate environment policy.

- [ ] **Step 5: Commit.** Stage the task files; commit
  `refactor: capture native command facts before authorization`.

### Task 3: Admit and launch exactly the captured facts

**Files:** Modify `internal/tools/policy/{shell_types,admission,evaluation}.go`,
`internal/agent/execute.go`, `internal/tools/native/{command,jobs_manager,workspace_setup}.go`,
`internal/app/app.go` (authorizer/setup wiring). Create
`internal/agent/command_facts_test.go`,
`internal/tools/native/command_facts_test.go`; extend
`internal/tools/native/{admission,workspace_setup}_test.go` and
`internal/tools/policy/admission_test.go`.

**Interfaces:** Consume Tasks 1–2. Alias `policy.ShellInput` to
`registry.CommandFacts`. Change the private `Runner.policyCall` to
`policyCall(ctx context.Context, invocationID, toolName string, args map[string]interface{}) (policy.Call,error)`;
all initial, edited and stale-retry callers propagate preparation errors.
Use `Tool.ResolvePath` to fill trusted `Call.Targets` for non-shell writes.

- [ ] **Step 1: Add full-boundary regressions.**

  | Test | Assertions |
  |---|---|
  | `TestApprovalAndLaunchFactsMatch` | real authorizer's evaluated facts, accepted attempt, recorded `CommandRequest`, and execution evidence share command/cwd; environment passed to runner equals preparation |
  | `TestParentRebindDuringApprovalRejectsStaleProof` | no old-proof launch after rebind; reevaluation prepares the new parent root |
  | `TestAcceptedFactsCannotBeRetargeted` | a later workspace/config/default change leaves accepted command and cwd unchanged |
  | `TestChildRevocationPreventsLaunch` | owner grant revocation between evaluation and admission yields no process start |
  | `TestAbsoluteGrantedWritePolicyAgreesWithTool` | permitted additional-root writes require no System; a read-only reference never becomes writable |
  | `TestSetupHooksUseBoundedAdmission` | exact hook cwd/environment are evaluated and launched identically; denied/unsupported hooks launch nothing; deadline stops only the hook execution |

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/agent ./internal/tools/native ./internal/tools/policy -run 'ApprovalAndLaunchFacts|RebindDuringApproval|AcceptedFacts|ChildRevocation|AbsoluteGrantedWritePolicy|SetupHooksUseBounded' -count=1`.
  Expected: current differing approval/launch cwd or missing preparation path.

- [ ] **Step 3: Join policy and launch.** `commandAttempt` takes a deep copy of
  proof-attached facts; native `LaunchSpec.Command`, `.Dir` and `.Environment`
  come from that attempt, not a new root read. Reject any mismatch between
  handler-normalized bytes and proof bytes. For direct approved internal
  callers, prepare once before evaluating. Teach non-shell policy to honor
  validated targets from the native resolver while preserving explicit rules
  and fail-closed behavior for unresolvable targets.

  Route setup hooks through prepared facts, the bounded actor's real
  authorizer and the conversation service. Add
  `RunSetupPlan(ctx context.Context, plan worktree.SetupPlan, reg
  *registry.Registry, authorizer *policy.Authorizer, jobs *JobManager)
  ([]string,error)` inside `internal/tools/native`. It runs the bound
  registry's `shell.run` preparation, admits
  only Allow or an already valid exact approval, and returns a clear refusal
  for missing capability/confirmation; it never fabricates approval. Preserve
  explicit hook deadlines by stopping/joining that hook's execution at its
  deadline rather than applying ordinary foreground wait semantics. Use an
  instance-scoped `Options.SetupAuthorizer *policy.Authorizer`; keep the
  privileged `CommandAuthorizer` gate separate. Existing interactive workspace
  hooks may render warnings; P2 dispatch treats required setup failure as a
  failed owned dispatch with retained artifacts. Do not create another manager.

- [ ] **Step 4: Verify green and races.** Run the red command, then
  `scripts/test.sh quick ./internal/agent ./internal/tools/native ./internal/tools/policy ./internal/sandbox ./internal/pipeline -count=1`.
  Run `scripts/test.sh quick ./internal/tools/native ./internal/tools/policy -race -run 'ApprovalAndLaunchFacts|RebindDuringApproval|AcceptedFacts|ChildRevocation' -count=1`.
  Expected: pass; no test uses a provider or ambient service.

- [ ] **Step 5: Commit.** Commit
  `fix: launch only the command facts accepted by admission`.

### Task 4: Tell the model and previews the actual workspace

**Files:** Modify `internal/agent/{route,prompts,requests,patch_hint,patch_preview}.go`,
`internal/agent/cacheprefix_test.go`, `internal/agent/prompts_test.go`,
`internal/agent/patch_preview_test.go`; create
`internal/agent/workspace_context_test.go`. Update
`docs/tools/{editing,execution,read-search,workspace-interaction}.md`.

**Interfaces:** Consume Task 1 `Tool.ResolvePath`. Add private
`(*Runner).workspaceStateMessage() schema.ChatMessage`, marked `Volatile`,
and `PreviewResolvedFilePatches(resolve func(string)(string,error), patches
[]patch.FilePatch) (string,error)`. Keep the existing preview wrapper for
callers with an explicit root.

- [ ] **Step 1: Write tests.** `TestInitialPromptNamesBoundWorkspace` asserts
  the initial root is the active worktree. `TestWorkspaceRebindOnlyChangesTail`
  asserts byte-identical prior prefix/history and one current volatile root
  message. `TestPreviewAndRetryHintUseBoundTarget` puts different bytes in
  root/worktree copies and asserts only the worktree bytes appear in preview
  and retry hint. `TestWorkspaceTailSurvivesCompactionAndRetry` asserts current
  root guidance without duplicate tails or replay into stored history.

- [ ] **Step 2: Run red.**
  `scripts/test.sh quick ./internal/agent -run 'InitialPromptNamesBound|WorkspaceRebindOnly|PreviewAndRetryHint|WorkspaceTailSurvives' -count=1`.
  Expected: immutable launch-root text or wrong-copy preview failures.

- [ ] **Step 3: Implement guidance and resolved previews.** Initial prompts
  use the actual binding. Append current root/reference grants in
  `withWorkingState` after history; do not add mutable data ahead of the cache
  anchor. Use the tool resolver for patch previews and repair hints, including
  aliases and permitted absolute paths. Failure to resolve a hint degrades
  without reading another same-named file. Update the tool docs to explain
  contained absolute paths and scoped references.

- [ ] **Step 4: Verify.** Run the red command, then
  `scripts/test.sh quick ./internal/agent -run 'PromptPrefix|CrossTurnStable|Cache|Workspace|Patch|Preview' -count=1`.
  Final P1 check: `scripts/test.sh quick ./internal/pathutil ./internal/tools/registry ./internal/tools/policy ./internal/tools/native ./internal/agent ./internal/app ./internal/pipeline ./internal/sandbox -count=1`.
  Expected: pass. These commands are verification instructions for execution,
  not claims that the planning-only change ran the Go suite.

- [ ] **Step 5: Commit.** Commit
  `fix: align workspace context and previews with tool bindings`.

## Handoff

P2 consumes the published README contracts, not inferred copies of P1 bodies.
Record the completed P1 commit in its execution brief. The delegation default
and child lifecycle remain for P2/P3; no current child is rebound by P1.
