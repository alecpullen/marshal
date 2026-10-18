# Architecture

Marshal is a terminal-native coding agent built in Go. It wires together a Bubble Tea TUI, an agent loop, a provider-flexible LLM layer, safe native tools, repository intelligence, and SQLite persistence.

## High-level flow

1. `cmd/marshal/main.go` calls `internal/app.Run()`.
2. `app.Run()` loads merged config, builds `session.State`, and starts the TUI program.
3. The TUI (`internal/app/tui/`) collects input and dispatches it to the agent runtime.
4. The agent (`internal/agent/`) runs a loop: build prompt → call LLM → parse tool calls → execute tools → render results.
5. Tools (`internal/tools/`) operate on the working tree under policy and approval.
6. State and history are persisted in SQLite (`internal/db/`).

## Project identity and config layers

Marshal anchors everything project-scoped at the **git repository root**,
not the launch directory. `resolveWorkingDir` (internal/app) walks up from
the working directory with `repo.FindRoot` until it finds a `.git` entry;
non-git directories are used as-is. The resolved root decides where
`.marshal/` lives, which session database opens (`db.Path` re-derives the
same root, so a subdirectory launch shares one project), and which
project config loads. Launching from a subdirectory therefore lands in
the same project — same config, database, and history — as launching
from the root.

**Trust key.** Trust records are keyed by `trust.Canonicalize(workingDir)`:
the symlink-resolved absolute path of the repository root. This makes
symlinked checkouts and macOS `/var` vs `/private/var` one identity, so a
record written by one launch style is found by every other. The record
also stores the SHA-256 hash of `.marshal/config.toml` at trust time;
`trust.Evaluate` revalidates it on every launch and re-prompts when the
file changed outside a trusted session. Interactive project-config saves
(`/settings`, `/set`, `/agents`) advance the stored hash via
`RefreshConfigHash`, so a user's own edit does not force a re-prompt.

**Committable project config.** `.marshal/config.toml` is a shared,
often-committed file: it travels with the clone so commands, diagnostics,
and sandbox policy reproduce on other machines, gated by the trust hash
above. Three mechanisms keep machine-local state out of it:

- Providers and presets are user-global only. `[providers]` and
  `[models.presets]` live exclusively in the user config
  (`~/.config/marshal/config.toml`, mode 0600) — `SaveProjectConfig` never
  writes them, and every editing surface (connect, `/settings`, `/options`,
  `/models`, the `config.providers.*`/`config.models.preset.*` tools) saves
  them globally. On load, a trusted project file that still carries either
  section is hoisted into the user config and stripped; an entry that
  conflicts with an existing user-global one is kept project-local with a
  deprecation diagnostic rather than overwriting user state.
- `SaveProjectConfig` is layer-aware. It receives the load-time
  `config.Layers` snapshot and drops any section whose merged value equals
  the user-layer value, so user-global profiles and settings are never
  baked into the project file. Callers without a snapshot (zero `Layers`)
  keep the historical write-everything behaviour.
- A generated `.marshal/.gitignore` excludes machine-local state
  (`marshal.db`, logs, tool results, pipeline scratch) even when the
  config itself is committed. The top-level `.gitignore` is only appended
  while no project config exists.

**Environment overrides.** `MARSHAL_CONFIG_DIR` (or
`$XDG_CONFIG_HOME/marshal`) relocates the user config directory;
`MARSHAL_DATA_DIR` (or `$XDG_DATA_HOME/marshal`) relocates the data
directory holding the trust store, model cache, and logs. Defaults remain
`~/.config/marshal` and `~/.local/share/marshal`.

## SDD plan workflow

Feature exploration may use the normal brainstorming workflow. An approved
design can become a Marshal executable plan through `/sdd new <goal>` or
`/sdd new --from-last-plan`. Marshal writes the candidate under the effective
`sdd.plans_dir`, inspects it before execution, and requires review before
`/sdd` starts. Executable blocks use adaptive execution by default; prose-only
plans retain the legacy agent strategy.

## Key layers

