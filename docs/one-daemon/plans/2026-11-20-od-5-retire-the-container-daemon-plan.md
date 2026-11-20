# One daemon plan 5: retire the containerized daemon, and sweep the docs — implementation plan

Date: 2026-11-20. Status: approved 2026-11-20. Amended 2026-11-20: Task 4's
browser section also covers the guard, streaming and sign-out through a proxy.

Base: `main` at `bbdc4486`, with plans [1](2026-11-20-od-1-one-daemon-plan.md)
to [4](2026-11-20-od-4-studio-as-files-plan.md) applied first. Every anchor
below was checked at `bbdc4486`. A step names the function as well as the
line, because plans 1–4 move lines in these files. The baseline is plan 3's,
taken at the same commit: `./web/bridge/...`, `./cmd/webbridge/` and
`./test/daemon/` passed in full.

Approved design: [design.md](../design.md) D5, and the docs named in its
header and in plan 5 of its Plans section. Planning settled these, and
design.md records them:
- **Volume mode goes.** Plan 1 kept named-volume state for a containerized
  bridge. With that bridge gone, a fleet's state is always the host directory
  plan 1 binds. `NewFleet` loses its `projectMounts` and `stateVolume`
  parameters.
- **The reserved prefix stays.** The `marshal-` volume prefix stays reserved
  for workspaces, so an old `marshal-state` volume cannot be mounted into an
  agent by name.
- **`--state-volume` and `--project-mount` are refused,** with a message
  naming D5. No unit ever wrote them: only `compose.yaml` and hand-run
  containers did. `--profile` and plan 3's retired listener flags stay
  accepted and ignored, because installed units carry them.
- **The docs sweep runs from a table.** Each retired term has a replacement.
  Live docs are rewritten to the replacement. Decision records (P4, P5, P8,
  F1, distribution) are not rewritten: they get a dated "superseded by" line.
- **`docs/DEPLOYMENT.md`** becomes "Running Marshal on a server": a Linux
  machine with `marshal daemon install`, linger on, and rootless Podman for
  workspace agents.
- **`CHANGELOG.md`** gets one "One daemon" section under Unreleased, covering
  plans 1–5.

Inventory: [inventory.md](../inventory.md) §Plan 5.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **The daemon has one state layout:** its state directory on the host,
  bind-mounted into container agents.
- **The containerized daemon is gone:** `compose.yaml`,
  `build/Dockerfile.bridge`, the `webbridge` images, `--state-volume` and
  `--project-mount`.
- **No live doc describes a profile, a token, a relay, a server profile, an
  embedded Studio or a containerized bridge.** The decision records that
  chose them say what superseded them.

## Non-goals

- Removing `--profile` or plan 3's accepted-and-ignored flags. Units written
  before plans 1 and 3 still carry them.
- Removing `DaemonInfo.profile`. Clients still check it for `"local"`
  (`internal/launch/daemon.go:185`, `fleet.go:84`).
- The agent image and its Dockerfiles (`build/Dockerfile`, `Dockerfile.dev`,
  `Dockerfile.release`), which stay (D5).
- Rewriting historical plans and specs under `docs/*/plans/` and
  `docs/*/specs/`. They record what was decided at the time.

## Assumptions

Each was checked at the base unless it says otherwise.

- **Volume mode.**
  - `NewFleet(ws, marshalBin, agentEnv, stateDir, limits, buildVersion,
    projectMounts, stateVolume)` (`web/bridge/fleet.go:475`) defaults an empty
    volume to `marshal-state` (`:479–481`).
  - There are 41 calls to `NewFleet` outside its definition: 36 with eight
    arguments, four with nine, one with five.
  - After plan 1:
    - `stateMount` binds under the state directory when `stateOnHost` is set,
      else calls `volumeMount` (`mounts.go`);
    - `SetStateOnHost` sets `stateOnHost`, `userConfigDir`, and
      `requireRootless` on Linux;
    - `webbridge` calls it whenever `--state-volume` is empty.
