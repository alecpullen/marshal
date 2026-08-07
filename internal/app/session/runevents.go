package session

import "time"

// RunEventKind classifies one thing that happened during a plan run.
type RunEventKind int

const (
	// RunEventVerifyFailed is a failed build or test gate. Title is the
	// failing command; Body is its combined output, verbatim.
	RunEventVerifyFailed RunEventKind = iota
	// RunEventGateSkipped is a verification gate that did not run. This is
	// never a pass — it means no command was configured.
	RunEventGateSkipped
	// RunEventReview is a reviewer verdict. Title is the finding text and
	// Severity is its severity; a clean review uses Detail == "clean".
	RunEventReview
	// RunEventCommit is a task's commit. Title is the short SHA, Detail the
	// commit subject.
	RunEventCommit
	// RunEventRetry is a transient dispatch failure being retried.
	RunEventRetry
	// RunEventConcern is an implementer's DONE_WITH_CONCERNS note.
	RunEventConcern
	// RunEventTaskDone marks a task finishing cleanly.
	RunEventTaskDone
)

// RunEvent is one durable entry in the plan-run event log, rendered in the
// transcript alongside subagent cards.
//
// It is deliberately flat and presentation-ready rather than a mirror of
// pipeline's payload types: internal/pipeline imports this package, so this
// package cannot import pipeline. The pipeline adapter flattens each
// payload into one RunEvent.
//
// Run events are in-memory only. They are never persisted to SQLite and
// never enter model history — they are a UI trace of a run, not part of the
// conversation.
type RunEvent struct {
	Kind     RunEventKind
	TaskN    int
	Title    string
	Detail   string
	Body     string
	Severity string
	At       time.Time
}

// AddRunEvent appends one event to the run log, stamping At when the caller
// left it zero.
func (s *State) AddRunEvent(ev RunEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runEvents = append(s.runEvents, ev)
}

// RunEvents returns a copy of the run log.
func (s *State) RunEvents() []RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RunEvent, len(s.runEvents))
	copy(out, s.runEvents)
	return out
}

// ClearRunEvents empties the run log. The TUI calls this when a new user
// turn clears the collapsed run summary.
func (s *State) ClearRunEvents() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runEvents = nil
}
