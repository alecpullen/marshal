# One daemon — inventory

What [`design.md`](design.md) deletes, cuts, reshapes and adds, file by file.
It is grouped by the plan that does the work.

Anchors are relative to `main` at `bbdc4486`. Line counts are `wc -l`, given
as source / test.

What each action means:
- **Delete:** the whole file goes.
- **Cut:** named functions or blocks go from a file that stays.
- **Reshape:** the file stays, and its behaviour changes as described.
- **Add:** new code. This doc gives no line counts for it.

## Totals

| | Source | Tests |
|---|---:|---:|
| Go files deleted | 1,203 | 1,322 |
| Dart files deleted | 201 | — |
| Deploy files deleted (`compose.yaml`, `Dockerfile.bridge`) | 47 | — |
| Go cut from files that stay (measured functions only) | about 730 | — |
| **Removed before any reshaping** | **about 2,180** | **1,322** |

The 730 is the sum of the function ranges named under Cut below:
- `cmd/webbridge/main.go`: 170
- `hostrelay.go`'s join half: 221
- `internal/daemon/client.go`: 121
- `profile.go`'s URL and relay checks: 77
- `fleetclient`'s HTTPS transport: 56
- `attach.go`: 53
- `edit.go`'s `deleteToken`: 19
- `launch/fleet.go`'s token options: 12

Not counted above:
- Reshaped tests. `cmd/webbridge/local_test.go`, `hostrelay_test.go`,
  `fleet_cli_test.go`, `profile_test.go` and `edit_test.go` total 2,492
  lines. Much of each covers profiles, tokens or relays, but this inventory
  has not measured how much.
- Config fields and the flag declarations in `cmd/webbridge/main.go`.
- `contract.g.dart`, which regenerates.

## Plan 1 — One daemon (D1, D4, A5)

Plan: [plans/2026-11-20-od-1-one-daemon-plan.md](plans/2026-11-20-od-1-one-daemon-plan.md).

| File | Lines | Action | What |
|---|---:|---|---|
| `cmd/webbridge/main.go` | 1,183 | Cut | The `--profile` flag (`:146`). `applyProfile` (`:307–375`) becomes the one daemon's defaults, without the refusals. The `cfg.profile == "local"` branches (`:260`, `:779`, `:853`, `:923`, `:959`) now always run. The working-directory project (`:260–266`) goes. |
| `web/bridge/fleet.go` | — | Cut | The `hostOnly` field (`:156`), `SetHostProcessesOnly` (`:870`), its branch in `containerRuntime` (`:879`), and its clause in `onProjectChild` (`:966`). In `newRuntime` (`:537`), the container path is taken only per D4. Today it is taken whenever a runtime answers. |
| `web/bridge/derive.go:106`, `egress_fleet.go:43`, `wsbuild.go:248`, `wsspawn.go:58` | — | Cut | The `!f.hostOnly` guards. |
| `web/bridge/container.go` | 549 | Reshape | Mount agent state from the daemon's state directory by path, not as subpaths of a named volume (A5). Verify this before plan 5. |
| `web/bridge/mounts.go` | 62 / 132 | Reshape | The same: `volumeMount` builds subpath mounts of the shared volume. |
| `cmd/webbridge/local_test.go` | — / 476 | Reshape | Local-profile defaults and refusals become the one daemon's defaults. |
| `cmd/webbridge/main_test.go` | — / 538 | Reshape | Its profile cases go. |
| `web/bridge/control.go` | — | Cut | `controlContainerConfig`, `controlContainerName`, and `controlRuntime`'s `containerized` and `boundRoots` fields. The control agent is always a host process. `ContainerConfig.HomeConfigWritable` and `ExtraBinds`, which only it used, go too. |
| `web/bridge/egresshost.go`, `egress_fleet.go` | — | Cut | Egress process mode: `startProcessProxy` and `drainRecords` (`egresshost.go:697–764`), the `mode` field and its checks. Egress is the sidecar, for container agents only. |
| `web/bridge/fleet.go` | — | Cut | The plain-agent derive step in `Spawn` (`:1546–1560`). |
| Rootless check | — | Add | `runtimeProbe.rootless`, `rootlessFromInfo` and `Fleet.needsRuntime`. On Linux, a daemon with host state refuses a rootful runtime. |
| `[daemon] egress` | — | Add | A config field, `UnitSpec.Egress` and `--egress on\|off` in the unit. `--egress` defaults to `off`. |
| `scripts/dev-studio.sh`, `compose.yaml` | — | Reshape | Run on the new `webbridge` until plans 3, 4 and 5 change or delete them. The dev studio drops `resolve_state_dir`, the volume-mountpoint workaround. |

