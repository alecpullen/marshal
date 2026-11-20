# One daemon plan 2: hosts over SSH — implementation plan

Date: 2026-11-20. Status: approved 2026-11-20.

Base: `main` at `bbdc4486`, with [plan 1](2026-11-20-od-1-one-daemon-plan.md)
applied first. Every anchor below was checked at `bbdc4486`. A step that edits
lines plan 1 also changes says so. The baseline passed in full at that commit,
with `TMPDIR=/private/tmp/od2`:
- `go test -count=1 ./internal/fleetprofile/ ./internal/fleetclient/
  ./internal/launch/ ./internal/daemon/ ./cmd/marshal/ ./web/bridge/contract/...
  ./test/daemon/`;
- `cd app && flutter test test/client/host_discovery_io_test.dart
  test/client/fleet_profiles_io_test.dart test/client/transport_io_test.dart
  test/model/hosts_model_test.dart test/ui/settings_test.dart
  test/status_test.dart`.

Approved design: [design.md](../design.md) D6–D9 and D15, and assumptions
A1–A3. Planning settled six details, and design.md records them:
- **Reloading.** The tunnel helper re-reads `fleet.toml` every 2 s. `marshal
  fleet add` and `remove` make no reload call, and a hand edit reloads too.
- **SSH agent (open question 3).** The unit carries no `SSH_AUTH_SOCK`: on
  both platforms that path is per login or per boot. The docs say how a
  systemd user service reaches an agent, and a host that cannot authenticate
  is `down` with ssh's own message.
- **Retired commands.** `marshal fleet servers`, `login` and `token` go.
  `marshal daemon status` lists the hosts with their state.
- **Moved to plan 3.** `marshal daemon token` and `marshal daemon join-token`
  serve the listener's token and the join, which plan 3 deletes, so they go
  there.
- **Also moved to plan 3.** After this plan, nothing the fleet client dials
  answers 401, but its rejected state reaches the fleet screen. Plan 3 deletes
  it with the bearer check.
- **The browser is unchanged.** It keeps its relayed hosts and their sign-in
  until plan 3. This plan removes only the native app's token paths.

Inventory: [inventory.md](../inventory.md) §Plan 2.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **A host is an SSH destination.** `fleet.toml` holds `[hosts.NAME]` tables
  with `ssh`, and optionally `marshal` and `notify`. `[servers.*]` tables are
  refused, with a message that shows the new shape.
- **The daemon keeps one tunnel per host.** `marshal daemon tunnels` runs
  beside the bridge. It keeps one `ssh -N` per host, forwarding
  `<daemon dir>/hosts/NAME.sock` to that host's daemon socket, and reports each
  tunnel's state to the bridge.
- **The daemon says where its hosts are.** `GET /api/daemon` carries `hosts`
  (name, destination, state, socket, last error, since). `marshal daemon
  status` prints them.
- **Native clients dial a tunnel as they dial the local socket.** `marshal
  fleet --host NAME` runs the fleet screen on the host's tunnel socket. The
  native Studio discovers the local daemon, and then each host its status
  document names.
- **Settings › Hosts adds a host by name and SSH destination.** A host's
  detail shows its tunnel's state, its last error and **Remove**.
- **No client-side token or TLS remains for native clients.** This deletes
  `ServerFleet`, `daemon.Remote`, `fleetclient.HTTPS`, `fleetprofile`'s token
  and TLS code, and the app's `serverTransport`.

## Non-goals

- The web listener, its forward to hosts, and the browser's view of hosts
  (plan 3). A browser still reaches relayed hosts, and does not see SSH hosts.
- Previews of a remote host's agents (open question 1). They wait for D11's
  preview listener in plan 3.
- `marshal attach --host`. A host's sessions open from its fleet screen with
  `⏎`.
- Showing several hosts in one TUI fleet screen. `marshal fleet --host` shows
  one host.
- The fleet client's 401 handling, `marshal daemon token`, `marshal daemon
  join-token` and `fleetStdoutIsTerminal`: plan 3.
- Renaming `HostKind.server` in the app.
- Windows.

## Assumptions

Each was checked at the base unless it says otherwise.

- **`fleet.toml` today** (`internal/fleetprofile`).
  - `File` holds `Servers` and `Local` (`profile.go:29`).
  - Decoding is strict: `DisallowUnknownFields` (`profile.go:113`).
  - `checkName` (`:191`) refuses `local`, dots alone, and anything outside
    `[A-Za-z0-9._-]`. Its messages say `servers.<name>`.
  - `AddServer` and `RemoveServer` (`edit.go:18`, `:67`) edit the file text,
    keep its comments, re-check the result with `decode`, and write it with
    `writePrivate` (`:222`).
  - `RemoveServer` also deletes the keychain token (`deleteToken`, `:203`),
    which is why it takes an `OpenStore`.
  - `token.go` holds `OpenStore`, `CommandRunner`, `Token`, `KeychainKey` and
    `RunShell`. `tls.go` holds `TLSConfig`.
- **Callers of the server profiles.**
  - `cmd/marshal/main.go`: `fleetServerRunner` (`:32`), `fleetLoginStore`
    (`:36`), `fleetTokenStore` and `fleetTokenRunner` (`:45–46`).
  - Also in `main.go`: `runFleet` (`:248`), `FleetServers` and `FleetServer`
    (`:383`, `:390`), `runFleetServers` (`:401`), `runFleetAdd` (`:452`),
    `runFleetRemove` (`:541`), `runFleetToken` (`:565`), `runFleetLogin`
    (`:598`) and `readFleetToken` (`:644`).
  - `internal/launch/fleet.go`: `ServerFleet` (`:101`).
  - Their tests: `cmd/marshal/fleet_cli_test.go`, `cmd/marshal/main_test.go`
    (`:802–1066`), `internal/launch/fleet_test.go` (`:383–700`) and
    `test/daemon/server_profile_test.go`.
- **`fleetStdoutIsTerminal`** (`main.go:51`) is also used by `marshal daemon
  token` (`daemon.go:545`). It stays.
- **The launch loop's server mode.**
  - `options.remote` (`launch.go:83`) is read at `daemon.go:137` (the attach
    client), `:417` (the connect panel: `local` is `remote == nil`), `:659`
    (the fleet transport), `:773` (the version note) and `:854`
    (`SharedFilesystem`).
  - `Fleet` (`fleet.go:61`) probes `Info` and refuses a daemon whose
    `Profile` is not `"local"` (`:84`). A host's daemon reports `"local"`
    too, because plan 1 keeps that value.
  - `fleetclient.Socket` (`client.go:41`) names its transport `"local"`, and
    `Transport.Server` is an exported field.
- **The daemon client's remote mode.**
  - `Remote` and `remoteTLSConfig` are at `internal/daemon/client.go:103–184`.
  - The `token` and `remote` fields (`:74`, `:79`) are read at `:523`
    (attach), `:769` and `:780` (instance identity), `:806` (enrollment) and
    `:1001` (access).
  - `MarshalInstance.Remote` (`:703`) is set only from `c.remote`, and nothing
    outside the package reads it.
- **The fleet client's HTTPS mode.**
  - `HTTPS` is at `internal/fleetclient/client.go:66`, with
    `tlsHandshakeTimeout` above it. `Transport.Token` is at `:35`.
  - `authorize` (`:508`) is called at `:489`, `:528` and `actions.go:55`.
  - The tests that use HTTPS or Token: `TestHTTPSReadsABridgeOverTLS` and
    `TestHTTPSNeverReadsTheDaemon` (`client_test.go:1117` and after),
    `TestAnActionSendsTheToken` (`actions_test.go:293`), and
    `actionBridge.client`'s `token` argument (`actions_test.go:67`).
- **The bridge's helper and daemon info.**
  - `Helper` (`web/bridge/helper.go:25`) logs `"webbridge: refresh helper
    exited"` (`:70`) whatever it runs.
  - `Server.daemon` (`daemoninfo.go:43`) fills the per-request fields.
  - `SocketHandler` (`http.go:205`) dispatches the socket-only routes before
    `route`. The network mux never sees them, so it answers them 404.
  - The refresh helper is started at `cmd/webbridge/main.go:978–997`, when
    `--oauth-refresh` (`:206`) is set. Plan 1 moves that block out of its
    `cfg.profile == "local"` condition.
- **The unit.** `UnitSpec.Args` (`internal/daemon/daemon.go:67`) ends with
  `--oauth-refresh` (`:102`). Plan 1 removes `--profile local` from its start
  and adds `--egress`.
- **The status document.** `daemon.Status` (`internal/daemon/status.go:9`)
  embeds `contract.DaemonInfo`. `test/contract/daemon-status.json` is held to
  it by `TestTheStatusDocumentIsCurrent` (`cmd/marshal/daemon_test.go`), and
  read by `app/test/status_test.dart`.
- **`contract.g.dart` renders a typed string as a Dart enum** with a `wire`
  field and an `unknown` value, as it does for `AgentState`
  (`contract.g.dart:59`).
- **The app.**
  - Native discovery is `discoverHosts` (`app/lib/src/client/host_discovery_io.dart:20`).
    It runs `marshal daemon status --json` through `DaemonCli.status`
    (`app/lib/src/daemon/cli.dart:46`), and then `marshal fleet servers --json`
    and `marshal fleet token`.
  - The tray's own `DaemonInfo` (`app/lib/src/daemon/status.dart:90`) parses
    the status document. It is separate from `contract.g.dart`'s.
  - `serverTransport` and `TokenProvider` are in `transport.dart:8–11` and
    `:105–111`, `transport_io.dart:11–18` and `:121–173`, and
    `transport_stub.dart:15–20`.
  - `HostRegistry` builds each `Host` from a `HostSpec` (`host.dart:145`).
  - `FleetStore.hosts` builds each `HostView` (`fleet_store.dart:44`).
  - `HostsModel` (`hosts_model.dart:28`) and `HostsSection`, `HostForm` and
    `HostDetail` (`hosts_section.dart:13`, `:95`, `:222`). The model is built
    in `main.dart:753–762`.
  - `FleetProfiles` (`fleet_profiles.dart:13`) declares `add(name, url,
    caFile)`, `login` and `remove`.
  - In a browser, `storeToken` and `clearToken` are set (`main.dart:761–762`).
    In a native build both are null, and Sign in runs `fleet login`.
  - The Hosts screens are registered in `app/test/support/screens.dart` at
    `:1433–1463`, with fixtures at `:888–1012`. None of them is a golden.
