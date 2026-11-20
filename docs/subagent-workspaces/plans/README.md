# Subagent workspace implementation plans

Approved specification: [feature-spec.md](../feature-spec.md).
Planning baseline: `55a91a35` (runtime code at `bbdc4486`).

Execute in order, on one integration branch. Each plan is independently
testable; later plans consume the interfaces pinned below. Commit required
documentation and interfaces before dispatching an implementation child.

1. [Execution facts and paths](2026-11-20-p1-execution-facts-and-paths.md)
2. [Owned child dispatch](2026-11-20-p2-owned-child-dispatch.md)
3. [Parent integration and lifecycle](2026-11-20-p3-parent-integration-and-lifecycle.md)

P1 repairs existing execution without changing the delegation default. P2
switches the default and its instructions atomically, returning retained task
branches; it does not promise integration tooling before P3. P3 completes the
parent workflow. Tests for dependency scheduling distinguish independent
children from a child that needs a prior child's integrated commit.

## Shared contracts

These signatures are implementation decisions for the approved behavior.
Each owning task creates its definitions and tests; subsequent tasks consume
them without renaming. Types below use ordinary exported Go fields and JSON
lower_snake_case for durable records. Durations retain existing native APIs.

### P1 registry and native execution

In `internal/tools/registry/command.go`:

```go
type CommandFacts struct {
    Command, Dir, WorkspaceRoot, HomeDir string
    ReadRoots []string
    Env map[string]string // only facts the analyzer actually knows
    Environment []string `json:"-"` // captured backend shell input; never log
}
type CommandPreparer func(context.Context, ToolCall) (CommandFacts, error)
```

`registry.Tool.PrepareCommand CommandPreparer` prepares the actual native
command; optional tools without shell execution leave it nil. Preserve this
field through tool cloning/views. Policy's `ShellInput` becomes an alias of
`registry.CommandFacts`, removing duplicate field lists without a reverse
registry-to-policy import. Policy fingerprints include analyzed `Env`, never
the full environment. An edited command starts a fresh preparation/evaluation.

`native.CommandEnvironmentSource` has
`CaptureCommandEnvironment(dir string) ([]string, map[string]string, error)`.
Production ordinary runners implement it. `CommandRequest.Environment` and
`LaunchSpec.Environment` are `[]string`; non-nil means the captured environment,
including an intentionally empty list. Nil preserves legacy direct-run
behavior. Backend capture applies its existing allow/deny policy first;
container shell inputs and the container CLI's host environment are distinct.
An unknown image/login-script value is unknown to policy, not a host guess.
This pins process launch inputs, not arbitrary subsequent shell startup code.

`registry.Tool.ResolvePath func(context.Context, string, bool) (string, error)`
resolves a path using that toolset's rules (`bool` means write). It is used by
policy target preparation, patch previews and retry hints. Native methods keep
System and Marshal-owned read exceptions around the shared path resolver.

In `internal/pathutil/grants.go`:

```go
type RootGrant struct { Path, Alias string; Read, Write bool }
func ResolveGrantedPath(primary string, grants []RootGrant, path string, write bool) (string, error)
```

`native.Options.PathGrants []pathutil.RootGrant` adds operation-specific grants.
Explicit legacy `AdditionalRoots`/`NamedRoots` remain read/write grants unless
the caller supplies a narrower grant for that same root/alias. Dynamically
added project/archive references are read-only. Primary is readable/writable.
Aliases resolve one target from their own root; ordinary relative paths resolve
one target from primary. Resolve symlinks before checking permitted roots.

### P2 durable ownership

Create `internal/subagentwork`, which imports no `app/session`, `agent`,
`tools/native`, `db`, transport or UI package. It may use `internal/worktree`.
This keeps DB/session/native adapters free of dependency cycles.

```go
type Mode string // ModeIsolated="isolated", ModeShared="shared"
type Binding struct {
    Mode Mode
    Root, ProjectRoot, Repository, Branch, BaseSHA string
}
type Target struct { Root, Repository, Branch, BaseSHA string }
type Verification struct { Command, Outcome string; ExitCode *int }
type Record struct {
    Version int // 1
    OwnerSessionID, TaskKey, InvocationID string
    Binding Binding
    Target Target
    Revision uint64
    Outcome, Candidate, SourceError string
    Commits, ChangedPaths []string
    Dirty *bool // nil means unknown
    ReportMessageID int64
    Verification []Verification
    Integration *Integration
    CleanupState string
}
type Integration struct {
    OperationID, Kind, State, ExpectedSource, BeforeTarget, ResultCommit, SnapshotHash, Reason string
}
type Store interface {
    Create(context.Context, Record) (Record, bool, error) // bool: created
    Get(context.Context, string, string) (Record, error) // owner, key
    ByInvocation(context.Context, string, string) (Record, error)
    CompareAndSwap(context.Context, Record, uint64) (Record, error)
    List(context.Context, string) ([]Record, error)
}
```

