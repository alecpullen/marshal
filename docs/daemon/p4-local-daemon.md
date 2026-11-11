# P4 — the local daemon

Parent: [`design.md`](design.md) §4.1 to §4.4, §6 and §7 (P4). It builds on
[`p3-acp-transport.md`](p3-acp-transport.md), whose client and agent host it
puts the bridge between. Where this spec and the design differ, this spec
wins. Each difference is listed under "Decisions".
Depends on: P3, implemented (its plan 8 merged at `40dc0eff`).
Status: approved 2026-11-09, with every recommendation under "Decisions"
accepted. The line anchors were re-checked at `40dc0eff` for plans 1 and 2,
and at `7802701f` (plans 1 and 2 merged) for plans 3 and 4, and at
`29851a61` (plans 3 and 4 merged) for plans 5 and 6, and at `87c0ad10`
(plans 5 and 6 merged) for plans 7 and 8. Plans 1 and 2
shared no code and ran side by side. Plans 3 and 4 do the same, except plan
4's last task, which needs plan 3 on `main`. Plans 5 and 6 share no file but
this spec, each filling its own record, and so do plans 7 and 8. Each plan
records itself under its own heading in the Implementation record.

## Goal

The user runs one daemon per machine (the bridge, local profile, as a user
service). `marshal attach` reconnects the TUI to a session running there, a
turn in flight included, after the TUI quit or the terminal closed. Quitting
the TUI never stops the agent's work. Plain `marshal` with no daemon running
still works in embedded mode, exactly as after P3.

The design's done-when for P4: a TUI can quit and reattach to a running turn
on the local daemon.

## Where the code is today

- **The bridge listens on loopback TCP only.** `webbridge` serves
  `127.0.0.1:7700` behind a bearer token, with optional TLS
  (`cmd/webbridge/main.go:103`, `:415`). It has no unix listener and no notion
  of a local profile.
- **One `marshal acp` child per fleet agent.** Each agent has its own child,
  registry and event log, and two agents on one project share nothing
  (`web/bridge/fleet.go:62`). A control child serves the Studio's own
  sessions (`control.go`). With no container runtime, an agent runs as a host
  process that inherits the bridge's environment (`fleet.go:391`). In a user
  service that is the service's environment, not the user's shell (design
  §6.1).
- **The bridge is not a marshal client.** Its `initialize` declares no
  capability (`fleet.go:792`, `control.go:175`). So its children send it no
  marshal update, and its calls reach ACP's own handlers, not the service. A
  TUI behind today's bridge would lose read-your-writes, the mirror's update
  kinds and every `marshalOr` route.
- **The bridge's event stream is lossy by design.** A lagging SSE subscriber
  drops events and gets a `replay_overflow` nudge, and stack patches are
  broadcast but not kept in the replay ring (`events.go:60`, `:273`). A
  whole-value mirror (status, requests, config) that misses one update stays
  stale until the value changes again.
- **`marshal acp` serves one connection at a time.** `--listen` accepts the
  next dialer only after the first hangs up (`internal/acp/listen.go:72`).
  Every connection's managers notify through the host's single sink
  (`host.go:361`): it exists so a turn outliving its connection reports to
  the next one. Two connections at once would get each other's updates.
- **A TUI that leaves ends its turn.** The service runs a turn on a context
  derived from the prompt call's (`service/turns.go:190`), and the server
  cancels every handler's context when the connection ends (`shutdown`,
  `server.go:829`).
- **Only the caller learns how a run ended.** The status says which route a
  turn uses (`TurnRoute`, nil between turns) and what the session is doing,
  but not that a run is in flight or how the last one ended. The TUI clears
  `busy`, writes "✘ Turn failed: …" or "Agent turn cancelled.", sets the
  notice and flushes queued steering when its own `Prompt` call returns
  (`handleAgentFinished`, `tui/model.go:4935`).
- **Headless sessions cannot ask about trust.** `marshal acp` starts runtimes
  with the headless resolver, which grants trust only from a stored
  permanent record and records nothing (`internal/trust/headless.go:14`).
  Only embedded mode sets the trust prompt (`app/embedded.go:226`). A TUI
  attached to a daemon session on an untrusted project would run without the
  project's config and never be asked.
- **OAuth refresh runs once per embedded TUI.** Only `Embedded.Start` builds
  the refresh workers, one per `openai_codex` OAuth provider
  (`app/app.go:2528`). Two TUIs refresh the same grant and race the token's
  rotation; no `marshal acp` child refreshes at all.
- **No project can be pinned.** The workspace registry's `ProjectSettings`
  (`web/bridge/workspace.go:118`) has no pin, and a project host closes 30s
  after its last session (`app/hosts.go:31`).
- **The pieces that already work:**
  - ACP sessions belong to the host, not the connection, so they survive a
    hangup (`listen.go:19`);
  - the TUI is a pure ACP client (P3), and `rpc.NewStream` frames any byte
    stream (`rpc/transport.go:37`);
  - a sudo answer is a call (P3 R5);
  - read-your-writes holds on any single ordered stream (P3 R2).

## In scope

1. The local profile: a unix socket listener, a user service, and a bridge
   that starts no container, egress or secrets machinery when none is
   configured.
2. One `marshal acp` child per project for host-process sessions, serving
   the bridge's own connection and any number of marshal connections at once.
3. The attach relay: a TUI's ACP stream carried, frame for frame, to its own
   connection on the project's child.
4. Turns that outlive their caller, and a status that says what runs and how
   the last run ended.
5. The TUI's daemon mode: `marshal attach`, detaching on quit, and following
   a run it did not start.
6. Trust for daemon sessions, the project environment, pinning, and OAuth
   refresh run once.
7. Host-process fleet agents moved onto their project's child.

## Decisions

The user accepted every recommendation on 2026-11-09, and the Behaviour
section is written to them. Each item keeps the alternative it was weighed
against.

1. **The TUI reaches the agent through a frame relay, not the per-call
   passthrough** (design §4.4). The bridge upgrades one request
   on the unix socket to a raw stream, and relays the TUI's ACP frames, both
   ways and unchanged, to a connection of the TUI's own on the project's
   child (D3). One ordered stream keeps P3's read-your-writes, cancellation
   and progress with no new protocol, and a connection per viewer gives each
   TUI its own busy flag and subscriptions. The per-call
   `POST /api/sessions/{id}/rpc` would split responses (HTTP) from updates
   (SSE). Reordering the two needs stream positions on every event, stack
   patches included, and a lossless SSE path. The per-call passthrough stays
   the web UI's, for when it needs one (P5). The alternative is the design's
   passthrough plus a sequence number on every SSE event, and the client
   holding each response until its stream catches up.
2. **The daemon is `webbridge --profile local`, managed by
   `marshal daemon`.** `marshal daemon install` writes a launchd
   agent (macOS) or a systemd user unit (Linux) that runs the `webbridge`
   binary shipped beside `marshal`. `start`, `stop`, `status` and `uninstall`
   manage it. The bridge stays the only process that supervises agents, and
   keeps its standard-library-only rule (design §4.1). The alternative is a
   `marshal daemon run` that embeds the bridge, which the boundary rule
   forbids.
