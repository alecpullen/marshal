package config

import (
	"testing"

	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

func TestRenameProviderRekeysProviderAndPresets(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
		},
		Models: ModelsConfig{Presets: map[string]routing.ModelPreset{
			"ollama/qwen": {Provider: "ollama", Model: "qwen2.5-coder:7b"},
			"other/gpt":   {Provider: "other", Model: "gpt-4o"},
		}},
	}
	discovered := map[string][]schema.ModelInfo{
		"ollama": {{ID: "qwen2.5-coder:7b"}},
	}

	if err := RenameProvider(&cfg, "ollama", "my-ollama", discovered); err != nil {
		t.Fatalf("RenameProvider: %v", err)
	}

	if _, ok := cfg.Providers["ollama"]; ok {
		t.Fatal("old provider key should be deleted")
	}
	if _, ok := cfg.Providers["my-ollama"]; !ok {
		t.Fatal("new provider key should exist")
	}
	if cfg.Models.Presets["my-ollama/qwen"].Provider != "my-ollama" {
		t.Fatalf("preset provider not updated: %+v", cfg.Models.Presets["my-ollama/qwen"])
	}
	if _, ok := cfg.Models.Presets["ollama/qwen"]; ok {
		t.Fatal("old pair-keyed preset should be renamed")
	}
	if _, ok := cfg.Models.Presets["other/gpt"]; !ok {
		t.Fatal("unrelated preset should be preserved")
	}
	if _, ok := discovered["my-ollama"]; !ok {
		t.Fatal("discovered entry should be renamed")
	}
	if _, ok := discovered["ollama"]; ok {
		t.Fatal("old discovered entry should be deleted")
	}
}

func TestRenameProviderRejectsEmptyAndCollision(t *testing.T) {
	cfg := Config{Providers: map[string]ProviderConfig{
		"a": {}, "b": {},
	}}
	if err := RenameProvider(&cfg, "a", "", nil); err == nil {
		t.Fatal("empty name should error")
	}
	if err := RenameProvider(&cfg, "a", "b", nil); err == nil {
		t.Fatal("collision should error")
	}
	if err := RenameProvider(&cfg, "a", "a", nil); err != nil {
		t.Fatal("rename to same name should be a no-op")
	}
}
