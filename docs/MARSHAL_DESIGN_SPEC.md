# Marshal — Design Specification

**Version** 0.6 — Open skills system with three-tier resolution  
**Date** March 2026  
**Language** Go  

> **Changes in v0.6**
>
> Section 4.6 (Skills system) expanded to cover three-tier resolution (project > user-global > built-in), full skill schema with `[meta]` block, complete `marshal skills` command surface, and community skills distribution model. Updated feature roadmap and resolved design decisions.


---


# 1. Project Overview


## 1.1 What is Marshal?


Marshal is a self-hosted multi-model coding agent orchestrator. It manages an automated loop between a code executor model and a code critic model, applies changes to a real git repository, and presents the process through a terminal-native TUI built with Bubble Tea.


The distinction from existing tools like OpenCode or Aider is that Marshal is loop-first: the orchestration logic — how feedback flows between models, how verdicts are parsed, when to retry, when to commit, when to give up — is the product. Models are interchangeable backends.


Phase 3 extends Marshal from a single-task tool into a multi-agent pipeline capable of decomposing large features into dependency-ordered tasks and executing independent tasks in parallel across isolated branches.


## 1.2 The problem it solves


Running two LLM models in a critic/executor pattern against a real codebase requires manual intervention with existing tools. Marshal removes that. Once a task is submitted, the loop runs to completion without further input.


At the feature level, large tasks that span multiple files and modules require decomposition, sequencing, and coordination that no single loop iteration can handle. The multi-agent pipeline addresses this by giving Marshal a planner that understands dependencies and a scheduler that can run independent work in parallel.


> **Core insight**
>
> The critic/executor pattern produces measurably better code than single-model generation. At scale, the dependency-aware parallel pipeline extends that quality guarantee to feature-sized work — not just individual task-sized changes.


## 1.3 Technology stack


| Component | Technology | Rationale |
| --- | --- | --- |
| Language | Go | Single binary, strong stdlib, excellent TUI ecosystem. Goroutines make parallel agent execution natural. |
| TUI framework | Bubble Tea + Lip Gloss | Elm-style architecture. The DAG progress view in Phase 3 is a natural Bubble Tea component. |
| CLI framework | Cobra | Idiomatic multi-command Go CLI. |
| Session storage | SQLite via database/sql | Per-project `.marshal/sessions.db`. Extended in Phase 3 for pipeline runs and task dependency graphs. |
| Config format | TOML via BurntSushi/toml | Human-readable, git-committable, env var interpolation. |
| Model client | openai-go SDK | Works against RunPod, Ollama, Anthropic, and any vLLM deployment. |
| Git operations | os/exec wrapping git CLI | Extended in Phase 3: concurrent isolation branches, topological merge ordering. |


---


# 2. Architecture


## 2.1 Layer overview


| Layer | Technology | Responsibility |
| --- | --- | --- |
| TUI | Bubble Tea + Lip Gloss | Live loop view, session browser, diff viewer, config screen, think-block panel, DAG view (Phase 3) |
| CLI | Cobra | `marshal run`, `marshal pipeline`, `marshal sessions`, `marshal config`. Headless `--no-tui --json` for CI. |
| Pipeline (Phase 3) | Pure Go | Scheduler: dependency graph parsing, topological sort, goroutine fan-out, result collection, integration critic |
| Loop engine | Pure Go | Orchestrator for a single task: rounds, compaction, feedback injection, verdict parsing, retry |
| Agent layer | Pure Go | Executor, Critic, Planner, Integration Critic structs. System prompts, temperature, structured output. |
| Session store | database/sql + SQLite | Sessions, rounds, tokens, verdicts, diffs. Extended: `pipeline_runs`, `task_graph` tables. |
| Provider layer | openai-go SDK | `Backend` interface + `OpenAICompatibleBackend`. Streaming, caching awareness, concurrent-safe. |
| Git layer | os/exec | `GitContext`: branch, diff, commit, revert, merge. Extended for concurrent branch management. |


## 2.2 Project structure


