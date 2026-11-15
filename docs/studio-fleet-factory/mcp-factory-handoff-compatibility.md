# MCP factory handoff — compatibility report

Date: 2026-11-16 (Australia/Sydney)
Branch: `mcp-factory-handoff`, base `b79748c`
Specification: [MCP factory handoff specification](mcp-factory-handoff-spec.md) §5–§6

This report records what the documents-only MCP factory handoff was actually
run against: the protocol revision and transport `/mcp` speaks, the
authentication setup it expects, the machine client that was exercised end to
end, and what was not exercised. Where no client was run, the row says
**untested**. That is missing evidence, not a prediction of failure, and this
report makes no interoperability claim beyond the fixtures named in §4.

## 1. Summary

| Question | Answer |
|---|---|
| Protocol revision | `2025-06-18` — the one revision `/mcp` speaks |
| Transport | Streamable HTTP; JSON-RPC 2.0 over `POST /mcp`, loopback |
| Authentication | An explicitly configured per-client bearer token. No OAuth authorization-server discovery, no automatic client enrolment |
| Exercised by a protocol-conformant client | yes — three Go tests over a real loopback `httptest` listener (§4) |
| Claude Code | **untested** |
| A second, independent MCP client | **untested** |
| Studio grant editor and provenance display | exercised by automated tests and `svelte-check` (§6) |
| Desktop and 390px browser walkthrough | **untested** |

## 2. Protocol and transport as implemented

`/mcp` is registered as `POST /mcp` on the existing bridge mux. The handler is
`web/bridge/mcp.go`'s `mcpHandler`; the wire constants are
`mcpProtocolVersion = "2025-06-18"` and `mcpProtocolVersionHeader =
"MCP-Protocol-Version"`.

| Requirement | Behaviour |
|---|---|
| Initialization | `initialize` always answers `protocolVersion: "2025-06-18"` and advertises `capabilities.tools` only. A client that requested another revision in its `initialize` params reads the supported revision from the result and uses it for the rest of the session. |
| Version headers | The negotiated revision is set on every response and accepted, or refused with `400`, on every subsequent request. An absent header is accepted as a client that initialized before the header existed. |
| Notifications | A notification (`notifications/initialized`, or any request without an `id`) is answered `202` with no body. |
| GET | No server-initiated stream, so there is no GET stream; a `GET` (or session `DELETE`) reaches the mux's method matching and is answered `405`. Clients poll with `factory_get` instead. |
| Sessions | No session identifier is issued (`Mcp-Session-Id` is never set or required): every request authenticates on its own bearer token, and there is no server-side session state to expire. |
| `Content-Type` | Required to be JSON (`application/json`, or a `+json` media type); an absent header is tolerated, anything else is `415`. |
| `Accept` | Must allow `application/json`; an absent or wildcard `Accept` is tolerated, otherwise `406`. |
| Origin | Validated by the handler itself, not left to CORS: a browser origin matching the request host (or the configured public URL base) is allowed, any other present `Origin` is `403`, and an absent `Origin` is treated as a non-browser client already covered by its token. |
| Envelope limit | The whole `/mcp` request body is bounded by `factoryRequestLimit` (2 MiB); exceeding it is `413`. |
| Artifact limit | The combined plan and spec inside a request keep the smaller `factoryImportLimit` (1 MiB). |
| Errors | JSON-RPC codes `-32700`, `-32600`, `-32601`, `-32602`, `-32603`. Tool failures are a standard `CallToolResult` with `isError: true` and `{code,message}`. |
| Served surface | Tools only: no resources, no prompts, no push channel. |

The only MCP methods answered are `initialize`, `tools/list` and `tools/call`;
any other method is `-32601`.

## 3. Authentication and authorization

The endpoint authenticates on its own before any handler sees a request
(`authenticateMCP`). It accepts an explicit per-client bearer token matched
against the registered client list; the shared Studio API token is
deliberately rejected, because it carries no client identity and therefore no
repository allowlist, no capability set and no grant.

What that means for a client:

- A client can see only the projects it holds a factory grant for, through
  `factory_projects`. An omitted grant is denial — there is no all-project
  wildcard.
- The existing `Autonomous` flag, an empty `AllowedRepos` list, and being an
  item's creator grant nothing.
- An empty client record (an existing client from before this work, or a
  freshly created one) holds zero grants and can call nothing but
  `initialize` and `tools/list`.
- Permissions cannot be changed over MCP. There is no grant, revoke or
  client-administration tool; grants are created and revoked by the Studio
  operator through the bridge's authenticated management routes.
- A user-directed approval or answer is recorded as a client assertion
  (`user_directed`), not as verified human approval. `standing` approval
  requires the grant's configured authority as well.

This is a bearer-token deployment. It does not implement the OAuth
authorization-server discovery §5 of the specification mentions evaluating: no
OAuth authorization server is advertised, no client is enrolled automatically,
and no dynamic client registration is performed. A client is set up by the
operator creating it in Studio and configuring its token.

## 4. Automated end-to-end evidence

`web/bridge/mcp_factory_integration_test.go` holds the proof, in three tests.
Each one runs a real `httptest` listener speaking JSON-RPC over loopback — the
production handler, not a fake seam — with isolated temporary client and
project stores and disposable repositories. The client that drives it
(`handoffClient`) performs the transport's real opening exchange: `initialize`
with a *different* requested revision (`2025-03-26`), asserting the answer
negotiates `2025-06-18` and carries the `MCP-Protocol-Version` header;
`notifications/initialized` asserting `202`. Then `tools/list` and `tools/call`.

