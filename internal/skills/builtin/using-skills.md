---
name: using-skills
description: Entry point for the skill suite. Before starting non-trivial work, scan the skill roster and load every skill whose description matches the task. Autoloaded by default so the habit is always on.
risk: read_only
---

# Using Skills

Skills are procedures, not reference material. A skill that matches your
task but is never loaded might as well not exist.

## Before acting on non-trivial work

1. Scan the skill roster in your system prompt.
2. Load the skill whose description matches the stage you are starting
   (`skill.load`), BEFORE you act. A loaded skill's body sits in context on
   every later turn, so load for the current stage, not for every stage you
   can imagine.
3. When skills chain, load the next one as each stage begins, not all up
   front.

## Known chains

- New feature or behaviour change: brainstorming (interrogate → design
  sections → spec at the repo's spec location, default `docs/specs/` → user review) → marshal-writing-plans
  (consumes the approved spec, plan at the repo's plan location, default `docs/plans/`, with verbatim code for
  mechanical edits and prose + verified anchors for judgment edits, then
  self-reviews and waits for user approval) → marshal-executing-plans
  (settles gaps and asks inline-vs-delegated once up front, then executes
  the approved plan task-by-task, applying embedded code verbatim).
- Anything misbehaving: systematic-debugging before any fix, even for
  open-ended investigations with no fix requested.
- Code changes: test-driven-development, then verification-before-completion
  before claiming done.
- Executing a written plan: marshal-executing-plans alone. It already
  covers delegation, sequencing, and verification. Do not also load
  dispatching-parallel-agents or work-decomposition for plan tasks.
- Large unplanned work: work-decomposition, then
  dispatching-parallel-agents only if the units are independent (disjoint
  files AND separate build/test units).

## Restraint

- Do not load a skill whose description does not match the task. Active
  skills are budgeted (`skills.max_active`, default 8) and their bodies
  consume context on every turn.
- Two skills that describe the same job (for example two plan-execution
  procedures) conflict. Load one.
- A skill guides how you work; it never overrides an explicit user
  instruction.