```
marshal/
├── cmd/marshal/main.go
├── internal/
│   ├── backend/
│   │   ├── backend.go             Backend interface
│   │   └── openai_compat.go       OpenAICompatibleBackend
│   ├── config/
│   │   └── config.go              marshal.toml loader
│   ├── loop/
│   │   ├── loop.go                Single-task orchestrator
│   │   ├── executor.go            Executor agent
│   │   ├── critic.go              Critic agent + JSON verdict parser
│   │   ├── compactor.go           Compaction interface
│   │   └── retry.go               Transient retry
│   ├── pipeline/                  Phase 3
│   │   ├── pipeline.go            Multi-task scheduler
│   │   ├── graph.go               DAG + topological sort
│   │   ├── planner.go             Planner agent
│   │   └── integration.go         Integration critic
│   ├── git/
│   │   └── git.go                 GitContext
│   ├── store/
│   │   └── store.go               SQLite store
│   └── tui/
│       ├── app.go
│       ├── loop_view.go
│       ├── session_view.go
│       ├── diff_view.go
│       └── dag_view.go            Phase 3
├── .marshal/skills/               Project-local skills
├── marshal.toml
├── go.mod
└── go.sum
```


## 2.3 The Backend interface


```go
type Message struct {
    Role    string // "system" | "user" | "assistant"
    Content string
}

type Response struct {
    Content          string
    PromptTokens     int
    CompletionTokens int
    CacheHit         bool
    CachedTokens     int
}

type Backend interface {
    Complete(ctx context.Context, model string, messages []Message) (Response, error)
    Name() string
}
```


## 2.4 Configuration — marshal.toml


```toml
[executor]
model       = "RedHatAI/Devstral-Small-2507-quantized.w8a8"
base_url    = "https://api.runpod.ai/v2/ENDPOINT_ID/openai/v1"
api_key     = "${RUNPOD_API_KEY}"
temperature = 0.2
max_tokens  = 2048

[critic]
model       = "deepseek-ai/DeepSeek-R1-Distill-Qwen-14B"
base_url    = "https://api.runpod.ai/v2/ENDPOINT_ID/openai/v1"
api_key     = "${RUNPOD_API_KEY}"
temperature = 0.0
max_tokens  = 1024
json_output = true

# Phase 3
[planner]
model       = "deepseek-ai/DeepSeek-R1-Distill-Qwen-14B"
base_url    = "https://api.runpod.ai/v2/ENDPOINT_ID/openai/v1"
api_key     = "${RUNPOD_API_KEY}"
temperature = 0.0
max_tokens  = 2048

[integration_critic]
model       = "deepseek-ai/DeepSeek-R1-Distill-Qwen-14B"
base_url    = "https://api.runpod.ai/v2/ENDPOINT_ID/openai/v1"
api_key     = "${RUNPOD_API_KEY}"
temperature = 0.0
max_tokens  = 2048

[loop]
max_rounds        = 3
auto_commit       = true
auto_revert       = true
branch_isolation  = true
compact_after     = 2
token_budget_warn = 0.80

[pipeline]
max_parallel = 3
fail_fast    = false

[session]
db_path = ".marshal/sessions.db"

[retry]
max_attempts       = 3
initial_backoff_ms = 1000
backoff_factor     = 2.0
retry_status_codes = [429, 502, 503]
```


---


# 3. Loop Engine


## 3.1 Lifecycle — single task


| Step | Detail |
| --- | --- |
| 1. Task submitted | Session created in `.marshal/sessions.db`. HEAD SHA recorded. Isolation branch created. |
| 2. Token budget check | Estimated token count checked. Warning at `token_budget_warn`. Blocked at 95%. |
| 3. Compaction check | If `round > compact_after`, `Compactor.Compact()` called. |
| 4. Executor invoked | Messages built: system prompt + task + history + critic feedback. `Backend.Complete()` called. |
| 5. Diff extracted | `GitContext.Diff()` called. Unified diff appended to critic context. |
| 6. Critic invoked | Receives task + executor prose + git diff. Temperature 0. Think-blocks stripped. JSON verdict parsed. |
| 7a. PASS | Branch merged. Changes committed. Session marked success. Exit 0. |
| 7b. FAIL | Feedback stored. If rounds remain, return to step 2. If exhausted, proceed to step 8. |
| 8. Exhaustion | Hard reset to recorded HEAD SHA. Isolation branch deleted. Session marked failed. Exit 1. |


