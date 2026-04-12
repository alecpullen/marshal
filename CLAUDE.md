# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Marshal

Marshal is a Go CLI/TUI that combines Aider's feature surface (repo map, edit formats, git-native workflow, slash commands, watch mode, linter loop) with a four-role multi-model orchestration system. Every user turn is a discrete, branch-isolated, critic-reviewed task — but the UX is a fluid, streaming conversation like Aider or Claude Code.

**The product is the interaction model. The multi-model loop is the mechanism.** If any milestone change threatens the fluid feel, fluid feel wins.

## Commands

```bash
# Build
go build -o bin/marshal ./cmd/marshal

# Test
go test ./...

# Run a single test package
go test ./internal/git/...

# Lint
golangci-lint run

# Release snapshot
goreleaser release --snapshot --clean
```

**CLI subcommands (once implemented):**
```bash
marshal config show                   # Print parsed config (secrets redacted)
marshal config show --profile dev     # Use a specific config profile
marshal chat                          # Conversational TUI
marshal run "task prompt"             # Single-task headless mode
marshal run --no-tui --json "..."     # CI/headless NDJSON output
marshal pipeline "big feature"        # Multi-task DAG mode
marshal debug chat --role executor "hello"  # Stream a reply from one role
marshal debug git-session --task test # Debug the branch session lifecycle
```

## Architecture

### Package layout (target state)

```
cmd/marshal/          — cobra root; run/pipeline/chat/config/version subcommands
internal/
  backend/            — Backend interface + openai_compat.go + registry.go
  commands/           — Slash command dispatcher
  config/             — TOML loader (BurntSushi/toml + viper); precedence: flag > env > ./marshal.toml > ~/.config/marshal/config.toml
  edit/               — searchreplace.go, editblock.go, wholefile.go, udiff.go
  git/                — repo.go (os/exec wrapper), session.go (Session + TaskTx + Ship)
  history/            — Chat-history summarisation (distinct from loop-level compaction)
  linter/             — Shell-out to golangci-lint / flake8 / eslint, error parser
  loop/               — engine.go (task orchestrator), verdict.go, queue.go, compactor.go
  models/             — Per-model settings table
  output/jsonstream/  — NDJSON event emitter for CI mode
  pipeline/           — scheduler.go (DAG topo sort, errgroup tiers), integration.go
  planner/            — Marshal-model call that emits task-graph JSON
  prompts/            — layers.go; base/{marshal,executor,critic,compactor}.md; security/; skills
  reasoning/          — tags.go (<think> stripping, port of aider/reasoning_tags.py)
  repomap/            — PageRank-ranked repo map; queries/*.scm copied verbatim from aider
  sandbox/            — Path allowlist + command allowlist for tool-use executor
  session/            — SQLite store (sessions, tasks, rounds tables)
  skills/             — Skill registry from ~/.config/marshal/skills/*.toml
  tokens/             — tiktoken-go wrapper + char-heuristic fallback + image tile math
  tools/              — read_file / write_file / run_command tool schemas
  ui/tui/             — bubbletea: chatViewport, promptBar, statusBar, historyPane, thinkBlock
  watch/              — fsnotify AI-comment scanner (port of aider/watch.py)
```

### Four-role model roster

| Role | Purpose |
|------|---------|
| **Marshal** | Clarification and task decomposition (silent pass-through by default) |
| **Executor** | Code edits — via tool use (M12) or edit formats (M6) |
| **Critic** | Returns JSON verdict: `{verdict, summary, issue, fix, concerns[]}` |
| **Compactor** | History summarisation and commit message generation |

Each role can point at a different provider. All backends implement the same `Backend` interface (`Complete`, `Stream`, `TokenCount`, `SupportsTools`, `SupportsJSONMode`). OpenAI-compatible is the only backend in v0.1; native Anthropic/Google adapters are a follow-up.

### Three-tier branch hierarchy

```
target branch        (e.g. main)         ← only touched by /ship
  └── marshal/session-<timestamp>        ← staging; accumulates all task work
        └── marshal/task-<id>            ← per-turn isolation; squash-merged on PASS, deleted on FAIL
```

- `/ship` squash-merges staging → target and starts a fresh staging branch.
- `/undo` / `/revert <id>` operate only on the staging branch; target is never touched.
- Marshal never force-pushes, never rewrites history on non-`marshal/*` branches.

### Task loop (M3 `internal/loop/engine.go`)

1. User turn → task id generated → `git.TaskTx` creates `marshal/task-<id>` from staging HEAD
2. Round loop (default `max_rounds=3`):
   - Assemble executor prompt → stream response → apply edits → `git diff -U1` against staging HEAD
   - Call critic → strip `<think>` blocks → parse JSON verdict
   - **PASS** → squash-merge task branch into staging, update ledger `status='passed'`
   - **FAIL** → inject `issue`/`fix` into next round; on exhaustion, delete task branch, `status='failed'`
