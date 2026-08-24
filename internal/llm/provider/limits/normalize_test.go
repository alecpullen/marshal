package limits

import "testing"

func TestNorm(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "gpt-4o", "gpt-4o"},
		{"uppercase", "GPT-4o", "gpt-4o"},
		{"vendor prefix", "anthropic/claude-sonnet-4.5", "claude-sonnet-4-5"},
		{"dotted version", "claude-3.5-sonnet", "claude-3-5-sonnet"},
		{"date suffix dash", "claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
		{"date suffix at", "gemini-2.0-flash@20250101", "gemini-2-0-flash"},
		{"openai date", "gpt-4o-2024-11-20", "gpt-4o-2024-11-20"},
		{"free tag", "deepseek/deepseek-r1:free", "deepseek-r1"},
		{"latest tag dash", "mistral-large-latest", "mistral-large"},
		{"latest tag colon", "llama3.1:latest", "llama3-1"},
		{"ollama size tag kept", "qwen2.5-coder:7b", "qwen2-5-coder:7b"},
		{"collapse dashes", "foo--bar", "foo-bar"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := norm(tt.in); got != tt.want {
				t.Errorf("norm(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStripRoleSuffix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"kimi-k2-instruct", "kimi-k2"},
		{"qwen-chat", "qwen"},
		{"gemma-it", "gemma"},
		{"gpt-4o", "gpt-4o"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := stripRoleSuffix(tt.in); got != tt.want {
				t.Errorf("stripRoleSuffix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeOpenRouterReasoning(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"acme/thinker","context_length":1000,"supported_parameters":["tools","reasoning"]},
		{"id":"acme/plain","context_length":1000,"supported_parameters":["tools"]},
		{"id":"acme/unreported","context_length":1000}
	]}`)
	out, err := NormalizeOpenRouter(body)
	if err != nil {
		t.Fatalf("NormalizeOpenRouter: %v", err)
	}
	if r := out["acme/thinker"].Reasoning; r == nil || !*r {
		t.Fatalf("thinker Reasoning = %v, want true", r)
	}
	if r := out["acme/plain"].Reasoning; r == nil || *r {
		t.Fatalf("plain Reasoning = %v, want false", r)
	}
	if r := out["acme/unreported"].Reasoning; r != nil {
		t.Fatalf("unreported Reasoning = %v, want nil", r)
	}
}

func TestNormalizeLiteLLMReasoning(t *testing.T) {
	body := []byte(`{
		"acme/thinker": {"max_input_tokens": 1000, "supports_reasoning": true},
		"acme/only-reasoning": {"supports_reasoning": false}
	}`)
	out, err := NormalizeLiteLLM(body)
	if err != nil {
		t.Fatalf("NormalizeLiteLLM: %v", err)
	}
	if r := out["acme/thinker"].Reasoning; r == nil || !*r {
		t.Fatalf("thinker Reasoning = %v, want true", r)
	}
	// An entry carrying only a reasoning signal is still kept.
	if r := out["acme/only-reasoning"].Reasoning; r == nil || *r {
		t.Fatalf("only-reasoning Reasoning = %v, want false", r)
	}
}
