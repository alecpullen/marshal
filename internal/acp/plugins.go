package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"marshal/internal/plugins"
)

// PluginsRuntime is the per-session slice of state PluginsManager needs.
type PluginsRuntime struct {
	HomeDir    string
	WorkingDir string
	// Trusted gates project-scope operations, read once at Lookup time
	// from session.State.Trusted() — matching how every other manager's
	// Lookup closure in run.go projects fields off *app.Runtime rather
	// than re-checking live inside each handler.
	Trusted bool
}

// PluginsLookup returns the runtime registered for an ACP session id.
type PluginsLookup func(sessionID string) (*PluginsRuntime, bool)

// PluginsManagerConfig wires a PluginsManager to external dependencies.
type PluginsManagerConfig struct {
	Lookup PluginsLookup
}

// PluginsManager dispatches session/plugins_list and
// session/plugins_install_*. Like SkillsManager, it holds state across
// calls — staged (scanned, not yet confirmed or discarded) installs, per
// session.
type PluginsManager struct {
	lookup PluginsLookup

	scansMu sync.Mutex
	scans   map[string]map[string]*scannedPlugin // sessionID -> token -> entry
}

// scannedPlugin is one in-flight (scanned, not yet confirmed or
// discarded) plugin install.
type scannedPlugin struct {
	tempDir   string // the whole temp root; removed wholesale on cleanup
	cloneDest string // tempDir/clone — the scanned content
	name      string
	source    string
	ref       string
	commit    string
}

func NewPluginsManager(cfg PluginsManagerConfig) *PluginsManager {
	if cfg.Lookup == nil {
		panic("acp: PluginsManagerConfig.Lookup is required")
	}
	return &PluginsManager{lookup: cfg.Lookup, scans: map[string]map[string]*scannedPlugin{}}
}

// validPluginName reports whether name is safe to join onto a store
// directory. Plugin names identify a directory directly inside the
// plugin store, so anything that can redirect the join — a separator,
// a parent traversal, an absolute or volume-qualified path — is
// rejected rather than cleaned. This is enforced because the name
// arrives from an ACP client over the wire and PluginsRemove hands the
// result to os.RemoveAll: a name like "../../.." would otherwise delete
// an arbitrary directory tree outside the plugin store.
func validPluginName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, os.PathSeparator) {
		return false
	}
	return !filepath.IsAbs(name) && filepath.VolumeName(name) == ""
}

// PluginEntry mirrors plugins.LockEntry for JSON transport, with a Scope
// field added since a client sees both scopes merged into one list.
type PluginEntry struct {
	Name        string    `json:"name"`
	Source      string    `json:"source"`
	Ref         string    `json:"ref,omitempty"`
	Commit      string    `json:"commit"`
	ContentHash string    `json:"contentHash"`
	InstalledAt time.Time `json:"installedAt"`
	Scope       string    `json:"scope"`
}

// PluginsListResult is the JSON-RPC result for session/plugins_list.
type PluginsListResult struct {
	Plugins []PluginEntry `json:"plugins"`
}

// PluginsList handles session/plugins_list. Project-scope entries are
// included only when the session is trusted — silently omitted, not an
// error, matching the TUI's own lockEntries() behavior (listing is
// read-only, so a silent narrowing here is acceptable in a way it would
// not be for a write operation).
func (m *PluginsManager) PluginsList(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/plugins_list params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/plugins_list requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}

	out := []PluginEntry{}
	if lf, err := plugins.ReadLockfile(plugins.GlobalLockPath(rt.HomeDir)); err == nil {
		for _, e := range lf.Plugins {
			out = append(out, toPluginEntry(e, "global"))
		}
	}
	if rt.Trusted {
		if lf, err := plugins.ReadLockfile(plugins.ProjectLockPath(rt.WorkingDir)); err == nil {
			for _, e := range lf.Plugins {
				out = append(out, toPluginEntry(e, "project"))
			}
		}
	}
	return PluginsListResult{Plugins: out}, nil
}

func toPluginEntry(e plugins.LockEntry, scope string) PluginEntry {
	return PluginEntry{
		Name: e.Name, Source: e.Source, Ref: e.Ref, Commit: e.Commit,
		ContentHash: e.ContentHash, InstalledAt: e.InstalledAt, Scope: scope,
	}
}

// PluginsInstallScanParams is the JSON-RPC body for
// session/plugins_install_scan.
type PluginsInstallScanParams struct {
	SessionID string `json:"sessionId"`
	Source    string `json:"source"`
	Ref       string `json:"ref,omitempty"`
}

