# Changelog

All notable changes to Marshal are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.2-alpha] - 2026-08-27

### Fixed

**Interface**
- Side-rail sections now share a row layout and align to one right edge,
  eliminating ragged column boundaries between sections.
- Working-set stats skip empty file paths instead of rendering blank rows.

## [0.0.1-alpha] - 2026-08-26

First public alpha. Marshal is a terminal-native coding agent with
local-friendly defaults: it reads and edits your repository, runs shell
commands and tests, and carries project context across sessions, against any
OpenAI-compatible endpoint.

This is alpha software. Interfaces, config keys, and on-disk formats may change
without a migration path before 0.1.0.

### Added

**Agent runtime**
- Single-agent loop with streaming responses, tool dispatch, and cancellation.
- Swarm runtime for multi-agent orchestration with specialist roles, shared
  locking, and verdict aggregation.
- Plan execution pipeline with implementer, reviewer, and branch-reviewer
  subagents, a build-and-test gate, and controller-owned commits.
- Supervised background workers over an in-process typed event broker.

**Models and routing**
- Provider abstraction over any OpenAI-compatible endpoint, with built-in
  templates for Ollama, LM Studio, vLLM, OpenRouter, Groq, OpenAI, and others.
- Role-based routing so cheap models handle search and strong models handle
  patches, switchable at `/profile`.
- Curated model catalog, per-model token pricing, and local-friendly embeddings.
- Guided provider connect flow: template, base URL, key, probe, model.

**Repository intelligence**
- Repo scanner with gitignore handling, file hashing, and a generated repo map.
- Tree-sitter symbol extraction for Go, TypeScript/JavaScript, Python, and Rust.
- Chunking, embedding indexer, and file watcher backing semantic retrieval.
- LSP client with symbol, query, and diagnostics adapters.
- Configurable per-language diagnostics checkers.

**Context management**
- Context pack builder with explicit token budgets, inspectable via `/context`.
- Automatic context-window rollover for long sessions.
- Knowledge agent providing durable project memory across session boundaries.
- Skill system for loadable, task-specific instruction sets.

**Tools and safety**
- Native tools: file read/write, search, shell, git, repo, and symbol lookup.
- Shell commands are risk-classified and approval-gated before execution.
- Sandboxed execution with restricted, container, and passthrough backends.
- Patch tool with diff preview and explicit apply approval.
- Folder-trust store so untrusted checkouts prompt before running anything.
- User-defined hooks (`PreToolUse`, and friends) and third-party plugin loading.
- MCP client and server manager, namespaced and permissioned.

**Interface**
- Bubble Tea TUI with a docked panel host, widescreen side rail, and a
  persistent keybinding footer.
- Slash commands including `/plan`, `/review`, `/test`, `/memory`, `/profile`,
  `/settings`, `/agents`, and `/context`.
- Semantic theme slots with `NO_COLOR`, 16-colour, and 256-colour detection.
- Syntax-highlighted unified diffs.
- Session transcript export to self-contained HTML, with secret redaction.
- `marshal --version` reports version, commit, build date, and toolchain.

**Headless and persistence**
- ACP v1 headless mode over stdio JSON-RPC 2.0 (`marshal acp`) for editor and
  IDE integration, covering session lifecycle, prompt/cancel, and permissions.
- SQLite persistence for projects, sessions, messages, and the symbol index.
- Git-backed workspace snapshots for rollback.
- `marshal history` for generation listing, transcript dump, and archived-turn
  search.

### Known limitations

- Alpha quality: expect rough edges, and expect breaking changes before 0.1.0.
- No built-in providers ship by default; you must configure a provider before
  first use.
- Prebuilt binaries are published for Linux and macOS only (amd64 and arm64).
  Windows is not yet supported.
- Building from source requires `CGO_ENABLED=1` and a C toolchain, because of
  the tree-sitter dependency.

[Unreleased]: https://github.com/alecpullen/marshal/compare/v0.0.2-alpha...HEAD
[0.0.2-alpha]: https://github.com/alecpullen/marshal/compare/v0.0.1-alpha...v0.0.2-alpha
[0.0.1-alpha]: https://github.com/alecpullen/marshal/releases/tag/v0.0.1-alpha