- **Project mounts.**
  - `ProjectMount` and `TranslateToHost` are in `mounts.go:30–60`.
    `Fleet.projectMounts` is at `fleet.go:246`, and `localMountFor`
    (`:686`) translates through it.
  - Plan 1 deleted the control agent's use (`control.go:100–113`) with
    `ExtraBinds`.
  - `cmd/webbridge` parses `--project-mount` with `parseProjectMounts`
    (`main.go:550–567`).
- **Workspace mounts.**
  - `wsspawn.go:131` and `:153` mount a mirror and a stored file from the
    state volume (plan 1 makes them `f.stateMount`).
  - `:138` refuses a workspace volume named `f.stateVolume`, or starting with
    `marshal-`.
  - `plainVolumeMount` is a workspace's own named volume, and stays.
- **`defaultStateDir`** is `/state` (`cmd/webbridge/main.go:48–51`), the
  container's mount point. After plan 1, the daemon's default state directory
  comes from `applyDaemonDefaults`.
- **The deploy files.**
  - `compose.yaml` (17 lines) and `build/Dockerfile.bridge` (30 lines).
  - `.goreleaser.yaml`'s `bridge-amd64` and `bridge-arm64` (`:151–168`) and
    its two `webbridge` manifests (`:180–187`).
  - The release runs GoReleaser with `--skip=docker` (`release.yaml:59`), so
    no image was ever pushed from CI.
- **Docs.**
  - `docs/DEPLOYMENT.md` (88 lines) is wholly the containerized bridge.
  - The decisions named in design.md's header are at:
    - `docs/daemon/p4-local-daemon.md:232` (`### D1. The local profile`);
    - `docs/daemon/p5-fleet-screen.md:999` (`### F7. Server profiles`);
    - `docs/daemon/p8-studio-federation.md` D6 (`:143`), D11 (`:202`) and
      D18 (`:296`), each opening a run of decisions;
    - `docs/studio-flutter/f1-client.md`'s numbered decisions 3, 5, 6 and 8
      (`:71`, `:86`, `:94`, `:103`);
    - `docs/distribution/design.md:97`.
  - `CHANGELOG.md` follows Keep a Changelog, with an `## [Unreleased]` section
    first.

## Contracts

```go
// web/bridge/fleet.go after Task 1

// NewFleet builds a fleet whose state lives under stateDir on the host.
func NewFleet(ws *Workspace, marshalBin string, agentEnv map[string]string, stateDir string, limits Limits, buildVersion string) *Fleet

// SetConfigHome binds userConfigDir read-only as container agents' config
// home ("" keeps the state subpath), and on Linux requires a rootless
// container runtime. It replaces plan 1's SetStateOnHost. Call it before
// anything starts.
func (f *Fleet) SetConfigHome(userConfigDir string)

// web/bridge/mounts.go after Task 1

// stateMount binds stateDir/subpath at target.
func stateMount(stateDir, target, subpath string, readonly bool) []string
```

## Task 1: A fleet's state is always on the host

**Goal:**
- Named-volume state, project mounts and the `stateOnHost` switch are deleted.
- `NewFleet` takes no volume and no mounts.
- Every state mount binds the state directory.

**Files:** `web/bridge/fleet.go`, `web/bridge/mounts.go`,
`web/bridge/container.go`, `web/bridge/egress_fleet.go`,
`web/bridge/terminal.go`, `web/bridge/wsspawn.go`, every `web/bridge` test
that calls `NewFleet` or pins a volume spelling, `test/daemon` (its
`NewFleet` calls), and the wiring in `cmd/webbridge/main.go`.

**Steps:**
1. **`mounts.go`.**
   - `stateMount` takes Contracts' arguments and always answers
     `bindMount(filepath.Join(stateDir, subpath), target, readonly)`.
   - Delete `volumeMount`, `ProjectMount`, `TranslateToHost` and their
     comments.
   - `plainVolumeMount` stays.
2. **`container.go`.** Delete `ContainerConfig.StateVolume`. `StateDir`'s
   comment says it is the host directory every state subpath binds under.
   `commonRunArgs` calls the new `stateMount` with `c.cfg.StateDir`.
