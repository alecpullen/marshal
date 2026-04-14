package loop

import (
	"testing"
)

func TestCompactor_ShouldCompact(t *testing.T) {
	c := &Compactor{}

	// Should compact when round >= compactAfter
	if !c.ShouldCompact(3, 2) {
		t.Error("should compact when round >= compactAfter")
	}

	// Should not compact when round < compactAfter
	if c.ShouldCompact(1, 3) {
		t.Error("should not compact when round < compactAfter")
	}

	// Edge case: equal
	if !c.ShouldCompact(2, 2) {
		t.Error("should compact when round == compactAfter")
	}
}

func TestCompactor_EmptyHistory(t *testing.T) {
	c := &Compactor{}

	result, err := c.Compact(nil, []Round{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if result.Summary != "" {
		t.Error("empty history should give empty summary")
	}
}
