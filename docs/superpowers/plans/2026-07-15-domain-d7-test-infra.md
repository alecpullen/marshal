# Domain D7 — Test Infrastructure Refactor

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Worktree:** `/Users/alecpullen/projects/coder-agent/.worktrees/domain-d-agent-runtime` (branch `feature/domain-d-agent-runtime`)

**Goal:** Resolve the three LOW-severity test-infrastructure findings (F-POL-92, F-POL-94, F-POL-96) from `docs/14-codebase-improvement-audit-2026-07-14.md`.

**Architecture:** Three independent mechanical refactors, ordered by risk (smallest first). Each lands in its own commit. The full test suite must pass after each commit.

**Tech Stack:** Go (stdlib only).

## Global Constraints

- Go version: 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter), but the tasks below touch pure-Go files only.
- Every code change MUST compile: run `go build ./...` after the implementation step of each task.
- Every test change MUST pass: run `go test ./internal/...` at the end of each task.
- Commit per task with the message specified.
- Do NOT change test logic — all changes are mechanical refactors.
- Work in the worktree: `/Users/alecpullen/projects/coder-agent/.worktrees/domain-d-agent-runtime`

---

## File Structure

Files modified or created by this plan:

- `internal/agent/steering.go` — Removed (SteeringProvider interface deleted).
- `internal/agent/runner.go` — Remove SteeringProvider field; inline calls to State.DrainSteering().
- `internal/agent/steering_test.go` — Replace fakeSteeringState with state.PushSteering().
- `internal/agent/atfile_test.go` — Remove `runner.SteeringProvider = state` (no longer needed).
- `internal/app/app.go` — Remove `runner.SteeringProvider = state`.
- `internal/agent/agenttest/provider.go` — NEW: ScriptedProvider (shared test stub).
- `internal/agent/agenttest/doc.go` — NEW: package doc comment.
- `internal/agent/runner_test.go` — Remove local scriptedProvider; import agenttest.ScriptedProvider; remove SteeringProvider test lines.
- `internal/agent/swarm/provider_test.go` — Remove local scriptedProvider; import agenttest.
- `internal/agent/swarm/orchestrator_test.go` — Update scriptedProvider refs to agenttest.
- `internal/agent/swarm/lock_test.go` — Update scriptedProvider refs to agenttest.
- `internal/agent/runner_testhelpers_test.go` — NEW: shared helpers for split test files.
- `internal/agent/runner_basic_test.go` — NEW: basic flow / execution tests.
- `internal/agent/runner_approval_test.go` — NEW: approval + policy tests.
- `internal/agent/runner_context_test.go` — NEW: context pack / memory tests.
- `internal/agent/runner_parallel_test.go` — NEW: parallel actions / write gate tests.
- `internal/agent/runner_parse_test.go` — NEW: parse failure / stall / repair tests.
- `internal/agent/runner_askuser_test.go` — NEW: ask user / question flow tests.
- `internal/agent/runner_hooks_test.go` — NEW: hook runner tests.
- `internal/agent/runner_misc_test.go` — NEW: remaining misc tests.

(Split files are created by `git mv` to preserve history where possible.)

---

## Task Dependencies

```
Task 1 (F-POL-94): SteeringProvider interface inlining
    ↓
Task 2 (F-POL-96): Extract agenttest package
    ↓
Task 3 (F-POL-92): Split runner_test.go
```

---

### Task 1 — F-POL-94: SteeringProvider interface inlining

**Files:**
- Modify: `internal/agent/steering.go`
- Modify: `internal/agent/runner.go`
- Modify: `internal/agent/steering_test.go`
- Modify: `internal/agent/atfile_test.go`
- Modify: `internal/app/app.go`

**Interfaces:**
- Consumes: existing `SteeringProvider` interface in `steering.go`, `session.State.DrainSteering()` method.
- Produces: The `SteeringProvider` interface and field are removed; the call site in `RunTask` calls `r.State.DrainSteering()` directly.

**Rationale:** The interface has one method and one call site. `session.State` already implements `DrainSteering()`, and the Runner already holds `State *session.State` (always set by `NewRunner`). The `SteeringProvider` indirection adds no value.

