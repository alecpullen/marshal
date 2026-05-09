// Package integration provides the bridge between Marshal's existing backend
// system and the new gateway. This allows gradual migration while maintaining
// backward compatibility.
package integration

import (
	"context"
	"fmt"
	"strings"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/gateway"
	"github.com/alecpullen/marshal/internal/gateway/anthropic"
	"github.com/alecpullen/marshal/internal/gateway/detect"
	"github.com/alecpullen/marshal/internal/gateway/openai"
	"github.com/alecpullen/marshal/internal/models"
)

// GatewayRegistry wraps the new gateway system to implement the existing
// backend.Registry interface. This allows the rest of Marshal to use the
// gateway without code changes.
type GatewayRegistry struct {
	router    *gateway.Router
	detector  *detect.Detector
	providers map[string]gateway.ProviderAdapter
}

// NewGatewayRegistry creates a new registry that uses the gateway system.
// It initializes from the provided config and auto-detects providers.
func NewGatewayRegistry(cfg *config.Config, modelReg *models.Registry) (*GatewayRegistry, error) {
	// Create budget tracker
	budget := gateway.NewBudgetTracker(
		gateway.WithSessionBudget(10.0),
		gateway.WithDailyBudget(50.0),
	)

	// Create router with auto-resolve enabled
	router := gateway.NewRouter(budget, gateway.WithAutoResolve(true))

	// Create detector
	detector := detect.NewDetector()

	// Auto-detect providers and configure router
	ctx := context.Background()
	detected := detector.Probe(ctx)
	
	// Convert detected providers to gateway.Provider
	providers := make([]gateway.Provider, 0, len(detected))
	for _, d := range detected {
		providers = append(providers, gateway.Provider(d.Name))
	}
	router.SetAvailableProviders(providers)

	// Convert existing config bindings to gateway bindings
	if err := convertConfigToBindings(cfg, router); err != nil {
		return nil, fmt.Errorf("converting config: %w", err)
	}

	reg := &GatewayRegistry{
		router:    router,
		detector:  detector,
		providers: make(map[string]gateway.ProviderAdapter),
	}

	// Initialize provider adapters for each role
	if err := reg.initProviders(cfg); err != nil {
		return nil, fmt.Errorf("initializing providers: %w", err)
	}

	return reg, nil
}

// For returns a backend for the given role by wrapping the gateway adapter.
func (r *GatewayRegistry) For(role string) (backend.Backend, error) {
	// First try to get existing provider
	if provider, ok := r.providers[role]; ok {
		return &backendAdapter{provider: provider, role: role}, nil
	}

	// Try to resolve a binding for this role
	ctx := context.Background()
	resolved, err := r.router.Resolve(ctx, role, 0)
	if err != nil {
		// If no binding found, return error
		return nil, fmt.Errorf("no provider available for role %q: %w", role, err)
	}

	// Create provider on-demand
	provider, err := r.createProvider(resolved.Binding)
	if err != nil {
		return nil, fmt.Errorf("creating provider for role %q: %w", role, err)
	}

	r.providers[role] = provider
	return &backendAdapter{provider: provider, role: role, binding: resolved.Binding}, nil
}

// initProviders creates provider adapters from config.
func (r *GatewayRegistry) initProviders(cfg *config.Config) error {
	roles := []struct {
		name string
		mc   config.ModelConfig
	}{
		{config.RoleMarshal, cfg.Model.Marshal},
		{config.RoleExecutor, cfg.Model.Executor},
		{config.RoleCritic, cfg.Model.Critic},
		{config.RoleCompactor, cfg.Model.Compactor},
	}

	for _, entry := range roles {
		if entry.mc.Model == "" {
			continue
		}

		binding := convertModelConfigToBinding(entry.mc)
		
		// Resolve API key from auth_ref
		if binding.AuthRef == "" && binding.APIKey == "" {
			binding.AuthRef = fmt.Sprintf("env:%s_API_KEY", strings.ToUpper(entry.name))
		}
		apiKey, _ := gateway.ResolveAuth(binding.AuthRef)
		if apiKey != "" {
			binding.APIKey = apiKey
		}

		provider, err := r.createProvider(binding)
		if err != nil {
			continue // Skip failed providers
		}

		r.providers[entry.name] = provider
	}

	return nil
}

