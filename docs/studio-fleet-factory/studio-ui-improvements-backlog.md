# Studio UI improvement backlog — Flutter port

Updated: 2026-11-18. The six product directions were approved on 2026-11-17; their Flutter ports below are proposed for review and have not been implemented by this PR. Implement in order, rechecking the current tree and in-flight work. Each plan builds on the actual Flutter Studio in `app/`, the [normative design system](../studio-flutter/design-system.md), and existing behavior rather than rebuilding parity. The Svelte Studio is being retired; none of these plans targets it. At the time of this port, [F8](../studio-flutter/f8-cutover.md) records that the final route swap is deferred: Svelte is at `/` and Flutter at `/next/`. Check the current cutover record before testing rather than hard-coding either route.

| Order | Plan | Flutter scope |
|---|---|---|
| 1 | [Visual foundation](plans/2026-11-17-studio-visual-foundation-plan.md) | Audit Flutter and improve the shared kit only where evidence warrants it. |
| 2 | [Work-item and worker experience](plans/2026-11-17-studio-work-item-experience-plan.md) | Refine existing asks, worker drill-in and activity. |
| 3 | [Project settings and navigation](plans/2026-11-17-studio-project-navigation-plan.md) | Project orientation and lossless expected-checks editing. |
| 4 | [Planning, drafts and handoff](plans/2026-11-17-studio-planning-handoff-ux-plan.md) | Truthful persistence, capture and approval journeys. |
| 5 | [Shared agent conversation](plans/2026-11-17-studio-agent-conversation-ux-plan.md) | Improve the shared thread and composer without duplicating subagent work. |
| 6 | [Fleet attention and recovery](plans/2026-11-17-studio-fleet-attention-ux-plan.md) | Refine existing Factory asks and per-source freshness in Overview and Inbox. |

The [worker-subagent plan](plans/2026-11-16-studio-worker-subagents-plan.md), [project settings design](project-factory-settings-ui-design.md), and [Flutter programme](../studio-flutter/programme.md) remain authoritative for their own scope. Keep merge/completion decisions and project-level inheritance intact. [Original Svelte screenshots](studio-visual-foundation-design.md) are historical evidence only, not evidence of Flutter's current appearance or defects. Any missing backend capability discovered by these plans must be explicitly scoped and tested; do not substitute mock data or claim an unsupported capability exists.