3. **`marshal` stays a new embedded session; `marshal attach` reconnects.**
   Plain `marshal` keeps its meaning whether or not a daemon
   runs. When the daemon hosts the project, the TUI starts its new session
   there instead of in process (D5), so it can be reattached later.
   `--embedded` forces today's in-process mode. `marshal attach` with no
   argument reattaches to the project's most recently active daemon session;
   with an ID, to that one. The alternatives:
   - daemon mode only when asked (`--daemon`);
   - asking at every start (design §6.5's "offers to attach").
4. **Quitting detaches; `/exit` ends.** In daemon mode, the
   TUI's quit keys (Ctrl+C at an empty prompt, Ctrl+D) close the connection
   and leave the session and its work running. `/exit` runs the end flow
   exactly as in embedded mode: the warning, the stop, the postmortem, the
   close. A session that no client is attached to and that runs nothing ends
   after `daemon.idle_session_timeout` (default 30 minutes), so detached idle
   sessions do not pile up; its transcript stays resumable. The alternative,
   quit ending an idle session at once, would make "quit" mean two things.
   (Plan 5 corrects the keys: the TUI has no Ctrl+D quit, and its quit key is
   Ctrl+C pressed twice while no run is in flight. Pressed twice during a run,
   Ctrl+C still stops the run, so `/detach` is added for leaving mid-run. See
   D5.)
5. **A run's outcome is written by the agent.** The notes the
   TUI writes when its own run returns ("✘ Turn failed: …", "Agent turn
   cancelled.", a refusal's sentence) move into the service, which writes
   them when the run ends, whoever started it. The TUI keeps only its local
   effects: `busy`, the success pulse, the notice and the steering flush.
   Then a run that ended while no TUI was attached still shows its outcome
   in the transcript, and two viewers never write it twice. The alternative
   is that a reattaching TUI reports the last run from the status, which a
   viewer that was never attached would miss.
6. **A pinned project always watches.** Pinning keeps the
   project's host open with its index watcher running, whatever
   `indexing.watch` says, since keeping embeddings current is what a pin is
   for (design §4.3). Unpinned projects keep `indexing.watch`.
7. **The socket's permissions are the local profile's auth.**
   The socket sits in a `0700` directory in the data dir, with mode `0600`.
   The bridge also checks the peer's user ID (`SO_PEERCRED` on Linux,
   `LOCAL_PEERCRED` on macOS) and refuses any other user, which is cheap defence
   in depth. No token is needed on the socket. The loopback TCP listener the
   browser uses keeps its bearer token, unchanged. This answers design §8's
   "local-profile auth".
8. **The web UI watches a TUI's session but does not answer its requests in
   P4.** A run a TUI started asks nothing of the bridge's own
   connection (P3 R5), so the web UI cannot answer its approvals until it
   reads the pending mirror too (P5). A TUI attached to a web-started session
   sees and answers that session's requests through its own mirror.

## Out of scope (follow-ups)

- **H1.** The server profile for the TUI: TLS from a remote machine, and
  relaying to containerized agents. P5 runs the fleet screen against both
  profiles.
- **H2.** The fleet screen, the needs-you count, shared REST types and
  server-computed views (design §4.4, §4.6). P5.
- **H3.** The web UI over the relay or the per-call passthrough, and the web
  UI answering a TUI-started run's requests (decision 8).
- **H4.** Drain on upgrade (design §6.4). P4 only warns on version skew
  (D11).
- **H5.** Watching config and trust files while a session runs (design
  §6.2). P4 asks about trust when a session starts (D8).
- **H6.** Design §8's open questions on MCP scope, headless credentials and
  remote OAuth. They are server-profile concerns.
- **H7.** Worktree indexes (design §4.2).
- **H8.** A watch wake while no viewer is attached. The wake is the TUI's
  (P3: `bindWatchResume` skips marshal connections, and the bridge's own
  connection binds none on a TUI's session), so a wake that fires while the
  session is detached is not resumed; its report stays in the transcript.
- **H9.** The project environment's warning in the TUI. Plan 6 reports it
  on `GET /api/daemon` and in `marshal daemon status`; the attach answer
  does not carry it yet.
- **H10.** A project's plugins after a trust applied in place. A daemon
  session's trust reloads its config in place (D8), but project plugins are
  loaded only when a runtime starts, so they arrive at the session's next
  start. An ordinary config reload already drops plugin-applied hooks and
  MCP entries the same way.

## Behaviour

### D1. The local profile

- **The flags.** `webbridge --profile local` selects the profile; `server`, the
  default, is today's bridge. `--socket <path>` names the unix listener and is
  valid only with `--profile local`. Its default is
  `<data>/daemon/marshal.sock`, where `<data>` follows `config.DataDir`'s rule
  (`app/config/locations.go:36`): `MARSHAL_DATA_DIR`, else
  `$XDG_DATA_HOME/marshal`, else `~/.local/share/marshal`. The bridge cannot
  import `config`, so it repeats the rule, and a test pins the two together.
- **The socket.** The bridge creates the directory `0700`, and refuses one that
  is looser or that another user owns. It dials the path first: when a daemon
  answers, it refuses to start and leaves the socket alone. Otherwise it removes
  a stale socket, as `marshal acp --listen` does (`listen.go:40`), listens,
  makes the socket `0600`, and removes it on shutdown. The directory's mode
  covers the moment between listening and the `chmod`.
- **The peer check.** Every connection on the socket is checked for the peer's
  user ID before any byte is read, and a peer of another user is closed and
  logged (decision 7). Linux reads it with `SO_PEERCRED`. macOS has no
  standard-library call for it, so the bridge reads `LOCAL_PEERCRED` with a raw
  `getsockopt`, which keeps `web/` standard-library only. On any other system
  the local profile refuses to start.
- **The socket needs no token.** It serves the same routes as the TCP
  listener, without the bearer check. The browser's loopback TCP listener is
  optional in the local profile: `--addr off` disables it, and the preview
  listener with it. When it is on, it keeps its bearer token.
- **`GET /api/daemon`** answers the bridge's version, its profile, its process
  ID and start time, its socket, and its projects. `marshal daemon status`
  reads it.
- **The local profile starts no machinery it does not use.**
  - Agents always run as host processes, even when docker is installed: the
    bridge never probes for a container runtime.
  - The egress proxy is off, and the secrets backend is `env`. An explicit
    `--egress on`, or a `--secrets` other than `env`, is an error.
  - Its state directory defaults to `<data>/daemon/state`, and its fleet
    registry to `<data>/daemon/fleet.json`. A daemon and a bridge the user runs
    by hand (the Studio today) never share a registry or state; P5 decides
    whether the daemon takes over the Studio's.
  - It registers no project at start: the working directory is not added, as
    `--cwd-root` adds it today, because a service starts in `/`.

  A test pins that a local-profile start probes for no container runtime, runs
  no container command and starts no proxy (design §8's "bridge startup
  without containers").
- **The service.** `marshal daemon install` writes the unit for the platform:
  `~/Library/LaunchAgents/dev.marshal.daemon.plist` or
  `~/.config/systemd/user/marshal-daemon.service`.
  - The unit runs the `webbridge` beside the `marshal` binary (else the one on
    `PATH`; `--webbridge` overrides), with `--profile local --addr off`, and
    restarts it on failure.
  - It names every path explicitly: the socket, the `marshal` binary to
    supervise, the state directory and the registry. It carries the installing
    shell's `PATH`, and its `MARSHAL_CONFIG_DIR`, `MARSHAL_DATA_DIR`,
    `XDG_CONFIG_HOME` and `XDG_DATA_HOME` when they are set: a user service
    does not inherit the shell's environment, and without them the daemon and
    its children would resolve other directories than the TUI does. D7
    resolves each project's own environment later.
  - `start`, `stop` and `status` drive `launchctl` or `systemctl --user`.
    `status` also reads `GET /api/daemon` over the socket. `uninstall` stops
    the service and removes the unit, and keeps the data directory.
  - The daemon logs to `<data>/daemon/webbridge.log`.
  - The browser on the daemon (a TCP listener, its token) is P5's.

### D2. Project children

- **One child per project root** (`repo.FindRoot`'s root, as P1 keys project
  hosts) hosts every host-process session on that project: the TUI's, and
  from plan 10 of this phase the fleet's host-process agents (D10). The bridge
  starts it on first use (an attach or new session, a spawn, a pin), in the
  project directory, with the project's environment (D7). It stops a child
  that has no session and no pin after a linger of 30s, the project host's
  own.
- **The child serves the bridge and the viewers at once.** The bridge's own
  connection stays on stdio. A new `marshal acp --viewers unix://<path>` also
  accepts any number of marshal connections, concurrently, on a socket in the
  bridge's state directory (`0600`). Each viewer connection has its own
  managers, forwarders and barrier, as each connection does today. They
  notify on that connection only, never through the host's sink. A turn on
  ACP's own path (the bridge's connection) keeps the sink, so its
  orphaned-turn rule is unchanged (`addTurnCanceller`, `host.go:143`).
- **The viewer socket** (plan 3).
  - `--viewers` is valid only in stdio mode; with `--listen` it is an error.
  - The child creates the socket's directory `0700`, removes a stale socket,
    listens, makes the socket `0600`, and removes it when it exits, as
    `--listen` does. It binds before it serves stdio, so by the time the
    bridge's `initialize` is answered, the socket accepts.
  - A viewer connection reads frames with P3's 64 MiB cap, as the embedding's
    pipe does, and has no idle deadline: a TUI can sit idle for hours, and its
    process closing the socket ends the connection.
  - When stdio ends, the child stops accepting, closes every viewer
    connection, and then closes its sessions, as today.
- **A viewer connection serves marshal clients only.**
  - An `initialize` that does not declare the marshal capability is refused,
    and so is every call before a marshal `initialize`.
  - A viewer cannot start, load, resume or delete a runtime. `session/new` is
    served only with `replaces` (the TUI's `/new`), and `session/load`,
    `session/resume` and `session/delete` are refused. The bridge owns
    sessions' lifecycles (D3), and `session/load`'s replay would go to the
    host's sink, the bridge's connection.
  - A marshal client's runs are the service's, so a viewer never owns a turn
    on ACP's path. Its canceller therefore never joins the host's chain, and
    an attach does not grow the chain. When the connection ends, its cleanup
    also stops its stack loops, which today outlive their connection.
- **One run slot per session.** The service's turn slot and ACP's
  `TurnManager` slot exclude each other (P3 plan 6b's open point): a session
  runs one turn at a time, whichever connection started it, and the second
  caller is refused with the code its path already uses (`CodeBusy` for a
  marshal client, ACP's "already has an active turn" otherwise).
  - The slot is a claim on the session state, the one object both paths
    hold. Each path takes it at admission, after its own slot and before
    `BeginWork`, and releases it when its run ends. ACP releases it before
    the post-turn watch resume looks for a free slot.
  - `run` and `lastRun` (D4) show the service's runs only. A turn on ACP's
    path holds the claim but does not show in `run`; showing it is P5's, with
    H3.
- **The project child** (plan 4).
  - The bridge runs it as `marshal acp --viewers unix://<state>/v/<id>.sock`,
    where `<id>` is the first 16 hex digits of the root's SHA-256. The short
    name keeps the path under the platform's socket-path limit.
  - It runs in the project root, with the bridge's environment until D7.
  - The bridge's own connection declares no capability. On a TUI's session it
    calls only `session/new`, `session/resume` and `session/close`, so it never
    binds that session's watch resume or child approvals. Its notifications
    are dropped in P4: nothing reads them until the web UI does (P5).
  - The fleet's close stops each project child; it does not detach it.
- A child that exits unexpectedly is restarted, as `Child.supervise` does
  today. Its viewers' connections close, and their TUIs reattach (D5). The
  bridge resumes the sessions it tracks on the new generation, as
  `Registry.resumeAll` does.

### D3. The attach relay

- **The handshake.** On the socket, the TUI sends
  `POST /api/projects/attach` with the project root and either a session ID
  (`attach`) or none (`new`), plus `Connection: Upgrade` and
  `Upgrade: marshal-acp`. The bridge resolves or starts the project's child.
  For `new` it creates the session through its own connection and tracks
  it, as `Registry.New` does. Then it opens a viewer connection to the child,
  answers `101 Switching Protocols` with the session's ID in a header, and
  from then on copies bytes both ways. A refusal (an unknown session, a
  project the bridge cannot start) is an ordinary HTTP error with a JSON
  body, before the upgrade.
- **End to end.** The TUI's `rpc.Dial` runs `initialize` over the relayed
  stream with the child itself, declares the marshal capability, and checks
  the protocol version (D11). Nothing in P3's protocol changes:
  read-your-writes, `$/cancel_request` and `$/progress` hold, because the
  stream is one ordered byte stream.
- **The relay reads frames, and changes none.** It splits the stream at
  newlines, in both directions, with P3's 64 MiB cap, and forwards each
  frame unchanged. It watches the responses to `session/new` with
  `replaces` and to `session/close`, so its registry follows a replacement's
  new ID and a close. It logs no params (P3 R8).
- **The handshake's details** (plan 4).
  - The route is served on the socket only, never on the TCP listener: an
    attached stream is a full marshal connection, and the remote TUI is H1's.
  - The body is JSON: `root` and an optional `sessionId`. The root must be
    absolute and a directory, and is cleaned; plan 5's client sends
    `repo.FindRoot`'s root. The project is not added to the workspace
    registry: the daemon's children and their sessions are reported by
    `GET /api/daemon` instead.
  - An ID the bridge tracks is attached at once. An ID it does not track is
    resumed through the bridge's own connection (`session/resume`, which
    replays nothing) and tracked. A resume that fails is refused with 404.
  - The answer carries the session's ID in `Marshal-Session`. The client
    writes nothing after its request until it has read the `101`, and the
    relay still forwards any bytes it had already buffered.
- **The relay's costs.** It decodes the ID and method of every frame the TUI
  sends, because they are small. It decodes the agent's frames only while
  one of the two watched calls is waiting for its answer, because the
  agent's stack patches can be megabytes. IDs are compared by their compact
  JSON.
- **The registry follows the viewers.**
  - A replacement renames the tracked session. A viewer's successful
    `session/close` has ended the conversation, not the runtime (P3 plan 6c),
    so the bridge then closes the runtime through its own connection and
    stops tracking the session.
  - Each tracked session counts its attached viewers and records when it was
    last active: an attach, a detach, or a frame relayed either way.
    `GET /api/daemon` lists each child's root, version and sessions with
    those two values. Plan 5's `marshal attach` with no ID reads it.
  - A child with no tracked session stops after the 30s linger. In P4 a
    session leaves the registry only by a close, until plan 6's idle timeout
    and plan 9's pins.
- **The client's half** is `internal/daemon`'s `Client.Attach`. It sends the
  handshake and returns the upgraded stream and the session ID, which plan 5
  wraps in `rpc.NewStream`.
- **When either side goes,** the relay closes the other. A TUI that loses
  the daemon reports it and exits (as P3 R11 does for the pipe), and can be
  reattached.

### D4. Turns outlive their caller

- For a marshal client, `session/prompt`, `review`, `agent_run`,
  `swarm_start`, `sdd_start` and `sdd_answer` run their work on the session's
  context, not the call's. The call still waits for the run and answers with
  its outcome. When the call's context ends, whether by `$/cancel_request` or
  because the connection closed, it answers the context's error at once and
  leaves the run going. `session/stop` (or the end flow, or the session's
  close) is how a run stops. `suggest`, `verify_proposal`, `sdd_preflight`
  and the sign-ins stay cancellable as they are: they are short, and their
  caller owns them. `sdd_author` stays cancellable too, because its result
  (the candidate plan) goes to its caller only, but it takes the run slot, so
  it shows in `run`.
- **Embedded mode sees no difference.** Its TUI stops a run with `Stop`, and
  its quit runs the end flow and then closes the runtime, which stops
  everything (P3 plan 8).
- **The status says what runs.** `SessionStatus` gains:
  - `run`, the run in flight: its kind (`turn`, `review`, `agent`, `swarm`,
    `sdd`, `author`), its ID and its start time. The ID is the run's stop
    group's turn ID, which its work items and commands already carry. `run`
    is set once the session admits the run, so a refused start never shows;
  - `lastRun`, the last finished run: its ID, kind, start and end times, its
    outcome (`done`, `cancelled`, `failed`, `gated`) and, for `failed`, its
    error as an `*acpwire.Error`. A refused start changes neither field:
    nothing ran.

  Both change through the status forwarder, as every status field does.

### D5. The TUI's daemon mode

- **Which mode.** `launch` checks the daemon socket once at start. If a
  daemon answers and hosts (or can host) the project, the TUI runs over the
  relay; otherwise it runs embedded, as after P3. `--embedded` skips the
  check (decision 3). In daemon mode the TUI process starts no runtime and
  opens no project database. It logs to its own client log in the data dir.
- **`marshal attach [session]`** reattaches: with no ID, to the project's
  most recently active session the bridge tracks; with one, to that session,
  after the bridge loads it on the project's child if it is not live. `/resume`
  in daemon mode does the same for the session the picker chose, where
  embedded mode restarts its runtime (P3 R7).
- **Quit detaches** (decision 4). The quit keys close the connection without
  the end flow. `/exit` runs the end flow over the relay, as in embedded
  mode. A session that no viewer is attached to and that runs nothing ends
  after `daemon.idle_session_timeout`.
- **Following a run the TUI did not start.** When the status shows a run that
  this TUI's own call did not start, `busy` follows the status. When the run
  ends, the TUI does its local finishing (D4's `lastRun`: busy cleared, the
  success pulse, the notice, the steering flush) as it does when its own call
  returns. The run's notes are already in the transcript (decision 5). On
  attach, a run already in flight shows exactly as it would have if the TUI
  had never left: the transcript is the mirror's, and the live box follows
  the status.
- **What stays local.** The clipboard, `$EDITOR` (the daemon is on the same
  machine, so `SharedFilesystem` is true), the browser for sign-ins and the
  terminal.
- **The details** (plan 5).
  - *The check.* `cmd/marshal` hands `launch.Run` the daemon socket
    (`config.DaemonSocketPath`) unless `--embedded` is given. `launch` reads
    `GET /api/daemon` once, bounded at 500ms. Daemon mode needs an answer
    whose profile is `local`, and a project whose trust needs no question:
    `trust.Evaluate` on the project root reports no prompt (no project config,
    or a permanent record whose hash still matches). An untrusted project runs
    embedded, where the question is asked, until plan 7 brings D8. Any other
    outcome runs embedded too, silently: no daemon is the ordinary case.
  - *The root.* The TUI attaches with `repo.Root` of its working directory,
    and its working directory becomes the session's (`Status().WorkingDir`),
    the project root the child runs in. A launch from a subdirectory starts
    its daemon session at the root.
  - *The connection.* `Client.Attach` with a 30s handshake bound, then
    `rpc.Dial` over `rpc.NewStream` with P3's frame cap and
    `SharedFilesystem`, with no version demand, then `Host.Attach` of the
    session the daemon named. The TUI's context, not the handshake's, carries
    the stream afterwards.
  - *The client log* is `<data>/daemon/tui.log`, mode `0600`, appended.
  - *The quit key.* Ctrl+C pressed twice while no run is in flight and no
    ending is under way detaches: no warning, no end flow, no close. Pressed
    twice during a run it stops the run, as in embedded mode. A new TUI-only
    command, `/detach`, detaches at any time, a run in flight included. In
    embedded mode `/detach` explains that only a daemon session can be
    detached, and the quit key keeps running the end flow. `/exit` and `/quit`
    end the session in both modes.
  - *After the program.* A detach prints the session's ID and
    `marshal attach <id>` to the terminal. A lost connection returns an error
    that names the same command. A `/resume` reattaches to the chosen session
    after the end flow closed the current one, as embedded mode restarts its
    runtime.
  - *`marshal attach`* needs a daemon: without one it fails and says how to
    start one. With no ID it attaches to the first session of the project's
    child in `GET /api/daemon` (most recently active first), and fails when
    the child has none. It holds the same trust condition as `chooseDaemon`,
    through the same predicate (`daemonTrustNeedsAsking`): a project whose
    trust needs the question is refused before the daemon is even probed —
    attach runs no embedded startup, so it cannot ask it — with a message
    naming the root and saying `marshal` there answers the question. (Plan 7
    removes this refusal with the launch's condition.)
  - *Following.* A run is followed when the status shows one while the TUI
    is not busy and holds no run call of its own. `busy` and the turn's start
    follow it, and its ticks start. When the status's run is gone, or is
    another, the TUI finishes it through the same handler its own run's end
    uses, with the error `lastRun` describes: none for `done`, the cancelled
    context for `cancelled`, `lastRun.error` for `failed`, a
    `CodeHumanGateRequired` error for `gated`. A stop the user asks for while
    following is the TUI's own stop, so its steering clear applies. The check
    runs on every change and at the end of the TUI's own run's finishing, so a
    run another viewer started just as this TUI's ended is not missed.
  - *Watch resumes.* A new model takes the status's watch-resume sequence as
    already seen, so attaching never replays a wake that fired before it. A
    watch resume the session refuses because another run holds the slot
    writes no note: two viewers can both answer one wake, and one wins. A wake
    that fires while no viewer is attached is not resumed (H8).
  - *Versions* (D11). A daemon-mode TUI whose agent reports another
    `agentInfo.version` writes one note naming both versions before the
    program starts.

### D6. A run's notes are the agent's

- The service writes a run's outcome note when the run ends, before the
  run's call is answered, with the sentences and the roles the TUI writes
  today:
  - "Agent turn cancelled." when `session/stop` stopped the run and the
    session is still open. A run the end flow or the session's close stopped
    writes nothing, as today;
  - "✘ Turn failed: " and the error's first line when the run failed, or
    "✘ Plan authoring failed: " for an authoring run. An error that is a
    cancellation, or a gate's `CodeHumanGateRequired`, is not a failure.

  `handleAgentFinished` and `handlePlanAuthorFinished` stop writing these
  notes. In embedded mode the transcript is unchanged, line for line: the
  TUI's note tests, driven through real runs over the pipe, prove it. T6's
  goldens cover rendering, not notes.
- The TUI keeps everything else a run's end does: `busy`, the success pulse,
  setting or clearing the notice, clearing the steering queue after its own
  stop, the steering flush, the gate panel, and "Plan accepted in Full Auto"
  (the authoring result is its caller's).
- A run refused before it started (`CodeBusy`, `CodeNoRunner`, an ending
  conversation) is answered to its caller, whose TUI writes the refusal note
  as today. Nothing ran, so the service writes nothing.

### D7. The project environment

- In the local profile, the bridge resolves each project's environment once,
  before starting its child. It runs the user's login shell (`$SHELL`, else
  `/bin/sh`) in the project directory as `-l -c` with a command that prints
  the environment NUL-separated, bounded at 10s. When the project has an
  `.envrc` and `direnv` is on PATH, it overlays `direnv export json` from the
  project directory.
- A failed or timed-out resolution falls back to the bridge's own
  environment, and leaves a warning in the project's status for the TUI and
  the web UI to show.
- **The cache** is keyed by project and invalidated when any of these change
  (by modification time, checked at each child start): the project's
  `.envrc`, `.tool-versions`, `mise.toml` and `.mise.toml`, and `.nvmrc`, and
  the user's login-shell startup files. A changed environment takes effect
  at the child's next start, because a running process's environment cannot
  change.
- Secrets in the environment never reach a log or an event. The server
  profile keeps supplying environments explicitly (design §6.1).
- **The details** (plan 6).
  - *The printer.* The shell runs `exec '<webbridge>' print-env`, a new
    `webbridge` subcommand that writes a marker and then the environment,
    NUL-separated, so whatever a startup file prints before it is skipped. The
    format lives in one place, `bridge.WriteEnv`, beside the parser. A
    webbridge path holding a single quote cannot be quoted for every shell and
    falls back with a warning.
  - *Two steps, each bounded at 10s:* the login shell, then the direnv
    overlay. direnv is looked up on the resolved `PATH`, not the bridge's. Its
    JSON's `null` unsets a variable. A failed overlay keeps the login
    environment and warns.
  - *What the bridge keeps.* `MARSHAL_CONFIG_DIR`, `MARSHAL_DATA_DIR`,
    `XDG_CONFIG_HOME` and `XDG_DATA_HOME`, when the bridge has them, override
    the resolved values, so a child resolves the directories the TUI does.
  - *`$SHELL` reaches the daemon.* `marshal daemon install` adds `SHELL` to
    the unit's environment, beside `PATH` and the location variables. A user
    service has no `SHELL` of its own.
  - *The startup files* are a fixed list stat'ed whatever the shell:
    `~/.profile`, `~/.bash_profile`, `~/.bash_login`, `~/.bashrc`,
    `~/.zshenv`, `~/.zprofile`, `~/.zshrc`, `~/.zlogin` and
    `~/.config/fish/config.fish`. A file appearing or disappearing changes
    the fingerprint as a changed time does. Only a successful resolution is
    cached, so a failure is retried at the next start.
  - *The warning* names what failed ("the login shell timed out after 10s",
    "direnv export exited with status 1") and never carries the commands'
    output. `GET /api/daemon` reports it per child as `envWarning`, and
    `marshal daemon status` prints it. The TUI does not show it yet (H9).
  - *Where it applies.* Only a fleet given a resolver resolves. The local
    profile gives its fleet one; the server profile and the end-to-end
    harness do not, so their children inherit the bridge's environment as
    before.

### D7a. Idle sessions end (plan 6)

- The bridge enforces decision 4's `daemon.idle_session_timeout`, because it
  is what knows a session's viewers. Every 30s it looks at each project
  child's tracked sessions. A session with no viewer whose last activity is
  older than the timeout is asked for its status on the bridge's own
  connection (`session/status`, which serves a client without the marshal
  capability). A session with a `run` is left alone. Its idle time runs from
  the later of its last activity and `lastRun.endedAt`, so a run that ends
  while no one watches gets the full timeout. An idle session is closed as a
  viewer's close is (`Registry.Close`), and a child left with no session
  starts its linger.
- **The value** is `[daemon] idle_session_timeout` in the user config, a
  duration, default `30m`; `0` never ends a session. `webbridge` takes it as
  `--idle-session-timeout` (local profile only), and `marshal daemon install`
  writes the user config's value into the unit, so a change takes effect at
  the next install.
- **A known race.** An attach that resolves a session just as the sweep
  closes it gets a session the child no longer holds, and the TUI reports
  that the session is gone; the next `marshal attach <id>` resumes it.
- The status line `marshal daemon status` prints per child (D11) is written
  here too: its root, version, session count and environment warning.

### D8. Trust for daemon sessions

- A session the bridge starts on a project whose config is not trusted
  defers the decision to its client, as embedded mode does. It starts
  without the project config. `trust/prompt` answers the project directory,
  and `trust/decide` records the decision in the trust store and reloads the
  session's layers with it, in place, as the in-process reload does.
- The child gets a resolver that defers in this way only for sessions a
  marshal client will drive. For every other session (fleet agents, the
  control agent) the headless rule stays: fail closed, record nothing.
- **The details** (plan 7).
  - *Which sessions.* The bridge marks the sessions it opens for an attach:
    on a project child's registry, `session/new` and `session/resume` carry
    `clientTrust: true`, and so does the resume after a child restarts. The
    child honours the mark on `session/new`, `session/load` and
    `session/resume` with `app.WithClientTrust`, which wins over the headless
    resolver the child gives every runtime. A viewer's `/new` (`session/new`
    with `replaces`) builds its session inside the same runtime, so it keeps
    the runtime's answer. The mark is per registry in P4, because every
    session on a project child is a TUI's until plan 10 (D10) brings fleet
    agents there; that plan must mark sessions one by one.
  - *Starting.* A client-trust runtime evaluates trust as embedded mode does
    (`trust.Evaluate`): a stored permanent record whose hash matches applies
    the project config, and a needed question skips it and leaves the prompt
    pending.
  - *Who answers.* `trust/prompt` and `trust/decide` serve marshal clients
    only. The bridge's own connection reads no prompt and has its decision
    refused, so the web UI cannot answer a TUI's question (decision 8).
  - *The prompt says how a trust applies.* `trust/prompt` answers
    `inPlace: true` for a daemon session. The TUI's trust panel then closes
    after a trust instead of ending the program, and opens the connect panel
    if the TUI was to open it at start, as embedded mode's restart does.
    Embedded mode keeps its restart.
  - *Applying a trust in place.* The project's layers are loaded with trust
    first, so a project config that does not load is refused before anything
    is recorded. Then a permanent answer is recorded in the trust store with
    the config's hash, the session is marked trusted, the runtime is reloaded
    from the new merged config (its `ConfigReloader`), and the session's
    layers are replaced. The agent writes one note saying the project config
    is in force. A reload that fails marks the session untrusted again and
    leaves the prompt pending; a permanent answer already recorded stays
    recorded, since embedded mode's restart would fail the same way.
  - *Settled.* After a trust the prompt answers no directory, and another
    viewer's answer to the same prompt is accepted without effect: one wins.
    A decline records nothing and keeps the prompt pending, as in embedded
    mode, so `/trust` asks again, and so does a later attach, as embedded
    mode's `/resume` does.
  - *A session-only trust* lasts as long as the session's runtime. A detach
    and a reattach keep it. A session resumed after a close (an idle end
    included), or after its child restarted, starts untrusted and asks
    again.
  - *What does not reload in place:* project plugins (H10).
  - *`launch`* drops `daemonTrustNeedsAsking`: `chooseDaemon` no longer
    weighs trust, and `marshal attach` no longer refuses an untrusted
    project.

### D9. Pinned projects

- `pinned` is a project setting in the workspace registry. `marshal daemon
  pin [dir]` and `unpin` set it, and so can the web UI's project settings
  (later).
- A pinned project's child runs while the project is pinned, holding its
  project host open through a new host-level method, `project/hold`
  (released by `project/release`). The host's index watcher runs for as long
  as it is held, whatever `indexing.watch` says (decision 6). Language
  servers still stop after their idle linger, and embedding calls stay
  debounced and batched (design §4.3).
- Unpinning releases the hold. The child stops after its linger once it has
  no session.

### D10. Fleet agents on the project's child

- With the local profile (and with no container runtime in any profile), a
  fleet agent's session runs on its project's child, beside the TUI's
  sessions, instead of on a child of its own. The agent keeps its bridge ID,
  its registry entry, its event log and its lifecycle (finish, merge,
  discard). Finishing an agent closes its session, not the child.
- An isolated agent's worktree is its session's active root, as today, so
  the shared project host serves its index and pools its language servers by
  root (P1).
- Containerized agents keep a child each (H1).

### D11. Versions

- The TUI refuses an agent whose marshal protocol version differs from its
  own, as P3's `initialize` check does. In daemon mode it warns, and does not
  refuse, when the agent's `agentInfo.version` differs from its own build
  (design §6.4). Embedded mode keeps refusing a mismatch, since both ends are
  one binary.
- `marshal daemon status` reports the daemon's and the children's versions
  (plan 6, D7a).

### D12. OAuth refresh runs once

- A lock file in the data dir, `oauth-refresh.lock` (`flock`), names the one
  process that refreshes. The daemon supervises a small helper,
  `marshal daemon refresh`, which takes the lock and runs the refresh workers
  for the user config's OAuth providers. It re-reads the user config when the
  file changes.
- Embedded `marshal` runs its workers only if it gets the lock, and stops
  them when it exits. So with a daemon running no embedded TUI refreshes, and
  two embedded TUIs no longer race.
- A process that loses the lock does nothing else: every process still reads
  tokens from the keychain on use.
- **The details** (plan 8).
  - *The lease.* `oauthworker.TryLease` takes an exclusive, non-blocking
    `flock` on `<data>/oauth-refresh.lock` (mode `0600`), as `index.TryLease`
    does for a project's index. The kernel drops it when its holder exits,
    however it exits.
  - *Embedded mode* takes the lease only when its runtime has a refresh
    worker to run (an `openai_codex` provider signed in with OAuth), so a TUI
    without one never touches the file. Holding it, the runtime's workers run
    until the runtime closes, and the lease is released after they return.
    Not holding it, the runtime tries again every 30s, so an embedded TUI
    takes over when the daemon stops. The process's next runtime (a resume, a
    trust reload) waits up to 2s for the previous one's release before it
    tries.
  - *A lease that cannot be taken* for any reason other than another holder
    (the file cannot be opened, or the platform has no lock binding) is
    logged, and the workers run without it, as before P4.
  - *`marshal daemon refresh`* runs until it is signalled. Every 5s it reads
    the user config's modification time and size; the project config is
    never read, since providers are user-global. While the config names an
    OAuth provider it holds the lease (trying every 30s while another process
    holds it) and runs one worker per provider. A change rebuilds the workers
    under the same lease, and a config that names none releases the lease. It
    logs to standard error.
  - *The daemon supervises it* only when started with `--oauth-refresh`
    (local profile only), which `marshal daemon install` writes into the
    unit. A bridge the user runs by hand, and every test, starts no helper
    unless asked. The bridge runs `<marshal-bin> daemon refresh` with its own
    environment and standard error. It restarts the helper after a backoff
    that starts at 1s, doubles to at most 1m, and resets after a run that
    lasted a minute, and it stops the helper (SIGTERM, then SIGKILL after 5s)
    when the bridge stops.
  - *`GET /api/daemon`* reports the helper as `refresh` (its process ID and
    its restart count) when the bridge supervises one, and `marshal daemon
    status` prints a `refresh:` line saying whether it runs.

## Design

| Area | Change |
|---|---|
| `cmd/webbridge`, `web/bridge` | `--profile local`; the unix listener and the peer check; project children; the attach handshake and the relay; the environment resolver and `webbridge print-env`; the idle-session sweep and `--idle-session-timeout`; the client-trust mark on a project child's sessions (plan 7); the refresh helper's supervision behind `--oauth-refresh` and its `refresh` report (plan 8); pins; host-process agents on project children. Standard library only, as ever. |
| `cmd/marshal` | `marshal attach`, `marshal daemon install/uninstall/start/stop/status/pin/unpin/refresh`, `--embedded`. Install writes `SHELL`, the idle timeout and `--oauth-refresh` into the unit; status prints each child and the refresh helper. |
| `internal/commands` | The TUI-only `/detach` (plan 5). |
| `internal/daemon` (new) | The service units and the commands that drive them, and the socket's HTTP client with its attach handshake, which plan 5's daemon mode reuses; the unit's `--oauth-refresh` and the answer's `refresh` (plan 8). |
| `internal/app/config` | `DaemonDir` and `DaemonSocketPath`, beside `DataDir`; the `[daemon]` section's `idle_session_timeout` (plan 6). |
| `internal/acp` | `--viewers`: concurrent marshal connections that notify on their own connection; one run slot per session; `project/hold` and `project/release`; `clientTrust` on the lifecycle calls, and the trust calls served to marshal clients only (plan 7). |
| `internal/acp/service` | Runs on the session's context; `run` and `lastRun` in the status; run outcome notes; a trust applied in place and its settled prompt (plan 7). |
| `internal/acpwire` | `SessionStatus.Run`, `LastRun`; `TrustPromptResult.InPlace` (plan 7); the hold methods. |
| `internal/acpclient`, `internal/acpclient/rpc` | No new method; the relay is a byte stream `rpc.NewStream` already frames. `Host.TrustPrompt` returns the whole prompt, its `InPlace` included (plan 7). |
| `internal/launch` | The daemon check, the attach handshake, daemon mode's loop and its detach, and the client log; plan 7 drops the check's trust condition. |
| `internal/app/tui` | Quit and `/detach` detach in daemon mode; `busy` follows a run it did not start; an attach never replays an old wake; the outcome notes leave `handleAgentFinished` and `handlePlanAuthorFinished`; the trust panel closes after a trust applied in place (plan 7). |
| `internal/app` | `WithClientTrust` and the trust applied in place (plan 7); the refresh lease around the OAuth workers, and `RunOAuthRefresh` for `marshal daemon refresh` (plan 8). |
| `internal/llm/provider/oauthworker` | The refresh lease, `TryLease` (plan 8). |
| `internal/app/session` | The run claim both run slots take (plan 3). |
| `test/daemon` (new) | The end-to-end attach test: a real bridge and a real child in one test process, driven by a scripted client (plan 4); the reattach to a running turn against a test-owned provider, and `launch`'s daemon mode detaching from it (plan 5); a daemon session asking its client about trust (plan 7). |

## Migration order

Two plans at a time where they share no files, as in P3.

1. **Turns outlive their caller** (D4, D6): the session's context, `run` and
   `lastRun`, and the outcome notes moved into the service. Agent side, plus
   the TUI's two finish handlers; proved in embedded mode by the TUI's note
   tests driven through real runs. Alongside plan 2.
2. **The local profile** (D1): `--profile local`, the socket, the peer check,
   no containers, `GET /api/daemon`, and
   `marshal daemon install/start/stop/status/uninstall`. The bridge,
   `cmd/marshal`, `internal/daemon` and `config` only. Alongside plan 1.
3. **Concurrent viewer connections** (D2's `--viewers` and the single run
   slot), in `internal/acp`, `internal/app/session` and `cmd/marshal`'s `acp`
   dispatch, proved with two marshal clients on one session. Alongside plan
   4.
4. **Project children and the relay** (D2, D3): the per-project child, the
   handshake, the relay and its registry tracking, in the bridge and
   `internal/daemon`. Proved against a fake child in the bridge's own tests.
   Its last task, run once plan 3 is on `main`, proves it with a scripted
   client over the socket against a real child (`test/daemon`).
5. **The TUI's daemon mode** (D5, D11's warning): the check, `marshal
   attach`, detaching and `/detach`, following a run, `/resume` and the
   version warning, in `internal/launch`, `internal/app/tui`,
   `internal/commands`, `cmd/marshal`'s dispatch and `test/daemon`. The
   done-when test lives here. Alongside plan 6.
6. **The project environment and idle sessions** (D7, D7a, D11's status
   lines), in the bridge, `cmd/webbridge`, `internal/daemon`, `config` and
   `cmd/marshal`'s `daemon` command. The idle timeout moved here from plan 5
   because the bridge enforces it, and plan 5 then touches no bridge file.
   Alongside plan 5.
7. **Trust for daemon sessions** (D8), in `internal/app`, `internal/acp`,
   `internal/acp/service`, `internal/acpwire`, `internal/acpclient`,
   `internal/app/tui`, `internal/launch`, the bridge's registry,
   `cmd/marshal/main.go`'s comment and `test/daemon`. Alongside plan 8.
8. **OAuth refresh once** (D12), in `internal/llm/provider/oauthworker`,
   `internal/app`'s embedding and a new refresh file, `cmd/marshal`'s `daemon`
   command, `internal/daemon`, the bridge's new helper supervisor and
   daemon answer, and `cmd/webbridge`. Alongside plan 7.
9. **Pinned projects** (D9). Pins moved here from plan 7: their
   `marshal daemon pin`, the bridge's start-up of pinned children and the
   unit's environment share `cmd/marshal/daemon.go`, `cmd/webbridge/main.go`
   and `internal/daemon` with plan 8, while trust and the refresh share no
   file.
10. **Fleet agents on the project's child** (D10). Last: it changes the
    fleet's lifecycle, which every earlier plan leaves alone. Plans 9 and 10
    both change a project child's lifecycle, so they run one after the
    other.

## Tests

- **W1. Reattach.** A scripted TUI client starts a turn whose fake runner
  blocks, closes its connection, and the run keeps going. A second client
  attaches, sees the run in `run`, reads the live transcript, answers an
  approval the run raises, and gets `lastRun` when the run ends. This is the
  done-when, run against a real daemon and child in one test process.
- **W2. Two viewers.** Two marshal connections on one session each see
  every update, keep their own busy flags and subscriptions, and only one
  answer to an approval wins (`CodeRequestSettled` for the other).
- **W3. One run slot.** A turn started on the bridge's connection refuses a
  marshal client's prompt, and the other way round.
- **W4. The relay keeps order.** P3's read-your-writes tests (V3) pass
  through the relay, with the agent's update frames slowed.
- **W5. Detach does not stop.** In daemon mode, quitting the TUI during a
  turn leaves the run in flight, and `/exit` stops it through the end flow.
- **W6. Embedded unchanged.** The TUI suite, its note tests driven through
  real runs, and T6's goldens pass with the outcome notes written by the
  service, and the usability suite passes.
- **W7. Local profile.** The socket and directory modes, the live-socket
  refusal and the stale-socket removal, the peer check refusing another UID
  (the test injects the wanted one), a token-free request on the socket, and
  no runtime probe, container command or proxy at start. The service units
  render with explicit paths, and every service command runs the
  `launchctl` or `systemctl` command lines a fake runner records.
- **W8. Environment.** A login shell's exported variable and a direnv
  overlay reach the child, a timeout falls back with a warning, and a changed
  `.envrc` changes the next start's environment.
- **W9. Refresh once.** Two processes contend for the lock and only one runs
  workers; the holder exiting lets the other take over. An embedded runtime
  with no OAuth provider never takes the lock, and the helper rebuilds its
  workers when the user config changes.
- **W11. Trust over the daemon.** A daemon session on an untrusted project
  starts without the project config and its marshal client is asked. A
  permanent answer records the trust and loads the config in place, with the
  session kept and the client's program still running; a second session
  starts trusted and asks nothing. A session-only answer records nothing,
  and the bridge's own connection can neither read the prompt nor answer it.
- **W10. Suite.** Every plan keeps `go test ./...` green, apart from the known
  local failures, and `TestWebIsStdlibOnly` passes.

## Done when

- `marshal daemon install` starts a daemon that survives logout of the
  terminal, and `marshal` in a project starts its session there.
- Quitting the TUI mid-turn leaves the turn running; `marshal attach`
  reattaches to it, and the TUI shows the live transcript, its open requests
  and, when it ends, its outcome, as if it had never left.
- Plain `marshal --embedded`, and `marshal` with no daemon, behave as after
  P3.
- W1 to W11 pass. A manual run covers the above, plus a sudo prompt and an
  MCP sign-in through the daemon, a pinned project's embeddings refreshing
  after an edit with no session open, and a project whose `.envrc` sets the
  toolchain.

## Findings while drafting

- **A TUI behind today's bridge would quietly lose P3's guarantees** (by
  reading). The bridge's children see a client without the marshal
  capability, so a TUI's calls would reach ACP's handlers rather than the
  service. Its mirror would get no `session_status`, `pending_requests` or
  `session_config`, and its responses would race its updates across HTTP and
  SSE. That is why decision 1 relays the TUI's own connection instead.
- **Leaving cancels the turn** (by reading). The service's run context is the
  call's (`service/turns.go:190`), and a closed connection cancels its
  handlers (`server.go:829`). So today a TUI over any transport that hangs up
  mid-turn stops its own run. Embedded mode never noticed, because quitting
  ends the runtime anyway. D4 fixes it.
- **The host's single notification sink assumes one connection at a time**
  (by reading, `host.go:361`). It is what lets an orphaned turn report to the
  next connection after `--listen`'s reconnect. Concurrent viewers need their
  own path (D2), and the sink stays for ACP's own turns.

## Implementation record

### Plan 1 — turns outlive their caller

Run 2026-11-09 from [the plan](plans/2026-11-09-p4-1-runs-plan.md) on branch
`p4-1-runs`, one commit per task, mixed execution: tasks 1 and 4 delegated,
tasks 2, 3 and 5 inline. `main` did not move while it ran.

**The run path.** `runTurn` (`internal/acp/service/turns.go`) takes a
`runSpec`: its kind, whether it detaches, the work, and an optional `finish`
hook. The five detaching kinds — `turn`, `review`, `agent`, `swarm` and `sdd` —
derive the run's context from `s.ctx`, so a caller whose call ends (a
`$/cancel_request`, a closed connection) answers at once with its context's
error and leaves the run going. `author` keeps deriving it from the call's
context, because only its caller reads the result. Admission — the slot,
`BeginWorkCancelable`, `BeginTurnStop` — stays on the caller's goroutine, so every
refusal and every stop that skipped the run is still answered synchronously.

The run and its whole ending run on a goroutine of their own, and the caller
waits on a buffered channel of one or on its own context. `endRun` does the
work, then `finish` (Prompt's put-back of a queued text a stop skipped), then
the outcome note, then sets `lastRun` and releases the slot in one critical
section, and only then returns the session to idle, ends the stop group and
the work, and cancels the run's context.

**The status.** The slot carries an `acpwire.Run`, set at admission from the
stop group's turn ID, the spec's kind and `state.Now()`. `lastRun` is set from
it when the ending releases the slot, so a status read never shows neither or
both. A refused start sets neither. The outcome is checked in this order:
`gated` (the error carries `CodeHumanGateRequired`, even when stopped),
`cancelled` (the slot was stopped, the error is `context.Canceled`, or the
run's context ended), `failed` (any other error), `done`. `lastRun.error` is
set for `failed` only, as `runError`'s result: a coded error kept as itself,
any other wrapped uncoded with its own sentence.

**The notes are the agent's (D6).** `runNote` writes "Agent turn cancelled."
for a cancellation whose stop was the user's — the slot was stopped, its
`ending` flag clear, and the session not closed — and "✘ Turn failed: …" or,
for an authoring run, "✘ Plan authoring failed: …" with the first line of the
sentence. `stopRun(cause, ending)` is `Stop`'s body; `Stop` passes `false` and
`Release` passes `true`, so a release, a swap, a close and the end flow (which
never calls `Stop`) write nothing.

The TUI keeps what is its own: `handleAgentFinished` no longer writes the
cancel note or the failure note, and `handlePlanAuthorFinished` no longer
writes either of its own. Both keep the notice work and the refusal note;
`handleAgentFinished` also keeps the steering clear and the success pulse,
and `handlePlanAuthorFinished` keeps the review-panel finishing. Nothing ran
behind a refusal, so the refusal note stays the TUI's.

**Embedded mode is unchanged for a user.** Esc still stops a run at once,
through `session/stop`; the notes keep their sentences and the transcript
keeps its rows, except that the cancel note now lands when the run has
actually ended, arriving with (not before) the TUI going idle.

**Tests.** New: `internal/acpwire`'s `TestAStatusWithoutRunsOmitsThem`, with
`TestStatusUpdateRoundTrips` extended to carry both fields and a coded error;
`internal/acp/service`'s `TestACallerThatLeavesDoesNotStopTheTurn`,
`TestAStopEndsADetachedRun`, `TestEachDetachingKindOutlivesItsCaller` (one row
per detaching kind), `TestAnAuthoringRunEndsWithItsCaller`,
`TestAQueuedTextIsPutBackWhenItsCallerLeft`, `TestOutcomesAreRecorded`,
`TestARefusalLeavesTheRunsAlone`, `TestAReleaseEndsARunWithoutAUserStop`,
`TestASwapEndsADetachedRun`, `TestAUserStopWritesTheCancelNote`,
`TestAFailedRunWritesItsNote`, `TestNoNoteForAnEndingOrAGate`,
`TestTheNoteLandsWhenTheCallerLeft` and `TestARunNoteIsWrittenOnce`; and
`internal/acp`'s `TestARunOutlivesItsConnection`, `TestTheStatusCarriesTheRun`
and `TestTheCancelNoteReachesTheCaller`, over a dialed client host.

Inverted and moved: `TestCancellingThePromptStopsTheRun` became
`TestCancellingThePromptLeavesTheRunGoing`, and `TestCancellingAPlanRunStopsIt`
became `TestCancellingAPlanRunLeavesItGoing`.
`TestAgentFinishedErrorSetsNoticeAndCompactRow` became
`TestAgentFinishedErrorSetsTheNotice` (the handler and the banner) plus
`TestAFailedTurnWritesOneCompactRow` (a real failing turn through `Update`), and
`TestPlanAuthorFailureDoesNotDoubleReport` now drives a real failing authoring
run through `/sdd new`. `TestCancelledTurnClearsProviderNotice` dropped its note
assertion, and `TestAStoppedAuthoringIsNotAFailure` waits for `Status().Run` to
clear before counting, because the authoring call now returns before the
run's ending writes the note. No other TUI test needed a fix: none of the
triage cases (a note asserted through a handler call, a transcript read
straight after a stopped run, a fake run ended only by its call's context)
turned up outside the tests named above.

**Where the plan was short.** Three gaps were filled:
- The run registers with `BeginWorkCancelable(slot.cancel)` rather than
  `BeginWork`. The plan's premise — "the end flow stops a run through the
  runtime" — was only true of the caller's context before this plan: the
  runtime's stop pass cancels a parent operation's work hook, and the service
  registered none. Without the hook, a run whose caller had left made
  `Runtime.Quiesce` wait on work nothing could stop, which the full suite
  caught as a 20-minute hang in `internal/app`'s
  `TestRuntimeSendNowTUIOptionsExerciseRealFacade`. With it, the end pass
  cancels the run's context exactly as the plan assumed, the outcome is
  `cancelled`, and no note is written.
- The control test harness was split into `serveControl` (the agent end, over
  the pipe's client transport) and `startControlAgentWith` (which wraps it in
  `rpc.NewConn` exactly as before), because `TestTheStatusCarriesTheRun` and
  `TestTheCancelNoteReachesTheCaller` need a dialed `rpc.Host` to read the
  client's own mirror and transcript. Test-only, every existing call site
  unchanged.
- Task 4's mutation check ("make the caller wait for the run even after its
  context ends") is caught by `TestACallerThatLeavesDoesNotStopTheTurn`, not by
  `TestARunOutlivesItsConnection`: the wire test starts the prompt on its own
  goroutine, so a blocked caller does not fail it.

Task 3's `./internal/acp/...` leg was red on exactly the two tests Task 4
inverts (`TestCancellingThePromptStopsTheRun`, and `TestStackRoundTripBudget`
as the known load flake), which the plan's task order makes unavoidable since
Task 4 follows Task 3.

**Open for plan 5.** The TUI reads neither `run` nor `lastRun` yet: it drives
stops with `session/stop` and its own `cancelling` flag, which is what a
reattaching TUI's finishing (D5) builds on.

### Plan 2 — the local profile

Run as tasks 1 to 5 of
[`plans/2026-11-09-p4-2-local-profile-plan.md`](plans/2026-11-09-p4-2-local-profile-plan.md),
one commit each, then the smoke run and the full suite below.

**The flags and their refusals** (`cmd/webbridge/main.go`, `applyProfile`):

- `--profile` (`WEBBRIDGE_PROFILE`), default `server`; any other value is
  `--profile %q: expected server or local`.
- `--socket` and `--addr off` are refused under the server profile with
  `... is for --profile local`, so a setting that belongs to the local profile
  cannot be given and silently ignored.
- Under `local`: `--egress on` is refused (`the local profile runs no egress
  proxy`) and any `--secrets` but `env` is refused (`the local profile takes
  secrets from the environment`). Each refusal fires only when the setting was
  given by its flag or its variable; otherwise egress is quietly `off`.
- The defaults apply only when both the flag and its variable are unset, all
  under `bridge.DefaultDaemonDir`: `--socket` `<data>/daemon/marshal.sock`,
  `--state-dir` `<data>/daemon/state`, `--workspace`
  `<data>/daemon/fleet.json`. The working directory is registered as a project
  only when `--cwd-root` or its variable was given; otherwise `cwdRoot` stays
  empty and `run` skips it.
- In `run`: `SetHostProcessesOnly` before the fleet starts anything, then
  `PeerChecked(ListenLocal(cfg.socket))` served by `SocketHandler()` on its own
  `http.Server`; `DaemonInfo` set to the build's version (`dev` when
  unstamped), `local`, the pid, the start time and the socket;
  `webbridge: listening on unix://<path>` printed to stderr. Under `--addr off`
  no TCP or preview listener opens and no token is generated. The socket's
  server shuts down beside the TCP one, which closes and removes the socket. A
  refusal from `ListenLocal` closes the fleet and returns the error, as a
  failed TCP listen does.

**The socket's checks** (`web/bridge/localsocket.go`). `prepareLocalDir` runs
before the listen: an absent directory is created `0700`; an existing one must
be a directory (`... is not a directory`), the user's own (`... belongs to
another user`), and closed to group and other (`... is open to other users
(mode %04o, want 0700)`). A refused directory is left exactly as it was — it
never chmods one it did not create — and a regular file at the path is refused
and kept. `net.Listen` yields a `*net.UnixListener`, which sets
`SetUnlinkOnClose`, so `Close` removes the socket. A path longer than the
platform's limit fails at listen with the system's own error, unchanged. `ErrDaemonRunning` comes from a real dial, not
from the file's existence: an answered dial means a live daemon (`a daemon is
already listening on <path>`, the smoke run's step 5), and a refused one means
a stale socket, which is removed and replaced.

**The peer check, per system** (`web/bridge/peercred_{linux,darwin,other}.go`).
`peerUID(conn)` reaches the descriptor through the connection's `SyscallConn`
and its `Control`, never through `File`, which would duplicate the descriptor
and lose what the kernel recorded against it.

- linux: `SO_PEERCRED` and its `ucred`.
- darwin: declares `xucred` and calls `getsockopt(SOL_LOCAL, LOCAL_PEERCRED)`
  through `syscall.Syscall6` with the struct's address and size, and refuses a
  `Version` other than 0. This is the one call outside `syscall`'s typed
  wrappers, and `TestPeerUIDIsOurs` runs it on this machine.
- other systems: no answer, so `ListenLocal` refuses before it creates a
  directory or binds, and the local profile does not start.

`PeerChecked` keeps the wrapped listener's `Close`, `Addr` and its failed
`Accept` unchanged; a connection from another uid, or one whose peer cannot be
read, is closed and logged, and the loop accepts again.

**The host-only fleet** (`web/bridge/fleet.go`). `SetHostProcessesOnly` sets
`hostOnly`, and `f.containerRuntime()` answers none before any probe. Its six
callers are the agent factory (`fleet.go:400`), `buildControlChild`
(`control.go:65`), `derive.go:109`, `egress_fleet.go:46`, `wsbuild.go:251` and
`wsspawn.go:61`. `detectedRuntime` stays as the default probe, and the
`egressRuntime` override still wins first. `TestAHostOnlyFleetNeverProbes` pins
the whole set: the counting probe is never asked, the fake runner records no
command, and both the agent factory and `buildControlChild` build a
host-process `Child`. In the smoke run, the machine's own nine Docker Desktop
processes were the same before and after the daemon started and stopped.

Every container-only operation therefore answers `501 Not Implemented`:
`ErrWorkspaceNeedsRuntime` (`wsspawn.go`) is what a workspace spawn returns,
`runtimeName` wraps it for a workspace build, and `writeErr` maps it rather
than letting it fall through to `502`, which would report a profile limitation
as a gateway fault.

**`SocketHandler`** (`web/bridge/http.go`). `ServeHTTP`'s routing moved into an
unexported method taking the API handler: `ServeHTTP` calls it with the
token-checked mux and `SocketHandler` with the bare one. Nothing `ServeHTTP`
answers changed, and the existing bridge tests pass unchanged.

**`GET /api/daemon`** (`web/bridge/daemoninfo.go`). `DaemonInfo` and
`SetDaemonInfo`; the handler copies the info and fills `Projects` from the
workspace per request, sorted and never nil so it encodes `[]` rather than
`null`. Registering without info answers 404 `not a daemon`. `StartedAt` is
Unix milliseconds, which is what `marshal daemon status` formats, and `Socket`
is the daemon's own path — under the default data dir that contains the
username, the same class of path `/api/projects` already returns, with the
route bearer-gated on TCP and peer-checked on the socket.

**`internal/daemon`** (`daemon.go`, `units.go`, `manager.go`, `client.go`,
`find.go`) and `config.DaemonDir`/`DaemonSocketPath` beside `DataDir`.
`PathsFor` gives the directory, `marshal.sock`, `state`, `fleet.json` and
`webbridge.log`; the label is `dev.marshal.daemon` and the unit
`marshal-daemon.service`. The client reads `GET /api/daemon` over a socket
transport bounded by the caller's context, and `FindWebbridge` prefers
`--webbridge`, then the `webbridge` beside the symlink-resolved `marshal`, then
`PATH`.

**The unit's contents** (decision 10). The arguments are `--profile local
--socket … --state-dir … --workspace … --addr off --marshal-bin <marshal>`.

- The plist: `Label`, `ProgramArguments` (the webbridge, then those arguments),
  `EnvironmentVariables`, `RunAtLoad`, `KeepAlive` with `SuccessfulExit` false,
  and `StandardOutPath` and `StandardErrorPath` both the daemon's log. Every
  string is XML-escaped, so a path holding `&`, `<` or a quote still parses.
- The systemd unit: `[Unit] Description=Marshal daemon`; `ExecStart` with each
  argument double-quoted and `\`, `"` and `%` escaped as systemd requires; one
  `Environment=` per variable; `Restart=on-failure`; `StandardOutput` and
  `StandardError` at `append:<log>`; `[Install] WantedBy=default.target`.
  Variables are sorted in both formats, so a render is stable.
- The manager (decision 11) runs `launchctl print|bootout|bootstrap|kickstart`
  on darwin and `systemctl --user show|daemon-reload|enable --now|restart|stop|
  disable --now` on linux, every command through `Run` with its full command
  line; a "not loaded" or "not found" answer counts as done. `NewManager`
  refuses any `GOOS` but darwin and linux. `stop` is not durable on either:
  the unit stays installed and enabled, so the service returns at the next
  login, and `uninstall` is the only lasting stop.
- `install` and `start` then wait up to five seconds for `/api/daemon` to
  answer and print `answering`, or print `not answering yet` and name the log.
  Neither fails and neither stops the service, which the manager owns: the
  point is that `install && status` in one script does not fail only because
  launchd is still starting the process.

**The smoke run.** Both binaries built into `/private/tmp/p4-2-suite/bin`, then:

```console
$ webbridge --profile local --addr off \
    --socket /private/tmp/p4-2-suite/d/marshal.sock \
    --state-dir /private/tmp/p4-2-suite/d/state \
    --workspace /private/tmp/p4-2-suite/d/fleet.json \
    --marshal-bin /private/tmp/p4-2-suite/bin/marshal &
webbridge pid=10178
webbridge: listening on unix:///private/tmp/p4-2-suite/d/marshal.sock

$ curl -sS --unix-socket /private/tmp/p4-2-suite/d/marshal.sock http://daemon/api/daemon
{"version":"dev","profile":"local","pid":10178,"startedAt":1794261567935,"socket":"/private/tmp/p4-2-suite/d/marshal.sock","projects":[]}

$ stat -f '%N mode=%Sp (%Lp)' /private/tmp/p4-2-suite/d /private/tmp/p4-2-suite/d/marshal.sock
/private/tmp/p4-2-suite/d mode=drwx------ (700)
/private/tmp/p4-2-suite/d/marshal.sock mode=srw------- (600)

$ webbridge --profile local --addr off --socket .../d/marshal.sock ...   # a second one
webbridge: a daemon is already listening on /private/tmp/p4-2-suite/d/marshal.sock
second exit=1
first still answers

$ kill -TERM 10178; wait
first exit=0
socket removed: /private/tmp/p4-2-suite/d/marshal.sock
2026/11/10 08:59:28 INFO webbridge: shutting down
```

The curl was token-free, the directory and the socket were the modes above, the
refused second daemon left the first answering, SIGTERM exited 0 with the
socket unlinked, and no docker or podman process started (nine before, nine
after; all Docker Desktop's own). A first attempt that created the socket's
directory by hand at `0755` was refused — `socket directory ... is open to
other users (mode 0755, want 0700)` — which is the directory check refusing on
a real socket rather than only in `TestListenLocalRefusesAnOpenDirectory`.

**Full verification** in the worktree, at `b35748ce` plus these docs: `gofmt -l
internal cmd test web` printed nothing; `go vet ./...` and `GOOS=linux go vet
./web/... ./internal/daemon/ ./cmd/webbridge/` were both clean; `go test ./...
-timeout 20m` passed 121 packages with no failure. The first pass failed in
`internal/acp` — one of the plan's known `*Budget` load flakes — and that
package alone re-ran green (23.3s) on the second pass, with every other package
cached, so no new failure remains. `TestWebIsStdlibOnly` passed.

**Open for plan 4:** project children will listen in `<data>/daemon/state`.

### Plan 3 — concurrent viewer connections

Implemented in the worktree `p4-3-viewers`, one commit per task: `cf369d54`
(task 1), `540878f3` (task 2), `dd8dd151` (task 3).

**What was built.**

- **One run slot.** `session.State.ClaimRun` (beside `BeginWork`) reserves the
  session's single run behind its own leaf lock and returns an idempotent
  release; a nil state claims nothing. The service's `runTurn` claims it right
  after taking `s.turn`, gives the slot back and refuses with `errTurnBusy`
  when the claim is taken, and releases it in the critical section that clears
  `s.turn` — and on the `BeginWorkCancelable` refusal. ACP's `runTurn` claims
  it after reserving the connection's slot, deletes that slot and refuses with
  its existing sentence when the claim is taken, and releases before
  `close(slot.done)`, so the post-turn watch-resume waiter finds the slot free.
- **Viewer connections.** `registerHandlers` takes the notify function and a
  viewer flag. The stdio, `--listen` and embedding paths pass `h.sink.Notify`
  and false, so what they register is unchanged. `serveViewer` (`viewers.go`)
  builds a `Server` over the connection with `MaxLineBytes =
  viewerMaxFrameBytes` (64 MiB), `MarshalOnly` set and the host's logger,
  registers the handlers with the server's own `Notify` and true, and never
  sets the host's sink.
- **What a viewer refuses.** `Server.MarshalOnly` refuses a client that did not
  declare the marshal capability at `initialize` (recording nothing) and every
  call before a marshal `initialize` has been answered. A viewer's
  `session/new` serves only a blob with `replaces`; `session/load`,
  `session/resume` and `session/delete` answer the refusal.
- **Teardown.** `TurnManager.Close` collects every session with a projector
  under `stacksMu` (a subagent key is trimmed to its session) and drops each
  outside the lock. A viewer's cleanup calls it after today's cleanup; the
  stdio and `--listen` paths do not. A viewer also stays out of the host's
  canceller chain.
- **`--viewers`.** `listenUnix` is `ListenAndServe`'s socket preparation,
  shared. `RunWithViewers` binds the socket before serving stdio and runs
  `runWithViewersConfig`, which accepts viewers on their own goroutines with no
  idle deadline while stdio serves the bridge's connection; when stdio ends or
  the context does, it closes the listener, cancels and waits for the viewers
  (bounded by the host's shutdown), closes the sessions and the lister, and
  joins every error. `cmd/marshal` parses `--viewers` / `--viewers=VALUE`,
  refuses it beside `--listen`, without a value, and for a network other than
  `unix`, and routes the path to `acp.RunWithViewers`.

**Where the plan was short, and how it was filled.**

1. **The refusal sentences did not travel.** Task 2 says both refusals are a
   `jsonRPCError` with the invalid-request code "so the sentence travels";
   `wireError` collapsed every invalid-request message to the fixed string
   `invalid request` (F-SEC-37). Decided with the user, 2026-11-10: pass
   server-authored invalid-request messages through as `serverError` already
   is. `wireError` now returns `invalid request: <message>`,
   `TestWireErrorPreservesInvalidRequest` pins it, and the opaque-code table in
   `server_test.go` lost its invalid-request row. This is the plan's one
   deviation into a shared error path; no code, method or frame shape changed.
2. **Four test files changed with `registerHandlers`.** The two new parameters
   break the call sites in `host_test.go`, `acpclient_methods_test.go` and
   `client_core_test.go`; they pass `h.sink.Notify, false`, which is what they
   meant before.
3. **The watch-resume anchor is in the session package.**
   `SetWatchResume` is at `internal/app/session/watch_resume.go:17`, not
   `internal/acp/watch_resume.go` as the plan's text implies.
4. **Two viewers each see every update, without a transcript write.** A viewer
   cannot add a transcript message — a prompt needs a runner these tests do not
   build — so `TestTwoViewersEachSeeEveryUpdate` pins the same guarantee
   through the status mirror: A sets a notice and both viewers see it, then B
   changes it and A sees the change.
5. **The viewer tests need a `net.Conn`.** `serveViewer` takes one as
   contracted, but the in-memory tests drive an `rpc.Pipe`; they wrap its agent
   end in `pipeNetConn`, a `net.Conn` with no addresses and no deadlines.

**Manual run** — the built `./marshal`, its stdin held open by a FIFO, two
`nc -U` viewers initializing at once, then stdin closed. The capability lists
are elided with `…`; nothing else is cut:

```
--- socket ---
srw-------@ 1 alecpullen  wheel  0 Nov 10 15:50 /private/tmp/p4-3-suite/v.sock
--- A ---
{"jsonrpc":"2.0","id":1,"result":{"agentCapabilities":{…,"loadSession":true,"marshal":{"protocolVersion":1},…},"agentInfo":{"name":"marshal","title":"Marshal","version":""},"authMethods":[],"protocolVersion":1}}
--- B ---
{"jsonrpc":"2.0","id":1,"result":{"agentCapabilities":{…,"loadSession":true,"marshal":{"protocolVersion":1},…},"agentInfo":{"name":"marshal","title":"Marshal","version":""},"authMethods":[],"protocolVersion":1}}
agent exited after stdin closed
agent exit: 0
--- socket after stdio end ---
ls: /private/tmp/p4-3-suite/v.sock: No such file or directory
socket gone
--- agent log ---
time=2026-11-10T15:50:09.910+11:00 level=INFO msg="acp dispatch" method="initialize" id=&[49] duration_ms=0 error=<nil>
time=2026-11-10T15:50:09.910+11:00 level=INFO msg="acp dispatch" method="initialize" id=&[49] duration_ms=0 error=<nil>
```

**Full verification** in the worktree, at `dd8dd151` plus this record: `gofmt
-l internal cmd test web` printed nothing; `go vet ./...` was clean;
`go test ./... -timeout 20m -count=1` ran 126 packages and failed only
`internal/acp`, with three of the plan's known `*Budget` load flakes
(`TestStackBusyFlipBudget` 0.35s, `TestStackPatchApplyBudget` 0.17s,
`TestStackRoundTripBudget` 4.14s). Each re-ran green alone: 0.933s, 0.398s and
1.444s. Every other package passed, `web/bridge` (182.7s) included, and the
baseline's own load flake (`TestACPSendNowCancelsPromptRPCWithoutCancelingTurn`)
did not recur. `TestWebIsStdlibOnly` (`web/bridge`) and
`TestTUIStaysOnTheClientSeam` (`internal/app/tui`) both passed on their own.
`CGO_ENABLED=1 go build -o marshal ./cmd/marshal` succeeded, and
`go test ./test/usability/... -run TestScripted -timeout 5m -count=1` passed
(45.3s, every usability sub-package ok).

**Open for plan 4:** the child's contract is D2's, unchanged:
`marshal acp --viewers unix://<path>`, a socket `0600` in a directory `0700`,
whose viewers serve marshal clients only and share the session's one run slot
with the service. One wire detail plan 4 should know: an invalid-request
refusal now carries its sentence (`invalid request: <message>`), so a relay
that shows an error's message reads the refusal rather than the generic text.

### Plan 4 — project children and the relay

**Status:** done. Branch `p4-4-relay`, based on `59e0611d`, with `main` merged
at `2d26190e` (P4 plan 3) before task 6. One commit per task; tasks 1 and 5 ran
in parallel.

**What was built.**

- *The project child* (`web/bridge/projectchild.go`, `child.go`,
  `transport.go`). One `marshal acp --viewers` process per project root, started
  on first attach, running in that root, with its viewer socket at
  `<stateDir>/v/<first 16 hex digits of sha256(root)>.sock`. `Child` gained
  `Dir`, and the process transport runs the child there. A restarted child
  re-runs `initialize` and then `session/resume` for the sessions the registry
  holds. A failed handshake stops the child, so the next caller does not take
  over a socket the dead process still holds. The child stops 30s after its
  last session closes, or with the fleet.
- *The registry follows the viewers* (`registry.go`). `sessionInfo` gained
  `viewers` and `lastActive`; `Registry` gained `Resume`, `Close`, `rename`,
  `attachViewer`, `detachViewer`, `touch`, `summaries` and `count`. A close
  sends `session/close` and then untracks; a rename keeps the same record,
  directory, busy flag, generation, context and viewer count.
- *The attach handshake* (`attach.go`, `daemoninfo.go`, `http.go`).
  `POST /api/projects/attach` on the socket, with `Connection: Upgrade` and
  `Upgrade: marshal-acp`, takes `{root, sessionId?}`, resolves or starts that
  project's child, resolves the session (new, tracked or resumed), dials the
  child's viewer socket, and answers `101` with `Marshal-Session`. The TCP
  handler has no attach route, so it stays 404 there. `GET /api/daemon` lists
  the children, their sessions, viewer counts and last-active times.
- *The relay* (`relay.go`). Frames cross unchanged, both ways, newline
  included. A watch is decoded only while one is pending: `session/new` with
  `replaces` renames the tracked session on success, and a successful
  `session/close` is forwarded before the bridge closes the runtime through its
  own connection. An oversize frame (over 64MiB) ends the relay, and either
  side ending ends the other.
- *The client's half* (`internal/daemon/client.go`). `ChildInfo`,
  `SessionInfo`, `Info.Children`, `Attachment` and `AttachError`, and
  `Client.Attach`: a raw unix connection carrying `http.Request.Write`, because
  the response is hijacked. The deadline bounds the handshake only and is
  cleared before the stream is handed over, and bytes the reader buffered
  behind the `101` are returned before the connection's own.
- *The end-to-end proof* (`test/daemon`). Four tests drive a real client
  through a real bridge to a real child: a new session with read-your-writes
  (W4), a session that outlives its viewer and is resumed by ID, two viewers
  sharing one session, and a close that untracks it.

**Where the plan was short, and how it was filled.**

1. `Child` had no working directory. It gained `Dir`, and
   `newProcessTransport` took `(bin, dir, args, env)`.
2. The child's environment. It inherits the bridge's (`Child.Env` nil) rather
   than `agentEnv`; that is also what lets `test/daemon` re-exec itself as the
   child.
3. Restart ordering. `OnRestart` is set before `NewRegistry`, so `initialize`
   precedes `session/resume` in the restarted child.
4. A failed handshake's child. Stopping it under `pc.mu` would hold the lock
   across a process wait, so the caller stops it off the lock, before the next
   caller takes the same socket path.
5. Summary order. `summaries` sorts by `LastActive` descending and by session
   ID ascending, so a timestamp tie is stable.
6. Attach refusals. The message is the JSON `error`, else the trimmed body,
   else the status text; the statuses are 400, 404, 405, 500, 501 and 502.
7. The relay's decoding. The plan said the relay watches frames; decoding every
   agent frame would cost every one, so a frame is decoded only while a watch
   is pending, and watches carry compact JSON IDs.
8. Test fixtures. `fakeAgent.Kill()` did not hang up its streams and
   `fakeProjectChild.Signal()` left stdio open, so `Fleet.Close` could wait
   forever for a generation spawned after `Stop`. Both now end their streams.
9. The task 6 harness. The bridge prepends `acp` to `Child.Args`, so `TestMain`
   accepts exactly `acp --viewers unix://<path>`. The socket's directory must
   be `0700`: the smoke run's first attempt was refused for precisely that. The
   detach is asynchronous, so the session-outlives-its-viewer test polls
   `GET /api/daemon`.

**Review response.** A three-way branch review (relay core; registry, client and
the end-to-end suite; boundaries, docs and plan conformance) returned no
blockers. These findings were acted on in this pass:

- A child whose linger an attach had cancelled was never reaped when that
  attach then failed — at `session/new`, at the resume, at the dial, at the
  hijack or at the `101` write. Every post-resolve failure path now calls
  `rearmLinger`, which arms only when the registry is empty, and a session
  opened for an attach whose `101` write fails is closed again.
- `Registry.track` replaced an existing record, so two attaches resolving one
  stored id lost the first's viewer count and leaked its cancel context. It
  now updates the live record in place.
- `armLinger`'s fire deleted the entry before stopping the child, and a
  resolve could start a replacement against a viewer socket the dying process
  still held. The fire stops before it forgets, and `stopChild` keeps a marker
  open until the process is gone; `startProjectChild` waits on it.
- A resolve in flight across `Fleet.Close` could start a child that nothing
  would ever reap. `startProjectChild` now checks that the entry is still the
  fleet's live one, and that the fleet has not closed, and stops the child
  otherwise.
- A relayed watch whose answer never came kept every later agent frame on the
  decode path for the life of the attach. The relay clears its watches when
  both pumps have returned.
- A malformed `initialize` answer was reported as a child with no version; it
  now fails the handshake like any other unusable child.
- `Client.Attach` ignored cancellation that carried no deadline. It now closes
  the handshake's connection and reports the caller's own cancellation.
- The relay's frame cap was a package variable the oversize test wrote while a
  neighbouring test's pumps read it, which `go test -race` caught. The cap is
  now the fleet's (`relayFrameCap`, zero meaning `maxFrameBytes`) and travels
  to the relay once, so no pump reads shared state per frame. The linger's
  duration had the same shape and the same defect — a harness cleanup restoring
  `projectLinger` while a relay's `finish` read it — and is now the fleet's
  `lingerDur` for the same reason.
- `unixServer` bound its socket under `t.TempDir()` beneath a comment citing
  the `sun_path` cap; both fixtures now share `shortSocket`, which falls back
  to `/tmp` as well as macOS's `/private/tmp`.
- `TestPeerCheckedRefusesAnotherUser` read a bare `bytes.Buffer` while the
  listener's `Accept` goroutine wrote its log line — pre-existing, and found by
  the same `-race` run. It uses the package's `syncBuffer` now.
- `AGENTS.md` and `docs/ARCHITECTURE.md` said each project "runs" its child,
  which reads as always-on. Both now say it starts on the first attach and
  stops 30s after its last session.

Two findings were left open deliberately:

- The fire's ordering is improved but not serialised completely: closing it
  fully would mean holding `pc.mu` across `Child.Stop`, which can deadlock
  against a restart's handshake on the child's supervision goroutine. A
  resolve already holding the entry can therefore still race a reap and fail
  that one attach; the next attach builds a fresh child.
- The end-to-end suite has no case for the untracked-session resume path: the
  bridge untracks only on close, so the harness cannot reach that state. The
  `web/bridge` unit tests cover it.

**The smoke run** (built binaries, `--profile local --addr off`, one project):

```
=== attach handshake
HTTP/1.1 101 Switching Protocols
Connection: Upgrade
Upgrade: marshal-acp
Marshal-Session: sess_1794320219740098000

=== child processes while the daemon runs
       1
=== GET /api/daemon
{"version":"dev","profile":"local","pid":89320,"startedAt":1794320219533,
 "socket":"…/smoke/daemon.sock","projects":["…/smoke/project"],
 "children":[{"root":"…/smoke/project","version":"","sessions":[
   {"id":"sess_1794320219740098000","viewers":0,"lastActive":1794320222641}]}]}
=== SIGTERM
=== child processes after
       0
```

The bridge logged `attach attached … reason=requested`, then
`attach detached … reason="client: closed"` when curl's `--max-time` ended the
stream, then `shutting down`.

**Verification** (in the worktree, `TMPDIR=/private/tmp/p4-4-suite`):

- `go test ./test/daemon/ -count=1` — 4 tests, `ok 2.318s`, with
  `MARSHAL_DAEMON_TEST_MARSHAL` pointing at the built `marshal` (`1.649s` with
  the test binary as the child).
- `go test ./web/bridge/ -count=1` — `ok 101.527s`.
- `go test ./internal/daemon/ -count=1` — `ok 1.002s`.
- `go test ./web/bridge/ -run TestWebIsStdlibOnly -count=1` — `ok 0.170s`.
- `gofmt -l internal cmd test web` — no output.
- `go vet ./...` — clean.
- `GOOS=linux go vet ./web/... ./internal/daemon/ ./cmd/webbridge/` — clean.
- `go test ./... -timeout 20m` — one failing package, `internal/acp`, with the
  two known load flakes `TestStackPatchApplyBudget` and
  `TestStackRoundTripBudget`; alone, `go test ./internal/acp/ -count=1` is
  `ok 24.043s`. Everything else passed, `web/bridge` (101.5s) and
  `test/daemon` included.

After the review-response pass the same suites were run again:
`go test ./web/bridge/ ./internal/daemon/ ./test/daemon/ -count=1` is
`ok 90.651s`, `ok 1.342s` and `ok 1.117s`, and
`go test -race ./web/bridge/ -skip TestRecipeRunMaxUSDSetsAPausingCapForTheRun
-count=1` is `ok 105.329s` with no data race. That test is the intermittent hang
plan 2 recorded; a `-race` run that included it hung there, and the goroutine
dump landed in `blockPrompt` → `fakeAgent.reply` with no frame of this branch's
code anywhere in it.

**Open for plan 5.**

- The bridge's connection cannot read a session's `run`: it declares no
  capability, so the run methods are refused on its own stdio connection. Plan
  5's idle rule needs that read.
- `GET /api/daemon` is where `marshal attach` with no ID finds the most
  recently active session. It reports `lastActive` per session and no
  cross-project ordering.
- The linger is a fixed 30s. Nothing configures it, and only a new attach or
  the fleet closing cancels it.
- An attach naming a session the child no longer holds answers 404, so the
  client must ask for a new session rather than resume one.
- The relay has no flow control beyond the 64MiB frame cap: a viewer that reads
  slowly blocks only its own pump.

### Plan 5 — the TUI's daemon mode

Built and verified on branch `p4-5-daemon-mode`, from `c680de4e`; one commit per
task. [The plan](plans/2026-11-11-p4-5-daemon-mode-plan.md) holds the design and
the task list; this section records what it produced, where it was short, and
what was measured.

**What was built.**

- `launch` gained the daemon mode (`WithDaemon`, `Attach`, `chooseDaemon`,
  `runDaemon`, a `daemonClient` seam, and `ProgramResult.Detached`). A plain
  launch offers the socket to `Run`, which probes it once with a 500ms budget
  and takes the relay only when the profile is `local` and
  `trust.Evaluate(...)` needs no question; everything else stays embedded, as
  `--embedded` always does.
- The TUI detaches (`WithDetachOnQuit`, `Detached()`, `/detach`, and the quit
  key at idle), leaves the session and its run going, and ends with `/exit` as
  before.
- The TUI follows a run it did not start (`syncFollowedRun`, `afterRun`,
  `ignoreRun`), so a run admitted for another viewer, or admitted right after
  its own refusal, drives the same busy state, spinner and clock; a follow-on
  start refused with `CodeBusy` is quiet.
- A new model seeds `watchResumeSeq` from the status, so attaching never
  replays a wake that fired before.
- `marshal attach [id]` and `marshal --embedded` exist; `attach` takes at most
  one ID, refuses `--embedded` and refuses flag-shaped arguments.
- `test/daemon` gained a test-owned OpenAI-compatible provider with a gate per
  scripted turn, and two end-to-end journeys: W1 (a run followed to its outcome
  by a second client) and W5 (a TUI that detaches from a running turn).

**Where the plan was short, and how it was filled.**

- *Order.* The plan runs tasks in order. Task 3 consumes `tui.WithDetachOnQuit`,
  which Task 2 adds, so Task 2 ran first. Tasks 1 and 3 then ran concurrently,
  each in its own worktree (`p4-5-task1`, `p4-5-task3`) because their file sets
  are disjoint, and were cherry-picked back onto the plan branch. The commits
  are still one per task; only their order differs.
- *A signature change ripples.* Task 1's `quietBusy` parameter reaches
  `commands_dispatch.go`'s `/review` caller, which the task's Files list does not
  name. The one-word edit was made there.
- *`flushQueuedSteering`* calls `startSessionRun(..., true, …)` directly rather
  than routing through `startPrompt`, which keeps its signature: the plan names
  only `startSessionRun` and `sessionRunCmd`.
- *Error shapes the plan left open:* a `failed` `lastRun` with no error becomes
  `&acpwire.Error{Code: CodeProviderError, Message: "the run failed"}`, and a
  `gated` one carries `CodeHumanGateRequired` with "the run needs a person's
  answer".
- *Loss bookkeeping.* `daemonConn` mirrors `agentConn`'s `lost`/`markLost`/
  `connectionLost`/`close` rather than reusing it, which is bound to an
  embedding and a runtime.
- *Root and home* are re-resolved inside `runDaemon` through one helper instead
  of being threaded through, keeping the plan's four-argument signature;
  `chooseDaemon`'s returned pair is discarded by `Run`.
- *The version note is best-effort:* a failed `AddNote` is logged to the client
  log rather than aborting the run.
- *`Attach` installs no signal context*, which the plan scopes to `Run`; it uses
  its caller's context.
- *The child config's `base_url` carries the `/v1` suffix.* The client appends
  only the operation's path, so the fixture's first attempt was refused at
  `/chat/completions`.
- *An approval needs a rule.* Act mode with Auto off does not by itself ask for
  a shell command. The W1 test writes the project's confirmation rule for
  exactly `rm -f approval-marker.txt` and seeds the trust record that makes the
  project layer apply, as `test/usability`'s approval journey does.
- *A gated turn the run abandoned* is recorded as a refusal by the fixture, so a
  scripted reply lost to a dying run fails loudly instead of shifting the script.
- *The fixture's fake client* gives each attach its own clock (a pinned clock
  made the second attach derive the first session's ID and trip the
  `agent_sessions.id` constraint) and closes the previous runtime on the next
  attach.
- *A missing home* runs embedded, and `attach` says so, because neither has a
  socket to look for.

**The manual run.** The plan asks for a by-hand TUI run. The interactive keys
were exercised through Task 5's end-to-end journeys (W1, W5) and Task 2's model
tests instead; the real binaries were driven as far as a terminal-less
environment allows:

- `webbridge --profile local --addr off --marshal-bin ./marshal` with a scratch
  `MARSHAL_DATA_DIR` listened on `<data>/daemon/marshal.sock`.
- `marshal --trust` in a project records the trust and then falls through into
  the launch, as it always has; headless, that launch starts the TUI and waits.
  Its daemon session survived the killed process: `GET /api/daemon` over the
  socket listed it with `"viewers": 0`, which is D5's premise observed directly.
- `marshal attach` with that session left found it and started the TUI, which a
  terminal-less run cannot drive.
- `marshal --embedded attach` and `marshal attach --embedded` both →
  `marshal attach: --embedded cannot be combined with attach`; a flag-shaped
  session ID, `marshal attach -abc`, → `marshal attach: unexpected flag
  "-abc"`.
- `marshal attach` with no daemon → names the socket and ends with
  `` `marshal daemon install` starts one ``.

The Ctrl+C-twice, `/detach` and reattach key sequences were therefore verified
through W1/W5 and `detach_test.go`, not by hand.

**Verification** (all on the plan branch, `TMPDIR=/private/tmp/p4-5-suite`):

- Task 2: `go test ./internal/app/tui/ -run 'Detach|QuitKey|ExitStillEnds|ArmedHint'`
  ok (0.7s); `go test ./internal/app/tui/... ./internal/commands/... -count=1`
  all packages ok (`internal/app/tui` 34.2s); `TestTUIStaysOnTheClientSeam` ok.
- Task 1: `go test ./internal/app/tui/ -run 'Follow|Followed|OwnStopping|RefusedStartFollows|QuietWake|OldWake'`
  ok (1.1s); `go test ./internal/app/tui/... -count=1` all packages ok
  (`internal/app/tui` 35.9s).
- Task 3: `go test ./internal/launch/ -run 'Daemon|Embedded|NeverDials|Attach|Detach|Resume|LostConnection|VersionSkew|ClientLog'`
  ok (0.8s); `go test ./internal/launch/ -count=1` ok (1.1s); the merged tree's
  `internal/launch` + `internal/app/tui` + `internal/commands` all ok.
- Task 4: `go test ./cmd/marshal/ -count=1` ok (2.7s);
  `CGO_ENABLED=1 go build ./cmd/marshal` ok.
- Task 5: `go test ./test/daemon/ -count=1` ok (1.4s), and again with
  `MARSHAL_DAEMON_TEST_MARSHAL=$PWD/marshal` ok (2.8s).
- Task 6: `gofmt -l internal cmd test web` prints nothing; `go vet ./...` is
  clean; `go test ./... -timeout 20m` passed 122 packages with no failures and
  exit 0 (the base commit's `internal/acp` load flake did not recur);
  `TestWebIsStdlibOnly` and `TestTUIStaysOnTheClientSeam` pass;
  `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` succeeds and
  `go test ./test/usability/... -run TestScripted -timeout 5m` passes.
- Mutation checks, each applied, observed and restored: `detach` closing the
  session fails `TestTheQuitKeyDetachesAnIdleDaemonSession`; dropping
  `ignoreRun` fails `TestTheTUIsOwnStoppingRunIsNotFollowed`; dropping the
  follow check at the end of `handleAgentFinished` fails
  `TestARefusedStartFollowsTheRunThatHoldsTheSlot`; dropping `chooseDaemon`'s
  trust condition fails `TestRunStaysEmbeddedForAProjectThatNeedsTrust`; making
  detaching runs derive from the call again fails both `test/daemon` journeys.

**Review fixes.** A post-merge branch review of `c680de4e..HEAD` found
nothing blocking and two majors; both are fixed on the same branch, with the
three minors and one nit beside them.

- *`marshal attach` never consulted the trust store.* The plan told `Attach` to
  skip the trust condition (Task 3, "It skips the trust condition"), and it did
  — but attach runs no embedded startup, so it cannot ask the question either.
  An untrusted project, or one whose config hash changed since it was trusted,
  was served by the daemon with no question asked anywhere. `Attach` now
  refuses such a project with a message naming the root and saying `marshal`
  there answers the question, then `marshal attach` reattaches. This is a
  deliberate deviation from the plan, recorded here rather than in the plan
  file. `daemonTrustNeedsAsking` is now the one predicate both paths consult,
  so the launch path and the attach path cannot drift apart;
  `TestAttachRefusesAProjectThatNeedsTrust` pins the refusal and that the
  daemon is never asked for an attach.
- *A failure after the probe answered named neither the socket nor a way
  forward.* A refused attach, a failed dial and a handshake timeout printed
  bare errors such as "launch: attach \"abc\" on the daemon: …", while the
  probe's own error names the socket and `marshal daemon install`. Every
  post-probe failure now goes through `daemonServingError`: it names the
  daemon (its socket when the client was built from one) and both ways
  forward — `marshal attach` retries, `marshal --embedded` runs without it —
  and keeps the underlying error wrapped.
  `TestAServingFailureNamesTheDaemonAndTheWayOut` pins the socket, the status,
  and both commands. The review also asked whether a probe-ok launch should
  fall back to embedded when the daemon then lets it down; it does not, on the
  record's own rule that a daemon which answers owns the session, and a silent
  mode switch would hide a child that cannot start. The message is the fix.
- *`Attach` returned `nil` for an already-cancelled context* — exit 0, no
  output, indistinguishable from success. It returns the context's error and
  probes nothing (`TestAttachReturnsTheContextsError`).
- *A refused start of the model's own stranded a followed run in the TUI.* The
  quiet follow-on path was handled, but a non-quiet refusal — a gate answer,
  say — cleared `busy` while `m.following` still named a run in flight, and
  `syncFollowedRun` re-adopts only when nothing is followed, so the model
  rendered idle under a running session until the run ended. A followed run
  still in the status now restores the busy state and the turn's clock
  (`TestARefusedStartKeepsTheFollowedRunBusy`).
- *`marshal attach` misdescribed its own refusals.* `attach --embedded` said
  `unexpected flag "--embedded"`, and `attach abc --embedded` said `at most one
  session ID`. `--embedded` is refused as a conflict wherever it appears, and a
  flag-shaped ID is the only remaining "unexpected flag"; the table test covers
  all five invocations, including `attach -abc`.

**Verification of the review fixes** (same branch, `TMPDIR=/private/tmp/p4-5-suite`):
`gofmt -l internal cmd test web` prints nothing; `go vet ./...` clean;
`CGO_ENABLED=1 go build ./cmd/marshal ./cmd/webbridge` ok;
`go test ./internal/launch/ ./internal/app/tui/ ./cmd/marshal/ ./test/daemon/ -count=1`
all ok; `go test ./... -timeout 20m` passed with no failures;
`TestWebIsStdlibOnly` and `TestTUIStaysOnTheClientSeam` pass;
`go test ./test/usability/... -run TestScripted -timeout 5m` passes. Mutation
checks: dropping `Attach`'s trust condition fails
`TestAttachRefusesAProjectThatNeedsTrust`; returning `nil` for a cancelled
context fails `TestAttachReturnsTheContextsError`; clearing `busy` in the
refusal branch without the follow restore fails
`TestARefusedStartKeepsTheFollowedRunBusy`.

**Open for plan 7:** the trust condition the two paths now share — `chooseDaemon`
and `Attach`, through `daemonTrustNeedsAsking`. D8 removes it, and daemon mode
then needs no trust question in either direction.


### Plan 6 — the project environment and idle sessions

Run and merged as `p4-6-environment`, eight tasks, one verified commit each,
in a worktree off `main` at `29851a61`. [The plan](plans/2026-11-11-p4-6-environment-plan.md)
holds the task list.

**What was built.**

- `web/bridge/projectenv.go`, beside `projectenv_unix.go` (`linux || darwin`)
  and `projectenv_other.go`: `EnvMarker`, `WriteEnv` and `parseEnv`;
  `ProjectEnv` and its `Resolve`. A resolution is the login shell, then the
  direnv overlay, then the bridge's four location variables, with each step
  bounded at 10s and standard output capped at 4 MiB. Only a clean resolution
  is cached, under a fingerprint of the project's five files and the home's
  nine startup files.
- `webbridge print-env`: the shell runs `exec '<webbridge>' 'print-env'`, so
  whatever a startup file prints first is skipped by the last marker, and the
  format lives only in `web/bridge`.
- The fleet: `SetProjectEnv`, `buildProjectChild(ctx, root, socket) (*Child,
  string, error)` — the resolver applies to whatever was built, a test's fake
  included — `projectChild.envWarning`, and `DaemonChild.EnvWarning` on
  `GET /api/daemon`.
- The idle sweep (`web/bridge/idle.go`): `SetIdleSessionTimeout`,
  `RunIdleSweep` and `sweepIdleSessions`, with `Registry.idleCandidates` and
  `Registry.closeIfIdle` rechecking viewers and last activity under the
  registry's own lock.
- `[daemon] idle_session_timeout` in the user config, default `30m`, `0` never.
- `webbridge --idle-session-timeout` (`WEBBRIDGE_IDLE_SESSION_TIMEOUT`),
  refused under the server profile and refused when negative under the local
  one.
- `marshal daemon install` writes `SHELL` and the timeout into the unit;
  `marshal daemon status` prints a line per child with its version and session
  count, and the environment warning on a second, indented line.
- `cmd/webbridge/README.md` gains "The local profile", stating D1, D7 and D7a
  only.

**Where the plan was short, and how it was filled.**

1. *The platform split.* The plan named a file per platform for `runEnvStep`,
   but the capture, the cap and the error classification are identical on
   every system. One shared `runEnvStep` calls a per-platform
   `configureEnvStep` that carries only decision 2's settings (own process
   group plus a group `SIGKILL`, or `WaitDelay` alone).
2. *Classifying a step's failure.* `runEnvStep` reports a step timeout only
   when that step's own deadline passed; any other kill falls through to the
   exit-status branch, so a parent cancellation is not reported as "timed out".
   A direnv that cannot start has no warning string of its own in the plan and
   lands on "direnv export printed something other than JSON"; no new string was
   invented.
3. *The mutation check's bound.* Dropping only `Setpgid`/`Cancel` while
   keeping `WaitDelay` resolves at timeout+1s — inside the plan's "timeout plus
   two seconds" bound — because `WaitDelay` closes the pipes a second after the
   deadline. The unix block owns the kill and its `WaitDelay` together, so the
   mutation the bound catches is removing that block, and the fixtures' sleeps
   were shortened from 30s to 5s so the mutated run ends quickly.
4. *What a failed overlay keeps.* Step 6 follows step 5, so a login
   environment whose direnv overlay failed still gets the bridge's location
   variables; only a login-shell failure returns `Base` untouched.
5. *`Registry.Close`'s body.* The plan said `closeIfIdle` "calls `Close`'s
   body". The body became `closeTracked`, so `Close` and `closeIfIdle` share one
   implementation and nothing is duplicated.
6. *Task 3's Verify regex.* It names no `Unreadable`, so it never runs
   `TestAnUnreadableStatusKeepsTheSession`; that test was run alone (`ok
   0.414s`). The plan's regex, not the test, was short.
7. *The negative timeout's message.* `[daemon]` reuses `web.fetch_timeout`'s
   `parse <key>: %w` shape, so a negative value fails as `parse
   daemon.idle_session_timeout: must not be negative`. The save trigger is the
   plain equality comparison `web` and `desktop` use, since the section is one
   comparable duration. No merge- or save-completeness test walks the new
   section, so none needed extending.
8. *The flag's environment default.* `envOr` is string-only, so
   `WEBBRIDGE_IDLE_SESSION_TIMEOUT` is parsed once before `fs.Duration` is
   declared: a variable that does not parse fails `parseConfig` rather than
   silently falling back. The negative refusal lives in `applyProfile`'s local
   refusals and names the profile, as `TestTheLocalProfileRefuses` requires.
   `isolateWebbridgeEnv` gained the variable so a host's setting cannot leak
   into a parse under test.
9. *The unit's test values.* The renderer tests carry `45m` (rendered `45m0s`)
   so the pair is visible as a non-default. The install test sets `SHELL` and
   clears it in the neighbouring subtest, so the result does not depend on the
   developer's shell, and it writes its user config under the test's home while
   a project config in a temporary working directory holds another value.
10. *The status line.* `child <root>: version <v>, <n> session(s)`, with real
    pluralisation (`1 session`, `3 sessions`), the aside `(this marshal
    <ours>)` only when the *rendered* versions differ (so `dev` against `dev`
    has none), and `  environment: <warning>` for a non-blank warning. An empty
    child version renders `dev`, as `daemonVersion` does.
11. *The resolver's wiring.* The seam-versus-local build is one unexported
    `projectChildForRoot`, so `buildProjectChild` stays a short read;
    `localProjectEnv` tolerates a failed `EvalSymlinks` exactly as
    `resolvedExecutable` does, and only an `os.Executable` failure leaves
    children inheriting.

**The smoke run** (built `webbridge` and `CGO_ENABLED=1` `marshal`, a scratch
`HOME` whose `.profile` exports `P6_SMOKE`, `SHELL=/bin/sh`, a `git init`ed
project, `--profile local --addr off --idle-session-timeout 1m`). `direnv` is
not installed on this machine, so the overlay was proved by Task 1's fake
`direnv` and the smoke run proved the login-shell half:

```
=== attach handshake
HTTP/1.1 101 Switching Protocols
Connection: Upgrade
Upgrade: marshal-acp
Marshal-Session: sess_1794342868901363000

=== child pid 15524; P6_SMOKE=yes entries in its environment:
1
=== GET /api/daemon at t+2s
{"version":"dev","profile":"local",…,"children":[{"root":"…/proj","version":"",
 "sessions":[{"id":"sess_1794342868901363000","viewers":0,"lastActive":1794342871805}]}]}
=== GET /api/daemon at t+109s
{"version":"dev","profile":"local",…,"children":[{"root":"…/proj","version":"","sessions":[]}]}
=== bridge log
INFO webbridge: resolved project environment root=…/proj variables=8
INFO webbridge: closed idle session root=…/proj session=sess_… reason=idle
children now: ''
```

The child carried the login shell's variable, `GET /api/daemon` reported no
`envWarning`, the sweep closed the session, and the child was gone after the
linger. Two things the plan's smoke step did not say, both found by running it:
the socket's parent directory must be `0700` or the bridge refuses to start at
all (the first attempt died on this), and with a 1m timeout and a 30s tick the
close lands at about t+90s, so "wait just over a minute" reads the daemon too
early. `marshal daemon status` against an installed service was not run: this
machine has none installed.

**Verification** (worktree, `TMPDIR=/private/tmp/p4-6-suite`; the baseline at
the starting commit had every package below passing, so no failure here is
pre-existing):

- Task 1: `go test ./web/bridge/ -run 'WriteEnv|LoginShell|Direnv|TimedOutShell|FailingShell|FailedOverlay|LocationVariables|ChangedEnvrc|FallbackIsNot'`
  — `ok 2.222s`, with `TestATimedOutShellFallsBack` failing as required when
  the process-group kill is removed. `GOOS=linux go vet ./web/...` and
  `GOOS=windows go vet ./web/bridge/` are clean; `TestWebIsStdlibOnly` `ok
  0.176s`.
- Task 2: the three runs — `ok 0.891s`, `./cmd/webbridge` `ok 0.459s`, and
  `./web/bridge/` whole `ok 82.621s` (no hang).
- Task 3: `ok 2.018s`, `-race` `ok 2.804s`, and both mutation checks failed as
  stated (`closed = [s1], want nothing`; `closeIfIdle closed a session a viewer
  attached to after the candidate list`).
- Task 4: `./internal/app/config` `ok 0.421s`.
- Task 5: `./cmd/webbridge` `ok 0.355s`; `go build ./cmd/webbridge` clean.
- Task 6: `./internal/daemon` `ok 1.097s`, `./cmd/marshal` `ok 2.478s`.
- Task 7: `./internal/daemon` `ok 1.371s`, `./cmd/marshal` `ok 3.148s`.
- `gofmt -l internal cmd test web` — no output.
- `go vet ./...` — clean; `GOOS=linux go vet ./web/... ./internal/daemon/
  ./cmd/webbridge/` — clean.
- `go test ./web/bridge/ -run TestWebIsStdlibOnly -count=1` — `ok 0.665s`.
- `go test ./... -timeout 20m` — one failing package, `internal/acp`, with
  `TestStackPatchApplyBudget` (2.073ms against a 2ms budget) and
  `TestStackRoundTripBudget` (88.176ms against 50ms): the load flakes the base
  commit recorded. Alone, `go test ./internal/acp/ -count=1` is `ok 22.753s`.
- `go test -race ./web/bridge/ -skip TestRecipeRunMaxUSDSetsAPausingCapForTheRun
  -count=1` — `ok 90.508s`, no data race.

**Open for plan 7.**

- H9 is unchanged: the attach answer does not carry `envWarning`, so a TUI
  attached to a project whose environment fell back cannot show it. `GET
  /api/daemon` and `marshal daemon status` do. Whoever wires the TUI warning
  must add it to the attach answer rather than re-resolving on the TUI side.
- The sweep is a 30s tick with a 5s status bound, so a close can lag the
  timeout by up to that tick. A future plan that wants tighter idle ends needs
  its own scheduling, not a shorter timeout.
- Only a fleet given a resolver resolves: the local profile does, the server
  profile and the end-to-end harness do not. A plan that needs project
  environments under the server profile must give that fleet one.
- `marshal daemon install` reads `[daemon] idle_session_timeout` with the
  project config skipped, so no project can retune the daemon; nothing reads
  it at runtime from the unit's flag but `webbridge` itself.


### Plan 7 — trust for daemon sessions

Not yet run. [The plan](plans/2026-11-11-p4-7-trust-plan.md) fills this record.

### Plan 8 — OAuth refresh once

Not yet run. [The plan](plans/2026-11-11-p4-8-refresh-plan.md) fills this record.
