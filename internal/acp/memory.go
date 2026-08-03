package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
)

// MemoryRuntime is the per-session slice of state MemoryManager needs.
type MemoryRuntime struct {
	DB        *db.DB
	ProjectID int64
	State     *session.State
}

// MemoryLookup returns the runtime registered for an ACP session id.
type MemoryLookup func(sessionID string) (*MemoryRuntime, bool)

// MemoryManagerConfig wires a MemoryManager to external dependencies.
type MemoryManagerConfig struct {
	Lookup MemoryLookup
}

// MemoryManager dispatches session/memory_list, session/memory_delete,
// session/memory_set_confidence, and session/agents_roster. Distinct from
// TurnManager: none of these are turns — no active-turn slot, no
// cancellation, no event forwarding.
type MemoryManager struct {
	lookup MemoryLookup
}

func NewMemoryManager(cfg MemoryManagerConfig) *MemoryManager {
	if cfg.Lookup == nil {
		panic("acp: MemoryManagerConfig.Lookup is required")
	}
	return &MemoryManager{lookup: cfg.Lookup}
}

// MemoryEntry mirrors db.Memory for JSON transport.
type MemoryEntry struct {
	ID              int64     `json:"id"`
	Kind            string    `json:"kind"`
	Content         string    `json:"content"`
	Confidence      string    `json:"confidence"`
	SourceSessionID string    `json:"sourceSessionId,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// MemoryListResult is the JSON-RPC result for session/memory_list.
type MemoryListResult struct {
	Entries []MemoryEntry `json:"entries"`
}

// MemoryList handles session/memory_list.
func (m *MemoryManager) MemoryList(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/memory_list params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/memory_list requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.DB == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no database handle"}
	}
	records, err := rt.DB.GetMemories(rt.ProjectID)
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("list memories: %v", err)}
	}
	entries := make([]MemoryEntry, len(records))
	for i, r := range records {
		entries[i] = MemoryEntry{
			ID: r.ID, Kind: r.Kind, Content: r.Content, Confidence: r.Confidence,
			SourceSessionID: r.SourceSessionID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
	}
	return MemoryListResult{Entries: entries}, nil
}

// MemoryDeleteParams is the JSON-RPC body for session/memory_delete.
type MemoryDeleteParams struct {
	SessionID string `json:"sessionId"`
	ID        int64  `json:"id"`
}

// MemoryDelete handles session/memory_delete. No confirmation step — the
// explicit RPC call is itself the confirmation.
func (m *MemoryManager) MemoryDelete(ctx context.Context, params json.RawMessage) (any, error) {
	var p MemoryDeleteParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/memory_delete params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/memory_delete requires sessionId")
	}
	if p.ID == 0 {
		return nil, invalidParamsError("session/memory_delete requires a non-zero id")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.DB == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no database handle"}
	}
	if err := rt.DB.DeleteMemory(p.ID); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("delete memory: %v", err)}
	}
	return map[string]any{}, nil
}

var validMemoryConfidence = map[string]bool{"tentative": true, "confirmed": true, "stale": true}

// MemorySetConfidenceParams is the JSON-RPC body for
// session/memory_set_confidence.
type MemorySetConfidenceParams struct {
	SessionID  string `json:"sessionId"`
	ID         int64  `json:"id"`
	Confidence string `json:"confidence"`
}

// MemorySetConfidence handles session/memory_set_confidence. Goes beyond
// what the TUI's memory panel exposes (db.SetMemoryConfidence exists but
// no TUI path calls it) — an intentional, explicitly-scoped addition.
func (m *MemoryManager) MemorySetConfidence(ctx context.Context, params json.RawMessage) (any, error) {
	var p MemorySetConfidenceParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/memory_set_confidence params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/memory_set_confidence requires sessionId")
	}
	if p.ID == 0 {
		return nil, invalidParamsError("session/memory_set_confidence requires a non-zero id")
	}
	if !validMemoryConfidence[p.Confidence] {
		return nil, invalidParamsError("invalid confidence %q: want one of tentative, confirmed, stale", p.Confidence)
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.DB == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no database handle"}
	}
	if err := rt.DB.SetMemoryConfidence(p.ID, p.Confidence, time.Now()); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("set memory confidence: %v", err)}
	}
	return map[string]any{}, nil
}

// RosterRole is one role's resolved binding in the session/agents_roster
// result.
type RosterRole struct {
	Role        string `json:"role"`
	Profile     string `json:"profile"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	PresetName  string `json:"presetName,omitempty"`
	CustomAgent string `json:"customAgent,omitempty"`
	LocalOnly   bool   `json:"localOnly"`
	Error       string `json:"error,omitempty"`
}

// RosterBudget is one subsystem's token/round budget.
type RosterBudget struct {
	MaxFixRounds   int `json:"maxFixRounds"`
	MaxTotalTokens int `json:"maxTotalTokens"`
}

// AgentsRosterResult is the JSON-RPC result for session/agents_roster.
type AgentsRosterResult struct {
	Roles       []RosterRole `json:"roles"`
	SwarmBudget RosterBudget `json:"swarmBudget"`
	SDDBudget   RosterBudget `json:"sddBudget"`
}

// AgentsRoster handles session/agents_roster: a read-only resolved view
// of every role's live binding, plus swarm/SDD budgets. There is no
// write counterpart — roster "editing" is general settings mutation
// under the hood (same config.Config, same config.SaveProjectConfig
// persistence /settings uses), out of this sub-project's scope.
func (m *MemoryManager) AgentsRoster(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/agents_roster params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/agents_roster requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.State == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no state"}
	}
	cfg := rt.State.Config
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	cast := router.Cast(routing.AllRoles)
	roles := make([]RosterRole, len(cast))
	for i, ce := range cast {
		r := RosterRole{
			Role:       string(ce.Role),
			Profile:    ce.Route.Profile,
			Provider:   ce.Route.Preset.Provider,
			Model:      ce.Route.Preset.Model,
			PresetName: ce.Route.Preset.Name,
			LocalOnly:  ce.Route.Preset.LocalOnly,
		}
		if ce.Route.CustomAgent != nil {
			r.CustomAgent = ce.Route.CustomAgent.Name
		}
		if ce.Err != nil {
			r.Error = ce.Err.Error()
		}
		roles[i] = r
	}
	return AgentsRosterResult{
		Roles: roles,
		SwarmBudget: RosterBudget{
			MaxFixRounds: cfg.Swarm.Budget.MaxFixRounds, MaxTotalTokens: cfg.Swarm.Budget.MaxTotalTokens,
		},
		SDDBudget: RosterBudget{
			MaxFixRounds: cfg.SDD.MaxFixRounds, MaxTotalTokens: cfg.SDD.MaxTotalTokens,
		},
	}, nil
}
