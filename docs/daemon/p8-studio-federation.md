# P8 — one Studio over many daemons

Parent: [`design.md`](design.md).
Builds on [`p7-studio-sessions.md`](p7-studio-sessions.md) §Phase 2 direction,
which this spec settles; [`p4-local-daemon.md`](p4-local-daemon.md) (the local
profile, its Studio listener and token); [`p5-fleet-screen.md`](p5-fleet-screen.md)
F7 and its [server-profiles plan](plans/2026-11-16-p5-7-server-profiles-plan.md)
(`fleet.toml`, its tokens); [`../studio-flutter/design.md`](../studio-flutter/design.md)
§2 (the host switcher, the composer's chips); [`../studio-flutter/f1-client.md`](../studio-flutter/f1-client.md)
decisions 3–8 (discovery, tokens, hosts); [`../studio-flutter/f7-automations-library-settings.md`](../studio-flutter/f7-automations-library-settings.md)
(the sections and their models); and
[`../studio-flutter/design-system.md`](../studio-flutter/design-system.md)
§7 (states), §9 (unsaved edits) and §10 (extending the system).
Depends on: nothing beyond `main`. P7 is independent; where both edit
`app/lib/main.dart`, the later plan re-checks its anchors.
Status: proposed 2026-11-18. Plans: [§Plans](#plans).

Anchors are relative to `main` at `bdd20ddc`.

## Goal

A person attaches as many daemons as they like to one Studio, and manages
agents, the fleet, the factory and automations on all of them from it, in the
native app and in a browser.

- **Every place reads every host.** Automations, Library and Settings read the
  host the switcher names, not the first one discovery found. The Overview and
  the Inbox show what needs a person on every host, automations included.
- **A host is added and removed from the Studio,** and new work chooses the
  host it runs on.
- **A daemon behind NAT, or one a browser cannot reach, joins a relay** by
  dialling out. A Studio served by that relay lists it as a host.
- **The client stays the only place hosts are merged** (P7 §Phase 2
  direction). No bridge namespaces ids, merges views or fans in streams; the
  relay forwards one host's API unchanged.

## Context

**The app already holds several hosts.** `HostRegistry`
(`app/lib/src/client/host.dart:114`) keeps one `Host` per daemon. The fleet,
factory and pull-request stores read every host and key by host and id. A
native build discovers the local daemon and every `fleet.toml` server profile
(`host_discovery_io.dart`). A browser discovers only the bridge that served
the page (`host_discovery_stub.dart`).

**Three places read the first host.** `_Sections._host`
(`app/lib/main.dart:308`) is `hosts.hosts.first`. Automations
(`:319`), Library (`:348`) and Settings (`:379`) build every model from its
client, once. That covers bots, schedules, watches, recipes, skills, plugins,
MCP, memory, environments, connections, models, secrets, network, usage,
notifications, access and General's daemon document.

**The switcher narrows the tree only.** `HostSwitcher`
(`app/lib/src/ui/shell/host_switcher.dart`) sets `_ShellState._host`
(`shell.dart:107`), and only the sidebar's tree reads it (F2: "the host
switcher narrows the sidebar's tree only"). The Studio's design says it
"scopes all of it to one daemon or to all of them" (design.md §2). The place
builders take no host: `automationsFor`, `libraryFor` and `settingsFor` are
`(section, state)` (`shell.dart:427-433`).

**Three cross-host signals reach no screen.**
- The Overview's Automations card takes `automations: List<TrippedWatch>`
  (`overview.dart:41`), and the shell passes none (`shell.dart:574`).
- The Inbox projects blocked network requests and held review drafts from
  `showPendingBlocks` and `showHeldDrafts` (`inbox_model.dart:298`, `:309`).
  Only tests call them.

So a watch that trips, a request the egress proxy blocks, and a review the
bot holds reach no screen on any host, the first one included.

**A host is added by hand.** The Hosts section is read-only, and its
"Add a host" row is planned (`hosts_section.dart:41`). `marshal fleet` has
`login`, `servers` and `token` (`cmd/marshal/main.go:305-329`), and nothing
writes `fleet.toml`. `fleet login` reads one line from a stdin that is not a
terminal (`readFleetToken`, `main.go:497`). The registry adds hosts and never
removes one (`discover`, `host.dart:129`). The stores follow additions only
(`fleet_store.dart:192`, `factory_store.dart:394`, `pulls_store.dart:226`).

**New work runs where the thread is.** The dialog's Host chip is a note,
"The thread runs here; F7 lists hosts" (`new_work_model.dart:243`).
`newWorkFor` builds the model on the current thread's host (`main.dart:141`).

**Reach stops at a host's listener.** A bridge serves the API on a TCP
listener behind its bearer token, over TLS when `--tls-cert` is set
(`cmd/webbridge/main.go:139`, `:708`). The local profile opens it on
`daemon.studio_addr`, a loopback address, with `--token-file`
(`internal/daemon/daemon.go:64-67`). A machine behind NAT cannot be reached
at all. A browser cannot reach a second origin: the bridge sends no CORS
headers, and the page keeps one token (`token_store_web.dart`).

## Decisions

### Every host's places (plan 1)

**D1. The switcher is the one host scope.** The sidebar's switcher scopes the
project tree, Automations, Library and Settings. Each of the three places
reads the host it names. The Overview, the Inbox and Pull requests stay
fleet-wide, as F2 decided, because they are projections of every host. The
place builders take the host: `automationsFor`, `libraryFor` and
`settingsFor` become `(host, section, state)`.

**D2. A place under "All hosts" shows one host and says which.** These places
edit one daemon's configuration, so they never merge hosts.
- Under All hosts, a place reads the first live host in discovery order. When
  no host is live, it reads the first host, whose state then says it is
  unreachable (§7).
- The place's top line is the `HostSwitcher` itself, showing the host being
  read. Choosing there sets the shell's one scope, so it narrows the tree too.
- Settings › Hosts and General's appearance and keyboard rows are about the
  app, not a host, and read the same under every scope.

**D3. Each host's models are built once and kept.** `_Sections` keeps one set
of Automations, Library and Settings models per host, built the first time
that host is shown. Switching back is instant, and an unsaved edit on one host
survives a switch.
- Changing the scope while a place holds an unsaved edit goes through the
  shell's guard (design-system.md §9), as leaving by the sidebar does.
- A host's section project is that host's first project, as `_project` is
  today for the first host.
- A forgotten host's models are disposed (D9).

**D4. One reader feeds the cross-host signals.** A new `SignalsStore`
(`app/lib/src/model/signals_store.dart`) reads, for every host:
- `GET /api/watches`, for the tripped watches on the Overview's Automations
  card;
- `GET /api/network/pending`, for the Inbox's network asks
  (`showPendingBlocks`);
- `GET /api/automations/review/drafts?status=draft`, for the Inbox's draft
  asks (`showHeldDrafts`).

It reads each host when the host is added, then every 15 s, with one read in
flight per host. A failed read keeps that host's last answer. A host that has
never answered contributes nothing. Every tripped watch names its host, and
the card shows it when more than one host is known. After the Inbox answers a
network or draft ask, it asks the store to re-read that host at once.

**D5. A section's write re-reads the signals.** Posting, sending or discarding
a draft in Bots, and starting a watch in Watches, ask `SignalsStore` to
re-read that host. A section never hands its own list to the Inbox.

### Adding a host, and choosing where work runs (plan 2)

**D6. `marshal fleet add` and `marshal fleet remove` edit `fleet.toml`.**
- `marshal fleet add NAME URL [--ca-file PATH] [--no-notify]` appends a
  `[servers.NAME]` table to the file's text. It creates the file `0600`, in a
  `0700` directory, when there is none. It keeps the file's comments and
  order. The result is decoded with `Load`'s strict rules before anything is
  written, and the write is atomic (temporary file, then rename) at `0600`. A
  name already present is refused.
- `marshal fleet remove NAME` deletes the `[servers.NAME]` table, from its
  header line to the next table header or the end of the file. A file that
  defines the server any other way (dotted keys, an inline table) is refused,
  naming the file to edit by hand. Remove also deletes the keychain token
  (`KeychainKey(NAME)`), and a missing token is not an error.
- Both refuse a file that does not load, with `Load`'s words.
- `fleetprofile` owns both edits: `AddServer` and `RemoveServer`.

**D7. A token reaches the CLI on stdin only.** The Studio adds a host by
running `marshal fleet add`, then `marshal fleet login NAME` with the token
written to its stdin and stdin closed. The token is never in argv or the
environment. A `token_cmd` profile is added by editing the file; the form
does not offer one. The runner lives in `app/lib/src/client/fleet_profiles_io.dart`,
the lane where `dart:io` is allowed. A web stub answers that the browser
cannot add a host.

**D8. Settings › Hosts adds, removes and signs in.**
- In a native build, "Add a host" becomes a real row. Its detail asks for a
  name, a URL, an optional CA file and the token, written as Secrets' "New
  value" field is and never shown again. Save runs D7, then discovery.
  A failure keeps the form and shows the command's last line of stderr.
- A server host's detail gains **Remove**, behind a confirmation, which runs
  `fleet remove` and forgets the host (D9). The local daemon has no Remove.
- A server host whose token was refused gains **Sign in**: a token field that
  runs `fleet login NAME` (D7), then retries that host (D9).
- In a browser the row stays planned until plan 4 (D22).

**D9. The registry forgets and retries a host.**
- `HostRegistry.forget(name)` stops and disposes the host and notifies.
  `FleetStore`, `FactoryStore`, `PullsStore` and `SignalsStore` drop its
  state on that change, and `_Sections` disposes its models. A switcher that
  named it falls back to All hosts.
- `HostRegistry.retry(name)` stops the host's stream and starts it again, for
  a host whose token has just changed.
- Discovery still never forgets on its own: a host that is down is kept and
  marked (F1 decision 8).

**D10. New work chooses its host.** The new-work dialog's Host chip is a menu
of every live host that lists a project, in discovery order. It defaults to
the current thread's host, then the switcher's, then the first live host.
- Choosing a host binds the dialog's routes — spawn, planner, recipes, recent
  prompts, issues and the project list — to that host's client.
- The Project chip keeps its root when the chosen host lists it, and otherwise
  takes that host's first project.
- The thread it starts opens on the chosen host: `onThreadStarted` and
  `onPlannerStarted` carry the host.

This is design.md §2's composer host chip. A thread's own composer steers a
thread that lives on one host, and needs none.

### The relay (plan 3)

**D11. A relay is a bridge that names the hosts that may join it.**
`webbridge --relay-join NAME=PATH`, repeatable (`WEBBRIDGE_RELAY_JOIN`,
comma-separated), names each host that may join and the file that holds that
name's join secret. The file must be private (`0600`, owned by the user), and
is read at start. A name follows `fleet.toml`'s name rule
(`^[A-Za-z0-9._-]+$`). Either profile can relay. A bridge with no
`--relay-join` relays nothing, and its own API is unchanged.

**D12. A daemon dials out to join.**
- `webbridge --join URL --join-name NAME --join-token-file PATH
  [--join-ca-file PATH]` joins the relay at URL. URL is `https`, or `http` on
  a loopback host, as `fleet.toml`'s rule has it.
- A joining bridge must have a bearer token (`--token` or `--token-file`),
  because relayed requests are checked with it (D15). With `--join`,
  `--token-file` is read or created even under `--addr off`.
- The local daemon joins through user config: `[daemon] join_url`,
  `join_name` and `join_ca_file`. `marshal daemon join-token` stores the
  secret, read from stdin as `fleet login` reads a token, in
  `<daemon dir>/join.token` at `0600`. `daemonUnitSpec` writes the join flags
  and `--token-file`. A change takes effect at the next `marshal daemon
  install`, as `idle_session_timeout` does.

**D13. A join is an HTTP/1.1 upgrade that reverses the connection.**
- The joiner sends `POST /api/relay/join` with `Authorization: Bearer
  <secret>`, `Connection: Upgrade`, `Upgrade: marshal-relay/1`,
  `X-Marshal-Join-Name` and `X-Marshal-Join-Instance`, a random id per
  process. Over TLS it offers HTTP/1.1 only, so the relay can take the
  connection over.
- The relay checks that the name is one it names, compares the secret with
  that name's in constant time, answers 101, and hijacks the connection. From
  then on the relay is the HTTP client on it and the joiner the server.
- An unknown name and a wrong secret are both 401, with the same body. A
  request that is not the upgrade is 400. The route answers before the bearer
  check, as the Studio's sign-in exchange does (`studioRoute`,
  `studio_access.go:140`), and only on a relay.
- It is all standard library. The joiner serves its connections with
  `http.Server.Serve`, over a listener whose `Accept` returns joined
  connections. The relay's per-host `http.Transport` dials by taking one.

**D14. A joiner keeps a small pool, and the newest instance wins.**
- The joiner keeps two idle joined connections. When one carries its first
  request, it dials a replacement, up to 32 connections. Each connection has
  TCP keep-alive at 15 s.
- A failed join backs off from 1 s, doubling to 30 s. A 401 is logged once
  per change of answer and retried every 30 s.
- The relay waits up to 5 s for a connection to a host, then answers 503,
  `"<name> is not connected"`.
- A join with a new instance id for a name closes the older instance's idle
  connections, and lets its requests in flight finish. A restarted daemon
  takes its name back at once. Anyone who could take it holds that name's
  secret anyway.

**D15. The relay gives reach, not authority.**
- The relay forwards `/api/hosts/{name}/{rest...}` to `/{rest}` on the joined
  host, and only when `rest` is under `api/` and not under `api/hosts/` or
  `api/relay/`, so relays never chain. The route needs the relay's own
  bearer token, as every `/api/` route does. It also needs
  `X-Marshal-Host-Token`, the host's own token; without it the relay answers
  401 itself and forwards nothing.
- On the hop, `Authorization` becomes `Bearer <host token>`.
  `X-Marshal-Host-Token` and `Cookie` are removed. No `X-Forwarded-*` header
  is added.
- The host serves a relayed request through `ServeHTTP` (`http.go:164`), its
  network listener's handler. So its own bearer check decides, and the
  socket-only routes stay 404. On a relayed connection it also answers the
  agent attach and `/api/studio/*` 404.
- Every forwarded response carries `X-Marshal-Relayed: <name>`, so a client
  can tell the host's 401 from the relay's.
- Bodies stream both ways. An event stream is flushed as it is written.
- **The limit:** the relay terminates TLS, so it sees each host's token in
  transit. It must be a machine trusted as much as the daemons that join it.
  What the relay cannot do is act on a host without a client presenting that
  host's token. Its own token opens no daemon, and a join secret only lets a
  daemon offer itself.

**D16. A relay lists its hosts.** `GET /api/relay/hosts` answers
`RelayHosts{hosts: [RelayHost{name, connected, since, lastContact}]}`: every
name the relay names, connected or not, sorted by name. `since` is when the
current instance joined, and `lastContact` is the last response forwarded or
join accepted. A bridge that relays nothing answers `{"hosts": []}`; an older
bridge answers 404.

**D17. A joiner reports its join and hands over its token.**
- `GET /api/daemon` (`DaemonInfo`) gains an optional `join`:
  `{url, name, connected, lastError}`. `marshal daemon status` prints one line
  from it: "joined <url> as <name>", or the last error.
- `marshal daemon token` writes the daemon's bearer token
  (`<daemon dir>/studio.token`) and a newline to stdout. It refuses a terminal,
  as `fleet token` does (F1 decision 3). So `marshal daemon token | pbcopy`
  puts it where a browser's Sign in can take it, and it never reaches
  scrollback.

### The Studio through a relay (plan 4)

**D18. A relayed host is an ordinary host.** Its `BridgeClient` runs over a
relayed transport: the relay's transport, with every path prefixed by
`/api/hosts/<name>` and `X-Marshal-Host-Token` set from that host's token.
The transport is plain Dart (`app/lib/src/client/relayed_transport.dart`).
- A 401 on a relayed host is that host's `Unauthorized`, and it reads "token
  refused". The relay refusing the page's own token also refuses the relay's
  own host, which then reads "token refused" too. While its relay does, a
  relayed host's detail says to sign in to the relay first. The app needs no
  header for this. `X-Marshal-Relayed` (D15) is for a caller that reads
  headers, such as `curl`.
- With no token for the host, the transport answers as a 401 would, with
  "sign in to <name> first", and sends nothing.
- `HostSpec` gains `via`, the relay's host name, or null.

**D19. A browser lists the relay's hosts.** Browser discovery reads the
serving bridge's `GET /api/relay/hosts` after listing that bridge. Each name
becomes a host via the page's host. A 404 adds none. A name that equals a
host already listed is skipped.

**D20. A native build names relayed hosts in `fleet.toml`.** A profile holds
`relay = "HUB"` in place of `url`: the host is reached through server profile
HUB.
- `fleetprofile` refuses a relay that names no `url` profile, or one that is
  itself relayed. There are no chains.
- `fleet servers --json` reports `relay`, and `fleet add NAME --relay HUB`
  writes one (D6).
- The token is the host's own, stored by `fleet login NAME` as today.
- Native discovery wraps HUB's server transport for the host.
- `marshal fleet --server NAME` refuses a relayed profile: "NAME is reached
  through HUB, and the fleet screen does not relay yet".

**D21. A browser keeps one token per host.** `TokenStoreWeb.forHost(name)`
keeps a relayed host's token under `marshal:bridge-token@<name>`, in the
same storage the page's own token uses (`token_store_web.dart:4`). Reading,
writing and clearing behave as the page token's do, `normalizeToken`
included.

**D22. Settings › Hosts signs a browser in to a relayed host.** A relayed
host's detail offers **Sign in** (a token field that stores the token and
retries the host, D9) and **Sign out** (which clears it). The browser's "Add
a host" row stays planned, with the note "A browser reaches the hosts the
bridge that served it relays: join a daemon to it with `webbridge --join`".

