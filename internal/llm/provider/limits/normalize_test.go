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
