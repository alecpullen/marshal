# W4.3 · Bridge: secrets, credentials, egress proxy — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w4-workspaces-design.md`](../specs/2026-10-03-w4-workspaces-design.md) §6
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** backend. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W4.2](2026-10-03-w4-2-bridge-templates-builds-plan.md) (previous backend plan), with everything it depends on. Backend plans never depend on UI plans.
**Base:** a branch containing every plan listed under Runs after. Anchors into code from before
W1 were checked on `2ddc09e`. W4.2's `workspaceNetworkEnv` hook,
`ContainerConfig.ExtraEnv`/`ExtraMounts`, `ResolveWorkspace` and
`TemplateStore` are as defined there.
**Plan slug:** `w4-3-bridge-secrets-egress`. Commit each task as
`w4-3-bridge-secrets-egress: task N — <title>`.

## Goal

This plan delivers:

- a pluggable secret provider: env, local AES-GCM, or OpenBao;
- persisted credentials and repos, backed by the provider;
- a per-workspace CA;
- the shared egress proxy: a sidecar on an internal network, with
  allowlist and credential injection;
- the network log and its aggregates;
- blocked-request decisions.

## Non-goals

- UI (W4.4, W4.5).
- 1Password and `pass` backends.
- Per-workspace proxies.

## Assumptions

- The bridge stays standard library only: `crypto/aes`, `crypto/cipher`,
  `crypto/ecdsa`, `crypto/x509`, `crypto/tls`, `net/http`.
- `cmd/webbridge/main.go` parses flags in `parseConfig` (`:82`) with a
  `flag.FlagSet`, and treats any positional argument as an error (`:107`).
  This plan adds a subcommand dispatch before `parseConfig`.
- The container runtime seam is `commandRunner` (`container.go:71`). Fleet
  tests use `testFleetWithRunner` (`derive_test.go:105`).

---

## Task 1: Secret provider interface, and the env and local backends

**Goal:** a `SecretProvider` with the `env` and `local` (AES-256-GCM)
backends, plus the key-file safety rule (spec §6.1).

**Files:**
- `web/bridge/secrets.go` (new), `secrets_local.go` (new), `secrets_test.go` (new)

**Steps:**

1. Define the interface from spec §6.1. Add `ParseSecretRef(ref string) (string, error)`,
   which requires the `vault:` prefix and rejects `..` segments and a
   leading `/`. Storage paths are `marshal/<owner>/<path>`.
2. The `envProvider`:
   - `Get` accepts only `env/<NAME>` paths and reads `os.LookupEnv`;
   - `Put` and `Delete` return `ErrSecretsReadOnly`;
   - `List` returns nothing.
3. The `localProvider{path, key [32]byte, mu}`:
   - **Loading.** `NewLocalProvider(stateDir, keyFile)`:
     - reads 32 raw bytes from `keyFile`;
     - fails when `filepath.Rel(stateDir, keyFile)` doesn't start with
       `..`, meaning the key sits inside the state dir;
     - fails when the key file's mode is wider than `0600`.
   - **Storage.** `<state>/secrets/store.json` maps each path to
     `{nonce, ct}` in base64.
     - Each entry is sealed with AES-GCM using a random 12-byte nonce,
       with `AAD = []byte(path)`.
     - Writes are atomic: temp file, then rename.
   - **`List`** filters by prefix.
4. Add `func GenerateKeyFile(path string) error`, which writes 32 bytes
   from `crypto/rand` with mode `0600`. Refuse when the file already
   exists.
5. Tests:
   - local round-trip;
   - a wrong key fails to open;
   - swapping two entries' ciphertexts fails, which proves the AAD binds
     the path;
   - a key inside the state dir is refused;
   - a key with mode `0644` is refused;
   - env `Get` works, and env `Put` errors;
   - bad refs are rejected.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestSecret|TestLocalProvider|TestEnvProvider' -v
