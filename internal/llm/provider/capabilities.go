package provider

import "context"

// ModelCapabilities is a per-model capability signal a provider can report
// beyond its bulk Models() call. ToolCalling nil means not reported;
// ContextWindow 0 means not reported.
type ModelCapabilities struct {
	ToolCalling   *bool
	ContextWindow int
}

// CapabilityProber is implemented by providers that can probe a single
// model for capabilities beyond what their bulk Models() call returns.
// Callers should type-assert a Provider to this interface rather than
// requiring every provider to implement it: most providers' model-list
// endpoints already return everything they know, and have no extra probe
// to make.
type CapabilityProber interface {
	ProbeCapabilities(ctx context.Context, model string) ModelCapabilities
}
