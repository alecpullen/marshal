# Factory handoff through MCP

Date: 2026-11-15
Status: product design approved 2026-11-15. See the [implementation specification](mcp-factory-handoff-spec.md) and [plan](plans/2026-11-15-mcp-factory-handoff-plan.md), which require review before execution.
Companion directions: [workers and external planners](worker-and-planner-integration.md)
and [Claude ACP planner design (PR #81)](https://github.com/alecpullen/marshal/pull/81).

## Intent

Let a user work with an external planning agent and hand work to Marshal's
factory without switching to Studio for every action. The agent can follow
work and, when authorized, respond to factory questions. The user can inspect
and control the same work from Studio on a phone or computer. Claude Code is
the first and main client to validate; the interface is usable by other MCP
clients and must not depend on Claude-specific commands or extensions.

## Agreed behavior

- Extend Marshal's existing MCP endpoint with factory capabilities rather than
  introducing an independent factory server. Both MCP and Studio use the same
  underlying factory records, approval state and controller. Existing fleet
  MCP operations remain available according to their existing permissions.
- A client can access project factory work beyond the items it created when
  the user grants appropriate permissions. The creator is provenance, not the
  sole access rule. Scope access and authority to a particular MCP client and
  project. A user can inspect, change and revoke these permissions.
- Permissions distinguish reading from making proposals, editing drafts,
  answering questions, and approving or starting work. Having read permission
  does not grant write access. Use only the actions the user grants; an agent
  cannot grant itself more authority.
- An authorized client can approve and start a factory handoff when the user
  asks it to. The user may instead explicitly grant that client standing
  authority for a project. Standing authority is off by default and revocable.
  Approval and start should not require a separate Studio click when the chosen
  authority permits the MCP action.
- Record the client, project, action, relevant work-item and revision, and the
  actual configured authority. Record client-reported user direction as a
  client assertion, not verified human consent. A user may ask an agent in a
  separate app to act, but Marshal cannot independently verify that request.
- The project-level planner-delegation toggle remains off by default. When
  enabled, the designated planner can answer routine questions within approved
  scope; consequential changes or uncertainty are escalated. This does not
  grant permission to answer questions to every MCP client.
- A client need not remain connected for factory execution to continue. If it
  is unavailable, retain questions and show them to the user; MCP alone does
  not guarantee the ability to initiate a turn in that external session.

## Approaches

1. **Extend the existing authenticated MCP endpoint (selected):** add
   factory tools and project-scoped authorization before the existing services.
   This reuses client identity and avoids a second integration, but existing
   repo allowlists and autonomy flags do not by themselves express factory
   authority and must not silently confer it.
2. **Separate factory MCP server:** isolates configuration, but duplicates
   client setup, authentication and access policy while accessing the same
   factory records.

## Proposed flow and authority model

The client discovers the projects and actions it may use, reads relevant
factory context and submits a proposal with the specification, plan and
configuration. Marshal returns a stable work-item identity, status and Studio
link. The client can inspect the exact stored artifact revision and any
validation errors. Changes to a draft must be revision-checked; once approved,
changing scope follows the factory's explicit revision and recovery rules,
not an in-place edit of a running agreement.

A client with the appropriate capability can ask to approve and start the
specific revision. Support two explicit authority modes configured for that
client and project:

- **User-directed:** the client reports it was instructed for this handoff.
  This is an assertion by the client, not independently verified consent. The
  user is trusting the client to honor their instructions.
- **Standing:** the user explicitly authorizes the client to make qualifying
  approval/start decisions without a new instruction for each handoff.

The server checks current permissions and configured authority for every
mutation, binds approval to the precise stored document/configuration revision,
records the mode and principal, and does not treat a tool argument such as
`userApproved: true` as independent evidence of human approval. Missing,
revoked or ambiguous permission fails closed. Provide an ordinary Studio
review path when the client cannot act. A successful response says what was
actually accepted; it does not claim that a background factory effect has
completed. Existing `approve` currently queues a factory item; the eventual
contract must describe the actual state transition rather than promise a
separate start operation where none exists.

Read access includes project factory items, status, relevant artifacts,
questions and outcomes within the granted project. Proposals and draft edits
are distinct from approval/start and from controls on active work. The user
can configure these capabilities separately. Question answers are attributed
and revision/decision checked; a client cannot answer as a delegated planner
unless it is the designated planner and both its permission and the project's
delegation policy permit it. User-directed answers and delegated answers must
not be conflated. Exactly how externally conveyed user instructions are
represented and how conflicts with an existing pending answer are settled
must be pinned in the implementation specification.

The first MCP release hands off documents and configuration only. Selected-edit
transfer is a follow-up, not a prerequisite for this release. The prior Claude
ACP design includes optional selected workspace edits; the later MCP extension
should reuse the same reviewed, immutable handoff semantics when that
underlying capability is available. External paths must never be assumed to
exist on the Marshal host. A future edit-transfer contract must define size
limits, metadata, base identity and conflict handling and check compatibility
with the ACP implementation. Neither path may silently include unrelated
changes or bypass factory checks.

## Compatibility and protocol

The current `/mcp` handler authenticates each request with an MCP-client bearer
credential, returns a tools-only MCP interface and has client/repository
scoping for fleet tools. Its present implementation is not proof of complete
interoperability with every MCP client or version. Verify actual initialize,
tool result/error shape, transport, authentication and token provisioning with
Claude Code and at least one independent MCP client before claiming general
compatibility. Support client capability/version negotiation and explicitly
report unsupported operations. The MCP [tools specification](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)
and [transport specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)
provide protocol requirements; choose and document a tested version rather than
assuming the current fixed version suffices.

Do not expose shared bridge API credentials as an MCP identity. Keep existing
clients working without granting them new factory permissions by default.
Scope both reads and writes server-side, including indirect IDs, and avoid
revealing unauthorized work through errors or links. Token handling, transport
and the MCP [authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization)
must be assessed against the actual deployment and supported clients; do not
claim OAuth conformance solely because bearer tokens exist.

## Failure, concurrency and review

Request IDs and item versions must make retries safe: loss of a response must
not create a duplicate item, approval or run; a stale update must return a
conflict that allows the client to re-read. Approval records preserve the
exact approved inputs and identity/authority at the time of approval. A denied
or failed operation must not start work or broaden permissions. If the
factory's result is uncertain, surface that state rather than automatically
replaying a potentially mutating operation. Revocation affects future actions;
it does not retroactively erase recorded decisions or silently stop running
work.

Clients and Studio may act concurrently. Preserve existing factory ownership,
CI, review, workspace and recovery gates. Neither an MCP tool description nor
an agent's prompt is an authorization control. Display external actions and
client provenance in Studio and factory history; credentials must not appear
in logs, model-visible results or URLs.

## Validation and outstanding implementation design

Use fake clients, isolated credentials, disposable repositories and a fake
factory service for automated tests. Verify per-client/per-project capabilities,
read versus write isolation, unauthorized IDs, approval authority modes,
replay, stale versions, revoked access, concurrent Studio/MCP updates, question
settlement and unknown outcomes. Prove one Claude Code round trip and one
independent MCP client against the actual endpoint; label live and versioned
compatibility tests explicitly. Include an offline-client scenario and a
phone-sized Studio review of provenance and permissions.

The implementation specification and plan must select exact tool names,
schemas, permissions UI, project identity mapping, policy storage, authority
lifecycle, durable provenance and versioning. Selected-edit transport is out of
scope for the first release.
Reconcile against the current Claude ACP implementation and in-flight P5 work
before coding; preserve existing legacy MCP clients and the web/ stdlib-only
boundary. This design does not itself authorize code changes.
