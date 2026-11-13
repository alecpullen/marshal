package bridge

import (
	"context"
	"encoding/json"
	"fmt"
)

// factoryValidateRoutes applies the route rules to both routes of a
// configuration: an explicit routing profile, a known profile, a resolvable
// preset, a configured provider and its credential.
//
// The preflight and the route-only retry both call this, so "the same rules
// apply" is a structural property rather than a convention. Every message
// names the role whose route was at fault, because the helper checks both
// routes of a configuration.
func (a *factoryAPI) factoryValidateRoutes(ctx context.Context, c FactoryConfig) error {
	if a.fleet == nil {
		return fmt.Errorf("%w: fleet unavailable", ErrFactoryUnavailable)
	}
	raw, err := a.fleet.controlCall(ctx, "config/get", nil, false)
	if err != nil {
		return fmt.Errorf("%w: model configuration unavailable", ErrFactoryUnavailable)
	}
	var modelConfig struct {
		Providers map[string]struct {
			HasKey    bool   `json:"hasKey"`
			Type      string `json:"type"`
			APIKeyEnv string `json:"apiKeyEnv"`
		} `json:"providers"`
		Presets map[string]struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"presets"`
		Profiles map[string]map[string]struct {
			Preset string `json:"preset"`
		} `json:"profiles"`
		DefaultProfile string `json:"defaultProfile"`
	}
	if json.Unmarshal(raw, &modelConfig) != nil {
		return fmt.Errorf("%w: invalid model configuration", ErrFactoryUnavailable)
	}
	for n, route := range []json.RawMessage{c.ImplementerRouting, c.ReviewerRouting} {
		role := "implementer"
		if n == 1 {
			role = "reviewer"
		}
		var selection struct {
			Profile   string            `json:"profile"`
			Overrides map[string]string `json:"overrides"`
		}
		if len(route) == 0 || json.Unmarshal(route, &selection) != nil {
			return fmt.Errorf("%w: explicit %s routing required", errFactoryConfig, role)
		}
		if selection.Profile == "" {
			return fmt.Errorf("%w: explicit %s routing profile required", errFactoryConfig, role)
		}
		profile := selection.Profile
		if profile == "" {
			profile = modelConfig.DefaultProfile
		}
		bindings, ok := modelConfig.Profiles[profile]
		if !ok {
			return fmt.Errorf("%w: unknown %s routing profile", errFactoryConfig, role)
		}
		preset := selection.Overrides[role]
		if preset == "" {
			preset = bindings[role].Preset
		}
		p, ok := modelConfig.Presets[preset]
		if !ok || p.Model == "" || p.Provider == "" {
			return fmt.Errorf("%w: %s model preset unavailable", errFactoryConfig, role)
		}
		provider, ok := modelConfig.Providers[p.Provider]
		if !ok || provider.Type == "" {
			return fmt.Errorf("%w: %s provider unavailable", errFactoryConfig, role)
		}
		if provider.Type != "openai_codex" && (provider.Type == "anthropic" || provider.APIKeyEnv != "") && !provider.HasKey {
			return fmt.Errorf("%w: %s provider credential unavailable", errFactoryConfig, role)
		}
	}
	return nil
}
