# webbridge

The fleet control plane. Serves the REST API, the SSE streams, the MCP
endpoint, and the embedded SPA.

```bash
webbridge --project /path/to/repo --addr 127.0.0.1:7700
```

The listen address defaults to `127.0.0.1:7700`, so the bridge is not
reachable off-host unless you say otherwise.

## Serving over HTTPS

`webbridge` will serve TLS directly when given a certificate and key:

```bash
webbridge --tls-cert /etc/ssl/marshal.crt --tls-key /etc/ssl/marshal.key
```

Both flags are also readable from `WEBBRIDGE_TLS_CERT` and
`WEBBRIDGE_TLS_KEY`. Supplying only one is a startup error rather than a
silent fall back to plaintext.

**Certificate lifecycle is deliberately out of scope.** `webbridge` does
not implement ACME, and does not renew anything. Every deployment already
solves this, and differently:

- **A terminating reverse proxy** (Caddy, Traefik, nginx) in front of a
  plaintext `webbridge` on loopback. The common homelab shape, especially
  with a wildcard certificate.
- **Your own certificates**, passed with the flags above — an internal CA,
  a wildcard for your domain, or `tailscale cert`, which issues a
  publicly-trusted certificate for a `*.ts.net` name and renews it.

When something else terminates TLS, set `--public-url` to the externally
reachable base (`https://marshal.example.dev`). URLs handed to MCP clients
are built from it, so without it they will point at the internal listen
address.

> **Note on `.dev` domains:** the entire `.dev` TLD is on the HSTS preload
> list, so browsers refuse plaintext HTTP to a `.dev` hostname outright.
> Behind a terminating proxy this never comes up; hitting the bridge
> directly over HTTP on such a name will simply fail to load.

## Secrets

Credential values (git tokens, provider keys, workspace CA keys) live in
a secret backend, chosen at startup with `--secrets`:

| Backend | Flags | Notes |
|---|---|---|
| `env` (default) | none | Read-only. `vault:env/NAME` reads `$NAME` at use time. |
| `local` | `--secrets-key-file F` | AES-256-GCM in `<state-dir>/secrets/store.json`. The key file holds 32 raw bytes, must be mode `0600`, and must sit **outside** `--state-dir`. |
| `openbao` | `--bao-addr`, `--bao-mount` (default `secret`), `--bao-role-id-file`, `--bao-secret-id-file`, `--bao-ca-file` | KV v2 with AppRole login; the token renews at half its TTL. |

Every flag also reads `WEBBRIDGE_SECRETS`, `WEBBRIDGE_SECRETS_KEY_FILE`,
`WEBBRIDGE_BAO_ADDR`, `WEBBRIDGE_BAO_MOUNT`,
`WEBBRIDGE_BAO_ROLE_ID_FILE`, `WEBBRIDGE_BAO_SECRET_ID_FILE` and
`WEBBRIDGE_BAO_CA_FILE`.

Create a key file for the `local` backend with:

```bash
webbridge secrets init-key /etc/marshal/secrets.key
```

It refuses to overwrite an existing file. Values are written through
`PUT /api/secrets/vault:<path>`, are never returned by any route, and
are never logged: the audit log records `secret_set` and
`secret_deleted` with the ref only.

## Egress proxy

Agents reach the network through a proxy that enforces a per-agent
policy, injects credentials the agent never sees, and logs every
connection. `--egress on` (the default; `WEBBRIDGE_EGRESS=off` or
`--egress off` disables it) starts it at bridge start:

- **Container mode.** The bridge creates an internal network
  (`marshal-agents`), builds `marshal-egress:<version>` from a
  Debian base plus this binary, and runs it as the `marshal-egress`
  sidecar, also attached to the outside network. Agent containers join
  `marshal-agents` only, with `HTTP(S)_PROXY=http://<agent>:<token>@marshal-egress:3128`.
  The sidecar is the same binary: `webbridge egress --listen :3128 --control unix:///egress/control.sock`.
  The bridge serves the control link on `<state-dir>/egress/control.sock`
  (mode 0600): `GET /policy` streams policy snapshots, `POST /log` takes
  connection records, `POST /blocked` reports refused requests, and
  `GET /leaf` mints a leaf certificate, so the sidecar never holds a CA key.
- **Process mode** (no container runtime). The same proxy runs in the
  bridge on `127.0.0.1:<port>` and agents get the proxy variables.
  A process can ignore them, so this is advisory, and `GET /api/network`
  reports `processMode: true`.

Agent tokens are derived from the agent ID with a key kept in
`<state-dir>/egress/token.key` (mode 0600), so a container that outlives
a bridge restart keeps authenticating.

Per-agent modes are `off`, `open` and `allowlist`. A workspace with no
`[network]` is `open`. Injecting a credential into a host needs a
writable secrets backend (`local` or `openbao`), because the workspace CA
key lives in the vault at `vault:ca/<workspace>`; with the `env` backend
a spawn that asks for injection is refused. Provider keys stored at
`vault:providers/<name>` are injected as `Authorization: Bearer` for the
provider's host in every workspace, and provider hosts are added to
allowlists automatically.

Connection records go to `<state-dir>/network/YYYY-MM-DD.jsonl`; the
bridge keeps aggregates for 7 days and deletes files after 30.
