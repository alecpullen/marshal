# W4 · Workspaces — phase spec

Parent design: [`docs/web-studio/design.md`](../design.md) (§8.8, §8.9 project overview, §8.11, §8.15; §9 row W4).
Previous phases, assumed complete: W1, W2, W3 (all plans).
Plans, executed in this order:
1. [W4.1 · Engine: workspace files, trust, policy](../plans/2026-10-03-w4-1-engine-workspace-plan.md)
2. [W4.2 · Bridge: templates, builds, spawn, warm pools](../plans/2026-10-03-w4-2-bridge-templates-builds-plan.md)
3. [W4.3 · Bridge: secrets, credentials, egress proxy](../plans/2026-10-03-w4-3-bridge-secrets-egress-plan.md)
4. [W4.4 · UI: workspaces, builds, New agent](../plans/2026-10-03-w4-4-ui-workspaces-plan.md)
5. [W4.5 · UI: network inspector, projects, secrets](../plans/2026-10-03-w4-5-ui-network-projects-plan.md)

## 1. Summary

W4 makes the agent's environment something you design:

- **Workspace templates.** Templates are layered and versioned. Studio
  templates live in the bridge; repo templates are optional and live in
  `.marshal/workspaces/`.
- **A build pipeline.** It produces cached per-layer images, plus warm
  pools of ready containers.
- **Secrets that stay out of containers.** A pluggable provider supplies
  them: OpenBao, a local AES-GCM store, or environment variables.
- **One shared egress proxy.** Agents can't bypass it. It enforces
  allowlists, injects credentials for chosen hosts, and logs every
  connection.
- **A network inspector**, which turns blocked requests into decisions.
- **Projects 2.0**, which gives each project per-project defaults,
  intake, health and policy.

This phase also closes a gap the research found: the bridge's credential
store is always empty in production (`fleet.go:232`), and there is no way
to register repos or credentials except through the issue poller. W4 adds
both, backed by the secrets provider.

## 2. What earlier phases left in place

| From | Used for |
|---|---|
| W3: the control agent (`Fleet.control`, `controlCall`), shared homes (`home/config`, `home/data`), the `ErrUnsupported` pattern, budgets, the usage ledger, the fleet deltas | Workspace parsing runs on the control agent; templates and secrets are owner-scoped like the W3 state |
| W2: the review-comment workspace migration pattern (`fleet.json` v7) and W3's v8 (`watchRules`) | W4 bumps the workspace version to v9 |
| Before W1: `ensureDerivedImage`/`buildDerived`/`runRuntime` (`derive.go`), `ContainerConfig`/`buildRunArgs` (`container.go`), `ResolveProfile`/`devcontainerImage` (`profile.go`), `Credential`/`CredentialStore` (`credential.go`), `gitEnv` (`git.go:35`), the `Forge` interface, `internal/trust` | Extended, not replaced |

## 3. Scope

### In scope

- **Engine:**
  - the workspace file format, parsed and edited by `internal/workspacecfg`;
  - the control-agent methods `workspace/parse`, `workspace/patch` and
    `workspace/format`;
  - trust-hash coverage of `.marshal/workspaces/*.toml`;
  - a session policy on `session/new`.
- **Bridge:**
  - the Studio template store and versions;
  - repo templates and `extends`;
  - the layered build, its build log stream, and devcontainer import;
  - "snapshot a running agent";
  - spawning with a workspace (mounts, files, resources, setup);
  - warm pools;
  - the secrets provider (OpenBao, local AES-GCM, env);
  - the credentials and repos routes;
  - the egress proxy (sidecar, internal network, policy, CA, injection,
    logging);
  - the network log and blocked-request decisions;
  - project settings and project health.
- **UI:**
  - the Workspaces gallery and designer (Layers | Layers + source |
    Source), plus builds;
  - the workspace chip on New agent;
  - the network inspector;
  - the project overview;
  - settings for secrets, credentials and repos.

### Out of scope

- Per-workspace proxy sidecars. One shared proxy was decided (design
  §8.15).
- 1Password and `pass` backends, which come later through the same
  interface.
- The automations tab of the project overview (W5).
- Running tasks in parallel.

## 4. Workspace file