| Layer | Packages | Responsibility |
|-------|----------|----------------|
| Entrypoint | `cmd/marshal/` | Thin main that delegates to `internal/app`. |
| App shell | `internal/app/`, `internal/app/session/` | Dependency wiring, config, logging, signal handling, shared session state. |
| TUI | `internal/app/tui/` | Bubble Tea model, panels, settings, chrome, themes, help footer. |
| Agent | `internal/agent/`, `internal/agent/swarm/` | Single-agent loop and multi-agent swarm orchestration. |
| Pipeline | `internal/pipeline/` | Plan execution with implementer/reviewer subagents. |
| LLM | `internal/llm/`, `internal/llm/routing/` | Provider abstraction, model presets, role-based routing. Assistant messages carry provider-bound reasoning (`schema.ChatMessage.Reasoning`) that each adapter replays in its own wire format. |
| Tools | `internal/tools/registry/`, `internal/tools/native/`, `internal/tools/patch/`, `internal/tools/policy/`, `internal/tools/mcp/` | Tool registration, file/search/shell/git tools, patch apply, risk policy, MCP clients. The model-facing surface is documented family-by-family in [`docs/tools/`](tools/README.md). |
| Repo intelligence | `internal/repo/`, `internal/index/`, `internal/retrieval/`, `internal/lsp/` | Repo scanning, tree-sitter symbol extraction, embeddings, semantic retrieval, LSP integration. |
| Knowledge | `internal/contextpack/`, `internal/rollover/` | Curated memory injection, context budget, long-session rollover. |
| Persistence | `internal/db/`, `internal/snapshot/`, `internal/filetrack/` | SQLite schema, workspace snapshots, file access tracking. |
| ACP | `internal/acp/` | Headless JSON-RPC server for editor/IDE integration. |
| Web bridge | `web/bridge/`, `cmd/webbridge/` | Fleet control plane: supervises one `marshal acp` child per project, brokers approvals, serves the REST API and SSE streams, and drives per-agent worktree isolation, diff, merge and discard entirely over ACP. |
| Web UI | `web/ui/` | Svelte 5 + Vite + TypeScript SPA: fleet dashboard, spawn composer, and per-agent chat (dev-only Node toolchain; build output is embedded by `web/bridge`). |

## Data flow of a single turn

```
User input
    ↓
TUI → session.State
    ↓
Agent loop builds context pack and prompt
    ↓
LLM layer routes to configured provider/model
    ↓
Streaming response parsed for text and tool calls
    ↓
Tool calls classified, approved, and executed
    ↓
Results streamed back to TUI transcript
    ↓
State persisted to SQLite
```

## Prompt layout and caching

Every request is laid out so that provider prompt caches can reuse
everything except what changed since the previous request:

```
[0] system prompt               stable within a session (rebuilt only when skills,
                                loaded tools or system access change; the agent
                                roster's discovered models are snapshotted per session)
[1] context pack (stable part)  curated memories, plan, pins
    skill bodies
    replayed history            ← CacheAnchor: end of the cross-turn stable region
    goal, then the turn's tool loop (append-only)
[n] working state (Volatile)    todos, semantic snippets, tool output; rebuilt
                                for every request, never stored
```

The working state is appended in `chatOnceAttempt`, the single dispatch
point. Everything up to the `CacheAnchor` opens the next turn's first
request unchanged (`TestCrossTurnStableRegionIsAppendOnly`; not on plan-first
turns: the refreshed plan rewrites the stable pack); within a turn
the prefix only grows (`TestPromptPrefixStableAcrossSteps`). Anthropic
places cache breakpoints on the tools, the system prompt, the anchor and
the last non-`Volatile` block. OpenAI and Codex requests carry a
session-derived `prompt_cache_key`. In native-tools mode, tools-disabled
calls (finalize, handoff summary, plan) keep the tool definitions with
`tool_choice: none`. On automatic-prefix providers they then share the
loop's whole prefix; on Anthropic, changing `tool_choice` invalidates the
cached messages, so they reuse the tools and system prompt only. A provider
that rejects `none` (or whose model calls a tool anyway) is switched to
tool-less calls for the session. Loading a deferred tool with `tools.select` costs
one cache miss, because tools head every provider's prefix. Design:
`docs/prompt-caching/design.md`, `docs/prompt-caching/followups-design.md`.

## Web UI dependency rules

- `web/bridge/` and `cmd/webbridge/` are pure Go and embed only the compiled
  assets from `web/bridge/static/` via `//go:embed`. The Go module graph does
  not depend on Node.
