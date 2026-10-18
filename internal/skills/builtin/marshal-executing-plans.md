---
name: marshal-executing-plans
description: Execute an approved written implementation plan task-by-task, inline or by delegating each task to a subagent, with one verified commit per task. Supersedes the executing-plans skill.
risk: workspace_write
---

# Marshal Plan Execution

Use this skill when executing a written implementation plan outside the /sdd
pipeline. This skill supersedes executing-plans: where they conflict, follow
this skill.

You are the controller. The plan already holds the design; your job is to
apply it, not to redo it. Settle every open question once, up front, so
that the whole run spends its time implementing. A subagent that has to make
design decisions or re-check the repository's health wastes hours.

## 1. Set up the run (once, before Task 1)

1. **Read the plan once, in full.** Note its base commit, shared contracts,
   sequencing rules and each task's Files and Verify blocks. If HEAD has moved
   past the base, re-check the anchors of the first task you will run.
2. **Readiness pass.** For every task, decide whether a cold agent could
   complete it from its own section without making a design decision (see
   "Gaps and decisions" below). Fill small gaps now and write each one down.
   Collect every bigger decision.
3. **Ask once.** Ask the user in a single `question.ask` call:
   - every bigger decision from the readiness pass, with the options you see;
   - how tasks should run, unless the user already said so in their request:
     - **Inline** — you implement every task yourself.
     - **Delegate** — each task goes to a subagent via `agent.run`.
     - **Mixed** — tasks that are verbatim or fully pinned down go to a
       subagent; tasks that need judgment stay inline.
   - for Delegate or Mixed, which model the subagents use: "same as mine"
     plus the relevant entries from the agent roster in your system prompt.

   If you cannot ask (auto mode, or `question.ask` is unavailable), run
   **Inline** and say so. Treat any bigger decision as a blocker: stop and
   report it before starting the first task it affects.
4. **Capture a baseline.** Run the union of every task's Verify commands once,
   at the starting commit, before any edit. Record each failing test by name.
   These are the known baseline failures for the whole run. Nobody
   investigates, reproduces, or re-proves them later.
5. Create one todo per task.

### Choosing what to delegate

The choice of model sets what a subagent can safely take on:

- A subagent on a **less capable** model than yours gets only tasks that are
  verbatim code or that you have fully pinned down (signatures, test names,
  every decision written into the brief). Keep tasks that need judgment
  inline.
- A subagent on an **equal or more capable** model may take tasks that need
  judgment, but every decision the plan or the user settled still goes into
  the brief.

Run tasks **sequentially**. Delegated tasks share your worktree, so a sibling's
half-finished edits break the other's build and tests. Run two tasks at once
only when the plan explicitly marks them parallel-safe and their file sets are
disjoint. Otherwise do not use `dispatching-parallel-agents` for plan tasks.

## 2. Gaps and decisions

**Small gap — fill it yourself, record it, continue:**

- names of unexported helpers, test functions, or fixtures the plan left open;
- reusing an existing helper the plan did not mention;
- an anchor that moved but is unambiguous at its new location;
- a test file in the same package that the task's Files list forgot;
- ordering or placement within a file.

**Bigger decision — stop and ask the user:**

- a new or changed exported API, interface, config key, or persisted format
  that the plan does not spell out;
- a behavior choice the spec and plan do not settle;
- work in files or packages outside the task's list, other than its own tests;
- the plan contradicting the spec, the code, or itself;
- an anchor that is gone, or embedded code that cannot apply verbatim;
- a step that is design rather than instruction ("audit…", "ensure…",
  "cover the lifecycle…") and touches more than one package.

If a bigger decision only surfaces mid-run, finish or park the current task
cleanly, ask, and resume. Never guess at it, and never hand it to a subagent.

## 3. Execute each task

### Inline

1. Load only the current task's section and the contracts it uses.
2. Apply embedded code blocks **verbatim** — do not reformat, reorder, or
   "improve" them. Prose steps may adapt to the actual file contents, but
   re-check every anchor (symbol, file:line, distinctive string) before
   editing.
3. Run exactly the task's Verify commands with `shell.run`. A failure that is
   on the baseline list is not yours. Any other failure is.
4. Commit (see "Commit after each verified task").

### Delegated

1. **Write the brief** from the template below. Paste the task section
   verbatim. Do not tell the subagent to read the plan; a subagent that reads
   the whole plan inherits every other task's scope.
2. Dispatch it with `agent.run` (`model` set to the user's choice; omit it
   for "same as mine"). Wait with `agent.await`.
3. While it runs, peek now and then (`agent.await` with `"peek": true`). If
   it is redesigning, exploring beyond its files, re-checking baseline
   failures, or running commands outside Verify, redirect it with
   `agent.steer`.
4. **If it reports BLOCKED,** classify the question. Answer a small gap by
   re-dispatching with the answer added to the brief. Take a bigger decision
   to the user.
5. **If it reports DONE, check it yourself.** Its report is a claim, not
   evidence.
   - `git.status` / `git.diff`: changes stay within the task's files and its
     own tests, and embedded code landed verbatim. Revert anything outside
     scope, or treat it as a bigger decision.
   - Re-run the task's Verify commands.
   - Then commit.

Before the next task, read the signatures the next task consumes from the
committed code. Paste those into its brief rather than relaying the previous
subagent's description of them.

### Subagent brief template

```text
You are implementing ONE task of an approved implementation plan in the
worktree at <path> (branch <branch>). The design is settled. Implement it;
do not redesign, extend, or refactor beyond it.

## Task
<the task's section from the plan, pasted verbatim: Goal, Files, Steps,
Verify>

## Contracts this task uses
<only the shared contracts and earlier-task signatures it consumes, copied
from the plan or from the committed code>

## Decisions already made
<every small gap you filled and every user answer that applies, or "none">

## Rules
- Apply embedded code blocks verbatim. Re-check prose anchors before
  editing. If one is gone, stop and report BLOCKED.
- Change only the files listed above, plus their own tests.
- Run exactly these commands, and nothing else (no ./..., no vet sweeps,
  no other packages):
  <Verify commands>
- Known baseline failures (they fail before your change; do not
  investigate or reproduce them): <names, or "none">
- Do not commit or run any git command that writes state. The controller
  commits.
- If you reach a decision the plan does not settle (an exported API, a
  behavior choice, a file outside the list), stop and report BLOCKED with
  the question. Do not choose an assumption and continue.

## Report (keep it short)
- Status: DONE or BLOCKED
- Files changed
- Each Verify command with the tail of its output
- Small choices you made that the plan left open
- If BLOCKED: the exact question and the options you see
```

## Commit after each verified task

- Check what changed with `git.status` and `git.diff` first.
- Stage only the files the task touched (`git add <paths>`), never
  `git add -A` on an unfamiliar tree.
- Commit message: `<plan-slug>: task N — <task title>`.
- If the tree was already dirty before you started, do not sweep pre-existing
  changes into your commits.
- Mark the task complete in your todo list, then move to the next task.

Never commit failing work "to fix later". If a task cannot be verified and
the failure is not on the baseline list, stop and report instead of
committing.

## 4. Finish

Run the plan's final verification. Compare failures against the baseline:
only new failures count. Then report:

- the commits (`git log --oneline <base>..HEAD`);
- every small gap you filled and every decision the user made;
- any deviation from the plan, and any task left unfinished, with the reason.