**D23. A relayed host says where it is reached.** Settings › Hosts' kind line
for a relayed host is "A server profile, through <via>". The switcher, the
tree and every place treat it as any other host.

## Components and data flow

```
 native app                                     browser on hub's page
 ──────────                                     ─────────────────────
 HostRegistry ── local (unix socket)            HostRegistry ── hub (page origin)
              ├─ build (https, fleet.toml)                   └─ laptop (via hub)
              └─ laptop (relay = "hub")
                       │                                      │
                       ▼                                      ▼
            hub's TCP listener   /api/hosts/laptop/api/…  + X-Marshal-Host-Token
                       │
                       ▼
            hub: relay table ── laptop: joined connections (dialled out by laptop)
                                         │
                                         ▼
                              laptop's ServeHTTP, laptop's bearer check
```

- **Places.** `Shell` passes its scope to the place builders. `_Sections`
  resolves the host (D2), finds or builds that host's models (D3), and wraps
  the place with the switcher.
- **Signals.** `SignalsStore` follows `HostRegistry` as `FactoryStore` does.
  It polls each host and hands blocks and drafts to `InboxModel`, and the
  Overview reads its tripped watches.
- **Hosts.** Settings › Hosts runs `FleetProfiles` (D7), then
  `HostRegistry.discover`, `forget` or `retry`.
