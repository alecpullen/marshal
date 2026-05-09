// Package protocol defines the core event protocol and shared types for the
// Marshal/Swarm system. These types are designed to be stable and may be used
// by third-party tools that integrate with the system.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// EventKind categorizes the type of a SwarmEvent.
type EventKind string

const (
	// Task lifecycle events
	EventTaskSpawned    EventKind = "task_spawned"
	EventTaskProgress   EventKind = "task_progress"
	EventTaskCompleted  EventKind = "task_completed"
	EventTaskFailed     EventKind = "task_failed"

	// Tool execution events
	EventToolCall       EventKind = "tool_call"
	EventToolResult     EventKind = "tool_result"

	// Model interaction events
	EventModelCall      EventKind = "model_call"
	EventModelChunk     EventKind = "model_chunk" // streaming delta

	// System events
	EventUserInterrupt  EventKind = "user_interrupt"
	EventGraphMutation  EventKind = "graph_mutation"

	// Knowledge tier events
	EventKnowledgeQuery EventKind = "knowledge_query"
	EventKBMutation     EventKind = "kb_mutation"

	// Edit events (for proposed-edits model)
	EventEditProposed   EventKind = "edit_proposed"
	EventEditApplied    EventKind = "edit_applied"
	EventEditRejected   EventKind = "edit_rejected"

	// Configuration events
	EventProfileChanged EventKind = "profile_changed"
	EventCostUpdate     EventKind = "cost_update"
)

// String returns the string representation of the EventKind.
func (k EventKind) String() string {
	return string(k)
}

// IsValid reports whether the EventKind is a known value.
func (k EventKind) IsValid() bool {
	switch k {
	case EventTaskSpawned, EventTaskProgress, EventTaskCompleted, EventTaskFailed,
		EventToolCall, EventToolResult,
		EventModelCall, EventModelChunk,
		EventUserInterrupt, EventGraphMutation,
		EventKnowledgeQuery, EventKBMutation,
		EventEditProposed, EventEditApplied, EventEditRejected,
		EventProfileChanged, EventCostUpdate:
		return true
	}
	return false
}

// SwarmEvent is the primary event structure used throughout the system.
// It uses a content-addressed payload design to maintain backward compatibility
// as new event kinds are added.
type SwarmEvent struct {
	// ID is a unique identifier for this event (ULID or UUID).
	ID string `json:"id"`

	// Kind categorizes the event type.
	Kind EventKind `json:"kind"`

	// SessionID identifies the session this event belongs to.
	SessionID string `json:"session_id"`

	// AgentID identifies the agent that produced this event, if applicable.
	AgentID string `json:"agent_id,omitempty"`

	// TaskID identifies the task this event relates to, if applicable.
	TaskID string `json:"task_id,omitempty"`

	// ParentID references a parent event in causal chains (e.g., tool_result -> tool_call).
	ParentID string `json:"parent_id,omitempty"`

	// Timestamp when the event was created.
	Timestamp time.Time `json:"timestamp"`

	// Payload contains event-specific data as json.RawMessage.
	// Using RawMessage keeps the core type stable while allowing
	// payload evolution without breaking persistence.
	Payload json.RawMessage `json:"payload"`
}

// NewEvent creates a new SwarmEvent with the given kind and session ID.
// The ID is generated automatically and timestamp is set to now.
func NewEvent(kind EventKind, sessionID string) SwarmEvent {
	return SwarmEvent{
		ID:        GenerateID(),
		Kind:      kind,
		SessionID: sessionID,
		Timestamp: time.Now().UTC(),
	}
}

// WithPayload attaches a typed payload to the event by marshaling it to JSON.
// Returns the modified event for chaining.
func (e SwarmEvent) WithPayload(payload interface{}) (SwarmEvent, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return e, err
	}
	e.Payload = data
	return e, nil
}

// UnmarshalPayload unmarshals the payload into the provided target type.
func (e SwarmEvent) UnmarshalPayload(target interface{}) error {
	if len(e.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(e.Payload, target)
}

// GenerateID creates a new unique identifier for events.
// Format: timestamp + 8-char random hex suffix (e.g., 20260115T143022.123-abc123de)
// This provides lexicographic sortability with sufficient uniqueness.
func GenerateID() string {
	now := time.Now().UTC()
	return now.Format("20060102T150405.000") + "-" + randomHex(8)
}

// randomHex generates n bytes of random data and returns as hex string.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based randomness if crypto/rand fails
		return fmt.Sprintf("%016x", time.Now().UnixNano())[:n*2]
	}
	return hex.EncodeToString(b)
}

// --- Payload types for specific events ---

