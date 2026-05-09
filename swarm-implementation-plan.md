# Implementation Plan — AI Swarm Coding Tool (Go)

A phased plan structured so each milestone produces a runnable system you can actually test. Go is well-suited here: strong concurrency primitives for the scheduler, solid HTTP/streaming support for model adapters, and the static typing helps keep agent contracts honest.

This is the **v1 plan**. Items beyond v1 (LoRA stacking, self-improving loops, advanced TUI animations) are listed in the "Future Directions" section at the end, intentionally not part of the committed schedule.

## Exploration Flag Legend

Items marked with the following flags need further investigation before development starts.

- 🔴 **Needs design spike** — significant architectural decision, build a throwaway prototype
- 🟡 **Needs validation** — assumptions to verify against real data or provider behaviour
- 🟢 **Needs library check** — verify the tool/library actually does what's assumed

## Project Structure

```
swarm/
├── cmd/
│   └── swarm/              # CLI entry point
├── internal/
│   ├── session/            # L2: conversation + session manager
│   ├── orchestrator/       # L3: planner, scheduler, replanner
│   ├── graph/              # task graph data structures
│   ├── agent/              # L4: agent runtime, lifecycle
│   ├── context/            # context store (tier 3) + search index
│   ├── knowledge/          # L4.5: knowledge tier (search + agent)
│   ├── kb/                 # precomputed knowledge base (symbols, summaries)
│   ├── gateway/            # L5: model gateway + adapters + detection
│   │   ├── anthropic/
│   │   ├── openai/         # OpenAI / OpenRouter / Ollama / LM Studio / vLLM
│   │   ├── runpod/
│   │   ├── fireworks/      # supports LoRA
│   │   └── detect/         # provider auto-detection
│   ├── profiles/           # named role-binding profiles
│   ├── lora/               # adapter registry + binding extension (advanced)
│   ├── tools/              # L6: fs, shell, git, test runners, editors
│   ├── tui/                # M12: graph/focus/flow/chat modes
│   ├── events/             # SwarmEvent bus
│   ├── sandbox/            # L7: exec isolation
│   └── telemetry/          # structured logging, traces
├── pkg/
│   ├── manifest/           # role manifest parsing (public API)
│   └── protocol/           # shared types (SwarmEvent, etc.)
├── configs/
│   ├── roles/              # *.yaml role manifests
│   ├── profiles/           # *.yaml profiles (local-only, balanced, ...)
│   └── local-catalog.yaml  # curated local model recommendations
├── test/
│   ├── integration/
│   └── fixtures/
└── go.mod
```

**Module choice rationale.** `internal/` for anything not meant as a reusable library; `pkg/` for the manifest format and event protocol, which third parties might build against. Keep adapter-specific code firewalled inside `internal/gateway/<provider>/` so the rest of the codebase talks only to the normalised interface.

## Milestone 0 — Scaffolding & Core Types (Week 1)

Ship a buildable binary that does nothing interesting but has every package defined with the core types.

### Deliverables

- `go.mod` with minimal deps: `github.com/spf13/cobra` (CLI), `github.com/charmbracelet/bubbletea` (TUI, later), `gopkg.in/yaml.v3`, `go.uber.org/zap` (logging), `github.com/google/uuid`.
- Core protocol types in `pkg/protocol/`:

```go
// pkg/protocol/events.go
package protocol

type EventKind string

const (
    EventTaskSpawned     EventKind = "task_spawned"
    EventTaskProgress    EventKind = "task_progress"
    EventToolCall        EventKind = "tool_call"
    EventToolResult      EventKind = "tool_result"
    EventModelCall       EventKind = "model_call"
    EventTaskCompleted   EventKind = "task_completed"
    EventTaskFailed      EventKind = "task_failed"
    EventUserInterrupt   EventKind = "user_interrupt"
    EventGraphMutation   EventKind = "graph_mutation"
    EventKnowledgeQuery  EventKind = "knowledge_query"
    EventKBMutation      EventKind = "kb_mutation"
    EventEditProposed    EventKind = "edit_proposed"
    EventEditApplied     EventKind = "edit_applied"
    EventEditRejected    EventKind = "edit_rejected"
    EventProfileChanged  EventKind = "profile_changed"
    EventCostUpdate      EventKind = "cost_update"
)

type SwarmEvent struct {
    ID        string          `json:"id"`
    Kind      EventKind       `json:"kind"`
    SessionID string          `json:"session_id"`
    AgentID   string          `json:"agent_id,omitempty"`
    ParentID  string          `json:"parent_id,omitempty"`
    Timestamp time.Time       `json:"timestamp"`
    Payload   json.RawMessage `json:"payload"`
}
```

- `pkg/protocol/context.go` for `ContextRef`, `ContextEntry`.
- `internal/graph/task.go` for `Task`, `TaskStatus`, `TaskSpec`.

### Exploration Flags

- 🟢 **Event schema stability** — confirm the `json.RawMessage` payload approach keeps the core type stable as new kinds are added without breaking persistence.

### Tests

- `TestProtocolTypes_RoundTripJSON` — marshal/unmarshal every event kind, every task status.
- `TestTaskSpec_Validate` — reject specs with empty goal, missing output schema, circular deps declared inline.

### Expected output

```
$ swarm version
swarm 0.1.0-dev (go1.23.0)

$ swarm doctor
✓ config directory:  ~/.config/swarm
✗ providers:         none configured (run: swarm)
```

## Milestone 1 — Model Gateway with Two Adapters (Weeks 2–3)

This is the foundation. If you get this right, everything downstream becomes much easier.

### Design

```go
// internal/gateway/gateway.go
package gateway

type ChatRequest struct {
    Messages    []Message
    Tools       []ToolDef
    MaxTokens   int
    Temperature float64
    StopWhen    []string
}

type Message struct {
    Role    Role              // system|user|assistant|tool
    Content []ContentBlock    // text, tool_use, tool_result
}

type StreamEvent struct {
    Kind    StreamEventKind   // delta|tool_call|done|error
    Text    string
    ToolCall *ToolCall
    Usage   *Usage
    Err     error
}

type Binding struct {
    Provider string            // "anthropic" | "openai" | "openrouter" | "ollama" | "lmstudio" | "vllm" | "runpod" | "fireworks"
    Model    string
    Endpoint string            // optional override
    AuthRef  string            // key into secret store
    LoRAs    []string          // adapter ids/paths; empty for most users (M8.5)
}

type Gateway interface {
    Complete(ctx context.Context, binding Binding, req ChatRequest) (<-chan StreamEvent, error)
}
```

Each adapter implements a `Provider` interface and the gateway selects by `Binding.Provider`. Normalisation happens at the adapter boundary.

### Adapter responsibilities

**Anthropic adapter** — handles the `content` block array format, maps `tool_use` blocks to `ToolCall`, surfaces `stop_reason` correctly, and normalises the Messages API streaming events.

**OpenAI-compatible adapter** — single adapter handles OpenAI, OpenRouter, RunPod vLLM, Ollama, LM Studio, and self-hosted vLLM. All speak OpenAI Chat Completions; differences live in `Binding.Endpoint` and auth handling. This is the workhorse adapter.

**Fireworks adapter** (slim wrapper around OpenAI-compatible with LoRA parameter support) — added in M8.5 if/when the user opts into adapters.

### Routing and policies

```go
// internal/gateway/router.go
type Router struct {
    bindings map[string]Binding    // role -> binding
    fallback map[string]Binding    // role -> fallback binding
    budget   *BudgetTracker
}

func (r *Router) Resolve(role string, estTokens int) (Binding, error) {
    // 1. check budget
    // 2. check rate limits
    // 3. return primary or fallback
}
```

### Exploration Flags

- 🔴 **Tool call schema normalisation across providers** — Anthropic, OpenAI, vLLM/Ollama all handle tool calls differently, especially around partial JSON in streaming tool arguments, reasoning-model quirks (R1-style `<think>` blocks), and parallel tool calls. Build a small harness that hits each provider with the same tool-using prompt and document the delta before locking in the canonical `ToolCall` type.
- 🟡 **SSE parsing robustness** — providers differ on reconnection, keepalives, and partial event delivery.
- 🟡 **Token counting pre-request** — estimating tokens for budget gating. Anthropic has a count endpoint; OpenAI-compatible servers vary. May need a fallback heuristic.
- 🟡 **Reasoning model output handling** — DeepSeek-R1 and similar emit thinking tokens that may or may not be separable. Decide whether to surface, hide, or discard in normalisation.
- 🟢 **RunPod endpoint format** — confirm specific deployments are OpenAI-compatible at the wire level.

### Tests

- `TestAnthropicAdapter_StreamingNormalization` — replay recorded SSE fixtures, assert normalised event sequence.
- `TestOpenAIAdapter_ToolCallMapping` — verify OpenAI-style tool_calls arrays convert to canonical `ToolCall` objects.
- `TestRouter_FallbackOnProviderError` — primary returns 503, confirm fallback is invoked.
- `TestRouter_BudgetRejection` — exhausted budget, requests fail with `ErrBudgetExceeded`.
- Integration test (optional, behind `-tags=live`): hit real providers with a trivial prompt.

