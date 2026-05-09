package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alecpullen/marshal/internal/gateway"
	ctxstore "github.com/alecpullen/marshal/internal/context"
	agttools "github.com/alecpullen/marshal/internal/agent/tools"
	"github.com/alecpullen/marshal/pkg/protocol"
)

// AgentStatus tracks the agent's execution state.
type AgentStatus int

const (
	AgentStatusIdle AgentStatus = iota
	AgentStatusRunning
	AgentStatusPaused
	AgentStatusCompleted
	AgentStatusFailed
	AgentStatusCancelled
)

// String returns the human-readable status.
func (s AgentStatus) String() string {
	switch s {
	case AgentStatusIdle:
		return "idle"
	case AgentStatusRunning:
		return "running"
	case AgentStatusPaused:
		return "paused"
	case AgentStatusCompleted:
		return "completed"
	case AgentStatusFailed:
		return "failed"
	case AgentStatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// TaskSpec defines a task for the agent to execute.
type TaskSpec struct {
	ID       string
	Goal     string
	Role     string
	Context  map[string]protocol.ContextRef
	Deadline time.Time
}

// Round represents one iteration of the agent's run loop.
type Round struct {
	Number       int
	StartTime    time.Time
	EndTime      *time.Time
	Messages     []gateway.Message
	ToolCalls    []ToolCall
	ToolResults  []ToolResult
	Output       string
	Complete     bool
	Usage        gateway.Usage
}

// ToolCall represents a tool invocation.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResult represents the outcome of a tool execution.
type ToolResult struct {
	ToolCallID string
	Content    string
	IsError    bool
	ContextRef *protocol.ContextRef
}

// SubAgentRequest represents a request to spawn a sub-agent.
type SubAgentRequest struct {
	ID       string
	Role     string
	Goal     string
	ParentID string
}

// SubAgentResult contains the result from a sub-agent.
type SubAgentResult struct {
	RequestID string
	Status    string
	Output    string
	Error     string
}

// Agent executes tasks according to a role manifest.
type Agent struct {
	// Identity
	ID       string
	Role     string
	ParentID string // Empty if top-level agent

	// Configuration
	Manifest *Manifest
	Task     *TaskSpec

	// Dependencies
	Gateway gateway.ProviderAdapter
	Store   *ctxstore.Store
	Tools   *agttools.Registry
	Events  EventPublisher

	// State
	ReadSet     *ReadSet
	History     []Round
	Status      AgentStatus
	SubAgents   map[string]*Agent // Spawned sub-agents

	// Runtime control
	Ctx       context.Context
	cancelFunc context.CancelFunc

	// Configuration
	RepoRoot string
}

// EventPublisher is the interface for publishing agent events.
type EventPublisher interface {
	AgentStarted(agentID, role, goal string)
	AgentCompleted(agentID, output string)
	AgentFailed(agentID, reason string)
	RoundStart(agentID string, round, maxRounds int)
	RoundEnd(agentID string, round int, usage gateway.Usage)
	Token(agentID, content string)
	ThinkBlock(agentID, content string)
	ToolCall(agentID, name string, args json.RawMessage)
	ToolResult(agentID, name, content string, isError bool)
	SubAgentSpawned(parentID, childID, role string)
	SubAgentCompleted(parentID, childID, output string)
}

// New creates an initialized Agent.
func New(
	id string,
	manifest *Manifest,
	task *TaskSpec,
	gateway gateway.ProviderAdapter,
	store *ctxstore.Store,
	tools *agttools.Registry,
	events EventPublisher,
) *Agent {
	return &Agent{
		ID:        id,
		Role:      manifest.Role,
		Manifest:  manifest,
		Task:      task,
		Gateway:   gateway,
		Store:     store,
		Tools:     tools,
		Events:    events,
		ReadSet:   NewReadSet(),
		History:   make([]Round, 0),
		Status:    AgentStatusIdle,
		SubAgents: make(map[string]*Agent),
	}
}

// GetStatus returns the current agent status.
func (a *Agent) GetStatus() AgentStatus {
	return a.Status
}

// IsRunning returns true if the agent is currently executing.
func (a *Agent) IsRunning() bool {
	return a.Status == AgentStatusRunning
}

// CanSpawnSubAgents returns true if this agent can spawn sub-agents.
func (a *Agent) CanSpawnSubAgents() bool {
	return a.Manifest.CanSpawnAgents && len(a.SubAgents) < a.Manifest.MaxConcurrentSubs
}

// SpawnSubAgent creates and returns a new sub-agent.
func (a *Agent) SpawnSubAgent(role, goal string) (*Agent, error) {
	if !a.CanSpawnSubAgents() {
		return nil, fmt.Errorf("cannot spawn more sub-agents (max: %d)", a.Manifest.MaxConcurrentSubs)
	}

	if !a.Manifest.CanSpawnRole(role) {
		return nil, fmt.Errorf("role %s is not in allowed sub-roles", role)
	}

	// Load sub-agent manifest
	manifest, err := LoadManifestForRole(role)
	if err != nil {
		return nil, fmt.Errorf("load manifest for %s: %w", role, err)
	}

	subID := fmt.Sprintf("%s-sub-%d", a.ID, len(a.SubAgents)+1)
	task := &TaskSpec{
		ID:      subID,
		Goal:    goal,
		Role:    role,
		Context: a.inheritContextForSub(),
	}

	subAgent := New(subID, manifest, task, a.Gateway, a.Store, a.Tools, a.Events)
	subAgent.ParentID = a.ID
	subAgent.RepoRoot = a.RepoRoot

	a.SubAgents[subID] = subAgent
	a.Events.SubAgentSpawned(a.ID, subID, role)

	return subAgent, nil
}

// inheritContextForSub builds inherited context for sub-agents.
func (a *Agent) inheritContextForSub() map[string]protocol.ContextRef {
	// Sub-agents inherit from parent's context
	inherited := make(map[string]protocol.ContextRef)

	// Add parent's read-set as context
	for _, path := range a.ReadSet.AllPaths() {
		if hash, ok := a.ReadSet.GetHash(path); ok {
			// Create a synthetic context ref for the parent's knowledge
			ref := protocol.NewContextRef(protocol.EntryKindFile, path, []byte(hash))
			inherited["parent:"+path] = ref
		}
	}

	return inherited
}

// Stop stops the agent execution.
func (a *Agent) Stop() {
	if a.cancelFunc != nil {
		a.cancelFunc()
	}
	a.Status = AgentStatusCancelled

	// Cancel all sub-agents
	for _, sub := range a.SubAgents {
		sub.Stop()
	}
}

// WaitForSubAgents waits for all sub-agents to complete.
func (a *Agent) WaitForSubAgents() error {
	for id, sub := range a.SubAgents {
		// In a real implementation, we'd wait for completion
		// For now, just check status
		if sub.IsRunning() {
			return fmt.Errorf("sub-agent %s still running", id)
		}
	}
	return nil
}

// GetSubAgentResult retrieves the result from a completed sub-agent.
func (a *Agent) GetSubAgentResult(subID string) (*SubAgentResult, error) {
	sub, ok := a.SubAgents[subID]
	if !ok {
		return nil, fmt.Errorf("sub-agent %s not found", subID)
	}

	if sub.IsRunning() {
		return nil, fmt.Errorf("sub-agent %s still running", subID)
	}

	// Get the last round's output
	var output string
	if len(sub.History) > 0 {
		lastRound := sub.History[len(sub.History)-1]
		output = lastRound.Output
	}

	return &SubAgentResult{
		RequestID: subID,
		Status:    sub.Status.String(),
		Output:    output,
	}, nil
}
