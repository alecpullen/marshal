# webbridge

The fleet control plane. Serves the REST API, the SSE streams, the MCP
endpoint, and the Studio.

```bash
webbridge --project /path/to/repo --web-listen 127.0.0.1:7700
```

The Studio's files are a directory beside the binary, `share/marshal/studio`,
not an embed: a rebuilt Studio is served at once, with no relink and no
restart. They are looked for in `<binary dir>/share/marshal/studio`, then
`<binary dir>/../share/marshal/studio` — so a checkout's `./webbridge` finds the
build at the repository root, and an installed one finds it under its prefix.
The binary's symlinks are resolved first, and the first directory holding an
`index.html` wins. `--studio-dir DIR` overrides the search; a `DIR` without an
`index.html` is refused at startup. A daemon that finds nothing still serves its
API, and the Studio's routes answer 503 with a page naming every candidate it
looked in (one-daemon D12). `GET /api/daemon` reports the resolved directory as
`studioDir`.

The web listener is off unless you ask for it. It takes `unix` (a `web.sock`
beside the daemon's own), an absolute socket path, or a loopback `host:port`,
and refuses anything else: it serves the Studio and the API with no
authentication of its own, so the operator's proxy in front of it is the only
thing authenticating a browser (one-daemon D3, D13).

| Flag | Meaning |
|---|---|
| `--web-listen` | the web listener: `unix`, an absolute socket path, or a loopback `host:port` (default: none) |
| `--studio-dir` | the Studio's files, and must hold an `index.html` (also `WEBBRIDGE_STUDIO_DIR`; default: `<binary dir>/share/marshal/studio`, then `<binary dir>/../share/marshal/studio`) |
| `--web-group` | share a unix `--web-listen` with this group (`0660`) |
| `--preview-addr` | loopback address for previews (default: a free port on `127.0.0.1`; `off` disables) |
| `--preview-url` | the public origin preview links name, for a proxy that publishes previews |

`--addr` and `--token-file` are retired and ignored, and `--token`, `--tls-cert`
and `--tls-key` are refused.

## The daemon

`webbridge` has one shape: the user's own daemon (one-daemon D1). It listens on
a unix socket, and its agents are host processes unless they have a workspace,
which runs them in a container. It serves no TLS itself (D3): a certificate, and
whatever admits the browser, are the proxy's business, not the daemon's.

```bash
webbridge --socket ~/.local/share/marshal/daemon/marshal.sock --web-listen unix
```

`--profile` is retired; `local` is ignored and `server` is refused.

- `--socket <path>` names the listener (also `WEBBRIDGE_SOCKET`). Its default
  is `<data>/daemon/marshal.sock`, where `<data>` is `MARSHAL_DATA_DIR`, else
  `$XDG_DATA_HOME/marshal`, else `~/.local/share/marshal`. The directory is
  created `0700`, and the socket is `0600` and peer-checked by user ID, so it
  needs no token.
- `--web-listen unix` puts the web listener's `web.sock` beside the daemon's own,
  mode `0600`. An absolute socket path with `--web-group` shares it with a
  group (`0660`) instead. `--preview-addr off` disables the preview listener,
  which otherwise opens whether or not the web listener does.
- The web listener's bind outcome is reported, not swallowed: `GET /api/daemon`
  answers `webListen` (the address actually bound, so a configured port `0`
  names its own) and `webError` (why nothing bound). A busy web port never
  stops the daemon — the socket is the daemon's real interface — but the
  failure is logged at error level and reported by name, instead of surfacing
  as "nothing is serving".
- The state directory defaults to `<data>/daemon/state` and the fleet registry
  to `<data>/daemon/fleet.json`, so a daemon and a bridge run by hand never
  share either.
- `--idle-session-timeout <duration>` (also
  `WEBBRIDGE_IDLE_SESSION_TIMEOUT`, default `30m`) ends a project session that
  has no viewer and no run after that long; `0` never ends one. The bridge
  sweeps every 30s and closes what it has just read as idle. `marshal daemon
  install` writes this flag into the unit from `[daemon]
  idle_session_timeout` in the user config, so a change takes effect at the
  next install.
- A fleet agent the daemon starts on a local project runs on that project's
  child, beside the TUI's sessions; only plain local host agents move, and a
  git-sourced, workspace or containerized agent keeps a child each. Releasing
  an agent closes its session, never the child.

Two routes upgrade a marshal client onto a session and relay its frames: `POST
/api/projects/attach` onto a project's child, and `POST
/api/agents/{id}/attach` onto one agent's session, whose `101 Switching
Protocols` carries `Marshal-Session` (the agent's session) and `Marshal-Agent`
(its ID). Both are the socket's own, and so are pin, restart, the hosts report
and enrollment: that set is the one `socketOnly` names (`web/bridge/http.go`),
none of it is on the mux, and the web listener answers every route in it 404 —
whatever credential a request carried, and whether or not the listener bound a
TCP address rather than a unix one.
Neither attach resumes a paused agent: it refuses an unknown
agent with 404, a runtime that fails to start with 502, and an agent that is
paused (`resume it first`), runs on a child of its own with no viewer socket
yet, or has no session yet with 409.

A containerized agent's session opens too. Its viewers reach the host side of
the socket the container's agent serves beside `agent.sock`, and a dial that
fails there is 502 with `dial the agent's container: <error>` — plus `; its
image runs another marshal version, which may serve no viewer socket: rebuild
it` when the agent reported a different marshal version. Such a relay is
restricted: its viewer may call only the methods the session screen uses, so
any other request is answered with JSON-RPC `-32000` and `<method> is not
available for a containerized agent`, any other notification is dropped and
logged once per method, and a batch or malformed frame is dropped rather than
forwarded. Answers to the agent's own requests carry no method and still pass,
so approvals and questions work. A project attach relays every frame unchanged.

A viewer on either attach still cannot close or replace the session:
`session/close` naming an agent's session, and a replacing `session/new`, are
answered with JSON-RPC `-32000` and the sentence `This is agent <name>'s
session: finish it from the fleet screen.`, and the project attach's 409 refuses
every session an agent record names, paused or live.

A project can be pinned, which keeps its child running with no session:
`marshal daemon pin [dir]` and `marshal daemon unpin [dir]` change the pins
through `POST /api/projects/pin`, a route the socket serves and the web
listener answers 404 for. The pins are the fleet registry's `pinned` list, so they
survive a restart; a pinned child is started at the daemon's start and holds
its project host through `project/hold` after every handshake, with the index
watcher on. `GET /api/daemon` reports the pins and marks each pinned child,
and `marshal daemon status` prints them.

`--oauth-refresh` (also `WEBBRIDGE_OAUTH_REFRESH`, off unless asked) supervises
`marshal daemon refresh` beside the bridge. That
helper holds `<data>/oauth-refresh.lock` and, every few seconds, re-reads the
user config's modification time and size, refreshing the OAuth providers it
names and rebuilding its workers when the file changes. One process refreshes:
an embedded `marshal` runs its own refresh workers only while it holds the same
lock, so with a daemon running no embedded TUI refreshes, and two TUIs no
longer race the same grant. A process that loses the lock still refreshes
inline when the token is used. `marshal daemon install` always writes
`--oauth-refresh` into the unit, so an installed daemon refreshes; a bridge
started by hand, and every test, starts no helper unless asked.

`--tunnels` (also `WEBBRIDGE_TUNNELS`, off unless asked) runs
`<marshal-bin> daemon tunnels --socket <socket>` beside the bridge. That
helper keeps one `ssh -N` forward per `fleet.toml` host, from
`<daemon dir>/hosts/<name>.sock` to that host's own socket, so the fleet
screen's `marshal fleet --host NAME` reaches another machine's daemon; it
reports each tunnel through the socket's hosts route, which `GET /api/daemon`
serves. `marshal daemon install` always writes `--tunnels` into the unit.

Each project child starts with the project's own environment, as a terminal in
that project would give it: the user's login shell (`$SHELL`, else `/bin/sh`)
is run in the project directory, and a `.envrc` is overlaid through `direnv`
when it is on the resolved `PATH`. `webbridge print-env`, a subcommand of this
binary, is what prints it, so whatever a startup file prints first is skipped.
Each step is bounded at 10s. The bridge keeps its own `MARSHAL_CONFIG_DIR`,
`MARSHAL_DATA_DIR`, `XDG_CONFIG_HOME` and `XDG_DATA_HOME` over the resolved
values. A resolution that fails or times out falls back to the bridge's
environment and records a warning naming the step and how it failed — never
the step's output — which `GET /api/daemon` reports per child as `envWarning`
and `marshal daemon status` prints.

## The fleet inbox

`GET /api/inbox` answers the fleet's inbox:
`{"groups":{"needsYou":[…],"running":[…],"ready":[…],"earlier":[…]},"queue":[…]}`.
The groups are agent IDs in the order they need attention, and the queue is
what needs a person, oldest first — the needs-you agents and the intake
submissions, each naming its kind and the instant it arrived. The bridge
computes the grouping, the urgency order and the queue, so a client renders
them and groups nothing itself.

The same object arrives unprompted as an `inbox` delta on the fleet stream
(`GET /api/events?stream=fleet`), sent whenever the inbox changes. Like the
budget, reroute and watch deltas it names no session: it is one view of the
whole fleet, so the last one received is the current inbox.

## Secrets

Credential values (git tokens, provider keys, workspace CA keys) live in
a secret backend, chosen at startup with `--secrets`:

| Backend | Flags | Notes |
|---|---|---|
| `env` (default) | none | Read-only. `vault:env/NAME` reads `$NAME` at use time. |
| `local` | `--secrets-key-file F` | AES-256-GCM in `<state-dir>/secrets/store.json`. The key file holds 32 raw bytes, must be mode `0600`, and must sit **outside** `--state-dir`. |
| `openbao` | `--bao-addr`, `--bao-mount` (default `secret`), `--bao-role-id-file`, `--bao-secret-id-file`, `--bao-ca-file` | KV v2 with AppRole login; the token renews at half its TTL. |

Every flag also reads `WEBBRIDGE_SECRETS`, `WEBBRIDGE_SECRETS_KEY_FILE`,
`WEBBRIDGE_BAO_ADDR`, `WEBBRIDGE_BAO_MOUNT`,
`WEBBRIDGE_BAO_ROLE_ID_FILE`, `WEBBRIDGE_BAO_SECRET_ID_FILE` and
`WEBBRIDGE_BAO_CA_FILE`.

Create a key file for the `local` backend with:

```bash
webbridge secrets init-key /etc/marshal/secrets.key
```

It refuses to overwrite an existing file. Values are written through
`PUT /api/secrets/vault:<path>`, are never returned by any route, and
are never logged: the audit log records `secret_set` and
`secret_deleted` with the ref only.

## Recipes, schedules, notifications and status links

- **Recipes** are parameterised prompts (or plan or swarm runs) with
  limits, stored as JSON under `<state-dir>/recipes/`. Four built-ins ship
  (`review-pr`, `fix-ci`, `summarize-changes`, `update-deps`); copy one to
  edit it. `POST /api/recipes/{name}/run` starts one. A recipe with an
  `output` ends with a fenced `json` block the bridge parses.
- **Schedules** run a recipe on a five-field UTC cron
  (`/api/schedules`). A tick is skipped while the previous run is still
  active; missed ticks are not made up.
- **Notifications** POST fleet events (`needs_you`, `run_finished`,
  `budget`, `automation`, `watch_fired`, `network_block`) to webhooks
  (`/api/notifications`). With a `secretRef` the body is signed:
  `X-Marshal-Signature: sha256=<hex hmac>`. Links in them use
  `--public-url` (`WEBBRIDGE_PUBLIC_URL`); without it they are relative.
- **Status links** (`/api/status-links`) give a revocable, read-only
  public page at `/s/<token>`. It shows the agent name, project folder
  name, status, progress, task list and the current step headline, and
  nothing else. It needs no credential, is rate limited per IP, and
  answers 404 for unknown, expired and revoked tokens alike.

## Egress proxy

Agents reach the network through a proxy that enforces a per-agent
policy, injects credentials the agent never sees, and logs every
connection. `--egress on` (off unless asked; `--egress on` or
`WEBBRIDGE_EGRESS=on` enables it) starts it at bridge start, and it fails to
start without a usable container runtime:

- **Container mode.** The bridge creates an internal network
  (`marshal-agents`), builds `marshal-egress:<version>` from a
  Debian base plus this binary, and runs it as the `marshal-egress`
  sidecar, also attached to the outside network. Agent containers join
  `marshal-agents` only, with `HTTP(S)_PROXY=http://<agent>:<token>@marshal-egress:3128`.
  The sidecar is the same binary: `webbridge egress --listen :3128 --control unix:///egress/control.sock`.
  The bridge serves the control link on `<state-dir>/egress/control.sock`
  (mode 0600): `GET /policy` streams policy snapshots, `POST /log` takes
  connection records, `POST /blocked` reports refused requests, and
  `GET /leaf` mints a leaf certificate, so the sidecar never holds a CA key.
  Egress applies to container agents only; a host agent has no proxy, and
  `GET /api/network` reports `processMode: true` when egress is off, meaning
  there is no isolation.

Agent tokens are derived from the agent ID with a key kept in
`<state-dir>/egress/token.key` (mode 0600), so a container that outlives
a bridge restart keeps authenticating.

Per-agent modes are `off`, `open` and `allowlist`. A workspace with no
`[network]` is `open`. Injecting a credential into an agent container needs a
writable secrets backend (`local` or `openbao`), because the workspace CA
key lives in the vault at `vault:ca/<workspace>`; with the `env` backend
a spawn that asks for injection is refused. Provider keys stored at
`vault:providers/<name>` are injected as `Authorization: Bearer` for the
provider's host in every workspace, and provider hosts are added to
allowlists automatically.

Connection records go to `<state-dir>/network/YYYY-MM-DD.jsonl`; the
bridge keeps aggregates for 7 days and deletes files after 30.

## Bootstrap factory operation

The factory is available at `#factory` and `#factory/<item-id>` in the bridge UI. Keep one stable `webbridge` process running with a fixed `--workspace` and `--state-dir`; the journal is `<state-dir>/factory/v1/journal.jsonl`. A second bridge cannot own that directory. Build factory changes in another checkout and upgrade the stable controller only between runs, after quiescing it. The factory does not merge PRs or deploy its own binary.

Create a draft work item with an approved plan/spec, registered GitHub repo, trusted project root, target branch, native or published workspace, and explicit implementer/reviewer model routing. Record the required CI identities: a check run requires its name and GitHub App ID; a commit status uses its context. If there truly is no required CI, explicitly acknowledge it. Approval starts the seed commit and draft PR. The default allowance is **five total implementation/review rounds**, including the first implementation; a later explicit grant adds rounds without erasing history. Changing the implementer model on a retry starts a fresh session with a summary of retained code, review and CI evidence. It does not reuse the old model's hidden context.

The UI polls active items every two seconds and idle items every 15 seconds while visible; reopening the browser refreshes durable state. For a script or controller, use the authenticated REST surface: `GET/POST /api/factory/items`, `GET /api/factory/items/<id>`, and `POST /api/factory/items/<id>/commands`. Commands carry a new `requestId`, the visible `expectedVersion`, and `kind` (`approve`, `answer`, `stop`, `retry`, `reconcile`, or `recover`). Reuse the same request ID only for an identical retry after a lost HTTP response. A conflict means refresh the item, inspect its decision/effects and submit a new command only for a new action. Normal agent permission and question answers remain on their existing endpoints.

A surviving `owner.lock` after a crash is intentional. First stop the old bridge and verify its process/container is gone. Check that no other bridge uses the same `--state-dir`; preserve a copy of `journal.jsonl` and the lock for diagnosis. Only then remove `<state-dir>/factory/v1/owner.lock` and restart with the same state/workspace paths. Never use lock age or a stale PID alone as proof. A malformed complete journal record is a startup failure; keep it for diagnosis rather than editing it by hand.

For `recovering` or an unknown dispatched prompt, inspect the item history, saved worker/session, checkout HEAD/tree, remote branch and PR SHA before acting. `reconcile` refreshes available evidence but cannot infer a completed model turn from a live container or a successful RPC with a cancelled stop reason. `stop` records intent and waits for confirmed idle and no detached work; it may remain `stopping` if status is unknown. To `recover` and retry from retained code, first establish the previous worker has stopped; if the runtime is unreachable, an operator must explicitly attest this in the command. The attestation is recorded and is not inferred from elapsed time. A retry can append an amended approval and additional round allowance. Never force push, remove retained workspaces, or start another worker on an uncertain branch.

Native mode runs the worker as a local process; after a bridge crash, a lost native runtime cannot be reattached automatically and may require manual recovery. Container mode can reattach an identified surviving container; parking stops a confirmed idle worker while retaining its checkout and session. Before relying on OrbStack ARM64, validate the bridge/agent mount paths, actual container stop and slot release, saved session resume, and lack of interference with unrelated containers on that host. See the [acceptance record](../../docs/bootstrap-factory/acceptance.md) for the pending live and ARM64 gates. Podman needs its own compatibility run.