### 4.1 Format

The format is the design's §8.8 draft schema, version 1, and is the same
for both homes:

| Section | Keys | Layer |
|---|---|---|
| `[workspace]` | `name`, `base`, `toolchains`, `extends` (repo templates only) | 1 base, 2 toolchains |
| `[packages]` | `apt`, `go`, `npm`, `pip` (string lists) | 3 |
| `[[mounts]]` | `repo` or `volume`, `target`, `readonly` | 4 libraries |
| `[files]` | `"<source>" = {target, readonly}` | 5 shared files |
| `[secrets]` | `NAME = "vault:<ref>"` (environment injection, last resort) | 6 |

> **W4.2 status:** the bridge parses `[secrets]` name-to-ref pairs (`secretsEnv`) and merges them, but does not yet read them when it builds a container. Putting a secret value in a container's environment shows it in `inspect` and process listings, so it is left out until it has a plan of its own. `[secrets.inject]` (proxy injection) is the supported path.

| `[secrets.inject]` | `"<host>" = {ref = "vault:<ref>", header = "Authorization", format = "Bearer {}"}` | 6 |
| `[network]` | `mode = open\|allowlist\|off`, `egress = [host or *.suffix]` | 7 |
| `[resources]` | `cpu`, `memory`, `disk`, `timeout` | 8 |
| `[policy]` | `mode`, `allow = ["go test *", …]` | (side panel) |
| `[setup]` | `run = "<shell>"` | 9 |

- **Toolchains** take the form `<lang>@<version>`. Supported languages:
  `go`, `node`, `python`, `rust`.
- **Mount `repo`** names a registered repo ID (the W4 repos registry,
  §6.2), whose bare mirror is mounted read-only. A `volume` is a named
  volume in the runtime.
- **`[files]` sources** are paths under the template's file store,
  `<state>/workspaces/<name>/files/`, which is uploaded through the UI.
- **`[setup]` is a table.** The design's draft writes `setup = "…"` as a
  bare key after other tables, but TOML would assign that key to the
  preceding table.
- **`[secrets]` mixes two forms:** `NAME = "vault:…"` string values for
  environment injection, and the `inject` subtable. The parser decodes
  `[secrets]` as a raw map and splits it by value type.

### 4.2 `internal/workspacecfg` (engine)

This is a new package that uses `go-toml/v2`, which is already a
dependency:

- **`Parse(src []byte) (Doc, []Section, []Diagnostic)`**
  - `Doc` is the typed form, with JSON tags.
  - `Section{Layer int; Key string; StartLine, EndLine int}` gives each
    layer's source line range. It's found by scanning table headers.
    `[workspace]` serves both layer 1 and layer 2.
  - Diagnostics carry `{line, message, severity}` for:
    - an unknown key;
    - a bad toolchain;
    - a bad network mode;
    - a secret value without `vault:`;
    - `extends` used in a Studio template.
- **`Patch(src []byte, layer int, value json.RawMessage) ([]byte, error)`**
  re-renders only that layer's section(s) from `value` and splices them
  into `src` at the section's line range. Comments elsewhere survive. A
  missing section is appended in canonical order.
- **`Format(doc Doc) []byte`** gives a canonical rendering, used for new
  templates.

### 4.3 Control-agent methods

Like `config/*` (W3), these have no session.

| Method | Params | Result |
|---|---|---|
| `workspace/parse` | `{source}` | `{doc, sections, diagnostics}` |
| `workspace/patch` | `{source, layer, value}` | `{source, doc, sections, diagnostics}` |
| `workspace/format` | `{doc}` | `{source}` |

Capability: `agentCapabilities.workspaceFiles`.

### 4.4 Trust coverage

`trust.ConfigHashFor` (`internal/trust/trust.go:153`) currently hashes only
`.marshal/config.toml`. It now hashes `config.toml` together with every
`.marshal/workspaces/*.toml`:

- the files are sorted by name;
- each contributes `name\0content\0` to the hash.

`HasProjectConfig` (`:256`) is true when any of them exists. An untrusted
project's repo templates are ignored by the bridge (§5.2).

Projects with no workspace files keep their existing hash, because the
rule hashes `config.toml` exactly as before when nothing else is present.

