package acp

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
)

// configProbeTimeout bounds config/probe_provider.
const configProbeTimeout = 15 * time.Second

// ModelEntry is one model returned by config/probe_provider.
type ModelEntry struct {
	ID            string `json:"id"`
	ContextWindow int    `json:"contextWindow,omitempty"`
}

// ConfigManager serves the config/* methods: the control agent reads and
// writes the user-global config (providers, presets, routing, budgets).
// Saved config applies to agents and sessions started afterwards.
type ConfigManager struct {
	home  string
	load  func() (config.Config, error)
	probe func(ctx context.Context, name string, pc config.ProviderConfig) ([]ModelEntry, error)

	mu sync.Mutex // serialises read-modify-write cycles on the config file
}

// NewConfigManager builds a manager over the user config under home. A nil
// load reads the user config (defaults plus the user file, no project layer);
// a nil probe asks the provider for its model list.
func NewConfigManager(home string, load func() (config.Config, error), probe func(context.Context, string, config.ProviderConfig) ([]ModelEntry, error)) *ConfigManager {
	m := &ConfigManager{home: home, load: load, probe: probe}
	if m.load == nil {
		m.load = func() (config.Config, error) {
			l, err := config.LoadLayers(config.LoadOptions{HomeDir: home, SkipProjectConfig: true, WorkingDir: home})
			if err != nil {
				return config.Config{}, err
			}
			return l.Merged, nil
		}
	}
	if m.probe == nil {
		m.probe = func(ctx context.Context, name string, pc config.ProviderConfig) ([]ModelEntry, error) {
			p, err := provider.NewFromConfig(name, pc, config.DataDir(home), false, 0)
			if err != nil {
				return nil, err
			}
			models, err := p.Models(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]ModelEntry, 0, len(models))
			for _, mi := range models {
				out = append(out, ModelEntry{ID: mi.ID, ContextWindow: mi.ContextWindow})
			}
			return out, nil
		}
	}
	return m
}

func (m *ConfigManager) path() string { return config.UserConfigPath(m.home) }

// providerWire is a provider as clients see it. APIKey is never part of it.
type providerWire struct {
	Type              string `json:"type"`
	BaseURL           string `json:"baseUrl"`
	APIKeyEnv         string `json:"apiKeyEnv,omitempty"`
	ToolCalling       bool   `json:"toolCalling"`
	Template          string `json:"template,omitempty"`
	Auth              string `json:"auth,omitempty"`
	KeepAlive         string `json:"keepAlive,omitempty"`
	ThinkingBudget    int    `json:"thinkingBudget,omitempty"`
	ReasoningSummary  bool   `json:"reasoningSummary,omitempty"`
	StructuredOutput  bool   `json:"structuredOutput,omitempty"`
	TemperatureLocked bool   `json:"temperatureLocked,omitempty"`
}

// providerView is providerWire plus the read-only credential facts.
type providerView struct {
	providerWire
	HasKey    bool   `json:"hasKey"`
	KeySource string `json:"keySource"` // "config", "env" or "none"
}

func providerToWire(pc config.ProviderConfig) providerWire {
	return providerWire{
		Type: pc.Type, BaseURL: pc.BaseURL, APIKeyEnv: pc.APIKeyEnv,
		ToolCalling: pc.ToolCalling, Template: pc.Template, Auth: pc.Auth,
		KeepAlive: pc.KeepAlive, ThinkingBudget: pc.ThinkingBudget,
		ReasoningSummary: pc.ReasoningSummary, StructuredOutput: pc.StructuredOutput,
		TemperatureLocked: pc.TemperatureLocked,
	}
}

// applyTo overlays w onto pc, keeping pc's credentials and any field the
// wire does not model.
func (w providerWire) applyTo(pc config.ProviderConfig) config.ProviderConfig {
	pc.Type, pc.BaseURL, pc.APIKeyEnv = w.Type, w.BaseURL, w.APIKeyEnv
	pc.ToolCalling, pc.Template, pc.Auth = w.ToolCalling, w.Template, w.Auth
	pc.KeepAlive, pc.ThinkingBudget = w.KeepAlive, w.ThinkingBudget
	pc.ReasoningSummary, pc.StructuredOutput = w.ReasoningSummary, w.StructuredOutput
	pc.TemperatureLocked = w.TemperatureLocked
	return pc
}

func keySourceOf(pc config.ProviderConfig) string {
	switch {
	case pc.APIKey != "":
		return "config"
	case pc.APIKeyEnv != "" && os.Getenv(pc.APIKeyEnv) != "":
		return "env"
	}
	return "none"
}

