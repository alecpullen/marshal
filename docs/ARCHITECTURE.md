# Architecture

Marshal is a terminal-native coding agent built in Go. It wires together a Bubble Tea TUI, an agent loop, a provider-flexible LLM layer, safe native tools, repository intelligence, and SQLite persistence.

## High-level flow

1. `cmd/marshal/main.go` calls `internal/app.Run()`.
2. `app.Run()` loads merged config, builds `session.State`, and starts the TUI program.
3. The TUI (`internal/app/tui/`) collects input and dispatches it to the agent runtime.
4. The agent (`internal/agent/`) runs a loop: build prompt → call LLM → parse tool calls → execute tools → render results.
5. Tools (`internal/tools/`) operate on the working tree under policy and approval.
6. State and history are persisted in SQLite (`internal/db/`).

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

## Design principles

- **Local-friendly** — defaults assume no built-in providers; remote providers are opt-in.
- **Provider-flexible** — the LLM layer is swappable without TUI changes.
- **Tool-safe** — shell execution is risk-classified and approval-gated.
- **Transparent** — status bar and transcript show active model, route, context usage, and tool progress.
