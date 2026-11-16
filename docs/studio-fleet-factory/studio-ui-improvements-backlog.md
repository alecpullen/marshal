# Studio UI improvement backlog

Date: 2026-11-17

These slices build on the implemented Fleet-first Studio design and the baseline captured from `main` at `47fa29d7`. Execute them in order; each implementation must rebase on the current tree and reconcile in-flight changes. The six specifications and plans were approved by the user on 2026-11-17; implementation is not yet claimed.

| Order | Slice | Status | Scope |
|---|---|---|---|
| 1 | [Visual foundation](plans/2026-11-17-studio-visual-foundation-plan.md) | Approved | Fix the phone overlay first; improve contrast and shared visual primitives; make the session header responsive. |
| 2 | [Work-item and worker experience](plans/2026-11-17-studio-work-item-experience-plan.md) | Approved | Put decisions ahead of optional revisions; make worker identity and activity clearer; preserve separate completion and review decisions. |
| 3 | [Project settings and navigation](plans/2026-11-17-studio-project-navigation-plan.md) | Approved | Improve project orientation and section navigation; provide a structured CI-check editor and optional recorded-evidence suggestions. |
| 4 | [Planning, drafts and handoff](plans/2026-11-17-studio-planning-handoff-ux-plan.md) | Approved | Connect the existing planning and draft journeys, clarify cross-device save state, and make captured handoff evidence easier to review. |
| 5 | [Shared agent conversation](plans/2026-11-17-studio-agent-conversation-ux-plan.md) | Approved | Improve transcript hierarchy, prompt/steering and pending requests in Fleet and Factory sessions; integrate separately planned subagent controls. |
| 6 | [Fleet attention and recovery](plans/2026-11-17-studio-fleet-attention-ux-plan.md) | Approved | Bring factory work into Inbox and make per-source freshness, actions and recovery clear across projects. |

The work-item and conversation plans must coordinate with the [worker-subagent plan](plans/2026-11-16-studio-worker-subagents-plan.md); the project plan must preserve the existing [project factory settings design](project-factory-settings-ui-design.md) and any later subagent or integration settings. The planning slice uses the existing planner and working-draft contracts. The Fleet slice must reconcile the in-flight daemon reconnect changes. None of these slices changes factory authority or substitutes mocked presentation for live backend evidence.

Screenshots in the [foundation design](studio-visual-foundation-design.md) identify the unmodified desktop and phone baselines. The phone diagnostic image is explicitly labeled: it removes the opaque navigation background in a temporary preview to show the content underneath.
