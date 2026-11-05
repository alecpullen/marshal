package presetflow

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
)

// CapabilityProbeTimeout bounds a single per-model capability probe.
// Exported so tests can shorten it.
var CapabilityProbeTimeout = 5 * time.Second

// CapabilityProbedMsg reports the result of an async per-model capability
// probe issued by ProbeCapabilitiesCmdWithCancel. Caps is the zero value when
// the host could not tell anything about the model — callers should treat that
// exactly like "nothing detected," not as an error.
type CapabilityProbedMsg struct {
	Provider  string
	Model     string
	RequestID uint64
	Caps      acpwire.ModelCapabilities
}

// ProbeCapabilitiesCmdWithCancel probes modelID's capabilities through host,
// bounded by CapabilityProbeTimeout, and delivers the result as a
// CapabilityProbedMsg carrying requestID. It returns a cancellation function
// for callers that own the overlay lifecycle: cancelling invalidates the
// request context, and callers should also reject any result whose RequestID
// is no longer current.
func ProbeCapabilitiesCmdWithCancel(host acpclient.Host, providerName string, pc config.ProviderConfig, modelID string, requestID uint64) (tea.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), CapabilityProbeTimeout)
	return func() tea.Msg {
		// A host that cannot probe reports zero capabilities and no error,
		// which is exactly the "nothing detected" result.
		if host == nil {
			cancel()
			return CapabilityProbedMsg{Provider: providerName, Model: modelID, RequestID: requestID}
		}
		defer cancel()
		caps, err := host.ProbeModel(ctx, providerName, pc, modelID)
		if err != nil {
			return CapabilityProbedMsg{Provider: providerName, Model: modelID, RequestID: requestID}
		}
		return CapabilityProbedMsg{Provider: providerName, Model: modelID, RequestID: requestID, Caps: caps}
	}, cancel
}
