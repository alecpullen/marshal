# System-access approval implementation plan

Date: 2026-10-18. Status: executed and integrated; all ten tasks implemented and verified.
Integrated onto `main` (see [feature-spec.md](../feature-spec.md) "Integration onto main"):
`90ba0202` and `ab22c47b` were merged into this branch, eight overlapping files
were resolved by composing both features, Task 3's job-identity test was moved
onto the managed-execution API, and the `mode.request` description was trimmed
to fit the unchanged tool-schema ceiling.
Authoring skill: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).
Approved design: [feature-spec.md](../feature-spec.md).
Base: `de5e47bdc5e695d4e422e99059d27354a84dce14` in the Marshal checkout.

**Goal:** give system-only, editing-only and combined requests an explicit session-scoped approval, applied by one runtime resolver before the tool resumes, without rebuilding the runtime or disrupting jobs.

**Non-goals:** Send now, background-worker lifetime changes, job cards/widgets/watchers, latency or credential-isolation work, persistent grants, sudo, general approval queues, or a browser UI redesign.

**Assumptions:** latency and test isolation are complete; background-execution implementation is proceeding separately. Production uses the existing dock and single pending-approval slot. New and resumed conversations start with configured mode and system access off. Mode hints request editing capability; they do not force a particular editing variant.

Execute tasks **sequentially**, verifying and committing each before starting the next, with commit subject `system-access-approval: task N — <title>`. Package boundaries below are intentional. Intermediate commits compile and pass their named package checks; full transport behavior is assembled by Tasks 7 and 9. Do not deploy intermediate commits. Plan authoring changes documentation only; execution starts after plan approval.

If the background branch has landed, adapt verified anchors to its equivalent lifecycle methods while retaining the contracts below. In particular, reuse its session-mode override if equivalent; do not create a second override or change its stop/join/exit ordering.

## Contracts

These declarations are the agreed cross-task API. Put the session declarations into the existing `internal/app/session/pending.go` and `session.go`, rather than creating a new permission subsystem. JSON tags on session values support tool-result serialization; ACP still declares its own wire DTOs.

```go
// package session
type AccessState struct {
    Mode   string `json:"mode"`
    System bool   `json:"system"`
}

type AccessIntent struct {
    Editing bool
    System  bool
    Reason  string
}

type AccessRequest struct {
    RequestID      string      `json:"requestId"`
    SessionID      string      `json:"sessionId"`
    Actor          string      `json:"actor,omitempty"`
    StepID         StepID      `json:"stepId,omitempty"`
    Reason         string      `json:"reason"`
    Current        AccessState `json:"current"`
    MissingEditing bool        `json:"missingEditing"`
    MissingSystem  bool        `json:"missingSystem"`
}

type AccessChoice struct {
    RequestID string      `json:"requestId"`
    SessionID string      `json:"sessionId"`
    Current   AccessState `json:"current"`
    Approved  bool        `json:"approved"`
    Mode      string      `json:"mode,omitempty"`
}

type AccessStatus string
const (
    AccessAvailable AccessStatus = "already_available"
    AccessGranted   AccessStatus = "granted"
    AccessDenied    AccessStatus = "denied"
    AccessCanceled  AccessStatus = "canceled"
    AccessFailed    AccessStatus = "failed"
    AccessRefresh   AccessStatus = "refresh_required"
    AccessExpired   AccessStatus = "expired"
)

type AccessResult struct {
    RequestID string       `json:"requestId,omitempty"`
    SessionID string       `json:"sessionId"`
    Status    AccessStatus `json:"status"`
    Effective AccessState  `json:"effective"`
    Message   string       `json:"message,omitempty"`
}

// Prepare validates everything, returning an infallible, bounded memory-only
// policy commit. It must not mutate State, acquire the access gate, do I/O,
// reload, or call a provider. A nil Apply is invalid.
type AccessCommit struct {
    Effective AccessState
    Apply     func()
}
type AccessPrepare func(AccessRequest, AccessChoice) (AccessCommit, error)

type UserApprovalDecision struct {
    Approved bool
    Edited   string
    Access   *AccessResult
}
type PendingToolCall struct {
    ID, Name, Args, Command, Risk, Reason, Diff, Schema string
    StepID       StepID
    ResponseChan chan UserApprovalDecision
    Access       *AccessRequest
    responded    sync.Once
}
func (s *State) AccessState() AccessState
func (s *State) SetSessionMode(mode string, apply func()) error
func (s *State) ModeForConfig(cfg config.Config) string
func (s *State) RebaseSessionConfig(cfg config.Config)
func (s *State) RegisterAccess(ctx context.Context, pending *PendingToolCall,
    intent AccessIntent) (AccessRequest, bool, error)
func (s *State) AccessRequest() (AccessRequest, bool)
func (s *State) ResolveAccess(choice AccessChoice, prepare AccessPrepare) (AccessResult, error)
func (s *State) CancelAccess(requestID string, status AccessStatus, message string) bool

// package app; all three use rt.reloadMu before entering session's access gate.
func (rt *Runtime) AccessRequest() (session.AccessRequest, bool)
func (rt *Runtime) ResolveAccess(choice session.AccessChoice) (session.AccessResult, error)
func (rt *Runtime) SetSessionMode(mode string) error

// package native; add to both Options and toolSet, copying in newToolSet.
// AccessRequests bool: caller promises an attached access resolver/transport.
// Defaults false for standalone registration; child requests remain forbidden.

// package accesspanel
type ChoiceMsg struct { Choice session.AccessChoice }
type option struct{ label, mode string }
type Panel struct {
    request session.AccessRequest
    options []option
    selected, offset int
}
func New(request session.AccessRequest) *Panel
func (p *Panel) Request() session.AccessRequest
func (p *Panel) Update(msg tea.Msg) tea.Cmd
func (p *Panel) View(width, maxHeight int) string
func (p *Panel) Sizing() dock.Sizing
// No app/agent/policy dependency.

// package tui
func WithAccessRequests(
    get func() (session.AccessRequest, bool),
    resolve func(session.AccessChoice) (session.AccessResult, error),
) Option
func WithSessionModeSetter(set func(string) error) Option

// package acp; TurnRuntime retains its existing fields plus the two callbacks.
type TurnRuntime struct {
    SessionID string
    BeginWork func(context.Context) (context.Context, func(), error)
    Run RunnerFunc
    Events *pubsub.Broker[session.Event]
    SetMode func(string) error
    SetSystemAccess func(bool)
    AccessRequest func() (session.AccessRequest, bool)
    ResolveAccess func(session.AccessChoice) (session.AccessResult, error)
    Steer func(string)
    State *session.State
    SwarmRunner AgentRunner
    PipelineFactory func(string, map[routing.AgentRole]string) AgentRunner
    WatchResumeEnabled func() bool
    WatchResumeGatePending func() bool
    DB *db.DB
    ProjectID int64
}
```

The ACP wire declarations are also cross-task contracts (implemented in Task 8; consumed in Task 9). PermissionRequest adds ModeAccess *ModeAccessRequest, PermissionDecision adds ModeAccess *ModeAccessChoice, with `json:"modeAccess,omitempty"` on both fields.

```go
// package acp
type ModeAccessState struct {
    Mode   string `json:"mode"`
    System bool   `json:"system"`
}
type ModeAccessRequest struct {
    RequestID      string          `json:"requestId"`
    SessionID      string          `json:"sessionId"`
    Actor          string          `json:"actor,omitempty"`
    StepID         int64           `json:"stepId,omitempty"`
    Reason         string          `json:"reason"`
    Current        ModeAccessState `json:"current"`
    MissingEditing bool            `json:"missingEditing"`
    MissingSystem  bool            `json:"missingSystem"`
}
type ModeAccessChoice struct {
    RequestID string          `json:"requestId"`
    SessionID string          `json:"sessionId"`
    Current   ModeAccessState `json:"current"`
    Mode      string          `json:"mode,omitempty"`
}
func (b *PermissionBridge) CollectAccess(ctx context.Context, sessionID, toolCallID string,
    request session.AccessRequest) (session.AccessChoice, error)
```

**Ownership and locks:** `Runtime.reloadMu → Runtime.mu (when swapping pointers) → State.accessMu → State.workMu or State.mu`. Never hold State.mu while invoking a policy callback or publishing an event. Never hold workMu while taking accessMu. SetSystemAccess, session mode changes, registration, resolution, cancellation, shutdown settlement and the quiescing transition share accessMu. Permission readers take State.mu only. A grant linearizes after its final context/quiesce check under accessMu, immediately before Apply. Cancellation already visible then wins; cancellation afterward waits for the accepted application/ack to finish.

