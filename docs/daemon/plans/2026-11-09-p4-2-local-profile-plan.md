# P4 plan 2: The local profile — implementation plan

Date: 2026-11-09. Status: ready for review.
Base: `main` at `40dc0eff` (P3 plan 8 merged). Every anchor below was checked there. The full suite ran there (`TMPDIR=/private/tmp/p4-b`, `-timeout 20m`): 119 packages ok, `web/bridge` included. The one failure was `internal/acp`'s `TestStackRoundTripBudget`, a known load flake, which passed three times when rerun alone.
- **The spec edits are already made,** uncommitted in the main checkout:
  - in `p4-local-daemon.md`: the header, "Decisions", D1, D4, D6, the Design table, Migration order items 1 and 2, W6, W7, and an Implementation record with a placeholder for each of plans 1 and 2;
  - in `design.md`: §4.4, §6.5, the P4 row and §8;
  - a pointer in `p3-acp-transport.md`.
- **Plan 1 runs beside this one.** It touches `internal/acpwire`, `internal/acp`, `internal/acp/service` and `internal/app/tui`, none of which this plan touches. Both plans fill their own placeholder in the spec's Implementation record.

Approved design: [p4-local-daemon.md](../p4-local-daemon.md): Goal, Decisions 2 and 7, D1 (the local profile), the Design table's `cmd/webbridge`, `cmd/marshal`, `internal/daemon` and `internal/app/config` rows, Migration order item 2, Tests W7 and W10.
Previous plan: [P3 plan 8, the switch](2026-11-09-p3-8-switch-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the user can install, start, stop, inspect and remove a local daemon: the bridge in its local profile, run as a user service, serving its API on a unix socket that only the user can reach.
- `webbridge --profile local` serves the bridge's routes on a unix socket, with no token, after checking each peer's user ID. It starts no container, egress or secrets machinery.
- `marshal daemon install|start|stop|status|uninstall` manages it as a launchd agent (macOS) or a systemd user unit (Linux).
- `GET /api/daemon` says which daemon answers.

**Non-goals:**
- **No project children, relay or attach.** The daemon serves the bridge's existing routes only. A TUI cannot reach a session through it yet (plans 3 to 5).
- **No change to the server profile.** Without `--profile local`, `webbridge` parses, starts and serves exactly as today, and every existing `cmd/webbridge` and `web/bridge` test passes unchanged.
- **No browser on the daemon.** The unit runs with `--addr off`. A TCP listener with its token on the daemon is P5's (D1).
- **No environment resolution, trust, pins or OAuth.** The unit carries the installing shell's `PATH` and location variables until plan 6 resolves each project's environment.
- **No changelog entry** until the TUI can use the daemon (plan 5).

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p4-2-suite`. Create the directory first: `mkdir -p /private/tmp/p4-2-suite`. Keep the path short: a unix socket path is capped at 104 bytes on macOS, and the new socket tests make sockets under the test's temporary directory.
- **`webbridge` today** (`cmd/webbridge/main.go`).
  - `config` (`:53`) and `parseConfig` (`:100`) resolve flags over `WEBBRIDGE_*` variables (`envOr`, `:266`) over defaults:
    - `--addr` defaults to `127.0.0.1:7700` (`:103`);
    - `--state-dir` to `/state` (`:115`);
    - `--secrets` to `env` (`:123`);
    - `--egress` to `on` (`:129`);
    - the workspace to `bridge.DefaultWorkspacePath()` (`:167`, which is `~/.config/marshal/fleet.json`, `web/bridge/workspace.go:295`);
    - `--cwd-root` to the working directory (`:174`).
  - `run` (`:297`):
    - generates and prints a token when none is given (`:328`);
    - loads the workspace (`:337`) and adds `--project` and `--cwd-root` as projects;
    - builds the fleet (`:373`) and its secrets backend;
    - starts egress when `on` (`:384`);
    - reattaches agents, and starts the poller and the scheduler;
    - builds `bridge.NewServer(fleet, token, publicURL)` (`:405`);
    - listens on TCP (`:415`), and starts the preview listener unless `--preview-addr off` (`:428`);
    - reconciles worktrees;
    - waits for the signal context (`:455`), then shuts the HTTP servers down (`:476`) and closes the fleet.
  - The tests (`main_test.go`) call `parseConfig` and `run` directly. `versionString` is the `--version` banner, and `version` is stamped by the release.
- **The bridge's container probe.** `detectedRuntime` (`web/bridge/fleet.go:691`) runs `docker info`, then `podman info`, once per process (`probeRuntime`, `:673`). Six places call it:
  - the agent factory in `NewFleet` (`:391`), which falls back to a host process when it reports none;
  - `buildControlChild` (`control.go:65`);
  - `egressRuntimeName` (`egress_fleet.go:44`);
  - `ensureDerivedImage` (`derive.go:107`);
  - `runtimeName` (`wsbuild.go:250`);
  - `runtimeInfo` (`wsspawn.go:60`).

  Three of them answer "docker" first when the fleet has a fake `runner` (tests). `NewFleet` is `fleet.go:329`. The fleet's injectable fields are instance fields, such as `runner` (`:137`), `egressRuntime` (`:189`) and `newControl` (`:250`).
- **The bridge's HTTP server** (`web/bridge/http.go`).
  - `NewServer` (`:46`) wraps the mux in `bearerAuth` (`auth.go:14`) when a token is set.
  - `ServeHTTP` (`:98`) routes the public status and hook prefixes, refuses previews, serves the SPA for non-API paths, and sends `/api/` and `/mcp` through the wrapped mux.
  - `routes` (`:124`) registers every route. `GET /api/config` (`:334`) and `GET /api/projects` answer `fleet.ProjectStatus()`. The workspace's projects are `Workspace.Projects()`.
  - Nothing reports the bridge's version.
- **`web/` is standard library only**, test files included (`TestWebIsStdlibOnly`, `web/bridge/boundary_test.go:21`). Linux's `syscall.GetsockoptUcred` reads `SO_PEERCRED`. Darwin's `syscall` has no reader for `LOCAL_PEERCRED`, but `syscall.Syscall6` with `syscall.SYS_GETSOCKOPT` (118 on darwin) reaches `getsockopt` directly. Both binaries ship for linux and darwin only (`.goreleaser.yaml`).
- **`marshal acp --listen`'s socket** (`internal/acp/listen.go:34`) makes the directory `0700`, removes the old socket without checking whether something listens on it (`:40`), listens, and makes the socket `0600`.
- **`marshal`'s subcommands** (`cmd/marshal/main.go`). `run` (`:40`) parses `--trust` and `--version`, then dispatches `history`, `calibrate-tokens`, `acp`, `plugin` and `snapshots` (`:72` to `:100`) through package variables such as `snapshotsRunner` (`:26`), which `main_test.go` swaps (`:136`). `runSnapshots` (`snapshots.go:35`) is the subcommand shape to copy.
- **The data directory.** `config.DataDir(home)` (`internal/app/config/locations.go:36`): `MARSHAL_DATA_DIR`, else `$XDG_DATA_HOME/marshal`, else `~/.local/share/marshal`. `internal/testenv.Run` points these variables at a temporary directory for a package's tests (AGENTS.md).
- **The service managers.**
  - **launchd** loads a user agent with `launchctl bootstrap gui/<uid> <plist>` and unloads it with `launchctl bootout gui/<uid>/<label>`. It restarts a loaded one with `launchctl kickstart -k gui/<uid>/<label>`, and describes one with `launchctl print gui/<uid>/<label>`, whose output carries `state = running` and `pid = <n>` lines.
  - **systemd** user units live in `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`) and are driven by `systemctl --user daemon-reload | enable --now | start | stop | restart | disable --now | show --property=ActiveState,MainPID <unit>`. `StandardOutput=append:<path>` needs systemd 240 or later.
  - Neither passes the user's shell environment to the service.

**Decisions this plan makes beyond the spec** (D1 records them):

1. **`--profile server|local`**, defaulting to `server`, which is today's bridge. `--socket` is valid only with `local`. `--addr off` is valid only with `local`, and turns the preview listener off with it.
2. **The local profile's defaults.** Each applies only when its flag and its variable are both unset:
   - socket: `<data>/daemon/marshal.sock`;
   - state directory: `<data>/daemon/state`;
   - workspace: `<data>/daemon/fleet.json`;
   - working-directory project: none.

   `<data>` is the bridge's copy of `config.DataDir`'s rule, and a test outside `web/` pins the two together.
3. **The local profile's refusals.** An `--egress on` given explicitly, as a flag or as `WEBBRIDGE_EGRESS`, is an error: "the local profile runs no egress proxy". So is a `--secrets` other than `env`. With neither given, egress is `off` and secrets `env`.
4. **Host processes only.** The fleet gains a host-only switch, set before anything starts, and an instance probe that defaults to `detectedRuntime`. A new method answers the container runtime for the fleet: none when host-only, without probing; otherwise the probe's answer. All six callers use it. The host-only check comes before the fake-runner shortcut, so a host-only fleet never answers "docker", even in tests.
5. **The socket.** A new `bridge.ListenLocal` does everything the socket needs:
   - **The directory.** It creates the socket's directory `0700`, and refuses a directory that is not the user's or whose mode allows group or other access.
   - **A live daemon.** It dials the path with a 1s timeout. When the dial connects, it refuses with "a daemon is already listening on <path>" and touches nothing.
   - **A stale socket.** When the dial is refused, or the path is not there, it removes any file at the path, listens, and makes the socket `0600`. A file at the path that is not a socket is refused, never removed.

   The returned listener removes the socket when closed.
6. **The peer check is a listener wrapper.** Its `Accept` reads each connection's peer user ID and returns only a connection whose ID matches the wanted one. It closes any other connection, and any it cannot read, logging the peer's ID at warn with no other detail. The wanted ID is a parameter, so a test can refuse its own connection.
   - Linux reads it with `GetsockoptUcred`.
   - Darwin reads `LOCAL_PEERCRED` (level `SOL_LOCAL` 0, option 1) into an `xucred` with a raw `getsockopt`, and checks its version is 0.
   - Any other system answers an error, and `--profile local` refuses to start there.
7. **The socket's handler** is `Server.SocketHandler()`. It routes exactly as `ServeHTTP` does, with the unwrapped mux in place of the token-checked one. `/mcp` keeps its own per-client auth.
8. **`GET /api/daemon`** answers `DaemonInfo` and the workspace's projects. It is registered on every server. A server with no daemon info answers 404 (`{"error":"not a daemon"}`), so the server profile's API is unchanged in effect.
9. **`internal/daemon` holds the marshal side:** the paths, the two unit renderers, the service manager over an injectable command runner, and the socket's HTTP client. Plan 5 reuses the client. `cmd/marshal/daemon.go` only parses and prints.
10. **The unit names everything.** Its arguments are, in order:
    - `--profile local`;
    - `--socket`, `--state-dir` and `--workspace`, the paths `internal/daemon` resolves;
    - `--addr off`;
    - `--marshal-bin`, the absolute, symlink-resolved path of the running `marshal`.

    Its environment is the installing process's `PATH`, plus `MARSHAL_CONFIG_DIR`, `MARSHAL_DATA_DIR`, `XDG_CONFIG_HOME` and `XDG_DATA_HOME` when they are set. It restarts the daemon on failure, and logs stdout and stderr to `<data>/daemon/webbridge.log`.
11. **`stop` unloads; `start` loads.**
    - On macOS, `stop` is `bootout`. `start` is `bootstrap` when the agent is not loaded, and `kickstart -k` when it is. The plist stays in `~/Library/LaunchAgents`, so the next login loads it again.
    - On Linux, `stop` and `start` are `systemctl --user stop` and `start`; the unit stays enabled.
    - `install` over an existing install rewrites the unit and restarts the service.
    - `uninstall` stops, disables and removes the unit, and keeps the data directory.
12. **`status` exits non-zero when the socket does not answer.** It prints three lines:
    - the service's state: not installed, loaded and running (with its pid), or stopped;
    - the socket's path, and whether it answered;
    - for an answering daemon, its version against this `marshal`'s, its start time, and its projects.

**Execution:**
- In the main checkout, if `git status` shows any of these uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon: approve P4 and record its plans 1 and 2`:
  - `docs/daemon/p4-local-daemon.md`, `docs/daemon/design.md` and `docs/daemon/p3-acp-transport.md`;
  - this plan and `docs/daemon/plans/2026-11-09-p4-1-runs-plan.md`.

  Plan 1's executor may have committed them already. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p4-2-local-profile` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p4-2-local-profile: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- No task runs `launchctl` or `systemctl` for real, and no test may. Installing the service is the user's manual run.

## Contracts

### The bridge (`web/bridge/localsocket.go`, `peercred_linux.go`, `peercred_darwin.go`, `peercred_other.go`, `daemoninfo.go`; all new)

```go
// DefaultDaemonDir is <data>/daemon, where <data> follows config.DataDir's
// rule: MARSHAL_DATA_DIR, else $XDG_DATA_HOME/marshal, else
// ~/.local/share/marshal (P4 D1).
func DefaultDaemonDir() (string, error)

// ErrDaemonRunning is ListenLocal's refusal when a daemon already answers
// on the path.
var ErrDaemonRunning = errors.New("a daemon is already listening")

// ListenLocal listens on the unix socket at path for the local profile: a
// 0700 directory of the user's own, no live daemon on the path, a stale
// socket removed, the socket 0600 (decision 5). Closing the listener removes
// the socket.
func ListenLocal(path string) (net.Listener, error)

// PeerChecked wraps ln so Accept returns only connections whose peer runs as
// uid; any other, or one whose peer cannot be read, is closed and logged
// (decision 6).
func PeerChecked(ln net.Listener, uid int, log *slog.Logger) net.Listener

// peerUID is the user ID of the process on the other end of conn, a unix
// socket connection. It is per system: SO_PEERCRED on Linux, LOCAL_PEERCRED
// on darwin, an error elsewhere.
func peerUID(conn net.Conn) (int, error)

// xucred is darwin's struct xucred, which LOCAL_PEERCRED fills
// (peercred_darwin.go only).
type xucred struct {
	Version uint32
	UID     uint32
	NGroups int16
	_       int16
	Groups  [16]uint32
}

// DaemonInfo is what GET /api/daemon answers about the local daemon.
type DaemonInfo struct {
	Version   string   `json:"version"`
	Profile   string   `json:"profile"`
	PID       int      `json:"pid"`
	StartedAt int64    `json:"startedAt"` // Unix milliseconds
	Socket    string   `json:"socket"`
	Projects  []string `json:"projects"`  // filled per request
}

// SetDaemonInfo makes GET /api/daemon answer info; without it the route
// answers 404 (decision 8).
func (s *Server) SetDaemonInfo(info DaemonInfo)

// SocketHandler is the server's handler for the local socket: ServeHTTP's
// routing without the bearer check (decision 7).
func (s *Server) SocketHandler() http.Handler

// SetHostProcessesOnly makes every agent, the control agent included, a
// host process, and stops the fleet from ever probing for a container
// runtime (decision 4). Call it before anything starts.
func (f *Fleet) SetHostProcessesOnly()

// containerRuntime is the fleet's container runtime: none when host-only,
// without probing; otherwise the probe's answer.
func (f *Fleet) containerRuntime() (path, name string, ok bool)
```

The fleet gains `hostOnly bool` and `probe func() (path, name string, ok bool)`, nil meaning `detectedRuntime`.

### `webbridge` (`cmd/webbridge/main.go`)

```go
// In config:
	// profile is "server" (the default) or "local" (P4 D1).
	profile string
	// socket is the local profile's unix listener.
	socket string
```

### The config locations (`internal/app/config/locations.go`)

```go
// DaemonDir is the local daemon's directory: DataDir(home)/daemon.
func DaemonDir(home string) string

// DaemonSocketPath is the local daemon's socket: DaemonDir(home)/marshal.sock.
func DaemonSocketPath(home string) string
```

### The marshal side (`internal/daemon/`, new)

```go
// Package daemon installs and drives the local daemon (P4 D1): its paths,
// its service unit, the commands that manage it, and the client for its
// socket.
package daemon

// Label is the launchd agent's label; Unit is the systemd unit's name.
const (
	Label = "dev.marshal.daemon"
	Unit  = "marshal-daemon.service"
)

// Paths are the files the daemon uses, all under config.DaemonDir.
type Paths struct {
	Dir, Socket, StateDir, Registry, Log string
}

// PathsFor resolves Paths for home.
func PathsFor(home string) Paths

// UnitSpec is what a service unit runs.
type UnitSpec struct {
	Webbridge string            // absolute path of the webbridge binary
	Marshal   string            // absolute path of the marshal binary
	Paths     Paths
	Env       map[string]string // PATH and the location variables that were set
}

// Args are the daemon's arguments, in the order decision 10 gives.
func (u UnitSpec) Args() []string

// LaunchdPlist and SystemdUnit render the unit for each platform.
func LaunchdPlist(u UnitSpec) []byte
func SystemdUnit(u UnitSpec) []byte

// Runner runs one service-manager command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager drives the service on one platform.
type Manager struct {
	GOOS     string // "darwin" or "linux"
	Home     string
	UID      int
	UnitDir  string // ~/Library/LaunchAgents or the systemd user directory
	Run      Runner
}

// NewManager is the manager for this machine, with the real runner.
func NewManager(home string) (*Manager, error)

func (m *Manager) Install(ctx context.Context, u UnitSpec) error
func (m *Manager) Start(ctx context.Context) error
func (m *Manager) Stop(ctx context.Context) error
func (m *Manager) Uninstall(ctx context.Context) error

// ServiceState is what the service manager reports.
type ServiceState struct {
	Installed, Running bool
	PID                int
}

func (m *Manager) State(ctx context.Context) (ServiceState, error)

// Info mirrors the bridge's DaemonInfo on the wire.
type Info struct {
	Version   string   `json:"version"`
	Profile   string   `json:"profile"`
	PID       int      `json:"pid"`
	StartedAt int64    `json:"startedAt"`
	Socket    string   `json:"socket"`
	Projects  []string `json:"projects"`
}

// Client talks HTTP to the daemon over its socket.
type Client struct{ /* unexported */ }

func NewClient(socket string) *Client

// Info reads GET /api/daemon, bounded by ctx.
func (c *Client) Info(ctx context.Context) (Info, error)

// FindWebbridge is the webbridge beside the running marshal, else the one on
// PATH; override wins when set.
func FindWebbridge(override string) (string, error)
```

### `marshal daemon` (`cmd/marshal/daemon.go`, new; `main.go`)

```go
var daemonRunner = runDaemon

// runDaemon implements `marshal daemon <install|start|stop|status|uninstall>`.
func runDaemon(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error
```

## Task 1: The local socket

**Goal:** the bridge can listen on a unix socket that only the user reaches, and refuses to take over from a live daemon.

**Files:** `web/bridge/localsocket.go`, `peercred_linux.go`, `peercred_darwin.go`, `peercred_other.go` (all new), `web/bridge/localsocket_test.go` (new).

**Steps:**

1. **`DefaultDaemonDir`**, as in Contracts, reading the two variables and the home directory in the order `config.DataDir` does.
2. **`ListenLocal`** (decision 5). Its errors name the path. A path longer than the platform's limit fails at listen with the system's error, unchanged.
3. **`peerUID`**, one file per system, each with a build constraint (`linux`, `darwin`, `!linux && !darwin`). Each reaches the descriptor through the connection's `SyscallConn` and its `Control`, and never through `File`, which would duplicate the descriptor. The darwin file declares `xucred` as in Contracts, calls `getsockopt` through `syscall.Syscall6` with the struct's address and size, and refuses a version other than 0. `unsafe` is the standard library, so the boundary rule holds.
4. **`PeerChecked`** (decision 6). Its `Close` and `Addr` are the wrapped listener's. A failed `Accept` of the wrapped listener is returned as is. A refused connection is closed and the loop accepts again.
5. **Tests** in `localsocket_test.go`, each under `t.TempDir()`:
   - `TestListenLocalMakesAPrivateSocket`: the directory is created `0700`, and the socket is `0600` and a socket.
   - `TestListenLocalRefusesALiveDaemon`: a second `ListenLocal` on a path the first still serves returns `ErrDaemonRunning`, and the first still accepts a dial.
   - `TestListenLocalReplacesAStaleSocket`: a socket left by a closed listener (made with `net.Listen` and its `SetUnlinkOnClose(false)`) is replaced.
   - `TestListenLocalRefusesAnOpenDirectory`: an existing directory of mode `0755` is refused and left as it was.
   - `TestListenLocalLeavesAFileAlone`: a regular file at the path is refused and kept.
   - `TestClosingRemovesTheSocket`.
   - `TestPeerUIDIsOurs`: on linux and darwin, a dialled connection's accepted end reports `os.Getuid()`.
   - `TestPeerCheckedRefusesAnotherUser`: `PeerChecked` with `os.Getuid()+1`. A client's dial is accepted by the kernel, its read returns EOF within 1s, and `Accept` has returned nothing. With `os.Getuid()`, the same client is served.
   - `TestDefaultDaemonDirFollowsTheDataDirRule`: a table over `MARSHAL_DATA_DIR`, `XDG_DATA_HOME` and neither, set with `t.Setenv`.

   On a system other than linux and darwin the peer tests skip.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./web/bridge/
GOOS=linux go vet ./web/bridge/
TMPDIR=/private/tmp/p4-2-suite go test ./web/bridge/ -run 'ListenLocal|ClosingRemovesTheSocket|PeerUID|PeerChecked|DefaultDaemonDir|WebIsStdlibOnly' -count=1 -v 2>&1 | tail -30
```

Expected: the tests pass on this machine (darwin), the linux vet is clean, and `TestWebIsStdlibOnly` passes.

Mutation check: make `PeerChecked` skip its comparison. `TestPeerCheckedRefusesAnotherUser` fails. Restore it.

## Task 2: The bridge's local pieces

**Goal:** a fleet can be host-only, a server can serve its routes on the socket without a token, and a daemon can say what it is.

**Files:** `web/bridge/fleet.go`, `control.go`, `egress_fleet.go`, `derive.go`, `wsbuild.go`, `wsspawn.go`, `http.go`, `daemoninfo.go` (new), `web/bridge/hostonly_test.go` (new), `web/bridge/daemoninfo_test.go` (new).

**Steps:**

1. **`SetHostProcessesOnly` and `containerRuntime`** (decision 4). Replace each of the six `detectedRuntime()` calls with `f.containerRuntime()`. In `egressRuntimeName`, `runtimeName` and `runtimeInfo`, the host-only answer comes before the fake-runner shortcut, and the override `egressRuntime` stays first. `detectedRuntime` itself stays, as the default probe.
2. **`SocketHandler`** (decision 7). `ServeHTTP`'s routing moves into an unexported method that takes the API handler, which `ServeHTTP` calls with the token-checked mux and `SocketHandler` with the bare one. Nothing `ServeHTTP` answers changes.
3. **`DaemonInfo`, `SetDaemonInfo` and `GET /api/daemon`** (decision 8), in `daemoninfo.go`. The handler copies the info, fills `Projects` from the workspace (sorted, never nil, so it encodes as `[]`), and writes it with `writeJSON`. Register the route in `routes`.
4. **Tests** in `hostonly_test.go`:
   - `TestAHostOnlyFleetNeverProbes`: a fleet built as the package's fleet tests build one, with a counting probe and a recording fake `runner`, made host-only. `containerRuntime`, `egressRuntimeName`, `runtimeName` and `runtimeInfo` each report none. The agent factory builds a host-process `Child` (`Containerized` false), and so does `buildControlChild` with no `newControl`. The probe count is 0, and the runner recorded no command.
   - `TestAFleetProbesOnceByDefault`: without host-only, `containerRuntime` asks the counting probe.

   In `daemoninfo_test.go`:
   - `TestTheDaemonRouteAnswersItsInfo`: a server with info and two projects answers both through `SocketHandler` with no token, and answers 401 through `ServeHTTP` without one.
   - `TestTheDaemonRouteIsAbsentWithoutInfo`: 404 with `not a daemon`.
   - `TestTheSocketHandlerServesEveryRoute`: `GET /api/projects` through `SocketHandler` with no token answers 200, and an unknown `/api/` path answers what `ServeHTTP` answers with a token.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./web/bridge/
TMPDIR=/private/tmp/p4-2-suite go test ./web/bridge/ -run 'HostOnlyFleet|ProbesOnce|DaemonRoute|SocketHandler' -count=1 -v 2>&1 | tail -30
TMPDIR=/private/tmp/p4-2-suite go test ./web/bridge/ -count=1 -timeout 10m
```

Expected: the new tests pass, and every existing bridge test passes unchanged. `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` hangs about one run in three in its fake agent. That hang is known: rerun the package once before treating it as new.

Mutation check: drop the host-only check from `runtimeInfo`. `TestAHostOnlyFleetNeverProbes` fails on the fake runner's "docker". Restore it.

## Task 3: `webbridge --profile local`

**Goal:** `webbridge --profile local` serves on its socket with the local defaults and refusals, and the server profile is unchanged.

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/main_test.go`, `cmd/webbridge/local_test.go` (new).

**Steps:**

1. **The flags.**
   - `--profile` (`WEBBRIDGE_PROFILE`), default `server`; any other value is an error.
   - `--socket` (`WEBBRIDGE_SOCKET`).
   - `--addr off`, accepted only with `local`.
   - With `server`, a `--socket` or an `--addr off` is an error that names the local profile.
2. **The local defaults and refusals** (decisions 2 and 3), applied in `parseConfig` after the flags parse.
   - Whether a flag was set comes from the flag set's `Visit`, and whether its variable was set from `os.LookupEnv`.
   - The defaults resolve through `bridge.DefaultDaemonDir`, and a failure to resolve it is an error.
   - The working-directory project is skipped by leaving `cwdRoot` empty, unless `--cwd-root` or its variable was given.
   - `run` already skips an empty `cwdRoot`; check that it does, and keep it so.
3. **`run` in the local profile:**
   - before the fleet starts anything, call `SetHostProcessesOnly`;
   - skip egress, which the config already turned off;
   - listen with `bridge.ListenLocal(cfg.socket)`, wrapped in `bridge.PeerChecked` with `os.Getuid()` and the default logger;
   - serve `api.SocketHandler()` on its own `http.Server` with the same header timeout;
   - set the daemon info: `versionString`'s version (`dev` when unstamped), `local`, the pid, the start time and the socket;
   - print `webbridge: listening on unix://<path>` to stderr;
   - with `--addr off`, open no TCP or preview listener, and generate no token;
   - shut the socket's server down beside the TCP one, which closes and removes the socket;
   - a refusal from `ListenLocal` closes the fleet and returns the error, as a failed TCP listen does today.
4. **Tests.** In `main_test.go`, `TestParseConfigDefaults` gains `profile == "server"` and an empty socket.

   In `local_test.go`, with `MARSHAL_DATA_DIR` pointed at `t.TempDir()` through `t.Setenv`:
   - `TestTheLocalProfilesDefaults`: socket, state directory and workspace under `<data>/daemon`; egress `off`; secrets `env`; no cwd root.
   - `TestTheLocalProfileRefuses`: a table over `--egress on`, `WEBBRIDGE_EGRESS=on`, `--secrets local` (with a key file), `--socket` without the profile, `--addr off` without the profile, and `--profile cloud`.
   - `TestTheLocalProfileServesItsSocket`: `run` in a goroutine with `--profile local --addr off`, a short socket path under `/private/tmp/p4-2-suite`, and a context the test cancels.
     - Over the socket, with no token, `GET /api/daemon` answers profile `local`, this pid and the socket path.
     - Cancelling the context makes `run` return nil within 5s and removes the socket.
     - The stderr output holds no "generated bearer token" line.
   - `TestASecondLocalDaemonRefusesToStart`: a second `run` on the same socket returns an error wrapping `bridge.ErrDaemonRunning`, while the first still answers.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./cmd/webbridge/
TMPDIR=/private/tmp/p4-2-suite go test ./cmd/webbridge/ -count=1 -v 2>&1 | tail -40
```

Expected: every test in the package passes, the existing ones unchanged.

Mutation check: skip `SetHostProcessesOnly` in `run`. No test here catches it, since none spawns an agent; Task 2's test pins the fleet side. Confirm instead, by reading the code, that the call comes before `ReattachAll`. Then restore it.

## Task 4: `internal/daemon` and the config locations

**Goal:** marshal can resolve the daemon's paths, render its unit, drive the service manager, and read the daemon over its socket, all testable without a service manager.

**Files:**
- `internal/app/config/locations.go`, `internal/app/config/locations_test.go`;
- `internal/daemon/daemon.go`, `units.go`, `manager.go`, `client.go`, `find.go` (all new);
- `internal/daemon/main_test.go`, `units_test.go`, `manager_test.go`, `client_test.go`, `paths_test.go` (all new).

**Steps:**

1. **`DaemonDir` and `DaemonSocketPath`**, as in Contracts, with a test beside `DataDir`'s.
2. **`PathsFor`:** the directory, `marshal.sock`, `state`, `fleet.json` and `webbridge.log` under `config.DaemonDir(home)`.
3. **The units** (decision 10).
   - **The plist:** a `Label`, `ProgramArguments` (the webbridge, then `Args()`), `EnvironmentVariables`, `RunAtLoad` true, `KeepAlive` with `SuccessfulExit` false, and `StandardOutPath` and `StandardErrorPath` both the log. Every string is XML-escaped.
   - **The systemd unit:**
     - `[Unit] Description=Marshal daemon`;
     - `[Service]` with `ExecStart`, each argument double-quoted, with `\`, `"` and `%` escaped as systemd requires;
     - one `Environment=` line per variable, quoted the same way, in sorted order;
     - `Restart=on-failure`, and `StandardOutput` and `StandardError` set to `append:<log>`;
     - `[Install] WantedBy=default.target`.
   - The variables are sorted in both formats, so a render is stable.
4. **The manager** (decision 11).
   - `NewManager` refuses any `GOOS` but darwin and linux with "the local daemon runs on macOS and Linux only". It sets `UnitDir` to `~/Library/LaunchAgents`, or to `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`). It uses a runner over `exec.CommandContext`.
   - `Install` creates the daemon's directory `0700` and the unit directory, and writes the unit `0644`. Then:
     - **darwin:** `bootout` when `print` shows it loaded, then `bootstrap`;
     - **linux:** `daemon-reload`, `enable --now`, then `restart`.
   - `Start`, `Stop` and `Uninstall` run decision 11's commands. A "not loaded" or "not found" answer from `bootout`, `stop` or `disable` counts as done.
   - `State` reads `print`'s `state =` and `pid =` lines, or `show`'s `ActiveState=` and `MainPID=`.
   - Every command a manager runs goes through `Run`, with the full command line.
