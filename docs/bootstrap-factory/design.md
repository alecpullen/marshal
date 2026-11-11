# Bootstrap factory design

Date: 2026-11-11 (Australia/Sydney)
Status: implemented and locally tested with the companion [implementation plan](implementation-plan.md). The [acceptance record](acceptance.md) tracks pending live and ARM64 deployment gates.
Baseline: alecpullen/marshal main at bff50180cf1e282430d20eeccb0d508850ba88d7. Recheck before implementation. Sources and limitations: [compatibility assessment](compatibility.md).

## 1. Goal and scope

Deliver one real, supervised loop: approved plan → early draft PR → implementation → existing CI and independent review → repair → ready for human merge. Use this version to build subsequent Marshal improvements. Keep the broader fleet and factory design as the destination, not an initial-release checklist.

Bootstrap limits: one configured GitHub repository, one bridge host, one active factory work item, one PR per item, one implementer and one reviewer running serially. More items may queue. Independent agents remain available and share the existing fleet capacity controls. Store PR links as a collection and retain host identity so these delivery limits do not become permanent cardinality constraints.

Input is a pasted/imported specification and plan from the user's existing planning harness. No MCP dependency, embedded planning model, coordinator, automatic decomposition, live adoption, editable workflow graph, fleet distribution, voice, notifications or new forge is required. Questions go directly to the human in this release. Do not add an embedded planner merely to reproduce future delegated authority.

Existing configured model/provider routes and environments are selectable. Implementation uses act mode with the selected existing permission/auto setting, snapshotted at approval; absent an explicit selection, auto is false. Review uses ask mode. Handoff approval authorises factory sequencing but does not bypass agent tool permissions. No new provider or environment setup wizard. Preserve the existing bridge authentication and trust mechanisms; broader optional-auth changes are deferred. The browser uses the existing authenticated HTTP surface. No new public exposure requirement.

## 2. Approach and ownership

The compatibility discussion established the recommended direction and the user asked to write it up. The design and detailed defaults below were subsequently approved for implementation.

Alternatives considered:
- Adapt the SDD controller immediately: reuses task execution and checkpoints, but requires reconciling nested retries, private pipeline worktrees, branch results and ACP option differences.
- **Selected approach:** a fixed factory controller in the existing Go bridge, composing ordinary ACP sessions, existing review-result parsing and forge clients. Reuse worker execution; add only delivery orchestration and durable evidence.
- Build a general workflow engine first: postpones the first useful loop and is deferred.

The Go bridge owns factory state, dispatch intent, the delivery branch, forge writes, CI observations, scheduling and the overall allowance. ACP owns model execution, tools, approvals, questions, session state and cancellation. The Svelte UI renders state and sends commands. Closing a browser never cancels the backend run.

All Go code under web/ remains standard-library-only, with no marshal/internal imports. Keep the factory behind a narrow local execution adapter; do not set the newer Marshal ACP capability flag globally or change daemon contracts as part of this build. Reuse the bridge's current protocol path. If the target daemon revision breaks the necessary contract, isolate that integration issue before continuing; do not modify concurrent daemon work silently.

## 3. Lifecycle and attempt counting

States: draft, queued, preparing, implementing, checking, reviewing, repairing, needs_input, stopping, stopped, recovering, ready, closed. Keep the last completed stage and a typed blocker/recovery reason separately. closed records an externally merged/closed PR; the factory does not merge.

Creating a draft work item starts nothing. Approve & start freezes the plan/configuration and queues it. Eligible queued work starts automatically, FIFO by approval time. The scheduler allows one active item and uses fleet admission for workers. An unresolved/uncertain running worker retains ownership; a human-blocked item may release the factory slot only after its worker is confirmed idle. Independent work is unaffected.

Proposed interpretation of the previously agreed default of five iterations: five total implementation/review rounds, including the initial implementation. Round 1 implements; rounds 2–5 repair. A round consumes its allowance once a work-changing prompt is durably marked for dispatch; uncertain dispatch retains that consumption until reconciled. Read-only CI polling, review, and decisions do not consume another round. Do not silently retry a failed model turn; present a retry decision.

A tool permission/question answered during an in-flight turn continues that same round. A final needs_input report ends the turn; answering it starts another work-changing turn and therefore consumes the next round. If no allowance remains, retain the answer and require an explicit grant before dispatch.

After each successful implementation/repair: commit through the existing ACP commit operation, observe the actual Git head, push that head to the factory branch, wait for CI, then review. If CI fails, collect its evidence and run the reviewer against that same revision before preparing one combined repair brief. GitHub access/transport failures, cancelled/timed-out/action-required checks and missing checks are blockers, not code-fix prompts. An ordinary failure conclusion goes into the repair brief as evidence to diagnose; GitHub does not distinguish code failure from infrastructure failure reliably. The implementer must reproduce or investigate it and return a structured needs_input result if it cannot reproduce it or suspects infrastructure, rather than disable checks or claim a fix. At exhaustion, stop automated dispatch and request input.