**Private session fields:** `accessMu sync.Mutex`, `configuredMode string`, `sessionModeOverride string`, `pendingAccessCtx context.Context`, `pendingAccessIntent AccessIntent`. Override/context/intent are guarded by accessMu; Config/system/pending pointer also use the existing State.mu. Snapshot construction copies values, never mutexes. Session mode normalization is the five literal modes, with blank/unrecognized configuration normalized to `default` to match existing config parsing; a user selection must be valid and nonblank. It does not import policy.

**Settlement:** the canonical PendingToolCall owns the buffered response channel. Access registration requires capacity ≥1 and an empty channel. Event/display access copies carry immutable Access metadata and **no ResponseChan**. PendingToolCall.Respond is a no-op when Access != nil; capability delivery uses the session's private settlement helper only. Ordinary approvals retain their existing behavior. Direct or untyped approved results cannot make mode.request report success.

**Missing capability calculation:** editing is missing iff intent.Editing and current mode is Plan/Default; system is missing iff intent.System and !current.System. Already in Edit/Copilot/Auto means no editing selector, regardless of the hint. No missing capability means immediate AccessAvailable, no pending slot or fresh grant.

**Runtime prevalidation:** permitted selected modes are exactly Edit/Copilot/Auto when MissingEditing; otherwise Mode must be empty. Combined choices grant the displayed bundle; system-only preserves Mode, editing-only preserves System. The computed target must equal the resolver's AccessCommit.Effective; session settlement checks it again before Apply. Approval with no missing capability returns AccessAvailable without applying. Nil resolver/policy setter fails closed. Errors never accompany an approved tool decision.

## Task 1: Track the effective session mode separately from configured defaults

**Goal:** session permissions have one synchronized snapshot, and a temporary mode survives an unrelated reload without becoming the next conversation's default.

**Files:** `internal/app/session/session.go`, `internal/app/session/session_test.go`, `internal/app/session/pending.go` (AccessState declaration only).

**Steps**

1. Add AccessState and the three private fields accessMu/configuredMode/sessionModeOverride from Contracts. In `New` initialize configuredMode from cfg.Agent.ApprovalMode with configuredAccessMode. Leave systemAccess false and override empty. Store the normalized effective mode in State.Config.
2. Implement `AccessState` by taking State.mu once and copying Config.Agent.ApprovalMode/systemAccess. Add private `normalizeAccessMode(mode string) (string, error)`: accept exactly `plan/default/edit/copilot/auto`. Use a separate private `configuredAccessMode(mode string) string` that lowercases configuration as existing ParseApprovalMode does, then returns the valid mode or default for blank/unrecognized configuration, matching the existing seeded-mode tests.
3. Implement `SetSessionMode`: validate a nonblank literal mode and nonnil apply before mutation; acquire accessMu, reject quiescing by checking workMu, call apply without State.mu, then set sessionModeOverride and Config.Agent.ApprovalMode under State.mu. apply is an infallible runner setter prepared by app. Manual mode changes use this same method later.
4. Make `SetSystemAccess` take accessMu before State.mu. It remains a memory-only explicit toggle, including the existing exit-postmortem authorization. Do not add a persistent flag or alter child-inheritance call sites.
5. Implement `ModeForConfig` under accessMu: if the incoming configured mode equals configuredMode and override is nonempty, return override; otherwise return the incoming configured mode. It does not mutate anything.
6. Implement `RebaseSessionConfig` under accessMu: if normalized cfg.Agent.ApprovalMode differs from configuredMode, clear the override; record the new configuredMode; install cfg under State.mu with Agent.ApprovalMode replaced by the remaining override when present. Preserve systemAccess. Failed reloads must never call this method.
7. Add to existing session_test.go:
   - `TestAccessStateReadsModeAndSystemTogether`: new state reports configured mode and false; setters produce the expected pair.
   - `TestSessionModeOverrideSurvivesUnrelatedConfig`: override Auto, rebase config still configured Default with an unrelated change, effective Auto remains.
   - `TestExplicitConfiguredModeChangeClearsOverride`: Default→configured Copilot clears prior Auto override and a later unrelated reload retains Copilot.
   - `TestSessionModeFailureDoesNotMutate`: invalid mode/nil apply/quiescing leave the pair and override unchanged; invalid input never calls apply.
   - `TestNewStateDoesNotInheritAccessOverrides`: constructing a second state from the configured fixture yields configured Default/system false.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/session -run 'AccessState|SessionMode|ConfiguredMode|NewStateDoesNotInherit' -count=1
```

Expected: all new tests pass; no persistence, provider or credential access.

## Task 2: Make access requests settle once on canonical session state

**Goal:** approval, denial, cancellation and shutdown share a canonical settlement gate and cannot acknowledge an unapplied grant.

**Files:** `internal/app/session/pending.go`, `internal/app/session/session.go`, `internal/app/session/session_test.go`.

**Steps**

1. Add the remaining session declarations and fields from Contracts. Add `Access *AccessRequest` to PendingToolCall and `Access *AccessResult` to UserApprovalDecision. Add the Respond access guard before its sync.Once block. Ordinary command editing remains unchanged.
2. Implement `RegisterAccess` with accessMu held:
   - Reject nil/already-canceled context, nil pending, missing/unbuffered/nonempty response channel, empty intent, child depth >0, quiescing and any occupied pending-approval slot. Do not replace/clear the existing approval.
   - Read one current pair. If all capabilities are available, return a value snapshot, false, nil; do not allocate an ID or publish a pending event.
   - Generate a fresh `crypto/rand.Text()` request ID independently of pending.ID. Copy session ID, reason, pending.StepID and the matching `State.Step` actor label (fall back to role, then `Marshal`). Do this without recursively taking State.mu. Create missing flags from Contracts.
   - Save original ctx and full intent privately. Set pending.Access to a separately allocated snapshot, then set the canonical pending pointer under State.mu. Publish an immutable event copy with Access copied and ResponseChan nil, after unlocking State.mu. Return snapshot, true, nil.
3. Implement private `accessRequestLocked() (AccessRequest, bool)` (caller holds accessMu): read canonical identity + current pair under State.mu, recompute missing flags from saved intent, and return a value. Implement public `AccessRequest` as the accessMu wrapper. Do not mutate previously published metadata while refreshing.
4. Implement private `finishAccessLocked(result AccessResult)` (caller holds accessMu):
   - Capture and clear only the matching canonical typed request and its private ctx/intent under State.mu; an ID mismatch does nothing.
   - State is already committed when called. Publish EventPendingApprovalChanged with nil, then send exactly one UserApprovalDecision with Access pointing at a copied result, Approved true **only** for AccessGranted; close the canonical buffered channel. Use canonical responded.Do to protect delivery, bypassing Respond's access guard.
   - Never send/close on an event copy, never acquire a transport's mutex, and never invoke config/model/job code.
5. Implement `ResolveAccess` under accessMu:
   - Return AccessExpired + an error for wrong request/session ID, missing/foreign/child-owned request; preserve any current approval.
   - Check original ctx.Err and quiescing first. If canceled/quiescing, finish AccessCanceled with actual pair and return nonapproved.
   - Compare choice.Current to the actual pair. Mismatch returns AccessRefresh/current pair, nil; retain request and release gate, with no setter invocation.
   - For a matching denial, finish AccessDenied with actual pair; no capability is revoked.
   - If neither capability is now missing, finish AccessAvailable/current pair, no mutation.
   - Validate choice.Mode against missingEditing. Compute expected target: selected editing variant iff missingEditing, and true System iff missingSystem; all other fields retain the actual pair.
   - Nil prepare, invalid choice, prepare error, nil Apply or unexpected Effective settles AccessFailed with the unchanged actual pair, Message containing the concrete failure, and a nonnil error. Perform **all** validation before invoking Apply.
   - Recheck original context and quiescing after prepare. Then invoke Apply while holding accessMu but not State.mu; under State.mu update Mode and sessionModeOverride only when MissingEditing, and set System true only when MissingSystem. Preserve all other fields/overrides; finish AccessGranted. Do not call public mode/system setters reentrantly.
6. Implement `CancelAccess` under accessMu, accepting only AccessCanceled/AccessFailed. Compare canonical RequestID; a mismatch returns false and changes nothing. Matching cancellation finishes a result with actual pair.
7. In `SetPendingApproval` take accessMu before existing State.mu. When an access request occupies the slot, a generic nil clear is ignored; a nonnil replacement is rejected by denying that incoming ordinary pending after releasing locks. Existing ordinary P1→P2 replacement behavior remains unchanged when no access request occupies the slot. Include Access metadata/no channel in typed display copies.
8. Make `BeginQuiesce` take accessMu before workMu to linearize the gate against application. In `ResolvePendingForShutdown` take accessMu before clearing the pending fields; finish typed access as AccessCanceled through the private helper, then perform the existing ordinary approval/question/skill/sudo/steering cleanup. Avoid republishing a stale nil after a newer registration: shutdown is already quiescing and registration is gated. Preserve current ordinary cleanup behavior.
9. Add channel/barrier tests to existing session_test.go (no sleeps as race ordering):
   - `TestAccessRegistrationRejectsChildAndOccupiedSlot`: both fail without replacing or publishing; include a generic occupied slot.
   - `TestAccessRequestIdentityAndSnapshotIsolation`: reused provider IDs get different request IDs; event copies have metadata/no channel and cannot Respond/close canonical channel.
   - `TestAccessResolveAppliesBeforeAcknowledgment`: block inside Apply; waiter cannot receive until release, then sees effective state and typed grant.
   - `TestAccessCancelWinsBeforeAcceptance`: canceled original ctx prevents prepare/application and returns a typed canceled result.
   - `TestAccessAcceptedCommitWinsLaterCancellation`: cancel after Apply entered; no result before release, one grant afterward; late CancelAccess cannot revoke it.
   - `TestAccessRefreshRetainsPendingAndResetsNoAuthority`: changed pair returns refresh without calling prepare; next choice for fresh tuple resolves.
   - `TestAccessInvalidCommitFailsWithoutPartialGrant`: invalid variant, unexpected target, nil Apply, nil prepare and prepare error all unblock false with unchanged state.
   - `TestAccessDuplicateForeignAndLateChoicesCannotSettleNewRequest`: one application; old choice/cancel/cleanup cannot grant or erase a later access or ordinary request.
   - `TestAccessDeniedAndAvailableReturnActualState`: Deny from Auto/system-on reports Auto/true; already-satisfied registration uses no slot; satisfied-after-arrival resolves without a setter.
   - `TestAccessShutdownSettlesOnce`: quiesce/shutdown vs approval interleavings produce one canonical result and no post-shutdown grant.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/app/session -run 'Access|SessionMode|ConfiguredMode|ResolvePending|BeginQuiesce|PendingApproval' -count=1
```

