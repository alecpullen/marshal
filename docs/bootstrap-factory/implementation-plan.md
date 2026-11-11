# Bootstrap Factory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. The companion design and this plan are approved for implementation. No live deployment is authorised by implementation approval.

**Goal:** Deliver a supervised, durable plan → draft PR → implement → CI/review → repair → ready loop that can build small subsequent Marshal improvements.

**Architecture:** Extend the existing Go bridge with a fixed factory controller and an append-only store. Compose existing ACP sessions and GitHub access through bridge-local adapters; add a focused Svelte view. Do not import the internal pipeline or change the daemon migration.

**Tech Stack:** Go 1.26.1, standard library under web/, existing ACP/GitHub HTTP clients, Svelte 5, TypeScript, Vite, Vitest; cgo and a C compiler for the root Go suite.

**Spec:** [bootstrap-factory-design.md](design.md), especially §§3–7 for counting, evidence and recovery. [Compatibility evidence](compatibility.md) is pinned to bff50180cf1e282430d20eeccb0d508850ba88d7.

Status: approved; implementation underway. File locations below are repository-relative. This bundle contains the approved design, implementation plan and compatibility evidence with local companion links.

## Global constraints

- One configured GitHub repository, one bridge host, one active factory work item, one PR per item; model workers execute serially.
- Five total implementation/review rounds by default, including initial implementation. Explicit additional allowance never resets lifetime history.
- Human merge only; no auto-deployment or automatic cleanup of retained workspaces.
- Ordinary approvals/questions/steering continue to use existing agent channels; v0 has no planning agent.
- Go under web/ is standard-library-only, with no marshal/internal imports and no new Go dependencies.
- Ordinary tests use injected auth, clocks and endpoints; no developer credentials/config, live model calls or runtime access.
- Current daemon work remains independent. No newer capability flag, speculative RPC or unreviewed internal contract change.
- Poll CI every 15 seconds by default; missing/pending checks surface a decision after a configurable 30-minute threshold. Do not turn this threshold into a process timeout.
- UI polling is 2 seconds active, 15 seconds idle; pause while hidden and refresh on return.
- Repository instructions, not this snapshot's line numbers, govern the implementation checkout. Re-read AGENTS.md and current code before edits.

## Review focus

These five failure classes require explicit tests in the owning tasks:

1. Lost responses after an effect actually happened: never repeat a model turn or create a second PR blindly (Tasks 2, 4, 5).
2. A successful RPC carrying cancellation or an old final message: never publish it as a completed implementation (Task 3).
3. Check identity/revision mismatch or partial pagination: never treat incomplete evidence as green (Tasks 2, 4).
4. An idle-looking worker with detached commands, or a detached container still consuming resources: do not dispatch a competing worker (Tasks 3, 5).
5. A second browser command, background bot or external Git update changing state during a transition: preserve ownership, approvals and current evidence (Tasks 1, 4–7).

## File and dependency map

| Task | Files/responsibility | Depends on |
|---|---|---|
| 1 | factory_types.go, factory_store.go and tests: contracts, persistence, command identity | — |
| 2 | factory_forge.go, forge_github_factory.go and tests: complete CI and PR lifecycle | 1 |
| 3 | factory_workers.go, factory_prompts.go, factory_git.go and tests; small Registry/Fleet changes: real agent/checkout operations | 1 |
| 4 | factory_controller.go, factory_effects.go and tests: deterministic sequencing and bounded loop | 1–3 |
| 5 | factory_recovery.go, factory_ownership.go and tests; lifecycle/bot integration | 4 |
| 6 | factory_http.go and tests; HTTP/startup wiring | 5 |
| 7 | factory API/types/routes, three Svelte views and tests | 6 |
| 8 | integration acceptance tests, feature docs and live-run checklist | 7 |

All new Go files above live under web/bridge/. Existing integration files are named in each task. Do not reorganise unrelated large files. Steps within a task are sequential; commit each tested deliverable. Task 2 and 3 can be developed independently once Task 1 interfaces are fixed, but parallel execution is optional.

## Shared contracts (owned by Task 1)

Use exported bridge-local types below with lowerCamelCase JSON keys. These are names and required fields; internal helper layout remains the implementer's choice. All IDs are strings; versions/sequences are uint64; rounds/allowances are int; timestamps are UTC time.Time; routing remains json.RawMessage matching existing bridge routing. Use pointers where absence differs from a zero value.

