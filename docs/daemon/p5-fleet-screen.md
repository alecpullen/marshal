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
Behaviour written 2026-11-13. Plans 1 and 2 are implemented (merged at
`fcebaa8b`). Plans 3, 4a and 4b are written (2026-11-14), with their anchors
checked at `cef1e353`. Plan 4a is new: authoring plan 4 found that no real
agent's approval reaches the bridge (Findings), so plan 4 became 4a, the
requests, and 4b, opening an agent.

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
  - The connection state is one of five:
    - connecting, before the first read;
    - connected;
    - reconnecting, while the retries run;
    - down, once retries have failed for a minute. It keeps retrying every 30s;
    - rejected, when a 401 stops everything.
- **A 401.**
  - A 401 on any request stops the stream and every retry.
  - The state says "token rejected for <profile>".
  - The client never asks for a token. The user fixes the profile and starts
    the screen again (fleet-cli §5).
- **Action errors.** An action returns the bridge's refusal: its status and
  its `error` text. A budget refusal keeps the budget's own sentence.
- **Decided by plan 3:**
  - **One read.** `Fleet.State()` returns one `FleetState`: the rows, the
    inbox, the projects, the budgets, the latest gate per agent, the daemon's
    info and the connection. It is one consistent value, as `Session.Status`
    is.
  - **Plan 3's interface is the reads and `Changed`.** Plan 5 adds the
    actions, their routes and the last action's error.
  - **The route table** is `acpclient.FleetRoutes`, with `Routes:` doc lines.
    A test in `test/daemon` checks each route against a real bridge.
    `acpclient` may import the contract, as the TUI may.
  - **The stream opens first.** The client reads the snapshot once the stream
    has answered, and applies the deltas that arrived meanwhile after the
    reads. Reading first would lose what happens between the two.
  - **Freshness:**
    - An inbox that names an unknown agent, or moves any agent to another
      group, refetches the agents, at most once a second. A turn ending has no
      delta of its own.
    - Activity, mode, telemetry and run deltas move the row's `updatedAt`, as
      the bridge does.
    - A mode delta replaces the row's access unless its revision is older.
    - Locally, the daemon's info is reread every 5s, because its sessions are
      not on the stream.
  - **Epochs.** Every full read (a connect, a reconnect, an overflow) bumps
    `FleetState.Epoch`. The first state of an epoch is the baseline of F8.
  - **Before the first read,** the state is `connecting`, which the footer
    shows as reconnecting.
- **Decided by plan 5a:**
  - **The answer bodies join the contract,** in a new `answers.go`:
    `Decision`, `Answer`, `Answers` and `ModeAccessChoice`, with their JSON
    tags unchanged. The bridge keeps its names as aliases, as plan 1 did for
    the others; the generated `contract.ts` gains them, and `api.ts`
    re-exports the three answer types from it.
  - **Each action is one method that blocks until the bridge answers,**
    bounded by the caller's context. The fleet model gives each call 30s. A
    refusal is an `acpclient.FleetError`, which carries the status and the
    bridge's own sentence.
    - A 429 is a budget refusal, and its sentence is the budget's, as the web
      UI's is: "Budget reached (<scope>)".
    - A 410 on an answer is not an error. It is `acpclient.ErrSettled`,
      "already answered".
    - A transport failure is a `FleetError` with status 0 and the dial's text.
  - **The last action's error is not a read.** F3's read list loses it: the
    actions return their error to the caller, and the model keeps the last
    failure for its status line and Events. A second copy in `FleetState`
    would be one more thing to keep current.

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
  - The footer shows the state as a dot: `●` connected, `◌` connecting or
    reconnecting, `✗` down or rejected.
  - While down, the panels keep the last state, dimmed. Actions are refused
    with a status-line message.
  - A failed action shows on the status line for 4s, and as a line in Events.
- **Decided by plan 3:**
  - **The ST glyph comes from the inbox group,** so glyph and group agree:
    `⚠` needs you, `●` running, `✓` ready, and in earlier `✗` for an error and
    `·` otherwise. `glyph.Live` (`●`, sharing `Brand`'s shape, which the fleet
    screen never shows) and `glyph.Waiting` (`◌`) join the vocabulary.
  - **The layout's numbers:**
- The left column is 28 columns below 100. From 100 up the Agents table
      takes the larger pane and the right panel keeps 40%: the table's fixed
      columns alone fill a 40% pane, so the rule is the other way round.
    - Needs you shows 1 to 5 rows, as many as it has items.
    - Events shows 3 rows below 30 rows of height, and 6 from 30 up.
    - Agents takes the rest.
  - **The tabs are Activity (`1`) and Info (`3`)** until plan 5 adds Diff as
    `2`, so the numbers never move. A Sessions row has no tabs: the panel
    shows its project, session ID, viewers and last activity.
  - **The theme** is the user's `[tui]` config, without the project layer,
    read by launch.
  - **No mouse capture,** so the terminal's own selection works.
- **Decided by plan 5a:**
  - **The approval is answered inline.** `a` on an agent with a pending
    approval switches the right panel to Activity and shows the approval's
    tool, command, risk and reason, with `[y]es [n]o`.
    - While that prompt is open, `y` and `n` answer and `esc` closes it.
      Outside it, `n` is spawn, as fleet-cli's global keys say.
    - A skill gate is answered the same way: yes allows once, no is the
      sticky deny.
    - An approval carrying `modeAccess` says to open the agent to answer the
      access request, and offers nothing else.
  - **The question is a modal.** Its rows are the questions in order, each
    with its options.
    - `j` and `k` move between options, and `space` toggles one under a multi
      question.
    - `⏎` picks a single option and moves to the next question, or submits
      after the last.
    - A question with no options takes a typed answer in a text field.
    - `d` declines the whole request, and `esc` closes the modal without
      answering.
  - **The spawn modal has four fields,** moved through with `tab`: the
    project, the name, the mode and the first prompt.
    - The project is chosen with `j` and `k` from `State().Projects`,
      available ones only, starting on the selected agent's project when it
      has one.
    - The mode cycles with `space` through the project's default, `ask`,
      `act` and `act` with auto, which are the request's `mode` and `auto`;
      auto exists only with act. On the default the request leaves both out,
      and the bridge applies the default.
    - `⏎` on the prompt field spawns, and `ctrl+r` there cycles the project's
      recent prompts, read once when the modal opens, at most 20.
    - Success selects the new agent and a warning goes to Events. With no
      available project the modal says "add a project from the web UI or
      `marshal daemon pin`" and spawns nothing.
  - **Cancel is `x` on a running agent,** with no confirmation, as the web
    UI's stop is. Anywhere else it does nothing. It reaches a turn a viewer
    started once plan 4c is merged.
  - **Intake is a two-line prompt.** `⏎` on an intake item in Needs you asks
    "approve <title>?" over "[y]es [n]o, deny", the footer naming the keys
    ("y approve · n deny · esc close"), and `esc` closes it. Approving
    selects the spawned agent once it appears.
  - **Refused while down.** With the connection anything but connected, each
    action key sets the status line to "not connected to <server>" and calls
    nothing.
  - **The model holds the last failure and one open modal,** in a `modal` (nil
    when none is open), the `answer` field naming the agent whose approval is
    inline, and a `status` line that lives 4s by the model's clock.