## Plan 2 — Hosts over SSH (D6–D9, D15)

Plan: [plans/2026-11-20-od-2-hosts-over-ssh-plan.md](plans/2026-11-20-od-2-hosts-over-ssh-plan.md).

### Go

| File | Lines | Action | What |
|---|---:|---|---|
| `internal/fleetprofile/token.go` | 159 / 175 | Delete | `token_cmd`, the keychain key, `Token`. |
| `internal/fleetprofile/tls.go` | 55 / 130 | Delete | A server profile's TLS config. |
| `internal/fleetprofile/profile.go` | 307 / 450 | Reshape | `[hosts.NAME]` with `ssh`, `marshal` and `notify`. Refuse `[servers.*]`, `url`, `ca_file`, `token_cmd` and `relay` (D6). `checkURL`, `checkRelay`, `checkURLOrRelay` and `isLoopbackHost` (`:211–287`) go. |
| `internal/fleetprofile/edit.go` | 250 / 393 | Reshape | `AddServer` and `RemoveServer` write host tables. `deleteToken` (`:203`) goes. |
| `internal/daemon/client.go` | 1,049 / 1,318 | Cut | `Remote` and `remoteTLSConfig` (`:105–184`, 80 lines), and the bearer headers at `:531`, `:817` and `:1012`. A remote host is `NewClient(<tunnel socket>)`. |
| `internal/fleetclient/client.go` | 961 / 1,652 | Cut | The `HTTPS` transport (`:66–115`), `Transport.Token`, and the bearer header in `authorize` (`:508–522`). The `Socket` transport covers every host. The 401 handling and the rejected state stay until plan 3. |
| `internal/launch/fleet.go` | 139 | Reshape | `ServerFleet` (`:101–139`) and `remoteTarget` go. `Fleet` takes `WithHost(name)` for a tunnel socket. `withTokenStore` and `withTokenRunner` (`:27–38`) go. |
| `internal/launch/launch.go`, `daemon.go` | — | Cut | The `remote`, `tokenStore` and `tokenRunner` options (`:81–87`), and the `remote` branches in `daemon.go`. |
| `cmd/marshal/main.go` | — | Cut / Reshape | `fleet login`, `fleet token`, `fleet servers`, `--json`, `fleetServerRunner`, `fleetLoginStore`, `fleetTokenStore`, `fleetTokenRunner` (`:32–46`), `FleetServers` and `readFleetToken` go. `add` loses `--ca-file` and `--relay` and gains `DEST` and `--marshal`. `--server` becomes `--host`. `fleetStdoutIsTerminal` stays: `marshal daemon token` uses it until plan 3. |
| `cmd/marshal/fleet_cli_test.go`, `main_test.go` | — / 469, — | Reshape | Follow the subcommands. |
| `internal/launch/fleet_test.go` | — | Reshape | The `TestAServerFleet…` tests (`:383–700`) become host tests. |
| `test/daemon/server_profile_test.go` | — / 324 | Delete | Replaced by a host-fleet test over a second socket. |
| `internal/tunnels` | — | Add | The helper body (D7): the ssh commands, the per-host supervisor, and the reporter. |
| Tunnel helper | — | Add | `marshal daemon tunnels --socket PATH` (`cmd/marshal/daemon.go`), started by `webbridge --tunnels` as a `bridge.Helper` beside the refresh helper (`cmd/webbridge/main.go:983`). The unit writes `--tunnels`. It reports with a socket-only `PUT /api/daemon/hosts` (`web/bridge/daemonhosts.go`). |
| `DaemonInfo.hosts` | — | Add | `DaemonHost` and `DaemonHostsReport` in `web/bridge/contract`, printed by `marshal daemon status`. Regenerate `contract.g.dart` and `test/contract/daemon-status.json`. |

