package gateway

import (
	"context"
	"testing"
)

func TestNewRouter(t *testing.T) {
	budget := NewBudgetTracker()
	router := NewRouter(budget)

	if router == nil {
		t.Fatal("NewRouter() returned nil")
	}
	if router.budget != budget {
		t.Error("Router budget not set correctly")
	}
	if router.bindings == nil {
		t.Error("Expected bindings map to be initialized")
	}
}

func TestRouter_RegisterBinding(t *testing.T) {
	budget := NewBudgetTracker()
	router := NewRouter(budget)

	binding := NewBinding(ProviderAnthropic, "claude-opus-4-7")
	err := router.RegisterBinding("orchestrator", binding)

	if err != nil {
		t.Errorf("RegisterBinding() error = %v", err)
	}

	// Verify binding was registered
	got, ok := router.GetBinding("orchestrator")
	if !ok {
		t.Error("GetBinding() returned false for registered binding")
	}
	if got.Provider != ProviderAnthropic {
		t.Errorf("Binding Provider = %v, want %v", got.Provider, ProviderAnthropic)
	}
}

func TestRouter_RegisterBinding_Invalid(t *testing.T) {
	budget := NewBudgetTracker()
	router := NewRouter(budget)

	invalidBinding := Binding{Provider: "invalid", Model: "test"}
	err := router.RegisterBinding("test", invalidBinding)

	if err == nil {
		t.Error("RegisterBinding() expected error for invalid binding")
	}
}

func TestRouter_RegisterFallback(t *testing.T) {
	budget := NewBudgetTracker()
	router := NewRouter(budget)

	primary := NewBinding(ProviderAnthropic, "claude-opus-4-7")
	fallback := NewBinding(ProviderOpenAI, "gpt-4o")

	router.RegisterBinding("orchestrator", primary)
	err := router.RegisterFallback("orchestrator", fallback)

	if err != nil {
		t.Errorf("RegisterFallback() error = %v", err)
	}

	got, ok := router.GetFallback("orchestrator")
	if !ok {
		t.Error("GetFallback() returned false for registered fallback")
	}
	if got.Provider != ProviderOpenAI {
		t.Errorf("Fallback Provider = %v, want %v", got.Provider, ProviderOpenAI)
	}
}

func TestRouter_Resolve(t *testing.T) {
	// Use a very high budget so we don't hit budget exceeded
	budget := NewBudgetTracker(
		WithSessionBudget(1000.0),
	)

	router := NewRouter(budget)

	binding := NewBinding(ProviderAnthropic, "claude-opus-4-7")
	router.RegisterBinding("orchestrator", binding)

	ctx := context.Background()
	resolved, err := router.Resolve(ctx, "orchestrator", 1000)

	if err != nil {
		t.Errorf("Resolve() error = %v", err)
	}
	if !resolved.IsPrimary {
		t.Error("Expected IsPrimary to be true")
	}
	if resolved.IsFallback {
		t.Error("Expected IsFallback to be false")
	}
	if resolved.Binding.Provider != ProviderAnthropic {
		t.Errorf("Binding Provider = %v, want %v", resolved.Binding.Provider, ProviderAnthropic)
	}
}

func TestRouter_Resolve_NotFound(t *testing.T) {
	budget := NewBudgetTracker()
	router := NewRouter(budget)

	ctx := context.Background()
	_, err := router.Resolve(ctx, "unknown-role", 1000)

	if err == nil {
		t.Error("Resolve() expected error for unregistered role")
	}
}

func TestRouter_Resolve_BudgetExceeded(t *testing.T) {
	budget := NewBudgetTracker(
		WithSessionBudget(0.01), // Very low budget
	)

	router := NewRouter(budget)

	binding := NewBinding(ProviderAnthropic, "claude-opus-4-7")
	binding.SetDefaultCosts()
	router.RegisterBinding("orchestrator", binding)

	// Record spending to exhaust budget
	budget.RecordCost("orchestrator", 0.01)

	ctx := context.Background()
	_, err := router.Resolve(ctx, "orchestrator", 1000)

	if err != ErrBudgetExceeded {
		t.Errorf("Resolve() error = %v, want ErrBudgetExceeded", err)
	}
}

func TestRouter_ListRoles(t *testing.T) {
	budget := NewBudgetTracker()
	router := NewRouter(budget)

	// Register multiple bindings
	router.RegisterBinding("orchestrator", NewBinding(ProviderAnthropic, "claude-opus"))
	router.RegisterBinding("codegen", NewBinding(ProviderOpenAI, "gpt-4o"))
	router.RegisterBinding("critic", NewBinding(ProviderAnthropic, "claude-sonnet"))

	roles := router.ListRoles()

	if len(roles) != 3 {
		t.Errorf("ListRoles() returned %d roles, want 3", len(roles))
	}

	// Should be sorted
	expected := []string{"codegen", "critic", "orchestrator"}
	for i, role := range roles {
		if role != expected[i] {
			t.Errorf("ListRoles()[%d] = %v, want %v", i, role, expected[i])
		}
	}
}

func TestRouter_ClearBindings(t *testing.T) {
	budget := NewBudgetTracker()
	router := NewRouter(budget)

	router.RegisterBinding("orchestrator", NewBinding(ProviderAnthropic, "claude-opus"))

	router.ClearBindings()

	roles := router.ListRoles()
	if len(roles) != 0 {
		t.Errorf("ListRoles() after ClearBindings = %v, want empty", roles)
	}
}

func TestRouter_AutoResolve(t *testing.T) {
	budget := NewBudgetTracker()
	router := NewRouter(budget, WithAutoResolve(true))

	// Set available providers (simulating detection)
	providers := []Provider{ProviderOpenAI, ProviderOllama}
	router.SetAvailableProviders(providers)

	// Resolve without explicit binding (should auto-select)
	ctx := context.Background()
	resolved, err := router.Resolve(ctx, "unknown-role", 1000)

	if err != nil {
		t.Errorf("Resolve() error = %v", err)
	}

	// Should have resolved to one of the available providers
	// OpenAI has higher priority (80) than Ollama (50)
	if resolved.Binding.Provider != ProviderOpenAI && resolved.Binding.Provider != ProviderOllama {
		t.Errorf("Auto-resolved Provider = %v, want either openai or ollama", resolved.Binding.Provider)
	}
}

func TestSelectModelForRole(t *testing.T) {
	tests := []struct {
		provider Provider
		role     string
		want     string
	}{
		{ProviderAnthropic, "orchestrator", "claude-opus-4-7"},
		{ProviderAnthropic, "codegen", "claude-sonnet-4-7"},
		{ProviderAnthropic, "critic", "claude-opus-4-7"},
		{ProviderAnthropic, "compactor", "claude-haiku-4-5"},
		{ProviderAnthropic, "unknown", "claude-sonnet-4-7"},
		{ProviderOpenAI, "orchestrator", "gpt-4o"},
		{ProviderOpenAI, "codegen", "gpt-4o"},
		{ProviderOllama, "codegen", "qwen2.5-coder:14b"},
		{ProviderOllama, "orchestrator", "deepseek-r1:32b"},
	}

	for _, tt := range tests {
		t.Run(tt.provider.String()+"/"+tt.role, func(t *testing.T) {
			got := selectModelForRole(tt.provider, tt.role)
			if got != tt.want {
				t.Errorf("selectModelForRole(%s, %s) = %v, want %v", tt.provider, tt.role, got, tt.want)
			}
		})
	}
}