- **Decided by plan 5b:**
  - **The Diff tab is `2`,** as plan 3 reserved it.
    - It reads the files when it is shown, when the selection moves to
      another agent while it is shown, and when the shown agent's
      changed-files count changes.
    - `j` and `k` move between files while the right panel has focus, and
      reading a file's diff follows the cursor. `J` and `K` scroll the diff.
    - The header reads "<N> files +<added> −<removed>". A Sessions row has no
      Diff tab.
    - A failed read shows its sentence in the panel, not on the status line:
      it is a read, not an action.
  - **`v` on the Info tab** runs the gate, the line reading "verifying…"
    until the call returns. The result shows through the gate delta, and a
    refused call is an action failure.
  - **`f` opens the finish modal on an idle agent.** On one that is running
    or waiting, the status line says "finish needs <name> idle: cancel its
    turn first (x)".
  - **The modal's steps:**
    - the message, a text field pre-filled from the commit draft read when
      the modal opens, empty when that read fails or is unsupported;
    - ship, on `⏎`, to the destination decision 5 picks;
    - discard, on `ctrl+d`, which asks for the agent's name and needs it
      typed exactly;
    - close, on `esc`, at any step, without acting.
  - **The destination** is the web UI's rule, read from the row: a local
    agent merges, a read-only git agent leaves a patch, any other git agent
    exits. An exit that answers another destination shows that destination's
    result rather than pretending it pushed.
  - **A merge's refusals are the web UI's sentences, in the TUI's words:**
    - `dirty` asks for a commit message and retries with it;
    - `project_dirty`, `target_moved` and `conflicts` say what to do and
      leave the modal open on the message step;
    - any other reason reads "merge refused (<reason>)".
  - **A blocked push** shows the failed command and the last 20 lines of its
    output, then a reason field. `⏎` on a non-empty reason exits again with
    the override, and an empty reason is refused in the field.
  - **The result** names where the work went: the merged branch and target,
    or the pushed branch and the pull request's URL when there is one. Any
    key closes it. Every result and every failure is also an Events line.

### F5. Opening an agent

- **The route** is `POST /api/agents/{id}/attach`.
  - It takes the project attach's upgrade headers and no body.
  - The bridge resolves the agent's runtime and session, and dials its child's
    viewer socket. It answers `101`, with the session's ID in `Marshal-Session`
    and the agent's in `Marshal-Agent`.
  - Before plan 6, an agent with no viewer socket (one with its own child) is
    refused with 409 and a sentence saying so.
  - A paused agent is refused with 409. An unknown agent gets 404.
  - Plan 4b serves the route on the socket. Plan 7 adds it on the TLS listener,
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
  - The first answer settles the request on both paths, and the other path is
    then told it was already answered: 410 through the bridge,
    `CodeRequestSettled` through a viewer.
  - Checking this while authoring plan 4 found that it held in neither
    direction, for two reasons (Findings). Plan 4a makes it hold:
    - The bridge keeps a child request's ID as raw JSON and answers with it, so
      the agent's string IDs (`"marshal-out-N"`) are brokered at all.
    - When an outbound request's context ends before its answer, the agent
      sends `$/cancel_request` for it. The bridge ends that request's wait,
      which drops it from the bridge's list.
    - A forwarded prompt's RPC also ends when the prompt settles by any path,
      and the forwarder then returns quietly: it does not fail the turn or
      answer again. This covers approvals, questions, skill gates, access
      requests and a child's approvals.
- **The project attach widens** (closing P4 H15). It refuses with 409 any
  session ID an agent record names, live or paused. Today it refuses only the
  live fleet sessions (`attach.go:101`).
- **Decided by plan 4b:**
  - **No viewer may close or replace any agent's session,** on either attach.
    A project viewer can do both today, to a fleet agent on the same child. The
    relay refuses both whenever the named session is an agent's.
  - **"An agent's session"** is one an agent record names, live or paused, or
    one the classifier maps to a live agent.
  - **One sentence** serves the relay and the session screen: "This is agent
    <name>'s session: finish it from the fleet screen."
  - **Every ending is refused** on an agent's session screen, in `beginEnd`:
    `/exit`, `/quit`, `/new`, `/clear` and a resume. `/detach` still detaches.
  - **The agent attach's refusals:**
    - an unknown agent: 404;
    - a paused agent: 409;
    - a runtime that failed to start: 502;
    - an agent with its own child: 409 until plan 6;
    - an agent with no session yet: 409.

    It never resumes a paused agent.
  - **The loop returns to the fleet once the fleet has run in it,** from every
    ending, including a failed or refused open, with the reason in Events.
    Before that, a plain `marshal` or `marshal attach` behaves as today, apart
    from `/fleet`.
  - **The fleet client lives as long as the loop.**
- **A viewer's own turns reach the fleet** (found while authoring plan 4b;
  decided with the user on 2026-11-15).
  - **The gap.** The fleet's `running` is the bridge's `Busy` flag, which only
    a turn the bridge's own connection prompts sets (`registry.go:887`). A
    turn started from an agent's session screen runs on the viewer's
    connection.
  - It reaches the bridge as no delta, and its approvals are asked of the
    viewer's mirror only (P3 R5). So the fleet showed the agent idle, and
    never listed its requests in needs you.
  - This is P4 decision 8's gap, met from the other side. H3 covers it for
    the daemon's own TUI sessions, but here it is a fleet agent. The fleet
    loop's "go back to the fleet, leaving the agent running" needs it closed:
    W4 as written checks the run through a viewer, not through the fleet.
  - **The decision: the agent reports, in full.** The bridge does not take
    the session over or become a second owner. The agent reports a turn the
    service runs on a session the bridge's connection asked to follow, with
    its updates and its prompts. Two alternatives were rejected:
    - the fleet's connection becoming a co-owner of the session, which gives
      one session two owners — the objection that keeps the project attach
      from taking a fleet session;
    - the bridge polling the session's status for the run, which would carry
      no events, no approvals and no telemetry.
  - **The daemon's own TUI sessions stay out** (H3): the bridge follows only
    the sessions it created or loaded for a fleet agent. Plan 4c is this.
