# Running the daemon and studio from a working tree

Marshal has two long-running services, and on a development machine you want
both running *this checkout's* build instead of a release. This is how to set
that up, the day-to-day loop, and how to rebuild the setup from nothing.

| Service | Unit | Address |
|---|---|---|
| `marshal-daemon` | `~/.config/systemd/user/marshal-daemon.service` (Linux) or `~/Library/LaunchAgents/dev.marshal.daemon.plist` (macOS) | unix socket |
| `marshal-studio` | `~/.config/systemd/user/marshal-studio.service` | `http://127.0.0.1:7700` |

Both run the same daemon: `webbridge` has one shape, and `--profile` is retired.
The studio is a second *process* for one reason only: a Studio rebuild rewrites
the embedded SPA, so `webbridge` has to be relinked, and restarting the studio's
unit rather than the TUI's daemon keeps a rebuild from ending the TUI's running
turns. Both serve the browser the same way — the web listener.

Nothing on that listener authenticates a browser: Marshal holds no bearer
secret, and the operator's proxy is the login (one-daemon D13). A listener on a
loopback address is reachable by every account on the machine, which is why
`marshal daemon status` says so beside the address.

Two helpers own them: `scripts/dev-daemon.sh` and `scripts/dev-studio.sh`.
Both build into the repository root and restart the service; neither watches
the tree, because a restart ends running turns. Run them between turns.

## Why rebuilding in place is enough

`marshal daemon install` resolves the absolute paths of `marshal` and the
`webbridge` beside it **once** and writes those paths into the unit. The
service manager re-executes whatever sits at those paths on every start, so
the unit stays valid across rebuilds — no release, no version bump. The
version string comes from `buildinfo`: `dev+<12 hex of the commit>`, with
`+dirty` for a modified tree.

One consequence: two `+dirty` builds of the same commit share a version
string. The service's start time, which both scripts print, is what proves a
restart actually happened.

## One-time setup

```bash
cd ~/projects/marshal

# The daemon needs webbridge beside marshal; `install` resolves both paths.
CGO_ENABLED=1 go build -o . ./cmd/marshal ./cmd/webbridge
./marshal daemon install
./marshal daemon status

# The studio: SPA + binaries + agent image + unit.
./scripts/dev-studio.sh --install --dev-image
```

`-o .` is load-bearing: `go build ./cmd/marshal ./cmd/webbridge` without it
only type-checks the two mains and writes nothing. `CGO_ENABLED=1` is required
because tree-sitter (`internal/repo/symbols.go`) links a C library.

`./scripts/dev-daemon.sh` also auto-installs on its first run, so the explicit
`daemon install` is optional; it is shown here because it is what the daemon
script wraps.

## The loop

```bash
./scripts/dev-daemon.sh            # rebuild, restart the daemon, print status
./scripts/dev-studio.sh --dev-image  # SPA + binaries + agent image + restart
```

A Go change needs `--dev-image` for agents spawned by the studio, because the
agent's `marshal` comes from the **container image**, not from the bridge's
`--marshal-bin` (that only reaches host-process agents; studio agents have a
workspace, so they containerise when a rootless runtime is present). A change
under `app/` needs the SPA rebuilt with `flutter build web`, which is part of the
studio's default run: `//go:embed` bakes `web/bridge/static/` into `webbridge`,
so the binary has to be relinked as well as the SPA rewritten.

### Artifacts

| Artifact | Produced by | Refreshed when |
|---|---|---|
| `./marshal`, `./webbridge` | either script | Go code changes |
| `web/bridge/static/` (embedded SPA) | `dev-studio.sh` (`flutter build web`) | `app/` changes |
| `ghcr.io/marshal/agent:latest` | `dev-studio.sh --dev-image` | Go code changes, for studio-spawned agents |

`web/bridge/static/index.html` is tracked as a placeholder so a Go-only
checkout still serves a landing page, and the SPA build overwrites it: expect
that file to show as modified after every run, and do not commit it. CI builds
the SPA the same way before it builds `webbridge`.

### Common invocations