5. **The client.** An `http.Client` whose transport dials the socket, a request to `http://daemon/api/daemon` bounded by the caller's context, and a non-200 answer returned as an error carrying the status and the body's `error`.
6. **`FindWebbridge`:** the override when set (it must exist and be executable); else `webbridge` in the directory of the symlink-resolved `os.Executable()`; else `exec.LookPath("webbridge")`; else an error naming both places and `go build ./cmd/webbridge`.
7. **Tests.** `main_test.go` calls `testenv.Run`.
   - `TestThePathsAreUnderTheDaemonDir`.
   - `TestTheBridgeAndConfigAgreeOnTheDaemonDir`: the test-only import of `marshal/web/bridge` is allowed, since the rule binds `web/` only. A table over the three data-dir settings: `bridge.DefaultDaemonDir()` equals `config.DaemonDir(home)`.
   - `TestThePlistNamesEverything` and `TestTheSystemdUnitNamesEverything`: a spec with a space, a `%`, a quote and an `&` in its paths and one variable. Each render holds every argument in decision 10's order, escaped for its format. The plist parses as XML, with `encoding/xml` decoding its tokens without error.
   - `TestTheManagerRunsTheRightCommands`: a table over darwin and linux and each of `Install`, `Start` (loaded and not), `Stop`, `Uninstall` and `State`, with a recording runner whose answers the row scripts. Each row checks the command lines, in order, and the files written or removed under a temporary home.
   - `TestStopAndUninstallTolerateANotLoadedService`.
   - `TestStateReadsTheServiceManager`: canned `print` and `show` outputs give running with a pid, stopped, and not installed.
   - `TestTheClientReadsTheDaemon`: a test server on a unix socket, made with `httptest.NewUnstartedServer`, its listener replaced by one on a short path, answers a `DaemonInfo` that `bridge`'s own handler encodes. A second row answers 404, and `Info` returns an error naming it.
   - `TestFindWebbridgePrefersTheOverrideThenTheSibling`: under a temporary directory with fake executables.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/daemon/ ./internal/app/config/
