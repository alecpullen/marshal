# Containerized run — findings from the first end-to-end verification

This document records the manual verification campaign for running the
marshal agent inside a container, end to end: the bridge (`webbridge`)
spawning agent containers, the agent image they run on, and everything
built on top of that path — remote git sources, the exit path, MCP
intake, forge integration, and the limits/audit layer.

**Campaign run 2026-08-28** on macOS 15 / arm64, Docker 29.4.3.

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

## Open design question

The ACP endpoint needs a transport that works on both platforms, and TCP
removes the filesystem permission that was its only guard. Options are
recorded for design rather than decided here.

## Resolution — run the bridge in a container too, on named volumes

Proposed after the campaign and **verified experimentally on this host**.
Both blockers are artifacts of the macOS↔VM *file-sharing layer*. They
disappear entirely if the socket never crosses it — which it does not
when the bridge is itself a Linux container and the state lives on a
named volume.

Four experiments, all run on macOS 15 / arm64, Docker 29.4.3:

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
  unverified and must be checked before claiming parity.
- `webbridge` needs its own image and a documented compose deployment.

---

## Detailed results

Checks below retain their original text. Statuses are updated where the
campaign reached them; the remainder are blocked behind BLOCKER 2.

## How to read a finding

Each check carries:

- **Check** — what to do and what to observe.
- **Source** — the sub-project whose design assumed this behaviour. A
  failure here invalidates that sub-project's assumption, not just a
  line of code.
- **Status** — one of `Not yet run`, `Pass`, `Fail`, `Skipped`.
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
- **Not verified:** any of the checks below on a live host — real
  container caps under load, a real bridge restart with surviving
  containers, real credentials against a real remote, a real push, a
  real MCP client, a real forge, real disk accounting against real
  container volumes.
- **Known environment limitation:** on macOS hosts, a Unix socket in a
  host bind-mounted directory cannot be `chmod`ed from inside the
  container (APFS returns EINVAL). The first smoke test hit this; the
  socket works on the container's own filesystem. The campaign must run
  on Linux, or use a named volume for the socket directory.

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
- **Status:** Not yet run
- **Findings:** not yet run

### S1.2 — No host escape

- **Check:** Inspect the running agent container (`docker inspect`).
  Confirm: no `--privileged`, no docker socket mounted, no host
  networking, no host PID/IPC namespace. The bridge's `buildRunArgs`
  deliberately grants none of these; this check confirms the running
  container matches the intent.
- **Source:** S1 (containerized agent runtime).
- **Status:** Not yet run
- **Findings:** not yet run

### S1.3 — Container has git and ca-certificates

- **Check:** Inside the agent container, run `git --version` and confirm
  TLS verification works against a real HTTPS remote (e.g. a
  `git ls-remote https://...`). The default image installs both
  deliberately; a derived image must inherit them.
- **Source:** S0 task 1 (the image) / S1.
- **Status:** Not yet run
- **Findings:** not yet run

---

## Reattach — control-plane restart

Source sub-project: S1 and the S1 completion plan. The agent outlives
the bridge; the bridge must find it again.

### R.1 — Kill the bridge, confirm containers survive

- **Check:** Start the bridge, spawn an agent, kill the bridge process
  (not the container). Confirm the agent container is still running
  (`docker ps`) and its socket still answers.
- **Source:** S1 (containerized agent runtime).
- **Status:** Not yet run
- **Findings:** not yet run

### R.2 — Restart, confirm same container ids

- **Check:** Restart the bridge against the same state directory.
  Confirm it reattaches to the existing containers (same container
  names/ids) rather than starting duplicates. `Open` prefers reattach
  when a container under the agent's name is already running.
- **Source:** S1 / S1 completion plan.
- **Status:** Not yet run
- **Findings:** not yet run

### R.3 — Agent answers with prior context

- **Check:** After reattach, resume the session and confirm the agent
  still knows what it was doing before the restart (the persisted
  session id is restored and `session/resume` re-syncs state).
  Notifications emitted while detached are dropped by design; the
  re-sync is what must work.
- **Source:** S1 completion plan (Resume restores the ACP session).
- **Status:** Not yet run
- **Findings:** not yet run

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
- **Status:** Not yet run
- **Findings:** not yet run

### S2a.2 — Origin points at the real remote

- **Check:** After the same spawn, `git remote -v` inside the container
  shows the real remote URL (the bridge repoints origin after cloning
  from the mirror), so a push from the exit path goes to the real
  server, not the mirror path.
