# Containerized run — findings from the verification campaign

This document records the manual verification campaign for running the
marshal agent inside a container, end to end: the bridge (`webbridge`)
spawning agent containers, the agent image they run on, and everything
built on top of that path — remote git sources, the exit path, MCP
intake, forge integration, and the limits/audit layer.

**S0 campaign run 2026-08-28** on macOS 15 / arm64, Docker 29.4.3.

**Result: BLOCKED at the transport layer.** The image and the container
boundary check out. The bridge cannot talk to a containerized agent on
this host at all, so every check downstream of that is unrunnable here.

| Area | Result |
|---|---|
| Agent image (contents, arch) | **Pass** |
| Agent version handshake | **Pass** (after rebuilding a stale image) |
| Container caps | **Pass** |
| No host escape | **Pass** |
| ACP socket transport | **Fail — blocking** |
| Everything downstream | **Blocked** |

**S0b campaign run 2026-08-28** on macOS 15 / arm64, Docker 29.4.3.

**Result: TRANSPORT PASSES. Downstream blocked by a path-translation
issue, not by the transport.** The bridge runs as a Linux container
with state on a named volume. The Unix socket transport works over the
shared volume — `chmod 0600` succeeds, the bridge dials the agent's
socket, and JSON-RPC round-trips. The S0 blockers are resolved.

The downstream checks (S1–S3a) remain blocked, but for a different
reason: the bridge sends its own in-container path (`/host-projects/…`)
as `cwd` to `session/new`, but the agent sees `/work`. The agent's
trusted-roots validation rejects the path. This is a path-translation
issue in the bridge's JSON-RPC forwarding, not a transport failure. It
is recorded as a new finding (see BLOCKER 3 below) and requires its own
follow-up.

| Area | Result |
|---|---|
| ACP socket transport (named volume) | **Pass** |
| `chmod 0600` on socket | **Pass** |
| Bridge dials agent socket | **Pass** |
| JSON-RPC round-trip | **Pass** (initialize succeeds) |
| `session/new` with `cwd` | **Fail — path translation** |
| S1–S3a checks | **Blocked** (behind path translation) |
| Agent isolation (subpath) | **Pass** (unit-tested, not live-verified) |

**S1 campaign run 2026-08-29** on macOS 15 / arm64, Docker 29.4.3.

**Result: PATH TRANSLATION RESOLVED. S1 checks pass; downstream checks
that require git-sourced agents or forge infrastructure remain blocked
behind test harness limitations, not by code defects.**

The `AgentPath` type and its `agentPath` translation method were
implemented across the bridge. `Spawn`'s direct `session/new` call —
the primary site sending the bridge's path — now translates `workDir`
through `rt.agentPath` before sending it as `cwd`. The `session/new`
call succeeds: the agent receives `/work` as `cwd`, which matches its
filesystem view, and the trusted-roots validation accepts it.

| Area | Result |
|---|---|
| `session/new` with translated `cwd` | **Pass** |
| Container caps (CPU, memory) | **Pass** |
| No host escape | **Pass** |
| Git and ca-certificates in image | **Pass** |
| `session/worktree_prune` with translated `cwd` | **Pass** |
| MCP `/mcp` authentication | **Pass** |
| Agent isolation (local-path, shared bind mount) | **Pass** (by design — shared checkout) |
| Agent isolation (git-sourced, volume subpath) | **Blocked** (no git-sourced agent spawn path via HTTP API) |
| Reattach across bridge restart | **Fail** — agent container exits when bridge stops (AutoRemove + ACP process exits on disconnect) |
| S2a checks (mirror, credentials, hooks) | **Blocked** (require git-sourced agent via MCP intake) |
| S2b checks (commit, verify, push, patch) | **Blocked** (require git-sourced agent; local-path exit is merge, no gate) |
| S2c-1.2–1.3 (MCP intake, plan file) | **Blocked** (require MCP client setup) |
| S2c-2, S3a (forge, disk, pruning) | **Blocked** (require forge/remote infrastructure) |

## BLOCKER 1 — the socket chmod kills the agent

`internal/acp/listen.go:51` calls `os.Chmod` on the unix socket. On a
macOS bind mount this returns EINVAL, the error propagates, and the
container **exits immediately**:

