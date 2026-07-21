# Subagent-Driven Development (SDD) Mode Design

> **Status:** Design — approved direction, pending implementation plan.
>
> **Scope:** Adds SDD as a first-class built-in workflow mode in Marshal,
> surfacing the kilocode `subagent-driven-development` skill behaviour as a
> `/mode` entry and a `/sdd` command, with full TUI visibility into
> subagent lifecycle.

## Goal

Make Marshal execute multi-task implementation plans autonomously via a
controller + fresh-subagent-per-task pattern, with per-task review
(spec compliance + code quality) and a final whole-branch merge-gate
review — the workflow the kilocode `subagent-driven-development` skill
provides, but built-in and visible in the TUI.

## Architecture

SDD is a peer to the swarm runtime (`internal/agent/swarm/`): an
orchestrator that satisfies the TUI's `AgentRunner` interface, drives a
multi-phase loop, and reports progress through session state the TUI
renders. Unlike Ask/Edit/Auto (per-turn classification modes), SDD is a
plan-driven autonomous workflow runner — it takes over the session until
the plan is complete or the user cancels.

```
/sdd [plan-file]  ─┐
/mode → SDD ───────┤
                   ▼
          internal/agent/sdd/
            orchestrator.go   ── Run(ctx, planPath): the task loop
            plan.go           ── markdown parser (Task N sections + global constraints)
            workspace.go      ── .marshal/sdd/ scratch dir: briefs, reports, review packages
            ledger.go         ── durable progress.md (resume after compaction)
            prompts.go        ── implementer / task-reviewer / branch-reviewer prompt builders
            worktree.go       ── optional git worktree isolation
            verdict.go        ── parse reviewer verdicts (Spec ✅/❌, quality)
                   │
                   ▼  RunnerFactory (reuses agent.Runner + routing)
            internal/llm/routing  ── 3 new SDD roles
            internal/app/session ── SDDProgress state
            internal/app/tui      ── SDD panel, status segments, plan picker
```

The orchestrator reuses the existing `agent.Runner` + `RunTask` plumbing
(the same primitives the swarm uses). Each subagent dispatch constructs a
fresh `Runner` via the factory, bound to its role's route, with the same
provider/policy/registry wiring as swarm role runners.

## Entry Points

SDD is surfaced two ways:

1. **`/sdd [plan-file]`** — direct slash command, like `/swarm`.
   - With a path argument: reads the plan file and runs immediately.
   - Without an argument: auto-generates a plan first (runs the planner
     role against the user's goal, saves the plan to `plans_dir`, shows
     it for confirmation), then executes it.

2. **`/mode` picker** — a 4th entry, "SDD", with detail
   "plan-driven multi-task". Selecting it opens a plan-file picker
   (populated from `plans_dir` + custom path entry), then runs the
   workflow. If no plan files exist, offers "generate plan".

Both paths dispatch through the same `runAgentCmd` TUI plumbing the
swarm uses, with the SDD orchestrator as the `AgentRunner`.

## Plan Source

SDD executes plans. It can also generate them:

- **Plan exists** (`/sdd <path>`): parse the markdown, extract tasks,
  execute. The plan is the source of truth — SDD does not re-plan.
- **No plan given** (`/sdd` with no arg, or `/mode→SDD` with no files):
  run the planner role against the session's current goal/user prompt to
  produce a plan. Save it to `plans_dir` (default `.marshal/plans/`).
  Show it to the user for confirmation (a system message with the plan
  rendered as markdown + a prompt to proceed or edit). On confirmation,
  execute via SDD.

The planner step uses the existing `RolePlanner` routing role (no new
role needed for planning). The generated plan follows the
`writing-plans` format: a header with Global Constraints, then
`### Task N` sections.

## Task Extraction

Plans are markdown. The parser extracts each `### Task N:` heading block
(awk-style: capture from the heading line to the next `### Task` heading
or end of file, skipping fenced code blocks so headings inside code
fences are not mistaken for task markers). This mirrors the kilocode
`task-brief` script and is compatible with any `writing-plans`-style
plan or hand-written markdown.

