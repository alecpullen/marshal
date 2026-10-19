# Ask/Act implementation plan

Status: ready for plan review; implementation has not started.
Date: 2026-10-19.
Base: `bdf69de4e545d8b8516926ad357bdf941fc84467`.
Approved design: [feature spec](../feature-spec.md).
Workflow: `marshal-writing-plans`, then `marshal-executing-plans` after this
plan receives separate approval. Tasks run sequentially.

## Goal, scope, and execution rules

Replace the five interaction modes with Ask/Act and independent Auto/System
state, and replace shell substring guardrails with parsed operation policy.
Complete the same migration through configuration, children, watches, pipeline
roles, TUI, ACP, bridge, and browser UI. The feature spec's decision tables are
normative; this plan pins implementation choices and verification seams.

No new planning workflow, third mode, per-file approval preference, database
grant restoration, sudo transport, or sandbox backend is included. Ordinary
redirection, `tee`, and single-file `cp`/`mv`, including `-f`, are routine edits.
Named destructive primitives such as `truncate`, `shred`, and `dd of=...`,
recursive removal, and Git discard/bulk operations use the destructive policy.

The inspected tree contains unrelated uncommitted work, including ACP provider
configuration, model discovery, postmortems, and other design documents. Leave
it intact. Compare HEAD and this plan's named anchors before execution; adapt
line positions, not decisions. All source paths below are repository-relative.
New tests are explicitly called **new**; existing anchors are implementation
symbols, not promises that the proposed tests already exist.

Run each task's verification and commit that task before continuing, using
`interaction-modes: task N — <title>`. Do not stage unrelated files. Temporary
old API adapters may keep unmigrated callers compiling: they project from the
canonical tuple and never own another grant. Remove them in Tasks 45–47.
When adding a replacement API, retain its old signature until its consumers
have migrated. Compatibility behavior survives only at the named input
boundaries in the finished feature. Update changed-behavior fixtures in the
owning package with its task; do not preserve obsolete expectations through
hidden legacy behavior.

Tests use `config.Default()`, owned temporary directories, isolated auth and
fake runners. Classification strings are data: never execute destructive
examples or real sudo. Use channel barriers for races, not sleeps. Do not
enable integration/live credential flags. Existing config/app/TUI/native/ACP
test entry points already isolate the environment. Add `testenv.Run` to a new
package TestMain only when its tests resolve environment/config paths.

## Contracts

These declarations fix shared APIs. Except for the complete file in Task 2,
Go blocks are declaration contracts: method bodies belong to their named
implementation task. Imports belong in the actual owning files. Unexported
algorithm helpers and tests within one task do not create cross-task APIs.

### Canonical authority

New package `internal/interaction` imports only the standard library. Its
complete types and pure functions are in Task 2. In particular:

```go
type Mode string // only ModeAsk and ModeAct
type Defaults struct { Mode Mode; Auto bool }
type State struct {
    Mode Mode `json:"mode"`
    Auto bool `json:"auto"`
    System bool `json:"system_access"`
    Revision uint64 `json:"access_revision"`
}
type Patch struct {
    Mode *Mode `json:"mode,omitempty"`
    Auto *bool `json:"auto,omitempty"`
    System *bool `json:"system_access,omitempty"`
}
type Ceiling struct { Act, Auto, System bool }
type Snapshot struct {
    OwnerSessionID string
    ActorSessionID string
    WorkspaceRoot string
    Access State
}
type AdmissionLease interface { Release() }
type Authority interface {
    Snapshot() Snapshot
    BeginAdmission(context.Context, Snapshot) (AdmissionLease, error)
}
```

Owner revision starts at 1. Effective tuple changes increment it once;
no-op patches do not. Every successful runtime config install increments it
once, even for unrelated config, without revoking overrides or System.
Workspace changes and session-rule changes increment the same revision. There
is no second permission epoch. Existing job generations retain their separate
lifecycle meaning.

Session APIs, implemented in Tasks 6–8:

```go
func (s *State) InteractionState() interaction.State
func (s *State) ApplyInteraction(interaction.Patch) (interaction.State, error)
func (s *State) InteractionForConfig(config.Config) (interaction.State, error)
func (s *State) RebaseInteractionConfig(config.Config) (interaction.State, error)
func (s *State) InvalidateInteraction() interaction.State
func (s *State) InteractionAuthority() interaction.Authority
func WithInteractionOwner(*State, interaction.Ceiling) Option
func (s *State) PolicySnapshot() (config.Config, []string, uint64)

// App facade; the only mutation callback supplied to clients.
func (rt *Runtime) SetInteraction(interaction.Patch) (interaction.State, error)
```

Rendering and prompt APIs shared by later tasks:

```go
// internal/app/tui: runtime callback only; no policy fallback.
func WithInteractionSetter(func(interaction.Patch) (interaction.State, error)) Option

// internal/agent: replace SystemPromptOptions.Mode and .SystemAccess with:
Access interaction.State

func BuildSystemPromptWithMode(role AgentRole, tools []registry.Tool, deferred []registry.Tool, skillIndex *skills.Index, activeSkills []string, nativeTools bool, access interaction.State) schema.ChatMessage
func BuildSystemPromptWithAddendum(role AgentRole, tools []registry.Tool, deferred []registry.Tool, skillIndex *skills.Index, activeSkills []string, nativeTools bool, access interaction.State, addendum string, workingDir string, roster string, loadedNames ...string) schema.ChatMessage
```

The prompt wrappers retain their names to reduce caller churn, but accept no
legacy enum. Production BuildSystemPromptWithSystem uses SystemPromptOptions.
Its turn-start Access is fixed for the turn; fresh effective access appears in
the working tail. Default convenience builders use canonical Act/Auto off.

Each child State captures its ultimate owning State at construction. Its
authority reads that owner's current state and its own active workspace,
bounded by immutable intersected ceilings. A read-only role has Act=false;
an explicit child Auto=false narrows Auto; System=true in a ceiling requires
the child's explicit System request and still depends on live owner System.
No view can widen the parent. Create a separate actor State for a role rather
than changing the shared conversation's tuple. A root state owns config and
session rules; `PolicySnapshot` returns deep copies of both and the revision
under the same transaction. Never capture `rt.State` through a closure that
could retarget an old child to a replacement conversation.

Lock order is `reloadMu -> owner.accessMu -> owner.mu/workMu`, and admission
uses `owner.accessMu -> JobManager.mu -> existing lifecycle leaf gate`.
Acquire no admission lease from inside manager/policy/registry/State locks.
Leases end before notifications, callbacks, I/O, handler execution, and waits.

For ordinary tools, admission linearizes at a small dispatch commit immediately
before the handler. For managed commands, it linearizes at the existing
`JobManager.Launch` accepted-record insertion, not at later PID publication.
A command accepted first may start its goroutine after Ask; an unaccepted
command must revalidate. Preserve its captured runner, cwd, identity, command,
settings, and stop ownership. Do not hold a lease through `Run` or `Wait`.

### Configuration boundaries

Final fields and helper declarations:

```go
// config.AgentConfig
Mode interaction.Mode `toml:"mode"`
Auto bool `toml:"auto"`

// config.fileAgent: deprecated input exists only in the file mirror.
Mode *interaction.Mode `toml:"mode"`
Auto *bool `toml:"auto"`
ApprovalMode *string `toml:"approval_mode"`

// routing.CustomAgent: optional narrowing, not an independent grant.
Mode *interaction.Mode `toml:"mode,omitempty"`
Auto *bool `toml:"auto,omitempty"`

// config.fileCustomAgent: mirror all current fields, preserving presence.
type fileCustomAgent struct {
    Name string `toml:"name"`
    Preset string `toml:"preset"`
    SystemPrompt string `toml:"system_prompt,omitempty"`
    ToolDenylist []string `toml:"tool_denylist,omitempty"`
    Mode *interaction.Mode `toml:"mode,omitempty"`
    Auto *bool `toml:"auto,omitempty"`
    ApprovalMode *string `toml:"approval_mode,omitempty"`
    MaxIterations int `toml:"max_iterations,omitempty"`
    MaxIterationsUnlimited bool `toml:"max_iterations_unlimited,omitempty"`
    Context routing.ContextBudget `toml:"context,omitempty"`
    Temperature *float64 `toml:"temperature,omitempty"`
    Thinking string `toml:"thinking,omitempty"`
}

func (cfg Config) InteractionDefaults() (interaction.Defaults, error)
func Clone(Config) Config
func normalizeInteractionFile(configFile, string) (configFile, []Diagnostic, error)
func canonicalFileForSave(configFile, string) (configFile, error)
func (a fileCustomAgent) runtime(string) routing.CustomAgent
func customAgentFile(routing.CustomAgent) fileCustomAgent
```

Normalize fresh copies **per layer before merge**, preserving raw input for
`hoistProjectGlobals`. Canonical mode ignores same-layer legacy mode with a
notice; ignored legacy Auto never contributes Auto. Canonical Ask synthesizes
false to clear inherited Auto. Canonical Act with absent Auto preserves lower
Auto. Legacy-only mode supplies both fields; same-layer explicit Auto overrides
the derived bool. Absent mode remains absent. Validate each cumulative merge;
`auto=true` over inherited Ask is an error, not RPC-style promotion. Retain
custom-agent replacement-by-name semantics. Report source-specific notices in
`Layers.InteractionNotices []Diagnostic`, appended by `Diagnose`.

Explicit serializers use `canonicalFileForSave`; automatic hoist does not.
Drop deprecated mode and retired shell keys on explicit save, retain authored
rules, and never serialize effective System/revision/session overrides.

### Shell facts and policy

New files in `internal/tools/policy` define the following. `DirectArgv` is
deliberately omitted: execution always uses the existing shell wrapper, so no
second executable classifier or optimization contract is needed.

```go
type ShellInput struct {
    Command, Dir, WorkspaceRoot, HomeDir string
    ReadRoots []string
    Env map[string]string
}
type ShellWord struct {
    Text, Value string
    Known, SingleField bool
}
type ShellFlag struct { Name string; Value ShellWord; HasValue bool }
type ShellScope string
const (
    ShellScopeNone ShellScope = "none"
    ShellScopeWorkspace ShellScope = "workspace"
    ShellScopeReadRoot ShellScope = "read_root"
    ShellScopeExternal ShellScope = "external"
    ShellScopeUnknown ShellScope = "unknown"
)
type ShellEffect uint16
const (
    ShellRead ShellEffect = 1 << iota
    ShellWrite
    ShellDestructive
    ShellEnvironment
    ShellPublish
    ShellPrivilege
    ShellUnknown
    ShellCatastrophic
)
type ShellTarget struct {
    Word ShellWord
    Path string
    Scope ShellScope
    Effect ShellEffect
    WorkspaceRoot, EntireRootHome, RawDisk bool
}
type ReasonCode string
const (
    ReasonRead ReasonCode = "shell.read"
    ReasonEdit ReasonCode = "shell.edit"
    ReasonBuildTest ReasonCode = "shell.build_test"
    ReasonLocalInstall ReasonCode = "shell.local_install"
    ReasonDestructive ReasonCode = "shell.destructive"
    ReasonEnvironment ReasonCode = "shell.environment"
    ReasonPublishing ReasonCode = "shell.publishing"
    ReasonWorkspaceRootDelete ReasonCode = "shell.workspace_root_delete"
    ReasonFloorFormat ReasonCode = "shell.floor.format"
    ReasonFloorRawDisk ReasonCode = "shell.floor.raw_disk"
    ReasonFloorRootHome ReasonCode = "shell.floor.root_home"
    ReasonFloorShutdown ReasonCode = "shell.floor.shutdown"
    ReasonPrivilegeWrapper ReasonCode = "shell.privilege_wrapper"
    ReasonUnknownExecutable ReasonCode = "shell.unknown_executable"
    ReasonUnknownFlags ReasonCode = "shell.unknown_flags"
    ReasonUnknownTarget ReasonCode = "shell.unknown_target"
    ReasonUnknownPayload ReasonCode = "shell.unknown_payload"
    ReasonDepthLimit ReasonCode = "shell.depth_limit"
    ReasonExplicitDeny ReasonCode = "rule.deny"
    ReasonExplicitConfirm ReasonCode = "rule.confirm"
    ReasonRequiresAct ReasonCode = "access.requires_act"
    ReasonRequiresSystem ReasonCode = "access.requires_system"
)
type PolicyReason struct {
    Code ReasonCode
    OperationID int
    Rule, Message string
}
type ShellOperation struct {
    ID int
    Start, End uint
    Executable ShellWord
    Subcommand string
    Argv []ShellWord
    Flags []ShellFlag
    Operands []ShellWord
    Directories []string
    Targets []ShellTarget
    Effects ShellEffect
    Reasons []PolicyReason
}
type ShellAnalysis struct { Input ShellInput; Operations []ShellOperation }
type CapabilityRequirement struct { Act, System bool }
type Evaluation struct {
    Decision Decision
    Required CapabilityRequirement
    Reasons []PolicyReason
    ExactCommandApproval bool
}
type ConfigSource func() (config.Config, []string, uint64)
type Input struct {
    ToolName string
    Args map[string]any
    ActorState interaction.State
    Shell *ShellAnalysis
    Targets []ShellTarget
}
func AnalyzeShell(ShellInput) (ShellAnalysis, error)
func (pe *PolicyEngine) SetConfigSource(ConfigSource)
func (pe *PolicyEngine) EvaluateInput(Input) (Evaluation, error)
var ErrStalePolicy = errors.New("policy snapshot changed")
```

