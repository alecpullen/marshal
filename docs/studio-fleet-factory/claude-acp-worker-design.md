# Claude planning in Studio and the worker boundary

Date: 2026-11-15
Status: design and implementation plan approved 2026-11-15. Builds on [Workers and external planners](worker-and-planner-integration.md).

## Intent

Let a user hold a Claude planning conversation entirely in Studio on a phone or
computer and hand a reviewed specification and plan to a Marshal factory. The
same broader worker model should eventually support Marshal agents, generic ACP
agents, and a direct Claude Agent SDK integration if justified by evidence.
Project knowledge remains the baseline for continuity even when a conversation
cannot be resumed across sessions or tools.

## Agreed product behavior

- Claude planning in Studio through ACP comes first. The user can converse,
  see activity, respond to questions and permissions, and review the exact
  specification and plan before a factory handoff. The factory continues to
  own its approval, work-item and execution lifecycle.
- The user chooses the planner's access mode. The default is permissive:
  Claude may edit files and execute commands without per-action approval.
  A more restricted mode is available. Clearly show the selected mode at setup
  and during the session. Do not represent Claude's own tools as protected by
  Marshal's native tool policy. Apply the actual execution environment's
  boundaries and accurately surface what each mode controls.
- Start in a separate workspace by default; allow the user to choose an
  existing one. If an existing workspace is in use, warn clearly, offer an easy
  way to create or choose a separate workspace, and let the user proceed with
  shared use. Do not claim exclusivity when it cannot be established. Surface
  unknown occupancy rather than treating it as free.
- Handoff carries documents only by default. The user may optionally select
  planner-workspace edits to include, review their exact contents and confirm
  the selection. Unselected edits remain in the planner workspace.
- Distinguish persistent worker identity and configuration from sessions and
  assignments. Preserve project artifacts and decisions independently of any
  one conversation. Resume an individual session when the backend supports it;
  where it cannot be proved, offer a new session with shared context without
  claiming the previous conversation was resumed.
- Assemble relevant project context automatically, allow the user to inspect
  and add context, and distinguish approved decisions from proposals. Project
  delegation of routine factory questions remains off by default and subject to
  the previously agreed project toggle; enabling it is not required for this
  planner slice.

## Approaches

1. **Generic ACP worker backend with Claude adapter (selected for the first
   proof):** reuse ACP session interaction for multiple agents and test the
   Claude adapter against real planner and factory requirements. Adapter and
   protocol behavior may not expose every Claude-specific capability.
2. **Direct Claude Agent SDK backend:** gives Marshal direct access to SDK
   controls and events, but adds a Claude-specific integration and a helper
   runtime because the Go bridge cannot use the Python/TypeScript SDK in
   process. Build only if the proof establishes a material capability gap.
3. **Claude-specific ACP integration:** can ship the first experience, but
   ties the interface to one agent and makes future ACP or SDK workers harder
   to add. It does not fit the desired worker model.

A worker-facing contract must permit both the generic ACP and potential direct
SDK backends. It must not require every backend to implement every capability;
roles declare their needs, and unavailable, unknown and unsupported behavior
are distinguishable. Model selection, authentication, permissions, context,
streaming, questions, session persistence and lifecycle are capabilities to
negotiate and verify, not assumed identical across backends.

## First delivery and proof

The first product path is a Studio-hosted Claude **planner**, with a shared
worker boundary designed for planner, implementer and reviewer roles. Expose a
working interactive planner and explicit, reviewable handoff into the existing
factory draft flow. Keep Claude factory execution experimental: test bounded
implementer and reviewer assignments on disposable fixtures, but do not offer
Claude as a production factory worker until its role requirements and recovery
semantics are proven. The design does not replace existing Marshal workers.

The planner runs on the execution host in the selected workspace, not on the
phone or in the browser. The bridge/daemon owns the process and session; closing
or suspending a browser must not itself terminate an accepted running turn.
Show connected, disconnected, running, waiting for input, completed, failed
and unknown states according to actual evidence. Never silently replay an
uncertain turn. Verify process restart and reattachment behavior; if continuity
cannot be established, show that fact and offer a new context-based session.

Capture spec and plan as explicit editable artifacts, with provenance and
versions. Make their selection and exact contents visible to the user at the
handoff; user approval remains separate from creating or updating a draft.
Claude's file edits are workspace changes and must not silently become the
factory implementation checkout or be discarded. Report their presence before
handoff and offer an optional selection and diff review. Record the selected
change contents and their base revision with the draft so subsequent workspace
edits cannot silently alter the handoff; show conflicts or missing evidence
before applying anything. The factory starts from that reviewed selection only
after approval, and its delivery branch and revision evidence account for the
selected changes. Unselected edits stay in the planner workspace and are not
implicitly approved. Define safe handling of uncommitted and untracked files,
selection granularity, conflicts and restart recovery in the implementation
plan; do not assume a copied path or live workspace is a durable snapshot.