```

---

## Task 2: OpenBao backend

**Goal:** a KV v2 provider with AppRole login and token renewal
(spec §6.1).

**Files:**
- `web/bridge/secrets_openbao.go` (new), `secrets_openbao_test.go` (new)

**Steps:**

1. Define `baoProvider{addr, mount string; roleID, secretID string; client *http.Client; mu; token string; ttl time.Duration; renewAt time.Time}`.
   `NewBaoProvider(addr, mount, roleIDFile, secretIDFile, caFile string)`:
   - reads the ID files, trimming whitespace;
   - when `caFile` is set, builds a TLS config with system roots plus
     that CA.
2. `login(ctx)` sends
   `POST {addr}/v1/auth/approle/login {role_id, secret_id}` and reads
   `auth.client_token` and `auth.lease_duration`.
   - `ensureToken` logs in when there's no token, or past `renewAt` (half
     the TTL).
   - When renewing, it first tries `POST /v1/auth/token/renew-self`, and
     falls back to logging in again.
3. Operations, each with header `X-Vault-Token` and storage path
   `marshal/<owner>/<path>`:

   | Operation | Request | Detail |
   |---|---|---|
   | `Get` | `GET /v1/<mount>/data/<path>` | Returns `data.data.value`, base64-decoded |
   | `Put` | `POST /v1/<mount>/data/<path>` | Body `{"data":{"value":"<b64>"}}` |
   | `Delete` | `DELETE /v1/<mount>/metadata/<path>` | Removes every version |
   | `List` | `LIST /v1/<mount>/metadata/<path>` | Use `http.NewRequest("LIST", …)` |

   A 404 maps to `ErrSecretNotFound`, and a 403 forces a single re-login
   and retry.
4. Tests, with an `httptest.NewTLSServer` emulating login, renew-self, KV
   v2 get, put, list and delete, and the 403-then-retry case:
   - every operation;
   - renewal at half the TTL, with an injected clock;
   - a 403 retries once;
   - the CA file option trusts the test server's certificate.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestBao' -race -v
```

---

## Task 3: Wiring the provider, secrets routes, and the key-init subcommand

**Goal:** `webbridge` picks a provider from its flags, exposes
`/api/secrets…`, and offers `webbridge secrets init-key` (spec §6.1).

**Files:**
- `cmd/webbridge/main.go` (`parseConfig` `:82`, `config` struct, `run`, `main` `:194`)
- `web/bridge/fleet.go` (`Fleet.secrets`)
- `web/bridge/secrets_http.go` (new), tests
- `cmd/webbridge/main_test.go` (if it exists; otherwise new), `cmd/webbridge/README.md`

**Steps:**

1. Flags (each with a `WEBBRIDGE_*` env fallback, as the existing flags
   have):
   - `--secrets env|local|openbao`, default `env`;
   - `--secrets-key-file`;
   - `--bao-addr`, `--bao-mount` (default `secret`);
   - `--bao-role-id-file`, `--bao-secret-id-file`, `--bao-ca-file`.
2. Subcommand dispatch. In `run`, before `parseConfig`, when `args[0]` is
   `secrets` and `args[1]` is `init-key`, call
   `bridge.GenerateKeyFile(args[2])` and return. Task 6 adds `egress` the
   same way.
3. Add a `SetSecrets(p SecretProvider)` setter on `Fleet`, called from
   `run` after `NewFleet`. This avoids changing `NewFleet`'s long
   signature (`fleet.go:198`). The env provider is the default when the
   setter isn't called.
4. Routes:
   - `GET /api/secrets/status` returns `{backend, healthy, error?}`.
     Health comes from a `List` on the owner root.
   - `GET /api/secrets?prefix=` returns refs only.
   - `PUT /api/secrets/{ref...}` takes a body `{value}` and returns 204.
   - `DELETE /api/secrets/{ref...}`.

   Use the `{ref...}` wildcard pattern. Every mutation is audited as
   `secret_set` or `secret_deleted`, with the ref only. The value is never
   logged, and never returned.
5. Tests:
   - flag parsing for each backend;
   - `init-key` creates a `0600` file;
   - routes, where GET never includes values;
   - audit entries have no value.

**Verify:**

```bash
go test ./cmd/webbridge/ -v && cd web/bridge && go test ./ -run 'TestSecretsHTTP' -v
```

---

## Task 4: Persisted credentials and repo routes

**Goal:** credentials (including `vault` refs) and repos persist in
`fleet.json` and have CRUD routes. The production credential store is no
longer empty (spec §6.2).

