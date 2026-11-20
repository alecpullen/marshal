# One daemon plan 3: the web listener — implementation plan

Date: 2026-11-20. Status: approved 2026-11-20. Amended 2026-11-20: Task 14
(the request guard) is new, Task 16 handles a proxy's redirect, and the tasks
after 13 are renumbered.

Base: `main` at `bbdc4486`, with [plan 1](2026-11-20-od-1-one-daemon-plan.md)
and [plan 2](2026-11-20-od-2-hosts-over-ssh-plan.md) applied first. Every
anchor below was checked at `bbdc4486`, where line numbers are given. Plans 1
and 2 move some of those lines, so a step names the function too, and the
function is what to find. The baseline, with `TMPDIR=/private/tmp/od3`:
- `go test -count=1 ./web/bridge/... ./cmd/webbridge/ ./cmd/marshal/
  ./internal/daemon/ ./internal/app/config/ ./internal/fleetclient/
  ./internal/app/tui/fleet/ ./internal/acpclient/... ./test/daemon/`;
- `cd app && flutter test`: 4,268 passed, 5 skipped.

Approved design: [design.md](../design.md) D2, D3, D10, D11, D13 and D14, and
open question 2. Planning settled these, and design.md records them:
- **The plan is split.** This plan does not include D12 (serving the Studio
  from files). The daemon still embeds the Studio. A new plan 4 does D12, and
  the old plan 4 becomes plan 5.
- **`marshal mcp` is added (open question 2).** A local MCP client runs
  `marshal mcp --credential-file PATH`. That relays its JSON-RPC to `/mcp`
  over the daemon's socket, with the client's own credential.
- **Previews behind a proxy.** `[daemon] preview_listen` fixes the preview
  listener's loopback port, so a proxy can publish it. `[daemon] preview_url`
  is the public origin that preview links name.
- **`web_url` is also the public URL.** The unit writes `[daemon] web_url` as
  `--public-url`, so notification links, approval links and the MCP origin
  check name the proxy's address.
- **`web_group` needs its own path.** It requires `web_listen` to be an
  absolute socket path. The daemon directory is `0700`, so a proxy in the group
  could never reach `web.sock` there.
- **Installed units keep starting.** A unit written before this plan carries
  `--addr` and `--token-file`, and perhaps the four `--join` flags. The new
  `webbridge` accepts those, warns, and ignores them, as plan 1 handled
  `--profile local`. It refuses `--token`, `--tls-cert`, `--tls-key` and
  `--relay-join`, which no installed unit carries.
- **Retired config keys are named, not dropped.** A user config that still
  sets `studio_addr`, `join_url`, `join_name` or `join_ca_file` gets a warning
  from `marshal daemon install`. Config decoding is not strict, so they would
  otherwise vanish without a word.
- **Plan 3 depends on plan 2,** despite design.md's earlier note: the forward
  dials plan 2's tunnel sockets.
- **The web listener guards against other sites (added after approval).** The
  bearer token was the only thing that stopped another site from driving the
  daemon through the user's browser. So before it goes, the web listener
  refuses three things:
  - a `Host` that is not this daemon's (DNS rebinding);
  - a state-changing request a browser makes for another site (CSRF);
  - an `/api/` body that is not JSON.

  `/hooks/`, `/status/` and `/mcp` carry their own credentials and are exempt,
  and the socket is unchanged.
- **A proxy's redirect means signed out.** Most proxies send a fetch to their
  login page with a redirect, not a 401. The browser's transport follows no
  redirect, and reads one as signed out (D13).

Inventory: [inventory.md](../inventory.md) §Plan 3.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **No shared token.** The daemon holds no bearer token: `bearerAuth`,
  `--token`, `--token-file`, `studio.token`, the network agent attach and the
  sign-in handover are gone. MCP credentials, webhook signatures, status links
  and preview tokens stay (D2).
- **Nothing listens on a network interface.** The daemon binds its socket, the
  opt-in web listener, and the preview listener. The last two take a unix
  socket path or a loopback address, and refuse anything else (D3).
- **The web listener sits behind the operator's proxy.** `[daemon]
  web_listen` is `unix`, an absolute socket path, or a loopback address. It
  serves the Studio and the network listener's routes without a bearer check,
  plus `GET /api/hosts` and a forward to each host's tunnel (D10).
- **Previews keep their own listener,** which opens whether or not the web
  listener does (D11).
- **The relay and the join are gone.** The forward to plan 2's tunnels
  replaces them.
- **The browser holds no token.** Its "unauthorized" state now means the
  proxy refused the request (D13).
- **`marshal studio` opens the app.** It opens the native Studio, else the
  proxy's address, else says neither is set up (D14).
- **`marshal mcp`** relays a local MCP client over the socket (open question
  2).

## Non-goals

- Serving the Studio from a directory, and the release, CI, Homebrew, app
  bundle and `dev-studio.sh` changes that go with it: plan 4 (D12).
- Retiring `compose.yaml`, `Dockerfile.bridge` and named-volume state: plan 5.
  After this plan, `compose.yaml` serves no browser: it cannot bind a network
  address.
- Authenticating a browser inside Marshal (design non-goal).
- A preview forward to remote hosts (open question 1). A browser reaches
  previews of this daemon's agents only.
- The docs sweep beyond what this plan changes: plan 5.

## Assumptions

Each was checked at the base unless it says otherwise.

- **The bridge's server** (`web/bridge/http.go`).
  - `NewServer(target any, args ...any)` (`:97`). For a `*Fleet` target,
    `args[0]` is the token and `args[1]` the public URL. For a `*Registry`
    target, `args[0]` is the event log, `args[1]` the token and `args[2]` the
    public URL. A `FactoryAPI` anywhere in `args` is taken as the factory.
  - With a token, `s.http` is `bearerAuth(token, s.mux)` and `networkAttach`
    is set (`:146–152`). Without one, `s.http` is `s.mux`.
  - `ServeHTTP` (`:174`) runs `networkAttach`, the relay join, `studioRoute`,
    then `route(s.http, …)`. `SocketHandler` (`:205`) runs the socket-only
    routes, `studioRoute`, then `route(s.mux, …)`.
  - The socket-only routes are the project attach (`attachPath`,
    `attach.go:22`), the agent attach (`agentAttachID`, `attach.go:79`),
    `pinPath`, `daemonRestartPath`, `marshalEnrollPath`
    (`marshal_enrollment.go:34`) and plan 2's `daemonHostsPath`. None is on
    the mux, so `ServeHTTP` already answers each 404.
  - About 155 call sites build a server with `NewServer`, in `web/bridge`
    tests, `test/daemon` and `cmd/webbridge`. About 30 of them pass a token.
- **The token's other homes.**
  - `auth.go` holds `bearerAuth` and `unauthorized`. `mcp.go:121` also uses
    `unauthorized`.
  - `studio_access.go` holds the code, session and hello routes.
  - `attach.go:426–467` holds `attachNeedsTLS`, `overTLSOrLoopback` and
    `networkAgentAttach`. `hasToken` (`:472`) stays: the socket's attach uses
    it.
- **The relay and the join.**
  - `hostjoin.go` holds the joiner, `SetJoiner` and `joiner`.
  - `hostrelay.go` holds the relay table, the join route and the forward.
    `splitForward` (`:398`), `hopPath` and `hopPathKey` are the parts this plan
    keeps.
  - `contract/relay.go` holds `RelayHosts`, `RelayHost` and `DaemonJoin`.
    `DaemonInfo.Join` is filled at `daemoninfo.go:61–68`.
  - `cmd/webbridge/main.go` wires both: `applyRelay` (`:376`),
    `readSecretFile` (`:432`), `readRelaySecrets` (`:453`), `buildJoiner`
    (`:468`), `SetHostRelay` (`:882`), `SetJoiner` (`:897`), and
    `joiner.Run` (`:1090`).
- **The listeners in `run`** (`cmd/webbridge/main.go`).
  - The token block is `:705–724`.
  - The TCP listener block is `:913–960`. It records `studioAddr` and
    `studioErr` into `DaemonInfo`.
  - The preview block (`:1027–1039`) opens only when the TCP listener did. It
    uses `listenPreview` (`:1170`) and `serveHTTP` (`:1159`), which serves TLS.
- **Previews.** `issuePreview` (`preview.go:214`) answers 501 until
  `SetPreviewPort` is called. `previewOrigin` (`:269`) builds the origin from
  the public URL's host, else `r.Host`, with the preview port appended.
- **MCP.** `/mcp` is on the mux, so it is reachable over the socket too.
  `mcpHandler` (`mcp.go:110`):
  - checks `Origin`, allowing an absent one;
  - authenticates `Authorization: Bearer <client credential>` against the
    fleet's clients;
  - speaks one JSON-RPC message per POST, with notifications answered without
    a body.
- **The unit and the CLI.**
  - `UnitSpec` (`internal/daemon/daemon.go:47`) has `StudioAddr` and `Join`.
    `Args` writes `--addr`, `--token-file` and the four `--join` flags
    (`:75–95`).
  - `Paths.Token` and `Paths.JoinToken` are at `:25`, `:29`.
  - `Client.MintStudioCode` and `StudioCode` are at `client.go:222–272`.
  - `cmd/marshal/daemon.go`: `daemonUnitSpec` (`:257`), `daemonJoin`
    (`:312`), `daemonStudioAddr` (`:336`), `runDaemonJoinToken` (`:510`),
    `runDaemonToken` (`:541`), `readDaemonJoinSecret` (`:573`),
    `daemonWritePrivate` (`:600`), `daemonJoinLine` (`:952`), and the
    dispatch (`:83–110`).
  - `cmd/marshal/studio.go` is the sign-in flow.
  - `fleetStdoutIsTerminal` (`main.go:48–54`) is used only by `daemon token`
    after plan 2.
- **Config.**
  - `[daemon]` holds `studio_addr`, `join_url`, `join_name` and `join_ca_file`:
    `types.go:442–459`, `file_types.go:171–174`, `merge.go:414–428`,
    `save.go:337–340`, and the default at `defaults.go:142`.
  - `toml.Unmarshal` (`load.go:36`) ignores keys it does not know.
- **The fleet client's 401.**
  - `unauthorized` and `reject` are in `internal/fleetclient/client.go`
    (`:468`, `:711`), with call sites at `:297`, `:601`, `:626–650`, `:685`
    and `:755–818`.
  - `acpclient.FleetRejected` is at `fleet.go:19`.
  - The fleet screen handles it at `internal/app/tui/fleet/events.go:63` and
    `view.go:836`.
- **The browser.**
  - `WebTransport` (`app/lib/src/client/transport_web.dart:16`) reads
    `TokenStoreWeb`.
  - `main.dart:98` calls `adoptStudioCodeFromPage` (`studio_code_web.dart`).
  - `host_discovery_stub.dart` reads `relayHosts()`
    (`bridge_client.dart:24`), with a per-host `TokenStoreWeb.forHost` token.
  - `relayed_transport.dart` sends `X-Marshal-Host-Token`.
  - `host_tokens*.dart` give `HostsModel` its `storeToken` and `clearToken`
    (`main.dart:761–762`).
  - The words "refused this page's token" are in
    `ui/kit/state_message.dart:185`, with `signInAgain` (`:106`). They are
    also in `design-system.md:341`, in `connection_check.dart:161` ("token
    refused") and in `HostView.reachWords` (`fleet_store.dart:855`).
  - The gallery draws `StateMessage.unauthorized` (`gallery.dart:205`), so its
    goldens change with the copy.
- **The e2e suite.**
  - `scripts/e2e/e2e_client.py` sends `Authorization: Bearer` on every
    request (`:98–100`).
  - `run_e2e.py` requires `--token` (`:76`).
  - Group H's bearer checks are at `checks_errors.py:41`, `:167` and `:316`.
    Group I's bearer contract is at `checks_spa.py:151–171`.
- **The development box's studio.** It is reached with `tailscale serve
  --https=8443 7700` (`docs/dev-loop.md:190`). `scripts/dev-studio.sh` writes
  the unit with `--addr 127.0.0.1:7700` (`:281`) and a token from its
  environment file (`ensure_token`, `:251`).

## Contracts

```go
// internal/app/config — new DaemonConfig fields (types.go):
// WebListen is the web listener: "unix" (web.sock beside the daemon's
// socket), an absolute socket path, or a loopback host:port. Empty serves
// none (one-daemon D10).
WebListen string `toml:"web_listen"`
// WebGroup shares a web socket with a group, at 0660. It needs WebListen
// to be an absolute socket path.
WebGroup string `toml:"web_group"`
// WebURL is the proxy's public address for the web Studio. marshal studio
// opens it, and the unit passes it as --public-url.
WebURL string `toml:"web_url"`
// PreviewListen is the preview listener's loopback host:port. Empty is a
// free loopback port, and "off" opens none (D11).
PreviewListen string `toml:"preview_listen"`
// PreviewURL is the proxy's public origin for previews, when it publishes
// them on a hostname of their own.
PreviewURL string `toml:"preview_url"`
// Retired lists the retired [daemon] keys a file still sets, sorted:
// studio_addr, join_url, join_name, join_ca_file. Task 7 fills it.
Retired []string `toml:"-"`

