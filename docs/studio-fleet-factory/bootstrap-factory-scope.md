# Bootstrap factory — proposed initial scope

Date: 2026-11-11
Status: proposal for discussion, not an implementation plan or authorisation to alter independent daemon work.

## Intent

Build the smallest useful factory loop, then use it to implement small expansions of Marshal under human supervision. The full product design is a direction, not the first release checklist.

## Approaches

- Build the full fleet, environment and workflow platform first: broad foundations, but delays testing the actual factory loop.
- Build a standalone automation script: quick experiment, but less direct validation of the intended web/daemon interaction and durable work-item model.
- Recommended: a narrow end-to-end factory through the existing daemon, with a small responsive web surface and one working forge adapter. Reuse supported execution and environment capabilities.

## Proposed first usable loop

1. Supply an approved specification and plan from the existing planning harness. Manual import/paste is sufficient initially; embedded planning chat and MCP are follow-ups.
2. Create a durable work item and select implementer/reviewer models supported by the daemon, an available environment, and the attempt allowance.
3. Start implementation in an isolated workspace through one connected daemon. Create a draft PR early, using plan/spec documents for the first commit if appropriate.
4. Observe existing CI for the current revision and run a separate reviewer session. Keep passing checks compact and failures prominent.
5. Return actionable findings to the implementer and repeat within the configured limit, default five. Escalate blockers, consequential decisions, repeated lack of progress or exhaustion.
6. Mark ready only when required checks and reviews are clear for the final revision. Human merge remains the default.

Proposed initial constraint: one project/repository, one execution host, serial agent execution and one PR per work item in the first supported flow. These are delivery limits, not permanent one-to-one schema constraints or removal of the agreed multi-host direction. GitHub is the proposed first adapter because Marshal is already hosted there; keep provider operations behind a small interface for later Forgejo/Gitea/GitLab support. Do not fork or embed a forge initially.

## Minimum web surface

A responsive work-item list, work-item activity/status, CI and review evidence, and actions to answer blockers, stop, or explicitly retry with an updated allowance/configuration. Existing approved visual direction guides this small subset. Full narration editing, voice, notification integrations and workflow authoring are not prerequisites.

## Essential behaviour to settle before implementation

- Current daemon contract for session creation, event observation, messaging, completion, cancellation and recovery; identify unsupported needs without modifying independently developed daemon work.
- Persistence and ownership of factory state; a browser closing must not end orchestration.
- Repair-iteration counting, reviewer result format, evidence tied to code revisions, and conservative stop rules for unproductive retries.
- Restart/reconnect reconciliation: preserve history and never blindly duplicate an implementation or PR after uncertain dispatch. Explicit recovery is acceptable when safe automatic continuation cannot be established.
- Existing repository architecture, test commands, available CI, and supported environment path. Reuse what exists before choosing new infrastructure.

## Deferred until the loop is useful

General workflow editor and nested loops; automatic decomposition; multiple hosts and adaptive placement; comprehensive container lifecycle UI; all forge adapters; embedded/external planner integrations; live adoption; automatic spec-edit assessment; voice; push/chat bots; multiuser access. Retain compatibility boundaries where needed without building these features upfront.

## Acceptance and first self-development task

Run a small real Marshal change from approved plan through draft PR, implementation, an actionable review finding or CI failure, repair, fresh checks/review, and ready-for-human-review. Also verify a capped/blocked outcome and restart reconciliation without duplicate execution. Controlled fixtures can exercise deterministic failure paths; real execution must demonstrate end-to-end integration.

Use the running stable version to build changes on a separate branch/workspace. Review and deliberately deploy an update between runs; do not replace the orchestrator underneath its own active run. The first factory-built improvement should be small and observable, such as a better CI failure summary or retry configuration, before extending workflow orchestration itself.

## Next deliverable

Read-only inspection of the current repository and daemon interfaces, followed by a short bootstrap design and a concrete implementation plan. Do not require resolution of every open product question before this build.

## Compatibility assessment

The [read-only compatibility report](bootstrap-compatibility.md) now grounds this proposal in main at `bff50180cf1e282430d20eeccb0d508850ba88d7`. It recommends composing existing bridge agent/recipe/forge components, adding durable factory control and complete CI evidence. Existing SDD is substantial but does not directly implement the proposed factory semantics. The next deliverable is the short bootstrap design and then its implementation plan; the broad product backlog remains deferred.