Every engine and clone reads the same owner ConfigSource. Evaluate outside
the admission gate, requiring source revision == actor revision; mismatch
refreshes/retries, never evaluates old config under a new grant. Immutable
derived evaluators may be cached by revision. Message wording never controls
policy. An unknown valid operation prompts in Act/Auto; malformed syntax and
display controls are errors. Ask first rejects non-read effects, including
unknown effects, independently of allows. Collect all shell deny/confirm rule
matches before allows; mandatory matrix gates cannot be discharged by generic
allows. Plain prefixes match executable operations. Deliberate glob/regex
argument rules retain their explicit semantics.

### One-admission authorization

New `policy/admission.go` owns opaque proofs and consumes each at exactly one
boundary. Session imports no policy. Native and agent use these declarations:

```go
type Call struct {
    InvocationID, ToolName string
    Args map[string]any
    Shell *ShellInput
    Targets []ShellTarget
}
type CommandAttempt struct {
    InvocationID, ToolName string
    Input ShellInput
}
type Authorization struct {
    snapshot interaction.Snapshot
    call Call
    fingerprint [32]byte
    mu sync.Mutex
    approvedRequestID string
    consumed bool
}
type Admission struct {
    lease interaction.AdmissionLease
    authorization *Authorization
    committed bool
    release sync.Once
}
type Authorizer struct {
    engine *PolicyEngine
    authority interaction.Authority
}
func NewAuthorizer(*PolicyEngine, interaction.Authority) *Authorizer
func (a *Authorizer) Evaluate(context.Context, Call) (*Authorization, Evaluation, error)
func (a *Authorizer) ApproveExact(*Authorization, string) (*Authorization, error)
func WithAuthorization(context.Context, *Authorization) context.Context
func InvocationID(context.Context) string
func (a *Authorizer) BeginTool(context.Context, Call) (*Admission, error)
func (a *Authorizer) BeginCommand(context.Context, CommandAttempt) (*Admission, error)
func (a *Admission) Commit() error
func (a *Admission) Release()
var ErrStaleAdmission = errors.New("admission context changed")
var ErrApprovalRequired = errors.New("specific approval required")

// native.Options and toolSet: required on the final execution path.
CommandAdmission func(context.Context, policy.CommandAttempt) (*policy.Admission, error)

// native.LaunchSpec: captures exact cwd before admission; never late DirFunc.
Admit func(context.Context) (*policy.Admission, error)

// session.PendingToolCall and permission projections:
ExactCommandApproval bool
```

`InvocationID(ctx)` reads only the opaque context proof's identity, letting
native pass the same logical identity without substituting its provider call
ID. Without a proof, BeginCommand generates a fresh identity for an Allow-only
background attempt. It must not accept a caller-supplied identity as approval.

`Call.InvocationID` is a fresh logical invocation identity, not a reusable
provider tool-call ID. Proofs bind it plus owner/actor/session identity, raw
command bytes, cwd, workspace, revision, and analyzed targets/facts. Copy
mutable args/env/slices. `ApproveExact` is called only by trusted dispatch
after canonical approval settlement, with that request's identity. It cannot
approve a Deny or missing-capability result. It records one command, never
changes rules or capabilities. Begin reanalyzes against the current source,
checks facts/context/proof, then takes the owner lease; stale or changed facts
return a typed non-approved retry outcome. An absent proof may be synthesized
only for a current Allow, which lets read-only watch samples run without a
human approval channel. Confirm never becomes Allow that way.

`Commit` consumes the proof once. `Release` is idempotent and unlocks even on
failure. Ordinary tools consume immediately before handler dispatch; shell
tools pass the proof in context and consume at accepted Launch insertion.
Sudo consumes at each direct attempt's accepted dispatch; cold/password-failure
attempts do not authorize a different payload. Revision changes before a retry
require fresh validation/consent. Never acquire the gate across a password
prompt or process wait.

### Transport and browser contracts

ACP and bridge each own these wire structs; bridge imports no internal Go
packages. Effective-state booleans have no `omitempty`. Reject explicit null,
wrong types, and empty patches; fields absent from a patch remain absent.

```go
type AccessStateWire struct {
    Mode string `json:"mode"`
    Auto bool `json:"auto"`
    SystemAccess bool `json:"system_access"`
    AccessRevision uint64 `json:"access_revision"`
}
type AccessPatchWire struct {
    Mode *string `json:"mode,omitempty"`
    Auto *bool `json:"auto,omitempty"`
    SystemAccess *bool `json:"system_access,omitempty"`
}
type SetModeParams struct {
    SessionID string `json:"sessionId"`
    AccessPatchWire
}
type AccessStateParams struct { SessionID string `json:"sessionId"` }
// ACP TurnRuntime replaces separate mutable mode/System callbacks:
SetInteraction func(interaction.Patch) (interaction.State, error)
InteractionState func() interaction.State
// Bridge Registry:
func (r *Registry) SetAccess(context.Context, string, AccessPatchWire) (AccessStateWire, error)
func (r *Registry) AccessState(context.Context, string) (AccessStateWire, error)
```

`session/set_mode` returns all four fields and publishes the identical fields
in `mode_changed`. `session/new` returns actual effective fields with its
existing session/workspace metadata. New read-only `session/access_state`
returns the same full state. Bridge exposes GET/PATCH
`/api/sessions/{id}/access`; retained POST `/mode` is an ingress adapter only.
Reconnect/load fetches effective state instead of inferring it from defaults.
Capability `modeAccess.current` uses the same fields; existing camelCase
request identity fields remain unchanged. Its choice can specify only requested
normal Act/System, never Auto. General legacy set-mode migration and legacy
capability-reply translation are separate functions.

New `web/ui/src/lib/interaction.ts` owns these declarations:

```ts
export const MODES = ['ask', 'act'] as const
export type Mode = (typeof MODES)[number]
export interface AccessState {
  mode: Mode
  auto: boolean
  system_access: boolean
  access_revision: number
}
export interface AccessPatch { mode?: Mode; auto?: boolean; system_access?: boolean }
export interface InteractionDefaults { mode?: Mode; auto?: boolean }
export interface ModeAccessRequest {
  requestId: string
  sessionId: string
  actor?: string
  stepId?: number
  reason: string
  current: AccessState
  missingEditing: boolean
  missingSystem: boolean
}
export interface ModeAccessChoice {
  requestId: string
  sessionId: string
  current: AccessState
  mode?: 'act'
}
export function parseAccessState(value: unknown): AccessState
export function accessLabel(value: AccessState): string
export function migrateDefaults(value: InteractionDefaults | { mode?: string }): InteractionDefaults
```

Reducers require a full valid state and a safe nonnegative integer revision,
replace it atomically, and ignore older revisions for the same session.
Different sessions reset revision comparison. Unknown live reports raise a
visible error and retain prior state. `migrateDefaults` accepts legacy values
only for stored starting preferences; it must reject unknown values. Fresh
defaults are Act/false; only starting mode/Auto are remembered, never grants.

Child ordinary approval forwarding reuses the root event broker, without
relaying child transcript events or taking the root pending slot:

```go
// internal/app/session: new owner-tagged projection on Event.
type ApprovalSource struct {
    Owner *State
    Key PromptKey
    Pending *PendingToolCall
    Actor string
}
// Event field: ApprovalSource *ApprovalSource
```

Forward a safe display snapshot and retain canonical owner/key for context,
cause, and Respond settlement. Start one ACP child-only collector per captured
runtime, lasting until session/connection cleanup; root turn collectors skip
these tagged events. Deduplicate by owner+key and settle teardown without
approval. TUI already scans registered child pending approvals and keeps that
path.

## Task 1: Establish the implementation baseline

**Goal:** Existing failures and the starting workspace are known before product changes.

**Files:** No product files; append results to this plan's execution record.

**Steps:** Record HEAD and dirty paths; protect unrelated changes. Run the
commands below once before implementation. Record package/test names of
pre-existing failures and their output location. Do not fix unrelated failures
as part of this feature. The research-stage focused policy/permissions run
already passed; it is not a full-project baseline.

**Verify:** `CGO_ENABLED=1 go test ./...`, `CGO_ENABLED=1 go vet ./...`, and
`CGO_ENABLED=1 go build -o /private/tmp/marshal-interaction-baseline ./cmd/marshal`.
In `web/ui`, run `npm test` and `npm run build`. Expected: baseline outcomes
recorded, with no unaccounted product edits or committed generated artifacts.

## Task 2: Add the canonical interaction package

**Goal:** Pure, strict Ask/Act transition and ceiling semantics exist independently of app/policy.

**Files:** New `internal/interaction/state.go`, `internal/interaction/state_test.go`.

**Steps:** Create `state.go` with this complete implementation; tests are a
judgment task exercising the public transition table, not copies of helpers.

```go
package interaction

import (
    "context"
    "errors"
    "fmt"
    "strings"
)

type Mode string
const (
    ModeAsk Mode = "ask"
    ModeAct Mode = "act"
)
type Defaults struct { Mode Mode; Auto bool }
type State struct {
    Mode Mode `json:"mode"`
    Auto bool `json:"auto"`
    System bool `json:"system_access"`
    Revision uint64 `json:"access_revision"`
}
type Patch struct {
    Mode *Mode `json:"mode,omitempty"`
    Auto *bool `json:"auto,omitempty"`
    System *bool `json:"system_access,omitempty"`
}
type Ceiling struct { Act, Auto, System bool }
type Snapshot struct {
    OwnerSessionID string
    ActorSessionID string
    WorkspaceRoot string
    Access State
}
type AdmissionLease interface { Release() }
type Authority interface {
    Snapshot() Snapshot
    BeginAdmission(context.Context, Snapshot) (AdmissionLease, error)
}

func Default() Defaults { return Defaults{Mode: ModeAct} }
func ParseMode(raw string) (Mode, error) {
    switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
    case ModeAsk:
        return ModeAsk, nil
    case ModeAct:
        return ModeAct, nil
    default:
        return "", fmt.Errorf("invalid interaction mode %q: use ask or act", raw)
    }
}
func ValidateDefaults(d Defaults) error {
    if d.Mode != ModeAsk && d.Mode != ModeAct {
        return fmt.Errorf("invalid interaction mode %q", d.Mode)
    }
    if d.Mode == ModeAsk && d.Auto { return errors.New("Ask cannot enable Auto") }
    return nil
}
func ValidateState(s State) error {
    if err := ValidateDefaults(Defaults{Mode: s.Mode, Auto: s.Auto}); err != nil { return err }
    if s.Revision == 0 { return errors.New("interaction authority is not initialized") }
    return nil
}
func Apply(s State, p Patch) (State, error) {
    if err := ValidateState(s); err != nil { return s, err }
    if p.Mode == nil && p.Auto == nil && p.System == nil { return s, errors.New("empty interaction patch") }
    next := s
    if p.Mode != nil {
        if *p.Mode != ModeAsk && *p.Mode != ModeAct { return s, fmt.Errorf("invalid interaction mode %q", *p.Mode) }
        next.Mode, next.Auto = *p.Mode, false
    }
    if p.Auto != nil {
        if *p.Auto && p.Mode != nil && *p.Mode == ModeAsk { return s, errors.New("Ask cannot enable Auto") }
        next.Auto = *p.Auto
        if *p.Auto { next.Mode = ModeAct }
    }
    if p.System != nil { next.System = *p.System }
    if err := ValidateState(next); err != nil { return s, err }
    return next, nil
}
func Bound(s State, c Ceiling) State {
    if !c.Act { s.Mode = ModeAsk }
    s.Auto = s.Auto && c.Auto && s.Mode == ModeAct
    s.System = s.System && c.System
    return s
}
func Intersect(a, b Ceiling) Ceiling {
    return Ceiling{Act: a.Act && b.Act, Auto: a.Auto && b.Auto, System: a.System && b.System}
}
func LegacyDefaults(raw string) (Defaults, string, error) {
    switch strings.ToLower(strings.TrimSpace(raw)) {
    case "plan", "default":
        return Defaults{Mode: ModeAsk}, "replaced with Ask; planning remains a skill workflow", nil
    case "edit":
        return Defaults{Mode: ModeAsk}, "replaced with Ask; approval for every file change was retired", nil
    case "copilot":
        return Defaults{Mode: ModeAct}, "replaced with Act", nil
    case "auto":
        return Defaults{Mode: ModeAct, Auto: true}, "replaced with Act + Auto; Auto now preauthorizes destructive workspace commands", nil
    default:
        return Defaults{}, "", fmt.Errorf("invalid legacy interaction mode %q", raw)
    }
}
```

