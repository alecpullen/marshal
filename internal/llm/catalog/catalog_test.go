package catalog

import "testing"

func TestLookupKnownModel(t *testing.T) {
	w, out := Lookup("qwen2.5-coder:14b")
	if w == 0 {
		t.Fatal("known model qwen2.5-coder:14b should resolve a context window")
	}
	if out == 0 {
		t.Fatal("known model should resolve max output tokens")
	}
}

func TestLookupUnknownModelReturnsZero(t *testing.T) {
	w, out := Lookup("not-a-real-model:999b")
	if w != 0 || out != 0 {
		t.Fatalf("unknown model = (%d,%d), want (0,0)", w, out)
	}
}

func TestLookupCaseInsensitive(t *testing.T) {
	w1, _ := Lookup("Qwen2.5-Coder:14B")
	w2, _ := Lookup("qwen2.5-coder:14b")
	if w1 != w2 || w1 == 0 {
		t.Fatalf("case-insensitive lookup mismatch: %d vs %d", w1, w2)
	}
}
