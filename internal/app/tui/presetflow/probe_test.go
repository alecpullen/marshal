package presetflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
)

// fakeHost stands in for the client host. The embedded interface is nil: only
// ProbeModel is ever reached, and any other call is a test failure by panic.
type fakeHost struct {
	acpclient.Host

	caps acpwire.ModelCapabilities
	err  error

	// block makes ProbeModel wait for its context to be done, so a test can
	// prove the cancel function ends an in-flight probe.
	block   bool
	started chan struct{}
	once    sync.Once

	mu       sync.Mutex
	gotName  string
	gotModel string
	gotCfg   config.ProviderConfig
}

func (h *fakeHost) ProbeModel(ctx context.Context, name string, pc config.ProviderConfig, model string) (acpwire.ModelCapabilities, error) {
	h.mu.Lock()
	h.gotName, h.gotCfg, h.gotModel = name, pc, model
	h.mu.Unlock()
	if h.started != nil {
		h.once.Do(func() { close(h.started) })
	}
	if h.block {
		<-ctx.Done()
		return acpwire.ModelCapabilities{}, ctx.Err()
	}
	return h.caps, h.err
}

func TestProbeCapabilitiesCmdCallsTheHost(t *testing.T) {
	toolCalling := true
	host := &fakeHost{caps: acpwire.ModelCapabilities{ToolCalling: &toolCalling, ContextWindow: 40960}}
	pc := config.ProviderConfig{Type: "ollama", BaseURL: "http://localhost:11434"}

	cmd, cancel := ProbeCapabilitiesCmdWithCancel(host, "ollama", pc, "qwen3-coder:30b", 0)
	defer cancel()
	raw := cmd()
	msg, ok := raw.(CapabilityProbedMsg)
	if !ok {
		t.Fatalf("cmd() produced %T, want CapabilityProbedMsg", raw)
	}
	if msg.Provider != "ollama" || msg.Model != "qwen3-coder:30b" {
		t.Errorf("msg = %+v, want Provider=ollama Model=qwen3-coder:30b", msg)
	}
	if msg.Caps.ToolCalling == nil || !*msg.Caps.ToolCalling || msg.Caps.ContextWindow != 40960 {
		t.Errorf("msg.Caps = %+v, want ToolCalling=true ContextWindow=40960", msg.Caps)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.gotName != "ollama" || host.gotModel != "qwen3-coder:30b" ||
		host.gotCfg.Type != pc.Type || host.gotCfg.BaseURL != pc.BaseURL {
		t.Errorf("host received name=%q cfg=%+v model=%q, want %q, %+v, %q",
			host.gotName, host.gotCfg, host.gotModel, "ollama", pc, "qwen3-coder:30b")
	}
}

func TestProbeCapabilitiesCmdCarriesRequestID(t *testing.T) {
	host := &fakeHost{block: true, started: make(chan struct{})}

	cmd, cancel := ProbeCapabilitiesCmdWithCancel(host, "ollama", config.ProviderConfig{Type: "ollama"}, "qwen3", 42)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	select {
	case <-host.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe never reached the host")
	}
	cancel()

	select {
	case raw := <-done:
		msg, ok := raw.(CapabilityProbedMsg)
		if !ok {
			t.Fatalf("cmd() produced %T, want CapabilityProbedMsg", raw)
		}
		if msg.RequestID != 42 {
			t.Fatalf("RequestID = %d, want 42", msg.RequestID)
		}
		if msg.Caps.ToolCalling != nil || msg.Caps.ContextWindow != 0 || msg.Caps.Reasoning != nil {
			t.Fatalf("msg.Caps = %+v, want the zero value after cancellation", msg.Caps)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancel function did not end the blocking probe")
	}
}

func TestProbeCapabilitiesCmdTreatsAnErrorAsUnknown(t *testing.T) {
	host := &fakeHost{err: errors.New("probe failed")}

	cmd, cancel := ProbeCapabilitiesCmdWithCancel(host, "openrouter", config.ProviderConfig{Type: "openai_compatible"}, "gpt-4o", 0)
	defer cancel()
	raw := cmd()
	msg, ok := raw.(CapabilityProbedMsg)
	if !ok {
		t.Fatalf("cmd() produced %T, want CapabilityProbedMsg", raw)
	}
	if msg.Caps.ToolCalling != nil || msg.Caps.ContextWindow != 0 || msg.Caps.Reasoning != nil {
		t.Errorf("msg.Caps = %+v, want the zero value", msg.Caps)
	}
}
