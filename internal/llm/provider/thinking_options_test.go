package provider

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/schema"
)

func TestValidEffortLevel(t *testing.T) {
	tests := []struct {
		name  string
		opts  *schema.ThinkingOptions
		level string
		want  bool
	}{
		{"empty level always valid", &schema.ThinkingOptions{Levels: []string{"low"}}, "", true},
		{"nil options accept any level", nil, "high", true},
		{"listed level accepted", &schema.ThinkingOptions{Levels: []string{"low", "high"}}, "high", true},
		{"unlisted level rejected", &schema.ThinkingOptions{Levels: []string{"low", "high"}}, "max", false},
		{"off rejected when unlisted", &schema.ThinkingOptions{Levels: []string{"low", "high"}}, "off", false},
		{"off accepted when listed", &schema.ThinkingOptions{Levels: []string{"low", "off"}}, "off", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidEffortLevel(tt.opts, tt.level); got != tt.want {
				t.Errorf("ValidEffortLevel(%v, %q) = %v, want %v", tt.opts, tt.level, got, tt.want)
			}
		})
	}
}

func TestThinkingOptionsForOverrideWins(t *testing.T) {
	dir := t.TempDir()
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "https://a/v1"}
	cached := []schema.ModelInfo{
		{ID: "gpt-4o", Thinking: &schema.ThinkingOptions{Levels: []string{"low", "medium", "high"}, Mode: "effort"}},
	}
	if err := modelcache.Save(dir, modelcache.Cache{Providers: map[string]modelcache.Entry{
		"openai": {
			ConfigHash: modelcache.HashProvider(pc),
			Models:     cached,
			FetchedAt:  time.Now(),
		},
	}}); err != nil {
		t.Fatalf("save cache: %v", err)
	}

	override := &schema.ThinkingOptions{Levels: []string{"max"}, Mode: "effort"}
	got := ThinkingOptionsFor(dir, "openai", pc, "gpt-4o", override)
	if got == nil || len(got.Levels) != 1 || got.Levels[0] != "max" {
		t.Fatalf("override should win over cache, got %+v", got)
	}
}
