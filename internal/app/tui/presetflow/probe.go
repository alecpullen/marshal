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
	Provider  string
	Model     string
	RequestID uint64
	Caps      provider.ModelCapabilities
}

// ProbeCapabilitiesCmd probes prov for model's capabilities if prov
// implements provider.CapabilityProber, bounded by CapabilityProbeTimeout,
// and delivers the result as a CapabilityProbedMsg. Providers that don't
// implement the interface report a zero-value result immediately without a
// network call — callers should check this before deciding whether to show
// a "detecting…" state at all (see connect.go's probeCapabilities).
// ProbeCapabilitiesCmd is the cancel-free convenience wrapper around
// ProbeCapabilitiesCmdWithCancel; callers that don't own a confirmation
// lifecycle (legacy code paths) use this and let the deferred cancel
// inside the cmd release the timeout context when it returns.
func ProbeCapabilitiesCmd(prov provider.Provider, providerName, modelID string) tea.Cmd {
	cmd, _ := ProbeCapabilitiesCmdWithCancel(prov, providerName, modelID, 0)
	return cmd
}

// ProbeCapabilitiesCmdWithCancel is ProbeCapabilitiesCmd with a request
// identity and a cancellation function for callers that own the overlay
// lifecycle. Canceling invalidates the provider request context; callers
// should also reject any result whose RequestID is no longer current.
func ProbeCapabilitiesCmdWithCancel(prov provider.Provider, providerName, modelID string, requestID uint64) (tea.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), CapabilityProbeTimeout)
	return func() tea.Msg {
		prober, ok := prov.(provider.CapabilityProber)
		if !ok {
			cancel()
			return CapabilityProbedMsg{Provider: providerName, Model: modelID, RequestID: requestID}
		}
		defer cancel()
		return CapabilityProbedMsg{Provider: providerName, Model: modelID, RequestID: requestID, Caps: prober.ProbeCapabilities(ctx, modelID)}
	}, cancel
}
