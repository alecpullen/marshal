package session

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"marshal/internal/db"
	"marshal/internal/progress"
	"marshal/internal/tools/registry"
)

// JobExit records a managed command's settled outcome. It is the transcript's
// only trace of a background job's outcome: before this existed, a job exited
// and nothing was written anywhere — not the exit code, not the duration, not
// the output. The status-line count simply decremented.
//
// Named for the exit specifically, to avoid confusion with
// native.JobEvent, which is the broker payload for count changes.
//
// The first group of fields is the original record. The second group is the
// additive structured evidence a managed execution carries: its cross-resume
// identity, the origin call, attachment and the finer outcome. Each is empty
// when the record predates it or the fact is unknown, and an unknown exit is
// never read as success.
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

	// ExecutionID is the cross-resume pairing identity of the execution. A
	// display job ID repeats in every process, so it alone cannot tell one
	// process's job-1 from the next's; this can. Empty on a legacy record,
	// which falls back to order-based pairing by display ID.
	ExecutionID string
	// Exit is the observed exit code when the process produced one. Nil means
	// none was observed — a start-up failure, or a legacy record — which
	// ExitCode cannot express (there, -1 doubles as unknown). A nil or
	// negative Exit must never be read as success.
	Exit *int
	// ConversationID is the owning conversation, which outlives the turn that
	// launched the command; Actor is the parent agent, a named subagent, or a
	// pipeline worker.
	ConversationID string
	Actor          string
	// TurnID, StepID and ToolCallID identify the originating call.
	TurnID     string
	StepID     string
	ToolCallID string
	// Cwd and Generation are the working directory and runtime config
	// generation captured at admission.
	Cwd        string
	Generation int
	// PID is the process identity published when the process started.
	PID int
	// StartedAt is the admission time; EndedAt is the settlement time.
	StartedAt time.Time
	EndedAt   time.Time
	// State is the lifecycle status the execution settled in ("completed",
	// "failed", "killed", "timed_out", …); Outcome is the finer cause
	// ("nonzero_exit", "startup_failure", "stopped", "resource_limit", …).
	// Both are empty on a legacy record, which must read as unknown rather
	// than as success.
	State   string
	Outcome string
	// Attachment is how the launching caller related to the execution when it
	// ended: "background" for a detached job. Empty when unrecorded.
	Attachment string
	// Wait is the effective foreground wait the launch used, for evidence.
	Wait string
	// DetachReason and DetachedAt record why and when a wait handed off to
	// background attachment.
	DetachReason string
	DetachedAt   time.Time
	// StopCause and StopRequestedAt record an accepted explicit stop request,
	// separately from the outcome it produced: a request that arrived after
	// the process exited is evidence only, never a relabelling.
	StopCause       string
	StopRequestedAt time.Time
	// Truncated reports whether the retained output hit its cap, so a reader
	// knows the tail is not the whole story.
	Truncated bool

	// ProgressConfig is the normalized progress configuration captured at
	// admission and Progress the final bounded progress snapshot, the durable
	// evidence that survives the process. Both are additive: nil means the
	// record predates progress or never observed any.
	ProgressConfig *progress.Config
	Progress       *progress.Snapshot
	// ResultRevision is the content revision of this settlement, so a stored
	// work view can tell an acknowledged result from a re-run; SourceEpoch is
	// the runtime epoch the evidence belongs to, carried so a lost job keeps
	// the facts of the launch it belongs to.
	ResultRevision string
	SourceEpoch    string
}

// Identity returns the key that pairs this exit with the call that started its
// execution. A structured record's execution identity is stable across
// resumes; a legacy record has none, so its display ID and time stand in —
// which is exactly why legacy pairing must stay order-based.
func (e JobExit) Identity() string {
	if id := strings.TrimSpace(e.ExecutionID); id != "" {
		return "exec:" + id
	}
	return "legacy:" + e.ID + "@" + e.At.UTC().Format(time.RFC3339Nano)
}