### Expected output

````
$ swarm gateway test --role=codegen --prompt="write a Go function that reverses a string"
Resolved binding: ollama/qwen2.5-coder:14b
Streaming...
Here's a function that reverses a string in Go:

```go
func Reverse(s string) string {
    runes := []rune(s)
    for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
        runes[i], runes[j] = runes[j], runes[i]
    }
    return string(runes)
}
```

---
Tokens: in=42, out=89, latency=1.8s, cost=$0 (local)
````

## Milestone 1.5 — Provider Detection & Profiles (Weeks 3.5–4)

This milestone is about making the tool *easy to start using*. The goal: a brand-new user can install, run, and have a working swarm with no API keys and no config file, by leaning on local models. Users with API keys get auto-configured for the best mix of cloud + local.

### Auto-Detection at Startup

When the swarm CLI starts, it runs a parallel probe pass against known endpoints and env vars:

```go
// internal/gateway/detect/detector.go
type Detector struct {
    timeout time.Duration  // ~200ms per probe
}

type DetectedProvider struct {
    Name             string   // "ollama" | "lmstudio" | "anthropic" | ...
    Endpoint         string
    AuthAvailable    bool
    AvailableModels  []string // populated for local servers
}

func (d *Detector) Probe(ctx context.Context) []DetectedProvider {
    // parallel probes with short timeout
}
```

Probes:
- **Local servers** (HTTP probe + model listing): Ollama @ `:11434`, LM Studio @ `:1234`, vLLM @ `:8000`, custom endpoints from `SWARM_LOCAL_ENDPOINT` env var
- **Cloud providers** (env var presence): `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `FIREWORKS_API_KEY`, `RUNPOD_API_KEY`

Total detection adds ~300ms to cold start. Cached for the session.

### Named Profiles

Profiles bundle role-to-binding decisions so users don't think about role mappings unless they want to. Initial set (small, deliberately):

```yaml
# configs/profiles/local-only.yaml
name: local-only
description: "All inference local. No API keys needed. Best for privacy or offline work."
required: [ollama_or_lmstudio]
bindings:
  orchestrator: { provider: local, role_hint: large }
  codegen:      { provider: local, role_hint: code }
  knowledge:    { provider: local, role_hint: small }
  reviewer:     { provider: local, role_hint: large }
  summariser:   { provider: local, role_hint: small }

# configs/profiles/balanced.yaml
name: balanced
description: "Frontier orchestration, local execution. Best cost/quality tradeoff."
required: [anthropic_or_openai, ollama_or_lmstudio]
bindings:
  orchestrator: { provider: anthropic, role_hint: large }
  codegen:      { provider: local,     role_hint: code }
  knowledge:    { provider: anthropic, role_hint: small }
  reviewer:     { provider: anthropic, role_hint: large }
  summariser:   { provider: local,     role_hint: small }

# configs/profiles/quality.yaml
name: quality
description: "Frontier models for everything. Best results, highest cost."
required: [anthropic]
bindings:
  orchestrator: { provider: anthropic, role_hint: large }
  codegen:      { provider: anthropic, role_hint: large }
  knowledge:    { provider: anthropic, role_hint: small }
  reviewer:     { provider: anthropic, role_hint: large }
  summariser:   { provider: anthropic, role_hint: small }

# configs/profiles/budget.yaml
name: budget
description: "Cheapest viable. Mix of small frontier models and local."
required: [openrouter_or_anthropic, ollama_or_lmstudio]
bindings:
  orchestrator: { provider: openrouter, model: deepseek/deepseek-chat }
  codegen:      { provider: local,      role_hint: code }
  knowledge:    { provider: local,      role_hint: small }
  reviewer:     { provider: openrouter, model: deepseek/deepseek-r1 }
  summariser:   { provider: local,      role_hint: small }
```

**`role_hint` is intentional.** A profile says `role_hint: code` rather than naming a specific local model. The resolver matches the hint against locally available models using a curated catalog. This means a profile is portable — the same `local-only` config works on a laptop with `qwen2.5-coder:14b` and a server with `qwen2.5-coder:32b`.

### Local Model Catalog

```yaml
# configs/local-catalog.yaml
local_recommendations:
  small:
    - { name: qwen2.5:7b,        size_gb: 4.7, good_for: [knowledge, summariser] }
    - { name: llama3.2:3b,       size_gb: 2.0, good_for: [knowledge, summariser], note: "faster, lower quality" }
  
  code:
    - { name: qwen2.5-coder:14b, size_gb: 9.0,  good_for: [codegen], note: "best balance" }
    - { name: qwen2.5-coder:32b, size_gb: 20.0, good_for: [codegen, reviewer], note: "needs ≥24GB VRAM" }
    - { name: deepseek-coder-v2:16b, size_gb: 9.5, good_for: [codegen] }
  
  large:
    - { name: deepseek-r1-distill-qwen:32b, size_gb: 20.0, good_for: [orchestrator, reviewer], note: "reasoning" }
```

### Profile Selection Logic

```
1. User explicit selection (--profile or .swarm/config.yaml) → use it
2. Auto-select based on detected providers:
   - Anthropic (or OpenAI) + local available → "balanced"
   - Only cloud frontier → "quality"
   - Only local → "local-only"
   - Nothing → fail with clear setup instructions
3. Reasoning is shown to user
```

### Friendly First-Run

```
$ swarm
Welcome to swarm. Detecting your environment...

  ✓ Ollama running with 1 model (qwen2.5:7b)
  ✗ No frontier API keys detected

I can run in local-only mode. For best results on code tasks I'd 
recommend pulling qwen2.5-coder:14b (~9GB). Pull now? [y/N]
> y
[ollama] pulling qwen2.5-coder:14b... done in 2m34s.

Active profile: local-only
All inference will run on your machine. No data leaves your computer.

Ready. Type your first prompt or /help for commands.
> 
```

If `ANTHROPIC_API_KEY` is already in env at first run, the user lands in `balanced` automatically with a one-line summary and no decisions to make.

### Failure Recovery (the "three options" pattern)

Every failure response includes specific recovery commands:

```
✗ orchestrator → anthropic/claude-opus-4-7

Cannot reach Anthropic API.
Reason: ANTHROPIC_API_KEY environment variable is not set.