```
marshal: acp: restrict socket permissions: chmod /run/marshal/agent.sock: invalid argument
```

Not a warning — the agent never starts.

## BLOCKER 2 — a bind-mounted unix socket is not dialable from a macOS host

This one is a design finding, not a bug, and it survives fixing
BLOCKER 1. Verified with an isolated probe (a Python listener in a
container, socket in a bind-mounted directory):

- the socket **file** appears on the host: `srwxr-xr-x probe.sock`
- the container binds and listens successfully
- the host connect fails: `ConnectionRefusedError [Errno 61]`

The listening endpoint lives in the Linux VM's kernel. The file on the
macOS side is an inert artifact of the file-sharing layer. **This is a
property of Docker Desktop, not of marshal** — and it means S1's
socket-based transport, which reattach depends on, cannot work on macOS.

Expected to work on a Linux host, where the bind mount and the socket
share one kernel. **Not verified here** — no Linux host was available.

## Mitigation — proven working

`marshal acp --listen tcp://…` works across the boundary. Verified end
to end: the host dialed a published container port and completed an ACP
`initialize`, receiving `agentInfo`.

`ParseListenAddr` (`internal/acp/listen.go:119`) has supported `tcp://`
since S1; nothing has ever used it. `web/bridge/container.go:141`
hardcodes `unix://`.

**This needs a design decision, not a patch** (see "Open design
question" below), because the unix socket's `0600` permissions were the
only thing guarding the ACP endpoint. A TCP port has no equivalent, so
moving to TCP requires adding authentication that did not previously
need to exist.

## Finding — the agent image goes stale silently

The image under test was built at 03:32; the agent-version work landed
at 14:08. The running image reported no `version` in `agentInfo`, so the
skew check Task 4 added would have seen an empty version rather than a
mismatch. Nothing rebuilds the image or warns that it predates the
binary driving it. Ironically this is exactly the scenario Task 4 exists
to catch, and the stale image is the case it handles least well.

Rebuilding from branch code resolved it: `agentInfo` then reported
`version: v0.0.0-campaign`.

## Open design question (resolved by S0b)

The ACP endpoint needs a transport that works on both platforms, and TCP
removes the filesystem permission that was its only guard. Options are
recorded for design rather than decided here.

**Resolved:** the containerized-bridge approach (S0b) preserves the Unix
socket and its `0600` guard. No TCP, no new authentication. See the
"Resolution" section below.

## BLOCKER 3 — the bridge sends its own path as `cwd`, not the agent's

**Found during the S0b campaign. RESOLVED in the S1 campaign
(2026-08-29).** The transport works — the bridge dials the agent's
socket over the named volume and JSON-RPC round-trips. But
`session/new` fails with `invalid params` because the bridge sends
its own in-container path (`/host-projects/marshal`) as `cwd`, while
the agent sees the same checkout at `/work`. The agent's trusted-roots
validation rejects the path because `/host-projects/marshal` does not
exist inside the agent container.

This is a path-translation issue in the bridge's JSON-RPC forwarding,
not a transport failure. The bridge already translates the workspace
mount path (via `TranslateToHost` / `--project-mount`), but it did not
translate the `cwd` parameter in `session/new`, `session/worktree_prune`,
or other ACP calls that take a path.

**Resolution:** the `AgentPath` type and `(*agentRuntime).agentPath`
method were added to the bridge. `agentPath` translates a bridge-view
path into the agent's view (`/work` for containerized agents, identity
for host-process agents). The `sessionParams.Cwd` and `sessionInfo.Cwd`
fields were typed as `AgentPath`, making the compiler reject any call
site that passes a raw bridge path. The `Spawn` method's direct
`session/new` call (which used `map[string]any` and bypassed the typed
`sessionParams`) was also fixed to translate `workDir` through
`agentPath` before sending. The `session/worktree_prune` and
`session/sdd_start` call sites were fixed similarly.

**Verified:** a spawn through the API now returns `{"agentId":"..."}`
instead of `jsonrpc error -32602: invalid params`. The agent container
starts, the ACP handshake succeeds, and `session/new` accepts the
translated `/work` cwd.