// JobStart is the managed execution a shell call started, as the transcript
// records it. DisplayID is what a card shows; Identity is what pairs the call
// with its exit; Execution carries the structured metadata when the record has
// one.
type JobStart struct {
	DisplayID string
	Identity  string
	Execution registry.CommandExecution
	// Structured reports whether the record carried structured metadata
	// rather than only the legacy summary text.
	Structured bool
}

// JobStarted reports the managed execution a call's audit started. ok is false
// when the call did not start a card: a foreground command — including one
// whose metadata merely describes the attached execution — has its own tool
// row and must not become a background card.
//
// A structured record is preferred over the legacy summary text, and only a
// record that ended up background-attached (or has no attachment at all, i.e.
// the legacy shape) becomes a card.
func JobStarted(ev registry.AuditEvent) (JobStart, bool) {
	if ex, ok := ev.Notice.Execution(); ok {
		switch ex.Attachment {
		case registry.ExecutionAttachmentForeground:
			return JobStart{}, false
		case registry.ExecutionAttachmentBackground:
			// The display ID is what a card shows; the identity is what pairs
			// it, and it stands in as the label only when a record carries no
			// display ID at all. A card with no label would be indistinguishable
			// from the tool row beside it, and this metadata says the command is
			// background work.
			display := strings.TrimSpace(ex.DisplayID)
			if display == "" {
				display = JobStartedID(ev.ResultSummary)
			}
			identity := strings.TrimSpace(ex.ExecutionID)
			if display == "" {
				display = identity
			}
			if display == "" {
				return JobStart{}, false
			}
			return JobStart{DisplayID: display, Identity: identity, Execution: ex, Structured: true}, true
		}
	}
	if id := JobStartedID(ev.ResultSummary); id != "" {
		return JobStart{DisplayID: id}, true
	}
	return JobStart{}, false
}

// JobStartPrefix is how a shell call that started a background job
// summarises its result; the job ID follows it.
const JobStartPrefix = "started background job "

// JobStartedID returns the job ID a call with this result summary started, or
// "" when the summary is not a job start. It reads the legacy summary text
// only: use JobStarted to account for structured metadata.
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

// AddJobExit records a job's exit, persists it, and publishes it so every
// client — the TUI and ACP's stack projector alike — re-projects the job's
// card. A zero At is stamped at write time so transcript ordering is always
// well-defined.
//
// Ordering is the contract here: the record is written durably *before* the
// completion event is published, so a client that reacts to the event can read
// the outcome back after a restart. A failed write returns its error, keeps the
// record in memory and leaves it pending, and publishes nothing: announcing a
// completion that was never stored is precisely how a resumed session ends up
// showing a card that runs forever.
//
// Calling it again for the same execution is safe and is what a retry does: a
// record is identified by its execution identity (display ID and time for a
// legacy record), so a retried settlement updates the existing entry instead of
// appending a second completion.
func (s *State) AddJobExit(e JobExit) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.Output = capJobOutput(e.Output)

	s.mu.Lock()
	s.upsertJobExitLocked(e)
	s.mu.Unlock()

	// Hold the completion back from the event stream until it is durable. A
	// client that reacts to the event re-projects the job's card, so announcing
	// one that was not stored shows a settled card the next resume cannot
	// reproduce.
	//
	// Both queues are keyed by execution identity, so one execution produces one
	// completion and one announcement: a retry replaces its own pending entry,
	// and a settlement that was already announced — a duplicate delivery, or a
	// retry after a success — announces nothing.
	s.jobExitSaveMu.Lock()
	if !s.jobExitsPublished[e.Identity()] {
		if s.jobExitsUnpublished == nil {
			s.jobExitsUnpublished = make(map[string]JobExit)
		}
		s.jobExitsUnpublished[e.Identity()] = e
	}
	s.jobExitSaveMu.Unlock()

	return s.persistJobExits()
}