Expected: all assertions pass under race detection; no hanging waiter, double close or mutex inversion.

## Task 3: Apply access and manual mode changes through the app runtime

**Goal:** the runtime validates/apply/acknowledges grants without save/reload and preserves effective permissions correctly during real config reload and session replacement.

**Files:** `internal/app/runtime.go`, `internal/app/app.go`, `internal/app/runtime_test.go`, `internal/app/app_test.go` (all package app).

**Steps**

1. Add the three Runtime methods from Contracts in runtime.go. AccessRequest takes reloadMu and delegates to the current State; nil State yields no request. If the pending snapshot now has neither missing capability, call State.ResolveAccess with that identity/current tuple, Approved:true, empty Mode and nil prepare: its already-available branch performs no setter/application. Return State.AccessRequest afterward, so fulfilled requests disappear and a concurrent tuple change instead produces a fresh presentation. ResolveAccess holds reloadMu for the entire session.ResolveAccess call. Nil State returns an expired result/error. The prepare closure:
   - Revalidates the exact choice shape and expected target from Contracts.
   - Before any mutation, rejects missing Runner or Runner.Policy whenever an editing mode change is required; validate mode with `policy.ValidApprovalMode` and require the three literal editing variants. System-only requires State and preserves the current mode.
   - Returns AccessCommit{Effective: target, Apply: func(){...}}. For editing changes, Apply calls Runner.SetForceClass("edit") and Runner.SetApprovalMode(policy.ApprovalMode(target.Mode)); system-only uses a nonnil no-op Apply. Session commits its mode/flag afterward under its own gate.
   - Does not call State.SetSystemAccess/SetSessionMode inside Apply, save a file, reload, invoke provider/model code, replace a manager or acquire a write gate.
2. SetSessionMode holds reloadMu, validates the literal five-mode selection and State availability, then calls State.SetSessionMode with the existing in-memory setters. A present Runner must have Policy; an inactive runtime with no Runner records the manual mode using a nonnil no-op callback, retaining the current provider-unconfigured UI behavior. Force class is `question` for Plan/Default, `edit` otherwise. No config mutation on Runtime.Config. This API is also the manual TUI/ACP path.
3. In `reloadAgentRuntime`, after session-routing merge but before build, preserve the incoming configured `cfg` and make `effectiveCfg := cfg` with Agent.ApprovalMode = State.ModeForConfig(cfg). Build the new runner from effectiveCfg. On successful install, replace `rt.State.Config = cfg` with `rt.State.RebaseSessionConfig(cfg)`, and set `rt.Config = cfg` to the **configured** value. Set the new/live runner's force class and approval mode from the effective State.AccessState after CopyFrom. On dry-run failure, do not change configured baseline, override, flag or existing runner.
4. Retain current manager rebuild semantics for explicit reloads; access resolution never calls this path. Preserve SystemAccess across in-conversation reload. Acquire reloadMu across the whole `NewSession` build/install transaction, so it cannot race a resolver against a replaced State/Runner; reuse the equivalent gate if the background branch already adds it. No override/flag is copied into NewSession or startup `WithExistingSession` restore. Both construct State from configured Runtime.Config/loaded config. After a successful build and before installing the replacement, call oldState.BeginQuiesce and oldState.ResolvePendingForShutdown outside Runtime.mu, then swap pointers and perform existing resource cleanup. This settles the old access waiter before its managers close and rejects new requests on that old State. A failed build does neither and leaves old state/pending intact. Preserve the background branch's fuller stop/join transition if present rather than adding a competing cleanup.
5. Add to runtime_test.go/app_test.go, using existing injected config/auth fixtures:
   - `TestRuntimeAccessSystemOnlyPreservesEachMode`: table over five modes, including legacy editing hint in Edit/Copilot/Auto; only System changes.
   - `TestRuntimeAccessSatisfiedWhilePendingSettlesAvailable`: a manual grant satisfies the pending request; AccessRequest clears it with one AccessAvailable result and no policy setter, popup or new grant.
   - `TestRuntimeAccessCombinedCommitsSelectedBundle`: Default/Plan × Edit/Copilot/Auto commits policy, force class and state before the typed response.
   - `TestRuntimeAccessUnavailablePolicyFailsBeforeMutation`: combined request with nil Runner/Policy fails and preserves both capabilities; system-only succeeds without an editing setter.
   - `TestRuntimeAccessDoesNotSaveReloadOrReplaceJobs`: instrument config reloader and save-visible fixture bytes; resolve through Runtime; no calls/files change, and Runner/JobManager/WatchManager pointers stay equal.
   - `TestRuntimeAccessKeepsRunningCommandIdentity`: reuse runtime_test.go's existing `fakeRunner{run: ...}` with started/release channels, construct `native.NewJobManager(ctx, fr, state.WorkingDir, 2, time.Minute, 4096)`, attach it to the Runtime fixture, and Start(ctx,"fixture download",0). Capture CommandRequest on started; grant system access; assert same manager and List/Output job ID with unchanged captured Dir/Timeout/MaxOutputBytes. Release the fake and Wait(ctx,id) for normal completion. Always shut down the fixture manager in cleanup.
   - `TestRuntimeAccessOverrideSurvivesReload`: grant Copilot, reload a changed unrelated setting from configured Default, assert policy/state Copilot, System true and Runtime.Config still Default.
   - `TestRuntimeExplicitConfiguredModeReplacesOverride`: reload configured Edit after the above; policy/state become Edit, System stays true.
   - `TestRuntimeFailedReloadKeepsAccessState`: invalid rebuild leaves old pair, runner and override untouched.
   - `TestRuntimeNewAndResumedSessionResetAccess`: new session and separate startup with WithExistingSession both use configured mode/system false; prior request ID expires and cannot affect new state.
   - `TestRuntimeAccessSerializesWithReloadAndQuiesce`: barrier interleavings never apply to a replaced runner/state or acknowledge a partially installed mode.
