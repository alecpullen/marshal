package limits

import (
	"testing"
	"time"
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

func TestTableLookupExactProviderModel(t *testing.T) {
	tbl := NewTable(map[string]Limit{
		"azure_ai/deepseek-v4-flash": {ContextWindow: 1000000, MaxOutputTokens: 384000},
		"deepseek-v4-flash":          {ContextWindow: 8192, MaxOutputTokens: 8192},
	})
	lim, kind := tbl.Lookup("azure_ai", "deepseek-v4-flash")
	if kind != MatchExact {
		t.Fatalf("kind = %v, want MatchExact", kind)
	}
	if lim.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000", lim.ContextWindow)
	}
}

func TestTableLookupModelNameFallback(t *testing.T) {
	tbl := NewTable(map[string]Limit{
		"deepseek-v4-flash": {ContextWindow: 8192, MaxOutputTokens: 8192},
	})
	lim, kind := tbl.Lookup("ollama-cloud", "deepseek-v4-flash")
	if kind != MatchExact {
		t.Fatalf("kind = %v, want MatchExact", kind)
	}
	if lim.ContextWindow != 8192 {
		t.Errorf("ContextWindow = %d, want 8192", lim.ContextWindow)
	}
}

func TestTableLookupMissing(t *testing.T) {
	tbl := NewTable(map[string]Limit{})
	_, kind := tbl.Lookup("unknown", "unknown")
	if kind != MatchNone {
		t.Errorf("kind = %v, want MatchNone", kind)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	tbl := map[string]Limit{
		"deepseek/deepseek-v4-flash": {ContextWindow: 1048576, MaxOutputTokens: 0},
	}
	if err := Save(tmp, Cache{Table: tbl, FetchedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cache, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := cache.Table["deepseek/deepseek-v4-flash"]
	if !ok {
		t.Fatal("missing cached entry")
	}
	if got.ContextWindow != 1048576 {
		t.Errorf("ContextWindow = %d, want 1048576", got.ContextWindow)
	}
}

func TestNormalizeOpenRouterParsesToolCalling(t *testing.T) {
	data := []byte(`{
		"data": [
			{
				"id": "openai/gpt-4o",
				"context_length": 128000,
				"supported_parameters": ["tools", "tool_choice", "response_format"],
				"top_provider": {"context_length": 128000, "max_completion_tokens": 16384}
			},
			{
				"id": "some/no-tools-model",
				"context_length": 8192,
				"supported_parameters": ["response_format"],
				"top_provider": {"context_length": 8192, "max_completion_tokens": 4096}
			}
		]
	}`)

	out, err := NormalizeOpenRouter(data)
	if err != nil {
		t.Fatalf("NormalizeOpenRouter: %v", err)
	}

	withTools, ok := out["openai/gpt-4o"]
	if !ok {
		t.Fatalf("missing entry for openai/gpt-4o")
	}
	if withTools.ToolCalling == nil || !*withTools.ToolCalling {
		t.Errorf("openai/gpt-4o: want ToolCalling=true, got %v", withTools.ToolCalling)
	}

	withoutTools, ok := out["some/no-tools-model"]
	if !ok {
		t.Fatalf("missing entry for some/no-tools-model")
	}
	if withoutTools.ToolCalling == nil || *withoutTools.ToolCalling {
		t.Errorf("some/no-tools-model: want ToolCalling=false, got %v", withoutTools.ToolCalling)
	}
}
