# W4.2 · Bridge: templates, builds, spawn, warm pools — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w4-workspaces-design.md`](../specs/2026-10-03-w4-workspaces-design.md) §5, §7
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** backend. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W4.1](2026-10-03-w4-1-engine-workspace-plan.md) (previous backend plan), with everything it depends on. Backend plans never depend on UI plans.
**Base:** a branch containing every plan listed under Runs after. Bridge anchors were checked on
`2ddc09e`. W2 and W3 bridge additions are named as their plans define them:
- `Fleet.control` / `controlCall` (W3.2 Task 2);
- the shared homes and the `volumeMount` `readonly` parameter (W3.2 Task 1);
- workspace v8 (W3.2 Task 8).

**Plan slug:** `w4-2-bridge-templates-builds`. Commit each task as
`w4-2-bridge-templates-builds: task N — <title>`.

## Goal

The bridge:

- stores Studio templates;
- resolves Studio and repo references, including `extends`;
- builds layered images with logs;
- spawns agents into a workspace;
- keeps warm pools;
- imports devcontainers and snapshots agents;
- stores project settings and health.

## Non-goals

- Secrets, credentials and the egress proxy (W4.3). Spawn leaves a hook
  for them in Task 6.
- UI (W4.4, W4.5).

## Assumptions

- The control agent answers `workspace/parse`, `workspace/patch` and
  `workspace/format` (W4.1).
- These helpers exist as checked on `2ddc09e`:
  - `runRuntime`, `buildDerived`, `ensureDerivedImage` (`derive.go:43/59/102`);
  - `buildRunArgs`, `ContainerConfig` (`container.go:138/34`);
  - `devcontainerImage` (`profile.go:104`);
  - `EnsureMirror`, `mirrorHead` (`mirror.go:28/127`);
  - `projectTrust` (`trustinfo.go:33`);
  - `EventLog.ServeSSEKey` (`sse.go:29`);
  - `Fleet.FleetLog` (`fleet.go:284`);
  - `Fleet.Spawn` (`fleet.go:711`), `SpawnOptions` (`:656`);
  - `ProjectStatus` (`fleet.go:83/994`).
- Test seams: `testFleetWithRunner` (`derive_test.go:105`) and the
  `commandRunner` fakes.

---

## Task 1: Studio template store

**Goal:** create, save draft, publish, list, read, diff and delete Studio
templates on disk (spec §5.1).

**Files:**
- `web/bridge/wsstore.go` (new), `wsstore_test.go` (new)
- `web/bridge/wsstarters.go` (new)
- `web/bridge/audit.go`

**Steps:**

1. Define `TemplateMeta` (JSON):
   - `name`, `ownerId`, `createdAt`, `published`, `pool`;
   - `versions []TemplateVersion{N, At, By, SHA256, ImageTag, BuildStatus, SizeBytes, BuildMs}`.

   Define `TemplateStore{dir string; mu sync.Mutex}` rooted at
   `<state>/workspaces`.
2. Names match `^[a-z0-9][a-z0-9-]{0,40}$` and are validated on every
   call.
3. Methods:

   | Method | Behaviour |
   |---|---|
   | `Create(name, source, by)` | Writes `meta.json` and `draft.toml`; refuses existing names |
   | `SaveDraft(name, source)` | Overwrites `draft.toml` |
   | `Publish(name, by)` | Copies the draft to `v<n+1>.toml`, appends the version with `BuildStatus "pending"`, and sets `published` |
   | `List()` | All templates' meta |
   | `Read(name, n int)` | `n = 0` reads the draft |
   | `Delete(name, inUse func(string) bool)` | Refuses while in use |
   | `SetBuild(name, n, status, tag, size, ms)` | Records a build result |
   | `SetPool(name, n)` | Sets the pool size, 0–4 |

   - Writes are atomic: temp file, fsync, rename, as `Workspace.save`
     does (`workspace.go:269`).
   - Version files are never rewritten.
