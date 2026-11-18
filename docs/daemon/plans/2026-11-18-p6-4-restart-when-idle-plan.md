# P6 plan 4: a stale project child restarts when it is idle — implementation plan

Date: 2026-11-18. Status: ready for review.

Base: `main` at `bdd20ddc`. Every anchor below was checked there. Plans 1, 2, 3
and 6 are merged, so this plan assumes the following are already in place:
`internal/buildinfo`, `marshal/restarting` read by the session screen's loop,
`project/state` and `project/note` served by a project child, the status's
pending restart, and the `MARSHAL_ACP_PROJECT_CHILD` mark.

- **The baseline** is the batch's, recorded in the spec's "Implementation
  record". Rerun it at the base before Task 1 (see Execution).
- **Plan 5 runs after this one.** It extends this plan's sweep, its contract
  type and its relay registry, so it needs this plan on `main`.

Approved design: [p6-daemon-lifecycle.md](../p6-daemon-lifecycle.md):
decisions 2, 6 and 8; B4 and B6; Migration order item 4; Tests L1 to L5 (L4's
bridge half).
Previous plans: [plan 2](2026-11-16-p6-2-reconnect-plan.md) (the reconnect, and
`marshal/restarting` read but not sent) and
[plan 3](2026-11-16-p6-3-staleness-plan.md) (`project/state`, `project/note`).
Next: [plan 5](2026-11-18-p6-5-daemon-restart-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **The bridge sweeps its project children every 30s** and asks each whether
  it is stale and what keeps it busy.
- **A stale child restarts at its first idle moment:** nothing busy, and no
  relay frame for 2 minutes unless it has no viewer.
- **A build that cannot start is never installed.** Its sessions are told why,
  once per file.
- **The viewers hear it first.** Each session gets a note, and each relay gets
  `marshal/restarting`, before the child's process is replaced.
- **The restart is P4's crash path.** The child is respawned from the binary on
  disk, re-initialized and held again, and its sessions are resumed.
- **`marshal daemon status` shows each child's pending restart** and what it
  waits for.

## Non-goals

- **The bridge's own restart and `marshal daemon restart`.** Those are plan 5.
- **A fleet agent's own child.** B4 leaves it alone: it picks up a new build
  when the daemon restarts.
- **Retrying a respawn that fails.** The `--version` check makes this rare.
  When it does happen, this plan drops the child so the next attach starts a
  fresh one (decision 7 below). It does not add a retry loop to `supervise`.

## Assumptions

Each was checked at the base.

- **A project child is supervised by `Child`** (`web/bridge/child.go`).
  - `Stop` closes stdin, sends SIGTERM and escalates to SIGKILL after
    `stopGrace`, and sets `stopping` so that `supervise` ends (`child.go:229`).
  - `supervise` waits on each generation's `readDone` and then `Wait`. Unless
    `stopping` is set, it respawns after `restartBackoff` and calls `OnRestart`
    (`child.go:448`).
  - A failed respawn ends supervision (`child.go:475`).
  - `spawn` reuses the transport. `processTransport.Open` builds a new
    `exec.Command` from the binary path, so a respawn runs whatever is on disk
    at that path (`web/bridge/transport.go:55`).
- **A project child's `OnRestart`** is `reinitProjectChild`
  (`web/bridge/projectchild.go:351`), wrapped by `NewRegistry`. That wrapper
  then runs `resumeAll` on its own goroutine (`web/bridge/registry.go:181`,
  `:1157`). `reinitProjectChild` holds `pc.mu`, repeats the handshake and
  takes a pinned root's hold again.
- **An attach resolves its child through `projectChildFor`**
  (`projectchild.go:144`). That runs `startProjectChild` (`:189`), which first
  waits out a stop in flight (`waitStop`, `:506`). While `pc.ready` is true it
  does not touch the child. The pin route and a fleet spawn on a project
  resolve the child the same way.
- **The idle sweep** (`web/bridge/idle.go`) is the model for asking a child on
  the bridge's own connection, with a 5s bound and the child snapshot
  (`projectChildSnapshot`). It runs only when an idle timeout is set, so the
  restart sweep needs a loop of its own.
- **The registry records each session's viewers and last activity**
  (`sessionInfo.viewers`, `lastActive`; `registry.go:72` to `:77`).
  `attachViewer`, `detachViewer` and `touch` keep them current (`:432` to
  `:458`), and the relay touches on every frame in both directions
  (`web/bridge/relay.go`, `pumpClient` and `pumpAgent`).
- **The relays are not tracked anywhere.** `Fleet.relay`
  (`relay.go:82`) builds a `relay` and runs it to its end. Writes to the client
  are serialised by `relay.clientMu` (`relay.go:122`). `finish` (`:165`) runs
  once both pumps have returned.
- **`project/state` answers** `{"stale": [...], "busy": [...], "version": "..."}`,
  and `project/note` takes `{"text": "..."}` and answers a count
  (`internal/acpwire/project.go`; `internal/acp/host.go:411`, `:420`). Both are
  served only on the bridge's own connection.
- **`marshal/restarting`'s params are `{"reason", "version"}`**
  (`internal/acpwire/restart.go`). The session screen's loop records the last
  one on its connection (`internal/launch/daemon.go:930`) and titles the
  waiting screen with it.
- **`GET /api/daemon`'s children** come from `Fleet.projectChildren`
  (`web/bridge/daemoninfo.go:83`), as `contract.DaemonChild`
  (`web/bridge/contract/daemon.go`). `internal/daemon` aliases that type as
  `ChildInfo` (`internal/daemon/client.go:34`). `marshal daemon status` prints
  one line per child (`daemonChildLine`, `cmd/marshal/daemon.go:664`), then
  that child's environment warning indented under it (`runDaemonStatus`,
  `:463`).
