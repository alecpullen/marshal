# Marshal agent access to Studio and Factory

Date: 2026-11-16
Status: approved product design. See the [specification](marshal-agent-studio-integration-spec.md)
and [implementation plan](plans/2026-11-16-marshal-agent-studio-integration-plan.md).

## Intent

Let a user work with Studio's Fleet and Factory from their Marshal agent,
whether the conversation is in the TUI or Studio. An agent can discuss work,
hand an approved plan to the factory, report progress and help resolve a
factory question without the user having to configure an MCP server by hand.
Factory implementer and reviewer sessions are also ordinary Marshal agents
and should be able to use the integration within their own authority.

## Chosen approach

Make the connection a built-in Marshal capability and prototype it using the
existing Studio MCP endpoint and factory service. Prefer one implementation of
factory operations, authorization and durable state. Test the actual TUI,
Studio session and factory-worker workflows, including local and configured
remote deployments. MCP is the first transport to evaluate, not a promise
that it already supplies every required capability. If it cannot provide the
required identity, permissions, lifecycle or user experience reliably, use a
thin native adapter to the same service and authorization semantics. Do not
duplicate factory control logic in the agent.

The alternative is purpose-built native agent tools from the outset. That can
make session identity and interaction easier to integrate, but creates a
Marshal-specific client and additional maintenance alongside MCP. A user
manually configuring the existing MCP endpoint remains possible, but does not
provide the automatic, shared experience sought here.

## Connection and project scope

Make the integration discoverable without manual MCP configuration. A normal
Marshal session and factory workers automatically have read access to the
factory of their current project when the connection and project identity can
be established. Work from a subdirectory belongs to the same repository-root
project, following Marshal's existing project identity rules. Do not infer
that access to one project grants access to other projects.

Show which Studio or daemon is connected, which project it maps to, and
whether the integration is connected, unavailable or needs attention. If
there is more than one possible instance, allow the user to choose another;
do not silently send work to an ambiguous or different factory. Report
unavailable, stale or unverified data honestly. An absent Studio or unsupported
deployment must not prevent normal agent startup or other agent capabilities.
The details of local discovery, transport, client enrollment and remote
connections must be proven by the prototype; do not assume that the current
MCP listener or credential mechanism already works in every daemon profile.

## Permissions and identity

Read access is automatic for the current project for both normal sessions and
factory implementer/reviewer workers. Users can change permissions per
project, and can override the defaults for an individual Marshal session or
worker. The UI shows the effective permissions, their source and the affected
session or worker; restoring inheritance follows the current project default.
The design must permit different sessions and workers to have different
permissions in the same project. No client can grant itself authority.

Keep separate permissions for creating drafts, editing drafts, answering
questions, and approving and queuing work, building on the existing factory
MCP grants. A user-directed approval means the user asked for that action;
standing authority is an explicit user choice, off by default and revocable.
An agent's own tool arguments or an earlier unrelated request do not prove
user direction. When the session can establish the user's request from the
conversation, attribute it to that request without requiring a second Studio
click. If that attribution cannot be established, present it as unverified
and use the applicable user-approval flow; do not claim stronger proof than the
transport supplies. The user's explicit choices can allow a worker to take these
actions too, subject to that worker's existing authority limits. Do not
silently grant write or approval rights just because read is automatic.

The connection identifies the actual Marshal session, project and, for a
factory worker, its work item and role. The factory records the acting session
and its effective authority in the existing provenance/history so the user
can distinguish a user-directed operation from standing or delegated
authority. Do not treat an MCP token shared across all sessions as sufficient
proof of individual session identity. Permissions are checked at each action,
including after revocation, and a disconnected or unverified identity fails
closed for mutations. Securely provision and store whatever connection
credentials the chosen transport requires; do not put them in prompts, logs
or work-item history.

## Agent and user experience

The agent can discover its available factory capabilities and their limits,
then read work items and, when permitted, create or edit a draft, approve and
queue it, and answer a factory question. The initial scope is the currently
implemented factory MCP surface; additional Studio and Fleet actions require
their own explicit capabilities and must not be advertised as already
available. An agent cannot administer its own permissions. Preserve project,
CI, independent review, user approval, workspace and recovery rules.

Users can see and change connection and permissions in Studio and through an
appropriate TUI settings surface. A session shows its connection, current
project and effective access; a user can inspect individual session/worker
overrides. Provide clear actions for setup, switching instances, restoring
inheritance, revoking rights and resolving unavailable or ambiguous access.
The same underlying decisions apply on desktop and mobile. Failed or
uncertain operations retain the service's existing idempotency and recovery
behavior; do not retry a mutation blindly.

Workers are identified as such, and reading or editing a work item never
changes their role or substitutes for the independent factory reviewer. The
integration does not automatically create follow-on factory runs, grant
standing authority, or expose privileged actions merely because a worker is
part of a factory. A user may explicitly grant actions to a worker when they
want that workflow.

## Validation and boundaries

Prototype a real Marshal agent using the actual endpoint and current client,
not only an MCP protocol fixture. Exercise TUI-origin and Studio-origin
sessions and a factory worker; prove local discovery, project identity,
individual permission isolation, revocation, user-directed and standing
authority, provenance, connection loss and restart. Test two sessions in the
same project with different grants and two distinct projects. Assess local
daemon profiles and a configured remote Studio, marking unsupported or
untested cases explicitly. Include the no-Studio and offline cases, and check
that factory workers do not gain extra rights implicitly.

Keep `web/` standard-library-only and do not import agent internals into it.
Use isolated fixtures and credentials for automated tests. If the MCP-first
prototype reveals a gap, document the evidence and select a thin shared-service
adapter; do not infer that direct native tools are required without the test.