- **ssh.** These were not checked here; Task 18 checks them by hand.
  - OpenSSH forwards a unix socket with `-L local_path:remote_path` (A1).
  - `StreamLocalBindUnlink=yes` replaces a stale socket file at the local path.
  - `ExitOnForwardFailure=yes` makes a failed bind end the process.
  - `BatchMode=yes` turns a password or host-key prompt into a failure with
    ssh's own message (A2).
  - A remote command the remote shell cannot find exits 127 (A3).
  - `ControlMaster=no` with `ControlPath=none` gives each `ssh` its own
    connection, even when the user's `~/.ssh/config` multiplexes.
  - `-L` splits its argument at `:`, so neither socket path may contain one.
- **A unix socket path** may hold at most 103 bytes on macOS (104 with the
  NUL). Linux allows more, so 103 is the limit everywhere.

## Contracts

```go
// web/bridge/contract/daemon.go

// HostState is a remote host's tunnel state (one-daemon D7).
type HostState string

const (
	HostConnecting HostState = "connecting" // before the first attempt has an outcome
	HostLive       HostState = "live"       // the tunnel answers GET /api/daemon
	HostDown       HostState = "down"       // the last attempt failed; LastError says why
)

// DaemonHost is one remote host's tunnel as GET /api/daemon reports it
// (one-daemon D7, D9).
type DaemonHost struct {
	Name      string    `json:"name"`
	SSH       string    `json:"ssh"` // the destination, as fleet.toml names it
	State     HostState `json:"state"`
	Socket    string    `json:"socket"` // this machine's end of the tunnel
	LastError string    `json:"lastError,omitempty"`
	Since     int64     `json:"since"` // Unix milliseconds: when State last changed
}

// DaemonHostsReport is PUT /api/daemon/hosts's body: every host the tunnel
// helper keeps, replacing its last report.
type DaemonHostsReport struct {
	Hosts []DaemonHost `json:"hosts"`
}

// New DaemonInfo field, after Join:
Hosts []DaemonHost `json:"hosts"` // filled per request: the tunnel helper's last report
```

```go
// web/bridge/daemonhosts.go (new)

// daemonHostsPath is the tunnel helper's report: PUT, socket only.
const daemonHostsPath = "/api/daemon/hosts"

// DaemonHost is contract.DaemonHost; see that declaration.
type DaemonHost = contract.DaemonHost

// reportHosts takes the tunnel helper's report and replaces the last one.
func (s *Server) reportHosts(w http.ResponseWriter, r *http.Request)

// reportedHosts is the last report, sorted by name and never nil.
func (s *Server) reportedHosts() []DaemonHost

// New Server fields (http.go):
hostsMu sync.Mutex
hosts   []DaemonHost

// New Helper field (helper.go):
Name string // the helper's name in the bridge's log
```

```go
// internal/fleetprofile/profile.go

// Host is one [hosts.<name>] table (one-daemon D6).
type Host struct {
	Name    string
	SSH     string // the destination, exactly as ssh takes it
	Marshal string // the remote binary: absolute or "~/"-relative; "" is marshal on the remote PATH
	Notify  bool
}

// New File field:
Hosts map[string]Host // by name

// LookupHost answers the named host, or an error naming the file and the
// hosts it holds.
func (f File) LookupHost(name string) (Host, error)

// internal/fleetprofile/edit.go

// AddHost appends h to the file at path as a [hosts.NAME] table: comments and
// order kept, the result checked as Load checks it, and written atomically at
// 0600. A name already in the file is refused.
func AddHost(path string, h Host) error

// RemoveHost deletes name's [hosts.NAME] table from the file at path.
func RemoveHost(path, name string) error
```

```go
// internal/tunnels (new package)

// Config is what Run needs. A zero tuning field is the production default.
type Config struct {
	FleetFile string // fleet.toml, re-read every Poll
	Dir       string // where the tunnel sockets are made, mode 0700
	// Report sends every host's state to the daemon, replacing its last
	// report. Required.
	Report func(ctx context.Context, hosts []contract.DaemonHost) error
	// Probe asks the daemon at socket for GET /api/daemon. Required.
	Probe  func(ctx context.Context, socket string) error
	Stderr io.Writer // where load and report failures are written; nil discards them
	SSH    string    // the ssh binary; "" is "ssh" on PATH

	Commands Commands         // nil: Exec()
	Now      func() time.Time // nil: time.Now

	Poll       time.Duration // fleet.toml re-read; zero is 2s
	Check      time.Duration // a live tunnel's re-probe; zero is 15s
	Backoff    time.Duration // the first restart's delay; zero is 1s
	MaxBackoff time.Duration // the longest delay; zero is 30s
	LiveWithin time.Duration // how long a started forward has to answer; zero is 30s
}

// Run keeps one tunnel per host in cfg.FleetFile until ctx ends. Then it stops
// every ssh it started and returns nil. A missing Report or Probe is an error.
func Run(ctx context.Context, cfg Config) error

// Commands runs ssh. Exec() runs real processes, and tests script them.
type Commands interface {
	// Output runs argv to completion, bounded by ctx, and answers its standard
	// output. A failure is a *CommandError.
	Output(ctx context.Context, argv []string) ([]byte, error)
	// Start starts argv and answers the running process.
	Start(argv []string) (Process, error)
}

// Process is one running ssh -N.
type Process interface {
	Done() <-chan struct{} // closed once the process has exited
	Err() error            // why it exited, a *CommandError; read after Done
	Stop()                 // SIGTERM, then SIGKILL after 2s; returns once it has exited
}

// CommandError is how an ssh command ended: its exit code (-1 when it did not
// start or was signalled) and its last non-empty line of stderr.
type CommandError struct {
	Code int
	Line string
}

// Error is Line, or "exit status <Code>" when Line is empty.
func (e *CommandError) Error() string

// Exec is the Commands that runs processes.
func Exec() Commands

// internal/tunnels/ssh.go

// maxSocketPath is the longest unix socket path Run makes (macOS's limit).
const maxSocketPath = 103

// statusArgv is the ssh command that prints h's daemon status document.
func statusArgv(ssh string, h fleetprofile.Host) []string

// forwardArgv is the ssh -N command that forwards local to remote on h.
func forwardArgv(ssh string, h fleetprofile.Host, local, remote string) []string

// remoteCommand is `<marshal> daemon status --json` as the remote shell reads
// it: the binary single-quoted, keeping a leading "~/" outside the quotes.
func remoteCommand(marshal string) string

// remoteSocket reads the answering daemon's socket from a status document.
func remoteSocket(name string, doc []byte) (string, error)

// socketPath is dir/<name>.sock, or an error when it is longer than
// maxSocketPath.
func socketPath(dir, name string) (string, error)
```

```go
// internal/daemon/client.go

// HostInfo is the contract's DaemonHost; see that declaration.
type HostInfo = contract.DaemonHost

// daemonHostsPath is the tunnel helper's report route.
const daemonHostsPath = "/api/daemon/hosts"

// ReportHosts replaces the daemon's hosts report with hosts.
func (c *Client) ReportHosts(ctx context.Context, hosts []HostInfo) error
```

```go
// internal/launch

// WithHost marks the loop's socket as the tunnel to the host name: another
// machine's daemon. Nothing of its sessions is read as a local file, no
// connect panel opens, and its notes name the host.
func WithHost(name string) Option

// New options field:
host string // the host's name when the socket is a tunnel; "" for this machine's daemon
```

```go
// cmd/marshal

var hostOption = launch.WithHost        // indirected so a test can read the name
var tunnelsRunner = tunnels.Run         // tests replace it
var fleetHostWait = 5 * time.Second     // how long add and remove wait for the daemon

// hostTunnel reads the local daemon's report of the host name.
func hostTunnel(ctx context.Context, home, name string) (daemon.HostInfo, error)

// waitForHost waits up to fleetHostWait for the local daemon's report to list
// name (present) or not list it, and returns silently either way.
func waitForHost(ctx context.Context, home, name string, present bool)

func runDaemonTunnels(ctx context.Context, args []string, stderr io.Writer) error

// daemonHostLine renders one host for `marshal daemon status`.
func daemonHostLine(h daemon.HostInfo) string
```

```dart
// app/lib/src/daemon/status.dart

/// One host the daemon keeps a tunnel to, as its status document lists it.
class DaemonHost {
  const DaemonHost({
    required this.name,
    required this.ssh,
    required this.state,
    required this.socket,
    this.lastError = '',
    this.since = 0,
  });
  final String name, ssh, state, socket, lastError;
  final int since; // Unix milliseconds
  factory DaemonHost.fromJson(Map<String, dynamic> json);
}

// New DaemonInfo field, defaulting to const []:
final List<DaemonHost> hosts;
```

```dart
// app/lib/src/client/host.dart — new field on HostSpec and on Host:
/// The SSH destination this host is reached over, or null.
final String? destination;

// app/lib/src/model/fleet_store.dart

/// A host's tunnel as the local daemon last reported it.
@immutable
class HostTunnel {
  const HostTunnel({required this.state, this.lastError = '', this.since});
  final String state; // 'connecting', 'live' or 'down'
  final String lastError;
  final DateTime? since;
}

// New HostView fields and method:
final String? destination;
final HostTunnel? tunnel;
HostView withTunnel(HostTunnel? tunnel);

// app/lib/src/model/hosts_model.dart
class HostDraft {
  const HostDraft({this.name = '', this.destination = ''});
  final String name, destination;
}
// New HostsModel field and constructor parameter (both constructors):
final Future<Map<String, HostTunnel>> Function()? tunnels;

// app/lib/src/client/fleet_profiles.dart — FleetProfiles becomes:
bool get supported;
Future<void> add(String name, String destination); // marshal fleet add NAME DEST
Future<void> remove(String name);
```

## Task 1: The contract carries the daemon's hosts

