# Marshal — Model Interactions & Agentic Extension

**Version** 1.0  
**Date** April 2026  
**Companion** Architecture v0.9

---

# 1. Model Roster

Marshal uses four distinct models, each with a clearly bounded responsibility. No model performs another's job. The boundaries are enforced by the orchestration layer, not by prompt engineering.

| Role | Model (prod) | Model (dev/local) | Responsibility |
|---|---|---|---|
| **Marshal** | `qwen3-8b` (Fireworks serverless) | `qwen3:4b` (Ollama) | Conversational interface, task decomposition, dependency graph |
| **Executor** | `qwen3-coder-480b-a35b-instruct` (Fireworks serverless) | `qwen2.5-coder:7b` (Ollama) | Writes code, applies changes to the repository |
| **Critic** | `deepseek-r1-0528` (Fireworks serverless) | `deepseek-r1:7b` (Ollama) | Reviews diffs, issues structured PASS/FAIL verdicts |
| **Compactor** | `deepseek-v3p2` (Fireworks serverless) | *(same as critic locally)* | Summarises round history when context grows long |

---

# 2. Single-Task Loop — Phase 1 & 2

## 2.1 Diagram

```
  ┌─────────────────────────────────────────────────────────────────┐
  │                         USER                                    │
  └──────────────────────────────┬──────────────────────────────────┘
                                 │  "add venue column to events table"
                                 ▼
  ┌─────────────────────────────────────────────────────────────────┐
  │                       MARSHAL MODEL                             │
  │  • Receives user task via TUI prompt bar                        │
  │  • Clarifies scope if ambiguous                                 │
  │  • Passes confirmed task description to the loop engine         │
  └──────────────────────────────┬──────────────────────────────────┘
                                 │  task: string
                                 ▼
  ┌─────────────────────────────────────────────────────────────────┐
  │                       LOOP ENGINE (Go)                          │
  │  • Creates isolation branch  marshal/task-<id>                  │
  │  • Records HEAD SHA for revert                                  │
  │  • Manages round counter and feedback injection                 │
  └──────┬──────────────────────────────────────────────────────────┘
         │
         │  ┌─────────────────── ROUND N ───────────────────────────┐
         │  │                                                        │
         │  │   system prompt + task + history + critic feedback     │
         │  │                       │                                │
         │  │                       ▼                                │
         │  │          ┌────────────────────────┐                   │
         │  │          │      EXECUTOR MODEL     │                   │
         │  │          │  writes / edits code    │                   │
         │  │          │  streams output to TUI  │                   │
         │  │          └────────────┬───────────┘                   │
         │  │                       │  prose output                  │
         │  │                       ▼                                │
         │  │          ┌────────────────────────┐                   │
         │  │          │       GIT LAYER         │                   │
         │  │          │  git diff -U1           │                   │
         │  │          └────────────┬───────────┘                   │
         │  │                       │  unified diff                  │
         │  │                       ▼                                │
         │  │          ┌────────────────────────┐                   │
         │  │          │      CRITIC MODEL       │                   │
         │  │          │  reviews diff + output  │                   │
         │  │          │  emits think-blocks     │                   │
         │  │          │  returns JSON verdict   │                   │
         │  │          └────────────┬───────────┘                   │
         │  │                       │                                │
         │  │            ┌──────────┴──────────┐                    │
         │  │            │                     │                     │
         │  │           PASS                 FAIL                    │
         │  │            │                     │                     │
         │  └────────────┼─────────────────────┼────────────────────┘
         │               │                     │
         │               │              rounds left?
         │               │             /         \
         │               │           yes          no
         │               │            │            │
         │               │     inject feedback   REVERT
         │               │     → next round      branch deleted
         │               │
         ▼               ▼
       COMMIT         RESULT
   merge branch     surfaced in
   to original         TUI
```

## 2.2 What each model sees

### Marshal

Receives the raw user input. Its system prompt describes the codebase stack, conventions, and the task schema it must produce. It may ask one clarifying question before confirming the task. Once confirmed, it hands a clean task string to the loop engine and steps back — it has no further involvement until the loop completes and the user submits a new task.

