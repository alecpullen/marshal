# P6 — the daemon across rebuilds and edits

Parent: [`design.md`](design.md) §6.2 (config and trust) and §6.4 (upgrades
and version skew).
Builds on [`p4-local-daemon.md`](p4-local-daemon.md): its project children,
their crash restart, the idle sweep and the attach relay; and on
[`p5-fleet-screen.md`](p5-fleet-screen.md): its daemon loop between the fleet
and the session screen.
Depends on: P4, implemented (its hands-on acceptance run is still to do); P5,
implemented through plan 7 (merged at `87c41ad9`), with plan 8 in review
(PR #100).
Status: approved, 2026-11-16, with all nine decisions as recommended.
Behaviour written the same day. Plans 1, 2, 3 and 6 are written, with their
anchors checked at `87c41ad9` with plan 8's branch (`d57f178f`) merged in.
All four are implemented on `main`. Plans 4 and 5 are written (2026-11-18),
with their anchors checked at `bdd20ddc`.

## Goal

Plain `marshal` runs its sessions on the local daemon, and the daemon outlives
every rebuild and every config edit. P6 makes it pick them up by itself:

- **A rebuilt `marshal` reaches each project** at that project's next idle
  moment, without ending a running turn.
- **A rebuilt `webbridge` restarts the daemon itself** the same way.
- **A config edit reaches the sessions it affects,** and a changed project
  config is checked against the trust store before it applies.
- **An attached session screen survives all of it.** It reconnects to its
  session, keeping the prompt being typed.
- **The user can see what is pending:** which build each project runs, and
  what a restart is waiting for.

The done-when for P6: rebuild `marshal` and `webbridge` while daemon sessions
are open, one of them mid-turn; each project picks up the new build when it
goes idle, the turn finishes, and every attached screen comes back on its
session with its draft.

## Where the code is today

- **The daemon is installed with absolute paths to both binaries.**
  - `marshal daemon install` writes a unit that runs the `webbridge` path with
    `--marshal-bin` set to the `marshal` path (`internal/daemon/daemon.go:46`).
  - A rebuild at those paths reaches a project child only when the child next
    starts: after its 30s linger, or after a crash. A pinned project's child,
    or one with an open session, keeps the old build.
  - Nothing restarts the daemon. `marshal daemon` has `install`, `start`,
    `stop`, `status`, `uninstall`, `pin`, `unpin` and `refresh`
    (`cmd/marshal/daemon.go:78`), and `stop` ends every running turn.
- **The skew warning never fires for a source build.**
  - An unstamped build reports its version as `dev`, in both binaries
    (`cmd/marshal/daemon.go:561`, `cmd/webbridge/main.go:42`).
  - The bridge's skew check treats `dev` on either side as no version
    (`web/bridge/fleet.go:1040`), and the TUI's note compares the two `dev`
    strings, which match (`internal/launch/daemon.go:348`).
  - `marshal --version` does print the commit, from Go's build info
    (`cmd/marshal/version.go:46`).
- **A child's restart path already exists, for crashes.**
  - When a child's process exits, `supervise` respawns it from the binary on
    disk after 100ms (`web/bridge/child.go:418`, `:35`).
  - A project child then repeats its handshake and its hold
    (`reinitProjectChild`, `web/bridge/projectchild.go:326`). Its registry
    resumes every session it tracks (`web/bridge/registry.go:178`), fleet
    agents' included.
  - A respawn that fails is not retried (`child.go:446`), so a build that does
    not start leaves the project without a working child.
- **The bridge already asks each session whether it is busy.**
  - Every 30s the idle sweep asks each untouched session for its status, on
    the bridge's own connection, and leaves alone a session with a run
    (`web/bridge/idle.go:45`).
  - The registry records each session's last activity from the relay's frames
    (`touch`, `web/bridge/registry.go:442`).
  - A session's status also reports subagents, running jobs, a swarm and an
    SDD run (`internal/acpwire/status.go:33`, `:45` to `:48`).
  - It does not report watches. A watch lives only in its runtime's memory
    (`internal/watch/watch.go:242`), so a restart ends it.
- **A session screen that loses its connection ends.**
  - The loop reports "the daemon's connection closed … `marshal attach <id>`
    reattaches" (`internal/launch/daemon.go:391`), or, once the fleet has run,
    returns to the fleet with that line in Events.
  - The prompt being typed is lost: nothing carries a draft from one session
    screen to the next (`internal/launch/launch.go:26`).
  - A project attach that names a session the child does not hold resumes it
    (`web/bridge/attach.go:130`), so `marshal attach <id>` already comes back
    after a restart.
- **Config is read when a session starts.**
  - The settings panels write the user config through the session's host
    calls, then rebuild the session's runtime from a config they hand it
    (`ReloadConfig`, `internal/acp/service/config.go:316`). That rebuild reads
    neither the files nor the trust store.
  - A config edited by hand, or a project config changed by a `git pull`,
    reaches a daemon session only when the session next starts. Detached
    sessions last until the 30m idle timeout (P4 D7a).
- **A watch's wake is resumed by its viewer.** A marshal client resumes a
  watch itself (`bindWatchResume`, `internal/acp/host.go:316`), and the
  bridge's connection binds nothing on a TUI's session. So a wake that fires
  while no screen is attached waits (P4 H8).
- **Two warnings stop short of the TUI.** An agent's version warning is set on
  the runtime and shown nowhere (`fleet.go:930`, `:1031`; P4 H13). A project's
  environment warning is on `GET /api/daemon` (`contract.DaemonInfo`'s
  `EnvWarning`) and not on the session screen (P4 H9).

## In scope

1. Knowing when a project child is stale: its binary or a config file it read
   has changed since it started.
2. Restarting a stale project child when it is idle, and the daemon when
   `webbridge` is replaced.
3. `marshal daemon restart`.
4. The session screen reconnecting to its session, with its draft.
5. Versions that tell two source builds apart.
6. Showing a pending restart, the build each project runs, an agent's version
   warning and the environment warning.
7. A watch's wake resuming while no screen is attached.

## Decisions

Each gives the decision first, then the alternative it was weighed against.

1. **The child decides when it is stale, from what it started with.**
   - **What it records.** At start, a project child records its own
     executable's file identity (path, device and inode, size and modification
     time), and the content hashes of the user config and the project config.
     A missing file has a hash too, so creating one is a change.
   - **What makes it stale.** Any of those changing. A config write the child
     made itself, through a settings panel's host call, updates its record
     instead: the session that wrote it already applies it.
   - **Who asks.** The bridge, in its 30s sweep, with a new request on its own
     connection to each project child: `project/state`. The answer carries the
     stale reasons, whether any of its sessions is busy (decision 2), and its
     version.
   - **Why the child.** It alone knows which config writes were its own. A
     watcher in the bridge would restart a project after every settings
     change its own session made.
   - **Alternatives:**
     - Compare version strings. A source build is `dev` on both sides, so
       nothing would ever differ (Findings).
     - Watch the files from the bridge, which cannot tell the child's own
       writes from anyone else's.
