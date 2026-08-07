package session

import (
	"strings"
	"sync/atomic"
	"time"

	"marshal/internal/pubsub"
)

// SubagentStatus is the lifecycle state of a registered subagent view.
type SubagentStatus int

const (
	SubagentRunning SubagentStatus = iota
	SubagentDone
	SubagentFailed
)

// SubagentView is the TUI-facing snapshot of one subagent's work. It is the
// summary card rendered in the parent transcript in place of the subagent's
// full tool log, and the handle the drill-down view uses to reach the child
// session's live transcript.
//
// Child is the subagent's own session.State. It is retained (not copied) so
// the drill-down view streams the child's transcript live; for pipeline/SDD
// subagents, which share the parent's state, Child may be nil and the card
// renders status/summary only.
type SubagentView struct {
	ID        int64
	Label     string
	Status    SubagentStatus
	Child     *State
	StartedAt time.Time
	EndedAt   time.Time
	// ToolCalls counts completed tool calls observed in the child transcript
	// at card-render time; the TUI refreshes it while Status == Running.
	ToolCalls int
	// CurrentTool is the in-flight tool's display label while Status ==
	// SubagentRunning, refreshed at card-render time like ToolCalls. Empty
	// when the child is idle or finished.
	CurrentTool string
	// Summary is the subagent's final report, captured when it finishes so
	// the completed card (and the drilled-in view header) can show it.
	Summary string
}

// SubagentEvent is published on the subagent broker when a subagent is
// registered or transitions status, so the TUI re-renders the card without
// polling.
type SubagentEvent struct {
	View SubagentView
}

// subagentSeq allocates process-unique subagent IDs. IDs only need to be
// unique within a parent session's lifetime; an atomic counter avoids a
// State.mu-held increment and keeps registration cheap.
var subagentSeq atomic.Int64

// RegisterSubagent records a new running subagent and returns its view. The
// returned view's Child pointer is the live child state; the caller owns
// finishing the registration via FinishSubagent. Publishing happens after
// the mutex is released, matching SetWorkspace.
func (s *State) RegisterSubagent(label string, child *State) SubagentView {
	v := SubagentView{
		ID:        subagentSeq.Add(1),
		Label:     label,
		Status:    SubagentRunning,
		Child:     child,
		StartedAt: time.Now(),
	}
	s.mu.Lock()
	s.subagents = append(s.subagents, v)
	broker := s.subagentBroker
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("subagent", SubagentEvent{View: v})
	}
	return v
}

// FinishSubagent marks the subagent with the given ID done (or failed when
// err != nil), records its end time and final summary, and republishes so
// the card flips from the running spinner to its terminal state.
func (s *State) FinishSubagent(id int64, summary string, err error) {
	s.mu.Lock()
	var updated SubagentView
	found := false
	for i := range s.subagents {
		if s.subagents[i].ID == id {
			s.subagents[i].EndedAt = time.Now()
			s.subagents[i].Summary = summary
			if err != nil {
				s.subagents[i].Status = SubagentFailed
			} else {
				s.subagents[i].Status = SubagentDone
			}
			updated = s.subagents[i]
			found = true
			break
		}
	}
	broker := s.subagentBroker
	s.mu.Unlock()
	if found && broker != nil {
		broker.Publish("subagent", SubagentEvent{View: updated})
	}
}

// Subagents returns a copy of the registered subagent views in registration
// order. The Child pointers inside reference live states; callers read them
// under those states' own locks.
func (s *State) Subagents() []SubagentView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SubagentView, len(s.subagents))
	copy(out, s.subagents)
	return out
}

// Subagent returns the view with the given ID, or false if unknown.
func (s *State) Subagent(id int64) (SubagentView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.subagents {
		if v.ID == id {
			return v, true
		}
	}
	return SubagentView{}, false
}

// CompletedToolCallCount reports how many tool calls have finished in this
// session's audit log. The parent uses it to keep a subagent card's "N tool
// calls" figure current while the child is still running.
func (s *State) CompletedToolCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.auditLog)
}

// CurrentToolLabel returns a concise, human-readable label for the tool this
// session is currently running, or "" when idle. The parent uses it to show
// what a running subagent is doing on its card. For file-edit tools the label
// names the file being edited; other tools fall back to their display name.
func (s *State) CurrentToolLabel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeToolCall == nil {
		return ""
	}
	atc := *s.activeToolCall
	switch atc.Name {
	case "file.write_patch", "patch.apply":
		if path := firstPatchPath(atc.Args); path != "" {
			return "editing " + path
		}
	}
	return atc.Name
}

// firstPatchPath extracts the first "File: <path>" line from a patch
// proposal's args, used to name the file a file-edit tool is working on.
func firstPatchPath(args string) string {
	for _, line := range strings.Split(args, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "File:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "File:"))
		}
	}
	return ""
}

// SetSubagentBroker wires the broker RegisterSubagent/FinishSubagent publish
// to. Mirrors SetWorkspaceBroker.
func (s *State) SetSubagentBroker(b *pubsub.Broker[SubagentEvent]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subagentBroker = b
}