### Executor

Receives a message array structured as:

```
Message 1 (system):  base_executor_prompt
                     + security_standing_instructions  ← cached across all rounds
                     + skill.system_prompt_additions   ← cached across session

Message 2 (user):    task description                  ← cached from round 2+

Message 3..N (conv): history of previous rounds        ← grows per round
                     + critic feedback from last round

Message N+1 (user):  git diff -U1                      ← always last, changes every round
```

The executor never sees the critic's internal reasoning (think-blocks). It sees only the structured feedback fields: `issue` and `fix`.

### Critic

Receives a focused message array:

```
Message 1 (system):  base_critic_prompt
                     + security_standing_checks         ← cached
                     + skill.critic.system_prompt_additions

Message 2 (user):    task description
                     + executor prose output
                     + actual git diff (git diff -U1)
```

The critic's job is narrow: review what actually changed in the repo and issue a structured verdict. It does not see conversation history. It must output valid JSON:

```json
{
  "verdict":  "PASS" | "FAIL",
  "summary":  "one sentence",
  "issue":    "specific problem (FAIL only)",
  "fix":      "exactly what to change (FAIL only)",
  "concerns": ["non-blocking observations"]
}
```

The think-block reasoning chain (from R1-0528) precedes this JSON and is stripped before the verdict is parsed. In Phase 2 it is displayed in the TUI as collapsible italic purple text.

### Compactor

Only invoked when `round > compact_after` (default: 2). Receives all completed rounds as input and outputs a single compressed history message that replaces them. The executor then receives this summary instead of the full round history, keeping the context window bounded. The compactor never sees the git diff or the critic verdict JSON — it works only with the conversational turns.

## 2.3 Information flow table

| Data | From | To | When |
|---|---|---|---|
| Task description | Marshal | Loop engine | Once, at submission |
| System prompt + task | Loop engine | Executor | Every round (cached by Fireworks from round 2) |
| Round history | Loop engine | Executor | Every round (grows) |
| Critic feedback (`issue`, `fix`) | Loop engine | Executor | On retry only |
| Git diff | Git layer | Executor (last message) | Every round |
| Git diff | Git layer | Critic | Every round |
| Executor prose output | Executor | Critic | Every round |
| Structured verdict JSON | Critic | Loop engine | Every round |
| Think-blocks | Critic | TUI (display only) | Every round (stripped from logic path) |
| Completed rounds | Loop engine | Compactor | When `round > compact_after` |
| Compressed history | Compactor | Executor (replaces history) | After compaction |
| Verdict + SHA | Loop engine | Marshal / TUI | On session complete |

---

# 3. Multi-Task Pipeline — Phase 3

## 3.1 Diagram

```
  ┌─────────────────────────────────────────────────────────────────┐
  │                         USER                                    │
  └──────────────────────────────┬──────────────────────────────────┘
                                 │  "add staff portal with timesheets
                                 │   and Rentman integration"
                                 ▼
  ┌─────────────────────────────────────────────────────────────────┐
  │                       MARSHAL MODEL                             │
  │  • Receives feature description                                 │
  │  • Activates pipeline mode                                      │
  │  • Calls planner endpoint to decompose                          │
  │  • Shows plan to user, waits for confirmation                   │
  └──────────────────────────────┬──────────────────────────────────┘
                                 │  task graph JSON
                                 ▼
  ┌─────────────────────────────────────────────────────────────────┐
  │                     PIPELINE SCHEDULER                          │
  │  Topological sort → execution tiers                             │
  │                                                                 │
  │  Tier 1: [Task A, Task C]  ──────────────────────────────────  │
  │                ↓                          ↓                     │
  │         Loop(A) goroutine          Loop(C) goroutine            │
  │         Executor ↔ Critic          Executor ↔ Critic            │
  │         branch: task-A             branch: task-C               │
  │                ↓ PASS                     ↓ PASS                │
  │  Tier 2: [Task B (waits for A), Task E (waits for C)]          │
  │                ↓                          ↓                     │
  │         Loop(B) goroutine          Loop(E) goroutine            │
  │                ↓ PASS                     ↓ PASS                │
  │  Tier 3: [Task D (waits for B)]                                 │
  │                ↓                                                │
  │         Loop(D) goroutine                                       │
  │                ↓ PASS                                           │
  └──────────────────────────────┬──────────────────────────────────┘
                                 │  combined diff of all task branches
                                 ▼
  ┌─────────────────────────────────────────────────────────────────┐
  │                   INTEGRATION CRITIC MODEL                      │
  │  • Reviews combined diff across all tasks                       │
  │  • Identifies cross-task coherence issues                       │
  │  • Returns cross_task_issues JSON                               │
  └──────────────────────────────┬──────────────────────────────────┘
                                 │
                      ┌──────────┴──────────┐
                      │                     │
                    PASS                  FAIL
                      │                     │
               merge all branches    hold branches open
               topological order     report implicated tasks
               single pipeline       targeted rerun available
               commit
```

