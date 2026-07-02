# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**Marshal** is a local-first TUI coding agent for developers who want control over inference, context, tooling, and repository knowledge. Built in Go with Bubble Tea. The binary is named `marshal`.

## Commands

```bash
# Build (requires CGO_ENABLED=1 and a C toolchain — needed for the
# tree-sitter dependency used by Go symbol extraction)
go build ./cmd/marshal

# Run
go run ./cmd/marshal

# Run all tests
go test ./...

# Run a single package's tests
go test ./internal/app/...
go test ./internal/app/config/...

# Format
gofmt -w .

# Vet
go vet ./...
```

## Architecture

The current codebase is **Milestones A-M complete** (skeleton, TUI shell, config, provider abstraction, tool registry, read/search/shell tools, approval system, patch tool, git integration, SQLite project/session DB, repo scanner, tree-sitter symbol index, repo map, context packs, and role-based model routing). Milestones N onward (knowledge agent, swarm runtime, MCP/plugin support) are not yet implemented.

```
cmd/marshal/main.go                   — thin entrypoint, delegates to internal/app
internal/agent/                       — agent runtime and swarm orchestration
internal/app/app.go                   — Run(), dependency wiring, signal handling
internal/app/config/                  — TOML config loading, defaults, merge rules
internal/app/logging/                 — slog logger construction
internal/app/session/                 — in-memory app state, message list, shutdown context
internal/app/tui/                     — Bubble Tea model (View/Update/Init)
internal/contextpack/                 — context pack builder and budget logic
internal/db/                          — SQLite project/session persistence
internal/db/symbols.go                — symbol DB schema and queries
internal/llm/                         — provider abstraction, schema, streaming
internal/repo/                        — repo scanner, file hashing, gitignore, repo map/card
internal/repo/symbols.go              — tree-sitter Go symbol extraction
internal/tools/native/                — native tools: file, search, shell, git, repo, symbols
internal/tools/native/symbols_find.go — symbols.find tool implementation
internal/tools/patch/                 — patch apply and approval
internal/tools/policy/                — command approval and risk policy
internal/tools/registry/              — tool registration and dispatch
```

### Data flow

`app.Run()` loads config → creates `session.State` → wires `tui.Model` and the agent loop → hands off to `tea.NewProgram`. The `session.State` is the shared mutable state between the TUI and agent layers.

### Config loading

Config is merged in order (later wins):
1. Built-in defaults (`config.Default()`)
2. `~/.config/marshal/config.toml`
3. `.marshal/config.toml` (project-local)

### Dependency injection seams

`app.Run()` accepts functional options (`WithConfigLoader`, `WithProgramRunner`, `WithNow`) so tests can inject fakes without spinning up a real TUI. Tests in `app_test.go` use this pattern exclusively.

### Planned package layout

See `docs/02-system-architecture.md` for the full intended layout. Implemented packages are listed above. Remaining future packages include `internal/sandbox/` (isolated command execution) and plugin/MCP support.

## Design constraints

- **Local-first**: default config has `remote_providers_allowed = false`. Don't assume a hosted model.
- **Provider-flexible**: the model layer is swappable without TUI changes.
- **Tool-safe**: shell execution will require classification and user approval before running.
- The TUI is responsible for rendering only — no routing, policy, or prompt logic should live there.