3. **`fleet.go`.**
   - Delete the `stateVolume`, `stateOnHost` and `projectMounts` fields and
     `Fleet.stateMount`. Each caller uses the package `stateMount` with
     `f.stateDir`.
   - `NewFleet` takes Contracts' parameters.
   - `containerConfigWith` always sets `cfg.StateDir = f.stateDir`, and sets
     `cfg.HomeConfigDir = f.userConfigDir`.
   - Rename `SetStateOnHost` to `SetConfigHome`, as Contracts declares it.
   - `localMountFor` answers `a.Project, nil`. Inline it at its callers if
     that leaves it a single line.
4. **The other callers.**
   - `egress_fleet.go`: `egressSidecarArgs` loses its `volume` parameter.
   - `terminal.go` and `wsspawn.go` call the package `stateMount` with
     `f.stateDir`.
   - `wsspawn.go:138`'s check keeps only the `marshal-` prefix rule, with the
     comment: "the prefix stays reserved, so an old marshal-state volume
     cannot be mounted by name".
5. **Callers of `NewFleet`.** Drop the last two arguments everywhere.
   - Find the calls with `grep -rn 'NewFleet(' --include='*.go' . | grep -v
     'func NewFleet'`.
   - The four nine-argument calls pass a `FactoryAPI` or similar after them:
     read each one, and keep what follows.
   - In `cmd/webbridge/main.go`, the call drops `cfg.projectMounts` and
     `cfg.stateVolume`. The `SetStateOnHost` call becomes `SetConfigHome`,
     and is unconditional.
6. **Tests.**
   - Delete `TestStateVolumeIsConfigurable`, `testFleetWithStateVolume` and
     plan 1's `TestAVolumeStateFleetIsUnchanged` and
     `TestStateMountFallsBackToTheVolume`.
   - Every test that pins `type=volume`, `source=marshal-state` or a
     `volume-subpath` spelling for a state mount now expects the bind under
     the test's state directory.
   - Delete the `ProjectMount` and `TranslateToHost` tests in
     `mounts_test.go`, and any `localMountFor` test of translation.
   - Rename plan 1's `TestAHostStateFleet…` tests to drop "HostState":
     `TestAFleetBindsItsAgentsState` and `TestAFleetBindsTheSidecarsState`.

**Verify:**
- `go test ./web/bridge/... ./test/daemon ./cmd/webbridge -count=1` passes.
- `grep -rn 'stateVolume\|StateVolume\|volumeMount(\|ProjectMount\|SetStateOnHost' web/ cmd/ test/`
  prints nothing.

## Task 2: `webbridge` refuses the container flags