## 3.2 Task graph schema

The marshal model produces this JSON when decomposing a feature:

```json
{
  "feature": "add staff portal with timesheets and Rentman integration",
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

The `depends_on` field drives the execution tiers. Tasks with overlapping `files_likely_affected` are serialised regardless of declared independence, preventing concurrent writes to the same file.

## 3.3 Per-task model assignment

Each task in the pipeline runs a full single-task loop (Section 2). The task's optional `skill` field activates domain-specific prompt additions for that task's executor and critic. The marshal can route different tasks to different skills — a schema change task uses `schema-migration`, a security review task uses `security-audit`.

```
Task A → skill: schema-migration
  └─ Executor sees: base + security + schema-migration additions
  └─ Critic sees:   base + security checks + schema-migration checks

Task C → no skill
  └─ Executor sees: base + security
  └─ Critic sees:   base + security checks
```

## 3.4 Integration critic vs per-task critic

| | Per-task critic | Integration critic |
|---|---|---|
| **Invoked** | After each executor response | Once, after all tasks complete |
| **Input** | Single task diff + executor output | Combined diff of all task branches |
| **Focus** | Individual correctness | Cross-task coherence |
| **Output** | `verdict` + `issue` + `fix` | `verdict` + `cross_task_issues[]` |
| **Model** | DeepSeek R1-0528 | DeepSeek R1-0528 (same) |
| **Temperature** | 0.6 | 0.6 |
| **On FAIL** | Retry executor in next round | Hold branches, report implicated tasks |

---

# 4. Prompt Layering

Every model call assembles its system prompt from layers. The order is fixed. No layer can reference or override a layer above it.

```
┌─────────────────────────────────────────────────────┐
│  Layer 1: Base prompt                               │
│  Project context, stack, conventions                │
│  → identical across all sessions                    │
├─────────────────────────────────────────────────────┤
│  Layer 2: Security standing instructions            │
│  Always-on, non-negotiable                          │
│  → cannot be removed by skills or config            │
├─────────────────────────────────────────────────────┤
│  Layer 3: Skill additions (optional)                │
│  system_prompt_additions only                       │
│  → extends, never overrides                         │
└─────────────────────────────────────────────────────┘
         ↑ everything above is STATIC per session
         ↑ cached by Fireworks from round 2 onwards
─────────────────────────────────────────────────────
         ↓ dynamic content — never cached
┌─────────────────────────────────────────────────────┐
│  Message array body                                 │
│  Task description, history, feedback, git diff      │
│  → git diff ALWAYS last                             │
└─────────────────────────────────────────────────────┘
```

The reason the git diff must always be last: Fireworks caches exact prompt prefixes. Placing any dynamic content before the diff would invalidate the cache for everything after it, eliminating the 50% cached-token discount.

---

# 5. Model Capability Mapping

Why each model was chosen for its role:

```
MARSHAL (Qwen3 8B / Qwen3 4B local)
├── Hybrid thinking/non-thinking mode
│   ├── /no_think for conversational replies → fast
│   └── /think when building dependency graph → careful
├── Strong instruction following and dialogue
├── Reliable structured JSON output (task graph schema)
└── Cheap ($0.20/M) — marshal calls are short

