# Marshal — Architecture & Design

**Version** 1.3 — Updated through M12 (Agent Tool Use)  
**Language** Go

---

# 1. Project Overview

## 1.1 What is Marshal?

Marshal is an **agent-centric** coding assistant orchestrator. You converse naturally with the **Marshal** agent, which plans your requests and delegates to specialized **executor** agents (write code) and **critic** agents (review code). Changes are applied to a real git repository with full branch isolation, and presented through a terminal-native TUI.

The distinction from tools like OpenCode or Aider is that Marshal is **conversation-first**: you interact with a single agent (Marshal) that handles the complexity of spawning other agents, managing the feedback loop, and maintaining context across a session.

Phase 3 extends Marshal into a **multi-agent pipeline** (complete): the planner decomposes large features into dependency-ordered tasks that run in parallel across isolated branches, with an integration critic reviewing the combined result. Executor agents can now autonomously explore the codebase and validate changes via the tool use system.

## 1.2 The problem it solves

Existing coding agents either:
- Require you to manage the executor/critic loop manually
- Present as a swarm of agents with no clear conversational interface

Marshal solves this by giving you **one conversational partner** (Marshal) that:
- Understands your intent through natural dialogue
- Plans work and asks clarifying questions
- Spawns and manages executor/critic agents automatically
- Summarizes results and maintains context

At the feature level, large tasks spanning multiple files require decomposition and coordination. The multi-agent pipeline (Phase 3) gives Marshal a **planner** that produces dependency graphs and a **scheduler** that runs independent work in parallel.

> **Core insight:** The user should converse with one agent, not a swarm. Marshal orchestrates the specialized agents behind the scenes.

## 1.3 Technology stack

| Component | Technology | Rationale |
| --- | --- | --- |
| Language | Go | Single binary, strong stdlib, excellent TUI ecosystem. Goroutines make parallel agent execution natural. |
| TUI framework | Bubble Tea + Lip Gloss | Elm-style architecture. The conversation view and DAG progress view are natural Bubble Tea components. |
| CLI framework | Cobra | Idiomatic multi-command Go CLI. |
| Session storage | SQLite via database/sql | Per-project `.marshal/sessions.db`. Extended in Phase 3 for pipeline runs and task dependency graphs. |
| Config format | TOML via BurntSushi/toml | Human-readable, git-committable, env var interpolation. |
| Model client | openai-go SDK | Works against Fireworks AI, Ollama, Together AI, and any OpenAI-compatible endpoint. |
| Git operations | os/exec wrapping git CLI | Extended in Phase 3: concurrent isolation branches, topological merge ordering. |

---

# 2. Architecture

## 2.1 Agent-centric model

Marshal is built around **agents** — LLM-powered entities with specific roles:

| Agent | Role | User-facing | Spawns others |
| --- | --- | --- | --- |
| **Marshal** | Orchestrator — conversation, planning, delegation | Yes | Executor, Critic, Planner |
| **Executor** | Code generation — writes and modifies code | No | — |
| **Critic** | Code review — validates changes, produces verdicts | No | — |
| **Compactor** | Context management — summarizes conversation history | No | — |
| **Planner** | Task decomposition — creates dependency graphs (Phase 3) | No | — |
| **Integration Critic** | Cross-task review — validates combined changes (Phase 3) | No | — |

### Conversation flow

```
User ↔ Marshal (natural conversation)
         ↓
    [When code changes needed]
         ↓
    Marshal spawns Executor ──► Critic
         ↑                        ↓
         └──────── Feedback ──────┘ (if FAIL, retry up to max_rounds)
         ↓
    [If PASS, commit; if exhausted, revert]
         ↓
    Marshal summarizes to User
         ↓
    Conversation continues
```

## 2.2 Layer overview

| Layer | Technology | Responsibility |
| --- | --- | --- |
| TUI | Bubble Tea + Lip Gloss | Chat interface, live loop view, session browser, diff viewer, config screen, think-block panel, DAG view (Phase 3) |
| CLI | Cobra | `marshal run`, `marshal pipeline`, `marshal sessions`, `marshal config`. Headless `--no-tui --json` for CI. |
| Marshal orchestrator | Pure Go | User's conversational partner: interprets intent, plans work, spawns agents, summarizes results |
| Agent layer | Pure Go | Executor, Critic, Compactor, Planner structs. System prompts, temperature, structured output. |
| Session store | database/sql + SQLite | Sessions, rounds, tokens, verdicts, diffs, conversation history. |
| Provider layer | openai-go SDK | `Backend`, `StreamingBackend`, `ToolCapableBackend` interfaces + `OpenAICompatibleBackend` + `OllamaBackend`. Concurrent-safe. |
| Git layer | os/exec | `Git`: branch, diff, commit, revert, merge. |

## 2.3 Project structure (current)