// New fileDaemon fields (file_types.go), each a *string with its key's tag:
// WebListen, WebGroup, WebURL, PreviewListen, PreviewURL.
```

```go
// web/bridge/contract/daemon.go

// HostList is GET /api/hosts's answer: the hosts this daemon keeps a tunnel
// to, as its tunnel helper last reported them (one-daemon D10).
type HostList struct {
	Hosts []DaemonHost `json:"hosts"`
}

// New DaemonInfo fields, after StudioError:
// WebListen is the web listener's bound address ("unix:<path>" or
// host:port), and WebError is why it bound nothing (D10).
WebListen string `json:"webListen,omitempty"`
WebError  string `json:"webError,omitempty"`
```

```go
// web/bridge/weblisten.go (new)

// WebListen is a parsed --web-listen.
type WebListen struct {
	Network string // "unix" or "tcp"
	Address string // the socket path, or host:port
	Group   string // unix only: the group the socket is shared with; "" none
}

// String is "unix:<path>" or host:port, as DaemonInfo.WebListen reports it.
func (w WebListen) String() string

// ParseWebListen reads value: "unix" (web.sock in daemonSocket's directory),
// an absolute socket path, or a loopback host:port. group needs an absolute
// path. Anything else is refused, naming --web-listen (D3).
func ParseWebListen(value, group, daemonSocket string) (WebListen, error)

// ListenWeb opens w. A unix socket replaces a stale socket but refuses a live
// one, as ListenLocal does. It is made 0600, or 0660 in Group.
func ListenWeb(w WebListen) (net.Listener, error)

// CheckLoopback refuses an addr whose host is not a loopback address or
// localhost, naming setting.
func CheckLoopback(setting, addr string) error
```

```go
// web/bridge/preview.go

// CheckPreviewURL refuses a base that is not an http or https origin: a host,
// and no path beyond "/", no query and no fragment.
func CheckPreviewURL(base string) error

// SetPreviewURL makes issued preview links name base, after CheckPreviewURL,
// in place of the request's host and the preview port.
func (s *Server) SetPreviewURL(base string) error

// New Server field (http.go):
previewURL string
```

```go
// web/bridge/hostforward.go (hostrelay.go, renamed and cut)

const (
	hostsPath         = "/api/hosts"
	hostForwardPrefix = "/api/hosts/"
)

// listHosts answers GET /api/hosts: the reported hosts as a HostList.
func (s *Server) listHosts(w http.ResponseWriter, r *http.Request)

// hostForward sends /api/hosts/<name>/<rest> to that host's tunnel socket as
// /<rest>, refusing a socket-only route (D10).
func (s *Server) hostForward(w http.ResponseWriter, r *http.Request)

// hostTransport is the HTTP transport that dials socket, built once per path.
func (s *Server) hostTransport(socket string) *http.Transport

// New Server field (http.go):
hostTransports sync.Map // socket path → *http.Transport

// web/bridge/http.go

// socketOnly reports whether path is a route only the socket serves: the
// project attach, the agent attach, pin, restart, the hosts report and
// enrollment.
func socketOnly(path string) bool
```

```go
// web/bridge/webguard.go (Task 14)

// webGuard checks what the web listener is sent on another site's behalf
// (D13). It answers the status and reason of a refusal, or ok.
func (s *Server) webGuard(r *http.Request) (status int, reason string, ok bool)

// allowedHost reports whether host (a Host header, or an Origin's host)
// names this daemon: localhost, 127.0.0.1 or ::1 on any port, or the public
// URL's host name.
func (s *Server) allowedHost(host string) bool
```

```go
// web/bridge/http.go after Task 15

// NewServer builds a server over a *Fleet or a *Registry. For a fleet, the
// first string after it is the public URL. For a registry, the *EventLog
// comes first and the public URL second. A FactoryAPI anywhere in args is
// the factory.
func NewServer(target any, args ...any) *Server
```

```go
// internal/daemon/daemon.go — new UnitSpec fields (Task 8). Each is
// written only when set:
WebListen     string // --web-listen
WebGroup      string // --web-group
PublicURL     string // --public-url
PreviewListen string // --preview-addr
PreviewURL    string // --preview-url

// internal/daemon/client.go (Task 19)

// PostMCP sends one MCP JSON-RPC message to /mcp over the socket with
// credential as its bearer, and answers the status and body.
func (c *Client) PostMCP(ctx context.Context, credential string, body []byte) (status int, answer []byte, err error)
```

```go
// cmd/marshal

// daemonWebListen validates daemon.web_listen and daemon.web_group, and
// answers the --web-listen value.
func daemonWebListen(listen, group string) (string, error)

// daemonLoopbackAddr is daemonStudioAddr renamed: key's value must be a
// loopback host:port.
func daemonLoopbackAddr(key, setting string) (string, error)

// daemonPublicURL validates an http or https origin set under key.
func daemonPublicURL(key, setting string) (string, error)

// daemonWebLine renders the web listener for marshal daemon status.
func daemonWebLine(info daemon.Info) string

// studioOpenApp opens the native Studio: on macOS, `open -b dev.marshal.app`.
// Elsewhere it answers errNoStudioApp. Tests replace it.
var studioOpenApp func(ctx context.Context) error
var errNoStudioApp error
// studioOpenURL opens a URL in the browser; tests replace it.
var studioOpenURL = mcpauth.OpenBrowser

// mcpPoster is the part of daemon.Client that marshal mcp needs.
type mcpPoster interface {
	PostMCP(ctx context.Context, credential string, body []byte) (int, []byte, error)
}
var mcpClientFor = func(socket string) mcpPoster { return daemon.NewClient(socket) }
func runMCP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error
// readCredentialFile reads an MCP credential from a 0600 file.
func readCredentialFile(path string) (string, error)
```

```dart
// app/lib/src/client/bridge_client.dart
Future<HostList> hosts() => _objectOf('/api/hosts', HostList.fromJson);

// app/lib/src/client/relayed_transport.dart after Task 17
/// [relay] with every route under `/api/hosts/<host>`.
Transport relayedTransport(Transport relay, {required String host});