**Files:**
- `web/bridge/credential.go` (`Credential` `:21`, `CredentialStore` `:48`, `Resolve` `:69`)
- `web/bridge/workspace.go` (v9 → v10: `credentials`)
- `web/bridge/fleet.go` (`f.creds = NewCredentialStore(nil)` `:232`)
- `web/bridge/repos_http.go` (new), tests

**Steps:**

1. **Credential kinds.** Add kind `vault` with a `Ref` field
   (`json:"ref,omitempty"`), and a `secrets SecretProvider` field on
   `CredentialStore`, set through `SetProvider`. In `Resolve`, kind
   `vault` fetches the value with `secrets.Get(ctx, ownerID, ref)` and
   sets the unexported `literal`, which `gitEnv` already consumes through
   the askpass path; follow how a `pat` literal flows (`git.go:35`).
   - Change `Resolve` to take a `ctx`, and update every caller
     (`grep -n "\.Resolve(" web/bridge/*.go`).
2. **Persistence.**
   - Bump the workspace to version 10, adding
     `Credentials []Credential "credentials,omitempty"`. The unexported
     `literal` is never serialized.
   - `NewFleet` builds the store from `ws.Credentials()` in place of
     `nil`.
   - Add `PutCredential` and `RemoveCredential` on `Workspace`, which
     save the file and update the live store.
3. **Credential routes:**
   - `GET /api/credentials` returns credentials with
     `set: bool`, computed by a provider `Get` that only checks
     existence;
   - `POST /api/credentials` takes
     `{id, kind, envVar?, keyPath?, ref?, user?}`;
   - `DELETE /api/credentials/{id}` refuses while a repo references it.
4. **Repo routes:**
   - `GET /api/repos`;
   - `POST /api/repos` takes `{id, url, branch, forge, apiBase?, credRef?, watch?, watchLabel?}`,
     validates `forge` against `ForgeFor`'s known values (`forge.go:74`),
     calls `ws.PutRepo`, and audits `repo_registered` (that constant
     exists in `audit.go`);
   - `DELETE /api/repos/{id}` refuses while agents use the repo, and
     audits `repo_removed`.
5. Tests:
   - a vault credential resolves through a fake provider into git env;
   - credentials survive a reload;
   - the v9 → v10 migration;
   - the routes and their refusals;
   - `exit_test.go`'s `registerPATRepo` (`:291`) still works.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestCredential|TestRepos|TestExit|TestWorkspace' -v && go test ./
```

---

## Task 5: Workspace CA

**Goal:** a per-workspace CA whose key is held by the provider, plus a
combined trust bundle and leaf-certificate minting (spec §6.5).

**Files:**
- `web/bridge/wsca.go` (new), `wsca_test.go` (new)

**Steps:**

1. **CA creation.**
   `func (f *Fleet) workspaceCA(ctx, name string) (*x509.Certificate, crypto.Signer, error)`:
   - Load `vault:ca/<name>`: a PEM bundle holding the certificate and a
     PKCS#8 key.
   - When it's missing, generate one:
     - ECDSA P-256;
     - `IsCA`, with `MaxPathLen: 0` and `MaxPathLenZero: true`;
     - `KeyUsageCertSign|CRLSign`;
     - one year's validity;
     - subject `marshal workspace <name>`.

     Store it with `Put`. With the env provider this returns
     `ErrInjectionUnavailable`.
2. **Trust files.** Write `<state>/ca/<name>.pem` (the certificate) and
   `<state>/ca/<name>-bundle.pem`. The bundle is the first existing
   system bundle from spec §6.5, followed by the CA certificate. If none
   exists, the bundle is the CA certificate alone, and a warning is
   logged.
3. **Leaf certificates.** `func (c *caCache) leaf(host string) (*tls.Certificate, error)`
   mints a P-256 leaf with SAN `host` and 24h validity, signed by the CA,
   and caches it per host until an hour before expiry.
4. **Rotation.** `POST /api/workspaces/{name}/ca/rotate` deletes and
   regenerates the CA, rewrites the files, and audits `ca_rotated`.
5. Tests:
   - the generated CA verifies a minted leaf (`x509.Verify` with that CA
     as root);
   - the key round-trips through a local provider;
   - the bundle contains the system bundle (stub the path list) plus the
     CA;
   - the env provider gives `ErrInjectionUnavailable`;
   - rotation changes the serial.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestWorkspaceCA' -v
```

