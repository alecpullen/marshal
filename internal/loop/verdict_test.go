package loop_test

import (
	"testing"

	"github.com/alecpullen/marshal/internal/loop"
)

func TestParseVerdict_PlainJSON(t *testing.T) {
	raw := `{"verdict":"PASS","summary":"looks good","issue":"","fix":"","concerns":[]}`
	v, thinks, err := loop.ParseVerdict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !v.IsPASS() {
		t.Errorf("expected PASS, got %q", v.Verdict)
	}
	if v.Summary != "looks good" {
		t.Errorf("summary: %q", v.Summary)
	}
	if len(thinks) != 0 {
		t.Errorf("expected no think blocks")
	}
}

func TestParseVerdict_WithThinkBlock(t *testing.T) {
	raw := `<think>internal reasoning</think>
{"verdict":"FAIL","summary":"missing test","issue":"no unit test","fix":"add test","concerns":[]}`
	v, thinks, err := loop.ParseVerdict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.IsPASS() {
		t.Error("expected FAIL")
	}
	if v.Issue != "no unit test" {
		t.Errorf("issue: %q", v.Issue)
	}
	if len(thinks) != 1 || thinks[0] != "internal reasoning" {
		t.Errorf("thinks: %v", thinks)
	}
}

func TestParseVerdict_MarkdownFence(t *testing.T) {
	raw := "```json\n{\"verdict\":\"PASS\",\"summary\":\"ok\",\"issue\":\"\",\"fix\":\"\",\"concerns\":[]}\n```"
	v, _, err := loop.ParseVerdict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !v.IsPASS() {
		t.Errorf("expected PASS")
	}
}

func TestParseVerdict_TrailingComma(t *testing.T) {
	raw := `{"verdict":"PASS","summary":"ok","issue":"","fix":"","concerns":[],}`
	v, _, err := loop.ParseVerdict(raw)
	if err != nil {
		t.Fatalf("trailing comma should be tolerated: %v", err)
	}
	if !v.IsPASS() {
		t.Errorf("expected PASS")
	}
}

func TestParseVerdict_ProseWrapping(t *testing.T) {
	raw := `Here is my verdict:
{"verdict":"FAIL","summary":"bad code","issue":"nil deref","fix":"add nil check","concerns":[]}
Let me know if you have questions.`
	v, _, err := loop.ParseVerdict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.IsPASS() {
		t.Error("expected FAIL")
	}
}

func TestParseVerdict_CaseInsensitive(t *testing.T) {
	raw := `{"verdict":"pass","summary":"ok","issue":"","fix":"","concerns":[]}`
	v, _, err := loop.ParseVerdict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !v.IsPASS() {
		t.Errorf("expected PASS (case-insensitive), got %q", v.Verdict)
	}
}

func TestParseVerdict_InvalidVerdict(t *testing.T) {
	raw := `{"verdict":"MAYBE","summary":"","issue":"","fix":"","concerns":[]}`
	_, _, err := loop.ParseVerdict(raw)
	if err == nil {
		t.Error("expected error for invalid verdict value")
	}
}

func TestParseVerdict_InvalidJSON(t *testing.T) {
	_, _, err := loop.ParseVerdict("not json at all")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
