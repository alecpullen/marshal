# P7 — sessions in the Studio

Parent: [`design.md`](design.md).
Builds on [`p4-local-daemon.md`](p4-local-daemon.md): its project children, the
unix socket and the Studio listener; [`p5-fleet-screen.md`](p5-fleet-screen.md):
the fleet's read of a daemon; and
[`../studio-flutter/design-system.md`](../studio-flutter/design-system.md) §7
(states) and §10 (extending the system).
Depends on: P4, implemented; the F8 cutover, implemented.
Status: proposed for review, 2026-11-17. **Phase 1 of two.** Phase 2 — peer
daemons in one Studio — is not specified here and inherits this work.

## Goal

A Studio served by a daemon lists every session its projects hold — the ones a
terminal TUI started included — and opens any of them to read and drive.

Today it lists none of them. On a daemon whose only work is TUI sessions,
`GET /api/agents` answers `[]`, and the session routes cannot address them:

```
GET /api/sessions?cwd=/…/marshal   → five sessions, with titles
GET /api/sessions/sess_…/stack     → {"error":"bridge: unknown session"}
GET /api/sessions/sess_…/access    → {"error":"bridge: unknown session"}
```

## Context

Three facts decide the shape of this phase.

**The Studio is a bridge's client, and the two profiles serve the same API.**
`--profile local` already binds a TCP listener (`--addr`), so a daemon can serve
the Studio; the server profile cannot see another daemon's process, so Phase 1
is verified on a Studio served by the local profile. Making the container-plane
Studio at `:7700` show these sessions is Phase 2.

**Sessions are listable but not addressable.** `listSessions`
(`web/bridge/http.go`) resolves a root and falls back to
`controlRegistryForRoot`; every per-session route instead goes through
`registryForSession`, which knows only live agent runtimes, agent ids and
persisted agents. A project-child session is none of those.

**There are three resolution paths, not one.** `registryForSession` (16 call
sites: 14 in `http.go`, plus `images.go:116` and `runs.go:173`), `loadSession`'s
own inline `RuntimeForSession`, and `serveEvents` → `fleet.LogForSession(id)`.
A parameter on one of them fixes a third of the problem.

## Decisions

**D1. The bridge owns session resolution; clients keep their URLs.** The
alternative — an optional `cwd` on the per-session routes, symmetric with
`listSessions` — was proposed first and rejected on reading the call sites: 16
call sites, `loadSession` resolved elsewhere, and a third path in the event
stream. A client parameter also cannot help a client that never had a root.
Consequence: no change to those routes' wire shape, and no regeneration of
`contract.g.dart` on their account (the new index types do regenerate it — see
§Components).

**D2. One rule, in the fleet.** `fleet.ResolveSession(ctx, id)` answers a
`ResolvedSession{Reg, Log, SessionID, Root, Live}` — a struct, not five returns
— for all three paths, in this order:

1. a live agent runtime by session id (`liveRuntimeForSession`);
2. an agent id, then a persisted agent, starting a runtime (`RuntimeForSession`);
3. a live project child whose registry holds the id → that child's `pc.reg`;
4. a known root whose `session/list` names the id → that root's control registry.

Steps 3–4 enumerate the sorted union of `ws.Projects()` and the roots
`f.projects` holds, so two sources cannot disagree about the order. Each
`session/list` carries a 5 s timeout. One page per root — the child's default
cursor — so the index lists the most recent sessions; older ones are out of
reach until a later phase decides paging.

**D3. Step 3 precedes step 4 for correctness, not speed.** Loading a live TUI
session onto the control child would give two ACP children the same persisted
session: two writers on one transcript. Preferring the live child keeps
`Registry`'s `Busy` guard in force between the TUI and the browser, so a
concurrent prompt is a `409 turn already in flight` rather than a corrupted
transcript.

**D4. `GET /api/sessions/all`** is the daemon-wide list, grouped and never
null:

```json
{"projects": [
  {"root": "/…/marshal", "sessions": [
    {"sessionId": "sess_…", "title": "…", "updatedAt": 1763389530000, "messageCount": 71}]},
  {"root": "/…/other", "sessions": [], "error": "bridge: control agent is not running"}
]}
```

