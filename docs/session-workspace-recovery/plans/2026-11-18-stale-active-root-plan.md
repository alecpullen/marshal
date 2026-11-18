# Plan: a session recovers when its worktree is removed underneath it

Spec: [`../feature-spec.md`](../feature-spec.md).
Base commit: `ca668c4df739035e1cee7f78083e4dfcce737a91`.
Goal: `EnsureWorkspaceRoot()` returns the workspace tools should operate in,
recovering a session whose worktree has been removed mid-run, in the same
landing `restoreWorkspace` already gives a session that resumes into one.
Non-goals: out-of-band merge detection, changing the sandbox rejection, file
watching, any wire or config change (spec § Non-goals).

Assumptions:
- The tree is at or after the base commit, so `activeRoot()` is at
  `internal/tools/native/native.go:402` and `restoreWorkspace()` at
  `internal/app/session/workspace.go:125`.
- Task 1 lands before Tasks 2 and 3; neither compiles against `main` alone,
  because `EnsureWorkspaceRoot` does not exist there. Tasks 2 and 3 are
  `Parallel-safe with` each other.
- Run every command from the repository root with a C toolchain present.

## Contracts

Added by Task 1, used by Tasks 2 and 3.

```go
// internal/app/session/workspace.go

// EnsureWorkspaceRoot returns the workspace tools should operate in, first
// recovering from an active root that has disappeared.
func (s *State) EnsureWorkspaceRoot() Workspace

// staleWorktreeNotice is the one sentence a session writes when it lands back
// at the project root because the directory it was working in is gone.
func staleWorktreeNotice(root string) string

// unusedWorktreeRoot reports whether the session is isolated in a directory it
// can no longer work in: a worktree removed underneath a running session, or
// one replaced by something that is not a directory.
func unusedWorktreeRoot(ws Workspace) bool
```

```go
// internal/app/session/session.go — State gains one field, after `mu`.

// rootMu serializes stale-active-root recovery. Lock order:
// rootMu → accessMu (owner) → mu.
rootMu sync.Mutex
```

Behaviour, for the reader of a later task:

| `ActiveRoot` | Directory | Project root | Result |
|---|---|---|---|
| `== ProjectRoot` | — | — | unchanged, no stat, no notice |
| `!= ProjectRoot` | a directory | — | unchanged, no notice |
| `!= ProjectRoot` | missing / not a directory | a directory | project root, branch cleared, row persisted, **one** notice, `WorkspaceEvent` |
| `!= ProjectRoot` | missing / not a directory | also missing | unchanged, no notice |

---

## Task 1: the session recovers a root that has disappeared

**Goal.** `(*session.State).EnsureWorkspaceRoot()` returns the workspace tools
should operate in, moving a session out of a worktree directory that no longer
exists, and both landings share one notice sentence.

**Files**
- `internal/app/session/session.go`
- `internal/app/session/workspace.go`
- `internal/app/session/workspace_test.go`
- `docs/tools/workspace-interaction.md`

**Steps**

1. In `internal/app/session/session.go`, the `State` struct gains a field
   directly after `mu sync.Mutex` (line 254). The comment is part of the field:

```go
	mu         sync.Mutex
	// rootMu serializes stale-active-root recovery; see EnsureWorkspaceRoot.
	// Lock order: rootMu → accessMu (owner) → mu, so nothing may take rootMu
	// while holding either of the others.
	rootMu sync.Mutex
```

2. In `internal/app/session/workspace.go`, insert the following between
   `SetWorkspaceBroker` and `restoreWorkspace`'s doc comment (the comment
   beginning "restoreWorkspace rebinds the session", line 110):