- **Relay.** The relay's `Server` gains a relay table built from
  `--relay-join`, the join route, the forward route and the hosts route. A
  joining `cmd/webbridge` starts a joiner beside its listeners. The joiner
  serves `api`'s network handler over joined connections.

## Error handling

| Condition | Behaviour |
|---|---|
| A place under All hosts, no host live | The first host is read; its unreachable state names it with its last contact (§7) |
| A signals read fails | That host's last answer is kept; a host never answered contributes nothing |
| `fleet add`: the name is taken, the URL is bad, the file is not private | Refused with the reason (`checkURL`'s and `Load`'s words); the form shows it and keeps the fields |
| `fleet login` fails after `fleet add` succeeded | The host is added without a token. The form says "Added NAME, but its token was not stored: …", and the host reads "token refused" until it is signed in |
| `fleet remove` on a server defined inline or by dotted keys | Refused, naming the file to edit by hand |
| The chosen new-work host stops answering before Start | The spawn fails with the bridge's words and the draft is kept, as today |
| A join names an unknown host, or the wrong secret | 401; the joiner logs it once per change and retries every 30 s |
| The relay cannot be reached | The joiner backs off to 30 s; `DaemonInfo.join.lastError` says why |
| A forward names a host the relay does not name | 404, `"no relayed host <name>"` |
| A named host is not connected | 503 after 5 s, `"<name> is not connected"` |
| A forward has no `X-Marshal-Host-Token` | 401 from the relay, with no `X-Marshal-Relayed` |
| The host refuses the token | Its 401, with `X-Marshal-Relayed`; that host reads "token refused" |
| The relay refuses the page's own token | The relay's host and every host through it read "token refused"; a relayed host's detail says to sign in to the relay first |
| A forward's path is not under `api/`, or is a relay's own route | 404 |
| A joined connection drops mid-request | 502; an event stream ends and the client reconnects, as from any bridge |
| A relay older than plan 3 | `GET /api/relay/hosts` is 404, so a browser lists no relayed hosts |
| A relayed `fleet.toml` profile names a missing or relayed relay | `Load` refuses the file |