It excludes, server-side, sessions backing a live agent runtime (`rt.sessionID`)
so no row duplicates an agent thread, and `info.fleet` sessions, which
`Registry.summaries` already skips. It carries no `viewers`: that is
`marshal fleet`'s datum.

**D5. `updatedAt` is normalised to Unix milliseconds.** `listSessions` today
relays its child's raw `session/list` answer, whose `updatedAt` is an RFC3339
string, while `contract.SessionSummary` declares Unix milliseconds; no client
parses it, so the mismatch has never surfaced. The new route emits
`contract.SessionSummary`'s declared shape, parsing the child's string at
aggregation. Changing `/api/sessions` is a non-goal.

**D6. A cache, bounded and invalidated.** `id → root` and failed roots are
cached on the fleet (10 s for a hit, 30 s for a failure), dropped when the
control child restarts — the hook `control.go` already uses to clear
`projectSessions`. Without the failure cache a 5 s poll would re-handshake a
dead container runtime every tick.

**D7. The Studio gains two places, not one.** `PlaceKind.sessions` is the
list (`/sessions`, no target); `PlaceKind.session` is one open session
(`/sessions/<sessionId>?p=<root>`). `navAboveProjects` goes 3 → 4 so the list
sits above Projects beside Overview/Inbox/Pulls. A session's place carries its
root because the route needs one and the URL must round-trip (F2 D4).

**D8. A session is a fourth `ThreadKind`.** `ThreadScreen` requires a
`ThreadRef` for its header, so building one is cheaper than a parallel screen.
This is design-system work, not a one-off (§10): the kind, its icon and
tooltip, a `support/screens.dart` fixture per state, and a macOS golden.

**D9. The per-group `error` is the Phase 2 shape.** Phase 2's unreachable peer
reports in the same slot, so federation adds producers rather than a mechanism.

## Components and data flow

