package session

import (
	"marshal/internal/tools/registry"
	"time"

	"marshal/internal/db"
)

// StepID identifies a step within one session. 0 means "no step".
type StepID = int64

// Actor says who produced a step: the orchestrator (empty Role), a subagent
// ("reviewer #1"), a pipeline role, or a custom agent.
type Actor struct {
	Role     string // routing.AgentRole value; "" = orchestrator / general
	Label    string // "reviewer #1", "implementer", custom agent name
	Model    string
	Provider string
}

// Step is one model response and everything it caused: its narration,
// reasoning and tool calls. The runner owns the ID and stamps it explicitly;
// State keeps no ambient "current step", so pipeline roles that log into a
// shared parent State cannot race each other.
type Step struct {
	ID        StepID
	TurnMsgID int64 // in-memory ID of the turn's user message; 0 if none
	Actor     Actor
	TodoID    string // the in-progress todo when the step began; "" if none
	StartedAt time.Time
	EndedAt   time.Time // zero while open
}

// IsUserTurnMessage reports whether m is a prompt the user took, the boundary
// a turn starts at. A subagent report, watch report or steering message is
// stored under RoleUser for history replay but is not a turn.
func IsUserTurnMessage(m *Message) bool {
	return m != nil &&
		m.Role == RoleUser &&
		m.ContentType != ContentTypeSubagentReport &&
		m.ContentType != ContentTypeWatchReport &&
		m.ContentType != ContentTypeSteering
}

// BeginStep opens a step for actor and returns its ID. The step belongs to the
// most recent user-turn message on the active branch.
func (s *State) BeginStep(actor Actor) StepID {
	s.mu.Lock()
	var turn int64
	var turnDBID int64
	for i := len(s.messages) - 1; i >= 0; i-- {
		if IsUserTurnMessage(&s.messages[i]) {
			turn, turnDBID = s.messages[i].ID, s.messages[i].DBID
			break
		}
	}
	if s.nextStepSeq < 1 {
		s.nextStepSeq = 1
	}
	// A step belongs to the task that was active when it started. The usual
	// pattern (narrate, mark the next todo in progress, work) puts the
	// todo.write call itself in a step bound to the previous task; the
	// renderer re-binds those for display.
	var todoID string
	for _, td := range s.todos {
		if td.Status == "in_progress" {
			todoID = td.ID
			break
		}
	}
	st := Step{ID: s.nextStepSeq, TurnMsgID: turn, Actor: actor, TodoID: todoID, StartedAt: time.Now()}
	s.nextStepSeq++
	s.steps = append(s.steps, st)
	published := st
	s.mu.Unlock()

	if s.persistenceEnabled() {
		row := db.StepRow{
			Seq: st.ID, TurnMessageID: turnDBID,
			ActorRole: actor.Role, ActorLabel: actor.Label, Model: actor.Model, Provider: actor.Provider,
			TodoID: st.TodoID, StartedAt: st.StartedAt,
		}
		if err := s.db.SaveStep(s.sessionID, row); err != nil {
			s.logger.Error("save step failed", "error", err, "session_id", s.sessionID, "step", st.ID)
		}
	}
	s.publishEvent(EventStepChanged, Event{Step: &published})
	return st.ID
}

// EndStep closes a step. Ending an unknown or already-ended step is a no-op.
func (s *State) EndStep(id StepID) {
	s.mu.Lock()
	idx := -1
	for i := range s.steps {
		if s.steps[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 || !s.steps[idx].EndedAt.IsZero() {
		s.mu.Unlock()
		return
	}
	s.steps[idx].EndedAt = time.Now()
	ended := s.steps[idx]
	s.mu.Unlock()

	if s.persistenceEnabled() {
		if err := s.db.EndStep(s.sessionID, id, ended.EndedAt); err != nil {
			s.logger.Error("end step failed", "error", err, "session_id", s.sessionID, "step", id)
		}
	}
	s.publishEvent(EventStepChanged, Event{Step: &ended})
}

// Steps returns the steps on the active branch, oldest first: those with no
// turn message and those whose turn message is on the branch.
func (s *State) Steps() []Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	onBranch := s.branchIDsLocked()
	out := make([]Step, 0, len(s.steps))
	for _, st := range s.steps {
		if st.TurnMsgID == 0 || onBranch[st.TurnMsgID] {
			out = append(out, st)
		}
	}
	return out
}

// Step looks up a step by ID, on any branch.
func (s *State) Step(id StepID) (Step, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.steps {
		if st.ID == id {
			return st, true
		}
	}
	return Step{}, false
}

// AddNarration records the prose a model emitted alongside its tool calls as
// a narration message belonging to step.
func (s *State) AddNarration(step StepID, content string) {
	s.appendMessageStepped(RoleAssistant, content, ContentTypeNarration, false, false, "", 0, "", step)
}

// branchIDsLocked is the set of in-memory message IDs on the active branch.
// Callers must hold s.mu.
func (s *State) branchIDsLocked() map[int64]bool {
	ids := make(map[int64]bool, len(s.messages))
	for i := range s.messages {
		ids[s.messages[i].ID] = true
	}
	return ids
}

// stepOnBranchLocked reports whether items stamped with step id belong on the
// active branch. Step 0 (legacy, or synthesised by the TUI) always does, and
// so does a step with no record. Callers must hold s.mu.
func (s *State) stepOnBranchLocked(id StepID, onBranch map[int64]bool) bool {
	return s.stepVisibility(onBranch)(id)
}

// stepVisibility returns stepOnBranchLocked with the step table indexed once,
// for callers that test many items: a per-item scan is O(items × steps) under
// the state mutex.
func (s *State) stepVisibility(onBranch map[int64]bool) func(StepID) bool {
	turn := make(map[StepID]int64, len(s.steps))
	for i := range s.steps {
		turn[s.steps[i].ID] = s.steps[i].TurnMsgID
	}
	return func(id StepID) bool {
		if id == 0 {
			return true
		}
		t, ok := turn[id]
		return !ok || t == 0 || onBranch[t]
	}
}

// StepNarration returns the first narration recorded for a step, or "". It
// scans under the lock without copying the message list: the TUI asks every
// frame, and a full copy per frame blocks the runner's writes.
func (s *State) StepNarration(id StepID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.messages {
		if m := &s.messages[i]; m.ContentType == ContentTypeNarration && m.StepID == id {
			return m.Content
		}
	}
	return ""
}

// StepAudits returns the audit events stamped with a step.
func (s *State) StepAudits(id StepID) []registry.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []registry.AuditEvent
	for i := range s.auditLog {
		if s.auditLog[i].StepID == id {
			out = append(out, s.auditLog[i])
		}
	}
	return out
}

// OpenStep returns the most recently started step that has not ended.
func (s *State) OpenStep() (Step, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var live *Step
	for i := range s.steps {
		if s.steps[i].EndedAt.IsZero() && (live == nil || !s.steps[i].StartedAt.Before(live.StartedAt)) {
			live = &s.steps[i]
		}
	}
	if live == nil {
		return Step{}, false
	}
	return *live, true
}
