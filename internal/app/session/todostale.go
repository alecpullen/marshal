package session

import (
	"time"

	"marshal/internal/db"
)

// The stale rule is shared by the band (which shows it) and the runner
// (which nudges the model), so the two can never disagree.
const (
	TodoStaleAfter     = 5 * time.Minute
	TodoStaleResponses = 8
)

// TodoClock measures how long a todo list has gone unmaintained. LastWrite is
// the later of the last todo.write and the current turn's start, so a list
// carried in from an earlier session starts its clock with the turn.
type TodoClock struct {
	LastWrite time.Time
	Responses int
}

// TodoStale reports whether the list has gone unmaintained during a busy
// turn: an item is in progress and at least TodoStaleAfter and
// TodoStaleResponses have passed since the clock started. age is the time
// since then, for display.
func TodoStale(todos []db.TodoItem, c TodoClock, busy bool, now time.Time) (bool, time.Duration) {
	if !busy || c.LastWrite.IsZero() {
		return false, 0
	}
	inProgress := false
	for _, t := range todos {
		if t.Status == "in_progress" {
			inProgress = true
			break
		}
	}
	if !inProgress {
		return false, 0
	}
	age := now.Sub(c.LastWrite)
	return age >= TodoStaleAfter && c.Responses >= TodoStaleResponses, age
}