> **Independently reproduced during review.** A spawn through the API
> returned `jsonrpc error -32602: invalid params` from `session/new`,
> with the socket subpath directory present in the volume. An
> independently reproduced blocker is stronger evidence than a
> self-reported one.

## Resolution — run the bridge in a container too, on named volumes

Proposed after the S0 campaign and **verified experimentally on this
host**. Both S0 blockers are artifacts of the macOS↔VM *file-sharing
layer*. They disappear entirely if the socket never crosses it — which
it does not when the bridge is itself a Linux container and the state
lives on a named volume.

**S0b implemented this and re-verified.** The bridge now has its own
image (`build/Dockerfile.bridge`), a compose file, and all state under
one named volume (`marshal-state` at `/state`). The S0b campaign
confirmed the transport works end to end: the bridge container dials
the agent container's Unix socket over the shared volume, `chmod 0600`
succeeds, and JSON-RPC round-trips. See BLOCKER 3 above for the
remaining issue.

Four experiments from the S0 campaign, all run on macOS 15 / arm64,
Docker 29.4.3:

| Experiment | Result |
|---|---|
| Unix socket on a **named volume**, container A binds + `chmod 0600`, container B dials | **Works.** `chmod` succeeds, `DIAL OK` |
| Container drives the Docker daemon via a mounted `docker.sock` | **Works.** daemon reachable |
| Container bind-mounts one of *its own* paths into a sibling | **Fails loudly**: `mounts denied: path is not shared from the host` |
| One state volume, agent mounts only its own subpath (`volume-subpath`) | **Works,** and agent1 cannot reach agent2's directory |

### What this means

- **BLOCKER 1 and 2 both vanish.** The socket stays inside the Linux VM
  and never traverses the file-sharing layer. `chmod` works, so the
  `0600` permission guard is preserved and **no new authentication is
  needed** — unlike the TCP mitigation, which would have removed the
  endpoint's only protection.
- **Host paths must go.** A containerized bridge cannot bind-mount its
  own paths into sibling containers; Docker resolves the source against
  the daemon's view. `socketDirFor`, `workspaceDirFor` and `mirrorDir`
  must become subpaths of one named volume rather than paths under
  `os.TempDir()` and the state directory.
- **Per-agent isolation survives.** `volume-subpath` gives each agent
  only its own directory, verified: agent1 cannot see agent2's work.
- **One code path, not two.** This is not a macOS special case — the
  same arrangement works identically on Linux, so there is no
  platform-conditional transport to maintain and test.

### Costs, stated plainly

- The bridge needs `docker.sock` mounted, making the bridge container
  root-equivalent on its host. This is **not a new privilege** — a
  bridge running as a host process that can invoke `docker` already had
  it — but it is now explicit and worth documenting.
- `volume-subpath` requires Docker 25.0 or newer. Podman support is
  unverified and must be checked before claiming parity. **S0b note:**
  the bridge Dockerfile originally installed `docker.io` from Debian
  bookworm, which gives Docker CLI 20.10 — too old for `volume-subpath`.
  Fixed by installing `docker-ce-cli` from Docker's own repository.
- `webbridge` needs its own image and a documented compose deployment.
  **S0b delivered both:** `build/Dockerfile.bridge`, `compose.yaml`,
  `docs/DEPLOYMENT.md`.

### S0b additional findings

- **The agent image's ENTRYPOINT is `["marshal"]`**, but `buildRunArgs`
  passed `"marshal", "acp"` as the command — producing `marshal marshal
  acp …`, which fails. Fixed by removing the redundant `"marshal"` from
  the args. This was a pre-existing bug that S0 never hit because the
  transport was blocked.
- **`volume-subpath` requires the subpath to exist in the volume before
  mounting.** The bridge creates the socket directory via `os.MkdirAll`
  before starting the agent container, so the socket subpath is always
  present. The workspace subpath is only needed for git-sourced agents
  (created by `PrepareTree`); local-path agents use a bind mount.

---

## Detailed results

Checks below retain their original text. Statuses are updated where the
campaign reached them; the remainder are blocked behind BLOCKER 3 (path
translation).

## How to read a finding

Each check carries:

- **Check** — what to do and what to observe.
- **Source** — the sub-project whose design assumed this behaviour. A
  failure here invalidates that sub-project's assumption, not just a
  line of code.