// upsertJobExitLocked appends e, or replaces the record with the same identity
// in place. Callers must hold s.mu.
func (s *State) upsertJobExitLocked(e JobExit) {
	identity := e.Identity()
	for i := range s.jobExits {
		if s.jobExits[i].Identity() == identity {
			s.jobExits[i] = e
			return
		}
	}
	s.jobExits = append(s.jobExits, e)
}

// persistJobExits writes the current job-exit list and, once the write is
// durable, publishes the completions it was holding back.
//
// The snapshot and the write are serialized against each other on a lock of
// their own, *not* on the broad state mutex: the save spans a database round
// trip, which must never be held while the rest of the session waits, and two
// concurrent saves that interleaved a snapshot and a write could store an older
// list over a newer one. Taking the snapshot under this lock is what makes the
// last write the newest list.
func (s *State) persistJobExits() error {
	if !s.persistenceEnabled() {
		// Nothing to store, so nothing can fail: announce now.
		s.publishPendingJobExits()
		return nil
	}

	s.jobExitSaveMu.Lock()
	s.mu.Lock()
	records := jobExitRecords(s.jobExits)
	s.mu.Unlock()
	if len(records) > 0 {
		if err := s.db.SaveJobExits(s.sessionID, records); err != nil {
			s.jobExitsPending = true
			s.jobExitSaveMu.Unlock()
			s.logger.Error("save job exits failed", "error", err, "session_id", s.sessionID, "records", len(records))
			return err
		}
	}
	s.jobExitsPending = false
	s.jobExitSaveMu.Unlock()

	s.publishPendingJobExits()
	return nil
}

// publishPendingJobExits announces the completions that were waiting on a
// durable write. It is called only after the write succeeded, so nothing here
// claims an outcome the session cannot restore, and each identity is announced
// at most once however many times it is settled or retried.
func (s *State) publishPendingJobExits() {
	s.jobExitSaveMu.Lock()
	pending := make([]JobExit, 0, len(s.jobExitsUnpublished))
	for _, e := range s.jobExitsUnpublished {
		pending = append(pending, e)
	}
	s.jobExitsUnpublished = nil
	if len(pending) > 0 && s.jobExitsPublished == nil {
		s.jobExitsPublished = make(map[string]bool, len(pending))
	}
	s.jobExitSaveMu.Unlock()

	// Oldest first, so a client that reacts to each event sees the completions
	// in the order they happened rather than in map iteration order.
	sort.Slice(pending, func(i, j int) bool {
		if !pending[i].At.Equal(pending[j].At) {
			return pending[i].At.Before(pending[j].At)
		}
		return pending[i].ID < pending[j].ID
	})
	for _, e := range pending {
		published := e
		s.publishEvent(EventJobExit, Event{JobExit: &published})
		s.jobExitSaveMu.Lock()
		s.jobExitsPublished[e.Identity()] = true
		s.jobExitSaveMu.Unlock()
	}
}

// RetryJobExitSaves re-attempts the durable write of job exits that were
// recorded but could not be stored, and announces them once it succeeds. It
// returns the write error again when the retry fails, so a caller reports
// honestly instead of assuming the outcome is now durable; a session with
// nothing pending returns nil without touching the database.
func (s *State) RetryJobExitSaves() error {
	if !s.jobExitSavesPending() {
		return nil
	}
	return s.persistJobExits()
}

// jobExitSavesPending reports whether a previous write failed and the
// in-memory list has not been stored successfully since.
func (s *State) jobExitSavesPending() bool {
	s.jobExitSaveMu.Lock()
	defer s.jobExitSaveMu.Unlock()
	return s.jobExitsPending
}

// jobExitRecords converts recorded exits for storage, leaving out lost ones.
// A lost exit is synthesized from a start that never settled; it was never a
// completion, and the next resume synthesizes it again from the same start.
func jobExitRecords(exits []JobExit) []db.JobExitRecord {
	out := make([]db.JobExitRecord, 0, len(exits))
	for _, e := range exits {
		if e.Lost {
			continue
		}
		out = append(out, jobExitRecord(e))
	}
	return out
}

