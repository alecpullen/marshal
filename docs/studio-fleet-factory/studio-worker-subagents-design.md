# Studio worker subagents

Date: 2026-11-16
Status: approved product design. See the [specification](studio-worker-subagents-spec.md)
and [implementation plan](plans/2026-11-16-studio-worker-subagents-plan.md).

## Intent

Marshal agents used as factory workers retain the capabilities of ordinary
Marshal sessions, including delegating to subagents. Make that work reliable
and understandable in Studio, for both Fleet sessions and factory workers.
Users should be able to follow and influence delegated work from a phone or
desktop, with terminology and interactions recognizable from Marshal's TUI.

A factory worker is a supervised session, not a child of the planner's
`agent.run` call. A worker's own subagents belong to that worker. The factory
continues to own the work item, its implementer and independent reviewer, and
the transitions between their stages. A subagent's report is not a substitute
for the factory's independent review.

## Approach

Use the existing shared Studio session experience as the home for subagent
interaction, and extend the factory work-item view with a compact worker and
subagent overview. Studio already has transcript drill-down and breadcrumbs;
the TUI supports entering a subagent from its parent transcript. Reuse those
concepts and, where practical, the same components and agent-side projections.
The work-item overview and session transcript are complementary screens.

A separate factory-only delegation screen was considered. It could provide a
specialized overview but would duplicate the session experience and leave
Fleet users without the same improvements. It is not the chosen approach.

## Navigation and interaction

The factory work item shows each worker's role, state and current subagents,
including their names, tasks and statuses, and provides access to that worker's
session. A user's path can be work item → implementer → subagent; a Fleet
user can enter the same session and subagent views without a work item.
Returning to the parent preserves context. Show completed and failed children
as well as running work, with a clear way to inspect their reports or activity.
Distinguish the factory reviewer from a review subagent of the implementer.

Within the shared session experience, a user can open a subagent's transcript,
steer it directly and request that it stop. The child view identifies its
parent, current status and whether a requested action has taken effect. Record
user instructions in the child's activity, and notify the parent worker of
the instruction and the child it was sent to, so it can coordinate accordingly.
Distinguish a pending or failed delivery from one accepted by the child; do not
claim a stop succeeded until observed. These are direct user actions, not
permission for the child to exceed the parent's authority. Use the existing
Marshal cancellation, steering, policy and session ownership mechanisms rather
than a parallel factory-specific agent system.

Use familiar TUI concepts, labels, statuses, transcript drill-down and
breadcrumbs while adapting layout and touch targets to Studio. On narrow
screens use a stacked navigation flow rather than requiring simultaneous
panes. Controls must be keyboard-accessible, expose status without relying on
color, and preserve the user's place when entering or returning from a child.
Make running, completed, failed, stopping, unavailable and unknown states
understandable. A missing or unavailable transcript must not look like an
empty successful result.

## Configuration

Marshal's normal project/session subagent behavior is the starting point.
Project factory defaults can override subagent settings separately for the
implementer and reviewer; each work item can in turn override its project
factory defaults. Values not explicitly overridden continue to follow their
source when it changes. Expose the inherited value, its source and a way to
restore inheritance, alongside the existing agent defaults in the combined
project settings experience. Work-item overrides belong in the existing
work-item configuration and approval flow. Inherited values follow their
sources while the item is a draft. Approval freezes the resolved settings with
the rest of the approved run configuration; later default changes apply to
future approvals, not silently to an approved or active run. Changes to an
active run use its existing revision and approval flow.

Settings should cover whether delegation is enabled, its model selection and
concurrency, subject to capabilities and existing runtime limits. Preserve
ordinary Marshal defaults when there is no override; do not invent a new
factory-only default. Unsupported choices must be labeled rather than silently
claimed to work. A worker decides when delegation is useful, and the user may
steer directly; enabling delegation does not require the worker to use it.

## Factory lifecycle and authority

The factory must not advance a worker's stage based on a final report while
that worker has relevant delegated work running or its work state is unknown.
Show **Waiting for delegated work**, identify what remains, and allow the user
to inspect or stop the child. Once children have actually finished or stopped,
automatically resume the parent if its turn ended, prompting it to incorporate
the results and produce the report the factory needs. If a child failed, the
parent assesses the failure and can continue or request input. The factory
must not assume the parent has incorporated a late result. Do not
advance on an unconfirmed cancellation or unknown work state. Handle restart,
reconnect and interrupted turns without treating missing observations as
completion or blindly re-executing work.

Stopping a factory worker must account for its children and report the actual
outcome. It must not stop unrelated sessions. The implementer's children act
within the implementer's authority; the reviewer's children remain within the
reviewer's authority. Preserve the existing independent review, candidate
identity, worktree, permission and publication checks. If a direct user
instruction changes the work, it does not bypass those checks.

## Other worker backends

The first target is Marshal's own runtime and its existing subagent behavior.
Keep the worker abstraction open to ACP-backed workers, but do not represent
Claude's or another agent's internal delegation as available, steerable or
observable until its adapter actually exposes and verifies those capabilities.
If a capability is unavailable or a child cannot be observed, show that
clearly. This design does not require a direct Claude SDK integration.

## Validation and boundaries

Prove the full flow with a real Marshal worker session and delegated child,
not just a mock of factory effects. Exercise implementer and reviewer
authority, inherited and overridden configuration, direct steering and parent
notification, stop confirmation, late reports, active or unknown children,
restart/reconnect, and independent factory review. Exercise the shared
session experience for both Fleet and Factory, including mobile navigation,
keyboard access and an unavailable child transcript. Retain the existing
`web/` ACP boundary and account for the in-flight daemon conversion rather
than adding direct imports from `web/` into `internal/`.