```bash
# daemon
./scripts/dev-daemon.sh --no-build       # restart the binaries already on disk
./scripts/dev-daemon.sh --no-restart     # build only; the daemon keeps its old binary
./scripts/dev-daemon.sh --install        # rewrite the unit (repo moved, unit settings changed)
./scripts/dev-daemon.sh --status         # query only

# studio
./scripts/dev-studio.sh --no-spa         # Go only, keep the embedded SPA
./scripts/dev-studio.sh --no-build       # restart only
./scripts/dev-studio.sh --no-restart     # build (and optionally --dev-image) only
./scripts/dev-studio.sh --install        # rewrite the unit (repo moved, unit settings changed)
./scripts/dev-studio.sh --status         # query only
./scripts/dev-studio.sh --agent-image    # slow: compile marshal inside the image (release-shaped)
```

`--status` is a query: it never builds, restarts, or rewrites anything.

Editing the Studio has no hot-reload mode. F8's cutover removed the Svelte
studio together with the Vite dev server and the dev proxy that let its page
reach the bridge on `:7700`. The Flutter client is same-origin only
(`WebTransport` with no base, `app/lib/src/client/`), so a page served from
another port would send every `/api` request to that port instead of to the
bridge. The loop is: edit, run `./scripts/dev-studio.sh`, reload the tab.

## The agent image

A daemon runs an agent in a container when the agent has a workspace, mounting
its state from the daemon's state directory; on Linux that needs rootless
Podman or Docker. Without an agent image the studio serves, but every spawn
fails, so the image is part of a usable setup.

`--dev-image` builds `build/Dockerfile.dev`, which copies the host-built
`./marshal` into an image that adds only `git` and `ca-certificates`. That is
seconds instead of a module compile. Two constraints:

- **The base image must match the host's libc.** marshal links tree-sitter
  through cgo, so the copied binary is dynamically linked against the machine
  that built it and will not start on an older glibc (`GLIBC_2.43 not found`).
  The script derives the base from `/etc/os-release` (fedora, debian, ubuntu);
  override with `--build-arg BASE_IMAGE=<image>` if the guess is wrong.
- **It is dev-only.** Do not publish it. Releases use `build/Dockerfile`,
  which compiles marshal on the release base — that is `--agent-image`, and it
  is slow.

Both tag `ghcr.io/marshal/agent:latest`, which is what a source build resolves
for `defaultAgentImageFor`, so the studio's default project settings pick the
image up with no config edit. The dev image carries a `marshal.dev.bin-mtime`
label so `--status` can report when it predates `./marshal`.

## The studio's state

The studio holds no secret: the web listener it serves on has none, and the
proxy in front of it authenticates the browser (D13).

| Path | Contents |
|---|---|
| `$data_dir/studio/fleet.json` | the studio's own fleet |
| `$data_dir/studio/webbridge.log` | `append:` stdout/stderr |
| `$data_dir/studio/state` | `--state-dir` |

`$data_dir` is `MARSHAL_DATA_DIR`, else `$XDG_DATA_HOME/marshal`, else
`~/.local/share/marshal`.

The state dir is a host directory, `$data_dir/studio/state`: agent containers
bind subpaths of it (`work/<agent>`, `sockets/<agent>`, the shared home, the
egress sidecar) from the daemon's state directory, and the user's config home is
bound read-only beside it, so the bridge and its containers resolve the same
files. On Linux a container needs those binds, so a rootless Podman or Docker is
required and a rootful one is refused. `--status` prints the directory.

A directory written by an older run may still hold `token` and `webbridge.env`;
neither is read any more. Delete them when you like.

## Reaching a host from the TUI fleet screen

The studio is not something the fleet screen reaches: the profile shape that
pointed the screen at one is retired, and what it dials besides the local
daemon is a **host** — a machine whose daemon it reaches over an SSH tunnel.
The SSH prerequisites are under "Reaching another machine" below.

```bash
marshal fleet add box you@box     # writes [hosts.box] ssh = "you@box"
marshal fleet --host box          # the fleet screen, on box's daemon
marshal daemon status             # host box (you@box): live
```

`fleet add` writes the table into `~/.config/marshal/fleet.toml` — the file
must be `0600` and owned by you, and holds no token — and waits for the local
daemon's tunnel helper to report the host. That helper runs because
`marshal daemon install` writes `--tunnels` into the unit, so a daemon
installed before hosts existed needs `marshal daemon install` again.
`marshal daemon status` then prints one line per host: `host box (you@box):
live`, or `down` with the ssh error that stopped it.

