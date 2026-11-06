package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/presetsetup"
	"marshal/internal/app/session"
	"marshal/internal/llm/modelcatalog"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/strutil"
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
// layers. At a secret's path a masked value is restored to the stored secret
// it stands for; a mask with nothing behind it fails the call and writes
// nothing.
func (h *Host) SaveUserValue(ctx context.Context, tomlPath string, value any) error {
	path, err := userConfigPath(h.clientDeps())
	if err != nil {
		return err
	}
	value, err = h.restoreSecretValue(tomlPath, value)
	if err != nil {
		return err
	}
	if err := config.SaveUserConfigValue(path, tomlPath, value); err != nil {
		return err
	}
	refreshLayers(h.currentState(), h.currentReloadLayers())
	return nil
}

// restoreSecretValue replaces a masked value written at a secret's TOML path
// with the stored secret it stands for. Any other path is returned unchanged,
// so a typed key is written as given and a cleared one stays cleared.
func (h *Host) restoreSecretValue(tomlPath string, value any) (any, error) {
	raw, ok := value.(string)
	if !ok {
		return value, nil
	}
	cfg, secretValue, ok := secretConfigAt(tomlPath, raw)
	if !ok {
		return value, nil
	}
	restored, err := config.RestoreSecrets(cfg, nil, h.stored()...)
	if err != nil {
		return nil, err
	}
	return secretValue(restored), nil
}

// secretConfigAt reports the one-secret config that carries raw at tomlPath,
// with a function that reads that secret back. It reports false for any other
// path.
func secretConfigAt(tomlPath, raw string) (config.Config, func(config.Config) string, bool) {
	if rest, ok := strings.CutPrefix(tomlPath, "providers."); ok {
		name, found := strings.CutSuffix(rest, ".api_key")
		if !found || name == "" || strings.Contains(name, ".") {
			return config.Config{}, nil, false
		}
		return config.Config{Providers: map[string]config.ProviderConfig{name: {APIKey: raw}}},
			func(c config.Config) string { return c.Providers[name].APIKey }, true
	}
	switch tomlPath {
	case "web.search_key":
		return config.Config{Web: config.WebConfig{SearchKey: raw}},
			func(c config.Config) string { return c.Web.SearchKey }, true
	case "desktop.cdp_url":
		return config.Config{Desktop: config.DesktopConfig{CDPURL: raw}},
			func(c config.Config) string { return c.Desktop.CDPURL }, true
	}
	return config.Config{}, nil, false
}

// SaveUserProviders writes the user config's [providers] section, credentials
// included, then refreshes the session's layers. Each masked key is restored
// to the stored key it stands for, renamed mapping a provider's name here to
// its name in the stored configs; a mask with nothing behind it fails the call
// and writes nothing.
func (h *Host) SaveUserProviders(ctx context.Context, providers, baseline map[string]config.ProviderConfig, renamed map[string]string) error {
	path, err := userConfigPath(h.clientDeps())
	if err != nil {
		return err
	}
	restored, err := config.RestoreSecrets(config.Config{Providers: providers}, renamed, h.stored()...)
	if err != nil {
		return err
	}
	if err := config.SaveUserConfigProviders(path, restored.Providers, baseline); err != nil {
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
// A mask is rejected: written to disk it would become the stored key every
// later restore matches, and the real key would be lost.
func (h *Host) SaveProviderKey(ctx context.Context, name, key string) error {
	if strutil.IsMasked(key) {
		return config.MaskError("providers." + name + ".api_key")
	}
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

// Config is the session's live config, its secrets masked (C3). A closed
// session keeps answering with its final value (C4).
func (s *Session) Config() config.Config { return config.RedactSecrets(s.state.ConfigSnapshot()) }

// Layers is the session's config layers, with provenance, its secrets masked
// (C3). A closed session keeps answering with its final value (C4).
func (s *Session) Layers() config.Layers { return config.RedactLayers(s.state.Layers()) }

// storedConfigs is what a masked secret can be restored from: the state's
// live config first, because a typed key whose save failed lives only there,
// then the layers' merged config, because they are fresher than the live
// config right after a write. Without a state nothing is stored.
func storedConfigs(state *session.State) []config.Config {
	if state == nil {
		return nil
	}
	out := []config.Config{state.ConfigSnapshot()}
	if layers := state.Layers(); !reflect.DeepEqual(layers, config.Layers{}) {
		out = append(out, layers.Merged)
	}
	return out
}

// stored is storedConfigs over the session's state.
func (s *Session) stored() []config.Config { return storedConfigs(s.state) }

// stored is storedConfigs over the current session's state.
func (h *Host) stored() []config.Config { return storedConfigs(h.currentState()) }

// ApplyConfig installs cfg as the session's config without saving it or
// rebuilding the runtime. A masked secret in cfg is restored to the stored
// secret it stands for; a mask with nothing behind it fails the call and
// changes nothing. A manual mode selection survives, as a reload's does.
func (s *Session) ApplyConfig(ctx context.Context, cfg config.Config) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	restored, err := config.RestoreSecrets(cfg, nil, s.stored()...)
	if err != nil {
		return err
	}
	_, err = s.state.RebaseInteractionConfig(restored)
	return err
}

// ReloadConfig rebuilds the runtime from cfg, its masked secrets restored as
// ApplyConfig restores them. It reports false, and does nothing, when the
// session has no reload path; the error is the rebuild's.
func (s *Session) ReloadConfig(ctx context.Context, cfg config.Config) (bool, error) {
	if s.closed.Load() {
		return false, errSessionClosed
	}
	if s.reloadConfig == nil {
		return false, nil
	}
	restored, err := config.RestoreSecrets(cfg, nil, s.stored()...)
	if err != nil {
		return true, err
	}
	return true, s.reloadConfig(restored)
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
// trust record's hash and the layers. A masked secret in cfg is restored to
// the stored secret it stands for; a mask with nothing behind it fails the
// call and writes nothing.
func (s *Session) SaveProjectConfig(ctx context.Context, cfg config.Config) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	restored, err := config.RestoreSecrets(cfg, nil, s.stored()...)
	if err != nil {
		return err
	}
	if err := config.SaveProjectConfig(s.projectConfigPath(), restored, s.state.Layers()); err != nil {
		return err
	}
	if s.trustRefresh != nil && s.state.WorkingDir != "" {
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