// app/lib/src/client/transport_web.dart after Task 16
WebTransport({this.base});
```

## Task 1: `[daemon]` gains the web and preview settings

**Goal:** the config reads, merges and saves `web_listen`, `web_group`,
`web_url`, `preview_listen` and `preview_url`. The old keys still work until
Task 7.

**Files:** `internal/app/config/types.go`, `file_types.go`, `merge.go`,
`save.go`, `internal/app/config/daemon_test.go`.

**Steps:**
1. Add the five `DaemonConfig` fields and their `fileDaemon` pointers, as
   Contracts declares them (not `Retired`; Task 7 adds it). Each defaults to
   empty.
2. In `merge.go`'s `file.Daemon` block, merge each one as `studio_addr` is
   merged: a set, non-empty value wins.
3. In `save.go`, write each with `strutil.Ptr`, beside `StudioAddr`.

**Tests** (in `daemon_test.go`, as the idle-timeout tests at `:23` and `:78`
are built):
- `TestDaemonWebSettingsAreReadFromTheUserConfig`: all five.
- `TestDaemonWebSettingsRoundTripThroughASave`.
- `TestDaemonWebSettingsDefaultEmpty`.

**Verify:** `go test ./internal/app/config -count=1` passes.

## Task 2: The contract names the web listener and the host list

**Goal:** `contract.HostList` exists, and `DaemonInfo` has `webListen` and
`webError`. The old fields stay until Task 13.

**Files:** `web/bridge/contract/daemon.go`, `app/lib/src/client/contract.g.dart`
(generated).

**Steps:**
1. Add `HostList`, and the two `DaemonInfo` fields after `StudioError`, as
   Contracts declares them.
2. Regenerate: `go test ./web/bridge/contract/dartgen -run
   TestDartContractIsCurrent -update`.

**Verify:** `go test ./web/bridge/contract/... -count=1` passes without
`-update`, and `go build ./...` succeeds.

## Task 3: The bridge can open a web listener

**Goal:** `ParseWebListen`, `ListenWeb` and `CheckLoopback` exist and enforce
D3 and D10.

**Files:** `web/bridge/weblisten.go` (new) and `web/bridge/weblisten_test.go`
(new).

**Steps:**
1. Add `WebListen`, its `String`, `ParseWebListen`, `ListenWeb` and
   `CheckLoopback`, as Contracts declares them.
2. `ParseWebListen`:
   - `unix` is `filepath.Join(filepath.Dir(daemonSocket), "web.sock")`. With a
     group it is refused: `--web-group needs --web-listen to be an absolute
     socket path: the daemon directory is private, so the group could not
     reach web.sock there`.
   - A value starting with `/` is that path.
   - Any other value must split as host:port, and passes `CheckLoopback`. A
     group is refused: `--web-group is for a unix --web-listen`.
   - An empty value is refused: callers pass only a set value.
3. `CheckLoopback(setting, addr)`:
   - The host must be `localhost`, or an IP that `IsLoopback`. A name is not
     resolved.
   - An empty host (`:7700`) is refused, because it binds every interface.
   - The refusal reads: `<setting> <addr>: the daemon listens on a unix socket
     or a loopback address only; put a proxy in front of it
     (docs/one-daemon/design.md D3)`.
4. `ListenWeb` for unix:
   - The socket's directory must exist and belong to the user, and no group
     or other may write it. `ListenWeb` creates no directory: the daemon's
     own socket has made the daemon directory by then (Task 6 opens the web
     listener after it).
   - A stale socket is removed and a live one refused, as `ListenLocal`
     (`:48`, its dial at `:61` and removal at `:76`) does. Move that step
     into a helper both functions call.
   - The socket is `0600`. With a group, it is chowned to the group's gid
     (`os/user.LookupGroup`, `os.Lchown` with uid -1) and made `0660`. An
     unknown group is refused, naming it.
   - Closing the listener removes the socket.
5. `ListenWeb` for tcp is `net.Listen("tcp", address)`.

**Tests** (new):
- `TestParseWebListenReadsUnix`.
- `TestParseWebListenReadsAPath`.
- `TestParseWebListenReadsALoopbackAddress`: `127.0.0.1:7700`, `[::1]:7700`
  and `localhost:7700`.
- `TestParseWebListenRefusesANetworkAddress`: `0.0.0.0:7700`, `:7700`,
  `192.168.1.2:7700` and `box.local:7700`. Each error names `--web-listen`
  and D3.
- `TestAWebGroupNeedsAnAbsolutePath`: with `unix`, and with a loopback
  address.
- `TestListenWebMakesAPrivateSocket`: mode `0600`, and the socket is removed
  on close.
- `TestListenWebSharesASocketWithAGroup`: the user's own primary group (from
  `os/user.Current`) gives mode `0660` and that gid.
- `TestListenWebReplacesAStaleSocket` and `TestListenWebRefusesALiveSocket`.
- `TestListenWebRefusesADirectoryOthersCanWrite`.

**Verify:** `go test ./web/bridge -run 'WebListen|ListenWeb|Loopback' -count=1`
passes.

## Task 4: Preview links can name a public origin

**Goal:** after `SetPreviewURL`, an issued preview link starts with that
origin. Without it, nothing changes.

**Files:** `web/bridge/preview.go`, `web/bridge/http.go` (one field),
`web/bridge/preview_test.go`.

**Steps:**
1. Add `previewURL` to `Server` beside `previewPort` (`http.go:51`), and
   `CheckPreviewURL` and `SetPreviewURL` to `preview.go` beside
   `SetPreviewPort` (`:240`), as Contracts declares them.
   `SetPreviewURL` stores the origin without a trailing slash.
2. In `previewOrigin` (`:269`), when `s.previewURL` is set, answer it, with
   `tokenInHost` false, before any other rule. The issued URL then keeps its
   `?t=` token, as a non-localhost host does today.

**Tests** (in `preview_test.go`, beside the `previewOrigin` tests):
- `TestAPreviewURLNamesTheOrigin`.
- `TestAPreviewURLKeepsTheToken`.
- `TestCheckPreviewURLRefusesAPath`, and a query, and an `ftp` scheme.

**Verify:** `go test ./web/bridge -run Preview -count=1` passes.

## Task 5: `webbridge` stops relaying and joining

**Goal:** `cmd/webbridge` builds no relay and no joiner. `--relay-join` is
refused, and the four `--join` flags are accepted, warned about and ignored.

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/relay_test.go` (deleted),
`cmd/webbridge/local_test.go`.

**Steps:**
1. Delete the `relayJoins`, `join`, `joinName`, `joinTokenFile` and
   `joinCAFile` fields from `config`, and the `relayJoin` type.
2. `--relay-join` (and `WEBBRIDGE_RELAY_JOIN`) is refused:
   `--relay-join is retired: a remote daemon is a host reached over SSH
   (marshal fleet add; docs/one-daemon/design.md D6)`.
3. `--join`, `--join-name`, `--join-token-file` and `--join-ca-file` stay
   declared, with the help text "retired; ignored". When any is given, by flag
   or by its variable, `parseConfig` logs one warning that names them:
   `webbridge: <flags> are retired and ignored; run marshal daemon install to
   rewrite the unit`.
4. Delete `applyRelay`, `readSecretFile`, `readRelaySecrets` and
   `buildJoiner`. In `run`, delete the relay block (`:870–883`), the joiner
   block (`:885–898`) and the joiner's run (`:1085–1100`).
5. Every token rule that mentioned a join drops that clause. The `--token`
   rules for `--addr off` (in `applyDaemonDefaults` after plan 1) read: a
   token is for the TCP listener. Task 6 deletes them.
6. Delete `relay_test.go`.
7. In `local_test.go`, add `TestTheJoinFlagsAreIgnored` (a unit's four join
   flags start the daemon, and stderr holds the warning once) and
   `TestRelayJoinIsRefused`.

**Verify:**
- `go test ./cmd/webbridge -count=1` passes, and `go build ./cmd/webbridge`
  succeeds.
- `grep -n 'NewJoiner\|SetHostRelay\|SetJoiner\|NewHostRelay' cmd/webbridge/*.go`
  prints nothing.

## Task 6: `webbridge` serves the web listener, and no token