---

## Task 6: Egress proxy core

**Goal:** a standard-library proxy that enforces per-agent policy, tunnels
CONNECT, terminates TLS and injects credentials for injected hosts, and
emits connection records (spec §6.3).

**Files:**
- `web/bridge/egress_proxy.go`, `egress_policy.go`, `egress_inject.go`, `egress_proxy_test.go` (new files in package `bridge`). A subpackage is not possible: `web/bridge` is in the root `marshal` module, so it would be imported as `marshal/web/bridge/egress`, which `forbiddenImport` (`boundary_test.go:68`) rejects.

**Steps:**

1. **Types.** Each name is prefixed with `Egress` because the code shares
   package `bridge`:
   - `EgressPolicy{Agents map[string]EgressAgentPolicy}`;
   - `EgressAgentPolicy{Token string; IP string; Workspace string; Mode string; Allow []string; Grants []string; Inject map[string]EgressInjection}`;
   - `EgressInjection{Header, Value string}` (the value is already formatted);
   - `EgressRecord{At, AgentID, Workspace, Host, Port, Decision, Injected, BytesUp, BytesDown, DurationMs}`.
2. **`EgressProxy`.**
   `EgressProxy{policy atomic.Pointer[EgressPolicy]; leaf func(workspace, host string) (*tls.Certificate, error); records chan<- EgressRecord; blocked func(agentID, host string); upstream *tls.Config}`.
   - `SetPolicy` swaps the pointer, so updates apply live.
   - `ServeHTTP` handles `CONNECT` and absolute-URI requests.
3. **Authentication.**
   - Parse `Proxy-Authorization: Basic base64(agentID:token)`.
   - Compare the token in constant time.
   - Require `RemoteAddr`'s IP to equal `EgressAgentPolicy.IP` when it's set.
   - A failure returns 407.
4. **Decision.** `allowed(p EgressAgentPolicy, host string) bool` applies:
   - `off`: never;
   - `open`: always;
   - `allowlist`: an exact host match, or a `*.suffix` match, against
     `Allow` and `Grants`.

   A denied request returns 403, writes a `block` record, and calls
   `blocked`.
5. **CONNECT** for a host that isn't injected:
   - hijack the connection;
   - dial `host:port` with a 10s timeout;
   - copy in both directions, counting bytes;
   - write the record on close.
6. **CONNECT** for an injected host:
   1. Hijack the connection, and wrap it in `tls.Server` with
      `GetCertificate` from `leaf(workspace, host)` and `NextProtos`
      `["http/1.1"]`.
   2. Loop: `http.ReadRequest`, then set the injection header (deleting
      any client-sent copy first), then send the request upstream with an
      `http.Transport` (system roots, ForceAttemptHTTP2 false), then write
      the response.
   3. Count bytes, and mark the record `Injected`.
7. **Plain HTTP:** absolute-URI requests apply the same decision, then
   forward through `httputil.ReverseProxy` with the `Proxy-*` headers
   stripped. Injection applies to plain HTTP too, but it's discouraged
   in the docs.
8. Tests, using `httptest` upstreams (plain and TLS), a test CA from
   Task 5, and a client configured with the proxy URL:
   - allowlist allow;
   - allowlist block, with a 403 and a blocked callback;
   - `off` blocks and `open` allows;
   - bad auth gets 407, and a wrong source IP gets 407;
   - an injected host's upstream sees the header and the client never
     does;
   - a grant added through `SetPolicy` takes effect without a restart;
   - records have byte counts.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestEgressProxy' -race -v