**Goal:** `contract.DaemonInfo` has `hosts`, the app's Dart module is
regenerated, and the shared status document lists one host.

**Files:** `web/bridge/contract/daemon.go`,
`app/lib/src/client/contract.g.dart` (generated),
`test/contract/daemon-status.json`.

**Steps:**
1. In `contract/daemon.go`, add `HostState`, its three constants,
   `DaemonHost` and `DaemonHostsReport` after `DaemonRefresh` (`:40`), as
   Contracts declares them.
2. Add `Hosts` to `DaemonInfo` after `Join` (`:21`), with its comment from
   Contracts.
3. Regenerate the Dart module: `go test ./web/bridge/contract/dartgen -run
   TestDartContractIsCurrent -update`.
4. In `test/contract/daemon-status.json`, add a `hosts` array to `daemon`
   holding one host: name `box`, ssh `me@box`, state `live`, socket
   `/home/u/.local/share/marshal/daemon/hosts/box.sock`, since
   `1763300000000`. Then run `go test ./cmd/marshal -run
   TestTheStatusDocumentIsCurrent -update`, so the file takes the encoder's
   field order.

**Verify:**
- `go test ./web/bridge/contract/... -count=1` passes without `-update`.
- `go test ./cmd/marshal -run TestTheStatusDocumentIsCurrent -count=1` passes
  without `-update`.
- `go test ./web/bridge -run DaemonRoute -count=1` passes: `GET /api/daemon`
  encodes `hosts` as `null` until Task 2 fills it, and no test pins the body.
- `go build ./...` succeeds.

## Task 2: The socket takes the tunnel helper's report

**Goal:** `PUT /api/daemon/hosts` on the socket replaces the daemon's hosts,
and `GET /api/daemon` reports them. The network listener answers the route 404.
A helper's exit is logged under its own name.

**Files:** `web/bridge/daemonhosts.go` (new), `web/bridge/daemoninfo.go`,
`web/bridge/http.go`, `web/bridge/helper.go`, `web/bridge/daemonhosts_test.go`
(new), and one wiring line in `cmd/webbridge/main.go`.

**Steps:**
1. In `http.go`, add `hostsMu` and `hosts` to `Server` after `refresh`
   (`:57`).
2. Create `daemonhosts.go` with `daemonHostsPath`, the `DaemonHost` alias,
   `reportHosts` and `reportedHosts`, as Contracts declares them.
   - `reportHosts` answers any method but PUT with 405, `Allow: PUT`, and
     `{"error": "the hosts report is a PUT"}`, as `pinProject`
     (`pins.go:27`) does for POST.
   - It decodes a `contract.DaemonHostsReport` with `decodeJSON`.
   - It refuses a host with an empty name, or with a state other than the
     three constants, with 400 naming the host. A refused report changes
     nothing.
   - Otherwise it stores a copy sorted by name (an absent list is stored as
     empty) and answers 204.
   - `reportedHosts` copies the stored slice under `hostsMu` and answers
     `[]DaemonHost{}` when there is none.
3. In `Server.daemon` (`daemoninfo.go:43`), set `out.Hosts =
   s.reportedHosts()` after the refresh block (`:56–60`). Add "and so are the
   hosts" to the comment above the function.
4. In `SocketHandler` (`http.go:205`), dispatch `daemonHostsPath` to
   `reportHosts` right after the restart route (`:219–225`), with the comment:
   "The tunnel helper's report is the socket's own: the network listener
   answers it 404 (one-daemon D7)."
5. In `helper.go`:
   - add `Name` to `Helper` first among its fields;
   - change the warning at `:70` to `"webbridge: helper exited"`, with
     `"helper", h.Name` before `"status"`;
   - change the type's comment to say that `Name` labels the log.
6. In `cmd/webbridge/main.go`, the refresh helper's literal (`:983`) gains
   `Name: "refresh"`.

**Tests** (new, in `daemonhosts_test.go`; build the server as
`TestTheDaemonRouteAnswersItsInfo` in `daemoninfo_test.go` does):
- `TestTheSocketTakesAHostsReport`: a PUT of two hosts, out of name order,
  answers 204. `GET /api/daemon` then lists both, sorted, with every field as
  sent.
- `TestAHostsReportReplacesTheLastOne`: a second report of one host leaves
  only that host.
- `TestDaemonInfoListsNoHostsBeforeAReport`: the body of `GET /api/daemon`
  holds `"hosts":[]`.
- `TestAHostsReportIsAPut`: a GET of `/api/daemon/hosts` answers 405 with
  `Allow: PUT`.
- `TestAHostsReportWithAnUnknownStateIsRefused`: state `"up"` answers 400
  naming the host, and an earlier report is still what `GET /api/daemon`
  shows.
- `TestTheNetworkListenerHasNoHostsReport`: the same PUT through `ServeHTTP`,
  with the server's token, answers 404.

**Verify:** `go test ./web/bridge -run 'HostsReport|NoHosts|DaemonRoute|Helper' -count=1`
passes, and so does `go build ./cmd/webbridge`.

## Task 3: `fleet.toml` holds host tables

**Goal:** `Load` reads `[hosts.NAME]` tables into `File.Hosts`, with D6's
checks. `AddHost` and `RemoveHost` edit them. Server profiles still load,
until Task 14.

**Files:** `internal/fleetprofile/profile.go`, `internal/fleetprofile/edit.go`,
`internal/fleetprofile/profile_test.go`, `internal/fleetprofile/edit_test.go`.

**Steps:**
1. Add `Host` and `File.Hosts`, as Contracts declares them. `Load`'s empty
   `File` and `decode` make the map, as they do for `Servers`.
2. Add `rawHost`, and `Hosts map[string]rawHost` with tag `hosts` to
   `rawFile`. `rawHost` decodes `ssh`, `marshal` and `notify` (`*bool`). It
   also decodes the five retired keys as pointers, so each can be refused by
   name: `url`, `ca_file`, `token_cmd` and `relay` as `*string`, and `token`
   as `*string`.
3. Give `checkName` a first parameter, the table (`"servers"` or `"hosts"`).
   Every message names `<table>.<name>` and says "a host's name" when the
   table is `hosts`. Update the two existing callers to pass `"servers"`.
4. Add `buildHost(name string, raw rawHost) (Host, error)`, called from
   `decode` for each name in sorted order. It checks:
   - the name, with `checkName("hosts", name)`;
   - a retired key, if any is set: `fleet.toml: hosts.<name>.<key>: a host is
     reached over SSH and takes no <key>; set ssh = "you@machine"
     (docs/one-daemon/design.md D6)`. Keys are checked in the order `url`,
     `relay`, `ca_file`, `token_cmd`, `token`;
   - `ssh`, which must be set: `fleet.toml: hosts.<name>.ssh: a host needs an
     ssh destination, as ssh takes it: "you@machine" or a Host from
     ~/.ssh/config`;
   - `ssh` may not start with `-`, so ssh never reads it as an option: `... is
     read by ssh as an option; a destination never starts with "-"`;
   - `ssh` may not hold whitespace or a control character;
   - `marshal`, when set, must be absolute or start with `~/`, and may not hold
     a control character.

   `Notify` is true unless the table sets `notify = false`.
5. In `decode`, a name present under both `servers` and `hosts` is refused:
   `fleet.toml: <name> is both a server and a host; keep the [hosts.<name>]
   table`.
6. Add `LookupHost`, as `Lookup` (`:288`) is written. With no hosts, its
   message shows a starter table:
   `[hosts.<name>]` then `ssh = "you@machine"`.
7. In `edit.go`:
   - Replace `serverHeaderRe` and `serverHeader` with `tableHeaderRe` and
     `tableHeader(line string) (table, name string, ok bool)`. The pattern
     matches `servers` or `hosts`, captured as the table, with the same
     quoting rules.
   - `removeTable` takes `(path, table, name string, src []byte)`, and its
     refusal names `[<table>.<name>]`. `RemoveServer` passes `"servers"`.
8. Add `AddHost`, as `AddServer` (`edit.go:18`) does: check the name and the
   ssh and marshal rules (call `buildHost` on the equivalent raw table), then
   `Load`, then refuse a name already under `Servers` or `Hosts`, then the
   missing-file branch, append, `decode`, and `writePrivate`. Its table is
   written by `appendHostTable(src []byte, h Host) []byte`:
   - a blank line;
   - `[hosts.<headerKey(name)>]`;
   - `ssh = <tomlString>`;
   - `marshal = <tomlString>`, only when set;
   - `notify = false`, only when off.
9. Add `RemoveHost(path, name)`: `Load`, then `LookupHost`, then
   `removeTable(path, "hosts", name, src)`, `decode`, and `writePrivate`. It
   touches no keychain.

**Tests** (new; `profile_test.go` unless named):
- `TestAHostTableLoads`: `ssh`, `marshal` and the default `notify` true are
  read, and `notify = false` is false.
- `TestAHostNeedsAnSSHDestination`.
- `TestAHostDestinationMayNotLookLikeAnOption`: `-oProxyCommand=x` is refused,
  and the message names the option rule.
- `TestAHostDestinationMayNotHoldWhitespace`: `"me@box extra"` and a tab are
  refused.
- `TestAHostsMarshalIsAbsoluteOrHome`: `/opt/marshal/bin/marshal` and
  `~/.local/bin/marshal` load, and `bin/marshal` is refused.
- `TestAHostRefusesTheServerKeys`: a table per key (`url`, `relay`, `ca_file`,
  `token_cmd`, `token`). Each error names the key and contains
  `ssh = "you@machine"`.
- `TestANameIsNotBothAServerAndAHost`.
- `TestLookupHostNamesTheFileAndItsHosts`: a missing name lists the hosts,
  and an empty file shows the starter table.
- `TestAddHostAppendsATableAndKeepsComments` (`edit_test.go`): an existing
  commented file gains the table at its end, mode `0600`, and `Load` reads it.
- `TestAddHostWritesMarshalAndNotifyOnlyWhenSet` (`edit_test.go`).
- `TestAddHostRefusesANameAlreadyPresent` (`edit_test.go`): in `hosts`, and
  in `servers`.
