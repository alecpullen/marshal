# One daemon plan 1: one daemon — implementation plan

Date: 2026-11-20. Status: approved 2026-11-20.

Base: `main` at `bbdc4486`. Every anchor below was checked there. The baseline
(`go test -count=1 ./web/bridge/ ./cmd/webbridge/ ./internal/daemon/
./internal/app/config/ ./cmd/marshal/`, with `TMPDIR=/private/tmp/od1`) passed
in full at that commit.

Approved design: [design.md](../design.md) D1 and D4, and assumption A5. Three
decisions were made while planning, and design.md records them:
- A workspace is the only thing that puts an agent in a container. There is no
  new project setting.
- On Linux, container agents need a rootless runtime. Docker Desktop on macOS
  is accepted as it is.
- The egress proxy is off unless `[daemon] egress` turns it on, and it applies
  to container agents only.

Inventory: [inventory.md](../inventory.md) §Plan 1.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **The bridge has one shape.** The local profile's behaviour is the only
  behaviour. `--profile local` still parses, so installed units keep starting,
  and `--profile server` is refused.
- **Container agents work from a daemon on the host.** Their state is
  bind-mounted from the daemon's state directory, and their config home is the
  user's own config directory, read-only. Today both are named-volume subpaths,
  which only work when the bridge itself runs in a container.