EXECUTOR (Qwen3 Coder 480B A35B / qwen2.5-coder:7b local)
├── RL-trained on SWE-Bench → learned the read/reason/edit/verify loop
├── 262k context → can hold large codebases
├── Dense architecture considerations aside,
│   35B active parameters per token → fast inference despite large total
└── Produces the actual code changes

CRITIC (DeepSeek R1-0528 / deepseek-r1:7b local)
├── Full 671B MoE reasoning model (not a distil)
├── Think-blocks expose reasoning chain for debugging
├── System prompt support (original R1 lacked this)
├── Better JSON reliability than original R1
├── Temperature 0.6 required (0.0 causes repetition in full R1)
└── Deeper reasoning per round → fewer total rounds → lower overall cost

COMPACTOR (DeepSeek V3.2 / critic model local)
├── Non-reasoning model → no think-block overhead on summarisation
├── High code comprehension for faithful round compression
└── Called infrequently — only when round > compact_after
```

---

# 6. Agentic Extension

> This section covers how Marshal's multi-model design can be extended into broader agentic workflows, and where it maps naturally onto emerging agentic patterns.

## 6.1 What makes Marshal's design agentic-ready

Marshal already has the structural foundations that agentic systems require:

- **Isolation** — every task runs on a dedicated git branch. Mistakes are scoped and reversible.
- **Observation** — the critic observes the actual git diff, not the executor's description of it. This is the agentic pattern of grounding actions in their real-world effects.
- **Retry with feedback** — failed rounds inject the critic's `fix` into the next executor call. This is a minimal form of reflection.
- **Dependency awareness** — the pipeline scheduler understands which tasks must complete before others can start. This is prerequisite for any multi-step autonomous workflow.
- **Pluggable backends** — the `Backend` interface is the only contract. Any model that speaks OpenAI-compatible endpoints can be slotted in.

## 6.2 Phase 3 — Executor tool use

The most direct agentic extension is giving the executor real tools rather than relying on its prose output being applied by the user.

```
Current (Phases 1-2):
  Executor → prose output describing changes
  Human (or convention) → applies changes to filesystem
  Git → produces diff for critic

Phase 3:
  Executor → tool calls: read_file, write_file, run_command
  Tool layer → applies changes directly
  Git → produces diff for critic (same critic, unchanged)
```

The critic pipeline is completely unchanged. Branch isolation remains the safety mechanism. The only change is that the executor has agency over the filesystem rather than producing advisory text.

Tool schema for the executor:

```json
{
  "tools": [
    {
      "name": "read_file",
      "description": "Read the contents of a file in the repository",
      "parameters": {
        "path": { "type": "string" }
      }
    },
    {
      "name": "write_file",
      "description": "Write or overwrite a file in the repository",
      "parameters": {
        "path": { "type": "string" },
        "content": { "type": "string" }
      }
    },
    {
      "name": "run_command",
      "description": "Run a shell command in the repository root",
      "parameters": {
        "command": { "type": "string" }
      },
      "restrictions": "allowlist only: npm test, go test, pytest, tsc, etc."
    }
  ]
}
```

The sandbox policy layer determines which paths and commands are permitted. The critic still reviews the resulting diff — tool use doesn't change the review process, it changes how the diff was produced.

## 6.3 Agentic pattern: Observe-Orient-Decide-Act

Marshal's loop maps directly onto the OODA loop used in most agentic frameworks:

```
OBSERVE:  Critic reads the git diff
          (real observation of actual filesystem state)

ORIENT:   Critic reasons about the diff against the task
          (think-blocks from R1-0528)

DECIDE:   Critic issues structured PASS/FAIL verdict
          with specific issue and fix fields

ACT:      Loop engine routes:
          PASS → commit and merge
          FAIL → inject feedback into executor's next round
