package provider

import (
	"context"
	"testing"

	"marshal/internal/llm/schema"
)

type reasoningFakeProvider struct {
	caps schema.ProviderCapabilities
}

func (f reasoningFakeProvider) Name() string { return "fake" }
func (f reasoningFakeProvider) Models(context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (f reasoningFakeProvider) Chat(context.Context, schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	return nil, nil
}
func (f reasoningFakeProvider) Capabilities(context.Context) schema.ProviderCapabilities {
	return f.caps
}

type reasoningFakeProber struct {
	reasoningFakeProvider
	mc ModelCapabilities
}

func (f reasoningFakeProber) ProbeCapabilities(context.Context, string) ModelCapabilities {
	return f.mc
}

func boolPtr(b bool) *bool { return &b }

func TestResolveReasoningSupportProviderWideNoWins(t *testing.T) {
	prov := reasoningFakeProvider{caps: schema.ProviderCapabilities{Reasoning: false}}
	supported, known := ResolveReasoningSupport(context.Background(), prov, "m")
	if supported || !known {
		t.Fatalf("= (%v, %v), want (false, true): provider-wide no is authoritative", supported, known)
	}
}

func TestResolveReasoningSupportProbe(t *testing.T) {
	prov := reasoningFakeProber{
		reasoningFakeProvider: reasoningFakeProvider{caps: schema.ProviderCapabilities{Reasoning: true}},
		mc:                    ModelCapabilities{Reasoning: boolPtr(true)},
	}
	supported, known := ResolveReasoningSupport(context.Background(), prov, "m")
	if !supported || !known {
		t.Fatalf("= (%v, %v), want (true, true)", supported, known)
	}
}

func TestResolveReasoningSupportDefaultIsUnknown(t *testing.T) {
	prov := reasoningFakeProvider{caps: schema.ProviderCapabilities{Reasoning: true}}
	supported, known := ResolveReasoningSupport(context.Background(), prov, "m")
	if !supported || known {
		t.Fatalf("= (%v, %v), want (true, false): baseline default, not knowledge", supported, known)
	}
}
