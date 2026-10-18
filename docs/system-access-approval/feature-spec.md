# Clear, session-scoped access approvals

Status: approved design; implemented.
Date: 2026-10-18.
Source baseline: `de5e47bdc5e695d4e422e99059d27354a84dce14`.
Implementation: complete — see [Implementation status](#implementation-status).

## Context and scope

The user confirmed that latency issues are resolved and test isolation is complete. Background execution work is still in progress. The next selected delivery is system-access approvals; Send now remains separate.

The observed defect is that requesting system access opens the editing-mode escalation popup again. This delivery makes the requested capabilities clear and fixes grant/result ordering. It extends the existing pending-approval, runtime and dock flows; it is not a general permissions framework.

Included:

- Distinct system-only, editing-only and combined presentations.
- Arrival without a keyboard nudge, plus correct handling while another panel is open.
- One shared runtime resolution path for TUI and ACP.
- Session-scoped grants applied before the blocked tool resumes.
- Correct denial, cancellation, stale-choice and already-granted results.
- Preservation of ongoing jobs and the background execution ownership contract.

Excluded:

- Send now or operation interruption; task cards, watchers and progress widgets.
- Latency tuning, test-isolation redesign or reopening the completed work.
- A sudo/password flow, persistent system access, or new background-job lifetime semantics.
- Arbitrary per-file/per-command capability grants, a general approval queue, or redesigning ordinary tool approvals.
- A browser approval redesign; ACP contract additions remain additive and `web/` stays an external stdlib-only client.

## Evidence

The historical investigation and report analysis remain in `.docs-archive/triage/2026-10-15-testing-observations.md` and `.docs-archive/triage/2026-10-16-postmortem-evidence.md`.

Current source confirms:

1. `modeRequestTool` in `internal/tools/native/mode_request.go` requires a mode even for system access, posts a generic `PendingToolCall` and reports switching modes on any approved system request. Denial incorrectly says it stayed in Default even when another mode was active.
2. `handleApproval` in `internal/app/tui/model.go` opens “Elevate to editing mode” with Edit/Copilot/Auto choices for system requests. Request scope is buried in a subtitle.
3. The mode-elevation picked handler calls `Respond` before `setMode`/`SetSystemAccess` and then `persistAndReload`. That path can race the resumed tool and rebuild unrelated runtime services.
4. Runtime messages return before the key/input approval-routing path. `handleAgentTick` does not reconcile the missing access panel. Existing system-access tests deliberately inject a keypress before checking it.
5. `PermissionBridge.Request` in `internal/acp/permissions.go` sends to the tool's response channel before returning. The mode/system setters in `TurnManager.runTurn` run afterward. This shares the ordering defect with the TUI.
6. Ordinary manual mode changes are already described as session-scoped by `setMode`; `State.SystemAccess` is an in-memory session flag.

A temporary Go-overlay probe on this baseline reproduced the keyboard-nudge problem and generic editing title at 80×24, 120×40 and 200×60. The existing focused native/TUI/ACP mode/system/permission tests pass, showing that their current assertions do not catch this behavior. The probe changes no repository source.

A targeted keyword scan of semantic observations in the eight latest non-archived global postmortems added no matching system-access/popup observation. This does not negate the user's report or the direct reproduction; the earlier report supplement's missing-capability evidence remains distinct from the UI defect.

## Approach

Two viable implementations were considered:

- Extend the existing filtered picker with conditional titles/actions and the shared resolver. This reduces component work, but its search input and footer are awkward places to disclose current authority, requested changes and a reason.
- Use a small dedicated access panel inside the existing dock, backed by the same shared resolver. This gives the request a stable summary and explicit choices without replacing the dock or ordinary approval UI.

Recommend the dedicated panel. A broader permissions framework would add scope without helping this defect.

## Request semantics

Keep the existing tool name `mode.request` and its explicit-user-decision behavior, including in Auto mode. A tool's `RiskReadOnly` classification does not authorize granting capabilities automatically.

Arguments:

| Field | Contract |
| --- | --- |
| `mode` | Optional editing intent/hint: `edit`, `copilot` or `auto`. It asks for editing capability; it never forces a particular interaction variant. |
| `system` | Optional boolean, default false; asks for system access for the current conversation. |
| `reason` | Optional explanation, at most 400 Unicode characters. Supply a neutral fallback when absent. |

Require either a valid mode or `system:true`. Reject an empty/unknown supplied mode, a request with neither capability, malformed arguments and an overlong reason before publishing a pending request. Retain compatibility with existing valid `{"mode":"edit","system":true}` calls.

Compute missing capabilities from the runtime's effective mode and system flag:

| Current authority and request | Decision shown |
| --- | --- |
| Edit/Copilot/Auto, system missing, requests system with or without a mode hint | System only; keep the current mode. |
| Plan/Default, mode requested, no missing system grant | Editing only; user chooses Edit/Copilot/Auto. |
| Plan/Default, both editing and system requested/missing | Combined editing and system grant. |
| Plan/Default, `system:true` with no mode | System only; retain the read-only interaction mode. |
| Editing requested but already enabled; no other missing capability | Return “already available” with effective state; no popup or new grant. |
| System requested but already granted; no other missing capability | Same already-available result; no popup. |

When only editing is missing and system access was previously granted, retain it. Denying a new request never revokes a previous grant.

This flow belongs to the active top-level conversation. A subagent cannot use it to grant itself or its parent additional authority: reject child `mode.request` before publishing a pending approval, with an instruction to ask the parent. Ordinary child tool approvals remain unchanged. Existing child authority inheritance and launch-time settings are not broadened.

## Presentation

Use a dedicated `accesspanel` implementing the existing `dock.Panel` interface. Its input is an immutable presentation snapshot; it contains no policy or runtime mutation logic.

Display:

- A capability-specific title: “Allow system access?”, “Allow editing?” or “Allow editing and system access?”
- Requesting actor/step when available, and a bounded reason.
- Current mode and system-access state.
- The requested changes, including whether the interaction mode stays the same.
- Session lifetime and the scope of system access: filesystem access outside the workspace and relaxed workspace shell guardrails, subject to the interaction mode, per-tool policy and sandbox. It does not provide sudo privileges.

Choices:

- System only: `Deny`; `Allow system access for this session`.
- Editing only: `Deny`; `Use Edit`; `Use Copilot`; `Use Auto`.
- Combined: `Deny`; `Edit + system access`; `Copilot + system access`; `Auto + system access`.

Combined approval grants the displayed bundle in one decision. It does not silently grant only one part. If only system access is missing, there is no editing-mode selector.

Focus starts on Deny. Arrow keys/Tab move among choices; Enter submits the selected choice; Esc denies this request without stopping the turn. PgUp/PgDn scroll supporting details without changing the choice. Preserve application-level interrupt/exit handling. The footer shows only available navigation/decision keys. Pasted text or filter input cannot become an approval action.

Use current semantic theme slots, clear labels and layout that remains usable without color. Keep title, current mode, requested scope and actions visible; supporting reason text can scroll. At the dock's short-height budget, show a compact summary and one selected action with its position/count rather than clipping all choices. Below the application's supported terminal minimum, show its existing size gate and accept no hidden approval.

## Arrival and panel ownership

Reconcile access requests on existing runtime ticks/events before early returns, and after another panel closes. The next runtime update must surface a pending request without any keypress. The fix must not depend on the user typing into an invisible input.

Do not replace an unrelated open dock panel. Show a waiting-permission indication, leave its input/focus intact, and open the access panel once the dock becomes free. Keep the normal transcript and background updates flowing.

Bind every displayed panel and emitted choice to a fresh opaque request ID, owning session and the presented current mode/system tuple. Do not use a provider call ID alone as this identity: providers may reuse call IDs across steps. Close an expired panel when its request is canceled, replaced or cleared. Never answer whichever request happens to be pending when an old panel message arrives.

Once open, a request retains its panel identity across ordinary rerenders/resizes; it does not reopen/reset selection on every tick. A permission-state refresh resets the changed presentation to Deny. Preserve the user's main input draft.

## Shared resolution contract

Extend the existing pending mechanism with typed access-request metadata and typed applied-result metadata. These are in-memory companions, not a new persisted request format. Ordinary command editing keeps `UserApprovalDecision.Edited`; it is not the internal capability-decision format.

The shared presentation snapshot contains request ID, owning session ID, actor/step, reason, current mode/system flag and the two missing-capability flags. The choice contains request/session identity, presented mode/system state, approved/denied and an optional selected editing variant. A system-only choice has no editing variant. The result contains a status and the actual effective mode/system state.

App runtime owns two structural operations, supplied to TUI and ACP:

- `AccessRequest() (snapshot, pending)` returns the current typed presentation.
- `ResolveAccess(choice) (result, error)` validates and settles that exact request.

Session owns pending-request identity/lifetime and one settlement claim shared by approval, denial, cancellation and shutdown. App owns policy validation/application. The TUI renders and submits the choice; ACP gathers the client choice. Neither transport sends an approved result to the tool before the app resolver commits it.

Resolution order:

1. Find the canonical pending request by owner/session and request ID. Reject expired, foreign-session, child-owned and quiescing requests without changing permissions.
2. Check the request's original context and claim its settlement against cancellation/denial. Repeated delivery cannot apply a grant or send a second result.
3. Compare effective mode/system state with the presented tuple. If changed, apply nothing and return refresh-required while retaining the pending request. Rebuild the UI or reissue the ACP permission question against fresh state.
4. Validate the selected mode, requested capability bundle and availability of every required policy setter before any mutation. A choice cannot grant an unrequested capability. Invalid choices or unavailable application settle as a non-approved failure; the tool unblocks with accurate failure information.
5. Apply the mode/force-class policy and session state coherently using the existing validated, in-memory setters. No file save, runtime rebuild, provider construction or model request occurs in this commit step. Combined grants cannot partially succeed.
6. Publish the applied state, then deliver the typed actual result to the canonical waiting tool and clear only that request. Report approved only after application succeeds.

Validation/refresh that does not settle the request releases its claim; concurrent cancellation can still settle it. Once application has been accepted, it completes before acknowledgment. A later cancellation does not roll back the user's accepted session grant. Cancellation that wins first leaves permissions unchanged.

Do not resolve on an event/display copy of `PendingToolCall` or close/send its channel independently. All capability results pass through the canonical settlement path. Registration rejects a second access request rather than replacing an occupied approval slot. Cancellation/cleanup compare identity before clearing, so they cannot erase a newer ordinary approval.

The tool consumes the applied result and describes actual state. Denial in Copilot says it remains in Copilot; system-only success does not falsely announce a mode switch. An untyped “approved” channel value without a committed result cannot produce a success claim. Standalone/test runtimes must inject the resolver capability or fail closed.

## ACP compatibility

Add an optional `modeAccess` object to the existing permission-request JSON containing the presentation snapshot. Continue populating ordinary tool-name/reason fields with a clear capability summary so clients ignoring new fields still receive meaningful disclosure.

Add an optional typed `modeAccess` choice to permission responses. Continue accepting legacy `approved`/`edited` replies for valid requests: an editing variant is used only when editing is missing, and omitted legacy selection defaults to Edit in that case. An existing editing mode is preserved for a system-only request; arbitrary edited strings never become mode names.

Keep the original snapshot server-side for legacy replies. Validate its tuple before application. A stale result asks the client again against fresh state; an invalid/missing application capability returns non-approved failure.

Split client-choice collection from the bridge's current response-channel delivery. The capability branch collects a choice, calls the shared runtime resolver and notifies clients of the committed mode/system state. Ordinary tool approvals retain their existing bridge behavior. Do not retain the old duplicate mode/system setter branch after resolution.

No Go type is imported from `internal/` into `web/`. The additive JSON contract is the integration boundary; existing `approved`/`edited` clients continue to work.

## Session lifetime and background integration

Approving this request writes no config file. Both its mode choice and system flag apply to the active conversation, matching manual mode changes. A new or resumed conversation uses the configured starting mode and starts without the prior system grant. Explicit later settings saves retain their existing semantics; this delivery does not redesign all settings persistence.

Represent a user-selected mode as a session override separate from the runtime's configured starting mode. Grant resolution and existing manual mode setters update that same override; the displayed effective mode and runner policy agree. Config reload with an unchanged configured mode reapplies the override after rebuilding the runner. An explicit change to the configured mode clears the override and applies that new configured mode. New/resumed State does not copy the old override. Reuse any equivalent state introduced by the in-progress background delivery rather than adding a competing source of truth.

Runtime reload within the same conversation preserves its effective access state and request ownership according to the merged background work's session contract. A request never calls `persistAndReload` or replaces/stops a job/watch manager. Existing running jobs retain their captured settings; this grant does not mutate already-launched execution specifications.

Session replacement/exit cancels unresolved requests through the same settlement boundary. Preserve the background delivery's active-work warning and stop/join-before-optional-postmortem sequence. Existing explicit exit-analysis authorization remains separate from ordinary requests.

This feature can be planned while background execution is in progress. Implementation must reconcile shared runtime files against the merged lifecycle code. It needs no new command-execution API or timer semantics.

## Verification

Required cases:

1. Parent request opens on a runtime tick with no keyboard input at 80×24, 120×40 and 200×60.
2. System-only approval from Edit, Copilot and Auto preserves the mode, grants only session system access and offers no editing selector.
3. System-only approval from Plan/Default retains that mode; combined approval explicitly selects an editing variant and grants both capabilities.
4. Editing-only approval leaves system access unchanged. Already-satisfied requests return without a popup or a new grant.
5. Deny/Esc preserves effective state, gives an accurate tool result and leaves the turn usable.
6. Other panels stay open; queued requests appear afterward. Draft input, focus, resize, short height and no-color behavior remain correct.
7. A recording policy setter/barrier proves application completes before the tool can observe approval, in both TUI and ACP.
8. Invalid selection, absent resolver, setter-prevalidation failure and malformed arguments change no authority and cannot report success.
9. Cancellation, duplicate responses, replaced request, changed permission tuple and session shutdown arbitrate once. Late choices do not grant or clear another request.
10. Child escalation is rejected before a popup; ordinary child command approvals still work.
11. Config-save/reload spies receive zero calls during grant. An already-running fake/real managed command retains its manager, execution identity and captured settings across approval.
12. ACP typed and legacy clients receive explicit disclosure, actual committed state and valid result ordering. A client's edited text cannot widen scope.
13. Fresh/new/resumed conversations start without the prior system grant; in-conversation reload preserves the intended state without reviving expired requests.
14. Existing ordinary approvals, manual mode commands and exit postmortem authorization pass their regressions.

Use `testenv.Run`/explicit app fixtures and injected auth dependencies. Tests use test-owned endpoints and no real keychain, model, sudo or LAN target. Use channels/barriers for resolution races. The execution plan will name each new test and package-scoped implementation task.

Baseline checks performed during authoring:

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/tools/native ./internal/app/tui ./internal/acp -run 'ModeRequest|ModeElevation|SystemAccess|PermissionApproval' -count=1
```

The focused tests passed. The separate temporary TUI overlay probe also passed, reproducing the current defect rather than validating a fix. No production implementation or full-suite verification was performed.

## Implementation status

Implemented on branch `system-access-approval`, based on `de5e47bd` (the source
baseline above). One commit per implementation task:

```
28e77f69 system-access-approval: task 1 — Track the effective session mode separately from configured defaults
118da2ef system-access-approval: task 2 — Make access requests settle once on canonical session state
6092915f system-access-approval: task 3 — Apply access and manual mode changes through the app runtime
125b7545 system-access-approval: task 4 — Make mode.request request capabilities and report committed state
04a3a84e system-access-approval: task 5 — Add the dedicated access dock panel
526e5d75 system-access-approval: task 6 — Reconcile and resolve access panels in the TUI
ec063f31 system-access-approval: task 7 — Connect the TUI to the runtime resolver
7ee6ecd5 system-access-approval: task 8 — Add collect-only access permission requests to ACP
5d9141b3 system-access-approval: task 9 — Resolve ACP access through the shared runtime
```

Acceptance checks run at the final commit:

```sh
gofmt -l <touched Go files>     # no output
git diff --check                # clean
go test -race ./internal/app/session ./internal/acp -count=1   # ok
go test ./...                   # only the pre-existing baseline failures
go vet ./...                    # clean
go build -o /private/tmp/marshal-access-approval ./cmd/marshal # ok
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-access-approval go test ./test/usability/... -run TestScripted  # pass
```

Review assertions confirmed on the final diff: no access-grant path calls
`persistAndReload`, a config save or a runtime reload; the TUI gained no
`internal/agent` import; `web/` still imports no `internal/` package; there is
no schema migration and no persisted system-access flag; and the background
explicit-termination/exit ordering is untouched.

## Integration onto main

`main` moved while this branch was under review. The integration was done in the
branch rather than by a rebase, so the per-task commits and their verification
history survive:

| Merge | Integrates |
|---|---|
| `bf0e8f7e` | `main` at `90ba0202` — `reliable-background-execution` |
| `60450b3f` | `main` at `ab22c47b` — the base-rule and `workspace.finish` guard commits |
| `cb5c5322` | the schema-budget trim below |

Eight files conflicted, all in `internal/{app,acp,tools/native}` where the
access paths and the background-lifecycle paths overlap. Both features were
kept: the session's access settlement and quiesce compose with the new launch
generation rather than replacing it, and the access panel's arrival and
resolution seams are unchanged. Task 3's job-identity test now launches through
`native.LaunchSpec` (`JobManager.Launch`), matching the managed-execution API
that replaced `JobManager.Start`.

The merged tree re-ran the plan's checks: `gofmt -l` clean, `git diff --check`
clean, `go vet` clean, `go test -race ./internal/app/session ./internal/acp`
ok, `go test ./...` at the recorded pre-existing baseline, and `TestScripted`
passing against the merged binary. `TestDefaultPromptBudget` failed on the
merged tree — 15032 bytes of model-facing tool schemas against its unchanged
15000-byte ceiling — because the two `main` commits had consumed the headroom.
The `mode.request` description was trimmed by 55 bytes to 14977 (system prompt
8886/9000); the ceiling was not raised.

Known limitations:

- The background-execution branch landed after this implementation was first
  verified. That interaction is closed rather than outstanding: see
  "Integration onto main" above.
- `internal/snapshot`'s `TestRunManagedCancellationKillsTree`, which arrived
  with the background-execution work, is environment-sensitive: it failed
  intermittently in the agent worktree with `operation not permitted` while
  probing a process group, and passed 5/5 in the main checkout. This branch's
  diff against `main` for `internal/snapshot` and the managed-process code is
  empty, so the flake is not a regression from this feature.
- `internal/snapshot`, `internal/tools/desktop/browser`, `internal/worktree` and
  two snapshot-maintenance tests in `internal/app` carry pre-existing failures
  at the baseline commit. They are unrelated to this feature and were left
  alone.
- One full-suite run showed `internal/sandbox`'s
  `TestContainerRunWithFakeRuntime` failing at its 5s timeout under load. It
  passes in isolation and on a repeat full-suite run, so it is a load-induced
  flake rather than a regression from this change.

## Assumptions and review boundary

Approved choices are the dedicated dock panel, Deny as initial selection, session-only application without config save, system-only requests without a mode field, and top-level ownership of grants.

The user approved the detailed design and requested a full implementation plan, including code changes, using Marshal's internal `marshal-writing-plans` skill. The execution plan is [2026-10-18-system-access-approval-plan.md](plans/2026-10-18-system-access-approval-plan.md). Implementation has not started.

Open questions: none.
