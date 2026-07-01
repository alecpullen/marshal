# 08. Roadmap and Milestones

## Phase 0: Prototype

Goal: prove the core loop.

Features:

- Go CLI app
- simple TUI chat
- one OpenAI-compatible provider
- Ollama config example
- streaming model responses
- manual file read tool
- shell command proposal
- user approval prompt
- basic patch proposal
- test command execution

Success criteria:

- user can ask a question about repo files
- agent can read files through tools
- agent can propose a shell command
- user can approve command
- command output returns to agent

## Phase 1: Useful single-agent MVP

Goal: daily usable single-agent coding assistant.

Features:

- provider profiles
- TUI panels for chat, plan, tools, diff
- repo search tool
- file read tool
- patch apply tool
- git status/diff tools
- command approval rules
- run tests
- session persistence
- simple project config

Success criteria:

- agent can inspect a repo
- agent can make a small patch
- agent can run tests
- agent can summarise changes
- user can inspect all actions

## Phase 2: Repo intelligence

Goal: better context than grep.

Features:

- SQLite project DB
- repo scanner
- file hashing
- file summaries
- Tree-sitter symbol index
- symbol search
- repo map generation
- context pack builder
- role-specific context budgets

Success criteria:

- agent can explain project structure
- agent can find relevant files through symbols/summaries
- context packs are compact and useful
- repeated tasks do not require full rediscovery

## Phase 3: Role-based model routing

Goal: different models for different agent roles.

Features:

- model presets
- agent profiles
- model router
- local/remote flags
- context budget per role
- routing transparency in TUI
- basic escalation rules

Success criteria:

- knowledge agent can use small model
- implementer can use coder model
- reviewer/planner can use stronger model
- user can see and override model choices

## Phase 4: Knowledge agent

Goal: persistent project brain.

Features:

- session summaries
- durable project memories
- memory confidence states
- stale memory detection
- file summary updates
- architecture notes
- test failure notes
- onboarding brief generation

Success criteria:

- project knowledge improves across sessions
- stale or contradicted memories are marked
- knowledge writes are evidence-backed

## Phase 5: Swarm runtime

Goal: coordinated specialist agents.

Features:

- shared task state
- planner/repo scout/implementer/tester/reviewer roles
- sequential swarm mode
- parallel read-only repo scouts
- write lock
- agent activity panel
- agent budgets

Success criteria:

- multiple agents can contribute to one task
- only one write path exists
- findings are merged into a coherent plan
- reviewer can catch implementer issues

## Phase 6: Plugin and MCP ecosystem

Goal: extensibility.

Features:

- MCP client support
- external tool registration
- project-level tool allowlist
- plugin manifest
- custom commands
- tool audit UI

Success criteria:

- users can connect external tools
- MCP tools are permissioned and logged
- native safety model still applies

## Suggested build order

```text
1. Provider abstraction
2. TUI streaming chat
3. Tool registry
4. Read/search/shell tools
5. Approval system
6. Patch tool
7. Git diff integration
8. SQLite session/project DB
9. Repo scanner
10. Tree-sitter symbol index
11. Repo map
12. Context pack builder
13. Role-based model router
14. Knowledge agent
15. Swarm runtime
16. MCP/plugin support
```

## MVP demo scenario

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