- **The contract's one generated mirror** is
  `app/lib/src/client/contract.g.dart`: the Svelte client and its TypeScript
  generator were retired in #152. The bridge's recorded wire is
  `web/bridge/testdata/contract/`, pinned by `TestTheWireIsUnchanged`
  (`web/bridge/contractwire_test.go:44`). The tray reads `DaemonChild` by hand
  in `app/lib/src/daemon/status.dart` and ignores fields it does not know.
- **`marshal --version`'s first line is `marshal <version> (commit …)`**
  (`cmd/marshal/version.go`), so the version is its second field.
- **`headerLine`** (`web/bridge/attach.go:36`) cuts text to its first line,
  removes control characters and bounds the length. It is the rule for "the
  first line" of a failed build's output.
- **The local profile starts its sweeps in `run`**
  (`cmd/webbridge/main.go:617`, the idle sweep).
- **The end-to-end harness** (`test/daemon/attach_test.go:39`) runs this test
  binary as the project child, or the binary named by
  `MARSHAL_DAEMON_TEST_MARSHAL`. The binary's `TestMain`
  (`test/daemon/main_test.go:32`) answers only `acp` and `acp --viewers`, so
  it cannot yet answer `--version`.

## Decisions this plan makes beyond the spec

1. **One note per restart, sent before it.** B4's note, "Restarting on <new
   version>: <reasons>.", is added to every session while the old process
   still runs. The resume reloads it with the transcript, so it also serves as
   decision 6's "Restarted on" note. A second note after the restart would say
   the same thing twice.
2. **Busy is asked twice.** After the `--version` check passes, the bridge asks
   `project/state` once more, right before the note. If the child has gone
   busy in the meantime, the restart waits for the next sweep. That narrows the
   window in which a prompt can start between the check and the restart to the
   time a note takes.
3. **The quiet rule is per child.** A child is quiet at `cutoff` when none of
   its tracked sessions has a viewer, or when none has a `lastActive` after
   `cutoff`. Fleet sessions count, since they are what keeps a fleet agent on
   the child busy, and their runs already show in `busy`.
4. **An attach waits for a restart in flight.** A `recycling` channel on the
   project child is set before the process is ended, and closed once the new
   generation's handshake is done. `startProjectChild` waits on it, bounded by
   its caller's context, before anything else, as it waits out a stop.
5. **`Child.Recycle` ends a generation without ending supervision.** It is
   `Stop`'s close-stdin-then-SIGTERM, with SIGKILL after `stopGrace` only while
   that generation's output is still open. It never sets `stopping`.
6. **The sweep is synchronous.** `SweepRestarts` returns once every restart it
   started has a new, initialized generation, or once that has failed. Tests
   call it with a chosen `now` instead of waiting 30s or 2 minutes.
