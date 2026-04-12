// Package session provides the SQLite ledger for marshal sessions, tasks,
// and per-round token/timing records.
package session

import "time"

// Status values for tasks.
type Status string

const (
	StatusRunning        Status = "running"
	StatusPassed         Status = "passed"
	StatusFailed         Status = "failed"
	StatusRevertedByUser Status = "reverted_by_user"
	StatusDiscarded      Status = "discarded"
)

// Session is one marshal working session: a target branch + staging branch
// pair started when the user runs marshal.
type Session struct {
	ID               string
	TargetBranch     string
	TargetStartSHA   string
	StagingBranch    string
	StartedAt        time.Time
	ShippedAt        *time.Time
	ShippedTargetSHA *string
}

// Task is one user turn — a single bounded task run on a marshal/task-<id>
// branch.
type Task struct {
	ID               string
	SessionID        string
	Prompt           string
	ParentStagingSHA string
	StagingSHA       *string // set on status=passed
	Status           Status
	StartedAt        time.Time
	EndedAt          *time.Time
	Summary          *string // from critic verdict
}

// Round records one executor + critic cycle within a task.
type Round struct {
	ID               int64
	SessionID        string
	TaskID           string
	Round            int
	Role             string
	Model            string
	PromptTokens     int
	CompletionTokens int
	DurationMS       int64
	Content          string
	VerdictJSON      *string // JSON-encoded Verdict, set for critic rounds
	ThinkBlocks      *string // JSON array of stripped <think> block strings
}

// TaskUpdate carries the fields that change on a task after it completes.
type TaskUpdate struct {
	Status     Status
	StagingSHA *string
	EndedAt    *time.Time
	Summary    *string
}
