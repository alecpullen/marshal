---
name: marshal-executing-plans
description: Execute a written implementation plan task-by-task with one commit per task. Supersedes the executing-plans skill.
risk: workspace_write
---

# Marshal Plan Execution

Use this skill when executing a written implementation plan inline (outside
the /sdd pipeline). This skill supersedes executing-plans: where they
conflict, follow this skill.

## Required Workflow

1. Read the whole plan first. Load only the task you are working on.
2. Implement exactly one task at a time; do not start the next task until
   the current one verifies.
3. Run the task's verification command(s) and confirm they pass.
4. Commit after each verified task:
   - Stage only the files the task touched (`git add <paths>`), never
     `git add -A` on an unfamiliar tree.
   - Commit message: `<plan-slug>: task N — <task title>`.
   - If the tree was already dirty before you started, do not sweep
     pre-existing changes into your commits.
5. Mark the task complete in your todo list, then move to the next task.
6. When all tasks verify, report a summary with the list of commits
   (`git log --oneline <base>..HEAD`).

Never commit failing work "to fix later". If a task cannot be verified,
stop and report instead of committing.

## Applying task content

- Embedded code blocks in a task are applied **verbatim** — do not reformat,
  reorder, or "improve" them.
- Prose steps may be adapted to the actual file contents, but every anchor
  (symbol name, file:line, distinctive string) must be re-verified before
  editing; if an anchor no longer matches, stop and report rather than
  guessing.
