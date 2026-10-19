# Marshal daemon and detached TUI — design

Status: direction agreed 2026-10-19. P0 is specified and implemented (§5,
§7). P1 is specified ([`p1-project-host.md`](p1-project-host.md)) and
implemented (see its implementation record). Later phases have no spec
yet. The decision to keep the TUI in Go depends on P0 (text selection, §5)
working.

## 1. Goal and scope

**Outcome.** Marshal keeps running when no terminal is attached. It keeps
pinned projects' indexes and embeddings current. It can run on another
machine. One TUI drives both the fleet and a single agent, and it can
detach and reattach without stopping work.

**Decided**

| Question | Decision |
|---|---|
| Where does code live when the server is remote? | On the server. Agents work on server-side checkouts. There is no laptop-to-server file sync. |
| Does plain `marshal` keep working without a daemon? | Yes, for now. Embedded mode stays (§4.5). |
| Which projects stay warm? | Pinned projects only (§4.3). |
| Ink/TS or Go for the TUI? | Go (Bubble Tea v2), if P0 works. Reasons in §3. |
| Layout of the unified TUI | A fleet screen (agents, needs-you). Attaching to an agent opens the existing single-stack session view, with a needs-you count in the status line (§4.6). |

**Non-goals.** File sync or a remote filesystem for laptop checkouts.
Multi-user operation beyond the `OwnerID` already in the bridge's data
model. Replacing the web UI. Weakening the containerized server's security
model: credentials still never enter an agent container, and git stays
bridge-mediated.

## 2. Where the code is today

- **A socket listener already exists.** `marshal acp --listen` serves ACP
  on a socket, and sessions belong to the host, so a client can hang up and
  reconnect without losing them (`internal/acp/listen.go:19`). It serves
  one connection at a time (`listen.go:68`).
- **Every session builds a full runtime.** `SessionManager.startRuntime`
  (`internal/acp/session.go:357`) calls `app.StartRuntime` for each
  session. Each `app.Runtime` owns its own database handle, LSP manager,
  MCP manager, snapshot store and skill index. Three sessions on one repo
  start three of everything.
- **Headless sessions never index.** The index watcher and the OAuth
  refresh workers start only in `Run()`, the TUI entry point
  (`internal/app/app.go:2902-2910`). `StartRuntime` creates the index
  broker but no watcher, so ACP sessions and fleet agents never refresh
  embeddings.
- **The TUI is part of the runtime, not a client of it.** It calls 82
  distinct `session.State` methods outside tests. It also imports config,
  db, credentials, oauth and the tool registry directly. The non-test code
  is about 37k lines.
- **The transcript has one entry point.** The TUI turns state into a
  `viewmodel.Snapshot` and calls `viewmodel.Build`
  (`internal/app/tui/viewport_render.go:73`). ACP calls the same function
  (`internal/acp/stack.go:206`) and projects the result to a `WireTree`.
- **The bridge exposes only part of ACP.** ACP has about 70 `session/*`
  methods plus `config/*`. The bridge has about 12 session routes. It
  brokers only `session/request_permission` and `session/request_question`
  from agents (`web/bridge/registry.go:403`).
- **Secret prompts are TUI-only.** The TUI can show a masked secret prompt,
  and ACP cannot, so `shell.sudo` refuses on headless paths
  (`app.go:2920`).
- **The bridge can already run agents on the host.** With no container
  runtime it starts agents as host processes (`web/bridge/fleet.go:393`).
  It does so with `os.Environ()`, which in a service is the service's
  environment, not the user's (§6.1).

## 3. Why Go rather than Ink

- **Text selection does not depend on the framework.** It fails because the
  TUI uses the alternate screen with mouse capture (`tui/view.go:51-69`).
  Ink has the same two options. In the alternate screen it has the same
  problem. Inline `<Static>` output gives native selection, but text
  cannot change once printed, and the single-stack design relies on
  changing it (folding tasks, expanding rows, density levels, browse
  mode). The fix in either framework is selection handled by the app
  (§5).
- **Porting is much cheaper in Go.** Every transcript passes through
  `viewmodel.Build`, so staying in Go keeps the rendering code and changes
  only where the snapshot comes from. An Ink client would rewrite all 37k
  lines, including recent single-stack and live-work work.
- **Ink would share less code than it seems.** Components get rewritten
  for Ink either way. The shared part would be `web/ui/src/lib`: about
  2.8k lines, half of them fetch wrappers in `api.ts`. The real logic in
  `fleet.ts` and `inbox.ts` is about 300 lines. §4.4 removes the
  duplication from the server side instead.