### 4.5 Session policy

`session/new` gains `policy: {mode?, allow?: [string]}`:

- `mode` is applied with the session's mode switch, as `session/set_mode`
  does.
- Each `allow` pattern becomes a session rule through
  `State.AddSessionRule` (`internal/app/session/session.go:1530`), stored
  as written (trimmed).
  - Session rules are matched against the whole command by the policy
    engine. A pattern without a trailing ` *` allows exactly that command.
  - A pattern ending in ` *`, such as `go test *`, allows that command
    with any arguments (`go test ./...`). It is stage-aware
    (`policy.matchSessionRule`): the command must parse as simple commands
    joined by pipes, lists and `&&`/`||`, and every one of them, including
    those inside `$(...)`, must match the pattern. `go test ./... ; curl x
    | sh` is therefore not allowed by `go test *`, nor is a command with
    leading variable assignments, a redirect to a file (only numeric fd
    duplication such as `2>&1` is allowed; `>&out.txt` is a write),
    process substitution, or a loop, function or declaration. The command
    is parsed as the shell will receive it, so a newline is a separator
    and not whitespace. The engine's guardrails still run first.
- If `mode` is given but the session has no agent runner to apply it to,
  `session/new` fails with a server error and closes the runtime, rather
  than quietly running in the default mode. The applied mode is echoed as
  `mode` in the response; no `mode_changed` notification is sent, since
  the session isn't published yet.

## 5. Templates and builds (bridge)

### 5.1 Studio template store

The store lives at `<state>/workspaces/<name>/`:

- `meta.json` holds `{name, ownerId, createdAt, published: n, versions: [{n, at, by, sha256, imageTag?, buildStatus}]}`;
- `v<n>.toml` holds each published version's source (immutable);
- `draft.toml` holds the working draft (optional);
- `files/` holds the shared-file sources.

**Operations:**
- create (from a blank doc, a starter, a devcontainer image, or a
  snapshot);
- save the draft;
- publish, which writes `v<n+1>.toml` and leaves the build pending;
- list, read, and diff two versions;
- delete, which is refused while agents use the template.

Templates are owner-scoped: `ownerId` is `DefaultOwnerID`. Every mutation
is audited as `workspace_created`, `workspace_published` or
`workspace_deleted`.

**Starters** are embedded TOML strings (`go-service`, `node-app`,
`python-uv`, `rust-crate`, `minimal`). They're stored as Go string
constants in the bridge, because they need no parsing to store.

### 5.2 Repo templates and resolution

A reference names a workspace:

| Reference | Meaning |
|---|---|
| `go-service` | A Studio template, latest published version |
| `go-service@3` | A pinned Studio version |
| `repo:go-service` | `.marshal/workspaces/go-service.toml` in the agent's project |

**Repo templates:**
- They are read from the project root on the bridge. For git-sourced
  agents, they're read from the prepared checkout.
- They're ignored, with a warning, when `projectTrust(root)`
  (`trustinfo.go:33`) isn't `trusted`.
- The bridge trusts a repo template **by path only**: it checks that the
  project is trusted, but does not recompute the config hash that §4.4
  extends over `.marshal/workspaces/*.toml`. A template edited after the
  project was trusted still resolves in the bridge, although the engine
  would refuse the project. For a git-sourced agent, trust comes from the
  registered project whose intake names the repo, not from the checkout.
- `extends = "<studio ref>"` merges the parsed docs as JSON (§5.3).

**Merge rules (repo over Studio):**

| What | Rule |
|---|---|
| String lists (`toolchains`, packages, `egress`) | Append unique |
| `mounts` and `files` | Append; a target collision is an error |
| `secrets` | May only reference keys the Studio template already declares |
| `resources` | May only lower values |
| `network.mode` | May only tighten (open > allowlist > off) |
| `policy.allow` | Append |
| `setup.run` | Studio setup, then repo setup |
| `base` | Can't change |

A violation is a resolve error, shown in the designer and refused at
spawn.

### 5.3 Parsing in the bridge

The bridge must stay standard-library only, so it never parses TOML. It
calls `workspace/parse` on the control agent (W3) and works with the JSON
`doc`. Parsed docs are cached by the sha256 of their source.