6. Tests must use `provider.NewAuthResolver` over memory stores or the existing app fake resolver and explicit config fixtures. Reuse the package's testenv entry point. Do not contact developer providers, keychain, LAN or sudo.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app -run 'RuntimeAccess|RuntimeExplicitConfiguredMode|RuntimeFailedReload|RuntimeNewAndResumed|RuntimeNewSession' -count=1
```

Expected: runtime/state/policy agree; grant triggers zero rebuild/persistence and the active command completes with its original identity.

## Task 4: Make mode.request request capabilities and report committed state

**Goal:** tool arguments distinguish missing capabilities and every result describes actual state rather than claiming a transport-applied mode.

**Files:** `internal/tools/native/mode_request.go`, `internal/tools/native/native.go`, `internal/tools/native/mode_request_test.go`; one wiring field in `internal/app/app.go`.

**Steps**

1. Add `AccessRequests bool` to native.Options and `accessRequests bool` to toolSet. Copy it in newToolSet alongside sessionState. In app.go's root `nativeOpts := native.Options{` inside buildAgentRunnerWithLock, add `AccessRequests: true,`. Child construction remains unable to escalate because the handler and State both reject depth >0. Standalone registrations default false; tests enabling the capability explicitly provide a resolver-driven response.
2. Replace mode_request.go with this complete file:

```go
package native

import (
    "context"
    "encoding/json"
    "fmt"
    "strings"
    "unicode/utf8"

    "marshal/internal/app/session"
    "marshal/internal/tools/registry"
)

type modeRequestArgs struct {
    Mode   *string `json:"mode,omitempty"`
    System bool    `json:"system,omitempty"`
    Reason string  `json:"reason,omitempty"`
}

func accessToolResult(result session.AccessResult) (registry.ToolResult, error) {
    body, err := json.Marshal(result)
    if err != nil {
        return registry.ToolResult{}, err
    }
    return registry.ToolResult{
        Summary: fmt.Sprintf("%s — mode %s, system access %t",
            result.Status, result.Effective.Mode, result.Effective.System),
        Content: string(body),
    }, nil
}

// The runtime commits access before delivering a typed result. This tool
// never treats an untyped Approved/Edited response as a capability grant.
func (t *toolSet) modeRequestTool() registry.Tool {
    tool := registry.Tool{
        Name: "mode.request",
        Description: "Ask the user for missing editing capability and/or system access for this conversation. An editing hint does not choose the user's mode. System access remains subject to mode, policy and sandbox; it does not grant sudo.",
        Schema: json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string","enum":["edit","copilot","auto"]},"system":{"type":"boolean"},"reason":{"type":"string","maxLength":400}},"anyOf":[{"required":["mode"]},{"required":["system"],"properties":{"system":{"const":true}}}],"additionalProperties":false}`),
        Risk: registry.RiskReadOnly,
    }
    tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
        args, err := decodeArgs[modeRequestArgs](tool, call.Args)
        if err != nil {
            return registry.ToolResult{}, err
        }
        if args.Mode != nil && *args.Mode != "edit" && *args.Mode != "copilot" && *args.Mode != "auto" {
            return registry.ToolResult{}, fmt.Errorf("mode must be edit, copilot, or auto")
        }
        if args.Mode == nil && !args.System {
            return registry.ToolResult{}, fmt.Errorf("request an editing mode or system access")
        }
        if utf8.RuneCountInString(args.Reason) > 400 {
            return registry.ToolResult{}, fmt.Errorf("reason exceeds 400 characters")
        }
        if t.sessionState == nil {
            return registry.ToolResult{}, fmt.Errorf("session state not available")
        }
        if t.sessionState.SubagentDepth() > 0 {
            return registry.ToolResult{}, fmt.Errorf("mode.request is top-level only; ask the parent agent to request access")
        }
        if !t.accessRequests {
            return registry.ToolResult{}, fmt.Errorf("access request resolver is not available")
        }
        reason := strings.TrimSpace(args.Reason)
        if reason == "" {
            switch {
            case args.Mode != nil && args.System:
                reason = "The agent requests editing capability and system access."
            case args.System:
                reason = "The agent requests system access."
            default:
                reason = "The agent requests editing capability."
            }
        }
        ch := make(chan session.UserApprovalDecision, 1)
        pending := &session.PendingToolCall{
            ID: call.ID, Name: tool.Name, Args: string(call.Args),
            Reason: reason, Schema: tool.Description, ResponseChan: ch,
        }
        for _, active := range t.sessionState.ActiveToolCalls() {
            if active.ToolCallID == call.ID && active.Name == tool.Name {
                pending.StepID = active.StepID
                break
            }
        }
        request, waiting, err := t.sessionState.RegisterAccess(ctx, pending, session.AccessIntent{
            Editing: args.Mode != nil, System: args.System, Reason: reason,
        })
        if err != nil {
            return registry.ToolResult{}, err
        }
        if !waiting {
            return accessToolResult(session.AccessResult{
                SessionID: request.SessionID, Status: session.AccessAvailable,
                Effective: request.Current,
            })
        }
        var decision session.UserApprovalDecision
        var ok bool
        select {
        case decision, ok = <-ch:
        case <-ctx.Done():
            // If a grant already crossed its acceptance point, CancelAccess
            // waits for that commit; the queued applied result wins.
            t.sessionState.CancelAccess(request.RequestID, session.AccessCanceled, ctx.Err().Error())
            decision, ok = <-ch
        }
        if !ok || decision.Access == nil {
            return registry.ToolResult{}, fmt.Errorf("access request ended without a committed result")
        }
        result := *decision.Access
        if result.RequestID != request.RequestID || result.SessionID != request.SessionID {
            return registry.ToolResult{}, fmt.Errorf("access result identity mismatch")
        }
        if decision.Approved != (result.Status == session.AccessGranted) {
            return registry.ToolResult{}, fmt.Errorf("access result was not committed")
        }
        switch result.Status {
        case session.AccessGranted, session.AccessDenied, session.AccessAvailable:
            return accessToolResult(result)
        case session.AccessCanceled:
            if ctx.Err() != nil {
                return registry.ToolResult{}, ctx.Err()
            }
            return registry.ToolResult{}, fmt.Errorf("access request canceled: %s", result.Message)
        default:
            return registry.ToolResult{}, fmt.Errorf("access request failed: %s", result.Message)
        }
    }
    return tool
}
```

3. Rewrite the three existing mode_request tests to use AccessRequests:true, explicit configured modes, the canonical State.AccessRequest and State.ResolveAccess with a deterministic test AccessPrepare; never use PendingToolCall.Respond as a fake grant. Rename `TestModeRequestApprovedRelaysChosenMode` to `TestModeRequestApprovedReportsCommittedState` and `TestModeRequestDeniedStaysInDefault` to `TestModeRequestDeniedReportsActualState`. Keep `TestModeRequestSystemReasonAndGrant` but assert typed scope and actual state.
4. Add tests in that existing file:
   - `TestModeRequestArgumentValidation`: empty object, false-only system, empty/unknown/null mode, null/malformed types, unknown properties and 401 Unicode characters reject before pending; 400 Unicode characters pass validation.
   - `TestModeRequestCapabilityMatrix`: five current modes × editing-only/system-only/combined × initial system flag; already-satisfied returns without pending, missing scopes match Contracts.
   - `TestModeRequestAutoStillWaitsForSystemDecision`: Auto does not auto-grant ReadOnly mode.request; observe pending before resolving.
   - `TestModeRequestCanceledCleanupCannotEraseLaterApproval`: original ctx cancellation returns and old ID cleanup cannot clear a new generic request.
   - `TestModeRequestUntypedApprovalCannotReportGrant`: direct channel injection of Approved/Edited with no Access yields an error and no permission change; explicitly cancel canonical request during fixture cleanup.
   - `TestModeRequestChildAndStandaloneFailClosed`: child depth and omitted AccessRequests return actionable errors with no pending.
   - `TestModeRequestAcceptedGrantBeatsLaterContextCancel`: barrier in Apply, cancel ctx, release; tool returns committed grant, not a false denial.
5. For tests that wait on registration, use the package's event broker or a test-owned notification barrier; retain a short deadline only as a deadlock guard. No time.Sleep loops for race assertions.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/tools/native -run 'ModeRequest' -count=1
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go build ./internal/app
```

Expected: schema compiles; valid old mode+system calls remain accepted; no result claims a grant before application.

## Task 5: Add the dedicated access dock panel

**Goal:** an immutable request renders clear choices with Deny initially selected and no runtime/policy logic.

**Files:** new `internal/app/tui/accesspanel/accesspanel.go` and `accesspanel_test.go`.

**Steps**

1. Create accesspanel.go with the complete implementation below. Critical current/request/scope/action rows remain ahead of optional detail rows; at short budgets one selected action and its position replace the full list. Ctrl+C is not captured. Supporting details are scrollable when their row budget is positive.