**Goal:** `--state-volume` and `--project-mount` are refused with D5's
message, and their parsing is gone.

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/main_test.go`.

**Steps:**
1. Delete the `projectMounts` and `stateVolume` fields from `config`, and
   `parseProjectMounts` (`:550–567`).
2. Keep both flags declared, and refuse either when it is given, by flag or by
   its variable (`WEBBRIDGE_STATE_VOLUME`, and `--project-mount`'s
   repetition): `<flag> is retired: the daemon runs on the host and keeps its
   state in --state-dir; a server is a Linux machine with marshal daemon
   install (docs/one-daemon/design.md D5)`.
3. Delete `defaultStateDir`, if nothing reads it after plan 1. Otherwise,
   change `--state-dir`'s fallback to `applyDaemonDefaults`'s and delete it
   then. `--state-dir`'s help drops "(defaults to /state)".
4. Tests:
   - delete `TestParseConfigStateVolume` and any `--project-mount` test;
   - add `TestTheContainerFlagsAreRefused`: both flags and the variable, each
     error naming D5.

**Verify:** `go test ./cmd/webbridge -count=1` passes, and `grep -n
'state-volume\|project-mount\|WEBBRIDGE_STATE_VOLUME' cmd/webbridge/main.go`
prints four lines: the two flag declarations (`:166–167`) and the refusal
loop's two table entries (`:333–334`). Silence here means the refusals were
deleted with the parsing, not that the file is clean.

## Task 3: The containerized daemon's files go

**Goal:** `compose.yaml`, `build/Dockerfile.bridge` and the `webbridge` image
entries are deleted.

**Files:** `compose.yaml` (deleted), `build/Dockerfile.bridge` (deleted),
`.goreleaser.yaml`.

**Steps:**
1. Delete `compose.yaml` and `build/Dockerfile.bridge`.
2. In `.goreleaser.yaml`, delete the `bridge-amd64` and `bridge-arm64` entries
   under `dockers` (`:151–168`) and the two `ghcr.io/marshal/webbridge`
   manifests (`:180–187`). The agent entries and their manifests stay.

**Verify:**
- `goreleaser check` passes, if `goreleaser` is installed.
- `grep -rn 'Dockerfile.bridge\|marshal/webbridge' .goreleaser.yaml .github build`
  prints nothing.

## Task 4: `docs/DEPLOYMENT.md` describes a server as a machine

**Goal:** the deployment guide describes a Linux machine running the one
daemon, reached over SSH or behind a proxy.

**Files:** `docs/DEPLOYMENT.md`.

**Steps:** replace the whole file. Its title is "Running Marshal on a server",
and it has these sections, in order, each a few sentences or a short command
block:
1. **What a server is** (D5): a Linux machine, the same daemon as a laptop,
   installed by `install.sh` or Homebrew, with `marshal daemon install`. Linger
   is turned on so the daemon outlives logout.
2. **Workspace agents** (D4): an agent with a workspace runs in a container,
   which needs rootless Podman or rootless Docker. A rootful runtime is
   refused, and the daemon's message names the fix. Every other agent is a
   host process. Name `marshal daemon status`'s runtime and studio lines.
3. **Reaching it from a laptop** (D6–D9): `marshal fleet add NAME DEST` on the
   laptop, the SSH requirements (A1–A3, a key without a prompt, the remote
   `marshal` on `PATH` or `marshal = ...`), and `marshal fleet --host NAME`.
4. **A browser** (D10, D13):
   - set `[daemon] web_listen` and `web_url`, and run a proxy that
     authenticates. Give one short example each for Tailscale Serve and Caddy;
   - let `/hooks/`, `/status/` and `/mcp` through without the proxy's login;
   - publish previews on a second hostname with `preview_listen` and
     `preview_url` (D11);
   - set `web_url` to exactly the address the proxy publishes. The web
     listener answers only that host and the loopback names, and refuses a
     cross-site state-changing request (plan 3's guard). Say what the refusal
     reads, so a misconfigured proxy is recognisable;
   - turn off response buffering for `/api/`, because the event streams must
     flush as they are written: `proxy_buffering off;` in nginx, and
     `flush_interval -1` in Caddy;
   - keep the proxy's idle timeout above 30 s, because the streams send a
     heartbeat every 25 s;
   - a proxy that answers an expired session with a redirect to its login page
     is read by the Studio as "signed out", and a reload signs in again.
5. **MCP clients:** `marshal mcp --credential-file` on the server, or over
   SSH: `ssh box marshal mcp --credential-file …` as the client's command.
6. **The agent image:** unchanged, link to `docs/AGENT-IMAGE.md`.
7. **Moving from the containerized bridge:**
   - finish or discard its agents;
   - stop the compose stack;
   - install the daemon on the host;
   - the old `marshal-state` volume is not read, and can be deleted once
     nothing in it is wanted.

**Verify:** `grep -n 'compose\|Dockerfile.bridge\|/state\|--addr' docs/DEPLOYMENT.md`
prints only the moving section's mention of the compose stack.

## Task 5: The live docs describe one daemon

**Goal:** no live doc still describes a retired term, except where it says
the term is retired.

**Files:** `AGENTS.md`, `docs/ARCHITECTURE.md`, `cmd/webbridge/README.md`,
`docs/dev-loop.md`, `scripts/e2e/README.md`, `app/README.md`,
`app/lib/src/client/README.md`, `README.md`.

**Steps:**
1. In each file, find every line that matches this pattern:

   ```text
   --profile|local profile|server profile|bearer|studio\.token|token_cmd|fleet login|fleet token|fleet servers|--server\b|--token|--tls-|studio_addr|7788|relay-join|--join|join_url|join-token|/api/relay|X-Marshal-Host-Token|compose\.yaml|Dockerfile\.bridge|state-volume|marshal-state|project-mount|web/bridge/static|go:embed|dev-studio|marshal-studio|host processes only|SetHostProcessesOnly|process mode|HTTPS to a server
   ```

2. Rewrite each line to the replacement this table gives, citing the decision.
   Delete a line when nothing replaces it.

   | Retired | Replacement |
   |---|---|
   | `--profile local`/`server`, "the local profile", "the server profile" | "the daemon" (one-daemon D1) |
   | server profiles, `[servers.NAME]`, `url`/`ca_file`/`token_cmd`, `fleet login`/`token`/`servers`, `--server` | hosts over SSH: `[hosts.NAME] ssh = …`, `marshal fleet add NAME DEST`, `marshal fleet --host NAME` (D6–D8) |
   | the bearer token, `studio.token`, `--token`, `--token-file`, `marshal daemon token`, the sign-in code | none: the operator's proxy authenticates the browser (D2, D13) |
   | `--tls-cert`, `--tls-key`, TLS in the daemon | the proxy terminates TLS (D3) |
   | `--addr`, `daemon.studio_addr`, `127.0.0.1:7788` | `[daemon] web_listen` (D10) |
   | the relay, `--relay-join`, `--join`, `join_url`, `marshal daemon join-token`, `/api/relay/hosts`, `X-Marshal-Host-Token` | the forward, `/api/hosts/<name>/…`, over each host's tunnel, and `GET /api/hosts` (D10) |
   | `compose.yaml`, `Dockerfile.bridge`, `--state-volume`, the `marshal-state` volume, `--project-mount` | a Linux machine with `marshal daemon install` (D5, `docs/DEPLOYMENT.md`) |
   | `web/bridge/static`, the embedded Studio | `share/marshal/studio`, served from disk (D12) |
   | `dev-studio.sh`, `marshal-studio.service` | `scripts/dev-daemon.sh --studio` |
   | "host processes only", `SetHostProcessesOnly` | an agent with a workspace runs in a container; every other agent is a host process (D4) |
   | egress process mode | egress is a sidecar for container agents (D4) |
   | "HTTPS to a server" | "a host's tunnel socket" (D9) |

3. Lines that say a flag is retired, accepted or refused stay.
   `cmd/webbridge/README.md`'s retired-flags lines from plans 1 and 3 are
   examples.
4. Then reread each file once, top to bottom. Fix any sentence the line edits
   broke, and any section now empty. Merge `cmd/webbridge/README.md`'s TLS
   and profile sections into one "The daemon" section.

**Verify:** the step 1 grep, run over the Files list, prints only lines that
say a term is retired, accepted or refused.

## Task 6: The decision records say what superseded them

**Goal:** each decision design.md's header names carries a dated "superseded
by" line. Nothing else in those records changes.

**Files:** `docs/daemon/p4-local-daemon.md`, `docs/daemon/p5-fleet-screen.md`,
`docs/daemon/p8-studio-federation.md`, `docs/studio-flutter/f1-client.md`,
`docs/distribution/design.md`, `docs/daemon/design.md`.

**Steps:**
1. Under each heading or numbered decision below, add one line, as a block
   quote: `> Superseded by one-daemon <decisions> (docs/one-daemon/design.md,
   2026-11-20): <clause>.`

   | File | Where | Decisions | Clause |
   |---|---|---|---|
   | `p4-local-daemon.md` | `### D1. The local profile` (`:232`) | D1, D2, D10 | the daemon has one profile, no bearer token, and an opt-in web listener |
   | `p5-fleet-screen.md` | `### F7. Server profiles` (`:999`) | D6–D9 | a remote daemon is a host reached over SSH |
   | `p8-studio-federation.md` | D6 (`:143`) | D6–D8 | `fleet.toml` holds hosts over SSH |
   | `p8-studio-federation.md` | D11 (`:202`) | D10 | the relay and the join are replaced by the forward to each host's tunnel |
   | `p8-studio-federation.md` | D18 (`:296`) | D10, D15 | a browser reaches hosts through its daemon's forward, with no token |
   | `f1-client.md` | decisions 3, 5 and 6 (`:71`, `:86`, `:94`) | D2, D13 | no client holds a token; a 401 is the proxy's |
   | `f1-client.md` | decision 8 (`:103`) | D9 | native discovery is the local daemon and its hosts |

