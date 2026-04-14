# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run
go run cmd/marshal/main.go

# Build
go build ./...

# Test
go test ./...

# Test a single package
go test ./internal/backend/...

# Format (non-negotiable — gofmt is enforced)
gofmt -w .
```

## Environment variables

Marshal reads these from the shell — they must be exported before running:

```bash
export FIREWORKS_API_KEY="..."
```

`marshal.toml` uses `${VAR}` interpolation via `os.Expand` — missing vars log a warning and substitute as empty strings, which causes `Validate()` to fail.

## Architecture

Marshal is an **agent-centric** coding assistant orchestrator. The user converses with the **Marshal** agent (the orchestrator), which plans work and delegates to specialized **executor** agents (write code) and **critic** agents (review code). Changes are applied to a real git repo on an isolation branch, and merged only on a PASS verdict.

**Layer overview** (bottom to top):

| Layer | Package | Status |
|---|---|---|
| Backend interface + OpenAI-compat client | `internal/backend/` | ✅ Done (M1) |
| Config loader | `internal/config/` | ✅ Done (M1) |
| Agent layer (executor, critic, compactor, planner) | `internal/agents/` | ✅ Done (M2) |
| Marshal orchestrator (conversation, planning, delegation) | `internal/marshal/` | ✅ Done |
| Conversation system (intent, context, state) | `internal/conversation/` | ✅ Done |
| Git layer (branch, diff, commit, revert) | `internal/git/` | ✅ Done (M3) |
| Session store (SQLite) | `internal/store/` | ✅ Done (M4) |
| Loop engine (backward-compat wrapper) | `internal/loop/` | ✅ Done (M2) |
| TUI (chat view, loop view, session browser) | `internal/tui/` | ✅ Done (M5-6) |
| Context compaction + think-block panel | `internal/agents/compactor/` | ✅ Done (M7-8) |
| Multi-agent pipeline (planner, DAG scheduler, parallel execution) | `internal/pipeline/` | ✅ Done (M9) |
| Multi-backend abstraction (per-role provider selection) | `internal/backend/` | ✅ Done (M10) |
| Integration critic + parallel pipeline | `internal/pipeline/` | ✅ Done (M11) |
| Agent tool use (provider-agnostic capabilities) | `internal/tools/` | ✅ Done (M12) |
| Extended provider support (Claude, Gemini, Bedrock) | `internal/backend/` | Done (M13) |

### Marshal Agent (Orchestrator)

The **Marshal** agent is what the user converses with. It:
- Understands user intent through natural language conversation
- Plans work by breaking requests into tasks
- Spawns **executor** agents to write code
- Spawns **critic** agents to review code
- Summarizes results and maintains conversational context

Config section: `[marshal]` in `marshal.toml`

### Executor Agent

The **executor** agent writes code. It receives:
- A specific task from Marshal
- System prompts (base + security + skills)
- Git context (diffs, file contents)

It generates or modifies code and applies changes to the working tree.

Config section: `[executor]` in `marshal.toml`

### Critic Agent

The **critic** agent reviews code. It receives:
- The original task description
- The executor's changes (as a git diff)
- System prompts for review criteria

It returns a structured JSON verdict: `{"verdict": "PASS"|"FAIL", "summary": "...", "issue": "...", "fix": "...", "concerns": []}`

Config section: `[critic]` in `marshal.toml`

### Backend interface

`internal/backend/backend.go` defines the core interface that all model providers implement:

```go
type Backend interface {
    Complete(ctx context.Context, model string, messages []Message) (Response, error)
    Name() string
}
```

`OpenAICompatibleBackend` implements this against any OpenAI-compatible endpoint (RunPod, Ollama via `/v1/chat/completions`, Anthropic via compatibility layer, vLLM). Future backends add by implementing the interface.

### Multi-Backend Abstraction (M10)

The multi-backend system allows each agent role (Marshal, Executor, Critic, Planner) to use a different model provider. This enables cost optimization and local/offload hybrid setups:

```toml
[marshal]
provider = "ollama"  # Local orchestration
model = "qwen3:4b"
base_url = "http://localhost:11434"