- **A workspace decides placement.** An agent with a workspace (its own, or its
  project's default) runs in a container. Every other agent, and the control
  agent, is a host process, whether or not Docker or Podman is installed.
- **Linux needs a rootless runtime for host state.** A rootful one is refused
  with a message that names the fix.
- **Egress is a sidecar for container agents, off by default.** Process mode,
  the in-process proxy for host agents, is deleted.
- **`marshal daemon install` writes no `--profile`,** and writes `--egress`
  from `[daemon] egress`.

## Non-goals

- The TCP listener, the bearer token, the sign-in handover and the relay:
  plan 3.
- SSH hosts: plan 2.
- Retiring `compose.yaml`, `Dockerfile.bridge` and named-volume state: plan 4.
  This plan keeps volume mode working wherever `--state-volume` is given.
- Rootful Linux runtimes for host state, and running containers as the
  daemon's uid.
- `[daemon]` settings for `--secrets`, `--secrets-key-file` and OpenBao. The
  refusals go, so the flags work in a hand-written unit; `marshal daemon
  install` does not write them.
- Any change under `app/`. `DaemonInfo.profile` still reads `"local"`.

## Assumptions

Each was checked at the base unless it says otherwise.

- **Placement today.**
  - `newRuntime` (`web/bridge/fleet.go:537`) prepares egress first (`:542`).
    It then builds a container whenever `containerRuntime` answers (`:546`),
    and falls back to a host process when it does not.
  - The local profile turns the probe off with `SetHostProcessesOnly`
    (`fleet.go:870`), called at `cmd/webbridge/main.go:780`.
  - `onProjectChild` (`fleet.go:965`) puts plain local host agents on their
    project's child. It excludes git agents, workspace agents, and every agent
    while egress runs.
- **Workspace agents already need a runtime.**
  - `Spawn` falls back to the project's default workspace when the request
    names none (`fleet.go:1395–1398`).
  - It refuses with `ErrWorkspaceNeedsRuntime` when no runtime answers
    (`fleet.go:1572–1575`). `http.go:503` maps that error to 501.
  - The Studio imports `devcontainer.json` as a workspace
    (`app/lib/src/ui/settings/environments_section.dart:38–42`).
- **The plain-agent derive step.** `Spawn` derives an image for a plain agent
  whose profile image is not the agent image (`fleet.go:1546–1560`). It is the
  only caller of `ensureDerivedImage` outside workspace builds
  (`wsbuild.go:379`).
- **State mounts are named-volume subpaths.**
  - In `commonRunArgs` (`container.go:229–258`): the work tree, the socket
    directory, both shared homes, and `ExtraVolumes`.
  - Elsewhere: the egress sidecar (`egressSidecarArgs`, `egress_fleet.go:216`,
    called at `:154`), the test shell (`terminal.go:912`), and workspace repo
    and file mounts (`wsspawn.go:131`, `:153`).
  - The bridge dials `socketDirFor(stateDir, id)` (`fleet.go:615`), which
    `start` creates (`container.go:313`).
  - The two views meet only when the state directory is the volume's
    mountpoint. `scripts/dev-studio.sh:196–214` arranges exactly that, and
    says rootful Docker cannot.
  - `NewFleet` defaults an empty volume name to `marshal-state`
    (`fleet.go:479`). Thirty-six tests call `NewFleet`, and about 64 test
    lines pin the volume spelling.
- **Agent images set no `USER`** (`build/Dockerfile*`). Not checked here, and
  checked by hand in Task 10:
  - rootless Podman maps container root to the user;
  - Docker Desktop's file sharing writes as the user;
  - a rootful Linux runtime leaves root-owned files in a bind.
- **The probe.** `probeRuntime` (`fleet.go:844`) runs `<runtime> info` once
  per process. Known behaviour, not checked here:
  - `docker info --format '{{json .SecurityOptions}}'` lists `name=rootless`
    under rootless Docker;
  - `podman info --format '{{.Host.Security.Rootless}}'` prints `true` or
    `false`.
- **The control agent.**
  - `buildControlChild` (`control.go:60`) builds a container through
    `controlContainerConfig` (`:79`) whenever a runtime answers.
  - That config is the only user of `ContainerConfig.HomeConfigWritable` and
    `ExtraBinds` (`container.go:81–86`, `:247`, `:251`).
  - `controlRuntime.containerized` and `boundRoots` (`control.go:41–46`) are
    read at `:208` and `:316`.
- **Egress.**
  - `StartEgress` (`egress_fleet.go:57`) runs container mode when
    `egressRuntimeName` answers, and process mode otherwise (`:95`,
    `startProcessProxy` at `egresshost.go:700`).
  - Process mode runs only on a server-profile bridge with no runtime: the
    local profile refuses egress (`main.go:333`).
  - Mode checks: `egress_fleet.go:372`, `:385`, `:414`, `:434`;
    `preview.go:434`; `wspool.go:444`. `egresshost.go:231` pushes policy to
    `h.proxy`, which only process mode sets.
  - `network_http.go:91` reports `egressProcessMode()` as `processMode`
    (`contract/network.go:28`). That field stays.
  - `egressPrepare` has one production caller, `newRuntime`. Its tests call it
    directly (`egresshost_test.go`, `wsegress_followup_test.go`).
- **A workspace agent's extras are cached.** `containerConfigWith` resolves
  a workspace agent's mounts through `workspaceRuntimeExtras`
  (`wsspawn.go:211`). That returns the entry in `f.wsExtras[a.ID]` when there
  is one (`:213–216`), and `NewFleet` creates the map (`fleet.go:512`). A test
  that seeds `f.wsExtras[a.ID] = wsExtras{}` (under `f.wsMu`) reaches the
  container path without a stored workspace.
- **Tests that run the production `newRuntime`.** `TestStateVolumeIsConfigurable`
  (`fleet_test.go:55`), `TestSpawnPassesAgentEnvToContainer` (`:468`),
  `TestAHostOnlyFleetNeverProbes` (`hostonly_test.go:32`) and
  `TestAContainerChildNamesItsViewerSocket` (`ownviewers_test.go:93`). Every
  other test replaces `f.newRuntime`.
- **The command.**
  - `applyProfile` (`main.go:307–375`) holds both profiles' defaults and
    refusals.
  - The profile branches are at `:260`, `:779`, `:853`, `:923` and `:959`.
  - `--egress` defaults to `on` (`:175`) and `--state-volume` to
    `marshal-state` (`:162`).
  - `cmd/webbridge` already imports internal packages (`main.go:26–27`), so it
    can call `config.UserDir` (`internal/app/config/locations.go:12`).
- **The unit.**
  - `UnitSpec.Args` writes `--profile local` first
    (`internal/daemon/daemon.go:67–72`).
  - `marshal daemon install` builds the spec from the user config alone
    (`cmd/marshal/daemon.go:265–303`).
  - `[daemon]` follows `idle_session_timeout` through `types.go:441`,
    `file_types.go:170`, `merge.go:404` and `save.go:336`. `set` is at
    `merge.go:18`.
- **Clients check the profile.** `internal/launch/daemon.go:185` and
  `fleet.go:84` refuse a daemon whose `DaemonInfo.Profile` is not `"local"`,
  so the field keeps that value.
- **The binaries ship together.** `release.yaml:135`, `:154` and `:276` copy
  `marshal` and `webbridge` together, so a unit without `--profile` only ever
  meets a `webbridge` from this plan onwards.

## Contracts

```go
// web/bridge/mounts.go

// bindMount is a --mount argument binding source on the host at target.
func bindMount(source, target string, readonly bool) []string

// stateMount is the mount of one state subpath at target: a bind of
// stateDir/subpath when stateDir is set, else volumeMount of volume.
func stateMount(runtimeName, volume, stateDir, target, subpath string, readonly bool) []string
```

```go
// web/bridge/container.go — new ContainerConfig fields.

// StateDir, when non-empty, is the host directory the state subpaths live
// under: every state mount binds StateDir/<subpath>, and StateVolume is not
// used.
StateDir string
// HomeConfigDir, when non-empty, is a host directory bound read-only as the
// shared config home, in place of HomeConfigSubpath.
HomeConfigDir string
```

```go
// web/bridge/fleet.go

// SetStateOnHost makes the fleet's state mounts binds of its state
// directory, and binds userConfigDir read-only as container agents' config
// home ("" keeps the state subpath). On Linux it also requires a rootless
// container runtime. Call it before anything starts.
func (f *Fleet) SetStateOnHost(userConfigDir string)

// stateMount is the package stateMount with this fleet's volume and, when
// its state is on the host, its state directory.
func (f *Fleet) stateMount(runtimeName, target, subpath string, readonly bool) []string

// needsRuntime is the error a container-only operation returns when
// containerRuntime answers none: ErrWorkspaceNeedsRuntime, wrapped with
// the reason when a runtime was found and refused.
func (f *Fleet) needsRuntime() error

// rootlessFromInfo reads whether the runtime named name ("docker" or
// "podman") is rootless from the output of its info command, as
// probeRuntime runs it.
func rootlessFromInfo(name string, out []byte) bool

// New Fleet fields.
stateOnHost     bool        // set by SetStateOnHost
userConfigDir   string      // set by SetStateOnHost
requireRootless bool        // set by SetStateOnHost when runtime.GOOS == "linux"
rootless        func() bool // nil: probeRuntime's answer; tests inject one

// New runtimeProbe field.
rootless bool
```

```go
// internal/daemon/daemon.go — new UnitSpec field.
Egress bool // written as --egress on or --egress off

// internal/app/config — new fields.
// DaemonConfig:
Egress bool `toml:"egress"`
// fileDaemon:
Egress *bool `toml:"egress"`
```

## Task 1: State mounts can bind the host directory

**Goal:** a container config with `StateDir` set mounts every state subpath as
a bind under it, and one with `HomeConfigDir` set binds that directory
read-only as the config home.

**Files:** `web/bridge/mounts.go`, `web/bridge/container.go`,
`web/bridge/mounts_test.go`, `web/bridge/container_test.go`.

**Steps:**
1. In `mounts.go`, add `bindMount` and `stateMount` as Contracts declare them.
   - `bindMount` renders `--mount` with `type=bind,source=<source>,target=<target>`,
     plus `,readonly` when asked. It uses the same spelling for Docker and
     Podman.
   - `stateMount` returns `bindMount(filepath.Join(stateDir, subpath), target,
     readonly)` when `stateDir` is non-empty. Otherwise it returns `volumeMount`
     with the same arguments, unchanged.
2. In `ContainerConfig` (`container.go:48`), add `StateDir` and `HomeConfigDir`
   with the Contracts' comments, after `StateVolume`.
3. In `commonRunArgs` (`container.go:229`), replace the five `volumeMount`
   calls (work tree `:238`, socket `:241`, config home `:247`, data home
   `:249`, `ExtraVolumes` `:255`) with `stateMount`. Each call passes
   `c.cfg.RuntimeName`, `c.cfg.StateVolume` and `c.cfg.StateDir`.
4. Inside the `HomeConfigSubpath != ""` block, the config home is
   `bindMount(c.cfg.HomeConfigDir, containerConfigDir, true)` when
   `HomeConfigDir` is set, and the `stateMount` from step 3 otherwise. The
   data home always uses `stateMount`. The environment block at `:275` is
   unchanged.

**Tests** (new, in `mounts_test.go` unless named):
- `TestBindMountSpellsTheSourceAndTarget`: the argument pair, and the
  `,readonly` suffix.
- `TestStateMountBindsUnderTheStateDir`: with a state dir, the source is the
  joined path, and the volume name does not appear.
- `TestStateMountFallsBackToTheVolume`: with no state dir, the output equals
  `volumeMount`'s for Docker and for Podman.
- `TestAHostStateContainerBindsEveryStateMount` (`container_test.go`): a
  config with `StateDir`, a git agent's `WorkSubpath`, both home subpaths and
  one `ExtraVolumes` entry. `buildRunArgs` contains no `type=volume`, and it
  contains a bind source for each of the five paths.
- `TestTheConfigHomeBindsTheUsersDirectoryReadOnly` (`container_test.go`):
  with `HomeConfigDir`, the config home is that directory, `readonly`, at
  `containerConfigDir`. The data home is still under `StateDir`.

**Verify:** `go test ./web/bridge -run 'Mount|BuildRunArgs|HostState|ConfigHome' -count=1`
passes. So does `go test ./web/bridge -run 'Container' -count=1`, which shows
the existing volume-mode tests are unchanged.

## Task 2: The fleet can keep its state on the host

**Goal:** after `SetStateOnHost`, every container the fleet starts (agents, the
egress sidecar, test shells and workspace mounts) binds state from the state
directory. Without the call, nothing changes.

**Files:** `web/bridge/fleet.go`, `web/bridge/egress_fleet.go`,
`web/bridge/terminal.go`, `web/bridge/wsspawn.go`, and tests in
`web/bridge/fleet_test.go` and `web/bridge/preview_test.go`.

**Steps:**
1. In `fleet.go`, add the `stateOnHost` and `userConfigDir` fields beside
   `hostOnly` (`:156`), and `SetStateOnHost` beside `SetHostProcessesOnly`
   (`:870`). This task sets those two fields only; Task 3 adds
   `requireRootless`.
2. Add `Fleet.stateMount`. It calls the package `stateMount` with
   `f.stateVolume`, and with `f.stateDir` when `f.stateOnHost` is set (else
   `""`).
3. In `containerConfigWith` (`fleet.go:608`), when `f.stateOnHost` is set, set
   `cfg.StateDir = f.stateDir` and `cfg.HomeConfigDir = f.userConfigDir`.
   Leave `StateVolume` as it is: `commonRunArgs` ignores it once `StateDir` is
   set.
4. Change `egressSidecarArgs` (`egress_fleet.go:216`) to
   `egressSidecarArgs(rtName, volume, stateDir, image string)`. Its mount is
   `stateMount(rtName, volume, stateDir, egressMountDir, egressSubpath, false)`.
   The caller at `:154` passes `f.stateDir` when `f.stateOnHost` is set, else
   `""`.
5. Replace the three state `volumeMount` calls with `f.stateMount`:
   `terminal.go:912`, and `wsspawn.go:131` and `:153`. Leave the reserved
   volume check at `wsspawn.go:138` and `plainVolumeMount` alone.
6. Update the one caller in `preview_test.go:645` to pass `""` as the new
   `stateDir`.

**Tests** (new, in `fleet_test.go`):
- `TestAHostStateFleetBindsItsAgentsState`. It builds the fleet as
  `testFleetWithStateVolume` does (`fleet_test.go:43`), calls
  `SetStateOnHost` with a temporary config directory, injects a Docker probe,
  and calls `f.newRuntime` with an agent that has `Workspace: &AgentWorkspace{Name: "w"}`,
  after seeding `f.wsExtras` for it (see Assumptions).
  The container config's `StateDir` is the fleet's state directory and its
  `HomeConfigDir` is the given directory. The run arguments contain no
  `type=volume`.
- `TestAHostStateFleetBindsTheSidecarsState`: `egressSidecarArgs` with a state
  dir binds `<stateDir>/<egressSubpath>`.
- `TestAVolumeStateFleetIsUnchanged`: without `SetStateOnHost`, the run
  arguments name `source=marshal-state`, as `TestStateVolumeIsConfigurable`
  expects.

**Verify:** `go test ./web/bridge -run 'HostState|VolumeState|StateVolume|Sidecar|Preview|Terminal|Workspace' -count=1`
passes.

## Task 3: A host-state fleet on Linux needs a rootless runtime

**Goal:** on Linux, a fleet whose state is on the host uses a container runtime
only if it is rootless. A rootful one is refused, and every container-only
operation says so.

**Files:** `web/bridge/fleet.go`, `web/bridge/terminal.go`,
`web/bridge/wspool.go`, `web/bridge/wsbuild.go`, and a new
`web/bridge/rootless_test.go`.

**Steps:**
1. Add `rootless` to `runtimeProbe` (`fleet.go:837`). `probeRuntime` (`:844`)
   now runs `info` with a format:
   - Docker: `info --format {{json .SecurityOptions}}`;
   - Podman: `info --format {{.Host.Security.Rootless}}`.

   A runtime still counts as found when that command exits 0. `rootless` is
   `rootlessFromInfo(candidate, out)`.
2. Add `rootlessFromInfo`. For Docker it is true when the output contains
   `rootless`. For Podman it is true when the trimmed output is `true`. Any
   other name is false.
3. Add `requireRootless` and `rootless` to `Fleet`. `SetStateOnHost` (Task 2)
   sets `requireRootless` to `runtime.GOOS == "linux"`.
4. In `containerRuntime` (`fleet.go:878`), after the probe answers a runtime,
   return none when `f.requireRootless` is set and the runtime is not
   rootless. Rootless means `f.rootless()` when it is set, else
   `probeRuntime().rootless`. The `hostOnly` branch stays until Task 6.
5. Add `needsRuntime`.
   - When `f.requireRootless` is set and the probe (`f.probe`, else
     `detectedRuntime`) answers a runtime that is not rootless, it returns
     `ErrWorkspaceNeedsRuntime` wrapped with: `<name> runs as root, and a
     daemon on the host needs a rootless runtime on Linux (rootless Docker or
     Podman)`.
   - Otherwise it returns `ErrWorkspaceNeedsRuntime`.
6. Make these sites return `f.needsRuntime()` in place of the bare error:
   `fleet.go:1575`, `terminal.go:322`, `terminal.go:875` and `wspool.go:172`.
   `wsbuild.go:253` wraps it as `fmt.Errorf("%w: cannot build workspaces",
   f.needsRuntime())`.

**Tests** (new, in `rootless_test.go`):
- `TestRootlessFromInfoReadsDocker`: rootless and rootful `SecurityOptions`
  output.
- `TestRootlessFromInfoReadsPodman`: `true`, `false` and empty.
- `TestARootfulRuntimeIsRefusedForHostState`. The test sets `requireRootless`
  directly, so it runs on any OS. Probe Docker answers, `rootless` returns
  false. Then `containerRuntime` answers none, and `needsRuntime` satisfies
  `errors.Is(err, ErrWorkspaceNeedsRuntime)` and contains `runs as root`.
- `TestARootlessRuntimeIsUsedForHostState`: the same, with `rootless` returning
  true. The runtime is used.
- `TestAVolumeStateFleetNeverAsksIfRootless`: without `requireRootless`,
  `rootless` is never called.

**Verify:** `go test ./web/bridge -run 'Rootless|Rootful|NeedsRuntime|501' -count=1`
passes.

## Task 4: A workspace is what makes a container

**Goal:** only an agent with a workspace runs in a container. Plain agents and
the control agent are host processes, plain local agents go on their
project's child, and only container agents are wired to egress.

**Files:** `web/bridge/fleet.go`, `web/bridge/control.go`,
`web/bridge/container.go`, and tests in `web/bridge/fleet_test.go`,
`web/bridge/ownviewers_test.go`, `web/bridge/control_test.go`,
`web/bridge/container_test.go` and `web/bridge/hostonly_test.go`.

**Steps:**
1. In `newRuntime` (`fleet.go:537`):
   - **An agent with no workspace** (`a.Workspace == nil`) gets the host
     child that the `!ok` branch builds today (`:547–569`): the binary, the own
     viewer socket under the state root, and no proxy environment. It gets it
     without preparing egress and without asking for a runtime. Delete the
     `if proxied` environment block.
   - **An agent with a workspace** prepares egress (`:542`), asks
     `containerRuntime`, and returns `nil, f.needsRuntime()` when none
     answers. Otherwise it builds the container as it does now.
2. `onProjectChild` (`fleet.go:965`) answers true exactly when `a.Project` is
   non-empty, `a.SourceKind` is not `"git"` and `a.Workspace` is nil. Delete
   the `hostOnly` and egress clauses, and update its comment.
3. In `Spawn`, delete the plain-agent derive block (`fleet.go:1546–1560`, from
   the comment "A declared base…" to the closing brace). Leave
   `ResolveProfile` (`:1538`) as it is.
4. In `control.go`:
   - `buildControlChild` returns `(*Child, error)` and always builds
     `&Child{MarshalBin: f.marshalBin}` when `f.newControl` is nil.
   - Delete `controlContainerConfig`, `controlContainerName`, and the
     `containerized` and `boundRoots` fields with their comments.
   - Delete the check that reads them in `projectSession` (`:316–318`).
     `controlSessionCwd` returns `f.controlWorkDir()`.
   - Update the caller at `:139` and the assignment at `:160`.
5. In `ContainerConfig`, delete `HomeConfigWritable` and `ExtraBinds`.
   `commonRunArgs` mounts the config home read-only (the `!HomeConfigWritable`
   argument becomes `true`) and drops the `ExtraBinds` loop (`:251–253`).
6. Give these tests' agents `Workspace: &AgentWorkspace{Name: "w"}`, and seed
   `f.wsExtras` for each one (see Assumptions), so they still reach the
   container path: `TestStateVolumeIsConfigurable`,
   `TestSpawnPassesAgentEnvToContainer` and
   `TestAContainerChildNamesItsViewerSocket`. Delete
   `TestControlContainerConfigUsesControlName` (`control_test.go:96`), and
   drop the `container_test.go` line that sets `ExtraBinds` or
   `HomeConfigWritable`.
7. In `TestAHostOnlyFleetNeverProbes`, keep the assertions. The plain agent and
   the control agent are host processes, and the probe never runs. It is
   renamed in Task 6.

**Tests** (new, in `fleet_test.go`):
- `TestAPlainAgentIsAHostProcessWhenARuntimeAnswers`: a probe that answers
  Docker, and a plain local agent. `newRuntime` returns a child that is not
  containerized, and the probe is not called.
- `TestAWorkspaceAgentWithNoRuntimeIsRefused`: a probe that answers none, and
  an agent with a workspace. `newRuntime` returns an error that satisfies
  `errors.Is(err, ErrWorkspaceNeedsRuntime)`.
- `TestAPlainLocalAgentRunsOnItsProjectChildWhileEgressRuns`: with
  `f.egress = newEgressHost(f)` (`egress_fleet.go:73`; nothing needs to start),
  `onProjectChild` is true for a plain local agent and false for a workspace
  agent.
- `TestTheControlAgentIsAHostProcess` (`control_test.go`): with a probe that
  answers Docker and no `newControl`, `buildControlChild` returns a child
  that is not containerized.

**Verify:** `go test ./web/bridge -run 'HostProcess|Workspace|ProjectChild|Control|Container|StateVolume|AgentEnv|ViewerSocket|HostOnly' -count=1`
passes.

## Task 5: Egress needs a container runtime

**Goal:** the egress proxy runs only as the sidecar. With no usable runtime,
`StartEgress` fails and egress stays off. Process mode is deleted.

**Files:** `web/bridge/egress_fleet.go`, `web/bridge/egresshost.go`,
`web/bridge/preview.go`, `web/bridge/wspool.go`, and
`web/bridge/egresshost_test.go`.

**Steps:**
1. In `StartEgress` (`egress_fleet.go:57`), when `egressRuntimeName` answers
   no runtime, return `fmt.Errorf("egress: the proxy runs as a sidecar
   container: %w", f.needsRuntime())` before building anything. Delete the
   process-mode branch (`:94–99`). The log line at `:104` drops `"mode"`.
2. In `egresshost.go`:
   - Delete `egressModeContainer`, `egressModeProcess` (`:51–52`) and the
     `mode` field.
   - Delete `startProcessProxy`, `drainRecords` and the
     `// ---- process mode ----` section header (`:697–764`).
   - Delete the `procSrv`, `records` and `proxy` fields (`:116–118`), the
     shutdown of `procSrv` (`:778–780`), and the push to `h.proxy`
     (`:231–233`).
   - Keep `EgressProxy` itself: the sidecar runs it.
3. Every `h.mode == egressModeContainer` test becomes a nil check on the host:
   - `egress_fleet.go:385`: set `w.Network` unconditionally.
   - `:414`: return when `h == nil`.
   - `:434`: `egressProcessMode` returns `f.egress == nil`.
   - `preview.go:434` and `wspool.go:444`: the nil check.
4. At `egress_fleet.go:372–383`, keep the container branch (`:373–377`)
   without its condition, and delete the `else` branch (`:378–383`), which
   held the host CA paths.
5. Replace `TestEgressProcessMode` (`egresshost_test.go:544`) with
   `TestEgressNeedsAContainerRuntime`. With `egressRuntime` answering none,
   `StartEgress` returns an error that satisfies
   `errors.Is(err, ErrWorkspaceNeedsRuntime)`, `f.egress` is nil, and
   `f.egressProcessMode()` is true.

**Verify:**
- `go test ./web/bridge -run 'Egress|Preview|Pool|Network' -count=1` passes.
- `grep -n 'egressMode\|startProcessProxy\|drainRecords\|procSrv' web/bridge/*.go`
  prints nothing.

## Task 6: The host-only switch goes

**Goal:** `hostOnly` and `SetHostProcessesOnly` no longer exist. Placement is
Task 4's rule everywhere.

**Files:** `web/bridge/fleet.go`, `web/bridge/derive.go`,
`web/bridge/egress_fleet.go`, `web/bridge/wsbuild.go`,
`web/bridge/wsspawn.go`, `web/bridge/hostonly_test.go`,
`web/bridge/fleetchild_test.go`, `web/bridge/sessions_test.go`,
`web/bridge/fleet_test.go`, and the wiring line `cmd/webbridge/main.go:780`.

**Steps:**
1. Delete the `hostOnly` field (`fleet.go:156`), `SetHostProcessesOnly`
   (`:870`) and the `hostOnly` branch in `containerRuntime` (`:879–881`).
2. Each `f.runner != nil && !f.hostOnly` guard becomes `f.runner != nil`:
   `derive.go:106`, `egress_fleet.go:43`, `wsbuild.go:248`, `wsspawn.go:58`.
   Delete the comments above them that cite decision 4.
3. Delete the `fleet.SetHostProcessesOnly()` call at `cmd/webbridge/main.go:780`.
   The rest of that block stays: Task 7 makes it unconditional.
4. Delete every `SetHostProcessesOnly()` call in tests: six in
   `fleetchild_test.go`, one of them in `hostOnlyProjectFleet` (`:280`), and
   one in `fleet_test.go`. Plain agents are now host processes without it.
   Rename `hostOnlyProjectFleet` to `childOnlyProjectFleet`, and its comment
   (`:277`) to "projectFleet with newRuntime failing, so only project
   children start". Update its seven uses in `fleetchild_test.go` and two in
   `sessions_test.go` (`:213`, `:317`).
5. In `hostonly_test.go`:
   - `hostOnlyFleet` stops calling the switch.
   - `TestAHostOnlyFleetNeverProbes` becomes `TestPlainAgentsNeverProbeTheRuntime`,
     with its assertions and a plain agent.
   - `TestAHostOnlyFleetAnswers501ForContainerOnlyOperations` becomes
     `TestNoRuntimeAnswers501ForContainerOnlyOperations`, with a probe that
     answers none in place of the switch.
   - Rename the file to `placement_test.go`.

**Verify:**
- `go test ./web/bridge -count=1` passes in full.
- `grep -rn 'hostOnly\|SetHostProcessesOnly' web/ cmd/` prints nothing.

## Task 7: `webbridge` is one daemon

**Goal:** `webbridge` always applies the local profile's defaults and runs its
behaviour. `--profile local` is accepted and ignored, `--profile server` is
refused, egress defaults off, and a daemon given no `--state-volume` keeps its
state on the host.

**Files:** `cmd/webbridge/main.go`, `cmd/webbridge/main_test.go`,
`cmd/webbridge/local_test.go`.

**Steps:**
1. Keep the `--profile` flag and `WEBBRIDGE_PROFILE`, with the help text
   "retired: local is accepted and ignored". The `config.profile` field goes.
   - `local`, or unset: log a warning once, "webbridge: --profile is retired;
     the flag is ignored", but only when it was given.
   - Any other value: refuse with `--profile %q: the server profile is
     retired; every webbridge is the daemon (docs/one-daemon/design.md)`.
2. Rename `applyProfile` (`:307`) to `applyDaemonDefaults` and reduce it to:
   - the `--profile` rule from step 1;
   - the negative idle-timeout refusal (`:351–353`);
   - the two token refusals for `--addr off` with no join (`:345–350`), kept
     until plan 3;
   - the socket, state-dir and workspace defaults (`:355–369`).

   Delete:
   - the `server` case's refusals (`:309–322`) and the `switch` around them;
   - the egress and secrets refusals (`:328–340`).
3. Change `--egress`'s default (`:175`) to `off`. `WEBBRIDGE_EGRESS` still
   overrides it. Delete the local branch's egress assignment (`:335–337`),
   which is now the default.
4. Change `--state-volume`'s default (`:162`) to empty, and its help to "named
   volume holding the state, for a containerized bridge (default: state on
   the host)".
5. Delete the working-directory project (`:258–266`). `cwdRoot` is empty
   unless given. If `DefaultWorkspacePath` (`web/bridge/workspace.go:351`) has
   no caller left, delete it and its test.
6. In `run`, every `cfg.profile == "local"` condition (`:779`, `:853`, `:923`,
   `:959`) is now true:
   - The project environment, the idle and restart sweeps, the pinned
     projects, the socket, `DaemonInfo` (still `Profile: "local"`), the own
     binary and the refresh helper always run.
   - A Studio port that cannot bind is logged and recorded, never fatal
     (`:923`). Delete the `else` that returned the error.
7. Right after `NewFleet` (`:770`), when `cfg.stateVolume` is empty, call
   `fleet.SetStateOnHost(dir)`. `dir` is `config.UserDir(home)` from
   `marshal/internal/app/config`, with `home` from `os.UserHomeDir()`. When
   the home directory cannot be found, log a warning and pass `""`. Keep the
   call before every start below it, as the comment at `:776–778` asks.
8. Tests:
   - `TestParseConfigDefaults` (`main_test.go:40`) asserts the one daemon's
     defaults: the socket, state dir and workspace under `DefaultDaemonDir`,
     egress `off`, and an empty `cwdRoot`. It no longer reads `profile`.
   - `TestParseConfigStateVolume` (`:315`) expects an empty default.
   - In `local_test.go`, rename `TestTheLocalProfilesDefaults` to
     `TestTheDaemonsDefaults`, and `TestTheLocalProfileRefuses` to
     `TestTheDaemonRefuses`.
   - In the refusal table, delete the rows "egress flag", "egress env",
     "secrets local", "socket without the profile", "addr off without the
     profile", "idle timeout without the profile" and "oauth refresh without
     the profile".
   - "profile cloud" expects `retired`. Add "profile server", which expects
     `retired`.
   - Add an accepted row (empty `want`) for `--profile local` and one for
     `--egress on`.
   - Drop `--profile local` from the other tests' arguments; they need no
     renaming.

**Verify:** `go test ./cmd/webbridge -count=1` passes, and so does
`go build ./cmd/webbridge`.

## Task 8: `marshal daemon install` writes the one daemon's unit

**Goal:** the unit carries no `--profile`, and carries `--egress on` or `off`
from `[daemon] egress`, which defaults to false.

**Files:** `internal/daemon/daemon.go`, `internal/daemon/units_test.go`,
`internal/daemon/studio_test.go`, `internal/app/config/types.go`,
`file_types.go`, `merge.go`, `save.go`, `internal/app/config/daemon_test.go`,
`cmd/marshal/daemon.go` and `cmd/marshal/daemon_test.go`.

Runs after Task 7: the old `webbridge` defaults to the server profile, which
refuses `--socket` in a unit that has no `--profile`.

**Steps:**
1. In `UnitSpec.Args` (`daemon.go:67`):
   - delete `"--profile", "local"`;
   - add `UnitSpec.Egress`;
   - write `"--egress", "on"` or `"--egress", "off"` directly after
     `--idle-session-timeout`.
2. Add `DaemonConfig.Egress` (`types.go:437`), with the comment "runs the
   egress proxy sidecar for container agents; needs a container runtime.
   Default false." Add `fileDaemon.Egress` (`file_types.go:169`).
3. In `merge.go`, add `set(&cfg.Daemon.Egress, file.Daemon.Egress)` inside the
   `file.Daemon != nil` block (`:403`). In `save.go:335`, add `Egress:
   strutil.Ptr(cfg.Daemon.Egress)`.
4. In the spec that `cmd/marshal/daemon.go:295` returns, add `Egress:
   cfg.Daemon.Egress`.
5. Tests:
   - `TestThePlistNamesEverything` and `TestTheSystemdUnitNamesEverything`
     (`units_test.go:39`, `:131`) expect no `--profile` and expect
     `--egress off`.
   - Any expected argument list in `studio_test.go` drops `--profile local`.
   - New `TestTheUnitTurnsEgressOn` (`units_test.go`): `Egress: true` writes
     `--egress on`.
   - New `TestDaemonEgressIsReadFromTheUserConfig` and
     `TestDaemonEgressRoundTripsThroughASave` (`daemon_test.go` in config), as
     the idle-timeout tests at `:23` and `:78` do.
   - New `TestDaemonInstallWritesEgress` (`cmd/marshal/daemon_test.go`), as
     `TestDaemonInstallWritesTheIdleTimeout` (`:313`) does.

**Verify:** `go test ./internal/daemon ./internal/app/config ./cmd/marshal -count=1`
passes.

## Task 9: The deployment files and the docs follow

**Goal:** `compose.yaml` and `scripts/dev-studio.sh` run on the new
`webbridge`, and the docs stop describing two profiles.

**Files:** `compose.yaml`, `scripts/dev-studio.sh`, `AGENTS.md`,
`cmd/webbridge/README.md`, `docs/dev-loop.md`.

**Steps:**
1. `compose.yaml`: add `WEBBRIDGE_STATE_VOLUME: "marshal-state"` and
   `WEBBRIDGE_EGRESS: "on"` to the bridge's `environment`. These keep today's
   behaviour until plan 4 retires the file.
2. `scripts/dev-studio.sh`:
   - In the `ExecStart` line (`:281`), drop `"--profile" "server"`. Add
     `"--socket" "$studio_dir/marshal.sock"`, so it does not collide with the
     daemon's socket, and `"--egress" "on"`.
   - Delete `resolve_state_dir`, its call and the comment block above it
     (`:196–214`). `state_dir` stays `$studio_dir/state`.
   - Rewrite the header's two reasons (`:8–16`) as one. The studio is a
     second daemon only because it serves the browser on `127.0.0.1:7700`
     with its own token, and restarts apart from the TUI's daemon. Containers
     now run on any daemon.
   - Change the message at `:176` to "run webbridge yourself elsewhere".
3. `AGENTS.md:87–88`: replace "`webbridge --profile local` is the user's local
   daemon (P4): a unix socket, host processes only, and no container
   machinery." with "`webbridge` is the user's daemon (P4; one-daemon D1): a
   unix socket, and agents as host processes unless they have a workspace,
   which runs them in a container (a rootless runtime on Linux, or Docker
   Desktop)."
4. `cmd/webbridge/README.md:46–53`:
   - The heading "The local profile" becomes "The daemon".
   - The sentence after it says the daemon's agents are host processes unless
     they have a workspace.
   - The example drops `--profile local`.
   - Add one line: `--profile` is retired; `local` is ignored and `server` is
     refused.
5. `docs/dev-loop.md:110–113`: replace the first sentence with "A daemon runs
   an agent in a container when the agent has a workspace, mounting its state
   from the daemon's state directory; on Linux that needs rootless Podman or
   Docker." Delete the `SetHostProcessesOnly` clause.

**Verify:**
- `docker compose config` or `podman compose config` parses `compose.yaml`,
  if either is installed.
- `bash -n scripts/dev-studio.sh` passes.
- `grep -n -- '--profile' AGENTS.md cmd/webbridge/README.md docs/dev-loop.md scripts/dev-studio.sh compose.yaml`
  prints only the README's retirement line.

## Task 10: Full verification

**Steps:**
1. `gofmt -l .` prints nothing, and `go vet ./...` passes.
2. `CGO_ENABLED=1 go build ./cmd/marshal ./cmd/webbridge` succeeds.
3. `scripts/test.sh` (the packages the diff affects) passes. Compare it
   against the baseline at the top of this plan.
4. `go test ./web/bridge -run TestWebIsStdlibOnly -count=1` passes, because
   `web/` still imports nothing internal. The new import is in
   `cmd/webbridge`.
5. **By hand on macOS.** Run `marshal daemon install` with this build, then
   `marshal daemon status`:
   - The daemon answers.
   - A plain spawn on a project runs on the project's child.
   - With Docker Desktop running, a spawn with a built workspace starts a
     container whose agent socket appears under
     `<data>/daemon/state/sockets/<id>`.
   - Discarding that agent removes its work and socket directories.
6. **By hand on Linux with rootless Podman.** Run the same workspace spawn.
   Then, with a rootful Docker as the only runtime, the spawn answers 501
   with "runs as root".

## Integration notes

- **The development box's studio.** Its existing agents' state lives in the
  `marshal-state` volume's mountpoint, and after Task 9 the studio starts on
  `$studio_dir/state`. Finish or discard those agents before switching.
- **Plain agents stop running in containers.** A plain agent record that was
  containerized under the server profile is rebuilt as a host process when it
  is reattached. On a box that runs the server profile today, finish its
  container agents first, or give the project a default workspace.
- **Container agents read the user's config read-only.** A provider at
  `localhost` (LM Studio, Ollama) is the container's own `localhost`, not the
  host's. A workspace that needs one must name the host's address. This is not
  new for container agents, but it is new to the host daemon.
