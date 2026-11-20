# One daemon plan 6: previews of a remote host's agents — implementation plan

Date: 2026-11-20. Status: approved 2026-11-20.

Base: `main` at `bbdc4486`, with plans [1](2026-11-20-od-1-one-daemon-plan.md),
[2](2026-11-20-od-2-hosts-over-ssh-plan.md) and
[3](2026-11-20-od-3-web-listener-plan.md) applied first. It does not depend on
plans 4 and 5, and conflicts with neither. Anchors are at `bbdc4486` where
the code exists there. Plans 2 and 3 create the code most steps change, and
those steps name its functions as plans 2 and 3 declare them.

Approved design: [design.md](../design.md) open question 1, with its
recommendation: a second forward in the same `ssh` process, so a preview
keeps an origin of its own. Planning settled these, and design.md records them:
- **The tunnel forwards the preview port too.** The tunnel helper reads the
  remote daemon's preview address from its status document. It adds `-L
  127.0.0.1:<free local port>:<remote preview address>` to the same `ssh -N`,
  and reports the local port as `DaemonHost.previewPort`.
- **The caller names its own view of the preview listener.** A request that
  issues a preview may carry `X-Marshal-Preview-Origin`, the origin through
  which the caller reaches this daemon's preview listener. The daemon builds
  the preview link from it, in place of its own port. Plan 3's
  `p<token>.localhost` rule still applies, so each preview keeps its own
  origin.
- **Who sets the header.** The native Studio sets it for a host it dials over
  the tunnel. The local daemon's forward sets it for a host it forwards to,
  after deleting any value the caller sent.
- **A moved preview listener restarts the tunnel.** A remote daemon that
  restarts on a new preview port is noticed by the live check, which restarts
  the tunnel onto the new port.

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- A preview of an agent on a remote host opens from the native Studio, and
  from a browser on the machine whose daemon keeps the tunnel.
- The preview has an origin of its own (`p<token>.localhost:<port>`), as a
  local preview does.
- `marshal daemon status` and `GET /api/daemon` say where each host's previews
  are forwarded.

## Non-goals

- A browser that reaches the daemon from another machine, through a proxy,
  opening a forwarded host's preview. The forwarded port is this machine's
  loopback, which that browser cannot reach. Its own daemon's previews keep
  working through `preview_url` (plan 3).
- Previews of agents that are not container agents with declared preview
  ports. That is unchanged: only a workspace's `[preview]` ports are served.
- Forwarding anything else over the tunnel.

## Assumptions

- **The preview listener ignores the port.**
  - `hostPreviewToken` (`web/bridge/preview.go:310`) reads the token from a
    `p<token>.localhost` host and drops the port. A request forwarded from the
    laptop's port to the remote's listener therefore authenticates as it would
    locally.
  - The `?t=` and cookie path (`preview`, `:326`) does not read the port
    either.
- **The link is built from the request** (`previewOrigin`, `preview.go:269`):
  from the public URL's host, or `r.Host`, plus the preview port. Plan 3 adds
  `preview_url` ahead of both. A request over a tunnel socket carries the
  client's `Host` (`localhost` from the native Studio, `daemon` from the Go
  client), so today the remote answers its own preview port. That port is not
  reachable from the laptop.
- **The remote's preview address.** After plan 3, the preview listener opens
  on `127.0.0.1:0` unless `preview_listen` fixes it. `SetPreviewPort` records
  only the port, and `GET /api/daemon` reports neither.
- **The tunnel helper** (plan 2, `internal/tunnels`):
  - reads the remote status document with `remoteSocket`;
  - starts `forwardArgv`;
  - probes with `Config.Probe(ctx, socket) error`;
  - reports `contract.DaemonHost`;
  - `cmd/marshal`'s `runDaemonTunnels` supplies `Probe` from
    `daemon.NewClient(s).Info`.
- **The forward** (plan 3, `hostForward` in `web/bridge/hostforward.go`)
  rewrites each request to the host's tunnel socket and deletes
  `Authorization` and `Cookie`.
- **The native Studio.**
  - It issues a preview with `BridgeClient.preview`
    (`app/lib/src/client/bridge_client.dart:1105`), called from
    `PreviewModel` (`app/lib/src/model/preview_model.dart:66`).
  - Plan 2's discovery builds one `BridgeClient` per host from the status
    document.
- **OpenSSH** forwards a TCP port and a unix socket in one process with two
  `-L` arguments. `ExitOnForwardFailure` ends the process when either bind
  fails. Not checked here; Task 6 checks it by hand.

## Contracts

```go
// web/bridge/contract/daemon.go

// PreviewOriginHeader carries the origin a caller reaches this daemon's
// preview listener through. A preview link is built from it, in place of the
// daemon's own preview port (one-daemon open question 1).
const PreviewOriginHeader = "X-Marshal-Preview-Origin"