// TaskSpawnedPayload is the payload for EventTaskSpawned.
type TaskSpawnedPayload struct {
	TaskID      string          `json:"task_id"`
	Role        string          `json:"role"`
	Goal        string          `json:"goal"`
	ParentIDs   []string        `json:"parent_ids,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// TaskProgressPayload is the payload for EventTaskProgress.
type TaskProgressPayload struct {
	TaskID    string  `json:"task_id"`
	Status    string  `json:"status"` // "running", "waiting", "blocked"
	Message   string  `json:"message,omitempty"`
	Progress  float64 `json:"progress,omitempty"` // 0.0 to 1.0
}

// TaskCompletedPayload is the payload for EventTaskCompleted.
type TaskCompletedPayload struct {
	TaskID   string          `json:"task_id"`
	Result   json.RawMessage `json:"result,omitempty"`
	Duration int64           `json:"duration_ms"`
	Cost     CostInfo        `json:"cost"`
}

// TaskFailedPayload is the payload for EventTaskFailed.
type TaskFailedPayload struct {
	TaskID    string `json:"task_id"`
	Error     string `json:"error"`
	ErrorCode string `json:"error_code,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// ToolCallPayload is the payload for EventToolCall.
type ToolCallPayload struct {
	CallID    string          `json:"call_id"`
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResultPayload is the payload for EventToolResult.
type ToolResultPayload struct {
	CallID   string          `json:"call_id"`
	Status   string          `json:"status"` // "success", "error", "pending"
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
	NewRefs  []ContextRef    `json:"new_refs,omitempty"` // ContextRefs produced by tool
}

// ModelCallPayload is the payload for EventModelCall.
type ModelCallPayload struct {
	CallID      string   `json:"call_id"`
	Model       string   `json:"model"`
	Provider    string   `json:"provider"`
	MessageCount int     `json:"message_count"`
}

// ModelChunkPayload is the payload for EventModelChunk (streaming).
type ModelChunkPayload struct {
	CallID  string `json:"call_id"`
	Content string `json:"content,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

// UserInterruptPayload is the payload for EventUserInterrupt.
type UserInterruptPayload struct {
	Reason string `json:"reason"` // "cancel", "pause", "modify"
	Input  string `json:"input,omitempty"` // User's new input
}

// GraphMutationPayload is the payload for EventGraphMutation.
type GraphMutationPayload struct {
	GraphVersion int             `json:"graph_version"`
	MutationType string          `json:"mutation_type"` // "add", "remove", "update", "reorder"
	TasksAdded   []string        `json:"tasks_added,omitempty"`
	TasksRemoved []string        `json:"tasks_removed,omitempty"`
	EdgesChanged []EdgeChange    `json:"edges_changed,omitempty"`
}

// EdgeChange describes a change to a graph edge.
type EdgeChange struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Action string `json:"action"` // "add", "remove"
}

// KnowledgeQueryPayload is the payload for EventKnowledgeQuery.
type KnowledgeQueryPayload struct {
	Query      string       `json:"query"`
	Mode       string       `json:"mode"` // "search", "fetch", "llm"
	Results    []ContextRef `json:"results,omitempty"`
	Answer     string       `json:"answer,omitempty"`
	Confidence string       `json:"confidence,omitempty"`
}

// KBMutationPayload is the payload for EventKBMutation.
type KBMutationPayload struct {
	EntryType string `json:"entry_type"` // "symbol", "summary", "convention"
	Action    string `json:"action"`     // "add", "update", "delete"
	Key       string `json:"key"`
	ContentHash string `json:"content_hash,omitempty"`
}

// EditProposedPayload is the payload for EventEditProposed.
type EditProposedPayload struct {
	ProposalID   string   `json:"proposal_id"`
	Path         string   `json:"path"`
	ExpectedHash string   `json:"expected_hash"`
	Operations   []string `json:"operations"` // e.g., ["replace_lines_10_20"]
	Rationale    string   `json:"rationale,omitempty"`
}

// EditAppliedPayload is the payload for EventEditApplied.
type EditAppliedPayload struct {
	ProposalID string `json:"proposal_id"`
	NewHash    string `json:"new_hash"`
}

// EditRejectedPayload is the payload for EventEditRejected.
type EditRejectedPayload struct {
	ProposalID string `json:"proposal_id"`
	Reason     string `json:"reason"`
	Hint       string `json:"hint,omitempty"` // Recovery guidance
}

// ProfileChangedPayload is the payload for EventProfileChanged.
type ProfileChangedPayload struct {
	OldProfile   string            `json:"old_profile,omitempty"`
	NewProfile   string            `json:"new_profile"`
	BindingDelta map[string]string `json:"binding_delta,omitempty"` // role -> binding changes
}

// CostUpdatePayload is the payload for EventCostUpdate.
type CostUpdatePayload struct {
	SessionCost float64 `json:"session_cost"`
	TaskCost    float64 `json:"task_cost,omitempty"`
	DailyBudget float64 `json:"daily_budget"`
	DailySpent  float64 `json:"daily_spent"`
	KBCost      float64 `json:"kb_cost,omitempty"` // Knowledge base maintenance cost
}

// CostInfo captures cost information for a single operation.
type CostInfo struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	EstimatedUSD float64 `json:"estimated_usd,omitempty"`
}

// --- Marshal compatibility types ---

// MarshalRoundPayload maps Marshal's internal Round to a SwarmEvent.
// Used during the transition period to bridge old and new event systems.
type MarshalRoundPayload struct {
	RoundID          int             `json:"round_id"`
	TaskID           string          `json:"task_id"`
	Role             string          `json:"role"`
	Model            string          `json:"model"`
	PromptTokens     int             `json:"prompt_tokens"`
	CompletionTokens int             `json:"completion_tokens"`
	Verdict          string          `json:"verdict,omitempty"` // "PASS", "FAIL", ""
	ContentPreview   string          `json:"content_preview,omitempty"` // Truncated content
}

// MarshalTaskPayload maps Marshal's Task to a SwarmEvent.
type MarshalTaskPayload struct {
	TaskID    string `json:"task_id"`
	Prompt    string `json:"prompt"`
	Status    string `json:"status"` // "running", "passed", "failed"
	RoundCount int   `json:"round_count,omitempty"`
}