2. **A stale child restarts when it is idle, and only then.**
   - **Idle** is two things. No session of the child is busy: none has a run,
     subagents, running jobs, a swarm, an SDD run or an active watch. And no
     frame has crossed any of its relays for 2 minutes, or it has no viewer.
     The quiet period keeps a restart from landing while someone reads a
     reply.
   - **The restart is P4's crash path,** started on purpose. The bridge stops
     the child's process gracefully. `supervise` respawns it from the binary on
     disk, the handshake and any pin's hold are repeated, and the registry
     resumes every session it tracks.
   - **A build that cannot start is never installed.** Before stopping the old
     process, the bridge runs the new binary's `--version` with a 10s limit. A
     binary that fails it leaves the child running the old build, and the
     failure is reported (decision 6). The bridge tries again only when the
     file changes again.
   - **No turn is refused while a restart waits.** A child that never goes
     idle never restarts by itself, and its screens say what it waits for.
   - **Alternatives:**
     - Design §6.4's drain: refuse new turns, wait for running ones up to a
       limit, then restart. Refusing a prompt the user has just typed is worse
       than waiting for a gap, and a limit that ends a running turn loses
       work. `marshal daemon restart --now` (decision 4) keeps the forced
       restart for when the user wants it.
     - Restart at once. That ends running turns and drops watches.
3. **A replaced `webbridge` restarts the daemon in place.**
   - The bridge records its own executable's identity at start, and checks it
     in the same sweep.
   - When it has changed and every child is idle by decision 2's rule, the
     bridge checks the new binary's `--version`. Then it stops its children,
     closes its listeners and re-executes its own binary with the same
     arguments.
   - Sessions resume on their next attach, and fleet agents through
     `ReattachAll`, as on any start.
   - **Alternative:** exit non-zero and let launchd or systemd start it again.
     That depends on how the daemon was started: a daemon run by hand would
     just stop. And launchd throttles a restart by 10s.
4. **`marshal daemon restart` asks for the same restart, now.**
   - It asks the daemon, over a new socket-only route, to restart every child
     and itself as soon as each is idle, whether or not anything changed. It
     prints what it is waiting for until it is done. Ctrl-C withdraws the
     request.
   - `--now` restarts at once and ends running turns, as `stop` then `start`
     would, but with the screens reconnecting (decision 5).
   - With no daemon answering, it starts one, as `start` does.
   - **Alternative:** `stop` then `start` through the service manager. That
     ends running turns, and gives the screens no word of what happened.
5. **The session screen reconnects to its session.**
   - When its connection closes, other than by quit, `/detach` or `/fleet`,
     the loop dials the same session again. It retries for up to 60s, waiting
     1s and doubling to 5s.
   - Meanwhile a small program of its own says "Reconnecting to <session>…",
     so the terminal stays on the alternate screen rather than dropping to the
     shell.
   - The new session screen opens on the resumed session's transcript, with
     the draft the old one had. The session screen hands its draft back to the
     loop when it ends, and the loop keeps one draft per session, so a return
     from the fleet opens with it too.
   - After 60s, the loop ends as it does today, or returns to the fleet with
     the line in Events.
   - When the bridge closes viewers for a restart, the relay first sends them
     a `marshal/restarting` notification with the reason. The screen then says
     "restarting on a new marshal build" rather than "connection lost".
   - **Alternative:** keep the program running and swap its host and session
     live. P5's decision 4 rejected that: nothing in the model supports it.
6. **What is pending is on screen.**
   - **A pending restart.** A stale child's sessions carry the reason in their
     status, and the session screen's status line shows `restart pending`.
     The sessions sheet says what it waits for: a run, a watch, or the quiet
     period.
   - **A finished restart.** Each resumed session gets a note in its
     transcript: "Restarted on marshal <version>: <reason>." A build that
     failed decision 2's check gets a note saying so, with the first line of
     its error.
   - **`marshal daemon status`** lists each child's version and any pending
     restart with its reason.
   - **The two P4 warnings.** The fleet's Info tab shows an agent's version
     warning (H13). The session screen shows the project's environment
     warning as a note when it attaches (H9).
   - **Alternative:** a banner notice for the pending restart. A session shows
     one banner at a time, so it would hide a provider's notice.
7. **A source build's version carries its commit.**
   - An unstamped build reports `dev+<12 hex of vcs.revision>`, with
     `+dirty` when the tree was modified, from Go's build info. Both binaries
     do, wherever a version is shown or compared.
   - So `marshal daemon status` and the version note tell two source builds
     apart. Skew still only warns (P4 D11), and decision 1, not the version,
     decides a restart: two builds of one dirty tree share a version.
   - **Alternative:** keep `dev`, and nothing on screen says which build the
     daemon runs.
8. **A config edit applies by restart, never by a live reload.**
   - Decision 1 marks the child stale, and decision 2 restarts it. Its
     sessions resume with the files as they are now, through the path every
     session start takes.
   - A changed project config is checked against the trust store on that
     resume. An attached screen is asked in place (P4 D8), and nothing from
     the new file applies until it is trusted. So a `git pull` that changes
     `.marshal/config.toml` never runs untrusted config.
   - **Alternative:** rebuild each session's runtime between turns, as the
     settings panels do. That rebuild takes a config the caller hands it, and
     reads neither the files nor the trust store
     (`internal/acp/service/config.go:316`), so it would need a second,
     trust-checking load path beside session start.
9. **A wake with no screen attached resumes by itself.**
   - When no marshal viewer is attached to a session, its watch-resume hook
     is bound to the session's service, so the wake starts its turn as a
     detached run.
   - The next screen to attach sees the run in progress. When a viewer
     attaches, the binding moves back to it.
   - **Alternative:** leave the wake for the next attach to take. Its report
     then waits, perhaps for hours, though the watch was set to act on it.

## Out of scope (follow-ups)

- **K1.** Containers and the server profile. An image is rebuilt and pulled
  by its operator, and a server's bridge is restarted by its own service
  manager.
- **K2.** A live config reload without a restart (decision 8).
- **K3.** Rolling back to the previous build. A build that fails its check is
  never installed, but one that starts and then misbehaves stays.
- **K4.** The TUI client's own binary. The user restarts `marshal` itself;
  the version note says when it differs from the daemon's.