Retry requires an explicit additional allowance (positive integer), an optional new implementer/reviewer route, and a reason. When all affected work is confirmed idle, the same command can submit replacement plan/spec text or reviewer instructions for explicit renewed approval. Append a new immutable approval revision; retain earlier revisions and invalidate prior CI/review readiness. This is a manual replacement form, not automatic plan assessment or an embedded planning workflow. It appends a new allowance grant; lifetime round numbers/history never reset. Same-route retry can continue the saved implementer session; changing a route uses a fresh session with approved plan, current revision, prior findings and blocker summary. No native cross-provider conversation portability is promised.

No-progress rule for v0: if a completed repair leaves the tracked repository tree unchanged while actionable findings remain, stop immediately for input. Different commits containing the same tree do not count as progress. Changed trees may continue up to the cap; do not claim semantic stagnation detection. A worker failure/invalid result stops for input rather than consuming repeated automated turns.

## 4. Work item, persistence and command semantics

Each item stores: ID, version, repository/project/host references, title, immutable approval revisions containing plan/spec text and hash, configuration, approval, state/stage, allowance grants/rounds, worker links, delivery branch and PR links, CI/review evidence, report, pending decision, effect records and timestamped history. Store references to credentials only; use the existing credential resolver. Never include credential values in this record or prompts.

Approved configuration includes model routing for both workers, an existing environment reference (or explicit native mode), expected CI identities, reviewer instructions and the initial allowance. Existing project defaults populate the form; the approval snapshots the resolved values. Native mode remains subject to repository trust and permissions.

Use a separate standard-library-only journal under <state>/factory/v1/. One process owns this directory using an exclusive lock file. A second process refuses to open it. A lock remaining after a crash requires an operator to confirm the previous bridge is stopped before removing it; no PID-only or time-based automatic takeover. Document this recovery procedure.

One append-only JSONL journal serializes all factory mutations. Each record has monotonic sequence, request ID, item ID, complete resulting item snapshot, and SHA-256 over the encoded payload. Append, flush and fsync before publishing state or initiating its next effect. Restore from replay; truncate an incomplete final line only. A complete malformed/checksum-invalid record or unsupported version prevents scheduling and produces a visible startup error. Persistence failure prevents new effects; already-dispatched work may still be running and must be shown as such. No compaction or automatic workspace deletion in v0.

Every mutating API command includes a unique requestId and expectedVersion. Answers additionally name the current decision ID so an old question cannot settle a replacement decision. Repeating the same request with identical contents returns its original result; reuse with different contents or a stale version returns conflict. The journal is the authority, not the SSE ring or in-memory completion callback.

An effect record is persisted before spawn, prompt, commit/push, PR creation or ready/draft mutation. It contains an ID, kind, stage/round, relevant agent/branch/revision, and state prepared/dispatched/completed/uncertain. This is durable intent, not a claim that ACP or GitHub implements exactly-once execution.

## 5. Workers, workspace and early draft PR

Use one factory-owned implementation checkout and branch: marshal/factory/<item-id>. Create it from the approved target branch's recorded base SHA. Start from a clean tree. Seed committed plan/spec documents at docs/marshal-factory/<item-id>/, using normal safe path handling and a non-user-controlled ID. Commit through ACP, push the branch and create a draft PR before the first code-changing prompt. Seed preparation is outside the five model rounds.

The existing Exit flow is not used as the factory lifecycle: it assumes a late PR and has a verify gate inappropriate for the initial docs-only seed. Reuse its credential resolution, commit and push primitives behind dedicated factory operations. The controller checks the actual commit and remote ref, rather than trusting a worker's claimed SHA. Never force-push, reset or delete retained user work.

Reuse Fleet.Spawn/Registry/Child and existing routing. Add optional factory identity/role metadata to agent records so a restart can find workers. Keep model execution on the agent. A completed implementation requires ACP stopReason=end_turn, a new matching operationId in the final structured report, outcome=complete, and observed Git/work status consistent with that report. Cancellation, missing reports, provider/limit errors and other stop reasons never imply completion. The alternative report outcome is needs_input, with a question and reason (including not_reproducible or infrastructure_suspected). Extend Registry with a result-returning prompt method while preserving existing Prompt callers. Implementation prompts ask for the approved scope, test evidence, a concise engineering report and explicit questions when blocked; forbid autonomous merge/push in the task instructions. Branch observation still detects changes regardless of their origin; instructions alone are not treated as a concurrency guarantee.

