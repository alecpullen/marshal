package session

import "testing"

func TestForceVisionIsSessionScoped(t *testing.T) {
	s := newTestState()
	s.ForceVision("openai", "gpt-4o")
	if !s.VisionForced("openai", "gpt-4o") {
		t.Fatal("expected the forced model to read true")
	}
	if s.VisionForced("openai", "gpt-4o-mini") {
		t.Fatal("a different model in the same session must not read true")
	}
	if s.VisionForced("anthropic", "gpt-4o") {
		t.Fatal("a different provider with the same model name must not read true")
	}
	other := newTestState()
	if other.VisionForced("openai", "gpt-4o") {
		t.Fatal("a different session must not read true")
	}
}