```go
// staleWorktreeNotice is the one sentence a session writes when it lands back
// at the project root because the directory it was working in is gone. The
// load-time restore (restoreWorkspace) and the mid-session recovery
// (EnsureWorkspaceRoot) reach the same landing, so they say the same thing.
func staleWorktreeNotice(root string) string {
	return fmt.Sprintf("System notice: the worktree this session was working in (%s) no longer exists; the session has been returned to the project root.", root)
}

// unusedWorktreeRoot reports whether the session is isolated in a directory it
// can no longer work in: a worktree removed underneath a running session, or
// one replaced by something that is not a directory. It is the predicate
// restoreWorkspace applies to a resumed session.
func unusedWorktreeRoot(ws Workspace) bool {
	if ws.ActiveRoot == "" || ws.ActiveRoot == ws.ProjectRoot {
		return false
	}
	info, err := os.Stat(ws.ActiveRoot)
	return err != nil || !info.IsDir()
}

// EnsureWorkspaceRoot returns the workspace tools should operate in, first
// recovering from an active root that has disappeared.
//
// A worktree can be removed by anything outside the session: a merged pull
// request cleaned up from another terminal, a `git worktree remove`, a deleted
// temporary directory. Before this check the path stayed recorded until the
// process exited, and every tool call failed in the sandbox with "working
// directory ... does not exist" — a stop with no way out, because
// workspace.worktree's only exit is a branch argument that a model does not
// reach for when the error names a path.
//
// The recovery lands where restoreWorkspace lands a session that resumes into
// a gone worktree: the project root, the stale row cleared, one system notice.
// It runs once however many callers race for it, so a parent and its subagents
// write one notice between them.
//
// A project root that is also gone is not recovered: there is nowhere to land,
// and the sandbox reports the truth about the directory the tool asked for.
func (s *State) EnsureWorkspaceRoot() Workspace {
	ws := s.Workspace()
	if !unusedWorktreeRoot(ws) {
		return ws
	}
	s.rootMu.Lock()
	defer s.rootMu.Unlock()
	// Re-read under the lock: a concurrent caller may already have recovered
	// while this one waited, and a second notice is the thing to avoid.
	ws = s.Workspace()
	if !unusedWorktreeRoot(ws) {
		return ws
	}
	if info, err := os.Stat(ws.ProjectRoot); err != nil || !info.IsDir() {
		return ws
	}
	s.SetWorkspace(Workspace{ProjectRoot: ws.ProjectRoot, ActiveRoot: ws.ProjectRoot})
	s.AddMessage(RoleSystem, staleWorktreeNotice(ws.ActiveRoot), ContentTypePlain)
	return s.Workspace()
}
```

3. In `restoreWorkspace`, replace its final `s.AddMessage` call (the one
   spanning the `"System notice: the worktree this session was working in …"`
   literal) with the shared helper. Nothing else in the function changes:

```go
	s.AddMessage(RoleSystem, staleWorktreeNotice(row.ActiveRoot), ContentTypePlain)
```

   Its doc comment gains one sentence at the end: "The sentence it writes is
   shared with EnsureWorkspaceRoot, which reaches the same landing mid-session."

4. In `docs/tools/workspace-interaction.md`, append this paragraph to the
   `## workspace.worktree` section, immediately before the `## workspace.finish`
   heading (line 35):

```markdown
If the worktree directory is removed while the session is running — a merged
branch cleaned up from another terminal, `git worktree remove` — the next tool
call returns the session to the project root and writes one system notice
naming the path. The same landing applies when a session resumes into a
worktree that is gone.
```

5. Add the four tests below to `internal/app/session/workspace_test.go`, after
   `TestResumeFallsBackWithNoticeWhenWorktreeGone`. Add `"sync"` to that file's
   import block; every other import they use is already there.

```go
func TestEnsureWorkspaceRootLeavesALiveWorktreeAlone(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	before := len(s.Messages())

	ws := s.EnsureWorkspaceRoot()
	if ws.ActiveRoot != wt || ws.Branch != "feat/x" {
		t.Fatalf("EnsureWorkspaceRoot() = %+v, want the worktree unchanged", ws)
	}
	if got := len(s.Messages()); got != before {
		t.Fatalf("messages = %d, want %d (no notice for a live worktree)", got, before)
	}
}

func TestEnsureWorkspaceRootRecoversFromARemovedWorktree(t *testing.T) {
	s, d, sid, root := newPersistedState(t)
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x", TargetBranch: "main", BaseSha: "abc123"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	ws := s.EnsureWorkspaceRoot()
	if ws.ActiveRoot != root || ws.Branch != "" || ws.BaseSha != "" || ws.TargetBranch != "" {
		t.Fatalf("EnsureWorkspaceRoot() = %+v, want the project root with an empty branch", ws)
	}
	msgs := s.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != RoleSystem || !strings.Contains(last.Content, wt) {
		t.Fatalf("last message = %v %q, want a system notice naming %q", last.Role, last.Content, wt)
	}
	// The cleared row is what stops the next resume warning a second time.
	row, err := d.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != "" || row.WorktreeBranch != "" {
		t.Fatalf("row still records ActiveRoot %q / branch %q", row.ActiveRoot, row.WorktreeBranch)
	}
}

func TestEnsureWorkspaceRootWritesOneNoticeForConcurrentCallers(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ws := s.EnsureWorkspaceRoot(); ws.ActiveRoot != root {
				t.Errorf("EnsureWorkspaceRoot() = %+v, want %q", ws, root)
			}
		}()
	}
	wg.Wait()

	notices := 0
	for _, m := range s.Messages() {
		if m.Role == RoleSystem && strings.Contains(m.Content, "no longer exists") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("system notices = %d, want 1", notices)
	}
}

func TestEnsureWorkspaceRootStaysPutWhenTheProjectRootIsAlsoGone(t *testing.T) {
	root := t.TempDir()
	s := New(config.Default(), root, time.Unix(100, 0), Persistence{})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	s.SetWorkspace(Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove project: %v", err)
	}

	ws := s.EnsureWorkspaceRoot()
	if ws.ActiveRoot != wt {
		t.Fatalf("EnsureWorkspaceRoot() = %+v, want the root left alone with nowhere to land", ws)
	}
	for _, m := range s.Messages() {
		if m.Role == RoleSystem && strings.Contains(m.Content, "no longer exists") {
			t.Fatal("a notice was written although the session could not move")
		}
	}
}
```