// presetWire is a model preset as clients see it.
type presetWire struct {
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	ContextWindow    int      `json:"contextWindow,omitempty"`
	MaxOutputTokens  int      `json:"maxOutputTokens,omitempty"`
	ToolCalling      string   `json:"toolCalling,omitempty"`
	LocalOnly        bool     `json:"localOnly,omitempty"`
	Thinking         string   `json:"thinking,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	VerificationGate *bool    `json:"verificationGate,omitempty"`
}

func presetToWire(p routing.ModelPreset) presetWire {
	return presetWire{
		Provider: p.Provider, Model: p.Model, ContextWindow: p.ContextWindow,
		MaxOutputTokens: p.MaxOutputTokens, ToolCalling: p.ToolCalling,
		LocalOnly: p.LocalOnly, Thinking: p.Thinking, Temperature: p.Temperature,
		VerificationGate: p.VerificationGate,
	}
}

func (w presetWire) applyTo(p routing.ModelPreset) routing.ModelPreset {
	p.Provider, p.Model, p.ContextWindow, p.MaxOutputTokens = w.Provider, w.Model, w.ContextWindow, w.MaxOutputTokens
	p.ToolCalling, p.LocalOnly, p.Thinking = w.ToolCalling, w.LocalOnly, w.Thinking
	p.Temperature, p.VerificationGate = w.Temperature, w.VerificationGate
	return p
}

type bindingWire struct {
	Preset      string `json:"preset,omitempty"`
	CustomAgent string `json:"customAgent,omitempty"`
}

type budgetsWire struct {
	DailyUSD    float64 `json:"dailyUsd"`
	PerAgentUSD float64 `json:"perAgentUsd"`
	OnDailyCap  string  `json:"onDailyCap"`
	OnAgentCap  string  `json:"onAgentCap"`
}

func budgetsToWire(b config.BudgetsConfig) budgetsWire {
	return budgetsWire{b.DailyUSD, b.PerAgentUSD, b.OnDailyCap, b.OnAgentCap}
}

// ConfigGetResult is the config/get result.
type ConfigGetResult struct {
	Providers      map[string]providerView           `json:"providers"`
	Presets        map[string]presetWire             `json:"presets"`
	Profiles       map[string]map[string]bindingWire `json:"profiles"`
	CustomAgents   []string                          `json:"customAgents"`
	DefaultProfile string                            `json:"defaultProfile"`
	ActivePreset   string                            `json:"activePreset"`
	Roles          []string                          `json:"roles"`
	Budgets        budgetsWire                       `json:"budgets"`
}

func (m *ConfigManager) snapshot(cfg config.Config) ConfigGetResult {
	res := ConfigGetResult{
		Providers:      map[string]providerView{},
		Presets:        map[string]presetWire{},
		Profiles:       map[string]map[string]bindingWire{},
		CustomAgents:   []string{},
		DefaultProfile: cfg.Profile.Default,
		ActivePreset:   cfg.Profile.ActivePreset,
		Roles:          []string{},
		Budgets:        budgetsToWire(cfg.Budgets),
	}
	for name, pc := range cfg.Providers {
		src := keySourceOf(pc)
		res.Providers[name] = providerView{providerToWire(pc), src != "none", src}
	}
	for name, p := range cfg.Models.Presets {
		res.Presets[name] = presetToWire(p)
	}
	for name, prof := range cfg.AgentProfiles {
		roles := map[string]bindingWire{}
		for role, b := range prof.Roles {
			roles[string(role)] = bindingWire{b.Preset, b.CustomAgent}
		}
		res.Profiles[name] = roles
	}
	for name := range cfg.CustomAgents {
		res.CustomAgents = append(res.CustomAgents, name)
	}
	sort.Strings(res.CustomAgents)
	for _, r := range routing.AllRoles {
		res.Roles = append(res.Roles, string(r))
	}
	return res
}

// Get handles config/get.
func (m *ConfigManager) Get(ctx context.Context, params json.RawMessage) (any, error) {
	cfg, err := m.load()
	if err != nil {
		return nil, serverErrorf("load config: %v", err)
	}
	return m.snapshot(cfg), nil
}

// mergeEntries overlays the incoming entries onto existing ones: a null entry
// deletes, a present key overwrites only the fields it names.
func mergeEntries[W any](raw map[string]json.RawMessage, existing func(name string) (W, bool), onNew func() W, rejectKeys ...string) (map[string]W, []string, error) {
	updated := map[string]W{}
	var deleted []string
	for name, entry := range raw {
		if string(entry) == "null" {
			deleted = append(deleted, name)
			continue
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(entry, &keys); err != nil {
			return nil, nil, invalidParamsError("entry %q must be an object or null", name)
		}
		for _, k := range rejectKeys {
			if _, bad := keys[k]; bad {
				return nil, nil, invalidParamsError("entry %q: %s is not accepted here", name, k)
			}
		}
		w, ok := existing(name)
		if !ok {
			w = onNew()
		}
		if err := json.Unmarshal(entry, &w); err != nil {
			return nil, nil, invalidParamsError("entry %q: %v", name, err)
		}
		updated[name] = w
	}
	return updated, deleted, nil
}

// SetProviders handles config/set_providers.
func (m *ConfigManager) SetProviders(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	if err := decodeParams(params, &p, "config/set_providers"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	if p.Providers == nil {
		return nil, invalidParamsError("config/set_providers requires providers")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.load()
	if err != nil {
		return nil, serverErrorf("load config: %v", err)
	}
	updated, deleted, err := mergeEntries(p.Providers,
		func(name string) (providerWire, bool) {
			pc, ok := cfg.Providers[name]
			return providerToWire(pc), ok
		},
		func() providerWire { return providerWire{} },
		"apiKey", "api_key")
	if err != nil {
		return nil, err
	}
	next := map[string]config.ProviderConfig{}
	for name, pc := range cfg.Providers {
		next[name] = pc
	}
	for _, name := range deleted {
		delete(next, name)
	}
	for name, w := range updated {
		next[name] = w.applyTo(next[name])
	}
	if err := config.SaveUserConfigProviders(m.path(), next); err != nil {
		return nil, invalidParamsError("save providers: %v", err)
	}
	return map[string]any{}, nil
}

// SetProviderKey handles config/set_provider_key.
func (m *ConfigManager) SetProviderKey(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	if err := decodeParams(params, &p, "config/set_provider_key"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	if p.Name == "" || p.Key == "" {
		return nil, invalidParamsError("config/set_provider_key requires name and key")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.load()
	if err != nil {
		return nil, serverErrorf("load config: %v", err)
	}
	pc, ok := cfg.Providers[p.Name]
	if !ok {
		return nil, invalidParamsError("unknown provider: %s", p.Name)
	}
	pc.APIKey = p.Key
	if err := config.SaveUserConfigProviderAPIKey(m.path(), p.Name, pc); err != nil {
		return nil, serverErrorf("save provider key: %v", err)
	}
	return map[string]any{}, nil
}

// SetPresets handles config/set_presets.
func (m *ConfigManager) SetPresets(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Presets map[string]json.RawMessage `json:"presets"`
	}
	if err := decodeParams(params, &p, "config/set_presets"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	if p.Presets == nil {
		return nil, invalidParamsError("config/set_presets requires presets")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.load()
	if err != nil {
		return nil, serverErrorf("load config: %v", err)
	}
	updated, deleted, err := mergeEntries(p.Presets,
		func(name string) (presetWire, bool) {
			pr, ok := cfg.Models.Presets[name]
			return presetToWire(pr), ok
		},
		func() presetWire { return presetWire{} })
	if err != nil {
		return nil, err
	}
	next := map[string]routing.ModelPreset{}
	for name, pr := range cfg.Models.Presets {
		next[name] = pr
	}
	for _, name := range deleted {
		delete(next, name)
	}
	for name, w := range updated {
		if _, ok := cfg.Providers[w.Provider]; !ok {
			return nil, invalidParamsError("preset %q: unknown provider %q", name, w.Provider)
		}
		if w.Model == "" {
			return nil, invalidParamsError("preset %q: model is required", name)
		}
		next[name] = w.applyTo(next[name])
	}
	if err := config.SaveUserConfigPresets(m.path(), next); err != nil {
		return nil, serverErrorf("save presets: %v", err)
	}
	return map[string]any{}, nil
}

// SetRouting handles config/set_routing. Omitted fields keep their values.
func (m *ConfigManager) SetRouting(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Profiles       map[string]map[string]bindingWire `json:"profiles"`
		DefaultProfile *string                           `json:"defaultProfile"`
		ActivePreset   *string                           `json:"activePreset"`
	}
	if err := decodeParams(params, &p, "config/set_routing"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.load()
	if err != nil {
		return nil, serverErrorf("load config: %v", err)
	}
	if p.Profiles != nil {
		known := map[routing.AgentRole]bool{routing.RoleFast: true, routing.RoleEmbedding: true}
		for _, r := range routing.AllRoles {
			known[r] = true
		}
		profiles := map[string]routing.AgentProfile{}
		for name, roles := range p.Profiles {
			prof := routing.AgentProfile{Name: name, Roles: map[routing.AgentRole]routing.RoleBinding{}}
			for role, b := range roles {
				if !known[routing.AgentRole(role)] {
					return nil, invalidParamsError("profile %q: unknown role %q", name, role)
				}
				if (b.Preset == "") == (b.CustomAgent == "") {
					return nil, invalidParamsError("profile %q role %q: set exactly one of preset or customAgent", name, role)
				}
				if b.Preset != "" {
					if _, ok := cfg.Models.Presets[b.Preset]; !ok {
						return nil, invalidParamsError("profile %q role %q: unknown preset %q", name, role, b.Preset)
					}
				} else if _, ok := cfg.CustomAgents[b.CustomAgent]; !ok {
					return nil, invalidParamsError("profile %q role %q: unknown custom agent %q", name, role, b.CustomAgent)
				}
				prof.Roles[routing.AgentRole(role)] = routing.RoleBinding{Preset: b.Preset, CustomAgent: b.CustomAgent}
			}
			profiles[name] = prof
		}
		cfg.AgentProfiles = profiles
	}
	if p.DefaultProfile != nil {
		if *p.DefaultProfile != "" {
			if _, ok := cfg.AgentProfiles[*p.DefaultProfile]; !ok {
				return nil, invalidParamsError("unknown default profile %q", *p.DefaultProfile)
			}
		}
		cfg.Profile.Default = *p.DefaultProfile
	}
	if p.ActivePreset != nil {
		if *p.ActivePreset != "" {
			if _, ok := cfg.Models.Presets[*p.ActivePreset]; !ok {
				return nil, invalidParamsError("unknown active preset %q", *p.ActivePreset)
			}
		}
		cfg.Profile.ActivePreset = *p.ActivePreset
	}
	if err := config.SaveUserConfigSection(m.path(), cfg); err != nil {
		return nil, serverErrorf("save routing: %v", err)
	}
	return map[string]any{}, nil
}

// SetBudgets handles config/set_budgets.
func (m *ConfigManager) SetBudgets(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Budgets *budgetsWire `json:"budgets"`
	}
	if err := decodeParams(params, &p, "config/set_budgets"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	if p.Budgets == nil {
		return nil, invalidParamsError("config/set_budgets requires budgets")
	}
	b := p.Budgets
	if b.DailyUSD < 0 || b.PerAgentUSD < 0 {
		return nil, invalidParamsError("budget caps must not be negative")
	}
	if b.OnDailyCap != "warn" && b.OnDailyCap != "block" {
		return nil, invalidParamsError("onDailyCap must be warn or block")
	}
	if b.OnAgentCap != "warn" && b.OnAgentCap != "pause" {
		return nil, invalidParamsError("onAgentCap must be warn or pause")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.load()
	if err != nil {
		return nil, serverErrorf("load config: %v", err)
	}
	cfg.Budgets = config.BudgetsConfig{DailyUSD: b.DailyUSD, PerAgentUSD: b.PerAgentUSD, OnDailyCap: b.OnDailyCap, OnAgentCap: b.OnAgentCap}
	if err := config.SaveUserConfigSection(m.path(), cfg); err != nil {
		return nil, serverErrorf("save budgets: %v", err)
	}
	return map[string]any{}, nil
}

// ConfigProbeResult is the config/probe_provider result. A failed probe is a
// result, not a protocol error.
type ConfigProbeResult struct {
	Models []ModelEntry `json:"models"`
	Error  string       `json:"error,omitempty"`
}

// ProbeProvider handles config/probe_provider with {name} or {config}.
func (m *ConfigManager) ProbeProvider(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Name   string `json:"name"`
		Config *struct {
			providerWire
			APIKey string `json:"apiKey"`
		} `json:"config"`
	}
	if err := decodeParams(params, &p, "config/probe_provider"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	var pc config.ProviderConfig
	name := p.Name
	switch {
	case p.Config != nil:
		pc = p.Config.applyTo(config.ProviderConfig{})
		pc.APIKey = p.Config.APIKey
		if name == "" {
			name = "probe"
		}
	case p.Name != "":
		cfg, err := m.load()
		if err != nil {
			return nil, serverErrorf("load config: %v", err)
		}
		var ok bool
		if pc, ok = cfg.Providers[p.Name]; !ok {
			return nil, invalidParamsError("unknown provider: %s", p.Name)
		}
	default:
		return nil, invalidParamsError("config/probe_provider requires name or config")
	}
	pctx, cancel := context.WithTimeout(ctx, configProbeTimeout)
	defer cancel()
	models, err := m.probe(pctx, name, pc)
	if err != nil {
		return ConfigProbeResult{Models: []ModelEntry{}, Error: err.Error()}, nil
	}
	if models == nil {
		models = []ModelEntry{}
	}
	return ConfigProbeResult{Models: models}, nil
}
