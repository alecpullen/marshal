package modelcatalog

import (
	"strings"
	"testing"
)

const fixture = `{
  "google": {
    "id": "google",
    "name": "Google",
    "models": {
      "gemini-3-pro": {
        "id": "gemini-3-pro",
        "name": "Gemini 3 Pro",
        "limit": {"context": 1048576, "output": 65536},
        "cost": {"input": 1.25, "output": 10, "cache_read": 0.31},
        "tool_call": true
      }
    }
  },
  "togetherai": {
    "id": "togetherai",
    "name": "Together AI",
    "models": {
      "deepseek-v4.1-flash:1017": {
        "id": "deepseek-v4.1-flash:1017",
        "limit": {"context": 131072, "output": 8192},
        "cost": {"input": 0.1, "output": 0.2},
        "tool_call": false
      }
    }
  }
}`

func decodeFixture(t *testing.T) *Catalog {
	t.Helper()
	cat, err := Decode(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return cat
}

func TestDecodeReadsLimitsCostAndToolCall(t *testing.T) {
	cat := decodeFixture(t)

	m, ok := cat.Lookup("google", "gemini-3-pro")
	if !ok {
		t.Fatalf("Lookup(google, gemini-3-pro) missed")
	}
	if m.Limit.Context != 1048576 {
		t.Errorf("context = %d, want 1048576", m.Limit.Context)
	}
	if m.Limit.Output != 65536 {
		t.Errorf("output = %d, want 65536", m.Limit.Output)
	}
	if m.Cost.Input != 1.25 {
		t.Errorf("input cost = %v, want 1.25", m.Cost.Input)
	}
	if m.ToolCall == nil || !*m.ToolCall {
		t.Errorf("tool_call = %v, want true", m.ToolCall)
	}
}

func TestDecodeIgnoresUnknownFields(t *testing.T) {
	doc := `{
      "google": {
        "id": "google",
        "models": {
          "gemini-3-pro": {
            "id": "gemini-3-pro",
            "reasoning": true,
            "modalities": {"input": ["text", "image"], "output": ["text"]},
            "limit": {"context": 1048576, "output": 65536}
          }
        }
      }
    }`
	cat, err := Decode(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, ok := cat.Lookup("google", "gemini-3-pro"); !ok {
		t.Fatalf("Lookup(google, gemini-3-pro) missed")
	}
}

func TestLookupIsExactAndCaseSensitive(t *testing.T) {
	cat := decodeFixture(t)

	if _, ok := cat.Lookup("google", "Gemini-3-Pro"); ok {
		t.Errorf("Lookup(google, Gemini-3-Pro) hit, want miss")
	}
	if _, ok := cat.Lookup("Google", "gemini-3-pro"); ok {
		t.Errorf("Lookup(Google, gemini-3-pro) hit, want miss")
	}
}

func TestLookupKeepsTaggedModelIDs(t *testing.T) {
	cat := decodeFixture(t)

	if _, ok := cat.Lookup("togetherai", "deepseek-v4.1-flash:1017"); !ok {
		t.Errorf("Lookup with exact tag missed")
	}
	if _, ok := cat.Lookup("togetherai", "deepseek-v4.1-flash"); ok {
		t.Errorf("Lookup without tag hit, want miss")
	}
}

func TestResolveBindingUsesReviewedTable(t *testing.T) {
	cases := []struct {
		provider string
		catalog  string
	}{
		{"gemini", "google"},
		{"together", "togetherai"},
		{"fireworks", "fireworks-ai"},
		{"opencode-go", "opencode-go"},
		{"ollama-cloud", "ollama-cloud"},
	}
	for _, tc := range cases {
		got, ok := ResolveBinding(tc.provider, "")
		if !ok {
			t.Errorf("ResolveBinding(%q) unbound, want %q", tc.provider, tc.catalog)
			continue
		}
		if got != tc.catalog {
			t.Errorf("ResolveBinding(%q) = %q, want %q", tc.provider, got, tc.catalog)
		}
	}
}

func TestResolveBindingRequiresBaseURLForKimi(t *testing.T) {
	got, ok := ResolveBinding("kimi", "https://api.kimi.com/coding/v1")
	if !ok {
		t.Fatalf("ResolveBinding(kimi, coding endpoint) unbound")
	}
	if got != "kimi-code-plan-cn" {
		t.Errorf("ResolveBinding(kimi, coding endpoint) = %q, want %q", got, "kimi-code-plan-cn")
	}

	if got, ok := ResolveBinding("kimi", "https://api.moonshot.ai/v1"); ok {
		t.Errorf("ResolveBinding(kimi, moonshot endpoint) = %q, want unbound", got)
	}
}

func TestResolveBindingRejectsUnknownProvider(t *testing.T) {
	if got, ok := ResolveBinding("my-custom-endpoint", "https://example.com/v1"); ok {
		t.Errorf("ResolveBinding(my-custom-endpoint) = %q, want unbound", got)
	}
}
