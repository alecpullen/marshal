package config

import (
	"fmt"
	"os"

	"marshal/internal/llm/pricing"

	"github.com/pelletier/go-toml/v2"
)

func Load(opts LoadOptions) (Config, error) {
	cfg, _, err := LoadWithLayers(opts)
	return cfg, err
}

// LoadWithLayers runs LoadLayers once and returns both the merged Config
// and the Layers struct. This avoids the double-load that occurs when
// callers need both Config and Layers but call Load() and LoadLayers()
// separately (each running the full layer merge internally).
func LoadWithLayers(opts LoadOptions) (Config, Layers, error) {
	l, err := LoadLayers(opts)
	return l.Merged, l, err
}

func loadFile(path string) (configFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return configFile{}, nil
		}
		return configFile{}, fmt.Errorf("read config %s: %w", path, err)
	}

	var file configFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return configFile{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return file, nil
}

func HasConfig(opts LoadOptions) bool {
	home := opts.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return false
		}
	}

	work := opts.WorkingDir
	if work == "" {
		var err error
		work, err = os.Getwd()
		if err != nil {
			return false
		}
	}

	for _, path := range []string{
		UserConfigPath(home),
		ProjectConfigPath(work),
	} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// coercePresetPricing walks cfg.Models.Presets and converts any Pricing
// value that the go-toml decoder produced as map[string]any back into
// a *pricing.ModelPricing. This is necessary because routing.ModelPreset
// types Pricing as `any` to avoid an import cycle (pricing imports
// routing), and go-toml/v2 decodes nested tables into map[string]any
// when the destination field is `any`. Callers that read the pricing
// fields directly (e.g. cost estimation) need the concrete type.
func coercePresetPricing(cfg *Config) {
	if cfg == nil || len(cfg.Models.Presets) == 0 {
		return
	}
	for name, p := range cfg.Models.Presets {
		if p.Pricing == nil {
			continue
		}
		if _, ok := p.Pricing.(*pricing.ModelPricing); ok {
			continue
		}
		m, ok := p.Pricing.(map[string]any)
		if !ok {
			continue
		}
		coerced, err := decodePricingMap(m)
		if err != nil {
			continue
		}
		p.Pricing = coerced
		cfg.Models.Presets[name] = p
	}
}

// decodePricingMap turns a map[string]any (as produced by go-toml when
// unmarshalling into `any`) into a *pricing.ModelPricing. It re-marshals
// to TOML and re-unmarshals into the concrete type, which is robust
// against the field-name and integer/float type differences between
// generic and typed decoding.
func decodePricingMap(m map[string]any) (*pricing.ModelPricing, error) {
	data, err := toml.Marshal(m)
	if err != nil {
		return nil, err
	}
	var p pricing.ModelPricing
	if err := toml.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
