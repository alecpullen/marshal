# Subagent workspace ownership and parent integration

Date: 2026-11-20

Status: draft for design approval. Implementation plans follow approval.

## Intent

Parallel implementation children run in separate git worktrees by default.
Each returns committed work for its parent to review and integrate into the
parent's working branch. The parent may explicitly choose a shared worktree
for a smaller task. A workspace choice binds the child's tools and authority;
mentioning a path in its prompt is not a binding.

The parent commits the changes its children need before dispatch. This is a
workflow instruction, not an automatic commit or a mechanism for copying a
dirty checkout. Dispatch names the exact starting commit.

This work fixes the workspace, path and approval defects that made existing
subagent work drift between checkouts. It also fixes the production child
registry's reintroduction of tools that the child scope excludes.

## Decisions and approaches

The user selected **separate worktrees with parent integration**, retained an
explicit shared-worktree option, and chose **parent commits before dispatch**
instead of copying uncommitted edits into a child.

Three approaches were considered:

| Approach | Tradeoff | Decision |
|---|---|---|
| Separate worktrees; parent integrates | Parallel changes have distinct histories; the parent resolves integration and validates the combination | Selected default |
| Separate worktrees; separate PRs | Each task is independently reviewable, with more branches and review work | Parents can still publish task branches when requested; publishing is outside this feature |
| One shared worktree | Simple for small tasks, with shared edits and build state | Explicit option; never a silent fallback |

The operational choices below are the proposed design for approval, including
merge topology, retention, scope and recovery. They are not additional user
decisions already made in conversation.

## Evidence and existing machinery

At investigation commit `bbdc4486`, temporary deterministic probes reproduced:

1. Child A's `workspace.worktree` changes the parent's active root; child B
   subsequently writes into A's worktree.
2. Shell authorization captures the original project cwd while native launch
   uses the active worktree cwd.
3. The system prompt still describes the original project root after a switch.
4. A worktree-prefixed relative write inside that worktree creates a nested
   `.marshal/worktrees/<name>/` tree instead of receiving the existing diagnostic.
5. A sibling-relative target under an explicitly allowed project root is
   rejected because the relative path is joined separately against each root.
6. The production child registry includes `question.ask` even though
   `SubtaskScopeView` excludes it because the child cannot collect a user answer.

Remote and local postmortems describe wrong-worktree writes, shared-root
drift, stale reads, sibling write refusals and cleanup blocked by System scope.
These observations establish recurring symptoms; an individual stale-edit
refusal does not by itself establish a workspace bug.

Reuse `internal/worktree` for git mechanics and setup, the conversation command
service for process ownership, existing subagent work keys and reports for
identity, and the existing interaction authority for policy. Pipeline dispatch
already binds a separate child state and registry. Ordinary `agent.run`
currently binds native workspace state to the parent; that coupling changes.

PR #182's missing-worktree recovery remains applicable to interactive parent
sessions. Dispatcher-bound children fail in their assigned workspace when it
disappears; they do not recover into the parent's checkout.

## Goals and boundaries

- Make isolated delegation the default for ordinary Marshal `agent.run`,
  including factory worker sessions that use that tool.
- Make shared delegation explicit and keep every dispatched root stable.
- Use the same resolved execution facts for permissions and execution.
- Give the parent scoped integration and cleanup operations with durable,
  inspectable results.
- Preserve live permission revocation, child ceilings, command ownership,
  snapshots, read-before-write protection, task identity and cancellation.
- Keep the workflow usable without System access for ordinary owned worktrees.

This is git checkout isolation and tool workspace correctness. It does not
introduce a new OS sandbox, make every shell interpreter enforce a filesystem
boundary, or remove explicitly granted System capabilities. It does not add
nested delegation, live child resumption after process death, arbitrary
worktree adoption, automatic PR creation, or automatic final integration into
an outer branch such as `main`.

Built-in `/swarm` retains its staged, single-implementer coordination. Its
workspace and shell facts receive the correctness fixes, but its roles are
not automatically converted into separate task branches. `/sdd` retains its
existing controller-owned worktree and role scopes.