**Goal:** `--web-listen` opens the web listener, serving the API with no bearer
check. The TCP listener, the token and TLS are gone. Previews open on a
loopback listener of their own.

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/main_test.go`,
`cmd/webbridge/local_test.go`, `cmd/webbridge/restart_test.go`,
`cmd/webbridge/tokenfile_test.go` (deleted).

**Steps:**
1. **Config.** Delete the `addr`, `token`, `tokenFile`, `tlsCert` and
   `tlsKey` fields. Add `webListen`, `webGroup` and `previewURL`.
2. **Flags.**
   - `--web-listen` (`WEBBRIDGE_WEB_LISTEN`): "the web listener: unix, an
     absolute socket path, or a loopback host:port (default: none)".
   - `--web-group` (`WEBBRIDGE_WEB_GROUP`): "share a unix --web-listen with
     this group (0660)".
   - `--preview-url` (`WEBBRIDGE_PREVIEW_URL`): "the public origin preview
     links name, for a proxy that publishes previews".
   - `--preview-addr`'s help becomes "loopback address for previews (default:
     a free port on 127.0.0.1; \"off\" disables)".
3. **Retired flags.**
   - `--addr` and `--token-file` (and `WEBBRIDGE_ADDR`, `WEBBRIDGE_TOKEN_FILE`)
     stay declared as "retired; ignored". They join Task 5's warning, because
     installed units carry them.
   - `--token`, `--tls-cert` and `--tls-key`, and their variables, are
     refused: `<flag> is retired: the daemon holds no token and serves no TLS;
     put a proxy in front of --web-listen (docs/one-daemon/design.md D2, D3)`.
4. **Parse checks.**
   - Delete the TLS pair check and the token rules in `applyDaemonDefaults`.
   - When `--web-listen` is set, `parseConfig` calls `bridge.ParseWebListen`
     with the socket once it is resolved, and returns its error.
   - `--web-group` without `--web-listen` is refused.
   - `--preview-addr` other than `""` or `off` must pass
     `bridge.CheckLoopback("--preview-addr", …)`.
   - `--preview-url` must pass `bridge.CheckPreviewURL`.
5. **`run`.**
   - Delete the token block (`:705–724`). Build the server with
     `bridge.NewServer(fleet, "", cfg.publicURL, factory)`: no token, so
     `ServeHTTP` has no bearer check. Task 15 removes the argument.
   - Delete the TCP listener block (`:913–960`). Open the web listener after
     `ListenLocal` returns and before the socket's `Serve` goroutine starts:
     `unix` puts `web.sock` in the socket's directory, and `SetDaemonInfo`,
     which must be called before anything serves, reports the outcome. Move
     `SetDaemonInfo` to just after the web listener opens.
   - Parse with `ParseWebListen` and open with `ListenWeb`. A failure is
     logged at error level and recorded as `webErr`, never fatal, as plan 1
     made the Studio port.
   - Serve the server's `ServeHTTP` with `ReadHeaderTimeout: 10 * time.Second`
     on its own goroutine into `serveErr`. Print `webbridge: web listener on
     <String()>`, using the bound address for tcp.
   - `DaemonInfo` gets `WebListen` (that string) and `WebError`. It sets no
     `StudioAddr` or `StudioError`.
   - The preview block opens whether or not the web listener did. Its address
     is `cfg.previewAddr`, else `127.0.0.1:0`. It is served with `Serve`, and
     its log line says `http://`. After the bind it calls `SetPreviewPort`,
     and `SetPreviewURL` when `--preview-url` is set.
   - Delete `serveHTTP`, `listenPreview`'s dependence on the API address
     (its parameter goes), `genToken` and `readOrCreateToken`. Rename `srv` to
     `webSrv` in the shutdown and the serve-error cleanup.
6. **Tests.**
   - Delete `tokenfile_test.go`.
   - Drop `--addr` and `--token` from every test's arguments in
     `restart_test.go`, `local_test.go` and `main_test.go`.
   - Delete the refusal rows that only checked a token rule.
   - New `TestParseConfigReadsTheWebListener` (`main_test.go`): the flags and
     their variables.
   - New `TestTheRetiredListenerFlagsAreIgnored`: `--addr off --token-file X`
     starts the daemon with one warning.
   - New `TestTheTokenAndTLSFlagsAreRefused`: a table of the three flags and
     their variables.
   - New `TestAPreviewAddrMustBeLoopback`.
   - New `TestWebGroupNeedsAWebListener`.
   - New `TestTheDaemonServesTheWebListener` (`local_test.go`): with
     `--web-listen unix`, `GET /api/agents` over `web.sock` answers 200 with
     no `Authorization`. `GET /api/daemon` over the socket reports
     `webListen` `unix:<path>`, and `POST /api/projects/pin` over `web.sock`
     answers 404.
   - New `TestAWebListenerThatCannotBindIsReported`: a live process already
     on the path, so the daemon still serves its socket, and `webError` is
     set.
   - New `TestPreviewsOpenWithoutAWebListener`: with no `--web-listen`,
     `POST /api/agents/x/preview/3000` over the socket answers anything but
     501.

**Verify:**
- `go test ./cmd/webbridge -count=1` passes.
- `grep -n 'tlsCert\|readOrCreateToken\|genToken\|cfg.token' cmd/webbridge/main.go`
  prints nothing.

## Task 7: The config retires `studio_addr` and the join keys

**Goal:** `DaemonConfig` loses `StudioAddr`, `JoinURL`, `JoinName` and
`JoinCAFile`, and lists any the user config still sets in `Retired`.
`cmd/marshal` stops reading them.

**Files:** `internal/app/config/types.go`, `file_types.go`, `merge.go`,
`save.go`, `defaults.go`, `internal/app/config/daemon_test.go`, and the
readers in `cmd/marshal/daemon.go`.

**Steps:**
1. Delete the four `DaemonConfig` fields and their comments, and the
   `StudioAddr` default (`defaults.go:142`). Add `Retired`, as Contracts
   declares it.
2. Keep the four `fileDaemon` pointers, with a comment: "retired
   (one-daemon D10, D6): read only to be named in DaemonConfig.Retired".
3. In `merge.go`, replace their merging with: each that is set appends its key
   to `cfg.Daemon.Retired`. Sort it, and add no key twice when two layers set
   it.
4. In `save.go`, delete the four lines. A save never writes a retired key.
5. In `cmd/marshal/daemon.go`'s `daemonUnitSpec`, stop calling
   `daemonStudioAddr` and `daemonJoin`. The spec's `StudioAddr` and `Join`
   are left zero. Task 9 deletes those functions and fills the new fields.

**Tests:**
- `TestARetiredDaemonKeyIsNamed`: `studio_addr` and `join_url` in a user file
  give `Retired` `["join_url", "studio_addr"]`.
- `TestASaveDropsTheRetiredDaemonKeys`.
- Delete the tests that read `studio_addr` or the join keys into the old
  fields, here and in `cmd/marshal/daemon_test.go`.

**Verify:** `go test ./internal/app/config ./cmd/marshal -count=1` passes.

## Task 8: The unit writes the web listener

**Goal:** `UnitSpec` has Contracts' five fields, and `Args` writes them. The
old `StudioAddr` and `Join` paths still compile, and nothing sets them.

**Files:** `internal/daemon/daemon.go`, `internal/daemon/studio_test.go`,
`internal/daemon/units_test.go`.

**Steps:**
1. Add the five fields after `StudioAddr`, as Contracts declares them.
2. In `Args`, after `--idle-session-timeout` and `--egress`, append each that
   is set, in Contracts' order:
   - `--web-listen <v>`;
   - `--web-group <v>`;
   - `--public-url <v>`;
   - `--preview-addr <v>`;
   - `--preview-url <v>`.

   The `--addr` and `--token-file` lines stay until Task 10.
3. Tests:
   - `TestArgsWriteTheWebListener` (`studio_test.go`): all five, in order.
   - `TestArgsWriteNoWebFlagsByDefault`.
   - `TestThePlistNamesEverything` and `TestTheSystemdUnitNamesEverything`
     keep passing unchanged, since nothing is set there.

**Verify:** `go test ./internal/daemon -count=1` passes.

## Task 9: `marshal daemon` installs, reports and forgets the token

**Goal:**
- `marshal daemon install` turns the five settings into the unit, refuses an
  address D3 forbids, warns about retired keys, and removes the stale token
  files.
