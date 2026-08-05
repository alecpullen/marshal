package session

import (
	"strings"
	"time"
)

// SDDProgress is the live state of a plan-execution run, rendered by the
// TUI's run panel. The controller emits events; the app layer maps them
// here. Tasks holds the plan's task titles in order so the panel can
// render the checklist; StartedAt/EndedAt bracket the run for the elapsed
// readouts. When the run ends, Active flips off and Finished flips on —
// the panel collapses to a one-line summary until the next user turn
// calls ClearSDDProgress.
type SDDProgress struct {
	Active       bool
	PlanName     string
	PlanPath     string
	Branch       string
	Tasks        []string
	TotalTasks   int
	DoneTasks    int
	CurrentTask  int
	Phase        string
	Detail       string
	FixRound     int
	MaxFixRounds int
	TokensUsed   int
	TokensMax    int
	LastLedger   string
	StartedAt    time.Time
	Finished     bool
	Succeeded    bool
	EndedAt      time.Time
	Error        string
}

func (s *State) SetSDDProgress(p SDDProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddProgress = p
}

// SDDProgress returns a copy of the current progress.
func (s *State) SDDProgress() SDDProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sddProgress
}

// UpdateSDDProgress applies fn to the current progress under the lock.
func (s *State) UpdateSDDProgress(fn func(*SDDProgress)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.sddProgress)
}

// UpdateSDDTokens records token usage for the current run.
func (s *State) UpdateSDDTokens(used, max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddProgress.TokensUsed = used
	s.sddProgress.TokensMax = max
}

// ClearSDDProgress resets the progress to its zero value.
func (s *State) ClearSDDProgress() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddProgress = SDDProgress{}
}

// FinishSDDRun marks the run ended. Unlike ClearSDDProgress it keeps the
// summary data (tasks, counts, timing) so the TUI can render the collapsed
// done/stopped line until the user's next turn. runErr is stored as a
// one-line summary for the stopped-line reason.
func (s *State) FinishSDDRun(succeeded bool, endedAt time.Time, runErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddProgress.Active = false
	s.sddProgress.Finished = true
	s.sddProgress.Succeeded = succeeded
	s.sddProgress.EndedAt = endedAt
	if runErr != nil {
		s.sddProgress.Error = shortErr(runErr)
	} else {
		s.sddProgress.Error = ""
	}
}

// shortErr returns the first line of err truncated to 80 characters.
func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
