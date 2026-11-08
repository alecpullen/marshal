package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// hostReadTimeout bounds a host read's first call, and the config mirror's.
const hostReadTimeout = 5 * time.Second

// The host calls. Every one names the client's current session, whose runtime
// serves it (decision 1), so before the first Attach each of them fails with
// errNoSession; the two reads answer nothing instead, and ask again on the next
// read. The session calls are the other half of this file.

// ProviderTemplates is the provider catalog, sorted by ID. It sends
// config/provider_templates on its first read and keeps the answer: the
// catalog does not change while the host runs (decision 2).
func (h *Host) ProviderTemplates() []acpwire.ProviderTemplate {
	h.mu.Lock()
	if h.templatesSet {
		templates := h.templates
		h.mu.Unlock()
		return templates
	}
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), hostReadTimeout)
	defer cancel()
	sessionID, err := h.currentID()
	if err != nil {
		return nil
	}
	var templates []acpwire.ProviderTemplate
	if err := h.conn.Call(ctx, acpwire.MethodProviderTemplates, acpwire.HostCallParams{SessionID: sessionID}, &templates); err != nil {
		// The read is not kept, so the next one asks again.
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.templatesSet {
		// A concurrent first read answered already, and its answer stands.
		return h.templates
	}
	h.templates = templates
	h.templatesSet = true
	return templates
}

// TrustPrompt is the directory whose trust decision is pending, "" when none
// is. It sends trust/prompt on its first read and keeps the answer, which does
// not change while the host runs (decision 2).
func (h *Host) TrustPrompt() string {
	h.mu.Lock()
	if h.trustSet {
		dir := h.trustPrompt
		h.mu.Unlock()
		return dir
	}
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), hostReadTimeout)
	defer cancel()
	sessionID, err := h.currentID()
	if err != nil {
		return ""
	}
	var result acpwire.TrustPromptResult
	if err := h.conn.Call(ctx, acpwire.MethodTrustPrompt, acpwire.HostCallParams{SessionID: sessionID}, &result); err != nil {
		// The read is not kept, so the next one asks again.
		return ""
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.trustSet {
		// A concurrent first read answered already, and its answer stands.
		return h.trustPrompt
	}
	h.trustPrompt = result.Dir
	h.trustSet = true
	return result.Dir
}

// DecideTrust answers the trust prompt for dir.
func (h *Host) DecideTrust(ctx context.Context, dir string, decision acpwire.TrustDecision) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	params := acpwire.TrustDecideParams{SessionID: sessionID, Dir: dir, Decision: decision}
	return h.conn.Call(ctx, acpwire.MethodTrustDecide, params, nil)
}

// ProbeProvider lists the models pc's endpoint reports.
func (h *Host) ProbeProvider(ctx context.Context, name string, pc config.ProviderConfig) ([]schema.ModelInfo, error) {
	sessionID, err := h.currentID()
	if err != nil {
		return nil, err
	}
	params, err := providerParams(sessionID, name, pc)
	if err != nil {
		return nil, err
	}
	var result acpwire.ModelsResult
	if err := h.conn.Call(ctx, acpwire.MethodProbeProvider, params, &result); err != nil {
		return nil, err
	}
	var models []schema.ModelInfo
	if len(result.Models) > 0 {
		if err := json.Unmarshal(result.Models, &models); err != nil {
			return nil, err
		}
	}
	return models, nil
}

// CanProbeModel reports whether ProbeModel can tell anything about a model of
// pc, and false on any error.
func (h *Host) CanProbeModel(ctx context.Context, name string, pc config.ProviderConfig) bool {
	sessionID, err := h.currentID()
	if err != nil {
		return false
	}
	params, err := providerParams(sessionID, name, pc)
	if err != nil {
		return false
	}
	params.Check = true
	var result acpwire.CanProbeResult
	if err := h.conn.Call(ctx, acpwire.MethodProbeModel, params, &result); err != nil {
		return false
	}
	return result.CanProbe
}