**Bridge.** `fleet.ResolveSession` (D2) replaces the resolution bodies of
`registryForSession` and `loadSession`, and `LogForSession` becomes a wrapper
over it so the event stream cannot drift from the REST routes.
`serveSessionIndex`
answers D4 from `ws.Projects()`: for each root the live child's `pc.reg` when
one exists, else a control registry, each `session/list` under its own timeout.
`contract.SessionIndex` and `contract.ProjectSessions` are new; `contract.g.dart`
is regenerated with
`go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.

**App.** `PlaceKind.sessions` / `PlaceKind.session` and their parser (D7);
`SessionIndex` in `BridgeClient` as `sessionIndex()` and
`loadSession(id, cwd)`; `sessions_model.dart`, a `ChangeNotifier` polling
`GET /api/sessions/all` every 5 s while mounted and nowhere else;
`sessions_screen.dart`, kit-only, grouped by project, a row per session with its
title, relative age and message count, opening on `⏎`. Opening builds
`StackStore(client, sessionId)` and `ComposerModel.forSession(client, sessionId)`
— both already take a bare session id — after `POST /api/sessions/{id}/load`
when the row was not `live`.

**Flow.** Mount → `GET /api/sessions/all` → rows. Open → `load` if not live →
`StackStore` fetches `/stack` and subscribes `/api/events?sessionId=…` →
`ComposerModel` posts `/prompt`. No new transport: read and drive both ride
routes that exist.

## Error handling

| Condition | Answer |
|---|---|
| resolution misses after all four steps | `404 ErrUnknownSession`, as today |
| one root's control agent fails to start, or times out | that group carries `error`, the endpoint answers `200` |
| a root's `session/list` stalls | its own timeout; the request still returns |
| a root failed last time | served from the failure cache for 30 s |
| the child dies between resolution and `load` | resolved once more, then `404`; never loaded into a second child |
| prompt while the TUI is mid-turn | `409 turn already in flight`, unchanged |
| an agent with no stack | `501`, which Dart already models as `StackStatus.unsupported` |

The app maps these onto the five states `StateMessage` already has: `loading`;
`empty` when every group is empty, with **Refresh** as its one action (named in
§Assumptions as the weak point — Phase 1 creates no sessions, so there is no
honest "start one"); `stale(age)` when a poll fails with rows held, which never
clears the rows; `error` with retry when the first fetch fails and nothing is
held; `unavailable` on `404`, which is real because an older bridge is
reachable. Opening adds three: a failed `load` shows the thread screen's error
with retry, never an empty transcript; a session deleted while open answers
`404` from `/stack` or the stream and says the session is gone; a `409` is a
transient notice in the composer, the path agents already use.

## Testing

Bridge, with `ownChildFleet` (`ownviewers_test.go`, the production factory on
its host-process fallback) and `newFakeProjectChild`:

| Test | Proves |
|---|---|
| `TestResolveSessionByLiveChild` | step 3 resolves to `pc.reg`, `live=true` |
| `TestResolveSessionByKnownRoot` | step 4 resolves an ended session, `live=false` |
| `TestResolveSessionPrefersLiveChild` | D3 — the live child wins when both could answer |
| `TestResolveSessionUnknownIs404` | a full miss keeps `ErrUnknownSession` |
| `TestSessionIndexGroupsAndSkipsAgentSessions` | a live agent's session and `fleet` sessions are absent |
| `TestSessionIndexPerProjectError` | one dead root yields `error` and `200` |
| `TestSessionIndexBoundsSlowRoot` | a stalled child times out; the request returns |
| `TestSessionIndexCachesRootFailure` | D6 — no re-handshake per request |
| `TestSessionIndexNormalisesUpdatedAt` | D5 |
| `TestLoadSessionResolvesProjectChild` | the second path gets the same rule |
| `TestSessionEventsResolveProjectChild` | the third path streams for a project-child session |

`TestWebIsStdlibOnly` holds the boundary; `TestDartContractIsCurrent` gates the
new contract types.

App (`cd app && flutter test`): `sessions_model_test.dart` (poll success, a
failed poll keeps rows, `404` → unavailable, a per-project `error` beside its
siblings); `sessions_screen_test.dart` (the five states, and a row opening);
registration in `support/screens.dart` is what brings `states_test`,
`design_rules_test`, `tooltips_test` and `overflow_test` to bear, and the
gallery golden is generated on macOS with `--update-goldens`.
`route_parity_test.dart` needs rows for `GET /api/sessions/all` and
`POST /api/sessions/{id}/load` in `docs/studio-flutter/route-parity.md`, and
`boundary_test.dart` requires the screen to read `model/` and never `client/`.

Acceptance, by hand, because composition is not proved by unit tests: build,
run the local profile with `--addr 127.0.0.1:7700`, open the Studio, see the
five sessions in the sidebar, open one, read its transcript, send a prompt, and
watch that session move in its terminal.

Not tested: container-plane behaviour (no runtime in CI), Phase 2, and the
macOS-only golden.

## Assumptions

Choices made without the user's input, each the conservative reading:

- **Poll 5 s, resolution cache 10 s, failure cache 30 s.** No measurement backs
  the numbers; they are the smallest that keep the poll off the control
  handshake.
- **`empty`'s action is Refresh.** §7 requires one and the phase creates no
  sessions, so "start a session" would be a lie. The weak mapping is here to be
  revisited, not hidden.
- **The opened session's root travels as `p=<root>`** in the URL rather than a
  path segment, since a root contains slashes; `p` joins the honoured query
  keys.
- **Sessions do not also appear under their project's thread list.** The
  Sessions place is the only surface, so nothing is drawn twice.
- **Sixteen call sites** is a count from `grep`, not from the compiler.

## Non-goals

- Peer daemons, merged views, id namespacing, action forwarding and SSE fan-in —
  Phase 2.
- Making the server-plane Studio at `:7700` see the local daemon's sessions —
  Phase 2.
- Creating sessions from the Studio.
- The viewer/attach transport: REST and SSE are enough to read and drive.
- Container-plane sessions, egress, budgets.
- Fixing `GET /api/sessions`' passthrough `updatedAt` (D5, observation only).

## Open questions

None.