4. `wsstarters.go` holds Go string constants for the starters
   `go-service`, `node-app`, `python-uv`, `rust-crate` and `minimal`,
   using the spec §4.1 format with `[setup]` as a table. Add
   `TestStartersParse`, which runs each starter through a fake control
   agent's `workspace/parse` with no error diagnostics. Mark it
   `t.Skip` unless `MARSHAL_BIN` is set, since it needs the real
   `marshal`. Add the same check to the final verification.
5. Add the audit constants `workspace_created`, `workspace_published`,
   `workspace_deleted`, `workspace_build`, `workspace_snapshot`.
6. Tests:
   - the full lifecycle;
   - publishing twice gives v1 and v2, with immutable files;
   - delete is refused while in use;
   - bad names are rejected;
   - writes are atomic (no partial file after a simulated failure, by
     injecting a failing rename seam).

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestTemplateStore' -v && go test ./
```

---

## Task 2: Parsing through the control agent, and resolution

**Goal:** turn a reference into a resolved doc, with repo templates,
`extends` merge rules and trust gating (spec §5.2–§5.3).

**Files:**
- `web/bridge/wsresolve.go` (new), `wsresolve_test.go` (new)

**Steps:**

1. Define `WSDoc`, a Go mirror of W4.1's `Doc` JSON (bridge-side types;
   no shared Go type, per AGENTS.md).
2. `func (f *Fleet) parseWorkspace(ctx, src []byte) (WSDoc, []WSSection, []WSDiag, error)`:
   - calls `controlCall(ctx, "workspace/parse", {source}, false)`;
   - caches results by the sha256 of `src` (LRU, 256 entries).
3. Add `type WSRef struct{Source string; Name string; Version int}` and
   `func ParseWSRef(s string) (WSRef, error)`, covering:
   - `name`;
   - `name@3`;
   - `repo:name`.
4. `func (f *Fleet) ResolveWorkspace(ctx, ref WSRef, projectRoot string) (Resolved, error)`.
   `Resolved{Doc WSDoc; Name; Version int; Source string; Hash string}`.
   - **Studio:** read the published (or pinned) version, then parse it.
     Error diagnostics fail the resolve.
   - **Repo:**
     1. `projectTrust(projectRoot)` must be `trusted`. Otherwise return
        `ErrUntrustedRepoTemplate`.
     2. Read `<root>/.marshal/workspaces/<name>.toml`, then parse it.
     3. If it has `extends`, resolve the Studio ref and merge with
        `mergeWS(base, overlay)`.
   - `mergeWS` applies the spec §5.2 rules. Each violation returns
     `ErrWorkspaceMerge{Field, Reason}`. "Lower" for memory and disk
     compares parsed sizes; for timeout it compares `time.ParseDuration`
     values.
   - `Hash` is the sha256 of the canonical JSON of the resolved doc.
5. Tests:
   - parse refs;
   - Studio latest and pinned;
   - a repo template in an untrusted project is refused (use
     `projectWithConfig`/`writeTrustStore`, `trustinfo_test.go:19/33`);
   - each merge rule, including every violation;
   - the cache hit avoids a second control call.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestResolveWorkspace|TestMergeWS|TestParseWSRef' -v
```

---

## Task 3: Layered build and build logs

**Goal:** `Build(name, n)` builds layers 1–3 plus the final layer with
per-layer cache, streams logs, and records the result (spec §5.4–§5.5).

**Files:**
- `web/bridge/wsbuild.go` (new), `wsbuild_test.go` (new)
- `web/bridge/toolchains.go` (new), `toolchains_test.go` (new)
- `web/bridge/derive.go` (add a log-streaming variant)

**Steps:**

1. `toolchains.go` holds a map `lang → func(version string) (envLines []string, run string)`
   with the four installers in spec §5.5. Use
   `ARG TARGETARCH` / `dpkg --print-architecture` to choose the
   architecture. Test that each produces a non-empty `RUN` and that an
   unknown language errors.
