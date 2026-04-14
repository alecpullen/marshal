# Marshal

An AI-powered coding agent orchestrator. Marshal acts as your **coding assistant** — you converse with it naturally, and it handles the work by spawning specialized **executor** agents (write code) and **critic** agents (review code), automatically refining solutions until they pass review.

## How It Works

1. **You chat with Marshal** — describe what you want in natural language
2. **Marshal plans and delegates** — it breaks down your request and spawns specialized agents:
   - **Executor** generates or modifies code
   - **Critic** reviews the changes and returns a structured verdict (PASS/FAIL)
3. **The loop runs automatically** — if FAIL, feedback is injected and the executor retries; if PASS, changes are committed
4. **You get the result** — Marshal summarizes what was done and you continue the conversation

## Quick Start

### Prerequisites

- Go 1.22+
- Git repository initialized
- (Optional) Ollama for local models, or
- (Optional) Fireworks account for cloud models

### Setup

```bash
# Clone and enter the repository
cd marshal

# Install Go dependencies
go mod tidy
```

### Choose Your Backend

**Option A: Ollama (Local, Free, ~8.5GB RAM)**

```bash
# Install Ollama
brew install ollama

# Run setup script to pull models and create config
./scripts/setup-ollama.sh
```

**Option B: Fireworks (Cloud, Higher Quality)**

```bash
# Get API key from fireworks.ai
export FIREWORKS_API_KEY="your_key_here"

# Optional: add to your shell profile
echo 'export FIREWORKS_API_KEY="your_key_here"' >> ~/.zshrc
```

## Usage

### Run with Ollama (Local)

```bash
export FIREWORKS_API_KEY="ollama"
go run cmd/marshal/main.go -config ollama.toml
```

Or use the helper script:

```bash
./scripts/run-m2-ollama.sh "Write a function to reverse a string"
```

### Run with Fireworks (Cloud)

```bash
go run cmd/marshal/main.go
```

## Configuration

### ollama.toml (Local)

```toml
[marshal]
# Orchestrator model — the agent you converse with
model       = "qwen3:4b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.3
max_tokens  = 4096

[executor]
# Code generation model
model       = "qwen2.5-coder:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.2
max_tokens  = 4096

[critic]
# Code review model with reasoning
model       = "deepseek-r1:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.6
max_tokens  = 8192

[loop]
max_rounds        = 3
auto_commit       = false
auto_revert       = true
branch_isolation  = true
compact_after     = 2
```

### marshal.toml (Cloud)

```toml
[marshal]
# Orchestrator model — plans work and manages conversation
model       = "accounts/fireworks/models/deepseek-v3p1"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.3
max_tokens  = 4096

[executor]
# Code generation model
model       = "accounts/fireworks/models/devstral-small-2-24b-instruct-2512"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.2
max_tokens  = 4096

[critic]
# Code review model with reasoning
model       = "accounts/fireworks/models/deepseek-r1-0528"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.6
max_tokens  = 8192
json_output = true
```

## Project Structure

```
.
├── cmd/marshal/          # CLI entry point
├── internal/
│   ├── marshal/          # Orchestrator — your conversational agent
│   ├── agents/           # Agent implementations
│   │   ├── executor/     # Code writing agent
│   │   ├── critic/       # Code review agent
│   │   └── compactor/    # Context summarization agent
│   ├── backend/          # LLM backend interface (OpenAI-compatible)
│   ├── config/           # TOML configuration loader
│   ├── git/              # Git operations (branch, commit, diff)
│   ├── loop/             # Core feedback loop engine
│   ├── store/            # SQLite session storage
│   └── tui/              # Bubble Tea terminal UI
├── scripts/              # Setup and test scripts
└── docs/                 # Design docs and specifications
```

## Development

```bash
# Build
go build ./...

# Run tests
go test ./...

# Format code
go fmt ./...

# Run with custom config
go run cmd/marshal/main.go -config my-config.toml
```

## Troubleshooting

**Ollama connection refused:**
```bash
ollama serve  # Run in another terminal
```

**Model not found:**
```bash
ollama pull qwen3:4b
ollama pull qwen2.5-coder:7b
ollama pull deepseek-r1:7b
```

**Fireworks 401 Unauthorized:**
```bash
export FIREWORKS_API_KEY="your_key"
```

## Roadmap

### Completed

| Milestone | Description |
|-----------|-------------|
| **M1** | Backend interface + config loader |
| **M2** | Agent layer (executor, critic, compactor) + skills system |
| **M3** | Git layer (branch isolation, diff, commit, revert) |
| **M4** | Session store + CLI (`marshal run`, `marshal sessions`) |
| **M5-6** | Bubble Tea TUI (chat view, loop view, session browser) |
| **M7-8** | Real compaction + think-block panel |
| **—** | Marshal orchestrator + conversation system |
| **—** | Planner agent for task decomposition |

### Planned

| Milestone | Description |
|-----------|-------------|
| **M9** | Sequential pipeline (`marshal pipeline` command) |
| **M10** | Integration critic for cross-task review |
| **M11** | Parallel execution with DAG scheduler |
| **M12** | **Agent tool use** — provider-agnostic file operations, commands, search |
| **M13+** | Extended provider support (Claude, Gemini, Bedrock) |

## Documentation

- `CLAUDE.md` — Architecture and design decisions
- `docs/ARCHITECTURE.md` — Full architecture specification
- `.claude/plans/` — Implementation plans by milestone

## License

[Add your license here]
