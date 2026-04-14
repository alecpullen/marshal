// internal/conversation/conversation.go
// Core types for agent-centric conversation model.
// A conversation is a chat between user and Marshal that may spawn tasks.

package conversation

import (
	"fmt"
	"time"

	"github.com/alecpullen/marshal/internal/agents/planner"
)

// Mode represents the TUI display mode for conversation
type Mode int

const (
	ModeChat        Mode = iota // Normal chat display
	ModeClarifying              // Highlighted clarifying mode
	ModePlanning                // Showing task plan
	ModeExecuting               // Tasks running
	ModeSummarizing             // Showing results
)

// State represents the conversation's internal state
type State int

const (
	StateChatting    State = iota // Free-form conversation
	StateClarifying               // Marshal asked a question, waiting for answer
	StateExploring                // Autonomously exploring codebase for context
	StatePlanning                 // Analyzing request, may create task plan
	StateExecuting                // Tasks are running
	StateSummarizing              // Tasks complete, summarizing results
)

func (s State) String() string {
	switch s {
	case StateChatting:
		return "chatting"
	case StateClarifying:
		return "clarifying"
	case StateExploring:
		return "exploring"
	case StatePlanning:
		return "planning"
	case StateExecuting:
		return "executing"
	case StateSummarizing:
		return "summarizing"
	default:
		return "unknown"
	}
}

// Intent represents what the user is trying to do
type Intent int

const (
	IntentChat           Intent = iota // Casual conversation
	IntentRequestWork                  // User wants something done
	IntentProvideContext               // Answering Marshal's question
	IntentConfirm                      // Confirming a plan/decision
	IntentDecline                      // Declining a plan
	IntentCancel                       // Cancelling current work
)

func (i Intent) String() string {
	switch i {
	case IntentChat:
		return "chat"
	case IntentRequestWork:
		return "request_work"
	case IntentProvideContext:
		return "provide_context"
	case IntentConfirm:
		return "confirm"
	case IntentDecline:
		return "decline"
	case IntentCancel:
		return "cancel"
	default:
		return "unknown"
	}
}

// Message represents a single message in the conversation
type Message struct {
	ID             int64
	ConversationID string
	Role           string // "user", "marshal", "system", "executor", "critic"
	Content        string
	Intent         string // Classified intent (for user messages)
	TaskID         string // If this message spawned/related to a task
	Metadata       map[string]string
	CreatedAt      time.Time
}