- **Performance does not decide it.** A virtualized Ink client would be
  usable. But Bubble Tea v2 diffs cells, while Ink reconciles and lays out
  the whole tree on each update. Ink also uses more memory, starts slower,
  and needs Node or a large compiled bundle to ship.

## 4. Architecture

```
 clients                       daemon (bridge)                   project hosts
┌───────────────────┐  HTTP   ┌────────────────────────────┐    ┌──────────────────────────┐
│ marshal (Go TUI)  │ ──SSE──▶│ webbridge                  │ACP │ marshal acp  (per tree)  │
│  fleet screen     │  unix / │  fleet registry, projects  │───▶│  ProjectHost: db, index, │
│  session screen   │  TLS    │  broker: perms, questions, │    │   embedder, LSP, snaps,  │
├───────────────────┤         │   secrets                  │    │   skills                 │
│ web SPA (Svelte)  │ ──────▶ │  /api/sessions/{id}/rpc    │    │  Sessions: state, runner,│
└───────────────────┘         └────────────────────────────┘    │   jobs, brokers, MCP     │
                                                                └──────────────────────────┘

 embedded mode (no daemon)
┌──────────────────────────────────────────────┐
│ marshal: Go TUI ⇄ io.Pipe ⇄ in-process ACP   │
│          host (same ProjectHost + Sessions)  │
└──────────────────────────────────────────────┘
```

### 4.1 The bridge is the daemon

There is one server, in two deployment profiles:

| | Local profile | Server profile |
|---|---|---|
| Runs as | launchd agent / systemd user unit | Container (`docs/DEPLOYMENT.md`) |
| Agents | Host processes in the user's checkouts | Containers on the state volume |
| Client listener | Unix socket, mode `0600`, in the data dir | TCP with `--tls-cert`/`--tls-key` |
| Auth | Socket permissions | Bearer token (existing) |
| Agent environment | Resolved per project (§6.1) | Explicitly supplied (existing rule) |

The `web/` boundary is unchanged. The bridge imports no `internal/`
package and only speaks ACP. Merging the bridge into `marshal` is rejected
because that process boundary is what keeps credentials out of agent
containers.

### 4.2 Project host and session split

Spec: [`p1-project-host.md`](p1-project-host.md). Where this sketch and
the spec differ, the spec wins.

`app.Runtime` splits into two parts:

- **ProjectHost**, one per project root in a process (the root from
  `repo.FindRoot`, so a git worktree opened directly is its own project).
  It sets up the database once (migrate, reconcile), owns the index
  workers and the embedder, and pools language servers by active root, so
  sessions in the same worktree share them too. A lock file in
  `.marshal/` lets one process at a time index a tree.
- **Session**, many per ProjectHost. It owns `session.State`, its own
  database connection, the runners, the job manager and command service,
  the event brokers, MCP, the snapshot service and the skill index. MCP
  stays per session because some MCP servers keep per-session state. The
  snapshot service follows the session's active root, and skills are
  cheap to load.

The index stays one per project root: an isolated session searches the
root's index, and worktree indexes are a later phase. OAuth refresh
workers stay in the TUI until the daemon runs them once (P4): every fleet
agent is its own process, and refreshing in each would race token
rotation on one grant.

`StartRuntime` becomes "get or create the ProjectHost, then start a
Session on it". Index workers start from the ProjectHost on every
production path, so headless sessions index too. A `marshal acp` process
may host several projects, each with its own ProjectHost. Keeping one
project per process, so a panic in one project does not stop the others,
is a bridge routing choice (P4).

The bridge stays the only client of a project host. `listen.go`'s limit
of one connection is fine, because fan-out to many viewers already happens
in the bridge's SSE layer.

### 4.3 Pinned projects

The pin is a project setting in the bridge's workspace registry
(`web/bridge/projects.go`). A ProjectHost stays alive while its project is
pinned or has at least one session. Its watcher runs for its whole life.
Unpinned projects index when a session starts, as the TUI does today, and
shut down with their last session. Language servers are stopped after an
idle timeout even on pinned projects; the index and embeddings stay
current without them. Embedding calls are debounced and batched per
project.

### 4.4 Contract

- **Client to agent:** `POST /api/sessions/{id}/rpc` with `{method,
  params}`, checked against an allowlist of ACP methods. The allowlist is
  where the bridge decides what clients may call. Without the passthrough,
  matching the TUI's features would take about 60 new hand-written routes.
  Existing typed routes stay, and `api.ts` can wrap the passthrough.
