---
name: marshal-writing-plans
description: Author a written implementation plan for inline (non-SDD) execution from an approved spec — decompose the design into ordered, self-contained tasks with exact files, verbatim code for mechanical edits, prose+verified anchors for judgment edits, and verification commands. Use after brainstorming approves a spec, when not using the /sdd pipeline. The plan is executed task-by-task with marshal-executing-plans after user approval.
risk: read_only
---

# Marshal Plan Writing (non-SDD)

Use this skill after `brainstorming` has produced an approved spec (see
`docs/specs/`) and the work will be executed inline rather than through the
`/sdd` pipeline. You are authoring a plan, not implementing source changes.
The output is a plain Markdown plan that `marshal-executing-plans` — or a
human — executes task-by-task.

## Required workflow

1. Read the approved spec from `docs/specs/` first. The design must be
   locked before a plan is written — never propose a plan before the design
   is approved.
2. Inspect the repository before naming files, symbols, tests, or commands.
   Never invent an anchor: every path, symbol, and verification command in
   the plan must be verified to exist (or be a new file the plan creates).
3. Decompose the approved design into self-contained `## Task N: <title>`
   sections, ordered so dependency-defining tasks come first (see
   `work-decomposition` for finding the seams).
4. Give every task the same shape:
   - **Goal** — one sentence: what exists when this task is done.
   - **Files** — the exact paths touched.
   - **Steps** — the concrete edits, in order, encoded per the hybrid rule
     below.
   - **Verify** — the exact command(s) that prove it, and the expected
     outcome. A task without a runnable verification is not a task.
5. Keep tasks independently verifiable and committable: the executor runs
   one task's verification and commits it before starting the next
   (`<plan-slug>: task N — <task title>`). A task is one reviewable unit.
6. Header the plan with the goal, non-goals, assumptions, the base branch
   or commit it was authored against, and a link to the spec file.
7. Tail the plan with a final full-suite verification (`go test ./...`,
   `gofmt`, `go vet` per AGENTS.md) and integration notes.
8. Write the plan to `docs/plans/YYYY-MM-DD-<topic>-plan.md`. Commit it if
   the repo's ignore rules permit (check `.gitignore` first; if the path is
   ignored, note it in the plan and move on — never `git add -f`). A plan
   that lives only in chat dies with the context.
9. Self-review the plan against the checklist below, then present it for
   user approval before any execution.

## Encoding — verbatim code vs prose

Plans use a hybrid encoding. Choose per task based on the nature of the
change:

- **Mechanical changes** — new files, config entries, boilerplate, precise
  patch hunks. Embed the complete code verbatim: full file contents, or an
  exact `file.write_patch`-style SEARCH/REPLACE block. The executor applies
  it as-is.
- **Judgment changes** — edits entangled with existing code, refactors,
  anything whose exact text depends on what is actually there. Write prose
  steps plus verified anchors (symbol names, file:line, distinctive
  strings), each confirmed via `symbols.find` / `definition` / `hover` /
  `repo.search` before being written into the plan.

The executor applies embedded code verbatim; prose+anchor steps may be
adapted to the actual file contents.

This is the difference from `marshal-sdd-plan-authoring`, which encodes
exact `marshal.patch` / `marshal.run` operations for the `/sdd` pipeline.
If the change is so mechanical it could be encoded as exact patches,
consider `/sdd` instead; if it needs model judgment, prose steps are the
point.

## Plan self-review

Before presenting the plan, run this checklist:

- Every task self-contained and independently verifiable?
- Every anchor verified to exist?
- Code blocks complete and compilable in isolation?
- Verification commands correct per AGENTS.md?
- No placeholders or TBDs?
- Contradicts nothing in the spec?

Fix any finding before proceeding.

## User approval gate

Present the written plan for review. Only after a "yes" from the user, hand
off to `marshal-executing-plans` — or stop if the user wants to execute
later or manually. Never start executing before the plan is approved.

## Red flags

- "Task 3: implement the feature" — steps must be concrete enough that a
  fresh context could execute them without re-deriving the design.
- Invented symbols, test names, or commands. Verify or cut.
- Tasks that cannot be verified alone ("builds on task 2's untested state").
- One giant task — that is not a plan, it is a todo list with extra steps.
- Starting implementation because "it's quicker now". The plan is the
  artifact; execution is a separate, gated step.
- Embedded code that was not verified to compile in isolation.
- Prose anchors that were not checked before being written into the plan.
- Tasks with neither verification commands nor clear done-criteria.
