# Bootstrap factory compatibility check

Date: 2026-11-11 (Australia/Sydney)
Repository: alecpullen/marshal
Inspected main: bff50180cf1e282430d20eeccb0d508850ba88d7 (merge of p3-6c-ending, 2026-11-09 UTC).
Main still pointed to this commit at the end of inspection.

This is a read-only source assessment, not a runtime validation or code review. Files were obtained through the GitHub connector and selected copies retained in research-snapshot/. No repository files, branches, agents, services or independent daemon work were changed. Unpushed work and the state of the user's machines are outside this assessment. No build, tests, live models or container runtimes were run.

## Conclusion

A small factory can be built largely from existing bridge capabilities. Keep the current Go bridge and Svelte/TypeScript toolchain for the bootstrap; implement a new, small responsive factory surface within them. Reusing infrastructure does not require retaining the prototype's product layout.

Recommend a durable, fixed factory controller that composes existing ACP agent sessions, structured review recipes and forge adapters. It owns work-item state, the delivery branch/PR, evidence and the overall attempt allowance. The agent continues to own tools, permissions, model execution and workspace operations. Do not build a general workflow engine or change the independent daemon migration as a prerequisite.

There is already an SDD implementation/review pipeline. It is useful prior art and a potential later execution stage, but wrapping it as though it were the agreed factory would conceal different budgets, worktree ownership and completion semantics.

## What exists and what remains

| Need | Source evidence | Bootstrap consequence |
|---|---|---|
| Agent launch and isolation | Fleet.Spawn, registry sessions, local process/container transports and registered-repository checkout preparation | Reuse launch/workspace support; validate one actual runtime configuration first. No new environment manager. |
| Plan handoff | intake.go accepts plans/prompts and persists pending external submissions; runs.go writes plan markdown and dispatches SDD | Reuse input and approval patterns. A durable factory work item is still needed. |
| Model selection | SpawnOptions.Routing, recipe routing overrides; modern SDDParams carries role-to-preset overrides | Use configured provider/model choices; no provider implementation needed. Check selected route actually reaches each worker. |
| Execution and decisions | Registry.Prompt, cancellation, approval/question brokers, existing SDD gates | Reuse the existing channels. A successful RPC alone is not proof of successful implementation or readiness. |
| Review output | review-pr recipe, ReviewFindings parser, review drafts linked to HeadSHA | Reuse parsing and review presentation patterns; bind every factory review to its examined revision and treat invalid/missing output as blocked. |
| CI repair | fix-ci recipe and CI fixer with history, current-tip checks and loop protection | Reuse prompts/log retrieval and result structures; do not enable an independent bot to race the factory on its branch. |
| Forge access | Forge interface with GitHub/Gitea implementations, CreatePR accepts Draft, PR/check-log retrieval | Extend this interface rather than invent another provider layer. GitHub first remains the smallest initial delivery. |
| CI readiness | ListFailedChecks returns only failure conclusions from check-runs, first page | Add complete paginated checks/status observations and expected-check policy. No failures is not equivalent to passing CI. |
| Early draft and ready state | Existing Exit commits/verifies/pushes and creates a PR at exit; Forge has no mark-ready operation | Add explicit early draft creation, branch update and ready transition. Do not reuse exit as the whole lifecycle. |
| Persistence | fleet.json, automation history/drafts, SDD manifest/checkpoints | Reuse storage conventions, but add durable factory intent, stage and outcome records. Current recipe completion callbacks and automation in-flight claims are memory-only. |
| Browser continuity | Backend dispatch detaches from request cancellation; existing SSE and status routes | Browser closure need not stop work. Restoring factory state after server restart requires additional reconciliation. |
| Recovery | Container reattachment and ACP socket host survive client disconnection; processTransport.Detach terminates local children | Distinguish browser, bridge, agent and host failures. Do not promise native-process restart survival on this snapshot. |

## Daemon boundary and current migration

The daemon design explicitly makes the bridge the daemon/control plane, with ACP project hosts beneath it. A second competing server that takes over agent lifecycle would work against that architecture.

The P3 document records the controls, ending and browser groups, including plans 6c and 7b. Plan 8 switches embedded TUI execution onto the pipe; internal/launch/launch.go still imports the in-process client at this snapshot. P4's local service profile, RPC passthrough, project routing and related work are a subsequent phase. These observations do not describe any newer unpushed progress.

The existing bridge initializes ACP with protocolVersion only. Newer Marshal-capability handlers differ from the legacy handlers, including SDD routing/options, human-gate errors and request handling. Do not merely add the capability flag to unlock methods: client request/event handling must match the selected contract. Isolate factory agent operations behind a small bridge-side adapter and recheck against the daemon revision used for implementation.

Repository constraint: web Go code must remain standard-library-only and must not import marshal/internal packages. TestWebIsStdlibOnly enforces this. In particular, the bridge cannot directly import internal/pipeline, internal/acpclient or the existing SQLite package. Prefer a small versioned file-backed store with tested crash behaviour for the first factory; exact write protocol belongs in the bootstrap design.

## Why not simply expose /sdd?

The controller already implements task execution, local build/test gates, reviewers, repairs, final branch review, human gates, checkpoints and a task ledger. It never merges automatically.

