# Marshal agent Studio integration: MCP transport and identity prototype

Date: 2026-11-16

Status: Task 1 prototype report for the [implementation plan](plans/2026-11-16-marshal-agent-studio-integration-plan.md). It records what was actually run and what it proved; it is not a claim that the joined session-to-factory path works.

## What this task added

- `internal/tools/mcp/http_client.go`: `func WithHTTPClient(hc *http.Client) HTTPClientOption`.
  The option replaces only the client's source of `Do` — a Unix-socket dialer, a
  pinned certificate pool, a timeout. A nil client is ignored. Nothing else moves:
  the endpoint is still the configured URL, the token-source rules
  (`WithHTTPOAuth`, one consultation per POST, `*oauth.ErrAuthRequired` on an
  empty token or a 401) are unchanged, and `ValidateRemoteServer` remains the gate
  for a configured remote server. No test bypasses that gate.
- `web/bridge/mcp_factory_integration_test.go`: `TestStudioIntegrationMCPOverOwnerOnlySocket`,
  which serves the real bridge `Server` on an owner-only Unix socket.
- `internal/tools/mcp/http_client_test.go`: `TestMCPStudioIntegrationUnixSocketClient`
  and `TestMCPStudioIntegrationRemoteTLS`, which drive the production
  `mcp.HTTPClient` over an injected transport.
- `internal/daemon/client.go`: **not touched**. The dial seam this task needed is
  the injected `*http.Client`; `daemon.Client` already dials the socket for its own
  routes, and no MCP request travels through it. Adding an exported seam there
  that nothing tests would have been dead surface.

## Why the proof is in two packages

`TestWebIsStdlibOnly` (`web/bridge/boundary_test.go`) parses every `.go` file
under `web/`, `_test.go` included, and has no allowlist: `web/bridge` may not
import `marshal/internal/tools/mcp`, and it must not gain an exception for
this. So no single test can both serve the real handler and drive the
production client. The two halves are therefore proven separately, over the
same JSON-RPC wire:

| Half | Package | Endpoint | Client |
| --- | --- | --- | --- |
| Server | `web/bridge` (real `*Server`) | real `mcpHandler`, on a Unix socket | fixture stdlib JSON-RPC client |
| Client | `internal/tools/mcp` (production `HTTPClient`) | in-test MCP responder | `mcp.HTTPClient` |

The joined end-to-end path — a Marshal session's `HTTPClient` against the
bridge's real handler — was **not** run in this task and is not claimed. Task 7
owns it.

## Tests run and their actual results

Command (the task's verification, run twice: once as specified, once with `-v`
to capture per-test results):

```bash
CGO_ENABLED=1 go test ./internal/tools/mcp ./internal/daemon ./web/bridge \
  -run 'Test.*(MCP|StudioIntegration)' -timeout 10m
```

Non-verbose result:

```
ok  	marshal/internal/tools/mcp	0.012s
ok  	marshal/internal/daemon	(cached) [no tests to run]
ok  	marshal/web/bridge	(cached)
```

Per-test result (`-v`, same packages and regex):

```
=== RUN   TestMCPStudioIntegrationUnixSocketClient
--- PASS: TestMCPStudioIntegrationUnixSocketClient (0.00s)
=== RUN   TestMCPStudioIntegrationRemoteTLS
--- PASS: TestMCPStudioIntegrationRemoteTLS (0.00s)
ok  	marshal/internal/tools/mcp	0.010s
ok  	marshal/internal/daemon	0.002s [no tests to run]
=== RUN   TestStudioIntegrationMCPOverOwnerOnlySocket
--- PASS: TestStudioIntegrationMCPOverOwnerOnlySocket (0.00s)
ok  	marshal/web/bridge	1.262s
```

Nothing skipped: both socket tests ran on this host (a Unix socket was
created), and neither TLS nor socket test read the developer's config,
credentials, environment or a running service.

### 1. `TestStudioIntegrationMCPOverOwnerOnlySocket` — the real handler on the local socket

- Serves `fx.srv.ServeHTTP` on `net.Listen("unix", sock)` from an `http.Server`;
  that is the real bridge `Server`, whose mux mounts `POST /mcp` to
  `(*Server).mcpHandler`, with the fixture's real factory, journal and grant.
- The socket lives in a `t.TempDir()`-rooted directory whose mode the test sets
  to `0700` and asserts is owner-only, with the absolute path kept under 100
  bytes; the test skips rather than fails on a platform that cannot host a Unix
  socket.
