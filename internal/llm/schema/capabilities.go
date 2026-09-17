package schema

type ProviderCapabilities struct {
	ToolCalling      bool
	JSONMode         bool
	StructuredOutput bool
	// Reasoning reports that the backend accepts a reasoning/effort control
	// (reasoning_effort or thinking budget_tokens).
	Reasoning bool
	// TemperatureLocked reports that the backend only accepts its own fixed
	// sampling temperature (e.g. Kimi's coding endpoint accepts only
	// temperature = 1.0). The agent runner suppresses any requested
	// temperature at request-build time when this is set.
	TemperatureLocked bool
}
