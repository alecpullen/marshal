# Running the daemon and studio from a working tree

Marshal has two long-running services, and on a development machine you want
both running *this checkout's* build instead of a release. This is how to set
that up, the day-to-day loop, and how to rebuild the setup from nothing.

| Service | Unit | Address |
|---|---|---|
| `marshal-daemon` | `~/.config/systemd/user/marshal-daemon.service` (Linux) or `~/Library/LaunchAgents/dev.marshal.daemon.plist` (macOS) | unix socket |
| `marshal-studio` | `~/.config/systemd/user/marshal-studio.service` | `http://127.0.0.1:7700` |

Both run the same daemon: `webbridge` has one shape, and `--profile` is retired.
The studio is a second *process* only because a browser needs a loopback address
of its own.

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

# The studio: SPA + binaries + agent image + unit + token.
./scripts/dev-studio.sh --install --dev-image --token
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
./scripts/dev-studio.sh --install        # rewrite the unit and rotate the token
./scripts/dev-studio.sh --status         # query only
./scripts/dev-studio.sh --token          # print the bearer token
./scripts/dev-studio.sh --agent-image    # slow: compile marshal inside the image (release-shaped)
```

`--status` and `--token` are queries: they never build, restart, or rewrite
anything.

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

## The studio's token and state

The SPA authenticates `/api` with a bearer token held in `localStorage` and
asks for it once per browser. `--token` prints it.

| Path | Contents |
|---|---|
| `$data_dir/studio/token` | the bearer token |
| `$data_dir/studio/webbridge.env` | `WEBBRIDGE_TOKEN=…`, mode `0600`, read by the unit |
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

`--install` rotates the token and signs every browser out; plain restarts keep
it, so the browser stays signed in.

## Reaching studio from the TUI fleet screen

Studio is a separate fleet from the local daemon (separate units, state, and
restarts). To drive it from the TUI, give it a profile in
`~/.config/marshal/fleet.toml` — the file must be `0600` and owned by you, and
holds no token:

```toml
[servers.studio]
url = "http://127.0.0.1:7700"
token_cmd = "cat \"${MARSHAL_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/marshal}/studio/token\""
notify = true
```

The `token_cmd` mirrors the helper's own data-dir resolution, so it survives
`MARSHAL_DATA_DIR`/`XDG_DATA_HOME` moving; the shell expansion is done by
`/bin/sh -c` at connect time. Then:

```bash
marshal fleet --server studio
```

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
and renews the certificate, so there is no `--tls-cert` to manage, and the
entry is tailnet-only — no LAN exposure, no public DNS record. Remove just it
with `tailscale serve --https=8443 off`; `tailscale serve reset` clears every
entry on the host, including ones you did not create.

Forwarding is also what keeps **agent sessions** usable. The agent attach
refuses a request that is neither TLS nor on a loopback connection, answering
403 `the agent attach needs TLS` (`overTLSOrLoopback`, `web/bridge/attach.go`).
Traffic forwarded by `tailscaled` reaches webbridge on loopback, so the
transport rule passes and sessions open normally from a remote browser.
Verified: `POST /api/agents/<id>/attach` through the forwarded port reaches
`attachAgent` (it rejects the missing upgrade header) instead of 403.

Binding the bridge itself to a routable address instead (`--addr 0.0.0.0:7700`)
is not equivalent: plain HTTP off loopback gets the SPA and `/api` but every
attach is refused, so studio becomes read-only-ish — you can spawn and watch
the fleet, but you cannot open a session. If you do need a direct bind, give it
TLS with `--tls-cert`/`--tls-key` (both, or it refuses to start), and remember
the bearer token then travels in an `Authorization` header over whatever
network that is.

`tailscale serve` is tailnet-only, so it is not the LAN answer: the LAN needs
its own TLS front on a LAN address — any reverse proxy pointed at
`127.0.0.1:7700` whose origin has a certificate, or a direct
`--tls-cert`/`--tls-key` bind — and the plain-HTTP rebind above is never it.
Prefer the tailnet: it reaches the same studio with a valid certificate and no
LAN exposure.

Which origin is actually live is not something you should have to guess:
`scripts/dev-studio.sh --status` prints a `remote:` line for every
`tailscale serve` entry that proxies to the studio's loopback port. Use it —
on a box where 443 already belongs to another service, the bare tailnet
hostname is that other service (`401 Unauthorized` here), not studio.

Two things do not travel through the forward:

- **Preview URLs** are gated to localhost hostnames (`isLocalhostName` in
  `web/bridge/preview.go`), so previews opened from the tailnet hostname are
  refused. Browse those from the box itself.
- **`marshal fleet --server studio`** from another machine needs the forwarded
  URL rather than the loopback one in `fleet.toml`:

  ```toml
  [servers.studio]
  url = "https://<host>.<tailnet>.ts.net:8443"
  ```

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

## A relay on the development box

One box can hold both halves of a relay: the Studio on `127.0.0.1:7700` relays,
and the local daemon joins it as `local`. That is the way to see a relayed host
without a second machine.

Give the Studio the name it relays and that name's secret. Install first: the
install rewrites `webbridge.env`, so the relay line is appended after it, and
another `--install` later wipes it and needs the append again.

```bash
secret_dir="${MARSHAL_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/marshal}/studio"
scripts/dev-studio.sh --install
install -m 0600 /dev/null "$secret_dir/join-local.secret"
printf '%s\n' 'some-long-secret' > "$secret_dir/join-local.secret"
printf 'WEBBRIDGE_RELAY_JOIN=local=%s\n' "$secret_dir/join-local.secret" \
  >> "$secret_dir/webbridge.env"