## 3.2 Structured critic output


```json
{
  "verdict":  "PASS" | "FAIL",
  "summary":  "one sentence explaining the verdict",
  "issue":    "specific problem identified (FAIL only)",
  "fix":      "exactly what needs to change (FAIL only)",
  "concerns": ["optional non-blocking observations"]
}
```


## 3.3 Branch isolation


| Event | Git operation |
| --- | --- |
| Session start | `git checkout -b marshal/task-<id>` from current HEAD |
| PASS | checkout original → merge `marshal/task-<id>` → delete branch |
| Exhaustion | checkout original → delete `marshal/task-<id>` without merge |
| Unhandled error | Attempt checkout of original. Log branch for manual cleanup. |


## 3.4 Context compaction


```go
type Compactor interface {
    Compact(ctx context.Context, rounds []Round) ([]Message, error)
}
// Phase 1: PassThroughCompactor (no-op hook)
// Phase 2: SummaryCompactor    (real summarisation)
```


---


# 4. AI-Specific Features


## 4.1 Prompt caching


`CacheHit` and `CachedTokens` stored per model call. Enable on RunPod with `ENABLE_PREFIX_CACHING=true`. The system prompt — identical across rounds — is the primary cache beneficiary.


## 4.2 Temperature per agent


| Agent | Recommended | Rationale |
| --- | --- | --- |
| Executor | 0.1 – 0.3 | Low for correct code. Some variance for creative solutions on retry. |
| Critic | 0.0 | Deterministic. Consistent verdicts required. |
| Planner | 0.0 | Decomposition must be consistent and reproducible. |
| Integration critic | 0.0 | Final review must be deterministic. |
| Compactor (Phase 2) | 0.0 – 0.1 | Summarisation should be faithful, not creative. |


## 4.3 Token budget


| Threshold | Action |
| --- | --- |
| < `token_budget_warn` (default 80%) | No action. |
| >= `token_budget_warn` | Warning in TUI statusbar. |
| >= 95% | Request blocked. User prompted to compact or reduce scope. |


## 4.4 Executor tool use — Phase 3 north star


Phases 1 and 2 operate in advisory mode. Phase 3 adds autonomous file editing via tool calls. Branch isolation is the primary safety mechanism. `Backend` will be extended with `CompleteWithTools()` additively.


## 4.5 Security-first system prompts


Marshal is security-focused by design. Every executor and critic call includes a standing instruction layer — non-negotiable security rules that apply to every task, every session, regardless of what the user asks for.


> ⚠️ **Design principle**
>
> The standing instruction layer is part of the base system prompt for all agents. Skills (Section 4.6) extend this foundation. Security is always on — even when no skill is active and even when the task description says nothing about security.


### Executor standing instructions


```
# Security standing instructions (executor)
# Prepended to every executor system prompt.

Input and data handling:
- Never trust user input. Validate and sanitise at the boundary where
  data enters the system, before it reaches business logic.
- Never construct database queries with string interpolation.
  Always use parameterised queries or the ORM query builder.
- Never deserialise untrusted data into executable objects.

Secrets and credentials:
- Never hardcode secrets, API keys, tokens, or passwords.
  Always read from environment variables or the established secrets pattern.
- Never log sensitive values: passwords, tokens, PII, session identifiers.

Authentication and authorisation:
- Never create a route or endpoint without the established auth middleware.
- Never bypass auth for convenience — use test fixtures instead.
- Default to least privilege. Request only the permissions a function needs.

Dependencies:
- Do not introduce new third-party dependencies without flagging them
  explicitly in your output. New dependencies are a security surface.

Cryptography:
- Never implement custom cryptographic operations.
  Use the established library pattern already in the codebase.
- Never use MD5 or SHA-1 for security-sensitive hashing.
```


### Critic standing security checks