2. `func layerDockerfiles(doc WSDoc) ([]layerSpec, error)`, where
   `layerSpec{Key string; Dockerfile func(parent string) string; Hash string}`:
   - **l1** is `FROM <base>`;
   - **l2** is one `RUN` per toolchain, plus `ENV PATH=…`;
   - **l3** is `apt`, then `go install`, `npm i -g` and `pip install`
     (each only when its list is non-empty);
   - **final** adds the labels.
   - `Hash` is the sha256 of the layer's canonical JSON plus the parent
     tag, giving the tag `marshal-ws/<name>:l<k>-<hash[:12]>`.
3. `func (f *Fleet) buildDerivedStream(runtime, tag, dockerfile string, out io.Writer) error`
   is like `buildDerived`, but streams combined output to `out` line by
   line. With `f.runner` set (tests), it calls the runner and writes its
   output.
4. `func (f *Fleet) BuildWorkspace(ctx, name string, n int) error`:
   - Check the limits: one build per template and two in total,
     enforced with a semaphore. A full slot returns `ErrBuildBusy`.
   - For each layer, check the cache with `runRuntime(rt, "image", "inspect", tag)`.
     If the image exists, log `cached l<k>`. Otherwise build it.
   - Then tag the final layer as `marshal-ws/<name>:v<n>` and run
     `ensureDerivedImage(ctx, finalTag)`, which adds marshal and returns
     the derived tag.
   - Record `SetBuild`: status `ok` or `failed`, the derived tag, the
     size from `inspect --format {{.Size}}`, and the duration. Audit
     `workspace_build`.
   - Logs go to a dedicated `EventLog` (`f.buildLog`, created in
     `NewFleet`) under the key `build:<name>:<n>`, as
     `{line, at}` events, plus a final `{done, status}` event.
5. Routes:
   - `POST /api/workspaces/{name}/builds` `{version?}` returns 202
     `{version}`;
   - `GET /api/workspaces/{name}/builds` lists versions with their build
     status;
   - `GET /api/workspaces/{name}/builds/{n}/events` calls
     `f.buildLog.ServeSSEKey(w, r, "build:"+name+":"+n)`.
6. Tests, using `testFleetWithRunner`:
   - the first build issues three `build` calls plus the derive;
   - a second build after only a packages change hits the cache for l1
     and l2, builds l3 and the final layer, and derives;
   - a failure records `failed`;
   - the SSE replay returns the log lines;
   - a concurrent build gets `ErrBuildBusy`.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestBuildWorkspace|TestLayer|TestToolchain' -v && go test ./
```

---

## Task 4: Template and build routes, devcontainer import, snapshot

**Goal:** HTTP routes over the store, plus import and snapshot
(spec §5.1, §5.8).

**Files:**
- `web/bridge/wshttp.go` (new), `wshttp_test.go` (new)
- `web/bridge/http.go`

**Steps:**

1. Template routes:

   | Route | Behaviour |
   |---|---|
   | `GET /api/workspaces` | Studio metas, plus repo templates for each trusted project. Each entry gives `{source:"studio"\|"repo", name, project?, published?, usage}`, where `usage` counts agents with that workspace. |
   | `POST /api/workspaces` `{name, from: "blank"\|"starter:<id>"\|"devcontainer:<root>"\|"snapshot:<agentId>"}` | Creates a template |
   | `GET /api/workspaces/{name}?version=` | `{source, doc, sections, diagnostics}` |
   | `PUT /api/workspaces/{name}/draft` `{source}` | Saves and parses |
   | `POST /api/workspaces/{name}/patch` `{layer, value}` | `workspace/patch` against the draft, then saves |
   | `POST /api/workspaces/{name}/publish` | Publishes |
   | `GET /api/workspaces/{name}/diff?a=&b=` | A line diff of two versions, as unified text from a small LCS diff in Go |
   | `DELETE /api/workspaces/{name}` | Deletes |
   | `PUT /api/workspaces/{name}/pool` `{size}` | Sets the pool size |

2. `devcontainer:<root>` uses `devcontainerImage(root)`. A `build` field
   returns 400 with the reason text from that function. The template
   source is `Format` of a doc with only `base`.
3. `snapshot:<agentId>`:
   - The agent must be containerized: check `rt.containerized`, the
     `agentRuntime` field.
   - Run `runRuntime(rt, "commit", containerNameFor(id), "marshal-ws/<name>-snap:<unix>")`.
   - The template's `base` is that tag.
   - Audit `workspace_snapshot`.
4. Tests: every route, the devcontainer refusal, and the snapshot command
   via the fake runner.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestWorkspacesHTTP' -v && go test ./
```