- **K5.** P4's other open follow-ups: H3 (the web UI over the relay), H7
  (worktree indexes), H10 (a project's plugins after a trust in place), H12
  (pinning from the web UI) and H14 (a child's event log growing), and P5's
  H1 to H5.
- **K6.** Design §8's MCP scope question.

## Behaviour

### B1. Versions (plan 1)

- **One rule, in a new package `internal/buildinfo`.** A binary's version is
  the release version stamped into it when there is one. Otherwise it is
  `dev+<the first 12 hex of vcs.revision>`, with `+dirty` added when
  `vcs.modified` is true, read from Go's build info. A build with no VCS
  information is plain `dev`.
- **`marshal` uses it everywhere it names itself:** the `--version` banner's
  version word, the provider User-Agent, the agent's `agentInfo.version`, and
  the TUI's own version in every mode. Today `acp.AgentVersion` is set only by
  `marshal acp` (`cmd/marshal/main.go:105`), so a plain `marshal`'s TUI reports
  an empty version (Findings).
- **`webbridge` uses it** for `GET /api/daemon`'s version and the fleet's
  build version.
- **The comparisons keep their rules.** The bridge's skew check still treats
  `dev` alone as no version; `dev+…` is a version, so two builds from
  different commits warn. The TUI's note compares the two values as before.
  Embedded mode pins the agent's version to the TUI's, and both are now the
  same value.

### B2. The reconnect and the drafts (plan 2)

- **When the loop reconnects.** The session screen's program ended because
  its connection closed, and not by quit, `/detach`, `/fleet` or a resume.
  That is today's `connectionLost` (`internal/launch/daemon.go:385`).
- **The waiting program** is a small model of its own, in a new package
  `internal/app/tui/reconnect`. It reads "Reconnecting to <label>…", with the
  attempt count and the time left. Once a `marshal/restarting` notification
  has arrived on the lost connection, it reads "<label> is restarting:
  <reason>" instead. `esc` and `q` give up at once; ctrl+c quits the program
  as everywhere else.
- **The attempts.** Launch dials the same step as before: the agent attach for
  an agent, the project attach with the session's ID otherwise. It waits 1s,
  then doubles to at most 5s, until 60s have passed since the loss.
  - A refusal with a 4xx status (an unknown agent, a paused one, a fleet
    session) ends the attempts at once, since retrying cannot change it.
  - A dial failure, a 5xx answer, or a session the child cannot attach yet is
    retried.
- **When it succeeds** the waiting program ends, and the session screen runs
  on the new connection, exactly as a fresh attach does.
- **When it gives up** the loop ends as today: the error saying `marshal
  attach <id>` reattaches, or, once the fleet has run, the fleet with that line
  in Events.
- **`marshal/restarting`** is a notification the bridge sends a viewer before
  it closes the viewer's connection for a restart (B4). Its params are
  `reason` and `version`. Launch records the last one on the connection
  through the client's notification hook, which every caller already has.
  This plan defines it; plan 4 sends it.
- **Drafts.** The session screen's draft is its composer's text and its paste
  attachments.
  - The screen hands it back when it ends, in the program's result, whatever
    the reason.
  - The loop keeps one draft per session ID, and passes it to the next screen
    on that session: after a reconnect, on a return from the fleet, or on a
    resume. An empty draft clears the entry.
  - The drafts live as long as the loop: quitting the program drops them.
- **A note the loop adds is added once.** The version note today is added on
  every attach while the versions differ. With reconnects that would add one
  per restart. The loop adds a note only when the session's newest message is
  not already that note.

### B3. Staleness in the agent (plan 3)

- **Only a project child keeps a record:** the bridge marks it, with
  `MARSHAL_ACP_PROJECT_CHILD` in the child's environment
  (`web/bridge/projectchild.go`, which carries its own copy of the literal
  because `web/` does not import `internal/...`). `--viewers` alone is not the
  discriminator: the bridge starts a fleet agent's own child with it too, and
  that child is not marked and keeps none. Embedded mode and `--listen` keep
  none.
- **The record** is taken when the child starts:
  - its executable: `os.Executable`'s path, and that path's device, inode,
    size and modification time;
  - the user config: `config.UserConfigPath(home)`;
  - the project config: `config.ProjectConfigPath` of the child's working
    directory, which the bridge sets to the project's root.

  Each config file is kept as the SHA-256 of its content, or as absent.
- **Its own writes.** `service.Deps` gains a `ConfigWritten(path)` callback,
  which every write the agent makes to the user or the project config calls
  after it succeeds. The record re-hashes that file, so the change is the
  child's own. The callers are the settings' host and session writes, the
  "always" permission rule, the skill autoload and gate, and the `config/`
  methods `internal/acp/config.go` serves.
- **The check runs when the bridge asks,** not on a timer of its own, and its
  answer is kept until the next ask. So nothing hashes a file more often than
  the bridge's sweep, every 30s.
- **The reasons.** "a new marshal build" when the executable's identity
  differs; "config.toml changed" or ".marshal/config.toml changed" when a
  file's hash differs from the record. The check compares against the record
  each time, so a file put back as it was is no longer a reason.
- **`project/state`** is a new request on the bridge's own connection, served
  only by a project child, beside `project/hold`. Its answer:
  - `stale`: the reasons, empty when the child is current;
  - `busy`: what holds the child up, one line per item: "a run in <session>",
    "subagents in <session>", "jobs in <session>", "a swarm in <session>", "an
    SDD run in <session>", "watch <name> in <session>". A session is named by
    its title, else its ID;
  - `version`: the child's own version (B1).
- **`project/note`** is a second request: its `text` becomes a plain note in
  every live session of the child. Plan 4 uses it to say a restart is coming,
  or that a new build was refused.
- **The status carries a pending restart.** While the last answer was stale,
  every session's status has `restart`: its first reason and what it waits
  for, which is the first busy line or "a quiet moment".
- **The session screen shows it.** The status line's right cluster reads
  `restart pending` in the warning colour, beside plan 6's needs-you count.
  The sessions sheet's session section has a row: "Restart: <reason>, waiting
  for <what>".

### B4. The restart when idle (plan 4)

- **A sweep of its own,** every 30s on the local profile, whether or not idle
  sessions are being ended. For each project child it sends `project/state`,
  bounded by 5s. A child that does not answer is skipped.
- **Idle and quiet.** A stale child restarts when its `busy` is empty and no
  session it tracks has had a frame through its relays for 2 minutes (the
  registry's last activity). A child with no viewer only needs the first.
- **The new build is checked first.** The bridge runs `<marshal-bin>
  --version` with a 10s limit.
  - If that fails, the bridge sends `project/note` with "A new marshal build
    could not start (<its first line>); this project stays on <version>." It
    sends it once per file identity, and does not restart.
  - If it succeeds, the bridge sends `project/note` with "Restarting on
    <the new version>: <reasons>.", then `marshal/restarting` to every relay of
    the child, then restarts the child.
- **The restart is P4's crash path.** The bridge ends the child's current
  process gracefully, as `Stop` does, without stopping its supervision.
  `supervise` respawns it from the binary on disk, the handshake and any
  pin's hold are repeated (`reinitProjectChild`), and the registry resumes
  every session it tracks, fleet agents' included.
- **Attaches meanwhile** wait for the new generation, as they wait for a
  first start.
- **`marshal daemon status`** gains each child's pending restart and what it
  waits for, from the last sweep.
- **A fleet agent's own child** is not checked. It picks up a new build when
  the daemon restarts (B5).

### B5. The daemon's own restart, and `marshal daemon restart` (plan 5)

- **The bridge's record** is its own executable's identity, taken at start and
  compared in B4's sweep.
- **When it has changed,** and every project child is idle and quiet by B4's
  rule, the bridge runs its new binary's `--version` with a 10s limit. A
  failure is logged and shown by `marshal daemon status`, and the bridge keeps
  running.
- **The restart.** The bridge sends `marshal/restarting` to every relay,
  stops its fleet as a shutdown does, closes its listeners, and re-executes
  the binary at its path with the same arguments and environment. The process
  ID is kept, so launchd and systemd see one long-running daemon.
- **`marshal daemon restart`** posts to a new socket-only route,
  `POST /api/daemon/restart`.
  - The bridge then treats itself as replaced. It waits for every session to
    be idle, though not for the quiet period, since the user asked.
  - The route answers with one JSON line per sweep saying what it waits for,
    and the command prints each. Closing the request, as ctrl+c does,
    withdraws it.
  - `--now` skips the wait: running turns end, as `stop` ends them, and the
    screens reconnect.
  - When the daemon is back, the command prints its version, from
    `GET /api/daemon`.
  - With no daemon answering, it starts one, as `start` does.

### B6. Config edits and trust (plans 3 and 4)

- A config edit is a stale reason (B3), and the restart (B4) resumes each
  session through the path every session start takes: the files as they are
  now, and the project config checked against the trust store.
- A project config whose hash no longer matches its trust resumes untrusted.
  An attached screen is asked in place (P4 D8); a detached one is asked when a
  screen next attaches. Nothing from the new file applies until it is
  trusted.

### B7. A wake with no screen (plan 6)

- **The host counts each session's marshal viewers:** a connection that
  declared the marshal capability and has attached the session (its first
  `session/status` for it). The count drops when that connection ends.
- **The watch-resume hook** of a session on a project child is bound, when the
  session starts, to a resume through the session's service: a prompt with
  the watch's name, as the session screen sends. The hook does nothing while
  the session has a marshal viewer, whose screen resumes the watch itself (P3
  plan 6b), or while the session is busy.
- **A non-marshal connection's binding** (the bridge's, for a fleet agent)
  replaces it as it does today.
- **The next screen to attach** sees the run as a run the session started on
  its own, as it sees a fleet agent's (P5 plan 4c).

### B8. The two P4 warnings (plan 6)

- **An agent's version warning (H13).** `contract.AgentStatus` gains
  `versionWarning`: "agent runs marshal <theirs>; the daemon is <ours>", set
  from the runtime's `versionWarning` and the agent's reported version. The
  fleet's Info tab shows it as its own line, in the warning colour.
- **The environment warning (H9).** Both attaches' `101` answers carry
  `Marshal-Env-Warning` when the project child's environment resolution
  warned: the warning's first line, with control characters removed.
  `daemon.Attachment` gains `EnvWarning`, and the loop adds it as a note, once
  (B2's rule).

## Design

| Area | Change |
|---|---|
| `internal/acp` | The child's record of what it started from, and its own config writes (decision 1); `project/state` (decisions 1, 2); the watch-resume binding with no viewer (decision 9). |
| `internal/acp/service` | Telling the record about a settings panel's config write (decision 1). |
| `internal/acpwire` | The status's pending restart; the `marshal/restarting` notification (decisions 5, 6). |
| `web/bridge` | The sweep's staleness check, the new-binary check and the restart (decision 2); the bridge's own re-execution (decision 3); the restart route (decision 4); the relay's notification (decision 5); the agent's version warning on its status (decision 6). |
| `web/bridge/contract` | The daemon's pending restarts and children's versions; the agent's version warning (decision 6). |
| `internal/launch` | The reconnect loop, its waiting program and the draft (decision 5). |
| `internal/app/tui` | The draft in and out; `restart pending`; the notes (decisions 5, 6). |
| `internal/app/tui/fleet` | The version warning in the Info tab (decision 6). |
| `cmd/marshal`, `cmd/webbridge` | `dev+<revision>` (decision 7); `marshal daemon restart` (decision 4). |
| `internal/daemon` | The restart request and `status`'s pending restarts (decisions 4, 6). |
| `test/daemon` | A rebuilt binary picked up when idle, with a reconnected screen; a busy child left alone; a failed build refused; a config edit and its trust; a wake with no screen. |

## Migration order

Each item is one plan, numbered as here. Plans 1, 2, 3 and 6 need nothing
from each other and run side by side; plans 4 and 5 are the second batch.

1. **Versions for source builds** (decision 7; B1). Small, and needs nothing.
2. **The reconnect and the drafts** (decision 5; B2), with the
   `marshal/restarting` notification read but not yet sent. Useful alone:
   today's `marshal daemon stop` and `start` end every screen.
3. **Staleness in the agent** (decision 1; B3): the record, its own writes,
   `project/state`, `project/note`, and the status's pending restart with
   what the screen shows of it.
4. **The restart when idle** (decisions 2, 6 and 8; B4, B6): the sweep, the
   new-binary check, the notes, the notification, the restart, and `marshal
   daemon status`. It needs plans 2 and 3 on `main`.
5. **The daemon's own restart and `marshal daemon restart`** (decisions 3
   and 4; B5). It needs plan 4.
6. **A wake with no screen, and the two P4 warnings** (decisions 9 and 6's
   last bullet; B7, B8). It needs nothing.

## Tests

- **L1.** Against a real bridge: replace the `marshal` binary under an idle
  project child with an attached session screen. The child restarts on the
  new build, and the screen reconnects to its session with its draft and its
  transcript.
- **L2.** A child with a running turn, or an active watch, is not restarted
  until the turn ends or the watch stops, and its screen shows `restart
  pending`.
- **L3.** A replacement binary that fails `--version` leaves the child on the
  old build, and the sessions get the failure's note.
- **L4.** A config write through a settings panel does not mark its own child
  stale. The same write marks another project's child stale.
- **L5.** A project config changed on disk resumes its session untrusted, and
  the attached screen is asked in place.
- **L6.** A replaced `webbridge` re-executes once every child is idle, and its
  fleet agents and screens come back.
- **L7.** A watch's wake while no screen is attached starts a run, and the
  next attach sees it.
- **L8.** Two source builds of different commits report different versions,
  and a modified tree reports `+dirty`.

## Done when

- The done-when in Goal holds against the real local daemon.
- L1 to L8 pass.
- A manual run: with the daemon installed from this repository's build, open
  two sessions on two projects, start a long turn in one, rebuild both
  binaries, and watch each project and then the daemon pick up the new build,
  the turn finish, and both screens come back.

## Findings while drafting

- **The skew warning cannot fire for a source build.** Both binaries report
  `dev`, and the checks treat `dev` as no version (Where the code is today).
  The user's daemon, installed from a source build, has never warned. Decision
  7 fixes the display, and decision 1 decides restarts without versions.
- **A failed respawn is never retried** (`web/bridge/child.go:446`). A broken
  build installed by a crash restart today leaves the project without a
  working child. Decision 2's check keeps a restart from reaching that state;
  a crash after a rebuild can still reach it (K3).
- **A restart drops watches.** They live only in the runtime's memory, so
  decision 2 counts an active watch as busy.
- **A config reload re-reads nothing.** `ReloadConfig` rebuilds from the
  config the caller hands it, which is why decision 8 applies edits through
  the session-start path instead.
- **A plain `marshal`'s TUI has no version** (found while writing the
  behaviour). `acp.AgentVersion` is set only in the `marshal acp` branch
  (`cmd/marshal/main.go:105`), and launch reads it as the TUI's own version
  (`internal/launch/daemon.go:121`). So a release build's TUI compares an
  empty version with its daemon's, and the note fires on every attach. B1
  sets it in every mode.
- **The idle sweep stops when the idle timeout is zero**
  (`web/bridge/idle.go:22`), so B4's restart runs a sweep of its own.
- **The draft is lost whenever the screen is rebuilt:** on a reconnect, and
  on every return from the fleet, since P5's loop builds a new session screen
  each time. Decision 5 keeps one draft per session in the loop for both.

## Implementation record

**The batch's baseline** (plans 1, 2, 3 and 6), run at `87c41ad9` with plan
8's branch (`d57f178f`) merged in, on macOS with `TMPDIR=/private/tmp/p6-base`:
`scripts/test.sh full` ran 127 packages ok and failed only
`TestStackPatchApplyBudget` and `TestStackRoundTripBudget` in
`internal/acp`. Both are latency budgets that CI's command skips by name
(`.github/workflows/go-checks.yml:29`); `internal/acp` passes under that
command. No other Go test failed, so no failure is to be re-proved by any
plan of the batch.

### Plan 1 — versions for source builds

**What was built.** One rule, in a new `internal/buildinfo`. `Version` returns
the stamped release version when there is one; otherwise it reads Go's build
info and answers `dev+` and the first 12 hex of `vcs.revision`, with `+dirty`
appended when `vcs.modified` is `true`, and plain `dev` when the build carries
no VCS information at all. A revision shorter than 12 characters is used whole.
`IsSource` names a source build — `dev`, or `dev+` followed by anything — which
is the question both binaries and the bridge ask before treating a version as
something worth pinning an image to. The build info is read through the
package-level `ReadBuildInfo`, which the tests replace: a test binary carries no
VCS settings, so the rule is proven by feeding it settings, not by building.

`marshal` computes `buildinfo.Version(version)` once, at the top of `run`
before anything names the build (decision 2), and that one value serves all four
places it names itself. The `--version` banner's version word is it, with the
commit and date after it unchanged. The provider User-Agent is always
`marshal/<version>`; before, an unstamped build kept the package's `marshal/dev`
default. `acp.AgentVersion` is set in every mode, so a plain `marshal`'s TUI has
a version and matches its `marshal acp` — the gap the spec's Findings named. The
`acp` branch no longer stamps the agent version itself, and `daemonVersion` is
the same value.

`webbridge`'s `daemonVersion` is the same rule, so `GET /api/daemon`'s version
and the fleet's build version are the source build's `dev+<commit>`. The
`NewFleet` call passes `daemonVersion()` rather than the raw stamp, which was
empty for a source build and would have left the fleet with no version at all.
`web/bridge` repeats the rule as `bridge.SourceBuild`, because `web/` imports no
`internal` package (decision 3), and `defaultAgentImageFor` gives `:latest` for
any source build rather than for `dev` alone (decision 4) — a source build has
no published tag, and a `+` could not be an image tag in any case. The skew
check is untouched: plain `dev` on either side is still no version, so two
different `dev+…` builds warn and a `dev` on one side never does. The derived
image's tag still hashes the version (decision 5), and `web/bridge/derive.go`
was not touched.

**Where the plan was short, and how that was filled.**

- **The Verify commands' `TMPDIR`.** The plan and the batch's baseline write
  `TMPDIR=/private/tmp/p6-1-suite`, a macOS path; this machine is Linux and has
  no `/private/tmp`, so every run used `/tmp/p6-1-suite`. Nothing in the tests
  depends on the path.
- **The anchors moved, the symbols did not.** The plan was written at
  `87c41ad9` with plan 8's branch merged, and plan 8 (#100) reached `main`
  before this plan ran — the one file both touch, `cmd/marshal/main.go`, is
  exactly where the new `--viewers` code landed. Every symbol the plan names
  still exists and its description still holds; only line numbers moved.
  `daemonVersion` is at `cmd/marshal/daemon.go:563`, as the plan already said.
- **`""` beside `SourceBuild`.** `defaultAgentImageFor` keeps returning
  `:latest` for the empty version as well as for a source build: an unstamped
  `webbridge` with no VCS information has no tag to pin to either, and
  `TestDefaultImageFallsBackToLatestForADevBuild` requires it.
  `SourceBuild("")` is `false`, as `buildinfo.IsSource("")` is.
- **The short revision has no test name of its own.** "A revision shorter than
  12 characters is used whole" is a second subtest inside
  `TestASourceBuildNamesItsCommit` rather than an unlisted test function.

**The tests.** Task 1's seven tests replace `ReadBuildInfo` through one helper
and cover a stamped version winning, a 40-character revision cut to 12, the
short one used whole, `+dirty`, a clean tree (`vcs.modified` stamped `"false"`)
staying clean, no VCS info, no build info, and the six-version `IsSource`
table. Task 2's `TestEveryModeSetsTheAgentVersion` replaces the app,
attach and fleet runners, clears `acp.AgentVersion` before each dispatch so no
mode can pass by inheriting another's value, and restores the runners, `version`,
`acp.AgentVersion` and `provider.UserAgent` in `t.Cleanup`;
`TestTheUserAgentCarriesTheVersion` covers a stamped and an unstamped build;
`TestVersionStringFallsBackToDev` keeps passing because a test binary has no VCS
information. Task 3's `TestTheBridgesSourceRuleMatchesBuildinfo` holds the
`web/bridge` copy to `buildinfo.IsSource` over eight versions and also asserts
the expected answer for each, so a copy that drifts the same way on both sides
cannot pass; `TestDefaultImageTracksTheBuildVersion` gains the
`dev+abc123def456` and `dev+abc123def456+dirty` cases; and
`TestTwoSourceBuildsWarnOfSkew`, beside
`TestVersionMismatchWarnsButDoesNotRefuse`, covers different commits warning,
the same commit staying quiet, and a plain `dev` on either side staying quiet,
each case on a fresh fleet. `TestWebbridgeReportsItsVersion` keeps passing.

**The review pass.** Three reviewers read the branch — code, tests and the
record. Six findings were actioned: `TestACleanTreeIsNotDirty` pins the
`vcs.modified` `"false"` half (without it, a rule that appended `+dirty` for
any non-empty `vcs.modified` would mark every clean checkout dirty and pass);
`TestTheUserAgentCarriesTheVersion` restores `acp.AgentVersion` and asserts
literal expectations, gaining the source-build User-Agent and agent version;
`TestTwoSourceBuildsWarnOfSkew` gained the same-commit cell; `daemonVersion` in
both commands is pinned by `TestDaemonVersionResolvesASourceBuild`, which feeds
`buildinfo.ReadBuildInfo` a revision, so neither can fall back to plain `dev`
unnoticed — that is why the reader is exported rather than package-private.
Three stale comments (`buildVersion`, `versionSkew`, `provider.UserAgent`) and
the slipped `daemonVersion` anchor were corrected. Two findings were not
actioned: `buildinfo.Version(version)` is called at each site that renders it
rather than read from one variable, because it is a pure function of the
linker stamp and build info, and threading a package-level resolved value
through `run` would add test-order dependence for no behavioural difference;
and `defaultAgentImageFor` still builds a tag from any non-source version,
including a hypothetical one containing `+`, which is pre-existing behaviour
this plan deliberately did not widen.

Both mutation checks failed as the plan said they must. Setting
`acp.AgentVersion` in the `acp` branch only made all three
`TestEveryModeSetsTheAgentVersion` subtests fail with an empty agent version;
letting `defaultAgentImageFor` treat only plain `dev` as a source build made
`TestDefaultImageTracksTheBuildVersion` fail on
`defaultAgentImageFor("dev+abc123def456")`. Both were restored, and the
restored tree is what was committed.

**Verification.** `gofmt -l internal cmd test web` prints nothing; `go vet ./...`
is clean; CI's suite command
(`go test -p=1 ./... -skip` the twelve named latency budgets) passes every
package; `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` and the same for
`./cmd/webbridge` both succeed; and `go test ./test/usability/...` passes in
46s. No failure was on the baseline list for this plan, and none appeared.

**The manual run.** Both binaries were built from a standalone clone of the
branch at `2f26dbe9`, so the stamped commit is the branch's own:
`marshal dev+2f26dbe99560 (commit 2f26dbe995603aecc1a6e45f989afdadaf6e0129,
built …)` and `webbridge dev+2f26dbe99560`. With one tracked file reverted in
that clone both report `dev+2f26dbe99560+dirty`. A daemon started from that
`webbridge` — `--profile local`, its socket, state dir and registry under an
isolated `HOME`, `--addr off` — answers
`marshal daemon status` with `socket: … (answering)` and
`daemon: version dev+2f26dbe99560 (this marshal dev+2f26dbe99560), started …,
projects: (none)`, so the two sides agree and L8's "two source builds of
different commits report different versions" is what the `+<commit>` is for.
The socket directory had to be `0700` first: the bridge refuses one that is
open to other users, which is existing behaviour and not this plan's.

**What the manual run turned up.** A build made inside a linked worktree does
not name its own commit. Building in `.marshal/worktrees/p6-1-versions`, whose
HEAD was `2f26dbe9`, stamped main's HEAD — `dev+4e748e6ce4df`, whose `vcs.time`
also matched main's commit — because a linked worktree's `.git` is a file
pointing into the main checkout's `.git` and Go's buildvcs resolves the
repository there; dirtying the worktree produced no `+dirty` while main was
clean, so its modified state comes from main too. That is why the evidence
above comes from a standalone clone. It is Go's stamping rather than this
plan's formatting, and it costs nothing the plan promised: a source build still
names a revision, and the same build always names the same one. It does mean
that agents building in `.marshal/worktrees/` under this repository report the
main checkout's commit, which is worth knowing before a future plan decides a
restart by version — decision 1 already decides by file identity instead.

### Plan 2 — the reconnect and the drafts

**What was built,** against B2, in the order the plan's tasks built it:

- **`marshal/restarting`** (`internal/acpwire/restart.go`): `MethodRestarting`
  and `RestartingParams{Reason, Version}`, with `TestRestartingParamsRoundTrip`
  pinning the wire names. Nothing sends it yet: that is plan 4's half.
- **The draft** (`internal/app/tui/draft.go`): `Draft` — the composer's text
  and its paste attachments — `Draft.Empty()`, `Model.Draft()`, and
  `WithDraft`, which seeds the composer when `tui.New` builds it. Tests:
  `TestADraftRoundTrips`, `TestAnEmptyComposerIsAnEmptyDraft` and
  `TestADraftIsACopy`.
- **The waiting screen** (`internal/app/tui/reconnect/`):
  `New(label, reason, deadline, attempts, opts...)` reads "Reconnecting to
  <label>…", or, once a `marshal/restarting` has arrived, "<label> is
  restarting: <reason>", with the attempt count, the last error's first line
  and the seconds left. Both lines wrap to the frame's width, and a multi-line
  reason keeps its first line, so the block stays centred. `esc`, `q` and
  ctrl+c give up (`GaveUp()`); a closed attempts channel ends the wait without
  giving up. Tests:
  `TestTheWaitingScreenNamesTheSession`,
  `TestTheWaitingScreenSaysWhyARestartHappens`, `TestEachAttemptRepaints`,
  `TestSecondsLeftRoundUpAndStopAtZero`, `TestTheScreenEndsWhenTheAttemptsDo`,
  `TestEscGivesUp` and the golden `TestTheWaitingScreenMatchesItsGoldens`.
- **The loop** (`internal/launch/daemon.go`): `sessionStep` keeps the draft of
  every ending, keyed by the session ID and cleared when the composer is
  empty, and a connection that ends on its own — not by quit, `/detach`,
  `/fleet` or a resume — runs `reconnect`. The attempts run on their own
  goroutine while the waiting screen runs, at 1s, 2s, 4s and then 5s, inside a
  60s window; a 4xx refusal ends them at once, and a dial failure, a 5xx and a
  session the child cannot attach yet are retried. A successful attempt rides
  on the step, so the screen after it runs on the new connection rather than
  dialling again, and it is the same session's ID, so the draft comes back
  with it. Giving up builds today's message (`marshal attach <id>`) or, once
  the fleet has run, the fleet with that line in Events. The last
  `marshal/restarting` is recorded on the connection and titles the waiting
  screen when the attempt is retried. Tests:
  `TestALostConnectionReconnectsToTheSameSession`,
  `TestAReconnectedScreenHasTheDraft`, `TestAReturnFromTheFleetHasTheDraft`,
  `TestAnEmptyDraftClearsTheEntry`,
  `TestTheRestartReasonReachesTheWaitingScreen`,
  `TestTheReconnectGivesUpAfterItsWindow`, `TestARefusedReconnectStopsAtOnce`,
  `TestGivingUpStopsTheAttempts` and `TestTheVersionNoteIsAddedOnce`.
- **L1's screen half against a real bridge** (`test/daemon/reconnect_test.go`,
  `TestAScreenComesBackAfterTheChildRestarts`): `launch.Attach` on a real
  harness session types a draft into the screen with key messages, ends the
  project child with SIGTERM — the unexpected exit the bridge respawns after
  100ms — and checks that the screen which comes back is the same session,
  holding that draft and the transcript the restart resumed. The child's
  process ID comes from `MARSHAL_DAEMON_TEST_CHILD_PID`, which `runChild`
  writes for `acp --viewers` only (`test/daemon/main_test.go`): the bridge
  also starts its control agent from this binary with no arguments, and its
  write would name the wrong process. `GET /api/daemon` names the project's
  children but no process IDs for them. The waiting screen is driven there by
  a small command pump, because the attempts channel is unbuffered and only
  the screen reads it.

