package service

import (
	"context"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/schema"
)

// currentState returns the current session's state, or nil when the host has
// none.
func (h *Host) currentState() *session.State {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil {
		return nil
	}
	return h.current.state
}

// modelCacheDir is Deps.ModelCacheDir: the directory the TUI used to pass to
// every cache call.
func (h *Host) modelCacheDir() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deps.ModelCacheDir
}

// client builds the provider client a probe uses. Name, pc and the resolver
// are the caller's; the data directory and the thinking budget margin are the
// runtime's. pc's masked key is restored to the stored key it stands for; a
// mask with nothing behind it drops the key, so the probe runs unkeyed as an
// unconfigured provider's does.
func (h *Host) client(name string, pc config.ProviderConfig) (provider.Provider, error) {
	margin := 0
	if st := h.currentState(); st != nil {
		margin = st.ConfigSnapshot().Agent.ThinkingBudgetMargin
	}
	if restored, ok := config.RestoreProviderKey(name, pc, nil, h.stored()...); ok {
		pc = restored
	} else {
		pc.APIKey = ""
	}
	return provider.NewFromConfigWithAuth(name, pc, h.modelCacheDir(), margin, "", h.AuthResolver())
}

// ProviderTemplates lists the provider catalog, sorted by ID, each with every
// field.
func (h *Host) ProviderTemplates() []acpwire.ProviderTemplate {
	templates := provider.All()
	out := make([]acpwire.ProviderTemplate, 0, len(templates))
	for _, tpl := range templates {
		out = append(out, templateWire(tpl))
	}
	return out
}

// ProbeProvider lists the models pc's endpoint reports.
func (h *Host) ProbeProvider(ctx context.Context, name string, pc config.ProviderConfig) ([]schema.ModelInfo, error) {
	p, err := h.client(name, pc)
	if err != nil {
		return nil, err
	}
	return p.Models(ctx)
}

// CanProbeModel reports whether ProbeModel can tell anything about a model of
// pc.
func (h *Host) CanProbeModel(ctx context.Context, name string, pc config.ProviderConfig) bool {
	p, err := h.client(name, pc)
	if err != nil {
		return false
	}
	_, ok := p.(provider.CapabilityProber)
	return ok
}

// ProbeModel probes one model's capabilities.
func (h *Host) ProbeModel(ctx context.Context, name string, pc config.ProviderConfig, model string) (acpwire.ModelCapabilities, error) {
	p, err := h.client(name, pc)
	if err != nil {
		return acpwire.ModelCapabilities{}, err
	}
	prober, ok := p.(provider.CapabilityProber)
	if !ok {
		return acpwire.ModelCapabilities{}, nil
	}
	return capsWire(prober.ProbeCapabilities(ctx, model)), nil
}

// ListModels returns the cached list for name when the cache holds a fresh one
// for the provider's current config.
func (h *Host) ListModels(ctx context.Context, name string) ([]schema.ModelInfo, bool) {
	dir := h.modelCacheDir()
	if dir == "" {
		return nil, false
	}
	st := h.currentState()
	if st == nil {
		return nil, false
	}
	pc, ok := st.ConfigSnapshot().Providers[name]
	if !ok {
		return nil, false
	}
	return modelcache.Load(dir).Lookup(name, pc, modelcache.DefaultTTL, time.Now())
}

// SaveModels caches models for name, keyed by its current config.
func (h *Host) SaveModels(ctx context.Context, name string, models []schema.ModelInfo) error {
	dir := h.modelCacheDir()
	if dir == "" || len(models) == 0 {
		return nil
	}
	st := h.currentState()
	if st == nil {
		return nil
	}
	pc, ok := st.ConfigSnapshot().Providers[name]
	if !ok {
		return nil
	}
	c := modelcache.Load(dir)
	c.Providers[name] = modelcache.Entry{
		ConfigHash: modelcache.HashProvider(pc),
		Models:     models,
		FetchedAt:  time.Now(),
	}
	return modelcache.Save(dir, c)
}

// ForgetModels drops the cached list for name.
func (h *Host) ForgetModels(ctx context.Context, name string) error {
	dir := h.modelCacheDir()
	if dir == "" {
		return nil
	}
	c := modelcache.Load(dir)
	if _, ok := c.Providers[name]; !ok {
		return nil
	}
	delete(c.Providers, name)
	return modelcache.Save(dir, c)
}

// RenameModels moves the cached list of from to to.
func (h *Host) RenameModels(ctx context.Context, from, to string) error {
	dir := h.modelCacheDir()
	if dir == "" {
		return nil
	}
	c := modelcache.Load(dir)
	entry, ok := c.Providers[from]
	if !ok {
		return nil
	}
	delete(c.Providers, from)
	c.Providers[to] = entry
	return modelcache.Save(dir, c)
}

// templateWire copies a catalog entry onto the wire.
func templateWire(tpl provider.ProviderTemplate) acpwire.ProviderTemplate {
	return acpwire.ProviderTemplate{
		ID:                tpl.ID,
		Label:             tpl.Label,
		Type:              tpl.Type,
		BaseURL:           tpl.BaseURL,
		Local:             tpl.Local,
		ToolCalling:       tpl.ToolCalling,
		StructuredOutput:  tpl.StructuredOutput,
		TemperatureLocked: tpl.TemperatureLocked,
		Auth:              tpl.Auth,
		KeyEnv:            tpl.KeyEnv,
		KeyHint:           tpl.KeyHint,
		Models:            tpl.Models,
	}
}

// capsWire copies a capability probe's result onto the wire.
func capsWire(caps provider.ModelCapabilities) acpwire.ModelCapabilities {
	return acpwire.ModelCapabilities{
		ToolCalling:   caps.ToolCalling,
		ContextWindow: caps.ContextWindow,
		Reasoning:     caps.Reasoning,
	}
}
