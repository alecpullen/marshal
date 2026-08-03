package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// validSkillName reports whether name is safe to join onto a scope
// directory. Skill names identify a file or bundle directory directly
// inside that scope, so anything that can redirect the join — a separator,
// a parent traversal, an absolute or volume-qualified path — is rejected
// rather than cleaned. This is enforced because the name arrives from an
// ACP client over the wire and SkillsRemove hands the result to
// os.RemoveAll: a name like "../../.." would otherwise delete an arbitrary
// directory tree outside the skills scope.
func validSkillName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, os.PathSeparator) {
		return false
	}
	// filepath.IsAbs misses "C:foo" style volume-relative paths on Windows,
	// which VolumeName catches.
	return !filepath.IsAbs(name) && filepath.VolumeName(name) == ""
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
	if !validSkillName(p.Name) {
		return nil, invalidParamsError("invalid skill name %q", p.Name)
	}
	if !validScope(p.Scope) {
		return nil, invalidParamsError("invalid scope %q: want \"global\" or \"project\"", p.Scope)
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	dir := skills.ScopeDir(rt.HomeDir, rt.WorkingDir, p.Scope == "project")

	// A skill is stored either as a bundle directory or as a flat <name>.md
	// file (see skills/loader.go, and Install, which writes the flat form for
	// any non-bundle source). Removing only the directory left every
	// single-file skill on disk while still reporting success, because
	// os.RemoveAll returns nil for a path that does not exist.
	removed := false
	for _, path := range []string{
		filepath.Join(dir, p.Name),
		filepath.Join(dir, p.Name+".md"),
	} {
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("remove skill: %v", err)}
		}
		removed = true
	}
	if !removed {
		return nil, invalidParamsError("no %s skill named %q", p.Scope, p.Name)
	}
	return map[string]any{}, nil
}

// SkillsInstallPreviewParams is the JSON-RPC body for
// session/skills_install_preview.
type SkillsInstallPreviewParams struct {
	SessionID string `json:"sessionId"`
	Source    string `json:"source"`
}