### 5.4 Layered build

A build of `<name>@<n>` resolves the doc, then builds a chain of images.
Each layer image is tagged
`marshal-ws/<name>:l<k>-<hash12>`. The hash covers the parent tag plus the
layer's canonical JSON. If `image inspect` finds the tag, that layer is
skipped as cached.

| Layer | Dockerfile (generated) |
|---|---|
| 1 base | `FROM <base>` |
| 2 toolchains | `FROM <l1>` plus one `RUN` per toolchain from an installer table (§5.5) |
| 3 packages | `FROM <l2>`, then `RUN apt-get update && apt-get install -y --no-install-recommends …` (when `apt` is set) and `RUN go install …` / `npm i -g …` / `pip install …` |
| final | `FROM <l3>` plus `LABEL marshal.workspace=<name> marshal.version=<n>`, tagged `marshal-ws/<name>:v<n>` |

Then `ensureDerivedImage` (`derive.go:102`) adds the `marshal` binary,
exactly as for devcontainer images today.

Layers 4–9 aren't image layers. Mounts, files, secrets, network,
resources and setup are applied when an agent spawns (§5.6).

**Mechanics:**
- Builds use the existing `buildDerived` pattern: the Dockerfile goes on
  stdin with an empty context.
- Builds run one at a time per template, and at most two run in total.
- Build output is captured line by line and appended to an event log
  keyed `build:<name>:<n>`.
- `GET /api/workspaces/{name}/builds/{n}/events` serves that log over SSE
  through the existing `ServeSSEKey` mechanism (`fleet.go:1123`).
- The result is recorded in `meta.json`: status, image tag, size from
  `image inspect`, and duration.

### 5.5 Toolchain installers

Installers live in a table in Go source. Each one is a shell snippet
parameterised by version, and assumes a Debian-based base image. A base
image of another family fails the build with a clear message.

| Lang | Install |
|---|---|
| `go@X` | Download `go<X>.linux-<arch>.tar.gz` from `go.dev/dl`, extract to `/usr/local/go`, and add it to `PATH` with `ENV` |
| `node@X` | Download the official `node-v<X>`-latest tarball from `nodejs.org/dist/latest-v<X>.x/`, then extract it |
| `python@X` | `uv` from its install script, then `uv python install X` |
| `rust@X` | `rustup` with `--default-toolchain X` |

Builds need network access. They run outside the egress proxy, because
they're bridge operations rather than agent traffic, and are audited as
`workspace_build`.

### 5.6 Spawning with a workspace

`SpawnOptions` and `POST /api/agents` gain `workspace` (a reference).
Projects can set a default (§7).

**Spawn steps:**
1. Resolve the reference and require a successful build.
2. Use image `marshal-ws/<name>:v<n>` (derived) in place of the profile
   image.
3. Set CPU and memory from `[resources]`. `disk` becomes a
   `--storage-opt size=` only when the runtime supports it, which is
   probed once. `timeout` sets an agent deadline after which the bridge
   cancels and pauses the agent.
4. Add the mounts:
   - each `repo` mount is the bare mirror (`mirror.go`, `EnsureMirror`)
     at `target`, read-only;
   - each `volume` mount is `volumeMount(runtime, volume, target, "")`;
   - `[files]` entries are bind mounts from
     `<state>/workspaces/<name>/files/<src>` (state-volume subpath
     `workspaces/<name>/files/<src>`).
5. Apply network and secrets through the proxy (§6).
6. Pass `policy` to `session/new` (§4.5).
7. **Setup.** Before the agent's `acp --listen`, run `setup` once in a
   one-shot container with the same image, mounts and env:
   `run --rm … <image> sh -c '<setup>'`. A failure aborts the spawn and
   shows the output. It is skipped on reattach.

`Agent` gains `workspace {name, version, source}`, so the session header
and Live wall can show it. `AgentStatus` gains `workspace`.

### 5.7 Warm pools

A template can set `pool = N` in the Studio meta (UI field, 0–4). The
bridge keeps `N` idle containers per published version:

- **Naming and mounts:** each pool container is named
  `marshal-pool-<name>-v<n>-<k>`. Its work and socket subpaths are
  `pool/<name>-<k>`, and it runs `acp --listen`.
