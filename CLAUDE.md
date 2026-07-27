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

The current codebase is **Milestones A-Q complete** (skeleton, TUI shell, config, provider abstraction, tool registry, read/search/shell tools, approval system, patch tool, git integration, SQLite project/session DB, repo scanner, tree-sitter symbol index, repo map, context packs, role-based model routing, knowledge agent, swarm runtime with specialist roles, MCP/plugin ecosystem, sandboxed command execution with restricted/container/passthrough backends, and ACP v1 conversation lifecycle). See [docs/04-tooling-and-shell-safety.md](docs/04-tooling-and-shell-safety.md) for sandbox details and [docs/10-acp.md](docs/10-acp.md) for the ACP support matrix.

This tree is complete for `internal/`. Check here before building
something — several subsystems that sound like they need writing already
exist (the docked-panel host, the side rail, the provider connect flow).

```
cmd/marshal/main.go                   — thin entrypoint, delegates to internal/app

Runtime and orchestration
internal/agent/                       — agent runtime (single-agent loop)
internal/agent/swarm/                 — swarm orchestration, lock, state, verdict
internal/agent/agenttest/             — shared test stubs for the agent package
internal/sdd/                         — spec-driven development: DAG, gates, worktrees, verify, rescue
internal/worker/                      — lifecycle contract for supervised background workers
internal/pubsub/                      — in-process typed event broker
internal/acp/                         — ACP v1 headless transport (initialize, session lifecycle, prompt/cancel, permissions)

App shell
internal/app/app.go                   — Run(), dependency wiring, signal handling
internal/app/onboarding.go            — first-run provider setup (separate pre-TUI tea.Program)
internal/app/config/                  — TOML config loading, defaults, merge rules
internal/app/logging/                 — slog logger construction
internal/app/session/                 — in-memory app state, message list, shutdown context
internal/trust/                       — folder-trust store, resolver, project-config hashing

TUI
internal/app/tui/                     — Bubble Tea model (View/Update/Init); model.go is the hub
internal/app/tui/dock/                — hosts a single interactive panel above the input area
internal/app/tui/sidepanel/           — widescreen side rail: read-only sections, fit/collapse algorithm
internal/app/tui/settings/            — /settings browser: field list, pane stack, config frames
internal/app/tui/connect/             — provider connect flow (template → base URL → key → probe → model)
internal/app/tui/agents/              — /agents roster panel with per-role attribution
internal/app/tui/memory/              — /memory panel
internal/app/tui/castlist/            — pre-flight cast list shown before /sdd and /swarm
internal/app/tui/picker/              — centered modal selection list
internal/app/tui/chrome/              — shared panel dressing: gutter-framed panels with titles
internal/app/tui/theme/               — semantic color slots with NO_COLOR/16/256 detection
internal/app/tui/huhtheme/            — huh.Theme retuned to the marshal palette
internal/app/tui/help/                — persistent keybinding footer and ? help overlay
internal/app/tui/probe/               — provider model-list probing as tea.Cmds
internal/app/tui/changedfiles/        — working-tree diff against a base ref
internal/app/tui/gitinfo/             — current branch and linked-worktree name
internal/app/tui/fuzzy/               — shared filter-as-you-type matcher

Commands and knowledge
internal/commands/                    — slash commands (/plan, /test, /profile, …)
internal/history/                     — generation listing, transcript dump, archived-turn search
internal/contextpack/                 — context pack builder and budget logic
internal/rollover/                    — context-window rollover for long sessions
internal/knowledge/                   — durable project memory agent
internal/skills/                      — skill-based instruction sets
internal/export/                      — session transcript → self-contained HTML
internal/redact/                      — masks secret-bearing values for safe export
internal/diffview/                    — unified diffs as styled, syntax-highlighted output

Models
internal/llm/                         — provider abstraction
internal/llm/routing/                 — route resolver, model presets, role profiles
internal/llm/provider/templates.go    — built-in provider templates (ollama, openai, groq, …)
internal/llm/catalog/                 — curated table of well-known models
internal/llm/schema/                  — tool/response schema types
internal/llm/streaming/               — streaming response handling
internal/llm/embedding/               — local-first text embedding
internal/llm/pricing/                 — per-model token pricing table

Repo intelligence
internal/repo/                        — repo scanner, file hashing, gitignore, repo map/card
internal/repo/symbols.go              — tree-sitter Go symbol extraction
internal/index/                       — chunking, embedding indexer, file watcher
internal/retrieval/                   — semantic retrieval over embeddings
internal/lsp/                         — LSP client/manager with symbol, query, diagnostics adapters
internal/diagnostics/                 — configurable per-language checkers (go vet, …)

Persistence
internal/db/                          — SQLite project/session persistence
internal/db/symbols.go                — symbol DB schema and queries
internal/db/migrations.go             — schema; all CREATE TABLE statements live here
internal/snapshot/                    — git-backed workspace snapshots for rollback
internal/filetrack/                   — per-session file read/write timestamps

Tools and safety
internal/tools/registry/              — tool registration and dispatch
internal/tools/native/                — native tools: file, search, shell, git, repo, symbols
internal/tools/native/symbols_find.go — symbols.find tool implementation
internal/tools/patch/                 — patch apply and approval
internal/tools/policy/                — command approval and risk policy
internal/tools/mcp/                   — MCP client, protocol, manager
internal/tools/desktop/               — browser/desktop automation tools
internal/permissions/                 — tool permission rules, approval-pattern derivation
internal/hooks/                       — user-defined hook runner (PreToolUse, …)
internal/plugins/                     — third-party plugin loading (MCP servers, hooks)
internal/sandbox/                     — restricted, container, and passthrough execution backends

Utilities
internal/strutil/                     — shared string helpers (truncation, token formatting)
internal/jsonextract/                 — pulls the first JSON value out of model output
```

### Data flow

`app.Run()` loads config → creates `session.State` → wires `tui.Model` and the agent loop → hands off to `tea.NewProgram`. The `session.State` is the shared mutable state between the TUI and agent layers.

### Config loading

Config is merged in order (later wins):
1. Built-in defaults (`config.Default()`)
2. `~/.config/marshal/config.toml`
3. `.marshal/config.toml` (project-local)

### Dependency injection seams

`app.Run()` accepts 12 functional options (`app.go:105-210`) so tests can inject fakes without spinning up a real TUI. The most-used are `WithConfigLoader`, `WithProgramRunner`, and `WithNow`; also available are `WithTrustResolver`, `WithWorkingDir`, `WithSkipOnboarding`, `WithOnboardingProgramRunner`, `WithKnowledgeHook`, `WithWorker`, `WithSessionID`, `WithExistingSession`, and `WithAdditionalDirectories`. Tests in `app_test.go` use this pattern exclusively.

### Specs and plans

Design specs live in `docs/superpowers/specs/`, implementation plans in `docs/superpowers/plans/`, both named `YYYY-MM-DD-<topic>.md`. Check for an existing spec before designing something new — several approved-but-unimplemented designs currently sit there. `docs/02-system-architecture.md` describes the full intended architecture, which runs ahead of what is built.

## Design constraints

- **Local-first**: default config has `remote_providers_allowed = false`. Don't assume a hosted model.
- **Provider-flexible**: the model layer is swappable without TUI changes.
- **Tool-safe**: shell execution is classified and requires user approval before running.
- The TUI is responsible for rendering only — no routing, policy, or prompt logic should live there.