```

---

## Task 7: Sidecar, control link and spawn wiring

**Goal:** in container mode the proxy runs as the `marshal-egress`
sidecar on the `marshal-agents` internal network, fed by the bridge over
a Unix socket. Agents get the proxy env and the CA bundle. Process mode
runs the proxy in-process (spec §6.3).

**Files:**
- `web/bridge/egress_control.go` (new): the sidecar's client side
- `web/bridge/egresshost.go` (new), `egresshost_test.go` (new): the bridge side
- `web/bridge/container.go` (`buildRunArgs`: `--network`)
- `web/bridge/wsspawn.go` (W4.2's `workspaceNetworkEnv` hook)
- `cmd/webbridge/main.go` (the `egress` subcommand)

**Steps:**

1. **Bridge side** (`egresshost.go`):
   - Serve HTTP on the Unix socket `<state>/egress/control.sock` (mode
     `0600`), using `net.Listen("unix")` and `http.Serve`:
     - `GET /policy` is an SSE-like stream of policy JSON. It sends the
       full policy on connect and after every change.
     - `POST /log` takes a batch of records and appends them (Task 8).
     - `POST /blocked` takes `{agentId, host}`.
   - Hold `agentPolicies`, keyed by agent ID. They're updated at spawn,
     on grant, at stop, and when a workspace's `[network]` changes.
2. **Sidecar** (`egress_control.go` and the subcommand). `webbridge egress
   --listen :3128 --control unix:///egress/control.sock`:
   - connects to the control socket and applies each policy with
     `SetPolicy`;
   - batches records every second;
   - reports blocked requests;
   - gets leaf certificates through `GET /leaf?workspace=&host=` on the
     control socket. The bridge signs them; the sidecar never holds a CA
     key.

   Add the `leaf` endpoint to the bridge side.
3. **Starting the sidecar** in container mode, at bridge start:
   1. `network create --internal marshal-agents`, unless
      `network inspect` finds it.
   2. Build `marshal-egress:<version>` when it's missing:
      `FROM debian:stable-slim` plus `COPY webbridge /usr/local/bin/`.
      The build context is a temp dir holding `os.Executable()`. This is a
      variant of `buildDerived` that does use a context.
   3. Run `run -d --rm --name marshal-egress --network marshal-agents …`,
      with the state-volume subpath `egress/` mounted at `/egress`.
   4. Run `network connect bridge marshal-egress`, so the sidecar can
      reach outside networks.
   5. Reattach when it's already running, and restart it if it exits.
4. **Process mode:** run `EgressProxy` in-process on `127.0.0.1:0`, and
   record the address.
5. **Agent containers:** `buildRunArgs` adds `--network marshal-agents`
   when `ContainerConfig.Network` is set. Leave `Network` empty in process
   mode. `TestContainerRunArgsRefuseHostEscapes` still forbids `host`.
6. **The spawn hook** (`workspaceNetworkEnv`, from W4.2):
   - Create a 32-byte token per agent.
   - Set the proxy env (spec §6.3): the host is `marshal-egress:3128` in
     container mode, or the in-process address in process mode.
   - When the doc has `inject` entries, call `workspaceCA` (Task 5).
     Mount `<state>/ca/<name>-bundle.pem` at `/marshal/ca-bundle.pem` and
     `<name>.pem` at `/marshal/ca.pem`, both read-only, and set the CA env
     vars.
   - Build the agent's `EgressAgentPolicy`:
     - mode and egress from `[network]`. A workspace without `[network]`
       defaults to `open`, which matches today's behaviour.
     - inject values come from `secrets.Get` and are formatted with
       `format`.
     - model-provider hosts are added from the base URLs that W3's
       `config/get` returns, plus every `vault:providers/<name>` key as an
       injected `Authorization: Bearer` for that provider's host
       (spec §8.5).
   - After start, read the container IP with
     `inspect --format '{{(index .NetworkSettings.Networks "marshal-agents").IPAddress}}'`
     and set `EgressAgentPolicy.IP`.
7. Tests, all with the fake runner:
   - the network is created once;
   - the sidecar's run args;
   - an agent's run args include `--network marshal-agents`, the proxy
     env, and the CA mounts when injection is used;
   - the policy contains the agent with its IP after spawn;
   - the control socket streams a policy update after a grant;
   - process mode starts an in-process proxy, and agents get its address.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestEgress|TestContainer|TestSpawnWorkspace' -race -v && go test ./... && go vet ./...
