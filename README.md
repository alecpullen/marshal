# Marshal

Local-first terminal coding agent.

Use your own models — Ollama, OpenRouter, or any OpenAI-compatible endpoint.
Swap providers without rewriting workflows. Keep repository knowledge local.

## Quick start

```bash
go build ./cmd/marshal         # requires CGO_ENABLED=1 (tree-sitter)
./marshal
```

On first launch Marshal creates `~/.config/marshal/config.toml` and walks you
through the initial provider setup.

## Configure

Config is merged in order (later wins):

1. Built-in defaults
2. `~/.config/marshal/config.toml`
3. `.marshal/config.toml` (per-project)

Define a provider and a model preset, then choose a default profile:

```toml
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"

[models.presets.coder]
provider = "ollama"
model = "qwen2.5-coder:14b"
context_window = 32768
temperature = 0.0

[profile]
default = "coder"
```

## Architecture

```
cmd/marshal/main.go            — thin entrypoint
internal/agent/                — single-agent + swarm runtimes
internal/app/                  — dependency wiring, config, session
internal/app/tui/              — Bubble Tea TUI
internal/commands/             — slash commands (/plan, /test, …)
internal/contextpack/          — context budget + pack builder
internal/db/                   — SQLite project/session persistence
internal/knowledge/            — durable project memory
internal/llm/                  — provider abstraction, routing
internal/repo/                 — repo scanner, symbol index, repo map
internal/skills/               — skill-based instruction sets
internal/tools/native/         — read, search, shell, git, …
internal/tools/patch/          — diff apply + approval
internal/tools/policy/         — shell command risk classification
internal/tools/registry/       — tool registration + dispatch
internal/tools/mcp/            — MCP plugin support
```

## Key features

- **Provider-flexible** — OpenAI-compatible transport; swap models at `/profile`.
- **Role-based routing** — use small models for search, strong models for patches.
- **Repository intelligence** — tree-sitter symbol index, repo map, file summaries.
- **Context management** — pack builder with token budgets; view at `/context`.
- **Safe tools** — shell commands inspected, classified, and approval-gated.
- **Git integration** — automatically checkpoint the working tree before tooling.
- **Persistent sessions** — project state, messages, and memory stored in SQLite.
- **Knowledge agent** — durable project knowledge survives session boundaries.
- **Slash commands** — `/plan`, `/review`, `/test`, `/memory`, `/profile`, etc.
- **Swarm runtime** — multi-agent orchestration with specialist roles (under active development).

## Design commitments

- **Local-first** — default config prohibits remote providers; the tool works
  fully offline with Ollama/llama.cpp/LM Studio.
- **Tool-safe** — every shell execution is classified, presented with a risk
  label, and requires user approval.
- **Transparent** — the status bar and transcript show active model, route,
  context usage, and tool progress at all times.

## Requirements

- Go 1.26+
- C toolchain (for tree-sitter: `gcc`, `clang`, or Xcode CLT on macOS)

## Commands

```bash
go build ./cmd/marshal         # build binary
go run ./cmd/marshal           # run
go test ./...                  # all tests
gofmt -w .                     # format
go vet ./...                   # vet
```

## Roadmap

| Milestone | Status |
|-----------|--------|
| A–M: skeleton, TUI, config, tools, approval, git, DB, repo scan, symbols, context packs, role routing | Complete |
| N: durable knowledge agent | Implemented |
| O: swarm runtime & specialist roles | In progress |
| P: MCP/plugin system | Planned |
| Q: sandboxed command execution | Planned |

## License

MIT

## Project status

Marshal is under active development. Core functionality is usable; swarm and
plugin systems are in progress. Expect breaking changes as the APIs stabilise.
