package session

import "time"

// JobExit records a background shell job finishing. It is the transcript's
// only trace of a job's outcome: before this existed, a job exited and
// nothing was written anywhere — not the exit code, not the duration, not
// the output. The status-line count simply decremented.
//
// Named for the exit specifically, to avoid confusion with
// native.JobEvent, which is the broker payload for count changes.
type JobExit struct {
	ID       string
	Command  string
	ExitCode int
	Duration time.Duration
	Output   string
	At       time.Time
}

// AddJobExit appends a job's exit record to the transcript. A zero At is
// stamped at write time so transcript ordering is always well-defined.
func (s *State) AddJobExit(e JobExit) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	s.mu.Lock()
	s.jobExits = append(s.jobExits, e)
	s.mu.Unlock()
}
