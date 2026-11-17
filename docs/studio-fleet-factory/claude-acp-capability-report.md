# Claude ACP capability report

**Status:** deterministic proof complete; live Claude proof not run.
**Date:** 2026-11-15 (task 7 of [the planner plan](plans/2026-11-15-claude-acp-planner-plan.md)).
**Design:** [Claude planning in Studio and the worker boundary](claude-acp-worker-design.md).

## What this report is

The planner and the factory-role seam are proven against a fake ACP adapter:
`TestACPWorkerIntegration` (`web/bridge/worker_acp_integration_test.go`) drives
the real `PlannerService`, the real routes, the real snapshot store, the real
factory store and seed, and the real `acpWorkerBackend` over the scripted
in-process agent already used by `worker_acp_test.go`.

**No adapter binary was installed in this environment and no provider was
contacted.** Therefore:

- The **adapter version is `untested`**. No `claude-code-acp` (or any other
  ACP adapter) executable was available, so its version string, its negotiated
  capability set and its mode names were never observed.
- Every row that needs a real adapter, a real Claude credential or a real host
  policy is marked `untested` with the exact configuration that would be
  required. None of those rows is inferred from the fake, and nothing in this
  document claims observed Claude behaviour.

A `pass` below means the **bridge-side contract** is pinned by a test: the
frames the adapter receives, what the planner records, and what the factory
does with the result. A `fail` means the seam does not do what the requirement
needs. `untested` means nothing here can answer it.

## Environment

| Item | Value |
|---|---|
| Adapter binary | none installed (`MARSHAL_CLAUDE_ACP_ADAPTER` unset) |
| Adapter version | `untested` |
| Claude credential | none present, none used |
| Proof harness | in-process scripted ACP agent over `io.Pipe`, no subprocess, no network |
| Host platform | Linux (this developer machine); the platform matrix is untested |
| Determinism | every wait is bounded and condition-based; no live provider, no sleep-based ordering |

## Planner requirements

