# Changelog

All notable changes to Marshal will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-04-14

### Added
- Initial release of Marshal, an AI coding assistant with multi-model orchestration
- **Four-role architecture**: Marshal (gate), Executor, Critic, and Compactor models
- **Git-native workflow**: Three-tier branch hierarchy (target → staging → task branches)
- **Edit formats**: Search/replace, unified diff, and whole-file modes
- **Tool use**: `read_file`, `write_file`, `run_command` tools for capable models
- **TUI**: Interactive chat interface with bubbletea
- **Headless/CI mode**: NDJSON output with structured exit codes
- **Configuration**: TOML-based config with env var expansion and profiles
- **Security**: Path allowlist, command allowlist, sandboxed tool execution
- **Repo map**: Tree-sitter based symbol extraction with PageRank ranking
- **Skills**: TOML-based skill system for prompt specialization
- **Linter integration**: Automatic linter feedback loop
- **Pipeline mode**: Multi-task DAG execution with integration critic

### Features
- `/add`, `/drop`, `/ls` - File management
- `/diff`, `/commit`, `/ship`, `/undo`, `/revert` - Git operations  
- `/history`, `/tokens` - Session introspection
- `/map`, `/map-refresh` - Repo map management
- `/settings`, `/model` - Configuration
- `/skills`, `/skill <name>` - Skill activation
- `/run`, `/test`, `/git`, `/lint` - Shell command shortcuts
- `/web`, `/paste`, `/copy`, `/copy-context` - Content operations
- `/editor`, `/edit` - External editor integration
- `/reset`, `/save`, `/load` - Session management
- `/help`, `/multiline-mode` - UX improvements

### Configuration
- Support for OpenAI-compatible APIs (Fireworks, OpenAI, Anthropic, Ollama, etc.)
- Per-model configuration (supports_tools, edit_format, etc.)
- Profile-based configuration switching
- Git integration with branch isolation
- SQLite session ledger

### Security
- Path traversal prevention
- Symlink validation
- Command allowlisting
- Path sandboxing

### Platforms
- Linux (amd64, arm64)
- macOS (amd64, arm64)
- Windows (amd64, arm64)

[Unreleased]: https://github.com/alecpullen/marshal/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/alecpullen/marshal/releases/tag/v0.1.0
