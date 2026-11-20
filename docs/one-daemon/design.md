# One daemon, one socket

Supersedes, in part: [`../daemon/p4-local-daemon.md`](../daemon/p4-local-daemon.md)
D1 (the browser's TCP listener and its token);
[`../daemon/p5-fleet-screen.md`](../daemon/p5-fleet-screen.md) F7 (server
profiles, their tokens and TLS); [`../daemon/p8-studio-federation.md`](../daemon/p8-studio-federation.md)
D6–D8 (reshaped here), D11–D17 (the relay and the join) and D18–D23 (the
Studio through a relay); [`../studio-flutter/f1-client.md`](../studio-flutter/f1-client.md)
decisions 3, 5 and 6 (tokens) and 8 (discovery); the Studio's sign-in handover
(PR #151); and the containerized bridge of the self-hosted fleet server
(`compose.yaml`, `build/Dockerfile.bridge`).
Keeps: P7's rule that the client is the only place hosts are merged.
Inventory of what changes: [`inventory.md`](inventory.md).
Status: proposed 2026-11-20. Plans 1–7 approved 2026-11-20.

Anchors are relative to `main` at `bbdc4486`.

## Goal

Marshal Studio is a graphical client for Marshal daemons and the agents,
factory and automations running on them. Reaching a daemon should be
ordinary, and the daemon should not run a separate trust model for each way it
can be deployed.

- **One daemon.** Every machine runs the same daemon, as a user service. No
  `--profile`.
- **One interface.** The daemon's interface is its unix socket, checked by
  user ID. It holds no shared bearer token, and nothing it serves listens on a
  network interface.
- **Remote hosts over SSH.** Another machine's daemon is reached by forwarding
  its socket over SSH. SSH keys are the credential, and SSH or a tailnet gets
  through NAT.
- **The browser through the operator's proxy.** The web Studio is an opt-in
  listener that only a proxy the operator runs can reach. That proxy
  authenticates the browser; Marshal does not.
- **Containers are a sandbox for an agent,** not a way of deploying the daemon.

## Why

Each item below exists because a browser cannot open a unix socket or read the
keychain, or because the daemon is deployed two ways:

- **The local daemon opens a loopback TCP listener by default.** It is
  `daemon.studio_addr` (`internal/app/config/defaults.go:142`, `127.0.0.1:7788`).
  The listener carries a bearer token, `--token-file` and `studio.token`
  (`internal/daemon/daemon.go:41`, `:93`).
- **The browser needs a sign-in handover** to get that token into a tab
  without putting it in a URL. That handover is `web/bridge/studio_access.go`
  in the daemon and `studio_code_web.dart` and `token_store_web.dart` in the
  app.
- **A server profile is an HTTPS origin.** It brings `--tls-cert` and
  `--tls-key`, `fleet.toml`'s `url`, `ca_file` and `token_cmd`, the keychain,
  and `marshal fleet login` and `token` (`internal/fleetprofile/token.go`,
  `tls.go`). The fleet screen and the attach need an HTTPS transport of their
  own (`internal/daemon/client.go:105`, `internal/launch/fleet.go:101`).
- **A host behind NAT, or one a browser cannot reach, needs the relay.** That
  means join secrets, a reversed HTTP/1.1 upgrade and per-host tokens in the
  browser (`web/bridge/hostjoin.go`, `hostrelay.go`, `relayed_transport.dart`,
  `host_tokens*.dart`).
- **There are two profiles.** `applyProfile` (`cmd/webbridge/main.go:307`)
  gives each its own defaults and refusals. The local profile switches the
  container runtime off entirely (`SetHostProcessesOnly`,
  `web/bridge/fleet.go:870`). The server profile runs every agent in a
  container whenever one answers (`newRuntime`, `fleet.go:537`). It is
  deployed as a container itself, with a named state volume and
  `--project-mount`.
- **The Studio is embedded in the daemon.** `webbridge` needs a
  `flutter build web` before `go build` (`web/bridge/assets.go:28`).
- **The development box runs two daemons.** `scripts/dev-studio.sh` runs a
  second `webbridge` with the server profile, as `marshal-studio.service` on
  `127.0.0.1:7700`, with its own token, state and fleet. It does this because
  the local profile refuses containers, egress and secret backends, which the
  Studio's workspace screens drive (`dev-studio.sh:8–16`). It also keeps a
  Studio rebuild, which has to relink the embed, from restarting the daemon
  the TUI's turns run on.

Each of these was right for the direction in force when it was written: the
containerized fleet server (August), the local daemon (October), and then the
Flutter Studio and federation (November). None of the earlier ones was retired
when a later one arrived.

## Decisions

### The daemon

**D1. One daemon.** `--profile` goes. The daemon is today's local profile.
- Its interface is the unix socket (`ListenLocal`, `web/bridge/localsocket.go`),
  peer-checked by user ID. Every route that exists today stays reachable over
  it.
- Pinned projects, the idle sweep, the restart sweep, the project environment
  and the OAuth refresh helper always run. Today they run only under
  `cfg.profile == "local"` (`main.go:779`, `:853`, `:959`).
- The server profile's working-directory project goes (`main.go:260`). A
  daemon registers no project it was not given.

**D2. No shared token.** The daemon holds no bearer token. This removes
`--token`, `--token-file`, `studio.token`, `bearerAuth`
(`web/bridge/auth.go`) and the network agent attach (`networkAgentAttach`,
`attach.go:455`).

Four credentials stay, because each one names a caller or a link rather than
opening the daemon:
- per-client MCP credentials and their grants (`mcp.go`, `mcp_factory_auth.go`);
- webhook HMAC signatures (`hooks.go`);
- status-link tokens (`status.go`);
- preview tokens (`preview.go`).

**D3. Nothing listens on a network interface.** The daemon binds three things:
its socket, the web listener (D10) when it is set, and the preview listener
(D11). The web and preview listeners each take a unix socket path or a
loopback address. Any other address is refused at start, naming the setting.
`--tls-cert` and `--tls-key` go: TLS belongs to whatever publishes a listener.

**D4. Containers are a per-agent sandbox.** The `hostOnly` switch goes
(`fleet.go:156`, `:870`). Planning plan 1 settled the details, on 2026-11-20:
- **A workspace is the only trigger.** An agent runs in a container when it
  has a workspace: its own, or its project's default
  (`ProjectSettings.Workspace`, applied at `fleet.go:1397`). A
  `devcontainer.json` becomes a workspace through Settings › Environments.
- **Everything else runs on the host.** Every other agent, and the control
  agent, is a host process, whether or not a container runtime is installed.
  It never probes for one. A plain local agent runs on its project's child
  (P4 D10).
- **Container agents' files.** They mount the daemon's state directory, and
  the user's config directory read-only as their config home (A5).
- **Rootless runtimes only, on Linux.** A daemon whose state is on the host
  uses a rootless runtime there, and refuses a rootful one with a message that
  names the fix. Docker Desktop on macOS is accepted.
- **Egress.** The egress proxy is a sidecar for container agents only, off
  unless `[daemon] egress` turns it on. Its in-process mode for host agents
  goes.
- **Secrets and limits.** `--secrets local|openbao`, the quotas, the agent
  image and the resource caps work on any daemon. The local profile's
  refusals of `--egress on` and of non-`env` secrets go (`main.go:333-339`).

**D5. The containerized daemon is retired.** These go: `compose.yaml`,
`build/Dockerfile.bridge`, the `webbridge` images in `.goreleaser.yaml`, and
the `--state-volume` and `--project-mount` flags.
- A server is a Linux machine where the user ran `marshal daemon install`,
  with linger on, as on a laptop.
- The agent image and its Dockerfiles stay.
- Container agents started by a daemon on the host mount its state directory
  by path. Assumption A5 names what must be checked first.
- Planning plan 5 settled the rest. Named-volume state and project mounts are
  deleted, and `NewFleet` loses both parameters. The `marshal-` volume prefix
  stays reserved for workspaces. `--state-volume` and `--project-mount` are
  refused, while `--profile` and plan 3's retired listener flags stay accepted,
  because installed units carry them. `docs/DEPLOYMENT.md` becomes "Running
  Marshal on a server".

### Hosts

**D6. A remote host is an SSH destination.** `fleet.toml`'s server tables
become host tables. `[hosts.<name>]` takes:
- `ssh`: a destination exactly as `ssh` takes it, an alias from
  `~/.ssh/config` included;
- optionally `marshal`: the remote binary's path, for a remote whose
  non-interactive shell does not have it on `PATH`;
- `notify`, as today.

Loading refuses a `[servers.*]` table, and any `url`, `ca_file`, `token_cmd` or
`relay` key, with a message that names the new shape. `[local] notify` stays.
The file keeps its privacy check, though it now holds no secret.
`internal/fleetprofile` still owns the file.

**D7. The daemon keeps one SSH tunnel per host.** A helper,
`marshal daemon tunnels`, runs beside the bridge the way the OAuth refresh
helper does (`bridge.Helper`, `main.go:983`; P4 D12). The bridge starts it when
given `--tunnels`, which the installed unit always writes. The helper re-reads
`fleet.toml` every 2 s. For each host it:
1. Runs `ssh <dest> <marshal> daemon status --json` and reads the remote
   socket path from the document.
2. Keeps one `ssh -N` process that forwards `<daemon dir>/hosts/<name>.sock`
   to that remote path. It runs with `BatchMode`, `ExitOnForwardFailure`,
   `StreamLocalBindUnlink`, the default `StreamLocalBindMask` (`0177`), a
   keep-alive, and a connection of its own (`ControlMaster=no`,
   `ControlPath=none`), so the helper supervises exactly one process.
3. Restarts a process that exits, with a backoff that starts at 1 s and
   doubles to 30 s.
4. Reports the host's state to the daemon with a socket-only
   `PUT /api/daemon/hosts`: `connecting` before the first outcome, then
   `live`, or `down` with ssh's last line of stderr. A retry stays `down`
   until it is live.

On the far end, the remote daemon sees the SSH login user as its peer. That
daemon belongs to that user, so the caller is its owner, and the whole socket
surface works on a remote host as it does locally: attach, pin and restart
included.

**D8. Hosts are added and removed with `marshal fleet`.**
- `marshal fleet add NAME DEST [--marshal PATH] [--no-notify]` and
  `marshal fleet remove NAME` edit `fleet.toml` under today's rules (atomic,
  `0600`, comments kept; P8 D6). The helper picks the change up on its next
  read of the file, so neither command asks it to reload, and a hand edit is
  picked up too. Each command waits up to 5 s for the daemon's report to
  follow, so a client that rediscovers next sees the change.
- `marshal fleet login`, `marshal fleet token` and `marshal fleet servers`
  go. `marshal daemon status` lists the hosts.
- `marshal daemon token` and `marshal daemon join-token` go in plan 3, with the
  listener's token and the join they serve.
- `marshal fleet --server NAME` becomes `marshal fleet --host NAME`.

**D9. Clients reach every host through their own daemon.**
- `GET /api/daemon` (`DaemonInfo`) gains `hosts`: name, state, socket,
  `lastError` and `since`. `marshal daemon status --json` prints them.
- Native clients dial a host's tunnel socket exactly as they dial the local
  socket. The native clients are the fleet screen, `marshal attach` and the
  native Studio. Native discovery is the local daemon, then each host in its
  status document.
- No HTTPS transport and no token resolver remain on the client side.
- The browser reaches other hosts through D10's forward. Hosts are still
  merged only in the client (P7).

### The web Studio

**D10. The web listener is opt-in and sits behind the operator's proxy.**
`[daemon] web_listen` is unset by default. When set, it is one of:
- `unix`: `<daemon dir>/web.sock`, mode `0600`;
- an absolute socket path, for a proxy that needs the socket somewhere else;
- a loopback address, for a proxy that cannot dial a unix socket.

`[daemon] web_group` makes the socket `0660` in that group, for a proxy that
runs as another user. It needs `web_listen` to be an absolute socket path: the
daemon directory is `0700`, so the group could not reach `web.sock` there.
`[daemon] web_url`, the proxy's public address, is also the daemon's public
URL (`--public-url`), so notification links, approval links and the MCP
origin check name it.

What the web listener serves:
- the Studio's files (D12);
- the routes the network listener serves today (`ServeHTTP`'s set,
  `web/bridge/http.go:174`), without a bearer check;
- `/mcp`, `/hooks/` and `/status/`;
- `GET /api/hosts` and the `/api/hosts/<name>/<rest>` forward.

The socket-only routes stay on the socket: project attach, pin, restart and
enrollment (`SocketHandler`, `http.go:205`).

The forward:
- passes the request to that host's tunnel socket unchanged;
- is `hostrelay.go`'s reverse proxy with the joined-connection pool replaced
  by a unix dial;
- carries the web listener's route set, not the socket's, so a socket-only
  route answers 404 through it;
- is listed by `GET /api/hosts`, which replaces `/api/relay/hosts`.

**D11. Previews keep their own listener and tokens.** The preview listener
opens on a free loopback port whenever the daemon runs workspace agents. It no
longer depends on another listener being open (today it follows `--addr`,
`main.go:1029`). It keeps its own origin and per-preview tokens. A proxy that
publishes the web listener publishes previews on a second hostname. For that,
`[daemon] preview_listen` fixes the listener's loopback port, and `[daemon]
preview_url` is the public origin preview links name.

**D12. The daemon embeds no Studio.** `//go:embed static` goes
(`assets.go:28`).
- The web listener serves the Studio's files from a directory: the
  `studio-web` release asset, unpacked beside the binaries. The app bundle
  carries that directory too.
- `webbridge` builds with Go alone.
- A Studio change is a rebuild of that directory. It no longer relinks or
  restarts the daemon, so the development box needs one daemon, not
  `dev-studio.sh`'s second one.
- The web build, and the check that it fetches nothing from a CDN, move to
  that artifact's own CI job.
- Planning plan 4 settled where the directory is. It is `share/marshal/studio`
  beside the binaries (the release archive, a checkout) or above them
  (`<prefix>/bin` with `<prefix>/share`: Homebrew, `install.sh`, both app
  bundles). `webbridge` finds it from its own executable, and
  `--studio-dir` overrides that. A daemon with no files serves a 503 page that
  names where it looked, and runs on.

**D13. Marshal authenticates no browser.** The operator's proxy does it:
Tailscale Serve, Cloudflare Access, Caddy with an auth module, oauth2-proxy,
or similar.
- `/hooks/`, `/status/` and `/mcp` each carry their own credential (D2), so a
  proxy should let them through without its login.
- A web listener on a loopback address is reachable by every account on the
  machine. `marshal daemon status` says so beside the address.
- The Studio never receives a 401 from Marshal. Its `unauthorized` state
  (`HostReach.unauthorized`, `StateMessage.unauthorized`) now means the proxy
  refused the request, and reads "signed out: reload to sign in again". A
  proxy's redirect to its login page reads the same way.
- Marshal checks no browser's identity, but its web listener refuses what a
  browser sends on another site's behalf. The bearer token used to stop that,
  so plan 3 adds a guard before the token goes. It refuses:
  - a `Host` other than the loopback names and `web_url`'s host (DNS
    rebinding);
  - a state-changing request whose `Sec-Fetch-Site` or `Origin` is another
    site's (CSRF);
  - an `/api/` body that is not JSON.

  `/hooks/`, `/status/` and `/mcp` carry their own credentials and are
  exempt. The socket is unchanged. Added on 2026-11-20, after plan 3's
  approval, when planning the browser client found the gap.