| Requirement | Result | Evidence | Why untested / notes |
|---|---|---|---|
| ACP `initialize` and capability negotiation | pass | `TestACPWorkerIntegration` (`assertSessionStart`), `TestACPWorkerHandshakeAndMode` | initialize is sent once per adapter generation with `protocolVersion: 1` and empty `clientCapabilities`; a capability the adapter omits is false, never assumed |
| `session/new` with an absolute `cwd` and empty `mcpServers` | pass | `TestACPWorkerIntegration`, `TestACPWorkerHandshakeAndMode` | cwd is the planner's own workspace; `mcpServers` is always sent so an adapter default cannot attach tools the planner did not configure |
| Mode list reported and `session/set_mode` confirmed | pass | `TestACPWorkerIntegration` (both `bypassPermissions` and `default`), `TestACPWorkerHandshakeAndMode`, `TestACPWorkerModeUnavailable`, `TestPlannerModeNotConfirmedBlocksPrompt` | an unavailable or refused mode is reported by name and no prompt is dispatched; a mode the session did not confirm blocks prompts |
| Permission requests routed to the browser and answered once | pass | `TestACPWorkerIntegration` (request `tc-perm`), `TestACPWorkerPermissionAndQuestion`, `TestPlannerRequestSettlement` | the adapter's tool-call id is the stable request id; a second answer is a conflict |
| Elicitations / user questions routed and answered once | pass | `TestACPWorkerIntegration` (request `q-1`), `TestACPWorkerPermissionAndQuestion` | same sink, verbatim answer, one settlement |
| Streaming assistant text | pass | role exercises in `TestACPWorkerIntegration` (`agent_message_chunk` → `turn.text`), `TestACPWorkerSessionsDoNotCrossRoute` | text is per session, ordered, and truncated with a marker at the event bound |
| Tool activity updates | untested | none | the backend journals a `tool_call`-style update as `turn.tool` (`worker_acp.go`), but no test pins a tool-call stream and no real adapter was available to produce one |
| Cancel | pass | `TestACPWorkerPromptCancel`, `TestACPWorkerIntegration` ("a cancelled turn is not a role report") | cancel is the protocol notification for the turn in flight; the turn's own `cancelled` stop reason is what is reported |
| Close and request settlement | pass | `TestACPWorkerCloseSettlesRequests`, `TestACPWorkerPromptCancel` | an unanswered request settles exactly once when the planner ends; the adapter process stops with its last session |
| Transport loss yields an unknown outcome, never a replay | pass | `TestACPWorkerTransportLossIsUncertain`, `TestACPWorkerNoAutomaticTurnReplay`, `TestACPWorkerIntegration` ("turn-lost") | a dispatched turn the backend cannot observe is `ErrWorkerUncertain`; the planner records `unknown` and refuses to resend the same request id |
| Session load / resume | untested | `TestPlannerSeparateWorkspaceDefault` (resume withheld with a reason) | the bridge negotiates `loadSession` but performs no `session/load`; resume is reported unavailable rather than offered, so a real load is unproven |
| Workspace edits performed by the adapter | untested | none | the fake adapter advertises no edits and the planner records `workspaceEdits: false`; the snapshot capture is bridge-side, not an adapter capability |
| Permissive and restricted **actual** actions on a real host | untested | none | requires a configured disposable project, an installed adapter path and provider credentials; the fake confirms the mode only |
| Real Claude authentication and credential separation | untested | none | requires an installed adapter and explicitly provisioned API-key authentication on the execution host; no credential is copied to the browser and no subscription path is claimed |
| Installation and version diagnostics | fail (partial) | `TestACPWorkerIntegration` ("adapter diagnostics refuse instead of substituting"), `TestPlannerSeparateWorkspaceDefault` | **fails closed**: an unset or relative `MARSHAL_CLAUDE_ACP_ADAPTER` refuses to create a planner, with no fallback binary and no download. There is **no version probe or install diagnostic**, and the adapter version could not be recorded |
| Secret non-leakage through the bridge's own state | pass | `TestPlannerEventsReplayAndBounded` (journal scanned for `MARSHAL_`, `API_KEY`, `TOKEN`, `PASSWORD`), `TestACPWorkerIntegration` (review carries no host path) | the planner record and journal hold no prompt text, environment or credential; what an adapter process does with its own environment is untested |

## Factory-role requirements

The exercises run a worker session over the same `WorkerBackend` seam in a
disposable `git init` repository and parse the result with the factory's
existing parser (`ParseFactoryReport`). They do not register a production
Claude factory backend, and they do not run implementer or reviewer turns
against a real provider.

| Requirement | Result | Evidence | Why untested / notes |
|---|---|---|---|
| Implementer role report accepted | pass | `TestACPWorkerIntegration` ("implementer report is accepted at the expected revision") | a complete report with the narration the factory prompt requires parses for the effect's operation id |
| Reviewer role verdict accepted | pass | `TestACPWorkerIntegration` ("reviewer verdict is accepted for the revision under review") | a verdict is accepted only with accessible inputs, the expected base and head, and valid findings |
| Correct checkout revision | pass | `TestACPWorkerIntegration` (role fixtures), `TestFactorySnapshotBaseConflict`, `TestFactorySnapshotSeedSelectedOnly` | the role fixture asserts the checkout still sits at the approved base; a seed refuses a base the selection was not taken against |
| Bad report rejected | pass | `TestACPWorkerIntegration` ("a bad or missing report is rejected"), `TestFactoryPromptCompletion` | a complete implementer report without a narration, or a report for another operation id, is refused before it reaches the store |
| Missing report rejected | pass | `TestACPWorkerIntegration` ("a bad or missing report is rejected") | a turn that ends `end_turn` with no final JSON block is refused as a missing report |
| Cancellation is not a completed role turn | pass | `TestACPWorkerIntegration` ("a cancelled turn is not a role report") | the adapter's `cancelled` stop reason is reported as such; the factory worker path requires `end_turn` before it reads a report |
| Lost acknowledgement is uncertain and not replayed | pass | `TestACPWorkerIntegration` ("a lost acknowledgement is uncertain and not replayed"), `TestACPWorkerTransportLossIsUncertain` | the turn is `ErrWorkerUncertain` with no stop reason and exactly one dispatch |
| Factory recovery after a lost acknowledgement | pass | `TestFactoryRecoveryLostOutcomes`, `TestFactorySnapshotInterruptedSeed`, `TestFactoryRecoveryUnknownPromptRemainsBlocked` | the effect journal records the outcome as uncertain and reconciliation names the cause; a blind reapply is refused |
| Real implementer and reviewer execution against Claude | untested | none | requires a configured disposable project, an installed adapter path and provider credentials; nothing here ran a real Claude turn |