## 1. Dispatch contract

`agent.run` gains `workspace`, accepting `"isolated"` and `"shared"`.
Omitted means `"isolated"`; unknown or empty explicit values are rejected.
The parent chooses per call. A model/provider choice does not change workspace
mode. No new global default is needed in the first release.

Isolated dispatch also accepts an optional `base_commit`: an exact existing
commit object ID in the same repository. Omitted means the captured parent
HEAD. This lets the parent send an independent reviewer to an implementation
child's committed candidate without moving either workspace. An explicit base
is reported in the receipt and never changes the captured parent integration
target. Shared mode rejects `base_commit` rather than pretending to check it
out in a shared tree.

### Before dispatch

Parent instructions require it to:

1. Finish and commit its own changes needed by the child, including required
   plans and newly introduced interfaces. Stage explicit owned paths.
2. Leave unrelated user changes outside that commit. If an essential change
   cannot safely be committed, settle that before dispatching dependent work.
3. State the deliverable, file scope, verification commands and dependencies.
4. Select shared mode only when its shared files and build state are suitable.

There is no whole-checkout clean-tree requirement at isolated dispatch:
unrelated tracked or untracked user changes may remain. Dispatch neither
stages nor commits them. The isolated child starts from committed HEAD only,
and its receipt makes that fact explicit. The runtime does not guess which
uncommitted files a task requires.

### Capture and allocation

Admission allocates a stable task identity before workspace creation. Capture
the parent's current **active** root, checked-out branch/ref and HEAD together
with its owning project and permission context. The source commit comes from
that active checkout by default, which may itself be a worktree, not from the
immutable project-root checkout. An explicit `base_commit` is validated
against that same repository before creation.

For isolated mode, create a uniquely named owned branch and worktree from that
exact commit through `internal/worktree`. Reuse configured seeding and setup
hooks with the child's bounded authority. A repeated logical admission must
not create a second branch or child. A setup or construction failure returns
a definite failed dispatch and records any retained owned artifacts.

Non-git isolated dispatch and unsupported worktree backends return an
actionable refusal. They do not fall back to shared mode. Shared dispatch is
available for non-git directories.

For shared mode, bind the child to the captured active root. A later parent
workspace switch does not redirect that child. Shared children share files,
not a mutable pointer to whichever checkout the parent currently uses.

The dispatch receipt, retained metadata and completion report expose task key,
workspace mode, canonical worktree path, branch, starting SHA and captured
parent integration target. Do not require a model to reconstruct them from a
natural-language brief.

## 2. Workspace ownership and tool scope

Every child owns a fixed workspace binding. Its session authority, native
registry, command cwd and change tracking agree on that binding. Separate
workspace location from the conversation-owned services that hold permission
rules, process accounting and, where applicable, shared-file undo history.

| Concern | Isolated child | Shared child |
|---|---|---|
| Relative file, search, git and shell paths | Child worktree | Captured parent active root |
| Source writes and diagnostics | Child worktree | Captured shared root |
| File read/write ledger | Child workspace, distinct from sibling paths | Same workspace ledger, distinct writer identity |
| Snapshot capture and restore target | Child worktree | Captured shared root |
| Command lifetime and capacity | Parent conversation service; child actor and fixed cwd | Same |
| Live policy and permission revocation | Parent owner, bounded child actor | Same |
| Missing assigned directory | Explicit failure; no root recovery | Explicit failure; no root recovery |

Removing an isolated child's branch must not make a surviving process or
snapshot restore operate in the parent tree. Relative tool previews and patch
hints also use the bound root. Project-wide semantic indexes may remain
project-scoped under their existing contract; their results must not be
presented as proof of a child's uncommitted worktree contents.

Children cannot call workspace rebinding, integration or disposal operations.
Filter the **final** registry after native registration and inheritance. Apply
the existing child exclusions there, including `agent.run`, `agent.await`,
`agent.kill`, `agent.steer`, `question.ask` and deferred tools; also exclude
`workspace.worktree`, `workspace.finish`, `agent.integrate`, `agent.discard`,
`mode.request`, `tools.select` and privileged execution. Filtering must not
leave a copied handler that can reload the excluded surface from an unfiltered
registry. A child reports a blocker to its parent rather than
opening an unanswered local interaction or seeking its own escalation.