- FactoryConfig: RepoID, ProjectRoot, HostID, TargetBranch, Workspace string; Native bool; ImplementerRouting, ReviewerRouting json.RawMessage; ReviewerInstructions string; ImplementerAuto bool; ExpectedChecks []FactoryCheckKey; NoRequiredCIAcknowledged bool; MaxRounds int; CIPollSeconds, CIWaitSeconds int. Defaults: MaxRounds=5, CIPollSeconds=15, CIWaitSeconds=1800. Native and Workspace are mutually exclusive. No provider default is invented. Implementer mode is act with approved ImplementerAuto (false unless explicitly selected); reviewer mode is ask. HostID is local in v0. Do not expand tool permissions implicitly.
- FactoryCreate: RequestID, Title, Plan, Spec string; Config FactoryConfig. Nonblank plan/title and known registered repository are required; at least one expected check or explicit no-required-CI acknowledgement is required before approval. Repo/environment/credential capability validation belongs to Task 6.
- FactoryApproval: Revision uint64; Plan, Spec, PlanHash string; Config FactoryConfig; ApprovedAt time.Time. Hash the exact plan/spec bytes with an unambiguous length-delimited SHA-256 encoding.
- FactoryGrant: ID, Reason string; AdditionalRounds int; At time.Time. The first grant equals the initial MaxRounds; later grants append.
- FactoryWorker: AgentID, SessionID, Role, SpawnEffectID string; Parked bool. Roles: implementer, reviewer.
- FactoryPR: Number int; URL, NodeID, HeadRef, BaseRef, HeadSHA, BaseSHA, State string; Draft bool. State is open, closed or merged.
- FactoryCheckKey: Kind, Name string; AppID int64. Kind is check_run or commit_status; AppID is mandatory/nonzero only for check_run.
- FactoryCheck: Key FactoryCheckKey; ID, SHA, Status, Conclusion, URL, LogTail string; ObservedAt time.Time. Bound LogTail to the existing checkLogLimit; observations do not include credential data.
- FactoryCI: HeadSHA string; Checks []FactoryCheck; Complete bool; ObservedAt time.Time. Complete requires successful enumeration of all sources/pages; errors are not converted into an empty complete list.
- FactoryReport: OperationID, Outcome, Summary, Question, Reason, BaseSHA, HeadSHA string; InputsAccessible bool; Findings []ReviewFinding. Outcomes complete or needs_input. A review complete result requires explicit inputsAccessible=true, matching base/head and a non-nil findings list. An implementation complete result requires matching operation identity and a summary; head is independently observed. Decode through a separate wire struct when field-presence validation is needed.
- FactoryRound: Number int; ApprovalRevision uint64; ImplementEffectID string; BaseSHA, HeadSHA, TreeSHA string; CI *FactoryCI; Implementation, Review *FactoryReport; StartedAt, FinishedAt time.Time. Store the accepted stop reason with the corresponding effect outcome.
- FactoryEffect: ID, Kind, Status, WorkerRole string; Round int; ApprovalRevision uint64; Worker *FactoryWorker; ExpectedHead, ExpectedBase string; CreatedAt time.Time. Kinds spawn, seed, publish, create_pr, implement, review, park, resume, cancel, set_ready, set_draft. Status prepared, dispatched, completed or uncertain. Observe/poll reads are not mutating effects.
- FactoryOutcome: EffectID, StopReason, Error, HeadSHA, TreeSHA, BaseSHA string; Worker *FactoryWorker; PR *FactoryPR; Report *FactoryReport; Confirmed bool. Confirmed means the adapter observed the operation's outcome, not merely sent a request. Errors distinguish refused-before-dispatch from uncertain-after-dispatch; use typed error wrappers internally.
- FactoryObservation: Workers []FactoryWorker; RuntimeKnown, TurnIdle, ActiveWorkKnown, WorkIdle bool; HeadSHA, TreeSHA, BaseSHA, RemoteHead string; Dirty bool; PR *FactoryPR; CI *FactoryCI; Error string; ObservedAt time.Time. Worker-specific observations may be collected individually; the aggregate fields describe the current active worker. Unknown is never idle.
- FactoryDecision: ID, Kind, Question, Reason string; EffectID string; CreatedAt time.Time. Kinds question, invalid_result, ci_unavailable, ci_wait, no_progress, exhausted, worker_failed, recovery, conflict.
- FactoryEvent: Sequence uint64; At time.Time; Kind, Message, EffectID string. History is durable and user-facing; do not use it as the sole machine-readable state.
- FactoryItem: ID, Title, State, Stage, RepoID, ProjectRoot, HostID, Branch, BaseSHA string; Version uint64; Draft FactoryCreate; Approvals []FactoryApproval; Grants []FactoryGrant; Rounds []FactoryRound; Workers []FactoryWorker; PRs []FactoryPR; Effects []FactoryEffect; Decision *FactoryDecision; History []FactoryEvent; CreatedAt, ApprovedAt, UpdatedAt time.Time. State values are exactly the design §3 list. The active approval is the last entry; only one unsettled mutating effect may exist per item.
- FactoryCommand: RequestID, Kind, DecisionID, Answer, Reason string; ExpectedVersion uint64; AdditionalRounds int; Replacement *FactoryCreate; ImplementerRouting, ReviewerRouting json.RawMessage; ConfirmPreviousWorkerStopped bool. Kinds approve, answer, stop, retry, reconcile, recover. Replacement ignores its RequestID and is allowed only with retry from confirmed idle; it appends an approval revision and invalidates prior readiness. ConfirmPreviousWorkerStopped is recorded as an operator attestation, never inferred from elapsed time.

