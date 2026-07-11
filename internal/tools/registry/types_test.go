package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSandboxMetaLimitsJSONIncludesOutputTruncated(t *testing.T) {
	m := SandboxMeta{OutputTruncated: true}
	j := m.LimitsJSON()
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
	j2 := m2.LimitsJSON()
	if strings.Contains(j2, "output_truncated") {
		t.Fatalf("LimitsJSON() for false = %q, should not contain output_truncated", j2)
	}
}
