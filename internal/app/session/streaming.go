package session

import (
	"time"

	"marshal/internal/activity"
)

// ThinkingEntry captures reasoning text that led to a tool call. Unlike
// Message.Reasoning (which is attached to a final answer), ThinkingEntry
// preserves intermediate reasoning that the agent produced before calling a
// tool — reasoning that would otherwise be lost when the next BeginStreaming
// call resets the in-progress buffer.
type ThinkingEntry struct {
	Activity  activity.Ref
	Sequence  uint64
	Text      string
	Duration  time.Duration
	StartedAt time.Time
}

// InProgressMessage holds the reasoning text accumulated for the model call
// currently in flight (if any). It is not itself a Message: it becomes the
// Reasoning/ThinkDuration of the next Message added via AddMessage, at which
// point it is cleared for the next call.
type InProgressMessage struct {
	Activity  activity.Ref
	Sequence  uint64
	Reasoning string
	StartedAt time.Time
	Active    bool
}

// BeginStreaming starts a new in-progress message, resetting any reasoning
// left over from a previous call. Call this once per model call that may
// stream reasoning content, before consuming its event stream.
func (s *State) BeginStreaming() {
	s.BeginStreamingFor(activity.Ref{})
}

// BeginStreamingFor records thinking against the response captured by its caller.
func (s *State) BeginStreamingFor(owner activity.Ref) {
	s.mu.Lock()
	s.activitySequence++
	if owner == (activity.Ref{}) {
		owner = s.activityResponse
	}
	s.inProgress = InProgressMessage{StartedAt: time.Now(), Active: true, Activity: owner, Sequence: s.activitySequence}
	snap := s.inProgress
	s.mu.Unlock()
	s.publishEvent(EventThinkingChanged, Event{Thinking: &snap})
}

// AppendThinking appends a chunk of reasoning/thinking text to the
// in-progress message.
func (s *State) AppendThinking(delta string) {
	s.AppendThinkingFor(activity.Ref{}, delta)
}

// AppendThinkingFor appends a chunk and stamps the explicitly captured owner.
func (s *State) AppendThinkingFor(owner activity.Ref, delta string) {
	s.mu.Lock()
	if owner != (activity.Ref{}) {
		s.inProgress.Activity = owner
	}
	s.inProgress.Reasoning += delta
	snap := s.inProgress
	s.mu.Unlock()
	s.publishEvent(EventThinkingChanged, Event{Thinking: &snap})
}

// EndStreaming marks the in-progress message inactive. Reasoning captured so
// far is preserved (not cleared) so a subsequent AddMessage call can still
// pick it up.
func (s *State) EndStreaming() {
	s.mu.Lock()
	s.inProgress.Active = false
	snap := s.inProgress
	s.mu.Unlock()
	s.publishEvent(EventThinkingChanged, Event{Thinking: &snap})
}

func (s *State) LogThinking(entry ThinkingEntry) {
	s.mu.Lock()
	if entry.Activity == (activity.Ref{}) {
		entry.Activity = s.activityResponse
	}
	s.activitySequence++
	entry.Sequence = s.activitySequence
	s.thinkingLog = append(s.thinkingLog, entry)
	s.mu.Unlock()
}

// InProgress returns a copy of the current in-progress message.
func (s *State) InProgress() InProgressMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inProgress
}
