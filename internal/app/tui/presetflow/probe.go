package presetflow

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/llm/provider"
)

// CapabilityProbeTimeout bounds a single per-model capability probe.
// Exported so tests can shorten it.
var CapabilityProbeTimeout = 5 * time.Second

// CapabilityProbedMsg reports the result of an async per-model capability
// probe issued by ProbeCapabilitiesCmd. Caps is the zero value if the
// provider does not implement provider.CapabilityProber — callers should
// treat that exactly like "nothing detected," not as an error.
type CapabilityProbedMsg struct {
	Provider string
	Model    string
	Caps     provider.ModelCapabilities
}

// ProbeCapabilitiesCmd probes prov for model's capabilities if prov
// implements provider.CapabilityProber, bounded by CapabilityProbeTimeout,
// and delivers the result as a CapabilityProbedMsg. Providers that don't
// implement the interface report a zero-value result immediately without a
// network call — callers should check this before deciding whether to show
// a "detecting…" state at all (see connect.go's probeCapabilities).
func ProbeCapabilitiesCmd(prov provider.Provider, providerName, modelID string) tea.Cmd {
	return func() tea.Msg {
		prober, ok := prov.(provider.CapabilityProber)
		if !ok {
			return CapabilityProbedMsg{Provider: providerName, Model: modelID}
		}
		ctx, cancel := context.WithTimeout(context.Background(), CapabilityProbeTimeout)
		defer cancel()
		return CapabilityProbedMsg{Provider: providerName, Model: modelID, Caps: prober.ProbeCapabilities(ctx, modelID)}
	}
}