7. **A restart that does not come back** within 60s (no handshake on a new
   generation) is given up. The child is stopped and forgotten as a failed
   start is, and the marker is closed, so the next attach builds a fresh child
   and gets that start's own error. The restart is logged with the child's
   stderr tail.
8. **The refused build is remembered by file identity:** the `os.FileInfo` of
   the resolved `marshal` binary, compared with `os.SameFile`, size and
   modification time, as plan 3's record compares the executable. Until that
   identity changes, the child is not restarted for any reason, because a
   restart would spawn the refused binary.
9. **The version is the second field** of the first line of `--version`'s
   standard output, else that whole line. A check fails when the command does
   not exit 0 within 10s. Its first line is the first line of standard error,
   else of standard output, else the error's own text, through `headerLine`.

## Execution

- In the main checkout, if `git status` shows this plan or plan 5 uncommitted,
  commit both to `main` first, as explicit paths, with the message
  `docs/daemon: P6 plans 4 and 5`.
- Then branch: `git switch -c p6-4-restart-when-idle` from `main`, or create a
  worktree on that branch.
- Create the test directory first: `mkdir -p /tmp/p6-4-suite`. Keep `TMPDIR`
  that short for every test run: `TMPDIR=/tmp/p6-4-suite`. Unix socket paths
  are capped near 104 bytes on macOS.
- Record the baseline before Task 1: CI's suite command (Task 7) at the base
  commit. Note any failure, so no task re-proves it.
- Run each task's Verify and commit it as `p6-4-restart-when-idle: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The wire (`web/bridge/contract/daemon.go`, Task 1)

```go
// DaemonRestart is a restart the daemon is holding, as GET /api/daemon
// reports it (P6 B4).
type DaemonRestart struct {
	Reasons    []string `json:"reasons"`
	WaitingFor string   `json:"waitingFor,omitempty"` // "" when nothing holds it
	Refused    string   `json:"refused,omitempty"`    // the new build's failure; "" when none
}

// DaemonChild gains:
	Restart *DaemonRestart `json:"restart,omitempty"` // nil while the child is current
```

### The child (`web/bridge/child.go`, Task 2)

```go
// Recycle ends the current generation as Stop does, without ending
// supervision: supervise respawns it from the binary on disk and OnRestart
// runs. It returns once that generation's output has closed. It fails when the
// Child is not running or is stopping.
func (c *Child) Recycle() error
```

### Relays and quiet (`web/bridge`, Task 3)

```go
// fleet.go: Fleet gains
	relayMu sync.Mutex
	relays  map[*Registry]map[*relay]struct{} // the live relays, by the registry they count on

// relay.go
func (f *Fleet) trackRelay(r *relay)
func (f *Fleet) untrackRelay(r *relay)

// announceRestart writes marshal/restarting to every live relay counted on
// reg, and answers how many it reached.
func (f *Fleet) announceRestart(reg *Registry, reason, version string) int

// restartingMethod is acpwire.MethodRestarting; web/ may not import it.
const restartingMethod = "marshal/restarting"

// registry.go
// quietSince reports whether no session has a viewer, or none was active
// after cutoff (decision 3).
func (r *Registry) quietSince(cutoff time.Time) bool
```

### The sweep (`web/bridge/restart.go`, new; Task 4)

