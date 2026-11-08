package acp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// configInterval is how often a config forwarder compares the session's
// config with the last one it sent (P3 R3).
const configInterval = 50 * time.Millisecond

// SettingsManagerConfig wires one connection's settings handlers.
type SettingsManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// HostLookup resolves the service host of a session's runtime.
	HostLookup func(sessionID string) (*service.Host, bool)
	// Notify sends session_config updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// SettingsManager serves the user's and the session's config, the provider
// and model calls, the sign-ins and the trust prompt for one connection, and
// runs one config forwarder per session a marshal client reads.
type SettingsManager struct {
	lookup     func(sessionID string) (*service.Session, bool)
	hostLookup func(sessionID string) (*service.Host, bool)
	notify     NotifyFunc
	marshal    func() bool

	mu         sync.Mutex
	forwarders map[string]*configForwarder
	closed     bool

	// fences holds this connection's live config forwarders, by session, so a
	// response barrier can confirm their updates are written (P3 R2).
	fences fenceSet
}

// configForwarder tracks one session's config forwarder goroutine.
type configForwarder struct {
	cancel context.CancelFunc
	fence  *fence
}

// NewSettingsManager builds one connection's settings handlers.
func NewSettingsManager(cfg SettingsManagerConfig) *SettingsManager {
	return &SettingsManager{
		lookup:     cfg.Lookup,
		hostLookup: cfg.HostLookup,
		notify:     cfg.Notify,
		marshal:    cfg.Marshal,
		forwarders: map[string]*configForwarder{},
	}
}

// ProviderTemplates handles config/provider_templates.
func (m *SettingsManager) ProviderTemplates(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.HostCallParams
	if err := decodeParams(params, &p, acpwire.MethodProviderTemplates); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	return host.ProviderTemplates(), nil
}

// TrustPrompt handles trust/prompt: the directory whose trust decision is
// pending, "" when none is.
func (m *SettingsManager) TrustPrompt(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.HostCallParams
	if err := decodeParams(params, &p, acpwire.MethodTrustPrompt); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	return acpwire.TrustPromptResult{Dir: host.TrustPrompt()}, nil
}