Store complete snapshots but keep large tool transcripts in existing agent storage, linked by worker/session. Enforce a 2 MiB request body cap and 1 MiB combined imported plan/spec cap; report validation errors before journal writes. Bound a journal record at 16 MiB and fail safely if exceeded. Evidence stores summaries/log tails, not duplicate full transcripts. No automatic compaction in v0.

## Task 1: Durable items, approvals and command replay

**Files:** Create web/bridge/factory_types.go, factory_store.go, factory_store_test.go. Add docs/bootstrap-factory/design.md, implementation-plan.md and compatibility.md from the approved artifacts as part of this task.

**Interfaces:**
- OpenFactoryStore(dir string) (*FactoryStore, error); (*FactoryStore).Close() error.
- (*FactoryStore).Create(req FactoryCreate, now time.Time) (FactoryItem, error).
- (*FactoryStore).Get(id string) (FactoryItem, error); List() []FactoryItem.
- (*FactoryStore).Update(id, requestID string, expectedVersion uint64, command json.RawMessage, mutate func(*FactoryItem) error, now time.Time) (FactoryItem, error).
- Errors: ErrFactoryConflict, ErrFactoryLocked, ErrFactoryCorrupt, ErrFactoryUnavailable, ErrFactoryNotFound. Return deep copies so callers cannot mutate stored snapshots.
- Update hashes canonical command bytes, checks request replay before version conflict, runs mutate under the store lock without I/O, increments version, appends/fsyncs, then publishes the snapshot. Internal effect transitions use unique persisted effect-derived request IDs too.

- [x] Write TestFactoryStoreReplayAndCommandIdentity: create/approve/close/reopen preserves bytes, approvals and version; identical request returns the original result; altered request contents and stale versions conflict.
- [x] Write TestFactoryStoreInterruptedAppendAndCorruption: incomplete final line is truncated; a complete invalid checksum/JSON/version fails closed; injected write/fsync failure publishes no uncommitted snapshot and prevents further scheduling.
- [x] Write TestFactoryStoreExclusiveOwner: a second open refuses; clean close removes only its own lock; a simulated stale lock does not auto-expire. Also test deep-copy isolation, zero/negative limits, request/record size limits and unchanged state on invalid input.
- [x] Run `go test ./web/bridge -run '^TestFactoryStore' -count=1`; confirm failures describe the missing implementation.
- [x] Implement the contracts, single-owner journal and replay. Records encode a payload plus checksum, using one canonical serialization; reject truncated middle records. Use os.OpenFile O_CREATE|O_EXCL for ownership, and file Sync before acknowledgement. Never delete an existing lock on startup.
- [x] Run `go test -race ./web/bridge -run '^TestFactoryStore' -count=1`; require PASS.
- [x] Commit only this task's tested files: `feat(factory): persist work items and approval history`.

## Task 2: GitHub evidence and draft/ready lifecycle

**Files:** Create web/bridge/factory_forge.go, forge_github_factory.go, forge_github_factory_test.go. Modify forge_github.go only for reusable response data/helpers, preserving existing Forge consumers/tests.