- **Taking one:** a git-sourced spawn with that workspace takes an idle
  pool container:
  1. Record the container name and pool subpaths on the agent. `Agent`
     gains `containerName`, `workSubpath` and `socketSubpath`; when
     empty, the derived names are used.
  2. Prepare the checkout into the pool's work subpath.
  3. Connect, then call `session/new`.
  4. Start a refill in the background.
- **Not used for:** local-mount agents, whose bind mount is fixed at
  `run`.
- **Pool health** is part of the template's build panel: idle count, and
  the cold vs warm start time measured from the last 10 spawns.

### 5.8 Devcontainer import and snapshots

- **Import `devcontainer.json`.** Create a template whose `base` is the
  `image` field. `devcontainerImage` (`profile.go:104`) already reads it.
  A file with `build` is refused, with the same reason text.
- **Snapshot a running agent.** Run `commit <container> marshal-ws/<name>-snap:<ts>`,
  then create a template with `base` set to that tag and every other
  layer empty. Audit it as `workspace_snapshot`. Only container agents
  qualify.

## 6. Secrets and network (bridge)

### 6.1 Secret provider

```go
type SecretProvider interface {
    Name() string
    Get(ctx context.Context, owner, ref string) ([]byte, error)
    Put(ctx context.Context, owner, ref string, value []byte) error
    Delete(ctx context.Context, owner, ref string) error
    List(ctx context.Context, owner, prefix string) ([]string, error)
}
```

Refs are `vault:<path>`. The provider stores them at
`marshal/<owner>/<path>`.

| Backend | Configured by | Notes |
|---|---|---|
| `openbao` | `--secrets openbao --bao-addr URL --bao-mount secret --bao-role-id-file F --bao-secret-id-file F` | KV v2 over HTTP: `GET/POST/DELETE /v1/<mount>/data/<path>`, and `LIST /v1/<mount>/metadata/<path>`. AppRole login (`POST /v1/auth/approle/login`) renews the token at half its TTL. Uses `crypto/tls` with the system roots, plus an optional `--bao-ca-file`. |
| `local` | `--secrets local --secrets-key-file F` | AES-256-GCM. The data is one JSON file, `<state>/secrets/store.json`, holding per-ref `{nonce, ciphertext}` with the ref as AAD. The key file holds 32 raw bytes and must not be inside `--state-dir`: startup refuses when it is. `webbridge secrets init-key F` creates one with mode `0600`. |
| `env` (default) | none | Read-only. `vault:env/NAME` reads `$NAME` at use time. `Put` fails with "configure a secrets backend". |

The provider is chosen at startup. `GET /api/secrets/status` reports the
backend and its health.

### 6.2 Credentials and repos

Today a `Credential` is `{kind none|pat|ssh, envVar, keyPath}`, and the
production store is empty. W4 adds:

- **The `vault` kind.** `{kind:"vault", ref:"vault:git/github", user?}`.
  `Resolve` (`credential.go:69`) fetches the value through the provider
  at use time, and `gitEnv` (`git.go:35`) is unchanged.
- **Persistence.** Credentials are stored in `fleet.json` (workspace v10)
  without values. `NewFleet` loads them, which replaces the empty
  `NewCredentialStore(nil)` at `fleet.go:232`.
- **Routes:**
  - `GET/POST/DELETE /api/credentials`. Values are written through
    `PUT /api/secrets/{ref}`, never returned, and only `set: true` is
    reported.
  - `GET/POST/DELETE /api/repos` to register a repo (`id`, `url`,
    `branch`, `forge`, `apiBase`, `credRef`, `watch`, `watchLabel`).
    Each change is audited as `repo_registered` or `repo_removed`, using
    constants that already exist (`audit.go`).

### 6.3 Egress proxy

**Topology (container mode):**

- **The network.** At startup the bridge creates
  `marshal-agents`, an internal-only network
  (`network create --internal`), if it's missing. Agent containers join
  it alone, and nothing else is attached: `buildRunArgs` gains
  `--network marshal-agents`.
  - `TestContainerRunArgsRefuseHostEscapes` keeps forbidding
    `--network host`.
  - The sandbox's own `--network none` for tool commands is unaffected.