## Testing

- **Plan 1, the app.**
  - The shell passes its scope to the place builders.
  - Under All hosts with several hosts, the first live host is read.
  - Switching keeps each host's models, and an unsaved edit guards the switch.
  - `SignalsStore` reads every host, keeps a failed host's last answer, and
    coalesces re-reads.
  - Tripped watches name their host on the Overview.
  - Network and draft asks from a second host appear in the Inbox.
- **Plan 2, Go and the app.**
  - `fleetprofile`'s `AddServer` and `RemoveServer` keep comments, refuse
    duplicates and inline definitions, and write `0600`.
  - The `fleet add` and `fleet remove` commands.
  - The app's runner writes the token to stdin, never argv.
  - The Hosts section adds, removes and signs in.
  - The registry forgets and retries, and every store drops a forgotten
    host.
  - New work's Host chip rebinds the routes and the project.
- **Plan 3, the bridge.**
  - Join refusals and the constant-time check.
  - The upgrade, and a forwarded request and an event stream over a joined
    connection.
  - The host's own bearer check decides; the header rules on the hop.
  - The 404, 503 and 401 answers.
  - The newest instance wins; the pool's replenishment and its cap.
  - `GET /api/relay/hosts`, and the join report in `DaemonInfo`.
  - The flags' validation, and the unit's join arguments.
  - `marshal daemon join-token` and `marshal daemon token`, and the terminal
    refusal.