Explicit shared mode does not guarantee independent tests or a private git
index. Instructions require sequential shared writers unless the task plan
establishes disjoint files and compatible verification. Shared children leave
committing and integration decisions to the parent. Isolated children commit
their scoped changes before reporting successful implementation.

## 3. Execution and path correctness

### One set of facts per invocation

Capture command bytes, canonical cwd, workspace root, read roots and relevant
environment once for an invocation. Policy evaluation, its authorization
fingerprint, accepted admission, process launch and execution evidence all
consume those same facts. Native launch must not re-read a potentially
different root after authorization. A stale authority snapshot causes
re-evaluation, never substitution of a new cwd under an old proof.

Use this contract for ordinary parents, isolated/shared children, `/swarm`,
pipeline workers, configured `test.run` and workspace setup hooks. Preserve
the existing explicit privileged-execution seam; do not route it around its
authorizer or prompt requirements.

### Resolve a target once

Resolve a relative path from its active binding once, including symlinks and
the nearest existing ancestor for a new file. Compare that **same target**
with every root permitted for the requested operation. Never join the same
relative string independently to alternate roots.

Distinguish read roots from write roots. A project reference or seeded archive
may be readable without becoming writable. The child's parent checkout and
sibling worktrees are not implicitly added to its source-write roots. Named
artifact roots keep their explicit scoped permissions.

Accept canonical absolute file paths inside the actor's permitted roots for
both reads and writes. An absolute spelling alone does not require System.
An absolute write outside the actor's write roots still requires the existing
explicit capability and policy path; an absolute read outside its read roots
remains subject to the existing read exceptions and System rules. Preserve
symlink containment and do not widen privileges while improving path spelling.

Fix the repeated-worktree diagnostic against the effective joined target,
including the common case where the argument has one worktree prefix and the
bound root supplies the other. A mistaken prefix produces an actionable
relative-path suggestion before a write. Do not silently rewrite a request to
another file. Preserve access to a literal existing nested tree rather than
assuming every repeated segment is a typo.

### Truthful workspace guidance

The initial system prompt names the actual bound root. For an interactive
parent that changes roots mid-turn, append authoritative workspace state after
history or in the working-state tail. Never rewrite the cache-stable prompt
prefix mid-turn. Include current mode/root, allowed reference roots and
commit-first guidance where relevant. A child may not infer its workspace
from the original launch directory.

## 4. Completion and parent integration

Child execution status and integration status are separate. A successful
report does not mean its changes are present in the parent. Isolated child
reports include source base, current candidate SHA, commit list, changed
paths, dirty state and verification evidence. A partial, failed or stopped
child remains visibly partial, failed or stopped even if some commits exist.

Parent instructions require inspecting the candidate and its report, obtaining
the review required by the task workflow, and checking relevant verification
before integrating. The parent chooses dependency order and integrates one
candidate at a time. It then validates the combined result before reporting
the overall task complete. These are workflow requirements; a tool must not
claim it ran a reviewer or a test merely because the parent's prose says so.
An independent source reviewer can run in an isolated workspace with
`base_commit` set to the inspected candidate SHA. Its report names that SHA,
and the parent integrates that same candidate after settling any findings.

Add parent-only `agent.integrate`, addressed by the stable task key and the
exact candidate SHA the parent inspected. It acts on recorded owned workspace
metadata, not an arbitrary path supplied by a model. Use a non-fast-forward
merge to retain the child's commits and establish explicit task ancestry.
The target is the captured parent active checkout and branch, including when
that checkout is itself a worktree. It is not automatically the project's
main checkout.

Integration checks, under the existing write/admission gate and a target
serialization boundary:

- The child is observably terminal; its source workspace has no running or
  unknown command users, including detached commands.
- The source path, registered git worktree, branch and repository identity
  match the recorded ownership. Its HEAD matches the requested candidate and
  its working tree has no uncommitted or untracked changes.
