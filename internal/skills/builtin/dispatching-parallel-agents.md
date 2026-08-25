---
name: dispatching-parallel-agents
description: Run 2+ already-decomposed, genuinely independent tasks concurrently via agent.run. Use only after the work is split into units with disjoint files and no shared state. Not for a single task, and not for tasks that edit the same files.
risk: workspace_write
---

# Dispatching Parallel Agents

## Before you dispatch

Confirm all three. If any fails, do the work sequentially instead.

1. **Decomposed** — the units exist and each has one deliverable. If not, use `work-decomposition` first.
2. **Independent** — disjoint file sets, no unit needing another's output.
3. **Worth it** — 2+ units, each substantial. Parallelising trivial work costs more in coordination than it saves.

## Dispatching

Use the `agent.run` tool, one call per unit. Multiple `agent.run` calls in a single response all start immediately.

`agent.run` returns a **handle, not a result** — the child runs in the background. The task is not done when the call returns.

- `agent.await` — block until a subagent finishes and collect its report.
- `agent.output` — peek at progress without blocking.

Writes across agents are serialised by a write lock, so concurrent edits cannot interleave mid-write. That protects file integrity; it does **not** make two agents editing the same file correct. Keep file sets disjoint.

## Writing the task brief

Each child starts with a **cold context** — it sees only what you write. Include:

- The deliverable, stated concretely.
- Exact file paths to create or modify.
- Signatures and types it must consume from other units, spelled out — the child cannot see its siblings' work.
- How to verify: the exact command and the expected outcome.
- Any constraint from `AGENTS.md` that applies.

A brief that says "implement the parser" produces work you throw away.

## Collecting

`agent.await` every subagent you started. Review each report against its stated deliverable before integrating. A subagent reporting success is a claim, not evidence — check the verification actually ran.

If two children's work conflicts on integration, that is a decomposition failure, not a merge problem. Re-split and re-run.
