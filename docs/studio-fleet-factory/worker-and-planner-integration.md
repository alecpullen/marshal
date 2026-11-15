# Workers and external planners in Studio

Status: product direction and research questions, 14 November 2026. This is
not an implementation specification or a change to the existing bootstrap
factory contract. Plan and validate it against the then-current daemon and
factory before building.

## Intent

The user wants the option to do the complete planning and factory workflow in
Marshal Studio, including on a phone where a terminal UI is impractical. An
external Claude Code session remains useful too. Both paths should share the
relevant project and factory information. Continuing the exact same planner
conversation across paths is desirable if feasible, but is not required for
the workflow to be useful.

## Agreed product direction

- Treat workers as a reusable concept that can serve roles such as planner,
  implementer and reviewer, and can support further roles in future factory
  loops. Marshal agents and external agents such as Claude are candidates.
- Persistent workers are useful in some situations. A persistent project record
  is the baseline; persistence of an individual agent conversation is an
  optional capability, not a prerequisite for sharing project information.
- Assemble relevant context automatically when starting or transferring
  planning. The user can inspect and expand that context. The briefing should
  distinguish approved decisions from proposals, link to source material and
  identify missing or stale information. Its exact contents and selection
  algorithm need design.
- Provide one project-level on/off setting allowing the planner to answer
  routine factory questions; default it to off. When enabled, answers should
  follow the approved plan and recorded decisions. Uncertainty, consequential
  new product decisions and scope changes go to the user. Record the question,
  answer and basis in project history. An unavailable planner leaves the
  question visible for the user to resolve.
- The user can interact with the planner and individual workers directly;
  routing all human steering through the planner is not required.

## Proposed architecture

Separate the worker backend (how an agent is started or connected and its
session managed), the role (assignment and expected inputs and outputs), and
the factory controller (workflow state, dispatch, checkout coordination,
evidence and handoffs). A named worker's identity and configuration can outlive
individual sessions and assignments. Whether a session can actually be resumed
is a backend capability, not a promise implied by a persistent worker name.

Use capability discovery and role requirements rather than assuming every
backend supports every role. Planning can be a long-lived conversation; a
factory assignment can be a bounded turn. Factory workers need the appropriate
checkout, observable activity and permissions, a role-specific result,
cancellation and recovery semantics. The controller validates reports and
revision evidence; an agent's assertion alone does not prove completion.
Existing Marshal-specific operations, routing, workspace ownership, lifecycle
and recovery must be reconciled before an external worker can replace a Marshal
worker. Claude uses its own agent and tools; ACP or MCP does not automatically
apply Marshal's native tool policy or sandbox.

### Complementary ACP and MCP paths

Studio can host an external planner or worker through an ACP adapter: initiate
turns, display activity, and handle supported permissions and questions. The
[Claude ACP adapter](https://github.com/agentclientprotocol/claude-agent-acp)
uses the Claude Agent SDK; it is an integration candidate, not a tested
Marshal dependency. ACP session capabilities and behavior vary by backend.

The planned Marshal MCP interface can expose appropriately scoped project
and factory operations to Claude Code running independently, and potentially
to a Studio-hosted agent. Candidate operations include reading context and
progress, submitting artifacts, and handling questions. MCP and Studio should
call the same underlying Marshal services and enforce the same project policy.
MCP tool schemas, authentication, authorization, and the exact handoff contract
remain to be designed. MCP access alone does not wake an external session or
guarantee a tool call; an ACP-hosted session gives Studio a route to initiate
turns while that session is available.

Project knowledge, not an agent transcript, is the continuity baseline:
retain plans, specifications, decisions, questions, relevant results and
their provenance and versions. A new planner receives an assembled briefing
and can access additional material selected by the user. Continuing an old
conversation must not be represented as available until the backend supports
it and session ownership and concurrent access are resolved. Transfer of
planner responsibility retains the existing default of human approval; no
automatic takeover is implied by starting a new session.

## Validation before implementation planning

1. Check the current factory and daemon contracts, especially session and
   workspace ownership, role reports, cancellation and restart recovery.
2. Prove a Studio-hosted Claude planning session, including phone-friendly
   interaction, permissions, reviewable artifacts and a factory handoff.
3. Test MCP against the same services from an external Claude Code session;
   verify scoped access, shared history and offline question behavior.
4. Prove Claude implementer and reviewer assignments with revision-bound
   results, workspace isolation, cancellation and uncertain-outcome recovery.
5. Test persistence and resume separately, including simultaneous terminal and
   Studio access; do not promise same-session continuity without evidence.
6. Check current adapter versions, deployment and authentication terms.
   Anthropic's [Agent SDK documentation](https://code.claude.com/docs/en/agent-sdk/overview)
   currently restricts third-party offerings of claude.ai login or subscription
   rate limits without prior approval. Determine supported authentication for
   the actual product and deployment.

These are proposed validation slices, not completed tests or a commitment to
build every capability at once. The worker interface, context selection,
delegation enforcement, mobile presentation and release order require a
separate implementation-ready design and plan.
