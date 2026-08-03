package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"marshal/internal/app/session"
	"marshal/internal/skills"
)

// SkillsRuntime is the per-session slice of state SkillsManager needs.
type SkillsRuntime struct {
	HomeDir    string
	WorkingDir string
	State      *session.State
}

// SkillsLookup returns the runtime registered for an ACP session id.
type SkillsLookup func(sessionID string) (*SkillsRuntime, bool)

// SkillsManagerConfig wires a SkillsManager to external dependencies.
type SkillsManagerConfig struct {
	Lookup SkillsLookup
}

// SkillsManager dispatches session/skills_list, session/skills_install_*,
// session/skills_remove, and session/skills_load. Unlike MemoryManager
// and CommandManager, it holds state across calls — staged (not yet
// confirmed or discarded) installs, per session.
type SkillsManager struct {
	lookup SkillsLookup

	stagingMu sync.Mutex
	staging   map[string]map[string]*stagedSkill // sessionID -> token -> entry
}

// stagedSkill is one in-flight (previewed, not yet confirmed or
// discarded) skill install.
type stagedSkill struct {
	tempDir    string // the whole temp root; removed wholesale on cleanup
	stagedPath string // tempDir/<name> or tempDir/<name>.md — the staged content
	name       string
	source     string
}

func NewSkillsManager(cfg SkillsManagerConfig) *SkillsManager {
	if cfg.Lookup == nil {
		panic("acp: SkillsManagerConfig.Lookup is required")
	}
	return &SkillsManager{lookup: cfg.Lookup, staging: map[string]map[string]*stagedSkill{}}
}

func validScope(scope string) bool {
	return scope == "global" || scope == "project"
}

// SkillEntry mirrors skills.ScopedSkill for JSON transport.
type SkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
	Scope       string `json:"scope"`
}

// SkillsListResult is the JSON-RPC result for session/skills_list.
type SkillsListResult struct {
	Skills []SkillEntry `json:"skills"`
}

// SkillsList handles session/skills_list.
func (m *SkillsManager) SkillsList(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/skills_list params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/skills_list requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	scoped, err := skills.ListScopes(
		skills.ScopeDir(rt.HomeDir, rt.WorkingDir, false),
		skills.ScopeDir(rt.HomeDir, rt.WorkingDir, true),
	)
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("list skills: %v", err)}
	}
	out := make([]SkillEntry, len(scoped))
	for i, s := range scoped {
		out[i] = SkillEntry{Name: s.Skill.Name, Description: s.Skill.Description, Risk: s.Skill.Risk, Scope: s.Scope}
	}
	return SkillsListResult{Skills: out}, nil
}

// SkillsRemoveParams is the JSON-RPC body for session/skills_remove.
type SkillsRemoveParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
}

// SkillsRemove handles session/skills_remove. No confirmation step —
// the explicit RPC call is itself the confirmation.
func (m *SkillsManager) SkillsRemove(ctx context.Context, params json.RawMessage) (any, error) {
	var p SkillsRemoveParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/skills_remove params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/skills_remove requires sessionId")
	}
	if p.Name == "" {
		return nil, invalidParamsError("session/skills_remove requires name")
	}
	if !validScope(p.Scope) {
		return nil, invalidParamsError("invalid scope %q: want \"global\" or \"project\"", p.Scope)
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	dir := skills.ScopeDir(rt.HomeDir, rt.WorkingDir, p.Scope == "project")
	if err := os.RemoveAll(filepath.Join(dir, p.Name)); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("remove skill: %v", err)}
	}
	return map[string]any{}, nil
}
