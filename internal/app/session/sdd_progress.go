package session

// SDDPhase is the lifecycle state of one phase in an SDD task or the
// branch review.
type SDDPhase string

const (
	SDDPhasePending SDDPhase = "pending"
	SDDPhaseActive  SDDPhase = "active"
	SDDPhaseDone    SDDPhase = "done"
	SDDPhaseFailed  SDDPhase = "failed"
	SDDPhaseSkipped SDDPhase = "skipped"
)

// SDDTaskStatus is one row in the SDD progress panel. Each task tracks
// two sub-phases (implementer + reviewer) plus an overall phase.
type SDDTaskStatus struct {
	Name        string
	Phase       SDDPhase
	Implementer SDDPhase
	Reviewer    SDDPhase
	FixRound    int
	MaxFixes    int
	Detail      string
}

// SDDProgress is the live state of an SDD run, mirroring SwarmProgress
// but task-scoped rather than role-scoped.
type SDDProgress struct {
	Active       bool
	PlanName     string
	PlanPath     string
	Tasks        []SDDTaskStatus
	BranchReview SDDPhase
	TotalTasks   int
	DoneTasks    int
	TokensUsed   int
	TokensMax    int
}

func (p SDDProgress) clone() SDDProgress {
	out := p
	out.Tasks = append([]SDDTaskStatus(nil), p.Tasks...)
	return out
}

func (s *State) SetSDDProgress(p SDDProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddProgress = p.clone()
}

// SDDProgress returns a copy of the current SDD progress. The caller
// must not mutate the returned value; it is a defensive copy.
func (s *State) SDDProgress() SDDProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sddProgress.clone()
}

// UpdateSDDTask applies fn to the task at the given index (0-based).
// If the index is out of range, the call is a no-op.
func (s *State) UpdateSDDTask(index int, fn func(*SDDTaskStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.sddProgress.Tasks) {
		return
	}
	fn(&s.sddProgress.Tasks[index])
}

// UpdateSDDBranchReview sets the branch review phase on the current
// SDD progress.
func (s *State) UpdateSDDBranchReview(phase SDDPhase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddProgress.BranchReview = phase
}

// UpdateSDDTokens records the token usage for the current SDD run.
func (s *State) UpdateSDDTokens(used, max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddProgress.TokensUsed = used
	s.sddProgress.TokensMax = max
}

// ClearSDDProgress resets the SDD progress to its zero value.
func (s *State) ClearSDDProgress() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddProgress = SDDProgress{}
}
