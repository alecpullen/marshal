package acp

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/plugins"
)

func writeTestLockfile(t *testing.T, path string, entries ...plugins.LockEntry) {
	t.Helper()
	lf := &plugins.Lockfile{Plugins: entries}
	if err := lf.Write(path); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
}

func TestPluginsListIncludesProjectScopeOnlyWhenTrusted(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	globalLock := plugins.GlobalLockPath(home)
	projectLock := plugins.ProjectLockPath(work)
	writeTestLockfile(t, globalLock, plugins.LockEntry{Name: "global-plugin", Source: "github:a/b"})
	writeTestLockfile(t, projectLock, plugins.LockEntry{Name: "project-plugin", Source: "github:c/d"})

	untrusted := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: home, WorkingDir: work, Trusted: false}, true
		},
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := untrusted.PluginsList(context.Background(), raw)
	if err != nil {
		t.Fatalf("PluginsList: %v", err)
	}
	result := res.(PluginsListResult)
	if len(result.Plugins) != 1 || result.Plugins[0].Name != "global-plugin" {
		t.Fatalf("untrusted PluginsList = %+v, want only global-plugin", result.Plugins)
	}

	trusted := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: home, WorkingDir: work, Trusted: true}, true
		},
	})
	res, err = trusted.PluginsList(context.Background(), raw)
	if err != nil {
		t.Fatalf("PluginsList: %v", err)
	}
	result = res.(PluginsListResult)
	if len(result.Plugins) != 2 {
		t.Fatalf("trusted PluginsList = %+v, want 2 entries", result.Plugins)
	}
}

func TestPluginsListRequiresSessionID(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) { return nil, false },
	})
	_, err := mgr.PluginsList(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("PluginsList with no sessionId: got nil error, want an error")
	}
}

func TestPluginsListEmptyReturnsEmptySliceNotNil(t *testing.T) {
	mgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: t.TempDir(), WorkingDir: t.TempDir(), Trusted: false}, true
		},
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := mgr.PluginsList(context.Background(), raw)
	if err != nil {
		t.Fatalf("PluginsList: %v", err)
	}
	if res.(PluginsListResult).Plugins == nil {
		t.Fatal("PluginsList.Plugins = nil, want a non-nil empty slice")
	}
}
