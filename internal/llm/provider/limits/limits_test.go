package limits

import (
	"testing"
)

func TestNormalizeOpenRouter(t *testing.T) {
	body := []byte(`{
		"data": [
			{"id": "deepseek/deepseek-v4-flash", "context_length": 1048576, "top_provider": {"context_length": 1048576, "max_completion_tokens": null}}
		]
	}`)
	table, err := NormalizeOpenRouter(body)
	if err != nil {
		t.Fatalf("NormalizeOpenRouter: %v", err)
	}
	lim, ok := table["deepseek/deepseek-v4-flash"]
	if !ok {
		t.Fatal("expected deepseek/deepseek-v4-flash entry")
	}
	if lim.ContextWindow != 1048576 {
		t.Errorf("ContextWindow = %d, want 1048576", lim.ContextWindow)
	}
	if lim.MaxOutputTokens != 0 {
		t.Errorf("MaxOutputTokens = %d, want 0", lim.MaxOutputTokens)
	}
}

func TestNormalizeLiteLLM(t *testing.T) {
	body := []byte(`{
		"azure_ai/deepseek-v4-flash": {"max_tokens": 384000, "max_input_tokens": 1000000, "max_output_tokens": 384000},
		"deepseek-v4-flash": {"max_tokens": 8192, "max_input_tokens": 1000000, "max_output_tokens": 8192}
	}`)
	table, err := NormalizeLiteLLM(body)
	if err != nil {
		t.Fatalf("NormalizeLiteLLM: %v", err)
	}
	lim, ok := table["azure_ai/deepseek-v4-flash"]
	if !ok {
		t.Fatal("expected azure_ai/deepseek-v4-flash entry")
	}
	if lim.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000", lim.ContextWindow)
	}
	if lim.MaxOutputTokens != 384000 {
		t.Errorf("MaxOutputTokens = %d, want 384000", lim.MaxOutputTokens)
	}
}

func TestFetchReturnsData(t *testing.T) {
	// Smoke test: both public endpoints should return non-empty tables.
	table, err := Fetch(t.Context(), "openrouter")
	if err != nil {
		t.Fatalf("Fetch openrouter: %v", err)
	}
	if len(table) == 0 {
		t.Fatal("openrouter table is empty")
	}

	table, err = Fetch(t.Context(), "litellm")
	if err != nil {
		t.Fatalf("Fetch litellm: %v", err)
	}
	if len(table) == 0 {
		t.Fatal("litellm table is empty")
	}
}