// ProbeModel probes one model's capabilities.
func (h *Host) ProbeModel(ctx context.Context, name string, pc config.ProviderConfig, model string) (acpwire.ModelCapabilities, error) {
	sessionID, err := h.currentID()
	if err != nil {
		return acpwire.ModelCapabilities{}, err
	}
	params, err := providerParams(sessionID, name, pc)
	if err != nil {
		return acpwire.ModelCapabilities{}, err
	}
	params.Model = model
	var caps acpwire.ModelCapabilities
	if err := h.conn.Call(ctx, acpwire.MethodProbeModel, params, &caps); err != nil {
		return acpwire.ModelCapabilities{}, err
	}
	return caps, nil
}

// ListModels returns the cached model list for the provider called name and
// whether the cache held one, or nil and false on any error.
func (h *Host) ListModels(ctx context.Context, name string) ([]schema.ModelInfo, bool) {
	sessionID, err := h.currentID()
	if err != nil {
		return nil, false
	}
	params := acpwire.ModelsParams{SessionID: sessionID, Name: name}
	var result acpwire.ModelsResult
	if err := h.conn.Call(ctx, acpwire.MethodModelsList, params, &result); err != nil {
		return nil, false
	}
	var models []schema.ModelInfo
	if len(result.Models) > 0 {
		if err := json.Unmarshal(result.Models, &models); err != nil {
			return nil, false
		}
	}
	return models, result.Found
}

// SaveModels caches models for the provider called name.
func (h *Host) SaveModels(ctx context.Context, name string, models []schema.ModelInfo) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(models)
	if err != nil {
		return err
	}
	params := acpwire.ModelsParams{SessionID: sessionID, Name: name, Models: raw}
	return h.conn.Call(ctx, acpwire.MethodModelsSave, params, nil)
}

// ForgetModels drops the cached list for the provider called name.
func (h *Host) ForgetModels(ctx context.Context, name string) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	params := acpwire.ModelsParams{SessionID: sessionID, Name: name}
	return h.conn.Call(ctx, acpwire.MethodModelsForget, params, nil)
}

// RenameModels moves the cached list of the provider called from to the name
// to.
func (h *Host) RenameModels(ctx context.Context, from, to string) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	params := acpwire.RenameModelsParams{SessionID: sessionID, From: from, To: to}
	return h.conn.Call(ctx, acpwire.MethodModelsRename, params, nil)
}

// PrepareProviderLogin checks that the browser sign-in for the provider called
// name can start.
func (h *Host) PrepareProviderLogin(ctx context.Context, name string) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	params := acpwire.LoginParams{SessionID: sessionID, Name: name, Prepare: true}
	return h.conn.Call(ctx, acpwire.MethodLoginProvider, params, nil)
}

// LoginProvider runs the browser sign-in for the provider called name, handing
// each progress value's URL to show. show runs on the connection's read
// goroutine as the value arrives, before the response does, so it must return
// promptly and must not call back into this connection.
func (h *Host) LoginProvider(ctx context.Context, name string, show func(url string)) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	params := acpwire.LoginParams{SessionID: sessionID, Name: name}
	progress := func(value json.RawMessage) {
		var p acpwire.LoginProgress
		if err := json.Unmarshal(value, &p); err != nil {
			return
		}
		show(p.URL)
	}
	return h.conn.CallWithProgress(ctx, acpwire.MethodLoginProvider, params, nil, progress)
}

// SaveUserValue writes value at the dotted TOML path tomlPath of the user
// config. A nil value, or one whose dynamic type is a pointer, is refused here
// rather than sent: SaveUserConfigValue refuses both in process (decision 5).
func (h *Host) SaveUserValue(ctx context.Context, tomlPath string, value any) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	if value == nil || reflect.TypeOf(value).Kind() == reflect.Pointer {
		return fmt.Errorf("rpc: %s cannot write a %T value", acpwire.MethodSetValue, value)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	params := acpwire.SetValueParams{SessionID: sessionID, Path: tomlPath, Value: raw}
	return h.conn.Call(ctx, acpwire.MethodSetValue, params, nil)
}