// jobExitRecord maps one in-memory exit onto its stored form.
func jobExitRecord(e JobExit) db.JobExitRecord {
	r := db.JobExitRecord{
		ID: e.ID, Command: e.Command, ExitCode: e.ExitCode,
		DurationMs: e.Duration.Milliseconds(), Output: e.Output, At: e.At,
		ExecutionID:     e.ExecutionID,
		ConversationID:  e.ConversationID,
		Actor:           e.Actor,
		TurnID:          e.TurnID,
		StepID:          e.StepID,
		ToolCallID:      e.ToolCallID,
		Cwd:             e.Cwd,
		Generation:      e.Generation,
		PID:             e.PID,
		StartedAt:       e.StartedAt,
		EndedAt:         e.EndedAt,
		State:           e.State,
		Outcome:         e.Outcome,
		Attachment:      e.Attachment,
		Wait:            e.Wait,
		DetachReason:    e.DetachReason,
		DetachedAt:      e.DetachedAt,
		StopCause:       e.StopCause,
		StopRequestedAt: e.StopRequestedAt,
		Truncated:       e.Truncated,
	}
	if e.Exit != nil {
		code := *e.Exit
		r.ActualExitCode = &code
	}
	if e.ProgressConfig != nil {
		cfg := *e.ProgressConfig
		r.ProgressConfig = &cfg
	}
	r.Progress = progress.Clone(e.Progress)
	r.ResultRevision = e.ResultRevision
	r.SourceEpoch = e.SourceEpoch
	return r
}

// jobExitFromRecord reverses jobExitRecord. A record written before the
// structured fields existed restores with them empty: unknown, not zero-valued
// facts.
func jobExitFromRecord(r db.JobExitRecord) JobExit {
	e := JobExit{
		ID: r.ID, Command: r.Command, ExitCode: r.ExitCode,
		Duration: time.Duration(r.DurationMs) * time.Millisecond, Output: r.Output, At: r.At,
		ExecutionID:     r.ExecutionID,
		ConversationID:  r.ConversationID,
		Actor:           r.Actor,
		TurnID:          r.TurnID,
		StepID:          r.StepID,
		ToolCallID:      r.ToolCallID,
		Cwd:             r.Cwd,
		Generation:      r.Generation,
		PID:             r.PID,
		StartedAt:       r.StartedAt,
		EndedAt:         r.EndedAt,
		State:           r.State,
		Outcome:         r.Outcome,
		Attachment:      r.Attachment,
		Wait:            r.Wait,
		DetachReason:    r.DetachReason,
		DetachedAt:      r.DetachedAt,
		StopCause:       r.StopCause,
		StopRequestedAt: r.StopRequestedAt,
		Truncated:       r.Truncated,
		ResultRevision:  r.ResultRevision,
		SourceEpoch:     r.SourceEpoch,
	}
	if r.ActualExitCode != nil {
		code := *r.ActualExitCode
		e.Exit = &code
	}
	if r.ProgressConfig != nil {
		cfg := *r.ProgressConfig
		e.ProgressConfig = &cfg
	}
	e.Progress = progress.Clone(r.Progress)
	return e
}

