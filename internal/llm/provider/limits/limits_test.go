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
	lim, ok := tbl.Lookup("azure_ai", "deepseek-v4-flash")
	if !ok {
		t.Fatal("expected exact match")
	}
	if lim.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000", lim.ContextWindow)
	}
}

func TestTableLookupModelNameFallback(t *testing.T) {
	tbl := NewTable(map[string]Limit{
		"deepseek-v4-flash": {ContextWindow: 8192, MaxOutputTokens: 8192},
	})
	lim, ok := tbl.Lookup("ollama-cloud", "deepseek-v4-flash")
	if !ok {
		t.Fatal("expected model-name fallback match")
	}
	if lim.ContextWindow != 8192 {
		t.Errorf("ContextWindow = %d, want 8192", lim.ContextWindow)
	}
}

func TestTableLookupMissing(t *testing.T) {
	tbl := NewTable(map[string]Limit{})
	_, ok := tbl.Lookup("unknown", "unknown")
	if ok {
		t.Error("expected no match")
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