- `marshal daemon status` reports the web listener.
- `token` and `join-token` are gone.

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/main.go`,
`cmd/marshal/daemon_test.go`.

**Steps:**
1. Delete `daemonJoin`. Rename `daemonStudioAddr` to `daemonLoopbackAddr(key,
   setting)`, whose messages name `key` and say "the daemon listens on a unix
   socket or a loopback address only". Unlike the old function, it does not
   resolve a host name: only `localhost` and loopback IPs pass, as
   `CheckLoopback` rules.
2. Add `daemonWebListen`, as Contracts declares it:
   - `""` answers `""`;
   - `unix` answers `unix`, and a group with it is refused, as Task 3 words
     it, naming `daemon.web_group` and `daemon.web_listen`;
   - a value starting with `/` answers itself;
   - anything else goes through `daemonLoopbackAddr("daemon.web_listen", …)`,
     and a group with it is refused.
3. Add `daemonPublicURL(key, setting)`: `""` answers `""`, and anything else
   must be an `http` or `https` URL with a host.
4. In `daemonUnitSpec`, fill the spec:
   - `WebListen` and `WebGroup` from `daemonWebListen`;
   - `PublicURL` from `daemonPublicURL("daemon.web_url", …)`;
   - `PreviewListen`: `off` as it is, else
     `daemonLoopbackAddr("daemon.preview_listen", …)` when set;
   - `PreviewURL` from `daemonPublicURL("daemon.preview_url", …)`.
5. In `runDaemonInstall`, before writing the unit:
   - For each key in `cfg.Daemon.Retired`, print `marshal daemon install:
     daemon.<key> is retired and ignored; <hint>`. The hint for `studio_addr`
     is "set daemon.web_listen (docs/one-daemon/design.md D10)". The hint for
     the join keys is "a remote daemon is a host: marshal fleet add".
   - After the unit is written, remove `studio.token` and `join.token` from
     the daemon directory, ignoring a missing file. Build the paths from
     `daemon.PathsFor(home).Dir`, not from the `Paths` fields Task 10
     deletes.
6. Delete `runDaemonJoinToken`, `runDaemonToken`, `readDaemonJoinSecret`,
   `daemonWritePrivate` (if nothing else calls it) and `daemonJoinLine`, and
   their section header. Remove `join-token` and `token` from both usage
   strings and from the dispatch, where they fall to the unknown-subcommand
   error. Delete `fleetStdoutIsTerminal` from `main.go` (`:48–54`) and any
   import it leaves unused.
7. Add `daemonWebLine`, as Contracts declares it. In text mode, `status`
   prints it after the refresh line:
   - `web: off` when the listener and its error are both empty;
   - `web: not listening (<webError>)` when there is an error;
   - `web: <webListen>` for a unix socket;
   - `web: <webListen> (loopback: every account on this machine can reach
     it)` for an address (D13).
8. Tests:
   - delete every test of `token`, `join-token` and the join;
   - new `TestDaemonInstallWritesTheWebListener`: all five settings reach the
     spec;
   - new `TestDaemonInstallRefusesANetworkWebListener`;
   - new `TestDaemonInstallRefusesAWebGroupWithUnix`;
   - new `TestDaemonInstallNamesTheRetiredKeys`;
   - new `TestDaemonInstallRemovesTheOldTokenFiles`;
   - new `TestDaemonStatusReportsTheWebListener`: the four forms;
   - new `TestDaemonTokenAndJoinTokenAreGone`.

**Verify:** `go test ./cmd/marshal -count=1` passes.

## Task 10: `marshal studio` opens the app (D14)

**Goal:** `marshal studio` opens the native Studio, else `[daemon] web_url`,
else says neither is set up. It mints no code and probes no port.

**Files:** `cmd/marshal/studio.go` and `cmd/marshal/studio_test.go`.

Runs after Task 9.

**Steps:**
1. Rewrite `runStudio`. Its flags are `--print` only: "print the web
   Studio's address instead of opening it". `--no-open` falls to the flag
   set's unknown-flag error.
2. The order:
   1. Unless `--print` was given, call `studioOpenApp(ctx)`. On success,
      write `studio: opened the Marshal app` and return.
      `errNoStudioApp` goes on to step 2, and any other error is written as
      a warning before going on.
   2. Load the user config (`config.Load` with `SkipProjectConfig`). With
      `daemon.web_url` set:
      - `--print` writes `studio: <url>`;
      - otherwise `studioOpenURL(url)` runs, then `studio: <url>` is written.
        A failure to open is a warning that says to run `marshal studio
        --print`, as today.
   3. Otherwise return: `marshal studio: neither the Marshal app nor a web
      Studio is set up: install the app, or set daemon.web_listen and
      daemon.web_url and run marshal daemon install
      (docs/one-daemon/design.md D10, D14)`.
3. When step 2 runs, also read the daemon's `Info` over the socket with
   `studioProbeTimeout`. A `WebError`, or an empty `WebListen`, adds one
   warning line: `marshal studio: the daemon's web listener is not listening
   (…)`. A daemon that does not answer adds no line.
4. `studioOpenApp`: on `runtime.GOOS == "darwin"`, run `open -b
   dev.marshal.app` and answer its error. If `open` fails because no app has
   that identifier, answer `errNoStudioApp`, which is exit status 1 with
   stderr naming the identifier. Everywhere else, answer `errNoStudioApp`.
5. Delete `studioSignInURL`, `studioHello` and `probeStudio`, and the update
   to the comment above them.
6. Rewrite `studio_test.go` with these tests:
   - `TestStudioOpensTheApp`;
   - `TestStudioOpensTheWebURLWithoutAnApp`;
   - `TestStudioPrintsTheWebURL`: the app is not tried;
   - `TestStudioWithNeitherSaysSo`;
   - `TestStudioWarnsWhenTheWebListenerIsDown`;
   - `TestStudioRefusesNoOpen`.

**Verify:** `go test ./cmd/marshal -run Studio -count=1` passes.

## Task 11: The daemon package forgets the token and the join

**Goal:** `UnitSpec.StudioAddr`, `UnitSpec.Join`, `Join`, `Paths.Token`,
`Paths.JoinToken`, `MintStudioCode` and `StudioCode` are gone, and `Args`
writes no `--addr` and no `--token-file`.

**Files:** `internal/daemon/daemon.go`, `internal/daemon/client.go`,
`internal/daemon/studio_test.go`, `internal/daemon/client_test.go`,
`internal/daemon/paths_test.go`, and `cmd/marshal/studio.go`.

It runs after Task 10, which stops `cmd/marshal/studio.go` calling
`MintStudioCode`.

**Steps:**
1. Delete `Paths.Token` and `Paths.JoinToken` with their comments, and their
   two lines in `PathsFor`.
2. Delete `UnitSpec.StudioAddr`, `UnitSpec.Join` and the `Join` type. In
   `Args`, delete the join block, the `--addr` lines and the `--token-file`
   block (`:75–95`).
3. Delete `StudioCode` and `MintStudioCode` (`client.go:222–272`), and the
   studio code path constant if one is left.
4. Tests:
   - in `studio_test.go`, delete every test of `--addr`, `--token-file` or a
     join (`TestArgsWriteTheTokenFileForAJoinWithoutAStudio`, `:106`,
     included), and keep Task 8's two;
   - rename the file to `webargs_test.go`;
   - delete the `MintStudioCode` tests in `client_test.go`;
   - delete the `Token` and `JoinToken` assertions in `paths_test.go`;
   - the plist and systemd tests expect no `--addr`.

**Verify:**
- `go test ./internal/daemon ./cmd/marshal -count=1` passes.
- `grep -rn 'StudioAddr\|JoinToken\|MintStudioCode' internal/daemon cmd/marshal`
  prints nothing.

## Task 12: The browser lists hosts from `GET /api/hosts`

**Goal:** the browser's discovery reads `GET /api/hosts` with
`BridgeClient.hosts()`. `relayHosts()` goes.

**Files:** `app/lib/src/client/bridge_client.dart`,
`app/lib/src/client/host_discovery_stub.dart`,
`app/test/client/bridge_client_test.dart`, and a new
`app/test/client/host_discovery_stub_test.dart` if none covers discovery.

**Steps:**
1. In `bridge_client.dart`, replace `relayHosts()` (`:24`) with `hosts()`, as
   Contracts declares it.
2. In `host_discovery_stub.dart`, `_relayedHosts` becomes `_daemonHosts`. It
   reads `client.hosts()`.
   - For each host whose name is not the page's own, it answers a `HostSpec`
     with:
     - kind `HostKind.server`;
     - `via` the page's name;
     - `destination` from the host's `ssh`;
     - the client it builds today.
   - A failure still answers no hosts.
   - Rewrite the comments: the hosts are the daemon's tunnels (one-daemon
     D10), reached through it.
3. `bridge_client_test.dart`: `hosts reads /api/hosts` replaces the
   `relayHosts` test.

**Verify:**
- `cd app && flutter analyze` reports nothing new.
- `cd app && flutter test test/client/` passes.

## Task 13: The forward to the tunnels replaces the relay

**Goal:**
- `GET /api/hosts` lists the reported hosts.
- `/api/hosts/<name>/<rest>` forwards to that host's tunnel socket.
- The relay, the join and their contract types are deleted, and so are
  `DaemonInfo`'s `studioAddr`, `studioError` and `join`.

**Files:** `web/bridge/hostrelay.go` (renamed `hostforward.go`),
`web/bridge/hostrelay_test.go` (renamed `hostforward_test.go`),
`web/bridge/hostjoin.go` and `hostjoin_test.go` (deleted),
`web/bridge/http.go`, `web/bridge/daemoninfo.go`, `web/bridge/contract/relay.go`
(deleted), `web/bridge/contract/daemon.go`,
`app/lib/src/client/contract.g.dart` (generated).

**Steps:**
1. Delete `hostjoin.go` and `hostjoin_test.go`.
2. Rename `hostrelay.go` to `hostforward.go`, then cut it to what Contracts
   declares:
   - Keep `splitForward`, `hopPath` and `hopPathKey`.
   - Delete everything else: `HostRelay`, `relayHost`, `NewHostRelay`,
     `SetHostRelay`, `relayJoin`, the join's helpers, `relayConn`,
     `relayConnOpen`, `relayWait`, `relayCap`, `relayNameRe`, and the five
     header constants. `relayInvalidHost` goes if nothing is left using it.
   - `splitForward` keeps its rules, refusing the `api/relay/` prefix as well
     as `api/hosts/`.
3. Add `socketOnly` to `http.go`, as Contracts declares it. It is true for:
   - `attachPath`, `pinPath`, `daemonRestartPath`, `daemonHostsPath` and
     `marshalEnrollPath`;
   - any path `agentAttachID` accepts.
4. In `routes()` (`http.go:243`), register `GET /api/hosts` to `listHosts` and
   the `/api/hosts/` prefix to `hostForward`. The routes are then on both
   listeners.
5. `listHosts` answers `contract.HostList{Hosts: s.reportedHosts()}`.
6. `hostForward`:
   1. `splitForward`. A failure answers 404.
   2. Find the name in `reportedHosts()`. A missing name answers 404 with
      `{"error": "no host <name>"}`.
   3. A state other than live answers 503 with `{"error": "<name> is
      <state>"}`, plus `: <lastError>` when there is one.
   4. `socketOnly("/" + rest.path)` answers 404.
   5. Otherwise, serve through an `httputil.ReverseProxy` built per request:
      - its `Transport` is `s.hostTransport(host.Socket)`;
      - its `Rewrite` sets the URL to `http://daemon/<rest>`, keeping the raw
        path and the query, and deletes `Authorization` and `Cookie`;
      - `FlushInterval` is -1;
      - its `ErrorHandler` answers 502 with the error.
7. `hostTransport` keeps one `*http.Transport` per socket path in
   `s.hostTransports`. Its `DialContext` dials that unix path, whatever the
   address.
8. In `http.go`, delete the `relay` and `join` fields and `ServeHTTP`'s
   relay-join branch (`:181–187`).
9. In `daemoninfo.go`, delete the join block (`:61–68`).
10. Delete `contract/relay.go`. In `contract/daemon.go`, delete `DaemonInfo`'s
    `StudioAddr`, `StudioError` and `Join`. Regenerate `contract.g.dart`.
11. `hostforward_test.go` replaces the relay tests with these. A "fake tunnel"
    is an `http.Server` on a unix socket in a short temporary directory,
    recording what it is sent, and a hosts report naming it live.
    - `TestTheHostsRouteListsTheReport`;
    - `TestTheHostsRouteIsEmptyWithoutAReport`: `{"hosts":[]}`;
    - `TestAForwardReachesTheHostsSocket`: `GET
      /api/hosts/box/api/agents?x=1` reaches the fake as `/api/agents?x=1`,
      and the fake's answer comes back unchanged;
    - `TestAForwardToAnUnknownHostIs404`;
    - `TestAForwardToAHostThatIsDownIs503`: the body names the state and the
      error;
    - `TestAForwardRefusesTheSocketOnlyRoutes`: each of the six answers 404,
      and the fake sees nothing;
    - `TestAForwardDoesNotChain`: `/api/hosts/box/api/hosts/x/api/agents`
      answers 404;
    - `TestAForwardDropsTheCallersCredentials`: the fake sees no
      `Authorization` and no `Cookie`;
    - `TestAForwardStreams`: the fake writes two SSE events with a pause, and
      the first arrives before the second is written.

**Verify:**
- `go test ./web/bridge/... -count=1` passes.
- `cd app && flutter analyze` reports nothing new.
- `go build ./...` succeeds.
- `grep -rn 'HostRelay\|RelayHost\|Joiner\|DaemonJoin\|StudioAddr' web/bridge/*.go web/bridge/contract/*.go`
  prints nothing. (`relay.go`, the viewer attach's frame relay, stays:
  inventory, "Stays as it is".)

## Task 14: The web listener refuses requests made on another site's behalf

**Goal:** before the token goes, `ServeHTTP` refuses three requests:
- one whose `Host` is not this daemon's;
- a state-changing request a browser makes for another site;
- an `/api/` body that is not JSON.

`SocketHandler` is unchanged.

**Files:** `web/bridge/webguard.go` (new), `web/bridge/webguard_test.go` (new),
`web/bridge/http.go`.

**Steps:**
1. Add `allowedHost`, as Contracts declares it.
   - It drops a port, lower-cases the name, and trims a trailing dot.
   - It is true for `localhost`, `127.0.0.1` and `::1`, and for the public
     URL's host name (`s.publicURLBase`, which the unit fills from
     `web_url`).
   - No name is resolved.