- **Source:** S2a (remote sources).
- **Status:** Not yet run
- **Findings:** not yet run

### S2a.3 — Two agents share one mirror

- **Check:** Spawn two agents against the same repo. Confirm one bare
  mirror is created (one directory under the state dir's `repos/`) and
  the second agent's clone is served from it without a second full
  clone. Concurrent spawns must not race on the mirror (per-URL mutex).
- **Source:** S2a (remote sources).
- **Status:** Not yet run
- **Findings:** not yet run

### S2a.4 — A planted hook does not fire on the host

- **Check:** Register a repo whose tree contains a `.git/hooks` script
  (or plant one via a commit) that writes a marker file outside the
  workspace. Spawn from it and run a git operation that would normally
  fire the hook. Confirm the marker never appears on the host: the
  bridge's git invocations run with `core.hooksPath=/dev/null` and
  `protocol.ext.allow=never`, and the agent's own git runs inside the
  container, not on the host.
- **Source:** S2a (remote sources).
- **Status:** Not yet run
- **Findings:** not yet run

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
- **Status:** Not yet run
- **Findings:** not yet run

### S2b.2 — Override exercises the push path

- **Check:** On the same blocked exit, supply a gate override with a
  reason. Confirm the push proceeds, the override is recorded on the
  agent and in the audit log, and the PR body states the override.
- **Source:** S2b (exit path).
- **Status:** Not yet run
- **Findings:** not yet run

### S2b.3 — Derived image with a toolchain: real gate pass

- **Check:** Declare a per-project image (`.devcontainer/devcontainer.json`
  with a base like `golang:1.x`) so the bridge derives an image that
  carries marshal on top of the toolchain. Run the exit path and confirm
  the gate actually runs the build/test commands and passes on real
  success. Also confirm a real failure fails the gate (not skipped).
- **Source:** S2b (exit path) / S0 task 5 (derived images).
- **Status:** Not yet run
- **Findings:** not yet run

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
- **Status:** Not yet run
- **Findings:** not yet run

### S2c-1.2 — A submission queues

- **Check:** With a valid non-autonomous client token, submit a spawn
  via `tools/call`. Confirm the response is a pending id (status
  `pending`), the submission appears in the pending list, and nothing
  runs until an operator approves it. Confirm per-client caps and the
  registered-repo allowlist are enforced on the same path.
- **Source:** S2c-1 (MCP intake).
- **Status:** Not yet run
- **Findings:** not yet run

### S2c-1.3 — An approved plan lands inside the container

- **Check:** Submit a plan (not a prompt), approve it, and confirm the
  plan file appears at `/work/.marshal/intake/<pending-id>.md` inside
  the container and the agent starts executing it (`session/sdd_start`
  with the in-container path). A hostile pending id must not be able to
  steer the write path.
- **Source:** S2c-1 (MCP intake).
- **Status:** Not yet run
- **Findings:** not yet run

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
- **Status:** Not yet run
- **Findings:** not yet run

### S2c-2.2 — Issue intake

- **Check:** Register a repo with a watch label. Label an issue. Confirm
  the poller picks it up (default interval 5 minutes), submits it
  through the same intake seam as MCP (confirmation, caps, allowlist),
  and does not resubmit it on the next poll (dedup by issue number, not
  the `since` cursor). Confirm a rate-limited forge backs off rather
  than hammering the API.
- **Source:** S2c-2 (forge issues).
- **Status:** Not yet run
- **Findings:** not yet run

### S3a.1 — Disk accounting against real container volumes

- **Check:** Run agents until the state directory holds real mirrors and
  work trees. Confirm the reported disk usage (repos + work split)
  matches `du` on the host, and that a spawn over the configured
  `--max-disk-mb` budget is refused after a prune attempt, not before
  reclaiming.
- **Source:** S3a (limits and audit).
- **Status:** Not yet run
- **Findings:** not yet run

### S3a.2 — Pruning with a live agent

- **Check:** With one live agent and one finished agent (plus an
  unreferenced mirror), run prune. Confirm the finished agent's work
  tree and the unreferenced mirror are removed, the live agent's
  workspace and its mirror are untouched, and the reclaimed byte count
  is reported (and audited).
- **Source:** S3a (limits and audit).
- **Status:** Not yet run
- **Findings:** not yet run

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