`Repository` is the canonical absolute git common-directory identity, not a
branch name. DB operations are owner-scoped. Unique `(owner, invocation)`
deduplicates one logical dispatch; `(owner, task_key)` is the primary key.
CAS mismatch returns `subagentwork.ErrStaleRecord`.

`PrepareRequest` fields are `OwnerSessionID, TaskKey, InvocationID, ProjectRoot,
ParentRoot, BaseCommit string`, `Mode Mode`, `Setup worktree.WorktreeSetup`,
and `IsolatedSupported bool`. The app derives support from
`native.LinkedWorktreeRunner` (`SupportsLinkedWorktrees() bool`), never a model
argument. Exec/restricted/passthrough support host linked worktrees; the current
container backend does not, because its cwd-only mount excludes common git
metadata. Refuse unsupported isolated dispatch before allocating or calling a
model. A deterministic capability fixture needs no running container.
`Completion` fields are `Outcome string`, `ReportMessageID int64`,
`Verification []Verification`. `NewService(store Store, git Git, opts ...Option) *Service`
creates an instance-scoped owner; it does not create command supervisors.
`WithRecordObserver(func(Record)) Option` installs an observer at construction.
After each successful record write/read reconciliation, it publishes a deep
copy outside store/service locks. `State.ApplySubagentWorkspace(Record)`
retains only newer revisions owned by that session; `State.SubagentWorkspaces()
[]Record` returns deep copies for views and postmortems. No callback performs
another write of that same record.

```go
func (*Service) Prepare(context.Context, PrepareRequest) (Record, error)
func (*Service) Complete(context.Context, string, string, Completion) (Record, error)
func (*Service) Inspect(context.Context, string, string) (Record, error)
```

`Git` embeds `worktree.GitOps` and adds `RepositoryID(dir string) (string,error)`,
`ResolveCommit(dir, oid string) (string,error)`,
`CommitIDs(dir, from, to string) ([]string,error)`,
`ChangedPaths(dir, from, to string) ([]string,error)`, and
`IsAncestor(dir, older, newer string) (bool,error)`.
Implement these on `worktree.CLIGitOps` with argv, not interpolated shell code.

`agent.SubagentRequest` gains `TaskKey string`, `Binding subagentwork.Binding`
and `Target subagentwork.Target`. `SubagentMeta.WorkKey string` lets registration
preserve the already-allocated key (empty retains the current minting path).
`WithSubagentWorkspaceService(*subagentwork.Service)` configures `agent.run`;
`WithAwaitWorkspaces(*subagentwork.Service)` configures retained-key lookup.
Both are instance-scoped options. `WithIsolatedWorktreeSupport(bool)` supplies
the backend capability to `agent.run`; production dispatch requires the
service and an explicitly detected capability, with no shared-mode fallback.

### P3 root use and guarded mutations

All Marshal root users participate in one per-repository shared/exclusive
lease protocol, not just lifecycle callers. In `internal/worktree` define:

```go
type UseIdentity struct { OwnerSessionID, ActorID, ExecutionID, Kind string }
func AcquireWorkspaceUse(context.Context, string, string, UseIdentity) (*UseLease,error)
func (*UseLease) MarkStarted(pid int, processIdentity string) error
func (*UseLease) Release(terminal bool) error
func AcquireTargetLock(context.Context, string, string) (func(),error)
```

The two string arguments are canonical repository and workspace root.
Use kinds are `tool`, `command`, `child` and `pipeline`; durable use-note
version is 1, and an unknown version is unknown use, never an idle root.
Ordinary operations hold shared root-use leases; mutation takes the exclusive
lease on those same roots. Acquire multiple roots in canonical sorted order;
never upgrade a held shared lease. Native `Options.WorkspaceUse` is
`func(context.Context,string,string)(*worktree.UseLease,error)` (root, kind),
wired by app. Direct native file/search/git/diagnostic/setup command paths
hold it for their operation. Managed `LaunchSpec.WorkspaceUses
[]*worktree.UseLease` transfers leases into JobManager until observed terminal
settlement, including detached execution. Every child holds a root-use lease
through its model lifetime; commands keep separate leases afterward. Lifecycle
internals use their existing exclusive lease instead of reacquiring shared.

Per-root durable use notes in the common git directory mirror existing command
identities and process-start evidence. They are lock/reconciliation evidence,
not another command supervisor or machine-global registry. A crash without
observed terminal settlement leaves unknown use; process-owner death or an
unlocked file alone does not establish that orphan commands stopped. Exclusive
lifecycle acquisition refuses live/unknown use from any session or process.
Shared-lock acquisition and use-note publication must be atomic with respect
to the exclusive lock. Nil WorkspaceUse is allowed only in explicit test or
nongit fixtures; a production git root never silently omits participation.