2. Add `webGuard`, as Contracts declares it. It checks, in this order:
   1. A path under `/hooks/` or `/status/`, or exactly `/mcp`, is exempt and
      answers ok. Each carries its own credential, and `/mcp` checks its own
      `Origin` (`mcp.go:197`).
   2. `allowedHost(r.Host)` false answers 403: `this daemon does not answer to
      host <host>; set [daemon] web_url to the address the proxy publishes
      (docs/one-daemon/design.md D13)`.
   3. GET, HEAD and OPTIONS answer ok.
   4. A `Sec-Fetch-Site` header other than `same-origin` or `none` answers
      403: `cross-site request refused`.
   5. With no `Sec-Fetch-Site` but an `Origin` header, the origin must parse,
      and its host must pass `allowedHost` and equal `r.Host`'s host name or
      the public URL's. `null` fails. A failure answers 403 with the same
      reason.
   6. A path under `/api/` with a body (`ContentLength` > 0, or chunked)
      whose media type is not `application/json` answers 415: `the API takes
      application/json`. Parameters such as `charset` are allowed.
   7. Otherwise, ok.

   A request with neither `Sec-Fetch-Site` nor `Origin` (curl, the e2e suite,
   a CLI) passes steps 4 and 5. It is not a browser acting for another site.
3. At the start of `ServeHTTP`, run `webGuard`, and on a refusal write
   `{"error": reason}` with its status. `SocketHandler` does not call it: its
   callers are peer-checked as the owner.
4. Tests (new). The server is built with a public URL of
   `https://studio.example.com`, and the token argument stays `""` until
   Task 15.
   - `TestTheWebListenerRefusesAnotherHost`: `Host: evil.example:7700`
     answers 403, and the reason names `web_url`.
   - `TestTheWebListenerAnswersLoopbackHosts`: `localhost:7700`,
     `127.0.0.1:7700` and `[::1]:7700`.
   - `TestTheWebListenerAnswersThePublicHost`: `studio.example.com`, with
     and without a port.
   - `TestACrossSitePostIsRefused`: `Sec-Fetch-Site: cross-site` and
     `same-site` each answer 403 on `POST /api/agents`.
   - `TestASameOriginPostPasses`: `Sec-Fetch-Site: same-origin` reaches the
     route.
   - `TestAnOriginFromAnotherSiteIsRefused`: no `Sec-Fetch-Site`, and
     `Origin: https://evil.example` or `null`.
   - `TestARequestWithNeitherHeaderPasses`.
   - `TestAnAPIBodyMustBeJSON`: `text/plain` answers 415, and
     `application/json; charset=utf-8` passes.
   - `TestHooksStatusAndMCPAreExempt`: a cross-site POST to each passes the
     guard. Its own handler then decides.
   - `TestTheSocketHasNoGuard`: `SocketHandler` with `Host: evil.example`
     still answers.

**Verify:** `go test ./web/bridge -run 'WebListener|CrossSite|SameOrigin|Origin|NeitherHeader|APIBody|Exempt|SocketHasNoGuard' -count=1`
passes.

## Task 15: The bridge holds no token

**Goal:**
- `bearerAuth`, the sign-in handover and the network agent attach are deleted.
- `NewServer` takes no token.
- `ServeHTTP` is the web listener's route set.

**Files:** `web/bridge/auth.go` and `auth_test.go` (deleted),
`web/bridge/studio_access.go` and `studio_access_test.go` (deleted),
`web/bridge/attach.go`, `web/bridge/http.go`, `web/bridge/mcp.go`, every
`web/bridge` test that builds a server, `test/daemon` (each `NewServer`
call), and the wiring line in `cmd/webbridge/main.go`.

**Steps:**
1. Move `unauthorized` from `auth.go` into `mcp.go`, unchanged, then delete
   `auth.go` and `auth_test.go`.
2. Delete `studio_access.go` and `studio_access_test.go`, and the `token` and
   `codes` fields of `Server` with `newStudioCodes`.
3. In `attach.go`, delete `attachNeedsTLS`, `overTLSOrLoopback` and
   `networkAgentAttach` (`:426–467`). Keep `hasToken`.
4. In `http.go`:
   - Delete the `networkAttach` and `http` fields.
   - Rewrite `NewServer`'s argument rule, as Contracts declares it.
   - `ServeHTTP` is `route(s.mux, w, r)`, with a comment: it is the web
     listener's handler. It has no bearer check, and the socket-only routes
     are absent because they are not on the mux (D10, D13).
   - `SocketHandler` loses its `studioRoute` call.
   - `route` keeps its parameter.
   - Rewrite the `Server` and `ServeHTTP` comments, which describe the bearer
     check.
5. Every caller:
   - `NewServer(f, token)` becomes `NewServer(f)`;
   - `NewServer(f, token, url, …)` and `NewServer(f, "", url, …)` become
     `NewServer(f, url, …)`;
   - `NewServer(r, l, token)` becomes `NewServer(r, l)`.

   Find them with `grep -rn '[^.]NewServer(' web cmd test | grep -v httptest`.
   In `cmd/webbridge/main.go` the call becomes `bridge.NewServer(fleet,
   cfg.publicURL, factory)`.
6. Then run `go test ./web/bridge/...`.
   - Delete a failing test when all it asserts is a 401 for a missing or
     wrong bearer token, or the sign-in exchange.
   - Any other failure is a mistake in this task, and is fixed in the code,
     not in the test.

   Tests that only set an `Authorization` header keep it or drop it: the
   header is now ignored.

**Verify:**
- `go test ./web/bridge/... ./cmd/webbridge ./test/daemon -count=1` passes.
- `grep -rn 'bearerAuth\|studioRoute\|networkAgentAttach\|s.token' web/bridge/*.go`
  prints nothing.

## Task 16: The browser sends no token

**Goal:** `WebTransport` sends no `Authorization`, and the page's token store
and sign-in code are deleted.

**Files:** `app/lib/src/client/transport_web.dart`,
`app/lib/src/client/token_store_web.dart` (deleted),
`app/lib/src/client/studio_code_web.dart` and `studio_code_stub.dart`
(deleted), `app/lib/src/client/host_discovery_stub.dart`, `app/lib/main.dart`,
`app/test/client/transport_web_test.dart`.

**Steps:**
1. In `transport_web.dart`, delete the `tokens` field and parameter, the
   import, and the header block in `_fetch` (`:77–80`). Rewrite the class
   comment: same-origin requests to the daemon that served the page, through
   the operator's proxy. The page holds no credential (D13).
2. Delete `token_store_web.dart`, `studio_code_web.dart` and
   `studio_code_stub.dart`.
3. In `main.dart`, delete the studio code import (`:13–14`) and the call
   (`:98`).
4. In `host_discovery_stub.dart`, drop the `TokenStoreWeb` import and use.
   The relayed transport's token argument is Task 17's, so pass `token: () async
   => ''` here until then. An empty token passes its null check, and the
   forward ignores the header.
5. A proxy's redirect is a sign-out (D13).
   - `_fetch` sets `redirect: 'manual'` on its `RequestInit`, so a
     redirect is never followed to a login page on another origin.
   - `_check` treats a response whose `type` is `opaqueredirect` as the 401
     path does: it throws the `Unauthorized` that `bridgeFailure(401, …)`
     builds, with the body `signed out: reload to sign in again`.
   - The class comment says why: most proxies answer an expired session with
     a redirect, not a 401.
6. In `transport_web_test.dart`, delete the tests that check the token. Keep
   the rest, built with `WebTransport(base: …)`. Add `a redirect is a
   sign-out`: a fake bridge that answers 302 to another origin makes `send`
   throw `Unauthorized`, and makes `lines` fail with it.