- `TestRemoveHostDeletesOnlyItsTable` (`edit_test.go`): the tables before and
  after it, and their comments, are kept.

**Verify:** `go test ./internal/fleetprofile -count=1` passes, the existing
server tests included.

## Task 4: The tunnels package builds and runs ssh

**Goal:** `internal/tunnels` has the ssh commands one tunnel runs, the parse
of the remote status document, and `Exec`, the `Commands` that runs real
processes.

**Files:** `internal/tunnels/ssh.go`, `internal/tunnels/exec.go`,
`internal/tunnels/doc.go` (package comment), `internal/tunnels/ssh_test.go`,
`internal/tunnels/exec_test.go`, `internal/tunnels/main_test.go`. All are new.

**Steps:**
1. `doc.go`: "Package tunnels keeps one SSH forward per fleet.toml host,
   from this machine's daemon directory to that host's daemon socket, and
   reports each one's state to the daemon (one-daemon D7)."
2. `main_test.go` calls `testenv.Run(m)`, as `internal/fleetprofile/main_test.go`
   does.
3. In `ssh.go`, add the functions and the constant as Contracts declares them.
   `ssh` is `"ssh"` when empty.
   - Options shared by both commands, in this order: `-T`, `-o
     BatchMode=yes`, `-o ConnectTimeout=15`, `-o ControlMaster=no`, `-o
     ControlPath=none`.
   - `statusArgv`: ssh, the shared options, the destination, then
     `remoteCommand(h.Marshal)` as one argument.
   - `forwardArgv`: ssh, `-N`, the shared options, `-o
     ExitOnForwardFailure=yes`, `-o StreamLocalBindUnlink=yes`, `-o
     ServerAliveInterval=15`, `-o ServerAliveCountMax=3`, `-L
     <local>:<remote>`, then the destination.
   - `remoteCommand`:
     - `""` is `marshal`, quoted: `'marshal' daemon status --json`;
     - a single quote inside is written `'\''`;
     - a leading `~/` stays outside the quotes, as in `~/'.local/bin/marshal'`.
   - `remoteSocket` decodes `schemaVersion`, `answering` and `daemon.socket`
     into a local struct. It refuses:
     - a schema other than 1: `<name>'s marshal writes status schema <n>; this
       marshal reads 1`;
     - `answering` false: `the daemon on <name> is not answering; start it
       there with marshal daemon start`;
     - an empty socket: `<name>'s daemon names no socket`.
   - `socketPath` refuses a path over `maxSocketPath` bytes with: `the tunnel
     socket <path> is longer than a unix socket allows; give the host a
     shorter name`.
4. In `exec.go`, add `CommandError`, its `Error`, and `Exec()`, returning an
   unexported `execCommands`.
   - `Output` runs `exec.CommandContext` with stdout and stderr captured
     apart. On failure it answers a `*CommandError`: the exit code (`-1` when
     the process did not start or was signalled), and the last non-empty
     stderr line, trimmed.
   - `Start` runs `exec.Command`. Standard input is the null device, as
     `Helper.start` (`web/bridge/helper.go:98`) leaves it, and stdout is
     discarded. A goroutine reads stderr and keeps its last non-empty line. A
     second goroutine waits for the process, records a `*CommandError` with
     the exit code and that line, and closes `Done`. It records one even for
     exit code 0, so a forward that ends always says why.
   - `Stop` sends SIGTERM, waits up to 2 s for `Done`, then kills the process
     and waits again. It is safe to call twice, and after the process has
     exited.

**Tests** (new):
- `TestStatusArgvAsksForTheJSONStatus` (`ssh_test.go`): the exact argument
  list for `me@box` with no marshal.
- `TestRemoteCommandQuotesTheBinary`: `/opt/my marshal/marshal`,
  `/o'dd/marshal` and `~/.local/bin/marshal`.
- `TestForwardArgvForwardsTheSocket`: the exact argument list.
- `TestRemoteSocketReadsTheDocument`: a document as `marshal daemon status
  --json` writes it (`test/contract/daemon-status.json` is the shape) answers
  its `daemon.socket`.
- `TestRemoteSocketSaysTheDaemonIsNotAnswering`.
- `TestRemoteSocketRefusesAnotherSchema`.
- `TestSocketPathRefusesALongName`: a 100-byte name under a short dir is
  refused, and `box` is accepted.
- `TestExecOutputCarriesTheLastLineOfStderr` (`exec_test.go`): `/bin/sh -c
  'echo one >&2; echo two >&2; exit 3'` answers `Code` 3 and `Line` `two`.
- `TestExecOutputAnswersStdout`: `/bin/sh -c 'echo doc'` answers `doc\n`.
- `TestExecProcessStopsWithSIGTERM`: `/bin/sh -c 'sleep 30'` started, `Stop`
  returns within 3 s, and `Done` is closed.
- `TestExecProcessReportsItsExit`: `/bin/sh -c 'echo gone >&2; exit 255'`
  closes `Done`, and `Err` is a `*CommandError` with `Line` `gone`.

**Verify:** `go test ./internal/tunnels -count=1` passes.

## Task 5: The tunnels package keeps every host's tunnel

**Goal:** `tunnels.Run` reconciles running tunnels with `fleet.toml`,
supervises each one through D7's states, and reports every change to the
daemon.

**Files:** `internal/tunnels/tunnels.go` (new) and
`internal/tunnels/tunnels_test.go` (new).

**Steps:**
1. Add `Config` and `Run`, as Contracts declares them, with the defaults the
   field comments name.
2. **Reconciling.** `Run` reports an empty set first, so a report left by a
   previous helper is cleared. Then, at once and every `Poll`, it calls
   `fleetprofile.Load(cfg.FleetFile)`.
   - On an error, it writes `tunnels: <err>` to `Stderr` the first time that
     message is seen, and keeps the running tunnels.
   - On success:
     - a host no longer in the file is stopped, dropped from the state, and
       has its socket file removed (a missing file is fine);
     - a host whose `SSH` or `Marshal` changed is stopped and started again;
     - a new host is started.

     Each change is reported.
3. **Per-host state.** `Run` keeps one `contract.DaemonHost` per host, under a
   mutex.
   - A host starts as `HostConnecting`, with `Since` now and `Socket` from
     `socketPath`.
   - Setting a state updates `Since` only when the state changes.
   - `LastError` is set with `HostDown` and cleared with `HostLive`.
   - Every set signals the reporter.
4. **One tunnel's loop.** Each host runs in its own goroutine with its own
   context. It repeats one attempt until its context ends.
   - After a failed attempt, the host is set to `HostDown` with the error.
   - It then waits the current delay, which starts at `Backoff` and doubles to
     `MaxBackoff`. An attempt that was live for at least one minute resets the
     delay.
   - `connecting` is set only when the host starts. A retry after `down` stays
     `down` until it is live.
5. **One attempt.**
   1. `socketPath(cfg.Dir, name)`. An error ends the attempt.
   2. Run `statusArgv` through `Commands.Output` with a 30 s timeout.
      - A `*CommandError` with `Code` 127 ends the attempt with
        `<line>; set marshal = "/path/to/marshal" in [hosts.<name>]`.
      - Any other error ends it with the error as it is.
   3. `remoteSocket(name, out)`. An error ends the attempt.
   4. A local or remote path that contains `:` ends the attempt: `ssh cannot
      forward a socket path that contains ":": <path>`.
   5. Make `cfg.Dir` with mode `0700`, then `Commands.Start(forwardArgv(...))`.
   6. Every 250 ms, call `Probe` on the local socket with a 2 s timeout.
      - A success sets `HostLive`.
      - The process exiting first ends the attempt with its `Err()`.
      - `LiveWithin` passing without a success stops the process, and ends
        the attempt with `the tunnel to <name> did not answer within
        <LiveWithin>`.
   7. While live, call `Probe` every `Check`.
      - A failure sets `HostDown` with `<name>'s daemon stopped answering:
        <err>`, and leaves ssh running.
      - A later success sets `HostLive` again.
      - The process exiting ends the attempt with its `Err()`.
   8. The context ending stops the process and ends the attempt.
6. **The reporter.** One goroutine reports the whole set, sorted by name,
   whenever it is signalled.
   - Each `Report` gets a 5 s timeout.
   - A failed report is written to `Stderr`, and retried after `Backoff` until
     one lands or a newer signal replaces it.
   - The reporter drops intermediate signals: only the latest set is sent.
7. **Stopping.** When `ctx` ends, `Run` cancels every host, waits for each one
   to stop its process, and returns nil. It does not remove socket files on
   the way out, because the next start replaces them
   (`StreamLocalBindUnlink`).
8. A nil `Report` or `Probe` is an error before anything starts:
   `tunnels: Config.Report is required`, and the same for `Probe`.

**Tests** (new, in `tunnels_test.go`). They use a scripted `Commands`, a
recording `Report`, a `Probe` the test controls, millisecond tuning fields, a
`fleet.toml` written at `0600` in a short temporary directory, and `Dir` under
it.
- `TestRunNeedsReportAndProbe`.
- `TestTheFirstReportIsEmpty`: a file with no hosts gives exactly one report,
  and it is empty.
- `TestALiveTunnelIsReported`: the status call answers a document, and the
  probe succeeds. The reports go `connecting` and then `live`, with `SSH`
  `me@box` and `Socket` `<Dir>/box.sock`. `Start` was given `forwardArgv`
  for that socket and the document's remote socket.
- `TestAFailedStatusCallIsDownWithItsLine`: `Output` fails with `Line`
  `Permission denied (publickey).`, so the host is `down` with that
  `LastError`, and `Start` is never called.
- `TestAMissingMarshalNamesTheSetting`: `Code` 127, and `LastError` contains
  `set marshal =`.
- `TestAnExitedForwardIsRestartedWithBackoff`: the process exits at once,
  every time. `Start` is called again, the gaps between starts do not shrink,
  and none is below `Backoff`.
- `TestARetryStaysDownUntilLive`: after the first `down`, no later report says
  `connecting`.
- `TestAForwardThatNeverAnswersIsStopped`: the probe always fails, so after
  `LiveWithin` the process is stopped, and the host is `down` with `did not
  answer`.
