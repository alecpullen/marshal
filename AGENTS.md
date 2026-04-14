# AGENTS.md

Marshal is a Go CLI/TUI AI coding assistant with four-role multi-model orchestration and git-native workflows.

## Build & Test

```bash
# Build
mkdir -p bin && go build -o bin/marshal ./cmd/marshal

# Run all tests
go test ./...

# Run tests with race detection (CI)
go test -race ./...

# Test specific package
go test ./internal/git/...
go test ./internal/edit/...

# Run specific test by name
go test -run TestNew_NotARepo ./internal/git/...

# Lint
golangci-lint run

# Release snapshot
goreleaser release --snapshot --clean
```

## CLI Commands

```bash
./bin/marshal chat                      # Interactive TUI (default)
./bin/marshal run "task prompt"       # Single-task headless mode
./bin/marshal pipeline "big feature"  # Multi-task DAG mode
./bin/marshal config show             # Print parsed config
./bin/marshal version
```

Flags: `--profile <name>` loads profile-specific overrides; `--no-ship` leaves changes on staging; `--json` emits NDJSON for CI.

## Architecture

**Four roles:** Marshal (orchestrator), Executor (code gen), Critic (review), Compactor (history). Each can use different models/backends.

**Three-tier git hierarchy:**
- Target branch (e.g., `main`) — only touched by `/ship`
- `marshal/session-<timestamp>` — staging branch accumulates all task work
- `marshal/task-<id>` — per-turn isolation; squash-merged on PASS, deleted on FAIL

**Key packages:**
- `cmd/marshal/` — cobra root with subcommands
- `internal/loop/` — task orchestrator engine
- `internal/git/` — `os/exec` wrapper (not go-git), session lifecycle, TaskTx
- `internal/backend/` — Backend interface + OpenAI-compatible client
- `internal/edit/` — search/replace, udiff, wholefile formats
- `internal/tools/` — read_file, write_file, run_command schemas
- `internal/repomap/` — tree-sitter symbol extraction with PageRank
- `internal/pipeline/` — DAG scheduler with tier-level concurrency
- `internal/session/` — SQLite store (modernc.org/sqlite, no cgo)

**Design decisions (locked):**
- Git via `os/exec`, not go-git — matches real git behavior
- SQLite via `modernc.org/sqlite` — pure-Go
- Task merges use `git merge --squash` — enables trivial `/undo` and `/revert <id>`
- Edit formats kept for non-tool-use models; tool-use (M12) is additive
- No confirmation gate by default — every user turn is immediately a task

## Configuration

TOML with environment variable expansion (`${VAR}`). Precedence: flag > env > `./marshal.toml` > `~/.config/marshal/config.toml`.

```toml
[model.executor]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/routers/kimi-k2p5-turbo"
supports_tools = true

[loop]
max_rounds = 3

[git]
enabled = true
```

Profiles: define `[profiles.<name>.model.*]` sections, activate with `--profile <name>`.

## Exit Codes (Headless/CI)

- `0` — Task passed and merged (or left on staging with `--no-ship`)
- `1` — Task failed after all rounds
- `2` — Configuration error
- `3` — Git error
- `4` — Pipeline integration failure

## Testing Tips

- Task tests may create git branches; ensure you're in a test repo or use `--no-ship`
- Test scripts in repo root (`test_tools.sh`, `test_simple.sh`) require LM Studio on port 1234 and/or Fireworks API key
- CI runs: `go vet ./...`, `go test -race ./...`, `golangci-lint run`, `goreleaser check`

## Dependencies

- Go 1.25+
- Key external: `github.com/spf13/cobra`, `github.com/charmbracelet/bubbletea`, `github.com/smacker/go-tree-sitter`, `modernc.org/sqlite`
- CGO disabled for builds (pure Go SQLite)

## CI Workflow

`.github/workflows/ci.yml`: vet → test with race → lint → goreleaser-check on PR/push to main.