// JobExitFromExecution builds the exit record for a managed completion from the
// structured execution record the manager published. It is the one place the
// two shapes meet, so a producer cannot fill one field set and forget the other
// — the identity it writes here is what pairs the exit with the call that
// started the execution, even when the completion reaches the transcript before
// that call's audit row.
//
// exitCode is the process's observed code, nil when it never produced one; the
// legacy ExitCode field carries the record's -1-as-unknown convention, and Exit
// carries the truth.
func JobExitFromExecution(ex registry.CommandExecution, command, output string, ran time.Duration, exitCode *int) JobExit {
	e := JobExit{
		ID:              ex.DisplayID,
		Command:         command,
		Duration:        ran,
		Output:          output,
		At:              ex.EndedAt,
		ExecutionID:     ex.ExecutionID,
		ConversationID:  ex.ConversationID,
		Actor:           ex.Actor,
		TurnID:          ex.TurnID,
		StepID:          ex.StepID,
		ToolCallID:      ex.ToolCallID,
		Cwd:             ex.Cwd,
		Generation:      ex.Generation,
		PID:             ex.PID,
		StartedAt:       ex.StartedAt,
		EndedAt:         ex.EndedAt,
		State:           ex.State,
		Outcome:         ex.Outcome,
		Attachment:      string(ex.Attachment),
		Wait:            ex.Wait,
		DetachReason:    ex.DetachReason,
		DetachedAt:      ex.DetachedAt,
		StopCause:       ex.StopCause,
		StopRequestedAt: ex.StopRequestedAt,
		Truncated:       ex.Truncated,
		ResultRevision:  ex.ResultRevision,
		SourceEpoch:     ex.SourceEpoch,
		ExitCode:        -1,
	}
	if ex.ProgressConfig != nil {
		cfg := *ex.ProgressConfig
		e.ProgressConfig = &cfg
	}
	e.Progress = progress.Clone(ex.Progress)
	if e.ID == "" {
		e.ID = ex.ExecutionID
	}
	if exitCode != nil {
		code := *exitCode
		e.Exit = &code
		e.ExitCode = code
	}
	return e
}

// loadJobExitsLocked restores the session's job exits after its tool rows,
// then marks every restored job start that never got an exit as lost: the
// job ran in a process that has since ended, so no exit is coming for it.
// Job IDs restart at job-1 in each process, so a start is paired with the
// first exit of its ID that follows it, not with any exit of its ID — unless
// both carry a structured execution identity, which pairs them regardless of
// order. Callers must hold s.mu.
func (s *State) loadJobExitsLocked() {
	records, err := s.db.LoadJobExits(s.sessionID)
	if err != nil {
		s.logger.Warn("failed to load job exits", "error", err, "session_id", s.sessionID)
	}
	for _, r := range records {
		s.jobExits = append(s.jobExits, jobExitFromRecord(r))
	}
	s.jobExits = append(s.jobExits, lostJobs(s.auditLog, s.jobExits)...)
}

// lostJobs returns a lost exit for each job start in starts that no exit in
// exits settles. Both lists are in time order.
//
// Structured records pair by identity first, so a start whose execution never
// settled is lost even when the transcript's timestamps are ambiguous; only the
// records without an identity fall back to pairing a start with the first exit
// of its display ID that follows it in time. A lost exit carries the start's
// structured metadata, which is what lets the card it belongs to be found by
// identity afterwards.
func lostJobs(starts []registry.AuditEvent, exits []JobExit) []JobExit {
	settled := map[string]bool{}
	for _, e := range exits {
		settled[e.Identity()] = true
	}

	var lost []JobExit
	pending := map[string]*registry.AuditEvent{} // job ID → its latest unsettled start
	ei := 0
	for i := range starts {
		start := &starts[i]
		js, ok := JobStarted(*start)
		if !ok {
			continue
		}
		// Settle every pending start whose exit came before this start.
		for ; ei < len(exits) && exits[ei].At.Before(start.Timestamp); ei++ {
			clearPending(pending, exits[ei])
		}
		if prev := pending[js.DisplayID]; prev != nil {
			// The same display ID started again with nothing settling the
			// earlier start: that process ended while its job ran, whether or
			// not this one has a completion of its own.
			lost = append(lost, lostExit(*prev))
		}
		if js.Structured && settled["exec:"+js.Identity] {
			// This execution's own completion exists — the completion may even
			// have been written before this call's audit row — so nothing is
			// pending for it.
			delete(pending, js.DisplayID)
			continue
		}
		pending[js.DisplayID] = start
	}
	for ; ei < len(exits); ei++ {
		clearPending(pending, exits[ei])
	}
	for i := range starts {
		start := &starts[i]
		js, ok := JobStarted(*start)
		if !ok {
			continue
		}
		if pending[js.DisplayID] != start {
			continue
		}
		if js.Structured && settled["exec:"+js.Identity] {
			continue
		}
		lost = append(lost, lostExit(*start))
	}
	return lost
}