Fix one of:
  1. export ANTHROPIC_API_KEY=sk-ant-...   (get one at https://console.anthropic.com)
  2. swarm profile use local-only          (run everything locally)
  3. swarm profile use budget              (use OpenRouter, cheaper)
```

### Per-Role Override

Users can override a single binding without abandoning the profile:

```
> /model codegen anthropic://claude-opus-4-7
✓ codegen → anthropic/claude-opus-4-7 (was: ollama/qwen2.5-coder:14b)
```

### CLI

```
swarm                            # auto-detect + first-run if needed
swarm profiles list              # show available profiles
swarm profile use <name>         # switch profile
swarm profile show               # current bindings
swarm models recommend <role>    # show catalog matches for a role
swarm models pull <name>         # convenience wrapper for ollama pull
swarm doctor                     # detection + profile state report
```

### Exploration Flags

- 🔴 **Profile design and naming** — the named profiles are the user's primary mental model. Validate names and bindings with a few real users before locking in. The 4-profile set may be wrong.
- 🔴 **Role hint vocabulary** — "small/code/large" is a starting point but ignores reasoning models, vision, and other axes. Decide carefully because it appears in profile YAML and the local catalog.
- 🔴 **Local catalog curation and maintenance** — recommended models change every few months. Decide update mechanism (remote URL? bundled with releases? both?) and ownership.
- 🟡 **Probe false positives** — Ollama responds at :11434 even with no models loaded. Probes must verify capability, not just port-open.
- 🟡 **Auto-pull consent** — pulling 9GB is a real action. Always require explicit consent, never silent download.
- 🟡 **Profile override granularity** — single-role override should be persistent or session-only? Persistent is more useful, session-only is safer.
- 🟡 **First-run minimal vs verbose** — `--no-prompt` flag for scripted setups.
- 🟢 **Endpoint detection for non-default ports** — support `OLLAMA_HOST`, `LMSTUDIO_HOST` env vars, then config file.

### Tests

- `TestDetector_FindsRunningOllama` — fake Ollama at expected URL, detector finds it.
- `TestDetector_TimesOutGracefully` — unreachable URL doesn't block detection.
- `TestDetector_HandlesEmptyOllama` — Ollama running but no models loaded; detected with `available_models: []`.
- `TestProfile_AutoSelection_AnthropicAndLocal` — both available → balanced.
- `TestProfile_AutoSelection_OnlyLocal` → local-only.
- `TestProfile_AutoSelection_Nothing` → fails with setup instructions.
- `TestProfile_RoleHintResolves` — role_hint=code with qwen-coder pulled → that's the binding.
- `TestProfile_RoleHintFallsBackWithWarning` — only llama3.2:3b → uses it but warns.
- `TestProfile_OverrideKeepsRest` — `/model codegen X` replaces only that binding.
- `TestErrors_AuthFailure_ShowsFixCommands` — failure response includes three-options pattern.
- `TestPullPrompt_RequiresConsent` — no auto-pull without explicit y.

### Expected output

```
$ swarm doctor
Detected providers:
  ✓ Ollama          http://localhost:11434  (3 models: qwen2.5:7b, qwen2.5-coder:14b, llama3.2:3b)
  ✓ Anthropic       env: ANTHROPIC_API_KEY set
  ✗ OpenRouter      env: OPENROUTER_API_KEY not set
  ✗ LM Studio       not detected at :1234

Active profile: balanced (auto-selected)
  orchestrator → anthropic/claude-opus-4-7
  codegen      → ollama/qwen2.5-coder:14b
  knowledge    → anthropic/claude-haiku-4-5
  reviewer     → anthropic/claude-opus-4-7
  summariser   → ollama/qwen2.5-coder:14b

Local utilisation: 60%  Estimated cost per task: ~$0.05
```

## Milestone 2 — Context Store with Search Index (Week 5)

Before agents, build the context store. It's small, well-defined, and the thing agents will be built around.

### Implementation

```go
// internal/context/store.go
type Store interface {
    Put(ctx context.Context, entry Entry) (ContextRef, error)
    Get(ctx context.Context, ref ContextRef) (Entry, error)
    List(ctx context.Context, query Query) ([]Entry, error)
    Search(ctx context.Context, query SearchQuery) ([]SearchResult, error)
    Supersede(ctx context.Context, old, new ContextRef) error
}

type Entry struct {
    Key         ContextKey
    Kind        EntryKind
    Content     []byte
    ContentHash string      // sha256
    ProducedBy  string
    ProducedAt  time.Time
    Metadata    Metadata
}

type Metadata struct {
    SizeTokens   int
    Tags         []string
    TTL          time.Duration
    SupersededBy ContextRef
}

type SearchQuery struct {
    Query      string
    Kinds      []EntryKind
    Tags       []string
    PathGlob   string
    ProducedBy string
    LatestOnly bool
    Limit      int
}

type SearchResult struct {
    Entry   Entry
    Score   float64
    Snippet string
}
```

**Backend:** file-backed blobs under `.swarm/sessions/<session-id>/ctx/<sha>.blob`, metadata in SQLite per session. SQLite's FTS5 (bundled into `modernc.org/sqlite`, no CGo needed) provides BM25-ranked full-text search.

**Key format:** `<kind>/<path-or-id>@sha256:<hex>`. Content-addressed.

```sql
CREATE VIRTUAL TABLE entries_fts USING fts5(
    key UNINDEXED,
    content,
    tags,
    kind UNINDEXED,
    tokenize = 'porter unicode61 remove_diacritics 2'
);
```

### Content hashes exposed to tools

`ContextRef` returned by `read_file` carries the content hash. Mutating tools accept the hash as a precondition. Foundation for staleness protection in M6.

### Exploration Flags

- 🔴 **Tokeniser choice for code and docs** — `porter` stemmer is for English prose, not code. Evaluate options against representative queries before committing.
- 🟡 **FTS storage overhead** — FTS5 roughly doubles storage per entry. Measure on a realistic session.
- 🟡 **Content size limits** — decide index-exclusion rule (skip FTS for entries > 1MB, or for `kind=test_run`).
- 🟡 **Token counting tokeniser** — Anthropic and OpenAI tokenisers differ. Decide on canonical tokeniser with ±10% tolerance.
- 🟢 **`modernc.org/sqlite` FTS5 support** — verify with a trivial test.

### Tests

- `TestStore_PutGetRoundTrip`
- `TestStore_ContentAddressing`
- `TestStore_SupersedeChain`
- `TestStore_TagQuery`
- `TestStore_ConcurrentWrites`
- `TestStore_TokenAccounting`
- `TestStore_Search_BM25Ranking`
- `TestStore_Search_FilterCombination`
- `TestStore_Search_IndexConsistency`

### Expected output

```
$ swarm ctx put --kind=file --path=src/auth.go < auth.go
Stored: files/src/auth.go@sha256:a3f21c...
  Size: 4,218 bytes (1,104 tokens)

$ swarm ctx search "jwt middleware authentication" --limit=3
RANK  SCORE   KEY                                    SNIPPET
1     8.42    files/src/auth.go@sha256:a3f21c...     ...validates the <mark>JWT</mark>
2     6.18    docs/auth.md@sha256:c4d7e1...          ...<mark>authentication</mark> <mark>middleware</mark>
```

## Milestone 3 — Agent Runtime with a Single Role (Weeks 6–7)

Build the agent runtime and prove it end-to-end with one role: `codegen`.

### Core abstractions

```go
// internal/agent/agent.go
type Agent struct {
    ID       string
    Role     string
    Manifest Manifest
    Task     graph.Task
    gateway  gateway.Gateway
    store    context.Store
    tools    tools.Registry
    events   events.Publisher
    readSet  *ReadSet
}

type Manifest struct {
    Role          string              `yaml:"role"`
    ModelBinding  string              `yaml:"model_binding"`
    SystemPrompt  string              `yaml:"system_prompt"`
    Tools         []string            `yaml:"tools"`
    ContextPolicy ContextPolicy       `yaml:"context_policy"`
    OutputSchema  json.RawMessage     `yaml:"output_schema"`
    MaxIterations int                 `yaml:"max_iterations"`
    Timeout       time.Duration       `yaml:"timeout_s"`
}

func (a *Agent) Run(ctx context.Context) (Result, error) {
    prompt := a.assembleContext()
    stream, err := a.gateway.Complete(ctx, a.binding(), prompt)
    if err != nil {
        return Result{}, err
    }
    return a.runLoop(ctx, stream)
}
```

The `runLoop` handles the tool-call cycle. Terminates on `stop_reason=end_turn`, iteration limit, or timeout.

### Per-agent read-set

The runtime tracks which files have been read at what content hash. Foundation for read-before-edit enforcement in M6.

```go
type ReadSet struct {
    Entries map[string]ReadRecord  // path -> record
}

type ReadRecord struct {
    Hash    string
    ReadAt  time.Time
    ReadVia string  // tool name
}
```

### Exploration Flags

- 🔴 **Output schema enforcement strategy** — provider-native structured output (Anthropic tool use, OpenAI JSON mode) vs. application-side validation with retry. Prototype both.
- 🔴 **Summariser-on-demand trigger** — when an input exceeds the limit, how do we summarise? Options: (a) inline summariser call (blocking, expensive), (b) pre-summarise at `Put` time (wasteful if unread), (c) hybrid with cache.
- 🟡 **Tool-call loop termination conditions** — beyond iteration limit, detect repeated identical tool calls (stuck agent).
- 🟡 **Partial-progress on timeout** — affects replan logic in M4.
- 🟡 **Read-set scope and expiry** — per-agent across run, or per-task? When does a read "expire"?
- 🟢 **Tool arg streaming** — invoke tool when args complete in stream, or wait for full turn? Simpler to wait.

### Tests

- `TestAgent_HappyPath`
- `TestAgent_ToolCallLoop`
- `TestAgent_TimeoutTerminates`
- `TestAgent_MaxIterationsRespected`
- `TestAgent_OutputSchemaValidation`
- `TestAgent_ReadSetTracking`
- `TestAssembly_LargeInputTriggersSummary`

### Expected output

```
$ swarm agent run --role=codegen --task="Add a String() method to the User struct in models/user.go"
[agent_01HXYZ] spawned role=codegen binding=ollama/qwen2.5-coder:14b
[agent_01HXYZ] tool_call read_file path=models/user.go
[agent_01HXYZ] tool_result 312 bytes (hash: 7f3c1a...)
[agent_01HXYZ] tool_call edit_file path=models/user.go (expected_hash: 7f3c1a...)
[agent_01HXYZ] completed in 4.3s

Diff:
+func (u User) String() string {
+    return fmt.Sprintf("User{ID: %s, Name: %q}", u.ID, u.Name)
+}
```

## Milestone 3.5 — Knowledge Tier (Week 7.5)

Layered retrieval system between context store and agents.

### Three Layers

```
Layer A: Deterministic (zero model cost, ~1ms)
  ctx_fetch(key)
  ctx_list(tags, kinds, paths, ...)

Layer B: Search (zero model cost, ~5-20ms)
  ctx_search(query, mode)            BM25 / hybrid

Layer C: LLM-mediated (cheap model, ~500ms-2s)
  query_knowledge(question)          Knowledge Agent uses A+B internally
```

### Knowledge Agent

```yaml
# configs/roles/knowledge.yaml
role: knowledge
model_binding: $models.fast
system_prompt: ./prompts/knowledge.md
tools: [ctx_search, ctx_fetch, ctx_list, project_conventions_get]
context_policy:
  inherit: [project_root, active_files_index]
  exclude: [full_conversation, task_graph, decision_log]
output_schema: KnowledgeAnswer
max_iterations: 3
timeout_s: 15
```

```go
type KnowledgeAnswer struct {
    Answer     string         `json:"answer"`
    Citations  []ContextRef   `json:"citations"`   // required, non-empty
    Confidence Confidence     `json:"confidence"`  // high|medium|low|unknown
    Followups  []string       `json:"followups"`
}
```

Non-empty citations are schema-enforced.

### Tools Exposed to Other Agents

- `ctx_fetch(key)` — exact retrieval
- `ctx_list(...)` — structural filter
- `ctx_search(query, mode)` — full-text search
- `query_knowledge(question)` — natural-language

### Query Cache

LRU keyed on `(question_normalised, hash_of_referenced_entries)`.

### Exploration Flags

- 🔴 **Knowledge Agent system prompt design** — prototype with 10-20 representative questions before locking in.
- 🔴 **Hallucination guardrails beyond schema** — citations-required is necessary but not sufficient. Evaluate post-hoc validation, LLM-judge, pattern-match.
- 🔴 **Cache invalidation correctness** — must record every entry inspected, not just cited.
- 🟡 **`ctx_search` vs `query_knowledge`** — system-prompt tuning problem for calling roles.
- 🟡 **Scope-limited queries** — `query_knowledge(question, scope=...)` — decide vocabulary early.
- 🟡 **Semantic search deferral** — confirm BM25 is sufficient initially.
- 🟢 **Model binding default** — bakeoff Haiku vs DeepSeek-V3 vs local Qwen-Coder.

### Tests

- `TestKnowledgeAgent_AlwaysCitesSources`
- `TestKnowledgeAgent_UnknownOnInsufficientContext`
- `TestKnowledgeAgent_CitesRelevantEntries` (LLM-judged in golden fixtures)
- `TestKnowledgeCache_Hit`
- `TestKnowledgeCache_InvalidatedOnContentChange`
- `TestKnowledgeCache_InvalidatedOnUncitedDependency`
- `TestKnowledgeAgent_ConcurrentQueries`
- Golden fixture: 15 canonical questions vs seeded store + recorded provider.

### Expected output

```
$ swarm knowledge query "what's the error handling convention in this project?"
[knowledge] (haiku-4-5) ✓ done (0.8s, $0.0002)

Answer:
  Errors are wrapped with pkg/errors.Wrap() and include a request-scoped
  correlation ID. HTTP handlers use echo.NewHTTPError with structured
  error codes from internal/errors/codes.go.

Confidence: high
Citations:
  - docs/error-handling.md@sha256:c8d3e1a...
  - internal/errors/codes.go@sha256:7f2b4c9...
```

## Milestone 3.75 — Knowledge Base Foundation (Week 8)

Precomputed structured understanding agents consult before invoking the Knowledge Agent. Deterministic symbol indexing only; LLM summaries come in M3.8.

### Symbol Index

Tree-sitter parsing for Go, TypeScript, Python, Rust (initial set). Extracts declarations with line ranges, imports/exports, call graph (within and across files), type references.

```go
type SymbolIndex struct {
    File        string
    ContentHash string
    Symbols     []Symbol
    Imports     []Import
    IndexedAt   time.Time
    Parser      string
}

type Symbol struct {
    Name       string
    Kind       SymbolKind
    Range      LineRange
    Parent     string
    Signature  string
    References []Reference
}
```

### Tools (deterministic, no LLM)

- `kb_symbol_lookup(name, path_hint?)`
- `kb_symbol_references(name)`
- `kb_file_symbols(path)`
- `kb_package_exports(path)`
- `kb_project_map()`

Sub-millisecond latency, free.

### Maintainer

`fsnotify` watcher → reindex changed files. On change: hash compare, re-parse, diff symbols, write new SymbolIndex, supersede old.

### CLI

```
swarm kb status
swarm kb rebuild [--path=...]
swarm kb verify
swarm kb ignore <pattern>
```

### Exploration Flags

- 🔴 **Tree-sitter vs LSP** — LSP more accurate (type resolution, cross-file refs); tree-sitter simpler (no language-server dependency). Prototype on TypeScript/Go.
- 🔴 **Cross-file reference resolution** — tree-sitter alone gives best-effort. Pick one and live with limitations.
- 🟡 **Bootstrap cost on large repos** — prioritise recently-modified files; defer full indexing behind opt-in.
- 🟡 **Ignore pattern precedence** — respect `.gitignore` + `.swarm/kb-ignore`.
- 🟡 **KB schema versioning** — version every entry type from day one.
- 🟢 **Tree-sitter Go bindings** — `smacker/go-tree-sitter`, verify maintained and supports target grammars.

### Tests

- `TestKB_IndexesGoFile`
- `TestKB_IndexesTypeScriptFile`
- `TestKB_SymbolLookup`
- `TestKB_ReferencesAcrossFiles`
- `TestKB_MaintainerReindexOnChange`
- `TestKB_MaintainerSkipsUnchangedContent`
- `TestKB_IgnoresVendoredDirectories`
- `TestKB_CLIVerifyDetectsCorruption`

### Expected output

```
$ swarm kb status
Project: ~/dev/nexus-av-portal
Indexed:    312 / 312 (100%)
Symbols:    4,847
Languages:  TypeScript (218), Go (94)

$ swarm kb symbol JWTMiddleware
Found 2 matches:
  packages/api/src/middleware/jwt.ts:14-42  export function JWTMiddleware(...)
  packages/api/src/middleware/jwt.test.ts:8  import { JWTMiddleware }
```

## Milestone 3.8 — KB Derived Summaries (Weeks 8.5–9)

LLM-backed KB layer: file summaries, package summaries, project map, extracted conventions.

### Summary Types

```go
type FileSummary struct {
    Path           string
    ContentHash    string      // source file hash
    SymbolsHash    string      // hash of symbol set; drives invalidation
    Purpose        string
    PublicSurface  []string    // verifiable against symbol index
    DependsOn      []string
    RelatedTo      []string
    Notes          string
    GeneratedAt    time.Time
    GeneratedBy    string
}

type PackageSummary struct { /* ... */ }
type ProjectMap     struct { /* ... */ }
type ExtractedConvention struct {
    Topic          string
    Description    string
    Evidence       []CodeRef
    Confidence     Confidence
    ApprovedByUser bool
}
```

### Summariser Role

```yaml
role: summariser
model_binding: $models.fast
tools: [read_file, kb_file_symbols, kb_package_exports]
output_schema: FileSummary
max_iterations: 2
timeout_s: 30
```

Summariser gets file content + symbol index. Generates structured output verifiable against symbols (e.g., `PublicSurface` ⊆ exported symbols).

### Tools

- `kb_file_summary(path)`
- `kb_package_summary(path)`
- `kb_project_map()`
- `kb_conventions(topic?)`

### Maintenance Triggers

- Symbol set changed meaningfully → re-summarise file
- Package exports changed → re-summarise package
- Significant package re-summarisation → consider project map refresh
- User explicit: `swarm kb rebuild-summaries`

### Budget and Backpressure

- Daily summarisation cap (default $0.50)
- Bulk operations require explicit opt-in
- Priority queue: user-modified files first, vendored last
- Surfaced in `swarm kb status` with cost-so-far

### Convention Extraction

Separate `convention_extractor` role samples N call sites per topic, generates `ExtractedConvention` with confidence and evidence. **User approval required** before convention is `ApprovedByUser=true`.

### Exploration Flags

- 🔴 **Summary prompt design** — needs accurate, consistent, parseable, short. Build 30-file eval corpus with human reference summaries.
- 🔴 **Invalidation strategy** — content-hash too aggressive, symbol-set-hash imperfect. Test against real edit histories.
- 🔴 **Structured summary format vs prose** — both with structured hard-required and verified.
- 🔴 **Convention extraction reliability** — needs human-in-loop and minimum-evidence thresholds.
- 🔴 **Summary staleness representation** — every summary needs `generated_from_hash` and visible staleness indicator.
- 🟡 **Cascade invalidation control** — debounce and batch.
- 🟡 **Summary model binding** — benchmark on small models.
- 🟡 **Re-verification cadence** — periodic random-sample re-summarise for drift detection.

### Tests

- `TestSummariser_ProducesStructuredOutput`
- `TestSummariser_PublicSurfaceMatchesSymbols`
- `TestSummary_InvalidatedOnSymbolChange`
- `TestSummary_NotInvalidatedOnWhitespaceOnly`
- `TestSummary_StalenessSurfaced`
- `TestBudget_RespectsDailyCap`
- `TestConventionExtraction_RequiresApproval`
- `TestConventionExtraction_Evidence`
- Eval suite: 30-file corpus accuracy/completeness scoring.

### Expected output

```
$ swarm kb file-summary packages/api/src/middleware/jwt.ts
File: packages/api/src/middleware/jwt.ts
Hash: sha256:a3f21c...  (fresh, generated 2m ago)

Purpose:
  JWT bearer-token authentication middleware for the tRPC API.
  Verifies token signature, extracts claims, injects into request context.

Public surface:
  - JWTMiddleware(opts?: JWTOptions): MiddlewareFn
  - extractBearerToken(req: Request): string | null
  - JWTOptions (type)

Depends on:
  - jsonwebtoken (npm)
  - ../errors/codes (internal)
  - ../config (internal)
```

## Milestone 4 — Task Graph & Orchestrator (Weeks 9.5–11.5)

The brain. Build incrementally: static graph → dynamic expansion → replanning.

### Phase 4a: Static Graph Execution

```go
type Graph struct {
    RootGoal string
    Tasks    map[TaskID]*Task
    Edges    map[TaskID][]TaskID
    Version  int
    History  []Mutation
    mu       sync.RWMutex
}

func (g *Graph) Ready() []*Task
func (g *Graph) ApplyMutation(m Mutation) error
```

### Phase 4b: Scheduler

```go
type Scheduler struct {
    graph   *graph.Graph
    runtime *agent.Runtime
    events  events.Bus
    limits  Limits
}

func (s *Scheduler) Run(ctx context.Context) error
```

### Phase 4c: Orchestrator (LLM-driven planning)

```go
type Orchestrator struct {
    binding gateway.Binding
    graph   *graph.Graph
    store   context.Store
    gw      gateway.Gateway
}

func (o *Orchestrator) Plan(ctx context.Context, goal string) (*graph.Graph, error)
func (o *Orchestrator) OnTaskCompleted(ctx context.Context, task *graph.Task) error
func (o *Orchestrator) Replan(ctx context.Context, trigger ReplanTrigger) (graph.Mutation, error)
```

Planning prompt includes the KB project map and advertises `kb_*`, `query_knowledge`, `ctx_search`, `ctx_fetch` as subagent capabilities. Tighter task specs because the orchestrator knows project structure and self-serve information availability.

### Phase 4d: Critic Loop as Composite

```go
type CompositeCriticLoop struct {
    ExecutorRole  string
    CriticRole    string
    MaxIterations int
    Converged     func(criticResult context.Entry) bool
}
```

Scheduler treats as single node; spawns executor/critic pairs internally.

### Exploration Flags

- 🔴 **Planning prompt and schema** — single highest-leverage prompt. Schema shape, prompt structure, KB project map presentation, failure modes (over-decomposition, missed dependencies). Budget real time and benchmark.
- 🔴 **Progressive elaboration / task-completion classification** — wrong → runaway expansion or premature termination. Test against adversarial cases.
- 🔴 **Replan locality algorithm** — edge cases when invalidated task's output already consumed.
- 🔴 **Critic-loop convergence criterion** — needs structured output schema, not text matching.
- 🟡 **Deadlock detection** — easy detection (toposort), recovery less clear.
- 🟡 **Approval gate UX** — affects perceived responsiveness.
- 🟡 **Parallel budget tuning** — too low wastes time, too high rate-limits.
- 🟡 **Re-planning cost control** — hard cap with graceful escalation.
- 🟢 **Structured output fallback** — verify retry-with-error-feedback works for providers without native JSON mode.

### Tests

- `TestGraph_ReadySetComputation`
- `TestGraph_MutationVersioning`
- `TestScheduler_ParallelBudget`
- `TestScheduler_Deadlock`
- `TestOrchestrator_PlanProducesValidGraph`
- `TestOrchestrator_InvalidPlanRejected`
- `TestOrchestrator_ReplanLocality`
- `TestOrchestrator_IncludesProjectMapInPlanning`
- `TestCriticLoop_ConvergesOnApproval`
- `TestCriticLoop_MaxIterationsEnforced`
- Integration: full static pipeline vs golden fixture.

### Expected output

```
$ swarm run "Add JWT auth middleware to the Echo server in cmd/api"
[orchestrator] consulting project map and conventions...
[orchestrator] planning... (claude-opus-4-7, 2.1s)
[orchestrator] graph v1 with 6 tasks:
  ├─ t1 research     Audit existing auth setup
  ├─ t2 plan         Produce implementation plan [deps: t1]
  ├─ t3 codegen      Implement JWT middleware [deps: t2]
  ├─ t4 test_writer  Write middleware tests [deps: t3]
  ├─ t5 review       Review diff [deps: t3, t4]
  └─ t6 synthesize   Summarize for user [deps: t5]

[t1 research] running... (haiku-4-5)
[t1 research] tool_call: kb_package_summary(packages/api)
[t1 research] tool_call: query_knowledge("existing auth setup, what's in place?")
[t1 research] ✓ completed in 3.2s

[t3 codegen] running... (qwen2.5-coder:14b)
[t3 codegen] tool_call: kb_symbol_lookup("Middleware")
[t3 codegen] tool_call: kb_file_summary(cmd/api/server.go)
[t3 codegen] tool_call: read_file cmd/api/server.go
[t3 codegen] tool_call: edit_symbol path=cmd/api/server.go symbol=setupRoutes
[t3 codegen] ✓ completed in 8.7s

[t5 review] ⚠ partial: suggests refactoring token extraction
[orchestrator] replanning subtree... reason=review_feedback
[orchestrator] graph v2: inserted t3b codegen
...
```

## Milestone 5 — Session Manager & Conversational REPL (Week 12)

Wrap the orchestrator in a Claude Code-style interface. The full hybrid TUI (graph/focus/flow modes) lands in M12; this milestone delivers the conversational core.

### Session manager

```go
type Session struct {
    ID           string
    RootDir      string
    Thread       []Turn
    Store        context.Store
    Graph        *graph.Graph
    Orchestrator *orchestrator.Orchestrator
}

func (m *Manager) New(ctx context.Context, rootDir string) (*Session, error)
func (m *Manager) Resume(ctx context.Context, id string) (*Session, error)
func (m *Manager) Send(ctx context.Context, s *Session, input string) (<-chan UIEvent, error)
```

### TUI (basic chat mode)

Bubble Tea. Two panes: main conversation (streaming tokens), collapsible swarm panel (live task tree).

Slash commands:
- `/resume`, `/cancel`, `/approve`, `/cost`, `/help`
- `/profile use <name>`, `/profile show`
- `/model <role> <binding>`
- `/graph` (preview of M12), `/focus <task>`, `/flow`
- `/knowledge <question>`
- `/kb status`

### Live cost rendering

Status bar shows session cost, daily budget remaining, KB cost (separate bucket). Soft warnings at 50% and 80%; pause at 100% requiring user confirmation.

### Interrupt handling

User input during running task → `EventUserInterrupt` → scheduler pauses spawning → orchestrator decides (fold into current plan, or pause and dialogue).

### Exploration Flags

- 🔴 **Interrupt semantics** — cancel running, complete, or rollback? Different answers for different interjection types.
- 🟡 **UI event filtering rules** — too few opaque, too many noisy.
- 🟡 **Conversation persistence** — JSONL append-only is simple, SQLite for diff/resume.
- 🟡 **Resume mid-run correctness** — many edge cases; might disallow.
- 🟢 **Bubble Tea concurrency model** — confirm streaming pattern is comfortable.

### Tests

- `TestSession_ResumeRestoresState`
- `TestSession_Interrupt_PausesScheduling`
- `TestUIEventFiltering`
- TUI snapshot tests with teatest.

### Expected output

```
$ swarm
swarm 0.1.0 — profile: balanced
working directory: ~/dev/nexus-av-portal
session: sess_01HXYZ... (new)
kb: 312 files indexed, 4,847 symbols (fresh)

> add a /health endpoint that returns uptime and version
[orchestrator] planning...
[swarm] 3 tasks queued — type /graph to inspect

I've added the handler at internal/api/health.go:

  GET /health → {
    "status": "ok",
    "uptime_seconds": 12847,
    "version": "0.3.1",
    "commit": "a3f21c9"
  }

> /cost
this session: $0.08 (1,247 in / 892 out)
kb today:     $0.14 / $0.50 daily cap
daily budget: $2.00 / $5.00 remaining
```

## Milestone 6 — Tools, Robust Editing & Sandboxing (Weeks 13–14)

Tool layer with substantial focus on edit robustness against staleness, hallucination, approximate recall, ambiguity, and format drift.

### Tool registry

```go
type Tool interface {
    Name() string
    Schema() json.RawMessage
    Permissions() Permissions
    Invoke(ctx context.Context, args json.RawMessage) (Result, error)
}

type Permissions struct {
    Class            Class
    RequiresApproval bool
}

type Result struct {
    Status   ResultStatus
    Content  any
    Error    *ToolError
    NewRefs  []ContextRef
}

type ToolError struct {
    Code    string             // "stale_hash" | "symbol_not_found" | ...
    Message string
    Hint    string             // recovery instruction for the agent
    Details map[string]any
}
```

### Core Tool Set

Read/search/navigate (deterministic): `read_file`, `glob`, `grep`, `ctx_fetch`, `ctx_list`, `ctx_search`, `kb_*`, `query_knowledge`.

Mutating: `edit_file`, `edit_symbol`, `apply_patch`, `write_file`, `delete_file`.

Shell/test/vcs: `run_shell`, `run_tests`, `git_status`, `git_diff`.

Escape hatches: `ask_orchestrator`, `web_fetch`.

### The Edit Tool Family

Three tools, in order of preference:

#### 1. `edit_symbol` — structural, preferred

```go
edit_symbol(
    path          string,
    symbol        string,    // "calculateTax" or "User.String"
    expected_hash string,
    new_body      string,
    mode          string,    // "body" | "declaration" | "delete"
)
```

Uses the KB symbol index. Failure modes: symbol not found (returns available_symbols), ambiguous (requires disambiguation), stale hash (requires re-read).

#### 2. `apply_patch` — unified diff, general-purpose

```go
apply_patch(
    path          string,
    expected_hash string,
    diff          string,
    strategy      string,    // "strict" | "fuzzy" | "3way"
)
```

Applied via `git apply --3way` (or pure-Go equivalent). Handles moderate context drift.

#### 3. `edit_file` — string-replace, fallback

```go
edit_file(
    path          string,
    expected_hash string,
    old_str       string,
    new_str       string,
    line_hint     int,      // optional disambiguation
)
```

Matching cascade:
1. Exact match, unique → apply
2. Whitespace-tolerant match, unique → apply preserving original whitespace
3. Fuzzy (Levenshtein > 95%), unique → apply with detailed log
4. Exact match n>1 → require disambiguation
5. All fail → structured error with nearest candidates

### Staleness Protection

Every mutating tool takes `expected_hash`. Mismatch fails with structured error including current hash and "re-read" hint.

### Hallucination Protection (Read-Before-Edit)

Per-agent read-set checked on every mutation. Files not read → rejected. Symbol edits accept `kb_file_symbols` as read-equivalent.

### Rich Error Responses

Every failure returns structured error with `hint` field steering recovery:
- `symbol_not_found` → lists `available_symbols`
- `ambiguous_match` → lists matching locations with line numbers
- `patch_context_mismatch` → expected vs actual context lines
- `stale_hash` → tells agent to re-read

Codegen system prompt includes explicit recovery guidance.

### Proposed-Edits Model (Multi-Agent Coordination)

Agents produce `EditProposal` entries rather than mutating directly:

```go
type EditProposal struct {
    ID            string
    ProducedBy    string
    Path          string
    ExpectedHash  string
    Operations    []EditOp
    Rationale     string
    Dependencies  []string
}
```

Dedicated `applier` commits proposals in dependency order with retry on stale-hash. Symbol edits rebase cleanly; patches retry with 3-way; string edits may bounce back to producer.

### Sandboxing

Linux: `bwrap` with restricted bind-mount. macOS: `sandbox-exec` minimal profile. Network off by default for shell tools. `web_fetch` gets network; `run_shell` doesn't unless opt-in.

### Exploration Flags

- 🔴 **Edit tool taxonomy and agent guidance** — system prompt engineering for codegen role critical. Prototype on realistic edit corpus.
- 🔴 **Symbol-based edit tool design** — language-specific edge cases for `edit_symbol` modes. Methods vs free functions, overloads, generics, struct methods. Needs language-by-language spec.
- 🔴 **Proposed-edits model** — applier deterministic vs LLM? Dependency declaration mechanics? Rebase per op type? Review gate? Rollback semantics? Build minimum prototype on "two agents same file" scenario.
- 🔴 **Patch application library** — `git apply` subprocess vs pure-Go (`go-gitdiff`, `go-diff`). Evaluate on drift corpus.
- 🔴 **Fuzzy matching threshold** — 95% Levenshtein is starting guess. Consider tree-sitter AST similarity as higher-precision alternative.
- 🔴 **Cross-platform sandboxing** — bwrap, sandbox-exec, Windows. Possibly Docker/container fallback.
- 🔴 **Destructive operation approval model** — interactive prompts break automation; blanket approval unsafe.
- 🟡 **Error response size vs informativeness** — token cost tradeoff.
- 🟡 **Read-set scope and refresh policy** — expiry, surfacing staleness.
- 🟡 **LSP integration for edit precision** — worth complexity for some languages.
- 🟡 **Test runner language coverage** — Go/Python/Node first.
- 🟡 **Apply-patch in-process cache** — short-lived hash-keyed cache during edit bursts.
- 🟢 **`web_fetch` content extraction** — `go-readability` or similar.
- 🟢 **Tree-sitter language coverage** — reuse from M3.75.

### Tests

**Staleness/read protection:**
- `TestEdit_StaleHashRejected`
- `TestEdit_ReadBeforeEditEnforced`
- `TestEdit_UnreadFileCannotBeEdited`
- `TestEdit_SymbolEditAllowsKBLookupAsRead`

**Matching:**
- `TestEdit_ExactMatchUniqueApplies`
- `TestEdit_WhitespaceToleranceExactWhenPossible`
- `TestEdit_FuzzyMatchAboveThreshold`
- `TestEdit_FuzzyMatchBelowThresholdRejected`
- `TestEdit_AmbiguousMatchRequiresDisambiguation`
- `TestEdit_LineHintResolvesAmbiguity`

**Symbol editing:**
- `TestEditSymbol_BodyReplacement`
- `TestEditSymbol_DeclarationReplacement`
- `TestEditSymbol_SymbolNotFound_ReturnsAvailable`
- `TestEditSymbol_AmbiguousRequiresParent`
- `TestEditSymbol_CrossLanguage`

**Patch:**
- `TestApplyPatch_Clean`
- `TestApplyPatch_ContextDrift_3way`
- `TestApplyPatch_ConflictingHunks`

**Errors:**
- `TestEdit_StructuredErrorContainsRecoveryHint`
- `TestEdit_ErrorDetailsAreAgentActionable`

**Proposed-edits:**
- `TestProposal_AppliedInDependencyOrder`
- `TestProposal_RebaseOnStaleHash`
- `TestProposal_UnrebaseableReturnsToProducer`
- `TestProposal_TwoAgentsSameFile_SerialApply`

**Sandboxing:**
- `TestTool_ReadFile_PathEscapeRejected`
- `TestTool_WriteFile_RequiresApproval`
- `TestTool_RunShell_NetworkBlocked`
- `TestTool_RunShell_TimeoutEnforced`

**Fuzzing:**
- `FuzzApplyPatch`
- `FuzzEditFile`

### Expected output

```
$ swarm run "Refactor token validation in internal/auth to use the new ErrorCode system"

[t1 codegen] tool_call: kb_symbol_lookup("validateToken")
[t1 codegen] tool_call: kb_file_summary(internal/auth/tokens.go)
[t1 codegen] tool_call: read_file internal/auth/tokens.go (hash: 9f2c1a...)
[t1 codegen] tool_call: edit_symbol 
              path=internal/auth/tokens.go 
              symbol=validateToken 
              expected_hash=9f2c1a... 
              mode=body
[t1 codegen] ✓ edit applied, new hash: 7e3d88...

[t2 codegen] tool_call: edit_file 
              path=internal/auth/handlers.go 
              expected_hash=aa11bb... 
              old_str="return err"
[t2 codegen] ✗ tool_error: ambiguous_match
              hint: "old_str matches 4 locations. Provide line_hint or more context."
              details: matches: [line 42, 87, 134, 201]
[t2 codegen] retrying with line_hint=87...
[t2 codegen] ✓ edit applied
```

## Milestone 7 — Telemetry & Observability (Week 15)

- OpenTelemetry spans: session → orchestrator → agent → tool → model.
- Context assembly logs: inputs, sizes, summaries substituted, token count.
- Knowledge query traces: search queries, entries returned, citations.
- Edit logs: tool used, expected/actual hashes, match strategy, outcome.
- `swarm trace <session-id>` opens local viewer.
- Cost accounting: per session, per role binding, KB maintenance separate bucket.

### Exploration Flags

- 🟡 **Trace viewer choice** — local web UI vs TUI vs Jaeger export.
- 🟡 **Cost attribution** — real initiator vs direct caller (knowledge agent spawned by codegen).
- 🟡 **Edit-failure analytics** — patterns like "role X has high stale_hash rate" valuable for tuning.
- 🟢 **OTel exporter defaults** — confirm stdlib-only setup works locally.

### Tests

- `TestTelemetry_SpanHierarchy`
- `TestTelemetry_ContextAssemblyLog`
- `TestTelemetry_KnowledgeQueryTrace`
- `TestTelemetry_EditOutcomeRecorded`

## Milestone 8 — Project Memory & Config (Week 16)

- `.swarm/config.yaml`: profile, role binding overrides, tool allowlist, budget, KB ignore patterns.
- `.swarm/conventions.md`: ingested into KB conventions store.
- `.swarm/learnings.md`: appended via explicit orchestrator proposal + user approval.
- Project KB persists across sessions; M8 formalises lifecycle.

### Exploration Flags

- 🔴 **Cross-session learning promotion and decay** — automatic with approval, manual only? Stale-learning prevention?
- 🟡 **Learnings format** — structured (KB tags) vs freeform.
- 🟡 **Config hierarchy precedence** — global, project, CLI flags, env vars.
- 🟡 **KB portability across machines** — shared KB has value, major complexity.
- 🟢 **YAML vs TOML** — YAML's indentation footguns vs TOML.

### Tests

- `TestProjectConfig_OverridesGlobalBindings`
- `TestProjectConfig_ProfileSelectionPersisted`
- `TestLearnings_RequiresUserApproval`
- `TestConventions_IngestedIntoKB`

## Milestone 8.5 — LoRA Adapters (Power User Feature) (Week 17)

**Optional, opt-in feature for power users.** Most users will never touch this. Exists to support self-hosted vLLM/SGLang/Fireworks deployments where users want to run role-specialised fine-tuned adapters.

### Scope (v1)

- **In scope**: consume pre-trained LoRA adapters by reference; route requests to adapter-aware backends; per-role static binding (one adapter per role, no dynamic routing in v1).
- **Out of scope**: training adapters, adaptive routing, stacking, self-improving loops. All deferred to future versions or post-v1 work.

### Binding Extension

```go
type Binding struct {
    Provider string
    Model    string
    Endpoint string
    AuthRef  string
    LoRAs    []string  // adapter ids/paths; usually empty
}
```

`LoRAs` is a list (not single value) for forward-compatibility with stacking, but v1 enforces `len(LoRAs) <= 1` and emits a warning otherwise.

### Backend Support Matrix

| Backend | LoRA support | Notes |
|---|---|---|
| Anthropic | ✗ | No LoRA via API |
| OpenAI | ✗ | (fine-tuning is different mechanism) |
| OpenRouter | ✗ | Not applicable |
| Ollama | △ | Supported only if baked into custom Modelfile; not hot-swappable |
| LM Studio | ✗ | No hot-swap |
| vLLM (self-hosted) | ✓ | Native dynamic LoRA loading |
| SGLang (self-hosted) | ✓ | Native dynamic LoRA loading |
| Fireworks AI | ✓ | API-level adapter parameter |
| RunPod (vLLM) | ✓ | Same as vLLM |

If a user configures `LoRAs` for a backend that doesn't support it, gateway rejects with clear error.

### Adapter Resolution

Adapters identified by id/URI:
- `local:/path/to/adapter` — local filesystem path
- `hf:org/repo` — Hugging Face Hub reference
- `fireworks:adapter-id` — Fireworks-managed adapter

Gateway resolves at bind time; for vLLM/SGLang, may need to register adapter with the running server before first use.

### Configuration

In `.swarm/config.yaml`:

```yaml
bindings:
  codegen:
    provider: vllm
    endpoint: http://gpu-host:8000/v1
    model: kimi-k2.6
    loras:
      - local:/home/alec/adapters/codegen-go-v3
```

### CLI

```
swarm lora list                     # registered adapters
swarm lora register <name> <uri>    # add to local registry
swarm lora attach <role> <name>     # bind to a role
swarm lora detach <role>            # remove from role
```

### Exploration Flags

- 🔴 **vLLM/SGLang LoRA registration mechanics** — process for telling a running vLLM server about a new adapter varies. Validate the integration path on a representative deployment before designing the CLI.
- 🔴 **Adapter eval harness** — before production use, user needs to verify adapter is actually better than baseline. Provide simple A/B harness: same task, baseline vs adapter, side-by-side outputs for human review. Design before shipping.
- 🟡 **Adapter storage and registry format** — local registry as JSON file? Versioning scheme? Decide minimal viable shape.
- 🟡 **Backend incompatibility error messages** — clear "LoRAs not supported on Ollama" with explanation of viable backends.
- 🟡 **Cost attribution** — adapter inference often same price as base model on Fireworks; verify pricing model.
- 🟢 **Fireworks adapter parameter** — confirm API shape and integrate via OpenAI-compatible adapter with extension.

### Tests

- `TestBinding_LoRARejectedOnUnsupportedBackend`
- `TestBinding_LoRARoutedToFireworks`
- `TestBinding_LoRARoutedToVLLM`
- `TestBinding_MultipleLoRAsWarnsInV1`
- `TestLoRARegistry_PutGet`
- `TestLoRACLI_AttachDetach`

### Expected output

```
$ swarm lora register codegen-go-v3 local:/home/alec/adapters/codegen-go-v3
✓ Registered: codegen-go-v3 → local:/home/alec/adapters/codegen-go-v3

$ swarm lora attach codegen codegen-go-v3
✓ codegen role now uses adapter codegen-go-v3 on vllm/kimi-k2.6
  Note: adapter quality should be verified against baseline. 
  Run: swarm lora eval codegen --task-set ./eval-tasks.jsonl
```

## Milestone 12 — Hybrid TUI (Weeks 18–19)

The full multi-mode terminal UI. Earlier milestones use the basic chat from M5; M12 adds graph, focus, and flow modes.

### Modes

**Chat mode** (default, from M5) — Claude Code-style REPL with collapsible swarm panel.

**Graph mode** — full-screen interactive task DAG. Live updates as tasks spawn, progress, complete. Navigation:
- Arrow keys to move between tasks
- Enter to inspect task details (transitions to focus)
- Tab to cycle status filters (running / completed / failed)
- `q` returns to chat

**Focus mode** — single task expanded: full context (inputs, spec, logs, output, model calls, tool calls, cost). Useful for debugging why a specific task failed or to follow a critic loop.

**Flow mode** — streaming structured event view. Like a log tail but with semantic colouring and folding. Useful when a swarm is running 8 agents in parallel and you want to see what's happening overall.

Mode switching: `/graph`, `/focus <task>`, `/flow`, `/chat`. Or hotkeys `Ctrl+G`, `Ctrl+F`, `Ctrl+L`, `Ctrl+C` (subject to revision to avoid clashing with terminal conventions).

### Graph Layout

Sugiyama-style layered DAG, terminal-rendered using Unicode box-drawing characters.

Layout properties:
- **Left → Right** flow (root goal at left, terminal tasks at right)
- **Stable layout**: when a task is added, existing nodes don't reflow; new node slots into available space
- **Minimal crossings**: standard barycentre heuristic
- **Focus viewport**: scrollable when graph exceeds terminal size; selected node always visible

Rendering example (rough):
```
┌──────────┐     ┌──────────┐     ┌──────────┐
│ research │────▶│   plan   │────▶│ codegen  │──┐
│    ✓     │     │    ✓     │     │  ⏵ 8.7s  │  │
└──────────┘     └──────────┘     └──────────┘  │
                                                 ▼
                                           ┌──────────┐     ┌──────────┐
                                           │  review  │────▶│synthesize│
                                           │  ⏸      │     │    ⊙     │
                                           └──────────┘     └──────────┘
                                                ▲
                                           ┌──────────┐
                                           │test_writer│
                                           │  ⏵      │
                                           └──────────┘
```

States rendered: `⊙` pending, `⏵` running, `✓` succeeded, `✗` failed, `⏸` blocked, `⊘` superseded.

### Composition with Existing Architecture

Bubble Tea program is a single root model; modes are sub-models. `SwarmEvent` stream feeds all modes; each filters/projects events appropriately. The data layer doesn't change — modes are pure view layer over the same event bus and graph state.

### Exploration Flags

- 🔴 **Graph rendering implementation** — terminal Sugiyama-with-arrows is non-trivial. Evaluate libraries (`gocui`, custom on bubbletea/lipgloss). Build a static prototype before committing to live updates.
- 🔴 **Stable layout algorithm** — re-layout on every task addition is jarring. Need incremental layout that preserves positions where possible.
- 🔴 **Mode-switching keybindings** — collisions with terminal/shell conventions. User-testing.
- 🟡 **Focus mode information density** — too much info overwhelming, too little not useful. Iterate.
- 🟡 **Flow mode filtering** — which events show by default; user-configurable filters.
- 🟡 **Performance at large graph sizes** — 50+ tasks, render performance.
- 🟡 **Terminal capability variation** — Unicode box-drawing, colour, mouse support. Decide minimum requirement.
- 🟢 **`charmbracelet/lipgloss` layout primitives** — confirm they cover what's needed.

### Tests

- `TestGraphLayout_StableOnAdd` — add task, existing nodes don't move.
- `TestGraphLayout_MinimisesCrossings` — known-bad inputs, verify crossings ≤ baseline.
- `TestGraphLayout_FitsInViewport` — graph larger than terminal, scroll works.
- `TestModeSwitch_GraphToFocus` — Enter on a task transitions to focus correctly.
- `TestModeSwitch_PreservesState` — leaving and re-entering a mode preserves user's view state (selected task, scroll position).
- TUI snapshot tests for each mode in canonical states.

### Expected output

```
$ swarm
[mode: chat]
> add a /health endpoint
[orchestrator] planning... 4 tasks queued

> /graph
[mode: graph - 4 tasks, 1 running]

  ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
  │ research │───▶│   plan   │───▶│ codegen  │───▶│ review   │
  │    ✓     │    │    ✓     │    │ ⏵ 8.7s  │    │    ⊙    │
  └──────────┘    └──────────┘    └──────────┘    └──────────┘

  [↑↓←→] navigate  [Enter] focus  [Tab] filter  [q] back
  selected: codegen — running 8.7s, ollama/qwen2.5-coder:14b
```

## Cross-Cutting Testing Strategy

**Unit tests** — every package, table-driven. 70%+ coverage on `internal/graph`, `internal/context`, `internal/gateway`, `internal/knowledge`, `internal/kb`, `internal/tools`. Coverage less meaningful for orchestrator logic; integration tests matter more.

**Integration tests** — `test/integration/`, `-tags=integration`. Recorded gateway fixtures for determinism. `cmd/fixtures-record/` captures real responses.

**Golden file tests** — for event streams and final outputs of canonical workflows. Compare event log JSON against `testdata/golden/<scenario>.jsonl`. Update with `-update`.

**Property tests** — `gopter` for graph operations, edit invariants (apply + revert restores original).

**Live smoke tests** — optional, `-tags=live`, real providers, $0.50 cap. Nightly in CI.

**Knowledge Tier evals** — 30–50 questions vs seeded store, scored on citation accuracy and answer quality.

**KB summary evals** — 30-file corpus with human reference summaries, scored on accuracy/completeness/format.

**Edit robustness evals** — 50+ realistic edit scenarios including adversarial (duplicate code, trailing whitespace, mixed line endings, files changed since read). Main regression harness for M6.

**LoRA evals** (M8.5) — adapter-vs-baseline harness for human review.

**What not to test deeply** — LLM output quality. That's what golden fixtures and evals are for.

## Suggested Dependencies

| Purpose | Library |
|---|---|
| CLI | `github.com/spf13/cobra` |
| TUI | `github.com/charmbracelet/bubbletea` + `lipgloss` |
| TUI testing | `github.com/charmbracelet/x/exp/teatest` |
| Config | `gopkg.in/yaml.v3` |
| Logging | `go.uber.org/zap` |
| Tracing | `go.opentelemetry.io/otel` |
| SQLite (with FTS5) | `modernc.org/sqlite` |
| HTTP streaming | stdlib `net/http` + `bufio.Scanner` |
| JSON schema | `github.com/santhosh-tekuri/jsonschema/v6` |
| Property testing | `github.com/leanovate/gopter` |
| File watching | `github.com/fsnotify/fsnotify` |
| Tree-sitter | `github.com/smacker/go-tree-sitter` |
| Patch / diff | `github.com/bluekeyes/go-gitdiff` or `git apply` subprocess |

## Suggested Milestone Sequencing

| Weeks | Milestones |
|---|---|
| 1 | M0 |
| 2–3 | M1 |
| 3.5–4 | M1.5 (detection + profiles) |
| 5 | M2 |
| 6–7 | M3 |
| 7.5 | M3.5 |
| 8 | M3.75 |
| 8.5–9 | M3.8 |
| 9.5–11.5 | M4 |
| 12 | M5 (basic chat TUI) |
| 13–14 | M6 (edit robustness) |
| 15 | M7 |
| 16 | M8 |
| 17 | M8.5 (LoRA, optional) |
| 18–19 | M12 (hybrid TUI) |

~19 weeks to feature-complete v1. Resist skipping ahead — each milestone surfaces design issues that compound if deferred.

## What to Defer (Explicitly)

In v1 plan, **not built**:

- **Semantic/vector search** in the Knowledge Tier (BM25 sufficient for v1)
- **LSP integration** in the KB (tree-sitter sufficient for v1)
- **Shared KB across team members / machines** (local-only for v1)
- **LoRA training** (consume only, M8.5)
- **Adaptive LoRA routing** (static binding only in M8.5)
- **LoRA stacking** (single adapter only in M8.5)
- **Self-improving loop** (no automated improvement in v1)
- **Distributed execution across machines**
- **Web UI** (TUI only)
- **Fine-grained per-tool cost modelling** (session-level sufficient)
- **Multi-user collaboration / shared sessions**
- **Plugin system for custom agents** beyond YAML manifests
- **Automatic cross-session learning promotion** (manual only)

## Future Directions (Post-v1)

Listed for context, not in v1 scope:

- **M9: Adaptive LoRA Routing** — orchestrator picks adapter per-task based on content. Requires good adapter eval signals and a routing policy. Build only if M8.5 sees meaningful adoption.
- **M10: LoRA Stacking** — composing multiple adapters per request. Quality is hit-or-miss; defer until adapter ecosystem matures and there's a concrete use case.
- **M11: Self-Improving Loop** — pipeline that uses session corrections and human feedback to train new adapters automatically. Significant infrastructure (training, dataset management, eval automation) and consent/privacy considerations. v2-or-later territory.
- **Project-level KB sharing** — team-wide shared KB across machines. Major work in concurrency, trust, and conflict resolution.
- **Semantic search** — vector embeddings over context store, hybrid retrieval. Add if BM25 measurably underperforms on real queries.
- **LSP integration** — for languages where symbol precision in the KB is the bottleneck.

## Consolidated Exploration Priorities

🔴 flags ordered by milestone — spike before coding the relevant milestone:

**Before M1:**
- 🔴 Tool call schema normalisation across providers

**Before M1.5:**
- 🔴 Profile design and naming (validate with users)
- 🔴 Role hint vocabulary
- 🔴 Local catalog curation and maintenance

**Before M2:**
- 🔴 Tokeniser choice for code + docs

**Before M3:**
- 🔴 Output schema enforcement strategy
- 🔴 Summariser-on-demand trigger design

**Before M3.5:**
- 🔴 Knowledge Agent system prompt
- 🔴 Hallucination guardrails beyond citations-required
- 🔴 Cache invalidation correctness model

**Before M3.75:**
- 🔴 Symbol indexing: tree-sitter vs LSP
- 🔴 Cross-file reference resolution strategy

**Before M3.8:**
- 🔴 Summary prompt design and format
- 🔴 Invalidation strategy
- 🔴 Convention extraction reliability
- 🔴 Summary staleness representation

**Before M4:**
- 🔴 Planning prompt and schema
- 🔴 Progressive elaboration / task-completion classification
- 🔴 Replan locality algorithm
- 🔴 Critic-loop convergence criterion

**Before M5:**
- 🔴 Interrupt semantics

**Before M6:**
- 🔴 Edit tool taxonomy and agent guidance
- 🔴 Symbol-based edit tool design (per-language)
- 🔴 Proposed-edits model
- 🔴 Patch application library choice
- 🔴 Fuzzy matching threshold tuning
- 🔴 Cross-platform sandboxing
- 🔴 Destructive operation approval model

**Before M8:**
- 🔴 Cross-session learning promotion and decay

**Before M8.5 (if pursued):**
- 🔴 vLLM/SGLang LoRA registration mechanics
- 🔴 Adapter eval harness

**Before M12:**
- 🔴 Graph rendering implementation
- 🔴 Stable layout algorithm
- 🔴 Mode-switching keybindings

🟡 and 🟢 flags are validation-as-you-go, not blocking.

## Assumptions Made (Worth Confirming)

A few defaults I picked while consolidating that you should sanity-check:

1. **LoRA `Binding` field is `LoRAs []string`** (single field, list-typed for forward compat with stacking) rather than separate `LoRA` and `LoRAStack`. v1 enforces length ≤ 1 with warning otherwise.
2. **M8.5 ships before M12.** LoRA support is a smaller, more isolated piece; M12 is the bigger UI investment. Reordering is fine if you'd rather defer LoRA to after M12.
3. **Adapter resolution scheme** uses URI prefixes (`local:`, `hf:`, `fireworks:`) since you weren't sure how this should work. This is changeable later — just need a consistent identifier shape.
4. **Profile names**: `local-only`, `balanced`, `quality`, `budget`. These are placeholder names; could be changed during M1.5 user-testing.
5. **Mode hotkeys** in M12 (`Ctrl+G/F/L/C`) are likely to clash with existing terminal conventions. Treat as starting point for the keybinding design.
6. **All v1 milestones produce a runnable system.** If the timeline runs long and v1 ships at M8 (no LoRA, no fancy TUI), the tool is still useful — those final milestones are *enhancements*, not prerequisites.

---

A reasonable way to validate the early design before committing weeks: build M0–M2 first, then M1.5, and confirm that a fresh user can install, get auto-detected, and run a single agent end-to-end. If that experience feels right, the rest follows; if it feels clunky, fix the foundation before going deeper.
