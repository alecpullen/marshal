# Marshal — Design Specification

**Version** 0.8 — Confirmed model IDs and deployment configuration  
**Date** March 2026  
**Language** Go  

> **Changes in v0.8**
>
> Confirmed deployment model IDs after Fireworks serverless availability check. Neither Devstral Small 2 (2512) nor DeepSeek-R1-0528 are available on Fireworks serverless; both run as Fireworks on-demand deployments. Devstral Small 2 (2512) replaces Devstral Medium — same 68.0% SWE-Bench score, smaller and cheaper GPU footprint (A100 × 1). Updated GPU requirements, hourly cost estimates, and scale-to-zero configuration. Removed references to Devstral Medium and serverless operation for these two models.


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
| Model client | openai-go SDK | Works against Fireworks AI, Ollama, Together AI, and any OpenAI-compatible endpoint. |
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
# Devstral Small 2 (2512) — 68.0% SWE-Bench Verified, dense 24B, Apache 2.0
# Fireworks on-demand: A100 × 1 @ $2.90/hr, scale-to-zero 5m
model       = "accounts/fireworks/models/devstral-small-2-24b-instruct-2512"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.2
max_tokens  = 4096

[critic]
# DeepSeek-R1-0528 — full 671B MoE reasoning model, system prompt supported
# Fireworks on-demand: H200 × 8 @ $48/hr active, scale-to-zero 5m
model       = "accounts/fireworks/models/deepseek-r1-0528"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.6    # R1 series requires 0.5-0.7; 0.0 causes repetition
max_tokens  = 8192   # must accommodate think-blocks before verdict
json_output = true

# Phase 3
[planner]
# DeepSeek V3.2 — serverless, high code comprehension, no reasoning overhead
model       = "accounts/fireworks/models/deepseek-v3p1"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.0
max_tokens  = 2048

[integration_critic]
# Same deployment as critic — reuses the warm H200 × 8 replica
model       = "accounts/fireworks/models/deepseek-r1-0528"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.6
max_tokens  = 8192

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


## 4.1 Inference provider — Fireworks AI


Marshal uses Fireworks AI as its default inference provider. Fireworks offers serverless endpoints with no cold starts, an OpenAI-compatible API, and 50% pricing on cached input tokens. Switching providers requires only a `marshal.toml` change — the `Backend` interface is unchanged.


### Session affinity


Fireworks prompt caching only works within a single replica. For serverless deployments, send a session affinity hint on every request so all rounds of a session hit the same replica and share the KV cache:


```go
// internal/backend/openai_compat.go — add to every request
req.Header.Set("x-session-affinity", sessionID)
```


Use the Marshal session ID as the affinity key. Without this, cache hit rates on serverless are low because successive rounds may land on different replicas.


### Prompt structure rules


Fireworks caches exact prompt prefixes. The message array must be ordered so the maximum possible prefix is static:


```
Message 1 (system): base_executor_prompt
                    + security_standing_instructions   ← identical every round
                    + skill.system_prompt_additions    ← identical every session
Message 2 (user):   task description                  ← identical every round
Message 3+:         conversation history               ← grows, but prefix above is cached
Message N (user):   git diff                           ← ALWAYS LAST — changes every round
```


Never place dynamic content (timestamps, round counters, diff content) before the end of the message array. A round number in the system prompt invalidates the cache on every round.


### Batch inference


Non-streaming calls (planner, integration critic) can use Fireworks batch inference at 50% of serverless pricing for both input and output tokens. Implement as a separate code path in the Backend — streaming calls (executor, critic) stay on the real-time endpoint.


### Diff compression


The git diff is the largest variable input. Use `git diff -U1` (one context line instead of three) to reduce diff size by 30–50% on typical changes with no loss of critic comprehension. The critic cares about what changed, not surrounding context lines.


## 4.2 Model selection


| Agent | Model | Rationale |
| --- | --- | --- |
| Executor | Devstral Small 2 (2512) | 68.0% SWE-Bench Verified. Dense 24B, Apache 2.0. Fits on A100 × 1 at $2.90/hr. Fireworks on-demand deployment. |
| Critic | DeepSeek-R1-0528 | Full 671B MoE reasoning model. 57.6% SWE-Bench, 73.3% LiveCodeBench. System prompt supported. Better JSON reliability than original R1. Fireworks on-demand, H200 × 8 at $48/hr — scale-to-zero essential. |
| Planner | DeepSeek V3.2 | High code comprehension for dependency graph generation. Non-reasoning — no think-block overhead on a structured output task. Fireworks serverless. |
| Integration critic | DeepSeek-R1-0528 | Same deployment as critic. Cross-task coherence review benefits from full reasoning depth. |
| Compactor (Phase 2) | DeepSeek V3.2 | Summarisation task. Faithful compression, no reasoning overhead needed. Fireworks serverless. |