```
# Security standing checks (critic)
# Appended to every critic system prompt.

In addition to correctness review, check the diff for:

- Unvalidated user input entering the system without boundary validation
- Raw query construction using string interpolation
- Auth bypass: new routes missing the established middleware
- Hardcoded secrets: string literals resembling credentials or tokens
- Sensitive logging: passwords, tokens, or user data in log statements
- New dependencies: flag any new imports or package entries explicitly

If any of these are present, the verdict is FAIL regardless of
whether the functional requirements are otherwise met.
```


## 4.6 Skills system


A skill is a named, reusable bundle of system prompt additions and configuration overrides that specialises the executor and critic for a specific task domain. The skills system is open — anyone can write a skill as a plain TOML file and share it. No installation step, no manifest to update, no compilation required.


### Three-tier resolution


When a skill is requested, Marshal searches three locations in priority order and uses the first match:


```
1. .marshal/skills/            Project-local. Committed to the repo. Highest priority.
                               Use for project-specific conventions and overrides.

2. ~/.config/marshal/skills/   User-global. Applies to all projects on this machine.
                               Use for personal preferences that span projects.

3. built-in/                   Shipped inside the Marshal binary. Always available.
                               Lowest priority — overridden by either tier above.
```


### Skill file schema


```toml
# .marshal/skills/schema-migration.toml

[meta]
name        = "schema-migration"
version     = "1.2.0"
description = "Prisma schema changes and PostgreSQL migrations"
author      = "alecpullen"
tags        = ["database", "prisma", "postgresql"]
marshal_min = "0.1.0"

[executor]
temperature = 0.1
system_prompt_additions = """
You are specialised in Prisma v7 schema changes and PostgreSQL migrations.

Additional rules for this skill:
- Always generate a descriptive migration name matching the change
- Never touch prisma/generated/ — auto-generated, never modify
- Always add an index on any new foreign key column
- For column renames: two-step migration (add + backfill + drop)
- Output: files changed, migration command, risk level (low/medium/high)
"""

[critic]
temperature = 0.0
system_prompt_additions = """
Additional checks (beyond the standard security standing checks):
- Destructive op without a safety plan: FAIL
- Non-nullable column on existing table without default: FAIL
- Missing index on new foreign key: FAIL
"""

[context]
auto_inject = ["prisma/schema.prisma"]
```


> ⚠️ **system_prompt_additions only — no override**
>
> The skill schema has `system_prompt_additions`, not `system_prompt` or `system_prompt_override`. Skills can only add to the base prompt and security layer. A skill file containing any other prompt key is rejected with a validation error.


### Built-in skills


| Skill | Executor specialisation | Critic specialisation |
| --- | --- | --- |
| schema-migration | Prisma schema and migration conventions. Two-step renames. Index requirements. | Database safety: destructive ops, nullable columns, lock duration, index coverage. |
| security-audit | Find vulnerabilities: injection, auth bypass, insecure defaults, OWASP Top 10. | Verify the vulnerability is genuinely exploitable. Assess severity and remediation quality. |
| test-generation | Write tests for existing code. Edge cases, boundary values, error paths. | Test quality: behaviour vs implementation, mock realism, edge case coverage. |
| documentation | Rewrite comments, JSDoc, README for an unfamiliar reader. | Evaluate whether someone unfamiliar with the codebase would understand the result. |
| dependency-audit | Audit a dependency: licence, security history, maintenance, alternatives. | Verify the audit is complete and the recommendation is sound. |


### Command surface


```bash
# Run with a skill
marshal run --skill schema-migration "add venue column to events table"
marshal pipeline --skill schema-migration "refactor events schema for multi-venue"

# List and inspect
marshal skills list                     # all skills from all tiers
marshal skills list --tag database      # filter by tag
marshal skills list --source built-in   # only built-in
marshal skills list --source user       # only ~/.config/marshal/skills/
marshal skills list --source project    # only .marshal/skills/

marshal skills show schema-migration    # full prompt additions + metadata
marshal skills which schema-migration   # which file is actually loaded and why

marshal skills new my-skill             # interactive scaffold → .marshal/skills/my-skill.toml
marshal skills new --global my-skill    # scaffold to ~/.config/marshal/skills/
marshal skills validate my-skill.toml  # validate schema — exits 0 if valid, 1 with errors
```


