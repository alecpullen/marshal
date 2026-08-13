# Architecture

Marshal is a terminal-native coding agent built in Go. It wires together a Bubble Tea TUI, an agent loop, a provider-flexible LLM layer, safe native tools, repository intelligence, and SQLite persistence.

## High-level flow

1. `cmd/marshal/main.go` calls `internal/app.Run()`.
2. `app.Run()` loads merged config, builds `session.State`, and starts the TUI program.
3. The TUI (`internal/app/tui/`) collects input and dispatches it to the agent runtime.
4. The agent (`internal/agent/`) runs a loop: build prompt → call LLM → parse tool calls → execute tools → render results.
5. Tools (`internal/tools/`) operate on the working tree under policy and approval.
6. State and history are persisted in SQLite (`internal/db/`).

## SDD plan workflow

Feature exploration may use the normal brainstorming workflow. An approved
design can become a Marshal executable plan through `/sdd new <goal>` or
`/sdd new --from-last-plan`. Marshal writes the candidate under the effective
`sdd.plans_dir`, inspects it before execution, and requires review before
`/sdd` starts. Executable blocks use adaptive execution by default; prose-only
plans retain the legacy agent strategy.

## Key layers

| Layer | Packages | Responsibility |
|-------|----------|----------------|
| Entrypoint | `cmd/marshal/` | Thin main that delegates to `internal/app`. |
| App shell | `internal/app/`, `internal/app/session/` | Dependency wiring, config, logging, signal handling, shared session state. |
| TUI | `internal/app/tui/` | Bubble Tea model, panels, settings, chrome, themes, help footer. |
| Agent | `internal/agent/`, `internal/agent/swarm/` | Single-agent loop and multi-agent swarm orchestration. |
| Pipeline | `internal/pipeline/` | Plan execution with implementer/reviewer subagents. |
| LLM | `internal/llm/`, `internal/llm/routing/` | Provider abstraction, model presets, role-based routing. |
| Tools | `internal/tools/registry/`, `internal/tools/native/`, `internal/tools/patch/`, `internal/tools/policy/`, `internal/tools/mcp/` | Tool registration, file/search/shell/git tools, patch apply, risk policy, MCP clients. |
| Repo intelligence | `internal/repo/`, `internal/index/`, `internal/retrieval/`, `internal/lsp/` | Repo scanning, tree-sitter symbol extraction, embeddings, semantic retrieval, LSP integration. |
| Knowledge | `internal/knowledge/`, `internal/contextpack/`, `internal/rollover/` | Durable project memory, context budget, long-session rollover. |
| Persistence | `internal/db/`, `internal/snapshot/`, `internal/filetrack/` | SQLite schema, workspace snapshots, file access tracking. |
| ACP | `internal/acp/` | Headless JSON-RPC server for editor/IDE integration. |
| Web bridge | `web/bridge/`, `cmd/webbridge/` | Fleet control plane: supervises one `marshal acp` child per project, brokers approvals, serves the REST API and SSE streams, and drives per-agent worktree isolation, diff, merge and discard entirely over ACP. |
| Web UI | `web/ui/` | Svelte 5 + Vite + TypeScript SPA: fleet dashboard, spawn composer, and per-agent chat (dev-only Node toolchain; build output is embedded by `web/bridge`). |

## Data flow of a single turn

```
User input
    ↓
TUI → session.State
    ↓
Agent loop builds context pack and prompt
    ↓
LLM layer routes to configured provider/model
    ↓
Streaming response parsed for text and tool calls
    ↓
Tool calls classified, approved, and executed
    ↓
Results streamed back to TUI transcript
    ↓
State persisted to SQLite
```

## Web UI dependency rules

- `web/bridge/` and `cmd/webbridge/` are pure Go and embed only the compiled
  assets from `web/bridge/static/` via `//go:embed`. The Go module graph does
  not depend on Node.
- `web/ui/` is a standalone Node project used only at development/build time.
  Running `npm run build` in `web/ui` writes hashed static assets into
  `web/bridge/static/`; the next `go build ./cmd/webbridge` embeds them.
- During pure-Go development, `web/bridge/static/` contains a placeholder
  `index.html` so `webbridge` still serves a useful landing page before the
  SPA is built.

## Design principles

- **Local-friendly** — defaults assume no built-in providers; remote providers are opt-in.
- **Provider-flexible** — the LLM layer is swappable without TUI changes.
- **Tool-safe** — shell execution is risk-classified and approval-gated.
- **Transparent** — status bar and transcript show active model, route, context usage, and tool progress.
