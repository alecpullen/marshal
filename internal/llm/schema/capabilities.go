package schema

type ProviderCapabilities struct {
	ToolCalling      bool
	JSONMode         bool
	StructuredOutput bool
	// Reasoning reports that the backend accepts a reasoning/effort control
	// (reasoning_effort or thinking budget_tokens).
	Reasoning bool
}
