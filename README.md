# Marshal

An AI-powered coding agent that writes code through an iterative feedback loop. Marshal pairs an **executor** (writes code) with a **critic** (reviews code), automatically refining solutions until they pass review.

## How It Works

1. **Executor** generates or modifies code based on your task
2. **Critic** reviews the changes and returns a structured verdict (PASS/FAIL)
3. If FAIL, feedback is injected and the loop repeats
4. If PASS, changes are committed and the session completes

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
[executor]
model       = "qwen2.5-coder:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.2
max_tokens  = 4096

[critic]
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
[executor]
model       = "accounts/fireworks/models/devstral-small-2-24b-instruct-2512"
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
```

## Project Structure

```
.
├── cmd/marshal/          # CLI entry point
├── internal/
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
ollama pull qwen2.5-coder:7b
ollama pull deepseek-r1:7b
```

**Fireworks 401 Unauthorized:**
```bash
export FIREWORKS_API_KEY="your_key"
```

## Documentation

- `CLAUDE.md` — Architecture and design decisions
- `docs/MARSHAL_DESIGN_SPEC.md` — Full design specification
- `docs/MARSHAL_USER_STORIES.md` — User stories and use cases
- `.claude/plans/` — Implementation plans by milestone

## License

[Add your license here]
