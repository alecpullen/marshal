# MCP factory handoff — feature specification

Date: 2026-11-15
Status: implementation specification and plan approved 2026-11-15, based on the approved [MCP handoff design](mcp-factory-handoff-design.md). This document specifies the first, documents-only release.

## 1. Outcome and boundaries

An authenticated MCP client can discover projects it is explicitly allowed to use, propose and edit a factory draft, inspect factory work and questions across that project, and—with separate, user-configured authority—approve and queue a draft or answer a question. Claude Code is the first live client to exercise; the protocol and semantics do not depend on Claude. Studio and MCP use the same durable factory controller and journal. No client gains factory access from the existing `Autonomous` flag, an empty `AllowedRepos`, or being an item's creator.

The first release transfers plan and specification text and factory configuration, not project file edits. It does not expose merge, force-push, delete, arbitrary shell, permission changes, autonomous recovery, stop/retry or changing an approved plan. It does not rely on the Studio Claude ACP planner being implemented first. Keep all existing factory ownership, preflight, revision, CI and human-merge gates.

## 2. Identity and grants

Keep the existing per-client hashed token and `repoAllowed` check. Add explicit per-client, per-project factory grants; a project is the canonical registered project root already stored on the factory item, mapped to an opaque project ID for MCP (never accept an arbitrary path from a client). The mapping is stable across restarts; registration/removal and symlink normalization must use the same identity rules as existing registered projects. A valid grant names one existing client and one registered project. If a project is removed or a client deleted, existing grants cannot authorize any operation; recreating a distinct identity must not revive a grant silently. Reads and writes also require that the item's repository remains registered and allowed by the client's existing repo policy. A project grant never broadens a non-empty repository allowlist.

Each grant has independent booleans `read`, `createDraft`, `editDraft`, `answerQuestion`, and `approveAndQueue`, all false by default. `approvalAuthority` is `none`, `user_directed` or `standing`, default `none`; `approveAndQueue` also requires a non-`none` authority. Existing client records have no grant after upgrade. The ordinary bearer API token is never accepted as an MCP client identity. Project grants cannot be modified via MCP.

Studio's existing Clients panel supplies a project-specific grant editor: show client name, project, capabilities, authority and explicit disclosure that user-directed instructions are client assertions. Save/revoke through an authenticated bridge management route; do not expose token hashes or regenerate tokens. A grant update has an opaque version and uses conditional write; concurrent edits yield a conflict and retain the user's unsaved changes. An omitted grant is denied, not an all-project wildcard. Keep the old create/list/delete client APIs and legacy fleet MCP behavior compatible.

**Authorization boundary:** authenticate each request, validate a complete request, then immediately before a factory mutation re-read and authorize the current client/grant under the same serialized policy boundary used by grant changes and client revocation, held until the factory journal mutation is durably accepted. A revocation that returns before a mutation is admitted must prevent it. No network or human wait occurs under that boundary. Replays must reauthorize first: a revoked client cannot use an old request ID to retrieve a prior response. Existing running work is not canceled by revocation.

## 3. Tools and wire contract

Add the tools below to the current `tools/list` / `tools/call` endpoint; names are stable and prefixed to avoid the existing fleet `list` and `status`. Tools return MCP `CallToolResult.content` containing one JSON text object. Domain failures return `isError: true` with `{code,message}`; unauthorized and nonexistent indirect project/item IDs both return `not_found` without names, links or version. Invalid requests and protocol failures remain distinguishable. Never return raw tokens, host paths, environment values, secrets, or another project's artifacts. Arguments reject unknown fields, trailing data and oversized inputs; impose the existing 1 MiB plan+spec and request-size limits and document the actual MCP envelope limit. Use a caller-generated nonempty `requestId` on every mutation; scope journal replay identity to `(client ID, requestId)` rather than allowing one client's request ID to collide with another's.