- **Agent to client:** the session SSE stream carries ACP notifications.
  Agent requests stay brokered by the bridge, so every viewer sees them,
  the first answer wins, and the others see the request settle. The
  brokered set gains `session/request_secret` (§6.3).
- **Fleet:** the existing REST routes. Their request and response types
  move into one standard-library-only package under `web/bridge/` that
  both the bridge and the Go TUI import, and the TypeScript types are
  generated from it. The boundary rule only forbids `web/` importing
  `internal/`.
- **Server-computed views:** agent grouping and urgency ordering
  (`fleet.ts` `groupAgents`) and the needs-you list (`inbox.ts`
  `buildInbox`) move into bridge responses, so both clients render the
  same thing.

### 4.5 TUI client seam

The TUI talks to one ACP client interface: call a method, receive
notifications, answer agent requests. It has two transports:

- **Pipe** (embedded mode): an in-process ACP host connected with
  `io.Pipe`. `acp.NewServer` already takes a reader and a writer
  (`internal/acp/server.go:119`).
- **Bridge** (daemon mode): the RPC passthrough plus the session SSE
  stream, over the unix socket or TLS.

Fleet features need the bridge transport. Embedded mode shows only the
session screen.

During migration, an in-process adapter covers the 82 `session.State`
calls behind the same interface. Callers move over in groups, and nothing
breaks in between:

1. transcript snapshot inputs
2. answers to agent requests (approval, question, sudo, skill gate, access)
3. turn control (steer, cancel, mode, effort)
4. config reads and writes (`config/*`)
5. local notices and command output (`AddMessage` and its variants)
6. the three direct database reads

When every group runs over ACP, embedded mode switches to the pipe and the
adapter is deleted. The protocol then cannot drift from what the TUI
needs, because the TUI has no other path.

### 4.6 Screens

- **Fleet screen:** the agent list and needs-you list from the bridge,
  plus spawn, finish, merge and discard. The screen, key and notification
  rules from `docs/fleet-cli/design.md` §3–§4 carry over. Its Ink client
  (§2 of that doc) is superseded.
- **Session screen:** the current single-stack view, unchanged apart from
  where its data comes from. In daemon mode the status line shows a
  needs-you count for other agents.
- **Attach and detach:** leaving a session screen or quitting the TUI in
  daemon mode never stops the agent. In embedded mode, quitting ends the
  conversation through the existing exit phases, as today.

## 5. Text selection handled by the app (P0)

Spec: [`p0-text-selection.md`](p0-text-selection.md). Where this sketch
and the spec differ, the spec wins. It anchors to a block key, a row and a
column rather than to a content line, and it defines exactly what copied
text contains.

Selection is drawn and copied by the TUI, not the terminal. It is
anchored to transcript content, not screen cells, so it survives scrolling
and streaming output.

- **Input.** Cell-motion mouse mode (already on) reports drag motion. A
  click starts a selection, motion extends it, and release ends it.
  Dragging past the top or bottom edge scrolls the transcript.
  `contentLineForClick` (`tui/click.go:35`) already maps screen rows to
  content lines.
- **Anchor.** v1 anchors to content line and column. A re-layout (resize,
  or expanding a node above the selection) clears it. A later version can
  anchor to node ID and text offset.
- **Render.** The selected cell range is drawn in reverse video when
  visible lines are composed, using `charmbracelet/x/ansi` for
  escape-aware cutting.
- **Copied text.** The renderer records soft-wrap boundaries and each
  row's gutter width. Copied text joins soft-wrapped lines and leaves out
  gutter glyphs, the now bar and the dock.
- **Copy.** Copy happens on release (the tmux/zellij convention). It goes
  through `internal/app/clipboard` locally and through `tea.SetClipboard`
  (OSC 52) over SSH. A detached TUI runs on the user's machine, so the
  local path is the usual one.
- **Kept.** Ctrl+S still releases the mouse for native selection.
- **Caveats.** Terminal.app does not support OSC 52 (the local clipboard
  covers it). iTerm2 needs clipboard access enabled, and tmux needs
  `set-clipboard on`.

P0 is done when you can drag-select across a scroll boundary while a turn
streams, and the copied text matches the transcript's plain text.

## 6. Daemon concerns

### 6.1 Shell environment

A service does not inherit the user's shell environment: PATH from
nvm/mise/asdf, direnv, `SSH_AUTH_SOCK`, exported keys. Without a fix,
agent commands run against the wrong toolchain and nothing reports an
error. In the local profile, the ProjectHost resolves the environment once
per project by running the user's login shell in the project directory,
plus `direnv export` when an `.envrc` is present. It caches the result and
invalidates it when those files change. The server profile keeps its rule
that environments are supplied explicitly.

