package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// TrustPrompt handles trust/prompt: the trust prompt's whole answer, the
// directory whose trust decision is pending and whether a trust applies in
// place.
func (m *SettingsManager) TrustPrompt(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.HostCallParams
	if err := decodeParams(params, &p, acpwire.MethodTrustPrompt); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	return host.TrustPrompt(), nil
}

// trustPromptForOthers answers trust/prompt for a client without the marshal
// capability: no prompt.
func trustPromptForOthers(ctx context.Context, params json.RawMessage) (any, error) {
	return acpwire.TrustPromptResult{}, nil
}

// trustDecideForOthers refuses trust/decide for a client without the marshal
// capability.
func trustDecideForOthers(ctx context.Context, params json.RawMessage) (any, error) {
	return nil, &jsonRPCError{
		Code:    invalidRequest,
		Message: "trust/decide serves marshal clients only",
	}
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

// SessionConfig handles session/config: it returns the session's config,
// layers and tools, and for a marshal client it first makes sure the session's
// config forwarder is running, so the config arrives as an update before this
// call's response (P3 R2, R3).
func (m *SettingsManager) SessionConfig(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ConfigParams
	if err := decodeParams(params, &p, acpwire.MethodSessionConfig); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if m.isMarshal() {
		m.ensureForwarder(p.SessionID)
	}
	return sessionConfig(svc)
}

// ApplyConfig handles session/apply_config: it installs cfg as the session's
// config, without saving it or rebuilding the runtime.
func (m *SettingsManager) ApplyConfig(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ConfigParams
	if err := decodeParams(params, &p, acpwire.MethodApplyConfig); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	cfg, err := decodeConfig(p.Config, acpwire.MethodApplyConfig)
	if err != nil {
		return nil, err
	}
	if err := svc.ApplyConfig(ctx, cfg); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// ReloadConfig handles session/reload_config: the flag travels with the
// error, so a client without a reload path is not told the reload failed
// (decision 6).
func (m *SettingsManager) ReloadConfig(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ConfigParams
	if err := decodeParams(params, &p, acpwire.MethodReloadConfig); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	cfg, err := decodeConfig(p.Config, acpwire.MethodReloadConfig)
	if err != nil {
		return nil, err
	}
	reloaded, reloadErr := svc.ReloadConfig(ctx, cfg)
	return reloadResult(reloaded, reloadErr), nil
}

// MCPReconnect handles session/mcp_reconnect: it rebuilds the runtime from the
// session's current config, so an MCP server that has just been signed in to
// connects. The flag travels with the error, as ReloadConfig's does.
func (m *SettingsManager) MCPReconnect(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ConfigParams
	if err := decodeParams(params, &p, acpwire.MethodMCPReconnect); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	reloaded, reloadErr := svc.ReconnectMCP(ctx)
	return reloadResult(reloaded, reloadErr), nil
}

// SaveProjectConfig handles session/save_project_config: it writes cfg's
// project-scoped values to the session's project config.
func (m *SettingsManager) SaveProjectConfig(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ConfigParams
	if err := decodeParams(params, &p, acpwire.MethodSaveProjectConfig); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	cfg, err := decodeConfig(p.Config, acpwire.MethodSaveProjectConfig)
	if err != nil {
		return nil, err
	}
	if err := svc.SaveProjectConfig(ctx, cfg); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SaveVerifyCommands handles session/save_verify_commands: it writes the plan
// run's verify commands to the session's project config.
func (m *SettingsManager) SaveVerifyCommands(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.VerifyCommandsParams
	if err := decodeParams(params, &p, acpwire.MethodSaveVerifyCommands); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := svc.SaveVerifyCommands(ctx, p.Build, p.Test); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// MCPLogin handles session/mcp_login: with Prepare it answers whether the
// browser sign-in can start, and otherwise runs it, reporting the
// authorization URL as this call's progress, as LoginProvider does
// (decision 7).
func (m *SettingsManager) MCPLogin(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.LoginParams
	if err := decodeParams(params, &p, acpwire.MethodMCPLogin); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if p.Prepare {
		return struct{}{}, svc.PrepareMCPLogin(ctx, p.Name)
	}
	// The sign-in is cancellable, and the progress value is sent on the
	// request's own context so it precedes the response (P3 R2).
	loginCtx, cancel := cancellable(ctx)
	defer cancel()
	show := func(url string) {
		_ = reportProgress(ctx, acpwire.LoginProgress{URL: url})
	}
	if err := svc.LoginMCP(loginCtx, p.Name, show); err != nil {
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

// configEnvelope is the session/update body a session_config update travels
// in. It is a dedicated struct, not SessionUpdateParams, because the latter's
// Update is a map[string]any: a config carried through a map would not keep
// its typed fields.
type configEnvelope struct {
	SessionID string                      `json:"sessionId"`
	Update    acpwire.SessionConfigUpdate `json:"update"`
}

// fenceSession waits until the session's config forwarder has sent the config
// as it is now, if it differs from the last one sent (P3 R2).
func (m *SettingsManager) fenceSession(ctx context.Context, sessionID string) {
	m.fences.wait(ctx, sessionID)
}

// isMarshal reports whether the connection's client declared the marshal
// capability.
func (m *SettingsManager) isMarshal() bool {
	return m.marshal != nil && m.marshal()
}

// service resolves a session's client-seam service. An empty id is invalid
// params and an unknown session is a server error.
func (m *SettingsManager) service(sessionID string) (*service.Session, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, invalidParamsError("sessionId is required")
	}
	sess, ok := m.lookupService(sessionID)
	if !ok || sess == nil {
		return nil, serverErrorf("unknown session: %s", sessionID)
	}
	return sess, nil
}

// lookupService resolves a session's service without reporting why it failed.
func (m *SettingsManager) lookupService(sessionID string) (*service.Session, bool) {
	if m.lookup == nil {
		return nil, false
	}
	return m.lookup(sessionID)
}

// ensureForwarder starts one config forwarder for a session, unless the
// connection is closed or the session already has one. Its fence is registered
// before its goroutine starts, so a barrier that follows cannot miss it.
func (m *SettingsManager) ensureForwarder(sessionID string) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if _, ok := m.forwarders[sessionID]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &configForwarder{cancel: cancel, fence: newFence()}
	m.forwarders[sessionID] = f
	m.fences.add(sessionID, f.fence)
	m.mu.Unlock()

	go m.runForwarder(sessionID, f, ctx)
}

// runForwarder keeps the session's config mirror current: every
// configInterval, and whenever its fence is asked, it reads the config and
// sends it when the encoding differs from the last one it sent. Config
// changes without a broker event this manager sees (a host write, a reload),
// so polling is what keeps the mirror current. It stops when the connection
// closes or the session goes away.
func (m *SettingsManager) runForwarder(sessionID string, f *configForwarder, ctx context.Context) {
	defer func() {
		m.mu.Lock()
		delete(m.forwarders, sessionID)
		m.mu.Unlock()
		m.fences.remove(sessionID, f.fence)
		f.fence.close()
	}()

	// sent says a config has been sent on this connection, so last is the
	// encoding to compare against.
	var (
		last []byte
		sent bool
	)
	// send reads the config, encodes it and sends it when the encoding differs
	// from the last one sent. It reports whether the session still exists.
	send := func() bool {
		// Resolve the service on every pass, so a runtime replaced on load or
		// resume is followed.
		svc, ok := m.lookupService(sessionID)
		if !ok || svc == nil {
			return false
		}
		cfg, err := sessionConfig(svc)
		if err != nil {
			return true
		}
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return true
		}
		if sent && bytes.Equal(encoded, last) {
			return true
		}
		if err := m.notify(acpwire.MethodSessionUpdate, configEnvelope{
			SessionID: sessionID,
			Update: acpwire.SessionConfigUpdate{
				Kind:   acpwire.UpdateSessionConfig,
				Config: cfg,
			},
		}); err != nil {
			return true
		}
		last = encoded
		sent = true
		return true
	}

	ticker := time.NewTicker(configInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		case answer := <-f.fence.requests():
			alive := send()
			close(answer)
			if !alive {
				return
			}
		}
	}
}

// sessionConfig is what one pass reads: the session's config, layers and
// tools.
func sessionConfig(svc *service.Session) (acpwire.SessionConfig, error) {
	cfg, err := json.Marshal(svc.Config())
	if err != nil {
		return acpwire.SessionConfig{}, err
	}
	layers, err := json.Marshal(svc.Layers())
	if err != nil {
		return acpwire.SessionConfig{}, err
	}
	tools, ready := svc.Tools()
	return acpwire.SessionConfig{Config: cfg, Layers: layers, Tools: tools, ToolsReady: ready}, nil
}

// reloadResult carries a reload's flag with its error: a JSON-RPC error
// response could not carry both (decision 6).
func reloadResult(reloaded bool, err error) acpwire.ReloadResult {
	res := acpwire.ReloadResult{Reloaded: reloaded}
	if err == nil {
		return res
	}
	var coded *acpwire.Error
	if errors.As(err, &coded) {
		res.Error = coded
	} else {
		res.Error = &acpwire.Error{Message: err.Error()}
	}
	return res
}

// decodeConfig decodes a config a session call carries. A missing config is
// invalid params: every one of these calls has something to install.
func decodeConfig(raw json.RawMessage, method string) (config.Config, error) {
	var cfg config.Config
	if len(raw) == 0 {
		return config.Config{}, invalidParamsError("%s: config is required", method)
	}
	if err := config.UnmarshalConfigJSON(raw, &cfg); err != nil {
		return config.Config{}, invalidParamsError("%s: malformed config", method)
	}
	return cfg, nil
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
// unchanged. The trust calls are marshalOr's too, but a client without the
// capability reaches the fixed handlers below: no prompt, and a refusal
// (P4 D8, decision 3).
func registerSettings(srv *Server, settings *SettingsManager, others map[string]Handler) {
	srv.Handle(acpwire.MethodProviderTemplates, settings.ProviderTemplates)
	srv.Handle(acpwire.MethodProbeModel, settings.ProbeModel)
	srv.Handle(acpwire.MethodModelsList, settings.ModelsList)
	srv.Handle(acpwire.MethodModelsSave, settings.ModelsSave)
	srv.Handle(acpwire.MethodModelsForget, settings.ModelsForget)
	srv.Handle(acpwire.MethodModelsRename, settings.ModelsRename)
	srv.Handle(acpwire.MethodLoginProvider, settingsFailureOf(settings.LoginProvider))
	srv.Handle(acpwire.MethodSetValue, settingsFailureOf(settings.SetValue))
	srv.Handle(acpwire.MethodSaveProviders, settingsFailureOf(settings.SaveProviders))
	srv.Handle(acpwire.MethodSavePresets, settings.SavePresets)
	srv.Handle(acpwire.MethodPreparePresets, settings.PreparePresets)
	srv.Handle(acpwire.MethodSaveProvenance, settings.SaveProvenance)
	srv.Handle(acpwire.MethodTrustPrompt,
		marshalOr(srv.marshalClient, settings.TrustPrompt, trustPromptForOthers))
	srv.Handle(acpwire.MethodTrustDecide,
		settingsFailureOf(marshalOr(srv.marshalClient, settings.TrustDecide, trustDecideForOthers)))
	srv.Handle(acpwire.MethodSessionConfig, settings.SessionConfig)
	srv.Handle(acpwire.MethodApplyConfig, settingsFailureOf(settings.ApplyConfig))
	srv.Handle(acpwire.MethodReloadConfig, settings.ReloadConfig)
	srv.Handle(acpwire.MethodMCPReconnect, settings.MCPReconnect)
	srv.Handle(acpwire.MethodSaveProjectConfig, settingsFailureOf(settings.SaveProjectConfig))
	srv.Handle(acpwire.MethodSaveVerifyCommands, settings.SaveVerifyCommands)
	srv.Handle(acpwire.MethodMCPLogin, settingsFailureOf(settings.MCPLogin))
	srv.Handle(acpwire.MethodProbeProvider,
		settingsFailureOf(marshalOr(srv.marshalClient, settings.ProbeProvider, others[acpwire.MethodProbeProvider])))
	srv.Handle(acpwire.MethodSetProviderKey,
		settingsFailureOf(marshalOr(srv.marshalClient, settings.SetProviderKey, others[acpwire.MethodSetProviderKey])))
}

// settingsFailureOf wraps one settings handler so a failure keeps the sentence
// the client shows. The server writes every other error as its code's fixed
// string (F-SEC-37), so a plain error would arrive as "internal error"; these
// sentences are client-facing, and the settings browser reads them.
func settingsFailureOf(h Handler) Handler {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		result, err := h(ctx, params)
		return result, settingsFailure(err)
	}
}

// settingsFailure is settingsFailureOf's conversion: an *acpwire.Error and a
// JSON-RPC error travel as they are, so their code and message survive, and any
// other error travels as an *acpwire.Error carrying its message, the form
// reloadResult gives a reload's error (decision 6).
func settingsFailure(err error) error {
	if err == nil {
		return nil
	}
	var coded *acpwire.Error
	if errors.As(err, &coded) {
		return err
	}
	var rpcErr *jsonRPCError
	if errors.As(err, &rpcErr) {
		return err
	}
	// A cancelled call keeps its own code: requestCancelled is what the client
	// acts on (P3 R2).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &acpwire.Error{Message: err.Error()}
}