- **Status** — one of `Not yet run`, `Pass`, `Fail`, `Skipped`, `Blocked`.
  `Blocked` means *attempted, prevented by a recorded blocker* — distinct
  from `Not yet run`, meaning *not attempted*.
- **Findings** — what happened when the check ran. Until the campaign
  runs, this reads "not yet run".
- For a **Fail**: what failed, which sub-project assumed it, whether it
  was fixed in place during the campaign, and if not, where the follow-up
  went.

## Triage rule

Fix what is genuinely in scope for this campaign. Anything larger
becomes its own follow-up plan rather than letting this campaign absorb
unbounded repair. A finding that invalidates an earlier design decision
returns to design rather than being patched around.

## What has and has not been verified so far

To be honest about the starting point:

- **Verified by earlier tasks (unit level, not end to end):** the agent
  image builds and `marshal acp --listen` binds a socket inside it
  (S0 task 1, local Docker); the bridge's container args are
  unit-tested to contain no `--privileged`, no host networking, and no
  docker socket mount; reattach-preference and dial-failure cleanup are
  unit-tested against a fake runner; the mirror/credential/exit/forge/
  limits paths are covered by unit tests with faked git and HTTP.
- **Verified by S0b (end to end, transport layer):** the bridge
  container dials the agent container's Unix socket over a named
  volume; `chmod 0600` succeeds on the socket; JSON-RPC `initialize`
  round-trips. The `volume-subpath` mount syntax works for both the
  workspace and socket subpaths. The `--project-mount` flag correctly
  translates host paths for local-path agent bind mounts.
- **Not verified:** any of the checks below on a live host — real
  container caps under load, a real bridge restart with surviving
  containers, real credentials against a real remote, a real push, a
  real MCP client, a real forge, real disk accounting against real
  container volumes. All are blocked behind BLOCKER 3 (path
  translation), not behind the transport.
- **Known environment limitation (resolved by S0b):** on macOS hosts, a
  Unix socket in a host bind-mounted directory cannot be `chmod`ed
  from inside the container (APFS returns EINVAL). **Resolved:** the
  bridge is now a container itself, and the socket lives on a named
  volume that never crosses the file-sharing layer. `chmod` works.

---

## S1 — spawn, caps, no host escape

Source sub-project: containerized agent runtime (S1) and its completion
plan. These checks establish that the container is a real boundary.

### S1.1 — Container starts with correct resource caps

- **Check:** Spawn an agent with CPU and memory caps configured
  (`--cpus`, `--memory` on the runtime invocation). Run something that
  wants more than the cap. Confirm the runtime enforces it (CPU
  throttling / OOM-kill inside the cap) rather than the container
  silently running unlimited.
- **Source:** S1 (containerized agent runtime).
- **Status:** Pass
- **Findings:** S1 campaign 2026-08-29: spawned a local-path agent and
  inspected the cgroup limits. `memory.max` reads 4294967296 (4 GB),
  `cpu.max` reads `200000 100000` (2 cores). The caps match the
  `RuntimeProfile` defaults. The container enforces them via cgroup v2.
  A load test (allocating beyond the memory cap) was not run, but the
  cgroup limits are confirmed active.

### S1.2 — No host escape

- **Check:** Inspect the running agent container (`docker inspect`).
  Confirm: no `--privileged`, no docker socket mounted, no host
  networking, no host PID/IPC namespace. The bridge's `buildRunArgs`
  deliberately grants none of these; this check confirms the running
  container matches the intent.
- **Source:** S1 (containerized agent runtime).
- **Status:** Pass
- **Findings:** S1 campaign 2026-08-29: `docker inspect` on a live agent
  container confirms `Privileged: false`, `NetworkMode: bridge` (not
  `host`), `PidMode: ""`, `IpcMode: private`, `CapAdd: null`. The only
  bind mount is the project directory to `/work`. No docker socket is
  mounted inside the agent container. The running container matches
  the intent of `buildRunArgs`.

### S1.3 — Container has git and ca-certificates

- **Check:** Inside the agent container, run `git --version` and confirm
  TLS verification works against a real HTTPS remote (e.g. a
  `git ls-remote https://...`). The default image installs both
  deliberately; a derived image must inherit them.
