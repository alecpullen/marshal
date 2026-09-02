# Changelog

All notable changes to Marshal are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

**Config and project identity**
- `[providers]` and `[models.presets]` are user-global only. They live in
  `~/.config/marshal/config.toml`; every editing surface (connect,
  /settings, /options, /models, the config.providers.\* and
  config.models.preset.\* tools) saves them there, and project saves never
  emit them. On load, a trusted project config that still carries these
  sections is hoisted into the user config and stripped; an entry that
  conflicts with an existing user-global one is kept project-local with a
  deprecation warning.
- Marshal anchors `.marshal/`, the session database, project config, and
  trust records at the git repository root instead of the launch
  directory: launching from a subdirectory no longer creates a second,
  divergent project.
- Trust records are keyed by the symlink-resolved project root, so
  symlinked checkouts and macOS `/var` vs `/private/var` no longer
  re-prompt.
- `.marshal/config.toml` is committable: Marshal no longer force-appends
  `.marshal/` to `.gitignore` once a project config exists; machine-local
  state is excluded by a generated `.marshal/.gitignore`.
- Project-scope saves no longer bake user-global settings into
  `.marshal/config.toml`.
- `MARSHAL_CONFIG_DIR`, `MARSHAL_DATA_DIR`, `XDG_CONFIG_HOME`, and
  `XDG_DATA_HOME` are honoured for all Marshal state locations.

### Fixed

**Config**
- `[session]`, `[lsp]`, `[history]`, `[scratchpad]`, and `[agents]` are
  persisted again; config.session.rollover.set and config.lsp.set
  previously reported success but lost their values on restart.
- On first launch after this change, a session database left in a stray
  subdirectory `.marshal/` is adopted at the repository root.
- The settings layer reloader replays the session's trust decision
  through a non-prompting resolver, so provenance no longer shows a
  project layer the session never trusted — and a `/settings` save can
  never run the interactive trust prompt inside the TUI event loop.
- A global-scope settings save (user config) now refreshes the session's
  config-layer snapshot, so the next project-scope save no longer
  re-bakes user-global values into the committable
  `.marshal/config.toml`.
- `marshal --trust` keys the permanent-trust record on the repository
  root, so running it from a subdirectory writes a record the next
  root-anchored launch actually matches (and stores the root config's
  hash, not an empty one).

### Added

**Skills**
- `using-skills` entry-point skill that teaches the model to scan the skill
  roster and load matching skills before acting.
- Class-triggered skill hints: edit-class turns suggest test-driven
  development and verification skills, and investigation-shaped questions
  suggest systematic-debugging — no embedding model required.
- `/plan` now loads the `marshal-writing-plans` skill so inline plans follow
  the house format and chain into plan execution.
- Skill hint shortlists are logged per turn so misses can be tuned.

### Changed

**Skills**
- `skills.autoload` now defaults to `["using-skills"]` so skill activation
  works out of the box, including for users with no embedding model. Opt out
  with `skills.autoload = []` or the `/skills` panel toggle.

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
