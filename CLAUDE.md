# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Marshal

Marshal is a Go CLI/TUI that combines Aider's feature surface (repo map, edit formats, git-native workflow, slash commands, watch mode, linter loop) with a four-role multi-model orchestration system. Every user turn is a discrete, branch-isolated, critic-reviewed task — but the UX is a fluid, streaming conversation like Aider or Claude Code.

**The product is the interaction model. The multi-model loop is the mechanism.** If any milestone change threatens the fluid feel, fluid feel wins.

## Commands

```bash
# Build
mkdir -p bin && go build -o bin/marshal ./cmd/marshal

# Run the built binary
./bin/marshal chat
./bin/marshal run "task description"

# Test
# Run all tests
go test ./...

# Run tests for a specific package
go test ./internal/git/...
go test ./internal/edit/...

# Run a specific test by name
go test -run TestNew_NotARepo ./internal/git/...
go test -run TestParseSearchReplace ./internal/edit/...

# Run tests with verbose output
go test -v ./...

# Lint
golangci-lint run

# Release snapshot
goreleaser release --snapshot --clean
```

**CLI commands (implemented):**
```bash
marshal config show                      # Print parsed config (secrets redacted)
marshal config show --profile dev        # Use a specific config profile
marshal chat                           # Conversational TUI (default)
marshal chat --no-ship                 # Keep changes on staging; don't merge to target on exit
marshal chat --no-warmup               # Skip endpoint warmup (for local models)
marshal run "task prompt"                # Single-task headless mode
marshal run -m "task prompt" --exit    # Use -m flag, exit code 1 on failure
marshal run -f task.txt --json         # Read task from file, NDJSON output
marshal run --no-ship --json           # Leave changes on staging, emit NDJSON
marshal pipeline "big feature"         # Multi-task DAG mode
marshal pipeline "feature" --pipeline-only  # Emit task graph without executing
marshal debug chat --role executor "hello"  # Stream a reply from one role
marshal debug git-session --task test    # Debug the branch session lifecycle
marshal version
```

**Configuration profiles:**
Use `--profile <name>` with any command to load profile-specific overrides from marshal.toml:
```bash
marshal --profile dev chat
marshal --profile local run "test task"
```

## Architecture

### Package layout

```
cmd/marshal/          — cobra root; run/pipeline/chat/config/version subcommands
internal/
  analytics/          — Opt-in PostHog analytics (command names + token spend)
  backend/            — Backend interface + openai_compat.go + registry.go + grammar.go
  commands/           — Slash command dispatcher (40+ built-in commands)
  config/             — TOML loader (BurntSushi/toml); precedence: flag > env > ./marshal.toml > ~/.config/marshal/config.toml
  edit/               — searchreplace.go, wholefile.go, udiff.go, format.go
  git/                — repo.go (os/exec wrapper), session.go (Session + TaskTx + Ship)
  history/            — Chat-history summarisation (distinct from loop-level compaction)
  linter/             — Shell-out to golangci-lint / flake8 / eslint, error parser
  logging/            — Structured logging initialization
  loop/               — engine.go (task orchestrator), verdict.go, compactor.go
  models/             — Per-model settings table (settings.toml, settings.go)
  output/jsonstream/  — NDJSON event emitter for CI mode
  pipeline/           — scheduler.go (DAG topo sort, errgroup tiers), integration.go
  planner/            — Marshal-model call that emits task-graph JSON
  prompts/            — layers.go; base/{marshal,executor,critic,compactor}.md; security/; skills
  reasoning/          — tags.go ( stripping, port of aider/reasoning_tags.py)
  repomap/            — PageRank-ranked repo map; queries/*.scm copied verbatim from aider
  sandbox/            — Path allowlist + command allowlist for tool-use executor
  session/            — SQLite store (sessions, tasks, rounds tables)
  skills/             — Skill registry from ~/.config/marshal/skills/*.toml + built-ins
  tokens/             — tiktoken-go wrapper + char-heuristic fallback + image tile math
  tools/              — read_file / write_file / run_command tool schemas
  ui/tui/             — bubbletea: model.go, run.go, styles.go, event.go
  voice/              — Voice input support (portaudio-based)
  watch/              — fsnotify AI-comment scanner (port of aider/watch.py)
```

### Four-role model roster

| Role | Purpose |
|------|---------|
| **Marshal** | Clarification and task decomposition (silent pass-through by default) |
| **Executor** | Code edits — via tool use (M12) or edit formats (M6) |
| **Critic** | Returns JSON verdict: `{verdict, summary, issue, fix, concerns[]}` |
| **Compactor** | History summarisation and commit message generation |

Each role can point at a different provider. All backends implement the same `Backend` interface (`Complete`, `Stream`, `TokenCount`, `SupportsTools`, `SupportsJSONMode`).

### Three-tier branch hierarchy

```
target branch        (e.g. main)         ← only touched by /ship
  └── marshal/session-<timestamp>        ← staging; accumulates all task work
        └── marshal/task-<id>            ← per-turn isolation; squash-merged on PASS, deleted on FAIL
```

