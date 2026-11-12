# P5 — the fleet screen

Parent: [`design.md`](design.md) §4.4, §4.6 and §7 (P5). The fleet loop and
the screen design come from [`docs/fleet-cli/design.md`](../fleet-cli/design.md):
its §1 is the done-when's loop, and its §3 to §5 are the screens, keys,
notifications and connection rules this spec carries over. Where this spec
differs from either document, this spec wins, and the "Decisions" section
lists each difference.
Builds on [`p4-local-daemon.md`](p4-local-daemon.md): its daemon, its attach
relay and its fleet agents on project children.
Depends on: P4, implemented (all ten plans merged at `7ae94a9b`; its hands-on
acceptance run is still to do).
Status: approved, 2026-11-12, with all ten decisions as recommended. The
Behaviour section is still headings only and is written before the first plan.
The line anchors were checked at `7ae94a9b`.

## Goal

From one Go TUI, the user runs the fleet loop against the local daemon and
against a server-profile bridge on another machine. The loop is:

- see every agent and what needs them;
- spawn an agent;
- open an agent's session to watch, prompt, steer and answer it;
- review its diff;
- finish it: exit with the build-and-test gate and its override, merge, or
  discard;
- go back to the fleet, leaving the agent running.

The design's done-when for P5: the fleet loop from `docs/fleet-cli/design.md`
§1 works from the Go TUI against the local and server profiles.

## Where the code is today

- **The fleet's surface is REST plus one event stream.**
  - The routes are in `web/bridge/http.go:164` to `:268`.
  - `GET /api/agents` answers `Fleet.Snapshot()` as a list of `AgentStatus`
    (`web/bridge/fleetevents.go:355`).
  - `GET /api/events?stream=fleet` (`http.go:673`) streams deltas whose type,
    `fleetDelta`, is unexported (`fleetevents.go:9`).
  - `GET /api/pending` lists intake submissions as untyped maps
    (`http.go:1248`).
  - On the local socket, `SocketHandler` serves the whole mux without the
    bearer check (`http.go:120`). So a client on the socket can use every route
    as it is today.
- **The contract is written twice.**
  - The response types are Go structs in package `bridge`, a 231-file package
    that nothing outside `web/` should import. Some of them are unexported, and
    the pending list is a map.
  - The browser's types are written by hand in `web/ui/src/lib/api.ts` (1,414
    lines), so drift between the two shows up only when something breaks.
  - `internal/daemon` keeps its own copies of `GET /api/daemon`'s types
    (`internal/daemon/client.go:27`, `:49`, against
    `web/bridge/daemoninfo.go:9`).
- **The views are computed in the browser.**
  - Grouping and urgency order are `groupAgents` (`web/ui/src/lib/fleet.ts:74`),
    and the needs-you list is `buildInbox` (`web/ui/src/lib/inbox.ts:23`).
  - Both run over rows that `applyDeltaTo` keeps current from the deltas.
- **The TUI has no fleet client, and it may not import one freely.**
  - Its boundary test allows only `acpclient`, `acpwire` and a short list of
    data packages (`internal/app/tui/boundary_test.go:27`).
- **Daemon mode runs one session per program run.**
  - `runDaemon` (`internal/launch/daemon.go:176`) dials a relay, runs
    `tui.New(host, session, …)` and dials again when the TUI asks to resume
    another session (`:236`).
  - Nothing swaps a running model's host or session.
- **Only some agents can be reached by a viewer.**
  - An attach names a project root and reaches only that project's child
    (`web/bridge/attach.go`).
  - A live fleet session is refused with 409 (`attach.go:101`), because the
    relay would let the viewer close the agent's session.
  - Only local agents with no git source, no workspace and no egress proxy run
    on a project child (`web/bridge/fleet.go:818`). Every other agent has its
    own child, which serves no viewer socket:
    - a host process speaks over stdio (`web/bridge/child.go:328`);
    - a container runs `marshal acp --listen` on a socket in its mounted socket
      directory (`web/bridge/container.go:174`).
  - `--viewers` refuses `--listen` (`cmd/marshal/main.go:95`).
- **The relay already reads the viewer's frames.** It decodes each one to
  watch for a replacing `session/new` and for `session/close`
  (`web/bridge/relay.go:190`), so refusing a method there needs no new
  parsing.