| Tool | Required arguments | Capability and result |
|---|---|---|
| `factory_projects` | none | Lists opaque project IDs and granted capabilities for this client only. |
| `factory_list` | `projectId` | `read`; returns project-scoped item summaries and versions. |
| `factory_get` | `projectId`, `itemId` | `read`; returns stored plan, spec, effective config, item version, state, decision and relevant outcome/history, with access-scoped links. |
| `factory_create_draft` | `projectId`, `requestId`, `title`, `plan`, `spec`, `config` | `createDraft`; config identifies an allowed registered repo and execution choices; project root is resolved server-side. Returns stored draft, item ID, version and Studio link. |
| `factory_update_draft` | `projectId`, `itemId`, `requestId`, `expectedVersion`, `title`, `plan`, `spec`, `config` | `editDraft`; replaces only a draft after normal validation, resolution and preflight. Cannot alter identity/project/repo or an already queued item. |
| `factory_approve_and_queue` | `projectId`, `itemId`, `requestId`, `expectedVersion`, `authority` | `approveAndQueue` and matching configured authority. Atomically records provenance and queues the exact stored draft through the existing approve command. `authority` is `user_directed` or `standing`; user-directed is only a client assertion. |
| `factory_answer_question` | `projectId`, `itemId`, `requestId`, `expectedVersion`, `decisionId`, `answer`, `authority` | `answerQuestion` and authority `user_directed` or `delegated`. The latter also requires the project's delegation toggle and designated client; exact decision/version checked. |

`factory_create_draft` and `factory_update_draft` accept the existing `FactoryConfig` fields excluding `projectRoot`; reject attempts to supply that field or move an existing item. Validate repo ID and all supplied nested fields before resolving. The factory remains responsible for normalization, route/workspace preflight and CI acknowledgement. Return current version only when the caller is authorized to read the item. For an unchanged `(client, requestId, operation, payload)` replay, return the original accepted result; a reused request ID with different contents is `conflict`. If authorization was revoked, deny before replay. Same-item concurrent operations with stale `expectedVersion` conflict. Explicitly account for response loss after durable commit: never resubmit an already accepted operation using a new request ID.

For user-directed approval or answer, the configured capability authorizes the client to assert it received an instruction. The server cannot independently prove the user said it. `standing` approval is accepted only when the grant's authority is `standing`; user-directed is accepted when the configured authority is `user_directed` or `standing`. Log the claimed mode, not 'human approved'. For `delegated` question answers, require `answerQuestion`, the project toggle enabled, and the configured designated planner's client ID. The project toggle is off by default; no delegated client is selected by default. In Studio, expose those two project settings independently from the grant and persist them alongside project settings using its conditional update. For user-directed answers, require `answerQuestion` and log a client assertion; the delegation toggle does not prohibit a user-directed answer.

Current `FactoryController.Command` with `approve` appends approval and changes `draft` to `queued` in one journal mutation. The MCP operation calls that same path; response reports the resulting durable state, never a completed implementation. Answer uses the existing `answer` command, which requires the current matching question decision. Draft replacement is a new state-limited, version-checked controller/store command; validate candidate before commit and retain append-only existing approvals, immutable item/project/repo identity, and existing default resolution semantics. Do not replace a draft by creating a second work item or mutate approved snapshots.

## 4. Durable provenance and observability

MCP create, update, approve and answer append a structured provenance entry in the **same durable journal mutation** as the operation: client ID, opaque project ID, action, request identity, item version and configured/claimed authority when applicable. Approval stores the existing plan/spec hash and exact stored config; answer records the decision ID. The existing Studio path must continue to work with older records lacking provenance. Studio's factory detail presents these entries using client names when available and stable IDs otherwise; show `client reports user-directed` versus `standing authority` or `delegated planner` accurately. No asynchronous audit-only write is evidence of an accepted operation. Failed calls may be audited separately but must not create a success entry.

Client-provided plan/spec, answers, labels and tool results are untrusted content. Preserve exact artifact bytes within existing size limits, render without HTML injection, and do not interpret agent-authored content as a permission change. Do not include credentials in links or returned error details. Nonexistent, unauthorized and revoked IDs must not reveal inaccessible objects through returned content.

## 5. MCP compatibility