// createProvider creates a provider adapter from a binding.
func (r *GatewayRegistry) createProvider(binding gateway.Binding) (gateway.ProviderAdapter, error) {
	switch binding.Provider {
	case gateway.ProviderAnthropic:
		return anthropic.NewAdapter(
			binding.APIKey,
			binding.Model,
			anthropic.WithEndpoint(binding.Endpoint),
		), nil

	case gateway.ProviderOpenAI, gateway.ProviderOpenRouter, 
		gateway.ProviderOllama, gateway.ProviderLMStudio, 
		gateway.ProviderVLLM, gateway.ProviderFireworks, gateway.ProviderRunPod:
		return openai.NewForProvider(binding.Provider, binding.APIKey, binding.Model), nil

	default:
		return nil, fmt.Errorf("unsupported provider: %s", binding.Provider)
	}
}

// GetRouter returns the underlying router for advanced operations.
func (r *GatewayRegistry) GetRouter() *gateway.Router {
	return r.router
}

// GetDetector returns the detector for provider detection.
func (r *GatewayRegistry) GetDetector() *detect.Detector {
	return r.detector
}

// DetectProviders runs provider detection and returns results.
func (r *GatewayRegistry) DetectProviders(ctx context.Context) []detect.DetectedProvider {
	return r.detector.Probe(ctx)
}

// backendAdapter wraps a gateway.ProviderAdapter to implement backend.Backend.
type backendAdapter struct {
	provider gateway.ProviderAdapter
	role     string
	binding  gateway.Binding
}

// Complete implements backend.Backend.Complete.
func (b *backendAdapter) Complete(ctx context.Context, req backend.Request) (backend.Response, error) {
	// Convert backend.Request to gateway.ChatRequest
	gReq := convertBackendRequestToGateway(req)

	// We need to use the non-streaming interface, but gateway only has streaming
	// So we'll collect all events and return them
	events, err := b.provider.Complete(ctx, gReq)
	if err != nil {
		return backend.Response{}, err
	}

	var content strings.Builder
	var toolCalls []backend.ToolCall

	for event := range events {
		if event.Err != nil {
			return backend.Response{}, event.Err
		}
		switch event.Kind {
		case gateway.StreamEventDelta:
			content.WriteString(event.Text)
		case gateway.StreamEventToolCall:
			if event.ToolCall != nil {
				toolCalls = append(toolCalls, backend.ToolCall{
					ID:   event.ToolCall.ID,
					Type: "function",
					Function: backend.FunctionCall{
						Name:      event.ToolCall.Name,
						Arguments: event.ToolCall.InputJSON,
					},
				})
			}
		}
	}

	return backend.Response{
		Content:   content.String(),
		ToolCalls: toolCalls,
	}, nil
}