The studio itself is a browser surface at `http://127.0.0.1:7700` (the next
section reaches it from another machine). A browser that lists the SSH hosts
the daemon holds does not exist yet: it arrives with plan 3's forward for
hosts.

## Reaching studio from another machine

The unit binds `127.0.0.1:7700`, so on a headless box studio is not reachable
from the LAN or a tailnet until something forwards to it. Forward; do not
rebind.

```bash
tailscale serve --bg --https=8443 7700     # https://<host>.<tailnet>.ts.net:8443/
tailscale serve status                     # shows the 8443 entry beside whatever owns 443
```

Port 443 may already belong to another service on the same host (this box
serves `:8200` at the root), so pick a free `--https=` port. Tailscale issues
and renews the certificate, so there is no certificate to manage, and the entry
is tailnet-only — no LAN exposure, no public DNS record. Remove just it with
`tailscale serve --https=8443 off`; `tailscale serve reset` clears every entry
on the host, including ones you did not create.

The proxy is now the only gate (D13). Marshal holds no bearer secret and checks
no browser's identity: tailnet membership is the login, and a request that
arrives at `:7700` is answered. What the listener does check for itself is what
a browser sends on another site's behalf — the `Host` it is addressed to (only
`localhost`, `127.0.0.1`, `::1` and `[daemon] web_url`'s host), a
state-changing request whose `Sec-Fetch-Site` or `Origin` is another site's,
and an `/api/` body that is not JSON (`web/bridge/webguard.go`). So `web_url`
has to name the address the proxy publishes, or the forwarded browser is
refused before any route sees it.

`tailscale serve` is tailnet-only, so it is not the LAN answer: the LAN needs
its own TLS front — any reverse proxy with a certificate pointed at
`127.0.0.1:7700` — with that same gate behind it. The listener refuses a
routable bind in any case, so there is no plain-HTTP shortcut to prefer over the
tailnet.

Which origin is actually live is not something you should have to guess:
`scripts/dev-studio.sh --status` prints a `remote:` line for every
`tailscale serve` entry that proxies to the studio's loopback port. Use it —
on a box where 443 already belongs to another service, the bare tailnet
hostname is that other service (`401 Unauthorized` here), not studio.

Two things do not travel through the forward:

- **Preview URLs** are gated to localhost hostnames (`isLocalhostName` in
  `web/bridge/preview.go`), so previews opened from the tailnet hostname are
  refused. Browse those from the box itself.
- **The TUI's fleet screen** does not use the forward at all: `marshal fleet`
  dials a daemon's socket, not a URL, and a remote daemon is a host reached
  over SSH (`marshal fleet --host NAME`, next section). What the forward gives
  you is the studio's own page and `/api` at the forwarded origin.

## Pointing an MCP client at the daemon

An MCP client talks to the daemon as one more client with its own grant, not as
the owner: create one in **Settings › Access** in the Studio, then save the
credential it shows once to a file only you can read, because it is not
displayed again.

```bash
install -m 0600 /dev/null ~/.config/marshal/mcp-client.cred
# paste the credential into the file (Settings › Access shows it once)
```

The client speaks stdio, so its command is `marshal` with `mcp` and that file,
and nothing listens anywhere:

```jsonc
// e.g. an MCP client's server entry
{ "command": "/home/you/.local/bin/marshal",
  "args": ["mcp", "--credential-file", "/home/you/.config/marshal/mcp-client.cred"] }
```

`marshal mcp` relays the client's JSON-RPC to `/mcp` over the daemon's socket.
That keeps per-client grants and needs no web listener, so it works on a box
whose daemon listens only on its socket. The `/mcp` route carries its own
client credential either way, which is why the web listener's guard leaves it
alone (D13).

## Reaching another machine

`marshal fleet --host NAME` shows another machine's daemon in the fleet screen,
reached over SSH (one-daemon D6–D9). On the laptop:

```bash
marshal fleet add box you@box        # writes [hosts.box] into fleet.toml
marshal daemon install               # writes --tunnels into the unit
marshal daemon status                # lists the tunnel: box, its socket, its state
```

The remote needs `marshal daemon install` too, and an sshd that allows
stream-local forwarding (`AllowStreamLocalForwarding`, A1).

