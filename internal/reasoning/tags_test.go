package reasoning_test

import (
	"testing"

	"github.com/alec/marshal/internal/reasoning"
)

func TestStrip_NoThinkBlocks(t *testing.T) {
	stripped, thinks := reasoning.Strip("plain text")
	if stripped != "plain text" {
		t.Errorf("got %q", stripped)
	}
	if len(thinks) != 0 {
		t.Errorf("expected no think blocks, got %v", thinks)
	}
}

func TestStrip_SingleBlock(t *testing.T) {
	input := `<think>internal reasoning here</think>
{"verdict":"PASS","summary":"looks good"}`
	stripped, thinks := reasoning.Strip(input)

	if stripped != `{"verdict":"PASS","summary":"looks good"}` {
		t.Errorf("unexpected stripped: %q", stripped)
	}
	if len(thinks) != 1 || thinks[0] != "internal reasoning here" {
		t.Errorf("unexpected thinks: %v", thinks)
	}
}

func TestStrip_MultilineBlock(t *testing.T) {
	input := "<think>\nline one\nline two\n</think>\nresult"
	stripped, thinks := reasoning.Strip(input)
	if stripped != "result" {
		t.Errorf("got %q", stripped)
	}
	if len(thinks) != 1 {
		t.Fatalf("expected 1 think, got %d", len(thinks))
	}
	if thinks[0] != "line one\nline two" {
		t.Errorf("unexpected think content: %q", thinks[0])
	}
}

func TestStrip_MultipleBlocks(t *testing.T) {
	input := "<think>first</think> middle <think>second</think> end"
	stripped, thinks := reasoning.Strip(input)
	if stripped != "middle  end" {
		t.Errorf("got %q", stripped)
	}
	if len(thinks) != 2 {
		t.Errorf("expected 2 think blocks, got %v", thinks)
	}
}

func TestStrip_EmptyBlock(t *testing.T) {
	input := "<think></think>result"
	stripped, thinks := reasoning.Strip(input)
	if stripped != "result" {
		t.Errorf("got %q", stripped)
	}
	if len(thinks) != 1 || thinks[0] != "" {
		t.Errorf("unexpected thinks: %v", thinks)
	}
}
