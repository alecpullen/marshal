# P4 plan 8: OAuth refresh once — implementation plan

Date: 2026-11-11. Status: ready for review.
Base: `main` at `87c0ad10` (P4 plans 5 and 6 merged). Every anchor below was checked there. The full suite ran there (`TMPDIR=/private/tmp/p4ct`, `-timeout 20m`): 121 packages ok. The one failing package was `internal/acp`, with `TestStackPatchApplyBudget` and `TestStackRoundTripBudget` over budget under the suite's load: the known flakes. The package passed alone, and both tests passed three times in a row.
- **The spec edits are already made,** uncommitted in the main checkout, in `p4-local-daemon.md`. This plan's are D12's "The details (plan 8)" block, the Design table's plan 8 entries (the bridge, `cmd/marshal`, `internal/daemon`, `internal/app` and the new `internal/llm/provider/oauthworker` row), Migration order item 8, and Test W9's added sentence. Plan 7's are listed in [its header](2026-11-11-p4-7-trust-plan.md).
- **Plan 7 runs beside this one.** It touches `internal/app/app.go`, `runtime.go`, `servicedeps.go` and a new `clienttrust.go`, `internal/acp`, `internal/acp/service`, `internal/acpwire`, `internal/acpclient`, `internal/app/tui`, `internal/launch`, `web/bridge/registry.go`, `projectchild.go` and their tests, `cmd/marshal/main.go`, `test/daemon`, `AGENTS.md` and `docs/ARCHITECTURE.md`. This plan touches none of them. Both plans fill their own placeholder in the spec's Implementation record, and nothing else in the spec.

Approved design: [p4-local-daemon.md](../p4-local-daemon.md): D12 (with its details block), Migration order item 8, Tests W9 and W10.
Previous plans: [P4 plan 2, the local profile](2026-11-09-p4-2-local-profile-plan.md) and [P4 plan 6, the project environment and idle sessions](2026-11-11-p4-6-environment-plan.md), whose unit, flag and status-line patterns this plan follows.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** one process on the machine refreshes OAuth tokens ahead of their expiry: the daemon's helper when the installed daemon runs, else one embedded TUI.
- A lock file in the data dir names the process that refreshes.
- An embedded TUI runs its refresh workers only while it holds the lock, and takes over when the holder exits.
- `marshal daemon refresh` refreshes the user config's OAuth providers under the lock, and the installed daemon supervises it.

