package acp

import (
	"context"
	"encoding/json"
	"fmt"
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