Implement and test MCP `2025-06-18` Streamable HTTP on the existing endpoint: initialization/version behavior, POST request and notification handling, GET behavior as prescribed for a server without server-initiated streaming, `Accept` and `Content-Type`, session/version headers as applicable, correct result envelopes, and Origin validation. If another version is requested, return the supported version during initialization as the protocol specifies; validate subsequent protocol-version headers. Preserve existing fleet tool result behavior for legacy consumers while new factory tools use standard MCP results. Test the actual wire format with a protocol-conformant test client, Claude Code and a second independent MCP client. An advertised protocol version is not itself proof of interoperability. No push or wake-up of external sessions is required: clients poll using `factory_get`; normal factory work continues when they disconnect.

The MCP bearer credential remains an explicitly configured token. This slice does not claim OAuth authorization-server discovery or automatically enroll clients. Document transport and setup limitations accurately and evaluate the version's [authorization](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization), [tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools) and [transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) requirements against tested clients. Do not rely on CORS as Origin validation or expose the MCP endpoint without its own authentication.

## 6. Verification and compatibility

Tests use isolated project/client stores, fake factory effects and disposable repos, never ambient credentials or external services. Cover old client records with zero factory grants; distinct clients and projects; allowed and denied repos; all capability and authority combinations; a client changing another client's item only when granted; unauthorized IDs; concurrent revoke/admission; exact artifacts and limits; stale draft, approval and question races; duplicate request and lost response; journal replay including legacy records; and policy persistence across restart. Test that a client cannot modify permissions through MCP, `Autonomous` doesn't grant factory privileges, and an offline external session does not stop work.

Build and exercise the real MCP endpoint using protocol-conformant automated fixtures. A live Claude Code and second-client round trip use explicitly supplied disposable projects and credentials; if unavailable report **untested**, not passed. Test the permissions editor and provenance display at 390px and desktop. Recheck the Claude ACP planner branch, factory defaults and P5 work against the implementation base. That work may introduce shared interfaces; do not duplicate or overwrite them, and never assume its unmerged contracts exist.

## Implementation record

Delivered on branch `mcp-factory-handoff` from base `b79748c`, one commit per
task. The tasks ran in the order 1, 2, 5, 3, 4, 6: task 5 consumes only task
2's management routes, so the Studio controls landed before the journal and
tool work whose files they do not share.

| Task | Deliverable | Commit |
|---|---|---|
| 1 | `MCPFactoryProjectGrant`, stable opaque project identity records in the workspace save, persisted `plannerDelegationEnabled`/`designatedMCPClientId` | `bf21e231` |
| 2 | `factoryMCPAuthorizer` with the shared policy lock, grant management GET/PUT routes, `mcpProjectId` on the Studio project list | `7d089a39` |
| 5 | The grant editor in `ClientsPanel.svelte`, the delegation settings in `Project.svelte`, provenance in the factory history | `7b4f352c` |
| 3 | `FactoryProvenance` appended in the same journal mutation, version-checked draft replacement in `FactoryStore.Update` | `22ca2360` |
| 4 | The seven factory tools and `2025-06-18` Streamable HTTP in `mcp.go`/`mcp_factory_tools.go` | `571f2350` |
| 6 | The end-to-end proof, this record, the README entry and the [compatibility report](mcp-factory-handoff-compatibility.md) | task 6 — verify MCP handoff, committed by the controller after this documentation lands |

Files: `web/bridge/mcp_factory_auth.go`, `web/bridge/mcp_factory_tools.go` and
their tests, and `web/bridge/mcp_factory_integration_test.go` are new;
`web/bridge/client.go`, `workspace.go`, `projects.go`, `http.go`, `mcp.go`,
`mcptools.go`, `factory_types.go`, `factory_store.go`, `factory_controller.go`,
`factory_http.go` and `contract/projects.go` are modified, with the matching
`_test.go` files. In `web/ui`: `src/lib/ClientsPanel.svelte`,
`src/views/Project.svelte`, `src/views/FactoryItem.svelte`, `src/lib/api.ts`,
`src/lib/factory.ts` and the generated `src/lib/contract.ts`, plus their tests.
`web/` stays Go standard library only, nothing under `internal/...` is touched,
and the existing factory approval, preflight and journal paths are unchanged.

### Deviations from the plan, and why