- [ ] **Step 1: Remove `steering.go` and update `runner.go`**

  Delete `internal/agent/steering.go` entirely (the whole file is the interface).

  In `internal/agent/runner.go`:

  1. Remove the `SteeringProvider SteeringProvider` field from the `Runner` struct.
  2. Remove the `r.SteeringProvider = other.SteeringProvider` line from `CopyFrom`.
  3. Replace the steering drain block (lines ~438-451):
     ```go
     // Before:
     if r.SteeringProvider != nil {
         var steeringPins []contextpack.FileSnippet
         for _, msg := range r.SteeringProvider.DrainSteering() {
     // After:
     {
         var steeringPins []contextpack.FileSnippet
         for _, msg := range r.State.DrainSteering() {
     ```
     Remove the guard — `r.State` is always set, and an empty queue is a no-op.

  4. Remove the field comment block.

- [ ] **Step 2: Update `steering_test.go`**

  Replace `fakeSteeringState` with direct `state.PushSteering()` calls:

  - Remove the `fakeSteeringState` type and its methods.
  - Instead of `runner.SteeringProvider = steering`, push the message directly onto the state:
    ```go
    state.PushSteering("also update the README")
    ```

- [ ] **Step 3: Update `atfile_test.go`**

  Remove `runner.SteeringProvider = state` — the Runner's `State` field is already set.

- [ ] **Step 4: Update `app.go`**

  Remove `runner.SteeringProvider = state` — the Runner's `State` field is already set.

- [ ] **Step 5: Build and test**

  ```bash
  CGO_ENABLED=1 go build ./...
  go test ./internal/agent/... -count=1
  ```

- [ ] **Step 6: Commit**

  ```
  git add -A && git commit -m "D7-T1: inline SteeringProvider interface onto session.State
  
  The SteeringProvider interface had one method (DrainSteering) and one
  call site. session.State already implements DrainSteering and the
  Runner already holds State *session.State. Remove the interface
  indirection: delete steering.go, remove the SteeringProvider field
  from Runner, and call r.State.DrainSteering() directly.
  
  Fixes F-POL-94."
  ```

---

### Task 2 — F-POL-96: Extract common test stubs to `internal/agent/agenttest`

**Files:**
- CREATE: `internal/agent/agenttest/doc.go`
- CREATE: `internal/agent/agenttest/provider.go`
- MODIFY: `internal/agent/runner_test.go`
- MODIFY: `internal/agent/swarm/provider_test.go`
- MODIFY: `internal/agent/swarm/orchestrator_test.go`
- MODIFY: `internal/agent/swarm/lock_test.go`

**Interfaces:**
- Consumes: `scriptedProvider` definitions in `runner_test.go` and `swarm/provider_test.go`.
- Produces: Shared `ScriptedProvider` in `agenttest` used by both packages.

**Rationale:** The `scriptedProvider` type is independently re-implemented in `runner_test.go` (full) and `swarm/provider_test.go` (simplified). Extract to an `internal` test-support package so both share one implementation.

- [ ] **Step 1: Create `internal/agent/agenttest/doc.go`**

  ```go
  // Package agenttest provides common test stubs for the agent package and
  // its subpackages (swarm, sdd). Shared fakes reduce duplication and
  // provide a single source of truth for test infrastructure.
  package agenttest
  ```