- `TestALiveTunnelWhoseDaemonStopsIsDown`: the probe fails after `live`, so
  the host is `down` and the process is not stopped. When the probe succeeds
  again, it is `live`.
- `TestAHostAddedToTheFileStartsATunnel`: the host is written into the file
  after `Run` started, and it is reported within a few `Poll`s.
- `TestAHostRemovedFromTheFileIsStopped`: its process is stopped, its socket
  file is removed, and the next report lacks it.
- `TestAChangedDestinationRestartsTheTunnel`: `Start` is called again with the
  new destination.
- `TestAFileThatCannotLoadKeepsTheTunnels`: the file is rewritten at `0644`.
  The tunnel keeps running, and `Stderr` holds the load error once.
- `TestRunStopsEverySSHWhenTheContextEnds`: two hosts. After cancelling, `Run`
  returns nil, and both processes were stopped.

**Verify:** `go test ./internal/tunnels -count=1 -race` passes.

## Task 6: `webbridge --tunnels` supervises the helper

**Goal:** with `--tunnels`, the bridge runs `<marshal-bin> daemon tunnels
--socket <socket>` beside itself, as it runs the refresh helper.

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/local_test.go` and
`cmd/webbridge/main_test.go`.

**Steps:**
1. Add a `tunnels bool` field to `config`, after `oauthRefresh` (`:117`). Its
   comment: "runs `<marshal-bin> daemon tunnels` beside the bridge
   (one-daemon D7)".
2. Add the flag after `--oauth-refresh` (`:206`), as that one is built:
   - the default is read from `WEBBRIDGE_TUNNELS` with `strconv.ParseBool`,
     `false` when unset, and an unreadable value is an error naming the
     variable;
   - the help is "run <marshal-bin> daemon tunnels alongside the bridge";
   - set `tunnels` in the `config` literal (`:213–216`).
3. After the refresh helper's block (`:978–997`), add a block of the same
   shape for `cfg.tunnels`. It runs a `bridge.Helper` with `Name: "tunnels"`,
   `Argv: []string{cfg.marshalBin, "daemon", "tunnels", "--socket",
   cfg.socket}` and `Stderr: stderr`, on its own cancellable context, and
   waits for it in a `defer`. It is not passed to `SetRefreshHelper`: the
   hosts are its report. After plan 1 this block is unconditional, like the
   refresh helper's.

**Tests** (in `local_test.go`):
- `TestTheDaemonSupervisesTheTunnelsHelper`, built as
  `TestTheLocalProfileSupervisesTheRefreshHelper` (`:338`) is: with
  `--tunnels` and the helper script, the recorded argv is `daemon tunnels
  --socket <socket>`, and `run` returns nil once its context is cancelled.
- `TestTheDaemonStartsNoTunnelsHelperUnasked`: without `--tunnels`, the
  script records nothing within 500 ms.
- `TestParseConfigReadsTheTunnelsFlag` (`main_test.go`): unset is false,
  `--tunnels` is true, `WEBBRIDGE_TUNNELS=1` is true, and
  `WEBBRIDGE_TUNNELS=maybe` is an error.

**Verify:** `go test ./cmd/webbridge -run 'Tunnels|RefreshHelper|ParseConfig' -count=1`
passes.

## Task 7: The daemon client reports hosts, and the unit runs the tunnels

**Goal:** `daemon.Client.ReportHosts` PUTs a report on the socket, and the
installed unit passes `--tunnels`.

**Files:** `internal/daemon/client.go`, `internal/daemon/daemon.go`,
`internal/daemon/client_test.go`, `internal/daemon/units_test.go`.

**Steps:**
1. In `client.go`, add `HostInfo` beside the other aliases (`:34–58`), and
   `daemonHostsPath` to the route constants (`:23–31`).
2. Add `ReportHosts`, as Contracts declares it.
   - It PUTs `contract.DaemonHostsReport{Hosts: hosts}` as JSON to
     `"http://"+c.host+daemonHostsPath`, with `Content-Type:
     application/json`. A nil `hosts` is sent as `[]`.
   - It answers nil on 204.
   - Otherwise it answers the daemon's `error` field when the body has one,
     else the status, as `Pin`'s refusal does (`TestARefusedPinCarriesTheDaemonsSentence`).
3. In `UnitSpec.Args` (`daemon.go:67`), append `"--tunnels"` after
   `"--oauth-refresh"`. Extend the comment there: both helpers are always
   supervised, so both flags are written rather than configured.

**Tests:**
- `TestReportHostsPutsTheWholeSet` (`client_test.go`, as `TestPinPostsTheRoot`
  at `:144` is built): the method, path, content type and body.
- `TestReportHostsSendsAnEmptyListForNil`.
- `TestARefusedReportCarriesTheDaemonsSentence`.
- `TestThePlistNamesEverything` and `TestTheSystemdUnitNamesEverything`
  (`units_test.go:39`, `:131`) expect `--tunnels` last.

**Verify:** `go test ./internal/daemon -count=1` passes.

## Task 8: `marshal daemon tunnels`, and status lists the hosts

**Goal:** `marshal daemon tunnels --socket PATH` runs the helper body, and
`marshal daemon status` prints one line per host.

**Files:** `cmd/marshal/daemon.go` and `cmd/marshal/daemon_test.go`.

**Steps:**
1. In `runDaemon` (`daemon.go:83`), dispatch `"tunnels"` to
   `runDaemonTunnels(ctx, rest, stderr)` beside `"refresh"` (`:107`). Leave it
   out of both usage strings, as `refresh` is, and say so in the comment above
   `runDaemon`.
2. Add `tunnelsRunner` and `runDaemonTunnels`, as Contracts declares them,
   beside `runDaemonRefresh`.
   - Its flag set takes `--socket`, which is required: `marshal daemon
     tunnels: --socket is required`. Extra arguments are refused, as
     `runDaemonRefresh` refuses them.
   - It finds the home with `daemonHome()` and installs `daemonSignalContext`,
     as `runDaemonRefresh` does.
   - It calls `tunnelsRunner` with a `tunnels.Config` holding:
     - `FleetFile`: `fleetprofile.Path(home)`;
     - `Dir`: `filepath.Join(filepath.Dir(socket), "hosts")`;
     - `Report`: `daemon.NewClient(socket).ReportHosts`;
     - `Probe`: a function that calls `daemon.NewClient(s).Info(ctx)` and
       answers its error;
     - `Stderr`: `stderr`.
3. Add `daemonHostLine`, as Contracts declares it:
   - `host <name> (<ssh>): <state>`;
   - with ` (<lastError>)` appended when the state is `down` and the error is
     not empty, as `daemonJoinLine` (`:952`) appends its error.
4. In `runDaemonStatus` text mode, print `daemonHostLine` for each of
   `info.Hosts` after the join line (`:698–700`).

**Tests** (in `daemon_test.go`):
- `TestDaemonTunnelsRunsTheHelperOnTheSocket`: `tunnelsRunner` is replaced,
  and the `Config` it receives has the fleet file under the test home and the
  `hosts` directory beside the socket.
- `TestDaemonTunnelsNeedsTheSocket`.
- `TestDaemonStatusListsTheHosts`: a scripted `Info` with a live host and a
  down host (with an error) prints both lines.

**Verify:** `go test ./cmd/marshal -run 'DaemonTunnels|DaemonStatus|StatusDocument' -count=1`
passes.

## Task 9: The fleet loop runs on a host's tunnel

**Goal:** `launch.Fleet` with `launch.WithHost(name)` runs the fleet loop on
a tunnel socket as a remote host's. Its sessions do not share the filesystem,
it opens no connect panel, its notes name the host, and its fleet client is
named after the host.

**Files:** `internal/launch/launch.go`, `internal/launch/fleet.go`,
`internal/launch/daemon.go`, `internal/launch/fleet_test.go`, and
`test/daemon/host_fleet_test.go` (new).

**Steps:**
1. Add `host` to `options` beside `remote` (`launch.go:83`), and `WithHost`
   to `fleet.go`, as Contracts declares them.
2. Add `func (o *options) onThisMachine() bool` in `daemon.go`, answering
   `o.remote == nil && o.host == ""`. Use it:
   - for the `local` argument of `daemonTUIOptions` (`:417`);
   - for `SharedFilesystem` (`:854`);
   - in `versionNote` (`:773`), whose remote note becomes `This marshal is
     <ours>; <who>'s agent is <theirs>.`, where `<who>` is the host's name
     when `o.host` is set, else `the server`.
3. In `fleetClientFor` (`daemon.go:654–661`), when `l.opts.host` is set, set
   the socket transport's `Server` to the host's name before `fleetclient.New`.
4. In `Fleet` (`fleet.go:61`), when `runOpts.host` is set, the probe's
   failure reads: `launch: <host>'s daemon does not answer through its tunnel
   at <socket>: <err>; marshal daemon status shows the tunnel`.

**Tests:**
- In `fleet_test.go`, add `runHostFleet(t, fake, dir, script, opts...)`
  beside `runServerFleet` (`:415`). It runs `Fleet` on a socket path under
  `dir`, with `withDaemonClient(fake)`, `withWorkingDir(dir)`,
  `WithHost("box")` and `WithProgramRunner(script.run)`, then `opts`.
- The host twins of four server tests run through it with
  `loopFleetScript`:
  - `TestAHostFleetDialsWithoutASharedFilesystem`;
  - `TestAHostFleetNeverOpensTheConnectPanel`;
  - `TestAHostFleetsVersionNoteNamesTheHost` (the note contains `box's agent`);
  - `TestAHostFleetNamesItsClientAfterTheHost`.

  Each copies its assertions from its `TestAServerFleet…` counterpart.
- `TestAHostFleetThatCannotBeReachedNamesTheTunnel`: a daemon client whose
  `Info` fails, so the error contains `box's daemon does not answer through
  its tunnel`.
