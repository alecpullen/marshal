package gateway

import (
	"testing"
)

func TestProvider_IsValid(t *testing.T) {
	tests := []struct {
		provider Provider
		valid    bool
	}{
		{ProviderAnthropic, true},
		{ProviderOpenAI, true},
		{ProviderOpenRouter, true},
		{ProviderFireworks, true},
		{ProviderRunPod, true},
		{ProviderOllama, true},
		{ProviderLMStudio, true},
		{ProviderVLLM, true},
		{Provider("unknown"), false},
		{Provider(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			got := tt.provider.IsValid()
			if got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestProvider_SupportsThinking(t *testing.T) {
	tests := []struct {
		provider Provider
		thinking bool
	}{
		{ProviderAnthropic, true},
		{ProviderOpenAI, false},
		{ProviderOllama, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			got := tt.provider.SupportsThinking()
			if got != tt.thinking {
				t.Errorf("SupportsThinking() = %v, want %v", got, tt.thinking)
			}
		})
	}
}

func TestNewBinding(t *testing.T) {
	binding := NewBinding(ProviderAnthropic, "claude-opus-4-7")

	if binding.Provider != ProviderAnthropic {
		t.Errorf("Provider = %v, want %v", binding.Provider, ProviderAnthropic)
	}
	if binding.Model != "claude-opus-4-7" {
		t.Errorf("Model = %v, want %v", binding.Model, "claude-opus-4-7")
	}
	if binding.Priority != 100 {
		t.Errorf("Priority = %v, want %v", binding.Priority, 100)
	}
}

func TestBinding_String(t *testing.T) {
	tests := []struct {
		binding Binding
		want    string
	}{
		{
			NewBinding(ProviderAnthropic, "claude-opus-4-7"),
			"anthropic/claude-opus-4-7",
		},
		{
			NewBinding(ProviderAnthropic, "claude-opus-4-7").WithEndpoint("https://custom.api.com"),
			"anthropic/claude-opus-4-7@https://custom.api.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.binding.String()
			if got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBinding_Validate(t *testing.T) {
	tests := []struct {
		name    string
		binding Binding
		wantErr bool
	}{
		{
			"valid anthropic",
			NewBinding(ProviderAnthropic, "claude-opus-4-7"),
			false,
		},
		{
			"valid openai",
			NewBinding(ProviderOpenAI, "gpt-4o"),
			false,
		},
		{
			"empty provider",
			Binding{Provider: "", Model: "test"},
			true,
		},
		{
			"empty model",
			NewBinding(ProviderAnthropic, ""),
			true,
		},
		{
			"invalid provider",
			Binding{Provider: "unknown", Model: "test"},
			true,
		},
		{
			"too many loras",
			Binding{Provider: ProviderAnthropic, Model: "test", LoRAs: []string{"lora1", "lora2"}},
			true,
		},
		{
			"lora on unsupported provider",
			Binding{Provider: ProviderAnthropic, Model: "test", LoRAs: []string{"lora1"}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.binding.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBinding_EstimateCost(t *testing.T) {
	binding := NewBinding(ProviderAnthropic, "claude-opus-4-7")
	binding.SetDefaultCosts()

	// Opus: $15/1K input, $75/1K output
	// 1000 input tokens = $0.015
	// 500 output tokens = $0.0375
	// Total = $0.0525
	
	cost := binding.EstimateCost(1000, 500)
	
	// Opus: $15/1K input, $75/1K output
	// 1000 input tokens = $15.00
	// 500 output tokens = $37.50
	// Total = $52.50
	
	// Should be approximately 52.5
	if cost < 50.0 || cost > 55.0 {
		t.Errorf("EstimateCost(1000, 500) = %v, want approximately 52.5", cost)
	}
}

func TestBinding_IsLocal(t *testing.T) {
	tests := []struct {
		binding Binding
		local   bool
	}{
		{NewBinding(ProviderOllama, "qwen2.5"), true},
		{NewBinding(ProviderLMStudio, "local"), true},
		{NewBinding(ProviderVLLM, "local"), true},
		{NewBinding(ProviderAnthropic, "claude-opus"), false},
		{NewBinding(ProviderOpenAI, "gpt-4o"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.binding.Provider), func(t *testing.T) {
			got := tt.binding.IsLocal()
			if got != tt.local {
				t.Errorf("IsLocal() = %v, want %v", got, tt.local)
			}
		})
	}
}

func TestBinding_SupportsTools(t *testing.T) {
	tests := []struct {
		binding Binding
		tools   bool
	}{
		{NewBinding(ProviderAnthropic, "claude-opus"), true},
		{NewBinding(ProviderOpenAI, "gpt-4o"), true},
		{NewBinding(ProviderOllama, "qwen2.5-coder"), true},
		{NewBinding(ProviderOllama, "llama2"), false},
	}

	for _, tt := range tests {
		t.Run(tt.binding.String(), func(t *testing.T) {
			got := tt.binding.SupportsTools()
			if got != tt.tools {
				t.Errorf("SupportsTools() = %v, want %v", got, tt.tools)
			}
		})
	}
}

func TestDefaultPriority(t *testing.T) {
	tests := []struct {
		provider Provider
		priority int
	}{
		{ProviderAnthropic, 100},
		{ProviderOpenAI, 80},
		{ProviderOpenRouter, 70},
		{ProviderFireworks, 60},
		{ProviderRunPod, 55},
		{ProviderOllama, 50},
		{ProviderLMStudio, 50},
		{ProviderVLLM, 50},
		{Provider("unknown"), 50},
	}

	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			got := DefaultPriority(tt.provider)
			if got != tt.priority {
				t.Errorf("DefaultPriority(%s) = %v, want %v", tt.provider, got, tt.priority)
			}
		})
	}
}

func TestRoleHint_IsValid(t *testing.T) {
	tests := []struct {
		hint  RoleHint
		valid bool
	}{
		{RoleHintSmall, true},
		{RoleHintCode, true},
		{RoleHintLarge, true},
		{RoleHintFast, true},
		{RoleHint("unknown"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.hint), func(t *testing.T) {
			got := tt.hint.IsValid()
			if got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestResolvedBinding(t *testing.T) {
	rb := ResolvedBinding{
		Binding:     NewBinding(ProviderAnthropic, "claude-opus"),
		IsPrimary:   true,
		IsFallback:  false,
		Reason:      "primary binding selected",
	}

	if !rb.IsPrimary {
		t.Error("Expected IsPrimary to be true")
	}
	if rb.IsFallback {
		t.Error("Expected IsFallback to be false")
	}
}