```
marshal/
├── cmd/marshal/main.go
├── internal/
│   ├── marshal/              # Marshal orchestrator agent
│   │   └── marshal.go        # Conversation handling, planning, agent spawning
│   ├── agents/               # Specialized agent implementations
│   │   ├── executor/executor.go    # Code writing agent
│   │   ├── critic/critic.go        # Code review agent
│   │   ├── compactor/compactor.go  # Context summarization agent
│   │   └── planner/planner.go      # Task decomposition agent (Phase 3)
│   ├── loop/                 # Backward-compat wrapper around Marshal
│   │   ├── loop.go           # Thin wrapper for single-task execution
│   │   └── skills.go         # Three-tier skill loading
│   ├── prompts/prompts.go    # Shared system prompts (security, base instructions)
│   ├── types/types.go        # Shared types (Skill, etc.)
│   ├── tools/                # Agent tool use (M12)
│   │   ├── tools.go          # Provider-agnostic types (Definition, Call, Result, Response)
│   │   ├── registry.go       # DefaultTools(), Execute() dispatcher
│   │   ├── executor_tools.go # read_file, list_directory, search_code
│   │   ├── writer_tools.go   # write_file, edit_file
│   │   └── runner_tools.go   # run_command with allowlist
│   ├── backend/
│   │   ├── backend.go        # Backend, StreamingBackend, ToolCapableBackend interfaces
│   │   ├── openai_compat.go  # OpenAICompatibleBackend (+ CompleteWithTools)
│   │   ├── ollama.go         # OllamaBackend — native /api/chat (+ CompleteWithTools)
│   │   ├── ollama_models.go  # ListModels, PullModel
│   │   ├── factory.go        # Backend factory (provider → implementation)
│   │   └── reasoning.go      # DeepSeekR1Parser, ThinkBlockAccumulator
│   ├── config/config.go      # marshal.toml loader
│   ├── git/git.go            # Git layer
│   ├── store/store.go        # SQLite session store
│   └── tui/
│       ├── app.go            # Root Bubble Tea model
│       ├── chat_view.go      # Conversation interface with Marshal
│       ├── main_panel.go     # Continuous-scroll log panel
│       ├── marshal_adapter.go  # Bridges marshal execution to TUI events
│       └── styles.go         # Forge palette + Lip Gloss styles
├── .marshal/skills/          # Project-local skills
├── marshal.toml
├── go.mod
└── go.sum
```

## 2.4 The Backend interface

```go
// Message carries one conversation turn. ToolCallID and ToolCalls are optional
// (zero values omitted); they are populated during tool-use exchanges.
type Message struct {
    Role       string     // "system" | "user" | "assistant" | "tool"
    Content    string
    ToolCallID string     // role="tool": links result back to a Call.ID
    ToolCalls  []tools.Call // role="assistant": model-initiated tool calls
}

type Response struct {
    Content          string
    PromptTokens     int
    CompletionTokens int
    CacheHit         bool
    CachedTokens     int
}

// Backend is the core interface implemented by every provider.
type Backend interface {
    Complete(ctx context.Context, model string, messages []Message) (Response, error)
    Name() string
}

// StreamingBackend extends Backend with optional streaming support.
type StreamingBackend interface {
    Backend
    CompleteStreaming(ctx context.Context, model string, messages []Message, onChunk func(StreamResponse)) error
}

// ToolCapableBackend extends Backend with tool-call support (M12).
// Use AsToolCapable(be) to probe for this capability at runtime.
type ToolCapableBackend interface {
    Backend
    CompleteWithTools(ctx context.Context, model string, messages []Message, tools []tools.Definition) (tools.Response, error)
}
```

`AsStreaming` and `AsToolCapable` are helper functions that return nil when the backend doesn't implement the optional interface — callers use them for graceful degradation.

## 2.5 Configuration — marshal.toml

```toml
[marshal]
# Marshal orchestrator — your conversational partner
# Handles planning, conversation, and agent delegation
model       = "accounts/fireworks/models/deepseek-v3p1"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.3       # balanced for planning and chat
max_tokens  = 4096

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

# Tool use (M12) — opt-in per agent role
# enable_tools = true        # default false
# max_tool_calls = 20        # max tool invocations per round

[session]
db_path = ".marshal/sessions.db"

[retry]
max_attempts       = 3
initial_backoff_ms = 1000
backoff_factor     = 2.0
retry_status_codes = [429, 502, 503]
```

---

# 3. Marshal Agent Lifecycle

## 3.1 Conversation state machine

| State | Detail |
| --- | --- |
| **Chatting** | User and Marshal converse naturally. Marshal interprets intent, asks clarifying questions, or acknowledges simple requests. |
| **Planning** | Marshal decides code changes are needed. It may spawn a Planner (Phase 3) or formulate a single task. |
| **Executing** | Marshal spawns an Executor agent with a specific task. The executor writes code on an isolation branch. |
| **Reviewing** | Marshal spawns a Critic agent to review the diff. The critic returns PASS or FAIL with feedback. |
| **Retrying** | If FAIL and rounds remain, Marshal injects feedback and respawns the Executor. |
| **Completing** | If PASS: branch merged, changes committed. If exhausted: branch deleted, hard revert. Marshal summarizes to user. |

## 3.2 Single-task lifecycle (classic loop)

| Step | Detail |
| --- | --- |
| 1. Task submitted | Session created in `.marshal/sessions.db`. HEAD SHA recorded. Isolation branch created. |
| 2. Marshal delegates | Marshal spawns Executor with task description. |
| 3. Token budget check | Estimated token count checked. Warning at `token_budget_warn`. Blocked at 95%. |
| 4. Compaction check | If `round > compact_after`, `Compactor.Compact()` called. |
| 5. Executor writes | Messages built: system prompt + task + history + critic feedback. `Backend.Complete()` called. |
| 6. Diff extracted | `Git.GetDiff()` called. Unified diff appended to critic context. |
| 7. Critic reviews | Receives task + executor prose + git diff. Think-blocks stripped. JSON verdict parsed. |
| 8a. PASS | Marshal merges branch, commits changes, summarizes success to user. |
| 8b. FAIL | Marshal stores feedback. If rounds remain, return to step 2. If exhausted, proceed to step 9. |
| 9. Exhaustion | Marshal hard resets to recorded HEAD SHA, deletes branch, summarizes failure to user. |