// SaveUserProviders writes the user config's [providers] section, credentials
// included, merged against baseline.
func (h *Host) SaveUserProviders(ctx context.Context, providers, baseline map[string]config.ProviderConfig, renamed map[string]string) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	// A missing baseline sends nothing, which the agent reads as the nil map
	// that keeps every entry on disk.
	var baselineRaw json.RawMessage
	if baseline != nil {
		if baselineRaw, err = json.Marshal(baseline); err != nil {
			return err
		}
	}
	params := acpwire.SaveProvidersParams{
		SessionID: sessionID,
		Providers: raw,
		Baseline:  baselineRaw,
		Renamed:   renamed,
	}
	return h.conn.Call(ctx, acpwire.MethodSaveProviders, params, nil)
}

// SaveUserPresets writes the user config's [models.presets] section, merged
// against baseline as SaveUserProviders is.
func (h *Host) SaveUserPresets(ctx context.Context, presets, baseline map[string]routing.ModelPreset) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(presets)
	if err != nil {
		return err
	}
	// A missing baseline sends nothing, which the agent reads as the nil map
	// that keeps every preset on disk.
	var baselineRaw json.RawMessage
	if baseline != nil {
		if baselineRaw, err = json.Marshal(baseline); err != nil {
			return err
		}
	}
	params := acpwire.SavePresetsParams{SessionID: sessionID, Presets: raw, Baseline: baselineRaw}
	return h.conn.Call(ctx, acpwire.MethodSavePresets, params, nil)
}

// SaveProviderKey stores key as the literal API key of the provider called
// name.
func (h *Host) SaveProviderKey(ctx context.Context, name, key string) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	params := acpwire.ProviderKeyParams{SessionID: sessionID, Name: name, Key: key}
	return h.conn.Call(ctx, acpwire.MethodSetProviderKey, params, nil)
}

// PreparePresets proposes presets for the models a connected provider
// reported, and returns them with the provenance sidecar they came with.
func (h *Host) PreparePresets(ctx context.Context, cfg config.Config, provider, baseURL string, models []schema.ModelInfo) (map[string]routing.ModelPreset, acpwire.PresetSetup, error) {
	sessionID, err := h.currentID()
	if err != nil {
		return nil, acpwire.PresetSetup{}, err
	}
	cfgRaw, err := json.Marshal(cfg)
	if err != nil {
		return nil, acpwire.PresetSetup{}, err
	}
	var modelsRaw json.RawMessage
	if models != nil {
		if modelsRaw, err = json.Marshal(models); err != nil {
			return nil, acpwire.PresetSetup{}, err
		}
	}
	params := acpwire.PreparePresetsParams{
		SessionID: sessionID,
		Config:    cfgRaw,
		Provider:  provider,
		BaseURL:   baseURL,
		Models:    modelsRaw,
	}
	var result acpwire.PreparedPresets
	if err := h.conn.Call(ctx, acpwire.MethodPreparePresets, params, &result); err != nil {
		return nil, acpwire.PresetSetup{}, err
	}
	if len(result.Presets) == 0 {
		return nil, result.Setup, nil
	}
	// UnmarshalPresetsJSON restores each preset's Pricing to a
	// *pricing.ModelPricing, which a JSON decode into `any` cannot.
	presets := map[string]routing.ModelPreset{}
	if err := config.UnmarshalPresetsJSON(result.Presets, &presets); err != nil {
		return nil, result.Setup, err
	}
	return presets, result.Setup, nil
}

// SaveProvenance writes the provenance sidecar PreparePresets returned.
func (h *Host) SaveProvenance(ctx context.Context, provenance json.RawMessage) error {
	sessionID, err := h.currentID()
	if err != nil {
		return err
	}
	params := acpwire.ProvenanceParams{SessionID: sessionID, Provenance: provenance}
	return h.conn.Call(ctx, acpwire.MethodSaveProvenance, params, nil)
}

// providerParams is the params of a probe for pc. The entry may carry a
// literal key (P3 R8), so a marshal failure is reported without the value.
func providerParams(sessionID, name string, pc config.ProviderConfig) (acpwire.ProviderParams, error) {
	raw, err := json.Marshal(pc)
	if err != nil {
		return acpwire.ProviderParams{}, err
	}
	return acpwire.ProviderParams{SessionID: sessionID, Name: name, Provider: raw}, nil
}

// The session calls. The three reads answer from the config mirror, which one
// session/config read fills; the rest send their session methods.

