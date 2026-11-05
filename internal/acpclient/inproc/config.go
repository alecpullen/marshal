package inproc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/presetsetup"
	"marshal/internal/app/session"
	"marshal/internal/llm/modelcatalog"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// userConfigPath is the user config file the user-config writes change:
// Deps.UserConfigPath when it is set, otherwise config.UserConfigPath of the
// home directory.
func userConfigPath(d Deps) (string, error) {
	if d.UserConfigPath != "" {
		return d.UserConfigPath, nil
	}
	home, err := homeDirOf(d)
	if err != nil {
		return "", err
	}
	return config.UserConfigPath(home), nil
}

// homeDirOf is the home directory the user config is found under:
// Deps.HomeDir when it is set, otherwise os.UserHomeDir(). It carries the
// TUI's own wording for a home it cannot find (tui/model.go:871).
func homeDirOf(d Deps) (string, error) {
	if d.HomeDir != "" {
		return d.HomeDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return home, nil
}

// refreshLayers re-reads the config layers and installs them on state when the
// read reported success. Without a state or a reloader it does nothing, and a
// failed reload leaves the layers the state has.
func refreshLayers(state *session.State, reload func() (config.Layers, bool)) {
	if state == nil || reload == nil {
		return
	}
	layers, ok := reload()
	if !ok {
		return
	}
	state.SetLayers(layers)
}

// clientDeps returns the host's dependencies under its mutex, for the config
// calls that read more than one of them.
func (h *Host) clientDeps() Deps {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deps
}

// SaveUserValue writes value at the dotted TOML path tomlPath of the user
// config, leaving the rest of the file alone, then refreshes the session's
// layers.
func (h *Host) SaveUserValue(ctx context.Context, tomlPath string, value any) error {
	path, err := userConfigPath(h.clientDeps())
	if err != nil {
		return err
	}
	if err := config.SaveUserConfigValue(path, tomlPath, value); err != nil {
		return err
	}
	refreshLayers(h.currentState(), h.currentReloadLayers())
	return nil
}

// SaveUserProviders writes the user config's [providers] section, credentials
// included, then refreshes the session's layers.
func (h *Host) SaveUserProviders(ctx context.Context, providers, baseline map[string]config.ProviderConfig) error {
	path, err := userConfigPath(h.clientDeps())
	if err != nil {
		return err
	}
	if err := config.SaveUserConfigProviders(path, providers, baseline); err != nil {
		return err
	}
	refreshLayers(h.currentState(), h.currentReloadLayers())
	return nil
}

// SaveUserPresets writes the user config's [models.presets] section, merged
// against baseline, then refreshes the session's layers.
func (h *Host) SaveUserPresets(ctx context.Context, presets, baseline map[string]routing.ModelPreset) error {
	path, err := userConfigPath(h.clientDeps())
	if err != nil {
		return err
	}
	if err := config.SaveUserConfigPresets(path, presets, baseline); err != nil {
		return err
	}
	refreshLayers(h.currentState(), h.currentReloadLayers())
	return nil
}

// SaveProviderKey stores key as the literal API key of the provider called
// name, keeping the entry's other fields, then refreshes the session's layers.
func (h *Host) SaveProviderKey(ctx context.Context, name, key string) error {
	path, err := userConfigPath(h.clientDeps())
	if err != nil {
		return err
	}
	if err := config.SaveUserConfigProviderAPIKey(path, name, config.ProviderConfig{APIKey: key}); err != nil {
		return err
	}
	refreshLayers(h.currentState(), h.currentReloadLayers())
	return nil
}

// currentReloadLayers is the host's layer reloader, or nil when it has none.
func (h *Host) currentReloadLayers() func() (config.Layers, bool) {
	return h.clientDeps().ReloadLayers
}

// PreparePresets proposes presets for the models a connected provider reported:
// cfg's presets, copied, with every reported model's preset created or
// refreshed from the models.dev catalog and the provenance sidecar. cfg is not
// changed.
func (h *Host) PreparePresets(ctx context.Context, cfg config.Config, providerName, baseURL string, models []schema.ModelInfo) (map[string]routing.ModelPreset, acpwire.PresetSetup, error) {
	// The preset map is shared with the caller's live config. Copy it before
	// Apply mutates it, so a declined connect would still change presets. The
	// copy is shallow, which is enough: Apply copies the pricing struct it
	// updates rather than writing through the shared pointer.
	if cfg.Models.Presets != nil {
		presets := make(map[string]routing.ModelPreset, len(cfg.Models.Presets))
		for k, v := range cfg.Models.Presets {
			presets[k] = v
		}
		cfg.Models.Presets = presets
	}
	dir := h.modelCacheDir()
	allowed := cfg.Privacy.ModelsDevEnrichment != nil && *cfg.Privacy.ModelsDevEnrichment
	var cat *modelcatalog.Catalog
	if allowed {
		loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cat, _ = modelcatalog.NewCache(dir).Load(loadCtx)
		cancel()
	}
	// A missing sidecar is safe to recreate; an unreadable one is not.
	// Recreating it would discard every recorded field and permanently untrack
	// presets that are still fine on disk, so leave the file alone and let the
	// presets be created unmanaged.
	provenance, provErr := presetsetup.LoadProvenance(dir)
	if provErr != nil {
		provenance = nil
	}
	res := presetsetup.Apply(&cfg, providerName, baseURL, models, cat, provenance)
	setup := acpwire.PresetSetup{
		Prepared: res.Prepared,
		Matched:  res.Matched,
		Fallback: res.Fallback,
	}
	if provenance != nil {
		if raw, err := json.Marshal(provenance); err == nil {
			setup.Provenance = raw
			managed := make([]string, 0, len(provenance.Presets))
			for name := range provenance.Presets {
				managed = append(managed, name)
			}
			sort.Strings(managed)
			setup.Managed = managed
		}
	}
	return cfg.Models.Presets, setup, nil
}

// SaveProvenance writes the provenance sidecar PreparePresets returned. A nil
// or empty provenance writes nothing.
func (h *Host) SaveProvenance(ctx context.Context, provenance json.RawMessage) error {
	if len(provenance) == 0 {
		return nil
	}
	var p presetsetup.Provenance
	if err := json.Unmarshal(provenance, &p); err != nil {
		return err
	}
	dir := h.modelCacheDir()
	if dir == "" {
		home, err := homeDirOf(h.clientDeps())
		if err != nil {
			return err
		}
		dir = config.DataDir(home)
	}
	return presetsetup.SaveProvenance(dir, &p)
}

// Config is the session's live config. A closed session keeps answering with
// its final value (C4).
func (s *Session) Config() config.Config { return s.state.ConfigSnapshot() }

// Layers is the session's config layers, with provenance. A closed session
// keeps answering with its final value (C4).
func (s *Session) Layers() config.Layers { return s.state.Layers() }

// ApplyConfig installs cfg as the session's config without saving it or
// rebuilding the runtime. A manual mode selection survives, as a reload's
// does.
func (s *Session) ApplyConfig(ctx context.Context, cfg config.Config) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	_, err := s.state.RebaseInteractionConfig(cfg)
	return err
}