// PluginContentsSummary summarizes plugins.Contents for the wire — counts
// only, not the raw hook/MCP/command bodies, so a client can decide
// whether to proceed without the payload carrying executable content
// detail. (Deferred: exposing full detail for pre-install review.)
type PluginContentsSummary struct {
	HasManifest    bool `json:"hasManifest"`
	SkillCount     int  `json:"skillCount"`
	CommandCount   int  `json:"commandCount"`
	HookCount      int  `json:"hookCount"`
	MCPServerCount int  `json:"mcpServerCount"`
	MCPPolicyCount int  `json:"mcpPolicyCount"`
}

func summarizeContents(c plugins.Contents) PluginContentsSummary {
	return PluginContentsSummary{
		HasManifest: c.HasManifest, SkillCount: len(c.Skills), CommandCount: len(c.Commands),
		HookCount: len(c.Hooks), MCPServerCount: len(c.MCPServers), MCPPolicyCount: len(c.MCPPolicies),
	}
}

// PluginsInstallScanResult is the JSON-RPC result for
// session/plugins_install_scan.
type PluginsInstallScanResult struct {
	ScanToken string                `json:"scanToken"`
	Name      string                `json:"name"`
	Source    string                `json:"source"`
	Ref       string                `json:"ref,omitempty"`
	Commit    string                `json:"commit"`
	Contents  PluginContentsSummary `json:"contents"`
}

// PluginsInstallScan handles session/plugins_install_scan: clones source
// into a throwaway temp directory and scans its contents, without
// installing anything for real. Captures the real commit hash from
// Clone's return value — unlike the TUI's runInstallFromScan, which
// discards it and later records "unknown".
func (m *PluginsManager) PluginsInstallScan(ctx context.Context, params json.RawMessage) (any, error) {
	var p PluginsInstallScanParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/plugins_install_scan params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/plugins_install_scan requires sessionId")
	}
	if strings.TrimSpace(p.Source) == "" {
		return nil, invalidParamsError("session/plugins_install_scan requires a non-empty source")
	}
	if _, ok := m.lookup(p.SessionID); !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}

	cloneURL, name, err := plugins.NormalizeSource(p.Source)
	if err != nil {
		return nil, invalidParamsError("invalid source: %v", err)
	}
	inst, err := plugins.NewInstaller()
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: err.Error()}
	}
	tempDir, err := os.MkdirTemp("", "marshal-plugin-scan-*")
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("create staging dir: %v", err)}
	}
	cloneDest := filepath.Join(tempDir, "clone")
	commit, err := inst.Clone(ctx, cloneURL, p.Ref, cloneDest)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("clone: %v", err)}
	}
	os.RemoveAll(filepath.Join(cloneDest, ".git"))
	contents, err := plugins.ScanPlugin(cloneDest)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("scan plugin: %v", err)}
	}

	token := fmt.Sprintf("stg_%d", time.Now().UnixNano())
	m.scansMu.Lock()
	if m.scans[p.SessionID] == nil {
		m.scans[p.SessionID] = map[string]*scannedPlugin{}
	}
	m.scans[p.SessionID][token] = &scannedPlugin{
		tempDir: tempDir, cloneDest: cloneDest, name: name, source: cloneURL, ref: p.Ref, commit: commit,
	}
	m.scansMu.Unlock()

	return PluginsInstallScanResult{
		ScanToken: token, Name: name, Source: cloneURL, Ref: p.Ref, Commit: commit,
		Contents: summarizeContents(contents),
	}, nil
}

// takeScan removes and returns the scanned entry for (sessionID, token),
// used by both confirm and discard so an entry can only be consumed once.
func (m *PluginsManager) takeScan(sessionID, token string) (*scannedPlugin, bool) {
	m.scansMu.Lock()
	defer m.scansMu.Unlock()
	byToken, ok := m.scans[sessionID]
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

// PluginsInstallDiscardParams is the JSON-RPC body for
// session/plugins_install_discard.
type PluginsInstallDiscardParams struct {
	SessionID string `json:"sessionId"`
	ScanToken string `json:"scanToken"`
}

// PluginsInstallDiscard handles session/plugins_install_discard: removes
// a scanned install without installing it.
func (m *PluginsManager) PluginsInstallDiscard(ctx context.Context, params json.RawMessage) (any, error) {
	var p PluginsInstallDiscardParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/plugins_install_discard params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/plugins_install_discard requires sessionId")
	}
	staged, ok := m.takeScan(p.SessionID, p.ScanToken)
	if !ok {
		return nil, serverErrorf("no staged plugin scan %q for session %s", p.ScanToken, p.SessionID)
	}
	os.RemoveAll(staged.tempDir)
	return map[string]any{}, nil
}

func validPluginScope(scope string) bool {
	return scope == "global" || scope == "project"
}

// PluginsInstallConfirmParams is the JSON-RPC body for
// session/plugins_install_confirm.
type PluginsInstallConfirmParams struct {
	SessionID string `json:"sessionId"`
	ScanToken string `json:"scanToken"`
	Scope     string `json:"scope"`
}