| Planned | Delivered | Reason |
|---|---|---|
| "Register the six tools" | Seven tools — every row of §3: `factory_projects`, `factory_list`, `factory_get`, `factory_create_draft`, `factory_update_draft`, `factory_approve_and_queue`, `factory_answer_question` | The plan's prose miscounted; the §3 table is the contract. No grant or revoke tool is exposed over MCP |
| Task 2's file list did not name `web/bridge/contract` | `mcpProjectId` added to `contract.ProjectStatus` and `contract.ts` regenerated | The field §"Management endpoints" requires in the project-list response belongs to the shared wire contract; the TUI/bridge boundary rule leaves no alternative |
| `WithAuthorizedMutation` only | Added `WithAuthorizedMutationAuthority` beside it | A mutation that must accept `user_directed` only under the grant's configured authority has to learn that authority from inside the policy lock, where it is read |
| One capability name for answers | A delegated answer is authorized through the internal capability `answerQuestion:delegated`; plain `answerQuestion` is the user-directed answer | §3 says the delegation toggle does not prohibit a user-directed answer, so the two must be distinguishable capabilities |
| — | `FactoryProvenance` applied exactly as the approved contract struct, which carries no item-version field | An entry's version is therefore its position in the append-only journal |
| — | A question decision records the worker's question text in `Decision.Reason` | Pre-existing controller behaviour; that is the field Studio already renders and the MCP projection carries. The end-to-end test initially asserted `Decision.Question` and was corrected to assert the field production actually fills |
| — | `web/bridge/factory_controller.go` reformatted with `gofmt` in task 6 | Task 3 committed it with a formatting drift; no behaviour change |
| — | `web/bridge/testdata/contract/ProjectStatus.json` re-recorded | `TestTheWireIsUnchanged` pins the project-list wire byte for byte; `mcpProjectId` is a deliberate contract addition, so the recording was regenerated with `-update` and no other recording changed |
| Raise the `/mcp` envelope to `factoryRequestLimit` (2 MiB) | Delivered as specified; `factoryImportLimit` (1 MiB) still bounds the combined plan and spec | No change; recorded because the two limits are easy to conflate |
| — | Planner delegation is validated at the settings boundary: enabling it with nobody designated is refused, a designation must name a registered client, and deleting a client clears the designations naming it | The plan left the two new fields unvalidated. An inert toggle that looks configured is worse than a refused write, and the cascade means a project is never locked out of saving its own settings |
| — | `factory_list` reports `total` and, past 100 items, `truncated: true`, newest first; the open decision's question and reason are clipped at 4000 runes | Review round. The store orders by random hex ID, so the old silent cap could hide an item indistinguishably from a deletion, and `Decision` was the one projection with no bound |

### Acceptance gates, measured on this branch

| Gate | Result |
|---|---|
| `go test ./web/bridge -run '^TestMCPFactoryEndToEnd' -count=1` | `ok` — `TestMCPFactoryEndToEnd`, `TestMCPFactoryEndToEndLostResponse`, `TestMCPFactoryEndToEndRevokedAndOffline` |
| Repository-wide `go test ./...` excluding `web/bridge`, and `go vet ./...` | green at this base; no pre-existing failures to report |
| `cd web/ui && npm test` | PASS — 91 files, 1037 tests |
| `cd web/ui && npm run check` | 6 errors, 4 warnings in 10 files, all pre-existing at the base in files this work does not touch |
| `cd web/ui && npm run build` | PASS — the embedded bundle in `web/bridge/static` is rebuilt from this source |

### What remains untested

The [compatibility report](mcp-factory-handoff-compatibility.md) holds the
detail. In short: no live Claude Code MCP session and no second independent MCP
client were run (no explicitly configured credentials and disposable project
were available), so both are **untested** and no interoperability is claimed;
and the desktop and 390px browser walkthrough of the permissions editor and the
provenance display — including refresh and the revoked-access state — is
**untested**, with only the component tests and `svelte-check` covering those
screens. The Claude ACP planner (PR #81) was still open, so its contracts are
not in this base; working drafts (#88), P5 plans (#84) and fleet actions (#87)
are merged, and no unmerged contract was assumed.