## 3.3 Structured critic output

```json
{
  "verdict":  "PASS" | "FAIL",
  "summary":  "one sentence explaining the verdict",
  "issue":    "specific problem identified (FAIL only)",
  "fix":      "exactly what needs to change (FAIL only)",
  "concerns": ["optional non-blocking observations"]
}
```

## 3.4 Branch isolation

| Event | Git operation |
| --- | --- |
| Session start | `git checkout -b marshal/task-<id>` from current HEAD |
| PASS | checkout original → merge `marshal/task-<id>` → delete branch |
| Exhaustion | checkout original → delete `marshal/task-<id>` without merge |
| Unhandled error | Attempt checkout of original. Log branch for manual cleanup. |

## 3.5 Context compaction

```go
type Compactor interface {
    Compact(ctx context.Context, rounds []Round) ([]Message, error)
}
// Phase 1: PassThroughCompactor (no-op hook)  — implemented
// Phase 2: SummaryCompactor (real summarisation) — implemented (M7)
```

---

# 4. AI Features

## 4.1 Agent model selection

| Agent | Model | Rationale |
| --- | --- | --- |
| **Marshal** (orchestrator) | DeepSeek V3.2 | High code comprehension for planning, no reasoning overhead for conversational tasks. Fireworks serverless. |
| Executor | Devstral Small 2 (2512) | 68.0% SWE-Bench Verified. Dense 24B, Apache 2.0. Fits on A100 × 1 at $2.90/hr. |
| Critic | DeepSeek-R1-0528 | Full 671B MoE reasoning model. 57.6% SWE-Bench, 73.3% LiveCodeBench. System prompt supported. Better JSON reliability than original R1. H200 × 8 at $48/hr with scale-to-zero. |
| Planner | DeepSeek V3.2 | High code comprehension for dependency graph generation. Non-reasoning — no think-block overhead on structured output. Fireworks serverless. |
| Integration critic | DeepSeek-R1-0528 | Same deployment as critic. Cross-task coherence review benefits from full reasoning depth. |
| Compactor | DeepSeek V3.2 | Summarisation task. Faithful compression, no reasoning overhead needed. |

> **Why not use R1-Distill-14B for the critic?** The distilled models are compressed proxies for local hosting within VRAM constraints. On hosted inference those constraints don't apply. The full R1-0528 produces significantly deeper reasoning chains, catches more issues per round, and has better structured JSON output. Fewer FAIL-then-PASS cycles saves tokens overall despite the higher per-GPU-hour cost. The H200 × 8 cost ($48/hr active) is only paid while Marshal is actively running — with scale-to-zero at 5 minutes, a typical 10-minute coding session costs about $8 in critic GPU time.

## 4.2 Temperature per agent

| Agent | Setting | Rationale |
| --- | --- | --- |
| Marshal | 0.3 | Balanced for planning decisions and conversational variety. |
| Executor | 0.2 | Low for correct structured code. Some variance for creative solutions on retry. |
| Critic | 0.6 | R1 series requires 0.5–0.7. Setting 0.0 causes repetition and incoherent outputs in the full R1 model. |
| Planner | 0.0 | Decomposition must be consistent and reproducible. |
| Integration critic | 0.6 | Same rationale as critic. |
| Compactor | 0.0 – 0.1 | Summarisation should be faithful, not creative. |

## 4.3 Token budget

| Threshold | Action |
| --- | --- |
| < `token_budget_warn` (default 80%) | No action. |
| >= `token_budget_warn` | Warning in TUI statusbar. |
| >= 95% | Request blocked. User prompted to compact or reduce scope. |

Token efficiency measures (in order of impact):

| Measure | Where | Saving |
| --- | --- | --- |
| Session affinity header | `openai_compat.go` | 50% on cached prefix tokens (Fireworks pricing) |
| Diff last in message array | Message builder | Maximises cached prefix length |
| No dynamic content in system prompt | Prompt builder | Preserves cache across all rounds |
| `git diff -U1` context lines | `git.go` | 30–50% smaller critic input |
| Batch inference for planner + integration critic | Backend | 50% on both input and output tokens |
| Think-block stripping | Loop engine | Stored output excludes reasoning tokens |

## 4.4 Security-first system prompts

Every executor and critic call includes a standing instruction layer — non-negotiable security rules that apply to every task, every session, regardless of what the user asks for. Skills can only add to this layer; they cannot override it.

### Executor standing instructions

```
Input and data handling:
- Never trust user input. Validate and sanitise at the boundary.
- Never construct database queries with string interpolation. Always use parameterised queries.
- Never deserialise untrusted data into executable objects.

Secrets and credentials:
- Never hardcode secrets, API keys, tokens, or passwords.
- Never log sensitive values: passwords, tokens, PII, session identifiers.

Authentication and authorisation:
- Never create a route or endpoint without the established auth middleware.
- Default to least privilege.

Dependencies:
- Do not introduce new third-party dependencies without flagging them explicitly.

Cryptography:
- Never implement custom cryptographic operations.
- Never use MD5 or SHA-1 for security-sensitive hashing.
```

### Critic standing security checks

```
In addition to correctness review, check the diff for:
- Unvalidated user input entering the system without boundary validation
- Raw query construction using string interpolation
- Auth bypass: new routes missing the established middleware
- Hardcoded secrets: string literals resembling credentials or tokens
- Sensitive logging: passwords, tokens, or user data in log statements
- New dependencies: flag any new imports or package entries explicitly

If any of these are present, the verdict is FAIL regardless of whether the functional requirements are otherwise met.
```

## 4.5 Skills system