// Conversation holds the full state of a user-Marshal chat
type Conversation struct {
	ID            string
	Status        string // "active", "paused", "completed"
	State         State
	Messages      []Message
	Context       *AccumulatedContext
	PendingTasks  []TaskPlan // Tasks Marshal wants to run (waiting for confirmation if complex)
	ActiveTaskIDs []string   // Currently running task IDs
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ExplorationSummary holds results from autonomous codebase exploration.
// This is stored in AccumulatedContext to share findings with planning.
type ExplorationSummary struct {
	Summary      string   // High-level summary of findings
	FilesFound   []string // Relevant files discovered
	Architecture string   // Architecture overview
	Confidence   float64  // Confidence in findings (0-1)
	StepsTaken   int      // Number of exploration steps performed
}

// AccumulatedContext is Marshal's understanding of the conversation
type AccumulatedContext struct {
	UserGoal          string              // What the user wants to achieve
	CurrentTopic      string              // What we're discussing
	FilesMentioned    []string            // Files referenced in conversation
	OpenQuestions     []string            // Questions Marshal has asked
	KnownConstraints  map[string]string   // Constraints discovered
	Confidence        float64             // How confident Marshal is about goal (0-1)
	ExplorationResult *ExplorationSummary // Results from autonomous exploration
}

// TaskPlan represents a task the Marshal wants to spawn
type TaskPlan struct {
	ID                  string
	Description         string
	Dependencies        []string // Task IDs this depends on
	FilesLikelyAffected []string
	Complexity          string // "low", "medium", "high"
	AutoExecute         bool   // Can auto-execute (clear + low risk)
}

// Response is Marshal's response to a user message
type Response struct {
	Type         ResponseType
	Content      string             // Text to show user
	Questions    []string           // If asking clarifying questions
	TaskPlans    []TaskPlan         // Tasks Marshal proposes
	PlanGraph    *planner.TaskGraph // If planner was used
	UpdatedState State              // New conversation state
	Intent       Intent             // Classified intent (for logging/debug)
}

// ResponseType categorizes Marshal's response
type ResponseType int

const (
	ResponseChat          ResponseType = iota // Casual conversation
	ResponseClarification                     // Asking clarifying questions
	ResponseTaskPlan                          // Proposing tasks to run
	ResponseTaskProgress                      // Update on running tasks
	ResponseTaskComplete                      // All tasks finished with summary
)

func (r ResponseType) String() string {
	switch r {
	case ResponseChat:
		return "chat"
	case ResponseClarification:
		return "clarification"
	case ResponseTaskPlan:
		return "task_plan"
	case ResponseTaskProgress:
		return "task_progress"
	case ResponseTaskComplete:
		return "task_complete"
	default:
		return "unknown"
	}
}

// Mode returns the TUI display mode for this response type
func (r ResponseType) Mode() Mode {
	switch r {
	case ResponseClarification:
		return ModeClarifying
	case ResponseTaskPlan, ResponseTaskProgress:
		return ModePlanning
	case ResponseTaskComplete:
		return ModeSummarizing
	default:
		return ModeChat
	}
}

// New creates a new conversation with the given ID
func New(id string) *Conversation {
	now := time.Now()
	return &Conversation{
		ID:       id,
		Status:   "active",
		State:    StateChatting,
		Messages: []Message{},
		Context: &AccumulatedContext{
			KnownConstraints: make(map[string]string),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// AddMessage appends a message to the conversation
func (c *Conversation) AddMessage(role, content string, intent ...Intent) Message {
	msg := Message{
		ConversationID: c.ID,
		Role:           role,
		Content:        content,
		CreatedAt:      time.Now(),
		Metadata:       make(map[string]string),
	}
	if len(intent) > 0 && role == "user" {
		msg.Intent = intent[0].String()
	}
	c.Messages = append(c.Messages, msg)
	c.UpdatedAt = time.Now()
	return msg
}

// LastMessage returns the most recent message
func (c *Conversation) LastMessage() *Message {
	if len(c.Messages) == 0 {
		return nil
	}
	return &c.Messages[len(c.Messages)-1]
}

// IsClarifying returns true if Marshal is waiting for an answer
func (c *Conversation) IsClarifying() bool {
	return c.State == StateClarifying && len(c.Context.OpenQuestions) > 0
}

// CanAutoExecute returns true if pending tasks can run without confirmation
func (c *Conversation) CanAutoExecute() bool {
	if len(c.PendingTasks) == 0 {
		return false
	}
	for _, task := range c.PendingTasks {
		if !task.AutoExecute {
			return false
		}
	}
	return true
}

// Format for display
type DisplayMessage struct {
	Role      string
	Content   string
	Timestamp time.Time
	IsTask    bool
	TaskID    string
}

// ToDisplayMessages converts Messages to display format
func (c *Conversation) ToDisplayMessages() []DisplayMessage {
	var result []DisplayMessage
	for _, m := range c.Messages {
		dm := DisplayMessage{
			Role:      m.Role,
			Content:   m.Content,
			Timestamp: m.CreatedAt,
			IsTask:    m.TaskID != "",
			TaskID:    m.TaskID,
		}
		result = append(result, dm)
	}
	return result
}

// GenerateID creates a unique conversation ID
func GenerateID() string {
	return fmt.Sprintf("conv-%d", time.Now().UnixNano())
}

// IsComplexRequest heuristically determines if a request is complex
// This is used for auto-execute decisions
func IsComplexRequest(msg string, context *AccumulatedContext) bool {
	// Complex indicators
	complexKeywords := []string{
		"refactor", "architecture", "redesign", "rewrite",
		"multiple", "several", "many", "all files",
		"database", "migration", "schema change",
		"auth", "security", "permission",
	}

	msgLower := toLower(msg)
	complexityScore := 0

	for _, keyword := range complexKeywords {
		if contains(msgLower, keyword) {
			complexityScore++
		}
	}

	// Multiple files mentioned
	if len(context.FilesMentioned) > 3 {
		complexityScore++
	}

	// Unclear what user wants
	if context.Confidence < 0.5 {
		complexityScore++
	}

	// High complexity score = complex request
	return complexityScore >= 2
}

// Helper functions (minimal implementations)
func toLower(s string) string {
	result := make([]rune, len([]rune(s)))
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			result[i] = r + ('a' - 'A')
		} else {
			result[i] = r
		}
	}
	return string(result)
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