- **Attaching is the socket's only.**
  - The TCP listener answers the attach and pin routes 404 (P4 D3, D9;
    `http.go:117`).
  - A remote client has REST over TLS with the bearer token, but no session
    stream.
- **Two kinds of request, two places.**
  - A fleet agent's approvals and questions are brokered by the bridge. They
    show on `AgentStatus.Pending` and are answered by
    `POST /api/permissions/{id}` and `POST /api/questions/{id}`.
  - A TUI session's requests are its own mirror's (P3 R5), so a detached TUI
    session parked on an approval is on no one's list.

## In scope

1. One contract package for the fleet loop's types, shared by the bridge and
   the Go client, with the TypeScript types generated from it.
2. Server-computed views: grouping, urgency order and the needs-you list come
   from the bridge.
3. A Go fleet client over the local socket or TLS, behind an interface the TUI
   renders from.
4. The fleet screen in the TUI (fleet-cli §3), started by `marshal fleet`.
   Opening an agent shows its session screen, and leaving that screen returns
   to the fleet.
5. Opening any agent's session as a viewer that cannot end or replace it.
6. A needs-you count on the session screen's status line, and desktop
   notifications.
7. Server profiles: named servers, TLS and the token, the attach over TLS, and
   viewer sockets on agents' own children, containers included.

## Decisions

All ten were approved as recommended on 2026-11-12. Each gives the decision
first, then the alternative it was weighed against.

1. **The contract is one standard-library package, and the TypeScript types
   are generated from it.**
   - **Where.** `web/bridge/contract` holds types only: no behaviour, the JSON
     tags as they are today, and the same bytes on the wire.
   - **Who uses it.** The bridge's handlers encode its types, and
     `internal/daemon` and the new client decode them.
   - **The TypeScript side.** A standard-library generator in the same tree
     writes `web/ui/src/lib/contract.ts` by reading the package's syntax. A Go
     test fails when the checked-in file is stale, and `api.ts` re-exports the
     generated types in place of its hand-written ones.
   - **What moves in P5.** Only the loop's types: agents, deltas, pending and
     intake, projects, spawn, diff, gate, exit, merge, discard, budgets and the
     daemon's info. The rest move when a change touches them.
   - **The TUI** may import the package as a data package, which is one new
     entry in `seamImportAllowed`.
   - **Alternatives:**
     - Go client types kept by hand, with a contract test against recorded
       responses. It catches drift only where a fixture exists.
     - A JSON Schema as the source, which is a third language to keep.
2. **The bridge computes the inbox, and streams it as one value.**
   - **The route.** `GET /api/inbox` answers the four groups (needs you,
     running, ready, earlier) as ordered agent IDs. The needs-you group also
     carries the intake submissions, with each item's time. The rules are
     today's `groupAgents` and `buildInbox`.
   - **The stream.** The fleet stream gains an `inbox` delta carrying the same
     value. The bridge recomputes it after every fleet event and on a 1s tick,
     and sends it only when it differs.
   - **The clients** keep the rows themselves current from the existing deltas,
     and take membership and order from the bridge. The web UI's sidebar and
     Home move onto it, and `groupAgents` and `buildInbox` are deleted.
   - **Owner scoping** stays as today: one local owner.
   - **Alternatives:**
     - Each row carries its group and rank. Rank is relative, so one agent's
       change would send a delta for every agent it passes.
     - The grouping logic ported to Go and run in both clients, which the design
       rejected so that both clients show the same thing.
3. **The TUI reads the fleet through an interface, as it reads a session.**
   - **The client.** `internal/fleetclient` speaks REST and the fleet stream
     over a transport: the unix socket locally, HTTPS with the bearer token
     remotely. It refetches the snapshot after a `replay_overflow`.
   - **The interface.** `acpclient.Fleet` gives synchronous reads (agents,
     inbox, projects, budgets) and a change signal, as `acpclient.Session`
     does, plus the actions as calls:
     - spawn;
     - answer a permission or a question;
     - read a diff and a commit draft;
     - verify, exit, merge and discard;
     - approve or deny an intake.
   - **The TUI** renders from the contract's types through that interface, and
     never sees the transport.
   - **Alternative:** the TUI calls the client package directly. That breaks
     P2's rule that the TUI reaches everything through `acpclient`, and leaves
     no seam for a test fake.
