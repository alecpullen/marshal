package provider

import (
	"context"

	"marshal/internal/llm/schema"
)

// Provider is implemented by every LLM backend Marshal can talk to. Method
// signatures match docs/03-provider-and-model-routing.md exactly.
type Provider interface {
	Name() string
	Models(ctx context.Context) ([]schema.ModelInfo, error)
	Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error)
	Capabilities(ctx context.Context) schema.ProviderCapabilities
}
