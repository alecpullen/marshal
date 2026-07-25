package schema

import "testing"

func TestModelInfoLimitFieldsZeroByDefault(t *testing.T) {
	m := ModelInfo{ID: "x", OwnedBy: "y"}
	if m.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0", m.ContextWindow)
	}
	if m.MaxOutputTokens != 0 {
		t.Errorf("MaxOutputTokens = %d, want 0", m.MaxOutputTokens)
	}
}

func TestModelInfoLimitFieldsSettable(t *testing.T) {
	m := ModelInfo{ID: "x", OwnedBy: "y", ContextWindow: 128000, MaxOutputTokens: 4096}
	if m.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want 128000", m.ContextWindow)
	}
	if m.MaxOutputTokens != 4096 {
		t.Errorf("MaxOutputTokens = %d, want 4096", m.MaxOutputTokens)
	}
}