```go
const (
	restartSweepInterval = 30 * time.Second
	restartStateTimeout  = 5 * time.Second
	restartQuiet         = 2 * time.Minute
	buildCheckTimeout    = 10 * time.Second
	waitingForQuiet      = "a quiet moment"
)

// recycleTimeout bounds a restart's wait for its new generation (decision 7).
// A var so a test can shorten it, as restartBackoff is.
var recycleTimeout = 60 * time.Second

// projectState is project/state's answer as the bridge reads it.
type projectState struct {
	Stale   []string `json:"stale"`
	Busy    []string `json:"busy"`
	Version string   `json:"version"`
}

// RunRestartSweep runs SweepRestarts every restartSweepInterval until ctx
// ends or the fleet closes.
func (f *Fleet) RunRestartSweep(ctx context.Context)

// SweepRestarts checks every ready project child once, restarts each stale
// one that is idle at now, and returns when those restarts are done
// (decision 6).
func (f *Fleet) SweepRestarts(ctx context.Context, now time.Time)

func (f *Fleet) askProjectState(ctx context.Context, pc *projectChild) (projectState, error)

// checkNewBuild runs bin --version through f.checkBuild, or the binary when
// that is nil, and answers its version (decision 9).
func (f *Fleet) checkNewBuild(ctx context.Context, bin string) (string, error)

// buildVersionOf is decision 9's rule over --version's standard output.
func buildVersionOf(stdout string) string

// fleet.go: Fleet gains
	// checkBuild runs a new binary's --version; tests replace it. Nil runs
	// the binary.
	checkBuild func(ctx context.Context, bin string) (version string, err error)
	// sweepMu keeps one restart sweep at a time.
	sweepMu sync.Mutex

// projectchild.go: projectChild gains, guarded by mu
	restart   *DaemonRestart // the last sweep's view; nil when current
	refused   os.FileInfo    // the marshal binary whose check failed (decision 8)
	recycling chan struct{}  // open while a restart is in flight (decision 4)

// waitRecycle blocks while a restart of this child is in flight, until ctx ends.
func (pc *projectChild) waitRecycle(ctx context.Context) error
```

`DaemonRestart` is aliased in `web/bridge/daemoninfo.go` beside
`DaemonChild`, as `type DaemonRestart = contract.DaemonRestart`.

## Task 1 — the wire

**Files:** `web/bridge/contract/daemon.go`, `web/bridge/daemoninfo.go` (the
alias), `app/lib/src/client/contract.g.dart`,
`web/bridge/testdata/contract/` (regenerated).

1. Add `DaemonRestart` and the `Restart` field as Contracts declares them, and
   the alias.
2. Regenerate the Dart mirror and the recordings:
   - `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`
   - `TMPDIR=/tmp/p6-4-suite go test ./web/bridge/ -run TestTheWireIsUnchanged -update`

   Each diff must be the new type and the new field alone.

**Verify:**
`TMPDIR=/tmp/p6-4-suite go test ./web/bridge/contract/... -count=1` and
`TMPDIR=/tmp/p6-4-suite go test ./web/bridge/ -run 'TestTheWireIsUnchanged|TestWebIsStdlibOnly' -count=1`.

## Task 2 — `Child.Recycle`

**Files:** `web/bridge/child.go`, `web/bridge/child_test.go`.

1. `Recycle`, under `c.mu`, refuses with an error when the Child has not
   started or is stopping.
   - Otherwise it reads the current generation's `stdin` and `readDone`,
     closes that `stdin`, and sends SIGTERM through `signalCurrentResult`.
   - It then waits for `readDone` to close. If `stopGrace` passes first, it
     calls `killCurrent`, but only if `readDone` is still open at that moment
     (a non-blocking receive first). It then waits for `readDone` again.
2. It never touches `stopping`, so `supervise` reaps the generation, fails its
   pending requests, sleeps `restartBackoff`, respawns and calls `OnRestart`,
   exactly as it does after a crash.
3. `stopGrace` becomes a package `var`, as `restartBackoff` is
   (`child.go:35`), so a test can shorten it. Its value is unchanged.
4. Tests, over the re-executed helper child `child_test.go` already uses
   (`helperCommand`, `TestHelperProcess`, `runHelperChild`):
   - Add a helper mode, `stubborn`. It ignores SIGTERM
     (`signal.Ignore(syscall.SIGTERM)`) and keeps running after its stdin
     closes, until it is killed.
   - `TestRecycleRespawnsAndKeepsSupervising` (default mode): after `Recycle`,
     `Generation()` reaches 2, `OnRestart` ran once, a request answers on the
     new generation, and a later `Stop` still ends the Child.
   - `TestRecycleEscalatesAStubbornGeneration` (`stubborn` mode, with
     `stopGrace` shortened to 200ms): `Recycle` returns after the kill, the
     generation reaches 2, and the replacement still answers a request, so it
     was not killed too.
   - `TestRecycleRefusesAStoppedChild`: an error before `Start`, and after
     `Stop`.

**Verify:** `TMPDIR=/tmp/p6-4-suite go test ./web/bridge/ -run 'Recycle' -count=1`.