2. In `docs/distribution/design.md:97`, the row "Studio, pointed at any
   daemon" reads: `` `webbridge` behind a proxy (`web_listen`), and the native
   Studio over the daemon's tunnels (one-daemon D9, D10) ``.
3. Under `docs/daemon/design.md`'s title, add one block quote: `> Parts of this
   design are superseded by docs/one-daemon/design.md (2026-11-20): the
   profiles, the bearer token, server profiles and the relay.`

**Verify:** `grep -c 'Superseded by one-daemon' docs/daemon/p4-local-daemon.md
docs/daemon/p5-fleet-screen.md docs/daemon/p8-studio-federation.md
docs/studio-flutter/f1-client.md` prints 1, 1, 3 and 4. `git diff --stat` shows
only added or changed lines in these six files.

## Task 7: The changelog records one daemon

**Goal:** `CHANGELOG.md`'s Unreleased section has a "One daemon" block that
tells a user what changed and what to do.

**Files:** `CHANGELOG.md`.

**Steps:**
1. Under `## [Unreleased]`, add a bold `**One daemon**` heading under
   `### Changed`, in the file's style.
2. Write one bullet per change a user can see, each with the one action it
   needs:
   - **The unit.** Every machine runs the same daemon, and `--profile` is
     ignored. Run `marshal daemon install` after upgrading.
   - **Containers.** An agent with a workspace runs in a container. Linux
     needs rootless Podman or Docker.
   - **Egress** is off unless `[daemon] egress` turns it on.
   - **Hosts.** `fleet.toml`'s `[servers.*]` tables are refused. Replace them
     with `[hosts.NAME] ssh = …`. `marshal fleet login`, `token` and
     `servers` are gone, and `--server` is `--host`.
   - **The web Studio.** It needs `[daemon] web_listen` and a proxy that
     authenticates. The bearer token, `marshal daemon token` and the
     sign-in link are gone.
   - **The relay.** `--relay-join`, `--join` and `marshal daemon join-token`
     are gone, and hosts replace them.
   - **MCP.** A local MCP client runs `marshal mcp --credential-file`.
   - **`marshal studio`** opens the app, or `[daemon] web_url`.
   - **The Studio's files** ship in `share/marshal/studio`, and a checkout
     builds them with `scripts/dev-daemon.sh --studio`.
   - **The containerized bridge** is gone: `compose.yaml`, the `webbridge`
     image, `--state-volume` and `--project-mount`. Follow
     `docs/DEPLOYMENT.md`.
