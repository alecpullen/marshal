package pricing

import (
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// Lookup returns the pricing for a given preset. Precedence: config preset
// pricing (if the preset's Pricing field is set) -> built-in table by
// model name -> zero (local/unpriced). Pure function, no I/O.
//
// The preset's Pricing field is typed as `any` on routing.ModelPreset
// (rather than *ModelPricing) to avoid an import cycle: routing is
// consumed by pricing, not the other way around. We type-assert back to
// *ModelPricing here, the only legitimate writer of that field.
func Lookup(preset routing.ModelPreset) ModelPricing {
	if p, ok := preset.Pricing.(*ModelPricing); ok && p != nil {
		return *p
	}
	if p, ok := builtInTable[preset.Model]; ok {
		return p
	}
	return ModelPricing{}
}

// EstimateCostCents computes the estimated cost of a turn's token usage
// in hundredths-of-a-cent (1/10000 of a dollar). Each category is
// (tokens * rate) / 1_000_000, summed. Sub-cent amounts truncate via
// integer division, which is correct for an estimate.
func EstimateCostCents(u schema.TokenUsage, p ModelPricing) int64 {
	cost := int64(0)
	cost += (int64(u.PromptTokens) * p.InputPerMTokCents) / 1_000_000
	cost += (int64(u.CompletionTokens) * p.OutputPerMTokCents) / 1_000_000
	cost += (int64(u.ReasoningTokens) * p.ReasoningPerMTokCents) / 1_000_000
	cost += (int64(u.CacheReadTokens) * p.CacheReadPerMTokCents) / 1_000_000
	cost += (int64(u.CacheWriteTokens) * p.CacheWritePerMTokCents) / 1_000_000
	return cost
}