// TrustDecide handles trust/decide.
func (m *SettingsManager) TrustDecide(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.TrustDecideParams
	if err := decodeParams(params, &p, acpwire.MethodTrustDecide); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := host.DecideTrust(ctx, p.Dir, p.Decision); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// ProbeProvider handles config/probe_provider for a marshal client: it asks
// the endpoint pc names for its model list. p.Provider may carry a literal key
// (P3 R8), so nothing here logs the params or quotes them in an error.
func (m *SettingsManager) ProbeProvider(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ProviderParams
	if err := decodeParams(params, &p, acpwire.MethodProbeProvider); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	pc, err := decodeProvider(p.Provider, acpwire.MethodProbeProvider)
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := cancellable(ctx)
	defer cancel()
	models, err := host.ProbeProvider(probeCtx, p.Name, pc)
	if err != nil {
		return nil, err
	}
	return modelsResult(models)
}

// ProbeModel handles config/probe_model: with Check it answers whether the
// model can be probed at all, and otherwise the model's capabilities.
func (m *SettingsManager) ProbeModel(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ProviderParams
	if err := decodeParams(params, &p, acpwire.MethodProbeModel); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	pc, err := decodeProvider(p.Provider, acpwire.MethodProbeModel)
	if err != nil {
		return nil, err
	}
	if p.Check {
		return acpwire.CanProbeResult{CanProbe: host.CanProbeModel(ctx, p.Name, pc)}, nil
	}
	probeCtx, cancel := cancellable(ctx)
	defer cancel()
	return host.ProbeModel(probeCtx, p.Name, pc, p.Model)
}

// ModelsList handles config/models_list: the cached list for the provider
// called name, when the cache holds a fresh one.
func (m *SettingsManager) ModelsList(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ModelsParams
	if err := decodeParams(params, &p, acpwire.MethodModelsList); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	models, found := host.ListModels(ctx, p.Name)
	res := acpwire.ModelsResult{Found: found}
	if models != nil {
		raw, err := json.Marshal(models)
		if err != nil {
			return nil, err
		}
		res.Models = raw
	}
	return res, nil
}

// ModelsSave handles config/models_save: it caches the models a probe
// reported for the provider called name.
func (m *SettingsManager) ModelsSave(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ModelsParams
	if err := decodeParams(params, &p, acpwire.MethodModelsSave); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	var models []schema.ModelInfo
	if len(p.Models) > 0 {
		if err := json.Unmarshal(p.Models, &models); err != nil {
			return nil, invalidParamsError("%s: malformed models", acpwire.MethodModelsSave)
		}
	}
	if err := host.SaveModels(ctx, p.Name, models); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// ModelsForget handles config/models_forget.
func (m *SettingsManager) ModelsForget(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ModelsParams
	if err := decodeParams(params, &p, acpwire.MethodModelsForget); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := host.ForgetModels(ctx, p.Name); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// ModelsRename handles config/models_rename.
func (m *SettingsManager) ModelsRename(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.RenameModelsParams
	if err := decodeParams(params, &p, acpwire.MethodModelsRename); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := host.RenameModels(ctx, p.From, p.To); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// LoginProvider handles config/login_provider: with Prepare it answers whether
// the browser sign-in can start, and otherwise runs it, reporting the
// authorization URL as this call's progress (decision 7).
func (m *SettingsManager) LoginProvider(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.LoginParams
	if err := decodeParams(params, &p, acpwire.MethodLoginProvider); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	if p.Prepare {
		return struct{}{}, host.PrepareProviderLogin(ctx, p.Name)
	}
	// The sign-in is cancellable, and the progress value is sent on the
	// request's own context so it precedes the response (P3 R2).
	loginCtx, cancel := cancellable(ctx)
	defer cancel()
	show := func(url string) {
		_ = reportProgress(ctx, acpwire.LoginProgress{URL: url})
	}
	if err := host.LoginProvider(loginCtx, p.Name, show); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SetValue handles config/set_value. The value is decoded into the config
// field's own type here, so the file receives it as that type (decision 5).
func (m *SettingsManager) SetValue(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SetValueParams
	if err := decodeParams(params, &p, acpwire.MethodSetValue); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	typ, ok := config.PathType(p.Path)
	if !ok {
		return nil, invalidParamsError("%s: unknown path %q", acpwire.MethodSetValue, p.Path)
	}
	value := reflect.New(typ)
	if err := json.Unmarshal(p.Value, value.Interface()); err != nil {
		return nil, invalidParamsError("%s: malformed value for %q", acpwire.MethodSetValue, p.Path)
	}
	if err := host.SaveUserValue(ctx, p.Path, value.Elem().Interface()); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SaveProviders handles config/save_providers for a marshal client: it writes
// the user config's [providers] section, baseline and renamed as the call
// names them. The entries may carry literal keys (P3 R8).
func (m *SettingsManager) SaveProviders(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SaveProvidersParams
	if err := decodeParams(params, &p, acpwire.MethodSaveProviders); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	if len(p.Providers) == 0 {
		return nil, invalidParamsError("%s: providers is required", acpwire.MethodSaveProviders)
	}
	providers := map[string]config.ProviderConfig{}
	if err := json.Unmarshal(p.Providers, &providers); err != nil {
		return nil, invalidParamsError("%s: malformed providers", acpwire.MethodSaveProviders)
	}
	// A missing baseline is a nil map: nothing was taken from disk, so every
	// entry on disk survives the write.
	var baseline map[string]config.ProviderConfig
	if len(p.Baseline) > 0 {
		baseline = map[string]config.ProviderConfig{}
		if err := json.Unmarshal(p.Baseline, &baseline); err != nil {
			return nil, invalidParamsError("%s: malformed baseline", acpwire.MethodSaveProviders)
		}
	}
	if err := host.SaveUserProviders(ctx, providers, baseline, p.Renamed); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SetProviderKey handles config/set_provider_key for a marshal client. The key
// is a secret (P3 R8), so nothing here logs the params or quotes them.
func (m *SettingsManager) SetProviderKey(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ProviderKeyParams
	if err := decodeParams(params, &p, acpwire.MethodSetProviderKey); err != nil {
		// The decode failure names neither the key nor the frame it came in.
		return nil, invalidParamsError("%s: malformed params", acpwire.MethodSetProviderKey)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := host.SaveProviderKey(ctx, p.Name, p.Key); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SavePresets handles config/save_presets: it writes the user config's
// [models.presets] section, merged against baseline.
func (m *SettingsManager) SavePresets(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SavePresetsParams
	if err := decodeParams(params, &p, acpwire.MethodSavePresets); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	if len(p.Presets) == 0 {
		return nil, invalidParamsError("%s: presets is required", acpwire.MethodSavePresets)
	}
	presets := map[string]routing.ModelPreset{}
	if err := config.UnmarshalPresetsJSON(p.Presets, &presets); err != nil {
		return nil, invalidParamsError("%s: malformed presets", acpwire.MethodSavePresets)
	}
	var baseline map[string]routing.ModelPreset
	if len(p.Baseline) > 0 {
		baseline = map[string]routing.ModelPreset{}
		if err := config.UnmarshalPresetsJSON(p.Baseline, &baseline); err != nil {
			return nil, invalidParamsError("%s: malformed baseline", acpwire.MethodSavePresets)
		}
	}
	if err := host.SaveUserPresets(ctx, presets, baseline); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// PreparePresets handles config/prepare_presets: it proposes presets for the
// models a connected provider reported, and returns the provenance the client
// hands back to config/save_provenance.
func (m *SettingsManager) PreparePresets(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.PreparePresetsParams
	if err := decodeParams(params, &p, acpwire.MethodPreparePresets); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	if len(p.Config) == 0 {
		return nil, invalidParamsError("%s: config is required", acpwire.MethodPreparePresets)
	}
	var cfg config.Config
	if err := config.UnmarshalConfigJSON(p.Config, &cfg); err != nil {
		return nil, invalidParamsError("%s: malformed config", acpwire.MethodPreparePresets)
	}
	var models []schema.ModelInfo
	if len(p.Models) > 0 {
		if err := json.Unmarshal(p.Models, &models); err != nil {
			return nil, invalidParamsError("%s: malformed models", acpwire.MethodPreparePresets)
		}
	}
	prepareCtx, cancel := cancellable(ctx)
	defer cancel()
	presets, setup, err := host.PreparePresets(prepareCtx, cfg, p.Provider, p.BaseURL, models)
	if err != nil {
		return nil, err
	}
	res := acpwire.PreparedPresets{Setup: setup}
	if presets != nil {
		raw, err := json.Marshal(presets)
		if err != nil {
			return nil, err
		}
		res.Presets = raw
	}
	return res, nil
}

// SaveProvenance handles config/save_provenance: it writes the provenance
// sidecar config/prepare_presets returned.
func (m *SettingsManager) SaveProvenance(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ProvenanceParams
	if err := decodeParams(params, &p, acpwire.MethodSaveProvenance); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := host.SaveProvenance(ctx, p.Provenance); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// Close stops every config forwarder. The connection's cleanup calls it.
func (m *SettingsManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	cancels := make([]context.CancelFunc, 0, len(m.forwarders))
	for id, f := range m.forwarders {
		cancels = append(cancels, f.cancel)
		delete(m.forwarders, id)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// host resolves the service host of the session's runtime: the host the
// session's settings calls act through (decision 1). An empty id is invalid
// params and an unknown session is a server error.
func (m *SettingsManager) host(sessionID string) (*service.Host, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, invalidParamsError("sessionId is required")
	}
	host, ok := m.lookupHost(sessionID)
	if !ok || host == nil {
		return nil, serverErrorf("unknown session: %s", sessionID)
	}
	return host, nil
}

// lookupHost resolves a session's service host without reporting why it
// failed.
func (m *SettingsManager) lookupHost(sessionID string) (*service.Host, bool) {
	if m.hostLookup == nil {
		return nil, false
	}
	return m.hostLookup(sessionID)
}

// decodeProvider decodes a provider config a request carries. It never quotes
// the value: the entry may hold a literal key (P3 R8).
func decodeProvider(raw json.RawMessage, method string) (config.ProviderConfig, error) {
	var pc config.ProviderConfig
	if err := json.Unmarshal(raw, &pc); err != nil {
		return config.ProviderConfig{}, invalidParamsError("%s: malformed provider", method)
	}
	return pc, nil
}

// modelsResult encodes a model list into the wire value the models calls
// answer with.
func modelsResult(models []schema.ModelInfo) (acpwire.ModelsResult, error) {
	if models == nil {
		return acpwire.ModelsResult{}, nil
	}
	raw, err := json.Marshal(models)
	if err != nil {
		return acpwire.ModelsResult{}, err
	}
	return acpwire.ModelsResult{Models: raw}, nil
}

// registerSettings registers the new methods on srv, and the two existing ones
// through marshalOr: a client without the capability reaches others[method],
// unchanged.
func registerSettings(srv *Server, settings *SettingsManager, others map[string]Handler) {
	srv.Handle(acpwire.MethodProviderTemplates, settings.ProviderTemplates)
	srv.Handle(acpwire.MethodProbeModel, settings.ProbeModel)
	srv.Handle(acpwire.MethodModelsList, settings.ModelsList)
	srv.Handle(acpwire.MethodModelsSave, settings.ModelsSave)
	srv.Handle(acpwire.MethodModelsForget, settings.ModelsForget)
	srv.Handle(acpwire.MethodModelsRename, settings.ModelsRename)
	srv.Handle(acpwire.MethodLoginProvider, settings.LoginProvider)
	srv.Handle(acpwire.MethodSetValue, settings.SetValue)
	srv.Handle(acpwire.MethodSaveProviders, settings.SaveProviders)
	srv.Handle(acpwire.MethodSavePresets, settings.SavePresets)
	srv.Handle(acpwire.MethodPreparePresets, settings.PreparePresets)
	srv.Handle(acpwire.MethodSaveProvenance, settings.SaveProvenance)
	srv.Handle(acpwire.MethodTrustPrompt, settings.TrustPrompt)
	srv.Handle(acpwire.MethodTrustDecide, settings.TrustDecide)
	srv.Handle(acpwire.MethodProbeProvider,
		marshalOr(srv.marshalClient, settings.ProbeProvider, others[acpwire.MethodProbeProvider]))
	srv.Handle(acpwire.MethodSetProviderKey,
		marshalOr(srv.marshalClient, settings.SetProviderKey, others[acpwire.MethodSetProviderKey]))
}
