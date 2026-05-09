package pipeline

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/alecpullen/marshal/pkg/protocol"
)

// Extended TaskState values for swarm (extending existing constants).
const (
	// TaskWaiting means the task is waiting for dependencies (new for swarm).
	TaskWaiting TaskState = iota + 4 // Continue after TaskFailed (3)
	// TaskBlocked means the task is blocked (e.g., by failure of dependency).
	TaskBlocked
	// TaskCancelled means the task was cancelled by user.
	TaskCancelled
	// TaskSuperseded means the task was replaced by a newer version.
	TaskSuperseded
)

// String returns the string representation of TaskState (extended for new states).
func TaskStateString(s TaskState) string {
	switch s {
	case TaskPending:
		return "pending"
	case TaskRunning:
		return "running"
	case TaskPassed:
		return "passed"
	case TaskFailed:
		return "failed"
	case TaskWaiting:
		return "waiting"
	case TaskBlocked:
		return "blocked"
	case TaskCancelled:
		return "cancelled"
	case TaskSuperseded:
		return "superseded"
	}
	return fmt.Sprintf("unknown(%d)", s)
}

// IsTerminal returns true if the state represents a completed state.
func (s TaskState) IsTerminal() bool {
	return s == TaskPassed || s == TaskFailed || s == TaskCancelled || s == TaskSuperseded
}

// CanTransitionTo checks if a transition from the current state to target is valid.
func (s TaskState) CanTransitionTo(target TaskState) bool {
	// Terminal states cannot transition
	if s.IsTerminal() {
		return false
	}

	// Valid transitions:
	// pending -> running, waiting, blocked, cancelled
	// running -> passed, failed, cancelled
	// waiting -> running, blocked, cancelled
	// blocked -> running, failed, cancelled

	switch s {
	case TaskPending:
		return target == TaskRunning || target == TaskWaiting || target == TaskBlocked || target == TaskCancelled
	case TaskRunning:
		return target == TaskPassed || target == TaskFailed || target == TaskCancelled
	case TaskWaiting:
		return target == TaskRunning || target == TaskBlocked || target == TaskCancelled
	case TaskBlocked:
		return target == TaskRunning || target == TaskFailed || target == TaskCancelled
	}

	return false
}

// ContextPolicy defines how an agent should handle context inheritance.
type ContextPolicy struct {
	// Inherit lists context keys to inherit from parent tasks.
	Inherit []string `json:"inherit,omitempty"`

	// Exclude lists context kinds to exclude from context assembly.
	Exclude []protocol.EntryKind `json:"exclude,omitempty"`

	// IncludeExplicit lists additional context refs to include.
	IncludeExplicit []protocol.ContextRef `json:"include_explicit,omitempty"`

	// MaxTokens limits the total context size.
	MaxTokens int `json:"max_tokens,omitempty"`

	// SummarizeIfOver triggers summarization if context exceeds this threshold.
	SummarizeIfOver int `json:"summarize_if_over,omitempty"`
}

