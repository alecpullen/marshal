# AGENTS.md

Guidance for AI coding agents (Claude Code, Kimi Code, Codex, …) working in
this repository. Marshal loads the first of `AGENTS.md` / `CLAUDE.md` /
`GEMINI.md` / `.cursorrules` as repo-level instructions
(`internal/app/instructions.go`), so this file is part of the prompt.

## Project

**Marshal** is a local-friendly TUI coding agent: it understands a repository,
edits files, runs shell commands and tests, and keeps project context across
sessions — with local models as first-class citizens and any
OpenAI-compatible, Anthropic, or Ollama-native endpoint as a supported
backend. Go + Bubble Tea, cgo (tree-sitter), SQLite. Binary: `marshal`.

A separate fleet control plane (`cmd/webbridge` + `web/`) drives many agent
sessions over ACP from a browser.

## Commands

```bash
# Build. CGO_ENABLED=1 is required: tree-sitter (internal/repo/symbols.go)
# links a C library, so a C toolchain must be present.
CGO_ENABLED=1 go build ./cmd/marshal

go run ./cmd/marshal          # run the TUI
go test ./...                 # all tests
go test ./internal/app/...    # one package (prefer the narrowest scope)
go vet ./...
gofmt -w .
```

`marshal` also dispatches subcommands: `acp` (headless JSON-RPC server),
`history`, `plugin`, `snapshots`, `calibrate-tokens`. Top-level flags are
`--trust` and `--version`.

### Web control plane

```bash
cd web/ui && npm install && npm run build   # writes web/bridge/static/
go build ./cmd/webbridge                    # embeds the built assets
```

The SPA is baked in with `//go:embed`, so a change under `web/ui/src` is
**invisible until `npm run build` reruns and the binary is rebuilt**.

### End-to-end and usability suites

```bash
python3 scripts/e2e/run_e2e.py --base http://127.0.0.1:7700 --token TOK ...   # see scripts/e2e/README.md
go test ./test/usability/... -run TestScripted -v -timeout 5m
```

The usability harness execs a real binary: build `./marshal` at the repo root
first, or point `USABILITY_MARSHAL_BINARY` at one.

## Where things live

`docs/ARCHITECTURE.md` is the canonical narrative — high-level flow, project
identity, and the full layer table. Read it before this map. Below is the
short version, grouped by concern.

```
Runtime & orchestration
  internal/agent/          single-agent loop (runner, tools, protocol, budget)
  internal/agent/swarm/    multi-agent orchestration, lock, verdict
  internal/pipeline/       plan execution: implementer/reviewer subagents, build+test gate
  internal/acp/            ACP v1 transport (sessions, prompts, permissions)
  internal/viewmodel/      transcript tree (turn → step → row) + JSON projection, shared with ACP
  internal/worker/         lifecycle contract for supervised background workers
  internal/pubsub/         in-process typed event broker
  internal/watch/          background watches (command/job/file sources)
  internal/worktree/       agent and pipeline worktrees: seed, list, finish
  internal/postmortem/     per-session harness-friction report

App shell & config
  internal/app/            Run(), dependency wiring, signal handling, repo instructions
  internal/app/config/     TOML loading, defaults, merge rules
  internal/app/session/    shared mutable state between TUI and agent
  internal/trust/          folder-trust store, resolver, project-config hashing
  internal/credentials/    OS-keychain secret store
  internal/oauth/          OAuth 2.1 + PKCE client (loopback, DCR)
  internal/workspacecfg/   sandbox workspace TOML: parse, validate, patch, render
  internal/testenv/        test helpers immune to ambient XDG_*/home env
  internal/pathutil/       path resolution helpers

TUI (rendering only)
  internal/app/tui/        Bubble Tea model; model.go is the hub
  internal/app/tui/{dock,sessionsheet,inspector,docpanel,listpanel,chrome,layout,picker,fuzzy,glyph,theme,help}
                           shared panel host, session sheet, browse inspector, list engine, dressing
  internal/app/tui/{settings,connect,agents,memory,skills,plugins,mcpauth,modeloptions,presetflow,probe,doctorpanel}
                           interactive surfaces for config, providers, roles, MCP, models
  internal/app/tui/{sddreview,castlist,gatepanel,postmortempanel,trustpanel,changedfiles,gitinfo,liveregion,textfield}

Models
  internal/llm/            provider abstraction (OpenAI-compatible, Anthropic, Ollama-native, Codex)
  internal/llm/provider/{limits,modelcache,oauthworker}   limit discovery, model-list cache, token refresh
  internal/llm/routing/    route resolver, model presets, role profiles
  internal/llm/{schema,streaming,embedding,pricing,catalog}

Repo intelligence
  internal/repo/           scanner, hashing, gitignore, repo map/card, tree-sitter symbols
  internal/index/          chunking, embedding indexer, file watcher
  internal/retrieval/      semantic retrieval over embeddings
  internal/lsp/            LSP client/manager with symbol, query, diagnostics adapters
  internal/diagnostics/    configurable per-language checkers

Context & knowledge
  internal/contextpack/    context pack builder and budget logic
  internal/rollover/       context-window rollover for long sessions
  internal/knowledge/      durable project memory agent
  internal/skills/         skill-based instruction sets (builtin/ ships the defaults)
  internal/commands/       slash commands (/plan, /sdd, /swarm, /settings, …)
  internal/history/        generation listing, transcript dump, archived-turn search
  internal/{export,redact,diffview}   HTML export, secret masking, styled unified diffs

SDD plan authoring
  internal/sddauthor/      converts an approved design into one reviewed executable plan
  internal/sddplans/       discovers plan files a /sdd run can execute

Persistence
  internal/db/             SQLite project/session persistence (migrations.go owns the schema)
  internal/snapshot/       git-backed workspace snapshots, bounded storage, rollback
  internal/filetrack/      per-session file read/write timestamps

Tools & safety
  internal/tools/registry/  tool registration and dispatch
  internal/tools/native/    file, search, shell, git, repo, symbols, jobs, scratchpad, recall
  internal/tools/{patch,policy,mcp,desktop}
  internal/permissions/     tool permission rules, approval-pattern derivation
  internal/{hooks,plugins}/ user hooks and third-party plugin loading
  internal/sandbox/         restricted, container, and passthrough execution backends
  internal/{jsonextract,strutil}

Web control plane
  cmd/webbridge/           HTTP + SSE server supervising `marshal acp` children
  web/bridge/              bridge subsystems (see the boundary rule below)
  web/ui/                  Svelte 5 + Vite SPA (dev-only Node toolchain)
```