**D14. `marshal studio` opens the app.**
- It opens the native Studio when one is installed.
- Otherwise it opens `[daemon] web_url` when that is set: the proxy's public
  address.
- Otherwise it says that neither is set up.
- The single-use sign-in code, `/api/studio/code`, `/api/studio/session` and
  `/api/studio/hello` go.

**D15. Settings › Hosts adds a host by name and destination.**
- In a native build, "Add a host" asks for a name and an SSH destination, and
  runs D8.
- A host's detail shows its state, its last error and **Remove**.
- There is no Sign in, no Sign out, no CA file and no token field.
- In a browser the section is read-only, with the note "add hosts with
  `marshal fleet add` on the machine this daemon runs on". Plan 7 reverses
  this (open question 4). There, both builds add and remove hosts through
  their home daemon's API: `POST /api/hosts` and `DELETE /api/hosts/<name>`
  run `marshal fleet add` and `remove` on the daemon's machine.

## Assumptions

- **A1. OpenSSH can forward a unix socket.** OpenSSH 6.7 and later forwards
  them on both ends, and `AllowStreamLocalForwarding` is `yes` by default in
  sshd. A server that turns it off cannot be a host.
- **A2. The daemon's service can authenticate SSH without a prompt.** That
  means keys in an agent the service can reach, or a key with no passphrase,
  and host keys already in `known_hosts`. `BatchMode` turns a prompt into a
  failure with ssh's own message, which D7 reports as the host's last error.
  On macOS, launchd gives user agents `SSH_AUTH_SOCK`. Whether a systemd user
  service has it depends on the session, and must be checked (open question
  3).
