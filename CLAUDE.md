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

The current codebase is **Milestones A-B complete** (skeleton + TUI shell). Milestones C onward (provider, tools, agent loop, SQLite, repo indexing, swarm) are not yet implemented.

```
cmd/marshal/main.go          — thin entrypoint, delegates to internal/app
internal/app/app.go          — Run(), dependency wiring, signal handling
internal/app/config/         — TOML config loading, defaults, merge rules
internal/app/logging/        — slog logger construction
internal/app/session/        — in-memory app state, message list, shutdown context
internal/app/tui/            — Bubble Tea model (View/Update/Init)
```

### Data flow

`app.Run()` loads config → creates `session.State` → wires `tui.Model` → hands off to `tea.NewProgram`. The `session.State` is the shared mutable state between the TUI and future agent layers.

### Config loading

Config is merged in order (later wins):
1. Built-in defaults (`config.Default()`)
2. `~/.config/marshal/config.toml`
3. `.marshal/config.toml` (project-local)

### Dependency injection seams

`app.Run()` accepts functional options (`WithConfigLoader`, `WithProgramRunner`, `WithNow`) so tests can inject fakes without spinning up a real TUI. Tests in `app_test.go` use this pattern exclusively.

### Planned package layout (not yet implemented)

See `docs/02-system-architecture.md` for the full intended layout. Key future packages: `internal/llm/`, `internal/agent/`, `internal/tools/`, `internal/repo/`, `internal/db/`, `internal/sandbox/`, `internal/patch/`.

## Design constraints

- **Local-first**: default config has `remote_providers_allowed = false`. Don't assume a hosted model.
- **Provider-flexible**: the model layer (once built) must be swappable without TUI changes.
- **Tool-safe**: shell execution will require classification and user approval before running.
- The TUI is responsible for rendering only — no routing, policy, or prompt logic should live there.
