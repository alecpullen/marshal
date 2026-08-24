// Package limits discovers public provider/model context and output limits
// from OpenRouter and LiteLLM, normalizes them, and exposes a lookup table.
package limits

// Limit is a normalized model limit. Zero values for ContextWindow/
// MaxOutputTokens mean unknown. ToolCalling and Reasoning are nil when the
// source did not report them at all — never conflate "unreported" with
// "reported false."
type Limit struct {
	ContextWindow   int
	MaxOutputTokens int
	ToolCalling     *bool
	// Reasoning reports whether the model accepts a reasoning/effort
	// control (OpenRouter supported_parameters, LiteLLM supports_reasoning).
	Reasoning *bool
}