### Dart

| File | Lines | Action | What |
|---|---:|---|---|
| `app/lib/src/client/transport_io.dart`, `transport.dart`, `transport_stub.dart` | 173 / 193 | Cut | `serverTransport` and `TokenProvider`: HTTPS, the token callback and `caFile`. `unixTransport` stays. |
| `app/lib/src/client/host_discovery_io.dart` | 151 / 110 | Reshape | Discovery is the local daemon, then each host in its status document. Running `fleet servers --json`, the server profiles and the relayed profiles all go. |
| `app/lib/src/client/fleet_profiles.dart`, `_io.dart`, `_stub.dart` | 40, 99, 29 / 125 | Reshape | Add takes `NAME DEST`. There is no `login`. |
| `app/lib/src/model/hosts_model.dart` | 217 / 244 | Reshape | `HostDraft`'s `url`, `caFile` and `token`, the login after an add, and the native Sign in go. The SSH destination and each host's tunnel (state, last error, since) come in. The browser's `storeToken`, `clearToken`, `signIn` and `signOut` stay until plan 3. |
| `app/lib/src/ui/settings/hosts_section.dart` | 483 | Reshape | D15's form and detail. Sign in shows only where the browser keeps a token, until plan 3. |
| `app/test/ui/settings_test.dart`, `app/test/support/screens.dart` | — / 441, 2,295 | Reshape | The Hosts fixtures and their states. |

## Plan 3 — The web listener (D2, D3, D10, D11, D13, D14)

Plan: [plans/2026-11-20-od-3-web-listener-plan.md](plans/2026-11-20-od-3-web-listener-plan.md).
D12 (the Studio served from files) was split out into plan 4 while planning.

### Go