// TaskSpec is the full specification for a task.
// This extends PipelineTask with swarm-specific fields.
type TaskSpec struct {
	// ID is the unique task identifier.
	ID string `json:"id"`

	// Role is the agent role responsible for this task (e.g., "codegen", "research").
	Role string `json:"role"`

	// Goal is the natural language description of what the task should accomplish.
	Goal string `json:"goal"`

	// Description is a human-readable summary.
	Description string `json:"description,omitempty"`

	// DependsOn lists task IDs that must complete before this task can start.
	DependsOn []string `json:"depends_on,omitempty"`

	// Files lists files this task is expected to read or modify.
	Files []string `json:"files,omitempty"`

	// OutputSchema defines the expected output structure (JSON schema).
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`

	// ContextPolicy controls how context is assembled.
	ContextPolicy ContextPolicy `json:"context_policy,omitempty"`

	// MaxIterations limits the number of tool-call rounds.
	MaxIterations int `json:"max_iterations,omitempty"`

	// Timeout for task execution.
	Timeout time.Duration `json:"timeout,omitempty"`

	// CreatedAt timestamp.
	CreatedAt time.Time `json:"created_at"`

	// Version tracks spec revisions (for replanning).
	Version int `json:"version"`

	// ParentID for tracking task lineage (replanning).
	ParentID string `json:"parent_id,omitempty"`

	// Output captured from task execution.
	Output json.RawMessage `json:"output,omitempty"`

	// Status is the current task state (uses TaskState).
	Status TaskState `json:"status"`

	// StartedAt when execution began.
	StartedAt *time.Time `json:"started_at,omitempty"`

	// CompletedAt when execution finished.
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// Error message if task failed.
	Error string `json:"error,omitempty"`
}

// NewTaskSpec creates a new task spec with defaults.
func NewTaskSpec(id, role, goal string) *TaskSpec {
	return &TaskSpec{
		ID:            id,
		Role:          role,
		Goal:          goal,
		DependsOn:     []string{},
		Files:         []string{},
		MaxIterations: 3,
		Timeout:       5 * time.Minute,
		CreatedAt:     time.Now().UTC(),
		Version:       1,
		Status:        TaskPending,
	}
}

// Validate checks if the task spec is valid.
func (s *TaskSpec) Validate() error {
	if s.ID == "" {
		return ErrTaskValidation("task ID is required")
	}
	if s.Role == "" {
		return ErrTaskValidation("task role is required")
	}
	if s.Goal == "" {
		return ErrTaskValidation("task goal is required")
	}

	// Check for circular dependencies (simplified - just check self-dependency)
	for _, dep := range s.DependsOn {
		if dep == s.ID {
			return ErrTaskValidation("task cannot depend on itself")
		}
	}

	return nil
}

// IsReady returns true if the task is ready to execute based on dependencies.
func (s *TaskSpec) IsReady(completed map[string]bool) bool {
	if s.Status != TaskPending {
		return false
	}
	for _, dep := range s.DependsOn {
		if !completed[dep] {
			return false
		}
	}
	return true
}

// ToPipelineTask converts a TaskSpec to a PipelineTask for the existing scheduler.
func (s *TaskSpec) ToPipelineTask() *PipelineTask {
	return &PipelineTask{
		ID:          s.ID,
		Description: s.Description,
		Files:       s.Files,
		DependsOn:   s.DependsOn,
		State:       s.Status,
		MaxRounds:   s.MaxIterations,
	}
}

// FromPipelineTask creates a TaskSpec from a PipelineTask.
func FromPipelineTask(pt *PipelineTask) *TaskSpec {
	return &TaskSpec{
		ID:            pt.ID,
		Description:   pt.Description,
		Files:         pt.Files,
		DependsOn:     pt.DependsOn,
		Status:        pt.State,
		MaxIterations: pt.MaxRounds,
		CreatedAt:     time.Now().UTC(),
		Version:       1,
	}
}

// --- Graph mutation types for replanning ---

// MutationType categorizes graph mutations.
type MutationType string

const (
	MutationAdd     MutationType = "add"
	MutationRemove  MutationType = "remove"
	MutationUpdate  MutationType = "update"
	MutationReorder MutationType = "reorder"
	MutationReplace MutationType = "replace"
)

// GraphMutation represents a change to the task graph.
type GraphMutation struct {
	// Type of mutation.
	Type MutationType `json:"type"`

	// Target task ID (for remove, update, replace).
	TargetID string `json:"target_id,omitempty"`

	// New task spec (for add, update, replace).
	NewSpec *TaskSpec `json:"new_spec,omitempty"`

	// New edges to add (for add, reorder).
	NewEdges [][2]string `json:"new_edges,omitempty"`

	// Edges to remove (for remove, reorder).
	RemoveEdges [][2]string `json:"remove_edges,omitempty"`

	// Reason for the mutation (for audit trail).
	Reason string `json:"reason,omitempty"`

	// Trigger describes what caused the mutation.
	Trigger string `json:"trigger,omitempty"`

	// Timestamp of the mutation.
	Timestamp time.Time `json:"timestamp"`
}

// NewGraphMutation creates a new graph mutation with current timestamp.
func NewGraphMutation(mutationType MutationType) *GraphMutation {
	return &GraphMutation{
		Type:      mutationType,
		Timestamp: time.Now().UTC(),
	}
}

// --- Validation error ---

// ErrTaskValidation is returned when a task spec fails validation.
type ErrTaskValidation string

func (e ErrTaskValidation) Error() string {
	return "task validation failed: " + string(e)
}
