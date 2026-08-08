package presetflow

import (
	"context"
	"testing"

	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
)

// fakeProviderWithCaps implements the full provider.Provider interface
// (Name/Models/Chat/Capabilities) plus provider.CapabilityProber, mirroring
// how *provider.OllamaNative implements both in production.
type fakeProviderWithCaps struct {
	caps provider.ModelCapabilities
}

func (f fakeProviderWithCaps) Name() string { return "fake" }
func (f fakeProviderWithCaps) Models(context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (f fakeProviderWithCaps) Chat(context.Context, schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	return nil, nil
}
func (f fakeProviderWithCaps) Capabilities(context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}
func (f fakeProviderWithCaps) ProbeCapabilities(_ context.Context, _ string) provider.ModelCapabilities {
	return f.caps
}

// fakeProviderNoCaps implements provider.Provider only — no
// ProbeCapabilities method — mirroring *provider.OpenAICompatible.
type fakeProviderNoCaps struct{}

func (fakeProviderNoCaps) Name() string { return "fake" }
func (fakeProviderNoCaps) Models(context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (fakeProviderNoCaps) Chat(context.Context, schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	return nil, nil
}
func (fakeProviderNoCaps) Capabilities(context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func TestProbeCapabilitiesCmdCallsProberAndReturnsMsg(t *testing.T) {
	toolCalling := true
	prov := fakeProviderWithCaps{caps: provider.ModelCapabilities{ToolCalling: &toolCalling, ContextWindow: 40960}}

	cmd := ProbeCapabilitiesCmd(prov, "ollama", "qwen3-coder:30b")
	if cmd == nil {
		t.Fatal("expected a non-nil cmd")
	}
	msg, ok := cmd().(CapabilityProbedMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want CapabilityProbedMsg", msg)
	}
	if msg.Provider != "ollama" || msg.Model != "qwen3-coder:30b" {
		t.Errorf("msg = %+v, want Provider=ollama Model=qwen3-coder:30b", msg)
	}
	if msg.Caps.ToolCalling == nil || !*msg.Caps.ToolCalling || msg.Caps.ContextWindow != 40960 {
		t.Errorf("msg.Caps = %+v, want ToolCalling=true ContextWindow=40960", msg.Caps)
	}
}

func TestProbeCapabilitiesCmdReturnsZeroValueWhenNotAProber(t *testing.T) {
	cmd := ProbeCapabilitiesCmd(fakeProviderNoCaps{}, "openrouter", "gpt-4o")
	if cmd == nil {
		t.Fatal("expected a non-nil cmd even when the provider can't be probed")
	}
	msg, ok := cmd().(CapabilityProbedMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want CapabilityProbedMsg", msg)
	}
	if msg.Caps.ToolCalling != nil || msg.Caps.ContextWindow != 0 {
		t.Errorf("msg.Caps = %+v, want zero value", msg.Caps)
	}
}