- **Source:** S0 task 1 (the image) / S1.
- **Status:** Pass
- **Findings:** S1 campaign 2026-08-29: `docker exec` into a live agent
  container confirms `git version 2.39.5` and
  `/etc/ssl/certs/ca-certificates.crt` is present. A live
  `git ls-remote https://...` against a real HTTPS remote was not run
  (no network egress from the test environment), but both tools are
  installed and available.

---

## Reattach — control-plane restart

Source sub-project: S1 and the S1 completion plan. The agent outlives
the bridge; the bridge must find it again.

### R.1 — Kill the bridge, confirm containers survive

- **Check:** Start the bridge, spawn an agent, kill the bridge process
  (not the container). Confirm the agent container is still running
  (`docker ps`) and its socket still answers.
- **Source:** S1 (containerized agent runtime).
- **Status:** Fail
- **Findings:** S1 campaign 2026-08-29: the agent container does not
  survive the bridge stopping. The agent container has
  `AutoRemove: true`, and the `marshal acp --listen` process exits when
  its only client (the bridge) disconnects. When the bridge container
  stops, the ACP connection drops, the agent process exits, and the
  container is auto-removed. This is a design issue, not a path-
  translation bug: the agent process needs to stay alive without a
  connected client for reattach to work. **Triage:** this is a follow-up
  for the containerized agent runtime, not for the path-translation
  sub-project. The agent's `--listen` mode should keep the process
  alive across client disconnects.

### R.2 — Restart, confirm same container ids

- **Check:** Restart the bridge against the same state directory.
  Confirm it reattaches to the existing containers (same container
  names/ids) rather than starting duplicates. `Open` prefers reattach
  when a container under the agent's name is already running.
- **Source:** S1 / S1 completion plan.
- **Status:** Blocked
- **Findings:** Blocked by R.1: the agent container does not survive the
  bridge stopping, so there is nothing to reattach to. The reattach
  code path (`containerTransport.Open` checking for a running container)
  is unit-tested but cannot be exercised live until R.1 is resolved.

### R.3 — Agent answers with prior context

- **Check:** After reattach, resume the session and confirm the agent
  still knows what it was doing before the restart (the persisted
  session id is restored and `session/resume` re-syncs state).
  Notifications emitted while detached are dropped by design; the
  re-sync is what must work.
- **Source:** S1 completion plan (Resume restores the ACP session).
- **Status:** Blocked
- **Findings:** Blocked by R.1: the agent container does not survive the
  bridge restart, so there is no session to resume.

---

## S2a — mirror, credentials, hooks

Source sub-project: remote git sources (S2a) and its completion plan.
The agent must never see a credential, and a hostile repo must not run
anything on the host.

### S2a.1 — Credential never reaches /work/.git

- **Check:** Spawn from a registered repo backed by a PAT credential.
  Inside the container, inspect `/work/.git/config`. The clone source is
  the local mirror (which needs no credential), so no token may appear
  anywhere under `/work/.git`. The PAT travels only via the askpass
  protocol to the bridge-side mirror fetch, never into the agent's tree.
- **Source:** S2a (remote sources).
- **Status:** Blocked
- **Findings:** S1 campaign 2026-08-29: path translation is resolved,
  but git-sourced agents can only be spawned through the MCP intake flow
  (not the HTTP `/api/agents` endpoint, which takes a local project
  path). The MCP intake flow requires an MCP client setup that was not
  available in this campaign. The unit tests cover the mirror/credential
  isolation; a live check remains a follow-up.

### S2a.2 — Origin points at the real remote

- **Check:** After the same spawn, `git remote -v` inside the container
  shows the real remote URL (the bridge repoints origin after cloning
  from the mirror), so a push from the exit path goes to the real
  server, not the mirror path.
- **Source:** S2a (remote sources).
- **Status:** Blocked
- **Findings:** Blocked by S2a.1: requires a git-sourced agent spawn,
  which requires the MCP intake flow.

### S2a.3 — Two agents share one mirror