- The target still exists, is on the recorded branch and has no conflicting
  operation, dirty index or tracked edits. Unrelated untracked files may stay;
  refuse if integration would overwrite any of them.
- Recheck the target HEAD at accepted mutation. Previous successfully
  integrated children may have advanced it; do not require it to equal the
  original dispatch base. A concurrent unrelated change triggers a fresh
  evaluation instead of mutating under stale assumptions.

Conflicts return the paths and preserve both committed branches. Abort only
the merge operation this tool started, restore its target to the pre-operation
state and retain the source for parent-directed resolution. Do not stash,
reset, rebase, commit unrelated work or resolve conflicts silently. A source
SHA that is already proven integrated produces an idempotent receipt. A
successful no-change child can be acknowledged without creating an empty merge.

Shared children do not use `agent.integrate`: their edits are already in the
captured shared checkout and the parent reviews and commits them there.

## 5. Retention, disposal and recovery

The parent owns child worktree lifecycle. Child completion, failure,
cancellation and parent turn completion do not delete the branch or worktree.
The conversation's existing end gate still stops and accounts for its work.

After a successful integration, attempt cleanup of that owned source
worktree and branch only when no process uses it and it remains clean and at
the integrated candidate. If cleanup fails, retain an explicit
`integrated_cleanup_pending` outcome; do not report the merge as failed or
retry it. The parent's workspace binding does not change during cleanup.

Add parent-only `agent.discard`, also addressed by task key and expected source
SHA, for an owned terminal child the parent intentionally abandons. Refuse
while it is busy, unknown, dirty, has untracked files, has changed HEAD, or
fails ownership checks. Dirty failed work remains available for salvage;
this feature does not add a force-delete shortcut. Apply the existing
destructive-operation approval rules to discard. Correctly identified owned
worktree cleanup does not require System solely because the path is a sibling
of the parent's active worktree. External scratch worktrees and arbitrary
paths keep their existing restrictions.

Retain a durable parent-session task record containing identity, source and
target bindings, original base, observed candidate, execution outcome,
integration intent/result and cleanup result. Prefer the existing project
database and retained work/report identity; do not create another daemon or
global registry. Journal integration intent before mutating git, and journal
its resulting commit before cleanup. Duplicate calls for the same candidate
reconcile the same operation. Reusing a task key with a different candidate
must not silently replay an earlier success.

After restart, reconcile the recorded operation with exact repository and
branch/commit evidence. A candidate proven integrated in the captured target
is not merged again; a missing or changed checkout is reported accurately.
An interrupted child's execution becomes unknown/interrupted rather than
successful, and no new child is launched automatically. Worktree retention
does not imply that the child model or process can be resumed.

`agent.await` can inspect a retained child by task key as well as its existing
live numeric handle, returning known reports and workspace/integration state.
Legacy numeric handles keep their current behavior. Retained lookup does not
revive a child or infer process liveness from a directory's existence.

## 6. Client and compatibility contracts

Expose additive workspace and integration metadata through existing subagent
views, stack detail, reports and postmortem evidence. Identity is parent
session plus stable task key; a numeric handle or branch name alone is not
ownership. Clients render known, pending, failed and unknown outcomes without
claiming an unintegrated child changed the parent branch.

Use the existing generic child/tool detail and report surfaces first; no new
Studio screen is required. If an existing structured wire projection needs
fields, extend `internal/acpwire` and the independently defined
`web/bridge/contract` and regenerate Dart with the existing currency test.
The TUI remains rendering-only, `web/` remains standard-library-only, and the
Flutter client retains its model/client/UI boundaries.

New omitted `workspace` values use isolated mode. Update the tool schema,
descriptions and shipped delegation/execution skills in the same release so
the parent explicitly selects shared mode for workflows that need it.
Previously running children are not rebound by a configuration reload; old
retained children without workspace ownership metadata are treated as legacy
shared/unknown and cannot be disposed of through the new owned-worktree tools.
Unknown mode and unsupported capability requests fail visibly.