- **A3. A remote `marshal` is callable over non-interactive SSH,** or its path
  is in the host table.
- **A4. Caddy and nginx can proxy to a unix socket.** For a proxy that cannot,
  use a loopback web listener (D10) and accept D13's caveat.
- **A5. Container agents can run from a daemon on the host.** This was
  checked while planning plan 1, and it does not hold today:
  - Container agents mount their work tree, socket directory and homes as
    subpaths of a named volume (`mounts.go`; `container.go:229–258`). The
    daemon dials the socket under its own state directory.
  - The two views meet only when that directory is the volume's mountpoint,
    which `scripts/dev-studio.sh:196–214` arranges by hand.
  - Plan 1 binds the state directory instead, and keeps volume mode for a
    containerized bridge until D5.

## Non-goals

- Authenticating browsers inside Marshal: passwords, OIDC, sessions or
  cookies.
- More than one user on a daemon. `OwnerID` stays in the data model.
- A daemon listening on a network interface, and TLS inside the binary.
- Merging hosts on a daemon, or chaining forwards.
- Reaching a host that offers no SSH, such as a sandbox reachable only over
  HTTPS. This is what the design gives up.
- Windows.

## Open questions

1. **Previews of a remote host's agents.** Either D7 forwards the remote
   preview port over the same SSH connection, or previews go through D10's
   forward. Recommendation: a second forward in the same `ssh` process, so a
   preview keeps an origin of its own. Plan 3 left it out, and plan 6 takes
   the recommendation:
   - the tunnel helper forwards the remote's preview listener to a free
     loopback port and reports it as `DaemonHost.previewPort`;
   - a preview request names the caller's way in with
     `X-Marshal-Preview-Origin`, set by the native Studio for a tunnel it
     dials and by the forward for a host it forwards to;
   - a browser that reaches the daemon from another machine still cannot open
     a forwarded host's preview.