- `web/ui/` is a standalone Node project used only at development/build time.
  Running `npm run build` in `web/ui` writes hashed static assets into
  `web/bridge/static/`; the next `go build ./cmd/webbridge` embeds them.
- During pure-Go development, `web/bridge/static/` contains a placeholder
  `index.html` so `webbridge` still serves a useful landing page before the
  SPA is built.

## Background command execution

`shell.run` (and every ordinary shell-capable tool) launches through the
**conversation-owned command service** (`internal/tools/native`'s `JobManager`,
built by `internal/app` and installed on the session with
`State.SetTurnStopService`). One service per conversation owns process
lifetime; the tool call only *waits* for it. Design:
`docs/background-execution/feature-spec.md`.

- **Wait-only arguments.** `timeout_seconds` bounds how long the call waits,
  never how long the command may run. Omitted or zero uses
  `tools.shell.foreground_wait_seconds` (default 10); a positive value is
  honoured exactly; a negative value is rejected before anything launches.
  Elapsed wall-clock time never terminates a process.
- **Attachment, not two implementations.** A foreground command that outlives
  its wait transitions to background attachment on the same process, PID and
  execution identity — no relaunch, no second approval, no working-directory
  change. `background: true` is the immediate-return form of the same launch.
- **Deprecated config key.** `tools.shell.default_timeout_seconds` still loads
  and saves for compatibility but drives nothing: it cannot override the
  foreground wait and is not an editable `/settings` row.
- **Broadened capacity.** `tools.shell.max_background_jobs` now means max
  concurrent *running commands*, counting foreground-attached and detached
  alike (`/settings` labels it `Max running commands`). Capacity is reserved at
  admission, so a handoff never needs a new slot and a lowered limit never
  evicts a running command.
- **Stop ownership.** An explicit stop names a *group*: an
  `agent`/TUI/ACP parent operation registers one with
  `State.BeginTurnStop`, and every command it launches records that turn as its
  `TurnID`. Stopping it is `State.RequestTurnStop(cause)`, which asks the
  service to stop the still-attached commands **before** the agent's waits and
  provider calls are cancelled. A command that already detached is
  conversation-owned and survives. Ordinary cleanup
  (`State.EndTurnStop(token)`) only unregisters, and only its own token.
  ACP applies the same rules headlessly: `session/cancel` stops the turn
  (`turn stop requested`), close/close-all/replacement stop the conversation
  (`conversation ended`), a request deadline ends only the wait, and no headless
  path consults a prompt.
- **Evidence.** The service writes a versioned `registry.CommandExecution`
  record into the tool notice: execution identity, conversation/actor/turn/call,
  cwd and config generation, PID, state vs attachment, effective wait, detach
  reason, accepted stop cause, optional exit code (nil means never observed —
  never success) and truncation. Detached completions are stored as
  `session.JobExit` records and persisted before their completion is announced,
  so a resume shows the outcome rather than a card that runs forever; a job
  whose owner is gone is shown *lost*, not running or completed. The record
  reaches clients through the ACP stack-detail (`CallDetail.execution`) and wire
  (`WireJob`) projections, and the postmortem report's additive
  `execution_evidence` section.
- **Exit phases.** Ending a conversation is a state machine on the session
  (`ExitPhase`: `open` → `stopping_work` → `postmortem` → `closing` →
  `closed`) with the runtime exposing `ActiveWork()`, `AcceptEnd`,
  `WaitForEnd` and `EndPhase()`. Accepting an end freezes ordinary admission at
  the work gate and the command service, then stops and joins exactly the
  membership the user was shown while the database is still open. Stopping work
  and tearing down infrastructure are separate steps, so the optional
  postmortem pass can still run — and it cannot reopen ordinary work. The
  noninteractive path (`Runtime.Quiesce`/`Close`) runs the same stop/join pass
  with `RequireDisplayed:false`, so a headless shutdown prompts nobody.

## Design principles

- **Local-friendly** — defaults assume no built-in providers; remote providers are opt-in.
- **Provider-flexible** — the LLM layer is swappable without TUI changes.
- **Tool-safe** — shell execution is risk-classified and approval-gated.
- **Transparent** — status bar and transcript show active model, route, context usage, and tool progress.