**Where it differs from B2.** B2 says "the loop adds a note only when the
session's newest message is not already that note". The implementation reads a
bounded window of the session's newest messages (`noteOnceWindow = 32`, in
`internal/launch/daemon.go`) and looks for the note's exact text in it. A
resumed session appends its own startup messages — the autoloaded skill bodies
among them — after the transcript it reloaded, so the note the loop wrote
before a daemon restart is not the newest message when the next attach reads
it. Under the literal newest-message rule the note is rewritten once per
restart, which is the stacking B2 exists to stop; `TestTheVersionNoteIsAddedOnce`
fails with it (checked by dumping the resumed transcript).

**The manual run is still to be done.** It is one person's check at a
terminal with the daemon installed from this build: type a draft in a session
screen, run `marshal daemon stop` and then `marshal daemon start` from another
terminal, and record what the screen shows meanwhile and whether it comes back
with the draft. It was not run for this record — it needs a human at a
terminal with that daemon installed — and nothing here claims it passed. The
rest of this plan is the automated half.

**Verification,** on Linux (where the plan's `TMPDIR=/private/tmp/p6-2-suite`
does not exist, so `/tmp/p6-2-suite` was used throughout):

- `TMPDIR=/tmp/p6-2-suite CGO_ENABLED=1 go test ./test/daemon/ -run Comes
  -count=1 -timeout 15m` — `ok marshal/test/daemon 2.173s`; `-count=2` passed
  again (`4.304s`).