| File | Lines | Action | What |
|---|---:|---|---|
| `web/bridge/auth.go` | 41 / 53 | Delete | `bearerAuth`. |
| `web/bridge/studio_access.go` | 258 / 178 | Delete | The sign-in code, `/api/studio/session` and `/api/studio/hello`. |
| `web/bridge/hostjoin.go` | 690 / 423 | Delete | The joiner: dialling out, the upgrade and the pool. |
| `web/bridge/hostrelay.go` | 537 / 704 | Cut / Reshape | The join half goes: `relayJoin`, `join`, `signal`, `take`, `newTransport`, `relayConn` and `left` (`:150–370`, about 220 lines). The secrets in `NewHostRelay` go too. The forward half stays: `relayForward`, `splitForward`, `newProxy`, `rewrite`, `relayed`, `forwardFailed` and `relayHosts` (`:382–522`). It dials the host's tunnel socket and drops `X-Marshal-Host-Token`. |
| `web/bridge/attach.go` | 485 | Cut | `overTLSOrLoopback`, `networkAgentAttach` and `hasToken` (`:430–482`, 53 lines). |
| `web/bridge/http.go` | — | Cut | The token and `networkAttach` setup (`:146–152`). In `ServeHTTP`, the relay-join branch (`:181–187`) and the `studioRoute` calls (`:191`, `:236`). |
| `cmd/webbridge/main.go` | — | Cut | The flags `--token`, `--token-file`, `--tls-cert`, `--tls-key`, `--relay-join`, `--join`, `--join-name`, `--join-token-file` and `--join-ca-file`. The functions `applyRelay` (`:376–431`), `readSecretFile` (`:432–452`), `readRelaySecrets` (`:453–467`), `buildJoiner` (`:468–481`), `genToken` (`:595–611`), `readOrCreateToken` (`:612–635`) and the TLS branch of `serveHTTP` (`:1159–1168`). The TCP listener block (`:913–960`) becomes the web listener. |
| `cmd/webbridge/tokenfile_test.go` | — / 62 | Delete | `readOrCreateToken`. |
| `cmd/webbridge/relay_test.go` | — / 301 | Delete | The relay and join flags. |
| `internal/daemon/client.go` | — | Cut | `MintStudioCode` (`:232–272`). |
| `internal/daemon/daemon.go` | 104 | Reshape | The `Token` and `JoinToken` paths (`:41–42`), the join arguments (`:79`) and `--token-file` (`:93–94`) go. `StudioAddr` becomes `WebListen`. |
| `internal/daemon/studio_test.go` | — / 128 | Reshape | Becomes the unit-arguments test for `web_listen`. |
| `internal/app/config` | — | Reshape | `studio_addr`, `join_url`, `join_name` and `join_ca_file` (27 references across `defaults.go`, `file_types.go`, `merge.go`, `save.go` and `types.go`) are retired: still read, only to be named by `marshal daemon install`. `web_listen`, `web_group`, `web_url`, `preview_listen` and `preview_url` come in. |
| `web/bridge/contract` | — | Reshape | `DaemonInfo.studioAddr` and `studioError` become `webListen` and `webError`. `DaemonJoin` goes, and `RelayHosts` becomes `Hosts`. Regenerate `contract.g.dart`. |
| `cmd/marshal/studio.go` | 154 / 131 | Reshape | D14. |
| `cmd/marshal/daemon.go`, `main.go` | — | Cut | `marshal daemon join-token` (`daemon.go:95`) and `marshal daemon token` (`:97`), moved here from plan 2, and `fleetStdoutIsTerminal` (`main.go:51`), which only `daemon token` uses after plan 2. |
| `internal/fleetclient`, `internal/acpclient/fleet.go`, `internal/app/tui/fleet` | — | Cut | The fleet client's 401 handling (`unauthorized`, `reject`), `acpclient.FleetRejected` and the fleet screen's two cases for it (`events.go:63`, `view.go:836`). Moved here from plan 2: after it, nothing the client dials answers 401. |
| Web listener | — | Add | The unix socket or loopback listener, with its mode and group, refusing any other address (D3, D10). Also `GET /api/hosts`, and the preview listener's own setting (D11). |
| `marshal mcp` | — | Add | Open question 2, accepted: `cmd/marshal/mcp.go` and `daemon.Client.PostMCP`. |
| `web/bridge/weblisten.go`, `hostforward.go` | — | Add | `ParseWebListen`, `ListenWeb` and `CheckLoopback`; `GET /api/hosts` and the forward to the tunnel sockets, refusing socket-only routes. |
| `web/bridge/preview.go` | — | Reshape | `SetPreviewURL`, for a proxy that publishes previews on a hostname of their own. The preview listener opens whether or not the web listener does. |

### Dart

| File | Lines | Action | What |
|---|---:|---|---|
| `app/lib/src/client/token_store_web.dart` | 88 | Delete | The page's token. |
| `app/lib/src/client/studio_code_web.dart`, `studio_code_stub.dart` | 59, 3 | Delete | The sign-in code. |
| `app/lib/src/client/host_tokens.dart`, `_web.dart`, `_stub.dart` | 28, 15, 8 | Delete | Per-host browser tokens. |
| `app/lib/src/client/transport_web.dart` | 110 / 223 | Reshape | Same origin, no token. |
| `app/lib/src/client/relayed_transport.dart` | 77 / 157 | Reshape | A path prefix only, with no host token header. |
| `app/lib/src/client/host_discovery_stub.dart` | 68 | Reshape | Reads `GET /api/hosts`, not `/api/relay/hosts`, and uses no token. |
| `app/lib/src/ui/kit/state_message.dart` | — | Reshape | The text of `StateMessage.unauthorized` (D13). The 21 screens that show it are unchanged. |
| `app/lib/src/ui/debug/connection_check.dart` | 189 | Reshape | "token refused" (`:161`). |
| `app/lib/main.dart` | 824 | Cut | The host-token wiring (`:11`, `:761–762`). |
| `app/lib/src/model/hosts_model.dart`, `app/lib/src/ui/settings/hosts_section.dart` | — | Cut | The browser's `storeToken`, `clearToken`, `signIn`, `signOut` and `canSignOut`, and the detail's Token field, Sign in and Sign out. Plan 2 left them for relayed hosts. |