**Interfaces:** Optional FactoryForge interface implemented by *githubForge:
- ObserveFactoryCI(ctx context.Context, repo Repo, sha string, cred Credential) (FactoryCI, error).
- FindFactoryPR(ctx context.Context, repo Repo, head, base string, cred Credential) (*FactoryPR, error).
- GetFactoryPR(ctx context.Context, repo Repo, number int, cred Credential) (FactoryPR, error).
- SetFactoryDraft(ctx context.Context, repo Repo, pr FactoryPR, draft bool, cred Credential) error.
- Existing Forge.CreatePR remains the create primitive; fetch the created PR to populate FactoryPR. Unsupported providers return a capability error; no empty-success stubs.
- EvaluateFactoryCI(ci FactoryCI, expected []FactoryCheckKey, noRequiredCIAcknowledged bool) (state string, actionable []FactoryCheck). States passed, waiting, failed, blocked, unavailable. Non-required skipped/neutral are visible; required success semantics follow spec §6.

- [ ] Write TestFactoryGitHubEvidence with httptest fixtures for paginated check runs and commit statuses, latest attempt selection, same-name different-App checks, pending/missing checks, skipped/neutral/cancelled/timeouts, empty-but-unacknowledged requirements, 403/429/5xx and truncated second pages. Assert no false passing state.
- [ ] Write TestFactoryGitHubPRLifecycle: find exactly one matching head/base across pages, distinguish closed/merged, reject ambiguous matches, create draft payload, fetch node ID and call GraphQL ready/convert-to-draft with explicit errors. Lost create response followed by lookup finds the existing PR; no second create occurs in the controller harness fixture.
- [ ] Run `go test ./web/bridge -run '^TestFactoryGitHub' -count=1`; confirm the new tests fail.
- [ ] Implement the optional capability, using existing URL/auth/client conventions. Follow all pagination; use newest check attempt and newest status per identity, not response order assumptions. Keep timestamps/IDs for evidence. Reuse CheckLog only for failing checks; never download logs for passing checks.
- [ ] Run `go test ./web/bridge -run 'TestFactoryGitHub|TestGitHub|TestForge' -count=1`; require PASS, then `go test ./web/bridge -run TestWebIsStdlibOnly -count=1`.
- [ ] Commit: `feat(factory): observe complete CI and manage draft readiness`.

## Task 3: Reliable workers, seed branch and retained parking

**Files:** Create web/bridge/factory_workers.go, factory_workers_test.go, factory_prompts.go, factory_prompts_test.go, factory_git.go, factory_git_test.go. Modify registry.go, fleet.go, workspace.go and their relevant tests; add only narrow lifecycle helpers in child.go/container.go if necessary.

**Interfaces:**
- Registry.PromptResult(ctx context.Context, id, text string) (PromptResult, error), where PromptResult has StopReason string. Existing Prompt delegates and retains its error-only signature, events and approval/question admission lifecycle.
- ParseFactoryReport(text, operationID, role, baseSHA, headSHA string) (FactoryReport, error); BuildFactoryPrompt(item FactoryItem, effect FactoryEffect) (string, error).
- Fleet.SpawnFactoryWorker(ctx context.Context, item FactoryItem, role, effectID string) (FactoryWorker, error); Fleet.ParkFactoryWorker(ctx context.Context, worker FactoryWorker) error.
- (*factoryWorkers).Execute(ctx context.Context, item FactoryItem, effect FactoryEffect) (FactoryOutcome, error); Observe(ctx context.Context, item FactoryItem) (FactoryObservation, error). Construct with newFactoryWorkers(fleet *Fleet).
- (*factoryGit).Seed(ctx context.Context, item FactoryItem, worker FactoryWorker) (head, tree string, err error); Publish(ctx context.Context, item FactoryItem, worker FactoryWorker, expectedRemote string) (head, tree string, err error). Construct with newFactoryGit(fleet *Fleet). Use Fleet commitSession and hardened git primitives.
- Add optional Agent.FactoryItemID, FactoryRole, FactorySpawnEffectID string and FactoryParked bool. Add a private tagged spawn path that assigns/persists metadata before starting the runtime; public Spawn behaviour stays unchanged. Factory workers do not use warm pools.