// New DaemonInfo field, after WebError:
// PreviewAddr is the preview listener's bound address (host:port), or ""
// when previews are off.
PreviewAddr string `json:"previewAddr,omitempty"`

// New DaemonHost field, after Socket:
// PreviewPort is this machine's loopback port forwarded to the host's
// preview listener, or 0 when the host serves no previews.
PreviewPort int `json:"previewPort,omitempty"`
```

```go
// web/bridge/preview.go

// SetPreviewAddr records the preview listener's bound address, which
// GET /api/daemon reports. It also sets the preview port, as SetPreviewPort did.
func (s *Server) SetPreviewAddr(addr *net.TCPAddr)

// previewOriginFrom reads PreviewOriginHeader: an http origin with a host and
// a port, and no path, query or fragment. ok is false for anything else, and
// the header is then ignored.
func previewOriginFrom(r *http.Request) (host string, port int, ok bool)

// New Server field (http.go):
previewAddr atomic.Pointer[string]
```

```go
// internal/tunnels

// remoteDaemon reads the answering daemon's socket and preview address from
// a status document (the address is "" when previews are off). It replaces
// remoteSocket.
func remoteDaemon(name string, doc []byte) (socket, previewAddr string, err error)

// forwardArgv gains the preview forward: with previewAddr set, it adds
// -L 127.0.0.1:<previewPort>:<previewAddr> before the destination.
func forwardArgv(ssh string, h fleetprofile.Host, local, remote string, previewPort int, previewAddr string) []string

// freeLoopbackPort answers a port free on 127.0.0.1 at the time of the call.
func freeLoopbackPort() (int, error)

// Config.Probe becomes:
// Probe asks the daemon at socket for GET /api/daemon, and answers its
// preview address. Required.
Probe func(ctx context.Context, socket string) (previewAddr string, err error)
```

```dart
// app/lib/src/client/bridge_client.dart

/// The header a preview request names this client's way to the host's
/// preview listener in (contract.PreviewOriginHeader).
const previewOriginHeader = 'X-Marshal-Preview-Origin';

/// New constructor parameter and field:
BridgeClient(this._transport, {this.previewOrigin});
/// The origin this client reaches the host's preview listener through, sent
/// with every preview request; null sends none.
final String? previewOrigin;

// app/lib/src/daemon/status.dart — new DaemonHost field:
final int previewPort; // 0 when the host serves no previews
```

## Task 1: The contract carries the preview addresses

**Goal:** `DaemonInfo.previewAddr`, `DaemonHost.previewPort` and
`PreviewOriginHeader` exist, the Dart module is regenerated, and the shared
status document shows both fields.

**Files:** `web/bridge/contract/daemon.go`, `app/lib/src/client/contract.g.dart`
(generated), `test/contract/daemon-status.json`.

**Steps:**
1. Add the constant and the two fields, as Contracts declares them.
2. Regenerate with `go test ./web/bridge/contract/dartgen -run
   TestDartContractIsCurrent -update`.
3. In `test/contract/daemon-status.json`:
   - give `daemon` a `previewAddr` of `127.0.0.1:54321`;
   - give plan 2's `box` host a `previewPort` of `54400`;
   - run `go test ./cmd/marshal -run TestTheStatusDocumentIsCurrent -update`.

**Verify:** `go test ./web/bridge/contract/... -count=1` and `go test
./cmd/marshal -run TestTheStatusDocumentIsCurrent -count=1` pass without
`-update`.

## Task 2: The daemon reports its preview address, and honours a caller's origin

**Goal:**
- `GET /api/daemon` reports `previewAddr`.
- A preview request with a valid `X-Marshal-Preview-Origin` gets a link on
  that origin.
- The forward sets the header for the host it forwards to.

**Files:** `web/bridge/preview.go`, `web/bridge/http.go`,
`web/bridge/daemoninfo.go`, `web/bridge/hostforward.go`,
`web/bridge/preview_test.go`, `web/bridge/hostforward_test.go`, and one
wiring line in `cmd/webbridge/main.go`.

**Steps:**
1. Add `previewAddr` to `Server` beside `previewPort` (`http.go:51`). Add
   `SetPreviewAddr` beside `SetPreviewPort` (`preview.go:240`): it stores
   `addr.String()` and calls `SetPreviewPort(addr.Port)`.
   - `Server.daemon` (`daemoninfo.go:43`) sets `out.PreviewAddr` from it when
     set.
   - In `cmd/webbridge/main.go`, the preview block's `SetPreviewPort` call
     becomes `SetPreviewAddr` with the listener's `*net.TCPAddr`.
2. Add `previewOriginFrom`, as Contracts declares it. The scheme must be
   `http` or `https`, the port explicit and in range, and the path empty or
   `/`.
3. In `issuePreview`, when `previewOriginFrom` answers ok, build the origin
   from it in place of `previewOrigin`'s request rules:
   - its host is `isLocalhostName`, so `p<token>.localhost:<port>` with
     `tokenInHost`, as `previewOrigin` does today;
   - or `<scheme>://<host>:<port>` with the `?t=` token.

   The header takes precedence over `preview_url` and the request's host: the
   caller knows its own way in. Without it, nothing changes.