**Verify:**
- `cd app && flutter analyze` reports nothing new.
- `cd app && flutter test --platform chrome test/client/transport_web_test.dart`
  passes, if Chrome is installed. Otherwise
  `cd app && flutter test test/client/` passes.
- `grep -rn 'TokenStoreWeb\|adoptStudioCode' app/lib` prints nothing.

## Task 17: A forwarded host carries no token, and a refusal is the proxy's

**Goal:**
- The relayed transport is a path prefix and nothing else.
- The per-host browser tokens, Sign in and Sign out are deleted.
- The unauthorized state reads as D13 words it.

**Files:** `app/lib/src/client/relayed_transport.dart`,
`app/lib/src/client/host_tokens.dart`, `host_tokens_web.dart` and
`host_tokens_stub.dart` (deleted), `app/lib/src/client/host_discovery_stub.dart`,
`app/lib/src/client/host.dart`, `app/lib/src/model/hosts_model.dart`,
`app/lib/src/model/fleet_store.dart`, `app/lib/src/ui/settings/hosts_section.dart`,
`app/lib/src/ui/kit/state_message.dart`, `app/lib/src/ui/debug/connection_check.dart`,
`app/lib/main.dart`, `docs/studio-flutter/design-system.md`, and tests:
`app/test/client/relayed_transport_test.dart`, `app/test/model/hosts_model_test.dart`,
`app/test/ui/settings_test.dart`, `app/test/support/screens.dart`,
`app/test/kit/state_message_test.dart` (if it pins the words), and the gallery
goldens.

**Steps:**
1. **The transport.** `relayedTransport` takes `relay` and `host` only, as
   Contracts declares it.
   - `_forward` keeps the path prefix and the route's own headers.
   - `HostTokenProvider`, `_hostTokenHeader`, `_refused` and both refusals
     are deleted.
   - The comment says the daemon that served the page forwards the request
     to the host's tunnel (D10).
   - `host_discovery_stub.dart` drops its `token:` argument.
2. **The host tokens.** Delete the three `host_tokens*.dart` files, and the
   `hostTokenWriters` wiring in `main.dart` (`:11`, `:761–762`) with its
   comment.
3. **`HostsModel`.**
   - Delete `storeToken`, `clearToken`, `retry` and `canSignIn`, from the
     fields and from both constructors.
   - Delete `signIn`, `signOut` and `canSignOut`.
   - `main.dart` stops passing `retry`.
   - The class comment says a browser lists the hosts its daemon forwards to,
     and adds none.
4. **`HostDetail`.**
   - Delete the Token field, Sign in and Sign out, with their state
     (`_token`, `_failure`, `_signingIn`, `_signingOut`), `_relayRefused`, and
     the "Sign in to the relay first" line.
   - The button row shows Remove for a host with a destination when
     `canAdd`, as plan 2 made it.
5. **`HostsSection`.**
   - The browser's planned add row says: "Add hosts with `marshal fleet add`
     on the machine this daemon runs on" (D15).
   - `_kind` for a host with `via` reads `Through <via>, over SSH to
     <destination>` when there is a destination, else `Through <via>`.
6. **The unauthorized state** means the proxy refused the request:
   - `StateMessage.unauthorized` reads `<host or "This Studio"> is signed
     out`, and `signInAgain` becomes `Reload the page to sign in again.`;
   - their comments change to match;
   - `HostView.reachWords` gives `signed out`, and `refused`'s comment says
     the proxy refused;
   - the `HostReach` doc (`host.dart:9–13`) says the same;
   - `connection_check.dart:161` gives `signed out`;
   - `design-system.md:341`'s bullet becomes: "**Unauthorized**: the
     operator's proxy refused the request, which no retry clears. Name the
     host, and say to reload the page to sign in again." The design system
     (§10) changes the document, the kit and the goldens together.
7. **Fixtures** (`screens.dart`):
   - delete `settings · hosts · sign in` and `_hostsSignIn`;
   - `_hostsRelayed` drops its `clearToken` arguments and its refused-hub
     half, keeping one forwarded host with a destination;
   - `_FixtureFleetProfiles` is unchanged.
8. **Tests:**
   - `relayed_transport_test.dart`: delete the token tests, and keep the
     prefix and encoding tests;
   - `hosts_model_test.dart`: delete the sign-in and sign-out tests;
   - `settings_test.dart`: delete `an unauthorized host offers sign in`, `a
     relayed host behind a refused relay says to sign in to the relay` and `a
     relayed host offers sign out where a store can clear it`. Add `a browser
     says where hosts are added`;
   - regenerate the gallery goldens on macOS with `flutter test
     --update-goldens test/gallery_test.dart`, and check that only the
     unauthorized cell changed.

**Verify:**
- `cd app && flutter analyze` reports nothing new.
- `cd app && flutter test` passes in full.
- `grep -rn "token refused\|refused this page\|HostTokenProvider\|storeToken" app/lib`
  prints nothing.

## Task 18: The fleet client has no rejected state

**Goal:** a 401 is an ordinary failure to the fleet client.
`acpclient.FleetRejected` and the fleet screen's two cases for it are gone.

**Files:** `internal/fleetclient/client.go`, `internal/fleetclient/actions.go`
(if it calls `unauthorized`), `internal/acpclient/fleet.go`,
`internal/app/tui/fleet/events.go`, `internal/app/tui/fleet/view.go`, and
tests: `internal/fleetclient/client_test.go`,
`internal/app/tui/fleet/fleet_test.go`.

**Steps:**
1. In `client.go`, delete `unauthorized` (`:468`) and `reject` (`:711`).
   - At every call site, remove the `unauthorized(err)` branch. A failed read
     or stream is then handled as any other failure: reconnecting, then down.
   - In `readOverflow` (`:620`), the second return value goes, and its caller
     drops the check.
   - `connected` (`:684`) drops its `FleetRejected` guard and comment.
2. Delete `FleetRejected` from `acpclient/fleet.go:19`.
3. Delete the `FleetRejected` case in `events.go:63`. In `view.go:836`, the
   case reads `case acpclient.FleetDown:`.
4. Tests:
   - delete the `client_test.go` tests that assert the rejected state, or
     that a 401 stops the client (find them by `FleetRejected`, `reject` and
     401);
   - add `TestA401IsAnOrdinaryFailure`: a bridge that answers 401 to
     `/api/agents` leaves the client reconnecting, and it connects once the
     bridge answers 200;
   - delete `fleet_test.go:357`'s rejected case.

**Verify:**
- `go test ./internal/fleetclient ./internal/acpclient/... ./internal/app/tui/fleet -count=1`
  passes.
- `grep -rn 'FleetRejected\|reject()' internal/` prints nothing.

## Task 19: `marshal mcp` relays a local MCP client over the socket

**Goal:** `marshal mcp --credential-file PATH [--socket PATH]` reads MCP
JSON-RPC lines on stdin, POSTs each to `/mcp` over the daemon's socket with
the credential, and writes each answer as a line on stdout.

**Files:** `internal/daemon/client.go`, `internal/daemon/client_test.go`,
`cmd/marshal/mcp.go` (new), `cmd/marshal/mcp_test.go` (new), and the
dispatch line in `cmd/marshal/main.go`.

**Steps:**
1. Add `PostMCP` to the daemon client, as Contracts declares it.
   - It POSTs `body` to `"http://"+c.host+"/mcp"` with `Authorization: Bearer
     <credential>`, `Content-Type: application/json` and `Accept:
     application/json, text/event-stream`.
   - It answers the status and the body, read up to 8 MiB.
   - A transport failure is the error.
2. In `main.go`'s `run`, dispatch `mcp` to `runMCP(ctx, args[1:], stdin,
   stdout, stderr)` beside `studio`.
3. `cmd/marshal/mcp.go` holds `mcpPoster`, `mcpClientFor`, `runMCP` and
   `readCredentialFile`, as Contracts declares them.
   - **Flags.** `--credential-file` is required. `--socket` defaults to
     `config.DaemonSocketPath(home)`.
   - **The credential.** `readCredentialFile` refuses a file group or other
     can read, as `readSecretFile` did (`cmd/webbridge/main.go:432`, deleted
     in Task 5): `marshal mcp: <path> is mode <m>; a credential file must be
     0600`. It answers the trimmed contents, and refuses an empty file.
   - **The loop.** It reads stdin one line at a time with a `bufio.Reader`, so
     a line has no length limit, and skips blank lines.
     - Each line is handled on its own goroutine, with at most 8 at once.
     - Writes to stdout go through one mutex, and each answer is one line.
   - **One line.**
     - `PostMCP` answering 200 with a body: compact the body to one line with
       `json.Compact` and write it.
     - 202 or 204, or an empty body: write nothing.
     - Any other status, or a transport error, when the line has an `id`:
       write a JSON-RPC error response with that `id`. Its code is -32603,
       and its message is `marshal mcp: the daemon answered <status>: <its
       error>`, or `marshal mcp: <err>`.
     - The same for a notification (no `id`): write the message to stderr
       only.
   - **The end.** Stdin closing waits for the requests in flight, then
     returns nil. The context ending returns its error.
4. Add `mcp` to `AGENTS.md`'s subcommand list in Task 21, not here.

**Tests:**
- `TestPostMCPSendsTheCredential` (`client_test.go`): on a socket stub, the
  method, path, headers and body.
- `TestMCPRelaysARequestAndItsAnswer` (`mcp_test.go`): a scripted poster.
- `TestMCPWritesNothingForANotification`.
- `TestMCPAnswersAFailureWithAnError`: a 401 gives a JSON-RPC error with the
  request's `id`.
- `TestMCPNeedsAPrivateCredentialFile`: mode `0644`, an empty file, and no
  flag.
- `TestMCPHandlesConcurrentRequests`: two requests whose answers are released
  in reverse order both arrive, each a whole line.
- `TestPostMCPOverTheRealSocket` (`test/daemon`): the daemon harness, and an
  MCP client created with `POST /api/clients` over its socket, whose answer
  carries the credential. `daemon.Client.PostMCP` with that credential
  answers `initialize` and then `tools/list` with 200. `cmd/marshal` is a
  `main` package, so the loop itself is `mcp_test.go`'s.

**Verify:** `go test ./internal/daemon ./cmd/marshal ./test/daemon -run 'MCP' -count=1`
passes.

## Task 20: The e2e suite dials the web listener

**Goal:** the suite sends no token, and `--base` takes a unix socket or a
loopback URL.

**Files:** `scripts/e2e/e2e_client.py`, `scripts/e2e/harness.py`,
`scripts/e2e/run_e2e.py`, `scripts/e2e/checks_errors.py`,
`scripts/e2e/checks_spa.py`, `scripts/e2e/README.md`.

**Steps:**
1. `e2e_client.py`:
   - `Client(base, timeout)` loses `token`, and `request` loses its `token`
     keyword and the header lines (`:98–100`).
   - A `base` of `unix:<path>` sends every request over that socket. Use an
     `http.client.HTTPConnection` subclass whose `connect` opens an
     `AF_UNIX` socket. The request URL's path and query are sent as they are.
   - Rewrite the module docstring: no bearer token, the web listener.
2. `run_e2e.py` and `harness.py`:
   - `--token`, `E2E_TOKEN`, the required-token check (`:76–78`) and the
     `token` field go;
   - `--base`'s help says `unix:/path/web.sock` or `http://127.0.0.1:PORT`;
   - the usage examples drop `--token`.