TMPDIR=/private/tmp/p4-2-suite go test ./internal/daemon/ ./internal/app/config/ -count=1 -v 2>&1 | tail -40
```

Expected: the new tests pass, and every config test passes unchanged.

Mutation check: drop `%` escaping from the systemd renderer. `TestTheSystemdUnitNamesEverything` fails. Restore it.

## Task 5: `marshal daemon`

**Goal:** the user's commands.

**Files:** `cmd/marshal/main.go`, `cmd/marshal/daemon.go` (new), `cmd/marshal/daemon_test.go` (new), `cmd/marshal/main_test.go`.

**Steps:**

1. **The dispatch.** `daemonRunner` beside the others, and `daemon` dispatched after `snapshots`, as `snapshots` is.
2. **`runDaemon`**, shaped as `runSnapshots`.
   - `install [--webbridge PATH]` builds the `UnitSpec`:
     - the webbridge from `FindWebbridge`;
     - the symlink-resolved `os.Executable()`;
     - `PathsFor(home)`;
     - the environment of decision 10.

     It installs, then prints the unit's path and the socket's.
   - `start`, `stop` and `uninstall` run the manager's call and print one line each.
   - `status` prints decision 12's three lines. It reads the service state and then the daemon over the socket with a 2s bound. It returns an error ("the daemon is not answering on <path>") when the socket does not answer, after printing.
   - An unknown or missing subcommand is an error listing the five.
   - The manager and the client come from two package variables, so the tests replace them.
3. **Tests** in `daemon_test.go`, with a fake manager and a fake client:
   - `TestDaemonInstallPassesExplicitPaths`: the spec carries the resolved paths, `PATH`, and `MARSHAL_DATA_DIR` when set (and not when unset).
   - `TestDaemonStatusReportsARunningDaemon`: the three lines, the versions side by side.
   - `TestDaemonStatusFailsWhenTheSocketIsSilent`: the error, after the service line.
   - `TestDaemonRejectsUnknownSubcommands`.

   In `main_test.go`, `TestDaemonDispatches` swaps `daemonRunner`, as the snapshots test does.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./cmd/marshal/
TMPDIR=/private/tmp/p4-2-suite go test ./cmd/marshal/ -count=1 -v 2>&1 | tail -30
```