```go
package accesspanel

import (
    "fmt"
    "strings"
    "unicode"

    tea "charm.land/bubbletea/v2"
    "github.com/charmbracelet/x/ansi"

    "marshal/internal/app/session"
    "marshal/internal/app/tui/chrome"
    "marshal/internal/app/tui/dock"
    "marshal/internal/app/tui/layout"
    "marshal/internal/app/tui/theme"
)

type ChoiceMsg struct{ Choice session.AccessChoice }
type option struct{ label, mode string }
type Panel struct {
    request session.AccessRequest
    options []option
    selected, offset int
}
var _ dock.Panel = (*Panel)(nil)

func New(request session.AccessRequest) *Panel {
    p := &Panel{request: request, options: []option{{label: "Deny"}}}
    if request.MissingEditing {
        for _, mode := range []string{"edit", "copilot", "auto"} {
            label := "Use " + strings.ToUpper(mode[:1]) + mode[1:]
            if request.MissingSystem {
                label = strings.ToUpper(mode[:1]) + mode[1:] + " + system access"
            }
            p.options = append(p.options, option{label: label, mode: mode})
        }
    } else if request.MissingSystem {
        p.options = append(p.options, option{label: "Allow system access for this session"})
    }
    return p
}
func (p *Panel) Request() session.AccessRequest { return p.request }
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
func (p *Panel) decision(approved bool) tea.Cmd {
    choice := session.AccessChoice{
        RequestID: p.request.RequestID, SessionID: p.request.SessionID,
        Current: p.request.Current, Approved: approved,
    }
    if approved {
        choice.Mode = p.options[p.selected].mode
    }
    return func() tea.Msg { return ChoiceMsg{Choice: choice} }
}
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
    k, ok := msg.(tea.KeyPressMsg)
    if !ok { return nil }
    switch k.String() {
    case "up", "left", "shift+tab":
        p.selected = (p.selected + len(p.options) - 1) % len(p.options)
    case "down", "right", "tab":
        p.selected = (p.selected + 1) % len(p.options)
    case "pgup":
        p.offset = max(0, p.offset-1)
    case "pgdown":
        p.offset++
    case "enter":
        return p.decision(p.selected != 0)
    case "esc":
        return p.decision(false)
    }
    return nil
}
func clean(text string) string {
    return strings.Map(func(r rune) rune {
        if r == '\n' || r == '\t' { return ' ' }
        if unicode.IsControl(r) { return -1 }
        return r
    }, ansi.Strip(text))
}
func (p *Panel) View(width, maxHeight int) string {
    if maxHeight <= 0 || width <= 0 { return "" }
    pw := layout.PanelWidth(width)
    inner := max(1, pw-2)
    title := "Allow system access?"
    requested := "system access; mode unchanged"
    if p.request.MissingEditing {
        title, requested = "Allow editing?", "editing capability"
        if p.request.MissingSystem {
            title, requested = "Allow editing and system access?", "editing + system access"
        }
    }
    rows := []string{
        fmt.Sprintf("Current: %s; system access: %t", clean(p.request.Current.Mode), p.request.Current.System),
        "Request: " + requested + "; this session only",
    }
    scope := "Interaction mode and tool policy still apply."
    if p.request.MissingSystem {
        scope = "Outside workspace; mode/policy/sandbox still apply; no sudo."
    }
    rows = append(rows, scope)
    actionRows := 1
    if maxHeight >= 10+len(p.options) { actionRows = len(p.options) }
    detailRows := max(0, maxHeight-1-len(rows)-actionRows-1)
    detail := clean(p.request.Reason)
    if p.request.Actor != "" || p.request.StepID != 0 {
        attribution := "Requested"
        if p.request.Actor != "" { attribution = "From " + clean(p.request.Actor) }
        if p.request.StepID != 0 { attribution += fmt.Sprintf(" / step %d", p.request.StepID) }
        detail = attribution + ". " + detail
    }
    details := strings.Split(ansi.Wrap(detail, inner, "/-:"), "\n")
    p.offset = min(p.offset, max(0, len(details)-detailRows))
    for i := p.offset; i < len(details) && i < p.offset+detailRows; i++ {
        rows = append(rows, theme.MutedStyle().Render(details[i]))
    }
    if actionRows == 1 {
        rows = append(rows, fmt.Sprintf("> [%d/%d] %s", p.selected+1, len(p.options), p.options[p.selected].label))
    } else {
        for i, choice := range p.options {
            marker := "  "
            if i == p.selected { marker = "> " }
            rows = append(rows, marker+choice.label)
        }
    }
    hints := "Arrows/Tab choose; Enter decide; Esc deny"
    if detailRows > 0 && len(details) > detailRows { hints += "; PgUp/PgDn details" }
    rows = append(rows, theme.MutedStyle().Render(hints))
    return chrome.Panel(title, strings.Join(rows, "\n"), pw, maxHeight, true, theme.Current())
}
```

2. Create accesspanel_test.go with the complete tests below. These avoid text-input components, global theme mutations in parallel tests, and snapshots of unrelated chrome.

