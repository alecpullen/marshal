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

Recommended Go layout:

```text
cmd/marshal
  main.go

internal/app
  tui/
  config/
  session/
  logging/

internal/llm
  provider/
  schema/
  streaming/
  toolcalling/

internal/agent
  loop/
  planner/
  executor/
  reviewer/
  swarm/
  prompts/
  memory/

internal/tools
  registry/
  filesystem/
  shell/
  git/
  search/
  treesitter/
  test_runner/
  mcp/

internal/repo
  scanner/
  indexer/
  repomap/
  symbols/
  graph/
  summaries/

internal/db
  sqlite/
  migrations/
  embeddings/
  events/

internal/sandbox
  policy/
  approvals/
  command_classifier/

internal/patch
  diff/
  apply/
  rollback/
```

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