Reviewers get a separate checkout pinned to the pushed head, the approved spec/plan and the base SHA, using read-only/ask mode and the selected model route. Reuse the review recipe's structured findings format, extending the prompt to require explicit reviewed base/head and access confirmation. Validate those fields against controller-observed Git state; an inaccessible diff or malformed/missing report is a blocker. Check reviewer checkout cleanliness after its turn; unexpected changes block the review.

All returned findings (including nits) count as actionable in v0. Readiness requires an explicitly valid empty findings list. Do not inherit SDD's rule that minor findings may remain. A disputed finding becomes human input; an initial user can amend the task/reviewer instructions and explicitly retry rather than an unimplemented finding-waiver system.

Park the implementer only after its turn, all owned work and publication complete; preserve its checkout and saved conversation. Existing Fleet.Pause only detaches a container, so add a factory-specific park operation that disables supervision, stops the confirmed-idle runtime, verifies it has exited, and releases its slot without calling the workspace-deleting retirement path. Container --rm cleanup may remove only the runtime container, never its retained mounted checkout/state. Resume uses the existing restoration path. Factory spawns bypass warm pooling in v0 to give each worker an unambiguous runtime and persistent checkout. No shared/unowned supporting service is stopped. Run the reviewer, then park it before resuming the implementer. Parked agent metadata must not cause startup ReattachAll to launch every historical factory worker. Route factory-owned restoration through factory reconciliation. Verify actual slot and process/container release; serial prompts alone do not guarantee low memory use. Retain all workspaces until explicit existing cleanup actions; refuse cleanup of active/uncertain factory work and retain references/history when terminal workspaces are later removed.

## 6. CI and forge contract

GitHub first, using an optional factory capability interface implemented by the existing GitHub client; do not force unsupported implementations into Gitea or change other Forge consumers. Required operations: complete check/status observation, exact branch/PR lookup, draft creation, mark-ready and convert-to-draft. Reuse existing error/rate-limit/credential conventions.

Enumerate every page of check runs (latest attempts) and commit statuses. An expected identity is either check-run name plus GitHub App ID, or commit-status context. Never satisfy a required check from a different application. The form can suggest observed identities but the human confirms the expected list at handoff. Marshal currently has CI but no protected-branch required contexts in the inspected branch response. An empty expected list requires explicit 'This repository has no required CI' acknowledgement; do not silently infer it from a failed fetch or empty response.

For the reviewed head: queued/in-progress checks and pending statuses mean waiting. Missing expected checks remain waiting with a visible explanation and human decision after a proposed 30-minute configurable wait threshold. An expected check passes only on success; skipped, neutral, cancelled, timed-out and action-required results cannot satisfy it. Failed observed checks, including non-required checks, are surfaced and block readiness. Non-required skipped/neutral results are shown as such but do not themselves require repair. Permission/rate/network errors are unavailable evidence, never success. Retry reads with existing rate-limit guidance; a timeout does not trigger a code repair.

Poll while active, default 15 seconds, backing off to the provider's rate-limit/retry response when longer. Do not require inbound webhooks. The 30-minute wait is a UI escalation threshold, not a command runtime timeout or authority to kill work.

Review evidence binds to both head SHA and base SHA. Before ready, fetch PR state/head/base and current CI again. Any changed head or base invalidates review and returns to evidence collection, within the existing budget if code repair is needed. Check again after the GitHub ready mutation; if the revision raced, convert back to draft and invalidate readiness. GitHub does not offer an atomic 'mark ready only at this SHA' primitive: surface unknown/failed compensation rather than promise elimination of this race. Continue observing ready PRs while the service runs; changed revisions revoke Marshal readiness. Offline changes are detected on reconnect.

Find an existing PR by repository + factory head branch + base before create and after uncertain create responses. Persist its number, URL and node ID. Multiple matches, changed branch ownership, external closure/merge or branch divergence require explicit handling; never create an automatic replacement PR. Human merge stays external through the forge link in v0.

## 7. Restart, stop and competing actions

On startup, replay factory state before scheduling and before reattaching factory-tagged workers. Reconcile their persisted agent/session identities, runtime status, checkout, remote branch and PR. Completed effects with matching durable evidence can advance; uncertain prompts/spawns cannot be automatically resent. A surviving container is not itself proof that a turn succeeded. If completion cannot be proven from the current ACP contract, enter recovering with links and an explanation.

Recovery offers refresh evidence; stop the known worker; or, after verified termination/absence or an explicit operator attestation that an otherwise unreachable worker has stopped, resume from retained code with a fresh summary and additional allowance. Record that attestation and its limitations. Never accept 'retry' as permission to leave a possibly running old worker and start another against the same branch. An unidentifiable spawn blocks recovery until the operator resolves its ownership.