---

## Task 5: Workspace v9 — agent workspace, pool fields, project settings

**Goal:** `fleet.json` carries the agent workspace, container overrides
and project settings (spec §5.6, §5.7, §7).

**Files:**
- `web/bridge/workspace.go`, `workspace_test.go`
- `web/bridge/fleetevents.go` (`AgentStatus`)

**Steps:**

1. Set `workspaceVersion = 9`.
2. Add `Workspace *AgentWorkspace "workspace,omitempty"`, where
   `AgentWorkspace{Name, Version int, Source}`.
3. Add `ContainerName`, `WorkSubpath` and `SocketSubpath` (strings,
   omitempty) to `Agent`. Empty values mean the derived names
   (`containerNameFor`, `work/<id>`, `sockets/<id>`).
4. Add `ProjectSettings map[string]ProjectSettings "projectSettings,omitempty"`
   to the file and to `Workspace`, with `PutProjectSettings` and
   `ProjectSettingsFor(root)`. Fields follow spec §7. `RemoveProject`
   deletes the project's entry.
5. Add `Workspace *AgentWorkspace` to `AgentStatus`, and fill it in
   `Fleet.Snapshot`.
6. Make the `newRuntime` closure honour the overrides. When
   `a.ContainerName`, `WorkSubpath` or `SocketSubpath` are set, use them
   in `ContainerConfig` instead of the derived names.