- **The proxy.** It is a sidecar container, `marshal-egress`, attached to
  both `marshal-agents` and the default network. Its image is built like
  `ensureDerivedImage`: `FROM` a small Debian base, then `COPY` the
  running `webbridge` binary in. The bridge resolves its own path with
  `os.Executable`. The container runs `webbridge egress --listen :3128 --control unix:///egress/control.sock`.
- **Control link.** The sidecar and the bridge talk over a Unix socket in
  the state volume (subpath `egress/`). The bridge serves:
  - `GET /policy`, a stream of JSON policy snapshots, one per change;
  - `POST /log`, batches of connection records;
  - `POST /blocked`, a blocked request awaiting a decision.
- **Agent env.** Agents get `HTTPS_PROXY`, `HTTP_PROXY`, `https_proxy`
  and `http_proxy`, all set to
  `http://<agentId>:<token>@marshal-egress:3128`. `NO_PROXY` is empty.
  - The token is per agent: an HMAC-SHA256 of the agent ID under a
    bridge-held 32-byte key (`<state>/egress/token.key`, mode 0600). It is
    a function of the agent ID, so a container that outlives a bridge
    restart keeps working and reattach needs no re-issue.
  - The proxy checks the token and the source IP. The IP is the
    container's address on `marshal-agents`, read with `inspect` at
    spawn.

**Process mode** (no container runtime):
- The same proxy runs in the bridge process on `127.0.0.1:<port>`, and
  agents get the env vars.
- Enforcement is advisory, because a process can ignore the proxy, and
  the UI labels it "not isolated".

**Policy per agent**, from its workspace's `[network]` and
`[secrets.inject]`:

| `mode` | Behaviour |
|---|---|
| `off` | Every request is refused |
| `open` | Everything is allowed and logged |
| `allowlist` | Only hosts in `egress` and in agent grants (§6.4) are allowed |

Model provider hosts are added to the allowlist automatically: the base
URLs of providers in `config/get` (W3).

**Request handling:**
- **CONNECT to a host that isn't injected:** check the policy, then
  tunnel bytes, counting each direction. Plain HTTP is handled the same
  way, as an absolute-URI request.
- **CONNECT to an injected host:**
  1. Terminate TLS with a leaf certificate for that host, signed by the
     workspace CA (§6.5) and cached for 24h.
  2. Read each HTTP/1.1 request, then set the header from
     `[secrets.inject]`, with the value fetched through the provider and
     cached for 5 minutes.
  3. Forward it over TLS to the real host, verifying against the system
     roots, and stream the response back.
  4. HTTP/2 isn't offered to the client (ALPN `http/1.1` only).
- **Logging.** Every connection is recorded:
  `{at, agentId, workspace, host, port, decision: allow|block, injected: bool, bytesUp, bytesDown, durationMs}`.
  Records go in batches to the bridge, which appends them to
  `<state>/network/YYYY-MM-DD.jsonl` and keeps in-memory aggregates per
  host, per workspace and per agent for the last 7 days.
- **Blocked requests** are reported to the bridge, which appends a fleet
  delta `{kind:"network_block", agentId, host, workspace}` and an inbox
  decision (§7.3).

### 6.4 Blocked-request decisions

`POST /api/network/decisions {agentId, host, decision}` handles three
decisions:

| Decision | Effect |
|---|---|
| `block` | Dismiss |
| `allow-agent` | Add a per-agent grant, held in memory for the agent's lifetime, and push a new policy |
| `add-to-workspace` | Use `workspace/patch` on the template's draft to append the host to `[network].egress`, then report the draft as changed. A Studio template gets a draft change. A repo template gets a downloadable patch instead, since the bridge doesn't write to repos. |

`GET /api/network/pending[?agent=<id>]` returns
`{pending: [{kind:"network_block", sessionId, agentId, host, workspace?, at}]}`:
blocks (the `network_block` delta shape, `at` in Unix ms) that no decision
has answered yet, oldest first, for agents that still exist. A page that
reloads rebuilds its prompts from it. Any decision on an (agent, host)
removes it.

Each decision is audited as `network_decision`.

### 6.5 Workspace CA

