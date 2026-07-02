# Milestone E: Basic Native Tools Design

## Goal

Milestone E adds Marshal's first concrete native tools:

- `file.read`
- `repo.search`
- `git.status`
- `git.diff`
- `shell.run`
- `test.run`

These tools should be real and directly executable through the Milestone D registry handlers. They should not add approval UI, persistent audit logging, agent loop wiring, patch application, or a full shell policy engine.

## Package Boundary

Create one package for the MVP native tools:

```text
internal/tools/native
```

The package imports `internal/tools/registry` and registers tool metadata plus handlers. It should not import TUI, session, agent, database, or LLM packages.

One package is intentional for Milestone E. The six tools share workspace path handling, command running, output limits, and JSON argument decoding. If the implementation grows too large in later milestones, it can split into `filesystem`, `search`, `git`, `shell`, and `test_runner` packages.

## Public API

```go
type Options struct {
	WorkspaceRoot  string
	CommandRunner  CommandRunner
	TestCommand    string
	MaxOutputBytes int
}

type CommandRunner interface {
	Run(ctx context.Context, req CommandRequest) (CommandResult, error)
}

type CommandRequest struct {
	Command string
	Dir     string
	Timeout time.Duration
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func RegisterAll(reg *registry.Registry, opts Options) error
```

`RegisterAll` registers all six tools. If registration fails, it returns the first error. It does not partially roll back earlier successful registrations.

`Options.WorkspaceRoot` is required. All file and command operations are scoped to this root.

`Options.CommandRunner` is optional. If nil, use a default `exec.CommandContext` based runner.

`Options.TestCommand` is optional. If empty, default to `go test ./...`.

`Options.MaxOutputBytes` is optional. If zero or negative, default to `200000`.

## Shared Behavior

### JSON Args

Each handler receives `registry.ToolCall.Args`. It must:

1. Call `registry.ValidateArgs(tool, call.Args)` before decoding.
2. Decode into a tool-specific argument struct.
3. Treat empty args as an empty JSON object.

Malformed args return an error and an empty `ToolResult`.

### Workspace Path Safety

Any argument that names a workspace path must be resolved through a shared helper:

```go
func resolveWorkspacePath(root string, rel string) (string, error)
```

Rules:

- `rel` must be relative.
- empty path means workspace root only for tools where that is meaningful.
- clean the path with `filepath.Clean`.
- reject paths that escape `WorkspaceRoot` via `..`.
- reject absolute paths.
- symlink traversal is out of scope for Milestone E; document this limitation in code comments and tests should not depend on symlink behavior.

### Result Shape

Handlers return `registry.ToolResult`:

- `Summary`: short, human/model-facing status line.
- `Content`: full textual output, capped by `MaxOutputBytes`.
- `FilesChanged`: empty for all Milestone E tools.
- `CommandExitCode`: populated by `shell.run` and `test.run`; nil for read/search/git inspection tools unless command execution fails before a process exits.

### Output Limiting

Use a shared helper that truncates combined textual output to `MaxOutputBytes` bytes. When truncation happens, append a clear marker:

```text

[output truncated]
```

The helper may truncate by bytes, not runes. This is acceptable for MVP command/file/search output.

## Tool Details

### `file.read`

Risk: `registry.RiskReadOnly`

Args:

```json
{
  "path": "relative/path.go",
  "start_line": 1,
  "end_line": 80
}
```

Behavior:

- `path` is required and must resolve under `WorkspaceRoot`.
- Reads a regular file.
- Splits on `\n`.
- Line numbers are 1-based.
- `start_line <= 0` means start at line 1.
- `end_line <= 0` means read through EOF.
- Reject `start_line > end_line` when both are positive.
- Return selected content in `ToolResult.Content`.
- `Summary` includes the relative path and returned line range.

Non-goals:

- binary detection beyond returning read errors or raw text
- token-aware truncation
- symlink policy

### `repo.search`

Risk: `registry.RiskReadOnly`

Args:

```json
{
  "query": "text to find",
  "path": "optional/subdir",
  "max_results": 50
}
```

Behavior:

- `query` is required and must be non-empty.
- `path` is optional and scopes the search under `WorkspaceRoot`.
- Implement search in Go for deterministic tests; do not depend on `rg`.
- Walk files recursively.
- Skip directories: `.git`, `.idea`, `.superpowers`, `.worktrees`, `node_modules`, `vendor`, `dist`.
- Skip non-regular files.
- Read text files as bytes/strings; if a file cannot be read, skip it.
- Match by simple substring, case-sensitive.
- Output lines as:

```text
relative/path:line:line text
```

- Stop at `max_results`, default `50`, hard cap `200`.
- `Summary` reports match count and whether results were capped.