- **Plan 4, the app and Go.**
  - The relayed transport's prefix and header, and its no-token answer.
  - Browser discovery of relayed hosts.
  - The per-host token store.
  - The `relay` key's validation, and the fleet screen's refusal.
  - Native discovery of a relayed profile.
  - Sign in and sign out.
- **Acceptance, by hand,** on a development box, with `scripts/dev-studio.sh`'s
  server-profile Studio on `127.0.0.1:7700` as the second host:
  1. Add the `:7700` Studio as a host in the native app's Settings › Hosts.
     It appears in the switcher.
  2. Switch to it. Automations, Library and Settings show its sections, and
     switching back to `local` shows the local daemon's.
  3. Start new work on `:7700` from a `local` thread. The thread opens on
     `:7700`.
  4. Give the `:7700` Studio `--relay-join local=…`, set the local daemon's
     `join_url` to it, reinstall, and open `http://127.0.0.1:7700` in a
     browser. `local` is listed, reads "token refused", signs in with
     the token `marshal daemon token | pbcopy` copied, and shows the local
     daemon's projects and sessions.
  5. Remove the `:7700` host in the native app. It leaves the switcher and the
     tree.

Not tested: two physical machines across NAT (the loopback relay stands in
for it), a containerized agent's viewer over the relay, and CI's lack of a
keychain for the Sign in path, which the tests cover with an injected runner.