Expected: every test in the package passes.

## Task 6: The docs, a smoke run and full verification

**Goal:** the maps and the spec record the local profile, a real daemon answers on a real socket, and the whole tree is green.

**Files:** `docs/daemon/p4-local-daemon.md`, `AGENTS.md`, `docs/ARCHITECTURE.md`.

**Steps:**

1. **The Implementation record.** Replace the "Not yet run." under "### Plan 2 — the local profile", and touch nothing under plan 1's heading. Name:
   - the flags and their refusals;
   - the socket's checks;
   - the peer check per system, and the raw `getsockopt` on darwin;
   - the host-only fleet and its six callers;
   - `SocketHandler`;
   - `GET /api/daemon`;
   - `internal/daemon`;
   - the unit's contents;
   - the smoke run's output.

   Under "Open for plan 4": project children will listen in `<data>/daemon/state`.
2. **`AGENTS.md`:**
   - add `daemon` to the subcommand list (`:33`): "`daemon` (install and drive the local daemon)";
   - add `internal/daemon/` after `internal/launch/` (`:81`): "the local daemon's paths, service units and commands, and its socket's client";
   - in "Web control plane", add one line: `webbridge --profile local` is the local daemon (P4).
3. **`docs/ARCHITECTURE.md`:**
   - the Web bridge row (`:92`) gains: "With `--profile local` it is the user's daemon: a user service on a unix socket, host processes only.";
   - the Entrypoint row (`:79`) gains `marshal daemon`, which drives that service through `internal/daemon`.