3. TUI event channel (`Submit(prompt) <-chan Event`): `TokenChunk`, `RoundStart`, `VerdictBadge`, `TaskMerged`, `TaskReverted`, `ClarificationQuestion`

### SQLite ledger (3 tables)

- `sessions(id, target_branch, target_start_sha, staging_branch, started_at, shipped_at, shipped_target_sha)`
- `tasks(id, session_id, prompt, parent_staging_sha, staging_sha, status, started_at, ended_at, summary)` — status ∈ `{running, passed, failed, reverted_by_user, discarded}`
- `rounds(session_id, task_id, round, role, model, prompt_tokens, completion_tokens, duration_ms, content, verdict_json, think_blocks)`

## Key design decisions (locked)

- **Git via `os/exec`**, not `go-git` — matches real `git` behaviour exactly
- **SQLite via `modernc.org/sqlite`** — pure-Go, no cgo
- **Task merges use `git merge --squash`** — one commit per task makes `/undo` and `/revert <id>` trivial
- **Edit formats are kept** (M6) for models without tool use; M12 tool-use is additive
- **Concurrency:** one goroutine per pipeline task; `errgroup` for tier-level sync; per-path `sync.Mutex` map for file-overlap serialisation
- **Prompt-prefix caching discipline:** system prompt prefix must be byte-identical across rounds 1→N; git diff is always the final message
- **No confirmation gate** by default — every user turn is immediately a task; marshal-model clarification is opt-in for genuinely ambiguous prompts only

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

## Milestone order and deliverables

M0 → M1 → M2 → M3 → M4 → M5 (‖M4) → M6 → M7 (‖M6) → M8 → M9 → M10 → M11 → M12 → M13

### Status (as of 2026-04-13)

| Milestone | Status |
|-----------|--------|
| M0 skeleton | **done** |
| M1 backend | **done** |
| M2 git layer | **done** |
| M3 single-task loop | **done** |
| M4 TUI + task ledger | **done** |
| M5 repo map | **done** |
| M6 edit formats | **done** (wholefile, search-replace, udiff; Format interface; per-model settings) |
| M7 linter feedback loop | **done** |
| M8 prompt layering + compaction + skills | **done** (security layer; schema-migration, security-audit, test-generation built-ins) |
| M9 pipeline | **done** (planner, scheduler, integration critic, pipeline command) |
| M10 Aider-parity polish | not started |
| M11 headless/CI mode | not started |
| M12 executor tool use | not started |
| M13 release | not started |

### M0 — Skeleton
Config loader (TOML + env), cobra root with `chat`/`run`/`version` stubs, `go.mod`, `goreleaser.yml`.

### M1 — Backend
`Backend` interface (`Complete`, `Stream`, `TokenCount`, `SupportsTools`, `SupportsJSONMode`), `OpenAICompatBackend`, `Registry` (role → backend), token-count helpers with tiktoken-go + char-heuristic fallback.

### M2 — Git layer
`git.Repo` (os/exec wrapper), `git.Session` (three-tier branch hierarchy), `TaskTx` (create/commit/merge/delete task branch), `MergeSquash`, `Ship`.

### M3 — Single-task loop (core)
`internal/loop/engine.go`: round loop, executor call, critic verdict parse, PASS/FAIL routing, SQLite ledger writes, `Sink` event channel. Default `max_rounds=3`.

### M4 — TUI + task ledger
`bubbletea` model: `chatViewport`, `promptBar`, `statusBar`, `historyPane`. Task ledger (`/history`). ChanSink wiring. `tui.Run()`.

### M5 — Repo map
`internal/repomap`: tree-sitter tag extraction, PageRank ranking, token-budget packing. `.scm` query files copied verbatim from aider. File-content injection into executor prompt up to 100k char budget (`buildFileContext`). Per-session cache invalidation on file changes.