// ReloadConfig rebuilds the runtime from cfg. It reports false, and does
// nothing, when the session has no reload path.
func (s *Session) ReloadConfig(ctx context.Context, cfg config.Config) (bool, error) {
	if s.closed.Load() {
		return false, errSessionClosed
	}
	if s.reloadConfig == nil {
		return false, nil
	}
	return true, s.reloadConfig(cfg)
}

// ReconnectMCP rebuilds the runtime from the session's current config, so an
// MCP server that has just been signed in to connects. It reports false, and
// does nothing, when the session has no reload path.
//
// It clears the notices first, not after: the stale "needs OAuth" notice is
// what the sign-in resolved, but a rebuild that fails sets a fresh notice of
// its own, and clearing afterwards would wipe it and leave the user with a
// bare success message.
func (s *Session) ReconnectMCP(ctx context.Context) (bool, error) {
	if s.closed.Load() {
		return false, errSessionClosed
	}
	if s.reloadConfig == nil {
		return false, nil
	}
	s.state.ClearNotice(session.NoticeProvider)
	s.state.ClearNotice(session.NoticeConfig)
	if err := s.reloadConfig(s.state.ConfigSnapshot()); err != nil {
		return true, err
	}
	return true, nil
}

// projectConfigPath is the session's project config file, or "" when the
// session's working directory is unknown.
func (s *Session) projectConfigPath() string {
	if s.state.WorkingDir == "" {
		return ""
	}
	return config.ProjectConfigPath(s.state.WorkingDir)
}

// SaveProjectConfig writes cfg's project-scoped values to the session's
// project config, diffed against the session's layers, then refreshes the
// trust record's hash and the layers.
func (s *Session) SaveProjectConfig(ctx context.Context, cfg config.Config) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if err := config.SaveProjectConfig(s.projectConfigPath(), cfg, s.state.Layers()); err != nil {
		return err
	}
	if s.trustRefresh != nil {
		s.trustRefresh(s.state.WorkingDir)
	}
	refreshLayers(s.state, s.reloadLayers)
	return nil
}

// SaveVerifyCommands writes the plan run's verify commands to the session's
// project config, then refreshes the layers.
func (s *Session) SaveVerifyCommands(ctx context.Context, build, test string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if err := config.SaveVerifyCommands(s.projectConfigPath(), build, test); err != nil {
		return err
	}
	refreshLayers(s.state, s.reloadLayers)
	return nil
}
