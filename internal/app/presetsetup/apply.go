package presetsetup

import (
	"strconv"

	"marshal/internal/app/config"
	"marshal/internal/llm/modelcatalog"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// Result summarises a bulk apply.
type Result struct {
	Prepared int
	Matched  int
	Fallback int
}

// Apply creates or refreshes a preset for every reported model. It never
// deletes presets and never touches a preset without provenance.
func Apply(cfg *config.Config, providerName, baseURL string, models []schema.ModelInfo, cat *modelcatalog.Catalog, prov *Provenance) Result {
	var res Result
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = map[string]routing.ModelPreset{}
	}
	if prov == nil {
		prov = &Provenance{Version: ProvenanceVersion, Presets: map[string]PresetProvenance{}}
	}
	if prov.Presets == nil {
		prov.Presets = map[string]PresetProvenance{}
	}
	catalogProvider, bound := modelcatalog.ResolveBinding(providerName, baseURL)
	for _, mi := range models {
		if mi.ID == "" {
			continue
		}
		name := providerName + "/" + mi.ID
		meta := Resolve(providerName, baseURL, mi.ID, mi, cat)
		preset, exists := cfg.Models.Presets[name]
		if !exists {
			preset = routing.ModelPreset{
				Name:      name,
				Provider:  providerName,
				Model:     mi.ID,
				LocalOnly: routing.IsLocalProvider(baseURL),
			}
		} else if _, managed := prov.Presets[name]; !managed {
			// A preset created before this feature (or by hand) has no
			// provenance: never touch it.
			continue
		}
		pp := prov.Presets[name]
		if pp.Fields == nil {
			pp.Fields = map[string]FieldState{}
		}
		preset.ContextWindow = applyIntField(preset.ContextWindow, pp.Fields, "context_window", meta.ContextWindow, meta.ContextSource)
		preset.MaxOutputTokens = applyIntField(preset.MaxOutputTokens, pp.Fields, "max_output_tokens", meta.MaxOutputTokens, meta.OutputSource)
		preset.ToolCalling = applyToolField(preset.ToolCalling, pp.Fields, meta.ToolCalling, meta.ToolSource)
		applyPricing(&preset, pp.Fields, meta.Pricing, meta.PricingSource)
		if bound {
			pp.CatalogProvider = catalogProvider
			pp.CatalogModel = mi.ID
		}
		prov.Presets[name] = pp
		cfg.Models.Presets[name] = preset
		res.Prepared++
		if meta.ContextSource == SourceModelsDev || meta.OutputSource == SourceModelsDev ||
			meta.ToolSource == SourceModelsDev || meta.PricingSource == SourceModelsDev {
			res.Matched++
		} else {
			res.Fallback++
		}
	}
	return res
}

// applyIntField updates a managed integer field. A field whose current value
// no longer matches the last applied value is a user edit: it is preserved and
// dropped from management. A non-positive next value never erases a value.
func applyIntField(current int, fields map[string]FieldState, key string, next int, source Source) int {
	prev, managed := fields[key]
	if managed && prev.Value != strconv.Itoa(current) {
		delete(fields, key)
		return current
	}
	if next <= 0 {
		return current
	}
	fields[key] = FieldState{Source: string(source), Value: strconv.Itoa(next)}
	return next
}

// applyToolField updates tool-calling support with the same compare-before-
// refresh rule.
func applyToolField(current string, fields map[string]FieldState, next *bool, source Source) string {
	prev, managed := fields["tool_calling"]
	if managed && prev.Value != current {
		delete(fields, "tool_calling")
		return current
	}
	if next == nil {
		return current
	}
	v := "none"
	if *next {
		v = "native"
	}
	fields["tool_calling"] = FieldState{Source: string(source), Value: v}
	return v
}

// applyPricing updates each pricing component independently. A preset whose
// Pricing holds an unrecognised shape is user-owned and is left alone.
//
// The pricing struct is copied before it is updated. Callers hand Apply a
// shallow copy of the preset map, so *cur can still be the pointer held by
// the live config; mutating it in place would leak refreshed prices into a
// connect the user then cancels.
func applyPricing(preset *routing.ModelPreset, fields map[string]FieldState, next *pricing.ModelPricing, source Source) {
	if next == nil {
		return
	}
	cur, ok := preset.Pricing.(*pricing.ModelPricing)
	if preset.Pricing != nil && !ok {
		return
	}
	updated := &pricing.ModelPricing{}
	if cur != nil {
		*updated = *cur
	}
	updated.InputPerMTokCents = applyInt64Field(updated.InputPerMTokCents, fields, "pricing.input", next.InputPerMTokCents, source)
	updated.OutputPerMTokCents = applyInt64Field(updated.OutputPerMTokCents, fields, "pricing.output", next.OutputPerMTokCents, source)
	updated.ReasoningPerMTokCents = applyInt64Field(updated.ReasoningPerMTokCents, fields, "pricing.reasoning", next.ReasoningPerMTokCents, source)
	updated.CacheReadPerMTokCents = applyInt64Field(updated.CacheReadPerMTokCents, fields, "pricing.cache_read", next.CacheReadPerMTokCents, source)
	updated.CacheWritePerMTokCents = applyInt64Field(updated.CacheWritePerMTokCents, fields, "pricing.cache_write", next.CacheWritePerMTokCents, source)
	preset.Pricing = updated
}

// applyInt64Field updates a managed pricing component with the same
// compare-before-refresh rule as applyIntField. A non-positive next value
// never erases a value: models.dev omits a rate by publishing zero, and a
// dropped component must not wipe the figure recorded on an earlier connect.
func applyInt64Field(current int64, fields map[string]FieldState, key string, next int64, source Source) int64 {
	prev, managed := fields[key]
	if managed && prev.Value != strconv.FormatInt(current, 10) {
		delete(fields, key)
		return current
	}
	if next <= 0 {
		return current
	}
	fields[key] = FieldState{Source: string(source), Value: strconv.FormatInt(next, 10)}
	return next
}