7. Tests:
   - a v8 file loads as v9;
   - project settings round-trip and are removed with the project;
   - the overrides reach `ContainerConfig` (capture `buildRunArgs`).

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestWorkspace|TestSnapshot|TestNewRuntime' -v && go test ./
```

---

## Task 6: Spawning into a workspace

**Goal:** `SpawnOptions.Workspace` (and the project default) runs the
agent in the built image with its mounts, files, resources, policy and
setup step (spec §5.6).

**Files:**
- `web/bridge/fleet.go` (`SpawnOptions` `:656`, `Spawn` `:711`, `newRuntime` closure, session/new params `:863`)
- `web/bridge/container.go` (`ContainerConfig`, `buildRunArgs`)
- `web/bridge/http.go` (`spawnAgent` body)
- `web/bridge/wsspawn.go` (new), `wsspawn_test.go` (new)
- `web/bridge/disk.go` (`Prune` `:146`)

**Steps:**

1. Add `Workspace string` to `SpawnOptions`, and decode it from the
   `spawnAgent` body. When it's empty, use
   `ProjectSettingsFor(root).Workspace`. When that's empty too, keep
   today's behaviour (profile and devcontainer).
2. In `Spawn`, before the runtime starts:
   1. `ResolveWorkspace`.
   2. Require that version's `BuildStatus == "ok"`. Otherwise return
      `ErrWorkspaceNotBuilt`, which becomes a 409 with a "Build it first"
      message.
   3. Set `a.Workspace`.
   4. Set the profile from the workspace:
      - `Image` is the derived tag;
      - `CPUs` and `MemoryMB` come from `[resources]`, parsed from `8g`
        and similar.
3. Add `ExtraMounts []string` (pre-rendered `--mount` args) and
   `ExtraEnv map[string]string` to `ContainerConfig`. `buildRunArgs`
   appends `ExtraMounts` after the socket mount and merges `ExtraEnv`
   before the sorted env loop. `TestContainerRunArgsRefuseHostEscapes`
   must still pass: never bind `/var/run/docker.sock`, and reject any
   target under `/run/marshal`.
4. Add `func (f *Fleet) workspaceMounts(ctx, a Agent, doc WSDoc) ([]string, error)`:
   - **`repo` mounts:** get the mirror path with `EnsureMirror` for the
     registered repo, then add a `type=volume` mount of the state-volume
     subpath `repos/<hash>` at the target, read-only.
   - **`volume` mounts:** use `volumeMount(runtime, name, target, "", false)`.
   - **`[files]`:** add a state-volume subpath mount for
     `workspaces/<name>/files/<src>`, with `readonly` from the entry.
   - Reject a target that collides with `/work`, `/run/marshal` or
     `/marshal`.
5. **Hook for W4.3.** Add
   `func (f *Fleet) workspaceNetworkEnv(ctx, a Agent, doc WSDoc) (map[string]string, []string, error)`,
   which returns env and mounts. In this plan it returns `nil, nil, nil`;
   W4.3 fills it with the proxy and CA settings. Merge its results into
   `ExtraEnv` and `ExtraMounts`.
6. **Policy:** when `doc.Policy` is set, add `params["policy"] = {mode, allow}`
   to the `session/new` params (`fleet.go:863`).
7. **Setup.** When `doc.Setup.Run != ""` and this isn't a reattach, run a
   one-shot container before `start()`:
   - `run --rm --name marshal-setup-<id> -w /work <the same mounts and env> <image> sh -c <run>`,
     through `runRuntime`;
   - a non-zero exit aborts the spawn with
     `ErrSetupFailed{Output: tail 64KiB}`, shown as a 502 with the
     output.

   Add `func (c *containerTransport) buildSetupArgs(cmd string) []string`
   beside `buildRunArgs`, sharing a private `commonRunArgs` helper.
8. **Timeout:** when `[resources].timeout` is set, start a timer when the
   agent's first prompt is sent. When it fires, call `Registry.Cancel`
   and `Fleet.Pause` (`fleet.go:1301`), and write the audit entry
   `agent_timeout`.
9. **Protect mounted mirrors from prune.** In `Fleet.Prune`
   (`disk.go:146`), the mirror loop over `repos/` treats a mirror as in
   use when any published Studio template's resolved doc has a `repo`
   mount on that repo. Collect those through the store and cached
   parses before deleting.
10. Tests, all with the fake runner:
   - the run args contain the workspace image, mounts and env;
   - a project default workspace is used when the request names none;
   - an unbuilt workspace gives 409;
   - setup runs before `run -d`, and its failure aborts with the output;
   - `policy` is in `session/new` params;
   - a mount target collision is refused;
   - the timeout pauses the agent (with an injected clock);
   - prune keeps a mirror that a template mounts.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestSpawnWorkspace|TestContainer|TestSetup' -v && go test ./
```

---

## Task 7: Warm pools

**Goal:** keep `pool` idle containers per published version and hand
them to git-sourced spawns (spec §5.7).

**Files:**
- `web/bridge/wspool.go` (new), `wspool_test.go` (new)
- `web/bridge/fleet.go` (`Spawn`)

**Steps:**

1. Add a `poolManager` holding, per `<name>@<n>`, a list of idle
   `poolEntry{container, workSubpath, socketSubpath, startedAt}`. It is
   guarded by a mutex.
2. `fill(name, n)` starts containers until the idle count reaches
   `meta.pool`:
   - name: `marshal-pool-<name>-v<n>-<k>`;
   - subpaths: `pool/<name>-<k>/work` and `…/sock`;
   - image: the derived image;
   - no `LocalMount`;
   - the shared homes and the network hook env, as in Task 6;
   - command: `acp --listen`.

   Filling runs after a successful build, after a take, and at bridge
   start for templates with `pool > 0`.
3. Add `take(name, n) (poolEntry, bool)`. In `Spawn`, when the agent is
   git-sourced (`SourceKind == "git"`) and the workspace has an idle
   entry:
   1. Set `a.ContainerName`, `a.WorkSubpath` and `a.SocketSubpath` from
      the entry.
   2. Prepare the checkout into the bridge-side path for that work
      subpath (reuse the worktree preparation with the new directory).
   3. Reattach through `Open()`, which finds the running container by
      name (`container.go:181`).
   4. Continue with `session/new`.
   5. Trigger `fill` in the background.