The daemon's ssh runs with `BatchMode`, so it needs a key it can use without a
prompt (A2). On Linux, the systemd user manager must see the agent: run
`systemctl --user import-environment SSH_AUTH_SOCK` once per login, run the
agent as a user unit, or set `IdentityAgent` in `~/.ssh/config`. On macOS,
launchd gives the daemon the login's agent.

## Restart cost

- The daemon's restart ends running turns. The TUI is told
  `marshal/restarting`, and its session screen reconnects for up to a minute
  keeping the draft. Project children are stopped with the daemon (so a stale
  child cannot outlive it) and respawn from `--marshal-bin` on the next attach.
  Pinned projects stay pinned.
- The studio's restart ends its agents. It is a separate unit precisely so this
  does not also end the TUI's turns.

Neither service restarts itself when the tree changes. The idle-gated design
in `docs/daemon/p6-daemon-lifecycle.md` covers that, but the detection half is
not in the tree yet; until it is, an automatic trigger would kill turns
mid-flight.

## Recreating this setup from a bare checkout

1. **Prerequisites.** Go 1.26+ and a C toolchain (cgo). Flutter 3.50+ for the
   Studio's web build (`flutter build web` from `app/`; there is no Node step
   any more). rootless podman or docker (Docker Desktop on macOS) for
   containerized agents. systemd (Linux) or
   launchd (macOS) for the daemon; the studio helper is Linux-only (it writes a
   systemd user unit — elsewhere run `webbridge` yourself).
2. **Build and install the daemon.**

   ```bash
   CGO_ENABLED=1 go build -o . ./cmd/marshal ./cmd/webbridge
   ./marshal daemon install
   ./scripts/dev-daemon.sh --status
   ```

   On Linux `install` enables lingering so the daemon survives logout; check
   with `loginctl show-user "$USER" -p Linger`.
3. **Build and install the studio.**

   ```bash
   ./scripts/dev-studio.sh --install --dev-image
   ./scripts/dev-studio.sh --status
   ```

   The first run builds the SPA with `flutter build web`, builds both binaries,
   writes the unit, enables and starts the unit. `--install` implies a restart;
   no separate `start` is needed.
4. **Confirm both services**, and add a host if you have another machine to
   reach (optional: with no other machine, `fleet.toml` is never needed):

   ```bash
   ./scripts/dev-daemon.sh --status
   ./scripts/dev-studio.sh --status
   curl -fsS http://127.0.0.1:7700/api/projects   # no credential: the listener has none

   ./marshal fleet add box you@box  # optional: writes [hosts.box]
   ./marshal daemon status          # host box (you@box): live
   ```

5. **Verify a rebuild reaches the services.** Run the loop once and check the
   start time moves in `--status` output.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `dev-daemon.sh` exits non-zero after "did not answer" | It tails `$data_dir/daemon/webbridge.log`; also `systemctl --user status marshal-daemon`. Usually a missing `webbridge` beside `marshal`. |
| Studio answers `401` | Not Marshal: the listener holds no secret, so a `401` comes from the proxy in front of it. Sign in there again (D13). |
| Studio serves but spawns fail | No `ghcr.io/marshal/agent:latest`; run `--dev-image`. `--status` says when it is missing or stale. Or the runtime is rootful: agents need rootless podman or docker on Linux, and a rootful one answers 501. |
| Agent exits with `GLIBC_2.x not found` | The dev image's base is older than the host's libc; rebuild with `--build-arg BASE_IMAGE=<host-matching image>`. |
| `--install` refuses to write a unit | `MARSHAL_CONFIG_DIR`/`MARSHAL_DATA_DIR`/`XDG_*` point outside `$HOME`. The unit bakes those paths, so it would outlive a temp dir. Unset them, or pass `--force` deliberately. |
| Version line unchanged after a rebuild | Two `+dirty` builds of one commit share a version; compare the start time. |
| `--dev-image` says the build context is wrong | The script copies only `./marshal` into a temp context on purpose: the repo root would hand `app/build` and `.marshal/worktrees` to the runtime. |
| `dev-studio.sh` says flutter is missing | The SPA build is `flutter build web` from `app/` and needs Flutter 3.50+; the Node toolchain left with F8. Install Flutter, or pass `--no-spa` to rebuild Go only. |