Its current semantics differ from the proposed factory:
- MaxFixRounds defaults to three, with separate task verification and task review loops; transient dispatch retries are separate again.
- Final branch review allows one repair and one re-review.
- Clean means positive spec/quality verdicts with no Critical/Important findings; Minor findings may remain.
- SDDParams exposes plan path, strategy, role overrides and verification commands, but not a factory-wide repair allowance.
- The controller creates its own pipeline branch/worktree, with an explicit successful-run cleanup path. A bridge agent's checkout is not automatically that delivery branch.
- Run manifests/checkpoints are useful recovery building blocks, but they do not supply an end-to-end factory dispatch/PR reconciliation contract.

Approaches considered:
1. Adapt SDD first: substantial execution reuse, but needs an explicit branch/result/budget adapter and potentially coordinated agent-side changes.
2. Recommended bootstrap: fixed bridge controller over ordinary implementer sessions plus a separate reviewer recipe/session. Reuse existing execution, tools and provider routing; keep one factory-owned delivery branch and budget.
3. General workflow engine now: supports the long-term editor, but adds scheduling and schema work before the first useful loop.

The recommended controller is intentionally small: it sequences existing workers and evidence, rather than implementing another coding-agent runtime. Existing SDD can later become an optional execution strategy behind that boundary.

## Concrete first build boundary

Proposed sequence, not yet an implementation plan:

1. **Durable work item and one implementation.** Store the approved plan/configuration and branch identity; dispatch through the existing fleet/ACP seam; display activity and human requests. Persist dispatch intent before sending it, and record the session/operation outcome. If acknowledgement is lost, reconcile or block rather than blindly resend.
2. **Draft PR and CI evidence.** Commit plan/spec documents and open a draft before code implementation, then update that same branch. Reconcile uncertain PR creation using the expected repository/head/base. Gather both check runs and commit statuses, retaining revision, state and source links. Explicitly configure expected checks where forge policy is unavailable.
3. **Review and bounded repair.** Review an immutable revision, combine actionable findings with CI failures, and send a repair brief to the existing implementer. One overall allowance prevents independent loops from multiplying work. Missing review evidence, access failure, cancelled checks and unchanged repeated outcomes must not become a pass.
4. **Ready and recovery.** Revalidate the PR head before readiness, retain evidence/history, and require human merge. Exercise restart, stop and explicit retry before using it to expand Marshal.

The first release supports one project/repository, one host, one work item running at a time and one delivery PR per item. Work-item-to-PR storage should allow later expansion. Only one model worker needs to run at once, but idle sessions/containers may still consume memory: validate parking/releasing their compute before calling this suitable for the 16 GB machine.

Likely code locations:
- New focused factory state/controller/HTTP files under web/bridge/, with matching tests.
- Extend forge.go and forge_github.go for full CI evidence, PR lifecycle and capability limits.
- Small integration points in fleet.go, registry.go/reciperun.go, existing event routes and server wiring.
- A new factory view and API methods in web/ui/src using the existing Svelte stack.
- Keep cmd/webbridge/main.go composition changes small because the daemon work will also touch it.

These locations are candidates, not committed task assignments. Recheck the working tree and daemon contracts before writing exact interfaces.

## CI and validation findings

.github/workflows/go-checks.yml already runs the full Go suite and go vet on PRs and main pushes. AGENTS.md's note that the full suite runs only on release tags is stale. Existing usability CI is additional coverage. The branch response currently reports main as unprotected with no configured required contexts; the factory therefore needs an explicit expected-check policy for its own first repository rather than assuming branch protection defines one.

Relevant existing tests include bridge recipe, review bot, CI fixer, forge, intake, run, transport/container and boundary tests, plus ACP listener/conformance and pipeline controller/runstore tests. Their existence is source evidence only; they were not executed in this assessment.

Before claiming the bootstrap works, prove:
- One real approved plan reaches a draft PR, implementation, review/failure, repair, fresh evidence and ready.
- Pending, missing, failed, cancelled, stale and inaccessible checks never masquerade as a passing result; explicitly define treatment of skipped/neutral results.
- A reviewer looking at an old revision cannot approve newer code.
- Lost dispatch/PR acknowledgements and server restart do not create duplicate work or PRs.
- Stop and human blockers prevent new stages; explicit retries preserve earlier attempts.
- Factory workers and independent automation cannot both mutate the same branch.
- The browser can close/reopen without ending the backend run.
- One chosen OrbStack or Podman deployment can actually launch, observe and recover the relevant ACP sessions. Container/socket path compatibility remains untested.

For self-development, build changes in a separate branch/workspace and deliberately deploy the reviewed version between runs.

## Source anchors

- [Repository instructions](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/AGENTS.md)
- [Daemon architecture](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/docs/daemon/design.md)
- [P3 migration status](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/docs/daemon/p3-acp-transport.md)
- [Current embedded launch](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/internal/launch/launch.go)
- [Forge operations](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/web/bridge/forge.go)
- [GitHub check retrieval](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/web/bridge/forge_github.go#L376)
- [Recipe execution and result parsing](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/web/bridge/reciperun.go)
- [Structured built-in recipes](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/web/bridge/recipes_builtin.go)
- [Bridge run dispatch](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/web/bridge/runs.go)
- [SDD controller](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/internal/pipeline/controller.go)
- [SDD wire options](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/internal/acpwire/pipelines.go)
- [Pipeline checkpoints](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/internal/pipeline/runstore.go)
- [ACP reconnect listener](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/internal/acp/listen.go)
- [Native transport lifecycle](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/web/bridge/transport.go)
- [PR CI workflow](https://github.com/alecpullen/marshal/blob/bff50180cf1e282430d20eeccb0d508850ba88d7/.github/workflows/go-checks.yml)