### M6 — Edit formats
- `internal/edit/searchreplace.go` — fuzzy SEARCH/REPLACE (port aider's test cases)
- `internal/edit/wholefile.go` — fenced `path:\n\`\`\`content\`\`\`` blocks
- `internal/edit/udiff.go` — unified diff apply
- `internal/edit/format.go` — `Format` interface + `FormatFor(name)` picker, `AllFormats()`
- `internal/models/settings.toml` — per-model capabilities (supports_tools, edit_format, etc.)
- `internal/models/settings.go` — `Registry.Lookup(model)`, `DefaultSettings()`, pattern matching
- `loop.Config.EditFormat` selects format; `applyEdits` returns changed file paths

### M7 — Linter feedback loop
`internal/linter`: shell out to golangci-lint/flake8/eslint based on file extension; `LintResult{file,line,message}`. After edits, run linter on changed files; errors short-circuit critic and inject directly into next executor round as issue/fix. `--lint` CLI flag and `/lint` TUI command (M10).

### M8 — Prompt layering + compaction + skills
- `internal/prompts/layers.go` — `Assemble(base, extra)` with fixed layer order
- `internal/skills` — TOML skill registry from `~/.config/marshal/skills/*.toml`; `Skill{Name,Trigger,Executor.SystemExtra,Critic.SystemExtra}`
- `internal/commands/dispatcher.go` — slash command dispatch (builtins take priority over skills)
- `internal/loop/compactor.go` — after `compact_after` consecutive FAILs, call compactor model with full failure history; synthesize `(issue, fix)`; reset window after each synthesis
- Per-engine system prompts (`execSysPrompt`, `criticSysPrompt`) assembled at `loop.New()` time
- Caching discipline: system-prompt prefix byte-identical across rounds; git diff always the final message

### M9 — Multi-task pipeline
- `internal/planner` — marshal model call → task-graph JSON; user confirmation in TUI
- `internal/pipeline/scheduler.go` — topo sort → execution tiers; per-path mutex serialisation; errgroup goroutines
- `internal/pipeline/integration.go` — combined diff → integration critic; PASS → topo merge into staging; FAIL → surface implicated tasks
- TUI: tier-column pipeline view with per-task round counters
- `--pipeline-only` flag emits graph without executing

### M10 — Aider-parity polish
- Full slash command set: `/commit`, `/tokens`, `/run`, `/test`, `/git`, `/map`, `/map-refresh`, `/settings`, `/web`, `/paste`, `/read-only`, `/reset`, `/save`, `/load`, `/copy`, `/editor`, `/think-tokens`, `/reasoning-effort`, `/multiline-mode`
- Read-only files (`abs_read_only_fnames` equivalent)
- Watch mode (`internal/watch`, fsnotify, `// ai` / `# ai` markers)
- Chat history summarisation (`internal/history`, port `aider/history.py`)
- Image/multimodal: base64 data URIs, `supports_vision` flag, tile-token math
- `.marshalignore` gitignore-syntax exclusions
- Voice (`/voice`), web scraping (`/web`), help RAG (`/help`)
- Onboarding (detect API keys, interactive setup)
- Analytics (opt-in PostHog; command names + token spend only)
- One-shot mode (`-m`, `-f`, `--exit`)

Ship in two sub-phases if needed: M10a = commands + read-only + watch + history + ignore; M10b = voice + web + help + onboarding + analytics.

### M11 — Headless / CI mode
`internal/output/jsonstream`: NDJSON events (`session_start`, `round_start`, `round_end`, `verdict`, `merged`, `session_end`). Exit codes: 0=PASS, 1=exhausted, 2=config error, 3=git error, 4=pipeline integration FAIL. Token-cost accounting in `session_end`. Example `.github/workflows/marshal.yml`.

### M12 — Executor tool use
`internal/tools`: `read_file`/`write_file`/`run_command` schemas. `internal/sandbox`: path allowlist (prefix-check vs repoRoot, reject `..`/symlink escapes), command allowlist (configurable). Multi-turn tool-call loop within a single executor round. Fallback to M6 edit formats when `SupportsTools()==false`.

### M13 — Benchmarks, docs, release
Aider exercism benchmark runner. Jekyll/Hugo docs: model-roster, config reference, skill authoring, CI example, TUI keybindings. `goreleaser` for 6 platforms (linux/darwin/windows × amd64/arm64). `CHANGELOG.md`. Tagged v0.1.0.

### End-to-end verification (11 steps = v0.1.0-ready)
See `plan.md §"End-to-end verification plan"` for the full scenario. Key gates:
1. Fresh clone → `marshal chat` → repo map populates, status bar shows staging branch, `main` HEAD unchanged
2. Three rapid prompts queue and execute in order; each PASS badge; staging accumulates three squash commits; `main` untouched
3. Ambiguous prompt → marshal clarifying question inline; reply continues same task
4. Forced failure → critic FAILs ×3 → task branch deleted → `✗ reverted` badge → staging HEAD unchanged
5. `/history`, `/undo`, `/revert <id>` (with conflict detection for dependent tasks)
6. `/ship --dry-run` then `/ship` → `main` advances by exactly one commit; fresh staging branch starts
7. `marshal pipeline` → planner graph → user confirm → parallel tiers → integration critic → topo merge → `/ship`
8. `marshal run --no-tui --json` → NDJSON schema conformance
9. Watch mode: `// ai:` marker → auto task → appears in ledger
10. Safety: `git log main` only advances on explicit `/ship`; staging branch deletion handled cleanly