// clearPending removes the pending start a legacy exit settles. Job display IDs
// repeat in every process, so a record without an execution identity pairs by
// display ID and time — the legacy fallback. A record that carries an identity
// pairs by that identity instead: clearing by display ID here would let one
// process's completion settle another process's still-running start.
func clearPending(pending map[string]*registry.AuditEvent, e JobExit) {
	if e.ExecutionID != "" {
		return
	}
	delete(pending, e.ID)
}

// lostExit synthesizes the lost outcome of a start that never settled. It
// carries the start's display ID, time and structured identity, and no exit
// code: the process's own outcome is unknown, and -1 is the legacy spelling of
// exactly that.
func lostExit(start registry.AuditEvent) JobExit {
	js, _ := JobStarted(start)
	e := JobExit{ID: js.DisplayID, ExitCode: -1, At: start.Timestamp, Lost: true}
	if js.Structured {
		ex := js.Execution
		e.ExecutionID = ex.ExecutionID
		e.ConversationID = ex.ConversationID
		e.Actor = ex.Actor
		e.TurnID = ex.TurnID
		e.StepID = ex.StepID
		e.ToolCallID = ex.ToolCallID
		e.Cwd = ex.Cwd
		e.Generation = ex.Generation
		e.PID = ex.PID
		e.StartedAt = ex.StartedAt
		e.Attachment = string(ex.Attachment)
		e.Wait = ex.Wait
		e.DetachReason = ex.DetachReason
		e.DetachedAt = ex.DetachedAt
		e.StopCause = ex.StopCause
		e.StopRequestedAt = ex.StopRequestedAt
		// The launch notice's stable facts, including its source epoch and
		// progress configuration, survive to the lost record. The result
		// revision is left exactly as the start recorded it (unknown), never
		// synthesized from the restart time: a lost job's own revision was
		// never observed.
		e.SourceEpoch = ex.SourceEpoch
		e.ResultRevision = ex.ResultRevision
		if ex.ProgressConfig != nil {
			cfg := *ex.ProgressConfig
			e.ProgressConfig = &cfg
		}
		e.Progress = progress.Clone(ex.Progress)
	}
	return e
}

// JobExitSnapshot returns a deep copy of the session's job exits, so a caller
// can hold or publish them without the session mutating them underneath. Every
// nested pointer — the observed exit code, the progress configuration and the
// progress snapshot — is copied too.
func (s *State) JobExitSnapshot() []JobExit {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JobExit, len(s.jobExits))
	for i, e := range s.jobExits {
		out[i] = e
		if e.Exit != nil {
			code := *e.Exit
			out[i].Exit = &code
		}
		if e.ProgressConfig != nil {
			cfg := *e.ProgressConfig
			out[i].ProgressConfig = &cfg
		}
		out[i].Progress = progress.Clone(e.Progress)
	}
	return out
}

// The work-view index is the session's cross-resume record of settled work:
// which watch results have arrived, at what revision, and which of them the
// caller has acknowledged. It is persisted whole in session_state, so every
// mutation goes through the same serialized snapshot-and-write sequence job
// exits use — snapshot under mu, write outside it, commit only on success.
//
// safeWorkResult bounds the free text a work result carries before it is ever
// stored or shown. Identity, origin and reference keys are copied exactly: they
// are structured keys, not terminal text, and truncating one would break the
// pairing it exists to provide.
func safeWorkResult(record db.WorkResultRecord) db.WorkResultRecord {
	record.Label = progress.Clean(record.Label, 80, false)
	record.Owner = progress.Clean(record.Owner, 80, false)
	record.Detail = cleanWorkDetail(record.Detail)
	return record
}