New tests: `TestApplyInteractionPatchTable` covers every command-equivalent
tuple and false/omitted semantics; `TestApplyInteractionPatchRejectsAtomically`
covers empty/invalid/Ask+Auto/uninitialized state; `TestLegacyDefaultsMapping`
covers all legacy and unknown values; `TestBoundCeilingNeverWidens` covers
intersections and revision preservation.

**Verify:** `CGO_ENABLED=1 go test ./internal/interaction -count=1`;
`gofmt -l internal/interaction` prints nothing after formatting. All tests pass.

## Task 3: Add presence-preserving custom-agent defaults

**Goal:** Routing can represent inherited, false, Ask, and Act custom-agent restrictions.

**Files:** `internal/llm/routing/types.go`, new `internal/llm/routing/interaction_test.go`.

**Steps:** At `CustomAgent`, add the optional Mode/Auto contract fields.
Retain ApprovalMode temporarily for unmigrated input conversion; do not use
new fields to grant independent authority. Add new
`TestCustomAgentInteractionPresenceRoundTrip` exercising TOML nil versus
explicit false and the existing Context/Temperature fields without loss.
No route/model selection behavior changes.

**Verify:** `CGO_ENABLED=1 go test ./internal/llm/routing -count=1` passes.

## Task 4: Normalize interaction configuration per layer

**Goal:** Fresh defaults and legacy configuration produce strict canonical starting tuples and notices.

**Files:** `internal/app/config/types.go`, `defaults.go`, `file_types.go`,
`layers.go`, `merge.go`, `diagnostics.go`, new `interaction.go`,
`interaction_test.go` in that package.

**Steps:** Add the contract fields and fileCustomAgent conversions. Change
`configFile.CustomAgents` to raw mirrors. At `LoadLayers`, normalize copied
user/project mirrors before their respective `merge` calls, validate cumulative
tuples after each merge, and retain original mirrors for `hoistProjectGlobals`.
Implement the Contracts precedence table including replacement-by-name custom
agents and source-specific diagnostics. `InteractionDefaults` validates without
fallback. Set fresh `Agent.Mode=act, Auto=false`; keep a derived old field only
for unmigrated callers. Retired shell keys load with notices and no new-policy
effect; leave raw pointers only for compatibility input. Do not remove authored
rules by comparing their text to old defaults.

New tests: `TestInteractionFreshDefaults`, `TestInteractionLegacyLayerMappings`,
`TestInteractionCanonicalFieldPrecedence`, `TestInteractionHigherAskClearsInheritedAuto`,
`TestInteractionExplicitFalsePresence`, `TestInteractionUnknownExplicitFailsLoad`,
`TestInteractionIgnoredLegacyCannotEnableAuto`,
`TestInteractionCustomAgentLayerNormalization`, and
`TestInteractionLoadDoesNotRewriteLegacyFieldsDuringHoist`. The last test
combines an actual provider/preset hoist with legacy interaction fields.

**Verify:** `CGO_ENABLED=1 go test ./internal/app/config -count=1` passes;
invalid explicit values report their source and never become Act.

## Task 5: Make explicit saves canonical and snapshots independent

**Goal:** Explicit saves retire old keys without losing rules or inheritance, and config snapshots do not alias.

**Files:** `internal/app/config/save.go`, `layers.go`, `copy.go`, new
`interaction_save_test.go`, `copy_test.go` in that package.

**Steps:** Export `Clone` as a wrapper around existing `deepCopyConfig`.
At `writeSections`, replace old agent/custom-agent serializers with canonical
Mode/Auto and omit retired shell keys. Route raw-file explicit serializers
(`SaveVerifyCommands`, API-key save, `writeUserConfig`, `SaveUserConfigValue`)
through `canonicalFileForSave` too; never add it to automatic hoist. Keep absent
child fields absent. Preserve unrelated sections, comments to the existing
serializer's extent, and explicit permission rules.

New tests: `TestInteractionSaveEmitsCanonicalOnly`,
`TestInteractionCustomAgentSaveRetainsAbsentAndFalse`,
`TestInteractionExplicitSectionSaveRetiresLegacyKeys`,
`TestInteractionSavePreservesExplicitRules`,
`TestInteractionSaveDoesNotPersistSessionGrants`, and
`TestCloneConfigDoesNotAliasInteractionOrPermissionMaps`.

**Verify:** `CGO_ENABLED=1 go test ./internal/app/config -count=1` passes.

## Task 6: Install authoritative session state and reload semantics

**Goal:** A session owns one atomic tuple, configured defaults, and a whole-tuple manual override.

**Files:** `internal/app/session/session.go`, `workspace.go`, new
`interaction.go`, `interaction_test.go` in that package.

**Steps:** Replace the state behind `configuredMode`, `sessionModeOverride`,
and `systemAccess` with canonical access, configured Defaults, and optional
Defaults override; keep old accessor signatures as derived adapters until
consumers migrate. `New` initializes revision 1 only from valid defaults;
malformed standalone fixtures cannot authorize work. Stop mirroring effective
grants into Config. Apply patches under accessMu, committing all fields once
and publishing after unlock. A mode/Auto patch records the whole pair override.
Rebase validates first, preserves override when configured pair is unchanged,
drops it when the pair changes, preserves System, installs configured Config,
and increments revision once. Failed rebase leaves everything unchanged.
`SetWorkspace` takes owner accessMu before mu and invalidates revision on active
root changes. `AddSessionRule` also uses the owner transaction and invalidates.

New tests: `TestInteractionSessionAtomicTransitions`,
`TestInteractionRevisionDetectsABA`, `TestInteractionOverrideSurvivesUnrelatedRebase`,
`TestInteractionConfiguredAutoChangeClearsTupleOverride`,
`TestInteractionInvalidRebasePreservesState`, `TestInteractionNewAndResumeResetGrants`,
`TestInteractionWorkspaceInvalidatesRevision`, and
`TestSessionRuleChangeInvalidatesAccessRevision`. Use explicit configured defaults.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/app/session -run 'Interaction|SessionMode|Workspace|SessionRuleChange' -count=1` passes.

## Task 7: Add live actor views and the admission lease

**Goal:** Children read current owner permissions/config and stale operations cannot cross admission.

**Files:** New `internal/app/session/authority.go`, `authority_test.go`;
`internal/app/session/interaction.go`, `session.go`, `workspace.go`.

**Steps:** Implement `WithInteractionOwner`, flattening ancestry and intersecting
ceilings at construction. `InteractionAuthority` snapshots current owner tuple
and actor workspace. BeginAdmission takes the owner gate, validates context,
lifecycle, full expected identity/workspace/state/revision, and returns an
idempotent lease; mismatches unlock and fail. Actor workspace changes invalidate
the owner's revision without changing the owner's root. Implement PolicySnapshot
using config.Clone and copied rules under the same owner transaction. No engine,
provider, DB, or filesystem callback runs under the gate.

New tests: `TestInteractionAdmissionStaleRevisionFails`,
`TestInteractionAdmissionLeaseSerializesModeChange`,
`TestInteractionAdmissionReleaseIsIdempotent`,
`TestInteractionChildViewUsesCurrentParentCeiling`,
`TestInteractionNestedCeilingsOnlyNarrow`,
`TestInteractionActorWorkspaceDoesNotRetargetOwner`, and
`TestPolicySnapshotSharesAccessRevisionAndCurrentRules`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/app/session -run 'Interaction|PolicySnapshot|SessionRuleChange' -count=1` passes without blocked mode changes after lease release.

## Task 8: Narrow typed capability grants to Act and System

**Goal:** Existing exactly-once capability settlement grants only missing requested capabilities.

**Files:** `internal/app/session/pending.go`, `session.go`, `pending_test.go`,
`session_test.go`, `request_inspection_test.go`.

**Steps:** Extend access presentation/current/effective snapshots to all four
canonical fields, with conversion from InteractionState; retain old string
Mode transport shape during caller migration if needed. At RegisterAccess and
ResolveAccess, compare revision as well as tuple and existing identities.
Editing grants normal Act with Auto=false; system-only preserves mode/Auto;
combined grants commit one revision. Stale workspace/config/ABA presentations
refresh and remain pending. Keep cancellation, shutdown, acknowledgment order,
and Prepare/Commit ownership; prepare cannot mutate grants or an engine.
Update shutdown snapshots and legacy editing-choice fixtures accordingly.

New tests: `TestAccessActGrantNeverEnablesAuto`,
`TestAccessSystemOnlyPreservesModeAndAuto`, `TestAccessCombinedGrantCommitsOneRevision`,
`TestAccessABAChoiceRequiresRefresh`, and
`TestAccessWorkspaceOrConfigRevisionRequiresRefresh`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/app/session -run 'Access|Pending|Prompt' -count=1` passes, including existing cancellation/foreign-choice regressions.

## Task 9: Forward child approval identity without sharing pending slots

**Goal:** Child and pipeline ordinary approvals can be observed with their real owning prompt registry.

**Files:** `internal/app/session/session.go`, `subagents.go`, `prompts.go`, new
`approval_sources.go`, `approval_sources_test.go` in that package.

**Steps:** Add ApprovalSource to Event. At central `registerSubagent`, forward
only ordinary approval lifecycle snapshots to the captured root broker,
including an already-pending child approval. Preserve child owner/key/context
and Respond reference while copying display fields; do not copy sync.Once or
expose response channels as a new settlement path. Deduplicate active keys and
detach forwarding when the child/root closes. Never relay child transcript
events or occupy the parent's approval slot.

New tests: `TestChildApprovalForwardingRetainsCanonicalOwner`,
`TestChildApprovalForwardingDoesNotRelayTranscript`,
`TestRegisterChildForwardsAlreadyPendingApprovalOnce`, and
`TestChildApprovalForwardingNeverUsesParentPromptSlot`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/app/session -run 'ChildApprovalForwarding|RegisterChildForwards|Subagent' -count=1` passes.

## Task 10: Parse executable shell operations once

**Goal:** One AST analysis distinguishes executable behavior from quoted data, including nested execution.

**Files:** New `internal/tools/policy/shell_types.go`, `shell_parse.go`,
`shell_parse_test.go`; `internal/tools/policy/policy.go` parser/control helpers.

**Steps:** Add the shell fact contracts and AnalyzeShell using existing
`mvdan.cc/sh/v3/syntax`. Preserve raw command bytes and display-control checks;
syntax errors return errors. Replace flat stage extraction in the new path
with context-aware traversal of chains, pipelines, blocks, subshells,
command/process substitutions, redirections, and heredocs. Represent known
empty arguments separately from unknown expansions. Decode static quoted and
escaped words with a quote-aware decoder; resolve environment only from
captured known values, never execute expansion callbacks. Single-quoted data
does not recurse; executable substitutions inside double quotes do. Analyze
unquoted heredoc expansion, and quoted heredoc bodies when a recognized shell
actually consumes them as code. Recognized transparent wrappers consume their
own flags/values; inspect sh/bash/zsh/dash/ksh/eval, xargs, and **all** find
exec payloads. Bound recursion at 8; exhaustion records unknown effects.

