package pricing

import (
	"log/slog"

	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// Lookup returns the pricing for a given preset. Precedence: config preset
// pricing (if the preset's Pricing field is set) -> built-in table by
// model name -> zero (local/unpriced).
//
// The preset's Pricing field is typed as `any` on routing.ModelPreset
// (rather than *ModelPricing) to avoid an import cycle: routing is
// consumed by pricing, not the other way around. We type-assert back to
// *ModelPricing here, the only legitimate writer of that field.
//
// logger is optional. When non-nil the "model not in pricing table" warning
// is routed through it (so it lands in the log file rather than stderr);
// when nil no warning is emitted.
func Lookup(preset routing.ModelPreset, logger *slog.Logger) ModelPricing {
	if p, ok := preset.Pricing.(*ModelPricing); ok && p != nil {
		return *p
	}
	if p, ok := builtInTable[preset.Model]; ok {
		return p
	}
	if logger != nil {
		logger.Warn("pricing: model not in pricing table, using zero (configure manually)",
			"model", preset.Model,
			"provider", preset.Provider)
	}
	return ModelPricing{}
}

// EstimateCostCents computes the estimated cost of a turn's token usage
// in hundredths-of-a-cent (1/10000 of a dollar). Each category is
// (tokens * rate) / 1_000_000, summed. Sub-cent amounts truncate via
// integer division, which is correct for an estimate.
func EstimateCostCents(u schema.TokenUsage, p ModelPricing) int64 {
	// Cache tokens are already included in PromptTokens by all providers;
	// subtract them before applying the input rate to avoid double-charging.
	nonCachedPrompt := int64(u.PromptTokens - u.CacheReadTokens - u.CacheWriteTokens)
	if nonCachedPrompt < 0 {
		nonCachedPrompt = 0
	}
	// Reasoning tokens are already included in CompletionTokens by OpenAI
	// and DeepSeek; subtract before applying the output rate.
	nonReasoningCompletion := int64(u.CompletionTokens - u.ReasoningTokens)
	if nonReasoningCompletion < 0 {
		nonReasoningCompletion = 0
	}
	cost := int64(0)
	cost += (nonCachedPrompt * p.InputPerMTokCents) / 1_000_000
	cost += (nonReasoningCompletion * p.OutputPerMTokCents) / 1_000_000
	cost += (int64(u.ReasoningTokens) * p.ReasoningPerMTokCents) / 1_000_000
	cost += (int64(u.CacheReadTokens) * p.CacheReadPerMTokCents) / 1_000_000
	cost += (int64(u.CacheWriteTokens) * p.CacheWritePerMTokCents) / 1_000_000
	return cost
}
