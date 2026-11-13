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
Status: approved, 2026-11-12, with all ten decisions as recommended.
Behaviour written 2026-11-13; plans 1 and 2 are written. The line anchors were
checked at `d5e63dcd`.

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
  - Some changes reach the browser only on a refetch, because no delta carries
    them: a turn starting or ending, a request being answered, and an intake
    submission arriving.
- **`web/` may not import itself, and its types are never checked.**
  - `TestWebIsStdlibOnly` refuses every `marshal/` import
    (`web/bridge/boundary_test.go:68`), including a package under `web/`.
  - The web UI's build and tests strip its types without checking them, and
    `svelte-check` reports 6 errors.
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

### F1. The contract

- **The package.** `web/bridge/contract` holds the fleet loop's wire types and
  nothing else: no functions, no methods and no state. Beside the types it
  declares only the constants of its enumerations and one list of method names
  (F6). It imports only the standard library.
- **What moves in plan 1** (decision 1's list):
  - agents: `AgentStatus`, with `AccessState`, `PendingRequest`,
    `GateOverride` and `AgentWorkspace`;
  - the fleet stream: `FleetDelta`, which is today's `fleetDelta` with its
    wire fields in today's order; `BudgetDelta`; and `ProjectRemovedDelta`, in
    place of the map at `http.go:434`;
  - intake: `PendingSubmission`, in place of the map `listPending` builds
    (`http.go:1248`), and the approve and deny answers;
  - projects: `ProjectStatus`;
  - spawn: the request body, which `spawnAgent` decodes into an anonymous
    struct today, and its answer, a map today (`http.go:540`);
  - finishing: `DiffFile` and `DiffResult`, the commit draft, `GateResult` and
    `GateRecord`, the exit request and `ExitResult`, the merge request and
    `MergeResult`, and the discard answer;
  - budgets: `Budgets`, and the report `GET /api/budgets` answers, with its
    per-agent rows;
  - the daemon: `DaemonInfo`, `DaemonChild`, `DaemonSession`, `DaemonRefresh`
    and `PinState`.

  Answers to permissions and questions move in plan 5, which is their first Go
  client.
- **The bytes do not change.**
  - The bridge keeps its names through type aliases, so moving a type changes
    no code that uses it. That includes the unexported ones: `gateRecord`
    becomes an alias of `contract.GateRecord`.
  - `fleetDelta` keeps two fields off the wire (the access state's session,
    and the usage rows). It becomes a bridge type that embeds
    `contract.FleetDelta` and keeps those two unexported, so they are never
    encoded, as today.
  - Where today's encoder is a map, the struct declares its fields in the
    map's sorted key order, which is the order `encoding/json` writes a map
    in. None of those fields is `omitempty`, because the map writes every
    entry today.
  - The bridge forwards some of the agent's replies unread: the diff, the
    commit draft and the merge. It keeps doing so. The contract describes them
    for the clients, and a test in `internal/acp` checks that the agent's reply
    types decode into the contract's types without loss.
  - W1 records today's answers before anything moves, and compares every later
    answer with them byte for byte.
- **Enumerations are named string types.** These each get a named string type
  with a constant per value: an agent's status, a pending request's kind, a
  project's trust, a delta's kind, and a workspace's source. Go code cannot
  misspell one, and the generator turns each into a TypeScript union of its
  values, as `api.ts` declares them by hand today.
- **The generator** is `web/bridge/contract/tsgen`. It is a standard-library
  package that parses the contract package's source with `go/parser` and
  writes `web/ui/src/lib/contract.ts`.
  - A struct becomes an interface, keyed by the JSON names. A field that is
    `omitempty` is optional. A pointer that is not `omitempty` may be `null`. A
    field tagged `-`, or unexported, is left out.
  - The basic types map as follows:
    - `string`, `bool` and every number type become `string`, `boolean` and
      `number`;
    - `time.Time` becomes `string`;
    - `json.RawMessage` becomes `unknown`;
    - a slice becomes an array;
    - a map with string keys becomes a `Record`.
  - A named string type with constants becomes a union of their values. Any
    other named type keeps its name.
  - Doc comments carry over. The file starts with a line saying it is
    generated, and by what.
  - The output is deterministic: files in name order, and declarations in
    source order.
- **Staleness.** `TestTypeScriptContractIsCurrent` fails when the checked-in
  file differs from the generator's output. Its message gives the command that
  rewrites the file: the same test run with `-update`. Nothing in the build runs
  the generator.
- **The TypeScript side.**
  - `api.ts` deletes its own declarations of the moved types, and re-exports
    the generated ones under the names its callers use today.
  - Where a generated name would clash with a web UI name, the re-export
    renames it. For example, `AccessState` is already the parsed state in
    `interaction.ts`.
  - `fleet.ts` keeps its union of per-kind deltas, but declares each member
    from the generated `FleetDelta`. A field renamed in Go then fails the type
    check there too.
- **The type check.**
  - The web UI has no type-check step: `vite build` and `vitest` strip types
    without checking them.
  - `svelte-check` reports 6 errors today: four in test files, and one each in
    `Chat.svelte` and `Run.svelte`.
  - Plan 1 adds `npm run check`, which runs `svelte-check`. Its gate is that the
    count does not rise. Fixing the six is not P5's work.
- **Who imports the package.**
  - The bridge, through the aliases.
  - `internal/daemon`. Its mirrored types (`client.go:27`, `:49`) become
    aliases of the contract's, so `cmd/marshal` and `internal/launch` do not
    change.
  - The fleet client (plan 3), and the TUI, which renders from these types.
    The TUI's import is one new entry in `seamImportAllowed`, added by plan 3,
    the first TUI code to need it.
- **The web boundary.** `TestWebIsStdlibOnly` forbids every `marshal/` import
  today (`web/bridge/boundary_test.go:68`). It would therefore refuse the
  bridge's own import of the contract.
  - Plan 1 changes it so a package under `web/` may import another one under
    `marshal/web/`. Every other module path is still refused.
  - That keeps the rule's intent: `web/` must not depend on the agent's
    packages, and the contract is not one of them.

### F2. The inbox

- **The value.** `contract.Inbox` has two parts.
  - `groups` is four ordered lists of agent IDs: `needsYou`, `running`,
    `ready` and `earlier`.
    - An agent goes in the first group it qualifies for. Needs you: it has a
      pending request. Running: its status is `running`. Ready: it is idle, has
      changed files, and has no pull request. Otherwise it goes in earlier.
    - Within a group, agents are ordered by pending kind (approval, then
      question, then none). Then by status, awaiting first, then error, then
      running, then idle. Then the most recently updated comes first.
    - Ties keep the snapshot's order.

    These are `groupAgents`' rules (`web/ui/src/lib/fleet.ts:74`).
  - `queue` is what needs the user, oldest first.
    - It has one item per agent in `groups.needsYou`, dated by its
      `updatedAt`.
    - It has one item per intake submission, dated by its `createdAt`. The
      item carries the submission itself.
    - On a tie, agents come before submissions, each kind in its own order.

    These are `buildInbox`'s rules (`web/ui/src/lib/inbox.ts:23`).

  Times are compared as times. The browser compares them as strings, which
  misorders two times in the same second when only one has a fraction.
- **Owners.** The inbox covers every owner.
  - The web UI's Home keeps its "Mine" scope by dropping the items another
    owner has, which keeps the bridge's order. An unset owner, or `local`, is
    the user's.
  - The Go client has one owner, and no scope.
- **The route.** `GET /api/inbox` answers the current value. It is on both
  listeners, with the bearer check on TCP, as `GET /api/agents` is.
- **The delta.** An `inbox` delta on the fleet stream carries the whole value.
  - A fleet event wakes the bridge's inbox loop, which recomputes 250ms later,
    so a burst of events costs one recompute.
  - The loop also recomputes every second. Some changes emit no event:
    - a turn starting or ending;
    - a request being answered;
    - an intake submission arriving or expiring.
  - It sends the value only when the value's encoding differs from the last one
    sent. Every activity, telemetry and run delta moves its agent's
    `updatedAt` (`fleetevents.go:216`). So with agents running, the order
    changes, and inbox deltas follow, often.
  - It goes through the fleet's `emit`, as every other delta does. It takes a
    slot in the ring, and a client that falls behind gets a `replay_overflow`.
- **The clients.**
  - A client takes membership and order from the inbox. It takes the rows
    themselves from `GET /api/agents` and the deltas it applies today.
  - The client does not show an ID it has no row for. Instead it refetches
    the agents, at most once a second. That is how an agent spawned elsewhere,
    such as an approved intake or an MCP spawn, appears without a reload.
  - On connect, and after a `replay_overflow`, it fetches `GET /api/inbox`
    with the agents.
- **The web UI on it.**
  - These now come from the inbox:
    - the sidebar's groups;
    - its needs-you count;
    - Home's lists;
    - Home's intake items, from the queue.
  - `groupAgents` and `buildInbox` are deleted. Their tests are ported to Go as
    W2's cases.
  - `sortAttentionFirst` orders the Dashboard, which is not grouping, so it
    stays.
  - Intake submissions also stop waiting for a `pending` delta, or an approve
    or a deny, to refetch them. That is when `App.svelte:189` refetches them
    today.

### F3. The fleet client

- **The package.** `internal/fleetclient` is the Go client. It speaks REST and
  the fleet stream over a transport it is given:
  - locally, the daemon's socket, dialled as `internal/daemon.Client` dials it,
    with no token;
  - remotely (plan 7), HTTPS with the profile's bearer token on every request.
- **The interface.** `acpclient.Fleet` sits beside `Host` and `Session`.
  - Reads are answered from the client's own state, without blocking:
    - the rows;
    - the inbox;
    - the projects;
    - the budgets;
    - locally, the daemon's sessions;
    - the connection state;
    - the last action's error.
  - `Changed()` fires after anything those reads return may have changed, as
    `Session.Changed` does.
  - The actions are calls that block until the bridge answers:
    - spawn;
    - answer a permission or a question;
    - read a diff and a commit draft;
    - verify, exit, merge and discard;
    - cancel an agent's turn;
    - approve or deny an intake.
  - A table maps each method to its route, as `acpclient.Methods` maps the
    session's methods to ACP. A test keeps the table complete.
  - The TUI sees only the interface and the contract's types.
- **The stream.**
  - On connect, the client reads the agents, the inbox, the projects and the
    budgets, plus the daemon's info when local. Then it opens
    `GET /api/events?stream=fleet`.
  - It applies deltas to its rows by the rules `applyDeltaTo` uses today
    (`fleet.ts:86`).
  - It refetches the agents on a `pending` delta, because the request's payload
    is only in the snapshot, as the web UI does. It refetches the budgets on a
    `budget` delta.
  - It ignores delta kinds outside the loop: watches, reroutes, automations and
    network blocks.
  - A `replay_overflow` makes it refetch everything it read on connect.
- **Reconnecting.**
  - A dropped stream reconnects with backoff: 1s, doubling to 30s, reset after a
    minute connected.
  - Each reconnect resumes with `Last-Event-ID`, and refetches what it read on
    connect, since the ring may have moved past it.
  - The connection state is one of three:
    - connected;
    - reconnecting, while the retries run;
    - down, once retries have failed for a minute. It keeps retrying every 30s.
- **A 401.**
  - A 401 on any request stops the stream and every retry.
  - The state says "token rejected for <profile>".
  - The client never asks for a token. The user fixes the profile and starts
    the screen again (fleet-cli §5).
- **Action errors.** An action returns the bridge's refusal: its status and
  its `error` text. A budget refusal keeps the budget's own sentence.

### F4. The fleet screen

- **Starting it.**
  - `marshal fleet` uses the local daemon, or the server `--server NAME` names
    (plan 7).
  - With no daemon answering and no server named, it says to start or install
    the daemon, and exits non-zero. It never falls back to embedded mode.
  - `/fleet` exists only on a daemon session screen, as `/detach` does. It
    detaches the session and runs the fleet program in the same loop.
- **The model.**
  - `internal/app/tui/fleet` is a Bubble Tea model.
  - It renders from `acpclient.Fleet`, under the TUI's boundary rule.
  - It reuses the TUI's theme, glyph and layout packages.
- **The layout** is fleet-cli §3's.
  - It is one screen, with a left column of Agents, Needs you and Events, and a
    tabbed panel on the right.
  - `tab` and `shift-tab` move focus, and `z` zooms the focused panel.
  - Widths, columns and glyphs follow fleet-cli §3.1:
    - below 80×24 it shows "terminal too small (80×24)";
    - every glyph is paired with a colour;
    - every state is still legible under `NO_COLOR`.
- **The Agents panel**
  - It lists `groups` in order, with a header line before each group that has
    agents in it, as the web UI's sidebar does.
  - Below them is a Sessions group (decision 9), local only. It lists the
    daemon's TUI sessions from `GET /api/daemon`, with each one's project,
    session ID, viewers and last activity. Its rows have no Diff or Info tab.
  - `/` filters by name or project.
- **The Needs you panel**
  - It is the queue, oldest first (fleet-cli §3.2).
  - An agent item shows its pending kind. An intake item shows its origin and
    title.
- **The Events panel**
  - It is a ring of the last 200 lines, newest at the bottom (fleet-cli §3.3):
    - agents becoming ready, failing, or needing the user;
    - action failures;
    - connection changes.
- **The right panel** (decision 4) has three tabs, picked with `1` to `3`:
  - Activity: the agent's activity line, its status, and what it is waiting on;
  - Diff (fleet-cli §3.5);
  - Info (fleet-cli §3.6).

  The full transcript is the session screen.
- **Keys.** Fleet-cli §3.9's keys, with these changes:
  - `⏎` opens the selected agent's or session's session screen (F5). On an
    intake item, `⏎` opens approve and deny.
  - The composer keys (`i`, `ctrl-x`, `ctrl-o`) belong to the session screen.
    `x` still cancels the selected agent's turn.
  - `a` answers the selected agent's request:
    - an approval inline in the Activity tab, with `y` or `n`;
    - a question in a modal.
  - `n` opens the spawn modal (fleet-cli §3.8).
  - `f` opens the finish modal (fleet-cli §3.7). A discard needs the agent's
    name typed.
  - `s` does not switch servers in P5. A server is chosen when the screen
    starts.
  - `q` and `ctrl-c` leave the fleet program for the shell.
- **Connection and failures** follow fleet-cli §5.
  - The footer shows the state as a dot: `●` connected, `◌` reconnecting, `✗`
    down.
  - While down, the panels keep the last state, dimmed. Actions are refused
    with a status-line message.
  - A failed action shows on the status line for 4s, and as a line in Events.

### F5. Opening an agent

- **The route** is `POST /api/agents/{id}/attach`.
  - It takes the project attach's upgrade headers and no body.
  - The bridge resolves the agent's runtime and session, and dials its child's
    viewer socket. It answers `101`, with the session's ID in `Marshal-Session`
    and the agent's in `Marshal-Agent`.
  - Before plan 6, an agent with no viewer socket (one with its own child) is
    refused with 409 and a sentence saying so.
  - A paused agent is refused with 409. An unknown agent gets 404.
  - Plan 4 serves the route on the socket. Plan 7 adds it on the TLS listener,
    with the bearer token.
- **The relay** is P4's. For an agent's session it refuses two calls:
  `session/close` for that session, and `session/new` with `replaces`.
  - It answers each itself, with an error whose message says to finish the
    agent from the fleet screen. It never forwards them to the agent.
  - Every other frame passes unchanged.
  - When the viewer goes, the relay never closes the agent's session:
    detaching is closing the stream.
- **The TUI** learns from `Marshal-Agent` that the session is an agent's.
  `internal/daemon`'s attachment gains the agent's ID.
  - `/exit` and `/new` then say "This is agent <name>'s session: finish it from
    the fleet screen", and do nothing else.
  - The status line names the agent.
- **The loop.** Launch's daemon loop gains a fleet pass.
  - The fleet program ends with what to open: an agent, a daemon session, or
    nothing, which means quit.
  - The loop dials what it names and runs the session screen.
  - When the session screen ends, the loop runs the fleet program again, with
    the agent just left still selected. It ends by quitting, by `/detach` or by
    `/fleet`.
  - `/exit` on a daemon session that is not an agent's ends that session, and
    returns to the fleet too.
  - A lost connection on the session screen returns to the fleet with a line in
    Events, not to the shell.
- **Answers.**
  - The fleet screen answers through `POST /api/permissions/{id}` and
    `POST /api/questions/{id}`. The session screen answers through its mirror
    (P3 R5).
  - Plan 4 first tests that the first answer settles the request on both paths,
    and that the other path is then told it was already answered.
  - If that does not hold, plan 4 makes it hold before relying on it.
- **The project attach widens** (closing P4 H15). It refuses with 409 any
  session ID an agent record names, live or paused. Today it refuses only the
  live fleet sessions (`attach.go:101`).

### F6. Viewer sockets

- **A host-process agent with its own child** (plan 6) starts that child with
  `--viewers` on `<state>/v/a-<16 hex>.sock`.
  - The hex is from the SHA-256 of the agent's ID, as the project children's
    sockets use the root's. That keeps the path inside the unix-socket length
    limit.
  - The directory is `0700` and the socket `0600`, as P4's are.
  - The bridge removes a stale socket before starting, and the socket after the
    child stops.
- **A containerized agent** (plan 8) runs `marshal acp --listen` on
  `agent.sock`, as today, and `--viewers` on `viewers.sock`. Both are in the
  mounted socket directory.
  - The bridge dials `viewers.sock` from the host side of the mount
    (`SocketDir`).
  - `cmd/marshal` accepts `--viewers` with `--listen`. Each serves its own
    connections, as each does alone today.
- **Which agents a viewer reaches.** After plan 6, every host-process agent's
  session can be opened; after plan 8, every agent's.
- **The container allowlist** (decision 6).
  - The list holds the methods the session screen needs:
    - its turns, controls and answers;
    - its reads;
    - its notes;
    - its browse calls.
  - It leaves out every `Host` method except reading the agent's info, and
    every method that writes user config or credentials.
  - It is data in the contract package, so the relay and the agent read one
    list.
  - Plan 8 pins it against `acpclient.Methods`. A test in `internal/acp` checks
    that each method on it is one the session screen calls, and none is a
    config or credential write.
  - The allowlist is enforced twice:
    - the relay answers a method that is not on the list with an error and
      never forwards it;
    - `marshal acp` refuses it on the viewer connection too. The bridge starts
      a container's agent with a flag that turns the viewers' allowlist on.
  - The error says the method "is not available for a containerized agent".
    The session screen shows it as it shows any error.

### F7. Server profiles

- **The file** is `fleet.toml` in the user's config directory: `MARSHAL_CONFIG_DIR`, else
  `$XDG_CONFIG_HOME/marshal`, else `~/.config/marshal`.
  - It holds a `servers` table of named profiles. Each profile has:
    - `url`: `https`, or `http` for a loopback address only;
    - optionally `token_cmd`;
    - optionally `ca_file`;
    - `notify`, true by default.
  - A `local` table holds `notify` for the local daemon.
  - A `token` field is an error, saying to use `marshal fleet login` or
    `token_cmd`. Any other unknown field is also an error.
  - The file must belong to the user, with mode `0600` or tighter. Otherwise
    the fleet refuses to read it and says what the mode is.
- **The token.**
  - `token_cmd` runs once per connect through `/bin/sh -c`, with a 10s limit.
    Its trimmed output is the token. A failure, or empty output, is reported
    with the first line of its stderr.
  - Without `token_cmd`, the token is the keychain entry
    `marshal fleet login NAME` stored through `internal/credentials`. Login
    reads the token without echoing it, and refuses a profile the file does not
    name.
  - With neither, the screen says "no token for <name>: run `marshal fleet
    login <name>`".
- **TLS** uses the system roots plus the certificates in `ca_file`. There is no
  option to skip verification. The bridge already serves TLS with `--tls-cert`
  and `--tls-key` (`cmd/webbridge/main.go:134`).
- **The attach over TLS.**
  - The network listener serves `POST /api/agents/{id}/attach`, with the bearer
    token, over TLS.
  - It serves it over plain HTTP only on a loopback address. Anywhere else the
    answer is 403, "the agent attach needs TLS".
  - The project attach and pin stay socket-only, answering 404 on TCP as today.
- **A remote session** is dialled with `SharedFilesystem: false`.

### F8. Needs you and notifications

- **The count.**
  - A daemon session screen gets an `acpclient.Fleet` from launch. It is the
    fleet program's client when the screen was opened from the fleet.
    Otherwise it is a client of the local daemon.
  - When the count is more than zero, the status line shows `needs you N`:
    the queue's items, not counting the shown agent's own.
  - It shows nothing at zero, and nothing in embedded mode.
- **Transitions.** The notifier watches the client's state for two
  transitions:
  - **Needs you:** an agent enters `groups.needsYou`, or a submission enters
    the queue.
  - **Ready:** an agent goes from running to idle with changed files.

  The first state after a connect or a refetch is the baseline, and notifies
  nothing.
- **The burst.** Notifications for one agent within 2s collapse into one.
- **The side effect.**
  - Each notification rings the terminal bell and shows a desktop
    notification:
    - `osascript`'s `display notification` on macOS;
    - `notify-send` on Linux;
    - nothing on other systems.
  - The desktop half is a notifier that launch passes to the TUI, as it passes
    the clipboard. The TUI only decides when to notify.
  - The text is "<agent> needs you: approval", or "… question", or "<agent> is
    ready".
- **Off.** `notify = false` turns notifications off for a profile, or for the
  daemon in the file's `local` table.
- **Not suppressed for the shown agent**, for fleet-cli §4's reason: the client
  cannot tell whether its terminal is in front. The count leaves the shown
  agent out, because the user is already looking at it.

## Design

| Area | Change |
|---|---|
| `web/bridge/contract` (new) | The fleet loop's wire types, their enumerations and the container viewers' method list (plans 1, 2, 5, 8); `tsgen`, the TypeScript generator, and its staleness test (plan 1). |
| `web/bridge` | Type aliases onto the contract (plan 1); the inbox, its loop, route and delta (plan 2); the agent attach and the relay's refusals, and the widened project attach (plan 4); viewer sockets on agents' own children (plan 6); the attach on the TLS listener (plan 7); container viewers and the allowlist in the relay (plan 8). `TestWebIsStdlibOnly` allows `marshal/web/` imports (plan 1). |
| `web/ui` | `contract.ts`, generated; `api.ts` and `fleet.ts` re-export it; `npm run check` (plan 1). The sidebar and Home on the inbox; `groupAgents` and `buildInbox` deleted (plan 2). |
| `internal/daemon` | Its mirrored types become contract aliases (plan 1); the attachment's agent ID (plan 4). |
| `internal/fleetclient` (new) | The fleet client over the socket (plan 3) and HTTPS (plan 7). |
| `internal/acpclient` | The `Fleet` interface and its route table (plan 3). |
| `internal/app/tui` | The fleet model in `fleet/` (plans 3, 5); `/fleet`, and `/exit` and `/new` on an agent's session (plan 4); the needs-you count and notifications (plan 6). The boundary test allows the contract package (plan 3). |
| `internal/launch` | The fleet pass in daemon mode's loop (plans 3, 4); the notifier (plan 6); remote profiles (plan 7). |
| `cmd/marshal` | `marshal fleet` (plan 3), `--server` and `marshal fleet login` (plan 7); `acp --listen` with `--viewers` (plan 8). |
| `internal/acp` | The test that the agent's forwarded replies decode into the contract (plan 1); the viewers' allowlist when started for a container (plan 8). |
| `test/daemon` | The fleet loop against a real bridge (plans 4, 5, 6); a server profile over TLS (plan 7). |

## Migration order

1. **The contract.** The package, the generator, the bridge and
   `internal/daemon` moved onto it, with the bytes on the wire pinned by
   recorded responses. The web boundary rule and `npm run check`.
2. **The inbox.** `GET /api/inbox`, the inbox loop and the `inbox` delta; the
   web UI's sidebar and Home on it.
   - Its bridge side runs beside plan 1, with the inbox's types in the bridge.
   - Its last tasks wait for plan 1 on `main`: they move those types into the
     contract, carry each intake item's whole submission, and move the web UI
     onto the inbox.
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
- **The contract package would fail the web boundary test** as it stands,
  since the test refuses `marshal/web/` imports too. Plan 1 narrows the rule to
  what it is for (F1).
- **The browser's views go stale between refetches.** Nothing announces a turn
  ending or a request answered, so the sidebar can show an agent in the wrong
  group until something refetches. The inbox loop's one-second recompute closes
  that.
- **The browser orders times as strings.** Two times in the same second sort
  wrongly when only one has a fraction. The bridge compares them as times.
- **Not yet checked:** whether a viewer's answer settles the bridge's brokered
  request for the same approval, so that the fleet screen stops listing it.
  Decision 5 assumes the first answer wins on both paths. Plan 4 must test
  it before relying on it.

## Implementation record

### Plan 1 — the contract

**What was built.** `web/bridge/contractwire_test.go` plus 55 exact wire
recordings under `web/bridge/testdata/contract/` pin the encoding of every type
the plan moves: filled and zero encodings, `fleetDelta` with its off-wire
fields, every map answer, and all fleet deltas. `web/bridge/contract/` then
holds those wire types and their enumerations; the web boundary now permits
`marshal/web/...` imports while still refusing every other `marshal/...`
import. The bridge reads the contract through type aliases, its unexported
`fleetDelta` embeds `contract.FleetDelta` with the two fields kept off the wire,
and the map answers and request bodies are contract types — every recording
stays byte-identical. `internal/daemon` uses the same aliases, and
`internal/acp/contract_test.go` proves that diff, merge-refusal and
commit-draft replies decode strictly into the contract and re-encode
identically. `web/bridge/contract/tsgen/` generates the TypeScript contract,
covering every mapping and unsupported construct in its fixture suite, and
`TestTypeScriptContractIsCurrent` holds `web/ui/src/lib/contract.ts` current.
The web UI re-exports the generated types under its existing names, derives its
fleet deltas from the contract, narrows unknown run and telemetry payloads
safely, gains `npm run check`, and `web/bridge/static/` is rebuilt.

**Where the plan was short, and how that was filled.** The plan required both a
`BudgetDaily` struct and a `BudgetDaily` scope constant in the one package,
which cannot compile. It was resolved conservatively: `contract.BudgetDaily`
stays the daily report struct (Tasks 3, 5 and 6 depend on that name), and the
scope constants are `contract.BudgetScopeDaily` and `contract.BudgetScopeAgent`.
W1's map-answer recordings could not record the `POST /api/agents` answer's
non-deterministic `warning` key, because no fixture produces a stable warning;
the clean spawn answer is recorded and the test comment documents the omission.
The generated budget and request types are consumed directly instead of
keeping a second UI model. A request body's optional fields are `omitempty` in
Go, while the TypeScript request's `routing` is `unknown`, matching the wire
contract.

**The type check.** `npm run check` reports exactly 6 errors, all in the six
baseline files: `web/ui/src/lib/fleet.test.ts`,
`web/ui/src/lib/Sidebar.test.ts`, `web/ui/src/views/Review.test.ts`,
`web/ui/src/views/Runs.test.ts`, `web/ui/src/views/Chat.svelte` and
`web/ui/src/views/Run.svelte`. The count did not rise and no error appears in
another file. `npm test` passes with 88 files and 933 tests.

**The branch review.** Three `glm-5.3-flash` reviewers read the branch in
disjoint slices — the wire and the contract, the generator, and the web UI
with the docs. The wire and the UI slices found nothing: the recordings stay
byte-identical, the contract's aliases decode as before, and a fresh UI build
matches the committed bundle. The generator slice found four latent holes,
none of which the current contract triggers, and all four are fixed here.
- A typed constant was dropped from its union when the constant was declared
  before its type, when it repeated the previous constant's expression list,
  or when it took its value from another constant. The generator now collects
  the types first and resolves every constant's value, by literal or by
  reference.
- A slice of pointers became `Inner | null[]`, which TypeScript reads as
  `Inner | (null[])`. It is now `(Inner | null)[]`.
- `any` and a named type the module cannot declare were accepted silently,
  which produced a field whose type the module never declares. Both are
  errors now, naming the file, the line and the construct.
- A doc comment holding `*/` ended the generated block comment early, and a
  raw-string constant could produce an invalid TypeScript literal. Both are
  escaped now.

**Verification.**

| Command | Result |
| --- | --- |
| `gofmt -l internal cmd test web` | No output. |
| `go vet ./...` | Clean. |
| `GOOS=linux go vet ./web/...` | Clean. |
| `GOOS=windows go vet ./web/bridge/...` | Clean. |
| `TMPDIR=/private/tmp/p5-1-suite go test ./... -timeout 20m` | One full-suite run was clean. A controller rerun hit the base's load-sensitive `internal/acp` failure `TestStackRoundTripBudget` (first read 53.464ms against the 50ms budget); `go test ./internal/acp/ -count=1` then passed. No other package failed. The baseline also records `TestStackPatchApplyBudget` as load-sensitive; it did not fail in either final full-suite run. |
| `web/ui: npm run check` | 6 errors in the six baseline files above, as expected; the gate, not a regression. |
| `web/ui: npm test` | 88 files / 933 tests passed. |
| `web/ui: npm run build` | Built in 3.49s; `web/bridge/static/` is unchanged against the committed build. |
| `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` | Built (git-ignored via `.gitignore`'s `/marshal`). |
| `TMPDIR=/private/tmp/p5-1-suite go test ./test/usability/... -run TestScripted -timeout 5m` | Passed (`marshal/test/usability` ok, 45.7s). |

### Plan 2 — the inbox

**What was built.** The bridge computes the fleet's inbox, so no client has to.
`web/bridge/inbox.go` holds the rules: `computeInbox` puts each agent in the
first group it qualifies for — needs you when it is parked on a request,
running, ready to ship when it is idle with changes and no pull request, else
earlier — and sorts each group by pending kind, then status rank, then most
recently updated, stably. The queue merges the needs-you agents at their
`updatedAt` with the intake submissions at their `createdAt`, oldest first,
agents first on a tie. Every list is non-nil, so an empty inbox encodes as four
empty arrays and an empty queue, never `null`. `GET /api/inbox` answers a fresh
computation behind the same bearer check as the rest of `/api/`, and answers
what the mux answers for an unknown path on a bridge built over a registry
rather than a fleet. A loop beside the fleet's deltas wakes on any delta but
its own, settles 250ms so a burst costs one recompute, and recomputes every
second when nothing wakes it; it sends an `inbox` delta through the fleet's
`emit` only when the encoding differs from the last one it sent, and always on
its first computation. The route never sees that cache: only the loop keeps it,
and only to decide whether to send. The inbox's types live in
`web/bridge/contract/` with the rest of the wire types, the bridge keeping its
names as aliases, and four new wire recordings pin the inbox and its delta
without changing one of plan 1's. An intake item carries the whole submission,
built by the same function `GET /api/pending` uses, so both answers carry the
same bytes for one submission. The web UI's sidebar and Home render the
bridge's grouping and queue; `groupAgents`, `pendingRank`, `byUrgency`,
`AgentGroupKey` and `AgentGroups` are gone, and `buildInbox` is replaced by
`mineOnly`, which keeps Home's "Mine" scope as a filter over the lists the
bridge sent. The sidebar looks rows up by the IDs the inbox named and refetches
the agents, at most once a second, when it sees an ID it has no row for. The
docs say where this lives: `cmd/webbridge/README.md` gains "The fleet inbox",
between "The local profile" and "Secrets", and `docs/ARCHITECTURE.md`'s web
bridge row gains one clause. `AGENTS.md` does not change: it never described
the web UI's grouping.

**Where the plan was short, and how that was filled.** The plan says a fleet
event wakes the inbox loop. The loop's own delta is a fleet event, so without
an exclusion each send would wake the loop and it would recompute itself every
250ms for nothing; the exclusion is explicit in `emit`, where an `inboxDelta`
does not signal `inboxWake`. The plan requires the delta to name no session,
as the budget, reroute and watch deltas do, but `sse.ts` dropped every delta
without a session ID, so the inbox delta never reached the store; the client
now passes it through, with a test. The plan's migration order deferred the
inbox's last three tasks until plan 1 landed on `main` — the types into the
contract, each intake item's whole submission, and the web UI onto the inbox —
and that is the order the branch took, after the merge.

**The manual run.** `webbridge` and `marshal` were built and the bridge run
with `--profile local`, `--marshal-bin $PWD/marshal`, its TCP listener on
`127.0.0.1:7799`, a fixed bearer token, `--egress off`, and a scratch
`MARSHAL_DATA_DIR`, `--state-dir` and `--socket` under `/private/tmp`. A scratch
git project was added with `POST /api/projects` and three local agents spawned
with `POST /api/agents`; the provider was the local Ollama with
`llama3.1:8b-instruct-q4_K_M`.

Driven, and seen on `curl -N` against `GET /api/events?stream=fleet`:

- **The first computed value reaches a client that is already connected.** A
  `curl` started before the bridge, retrying until the listener answered, saw
  `id: 1` as an `inbox` delta when the loop first computed. A client that
  connects later sees nothing until the inbox changes, which is what the loop
  sending only on a change means.
- **A spawn moves the agent into `running`, and the turn's end moves it to
  `ready`.** Each completed turn cost two inbox deltas: one entering `running`
  and one leaving into `ready`.
- **How many, for a running agent.** Each turn produced 2 inbox deltas — into
  `running`, then out to `ready`. Across the run the stream carried 7: 2 for
  the first agent's one turn, 4 for the second agent's two turns, and 1 for the
  third, which never finished.
- **`GET /api/inbox` answers the last delta's value.** Fetched at the end of the
  run and compared as sorted JSON with the last `inbox` delta on the stream:
  identical.

Not driven, and why:

- **An inbox delta moving an agent into `needsYou`, and answering the approval
  moving it out within a second.** These need a turn that parks on a request.
  `echo hello` is a safe command, so the bridge executed it without asking even
  with the session in `ask` mode and auto off. The two later turns that asked
  for a risky command never raised a request: both hung — one at `symbols` for
  about seven minutes, one at `mode.request` — until the sessions were
  cancelled. The only provider available was the local 8B model, so the
  approval path is the one part of this plan the manual run did not exercise.
- **The `needsYou → running` half of "the turn ending moves it to running, then
  ready or earlier".** The `running → ready` half was seen; the half that
  starts from a parked agent was not, for the same reason.
- **What needed a browser.** The sidebar's and Home's rendering of the inbox —
  the groups, the needs-you count, and Home's intake items from the queue —
  was not checked. A browser is not available here, so only the values the
  bridge served and streamed were checked.

**Verification.**

| Command | Result |
| --- | --- |
| `gofmt -l internal cmd test web` | No output. |
| `go vet ./...` | Clean. |
| `GOOS=linux go vet ./web/...` | Clean. |
| `GOOS=windows go vet ./web/bridge/...` | Clean. |
| `TMPDIR=/private/tmp/p5-2-suite go test ./... -timeout 20m` | `marshal/internal/acp` failed four timing-budget tests: `TestNoteCallBudget`, `TestStackBusyFlipBudget`, `TestStackPatchApplyBudget` and `TestStackRoundTripBudget`. All four are load-sensitive budget tests the repo already records: plan 1's header names `TestNoteCallBudget` and `TestStackRoundTripBudget` as the base's two, and `docs/bootstrap-factory/acceptance.md` records `TestStackBusyFlipBudget` and `TestStackPatchApplyBudget` as the same family. Each skips under `-short` and the race detector and holds a wall-clock budget, and this branch touches none of them: `git diff main..HEAD -- internal/acp` is empty. Rerun alone: `TestNoteCallBudget`, `TestStackBusyFlipBudget` and `TestStackPatchApplyBudget` passed; `TestStackRoundTripBudget`, a median of samples against 50ms, failed 2 of 3 runs alone on this loaded machine and passed once. No other package failed; `web/bridge` passed in 191.9s. |
| `TMPDIR=/private/tmp/p5-2-suite go test -race ./web/bridge/ -run 'Inbox\|Fleet' -count=1` | `ok marshal/web/bridge 6.630s`. |
| `web/ui: npm run check` | 6 errors in the six baseline files — `src/lib/fleet.test.ts`, `src/lib/Sidebar.test.ts`, `src/views/Review.test.ts`, `src/views/Runs.test.ts`, `src/views/Chat.svelte` and `src/views/Run.svelte` — plus 1 warning. The count is the base's and no error appears in another file; exit 1 is the gate, not a regression. |
| `web/ui: npm test` | 88 files / 940 tests passed. |
| `web/ui: npm run build` | Built in 3.80s; `git status` shows no change under `web/bridge/static/`. |
| `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` | Built (git-ignored via `.gitignore`'s `/marshal`). |
| `TMPDIR=/private/tmp/p5-2-suite go test ./test/usability/... -run TestScripted -timeout 5m` | Passed (`marshal/test/usability` ok, 45.3s). |