- [ ] Write TestFactoryPromptCompletion: end_turn plus matching new complete report succeeds; cancelled/max-token/unknown reasons, previous-turn JSON, wrong operation ID, missing summary, malformed review, inaccessible inputs and needs_input never count as a completed implementation. Verify existing Prompt callers and permission/question handling remain unchanged.
- [ ] Write TestFactorySeedAndPublish with disposable Git repositories: clean base creates one deterministic branch and committed docs, preserves exact plan bytes, pushes a new branch without force, rejects dirty initial work, unsafe paths and unexpected/divergent remote changes. An uncertain push is resolved by reading its exact expected remote SHA, not repeating model work. The user-supplied plan is data, never shell code.
- [ ] Write TestFactoryParkRetainsWorkspace with fake native/container transports: confirmed idle leads to disabled supervision and stopped runtime, verified exit precedes slot release, workspace/state survive, Resume loads the saved session, unknown/detached active work refuses parking, and a stop failure cannot free capacity. ReattachAll handling is completed in Task 5.
- [ ] Run `go test ./web/bridge -run '^TestFactory(Prompt|Seed|Park)' -count=1`; confirm failure.
- [ ] Implement worker tagging, prompt/result validation and per-role route forwarding and the approved act/ask/auto tuple. Keep the successful response and final report together; never infer completion from nil error alone. Reviewer checkout is pinned to the candidate SHA, reads base/head/spec and is checked for unexpected writes.
- [ ] Implement seed/publication using the existing bridge-visible checkout mapping. Verify clean initial state, create marshal/factory/<item-id>, safely write docs/marshal-factory/<item-id>/plan.md and spec.md, commit through ACP, then inspect Git. Publication never force-pushes; bridge verifies actual HEAD and recorded branch ownership. Seed does not run the late Exit gate.
- [ ] Implement factory-specific parking: do not call destructive stopAgent or container-detaching Pause. Stop only confirmed-idle factory-owned compute, preserve directories/mounted state, confirm process/container termination, and remove runtime bookkeeping/release its slot only afterward. Use existing Resume for the retained workspace; fail explicitly if saved session cannot load.
- [ ] Run `go test -race ./web/bridge -run 'TestFactory(Prompt|Seed|Park)|TestRegistry|TestTransport|TestContainer' -count=1`; require PASS and no regressions in existing lifecycle tests.
- [ ] Commit: `feat(factory): execute validated worker turns in retained workspaces`.

## Task 4: Fixed loop and durable effect dispatch

**Files:** Create web/bridge/factory_controller.go, factory_effects.go, factory_controller_test.go, factory_effects_test.go.

**Interfaces:**
- FactoryEffects interface: Execute(context.Context, FactoryItem, FactoryEffect) (FactoryOutcome, error); Observe(context.Context, FactoryItem) (FactoryObservation, error).
- NewFactoryEffects(fleet *Fleet) FactoryEffects composes Task 2 forge capability and Task 3 workers/git.
- AdvanceFactory(item FactoryItem, observed FactoryObservation, now time.Time) (FactoryItem, *FactoryEffect, error): pure transition logic; returns at most one new effect and never performs I/O.
- CompleteFactoryEffect(item FactoryItem, effectID string, outcome FactoryOutcome, now time.Time) (FactoryItem, error): accepts only the matching unsettled effect.
- NewFactoryController(store *FactoryStore, effects FactoryEffects, now func() time.Time) *FactoryController; Run(ctx context.Context) error; Command(ctx context.Context, itemID string, cmd FactoryCommand) (FactoryItem, error); Wake(). Run owns backend lifecycle, not an HTTP request.
- NewFactoryController does not schedule until recovery in Task 5 completes. Its loop persists prepared/dispatched/completed or uncertain states, and reevaluates from durable data. Inject clock/timer scheduling through an internal test seam rather than sleeping in tests.

- [ ] Write TestFactoryLoopHappyAndRepair: approve queues; seed/draft occurs before implementation; round 1 produces one published SHA; CI plus reviewer findings produce one combined repair; round 2 passes; PR ready is issued only with matching fresh base/head evidence. Verify every effect has durable intent before the fake sees it.
- [ ] Write TestFactoryLoopLimitsAndNoProgress: five total rounds dispatch at most five work-changing prompts; a sixth requires an explicit positive grant; unchanged-tree repair stops immediately; failures/invalid reports stop, and lifetime numbering survives retry. A changed-tree failure may continue to the cap without claiming semantic progress.
- [ ] Write TestFactoryLoopEvidenceAndRevision: incomplete CI/old review/missing success fields cannot pass; current failed CI is diagnosed, access/timeouts request input, cancelled implementation never publishes; base/head change revokes review. Simulate head movement between ready precheck and response; require compensating draft mutation and visible uncertainty on compensation failure.
- [ ] Write TestFactoryLoopCommands: FIFO items, one active model, explicit approve, stop-before-dispatch, replayed callbacks ignored, stale browser conflicts, an in-flight tool answer consumes no extra round; an answer to a final needs_input report consumes the next round and waits for a grant if exhausted; stale decision IDs cannot settle new questions; authorised continuation needs no separate start, replacement plan/reviewer instructions append approval history only when idle and invalidate evidence. Evidence-only refresh does not consume a round.
- [ ] Run `go test ./web/bridge -run '^TestFactoryLoop' -count=1`; confirm failures.
- [ ] Implement pure transitions and the sequential scheduler. Prepare spawn/seed/publish/create/review/park/resume separately; never hide model retries inside adapters. Read polls respect Retry-After/rate-limit delays when present, otherwise configured interval. A decision pauses affected work; release the factory slot only after confirmed idle.
- [ ] Implement effect dispatch through the capability adapter. Factory reviews do not post public review comments automatically. Prompt ordinary failed-check diagnosis with structured not_reproducible/infrastructure_suspected escalation. Ready PR monitoring continues while the service runs and refreshes after reconnect.
- [ ] Run `go test -race ./web/bridge -run '^TestFactory(Loop|Effect)' -count=1`; require PASS.
- [ ] Commit: `feat(factory): orchestrate bounded implementation review and repair`.

