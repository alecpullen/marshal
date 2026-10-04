package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	Scope           string    `json:"scope"`
	ScopeKey        string    `json:"scopeKey"`
	OwnerID         string    `json:"ownerId"`
	// LearnedProjectRoot is the root of the project the memory was learned in.
	LearnedProjectRoot string   `json:"learnedProjectRoot,omitempty"`
	LearnedAgent       string   `json:"learnedAgent,omitempty"`
	LearnedStep        int64    `json:"learnedStep,omitempty"`
	ConfirmedBy        []string `json:"confirmedBy"`
}

// MemoryListParams is the JSON-RPC body for session/memory_list.
type MemoryListParams struct {
	SessionID string `json:"sessionId"`
	// Scope optionally filters to one of project, workspace or global.
	Scope string `json:"scope,omitempty"`
}

// MemoryListResult is the JSON-RPC result for session/memory_list.
type MemoryListResult struct {
	Entries []MemoryEntry `json:"entries"`
}

// MemoryList handles session/memory_list.
func (m *MemoryManager) MemoryList(ctx context.Context, params json.RawMessage) (any, error) {
	var p MemoryListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/memory_list params: %v", err)
		}
	}
	if p.Scope != "" && !validMemoryScope[p.Scope] {
		return nil, invalidParamsError("invalid scope %q: want one of project, workspace, global", p.Scope)
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
	records, err := rt.DB.GetScopedMemories(rt.ProjectID, os.Getenv("MARSHAL_WORKSPACE"))
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("list memories: %v", err)}
	}
	entries := make([]MemoryEntry, 0, len(records))
	roots := map[int64]string{}
	for _, r := range records {
		if p.Scope != "" && r.Scope != p.Scope {
			continue
		}
		root, seen := roots[r.ProjectID]
		if !seen {
			root, _ = rt.DB.ProjectRoot(r.ProjectID)
			roots[r.ProjectID] = root
		}
		confirmed := r.ConfirmedBy
		if confirmed == nil {
			confirmed = []string{}
		}
		entries = append(entries, MemoryEntry{
			ID: r.ID, Kind: r.Kind, Content: r.Content, Confidence: r.Confidence,
			SourceSessionID: r.SourceSessionID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			Scope: r.Scope, ScopeKey: r.ScopeKey, OwnerID: r.OwnerID,
			LearnedProjectRoot: root, LearnedAgent: r.LearnedAgent, LearnedStep: r.LearnedStep,
			ConfirmedBy: confirmed,
		})
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

var validMemoryScope = map[string]bool{
	db.MemoryScopeProject: true, db.MemoryScopeWorkspace: true, db.MemoryScopeGlobal: true,
}

// memoryRuntime resolves a session for the scope methods, with the same
// errors as the other memory handlers.
func (m *MemoryManager) memoryRuntime(sessionID, method string) (*MemoryRuntime, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("acp: %s requires sessionId", method)
	}
	rt, ok := m.lookup(sessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", sessionID)
	}
	if rt.DB == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no database handle"}
	}
	return rt, nil
}

// requireVisible rejects a memory id the session could not see in
// memory_list: another project's project-scoped row, or a workspace row of
// another workspace.
func (m *MemoryManager) requireVisible(rt *MemoryRuntime, id int64, method string) error {
	mem, err := rt.DB.GetMemory(id)
	if err != nil {
		return invalidParamsError("%s: unknown memory %d", method, id)
	}
	switch mem.Scope {
	case db.MemoryScopeGlobal:
		return nil
	case db.MemoryScopeWorkspace:
		if ws := os.Getenv("MARSHAL_WORKSPACE"); ws != "" && mem.ScopeKey == ws {
			return nil
		}
	default:
		if mem.ProjectID == rt.ProjectID {
			return nil
		}
	}
	return invalidParamsError("%s: memory %d is not visible to this session", method, id)
}