- [ ] **Step 2: Create `internal/agent/agenttest/provider.go`**

  Extract the full `scriptedProvider` from `runner_test.go` (with all fields: `responses`, `toolCalls`, `finishReasons`, `thinking`, `errs`, `usages`, `capabilities`, `onChat`, `requests`). Add a `sync.Mutex` for concurrent safety (required by swarm tests).

  Export all externally-accessed fields (`Calls`, `Requests`, `Capabilities`, etc.).

  ```go
  package agenttest
  
  import (
      "context"
      "sync"
      "marshal/internal/llm/schema"
  )
  
  // ScriptedProvider returns pre-canned responses in call order. Each call to
  // Chat consumes the next entry from responses/errs (whichever is non-empty
  // at that index); once the scripts run out, the last response is repeated.
  // Safe for concurrent Chat calls.
  type ScriptedProvider struct {
      mu            sync.Mutex
      Responses     []string
      ToolCalls     [][]schema.ToolCall
      FinishReasons []string
      Thinking      []string
      Errs          []error
      Usages        []*schema.TokenUsage
      Calls         int
      Requests      []schema.ChatRequest
      Capabilities  schema.ProviderCapabilities
      OnChat        func(idx int, req schema.ChatRequest)
  }
  
  func (p *ScriptedProvider) Name() string { return "scripted" }
  
  func (p *ScriptedProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
      return nil, nil
  }
  
  func (p *ScriptedProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
      return schema.EmbedResponse{}, nil
  }
  
  func (p *ScriptedProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
      return p.Capabilities
  }
  
  func (p *ScriptedProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
      p.mu.Lock()
      idx := p.Calls
      p.Requests = append(p.Requests, req)
      p.Calls++
      p.mu.Unlock()
  
      if p.OnChat != nil {
          p.OnChat(idx, req)
      }
  
      ch := make(chan schema.ChatEvent, 3)
      if idx < len(p.Thinking) && p.Thinking[idx] != "" {
          ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: p.Thinking[idx]}
      }
  
      if idx < len(p.Errs) && p.Errs[idx] != nil {
          ch <- schema.ChatEvent{Type: schema.ChatEventError, Err: p.Errs[idx]}
          close(ch)
          return ch, nil
      }
  
      content := ""
      switch {
      case idx < len(p.Responses):
          content = p.Responses[idx]
      case len(p.Responses) > 0:
          content = p.Responses[len(p.Responses)-1]
      }
      if content != "" {
          ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: content}
      }
      done := schema.ChatEvent{Type: schema.ChatEventDone}
      if idx < len(p.Usages) {
          done.Usage = p.Usages[idx]
      }
      if idx < len(p.ToolCalls) {
          done.ToolCalls = p.ToolCalls[idx]
      }
      if idx < len(p.FinishReasons) {
          done.FinishReason = p.FinishReasons[idx]
      }
      ch <- done
      close(ch)
      return ch, nil
  }
  ```

