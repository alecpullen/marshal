package pricing

// ModelPricing holds per-token-category rates in cents per million tokens
// (integer math, no floats). Used by EstimateCostCents to compute the
// estimated cost of a turn. Zero values mean free (local models default to
// all-zero). Rates are in hundredths-of-a-cent per million tokens, so
// InputPerMTokCents=250 means $2.50 per million input tokens.
type ModelPricing struct {
	InputPerMTokCents      int64
	OutputPerMTokCents     int64
	ReasoningPerMTokCents  int64
	CacheReadPerMTokCents  int64
	CacheWritePerMTokCents int64
}

// builtInTable is the seeded pricing for common hosted models, keyed by
// model name. Local models (Ollama) are absent -> lookup returns zero.
// Prices are approximate and maintained manually; users can override per
// preset in config.toml. Rates in cents per million tokens.
var builtInTable = map[string]ModelPricing{
	"gpt-4o": {
		InputPerMTokCents:      250,
		OutputPerMTokCents:     1000,
		CacheReadPerMTokCents:  125,
		CacheWritePerMTokCents: 250,
	},
	"gpt-4o-mini": {
		InputPerMTokCents:      15,
		OutputPerMTokCents:     60,
		CacheReadPerMTokCents:  7,
		CacheWritePerMTokCents: 15,
	},
	"claude-sonnet-4": {
		InputPerMTokCents:      300,
		OutputPerMTokCents:     1500,
		CacheReadPerMTokCents:  30,
		CacheWritePerMTokCents: 375,
	},
	"deepseek-chat": {
		InputPerMTokCents:      14,
		OutputPerMTokCents:     28,
		CacheReadPerMTokCents:  1,
		CacheWritePerMTokCents: 14,
	},
	"deepseek-reasoner": {
		InputPerMTokCents:      55,
		OutputPerMTokCents:     219,
		ReasoningPerMTokCents:  219,
		CacheReadPerMTokCents:  14,
		CacheWritePerMTokCents: 55,
	},
}
