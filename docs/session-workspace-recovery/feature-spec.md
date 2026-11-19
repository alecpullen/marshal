# Session workspace recovery: a worktree removed underneath a live session

Status: approved design (from the 2026-11-18 diagnosis); written specification,
not yet implemented.
Date: 2026-11-18.
Source baseline: `ca668c4df739035e1cee7f78083e4dfcce737a91`.
Workflow: [Marshal brainstorming](../../internal/skills/builtin/brainstorming.md),
followed by Marshal's internal plan-writing skill after written-spec review.

## Context

On 2026-11-18 the two Studio plans were merged (#178, #180) and their worktrees
were removed with `git worktree remove`, from a shell, while a Marshal session
was still open in one of them. The next tool call in that session failed:

```
shell.run failed: sandbox: working directory
/home/alec-strix/projects/marshal/.marshal/worktrees/feat-studio-conversation-readability
does not exist
```

Every subsequent tool call failed the same way, including after the agent was
told what the path was. The only way out was `workspace.worktree` with no
`branch` argument — a detach the model did not think of, because the error it
was reading named a path, not an action.

This is a **bounded** change: one new method on the session's workspace
authority, two call sites, one shared sentence. It adds no watcher, no
subsystem, and no wire field.

## What the code does today

| Piece | Behaviour |
|---|---|
| `toolSet.activeRoot()` (`internal/tools/native/native.go:402`) | Returns the session's `ActiveRoot` with no check that it exists. Every tool's working directory derives from it, directly or through `commandRoot()` (`:417`). |
| `resolveConfinedDir` (`internal/sandbox/restricted.go:192`) | `os.Stat`s the directory and rejects with `sandbox: working directory %s does not exist`. Correct: running a command in a vanished directory is worse than refusing it. |
| `restoreWorkspace` (`internal/app/session/workspace.go:125`) | Stats the recorded root **when a session is constructed**; if it is gone, clears the stale row, writes a system notice, and stays at the project root. |
| `SetWorkspace` (`internal/app/session/workspace.go:46`) | The only thing that rebinds the active root; persists the row, publishes `WorkspaceEvent`, bumps the owner's access revision. |

So the recovery exists, and runs exactly one lifecycle too early. A session that
loads into a gone worktree lands safely; a session that is running when its
worktree disappears has no path back, because nothing between `New` and the
next tool call re-checks the directory.

Two details make the failure worse than it looks:

1. **Subagents inherit the parent's root, not the parent's recovery.** A child
   toolset is built with `childOpts.WorkspaceState = parentState`
   (`internal/app/app.go:2413`): per-agent state is the child's, per-workspace
   state is the parent's. So a child spawned into a removed worktree fails
   immediately, on its first tool call, with no way to report anything useful.
2. **The failure is unreadable as an instruction.** The sandbox message is
   accurate about the directory and silent about the remedy, and the remedy is
   a tool call whose schema requires a `branch` that the caller does not want.

## Design

### D1 — The recovery belongs to the workspace authority

New method on `*session.State`: `EnsureWorkspaceRoot() Workspace`.

Not in the tool layer, for three reasons: the active root is per-workspace and
children share it through `WorkspaceState`, so one implementation covers the
parent and every child; the landing has to persist the cleared row, which is
db work the session owns; and the notice is a transcript write, which is also
the session's. The tool layer only asks.

### D2 — It is the load-time landing, not a new one

The recovery lands where `restoreWorkspace` lands a resumed session: the
project root, the stale row cleared, one system notice. The sentence is shared
through one helper so the two landings cannot drift, and so a reader of the
transcript sees the same words whichever way the session arrived there.

The cleared row matters beyond tidiness: it is what stops the *next* resume
from warning about the same worktree a second time.

### D3 — Readers that decide recover; readers that describe do not

`ActiveRoot` has four readers outside `workspace.go`:

| Reader | Class | Uses |
|---|---|---|
| `native.go:402` `activeRoot()` (and `commandRoot`) | decides — the directory every tool runs in | `EnsureWorkspaceRoot()` |
| `internal/agent/execute.go:156` `shellInputFor` | decides — the workspace a command is admitted against, evaluated **before** dispatch | `EnsureWorkspaceRoot()` |
| `internal/agent/snapshot_warning.go:422` | describes — the label on a snapshot | `Workspace()` |
| `internal/tools/native/helpers.go:354` | describes — "last written in a worktree" text | `Workspace()` |

The policy reader is included deliberately. Admission runs before the tool
does, so a stale root consulted there would classify a command against a
directory that no longer exists — over-restricting the very call that would
have recovered the session. Analysing the recovered root keeps the judgement
and the execution in the same directory.

The two describing readers keep the recorded root on purpose: a snapshot label
naming the worktree the work happened in is history, and rewriting it to the
project root would lose that.

### D4 — Fail closed when there is nowhere to land

If the project root is also missing, nothing is recovered: the workspace is
returned untouched and the sandbox reports the truth about the directory the
tool asked for. A recovery that moves a session into a second missing directory
would replace an accurate complaint with a quieter, worse one.

### D5 — Exactly one recovery, however many callers race

Recovery takes a dedicated mutex, re-reads the workspace under it, and writes
the notice only in the branch that actually moved the session. Without that, a
parent and four children noticing the same gone directory between them write
five notices for one event.

Lock order is `rootMu → accessMu (owner) → mu`, documented on the field.
Nothing else takes `rootMu`, so the order cannot be violated by existing code.

### D6 — A root a dispatcher bound is never recovered

Recovery is for a root the session *chose*. A pipeline dispatch rebinds a
child's active root to the dispatch's own worktree
(`internal/pipeline/dispatch.go`), and that child was built from the parent's
launch directory, so its `ProjectRoot` is the user's checkout rather than the
work it was isolated to. Recovering such a child would land an unattended
worker in the user's working tree — writing files outside the isolation the
dispatch promised — while its commands, which are pinned to the dispatch
worktree through `CommandDir`, kept failing there. Before this feature the
child failed loudly in the missing directory and wrote nothing.

So a dispatcher's binding is marked isolated (`BindIsolatedWorkspace`), and
`EnsureWorkspaceRoot` returns it unchanged: no stat, no notice, no move. The
session fails where it was told to work, which is the truth, and the pipeline
reports that failure. An ordinary `SetWorkspace` clears the marking, because a
direct rebinding is the session's own choice again.

The guard has to live in the session rather than in `activeRoot()`, because
admission (`shellInputFor`) runs before the tool and mutates the same state: a
tool-layer-only guard would be defeated by the recovery that admission had
already performed.

## Behaviour

`EnsureWorkspaceRoot()` returns the workspace tools should operate in.

| `ActiveRoot` | Directory state | Project root | Result |
|---|---|---|---|
| bound by a dispatcher (D6) | — | — | returned unchanged; no stat, no notice |
| `== ProjectRoot` | — | — | returned unchanged; no stat, no notice |
| `!= ProjectRoot` | is a directory | — | returned unchanged; no notice |
| `!= ProjectRoot` | missing, or not a directory | is a directory | `ActiveRoot = ProjectRoot`, branch/base/target cleared, row persisted, **one** system notice, `WorkspaceEvent` published |
| `!= ProjectRoot` | missing, or not a directory | also missing | returned unchanged; no notice; the tool's own failure stands |

A path that exists but is not a directory is treated as unusable, matching
`restoreWorkspace`'s predicate (`statErr == nil && info.IsDir()`). One
predicate, two landings.

## Non-goals

- **Detecting a merge that happened out of band.** After `gh pr merge --squash`
  the branch tip is not an ancestor of the target, so `workspace.finish` cannot
  recognise "already merged" without patch-id equivalence. That is its own
  feature; the recovery here is what makes the aftermath survivable.
- **Weakening the sandbox rejection.** It stays exactly as it is, and remains
  the last line of defence for a tool that reaches it some other way.
- **Watching the directory.** A `stat` per tool call costs nothing next to the
  call it precedes, and a watcher would add lifecycle to every session,
  including the overwhelming majority that never enter a worktree.
- **Changing `restoreWorkspace`'s behaviour.** Only its notice text becomes a
  shared helper.
- **Rewording the `workspace.worktree {}` exit.** It is correct as it stands
  and remains the explicit way to detach with a worktree that still exists.

## Contracts

```go
// internal/app/session/workspace.go

// EnsureWorkspaceRoot returns the workspace tools should operate in, first
// recovering from an active root that has disappeared.
func (s *State) EnsureWorkspaceRoot() Workspace

// staleWorktreeNotice is the one sentence a session writes when it lands back
// at the project root because the directory it was working in is gone.
func staleWorktreeNotice(root string) string

// unusedWorktreeRoot reports whether the session is isolated in a directory it
// can no longer work in.
func unusedWorktreeRoot(ws Workspace) bool

// BindIsolatedWorkspace points the session at a root a dispatcher owns rather
// than one the session chose. Such a session does not recover from that root
// disappearing (D6).
func (s *State) BindIsolatedWorkspace(root string)
```

```go
// internal/app/session/session.go — State gains two fields.

// rootMu serializes stale-active-root recovery. Lock order:
// rootMu → accessMu (owner) → mu.
rootMu sync.Mutex

// isolatedRoot marks a workspace a dispatcher bound (D6); guarded by mu.
isolatedRoot bool
```

No exported wire, contract or config change.

## Tests

Each name states the one behaviour it pins.

| Package | Test | Pins |
|---|---|---|
| session | `TestEnsureWorkspaceRootLeavesALiveWorktreeAlone` | a live worktree is returned untouched, with no notice |
| session | `TestEnsureWorkspaceRootRecoversFromARemovedWorktree` | the landing: project root, cleared branch, persisted row, one notice naming the gone path |
| session | `TestEnsureWorkspaceRootWritesOneNoticeForConcurrentCallers` | eight concurrent callers produce one notice |
| session | `TestEnsureWorkspaceRootStaysPutWhenTheProjectRootIsAlsoGone` | nowhere to land means no move and no notice |
| native | `TestActiveRootReturnsToTheProjectRootWhenTheWorktreeIsGone` | `activeRoot()` reports the project root and the session records it |
| native | `TestShellRunRecoversAfterItsWorktreeIsRemoved` | the incident end to end: `shell.run` succeeds and its recorded `Dir` is the project root |
| native | `TestActiveRootRecoveryReachesTheSharedWorkspaceState` | a child recovers the parent's workspace and the notice lands in the parent's transcript |
| agent | `TestShellInputRecoversARemovedWorktree` | admission is judged against the recovered root |
| session | `TestEnsureWorkspaceRootLeavesADispatchIsolatedRootAlone` | a dispatcher-bound root is never recovered and writes no notice (D6) |
| session | `TestASetWorkspaceClearsDispatchIsolation` | a direct rebinding makes the session recoverable again |
| session | `TestEnsureWorkspaceRootRecoversWhenTheRootIsAFile` | a path that exists as a non-directory is unusable, like a missing one |
| native | `TestAPipelineToolsetDoesNotWriteOutsideItsDispatchWorktree` | a dispatch-bound toolset fails loudly instead of writing to the launch directory |
| agent | `TestShellInputDoesNotRecoverADispatchIsolatedRoot` | admission leaves a dispatcher-bound root where it was |

The three existing workspace tests (`TestResumeFallsBackWithNoticeWhenWorktreeGone`
and its neighbours) stay green unchanged; the shared helper changes no text.

## Risks

- **A stat per tool call.** One `os.Stat` on a path the process is about to
  `chdir` into; below the noise of a tool call.
- **A transient mount making a live directory stat as missing.** It would cost
  a session its worktree binding for one call and write a notice. Narrow, and
  the same exposure `restoreWorkspace` has had since it was written; a shared
  predicate is the right place to fix it if it ever bites.
- **A notice that arrives mid-turn.** It is a normal `RoleSystem` message,
  rendered by the transcript's `Notice` row and streamed over ACP like any
  other. It is *not* replayed into the model's context —
  `internal/agent/history.go` builds history from `RoleUser` and
  `RoleAssistant` only — so the turn is unblocked by the recovery itself, not
  by the agent reading the sentence. Nothing is expected to act on it beyond
  the person and the clients.

## Follow-ups

- `workspace.finish` after an out-of-band merge: recognise an already-merged
  branch and detach instead of refusing.
- `files_tab`'s viewer state messages (noted in #180) remain uninset.