## Direct SDK comparison

This compares only gaps the ACP path actually has. It is **not** a proposal: no
SDK backend is implemented, and no decision to add one is implied. The user
decides whether a material gap justifies the work.

| Actual ACP gap | What a direct SDK backend would offer | Cost and terms caveat |
|---|---|---|
| No installation or version diagnostic; the adapter path is an environment variable and an unset/relative value refuses the planner | the SDK is a library in this process, so there is no separate adapter to locate or version; the bridge would link it and report its own build | the SDK brings its own supported-platform, release-cadence and credential contract into the bridge, and a library upgrade becomes a Marshal release rather than a user-managed binary |
| Session resume is not performed; the planner reports resume unavailable | the SDK documents session continuity as its own API surface, which a process-local backend could drive directly | adopting it would tie planner durability to a provider-specific state model and needs its own proof; no portability across ACP agents would remain |
| Tool activity reaches the browser as an opaque `turn.tool` payload | the SDK exposes typed tool use and results, which a native backend could project into `viewmodel` rows | the projection is new work, and the same typing is achievable over ACP by modelling the update kinds the adapter actually emits |
| No proof of permissive versus restricted behaviour on a real host | not a gap the SDK closes: managed policy and host permissions are the provider's, not the transport's | any claim of unrestricted local access would be false either way; the effective mode must be displayed, not the requested one |
| No authentication surface in the bridge; credentials are the adapter's environment | the SDK has a documented authentication story, including restrictions on third-party offerings of claude.ai login or subscription limits without prior approval | those terms apply to what Marshal may ship; they must be read and accepted before any SDK authentication path is offered, and they do not disappear by choosing a library |

Nothing in this comparison changes what the shipped planner does: the ACP
adapter is configured by absolute path, capabilities are gated on what the
adapter advertises, and a mode is confirmed before a prompt is dispatched.

## What is required to complete the untested rows

Run these only on an explicitly configured disposable project with provider
credentials you are willing to spend, and never in the default test suite:

1. Install the standalone Claude ACP adapter and its paired Claude executable
   on the execution host; record the adapter version.
2. Point `MARSHAL_CLAUDE_ACP_ADAPTER` at its absolute path on a throwaway
   project, create a planner in each mode, and confirm the effective mode the
   adapter reports.
3. Exercise a real permission-requiring action and a real question in both
   modes; record what the host policy actually allowed.
4. Run one real implementer turn and one real reviewer turn on a disposable
   repository, and confirm the report, the revision evidence and that nothing
   was pushed or merged.
5. Kill the adapter mid-turn and confirm the planner reports `unknown` and does
   not resent the prompt.
6. Record the platform(s) this was repeated on; the platform matrix is empty
   today.

## Result

The bridge-side contract is proven and reproducible: session setup, both
access modes, request routing, disconnect survival, unknown-on-process-loss,
snapshot review, factory draft and selected-edit seed, and the factory role
report contract. Whether ACP with a real Claude adapter meets factory-role needs
remains **untested**, and no production Claude worker may be claimed on the
strength of this report.