- `TestAHostFleetOpensAnAgentAndComesBack` (`test/daemon/host_fleet_test.go`):
  - It builds `newDaemonHarness`, and serves the harness's handler
    (`h.srv.Handler`) on a second unix socket, `box.sock` in a new short
    temporary directory, with `bridge.ListenLocal` and `bridge.PeerChecked`.
    That socket stands in for the tunnel.
  - It uses the provider fixture (`newProviderFixture(t, nil)` and
    `useProvider`) and spawns an agent with `spawnFleetAgent`, as
    `TestTheServerFleetOpensAnAgentAndComesBack`
    (`server_profile_test.go:270`) does.
  - It runs `launch.Fleet` on the second socket with `launch.WithHost("box")`,
    `launch.WithNotify(false)` and that test's program runner.
  - The fleet runs twice, and the session screen is the agent's.

**Verify:** `go test ./internal/launch -count=1` and
`go test ./test/daemon -run HostFleet -count=1` pass.

## Task 10: `marshal fleet` speaks hosts

**Goal:** `marshal fleet add NAME DEST [--marshal PATH] [--no-notify]`,
`marshal fleet remove NAME` and `marshal fleet --host NAME` work on host
tables and tunnels. `--server`, `login`, `token`, `servers` and `--json` are
retired with messages that say what replaced them.

**Files:** `cmd/marshal/main.go`, `cmd/marshal/fleet_cli_test.go`,
`cmd/marshal/main_test.go`.

**Steps:**
1. Delete `fleetServerRunner` (`:32`), `fleetLoginStore` (`:36–38`),
   `fleetTokenStore` and `fleetTokenRunner` (`:45–46`), with their comments.
   Keep `fleetStdoutIsTerminal`. Add `hostOption` and `fleetHostWait`, as
   Contracts declares them.
2. In `runFleet` (`:248`):
   - Replace `--server` and `--server=` with `--host` and `--host=`, with the
     same refusals (empty, given twice), saying "host".
   - `--server` in either form is refused: `marshal fleet: --server is
     retired; a remote daemon is a host: marshal fleet --host NAME
     (docs/one-daemon/design.md D8)`.
   - Delete `--json` and every refusal that mentions it. `--json` now falls
     to the unexpected-flag case.
   - `add`'s pass-through (`:291–298`) hands on `--marshal` (with its value,
     in both spellings) and `--no-notify`, in place of `--ca-file` and
     `--relay`.
   - The subcommands are `add` and `remove`, each refusing `--host` as it
     refuses `--server` today.
   - `login` and `token` are refused: `marshal fleet <sub>: retired; a host is
     reached over SSH and needs no token`.
   - `servers` is refused: `marshal fleet servers: retired; marshal daemon
     status lists the hosts`.
3. The screen path (`:359–378`): load the file as today.
   - With no `--host`, run the local fleet as today.
   - With `--host`:
     1. `file.LookupHost(name)`;
     2. `hostTunnel(ctx, home, name)`;
     3. `fleetRunner(ctx, stdout, h.Socket, notifyOption(host.Notify),
        hostOption(name))`.
4. Add `hostTunnel`, as Contracts declares it. It reads
   `daemonClientFor(config.DaemonSocketPath(home)).Info` with
   `daemonStatusTimeout`.
   - The daemon not answering: `marshal fleet: the local daemon is not
     answering on <socket>, and it keeps the tunnel to <name>; marshal daemon
     start`.
   - No host of that name in `info.Hosts`: `marshal fleet: the daemon reports
     no tunnel to <name>; wait a few seconds after adding it, or run marshal
     daemon install if the daemon predates hosts`.
   - A state other than live: `marshal fleet: <name> is <state>` plus `:
     <lastError>` when there is one.
5. Delete `FleetServers`, `FleetServer`, `runFleetServers`, `runFleetToken`,
   `runFleetLogin` and `readFleetToken`. Drop the imports nothing uses any
   more (`bufio`, `credentials`, `sort`, `encoding/json` and `errors`, if
   unused).
6. Rewrite `runFleetAdd` for `NAME DEST [--marshal PATH] [--no-notify]`.
   - It parses positionals and flags as it does today. `--marshal` takes a
     value, which may not start with `-`, and is refused when given twice.
   - It needs exactly two positionals: `marshal fleet add: a host name and an
     ssh destination are required`.
   - It calls `fleetprofile.AddHost`, then `waitForHost(ctx, home, name,
     true)`.
   - It writes: `Added <name>. marshal fleet --host <name> opens its fleet
     once its tunnel is live; marshal daemon status shows it.`
7. `runFleetRemove` calls `fleetprofile.RemoveHost`, then `waitForHost(ctx,
   home, name, false)`, and writes `Removed <name>.` as today.
8. Add `waitForHost`, as Contracts declares it. It polls the local daemon's
   `Info` every 200 ms until `fleetHostWait` passes, and returns at once when
   the daemon does not answer.
9. Tests in `fleet_cli_test.go`:
   - Delete `TestFleetServersJSONListsProfilesWithoutTokens`,
     `TestFleetTokenPrintsResolvedToken`, `TestFleetTokenRefusesATerminal`,
     `TestFleetTokenUnknownProfileExitsOne`, `TestFleetAddWithARelay`,
     `TestFleetServersJSONReportsTheRelay` and
     `TestTheFleetScreenRefusesARelayedProfile`.
   - `TestFleetAddWritesAProfile` becomes `TestFleetAddWritesAHostTable`.
   - `TestFleetAddTakesACAFileAndNoNotify` becomes
     `TestFleetAddTakesMarshalAndNoNotify`.
   - `TestFleetRemoveDeletesTheProfileAndToken` becomes
     `TestFleetRemoveDeletesTheHostTable`.
   - `TestFleetAddRefusesTheServerFlag` refuses `--host` instead.
   - Every test sets `fleetHostWait` to zero.
10. Tests in `main_test.go`:
    - `TestFleetServerRoutesToTheServerRunner` becomes
      `TestFleetHostRunsOnTheTunnelSocket`. A scripted daemon client
      reports `box` live on a socket path, and `fleetRunner` receives that
      socket. `hostOption`, replaced for the test, records `box`.
    - `TestFleetServerNeedsAKnownName` becomes `TestFleetHostNeedsAKnownName`.
    - `TestFleetServerWithoutAFileGivesAStarter` becomes
      `TestFleetHostWithoutAFileGivesAStarter`. The starter shows `[hosts.`.
    - `TestFleetPassesTheFilesNotify` uses a host table and the tunnel stub.
    - New `TestFleetHostRefusesAHostThatIsDown`: the message holds the state
      and the last error.
    - New `TestFleetHostRefusesAHostTheDaemonDoesNotReport`.
    - New `TestFleetServerIsRetired`.
    - New `TestFleetLoginTokenAndServersAreRetired`.
    - `TestFleetRefusesArgumentsAndEmbedded` (`:794`): its `--server` and
      `--json` cases become `--host` cases.

**Verify:** `go test ./cmd/marshal -count=1` passes.

## Task 11: The server fleet goes from launch

**Goal:** `ServerFleet`, `remoteTarget` and the token options no longer exist.
The loop's two kinds are this machine's daemon and a host.

**Files:** `internal/launch/fleet.go`, `internal/launch/launch.go`,
`internal/launch/daemon.go`, `internal/launch/fleet_test.go`, and
`test/daemon/server_profile_test.go` (deleted).

**Steps:**
1. Delete `remoteTarget`, `withTokenStore`, `withTokenRunner` and
   `ServerFleet` (`fleet.go:18–35`, `:94–139`), and the `fleetprofile` and
   `daemon` imports they used.
2. Delete `remote`, `tokenStore` and `tokenRunner` from `options`
   (`launch.go:81–87`).
3. In `daemon.go`:
   - `daemonClientFor` loses its `remote` branch (`:137–139`) and its comment
     clause;
   - `fleetClientFor` loses its `remote` branch (`:659–661`), and its comment
     names the host in place of the server profile;
   - `onThisMachine` answers `o.host == ""`;
   - `versionNote`'s remote note always names the host.
4. In `fleet_test.go`, delete `serverProfile`, `countingToken`,
   `runServerFleet` and every `TestAServerFleet…` test. Their host twins from
   Task 9, `runHostFleet` and `loopFleetScript` stay.
5. Delete `test/daemon/server_profile_test.go`. Task 9's
   `TestAHostFleetOpensAnAgentAndComesBack` covers its loop.

**Verify:**
- `go test ./internal/launch ./test/daemon -count=1` passes.
- `grep -rn 'ServerFleet\|remoteTarget\|withToken' internal/ cmd/ test/`
  prints nothing.

## Task 12: The daemon client has no remote mode

**Goal:** `daemon.Remote` and every token and remote branch in the daemon
client are gone. A client is a socket's.

**Files:** `internal/daemon/client.go`, `internal/daemon/client_test.go`,
`internal/daemon/marshal_enrollment_test.go`.

**Steps:**
1. Delete `Remote` and `remoteTLSConfig` (`:103–184`), and the `crypto/tls`
   and `net/url` imports if nothing else uses them.
2. Delete the `token` and `remote` fields (`:74–79`) with their comments, and
   reword `Client`'s comment to "talks HTTP to a daemon over its socket".
3. Delete the attach's token block (`:523–533`), the enrollment's remote block
   (`:806–818`) and the access request's remote block (`:1001–1013`).
4. `marshalInstanceOf` (`:767`) always prefers `info.Socket` when it is set.
5. Delete `MarshalInstance.Remote` (`:703`) and its assignment (`:780`).
   Reword the type's comment: an instance is named by its canonical socket
   path.
6. Update the comments on `DiscoverMarshal`, `EnrollMarshal` and
   `marshalAccessDo`, which describe a remote profile.
7. Tests:
   - delete `TestARemoteAttachUpgradesOverTLS`,
     `TestARemoteAttachOverLoopbackHTTP`,
     `TestARemoteAttachRefusesAnUntrustedCertificate`,
     `TestARemoteAttachTokenFailureDialsNothing`,
     `TestARemoteAttachOffersHTTP1` and `TestRemoteRefusesANonHTTPScheme`
     (`client_test.go:863` to the end of the test at `:1095`), and any
     helper only they use;
   - delete `TestMarshalEnrollmentRemoteRequiresAuth`
     (`marshal_enrollment_test.go:59`);
   - drop `Remote: true` from the `ResolveMarshalInstance` test's second
     instance (`:160`).