## Evidence and decision gate

Prototype the generic ACP path using the Claude adapter against a disposable
repository and controlled credentials. Test:

1. Start a planner with a separate workspace; choose an existing busy workspace
   and confirm warning, separate-workspace alternative and explicit proceed.
2. Permissive file edits and commands, a restricted mode's actual permission
   behavior, user questions and handling on a phone-sized Studio viewport.
3. Conversation streaming, interruption/cancellation, browser disconnect,
   process failure and supported session resume; record uncertain outcomes.
4. Review and handoff of exact artifact versions to a factory draft; test both
   documents-only and selected-edit handoffs with unrelated edits, later source
   modifications, conflicts and a fresh-session context summary. Verify the
   factory receives only approved changes and that the recorded snapshot can
   be recovered without the planner workspace.
5. Bounded implementer and reviewer proofs against disposable factory fixtures:
   correct checkout and revision, role result, permissions, stop behavior,
   report validation and recovery after lost acknowledgement. Do not push or
   merge a real repository during the proof.
6. Capability negotiation, diagnostics, supported installation/host platforms,
   credential separation, adapter version and actual SDK authentication terms.
   Anthropic's [Agent SDK documentation](https://code.claude.com/docs/en/agent-sdk/overview)
   describes restrictions on third-party offerings of claude.ai login or
   subscription limits without prior approval; verify applicable terms and
   supported credentials before choosing a shipped authentication path.

Produce a capability matrix with measured pass, fail or untested evidence for
planner and worker roles. Compare concrete ACP gaps with what a direct SDK
backend could actually provide; assess importance, workaround, maintenance and
authentication/deployment tradeoffs. A failed proof is a result, not a license
to hide gaps or silently substitute a different agent. The user decides whether
material gaps justify direct SDK implementation.

## Constraints and builder decisions

Maintain the `web/` stdlib-only and TUI client-seam boundaries. Do not disturb
in-flight P5 ownership or assume its relay makes terminal-started sessions
available to the browser. Compare actual branch and daemon contracts before
planning; integrate through stable seams and recheck the parallel preview and
project-defaults work. Do not implement the external-terminal MCP factory
handoff in this first slice, nor promise conversation portability or support for
other ACP agents until tested. The existing fleet MCP endpoint remains distinct
from that proposed factory handoff.

The implementation planner chooses concrete data and API shapes, workspace
provisioning mechanics, the adapter distribution/update strategy, UI layout,
restricted-mode mapping, and how to bound and reference the project briefing.
Those choices must preserve the agreed behavior above and expose unavailable
capabilities rather than inventing them. Document any material constraint that
would require changing user-visible behavior before implementation.

## Implementation record

Delivered commits, oldest first:

| Commit | Task |
|---|---|
| `103ff32c` | task 1 — worker seam |
| `b2cec63e` | task 2 — ACP worker backend |
| `ad640351` | task 3 — planner service |
| `94108611` | task 4 — capture immutable handoff |
| `e466c983` | task 5 — bind and seed selected edits |
| `c5004e6c` | task 6 — Studio planner |

Task 7 delivers the deterministic proof
(`web/bridge/worker_acp_integration_test.go`), the
[capability report](claude-acp-capability-report.md) and this record.

Deviations from this design, recorded rather than corrected:

- Captured artifact text uses a new `PlannerSnapshotArtifact` type;
  `PlannerArtifact` was already the planner record's reference type
  (`{kind, ref, label}`) in task 3 and was left unchanged.
- `Child.answerRequest` gained `handlerError` so a handler can choose the
  protocol error code (`-32601`); every other error path is unchanged.
- The adapter path is configured by the `MARSHAL_CLAUDE_ACP_ADAPTER` environment
  variable (absolute, no download, no fallback).
- Handoff resolves the factory API through `PlannerService.factorySource`
  (`http.go` untouched); `factoryRoutes` publishes the API.
- Snapshot diffs are persisted in the immutable snapshot record so review
  survives workspace removal; the snapshot hash covers `{baseCommit, files}`
  only.
- File diffs are computed in-process (single-hunk, binary marker), not parsed
  from git output.
- `factoryValidateSnapshot` additionally rejects duplicate paths and any `.git`
  path.
- `factoryGit.beforeSeedWrite` exists solely as a nil-in-production test seam
  for the interrupted-seed proof.
- The Studio planner route is `#planner/new[?project=<root>]` and
  `#planner/<id>`.

Remains untested: real Claude adapter authentication and actions,
process/resume behaviour against a real adapter, the local host platform
matrix, and a real-browser desktop/390px walkthrough (no adapter or credentials
in this environment; no browser session was driven).
