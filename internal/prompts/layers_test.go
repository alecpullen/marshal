package prompts_test

import (
	"strings"
	"testing"

	"github.com/alecpullen/marshal/internal/prompts"
)

func TestAssemble_BaseOnly(t *testing.T) {
	result := prompts.AssembleWithSecurity("base prompt", "", "")
	if result != "base prompt" {
		t.Errorf("expected base only, got %q", result)
	}
}

func TestAssemble_WithSecurity(t *testing.T) {
	result := prompts.AssembleWithSecurity("base prompt", "security rules", "")
	expected := "base prompt\n\nsecurity rules"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestAssemble_WithExtra(t *testing.T) {
	result := prompts.AssembleWithSecurity("base prompt", "", "extra instructions")
	expected := "base prompt\n\nextra instructions"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestAssemble_AllLayers(t *testing.T) {
	result := prompts.AssembleWithSecurity("base prompt", "security rules", "extra instructions")
	expected := "base prompt\n\nsecurity rules\n\nextra instructions"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestAssemble_TrimsWhitespace(t *testing.T) {
	result := prompts.AssembleWithSecurity("  base  ", "  security  ", "  extra  ")
	expected := "base\n\nsecurity\n\nextra"
	if result != expected {
		t.Errorf("expected trimming, got %q", result)
	}
}

func TestAssemble_EmptyBase(t *testing.T) {
	result := prompts.AssembleWithSecurity("", "security", "extra")
	expected := "security\n\nextra"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestAssemble_EmbeddedSecurity(t *testing.T) {
	// The actual Security content is embedded at build time
	result := prompts.Assemble("base prompt", "extra instructions")
	// Should contain the base, then security layer, then extra
	if !strings.Contains(result, "base prompt") {
		t.Error("expected result to contain base prompt")
	}
	if !strings.Contains(result, "extra instructions") {
		t.Error("expected result to contain extra instructions")
	}
	// Security layer should be present (from embedded file)
	if !strings.Contains(result, "Security") {
		t.Error("expected result to contain Security layer content")
	}
}

func TestEmbeddedPromptsExist(t *testing.T) {
	// Verify all embedded prompts are non-empty
	if prompts.Executor == "" {
		t.Error("Executor prompt is empty")
	}
	if prompts.Critic == "" {
		t.Error("Critic prompt is empty")
	}
	if prompts.Marshal == "" {
		t.Error("Marshal prompt is empty")
	}
	if prompts.Compactor == "" {
		t.Error("Compactor prompt is empty")
	}
	if prompts.Security == "" {
		t.Error("Security prompt is empty")
	}
}