2. **Local MCP clients without the loopback listener.** Today a client on the
   laptop reaches `/mcp` over `127.0.0.1:7788`. Recommendation: add a stdio
   command, `marshal mcp --credential-file PATH`, that relays JSON-RPC to
   `/mcp` over the socket with the client's own credential. That keeps D2's
   per-client grants and avoids a listener. The alternative is to tell those
   clients to set `web_listen` to a loopback address. Accepted on 2026-11-20:
   plan 3 adds `marshal mcp`.
3. **SSH agent reachability for a systemd user service.** If `SSH_AUTH_SOCK`
   is not in the user manager's environment, either `marshal daemon install`
   imports it or the documentation says to. Resolved while planning plan 2:
   the documentation says to. The unit carries no `SSH_AUTH_SOCK`, because
   the path is per login on Linux and per boot on macOS. A host that cannot
   authenticate is `down` with ssh's own message.
4. **Adding a host from a browser.** It would need a route on the web listener
   that edits `fleet.toml` on the daemon's machine. Recommendation: no;
   D15 keeps the browser read-only. Reversed on 2026-11-20 at the user's
   request, in plan 7:
   - the daemon runs `marshal fleet add` and `remove` for the routes;
   - the native app uses the same routes, and runs no `marshal fleet`;
   - the tunnel's `ssh` refuses agent and X11 forwarding, local commands and
     a configured remote command, so a host added this way gets nothing but
     a tunnel.

