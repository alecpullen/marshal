# Marshal environment foundation — feasibility research

Research date: 2026-11-10 (Australia/Melbourne).

Status: the user approved the recommended foundation on 2026-11-10: Dev Containers and Compose for container environments, native execution where needed, and Marshal-managed workspace lifecycle and host placement. Detailed architecture, runtime compatibility and integration remain to be validated. This is not an implementation plan, benchmark, or integration proof. Research used official upstream documentation and read-only access to Marshal's existing design/deployment documents. No daemon work, repository branches, containers or services were changed.

## Recommendation

Reuse the Dev Containers reference CLI for interpreting and preparing container development environments, Compose for companion services, and the existing Marshal execution boundary for agent sessions. Keep native execution as a distinct supported environment type. Marshal should own workspace identity, durable state, admission/placement, service ownership, UI state and recovery; it should not reimplement a container engine or assume a devcontainer CLI provides those policies.

This fits the approved flows, with three qualifications:

1. **Automatic placement applies most naturally to new workspaces.** Existing workspaces with host-local files or volumes normally resume on their owning host. Moving them is a separate transfer operation.
2. **The visual editor needs a defined editing scope.** Reuse existing configuration without promising lossless visual editing of every Dockerfile, shell script and Compose construct.
3. **Stopping compute and retaining work are different operations.** Preserve files, durable session state and service volumes; stop processes when appropriate. This does not preserve live process memory or guarantee a restartable third-party harness session.

## Existing Marshal foundations and constraints