For each task the parser produces:

```go
type PlanTask struct {
    Number      int
    Title       string
    Body        string          // full heading-block text (the brief)
}

type Plan struct {
    Title             string
    GlobalConstraints string    // the "## Global Constraints" section text
    Tasks             []PlanTask
}
```

The controller writes each task's `Body` to
`.marshal/sdd/task-N-brief.md` and hands the implementer the file path.
The task text never enters the controller's context as pasted text.

## Execution Loop

```
Run(ctx, planPath):
  1. Parse plan → tasks[], globalConstraints
  2. If auto-gen: run planner role, save plan, confirm with user
  3. If auto_worktree: create git worktree, cd into it
  4. Read ledger (.marshal/sdd/progress.md)
     → tasks the ledger marks complete are DONE — skip them
  5. Pre-flight plan review: scan plan text for destructive git commands,
     interactive npx/CLI invocations without non-interactive flags, and
     dependency changes that omit the lockfile. Announce findings before
     dispatching Task 1.
  6. SetSDDProgress({Active, PlanName, Tasks: [pending...]})
  7. For each remaining task N:
        a. Record BASE = git -C <worktree> rev-parse HEAD
        b. Record BRANCH = git -C <worktree> rev-parse --abbrev-ref HEAD
        c. Write task-N-brief.md
        d. UpdateSDDTask(N, implementer=active)
        e. Dispatch implementer subagent (sdd_implementer role)
           with: brief path, report path, worktree path, scene-setting context,
           global constraints, interfaces from earlier tasks
        f. Handle implementer status:
           - DONE              → verify branch state (see Controller Discipline)
           - DONE_WITH_CONCERNS → assess concerns, verify branch state, proceed to review
           - NEEDS_CONTEXT      → provide missing context, re-dispatch
           - BLOCKED            → escalate to user (stop)
        g. Verify branch state:
           - HEAD matches the SHA the implementer reported
           - git rev-parse --abbrev-ref HEAD matches the expected branch
           - the reported commit is not in main's recent log
           If verification fails, treat as a wrong-branch incident and escalate.
        h. Write review package: git log/diff -U10 BASE..HEAD
           → .marshal/sdd/review-<base7>..<head7>.diff
        i. UpdateSDDTask(N, reviewer=active)
        j. Dispatch task reviewer (sdd_reviewer role)
           with: brief path, report path, diff path, global constraints
        k. Parse reviewer verdict:
           - Spec ✅ + quality Approved → proceed to state sweep
           - Issues found (Critical/Important):
             → dispatch fix subagent (sdd_implementer, bounded MaxFixRounds)
             → re-review (go to step j)
             → if MaxFixRounds exhausted: escalate to user
           - Minor findings: record in ledger, proceed
        l. Deviations from the brief are tracked separately from Minor findings
           and surfaced at the final checkpoint.
        m. Post-review state sweep (git status, worktree log, main log) before
           marking the task complete.
        n. On clean review:
           - Append ledger line: "Task N: complete (commits <base7>..<head7>)"
           - UpdateSDDTask(N, phase=done, implementer=done, reviewer=done)
           - Announce "✔ Task N complete" in transcript
  8. Surface any accumulated deviations from the brief to the user before the
     final branch review.
  9. After all tasks:
        a. Write branch review package: git log/diff -U10 MERGE_BASE..HEAD
           (MERGE_BASE = git merge-base main HEAD)
        b. UpdateSDDBranchReview(active)
        c. Dispatch branch reviewer (sdd_branch_reviewer role)
           with: full plan path, task reports dir, branch diff path,
           global constraints, accumulated Minor findings list, deviations list
        d. Parse branch verdict:
           - ✅ Ready to merge → announce completion
           - Findings → ONE fix subagent for all findings, re-review once
  10. Announce "SDD complete" + finishing-a-development-branch guidance
  11. ClearSDDProgress()
```

**Continuous execution:** the loop does not pause between tasks. The only
stop conditions are: a BLOCKED status the controller cannot resolve, a
fix-round budget exhausted, ambiguity that genuinely prevents progress,
user cancellation (`/stop`), or all tasks complete.