### 6.2 Config and trust

Today, config and the trust hash are checked when a runtime starts. A
daemon never relaunches, so the checks happen at session start, and the
ProjectHost watches the user config and `.marshal/config.toml`. A changed
project config marks the project untrusted until the next session start
re-prompts. The prompt goes to whichever client starts the session.

### 6.3 Flows that need the user

- **Secret prompt (sudo).** A new brokered request,
  `session/request_secret`. The answer travels only on the request's
  response. It is never put on an SSE stream or the replay ring, and never
  logged. It is allowed over the unix socket and over TLS. In the server
  profile it is the server's sudo.
- **OAuth loopback logins.** These work in the local profile. In the
  server profile the redirect would land on the server's loopback address,
  so those flows need a device-code grant or a callback forwarded by the
  client. Open question (§8).
- **Provider credentials** are stored in the keychain of the machine the
  daemon runs on. A launchd user agent can reach the login keychain. A
  headless Linux server usually has no Secret Service. Open question
  (§8).

### 6.4 Upgrades and version skew

Replacing the binary restarts every project host. The daemon drains first:
it refuses new turns, waits for running turns up to a limit, then
restarts, and sessions come back through `session/resume`. Clients compare
their version with the daemon's (the bridge build version and ACP
`agentInfo.version`) and warn on mismatch.

### 6.5 Embedded mode next to a daemon

Embedded `marshal` checks whether a daemon is hosting the project. If it
is, it offers to attach rather than open a second runtime on the same
database. SQLite with WAL already tolerates two processes, as the TUI and
an ACP child do today, so this is about duplicated work and confusing
state, not corruption.

## 7. Phases

| Phase | Goal | Done when |
|---|---|---|
| P0 | Selection handled by the app (§5) — implemented; manual terminal check pending (see the spec's implementation record) | The P0 check in §5 passes in the current TUI |
| P1 | ProjectHost/Session split and headless indexing (§4.2) — implemented; see the spec's implementation record | Two ACP sessions on one repo in one process share one host, one set of language servers and one watcher. With `indexing.watch = true`, a headless session's edit refreshes its embeddings. Two processes on one tree run one indexer |
| P2 | TUI client seam with the in-process adapter (§4.5) | The TUI calls no `session.State` method directly. Also settles the transcript-shape question (§8) |
| P3 | ACP client transports; embedded mode over the pipe | Embedded mode runs entirely over ACP and the adapter is deleted |
| P4 | Bridge local profile: user service, unix listener, RPC passthrough, `request_secret`, pinning, environment resolution, OAuth refresh run once, one `marshal acp` child per project for host-process agents | A TUI can quit and reattach to a running turn on the local daemon |
| P5 | Fleet screen in the Go TUI, shared types, server-computed views | The fleet loop from `docs/fleet-cli/design.md` §1 works from the Go TUI against local and server profiles |

P0 and P1 are independent of each other and of the later phases, and each
is useful on its own.

## 8. Open questions

- **Transcript shape on the wire.** Option one: send the
  `viewmodel.Snapshot` inputs, so `Build` runs in the client and the
  renderer is unchanged, at the cost of a second wire format next to the
  web UI's. Option two: send the `WireTree`, one format for both clients,
  but the renderer moves from `Node` to `WireNode`, and large nodes arrive
  truncated and are fetched when expanded. The lean is `WireTree`. A spike
  in P2 checks whether the projection drops anything the TUI renders.
- **MCP scope.** Should MCP servers stay per session, or move to the
  ProjectHost behind an opt-in for servers known to be stateless?
- **Headless credentials.** Where do provider credentials and OAuth
  tokens live on a server without a keychain? Options are the bridge's
  existing secrets backends (`secrets_local`, OpenBao) or a file store
  readable only by its owner.
- **Remote OAuth.** Should server-profile logins use a device-code grant
  or a callback forwarded by the client?
- **Local-profile auth.** Is socket permission enough, or should the local
  listener also require the token for defence in depth?
- **Bridge startup without containers.** Confirm that the egress, secrets
  and container subsystems stay inactive, and cheap, when no container
  runtime is present.
- **Service install.** How is the daemon installed (`marshal daemon
  install` writing the launchd/systemd unit?), and is it `webbridge` or a
  `marshal` subcommand that runs it? Both binaries already ship through
  `.goreleaser.yaml`.
