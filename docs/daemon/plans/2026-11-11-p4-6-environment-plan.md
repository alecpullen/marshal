# P4 plan 6: The project environment and idle sessions — implementation plan

Date: 2026-11-11. Status: ready for review.
Base: `main` at `29851a61` (P4 plans 3 and 4 merged). Every anchor below was checked there. The full suite ran there (`TMPDIR=/private/tmp/p4bt`, `-timeout 20m`): 121 packages ok, `web/bridge` included. The one failure was `internal/acp`'s `TestStackRoundTripBudget`, the known load flake, which passed three times when rerun alone.
- **The spec edits are already made,** uncommitted in the main checkout, in `p4-local-daemon.md` (listed in [plan 5](2026-11-11-p4-5-daemon-mode-plan.md)'s header). This plan's are D7's "The details (plan 6)" block, the new D7a, D11's pointer, H9, the Design table's bridge, `cmd/marshal` and config rows, and Migration order item 6.
- **Plan 5 runs beside this one.** It touches `internal/launch`, `internal/app/tui`, `internal/commands`, `cmd/marshal/main.go` and `test/daemon`, none of which this plan touches. Both plans fill their own placeholder in the spec's Implementation record, and nothing else in the spec.

Approved design: [p4-local-daemon.md](../p4-local-daemon.md): decision 4's idle timeout, D7 (with its details block), D7a, D11's status lines, Migration order item 6, Tests W8 and W10.
Previous plans: [P4 plan 2, the local profile](2026-11-09-p4-2-local-profile-plan.md) and [P4 plan 4, project children and the relay](2026-11-10-p4-4-relay-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a project child the local daemon starts runs with the project's own environment, as a terminal in that project would give it, and a session nobody is attached to and nothing runs in ends after the idle timeout.
- The bridge resolves each project's environment through the user's login shell and direnv, caches it, and falls back with a warning.
- The bridge sweeps idle sessions and closes them.
- `marshal daemon install` writes `SHELL` and the idle timeout into the unit, and `marshal daemon status` reports each child.

**Non-goals:**
- **No TUI, `launch`, agent-side or wire change.** No ACP method is new: the sweep reads the existing `session/status`, which serves a client without the marshal capability.
- **The server profile is unchanged.** Its children keep the environment the bridge gives them (design §6.1), and it has no idle sweep.
- **No warning in the TUI** (H9).
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p4-6-suite`. Create the directory first: `mkdir -p /private/tmp/p4-6-suite`. Keep the path short: a long one breaks the unix-socket tests in `web/bridge`.
- **`web/` is standard library only** (`TestWebIsStdlibOnly`, `web/bridge/boundary_test.go`). Nothing below imports `marshal/internal/...` from `web/` or `cmd/webbridge`.
- **The project child** (`web/bridge/projectchild.go`).
  - `projectChild` (`:25`) holds `root`, `socket`, `child`, `reg`, `version`, and `mu` guarding `ready` and `linger`.
  - `buildProjectChild(root, socket)` (`:62`) returns the fleet's `newProjectChild` when a test set it, else a `Child` running `marshalBin` in `root` with `--viewers`. Its `Env` is nil, so the child inherits the bridge's environment (`Child.Env`, `child.go:70`; `newProcessTransport`, `transport.go:61`).
  - `startProjectChildLocked(ctx, pc)` (`:169`) builds and starts the child under `pc.mu`, then handshakes within `controlHandshakeTimeout` (30s, `control.go:22`).
  - `rearmLinger(pc)` (`:392`) arms the linger when the registry is empty.
- **The registry** (`web/bridge/registry.go`): `sessionInfo` (`:67`) carries `viewers` and `lastActive`; `Registry.Close(ctx, id)` (`:246`) sends `session/close`, cancels the session's waits and untracks it; `summaries` (`:315`) and `count` (`:336`). `Child.Request(ctx, method, params)` (`child.go:238`) calls the child on the bridge's own connection.
- **`GET /api/daemon`** (`web/bridge/daemoninfo.go`): `DaemonChild` (`:20`) is filled by `Fleet.projectChildren` (`:69`), which reads `pc.version` and `pc.reg` under `pc.mu`.
- **Fleet seams:** `newProjectChild` (`fleet.go:263`), `afterFunc` (`:331`), `lingerDur` (`:343`), the fleet's `done` channel (read in `projectChildFor`), and setters such as `SetHostProcessesOnly` (`fleet.go:728`).
- **Bridge test helpers that exist** (`web/bridge/projectchild_test.go`, `fakeagent_test.go`): `projectFleet` and `projectFleetWith` (`:37`, `:44`) build a fleet whose project children are `fakeProjectChild`s; `projectEntry` (`:73`); `shortStateDir` (`:17`); `fakeAgent.results` answers a method with a fixed value and `fakeAgent.handler` answers it with code, and an unlisted method answers `{}` (`fakeagent_test.go:94`); `fakeAgent.calls(method)` lists what was called.
- **Platform splits** follow `peercred_unix.go` (`//go:build linux || darwin`) and `peercred_other.go` (`//go:build !linux && !darwin`).
- **`cmd/webbridge`** (`main.go`): `parseConfig` (`:110`) defines the flags; `applyProfile` (`:238`) validates the local profile, refusing a local-only setting under the server profile as it refuses `--socket` (`:241`), and reads `givenSettings` (`:204`); `runSubcommand` (`:305`) handles `egress` and `secrets`; the local profile's fleet is set up at `:496` (`SetHostProcessesOnly`) and its socket at `:538`. Tests: `local_test.go` (`TestTheLocalProfilesDefaults` `:87`, `TestTheLocalProfileRefuses` `:119`) and `main_test.go`.
- **`internal/daemon`**: `UnitSpec` and `UnitSpec.Args` (`daemon.go:36`, `:44`); `LaunchdPlist` and `SystemdUnit` render the args and `Env` (`units.go`); `ChildInfo` (`client.go:26`). Tests: `units_test.go` (`TestThePlistNamesEverything` `:37`, `TestTheSystemdUnitNamesEverything` `:111`).
- **`cmd/marshal/daemon.go`**: `daemonUnitSpec` (`:112`), `daemonEnv` (`:151`, PATH and the four location variables), `runDaemonStatus` (`:261`), `daemonInfoLine` (`:314`), and the seams `daemonManagerFor` and `daemonClientFor` (`:25`, `:29`). Tests in `daemon_test.go`: `TestDaemonInstallPassesExplicitPaths` (`:124`), `TestDaemonStatusReportsARunningDaemon` (`:225`).
- **Config** (`internal/app/config`): `Config` (`types.go:12`); the file form `configFile` (`file_types.go`); `merge.go` parses `web.fetch_timeout` from a `*string` with `time.ParseDuration` (`:390`); `Default()` (`defaults.go:10`) sets `FetchTimeout`; `writeSections` (`save.go:212`) writes the file form back. `web.fetch_timeout` is the model for a duration setting in all four places. `config.Load(LoadOptions{HomeDir, SkipProjectConfig: true})` loads defaults and the user config only (`types.go:740`).

**Decisions this plan makes beyond the spec** (D7's details block and D7a record them):

1. **The printer is the bridge itself.** `webbridge print-env` writes `EnvMarker` and then `os.Environ()`, each entry NUL-terminated. The login shell runs `exec '<webbridge>' 'print-env'`, so the bytes a startup file prints before it are skipped by looking for the last marker. The format is written and parsed in `web/bridge` only.
2. **A resolution is two steps, each bounded by `ProjectEnv.Timeout`** (10s). The shell runs in its own process group, killed as a group when its step's time is up, with a `WaitDelay` of one second, so a startup file that backgrounds a process cannot hold the step open. Its standard output is capped at 4 MiB.
3. **direnv is optional and found on the resolved `PATH`.** No `.envrc`, or no `direnv` on the resolved `PATH`, means no overlay and no warning.
4. **The bridge's location variables win** over the resolved values when the bridge has them set (`MARSHAL_CONFIG_DIR`, `MARSHAL_DATA_DIR`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`).
5. **Only a clean resolution is cached.** A fallback, or a login environment whose direnv overlay failed, is retried at the next start.
6. **Warnings carry no output.** They name the step and how it failed. The log line for a resolution carries the root and the number of variables, never a value.
7. **The resolver is applied to whatever child `buildProjectChild` builds,** a test's fake included, so the wiring is testable without a process.
8. **The sweep closes only what it has just read as idle.** It reads `session/status` for a candidate, skips one with a `run` or whose `lastRun` ended after the cutoff, and closes through a registry call that rechecks the viewers and the last activity under the registry's lock. A status read that fails skips the session; it never closes one it could not read.
9. **The idle timeout is written into the unit at install.** `[daemon] idle_session_timeout` is read with the project config skipped, so a project cannot change the daemon's behaviour, and a change takes effect at the next `marshal daemon install`.

**Execution:**
- In the main checkout, if `git status` shows any of these uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon: record P4 plans 5 and 6`:
  - `docs/daemon/p4-local-daemon.md`;
  - this plan and `docs/daemon/plans/2026-11-11-p4-5-daemon-mode-plan.md`.

  Plan 5's executor may have committed them already. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p4-6-environment` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p4-6-environment: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The environment (`web/bridge/projectenv.go`)

```go
// EnvMarker precedes the environment that `webbridge print-env` writes.
const EnvMarker = "\x00marshal-env\x00"

// WriteEnv writes EnvMarker and then each entry of environ followed by a NUL.
func WriteEnv(w io.Writer, environ []string) error

// parseEnv returns the entries after the last EnvMarker in out; an error when
// out holds no marker.
func parseEnv(out []byte) ([]string, error)

// ProjectEnv resolves a project's environment for its child (P4 D7). The
// zero values of its fields are the production defaults.
type ProjectEnv struct {
	PrintEnv []string      // argv that prints the environment: the webbridge and "print-env"
	Shell    string        // the login shell; empty means Base's SHELL, else /bin/sh
	Base     []string      // the bridge's environment: the shell starts from it and a fallback returns it; nil means os.Environ()
	Home     string        // whose startup files are fingerprinted; empty means Base's HOME
	Timeout  time.Duration // each step's bound; zero means envStepTimeout

	mu    sync.Mutex
	cache map[string]envCacheEntry
}

const (
	envStepTimeout   = 10 * time.Second
	envOutputCap     = 4 << 20
	envKillWaitDelay = time.Second
)

// NewProjectEnv is a resolver that prints with printEnv.
func NewProjectEnv(printEnv []string) *ProjectEnv

// Resolve returns root's environment, and the reason when it fell back.
func (p *ProjectEnv) Resolve(ctx context.Context, root string) (env []string, warning string)

// envCacheEntry is one cached resolution: the fingerprint it was made at.
type envCacheEntry struct {
	fingerprint string
	env         []string
}
```

### The fleet and the registry (`web/bridge`)

```go
// SetProjectEnv makes project children start with their project's
// environment (P4 D7). Without it they inherit the bridge's.
func (f *Fleet) SetProjectEnv(p *ProjectEnv)

// SetIdleSessionTimeout sets how long a project session with no viewer and
// no run lives on (P4 D7a); zero never ends one.
func (f *Fleet) SetIdleSessionTimeout(d time.Duration)

// RunIdleSweep sweeps every idleSweepInterval until ctx ends or the fleet
// closes. It returns at once when the timeout is zero.
func (f *Fleet) RunIdleSweep(ctx context.Context)

// sweepIdleSessions closes every project session idle past the timeout at
// now, and returns the IDs it closed.
func (f *Fleet) sweepIdleSessions(ctx context.Context, now time.Time) []string

const (
	idleSweepInterval  = 30 * time.Second
	idleStatusTimeout  = 5 * time.Second
)

// idleCandidates lists the tracked sessions with no viewer whose last
// activity is at or before cutoff.
func (r *Registry) idleCandidates(cutoff time.Time) []string

// closeIfIdle closes id as Close does when, under the registry's lock, it
// still has no viewer and its last activity is at or before cutoff. It
// reports whether it closed the session.
func (r *Registry) closeIfIdle(ctx context.Context, id string, cutoff time.Time) (bool, error)

// buildProjectChild's new shape: the warning is the environment's.
func (f *Fleet) buildProjectChild(ctx context.Context, root, socket string) (*Child, string, error)

// On projectChild, guarded by mu:
	envWarning string

// On DaemonChild:
	EnvWarning string `json:"envWarning,omitempty"`
```

### Config and the unit

```go
// internal/app/config, on Config:
	Daemon DaemonConfig `toml:"daemon"`

// DaemonConfig is the local daemon's user settings (P4 D7a).
type DaemonConfig struct {
	// IdleSessionTimeout ends a daemon session with no viewer and no run
	// after this long; zero never ends one. Default 30m.
	IdleSessionTimeout time.Duration `toml:"idle_session_timeout"`
}

// internal/daemon, on UnitSpec:
	IdleSessionTimeout time.Duration // written as --idle-session-timeout

// internal/daemon, on ChildInfo:
	EnvWarning string `json:"envWarning,omitempty"`
```

## Task 1 — the environment resolver

**Files:** a new `web/bridge/projectenv.go`, a new `web/bridge/projectenv_unix.go` (`//go:build linux || darwin`), a new `web/bridge/projectenv_other.go` (`//go:build !linux && !darwin`), a new `web/bridge/projectenv_test.go`.

1. `WriteEnv` and `parseEnv` (decision 1). `parseEnv` splits what follows the last marker at NULs and drops empty entries and entries without `=`.
2. `Resolve(ctx, root)`:
   1. Compute the fingerprint: for each of `root`'s `.envrc`, `.tool-versions`, `mise.toml`, `.mise.toml` and `.nvmrc`, and each of the home's `.profile`, `.bash_profile`, `.bash_login`, `.bashrc`, `.zshenv`, `.zprofile`, `.zshrc`, `.zlogin` and `.config/fish/config.fish`, the path and either its modification time in nanoseconds or "absent", joined in that order.
   2. Under `p.mu`, return the cached environment when the root's entry has the same fingerprint.
   3. Build the shell's command: `exec` followed by each `PrintEnv` element in single quotes. An element holding a single quote falls back with "the webbridge path cannot be quoted for the login shell".
   4. Run `<shell> -l -c <command>` in `root`, with `Base` as its environment and the step's timeout, through `runEnvStep` (below). A start failure, a timeout, a non-zero exit, output over the cap, or output with no marker falls back with a warning that names it: "the login shell timed out after <d>", "the login shell exited with status <n>", "the login shell printed more than 4 MiB", "the login shell printed no environment", or "the login shell could not start".
   5. When `root/.envrc` exists and `direnv` is found on the resolved environment's `PATH` (search each directory in order for an executable regular file), run `direnv export json` in `root` with the resolved environment through `runEnvStep`. Decode its output as an object of string or null values: a string sets the variable, null unsets it. Empty output means nothing to apply. A failure keeps the login environment and warns "direnv export timed out after <d>", "direnv export exited with status <n>" or "direnv export printed something other than JSON".
   6. Overlay the bridge's location variables (decision 4) from `Base`.
   7. Cache the result under the fingerprint only when there is no warning (decision 5).
   8. A fallback returns `Base` (with nothing resolved) and the warning.
3. `runEnvStep(ctx, timeout, dir, env, argv) ([]byte, error)` runs a command with standard output captured up to `envOutputCap` and standard error discarded. In `projectenv_unix.go` it starts the command in its own process group, sets `Cancel` to kill the whole group with `SIGKILL`, and sets `WaitDelay` to `envKillWaitDelay` (decision 2). In `projectenv_other.go` it sets only `WaitDelay`. Its errors let `Resolve` tell a timeout, an exit status and an oversize output apart.
4. Log each resolution once at info with the root and the variable count, and each fallback at warn with the root and the warning (decision 6), on `slog.Default()` as the rest of the bridge logs.
5. Tests in `projectenv_test.go`. They write scripts into a short temporary directory: a printer script that writes the marker and then `/usr/bin/env -0`'s output, a home with a `.profile`, and fake `direnv` scripts. `Shell` is `/bin/sh`, `Base` holds `HOME` (the test's home) and a `PATH` of `/usr/bin:/bin`, and `Timeout` is short where a test waits on it.
   - `TestWriteEnvRoundTrips`: `parseEnv` of `WriteEnv`'s output with a banner written before it returns the entries, a value holding a newline included.
   - `TestTheLoginShellsExportsReachTheEnvironment` (W8): a `.profile` that echoes a banner and exports `P6_LOGIN=yes` gives an environment holding it.
   - `TestDirenvOverlaysTheLoginEnvironment` (W8): with an `.envrc` in the root and a fake `direnv` on a `PATH` only the `.profile` exports, its JSON sets `P6_DIRENV=on` and unsets a variable the profile exported.
   - `TestNoDirenvMeansNoOverlayAndNoWarning`: an `.envrc` with no `direnv` on the resolved `PATH` resolves cleanly.
   - `TestATimedOutShellFallsBack` (W8): a `.profile` that sleeps past the timeout (with a background `sleep` holding its output open) returns `Base` and "the login shell timed out after …" within the timeout plus two seconds.
   - `TestAFailingShellFallsBack`: a `.profile` that exits 3 returns `Base` and a warning naming status 3; the warning holds nothing the profile printed.
   - `TestAFailedOverlayKeepsTheLoginEnvironment`: a fake `direnv` that exits 1 keeps `P6_LOGIN` and warns, and the next resolve runs the shell again.
   - `TestTheLocationVariablesStayTheBridges`: a `.profile` that exports another `MARSHAL_DATA_DIR` loses to `Base`'s.
   - `TestAChangedEnvrcResolvesAgain` (W8): a `.profile` that appends a line to a counter file each run; two resolves run it once; moving `.envrc`'s modification time forward (`os.Chtimes`) runs it again, and so does creating `.nvmrc`.
   - `TestAFallbackIsNotCached`: after a failing resolve, fixing the `.profile` (without changing a fingerprinted file's time) resolves cleanly at the next call.

**Verify:**
- `go test ./web/bridge/ -run 'WriteEnv|LoginShell|Direnv|TimedOutShell|FailingShell|FailedOverlay|LocationVariables|ChangedEnvrc|FallbackIsNot' -count=1`
- `GOOS=linux go vet ./web/...` and `GOOS=windows go vet ./web/bridge/` are clean.
- `go test ./web/bridge/ -run TestWebIsStdlibOnly -count=1`
- Mutation check: drop the process-group kill. `TestATimedOutShellFallsBack` must fail by exceeding its bound. Restore it.

## Task 2 — project children start with it, and `webbridge print-env`

**Files:** `web/bridge/projectchild.go`, `web/bridge/daemoninfo.go`, `web/bridge/fleet.go`, `web/bridge/projectchild_test.go`, `cmd/webbridge/main.go`, `cmd/webbridge/main_test.go`.

1. Add `SetProjectEnv` beside `SetHostProcessesOnly`, storing the resolver on the fleet.
2. Give `buildProjectChild` the context and the warning (Contracts). After it has a child, from the test seam or its own build, and when the fleet has a resolver, resolve the root and set the child's `Env` (decision 7). `startProjectChildLocked` passes its context and stores the warning in `pc.envWarning`, so a restarted generation (same `Child`, same `Env`) keeps the warning it started with.
3. `projectChildren` reads `pc.envWarning` under `pc.mu` beside `pc.version` and reports it as `DaemonChild.EnvWarning`.
4. In `cmd/webbridge`'s `runSubcommand`, handle `print-env`: no arguments, `bridge.WriteEnv(stdout, os.Environ())`. Add `localProjectEnv() (*bridge.ProjectEnv, error)`, which builds `bridge.NewProjectEnv` with `os.Executable()` (symlinks resolved) and `print-env`. In the local profile's setup at `:496`, call `fleet.SetProjectEnv` with it; a failure to find the executable logs a warning and leaves children inheriting.
5. Tests:
   - In `projectchild_test.go`, `TestAProjectChildStartsWithTheProjectsEnvironment`: a `projectFleet` with a resolver whose shell is a script that prints the marker and `P6=1` gives the project child's `Child.Env` holding `P6=1`, and `GET /api/daemon`'s child (through `projectChildren`) has no warning.
   - `TestAFallbackIsReportedOnTheChild`: a shell script that exits 2 leaves `Child.Env` as the resolver's `Base` and `projectChildren` reports the warning.
   - `TestWithoutAResolverChildrenInherit`: no `SetProjectEnv` leaves `Child.Env` nil.
   - In `cmd/webbridge/main_test.go`, `TestPrintEnvWritesTheMarkerAndTheEnvironment`: with `t.Setenv("P6_PRINTED", "yes")`, `runSubcommand` of `print-env` reports it handled the call, and its output holds the marker followed later by `P6_PRINTED=yes` and a NUL. `print-env extra` is refused.
   - `TestTheLocalResolverPrintsWithThisBinary`: `localProjectEnv`'s `PrintEnv` is this executable and `print-env`.

**Verify:**
- `go test ./web/bridge/ -run 'ProjectsEnvironment|FallbackIsReported|ChildrenInherit|ProjectChild|Daemon' -count=1`
- `go test ./cmd/webbridge/ -count=1`
- `go test ./web/bridge/ -count=1` (the recipe test's known intermittent hang aside; rerun it alone before calling it a regression)

## Task 3 — the idle sweep

**Files:** `web/bridge/registry.go`, `web/bridge/fleet.go`, a new `web/bridge/idle.go`, a new `web/bridge/idle_test.go`.

1. Add `idleCandidates` and `closeIfIdle` to the registry (Contracts). `closeIfIdle` rechecks the session under `r.mu`, then calls `Close`'s body; a session that is gone, has a viewer, or was active after the cutoff reports false and closes nothing.
2. Add `SetIdleSessionTimeout`, `RunIdleSweep` and `sweepIdleSessions` in `idle.go`.
   - `sweepIdleSessions` returns nothing when the timeout is zero. Otherwise, with `cutoff = now - timeout`, for each live project child with a registry and a child: for each of `idleCandidates(cutoff)`, request `session/status` with `{"sessionId": id}` on the child's own connection, bounded by `idleStatusTimeout`. Decode only `run` and `lastRun.endedAt`. Skip the session when the request fails (logged at warn with the root and the session), when `run` is set, or when `lastRun.endedAt` (Unix milliseconds) is after the cutoff. Otherwise `closeIfIdle(ctx, id, cutoff)`. Log each close at info with the root, the session and "idle". After a child's pass, when its registry is empty, call `rearmLinger` (decision 8).
   - `RunIdleSweep` returns at once for a zero timeout, else ticks every `idleSweepInterval` and calls `sweepIdleSessions` with `time.Now()`, until `ctx` ends or the fleet's `done` closes.
3. Tests in `idle_test.go`, over `projectFleet` with a tracked session on a fake child; set a session's `lastActive` directly through the registry's map under its lock. The fake answers `session/status` from `fakeAgent.results` or `fakeAgent.handler`.
   - `TestAnIdleSessionIsClosed`: no viewer, last active an hour before `now`, a status with no run: the sweep returns the ID, the fake saw `session/close` for it, the registry no longer tracks it, and the child's linger is armed (`captureTimers`).
   - `TestASessionWithAViewerIsKept`: one viewer, otherwise idle: no status request, no close.
   - `TestARecentlyActiveSessionIsKept`: last active a minute before `now` with a 30-minute timeout: no status request.
   - `TestARunningSessionIsKept`: the status carries a `run`: no close.
   - `TestARunThatEndedRecentlyIsKept`: no `run`, and `lastRun.endedAt` a minute before `now`: no close.
   - `TestAnUnreadableStatusKeepsTheSession`: the status request answers an error: no close.
   - `TestCloseIfIdleRechecks`: a viewer attached between `idleCandidates` and `closeIfIdle` makes `closeIfIdle` report false and close nothing.
   - `TestAZeroTimeoutNeverSweeps`: `sweepIdleSessions` with a zero timeout requests nothing, and `RunIdleSweep` returns at once.
   - `TestTheSweepStopsWithTheFleet`: `RunIdleSweep` with a timeout returns after `Fleet.Close`.

**Verify:**
- `go test ./web/bridge/ -run 'Idle|IsKept|CloseIfIdle|ZeroTimeout|SweepStops' -count=1`
- `go test -race ./web/bridge/ -run 'Idle|IsKept|CloseIfIdle|SweepStops' -count=1`
- Mutation check: drop the `run` check. `TestARunningSessionIsKept` must fail. Restore it. Then drop `closeIfIdle`'s recheck. `TestCloseIfIdleRechecks` must fail. Restore it.

## Task 4 — the `[daemon]` config section

**Parallel-safe with:** Tasks 1 to 3 (it touches only `internal/app/config`, and nothing before Task 6 reads it).

**Files:** `internal/app/config/types.go`, `internal/app/config/file_types.go`, `internal/app/config/merge.go`, `internal/app/config/defaults.go`, `internal/app/config/save.go`, a new `internal/app/config/daemon_test.go`.

1. Add `DaemonConfig` and `Config.Daemon` (Contracts), and its file form with `idle_session_timeout` as a `*string`, as `web.fetch_timeout` has.
2. Merge it as `web.fetch_timeout` is merged: a non-empty string is parsed with `time.ParseDuration`, and a bad value fails the load with "parse daemon.idle_session_timeout: …". A negative duration fails the same way.
3. `Default()` sets it to 30 minutes.
4. `writeSections` writes it back as `web.fetch_timeout` is written.
5. If a package test that walks every section or field (a merge or save completeness test) fails on the new section, extend it as it asks, and name it in the record.
6. Tests in `daemon_test.go`:
   - `TestTheIdleTimeoutDefaultsToThirtyMinutes`.
   - `TestTheIdleTimeoutIsReadFromTheUserConfig`: `"10m"` gives 10 minutes, and `"0"` gives zero.
   - `TestABadIdleTimeoutFailsTheLoad`: `"soon"` and `"-1m"`.
   - `TestTheIdleTimeoutRoundTripsThroughASave`.

**Verify:**
- `go test ./internal/app/config/ -count=1`

## Task 5 — `webbridge --idle-session-timeout`

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/local_test.go`.

1. Add the flag `--idle-session-timeout` (a duration, default 30 minutes, environment variable `WEBBRIDGE_IDLE_SESSION_TIMEOUT` through `envOr` as the other flags read theirs) to `parseConfig` and a field to `config`.
2. In `applyProfile`, refuse it under the server profile when it was given ("--idle-session-timeout is for --profile local"), as `--socket` is refused, and refuse a negative value under the local profile.
3. In the local profile's setup, after `SetProjectEnv`, call `fleet.SetIdleSessionTimeout` with it and start `fleet.RunIdleSweep(ctx)` on its own goroutine.
4. Tests in `local_test.go`:
   - extend `TestTheLocalProfilesDefaults` with the 30-minute default;
   - extend `TestTheLocalProfileRefuses` with the server-profile refusal and the negative value;
   - `TestTheIdleTimeoutParses`: `--idle-session-timeout 5m` and `0` reach the config.

**Verify:**
- `go test ./cmd/webbridge/ -count=1`
- `go build ./cmd/webbridge`

## Task 6 — the unit carries `SHELL` and the timeout

**Files:** `internal/daemon/daemon.go`, `internal/daemon/units_test.go`, `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

1. Add `UnitSpec.IdleSessionTimeout` (Contracts). `Args` appends `--idle-session-timeout` and the duration's `String()` after `--marshal-bin`, always, so the unit states the value it runs with.
2. In `daemonEnv`, add `SHELL` to the variables carried when set.
3. In `daemonUnitSpec`, load the config with `config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})` and set `IdleSessionTimeout` from `Daemon.IdleSessionTimeout` (decision 9). A load error fails the install with "read the user config: …".
4. Tests:
   - in `units_test.go`, extend `TestThePlistNamesEverything` and `TestTheSystemdUnitNamesEverything` to expect the new argument pair;
   - in `daemon_test.go`, extend `TestDaemonInstallPassesExplicitPaths` to expect `SHELL` in the unit's environment when it is set, and add `TestDaemonInstallWritesTheIdleTimeout`: a user config with `[daemon] idle_session_timeout = "10m"` under the test's config directory gives a unit whose arguments carry `10m0s`, and a project config in the working directory with another value is ignored.

**Verify:**
- `go test ./internal/daemon/ ./cmd/marshal/ -count=1`

## Task 7 — `marshal daemon status` reports each child

**Files:** `internal/daemon/client.go`, `internal/daemon/client_test.go`, `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

1. Add `ChildInfo.EnvWarning` (Contracts).
2. After the daemon's line, `runDaemonStatus` prints one line per child from `daemonChildLine(child, marshalVersion)`: "child <root>: version <v>, <n> session(s)", with "(this marshal <ours>)" appended when the versions differ, and a second, indented line "environment: <warning>" when the child has one. An empty version shows as `dev`, as `daemonVersion` does.
3. Tests:
   - in `client_test.go`, `TestInfoDecodesTheEnvironmentWarning`: an answer carrying `envWarning` decodes into the child.
   - in `daemon_test.go`, extend `TestDaemonStatusReportsARunningDaemon` with two children, one with a warning and another version, and expect both lines and the warning line.

**Verify:**
- `go test ./internal/daemon/ ./cmd/marshal/ -count=1`

## Task 8 — the docs, the record and full verification

**Files:** `docs/daemon/p4-local-daemon.md` (the "Plan 6" placeholder only), `cmd/webbridge/README.md`.

1. `cmd/webbridge/README.md` has no local-profile section yet. Add `## The local profile` after "Serving over HTTPS": `--profile local`, `--socket`, `--addr off`, the defaults under `<data>/daemon`, `--idle-session-timeout` (and that `marshal daemon install` writes it from `[daemon] idle_session_timeout`), and that each project child starts with the project's environment through `webbridge print-env`, with the fallback and its warning. Keep it to the facts D1, D7 and D7a state.
2. Replace the placeholder under "### Plan 6 — the project environment and idle sessions" with the record:
   - what was built;
   - every place the plan was short, and how it was filled;
   - the smoke run below;
   - the verification results, each with its number;
   - an "Open for plan 7" line for anything a later plan must know (at least H9's attach-answer gap).

   Touch nothing else in the spec: plan 5 fills its own placeholder.
3. **The smoke run,** from the worktree, with the built binaries (`go build ./cmd/webbridge`, `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`), `MARSHAL_DATA_DIR` set to a short scratch directory, and a scratch project (a `git init`ed directory) whose `.envrc` exports a variable (only when `direnv` is installed; otherwise note that the overlay was proved by Task 1's fake):
   1. start `./webbridge --profile local --addr off --marshal-bin $PWD/marshal --idle-session-timeout 1m`;
   2. attach a new session with `curl` over the socket as plan 4's smoke run did (`POST /api/projects/attach` with the upgrade headers and `--max-time 3`), so the child starts and the session is tracked;
   3. read the child's environment with `ps eww <pid>` (macOS) or `/proc/<pid>/environ` (Linux) and confirm a variable the login shell exports is there; record the variable's name only, never values;
   4. `GET /api/daemon`: the child has no `envWarning`;
   5. wait just over a minute and a sweep: `GET /api/daemon` no longer lists the session, the bridge log has the idle close, and 30s later the child is gone;
   6. `./marshal daemon status` against a daemon installed with `marshal daemon install` is optional; if run, record its child lines.
4. Full verification, in the worktree:
   - `gofmt -l internal cmd test web` prints nothing;
   - `go vet ./...` is clean, and `GOOS=linux go vet ./web/... ./internal/daemon/ ./cmd/webbridge/` is clean;
   - `go test ./... -timeout 20m` passes apart from the known load flakes, each rerun alone;
   - `TestWebIsStdlibOnly` passes;
   - `go test -race ./web/bridge/ -skip TestRecipeRunMaxUSDSetsAPausingCapForTheRun -count=1` passes.

**Verify:** the commands above, each recorded with its result.

## Done when

- A project child the local daemon starts has the variables the user's login shell and the project's direnv export, and the bridge's location variables, and a failed resolution falls back to the bridge's environment with a warning that `GET /api/daemon` and `marshal daemon status` show.
- A changed `.envrc`, toolchain file or shell startup file changes the next child start's environment, and an unchanged one does not run the shell again.
- A daemon session with no viewer and no run ends after `daemon.idle_session_timeout`, and one with a viewer, a run, or a run that ended within the timeout does not.
- `marshal daemon install` writes `SHELL` and the configured timeout into the unit.
- Tasks 1 to 7's tests pass, their mutation checks fail as stated, and Task 8's suite passes.