## Plans

Prose plans, written one at a time. Each one deletes what it replaces in the
same change: [`inventory.md`](inventory.md) lists the files.

1. **One daemon** (D1, D4, A5):
   [plans/2026-11-20-od-1-one-daemon-plan.md](plans/2026-11-20-od-1-one-daemon-plan.md).
   It removes `--profile` and `hostOnly`, and makes container agents run from
   a daemon on the host. It runs first because a server machine needs it
   before it can stop running the containerized bridge.
2. **Hosts over SSH** (D6–D9, D15):
   [plans/2026-11-20-od-2-hosts-over-ssh-plan.md](plans/2026-11-20-od-2-hosts-over-ssh-plan.md).
   Add the tunnel helper, `DaemonInfo.hosts`, `marshal fleet --host`, native
   discovery and Settings › Hosts. Delete the HTTPS transports, `fleet
   login`/`token`/`servers`, and `fleetprofile`'s token and TLS code. The
   browser keeps its relayed hosts until plan 3.
3. **The web listener** (D2, D3, D10, D11, D13, D14):
   [plans/2026-11-20-od-3-web-listener-plan.md](plans/2026-11-20-od-3-web-listener-plan.md).
   Add the opt-in listener, the forward and `marshal mcp`. Delete the TCP
   listener, `bearerAuth`, the sign-in handover, the relay, the join, the
   browser's token stores and the fleet client's rejected state. Move the e2e
   suite onto the web listener. It depends on plan 2, because the forward
   dials plan 2's tunnel sockets.
