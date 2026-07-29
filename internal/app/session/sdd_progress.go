package session

// SDDProgress is the live state of a plan-execution run, rendered by the
// TUI panel. The controller emits events; the app layer maps them here.
type SDDProgress struct {
	Active       bool
	PlanName     string
	PlanPath     string
	Branch       string
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