- **Decided by plan 4c:**
  - **The opt-in is a method, `session/follow`,** sent by the bridge on its
    own connection for one session. Its params are the session's ID, and its
    result is an empty object.
    - Following is idempotent, and lasts until the connection closes or the
      session's runtime is replaced.
    - A viewer connection, or any marshal client, is refused with an invalid
      request error: "a marshal client reads its runs from the session's
      status". An unknown session is a server error naming it.
    - A field on `session/new` and `session/load` was the alternative: it
      would need repeating on every load and resume, and a method is testable
      alone.
  - **The agent reports only runs on the service.** The follower reports a run
    the service reports as in flight. The bridge's own turn runs on the
    bridge's own `runTurn` and is never in the service's status, so the
    follower can never report the bridge's own turn back to it.
  - **One update kind, `followed_run`,** with a state of `started` or
    `ended`:
    - `started` carries the run's ID and kind, and is sent before anything
      from the run is forwarded;
    - `ended` carries the run's ID and outcome, and a failed run's message,
      and is sent after the run's last event and its telemetry.
  - **A followed run's updates are a turn's updates.** The follower forwards
    the run's events exactly as `runTurn` does — the same projection, the
    same stack patches, the same closing telemetry — through one forwarder
    shared with `runTurn`, so the two can never drift.
  - **A followed run's prompts go to the bridge, and failing quietly is
    safe.** Approvals, typed access requests, questions and skill gates are
    forwarded through the connection's bridges, each with plan 4a's settled
    channel.
    - A bridge failure, or a missing bridge, never stops the run and never
      answers the prompt: the viewer that started the run still has it in its
      mirror. That differs from `runTurn`, which cancels its own turn, denies
      without a bridge, or answers `Unanswered`.
    - Every prompt is still asked at most once per identity, with the same
      guards `runTurn` uses.
  - **The bridge's cancel stops a followed run.** `session/cancel` on the
    bridge's connection, with no turn of its own in flight on that session,
    stops the service's run when the connection follows it. That is what
    makes the fleet's `x` (plan 5a) work on a turn a viewer started.
  - **The bridge keeps the two kinds of turn apart.** A session's record holds
    whether its own prompt is in flight and the ID of the followed run in
    flight. `Busy` is true while either is, and so is `accepting`.
    - A followed run's `ended` clears them only if the bridge's own prompt is
      not in flight, so a late `ended` cannot hide a bridge turn that started
      straight after it.
    - An `ended` for a run other than the one recorded is ignored.
  - **The bridge follows every fleet agent's session:** after a spawn's
    `session/new`, after `restoreSession`'s load on either kind of child, and
    again for each followed session after a child restart's resume.
    - A child that answers method-not-found (an older `marshal`) is logged
      once per session and left as it was.
    - Any other failure is logged and fails neither the spawn nor the
      restore: following improves what the fleet shows, and a spawned agent
      is still an agent without it.
  - **A followed run's end is a `turn_end` event,** as the bridge's own
    prompt's end is, so the web UI's transcript and any other `turn_end`
    reader see it. The outcome maps to the stop reason:
    - `done` and `gated` are `end_turn`;
    - `cancelled` is `cancelled`;
    - `failed` carries the run's message as the event's error.

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
- **Decided by plan 6:**
  - **An own child's socket** is `<state>/v/a-<16 hex of sha256(agent ID)>.sock`,
    as F6 says, with the `a-` keeping it apart from the project children's
    names.
    - The bridge removes a stale socket before the child starts, and the
      socket after it stops.
    - Only a host process gets one. A container's child is unchanged.
    - The arguments are added where `newRuntime`'s fallback builds the host
      process, so a test that replaces `newRuntime` keeps its own child as it
      was, and task 1's tests drive the fallback itself.
  - **The agent attach reaches an own child** through that socket, counting
    its viewer on the agent's own registry. A container's agent is refused
    with 409, "agent <name> runs in a container, which has no viewer socket
    yet".
    - The relay takes from the project child what it used it for: the
      registry that counts the viewer, and a hook that rearms a project
      child's linger, which is nil for an own child.

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
- **Decided by plan 6:**
  - **One watch outlives both programs.** The notifications' state — the last
    state seen and each key's last notification time — lives in a
    `fleet.Watch` that launch creates once per daemon loop. The fleet program
    and every session screen after it observe the client through it.
    - So a change that lands while the loop switches programs is still
      compared against the state before it, rather than becoming a new
      baseline.
    - The watch's first observed state, and the first of each epoch, notifies
      nothing.
  - **What notifies,** from plan 3's transitions:
    - an agent's needs-you: "<agent> needs you: approval", or "…: question";
    - a submission: "<title> needs you: intake";
    - ready: "<agent> is ready".

    A failed agent is an Events line, not a notification.
  - **The burst is per key:** an agent's ID, or a submission's. A
    notification within 2s of the last one sent for the same key is dropped.
  - **The bell is the TUI's, the desktop notification launch's.** For each
    notification the watch passes, the program returns `tea.Raw` with the bell
    character, and the watch calls the notifier launch gave it. The notifier
    must not block: the desktop one runs its command on its own goroutine,
    bounded by 5s, and logs a failure once.
  - **The desktop notifier is a new package, `internal/app/notify`,** which
    the TUI never imports. On macOS it runs `osascript -e` with `display
    notification`, its text quoted for AppleScript; on Linux it runs
    `notify-send`; elsewhere it does nothing. Its title is "Marshal".
  - **Where the count shows, in plan 4b's loop.**
    - The loop creates its fleet client when it starts, not when the fleet
      program first runs, so every daemon session screen gets it and a plain
      `marshal` shows the count from the start.
    - The watch, and so notifications, starts with the first fleet program and
      is passed to every program after it. A plain `marshal` that never opens
      the fleet does not notify, as decision 8's title says: notifications run
      wherever the fleet is open.
  - **The count's segment** is on the status line's right, before the
    session's own right-hand segment, and reads `needs you N` in the warning
    colour. Its number is the queue's items minus the shown agent's own.

## Design

| Area | Change |
|---|---|
| `web/bridge/contract` (new) | The fleet loop's wire types, their enumerations and the container viewers' method list (plans 1, 2, 5a, 8); the answer bodies (plan 5a); `tsgen`, the TypeScript generator, and its staleness test (plan 1). |
| `web/bridge` | Type aliases onto the contract (plan 1); the answer bodies' aliases in `registry.go` and `interaction.go`, and the recent prompts on `contract.RecentPrompts` (plan 5a); the inbox, its loop, route and delta (plan 2); the child's requests kept by their raw ID, and `$/cancel_request` honoured (plan 4a); followed runs, their updates and their `turn_end` (plan 4c); the agent attach and the relay's refusals, and the widened project attach (plan 4b); viewer sockets on agents' own children (plan 6); the attach on the TLS listener (plan 7); container viewers and the allowlist in the relay (plan 8). `TestWebIsStdlibOnly` allows `marshal/web/` imports (plan 1). |
| `web/ui` | `contract.ts`, generated; `api.ts` and `fleet.ts` re-export it; `npm run check` (plan 1); the answer types re-exported from it (plan 5a). The sidebar and Home on the inbox; `groupAgents` and `buildInbox` deleted (plan 2). |
| `internal/daemon` | Its mirrored types become contract aliases (plan 1); `AttachAgent` and the attachment's agent ID (plan 4b). |
| `internal/fleetclient` (new) | The fleet client over the socket (plan 3) and HTTPS (plan 7); its actions, `post` and `FleetError` (plan 5a); its finishing actions (plan 5b). |
| `internal/acpclient` | The `Fleet` interface and its route table (plan 3); the actions, `FleetError` and `ErrSettled` (plan 5a); the finishing actions (plan 5b). |
| `internal/app/tui` | The fleet model in `fleet/` and two glyphs (plans 3, 5a, 5b); the answers, the question and spawn modals, cancel and intake (plan 5a); the Diff tab and the finish modal (plan 5b); the fleet model opening a target (plan 4b); `/fleet`, and every ending refused on an agent's session (plan 4b); the needs-you count (plan 6); the notifications' watch (plan 6). The boundary test allows the contract package (plan 3). |
| `internal/launch` | `launch.Fleet` (plan 3); the loop between the fleet and the session screen (plan 4b); the notifier (plan 6); remote profiles (plan 7). |
| `internal/app/notify` (new) | The desktop notifier launch passes in (plan 6). |
| `cmd/marshal` | `marshal fleet` (plan 3), `--server` and `marshal fleet login` (plan 7); `acp --listen` with `--viewers` (plan 8). |
| `internal/acp` | The test that the agent's forwarded replies decode into the contract (plan 1); `$/cancel_request` sent when an outbound request stops waiting, and a forwarded prompt's RPC ended by its settlement (plan 4a); `session/follow` and the follower that reports a service run, through the turn's own forwarder (plan 4c); the viewers' allowlist when started for a container (plan 8). |
| `internal/app/session` | `PromptSettled` (plan 4a). |
| `test/daemon` | The fleet client and its routes against a real bridge (plan 3); requests on both paths (plan 4a); the agent attach and the fleet loop (plan 4b); a viewer's turn through the fleet (plan 4c); the actions (plan 5a), the diff, the gate and a finish (plan 5b), and viewers everywhere (plan 6); a server profile over TLS (plan 7). |

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
4. **Plan 4 became three** when its authoring found that no real agent's
   approval reaches the bridge, and plan 4b's found that a viewer's own turn
   reaches the fleet as nothing.
   - **4a. The requests.** A real agent's approvals and questions reach the
     bridge, and an answer on either path settles them on both. It needs
     nothing, and runs beside plans 3 and 4b.
   - **4b. Opening an agent.**
     - The agent attach and the relay's refusals.
     - The widened 409.
     - The session screen on an agent's session, and `/fleet`.
     - The fleet-to-session loop in launch, for agents on a project child.

     Its bridge, daemon-client and session-screen tasks run beside plan 3.
     Its fleet-model and launch tasks need plan 3.
   - **4c. A viewer's turn reaches the fleet.** `session/follow`, the
     follower that reports a service run through the turn's own forwarder,
     and the bridge's counting and cancelling of that run.
     - Its Tasks 1 to 4 need nothing from plan 4b, and run beside it.
     - Its Task 5 tightens plan 4b's W4 test, so it waits for plan 4b on
       `main`.
