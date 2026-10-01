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
- **Reading-first conversation** — an anchored reading position that survives resizes and streaming output, application-owned drag and keyboard selection, copying, current-conversation find with match navigation, and a four-tab inspector (changes, agents, context, overview). See [docs/tui-interactions.md](docs/tui-interactions.md).
- **Transcript views (preview)** — the legacy transcript remains the default. Choose **Notebook (preview)** from the action palette (`F2` or `/actions`), or set `tui.transcript_view = "notebook"` under `[tui]` for the configured default. A per-session palette choice overrides that setting until **Use configured transcript view** clears it. View changes wait until active text selection is cleared. Phase 1 groups only newly attributed live activity; older history has no persisted ownership metadata, so it remains in chronological fallback blocks. Rich resource summaries and semantic sections are planned for later phases.
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

### Transcript view preference

Marshal starts in the legacy transcript. To make Notebook (preview) the
configured default, add this to the user or project config:

```toml
[tui]
transcript_view = "notebook"
```

The setting controls new sessions and sessions without a temporary choice.
During a session, open the action palette with `F2` or `/actions` and choose
**Use notebook transcript** or **Use legacy transcript**. That choice is a
session override and takes precedence over configuration until you choose
**Use configured transcript view**. If text is selected, switching waits for
the selection to be cleared so the selection stays attached to its source.

Notebook (preview) groups activity when current runtime ownership metadata
connects narration, tools, and results. Phase 1 does not persist that ownership
metadata, so restored and older history stays visible in chronological
fallback blocks instead of being retroactively grouped. Rich resource
summaries and semantic sections are future phases.

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