**Non-goals:**
- **Correctness does not depend on the refresh.** A provider still refreshes an expired token inline when it is used (`oauthworker`'s package comment). A process that does not hold the lock reads tokens from the keychain as it does today.
- **No change to `marshal acp` children,** which run no refresh workers, or to daemon-mode TUIs, which start no runtime.
- **No new config setting.** The helper reads the user config's providers, which are user-global only.
- **No TUI change.**
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p4-8-suite`. Create the directory first: `mkdir -p /private/tmp/p4-8-suite`. Keep the path short: a long one breaks the unix-socket tests in `web/bridge` and `cmd/webbridge`.
- **`web/` is standard library only** (`TestWebIsStdlibOnly`, `web/bridge/boundary_test.go`). Nothing below imports `marshal/internal/...` from `web/` or `cmd/webbridge`.
- **The refresh workers today.** `buildOAuthWorkers(cfg, logger, auth...)` (`internal/app/app.go:2536`) returns one `oauthworker.Worker` per provider whose `Type` is `openai_codex` and whose `Auth` is `oauth`, built through the auth resolver; it is unexported, and this plan calls it from a new file in the same package without editing `app.go`. `startWorker` (`app.go:254`) runs one worker on a wait group. `Embedded.Start` (`internal/app/embedded.go:164`) starts the workers of every runtime it builds at `:191`, on the runtime's `workCtx`, tracked by `Embedded.workers` (`:47`), which nothing waits on. `Resume` (`:240`) and `TakeTrustReload` (`:250`) close the runtime before the next `Start`; `Finish` (`:270`) quiesces, which cancels `workCtx`. No other process runs refresh workers.
- **The worker** (`internal/llm/provider/oauthworker/worker.go`): `Worker` (`:46`) and `Run` (`:105`) return when the context ends; with no token stored it rechecks every 30s. The package has no `TestMain`.
- **The model for a lease** is `internal/index`: `TryLease` (`lease.go:23`) opens the lock file and takes a non-blocking `flock`, returning `(release, held, err)`; `lease_unix.go` (`//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris`) and `lease_other.go` split the platform; `TestTryLeaseExcludesASecondHolderUntilReleased` (`lease_test.go:18`) shows that a second open of the file in one process is excluded. The model for holding one is `indexSupervisor.run` (`internal/app/host_index.go:38`): try, work while held, retry while another holder has it, and work without the lease when it cannot be taken for any other reason.
- **The auth resolver.** `provider.DefaultAuthResolver()` (`internal/llm/provider/auth.go:18`) is the production keychain; `provider.NewAuthResolver(open)` (`:35`) builds an isolated one over in-memory stores. `effectiveAuthResolver` (`app.go:373`) fails closed on nil, and `ErrAuthResolverRequired` (`app.go:153`) is the refusal for an explicit nil.
- **Config.** `config.DataDir(home)` (`internal/app/config/locations.go:36`), `config.UserConfigPath(home)` (`:23`), and `config.Load(LoadOptions{HomeDir, SkipProjectConfig: true})`, which loads the defaults and the user config only, as `daemonUnitSpec` uses it (`cmd/marshal/daemon.go:114`).
- **`cmd/marshal`.** `main` passes `context.Background()` to `run` (`main.go:37`); only `launch.Run` installs a signal context (`internal/launch/launch.go:79`). `runDaemon` dispatches the `daemon` subcommands (`daemon.go:55`, the switch at `:60`); `runDaemonStatus` (`:273`), `daemonInfoLine` (`:333`), `daemonChildLine` (`:355`); the seams `daemonManagerFor` and `daemonClientFor` (`:26`, `:30`). Tests in `daemon_test.go`: `TestDaemonInstallPassesExplicitPaths` (`:124`), `TestDaemonStatusReportsARunningDaemon` (`:295`), `TestDaemonChildLine` (`:415`), `TestDaemonRejectsUnknownSubcommands` (`:492`).
- **`internal/daemon`.** `Paths` and `PathsFor` (`daemon.go:20`, `:25`), `UnitSpec` and `UnitSpec.Args` (`:36`, `:46`), rendered by `LaunchdPlist` and `SystemdUnit` (`units.go`); `Info` (`client.go:41`). Tests: `units_test.go`'s `TestThePlistNamesEverything` and `TestTheSystemdUnitNamesEverything`, and `client_test.go`.
- **The bridge.** `DaemonInfo` (`web/bridge/daemoninfo.go:9`), `Server.SetDaemonInfo` (`:29`) and the handler `Server.daemon` (`:36`), which copies the info and fills the per-request fields; the `Server` struct holds `daemonInfo` (`http.go:46`). `Child.supervise` (`child.go:359`) restarts an exited child after `restartBackoff` (`:35`), and is the model for a restart loop, not code to reuse.
- **`cmd/webbridge`** (`main.go`): `parseConfig` defines the flags; the duration flags parse their variable once before declaring the flag (`:151`); `config` (`:59`) holds `marshalBin` (`:69`); `applyProfile` (`:251`) refuses a local-only setting under the server profile through `given.given` (`:261`); the local profile's fleet is set up at `:539` and its socket at `:594`, where `SetDaemonInfo` is called. `run` (`:447`) returns when its context ends. Tests: `local_test.go`'s `isolateWebbridgeEnv` (`:22`), `TestTheLocalProfilesDefaults` (`:88`), `TestTheLocalProfileRefuses` (`:123`) and `TestTheLocalProfileServesItsSocket` (`:194`), which runs `run` with the default `--marshal-bin` of `marshal`. That is why the helper must be off unless asked (decision 5): with it on, these tests would execute whatever `marshal` is on the developer's `PATH`, against the developer's keychain.

**Decisions this plan makes beyond the spec** (D12's details block records them):

1. **The lease lives with the worker,** in `oauthworker`, beside `Worker`, shaped as `index.TryLease` is: `TryLease(dataDir)` on `oauth-refresh.lock`, created `0600`.
2. **Holding the lease is one helper in `internal/app`,** `oauthLease.hold`, used by both the embedded runtime and the daemon's helper. It retries every 30s while another process holds the lease, and runs the work without it when the lease cannot be taken for any other reason, as `indexSupervisor.run` does.
3. **An embedded runtime takes the lease only when it has a worker.** Most users have no OAuth provider, and a TUI that held the lock for nothing would keep the daemon's helper from refreshing after a provider is added.
4. **The handoff inside one process is a wait, not a retry.** A process's next runtime waits up to 2s for the previous runtime's work to return and release the lease, because a second open of the lock file in the same process is excluded too, and the 30s retry would leave a gap after every resume.
5. **The daemon starts the helper only when told to.** `webbridge --oauth-refresh` (local profile only) supervises `<marshal-bin> daemon refresh`, and `marshal daemon install` always writes the flag into the unit. A bridge run by hand or by a test starts no helper.
6. **The helper polls the user config** every 5s by modification time and size (or "absent"), rather than watching it: an editor's atomic rename replaces the file a watch was on. A change rebuilds the workers under the same lease, and a config that names no OAuth provider releases it.
7. **The helper logs to standard error,** which the bridge passes through to its own, so the unit's log holds both.
8. **The bridge reports the helper** on `GET /api/daemon` as `refresh` (its process ID and restart count), and `marshal daemon status` prints a `refresh:` line, so a user can tell whether the daemon refreshes.
9. **`marshal daemon refresh` installs its own signal context.** `main` passes a background context, and the helper must release the lease and stop its workers when the bridge sends SIGTERM.

**Execution:**
- In the main checkout, if `git status` shows any of these uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon: record P4 plans 7 and 8`:
  - `docs/daemon/p4-local-daemon.md`;
  - this plan and `docs/daemon/plans/2026-11-11-p4-7-trust-plan.md`.

  Plan 7's executor may have committed them already. The main checkout holds other sessions' work, uncommitted files under `internal/db`, `internal/filetrack` and `internal/tools/native` among them, and untracked ones (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them all alone.
- Then branch: `git switch -c p4-8-refresh` from `main`, or create a worktree on that branch.
- Run the tasks in order, except where a task says it is parallel-safe. Run each task's Verify and commit it as `p4-8-refresh: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The lease (`internal/llm/provider/oauthworker`)

```go
// lease.go
// LeaseFile is the lock file, in the data dir, held by the one process that
// refreshes OAuth tokens (P4 D12).
const LeaseFile = "oauth-refresh.lock"

// TryLease tries to take the refresh lease in dataDir without blocking. held
// is false, with a nil error, when another holder has it. Any other failure
// (the file cannot be opened, or the platform has no lock binding) is
// returned as err. release is non-nil only when held is true; it is safe to
// call more than once.
func TryLease(dataDir string) (release func(), held bool, err error)

// lease_unix.go (the build tag of internal/index/lease_unix.go) and
// lease_other.go (its negation), as internal/index splits them.
func tryFlock(f *os.File) (bool, error)
func releaseFlock(f *os.File) error
```

### Holding it (`internal/app/oauthrefresh.go`)

```go
const (
	oauthLeaseRetry  = 30 * time.Second
	oauthConfigPoll  = 5 * time.Second
	oauthHandoffWait = 2 * time.Second
)

// oauthLease holds the refresh lease around work (decision 2).
type oauthLease struct {
	tryLease func() (release func(), held bool, err error)
	retry    time.Duration
	log      *slog.Logger
}

// hold runs work while the lease is held, trying every retry until it is. It
// returns once ctx has ended and work has returned, with the lease released.
// A lease that cannot be taken for a reason other than another holder is
// logged, and work runs without it.
func (l oauthLease) hold(ctx context.Context, work func(ctx context.Context))

// runWorkers starts each worker on ctx and returns once every one has
// returned.
func runWorkers(ctx context.Context, workers []worker.Worker, log *slog.Logger)

// configStamp is path's modification time and size, or "absent".
func configStamp(path string) string

// OAuthRefreshOptions configures RunOAuthRefresh.
type OAuthRefreshOptions struct {
	HomeDir string                // required: whose user config and data dir
	Auth    provider.AuthResolver // required: the credential store the workers refresh through
	Logger  *slog.Logger          // nil means slog.Default()

	// Test seams; their zero values are the production ones.
	tryLease func() (release func(), held bool, err error)
	workers  func(cfg config.Config) []worker.Worker
	retry    time.Duration
	poll     time.Duration
}

// RunOAuthRefresh is `marshal daemon refresh` (P4 D12): it refreshes the user
// config's OAuth providers under the refresh lease until ctx ends.
func RunOAuthRefresh(ctx context.Context, opts OAuthRefreshOptions) error

// internal/app, on Embedded:
	// refreshDone is closed when the current runtime's refresh work has
	// returned, its lease released; nil until a runtime has a worker.
	refreshDone chan struct{}
	// leaseRetry is how often a runtime retries a held lease; zero means
	// oauthLeaseRetry. Tests shorten it.
	leaseRetry time.Duration
```

### The helper's supervisor (`web/bridge/helper.go`)

```go
// Helper runs one long-lived helper process and restarts it when it exits
// (P4 D12's `marshal daemon refresh`). The zero values of its tuning fields
// are the production defaults.
type Helper struct {
	Argv        []string      // the command and its arguments
	Stderr      io.Writer     // the helper's standard error; nil discards it
	Backoff     time.Duration // the first restart's delay; zero means helperBackoff
	MaxBackoff  time.Duration // the longest delay; zero means helperMaxBackoff
	SteadyRun   time.Duration // a run this long resets the delay; zero means helperSteadyRun
	StopTimeout time.Duration // SIGTERM to SIGKILL; zero means helperStopTimeout

	mu       sync.Mutex
	pid      int
	restarts int
}

const (
	helperBackoff     = time.Second
	helperMaxBackoff  = time.Minute
	helperSteadyRun   = time.Minute
	helperStopTimeout = 5 * time.Second
)

// Run starts the helper and keeps it running until ctx ends, then stops it
// and returns once it has exited.
func (h *Helper) Run(ctx context.Context)

// State is the running helper's process ID (zero between runs) and how many
// times it was restarted.
func (h *Helper) State() (pid, restarts int)

// on DaemonInfo:
	Refresh *DaemonRefresh `json:"refresh,omitempty"` // filled per request

// DaemonRefresh is the refresh helper as GET /api/daemon reports it.
type DaemonRefresh struct {
	PID      int `json:"pid"`
	Restarts int `json:"restarts"`
}

// SetRefreshHelper makes GET /api/daemon report h.
func (s *Server) SetRefreshHelper(h *Helper)

// on Server (http.go):
	refresh *Helper
```

### The unit, the client and the commands

```go
// internal/daemon, on Info:
	Refresh *RefreshInfo `json:"refresh,omitempty"`

// RefreshInfo is the refresh helper the daemon supervises.
type RefreshInfo struct {
	PID      int `json:"pid"`
	Restarts int `json:"restarts"`
}

// cmd/marshal/daemon.go
// oauthRefreshRunner is the helper's body; tests replace it.
var oauthRefreshRunner = app.RunOAuthRefresh

// runDaemonRefresh implements `marshal daemon refresh`.
func runDaemonRefresh(ctx context.Context, args []string, stderr io.Writer) error

// daemonRefreshLine renders info's refresh helper for `marshal daemon status`.
func daemonRefreshLine(info daemon.Info) string

// cmd/webbridge, on config:
	oauthRefresh bool
```

## Task 1 — the lease

**Files:** a new `internal/llm/provider/oauthworker/lease.go`, a new `lease_unix.go`, a new `lease_other.go`, a new `lease_test.go` and a new `main_test.go`, all in `internal/llm/provider/oauthworker`.

1. `TryLease` as `index.TryLease` is, on `LeaseFile` in `dataDir`, created with mode `0600`. It creates `dataDir` (`0700`) when it is missing, since a first run may have no data dir yet. The two platform files copy `internal/index`'s.
2. `main_test.go` runs the package through `testenv.Run`. When the environment variable `OAUTHWORKER_LEASE_HOLDER` names a directory, it instead takes the lease there, prints `held` on its own line to standard output, and blocks until its standard input closes; that is the second process the tests start.
3. Tests in `lease_test.go`:
   - `TestTheLeaseExcludesASecondHolderUntilReleased`: two `TryLease` calls on one directory: the second reports not held with no error until the first releases; a second release is harmless.
   - `TestTheLeaseFileIsPrivate`: the file is `0600`, in a data dir created `0700`.
   - `TestTheLeaseReportsAnUnopenableFile`: a `dataDir` that is a regular file returns an error, not "not held".
   - `TestAHolderExitingLetsAnotherTakeOver` (W9): start this test binary with `OAUTHWORKER_LEASE_HOLDER` set, read `held`, see `TryLease` report not held, kill the process without letting it clean up, and see `TryLease` take the lease within 5s.

**Verify:**
- `go test ./internal/llm/provider/oauthworker/ -count=1`
- `GOOS=windows go vet ./internal/llm/provider/oauthworker/` is clean.

## Task 2 — holding the lease

**Files:** a new `internal/app/oauthrefresh.go`, a new `internal/app/oauthrefresh_test.go`.

1. `oauthLease.hold` (decision 2): try the lease. Held: run `work` on `ctx`, and release after it returns. Held elsewhere: wait `retry` or for `ctx` to end, then try again. Any other error: log it once at warn with the error and run `work` on `ctx` without the lease. Return only after `work` has returned, or at once when `ctx` ended before the lease was taken.
2. `runWorkers` starts each worker as `startWorker` does, on its own wait group, and waits for all of them.
3. `configStamp` reads `os.Stat`: the modification time in nanoseconds and the size, or "absent" when the file does not exist.
4. `RunOAuthRefresh`:
   1. Refuse an empty `HomeDir`, and a nil `Auth` with `ErrAuthResolverRequired`.
   2. The defaults: the lease is `oauthworker.TryLease` on `config.DataDir(HomeDir)`, the workers are `buildOAuthWorkers` with the options' logger and auth, `retry` is `oauthLeaseRetry`, and `poll` is `oauthConfigPoll`. The config path is `config.UserConfigPath(HomeDir)`.
   3. Loop until `ctx` ends. Read the stamp and load the user config (`config.Load` with the home and `SkipProjectConfig`); a load that fails is logged at warn and counts as naming no provider. With no workers, wait until the stamp changes (checking every `poll`) or `ctx` ends, and loop.
   4. With workers, `hold` the lease around the work. The work loads the config again (it may have changed while the lease was held elsewhere) and builds the workers. With none it returns at once. Otherwise it runs them on a child context and checks the stamp every `poll`. On a change it stops them, waits for them, loads the config and builds again, and starts the new set, all under the same lease. A rebuilt set that is empty ends the work, which releases the lease (decision 6).
   5. Log at info when the lease is taken and when it is released, each with the providers' count, never a token or a provider's settings.
   6. Return nil when `ctx` ends.
5. Tests in `oauthrefresh_test.go`, with a fake lease whose holder the test controls (held, held elsewhere, or an error), fake workers that record their start and stop, a short `retry` and `poll`, and a user config written under a temporary home:
   - `TestHoldRunsTheWorkWhileTheLeaseIsHeld`: the work runs, and the lease is released after `ctx` ends and the work returns.
   - `TestHoldWaitsForAnotherHolder`: held elsewhere, no work runs; once the fake lets go, the work runs within a few retries.
   - `TestHoldRunsWithoutALeaseItCannotTake`: an error from the lease runs the work without it, and the warning is logged once.
   - `TestTheHelperRunsTheConfigsProviders`: a config with one OAuth provider runs one worker under the lease.
   - `TestTheHelperRebuildsOnAChange`: adding a second provider to the file, with its modification time moved forward, stops the first set and starts two workers, without releasing the lease in between.
   - `TestTheHelperReleasesWhenNoProviderIsLeft`: removing the provider stops the workers and releases the lease, and adding it back takes the lease again.
   - `TestTheHelperRefusesWithoutAHomeOrAnAuth`: both refusals.

**Verify:**
- `go test ./internal/app/ -run 'TestHold|TestTheHelper' -count=1`

## Task 3 — embedded mode runs its workers under the lease

**Files:** `internal/app/embedded.go`, a new `internal/app/embedded_refresh_test.go`.

1. In `Start`, replace the loop at `:191`. Build the runtime's workers. With none, take no lease and touch no file (decision 3). With some:
   1. Wait for the previous runtime's `refreshDone`, when there is one, up to `oauthHandoffWait` (decision 4). A wait that runs out is logged and does not fail the start.
   2. Make a new `refreshDone` and start the refresh on its own goroutine, tracked by `e.workers`: `oauthLease.hold` on the runtime's `workCtx`, with `oauthworker.TryLease` on `config.DataDir` of the embedding's home, `leaseRetry` (else `oauthLeaseRetry`), and the runtime's logger, around `runWorkers` of those workers. Close `refreshDone` when it returns.
2. Update the `workers` field's comment and the worker-lifecycle comment in `Start`: the workers run only under the lease, and the lease is released when the runtime's work context ends.
3. Tests in `embedded_refresh_test.go`. Each builds an `Embedded` over a temporary home and project with an isolated auth resolver (`provider.NewAuthResolver` over in-memory stores) and a config loader, as `embedded_test.go`'s tests do. The OAuth case's config has one provider with `Type` `openai_codex` and `Auth` `oauth`; with no token stored its worker only waits.
   - `TestAnEmbeddedRuntimeWithoutOAuthTakesNoLease`: after `Start`, no lock file exists in the data dir.
   - `TestAnEmbeddedRuntimeHoldsTheLeaseWhileItRuns`: after `Start`, `oauthworker.TryLease` from the test reports not held; after `Finish`, it takes the lease within 2s.
   - `TestASecondEmbeddingWaitsAndTakesOver` (W9): two embeddings on one home, the second with a short `leaseRetry`. While the first runs, the second's lease stays unheld; after the first finishes, the test's own `TryLease` stays unheld within a few retries, because the second has taken it.
   - `TestAResumeHandsTheLeaseOver`: `Resume` and then `Start` on the same embedding leaves the lease held by the new runtime, with no gap a test `TryLease` could take.

**Verify:**
- `go test ./internal/app/ -run 'Embedded|Lease|Resume' -count=1`
- `go test ./internal/app/ -count=1`
- Mutation check: take the lease whether or not there is a worker. `TestAnEmbeddedRuntimeWithoutOAuthTakesNoLease` must fail. Restore it.

## Task 4 — `marshal daemon refresh`

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

1. Add `refresh` to `runDaemon`'s switch, calling `runDaemonRefresh`. Leave it out of the usage hint, since the daemon runs it, and say so in `runDaemon`'s doc comment.
2. `runDaemonRefresh`: no arguments (an argument is refused, naming it); resolve the home as the other subcommands do (`daemonHome`); install `signal.NotifyContext` for `os.Interrupt` and `syscall.SIGTERM` on the given context (decision 9); a text logger on standard error at info (decision 7); then `oauthRefreshRunner` with the home, `provider.DefaultAuthResolver()` and the logger. Return its error.
3. Tests in `daemon_test.go`, replacing `oauthRefreshRunner` for the test and restoring it:
   - `TestDaemonRefreshRunsTheHelper`: `marshal daemon refresh` calls the runner once with the test's home and a non-nil auth resolver; the fake returns when its context ends, and the test cancels the context it passed in.
   - `TestDaemonRefreshRefusesArguments`: `daemon refresh extra` is refused and the runner is not called.
   - `TestDaemonRejectsUnknownSubcommands` still passes, and its usage text still lists five subcommands.

**Verify:**
- `go test ./cmd/marshal/ -run 'DaemonRefresh|DaemonRejects' -count=1`
- `go test ./cmd/marshal/ -count=1`

## Task 5 — the bridge supervises a helper

**Parallel-safe with:** Tasks 1 to 4 (it touches only `web/bridge`).

**Files:** a new `web/bridge/helper.go`, a new `web/bridge/helper_test.go`, `web/bridge/daemoninfo.go`, `web/bridge/http.go` (the `Server` field only).

1. `Helper.Run`:
   - Start `Argv` with `Stderr` as its standard error, no standard input or output, and the bridge's environment and working directory. Record its process ID.
   - When it exits while `ctx` is live, log at warn with its exit status, clear the process ID, count a restart, and wait the delay before starting it again: `Backoff` at first, doubling up to `MaxBackoff`, and back to `Backoff` after a run that lasted at least `SteadyRun`. A start that fails counts as an exit. A `ctx` that ends during the wait returns at once.
   - When `ctx` ends while it runs, send SIGTERM; when it has not exited after `StopTimeout`, or when the signal cannot be sent, kill it. Return after it has exited.
   - `State` reads both values under `mu`.
2. `SetRefreshHelper` stores the helper on the server. `Server.daemon` fills `Refresh` from `State` when a helper is set, and leaves it out otherwise.
3. Tests in `helper_test.go`, with `/bin/sh -c` scripts and short tuning fields. Skip them where `/bin/sh` does not exist.
   - `TestTheHelperRuns`: a script that sleeps reports a process ID, and no restart.
   - `TestAnExitingHelperIsRestarted`: a script that exits at once is restarted, with growing delays capped at `MaxBackoff`, and its restart count rises.
   - `TestTheHelperStopsWithTheContext`: cancelling the context sends SIGTERM to a script that traps it and exits, and `Run` returns before `StopTimeout`.
   - `TestAHelperIgnoringSIGTERMIsKilled`: a script that ignores SIGTERM is killed after `StopTimeout`, and `Run` returns.
   - `TestTheDaemonReportsItsHelper`: `GET /api/daemon` on a server with daemon info and a running helper answers `refresh` with its process ID; without a helper it has no `refresh`.

**Verify:**
- `go test ./web/bridge/ -run 'Helper|ReportsItsHelper' -count=1`
- `go test ./web/bridge/ -run TestWebIsStdlibOnly -count=1`
- `GOOS=windows go vet ./web/bridge/` and `GOOS=linux go vet ./web/...` are clean.
- Mutation check: drop the kill after `StopTimeout`. `TestAHelperIgnoringSIGTERMIsKilled` must fail by timing out. Restore it.

## Task 6 — `webbridge --oauth-refresh`

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/local_test.go`.

1. Add a boolean flag `--oauth-refresh` with the variable `WEBBRIDGE_OAUTH_REFRESH`, parsed once with `strconv.ParseBool` before the flag is declared, as the duration flags parse theirs; a variable that does not parse fails `parseConfig`. An empty variable means false. Its usage text says it runs `<marshal-bin> daemon refresh` (local profile only).
2. In `applyProfile`, refuse it under the server profile when it was given, as `--idle-session-timeout` is refused (`:261`).
3. In the local profile's setup, when the flag is on, build a `bridge.Helper` whose `Argv` is `marshalBin`, `daemon`, `refresh`, with the bridge's standard error; run it on its own goroutine on the run's context; and call `api.SetRefreshHelper` beside `SetDaemonInfo`. Before `run` returns, wait for the helper's `Run` to return, so the bridge never leaves a helper behind.
4. Add the variable to `isolateWebbridgeEnv`.
5. Tests in `local_test.go`:
   - `TestTheLocalProfilesDefaults` checks the flag is off by default.
   - `TestTheLocalProfileRefuses` gains `--oauth-refresh` under the server profile, and an unparsable `WEBBRIDGE_OAUTH_REFRESH`.
   - `TestTheLocalProfileSupervisesTheRefreshHelper`: `run` with `--profile local --addr off --oauth-refresh --marshal-bin <script>`, where the script writes its arguments to a file and sleeps. `GET /api/daemon` answers `refresh` with a process ID, the file holds `daemon refresh`, and after the context is cancelled `run` returns within 10s and the process is gone.
   - `TestTheLocalProfileStartsNoHelperUnasked`: without the flag, `GET /api/daemon` has no `refresh`, and the script was never run.

**Verify:**
- `go test ./cmd/webbridge/ -count=1`
- `go build ./cmd/webbridge`

## Task 7 — the unit and the status

**Files:** `internal/daemon/daemon.go`, `internal/daemon/client.go`, `internal/daemon/units_test.go`, `internal/daemon/client_test.go`, `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

1. `UnitSpec.Args` ends with `--oauth-refresh`, after `--idle-session-timeout` (decision 5). The installed daemon always refreshes, so `UnitSpec` gains no field.
2. Add `Refresh` and `RefreshInfo` to `Info` (Contracts).
3. `daemonRefreshLine`: `refresh: running (pid <n>)`, with `, restarted <k> time(s)` (real plurals: `1 time`, `3 times`) when the count is not zero, and `refresh: off` when the info has none. `runDaemonStatus` prints it after the daemon's line, before the children's.
4. Tests:
   - In `units_test.go`, `TestThePlistNamesEverything` and `TestTheSystemdUnitNamesEverything` check `--oauth-refresh` is the last argument.
   - In `client_test.go`, the info decoding test reads a `refresh` object.
   - In `daemon_test.go`, `TestDaemonInstallPassesExplicitPaths` checks the argument, a new `TestDaemonRefreshLine` covers the three shapes, and `TestDaemonStatusReportsARunningDaemon` checks the line.

**Verify:**
- `go test ./internal/daemon/ ./cmd/marshal/ -count=1`

## Task 8 — the docs, the record and full verification

**Files:** `cmd/webbridge/README.md`, `docs/daemon/p4-local-daemon.md` (the "Plan 8" placeholder only).

1. In `cmd/webbridge/README.md`'s "The local profile" section, add a paragraph: `--oauth-refresh` supervises `marshal daemon refresh`, which holds `<data>/oauth-refresh.lock` and refreshes the user config's OAuth providers; `marshal daemon install` writes the flag; an embedded TUI refreshes only while it holds the same lock. State D12 only.
2. The smoke run. Use a scratch data dir (`export MARSHAL_DATA_DIR=$(mktemp -d /private/tmp/p4-8-data.XXXX)`; its `daemon` directory created `0700`, which the bridge requires), a scratch `HOME` whose user config names one provider with `type = "openai_codex"` and `auth = "oauth"`, and the built `marshal` and `webbridge`. The provider has no stored token, so its worker only waits, and nothing reaches a token endpoint.
   - `./webbridge --profile local --addr off --oauth-refresh --marshal-bin $PWD/marshal`. The bridge's standard error shows the helper taking the lease, and `GET /api/daemon` over the socket answers `refresh` with a process ID.
   - A second `./marshal daemon refresh` started by hand logs that another process holds the lease, and takes it within about 30s of the bridge stopping (Ctrl+C on the bridge).
   - `./marshal daemon status` against an installed service was not available in plan 6's run. Record whether it is this time.
   - The keychain reached from a user service (the launchd agent on macOS, the user's session bus for a systemd user unit) is not exercised by a token-less provider. Record that, and whether a real signed-in provider was tried.
3. Fill the "Plan 8" placeholder in the spec's Implementation record: what was built, where the plan was short and how it was filled, the smoke run, the verification with each command's result, and what is open for later plans.
4. Full verification, each result recorded:
   - `gofmt -l internal cmd test web` prints nothing;
   - `go vet ./...` is clean, and so are `GOOS=linux go vet ./web/... ./internal/daemon/ ./cmd/webbridge/ ./internal/llm/provider/oauthworker/` and `GOOS=windows go vet ./web/bridge/ ./internal/llm/provider/oauthworker/`;
   - `go test ./... -timeout 20m`, every failure named, and compared with the base commit's two `internal/acp` load flakes;
   - `go test ./web/bridge/ -run TestWebIsStdlibOnly -count=1`;
   - `go test -race ./internal/app/ -run 'TestHold|TestTheHelper|Lease' -count=1` and `go test -race ./web/bridge/ -run Helper -count=1`, with no data race;
   - `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`, then `go test ./test/usability/... -run TestScripted -timeout 5m`.

**Verify:** the commands above, each recorded with its result.

## Done when

- An embedded TUI with an OAuth provider refreshes only while it holds `<data>/oauth-refresh.lock`, and one without never touches the file.
- `marshal daemon refresh` refreshes the user config's OAuth providers under the lock, follows changes to the config, and stops cleanly on SIGTERM.
- The installed daemon supervises the helper; a bridge run without `--oauth-refresh` starts none.
- `GET /api/daemon` and `marshal daemon status` say whether the helper runs.
- W9 and W10 pass, and the record is filled.