4. **The Studio as files** (D12), split out of plan 3 while planning it:
   [plans/2026-11-20-od-4-studio-as-files-plan.md](plans/2026-11-20-od-4-studio-as-files-plan.md).
   Delete the embed, serve the Studio from a directory beside the binaries,
   ship that directory in the release archive, Homebrew, `install.sh` and both
   app bundles, give its web build and CDN check a CI job of their own, and
   delete `scripts/dev-studio.sh`.
5. **Retire the containerized daemon** (D5) and sweep the docs:
   [plans/2026-11-20-od-5-retire-the-container-daemon-plan.md](plans/2026-11-20-od-5-retire-the-container-daemon-plan.md).
   The sweep covers `AGENTS.md`,
   `cmd/webbridge/README.md`, `scripts/e2e/README.md`, P4, P5, P8,
   distribution and F1.
6. **Previews of a remote host's agents** (open question 1):
   [plans/2026-11-20-od-6-remote-previews-plan.md](plans/2026-11-20-od-6-remote-previews-plan.md).
   Forward the remote preview listener over the tunnel, and let the caller
   name its way in. It needs plans 2 and 3, and not 4 or 5.
7. **Adding and removing hosts from a browser** (D15, open question 4):
   [plans/2026-11-20-od-7-hosts-from-a-browser-plan.md](plans/2026-11-20-od-7-hosts-from-a-browser-plan.md).
   `POST /api/hosts` and `DELETE /api/hosts/<name>` run `marshal fleet` on
   the daemon's machine, both builds use them, and the tunnel's `ssh` is
   hardened. It needs plans 2 and 3 (with plan 3's guard).
