# P7 — sessions in the Studio

Parent: [`design.md`](design.md).
Builds on [`p4-local-daemon.md`](p4-local-daemon.md): its project children, the
attach relay, the idle sweep and the Studio listener;
[`p5-fleet-screen.md`](p5-fleet-screen.md): the fleet's read of a daemon and
`session/follow`; [`../studio-flutter/design.md`](../studio-flutter/design.md)
§2–§3 (threads live under projects); [`../studio-flutter/f1-client.md`](../studio-flutter/f1-client.md)
decision 8 (hosts); and
[`../studio-flutter/design-system.md`](../studio-flutter/design-system.md) §6
(kinds), §7 (states) and §10 (extending the system).
Depends on: P4, implemented; the F8 cutover, implemented.
Status: revised 2026-11-18 after review; proposed for approval. **Phase 1 of
two.** Phase 2 — many daemons in one Studio, in the browser as well as the
native app — is not specified here. §Phase 2 direction fixes the shape Phase 1
must not foreclose. Plans: [§Plans](#plans).

Anchors are relative to `main` at `bdd20ddc`.

## Goal

A Studio lists every session its daemons' projects hold, including the ones a
terminal TUI started, under their project. It opens any of them to read and
drive. Every session is keyed by its host. A Studio that holds several daemons
shows each daemon's sessions as that host's threads, as it already does for
agents, work items and pull requests.

Today it lists none of them. On a daemon whose only work is TUI sessions,
`GET /api/agents` answers `[]`, and the session routes cannot address them:

```
GET /api/sessions?cwd=/…/marshal   → five sessions, with titles
GET /api/sessions/sess_…/stack     → {"error":"bridge: unknown session"}
GET /api/sessions/sess_…/access    → {"error":"bridge: unknown session"}
```

## Context

Six facts decide the shape of this phase.

**The Studio already holds several hosts.** `HostRegistry` (`app/lib/src/client/host.dart`)
keeps one `Host` per daemon. The fleet, factory and pull-request stores each
read every host and merge in the client. `ThreadRef` carries `host`, and a
thread's place is `/thread/<host>/<id>` (`app/lib/src/model/place.dart`: "The
host a thread belongs to. Every other place spans hosts."). A native build
finds the local daemon and every server profile. A browser reaches only the
bridge that served the page (`host_discovery_stub.dart`). Phase 1 works inside
that model; it does not add a second one.

**Sessions are listable but not addressable.** `listSessions`
(`web/bridge/http.go:868`) resolves a root and falls back to the control
child. Every per-session route goes through `registryForSession`
(`http.go:846`), which knows only live agent runtimes, agent ids and persisted
agents. A project child's session is none of those.

**There are four resolution paths, not one.** `registryForSession` has 16 call
sites: 14 in `http.go`, plus `images.go` and `runs.go`. `loadSession` has its
own inline `RuntimeForSession` (`http.go:971`). `serveEvents` goes through
`fleet.LogForSession` (`fleet.go:1132`). `imageTarget` (`images.go:115`) has a
project-child fallback of its own. A parameter on one of them fixes a quarter
of the problem.

**The bridge does not watch a terminal's session.**
- The attach handshake opens or resumes the TUI's session on the project
  child's registry (`attach.go:155`, `:185`). It never sends `session/follow`.
  The bridge follows fleet agents only (`fleet.go:1671`, `:2262`, `:2273`).
- Without a follow, the child forwards a run that another connection started
  to nobody but that connection (`internal/acp/follow.go`, `runFollower`).
- The project child's event log keeps every session except a client's
  (`projectchild.go:269`, `tracksClient`).

So neither `Registry`'s `Busy` flag nor the event stream sees a turn typed in
the terminal.

**The control child is the wrong home for a user's session.**
- In the local profile it is a bare `&Child{MarshalBin}` (`control.go:67`).
  It runs with the service's environment, not the project's resolved one
  (P4 D7 applies to project children only, `projectchild.go:107`).
- It has no viewer socket, so a terminal cannot attach to a session loaded
  there.
- `controlRegistryForRoot` (`control.go:342`) builds a new `Registry` on every
  call. Each `NewRegistry` (`registry.go:155`) overwrites the child's
  `OnRequest` and adds a link to its `OnNotification` and `OnRestart` chains.

**The idle sweep already reaps project sessions.** P4 D7a closes a project
child's session with no viewer and no run after `--idle-session-timeout`
(default 30m, `idle.go`).

## Decisions

**D1. The bridge owns session resolution; clients keep their URLs.** The
alternative was an optional `cwd` on the per-session routes, symmetric with
`listSessions`. It was proposed first and rejected on reading the call sites:
16 of them, `loadSession` resolved elsewhere, and a third path in the event
stream. A client parameter also cannot help a client that never had a root. So
the per-session routes keep their wire shape, and a client never sends a root
to address a session.

**D2. One rule, in the fleet.** `fleet.ResolveSession(id)` answers a
`ResolvedSession{Reg, Log, SessionID, Root, Agent}` for every per-session
route, the event stream and image uploads. It checks three places, in this
order:

1. a live agent runtime by session id;
2. an agent id, then a persisted agent, starting its runtime (a call to
   `RuntimeForSession`, unchanged);
3. a live project child whose registry tracks the id as a client's session
   (not `IsFleet`).

Resolution never starts a project child and never loads a session. Anything
else is `404 ErrUnknownSession`. `registryForSession`, `LogForSession` and
`imageTarget` become wrappers over `ResolveSession`, so the stream and the
routes cannot drift. The agent-only callers of `RuntimeForSession` (gate,
review, runs by agent id, spawn's first prompt) keep calling it.

**D3. A session has one home: its project's child.**
`fleet.OpenSession(ctx, id)` is the only path that brings a closed session
back. `POST /api/sessions/{id}/load` calls it for any id that does not resolve
to an agent. It:
1. refuses a session the bridge owns (D5);
2. finds the session's root from the listings (D6);
3. starts the root's project child (`projectChildFor`), and `pc.reg.Resume`s
   the session there, as `attachProject` does;
4. follows it (D4).

No user session is ever loaded on the control child. So a Studio and a later
`marshal attach <id>` always reach the same process. That process's
per-session turn lock refuses a second turn (`internal/acp/turn.go:1008`), so
two writers on one transcript cannot happen by construction. An agent's
session keeps today's `load` behaviour, path translation included.

**D4. The bridge follows every client session on a project child, and logs
it.** When a client session is tracked on a project child, the bridge sends
`session/follow` for it: at the attach handshake, and in `OpenSession`. The
project child's log keeps every session, the client's included. A followed
run's start and end set `Busy` (`observeFollowedRun`). That gives the Studio
three things:
- a prompt during a terminal's turn answers `409 turn already in flight`;
- a turn typed in the terminal streams on `/api/events?sessionId=`;
- the index knows which sessions are running.

**D5. The bridge's own sessions are not the user's.** The index and
`OpenSession` exclude every session the bridge owns:
- an agent's, live, paused or finished, by record (`agentOwning`);
- the control agent's own sessions (`controlRuntime.sessionID` and
  `projectSessions`);
- a planner's, by record.

`OpenSession` refuses one with `409`, naming its owner, as the attach
handshake does (`attach.go:177-183`). An agent whose record is gone leaves an
ordinary session.

**D6. Listing runs on the control child, without a registry.** A root's
stored sessions come from `session/list` on the control child, called directly
through a new `fleet.controlChild(ctx)`. `controlRegistryForRoot` is deleted,
and `listSessions` uses the same call. One page per root (the child's default,
50), most recent first. Older sessions are out of reach until a later phase
decides paging. Each call carries a 5 s timeout.

**D7. `GET /api/sessions/all` is the daemon-wide index.** It takes an optional
`root` that narrows it to one project. It is grouped by project, sorted by
root, and never null:

```json
{"projects": [
  {"root": "/…/marshal", "sessions": [
    {"sessionId": "sess_…", "title": "…", "updatedAt": 1763389530000, "messageCount": 71,
     "open": true, "viewers": 1, "busy": false}]},
  {"root": "/…/other", "sessions": [], "error": "bridge: control agent is not running"}
]}
```

- A row is **open** when its project's live child tracks it as a client's
  session. Every other row is **closed**: stored, with no runtime.
- `viewers` is the relay's count (terminals attached), as the daemon document
  reports it.
- `busy` is the registry's flag.
- `pending` is the open request of a turn the bridge started (D11), absent
  otherwise.
- An open session the listing does not name yet (a session started a moment
  ago) is still a row, with its id and its `lastActive` as `updatedAt`.
- A root whose listing fails carries `error`, and its open sessions still
  list. The route answers `200`.

**D8. `updatedAt` is Unix milliseconds, and `messageCount` is read from
`_meta`.** The child's `session/list` answers `updatedAt` as an RFC3339 string
and `messageCount` under `_meta` (`internal/acp/session.go:778`). The index
emits `contract.SessionRow`'s declared shape, parsing at aggregation.
`GET /api/sessions` keeps relaying the child's raw answer. Changing it is a
non-goal.

**D9. A change notice on the fleet stream, not a poll.** The fleet stream
gains a `sessions` delta, `{"kind":"sessions","project":"<root>"}`. A project
child's registry calls a new `OnChange` hook when it tracks a new session,
drops one, counts a viewer in or out, starts or ends a turn, observes a
followed run start or end, or registers or clears a pending request. The
fleet turns each call into that delta. On it, the app reads
`GET /api/sessions/all?root=<root>` for that host, one read in flight per
host. The app also reads the whole index when it starts following a host.
Nothing polls.

**D10. Two caches, both bounded.**
- **Listings.** A root's listing is cached for 10 s and a failed root for
  30 s. Both are dropped when the control child restarts, through the same
  hook that clears `projectSessions` (`control.go:149`). A root's listing is
  also dropped when a session is tracked on it or a turn ends in it. Those
  are the changes that move a title or a message count, so the next read
  carries them. Other changes, such as a viewer or a turn starting, leave it
  cached.
- **Roots.** `OpenSession` finds a session's root through the same cached
  listings, keeping an `id → root` map that each listing refreshes.

**D11. Asks from the Studio's own turns are the Studio's to answer.** A turn
the bridge started sends its permission and question requests to the bridge's
connection, and `Registry` parks them.
- `Registry` keeps each parked request's payload, and
  `Registry.PendingRequest(sessionID)` answers it as `contract.PendingRequest`.
- The index row carries it. The app shows it as the same ask card an agent's
  request is, in the Inbox, on the Overview and pinned in the thread.
- `fleet.ResolvePermission` and `ResolveQuestion` search the project
  children's registries after the agent runtimes, so the existing answer
  routes reach it.

A turn the terminal started keeps its requests in the session's open
requests (P3 R5), and the terminal answers them. Phase 1 does not show those
in the Studio.

**D12. A session is a fourth `ThreadKind`, opened at the existing thread
place.**
- The kind is design-system work, not a one-off (§10): `ThreadKind.session`
  with the word "Session" and `CupertinoIcons.desktopcomputer`, in
  design-system §6, the kit's vocabulary, the gallery and its goldens.
- A session thread is `ThreadRef(host, id: sessionId, project: root, kind:
  session)`. It opens at `/thread/<host>/<sessionId>`. There is no
  `PlaceKind.session`, and no root in the URL. The bridge resolves the root
  (D1), and the host is already in the path.
- Its status: needs you when `pending` is set; running when `busy`; idle
  otherwise.
- Its inspector has one tab, Inspect, whose routes are all per-session. The
  agent's other tabs use `/api/agents/…`, which a session does not have.

**D13. Sessions live under their project, not on a page of their own.**
design.md §2 puts every thread under a project, and its map sends "Sessions
list" to "Project tree; 'Earlier' under each project". F6 decision 13 put work
items into the tree the same way.
- An open session is a current thread in its project's tree.
- A closed session counts toward the project's `Earlier · N`.
- The project page's Work tab lists the project's sessions after its agents:
  open ones first, then closed ones, newest first. Choosing one opens its
  thread.
- `navAboveProjects` stays 3. There is no Sessions place.

**D14. Opening a closed session is explicit.** The app calls
`POST /api/sessions/{id}/load` before it reads a closed session's stack. That
makes it open, and the next `sessions` delta moves it from Earlier into the
tree. While a session thread is on screen, its event stream counts as
watching it. The idle sweep skips a session whose log has a subscriber.
Unwatched, an idle session is closed by P4's timeout, as a detached terminal
session is. A session closed while the Studio's stream was down answers `404`.
The thread then says it was closed while idle, and offers Reopen, which calls
`load` again.

## Phase 2 direction

Phase 1 must not foreclose this. Phase 2's spec,
[`p8-studio-federation.md`](p8-studio-federation.md), settles the rest.

- **The client is the only place hosts are merged.** Every id on the wire
  stays local to its host, and the app keys everything by host and id, as it
  already does. No bridge namespaces ids, merges another bridge's views or fans
  in another bridge's event streams.
- **What Phase 2 adds is reach, not merging.** A browser reaches one origin,
  and a phone cannot reach a laptop's daemon behind NAT. So a bridge can
  **relay** another daemon's API unchanged under a per-host prefix, and a
  daemon can dial out to a relay to join it. Browser host discovery then lists
  the relay's hosts beside the serving one.
- **A host that cannot be reached is a host state.** It is `HostReach` with
  its last contact (F1 decision 8). The index's per-project `error` stays a
  fact about one root's listing.
- **Known gaps for operating across daemons,** for Phase 2's spec:
  - Automations, Library and Settings read the first host discovery found
    (`app/lib/main.dart:308`, `_Sections._host`). That is every model in their
    sections, from watches, schedules, bots and recipes to skills, plugins,
    MCP, memory, environments, secrets, models, network and usage. The fleet,
    factory and pull-request stores already read every host.
  - New work cannot choose a host: its Host field is a note
    (`new_work_model.dart:243`), and the composer has no host chip
    (design.md §2).
  - Settings › Hosts cannot add a host yet; that row is planned
    (`hosts_model.dart`). Nor is there anywhere for a browser to keep each
    host's token.

## Components and data flow

**Bridge** (`web/bridge`, standard library only):
- **New `sessions.go`.** It holds `ResolveSession`, `OpenSession`, the listing
  cache (`listRoot`), `rootOfSession`, `ownedSessions`, and
  `serveSessionIndex` for `GET /api/sessions/all`.
- **`registry.go`.** It gains `OnChange` and parked-request payloads with
  `PendingRequest`.
- **`control.go`.** `controlChild` replaces `controlRegistryForRoot`.
- **`attach.go` and `projectchild.go`.** They follow client sessions, log
  every session, and wire `OnChange` to the `sessions` delta.
- **`idle.go`.** It skips watched sessions. `EventLog` gains
  `Subscribers(sessionID)`.
- **`fleet.go`.** Answers reach the project children's registries.
- **Contract.** `contract.SessionIndex`, `ProjectSessions`, `SessionRow`,
  `SessionsDelta` and `DeltaSessions` are new. `contract.g.dart` is
  regenerated with
  `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.

**App** (`app/lib/src`):
- **Client.** `BridgeClient.sessionIndex({root})` and `openSession(id)`.
- **Fleet store.** `FleetStore` holds each host's index, reads it with the
  snapshot, and re-reads one root on its `sessions` delta.
- **Threads.** `threadOfSession` in `thread_ref.dart` maps a row to a thread.
  `ThreadKind.session` and `ThreadRef.closed` are new. `isEarlier` counts a
  closed thread as earlier.
- **Asks.** `InboxModel` asks for a session row's `pending`.
- **Screens.** The shell resolves a session's thread place. The project page
  lists sessions. The thread screen gets its Inspect-only inspector and its
  closed-while-idle state.

**Flow.**
1. The app follows a host: it reads `GET /api/sessions/all`, and sessions
   appear in the tree.
2. A terminal starts a turn: the followed run sets `Busy`, `OnChange` fires,
   the `sessions` delta arrives, the app re-reads that root, and the thread
   shows running.
3. The user opens a closed session: `load`, then `/stack`, then
   `/api/events?sessionId=…`.
4. The user sends a prompt: `/prompt`. Read and drive both ride routes that
   exist.

## Error handling

| Condition | Answer |
|---|---|
| resolution misses all three steps | `404 ErrUnknownSession`, as today |
| `load` of a session the bridge owns | `409`, naming the agent, the control agent or the planner |
| `load` of an id no listing names | `404 ErrUnknownSession` |
| one root's listing fails or times out | that group carries `error`; the endpoint answers `200`; its open sessions still list |
| a root failed in the last 30 s | served from the failure cache, without asking the child |
| the project child restarts while a session is open | `resumeAll` resumes and re-follows it on the same child; a call during the restart answers `502` |
| prompt while a turn runs, either side's | `409 turn already in flight` |
| an agent with no stack | `501`, which Dart already models as `StackStatus.unsupported` |
| a session closed by the idle sweep, then addressed | `404`; the thread offers Reopen |

The app maps them onto `StateMessage`'s states:
- **Tree.** Sessions appear in the tree and on the project page. A host's
  index that never answered shows no sessions and no error: the tree's own
  states stand. A failed re-read keeps the rows it holds. A per-root `error`
  shows as one line under the project page's Sessions group, with Retry.
- **Opening a session:**
  - a failed `load` shows the thread screen's error with Retry, never an
    empty transcript;
  - a `404` shows "This session was closed while idle", with Reopen;
  - a `409` on `load` shows the bridge's sentence naming the owner, with Try
    again;
  - a `409` on a prompt is a transient notice in the composer, the path
    agents already use.

## Testing

Bridge tests use `projectFleet` / `projectFleetWith`
(`projectchild_test.go`) and `newFakeProjectChild` (`fakeagent_test.go`). The
plan names each test. The behaviours they prove:

| Behaviour | Proves |
|---|---|
| resolution order | a live runtime, then an agent, then a live project child; a full miss is `404` |
| one home | `OpenSession` resumes on the project child, never on the control child; a later attach finds it tracked and does not resume it again |
| ownership | an agent's (paused included), the control agent's and a planner's sessions are absent from the index and refused by `load` |
| follow | an attach and an open send `session/follow`; a followed run sets `Busy`, so a prompt answers `409` |
| streaming | a followed run's update for a client session reaches `/api/events?sessionId=` |
| listing | no new `Registry` per call: the control child's hooks are untouched after many listings |
| index | grouping, open and closed rows, `updatedAt` and `messageCount` normalised, a dead root's `error` beside its live siblings, a stalled root bounded |
| caches | a failed root is not asked again within 30 s; a control restart drops both caches |
| change notice | each registry change emits one `sessions` delta for its root |
| watched | the idle sweep skips a session with a stream subscriber, and closes it once none remains |
| asks | a parked request's payload is in the row; the answer route resolves it through a project child |

`TestWebIsStdlibOnly` holds the boundary. `TestDartContractIsCurrent` and the
shared fixtures gate the new contract types.

The app tests (`cd app && flutter test`) cover these:
- the store's index read and its per-root re-read on a delta;
- `threadOfSession` and the status rule;
- the nav model putting open sessions in the tree and closed ones in Earlier;
- the shell resolving `/thread/<host>/<sessionId>`;
- the project page's Sessions group;
- a session's ask in the Inbox;
- the thread screen's closed-while-idle state.

Registration in `support/screens.dart` brings `states_test`,
`design_rules_test`, `tooltips_test` and `overflow_test` to bear. The gallery
golden is regenerated on macOS with `--update-goldens`.
`route_parity_test.dart` needs the new row and the changed `load` row in
`docs/studio-flutter/route-parity.md`.

Acceptance is by hand, because composition is not proved by unit tests:
1. Build, restart the daemon with `scripts/dev-studio.sh`, and open the
   Studio with `marshal studio`.
2. With two terminal sessions on one project, both appear under the project.
3. Type a prompt in one terminal: its thread shows running, and its
   transcript streams in the Studio.
4. Send a prompt from the Studio: it moves in the terminal.
5. Prompt from the Studio while the terminal's turn runs: you get the `409`
   notice.
6. Open a closed session from the project page: it reads, then drives.

Not tested: container-plane behaviour (no runtime in CI), Phase 2, and the
macOS-only golden off macOS.

## Assumptions

Each is the conservative choice, made without the user's input:

- **10 s listing cache, 30 s failure cache, 5 s list timeout.** No
  measurement backs the numbers. They keep a burst of deltas off the control
  child's database reads.
- **Following every client session doubles a terminal turn's update traffic**
  between the child and the bridge, as following every fleet agent already
  does. A local daemon carries it.
- **Planner ownership reads the planner store's records** on each index build.
  The store is a directory of small JSON files.
- **The project page lists one page of closed sessions**, the listing's 50.
- **A session's title is the listing's;** an untitled one is called "Session"
  and its short id.

## Non-goals

- Phase 2: the relay, browser discovery of other hosts, the gaps it lists.
- Making the server-plane Studio at `:7700` see a local daemon's sessions.
- Creating sessions from the Studio. New work still starts through spawn.
- Showing the requests of a turn the terminal started (D11).
- Paging closed sessions beyond the first 50.
- The viewer/attach transport for the Studio: REST and SSE are enough to read
  and drive.
- Container-plane sessions, egress, budgets.
- Fixing `GET /api/sessions`' passthrough `updatedAt` (D8, observation only).

## Open questions

None. Every open point is a decision or a named assumption, so the plans can
be executed cold from this document.

## Plans

1. [P7 plan 1, the bridge](plans/2026-11-18-p7-1-bridge-plan.md): resolution,
   one home, follow, the index, the change notice, asks.
2. [P7 plan 2, the Studio](plans/2026-11-18-p7-2-studio-plan.md): the
   session thread kind, sessions in the tree and on the project page, opening
   and asks, acceptance. It runs after plan 1.

## Implementation record

Baseline at `bdd20ddc`, recorded 2026-11-18 while the plans were written:

- `TMPDIR=/private/tmp/p7b go test ./web/... -count=1 -timeout 20m`: every
  package passes (`web/bridge` in 242 s, `contract`, `contract/dartgen`).
- `cd app && flutter test` (Flutter 3.50.0): 3163 passed, 5 skipped, none
  failed.

### Plan 1

Not started.

### Plan 2

Not started.