A skill is a named, reusable bundle of system prompt additions that specialises the executor and critic for a specific task domain. Skills are plain TOML files — no installation step, no manifest to update, no compilation required.

### Three-tier resolution

```
1. .marshal/skills/            Project-local. Committed to the repo. Highest priority.
2. ~/.config/marshal/skills/   User-global. Applies to all projects on this machine.
3. built-in/                   Shipped inside the Marshal binary. Always available.
```

### Skill file schema

```toml
[meta]
name        = "schema-migration"
version     = "1.2.0"
description = "Prisma schema changes and PostgreSQL migrations"
author      = "alecpullen"
tags        = ["database", "prisma", "postgresql"]

[executor]
system_prompt_additions = """
You are specialised in Prisma v7 schema changes and PostgreSQL migrations.
- Always generate a descriptive migration name matching the change
- Never touch prisma/generated/ — auto-generated, never modify
- Always add an index on any new foreign key column
"""

[critic]
system_prompt_additions = """
Additional checks (beyond standard security checks):
- Destructive op without a safety plan: FAIL
- Non-nullable column on existing table without default: FAIL
- Missing index on new foreign key: FAIL
"""
```

> `system_prompt_additions` only — no override. A skill file containing `system_prompt` or `system_prompt_override` is rejected with a validation error.

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

### Built-in skills

| Skill | Executor specialisation | Critic specialisation |
| --- | --- | --- |
| schema-migration | Prisma schema and migration conventions. Two-step renames. Index requirements. | Database safety: destructive ops, nullable columns, lock duration, index coverage. |
| security-audit | Find vulnerabilities: injection, auth bypass, insecure defaults, OWASP Top 10. | Verify the vulnerability is genuinely exploitable. Assess severity and remediation quality. |
| test-generation | Write tests for existing code. Edge cases, boundary values, error paths. | Test quality: behaviour vs implementation, mock realism, edge case coverage. |
| documentation | Rewrite comments, JSDoc, README for an unfamiliar reader. | Evaluate whether someone unfamiliar with the codebase would understand the result. |
| dependency-audit | Audit a dependency: licence, security history, maintenance, alternatives. | Verify the audit is complete and the recommendation is sound. |

---

# 5. TUI Design

## 5.1 Visual Design Language — Forge palette

| Token | Hex | Usage |
| --- | --- | --- |
| `--bg` | `#0D1117` | Primary background — main panel, sidebar |
| `--bg2` | `#161B22` | Secondary — task block headers, statusbar, prompt bar background |
| `--bg3` | `#21262D` | Tertiary — progress bar track, inactive round dots, think-block background |
| `--br` | `#30363D` | Primary border — panel divider, task block borders |
| `--br2` | `#21262D` | Secondary border — round separators, minor dividers |
| `--br3` | `#484F58` | Emphasis border — focused prompt input outline |
| `--tx` | `#E6EDF3` | Primary text — task headers, active sidebar items, verdicts |
| `--tx2` | `#8B949E` | Secondary text — log output, sidebar inactive items |
| `--tx3` | `#484F58` | Tertiary — prefixes, timestamps, hints, metadata |
| `--bl` | `#388BFD` | Blue — executor agent, prompt prefix, focused state, **Marshal agent** |
| `--gr` | `#3FB950` | Green — pass verdict, commit confirmation |
| `--rd` | `#F85149` | Red — fail verdict, revert confirmation |
| `--am` | `#D29922` | Amber — running state, warnings, queued indicator |
| `--pu` | `#A78BFA` | Purple — critic agent, think-blocks |

All text uses the system monospace font.

## 5.2 Overall layout

```
┌────────────────┬─────────────────────────────────────────────┐
│ ● add venue c… │                                             │
│   pass  r1     │  Marshal: I'll add that venue column for    │
│                │  you. Let me start by creating the migration│
│ ● fix auth b…  │                                             │
│   fail  r3     │  ╔ add venue column to events table ════════╗ │
│                │  ║                                         ║ │
│ ─────────────  │  ║ exec  writing migration file...         ║ │
│                │  ║       ALTER TABLE events ADD COLUMN ... ║ │
│ ◎ add staff p… │  ║       ─────────────────────────────     ║ │
│   running  r1  │  ║ critic reviewing diff...                ║ │
│                │  ║       PASS — migration correct,         ║ │
│ ○ add input v… │  ║       index on foreign key present      ║ │
│   queued       │  ╚═══════════════════════════ pass ═══════╝ │
│                │                                             │
│                │  Marshal: Done! I've added the venue column │
│                │  with the proper index. What's next?      │
│                │                                             │
│                │  › add input validation to the event fo_   │
└────────────────┴─────────────────────────────────────────────┘
│ round 1 · exec · marshal/task-def456 · 3 files changed      │
└─────────────────────────────────────────────────────────────-┘
```

| Panel | Width | Scrolls | Purpose |
| --- | --- | --- | --- |
| Sidebar | 28 chars + 1 border | Vertically if many tasks | Task timeline: history, running, queued |
| Main panel | Remaining width | Vertically (continuous) | Chat with Marshal + task output, streaming log, prompt bar |
| Statusbar | Full width | Never | Persistent orientation — round, agent, branch, files |

## 5.3 Sidebar — Task Timeline

Each task occupies two lines:

```
● add venue column to events table…    ← dot + truncated description (26 chars max)
  pass  r1                             ← status word + round count
```

