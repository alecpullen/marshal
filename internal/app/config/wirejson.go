package config

import (
	"encoding/json"
	"reflect"
	"strings"

	"marshal/internal/llm/pricing"
	"marshal/internal/llm/routing"
)

// UnmarshalConfigJSON decodes a Config that crossed the client seam as JSON,
// then restores each preset's Pricing to a *pricing.ModelPricing: a JSON
// decode into routing.ModelPreset's `any` field yields a map[string]any.
func UnmarshalConfigJSON(data []byte, cfg *Config) error {
	if err := json.Unmarshal(data, cfg); err != nil {
		return err
	}
	restorePresetPricing(cfg.Models.Presets)
	return nil
}

// UnmarshalLayersJSON is UnmarshalConfigJSON for each of the layers' three
// configs.
func UnmarshalLayersJSON(data []byte, layers *Layers) error {
	if err := json.Unmarshal(data, layers); err != nil {
		return err
	}
	restorePresetPricing(layers.Default.Models.Presets)
	restorePresetPricing(layers.User.Models.Presets)
	restorePresetPricing(layers.Merged.Models.Presets)
	return nil
}

// UnmarshalPresetsJSON decodes a preset map and restores its Pricing values.
func UnmarshalPresetsJSON(data []byte, presets *map[string]routing.ModelPreset) error {
	if err := json.Unmarshal(data, presets); err != nil {
		return err
	}
	restorePresetPricing(*presets)
	return nil
}

// restorePresetPricing is coercePresetPricing's JSON counterpart: it rewrites
// every Pricing that decoded as a map[string]any into a
// *pricing.ModelPricing by re-encoding the map as JSON. A Pricing that is
// neither a pointer nor such a map is left as it is, and a map that does not
// decode into the typed pricing is left as it is too, exactly as
// coercePresetPricing leaves both.
func restorePresetPricing(presets map[string]routing.ModelPreset) {
	for name, p := range presets {
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
		coerced, err := jsonPricingMap(m)
		if err != nil {
			continue
		}
		p.Pricing = coerced
		presets[name] = p
	}
}

// jsonPricingMap turns a map[string]any (as a JSON decode into `any` produces)
// into a *pricing.ModelPricing.
func jsonPricingMap(m map[string]any) (*pricing.ModelPricing, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var p pricing.ModelPricing
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// PathType is the Go type of the value SaveUserConfigValue can write at the
// dotted TOML path: the Config field's type there with any pointer removed.
// It walks struct fields by their toml tag, through pointers, and through a
// string-keyed map's element type, so a path into a map entry that does not
// exist yet still has a type. It reports false for a path that names nothing.
func PathType(dottedPath string) (reflect.Type, bool) {
	t := reflect.TypeOf(Config{})
	for _, seg := range strings.Split(dottedPath, ".") {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			ft, ok := fieldTypeByTOMLTag(t, seg)
			if !ok {
				return nil, false
			}
			t = ft
		case reflect.Map:
			if t.Key().Kind() != reflect.String {
				return nil, false
			}
			t = t.Elem()
		default:
			return nil, false
		}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Interface {
		// A field typed any (a preset's Pricing) has no type to decode into: a
		// JSON value would arrive as a map[string]any and silently lose the
		// pointer the field holds. Reporting no type makes the write fail
		// loudly instead.
		return nil, false
	}
	return t, true
}

// fieldTypeByTOMLTag is fieldByTOMLTag for a type instead of a value: it
// matches a struct field by its toml tag, falling back to the lowercased field
// name.
func fieldTypeByTOMLTag(t reflect.Type, name string) (reflect.Type, bool) {
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("toml")
		if tag == "" {
			tag = strings.ToLower(t.Field(i).Name)
		}
		if tag == name || strings.Split(tag, ",")[0] == name {
			return t.Field(i).Type, true
		}
	}
	return nil, false
}