### Tests and scripts

| File | Lines | Action | What |
|---|---:|---|---|
| `scripts/e2e/e2e_client.py` | 124 | Reshape | No `Authorization` header. It dials the web listener, over a unix socket or loopback. |
| `scripts/e2e/harness.py` | 279 | Reshape | `--token` goes. `--base` accepts a socket path. |
| `scripts/e2e/checks_errors.py` | 349 | Reshape | Group H loses its bearer checks (`:41`, `:167`, `:316`). |
| `scripts/e2e/checks_spa.py` | 198 | Reshape | Group I loses the bearer contract (`:151–171`). Plan 4 makes it check the Studio served from its directory. |
| `scripts/dev-studio.sh` | 535 | Reshape | Its daemon serves a loopback web listener with no token. Plan 4 deletes it. |

## Plan 4 — The Studio as files (D12)

Plan: [plans/2026-11-20-od-4-studio-as-files-plan.md](plans/2026-11-20-od-4-studio-as-files-plan.md).
Split out of plan 3 while planning it.

| File | Lines | Action | What |
|---|---:|---|---|
| `web/bridge/assets.go` | 194 / 185 | Reshape | Serve the Studio's files from a directory, in place of `//go:embed static` (`:28`). |
| `web/bridge/static/` | placeholder | Delete | The embed's placeholder index. |
| `.github/workflows/go-checks.yml` | — | Reshape | `webbridge` builds without `flutter build web` first (`:85–93`). The web build and its CDN check become their own step. |
| `scripts/dev-studio.sh` | 535 | Delete | The second, server-profile daemon, its unit and its token. A Studio rebuild becomes a `flutter build web` into the web listener's directory, run from `scripts/dev-daemon.sh` when that daemon has `web_listen` set. |
| `.github/workflows/release.yaml` | — | Reshape | The `studio-web` job (`:72–88`) publishes an asset instead of feeding the embed. `app-darwin` and `app-linux` copy that directory into the bundle (`:154`, `:276`). |
| Studio directory | — | Add | `FindStudioDir`, `Server.SetStudioDir`, `webbridge --studio-dir` and `DaemonInfo.studioDir`, with `share/marshal/studio` beside or above the binaries. |
| `.goreleaser.yaml` | — | Reshape | The archive carries `share/marshal/studio/**/*`, and the formula installs it into `pkgshare`. |
| `scripts/install.sh`, `install_test.sh` | — | Reshape | Install the Studio at `<prefix>/../share/marshal/studio`, and remove it on `--uninstall`. |
| `scripts/dev-daemon.sh` | 248 | Reshape | `--studio` builds the Studio into the checkout's `share/marshal/studio`. `--dev-image` and `--agent-image` move here from `dev-studio.sh`. |
| `scripts/e2e/checks_spa.py` | — | Reshape | Group I checks that the daemon names its Studio directory. |
| `.gitignore`, `.gitattributes` | — | Reshape | Ignore `/share/`, and drop the `web/bridge/static` attributes. |

## Plan 5 — Retire the containerized daemon, and the docs (D5)

Plan: [plans/2026-11-20-od-5-retire-the-container-daemon-plan.md](plans/2026-11-20-od-5-retire-the-container-daemon-plan.md).