// configMirror is the session's mirror of its config, layers and tools
// (decision 3): one entry, created by its first read and replaced by each
// session_config update.
type configMirror struct {
	value acpwire.SessionConfig
	// cfg is value.Config decoded, with each preset's Pricing restored, so a
	// read does not decode it again.
	cfg config.Config
	// updated says an update arrived, so the first read's result, which the
	// agent computed before sending it, is stale.
	updated bool
	// ready is closed once the first read's call has settled, and err is its
	// outcome, so a caller that arrives while the call is in flight waits for
	// it instead of sending a second session/config.
	ready chan struct{}
	err   error
}

// settle records the first read's outcome and releases every caller waiting
// on it.
func (c *configMirror) settle(err error) {
	c.err = err
	close(c.ready)
}

// Config is the session's live config, its secrets masked: no I/O once the
// mirror holds a value, and never a read the agent has to answer. It is a
// clone, so a caller that changes what it got cannot change the mirror.
func (s *Session) Config() config.Config {
	ctx, cancel := context.WithTimeout(context.Background(), hostReadTimeout)
	defer cancel()
	entry, err := s.configEntry(ctx)
	if err != nil {
		return config.Config{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return config.Clone(entry.cfg)
}

// Layers is the session's config layers, with provenance: no I/O once the
// mirror holds a value. The mirror keeps their raw value and each read decodes
// it, so a caller that changes what it got cannot change the mirror.
func (s *Session) Layers() config.Layers {
	ctx, cancel := context.WithTimeout(context.Background(), hostReadTimeout)
	defer cancel()
	entry, err := s.configEntry(ctx)
	if err != nil {
		return config.Layers{}
	}
	s.mu.Lock()
	raw := entry.value.Layers
	s.mu.Unlock()

	var layers config.Layers
	if err := config.UnmarshalLayersJSON(raw, &layers); err != nil {
		return config.Layers{}
	}
	return layers
}

// Tools names every tool the session's registry holds, sorted, and reports
// false while it has no registry: no I/O once the mirror holds a value. The
// slice is a copy, so a caller that changes it cannot change the mirror.
func (s *Session) Tools() ([]string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), hostReadTimeout)
	defer cancel()
	entry, err := s.configEntry(ctx)
	if err != nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), entry.value.Tools...), entry.value.ToolsReady
}