```

This is not coincidental — the critic/executor pattern was designed to match this structure. The think-blocks are the "orient" phase made visible.

## 6.4 Extension: Multi-agent swarm (excluded by design)

Marshal explicitly does not use a swarm pattern — agents spawning other agents dynamically. The reasons are architectural:

- Swarms have non-deterministic execution graphs, making debugging difficult
- The DAG-based pipeline scheduler gives the same parallelism benefit with explicit, inspectable structure
- Each task in the pipeline is isolated and independently reversible
- The integration critic provides coherence checking that swarms typically lack

If you want to add more agents to Marshal, the right pattern is to add them as named roles in the pipeline DAG — not as dynamically spawned workers.

## 6.5 Extension: Security audit agent

Marshal's skill system enables a purpose-built security audit workflow that leverages the multi-model design directly:

```
                    ┌─────────────┐
                    │   MARSHAL   │
                    │  (receives  │
                    │ audit task) │
                    └──────┬──────┘
                           │
              ┌────────────▼─────────────┐
              │      PIPELINE PLAN       │
              │  Task A: Scan attack     │
              │          surface         │
              │  Task B: Review auth     │
              │          middleware       │
              │  Task C: Check secrets   │
              │          handling        │
              │  Task D: Dependency      │
              │          audit           │
              └────────────┬─────────────┘
                           │ (all parallel — no deps)
              ┌────────────┴──────────────┐
              │                           │
    ┌─────────▼──────────┐   ┌────────────▼────────────┐
    │   Executor A       │   │   Executor B             │
    │  skill:            │   │  skill:                  │
    │  security-audit    │   │  security-audit           │
    │  (finds vulns)     │   │  (reviews auth)           │
    └─────────┬──────────┘   └────────────┬─────────────┘
              │                           │
    ┌─────────▼──────────┐   ┌────────────▼────────────┐
    │   Critic A         │   │   Critic B               │
    │  (verifies         │   │  (verifies auth          │
    │   exploitability)  │   │   bypass is real)         │
    └────────────────────┘   └─────────────────────────┘
                           │
              ┌────────────▼────────────┐
              │  INTEGRATION CRITIC     │
              │  Reviews combined       │
              │  security findings      │
              │  for duplicates and     │
              │  cross-cutting issues   │
              └─────────────────────────┘
```

Each executor is prompted to find vulnerabilities; each critic verifies they are genuine and rates severity. The integration critic deduplicates and produces the final report. This is the security-audit built-in skill applied at pipeline scale.

## 6.6 Extension: Test-driven development agent

A natural workflow for Marshal's pipeline: write tests first, then implement.

```
Task A: Generate test suite (skill: test-generation)
  └─ Executor writes tests for the feature
  └─ Critic checks test quality and coverage
  └─ PASS: tests committed to branch A

Task B: Implement feature to pass tests (depends_on: A)
  └─ Executor reads committed tests from Task A's branch
  └─ Executor writes implementation
  └─ Critic runs tests: does the implementation pass?
  └─ PASS: implementation committed

Integration critic: do tests and implementation form a coherent pair?
```

The dependency graph forces the right order. The executor on Task B can read Task A's committed tests as context, because Task A's branch is held open pending the integration critic.

## 6.7 Extension: Continuous integration agent

Marshal's headless mode (`--no-tui --json`) makes it a natural CI participant:

```yaml
# .github/workflows/marshal.yml
- name: Run Marshal on failing tests
  run: |
    marshal run --no-tui --json \
      "Fix the failing tests in the test suite output below: $(cat test_output.txt)"
  env:
    FIREWORKS_API_KEY: ${{ secrets.FIREWORKS_API_KEY }}
```

The structured JSON output enables CI pipelines to:
- Parse the verdict and fail the build on FAIL
- Extract the commit SHA on PASS and tag it
- Log token costs per run for spend tracking
- Surface critic feedback as PR comments

The multi-model design adds value here because the critic's structured `issue` and `fix` fields are machine-parseable — a swarm's freeform output would not be.

## 6.8 Extension: Long-horizon refactoring agent

For large codebase migrations (e.g. upgrading a framework, migrating from REST to tRPC, moving from JavaScript to TypeScript), the pipeline can be composed into phases:

```
Phase 1: Analysis pipeline (parallel)
  Task A: Identify all files touching the old API
  Task B: Map dependency graph of affected modules
  Task C: Document current API surface