## Assumptions

- **The relay is trusted with tokens in transit** (D15). A design that hides
  them from the relay needs encryption inside the tunnel, which a browser
  cannot do over `fetch`.
- **Polling the signals every 15 s is cheap.** It is three reads per host. The
  fleet stream carries none of them, and adding them to it is a contract
  change no screen needs yet.
- **A browser's per-host tokens live where the page token lives.** That is
  the browser's storage for the relay's origin. It is the same exposure the
  page token has today, once per host.

## Non-goals

- Merging any host's views on a bridge, or relaying a relay (no chains).
- The fleet screen, `marshal attach` or a containerized agent's viewer over a
  relay.
- A single-use sign-in code through the relay. A browser signs in to a
  relayed host by pasting its token.
- The local daemon acting as a relay through `marshal daemon install`.
- A native build discovering a relay's hosts on its own. Native hosts stay
  explicit in `fleet.toml`.
- Mutual TLS, multiple users, and a host's token rotating itself.
- Adding a `token_cmd` profile from the Studio.

## Open questions

None. Every open point is a decision or a named assumption.

## Plans

1. [P8 plan 1, every host's places](plans/2026-11-18-p8-1-places-plan.md):
   D1–D5.
2. [P8 plan 2, adding hosts and choosing one](plans/2026-11-18-p8-2-hosts-plan.md):
   D6–D10. It runs after plan 1.
3. [P8 plan 3, the relay](plans/2026-11-18-p8-3-relay-plan.md): D11–D17. It
   does not depend on plans 1 and 2.
4. [P8 plan 4, the Studio through a relay](plans/2026-11-18-p8-4-relayed-studio-plan.md):
   D18–D23 and the acceptance run. It runs after plans 2 and 3.

## Implementation record

Baseline at `bdd20ddc`, recorded by P7: `go test ./web/...` passes, and
`cd app && flutter test` has 3163 passing and 5 skipped.

### Plan 1

Landed, in four commits on `p8-1-places` (`p8-1-places: task N — …`):

- **Task 1.** `placeHost` (`app/lib/src/model/place_host.dart`),
  `HostPlaceBuilder`, and the host-resolving `_hostPlace()` in
  `app/lib/src/ui/shell/shell.dart`; the switcher above each of the three
  places; one guarded scope setter (`_setHost`, which goes through `_leave`)
  for the sidebar and the top line.
- **Task 2.** `PerHost<T>` (`app/lib/src/model/per_host.dart`); `_Sections`
  keeps one model set per host, finds that host's client by name, and each set
  disposes its own models.
- **Task 3.** `SignalsStore` (`app/lib/src/model/signals_store.dart`);
  `TrippedWatch.host` and the top-level derivation that `WatchesModel.tripped`
  and the store share; the Overview's Automations card, whose rows name the
  host when the fleet lists more than one; `InboxModel.onAnswered`; blocks and
  held drafts from every host in the Inbox.
- **Task 4.** `onWrote` on `BotsModel` and `WatchesModel`, wired in `main.dart`
  to re-read the host that was written.

**Decisions the executor made.** Each fills a gap the plan left; none changes a
contract it states.

1. The three builders moved from `Studio` to `Shell`, where the plan's
   Contracts section puts them; `Studio` keeps its own fields and constructor
   and forwards them. That is what lets the shell resolve the host with
   `_host` and wrap the place in the switcher.
2. The switcher is drawn only for a place this shell has a builder for. A place
   with no builder — an embedded host, or a test that supplies its own content
   — is drawn by the content builder as before, without a switcher that could
   change nothing.
3. `signals` is optional (`SignalsStore?`) on `Studio` and `Shell`, as `pulls`
   and `factory` are, so a caller that builds the shell without one still
   compiles and draws; an absent store leaves the Automations card empty.
4. `SignalsStore` keeps each host's last watch list, and a route that fails
   skips its hand-over: a failed `networkPending` never hands an empty list
   over, so the Inbox keeps the asks it had. The plan asked for that rule on
   the watches read; applying it to the other two routes is what makes "one
   route failing does not lose the others" hold.
5. `tripped` merges the raw rows before they become `TrippedWatch`s, because a
   row carries no timestamp to sort a merged list by.
6. The Library's project picker lists that host's projects only; it had listed
   every host's, and D2 says a section merges nothing across hosts.

**Mutation checks.** Every task's named test failed on the mutation the plan
names, and passed again once the line was restored:

| Task | Mutation | Test that failed |
|---|---|---|
| 1 | `placeHost` returns the first host whatever its reach | `all hosts reads the first live host` |
| 2 | `of` builds a new value on every call | `a value is built once per host` |
| 3 | blocks handed over under the first host's name for every host | `a second host's blocked request becomes an inbox ask` |
| 4 | `onWrote` called before the post's await | `a refused post reports nothing` |

**A regression the full run caught.** `leaving a dirty place through the Inbox
asks first` (`app/test/ui/sections_test.dart`) failed: a per-host place with no
builder drew its own promise in place of the content builder, so a place that
supplies its content was never mounted and the guard never watched its edit.
`_hostPlace()` now answers null when no builder is wired and falls back to the
content builder when the builder answers nothing — decision 2 above.

**Verification.** `flutter analyze` is clean; `flutter test` has 3766 passing
and 0 failing (3737 before this plan); `dart format --output=none
--set-exit-if-changed lib test` reports 347 files unchanged. No kit piece was
added and the gallery is untouched: `test/gallery_test.dart` passed as part of
the run, with its macOS-pinned goldens not regenerated.

**The run by hand was not performed.** It needs a desktop with two hosts — the
local daemon, and `scripts/dev-studio.sh`'s server-profile Studio named in
`fleet.toml` with a token stored by `marshal fleet login` — and a headless run
cannot drive it. What it would have shown is covered by tests instead: the
resolved host per scope (`place_host_test.dart`, `shell_test.dart`), the models
kept per host (`per_host_test.dart`), a second host's blocks, drafts and trips
reaching the Inbox and the card (`signals_store_test.dart`,
`overview_test.dart`, `inbox_model_test.dart`), and a write re-reading its host
(`bots_test.dart`, `watches_test.dart`). Switching hosts over an unsaved edit
and tripping a watch on the second host remain for the first person with that
setup to confirm.

**Branch review.** Two reviewers read the branch, one for correctness and one
against the design system. Neither found a blocker or a major. These changes
followed:

- `WatchesModel.create` reports the write as soon as the bridge has accepted
  it, before the list read, so a list read that fails cannot suppress the
  re-read of that host's signals.
- `SignalsStore` times its next cycle from the tick rather than from the reads
  it starts, so one host's slow bridge cannot hold every other host's read
  back; the per-host guard still coalesces a read asked while one is in
  flight.
- The switcher's inset comment now names the place's own page inset rather
  than a sidebar inset it does not take; the doubled gap below the switcher is
  gone; and `_hostPlace()` hands a fleet that lists no host to the content
  builder, which the fleet state's own page replaces anyway, instead of drawing
  a promise for a place that is built.
- A narrow-width shell test now pumps a per-host place, so the top line is
  covered below 760 as design-system §4 asks.

Declined: moving the host from the row's name into its description. The design
review called it a judgment call rather than a violation, and the plan's
decision 5 and this spec's Testing section both say the row names its host.

### Plan 2

Not started.

### Plan 3

Not started.

### Plan 4

Not started.