### Community skills


Because a skill is a plain TOML file, sharing one is as simple as sharing any text file:


```bash
# Install a community skill to your user-global directory
curl -fsSL https://raw.githubusercontent.com/marshal-skills/community/main/skills/nextjs-api-route.toml \
  -o ~/.config/marshal/skills/nextjs-api-route.toml

# Or commit it into the project so the whole team gets it
curl -fsSL https://raw.githubusercontent.com/.../nextjs-api-route.toml \
  -o .marshal/skills/nextjs-api-route.toml
git add .marshal/skills/nextjs-api-route.toml
git commit -m "chore: add nextjs-api-route marshal skill"
```


### Prompt layering


```
final_executor_prompt =
    base_executor_prompt              // project context, stack, conventions
  + security_standing_instructions   // always on, non-negotiable
  + skill.executor.system_prompt_additions  // task-domain specialisation

final_critic_prompt =
    base_critic_prompt               // verdict format, JSON schema
  + security_standing_checks         // always on, non-negotiable
  + skill.critic.system_prompt_additions    // task-domain review focus
```


## 4.7 Feature roadmap


| Feature | Phase | Status |
| --- | --- | --- |
| Structured JSON critic output | 1 | Planned |
| Diff-aware critic prompting | 1 | Planned |
| Independent temperature per agent | 1 | Planned |
| Transient failure retry | 1 | Planned |
| Branch isolation (single task) | 1 | Planned |
| Prompt caching awareness | 1 | Planned |
| Token budget warnings | 1 | Planned |
| Compaction hook (pass-through) | 1 | Planned |
| Security standing instruction layer | 1 | Planned |
| Three-tier skill resolution | 1 | Planned |
| Five built-in skills | 1 | Planned |
| `--skill` flag, `marshal skills list/show/which/validate` | 1 | Planned |
| Real context compaction | 2 | Planned |
| Think-block TUI panel | 2 | Planned |
| `marshal skills new` scaffold, tag filtering | 2 | Planned |
| Skill auto-injection of context files | 2 | Planned |
| Planner agent with dependency graph output | 3 | Planned |
| Sequential pipeline execution | 3 | Planned |
| Parallel pipeline execution | 3 | Planned |
| Integration critic | 3 | Planned |
| DAG progress view in TUI | 3 | Planned |
| Skill routing per-task in pipeline | 3 | Planned |
| Executor tool use (autonomous file editing) | 3 | Stretch |
| VS Code / Neovim plugin | 3 | Stretch |
| Cost tracking dashboard | 3 | Stretch |
| Community skills repository | 3 | Stretch |


---


# 5. TUI Design


Full visual design specification: see [TUI Design](./MARSHAL_TUI_DESIGN.md). This section covers Phase 3 additions only.


## 5.1 Pipeline input


The task composer gains a pipeline mode toggle. When enabled, the submitted description is sent to the planner rather than the executor. The composer label changes from "task" to "feature".


## 5.2 DAG progress view (Phase 3)


| Element | Content |
| --- | --- |
| DAG diagram | Task nodes by dependency tier. Colours: pending (dim), running (amber, pulsing), passed (green), failed (red), blocked (dim). |
| Active task panels | One live log panel per running task, stacked vertically. Each has its own agent badge and streaming output. |
| Pipeline summary bar | "N tasks · N running · N passed · N failed · N pending". Aggregate token totals. |
| Statusbar | Pipeline ID · target branch · elapsed time · `fail_fast` status |


## 5.3 Headless pipeline JSON


```jsonl
{"event":"pipeline_start","pipeline_id":"pip_abc","task_count":5}
{"event":"task_start","task_id":"A","description":"Prisma schema changes"}
{"event":"task_start","task_id":"C","description":"Rentman API client"}
{"event":"task_complete","task_id":"C","verdict":"PASS","rounds":1,"tokens":3201}
{"event":"task_start","task_id":"E","description":"Rentman sync UI","unblocked_by":"C"}
{"event":"task_complete","task_id":"A","verdict":"PASS","rounds":2,"tokens":5840}
{"event":"integration_start"}
{"event":"integration_complete","verdict":"PASS","tokens":4100}
{"event":"pipeline_complete","verdict":"PASS","tasks_passed":5,"tasks_failed":0,
 "tokens_total":32887,"commit_sha":"b7e3f19","duration_ms":184200}
```


