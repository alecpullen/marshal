package agent

import (
	"strings"
	"time"
)

type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusPlanning  TaskStatus = "planning"
	TaskStatusExecuting TaskStatus = "executing"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
)

type TaskClass string

const (
	ClassQuestion TaskClass = "question"
	ClassEdit     TaskClass = "edit"
	ClassCommand  TaskClass = "command"
)

// Task represents one user-goal turn driven by Runner.Run. Runner mutates
// it as the loop progresses so callers can inspect what the agent decided
// without re-parsing the message transcript.
type Task struct {
	Goal      string
	Class     TaskClass
	Status    TaskStatus
	Plan      []string
	Summary   string
	StartedAt time.Time
}

func NewTask(goal string, startedAt time.Time) *Task {
	return &Task{
		Goal:      goal,
		Status:    TaskStatusPending,
		StartedAt: startedAt,
	}
}

// splitPlanLines turns the model's free-text planning response into
// individual non-blank, trimmed lines for storage on Task.Plan.
func splitPlanLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