New tests: `TestShellParseLiteralDataNeverBecomesOperation`,
`TestShellParseKnownEmptyAndUnknownWordsDiffer`,
`TestShellParseEveryNestedExecutable`, `TestShellParseWrapperFlagConsumption`,
`TestShellParseHeredocExecutionContexts`, `TestShellParseAllFindPayloads`, and
`TestShellParseInvalidSyntaxAndControls`. Strings include the seven reproduced
commit messages, multiline/multiple -m messages, and escaped executable names.

**Verify:** `CGO_ENABLED=1 go test ./internal/tools/policy -run 'ShellParse|Controls' -count=1` passes; no payload processes run.

## Task 11: Resolve shell cwd, Git context, and target scope

**Goal:** Every operation carries known canonical targets or explicit uncertainty relative to the active worktree.

**Files:** New `internal/tools/policy/shell_scope.go`, `shell_scope_test.go`;
`internal/tools/policy/shell_parse.go`.

**Steps:** Carry success/failure cwd sets through control flow: && consumes
success, || consumes failure, sequencing joins outcomes, blocks share scope,
and pipelines/subshells/substitutions cannot leak cwd to the parent. Cap finite
alternatives at 8 and mark unresolved loop/dynamic flow unknown. Interpret cd
and Git -C chains, -c, --git-dir/--work-tree and known GIT_* context before
actual subcommand/flags. Resolve existing symlinks/nearest ancestors with
`pathutil.ResolveAbsolute`, use filepath.Rel containment, and mark dangling
symlink resolution unknown. ReadRoots are read-only exceptions, never Auto
write roots. Linked-worktree .git metadata is incidental Git state; a different
targeted repository is not the actor workspace. Detect whole workspace root,
root/home and known whole-contents equivalents, preserving mixed target scopes.

New tests: `TestShellScopeCdSuccessFailureAndIsolation`,
`TestShellScopeGitDirectoryOptions`, `TestShellScopeActiveLinkedWorktree`,
`TestShellScopeSymlinkAndMissingDescendant`, `TestShellScopeMixedAndUnknownTargets`,
and `TestShellScopeReadExceptionsNeverGrantWrites`.

**Verify:** `CGO_ENABLED=1 go test ./internal/tools/policy -run 'ShellScope|ShellParse' -count=1` passes using owned filesystem fixtures.

## Task 12: Classify routine reads, edits, builds, and local installs

**Goal:** Routine project work allows in Act while Ask admits only positively read-only operations.

**Files:** New `internal/tools/policy/shell_classify.go`,
`shell_classify_test.go`; `internal/tools/policy/shell_parse.go`.

**Steps:** Classify actual executable/subcommand and consume flag values before
operands. Positive read vocabulary: pwd/true/false, date query/uname,
echo/printf/command -v, cat/head/tail/wc/stat/readlink/realpath/ls/du, known
grep/rg query flags, known find predicates, Git queries, and bounded `sed -n`
numeric print queries. Flags that execute helpers (`rg --pre`), Git external
diff/textconv/config overrides, mutating flags, or unsupported syntax become
their actual effects or uncertainty, not reads. Ordinary writes/redirections,
tee/cp/mv and commits are edits. go test/build/vet/run, cargo check/test/build/run,
make, and npm/pnpm/yarn run/test/build are Act operations; do not inspect their
opaque program bodies. Classify module-local go get/mod tidy/download and
local npm/pnpm/yarn installs as routine, including normal caches. Pip requires
known project-local venv/target, go install a known local GOBIN. External
prefix/global flags and machine package managers require environment policy;
uncertain targets stay unknown. Arbitrary interpreter -c/-e, awk programs,
source and unknown scripts are not proven reads.

New tests: `TestShellRoutineOperationTable`, `TestShellReadFlagsArePositive`,
`TestShellCommitMessagesAreOnlyData`, `TestShellLocalInstallTargets`,
`TestShellGlobalAndUncertainInstallTargets`, and
`TestShellOrdinaryOverwriteIsRoutineEdit`.

**Verify:** `CGO_ENABLED=1 go test ./internal/tools/policy -run 'ShellRoutine|ShellRead|ShellCommit|ShellLocal|ShellGlobal|ShellOrdinary|ShellParse|ShellScope' -count=1` passes.

## Task 13: Classify destruction, publishing, and the small floor

**Goal:** Recoverable destruction is approval-gated and only catastrophic operations are intrinsically denied.

**Files:** New `internal/tools/policy/shell_destructive.go`,
`shell_destructive_test.go`; `internal/tools/policy/shell_classify.go`.

**Steps:** Classify Git hard reset, forced non-dry-run clean, restore/discard,
forced checkout/switch/branch deletion/worktree removal; recursive rm even
without -f; destructive find actions/payloads; recursive chmod/chown; truncate,
shred, and dd output targets. Distinguish dry-run/query forms and ordinary
single-file removal. Mark actual git push through wrappers as publishing in
all flag variants. Global config/install/cache destruction is environment
effect. Actual privilege wrappers refuse ordinary shell with shell.sudo
guidance. Catastrophic operations are formatting (`mkfs*`, mke2fs, newfs,
diskutil erase), raw disk writes, recursive root/home deletion including
whole-contents equivalents, and shutdown/reboot equivalents. /dev/null and
/dev/zero sinks are not raw disks. Unknown operands produce uncertainty rather
than guessed floor matches; harmless words/paths never match executable floors.

New tests: `TestShellDestructiveEquivalentForms`,
`TestShellDestructiveDryRunsAndSingleRemoval`, `TestShellPublishingEveryWrapper`,
`TestShellFloorOperationTable`, `TestShellFloorWholeContentsEquivalents`, and
`TestShellPrivilegeWordsVersusWrappers`.

**Verify:** `CGO_ENABLED=1 go test ./internal/tools/policy -run 'ShellDestructive|ShellPublishing|ShellFloor|ShellPrivilege|ShellCommit' -count=1` passes without executing those commands.

## Task 14: Apply structured policy and live configuration

**Goal:** One decision matrix combines current authority, effects, scope, rules, and mandatory approvals.

**Files:** `internal/tools/policy/policy.go`, `classify.go`, `sessionrule.go`,
new `evaluation.go`, `evaluation_test.go`, `config_source_test.go` in that package;
existing `policy_test.go`, `system_floor_test.go`, `matchrule_test.go`.

**Steps:** Add EvaluateInput and ConfigSource. Clone retains the same source;
source revision must match ActorState before evaluation. Classify non-shell
tools from registry risk plus trusted read-only identities: MCP remains
workspace-write unless explicitly classified; browser click/fill/submit and
unclassified network effects require Act, while approved web reads and
session bookkeeping remain available. Apply Ask and scope capabilities before
allows, then explicit deny/confirm, catastrophic/privilege constraints and all
applicable spec matrix gates. Auto only preauthorizes fully known workspace
destruction; workspace-root deletion, outside/environment, publishing and
uncertainty always retain exact approval. System broadens scope only.
Replace plain shell prefix/standing-session matching with parsed operations;
retain deliberate glob/regex argument policy. Collect deny/confirm before allow
so later allow cannot mask them. Add stable reason codes and exact-approval
metadata; do not parse reason text. Retain generic non-shell rule semantics.
Expose old Evaluate only as a temporary adapter; remove substring helpers in
Task 45 after consumers migrate. Replace obsolete System/allow-bypass tests
with matrix expectations.

New tests: `TestInteractionPolicyMatrix`, `TestAskCeilingPrecedesEveryAllow`,
`TestShellRulesMatchOperationsAndPreserveExplicitPatterns`,
`TestShellConfirmAndDenyCannotBeMasked`, `TestClonedEngineReadsCurrentConfigSource`,
`TestEvaluationRejectsMismatchedSourceRevision`,
`TestAskNetworkAndMCPRequirePositiveReadClassification`, and
`TestShellApprovedDestructionHasNoSecondGuardrailDeny`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/tools/policy ./internal/permissions -count=1` passes, with the canonical matrix explicitly tested.

## Task 15: Bind policy decisions to one admission

**Goal:** Approval cannot outlive its exact command/context or authorize a second invocation.

**Files:** New `internal/tools/policy/admission.go`, `admission_test.go`;
`internal/tools/policy/evaluation.go`.

**Steps:** Implement the Authorizer contracts. Evaluate captures a fresh
authority snapshot, matching ConfigSource revision and deep-copied call/facts.
Use deterministic encoding plus SHA-256 for identity/facts comparison, retaining
raw command bytes; do not shell-normalize them. ApproveExact only marks the
captured Confirm after canonical settlement. BeginTool/BeginCommand refresh
facts/config and compare before acquiring the lease, then compare the live
snapshot under the lease. Missing authority, stale revision, changed targets,
changed cwd, changed invocation or used proof fails closed. Commit consumes
once; rejected admission and Release cannot execute anything. Allow-only
background use can prepare a new proof without a UI; Confirm cannot. Declare
typed sentinel errors for stale admission and approval required so dispatch
can distinguish retry/presentation from execution failure. Limit dispatch
pre-admission refresh to three attempts, then return a non-approved retry
result; no automatic retries after accepted execution.

New tests: `TestAdmissionBindsExactInvocationCommandAndDirectory`,
`TestAdmissionRechecksSymlinkFactsAndConfig`, `TestAdmissionCannotReuseApproval`,
`TestAdmissionAskWinsBeforeCommit`, `TestAdmissionMissingAuthorityFailsClosed`,
and `TestAdmissionBackgroundAllowNeverManufacturesApproval`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/tools/policy -run 'Admission|ConfigSource|SourceRevision' -count=1` passes using fake authorities and owned target fixtures.

## Task 16: Gate native managed commands at accepted launch

**Goal:** Approved destructive commands launch once without a second classifier, and pending launches obey current access.

**Files:** `internal/tools/native/native.go`, `command.go`, `jobs_manager.go`,
`runner.go`, `command_test.go`, `jobs_test.go`, `runner_test.go`, new
`admission_test.go` in that package.

**Steps:** Add CommandAdmission and LaunchSpec.Admit. Capture the actual test
command, nonempty cwd, workspace, home/read roots and known environment before
analysis; test.run uses the resolved configured command, not its empty input
argument. Resolve default test commands from the owner's current PolicySnapshot,
including in old child toolsets, rather than their captured creation config.
`runManagedCommandWithProgress` passes a closure over that captured
attempt. In Launch, acquire its admission before manager.mu; perform existing
capacity/cancellation/lifecycle checks, Commit immediately before inserting
m.jobs/incrementing active, and Release after unlock before notifications and
goroutine launch. Remove late DirFunc resolution from this path. Keep existing
work lifecycle admission and execution origin. Missing callback fails closed
in the new path. During app migration only, retain the existing nonnil Guardrail
adapter for old app wiring; remove it in Task 23. Standalone tests use an
explicit owned fake authority, not a nil-means-trusted bypass. In execRunner.Run,
remove shlex/ClassifyCommand fast-path selection and always use shellCommand;
preserve pipefail, output capture, cancellation, and OnStart.