4. **The fleet screen is its own program in daemon mode's loop.**
   - **Starting it.** `marshal fleet` runs a fleet model
     (`internal/app/tui/fleet`) in launch's daemon loop. So does `/fleet` from
     a daemon session screen, which detaches the session first.
   - **Opening an agent** ends the fleet program with the agent to open. The
     loop then runs the session screen over that agent's relay, exactly as P4
     runs it. Leaving the session screen, by quitting or `/fleet`, returns to
     the fleet program instead of the shell.
   - **Why.** One program at a time keeps the session screen as it is. It
     reuses `runDaemon`'s loop (`launch/daemon.go:236`), and it never needs a
     model to swap its host while running.
   - **What changes from fleet-cli §3.**
     - The right-hand panel keeps the Diff and Info tabs, and a Transcript tab
       showing the agent's activity line.
     - The full transcript is the session screen, which `⏎` opens.
   - **Embedded mode** has no fleet screen (design §4.5). `marshal fleet`
     needs a daemon or a server profile, and says so otherwise.
   - **Alternative:** one model holding both screens, switching sessions live
     with a second stream inside it. Nothing supports that today, and it would
     put two transcripts' state in one model.
5. **A fleet agent's session opens as a viewer that cannot end or replace
   it.**
   - **The route.** A new `POST /api/agents/{id}/attach`, with the same upgrade
     handshake as the project attach, resolves the agent's session and child
     and relays to that child's viewer socket.
   - **What the relay refuses.** For an agent's session it answers
     `session/close` and a replacing `session/new` itself, with an error the
     TUI shows. The child already refuses load, resume and delete on viewers
     (P4 plan 3).
   - **The lifecycle stays the fleet's**, through the finish flow.
   - **The TUI** learns from the attach answer that the session is an agent's
     (a `Marshal-Agent` header). Its `/exit` and `/new` then say to finish the
     agent from the fleet screen.
   - **Answers.** A fleet agent's request is brokered by the bridge and also
     listed in a viewer's mirror (P4, decision 8's last sentence). The fleet
     screen answers through the bridge, and the session screen through its
     mirror.
   - **The project attach keeps its 409, and widens.** It also refuses a
     session ID that any agent record names, paused or live, which closes P4
     H15.
   - **Alternative:** let the project attach take a fleet session under a
     takeover rule. That gives one session two owners.
6. **Every agent gets a viewer socket, and a container's viewers get an
   allowlist.**
   - **Agents on a project child** are reachable already.
   - **Host-process agents with their own child** (git-sourced or workspace
     agents, locally, and a server-profile bridge with no container runtime)
     start their child with `--viewers` on a socket in the state directory.
     `RunWithViewers` already serves stdio and viewers together.
   - **A containerized agent's** `marshal acp --listen` also takes `--viewers`,
     on a second socket in the mounted socket directory. The bridge dials that
     socket from the host side. This lifts the refusal at
     `cmd/marshal/main.go:95` for that pairing.
   - **The allowlist.** The design never lets credentials enter an agent
     container, and a marshal client's host calls include provider keys,
     provider and MCP sign-ins, and config writes (P2 plans 7a, 7b and 7d). So
     a container's viewer gets an allowlist, enforced in the relay and again
     in the agent when it runs in a container:
     - the session screen's turns, controls, answers and reads;
     - its notes and its browse calls.

     Anything else is refused with a sentence saying it is not available for
     a containerized agent.
   - **Alternatives:**
     - A denylist of credential methods, which a new method could slip past.
     - Containers watch-only from the TUI, which fails the loop: the user
       could not prompt or answer.