**Verify:**
- `go test ./internal/daemon -count=1` passes.
- `go build ./...` succeeds.
- `grep -n 'Remote\b\|c.token\|c.remote' internal/daemon/client.go` prints
  nothing.

## Task 13: The fleet client has no HTTPS transport

**Goal:** `fleetclient.HTTPS`, `Transport.Token` and `authorize` are gone.
Every transport is a daemon socket's.

**Files:** `internal/fleetclient/client.go`, `internal/fleetclient/actions.go`,
`internal/fleetclient/client_test.go`, `internal/fleetclient/actions_test.go`.

**Steps:**
1. Delete `tlsHandshakeTimeout`, `HTTPS` (`client.go:60–83`) and the
   `crypto/tls` import.
2. Delete `Transport.Token` (`:34–35`) and `authorize` (`:508`), and the
   three calls to it (`client.go:489`, `:528`, `actions.go:55`).
3. Reword `Transport.Server`'s comment: "the name the screen shows: `local`,
   or a host's". Leave `Local`, the 401 handling and the rejected state as
   they are: plan 3 deletes them.
4. Tests:
   - delete `TestHTTPSReadsABridgeOverTLS`, `TestHTTPSNeverReadsTheDaemon`
     and `TestAnActionSendsTheToken`, and `fakeBridge.serverTLSConfig` and
     `authHeaders` if nothing else uses them;
   - `actionBridge.client` takes no argument, and each caller drops its
     `nil`.

**Verify:**
- `go test ./internal/fleetclient -count=1` passes.
- `grep -rn 'HTTPS(\|Token:\|authorize' internal/fleetclient` prints nothing.

## Task 14: Server profiles go from `fleet.toml`

**Goal:** `fleetprofile` holds hosts and `[local]` only. A `[servers.*]` table
is refused with D6's message, and the token and TLS code is deleted.

**Files:** `internal/fleetprofile/profile.go`, `internal/fleetprofile/edit.go`,
`internal/fleetprofile/token.go` (deleted), `internal/fleetprofile/tls.go`
(deleted), and their tests: `profile_test.go`, `edit_test.go`, `token_test.go`
(deleted), `tls_test.go` (deleted).

**Steps:**
1. Delete `token.go`, `tls.go`, `token_test.go` and `tls_test.go`.
2. In `profile.go`:
   - Delete `Server`, `File.Servers`, `rawServer`, `buildServer`,
     `checkRelay`, `checkURLOrRelay`, `checkURL`, `isLoopbackHost` and
     `Lookup` (`:35–43`, `:64–71`, `:160–297` less `checkName`), and the
     `net` and `net/url` imports.
   - `rawFile.Servers` becomes `map[string]any` with tag `servers`. In
     `decode`, a non-empty map is refused, naming its first name in sorted
     order:

     ```text
     fleet.toml: servers.<name>: server profiles are retired; a remote
     daemon is a host reached over SSH:

     [hosts.<name>]
     ssh = "you@machine"

     (docs/one-daemon/design.md D6)
     ```
   - Delete Task 3's both-tables refusal.
   - `checkName` loses its table parameter and always says `hosts.<name>` and
     "a host's name".
   - The package comment says: fleet.toml holds the hosts the daemon reaches
     over SSH (one-daemon D6) and the `[local]` table; it holds no secret, and
     is kept private all the same.
3. In `edit.go`:
   - delete `AddServer`, `RemoveServer`, `deleteToken`, `appendTable` and the
     `credentials` import;
   - `tableHeaderRe` matches `hosts` only, and `tableHeader` answers the name;
   - `removeTable` loses its table parameter.
4. Tests:
   - delete every server-profile test in `profile_test.go` and
     `edit_test.go`, and keep the host tests from Task 3;
   - new `TestAServersTableIsRetired`: the message holds `[hosts.studio]` and
     `ssh = "you@machine"`;
   - new `TestAServersTableIsRetiredEvenWhenEmptyOfKeys`: `[servers.x]` with
     no keys is refused the same way.

**Verify:**
- `go test ./internal/fleetprofile ./cmd/marshal ./internal/launch -count=1`
  passes.
- `grep -rn 'fleetprofile\.\(Server\|Token\|TLSConfig\|KeychainKey\|OpenStore\)' --include='*.go' .`
  prints nothing.

## Task 15: The native Studio discovers hosts from the daemon

**Goal:** native discovery lists the local daemon and then each host in its
status document, each dialled over its tunnel socket. `serverTransport` and
`TokenProvider` are gone.

**Files:** `app/lib/src/daemon/status.dart`, `app/lib/src/client/host.dart`,
`app/lib/src/client/host_discovery_io.dart`, `app/lib/src/client/transport.dart`,
`app/lib/src/client/transport_io.dart`, `app/lib/src/client/transport_stub.dart`,
`app/test/status_test.dart`, `app/test/client/host_discovery_io_test.dart`,
`app/test/client/transport_io_test.dart`, `app/test/client/host_registry_test.dart`.

**Steps:**
1. In `status.dart`, add `DaemonHost`, as Contracts declares it, and
   `DaemonInfo.hosts`.
   - `fromJson` reads `hosts` with `.map(...).toList()` forced, as
     `children` is read (`:114–116`), and an absent list is `const []`.
   - `DaemonHost.fromJson` reads each field with a `?? ''` (or `?? 0`)
     default.
2. In `host.dart`, add `destination` to `HostSpec` and to `Host`, as
   Contracts declares it. `HostRegistry` passes `spec.destination` where it
   builds the `Host` (`:145–151`).
3. In `host_discovery_io.dart`:
   - `discoverHosts` reads `cli.status()` once, inside the `try` that
     `_localHost` has today (`:32–50`). Its three exceptions answer an empty
     list.
   - With no answering daemon, or no socket in its document, it answers an
     empty list, because the hosts are that daemon's tunnels.
   - Otherwise it answers the local host, built as `_localHost` builds it,
     then one `HostSpec` per entry in `status.daemon!.hosts` whose socket is
     not empty: its name, kind `HostKind.server`, `destination` from its
     `ssh`, and `BridgeClient(unixTransport(socket))`.
   - Delete `_localHost`, `_serverHosts`, `_fleetToken` and `_run`, and every
     import left unused (`dart:convert`, `dart:io` and
     `relayed_transport.dart`).
   - Rewrite the doc comment: hosts come from the local daemon's status
     document, which names each tunnel's socket (one-daemon D9). The app runs
     no `fleet` command and holds no token.
