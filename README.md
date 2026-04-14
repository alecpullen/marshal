# Marshal

**AI coding assistant with multi-model orchestration and git-native workflows.**

Marshal combines the conversational fluidity of Aider/Claude Code with a disciplined, branch-isolated execution model. Every user turn is a discrete, critic-reviewed task — but the UX feels like a natural pair-programming session.

[![Go Version](https://img.shields.io/badge/go-1.23+-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

## Features

- **🤖 Four-Role Architecture**: Marshal (gate), Executor, Critic, and Compactor models work together
- **🌿 Git-Native Workflow**: Three-tier branch hierarchy (target → staging → task) with automatic isolation
- **🔧 Multiple Edit Modes**: Search/replace, unified diff, whole-file, or tool-use (for capable models)
- **💬 Conversational TUI**: Natural chat interface with streaming responses
- **🤝 Tool Use**: `read_file`, `write_file`, `run_command` for models that support function calling
- **⚡ Headless/CI Mode**: NDJSON output for automated workflows
- **🛡️ Security**: Path allowlisting, command allowlisting, sandboxed execution
- **📊 Repo Map**: Tree-sitter based symbol extraction with intelligent ranking

## Installation

### Binary Releases

Download pre-built binaries from [GitHub Releases](https://github.com/alec/marshal/releases):

```bash
# Linux/macOS
curl -sSL https://github.com/alec/marshal/releases/latest/download/marshal_$(uname -s)_$(uname -m).tar.gz | tar xz
sudo mv marshal /usr/local/bin/

# Or use go install
go install github.com/alec/marshal/cmd/marshal@latest
```

### From Source

```bash
git clone https://github.com/alec/marshal.git
cd marshal
go build -o bin/marshal ./cmd/marshal
```

## Quick Start

1. **Create a configuration file** (`marshal.toml`):

```toml
[model.executor]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/routers/kimi-k2p5-turbo"
supports_tools = true

[model.critic]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/models/qwen2p5-14b-instruct"

[model.marshal]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/models/qwen2p5-7b-instruct"

[loop]
max_rounds = 3

[git]
enabled = true
```

2. **Start an interactive session**:

```bash
cd your-repo
marshal chat
```

3. **Or run a single task**:

```bash
marshal run "add error handling to the login function"
```

## Usage

### Interactive Mode (`marshal chat`)

The TUI provides a conversational interface:

```
Marshal » add a README with installation instructions
[Executor streams response...]
✓ PASS  README.md created with comprehensive installation guide

Marshal » /ship
shipped to main (a1b2c3d)
```

**Key slash commands**:
- `/add <file>` - Add file to context
- `/diff` - Show current changes
- `/ship` - Merge staging to target branch
- `/undo` - Revert last task
- `/help` - Show all commands

### One-Shot Mode (`marshal run`)

For automation and scripting:

```bash
# Basic usage
marshal run "fix the typo in README.md"

# With flags
marshal run --json --exit "run the test suite" | jq '.verdict'

# From a file
marshal run -f task-description.txt
```

**Exit codes**:
- `0` - Task passed and merged
- `1` - Task failed after all rounds
- `2` - Configuration error
- `3` - Git error
- `4` - Pipeline integration failure

### CI/CD Integration

See [docs/ci/github-actions.md](docs/ci/github-actions.md) for GitHub Actions examples.

## Configuration

Marshal uses TOML configuration with environment variable expansion:

```toml
# ~/.config/marshal/config.toml or ./marshal.toml

[model.executor]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/routers/kimi-k2p5-turbo"
supports_tools = true
edit_format = "search_replace"

[model.critic]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/models/qwen2p5-14b-instruct"

[loop]
max_rounds = 3
compact_after = 2

[git]
enabled = true
```

**Supported providers**:
- Fireworks AI
- OpenAI
- Anthropic (via OpenAI-compatible proxy)
- Ollama
- LM Studio
- Any OpenAI-compatible endpoint

See [docs/config/reference.md](docs/config/reference.md) for complete options.

## Model Selection

Marshal's four roles can use different models:

| Role | Purpose | Recommended |
|------|---------|-------------|
| **Executor** | Code generation | Claude Sonnet, GPT-4o, Kimi K2.5 |
| **Critic** | Code review | Qwen2.5-14B, Claude Haiku |
| **Marshal** | Task classification | Qwen2.5-7B, smaller local models |
| **Compactor** | History summarization | Same as Marshal |

See [docs/models/roster.md](docs/models/roster.md) for detailed recommendations.

## Architecture

```
User Input
    ↓
[Marshal Model] → classify (proceed/chat/clarify)
    ↓ (if proceed)
[Executor] → generate code (with tool-use or edit formats)
    ↓
[Critic] → review and output verdict (PASS/FAIL)
    ↓ (if FAIL, retry up to max_rounds)
[Git] → commit on task branch
    ↓ (if PASS)
[Git] → squash-merge to staging
```

## Documentation

- [Configuration Reference](docs/config/reference.md) - All configuration options
- [Model Roster](docs/models/roster.md) - Supported models and capabilities
- [Skill Authoring](docs/skills/authoring.md) - Creating custom skills
- [TUI Keybindings](docs/tui/keybindings.md) - Keyboard shortcuts
- [CI/CD Examples](docs/ci/github-actions.md) - GitHub Actions integration

## Benchmarks

Marshal achieves competitive performance on coding benchmarks:

| Benchmark | Marshal | Aider | Claude Code |
|-----------|---------|-------|-------------|
| Exercism Go (easy) | TBD | TBD | TBD |
| Exercism Go (medium) | TBD | TBD | TBD |

See [benchmark/README.md](benchmark/README.md) for methodology.

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Acknowledgments

Marshal is inspired by:
- [Aider](https://github.com/paul-gauthier/aider) - The original pair-programming assistant
- [Claude Code](https://claude.ai/code) - Anthropic's agentic coding tool

The repo map implementation is derived from Aider's tree-sitter based symbol extraction.