## Task 3 — the relays' registry, the notification, and quiet

**Files:** `web/bridge/relay.go`, `web/bridge/registry.go`,
`web/bridge/fleet.go` (the two fields), `web/bridge/relay_test.go`,
`web/bridge/registry_test.go`.

1. `Fleet.relay` calls `trackRelay` once the `relay` is built, before either
   pump starts. `finish` calls `untrackRelay` first. Both key the relay by
   `r.end.reg`. A registry whose set empties is deleted from the map.
2. `announceRestart` copies the set for `reg` under `relayMu`, then writes one
   frame to each relay's client under that relay's `clientMu`:
   `{"jsonrpc":"2.0","method":"marshal/restarting","params":{"reason":…,"version":…}}`
   followed by a newline, marshalled from a map so the reason is quoted.
   - A relay whose write fails is skipped, because its pumps will end it.
   - `version` is omitted when empty, matching the agent's `omitempty`.
   - It answers the number of writes that succeeded.
3. `quietSince` follows decision 3, under `r.mu`.
4. Tests:
   - `TestTheRelayIsTrackedWhileItRuns`: a relay through the attach harness
     (`newAttachHarness`, `attach`) is in `f.relays` for the child's registry,
     and gone once the client closes.
   - `TestTheViewersHearTheRestart`: two attaches to one session; after
     `announceRestart(pc.reg, "a new marshal build", "v2")`, each client's next
     line (`readLine`) is exactly the frame above, and the call answers 2.
   - `TestAnAnnouncementSkipsOtherChildren`: an attach on a second root reads
     nothing within 200ms.
   - `TestQuietSince`: a table over no sessions; sessions with no viewer and
     recent activity; one viewer with activity before the cutoff; one viewer
     with activity after it. Only the last is not quiet.

**Verify:** `TMPDIR=/tmp/p6-4-suite go test ./web/bridge/ -run 'Relay|Restart|Quiet|Announce' -count=1`.

## Task 4 — the sweep and the restart

**Files:** `web/bridge/restart.go` (new), `web/bridge/restart_test.go` (new),
`web/bridge/projectchild.go`, `web/bridge/daemoninfo.go`,
`web/bridge/fleet.go` (two fields), and one wiring line in
`cmd/webbridge/main.go`.

1. **The loop.** `RunRestartSweep` ticks every `restartSweepInterval` and calls
   `SweepRestarts(ctx, time.Now())`, as `RunIdleSweep` ticks, returning on
   `ctx` or `f.done`. In `run`, start it on the local profile with
   `go fleet.RunRestartSweep(ctx)`, next to `go fleet.RunIdleSweep(ctx)`
   (`cmd/webbridge/main.go:617`). Unlike the idle sweep, it runs whatever the
   idle timeout is.
2. **One sweep.** `SweepRestarts` holds `sweepMu` for its whole run. It walks
   `projectChildSnapshot()` and, for each child, reads `child`, `reg`, `ready`
   and `recycling` under `pc.mu`. It skips a child with no child, no registry,
   not ready, or a restart in flight. For every other child it runs the steps
   below, one child after another.
3. **Asking.** `askProjectState` sends `project/state` on `pc.child` with
   `restartStateTimeout` and decodes `projectState`. An error is logged at warn
   with the root, and that child is skipped with its last `restart` left as it
   was.
4. **Current.** No stale reasons: set `pc.restart` to nil and go on.
5. **What it waits for.** This is the first busy line. When nothing is busy and
   `!reg.quietSince(now.Add(-restartQuiet))`, it is `waitingForQuiet`.
   Otherwise it is empty. Set `pc.restart` to the reasons, this waiting-for,
   and the `Refused` text kept from an earlier sweep while `pc.refused` still
   matches. Go on to the next child when waiting-for is not empty.
6. **The binary.** Resolve `f.marshalBin` with `exec.LookPath` and `os.Stat`
   it. If it matches `pc.refused` (decision 8), go on. Otherwise run
   `checkNewBuild` with `buildCheckTimeout`.
   - **On failure:** set `pc.refused` to that `FileInfo` and `Refused` to the
     first line (decision 9). Send `project/note`, bounded by
     `restartStateTimeout`, with "A new marshal build could not start
     (<first line>); this project stays on <version>." Here `<version>` is the
     state's version, or "its current build" when that is empty. Log at warn.
     Go on.
   - **On success:** clear `pc.refused`.
