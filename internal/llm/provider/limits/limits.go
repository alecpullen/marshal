// Package limits discovers public provider/model context and output limits
// from OpenRouter and LiteLLM, normalizes them, and exposes a lookup table.
package limits

// Limit is a normalized model limit. Zero values mean unknown.
type Limit struct {
	ContextWindow   int
	MaxOutputTokens int
}
