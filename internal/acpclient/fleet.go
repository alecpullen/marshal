package acpclient

import "marshal/web/bridge/contract"

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
}

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
}