[executor]
provider = "fireworks"  # Cloud API for coding
model = "accounts/fireworks/models/devstral-small-2-24b-instruct-2512"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"

[critic]
provider = "ollama"  # Local reasoning
model = "deepseek-r1:7b"
base_url = "http://localhost:11434"
```

**Provider values:** `ollama`, `openai`, `fireworks`, `together`. Unknown providers fall back to OpenAI-compatible mode.

**Backend Factory:** `internal/backend/factory.go` creates the appropriate backend based on the `provider` field:
- `ollama` → `OllamaBackend` using native `/api/chat` endpoint
- `fireworks`, `openai`, `together` → `OpenAICompatibleBackend` with provider-specific defaults
- Missing/unknown → `OpenAICompatibleBackend` (backward compatible)

### Enhanced Ollama Integration (M10+)

Marshal provides enhanced Ollama integration for local development:

**Native API Features:**
- `ListModels()` - Query `/api/tags` to discover available local models
- `PullModel()` - Download models with progress tracking via `/api/pull`
- `CompleteStreaming()` - Stream responses for real-time UX
- Context window control via `context_window` config (sets `num_ctx`)

**TUI Commands:**
- `:models` - List available Ollama models with size, parameter count, quantization
- `:pull <model>` - Download a model from Ollama Hub (e.g., `:pull qwen2.5-coder:7b`)

**Configuration:**
```toml
[executor]
provider = "ollama"
model = "qwen2.5-coder:7b"
base_url = "http://localhost:11434"
context_window = 32768  # Large context for tool use (Ollama num_ctx)
temperature = 0.2
max_tokens = 4096
```

**Key Ollama Options:**
- `num_ctx` (context_window): Controls context window size. Critical for tool use - larger values allow more tool definitions and conversation history.
- `num_predict` (max_tokens): Maximum tokens to generate
- `temperature`: Sampling temperature

**Implementation:** `internal/backend/ollama.go` (backend), `internal/backend/ollama_models.go` (model management), `internal/tui/app.go` (command handlers)

### Configuration

`marshal.toml` is the single config file. Loaded by `config.Load(path)` which expands env vars, then validated by `cfg.Validate()`. Config struct mirrors the TOML sections: `[marshal]`, `[executor]`, `[critic]`, `[planner]`, `[loop]`, `[session]`, `[retry]`.

### Conversation Flow

1. User sends a message to Marshal
2. Marshal interprets intent and plans (may ask clarifying questions)
3. When code changes are needed:
   a. Marshal spawns an executor with a specific task
   b. Executor generates/modifies code on an isolation branch
   c. Marshal spawns a critic to review the diff
   d. If FAIL, feedback is injected; executor retries (up to `max_rounds`)
   e. If PASS, branch is merged and changes committed
4. Marshal summarizes results to the user
5. Conversation continues

### Critic verdict schema

```json
{"verdict": "PASS"|"FAIL", "summary": "...", "issue": "...", "fix": "...", "concerns": []}
```

### Skills system (three-tier resolution)

Skills are TOML files that add to (never override) executor/critic system prompts. Resolution order:
1. `.marshal/skills/` — project-local, committed to repo
2. `~/.config/marshal/skills/` — user-global
3. Built-in skills inside the binary

Skills use `system_prompt_additions` only — any other prompt key is a validation error. Security standing instructions are always-on and cannot be overridden by skills.

## Development Milestones

The project follows a milestone order. Do not implement a later milestone before the earlier one is verified working:

### Phase 1: Foundation (Complete)

| # | Milestone | Status | Description |
|---|---|---|---|
| 1 | Backend interface + Config | ✅ Done | `OpenAICompatibleBackend`, TOML config loader with env var expansion |
| 2 | Agent layer | ✅ Done | Executor, Critic, Compactor agents with structured prompts and JSON verdicts |
| 3 | Git layer | ✅ Done | Branch isolation, diff extraction, commit, merge, revert |
| 4 | Session store + CLI | ✅ Done | SQLite persistence, `marshal run`, `marshal sessions`, headless `--no-tui --json` |
| 5-6 | TUI | ✅ Done | Bubble Tea interface: chat view, loop view, sidebar, composer, diff viewer |
| 7-8 | Compaction + Think-blocks | ✅ Done | Real context compaction (not pass-through), R1 think-block extraction and display |

### Phase 2: Agent-Centric Model (Complete)

| # | Milestone | Status | Description |
|---|---|---|---|
| — | Marshal orchestrator | ✅ Done | Agent-centric architecture: user converses with Marshal, which spawns executor/critic |
| — | Conversation system | ✅ Done | Intent classification, context accumulation, state machine, clarifying questions |
| — | Planner agent | ✅ Done | Task decomposition into dependency graphs with validation |

### Phase 3: Pipeline & Tooling (Complete)

| # | Milestone | Status | Description |
|---|---|---|---|
| 9 | Sequential Pipeline | ✅ Done | `marshal pipeline` command, topological sort execution, task dependency chains |
| 10 | Multi-Backend Abstraction | ✅ Done | Per-role backend provider selection; Ollama native adapter with streaming |
| 11 | Integration Critic + Parallel Execution | ✅ Done | Cross-task coherence review; DAG scheduler, goroutine fan-out, file conflict detection |
| 12 | Agent Tool Use | ✅ Done | Provider-agnostic tool system: file operations, command execution, search |

### Phase 4: Extended Providers (Complete)

| # | Milestone | Status | Description |
|---|---|---|---|
| 13 | Extended Providers | Done | Native Anthropic Claude, Google Gemini, AWS Bedrock backends |

### Agent Tool Use (M12)

Executor agents can autonomously explore the codebase, apply changes, and verify results before the critic reviews the final diff.

**Available tools:**

| Tool | Purpose | Key args |
| --- | --- | --- |
| `read_file` | Read file contents | `path` |
| `write_file` | Write or overwrite a file | `path`, `content` |
| `edit_file` | Replace a line range | `path`, `start_line`, `end_line`, `new_content` |
| `run_command` | Run build/test commands | `args` |
| `search_code` | Regex search across the repo | `pattern`, `glob` |
| `list_directory` | List directory contents | `path` |

**Enabling tools** (opt-in per agent role):

```toml
[executor]
enable_tools = true
max_tool_calls = 20  # per round, default 20
```

**Allowed commands** for `run_command`: `go`, `make`, `npm`, `npx`, `python`, `python3`, `pytest`, `cargo`, `rg`. Shell execution (`sh`, `bash`) is never permitted.

**Safety:** All file operations are sandboxed to the repository root (absolute and traversal paths are rejected). Changes still go through the critic review loop; branch isolation remains the safety net.

**Implementation:** `internal/tools/` (types, registry, tool implementations), `internal/backend/backend.go` (`ToolCapableBackend` interface), `internal/agents/executor/executor.go` (`ExecuteWithTools` agentic loop).

**Graceful degradation:** If `enable_tools = false`, the backend doesn't implement `ToolCapableBackend`, or the model returns no tool calls, execution falls back to the standard non-tool path automatically.

### Extended Provider Support (M13)

Native implementations for non-OpenAI-compatible providers:

| Provider | Rationale | Status |
| --- | --- | --- |
| Anthropic Claude | Native message API with tool use, prompt caching | Done |
| Google Gemini | Vertex AI and Gemini API support | Done |
| AWS Bedrock | Enterprise deployment option | Done |
| Azure OpenAI | Enterprise Microsoft environment | Future |

Each implements the `Backend` interface with provider-specific optimizations.

**Configuration Examples:**

```toml
# Anthropic Claude
[executor]
provider = "claude"
model = "claude-3-5-sonnet-20241022"
api_key = "${ANTHROPIC_API_KEY}"
temperature = 0.2
max_tokens = 4096

# Google Gemini
[executor]
provider = "gemini"
model = "gemini-1.5-flash-latest"
api_key = "${GEMINI_API_KEY}"
temperature = 0.2
max_tokens = 4096

# AWS Bedrock
[executor]
provider = "bedrock"
model = "anthropic.claude-3-5-sonnet-20241022-v2:0"
# No API key needed - uses AWS credentials from environment
# base_url can specify region: base_url = "us-east-1"
temperature = 0.2
max_tokens = 4096
```