5. **The fleet's actions,** in two plans.
   - **5a. The actions.** The answer bodies in the contract, the actions on
     the seam and in the client, the inline approval, the question and spawn
     modals, cancel, and intake. Spawn, answering, cancel and intake need
     plans 3 and 4a and nothing else.
   - **5b. The diff tab and finishing.** The Diff tab, the gate's key, and
     the finish modal (exit, gate failure and override, merge, discard).
   - Both run beside plan 4b if their files stay apart: plan 4b is launch,
     the relay and the bridge's route, plan 5 the fleet screen's panels. Both
     add keys to the fleet model, and both plans' `⏎` is reconciled by
     whichever merges second: on an intake item it approves or denies (5a),
     anywhere else it opens (4b).
   - 5b needs 5a on `main` for every task: it extends 5a's seam, its client's
     `post`, its status line and its modal type.
6. **Viewers everywhere, and needs you.**
   - Viewer sockets on host-process agents' own children.
   - The session screen's needs-you count, and notifications.
   - Its Tasks 1, 4 and 5 need nothing from plan 4b and run beside it. The
     attach, the session-screen option and the daemon loop's wiring wait for
     plan 4b on `main`.
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
- **No real agent's approval or question reaches the bridge** (found while
  authoring plan 4, by reading and by a probe run, 2026-11-14).
  - The agent numbers its outbound requests as strings, `"marshal-out-N"`
    (`internal/acp/server.go:228`). The bridge's child reader decodes a
    child-initiated request's ID as an `int`, and drops the frame when that
    fails (`web/bridge/child.go:502`).
  - So every `session/request_permission` and `session/request_question` from
    a real `marshal acp` child is dropped, and the agent waits forever.
  - A probe ran a fleet agent on a project child with a confirmation rule. It
    made its tool call and then sat in `running` with nothing pending for
    40s. An instrumented reader logged the dropped
    `session/request_permission id="marshal-out-1"`.
  - The bridge's tests never saw it, because their fake child sends the
    integer ID 9000. It is also why plan 2's manual run never saw an approval.
  - It affects the web UI as much as the fleet screen. Plan 4a fixes it.
- **A viewer's answer did not settle the bridge's request** (the question this
  spec left for plan 4, answered by reading).
  - A viewer's `session/answer_approval` settles the session's prompt, but
    nothing ends the RPC the agent forwarded for it. When that RPC's context
    does end, `Server.Request` forgets its waiter without telling the peer
    (`server.go:277`).
  - So the bridge would keep listing the request until someone answered it
    there. The other direction already held.
  - Plan 4a fixes it with `$/cancel_request` (F5, "Answers").
- **A viewer's own turns are invisible to the fleet** (found while authoring
  plan 4b). Decided with the user on 2026-11-15 as "agent reports, full": the
  agent reports a service run to a bridge connection that follows the
  session, rather than the bridge taking the session over, and rather than
  the bridge polling the session's status for it. F5 holds the decision, and
  plan 4c is it.

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

**The branch review.** Three `glm-5.3-flash` reviewers read the branch in
disjoint slices — the bridge's rules, route and loop, the contract and the
wire, and the web UI with the docs. The contract and wire slice found
nothing: the inbox's types match the plan's declarations, the bridge's names
are true aliases of them, the four new recordings are deterministic, and not
one of plan 1's recordings changed. The other two slices each found one
thing, and both are fixed here.
- `NewFleet` started the inbox loop before the notifier was assigned, so a
  scheduler stall between the two statements would have had the loop's first
  delta reach a nil notifier. The loop starts once the fleet is built.
- The SSE parser passed the inbox delta through unchecked, where every other
  kind with an untyped payload is validated first. A drifted frame — one with
  no inbox, a group table missing a list, or no queue — would have been
  stored and blanked the sidebar or Home on the next render. The inbox is
  validated like its siblings now, and a malformed frame is dropped, so the
  browser keeps the last good inbox.

**Verification.**

