# Marshal MVP Milestones A-B Design

## Purpose

This spec defines the first implementation pass for Marshal: complete the project skeleton from Milestone A and add a thin Bubble Tea TUI shell from Milestone B. The goal is a runnable local application with stable foundations, basic state, graceful shutdown, and placeholder UI regions for the later agent workflow.

## Scope

In scope:

- CLI entrypoint at `cmd/marshal/main.go`
- Config loader with defaults, global config, and project config precedence
- Standard library structured logging
- Basic app/session state
- Graceful shutdown through context cancellation
- Bubble Tea shell with chat input, transcript, status bar, streaming output placeholder, command palette placeholder, tool log placeholder, and diff placeholder
- Local-only message entry: pressing Enter appends the typed user message and clears input
- Unit tests for config loading, app state, shutdown behavior, and TUI update behavior

Out of scope:

- LLM provider interfaces and model calls
- Tool registry and native tool execution
- Approval prompts
- Patch application
- SQLite persistence
- Repository indexing
- Tree-sitter, embeddings, role routing, memories, and swarm behavior

## Architecture

`cmd/marshal/main.go` remains small. It delegates startup to an `internal/app` package and translates returned errors into process exit behavior.

The first implementation uses these packages:

- `internal/app`: top-level application runner and dependency wiring
- `internal/app/config`: config types, defaults, path discovery, TOML loading, and merge rules
- `internal/app/logging`: `log/slog` logger construction
- `internal/app/session`: app state, in-memory messages, and shutdown context
- `internal/app/tui`: Bubble Tea model, update logic, and view rendering

The package boundaries keep UI rendering separate from app state and config loading. Later milestones can replace the placeholder TUI panels with provider, tool, diff, and context data without moving the entrypoint or config contracts.

## Configuration

The config loader starts with built-in defaults, then merges config files in this order:

1. Global config: `~/.config/marshal/config.toml`
2. Project config: `.marshal/config.toml` under the current working directory

Project config wins over global config. Missing config files are ignored. Malformed config files return an error that includes the path that failed.

Initial default values:

- Project name: `marshal`
- Languages: `go`, `markdown`
- Test command: `go test ./...`
- Format command: `gofmt -w .`
- Vet command: `go vet ./...`
- Remote providers allowed: `false`
- Redact secrets: `true`
- Include gitignored files: `false`

The first TOML support only needs the fields required by the documented examples for project, commands, profile, privacy, and indexing settings. Unknown fields are allowed by the parser so future config examples do not break startup.

## App State

Session state tracks:

- Loaded config
- Working directory
- Startup time
- Shutdown/cancel state
- In-memory message list

Messages have a role and content. The only roles needed in this pass are `user` and `system`. The TUI appends user messages locally; no assistant response is generated yet.

## TUI Behavior

The Bubble Tea shell renders a work-focused terminal interface:

- Status bar with project name, working directory, and local-only indicator
- Transcript area for submitted user messages
- Streaming output placeholder
- Tool log placeholder
- Diff placeholder
- Command palette placeholder
- Single-line chat input

Keyboard behavior:

- Text input edits the current prompt
- Enter submits non-empty trimmed input as a user message
- Enter on empty input does nothing
- Ctrl+C or Esc requests quit

The initial view can be plain text and layout-safe. Styling polish is not required in this pass beyond clear section labels and predictable rendering.

## Error Handling

Startup returns errors instead of panicking. The CLI prints startup errors to standard error and exits non-zero.

Config errors include the failed path. Missing optional config files are not errors.

Graceful shutdown is context-based. Signal handling cancels the session context, asks Bubble Tea to quit, and allows app shutdown to return cleanly.

## Dependencies

Allowed dependencies:

- Bubble Tea for the TUI shell
- A TOML parser for config loading

The implementation should avoid extra logging, CLI, or dependency-injection packages for this pass. Standard library `log/slog`, `context`, `os/signal`, and focused internal packages are enough.

## Testing

Tests should be written before implementation for each behavior.

Required coverage:

- Default config contains the expected project, command, and privacy values
- Project config overrides global config
- Missing config files are ignored
- Malformed config returns an error with the path
- Session state appends messages and preserves insertion order
- Shutdown cancellation marks the session context done
- TUI Enter appends non-empty input and clears the input
- TUI Enter on whitespace input does not append a message
- TUI quit key returns a quit command

The verification command for this pass is:

```bash
go test ./...
```

The runnable smoke test is:

```bash
go run ./cmd/marshal
```

## Acceptance Criteria

- `go test ./...` passes
- `go run ./cmd/marshal` starts the TUI shell
- The shell can accept a typed message and show it in the transcript
- Ctrl+C or Esc exits cleanly
- Config loading works with no files, only a global file, only a project file, and both files
- The implementation does not include provider calls, tool execution, persistence, repo indexing, or patch application