```go
package accesspanel

import (
    "strings"
    "testing"

    tea "charm.land/bubbletea/v2"
    "charm.land/lipgloss/v2"
    "github.com/charmbracelet/x/ansi"

    "marshal/internal/app/session"
    "marshal/internal/app/tui/theme"
)

func request() session.AccessRequest {
    return session.AccessRequest{RequestID: "r1", SessionID: "s1",
        Actor: "Marshal", StepID: 3, Reason: strings.Repeat("Downloading files. ", 20),
        Current: session.AccessState{Mode: "copilot"}, MissingSystem: true}
}
func press(p *Panel, key string) tea.Cmd {
    code := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape,
        "tab": tea.KeyTab, "down": tea.KeyDown, "pgdown": tea.KeyPgDown}[key]
    return p.Update(tea.KeyPressMsg{Code: code})
}
func TestAccessPanelDefaultsToDenyAndEscDenies(t *testing.T) {
    p := New(request())
    c := press(p, "enter")().(ChoiceMsg).Choice
    if c.Approved || c.RequestID != "r1" || c.Current.Mode != "copilot" { t.Fatalf("%+v", c) }
    press(p, "down")
    if c = press(p, "esc")().(ChoiceMsg).Choice; c.Approved { t.Fatal("esc approved") }
}
func TestAccessPanelChoicesMatchCapabilities(t *testing.T) {
    for _, editing := range []bool{false, true} {
        for _, system := range []bool{false, true} {
            r := request(); r.MissingEditing, r.MissingSystem = editing, system
            p := New(r)
            want := 1
            if editing { want = 4 } else if system { want = 2 }
            if len(p.options) != want { t.Fatalf("%t/%t: %d", editing, system, len(p.options)) }
            if want == 1 { continue }
            press(p, "tab")
            c := press(p, "enter")().(ChoiceMsg).Choice
            if !c.Approved || (c.Mode != "") != editing || c.Current != r.Current { t.Fatalf("%+v", c) }
        }
    }
}
func TestAccessPanelIgnoresPasteAndText(t *testing.T) {
    p := New(request())
    for _, msg := range []tea.Msg{tea.PasteMsg{Content: "enter auto"},
        tea.KeyPressMsg{Code: 'y', Text: "y"}, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}} {
        if cmd := p.Update(msg); cmd != nil || p.selected != 0 { t.Fatal("input changed decision") }
    }
}
func TestAccessPanelFitsBudgetsAndMonochrome(t *testing.T) {
    previous := theme.Current()
    t.Cleanup(func(){ theme.Reload(previous) })
    for _, mono := range []bool{false, true} {
        theme.Reload(theme.LoadFor(mono, "xterm-256color"))
        for _, width := range []int{80, 120, 200} {
            for _, height := range []int{6, 9, 16, 24} {
                p := New(request())
                out := p.View(width, height)
                if lipgloss.Height(out) > height { t.Fatalf("height %d", height) }
                for _, line := range strings.Split(out, "\n") {
                    if ansi.StringWidth(line) > width { t.Fatalf("width %d", width) }
                }
                plain := ansi.Strip(out)
                for _, text := range []string{"Allow system access?", "Current: copilot",
                    "mode unchanged", "this session only", "no sudo", "Deny", "Enter decide", "Esc deny"} {
                    if !strings.Contains(plain, text) { t.Fatalf("missing %q: %s", text, plain) }
                }
                if mono && strings.Contains(out, "\x1b[") { t.Fatal("monochrome emitted escape") }
            }
        }
    }
}
func TestAccessPanelDetailScrollKeepsDecisionAndIdentity(t *testing.T) {
    p := New(request()); before := ansi.Strip(p.View(80, 9))
    press(p, "pgdown"); after := ansi.Strip(p.View(80, 9))
    if before == after || p.selected != 0 || p.Request() != request() { t.Fatal("detail scroll changed selection or did not scroll") }
    p.View(120, 16)
    if p.selected != 0 || p.Request().RequestID != "r1" { t.Fatal("resize reset identity") }
}
```

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/tui/accesspanel -count=1
```

Expected: all tests pass; readable at 80 columns and six dock rows, also without color.

## Task 6: Reconcile and resolve access panels in the TUI

**Goal:** the next runtime update opens the right panel without typing, and only that panel's exact request can be resolved.

**Files:** `internal/app/tui/model.go`, `view.go`, `status.go`, `system_access_test.go`, `approval_mode_test.go`, `model_test.go` (all under internal/app/tui).

**Steps**

1. Import accesspanel in model.go. Add Model fields with the exact callback types in WithAccessRequests/WithSessionModeSetter from Contracts, named `accessRequest`, `resolveAccess` and `sessionModeSetter`. Implement the option functions by assigning these fields; neither option derives policy or mutates State.
2. Rename the existing `Model.Update` body to private `update` without changing its existing message routing. Add this complete wrapper. Reconciliation before and after the existing update covers runtime early returns and every dock-close branch without patching each branch or using defer on a value receiver:

```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    m.reconcileAccess()
    hidden := m.rawWidth < minTerminalWidth || m.rawHeight < minTerminalHeight
    if hidden {
        if _, pending := m.state.AccessRequest(); pending {
            switch key := msg.(type) {
            case tea.KeyPressMsg:
                if key.String() != "ctrl+c" { return m, nil }
            case tea.PasteMsg, accesspanel.ChoiceMsg:
                return m, nil
            }
        }
    }
    if choice, ok := msg.(accesspanel.ChoiceMsg); ok {
        m.handleAccessChoice(choice.Choice)
        m.reconcileAccess()
        return m, nil
    }
    updated, cmd := m.update(msg)
    switch next := updated.(type) {
    case Model:
        next.reconcileAccess()
        return next, cmd
    case *Model:
        next.reconcileAccess()
        return next, cmd
    default:
        return updated, cmd
    }
}
```

3. Implement private `reconcileAccess()` on *Model, with no returned command:
   - Read through accessRequest if present; otherwise read State.AccessRequest only to fail closed: CancelAccess(id, AccessFailed, "Access resolver unavailable") when a typed request exists. A missing resolveAccess callback does the same.
   - Reject a returned snapshot whose SessionID differs from m.state.SessionID; it belongs to a replaced conversation and must not open here.
   - If the current dock panel is *accesspanel.Panel, close it only when its ID/session is no longer pending. If the same request has a different current tuple/missing flags, replace with accesspanel.New(fresh), resetting Deny. Keep the same pointer/selection when the entire snapshot is unchanged.
   - If another dock panel is open, retain it. If free, pending, and at supported raw dimensions, call dock.Open(accesspanel.New(snapshot)). Do not overwrite pickerCommand, input content or input focus.
   - Synchronize m.approvalMode from State.AccessState().Mode, and update viewport height/refresh when panel or displayed permission state changes. This is presentation synchronization, not policy application.
4. Implement private `handleAccessChoice(choice session.AccessChoice)`:
   - Require an open access panel with exactly matching RequestID/SessionID/Current. Ignore stale messages without closing an unrelated/newer panel.
   - Call resolveAccess synchronously; it is a bounded memory-only operation. Never call pending.Respond, SetSystemAccess, setMode or persistAndReload in this path.
   - AccessRefresh retains pending; read the fresh snapshot and replace the matching panel with Deny selected. For final grant/deny/available/failure/expired, close only the matching access panel, sync mode from result.Effective when this still owns the session, and append one concise system note naming actual mode/system state and outcome. Errors name failure, never success. Reconciliation closes canceled panels.
5. Remove the `picker.PickedMsg` `cmdName == "mode-elevation"` branch and its matching picker.CancelledMsg approval/clear branch. Remove `modeRequestWantsSystem`. Retain `isModeElevationApproval` for the existing forced-prompt classification in approval.go, but update its comment: it classifies ordinary forced prompts, not dock ownership.
6. In `handleApproval` remove the generic mode-elevation picker-opening block. Typed Access requests are handled by reconciliation and must never create a generic approval model or consume runtime events. A raw untyped Name == "mode.request" fails closed with ordinary denial/cleanup and an explanatory note; it never switches mode. Ordinary file.write requests with a mode-elevation reason remain ordinary forced approvals, keeping approval.go's no-always/session rule.
7. Change the suppression branches in `inputChromeRows` and `renderInputArea` to use `tc.Access != nil`, not isModeElevationApproval. For a typed access request queued behind an unrelated dock panel show a muted `Access request waiting — close the current panel to review.` row, count its row budget, and retain the main draft. Do not render a second approve/deny form. In `statusRightSegment` show `Access waiting` while queued so FullFrame panels also disclose it; retain the existing interrupt/notice priority. While accesspanel owns the dock its footer supplies its keys.
8. In `setMode` call sessionModeSetter when provided; on error add a failure note, leave display unchanged and return. Without it, retain the existing structural runner setters but pass them as the infallible callback to State.SetSessionMode; a nil runner uses a nonnil no-op callback. Remove direct Config.Agent.ApprovalMode assignment. Retain the existing quiet Plan skill-load behavior after successful manual mode change.
9. In `applyNewConfig` replace direct State.Config assignment with RebaseSessionConfig(cfg), then read the effective mode/config from State. This prevents persistAndReload's final presentation refresh from overwriting a runtime-preserved override. Retain transcript/theme invalidation and settings' explicit persistence semantics.
10. Rewrite existing `TestModeElevationSystemRequestGrantsFlag`, `TestModeElevationPlainRequestDoesNotGrantSystem` and `TestModeElevationHidesApprovalPanel` to register typed requests with State.RegisterAccess, inject both callbacks, and drive agentTickMsg/ChoiceMsg. Use the existing tests' session/test model helpers; do not inject an unrelated key to open the panel. Remove obsolete picker imports.
11. Add named tests to existing TUI test files:
    - `TestAccessPanelAppearsOnRuntimeTickAtSupportedSizes`: 80×24,120×40,200×60 show system-specific panel after agentTickMsg with no key; exactly one decision UI.
    - `TestAccessPanelQueuesBehindOtherDockAndPreservesDraft`: use existing settings BrowserPanel and a FullFrame doc/inspector fixture; both remain open, status says waiting, closing opens access without changing draft/focus.
    - `TestAccessPanelKeepsIdentityUntilPermissionRefresh`: ticks and resize keep pointer/selection; changed mode/system refreshes to Deny with fresh tuple.
    - `TestAccessPanelIgnoresExpiredAndForeignChoice`: old request message after cancellation/replacement cannot grant or close a newer/other panel.
    - `TestAccessPanelHiddenSizeCannotApprove`: shrink below 80×24; Enter, paste and queued ChoiceMsg do nothing; resize reveals the panel and requires a visible decision.
    - `TestAccessPanelEscDeniesWithoutStoppingTurn`: Esc command result settles denial with actual state; busy/cancel handle and draft remain intact.
    - `TestAccessPanelGrantOrderingAndNoPersistence`: callback barrier commits before the waiting channel receives; save/reload counters stay zero and actual current mode remains Copilot for system-only.
    - `TestAccessPanelUnavailableResolverFailsClosed`: omitted get or resolve callback unblocks false and never reports success.
    - `TestAccessOrdinaryElevationReasonUsesForcedApproval`: file.write reason-prefix case keeps ordinary approval without always/session options and never opens accesspanel.
    - `TestAccessManualModeSetterAndReloadStayInSync`: injected setter receives manual selection once; applyNewConfig preserves unchanged-baseline override and resets on configured-mode change.
12. Run existing ordinary/child approvals, Ctrl+C, manual mode/status and both postmortem authorization tests unchanged. Do not combine the exit authorization with interactive mode.request.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/tui -run 'Access|Mode|Approval|CtrlC|Postmortem' -count=1
```

Expected: no keyboard nudge; no stale grant, invisible approval, lost draft, generic system picker or access-induced save/reload.

## Task 7: Connect the TUI to the runtime resolver

**Goal:** production TUI uses the same validated access and manual-mode operations tested in app.

**Files:** `internal/app/app.go`, `internal/app/app_test.go`.

**Steps**

1. Add this complete private helper in app.go and, in `Run` where tuiOpts already receive WithConfigReloader/WithRunnerSource, append `tuiOpts = append(tuiOpts, accessTUIOptions(rt)...)`. Bound methods read the current rt.State/Runner under the runtime gate, so a successful NewSession does not retain a closure over the old State.

```go
func accessTUIOptions(rt *Runtime) []tui.Option {
    return []tui.Option{
        tui.WithAccessRequests(rt.AccessRequest, rt.ResolveAccess),
        tui.WithSessionModeSetter(rt.SetSessionMode),
    }
}
```