- [ ] **Step 3: Update `internal/agent/runner_test.go`**

  1. Remove the `scriptedProvider` type and all its methods (lines ~39-108).
  2. Add import: `"marshal/internal/agent/agenttest"`.
  3. Replace all `scriptedProvider` → `agenttest.ScriptedProvider`.
  4. Replace all `scriptedProvider{` → `agenttest.ScriptedProvider{`.
  5. Replace all `p.requests` → `p.Requests`.
  6. Replace all `p.calls` → `p.Calls`.
  7. Replace all `p.capabilities` → `p.Capabilities`.
  8. Replace all `p.responses` → `p.Responses`.
  9. Replace all `p.toolCalls` → `p.ToolCalls`.
  10. Replace all `p.finishReasons` → `p.FinishReasons`.
  11. Replace all `p.errs` → `p.Errs`.
  12. Replace all `p.thinking` → `p.Thinking`.
  13. Replace all `p.onChat` → `p.OnChat`.
  14. Replace all `p.usages` → `p.Usages`.

  (All the field names were lowercase; now they're exported from the agenttest package.)

- [ ] **Step 4: Update `internal/agent/swarm/provider_test.go`**

  Replace the entire file content with a thin re-export or just delete it. The local `scriptedProvider` is no longer needed — swarm tests import `agenttest.ScriptedProvider` directly.

  In `orchestrator_test.go` and `lock_test.go`, replace `&scriptedProvider{` with `&agenttest.ScriptedProvider{` and add the import.

- [ ] **Step 5: Build and test**

  ```bash
  CGO_ENABLED=1 go build ./...
  go test ./internal/agent/... -count=1
  ```

- [ ] **Step 6: Commit**

  ```
  git add -A && git commit -m "D7-T2: extract ScriptedProvider to internal/agent/agenttest
  
  The scriptedProvider test stub was independently re-implemented in
  runner_test.go and swarm/provider_test.go. Extract the full version
  (with toolCalls, finishReasons, thinking, errs, usages, onChat)
  to internal/agent/agenttest so both packages share one source of
  truth. Add sync.Mutex for concurrent safety (required by swarm tests).
  
  Fixes F-POL-96."
  ```

---

### Task 3 — F-POL-92: Split `runner_test.go` by concern

**Files:**
- MODIFY: `internal/agent/runner_test.go` — keep only shared helpers, tests that exercise basic runner construction and defaults
- CREATE (via `git mv` where possible): 7 new test files

**Grouping plan (tests grouped by concern):**

| New file | Tests moved | Concern |
|---|---|---|
| `runner_testhelpers_test.go` | No tests; shared types and helpers extracted from runner_test.go | Helpers that all split files share: `scriptedRouteResolver`, `fakeMemoryProvider`, `blockingProvider`, `recordingGate`, `staticResolver`, `fakeHookRunner`, `newTestState`, `scriptRepeats`, `answerPendingQuestion` |
| `runner_basic_test.go` | `TestRunnerDefaultsAreSensible`, `TestLengthTruncatedToolCalls...`, `TestChatOnceRoutesThinkingDeltas...`, `TestChatOnceEndsStreamingEvenOnProviderError...`, `TestRunAnswersQuestionWithoutToolCalls`, `TestRunExecutesAllowedToolCallThenAnswers`, `TestRunNativeToolCallFeedsRoleToolThenAnswers`, `TestRunNativeMultiCallBatchFeedsEachRoleToolInOrder`, `TestRunNativeAskUserFeedsAnswerAsRoleTool`, `TestRunNativeUnknownToolAnswersToolCallIDWithError`, `TestBuildToolDefinitionsOmitsAskUserForSwarmRoles`, `TestChatOnceTimesOutPerRequest`, `TestResolveRouteRaisesBudget...`, `TestResolveRouteConfigWindowRaisesBudget`, `TestNormalizeArgsIsStableAcrossKeyOrder`, `TestRunTaskReturnsCompletedTaskWithSummary`, `TestRunLoadsSkillViaToolCall`, `TestRunnerUsesConfiguredRoleInSystemPrompt`, `TestRunNativeEmptyResponses...`, `TestRunNativeEmptyThenAnswerWins`, `TestSecondTurnSeesFirstTurnHistory` | Basic runner execution, tool flows, routing, normalize args |
| `runner_approval_test.go` | `TestRunRequiresApprovalForShellRunAndRespectsApproval`, `TestRunnerReevaluatesPolicyAfterEditedArgs`, `TestRunnerReevaluatesDenyAfterValidEdit`, `TestRunnerNonShellToolApprovalAndJSONEditing` | Approval flow, policy evaluation, arg editing |
| `runner_context_test.go` | `TestRunRetriesOnProviderErrorThenSucceeds`, `TestMergeMemories...`, `TestRunFailsAfterExhaustingRetries`, `TestRunInjectsStoredContextPack`, `TestRunOmitsContextPackWhenEmpty`, `TestRunMergesMemoriesIntoContextPack...`, `TestRunWithoutMemoryProvider...`, `TestRunSwallowsMemoryProviderErrors...`, `TestRunAddsPlanToContextPackForActionCalls`, `TestRunAddsPlanToContextPackBeforeSnippetsAndToolOutput`, `TestRunPreservesContextPackSectionMetadata...`, `TestRunResolvesQuestionRouteAndUpdatesModel`, `TestRunAppliesRouteContextBudgetToExistingPack`, `TestRunAppliesRouteContextBudgetToMemoryOnlyPack`, `TestRunFallsBackToOriginalProviderAndModelAfterResolverError`, `TestRunCachesReadOnlyToolResults`, `TestRunSummarizesLargeToolResults`, `TestLoopCompactsViaSummaryWhenOverBudget`, `TestHistoryAfterRewindExcludesAbandonedBranch` | Context pack, memories, compression, history |
| `runner_parallel_test.go` | `TestRunExecutesParallelReadOnlyActions`, `TestWriteGateAcquiredForWriteToolsOnly`, `TestRunAllowsSustainedDistinctReadsBeforeAnswering`, `TestRunAllowsParallelReadBatchWithoutStalling`, `TestParallelActionsSerializesQuestionTools`, `TestRunDetectsRepeatedToolCalls`, `TestRepeatedToolCallGetsReminderInResult` | Parallel execution, write gate, caching, repeated calls |
| `runner_parse_test.go` | `TestParseFailuresDoNotConsumeToolIterations`, `TestSecondConsecutiveParseFailureEscalatesToRepair`, `TestSecondConsecutiveParseFailureEnablesJSONMode`, `TestPersistentMalformedOutputSalvagesWhenWorkExists`, `TestPersistentMalformedOutputFailsFastWithoutWork`, `TestHardStallAsksUserAndContinuesOnGuidance`, `TestHardStallFinalizesWhenUserDeclines`, `TestHardStallAutoFinalizesForNonGeneralRole` | Parse failures, stall/repair, malformed output |
| `runner_askuser_test.go` | `TestRunHandlesAskUserAction`, `TestRunAskUserDeclinedContinues`, `TestRunAskUserCancelledByContext`, `TestSwarmRolesCannotAskUser`, `TestRunAskUserDeclinedCountsAgainstBudget` | Question/ask user flow |
| `runner_hooks_test.go` | `TestPreToolUseHookBlocksPatch`, `TestPreToolUseRewriteReentersPolicy`, `TestPreToolUseHookErrorBlocksWhenFailClosed`, `TestTurnEndHookContinuesExactlyOnce`, `TestTurnEndHookDoesNotContinueTwice`, `TestTurnEndHookContinuesNativePath` | Hook runner (pre_tool_use, turn_end) |
| `runner_misc_test.go` | `TestExhaustionSalvagesInsteadOfFailing`, `TestExhaustionWithoutValidActionFailsHard`, `TestExhaustionSalvageFailureReturnsError`, `TestRunStopsAfterMaxToolIterationsWithoutFinalAnswer`, `TestRunnerSetsActivityDuringToolExecute`, `TestRunnerSetsActivityDuringApproval`, `TestRunnerChatOnceSetsThinkingActivity`, `TestRunnerSetsPlanAfterPlanningPhase`, `TestPlanningStepSkippedByDefault`, `TestPlanningStepRunsWhenPlanFirstEnabled`, `TestRunRejectsNonReadOnlyActions`, `TestRunnerSetsAndClearsActiveToolCall`, `TestRunnerMarksFinalAnswer` | Finalization, planning phase, activity tracking, tool call lifecycle |

- [ ] **Step 1: Extract helpers to `runner_testhelpers_test.go`**

  Create `internal/agent/runner_testhelpers_test.go` containing all shared types and helpers from `runner_test.go`:
  - `scriptedRouteResolver`
  - `fakeMemoryProvider`
  - `blockingProvider`
  - `recordingGate`
  - `staticResolver`
  - `fakeHookRunner`
  - `newTestState`
  - `scriptRepeats`
  - `answerPendingQuestion`

  Remove these from `runner_test.go`.

- [ ] **Step 2: Create the split test files**

  For each group in the table above, create a new file under `package agent` with:
  - A doc comment explaining the concern.
  - The imports from `runner_test.go` (plus `agenttest`).
  - The test functions from that group.
  - **IMPORTANT:** Do not change any test logic. Only adjust imports and remove now-redundant local definitions.

- [ ] **Step 3: Trim `runner_test.go`**

  After extracting all helpers and test groups, `runner_test.go` should contain only:
  - Its `package agent` declaration.
  - Its imports.
  - Any tests that naturally belong to "basic runner construction" — just `TestRunnerDefaultsAreSensible` (or none, if that was already moved to `runner_basic_test.go`).
  - If nothing remains, delete the file entirely.

  Since we use `git mv` to create each new file, Git will track the history properly.

- [ ] **Step 4: Build and test**

  ```bash
  CGO_ENABLED=1 go build ./...
  go test ./internal/agent/... -count=1
  ```

- [ ] **Step 5: Commit**

  ```
  git add -A && git commit -m "D7-T3: split runner_test.go by concern into 10 files
  
  runner_test.go was 3000+ lines / 130 KB. Split it into 1 helper file
  and 8 concern-specific files:
    - runner_testhelpers_test.go (shared types and helpers)
    - runner_basic_test.go (basic execution, tool flow, routing)
    - runner_approval_test.go (approval and policy)
    - runner_context_test.go (context pack, memories, compression)
    - runner_parallel_test.go (parallel actions, write gate, caching)
    - runner_parse_test.go (parse failures, stall/repair)
    - runner_askuser_test.go (ask user / question flow)
    - runner_hooks_test.go (pre_tool_use and turn_end hooks)
    - runner_misc_test.go (finalization, activity, planning)
  
  No test logic was changed — purely a mechanical file split.
  
  Fixes F-POL-92."
  ```

---

## Verification

After all tasks are committed:

```bash
CGO_ENABLED=1 go build ./...
go test ./... -count=1
```

If any test fails, diagnose and fix before marking the task complete.

## Rollback

If a task's changes break the build or tests, use `git checkout -- <files>` to undo and re-assess the approach.