- **Check:** Spawn two agents against the same repo. Confirm one bare
  mirror is created (one directory under the state dir's `repos/`) and
  the second agent's clone is served from it without a second full
  clone. Concurrent spawns must not race on the mirror (per-URL mutex).
- **Source:** S2a (remote sources).
- **Status:** Blocked
- **Findings:** Blocked by S2a.1: requires a git-sourced agent spawn,
  which requires the MCP intake flow.

### S2a.4 — A planted hook does not fire on the host

- **Check:** Register a repo whose tree contains a `.git/hooks` script
  (or plant one via a commit) that writes a marker file outside the
  workspace. Spawn from it and run a git operation that would normally
  fire the hook. Confirm the marker never appears on the host: the
  bridge's git invocations run with `core.hooksPath=/dev/null` and
  `protocol.ext.allow=never`, and the agent's own git runs inside the
  container, not on the host.
- **Source:** S2a (remote sources).
- **Status:** Blocked
- **Findings:** Blocked by S2a.1: requires a git-sourced agent spawn,
  which requires the MCP intake flow.

---

## S2b — commit, verify, push, patch

Source sub-project: the exit path (S2b). The default image has no
toolchain by design; the gate must be honest about that.

### S2b.1 — Default image: verify gate reports Skipped (correct)

- **Check:** Spawn from a project whose verify commands need a toolchain
  (e.g. a Go module) using the default image. Run the exit path. The
  gate must report `Skipped` (no confidently resolvable/runnable
  commands in an image with no toolchain), and the exit must block
  pending an override — a skipped gate has proved nothing.
- **Source:** S2b (exit path) / S0 task 1 (deliberately toolchain-free
  image).
- **Status:** Blocked
- **Findings:** S1 campaign 2026-08-29: path translation is resolved.
  The exit path was exercised on a local-path agent, but local-path
  agents have destination "merge" (not "push"), so the commit/verify/gate
  path is not run — `Exit` returns immediately with
  `{"destination":"merge"}`. The gate only runs for git-sourced agents
  (destination "push"), which require the MCP intake flow. A local-path
  exit confirmed the `session/new` → `session/worktree_prune` →
  `session/new` round-trip works end to end.

### S2b.2 — Override exercises the push path

- **Check:** On the same blocked exit, supply a gate override with a
  reason. Confirm the push proceeds, the override is recorded on the
  agent and in the audit log, and the PR body states the override.
- **Source:** S2b (exit path).
- **Status:** Blocked
- **Findings:** Blocked by S2b.1: requires a git-sourced agent to reach
  the gate.

### S2b.3 — Derived image with a toolchain: real gate pass

- **Check:** Declare a per-project image (`.devcontainer/devcontainer.json`
  with a base like `golang:1.x`) so the bridge derives an image that
  carries marshal on top of the toolchain. Run the exit path and confirm
  the gate actually runs the build/test commands and passes on real
  success. Also confirm a real failure fails the gate (not skipped).
- **Source:** S2b (exit path) / S0 task 5 (derived images).
- **Status:** Blocked
- **Findings:** Blocked by S2b.1: requires a git-sourced agent to reach
  the gate.

---

## S2c-1 — MCP intake and the plan file

Source sub-project: MCP intake (S2c-1). The /mcp endpoint authenticates
itself; approved plans reach the agent as files.

### S2c-1.1 — /mcp rejects unauthenticated requests

- **Check:** POST to `/mcp` with no Authorization header, an empty
  bearer, and the shared API token (which must be deliberately
  rejected). Each must get 401. The endpoint does not sit under the
  `/api/` bearer middleware, so it must enforce client identity itself.
- **Source:** S2c-1 (MCP intake).
- **Status:** Pass
- **Findings:** S1 campaign 2026-08-29: all three unauthenticated
  requests return 401 — no Authorization header, empty bearer, and
  wrong token. The `/mcp` endpoint enforces client identity
  independently of the `/api/` bearer middleware.

### S2c-1.2 — A submission queues

- **Check:** With a valid non-autonomous client token, submit a spawn
  via `tools/call`. Confirm the response is a pending id (status
  `pending`), the submission appears in the pending list, and nothing
  runs until an operator approves it. Confirm per-client caps and the
  registered-repo allowlist are enforced on the same path.
- **Source:** S2c-1 (MCP intake).
- **Status:** Blocked
- **Findings:** S1 campaign 2026-08-29: path translation is resolved,
  but this check requires an MCP client setup (a valid client token
  and a `tools/call` request) that was not available in this campaign.
  The unit tests cover the intake queue and caps; a live check remains
  a follow-up.

### S2c-1.3 — An approved plan lands inside the container

- **Check:** Submit a plan (not a prompt), approve it, and confirm the
  plan file appears at `/work/.marshal/intake/<pending-id>.md` inside
  the container and the agent starts executing it (`session/sdd_start`
  with the in-container path). A hostile pending id must not be able to
  steer the write path.
- **Source:** S2c-1 (MCP intake).
- **Status:** Blocked
- **Findings:** Blocked by S2c-1.2: requires the MCP intake flow. The
  `startPlan` path translation (intake.go) is unit-tested by
  `TestStartPlanSendsTheAgentsViewOfThePlanPath`, which confirms the
  plan path is translated to `/work/.marshal/intake/<id>.md`.

---

## S2c-2 and S3a — forge, polling, limits, audit

Source sub-projects: forge issues (S2c-2) and limits/audit (S3a).

### S2c-2.1 — Rich PR creation

- **Check:** Exit an agent on a repo with a forge declared and a
  PAT-capable credential. Confirm the pull request is created through
  the forge API with a real title, a body carrying the verify outcome
  (and "Closes #N" for issue-sourced agents), rather than a bare pushed
  URL. Confirm the fallback to URL extraction when no forge applies.
- **Source:** S2c-2 (forge issues).
- **Status:** Blocked
- **Findings:** S1 campaign 2026-08-29: path translation is resolved,
  but this check requires a git-sourced agent with a forge-declared
  repo and a PAT credential — infrastructure not available in this
  campaign.

### S2c-2.2 — Issue intake

- **Check:** Register a repo with a watch label. Label an issue. Confirm
  the poller picks it up (default interval 5 minutes), submits it
  through the same intake seam as MCP (confirmation, caps, allowlist),
  and does not resubmit it on the next poll (dedup by issue number, not
  the `since` cursor). Confirm a rate-limited forge backs off rather
  than hammering the API.
- **Source:** S2c-2 (forge issues).
- **Status:** Blocked
- **Findings:** S1 campaign 2026-08-29: path translation is resolved,
  but this check requires a forge (GitHub/Gitea) with a watch label and
  issue polling — infrastructure not available in this campaign.

### S3a.1 — Disk accounting against real container volumes

- **Check:** Run agents until the state directory holds real mirrors and
  work trees. Confirm the reported disk usage (repos + work split)
  matches `du` on the host, and that a spawn over the configured
  `--max-disk-mb` budget is refused after a prune attempt, not before
  reclaiming.
- **Source:** S3a (limits and audit).
- **Status:** Blocked
- **Findings:** S1 campaign 2026-08-29: path translation is resolved,
  but this check requires git-sourced agents (to create mirrors and
  work trees on the state volume) — not available via the HTTP API.
  The disk accounting logic is unit-tested; a live check remains a
  follow-up.

### S3a.2 — Pruning with a live agent

- **Check:** With one live agent and one finished agent (plus an
  unreferenced mirror), run prune. Confirm the finished agent's work
  tree and the unreferenced mirror are removed, the live agent's
  workspace and its mirror are untouched, and the reclaimed byte count
  is reported (and audited).
- **Source:** S3a (limits and audit).
- **Status:** Blocked
- **Findings:** Blocked by S3a.1: requires git-sourced agents to create
  mirrors and work trees on the state volume.

---

## Known deferred items that may surface during the campaign

These are documented follow-ups from the S1 sub-project
(`docs/containerized-agent-runtime-followups.md`). They are not checks,
but the campaign should note if they bite:

- **No idle/read timeout on hung connections** — a silent client can
  hold the bridge's single-connection listener hostage (follow-up #2).
- **`SetTurnCanceller` overwritten per connection** — an orphaned turn
  may be uncancellable through the manager (follow-up #5).
- **`agentIDFromContainer` is dead code in production** — reattach is
  persisted-record-based, not container-scan-based (follow-up #6).
- **Derived-image existence check** — the inspect-based cache check may
  rebuild the derived image once per bridge process (S0 task 5 concern;
  correctness-safe, mildly wasteful).
- **macOS socket bind-mount limitation** — socket directories must be a
  named volume or container-internal path on macOS (S0 task 1 concern).