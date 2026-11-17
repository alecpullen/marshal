# P5 plan 7: server profiles — implementation plan

Date: 2026-11-16. Status: ready for review.

Base: `origin/main` at `1bc6d64b`, where plans 1 to 6 are merged. Every
anchor below was checked there.

- **The baseline** is CI's suite command at `1bc6d64b`: `scripts/test.sh full`
  passed, 127 packages ok, exit 0. It is recorded in the spec's "Plan 7"
  record entry, which this batch shares with plan 8.
- **The spec edits are already made,** uncommitted in the main checkout, in
  `docs/daemon/p5-fleet-screen.md`: the status lines, F7's "Decided by plan 7"
  bullets, Migration order items 7 and 8, the Design table, Findings, and the
  "Plan 7" and "Plan 8" record placeholders.
- **Plan 8 runs beside this plan.** Its files are the agent's serving code,
  the container transport, the relay and the contract. The two plans share
  `cmd/marshal/main.go`, in different functions (see Execution), and only
  plan 8's manual run waits for this plan on `main`.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decision 7;
F3's remote transport; F4's `--server`; F5's attach on the TLS listener; F7;
F8's `notify`; Migration order item 7; Tests W8.
Previous plans: [plan 3, the fleet screen](2026-11-14-p5-3-fleet-screen-plan.md),
whose client this plan gives a second transport;
[plan 4b, opening an agent](2026-11-14-p5-4b-open-agent-plan.md), whose agent
attach and daemon loop this plan takes to a server;
[plan 6, needs you](2026-11-15-p5-6-needs-you-plan.md), whose `WithNotify`
this plan sets from the file.
Sibling plan: [plan 8, containers](2026-11-16-p5-8-containers-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **`marshal fleet --server NAME` runs the fleet loop on a server-profile
  bridge** on another machine: the same screen, its actions, opening an
  agent's session over TLS, and returning to the fleet.
- **The profile lives in `fleet.toml`,** kept private, with no token in it.
  The token comes from the profile's `token_cmd` or from the keychain, where
  `marshal fleet login NAME` stores it.
- **The bridge serves the agent attach on its network listener,** with the
  bearer token, over TLS, or over plain HTTP on a loopback address only.
- **`notify = false`** turns notifications off for a server, or for the local
  daemon in the file's `local` table.

## Non-goals

- **Containers' viewers and their allowlist.** They are plan 8. Until it
  merges, a container's agent on a server is still refused with plan 6's 409.
- **Several servers at once,** and switching servers inside the screen (H1).
- **`marshal attach` and plain `marshal` against a server.** A server has no
  project children, so only the fleet's agents are reachable there.
- **Removing a stored token.** Running `marshal fleet login NAME` again
  replaces it.
- **A proxy.** Neither the fleet client nor the attach goes through an HTTP
  proxy, so both always reach the same address.

## Assumptions

Each was checked at `1bc6d64b`.

- **The fleet client's transport** already has a name, a base URL, an HTTP
  client and a token function (`internal/fleetclient/client.go:30`). The token
  function is called for every request (`authorize`, `client.go:483`), and a
  401 on any request stops the client with "token rejected for <server>"
  (`client.go:686`).
- **The daemon client dials only the unix socket.** `NewClient` takes a socket
  path (`internal/daemon/client.go:52`), and the attach handshake's `upgrade`
  sets the host to `daemon` (`client.go:220`) and dials that socket
  (`client.go:229`).
- **The network listener serves `ServeHTTP`** (`web/bridge/http.go:132`): the
  mux behind `bearerAuth` when a token is set (`http.go:113`,
  `web/bridge/auth.go:14`). The attach routes exist only in `SocketHandler`
  (`http.go:143`), and `TestTheTCPHandlerHasNoAgentAttach`
  (`web/bridge/attach_test.go:695`) pins the network's 404.
- **The bridge's TCP listener always has a token:** `webbridge` generates one
  when none is given and the TCP listener is open (`cmd/webbridge/main.go:500`).
  It serves TLS with `--tls-cert` and `--tls-key` through `ServeTLS`
  (`cmd/webbridge/main.go:835`), which also offers HTTP/2.
- **The attach hijacks the connection** (`web/bridge/attach.go:308`), which an
  HTTP/2 connection cannot do. A TLS client that offers only `http/1.1` gets an
  HTTP/1.1 connection from `ServeTLS`.
- **Every server-profile agent has a viewer socket after plan 6,** except a
  container's: a host process gets one wherever `newRuntime` falls back to it
  (`web/bridge/fleet.go:468`), on either profile.
- **Launch's daemon loop** builds its fleet client for the socket
  (`internal/launch/daemon.go:429`), dials every session through the daemon
  client (`daemon.go:567`) with `SharedFilesystem: true` (`daemon.go:591`),
  opens the connect panel when this machine has no user config
  (`daemon.go:517`), and adds a version note that says to restart the daemon
  (`daemon.go:348`). `launch.Fleet` probes the local daemon first
  (`internal/launch/fleet.go:39`).
- **`WithNotify`** sets the loop's notifications (`internal/launch/launch.go:85`);
  nothing sets it from a file yet.
- **`marshal fleet`** refuses every argument (`cmd/marshal/main.go:182`).
  Plain `marshal` and `marshal attach` reach launch at `main.go:146` and
  `main.go:177`.
- **The user config directory** is `config.UserDir(home)`
  (`internal/app/config/locations.go:12`), which honours `MARSHAL_CONFIG_DIR`
  and `XDG_CONFIG_HOME`.
- **The keychain** is `internal/credentials`: `New(service)` opens the OS store
  and fails with `ErrKeyringUnavailable` when there is none, and `Get` reports a
  missing key with `ErrNotFound` (`internal/credentials/credentials.go:78`,
  `:32`). `MemStore` is the test double (`credentials.go:148`).
- **Strict TOML decoding** is `DisallowUnknownFields` on go-toml v2's decoder,
  as `internal/workspacecfg/parse.go:64` does.
- **A terminal read without echo** is `term.ReadPassword` from
  `github.com/charmbracelet/x/term`, already a direct dependency
  (`internal/trust/prompt.go:10`).
- **The spawn modal's no-project sentence** names `marshal daemon pin`
  (`internal/app/tui/fleet/spawn.go:22`), which a server does not have.

## Decisions this plan makes beyond the spec

1. **A new package, `internal/fleetprofile`, owns the file, its tokens and its
   TLS.** Launch and `cmd/marshal` use it. `internal/fleetclient` and
   `internal/daemon` take a TLS configuration and a token function, and never
   import it.
2. **The file's shape.**
   - `[servers.<name>]` tables, each with `url`, and optionally `token_cmd`,
     `ca_file` and `notify` (true when absent). A `[local]` table holds
     `notify` for the daemon.
   - A server's name is letters, digits, `.`, `_` and `-`. `local` is refused
     as a server's name, because the footer already shows "local" for the
     daemon.
   - `url` is an origin: `https://host[:port]`, or `http://` when the host is
     `localhost` or a loopback address. A path other than `/`, a query, a
     fragment or user information is an error.
   - `ca_file` is a path, absolute or relative to the file's own directory.
   - A `token` field is an error, "fleet.toml: servers.<name>.token: tokens are
     not kept in this file; run `marshal fleet login <name>` or set
     token_cmd". Any other unknown field, `default` included, is an error
     naming it.
   - A missing file is an empty one: no servers, and notifications on.
3. **The file must be private,** on unix systems: owned by the user, and with
   no group or other permission bits. Otherwise loading it fails with
   "fleet.toml at <path> is mode <mode>: run chmod 600 <path>", or "… belongs
   to uid <n>, not you". Other systems skip the check, as they have no such
   modes.
4. **The token is resolved once per run, before the screen starts,** and kept
   for that run. Every request and every attach use it. F7's "once per
   connect" becomes "once per `marshal fleet --server` run": a reconnect after
   a dropped stream reuses it, and a rejected one stops the client (F3), so the
   user fixes the profile and starts again.
   - `token_cmd` runs through `/bin/sh -c` with a 10s limit and the user's
     environment. Its stdin is the terminal, and its stderr reaches the
     terminal and is kept, so a command that prompts can, and a failure is
     reported with its first stderr line. Its trimmed stdout is the token;
     empty output is a failure.
   - Without `token_cmd`, the keychain entry `marshal:fleet:<name>` under the
     service `marshal` is the token. With no entry: "no token for <name>: run
     `marshal fleet login <name>`". With no keychain: "no token for <name>:
     this system has no keychain; set token_cmd in fleet.toml".
5. **TLS** uses the system roots plus every certificate in `ca_file`, which
   must hold at least one. The minimum version is TLS 1.2, and the only
   protocol offered is `http/1.1`, because the attach is an HTTP/1.1 upgrade.
   There is no option to skip verification.
6. **The attach client for a server** is the daemon client over a TCP dial:
   TLS for `https`, plain for a loopback `http`. The handshake is the same one,
   with the URL's host as the request's host and the bearer token added. Only
   `AttachAgent` is used on it.
7. **The bridge serves the agent attach on its network listener** behind the
   same bearer check as every `/api/` route. After the check, the request must
   have arrived over TLS, or on a connection whose local address is loopback.
   Otherwise the answer is 403, "the agent attach needs TLS".
   - A server built without a token answers the attach 404, as today: an
     attach without a token check never ships.
   - The project attach and pin stay socket-only, 404 on the network listener.
8. **`launch.ServerFleet` runs the loop on a server.**
   - It resolves the token and the TLS configuration first, and fails before
     any program runs when either fails. It does not probe: an unreachable
     server shows as the screen's connection state, as F3 says.
   - The fleet client is the HTTPS transport, named after the profile, which
     the footer and "token rejected for <name>" then show.
   - Every session it opens is dialled with `SharedFilesystem: false`, never
     opens the connect panel (the server's config is not this machine's), and
     its version note reads "This marshal is <ours>; the server's agent is
     <theirs>.", without the daemon restart advice.
   - The rest of the loop is plan 4b's and plan 6's: the needs-you count, the
     watch and `/fleet` work as they do locally.
9. **`cmd/marshal` reads the file once per invocation.**
   - `marshal fleet` and `marshal fleet --server NAME` refuse a file they
     cannot load, and say why. `--server` with no file, or a name the file
     lacks, fails with the file's path and either the server names it has or a
     starter table for NAME.
   - Plain `marshal` and `marshal attach` read only `local.notify`. When the
     file cannot be loaded they print the reason on stderr, keep notifications
     on, and start anyway: a fleet file must not stop the session screen.
   - Every entry point passes `launch.WithNotify` from the file: the server's
     `notify`, or the `local` table's.
10. **`marshal fleet login NAME`** stores a token for a profile the file names.
    - It refuses a profile with `token_cmd`, since a stored token would never
      be read.
    - On a terminal it prompts "Token for <name>: " on stderr and reads without
      echo. Otherwise it reads one line from stdin, so a password manager can
      pipe it in.
    - The trimmed token must not be empty. It is stored under decision 4's key,
      replacing any earlier one, and the command prints "Stored the token for
      <name>."
11. **The spawn modal's no-project sentence on a server** is "add a project
    from the web UI". The model tells a server from the daemon by the state's
    daemon info, which only the local daemon has.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5 plans 7 and 8`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan and `2026-11-16-p5-8-containers-plan.md`.
- Then branch: `git switch -c p5-7-server-profiles` from `main`, or create a
  worktree on that branch.
- **Plan 8 may run at the same time.** It edits the `acp` branch of
  `cmd/marshal/main.go`'s `run` (`main.go:83` to `:116`); this plan edits
  `runFleet` and the two launch calls at `main.go:146` and `:177`. Whichever
  merges second re-runs `go test ./cmd/marshal/`.
- Both plans fill their own record entry in the spec and edit `AGENTS.md`.
  Whichever merges second keeps both.
- Run each task's Verify and commit it as `p5-7-server-profiles: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p5-7-suite`.

## Contracts

### The profile (`internal/fleetprofile`, Task 1)

```go
// File is fleet.toml (P5 F7).
type File struct {
	Path    string            // where it was read from
	Servers map[string]Server // by name
	Local   Local
}

// Server is one named server profile (decision 2).
type Server struct {
	Name     string
	URL      string // an origin: https, or http on a loopback host
	TokenCmd string
	CAFile   string // absolute once loaded
	Notify   bool
}

// Local is the file's [local] table.
type Local struct {
	Notify bool
}

// Path is fleet.toml in the user's config directory for home.
func Path(home string) string

// Load reads and checks the file at path (decisions 2 and 3). A missing file
// is the empty File, with notifications on.
func Load(path string) (File, error)

// Lookup answers the named server, or an error naming the file and what it
// holds (decision 9).
func (f File) Lookup(name string) (Server, error)

// KeychainKey is the credentials key a server's token is stored under.
func KeychainKey(name string) string

// OpenStore opens the keychain; it is called only when a token is read from
// it.
type OpenStore func() (credentials.Store, error)

// CommandRunner runs a token_cmd script and answers its stdout. Its stderr is
// written to stderr as it runs.
type CommandRunner func(ctx context.Context, script string, stderr io.Writer) ([]byte, error)

// RunShell is the production CommandRunner: /bin/sh -c, with the terminal's
// stdin (decision 4).
func RunShell(ctx context.Context, script string, stderr io.Writer) ([]byte, error)

// Token resolves s's token: token_cmd through run, else the keychain through
// open (decision 4).
func Token(ctx context.Context, s Server, open OpenStore, run CommandRunner, stderr io.Writer) (string, error)

// TLSConfig is s's client TLS configuration (decision 5), nil for a loopback
// http URL.
func TLSConfig(s Server) (*tls.Config, error)
```

### The transports (Task 2)

```go
// internal/fleetclient: HTTPS is a server profile's transport: the bridge at
// base, over tlsConfig (nil for a loopback http base), with the token on every
// request.
func HTTPS(name, base string, tlsConfig *tls.Config, token func(ctx context.Context) (string, error)) Transport

// internal/daemon: Remote is the attach client of a server-profile bridge at
// base (decision 6). Only AttachAgent is served there.
func Remote(base string, tlsConfig *tls.Config, token func(ctx context.Context) (string, error)) (*Client, error)

// Client gains what upgrade dials and how it names the host:
	dial  func(ctx context.Context) (net.Conn, error)
	host  string
	token func(ctx context.Context) (string, error) // nil on the socket
```

### The bridge (`web/bridge`, Task 3)

```go
// http.go: Server gains
	networkAttach http.Handler // the agent attach behind the bearer check; nil without a token

// attachNeedsTLS is the network listener's refusal (decision 7).
const attachNeedsTLS = "the agent attach needs TLS"

// overTLSOrLoopback reports whether r arrived over TLS, or on a connection
// whose local address is loopback.
func overTLSOrLoopback(r *http.Request) bool
```

### Launch (`internal/launch`, Task 4)

```go
// ServerFleet runs the fleet screen on the server profile srv (decision 8).
func ServerFleet(ctx context.Context, stdout io.Writer, srv fleetprofile.Server, opts ...Option) error

// withTokenStore and withTokenRunner replace the keychain and token_cmd's
// runner; tests inject them.
func withTokenStore(open fleetprofile.OpenStore) Option
func withTokenRunner(run fleetprofile.CommandRunner) Option

// options gains:
	remote *remoteTarget // a server profile's loop; nil for the local daemon

// remoteTarget is what a server's loop dials instead of the daemon.
type remoteTarget struct {
	name   string
	fleet  fleetclient.Transport
	attach daemonClient
}
```

### The command (`cmd/marshal`, Task 5)

```go
var fleetServerRunner = launch.ServerFleet

// fleetLoginStore opens the keychain `marshal fleet login` writes to; tests
// replace it.
var fleetLoginStore fleetprofile.OpenStore

// runFleet gains stdin and stderr, for login.
func runFleet(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, embedded bool) error

// runFleetLogin is `marshal fleet login NAME` (decision 10).
func runFleetLogin(ctx context.Context, args []string, stdin io.Reader, stderr io.Writer, home string) error

// localNotify is local.notify for plain marshal and marshal attach, printing
// why on stderr when the file cannot be loaded (decision 9).
func localNotify(home string, stderr io.Writer) bool
```

## Task 1 — the profile file, its tokens and its TLS

**Files:** `internal/fleetprofile/profile.go`, `token.go`, `tls.go`,
`owner_unix.go` and `owner_other.go` (all new), their tests, and
`main_test.go` calling `testenv.Run`.

1. `Path` joins `config.UserDir(home)` and `fleet.toml`.
2. `Load` reads the file and decodes it strictly into decoding types of its
   own, with `notify` as a pointer so an absent one reads as true. It then
   applies decision 2's rules and decision 3's check, in that order of
   reporting: the mode or owner first, since a file others can read is refused
   before its contents are trusted. Every error starts with "fleet.toml" and
   names the field it is about. `ca_file` is made absolute against the file's
   directory.
3. `Lookup` answers the server, or an error carrying the path and the file's
   server names, or, when the file has none, a starter table for the name with
   `url` and `token_cmd` lines.
4. `Token` follows decision 4. The command's context carries the 10s limit,
   and a timeout is reported as "token_cmd for <name> did not finish in 10s".
5. `TLSConfig` follows decision 5. A `ca_file` that cannot be read, or holds no
   certificate, is an error naming it.
6. Tests:
   - `TestAMissingFileIsEmpty`: no servers, and `Local.Notify` true.
   - `TestTheFileReadsServersAndLocal`: two servers and a `local` table, with
     `notify` absent reading true and `notify = false` reading false.
   - `TestATokenFieldIsRefused`, with decision 2's sentence.
   - `TestUnknownFieldsAreRefused`, `default` among them.
   - `TestServerNamesAreChecked`: a name with a space and the name `local`.
   - `TestURLsMustBeOrigins`: `https` with a port passes; `http` on
     `localhost`, `127.0.0.1` and `[::1]` passes; `http` on another host, a
     path, a query and user information fail.
   - `TestTheFileMustBePrivate` (unix): mode `0644` fails with decision 3's
     sentence, and `0600` and `0400` pass.
   - `TestACAFileIsResolvedAgainstTheFile`.
   - `TestLookupNamesTheServers` and `TestLookupOnAnEmptyFileGivesAStarter`.
   - `TestTokenRunsTheCommand`: a fake runner's output, trimmed, is the token,
     and the store is never opened.
   - `TestATokenCommandFailureCarriesItsStderr`: the first stderr line is in
     the error. `TestEmptyTokenOutputFails`.
   - `TestATokenCommandIsBounded`: a runner that waits for its context ends
     with the 10s sentence, under a shortened limit the test sets through the
     package's own seam.
   - `TestTokenFallsBackToTheKeychain`, `TestNoTokenSaysToLogIn` and
     `TestNoKeychainSaysToUseTokenCmd`, over a `MemStore` and an opener that
     fails with `ErrKeyringUnavailable`.
   - `TestRunShellRunsSh`: `RunShell` with `printf` gives its output, and a
     script that writes to stderr and exits 1 fails with that line.
   - `TestTLSConfigTrustsTheCAFile`: an `httptest` TLS server's certificate,
     written to a file, makes a client with the config connect; without it the
     same dial fails verification.
   - `TestTLSConfigOffersHTTP1Only`.

**Verify:** `TMPDIR=/private/tmp/p5-7-suite CGO_ENABLED=1 go test ./internal/fleetprofile/ -count=1`.

**Mutation check:** let `Load` accept mode `0644`;
`TestTheFileMustBePrivate` must fail. Restore.

## Task 2 — the fleet client and the attach over HTTPS

**Files:** `internal/fleetclient/client.go`, `internal/daemon/client.go`,
their tests.

1. `fleetclient.HTTPS` builds a transport whose HTTP client has its own
   `http.Transport`: the TLS configuration, no proxy, and a 10s TLS handshake
   limit. `Local` is false, so the daemon's info is never read. The base is
   the profile's URL with no trailing slash.
2. `daemon.Remote` parses the base and keeps a dial function for its host and
   port (443 or 80 when the URL has none): a TCP dial, then a TLS handshake
   under the caller's context when there is a configuration. `NewClient` keeps
   its unix dial, with `daemon` as its host and no token.
3. `upgrade` dials through the client's function, sets the request's host
   from the client, and adds the bearer token when the client has one. A token
   failure ends the attach before anything is dialled. Everything after the
   dial is unchanged.
4. Tests:
   - `TestHTTPSReadsABridgeOverTLS`: the existing fake bridge
     (`fleetclient/client_test.go`'s `fakeBridge`) behind `httptest`'s TLS
     server; the client connects with the server's certificate trusted, and
     every request carries the token.
   - `TestHTTPSNeverReadsTheDaemon`: no `GET /api/daemon` reaches the fake.
   - `TestARemoteAttachUpgradesOverTLS`: a TLS listener answering the
     handshake as `TestAttachAgentUpgrades` does; the request carries the
     bearer token and the URL's host, and the stream is usable after the
     `101`.
   - `TestARemoteAttachOverLoopbackHTTP`: the same over a plain listener on
     `127.0.0.1`.
   - `TestARemoteAttachRefusesAnUntrustedCertificate`: no configuration that
     trusts the server's certificate, and the attach fails before any request
     is written.
   - `TestARemoteAttachTokenFailureDialsNothing`.
   - `TestARemoteAttachOffersHTTP1`: the listener's TLS state reports
     `http/1.1` as the negotiated protocol.
   - The existing `TestAttach*` and `TestAttachAgent*` tests pass unchanged.

**Verify:** `TMPDIR=/private/tmp/p5-7-suite CGO_ENABLED=1 go test ./internal/fleetclient/ ./internal/daemon/ -count=1 -timeout 10m`.

## Task 3 — the bridge serves the agent attach on its network listener

**Files:** `web/bridge/http.go`, `web/bridge/attach.go`,
`web/bridge/attach_test.go`, `cmd/webbridge/README.md`.

1. `NewServer` builds `networkAttach` when it has a token: the agent attach
   behind `bearerAuth` with that token, which then applies decision 7's
   transport rule before calling `attachAgent`. Without a token it stays nil.
2. `ServeHTTP` routes an agent attach path to `networkAttach` when it is set,
   and leaves every other path, and the attach without a token, as today.
3. `overTLSOrLoopback` reads `r.TLS`, else the connection's local address from
   the request's context (`http.LocalAddrContextKey`). A request with no local
   address is not loopback.
4. Replace `TestTheTCPHandlerHasNoAgentAttach` with:
   - `TestTheNetworkAttachNeedsTheToken`: no token and a wrong one are 401.
   - `TestTheNetworkAttachNeedsTLSOffLoopback`: a request with the token,
     no TLS state and a local address of `192.0.2.1` is 403 with
     `attachNeedsTLS`; the agent's session is untouched.
   - `TestTheNetworkAttachServesLoopbackHTTP`: the same request with a local
     address of `127.0.0.1` reaches `attachAgent`, which answers its own
     refusal for an unknown agent, 404 with its JSON body.
   - `TestTheNetworkAttachServesTLS`: a request with TLS state and a
     non-loopback local address reaches `attachAgent` too.
   - `TestANetworkWithoutATokenHasNoAttach`: a server built with no token
     answers 404.
   - `TestTheTCPHandlerHasNoAttach` stays, and a pin on the network listener
     is still 404.
5. `cmd/webbridge/README.md`: the agent attach is also served on the TCP
   listener, with the bearer token, over TLS or on a loopback address, and
   refused with 403 otherwise.

**Verify:** `TMPDIR=/private/tmp/p5-7-suite CGO_ENABLED=1 go test ./web/bridge/ -run 'Attach|TCP|Network' -count=1 -timeout 10m`,
then `TMPDIR=/private/tmp/p5-7-suite CGO_ENABLED=1 go test ./web/... -count=1 -timeout 20m`.

**Mutation check:** make `overTLSOrLoopback` answer true;
`TestTheNetworkAttachNeedsTLSOffLoopback` must fail. Restore.

## Task 4 — launch runs the loop on a server

**Files:** `internal/launch/fleet.go`, `internal/launch/daemon.go`,
`internal/launch/launch.go`, `internal/app/tui/fleet/spawn.go`, their tests.

1. `ServerFleet` follows decision 8: it resolves the token with
   `fleetprofile.Token` (the keychain and `RunShell` unless the test seams
   replace them, stderr the process's) and the TLS configuration, builds the
   transports of Task 2 into `options.remote`, and runs `runDaemon` with a
   fleet step first. A token or TLS failure is its error, prefixed with the
   server's name.
2. In the loop, with `remote` set:
   - `fleetClientFor` builds the remote transport's client instead of the
     socket's. An injected client still wins.
   - `dialDaemon` attaches through the remote client and dials with
     `SharedFilesystem: false`. An injected daemon client still wins.
   - `daemonTUIOptions` leaves out the connect panel.
   - The version note uses decision 8's server wording.
   - A target naming a daemon session cannot arise, since a server's state has
     no daemon info; the existing "the fleet named none" branch covers it.
3. The spawn modal uses decision 11's sentence when the state has no daemon
   info, and keeps today's otherwise.
4. Tests, with the program runner and the fakes `fleet_test.go` uses:
   - `TestAServerFleetResolvesTheTokenOnce`: a counting token runner runs once,
     though the fleet program, a session screen and the fleet again all run.
   - `TestAServerFleetFailsWithoutAToken`: no program runs, and the error
     names the server.
   - `TestAServerFleetDialsWithoutASharedFilesystem`: the session screen's host
     reports `SharedFilesystem` false.
   - `TestAServerFleetNeverOpensTheConnectPanel`, with no user config file.
   - `TestAServerFleetsVersionNoteNamesTheServer`.
   - `TestAServerFleetNamesItsClientAfterTheProfile`: the client's state names
     the profile.
   - `TestTheLocalFleetIsUnchanged`: `TestFleetRunsTheFleetProgram` and its
     neighbours pass as they are.
   - In `internal/app/tui/fleet`: `TestTheSpawnRefusalOnAServerOmitsPin` and
     `TestTheSpawnRefusalOnTheDaemonNamesPin`.

**Verify:** `TMPDIR=/private/tmp/p5-7-suite CGO_ENABLED=1 go test ./internal/launch/ ./internal/app/tui/fleet/ -count=1 -timeout 15m`.

**Mutation check:** dial a server's sessions with `SharedFilesystem: true`;
`TestAServerFleetDialsWithoutASharedFilesystem` must fail. Restore.

## Task 5 — `marshal fleet --server`, `marshal fleet login`, and `notify`

**Files:** `cmd/marshal/main.go`, `cmd/marshal/main_test.go`.

1. `runFleet` parses `--server NAME` and `--server=NAME`, and `login NAME`;
   anything else is still refused, as is `--embedded`. It loads the file once
   (decision 9) and passes `launch.WithNotify` from the server, or from
   `local` for the daemon.
2. `runFleetLogin` follows decision 10. It reads from the terminal only when
   stdin is one (`term.IsTerminal` on its file descriptor), and opens the
   keychain through `fleetLoginStore`, which is `credentials.New("marshal")`
   in production.
3. Plain `marshal` and `marshal attach` pass `launch.WithNotify(localNotify(…))`.
4. Tests, in the pattern `TestFleetRoutesToTheFleetRunner` uses, with
   `MARSHAL_CONFIG_DIR` pointed at a temporary directory:
   - `TestFleetServerRoutesToTheServerRunner`: both spellings reach
     `fleetServerRunner` with the named profile.
   - `TestFleetServerNeedsAKnownName`, with the server names in the error, and
     `TestFleetServerWithoutAFileGivesAStarter`.
   - `TestFleetRefusesAFileItCannotRead`: mode `0644` stops `marshal fleet`
     with decision 3's sentence, and no runner runs.
   - `TestFleetPassesTheFilesNotify`: `local.notify = false` reaches the local
     runner as notifications off, and a server's `notify = false` reaches the
     server runner.
   - `TestPlainMarshalKeepsGoingPastABadFleetFile`: the app runner still runs,
     with notifications on, and stderr names the file.
   - `TestFleetLoginStoresTheToken`: a piped token lands in a `MemStore` under
     `KeychainKey`, trimmed.
   - `TestFleetLoginRefusesAnUnknownServer`,
     `TestFleetLoginRefusesATokenCmdServer` and
     `TestFleetLoginRefusesAnEmptyToken`.

**Verify:** `TMPDIR=/private/tmp/p5-7-suite CGO_ENABLED=1 go test ./cmd/marshal/ -count=1 -timeout 10m`.

## Task 6 — W8 against a real bridge

**Files:** `test/daemon/server_profile_test.go` (new).

1. A server-profile harness beside `newDaemonHarness`: the same fleet with
   host processes only, served by `bridge.NewServer` with a token, through
   `httptest`'s TLS server on loopback. The server's certificate is written
   to a `ca_file`, and a `fleet.toml` of mode `0600` in a temporary config
   directory names the server, with `token_cmd` printing the token from a file.
2. `TestAServerProfileOpensAnAgentOverTLS` (W8): a git agent spawned from a
   local bare repository, as `TestAnOwnChildsAgentOpensAsAViewer` spawns it,
   takes one turn. Then, through `fleetprofile.Load`, `Lookup`, `Token` (the
   real `RunShell`) and `TLSConfig`: the HTTPS fleet client lists the agent;
   `daemon.Remote`'s `AttachAgent` opens its session; a client dialled with
   `SharedFilesystem: false` reads the turn's transcript; after the viewer
   leaves, the fleet still lists the agent.
3. `TestAServerProfileRefusesAWrongToken`: the attach with another token is
   401, and the fleet client's state is rejected.
4. `TestTheServerFleetOpensAnAgentAndComesBack`: `launch.ServerFleet` with a
   program runner, as `TestTheFleetOpensAnAgentAndLeavesItRunning` drives
   `launch.Fleet`: the fleet program opens the agent, the session screen is on
   the agent's session, and the loop comes back to the fleet with the agent
   selected.
5. W8's refusal off loopback is Task 3's `TestTheNetworkAttachNeedsTLSOffLoopback`:
   a test cannot count on a non-loopback address to listen on.

**Verify:** `TMPDIR=/private/tmp/p5-7-suite CGO_ENABLED=1 go test ./test/daemon/ -count=1 -timeout 15m`.

## Task 7 — the docs, the record and full verification

**Files:** `docs/daemon/p5-fleet-screen.md`, `AGENTS.md`,
`docs/ARCHITECTURE.md`.

1. Fill the "Plan 7" record, as plan 6's is written.
2. `AGENTS.md`: the commands paragraph gains `marshal fleet --server NAME`
   and `marshal fleet login NAME`, with `fleet.toml`'s place; add
   `internal/fleetprofile/` to the map under "App shell & config".
   `docs/ARCHITECTURE.md`'s layer table gains the package.
3. **The manual run.** A server-profile `webbridge` on this machine with
   `--tls-cert` and `--tls-key` from a self-signed certificate and
   `--addr 127.0.0.1:<port>`, and a `fleet.toml` naming it with `ca_file` and
   a `token_cmd`. From `marshal fleet --server NAME`: spawn, open the agent,
   prompt it, answer an approval, read the diff, finish it, and record what
   was seen. Then run `marshal fleet login NAME` with `token_cmd` removed, and
   confirm the keychain token is used.
4. Full verification: `gofmt -l internal cmd test web` prints nothing; `go vet
   ./...` is clean; CI's suite command with `TMPDIR=/private/tmp/p5-7-suite`
   passes; `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`; the usability
   suite passes.

## Done when

- `marshal fleet --server NAME` runs the fleet loop on a server-profile bridge
  over TLS, opening agents and coming back to the fleet.
- The token comes from `token_cmd` or the keychain, never the file, and the
  file is refused unless it is private.
- The network listener serves the agent attach with the token, over TLS or on
  loopback, and refuses it with 403 elsewhere.
- `notify = false` turns notifications off for a server and for the daemon.
- W8 passes, every other test named above passes, each mutation check fails
  as described, and CI's suite command passes.