4. In `hostForward`'s `Rewrite`:
   - delete `PreviewOriginHeader` from the outgoing request;
   - when the reported host's `PreviewPort` is positive, set it to
     `http://localhost:<PreviewPort>`.

   A caller cannot choose the origin through the forward.
5. Tests:
   - `TestThePreviewAddrIsReported` (`daemoninfo_test.go`): after
     `SetPreviewAddr`, `GET /api/daemon` holds `previewAddr`, and
     `SetPreviewAddr` sets the port.
   - `TestAPreviewOriginHeaderNamesTheLink` (`preview_test.go`): with
     `http://localhost:54400`, the URL is `http://p<token>.localhost:54400/preview/…`
     with no `?t=`. With `http://box.lan:54400`, it is
     `http://box.lan:54400/preview/…?t=…`.
   - `TestABadPreviewOriginHeaderIsIgnored`: no port, a path, `ftp:`, and
     garbage. Each answers today's URL.
   - `TestTheHeaderWinsOverThePreviewURL`.
   - `TestTheForwardNamesTheHostsPreviewPort` (`hostforward_test.go`): the
     fake tunnel sees `X-Marshal-Preview-Origin: http://localhost:54400`, and
     the caller's own value does not reach it.
   - `TestTheForwardSendsNoPreviewOriginWithoutAPort`.

**Verify:** `go test ./web/bridge -run 'Preview|Forward|DaemonRoute' -count=1`
and `go build ./cmd/webbridge` pass.

## Task 3: The tunnel forwards the preview listener

**Goal:**
- A host whose daemon serves previews gets a second `-L` in its `ssh -N`, to
  a free local port, and the port is reported.
- A remote preview listener that moves restarts the tunnel.

**Files:** `internal/tunnels/ssh.go`, `internal/tunnels/tunnels.go`,
`internal/tunnels/ssh_test.go`, `internal/tunnels/tunnels_test.go`, and the
`Probe` adapter in `cmd/marshal/daemon.go` (`runDaemonTunnels`).

**Steps:**
1. Replace `remoteSocket` with `remoteDaemon`, as Contracts declares it.
   - It reads `daemon.previewAddr` beside `daemon.socket`.
   - An address whose host is not `127.0.0.1`, `::1` or `localhost` is
     treated as `""`: the remote only ever binds loopback (plan 3).
2. Add `freeLoopbackPort`: listen on `127.0.0.1:0`, take the port, close the
   listener.
3. `forwardArgv` gains its two parameters, as Contracts declares them.
   - With `previewAddr` set, `-L 127.0.0.1:<previewPort>:<previewAddr>`
     follows the socket's `-L`.
   - An IPv6 `previewAddr` is bracketed, as `net.JoinHostPort` writes it.