// SkillsInstallPreviewResult is the JSON-RPC result for
// session/skills_install_preview.
type SkillsInstallPreviewResult struct {
	StagingToken string `json:"stagingToken"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Risk         string `json:"risk"`
	Source       string `json:"source"`
}

// SkillsInstallPreview handles session/skills_install_preview. Runs the
// same clone-or-copy logic skills.Install always uses, pointed at a
// throwaway temp directory instead of a real scope directory — nothing
// is exposed to the rest of the system until session/skills_install_confirm.
func (m *SkillsManager) SkillsInstallPreview(ctx context.Context, params json.RawMessage) (any, error) {
	var p SkillsInstallPreviewParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/skills_install_preview params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/skills_install_preview requires sessionId")
	}
	if strings.TrimSpace(p.Source) == "" {
		return nil, invalidParamsError("session/skills_install_preview requires a non-empty source")
	}
	if _, ok := m.lookup(p.SessionID); !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}

	tempDir, err := os.MkdirTemp("", "marshal-skill-preview-*")
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("create staging dir: %v", err)}
	}
	stagedPath, err := skills.Install(ctx, p.Source, tempDir, "")
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("install preview: %v", err)}
	}
	name := strings.TrimSuffix(filepath.Base(stagedPath), ".md")
	idx, err := skills.LoadSkills(tempDir, tempDir)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("parse staged skill: %v", err)}
	}
	skill, ok := idx.Load(name)
	if !ok {
		os.RemoveAll(tempDir)
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("staged skill %q not found after install", name)}
	}

	token := fmt.Sprintf("stg_%d", time.Now().UnixNano())
	m.stagingMu.Lock()
	if m.staging[p.SessionID] == nil {
		m.staging[p.SessionID] = map[string]*stagedSkill{}
	}
	m.staging[p.SessionID][token] = &stagedSkill{
		tempDir: tempDir, stagedPath: stagedPath, name: name, source: p.Source,
	}
	m.stagingMu.Unlock()

	return SkillsInstallPreviewResult{
		StagingToken: token, Name: skill.Name, Description: skill.Description,
		Risk: skill.Risk, Source: p.Source,
	}, nil
}

// takeStaged removes and returns the staged entry for (sessionID, token),
// used by both confirm and discard so an entry can only be consumed once.
func (m *SkillsManager) takeStaged(sessionID, token string) (*stagedSkill, bool) {
	m.stagingMu.Lock()
	defer m.stagingMu.Unlock()
	byToken, ok := m.staging[sessionID]
	if !ok {
		return nil, false
	}
	staged, ok := byToken[token]
	if !ok {
		return nil, false
	}
	delete(byToken, token)
	return staged, true
}

// SkillsInstallConfirmParams is the JSON-RPC body for
// session/skills_install_confirm.
type SkillsInstallConfirmParams struct {
	SessionID    string `json:"sessionId"`
	StagingToken string `json:"stagingToken"`
	Scope        string `json:"scope"`
}

// SkillsInstallResult is the JSON-RPC result for
// session/skills_install_confirm.
type SkillsInstallResult struct {
	Name string `json:"name"`
}

// SkillsInstallConfirm handles session/skills_install_confirm: copies a
// previously staged install into the real scope directory.
func (m *SkillsManager) SkillsInstallConfirm(ctx context.Context, params json.RawMessage) (any, error) {
	var p SkillsInstallConfirmParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/skills_install_confirm params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/skills_install_confirm requires sessionId")
	}
	if !validScope(p.Scope) {
		return nil, invalidParamsError("invalid scope %q: want \"global\" or \"project\"", p.Scope)
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	staged, ok := m.takeStaged(p.SessionID, p.StagingToken)
	if !ok {
		return nil, serverErrorf("no staged install %q for session %s", p.StagingToken, p.SessionID)
	}
	defer os.RemoveAll(staged.tempDir)

	targetDir := skills.ScopeDir(rt.HomeDir, rt.WorkingDir, p.Scope == "project")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("create target dir: %v", err)}
	}
	dest := filepath.Join(targetDir, filepath.Base(staged.stagedPath))
	if err := copyPath(staged.stagedPath, dest); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("install skill: %v", err)}
	}
	return SkillsInstallResult{Name: staged.name}, nil
}

// SkillsInstallDiscardParams is the JSON-RPC body for
// session/skills_install_discard.
type SkillsInstallDiscardParams struct {
	SessionID    string `json:"sessionId"`
	StagingToken string `json:"stagingToken"`
}

// SkillsInstallDiscard handles session/skills_install_discard: removes a
// staged install without installing it.
func (m *SkillsManager) SkillsInstallDiscard(ctx context.Context, params json.RawMessage) (any, error) {
	var p SkillsInstallDiscardParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/skills_install_discard params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/skills_install_discard requires sessionId")
	}
	staged, ok := m.takeStaged(p.SessionID, p.StagingToken)
	if !ok {
		return nil, serverErrorf("no staged install %q for session %s", p.StagingToken, p.SessionID)
	}
	os.RemoveAll(staged.tempDir)
	return map[string]any{}, nil
}

// SkillsLoadParams is the JSON-RPC body for session/skills_load.
type SkillsLoadParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

// SkillsLoad handles session/skills_load. Builds a fresh index from disk
// on every call (not a cached index), matching the TUI's own "Load now"
// action (internal/app/tui/skills/panel.go's activeIndex()) — so a
// just-confirmed install is loadable immediately, with no cache
// invalidation step to worry about.
func (m *SkillsManager) SkillsLoad(ctx context.Context, params json.RawMessage) (any, error) {
	var p SkillsLoadParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/skills_load params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/skills_load requires sessionId")
	}
	if p.Name == "" {
		return nil, invalidParamsError("session/skills_load requires name")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.State == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no state"}
	}
	idx, err := skills.LoadSkills(
		skills.ScopeDir(rt.HomeDir, rt.WorkingDir, false),
		skills.ScopeDir(rt.HomeDir, rt.WorkingDir, true),
	)
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("load skills index: %v", err)}
	}
	if err := skills.LoadSkillIntoSession(idx, rt.State, p.Name); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: err.Error()}
	}
	return map[string]any{}, nil
}

// CloseSession removes every staged (previewed, never confirmed or
// discarded) install for sessionID, so an abandoned preview doesn't leak
// a temp directory forever. Best-effort: no error return, since a
// cleanup failure must never block a session from actually closing.
func (m *SkillsManager) CloseSession(sessionID string) {
	m.stagingMu.Lock()
	byToken := m.staging[sessionID]
	delete(m.staging, sessionID)
	m.stagingMu.Unlock()

	for _, staged := range byToken {
		if err := os.RemoveAll(staged.tempDir); err != nil {
			slog.Default().Warn("acp: failed to clean up staged skill install", "session", sessionID, "tempDir", staged.tempDir, "err", err)
		}
	}
}

// copyPath copies src to dst. If src is a regular file, dst is written
// directly; if src is a directory, its contents are copied recursively.
// A staged skill install can be either, depending on whether the source
// was a single .md file or a SKILL.md bundle directory.
func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
