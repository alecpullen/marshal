package embedding

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func TestNewFromConfigSelectsBackendByType(t *testing.T) {
	ollama, err := NewFromConfig("ollama", config.ProviderConfig{Type: "ollama", BaseURL: "http://localhost:11434"}, "nomic-embed-text")
	if err != nil {
		t.Fatalf("ollama: %v", err)
	}
	if _, ok := ollama.(*ollamaEmbedder); !ok {
		t.Fatalf("type=ollama built %T, want *ollamaEmbedder", ollama)
	}

	for _, typ := range []string{"", "openai_compatible"} {
		e, err := NewFromConfig("oai", config.ProviderConfig{Type: typ, BaseURL: "http://localhost:1234"}, "m")
		if err != nil {
			t.Fatalf("type=%q: %v", typ, err)
		}
		if _, ok := e.(*openAIEmbedder); !ok {
			t.Fatalf("type=%q built %T, want *openAIEmbedder", typ, e)
		}
	}
}

func TestNewFromConfigRejectsUnknownType(t *testing.T) {
	_, err := NewFromConfig("x", config.ProviderConfig{Type: "weird"}, "m")
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("want unsupported type error, got %v", err)
	}
}

func TestNewFromConfigRequiresModel(t *testing.T) {
	_, err := NewFromConfig("x", config.ProviderConfig{Type: "ollama"}, "")
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("want model-required error, got %v", err)
	}
}
