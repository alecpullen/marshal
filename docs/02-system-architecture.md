# 02. System Architecture

## High-level architecture

```text
TUI
  ↓
Session Controller
  ↓
Agent Runtime
  ↓
Model Router ───────────────→ Provider Adapters
  ↓                              ↓
Tool Broker                  Ollama / LM Studio / vLLM / OpenRouter / Generic API
  ↓
Native Tools / MCP Tools / Plugins
  ↓
Repository Index / Project DB / Shell / Filesystem / Git
```

## Core modules

Current implementation layout:

```text
cmd/marshal
  main.go                          — thin entrypoint

internal/app
  app.go                           — Run(), dependency wiring, signal handling
  config/                          — TOML config loading, defaults, merge rules
  session/                         — in-memory app state, message list
  tui/                             — Bubble Tea model (View/Update/Init)
  logging/                         — slog logger construction

internal/agent
  runner.go                        — single-agent loop (RunTask, execute tool calls, finalize)
  finalize.go                      — salvaged completion / fallback answer synthesis
  progress.go                      — stall detection (exact repeat, read-only churn)
  protocol.go                      — JSON action envelope parsing
  prompts.go                       — system prompts, planning prompt, correction messages
  task.go                          — Task type and classification
  swarm/                           — multi-agent orchestration, lock, state, verdict

internal/commands
  commands.go                      — slash commands (/plan, /test, /profile, …)

internal/contextpack
  pack.go                          — context pack builder, token budgets, memory merge

internal/db
  db.go                            — SQLite connection and migrations
  symbols.go                       — symbol DB schema and queries

internal/knowledge
  knowledge.go                     — durable project memory agent
  prompts.go                       — knowledge agent prompts
  protocol.go                      — knowledge protocol types

internal/llm
  provider/                        — Provider interface and implementations
  schema/                          — ChatRequest, ChatMessage, ChatEvent types
  streaming/                       — streaming response handling
  routing/                         — route resolver, model presets, role profiles

internal/repo
  scanner.go                       — file scanning and gitignore
  map.go                           — repo map and card generation
  symbols.go                       — tree-sitter Go symbol extraction

internal/skills
  skill.go                         — skill type and index
  loader.go                        — skill loader
  tool.go                          — skill.load tool registration

internal/tools
  registry/                        — tool registration and dispatch
  native/                          — file, search, shell, git, repo, symbols
  patch/                           — diff apply, preview, and approval
  policy/                          — command approval and risk policy
  mcp/                             — MCP client, protocol, manager
```

Key design decisions:
- No separate `loop/`, `planner/`, `executor/`, `reviewer/` sub-packages — the single-agent loop lives in `runner.go` which handles planning, execution, and stall recovery.
- `internal/agent/swarm/` reuses `Runner` from `internal/agent` with role-specific prompts and a shared `session.State`.
- `internal/commands/` is a flat package of slash-command handlers, not a sub-directory per command.
- `internal/skills/` is a separate concern from tools — skills inject instruction sets, tools execute operations.
- `internal/db/` is a flat package (no `sqlite/`, `migrations/`, `events/` sub-packages).
- The planned `internal/sandbox/` (policy, approvals, command classifier) is not yet implemented — that is Milestone Q.

## Main runtime flow

```text
1. User submits request in TUI
2. Session Controller creates Task object
3. Agent Runtime classifies task
4. Context Manager builds initial context pack
5. Model Router chooses model preset for active agent role
6. Provider Adapter sends prompt/model request
7. Agent emits answer, tool request, or patch proposal
8. Tool Broker validates and executes approved tools
9. Results return to Agent Runtime
10. Agent updates plan and context
11. Patches/tests/reviews happen as needed
12. Session summary and durable project memories are stored
```

## Core data types

### Task

```go
type Task struct {
    ID          string
    Goal        string
    Mode        AgentMode
    Risk        RiskLevel
    Status      TaskStatus
    Plan        []PlanStep
    Constraints []string
    CreatedAt   time.Time
}
```

### Agent role

```go
type AgentRole string

const (
    RoleRouter           AgentRole = "router"
    RolePlanner          AgentRole = "planner"
    RoleRepoScout        AgentRole = "repo_scout"
    RoleImplementer      AgentRole = "implementer"
    RoleTester           AgentRole = "tester"
    RoleReviewer         AgentRole = "reviewer"
    RoleSecurityReviewer AgentRole = "security_reviewer"
    RoleKnowledge        AgentRole = "knowledge"
    RoleSummarizer       AgentRole = "summarizer"
)
```

### Agent mode

```go
type AgentMode string

const (
    ModeAsk   AgentMode = "ask"   // answer only
    ModePlan  AgentMode = "plan"  // plan only, no edits
    ModeEdit  AgentMode = "edit"  // propose patches
    ModeAuto  AgentMode = "auto"  // approved tools and edits
    ModeSwarm AgentMode = "swarm" // multi-agent task runtime
)
```

## System boundaries

### TUI

Responsible for:

- rendering chat, diffs, logs, plans, context, and approvals
- collecting user input
- displaying model/provider state
- showing tool activity and risk prompts

Not responsible for:

- model routing logic
- tool execution policy
- prompt construction
- repository indexing

### Agent Runtime

Responsible for:

- planning
- model calls
- tool selection
- task state
- reflection/retry loops
- final summaries

### Tool Broker

Responsible for:

- tool registration
- schema validation
- risk classification
- user approval checks
- execution
- audit logging

### Context Manager

Responsible for:

- building compact context packs
- selecting relevant files/symbols/summaries
- managing token budgets
- deciding when to fetch raw code

### Knowledge Agent

Responsible for:

- maintaining project summaries
- updating durable facts
- curating memories
- detecting stale project knowledge
- summarising sessions

## Design principle

The model should be replaceable. The durable value of the product should live in the TUI workflow, repository intelligence, safety system, model routing, and project memory.