New tests: `TestManagedCommandApprovedDestructionLaunchesOnce`,
`TestManagedCommandAskBlocksUnadmittedLaunch`,
`TestManagedCommandAdmittedBeforeAskKeepsCapturedIdentity`,
`TestManagedCommandWorkspaceCannotRetargetAfterApproval`,
`TestManagedCommandRejectedCapacityDoesNotExecute`, and
`TestManagedCommandMissingAuthorityFailsClosed`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/tools/native -run 'ManagedCommand|Admission|Job|Runner|Command' -count=1` passes with fake runners. No permission lease lasts for a process wait.

## Task 17: Apply the same contract to privileged attempts

**Goal:** Sudo preserves authentication behavior while enforcing exact command consent and current access.

**Files:** `internal/tools/native/sudo.go`, `sudo_test.go`,
`internal/app/session/pending.go`, `sudoauth_test.go` for the mechanical prompt
metadata extension only.

**Steps:** Replace runSudo's SudoPolicy/Guardrail checks in the new path with
the shared bare-payload analysis. Keep System, secret-capable client,
single-flight lock, child restriction, sanitized env, stdin password/zeroing,
no PTY, and the existing stdin-detachment fix. Capture cwd once. Fresh mode's
existing password confirmation must disclose exact command and mandatory
reasons and collect command consent there; pass its canonical prompt identity
to ApproveExact after settlement. Agent dispatch defers a Confirm to this
combined prompt only for secret-capable fresh shell.sudo, never for Deny or
missing Act/System. In unlock mode, collect required command approval before
the empty-stdin warm attempt; a warm credential is not consent. Revalidate and
admit immediately before each direct runner attempt, releasing before Run.
Retry the same authentication attempt only after fresh current evaluation;
mode/config/workspace change requires fresh consent, not silent proof renewal.
Mark combined prompt metadata explicitly so TUI displays its command-approval
role without adding a second password dialog. Retain old callback adapters
only until Task 23 removes their app wiring.

New tests: `TestSudoFreshPromptCombinesExactConsent`,
`TestSudoWarmUnlockStillRequiresCommandConsent`,
`TestSudoPayloadFloorAndPublishingUseSharedAnalysis`,
`TestSudoModeChangeBeforeAttemptRejects`, and
`TestSudoPasswordRetryCannotReuseStaleAuthority`. All use the existing fake
sudo/process/prompt seams; keep existing password-leak/no-PTY tests.

**Verify:** `CGO_ENABLED=1 go test ./internal/tools/native -run 'Sudo|Privileged' -count=1` and
`CGO_ENABLED=1 go test ./internal/app/session -run 'SudoAuth' -count=1` pass without real authentication.

## Task 18: Migrate native capability and clarification tools

**Goal:** Model-facing capability requests ask for normal Act/System, and questions follow live Auto.

**Files:** `internal/tools/native/native.go`, `mode_request_test.go`,
`question_test.go`, `schemas_test.go`, `agent_run_test.go`.

**Steps:** Change mode.request schema/handler to canonical act plus independent
system request. Translate old editing hints only to requested Act; reject any
Auto grant request. Preserve reason, typed settlement and child restriction.
Use the actor's live authority in clarification question handling, not config
or a captured Policy.ApprovalMode. Auto returns the existing assumption/blocker
path instead of opening a clarification prompt; ordinary Act can ask. Keep
explicit skill/user review requests separate from clarification suppression.
Read-only delegation remains available in Ask; custom agent/tool restrictions
are narrowed later by app actor construction.

New tests: `TestModeRequestOnlyRequestsNormalActAndSystem`,
`TestModeRequestCannotRequestAuto`, `TestQuestionUsesCurrentAutoModifier`, and
`TestAskAllowsReadOnlyDelegationBookkeeping`.

**Verify:** `CGO_ENABLED=1 go test ./internal/tools/native -run 'ModeRequest|Question|Schema|AgentRun' -count=1` passes.

## Task 19: Revalidate agent dispatch and approval settlement

**Goal:** Cached results, hook rewrites, edited approvals and queued writes cannot bypass current permissions.

**Files:** `internal/agent/execute.go`, `requests.go`, `runner.go`,
`runner_approval_test.go`, `runner_hooks_test.go`, `cache_invalidator_test.go`,
new `interaction_admission_test.go`; `internal/app/session/pending.go` for
the ExactCommandApproval field only.

**Steps:** Construct per-runner Authorizer from its live State authority and
Policy source. Generate one fresh invocation identity per logical tool attempt.
Move capability/scope checks before cache lookup. Replace copied
SetSessionRules/Evaluate calls with EvaluateInput through Authorizer. On hook
rewrite or user-edited command, discard proof, reparse/re-evaluate, and request
new mandatory consent as needed; a shell edit is not implicitly approved by
its old decision. RequestApproval marks exact-only requests so standing/session
choices cannot be accepted. Canonical settlement alone calls ApproveExact.
After all waits/write locks/snapshot preparations, ordinary tools BeginTool,
Commit, Release and invoke handler; shell/test/sudo pass proof context for
native command admission instead. No duplicate consume. Fresh secret-capable
sudo uses Task 17's combined prompt path. Bounded stale retry is pre-admission
only. Replace SetApprovalMode behavior with read-only derived compatibility
until caller cleanup; it cannot mutate authority.

New tests: `TestDispatchAskCeilingRunsBeforeCachedRead`,
`TestDispatchEditedShellRequiresNewApproval`,
`TestDispatchHookRewriteRechecksAllEffects`,
`TestDispatchQueuedWriteRechecksAfterWriteGate`,
`TestDispatchExactApprovalCannotCreateStandingRule`,
`TestDispatchShellProofConsumedOnlyAtLaunch`, and
`TestDispatchStaleApprovalCannotLaunchAfterAsk`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/agent -run 'Dispatch|Approval|Hooks|Cache|Steer' -count=1` passes with no real shell mutations.

## Task 20: Migrate prompts without changing the cached prefix mid-turn

**Goal:** Ask/Act/Auto guide behavior while mode changes enter only the volatile working-state tail.

**Files:** `internal/agent/prompts.go`, `route.go`, `chat.go`, `runner.go`,
`execute.go`, `prompts_test.go`, `cacheprefix_test.go`,
`runner_mode_test.go`, `runner_skillgate_test.go`, `runner_askuser_test.go`.

**Steps:** Replace Plan/Default/Edit/Copilot/Auto mode directives with Ask/Act
behavior and separate Auto/System descriptions. Remove numbered-plan and
mode-selected planning skill instructions. At withWorkingState/chatOnceAttempt,
append the fresh authoritative tuple/revision every provider request even when
there are no other working-state parts. Keep turn-start system prefix/tool
schemas stable during that turn; never insert a changed mode message before
history. Envelope questions and skillLoadGate consult live Auto. The existing
small-context skill-load optimization may still auto-accept instruction loading;
explicit spec/plan/user review gates must still wait. Task classification
cannot grant editing authority. Update prompt/schema budget fixtures for the
new text without relaxing their limits.

New tests: `TestInteractionPromptUsesAskActAndModifiers`,
`TestInteractionChangeAppearsOnlyInWorkingTail`,
`TestAutoNeverInventsExplicitSkillReviewApproval`, and
`TestActPlanningWorkflowNeedsNoModeSwitch`.

**Verify:** `CGO_ENABLED=1 go test ./internal/agent -run 'Interaction|Prompt|Prefix|Mode|Skill|AskUser|Schema' -count=1` passes, including existing `TestPromptPrefixStableAcrossSteps` and its reasoning variant in cacheprefix_test.go.

## Task 21: Bind pipeline actors to their execution workspace

**Goal:** Pipeline actors use the active dispatch worktree and retain live owner ceilings.

**Files:** `internal/pipeline/dispatch.go`, `dispatch_test.go`,
`exec_context_test.go`.

**Steps:** In runExec, set the newly created runner State's actor workspace to
ExecutionContext.WorkspaceRoot before RegistryFactory and child registration.
Do not change the parent workspace or treat NamedRoots/RepoRoot read exceptions
as Auto write roots. Preserve ScopeReadOnly/ArtifactWriter/FullWithoutNetwork
registry restrictions. Keep child registration using its actual State, so
ordinary approvals can be forwarded. Pipeline future tool calls use the actor's
authority; no permission grant is captured for an entire run.

New tests: `TestDispatchActorUsesExecutionWorktree`,
`TestDispatchActorDoesNotRetargetParentWorkspace`, and
`TestDispatchReadOnlyRoleRemainsNarrowerThanAct`.

**Verify:** `CGO_ENABLED=1 go test ./internal/pipeline -run 'Dispatch|ExecContext|Registry' -count=1` passes.

## Task 22: Expose atomic runtime patches and install config transactionally

**Goal:** Runtime access changes preserve managers and config reloads install one coherent policy/state revision.

**Files:** `internal/app/runtime.go`, `app.go`, `runtime_test.go`,
`app_test.go`, new `interaction_test.go` in that package.

**Steps:** Add SetInteraction under reloadMu, delegating once to session
ApplyInteraction and publishing the committed full snapshot. accessPrepare
validates requested normal Act/System only; remove engine setters and config
saves from access commits. Strictly validate InteractionDefaults before startup,
new/resume, and reload candidate construction. In reloadAgentRuntime, build
the candidate before installing anything, rebase config/defaults in the owner
transaction, and point every installed engine at the captured owner
PolicySnapshot. Failed candidate construction changes no tuple/source/revision.
Reuse command/watch managers; a pure access patch never rebuilds providers or
restores grants from history. Delete runWithExitAnalysisElevation's implicit
mode/System elevation. The existing ExitAnalysisScope controls lifecycle only;
optional agent analysis uses current owner permissions. Host-managed report
extraction/persistence continues as bookkeeping. If the optional agent pass
needs unavailable write/System capability, return its existing non-approved
analysis failure and retain the extracted report; do not hang exit or silently
grant authority. Keep explicit stop/exit settlement ownership.

New tests: `TestRuntimeInteractionPatchDoesNotSaveReloadOrReplaceManagers`,
`TestRuntimeInteractionPatchFailureIsAtomic`,
`TestRuntimeInteractionConfigStaysStartingDefaults`,
`TestRuntimeInteractionDefaultTupleRebase`,
`TestRuntimeInteractionFailedReloadPreservesRevision`, and
`TestRuntimeInteractionGrantRevisionSerializesWithReload`.
Add `TestExitAnalysisNeverElevatesConversationPermissions` for Ask and Act
without System, asserting the managed extracted report still exists and an
unapproved optional agent write cannot run.
Retain `TestRuntimeAccessKeepsRunningCommandIdentity` and
`TestRuntimeAccessSerializesWithReloadAndQuiesce`, updated to canonical tuples.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/app -run 'Interaction|RuntimeAccess|RuntimeExplicitConfigured|RuntimeFailedReload|ReloadAgentRuntime' -count=1` passes.

## Task 23: Wire owner policy and actor ceilings through every app runner

**Goal:** Root, child, role, and plan-author native calls all use the same live policy and admission contract.

**Files:** `internal/app/app.go`, new `internal/app/interaction_children_test.go`;
`internal/tools/native/native.go`, `command.go`, `sudo.go` for mechanical deletion
of retired options and temporary callback adapters only.

**Steps:** At root policy/native construction, set ConfigSource to the captured
root State.PolicySnapshot and CommandAdmission to that actor's Authorizer.
Replace old Guardrail/SudoPolicy options entirely; remove the temporary native
fallbacks from Tasks 16–17. At roleRunnerSpec.newRunner, buildPlanAuthorFactory,
makePipelineRegistryFactory and ad-hoc child State creation, pass
WithInteractionOwner and immutable role/custom-agent ceilings. Remove forced
ModeAuto clones and one-time parent System snapshots. Explicit System request
still caps child System, and existing cost consent/privileged-child exclusions
remain. Pipeline CommandDir is ctx.WorkspaceRoot and feeds the actor authority;
AdditionalRoots/NamedRoots remain read exceptions. Plan-author artifact scope
stays narrow and requires owner's Act for writes. Existing live children retain
the original owner's ConfigSource across reload and session replacement.

New tests: `TestChildCannotWriteAfterOwnerSwitchesAsk`,
`TestRoleAndPlanAuthorCannotForceAuto`,
`TestChildSystemRequiresExplicitRequestAndLiveOwnerGrant`,
`TestPipelineAutoOnlyCoversDispatchWorktree`,
`TestOldChildPolicyCannotAdmitAfterReloadedDeny`, and
`TestRejectedReloadDoesNotReplacePolicySource`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/app -run 'Child.*(Ask|System|Policy)|RoleAndPlanAuthor|PipelineAuto|PolicySource' -count=1`;
`CGO_ENABLED=1 go test ./internal/tools/native -count=1` both pass. A native toolset without CommandAdmission can no longer execute a command.

## Task 24: Reauthorize each repeating watch sample

**Goal:** A watch cannot keep executing new writes from its original Act state after the owner selects Ask.

**Files:** `internal/app/app.go`, `watch_resume_test.go`, new
`interaction_watch_test.go` in that package.

**Steps:** Replace watchManager.RunSample's direct unchecked commandRunner.Run
with the captured owner Authorizer using the same ShellInput/BeginCommand
contract for each sample. Construct policy/source before starting watches.
Only current Allow samples run; Confirm/missing capability/Deny returns a
typed non-approved sample error through existing watch error handling, without
opening unsolicited background approval prompts or inventing Auto. Commit and
Release immediately before the direct runner attempt. Previously admitted
sample continues; next sample rereads current mode, root, rules and config.
Keep once/repeat watch modes and existing stop/error-budget lifecycle.

