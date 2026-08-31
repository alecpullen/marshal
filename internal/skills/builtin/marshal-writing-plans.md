---
name: marshal-writing-plans
description: Author a written implementation plan for inline (non-SDD) execution — decompose approved work into ordered, self-contained tasks with exact files, steps, and verification commands. Use after a design is approved, when not using the /sdd pipeline. The plan is executed task-by-task with marshal-executing-plans.
risk: read_only
---

# Marshal Plan Writing (non-SDD)

Use this skill after a design has been approved (see `brainstorming`) when
the work will be executed inline rather than through the `/sdd` pipeline.
You are authoring a plan, not implementing source changes. The output is a
plain Markdown plan that `marshal-executing-plans` — or a human — executes
task-by-task.

## Required workflow

1. Inspect the repository before naming files, symbols, tests, or commands.
   Never invent an anchor: every path, symbol, and verification command in
   the plan must be verified to exist (or be a new file the plan creates).
2. Decompose the approved design into self-contained `## Task N: <title>`
   sections, ordered so dependency-defining tasks come first (see
   `work-decomposition` for finding the seams).
3. Give every task the same shape:
   - **Goal** — one sentence: what exists when this task is done.
   - **Files** — the exact paths touched.
   - **Steps** — the concrete edits, in order.
   - **Verify** — the exact command(s) that prove it, and the expected
     outcome. A task without a runnable verification is not a task.
4. Keep tasks independently verifiable and committable: the executor runs
   one task's verification and commits it before starting the next
   (`<plan-slug>: task N — <task title>`). A task is one reviewable unit.
5. Header the plan with the goal, non-goals, assumptions, and the base
   branch or commit it was authored against.
6. Write the plan to a file — a plan that lives only in chat dies with the
   context. Check the repo's conventions first (`AGENTS.md`, `docs/`); if
   there are none, ask where plans should live or use a gitignored location.
7. Hand off: load `marshal-executing-plans` and execute, or return the plan
   path to the user for review.

## Encoding

Prose and fenced code are the whole format — there are no executable
blocks. That is the difference from `marshal-sdd-plan-authoring`, which
encodes exact `marshal.patch` / `marshal.run` operations for the `/sdd`
pipeline. If the change is so mechanical it could be encoded as exact
patches, consider `/sdd` instead; if it needs model judgment, prose steps
are the point.

## Red flags

- "Task 3: implement the feature" — steps must be concrete enough that a
  fresh context could execute them without re-deriving the design.
- Invented symbols, test names, or commands. Verify or cut.
- Tasks that cannot be verified alone ("builds on task 2's untested state").
- One giant task — that is not a plan, it is a todo list with extra steps.
- Starting implementation because "it's quicker now". The plan is the
  artifact; execution is a separate, gated step.