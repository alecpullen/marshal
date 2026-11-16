# Marshal agent Studio integration: MCP transport and identity prototype

Date: 2026-11-16

Status: prototype report for the [implementation plan](plans/2026-11-16-marshal-agent-studio-integration-plan.md). The Task 1 sections record the transport and identity prototype. The **Task 7** section near the end records the joined session-to-factory path, which now runs end to end; the "Remaining gaps" list below is the Task 1 assessment and is closed there.

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

## Task 7: the joined session-to-factory path

The Task 1 report deferred the joined path (`Task 7 owns it`). It now runs, in
two layers: the real endpoint driven by an enrolled session, and a real Marshal
runtime driven through `launch.Run`.

### The defect the real-session test found, and its fix

`TestMarshalStudioRealSessionReadsItsProject` failed on its first run. A
session that had enrolled through the owner-only socket and held `read` on its
own project got an empty `factory_projects` and `not_found` from
`factory_list`.

The cause: only `factory_approve_and_queue` and `factory_answer_question`
consulted a built-in session's Marshal access. `factory_projects`,
`factory_list`, `factory_get`, `factory_create_draft` and
`factory_update_draft` still authorized by client grant alone — and a built-in
session holds no client grant. Task 2's `AuthorizeMarshal*` authorizers had no
production call sites.

Fixed in `f082a4e9`. Those tools now resolve the calling principal once
(`(*Server).toolPrincipal`): an external client keeps its grant and repository
allowlist, and a built-in session is scoped to the project its binding names
and authorized by its effective access, resolved under the policy lock on every
call. The journal request identity and the create/replace admission guards come
from the principal, so a session's own scope names the operation. Both
real-session tests above that found the defect pass since, unmodified in intent.

### What the endpoint tests prove (`web/bridge`)

One real `*Server`, one real factory journal, the real owner-only socket, and a
session that enrolled exactly as `internal/app` does. No fake factory and no
fake `/mcp` endpoint.

| Test | Proves |
| --- | --- |
| `...ReadsItsProject` | An enrolled session holding `read` discovers its project and reads its items through the real tools — the regression above. |
| `...WritesAfterAccessGranted` | A session granted drafting drafts, and the journal attributes the create to its own `sessionId` with an empty `clientId`. |
| `...TwoSessionsDistinctAccess` | Two sessions in one project, one read-only and one granted `createDraft`, get different answers from the same tool; neither lists or can name a second registered project. |
| `...CrossProjectDenied` | Naming another project is refused by `list`, `get`, `create_draft`, `update_draft` and `approve_and_queue`, each refusal names no id and no path, and nothing is journalled. |
| `...RevokedBetweenCalls` | A denial written between two calls is in force at the next: reads and drafting are refused, nothing is journalled, and the reported capabilities are empty. |
| `...RestartRevokesCredential` | After the daemon's generation moves (PID and start time), the old credential is `401`; only re-enrollment mints a working one, and the old token never works again. |
| `...LostResponseReplayed` | A mutation whose response was lost is returned again for the same request id with exactly one journal record; changed content under that id is a conflict, not a second operation. |
| `...ExternalClientAndWorkerUnchanged` | An external MCP client keeps its grant semantics and its provenance bytes exactly; a worker's role and item are recorded but grant nothing, and its approval is journalled with `workerId`/`role` and a `standing` authority origin. |
| `...NoCredentialOrPathLeak` | The enrollment token appears in no tool result, journal entry or captured log line, and no tool result carries this host's project root. |

### What the real-runtime tests prove (`internal/launch`)

A real runtime through `launch.Run` — the composition root `marshal` uses — with
a scripted deterministic provider, a disposable project, and one real bridge
`Server` served on `daemon.PathsFor(home).Socket`. The runtime's own built-in
client speaks the real transport to the real `/mcp` route; nothing is stubbed.

- `TestMarshalStudioRealSessionReadsItsProject`: a real session enrolls over the
  daemon socket, the built-in client attaches (its tools appear in the session's
  tool list, so discovery came from the endpoint's `tools/list`), and a real
  turn calls `factory_projects` and is shown this project's id — with no host
  path in the tool result.
- `TestMarshalStudioRealSessionWritesFollowTheAccessSurface`: the mutation is
  refused while the session holds the read-only default and nothing is
  journalled; writing that session's `approveAndQueue` and `standing` through
  the real access surface (`PUT /api/marshal/access/override`, read-then-write
  against the version the surface reports) makes the next turn's approval land;
  the journal records this session — `sessionId` set, `clientId` empty, this
  instance and project, authority origin `standing`.

One limit is deliberate, and is stated in the test file: `factory_create_draft`
and `factory_update_draft` resolve and preflight their routing against the
bridge's control agent (`config/get`), which a `launch`-level test cannot start
because it owns no marshal binary. Those two tools are therefore driven at the
endpoint level above, where the control agent is the fixture's; the
real-runtime test drives `factory_approve_and_queue`, which authorizes first and
needs no control agent, through the same access gate, journal and provenance.

### Gaps this closes

Items 1–6 of the Task 1 list below are implemented and covered by the tests
named above: per-session enrollment, the built-in client path in the runtime,
the session binding, the revocation lifecycle (a generation change revokes), the
protocol-version interop on the joined path, and the user receipt (task 4, at
the tool level). Item 7 exists as the HTTP access surface
(`/api/marshal/access`), which the real-runtime test drives; the TUI panel that
will present it is not exercised here.

### Not exercised

Stated plainly, so nothing here is claimed on a test that did not run:

- **A live remote Studio.** The remote TLS path is proven only against an
  in-test responder (`TestMCPStudioIntegrationRemoteTLS`, task 1): no network
  server, no real token exchange.
- **The Studio SPA (`web/ui`) and the TUI's Studio panels.** The access surface
  was driven in-process over HTTP; there was no browser or keystroke
  walkthrough.
- **Mobile.** No mobile client exists and none was run.
- **A containerized worker.** No test in this task spawns a worker or a
  container; the factory's effects are never dispatched.
- **A real control agent in the launch test.** As above — `config/get` is
  answered by the bridge fixture at the endpoint level and not at all in
  `internal/launch`.
- **A headless ACP session.** The real-runtime tests use `launch.Run`'s embedded
  path only, not `marshal acp`.
- **A real daemon process.** The launch test serves the bridge `Server`'s
  `SocketHandler` on the daemon's socket path in-process; `webbridge` was not
  started.
- **A real forge, git remote or hosted model.** No GitHub API call, no clone,
  no remote provider: the model is a scripted HTTP server and the repository is
  the fixture's.
- **A `user_directed` approval against a trusted receipt through a real
  session.** That path is proven at the tool level
  (`marshal_user_direction_test.go`, task 4); the real-session tests use
  standing authority.

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

**Confirmed by task 7, not revisited.** The additive work landed on that same
service and policy: a real Marshal session enrolls over the daemon's owner-only
socket, attaches its built-in MCP client, reads its own project and — once the
access surface grants it — writes, with the journal attributing the write to
that session and no external client. The one defect the joined path exposed was
a missing authorization call site in tool handlers (`f082a4e9`), not a limit of
the transport, the identity model or the policy store. No adapter was added and
none is needed.