- `gofmt -l internal cmd test web` printed nothing.
- `CGO_ENABLED=1 go vet ./...` was clean.
- CI's suite command (`.github/workflows/go-checks.yml`: `-p=1 ./... -count=1
  -timeout 20m`, the twelve latency budgets skipped by name — the two in the
  batch's baseline among them) passed under `TMPDIR=/tmp/p6-2-suite`, every
  package `ok`, `test/daemon` in 13.575s and `web/bridge` in 141.687s.
- `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` built.
- The scripted usability suite passed with the built binary at the repository
  root (`USABILITY_REQUIRE_BINARY=1 go test ./test/usability/... -run
  TestScripted -timeout 5m`).

### Plan 3 — staleness in the agent

Executed on Linux at `4e748e6c` on branch `p6-3-staleness`, one commit per
task: the wire, the record, the host's requests and its own writes, the
session screen, then this record. The `TMPDIR` the plan names is a macOS path;
every run used `TMPDIR=/tmp/p6-3-suite`.

- **The record is taken by `RunWithViewers` alone,** so embedded mode, a
  `--listen` container and a fleet agent's own child keep none, as B3 says.
  `newAgentHost` hands it to the session manager, whose service builder puts
  `ConfigWritten` and `PendingRestart` on every session's `Deps`. With no
  record both are nil and no connection behaves differently.
- **The executable's entry is compared in the portable style
  `internal/snapshot` already uses, with the size added** — `os.SameFile` and
  the modification time there, plus a size test here — because
  `syscall.Stat_t`'s modification-time field differs between Linux and macOS,
  and this must build on both. That covers the device, inode, size and
  modification time the plan asks for without OS-specific code.
- **The eight writes report after they succeed,** and the five `config/`
  methods do the same through the manager's `wrote`. A write that fails tells
  no one: the record's whole purpose is that the child's own change is not a
  reason, and a failed write changed nothing.
- **`project/state` and `project/note` are registered only when the host keeps
  a record,** and refused on a viewer's connection with `project/hold`'s
  sentence, beside it in the same handler block. `project/note` answers the
  count of sessions its note reached.
- **Sessions are visited in creation order.** `SessionManager.All` now sorts
  by an unexported sequence number assigned when a runtime is published, so
  the first busy line is stable; no exported signature changed.

**Deviations and gaps filled.** Task 4 needed two files its list did not name:
`SessionSection.Render` is a pure function of `sessionsheet.Data`, which had no
restart field, and `Data` is assembled only in `Model.sheetDataFor`. The plan's
intent was unambiguous, so `Data` gained `Restart *acpwire.PendingRestart`, set
from the session status, and the row renders from it — an internal struct field,
no new exported surface. `project/note`'s answer is a bare integer count, since
the plan declares `ProjectNoteParams` and no result type. `service.newSession`
lives in `events.go`, outside the file list, so the two `Deps` fields are copied
by a `withRecord` helper in `service.go` that `New` and `Host.swap` both use
rather than touching the constructor. Task 2's verify regex does not match
`TestAFilePutBackIsCurrent` or `TestAnUnreadableExecutableStillWatchesConfig`;
both were run separately, in the same package. The Contracts block marks
`internal/acpwire/project.go` "(new)", and the file was already there — it
holds `ScaffoldResult` — so the new constants and types went in beside it. The
record's gating is a wire change the plan does not describe: its assumption
takes a project child to be any `marshal acp --viewers`, and no task names
`web/bridge/projectchild.go`, which the plan cites only as an assumption. The
branch marks the child instead: the bridge sets `MARSHAL_ACP_PROJECT_CHILD` on
a project's child only, and `RunWithViewers` reads the mark once, removes it
before anything is spawned, and builds the record only when it was set —
required because the bridge also starts a fleet agent's own child with
`--viewers` (`web/bridge/fleet.go`), so `--viewers` alone is not the
discriminator B3 asks for.

**Mutation checks.** Dropping `SaveUserValue`'s `ConfigWritten` call failed both
`TestEveryConfigWriteReportsItself` (a table cell) and
`TestASettingsWriteLeavesItsChildCurrent` ("stale = [config.toml changed], want
none"), as the plan requires. Making `wrote` a no-op in task 2 failed
`TestAnOwnWriteIsNotStale` in all four of its cells. Both were restored.

**No manual run, and why.** Nothing asks `project/state` until plan 4's sweep,
and the request is served only on the bridge's own connection, so a person has
no way to see it yet — there is nothing to drive by hand. Task 3's tests drive
both requests over a real host instead: `TestASettingsWriteLeavesItsChildCurrent`
takes a settings write through a viewer and then reads `project/state` on the
bridge's connection, and `TestTheStatusCarriesAPendingRestart` reads the restart
out of a viewer's status after a stale answer.

**Verification.** The baseline was clean under CI's suite command — no package
failed at the starting commit, so there is no failure this plan had to
re-prove. `gofmt -l internal cmd
test web` printed nothing; `CGO_ENABLED=1 go vet ./...` was clean; CI's suite
command (`go test -p=1 ./... -count=1 -timeout 20m` with the twelve latency
budgets skipped by name) passed every package; `CGO_ENABLED=1 go build -o
marshal ./cmd/marshal` succeeded; the scripted usability suite passed.

**The review pass.** Three reviewers read the branch on disjoint angles — code
correctness, test quality and isolation, and docs, spec conformance and
constraints. The major finding is the leak the deviations above answer: the
record reached a fleet agent's own `--viewers` child, which B3 forbids. The
minors: `m.seq` was never pruned, retaining a runtime per closed session;
`busy()`'s subagent, jobs, swarm and SDD lines were untested, so dropping them
would have passed; the sessions' creation order was pinned only through the
map `All` walks, probabilistically, and is now pinned by a deterministic
three-session test; the executable fingerprint's device and inode leg was
never exercised alone — every case also changed the size or the mtime — and
is now pinned by an equal-size, restored-mtime case;
`docs/ARCHITECTURE.md`'s ACP row was incomplete, and is extended above. The
nits, also fixed above: the `internal/snapshot` attribution, the baseline
sentence, and the Contracts' phantom "(new)". Three findings were examined and
left as they are: the same-path `want`s
`internal/acp/service/configwritten_test.go` computes, because the helper is
independent of the code under test and the assertion still compares the exact
path; same-struct JSON round-trips, which cannot catch a renamed tag on their
own, because `TestTheStatusCarriesAPendingRestart` pins the raw `"restart"`
key on the wire; and `TestWroteIgnoresOtherPaths`, which is weaker than it
looks, because the exact-match `assertStale` pins that no reason appears.

### Plan 6 — a wake with no screen, and the two P4 warnings

Executed on Linux at `5a7aa4ab` on branch `p6-6-wake-warnings`, one commit per
task, each task in its own git worktree merged into that branch — the box had
unrelated work in flight in other worktrees, so nothing was edited in the main
checkout. The plan's base assumes P5 plan 8 already merged; it had not, so the
line numbers the plan cites had moved, but every anchor it names existed. The
`TMPDIR` the plan names is a macOS path; every run used
`TMPDIR=/tmp/p6-6-suite`.

- **The viewer count is the host's** (decision 1): `marshalViewers` on
  `agentHost`, handed to each connection's `StatusManager`. `ensureForwarder`
  adds one for a session the first time that connection subscribes to its
  status; `StatusManager.Close` and the forwarder's own end both give it back,
  and the release is idempotent so a forwarder that ends because its session
  went does not leave a screen behind. The counter is built on every host;
  only the binding is gated.
- **The detached binding is made where the service is built** (decision 2):
  `RunWithViewers` sets `resumeDetached` in the run config, `newAgentHost`
  gives the session manager a binder, and `servicePair` calls it once per
  session after `service.New`. `bindDetachedResume` returns unless the report
  is resumable, the config gate is on, no SDD gate is pending, no run is in
  flight, and the session has no marshal viewer; its resume is the screen's own
  call — `svc.Prompt` with `WatchResume{Name, Repeat}` — on a goroutine, so the
  watch's goroutine never waits for a turn. A non-marshal connection's later
  `bindWatchResume` replaces it, as it did before.
- **The agent's version is kept beside the warning** (decision 4):
  `agentRuntime.agentVersion`, set from the same handshake that decides
  `versionWarning`, at both of the sites that set it. `Snapshot` builds
  decision 5's sentence from it and `f.buildVersion` — the version the existing
  skew rule already compares against, so no second source of truth.
- **The environment header is one line** (decision 6): `headerLine` takes the
  warning's first line, deletes control characters, cuts at 300 bytes on a rune
  boundary, and trims. Both `101` answers add `Marshal-Env-Warning` only when
  what is left is non-empty. `envWarningOf` reads `pc.envWarning` under
  `pc.mu`; an agent on a child of its own has no project child, and says
  nothing.
- **The screen's note reuses `addNoteOnce`,** immediately after the version
  note, and carries the child's warning unchanged after
  "This project's environment could not be resolved fully: ". The warning
  travels on the dialled connection, so the reconnect path adds it at most
  once — "at most once" as `addNoteOnce` means it: the check reads the
  session's newest messages (`noteOnceWindow`, 32), so a note that has scrolled
  out of that window is added again, exactly as the version note has always
  been.

**The resume exemption — the one production change this plan did not list.**
Task 6's test could not pass as written, and the reason was upstream of this
plan: every watch is created over ACP (`session/watch_start`),
`WatchManagerACP.Start` tags it `Owner: "studio"`, and `watch.Manager.Start`
dropped `resume` for *any* owner ("subagent watches never resume"). So a
`Report` never carried `Resume`, and neither the new detached hook nor the
screen's own resume path could ever fire: no client could start a resuming
watch at all. With the user's approval the drop is now keyed on ownership
rather than on the field being set: `subagentOwned` accepts the
`subagent-<id>` tag `internal/agent/subagent.go` writes, and the studio tag is
documented as provenance. `TestStartStudioWatchKeepsResume` and
`TestAClientStartedWatchKeepsResume` pin the exemption;
`TestStartOwnedWatchDropsResume` moved from a made-up `sa-1` owner to the
production tag format it is about.

**Deviations and gaps filled.**

- **Plan 8 was not on the base.** The plan's anchors were checked with it
  merged; every one of them was still present, so each task re-checked its
  anchors before editing rather than trusting the numbers.
- **Task 5's "after plan 2" gate was already satisfied.** `addNoteOnce` was on
  main at the base commit, so the task ran with Tasks 1 to 4 and no merge of
  main was needed.
- **Task 2's file list missed the recorded wire contract.**
  `web/bridge/testdata/contract/AgentStatus.json`, which
  `TestTheWireIsUnchanged` pins, was regenerated with `-update`; its diff is the
  new field alone. The plan's verify regex does not select that test, so the
  full suite is what caught it.
- **Task 1's `TestAHostWithoutViewersBindsNothing` matches none of the plan's
  `-run` alternatives** (`Wake|ViewerCount|Binding|BindsWatchResume`). It is
  written as specified, and the same stdio-host assertion sits inside
  `TestTheServiceBuilderMakesTheDetachedBinding`, which the filter does select.
- **Task 6's test speaks ACP directly.** Watches in production are started by
  the bridge, and the rpc client has no `session/watch_start` wrapper, so the
  test builds the connection and sends the method itself — on a connection that
  never subscribes to the session's status, which is exactly a session with no
  viewer.

**The branch review.** Three reviewers read the nine commits by area before
the branch was pushed — the detached wake and the resume exemption, both
warnings end to end, and the Info tab with this record and the repo's design
constraints. They raised one major, five minor and three nit findings. All are
fixed here except the two limits recorded below.

- **The session's binding was never restored when a driver connection died
  (major).** A non-marshal connection's `bindWatchResume` replaces the
  session's own binding with one gated on that connection's liveness flag, so
  its death left the hook inert — and on a viewers host nothing put the
  session's binding back. A webbridge re-exec or a relay reset is enough to
  reach it, and every later wake with no screen up would then have done
  nothing for the rest of the runtime's life: the case this plan exists for.
  `serveConnWith` now calls `SessionManager.RebindDetachedResume` once the flag
  drops, which re-installs the session's own binding for every session whose
  service is built. A viewer connection serves through `serveViewer`, which
  binds nothing, so a screen coming and going never disturbs a live driver's
  binding. `TestATeardownRestoresTheDetachedBinding` pins it.
- **A refused wake was silent (minor).** The binder's goroutine discarded
  `svc.Prompt`'s error, so a wake refused for a busy turn, a runner not built
  yet or a closing session looked exactly like one that ran. It now logs one
  warn line with the session, the watch and the error; the report stays in the
  session's queue either way.
- **The status forwarder's map delete was unqualified (minor).** A forwarder
  that ended because its session went could delete a younger one that had
  replaced it, leaving that one uncancellable and its viewer count alive — a
  phantom screen suppressing detached wakes. The delete is now qualified on the
  forwarder itself, the pattern `retireWorkForwarder` already used.
- **A fire report could name a subagent that does not exist (nit).** The
  "(from subagent …)" suffix now takes the same `subagentOwned` test the resume
  rule does, so the `studio` provenance tag does not claim one.
- **Two sentences were looser than the code (nit, minor).** `AGENTS.md` now
  says "a watch a client started", since a subagent's own watch never resumes;
  and "at most once" above now says what `addNoteOnce` checks.
- **The recorded verify commands did not select the exemption's own tests
  (minor).** `TestAClientStartedWatchKeepsResume` and `./internal/watch`'s two
  exemption tests matched none of the plan's `-run` alternatives, so no
  documented command would re-select them after a refactor. This is the command
  that does, and it selects the restore test too:

  ```
  TMPDIR=/tmp/p6-6-suite CGO_ENABLED=1 go test ./internal/acp/ ./internal/watch/ \
    -run 'Wake|ViewerCount|Binding|BindsWatchResume|KeepsResume|DropsResume|SubagentOwner' \
    -count=1
  ```

**Two limits recorded, not fixed.**
- **A wake that fires before a session's service is first built does nothing
  (minor).** Decision 2 binds where the service is built, and that build is
  lazy, so a watch firing in the window before anything reads the session's
  service starts no turn; its report waits in the queue and the next wake
  resumes. Binding at publish time would put service construction on the
  create and resume paths, which decision 2 deliberately kept it off.
- **The wake test's repeat budget is generous, not exact (nit).** The repeat
  watch can fire again between the flag write and `watch_stop`, so five
  identical replies are scripted rather than one; a fourth fire is still a
  refusal the test fails on. Tightening it would mean making the stop window
  deterministic, which the test deliberately does not do.

**Mutation check.** Letting the hook resume even with a marshal viewer failed
`TestAWakeWithAScreenIsTheScreens` ("a wake with a screen on the session
started … want nothing"), as the plan requires. Restored.

**No manual run, and why.** The plan's item 4 needs a person at a terminal, and
the user directed that it be skipped and recorded as not performed. What it
would have shown is covered by `TestADetachedSessionsWakeRuns`, which drives a
real bridge, a real project child and a screen that detaches: the watch fires
while no viewer is on the session, the session runs the wake's turn on the
provider fixture, and an attach afterwards reads the reply in the transcript.
The fleet's Info tab was not driven by hand either; `TestTheInfoTabShowsAVersionWarning`
pins the line, its position and its colour.

**Verification.** The baseline was clean under every task's verify command — no
filtered test failed at the starting commit — so there was no failure this plan
had to re-prove. `gofmt -l internal cmd test web` printed nothing and
`CGO_ENABLED=1 go vet ./...` was clean. CI's suite command
(`go test -p=1 ./... -count=1 -timeout 20m` with the twelve named latency
budgets skipped) passed in full; its first run is what caught the missing wire
recording above. A second run failed `internal/app`'s
`TestWorkCoordinatorAllActiveAndBoundedTerminalDetails` on a loaded machine —
that package passes alone on both this branch and the base commit, and the
third full run was clean, so it is load flake rather than this change.
`CGO_ENABLED=1 go build -o marshal ./cmd/marshal` succeeded, the scripted
usability suite passed against that binary (44s), and `cd web/ui && npm install
&& npm test` passed 1,299 tests in 119 files.