4. Measure starts. Record each spawn's time from request to `session/new`
   reply, keeping the last 10 per template, labelled cold or warm.
   Expose them in `GET /api/workspaces/{name}/builds` as
   `starts: {coldMs, warmMs}` medians.
5. Shutdown and changes:
   - `PUT …/pool {size: 0}` and template deletion kill idle containers;
   - bridge shutdown leaves pool containers running (like agents), and
     `listAgentContainers` is extended to also list
     `marshal-pool-*` so startup can adopt them.
6. Tests:
   - fill starts N containers (the fake runner records `run`);
   - a git spawn takes one and triggers a refill;
   - a local spawn doesn't take;
   - pool size 0 kills idle containers;
   - adoption on startup.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestPool' -race -v && go test ./
```

---

## Task 8: Project settings and health routes

**Goal:** `GET/PUT /api/projects/settings` and `GET /api/projects/health`
(spec §7). Defaults apply to spawns and runs.

**Files:**
- `web/bridge/projects.go` (new), `projects_test.go` (new)
- `web/bridge/http.go`, `web/bridge/runs.go` (W3.2)

**Steps:**

1. Settings:
   - `GET` returns `ProjectSettingsFor(root)`.
   - `PUT` validates:
     - the workspace ref parses;
     - `mode` is a valid mode;
     - `shipTarget` is in the allowed set;
     - `intake.repoId` is a registered repo.

     It then stores the settings and writes the audit entry
     `project_settings`.
   - When `intake.labels` changes, the registered repo's `watch` and
     `watchLabel` (`workspace.go:98`) are updated to match, using the
     first label. The poller supports one label.
2. Health:
   - `gateRunnable` comes from W2.1's stored gates for the project's
     agents;
   - `mirrorFresh` comes from `mirrorHead` of each registered repo's
     mirror, as an age;
   - `orphanWorktrees` and `trust` come from `ProjectStatus()`
     (`fleet.go:994`);
   - `workspaceResolves` is the result of `ResolveWorkspace`.
3. Defaults:
   - `spawnAgent` and W3.2's `startRun` fill `mode`, `isolated`,
     `routing` and `workspace` from the settings when the request leaves
     them out;
   - `Exit` uses `shipTarget` when it's valid for the agent's source;
     `merge` is never allowed for a git-sourced agent.
4. Tests:
   - settings round-trip and validation;
   - health fields from fixtures;
   - spawn picks up the defaults;
   - an invalid ship target is refused.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestProjectSettings|TestProjectHealth|TestSpawn' -v && go test ./ && go vet ./
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./...
cd web/bridge && go test ./... -race && go vet ./...
MARSHAL_BIN=$(cd ../.. && go build -o /tmp/marshal ./cmd/marshal && echo /tmp/marshal) go test ./ -run TestStartersParse -v
cd ../.. && CGO_ENABLED=1 go test ./... ; gofmt -l .
```

Expected:
- the bridge passes, including `TestWebIsStdlibOnly`;
- the starters parse with the real `marshal`;
- the repo-wide run fails only on the five known tests.

## Integration notes

- Spawning without a workspace (and with no project default) behaves
  exactly as before.
- Pool containers are visible in `docker ps` as `marshal-pool-*`.
  `Fleet.Prune` (`disk.go:146`) scans only `repos/` and `work/`, so pool
  subpaths under `pool/` are untouched. Mirrors used by workspace `repo`
  mounts are protected by Task 6, step 9.
- Workspace version is now 9.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. Store, resolve, build, routes, data model, spawn, pools, then projects, each with `go test -run` targets. |
| Anchors verified? | Checked on `2ddc09e`: `derive.go:43/59/102`, `container.go:34/138/181`, `TestContainerRunArgsRefuseHostEscapes` (`container_test.go:64`), `profile.go:104`, `mirror.go:28/127`, `trustinfo.go:33` and its test fixtures, `sse.go:29`, `fleet.go:284/656/711/863/994/1301`, `workspace.go:98/269`. Later names are as defined. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | One explicit hook (Task 6 step 5), filled by W4.3 Task 6. |
| Matches the spec? | §5, §7. |
