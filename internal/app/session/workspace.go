package session

import (
	"fmt"
	"os"

	"marshal/internal/pubsub"
)

// Workspace separates the project (which owns .marshal/) from the directory
// tools currently operate in. They differ only while the session is in a
// worktree.
type Workspace struct {
	ProjectRoot string // fixed for the session's lifetime
	ActiveRoot  string // == ProjectRoot, or a worktree path
	Branch      string // worktree branch; "" when at ProjectRoot
	// BaseSha is the commit the worktree branch started from. Set when the
	// session is isolated; used as the left side of the diff range. It cannot
	// be recomputed inside the worktree, where HEAD is the branch tip.
	BaseSha string // "" when at ProjectRoot
	// TargetBranch is the project branch the worktree branch is destined to
	// merge into. Recorded at isolation time and persisted so a later
	// session/merge can reject a caller-supplied target that differs. It
	// cannot be derived later: EnsureWorktree returns a SHA, and merging into
	// whatever the project is on at merge time would silently pick the wrong
	// branch. "" when at ProjectRoot.
	TargetBranch string
}

// WorkspaceEvent is published on the workspace broker when SetWorkspace
// changes the active workspace.
type WorkspaceEvent struct {
	Workspace Workspace
}

// Workspace returns the session's current workspace.
func (s *State) Workspace() Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspace
}

// SetWorkspace rebinds the session's active root and publishes a
// WorkspaceEvent on the workspace broker (when wired) if the value changed.
// Publishing happens after the mutex is released, matching PushSteering.
// Empty ProjectRoot keeps the current one; empty ActiveRoot means "back to
// the project root"; Branch is forced empty whenever ActiveRoot ==
// ProjectRoot. (Task 3 adds persistence here.)
func (s *State) SetWorkspace(w Workspace) {
	s.rebind(w, false)
}

// rebind makes w current with the given isolation, then publishes and persists
// it when it changed.
func (s *State) rebind(w Workspace, isolated bool) {
	// The owner's accessMu first: an active-root change invalidates the
	// owner's access revision, and the documented lock order is
	// accessMu → mu.
	owner := s.ownerState()
	owner.accessMu.Lock()
	ws, changed, rootChanged, broker := s.assignWorkspace(w, isolated)
	if rootChanged {
		owner.access.Revision++
		owner.signalInteractionLocked()
	}
	// Release accessMu before publishing or writing anything: the session
	// broker's terminal subscriber blocks the publisher until it drains, and
	// holding accessMu across that would stall every concurrent authority,
	// steering and interaction read for as long as the subscriber is stalled.
	// Only the revision bump and the interaction signal need the lock.
	owner.accessMu.Unlock()

	if !changed {
		return
	}
	if s.persistenceEnabled() {
		persistRoot, persistBranch, persistTarget, persistBase := ws.ActiveRoot, ws.Branch, ws.TargetBranch, ws.BaseSha
		if ws.ActiveRoot == ws.ProjectRoot {
			persistRoot, persistBranch, persistTarget, persistBase = "", "", "", ""
		}
		if err := s.db.UpdateSessionWorkspace(s.sessionID, persistRoot, persistBranch, persistTarget, persistBase); err != nil {
			s.logger.Warn("persist workspace", "error", err)
		}
	}
	if broker != nil {
		broker.Publish("workspace", WorkspaceEvent{Workspace: ws})
	}
	s.publishEvent(EventTranscriptChanged, Event{})
}

// assignWorkspace normalizes w and makes it current under mu. The isolation
// flag is written in the same critical section as the workspace itself, so a
// concurrent reader never sees one without the other — which is what keeps a
// dispatcher's binding and the reason it must not be recovered atomic.
// An ordinary SetWorkspace passes false: a direct rebinding is the session's
// own choice again, so recovery applies to it.
func (s *State) assignWorkspace(w Workspace, isolated bool) (ws Workspace, changed, rootChanged bool, broker *pubsub.Broker[WorkspaceEvent]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.ProjectRoot == "" {
		w.ProjectRoot = s.workspace.ProjectRoot
	}
	if w.ActiveRoot == "" {
		w.ActiveRoot = w.ProjectRoot
	}
	if w.ActiveRoot == w.ProjectRoot {
		w.Branch = ""
		w.BaseSha = ""
		w.TargetBranch = ""
	}
	changed = s.workspace != w
	rootChanged = s.workspace.ActiveRoot != w.ActiveRoot
	s.workspace = w
	s.isolatedRoot = isolated
	// The normalized workspace goes back to the caller: publishing or storing
	// the argument as it arrived would drop the ProjectRoot default and the
	// branch clearing, and a caller that left ProjectRoot empty would persist
	// its own active root as a worktree.
	return w, changed, rootChanged, s.workspaceBroker
}