| State | Dot | Dot colour | Status word | Line 2 colour |
| --- | --- | --- | --- | --- |
| Queued | ○ | `--tx3` | queued | `--tx3` |
| Running | ◎ | `--am` (pulse) | running  rN | `--am` |
| Pass | ● | `--gr` | pass  rN | `--tx3` |
| Fail | ● | `--rd` | fail  rN | `--rd` |

A thin horizontal rule separates completed tasks from running + queued tasks.

Sidebar keyboard:

| Key | Action |
| --- | --- |
| `↑` / `↓` | Move sidebar focus |
| `↵` | Scroll main panel to that task's block |
| `d` | Open diff viewer for focused completed task |
| `esc` | Return focus to main panel |

## 5.4 Main Panel — Continuous Scroll

The main panel combines **chat with Marshal** and **task execution logs**:

```
Marshal: I'll help you add that feature. Let me start by
understanding the current codebase structure.

  ╔ Task: Add user authentication middleware ═════════════╗
  ║                                                       ║
  ║  exec  scanning for existing auth patterns...         ║
  ║  exec  creating middleware/auth.ts...                 ║
  ║       export function requireAuth(...) {               ║
  ║  ──────────────────────────────────────────────────    ║ ← round separator
  ║  critic reviewing changes...                           ║
  ║       PASS — auth middleware correctly validates JWT  ║
  ║                                                       ║
  ╚══════════════════════════════ pass ═══════════════════╝

Marshal: Authentication middleware is ready. I've added
JWT validation with proper error handling. Should I now
apply this to the protected routes?
```

Log line rendering:

| Prefix (6 chars) | Colour | Content |
| --- | --- | --- |
| `marshal` | `--bl` bold | Marshal orchestration events |
| `exec  ` | `--bl` | Executor response for this chunk |
| `critic` | `--pu` | Critic response |
| (blank) | `--tx3` | Continuation lines, code, diff context |
| (blank) | `--gr` | Lines starting with ✓ — success confirmations |
| (blank) | `--rd` | Lines starting with ✗ — errors |
| (blank) | `--am` | Warnings, token budget warnings |

Round separators: dim horizontal rule (`──────────` in `--br2`) with centred `round N` label in `--tx3`.

Think-blocks: rendered inline in italic `--pu` on `--bg3`, prefixed with dim `think` label.

## 5.5 Prompt Bar & Composer

The prompt bar is permanently anchored at the bottom of the main panel:

```
  › add input validation to the event form fields_
    ↵ run · tab expand · ↑↓ history · esc clear
```

`tab` expands to the task composer overlay:

```
┌─────────────────────────────────┐
│ task                            │
│ add input validation to the     │
│ event form fields_              │
├─────────────────────────────────┤
│ 📎 src/components/EventForm.tsx │
├─────────────────────────────────┤
│ rounds 3  branch ✓  commit ✓    │
│               [run task  ↵]     │
└─────────────────────────────────┘
```

Composer keyboard:

| Key | Action |
| --- | --- |
| `tab` from prompt bar | Open composer with current text pre-filled |
| `esc` | Close composer, return text to prompt bar |
| `ctrl+↵` | Submit task from composer |
| `ctrl+f` | Open fuzzy file picker to pin context |
| `space` on option | Toggle option |

## 5.6 Overlays

**Diff viewer** (`d` from sidebar on completed task): Full-screen overlay showing per-file unified diff. Addition lines `--gr`, deletion lines `--rd`, context lines `--tx3`. `q`/`esc` closes.

**Config view** (`c`): Full-screen read-only overlay showing resolved configuration. Section headers in `--bg2`, model names in `--bl`/`--pu`, resolved env vars as `$VAR ✓`.

**Help overlay** (`?`): Two-column keyboard shortcut reference. Global shortcut, works from any panel.

**Sessions browser** (`:sessions` command): Full-screen overlay listing persisted sessions from SQLite. Columns: ID, task, status badge, relative time. `↵` resumes, `d` diffs, `x` deletes.

**Fuzzy file picker** (`ctrl+f` from composer): Live fuzzy search over repo files with preview. `↵` pins selected file to composer.

## 5.7 Statusbar

Always visible. Maximum 4 segments separated by `·`.

| Screen state | Segments |
| --- | --- |
| Idle | "ready" · branch · session count |
| Task running | round N · agent (coloured) · branch · files changed |
| Task complete (pass) | "pass · committed SHA7" · branch |
| Task complete (fail) | "fail · reverted" · branch |
| Composer open | files pinned · token estimate · branch |
| Scrolled up during run | round N · agent · "↑ scrolled" · branch |

## 5.8 Full Keyboard Map

| Key | Context | Action |
| --- | --- | --- |
| `↵` | Prompt bar | Submit task (or queue if one is running) |
| `tab` | Prompt bar | Open task composer |
| `↑` / `↓` | Prompt bar | Cycle input history |
| `esc` | Prompt bar | Clear input |
| `ctrl+↵` | Composer | Submit task |
| `esc` | Composer | Close, return text to prompt bar |
| `ctrl+f` | Composer | Open fuzzy file picker |
| `c` | Any (not running) | Open config overlay |
| `d` | Sidebar focused | Open diff viewer for focused task |
| `q` / `esc` | Diff / config overlay | Close overlay |
| `↑` / `↓` | Main panel | Scroll (pauses auto-scroll) |
| `T` | Any | Toggle think-block visibility |
| `?` | Any | Help overlay |
| `ctrl+c` | Any | Quit (prompts if task is running) |

## 5.9 Phase 3 TUI additions

The task composer gains a pipeline mode toggle. When enabled, the description is sent to the planner rather than the executor.

DAG progress view:

| Element | Content |
| --- | --- |
| DAG diagram | Task nodes by dependency tier. Colours: pending (dim), running (amber pulse), passed (green), failed (red). |
| Active task panels | One live log panel per running task, stacked vertically. |
| Pipeline summary bar | "N tasks · N running · N passed · N failed · N pending". Aggregate token totals. |

---

# 6. Multi-agent Pipeline

> Marshal uses the fan-out/fan-in pattern: a planner decomposes a feature into a dependency graph, independent tasks run concurrently in separate goroutines each with their own executor/critic loop, dependent tasks wait for their dependencies to pass, and an integration critic reviews the combined result. This is structured parallel specialisation with explicit dependency management, not a swarm.

## 6.1 Planner output schema

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

## 6.2 Scheduling

```
Dependency graph:
  A (Prisma schema) ──► B (tRPC router) ──► D (timesheet UI)
  C (Rentman client) ──────────────────► E (Rentman sync UI)

Execution tiers (same tier runs in parallel):
  Tier 1: [A, C]   — no dependencies, both start immediately
  Tier 2: [B, E]   — B waits for A; E waits for C
  Tier 3: [D]      — waits for B

Tasks with overlapping files_likely_affected are serialised regardless of declared independence.
```

## 6.3 Parallel execution

| Concern | Approach |
| --- | --- |
| Goroutine coordination | `errgroup` per tier. Scheduler blocks until all goroutines in a tier complete. |
| Branch naming | `marshal/task-<pipeline-id>-<task-id>`. Unique per task. |
| Merge ordering | Task branches merged in topological sort order on pipeline completion. |
| Failure handling | `fail_fast=true`: cancel all running goroutines via context. `fail_fast=false`: continue independent tasks. |
| Endpoint concurrency | `max_parallel` caps concurrent loops. |
| File conflict detection | `files_likely_affected` overlap → serialise conflicting tasks. |

## 6.4 Integration critic output

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

## 6.5 Pipeline lifecycle

```
1.  User asks Marshal for a large feature
2.  Marshal spawns Planner to produce task graph JSON
3.  User shown plan, confirms (--auto-approve skips)
4.  Pipeline ID assigned
5.  Scheduler builds execution tiers from dependency graph

For each tier:
6.  Ready tasks launched as goroutines (up to max_parallel)
7.  Each task: Marshal spawns Executor/Critic loop → PASS/FAIL
8.  PASS: branch held open (not yet merged)
9.  FAIL: branch deleted, task marked failed

10. All tiers complete
11. Marshal spawns Integration Critic to review combined diff
12a. Integration PASS → merge branches in topological order → commit
12b. Integration FAIL → hold branches open, report cross_task_issues
```

> **Pipeline git safety:** The original branch is only modified on a confirmed integration PASS. All task branches are held pending until integration approval. If the pipeline aborts, all task branches are deleted and the original branch is restored to the pre-pipeline HEAD.

---

# 7. Goals & Roadmap

### Phase 1 + 2 goals (complete)

| Goal | Definition of done |
| --- | --- |
| Agent-centric design | User converses with Marshal; Marshal spawns executor/critic agents |
| Zero-intervention loop | Submit a task, walk away. Runs to PASS or exhaustion without further input. |
| Pluggable backends | Swapping endpoints requires only a `marshal.toml` change. |
| Full session observability | Every session browsable with per-round detail, tokens, cache hits, diffs. |
| CI-safe headless mode | `marshal run --no-tui` exits with documented exit codes and JSON on stdout. |
| Git-safe by default | Original branch untouched until PASS confirmed. Exhaustion hard-reverts. |
| Security by default | Every executor and critic call includes standing security instructions. |
| Open skills system | Skills loadable from three tiers. Community-shareable as plain TOML files. |

### Phase 3 goals

| Goal | Definition of done |
| --- | --- |
| ✅ Sequential pipeline | Tasks run in topological order. Dependent tasks wait for their dependencies. |
| ✅ Multi-backend abstraction | Each role (Marshal, Executor, Critic, Planner) can use different providers. Ollama native adapter with streaming. |
| ✅ Integration critic | Receives combined diff. `cross_task_issues` identifies implicated tasks. |
| ✅ Parallel pipeline | Independent tasks run concurrently up to `max_parallel`. File conflict detection serialises overlapping tasks. |
| ✅ Agent tool use | Provider-agnostic tool system: file operations, command execution, search. Sandboxed to repo root. |
| ✅ Pipeline git-safe | Task branches pending until integration PASS. Abort cleans all task branches. |
| Extended provider support | Native Anthropic Claude, Google Gemini, AWS Bedrock backends. |
| Pipeline TUI | DAG progress view with real-time task status. Concurrent streaming log panels. |

### Development milestones

#### Phase 1: Foundation (Complete)

| # | Milestone | Status | Description |
| --- | --- | --- | --- |
| 1 | Backend interface + `OpenAICompatibleBackend` + config loader | ✅ Done | OpenAI-compatible client for Fireworks, Ollama, Together AI |
| 2 | Loop engine — executor, critic, verdict, compaction hook, security prompts, skills | ✅ Done | Core executor-critic feedback loop with skills system |
| 3 | Git layer — branch isolation, diff, commit, revert | ✅ Done | Full git operations with isolation branch safety |
| 4 | Session store + Cobra CLI — SQLite, `marshal run`, headless `--no-tui --json` | ✅ Done | SQLite persistence and CLI commands |
| 5–6 | Bubble Tea TUI — live loop view, session browser, diff viewer | ✅ Done | Terminal-native UI with chat and task views |
| 7–8 | Real compaction + think-block panel | ✅ Done | Context summarization and R1 reasoning display |

