package session

import (
	"marshal/internal/pubsub"
)

// Workspace separates the project (which owns .marshal/) from the directory
// tools currently operate in. They differ only while the session is in a
// worktree.
type Workspace struct {
	ProjectRoot string // fixed for the session's lifetime
	ActiveRoot  string // == ProjectRoot, or a worktree path
	Branch      string // worktree branch; "" when at ProjectRoot
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
	}
	changed := s.workspace != w
	s.workspace = w
	broker := s.workspaceBroker
	s.mu.Unlock()

	if changed && broker != nil {
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