3. Add a `### Removed` list if the file uses one under Unreleased, else keep
   everything under `### Changed`.

**Verify:** the block renders as a list under `[Unreleased]`. Check it with
`sed -n '/## \[Unreleased\]/,/^## \[/p' CHANGELOG.md`.

## Task 8: Full verification, and the design is marked implemented

**Steps:**
1. `gofmt -l .` prints nothing, and `go vet ./...` passes.
2. `CGO_ENABLED=1 go build ./cmd/marshal ./cmd/webbridge` succeeds.
3. `scripts/test.sh full` passes, the whole suite uncached. Compare it with the
   baseline, and with the local suite's known flakes (the acp budget tests).
4. `cd app && flutter analyze && flutter test` passes.
5. **By hand on Linux with rootless Podman.** A workspace agent spawns, and
   its agent socket appears under `<data>/daemon/state/sockets/<id>`. No
   `marshal-state` volume is created (`podman volume ls`).
6. In `docs/one-daemon/design.md`:
   - set the status line to `Status: implemented <date>; plans 1–5 merged`,
     with the date the last plan merged;
   - add one line under it: "Inventory: everything listed is done; see
     git log for the commits."

## Integration notes

- **A server running the containerized bridge must move first.** Follow
  "Moving from the containerized bridge" in `docs/DEPLOYMENT.md` before
  upgrading it to this build: the new `webbridge` refuses `--state-volume`.
- **Old volumes are not deleted.** The `marshal-state` volume stays on any
  machine that had one, until the user deletes it.
