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
	s.mu.Lock()
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
	changed := s.workspace != w
	s.workspace = w
	broker := s.workspaceBroker
	s.mu.Unlock()

	if !changed {
		return
	}
	if s.persistenceEnabled() {
		persistRoot, persistBranch, persistTarget := w.ActiveRoot, w.Branch, w.TargetBranch
		if w.ActiveRoot == w.ProjectRoot {
			persistRoot, persistBranch, persistTarget = "", "", ""
		}
		if err := s.db.UpdateSessionWorkspace(s.sessionID, persistRoot, persistBranch, persistTarget); err != nil {
			s.logger.Warn("persist workspace", "error", err)
		}
	}
	if broker != nil {
		broker.Publish("workspace", WorkspaceEvent{Workspace: w})
	}
}

// SetWorkspaceBroker wires the broker SetWorkspace publishes to. Mirrors
// SetSteeringBroker.
func (s *State) SetWorkspaceBroker(b *pubsub.Broker[WorkspaceEvent]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceBroker = b
}

// restoreWorkspace rebinds the session to the workspace recorded on its
// session row. Called once from New, after loadFromDB. If the recorded
// worktree no longer exists, the session stays at the project root, the
// stale record is cleared, and a system notice is written into the
// transcript — silently landing in the main checkout while the restored
// transcript shows worktree work is the failure this feature exists to
// prevent.
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
		s.workspace = Workspace{ProjectRoot: s.WorkingDir, ActiveRoot: row.ActiveRoot, Branch: row.WorktreeBranch, TargetBranch: row.WorktreeTargetBranch}
		s.mu.Unlock()
		return
	}
	if err := s.db.UpdateSessionWorkspace(s.sessionID, "", "", ""); err != nil {
		s.logger.Warn("clear stale workspace", "error", err)
	}
	s.AddMessage(RoleSystem, fmt.Sprintf(
		"System notice: the worktree this session was working in (%s) no longer exists; the session has been returned to the project root.",
		row.ActiveRoot), ContentTypePlain)
}
