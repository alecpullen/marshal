# Changelog

All notable changes to Marshal are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- The verification reminder no longer treats every `shell.run` as an edit.
  Classification is now an explicit allowlist with two structural rules:
  patterns anchor to command-segment starts (`^`, `;`, `|`, `&`, newline,
  and the `(`/`{` group opener at a segment boundary), and quoted spans are
  stripped before both classifications — words in quotes are data in both
  directions (a "make" in a commit message can neither arm the gate nor
  satisfy it as a phantom verification).
  Mutating list (arms the gate, resets repeat streaks): `rm`-class
  destructive commands and `sudo`, `xargs` feeding destructive commands
  (flags tolerated: `xargs -0 rm`), `sed -i`/`--in-place` and `perl -i`
  (clustered flags tolerated: `sed -ni`, `perl -pi`), `curl` writing to a
  file (`-o`/`-O` in a flag cluster, `--output`, `--remote-name`) and
  `curl | bash`/`| sh`, git state changes (`checkout`/`switch`/`restore`/
  `reset`/`clean`/`rebase`/`merge`/`cherry-pick`/`revert`/`am`/`apply`/
  `rm`/`mv` and state-changing `stash` forms — `git stash list`/`show` and
  `git apply --stat`/`--check` stay neutral), `go generate`,
  `make`/`cmake`/`gradle`/`mvn` (dry-run forms like `make -n` and
  `--dry-run` are stripped first), docker `build`/`run`/`rm`/`rmi`/`kill`/
  `stop`/`compose` and `docker exec` only when the inner command is
  destructive (`docker exec -it c rm -rf /data` arms), `ssh` (not
  `ssh-keygen`), and redirection into files after stripping quoted spans,
  fd-numbered redirects (`2>`, `2>&1`, `>&1`), and `/dev/null` sinks.
  Verification list (checked first, always wins): `go test`/`vet`/`build`,
  `npm`/`pnpm`/`yarn test`, `pytest`, `cargo test`/`check`/`clippy`/`build`,
  `make test`/`check` and `gradle`/`mvn test`/`check`/`verify` — all with
  flag-tolerant patterns so `make -j4 test` and `mvn -q verify` satisfy the
  gate. Everything else — `git log`/`status`/`diff`, `grep`, `find`, plain
  `curl`, read-only `sed`/`awk`, `git commit`/`push`, `go mod tidy`,
  `gofmt`, installs, and unrecognized commands — is neutral: it neither
  arms nor satisfies the gate. `--help`/`-h` forms of mutating commands
  (`truncate --help`, `git stash --help`) are stripped and stay neutral.
  Models answering questions or doing research no longer get the "you
  made changes but have not verified them" nudge. The nudge text says
  "made changes".
  Known limitations (missed gate once is the cheap failure direction):
  prefix wrappers (`env`, `nohup`, `time`, `timeout`, `nice`, `FOO=bar`)
  and quoted code payloads (`bash -c 'echo hi > f'`, `eval 'rm x'`) stay
  neutral because the quote-stripper cannot distinguish data-quotes from
  code-quotes; `find -delete`/`-exec rm` and archive extraction (`tar`,
  `unzip`) stay neutral as unrecognized commands.

## [0.0.3-alpha] - 2026-09-02

### Added

**Web bridge — fleet control plane**
- `webbridge`, a fleet control plane for running many agents at once: it
  supervises one `marshal acp` child per project, brokers approvals, and
  serves the REST API, SSE streams, an MCP endpoint, and an embedded web
  UI. Ships as a second binary in the release archives, with the build
  version stamped into the fleet.
- Containerized agents: each agent runs in its own container with CPU and
  memory caps, no privileged mode, no host mounts, and a bounded spawn
  queue; agents can be paused and resumed.
- Agents survive bridge restarts: the bridge detaches on shutdown instead
  of stopping its agents, and reattaches on startup, restoring each
  agent's ACP session.
- Remote git sources: spawn agents against git remotes through a
  credential broker — secrets stay env-indirected and reach git only via
  `GIT_ASKPASS`, never the agent's tree — with a shared bare-mirror cache
  per repo and bridge-side git hardened against planted hooks.
- An exit path for agent work: commit, a build-and-test verify gate
  (`session/verify`), push with a forge-created pull request, or export as
  a patch series for read-only agents. A failed or skipped gate blocks the
  push unless an override with a reason is recorded; the agent drafts its
  own commit message when the operator supplies none.
- MCP intake: a `/mcp` JSON-RPC endpoint with per-client bearer tokens
  (the shared API token is deliberately rejected), six tools — spawn,
  status, result, send, cancel, list — scoped per client, and an
  origin-tagged intake seam: submissions queue for operator confirmation
  under per-client concurrency and daily caps and a registered-repo
  allowlist. Approved plans land inside the agent's workspace.