Stop first records stopping and disables all future stage dispatch. Send existing cancellation to the active turn. Only after confirmed turn completion/idle may the item become stopped and release its execution slot. If confirmation is unavailable, remain stopping/uncertain. Detached commands can outlive turn cancellation; check active work through available ACP/work views and require resolution before reusing the checkout. Do not substitute process termination for successful cancellation silently.

Factory-owned branches are excluded from independent review/CI bots and scheduled automation that would mutate them. Existing UI merge, discard, exit, new-run and deletion actions on active/uncertain factory workers return a conflict with a link to the work item. Ordinary user steering and approval/question answers remain available; they are recorded/linked, and any changed revision invalidates prior evidence. The controller is the sole dispatcher of factory stages. External Git edits cannot be locked by Marshal; detect divergent remote heads and pause rather than overwriting them.

## 8. Minimum responsive UI and HTTP

Add a factory entry from the existing project view; retain the main fleet manager and independent-agent navigation. Routes: #factory and #factory/<item-id>. A list shows queued/running/needs-input/ready items with one primary action. The creation form captures the plan, configured repo/environment, model routes, review prompt, CI expectations and allowance. Show the approved summary before Approve & start.

The detail page puts state and required human action first, then failing CI, compact passing checks, engineering report and activity. Agent drill-in and external PR links reuse existing navigation. On narrow screens use a single column, expandable evidence, labelled touch controls and no drag-only interactions. Desktop can show evidence beside activity. Do not build the full approved mockup suite or a new mockup during this task.

REST under /api/factory/items: list/create; /{id}: get; /{id}/commands: approve, answer, stop, retry, reconcile, recover. Requests carry command identity and expected version. Reuse existing agent permission/question endpoints for pending tool requests; factory answer resolves an explicit factory blocker. Creation and all commands use existing auth/JSON error handling. Unsupported forge/runtime capabilities are explicit validation errors.

For v0, poll visible factory state every 2 seconds while active and every 15 seconds when idle, backing off errors and stopping when the page is hidden; refresh immediately on return. Existing agent SSE supplies drill-in activity. The durable item history provides factory activity. This avoids extending SSE replay semantics before the loop works.

## 9. Acceptance and rollout

No production migration, auto-deploy or auto-merge. Use an isolated checkout/worktree for implementation. Preserve current daemon work; integration changes are small and revisited on the current main before merging.

Automated tests must prove: persisted command/effect replay; duplicate and conflicting commands; incomplete-tail versus corrupted-journal handling; lost dispatch/create responses; no duplicate worker/PR on restart; stop while dispatching; stale head/base and ready-mutation race; missing/failed/stale/unauthorised CI; invalid reviewer output; no-progress and fifth-round exhaustion; bot exclusion; browser reload and responsive operation.

Use injected clocks, fake ACP/GitHub services and disposable Git repositories. Ordinary tests must not read user credentials/config or access live services. Follow repository test isolation and TestWebIsStdlibOnly. Baseline Go is 1.26.1 with cgo/C compiler; UI remains Svelte 5, TypeScript, Vite and Vitest.

A live opt-in acceptance run must exercise the real model/provider and current daemon on a small agreed branch/PR, including one repair, human blocker/stop and restart reconciliation. First developer smoke can use native mode. Before using it on the user's Mac mini, validate OrbStack ARM64, shared path visibility, parking/resume and resource use. Linux Podman remains a subsequent compatibility run, not a claim established by fake-runtime tests. If the necessary runtime is unavailable, report the outstanding gate rather than declaring deployment-ready.

First self-development work should improve a small UI/reporting feature. Keep the stable controller running while changes are built on branches; deploy reviewed upgrades between runs.

## 10. Proposed choices requiring review

The most visible choices are five total rounds including the initial implementation, one actionable reviewer with no waived findings, manual external planning input, human handling of questions, one active factory item, GitHub first, and conservative manual recovery when ACP cannot establish an outcome. File-journal storage, polling intervals, CI wait escalation and fresh-session handling after a model change are implementation proposals. These can be adjusted before execution; they are not a request to resume broad product brainstorming.

## Builder check

A separate software-engineer check identified four material gaps: container parking only detached compute; prompt completion did not expose a success contract; immutable approvals conflicted with retry amendments; and CI failure conclusions could not reliably classify infrastructure problems. This revision resolves them with factory-specific stop-and-retain parking, explicit stopReason/report validation, renewed immutable approval revisions on manual retry, and evidence-based diagnosis with structured escalation. These are assistant implementation proposals for review.