3. Delete group H's bearer checks (`checks_errors.py:41`, `:167`, `:316`) and
   group I's bearer contract (`checks_spa.py:151–171`). Add one group H check:
   a socket-only route (`POST /api/projects/pin`) answers 404 on the web
   listener.
4. `README.md` follows: the run command, and a sentence on the base forms.

**Verify:**
- `python3 -m py_compile scripts/e2e/*.py` succeeds.
- `grep -rn 'token' scripts/e2e/*.py` prints only the preview-token and
  status-link checks.
- Task 22 runs the suite.

## Task 21: The development box and the docs follow

**Goal:** `dev-studio.sh` runs its daemon on a loopback web listener with no
token, and the docs describe the web listener.

**Files:** `scripts/dev-studio.sh`, `docs/dev-loop.md`, `AGENTS.md`,
`cmd/webbridge/README.md`.

**Steps:**
1. **`scripts/dev-studio.sh`.**
   - The `ExecStart` line (`:281`) replaces `--addr 127.0.0.1:7700` with
     `--web-listen 127.0.0.1:7700`.
   - Delete the token and everything that carries it:
     - `ensure_token`, `read_token` and `token_file`;
     - `env_file` (`:192`), which holds only `WEBBRIDGE_TOKEN`, and the unit's
       `EnvironmentFile=` line (`:282`);
     - `print_token` and the `--token` option;
     - the `Authorization` header in both `curl` probes (`:302`, `:338`);
     - the `token:` status line (`:351`).
   - In the header (`:32–48`), the studio is a second daemon only so that a
     Studio rebuild, which relinks the embed, does not restart the TUI's
     daemon (plan 4 removes that). Delete the token paragraph.
   - `--token` falls to the unknown-option error.
2. **`docs/dev-loop.md`.**
   - The studio's table row (`:10`), "The studio's token and state"
     (`:133–160`), "Reaching studio from the TUI fleet screen" (`:161–182`) and
     "A relay on the development box" (`:243–291`) lose everything about the
     token, `token_cmd` and the relay. The last two sections are deleted.
   - "Reaching studio from another machine" keeps `tailscale serve`, and says
     the proxy is now the only gate: tailnet membership is the login, and
     Marshal checks nothing (D13).
   - Add a short section, "Pointing an MCP client at the daemon": create a
     client in Settings › Access, save its credential to a `0600` file, and
     configure the client to run `marshal mcp --credential-file <file>`.
3. **`AGENTS.md`.**
   - Replace the paragraph that begins "A bridge given `--relay-join
     NAME=PATH` is a relay" with one that says:
     - `[daemon] web_listen` (`unix`, a socket path, or a loopback address)
       opens the web listener, which serves the Studio and the API with no
       bearer check, behind the operator's proxy (one-daemon D10, D13);
     - `GET /api/hosts` and `/api/hosts/<name>/…` forward to each host's
       tunnel;
     - the socket-only routes are not served there;
     - it answers only `localhost`, the loopback addresses and `web_url`'s
       host, and refuses a cross-site state-changing request and a non-JSON
       `/api/` body (`web/bridge/webguard.go`).
   - Delete the sentence "The bridge serves the agent attach on its network
     listener behind the bearer token, over TLS or on a loopback address, and
     refuses it 403 elsewhere."
   - Add `mcp` (a stdio relay to `/mcp` over the socket) to the subcommand
     list.
4. **`cmd/webbridge/README.md`.**
   - Replace the description of `--addr`, `--token` and `--token-file` with
     `--web-listen`, `--web-group`, `--preview-addr` and `--preview-url`.
   - Add one line: `--addr` and `--token-file` are retired and ignored, and
     `--token`, `--tls-cert` and `--tls-key` are refused.
   - The TLS section is replaced by one sentence: TLS belongs to the proxy
     (D3). Plan 5 sweeps the rest.

**Verify:**
- `bash -n scripts/dev-studio.sh` passes.
- `grep -n 'token' scripts/dev-studio.sh` prints nothing.
- `grep -n -- '--relay-join\|join-token\|marshal daemon token' AGENTS.md docs/dev-loop.md`
  prints nothing.

## Task 22: Full verification

**Steps:**
1. `gofmt -l .` prints nothing, and `go vet ./...` passes.
2. `CGO_ENABLED=1 go build ./cmd/marshal ./cmd/webbridge` succeeds.
3. `scripts/test.sh` (the packages the diff affects) passes. Compare it with
   the baseline at the top of this plan.
4. `go test ./web/bridge -run TestWebIsStdlibOnly -count=1` and `go test
   ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -count=1`
   pass.
5. `cd app && flutter analyze && flutter test` passes.
6. **By hand on macOS.**
   1. Set `[daemon] web_listen = "127.0.0.1:7700"` and `web_url =
      "http://127.0.0.1:7700"`, then run `marshal daemon install`.
   2. `marshal daemon status` shows `web: 127.0.0.1:7700 (loopback: …)`.
   3. `marshal studio --print` prints the URL. The page loads with no sign-in,
      lists agents, and lists plan 2's `self` host. Its Hosts section is
      read-only with D15's note.
   4. Opening an agent on `self` works through `/api/hosts/self/…`, and
      `curl -X POST 127.0.0.1:7700/api/hosts/self/api/projects/pin` answers
      404.
   5. A workspace agent's preview opens on its own `p<token>.localhost` origin.
   6. With `web_listen = "unix"`, `curl --unix-socket <daemon
      dir>/web.sock http://x/api/agents` answers, and `web.sock` is `0600`.
   7. An old unit with `--addr off --token-file …` starts, with one warning in
      `webbridge.log`.
   8. The guard (Task 14):
      - `curl -H 'Host: evil.example' 127.0.0.1:7700/api/agents` answers 403;
      - a page on another origin that `fetch`es `POST /api/agents` with
        `mode: 'no-cors'` is refused (403 in the daemon's log);
      - the Studio itself still spawns an agent.
7. **The e2e suite** against the web listener: `python3 scripts/e2e/run_e2e.py
   --base unix:<daemon dir>/web.sock ...`, with the workspace and the agent
   image `scripts/e2e/README.md` names. Every group passes, or each failure
   is shown to fail at the baseline too.
8. **An MCP client.** Point Claude Code at `marshal mcp --credential-file
   <file>` with a client from Settings › Access. `tools/list` answers, and a
   factory read tool works.
9. **The development box.** Run `scripts/dev-studio.sh --install`. The studio
   answers on `127.0.0.1:7700` with no token, and `tailscale serve` still
   reaches it.

## Integration notes

- **`web_url` must name what the proxy publishes.** The guard answers only
  `localhost`, the loopback addresses and `web_url`'s host. A proxy that
  passes its public `Host` through (Tailscale Serve, Cloudflare, Caddy) needs
  `web_url` set to that address, or every request is refused, with a reason
  that says so.

- **The browser needs a proxy from here on.** A web Studio that was reached
  on `127.0.0.1:7788` with a token now needs `web_listen`. Anything beyond
  this machine needs a proxy in front of it: Tailscale Serve, Cloudflare
  Access, Caddy with an auth module, oauth2-proxy. Marshal checks no browser
  (D13).
- **Local MCP clients move to `marshal mcp`.** A client configured with
  `http://127.0.0.1:7788/mcp` stops connecting. It either runs `marshal mcp
  --credential-file`, or uses a loopback `web_listen` with the D13 caveat.
- **Relayed hosts are gone.** A relay set up with `--relay-join` and `marshal
  daemon join-token` stops relaying at the upgrade. Its hosts become
  `[hosts.*]` tables over SSH (plan 2).
- **Reinstall after upgrading.** An installed unit keeps starting, and its
  retired flags are warned about in the log. Run `marshal daemon install` to
  rewrite it and remove `studio.token` and `join.token`.
- **`compose.yaml` stops serving a browser.** It can no longer bind
  `0.0.0.0`. Plan 5 deletes it.
- **The Studio is still embedded.** A Studio change still relinks
  `webbridge`, so `dev-studio.sh` keeps its second daemon until plan 4.