2. Add `TestAccessTUIOptionsResolveWithoutConfigReload` in app_test.go. Construct an injected Runtime fixture and `tui.New(rt.State, accessTUIOptions(rt)...)`; register a typed system-only request on that known State, update with public WindowSizeMsg{Width:80,Height:24}, inspect View for the system-access title, and submit an exported accesspanel.ChoiceMsg bound to State.AccessRequest. The pending channel must contain a typed committed result; config reloader/save fixtures remain unused. No TUI-private fields or private messages are needed.
3. Add `TestAccessTUIOptionsFollowNewSession`: capture the helper's options, successfully call rt.NewSession using the existing app lifecycle fixture, and build the replacement tui.Model with those same options. Register a new request there and assert resolution commits to the replacement State; the old exported ChoiceMsg cannot change it. The pre-swap request must be canceled once.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app -run 'AccessTUIOptions|RuntimeAccess' -count=1
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go build -o /private/tmp/marshal-access-approval ./cmd/marshal
```

Expected: production options are wired, grant is in memory only, and replacement sessions do not receive old decisions.

## Task 8: Add collect-only access permission requests to ACP

**Goal:** ACP exposes additive typed scope/choice metadata and never delivers a capability approval from the bridge itself.

**Files:** `internal/acp/permissions.go`, `internal/acp/permissions_test.go`.

**Steps**

1. Add these complete wire declarations to permissions.go. They intentionally duplicate the JSON contract rather than aliasing session structs. Add `ModeAccess *ModeAccessRequest` to PermissionRequest and `ModeAccess *ModeAccessChoice` to PermissionDecision, both tagged `json:"modeAccess,omitempty"`. Outer PermissionDecision.Approved remains the decision; ModeAccess carries identity/presentation/variant only.

```go
type ModeAccessState struct {
    Mode   string `json:"mode"`
    System bool   `json:"system"`
}
type ModeAccessRequest struct {
    RequestID      string          `json:"requestId"`
    SessionID      string          `json:"sessionId"`
    Actor          string          `json:"actor,omitempty"`
    StepID         int64           `json:"stepId,omitempty"`
    Reason         string          `json:"reason"`
    Current        ModeAccessState `json:"current"`
    MissingEditing bool            `json:"missingEditing"`
    MissingSystem  bool            `json:"missingSystem"`
}
type ModeAccessChoice struct {
    RequestID string          `json:"requestId"`
    SessionID string          `json:"sessionId"`
    Current   ModeAccessState `json:"current"`
    Mode      string          `json:"mode,omitempty"`
}

func (b *PermissionBridge) CollectAccess(ctx context.Context, sessionID, toolCallID string, request session.AccessRequest) (session.AccessChoice, error) {
    if sessionID != request.SessionID || request.RequestID == "" {
        return session.AccessChoice{}, errors.New("acp: invalid access request identity")
    }
    scope := "System access; interaction mode stays unchanged."
    if request.MissingEditing {
        scope = "Editing capability."
        if request.MissingSystem { scope = "Editing capability and system access, as one bundle." }
    }
    disclosure := "This conversation only. Interaction mode, policy and sandbox still apply."
    if request.MissingSystem {
        disclosure += " Filesystem access outside the workspace and relaxed workspace shell guardrails; no sudo."
    }
    wire := &ModeAccessRequest{
        RequestID: request.RequestID, SessionID: request.SessionID,
        Actor: request.Actor, StepID: request.StepID, Reason: request.Reason,
        Current: ModeAccessState{Mode: request.Current.Mode, System: request.Current.System},
        MissingEditing: request.MissingEditing, MissingSystem: request.MissingSystem,
    }
    decision, err := b.client.RequestPermission(ctx, PermissionRequest{
        SessionID: sessionID, ToolCallID: toolCallID, ToolName: "mode.request",
        Risk: "read_only",
        Reason: fmt.Sprintf("%s\nCurrent mode: %s; system access: %t.\n%s\n%s",
            request.Reason, request.Current.Mode, request.Current.System, scope, disclosure),
        ModeAccess: wire,
    })
    if err != nil { return session.AccessChoice{}, err }
    choice := session.AccessChoice{
        RequestID: request.RequestID, SessionID: request.SessionID,
        Current: request.Current, Approved: decision.Approved,
    }
    if decision.ModeAccess != nil {
        echoed := decision.ModeAccess
        if echoed.RequestID != request.RequestID || echoed.SessionID != request.SessionID {
            return session.AccessChoice{}, errors.New("acp: access choice identity mismatch")
        }
        choice.Current = session.AccessState{Mode: echoed.Current.Mode, System: echoed.Current.System}
        choice.Mode = echoed.Mode
    } else if choice.Approved && request.MissingEditing {
        choice.Mode = decision.Edited
        if choice.Mode == "" { choice.Mode = "edit" }
    }
    // Legacy Edited is ignored when no editing selector was presented.
    // Validation/application and canonical channel delivery belong to runtime.
    return choice, nil
}
```

2. Add `fmt` to permissions.go imports. In existing PermissionBridge.Request, after nil pending validation and before ResponseChan validation, reject pending.Access != nil with `errors.New("acp: access requests require CollectAccess and runtime resolution")`. Keep ordinary command and skill-gate delivery unchanged.
3. Add tests to permissions_test.go:
   - `TestCollectAccessTypedWireAndChoice`: JSON round trip of both optional payloads and exact identity/current/missing fields; outer Approved controls approval.
   - `TestCollectAccessNeverAcknowledgesPendingTool`: a canonical access waiter remains blocked after collection; only subsequent runtime resolution delivers.
   - `TestCollectAccessLegacySelectionIsScopeBound`: missing editing + empty Edited defaults Edit; system-only ignores Edited Auto/arbitrary shell text and leaves choice.Mode empty; denial selects no new mode.
   - `TestCollectAccessDisclosureSurvivesLegacyClient`: ToolName/Reason explicitly identify system scope, unchanged mode, lifetime and no sudo without requiring modeAccess support.
   - `TestCollectAccessRejectsForeignIdentityAndClientError`: mismatch/error yields no applied decision and no send/close.
   - `TestPermissionBridgeRejectsTypedAccessDelivery`: old Request method cannot acknowledge typed access even with a response channel.
4. Retain existing `TestPermissionApprovalResolvesPendingToolCall`, edited-command and context-cancel tests for ordinary approvals.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/acp -run 'CollectAccess|PermissionBridgeRejectsTyped|PermissionRequest|PermissionEdited' -count=1
```

Expected: additive wire shape, legacy compatibility, and zero capability-channel delivery during collection.

## Task 9: Resolve ACP access through the shared runtime

**Goal:** ACP waits for the same application contract as TUI and broadcasts only actual committed state.

**Files:** `internal/acp/turn.go`, `host.go`, `turn_test.go`, `permissions_test.go`.

**Steps**

1. Add the AccessRequest/ResolveAccess callbacks to TurnRuntime as declared in Contracts. In host.go's `Lookup` TurnRuntime literal bind `AccessRequest: rt.AccessRequest` and `ResolveAccess: rt.ResolveAccess`. Replace its inline SetMode closure with `SetMode: rt.SetSessionMode`; keep explicit SetSystemAccess binding for manual RPC semantics. Remove imports only if now unused.
2. Add private `func (m *TurnManager) resolveAccessRequest(ctx context.Context, sessionID string, rt *TurnRuntime, requestID, toolCallID string) error` in turn.go:
   - Require State and both callbacks. Missing dependency or bridge cancels the matching request with AccessFailed; if no State exists, return an error so the turn cancels and its original ctx unblocks. Never synthesize a typed grant.
   - Read fresh AccessRequest server-side on every iteration. Require the original requestID/session; missing/replaced ends without touching anything newer.
   - Call bridge.CollectAccess using that snapshot, then rt.ResolveAccess. For AccessRefresh repeat against fresh state; do not replay the old choice.
   - Collection errors/invalid typed identity cancel that exact request with AccessFailed and return error, preserving existing client-error cancellation behavior.
   - Runtime validation errors already settle nonapproved; return after settlement, without a mode_changed success notification. Expired choice is a harmless end. Grant/deny/available are final; only grant sends `session/update` with `{"kind":"mode_changed","mode": result.Effective.Mode,"system_access": result.Effective.System}`.
   - If a refreshed snapshot has no missing capability, call ResolveAccess once with its exact Current, Approved:true and empty Mode to settle AccessAvailable without another question. No setter runs and no grant notification is sent.