// configEntry returns the config mirror, fetching it with ctx when this is its
// first read. On a failed read the entry is removed, so the next read asks
// again, as a failed Attach's is.
func (s *Session) configEntry(ctx context.Context) (*configMirror, error) {
	s.mu.Lock()
	if entry := s.config; entry != nil {
		s.mu.Unlock()
		select {
		case <-entry.ready:
			if entry.err != nil {
				return nil, entry.err
			}
			return entry, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// The entry is installed before the call goes out, so an update that
	// arrives while the call waits is applied instead of dropped.
	entry := &configMirror{ready: make(chan struct{})}
	s.config = entry
	s.mu.Unlock()

	var value acpwire.SessionConfig
	params := acpwire.ConfigParams{SessionID: s.id}
	if err := s.conn.Call(ctx, acpwire.MethodSessionConfig, params, &value); err != nil {
		// The entry goes even when an update arrived first: a later read asks
		// again, where keeping it would answer every later read with this
		// error for good.
		s.mu.Lock()
		if s.config == entry {
			s.config = nil
		}
		s.mu.Unlock()
		entry.settle(err)
		return nil, err
	}

	if err := s.installConfig(entry, value); err != nil {
		// A value that does not decode is not kept either: the next read asks
		// again rather than answering with a config the mirror cannot hold.
		s.mu.Lock()
		if s.config == entry {
			s.config = nil
		}
		s.mu.Unlock()
		entry.settle(err)
		return nil, err
	}
	entry.settle(nil)
	return entry, nil
}

// installConfig sets the mirror from session/config's result, unless an update
// already arrived: that update is newer than the result the agent computed
// before sending it. It reports the error of a result whose config does not
// decode, which is not installed.
func (s *Session) installConfig(entry *configMirror, value acpwire.SessionConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config != entry || entry.updated {
		return nil
	}
	var cfg config.Config
	if err := config.UnmarshalConfigJSON(value.Config, &cfg); err != nil {
		return err
	}
	entry.value = value
	entry.cfg = cfg
	return nil
}

// applyConfig replaces the mirror with an update's value and fires Changed. An
// update for a session that has not read its config is dropped: the first read
// asks the agent, and gets it. It runs on the connection's read goroutine and
// never blocks on it.
func (s *Session) applyConfig(value acpwire.SessionConfig) {
	var cfg config.Config
	if err := config.UnmarshalConfigJSON(value.Config, &cfg); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config == nil {
		return
	}
	s.config.value = value
	s.config.cfg = cfg
	s.config.updated = true
	s.signalLocked()
}

// ApplyConfig installs cfg as the session's config, without saving it or
// rebuilding the runtime.
func (s *Session) ApplyConfig(ctx context.Context, cfg config.Config) error {
	params, err := configParams(s.id, cfg)
	if err != nil {
		return err
	}
	return s.conn.Call(ctx, acpwire.MethodApplyConfig, params, nil)
}

// ReloadConfig rebuilds the runtime from cfg. It reports false, and does
// nothing, when the session has no reload path; the reload's own error travels
// with the flag (decision 6).
func (s *Session) ReloadConfig(ctx context.Context, cfg config.Config) (bool, error) {
	params, err := configParams(s.id, cfg)
	if err != nil {
		return false, err
	}
	return s.reloadResult(ctx, acpwire.MethodReloadConfig, params)
}

// ReconnectMCP rebuilds the runtime from the session's current config, so an
// MCP server that has just been signed in to connects. The flag travels with
// the error, as ReloadConfig's does.
func (s *Session) ReconnectMCP(ctx context.Context) (bool, error) {
	params := acpwire.ConfigParams{SessionID: s.id}
	return s.reloadResult(ctx, acpwire.MethodMCPReconnect, params)
}

// reloadResult sends one of the two reload calls and splits its result into
// the flag and the error the agent reported with it.
func (s *Session) reloadResult(ctx context.Context, method string, params acpwire.ConfigParams) (bool, error) {
	var result acpwire.ReloadResult
	if err := s.conn.Call(ctx, method, params, &result); err != nil {
		return false, err
	}
	if result.Error != nil {
		return result.Reloaded, result.Error
	}
	return result.Reloaded, nil
}

// SaveProjectConfig writes cfg's project-scoped values to the session's
// project config.
func (s *Session) SaveProjectConfig(ctx context.Context, cfg config.Config) error {
	params, err := configParams(s.id, cfg)
	if err != nil {
		return err
	}
	return s.conn.Call(ctx, acpwire.MethodSaveProjectConfig, params, nil)
}

// SaveVerifyCommands writes the plan run's verify commands to the session's
// project config.
func (s *Session) SaveVerifyCommands(ctx context.Context, build, test string) error {
	params := acpwire.VerifyCommandsParams{SessionID: s.id, Build: build, Test: test}
	return s.conn.Call(ctx, acpwire.MethodSaveVerifyCommands, params, nil)
}

// PrepareMCPLogin checks that the browser sign-in for the MCP server called
// server can start.
func (s *Session) PrepareMCPLogin(ctx context.Context, server string) error {
	params := acpwire.LoginParams{SessionID: s.id, Name: server, Prepare: true}
	return s.conn.Call(ctx, acpwire.MethodMCPLogin, params, nil)
}

// LoginMCP runs the browser sign-in for the MCP server called server, handing
// each progress value's URL to show. show runs on the connection's read
// goroutine as the value arrives, before the response does, so it must return
// promptly and must not call back into this connection.
func (s *Session) LoginMCP(ctx context.Context, server string, show func(url string)) error {
	params := acpwire.LoginParams{SessionID: s.id, Name: server}
	progress := func(value json.RawMessage) {
		var p acpwire.LoginProgress
		if err := json.Unmarshal(value, &p); err != nil {
			return
		}
		show(p.URL)
	}
	return s.conn.CallWithProgress(ctx, acpwire.MethodMCPLogin, params, nil, progress)
}

// configParams is the params of a session call that carries cfg.
func configParams(sessionID string, cfg config.Config) (acpwire.ConfigParams, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return acpwire.ConfigParams{}, err
	}
	return acpwire.ConfigParams{SessionID: sessionID, Config: raw}, nil
}