7. **Busy again** (decision 2). Ask `project/state` once more. If anything is
   busy, update `pc.restart`'s waiting-for and go on.
8. **The restart.** `recycleProjectChild` does the following, in order:
   - Send `project/note` with "Restarting on <version>: <reasons joined by ", ">."
   - Under `pc.mu`, make `pc.recycling` and set `pc.ready` to false.
   - Call `announceRestart(pc.reg, <reasons joined>, <version>)`.
   - Call `pc.child.Recycle()`.
   - Wait for `pc.recycling` to close, for at most `recycleTimeout`.
   - **When it closes:** set `pc.restart` to nil, and log at info with the
     root, the version and the reasons.
   - **When it does not** (decision 7): log at warn with the child's
     `StderrLog()`, close the marker under `pc.mu`, and then `pc.stop()` and
     `f.forgetProjectChild(pc)`.
   - A `Recycle` error is handled the same way, without the wait.
9. **The marker's end.** `reinitProjectChild` closes `pc.recycling` and sets it
   to nil before it returns, on every path, success or failure. On failure the
   child is left as today's failed handshake leaves it. `waitRecycle` reads the
   marker under `pc.mu` and waits on it outside the lock, or until `ctx` ends.
   `startProjectChild` calls it before `waitStop` and returns its error.
10. **`checkNewBuild`.** With `f.checkBuild` set, it calls that. Otherwise it
    runs `exec.CommandContext(ctx, bin, "--version")` with separate stdout and
    stderr buffers and the bridge's own environment, and applies decision 9.
11. **What the daemon reports.** `projectChildren` copies `pc.restart`, under
    `pc.mu` with the other fields, into `DaemonChild.Restart`.
12. **Tests**, over `projectFleetWith`'s fakes. Each sets
    `results["project/state"]` (or a `handler` for answers that change) and
    `f.checkBuild`. `f.marshalBin` points at a file in a temporary directory
    the test owns.
    - `TestAStaleIdleChildIsRestarted`: stale `["a new marshal build"]`,
      version "v1", the check answers "v2".
      - After `SweepRestarts(ctx, time.Now())` the fake's generations are 2.
      - One `project/note` reads "Restarting on v2: a new marshal build."
      - The registry's sessions were resumed.
      - `DaemonChild.Restart` is nil.
    - `TestACurrentChildIsLeftAlone`: nothing stale. No signal, no note, no
      check.
    - `TestABusyChildWaits`: busy `["a run in s-1"]`. No restart, and
      `Restart.WaitingFor` is "a run in s-1".
    - `TestAViewedChildWaitsForAQuietMoment`: a session with `attachViewer`
      and `touch` now.
      - A sweep at `now` waits with `waitingForQuiet`.
      - A sweep at `now + 3m` restarts.
    - `TestAChildWithNoViewerNeedsNoQuietPeriod`: a session touched now with
      no viewer restarts at `now`.
    - `TestARefusedBuildIsNotInstalled`: the check fails with "broken build".
      - No restart.
      - One note reads "A new marshal build could not start (broken build);
        this project stays on v1."
      - `Restart.Refused` is "broken build".
      - A second sweep sends no second note and runs no second check.
      - Rewriting the binary file (a new size) runs the check again.
    - `TestABuildThatGoesBusyWaits`: the first `project/state` is idle and the
      second busy. No restart, no "Restarting" note.
    - `TestAnUnansweredStateSkipsTheChild`: `project/state` answers an rpc
      error. No restart, and the previous `restart` is kept.
    - `TestAnAttachWaitsForTheRestart`: with `checkBuild` blocking on a
      channel the test holds, an attach started during the restart returns
      only after the new generation's `initialize` (by the fake's frame
      order).
    - `TestARestartThatDoesNotComeBackDropsTheChild`: a fake whose second
      `Open` fails. After the sweep, `projectEntry` finds nothing for the root.
      `recycleTimeout` is shortened through a package var for this test, as
      `restartBackoff` is.
    - `TestThePinIsHeldAgainAfterARestart`: a pinned root's fake sees
      `project/hold` on the new generation.
    - `TestBuildVersionOf`: "marshal v1.2.3 (commit …)" gives "v1.2.3",
      "webbridge dev+abc" gives "dev+abc", and a one-word line gives itself.