| Command | Result |
| --- | --- |
| `gofmt -l internal cmd test web` | No output. |
| `go vet ./...` | Clean. |
| `GOOS=linux go vet ./web/...` | Clean. |
| `GOOS=windows go vet ./web/bridge/...` | Clean. |
| `TMPDIR=/private/tmp/p5-2-suite go test ./... -timeout 20m` | One run, before the branch review's fixups, had `marshal/internal/acp` fail four timing-budget tests: `TestNoteCallBudget`, `TestStackBusyFlipBudget`, `TestStackPatchApplyBudget` and `TestStackRoundTripBudget`. All four are load-sensitive budget tests the repo already records: plan 1's header names `TestNoteCallBudget` and `TestStackRoundTripBudget` as the base's two, and `docs/bootstrap-factory/acceptance.md` records `TestStackBusyFlipBudget` and `TestStackPatchApplyBudget` as the same family. Each skips under `-short` and the race detector and holds a wall-clock budget, and this branch touches none of them: `git diff main..HEAD -- internal/acp` is empty. Rerun alone, the first three passed; `TestStackRoundTripBudget`, a median of samples against 50ms, failed 2 of 3 runs alone on the loaded machine and passed once. A controller rerun of the full suite on the final tree was clean — no package failed — and `web/bridge` passed in 132.1s. `main` then moved on with unrelated work, which was merged into this branch and the suite rerun: still clean, with `web/bridge` passing in 140.1s. A last run on `main` itself hit `TestStackPatchApplyBudget` and `TestStackRoundTripBudget` again; both fail at the base commit `d80cdf68` on this machine too — the patch apply failed two of three runs alone there, the round trip one of three — so they are the runner's known budget breach and not this work's. |
| `TMPDIR=/private/tmp/p5-2-suite go test -race ./web/bridge/ -run 'Inbox\|Fleet' -count=1` | `ok marshal/web/bridge 6.591s`, and 6.603s on the merged tree. |
| `web/ui: npm run check` | 6 errors in the six baseline files — `src/lib/fleet.test.ts`, `src/lib/Sidebar.test.ts`, `src/views/Review.test.ts`, `src/views/Runs.test.ts`, `src/views/Chat.svelte` and `src/views/Run.svelte` — plus 1 warning. The count is the base's and no error appears in another file; exit 1 is the gate, not a regression. |
| `web/ui: npm test` | 88 files / 943 tests passed, and 951 on the merged tree, which `main`'s newer work also grew. |
| `web/ui: npm run build` | Built; `web/bridge/static/` is the committed build. |
| `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` | Built (git-ignored via `.gitignore`'s `/marshal`). |
| `TMPDIR=/private/tmp/p5-2-suite go test ./test/usability/... -run TestScripted -timeout 5m` | Passed (`marshal/test/usability` ok, 45.3s). |

### Plan 3 — the fleet client and a read-only fleet screen

**What was built.** The seam grew a fleet interface beside its `Host` and
`Session`: `acpclient.Fleet` with `State()` and `Changed()`, the `FleetState`
it returns, the `FleetConn` the connection is in, and `acpclient.FleetRoutes`,
a table of the bridge routes that serve each method. The TUI may now import
`web/bridge/contract`, and `acpclient` may too, so the fleet's values cross
the seam as the contract's own types rather than as copies.

`internal/fleetclient/` is the client over one bridge. It opens
`GET /api/events?stream=fleet`, and once the stream has answered it reads
`/api/agents`, `/api/inbox`, `/api/projects`, `/api/budgets` and — locally —
`/api/daemon`, each bounded by ten seconds. The deltas that arrive while
those reads are in flight are held and applied after them, in order, so
nothing that happened between the snapshot and the stream is lost. Every delta
kind the bridge sends is applied: activity, telemetry, mode, hold, pending,
gate, run, budget, project_removed and inbox, each by the rules the web UI
follows. A stream that ends is reconnected, with the wait doubling from a
second to thirty, the last event's id sent back so the bridge replays what
was missed; a failure that lasts a minute puts the connection down and it
keeps retrying at the maximum wait; a 401 stops everything, says the token was
rejected and is never retried. Locally the daemon's own listing is reread
every five seconds, because the daemon's sessions are not on the stream.

`internal/app/tui/fleet/` is the screen: the model holds the fleet as the
client last read it, the Events ring, the selection and the focus, and the
keys that move them; `view.go` draws the layout, with the Agents table on the
left above the needs-you queue and the Events ring, and the selected agent's
Activity or Info on the right. Six golden frames pin it at 80×24, 100×30 and
140×40, with the Info tab, with the connection down, and below the minimum
size.

`marshal fleet` runs the screen on the local daemon. `launch.Fleet` probes the
daemon, refuses one that is not the local profile, reads the user's `[tui]`
config without the project layer, builds the client, and runs the model
through the program runner.

**Where the plan was short, and how that was filled.**

- **The `Routes:` doc line.** The plan's contract block wraps `State`'s routes
  over two comment lines, and its own rule is that the doc line lists the
  routes in order. A wrapped line cannot be that, so the test reads the whole
  statement and joins it, and the declaration keeps the plan's own wording.
- **The layout's width.** Decision 11 gives the left column 40% of the width
  from 100 columns up. The plan's own column set does not fit in it: at 100
  columns the table's fixed columns and their gaps need 37 cells of the 36 a
  40% pane leaves, and at 140 they need 50 of 52 — so NAME and ACTIVITY would
  each be one cell, and every name would read `…`. The Agents table therefore
  takes the larger pane and the right panel keeps the 40%, which keeps every
  column and the flex rule usable. The spec's F4 bullet is changed to say so.
- **`tea.KeyShiftTab` and `tea.KeyCtrlC` do not exist** in the Bubble Tea
  version this repository builds against. Shift-tab is `tea.KeyTab` with
  `tea.ModShift`, the idiom the session screen already uses, and ctrl+c is
  matched on the key's text.
- **The goldens hold no escape sequences.** Under `NO_COLOR` the renderer
  still emits bold and faint attributes, so the goldens are written and
  compared with the ANSI stripped, and `TestColourAddsNoText` strips the
  coloured render the same way.
- **The model's theme is read through an accessor.** `launch.Fleet`'s test has
  to see which theme the model resolved, and the model's fields are
  unexported, so `Model.Theme()` was added for it.
- **A 401 has to be recognisable.** The client now carries the status on its
  own error type, so a rejected token is told from a transient failure
  wherever it appears — the stream, a snapshot read or the daemon reread.
- **The overflow's refetch keeps what it can.** A failed budgets read leaves
  the budgets nil; the other reads keep their previous value, and the epoch
  still moves.
- **The allowlists grew by one path each.** `internal/app/tui/` and
  `acpclient` may now import `marshal/web/bridge/contract`, and nothing else
  was added to either.

**The manual run.** The screen was run against a real local daemon: a
`webbridge --profile local` on a scratch unix socket under `/private/tmp`, its
own scratch state and config directories, the local Ollama's
`llama3.1:8b-instruct-q4_K_M` as the provider, and a scratch git project
added over the socket's `POST /api/projects`. An agent was spawned over the
socket's `POST /api/agents` and ran there.

- **The screen draws at every size.** `marshal fleet` in an 80×24 terminal
  showed the Agents table with ST, NAME, CTX, Δ and AGE, the needs-you panel
  below it and the Events panel under that, the Activity panel on the right,
  and the footer reading `local ●  j/k move` with `?help q quit` at the far
  right. At 100 columns the table gained PROJECT and MODE; at 140 it gained
  ACTIVITY, COST and CHECK, with the gate and the spend shown for the
  selected agent.
- **The daemon's own sessions appear under Sessions.** A plain `marshal`
  session running in another terminal put one child with one session in the
  daemon's listing, and the fleet screen showed it a few seconds later — the
  daemon's listing is reread every five seconds, because those sessions are
  not on the stream — as `· project sess_179 1 viewer`, with its age.
- **The connection's state is shown, and it recovers.** Stopping the daemon
  turned the footer's dot from `●` to `◌` and put the dial error beside it,
  while the panels kept the last state they had. The Events ring recorded
  `reconnecting to local` and then `local is down: …`. Starting the daemon
  again brought the client back on its own: the dot returned to `●` and the
  ring recorded `connected to local`, with no key pressed.
- **What was not driven.** The needs-you queue was never filled by a real
  approval, for the reason plan 2's run found: the only local model is an 8B
  one, and it did not raise a request. The Diff tab, the actions and `⏎` are
  plans 4b and 5, and were not built.

**The branch review.** A reviewer read the whole branch against the base and
found two defects and three gaps. All five are fixed, and a second reviewer
verified each fix by reverting the four source files and watching the new
tests fail against them.

- **The model's quit never released its wait.** The model held a `done`
  channel that nothing closed: the quit keys returned `tea.Quit`, and Bubble
  Tea v2 handles `tea.QuitMsg` inside its own event loop without ever
  delivering it to `Update`, so the model never learned the program was
  ending. The armed wait goroutine stayed blocked on the client's signal
  forever — one leaked goroutine per program run, which matters for plan 4b's
  fleet program run repeatedly in one process. The model now holds a gate
  whose close is guarded, and its quit command releases the gate before it
  quits.
- **The model never seeded itself from the client,** so before the first
  read the panels said "Connecting to …" with no name and the footer's server
  cell was blank. It now takes the client's state when it is built, which
  carries the server's name from the start.
- **A 401 on a mid-stream refetch was swallowed.** The plan says a 401 on any
  read stops everything, but the refetches a pending, budget, inbox or
  overflow delta triggers ignored their errors, so the client stayed
  "connected" with stale rows. They now reject, and the overflow's reread
  stops at the first rejection rather than finishing the batch.
- **An inbox delta that changed only a submission's title fired no change,** so
  the screen kept the old title. The queue's submissions are now compared by
  their identity, title and origin.
- **`State` shared pointed-to values with the client,** though the seam says
  every slice and map is the caller's own copy. The agent's pending request,
  workspace, gate override, push time and each queue item's submission are
  now copied too, the pending request's payload into a fresh byte slice.

**The second branch review.** Four more reviewers read the branch, one over
the client, one over the screen, one over the seam and the launch path, and
one over this record and the plan. They found two defects in the client, two
in the screen, and two stale sentences in the documents. All are fixed, and
each fix has a test that fails without it.

- **A 401 in the connect's own window was overwritten.** The deltas that
  arrive while the connect reads the fleet are applied after the read, and
  one of them can ask for a refetch that meets the 401. The rejection stopped
  the client, but the connected state that followed presented the connection
  as live: the screen showed a stream that would never update again, and
  never said the token had been rejected. The connected state now leaves a
  rejection standing, with its message.
- **`State` still shared two values with the client:** a project's orphan
  worktrees, and the daemon's refresh record. They are copied now, and the
  test that says every slice and map is the caller's own copy mutates both.
- **The client never said it was closed.** A reader waiting on its change
  signal waited forever — one goroutine per program run, which matters for
  plan 4b's fleet program run repeatedly in one process. The client closes its
  signal when it closes, and the screen's wait ends on a closed signal rather
  than reading it as a change.
- **The Needs you panel could hide the row under the cursor.** It showed the
  queue's oldest rows, so a queue longer than the panel kept the cursor's row
  out of sight, and the right panel followed a selection nobody could see. The
  panel now scrolls around the cursor.
- **Two documents had gone stale.** This spec's connection-state list still
  said three states, and `AGENTS.md`'s `internal/acpclient` row did not
  mention the fleet seam. Both are corrected, and the plan's layout decision
  now carries a pointer to the corrected rule.
- **Smaller things, fixed with tests:** the help screen claimed `esc` clears
  the filter (it does not, unless the filter is open) and offered `j`/`k`
  without saying where they move a cursor; `tab` did nothing while the filter
  was open, so the focus could not move until it was closed; a row with no
  time read as a count of days since 1970; an identifier cut to eight bytes
  could split a character; the model kept a `prev` field it never read; and
  the stream parser dropped a field line with no colon, which the event
  stream format reads as a field with an empty value.
- **Two gaps in the launch path's own tests are now closed:** the production
  path — the client built over the socket, run and closed — had no test, and
  neither did the branch where the user's config cannot be read.
- **The screen's own untested keys are now tested:** `1` and `3` pick the
  right panel's tab, `z` and `?` toggle their overlays, and the view asks for
  the alternate screen and no mouse.

One finding was not a defect: the review noted that the execution claims in
this record are not code-checkable from the tree. That is what a record is —
the run's own account of what it saw — and the record says so.

**Verification.**

| Command | Result |
| --- | --- |
| `gofmt -l internal cmd test web` | No output. |
| `go vet ./...` | Clean. |
| `TMPDIR=/private/tmp/p5-3-suite CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 30m -skip '^Test(NoticeCallBudget\|ControlCallBudget\|AnswerCallBudget\|ResponseBarrierBudget\|SettingsCallBudget\|NoteCallBudget\|BrowserCallBudget\|EndCallBudget\|StackBusyFlipBudget\|StackPatchApplyBudget\|StackRoundTripBudget\|AKeyPressOverThePipeBudget)$'` | Passed: 125 packages ok, no failure, exit 0. The twelve skipped tests are the wall-clock budget family CI skips; the base's two known failures are among them, so no package failed here. |
| `TMPDIR=/private/tmp/p5-3-suite go test -race ./internal/fleetclient/ ./internal/app/tui/fleet/ -count=1` | `ok marshal/internal/fleetclient 3.009s`, `ok marshal/internal/app/tui/fleet 1.532s`, no data race. |
| `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` | Built (git-ignored via `.gitignore`'s `/marshal`). |
| `./marshal fleet --embedded` | Refused, as it must be: `marshal fleet: --embedded cannot be combined with fleet: the fleet screen needs the daemon`, exit 1. |
| `TMPDIR=/private/tmp/p5-3-suite go test ./test/usability/... -run TestScripted -timeout 5m` | Passed (`marshal/test/usability` ok, 45.1s). |

**The second review's verification.** The same tree, after the fixes above.

| Command | Result |
| --- | --- |
| `gofmt -l internal cmd test web` | No output. |
| `go vet ./...` | Clean. |
| `TMPDIR=/private/tmp/p5-3-suite CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 30m -skip '^Test(NoticeCallBudget\|ControlCallBudget\|AnswerCallBudget\|ResponseBarrierBudget\|SettingsCallBudget\|NoteCallBudget\|BrowserCallBudget\|EndCallBudget\|StackBusyFlipBudget\|StackPatchApplyBudget\|StackRoundTripBudget\|AKeyPressOverThePipeBudget)$'` | Passed: every package ok, no failure, exit 0. The twelve skipped tests are the wall-clock budget family CI skips. |
| `TMPDIR=/private/tmp/p5-3-suite CGO_ENABLED=1 go test -race ./internal/fleetclient/ ./internal/app/tui/fleet/ ./internal/acpclient/ ./internal/launch/ -count=1` | `ok marshal/internal/fleetclient 3.407s`, `ok marshal/internal/app/tui/fleet 1.745s`, `ok marshal/internal/acpclient 1.522s`, `ok marshal/internal/launch 13.940s`, no data race. |
| `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` | Built (git-ignored via `.gitignore`'s `/marshal`). |
| `./marshal fleet --embedded` | Refused, as it must be: `marshal: marshal fleet: --embedded cannot be combined with fleet: the fleet screen needs the daemon`, exit 1. |

### Plan 4a — fleet requests reach the bridge, and leave it

**What was built.** The bridge brokers a real agent's requests, and stops
listing one the agent settled elsewhere. `web/bridge/child.go` keys a child
request by its raw ID — any JSON, not only an integer — and answers with those
bytes, so the agent's `"marshal-out-N"` IDs are brokered at all; `OnRequest`
now takes a context and no ID. Each in-flight request is registered under its
compact ID on the read goroutine *before* the handler starts, so a
`$/cancel_request` read next always finds it; the cancel ends that request's
context, the registry's wait returns `errRequestCancelled`, the pending entry
is dropped, and the child is answered `-32800` with the message "request
cancelled". A `$/cancel_request` never reaches `OnNotification`, and one
naming no request in flight is ignored. `Child` gained a per-generation parent
context, created in `spawn` and cancelled in `failPending`, so a request in
flight ends when its generation's pipe closes. `internal/app/session/prompts.go`
gained `State.PromptSettled(key)`: it answers the live record's `done`
channel — closed exactly once, on any settlement — a shared already-closed
channel for a key that names no live record, and nil for a key with no ID.
`internal/acp` then made the agent stop waiting out loud: `Server.Request`
sends `$/cancel_request` when its context ends before the answer (a closed
server sends nothing), and `promptRPCContext` takes the settled channel, so
the RPC ends when the prompt settles by any path. All five waiters — the
approval, the question, the skill gate, the access request and a child's
approvals — pass their prompt's settled channel and, when it has closed,
return quietly: the turn is not cancelled, no `Unanswered` is fabricated, and
no denial is delivered. `test/daemon/requests_test.go` drives the whole path
end to end over a real local-profile bridge: the approval reaching the
bridge, a viewer's answer settling the bridge's request, and the bridge's
answer settling the viewer's mirror. `cmd/webbridge/README.md` names no
permission or question endpoint at all — it documents `/api/inbox`, the
fleet stream and the factory items — so it describes neither path and was
left unchanged.

**Where the plan was short, and how that was filled.** The plan's contract
for `PromptSettled` said to look the key up "as `lookupPromptLocked` does",
but that helper matches a record's kind and operation as well as its ID.
`resolveAccessRequest` names its request by the request ID alone
(`session.PromptKey{ID: requestID}`) while the access mirror record is filed
under the approval kind, so a kind-matching lookup reported a *live* access
request as already settled — and because the settled channel would already be
closed, `promptRPCContext` would have ended the access RPC before the client
was ever asked. Reading the access path against the contract found this, and
a throwaway test printed the record's kind and the already-closed channel to
confirm it. `PromptSettled` therefore resolves a record by its ID alone, the
way `PromptContext` already does, and a test pins that. The plan also did not
say what the bridge's generation parent context is; `Child` held none, so one
is created per generation in `spawn` and cancelled in `failPending`, the one
place that runs for every dead generation. The plan's Task 1 tests assumed
the fake child could issue a question under a string ID: it cannot, because
the existing `test/ask_question` issues its question under the integer ID
9000 and the plan's new fake method was a permission. The cancel fake
therefore takes its target ID from its params, defaulting to
`"marshal-out-1"`, so the question test drives a real cancel through the real
pipe without changing the existing case. The plan did not say how the bridge
names `$/cancel_request`: `web/` is standard-library only, so the method name
is a literal there and `marshal/internal/acpwire` is not imported. The plan's
Task 3 test — "the fake `PermissionClient` blocks until its context ends" —
needed a stronger form: its first version passed even with the settled
channel dropped, because the turn's own end also ended the fake's context, so
the test now holds the turn open on a channel until after the context-end
assertion. That is what makes the mutation bite.

**The manual run.** `webbridge` and `marshal` were built from the branch and
the bridge run with `--profile local`, `--marshal-bin` pointing at the built
`marshal`, its TCP listener on `127.0.0.1:7799`, a fixed bearer token,
`--egress off`, and a scratch `MARSHAL_DATA_DIR`, `--state-dir` and
`--socket` under `/private/tmp`. A scratch git project carried a
`[tools.shell.confirm]` rule for `rm -f marker.txt` and a passthrough
sandbox, and was trusted with `marshal --trust` — which records the trust and
then falls through into the TUI, so the record was read back from the
scratch `trust.json` rather than from a finished process. A fleet agent was
spawned on it with `POST /api/agents`, every routing role pinned to
`ollama-cloud/deepseek-v4.1-flash:1017`.

Driven, and seen on `curl -N` against `GET /api/events?stream=fleet`, which
carried 10 `inbox` deltas:

- **A real agent's request reaches the bridge.** The fleet's snapshot showed
  the agent `awaiting-approval`, with a pending approval carrying its
  `toolCallId`.
- **An inbox delta moves the agent into `needsYou`.** The delta named the
  agent in `groups.needsYou` and put it in the queue.
- **`POST /api/permissions/{id}` answers it, and the turn finishes.** The
  answer was HTTP 200, `{"status":"resolved"}`. The session's audit records
  the mode change as "granted — mode act" and then `shell.run` as
  `command "rm -f marker.txt" exited with code 0`; the agent's final message
  says the command ran with exit code 0.
- **A late answer gets 410.** Answering the first, already-settled ID again
  returned HTTP 410, `bridge: pending request already resolved or expired`.
- **The turn ends.** The agent's status went back to `idle`, `needsYou`
  emptied, and the stream carried the deltas out of `needsYou` and into
  `ready`.

Not driven, and why:

- **A viewer answering first.** That needs a TUI on the agent's session,
  which only plan 4b's agent attach gives. This half was covered by task 4's
  end-to-end test, not by hand: `TestAViewersAnswerSettlesTheFleetsRequest`
  and `TestTheFleetsAnswerSettlesTheViewersRequest` drive it over the
  project's own viewer socket.
- **The first two spawns never raised a request at all**, for a reason
  outside this plan and worth recording. A turn is more than the
  implementer's call: the auxiliary roles (title, router) keep the config's
  `active_preset`, which was `openai-codex/gpt-6.1-sol`, and that provider's
  OAuth refresh token was stale (HTTP 401, "Your refresh token has already
  been used to generate a new access token"). The turn aborted on that call
  before the model made a tool call, so no approval was ever raised. Pinning
  every routing role to `ollama-cloud/deepseek-v4.1-flash:1017` fixed it.
  Nothing in this plan changed that behaviour; it is recorded because the
  same failure would look like "the bridge dropped the request again".

**Verification.**

| Command | Result |
| --- | --- |
| `gofmt -l internal cmd test web` | No output. |
| `go vet ./...` | Clean. |
| `GOOS=linux go vet ./web/...` | Clean. |
| `GOOS=windows go vet ./web/bridge/...` | Clean. |
| `TMPDIR=/private/tmp/p5-4a-suite CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 30m -skip '^Test(NoticeCallBudget\|ControlCallBudget\|AnswerCallBudget\|ResponseBarrierBudget\|SettingsCallBudget\|NoteCallBudget\|BrowserCallBudget\|EndCallBudget\|StackBusyFlipBudget\|StackPatchApplyBudget\|StackRoundTripBudget\|AKeyPressOverThePipeBudget)$'` | Passed; no package failed. The twelve skipped tests are the repo's wall-clock budget tests, which CI also skips (`docs/testing-ci-audit.md`). |
| `TMPDIR=/private/tmp/p5-4a-suite CGO_ENABLED=1 go test ./web/bridge/ -count=1 -timeout 20m` | Passed (`ok marshal/web/bridge 104.4s`). |
| `TMPDIR=/private/tmp/p5-4a-suite CGO_ENABLED=1 go test ./internal/app/session/ -count=1` | Passed (`ok marshal/internal/app/session 3.8s`). |
| `TMPDIR=/private/tmp/p5-4a-suite CGO_ENABLED=1 go test ./internal/acp/ -count=1 -timeout 20m -skip '^Test(NoticeCallBudget\|ControlCallBudget\|AnswerCallBudget\|ResponseBarrierBudget\|SettingsCallBudget\|NoteCallBudget\|BrowserCallBudget\|EndCallBudget\|StackBusyFlipBudget\|StackPatchApplyBudget\|StackRoundTripBudget)$'` | Passed (`ok marshal/internal/acp 20.0s`). |
| `TMPDIR=/private/tmp/p5-4a-suite CGO_ENABLED=1 go test ./test/daemon/ -count=1 -timeout 15m` | Passed (`ok marshal/test/daemon 4.9s`). |
| `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` | Built (git-ignored via `.gitignore`'s `/marshal`). |
| `TMPDIR=/private/tmp/p5-4a-suite go test ./test/usability/... -run TestScripted -timeout 5m` | Passed (`marshal/test/usability` ok, 45.4s). |

Each task's mutation check was run and restored: decoding the child's ID as an
`int` again failed `TestAChildRequestWithAStringIDIsHandled`; dropping the
settled channel from the approval goroutine's `promptRPCContext` call failed
`TestASettledApprovalEndsItsForwardedRequest`; skipping the
`$/cancel_request` send in `Server.Request` failed
`TestAViewersAnswerSettlesTheFleetsRequest`, and only that one.

### Plan 4b — opening an agent

Not yet run. Plan 4b's last task fills this in.

### Plan 4c — a viewer's turn reaches the fleet

Not yet run. Plan 4c's last task fills this in.

**The batch's baseline** (plans 4c, 5a, 5b and 6), run at `bcceadab` — the code
of `67efccb7`, which differs only in `docs/` — with
`TMPDIR=/tmp/p5-5a-suite` on Linux, since `/private/tmp` is macOS's:

| Command | Result |
| --- | --- |
| `CGO_ENABLED=1 go test ./web/... -count=1 -timeout 20m` | Passed (`marshal/web/bridge 139.4s`). |
| `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent -count=1` | Passed. |
| `CGO_ENABLED=1 go test ./internal/acpclient/ ./internal/app/tui/... ./internal/launch/ -count=1 -timeout 20m` | Passed. |
| `CGO_ENABLED=1 go test ./internal/fleetclient/ ./test/daemon/ -count=1 -timeout 15m` | Passed. |
| `cd web/ui && npm test` | Passed: 985 tests in 88 files. |
| `cd web/ui && npm run check` | **Fails at the baseline:** 6 errors and 1 warning in 7 files, all in fixtures and one prop type, none in a file plan 5a adds. |

No Go test failed, so no Go failure is to be re-proved by any of the four
plans. The `svelte-check` errors are the baseline's, not any plan's, and are
listed so a task does not investigate them or pass by repairing them:

- `src/views/Chat.svelte:697:92` — an `AgentStatus | AgentRow | null` prop where
  `AgentStatus & {held?}` is wanted.
- `src/views/Run.svelte:336:74` — an `AgentRow | undefined` where the same is
  wanted.
- `src/lib/Sidebar.test.ts:16:3`, `src/views/Review.test.ts:49:3` and
  `src/views/Runs.test.ts:15:52` — fixture literals missing `AgentRow` fields.
- `src/lib/fleet.test.ts:78:42` — `system` is not in `AccessState`.

### Plan 5a — the fleet's actions

**What was built.** The fleet screen answers, spawns and cancels without
opening an agent. Each action crosses `acpclient.Fleet`, whose `FleetRoutes`
gained seven rows.

- **The answer bodies join the contract.** `web/bridge/contract/answers.go`
  holds `Decision`, `ModeAccessChoice`, `Answer`, `Answers` and
  `RecentPrompts`, with their tags unchanged; `registry.go` and
  `interaction.go` keep their names as aliases, `recentPrompts` encodes
  `contract.RecentPrompts`, `contract.ts` is regenerated, and `api.ts`
  re-exports the three answer types in place of its hand-written interfaces.
- **The seam** gained `Spawn`, `AnswerPermission`, `AnswerQuestion`, `Cancel`,
  `ApproveIntake`, `DenyIntake` and `RecentPrompts`, each with its `Routes:`
  line and a table row; `FleetError` carries the status and the bridge's
  sentence, and `ErrSettled` is a 410. Both fakes embed the interface, so an
  action a test did not set up fails loudly; the fleet model's fake records
  every call for Tasks 4 to 6.
- **The client** sends each action through `post`, `get`'s twin: the token,
  the JSON body, a 2xx check and the decode, bounded by the caller's context.
  `actionError` reads the bridge's `error`, turns a 429's `scope` into "Budget
  reached (<scope>)", and a 410 into `ErrSettled`; a dial failure is status 0.
  The route test substitutes `missing` for a `{…}` segment and counts a 404 or
  405 as an unserved route only when its body is not the bridge's JSON error.
- **The model** holds one open `modal`, the `answer` agent whose approval is
  inline, and a `status` line. `a` opens the selected agent's request: an
  approval inline in Activity, with its tool, command, risk and reason and
  `[y]es [n]o`; a question in a modal that walks questions in order, toggles
  under a multi one, takes a typed answer when there are no options, declines
  on `d`. Every action is one command with a 30s context; a refusal shows its
  sentence on the status line for 4s and as an Events line, `ErrSettled` says
  "<name>: already answered" in Events alone, and every action is refused
  while the connection is not connected.
- **Spawn, cancel and intake.** The four-field form moves with `tab`, picks
  the project with `j`/`k` from the available ones, cycles the mode through
  the project's default, `ask`, `act` and `act` with auto (leaving `mode` and
  `auto` out on the default), and reads the project's recent prompts behind
  `ctrl+r`. `x` cancels only a running agent. `⏎` on a needs-you intake item
  asks "approve <title>?" over "[y]es [n]o, deny". The help screen and the
  footer hints name the new keys.
- **The goldens** gain the plan's four frames at 100×30 and their four
  `NO_COLOR` twins at 80×24. Existing frames were regenerated twice: Task 6
  changed five of them for the footer's new hints (the footer line alone), and
  the review pass below changed six for the status line, the approval's
  duplicate request line and the spawn fields' widths.

**Review follow-ups applied.** A three-way review of the branch (contract and
client, TUI surface, web UI and docs) found no blockers, four should-fix
defects and a set of nits; every defect and every nit that changed behaviour
was fixed in place.

- **`RecentPrompts` refused unlike an action.** It went through the plain read
  path, so its refusal was not a `FleetError` and a 429 there lost the
  "Budget reached (<scope>)" sentence. Every action now sends through one
  `action` path, whatever its method, and an action's request refuses a
  redirect instead of following it.
- **An inline approval outlived the selection.** `y` answered a prompt that
  the right panel had stopped drawing, because the key path did not care
  which agent was selected. The prompt now closes when the cursor leaves its
  agent, and the key path requires the answered agent to be the selected one.
- **A refused submit discarded the answer.** A connection that dropped while
  the question modal was open closed it on `⏎`, typed answer and all; a
  refused `y` closed the inline prompt the same way. A refusal now leaves
  both open, as the intake and spawn forms already did.
- **A late result could undo a newer one.** `actionDone` applied its message
  whatever else had happened since, so a slow success cleared a newer
  failure's status line and moved the selection. Actions are numbered when
  they are dispatched; a stale answer no longer applies, though its failure
  still reaches Events.
- **The footer clipped its own message.** A status line longer than about 43
  cells lost its tail at 80 columns to the panel keys. While a status shows it
  takes the keys' room, so the bridge's reason survives.
- **Smaller rendering fixes.** The inline approval no longer prints the
  request twice; the spawn and question fields are clamped to their line (an
  ellipsis now means the value is longer, not that the field is too wide); the
  question modal's hints name only the keys that act for that question; and
  the web question modal trims a typed answer, as the TUI does.
- **New tests** cover each fix above, and the fleetclient error paths that had
  none: a dial failure as a status-zero refusal, a refusal body with no
  sentence, an unreadable answer, a redirect, and a read's refusal shape.

**Where the plan was short, and how that was filled.**

- **The web UI's multi-select answer was a live bug, and Task 1's re-export
  surfaced it.** `api.ts` typed `Answer.answer` as `string | string[]`, but
  the bridge decodes `Answers` with `Answer string`, so a multi-select answer
  would have been answered 400 by `resolveQuestion`. Re-exporting the
  contract's `Answer` broke `svelte-check` on `QuestionModal.svelte`, which is
  outside the task's files, so the task stopped and the user chose to fix it
  here: the modal joins its picks with ", ", exactly as
  `internal/app/tui/question.go` records them. That is also the one place this
  plan changes the web UI's behaviour.
- **Tasks 2 and 3 build each other.** The seam grows the interface that
  `*fleetclient.Client` implements, and the client needs the seam's
  `FleetError` and `ErrSettled`, so either task alone leaves
  `./internal/launch` unbuildable (it assigns the client to the interface).
  The plan counted only the two fakes as implementers. The two tasks therefore
  land as one commit.
- **Task 5's footer hints rewrite the existing goldens.** Task 5 item 4 adds
  `a`, `n` and `x` to the footer, which every existing golden renders, while
  the same task forbids touching `testdata`. Task 6 regenerated the five
  changed frames after checking that each diff is the footer line only. Task
  4's `TestNOutsideTheApprovalIsSpawn` also moved to Task 5, because the spawn
  modal it asserts on is Task 5's file.
- **Tasks 4 to 6 land as one commit,** because their edits interleave in
  `keys.go` and `view.go`; the plan's per-task commits could not be staged
  apart.
- **Decision 6's no-project sentence is wrapped,** not cut, so it fits the
  right panel whole; its test drops the frame's colour and borders before
  asserting the sentence, since it spans two lines.
- **The recent-prompts read is synchronous,** bounded by 5s, at the moment the
  spawn form opens. `Model.Update` routes only key presses and
  `actionDoneMsg` into an open modal, and `model.go` was outside Task 5's
  files; a command would need a `promptsMsg` case there. A failed read leaves
  `ctrl+r` with nothing to cycle.
- **The real-bridge test's cancel step is skipped, and that is a finding.**
  `POST /api/sessions/{id}/cancel` ends the turn — the bridge's own snapshot
  goes `running` to `idle` within about a second — but the fleet stream
  carries no agent-status delta for a turn ending, so the client's row keeps
  saying `running` until something refetches (`pending`, a replay overflow, or
  an inbox delta that moves an agent between groups, itself throttled to once
  a second with no retry). The action is served; the read path does not report
  it. That belongs to the fleet client's freshness rules (F3), not to this
  plan's actions.
- **The harness cannot start a git-sourced agent's own child,** so approving a
  repo-backed submission asserts the bridge's `FleetError` — a refusal, not a
  transport failure — which is what proves the route was served.
- **`npm run check` fails at the baseline** with six pre-existing errors in
  fixtures (recorded under "Plan 4c" above). Every task left exactly those
  six, and none of them is in a file this plan adds.
- **The plan's `TMPDIR=/private/tmp/...` is macOS's;** every run here used
  `TMPDIR=/tmp/p5-5a-suite` on Linux.

**The manual run.** Not run. Task 7's third item needs an interactive fleet
screen against a real local daemon with a real model, and this environment has
neither a TTY driver for the fleet program nor provider credentials. What a
human should drive, in order:

1. `webbridge --profile local` with a built `marshal`, a scratch
   `MARSHAL_DATA_DIR` and state directory.
2. `marshal fleet`, then `n`: spawn an agent with a prompt.
3. `a` on its approval: `y`, and see the turn finish.
4. `a` on a question, if the model asks one: answer it in the modal.
5. `x` on a running agent: its turn stops.
6. `⏎` on an intake submission: approve it, then deny another.
7. Confirm each refusal's status line and Events line, and that every action
   is refused with the connection down.

**Verification.**

| Command | Result |
| --- | --- |
| `gofmt -l internal cmd test web` | No output. |
| `go vet ./...` | Clean. |
| `TMPDIR=/tmp/p5-5a-suite scripts/test.sh full` | Passed: every package `ok`, exit 0. |
| `TMPDIR=/tmp/p5-5a-suite CGO_ENABLED=1 go test ./web/... -count=1 -timeout 20m` | Passed (`marshal/web/bridge` 133.7s). |
| `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent -count=1` | Passed. |
| `cd web/ui && npm run check` | The baseline's six errors and one warning, no seventh. |
| `cd web/ui && npm test` | Passed: 985 tests in 88 files. |
| `cd web/ui && npm run build` | Built in 4.3s; `web/bridge/static/` recommitted, since CI's `git diff --exit-code -- web/bridge/static` requires the bundle to match its source. |
| `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` | Built. |

Each task's mutation check was run and restored: dropping the body's sentence
in `actionError` failed `TestARefusalCarriesTheBridgesSentence` with
`the refusal says "status 409", want "the agent is held by another operator"`,
and only that one.

### Plan 5b — the diff tab and finishing an agent

Not yet run. Plan 5b's last task fills this in.

### Plan 6 — viewers everywhere, needs you and notifications

Not yet run. Plan 6's last task fills this in.