- Forge integration: GitHub, Gitea, and Forgejo clients; rich pull
  requests created through the forge API; an issue watcher that spawns
  agents from labelled issues (deduplicated, with backoff); oversize
  repos refused before cloning.
- Limits and audit: an append-only audit log with rotation, surfaced as a
  fleet activity feed; cached disk accounting; reference-counted pruning
  of unreferenced state; configurable concurrency and disk limits.
  `webbridge` serves TLS directly when given a certificate and key.
- Deployment: a bridge container image with all state on one named volume
  (`compose.yaml`, `docs/DEPLOYMENT.md`), a marshal agent image
  (`docs/AGENT-IMAGE.md`), and derived per-project images declared via
  `.devcontainer/devcontainer.json` when a project needs a toolchain.
- Web UI: a Svelte 5 single-page app embedded in the bridge — fleet
  dashboard, spawn composer, per-agent chat with markdown rendering and
  real dialogs, confirmation queue, MCP client management, forge
  registration with an issue picker, and an activity feed, under a
  persistent navigation sidebar.

**ACP**
- `marshal acp --listen unix://…|tcp://…` serves the protocol over a
  socket with reattachable sessions: clients may disconnect and reconnect
  without losing sessions, which is what lets a containerized agent
  survive a control-plane restart.
- `initialize` reports the agent version so the bridge can detect image
  skew.
- Turn failures surface the actual provider error to ACP clients instead
  of an opaque internal error.

**Agent tooling**
- `agent.kill` cancels a running background subagent; `agent.output` peeks
  at its status, report, and transcript without waiting.
- `todo.write` gains a `drop_unfinished` escape hatch for replacing the
  carried-over todo list wholesale.
- The model roster in the system prompt includes discovered models from
  provider probes, annotates presets with context size, locality,
  thinking support, and price facts, and adds model-selection guidance
  for subagent dispatch.

**TUI**
- The status footer shows the thinking-effort state for effort-capable
  routes; a preset effort silently dropped by a non-reasoning backend
  logs a warning, and opt-in `MARSHAL_WIRE_CAPTURE_REQUESTS` captures
  outbound request bodies for verification.
- The side rail follows the drilled-in subagent, and the breadcrumb shows
  the dispatched role.
- `/trust` returns an informative panel, and a pending trust decision
  opens the trust panel.

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

**Agent**
- Context budgets scale down for small-window local models: tool-result
  caps, compaction thresholds, and pack/history floors derive from the
  window instead of flat values that left 16k models one tool call from
  compaction. The tool-definition block is counted in token estimates,
  rollover defaults on for local routes with windows of 32k or less, and
  local routes get a default tool-iteration ceiling so a weak model
  cannot run away.
- Narration — the prose a model emits alongside tool calls — renders as
  full markdown, uncapped, like the final answer; differentiation is the
  gutter glyph only.
- `file.read` and `file.page` accept absolute paths that resolve inside
  an allowed root (additional directories); write tools still require
  relative paths.

**Skills**
- `skills.autoload` now defaults to `["using-skills"]` so skill activation
  works out of the box, including for users with no embedding model. Opt out
  with `skills.autoload = []` or the `/skills` panel toggle.

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

**Agent and providers**
- Native-mode tool calls are salvaged from non-JSON prose grammars —
  Hermes-style XML, mangled tool names, and tool-name-keyed JSON — and
  verified against the registry; live diagnostics went from zero tool
  executions to tools executing in every tool-requiring scenario.
- Chat templates that require the system message first (stock qwen3 on
  llama.cpp/LM Studio) no longer fail every turn: Marshal retries once
  with trailing system messages demoted to user, handling both the HTTP
  500 and the embedded-error-event rejection forms.
- The welcome banner renders on fresh sessions again; autoloading
  `using-skills` had suppressed it by writing boot items into the
  transcript.
- A data race in the pipeline's CLI command runner on context cancel is
  fixed, along with worktree cwd/path handling, a transcript click
  off-by-one, and panel alignment.

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

[Unreleased]: https://github.com/alecpullen/marshal/compare/v0.0.3-alpha...HEAD
[0.0.3-alpha]: https://github.com/alecpullen/marshal/compare/v0.0.2-alpha...v0.0.3-alpha
[0.0.2-alpha]: https://github.com/alecpullen/marshal/compare/v0.0.1-alpha...v0.0.2-alpha
[0.0.1-alpha]: https://github.com/alecpullen/marshal/releases/tag/v0.0.1-alpha