- Drives it over real JSON-RPC: `initialize` negotiated `2025-06-18` (the
  bridge's version, not the one requested), `notifications/initialized`
  returned 202, `tools/list` advertised the seven factory tools and no
  authority tool.
- **Authorized read:** `factory_projects` returned exactly the one project the
  client's token is granted, with a capability, and no host path.
- **Identity is the bearer's:** the bridge's shared API token (the one that
  unlocks `/api`) was refused `401` at `/mcp`, and so was an unknown token.
  An enrolled but ungranted client listed no project and got `not_found` for
  `factory_get`, naming neither the item nor the project.

### 2. `TestMCPStudioIntegrationUnixSocketClient` — the production client over a socket

The endpoint here is a small in-test MCP responder (initialize, `tools/list`,
`tools/call`, behind a bearer check). It is not the bridge: this package cannot
import `web/`'s test fixture, and `web/`'s boundary forbids the reverse. What
the responder lets this test prove is the production client and manager path
rather than a raw JSON-RPC call:

- `NewHTTPClient(... WithHTTPClient(unixDialer), WithHTTPOAuth(sessionToken))`,
  then `Start`: `initialize` and the notification both reached the socket.
- Tool discovery through `Manager.RegisterTools` (not a bare `Call`) registered
  `mcp.marshal-studio.factory_projects`, and an authorized read dispatched
  through the registry — the path a session's tool call takes — returned the
  endpoint's granted project.
- Every request that reached the endpoint carried `Authorization: Bearer
  <session token>`; the credential is per request, not a header frozen at
  construction.
- An unauthenticated client was refused with the endpoint's `401`, and no
  method was reached; a token source yielding a token the endpoint does not know
  produced `*oauth.ErrAuthRequired` with no method reached.

### 3. `TestMCPStudioIntegrationRemoteTLS` — the configured remote

- `ValidateRemoteServer` accepted `{type: "http", url: https://127.0.0.1:..., trust: "unrestricted"}`
  and refused the same endpoint without the trust acknowledgment. The injected
  transport changes nothing about that gate.
- Trust is explicit: a `RootCAs` pool holding only that `httptest.NewTLSServer`
  certificate, on an explicitly constructed `*http.Client`. No environment
  credential and no live network.
- An explicit token source (a literal in the test) supplied the bearer;
  initialize, discovery and the authorized read all succeeded.
- **The token's value appears nowhere it should not:** it is absent from every
  registered tool's name, description and schema, and from all client and
  manager log lines captured during the run. A refusal error does not carry it
  either.
- **Endpoint identity, wrong instance:** instance A's token presented at
  instance B was refused `*oauth.ErrAuthRequired`, and instance B answered zero
  methods.
- **Endpoint identity, certificate:** a client verifying the chain against a
  different name failed with a certificate verification error (not an
  authorization error), before any request was written.

## Transports proven

- Owner-only Unix domain socket, `0700` directory, `sockaddr_un` path under 100
  bytes, HTTP/1.1 POST carrying JSON-RPC (Streamable HTTP, single JSON response
  body; no SSE stream in these tests). Real bridge handler on the server side;
  production `mcp.HTTPClient` on the client side, in the two halves above.
- TLS over TCP to an `httptest` server with an explicitly pinned certificate
  pool, per-request bearer credentials, and no environment-sourced trust or
  credential.
- No loopback TCP proxy anywhere; no stub standing in for "the MCP path works"
  on the strength of a mock alone — the socket half runs the real handler,
  fixture, journal and grant.

Not proven here: SSE-framed responses, the joined client-against-real-handler
exchange, and the bridge's protocol-version header enforcement (see below).

## Remaining gaps

**Session enrollment and attestation.**

1. **No per-session enrollment exists.** `/mcp` authenticates an enrolled MCP
   client token (`bridge.MatchClient` over the workspace's clients), and the
   shared daemon/API token is deliberately refused — proven above. Nothing in
   `internal/daemon`, `internal/fleetclient` or `internal/app` mints a client
   record and token for a Marshal session, delivers that token to the session, or
   records which daemon-issued session it belongs to.
2. **No built-in client path in the runtime.** `mcp.Manager` builds its clients
   from `[mcp.servers]` in config. Nothing constructs a config-independent,
   session-scoped `HTTPClient` with the socket transport and the session's token
   source, which is the shape the spec calls a built-in client.
3. **No session binding.** A bearer proves possession only. The bridge binds a
   token to a client and a grant to `(client, project)` — `MCPFactoryProjectGrant`
   has no session, instance, worker-item or role field — so a stolen or replayed
   token would bind to whatever session presents it. The spec's required binding
   (daemon-issued session, selected instance, canonical project identity, worker
   item/role) has no implementation or test yet.
4. **No revocation lifecycle.** Grant revocation per client is proven elsewhere
   (`TestMCPFactoryEndToEndRevokedAndOffline`), but nothing revokes or expires a
   session's client when the session ends, restarts, or moves.
5. **Protocol-version interop is untested in the joined path.**
   `HTTPClient.Start` requests `2024-11-05` and never sends `MCP-Protocol-Version`;
   the bridge negotiates `2025-06-18` and accepts an absent header
   (`mcpVersionAccepted`). That tolerance is what would let today's client talk
   to today's handler, but only the bridge-side test exercises the real handler,
   and it echoes the negotiated version itself.
6. **No user receipt.** Provenance (`clientId`, `projectId`, `action`,
   `requestId`, authority) is recorded and tested, but no trusted user-action
   receipt exists anywhere yet, so a session's mutations cannot claim verified
   user direction — only client assertion plus an explicit approval tool call.
7. **No user-visible surface for a session-provisioned client.** The Studio
   editor manages grants per registered client id; nothing would show a human
   which session holds which client, or let them revoke it from the session's
   own screen.

## Decision

**Decision: MCP-first.**

The evidence that decided it: on the owner-only Unix socket the real bridge
handler resolved a per-client bearer to an MCP identity — refusing the shared
API token and an unknown token with `401` — and that identity authorized a real
factory read returning only its granted project. On TLS, the production client
carried a per-request token with explicitly pinned certificate trust, was
refused at the wrong instance, and leaked the token into no tool description or
log. Both transports therefore can carry authenticated per-request identity;
what is missing is enrollment, binding, receipts and lifecycle — additive work
on the same service and policy, not a transport incapability. A thin
same-service adapter would still have to invent that enrollment and would add a
second client path beside the existing MCP one, so it is not the choice here.