**Verify**

```bash
CGO_ENABLED=1 go test ./internal/app/session -run 'EnsureWorkspaceRoot|ResumeRestoresWorkspace|ResumeFallsBackWithNoticeWhenWorktreeGone' -count=1
```

Expected: `ok marshal/internal/app/session`. Four new tests pass, and
`TestResumeFallsBackWithNoticeWhenWorktreeGone` still passes — it is the proof
the shared sentence did not change.

Revert check (do it once): force `unusedWorktreeRoot` to `return false`, re-run
— `TestEnsureWorkspaceRootRecoversFromARemovedWorktree` and
`…WritesOneNoticeForConcurrentCallers` must fail. Restore.

---

## Task 2: every tool runs in the recovered root

**Goal.** `toolSet.activeRoot()` asks the session for the recovered workspace,
so `shell.run`, `test.run`, the file tools and every child toolset operate at
the project root after the worktree is removed.

**Parallel-safe with:** Task 3.

**Files**
- `internal/tools/native/native.go`
- `internal/tools/native/activeroot_test.go`

**Steps**

1. In `internal/tools/native/native.go`, replace the body of `activeRoot()`
   (line 402) with the version below, and add the second paragraph to its doc
   comment — between the "Reading through the session…" paragraph and "The
   empty-ActiveRoot fallback…":

```go
// The read is also where a worktree that has been removed underneath the
// session is noticed: EnsureWorkspaceRoot returns the session to its project
// root and writes one notice, so the tool runs where the session can work
// instead of failing in the sandbox on a directory that is gone.
func (t *toolSet) activeRoot() string {
	if ws := t.wsState(); ws != nil {
		if w := ws.EnsureWorkspaceRoot(); w.ActiveRoot != "" {
			return w.ActiveRoot
		}
	}
	return t.root
}
```

   `commandRoot()` is unchanged: it already delegates to `activeRoot()`, and an
   explicit `commandDir` (a pipeline dispatch bound to its own worktree) keeps
   taking precedence.

2. Add the three tests below to
   `internal/tools/native/activeroot_test.go`. Add `"os"` and `"strings"` to
   its import block; the rest are already imported. The helpers they use —
   `newToolSetForTest` (`scope_test.go:22`), `fakeRunner`, `invokeTool`,
   `newTestCommandService`, `allowAllAdmission` — are already used by
   `TestShellRunUsesActiveRoot` in this file.

```go
func TestActiveRootReturnsToTheProjectRootWhenTheWorktreeIsGone(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	tools := newToolSetForTest(t, Options{WorkspaceRoot: root, SessionState: state})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	if got := tools.activeRoot(); got != root {
		t.Fatalf("activeRoot() = %q, want the project root %q", got, root)
	}
	if got := state.Workspace().ActiveRoot; got != root {
		t.Fatalf("session ActiveRoot = %q, want %q", got, root)
	}
}

// TestShellRunRecoversAfterItsWorktreeIsRemoved is the incident end to end: a
// worktree removed from outside the session, then a shell command. Before the
// fix the sandbox rejected the call with "working directory ... does not exist".
func TestShellRunRecoversAfterItsWorktreeIsRemoved(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, SessionState: state, JobManager: newTestCommandService(t, runner, root, state, nil), CommandAdmission: allowAllAdmission(t)}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"pwd"}`); err != nil {
		t.Fatalf("shell.run after the worktree was removed: %v", err)
	}
	if len(runner.requests) == 0 {
		t.Fatal("no command recorded")
	}
	if got := runner.requests[len(runner.requests)-1].Dir; got != root {
		t.Fatalf("shell dir = %q, want the project root %q", got, root)
	}
}