7. **A server profile is a named server in its own file, with the token
   kept out of it.**
   - **The file.** `~/.config/marshal/fleet.toml`, which follows
     `MARSHAL_CONFIG_DIR` and `XDG_CONFIG_HOME` and must be mode `0600`. It
     names servers by URL, as fleet-cli §2.3 does. A server is chosen with
     `marshal fleet --server NAME`; with none named, the fleet uses the local
     daemon.
   - **The token** comes from the profile's `token_cmd`, or from the keychain
     where `marshal fleet login NAME` stored it through
     `internal/credentials`. The file has no plaintext `token` field.
   - **Certificates.** TLS uses the system roots, plus an optional `ca_file`
     per profile for a self-signed homelab.
   - **The attach over TLS.** The TLS listener serves the agent attach, with
     the bearer token, and refuses it over plain HTTP on a non-loopback
     address. The project attach and pin stay socket-only, since a server has
     no project children to reach.
   - **A remote session** dials with `SharedFilesystem: false`, so the session
     screen's local-file features stay off (P2 `Host.SharedFilesystem`).
   - **Alternatives:**
     - Servers in `config.toml`. The file would then hold a credential, and
       project configs merge into it.
     - Fleet-cli's plaintext `token` field.
8. **The needs-you count and notifications run wherever the fleet is open.**
   - **The count.** In daemon mode, the session screen also gets an
     `acpclient.Fleet`. Its status line shows `needs you N` for agents other
     than the one shown, read from the inbox.
   - **Notifications.** The fleet screen notifies as fleet-cli §4 does: a bell
     and a desktop notification on "needs you" and "ready", a burst for one
     agent within 2s collapsed to one, and `notify = false` per profile. A
     session screen opened from the fleet keeps notifying.
   - **The side effect** lives outside the TUI: launch passes a notifier in,
     as it passes the clipboard.
   - **Alternative:** notifications only on the fleet screen, which would miss
     every agent while the user works in one session.
9. **The daemon's TUI sessions are listed, but not watched for requests.**
   - **The list.** The fleet screen lists the local daemon's own sessions,
     from `GET /api/daemon`, in a Sessions group. `⏎` opens one through the
     project attach, as `marshal attach` does.
   - **Their requests.** A marshal session's requests are not brokered (P3
     R5), so they stay out of needs-you. That is follow-up H3.
   - **Alternative:** leave them out, and keep `marshal attach` as the only way
     back to them.
10. **The web UI over the relay stays out of P5** (P4 H3). The web UI keeps its
    routes, gains only the server-computed inbox, and still cannot answer a
    TUI-started run's requests.

## Out of scope (follow-ups)

- **H1.** Several servers at once, per-agent notification muting, and a
  shell into an agent (fleet-cli §7).
- **H2.** The web UI over the relay, and the web UI answering a TUI-started
  run's requests (P4 H3).
- **H3.** The daemon's TUI sessions in needs-you. The bridge would need to read
  a marshal session's pending requests, which it cannot today.
- **H4.** Workspace, template, credential, secret and egress management from
  the TUI, which fleet-cli §1 leaves to the web UI.