The CA is generated lazily per workspace with `crypto/x509` and
`crypto/ecdsa` P-256, valid for 1 year, with `IsCA` set and a
`MaxPathLen` of 0.

- **Storage:** the private key, PEM-encoded, is stored only through the
  secret provider at `vault:ca/<workspace>`. With the `env` backend,
  injection is unavailable and the designer says so.
- **Certificate distribution:** the certificate (public) is written to
  `<state>/ca/<workspace>.pem`. The bridge also builds a combined bundle,
  `<state>/ca/<workspace>-bundle.pem`. That file is the bridge host's
  system bundle, from the first path that exists:
  - `/etc/ssl/certs/ca-certificates.crt`
  - `/etc/pki/tls/certs/ca-bundle.crt`
  - `/etc/ssl/cert.pem`

  It is followed by the CA certificate.
- **In the container:** the `<state>/ca/` directory is mounted read-only
  at `/marshal/ca` (a directory, not single files, so the files are seen
  again after the bridge rewrites them). The env sets `SSL_CERT_FILE`,
  `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE` and `GIT_SSL_CAINFO` to
  `/marshal/ca/<workspace>-bundle.pem`, and `NODE_EXTRA_CA_CERTS` to
  `/marshal/ca/<workspace>.pem`.
- **Generations and rotation.** `POST /api/workspaces/{name}/ca/rotate`
  creates a new CA and retires the old one; it does not destroy it. Each
  injecting agent is bound to the generation it started with, and the
  proxy keeps signing that agent's leaves from it, so rotation never
  breaks a running agent. The trust files hold the current CA followed by
  every retired generation still in use. A retired generation is deleted
  once no agent uses it. Agents spawned after the rotation use the new CA.
  No rebuild is needed.
- **Reserved paths.** `ca/` in the secret provider belongs to the bridge:
  the secrets API refuses to write, delete or list it, and egress
  injection refuses refs under it. Vault credentials may reference only
  `git/` (or `env/` on the env backend).
- **Failure.** If the proxy cannot start, workspaces whose network policy
  is `allowlist` or `off`, or that inject credentials, refuse to start
  agents (the error names the proxy). Open workspaces run unproxied.
  `GET /api/network` carries `egressError` while the proxy is down.

## 7. Projects 2.0

`fleet.json` v9 adds `projectSettings`, keyed by project root:

| Field | Meaning |
|---|---|
| `workspace` | The default workspace reference |
| `routing` | A W3 routing object |
| `mode` | Default mode |
| `isolated` | Default isolation |
| `shipTarget` | `merge`, `push` or `patch`; informational, overriding the derived exit destination only when valid |
| `intake` | `{repoId?, labels: [], clients: []}`, mirroring `Repo.watch`/`watchLabel` and the client `allowedRepos` |

**Routes:**
- `GET/PUT /api/projects/settings?root=`;
- `GET /api/projects/health?root=`.

Health returns:

| Field | Source |
|---|---|
| `gateRunnable` | The last stored gate for any agent of the project (W2), or "unknown" |
| `mirrorFresh` | The mirror head age for registered repos (`mirrorHead`, `mirror.go:127`) |
| `orphanWorktrees` | The existing `ProjectStatus` field |
| `trust` | `projectTrust` |
| `workspaceResolves` | Whether the default workspace resolves and builds |

New agent and `POST /api/runs` take their defaults from the project
settings.

## 8. Web UI

### 8.1 Workspaces (`#workspaces`, `#workspaces/<name>/edit|builds|network`)

- **Gallery.** Studio and repo templates as cards, each with:
  - a source badge, the version, contents chips (base, toolchains,
    package count) and usage (agent count);
  - starters, "Import devcontainer.json" and "Snapshot a running agent"
    actions.
- **Designer.** The three-position view switch: Layers | Layers + source
  | Source.
  - **Layers:** nine cards in order. Each card is an editor for its
    section; network shows per-host usage from the network aggregates.
    An edit calls `workspace/patch`, and the source pane updates with the
    changed lines highlighted.
  - **Source:** a text editor with line numbers (no third-party editor).
    Typing pauses for 400ms, then runs `workspace/parse`. Diagnostics
    show inline. Selecting a layer card highlights its `sections` lines,
    and moving the cursor into a section selects that card.
  - **Side panel:**
    - build: size, cold and warm start, pool;
    - verify gate: "runnable" when the toolchains cover the project's
      gate command;
    - policy: mode and allow rules;
    - history: versions, and a diff between any two.
  - **Actions:** Save draft, Publish, Build, Test shell. Test shell is
    disabled with "W5", which adds the terminal.