**Verify:** `TMPDIR=/tmp/p6-4-suite go test ./web/bridge/ -count=1 -timeout 15m`
and `TMPDIR=/tmp/p6-4-suite go test ./cmd/webbridge/ -count=1`.

**Mutation check:** make the quiet test always pass (treat every child as
quiet). `TestAViewedChildWaitsForAQuietMoment` must fail. Restore.

## Task 5 — `marshal daemon status` shows pending restarts

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

1. In `runDaemonStatus`'s text mode, after each child's line and before its
   environment line, print one line when `child.Restart` is set:
   - `  restart pending: <reasons joined by ", ">, waiting for <waitingFor>`
     when it waits;
   - `  restart pending: <reasons>` when it does not;
   - `  restart refused: <refused>` as a line of its own when `Refused` is set.

   Write it as a pure helper, `daemonRestartLines(child daemon.ChildInfo)
   []string`, beside `daemonChildLine`.
2. `--json` needs nothing: the field rides in `Status.Daemon`. `StatusSchema`
   stays 1, because the change is additive.
3. Tests:
   - `TestDaemonRestartLines`: a table over none, waiting, not waiting and
     refused.
   - `TestDaemonStatusReportsARunningDaemon` gains a child with a pending
     restart, and asserts its line's position under the child's.

**Verify:** `TMPDIR=/tmp/p6-4-suite CGO_ENABLED=1 go test ./cmd/marshal/ -run 'Daemon' -count=1`.

## Task 6 — end to end (L1 to L5)

**Files:** `test/daemon/main_test.go`, `test/daemon/restart_test.go` (new).

1. **The child answers `--version`.** `runChild` handles a sole `--version`
   argument by printing `marshal <build>` and returning. `<build>` is
   `MARSHAL_DAEMON_TEST_BUILD` when set, else `test-build`. The bridge's check
   inherits this process's environment, the child marker included, so this
   binary answers.
2. **Helpers** in `restart_test.go`:
   - `ownedBinary(t) string` copies this test binary into a short temporary
     directory, mode 0755, and sets `MARSHAL_DAEMON_TEST_MARSHAL` to it before
     the harness is built.
   - `rebuild(t, path, build string)` writes a fresh copy beside it and renames
     it over `path`, so the file has a new identity. It sets
     `MARSHAL_DAEMON_TEST_BUILD` to `build`.
   - `breakBuild(t, path)` renames over it a shell script that prints "broken
     build" to standard error and exits 1.
   - `tryAttach(h, id) (*attachment, error)` is `h.attach` without the fatal
     failure, for use inside `waitFor` while the resume runs.
   - `childRestart(t, h) *daemon.ChildInfo` reads `h.client.Info` and returns
     the harness root's child.
