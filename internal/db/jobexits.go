package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// JobExitRecord is one background job's exit, stored so a resumed session's
// job cards settle the way they did before exit. The list lives in
// session_state as one JSON value, like todos: it is small (one bounded
// record per job) and only ever read whole.
//
// The first six fields are the original record, unchanged. Everything after
// them is additive structured evidence: execution identity, the origin call,
// attachment and the finer outcome. Each new field is omitted when empty, so a
// legacy record reads back exactly as it was written, and a reader that does
// not know a key ignores it rather than guessing. No migration is involved:
// the record shape is JSON inside one session_state row.
type JobExitRecord struct {
	ID         string    `json:"id"`
	Command    string    `json:"command,omitempty"`
	ExitCode   int       `json:"exit_code"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	Output     string    `json:"output,omitempty"`
	At         time.Time `json:"at"`

	// ExecutionID is the manager's execution identity. It is what pairs a
	// start with its exit across a resume, where the display ID repeats.
	ExecutionID string `json:"execution_id,omitempty"`
	// ActualExitCode is the observed exit code when the process produced one.
	// Nil means none was observed (a start-up failure), which the legacy
	// ExitCode field cannot express: there, -1 doubles as "unknown". A nil
	// here must never be read as success.
	ActualExitCode *int `json:"actual_exit_code,omitempty"`
	// ConversationID is the owning conversation, which outlives the turn that
	// launched the command.
	ConversationID string `json:"conversation_id,omitempty"`
	// Actor is the parent agent, a named subagent, or a pipeline worker.
	Actor string `json:"actor,omitempty"`
	// TurnID, StepID and ToolCallID identify the originating call, so an exit
	// can be paired with the call that produced it without timestamp order.
	TurnID     string `json:"turn_id,omitempty"`
	StepID     string `json:"step_id,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Cwd and Generation are the working directory and runtime config
	// generation captured at admission.
	Cwd        string `json:"cwd,omitempty"`
	Generation int    `json:"generation,omitempty"`
	// PID is the process identity published when the process started.
	PID int `json:"pid,omitempty"`
	// StartedAt is the admission time and EndedAt the settlement time, so a
	// restored card can show both without recomputing them from the exit.
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	// State is the lifecycle status the execution settled in ("completed",
	// "failed", "killed", "timed_out", …) and Outcome the finer cause
	// ("nonzero_exit", "startup_failure", "stopped", "resource_limit", …).
	// Empty on a legacy record, which must be shown as unknown rather than as
	// success.
	State   string `json:"state,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	// Attachment is how the launching caller related to the execution when it
	// ended: "background" for a detached job, "foreground" for a command the
	// caller was still waiting on. Empty on a legacy record.
	Attachment string `json:"attachment,omitempty"`
	// Wait is the effective foreground wait budget the launch used, in text
	// ("10s"), for diagnostics. Empty when the caller did not record one.
	Wait string `json:"wait,omitempty"`
	// DetachReason and DetachedAt record why and when a wait handed off to
	// background attachment.
	DetachReason string    `json:"detach_reason,omitempty"`
	DetachedAt   time.Time `json:"detached_at,omitempty"`
	// StopCause and StopRequestedAt record an accepted explicit stop request,
	// separately from the outcome it produced: a request that arrived after
	// the process exited is evidence only.
	StopCause       string    `json:"stop_cause,omitempty"`
	StopRequestedAt time.Time `json:"stop_requested_at,omitempty"`
	// Truncated reports whether the retained output hit its cap, so a reader
	// knows the tail it is seeing is not the whole story.
	Truncated bool `json:"truncated,omitempty"`
}

// SaveJobExits replaces the session's stored job exits. The write is a whole
// replacement, so callers must serialise it against other writers for the same
// session (session.State does, on a lock dedicated to this sequence): two
// saves racing would otherwise let an older snapshot land last.
func (db *DB) SaveJobExits(sessionID string, exits []JobExitRecord) error {
	data, err := json.Marshal(exits)
	if err != nil {
		return fmt.Errorf("marshal job exits: %w", err)
	}
	_, err = db.sqlDB.Exec(
		`INSERT INTO session_state (session_id, key, value) VALUES (?, 'job_exits', ?)
		 ON CONFLICT(session_id, key) DO UPDATE SET value=excluded.value`,
		sessionID, string(data),
	)
	if err != nil {
		return fmt.Errorf("save job exits: %w", err)
	}
	return nil
}

// LoadJobExits restores the session's job exits; a session with none returns
// nil.
func (db *DB) LoadJobExits(sessionID string) ([]JobExitRecord, error) {
	row := db.sqlDB.QueryRow(
		`SELECT value FROM session_state WHERE session_id=? AND key='job_exits'`,
		sessionID,
	)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load job exits: %w", err)
	}
	var exits []JobExitRecord
	if err := json.Unmarshal([]byte(raw), &exits); err != nil {
		return nil, fmt.Errorf("unmarshal job exits: %w", err)
	}
	return exits, nil
}