## Task 5: Recovery, stop confirmation and ownership guards

**Files:** Create web/bridge/factory_recovery.go, factory_recovery_test.go, factory_ownership.go, factory_ownership_test.go. Modify fleet.go, automation.go, reviewbot.go, cifixer.go, schedules.go, runs.go and relevant session/exit/discard entry points in http.go; retain small guards at their shared service entry points so API/MCP/internal callers cannot bypass them.

**Interfaces:**
- (*FactoryController).Recover(ctx context.Context) error: inspect all unfinished items before Run admits effects.
- (*FactoryController).OwnsAgent(agentID string) (itemID string, active bool); OwnsBranch(repoID, branch string) bool.
- (*Fleet).checkFactoryAction(agentID, action string) error: conflict for independent stage dispatch/merge/exit/discard/delete on active or uncertain factory workers; allow ordinary steering, request answers, inspection and the controller's own operations through an explicit internal ownership token, never a user-supplied bypass field.
- (*FactoryController).Command handles reconcile/recover with observed termination or the spec's recorded explicit operator attestation. Attestation must name the known previous worker; it does not resolve an unidentifiable spawn by itself.

- [ ] Write TestFactoryRecoveryLostOutcomes: crash after persisted spawn/prompt/push/create intent but before outcome; exact tagged worker and remote PR are found; unknown prompt is not resent; duplicate live workers or unknown ownership produce recovering. Confirmed completed observations may reconcile only with the appropriate report/stop-reason evidence.
- [ ] Write TestFactoryRecoveryParkedAndStopped: startup does not restart parked/history workers; stop stays stopping until active/detached work is resolved; reconnect reattaches without replaying a prompt; stale journal lock requires explicit operator procedure. A native child lost with the bridge is an interrupted operation, not success.
- [ ] Write TestFactoryOwnership: CI/review bots and scheduled branch mutation skip factory branches; direct exit/merge/delete/new-run paths conflict; steering and existing answers still work; external branch advance is detected before publication and cannot be force-overwritten. Terminal workspace cleanup retains history and makes absent files explicit.
- [ ] Run `go test ./web/bridge -run '^TestFactory(Recovery|Ownership)' -count=1`; confirm failures.
- [ ] Implement reconciliation and guards. Persist factory ownership before dispatch. ReattachAll skips factory-tagged agents and delegates their lifecycle to Recover; preserve ordinary-agent behaviour. Recover runs before enabling bots/schedules for factory-owned branches. Stop/recovery commands never treat time passage as permission or proof of termination.
- [ ] Run `go test -race ./web/bridge -run 'TestFactory(Recovery|Ownership)|TestReviewBot|TestCIFixer|TestSchedule|TestReattach' -count=1`; require PASS.
- [ ] Commit: `feat(factory): reconcile interrupted work and guard branch ownership`.

## Task 6: Authenticated API and service lifecycle

**Files:** Create web/bridge/factory_http.go, factory_http_test.go. Modify http.go and cmd/webbridge/main.go only at construction/startup/shutdown seams; create cmd/webbridge/factory_test.go for startup ordering tests. Do not invent a second server binary.