Non-goals:

- regex search
- `.gitignore` parsing
- binary file detection beyond best-effort skip-on-read-error

### `git.status`

Risk: `registry.RiskReadOnly`

Args: empty object.

Behavior:

- Runs `git status --short` in `WorkspaceRoot`.
- Uses `CommandRunner`.
- Returns stdout/stderr in `Content`.
- Populates `CommandExitCode`.
- `Summary` says whether the working tree appears clean based on empty stdout.

### `git.diff`

Risk: `registry.RiskReadOnly`

Args:

```json
{
  "path": "optional/relative/path"
}
```

Behavior:

- Runs `git diff --` with optional path argument resolved under `WorkspaceRoot`.
- Use a relative path when passing the path to git.
- Uses `CommandRunner`.
- Returns stdout/stderr in `Content`.
- Populates `CommandExitCode`.
- `Summary` reports whether a diff was present based on empty stdout.

### `shell.run`

Risk: `registry.RiskCommand`

Args:

```json
{
  "command": "go test ./...",
  "timeout_seconds": 120
}
```

Behavior:

- `command` is required and must be non-empty.
- Runs in `WorkspaceRoot` through `CommandRunner`.
- Default timeout: `120s`.
- Max timeout: `300s`.
- Populates `CommandExitCode`.
- `Content` includes stdout and stderr with labels:

```text
stdout:
...

stderr:
...
```

- `Summary` includes exit code.

Conservative guardrails before execution:

Reject commands containing obvious dangerous patterns:

- `sudo`
- `rm -rf`
- `git reset --hard`
- `git clean -fd`
- `curl` or `wget` combined with a pipe to `sh`, `bash`, or `zsh`
- `mkfs`
- `shutdown`
- `reboot`
- `chmod -R`
- `chown -R`

This is not a full command classifier. Milestone F owns command classification, approvals, and policy. In Milestone E, blocked commands return an error before calling the runner.

Implementation note:

The default runner may use `exec.CommandContext(ctx, shell, "-lc", command)` where `shell` is `/bin/sh` unless unavailable. Tests should use fake runners for most command behavior.

### `test.run`

Risk: `registry.RiskCommand`

Args:

```json
{
  "command": "optional override"
}
```

Behavior:

- Uses `args.command` when provided, otherwise `Options.TestCommand`, otherwise `go test ./...`.
- Runs through the same `shell.run` command guardrails and `CommandRunner`.
- Default timeout: `300s`.
- Max timeout: `600s`.
- Populates `CommandExitCode`.
- `Summary` includes command and exit code.

Non-goal:

- reading app config directly. The app or future broker can pass `config.Commands.Test` into `Options.TestCommand` when wiring exists.

## Tool Schemas

Each registered tool includes a syntactically valid JSON schema in `Tool.Schema`.

The schemas should be minimal Draft-like objects:

```json
{"type":"object","properties":{...},"required":[...]}
```

Milestone D only validates schema JSON syntax; full JSON Schema enforcement remains out of scope.

## Testing Strategy

Use package-local tests with temporary directories and fake command runners.

Tests should cover:

- `RegisterAll` registers exactly the six expected tools with expected risks.
- `RegisterAll` requires a workspace root.
- `file.read` reads whole files.
- `file.read` reads selected line ranges.
- `file.read` rejects path traversal.
- `repo.search` finds simple substring matches.
- `repo.search` respects max result caps.
- `repo.search` skips ignored directories.
- `git.status` invokes runner with `git status --short` in the workspace root.
- `git.diff` invokes runner with `git diff --` and optional path.
- `shell.run` invokes runner for allowed commands.
- `shell.run` blocks dangerous patterns before runner invocation.
- `shell.run` applies default and maximum timeout behavior.
- `test.run` uses default test command.
- `test.run` allows command override but still applies guardrails.
- command output is truncated with marker when over limit.

Verification commands:

```bash
go test ./internal/tools/native ./internal/tools/registry
go test ./...
go vet ./...
```

## Non-Goals

Milestone E does not:

- add approval prompts or TUI approval flows
- persist audit events
- add an agent tool-use loop
- implement patch application
- implement `.gitignore` parsing
- implement full shell command classification
- stream command output live into the TUI
- add MCP/plugin tools
- add Tree-sitter or symbol tools
- add third-party dependencies

## Acceptance Criteria

Milestone E is complete when:

- all six tools are registered by `native.RegisterAll`
- all six tools can be invoked through registry handlers
- read/search/git tools are read-only
- command tools execute through `CommandRunner`
- obvious dangerous shell commands are blocked before execution
- `docs/10-mvp-implementation-checklist.md` marks Milestone E complete
- `go test ./...` passes
- `go vet ./...` passes