- `/ship` squash-merges staging → target and starts a fresh staging branch.
- `/undo` / `/revert <id>` operate only on the staging branch; target is never touched.
- Marshal never force-pushes, never rewrites history on non-`marshal/*` branches.

### Task loop (`internal/loop/engine.go`)

1. User turn → task id generated → `git.TaskTx` creates `marshal/task-<id>` from staging HEAD
2. Round loop (default `max_rounds=3`):
   - Assemble executor prompt → stream response → apply edits → `git diff -U1` against staging HEAD
   - Call critic → strip `` blocks → parse JSON verdict
   - **PASS** → squash-merge task branch into staging, update ledger `status='passed'`
   - **FAIL** → inject `issue`/`fix` into next round; on exhaustion, delete task branch, `status='failed'`
3. TUI event channel (`Submit(prompt) <-chan Event`): `TokenChunk`, `RoundStart`, `VerdictBadge`, `TaskMerged`, `TaskReverted`, `ClarificationQuestion`

### SQLite ledger (3 tables)

- `sessions(id, target_branch, target_start_sha, staging_branch, started_at, shipped_at, shipped_target_sha)`
- `tasks(id, session_id, prompt, parent_staging_sha, staging_sha, status, started_at, ended_at, summary)` — status ∈ `{running, passed, failed, reverted_by_user, discarded}`
- `rounds(session_id, task_id, round, role, model, prompt_tokens, completion_tokens, duration_ms, content, verdict_json, think_blocks)`

### Skills system (`internal/skills/`)

Skills are TOML files that define custom slash commands with specialized prompts:

```toml
name = "Schema Migration"
trigger = "/migrate"

[executor]
system_extra = "You are a database migration specialist..."

[critic]
system_extra = "Review migrations for safety..."
```

Built-in skills (embedded at build time): `/schema-migration`, `/security-audit`, `/test-generation`
User skills: `~/.config/marshal/skills/*.toml` (override built-ins if triggers collide)

### Edit formats (`internal/edit/`)

Three formats supported; selected via `loop.EditFormat` config:
- `search_replace` — Fuzzy SEARCH/REPLACE blocks (default for capable models)
- `udiff` — Unified diff format
- `wholefile` — Full file in fenced code blocks (fallback for weaker models)

Tool use (M12) is additive — models with `supports_tools=true` use `read_file`/`write_file`/`run_command` instead of parsing edit formats.

### Key design decisions (locked)

- **Git via `os/exec`**, not `go-git` — matches real `git` behaviour exactly
- **SQLite via `modernc.org/sqlite`** — pure-Go, no cgo
- **Task merges use `git merge --squash`** — one commit per task makes `/undo` and `/revert <id>` trivial
- **Edit formats are kept** (M6) for models without tool use; M12 tool-use is additive
- **Concurrency:** one goroutine per pipeline task; `errgroup` for tier-level sync; per-path `sync.Mutex` map for file-overlap serialisation
- **Prompt-prefix caching discipline:** system prompt prefix must be byte-identical across rounds 1→N; git diff is always the final message
- **No confirmation gate** by default — every user turn is immediately a task; marshal-model clarification is opt-in for genuinely ambiguous prompts only

## Milestone status (as of 2026-04-14)

| Milestone | Status |
|-----------|--------|
| M0 skeleton | done |
| M1 backend | done |
| M2 git layer | done |
| M3 single-task loop | done |
| M4 TUI + task ledger | done |
| M5 repo map | done |
| M6 edit formats | done |
| M7 linter feedback loop | done |
| M8 prompt layering + compaction + skills | done |
| M9 pipeline | done |
| M10 Aider-parity polish | done (slash commands, read-only files, watch mode, history, .marshalignore, voice, web, onboarding) |
| M11 headless/CI mode | done (NDJSON output, exit codes, --json flag) |
| M12 executor tool use | done (read_file, write_file, run_command with sandbox) |
| M13 release | not started |

## Aider files to read before writing Go counterparts

| Aider file | Go counterpart |
|-----------|---------------|
| `aider/coders/search_replace.py` | `internal/edit/searchreplace.go` — port test cases verbatim from `tests/basic/test_editblock.py` |
| `aider/reasoning_tags.py` | `internal/reasoning/tags.go` |
| `aider/repomap.py` | `internal/repomap/*.go` |
| `aider/queries/tree-sitter-language-pack/*.scm` | `internal/repomap/queries/*.scm` — copy verbatim (MIT licensed) |
| `aider/repo.py` | `internal/git/repo.go` |
| `aider/linter.py` | `internal/linter/linter.go` |
| `aider/history.py` | `internal/history/summary.go` |
| `aider/commands.py` | `internal/commands/*.go` |
| `aider/models.py::token_count_for_image` | `internal/tokens/image.go` |
| `aider/sendchat.py` | `internal/backend/openai_compat.go` — `RETRY_TIMEOUT=60s` backoff behaviour |
| `aider/watch.py` | `internal/watch/watch.go` |
| `aider/resources/model-settings.yml` | `internal/models/settings.toml` |

## Exit codes for headless/CI mode

- `0` — Task passed and merged (or left on staging with `--no-ship`)
- `1` — Task failed after all rounds
- `2` — Configuration error
- `3` — Git error
- `4` — Pipeline integration failure
