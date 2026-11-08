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