The [daemon design](https://github.com/alecpullen/marshal/blob/main/docs/daemon/design.md) describes host-process and container deployment profiles, bridge-mediated ACP, server-side checkouts, and per-tree ProjectHost/session separation. It explicitly preserves the server profile's credential and git boundary. Its ProjectHost sharing applies to defined project roots; it is not evidence that arbitrary isolated workspaces share every index and language server. The web client should use service contracts rather than take over agent internals.

The [deployment document](https://github.com/alecpullen/marshal/blob/main/docs/DEPLOYMENT.md) already describes container workspace subpaths, shared bare mirrors, ACP sockets, and Docker/Podman mount differences. This is useful existing infrastructure to evaluate, not a reason to build another independent workspace store. Documentation was read, not verified against a running deployment or a complete code audit.

A concrete topology constraint: the current deployment document requires a Linux-container bridge for containerized agents because its Unix-socket arrangement does not work directly across the macOS host/Linux-container boundary. A native macOS daemon plus Linux development containers cannot simply be assumed to work with the present transport. The future implementation must reconcile native and container execution with the daemon's then-current supported transports, potentially exposing separate eligible hosts to the UI.

The lifecycle/trust boundary also needs deliberate integration. Devcontainer setup can execute host-side initialization, and tool attachment/shutdown conventions differ from an agent that must continue after a browser disconnects. Evaluate repository hooks, mounts and requested privileges against Marshal's existing execution policy; do not blindly apply editor-oriented defaults. [Dev Container metadata reference](https://github.com/devcontainers/spec/blob/main/docs/specs/devcontainerjson-reference.md)

## What can be reused

| Capability | Existing building block | Marshal responsibility |
|---|---|---|
| Environment definition and preparation | Dev Containers CLI; image/Dockerfile and Compose configurations | Resolve a version, report progress, preserve host policy and connect ACP |
| Development dependencies and installation | Existing images, devcontainer Features, lifecycle commands | Show provenance and failures; constrain supported editing |
| Databases, queues and related services | Compose services, networks, volumes and healthchecks | Per-workspace identity; optional shared-service bindings and ownership |
| Suspend/resume compute | Runtime/Compose stop and start | Decide when work is idle; retain durable state and account for other consumers |
| Persistent files/data | Host paths or named volumes; Marshal's existing workspace storage | Stable identities, retention, explicit deletion and recovery |
| Separate branches/checkouts | Existing Marshal git machinery; Git worktrees where appropriate | One active writer/owner policy, branch mapping and credential mediation |
| Resource controls | Runtime CPU/memory limits | Budget whole environments, observe pressure, reserve capacity before launch |
| Several hosts | An environment executor on each eligible daemon host | Admission, capability matching, affinity, failure recovery and a unified UI |

The [Dev Containers CLI](https://github.com/devcontainers/cli) supplies configuration reading, builds, startup, lifecycle commands and command execution. Its inspected README lists stop/down as unfinished, so stopping and deleting must use runtime/Compose lifecycle operations instead of depending on nonexistent CLI commands. This should be rechecked against a pinned release during implementation. The CLI has a Node runtime dependency; that is a setup/helper dependency, not evidence that every active workspace requires another persistent Node control process.

[Compose](https://docs.docker.com/compose/intro/compose-application-model/) provides service/network/volume definitions and deployment grouping. An explicit project name can distinguish copies of the same environment. Naming alone does not neutralize fixed host ports, explicit container names, external networks, explicitly named shared volumes or host paths. Those need validation and overrides or clear user choices. [Project names](https://docs.docker.com/compose/how-tos/project-name/), [volume naming](https://docs.docker.com/reference/compose-file/volumes/)

Service creation is not service readiness. Reuse healthchecks and dependency conditions before reporting an environment ready. [Compose startup order](https://docs.docker.com/compose/how-tos/startup-order/)

## Native execution and runtime choices

Native environments are useful for existing host toolchains and essential when the workload needs platform tooling such as Xcode. Apple's supported Xcode environments are macOS; a Linux devcontainer is not a substitute. [Xcode system requirements](https://developer.apple.com/xcode/system-requirements/)

Native mode should still use a dedicated workspace and explicit environment resolution. It offers weaker process/filesystem isolation than a container and does not automatically provide equivalent resource enforcement. Marshal's daemon design already identifies login-shell/toolchain resolution as important for host processes. Native agents can use separately managed container services if their connections and lifecycle are explicit; that hybrid arrangement still needs a transport test.

The user prefers Podman on their Linux boxes and uses OrbStack on an Apple Silicon M2 Mac mini with 16 GB RAM. They explicitly want the broadest practical runtime compatibility, rather than a design tied to their personal setup.

Recommendation: use a runtime adapter boundary, with Podman on Linux and OrbStack on macOS ARM64 as first validation targets, and Docker Engine/Desktop included in the compatibility matrix from the outset. Keep environment definitions portable and discover actual host capabilities, runtime endpoints and versions. Do not advertise identical Dev Containers behaviour across all engines without validation. Podman's Compose command delegates to an external Compose implementation, so the engine version alone is insufficient to characterize compatibility. On macOS, Podman also uses a Linux VM; switching engines does not eliminate VM overhead. [Podman Compose](https://docs.podman.io/en/latest/markdown/podman-compose.1.html), [Podman installation](https://podman.io/docs/installation)

Proposed compatibility checks should include rootless and rootful Podman, Docker-compatible endpoint selection, Compose implementation/version, UID/GID and file ownership, mounts and named volumes, networking and ports, resource-limit support, lifecycle hooks and ACP connectivity. Rootless/rootful configurations and Linux CPU architectures in the user's fleet are not yet known. An unsupported capability should make the relevant environment ineligible or offer an explicit alternative, rather than silently weaken isolation or change semantics. OrbStack-specific convenience features should remain optional integrations, not requirements in portable environment definitions.

OrbStack documents an unmodified Docker engine, Compose support and compatibility with Dev Containers. This supports reusing the Docker execution adapter rather than inventing a separate environment format for OrbStack. A daemon must resolve the intended Docker context/endpoint under its service identity rather than assume an interactive shell's configuration. End-to-end Marshal compatibility, especially its ACP socket topology, remains untested. OrbStack's Docker compatibility does not establish that macOS can directly dial arbitrary Linux-container Unix sockets. [OrbStack Docker support](https://docs.orbstack.dev/docker/), [OrbStack FAQ](https://docs.orbstack.dev/faq)

## Retention, isolation and resumption

`compose stop` retains containers for restarting. Named volumes outlive a container and allow data to survive replacement. `compose down` removes containers/networks by default; volume deletion is a separate option, and anonymous volumes are a poor durable identity for resumption. Recommended behaviour is an explicit workspace record covering checkout, environment revision, service volumes, host, durable agent-session reference and lifecycle state. [Compose stop](https://docs.docker.com/reference/cli/docker/compose/stop/), [Compose down](https://docs.docker.com/reference/cli/docker/compose/down/), [Docker volumes](https://docs.docker.com/engine/storage/volumes/)

Do not rely on a container's writable layer for the only copy of important state. Rebuilding must retain declared workspace/service data; database-version changes may need migrations or new test volumes. A successful build does not establish data-upgrade compatibility. Optional trials should use separate data unless a deliberate import is requested.

Git worktrees can give separate branches and working directories while sharing repository objects. They do not provide an independent repository/security boundary, and linked worktrees need access to their administrative metadata. Use the existing mediated git model and decide whether worktrees or independent checkouts fit each execution profile; do not mount the whole mirror store into every agent merely for convenience. [Git worktree](https://git-scm.com/docs/git-worktree)

Shared services should be separate managed resources or explicitly external connections, with consumers recorded. Stopping one workspace must not stop a shared database used by another. Sharing a process does not guarantee test isolation; database/schema/key separation depends on the service and workload. Keep isolated services as the default already agreed.

Preserve uncommitted and unpushed work under the normal retention policy. Treat retention as disk usage, not a promise that idle workspaces use no resources at all. Caches, image layers, stopped containers, volumes and indexes all need visibility and separately scoped cleanup. Automatic cleanup must not silently broaden into deleting unrelated runtime resources.

## The 16 GB shared Mac mini case

The number of model sessions alone is a poor capacity limit. A useful budget includes the agent/runtime, language servers and indexing, application services, build/test peaks, container VM allowance, and any local inference. Existing unrelated services must count as external demand. Remote model choice affects API use but does not remove local tool/build costs.

Docker containers are unconstrained by default unless limits are set. Devcontainer host requirements describe minimum needs; they are not a fleet scheduler or guaranteed resource ceiling. Use declared estimates plus observed usage to make admission decisions, and runtime limits where supported. [Docker resource constraints](https://docs.docker.com/engine/containers/resource_constraints/), [devcontainer host requirements](https://github.com/devcontainers/spec/blob/main/docs/specs/devcontainerjson-reference.md#minimum-host-requirements)

The user's runtime is **OrbStack**, which documents on-demand allocation and returning unused memory to macOS. Its configured memory ceiling covers containers and Linux machines together. Marshal should budget within both available host resources and that shared runtime ceiling, accounting for the user's unrelated services; the user's actual ceiling is unknown. These are documented capabilities, not measurements of their Mac mini. [OrbStack efficiency](https://docs.orbstack.dev/efficiency), [OrbStack resource settings](https://docs.orbstack.dev/settings)

The earlier Docker Desktop observation remains relevant to that supported-runtime candidate, not a description of the user's OrbStack setup: Resource Saver stops Docker Desktop's Linux VM when no containers run, so unrelated running services prevent that whole-VM saving. It should not be generalized to OrbStack's memory-reclamation behaviour. [Resource Saver](https://docs.docker.com/desktop/use-desktop/resource-saver/)

Recommendations for constrained hosts:

- Admit conservatively until an environment has measured history; serialize heavy builds initially rather than guessing a universal agent count.
- Budget image builds and optional trials as well as active agents. Dependency installation can be more expensive than steady-state editing.
- Reuse image layers and appropriate package caches while keeping working directories and mutable service data isolated.
- Start only needed supporting services; offer shared services explicitly when users accept their coupling.
- Stop idle compute only after checking active tools, jobs and service consumers. A waiting agent can still have a live test server or subprocess.
- Show retained storage separately from current CPU/RAM use. Preserve override controls.

No numeric concurrency limit or memory saving is claimed: this research did not benchmark the user's hardware or run representative environments.

## Scheduling across hosts

Recommended initial model: place an entire new workspace and its isolated services on one eligible host; expose both compute capability and storage ownership. Split-service placement can be added where explicitly configured, but need not be the default.

Eligibility should account for OS/architecture, native tools, runtime/Compose support, repository and credential access, network reachability, environment requirements and capacity. On an Apple Silicon machine, prefer matching ARM images where available; do not silently treat emulation as equivalent capacity. [Docker multi-platform builds](https://docs.docker.com/build/building/multi-platform/)

The fleet chooses a host; the host must reserve capacity before launch. This prevents two concurrent requests using the same reported free capacity. Record the durable launch request and reconcile it after connection loss so retrying the request does not duplicate an agent. These are proposed scheduler requirements, not claims about the current daemon.

**Storage creates affinity even without a user pin.** Docker bind mounts resolve on the runtime host, and ordinary local volumes are host-local. A retained workspace normally returns there. Another eligible machine can create a new workspace from the repository, but that does not transfer uncommitted files, conversation state, installed artifacts or database contents. Show unavailable/awaiting-host state honestly; design export/transfer and recovery separately. [Bind mounts](https://docs.docker.com/engine/storage/bind-mounts/), [volume lifecycle and migration](https://docs.docker.com/engine/storage/volumes/)

This aligns with the daemon design's server-side checkout direction. It does not introduce laptop filesystem synchronization or live migration. User pinning and workspace storage affinity should be distinct in the UI.

## Environment editor feasibility

The approved editor is feasible for known templates and supported fields. It is not a universal reversible visual representation of arbitrary build logic. Dockerfiles may compute versions indirectly; Compose can merge files and interpolate variables; setup scripts can install anything.

Recommended implementation scope:

1. Preserve source files, unknown keys, comments and references; patch only fields the editor can identify reliably.
2. Show where each editable value comes from. Use source editing for unsupported patterns instead of silently replacing them with a simplified generated file.
3. Diff against a known base revision and detect a changed branch before creating the PR/MR.
4. Keep runtime placement, local service bindings and secrets separate from portable repository definition where appropriate; the precise schema remains open.
5. Keep a workspace's resolved environment revision and apply configuration upgrades explicitly. Do not let a routine restart pick up arbitrary new environment definitions unnoticed.
6. Link any trial result to the exact definition tested, host and versions. Editing afterwards makes that result historical, not proof of the new configuration.

A trial can establish startup/tool readiness for that version. It does not prove application correctness, production compatibility or data migration safety. It remains optional, as agreed. Temporary environment trials are distinct from building a PR preview platform: existing forge previews remain integrations only.

## Alternatives considered

| Approach | Benefits | Costs / fit |
|---|---|---|
| **Dev Containers + Compose adapters, plus native mode — recommended for the first proof** | Reuses repo definitions and established runtime mechanisms; leaves Marshal in control of ACP and fleet UX | Marshal still owns lifecycle reconciliation, capacity, persistence and policy integration |
| Integrate a full workspace platform such as DevPod or Coder | Reuses broader provisioning, workspace lifecycle and connectivity capabilities | Additional lifecycle/control plane to reconcile with Marshal; evaluate if cloud provisioning becomes central |
| Build a proprietary environment format and launcher | Maximum control over a narrowly supported setup | More lifecycle/build compatibility work and weaker reuse of repositories' existing definitions |

[DevPod providers](https://devpod.sh/docs/managing-providers/what-are-providers) can provision machines or run containers on existing targets. [Coder's lifecycle](https://coder.com/docs/user-guides/workspace-lifecycle) uses Terraform, its control plane and workspace agent. Their functionality is relevant; the recommendation against making either a prerequisite is a fit judgement for the existing-daemon/self-hosted scope, not a performance benchmark or a claim that integration is impossible.

## Bounded follow-up proof, not executed

A separate throwaway experiment could validate the high-risk seams before a detailed implementation plan:

- Launch one representative devcontainer with two services through the intended bridge/runtime boundary; verify agent connection and lifecycle logs.
- Run two workspaces from the same repo without port, volume, branch or service collisions; check explicitly shared-service behaviour separately.
- Edit files and persist test data, stop compute, then resume and rebuild without losing either. Verify retained agent context independently of filesystem persistence.
- Exercise browser disconnect, host restart and interrupted launch; ensure work does not duplicate or get deleted.
- Demonstrate native macOS execution and the actual containerized ACP topology on macOS; check ARM/x86 eligibility.
- Measure idle, startup, dependency-install, build/test and stopped states on representative Linux and a 16 GB Mac host with unrelated services.
- Make one editor change, preserve unrelated configuration, run an optional trial, and produce a reviewable patch without a real repository write.

Success would justify refining the chosen architecture. Failure should narrow compatibility or change the integration approach, not be hidden by the UI. The user has approved research; these experiments have not been performed in this pass.

## Source provenance

Primary sources are linked next to the findings. Mutable repository documents were read on the research date. Observed Git blob IDs (file-content hashes, not commit IDs):

- Marshal `docs/daemon/design.md`: `c55976633ce48c60e4123840eb0b2564b387acb6`.
- Marshal `docs/DEPLOYMENT.md`: `ce34521e08e80c4d24ac7894e9ff5117c785d1ea`.
- Dev Containers CLI `README.md`: `39b043ba0520e6d818c4c84dab7dd586520655b5`.
- Dev Containers spec `docs/specs/devcontainerjson-reference.md`: `fa3893b18426ec56a79e6f64c89687572e186c80`.

Marshal's ongoing daemon work may supersede these documents. Recheck the contracts and supported deployment topology before implementation.