- **H5.** An inbox scoped to more than one owner (design non-goal).
- **H6.** P4's open follow-ups that P5 does not close: H4 (drain on upgrade),
  H5 (watching config and trust), H6 (design §8's server-profile questions),
  H7 (worktree indexes), H8 to H14. This spec closes H15 (decision 5).

## Behaviour

The decisions are settled; this section is written next, before the first
plan, as P4's was. Each heading below says what it will pin.

- **F1. The contract.** Which types move; byte-identical encoding; the
  generator and its staleness test; the TUI's one new allowed import.
- **F2. The inbox.** The group rules (today's `groupAgents`: pending, then
  running, then idle with changes and no PR, else earlier; urgency by
  approval before question, then status, then most recent); intake items in
  needs-you, oldest first; when the delta is sent.
- **F3. The fleet client.** Transports, reconnect with backoff (1s to 30s),
  the snapshot refetch on overflow and on reconnect, how a 401 stops the
  streams without prompting (fleet-cli §5).
- **F4. The fleet screen.** Fleet-cli §3's panels, widths, glyphs and keys,
  with decision 4's changes; the Sessions group; `marshal fleet` and `/fleet`.
- **F5. Opening an agent.** The agent attach, the relay's refusals, the
  `Marshal-Agent` header, the session screen's `/exit` and `/new` for an
  agent, and the way back to the fleet.
- **F6. Viewer sockets.** Paths, permissions and cleanup for own-child and
  container viewers; the container allowlist at both ends.
- **F7. Server profiles.** The file's shape and checks, the token sources,
  `marshal fleet login`, TLS, and the attach over TLS.
- **F8. Needs you and notifications.** The status line count, transition
  detection and burst collapse, and the notifier seam.

## Migration order

The plans are written in this order once the Behaviour section is.

1. **The contract.** The package, the generator, the bridge and
   `internal/daemon` moved onto it, with the bytes on the wire pinned by
   recorded responses. It runs beside plan 2.
2. **The inbox.** `GET /api/inbox` and the `inbox` delta; the web UI's sidebar
   and Home on it.
3. **The fleet client and a read-only fleet screen.**
   - `internal/fleetclient` and `acpclient.Fleet`.
   - `marshal fleet` showing the agents, needs you and events, local only.
   - It needs plans 1 and 2.
4. **Opening an agent.**
   - The agent attach and the relay's refusals.
   - The fleet-to-session loop in launch, for agents on a project child.
   - The widened 409.
   - It needs plan 3.
5. **The fleet's actions.** Spawn, answering, the diff tab, the finish modal
   (exit, gate failure and override, merge, discard), and intake. It needs
   plan 3, and runs beside plan 4 if their files stay apart: plan 4 is
   launch, the relay and the bridge's route, plan 5 the fleet screen's panels.
6. **Viewers everywhere, and needs you.**
   - Viewer sockets on host-process agents' own children.
   - The session screen's needs-you count, and notifications.
7. **Server profiles.** `fleet.toml`, the HTTPS transport, `token_cmd` and the
   keychain, `marshal fleet login`, and the agent attach over TLS.
8. **Containers.**
   - `--listen` with `--viewers`.
   - The container viewer's allowlist in the relay and in the agent.
   - It needs plans 6 and 7.

## Tests

- **W1.** Each moved type encodes byte for byte as the bridge's recorded
  responses do, and the generated TypeScript is current.
- **W2.** The bridge's inbox equals today's `groupAgents` and `buildInbox` on
  their own test cases, ported from `web/ui`.
- **W3.** The fleet screen's golden frames at 80×24, 100×30 and 140×40, with
  and without `NO_COLOR`.
- **W4.** Against a real bridge (`test/daemon`), open an agent from the fleet
  screen, prompt it, leave, and land back on the fleet screen with the agent
  still running.
- **W5.** A viewer of an agent's session cannot close or replace it; the agent
  still runs and the fleet still lists it.
- **W6.** The project attach refuses a paused agent's stored session.
- **W7.** The finish flow against a real bridge: a failed gate, then the
  override, then the result.
- **W8.** A server profile over TLS: a token from `token_cmd`, a remote agent
  attach, and the attach refused over plain HTTP on a non-loopback address.
- **W9.** A container viewer's refused method gets an error and never reaches
  the agent, at both ends.
- **W10.** The needs-you count changes when another agent parks on an
  approval.
- **W11.** Notification transitions and the burst collapse.

## Done when

- The design's done-when holds: the fleet loop of fleet-cli §1 works from the
  Go TUI against the local daemon and against a server-profile bridge running
  containerized agents.
- W1 to W11 pass. `TestWebIsStdlibOnly` and `TestTUIStaysOnTheClientSeam`
  pass, with one new allowed import: the contract package.
- A manual run covers the loop on both profiles: spawn, open, prompt, an
  approval, the diff, a finish with a failed gate and its override, a discard,
  and a notification.

## Findings while drafting

- **The done-when's reference was never committed.** `docs/fleet-cli/design.md`
  is untracked in the main checkout, though `design.md` §7 cites it. It is
  committed with this spec, with a note on which parts carry over.
- **A remote marshal client in a container would reach the credential
  methods** (by reading). A viewer's frames go to the agent unchanged, and the
  P2 seam's host calls write provider keys and run sign-ins. That is why
  decision 6 adds an allowlist, which a relay into a container must have
  before it ships.
- **The intake list is untyped** (`http.go:1248`), and `fleetDelta` is
  unexported, with fields that are kept off the wire (`json:"-"`). The
  contract has to type both without changing the bytes.
- **Not yet checked:** whether a viewer's answer settles the bridge's brokered
  request for the same approval, so that the fleet screen stops listing it.
  Decision 5 assumes the first answer wins on both paths. Plan 4 must test
  it before relying on it.