**Cancellation:** `/stop` (or `Esc`) cancels the active subagent's
context. The orchestrator catches `ctx.Err()`, writes the ledger up to
the last completed task, and announces `"SDD cancelled at task N/M —
/sdd <plan> to resume."` Resume reads the ledger and skips completed
tasks.

## Routing: Three New SDD Roles

Add to `internal/llm/routing/types.go`:

```go
RoleSDDImplementer     AgentRole = "sdd_implementer"
RoleSDDReviewer        AgentRole = "sdd_reviewer"
RoleSDDSBranchReviewer AgentRole = "sdd_branch_reviewer"
```

Add the mirror `agent.AgentRole` constants to
`internal/agent/prompts.go` with role addenda embedding the SDD
discipline (TDD, self-review, report-file contract, escalate-don't-guess).

Profiles map these roles to presets, same as swarm roles today:

```toml
[models.profiles.local_balanced.roles]
sdd_implementer      = "fast"
sdd_reviewer         = "coder"
sdd_branch_reviewer  = "strong"
```

Unconfigured roles fall back to the implementer preset (existing
`ResolveRole` fallback), so SDD works out-of-the-box with a single-model
local setup.

The three tiers map to the kilocode pinning model:
- **sdd_implementer** — implementation, code fixes (flash tier)
- **sdd_reviewer** — per-task spec compliance + code quality (pro tier)
- **sdd_branch_reviewer** — final whole-branch merge gate (strongest
  tier). Reserved for the merge gate only — sees the full branch diff +
  full plan.

## Prompt Templates

The kilocode prompt templates (`implementer-prompt.md`,
`task-reviewer-prompt.md`, `branch-reviewer-prompt.md`) become Go string
builders in `internal/agent/sdd/prompts.go`. Each builder fills
placeholders:

- `[BRIEF_FILE]` — the task brief path (controller writes, subagent reads)
- `[REPORT_FILE]` — the implementer report path (implementer writes, reviewer reads)
- `[DIFF_FILE]` — the review package path (controller writes, reviewer reads)
- `[GLOBAL_CONSTRAINTS]` — verbatim from the plan's Global Constraints section
- `[BASE_SHA]` / `[HEAD_SHA]` / `[MERGE_BASE_SHA]` — git commit refs
- `[PLAN_FILE]` — the full plan path (branch reviewer only)
- `[TASK_REPORTS_DIR]` — the reports directory (branch reviewer only)
- `[WORKTREE_DIR]` — the git working directory the implementer must use

The prompts preserve the kilocode contracts and add the discipline that
mirrors the custom kilo SDD agent:

- **Implementer** must work from the supplied worktree, run
  `git rev-parse --abbrev-ref HEAD` before every commit, never use
  `git reset --hard`, and report commits with branch verification. It
  includes a "Self-review limits" section: structural self-reviews must be
  surfaced as BLOCKED/NEEDS_CONTEXT before re-doing work.
- **Task reviewer** must treat the implementer's report as unverified claims,
  never silently absorb deviations from the brief, and include a
  **Deviated** category in spec compliance. Every deviation from the brief
  is reported as a finding, even if it seems reasonable.
- **Branch reviewer** must surface accepted per-task deviations in the
  whole-branch review (Accumulated Minor Triage / Deviation rule), and must
  not re-flag per-task findings that were already fixed or accepted.
- Fix dispatches (per-task and whole-branch) re-state the worktree
  discipline and branch-check rules so the fix subagent does not introduce a
  wrong-branch incident.

Implementer reports with status (DONE / DONE_WITH_CONCERNS / BLOCKED /
NEEDS_CONTEXT), commits, branch verification, one-line test summary,
concerns, and report path. Task reviewer returns two verdicts: spec
compliance (✅/❌/⚠️) and task quality (Approved / Needs fixes), with
file:line findings. Branch reviewer returns a merge verdict (Ready / Not
ready) with whole-plan coverage, cross-task integration, architecture,
deviation triage, and Minor triage.

## Controller Discipline: Branch State Checks

The orchestrator records the expected branch before dispatching the first
implementer (`git rev-parse --abbrev-ref HEAD` on the worktree). Before each
task it records `BASE_SHA`; after the implementer reports DONE it verifies:

1. The branch before dispatch matched the expected branch.
2. The worktree is still on the expected branch (`git rev-parse --abbrev-ref HEAD`).
3. The implementer's reported commit SHA matches the worktree HEAD.
4. The reported commit does not appear in `main`'s recent log (`git log --oneline -3`).

If any check fails, the task is treated as a **wrong-branch incident**. The
orchestrator escalates to the user and does **not** dispatch a fix subagent
for it — a fix subagent is likely to re-introduce the same working-directory
mistake. Manual recovery (e.g., `git update-ref`, `git reset`) is required.

## Handling Deviations from the Brief

If the task reviewer reports a deviation from the brief (changed assertion,
renamed identifier, altered behavior, etc.), the orchestrator:

- Tracks the deviation separately from "⚠️ Cannot verify from diff" items.
- Adds it to the deviations list that is surfaced at the final checkpoint.
- Does not let the reviewer's "this is reasonable" judgement absorb the
departure silently.
- Surfaces the accumulated deviations to the user before the final branch
review so the human can decide whether to accept or reject them.

If the final branch reviewer sees accepted per-task deviations, those are
surfaced in the Accumulated Minor Triage / Deviation rule section.

## Post-Review State Sweep

Before marking a task complete, the orchestrator runs a short state sweep:

- `git -C <worktree> status` — confirm no uncommitted package files or stray changes.
- `git -C <worktree> log --oneline -3` — confirm the branch tip moved as expected.
- `git -C <main> log --oneline -3` — confirm no commits leaked to main.

This catches wrong-branch incidents and forgotten-stage incidents before the
next task dispatch. The sweep is announced in the transcript so it is visible
in the TUI.

## Durable Workspace + Ledger

`.marshal/sdd/` — a git-ignored scratch directory (self-ignoring
`.gitignore` with `*`, like the kilocode `.superpowers/sdd/`):

```
.marshal/sdd/
  .gitignore          — "*\n" (ignore all contents)
  progress.md         — the ledger
  task-1-brief.md     — extracted task text (controller writes)
  task-1-report.md    — implementer's full report (implementer writes)
  review-<base7>..<head7>.diff — review package (controller writes)
```

The bash scripts (`task-brief`, `review-package`, `sdd-workspace`)
become Go functions in `internal/agent/sdd/workspace.go` — Marshal
already shells out to git for snapshots/diffs, but in-process functions
keep the workflow self-contained and testable.

**Ledger format** (`progress.md`): one line per completed task:

```
Task 1: complete (commits a1b2c3..d4e5f6, review clean)
Task 2: complete (commits e5f6g7..h8i9j0, review clean)
```

On startup the orchestrator reads the ledger and marks those tasks DONE
— they are skipped, not re-dispatched. This is the resume-after-compaction
recovery map: the commits it names exist in git even when the
controller's context no longer remembers creating them.

## Worktree Isolation

`internal/agent/sdd/worktree.go` optionally creates a git worktree before
the task loop:

- `git worktree add -b sdd/<plan-slug> <path> <base>`
- Records the original working dir; the orchestrator runs in the worktree.
- On completion (or cancellation): offers to merge/PR/cleanup via the
  finishing-a-development-branch guidance (system message).
- If git is unavailable or `auto_worktree = false`: falls back to the
  current working tree (like the swarm).

## Config

New `[sdd]` config block in `internal/app/config/config.go`, paralleling
`[swarm.budget]`:

```toml
[sdd]
auto_worktree   = true
max_fix_rounds  = 3        # bounded fix rounds per task
max_total_tokens = 0       # 0 = no budget cap
plans_dir        = ".marshal/plans"
```

Loaded via the existing config merge chain (defaults →
`~/.config/marshal/config.toml` → `.marshal/config.toml`). Defaults are
set in `config.Default()`.

## TUI Design

### Principles

Follows the tui-design skill: semantic color (never color-alone), spatial
consistency (fixed panel position), keyboard-first (no new bindings),
progressive disclosure (footer → ? → docs), async feedback (spinner +
progress), monochrome-safe (symbols carry meaning).

### Layout

Marshal is a single-column chat TUI. SDD adds a persistent progress
panel (like the swarm panel) without rearranging existing surfaces:

```
┌─ Transcript ──────────────────────────────────┐
│  user: /sdd docs/.../feature-plan.md            │
│  ▸ SDD started: feature-plan (5 tasks)          │
│    Task 1: Hook installation script              │
│  ✔ Task 1 complete (commits a1b2c3..d4e5f6)     │
│  ⠹ Task 2: Recovery modes · reviewer · 8s       │
│  ⚠ Task 2: review found 1 Important issue       │
│  ✔ Task 2 complete                              │
│  ...                                             │
├─ SDD Panel ────────────────────────────────────┤
│ SDD: feature-plan                                │
│ Task 1   ✔ implementer  ✔ reviewer              │
│ Task 2   ✔ implementer  ⠹ reviewer fix 1/3      │
│ Task 3   ○ implementer  ○ reviewer               │
│ Task 4   ○ implementer  ○ reviewer               │
│ Task 5   ○ implementer  ○ reviewer               │
│ Branch review: ○                                │
├─ Input ────────────────────────────────────────┤
│ ❯ SDD running — /stop to cancel                 │
├─ Footer ───────────────────────────────────────┤
│ [/]command [?]help [Esc]cancel-sdd [Enter]send   │
├─ Status ───────────────────────────────────────┤
│ sdd · task 2/5 · fix 1/3 · qwen3 @ ollama ...   │
└─────────────────────────────────────────────────┘
```

### Surface 1: SDD Progress Panel (persistent)

New file `internal/app/tui/sdd_panel.go`, rendered in `viewString()`
when `state.SDDProgress().Active` — a peer to `renderSwarmPanel`.

**New session state** (`internal/app/session/sdd_progress.go`):

```go
type SDDPhase string

const (
    SDDPhasePending SDDPhase = "pending"
    SDDPhaseActive  SDDPhase = "active"
    SDDPhaseDone    SDDPhase = "done"
    SDDPhaseFailed  SDDPhase = "failed"
    SDDPhaseSkipped SDDPhase = "skipped" // ledger-resumed completed tasks
)

type SDDTaskStatus struct {
    Name        string
    Phase       SDDPhase         // overall task phase
    Implementer SDDPhase         // implementer sub-phase
    Reviewer     SDDPhase         // reviewer sub-phase
    FixRound     int              // current fix round (0 = none yet)
    MaxFixes     int
    Detail       string           // "running 12s", "fix 1/3", "spec issues"
}

type SDDProgress struct {
    Active       bool
    PlanName     string
    PlanPath     string
    Tasks        []SDDTaskStatus
    BranchReview SDDPhase
    TotalTasks   int
    DoneTasks    int
    TokensUsed   int
    TokensMax    int
}
```

Methods mirror `swarm_progress.go`: `SetSDDProgress`, `SDDProgress`,
`UpdateSDDTask`, `UpdateSDDBranchReview`, `UpdateSDDTokens`,
`ClearSDDProgress`. All return clones (the swarm's copy-out pattern) so
TUI reads never mutate orchestrator state.

**Panel rendering** (`renderSDDPanel`): one row per task showing two
sub-phases side by side (implementer + reviewer) with status glyphs, plus
a final branch-review row. Glyphs reuse the shared `statusGlyph`
mapping (extracted from `swarm_panel.go`):

- `○` pending → `dimColor`
- spinner active → `accentColor`
- `✔` done → `successColor`
- `✘` failed → `errorColor`
- `⟳` fix round in progress → `warningColor`

The panel is capped at a fixed row count (`sddPanelRows`) and indents with
`indentBlock`, exactly like `renderSwarmPanel`. Symbols carry meaning in
monochrome (never color-alone).

### Surface 2: In-Transcript Lifecycle Messages

Each SDD phase transition writes a `RoleSystem` message to the
transcript (the same mechanism the swarm uses for "Swarm run started.").
This gives a chronological narrative:

```
▸ SDD started: feature-plan (5 tasks, worktree sdd/feature-plan)
  Task 1/5: Hook installation script — implementer dispatched
  Task 1/5: implementer DONE (commits a1b2c3..d4e5f6, 14/14 tests)
  Task 1/5: reviewer dispatched
  Task 1/5: review clean — spec ✅, quality approved
  ✔ Task 1 complete
  Task 2/5: Recovery modes — implementer dispatched
  Task 2/5: review found 1 Important issue (magic number)
  Task 2/5: fix round 1/3 dispatched
  Task 2/5: review clean — spec ✅, quality approved
  ✔ Task 2 complete
  ...
  Branch review dispatched (merge-base main..HEAD)
  ⚠ Branch review: 2 Important findings — fix wave dispatched
  Branch review re-check: ✅ ready to merge
  ▸ SDD complete. 5/5 tasks done. Use /merge or /pr to finish.
```

Uses existing `renderMessage` content types (`ContentTypePlain` /
`ContentTypeMarkdown`) — no new renderer. The `✔`/`⚠`/`▸` prefix symbols
carry meaning in monochrome.

### Surface 3: Activity Strip (in input area)

The existing `renderActivityStrip()` shows `"thinking"` /
`"shell.run · 12s"`. SDD sets the session `Activity` to a tool-kind
activity with a label like `"sdd: task 2 implementer"` or
`"sdd: task 2 reviewer (fix 1/3)"`. The strip renders:

```
⠹ sdd: task 2 implementer · running 12s
```

No new rendering code — the orchestrator calls
`state.SetActivity(...)` with descriptive labels. The spinner frame
comes from the existing `activeSpinnerFrame(session.ActivityTool)`.

### Surface 4: Status Line Segments

Add SDD-specific segments to `statusLeftSegments()`, following the
existing priority-drop pattern:

| Segment       | Priority | Example        | When shown                          |
|---------------|----------|----------------|-------------------------------------|
| mode cue      | 0        | `sdd`          | replaces ask/edit/auto while active |
| task progress | 1        | `task 2/5`     | always while active                 |
| fix round     | 2        | `fix 1/3`      | only during a fix round             |
| plan name     | 6        | `feature-plan` | lowest priority, dropped first      |

The `modeSegment()` helper gains an SDD branch: when
`state.SDDProgress().Active`, it returns `"sdd"` (overriding forceMode).
The right segment already shows activity + elapsed — no change needed.

### Surface 5: /mode Picker + Plan Picker

**Mode picker** — `modePickerItems()` gains a 4th entry:

```go
{Label: "SDD", Detail: "plan-driven multi-task", Badge: badge("sdd"), Value: "sdd"},
```

Picking "SDD" opens a **plan picker** — a second `picker.Model`
populated from `plans_dir` (default `.marshal/plans/*.md`), each item
showing the filename as Label and the first heading line as Detail.
`SetAllowCustom(true)` lets the user type an arbitrary path. The picked
value flows back through `dispatchCommand` → `case "sdd"` → orchestrator
dispatch, like `/model` picks flow today.

If no plan files exist, the picker offers a "generate plan" entry that
triggers the planner-first flow.

### Surface 6: Input During SDD

While SDD is active, the input box renders a muted hint instead of an
editable prompt:

```
❯ SDD running — /stop to cancel, wait for completion to resume typing
```

This follows the modal-confusion anti-pattern fix: the status line says
`sdd`, the input is visibly disabled, and `Esc` / `/stop` cancels. The
input regains focus when SDD completes or is cancelled. A small
`m.busy`-gated branch in `renderInputArea`, reusing `mutedStyle`.

### Surface 7: Cancellation + Completion

- `/stop` (or `Esc`) cancels via the existing `m.agentCancel` context.
  The orchestrator catches `ctx.Err()`, writes the ledger up to the last
  completed task, and announces `"SDD cancelled at task N/M — /sdd
  <plan> to resume."`.
- On completion, the transcript shows finishing-a-branch guidance
  (merge/PR/cleanup) as a system message. The panel clears via
  `ClearSDDProgress()`, restoring the normal Ask/Edit/Auto status line.

### Color + Accessibility

All SDD visuals use existing semantic slots (`successColor`,
`errorColor`, `warningColor`, `accentColor`, `dimColor`) — no new
colors. Status glyphs (`✔ ✘ ○ ⟳ ▸ ⚠`) carry meaning independent of color
(monochrome-safe). The spinner reuses existing braille frames. Panel
position is fixed (above input, below transcript). All interaction is
keyboard-driven via existing keys.

## File Handoffs (Context Hygiene)

The kilocode principle — bulk artifacts move as files, not pasted text —
is preserved. The orchestrator (controller) holds only the plan, the task
index, and the ledger. Each subagent gets file paths, reads them in one
`file.read` call, and writes its report to a file. The subagent returns
only a short status + commit list + report path. This keeps the
controller's context small across a long multi-task run.

## New + Modified Files

### New

```
internal/agent/sdd/
  orchestrator.go        — Run(ctx, planPath): the task loop
  plan.go                — markdown parser (Task N sections + constraints)
  workspace.go           — .marshal/sdd/ scratch dir + review-package writer
  ledger.go              — durable progress.md read/append
  prompts.go             — implementer / task-reviewer / branch-reviewer builders
  worktree.go            — optional git worktree creation + restore
  verdict.go             — parse reviewer verdicts
  orchestrator_test.go   — scripted factory tests (swarm pattern)
  plan_test.go           — parser tests against sample markdown
  ledger_test.go         — write → read → resume skips completed
  verdict_test.go        — verdict parser tests

internal/app/session/
  sdd_progress.go        — SDDProgress state + methods
  sdd_progress_test.go   — state round-trip + concurrency tests

internal/app/tui/
  sdd_panel.go           — renderSDDPanel
  sdd_panel_test.go      — panel render tests
```

### Modified

```
internal/llm/routing/types.go       — 3 new SDD role constants
internal/agent/prompts.go            — 3 new agent.AgentRole constants + addenda
internal/app/config/config.go       — [sdd] config block + defaults
internal/app/app.go                  — buildSDDRunner, WithSDDRunner wiring
internal/app/runtime.go              — SDDRunner field + reload
internal/commands/commands.go        — /sdd command registration
internal/app/tui/model.go            — sddRunner field, /sdd + /mode→sdd dispatch,
                                       plan picker, modePickerItems 4th entry
internal/app/tui/view.go             — add SDD panel to viewString rows
internal/app/tui/status.go           — modeSegment SDD branch + statusLeftSegments
internal/app/tui/swarm_panel.go      — extract shared statusGlyph for reuse
```

## Testing

Following the swarm's test patterns:

- **Scripted orchestrator tests** (`orchestrator_test.go`): fake
  `RunnerFactory` returning canned task summaries, verifying the
  dispatch order (implementer → reviewer → fix → re-review), fix-round
  budgeting, ledger appends, and cancellation.
- **Plan parser tests** (`plan_test.go`): sample markdown plans with
  fenced code blocks, nested headings, missing Global Constraints.
- **Ledger round-trip tests** (`ledger_test.go`): write → read → resume
  skips completed tasks; corrupt/empty ledger handling.
- **Verdict parser tests** (`verdict_test.go`): spec ✅/❌/⚠️, quality
  Approved/Needs fixes, branch Ready/Not ready.
- **State tests** (`sdd_progress_test.go`): set/get copy semantics,
  concurrent UpdateSDDTask.
- **Panel render tests** (`sdd_panel_test.go`): active/inactive,
  mid-fix-round, failed, narrow-width truncation.
- **TUI dispatch tests** (`model_test.go` additions): `/sdd` with and
  without args, `/mode→SDD` plan picker, cancellation, busy guard.

## Open Questions

None remaining — all design decisions resolved during brainstorming.