3. In runTurn alongside `turnAnswered`/`turnGateAnswered` add turn-local `var accessAsked sync.Map`. For a pending event with Access != nil, use request ID as LoadOrStore key and launch only one resolveAccessRequest goroutine. Do this **before** the generic bridge-nil branch. Use canonical callbacks/State.CancelAccess; never pa.Respond on an access event copy. Runtime errors that signal client/transport failure invoke existing slotCancel/subCancel as ordinary bridge failures do.
4. Leave the ordinary pending branch on PermissionBridge.Request. Delete the old `if pa.Name == "mode.request" && decision.Approved` application block entirely. No fallback to Edited or duplicate SetMode/SetSystemAccess remains. Delete the now-unused `modeRequestWantsSystem` helper. Keep manual TurnManager.SetMode's existing explicit true/false/absent system_access behavior, now backed by the shared session-mode override.
5. Rewrite `TestPromptTurnAppliesModeElevation` and `TestModeRequestSystemElevationApplied`: register real typed session requests, inject callbacks that perform State.ResolveAccess with a recording prepare, and assert the committed state/result. Event copies intentionally have no ResponseChan.
6. Add named tests to turn_test.go/permissions_test.go:
   - `TestACPAccessCommitsBeforeToolAcknowledgment`: recording Apply barrier; no tool response before release, then actual state and one mode_changed.
   - `TestACPAccessLegacySystemOnlyPreservesEveryMode`: five-mode table; legacy approved/Edited never switches a system-only request.
   - `TestACPAccessCombinedLegacyAndTypedVariants`: both client styles select Edit/Copilot/Auto only when editing was displayed, granting the full bundle.
   - `TestACPAccessChangedTupleReasksFreshQuestion`: change permission pair while client waits; first answer refreshes with no mutation, next prompt carries new tuple/flags and resolves once.
   - `TestACPAccessDuplicateEventsAndLateResponses`: one client question per live request, one commit; old provider call ID reused with new request ID remains safe.
   - `TestACPAccessMissingBridgeOrResolverFailsClosed`: both cases unblock false without mode/flag changes.
   - `TestACPAccessClientFailureAndCancellationSettleCanonical`: no event-copy response/close; matching canonical request canceled once, later request untouched.
   - `TestACPAccessInvalidEditingVariantCannotPartiallyGrantSystem`: malformed typed/legacy mode settles failure, keeps both old capabilities.
   - `TestACPManualModeUsesSessionOverride`: manual SetMode then unrelated config reload retains the manual override; explicit configured-mode change resets it.
7. Rerun ordinary permissions, skill gates, manual mode true/false/absent system flag, turn cancellation and no-bridge denial regressions.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/acp -run 'Access|Mode|Permission|SkillGate|ForwarderDenies' -count=1
```

Expected: application precedes tool acknowledgment for both ACP client styles; ordinary bridge behavior stays compatible.

## Task 10: Publish the tool contract and run final acceptance checks

**Goal:** documentation describes the shipped semantics and the complete feature passes hermetic project checks alongside the background implementation.

**Files:** `docs/tools/workspace-interaction.md`, `docs/system-access-approval/feature-spec.md`, this plan. Production code changes belong in their preceding package task if a check fails.

**Steps**

1. Replace the entire `## mode.request` section in workspace-interaction.md, up to `## tools.select`, with this exact text:

```markdown
## mode.request

**For:** asking the user for missing editing capability, system access, or both, for the current conversation.

**Surface:** native tool, risk `read_only`. Read-only classification does not auto-approve an access request, including in Auto mode.

| Arg | Required | Notes |
|---|---|---|
| `mode` | conditional | Editing hint: `edit`, `copilot` or `auto`. The user chooses the actual editing variant. |
| `system` | conditional | `true` requests system access. Defaults false. |
| `reason` | no | Plain-language purpose, at most 400 Unicode characters. |

Supply a valid mode or `system:true`. An explicitly supplied empty/unknown mode is invalid. For system access alone, use `{"system":true,"reason":"Read the requested files outside the workspace."}`.

The request displays only missing capabilities. Already in Edit/Copilot/Auto, a mode hint does not switch variants; a system-only grant preserves the mode, including Plan/Default. Already-satisfied requests return without a prompt.

The app runtime validates and applies the session grant before the canonical tool waiter receives a typed result. The TUI and ACP collect decisions; they do not persist grants or rebuild the runtime. Results carry the actual effective mode/system state and distinguish grant, denial, failure, cancellation and already-available access. Ordinary Edited command text is not an internal access grant.

System access permits filesystem scope outside the workspace and relaxes workspace shell guardrails, subject to interaction mode, per-tool policy and sandbox. It does not grant sudo. New/resumed conversations start without the grant; unrelated in-conversation config reload preserves a temporary mode override, while an explicit configured-mode change replaces it.

Only the top-level conversation can request escalation. A child must ask its parent. Denial preserves capabilities already held; cancel/shutdown and stale responses cannot grant a later request.
```

2. Update feature-spec Implementation status only after actual checks pass. Record the final implementation base/commit, commands and any concrete remaining limitation. Commit public spec/plan/tool documentation with the implementation; do not force-add ignored archives or unrelated files.
3. Run the final commands below once after all implementation tasks. Fix each failure in the responsible package, rerun its narrow check, then repeat only the affected broader checks. Confirm `gofmt -l` produces no output for touched Go files.
4. Confirm the final diff contains no access-grant call to persistAndReload/config save/reload, no TUI agent import, no web internal import, no schema migration/persisted system flag, and no change to background explicit-termination/exit ordering. These are final review assertions, not new implementation tasks.

**Verify**

```sh
gofmt -l internal/app/session/pending.go internal/app/session/session.go internal/app/session/session_test.go internal/app/runtime.go internal/app/app.go internal/app/runtime_test.go internal/app/app_test.go internal/tools/native/mode_request.go internal/tools/native/native.go internal/tools/native/mode_request_test.go internal/app/tui/accesspanel/accesspanel.go internal/app/tui/accesspanel/accesspanel_test.go internal/app/tui/model.go internal/app/tui/view.go internal/app/tui/status.go internal/app/tui/system_access_test.go internal/app/tui/approval_mode_test.go internal/app/tui/model_test.go internal/acp/permissions.go internal/acp/permissions_test.go internal/acp/turn.go internal/acp/host.go internal/acp/turn_test.go
git diff --check
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/app/session ./internal/acp -count=1
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./...
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go vet ./...
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go build -o /private/tmp/marshal-access-approval ./cmd/marshal
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-access-approval GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./test/usability/... -run TestScripted -v -timeout 5m
```

Expected: formatting/diff checks clean; race, full suite, vet, binary build and scripted usability pass. Default suite remains isolated from developer config, keychain and live services. Do not enable live-credential flags or integration tags to fix a unit test.

## Integration and handoff

- Background execution owns job cancellation, explicit termination and exit stop/join ordering. This change owns permission application and must never reload/replace managers during a grant. Rebase Task 3's JobManager test to the equivalent managed-execution API if that implementation lands first; retain its same-ID/captured-settings assertions.
- TUI remains rendering/choice collection only. ACP wire additions are optional. No web source change is required: legacy clients receive a full textual disclosure and scope-bound interpretation of Approved/Edited.
- Existing explicit settings saves keep their semantics. The override solves reload/session lifetime; this plan does not redesign how settings serialize a user-edited config.
- Stop/join active work before the optional exit-postmortem prompt remains the background plan's contract. The existing postmortem pass has its own scoped authorization; an interactive access request neither grants nor revokes that path.
- The executor should read the approved spec plus Contracts and the current task, apply code/test edits, run that task's checks, and commit before proceeding. Review fixes stay in that package's task. A changed product requirement goes back to the user instead of being silently invented.

## Authoring validation

This section records plan checks, not completion of the feature. No production implementation, commit or full-project verification has been performed during plan authoring. Exact embedded implementation code is checked in a temporary Go overlay; the package-scoped behavior and race tests named above are execution requirements.

Authoring checks passed:

- The complete accesspanel implementation and its five embedded tests compiled and passed in a temporary overlay onto the existing gatepanel directory. This avoids creating a production package directory just to check plan snippets; execution creates the actual accesspanel package.
- The complete native replacement compiled against temporary session API declarations. A temporary test compiled its schema and checked valid legacy/system-only arguments, rejected malformed/empty/null arguments, and accepted 400/rejected 401 Unicode characters.
- The ACP DTO/CollectAccess code compiled against current dependencies; temporary legacy selection checks passed for system-only and editing requests.
- The exact TUI Update wrapper and app options helper compiled in temporary overlays against contract stubs. These were compile-only checks with vet disabled because the new panel directory exists only virtually in that overlay; execution still requires the real-package tests and full vet command above.
- Plan structure, local links, code-fence balance, existing anchors and unchanged production source were checked. Full session/runtime/TUI/ACP behavior, races, build and full-suite checks remain requirements for execution, not authoring claims.
