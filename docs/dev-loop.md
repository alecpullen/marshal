# Running the daemon and studio from a working tree

Marshal has two long-running services, and on a development machine you want
both running *this checkout's* build instead of a release. This is how to set
that up, the day-to-day loop, and how to rebuild the setup from nothing.

| Service | Unit | Address | Profile |
|---|---|---|---|
| `marshal-daemon` | `~/.config/systemd/user/marshal-daemon.service` (Linux) or `~/Library/LaunchAgents/dev.marshal.daemon.plist` (macOS) | unix socket | local |
| `marshal-studio` | `~/.config/systemd/user/marshal-studio.service` | `http://127.0.0.1:7700` | server |

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
`--marshal-bin` (that only reaches host-process agents, which the server
profile does not use while a container runtime is on `PATH`). A `web/ui` change
needs the SPA rebuild, which is part of the studio's default run.

### Artifacts

| Artifact | Produced by | Refreshed when |
|---|---|---|
| `./marshal`, `./webbridge` | either script | Go code changes |
| `web/bridge/static/` (embedded SPA) | `dev-studio.sh` | `web/ui` changes |
| `ghcr.io/marshal/agent:latest` | `dev-studio.sh --dev-image` | Go code changes, for studio-spawned agents |

### Common invocations

```bash
# daemon
./scripts/dev-daemon.sh --no-build       # restart the binaries already on disk
./scripts/dev-daemon.sh --no-restart     # build only; the daemon keeps its old binary
./scripts/dev-daemon.sh --install        # rewrite the unit (repo moved, unit settings changed)
./scripts/dev-daemon.sh --status         # query only

# studio
./scripts/dev-studio.sh --ui             # Vite HMR on http://localhost:5173, proxying /api to :7700
./scripts/dev-studio.sh --no-spa         # Go only, keep the embedded SPA
./scripts/dev-studio.sh --no-build       # restart only
./scripts/dev-studio.sh --no-restart     # build (and optionally --dev-image) only
./scripts/dev-studio.sh --install        # rewrite the unit and rotate the token
./scripts/dev-studio.sh --status         # query only
./scripts/dev-studio.sh --token          # print the bearer token
./scripts/dev-studio.sh --agent-image    # slow: compile marshal inside the image (release-shaped)
```

`--ui` and `--token` are queries: they never build, restart, or rewrite
anything. `--ui` prints `localhost:5173`, not `127.0.0.1`, because Vite binds
`localhost`/`::1` and the dev proxy forwards the token.

## The agent image

The studio's server profile containerises every agent whenever podman or
docker is on `PATH`; `SetHostProcessesOnly` is called only for
`--profile local` (`cmd/webbridge/main.go`). Without an agent image the studio
serves, but every spawn fails, so the image is part of a usable setup.

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
| container-runtime volume `marshal-state` | `--state-dir` |

`$data_dir` is `MARSHAL_DATA_DIR`, else `$XDG_DATA_HOME/marshal`, else
`~/.local/share/marshal`.

The state dir is the runtime's `marshal-state` **volume mountpoint**, not a
directory beside it: agent containers mount subpaths of that volume
(`work/<agent>`, `sockets/<agent>`, the shared home, the egress sidecar), so
the bridge and its containers must resolve the same files. A rootless podman
volume is writable and is used; with rootful docker the volume is not writable
and the script falls back to `$data_dir/studio/state` — the studio still
serves, but containerized agents cannot see their state. `--status` prints
which one is in use.

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

1. **Prerequisites.** Go 1.26+ and a C toolchain (cgo). Node/npm for the SPA.
   podman or docker for containerized agents. systemd (Linux) or launchd
   (macOS) for the daemon; the studio helper is Linux-only (it writes a
   systemd user unit — elsewhere run `webbridge --profile server` yourself).
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

   The first run installs `web/ui` dependencies if needed, builds the SPA,
   builds both binaries, writes the unit and token, enables and starts the
   unit. `--install` implies a restart; no separate `start` is needed.
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
| Studio serves but spawns fail | No `ghcr.io/marshal/agent:latest`; run `--dev-image`. `--status` says when it is missing or stale. |
| Agent exits with `GLIBC_2.x not found` | The dev image's base is older than the host's libc; rebuild with `--build-arg BASE_IMAGE=<host-matching image>`. |
| `--install` refuses to write a unit | `MARSHAL_CONFIG_DIR`/`MARSHAL_DATA_DIR`/`XDG_*` point outside `$HOME`. The unit bakes those paths, so it would outlive a temp dir. Unset them, or pass `--force` deliberately. |
| Version line unchanged after a rebuild | Two `+dirty` builds of one commit share a version; compare the start time. |
| `--dev-image` says the build context is wrong | The script copies only `./marshal` into a temp context on purpose: the repo root would hand `web/ui/node_modules` and `.marshal/worktrees` to the runtime. |