- **Builds.** A build list, plus a live log viewer on the build SSE.

### 8.2 New agent

The workspace chip lists Studio and repo templates, the project default
first. Each entry shows its contents, pool status ("2 warm"), and a
warning when the verify gate would be skipped.

### 8.3 Network inspector (`#workspaces/<name>/network`, `#chat/<id>` dock-free page `#network?agent=`)

- **Hosts table:** host, rule (allowlisted, granted, open, injected,
  blocked), requests, bytes, agents, last seen. Injected hosts show a
  "proxy can read" badge.
- **Views:** Requests (recent records) and By agent.
- **Blocked requests** appear in the Home inbox's Needs you as decisions:
  Block, Allow for this agent, Add to workspace.

### 8.4 Project overview (`#projects/<root>`)

- **Defaults:** workspace, model, mode, isolation, ship target.
- **Intake:** repo, labels, allowed clients.
- **Health:** the checks listed in §7, each with a status dot.
- **Policies:** the default workspace's `[policy]`, read-only, with a
  link to the designer.
- **Session-sheet tab:** the agent telemetry sections (context, changed
  files, tool stats, rules) of the project's most recent agent.

### 8.5 Settings: secrets, credentials, repos

- **Secrets:**
  - backend status;
  - a list of refs under the owner, with values never shown;
  - set and delete.
- **Credentials:** kind, ref and user.
- **Repos:** registration form and list.
- **Provider keys:** W3's provider "Set key" offers "Store in vault". That
  writes `vault:providers/<name>`, and the provider's base URL host
  becomes an injected host for every workspace (§6.3). The key is never
  passed to agents.

## 9. Testing

| Layer | Tests |
|---|---|
| `workspacecfg` | Parse every section and its diagnostics. Section line ranges. `Patch` preserves comments outside the section and appends missing sections. `Format` round-trips through `Parse`. |
| trust | Hash unchanged without workspace files. Adding or changing a workspace file changes the hash. Untrusted projects ignore repo templates. |
| acp | `workspace/*` methods. `session/new` `policy` sets the mode and session rules. |
| bridge | The template store (versions, draft, delete refused while in use). Resolve and merge rules, each violation included. Dockerfile generation per layer and the cache hit via the fake runner. Build logs on SSE. Spawn args for a workspace (image, mounts, env, network, CA bundle). The setup one-shot. Warm pool take and refill. Devcontainer import and snapshot. Secret backends: local (round-trip, key-file location check, AAD binding) and OpenBao (an `httptest` server emulating login, KV v2 and renewal). Credentials and repos routes. The proxy: CONNECT allow and block, injected-host TLS with header injection (an `httptest` TLS upstream), per-agent auth and IP check, logs, blocked decisions, `add-to-workspace` patch. Project settings and health. Workspace v8→v9 migration. `TestWebIsStdlibOnly`. |
| UI | Designer: layer edits call patch, and the source highlight follows the selection. Diagnostics render. Gallery actions. Build log stream. Network tables. Decision actions. Project overview save. Secrets settings never render values. |

## 10. Acceptance criteria

1. A Studio template with Go and Node toolchains builds. Rebuilding after
   a package change rebuilds only layer 3 and the final layer.
2. An agent spawned on it runs inside the built image, with the declared
   mounts, files, resources and setup.
3. In container mode, an agent can't reach a host outside its allowlist,
   even with `HTTPS_PROXY` unset. A blocked host shows in the inbox, and
   "Allow for this agent" lets the agent's next request through.
4. An injected host receives the credential from OpenBao. The container's
   environment and filesystem contain no copy of it.
5. A repo template with `extends` resolves. Widening a secret or a
   resource is refused with a clear message.
6. Registered repos and vault-backed credentials make push and PR work
   for git-sourced agents.
7. All suites pass as in earlier phases.
