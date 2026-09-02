# Marshal

A terminal coding agent with local-friendly defaults and provider choice.

> **Status: alpha (v0.0.3-alpha).** Usable day to day, but interfaces, config
> keys, and on-disk formats may change without a migration path before 0.1.0.

Marshal is a terminal-native coding agent that understands your repository,
edits files, runs shell commands and tests, and keeps project context across
sessions — with local models as first-class citizens and the freedom to use
any OpenAI-compatible endpoint.

## Install

### Prebuilt binary (Linux, macOS)

Grab the archive for your platform from the
[latest release](https://github.com/alecpullen/marshal/releases):

```bash
tar -xzf marshal_*_<os>_<arch>.tar.gz
sudo mv marshal /usr/local/bin/
marshal --version
```

### From source

Requires Go 1.26+ and a C toolchain (tree-sitter needs cgo):

```bash
git clone https://github.com/alecpullen/marshal.git
cd marshal
CGO_ENABLED=1 go build ./cmd/marshal
./marshal
```

To use your dev build everywhere, symlink it onto your PATH — the link
always resolves to the latest rebuild:

```bash
ln -sfn "$PWD/marshal" ~/.local/bin/marshal
```

On first launch Marshal creates `~/.config/marshal/config.toml` and walks you
through the initial provider setup.

## Configure

Config is merged in order (later wins):

1. Built-in defaults
2. `~/.config/marshal/config.toml`
3. `.marshal/config.toml` (per-project)

Point Marshal at Ollama, LM Studio, vLLM, OpenRouter, or any OpenAI-compatible
endpoint by defining a provider and a model preset, then choose a default profile:

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
internal/acp/                  — ACP v1 headless JSON-RPC server
internal/agent/                — single-agent + swarm runtimes
internal/app/                  — dependency wiring, config, session
internal/app/tui/              — Bubble Tea TUI
internal/commands/             — slash commands (/plan, /test, …)
internal/contextpack/          — context budget + pack builder
internal/db/                   — SQLite project/session persistence
internal/knowledge/            — durable project memory
internal/llm/                  — provider abstraction, routing
internal/pipeline/             — plan execution pipeline
internal/plugins/              — skill/MCP/plugin loading and verification
internal/repo/                 — repo scanner, symbol index, repo map
internal/sandbox/              — isolated command execution backends
internal/skills/               — skill-based instruction sets
internal/tools/native/         — read, search, shell, git, …
internal/tools/patch/          — diff apply + approval
internal/tools/policy/         — shell command risk classification
internal/tools/registry/       — tool registration + dispatch
internal/tools/mcp/            — MCP client and server management
internal/worktree/             — git worktree helpers
```

## Key features

- **Provider-flexible** — Ollama, OpenRouter, LM Studio, vLLM, or any OpenAI-compatible endpoint; swap at `/profile`.
- **Role-based routing** — use small models for search, strong models for patches.
- **Repository intelligence** — tree-sitter symbol index, repo map, and file summaries.
- **Context management** — pack builder with token budgets; inspect usage at `/context`.
- **Safe, sandboxed tools** — shell commands classified, approval-gated, and run isolated by default.
- **Git integration** — automatically checkpoint the working tree before tooling.
- **Persistent sessions** — project state, messages, and memory stored in SQLite.
- **Knowledge agent** — durable project knowledge survives session boundaries.
- **Skill system** — loadable skill-based instruction sets for specialised workflows, with an autoloaded entry point so the agent reaches for them unprompted.
- **Slash commands** — `/plan`, `/review`, `/test`, `/memory`, `/profile`, etc.
- **Swarm runtime** — multi-agent orchestration with specialist roles.
- **MCP/plugin ecosystem** — connect external tools via MCP protocol, namespaced and permissioned.
- **ACP v1 headless mode** — `marshal acp` over stdio JSON-RPC 2.0 for editor/IDE integration.

## Design principles

- **Local-friendly** — works great offline with Ollama, LM Studio, or llama.cpp,
  without blocking remote providers when you need them.
- **Provider-flexible** — swap providers or model presets without rewriting
  workflows.
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

## Usability testing (synthetic users)

```bash
go build ./cmd/marshal
go test ./test/usability/... -run TestScripted -v
```

## Project status

Marshal is in **alpha**. Core functionality — agent loop, repository
intelligence, tool safety, multi-agent swarm, MCP/plugin ecosystem, sandboxed
execution, and ACP headless mode — is implemented and usable day to day.

Alpha means: expect rough edges, and expect breaking changes to config keys and
on-disk formats before 0.1.0. Prebuilt binaries cover Linux and macOS only;
Windows is not yet supported. See [CHANGELOG.md](CHANGELOG.md) for what landed.

## Releasing

Marshal uses [GoReleaser](https://goreleaser.com/) with the
`goreleaser-cross` image to build Linux and macOS binaries with CGO enabled.

To cut a release:

1. Move the `Unreleased` section of [CHANGELOG.md](CHANGELOG.md) under the new
   version heading and commit it.
2. Tag and push:

```bash
git tag -a v0.0.3-alpha -m "Release v0.0.3-alpha"
git push origin v0.0.3-alpha
```

The `release` GitHub Actions workflow will run tests, build the binaries,
create a draft release, and attach the archives and checksums file. Review
the draft in the GitHub web UI, then publish it.

Release notes are curated in `CHANGELOG.md`; GoReleaser's generated changelog
is disabled deliberately, since the commit log is too granular to be useful.

## License

MIT
