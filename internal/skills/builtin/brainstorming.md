---
name: brainstorming
description: Explore requirements and design BEFORE writing code for a new feature, component, or behaviour change by interrogating the request one question at a time, presenting the design in sections for approval, writing the approved design to a spec artifact, and gating planning on a user review — use when the request is "let's build/add/design X", when the desired behaviour is not yet pinned down, or when you are about to enter plan mode.
risk: read_only
---

# Brainstorming

Turn an idea into an agreed design before any code exists.

MUST use before any creative work.

## Classify first, and say so out loud

- **Spike** — a feasibility question whose output is an answer, not code you keep.
- **Bounded** — a scoped change to a flow that already exists in this repo. If there is no existing flow to read, it is not bounded.
- **Architectural** — new subsystems, or changes that restructure how components fit together.

When torn between two, take the heavier one.

## Required workflow

### Phase 1 — Scope assessment

Classify the request as spike, bounded, or architectural (table above).

- **Spike** — stop here. Discuss the question verbally; produce no spec.
- **Bounded / architectural** — continue to Phase 2. Bounded work may compress to a single design section, but never skips approval.

### Phase 2 — Interrogation

Explore purpose, constraints, and success criteria with `question.ask` — ONE question per call. Never batch a list of questions; the method depends on follow-ups reacting to answers.

Every question must test something, not just gather:

- Challenge stated assumptions: "you said X — what if that isn't true?"
- Probe edge cases and failure modes: "what breaks if two runs race? what if the file is huge or missing?"
- Force prioritization: "which matters more here, speed or correctness?"
- Surface non-goals explicitly: "what are you deliberately not building?"

Continue until no new answer would change the design, then summarize the hardened understanding and proceed.

### Phase 3 — Approaches

Propose 2-3 approaches with trade-offs and a stated recommendation. Wait for the user's choice.

### Phase 4 — Design presentation

Present the design in sections, pausing for explicit approval after each:

1. Scope / architecture
2. Components & data flow
3. Error handling
4. Testing
5. Anything else the design needs

Pushback revises that section before moving on.

### Phase 5 — Spec artifact

Write the approved design to `docs/specs/YYYY-MM-DD-<topic>-design.md`, containing:

- Context
- The approved design sections
- Decisions & trade-offs
- Explicit non-goals
- Open questions

Commit the spec if the repo's ignore rules permit — check `.gitignore` first. If the path is ignored, note that in the spec and move on; never `git add -f`.

Then run an inline spec self-review:

- No placeholders or TBDs
- No internal contradictions
- No ambiguous requirements
- Scope matches what was approved

Fix any finding before proceeding.

### Phase 6 — User review gate & handoff

Ask the user to review the written spec; offer to adjust it. Only after approval, hand off to `marshal-writing-plans` (default) or `marshal-sdd-plan-authoring` when the user wants the /sdd pipeline.

## Deliberate omissions

- **No separate design-reviewer dispatch** — the review is inlined as the Phase 5 self-review checklist; builtin skills are single self-contained files and can't ship sidecar prompt files.
- **No visual-companion hook** — the flow is text-only.

## YAGNI

Cut every feature the request does not need. A design that lists what you are deliberately not building is stronger than one that quietly includes everything.

## Red flags

- "This is too simple to need a design" — simple means a short design, not no design.
- "I understand this kind of app, so it's bounded" — bounded measures the repo, not your familiarity.
- "It grew, but I'm nearly done" — hidden complexity upgrades the path. Stop and re-classify.