---


# 6. Multi-agent Pipeline


> **What multi-agent means in Marshal**
>
> Marshal uses the fan-out/fan-in pattern: a planner decomposes a feature into a dependency graph, independent tasks run concurrently in separate goroutines each with their own executor/critic loop, dependent tasks wait for their dependencies to pass, and an integration critic reviews the combined result. This is not a swarm — it is structured parallel specialisation with explicit dependency management.


## 6.1 Multi-agent patterns


| Pattern | Description | Marshal's use |
| --- | --- | --- |
| Fan-out / fan-in | Coordinator decomposes into N independent sub-tasks, runs in parallel, collects results. | Used in Phase 3. Independent tasks run in separate goroutines. |
| Hierarchical pipeline | Multiple tiers at different abstraction levels: planner → executor → critic. | Used throughout. Phase 3 adds: planner → parallel executors → critics → integration critic. |
| Swarm | Many identical agents running autonomously, self-directing. | Explicitly excluded. Coordination overhead outweighs benefits. Relay uses structured pipelines with defined stopping conditions. |


## 6.2 Planner output schema


```json
{
  "feature": "add a staff portal with timesheets and Rentman integration",
  "tasks": [
    {
      "id": "A",
      "description": "Add Prisma schema tables for timesheets",
      "files_likely_affected": ["prisma/schema.prisma"],
      "depends_on": [],
      "skill": "schema-migration"
    },
    {
      "id": "B",
      "description": "Add tRPC router for timesheet CRUD",
      "files_likely_affected": ["packages/server/src/router/timesheets.ts"],
      "depends_on": ["A"]
    },
    {
      "id": "C",
      "description": "Build Rentman API client wrapper",
      "files_likely_affected": ["packages/server/src/lib/rentman.ts"],
      "depends_on": []
    }
  ]
}
```


## 6.3 Scheduling


```
Dependency graph:
  A (Prisma schema) ──► B (tRPC router) ──► D (timesheet UI)
  C (Rentman client) ──────────────────► E (Rentman sync UI)

Execution tiers (same tier runs in parallel):
  Tier 1: [A, C]   — no dependencies, both start immediately
  Tier 2: [B, E]   — B waits for A; E waits for C
  Tier 3: [D]      — waits for B

File conflict serialisation:
  Tasks with overlapping files_likely_affected are serialised
  regardless of declared independence.
```


## 6.4 Parallel execution


| Concern | Approach |
| --- | --- |
| Goroutine coordination | `errgroup` per tier. Scheduler blocks until all goroutines in a tier complete. |
| Branch naming | `marshal/task-<pipeline-id>-<task-id>`. Unique per task. |
| Merge ordering | Task branches merged in topological sort order on pipeline completion. |
| Failure handling | `fail_fast=true`: cancel all running goroutines via context. `fail_fast=false`: continue independent tasks. |
| Endpoint concurrency | `max_parallel` caps concurrent loops. With `max_workers=1` on RunPod, set `max_parallel=1`. |
| File conflict detection | `files_likely_affected` overlap → serialise conflicting tasks. |


> ⚠️ **RunPod scaling**
>
> Parallel execution requires `max_workers > 1` on RunPod endpoints. With `max_workers=1`, set `max_parallel=1` — sequential execution is fully correct and produces identical results.


## 6.5 Integration critic output


```json
{
  "verdict": "PASS" | "FAIL",
  "summary": "one sentence on the combined change set",
  "cross_task_issues": [
    {
      "tasks": ["B", "D"],
      "issue": "tRPC router returns camelCase but frontend expects snake_case",
      "fix": "align field naming in packages/server/src/router/timesheets.ts"
    }
  ]
}
```


## 6.6 Pipeline lifecycle


