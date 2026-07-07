# 08. Roadmap and Milestones

## Phase 0: Prototype ✅

Goal: prove the core loop.

Milestones A–B.

## Phase 1: Useful single-agent MVP ✅

Goal: daily usable single-agent coding assistant.

Milestones C–G (provider abstraction, tool registry, native tools, approval system, patch workflow).

## Phase 2: Repo intelligence ✅

Goal: better context than grep.

Milestones H–K (agent loop, SQLite persistence, repo indexing, context pack builder).

## Phase 3: Role-based model routing ✅

Goal: different models for different agent roles.

Milestone L.

## Phase 4: Knowledge agent ✅

Goal: persistent project brain.

Milestone N.

## Phase 5: Swarm runtime ✅

Goal: coordinated specialist agents.

Milestone O, plus phase 5 polish (tester feedback loop, roster activity panel, run-level budgets, token metering).

## Phase 6: Plugin and MCP ecosystem ✅

Goal: extensibility.

Milestone P.

## Next: Phase 7 — Sandboxed command execution

Goal: isolated, safe command execution with resource controls.

Features planned:

- container or jail-based shell execution
- per-command timeouts and memory limits
- network access policies
- audit trail of all executed commands
- no-host-access mode for untrusted operations

Success criteria:

- commands run in an isolated environment
- resource limits are enforced
- network can be restricted per command
- full audit trail is available

## Suggested build order (already executed)

```text
 1. Provider abstraction        ✅ Milestone C
 2. TUI streaming chat          ✅ Milestone B
 3. Tool registry               ✅ Milestone D
 4. Read/search/shell tools     ✅ Milestone E
 5. Approval system             ✅ Milestone F
 6. Patch tool                  ✅ Milestone G
 7. Git diff integration        ✅ Milestone G
 8. SQLite session/project DB   ✅ Milestone I
 9. Repo scanner                ✅ Milestone J
10. Tree-sitter symbol index    ✅ Milestone M
11. Repo map                    ✅ Milestone J
12. Context pack builder        ✅ Milestone K
13. Role-based model router     ✅ Milestone L
14. Knowledge agent             ✅ Milestone N
15. Swarm runtime               ✅ Milestone O
16. MCP/plugin support          ✅ Milestone P
17. Sandboxed command execution   🔜 Milestone Q
```

## MVP demo scenario (working)

```text
1. User runs `marshal` in a Go repo.
2. TUI opens.
3. User asks: "What does this project do?"
4. Agent scans repo and builds a repo map.
5. User asks: "Add a small test for X."
6. Agent reads relevant files.
7. Agent proposes a patch.
8. User approves.
9. Agent runs `go test`.
10. Agent summarises results.
```