This tree is complete: several subsystems that sound like they still need
writing already exist (the docked-panel host, the session sheet, the provider
connect flow, the plan-execution pipeline, worktree isolation). Check the map
and `docs/ARCHITECTURE.md` before building one.

## Design constraints

These are load-bearing; code comments and tests point back at them.

- **`web/` is standard library only.** Everything under `web/` is an external
  ACP client: it must not import `marshal/internal/...` or any third-party
  module. `TestWebIsStdlibOnly` (`web/bridge/boundary_test.go`) enforces this
  in CI. If it fails, do not add an exception — either the code belongs on the
  agent side of ACP, or the data belongs in the JSON contract. Extend the
  contract; do not share a Go type.
- **The TUI renders only.** No routing, policy, or prompt logic in
  `internal/app/tui/`. It talks to the agent loop through the structural
  `AgentRunner` interface so it never imports `internal/agent`.
- **Tool-safe.** Shell execution is risk-classified and approval-gated; file
  writes prefer `file.write` / `file.write_patch` because they alone give diff
  review, backups, and rollback.
- **Local-friendly.** Defaults assume no built-in providers; remote providers
  are opt-in and must be configured explicitly. Never assume a hosted model.
- **Provider-flexible.** The model layer is swappable without TUI changes.

## Config

Merged in order, later wins:

1. Built-in defaults (`config.Default()`)
2. `~/.config/marshal/config.toml`
3. `.marshal/config.toml` (project-local)

`[providers]` and `[models.presets]` are **user-global only**. Project configs
never carry them; every editing surface saves them to the user config, and a
trusted project file that still has them is hoisted on load (a conflicting
entry stays project-local with a deprecation diagnostic).

`MARSHAL_CONFIG_DIR` / `XDG_CONFIG_HOME` relocate the config dir;
`MARSHAL_DATA_DIR` / `XDG_DATA_HOME` relocate the data dir (trust store, model
cache, logs). Project-scoped state anchors at the **git repository root**, not
the launch directory, so launching from a subdirectory lands in the same
project.

## Testing notes

- Most packages need cgo, so run the suite with a C toolchain available.
- `app.Run()` takes functional options (`app.go`) so tests inject fakes
  instead of starting a real TUI — `WithConfigLoader`, `WithProgramRunner`,
  `WithNow`, and nine others. `app_test.go` uses this pattern exclusively;
  prefer it over driving a real program.
- `internal/testenv.SanitizeXDG` neutralises ambient `XDG_*` vars in
  `TestMain`. Any new test that injects a temp home into config resolution must
  live in a package that calls it, or GitHub runners will silently redirect it.
- Tests that need a specific context window must inject it through the
  production path (a scripted `routing.RouteResolver`); `RunTask` overwrites
  the window from the resolved route.
- CI: `usability.yml` runs only `./test/usability/...`; the full `go test ./...`
  suite runs in `release.yaml` on tag pushes. Full-suite regressions therefore
  surface at release time — run the suite locally before assuming a change is
  clean.

## Specs and plans

Feature design docs and phase specs live in the public tree under
`docs/<feature>/` (for example `docs/single-stack/` and `docs/web-studio/`).
Commit them with the work they describe and keep them current when decisions
change — reviewers should not flag them as violations.

Historical specs, scratch execution plans, and drafts that are not meant to be
published go in `.docs-archive/` (gitignored).