#### Phase 2: Agent-Centric Model (Complete)

| # | Milestone | Status | Description |
| --- | --- | --- | --- |
| — | Marshal orchestrator | ✅ Done | User converses with Marshal; Marshal spawns executor/critic agents |
| — | Conversation system | ✅ Done | Intent classification, context accumulation, state machine |
| — | Planner agent | ✅ Done | Task decomposition into validated dependency graphs |

#### Phase 3: Pipeline & Tooling (Complete)

| # | Milestone | Status | Description |
| --- | --- | --- | --- |
| 9 | Sequential pipeline | ✅ Done | `marshal pipeline` command, topological sort execution |
| 10 | Multi-backend abstraction | ✅ Done | Per-role provider selection; Ollama native adapter with streaming |
| 11 | Integration critic + parallel execution | ✅ Done | Cross-task coherence review; DAG scheduler, goroutine fan-out, conflict detection |
| 12 | Agent tool use | ✅ Done | Provider-agnostic tools: file ops, commands, search; sandboxed to repo root |

#### Phase 4: Extended Providers (Complete)

| # | Milestone | Status | Description |
| --- | --- | --- | --- |
| 13 | Extended providers | Done | Native Anthropic Claude, Google Gemini, AWS Bedrock backends |

### M10: Multi-Backend Abstraction

The multi-backend abstraction enables per-role provider selection, allowing cost optimization and hybrid local/cloud setups.

#### Configuration

```toml
[marshal]
provider = "ollama"      # "ollama", "openai", "fireworks", "together"
model = "qwen3:4b"
base_url = "http://localhost:11434"

[executor]
provider = "fireworks"   # Cloud API for heavy lifting
model = "accounts/fireworks/models/devstral-small-2-24b-instruct-2512"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"

[critic]
provider = "ollama"      # Local reasoning model
model = "deepseek-r1:7b"
base_url = "http://localhost:11434"

[planner]
provider = "ollama"
model = "qwen3:4b"
base_url = "http://localhost:11434"
```

#### Backend Factory

```go
// Factory creates appropriate backend based on provider string
func NewBackend(provider, name, baseURL, apiKey string) (Backend, error)
```

| Provider | Backend Type | Notes |
| --- | --- | --- |
| `ollama` | `OllamaBackend` | Native `/api/chat` endpoint, no API key needed |
| `fireworks` | `OpenAICompatibleBackend` | Fireworks on-demand with session affinity |
| `openai` | `OpenAICompatibleBackend` | Standard OpenAI API |
| `together` | `OpenAICompatibleBackend` | Together AI endpoint |
| missing/unknown | `OpenAICompatibleBackend` | Backward compatible |

#### Ollama Backend

The `OllamaBackend` implements the native Ollama API (`/api/chat`) rather than the OpenAI-compatible wrapper. Features:

**Core Features:**
- No API key required for local instances
- Native streaming via `CompleteStreaming()` for real-time UX
- Context window control via `context_window` config (sets Ollama `num_ctx`)

**Model Management:**
- `ListModels()` - Query `/api/tags` to discover available local models
- `PullModel()` - Download models with progress via `/api/pull`
- `:models` TUI command - List models with size, parameters, quantization
- `:pull <model>` TUI command - Download models from Ollama Hub

**Configuration:**
```toml
[executor]
provider = "ollama"
model = "qwen2.5-coder:7b"
base_url = "http://localhost:11434"
context_window = 32768  # Large context for tool use
```

**Ollama-specific Options:**
- `num_ctx` (context_window): Controls context window - critical for tool use
- `num_predict` (max_tokens): Maximum tokens to generate
- `temperature`: Sampling temperature

#### Use Cases

1. **Cost optimization:** Run Marshal and Critic locally (cheap), offload only Executor to API
2. **Privacy-sensitive code:** Keep all processing local via Ollama
3. **Hybrid setup:** Local planning/cloud execution for large codebases
4. **Offline development:** Use `:models` and `:pull` to manage local models without cloud dependency

### M12: Agent Tool Use

Executor agents can autonomously explore the codebase, apply changes, and run tests before the critic reviews the final diff. The tool loop is bounded per round (`max_tool_calls`, default 20) and sandboxed to the repository root.

#### Tool catalog

| Tool | Capability | Safety |
| --- | --- | --- |
| `read_file` | Read file contents | Path sandboxed to repo root; absolute paths rejected |
| `write_file` | Create or overwrite a file | Critic reviews the resulting diff |
| `edit_file` | Replace a line range (1-indexed, inclusive) | Critic reviews the resulting diff |
| `run_command` | Execute build/test commands | Allowlist: `go`, `make`, `npm`, `npx`, `python`, `python3`, `pytest`, `cargo`, `rg`. No shell. |
| `search_code` | Regex search — returns `file:line:content` | Pure Go walk, no shell expansion |
| `list_directory` | List files and subdirs | Path sandboxed to repo root |

#### Execution flow

```
Executor.ExecuteWithTools()
  │
  ├─ AsToolCapable(backend) → nil?  → fall back to Execute() (no tools)
  │
  └─ LOOP (≤ max_tool_calls):
       CompleteWithTools(messages, toolDefs)
       ├─ ToolCalls = []  → return final content          (done)
       └─ ToolCalls = […] → append assistant turn
                            Execute each call → append tool-role messages
                            continue
  │
  ▼
Final content → git.GetDiff() → Critic reviews diff (not the tool conversation)
```

#### Configuration

```toml
[executor]
enable_tools   = true   # opt-in; default false
max_tool_calls = 20     # per round
```