// SetWorkspaceBroker wires the broker SetWorkspace publishes to. Mirrors
// SetSteeringBroker.
func (s *State) SetWorkspaceBroker(b *pubsub.Broker[WorkspaceEvent]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceBroker = b
}

// BindIsolatedWorkspace points the session at a root a dispatcher owns rather
// than one the session chose: a pipeline dispatch bound to its own worktree.
// Such a session does not recover from that root disappearing
// (EnsureWorkspaceRoot), because its project root is the directory the run was
// launched from, not the work it was isolated to: landing there would write
// files outside the isolation the dispatch promised while its commands keep
// failing in the bound worktree. It fails loudly instead, as it did before
// recovery existed.
func (s *State) BindIsolatedWorkspace(root string) {
	// One critical section for both fields: binding the root and marking it a
	// dispatcher's must not be separable, or a concurrent recovery could move
	// the session between the two steps and leave the mark on the wrong root.
	s.rebind(Workspace{ActiveRoot: root}, true)
}

// workspaceAndIsolation returns the current workspace with the dispatcher-
// bound flag. Both are written together under mu (assignWorkspace), so they
// are always a consistent pair.
func (s *State) workspaceAndIsolation() (Workspace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspace, s.isolatedRoot
}

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
//
// A root a dispatcher bound (BindIsolatedWorkspace) is never recovered: the
// session did not choose it, and its project root is not where that work
// belongs.
func (s *State) EnsureWorkspaceRoot() Workspace {
	ws, isolated := s.workspaceAndIsolation()
	if isolated || !unusedWorktreeRoot(ws) {
		return ws
	}
	s.rootMu.Lock()
	defer s.rootMu.Unlock()
	// Re-read under the lock: a concurrent caller may already have recovered
	// while this one waited, and a second notice is the thing to avoid. The
	// isolation flag is re-read with it, so a concurrent clear cannot slip
	// past the first check.
	//
	// rootMu is held across SetWorkspace and AddMessage, which publish on
	// brokers; pubsub.Publish never runs subscriber code on the publisher's
	// goroutine, so no subscriber can re-enter this lock (same reason
	// publishEvent publishes with mu released).
	ws, isolated = s.workspaceAndIsolation()
	if isolated || !unusedWorktreeRoot(ws) {
		return ws
	}
	if info, err := os.Stat(ws.ProjectRoot); err != nil || !info.IsDir() {
		return ws
	}
	s.SetWorkspace(Workspace{ProjectRoot: ws.ProjectRoot, ActiveRoot: ws.ProjectRoot})
	s.AddMessage(RoleSystem, staleWorktreeNotice(ws.ActiveRoot), ContentTypePlain)
	return s.Workspace()
}

// restoreWorkspace rebinds the session to the workspace recorded on its
// session row. Called once from New, after loadFromDB. If the recorded
// worktree no longer exists, the session stays at the project root, the
// stale record is cleared, and a system notice is written into the
// transcript — silently landing in the main checkout while the restored
// transcript shows worktree work is the failure this feature exists to
// prevent. The sentence it writes is shared with EnsureWorkspaceRoot, which
// reaches the same landing mid-session.
//
// WorkingDir is the source of truth for ProjectRoot (spec §1: it is
// immutable for the session's lifetime, so using it here is correct
// even though the recorded row could in theory hold a different path).
//
// The stale-clear UPDATE runs before AddMessage so a subsequent resume
// does not re-warn about the same gone worktree; the notice itself is
// best-effort and never blocks session construction.
func (s *State) restoreWorkspace() {
	row, err := s.db.GetSession(s.sessionID)
	if err != nil || row.ActiveRoot == "" {
		return
	}
	if info, statErr := os.Stat(row.ActiveRoot); statErr == nil && info.IsDir() {
		s.mu.Lock()
		s.workspace = Workspace{ProjectRoot: s.WorkingDir, ActiveRoot: row.ActiveRoot, Branch: row.WorktreeBranch, TargetBranch: row.WorktreeTargetBranch, BaseSha: row.WorktreeBaseSha}
		s.mu.Unlock()
		return
	}
	if err := s.db.UpdateSessionWorkspace(s.sessionID, "", "", "", ""); err != nil {
		s.logger.Warn("clear stale workspace", "error", err)
	}
	s.AddMessage(RoleSystem, staleWorktreeNotice(row.ActiveRoot), ContentTypePlain)
}
