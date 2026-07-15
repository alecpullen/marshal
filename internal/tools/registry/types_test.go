package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSandboxMetaLimitsJSONIncludesOutputTruncated(t *testing.T) {
	m := SandboxMeta{OutputTruncated: true}
	j, err := m.LimitsJSON()
	if err != nil {
		t.Fatalf("LimitsJSON: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(j), &parsed); err != nil {
		t.Fatalf("LimitsJSON() = %q, cannot unmarshal: %v", j, err)
	}
	v, ok := parsed["output_truncated"]
	if !ok {
		t.Fatalf("LimitsJSON() = %q, missing output_truncated key", j)
	}
	if b, _ := v.(bool); !b {
		t.Fatalf("output_truncated = %v (%T), want true", v, v)
	}

	m2 := SandboxMeta{OutputTruncated: false}
	j2, err := m2.LimitsJSON()
	if err != nil {
		t.Fatalf("LimitsJSON: %v", err)
	}
	if strings.Contains(j2, "output_truncated") {
		t.Fatalf("LimitsJSON() for false = %q, should not contain output_truncated", j2)
	}
}

func TestSandboxMetaLimitsJSONReturnsValidJSON(t *testing.T) {
	// LimitsJSON must return a non-empty JSON string and a nil error
	// for valid meta. The marshal-error path is not exercisable through
	// the public API because all map-value types are primitives.
	m := SandboxMeta{Backend: "restricted"}
	s, err := m.LimitsJSON()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == "" {
		t.Fatal("expected non-empty JSON")
	}
}
