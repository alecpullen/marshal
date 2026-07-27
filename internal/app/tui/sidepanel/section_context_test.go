package sidepanel

import (
	"strings"
	"testing"

	"marshal/internal/contextpack"
)

func ctxData() Data {
	return Data{Pack: contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 14000, MaxTokens: 128000},
		Sections: []contextpack.Section{
			{Title: "system", EstimatedTokens: 2100},
			{Title: "repo map", EstimatedTokens: 4000},
			{Title: "files", EstimatedTokens: 1200},
			{Title: "transcript", EstimatedTokens: 6700},
		},
	}}
}

func TestContextSectionIdentity(t *testing.T) {
	s := ContextSection{}
	if s.ID() != "context" {
		t.Errorf("ID = %q, want context", s.ID())
	}
	if s.Priority() != 1 {
		t.Errorf("Priority = %d, want 1", s.Priority())
	}
}

func TestContextSectionRelevance(t *testing.T) {
	if (ContextSection{}).Relevant(Data{}) {
		t.Error("Relevant(empty pack) = true, want false")
	}
	if !(ContextSection{}).Relevant(ctxData()) {
		t.Error("Relevant(populated pack) = false, want true")
	}
}

func TestContextSectionRendersBreakdown(t *testing.T) {
	got := StripANSI(strings.Join((ContextSection{}).Render(ctxData(), 30, 12), "\n"))
	for _, want := range []string{"system", "repo map", "transcript", "2k", "6k"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing %q:\n%s", want, got)
		}
	}
}

func TestContextSectionOneLine(t *testing.T) {
	got := StripANSI((ContextSection{}).OneLine(ctxData(), 40))
	if !strings.Contains(got, "14k") {
		t.Errorf("OneLine = %q, want the used-token count", got)
	}
	if !strings.Contains(got, "10%") {
		t.Errorf("OneLine = %q, want the percentage", got)
	}
}

func TestBar(t *testing.T) {
	tests := []struct {
		frac  float64
		width int
		want  string
	}{
		{0, 4, "░░░░"},
		{1, 4, "████"},
		{0.5, 4, "██░░"},
		{-1, 4, "░░░░"}, // clamped
		{2, 4, "████"},  // clamped
		{0.5, 0, ""},    // degenerate
	}
	for _, tt := range tests {
		if got := StripANSI(Bar(tt.frac, tt.width)); got != tt.want {
			t.Errorf("Bar(%v, %d) = %q, want %q", tt.frac, tt.width, got, tt.want)
		}
	}
}
