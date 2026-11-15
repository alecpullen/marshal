package acpclient

import (
	"context"
	"errors"
	"fmt"

	"marshal/web/bridge/contract"
)

// FleetConn is the fleet stream's connection state (P5 F3).
type FleetConn int

const (
	FleetConnecting   FleetConn = iota // before the first connect
	FleetConnected                     // the stream is up
	FleetReconnecting                  // retrying, for under a minute
	FleetDown                          // retries have failed for a minute; still retrying every 30s
	FleetRejected                      // a 401: the stream and every retry have stopped
)

// FleetState is the fleet as the client last read it. Every slice and map is
// the caller's own copy.
type FleetState struct {
	Server   string // the bridge's name on screen: "local" for the daemon
	Conn     FleetConn
	ConnErr  string                 // why the stream is not connected; "" when it is
	Loaded   bool                   // a full read has landed
	Epoch    uint64                 // bumped by every full read: a connect, a reconnect, an overflow
	Agents   []contract.AgentStatus // GET /api/agents' order, kept current by deltas
	Inbox    contract.Inbox
	Projects []contract.ProjectStatus
	Budgets  *contract.BudgetReport         // nil until read, or after a failed read
	Gates    map[string]contract.GateRecord // the latest gate per agent ID, from gate deltas
	Daemon   *contract.DaemonInfo           // the local daemon's info; nil for a server
}

// Fleet is the fleet the TUI renders (P5 decision 3). Reads never block.
type Fleet interface {
	// State is the fleet as the client last read it.
	//
	// Routes: GET /api/agents, GET /api/inbox, GET /api/projects,
	// GET /api/budgets, GET /api/daemon, GET /api/events?stream=fleet.
	State() FleetState

	// Changed fires after anything State returns may have changed. It is a
	// coalescing signal with a buffer of 1. It is closed when the client
	// closes, so a reader learns there is nothing more to wait for.
	//
	// Routes: none.
	Changed() <-chan struct{}

	// Spawn starts an agent.
	//
	// Routes: POST /api/agents.
	Spawn(ctx context.Context, req contract.SpawnRequest) (contract.SpawnResult, error)

	// AnswerPermission answers an agent's approval. One settled elsewhere
	// answers ErrSettled.
	//
	// Routes: POST /api/permissions/{id}.
	AnswerPermission(ctx context.Context, id string, d contract.Decision) error

	// AnswerQuestion answers an agent's question, as AnswerPermission does.
	//
	// Routes: POST /api/questions/{id}.
	AnswerQuestion(ctx context.Context, id string, a contract.Answers) error

	// Cancel stops an agent's turn.
	//
	// Routes: POST /api/sessions/{id}/cancel.
	Cancel(ctx context.Context, agentID string) error

	// ApproveIntake spawns a submission's agent.
	//
	// Routes: POST /api/pending/{id}/approve.
	ApproveIntake(ctx context.Context, id string) (contract.ApproveResult, error)

	// DenyIntake discards a submission without spawning.
	//
	// Routes: POST /api/pending/{id}/deny.
	DenyIntake(ctx context.Context, id string) error

	// RecentPrompts is a project's last prompts, newest first.
	//
	// Routes: GET /api/prompts/recent.
	RecentPrompts(ctx context.Context, project string, limit int) ([]string, error)
}

// FleetError is a refused action (decision 2).
type FleetError struct {
	Status  int    // the HTTP status; 0 when the bridge was not reached
	Message string // the bridge's sentence, or the budget's
}

// Error is the bridge's sentence, or the status alone when it sent none.
func (e *FleetError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("status %d", e.Status)
}

// ErrSettled is an answer to a request settled elsewhere (a 410).
var ErrSettled = errors.New("already answered")

// FleetRoute is one row of the fleet's route table.
type FleetRoute struct {
	Name   string   // the Fleet method
	Routes []string // "METHOD /path[?query]" as the doc's Routes: line lists them; empty for none
}

// FleetRoutes maps every Fleet method to the bridge routes that serve it.
var FleetRoutes = []FleetRoute{
	{
		Name: "State",
		Routes: []string{
			"GET /api/agents",
			"GET /api/inbox",
			"GET /api/projects",
			"GET /api/budgets",
			"GET /api/daemon",
			"GET /api/events?stream=fleet",
		},
	},
	{Name: "Changed"},
	{Name: "Spawn", Routes: []string{"POST /api/agents"}},
	{Name: "AnswerPermission", Routes: []string{"POST /api/permissions/{id}"}},
	{Name: "AnswerQuestion", Routes: []string{"POST /api/questions/{id}"}},
	{Name: "Cancel", Routes: []string{"POST /api/sessions/{id}/cancel"}},
	{Name: "ApproveIntake", Routes: []string{"POST /api/pending/{id}/approve"}},
	{Name: "DenyIntake", Routes: []string{"POST /api/pending/{id}/deny"}},
	{Name: "RecentPrompts", Routes: []string{"GET /api/prompts/recent"}},
}
