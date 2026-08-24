package limits

import "testing"

func testTable() Table {
	return NewTable(map[string]Limit{
		"anthropic/claude-sonnet-4.5": {ContextWindow: 200000, MaxOutputTokens: 64000},
		"openai/gpt-4o":               {ContextWindow: 128000, MaxOutputTokens: 16384},
		"moonshotai/kimi-k2":          {ContextWindow: 131072, MaxOutputTokens: 16384},
		"groq/llama-3.3-70b":          {ContextWindow: 131072, MaxOutputTokens: 32768},
		"ollama/qwen2.5-coder:7b":     {ContextWindow: 32768, MaxOutputTokens: 8192},
	})
}

func TestLookupExactProviderModel(t *testing.T) {
	lim, kind := testTable().Lookup("openai", "gpt-4o")
	if kind != MatchExact {
		t.Fatalf("kind = %v, want MatchExact", kind)
	}
	if lim.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want 128000", lim.ContextWindow)
	}
}

func TestLookupSuffixIsExact(t *testing.T) {
	// A different provider serving the same literal id still counts as a
	// keyed hit, not an inference.
	_, kind := testTable().Lookup("azure", "gpt-4o")
	if kind != MatchExact {
		t.Fatalf("kind = %v, want MatchExact", kind)
	}
}

func TestLookupVariantDatestamp(t *testing.T) {
	lim, kind := testTable().Lookup("anthropic", "claude-sonnet-4-5-20250929")
	if kind != MatchVariant {
		t.Fatalf("kind = %v, want MatchVariant", kind)
	}
	if lim.ContextWindow != 200000 || lim.MaxOutputTokens != 64000 {
		t.Errorf("limit = %+v, want {200000 64000}", lim)
	}
}

func TestLookupVariantPrefixAfterRoleSuffix(t *testing.T) {
	lim, kind := testTable().Lookup("groq", "kimi-k2-instruct")
	if kind != MatchVariant {
		t.Fatalf("kind = %v, want MatchVariant", kind)
	}
	if lim.ContextWindow != 131072 {
		t.Errorf("ContextWindow = %d, want 131072", lim.ContextWindow)
	}
}

func TestLookupVariantHyphenDate(t *testing.T) {
	// gpt-4o-2024-11-20 keeps its date through norm; the prefix rung is
	// what resolves it against openai/gpt-4o.
	lim, kind := testTable().Lookup("openai", "gpt-4o-2024-11-20")
	if kind != MatchVariant {
		t.Fatalf("kind = %v, want MatchVariant", kind)
	}
	if lim.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want 128000", lim.ContextWindow)
	}
}

func TestLookupAmbiguousPrefixReturnsNone(t *testing.T) {
	tbl := NewTable(map[string]Limit{
		"a/kimi-k2":          {ContextWindow: 131072},
		"b/kimi-k2-thinking": {ContextWindow: 262144},
	})
	if _, kind := tbl.Lookup("groq", "kimi-k2-thinking-preview"); kind != MatchNone {
		t.Fatalf("kind = %v, want MatchNone for an ambiguous prefix", kind)
	}
}

func TestLookupCollisionPrefersSmallestContext(t *testing.T) {
	tbl := NewTable(map[string]Limit{
		"a/claude-sonnet-4.5": {ContextWindow: 200000, MaxOutputTokens: 64000},
		"b/claude-sonnet-4-5": {ContextWindow: 128000, MaxOutputTokens: 32000},
	})
	lim, kind := tbl.Lookup("c", "claude-sonnet-4-5-20250929")
	if kind != MatchVariant {
		t.Fatalf("kind = %v, want MatchVariant", kind)
	}
	if lim.ContextWindow != 128000 || lim.MaxOutputTokens != 32000 {
		t.Errorf("limit = %+v, want the smaller {128000 32000}", lim)
	}
}

func TestLookupUnknownReturnsNone(t *testing.T) {
	if _, kind := testTable().Lookup("openai", "totally-made-up-xyz"); kind != MatchNone {
		t.Fatalf("kind = %v, want MatchNone", kind)
	}
}

func TestLookupZeroTableDoesNotPanic(t *testing.T) {
	var tbl Table
	if _, kind := tbl.Lookup("openai", "gpt-4o"); kind != MatchNone {
		t.Fatalf("kind = %v, want MatchNone", kind)
	}
}

func TestKnownTrueForToolCallingOnly(t *testing.T) {
	trueVal := true
	lim := Limit{ToolCalling: &trueVal}
	if !known(lim) {
		t.Errorf("known() = false, want true for a limit with only ToolCalling set")
	}
}

func TestMergeTriStateAnyTrueWins(t *testing.T) {
	trueVal, falseVal := true, false
	a := map[string]Limit{"openai/gpt-4o": {ContextWindow: 100000, ToolCalling: &falseVal}}
	b := map[string]Limit{"openai/gpt-4o": {ContextWindow: 128000, ToolCalling: &trueVal}}

	out := merge(a, b)
	got, ok := out["openai/gpt-4o"]
	if !ok {
		t.Fatalf("missing merged entry")
	}
	if got.ToolCalling == nil || !*got.ToolCalling {
		t.Errorf("ToolCalling after merge = %v, want true", got.ToolCalling)
	}
}

func TestMergeTriStateFalseOnlyWhenBothSourcesSayFalse(t *testing.T) {
	falseVal := false
	a := map[string]Limit{"openai/gpt-4o": {ContextWindow: 100000, ToolCalling: &falseVal}}
	b := map[string]Limit{"openai/gpt-4o": {ContextWindow: 128000, ToolCalling: &falseVal}}

	out := merge(a, b)
	got := out["openai/gpt-4o"]
	if got.ToolCalling == nil || *got.ToolCalling {
		t.Errorf("ToolCalling after merge = %v, want false", got.ToolCalling)
	}
}

func TestMergeTriStateUnreportedDoesNotOverrideKnown(t *testing.T) {
	trueVal := true
	a := map[string]Limit{"openai/gpt-4o": {ContextWindow: 100000, ToolCalling: &trueVal}}
	b := map[string]Limit{"openai/gpt-4o": {ContextWindow: 128000, ToolCalling: nil}}

	out := merge(a, b)
	got := out["openai/gpt-4o"]
	if got.ToolCalling == nil || !*got.ToolCalling {
		t.Errorf("ToolCalling after merge = %v, want true (unreported source must not erase it)", got.ToolCalling)
	}
}
