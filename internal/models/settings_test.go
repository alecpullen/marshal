package models_test

import (
	"testing"

	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/models"
)

func TestLoadDefault(t *testing.T) {
	reg, err := models.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault failed: %v", err)
	}
	if reg == nil {
		t.Fatal("LoadDefault returned nil registry")
	}
}

func TestRegistry_Lookup_ExactMatch(t *testing.T) {
	data := `
[models.gpt-4o]
name = "GPT-4o"
supports_tools = true
edit_format = "wholefile"
max_tokens = 4096
`
	reg, err := models.Load([]byte(data))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	ms := reg.Lookup("gpt-4o")
	if ms.Name != "GPT-4o" {
		t.Errorf("Name: got %q, want %q", ms.Name, "GPT-4o")
	}
	if !ms.SupportsTools {
		t.Error("expected SupportsTools=true")
	}
	if ms.EditFormat != "wholefile" {
		t.Errorf("EditFormat: got %q, want %q", ms.EditFormat, "wholefile")
	}
}

func TestRegistry_Lookup_PatternMatch(t *testing.T) {
	data := `
[models.ollama]
pattern = "ollama/*"
supports_tools = false
edit_format = "search-replace"
max_tokens = 2048
`
	reg, err := models.Load([]byte(data))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	ms := reg.Lookup("ollama/llama3.1")
	if ms.Pattern != "ollama/*" {
		t.Errorf("Pattern: got %q, want %q", ms.Pattern, "ollama/*")
	}
	if ms.SupportsTools {
		t.Error("expected SupportsTools=false for ollama pattern")
	}
	if ms.EditFormat != "search-replace" {
		t.Errorf("EditFormat: got %q, want %q", ms.EditFormat, "search-replace")
	}
}

func TestRegistry_Lookup_DefaultsForUnknown(t *testing.T) {
	reg, err := models.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault failed: %v", err)
	}

	// Unknown model should return defaults
	ms := reg.Lookup("unknown-model-xyz")
	if ms.Name != "unknown" {
		t.Errorf("Name: got %q, want %q", ms.Name, "unknown")
	}
	if ms.SupportsTools {
		t.Error("expected default SupportsTools=false")
	}
	if ms.EditFormat != "wholefile" {
		t.Errorf("EditFormat: got %q, want %q", ms.EditFormat, "wholefile")
	}
}

func TestRegistry_Lookup_NilRegistry(t *testing.T) {
	var reg *models.Registry
	ms := reg.Lookup("anything")
	if ms.Name != "unknown" {
		t.Errorf("Name: got %q, want %q", ms.Name, "unknown")
	}
}

func TestModelSettings_GetConfigEditFormat(t *testing.T) {
	tests := []struct {
		format   string
		expected config.EditFormat
	}{
		{"wholefile", config.EditFormatWholeFile},
		{"search-replace", config.EditFormatSearchReplace},
		{"udiff", config.EditFormatUdiff},
		{"", config.EditFormatWholeFile}, // default
	}

	for _, tt := range tests {
		ms := models.ModelSettings{EditFormat: tt.format}
		got := ms.GetConfigEditFormat()
		if got != tt.expected {
			t.Errorf("EditFormat=%q: got %v, want %v", tt.format, got, tt.expected)
		}
	}
}

func TestContextWindowFor(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"gpt-4o", 128000},
		{"claude-3-5-sonnet", 200000},
		{"qwen3-coder", 32768},
		{"deepseek-coder", 16384},
		{"ollama/llama3.1", 8192},         // pattern match
		{"unknown-model-xyz", 8192},       // default
	}
	for _, tt := range tests {
		if got := models.ContextWindowFor(tt.model); got != tt.want {
			t.Errorf("ContextWindowFor(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestDefaultSettings(t *testing.T) {
	ms := models.DefaultSettings()
	if ms.Name != "unknown" {
		t.Errorf("Name: got %q, want %q", ms.Name, "unknown")
	}
	if ms.SupportsTools {
		t.Error("expected SupportsTools=false")
	}
	if !ms.SupportsJSON {
		t.Error("expected SupportsJSON=true")
	}
	if ms.EditFormat != "wholefile" {
		t.Errorf("EditFormat: got %q, want %q", ms.EditFormat, "wholefile")
	}
	if ms.MaxTokens != 4096 {
		t.Errorf("MaxTokens: got %d, want %d", ms.MaxTokens, 4096)
	}
	if ms.ContextWindow != 8192 {
		t.Errorf("ContextWindow: got %d, want %d", ms.ContextWindow, 8192)
	}
}