New tests: `TestWatchNextMutationSampleStopsAfterAsk`,
`TestWatchReadSampleContinuesInAsk`,
`TestWatchAdmittedSampleKeepsCapturedSettings`, and
`TestWatchConfirmNeverBecomesBackgroundApproval`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/app -run 'Watch.*(Ask|Sample|Approval)|WatchResume' -count=1` and
`CGO_ENABLED=1 go test ./internal/watch -count=1` pass.

## Task 25: Publish only canonical commands

**Goal:** Command help/completion exposes Ask/Act and modifiers without legacy or SDD mode entries.

**Files:** `internal/commands/commands.go`, `commands_test.go`.

**Steps:** In RegisterAll, remove plan/default/edit/copilot mode commands,
register ask/act, and change /auto metadata to toggle or on/off. /mode accepts
ask/act only; /system describes scope and retains unlock. Keep /sdd as its own
workflow command. Do not retain hidden aliases: ListAll includes hidden
commands in completion. Retired-command explanations belong to TUI dispatch.
Replace /settings summary's retired shell Auto-approve field with configured
mode/Auto defaults; effective state remains the runtime's separate presentation.

New tests: `TestInteractionCommandsExposeAskActAndModifiers` and
`TestRetiredModeCommandsAreNotRegistered` inspect both List and ListAll.

**Verify:** `CGO_ENABLED=1 go test ./internal/commands -count=1` passes.

## Task 26: Route TUI controls through the runtime patch

**Goal:** TUI commands, picker, and Tab apply the specified transitions and report only committed success.

**Files:** `internal/app/tui/model.go`, `commands_dispatch.go`,
`approval_mode_test.go`, `commands_plan_test.go`, `commands_dispatch_test.go`,
`model_test.go`; `internal/app/app.go` callback wiring only.

**Steps:** Replace sessionModeSetter with an injected SetInteraction callback;
add `WithInteractionSetter(func(interaction.Patch) (interaction.State,error))`.
Use committed snapshot for presentation, with no TUI fallback mutating runner,
policy, force-class, Config or State. Replace setMode/cycleMode with the
canonical patch table. Validate on/off arguments; /auto enables Act in one
action; Tab and plain mode selection reset Auto. modePickerItems contains
exactly Ask/Act. Before unknown-command receipt, handle exact retired tokens
with replacement guidance and no state mutation. Remove mode-driven skill
loading, Plan completion hints, and /mode sdd. Change startSDDAuthor guidance
to the existing approved-plan skill workflow. Mode receipts for Ask mention
continued active work using existing work state; no cancellation/restart.

New tests: `TestInteractionCommandTransitionTable`,
`TestInteractionCommandFailureDoesNotClaimSuccess`,
`TestRetiredModeCommandReceiptsDoNotChangeAccess`,
`TestModeCycleContainsOnlyAskActAndClearsAuto`,
`TestModeSelectionDoesNotLoadPlanningSkill`, and
`TestAskReceiptReportsAlreadyRunningWork`.

**Verify:** `CGO_ENABLED=1 go test ./internal/app/tui -run 'InteractionCommand|RetiredMode|ModeCycle|ModeSelection|AskReceipt|PlanCommand|ApprovalMode' -count=1` passes.

## Task 27: Keep mode and modifiers visible in TUI status

**Goal:** Persistent effective access remains visible during approvals, browsing, questions, and work.

**Files:** `internal/app/tui/status.go`, `status_test.go`, `system_access_test.go`.

**Steps:** modeSegment reads one committed snapshot and always formats Ask/Act
plus active ` · Auto` and ` · System`. Move existing transient labels into the
existing right/activity surfaces; retain Access waiting and queued-dock
behavior. Preserve the first status segment under width pressure and existing
running-work cards. Do not introduce another mandatory panel.

New tests: `TestInteractionStatusSurvivesTransientSurfaces` and
`TestInteractionStatusRetainsModifiersAtMinimumWidth`. Extend existing
`TestAccessPanelQueuesBehindOtherDockAndPreservesDraft` to assert full access
label while a FullFrame panel owns the dock.

**Verify:** `CGO_ENABLED=1 go test ./internal/app/tui -run 'InteractionStatus|AccessPanelQueues|StatusBadge|StatusLine' -count=1` passes in monochrome and narrow fixtures.

## Task 28: Simplify the capability panel

**Goal:** Capability review presents Deny and one requested Act/System grant, with no autonomy selection.

**Files:** `internal/app/tui/accesspanel/accesspanel.go`, `accesspanel_test.go`.

**Steps:** At New/decision/View, replace Edit/Copilot/Auto variants with Deny
and normal Act, System-only, or bundled Act+System as requested. Display full
current mode/modifiers/revision, conversation lifetime, and separate sudo
meaning. Keep presented identity/current snapshot in the choice. Refreshed
presentations default to Deny; hidden/undersized panels cannot approve.

New tests: `TestAccessPanelOffersOnlyRequestedActSystem` and
`TestAccessPanelDisplaysFullEffectiveState`; update existing capability-choice
and budget/monochrome tests without relaxing geometry checks.

**Verify:** `CGO_ENABLED=1 go test ./internal/app/tui/accesspanel -count=1` passes.

## Task 29: Preserve approval ownership and exact-only UI choices

**Goal:** Capability refreshes and child/ordinary approvals settle their real owner without standing grants for mandatory commands.

**Files:** `internal/app/tui/model.go`, `approval.go`, `approval_controls_test.go`,
`approval_child_test.go`, `system_access_test.go`, `sudoauth_test.go`.

**Steps:** reconcileAccess and handleAccessChoice compare all fields including
revision, refresh stale panels to Deny, and preserve queue/draft/escape behavior.
handleApproval copies ExactCommandApproval into display snapshots and disables
Always/SessionAllow for those requests; rejected forged choices do not add
rules. Extend forcedPromptClass in approval.go to include that explicit flag,
without matching presentation reason text. Ordinary explicit rule edits update configured/session rules through
runtime reload/owner state, not Runner.SetPolicyRules. Keep pendingApprovalTarget
and ResolveApproval's existing registered-child scan; use canonical owner
references rather than parent pending slots. Display Task 17's combined sudo
consent metadata in the existing password prompt. Never expose password transport
through the web/ACP path.

New tests: `TestAccessPanelRevisionRefreshResetsDeny`,
`TestExactCommandApprovalOmitsStandingChoices`,
`TestCanonicalChildApprovalSettlesOnlyChildOwner`, and
`TestChildDestructiveApprovalAfterAskDoesNotLaunch`. Retain existing foreign,
expired, hidden-size, queue, cancellation, and grant-ordering tests.

**Verify:** `CGO_ENABLED=1 go test ./internal/app/tui -run 'AccessPanel|AccessManual|ExactCommand|ChildApproval|ParentApprovalWins|SubagentApproval|Sudo' -count=1` passes.

## Task 30: Migrate settings starting defaults

**Goal:** Settings edit canonical starting mode/Auto and preserve optional child inheritance.

**Files:** `internal/app/tui/settings/frames_agent.go`,
`frames_custom_agents.go`, `frames_shell.go`, `frames_agent_test.go`,
`frames_collections_test.go`, `reset_test.go`.

**Steps:** Replace agent.approval_mode with agent.mode Ask/Act and agent.auto.
Selecting Ask clears Auto in the draft; invalid loaded input still fails in
the config loader. Custom-agent fields use optional Mode/Auto, with inherit as
field absence rather than a third mode. Explicit false survives saving.
Keep System/revision out of starting settings and use existing canonical save
surfaces; no effective session override is persisted.
Remove shell.auto_approve and shell.guardrail_dynamic_argv0 settings controls
and reset expectations; their migration diagnostics belong to the loader.

New tests: `TestInteractionSettingsOnlyAskActAndAuto`,
`TestCustomAgentInteractionSettingsPreserveInheritance`, and
`TestAskSettingClearsAutoDefault`.

**Verify:** `CGO_ENABLED=1 go test ./internal/app/tui/settings -count=1` passes.

## Task 31: Migrate the custom-agent roster editor

**Goal:** Roster configuration uses the same optional canonical defaults as settings.

**Files:** `internal/app/tui/agents/panel.go`, `panel_test.go`.

**Steps:** Replace the duplicated five-mode approval enum with Mode/Auto
controls and nil inheritance. Preserve existing model, tool-deny, context and
temperature fields and save routing. Choosing Ask clears an explicit true
Auto; nil Auto remains distinguishable from false. No roster setting can
grant beyond the current parent ceiling at runtime.

New test: `TestRosterInteractionFieldsUseCanonicalDefaults` checks read/edit/save
of inherited, Ask, Act, and explicit false values.

**Verify:** `CGO_ENABLED=1 go test ./internal/app/tui/agents -count=1` passes.

## Task 32: Make ACP switching atomic and add effective-state reads

**Goal:** ACP applies one validated patch and returns/broadcasts the full committed state.

**Files:** New `internal/acp/interaction.go`, `interaction_test.go`;
`internal/acp/turn.go`, `turn_test.go`, `host.go`.

**Steps:** Add owned wire types and strict presence-preserving decoder at
SetModeParams. Translate legacy mode values at ingress, with explicit Auto
overriding derived values and canonical Ask+Auto validation. Reject empty,
null, wrong-type and unknown explicit inputs before any callback. Replace
TurnRuntime's separate SetMode/System mutation paths with SetInteraction and
InteractionState; unavailable runtime fails closed. Wire host to Runtime once.
SetMode invokes one updater, returns its full result and emits the identical
mode_changed state. Register read-only session/access_state beside set_mode,
using captured session identity and authoritative read. Do not add any policy
decision or direct runner setter in ACP.

New tests: `TestSetModeAtomicPatchTransitionTable`,
`TestSetModeRejectsInvalidPatchWithoutMutation`,
`TestSetModeReturnsAndBroadcastsSameFullState`,
`TestSetModeLegacyIngressCanonicalReports`, and
`TestAccessStateReportsAuthoritativeSnapshot`.

**Verify:** `CGO_ENABLED=1 go test ./internal/acp -run 'SetMode|AccessStateReports|ManualMode' -count=1` passes, including false/omitted System and modifier-only cases.

## Task 33: Apply canonical ACP session starting policy

**Goal:** Session creation/load uses configured defaults and reports actual state without restoring manual grants.

**Files:** `internal/acp/sessionpolicy.go`, `session.go`, `session_test.go`.

**Steps:** Add optional Auto to PolicyParams. Normalize/validate starting policy
before starting the runtime, preserving false and absence. Apply its tuple via
the runtime seam before publishing the session; on failure close the candidate
runtime. Never call Runner.SetApprovalMode. SessionResponse always includes all
four effective fields from the runtime, even without requested policy. New/load
starts System off and uses starting defaults, not stored effective grants;
legacy policy mode migration emits canonical output. Preserve load's existing
response envelope where its protocol contract differs from session/new; use
session/access_state for effective bootstrap rather than inventing a transcript
state response.

New tests: `TestSessionNewReportsConfiguredInteractionDefaults`,
`TestSessionPolicyAutoPresenceAndLegacyModes`,
`TestInvalidSessionPolicyFailsBeforeRuntime`, and
`TestSessionLoadResetsInteractionGrants`.

**Verify:** `CGO_ENABLED=1 go test ./internal/acp -run 'Session(New|Policy|Load)|Policy' -count=1` passes.

## Task 34: Project scope-bound ACP capability approvals

**Goal:** Typed and legacy capability replies preserve full identity/current state and cannot grant Auto.

**Files:** `internal/acp/permissions.go`, `permissions_test.go`, `turn.go`, `turn_test.go`.

**Steps:** Replace ModeAccessState's old mode/system projection with the four
canonical fields. CollectAccess still uses the server-presented current
snapshot and canonical resolver, ignoring client-invented descriptions of it.
Normal editing choice is Act only; legacy edit/copilot/auto capability strings
translate to requested normal Act, not general LegacyDefaults. Unsupported
typed Auto/unknown choices fail closed without partial System grant. Update
disclosure to scope-only System wording and full committed mode_changed fields
after settlement. Preserve AccessRefresh loops, identity, late-reply and
cancellation arbitration.

New tests: `TestCollectAccessReportsFullState`,
`TestLegacyCapabilityModeCannotEnableAuto`,
`TestTypedCapabilityReplyCannotEnableUnrequestedAuto`, and
`TestACPAccessRevisionChangeReasksWithoutPartialGrant`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/acp -run 'CollectAccess|ACPAccess|ModeRequest|SetMode' -count=1` passes.