```
marshal pipeline "add staff portal with timesheets and Rentman integration"

1.  Planner produces task graph JSON
2.  User shown plan, confirms (--auto-approve skips)
3.  Pipeline ID assigned
4.  Scheduler builds execution tiers from dependency graph

For each tier:
5.  Ready tasks launched as goroutines (up to max_parallel)
6.  Each task: isolation branch → executor/critic loop → PASS/FAIL
7.  PASS: branch held open (not yet merged)
8.  FAIL: branch deleted, task marked failed

9.  All tiers complete
10. Integration critic reviews combined diff
11a. Integration PASS → merge branches in topological order → commit
11b. Integration FAIL → hold branches open, report cross_task_issues
```


## 6.7 Git safety invariant


> ⚠️ **Pipeline git safety**
>
> The original branch is only modified on a confirmed integration PASS. All task branches are held pending until integration approval. If the pipeline aborts, all task branches are deleted and the original branch is restored to the pre-pipeline HEAD.


---


# 7. Goals


## 7.1 Phase 1 + 2


| Goal | Definition of done |
| --- | --- |
| Zero-intervention loop | Submit a task, walk away. Runs to PASS or exhaustion without further input. |
| Pluggable backends | Swapping endpoints requires only a `marshal.toml` change. |
| Full session observability | Every session browsable with per-round detail, tokens, cache hits, diffs. |
| CI-safe headless mode | `marshal run --no-tui` exits with documented exit codes and JSON on stdout. |
| Git-safe by default | Original branch untouched until PASS confirmed. Exhaustion hard-reverts. |
| Security by default | Every executor and critic call includes standing security instructions. |
| Open skills system | Skills loadable from three tiers. Community-shareable as plain TOML files. |


## 7.2 Phase 3


| Goal | Definition of done |
| --- | --- |
| Valid dependency graphs | Planner output validates: cycle detection, missing refs, `files_likely_affected` populated. |
| Sequential pipeline | Tasks run in topological order. Dependent tasks wait for their dependencies. |
| Parallel pipeline | Independent tasks run concurrently up to `max_parallel`. File conflict detection serialises overlapping tasks. |
| Integration critic | Receives combined diff. `cross_task_issues` identifies implicated tasks. Targeted reruns possible. |
| Pipeline git-safe | Task branches pending until integration PASS. Abort cleans all task branches. |
| Pipeline TUI | DAG progress view with real-time task status. Concurrent streaming log panels. |


## 7.3 Stretch goals


| Goal | Description |
| --- | --- |
| Executor tool use | Autonomous file editing via tool calls. Branch isolation is the safety net. |
| Planner editing | User reviews and edits the task graph before execution. |
| Targeted pipeline rerun | After integration FAIL, rerun only implicated tasks. |
| VS Code / Neovim plugin | Single-task and pipeline modes via headless JSON interface. |
| Cost tracking | Per-pipeline token spend breakdown. Cache savings per task. |
| Community skills repo | Public GitHub repository of skill TOML files. |


---


# 8. Development Milestones


## Phase 1 — Working loop


| # | Milestone | Deliverable |
| --- | --- | --- |
| 1 | Scaffold + provider layer | `Backend` interface, `OpenAICompatibleBackend`, `marshal.toml` loading. Both RunPod endpoints verified. |
| 2 | Agent layer + loop engine + security prompts | Executor and Critic with security standing instructions. Loop orchestrator: rounds, feedback injection, JSON verdict parsing, think-block stripping, compaction hook, token budget. Built-in skills via `--skill`. |
| 3 | Git layer | `GitContext`: branch, diff, commit, revert. Branch isolation end-to-end. |
| 4 | Session store + CLI | SQLite `.marshal/sessions.db`. Cobra CLI. Headless `--no-tui --json`. Exit codes. |


## Phase 2 — TUI + compaction


| # | Milestone | Deliverable |
| --- | --- | --- |
| 5 | Bubble Tea live loop view | TUI: round indicator, agent badge, streaming viewport, verdict badge, token/cache statusbar. |
| 6 | Session browser + diff viewer | Browsable session table. Per-round expansion. Full-screen diff viewer. |
| 7 | Real context compaction | `SummaryCompactor`. `compact_after` wired to real behaviour. |
| 8 | Think-block panel | Collapsible R1 reasoning panel. Toggle with T. |