| File | Lines | Action | What |
|---|---:|---|---|
| `compose.yaml` | 17 | Delete | |
| `build/Dockerfile.bridge` | 30 | Delete | |
| `web/bridge/fleet.go`, `mounts.go`, `container.go`, `egress_fleet.go`, `terminal.go`, `wsspawn.go` | — | Cut | Named-volume state: `volumeMount`, `ContainerConfig.StateVolume`, `Fleet.stateVolume` and `stateOnHost`. Project mounts: `ProjectMount`, `TranslateToHost` and `Fleet.projectMounts`. `NewFleet` loses both parameters (41 callers), and `SetStateOnHost` becomes `SetConfigHome`. |
| `docs/DEPLOYMENT.md` | 88 | Reshape | "Running Marshal on a server": a Linux machine with `marshal daemon install`. |
| `CHANGELOG.md` | — | Reshape | One "One daemon" block under Unreleased, for plans 1–5. |
| `.goreleaser.yaml` | — | Cut | The two `Dockerfile.bridge` entries under `dockers` (`:156`, `:165`) and their manifests. The agent image entries stay. |
| `cmd/webbridge/main.go` | — | Cut | `--state-volume` (`:162`), `--project-mount` (`:157`) and `parseProjectMounts` (`:550–567`). This depends on plan 1 having confirmed A5. |
| `AGENTS.md` | — | Reshape | Web control plane: the local profile, the relay paragraph, `marshal fleet --server` and `fleet.toml`'s tokens. Also the e2e command, the `internal/fleetprofile` map line, and the `--no-web-resources-cdn` paragraph, which moves to the `studio-web` job. |
| `cmd/webbridge/README.md` | — | Reshape | The TLS section goes, and "The local profile" becomes "The daemon". |
| `scripts/e2e/README.md` | — | Reshape | The run command. |
| `docs/dev-loop.md` | 357 | Reshape | One daemon. These sections go: the studio's token, reaching the studio from the TUI with `token_cmd`, reaching it from another machine with a bearer token, and the relay on the development box. They become `marshal fleet add` over SSH and a proxy in front of `web_listen`. |
| `docs/daemon/p4-local-daemon.md`, `p5-fleet-screen.md`, `p8-studio-federation.md` | — | Note | A "superseded by `one-daemon` Dn" line at each decision named in design.md's header. |
| `docs/distribution/design.md` | — | Reshape | The row "Studio, pointed at any daemon" (`:97`). |
| `docs/studio-flutter/f1-client.md` | — | Note | Decisions 3, 5, 6 and 8. |
| `app/lib/src/client/README.md` | — | Reshape | "HTTPS to a server". |

## Plan 6 — Previews of a remote host's agents (open question 1)

Plan: [plans/2026-11-20-od-6-remote-previews-plan.md](plans/2026-11-20-od-6-remote-previews-plan.md).

| File | Lines | Action | What |
|---|---:|---|---|
| `web/bridge/contract/daemon.go` | — | Add | `DaemonInfo.previewAddr`, `DaemonHost.previewPort` and `PreviewOriginHeader`. Regenerate `contract.g.dart` and the status fixture. |
| `web/bridge/preview.go`, `hostforward.go` | — | Reshape | `SetPreviewAddr`; `issuePreview` builds the link from `X-Marshal-Preview-Origin` when the caller sends one; the forward sets it from the host's `previewPort`. |
| `internal/tunnels` | — | Reshape | The status document's `previewAddr`, a second `-L` to a free loopback port, `DaemonHost.previewPort`, and a restart when the remote preview listener moves. |
| `cmd/marshal/daemon.go` | — | Reshape | The host line names its preview port; the tunnels helper's `Probe` answers the preview address. |
| `app/lib/src/client/bridge_client.dart`, `host_discovery_io.dart`, `daemon/status.dart` | — | Reshape | `BridgeClient.previewOrigin`, sent with every preview request for a remote host. |

## Stays as it is

- The socket, its peer check and every socket-only route.
- The viewer attach and its frame relay (`web/bridge/relay.go`, 644 lines). The
  name is misleading: this is not P8's relay.
- Containerized agents' `viewers.sock` and `contract.ContainerViewerMethods`.
- Per-client MCP credentials and grants, Marshal enrollment, webhook HMAC,
  status links and preview tokens (D2).
- `internal/credentials`, which provider keys still use.
- The agent image, the egress proxy, the secret backends, the quotas and the
  workspace sandbox.
- Client-side host merging: `HostRegistry` and the per-host stores.