## Task 35: Collect ordinary child approvals for the ACP session lifetime

**Goal:** Pipeline/background child approvals remain answerable after the parent's turn ends.

**Files:** New `internal/acp/child_approvals.go`, `child_approvals_test.go`;
`internal/acp/turn.go`, `host.go`.

**Steps:** Start one guarded child-only subscriber on each captured runtime's
existing EventBroker before it can launch children. Keep it until session
replacement/shutdown or connection teardown, not the root runTurn lifetime.
Process only ApprovalSource-tagged events and deduplicate owner+PromptKey.
Root runTurn skips tagged events and continues its existing root collection.
Use source Owner for promptContextFor/steeringEndedPrompt equivalents and
PermissionBridge.Request with canonical Pending.Respond. Add actor attribution
to reasons. Missing responder or teardown settles that exact owner/key without
approval; late replies cannot clear newer root/child requests. No policy,
Auto decision, extra broker, or second response channel is introduced here.

New tests: `TestACPChildApprovalUsesOwningSessionRegistry`,
`TestACPPipelineChildDestructiveApprovalIsPresented`,
`TestACPChildApprovalAfterParentTurnStillResolves`,
`TestACPChildApprovalDuplicateEventsAskOnce`,
`TestACPChildApprovalCancellationLeavesNewerRequestUntouched`, and
`TestACPChildApprovalMissingResponderFailsClosed`.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/acp -run 'ChildApproval|PipelineChildDestructive' -count=1` passes with in-process transports.

## Task 36: Normalize workspace policy documents

**Goal:** Workspace policy preserves optional Auto and formats canonical interaction defaults.

**Files:** `internal/workspacecfg/doc.go`, `parse.go`, `render.go`,
`parse_test.go`, `render_test.go`, `preview_test.go`.

**Steps:** Add `Auto *bool` to Policy with `json:"auto,omitempty" toml:"auto,omitempty"`.
Replace policyModes validation with canonical Ask/Act plus legacy ingress
normalization. Reuse interaction.LegacyDefaults; do not alter networkModes.
Keep mode absence distinguishable, validate explicit Ask+Auto and higher-layer
tuples, and report source-specific migration notices through the existing
document diagnostic path. Parse never rewrites its input file. Format/render
and explicit patch output use canonical mode/Auto; no System/revision fields.
ACP WorkspaceFiles continues calling this package rather than duplicating
TOML parsing.

New tests: `TestPolicyLegacyModesNormalizeWithoutRewriting`,
`TestPolicyAutoPresenceAndAskValidation`, and
`TestPolicyFormatEmitsCanonicalInteractionFields`.

**Verify:** `CGO_ENABLED=1 go test ./internal/workspacecfg -count=1` passes; off/open/allowlist network tests remain unchanged.

## Task 37: Add bridge-owned access transport and typed permission forwarding

**Goal:** Bridge forwards atomic patches/full state and capability decisions without owning policy.

**Files:** New `web/bridge/interaction.go`, `interaction_test.go`;
`web/bridge/registry.go`, `registry_test.go`, `http.go`, `http_test.go`.

**Steps:** Declare stdlib-only owned wire contracts. Implement Registry.SetAccess
and AccessState using session/set_mode and session/access_state. Add strict
GET/PATCH /api/sessions/{id}/access routes, returning full committed results.
Keep POST /mode as a legacy ingress adapter to the same path, with no dummy
mode requirement for new clients. Validate wire shape/known values without
granting capabilities locally. Extend Decision and awaitPermission payloads
to preserve typed modeAccess request/choice, ordinary exact-only metadata and
reasons; keep ordinary edited command support. No secret transport is added.

New tests: `TestSetAccessForwardsOptionalFalseAndReturnsCommittedState`,
`TestAccessRouteReadsFullState`, `TestAccessRouteRejectsNullPatch`, and
`TestPermissionDecisionPreservesModeAccess`.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -run 'SetAccess|AccessRoute|ModeAccess|Registry|WebIsStdlibOnly' -count=1` passes.

## Task 38: Project full live state through fleet events and snapshots

**Goal:** Fleet status/reconnect preserves explicit revocations and never advertises inferred grants.

**Files:** `web/bridge/fleetevents.go`, `fleetevents_test.go`, `fleet.go`,
`status_test.go`, new `interaction_events_test.go`.

**Steps:** Replace mode-only live projection with one full AccessStateWire in
classifyNotification/liveState.apply, fleetDelta, AgentStatus and Snapshot.
Mode_changed replaces the whole state atomically; false values serialize.
Ignore older revisions per live session and reset comparison for replacement
session identity. On reattach/reconnect, query AccessState before claiming
current privileges; unknown/unavailable state is presented as unavailable,
never reconstructed from ProjectSettings or persisted Agent.Mode. Keep
existing status/work lifecycle deltas and SSE ordering.

New tests: `TestModeDeltaPreservesAutoSystemAndRevision`,
`TestFleetAccessSnapshotRejectsStaleRevision`, and
`TestFleetReattachReadsActualAccessInsteadOfStoredMode`.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -run 'ModeDelta|FleetAccess|FleetReattach|Status|FleetSnapshot|WebIsStdlibOnly' -count=1` passes.

## Task 39: Migrate persisted bridge starting defaults

**Goal:** Every stored default source migrates consistently without load-time rewriting or grant restoration.

**Files:** `web/bridge/workspace.go`, `workspace_test.go`, `projects.go`,
`projects_test.go`, `recipes.go`, `recipes_test.go`, `recipes_builtin.go`,
`wsresolve.go`, `wsresolve_test.go`.

**Steps:** Carry optional Auto beside mode in ProjectSettings, Recipe,
WSPolicy, PendingSpawn and historical/start Agent metadata. Add source-aware
normalization in Workspace.Load for agents/pending/settings, recipe read,
project validation and workspace tuple merge. Preserve absent versus false and
existing layer precedence; unknown values return actionable diagnostics,
not Act. Recipe List must surface invalid stored-recipe diagnostics rather
than silently hiding them. Load migration is memory-only; explicit saves emit
canonical mode/Auto. Do not change workspaceVersion solely to persist session
grants. Built-in review/summarize recipes start Ask; fix-ci/update-deps start
Act with Auto false. These authored defaults do not change stored Edit's
migration to Ask. No field restores System or manual live overrides.

New tests: `TestStoredInteractionDefaultsMigrateWithoutGrantRestore`,
`TestRecipeLegacyModesAndExplicitSaveAreCanonical`,
`TestRecipeInvalidStoredModeIsVisible`,
`TestProjectInteractionDefaultsRejectInvalidTuple`, and
`TestWorkspacePolicyMergePreservesAutoPresence`.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -run 'StoredInteraction|Recipe|ProjectInteraction|WorkspacePolicy|LoadMigrates|WebIsStdlibOnly' -count=1` passes with test-owned files.

## Task 40: Carry starting tuples through every spawn origin

**Goal:** HTTP, recipes, runs, intake, MCP and automation spawn the requested canonical defaults without silent promotion.

**Files:** `web/bridge/fleet.go`, `http.go`, `runs.go`, `reciperun.go`,
`intake.go`, `mcptools.go`, `fleet_test.go`, `intake_test.go`,
`mcptools_test.go`, `wsspawn_test.go`, `automation_test.go`.

**Steps:** Add Auto presence to SpawnOptions and input DTOs. Propagate it through
HTTP project fallback, run start, recipe launch, intake/pending approval, MCP
launch, and pending output. Merge requested/project/workspace starting policy
before session/new; send one combined policy and consume the actual full
session/new result. Delete effectiveSpawnMode's origin-based Edit-to-Auto
promotion. Do not send a second implicit set_mode or publish partially
configured live agents. Schedules/CI/review bots keep requested defaults;
unanswered mandatory approvals use the existing non-approved/cancel outcome.
Persist only starting/history metadata, never current grants. Update the old
acceptEdits verbatim workspace-policy fixture to the documented invalid case.

New tests: `TestSpawnUsesCombinedStartingTupleAndActualResponse`,
`TestEverySpawnOriginPreservesExplicitAutoFalse`,
`TestUnattendedOriginNeverPromotesLegacyEdit`, and
`TestPendingSpawnRetainsCanonicalTuplePresence`. Replace old automation tests
that expect implicit Auto rather than retaining a compatibility behavior.

**Verify:** `CGO_ENABLED=1 go test ./web/bridge -run 'Spawn|Intake|RecipeRun|Automation|MCP|WebIsStdlibOnly' -count=1` passes.

## Task 41: Centralize browser interaction types and state reduction

**Goal:** Browser bootstrap and notifications use one strict full-state model.

**Files:** New `web/ui/src/lib/interaction.ts`, `interaction.test.ts`;
`web/ui/src/lib/api.ts`, `api.test.ts`, `store.ts`, `store.test.ts`,
`fleet.ts`, `fleet.test.ts`.

**Steps:** Implement the shared TypeScript contracts, parser/formatter and
stored-default migration helper. Replace duplicated live Mode/MODES and
normalizeMode fallback. Add API GET/PATCH access methods returning committed
state; all effective DTOs carry full fields while default DTOs preserve Auto
presence. Both store mode_changed handlers and fleet reducers use one full
state reducer, retaining false and rejecting stale/malformed reports. Fetch
access state after replay/load and SSE reconnect. Do not optimistically grant
mode/System on a submitted patch. Extend PendingPermission/toPendingPermission
with reason, exact-only and typed modeAccess instead of dropping them.

New tests in interaction/store/fleet: `parses complete access reports`,
`rejects malformed and unknown live modes`, `keeps explicit revocations`,
`ignores older revisions within one session`, `resets revision for a new session`,
`bootstraps actual access after replay`, and `preserves typed permission scope`.

**Verify:** In `web/ui`, `npm test -- src/lib/interaction.test.ts src/lib/api.test.ts src/lib/store.test.ts src/lib/fleet.test.ts` passes.

## Task 42: Build the browser live controls and capability review

**Goal:** Chat/fleet show persistent Ask/Act plus modifiers and submit scope-bound capability decisions.

**Files:** `web/ui/src/lib/ModeSwitcher.svelte`, `PermissionModal.svelte`,
`AgentCard.svelte`, `web/ui/src/views/Chat.svelte`, `Chat.test.ts`,
new `web/ui/src/lib/ModeSwitcher.test.ts`, `PermissionModal.test.ts`;
`web/ui/src/lib/AttentionList.svelte` integration only.

**Steps:** Mode picker contains Ask/Act only; adjacent Auto/System controls
submit modifier-only patches. Render accessLabel from committed state through
waiting/approval panels and fleet cards. Show errors without claiming grants.
PermissionModal displays reason, exact command and typed current/requested
scope. Capability choices are Deny by default and one normal Act/System bundle;
no Auto choice. Return original identity/current with the choice. Ordinary
exact-only approvals do not expose standing grants. AttentionList uses the
same component and typed decision transport. Preserve current dialog/dock
geometry and unsupported secret behavior.

New tests: `shows two modes and independent modifiers`,
`waits for committed access before updating`, `keeps modifiers visible during approval`,
`grants only the requested capability bundle`, and `denies a refreshed presentation by default`.

**Verify:** In `web/ui`, `npm test -- src/lib/ModeSwitcher.test.ts src/lib/PermissionModal.test.ts src/views/Chat.test.ts` passes.

## Task 43: Migrate browser starting-default forms and remembered choices

**Goal:** New-agent, project, recipe and workspace forms use two modes and optional Auto consistently.

**Files:** `web/ui/src/lib/newagent/newAgent.ts`, `newAgent.test.ts`,
`web/ui/src/views/NewAgent.svelte`, `NewAgent.test.ts`, `Project.svelte`,
`Project.test.ts`, `web/ui/src/lib/project/model.ts`,
`web/ui/src/lib/library/RecipesTab.svelte`, `RecipeEditor.svelte`,
`RecipeEditor.test.ts`, `web/ui/src/lib/workspaces/SidePanel.svelte`,
`model.test.ts`, `web/ui/src/lib/api.ts` default DTOs only.

