# P6 plan 5: the daemon restarts itself, and `marshal daemon restart` — implementation plan

Date: 2026-11-18. Status: ready for review.

Base: `main` with [plan 4](2026-11-18-p6-4-restart-when-idle-plan.md) merged.
The anchors outside plan 4's code were checked at `bdd20ddc`. Anything this
plan names from plan 4 is in plan 4's Contracts:
- `SweepRestarts`, `RunRestartSweep` and `sweepMu`;
- `askProjectState` and `projectState`;
- `checkNewBuild` and `buildVersionOf`;
- `announceRestart` and the relay registry;
- `Registry.quietSince`, `restartQuiet` and `waitingForQuiet`;
- `contract.DaemonRestart`.

Re-check those against the merged code before Task 2.

Approved design: [p6-daemon-lifecycle.md](../p6-daemon-lifecycle.md):
decisions 3 and 4; B5; Migration order item 5; Tests L6; the Done-when's
manual run.
Previous plan: [plan 4](2026-11-18-p6-4-restart-when-idle-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **A replaced `webbridge` restarts the daemon in place** at a moment when
  every project child is idle and quiet, and no fleet agent is mid-turn.
- **The restart keeps the process ID.** The bridge tells every viewer, shuts
  down as it does on SIGTERM, and re-executes its own binary with the same
  arguments and environment.
- **`marshal daemon restart` asks for that restart now.** It waits for idle,
  though not for the quiet period. It prints what it waits for, and can be
  withdrawn with ctrl+c.
- **`marshal daemon restart --now` does not wait.** Running turns end, and the
  screens reconnect.
- **`marshal daemon status` shows the daemon's own pending or refused
  restart.**

## Non-goals

- **The server profile and containers** (K1). The sweep of the bridge's own
  binary runs on the local profile only, as plan 4's sweep does.
- **Restarting the service through launchd or systemd.** Decision 3 rejected
  that.
- **A fleet agent's own child picking up a new build earlier.** It picks one up
  here, because the shutdown ends it and `ReattachAll` starts it again from
  disk.

## Assumptions

Each was checked at the base, except plan 4's, which are in its Contracts.

- **`run` in `cmd/webbridge/main.go`** builds the fleet, the factory and the
  servers, then waits on `serveErr` or a signal (`main.go:862`). Its shutdown
  sequence is:
  - `previewSrv.Shutdown`, then `socketSrv.Shutdown`, which removes the socket
    file;
  - `srv.Shutdown`;
  - `shutdownFactory`;
  - `fleet.Close`;
  - the deferred refresh helper's stop.

  `main` (`:485`) calls `run` and exits 1 on an error.
- **`run` starts `fleet.StartPinned` twice** on the local profile
  (`main.go:674` and `:681`). The two blocks are identical, comment included.
  This plan removes the second (Task 3): a re-exec would otherwise repeat the
  double start at every restart.
- **`--version` is answered before flag parsing** with `webbridge <version>`
  (`main.go:36`, `:508`).
- **`fleet.Close` stops every project child and detaches every agent with a
  child of its own** (`web/bridge/fleet.go:2398`). On the next start,
  `ws.MarkAllInterrupted` and `fleet.ReattachAll` bring the agents back
  (`main.go:566`, and `ReattachAll` after it). A project session comes back on
  its next attach, which resumes an untracked session ID
  (`web/bridge/attach.go:130`). A pinned project's child comes back through
  `StartPinned`.
- **Socket-only routes** are dispatched by path in `Server.SocketHandler`
  before the mux (`web/bridge/http.go:188`), as the pin route is
  (`web/bridge/pins.go:15`). The network listener never sees them.
  `internal/daemon` names each one as a constant beside `pinPath`
  (`internal/daemon/client.go:24`).
- **`GET /api/daemon`** is served by `Server.daemon`
  (`web/bridge/daemoninfo.go:34`). It copies the info set at start and fills
  the per-request fields.
- **`Fleet.Snapshot`** reports each agent's `Status` as a
  `contract.AgentState`: `idle`, `running`, `awaiting-approval`,
  `awaiting-question` or `error` (`web/bridge/contract/agents.go:13`).
- **`marshal daemon`'s subcommands** are dispatched in `runDaemon`
  (`cmd/marshal/daemon.go:75`). `start` loads the service and then reports
  readiness (`runDaemonService`, `:366`; `daemonReadyNote`, `:415`;
  `daemonStartTimeout`, `:58`). The socket client seam is `daemonClient`
  (`:30`), faked in tests by `fakeDaemonClient` with `fakeDaemonSocket`
  (`cmd/marshal/daemon_test.go:27`, `:107`).
- **`cmd/webbridge` has no `TestMain`.** Its local-profile tests call `run`
  in-process (`TestTheLocalProfileServesItsSocket`,
  `cmd/webbridge/local_test.go:210`), and use `isolateWebbridgeEnv`,
  `localSocketPath`, `socketClient` and `waitForDaemon`.
- **`syscall.Exec` replaces the process image and keeps its ID** on Linux and
  macOS. Go opens its descriptors close-on-exec, so the old process's
  listeners and files do not leak into the new one once they are closed.

## Decisions this plan makes beyond the spec

1. **The daemon's own restart also waits for fleet agents.** A fleet agent with
   a child of its own is ended by the shutdown, so an unasked restart, and an
   asked one without `--now`, also wait while any agent's status is `running`,
   `awaiting-approval` or `awaiting-question`. The waiting line is "agent
   <name> is running" or "agent <name> is waiting for an answer". B5 names only
   the project children. This keeps decision 2's rule, never ending a running
   turn, for every turn the restart would end.
2. **Re-execution happens in `main`, after `run` returns.** `run` answers a
   value of its own error type carrying the path, so every deferred stop runs
   first. `main` then calls the `execSelf` seam with the path, `os.Args` and
   `os.Environ()`. If the exec itself fails, `main` prints why and exits 1, and
   the service manager starts the daemon again.
3. **The fleet decides and announces; `run` shuts down.** When the fleet
   decides to restart, it sends `marshal/restarting` to every relay and then
   one `SelfRestart` on a buffered channel. `run` selects on that channel
   beside the signal context and runs the signal path's shutdown. The fleet
   fires at most once per process.
4. **A requested restart sweeps every 2 seconds** while its request is open,
   because someone is waiting at a terminal. Each sweep answers one progress
   line. The 30s sweep goes on as before. Both hold `sweepMu`, so the two never
   run at once.
5. **The daemon's own binary is checked even when a restart was asked for**
   and nothing changed. A binary that fails `--version` is never executed. The
   route answers an `error` line, ends, and the daemon keeps running.
6. **`marshal daemon restart` knows the daemon is back when `startedAt`
   changes**, since the process ID does not. It reads `GET /api/daemon` before
   asking, and polls it after the answer ends until `startedAt` is later. That
   wait is bounded by `daemonStartTimeout` plus 30s, because the shutdown can
   take a few seconds.
7. **The route needs a recorded binary.** A bridge that never called
   `SetOwnBinary` answers 501 "this daemon cannot restart itself".
   `cmd/webbridge` calls it on the local profile. Tests that build a bridge
   in-process call it only when they want the route.

## Execution

- Plan 4 must be on `main`. Branch: `git switch -c p6-5-daemon-restart` from
  `main`, or create a worktree on that branch.
- `mkdir -p /tmp/p6-5-suite`, and use `TMPDIR=/tmp/p6-5-suite` for every test
  run.
- Record the baseline (CI's suite command) at the starting commit.
- Run each task's Verify and commit it as `p6-5-daemon-restart: task N — <title>`.
- Stage explicit paths only. Run `gofmt -w` on every Go file a task touches.

## Contracts

### The wire (`web/bridge/contract/daemon.go`, Task 1)

```go
// DaemonInfo gains:
	Restart *DaemonRestart `json:"restart,omitempty"` // the daemon's own; filled per request

// DaemonRestartRequest is POST /api/daemon/restart's body (P6 B5).
type DaemonRestartRequest struct {
	Now bool `json:"now,omitempty"`
}

// DaemonRestartProgress is one line of POST /api/daemon/restart's answer:
// newline-delimited JSON, one line per sweep, ending with Restarting or Error.
type DaemonRestartProgress struct {
	WaitingFor []string `json:"waitingFor,omitempty"`
	Restarting bool     `json:"restarting,omitempty"`
	Version    string   `json:"version,omitempty"` // the build it restarts on
	Error      string   `json:"error,omitempty"`   // why it will not; the answer ends
}
```

### The bridge (`web/bridge/selfrestart.go`, new; Task 2)

```go
// daemonRestartPath is the socket's restart route (P6 B5).
const daemonRestartPath = "/api/daemon/restart"

// restartRequestedInterval is how often a requested restart sweeps (decision 4).
var restartRequestedInterval = 2 * time.Second

// SelfRestart is the fleet's decision to re-execute the daemon (decision 3).
type SelfRestart struct {
	Path    string // the binary to execute
	Version string // what its --version reported
	Reason  string // "a new webbridge build" or "a restart was asked for"
}

// SetOwnBinary records the daemon's own executable for the sweep and the route.
func (f *Fleet) SetOwnBinary(path string) error

// SelfRestarts delivers at most one SelfRestart.
func (f *Fleet) SelfRestarts() <-chan SelfRestart

// daemonHolds lists what keeps the daemon from restarting at now: every
// project child's busy lines, the quiet lines when quiet is true, and the
// running agents (decision 1). The caller holds sweepMu.
func (f *Fleet) daemonHolds(ctx context.Context, now time.Time, quiet bool) []string

// fireSelfRestart checks the own binary, announces and sends; it answers the
// last progress line. The caller holds sweepMu.
func (f *Fleet) fireSelfRestart(ctx context.Context, reason string) DaemonRestartProgress

// announceRestartAll is announceRestart for every tracked relay.
func (f *Fleet) announceRestartAll(reason, version string) int

// requestRestart serves one restart request until it fires, fails or ctx ends.
func (f *Fleet) requestRestart(ctx context.Context, now bool, progress func(DaemonRestartProgress)) error

// ownRestart is the daemon's own pending restart for GET /api/daemon.
func (f *Fleet) ownRestart() *DaemonRestart

// Fleet gains, guarded by sweepMu unless noted:
	ownPath       string
	ownInfo       os.FileInfo // at SetOwnBinary
	ownRefused    os.FileInfo // the replacement whose check failed
	ownRestartMu  sync.Mutex  // guards ownRestartState, which GET /api/daemon reads
	ownRestartState *DaemonRestart
	selfRestart   chan SelfRestart // buffered 1
	selfFired     bool

// The Server's handler:
func (s *Server) restartDaemon(w http.ResponseWriter, r *http.Request)
```

`DaemonRestartRequest` and `DaemonRestartProgress` are aliased in
`web/bridge/daemoninfo.go` beside the other daemon types.

### The binary (`cmd/webbridge`, Task 3)

```go
// errReexec is run's answer when the daemon is to re-execute itself (decision 2).
type errReexec struct{ path string }

func (e *errReexec) Error() string

// The seams a test replaces.
var ownExecutable = os.Executable
var execSelf = syscall.Exec

// testAsBinaryEnv makes the test binary run as webbridge (TestMain).
const testAsBinaryEnv = "WEBBRIDGE_TEST_AS_BINARY"
```

### The client (`internal/daemon`, Task 4)

```go
// DaemonRestartProgress is the contract's; see that declaration.
type RestartProgress = contract.DaemonRestartProgress

// Restart asks the daemon to restart, calling progress for each line. It
// answers nil once a Restarting line has arrived and the answer has ended, and
// an error for an Error line or an answer that ends without either.
func (c *Client) Restart(ctx context.Context, now bool, progress func(RestartProgress)) error
```

`daemonRestartPath = "/api/daemon/restart"` joins the path constants beside
`pinPath`.

## Task 1 — the wire

**Files:** `web/bridge/contract/daemon.go`, `web/bridge/daemoninfo.go`,
`app/lib/src/client/contract.g.dart`,
`web/bridge/testdata/contract/`.

1. The field and the two types, as Contracts declares them, and the aliases.
2. Regenerate the Dart and the recordings with the two commands in plan 4's
   Task 1. Each diff is the new field and types alone.

**Verify:** as plan 4's Task 1.

## Task 2 — the bridge's own restart and its route

**Files:** `web/bridge/selfrestart.go` (new), `web/bridge/selfrestart_test.go`
(new), `web/bridge/restart.go` (one call at the end of `SweepRestarts`),
`web/bridge/http.go` (the route in `SocketHandler`),
`web/bridge/daemoninfo.go` (`out.Restart`), `web/bridge/fleet.go` (the
fields).

1. **`SetOwnBinary`** cleans the path, stats it, and keeps the path and the
   `FileInfo`. A stat error is returned and nothing is recorded. It makes
   `selfRestart` with a buffer of 1.
2. **The unasked restart.** At the end of `SweepRestarts`, still under
   `sweepMu`, when an own binary is recorded and nothing has fired:
   - Stat `ownPath`. When it is the same file (`os.SameFile`, size and
     modification time against `ownInfo`), clear `ownRestartState` and return.
   - When it matches `ownRefused`, keep the refused state and return.
   - Otherwise compute `daemonHolds(ctx, now, true)`, and set
     `ownRestartState` to reasons `["a new webbridge build"]`, the first hold
     as waiting-for, and no refusal.
   - With no holds, call `fireSelfRestart(ctx, "a new webbridge build")`.
3. **`daemonHolds`.** For every ready project child, it asks `project/state`
   fresh (plan 4's `askProjectState`), then collects:
   - its busy lines;
   - when `quiet` is true, `waitingForQuiet` for a child whose registry is not
     `quietSince(now.Add(-restartQuiet))`;
   - for a child that does not answer, "project <root> did not answer";
   - for every agent in `Snapshot()` whose status is running, awaiting approval
     or awaiting a question, decision 1's line, named by the agent's name, else
     its ID.

   The lines come in root order, then agent order, and duplicates are dropped.
4. **`fireSelfRestart`.**
   - Run `checkNewBuild(ctx, ownPath)` with `buildCheckTimeout`.
   - **On failure:**
     - Record `ownRefused` from a fresh stat.
     - Set the state's `Refused` to the first line, by plan 4's decision 9.
     - Log at warn.
     - Answer `Error` "the new webbridge build could not start (<first
       line>)". The daemon keeps running.
   - **On success:**
     - Set `selfFired`.
     - Call `announceRestartAll(reason, version)`.
     - Send `SelfRestart{Path: ownPath, Version: version, Reason: reason}`
       without blocking.
     - Log at info.
     - Answer `Restarting` with the version.

   A second call after `selfFired` answers `Restarting` with the same version
   and sends nothing.
5. **`announceRestartAll`** copies every registry's relay set under `relayMu`
   and writes the same frame plan 4's `announceRestart` writes.
6. **`requestRestart`.**
   - It answers 501's error when no binary is recorded.
   - **With `now`:** under `sweepMu` it calls
     `fireSelfRestart(ctx, "a restart was asked for")` at once, passes its line
     to `progress`, and returns.
   - **Otherwise** it loops:
     - Under `sweepMu`, compute `daemonHolds(ctx, time.Now(), false)`.
     - With holds, pass `{WaitingFor: holds}` to `progress` and wait
       `restartRequestedInterval` or `ctx`.
     - With none, call `fireSelfRestart` with the same reason, pass its line,
       and return.

     When `ctx` ends, it returns `ctx.Err()`, and nothing is left behind: the
     request withdraws by its context alone.
7. **The route.** `SocketHandler` sends `daemonRestartPath` to
   `restartDaemon`, beside the pin route.
   - It answers 405 for anything but POST, and 501 with `{"error": …}` when the
     fleet is nil or has no own binary.
   - Otherwise it decodes `DaemonRestartRequest` (an empty body is
     `{"now": false}`).
   - It sets `Content-Type: application/x-ndjson`, writes 200, and calls
     `requestRestart` with `r.Context()`. Its progress callback encodes each
     line followed by a newline and flushes through `http.Flusher`.
8. **`GET /api/daemon`** sets `out.Restart` from `ownRestart()`, which copies
   the state under `ownRestartMu`.
9. **Tests**, over `newAttachHarness` (its `fleet` and `socket`) with
   `fleet.checkBuild` replaced. The own binary is a file in a temporary
   directory the test owns.
   - `TestAReplacedBridgeRestartsWhenIdle`: after the file is rewritten, a
     `SweepRestarts` with no children delivers one `SelfRestart` with the
     check's version and reason "a new webbridge build".
   - `TestAnUnchangedBridgeDoesNotRestart`.
   - `TestTheBridgeWaitsForABusyChild`: a child whose `project/state` is busy
     holds it. `GET /api/daemon`'s `restart.waitingFor` is that line, and
     nothing fires.
   - `TestTheBridgeWaitsForARunningAgent`: a fleet agent whose live status is
     running holds it, with decision 1's line.
   - `TestARefusedBridgeBuildKeepsRunning`: the check fails. Nothing fires,
     `restart.refused` is set, and a second sweep does not check again.
   - `TestTheViewersHearTheDaemonRestart`: an attached client reads the
     `marshal/restarting` frame with reason "a new webbridge build" before the
     `SelfRestart` is received.
   - `TestTheRestartRouteStreamsUntilIdle`: with a busy child, a POST over the
     socket reads one line naming the busy line. The test then makes the
     child's `project/state` idle, and the next line is `restarting: true`
     with the version, after which the answer ends.
   - `TestTheRestartRouteNowDoesNotWait`: `{"now": true}` with a busy child
     answers `restarting` at once.
   - `TestClosingTheRequestWithdrawsIt`: the client closes during the wait.
     Making the child idle afterwards fires nothing, because the binary did not
     change.
   - `TestTheRestartRouteIsSocketOnly`: `Server.ServeHTTP` answers it 404.
   - `TestTheRestartRouteNeedsAnOwnBinary`: 501.

**Verify:** `TMPDIR=/tmp/p6-5-suite go test ./web/bridge/ -count=1 -timeout 15m`.

**Mutation check:** drop the agents from `daemonHolds`.
`TestTheBridgeWaitsForARunningAgent` must fail. Restore.

## Task 3 — `webbridge` re-executes itself

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/main_test.go` (the
`TestMain`), `cmd/webbridge/restart_test.go` (new).

1. **Recording the binary.** On the local profile, right after
   `api.SetDaemonInfo`, resolve `ownExecutable()` and call
   `fleet.SetOwnBinary`. A failure is logged at warn, and the daemon runs
   without the restart route.
2. **The duplicate start.** Delete the second, identical
   `if cfg.profile == "local" { go fleet.StartPinned(ctx) }` block, with its
   comment (`main.go:677` to `:682`).
3. **Shared shutdown.** Move the signal path's shutdown, from the
   `slog … "shutting down"` line through `fleet.Close()`, into a local closure
   `shutdown()`. The signal path calls it and returns nil.
4. **The restart path.** The final `select` gains
   `case sr := <-fleet.SelfRestarts():`. It logs "restarting on <version>:
   <reason>", calls `stop()` and `shutdown()`, and returns
   `&errReexec{path: sr.Path}`.
5. **`main`.** When `run`'s error is an `*errReexec`, call
   `execSelf(path, os.Args, os.Environ())`. If that returns, print
   `webbridge: re-execute <path>: <err>` to stderr and exit 1. Any other error
   keeps today's handling.
6. **`TestMain`.** When `WEBBRIDGE_TEST_AS_BINARY=1`, it calls `main()` and
   then `os.Exit(0)`, so the test binary is webbridge. Otherwise it is
   `os.Exit(m.Run())`.
7. Tests:
   - `TestARestartReturnsTheReexec` runs in-process.
     - Set `ownExecutable` to a shell script in a short temporary directory
       that prints `webbridge test-new` and exits 0.
     - Run `run` on the local profile with `--addr off` and
       `--oauth-refresh=false`.
     - POST `{"now": true}` to `/api/daemon/restart` over `socketClient`, and
       read the `restarting` line with version `test-new`.
     - `run` returns an `*errReexec` naming the script, within 10s.
     - The socket file is gone.
   - `TestTheDaemonReexecutesInPlace` (L6) runs a subprocess.
     - Copy this test binary into a short temporary directory.
     - Start the copy with `WEBBRIDGE_TEST_AS_BINARY=1`, `isolateWebbridgeEnv`'s
       variables cleared, `MARSHAL_DATA_DIR` set to a temporary directory, and
       the arguments `--profile local --addr off --socket <s> --state-dir <d>
       --workspace <w> --marshal-bin /usr/bin/false --oauth-refresh=false`.
     - `waitForDaemon` gives the first info.
     - Rename a fresh copy over the binary, then POST `{}` to the route and read
       lines until `restarting`.
     - Poll `GET /api/daemon` until `startedAt` is later than the first info's.
       Its `pid` is the subprocess's own (`cmd.Process.Pid`) and equals the
       first info's.
     - Cleanup sends SIGTERM and waits for the process.
   - `TestARefusedOwnBuildKeepsTheDaemon`: as the in-process test, with a
     script that exits 1. The route answers an `error` line, and `run` is still
     serving afterwards: `GET /api/daemon` answers, with `restart.refused`
     set.

**Verify:** `TMPDIR=/tmp/p6-5-suite CGO_ENABLED=1 go test ./cmd/webbridge/ -count=1 -timeout 10m`.

## Task 4 — the client's restart

**Files:** `internal/daemon/client.go`, `internal/daemon/client_test.go`.

1. **The request.** `Restart` POSTs `DaemonRestartRequest{Now: now}` to
   `daemonRestartPath` on `ctx`, with no client-side timeout. A non-200 answer
   is an error built as `Pin` builds its own, from the `{"error": …}` body.
2. **The answer.** It reads the body line by line with a `bufio.Scanner`
   (1 MiB cap), decodes each line as `RestartProgress`, and calls `progress`.
   - An `Error` line returns that error.
   - After a `Restarting` line it keeps reading to the end and returns nil.
     The daemon closing the connection as it restarts is a normal end, not an
     error.
   - An answer that ends with neither returns "the daemon ended the restart
     without restarting".
3. Tests, against `unixServer` (`internal/daemon/client_test.go:29`), as the
   client's other tests serve one:
   - `TestRestartStreamsProgress`: two waiting lines, then a restarting line;
     `progress` sees three, and the call returns nil.
   - `TestRestartReturnsTheDaemonsError`.
   - `TestRestartWithdrawsOnCancel`: cancelling `ctx` returns `context.Canceled`,
     and the server sees its request context end.
   - `TestRestartSendsNow`: the body is `{"now":true}`.

**Verify:** `TMPDIR=/tmp/p6-5-suite go test ./internal/daemon/ -count=1`.

## Task 5 — `marshal daemon restart` and the daemon's status line

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

1. **The interface.** `daemonClient` gains `Restart` with the client's
   signature. `fakeDaemonClient` gains scripted progress lines, an error and a
   pointer recording the `now` it was called with. It also gains a list of
   `Info` answers consumed in order, so a test can return a later `startedAt`
   after the restart.
2. **Dispatch.** `runDaemon` dispatches `restart` to `runDaemonRestart`. Both
   usage strings in `runDaemon` list `restart`.
3. **`runDaemonRestart(ctx, args, stdout, stderr)`** parses `--now` and
   rejects extra arguments.
   - It reads `Info` with `daemonStatusTimeout`.
   - **When that fails,** it does what `runDaemonService`'s `start` does: load
     the service, print `started`, and call `daemonReadyNote`. Then it
     returns.
   - **Otherwise** it calls `Restart` on `ctx`. For each waiting line it prints
     `waiting for <lines joined by "; ">`, but only when that text differs from
     the last one printed. On the restarting line it prints
     `restarting on <version>`.
   - On an error it returns `marshal daemon restart: <err>`. On ctrl+c the
     context ends, the request is withdrawn, and the command returns the
     context's error.
   - After a restart it polls `Info` every `daemonReadyPoll` until `StartedAt`
     is later than the first one's, bounded by `daemonStartTimeout` plus 30s.
     Then it prints `restarted: version <version>`. On timeout it prints
     `not answering yet; its log is <log>` and returns nil, as
     `daemonReadyNote` does.
4. **Status.** `runDaemonStatus`'s text mode prints, right after the daemon's
   info line, `daemon restart pending: <reasons>, waiting for <what>`, or
   `daemon restart refused: <refused>`, when `info.Restart` is set. This is
   plan 4's `daemonRestartLines` without the indent, through a shared helper.
5. Tests:
   - `TestDaemonRestartPrintsWhatItWaitsFor`: two identical waiting lines print
     once, then `restarting on v2` and `restarted: version v2`.
   - `TestDaemonRestartNowAsksForNow`.
   - `TestDaemonRestartStartsAnAbsentDaemon`: `Info` fails. The fake manager
     sees a start, and the output is `started`.
   - `TestDaemonRestartReportsARefusal`: an error line becomes the command's
     error.
   - `TestDaemonRestartRefusesArguments`.
   - `TestDaemonStatusShowsTheDaemonsRestart`.

**Verify:** `TMPDIR=/tmp/p6-5-suite CGO_ENABLED=1 go test ./cmd/marshal/ -run 'Daemon' -count=1`.

## Task 6 — the docs, the record, the manual run and full verification

**Files:** `docs/daemon/p6-daemon-lifecycle.md`, `AGENTS.md`.

1. **`AGENTS.md`.** Add `restart` to `daemon`'s description in the
   subcommand list. Add one sentence to the local daemon paragraph: a replaced
   `webbridge` re-executes itself at its next idle moment, keeping its process
   ID, and `marshal daemon restart [--now]` asks for that restart at once.
2. **The spec.** Fill a "Plan 5" record in the shape of plan 4's, and set the
   Status line to implemented, pending the manual run.
3. **The manual run.** This is the spec's Done-when run, by a person at a
   terminal with the daemon installed from this repository's build:
   - Open two sessions on two projects.
   - Start a long turn in one.
   - Rebuild both `marshal` and `webbridge` at their installed paths.
   - Watch the idle project restart first, then the busy one once its turn
     ends, then the daemon. Its PID in `marshal daemon status` stays the same.
   - Check that both screens come back with their drafts.
   - Then run `marshal daemon restart` with a turn running: it prints `waiting
     for a run in …` until the turn ends.
   - Then run `marshal daemon restart --now`.

   Record what happened. If no one ran it, record it as not performed, and do
   not claim the Done-when.
4. **Full verification,** as plan 4's Task 7, with `TMPDIR=/tmp/p6-5-suite`.

## Done when

- A replaced `webbridge` re-executes in place, with the same process ID, once
  every project child is idle and quiet and no fleet agent is mid-turn. It
  re-executes never when its new binary fails `--version`.
- Every viewer hears `marshal/restarting` first, and reconnects.
- `marshal daemon restart` waits for idle, says what it waits for, can be
  withdrawn, and reports the new version. `--now` does not wait, and the
  command starts a daemon that is not answering.
- `marshal daemon status` shows the daemon's own pending or refused restart.
- Every test named above passes, the mutation check fails as described, CI's
  suite command passes, and the manual run is recorded as done or as not done.