3. **Tests.** Each sweeps with `h.fleet.SweepRestarts(ctx, time.Now().Add(3*time.Minute))`
   unless it says otherwise.
   - `TestARebuiltMarshalReachesAnIdleProject` (L1). This is
     `TestAScreenComesBackAfterTheChildRestarts`'s runner, with the crash
     replaced by `rebuild(…, "rebuilt")` and a sweep on a goroutine.
     - The waiting screen's rendered view reads "is restarting: a new marshal
       build".
     - The screen that comes back holds the draft.
     - Its transcript holds the earlier note and "Restarting on rebuilt: a new
       marshal build."
   - `TestARunningTurnHoldsTheRestart` (L2). A gated scripted turn is started
     with `promptViewer`, as `TestTheFleetCancelsAViewersTurn` holds one.
     After `rebuild` and a sweep:
     - the attachment's `Status().Restart` is set and its `WaitingFor` begins
       "a run in", and the child did not restart;
     - after the gate opens and the prompt returns, a second sweep restarts it,
       and a reattach (`waitFor` over `tryAttach`) finds the "Restarting on"
       note.
   - `TestAnActiveWatchHoldsTheRestart` (L2). A watch is started with
     `attachDirectly` and `startWatch`, as `TestADetachedSessionsWakeRuns`
     starts one, on a flag file that never exists. A sweep leaves the child
     running with `WaitingFor` "watch <name> in …". After `stopWatch`, a sweep
     restarts it.
   - `TestABrokenBuildIsRefused` (L3). After `breakBuild`, a sweep leaves the
     child on its build.
     - The session's transcript holds "A new marshal build could not start
       (broken build); this project stays on".
     - `childRestart`'s `Refused` is "broken build".
     - The attachment still answers a note round trip.
   - `TestAUserConfigWriteMarksOnlyTheOtherProject` (L4, the bridge's half). A
     second project root is set up with `initProject` and added to `h.ws`, and
     both roots are attached. The first attachment's
     `host.SaveUserValue(ctx, "tools.shell.allow_network", true)` runs, then a
     sweep at `time.Now()` (no restart: both are viewed and recently active).
     `GET /api/daemon` then shows:
     - no `Restart` on the first root's child;
     - `Restart.Reasons` `["config.toml changed"]` with `WaitingFor` "a quiet
       moment" on the second.
   - `TestAnEditedProjectConfigResumesUntrusted` (L5). Set up with
     `trustUserConfig`, `writeTrustProjectConfig(…, "before-edit")`, an attach
     and a permanent `DecideTrust`. Then `writeTrustProjectConfig(…,
     "after-edit")` and a sweep. A reattach to the same session shows:
     - `host.TrustPrompt().InPlace` is true;
     - the session is not trusted;
     - its config's project name is not "after-edit".

**Verify:** `TMPDIR=/tmp/p6-4-suite CGO_ENABLED=1 go test ./test/daemon/ -run 'Rebuilt|HoldsTheRestart|BrokenBuild|UserConfigWrite|EditedProjectConfig|ComesBack' -count=1 -timeout 15m`.

**Mutation check:** have the sweep restart without asking `project/state` a
second time and without the busy check. `TestARunningTurnHoldsTheRestart` must
fail. Restore.

## Task 7 — the docs, the record and full verification

**Files:** `docs/daemon/p6-daemon-lifecycle.md`, `docs/ARCHITECTURE.md` (the
bridge row, if it lists the sweeps), `AGENTS.md` (the paragraph on the local
daemon).

1. Fill a "Plan 4" record in the spec's Implementation record, in the shape of
   plans 2 and 3: what was built, deviations, mutation checks and
   verification. Update the spec's Status line to say plans 4 and 5 are
   written with anchors checked at `bdd20ddc`.
2. In `AGENTS.md`'s local daemon paragraph, add one sentence after the one on
   the 30s linger: a project child whose `marshal` binary or config changed is
   restarted by the daemon at its next idle moment, and its screens reconnect.
3. **Manual run.** This needs a person at a terminal with the daemon installed
   from this build:
   - Open a session and type a draft.
   - Rebuild `marshal` at the installed path.
   - Wait up to 2½ minutes without typing.
   - Record what the screen showed and whether it came back on its session
     with the draft and the "Restarting on" note.
   - Then start a long turn, rebuild again, and record that
     `marshal daemon status` says `restart pending … waiting for a run in …`
     until the turn ends.

   If no one can run it, record it as not performed. Do not claim it.
4. **Full verification:**
   - `gofmt -l internal cmd test web` prints nothing.
   - `CGO_ENABLED=1 go vet ./...` is clean.
   - CI's suite command passes:
     `TMPDIR=/tmp/p6-4-suite CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 20m -skip '^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget|AKeyPressOverThePipeBudget)$'`.
   - `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` builds.
   - The scripted usability suite passes against it.
   - `cd app && flutter test test/boundary_test.dart` passes, if Flutter is
     installed. Otherwise record that it was not run.

## Done when

- A stale project child restarts on the new build at its first idle moment,
  and never while a run, subagents, jobs, a swarm, an SDD run or a watch keeps
  it busy, or while a viewed session has been active in the last 2 minutes.
- A build that fails `--version` is never installed, and its sessions are told
  once.
- Every viewer of the child receives `marshal/restarting` before its connection
  closes, and comes back through plan 2's reconnect.
- `marshal daemon status` shows each child's pending or refused restart.
- Every test named above passes, each mutation check fails as described, and
  CI's suite command passes.