4. In `transport.dart`, `transport_io.dart` and `transport_stub.dart`, delete
   `serverTransport`, `_ServerTransport` and `TokenProvider`. Reword the
   import comment at the top of `transport.dart` ("a native build gets the
   unix socket transport").
5. Tests:
   - `status_test.dart`: `parses the shared fixture` also expects one host,
     `box`, live, with its socket (the fixture from Task 1).
   - `host_discovery_io_test.dart` is rewritten. The fake `marshal` answers
     `daemon status --json` with a document the test names, and fails
     anything else, recording calls as today. Its tests:
     - `the local daemon comes first, then each host`;
     - `a host carries its destination and dials its socket`: a
       `FakeBridge.onUnixSocket` (`app/test/support/fake_bridge.dart:111`)
       at the host's socket answers `/api/agents` through the host's client;
     - `a host with no socket is not listed`;
     - `a silent daemon lists no hosts`;
     - `discovery runs only the status command`.

     Delete both relayed-profile tests.
   - `transport_io_test.dart`: delete `bearer token is sent` and `401 is
     Unauthorized`, which used `serverTransport`.
   - `host_registry_test.dart`: one new test, `a host keeps its spec's
     destination`.

**Verify:**
- `cd app && flutter analyze` reports nothing new.
- `cd app && flutter test test/status_test.dart test/client/ test/boundary_test.dart`
  passes.

## Task 16: Settings › Hosts adds a host by name and SSH destination

**Goal:** the add sheet asks for a name and an SSH destination. A host's
detail shows its destination, its tunnel's state and last error, and Remove.
The native Sign in and the login after an add are gone, and the browser's
relayed sign-in is unchanged.

**Files:** `app/lib/src/client/fleet_profiles.dart`,
`app/lib/src/client/fleet_profiles_io.dart`,
`app/lib/src/client/fleet_profiles_stub.dart`,
`app/lib/src/model/fleet_store.dart`, `app/lib/src/model/hosts_model.dart`,
`app/lib/src/ui/settings/hosts_section.dart`, `app/lib/main.dart`, and tests:
`app/test/client/fleet_profiles_io_test.dart`,
`app/test/model/hosts_model_test.dart`, `app/test/ui/settings_test.dart`,
`app/test/support/screens.dart`.

**Steps:**
1. **`FleetProfiles`** becomes Contracts' interface. Its doc comment drops
   the token sentences.
   - `_ProcessFleetProfiles.add` runs `fleet add NAME DEST`, and `login`
     goes.
   - The browser stub drops `login`, and its `add` takes the new parameters.
2. **`HostTunnel`, `HostView.destination` and `tunnel`, and `withTunnel`** go
   in `fleet_store.dart`, as Contracts declares them.
   - `FleetStore.hosts` (`:44`) passes `destination: owner.destination`.
   - `withTunnel` copies every field and sets `tunnel`.
   - `reachWords` checks the tunnel first:
     - a tunnel `down` reads `down <age>`, the age measured from its `since`;
     - a tunnel `connecting` reads `connecting`;
     - otherwise the words are today's.
3. **`HostsModel`:**
   - `HostDraft` is Contracts' two fields.
   - `hasUnsavedEdits` checks those two fields.
   - Add `tunnels` to both constructors.
   - `add` trims both fields and refuses an empty one: `A host needs a name
     and an SSH destination`. Then `runner.add(name, destination)`, then
     `rediscover`, then notify.
   - Delete `_loginFailures`, `loginFailure` and the login inside `add`.
   - `signIn` keeps its `storeToken` path. With no `storeToken`, it throws
     `This build cannot sign in to a host`.
   - Add `bool get canSignIn => storeToken != null`.
   - `fetch` reads `_read()`, then awaits `tunnels` when it is set, and
     answers each row with `withTunnel(tunnels[row.name])`. A reader that
     throws leaves the rows without a tunnel.
   - Rewrite the class comment: a native build adds and removes hosts with
     `marshal fleet`, and the browser signs in to relayed hosts until plan 3.
4. **`hosts_section.dart`:**
   - The native add row's description: "Another machine's daemon, reached
     over SSH and added to `fleet.toml`". The browser's planned row is
     unchanged.
   - `_kind`:
     - local: unchanged;
     - with a destination: `Over SSH to <destination>`;
     - with `via`: `A server profile, through <via>`, as today;
     - otherwise: `The daemon that served this page`.
   - `HostForm` has two fields:
     - `Name`, hint `box`;
     - `SSH destination`, hint `you@machine, or a Host from ~/.ssh/config`,
       which submits on enter.

     Delete the URL, CA file and Token controllers and fields.
   - `HostDetail`:
     - after `Kind`, `_Fact('SSH', destination)` when there is a destination;
     - `_Fact('Tunnel', words)` when there is a tunnel, the words being its
       state, with ` since <relativeAge> ago` when `since` is set;
     - `_Fact('Last error', lastError)` when the tunnel's `lastError` is not
       empty;
     - the row of buttons shows as today (`server && canAdd`, `:315`).
       Within it, Remove shows only for a host with a destination, and Sign
       out as today;
     - the Token field and Sign in show only when `host.refused &&
       model.canSignIn`, and no longer read `loginFailure`;
     - the remove sheet's text: "The Studio stops showing it, and its table
       is deleted from fleet.toml. Its daemon keeps running."
5. **`main.dart`'s `_Settings`** (`:753–762`) passes `tunnels: kIsWeb ? null
   : () async => {for (final h in (await client.daemon()).hosts) h.name:
   HostTunnel(...)}`.
   - Each `HostTunnel` takes the state's `wire` text and `lastError` (or
     `''`).
   - `since` is `DateTime.fromMillisecondsSinceEpoch(h.since, isUtc: true)`
     when it is positive.
   - Update the comment about `storeToken`: a native build signs in to
     nothing.
6. **`screens.dart`:**
   - `_FixtureFleetProfiles` implements the new interface.
   - `_hostsAdd`'s failure reads `box is already in fleet.toml`.
   - `_hostsSignIn` becomes the browser's case: its model sets `storeToken:
     (host, token) async {}`.
   - Add `_hostsDown`: a host `box` with destination `me@box`, reach
     `unreachable`, and tunnel `down` with last error `Permission denied
     (publickey).`. It is registered as `settings · hosts · down` with the
     same `states` map as its neighbours.
   - `_settings`'s `studio` row gains destination `me@studio`.
7. **Tests:**
   - `fleet_profiles_io_test.dart`: `add passes the name, the url and the ca
     file` becomes `add passes the name and the destination`. Delete `login
     writes the token to stdin and never to argv`.
   - `hosts_model_test.dart`:
     - `add runs add, login, then discovery` becomes `add runs add, then
       discovery`;
     - delete `a failed login keeps the host added and says why` and `sign in
       logs in, then retries`;
     - new `add needs a name and a destination`;
     - new `fetch attaches each host's tunnel`;
     - new `a tunnel reader that fails leaves the rows as they are`;
     - new `a native build cannot sign in`.
   - `settings_test.dart`:
     - `a failed add shows its words and keeps the fields` uses the two new
       fields;
     - `an unauthorized host offers sign in` gives its model a `storeToken`;
     - `remove asks first` gives its host a destination;
     - new `an SSH host shows its destination, tunnel and last error`;
     - new `a native build offers no sign in`.

**Verify:**
- `cd app && flutter analyze` reports nothing new.
- `cd app && flutter test` passes in full. That includes
  `design_rules_test.dart`, `tooltips_test.dart`, `states_test.dart`,
  `overflow_test.dart` and `boundary_test.dart`.
- `grep -rn 'caFile\|login(' app/lib` prints nothing.

## Task 17: The docs follow

**Goal:** `AGENTS.md` and the daemon's README describe hosts over SSH, and
the dev loop says how a systemd user service reaches an SSH agent.

**Files:** `AGENTS.md`, `cmd/webbridge/README.md`, `docs/dev-loop.md`.

**Steps:**
1. In `AGENTS.md`, replace the paragraph that begins "`marshal fleet --server
   NAME` runs that same screen" with one that says:
   - `marshal fleet --host NAME` runs that screen on another machine's daemon,
     reached over SSH (one-daemon D6–D9);
   - the hosts live in `fleet.toml` in the user's config directory, which must
     be private (`0600`, owned by the user);
   - each `[hosts.<name>]` table names an `ssh` destination as `ssh` takes
     it, and may set `marshal` (the remote binary's path) and `notify`, and
     `[local] notify` turns notifications off for the local daemon;
   - `marshal fleet add NAME DEST` and `marshal fleet remove NAME` write the
     file, keeping its comments;
   - a daemon run with `--tunnels` (as `marshal daemon install` writes it)
     runs `marshal daemon tunnels`, which keeps one `ssh -N` per host
     forwarding `<daemon dir>/hosts/<name>.sock` to that host's socket, and
     reports each tunnel in `GET /api/daemon`'s `hosts`;
   - `internal/fleetprofile` owns the file, and `internal/tunnels` the
     tunnels.

   Keep, as its own sentence, "The bridge serves the agent attach on its
   network listener behind the bearer token, over TLS or on a loopback
   address, and refuses it 403 elsewhere." Plan 3 removes it.
2. In `AGENTS.md`'s map:
   - the `internal/fleetprofile/` line becomes "fleet.toml: the hosts the
     daemon reaches over SSH, and `[local]`";
   - add `internal/tunnels/` after it: "`marshal daemon tunnels`: one ssh
     forward per fleet.toml host, reported to the daemon".
3. In `cmd/webbridge/README.md`, after the `--oauth-refresh` paragraph
   (`:139–148`), add one for `--tunnels` (also `WEBBRIDGE_TUNNELS`, off unless
   asked): it runs `<marshal-bin> daemon tunnels --socket <socket>` beside the
   bridge, and `marshal daemon install` always writes it into the unit.
4. In `docs/dev-loop.md`, add a short section, "Reaching another machine",
   which says:
   - `marshal fleet add NAME DEST` on the laptop, then `marshal daemon status`
     shows the tunnel;
   - the remote needs `marshal daemon install` and an sshd that allows
     stream-local forwarding (A1);
   - the daemon's ssh runs with `BatchMode`, so it needs a key it can use
     without a prompt (A2);
   - on Linux, the systemd user manager must see the agent: run
     `systemctl --user import-environment SSH_AUTH_SOCK` once per login, or
     run the agent as a user unit, or set `IdentityAgent` in `~/.ssh/config`;
   - on macOS, launchd gives the daemon the login's agent.

**Verify:**
- `grep -n -- '--server\|token_cmd\|fleet login' AGENTS.md` prints only lines
  plan 3 or 4 owns. Those are the `--join` paragraph and the `marshal daemon
  join-token` mention.
- `grep -n 'internal/tunnels' AGENTS.md` prints the map line.

## Task 18: Full verification

**Steps:**
1. `gofmt -l .` prints nothing, and `go vet ./...` passes.
2. `CGO_ENABLED=1 go build ./cmd/marshal ./cmd/webbridge` succeeds.
3. `scripts/test.sh` (the packages the diff affects) passes. Compare it with
   the baseline at the top of this plan.
4. `go test ./web/bridge -run TestWebIsStdlibOnly -count=1` passes, and so
   does `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent
   -count=1`.
5. `cd app && flutter analyze && flutter test` passes.
6. **By hand on macOS, with Remote Login on.** Use `localhost` as a host that
   is this machine.
   1. `marshal daemon install` with this build, then `marshal fleet add self
      localhost`.
   2. `marshal daemon status` shows `host self (localhost): live` within a few
      seconds, and `<data>/daemon/hosts/self.sock` exists with mode `0600`.
   3. `marshal fleet --host self` opens the fleet. `⏎` on an agent opens its
      session, and the status line names `self`.
   4. In the native Studio, Settings › Hosts lists `self` with `Over SSH to
      localhost` and its tunnel `live`.
   5. Stop sshd (turn Remote Login off). Within 30 s, `self` is `down` with
      ssh's last line, and turning it back on returns it to `live`.
   6. `marshal fleet remove self`, and `self.sock` is gone.
7. **By hand on Linux, against a second machine.** Add it by its
   `~/.ssh/config` alias.
   - With the agent not imported into the user manager, the host is `down`
     with `Permission denied (publickey).`.
   - After `systemctl --user import-environment SSH_AUTH_SOCK` and `marshal
     daemon restart`, it is `live`.
   - A host whose `marshal` is not on the remote's non-interactive `PATH` is
     `down` with the `set marshal =` hint, and setting `marshal` fixes it.
8. **The old file.** A `fleet.toml` that still holds a `[servers.x]` table
   makes `marshal fleet` fail with the retirement message. Plain `marshal`
   prints it once and carries on (`localNotify`).

## Integration notes

- **Upgrading needs a reinstall.** An installed unit from before this plan
  has no `--tunnels`, so `marshal fleet --host` says the daemon reports no
  tunnel. Run `marshal daemon install`, which rewrites the unit, after
  upgrading.
- **Old server profiles stop loading.** A `fleet.toml` with `[servers.*]`
  tables is refused until they are replaced by `[hosts.*]` tables. The
  keychain entries `marshal fleet login` stored are left behind: `marshal
  fleet remove` no longer touches the keychain.
- **The browser is unchanged.** A relayed host still signs in and out in a
  browser until plan 3. A browser sees no SSH host until plan 3's forward.
- **A remote host needs this build.** It needs `marshal daemon status --json`
  at schema 1 and `DaemonInfo.profile` `"local"`, which any daemon from plan 1
  onwards has.
- **A killed helper can leave an ssh behind.** If the helper is killed with
  SIGKILL rather than stopped, its ssh processes are orphaned. The next
  helper's forward replaces their socket file. The orphans keep a dead
  forward open until their connection drops.
