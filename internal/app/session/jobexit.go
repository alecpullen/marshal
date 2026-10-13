package session

import (
	"strings"
	"time"
	"unicode/utf8"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

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
	// Lost marks a job whose session ended while it was still running, so
	// its outcome was never recorded. Lost exits are synthesized on resume,
	// stamped at the job's start, and never persisted: the job died with
	// the process that ran it, and its card must not read as running.
	Lost bool
}

// JobStartPrefix is how a shell call that started a background job
// summarises its result; the job ID follows it.
const JobStartPrefix = "started background job "

// JobStartedID returns the job ID a call with this result summary started, or
// "" when the summary is not a job start.
func JobStartedID(summary string) string {
	if !strings.HasPrefix(summary, JobStartPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(summary, JobStartPrefix))
}

// JobExitOutputCap bounds the output a job exit keeps: the tail, which is
// where a failure's cause usually is. A job can buffer far more than this,
// and every exit is held for the session's life and persisted with it.
const JobExitOutputCap = 16 << 10

const jobExitOutputDropped = "… earlier output dropped\n"

// capJobOutput keeps the last JobExitOutputCap bytes of out, cut at a rune
// boundary, behind a marker saying the head was dropped.
func capJobOutput(out string) string {
	if len(out) <= JobExitOutputCap {
		return out
	}
	cut := len(out) - JobExitOutputCap
	for cut < len(out) && !utf8.RuneStart(out[cut]) {
		cut++
	}
	return jobExitOutputDropped + out[cut:]
}

// AddJobExit appends a job's exit record to the transcript, persists it, and
// publishes it so every client — the TUI and ACP's stack projector alike —
// re-projects the job's card. A zero At is stamped at write time so
// transcript ordering is always well-defined.
func (s *State) AddJobExit(e JobExit) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.Output = capJobOutput(e.Output)
	s.mu.Lock()
	s.jobExits = append(s.jobExits, e)
	var records []db.JobExitRecord
	if s.persistenceEnabled() {
		records = jobExitRecordsLocked(s.jobExits)
	}
	s.mu.Unlock()

	published := e
	s.publishEvent(EventJobExit, Event{JobExit: &published})

	if records != nil {
		if err := s.db.SaveJobExits(s.sessionID, records); err != nil {
			s.logger.Error("save job exits failed", "error", err, "session_id", s.sessionID, "job", e.ID)
		}
	}
}

// jobExitRecordsLocked converts the recorded exits for storage, leaving out
// lost ones. Callers must hold s.mu.
func jobExitRecordsLocked(exits []JobExit) []db.JobExitRecord {
	out := make([]db.JobExitRecord, 0, len(exits))
	for _, e := range exits {
		if e.Lost {
			continue
		}
		out = append(out, db.JobExitRecord{
			ID: e.ID, Command: e.Command, ExitCode: e.ExitCode,
			DurationMs: e.Duration.Milliseconds(), Output: e.Output, At: e.At,
		})
	}
	return out
}

// loadJobExitsLocked restores the session's job exits after its tool rows,
// then marks every restored job start that never got an exit as lost: the
// job ran in a process that has since ended, so no exit is coming for it.
// Job IDs restart at job-1 in each process, so a start is paired with the
// first exit of its ID that follows it, not with any exit of its ID.
// Callers must hold s.mu.
func (s *State) loadJobExitsLocked() {
	records, err := s.db.LoadJobExits(s.sessionID)
	if err != nil {
		s.logger.Warn("failed to load job exits", "error", err, "session_id", s.sessionID)
	}
	for _, r := range records {
		s.jobExits = append(s.jobExits, JobExit{
			ID: r.ID, Command: r.Command, ExitCode: r.ExitCode,
			Duration: time.Duration(r.DurationMs) * time.Millisecond, Output: r.Output, At: r.At,
		})
	}
	s.jobExits = append(s.jobExits, lostJobs(s.auditLog, s.jobExits)...)
}

// lostJobs returns a lost exit for each job start in starts that no exit in
// exits settles. Both lists are in time order.
func lostJobs(starts []registry.AuditEvent, exits []JobExit) []JobExit {
	var lost []JobExit
	pending := map[string]*registry.AuditEvent{} // job ID → its latest unsettled start
	ei := 0
	for i := range starts {
		start := &starts[i]
		id := JobStartedID(start.ResultSummary)
		if id == "" {
			continue
		}
		// Settle every pending start whose exit came before this start.
		for ; ei < len(exits) && exits[ei].At.Before(start.Timestamp); ei++ {
			delete(pending, exits[ei].ID)
		}
		if prev := pending[id]; prev != nil {
			// Same ID started again with no exit between: the earlier
			// process ended while its job ran.
			lost = append(lost, lostExit(id, prev))
		}
		pending[id] = start
	}
	for ; ei < len(exits); ei++ {
		delete(pending, exits[ei].ID)
	}
	for i := range starts {
		start := &starts[i]
		if id := JobStartedID(start.ResultSummary); id != "" && pending[id] == start {
			lost = append(lost, lostExit(id, start))
		}
	}
	return lost
}

func lostExit(id string, start *registry.AuditEvent) JobExit {
	return JobExit{ID: id, ExitCode: -1, At: start.Timestamp, Lost: true}
}