**Steps:** Replace all local five/six-value lists with shared MODES. Fresh
new-agent/recipe defaults are Act/false. Migrate remembered read/default/plan/edit
to Ask/false, copilot to Act/false, auto to Act/true, rejecting unknown values
with a visible reset affordance rather than silently granting Act. Remember
starting mode/Auto only. Selecting Ask clears Auto. Project/workspace controls
preserve inherit/omission versus explicit false; inherit is not a mode.
Workspace policy replacement patches retain unchanged policy fields. Loaded
recipes consume canonical server defaults. Remove free-text policy mode input.

New cases in existing suites: `migrates every remembered legacy mode`,
`starts fresh Act without Auto`, `never remembers System or revision`,
`preserves inherited Auto versus explicit false`, and `saves canonical defaults from every form`.

**Verify:** In `web/ui`, `npm test -- src/lib/newagent/newAgent.test.ts src/views/NewAgent.test.ts src/views/Project.test.ts src/lib/library/RecipeEditor.test.ts src/lib/workspaces/model.test.ts` passes; then `npm run build` succeeds.

## Task 44: Use one container shell execution path

**Goal:** Container execution no longer depends on the retired risk classifier or a second command-splitting path.

**Files:** `internal/sandbox/container.go`, `container_test.go`.

**Steps:** In Container.buildArgs, remove isShellFree/ClassifyCommand/shlex
direct-argv selection and its now-unused imports/helper. Keep image, env,
mount, resource, network and timeout construction unchanged. Use the existing
shell branch for every admitted command, with this exact inner argv:

```go
inner := []string{"/bin/sh", "-lc", "set -o pipefail 2>/dev/null || true; " + command}
```

Policy/native admission owns syntax/effect validation before launch; buildArgs
is transport construction, not a second policy evaluator. Replace direct-argv
and shlex-error expectations with exact command-byte preservation in shell
argv. Keep CPU timeout wrapping and all sandbox restrictions. New tests:
`TestContainerCommandUsesSingleShellPath` and
`TestContainerCommandPreservesQuotedMessageBytes`. Construction-only tests do
not start a container or download images.

**Verify:** `CGO_ENABLED=1 go test ./internal/sandbox -count=1` passes;
container.go imports neither tools/policy nor tools/registry for classification.

## Task 45: Remove old policy runtime and shell classifier APIs

**Goal:** The final policy implementation has no five-mode enum or alternative substring execution path.

**Files:** `internal/tools/policy/policy.go`, `classify.go`, `sessionrule.go`,
`approvalmode_test.go`, `classify_test.go`, `classify_destructive_test.go`,
`guardrail_stage_test.go`, `system_floor_test.go`, `gitpush_floor_test.go`,
`sessionrule_test.go`, `privileged_test.go`, `controls_test.go`, `su_payload_test.go`.

**Steps:** Delete ApprovalMode/approvalModes, SetApprovalMode/ApprovalMode,
ParseApprovalMode/ValidApprovalMode, applyModeTransform, guardrailPatterns,
systemGuardrailPatterns, legacyRMGuardrail, reasonEnvironment,
isBlockedByGuardrailLegacy, flat stage parsing, analyzeCommandDepth,
EvaluateGuardrails/GuardrailCheck/CheckPrivileged, and ClassifyCommand's separate
shlex walk. Replace remaining old Evaluate adapter callers in policy tests
with explicit Input/snapshot fixtures. Keep display-control validation and
fold reusable wrapper vocabularies into the one analyzer; remove dead duplicated
helpers. Delete obsolete enum tests, retaining their relevant behavior as
canonical matrix/regression cases. Do not remove explicit authored rule support.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/tools/policy ./internal/permissions -count=1` passes;
`rg -n 'ApprovalMode|ClassifyCommand|isBlockedByGuardrailLegacy|guardrailPatterns|analyzeCommandDepth' internal/tools/policy` has no runtime definitions/calls. New test descriptions may refer to historical failures.

## Task 46: Remove mode setters from runners and TUI interfaces

**Goal:** No runner adapter or rendering interface can mutate permission authority directly.

**Files:** `internal/agent/runner.go`, `prompts.go`,
`internal/agent/swarm/orchestrator.go`, `internal/pipeline/adapter.go`,
`internal/app/tui/model.go`, `internal/app/app.go`, plus their existing
interface fake implementations in `internal/app/tui/model_test.go`,
`internal/app/tui/steps_test.go`, `internal/agent/runner_mode_test.go`,
`internal/agent/runner_copyfrom_test.go`, `internal/pipeline/adapter_test.go`,
`internal/pipeline/integration_test.go`, `internal/agent/step_test.go`,
`internal/agent/chat_internal_test.go`, `internal/app/prompt_budget_test.go`.

**Steps:** Delete Runner.SetApprovalMode and obsolete SetPolicyRules mutation
methods, their swarm/pipeline adapter forwarders/no-ops, and AgentRunner
requirements/fakes. Replace old prompt builder signatures taking policy enums
with the canonical signatures in Contracts; migrate the listed test callers
and pipeline engine fixtures to explicit canonical state/source/authority.
Delete classForApprovalMode and TUI
WithSessionModeSetter/approvalMode cache. CopyFrom copies runtime dependencies
without transferring a mutable grant. Keep SetForceClass only where task
classification still needs it independently; no mode-driven calls survive.
This task is a mechanical deletion of the listed APIs after all callers migrated.

**Verify:** `CGO_ENABLED=1 go test ./internal/agent/... ./internal/pipeline ./internal/app/... -count=1` passes;
`rg -n 'SetApprovalMode|SetPolicyRules|WithSessionModeSetter|classForApprovalMode' internal` returns no runtime calls/definitions.

## Task 47: Retire legacy runtime config/session fields and shipped blanket rules

**Goal:** Legacy spelling exists only in raw config/protocol migration, with no second runtime mode model.

**Files:** `internal/app/config/types.go`, `defaults.go`, `file_types.go`,
`merge.go`, `interaction.go`, `config_test.go`, `load_test.go`,
`internal/llm/routing/types.go`, `interaction_test.go`,
`internal/app/session/session.go`, `pending.go`, `interaction.go`,
`session_test.go`, `internal/app/runtime.go`, `runtime_test.go`.

**Steps:** Delete runtime AgentConfig.ApprovalMode and
routing.CustomAgent.ApprovalMode and derived compatibility projections.
Keep deprecated TOML pointers only in raw mirrors and normalization. Delete
ShellToolConfig.AutoApprove/GuardrailDynamicArgv0 runtime fields and their
effects; raw keys remain diagnostic-only. Fresh shell defaults retain explicit
configured publishing floors but remove shipped broad deny and go-get/npm-install
confirm rules. Never erase equivalent authored user lists. Remove old
configuredAccessMode/ModeForConfig/RebaseSessionConfig/SetSessionMode families
and Config mirroring; callers use canonical methods. Final access projections
always carry canonical values and all fields. ACP may retain a string wire
adapter; session no longer interprets legacy strings as runtime modes.
Update remaining old-field test literals mechanically to canonical defaults
and explicit snapshots, preserving tests' original non-mode purpose.

**Verify:** `CGO_ENABLED=1 go test ./internal/app/... ./internal/llm/routing ./internal/tools/... ./internal/acp -count=1` passes.
Run `rg -n 'ApprovalMode|ModePlan|ModeDefault|ModeEdit|ModeCopilot|SetSessionMode|approval_mode|guardrail_dynamic_argv0|auto_approve' internal web`:
remaining matches must be raw migration input/diagnostics/fixtures, historical
text, or unrelated feature vocabulary. No hidden enum, mode setter, shipped
blanket deny, or runtime legacy branch remains.

## Task 48: Update current documentation and cross-surface regressions

**Goal:** Public guidance and an integration regression suite describe the shipped model.

**Files:** `README.md`, `docs/ARCHITECTURE.md`, `docs/system-access-mode.md`,
`docs/interaction-modes/feature-spec.md`, this plan;
new `internal/app/interaction_contract_test.go`,
`internal/acp/interaction_contract_test.go`,
`web/bridge/interaction_contract_test.go`.

**Steps:** Update current user-facing mode/config/command guidance and System
scope wording, linking this spec. Mark superseded mode behavior in the current
System doc; do not rewrite historical specs/plans. Add owned-fixture integration
tests through real runtime/ACP/bridge seams, using fake providers and runners:
`TestInteractionContractActPlanningSkillReview`,
`TestInteractionContractApprovalThenSingleLaunch`,
`TestInteractionContractAskBlocksFutureChildPipelineAndWatchWrites`,
`TestInteractionContractACPReportsSameCommittedTuple`, and
`TestInteractionContractBridgeReconnectAndCapabilityGrant`. Place each test in
its relevant package; bridge tests use a fake ACP child and owned JSON, never
an internal import. Exercise cancellation, reload/workspace changes and session
replacement between presentation and admission. Update spec implementation
status only after verification actually passes; record approved deviations if
the user later authorizes any.

**Verify:** `CGO_ENABLED=1 go test -race ./internal/app ./internal/acp ./web/bridge -run 'InteractionContract|WebIsStdlibOnly' -count=1` passes; browser suites from Tasks 41–43 verify UI behavior against the same wire shapes.

## Task 49: Run final project verification and produce the implementation handoff

**Goal:** The complete implementation is formatted, built, tested, and ready for review with evidence and limits.

**Files:** Only files changed by Tasks 2–48; generated web assets under
`web/bridge/static/` according to existing repository tracking conventions;
this plan's execution record.

**Steps:** Format changed Go files, run the exact checks below, and compare
failures to Task 1's baseline. Fix feature regressions in their owning package
before calling the change complete. Rebuild SPA before webbridge because it
embeds generated assets. Do not add a nonexistent npm lint/typecheck script.
Review the final diff for the spec coverage below, unrelated staged changes,
credentials and grant persistence. Report remaining baseline failures with
evidence; do not count them as passing. Record final task commits and checks,
then present the implementation for review through the repository's reviewer
workflow. This task does not authorize merging, deployment, or publication.

**Verify:** All feature tests pass; format/diff checks are clean, or any
baseline-only failure is explicitly identified.

```sh
git diff --check
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go test -race ./internal/tools/policy ./internal/app/session ./internal/acp ./internal/agent ./internal/app
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go build -o /private/tmp/marshal-interaction-modes ./cmd/marshal
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-interaction-modes go test ./test/usability/... -run TestScripted -v -timeout 5m
```

In `web/ui`:

```sh
npm test
npm run build
```

Back at repository root:

```sh
CGO_ENABLED=1 go build -o /private/tmp/marshal-interaction-webbridge ./cmd/webbridge
CGO_ENABLED=1 go test ./web/bridge -run TestWebIsStdlibOnly -count=1
git diff --check
```

## Spec coverage and plan self-review

Acceptance numbers refer to the feature spec's numbered required cases.

| Spec cases | Implementation tasks |
|---|---|
| 1–2: defaults, transitions, atomic patch, persistent labels | 2–8, 22, 25–34, 37–43 |
| 3: skill planning/review in Act; Auto review gate | 18, 20, 26, 47 |
| 4–5: commit messages versus nested executable behavior | 10–14, 19 |
| 6: destructive approval, Auto, no second denial | 13–17, 19, 47 |
| 7–10: targets, installs, rules, publishing, floor, uncertainty | 10–17 |
| 11: enforced Ask, network/MCP, reads and bookkeeping | 7, 14–24 |
| 12: admitted work continues; future child/watch/pipeline work blocked | 7, 16, 19, 21–24, 27, 35, 47 |
| 13: revision, approval/cancel/reload/workspace/shutdown races | 6–9, 15–17, 19, 22–24, 29, 32–35, 47 |
| 14–15: lifetime, reload, every legacy/default source | 3–8, 22–23, 30–43, 47 |
| 16: ACP/browser/TUI agreement and no unrequested Auto | 8, 18, 22, 26, 28–43, 47 |
| 17: prompt/schema, access panel, lifecycle, sudo, web boundary | 9, 16–20, 22–24, 27–29, 35, 37–49 |

Author self-review: all tasks have concrete files, pinned decisions, named new
tests and runnable package checks; shared contracts precede consumers; runtime
authority/policy source is live for old children; no waits occur under admission
locks; exact consent cannot become a reusable prefix; client changes preserve
false/absence and canonical output. Sequential execution avoids shared-file
parallelism. The pure new-file implementation is compiled separately during
plan authoring. Final source/test checks above are instructions for execution,
not claims that this unimplemented feature has passed them.

## Execution record

Not started. The user approved the feature spec and requested this written
plan. Separate plan approval is still required before Task 1 or product edits.