**Interfaces:**
- FactoryAPI interface: Create(FactoryCreate) (FactoryItem,error); Get(string) (FactoryItem,error); List() []FactoryItem; Command(context.Context,string,FactoryCommand) (FactoryItem,error).
- NewFactoryAPI(store *FactoryStore, controller *FactoryController, fleet *Fleet) FactoryAPI performs preflight capability/config validation.
- (*Server).factoryRoutes(api FactoryAPI) registers GET/POST /api/factory/items, GET /api/factory/items/{id}, POST /api/factory/items/{id}/commands behind existing auth.
- List returns {items:[...]}; item/create/command return {item:...}. POST create returns 201 for first creation and 200 on replay; accepted commands return 200 with the durable current state, not a promise the worker finished.
- Error JSON {error, code, itemId?, currentVersion?}; 400 invalid input, 404 absent item, 409 version/ownership/replay conflict, 422 unsupported capability or configuration, 503 unavailable store/unknown required runtime state. Request IDs are required on all mutations.

- [ ] Write TestFactoryHTTP for auth, missing request identity, body limits, expected-version conflict, duplicate create, invalid repo/model/environment/CI configuration, unsupported forge, plan-preview/approval distinction, explicit auto-permission selection and absence of secret fields in responses. Assert closing an HTTP request does not stop accepted work.
- [ ] Write TestFactoryStartupOrdering: open store/ownership, construct controller, reconcile factory records, then run scheduler; parked factory workers bypass generic reattach. Journal/lock failure exposes factory unavailable without silently starting jobs. Shutdown stops new dispatch and persists uncertainty before closing the journal; it does not falsely label detached agents as stopped.
- [ ] Run `go test ./web/bridge ./cmd/webbridge -run 'TestFactory(HTTP|Startup)' -count=1`; confirm failure.
- [ ] Implement API validation and lifecycle wiring. Validate routes using existing provider/model discovery/config paths, and validate environment readiness through existing workspace helpers. Keep credentials behind existing references. Do not broaden auth, bind addresses or daemon flags.
- [ ] Run `go test ./web/bridge ./cmd/webbridge -count=1`; require PASS, including existing boundary checks.
- [ ] Commit: `feat(factory): expose work item commands through the bridge API`.

## Task 7: Minimal responsive factory UI

**Files:** Create web/ui/src/lib/factory.ts, factory.test.ts, web/ui/src/views/Factory.svelte, FactoryNew.svelte, FactoryItem.svelte and matching *.test.ts. Modify web/ui/src/lib/api.ts, api.test.ts, routes.ts, routes.test.ts, web/ui/src/App.svelte and web/ui/src/views/Project.svelte. Reuse existing controls/styles and agent/PR navigation; no design-system rewrite.

**Interfaces:**
- factory.ts exports TypeScript interfaces matching the Go JSON fields, FactoryRoute = {itemId?: string; create?: boolean}, and the polling helper createFactoryPoller(load:()=>Promise<void>, active:()=>boolean, onError:(error:unknown)=>void): {start():void; stop():void}. Inject timers/document visibility for tests through an optional internal seam.
- api.ts exports listFactoryItems(), getFactoryItem(id), createFactoryItem(req), commandFactoryItem(id,cmd), using its existing authenticated request helper. Retain requestId across a network retry; generate a new ID for an intentional new command.
- routes.ts exports parseFactoryRoute(hash:string):FactoryRoute|null and formatFactoryRoute(route:FactoryRoute):string. Routes #factory, #factory/new, #factory/<encoded-id>; reserve new before generic ID parsing. Extend the existing pageFromHash page union and App dispatch to recognise factory as well as its subroutes.
- Factory.svelte lists items; FactoryNew.svelte creates drafts and reviews approval; FactoryItem.svelte shows state/evidence/history and commands. Project.svelte links to the factory with repository context. Filter/seed repo through UI state or explicit query parsing, never an undocumented route variant.

- [ ] Write route/API tests for encoded IDs, invalid paths, auth rejection, replayed request IDs, stale-version response and safe errors.
- [ ] Write component tests for draft-before-start, CI failures above report, compact passes, empty-vs-unavailable evidence, question answer, stopping-unconfirmed, exhausted retry grant, explicit replacement plan approval, wrong-version refresh, ordinary agent drill-in and retained draft input after network error.
- [ ] Write polling tests using fake timers: 2s active/15s idle, no concurrent requests, error backoff, hidden-page suspension, immediate foreground refresh and unmount cleanup.
- [ ] Run from web/ui: `npm test -- src/lib/factory.test.ts src/lib/routes.test.ts src/lib/api.test.ts src/views/Factory.test.ts src/views/FactoryNew.test.ts src/views/FactoryItem.test.ts`; confirm the new tests fail for missing behaviour.
- [ ] Implement the three views using existing accessible controls. Retry offers additional rounds, optional route changes and an expandable replacement plan/reviewer-instructions section. Recovery attestation is a deliberate named-worker action with explanatory copy; it is never preselected. Keep destructive existing actions blocked by the backend.
- [ ] Run the same named UI tests and `npm run build`; require PASS and successful embedded asset output.
- [ ] Inspect browser at 390px and 1440px using mocked local API data: no horizontal overflow, keyboard-reachable controls, visible failures/decisions, code/log panels scroll within their containers. Capture proof for implementation verification; these are not a new mockup approval round.
- [ ] Commit the UI and the repository's expected generated assets: `feat(web): operate the bootstrap factory on desktop and mobile`.

