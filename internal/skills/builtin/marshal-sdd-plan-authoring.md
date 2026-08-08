---
name: marshal-sdd-plan-authoring
description: Author a reviewed Marshal executable SDD plan from an approved feature design.
risk: read_only
---

# Marshal SDD Plan Authoring

Use this skill only after the feature design has been approved. You are
authoring a plan, not implementing source changes.

## Required Workflow

1. Inspect the repository before naming files, symbols, anchors, tests, or commands.
2. Decompose the approved design into self-contained `## Task N:` sections.
3. Keep the normal human-readable plan prose: goal, files, interfaces, steps,
   and validation intent.
4. Encode exact work with `marshal.*` blocks only when the inspected repository
   proves the preconditions.
5. Use a narrow `marshal.agent` block when exact deterministic encoding is not
   safe. State the allowed files and why model work remains.
6. Add structural and behavioral assertions for every mutation task.
7. Write exactly one candidate plan to the provided `@plan/<filename>.md` path.
8. Return the path and a concise operation summary. Do not edit source files,
   run implementation commands, commit, or start `/sdd`.

## Executable Blocks

Use only the versioned Marshal contract:

- `marshal.patch` for one exact unique replacement in an existing file.
- `marshal.file` for a new file or an explicitly marked replacement.
- `marshal.run` for an approved prepare or verify command.
- `marshal.assert` for a structural or behavioral postcondition.
- `marshal.agent` for unresolved implementation work with a narrow scope.

Ordinary prose and ordinary code fences are never executed. Never invent a
precondition, command result, symbol, or test name. If the exact operation is
not provable, use scoped fallback rather than guessing.

## Handoff

This is the Marshal replacement for generic `writing-plans` only in the SDD
workflow. Do not load or invoke generic `writing-plans`,
`subagent-driven-development`, or `executing-plans` from this context. The next
stage is user review followed by `/sdd`.