// PluginsInstallResult is the JSON-RPC result for
// session/plugins_install_confirm.
type PluginsInstallResult struct {
	Name string `json:"name"`
}

// PluginsInstallConfirm handles session/plugins_install_confirm: commits
// a previously scanned install into the real plugin store. scope
// "project" is rejected outright on an untrusted session — never
// silently redirected to global or silently no-op'd.
func (m *PluginsManager) PluginsInstallConfirm(ctx context.Context, params json.RawMessage) (any, error) {
	var p PluginsInstallConfirmParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/plugins_install_confirm params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/plugins_install_confirm requires sessionId")
	}
	if !validPluginScope(p.Scope) {
		return nil, invalidParamsError("invalid scope %q: want \"global\" or \"project\"", p.Scope)
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if p.Scope == "project" && !rt.Trusted {
		return nil, serverErrorf("project scope requires a trusted session")
	}
	staged, ok := m.takeScan(p.SessionID, p.ScanToken)
	if !ok {
		return nil, serverErrorf("no staged plugin scan %q for session %s", p.ScanToken, p.SessionID)
	}
	defer os.RemoveAll(staged.tempDir)

	var storeDir, lockPath string
	if p.Scope == "project" {
		storeDir, lockPath = plugins.ProjectStoreDir(rt.WorkingDir), plugins.ProjectLockPath(rt.WorkingDir)
	} else {
		storeDir, lockPath = plugins.GlobalStoreDir(rt.HomeDir), plugins.GlobalLockPath(rt.HomeDir)
	}
	dest := filepath.Join(storeDir, staged.name)
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("create store dir: %v", err)}
	}
	if err := os.RemoveAll(dest); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("clear existing install: %v", err)}
	}
	if err := plugins.CopyDir(staged.cloneDest, dest); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("install plugin: %v", err)}
	}
	hash, err := plugins.HashDir(dest)
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("hash installed plugin: %v", err)}
	}
	lf, err := plugins.ReadLockfile(lockPath)
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("read lockfile: %v", err)}
	}
	lf.Upsert(plugins.LockEntry{
		Name: staged.name, Source: staged.source, Ref: staged.ref,
		Commit: staged.commit, ContentHash: hash, InstalledAt: time.Now(),
	})
	if err := lf.Write(lockPath); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("write lockfile: %v", err)}
	}
	return PluginsInstallResult{Name: staged.name}, nil
}

// PluginsRemoveParams is the JSON-RPC body for session/plugins_remove.
type PluginsRemoveParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
}

// PluginsRemove handles session/plugins_remove. No confirmation step —
// the explicit RPC call is itself the confirmation. Project scope is
// rejected outright on an untrusted session, same as confirm.
func (m *PluginsManager) PluginsRemove(ctx context.Context, params json.RawMessage) (any, error) {
	var p PluginsRemoveParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/plugins_remove params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/plugins_remove requires sessionId")
	}
	if p.Name == "" {
		return nil, invalidParamsError("session/plugins_remove requires name")
	}
	if !validPluginName(p.Name) {
		return nil, invalidParamsError("invalid plugin name %q", p.Name)
	}
	if !validPluginScope(p.Scope) {
		return nil, invalidParamsError("invalid scope %q: want \"global\" or \"project\"", p.Scope)
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if p.Scope == "project" && !rt.Trusted {
		return nil, serverErrorf("project scope requires a trusted session")
	}
	var storeDir, lockPath string
	if p.Scope == "project" {
		storeDir, lockPath = plugins.ProjectStoreDir(rt.WorkingDir), plugins.ProjectLockPath(rt.WorkingDir)
	} else {
		storeDir, lockPath = plugins.GlobalStoreDir(rt.HomeDir), plugins.GlobalLockPath(rt.HomeDir)
	}
	if err := os.RemoveAll(filepath.Join(storeDir, p.Name)); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("remove plugin: %v", err)}
	}
	lf, err := plugins.ReadLockfile(lockPath)
	if err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("read lockfile: %v", err)}
	}
	lf.Remove(p.Name)
	if err := lf.Write(lockPath); err != nil {
		return nil, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("write lockfile: %v", err)}
	}
	return map[string]any{}, nil
}

// CloseSession removes every scanned (not yet confirmed or discarded)
// install for sessionID. Best-effort: no error return, since a cleanup
// failure must never block a session from actually closing.
func (m *PluginsManager) CloseSession(sessionID string) {
	m.scansMu.Lock()
	byToken := m.scans[sessionID]
	delete(m.scans, sessionID)
	m.scansMu.Unlock()

	for _, staged := range byToken {
		if err := os.RemoveAll(staged.tempDir); err != nil {
			slog.Default().Warn("acp: failed to clean up scanned plugin install", "session", sessionID, "tempDir", staged.tempDir, "err", err)
		}
	}
}