```

---

## Task 8: Network log, aggregates and decisions

**Goal:** connection records are stored and aggregated, blocked requests
become decisions, and `add-to-workspace` patches the template
(spec §6.3–§6.4).

**Files:**
- `web/bridge/netlog.go` (new), `netlog_test.go` (new)
- `web/bridge/network_http.go` (new), tests
- `web/bridge/fleetevents.go`

**Steps:**

1. **`NetLog`.**
   - `Append(records)` writes to `<state>/network/YYYY-MM-DD.jsonl` and
     updates in-memory aggregates keyed by `(workspace, host)` and
     `(agent, host)`: requests, bytes up and down, last seen, decision
     and the injected flag.
   - At startup, it rebuilds the aggregates from the last 7 days of
     files.
   - Files older than 30 days are deleted at startup.
2. **Routes:**
   - `GET /api/network?workspace=&agent=&view=hosts|requests|agents`:
     - `hosts` is `{processMode: bool, rows: [...]}`: the aggregate
       rows, with the rule (allowlisted, granted, open, injected or
       blocked) computed from the current policy. `processMode` is true
       when no container runtime was detected (`detectedRuntime`,
       `fleet.go:434`);
     - `requests` is the last 500 records, matching the filter, from
       today's file;
     - `agents` is the per-agent totals.
   - `POST /api/network/decisions` takes `{agentId, host, decision}`:
     - `allow-agent` adds a grant and pushes the new policy;
     - `add-to-workspace` covers both template sources:
       - **Studio:** run `workspace/patch` (through `controlCall`) on the
         draft, appending the host to `[network].egress`, then
         `SaveDraft`, and return `{draft: true}`;
       - **repo:** return `{patch: "<unified diff>"}` built from the old
         and patched source;
     - `block` only dismisses.

     Decisions are audited as `network_decision`.
3. **Blocked deltas.** On `POST /blocked`, the bridge appends the fleet
   delta `{kind:"network_block", sessionId: agentId, host, workspace}`,
   de-duplicated per `(agent, host)` for 10 minutes.
4. Tests:
   - aggregation;
   - rebuild from files;
   - each decision, including a Studio draft change and a repo patch
     output;
   - delta de-duplication;
   - 30-day cleanup.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestNetLog|TestNetworkHTTP' -v && go test ./... -race && go vet ./...
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./... && go build ./cmd/webbridge
cd web/bridge && go test ./... -race && go vet ./...
cd ../.. && CGO_ENABLED=1 go test ./... ; gofmt -l .
```

Expected:
- every bridge package passes, including `TestWebIsStdlibOnly`, which
  walks all of `web/`;
- the repo-wide run fails only on the five known tests.

Check by hand, in container mode with Docker:
- spawn an allowlist agent;
- `curl https://example.com` inside it fails;
- the request appears in the inbox;
- "Allow for this agent", then retry, succeeds;
- an injected host receives the header;
- `env | grep -i token` in the container shows nothing.

## Integration notes

- **Network isolation changes how existing agents spawn.** Container-mode
  agents spawned after this plan sit on an internal network. Any workspace
  without `[network]` defaults to `open`, so traffic still flows, but
  through the proxy. Agents spawned before it keep the default network
  until they're re-spawned.
- **Secrets backends:** the `env` backend can't hold a CA key, so
  injection needs `local` or `openbao`. The designer says so (W4.4).
- **Workspace version** is now 10.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. Provider, OpenBao, wiring, credentials, CA, proxy core, sidecar, then logs. Each has a test target. The proxy core is tested without containers, through `egress_*` files in package `bridge`. |
| Anchors verified? | Checked on `2ddc09e`: `parseConfig` (`main.go:82`), the positional-argument error (`:107`), `main` (`:194`), `Credential`/`CredentialStore`/`Resolve` (`credential.go:21/48/69`), `gitEnv` (`git.go:35`), `fleet.go:198/232`, `ForgeFor` (`forge.go:74`), `registerPATRepo` (`exit_test.go:291`), `commandRunner` (`container.go:71`), `TestContainerRunArgsRefuseHostEscapes`. The OpenBao KV v2 and AppRole endpoints are the documented HTTP API. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §6, including §8.5's provider-key injection. |
