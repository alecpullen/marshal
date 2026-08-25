---
name: brainstorming
description: Explore requirements and design BEFORE writing code for a new feature, component, or behaviour change. Use when the request is "let's build/add/design X", when the desired behaviour is not yet pinned down, or when you are about to enter plan mode.
risk: read_only
---

# Brainstorming

Turn an idea into an agreed design before any code exists.

## Classify first, and say so out loud

- **Spike** — a feasibility question whose output is an answer, not code you keep.
- **Bounded** — a scoped change to a flow that already exists in this repo. If there is no existing flow to read, it is not bounded.
- **Architectural** — new subsystems, or changes that restructure how components fit together.

When torn between two, take the heavier one.

## Required workflow

1. Read the current state first — the files, `AGENTS.md`, recent commits.
2. Ask clarifying questions one at a time. Prefer questions whose answers change what you build; skip the ones with an obvious default.
3. Propose 2-3 approaches with trade-offs. Lead with your recommendation and say why.
4. Present the design, scaled to complexity: a few sentences for bounded work, sectioned for architectural.
5. **Stop and get explicit approval before implementing.** This gate never scales down — a two-sentence design still needs a yes.

## YAGNI

Cut every feature the request does not need. A design that lists what you are deliberately not building is stronger than one that quietly includes everything.

## Red flags

- "This is too simple to need a design" — simple means a short design, not no design.
- "I understand this kind of app, so it's bounded" — bounded measures the repo, not your familiarity.
- "It grew, but I'm nearly done" — hidden complexity upgrades the path. Stop and re-classify.