Phase 2: Migration pipeline (depends on Phase 1)
  Tasks D..N: Migrate one module per task (parallel where files don't overlap)
  Integration critic: do migrated modules form a coherent API?

Phase 3: Verification pipeline (depends on Phase 2)
  Task: Run full test suite, fix failures
  Task: Update documentation
```

Each phase is a separate `marshal pipeline` invocation. The git history produced by Phase 1 is available to Phase 2's executor as committed context. This is how Marshal handles work that would overflow a single context window — not by increasing the context window, but by structuring the work into dependent stages.

## 6.9 The multi-model advantage in agentic contexts

The key insight that motivates the entire design: **specialisation beats general capability at every scale**.

A single large model doing everything — planning, coding, reviewing — produces worse results than specialised models doing their bounded jobs, because:

1. The planner/marshal role optimises for coherent dialogue and structured decomposition, not code generation
2. The executor role optimises for code generation quality and codebase familiarity
3. The critic role optimises for adversarial review — it is specifically trained to find problems, not create solutions

When you extend this into agentic workflows, the same principle applies. Each agent in the pipeline should have a narrow, well-defined job that plays to its model's strengths. Marshal's `skill` system is the mechanism for further specialising agents within a given role.

The multi-model design also makes the system inspectable: because each model has one job, when something goes wrong it is usually clear which model failed and why. The session store records every verdict, every think-block, every diff. This observability is what makes agentic workflows debuggable rather than magical.

---

# 7. Quick Reference

## 7.1 Model → role mapping

```
User input
    │
    ▼
MARSHAL (qwen3-8b)        ← conversation, planning, task graph
    │
    ▼
LOOP ENGINE (Go)          ← orchestration, git, session store
    │
    ├──► EXECUTOR (qwen3-coder-480b) ← writes code
    │
    ├──► CRITIC (deepseek-r1-0528)  ← reviews diff
    │         └── think-blocks → TUI display
    │
    └──► COMPACTOR (deepseek-v3p2) ← compresses history (when needed)
                                      (integration critic in Phase 3)
```

## 7.2 When each model is invoked

| Model | Invoked | Frequency |
|---|---|---|
| Marshal | User submits a task | Once per task, then silent |
| Executor | Each round of the loop | 1–3× per task (default max_rounds=3) |
| Critic | After each executor response | Same as executor |
| Compactor | When `round > compact_after` | Rarely — only on long sessions |
| Integration critic (Phase 3) | After all pipeline tasks complete | Once per pipeline |

## 7.3 Temperature settings and why

| Model | Temperature | Reason |
|---|---|---|
| Marshal | 0.6 | Qwen3 recommendation for thinking mode; conversational warmth |
| Executor | 0.2 | Low for correct code; small variance allows creative solutions on retry |
| Critic | 0.6 | R1 series requirement — 0.0 causes repetition in the full R1 model |
| Compactor | 0.0 | Summarisation must be faithful and deterministic |
| Integration critic | 0.6 | Same as critic |

## 7.4 marshal.toml (prod)

```toml
[marshal]
model       = "accounts/fireworks/models/qwen3-8b"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.6
max_tokens  = 2048

[executor]
model       = "accounts/fireworks/models/qwen3-coder-480b-a35b-instruct"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.2
max_tokens  = 4096

[critic]
model       = "accounts/fireworks/models/deepseek-r1-0528"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.6
max_tokens  = 8192
json_output = true

[planner]
model       = "accounts/fireworks/models/deepseek-v3p2"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.0
max_tokens  = 2048

[integration_critic]
model       = "accounts/fireworks/models/deepseek-r1-0528"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.6
max_tokens  = 8192
```

## 7.5 marshal.toml (dev/local)

```toml
[marshal]
model       = "qwen3:4b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.6
max_tokens  = 1024

[executor]
model       = "qwen2.5-coder:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.2
max_tokens  = 2048

[critic]
model       = "deepseek-r1:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.6
max_tokens  = 4096
json_output = true
```