4. **The smoke run**, which the executor runs:
   1. build both binaries into `/private/tmp/p4-2-suite/bin`;
   2. start `webbridge --profile local --addr off --socket /private/tmp/p4-2-suite/d/marshal.sock --state-dir /private/tmp/p4-2-suite/d/state --workspace /private/tmp/p4-2-suite/d/fleet.json --marshal-bin /private/tmp/p4-2-suite/bin/marshal` in the background;
   3. read `curl --unix-socket /private/tmp/p4-2-suite/d/marshal.sock http://daemon/api/daemon`;
   4. check the socket's and directory's modes with `stat`;
   5. start a second one on the same socket, and see it refuse;
   6. stop the first with SIGTERM, and see the socket gone;
   7. confirm with `ps` that no `docker` or `podman` process started.

   Record the output in the Implementation record. Delete the binaries afterwards.
5. **Main may have moved.** If `main` moved while this branch ran (plan 1 may have merged), merge it into the branch before the full verification. Resolve the conflicts, then rerun Tasks 2 and 3's Verify.
6. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p4-2-suite
gofmt -l internal cmd test web
go vet ./...
GOOS=linux go vet ./web/... ./internal/daemon/ ./cmd/webbridge/
TMPDIR=/private/tmp/p4-2-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and both `go vet` runs are clean.
- Every package passes, `internal/daemon` included. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, `TestNoticeCallBudget`, `TestAnswerCallBudget`, `TestStackBusyFlipBudget`, `TestControlCallBudget`, `TestSettingsCallBudget`, `TestEndCallBudget`, `TestNoteCallBudget`, `TestBrowserCallBudget`;
  - `internal/app/tui`: `TestAKeyPressOverThePipeBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three. A hang there is known.

**The manual run is the user's.** The executor reports that it is due and lists it, on macOS:
1. `marshal daemon install` with `webbridge` built beside `marshal`;
2. `marshal daemon status` shows it running and answering;
3. close the terminal, open a new one, and `status` still answers;
4. `marshal daemon stop`, after which `status` fails;
5. `marshal daemon start`, after which it answers again;
6. `marshal daemon uninstall`, which removes the plist from `~/Library/LaunchAgents`, while the data directory stays;
7. the log at `<data>/daemon/webbridge.log` has the start lines.

## Self-review

- **Spec coverage:**
  - D1's flags and defaults: Task 3.
  - The socket: Task 1, and Task 3's run test.
  - The peer check: Task 1.
  - No machinery: Task 2's `TestAHostOnlyFleetNeverProbes`, and Task 3's refusals and its run without egress.
  - The token-free socket: Task 2's handler tests and Task 3's run test.
  - `GET /api/daemon`: Task 2.
  - The service: Tasks 4 and 5.
  - W7 is Tasks 1 to 4's tests and the smoke run. W10 is Task 6.
  - Decisions 2 and 7: Tasks 4 and 5, and Tasks 1 and 3.
- **What could go wrong:**
  - **The raw `getsockopt` on darwin.** It is the one call here outside the `syscall` package's typed wrappers. `TestPeerUIDIsOurs` runs it on this machine. If it fails there, stop and report rather than drop the check: decision 7 asks for it.
  - **launchd and systemd are untested here.** The command lines are pinned by the recording runner, and the real run is the user's manual check. A real failure there is fixed in the manager's command lines, not by loosening the tests.
  - **The unit's environment goes stale.** A `PATH` change after install is not seen until the next install. Plan 6 resolves each project's environment and removes the dependence.
  - **The socket path's length.** `<data>/daemon/marshal.sock` is short on any normal home. A `MARSHAL_DATA_DIR` deep enough to pass 104 bytes on macOS fails at listen with the system's error. The tests keep their paths short.
  - **A second bridge on the old registry.** The local profile uses its own registry and state, so a Studio bridge the user runs by hand is untouched. Whether the daemon takes the Studio's registry over is P5's.