#### Graceful degradation

| Scenario | Behaviour |
| --- | --- |
| `enable_tools = false` | Delegates to `Execute()` immediately |
| Backend lacks `ToolCapableBackend` | `AsToolCapable` returns nil → `Execute()` fallback |
| Model returns no tool calls | Loop terminates on first turn, returns content |
| Unknown tool name | `Result{IsError: true}` returned; model self-corrects or stops |
| Tool call limit reached | Round fails with a descriptive error |

#### Implementation

- `internal/tools/` — all tool types and implementations
- `internal/backend/backend.go` — `ToolCapableBackend` interface + `AsToolCapable`
- `internal/backend/openai_compat.go` — `CompleteWithTools` for Fireworks/OpenAI/Together
- `internal/backend/ollama.go` — `CompleteWithTools` for Ollama native API
- `internal/agents/executor/executor.go` — `ExecuteWithTools` agentic loop

### Stretch goals

| Goal | Description |
| --- | --- |
| Targeted pipeline rerun | After integration FAIL, rerun only implicated tasks. |
| VS Code / Neovim plugin | Single-task and pipeline modes via headless JSON interface. |
| Cost tracking | Per-pipeline token spend breakdown. Cache savings per task. |
| Community skills repo | Public GitHub repository of skill TOML files. |

---

# 8. Resolved Design Decisions

| Decision | Resolution | Rationale |
| --- | --- | --- |
| Agent-centric vs loop-centric | Agent-centric | Users want one conversational partner, not a raw loop. Marshal is the interface; executor/critic are implementation details. |
| CLI framework | Cobra | Idiomatic for multi-command Go CLIs. |
| Session DB | Per-project `.marshal/sessions.db` | Git-ignorable, project-scoped. |
| Think-block display | Inline italic purple, collapsible with `T` | Visible but subordinate. Reasoning is context, not primary output. |
| Branch isolation | Enabled by default | AI tools warrant structural git safety. |
| Compaction | Hook Phase 1 (pass-through), real Phase 2 | Avoids over-engineering Phase 1. |
| Critic output format | Structured JSON | More robust than prefix parsing. |
| Multi-agent pattern | Fan-out/fan-in with explicit dependency graph | Structured pipelines with defined stopping conditions. Swarm excluded. |
| Parallel vs sequential | Sequential is correct default; parallel is a performance optimisation | Sequential always correct. Parallel requires endpoint scaling and explicit `max_parallel`. |
| Integration critic | Separate agent invocation after all tasks complete | Per-task critics: individual correctness. Integration critic: cross-task coherence. |
| Pipeline failure policy | `fail_fast` configurable, default false | Default: continue independent tasks. `fail_fast` for CI. |
| Security standing instructions | Always-on base layer in every prompt. Cannot be overridden. | Security by default without user effort. |
| Skills system | Plain TOML, no registration. Three-tier resolution: project > user-global > built-in. | Lowest possible barrier to sharing. Mirrors git config precedence. |
| Skills cannot override security | `system_prompt_additions` only. Other prompt keys rejected at load time. | Non-negotiable architectural constraint. |
| Tool loop location | Executor (not backend) | Executor already owns execution context. Backends remain stateless request/response converters. |
| Tool capability probe | `AsToolCapable(be)` returns nil for non-capable backends | Mirrors `AsStreaming` pattern. Zero-cost graceful degradation without branching config. |
| Tool sandboxing | Absolute paths rejected; all paths joined to `repoRoot` and prefix-checked | Prevents path traversal regardless of what the model generates. |
| `run_command` allowlist | Fixed set: `go`, `make`, `npm`, `npx`, `python`, `python3`, `pytest`, `cargo`, `rg` | Shell execution (`sh`, `bash`) is never permitted. Model can request tests and builds but not arbitrary system access. |
| Tool use opt-in | `enable_tools = false` by default | Preserves all existing behaviour for users who haven't opted in. No silent behaviour change. |
| Inference provider | Fireworks AI on-demand | Executor on A100 × 1 ($2.90/hr), critic on H200 × 8 ($48/hr). Scale-to-zero means cost is only incurred during active sessions. |
| Executor model | Devstral Small 2 (2512) | 68.0% SWE-Bench Verified. Dense 24B, Apache 2.0. Fits A100 × 1. |
| Critic model | DeepSeek-R1-0528 (full) | Full reasoning model, not a distil. Better JSON reliability. Deeper reasoning per round catches more issues. |
| Critic temperature | 0.6 (not 0.0) | R1 series recommendation. Temperature 0.0 causes repetition in the full R1 model. |
| Session affinity header | `x-session-affinity: <session-id>` on every request | Required for Fireworks on-demand to route session rounds to the same replica for KV cache reuse. |
| Diff context lines | `git diff -U1` | Reduces critic input by 30–50%. Critic cares about changed lines, not surrounding context. |
| TUI layout | Single continuous scroll + permanent sidebar | Matches developer mental model of a coding session. History always visible without navigation. |
| Prompt bar | Permanently anchored at bottom of main panel | No navigation required to submit. Always ready. |

---

# 9. Out of Scope

- Web UI — Marshal is terminal-native
- Multi-user or team features — personal developer tool
- Model training or fine-tuning — Marshal consumes models
- Cloud hosting — Marshal runs locally
- Swarm multi-agent pattern — coordination overhead outweighs benefits at this scale
- Dynamic agent spawning — agents do not spawn other agents autonomously; the planner runs once and produces a fixed graph
- Native provider APIs (Claude, Gemini) — use OpenAI-compatible endpoints for now; native SDKs in M13
