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
2. Load every skill whose description matches the task (`skill.load`),
   BEFORE you start acting. Loading is cheap; redoing work the skill would
   have prevented is not.
3. When skills chain, load the next one as each stage begins, not all up
   front.

## Known chains

- New feature or behaviour change: brainstorming (interrogate → design
  sections → spec at `docs/specs/` → user review) → marshal-writing-plans
  (consumes the approved spec, plan at `docs/plans/` with verbatim code for
  mechanical edits and prose + verified anchors for judgment edits, then
  self-reviews and waits for user approval) → marshal-executing-plans
  (executes the approved plan task-by-task, applying embedded code verbatim).
- Anything misbehaving: systematic-debugging before any fix, even for
  open-ended investigations with no fix requested.
- Code changes: test-driven-development, then verification-before-completion
  before claiming done.
- Large or parallelisable work: work-decomposition, then
  dispatching-parallel-agents once the units are independent.

## Restraint

- Do not load a skill whose description does not match the task. Active
  skills are budgeted (`skills.max_active`, default 8) and their bodies
  consume context on every turn.
- A skill guides how you work; it never overrides an explicit user
  instruction.