// MemorySuggestionEntry is one promotion suggestion.
type MemorySuggestionEntry struct {
	MemoryID         int64  `json:"memoryId"`
	MatchProjectRoot string `json:"matchProjectRoot"`
	SuggestedScope   string `json:"suggestedScope"`
}

// MemorySuggestions handles session/memory_suggestions: project memories
// that another project holds too, with the scope they could be promoted to.
func (m *MemoryManager) MemorySuggestions(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if err := decodeParams(params, &p, "session/memory_suggestions"); err != nil {
		return nil, err
	}
	rt, err := m.memoryRuntime(p.SessionID, "session/memory_suggestions")
	if err != nil {
		return nil, err
	}
	found, err := rt.DB.MemorySuggestions(rt.ProjectID)
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("memory suggestions: %v", err)}
	}
	out := make([]MemorySuggestionEntry, len(found))
	for i, s := range found {
		out[i] = MemorySuggestionEntry{MemoryID: s.MemoryID, MatchProjectRoot: s.MatchRoot, SuggestedScope: s.SuggestedScope}
	}
	return map[string]any{"suggestions": out}, nil
}

// MemoryPromoteParams is the JSON-RPC body for session/memory_promote.
type MemoryPromoteParams struct {
	SessionID string `json:"sessionId"`
	ID        int64  `json:"id"`
	Scope     string `json:"scope"`
	ScopeKey  string `json:"scopeKey,omitempty"`
}

// MemoryPromote handles session/memory_promote: it moves a memory to the
// given scope and merges same-content duplicates from other projects.
func (m *MemoryManager) MemoryPromote(ctx context.Context, params json.RawMessage) (any, error) {
	var p MemoryPromoteParams
	if err := decodeParams(params, &p, "session/memory_promote"); err != nil {
		return nil, err
	}
	if p.ID == 0 {
		return nil, invalidParamsError("session/memory_promote requires a non-zero id")
	}
	if !validMemoryScope[p.Scope] {
		return nil, invalidParamsError("invalid scope %q: want one of project, workspace, global", p.Scope)
	}
	if p.Scope == db.MemoryScopeWorkspace && p.ScopeKey == "" {
		return nil, invalidParamsError("session/memory_promote requires scopeKey for the workspace scope")
	}
	rt, err := m.memoryRuntime(p.SessionID, "session/memory_promote")
	if err != nil {
		return nil, err
	}
	if err := m.requireVisible(rt, p.ID, "session/memory_promote"); err != nil {
		return nil, err
	}
	if err := rt.DB.PromoteMemory(p.ID, p.Scope, p.ScopeKey, time.Now()); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("promote memory: %v", err)}
	}
	return map[string]any{}, nil
}

// MemoryConfirmParams is the JSON-RPC body for session/memory_confirm.
type MemoryConfirmParams struct {
	SessionID string `json:"sessionId"`
	ID        int64  `json:"id"`
	Agent     string `json:"agent"`
}

// MemoryConfirm handles session/memory_confirm: it records that agent also
// holds the memory (appended to confirmedBy, once).
func (m *MemoryManager) MemoryConfirm(ctx context.Context, params json.RawMessage) (any, error) {
	var p MemoryConfirmParams
	if err := decodeParams(params, &p, "session/memory_confirm"); err != nil {
		return nil, err
	}
	if p.ID == 0 {
		return nil, invalidParamsError("session/memory_confirm requires a non-zero id")
	}
	if p.Agent == "" {
		return nil, invalidParamsError("session/memory_confirm requires agent")
	}
	rt, err := m.memoryRuntime(p.SessionID, "session/memory_confirm")
	if err != nil {
		return nil, err
	}
	if err := m.requireVisible(rt, p.ID, "session/memory_confirm"); err != nil {
		return nil, err
	}
	if err := rt.DB.ConfirmMemory(p.ID, p.Agent); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("confirm memory: %v", err)}
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