> 💡 **Why not use the R1-Distill-14B for the critic?**
>
> The distilled models are compressed proxies designed for local hosting within VRAM constraints. On hosted inference those constraints don't apply. The full R1-0528 produces significantly deeper reasoning chains before issuing a verdict, catches more issues per round, and has better structured JSON output — all directly beneficial for the critic role. Fewer FAIL-then-PASS cycles saves tokens overall despite the higher per-GPU-hour cost.
>
> The H200 × 8 cost ($48/hr active) is only paid while Marshal is actively running a session. With scale-to-zero set to 5 minutes, a typical 10-minute coding session costs about $8 in critic GPU time — less than a coffee.


## 4.3 Temperature per agent


| Agent | Setting | Rationale |
| --- | --- | --- |
| Executor | 0.2 | Low for correct structured code. Some variance for creative solutions on retry. |
| Critic | 0.6 | R1 series requires 0.5–0.7. Setting 0.0 causes repetition and incoherent outputs in the full R1 model. |
| Planner | 0.0 | Decomposition must be consistent and reproducible. |
| Integration critic | 0.6 | Same rationale as critic. |
| Compactor (Phase 2) | 0.0 – 0.1 | Summarisation should be faithful, not creative. |


## 4.4 Token budget and efficiency


| Threshold | Action |
| --- | --- |
| < `token_budget_warn` (default 80%) | No action. |
| >= `token_budget_warn` | Warning in TUI statusbar. |
| >= 95% | Request blocked. User prompted to compact or reduce scope. |


Token efficiency measures implemented in order of impact:


| Measure | Where | Saving |
| --- | --- | --- |
| Session affinity header | `openai_compat.go` | 50% on cached prefix tokens (Fireworks pricing) |
| Diff last in message array | Message builder | Maximises cached prefix length |
| No dynamic content in system prompt | Prompt builder | Preserves cache across all rounds |
| `git diff -U1` context lines | `git.go` | 30–50% smaller critic input |
| Critic `max_tokens = 8192` | `marshal.toml` | Enough headroom for think-blocks without waste |
| Batch inference for planner + integration critic | Backend | 50% on both input and output tokens |
| Think-block stripping | Loop engine | Stored output excludes reasoning tokens |


## 4.5 Executor tool use — Phase 3 north star


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
| Fireworks AI provider with session affinity | 1 | Planned |
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


> 💡 **Fireworks on-demand and parallelism**
>
> Both executor and critic are on-demand deployments. The executor (A100 × 1) and critic (H200 × 8) each handle one request at a time per replica. Set `max_parallel=1` during development — the on-demand deployments can autoscale replicas if you need concurrency later. Each parallel task uses its own session affinity key so cache isolation is maintained.


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
| 1 | Scaffold + provider layer | `Backend` interface, `OpenAICompatibleBackend` with session affinity header, `marshal.toml` loading. Both Fireworks on-demand deployments verified (Devstral Small 2 executor + R1-0528 critic). |
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
| Inference provider | Fireworks AI on-demand | Executor on A100 × 1 ($2.90/hr), critic on H200 × 8 ($48/hr). Scale-to-zero 5m means cost is only incurred during active sessions. Planner/integration critic on serverless where available. |
| Executor model | Devstral Small 2 (2512) | 68.0% SWE-Bench Verified. Dense 24B, Apache 2.0. Fits A100 × 1. Fireworks on-demand at $2.90/hr with scale-to-zero. |
| Critic model | DeepSeek-R1-0528 (full) | Full reasoning model, not a distil. System prompt support. Better JSON reliability. Deeper reasoning per round catches more issues, reducing total round count. |
| Critic temperature | 0.6 (not 0.0) | R1 series recommendation. Temperature 0.0 causes repetition in the full R1 model. |
| Planner/compactor model | DeepSeek V3.2 | High code comprehension for structured output tasks. Non-reasoning model avoids think-block overhead where it adds no value. |
| Session affinity header | `x-session-affinity: <session-id>` on every request | Required for Fireworks on-demand to route session rounds to the same replica for KV cache reuse. |
| Diff context lines | `git diff -U1` | Reduces critic input by 30–50%. Critic cares about changed lines, not surrounding context. |
| Batch inference | Planner and integration critic use batch endpoint | 50% cost on both input and output for non-streaming calls (serverless only). |


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