4. In one attempt (plan 2's Task 5, step 5), after `remoteDaemon`:
   - With a preview address, pick a port with `freeLoopbackPort` and pass both
     to `forwardArgv`.
   - When `ssh` exits with an error whose line contains `Address already in
     use`, the next attempt picks a new port. That is the normal backoff,
     with no special case.
   - The host's `PreviewPort` is set when the host goes live, and set back to
     0 when it goes down.
5. `Config.Probe` takes Contracts' new signature. While live, a probe that
   answers a preview address different from the one forwarded stops the
   process. It ends the attempt with `<name>'s daemon moved its preview
   listener to <addr>`, so the next attempt forwards the new one.
   - A probe that answers `""` where one was forwarded counts as moved too.
   - The probe that turns the host live does not count: it only learns the
     address.
6. `runDaemonTunnels`' `Probe` answers `info.PreviewAddr` with the error.
7. Tests:
   - `TestRemoteDaemonReadsThePreviewAddr`, and
     `TestRemoteDaemonIgnoresANonLoopbackPreviewAddr` (`ssh_test.go`);
   - `TestForwardArgvForwardsThePreviewPort`, and
     `TestForwardArgvWithoutPreviews`: the exact argument lists;
   - `TestFreeLoopbackPortAnswersAPort`;
   - `TestALiveTunnelReportsItsPreviewPort` (`tunnels_test.go`);
   - `TestAHostWithoutPreviewsReportsNoPort`;
   - `TestAMovedPreviewListenerRestartsTheTunnel`: the probe answers a new
     address after `live`, so the process is stopped and `Start` is called
     again with the new address;
   - update plan 2's tests for the new `Probe` signature and `forwardArgv`
     parameters.

**Verify:** `go test ./internal/tunnels -count=1 -race` and `go test
./cmd/marshal -run DaemonTunnels -count=1` pass.

## Task 4: `marshal daemon status` says where a host's previews are

**Goal:** a live host with a preview port says so in its status line.

**Files:** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

**Steps:** `daemonHostLine` (plan 2) appends `, previews on
localhost:<port>` when `PreviewPort` is positive and the state is live.

**Tests:** `TestDaemonStatusNamesAHostsPreviewPort`, and the existing host-line
test with no port is unchanged.

**Verify:** `go test ./cmd/marshal -run DaemonStatus -count=1` passes.

## Task 5: The native Studio names its way to a host's previews

**Goal:** a preview requested from the native Studio for an agent on a remote
host carries `X-Marshal-Preview-Origin`, so its link opens on this machine.

**Files:** `app/lib/src/daemon/status.dart`, `app/lib/src/client/bridge_client.dart`,
`app/lib/src/client/host_discovery_io.dart`, `app/test/status_test.dart`,
`app/test/client/bridge_client_test.dart`,
`app/test/client/host_discovery_io_test.dart`.

**Steps:**
1. `status.dart`: `DaemonHost` gains `previewPort`, read with `as int? ?? 0`.
2. `bridge_client.dart`:
   - add `previewOriginHeader`, `previewOrigin` and the constructor
     parameter, as Contracts declares them;
   - `preview` sends the header in its `Request`'s headers when
     `previewOrigin` is set;
   - no other call sends it.
3. `host_discovery_io.dart`: a host whose `previewPort` is positive gets
   `BridgeClient(unixTransport(socket), previewOrigin:
   'http://localhost:<previewPort>')`. The local host gets none.
4. Tests:
   - `status_test.dart`'s fixture test expects `previewPort` 54400 on `box`;
   - `bridge_client_test.dart`: `a preview names the client's preview
     origin`, and `a preview without an origin sends no header`;
   - `host_discovery_io_test.dart`: `a host with a preview port names it`.

**Verify:**
- `cd app && flutter analyze` reports nothing new.
- `cd app && flutter test test/status_test.dart test/client/` passes.

## Task 6: The docs, and verification

**Goal:** the docs say how remote previews reach a laptop, and the whole
works by hand. design.md already records open question 1's answer, written
while planning.

**Files:** `AGENTS.md`, and `docs/DEPLOYMENT.md` if plan 5 has rewritten it.

**Steps:**
1. In `AGENTS.md`'s tunnels paragraph (plan 2's Task 17), add one sentence:
   - the same `ssh` forwards the host's preview listener to a free loopback
     port, reported as `previewPort`;
   - a preview requested through the tunnel names it in
     `X-Marshal-Preview-Origin`, so the link opens on this machine.
2. In `docs/DEPLOYMENT.md`'s "Reaching it from a laptop", if it exists, add
   that previews of the server's agents open in the native Studio on the
   laptop.
3. Run:
   - `gofmt -l .` prints nothing, and `go vet ./...` passes;
   - `scripts/test.sh` passes;
   - `cd app && flutter analyze && flutter test` passes.
4. **By hand.**
   1. Use plan 2's `self` host (`ssh localhost`) and a workspace agent that
      declares a preview port.
   2. `marshal daemon status` shows `host self (localhost): live, previews
      on localhost:<port>`.
   3. In the native Studio, opening the agent's preview on `self` loads from
      `http://p<token>.localhost:<that port>/preview/…`.
   4. `lsof -iTCP:<port>` shows the `ssh` process, not the daemon.
   5. Restart the daemon, which comes back on a new preview port. Within the
      live check's interval, the tunnel restarts, the port moves, and a new
      preview opens.
   6. On Linux against a second machine, the same check passes.

**Verify:** `grep -n 'X-Marshal-Preview-Origin' AGENTS.md` prints the new
sentence, and the step 4 checks pass.

## Integration notes

- **Older remote daemons serve no forwarded previews.** A remote daemon from
  before this plan reports no `previewAddr`, so the tunnel forwards only the
  socket, and its previews stay unreachable from the laptop until it is
  upgraded.
- **A fixed remote port is friendlier.** A remote daemon with
  `preview_listen` set keeps its preview port across restarts, so its tunnel
  never restarts on that account.
- **Browsers elsewhere still cannot open a forwarded host's previews**
  (non-goal). The native Studio, or a browser on the laptop, can.