| Test | What it proves |
|---|---|
| `TestMCPFactoryEndToEnd` | A complete authorized handoff. A fake `FactoryEffects` drives the **real** `FactoryController` through `spawn → seed → publish → create_pr → implement`, where `implement` returns `needs_input` with a question. The client then reads the item, answers the question, and the assertion checks the journal's approval hash and question attribution. |
| `TestMCPFactoryEndToEndLostResponse` | Response loss after a durable commit: the retried request produces one durable operation, not two. |
| `TestMCPFactoryEndToEndRevokedAndOffline` | Studio-side grant revocation driven over the same listener's authenticated management routes, and an external client that is stopped while the factory continues from its durable queue with the bridge answering zero requests. |

Across the three, the responses are checked for host-path leakage and for
cross-project leakage, and unauthorized and nonexistent indirect IDs are
required to be indistinguishable.

Measured on this branch:

| Gate | Result |
|---|---|
| `go test ./web/bridge -run '^TestMCPFactoryEndToEnd' -count=1` | `ok` — three tests |
| Repository-wide `go test ./...` excluding `web/bridge` | green at this base; no pre-existing failures to report |
| `go vet ./...` | green at this base |

The end-to-end suite is the compatibility evidence. A protocol-conformant test
client proves the wire format, the envelopes, the version negotiation and the
authorization boundaries; it does not prove that a particular shipped client
configures itself the same way.

## 5. Tool surface

The spec §3 table has **seven** rows, and all seven are registered:
`factory_projects`, `factory_list`, `factory_get`, `factory_create_draft`,
`factory_update_draft`, `factory_approve_and_queue` and
`factory_answer_question`. The plan's task 4 prose said "six tools"; the table
is the contract and all seven shipped. No grant or revoke tool is exposed over
MCP.

New factory tools return a standard MCP `CallToolResult` whose
`content[0].text` is one JSON object. The pre-existing fleet tools keep their
previous result shapes so legacy consumers are unaffected.

## 6. Studio-side surfaces

The permissions editor in `ClientsPanel.svelte` and the provenance display on
the factory item were verified by their automated tests — 168 UI tests across
`ClientsPanel`, `Project`, `FactoryItem` and `api` — and by `svelte-check`.
Measured on this branch:

| Gate | Result |
|---|---|
| `cd web/ui && npm test` | PASS — 91 files, 1037 tests |
| `cd web/ui && npm run check` | 6 errors, 4 warnings in 10 files — all pre-existing at the base commit, in files this work does not touch (`src/views/Chat.svelte`, `src/views/Run.svelte`, `src/lib/Sidebar.test.ts`, `src/lib/fleet.test.ts`, `src/views/Review.test.ts`, `src/views/Runs.test.ts`: `AccessState`/`AgentRow` fixture drift) |
| `cd web/ui && npm run build` | PASS — the bundle in `web/bridge/static` is rebuilt from this source |

What is **untested** here is the visual walkthrough: nobody opened the
permissions editor or the provenance display in a desktop browser and again at
390px, refreshed mid-edit, or observed the revoked-access state in the browser.
The component tests and the type check cover behaviour and shape, not
rendering.

## 7. Live clients: untested

Two clients the specification requires were not executed in this environment:

| Client | Status | Why |
|---|---|---|
| Claude Code | **untested** | No Claude Code MCP session was run. It needs an explicitly configured client credential and a disposable registered project, and neither was supplied here. |
| A second, independent MCP client | **untested** | Same reason; no second client was installed, configured or pointed at the endpoint. |

No version numbers for either client are recorded, because none was observed.
Nothing in this report should be read as a statement that Claude Code or any
other client interoperates with this endpoint — only that the endpoint speaks
`2025-06-18` over Streamable HTTP with a per-client bearer token, and that a
conformant fixture client completes the whole handoff.

Closing that gap needs, per client: a client entry in Studio with a
`read`/`createDraft`/`editDraft`/`approveAndQueue`/`answerQuestion` grant on one
disposable project, that client's token configured, the endpoint pointed at
`/mcp`, and a run of initialize → `tools/list` → create draft → read → approve
and queue → answer a question. Record the client's own version at that point;
do not copy one from this report.

## 8. Reconciliation with parallel work

The implementation base is `b79748c`, which contains:

- Cross-device factory working drafts (PR #88) — merged.
- P5 plans (PR #84) — merged.
- Fleet actions (PR #87) — merged.

The Claude ACP planner (PR #81) is **still open**, so its contracts are not in
this base and no unmerged contract was assumed. This slice adds no second
factory store and no competing route: it reuses the existing
`FactoryController`, journal and `FactoryAPI` that the running bridge already
serves.

## 9. Scope limits of this release

Documents and configuration only. Explicitly out of scope, and not
implemented:

- No project file or selected-edit transfer.
- No merge, push, delete, arbitrary shell or permission-change tools.
- No permission administration over MCP — grants are Studio-only.
- No factory stop/retry, no autonomous recovery, no change to an approved plan.
- No OAuth authorization-server discovery or automatic client enrolment.
- No waking, pushing to, or notifying an external session. Clients poll with
  `factory_get`; factory work continues while they are disconnected.
- All existing factory ownership, preflight, revision, CI and human-merge gates
  are unchanged.