// cleanWorkDetail strips control sequences and redacts secrets, then cuts the
// residue to 1,024 UTF-8 bytes at a rune boundary.
func cleanWorkDetail(detail string) string {
	detail = progress.Clean(detail, 0, false)
	const maxDetailBytes = 1024
	if len(detail) <= maxDetailBytes {
		return detail
	}
	cut := maxDetailBytes
	for cut > 0 && !utf8.RuneStart(detail[cut]) {
		cut--
	}
	return detail[:cut]
}

// copyWorkView deep-copies a work view, including every nested slice. A work
// result has no pointer fields, so copying each element is a complete copy.
func copyWorkView(v db.WorkViewRecord) db.WorkViewRecord {
	return db.WorkViewRecord{
		Version:      v.Version,
		WatchResults: append([]db.WorkResultRecord(nil), v.WatchResults...),
		AgentResults: append([]db.WorkResultRecord(nil), v.AgentResults...),
		Acknowledged: append([]db.WorkAcknowledgment(nil), v.Acknowledged...),
	}
}

// upsertAcknowledgment adds an acknowledgment, replacing any earlier one for the
// same task key: a task carries at most its newest terminal acknowledgment, so a
// re-run's revision supersedes the one before it.
func upsertAcknowledgment(list []db.WorkAcknowledgment, ack db.WorkAcknowledgment) []db.WorkAcknowledgment {
	for i := range list {
		if list[i].TaskKey == ack.TaskKey {
			list[i] = ack
			return list
		}
	}
	return append(list, ack)
}

// loadWorkView copies the session's stored work view into memory at startup,
// beside the job-exit restoration. An unknown stored version or corrupt JSON is
// a diagnostic: the in-memory view stays empty and every later save refuses, so
// the session never overwrites data it cannot understand.
func (s *State) loadWorkView() {
	record, err := s.db.LoadWorkView(s.sessionID)
	if err != nil {
		s.logger.Warn("failed to load work view", "error", err, "session_id", s.sessionID)
		s.workViewSaveMu.Lock()
		s.workViewLoadErr = fmt.Errorf("work view unavailable: %w", err)
		s.workViewSaveMu.Unlock()
		return
	}
	s.mu.Lock()
	s.workView = copyWorkView(record)
	s.mu.Unlock()
}

// WorkView returns a deep copy of the session's work-view index, so a caller
// cannot mutate the session's state through it.
func (s *State) WorkView() db.WorkViewRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyWorkView(s.workView)
}

// SaveWatchWorkResult stores one settled watch result and persists the work
// view. The result upserts by its canonical task key, so a re-run — a new result
// revision for the same key — supersedes the earlier result rather than
// accumulating beside it, and an acknowledgment of the old revision can no
// longer hide the new one.
//
// A failed write retains the in-memory result and its attention and marks the
// view dirty for a later RetryWorkViewSave; it returns the write error rather
// than pretending the result is durable.
func (s *State) SaveWatchWorkResult(record db.WorkResultRecord) error {
	record = safeWorkResult(record)
	s.workViewSaveMu.Lock()
	defer s.workViewSaveMu.Unlock()
	s.mu.Lock()
	for i := range s.workView.WatchResults {
		if s.workView.WatchResults[i].TaskKey == record.TaskKey {
			s.workView.WatchResults[i] = record
			s.mu.Unlock()
			return s.persistWorkViewLocked()
		}
	}
	s.workView.WatchResults = append(s.workView.WatchResults, record)
	s.mu.Unlock()
	return s.persistWorkViewLocked()
}

