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
| LLM | `internal/llm/`, `internal/llm/routing/` | Provider abstraction, model presets, role-based routing. |
| Tools | `internal/tools/registry/`, `internal/tools/native/`, `internal/tools/patch/`, `internal/tools/policy/`, `internal/tools/mcp/` | Tool registration, file/search/shell/git tools, patch apply, risk policy, MCP clients. |
| Repo intelligence | `internal/repo/`, `internal/index/`, `internal/retrieval/`, `internal/lsp/` | Repo scanning, tree-sitter symbol extraction, embeddings, semantic retrieval, LSP integration. |
| Knowledge | `internal/knowledge/`, `internal/contextpack/`, `internal/rollover/` | Durable project memory, context budget, long-session rollover. |
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

## Design principles

- **Local-friendly** — defaults assume no built-in providers; remote providers are opt-in.
- **Provider-flexible** — the LLM layer is swappable without TUI changes.
- **Tool-safe** — shell execution is risk-classified and approval-gated.
- **Transparent** — status bar and transcript show active model, route, context usage, and tool progress.
