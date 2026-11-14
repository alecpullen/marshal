# Marshal Studio: fleet manager and software factory planning

This is the full product planning archive developed with Alec on 10–11 November 2026, published on 12 November 2026. It covers the larger evolution of Marshal Studio beyond the initial bootstrap factory.

Start with the [design and decision record](design-notes.md), which distinguishes approved directions, proposals, deferred work and unresolved questions. The [journey map and gap inventory](journey-map.md) connects the flows. [Environment feasibility research](environment-feasibility.md) records the portable runtime direction and its unverified integration points.

## Planning documents

- [Product design and decisions](design-notes.md): optional project factories within the fleet manager, conversational planning and handoff, forge/CI/review, mobile/PWA, notifications, environments, hosts and recovery.
- [Journey map](journey-map.md) and [high-resolution visual](screenshots/journey-map.png).
- [Environment feasibility](environment-feasibility.md): Dev Containers/Compose/native execution, OrbStack ARM64 and Podman considerations.
- [Workers and external planners](worker-and-planner-integration.md): mobile-friendly Studio planning, reusable worker roles, complementary ACP and MCP paths, project context and opt-in planner delegation; a future direction to validate and plan.
- [Combined project and factory settings](project-factory-settings-ui-design.md): approved UI and live inheritance behavior for project agent defaults and factory overrides, with an approved [implementation plan](plans/2026-11-15-project-factory-settings-plan.md).
- [Initial bootstrap scope](bootstrap-factory-scope.md) and [source compatibility assessment](bootstrap-compatibility.md).
- [Original bootstrap design draft](bootstrap-factory-design.md) and [original implementation plan](bootstrap-factory-plan.md).
- [Earlier exploration overview](early-exploration-readme.md), retained as historical context; the decision record takes precedence over its older open-question lists.

## Active specification

- [Existing-project connection and read-only discovery](existing-project-discovery-spec.md): the design output for gap 1 of the journey map, and the first slice taken from this archive. It states the approved behaviour as requirements, records the served `POST /api/repos/{id}/discover` contract in §5 with the ten capability keys, five states, status boundary and read-only guarantees, and lists the questions still open. The specification's implementation record lists the commits and acceptance gates.
- [Existing-project read-only discovery implementation plan](plans/2026-11-12-existing-project-discovery-plan.md): the reconciled, test-first execution plan for the first slice. It freezes the HTTP/report contract, capability taxonomy, read-only guarantees, task boundaries, verification gates, and draft-PR review policy.
- [Project factory defaults and per-handoff overrides](project-factory-defaults-spec.md): the design output for gap 2 of the journey map. It turns the approved "configuration can supply defaults and individual handoffs can override them" decision into a concrete contract: which fields are inheritable, how an override is expressed, when the resolved configuration is snapshotted, and why adopting independent work into a factory is deferred to the daemon track.
- [Narrated engineering report in the pull request description](factory-narration-spec.md): the design output for the remaining approved §6 forge-experience behaviour. It turns "Preferred narration is an engineer's technical reasoning, not merely a concise summary" into a concrete contract: the narration sections, what the implementer must produce, how the description is composed and published, and which §6 proposals stay out of scope.
- [Preview and test-environment capability discovery](preview-environment-discovery-spec.md): the design output for the provider-capability discovery the narration slice left open. It records what GitHub, Gitea/Forgejo and GitLab actually expose — read from the providers' own published API descriptions — and turns it into a read-only, per-instance and per-credential report of a repository's deployment environments, deployments and their provider-reported status and revision. It is served as `GET /api/repos/{id}/preview-environments`: three ordered capability keys, the discovery contract's five states and its contextual classifier, and the provider's own values verbatim. The pull-request presentation of those links stays out of scope: that is the proposal, not the agreed part.

## Visual explorations

The decision record links all approved and exploratory screens to their standalone HTML and high-resolution screenshots. These cover voice planning, narrated PR inspection, fleet/factory dashboards, work items, handoff, execution, human decisions, ready review, stalled-work recovery, agent/environment setup, hosts and disconnection, new/existing project onboarding, and the extensible workflow editor.

View PNGs directly on GitHub. HTML files are interactive throwaway studies using illustrative data; opening them locally or serving this directory can exercise their simulated controls. They do not connect to live agents, providers, repositories or services. Some use browser localStorage for local drafts. The factory dashboard screenshot still contains the purple workflow strip rejected in discussion; the decision record explicitly supersedes that detail.

## Relationship to the bootstrap implementation

[PR #57](https://github.com/alecpullen/marshal/pull/57) implements the narrowly scoped first factory loop. Its docs/bootstrap-factory documents describe the implementation and its current acceptance limits. Bootstrap documents preserved here are the original planning drafts; their draft-status wording records their historical state and is not a claim that implementation has not since occurred.

The broad design is a product direction, not a promise that every described feature is implemented or an instruction to build the entire vision before using the bootstrap loop. No new mockups or product decisions were introduced when preparing this archive. Source snapshots, temporary tooling, execution ledgers, credentials and implementation review artifacts are excluded.