// AcknowledgeWork records that the caller has seen a task's result at a
// specific revision. The pair is validated against the live view *before* it is
// stored: the task key must name a stored result and the revision must be that
// result's current revision, so a display job ID never validates and a stale
// acknowledgment of an earlier revision cannot acknowledge a re-run's newer one.
//
// The acknowledgment is committed only once its serialized save succeeds. On
// failure it stays pending — the result's attention remains visible — and the
// error is returned; RetryWorkViewSave re-applies it to the newest snapshot.
func (s *State) AcknowledgeWork(taskKey, resultRevision string) error {
	taskKey = strings.TrimSpace(taskKey)
	resultRevision = strings.TrimSpace(resultRevision)
	if taskKey == "" || resultRevision == "" {
		return fmt.Errorf("work acknowledgment requires a task key and a result revision")
	}
	s.workViewSaveMu.Lock()
	defer s.workViewSaveMu.Unlock()
	if err := s.validateWorkAck(taskKey, resultRevision); err != nil {
		return err
	}
	s.workViewPendingAcks = upsertAcknowledgment(s.workViewPendingAcks,
		db.WorkAcknowledgment{TaskKey: taskKey, ResultRevision: resultRevision})
	return s.persistWorkViewLocked()
}

// RetryWorkViewSave re-attempts the durable write of a work view that could not
// be stored. It reports the failure again when the retry fails, and a session
// with nothing pending returns nil without touching the database.
func (s *State) RetryWorkViewSave() error {
	s.workViewSaveMu.Lock()
	defer s.workViewSaveMu.Unlock()
	if !s.workViewDirty && len(s.workViewPendingAcks) == 0 {
		return nil
	}
	return s.persistWorkViewLocked()
}

// validateWorkAck checks an acknowledgment pair against the live work view: the
// key must match a stored result — a display ID is not a task key — and the
// revision must be that result's current one, so an old acknowledgment cannot
// acknowledge a newer terminal revision. It never edits the source result.
func (s *State) validateWorkAck(taskKey, resultRevision string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.workView.WatchResults {
		if r.TaskKey == taskKey {
			if r.ResultRevision != resultRevision {
				return fmt.Errorf("work result %q is at revision %q, not %q", taskKey, r.ResultRevision, resultRevision)
			}
			return nil
		}
	}
	for _, r := range s.workView.AgentResults {
		if r.TaskKey == taskKey {
			if r.ResultRevision != resultRevision {
				return fmt.Errorf("work result %q is at revision %q, not %q", taskKey, r.ResultRevision, resultRevision)
			}
			return nil
		}
	}
	return fmt.Errorf("no work result for task key %q", taskKey)
}

// persistWorkViewLocked snapshots the in-memory work view, writes it durably,
// and only then commits the pending acknowledgments and clears the dirty flag,
// so a failed write leaves no acknowledgment claiming a result the next resume
// cannot restore. The snapshot is taken under mu; the database round trip runs
// with mu released. Callers must hold workViewSaveMu.
func (s *State) persistWorkViewLocked() error {
	if s.workViewLoadErr != nil {
		return s.workViewLoadErr
	}
	s.mu.Lock()
	record := copyWorkView(s.workView)
	s.mu.Unlock()
	record.Version = 1
	for _, ack := range s.workViewPendingAcks {
		record.Acknowledged = upsertAcknowledgment(record.Acknowledged, ack)
	}
	if s.persistenceEnabled() {
		if err := s.db.SaveWorkView(s.sessionID, record); err != nil {
			s.workViewDirty = true
			s.logger.Error("save work view failed", "error", err, "session_id", s.sessionID)
			return err
		}
	}
	// The write is durable (or there is none): commit the acknowledgments into
	// memory, where a reader now sees the result as acknowledged.
	if len(s.workViewPendingAcks) > 0 {
		s.mu.Lock()
		for _, ack := range s.workViewPendingAcks {
			s.workView.Acknowledged = upsertAcknowledgment(s.workView.Acknowledged, ack)
		}
		s.mu.Unlock()
		s.workViewPendingAcks = nil
	}
	s.workViewDirty = false
	return nil
}