## Phase 3 — Multi-agent pipeline


| # | Milestone | Deliverable |
| --- | --- | --- |
| 9 | Planner agent | Planner struct. Task graph JSON schema. Cycle detection. `marshal pipeline` command. |
| 10 | Sequential pipeline | Topological sort. Tasks run one at a time in dependency order. Pipeline records in SQLite. |
| 11 | Integration critic | Combined diff extraction. `cross_task_issues` output. Pipeline commit on PASS. |
| 12 | DAG progress view | Bubble Tea `dag_view.go`. Task node status colours. Concurrent log panels. Headless JSON. |
| 13 | Parallel execution | Goroutine fan-out per tier. `max_parallel` config. File conflict serialisation. `errgroup`. |
| 14 | Executor tool use (stretch) | `CompleteWithTools()` on Backend. Sandbox policy layer. Autonomous file editing. |


---


# 9. Resolved Design Decisions


| Decision | Resolution | Rationale |
| --- | --- | --- |
| CLI framework | Cobra | Idiomatic for multi-command Go CLIs. |
| Session DB | Per-project `.marshal/sessions.db` | Git-ignorable, project-scoped. |
| Streaming | Phase 1 | Core to TUI live view. |
| Think-block display | Strip Phase 1, collapsible panel Phase 2 | Phase 1 clean output. Phase 2 adds reasoning visibility. |
| Branch isolation | Enabled by default | AI tools warrant structural git safety. |
| Compaction | Hook Phase 1 (pass-through), real Phase 2 | Avoids over-engineering Phase 1. |
| Critic output format | Structured JSON with string-prefix fallback | More robust than prefix parsing. |
| Executor tool use | Phase 3 stretch | Phases 1-2 build the safety infrastructure Phase 3 requires. |
| Multi-agent pattern | Fan-out/fan-in with explicit dependency graph | Structured pipelines with defined stopping conditions. Swarm excluded. |
| Planner output format | JSON with `id`, `description`, `depends_on`, `files_likely_affected`, optional `skill` | Dependency info from day one. Sequential and parallel driven by same schema. |
| Parallel vs sequential | Sequential is the correct default; parallel is a performance optimisation | Sequential always correct. Parallel requires endpoint scaling and explicit `max_parallel`. |
| Integration critic | Separate agent invocation after all tasks complete | Per-task critics: individual correctness. Integration critic: cross-task coherence. |
| Conflict detection | `files_likely_affected` heuristic — serialise overlapping files | Conservative. Prevents concurrent writes to the same file. |
| Pipeline failure policy | `fail_fast` configurable, default false | Default: continue independent tasks. `fail_fast` for CI. |
| Security standing instructions | Always-on base layer in every prompt. Cannot be overridden. | Security by default without user effort. |
| Skills system — open by design | Plain TOML, no registration. Three-tier resolution: project > user-global > built-in. | Lowest possible barrier to sharing. Mirrors git config precedence. |
| Skills cannot override security | `system_prompt_additions` only. Other prompt keys rejected at load time. | Non-negotiable architectural constraint. |
| Skills travel with the project | `.marshal/skills/` is version-controlled. | Team gets same skill behaviour after `git clone`. No setup step. |
| Community skills model | Public GitHub repo of TOML files, install via `curl`. | `curl` + filename is the simplest distribution for a text file. |


---


# 10. Out of Scope


- Web UI — Marshal is terminal-native
- Multi-user or team features — personal developer tool
- Model training or fine-tuning — Marshal consumes models
- Cloud hosting — Marshal runs locally
- Swarm multi-agent pattern — coordination overhead outweighs benefits at this scale
- Dynamic agent spawning — agents do not spawn other agents autonomously; the planner runs once and produces a fixed graph
- Non-OpenAI-compatible backends in Phase 1/2 — Backend interface accommodates them; implementation is Phase 3 stretch
- Windows native support — macOS/Linux primary, WSL recommended for Windows