scripts/dev-studio.sh --restart
```

The unit reads `webbridge.env` as an `EnvironmentFile`, so a plain `--restart`
keeps the relay. Point the daemon at the same relay, store the same secret, and
reinstall — the join is a unit argument, so it changes only there:

```bash
marshal daemon join-token          # paste the secret above; it never echoes
# ~/.config/marshal/config.toml:
#   [daemon]
#   join_url = "http://127.0.0.1:7700"
#   join_name = "local"
marshal daemon install
marshal daemon status              # relay: joined http://127.0.0.1:7700 as local
```

The relay lists what joined it, and a forward needs both tokens: the relay's
bearer, then the host's own, which `marshal daemon token` prints for a pipe.

```bash
curl -s -H "Authorization: Bearer $(cat "$secret_dir/token")" \
  127.0.0.1:7700/api/relay/hosts

curl -s -H "Authorization: Bearer $(cat "$secret_dir/token")" \
  -H "X-Marshal-Host-Token: $(marshal daemon token)" \
  127.0.0.1:7700/api/hosts/local/api/agents
```

The daemon's own token opens no relay route, and the join secret only lets a
daemon offer itself: the relay gives reach, not authority.

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
   writes the unit and token, enables and starts the unit. `--install` implies
   a restart; no separate `start` is needed.
4. **Add the fleet profile** (previous section) and confirm both services:

   ```bash
   ./scripts/dev-daemon.sh --status
   ./scripts/dev-studio.sh --status
   curl -fsS -H "Authorization: Bearer $(./scripts/dev-studio.sh --token)" \
     http://127.0.0.1:7700/api/projects
   ```

5. **Verify a rebuild reaches the services.** Run the loop once and check the
   start time moves in `--status` output.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `dev-daemon.sh` exits non-zero after "did not answer" | It tails `$data_dir/daemon/webbridge.log`; also `systemctl --user status marshal-daemon`. Usually a missing `webbridge` beside `marshal`. |
| Studio answers `401` | The token changed (a `--install` ran) or the browser has an old one. Re-paste `--token`. |
| Studio serves but spawns fail | No `ghcr.io/marshal/agent:latest`; run `--dev-image`. `--status` says when it is missing or stale. Or the runtime is rootful: agents need rootless podman or docker on Linux, and a rootful one answers 501. |
| Agent exits with `GLIBC_2.x not found` | The dev image's base is older than the host's libc; rebuild with `--build-arg BASE_IMAGE=<host-matching image>`. |
| `--install` refuses to write a unit | `MARSHAL_CONFIG_DIR`/`MARSHAL_DATA_DIR`/`XDG_*` point outside `$HOME`. The unit bakes those paths, so it would outlive a temp dir. Unset them, or pass `--force` deliberately. |
| Version line unchanged after a rebuild | Two `+dirty` builds of one commit share a version; compare the start time. |
| `--dev-image` says the build context is wrong | The script copies only `./marshal` into a temp context on purpose: the repo root would hand `app/build` and `.marshal/worktrees` to the runtime. |
| `dev-studio.sh` says flutter is missing | The SPA build is `flutter build web` from `app/` and needs Flutter 3.50+; the Node toolchain left with F8. Install Flutter, or pass `--no-spa` to rebuild Go only. |
