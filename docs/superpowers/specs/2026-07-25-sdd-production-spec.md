# SDD Mode — Production Specification for Marshal

> **Status:** Design specification for replacing the existing SDD prototype.
>
> **Scope:** Replaces `internal/agent/sdd/` with a production implementation
> derived from the kilo-based SDD2 prototype pipeline. The existing design doc
> (`docs/superpowers/specs/2026-07-14-sdd-mode-design.md`) describes the
> prototype; this document describes the production version.

## Table of Contents

1. [Goals and non-goals](#1-goals-and-non-goals)
2. [Architecture overview](#2-architecture-overview)
3. [State machine](#3-state-machine)
4. [Data model](#4-data-model)
5. [Controller (deterministic code)](#5-controller-deterministic-code)
6. [Orchestrator (LLM-driven drain loop)](#6-orchestrator-llm-driven-drain-loop)
7. [Worker subagents](#7-worker-subagents)
8. [Tools exposed to LLMs](#8-tools-exposed-to-llms)
9. [Gates and guards](#9-gates-and-guards)
10. [Workspace lifecycle](#10-workspace-lifecycle)
11. [Worktree management](#11-worktree-management)
12. [Git operations](#12-git-operations)
13. [Progress logging](#13-progress-logging)
14. [Human gate points](#14-human-gate-points)
15. [Error recovery and escalation](#15-error-recovery-and-escalation)
16. [Model selection and routing](#16-model-selection-and-routing)
17. [Prompt management](#17-prompt-management)
18. [TUI integration](#18-tui-integration)
19. [Configuration](#19-configuration)
20. [File layout](#20-file-layout)
21. [Testing strategy](#21-testing-strategy)
22. [Migration from prototype](#22-migration-from-prototype)
23. [Unknowns](#23-unknowns)

---

## 1. Goals and non-goals

### Goals

- **Replace the existing SDD prototype** (`internal/agent/sdd/`) with a
  production implementation derived from 8+ postmortems of the kilo-based SDD2
  prototype pipeline.
- **Controller-orchestrator split**: a deterministic Go state machine (the
  controller) manages the pipeline, with LLMs only for the parts that need
  reasoning (orchestrator drain decisions, code implementation, review, audit).
- **Script-level enforcement** of all rules that prompt rules failed to enforce
  in the prototype (no-direct-edit, wrong-base merge, Allowed Files, review
  override, stale worktree, etc.).
- **Cost efficiency**: cheap cached model for the orchestrator drain loop,
  stronger model for the controller's escalation decisions, various models for
  workers. The controller's context is ~100K tokens (cached), not ~3M (uncached).
- **Parallel dispatch**: multiple workers in one orchestrator turn when the DAG
  has multiple ready tasks.
- **Full auditability**: every state transition, dispatch, merge, gate check,
  and escalation is logged to an append-only progress file.

### Non-goals

- **Not a general-purpose agent framework**: SDD is a specific pipeline for
  executing spec-driven multi-task implementation plans.
- **Not headless CLI**: the prototype was TUI-only; the production version
  keeps TUI-only for now. A headless mode is possible later but not in scope.
- **Not multi-repo**: SDD operates on a single git repository per run.
- **Not prompt-only enforcement**: every rule that can be enforced in Go code
  MUST be enforced in Go code. Prompt rules are documentation, not enforcement.

---

## 2. Architecture overview

```
User (via TUI)
  │
  ▼
Controller (Go code, deterministic state machine)
  ├── State management (dag.json, state/repo.json, progress.md)
  ├── Gate enforcement (audit, review, branch-base, edit, allowed-files)
  ├── Worktree lifecycle (create, rebase, cleanup)
  ├── Git operations (merge, finalize, checkpoint, rebranch)
  ├── Human gate presentation (spec approval, final merge, escalation)
  ├── Health monitoring (loop detection, stagnation, model escalation)
  │
  ├── dispatches (per drain iteration) ──► Orchestrator (LLM, cheap cached model)
  │     ├── Reads state-dump (Go-provided JSON)
  │     ├── Dispatches workers in parallel (task tool calls)
  │     ├── Runs gates (Go-provided tools)
  │     ├── Merges completed tasks (Go-provided tool)
  │     └── Returns structured report to controller
  │
  └── dispatches (on escalation) ──► Worker subagents (LLMs, various models)
        ├── sdd-implementer (code writing, full tool access)
        ├── sdd-auditor (static analysis, read-only)
        ├── sdd-reviewer (spec compliance + quality, read-only)
        ├── sdd-branch-reviewer (whole-branch review, read-only)
        ├── sdd-investigator (failure diagnosis, read-only)
        └── sdd-rescue (orchestrator recovery, read-only)
```

### Key difference from prototype

The prototype had the orchestrator as the top-level agent that:
- Talked to the user directly
- Made all strategic decisions (spec gate, branch correction, model swaps)
- Ran the drain loop indefinitely
- Was responsible for all rule enforcement via prompt rules

The production version splits this into:
- **Controller** (Go code): strategic decisions, rule enforcement, state
  management, human gates, escalation handling. Deterministic, no LLM needed
  for most operations. Uses an LLM only for ambiguous escalation decisions.
- **Orchestrator** (LLM): runs one drain iteration, returns a structured
  report, stops. The controller re-dispatches it for the next iteration.

---

## 3. State machine

The controller implements a state machine with these states:

```
                    ┌─────────┐
                    │  IDLE   │
                    └────┬────┘
                         │ /sdd <plan>
                         ▼
                ┌─────────────────┐
                │ WORKSPACE_RESET │
                └────────┬────────┘
                         │
                         ▼
                ┌─────────────────┐
                │   SPEC_GATE     │◄──── human approves spec
                └────────┬────────┘
                         │
                         ▼
                ┌─────────────────┐
                │   DECOMPOSE     │
                └────────┬────────┘
                         │
                         ▼
              ┌────────────────────┐
              │    DRAIN_ITERATION  │◄──────────────────┐
              └──────────┬─────────┘                    │
                         │                              │
                    next_action?                        │
                   ╱           ╲                        │
              drain          branch_review              │
                 │               │                     │
                 ▼               ▼                     │
    ┌────────────────┐  ┌──────────────┐               │
    │ DISPATCH_WORKERS│  │BRANCH_REVIEW │               │
    └───────┬────────┘  └──────┬───────┘               │
            │                  │                        │
            ▼                  │ PASS                   │
    ┌──────────────┐           │                        │
    │ VERIFY_AUDIT │           ▼                        │
    └──────┬───────┘    ┌──────────────┐                │
           │            │ FINAL_MERGE   │                │
           ▼            │ GATE (human)  │                │
    ┌──────────────┐    └──────┬───────┘                │
    │ REVIEW_MERGE │           │                        │
    └──────┬───────┘           │ confirm                │
           │                   ▼                        │
           │             ┌─────────┐                    │
           │             │ FINALIZE│                    │
           │             └─────────┘                    │
           │                                            │
           ├──── all merged? ──── yes ──► branch_review │
           │                                            │
           └──── not done ─────────────────────────────►│
                                                        │
              ┌──────────────────┐                      │
              │   BLOCKED        │──── resolved ────────►
              │   (escalation)   │
              └──────────────────┘
```

### State transitions

| From | To | Trigger |
|------|-----|---------|
| IDLE | WORKSPACE_RESET | `/sdd <plan>` command |
| WORKSPACE_RESET | SPEC_GATE | Reset complete |
| SPEC_GATE | DECOMPOSE | Spec approved by human |
| SPEC_GATE | IDLE | Spec rejected |
| DECOMPOSE | DRAIN_ITERATION | DAG extracted, contracts created |
| DRAIN_ITERATION | DISPATCH_WORKERS | `next_action == drain` |
| DRAIN_ITERATION | BRANCH_REVIEW | `next_action == branch_review` (all tasks merged) |
| DRAIN_ITERATION | BLOCKED | Escalation from orchestrator |
| DISPATCH_WORKERS | VERIFY_AUDIT | All workers returned |
| VERIFY_AUDIT | REVIEW_MERGE | Audit passed (or skipped) |
| VERIFY_AUDIT | BLOCKED | Audit failed, retry exhausted |
| REVIEW_MERGE | DRAIN_ITERATION | Task merged, more tasks remain |
| REVIEW_MERGE | BRANCH_REVIEW | Last task merged |
| REVIEW_MERGE | BLOCKED | Review failed, retry exhausted |
| BRANCH_REVIEW | FINAL_MERGE_GATE | Branch review PASS |
| BRANCH_REVIEW | BLOCKED | Branch review FAIL |
| FINAL_MERGE_GATE | FINALIZE | Human confirms |
| FINAL_MERGE_GATE | IDLE | Human cancels |
| BLOCKED | DRAIN_ITERATION | Escalation resolved |
| BLOCKED | IDLE | Human cancels |

---

## 4. Data model

### `dag.json`

The task graph. Stored at `.marshal/sdd/dag.json`.

```go
type DAG struct {
    SpecPath string     `json:"spec_path"`
    Tasks    []DAGTask  `json:"tasks"`
}

type DAGTask struct {
    ID           string   `json:"id"`             // "T1", "T2", etc.
    Title        string   `json:"title"`
    Status       string   `json:"status"`         // pending|in_progress|merged|blocked
    Deps         []string `json:"deps"`            // task IDs this depends on
    Files        []string `json:"files"`           // allowed files for this task
    Acceptance   []string `json:"acceptance"`      // acceptance criteria
    Base         string   `json:"base,omitempty"`  // base SHA when worktree was created
    WorktreePath string   `json:"worktree,omitempty"`
    Branch       string   `json:"branch,omitempty"` // sdd/T1
    ReviewedHead string   `json:"reviewed_head,omitempty"`
    Review       *bool    `json:"review,omitempty"` // override: true=required, false=deferred
}
```

### `state/repo.json`

Pipeline branch state. Stored at `.marshal/sdd/state/repo.json`.

```go
type RepoState struct {
    Branch       string            `json:"branch"`         // pipeline branch
    TargetBranch string            `json:"target_branch"`  // integration target
    Head         string            `json:"head"`           // last known pipeline HEAD
    Merged       []string          `json:"merged"`         // merged task IDs
    MergedAt     map[string]string `json:"merged_at"`      // task ID → ISO timestamp
    LastMergeAt  string            `json:"last_merge_at,omitempty"`
}
```

### `progress.md`

Append-only event log. One line per event:

```
<ISO_TIMESTAMP> <TASK_ID> <EVENT> key=value key=value ...
```

Events: `DECOMPOSE`, `DISPATCH_IMPL`, `DISPATCH_AUDIT`, `DISPATCH_REVIEW`,
`DISPATCH_BATCH`, `VERIFY_PASS`, `VERIFY_FAIL`, `AUDIT_PASS`, `AUDIT_FAIL`,
`AUDIT_SKIP`, `AUDIT_MALFORMED`, `REVIEW_PASS`, `REVIEW_FAIL`,
`REVIEW_MALFORMED`, `REVIEW_SKIP`, `REVIEW_STATE`, `REVIEW_DEFERRED`,
`MERGED`, `BLOCKED`, `RETRY`, `ROLLBACK`, `INTEGRATED`, `MODEL`,
`THINKING_ESCAPE`, `BRANCH_REVIEW`, `TELEMETRY`, `ORCHESTRATOR_HEALTH`,
`RESCUE`, `SYSTEM`.

### Checkpoint files

Stored at `.marshal/sdd/checkpoints/<short-sha>.json`:

```go
type Checkpoint struct {
    Tag       string            `json:"tag"`
    SHA       string            `json:"sha"`
    Timestamp string            `json:"ts"`
    Branch    string            `json:"branch"`
    Merged    []string          `json:"merged"`
    MergedAt  map[string]string `json:"merged_at"`
    Message   string            `json:"message"`
}
```

### Spec (`spec.md`)

Stored at `.marshal/sdd/spec.md`. Frontmatter:

```yaml
---
status: draft|approved
source_plan: <path-to-plan>
target_branch: main
---
```

Body contains a fenced ```yaml block with the `tasks:` list.

### Contracts

Stored at `.marshal/sdd/contracts/<T>.md`. Required H2 sections:
`## Knowledge Bundle`, `## Task`, `## Allowed Files`, `## Allowed Symbols`,
`## Acceptance Criteria`, `## Constraints`, `## Code Examples`. Plus optional
`## Dependencies` and `## Detailed Instructions`.

### Reports

Stored at `.marshal/sdd/reports/`:
- `<T>.md` — implementer report
- `<T>-audit.md` — auditor report
- `<T>-review.md` — per-task reviewer report
- `<T>-investigation.md` — investigator report
- `branch.md` — branch reviewer report
- `orchestrator-rescue.md` — rescue subagent report
- `orchestrator-rescue-bundle.md` — evidence bundle for rescue

All reports must start with `status: <STATUS>` on the first line.

---

## 5. Controller (deterministic code)

The controller is the top-level state machine. It lives in
`internal/sdd/controller.go` and replaces the current `sdd.Orchestrator`.

### Responsibilities

1. **Workspace lifecycle**: reset at startup, create directory structure,
   archive previous runs.
2. **Spec gate**: present spec to human, refuse to proceed without approval.
   Never auto-approve.
3. **Decomposition**: parse plan, extract DAG, create contracts, init state.
4. **Drain iteration dispatch**: call the orchestrator LLM for each drain
   iteration, parse its structured report, decide next action.
5. **Gate enforcement**: run audit-gate, review-gate, branch-base-guard,
   edit-guard, allowed-files check — all in Go code, not via prompt rules.
6. **Git operations**: worktree creation, task merge, checkpoint, finalize,
   rebranch.
7. **Health monitoring**: detect orchestrator loops, stagnation, model
   degradation.
8. **Escalation handling**: when the orchestrator returns BLOCKED or
   HEALTH_ALERT, the controller decides: fix deterministically (run a script),
   dispatch rescue, or ask the human.
9. **Human gates**: spec approval, final merge confirmation, branch correction,
   escalation presentation.
10. **Progress logging**: every state transition and gate result is logged to
    `progress.md`.

### What the controller does NOT do

- Write or edit source code (that's the implementer's job)
- Make subjective code quality judgments (that's the reviewer's job)
- Run indefinitely without checking state (the orchestrator runs one batch,
  returns, the controller re-dispatches)

### Controller-orchestrator protocol

The controller dispatches the orchestrator for each drain iteration with:
- Current `state-dump` JSON (task statuses, ready tasks, merged tasks, HEAD)
- The spec path, dag.json path, workspace path
- Instruction: "run one drain iteration and return a structured report"

The orchestrator returns:
```
status: DONE | BLOCKED | NEEDS_HUMAN | HEALTH_ALERT
batch_id: N
dispatched: [T2, T3]
merged: [T1, T4]
blocked_tasks:
  - task: T5
    reason: REBASE_CONFLICT
    detail: sdd/T5 cannot rebase onto sdd/feature
    files_in_conflict: [internal/foo.go]
health_alerts: []
edit_guard: clean
next_action: drain | branch_review | surface_blockers
```

The controller reads this report, executes any deterministic fixes needed,
and either re-dispatches the orchestrator or transitions to the next state.

### [UNKNOWN] Orchestrator dispatch mechanism

The controller needs to dispatch the orchestrator as an LLM call. The existing
marshal code uses `RunnerFactory` to build a fresh `agent.Runner` per role
dispatch, and `runner.RunTask(ctx, prompt)` to execute. The controller would
need either:

1. **A new SDD orchestrator role** (`RoleSDDOrchestrator`) that runs the drain
   loop for one iteration and returns the structured report. The controller
   calls it via `RunnerFactory` like any other role.
2. **Direct LLM call** without the full agent runner (lighter, but loses tool
   access).

Option 1 is consistent with the existing architecture but means the
orchestrator has full tool access (which it needs — it dispatches workers via
the `agent.run` tool). The orchestrator would be a new role in
`internal/llm/routing/types.go`.

**[UNKNOWN]**: Can the existing `agent.run` subagent tool be used by the
orchestrator to dispatch workers, or does the controller need to dispatch
workers directly? The prototype had the orchestrator dispatch workers via the
`task` tool. In marshal, the equivalent is `agent.run`. The orchestrator role
would need `ScopeFull` (not `ScopeReadOnly`) to use `agent.run`.

---

## 6. Orchestrator (LLM-driven drain loop)

The orchestrator is an LLM that runs one drain iteration per dispatch. Its
prompt is ~2-3K tokens (drain loop instructions only), not the 35K-token
prototype prompt.

### Per-iteration flow

1. Read `state-dump` (provided by controller as context)
2. Run `edit-guard` (Go tool — checks for source file edits since last iteration)
3. Run `orchestrator-health` (Go tool — checks for loops/stagnation)
4. Update TODO list (using a Go-provided tool, not the kilo `todowrite`)
5. Find ready tasks (from `state-dump`)
6. Create worktrees just-in-time (Go tool — creates from current pipeline HEAD)
7. Generate/validate contracts (Go tool — extracts from spec, validates)
8. Dispatch workers in parallel (via `agent.run` tool calls, all in one response)
9. On worker return: normalize report, validate report, run verify-task
10. Run audit-gate, review-state, review-guard (Go tools)
11. Merge completed tasks (Go tool — fast-forwards pipeline branch)
12. Return structured report to controller

### What the orchestrator does NOT do

- Talk to the user (the controller handles human interaction)
- Decide on branch corrections (the controller handles that)
- Approve specs (the controller handles the spec gate)
- Loop indefinitely (it runs one iteration and returns)
- Edit source files (enforced by Go code — the orchestrator's edit/write is
  restricted to `.marshal/sdd/` paths)

### [UNKNOWN] Orchestrator context management

Each drain iteration is a fresh `RunTask` call with no prior conversation
history (matching the existing SDD role behavior where role runners don't get
prior history replayed). The orchestrator gets:
- System prompt (drain loop rules, ~2-3K tokens)
- Context pack (from `session.State.ContextPack()`)
- The dispatch prompt (state-dump JSON + instruction)

This means the orchestrator's context resets each iteration, which is
desirable — no context growth, no compaction, no rule decay. But it also means
the orchestrator can't remember what it did in the previous iteration. The
`progress.md` file and `state-dump` JSON are its memory.

**[UNKNOWN]**: Does `runner.RunTask` support a "context pack" that includes
arbitrary structured data (like the state-dump JSON), or does it only support
`@file` references and the existing context pack mechanism? The state-dump
could be written to a file and referenced, or passed inline in the prompt.

---

## 7. Worker subagents

### sdd-implementer

- **Role**: `RoleSDDImplementer`
- **Model tier**: fast (cheap, high step count)
- **Scope**: `ScopeFull` (can edit/write files, run bash, use all tools)
- **Input**: contract path, worktree path, report output path
- **Output**: writes report to `<ws>/reports/<T>.md` with `status: DONE|BLOCKED|NEEDS_CONTEXT`
- **Must**: work only in the assigned worktree, commit changes, run tests
- **Must not**: edit files outside the contract's Allowed Files (enforced by
  verify-task check in Go)

### sdd-auditor

- **Role**: `RoleSDDAuditor` (new — not in prototype)
- **Model tier**: coder
- **Scope**: `ScopeReadOnly`
- **Input**: worktree path, contract path, allowed files list
- **Output**: writes report to `<ws>/reports/<T>-audit.md` with `status: PASS|FAIL`
- **Must**: check for undefined symbols, unused imports, naming, error wrapping
- **Must not**: edit any files

### sdd-reviewer

- **Role**: `RoleSDDReviewer`
- **Model tier**: coder/pro
- **Scope**: `ScopeReadOnly`
- **Input**: contract path, implementer report path, diff path, audit report
- **Output**: writes report to `<ws>/reports/<T>-review.md` with `status: PASS|FAIL`
- **Must**: include file:line references for every finding
- **Must not**: edit any files

### sdd-branch-reviewer

- **Role**: `RoleSDDBranchReviewer`
- **Model tier**: strong
- **Scope**: `ScopeReadOnly`
- **Input**: branch package path, spec path, plan path
- **Output**: writes report to `<ws>/reports/branch.md` with `status: PASS|FAIL`
- **Must**: attribute every finding to a specific task or file path
- **Must not**: edit any files

### sdd-investigator

- **Role**: `RoleSDDInvestigator` (new — not in prototype)
- **Model tier**: coder
- **Scope**: `ScopeReadOnly` + bash (for diagnostics)
- **Input**: failed task context, previous report, verify log, reviewer findings
- **Output**: writes report to `<ws>/reports/<T>-investigation.md`
- **Must**: diagnose root cause and recommend exactly one repair strategy

### sdd-rescue

- **Role**: `RoleSDDRescue` (new — not in prototype)
- **Model tier**: strong
- **Scope**: `ScopeReadOnly` + bash (for git diagnostics)
- **Input**: evidence bundle (dag.json, state, progress.md, relevant reports)
- **Output**: writes report to `<ws>/reports/orchestrator-rescue.md`
- **Must**: diagnose why the orchestrator is stuck and recommend one corrective
  action. Can recommend `MODEL_ESCALATION`.

### [UNKNOWN] Auditor role

The prototype SDD had 3 roles (implementer, reviewer, branch-reviewer). The
SDD2 prototype added an auditor, investigator, and rescue subagent. The
production version needs all 6. The auditor, investigator, and rescue roles
need to be added to `internal/llm/routing/types.go` and
`internal/agent/prompts.go` with appropriate role addenda.

---

## 8. Tools exposed to LLMs

The controller and orchestrator need tools that the prototype implemented as
bash scripts. In the production version, these are Go functions exposed as
native tools via the existing `registry` system.

### Tools the orchestrator calls (LLM-exposed)

| Tool | Purpose | Script equivalent |
|------|---------|-------------------|
| `sdd.state_dump` | Get current pipeline state as JSON | `state-dump` |
| `sdd.edit_guard` | Check for source file edits | `sdd2-edit-guard` |
| `sdd.health` | Check for loops/stagnation | `sdd2-orchestrator-health` |
| `sdd.worktree` | Create/rebase a task worktree | `task-worktree` |
| `sdd.contract` | Extract a task contract | `sdd2-contract extract` |
| `sdd.validate_contract` | Validate a contract | `validate-contract` |
| `sdd.normalize_report` | Fix report status lines | `sdd2-normalize-report` |
| `sdd.validate_report` | Validate a report | `validate-report` |
| `sdd.verify` | Run build/lint/test checks | `verify-task` |
| `sdd.audit_gate` | Check audit status before reviewer | `sdd2-audit-gate` |
| `sdd.review_state` | Decide if review is needed | `sdd2-review-state` |
| `sdd.review_guard` | Validate review, detect overrides | `sdd2-review-guard` |
| `sdd.merge` | Merge a task branch into pipeline | `task-merge` |
| `sdd.commit` | Commit staged changes in worktree | `task-commit` |
| `sdd.prepare_retry` | Assemble retry contract | `prepare-retry` |
| `sdd.branch_package` | Assemble branch review package | `branch-package` |
| `sdd.rescue_bundle` | Assemble evidence for rescue | `sdd2-rescue-bundle` |
| `sdd.todo` | Update TODO list | `todowrite` (kilo) |
| `agent.run` | Dispatch a worker subagent | `task` (kilo) |

### Tools the controller calls directly (Go code, not LLM-exposed)

| Function | Purpose | Script equivalent |
|----------|---------|-------------------|
| `Workspace.Reset()` | Archive + clear workspace | `sdd2-workspace-reset` |
| `State.Repair()` | Sync state head + merged array | `state-repair` |
| `Cleanup.Stale()` | Remove stale worktrees/branches | `task-cleanup` |
| `Checkpoint.Create()` | Create a checkpoint | `task-checkpoint` |
| `Branch.Rebranch()` | Move pipeline branch | `task-rebranch` |
| `Finalize.Confirm()` | Fast-forward target | `task-finalize` |
| `DAG.Update()` | Update task status/deps | `dag-update` |
| `DAG.Stats()` | DAG statistics | `dag-stats` |
| `Progress.Append()` | Log an event | `sdd2-progress append` |

### [UNKNOWN] Tool registration

The existing marshal tool registry (`internal/tools/registry`) registers tools
via `native.RegisterAll(reg, opts)`. SDD-specific tools would need a new
registration function (e.g., `sdd.RegisterTools(reg, deps)`) that registers
the SDD tools with the appropriate scope (full vs read-only). The tools need
access to:
- The workspace path
- The git repo root
- The DAG and state structs
- The progress logger

**[UNKNOWN]**: How does the existing tool registry handle tools that need
runtime state (like the workspace path)? The native tools seem to get their
dependencies via `nativeOpts` (a struct passed to `RegisterAll`). SDD tools
would need a similar `SDDToolOpts` struct.

---

## 9. Gates and guards

All gates are Go functions called by the controller or orchestrator. They log
to `progress.md` and return a typed result.

### Audit gate

- **Called after**: implementer returns, before reviewer dispatch
- **Logic**: if no audit report exists, BLOCK (require auditor dispatch first).
  If audit report is malformed, run normalize, then BLOCK if still malformed.
  If audit FAIL, block reviewer dispatch.
- **Combined-dispatch mode**: after ≥5 audits with fail rate <0.05, allow
  audit+review in one dispatch (skip the gate).

### Review state

- **Called after**: audit gate passes
- **Logic**: compare current HEAD against the task's reviewed_head. If trivial
  diff (≤N lines, ≤2 files, non-core), skip review (return REVIEW_SKIP). If
  first pass and trivial diff, also skip. Otherwise return REVIEW_REQUIRED.
- **Result**: REVIEW_REQUIRED (dispatch reviewer) or REVIEW_SKIP (defer to
  branch review)

### Review guard

- **Called after**: reviewer returns
- **Logic**: normalize report, validate report, read verdict. If PASS follows a
  prior REVIEW_FAIL (actual verdict, not malformed) without intervening RETRY,
  reject as ORCHESTRATOR_OVERRIDE.
- **Result**: ACCEPT (log REVIEW_PASS/REVIEW_FAIL) or REJECT (log BLOCKED)

### Branch-base guard

- **Called before**: task-merge and task-finalize
- **Logic**: verify the pipeline branch is not behind the target branch. If the
  pipeline branch doesn't contain the target's HEAD, refuse the merge.
- **Result**: BASE_OK or WRONG_BASE

### Edit guard

- **Called at**: start of each drain iteration
- **Logic**: check the pipeline branch checkout and all task worktrees for
  source file modifications. The orchestrator should never edit source files.
- **Result**: clean or ORCHESTRATOR_EDIT (with file list)

### Allowed files check

- **Called during**: verify-task
- **Logic**: compare the actual diff against the contract's Allowed Files list.
  If any file outside the list was modified, fail with ALLOWED_FILES_VIOLATION.
- **Result**: PASS or FAIL (with violation list)

### [UNKNOWN] Edit guard enforcement

In the kilo prototype, the edit guard was detective (detected after the fact).
In marshal, the controller can potentially **prevent** edits by restricting the
orchestrator's tool scope. The existing `registry.DenylistView` or a custom
scope view could deny `file.edit` and `file.write` for paths outside
`.marshal/sdd/`. However, the prototype showed that permission blocks cascade
to subagents — the orchestrator's deny must not affect the implementer's allow.

**[UNKNOWN]**: Does marshal's registry scope system support per-path
permission rules (allow `file.edit` for `.marshal/sdd/**` but deny for
everything else), or is it all-or-nothing per tool? The existing
`DenylistView` denies entire tools, not specific paths. A custom scope view
might be needed.

---

## 10. Workspace lifecycle

### Directory structure

```
.marshal/sdd/
  .gitignore              — "*\n" (ignore all)
  spec.md                 — the approved spec
  dag.json                — task graph
  progress.md             — append-only event log
  state/
    repo.json             — pipeline branch state
  contracts/
    T1.md, T2.md, ...     — per-task contracts
  reports/
    T1.md                 — implementer reports
    T1-audit.md           — audit reports
    T1-review.md          — review reports
    branch.md             — branch review report
  diffs/
    T1-base..head.diff    — per-task diffs
    branch-base..head.md  — branch review package
  checkpoints/
    <short-sha>.json      — checkpoint files
  worktrees/
    T1/, T2/, ...         — per-task git worktrees
  archive/
    <ISO>/                — archived previous runs
```

### Reset at startup

When a new SDD run starts (not a resume):
1. Archive the current workspace to `archive/<ISO>/`
2. Clear `reports/`, `contracts/`, `diffs/`, `checkpoints/`
3. Archive and remove `dag.json`, `spec.md`, `state/repo.json`, `progress.md`
4. Run `Cleanup.Stale()` to remove old `sdd/*` branches and worktrees
5. Log `SYSTEM WORKSPACE_RESET`

### Resume

If the user says "resume" or `dag.json` exists with in-progress tasks:
1. Do NOT reset
2. Read `dag.json` and `state/repo.json`
3. Run `State.Repair()` to sync head and merged array
4. Continue from the last drain iteration

---

## 11. Worktree management

### Just-in-time creation

Worktrees are created when a task is ready to dispatch, NOT at decomposition
time. This ensures the worktree branches from the current pipeline HEAD, which
includes all merged work from earlier tasks.

```go
func (c *Controller) createWorktree(taskID string) error {
    pipelineHead := c.git.RevParse(c.state.Branch)
    branch := fmt.Sprintf("sdd/%s", taskID)
    path := filepath.Join(c.ws.WorktreesDir, taskID)
    c.git.WorktreeAdd(path, branch, pipelineHead)
    c.dag.SetTaskBase(taskID, pipelineHead)
    c.dag.SetTaskWorktree(taskID, path)
    c.progress.Append(taskID, "SYSTEM", "worktree_created", ...)
    return nil
}
```

### Idempotent resume

If the worktree already exists (retry/idempotent resume), return the existing
path. Do NOT recreate it — the implementer's previous commits are preserved.

### Fresh (--force) for rebase conflicts

Only when a rebase has broken the worktree, the controller may recreate it with
`--force`. This destroys previous commits (intentional — the rebase broke them).
The controller must verify the task branch has no commits beyond the recorded
base before allowing `--force`, unless explicitly overridden.

### Stale cleanup

At startup, `Cleanup.Stale()` removes all `sdd/*` branches and worktrees that
don't correspond to tasks in the current `dag.json`. This prevents
contamination from previous runs.

### [UNKNOWN] Worktree path

The prototype used `.marshal/sdd/worktrees/<T>/`. The existing marshal SDD
used `.marshal/worktrees/<branch>/`. The production version should use
`.marshal/sdd/worktrees/<T>/` for consistency with the rest of the SDD
workspace.

---

## 12. Git operations

All git operations are Go functions in `internal/sdd/git.go`. No shelling out
to `git` CLI — use `go-git` or the existing marshal git utilities.

### [UNKNOWN] Git library

The existing marshal codebase shells out to `git` in several places
(`internal/agent/sdd/worktree.go`, `orchestrator.go`). The production version
should use either:
1. `go-git` library (pure Go, testable, but may lack some worktree features)
2. Continue shelling out (simpler, but harder to test and error-prone)
3. The existing marshal git utilities (if they exist — need to check
   `internal/tools/` for git-related tools)

**[UNKNOWN]**: Does marshal already have a git utility package, or does it
shell out everywhere? The exploration showed `internal/agent/sdd/worktree.go`
uses `exec.Command("git", ...)` — suggesting shelling out is the current
pattern. The production version should at minimum wrap these in a typed
`GitOps` interface for testability.

---

## 13. Progress logging

All events are logged to `progress.md` via `Progress.Append(taskID, event, kv...)`.
The log is append-only and is the source of truth for postmortems.

### Event categories

| Category | Events | Purpose |
|----------|--------|---------|
| Lifecycle | DECOMPOSE, MERGED, INTEGRATED, ROLLBACK | Pipeline milestones |
| Dispatch | DISPATCH_IMPL, DISPATCH_AUDIT, DISPATCH_REVIEW, DISPATCH_BATCH | Worker dispatches |
| Gates | VERIFY_PASS, VERIFY_FAIL, AUDIT_PASS, AUDIT_FAIL, REVIEW_PASS, REVIEW_FAIL | Gate results |
| Blocking | BLOCKED, RETRY | Task blocked or retried |
| Health | ORCHESTRATOR_HEALTH, RESCUE, MODEL, THINKING_ESCAPE | System health |
| System | SYSTEM, TELEMETRY, BRANCH_REVIEW | Infrastructure events |

---

## 14. Human gate points

The controller must stop and present to the human at these points:

1. **Spec approval**: after `sdd2-spec-bootstrap` creates the spec with
   `status: draft`. The human must change it to `status: approved` (or the
   controller provides a TUI confirmation dialog).
2. **Final merge gate**: after branch review PASS. The controller presents the
   merge command and waits for confirmation.
3. **Branch correction**: when the human asks to change the pipeline branch.
   The controller runs `Branch.Rebranch()` — never wipes state.
4. **Escalation**: when the orchestrator returns BLOCKED and the controller
   cannot fix it deterministically. The controller presents the block reason
   and asks for guidance.
5. **Model escalation**: when `sdd-rescue` recommends `MODEL_ESCALATION`. The
   controller presents the recommendation and asks the human to confirm.

### [UNKNOWN] Human gate UI

The existing TUI shows system messages in the transcript. Human gates could
be:
1. A system message with instructions (current pattern)
2. A TUI dialog with confirm/cancel buttons
3. A prompt in the input area

The prototype used system messages. The production version should keep this
pattern for simplicity, with clear `▸ SDD: ...` prefixes.

---

## 15. Error recovery and escalation

### Deterministic fixes (controller handles, no LLM)

| Error | Fix |
|-------|-----|
| `WRONG_BASE` | Run `Branch.Rebranch()` |
| `DIRTY_MAIN_REPO` | Stash or discard overlapping files |
| `WORKTREE_HAS_COMMITS` | Don't recreate worktree (use existing) |
| `REVIEW_MALFORMED` | Run `Normalize.Report()`, retry |
| `AUDIT_MALFORMED` | Run `Normalize.Report()`, retry |
| `STALE_HEAD` | Run `State.Repair()` |
| `ALLOWED_FILES_VIOLATION` | Expand contract Allowed Files, retry |

### LLM-assisted fixes (dispatch subagent)

| Error | Fix |
|-------|-----|
| `REBASE_CONFLICT` | Dispatch `sdd-investigator` for diagnosis, then retry |
| `VERIFY_FAIL` (real) | Dispatch `sdd-investigator`, then `sdd-implementer` with retry contract |
| `REVIEW_FAIL` (real) | Dispatch `sdd-implementer` with retry contract |
| `BRANCH_REVIEW_FAIL` | Dispatch `sdd-implementer` for branch-wide fixes |

### Human escalation

| Error | Action |
|-------|--------|
| Retry budget exhausted | Present blocked tasks to human |
| `HEALTH_ALERT` (loop) | Dispatch `sdd-rescue`, present recommendation |
| `MODEL_ESCALATION` recommended | Present to human, swap model on confirmation |
| Spec ambiguity | Ask human for clarification |

---

## 16. Model selection and routing

### Roles and model tiers

| Role | Tier | Rationale |
|------|------|-----------|
| sdd-orchestrator | fast (cached) | Runs one drain iteration, repetitive dispatch |
| sdd-implementer | fast | Code writing, high step count |
| sdd-auditor | coder | Static analysis needs code understanding |
| sdd-reviewer | coder/pro | Spec compliance + quality judgment |
| sdd-branch-reviewer | strong | Whole-branch review, cross-task analysis |
| sdd-investigator | coder | Failure diagnosis |
| sdd-rescue | strong | Orchestrator recovery, complex reasoning |

### Controller model

The controller is mostly Go code. When it needs an LLM (for ambiguous
escalation decisions), it uses the `sdd-rescue` role (strong model). The
controller's LLM context is ~100K tokens (state-dump + relevant reports),
heavily cached across calls.

### Model escalation

When `sdd-rescue` recommends `MODEL_ESCALATION`:
1. The controller presents the recommendation to the human
2. On confirmation, the controller re-dispatches the orchestrator with a
   stronger model (by changing the route for `RoleSDDOrchestrator`)
3. The stronger model runs the next drain iteration with the same small context

### [UNKNOWN] Dynamic route changing

The existing routing system (`internal/llm/routing/router.go`) resolves roles
to presets/models at runner construction time. To swap the orchestrator's model
mid-run, the controller would need to either:
1. Build a new `RunnerFactory` with a different route for the orchestrator role
2. Override the model on the runner directly

**[UNKNOWN]**: Can the route for a specific role be changed at runtime, or is
it fixed at config load time? If fixed, the controller would need to construct
a new `RunnerFactory` with a modified config for the escalated model.

---

## 17. Prompt management

### System prompts

Built in Go as `strings.Builder` (same pattern as existing
`internal/agent/sdd/prompts.go`). No external prompt files.

### Orchestrator prompt (~2-3K tokens)

The orchestrator's system prompt contains:
- Drain loop instructions (read state-dump, find ready tasks, dispatch workers,
  run gates, merge, return report)
- Parallel dispatch rule (all ready tasks in one response)
- No-direct-edit rule (enforced by Go code, but stated for clarity)
- Report format (structured return to controller)
- Self-check (count task tool calls before sending)

### Worker prompts

Each worker role has a system prompt addendum (in
`internal/agent/prompts.go`) and a dispatch prompt (built per-task in
`internal/sdd/prompts.go`). The dispatch prompt includes:
- Contract path (worker reads it)
- Worktree path (for implementer)
- Report output path (worker writes to it)
- Global constraints

### Prompt changes from prototype

The production prompts are much shorter than the 35K-token prototype prompt
because:
- The controller handles spec gate, branch correction, human gates (not the
  orchestrator)
- Gates are Go functions (not prompt rules)
- The orchestrator runs one iteration (not indefinitely)
- State is in `progress.md` and `state-dump` (not in the prompt)

---

## 18. TUI integration

### Existing surfaces (keep, adapt)

| Surface | Current | Production |
|---------|---------|------------|
| `/sdd` command | Dispatches orchestrator | Dispatches controller |
| `/mode → SDD` | Opens plan picker | Same |
| Plan picker | Globs `plans_dir` | Same |
| Pre-flight cast list | Shows 3 SDD roles | Shows 6 SDD roles |
| Status line | `sdd · task N/M` | `sdd · task N/M · <phase>` |
| Live strip | `sdd task N/M · <detail>` | Same, with phase detail |
| SDD progress panel | Not in current TUI (removed) | Add back: task rows with phases |
| Settings | `sdd.auto_worktree`, `max_fix_rounds`, `plans_dir` | Add: `sdd.default_model_tier`, `sdd.verify_timeout` |

### New TUI elements

- **Controller status**: the status line shows the controller's current state
  (WORKSPACE_RESET, SPEC_GATE, DRAIN, BRANCH_REVIEW, BLOCKED, FINALIZE)
- **Escalation dialog**: when the controller escalates, a system message
  presents the block reason and asks for input
- **Spec approval prompt**: when the spec is ready, a system message shows the
  spec path and asks for approval

### [UNKNOWN] SDD progress panel

The original design doc proposed a persistent SDD panel (like the swarm panel)
but it was removed in favor of the live strip. The production version should
add it back because the pipeline has more phases (audit, review, retry,
branch review) that need visibility. The panel would show:
- Each task with its current phase (pending, implementer, audit, review,
  merged, blocked)
- Retry count per task
- Branch review status
- Controller state (draining, blocked, finalizing)

---

## 19. Configuration

```toml
[sdd]
auto_worktree     = true
max_fix_rounds    = 3
max_total_tokens  = 0       # 0 = no budget cap
plans_dir         = ".marshal/plans"
verify_timeout_ms = 300000  # 5 minutes per verify-task check
default_model_tier = "fast" # default orchestrator model tier
cleanup_at_start  = true    # run task-cleanup --stale at startup
```

### [UNKNOWN] Config additions

The existing `SDDConfig` (`internal/app/config/types.go:60-65`) has
`AutoWorktree`, `MaxFixRounds`, `PlansDir`. The production version adds
`VerifyTimeoutMS`, `DefaultModelTier`, `CleanupAtStart`, `MaxTotalTokens`.
These need to be added to the config types, defaults, merge logic, and TUI
settings panel.

---

## 20. File layout

### New files

```
internal/sdd/                       (replaces internal/agent/sdd/)
  controller.go                     — state machine, drain dispatch, escalation
  orchestrator.go                   — drain loop LLM dispatch + report parsing
  git.go                            — typed git operations interface
  workspace.go                      — workspace lifecycle, reset, archive
  dag.go                            — DAG types, load/save, task status updates
  state.go                          — repo state, head sync, merged array
  progress.go                       — append-only progress logger
  contract.go                       — contract extraction + validation
  report.go                         — report normalization + validation
  verify.go                         — build/lint/test verification
  gates.go                          — audit-gate, review-state, review-guard
  guards.go                         — branch-base-guard, edit-guard, allowed-files
  worktree.go                       — worktree create/rebase/cleanup
  merge.go                          — task merge, finalize, rebranch
  checkpoint.go                     — checkpoint creation
  health.go                         — loop/stagnation detection
  rescue.go                         — rescue bundle assembly
  retry.go                          — retry contract preparation
  branch_package.go                 — branch review package assembly
  prompts.go                        — orchestrator + worker prompt builders
  types.go                          — all shared types (DAG, RepoState, etc.)
  config.go                         — SDD config types + defaults

  controller_test.go
  orchestrator_test.go
  dag_test.go
  state_test.go
  progress_test.go
  contract_test.go
  report_test.go
  verify_test.go
  gates_test.go
  guards_test.go
  worktree_test.go
  merge_test.go
  health_test.go
  prompts_test.go
```

### Modified files

```
internal/llm/routing/types.go       — new SDD roles (orchestrator, auditor, investigator, rescue)
internal/llm/routing/router.go      — SDDCastRoles updated
internal/agent/prompts.go           — new role addenda
internal/app/config/types.go        — new SDD config fields
internal/app/config/defaults.go     — new SDD defaults
internal/app/app.go                 — buildSDDRunner → buildSDDController, wiring
internal/app/runtime.go             — SDDRunner → SDDController
internal/app/session/sdd_progress.go — expanded SDDProgress (phases, retries, controller state)
internal/app/tui/model.go           — /sdd dispatch, plan picker, cast list
internal/app/tui/status.go          — controller state in status line
internal/app/tui/livestrip.go       — phase detail in live strip
internal/app/tui/sdd_panel.go       — (re-add) progress panel with phases
internal/app/tui/settings/         — new SDD settings
internal/commands/commands.go       — /sdd command (unchanged)
```

### Removed files

```
internal/agent/sdd/orchestrator.go  — replaced by internal/sdd/controller.go + orchestrator.go
internal/agent/sdd/plan.go          — replaced by internal/sdd/dag.go (plan parsing moves to controller)
internal/agent/sdd/prompts.go       — replaced by internal/sdd/prompts.go
internal/agent/sdd/workspace.go     — replaced by internal/sdd/workspace.go
internal/agent/sdd/ledger.go        — replaced by internal/sdd/progress.go
internal/agent/sdd/worktree.go      — replaced by internal/sdd/worktree.go
internal/agent/sdd/verdict.go       — replaced by internal/sdd/report.go (verdict parsing)
```

---

## 21. Testing strategy

### Unit tests (Go)

- **Controller**: state machine transitions, gate enforcement, escalation
  routing. Uses a fake `RunnerFactory` returning canned reports.
- **Gates**: audit-gate, review-guard, branch-base-guard, edit-guard,
  allowed-files — each tested with valid/invalid/malformed inputs.
- **DAG**: load/save, task status updates, dependency resolution, ready-task
  computation.
- **State**: head sync, merged array cross-check with dag.json.
- **Progress**: append, parse, health detection from log patterns.
- **Contract**: extraction from spec, validation of all required sections,
  file reference scanning.
- **Report**: normalization (buried status, audit verdict), validation.
- **Verify**: scoped diff computation, allowed-files violation detection.
- **Worktree**: creation, idempotent resume, stale cleanup.
- **Merge**: fast-forward, branch-base guard, dirty repo check.
- **Health**: loop detection, stagnation detection, alert generation.

### Integration tests

- **Full pipeline**: fake `RunnerFactory` + fake git ops → run a 3-task DAG
  through to branch review. Verify state transitions, progress log, merged
  array, checkpoint.
- **Retry flow**: implementer returns FAIL → retry contract → second implementer
  PASS → merge. Verify retry count, contract expansion.
- **Escalation**: orchestrator returns BLOCKED → controller dispatches rescue →
  rescue recommends fix → controller applies fix → re-dispatches orchestrator.
- **Resume**: mid-run checkpoint → cancel → resume → verify skipped tasks.

### [UNKNOWN] Integration test infrastructure

The existing SDD tests use a `fakeRunnerFactory` pattern. The production
version needs the same, plus a fake `GitOps` interface for git operations.
The test harness should be able to simulate:
- Git worktree creation/removal
- Branch HEAD advancement
- Merge conflicts
- Dirty working trees

---

## 22. Migration from prototype

### What changes

| Prototype (Kilo) | Production (Marshal) |
|---|---|
| Bash scripts (~30) | Go functions in `internal/sdd/` |
| 35K-token orchestrator prompt | ~2-3K-token orchestrator prompt |
| Orchestrator is primary agent | Controller (Go) is primary; orchestrator is a subagent |
| Prompt rules for enforcement | Go code for enforcement |
| Detective edit-guard (post-hoc) | Preventive edit restriction (tool scope) |
| `task` tool for worker dispatch | `agent.run` tool for worker dispatch |
| `.superpowers/sdd2/` workspace | `.marshal/sdd/` workspace |
| kilo.jsonc agent definitions | Go role addenda + prompt builders |
| Manual postmortem analysis | Postmortem prompt + kilo edit tracker (still manual, but structured) |

### What stays the same

- The pipeline flow: spec → decompose → drain (dispatch → verify → audit → review → merge) → branch review → finalize
- The DAG concept (tasks with deps, files, acceptance)
- The contract concept (Allowed Files, Knowledge Bundle, acceptance criteria)
- The review concept (per-task + branch-level)
- The retry concept (retry contract with evidence)
- The progress log concept (append-only event log)
- The worktree concept (per-task git worktrees)

### Migration steps

1. Create `internal/sdd/` with types and config
2. Implement the controller state machine
3. Implement git operations (GitOps interface + real implementation)
4. Implement gates and guards
5. Implement workspace lifecycle
6. Implement worktree management
7. Implement contract extraction and validation
8. Implement report normalization and validation
9. Implement verify-task
10. Implement merge, finalize, checkpoint, rebranch
11. Implement health monitoring and rescue
12. Add new SDD roles to routing
13. Build orchestrator and worker prompts
14. Wire TUI integration
15. Write tests
16. Remove old `internal/agent/sdd/`
17. Run a real pipeline to validate

---

## 23. Unknowns

These need investigation or user input before implementation:

1. **Orchestrator dispatch mechanism**: new `RoleSDDOrchestrator` role via
   `RunnerFactory`, or direct LLM call? Can the orchestrator use `agent.run`
   to dispatch workers?

2. **Orchestrator context**: does `runner.RunTask` support structured data in
   the prompt, or only `@file` references? How to pass the state-dump JSON?

3. **Tool registration**: how to register SDD-specific tools with the registry?
   Need a `SDDToolOpts` struct pattern similar to `nativeOpts`.

4. **Edit guard enforcement**: does marshal's registry scope support per-path
   permission rules (allow edit for `.marshal/sdd/**`, deny elsewhere)? Or is
   a custom scope view needed?

5. **Git library**: use `go-git`, shell out, or existing marshal git utilities?
   What does the existing codebase use?

6. **Dynamic route changing**: can the orchestrator's model be swapped at
   runtime for escalation, or is the route fixed at config load time?

7. **Human gate UI**: system messages (current pattern), TUI dialog, or input
   prompt? How does the controller wait for human input without blocking the
   TUI?

8. **SDD progress panel**: re-add the persistent panel from the original design
   doc, or keep the live strip only?

9. **Token metering**: the prototype SDD didn't wire `UsageObserver`. The
   production version should track per-role token usage for cost analysis. How
   does the swarm wire `UsageObserver`, and can SDD reuse it?

10. **Parallel worker dispatch**: the orchestrator needs to dispatch multiple
    workers in one `RunTask` response via multiple `agent.run` tool calls. Does
    marshal's runner support parallel tool calls within a single turn, or are
    they sequential?

11. **Subagent depth**: the existing `agent.run` tool has max depth 1 and max
    concurrency 2. The orchestrator dispatching workers is depth 1 (orchestrator
    is depth 0 or a top-level role). Workers are depth 1. Is this within the
    existing limits, or do they need adjustment?

12. **Context isolation**: the existing SDD role runners share the parent
    session state. The production orchestrator should get a fresh context each
    iteration. Does `RunTask` with no prior history achieve this, or does the
    shared session state leak context?

13. **Plan parser**: the existing `internal/agent/sdd/plan.go` parses
    `### Task N:` headings. The production version needs to also parse the
    spec's YAML `tasks:` block with `id`, `title`, `files`, `acceptance`,
    `deps`. Should the plan parser and the spec parser be separate, or merged?

14. **SDD workspace vs marshal workspace**: the existing SDD uses
    `.marshal/sdd/`. Some marshal git worktrees use `.marshal/worktrees/`. The
    production version should standardize on `.marshal/sdd/worktrees/` but this
    needs to be confirmed against the existing codebase conventions.