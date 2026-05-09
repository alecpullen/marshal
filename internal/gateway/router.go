package gateway

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Router manages role-to-binding resolution with fallback and budget checking.
type Router struct {
	mu sync.RWMutex

	// bindings maps role -> primary binding
	bindings map[string]Binding

	// fallback maps role -> fallback binding (used on unrecoverable errors)
	fallback map[string]Binding

	// budget tracks per-session and per-role spending
	budget *BudgetTracker

	// registry of available providers
	providers *ProviderRegistry

	// autoResolve enables automatic binding selection based on availability
	autoResolve bool

	// availableProviders is updated by detector
	availableProviders map[Provider]bool
}

// RouterOption configures the router.
type RouterOption func(*Router)

// WithAutoResolve enables automatic binding selection based on detected providers.
func WithAutoResolve(enable bool) RouterOption {
	return func(r *Router) {
		r.autoResolve = enable
	}
}

// WithProviderRegistry sets the provider registry.
func WithProviderRegistry(reg *ProviderRegistry) RouterOption {
	return func(r *Router) {
		r.providers = reg
	}
}

// NewRouter creates a new router with optional configuration.
func NewRouter(budget *BudgetTracker, opts ...RouterOption) *Router {
	r := &Router{
		bindings:           make(map[string]Binding),
		fallback:           make(map[string]Binding),
		budget:             budget,
		providers:          NewProviderRegistry(),
		autoResolve:        false,
		availableProviders: make(map[Provider]bool),
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// RegisterBinding registers a primary binding for a role.
func (r *Router) RegisterBinding(role string, binding Binding) error {
	if err := binding.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.bindings[role] = binding
	return nil
}

// RegisterFallback registers a fallback binding for a role.
func (r *Router) RegisterFallback(role string, binding Binding) error {
	if err := binding.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.fallback[role] = binding
	return nil
}

// SetAvailableProviders updates the set of available providers (from detector).
func (r *Router) SetAvailableProviders(providers []Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.availableProviders = make(map[Provider]bool)
	for _, p := range providers {
		r.availableProviders[p] = true
	}
}

// Resolve determines the appropriate binding for a role.
// It checks budget, applies auto-resolution if enabled, and returns the binding.
func (r *Router) Resolve(ctx context.Context, role string, estTokens int) (ResolvedBinding, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. Check if we have an explicit binding for this role
	binding, hasExplicit := r.bindings[role]

	// 2. If auto-resolve is enabled and no explicit binding, select based on availability
	if !hasExplicit && r.autoResolve {
		var err error
		binding, err = r.autoResolveBinding(role)
		if err != nil {
			return ResolvedBinding{}, err
		}
		hasExplicit = true // We now have a binding from auto-resolution
	}

	// 3. If still no binding, return error
	if !hasExplicit {
		return ResolvedBinding{}, fmt.Errorf("no binding registered for role %q", role)
	}

	// 4. Check budget for this role
	if err := r.budget.CheckRole(role, binding.EstimateCost(estTokens, estTokens/2)); err != nil {
		// Budget exceeded - try fallback if available
		if fallback, hasFallback := r.fallback[role]; hasFallback {
			if fbErr := r.budget.CheckRole(role, fallback.EstimateCost(estTokens, estTokens/2)); fbErr == nil {
				return ResolvedBinding{
					Binding:    fallback,
					IsPrimary:  false,
					IsFallback: true,
					Reason:     fmt.Sprintf("primary binding exceeded budget, using fallback"),
				}, nil
			}
		}
		return ResolvedBinding{}, ErrBudgetExceeded
	}

	return ResolvedBinding{
		Binding:    binding,
		IsPrimary:  true,
		IsFallback: false,
		Reason:     "primary binding selected",
	}, nil
}

// ResolveWithFallback attempts to resolve a binding, and on unrecoverable error,
// automatically tries the fallback binding.
func (r *Router) ResolveWithFallback(ctx context.Context, role string, estTokens int) (ResolvedBinding, error) {
	resolved, err := r.Resolve(ctx, role, estTokens)
	if err != nil {
		return ResolvedBinding{}, err
	}

	// If primary failed due to unrecoverable error, try fallback
	if !resolved.IsFallback && resolved.Binding.IsCloud() {
		// Pre-check if provider seems unavailable (would need integration with health check)
		// For now, just return the primary - caller handles fallback on actual error
	}

	return resolved, nil
}

// autoResolveBinding selects the best available binding based on priority.
// Priority order: Anthropic > OpenAI > Local (but user preference overrides).
func (r *Router) autoResolveBinding(role string) (Binding, error) {
	// Define the priority order for auto-selection
	priorityOrder := []Provider{
		ProviderAnthropic,  // Highest priority
		ProviderOpenAI,
		ProviderOpenRouter,
		ProviderFireworks,
		ProviderRunPod,
		ProviderVLLM,
		ProviderOllama,
		ProviderLMStudio, // Lowest priority
	}

	// Find the highest priority available provider
	for _, provider := range priorityOrder {
		if r.availableProviders[provider] {
			// Select appropriate model based on role and role hint
			model := selectModelForRole(provider, role)
			binding := NewBinding(provider, model)

			// Try to resolve API key
			if binding.IsCloud() {
				apiKey, err := ResolveAuth(string(provider))
				if err == nil && apiKey != "" {
					binding.APIKey = apiKey
					return binding, nil
				}
				// No API key, continue to next provider
				continue
			}

			// Local provider doesn't need API key
			return binding, nil
		}
	}

	return Binding{}, fmt.Errorf("no available providers found for role %q", role)
}

// selectModelForRole picks an appropriate model based on provider and role.
func selectModelForRole(provider Provider, role string) string {
	// Default models by provider
	defaults := map[Provider]map[string]string{
		ProviderAnthropic: {
			"orchestrator": "claude-opus-4-7",    // Extended thinking for orchestration
			"codegen":      "claude-sonnet-4-7", // Good balance for coding
			"critic":       "claude-opus-4-7",   // Quality review
			"compactor":    "claude-haiku-4-5",  // Fast, cheap
			"default":      "claude-sonnet-4-7",
		},
		ProviderOpenAI: {
			"orchestrator": "gpt-4o",
			"codegen":      "gpt-4o",
			"critic":       "gpt-4o",
			"compactor":    "gpt-4o-mini",
			"default":      "gpt-4o",
		},
		ProviderOllama: {
			"orchestrator": "deepseek-r1:32b",
			"codegen":      "qwen2.5-coder:14b",
			"critic":       "deepseek-r1:32b",
			"compactor":    "qwen2.5:7b",
			"default":      "qwen2.5-coder:14b",
		},
		ProviderLMStudio: {
			"default": "local-model",
		},
		ProviderVLLM: {
			"default": "local-model",
		},
	}

	if providerDefaults, ok := defaults[provider]; ok {
		if model, ok := providerDefaults[role]; ok {
			return model
		}
		return providerDefaults["default"]
	}

	return "default"
}

// RecordUsage records token usage against the budget.
func (r *Router) RecordUsage(ctx context.Context, role string, binding Binding, usage Usage) {
	r.budget.Record(role, binding, usage)
}

// GetBinding retrieves the current binding for a role (without budget check).
func (r *Router) GetBinding(role string) (Binding, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	binding, ok := r.bindings[role]
	return binding, ok
}

// GetFallback retrieves the fallback binding for a role.
func (r *Router) GetFallback(role string) (Binding, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	binding, ok := r.fallback[role]
	return binding, ok
}

// ListRoles returns all registered roles.
func (r *Router) ListRoles() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	roles := make([]string, 0, len(r.bindings))
	for role := range r.bindings {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

// ClearBindings removes all registered bindings.
func (r *Router) ClearBindings() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.bindings = make(map[string]Binding)
	r.fallback = make(map[string]Binding)
}

// --- Auto-configuration from detected providers ---

// AutoConfigure configures bindings automatically from detected providers.
// This is called after provider detection at startup.
func (r *Router) AutoConfigure(detected []DetectedProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Convert detected providers to available map
	providers := make([]Provider, 0, len(detected))
	for _, d := range detected {
		providers = append(providers, Provider(d.Name))
	}
	r.SetAvailableProviders(providers)

	// Enable auto-resolve
	r.autoResolve = true

	return nil
}

// DetectedProvider represents a detected provider from the environment.
type DetectedProvider struct {
	Name            string   // Provider name
	Endpoint        string   // Detected endpoint
	AuthAvailable   bool     // Whether auth is available
	AvailableModels []string // Models available (for local servers)
}

// --- Provider Health Tracking ---

// ProviderHealth tracks the health status of providers.
type ProviderHealth struct {
	Provider    Provider
	Available   bool
	LastChecked time.Time
	LastError   error
	ConsecutiveErrors int
}

// HealthChecker periodically checks provider health.
type HealthChecker struct {
	router   *Router
	interval time.Duration
	stop     chan struct{}
}

// NewHealthChecker creates a health checker.
func NewHealthChecker(router *Router, interval time.Duration) *HealthChecker {
	return &HealthChecker{
		router:   router,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start begins health checking.
func (h *HealthChecker) Start() {
	go h.run()
}

// Stop halts health checking.
func (h *HealthChecker) Stop() {
	close(h.stop)
}

func (h *HealthChecker) run() {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.checkProviders()
		case <-h.stop:
			return
		}
	}
}

func (h *HealthChecker) checkProviders() {
	// Implementation would check each provider's health
	// For now, this is a placeholder
}