// Stream implements backend.Backend.Stream.
func (b *backendAdapter) Stream(ctx context.Context, req backend.Request) (<-chan backend.Chunk, error) {
	// Convert request
	gReq := convertBackendRequestToGateway(req)

	// Get gateway events
	events, err := b.provider.Complete(ctx, gReq)
	if err != nil {
		return nil, err
	}

	// Convert gateway events to backend chunks
	chunks := make(chan backend.Chunk, 16)
	go func() {
		defer close(chunks)
		for event := range events {
			if event.Err != nil {
				chunks <- backend.Chunk{Err: event.Err}
				return
			}

			var chunk backend.Chunk
			switch event.Kind {
			case gateway.StreamEventDelta:
				chunk.Content = event.Text
			case gateway.StreamEventToolCall:
				if event.ToolCall != nil {
					chunk.ToolCalls = []backend.ToolCall{{
						ID:   event.ToolCall.ID,
						Type: "function",
						Function: backend.FunctionCall{
							Name:      event.ToolCall.Name,
							Arguments: event.ToolCall.InputJSON,
						},
					}}
				}
			case gateway.StreamEventDone:
				chunk.FinishReason = "stop"
			}

			select {
			case chunks <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()

	return chunks, nil
}

// TokenCount implements backend.Backend.TokenCount.
func (b *backendAdapter) TokenCount(messages []backend.Message) (int, error) {
	// Convert messages
	gReq := gateway.ChatRequest{
		Messages: convertBackendMessagesToGateway(messages),
	}
	return b.provider.TokenCount(gReq)
}

// SupportsTools implements backend.Backend.SupportsTools.
func (b *backendAdapter) SupportsTools() bool {
	return b.provider.SupportsTools()
}

// SupportsJSONMode implements backend.Backend.SupportsJSONMode.
func (b *backendAdapter) SupportsJSONMode() bool {
	return b.provider.SupportsJSONMode()
}

// Model implements backend.Backend.Model.
func (b *backendAdapter) Model() string {
	return b.provider.Model()
}

// --- Conversion Helpers ---

func convertBackendRequestToGateway(req backend.Request) gateway.ChatRequest {
	return gateway.ChatRequest{
		Messages:    convertBackendMessagesToGateway(req.Messages),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Tools:       convertBackendToolsToGateway(req.Tools),
		ToolChoice:  req.ToolChoice,
	}
}

func convertBackendMessagesToGateway(msgs []backend.Message) []gateway.Message {
	result := make([]gateway.Message, len(msgs))
	for i, msg := range msgs {
		result[i] = gateway.Message{
			Role: gateway.Role(msg.Role),
			Content: []gateway.ContentBlock{
				{
					Type: gateway.ContentBlockTypeText,
					Text: msg.Content,
				},
			},
		}
	}
	return result
}

func convertBackendToolsToGateway(tools []backend.Tool) []gateway.ToolDef {
	result := make([]gateway.ToolDef, len(tools))
	for i, tool := range tools {
		result[i] = gateway.ToolDef{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: tool.Function.Parameters,
		}
	}
	return result
}

func convertModelConfigToBinding(mc config.ModelConfig) gateway.Binding {
	provider := gateway.Provider(mc.Provider)
	if provider == "" {
		// Infer from base URL
		if strings.Contains(mc.BaseURL, "anthropic") {
			provider = gateway.ProviderAnthropic
		} else if strings.Contains(mc.BaseURL, "openrouter") {
			provider = gateway.ProviderOpenRouter
		} else if strings.Contains(mc.BaseURL, "fireworks") {
			provider = gateway.ProviderFireworks
		} else if strings.Contains(mc.BaseURL, "11434") {
			provider = gateway.ProviderOllama
		} else if strings.Contains(mc.BaseURL, "1234") {
			provider = gateway.ProviderLMStudio
		} else if strings.Contains(mc.BaseURL, "8000") {
			provider = gateway.ProviderVLLM
		} else {
			provider = gateway.ProviderOpenAI
		}
	}

	binding := gateway.NewBinding(provider, mc.Model)
	binding.Endpoint = mc.BaseURL
	binding.APIKey = mc.APIKey
	
	return binding
}

func convertConfigToBindings(cfg *config.Config, router *gateway.Router) error {
	// Convert each role's model config to a binding
	roles := map[string]config.ModelConfig{
		config.RoleMarshal:   cfg.Model.Marshal,
		config.RoleExecutor: cfg.Model.Executor,
		config.RoleCritic:   cfg.Model.Critic,
		config.RoleCompactor: cfg.Model.Compactor,
	}

	for role, mc := range roles {
		if mc.Model == "" {
			continue
		}
		binding := convertModelConfigToBinding(mc)
		if err := router.RegisterBinding(role, binding); err != nil {
			continue // Skip invalid bindings
		}
	}

	return nil
}