// TestActiveRootRecoveryReachesTheSharedWorkspaceState pins the wiring: a
// subagent's toolset carries the parent as WorkspaceState (app.go:2413), so the
// recovery must move the parent and notice the parent's conversation — a notice
// in the child's transcript would be read by nobody.
func TestActiveRootRecoveryReachesTheSharedWorkspaceState(t *testing.T) {
	root := t.TempDir()
	parent := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	child := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	childTools := newToolSetForTest(t, Options{WorkspaceRoot: root, SessionState: child, WorkspaceState: parent})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	parent.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	if got := childTools.activeRoot(); got != root {
		t.Fatalf("child activeRoot() = %q, want %q", got, root)
	}
	if got := parent.Workspace().ActiveRoot; got != root {
		t.Fatalf("parent ActiveRoot = %q, want %q", got, root)
	}
	parentNotices := 0
	for _, m := range parent.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "no longer exists") {
			parentNotices++
		}
	}
	if parentNotices != 1 {
		t.Fatalf("parent notices = %d, want 1", parentNotices)
	}
	for _, m := range child.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "no longer exists") {
			t.Fatal("the notice landed in the child's transcript")
		}
	}
}
```

**Verify**

```bash
CGO_ENABLED=1 go test ./internal/tools/native -run 'ActiveRoot|ShellRun' -count=1
```

Expected: `ok marshal/internal/tools/native`. `TestShellRunRecoversAfterItsWorktreeIsRemoved`
is the regression test for the reported failure; with `activeRoot()` reverted to
`ws.Workspace()` it must fail with a `shell.run` error naming the missing
directory (do this revert check once, then restore).

---

## Task 3: admission is judged in the recovered root

**Goal.** `Runner.shellInputFor` reports the workspace the command will actually
run in, so a shell command is not classified against a directory that is gone.

**Parallel-safe with:** Task 2.

**Files**
- `internal/agent/execute.go`
- `internal/agent/shell_input_test.go` (new)

**Steps**

1. In `internal/agent/execute.go`, `shellInputFor` (line 134), replace the
   assignment at line 156 inside the function's tail:

```go
	dir := r.State.WorkingDir
	// The recovered root, not the recorded one: a worktree removed underneath
	// the session is put right here, before admission decides, so the command
	// is judged against the directory it will actually run in.
	workspace := r.State.EnsureWorkspaceRoot().ActiveRoot
	if workspace == "" {
		workspace = dir
	}
```

2. Create `internal/agent/shell_input_test.go` with exactly this content. It
   reuses `testRunnerFromState` (`internal/agent/atfile_test.go:334`), which is
   in this package:

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// TestShellInputRecoversARemovedWorktree pins that admission is judged against
// the directory the command will run in: a stale workspace root would classify
// the very call that recovers the session.
func TestShellInputRecoversARemovedWorktree(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	wt := filepath.Join(root, ".marshal", "worktrees", "feat-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	state.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: wt, Branch: "feat/x"})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	r := testRunnerFromState(state, 1)
	in, ok := r.shellInputFor("shell.run", map[string]interface{}{"command": "pwd"})
	if !ok {
		t.Fatal("shellInputFor reported not a shell tool")
	}
	if in.WorkspaceRoot != root {
		t.Fatalf("WorkspaceRoot = %q, want the project root %q", in.WorkspaceRoot, root)
	}
	if got := state.Workspace().ActiveRoot; got != root {
		t.Fatalf("session ActiveRoot = %q, want %q", got, root)
	}
}
```

**Verify**

```bash
CGO_ENABLED=1 go test ./internal/agent -run 'TestShellInput' -count=1
```

Expected: `ok marshal/internal/agent`.

---

## Final verification

```bash
gofmt -l internal docs          # no output
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./internal/app/session ./internal/tools/native ./internal/agent -count=1
scripts/test.sh                 # the packages the diff affects, uncached
```

Expected: `gofmt` prints nothing, `go vet` and `go build` are clean, the three
packages pass, and `scripts/test.sh` reports no failures. `./marshal` is not
required by the suite.

## Integration notes

- **Commit per task**: `session-workspace-recovery: task 1 — …`, and so on.
  Task 1's commit is a prerequisite for Tasks 2 and 3; those two may land in
  either order or as one commit.
- **No wire change.** Nothing in `web/bridge/contract`, `app/lib`, the TUI, or
  the config; no golden, snapshot or recorded fixture moves.
- **`WorkspaceEvent` subscribers see the recovery as a normal rebinding**: the
  TUI and ACP already treat "active root changed" as a workspace move, so the
  header and status line follow without new code.
- **Children are covered by Task 2 alone.** A child toolset passes the parent
  state as `WorkspaceState` (`internal/app/app.go:2413`), so the child's
  `activeRoot()` recovers the parent and the notice appears in the parent's
  transcript — pinned by `TestActiveRootRecoveryReachesTheSharedWorkspaceState`.
- **The describing readers stay on `Workspace()`** (`snapshot_warning.go:422`,
  `helpers.go:354`) by design: they name history, not a decision (spec § D3).
  A reviewer who expects them to change should re-read that section first.