## Task 8: End-to-end proof and first self-development run

**Files:** Create web/bridge/factory_integration_test.go and docs/bootstrap-factory/acceptance.md. Update docs/bootstrap-factory/design.md and implementation-plan.md to match final implementation, and cmd/webbridge/README.md with factory operation/recovery instructions. Extend existing e2e tooling only if its current harness materially simplifies the browser acceptance flow.

- [ ] Write TestFactoryEndToEnd using a disposable Git remote, fake ACP worker server and httptest GitHub: approved docs seed → draft → implementation → failed check and review → repair → fresh checks/review → ready. Assert one PR, sequential model workers, exact evidence SHAs, durable round history and human merge untouched.
- [ ] Write TestFactoryEndToEndInterrupted at each effect boundary: replay never blindly duplicates dispatch; independent bot claims cannot mutate the branch; cancelled turn with nil RPC error never publishes; ready race compensates or displays uncertainty; journal failure prevents subsequent effects.
- [ ] Run `go test ./web/bridge -run '^TestFactoryEndToEnd' -count=1`; verify failures before completing missing integration wiring.
- [ ] Complete integration wiring and run the same tests until PASS. Fix integration defects in their owning units without broad refactors.
- [ ] Run final applicable checks once: `CGO_ENABLED=1 go test ./... -count=1 -timeout 5m`; `CGO_ENABLED=1 go vet ./...`; `go test -race ./web/bridge ./cmd/webbridge -count=1`; from web/ui, `npm test` and `npm run build`; then `CGO_ENABLED=1 go build ./cmd/webbridge`. Report unrelated baseline failures separately; do not claim overall PASS when they remain.
- [ ] Document the explicit stale-lock procedure, unknown-dispatch recovery, expected CI identity configuration, five-round counting, changed-model fresh summary, native-vs-container restart limits and how to keep the running controller stable during self-development.
- [ ] Prepare the opt-in live acceptance run with the user's configured repo, provider and runtime. Use a small approved feature branch/PR; obtain any missing concrete deployment credentials/target details before live writes. Never use ambient credentials in ordinary tests. Prove one repair, a human question/stop, browser reopen, and service restart reconciliation. Capture run ID, PR URL, commit SHAs, checks and outcomes in acceptance.md without secrets.
- [ ] Validate OrbStack on the user's ARM64 deployment before claiming that deployment supported: correct bridge/agent workspace paths, idle worker actually stopped, saved session and checkout resume, and no interference with unrelated containers. If hardware/runtime access is unavailable, mark this gate pending and hand over reproducible commands; do not substitute fake-container tests as proof. Record Podman separately when tested.
- [ ] Commit validated code/docs: `test(factory): prove the delivery loop and document recovery`.

## Acceptance boundary and execution handoff

Tasks 1–7 provide the implementation; Task 8 proves it. Automated integration passing is not equivalent to a validated live model/runtime deployment. A blocked live gate does not prevent code review, but must remain explicit before the user relies on the factory.

Recommended execution method: subagent-driven, with task-sized implementation/review gates, because dispatch/recovery, forge evidence and runtime lifecycle have distinct failure modes. Native execution is also available if preferred. Do not spawn implementation agents or begin product edits merely because this plan exists.

The first factory-built change should be a small approved UI/reporting improvement. Keep the stable controller running, review the resulting PR, merge deliberately and deploy between runs. A more general workflow editor, multi-host placement or SDD execution adapter becomes a later factory work item.

## Plan self-review

Coverage: design §§1–2 → Tasks 3/6/7 and global constraints; §§3–4 → Tasks 1/4; §5 → Task 3; §6 → Tasks 2/4; §7 → Task 5; §8 → Tasks 6/7; §9 → Task 8. All five review-focus classes have named tests. Shared types and method names above are the contract between tasks; existing concrete helper signatures must be checked against the implementation checkout before integration edits. No product tests have been run during plan drafting.
