# Marshal web fleet manager and software factory — design record

Updated: 2026-11-11 (Australia/Melbourne)

Status: ongoing brainstorming and design exploration. This records the conversation so far, including accepted directions, proposals, open questions, and mockups. It is not an implementation-ready specification or authorisation to change the daemon.

The user is developing the daemon/ACP split independently. This exploration concerns the web experience built on that separation. Files here are in a separate local workspace directory; no changes have been pushed to the Marshal repository.

## 1. Intent and existing work

Build a configurable web fleet manager with optional project software factories. The existing web bridge is an early prototype, not a constraint on the final product experience.

Mobile and small displays are first-class working environments. Larger displays, keyboard, mouse, and non-touch use should also be used effectively.

Relevant existing repository material:

- [Daemon design](https://github.com/alecpullen/marshal/blob/main/docs/daemon/design.md)
- [Web Studio design](https://github.com/alecpullen/marshal/blob/main/docs/web-studio/design.md)
- [Architecture](https://github.com/alecpullen/marshal/blob/main/docs/ARCHITECTURE.md)
- [Repository instructions](https://github.com/alecpullen/marshal/blob/main/AGENTS.md)

Existing architectural guidance places the web bridge outside the agent internals, communicating through JSON contracts. The new design should respect the ACP separation rather than duplicate agent behaviour in the UI. Exact contracts and ownership changes require later design against the daemon's then-current implementation. The existing Web Studio design is prior art; its earlier page layouts and product decisions are not automatically adopted here. A referenced `docs/fleet-cli/design.md` was not available on GitHub when checked.

## 2. Product structure — agreed direction

The fleet manager is the main UI. A factory is something users create and access within it, not a mutually exclusive installation mode.

- Users may run independent agents, use project factories, or do both.
- Each factory belongs to one project. Creating a factory should be lightweight.
- Users can create one for each project; cross-project factory operations are outside the initial scope.
- Factory configuration starts with a working plan → implement → review → fix process, then allows customisation.
- Factory workflows should be substantially extensible and customisable, rather than limited to changing settings in a fixed sequence. The user explicitly envisages stress-testing loops, automatic task decomposition, more automated planning loops, and specialist reviewers such as security reviewers at selected stages.
- This establishes the required breadth of workflow configuration. The authoring interface, stage types, branching/loop semantics, extension mechanism and relationship to future agent roles remain to be designed. Workflow customisation must not silently make the later coordinator role a prerequisite.
- Fleet-level views organise factories and independent agents. Factory views organise work by work item, with underlying agents still accessible.

Working hierarchy:

1. Fleet manager: project factories, independent agent sessions, shared attention.
2. Project factory: configuration, work items, execution and decisions.
3. Work item: intent, planning, specifications, plans, delivery and evidence.
4. Linked agent runs and PRs: implementation and review activity associated with the work.

The hierarchy describes navigation and relationships, not a settled database schema. “Feature” is a promising user-facing concept; “work item” remains the general working term. Treatment of bugs, investigations, migrations, and maintenance is still to be designed.

### Starting independent agents and choosing environments

The user wants to choose an agent's model, provider, working environment and connected repository when starting an agent outside a factory. Easy, useful integration with custom-hosted forges such as Gitea and Forgejo is a core requirement, alongside hosted services such as GitHub and GitLab. These capabilities should be available through the main fleet experience rather than requiring a factory.

The user suggested that a container system or similar execution layer will probably be needed. Containers are a candidate to investigate, not a settled requirement that every agent or environment must use them.

Each agent should get an isolated workspace by default, with an explicit option to resume an existing workspace. The user confirmed this direction. The isolation mechanism is not yet selected; separate working state does not itself require a separate full container stack for each agent. Concurrent reuse still needs design.

Workspaces remain available until explicitly deleted by default, with optional automatic cleanup. The user confirmed this retention policy. Proposed resource behaviour: preserve the checkout and durable agent context while stopping idle compute and services, so resumable work does not need to keep consuming RAM. The definition of idle, service lifecycle and exact resumption mechanism remain open. Automatic cleanup rules, including treatment of uncommitted or unpushed work, need separate design.

Environments should support companion services such as databases and queues alongside development tools. The user confirmed this capability. Supporting services are isolated per workspace by default, with an explicit option to use shared services. Persistent service data, startup/readiness dependencies and lifecycle ownership remain to be designed. In particular, stopping one workspace must account for any services still used by other workspaces; the exact mechanism remains open.

Proposals to refine: save reusable forge connections and environment definitions; distinguish a daemon host (where work runs), an environment (toolchain, dependencies and services), and a workspace (repository checkout, branch and working state). Consider existing repository environment definitions such as devcontainers before designing a proprietary equivalent. Evaluate container-backed environments alongside host-native execution where platform tools require it. Workspace isolation, persistence, reuse, service lifecycle and resource accounting need design against the independent daemon work.

Environment changes made through the visual editor should be saveable back into the repository for version control. The user confirmed this direction. Saving those changes should follow the usual branch-and-PR (or merge-request) flow by default. The definition format, preservation of existing configuration and when revisions apply to running workspaces remain to be designed. Proposed runtime behaviour: tie workspaces to an environment revision and apply updates explicitly rather than changing tools underneath a running agent. This establishes a desired product capability, not authorisation to write to any repository during brainstorming.

Users may optionally try an edited environment in a temporary workspace before submitting its PR/MR. This is user-initiated and is not a required gate for saving or proposing the configuration change. Temporary-workspace lifecycle, capacity admission and cleanup details remain to be designed.

## 3. Planning and the work item

### Agreed

The user plans conversationally with a capable model, often a frontier model, before it produces specifications and executable plans. The factory is principally the handoff, implementation, review, and repair cycle; the product should not assume a short prompt followed by hands-off delivery.

- A durable feature/work item connects the conversation, documents, decisions, execution, and PRs.
- Planning can begin without an attached repository. The user explicitly approved this and emphasised that initialising a new repository for new work must be a supported workflow.
- For new projects, the planning agent can propose the initial technology stack, project scaffold and development environment as part of the plan. The user approved this planning responsibility. Proposal generation does not itself authorise repository creation, dependency installation or execution; setup timing and how these choices are reviewed remain to be designed.
- Running the initial scaffold through the normal factory review/fix loop is optional and defaults to off. The user explicitly rejected making that loop the default for scaffolding. Users may opt in. This choice does not disable any checks already configured in the repository.
- In the default setup path, create the scaffold as the new repository’s initial baseline commit directly on its default branch. The user approved this placement; subsequent work can branch from that baseline. The opt-in reviewed-scaffold path still needs its initial branch/PR sequence designed. This decision concerns initialising new repositories, not bypassing review for changes to existing repositories.
- The planning-to-execution flow must accommodate connecting an existing repository or creating a new one on the chosen forge, without losing the existing conversation and planning context. Repository creation timing, ownership/visibility choices, scaffold contents and the opt-in reviewed-scaffold branch/PR sequence remain to be designed.
- There is no fixed one-to-one relationship between specifications, plans, and PRs. A concise specification may require substantial code and multiple PRs.
- The planner may propose a PR breakdown. Implementers may adjust it as needed within delegated authority.
- Users should have the option to hand off portions while continuing to plan other portions.
- A work item may eventually span repositories, but this is a low priority because the user does not currently have a workflow to test it. Initial factory work is project-scoped.

### Proposed details to refine

- Record the exact document revisions attached to each handoff, so ongoing planning does not silently change active implementation's agreement.
- Keep work-item completion distinct from individual PR readiness; assess the combined outcome against acceptance criteria.
- Preserve links and history when PRs split or combine; represent dependencies where necessary.
- Allow an independent agent's planning conversation to become a factory work item.
- Keep the reasoning behind decisions, relevant alternatives, unresolved questions, and acceptance criteria with the work, not only session transcripts.

### Adopting independent work into a factory — in exploration

On 2026-11-11, the user said adopting an independent agent while it is still running would be useful if feasible. Treat this as a desired capability, not verified daemon support. Do not require completion before adoption as a product constraint.

Proposed interaction, not yet approved: “Continue in factory” attaches the existing session and its work to a work item, retaining its environment, branch, conversation and history. The planner proposes the workflow entry point and remaining stages for human confirmation. The current implementation continues; factory scheduling must recognise it as already in progress rather than start a second implementer. New instructions or authority changes need an explicit, acknowledged transition. Missing planning material should be visible and can be assembled from existing context without pretending earlier stages ran. Exact daemon contracts, concurrent-edit handling and the point at which factory control takes effect remain unresolved.

The user subsequently approved having the planner automatically fill in missing specifications from the existing conversation and available work context, surfacing ambiguities that require human input. This is context reconstruction, not authorisation to invent requirements or treat inferred decisions as previously approved. Adoption while running remains a desired capability subject to daemon feasibility; the precise transition mechanics above remain proposals.

The user also approved the planner deciding whether to pause affected work within the factory's configured delegated authority when reconstructed context exposes an unresolved question. Surface the question immediately; allow unrelated work to continue. Pause the affected work when proceeding risks substantial rework or exceeds the agent's authority. Escalate decisions beyond the planner's authority to the human. This establishes intended behaviour; acknowledged pause delivery and handling an agent already inside a tool operation still need daemon-level design.

Related creation proposals remain under discussion: create within a project from the standard workflow and project defaults; reusable templates can carry workflow structure to other projects with project-specific bindings selected separately.

### Plan changes during implementation — in exploration

On 2026-11-11, the user said whether a new requirement amends the current work item or becomes a linked follow-up depends on whether it can reasonably be incorporated into the current work. The planner may make that choice, while keeping amendment of the current work item available to the user. There is no blanket rule that scope additions become separate work items. This routing decision does not expand the planner's existing delegated authority to approve consequential scope changes.

Proposed details for further discussion: assess affected implementation, dependencies and likely rework; explain the chosen route and allow the user to change it. Preserve the plan revisions used for completed work. Distinguish document edits from authorisation to change active execution, and show acknowledgement when approved changes reach affected agents. These mechanics have not yet been individually agreed.

The user requested continuing behavioural planning without further mockups for now, having been satisfied with the visual explorations so far.

The user approved automatically triggering planner assessment when a specification edit is saved as a user-selectable option. The default and configuration scope are not yet selected. Automatic assessment does not itself authorise consequential implementation changes.

### Bootstrap priority — user direction

The user wants to build as little as possible before obtaining a working factory loop, then use that factory to expand Marshal and test it through real development. Broad product brainstorming should give way to a narrowly scoped initial build. The wider design remains a destination, not a prerequisite checklist. A proposed initial scope and acceptance criteria are recorded in [Bootstrap factory scope](bootstrap-factory-scope.md); its implementation choices have not yet been approved or checked against current daemon contracts.


## 4. Handoff and delegated authority

### Agreed

The handoff is the primary place to select configuration. Optional per-project defaults should reduce repeated setup.

Approving a handoff automatically authorises execution when eligible capacity is available. There is no separate manual release step. When capacity is unavailable, approved work waits and starts when eligible capacity becomes available, respecting any pinning constraints.

Handoff choices include:

- Implementer model and/or provider, where alternatives exist.
- Reviewer model and/or provider.
- Specialist review focuses, such as security or UX.
- Whether the planning agent can answer implementers' questions.
- What changes it can approve without escalating to a human.
- Automated repair iteration limit: five by default.
- Whether responsibility may automatically transfer from an external planner to an internal planner; human approval is required by default.

When authorised, the planning agent may approve small, relatively inconsequential changes. Larger changes must be surfaced to the human. The exact boundaries are user-configurable at handoff, with project defaults available.

When a human decision blocks part of a work item, independent work should ideally continue while that decision waits. The user confirmed this preference; an escalation should not automatically pause the entire work item or factory.

Decision presentation should offer clear options as buttons or similarly easy-to-action choices when those options are available. Entering a conversation to discuss the decision is an optional path the user can choose, not a required step before acting.

Once the decision is resolved, affected work should resume automatically when eligible capacity is available, following the selected outcome. No separate resume approval is required. This applies to work authorised to continue by the decision; an outcome that stops or removes work should not restart it.

The user also wants notifications outside the app for decisions needing human attention, so they do not have to keep checking Marshal.

Notification and mobile delivery preferences:

- Browser push is a priority. The mobile web app should be a full-featured, installable PWA with access to supported platform capabilities, including push, and the main Marshal workflows available on mobile.
- Signal and Element (Matrix) chat bots are desired integrations.
- Email is not a priority; the user is not interested in it for their own workflow.
- ntfy may be useful, but is lower priority.

Platform support and permission/install requirements for PWA features need investigation; this preference is not a claim of identical browser capabilities on every device. Signal and Matrix bot integration mechanisms also need investigation. Notification preferences remain open.

The user considers answering questions and steering agents through chat bots a possible later enhancement, subject to feasibility and implementation effort. It is not a requirement for initial notifications. Proposed initial bot scope: deliver notifications with links back to the relevant context in Marshal. Interactive bot replies, identity/authority mapping, and decision handling should be investigated separately before committing to that extension.

### Role boundary

A more capable coordinator that directly manages cheaper subagents belongs to the user's later underlying-agent roles work. Do not introduce that coordinator as a prerequisite for the current factory design. For now the planning agent collaborates with the human and, if allowed, answers implementation questions and approves changes within its authority.

Specialist reviewers do not require the future roles system. Review instructions generated by the planner—or with input from implementers—can provide the initial mechanism.

### Proposed details to refine

- Planner-authored review briefs express the agreed assessment; implementer notes add areas for scrutiny rather than narrow the brief.
- Assess a deviation by its effect, not number of changed lines. Behaviour, acceptance criteria, architecture, costs, security, and data handling are candidate escalation dimensions, not a final policy table.
- Record approvals and answers with rationale and authority so implementers and reviewers share the same understanding.
- Save effective handoff settings with each execution. Later project-default changes should not silently alter running work.
- Consider cumulative impact of several small deviations.
- Pause the affected work and anything dependent on the unresolved decision, while continuing work that can proceed within existing approval. How independence is established remains to be designed. Show what is waiting and what is still progressing.
- Present a human decision with the issue, the agent's recommendation, and affected work. These contextual details remain proposed; actionable choices and optional conversation are agreed. Proposed button labels should describe the resulting action, with consequential effects visible before the user chooses.
- Copying a factory's defaults could simplify creation without coupling factories.

## 5. Factory review and repair loop

### Agreed

Review findings should automatically feed into repair inside the factory, within delegated authority. Default limit: five iterations, configurable by the user.

Outcomes:

- **Ready:** required checks pass and configured reviewers have no further actionable findings. Mark the PR ready.
- **Stalled:** unsuccessful attempts make insufficient progress or repeatedly reproduce problems. Stop early and surface the situation for a human decision.
- **Limit reached:** unresolved issues remain at the iteration cap. Stop and surface the remaining work.
- **Authority exceeded:** surface consequential decisions rather than making them without permission.

For recovery from a stalled run, the useful adjustments depend on the nature of the stall. The user wants to be able to change the models and/or providers used by implementation and review agents before retrying. Recovery should not be limited to rerunning the same configuration. Other recovery controls and how configuration changes apply to existing agent sessions remain to be designed.

When switching models during recovery, users should be able to choose between continuing the existing conversation and starting fresh with a handoff summary. The user confirmed both options would be useful. How conversation context is carried across providers/harnesses, including any limitations, remains to be investigated; this does not assume native session portability between providers. Proposed fresh-start context includes the approved scope, current code revision, prior attempts and findings, and unresolved blockers.

When retrying a stalled or capped run, the user chooses a new iteration allowance. All previous attempts remain preserved in the history. The new allowance authorises further attempts without erasing the earlier run's outcome or silently resetting its recorded count. The suggested allowance and detailed counting semantics remain to be designed.

Merging remains a human decision by default. Automatic merging may be explicitly enabled through configuration. Marking a PR ready does not itself authorise merging under the default policy. The configuration scope and detailed automatic-merge conditions remain to be designed.

Create a draft PR at the start of implementation. Specifications or plans can be the initial commit, allowing the draft to exist before implementation code.

### Proposed details to refine

- Define an iteration as an implementation/repair attempt followed by CI and configured reviews. The exact counting rule is still open.
- Correlate CI failures and review findings into repair briefs when they describe the same problem.
- Present what was tried, what changed, what still fails, and where agents disagree when a run stops.
- Review failure or inability to assess is not equivalent to a clean review.
- Tie CI and review evidence to revisions, so a new change cannot inherit a stale claim of readiness.
- Exact stagnation detection, flaky-check handling, retry semantics, and reviewer disagreements need design.
- Refine the scope and conditions of optional automatic merging, including how it respects required CI, reviews, and forge protections.

## 6. Forge experience and narrated engineering reports

### Agreed priorities

The user's main frustrations with traditional PR experiences are CI visibility and text-heavy descriptions/code reading. CI at the centre and narrated explanations solve most of those issues.

- Show failing CI prominently, with useful detail and access to logs.
- Compact or collapse successful checks while preserving visible success.
- Connect findings and failures directly to factory repair.
- Lead review with the issue and proposed solutions. Reading code is optional.
- Implementation reports and PR descriptions should include code with technical explanations of why and what is happening.
- Preferred narration is an engineer's technical reasoning, not merely a concise summary of the finished solution.

Narration should explain relevant existing behaviour, constraints, alternatives and tradeoffs, implementation choices, verification, and remaining limitations. Selected code excerpts support that account; avoid narrating every changed line.

Where a repository and its Git provider already have branch testing, preview deployments, or test environments configured, Marshal should surface those capabilities neatly in the PR experience. The user explicitly does not want Marshal to build its own separate preview/testing infrastructure. Availability depends on the existing repository/provider setup; this is integration and presentation of that setup, not a prerequisite Marshal must supply.

### Proposals to refine

- Store narration as structured content while being able to generate Markdown descriptions for external forges.
- Tie explanations and excerpts to exact revisions; update affected passages after repairs.
- Ask reviewers to check whether the code and evidence support the report's claims.
- Link the account back to recorded planning decisions and implementation clarifications.
- Show existing preview/test environment links with their provider-reported status and associated revision when available, so users can tell which change they are trying. Exact discovery and supported provider capabilities remain to be investigated.

### External forge integration — API-first direction approved

Documentation reviewed on 2026-11-11 in response to the user's question about limiting support to forges with CLI tools. All four target providers document APIs: [Gitea](https://docs.gitea.com/development/api-usage/), [Forgejo](https://forgejo.org/docs/latest/user/api/usage/), [GitHub](https://docs.github.com/en/rest/about-the-rest-api/about-the-rest-api), and [GitLab](https://docs.gitlab.com/api/rest/). Gitea and Forgejo document instance-local Swagger/OpenAPI descriptions, useful for checking the installed version's surface. API existence does not establish endpoint parity, enabled CI, sufficient token permissions or a tested Marshal integration.

The user approved the integration direction on 2026-11-11: API-based adapters for Gitea, Forgejo, GitHub and GitLab, with optional CLI tools and a basic Git-only fallback. Use standard Git for repository data and working state; provider-specific API adapters for repository creation/discovery, PRs/MRs, reviews, permissions, CI statuses/jobs/logs and existing preview links where supported; and webhooks plus polling for freshness. Forge CLIs may remain optional tools for agents or a narrowly justified adapter fallback, but should not determine which forges Marshal supports or be required in every workspace. APIs provide structured results and documented status/error handling; CLI integrations add binary/version/authentication differences and may expose only part of the API. Structured CLI output can be usable, but does not remove those dependencies.

Adapters should expose capabilities for each instance and credential set, rather than assume a provider name guarantees a complete feature set. Distinguish unsupported, unconfigured, insufficient permission and temporarily unavailable states. Preserve native provider IDs, URLs, states and commit revisions. Share implementation between Gitea and Forgejo where verified while allowing independent divergence. CI metadata and provider check links may be available even when detailed third-party CI logs require a separate integration. Existing preview/test infrastructure remains external; this proposal does not add a new preview service.

Private-network implication: hosted forge webhooks need network reachability to their callback destination; a Tailscale-only Marshal endpoint cannot directly receive requests from an unconnected hosted service. Recommend outbound, authenticated, efficient polling as a supported private-deployment mode, with webhooks when reachable (for example, a self-hosted forge with permitted private connectivity). Respect provider rate limits, conditional requests and poll intervals; refresh active work more promptly within those constraints and show freshness. Polling has latency/request-budget tradeoffs. No public callback, relay or router port opening should be required. Sources: [GitHub webhook delivery](https://docs.github.com/en/webhooks/about-webhooks), [GitHub API polling and rate-limit guidance](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api). The exact polling policy and private webhook configuration remain open.

Approved compatibility scope: integrated adapters for Gitea, Forgejo, GitHub and GitLab; optional generic Git remotes for repository inspection and agent work, clearly without PR/CI integration until an adapter exists. A failed supported-forge API connection should remain a visible error, not silently become generic Git. The user approved this Git-only fallback alongside the four integrated adapters. Detailed capability reporting and failure presentation remain to be designed.

Suggested connection flow: repository or instance URL, detected/specified forge type, authentication, then a capability summary before repository discovery. Credentials belong in the backend/daemon integration boundary, with execution-host access scoped as needed; the exact service location and credential distribution must be reconciled with daemon/ACP design later. Existing forge state remains authoritative for PRs/checks while Marshal owns planning and factory context. No live forge, token, endpoint behaviour or CLI was tested in this documentation pass.

### Forge backend remains open

External forge support must include custom-hosted Gitea/Forgejo instances as well as GitHub and GitLab. An optional embedded forge must not become a prerequisite for connecting existing repositories. Proposed connection UX: save an instance URL and authentication once, then browse its accessible repositories and expose supported PR/MR, CI and preview capabilities. Specific API compatibility, authentication and capability differences require investigation.

Investigate Gitea as an underlying service and potentially forking it to internalise or simplify the process. No backend has been selected. A custom Marshal review experience over an existing forge is the assistant's initial recommendation, not an approved architecture. Forking should be evaluated against actual API or behaviour limitations and maintenance needs.

## 7. External planners and MCP

The 14 November follow-up on Studio-hosted Claude sessions, reusable workers,
shared project context and opt-in planner delegation is recorded in
[Workers and external planners](worker-and-planner-integration.md). It extends
this section's MCP handoff direction with an ACP-hosted path; neither path has
been validated end to end.

### Agreed

Some planning models are available only through their provider's harness. Marshal needs a handoff path from those environments, with MCP the intended integration avenue.

- Export enough context for a model inside Marshal to take over planning responsibility.
- Investigate two-way external communication before excluding it.
- Claude Code is the first target: terminal use first, web use second.
- Users choose whether fallback can be automatic; transfer requires human approval by default.

### Proposed interface and continuity

An external planner could create/update a work item, attach specifications/plans/decisions, propose PRs, prepare handoff settings, retrieve progress and questions, and submit answers. Exact MCP tools and schema are undecided.

Preserve durable questions and decision history in Marshal. Track the responsible planner and whether it is reachable. If responsibility transfers, preserve its authority boundaries and context. Queuing questions and continuing unaffected work while an external planner is unavailable are proposed behaviours.

### Claude Code research — checked 2026-11-09, documentation only

- [Channels](https://code.claude.com/docs/en/channels): documented two-way event delivery into a running Claude Code session, with replies through tools. The session must remain running to receive events. Channels are in research preview and have authentication/provider and organisation restrictions.
- [Channels reference](https://code.claude.com/docs/en/channels-reference): a local stdio MCP adapter bridges external systems to Claude Code. Custom channels need the applicable allowlisting or interactive development opt-in. The development-channel flag is ignored in non-interactive `-p` and Agent SDK sessions.
- [Programmatic Claude Code](https://code.claude.com/docs/en/headless): `claude -p --resume <session-id>` can continue a particular session. Session ownership, safe concurrency, authentication, and suitability for this integration still need validation.
- [MCP compatibility](https://code.claude.com/docs/en/mcp#push-messages-with-channels): current docs describe a protocol-negotiation limitation for Channels on the newer MCP revision. Validate versions and handshake behaviour when prototyping.

These are documented possibilities, not tested Marshal integrations. Hosted Claude Code web-session capabilities still need research. Generic MCP connectivity alone does not guarantee the ability to initiate a model turn or wake an external session.

## 8. Mobile, desktop, and input

### Agreed

Phone use should support essentially all main forge actions, reading and reviewing work, checking CI, interacting with the factory, and conversing with a planning agent.

Pain points are CI navigation, long PR bodies, code reading, and sometimes composing agent messages. Voice dictation is especially valuable for substantial planning explanations.

Keep large displays and keyboard/mouse operation first class alongside touch.

### Proposed principles to validate

- Same main capabilities with progressive disclosure on small screens and concurrent context on larger displays.
- Preserve drafts, conversations, review position, and expanded context across navigation/device changes where practical.
- Handle browser suspension and network loss without losing longer drafts or making action outcomes ambiguous.
- Unified diffs on mobile and optional side-by-side diffs on wide displays.
- Readable selected excerpts, chapter navigation, searchable CI logs, and links to failure evidence.
- Discoverable touch actions and efficient keyboard navigation; exact bindings and desktop layouts are not designed yet.

## 9. Voice planning

### Agreed

Voice is mainly for thinking through substantial planning tasks. The agent may submit questions while the user is still talking, but must not interrupt or require an answer when the user does not notice or does not want to respond.

Approved visual direction: **B, questions alongside, as the default; C, quiet capture, as an optional quieter mode.** Both represent the same recording and question state.

### Proposed behaviour to refine

- Pause/resume longer recordings; silence does not automatically submit.
- Preserve the transcript and offer an editable structured interpretation before adding it to the conversation.
- Keep original intent, alternatives, and unresolved questions distinct from approved requirements.
- Questions should not speak over the user, stop recording, or steal focus.
- Allow defer/dismiss, recognise answers given naturally, and collect unresolved questions after recording.
- Limit question volume and avoid premature prompts while a thought is still developing.
- Ignoring a question is not agreement or approval.
- Audio retention, transcription model/provider, privacy settings, cost, latency, and mobile background/offline reliability remain open.

The mockups simulate recording, transcription, questions, and submission. No actual microphone or model integration exists. Approval of the layout does not establish technical feasibility of every proposed voice behaviour.

## 10. Approved visual directions

### Extensible workflow editor and planner proposals — approved

Created desktop and mobile explorations on 2026-11-11 after the user requested UI/UX suggestions for extensible factories. Three approaches were discussed: a node canvas (clear topology but sprawling and difficult on touch), a structured stage outline with expandable groups (readable and mobile-friendly), and conversational authoring (expressive but requiring a precise inspectable result). The assistant recommended combining the latter two, with a graph potentially available later as an optional overview. The user approved the desktop and mobile mockups on 2026-11-11. Approval covers the structured outline with expandable groups, focused mobile stage settings, and inspectable planner proposals applied to a draft before saving. The optional graph overview remains a later possibility.

Desktop pairs a workflow outline with settings for the selected stress-test/repair loop. Mobile shows the outline and opens those settings as a focused view with a return action. The example includes planning/decomposition, human handoff approval, an expandable implementation/repair group with parallel code/UX reviews, a stress-testing loop and readiness for human PR inspection. An Add menu illustrates agent, tool/CI, human checkpoint, condition, loop and parallel-group building blocks. Stage movement and duplication are proposed menu actions, not drag-only interactions.

The planner view turns a conversational request into two visible additions: a security design review before handoff approval, and a conditional security code reviewer within implementation reviews. The example matches authentication/session file paths; these are illustrative starting rules, not a complete security assessment or mandated product policy. Applying the proposal changes the draft, with Save workflow remaining separate. The footer proposes applying saved workflow changes to future runs while current runs retain their configuration; this workflow-version behaviour has not yet been separately agreed.

The stress-test example exposes command, environment, repair provider/model, instructions, success condition, attempt cap and escalation outcome. Its three-attempt cap is sample data, not a selected default. Five remains the agreed default factory implementation/review/fix allowance, but relationships between multiple/nested loops and run-wide budgets remain unresolved. The proposed ready condition requires required CI/reviews on the final changes; later repairs cannot simply inherit earlier clean evidence. Exact scheduling, review invalidation, input/output contracts, graph validation, task decomposition and plugin extension mechanics still need design. No future coordinator role is required by this UI proposal.

- [Desktop workflow and stage settings](screenshots/workflow-editor-desktop.png).
- [Desktop planner proposal](screenshots/workflow-planner-desktop.png).
- [Mobile workflow outline](screenshots/workflow-editor-mobile.png).
- [Mobile stage settings](screenshots/workflow-editor-mobile-stage.png).
- [Mobile planner proposal](screenshots/workflow-planner-mobile.png).
- [Add-stage building blocks](screenshots/workflow-add-stage.png).
- [Source HTML](workflow-editor.html).

These are throwaway mockups with illustrative workflows, commands, models, conditions and running-work counts. Local Chromium checks passed at 1488 px and 390 px: no horizontal overflow or page errors, group expansion, mobile settings navigation, provider/model changes, attempt-limit summary updates, planner-view navigation, applying the predefined two-stage proposal to the draft, and save/add-stage explanation dialogs. Real stage creation, movement, duplication, schema validation, planner calls, saving and workflow execution are not implemented. Desktop screenshots are 2880 px wide; mobile screenshots are 1170 px wide and show full scrolling pages. No workflow, agent, CI run or repository was changed.

### Existing-repository connection and discovery — approved

Created desktop and mobile mockups on 2026-11-11 for connecting an existing repository and inspecting the resulting project context. Entry supports pasting a URL or browsing sample saved connections. The example matches a private Forgejo instance, offers explicit Git-only integration, and creates a renameable Marshal project named from the repository. A separate connection preview offers Forgejo, Gitea, GitHub and GitLab and leads conceptually to provider authentication; credential collection and authentication are intentionally not simulated.

Discovery presents instructions and source paths, tooling detected from files, missing environment configuration, current-revision CI results, connection capabilities and existing PRs/branches. Facts and suggested next steps are visually distinct. The example separates visible CI status from inaccessible detailed logs, and file inspection from execution-host Git access. A missing recognised container definition can lead to choosing an existing environment, compatible native tools or a planner proposal. Planning and independent-agent setup are separate next actions; project connection starts neither automatically and does not require a factory.

- [Desktop repository connection](screenshots/repo-connect-desktop.png).
- [Desktop discovery results](screenshots/repo-discovery-desktop.png).
- [Mobile repository connection](screenshots/repo-connect-mobile.png).
- [Mobile discovery results](screenshots/repo-discovery-mobile.png).
- [Forge connection preview](screenshots/repo-forge-connection.png).
- [Git-only capability explanation](screenshots/repo-git-only.png).
- [Source HTML](connect-repo.html).

The user approved these desktop and mobile layouts on 2026-11-11. Approval covers repository connection, automatic project naming, discovery presentation separating detected facts, missing configuration and access limitations, and the paths into planning or independent-agent setup. It does not establish authentication methods or actual provider capability/permission behaviour. All instance URLs, repository files, credentials/access states, CI results, revision IDs and counts are illustrative. Discovery is a fixed sample, not an actual scan of the supplied URL; changing the sample repository name does not generate different repository facts. No credential is collected and no provider is contacted. The provider connection preview does not select an authentication method or promise identical APIs across instances. Polling is shown as a private-connectivity example, not a tested implementation.

Local Chromium checks passed at 1488 px and 390 px: no horizontal overflow or page errors, URL/browse switching, sample repository selection and project naming, connection-to-discovery navigation, planning/agent action previews, CI access disclosure, explicit Git-only presentation and the provider-connection dialog. Desktop captures are 2880 px wide; mobile captures are 1170 px wide and show full scrolling pages. No real project, repository, factory, agent or environment was created. Authentication, full access correction, duplicate-project handling, persistence, real discovery and entry into the actual planning/agent screens remain to be designed or implemented.

### New-project setup — approved

Created desktop and mobile mockups on 2026-11-11 for turning repository-free planning into a new repository and scaffold. The desktop pairs forge, owner, repository name, visibility, default branch, scaffold-agent provider/model and host placement with an editable planner proposal. Mobile separates these into Repository & setup and Planner's proposal tabs. The conversation, decisions and documents stay attached to the project.

The illustrative Fieldnotes proposal includes a technology stack, rationale, scaffold file outline and development environment. These are sample choices, not Marshal technology defaults. Forgejo, Gitea, GitHub and GitLab appear as sample connections. Private visibility, main, the model aliases and specific host choices are illustrative. Container/native selection previews an environment description; environment generation and compatibility validation are not implemented.

Scaffold factory review is visibly off by default. The default action proposes creating the repository and running a scaffold agent to establish the initial baseline directly on the default branch. Feature implementation can be handed off later, and a factory is optional. Opting into review changes the action to configure review before starting. The detailed initial branch/PR sequence for that optional path remains unresolved; the mockup does not settle it.

- [Desktop new-project setup](screenshots/new-project-desktop.png).
- [Mobile repository and setup](screenshots/new-project-mobile.png).
- [Mobile planner proposal](screenshots/new-project-mobile-proposal.png).
- [Source HTML](new-project.html).

The user approved these desktop and mobile layouts on 2026-11-11. Approval covers the grouping of repository and scaffold-agent settings beside the editable planner proposal, the mobile tabs, scaffold review off by default, and the default-branch baseline summary. Sample technology, visibility, branch names, models and forge connections remain illustrative; the opt-in reviewed-scaffold branch/PR sequence remains unresolved. Local Chromium checks passed at 1488 px and 390 px: review is initially off, mobile tabs switch, proposal fields are editable, repository summaries follow edits, model choices follow the provider, environment description changes, and default/opt-in actions open illustrative dialogs. No page errors or horizontal overflow were observed. Desktop capture is 2880 px wide; mobile captures are 1170 px wide and show full scrolling pages. No repository, commit, environment or agent was created. Persistence, scaffold generation, validation and real forge/provider integration are outside this throwaway prototype.

### Disconnected host and reconnection — approved

Desktop and mobile mockups show an unreachable host with last contact time, unknown current agent progress, stale resource readings and new assignments paused. A quiet notification state represents a drop within the user's configured grace period; the illustrative two-minute elapsed time does not select a default grace period. Running tasks remain associated with the host. The UI does not claim that loss of contact stopped the agents.

Desktop places the activity timeline beside an existing implementer conversation with a queued steering message. Mobile places the connection summary and actionable message before the timeline. The queued message is explicitly awaiting delivery and offers edit/cancel controls. A separate reconnection preview shows recovered offline activity with original event times and delayed-reporting labels, delivery and acknowledgement of the queued message, and continuing work. Acknowledgement is distinguished from completion of the requested work. The completed reviewer result does not itself mark the PR ready without required CI.

- [Disconnected desktop](screenshots/host-disconnected-desktop.png).
- [Reconnected desktop](screenshots/host-reconnected-desktop.png).
- [Disconnected mobile](screenshots/host-disconnected-mobile.png).
- [Reconnected mobile](screenshots/host-reconnected-mobile.png).
- [Source HTML](disconnection.html).

The user approved these desktop and mobile layouts on 2026-11-10. Approval covers the visual and interaction direction: connection status, last-known activity, queued steering with edit/cancel controls, delivery versus acknowledgement, and reconciled activity with original event times. These remain throwaway visual explorations, not an implemented delivery or recovery protocol. All hosts, timestamps, measurements, delivery receipts and agent activity are illustrative. State switching loads a predefined scenario and explicitly resets sample messages; this is not a functional durable queue or reconciliation implementation. The initial sample message supports editing/cancellation, and the composer can illustrate an additional queued message. Local Chromium checks covered both captured widths, horizontal overflow, preview-state switching, editing/cancelling the sample message, composing a queued message, and page errors. Desktop captures are 2880 px wide and mobile captures are 1170 px wide, showing full scrolling pages. No agent, host, provider or forge was contacted.

### Host onboarding and connected host — approved

Created desktop and mobile visual explorations of adding a daemon host and inspecting it after connection. Setup offers install-and-connect or an existing daemon, a host name and operating-system selection, and a copyable command preview. The command is explicitly illustrative and non-executable; installation, enrolment credentials, protocol, connection direction and final command syntax remain unresolved. The example fleet endpoint uses a reserved example domain and private connectivity. No router configuration is required by this proposed flow.

The connected view proposes availability for eligible work after successful setup checks, automatic capacity with optional resource ceilings, current agents, detected runtime capabilities, and environment compatibility. The example Linux Podman host distinguishes verified checks from unknown ARM emulation support and incompatible macOS environments. A Mac mini M2 / OrbStack / 16 GB appears as a fleet peer. Linux architecture, hardware, resource measurements and verification results are sample data, not observations of the user's machines. Capability verification procedures and resource accounting are still to be implemented and validated.

Mobile places availability, capacity and running agents before runtime and compatibility details. The approved Pause new work control stops new admissions while letting existing agents continue. Custom ceilings are illustrative controls, not selected defaults or a resource-enforcement design. Preview navigation is only for exploring mockup states.

- [Desktop Add host](screenshots/host-add-desktop.png).
- [Mobile Add host](screenshots/host-add-mobile.png).
- [Desktop connected host](screenshots/host-connected-desktop.png).
- [Mobile connected host](screenshots/host-connected-mobile.png).
- [Source HTML](hosts.html).

Local Chromium checks passed at 1488 px and 390 px: no horizontal page overflow, setup-path switching, platform selection, connected-view navigation, custom-limit visibility, and pause/resume state changes. Desktop screenshots are 2880 px wide; mobile screenshots are 1170 px wide and show full scrolling pages. Clipboard success was not tested; the mockup offers text selection if clipboard access fails. No host connection, installation, service, agent or runtime check was performed. The user approved these desktop and mobile layouts on 2026-11-10. Approval covers the visual and interaction direction, including setup paths, capability and compatibility presentation, automatic capacity with overrides, and pausing new work. It does not establish the installation/enrolment protocol, resource-accounting implementation or actual runtime compatibility.

### Environment editor — approved

Desktop presents environment tools and supporting services beside the resulting configuration diff. Mobile separates editing and reviewing into two tabs. The illustrative edit updates Node.js and Redis in a repository-defined devcontainer/Compose setup. Existing workspace isolation and retention appear as context, not as changes in this example. The runtime format is illustrative rather than a selected implementation standard.

An optional trial action opens temporary-workspace setup with host placement and separate test services. The trial is explicitly shown as not run, and Create PR remains available. The proposal summary shows the repository, new branch, target and title. The approved presentation includes trial status in the PR description and the optional trial setup. Detailed startup checks, terminal access and temporary-workspace cleanup still require design.

- [Desktop environment editor and diff](screenshots/environment-editor-desktop.png).
- [Mobile environment editor](screenshots/environment-editor-mobile.png).
- [Mobile review and Create PR](screenshots/environment-editor-mobile-review.png).
- [Optional trial workspace setup](screenshots/environment-editor-trial.png).
- [Source HTML](environment-editor.html).

The user approved these desktop and mobile environment-editor layouts on 2026-11-10. Approval covers the visual and interaction direction, not the overall implementation specification or a runtime-format selection. Tools, versions, files, changes, repository and capacity are illustrative. Local Chromium checks covered page overflow at the captured widths, a version selector updating its diff line, mobile review navigation, opening the Create PR preview without running a trial, and the separate trial setup/action. This is a visual exploration, not a general configuration editor: service creation, configuration round-tripping, full diff recalculation, validation, environment trials and repository writes are not implemented. No agent, workspace, service, branch or PR was created.

### Starting an independent agent — approved

Desktop groups forge/repository, model/provider and an optional opening message alongside workspace, environment and host placement. Mobile uses two connected steps: “Agent & repository” and “Workspace & environment”. The example presents a custom Forgejo instance and a picker with Gitea, GitHub and GitLab connections. Repository capabilities are illustrative, not a guarantee that every instance supports every feature.

Workspace controls default to a new isolated checkout and offer resuming retained work, including visible uncommitted changes. Retention defaults to keeping the workspace until deletion. Environment details summarise development tools and companion services, with expandable service-isolation settings. Automatic host selection and explicit pinning appear with eligibility/capacity context. The container/devcontainer example illustrates a candidate environment type; it does not settle the underlying runtime or exclude native environments.

- [Desktop Start agent](screenshots/start-agent-desktop.png).
- [Mobile agent and repository](screenshots/start-agent-mobile.png).
- [Mobile workspace and environment](screenshots/start-agent-mobile-environment.png).
- [Forge connection picker](screenshots/start-agent-forges.png).
- [Supporting services detail](screenshots/start-agent-services.png).
- [Resume existing workspace](screenshots/start-agent-resume.png).
- [Source HTML](start-agent.html).

The user approved these desktop and mobile Start agent layouts on 2026-11-10, including the supporting forge, service and workspace-resumption views. Approval covers the visual and interaction direction, not the overall implementation specification. Repositories, instance URLs, accounts, models, environments and capacity are sample data. Runtime formats, saved-configuration semantics and service-data resumption still require design. Local Chromium checks covered overflow at the captured widths, the mobile step change, forge selection, model choices following provider changes, service expansion, workspace resumption selection, host pinning and the mockup-only start dialog. No forge/provider is contacted, and no agent, workspace or service is created. Shared-service binding, configuration validation, environment changes on resumption and real connection setup are not implemented.

### Stalled-run recovery — approved

Desktop places the stall diagnosis, outstanding CI/review evidence and attempt history beside retry settings. Mobile splits these into connected “Why it stopped” and “Retry setup” views. The example stops early after three of five iterations because the same failure persists, while independent work continues. This is illustrative, not a chosen stagnation-detection rule.

Retry controls expose provider and model choices for implementation and review agents. Each agent can continue its conversation or use a fresh handoff; the example changes the implementation model and starts it fresh while preserving the reviewer's settings and context. A summary describes the scope, code revision, failed attempts and unresolved findings carried into a fresh start. The user explicitly selects an additional iteration allowance, shown again on the approval action, with previous history preserved. Run-only changes, separate retry accounting and the sample allowance are proposed presentation details rather than agreed defaults or final data-model decisions.

- [Desktop recovery](screenshots/recovery-desktop.png).
- [Mobile diagnosis and attempts](screenshots/recovery-mobile.png).
- [Mobile retry settings](screenshots/recovery-mobile-setup.png).
- [Source HTML](recovery.html).

The user accepted these desktop and mobile recovery layouts on 2026-11-10. This approves the visual direction, not the overall implementation specification. Models, providers, code revisions, evidence, capacity and retry values are illustrative. The prototype's bounded number control does not establish a product maximum. Local Chromium checks covered page overflow, mobile navigation between diagnosis and setup, model choices following provider changes, context choice, iteration allowance updating the approval label, and a mockup-only retry dialog. Actual agent continuation, handoff generation, scheduling and retries are not implemented. The retry dialog explains the prototype only and does not propose another production confirmation step.

### Ready-to-merge PR — approved

Desktop and mobile mockups extend the approved CI-first, narrated PR experience into its successful state. Passing CI and configured reviews appear in a compact expandable summary above the engineering walkthrough. An existing repository/provider branch preview appears with its reported status and revision. The walkthrough explains the issue, approach, alternatives, final repair, evidence and scope limits; relevant code is optional and expandable.

The desktop merge panel shows the revision, target branch, merge method, conflict status and human-approval policy beside the narration. Mobile places merge controls after the report, with review history collapsed initially to reduce scrolling. Request changes, send to factory and open in forge remain visible. The illustrative merge method is squash, not an agreed default. The example uses one revision consistently across checks, reviews, preview and narration. Provider permissions and protections still need detailed integration design.

- [Desktop ready PR](screenshots/ready-review-desktop.png).
- [Mobile ready PR, full scrolling page](screenshots/ready-review-mobile.png).
- [Expanded mobile checks](screenshots/ready-review-mobile-checks.png).
- [Optional mobile code excerpt](screenshots/ready-review-mobile-code.png).
- [Source HTML](ready-review.html).

The user approved these desktop and mobile ready-to-merge layouts on 2026-11-10. Approval covers their presentation and interaction direction, not the overall implementation specification. PR identifiers, code, revision hashes, test/review results and existing preview deployment are illustrative, not live repository evidence. No preview infrastructure is created. Local Chromium checks cover horizontal overflow at the captured widths, expanding checks/code, and opening/closing a mockup-only merge explanation. Nothing merges or calls a provider. The explanation dialog is a prototype safeguard, not a proposed production confirmation flow.

### Human decisions — approved

Desktop and mobile mockups explore the agreed decision flow: present clear options as action buttons, make discussion optional, let independent work continue, and resume authorised affected work automatically when eligible capacity is available.

The illustrative decision concerns recovering unfinished transcript drafts after closing the app. Each option explains its consequences before its action button. The planner's recommendation and reason for escalation appear with the decision. Desktop places waiting and continuing work alongside the choices; mobile shows a compact status count above the choices and detailed work status below. Discussion opens alongside the choices on desktop and as a separate screen on mobile. The resolved state records the chosen outcome and distinguishes a capacity wait from an unresolved human decision, without requiring another resume action.

- [Desktop decision](screenshots/decision-desktop.png).
- [Mobile decision, full scrolling page](screenshots/decision-mobile.png).
- [Optional mobile discussion](screenshots/decision-mobile-discussion.png).
- [Resolved decision and automatic capacity wait](screenshots/decision-mobile-resolved.png).
- [Source HTML](decision.html).

The user approved these desktop and mobile decision layouts, including optional discussion and the resolved state. Draft retention options, recommendation, task counts, PRs and host capacity are sample content, not new product decisions or live activity. The example must not be interpreted as approval of a retention policy. Local Chromium checks covered horizontal overflow at the captured widths, opening and closing discussion, and selecting the recommended option to show a resolved state. Messaging, scheduling, persistence and external notifications are not connected. Resolving decisions conversationally, revising an already resolved decision, and concurrent responses remain open design details. This approval covers the visual flow, not the overall implementation specification.

### Work item during execution — approved

The user wants what agents are doing to be the primary content. Include a progress indicator at the top, but keep it compact so it does not occupy most of the space. On smaller displays, expand that indicator/area to reveal more overview information. The user selected a combined activity timeline across agents, with drill-in options to inspect individual agents. The user approved the desktop and mobile execution mockups shown below.

The user confirmed unrestricted choice of steering path: users can message and steer individual agents directly or work through the planning agent. No mandatory routing through the planner should be imposed. This concerns the human's choice of interaction path; it does not remove the previously agreed limits on autonomous agent decisions.

Proposed presentation details: identify the agent and associated task/PR on each event; show meaningful work, handoffs, CI results, review findings, and repairs by default; expand raw tool output and logs on demand. Preserve timeline position when returning from an agent drill-in. Display concrete task/milestone progress rather than unsupported percentage estimates. Record user steering in the work-item history and make relevant direction changes available to the planner/reviewers, without adding a permission step or forcing the user to restate the instruction elsewhere. Delivery semantics while an agent is busy remain to be designed.

The approved execution layouts keep the shared timeline beside an individual agent conversation on desktop. Mobile opens the conversation as a separate screen with an explicit return to activity, and keeps the message composer outside the scrolling conversation. A compact progress summary expands to handoff scope, dependencies, placement and checks. The example shows an active repair, a direct human instruction, expanded failing CI with collapsed successes, and a specialist review finding. The planner remains directly accessible from the activity view. Approval covers these visual layouts; the sample data and iteration state do not describe live Marshal work, and the overall implementation specification remains in development.

- [Desktop execution and agent conversation](screenshots/execution-desktop.png).
- [Mobile activity, full scrolling page](screenshots/execution-mobile.png).
- [Mobile agent conversation](screenshots/execution-mobile-agent.png).
- [Expanded mobile progress summary](screenshots/execution-mobile-progress.png).
- [Source HTML](execution.html).

Local Chromium checks covered page overflow at the captured desktop and phone widths, expanding progress, opening the mobile agent view and returning to activity. Only these small navigation interactions are simulated; message delivery, filters, logs, CI actions and planner routing are not connected. Exact progress stages and iteration accounting remain open.

### Handoff review — approved with compact mobile option

Desktop and mobile mockups explore a partial handoff with two selected, versioned plans and another portion left in planning. The screen includes a proposed PR breakdown, model/provider defaults, specialist review briefs and a handoff-specific override, planning-agent authority, five-iteration repair limit, automatic placement or host pinning, and an approval summary. Approval starts execution when eligible capacity exists; otherwise authorised work waits for capacity. All documents, model choices and capacity readings are illustrative, not decisions about this feature's actual implementation or live host telemetry.

- [Desktop handoff](screenshots/handoff-desktop.png).
- [Mobile handoff, full scrolling page](screenshots/handoff-mobile.png).
- [Mobile authority and placement detail](screenshots/handoff-mobile-settings.png).
- [Source HTML](handoff.html).

The user approved this handoff layout on 2026-11-10, with a compact option for mobile displays. The existing screenshots show the expanded review state. Proposed compact treatment: summarise unchanged factory defaults in expandable sections, keep handoff overrides distinguishable, and preserve visible selected scope, authority/iteration summary, placement/capacity state, and approval consequences. Exact collapsed content and whether compact mode is the initial mobile default remain to be refined. No replacement mockup has been requested.

### Work item during planning

The user wants the conversation and work-item overview visible together, ideally side by side on a sufficiently large/full-screen display. On mobile, open into the conversation and provide an easy, explicit way to reach the overview, such as a button. The user confirmed that the overview should evolve automatically as the conversation progresses, rather than requiring a request to summarise or write up. The approved mockups below establish the visual direction; detailed update reconciliation and editing semantics remain open.

The user also confirmed that overview sections should be directly editable, alongside corrections made through conversation.

The user approved the following desktop and mobile planning-workspace mockups on 2026-11-10:

- [Desktop conversation and editable overview](screenshots/work-item-desktop.png).
- [Mobile conversation](screenshots/work-item-mobile-conversation.png).
- [Mobile overview](screenshots/work-item-mobile-overview.png).
- [Mobile side-by-side comparison](screenshots/work-item-mobile-comparison.png).
- [Source HTML](work-item.html).

The approved layout includes an explicit mobile Overview/Conversation button, quiet update cues, edit provenance, links to source discussion, inline section editing on desktop, and a separate Prepare handoff action. Conversation, document versions, model assignments, and section agreement labels are illustrative. Detailed update reconciliation and execution contracts still need design. This approves the planning-workspace design, not the overall implementation specification. No live updating or model integration is implemented.

Proposed interpretation: present the goal, current approach, decisions, open questions, linked specifications/plans, and eventually handoff status. Automatic summarisation should distinguish tentative ideas from agreed decisions and approved execution scope. This is not permission for the summary to approve new work. Preserve human edits and convey them to the planning agent; reconcile conflicting automatic updates visibly rather than silently overwriting edits. Editing approved/handed-off material should propose a revision rather than silently alter active execution. Detailed reconciliation and revision behaviour remain to be designed.

### Voice planning

- [B — questions alongside](screenshots/B-questions-alongside.png): default.
- [C — quiet capture](screenshots/C-quiet-capture.png): alternative mode.
- [Planning draft](screenshots/D-planning-draft.png): explored follow-on state; detailed editing/submission behaviour remains a proposal.
- [Interactive exploration](voice-planning.html).

### PR inspection

The user approved a combination of narrated review B and repair workspace C. Tapping a PR opens this view directly:

1. Expanded failing CI above the narration.
2. Related reviewer findings and factory repair status grouped with the failure.
3. Successful checks compact/collapsed.
4. Engineering walkthrough with optional code and evidence below.

- [Approved combined direction](screenshots/review-v2-combined.png).
- [HTML](change-review-v2.html).

The image represents a full scrolling page, not a single phone viewport.

### Fleet and project-factory dashboards

The user approved the grouping in these mockups:

- Fleet: shared attention area with direct actions, project factories grouped together, independent agents directly accessible.
- Project factory: decisions needing the user, work items with statuses and direct actions, underlying agents accessible at a secondary level.

**Accepted correction:** remove the purple workflow overview strip from the factory dashboard. Users already know the process they configured. No replacement mockup was requested. The existing screenshot still contains the strip and is superseded on this point.

- [Side-by-side dashboards](screenshots/fleet-factory-comparison.png).
- [Fleet](screenshots/fleet-dashboard.png).
- [Project factory](screenshots/factory-dashboard.png).
- [HTML](fleet-factory.html).

Screens show fictional model assignments, work, PRs, counts, and check results. The user approved layout direction, not those sample data choices. Exact copy, colours, typography, and navigation have not been separately finalised.

## 11. Remaining design and research

### Existing-project onboarding — current exploration

The user approved automatically creating a Marshal project when an existing repository is connected. Initially use the repository name, with renaming available. This does not automatically create a factory or start an agent. Duplicate connections, project identity and attaching a repository to an already-existing planning project still need design.

The user approved automatic initial inspection when connecting a repository: read project instructions/documentation, language/tooling and environment configuration, CI metadata/results and existing branches/PRs where available. Distinguish detected facts from recommendations. Initial inspection reads files and metadata; setup scripts and builds belong to workspace execution. The user then raised the integration approach for external forges, including whether support should depend on a forge CLI.

On 2026-11-11, the user chose existing-project onboarding as the next design area after reviewing the end-to-end journey synthesis. Explore connecting a repository, discovering its instructions, environment and CI, then choosing independent-agent or factory work. This selects the discussion topic, not an approved discovery implementation or authorisation to inspect/change a live repository now. Automatic read-only discovery was subsequently approved. Detailed treatment of existing configuration, missing access and the first-agent transition remain in exploration.

### Host connectivity — private network and Tailscale

The user plans to reach daemon hosts over a private network and Tailscale. They are uncertain about supporting public-internet exposure because of security concerns with self-hosted software. Design for private connectivity first, without requiring public exposure. Public-internet access remains undecided, not an approved initial requirement or a permanent product exclusion.

The user clarified that they would likely access the web endpoint itself over Tailscale and are wary of opening inbound ports into their home network. Their intended setup therefore has private access to both the web endpoint and daemon hosts, with no requirement for public router port forwarding. Do not interpret “one web endpoint” as a publicly exposed service.

Application authentication should be optional and configurable: users may set up a Marshal login if they want one, while a privately reachable deployment may operate without a separate application login. The user explicitly confirmed building authentication as an option. Supported authentication methods and the initial setup default remain to be designed. This decision concerns user access to the web app; it does not establish unauthenticated daemon-to-daemon connections or remove provider/forge credentials.

Multi-user access may be supported as an option later, but is not a priority. Focus the initial experience on a single owner with optional application authentication. Avoid making multi-user account administration or permissions a prerequisite for using the fleet. Later user identities, access scopes and shared-resource policies remain to be designed.

The Add host UI should provide a connection command that the user can copy and run on the new daemon host. The user confirmed this convenience. Command syntax, installation versus pairing scope, connection direction and enrolment credentials remain to be designed against the daemon's supported interfaces. Proposed follow-on: show the host connection result and discovered runtimes, architecture, environment capabilities and capacity. Discovery must distinguish verified capabilities from unavailable or unknown information.

Adding a host should also offer to install the daemon if it is not already present. The user confirmed this option. Support both an install-and-connect path and connecting an existing installation; do not interpret this as permission for the web UI to install software on a host without the user's chosen setup action. Installer distribution, platform/service support and handling existing daemon versions remain open. Any command shown in a future mockup must be labelled illustrative until supported by the daemon.

Proposed topology for discussion: the browser connects to one Marshal web endpoint, whose backend communicates with the enrolled daemon hosts over the private network/tailnet. This keeps individual daemon endpoints out of browser configuration and makes a phone's access path simpler. Placement of that backend, host enrolment, authentication, HTTPS and behaviour during host disconnections still require design. Private connectivity does not itself decide application-level access controls.

### Disconnected hosts — local continuation approved

The user confirmed that agents should continue already-authorised work locally while their daemon host is disconnected from the fleet UI/backend, provided the required resources and provider access remain available. This is a product requirement for the future architecture, not a claim that the current daemon supports disconnected execution. Loss of fleet connectivity must not itself be treated as agent failure or cancellation. Existing authority boundaries still apply; disconnection does not grant additional approval.

Supporting behaviour proposed for this discussion: show the host as unreachable with its last contact time and affected work's last known activity; stop assigning new work to it; permit eligible placement elsewhere for unpinned work that is confirmed not to have started; avoid automatically restarting potentially running tasks elsewhere; reconcile activity/results on reconnection; and expose deliberate recovery elsewhere with the available saved context and workspace changes. Dispatch acknowledgement uncertainty, reconciliation, durable activity storage and recovery ownership still need technical design. Subsequent disconnected/reconnected mockup approval established the presentation of last-known activity, paused new assignments, no duplicate restarts, and reconciled activity. Placement of confirmed unstarted work elsewhere and deliberate recovery on another host still need design.

### Steering unreachable agents — automatic queuing approved

The user confirmed that ordinary steering messages should queue automatically when an agent's host is unreachable. The subsequently approved presentation marks these messages as awaiting delivery and allows editing or cancellation before delivery. On reconnection, delivery and acknowledgement should be distinguished from queuing, and the agent should consider the direction against its current progress. Exact delivery semantics, ordering and handling of stale or no-longer-applicable instructions remain to be designed.

Related proposals from the discussion: a stop request remains visibly unconfirmed until acknowledged; decisions requiring human approval pause the affected work while independent authorised work may continue. The user's explicit confirmation concerned automatic queuing of ordinary steering messages; it does not approve automatic execution of stale commands or establish stop/cancellation semantics.

### Host disconnection notifications — approved

The user approved showing a disconnected host immediately in the UI while keeping brief connection drops quiet in outbound notifications. Notify when the disconnection persists or blocks progress. The grace period should be configurable; its default duration remains undecided. This approval concerns notification timing, not a delay in displaying the host's last known state or stopping new assignments to an unreachable host. Alert deduplication, recovery notifications and detection of blocked progress remain to be designed.

### Environment foundation — direction approved after feasibility research

The user's 16 GB Mac mini is an Apple Silicon M2 running OrbStack, and they prefer Podman on their Linux boxes. They explicitly want the broadest practical runtime compatibility. Proposed first validation targets are OrbStack on macOS ARM64 and Podman on Linux, with Docker Engine/Desktop included in the compatibility matrix from the outset. Environment definitions should remain portable and host placement should use verified capabilities; personal runtime preferences must not become a product restriction.

The feasibility report includes OrbStack's documented Docker/Compose compatibility, dynamic memory reclamation and shared runtime resource ceiling. The earlier Docker Desktop Resource Saver caveat is specific to Docker Desktop and should not be applied to the user's OrbStack setup. No OrbStack or Podman instance or user configuration was accessed or tested.

The user authorised a documentation-based feasibility pass, kept separate from the independently progressing daemon work. Findings and primary-source links are recorded in [Environment foundation feasibility](environment-feasibility.md), dated 2026-11-10. No runtime spike, hardware benchmark, implementation or repository mutation was performed.

The user approved the recommended foundation on 2026-11-10: reuse the Dev Containers reference CLI and Compose through execution adapters, retain native environments, and let Marshal own durable workspace identity, lifecycle, capacity admission and host placement. The report compares this with integrating a broader workspace platform or building a proprietary environment system. Approval establishes the design direction; detailed contracts, runtime compatibility and the proposed validation experiments remain outstanding.

Material findings: existing host-local workspaces have storage affinity even when new work is automatically distributed; retaining files and service volumes is distinct from preserving live process/session state; arbitrary Dockerfiles/Compose need a source-editing fallback; container engines still require explicit capacity management; unrelated containers can prevent Docker Desktop's VM from idling on a constrained Mac. The current Marshal deployment documentation's containerized bridge/ACP socket topology needs a specific macOS integration proof. Recheck it against the daemon's current contracts before implementation. A bounded follow-up experiment is outlined but has not been run.

### Capacity and shared hosts — agreed direction and remaining mechanics

The user expects self-hosting across a wide range of hardware. Their Marshal Studio prototype ran on a Mac mini with 16 GB RAM alongside an unrelated container stack, and resources were constrained. Numeric resource limits and admission mechanics remain open. Automatic start following handoff approval was subsequently agreed, as recorded below.

The user chose automatic adaptation to available capacity by default, with optional user overrides. They are independently making Marshal more flexible and daemons connectable, envisioning multiple daemon hosts/servers running agents through one UI. Plan for multiple hosts from the outset without interrupting that daemon work.

The user chose automatic distribution across eligible hosts, with an option to pin work. Exact pinning scope and semantics have not yet been specified.

The user confirmed automatic execution following handoff approval when capacity is available; no separate manual release is needed. Approved work without capacity waits for an eligible placement.

Updated proposals for discussion, not decisions: manage resource/concurrency capacity per host and present it across the fleet; factories and independent agents share available host capacity. Distinguish agent overhead from tool/build/test/container workloads and any locally hosted models. Retain headroom for unrelated host services and planning/human interactions. Show why work is waiting. Do not assume every host has equivalent project access, credentials, toolchains, or local models. Apply placement decisions to new work; live migration and interruption of running work require separate design. Pinning scope, workload portability, connection topology, resource measurement/enforcement, defaults, and scheduling fairness remain open. Multiple hosts do not change the agreed project-scoped factory model or prioritise cross-project factory operations.

- Work-item naming, detail view, lifecycle, and acceptance/completion evidence.
- Factory creation and customisation flow; handoff editor and default precedence.
- Incremental handoff, document versioning, PR dependencies, and changes during execution.
- Delegation policy representation, escalation UX, and planner transfer.
- Repair iteration accounting, stagnation detection, CI infrastructure failures, and reviewer disagreements.
- CI/notification integration mechanics, evidence freshness and edge cases; clear/ready PR presentation has approved mockups.
- Forge integration architecture: existing providers, Gitea underneath, or a fork.
- MCP handoff contracts and an actual Claude Code terminal integration proof; web-harness capabilities.
- Transcription/live questioning feasibility and data retention.
- Responsive desktop/tablet arrangements, keyboard navigation, accessibility, and cross-device continuity.

### Bootstrap compatibility assessment — completed, recommendation pending

On 2026-11-11, at the user's request, inspected GitHub main at `bff50180cf1e282430d20eeccb0d508850ba88d7` without changing repository or daemon work. [The compatibility report](bootstrap-compatibility.md) records substantial existing reuse: fleet/ACP execution, workspaces, GitHub/Gitea adapters, structured recipes, review and CI-fix bots, and the agent-side SDD pipeline. The main new work is durable factory sequencing/recovery, one delivery branch and early draft PR, complete current-revision CI evidence, and the factory-wide repair allowance. Recommended initial architecture is a fixed controller over existing agent/recipe primitives in the Go bridge, with a small responsive Svelte surface. SDD remains a reuse candidate but its nested budgets, branch ownership and completion semantics need an explicit adapter. This recommendation is not yet an approved implementation design. The inspected source still precedes the P3 embedded-client switch and P4 local daemon profile; current unpushed work is unknown. No runtime tests were run.

### Bootstrap design and implementation plan — drafted for review

At the user's request, wrote the [focused bootstrap design](bootstrap-factory-design.md) and [implementation plan](bootstrap-factory-plan.md) on 2026-11-11. The plan has eight testable tasks: durable state, GitHub evidence, reliable workers, bounded controller, recovery/ownership, API lifecycle, responsive UI, and end-to-end/live acceptance. No product code was changed. A separate builder check found four important gaps and the draft resolves them: stop-and-retain parking rather than container detachment, explicit successful prompt/report validation, versioned amendments on manual retry, and CI diagnosis without assuming that every failure is a code defect.

Assistant proposals now made concrete for review include five total work-changing rounds including initial implementation, human handling of questions, GitHub-first delivery, one active factory item, all reviewer findings actionable, file-journal persistence, and conservative recovery. These choices are not retroactively attributed to earlier approvals. Implementation and runtime validation remain outstanding. The wider product design and user request to stop further mockups are preserved.

## 12. Local artifacts and verification

### End-to-end synthesis — available for discussion

On 2026-11-11, consolidated the agreed behaviour and approved screens into [an end-to-end journey map and gap inventory](journey-map.md), with a [high-resolution visual map](screenshots/journey-map.png). The synthesis covers new projects, existing repositories and independent agents, optional factory delivery, interruptions and return paths, and shared fleet resources. It distinguishes approved behaviour from unresolved transitions and technical validation. Recommended gap priorities are proposals, not an approved roadmap. No implementation plan, runtime integration or daemon change was undertaken.

The recommended next discussion is first-use connections and existing-project import/discovery, followed by factory creation/adoption, plan revisions during execution, work completion/cleanup, failure semantics, review evidence and mobile continuity. Earlier stale statements about missing planning mockups and undecided automatic start were reconciled with subsequent approvals.

All exploration artifacts live under `/workspace/marshal-concepts/`. HTML and screenshots are separate from the Marshal checkout. Local screenshots were used because the cloud HTML preview did not work correctly for the user.

Voice prototype interactions were exercised in Chromium at desktop and phone widths. Review and dashboard screenshots were inspected for layout and content fit. These checks validate mockup presentation only, not real factory, CI, forge, MCP, or voice functionality.

This document is the primary working design record going forward. Append or revise decisions as the conversation continues and keep rejected/superseded choices distinguishable from current direction.