Update `docs/ARCHITECTURE.md`, the tool family references, the worktree skill,
parallel-dispatch skill and `marshal-executing-plans`. Remove the blanket
claim that delegated tasks share a worktree and the instruction forbidding
isolated children from committing. Preserve the latter instruction for shared
children. Document that separate worktrees permit independent implementation,
while dependency order still decides which tasks can run concurrently.

## Verification and acceptance

Use real temporary git repositories, scripted providers, isolated auth,
in-process transports and the production registry/factory/admission paths.
Ordinary checks do not read developer config, credentials or running services.
`scripts/test.sh` is the test entry point; use the affected packages and
uncached runs. Run the relevant race tests for shared ownership and admission.

| Scenario | Required observation |
|---|---|
| Parent is already in a worktree | Each child starts at that checkout's exact committed HEAD and integrates into that branch |
| Parent commits required interfaces before fan-out | Every child sees those interfaces and the same captured base for the wave |
| Unrelated parent dirt | It is neither committed nor copied into children; integration refuses relevant dirty target state |
| Two isolated children edit the same path | Each has its own file/index/snapshots; parent integration handles any real conflict |
| Shared child and later parent rebind | Child tools and authority stay on the original shared root |
| Authorization and launch | Command bytes, cwd, root and environment match exactly across the full boundary |
| Config/revocation/root change during admission | Stale proofs never launch different facts; accepted launches keep their captured facts |
| Absolute contained path, allowed sibling read, new-file tail, symlink pivot | One canonical target is tested against operation-specific roots; no privilege widening |
| Mistaken worktree prefix | No nested file is created; a useful relative-path diagnostic is returned |
| Final production child registry | No rebinding, lifecycle, nested-delegation, privilege or unanswered-interaction tools are reachable |
| Partial/stopped child or detached command | Work and evidence remain retained; integration/discard cannot race active or unknown use |
| Repeated integration and crash after merge | One integration result, preserved ancestry, no duplicate merge or accidental new target |
| Failed cleanup or dirty discard | Successful integration stays successful; source retention is explicit and user data stays present |
| Non-git shared / non-git isolated | Shared runs at its binding; isolated refuses with the explicit shared alternative |
| Pipeline and swarm | Their existing role scopes and stop behavior remain; policy and execution use their actual roots |
| Factory delegation | Waiting and continuation incorporate terminal child reports and actual integration state |

The acceptance journey dispatches two genuinely concurrent isolated children
from a parent feature worktree, verifies their separate edits and commits,
integrates both serially, validates the combined result, and confirms that the
project-root checkout and unrelated work remain untouched. Repeat with one
conflicting candidate and one cancelled child, then exercise explicit shared
mode and restart reconciliation. Add direct regression cases for each
diagnostic probe rather than relying only on mocked root getters.

## Delivery shape

One feature specification, followed by three ordered implementation plans:

1. **Execution facts and path correctness.** Align admission/launch, canonical
   target resolution, operation-specific roots and truthful workspace state.
   Include pipeline and swarm callers; this phase is useful independently.
2. **Owned child dispatch.** Add isolated/default and explicit shared dispatch,
   fixed bindings, commit-first instructions, final registry filtering,
   workspace-correct snapshots/ledgers and durable child workspace records.
   Children can complete with a retained committed branch before integration
   tooling ships.
3. **Parent integration and lifecycle.** Add guarded integration/discard,
   recovery and cleanup receipts, metadata projections, updated workflow
   documentation and the full concurrent factory/session acceptance journey.

Keep the default-mode cutover and corresponding skill updates atomic in plan
2. Do not advertise automatic parent integration until plan 3 is available;
the intermediate release describes retained branches and existing explicit
git operations honestly. The plans will pin exact interfaces and test names
after this design is approved.

## Left to the builder

Choose unexported helper names, source-file decomposition, the owned branch
naming encoding, task-record storage details and the exact bounded diagnostic
wording. Reuse existing work keys, locks, git helpers and projection seams.
The plan must name any exported interfaces and persisted schema it introduces,
map every requirement to a task and keep cross-plan signatures consistent.