```go
type Mutation struct { OwnerSessionID, TaskKey, ExpectedHead string }
type IdleGate interface {
    AcquireIdle(context.Context, string, []string) (func(), error) // owner, roots
}
type MutationDeps struct {
    Idle IdleGate
    Snapshot func(context.Context, string) (string, error) // exact target root
    Rescan func(string)
    Admit func(context.Context, string) (Admission, error) // effect key
}
type Admission interface { Commit() error; Release() }
func (*Service) Integrate(context.Context, Mutation, MutationDeps) (Record, error)
func (*Service) Discard(context.Context, Mutation, MutationDeps) (Record, error)
func (*Service) Reconcile(context.Context, string, MutationDeps) ([]Record, error)
type PreparedMutation struct {
    RecordRevision uint64
    Repository, SourceRoot, SourceHead, TargetRoot, TargetBranch, TargetHead string
}
func (*Service) PrepareMutation(context.Context, Mutation) (PreparedMutation, error)
func (PreparedMutation) EffectKey() string
```

Mutation methods type-assert a `MutationGit` extending P2 `Git` with:
`MergeExact(ctx context.Context,dir,candidate,message string) (string,error)`;
`MergeState(dir string) (string,error)` (empty means idle);
`Parents(dir,commit string) ([]string,error)`.
Return an explicit unsupported error if unavailable. Reuse existing abort,
status, branch and worktree operations. `MergeExact` runs `merge --no-ff
--no-edit -m <message> <exact SHA>`, retaining history and operation trailer.

`NewSubagentIntegrateTool(owner string, svc *subagentwork.Service, deps
subagentwork.MutationDeps, authorizer *policy.Authorizer) registry.Tool` and the corresponding
`NewSubagentDiscardTool` live in `internal/agent/subagent_lifecycle.go`.
JSON arguments are required `task_key` and `expected_head`; neither accepts
arbitrary paths/branches/owners. Their risks are workspace_write/destructive.
`Tool.OwnsAdmission bool` leaves the opaque proof unconsumed for these tools;
their handler fills `MutationDeps.Admit` from that proof, the real authorizer
and the exact tool arguments. The service requires it at the mutation boundary.
`Tool.PrepareEffect func(context.Context, ToolCall) (string,error)` returns
`PrepareMutation(...).EffectKey()` before evaluation. Add `policy.Call.EffectKey
string` to its fingerprint. The key is a deterministic digest of every
`PreparedMutation` field; the service recomputes it under the workspace lease
and passes it into `Admit`. A changed record/source/target needs a new proof.
Unchanged inspections do not advance record revisions. These definitions bind
mutation target facts without putting model-controlled paths into the schema.
`WorkspaceEffectManaged` skips default root preflight only because these
handlers own exact-root serialization, admission and snapshotting; it does
not skip policy or end-gate admission.

DB adapters, idle gates, LSP rescans and snapshots are wired in `internal/app`.
Add no UI-owned orchestration and no `internal/` import beneath `web/`.

Record outcome strings are `preparing`, `prepared`, `running`, `done`, `failed`,
`stopped`, `partial`, `interrupted` and `unknown`. Integration states are
`intent`, `integrated`, `no_changes`, `conflict`, `refused`, `unknown` and
`discarded`; `Kind` is `integrate` or `discard`. Cleanup states are `retained`,
`complete`, `integrated_cleanup_pending` and `unknown`. Bind each record to its
version/revision and preserve execution outcome independently of integration.

## Specification coverage map

| Approved requirement or acceptance case | Owning tasks |
|---|---|
| One canonical target; absolute contained paths; new-file and symlink safety; alias limits | P1 Task 1 |
| Backend environment; configured tester command; identical approval/launch facts; setup | P1 Tasks 2–3 |
| Root rebind/revocation during admission; fixed accepted launches | P1 Task 3 |
| Initial workspace truth; cache-stable tail; exact-copy previews/hints | P1 Task 4 |
| Parent active worktree base; commit-first dirt behavior; exact reviewer base; non-git modes | P2 Tasks 1, 3 |
| Stable task allocation; invocation deduplication; partial creation; owner-scoped durable records | P2 Tasks 1, 3 |
| Child file/index/authority/diagnostics/undo/snapshot binding; shared parent rebind; missing roots | P2 Task 2 |
| Final registry exclusions; no indirect reload/escalation; existing pipeline/swarm scopes | P2 Task 2 and P1 Task 3 |
| Terminal, partial, stopped, panic, setup failure, restart and config-reload behavior | P2 Task 3 |
| Root-use leases covering detached/unknown work; multiple processes and callers | P3 Task 1 |
| Exact-candidate serial integration; target drift/dirt; conflict abort; no-change review | P3 Task 2 |
| Intent/result/cleanup crashes; idempotent receipts; retained dirty failures; explicit discard | P3 Task 2 |
| Parent-only owned effects; fresh live policy at mutation; exact-root snapshot/rescan | P3 Task 3 |
| ACP/bridge/Dart/postmortem evidence; factory continuation; legacy unavailable metadata | P3 Task 4 |
| Real concurrent implementation and review; combined validation; conflict/cancel/shared/restart journeys | P3 Task 4 |

Planning self-review: checked specification coverage, per-task steps, shared
signatures, the five review-focus cases in each plan, and plan/spec proportion.
Execution still requires the commands and resulting evidence in each task.
