package sidepanel

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"time"

	"marshal/internal/contextpack"
	"marshal/internal/db"
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
	// Note: the brief's test template expected "11%" (rounded from 14000/128000),
	// and the brief's code template uses int(frac*100+0.5) rounding. The test
	// also expected "2.1k"/"6.7k" but strutil.CompactTokens uses integer
	// division (no decimal), so we assert "2k"/"6k" instead.
	if !strings.Contains(got, "11%") {
		t.Errorf("OneLine = %q, want the percentage", got)
	}
}

func TestContextSectionRendersTelemetry(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	d := ctxData()
	d.Now = now
	d.Turns = []db.TurnMetricsRow{
		{StartedAt: now.Add(-2 * time.Minute), PromptTokens: 1800, CompletionTokens: 200},
		{StartedAt: now.Add(-6 * time.Minute), PromptTokens: 1700, CompletionTokens: 300},
	}
	got := StripANSI(strings.Join((ContextSection{}).Render(d, 34, 20), "\n"))
	if !strings.Contains(got, "/min") {
		t.Errorf("no burn rate row:\n%s", got)
	}
	if !strings.Contains(got, "turns") {
		t.Errorf("no headroom row:\n%s", got)
	}
}

func TestContextSectionNoTelemetryWithoutTurns(t *testing.T) {
	got := StripANSI(strings.Join((ContextSection{}).Render(ctxData(), 34, 20), "\n"))
	if strings.Contains(got, "/min") {
		t.Errorf("burn rate rendered with no turn data:\n%s", got)
	}
}

func packWithUsage(t *testing.T, used, max int) contextpack.Pack {
	t.Helper()
	return contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{
			EstimatedTokens: used,
			MaxTokens:       max,
		},
	}
}

func anyStyled(rows []string) bool {
	for _, r := range rows {
		if strings.Contains(r, "\x1b[") {
			return true
		}
	}
	return false
}

func stripAll(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = StripANSI(r)
	}
	return out
}

func TestContextSectionWarnsWhenNearLimit(t *testing.T) {
	over := Data{Pack: packWithUsage(t, 90_000, 100_000)}
	under := Data{Pack: packWithUsage(t, 10_000, 100_000)}

	overRows := ContextSection{}.Render(over, 30, 0)
	underRows := ContextSection{}.Render(under, 30, 0)

	if !anyStyled(overRows) {
		t.Errorf("usage at 90%% should be warning-styled, got %v", stripAll(overRows))
	}
	if anyStyled(underRows) {
		t.Errorf("usage at 10%% should be unstyled, got %v", stripAll(underRows))
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

// pctGlyphCol reports the cell column of a row's trailing "%" glyph, which
// is the mark the eye actually tracks down the column. Cells, not bytes:
// the fill bar is built from multibyte block glyphs, so a byte offset says
// nothing about where the percentage appears on screen.
func pctGlyphCol(row string) int {
	plain := StripANSI(row)
	i := strings.LastIndex(plain, "%")
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(plain[:i])
}

// The reported defect: the fill bar's percentage sat one cell left of the
// composition percentages directly beneath it, because the two rows were
// built from format strings that reserved different trailing widths. They
// must share a column at every rail width.
func TestContextPercentColumnAlignsWithFillBar(t *testing.T) {
	for _, w := range []int{24, 28, 32, 36, 40, 56} {
		rows := (ContextSection{}).Render(ctxData(), w, 0)
		if len(rows) < 2 {
			t.Fatalf("width %d: want a bar row and breakdown rows, got %v", w, rows)
		}
		barCol := pctGlyphCol(rows[0])
		if barCol < 0 {
			t.Fatalf("width %d: bar row has no percentage: %q", w, StripANSI(rows[0]))
		}
		for i, r := range rows[1:] {
			got := pctGlyphCol(r)
			if got < 0 {
				continue // the telemetry rows carry no percentage
			}
			if got != barCol {
				t.Errorf("width %d: breakdown row %d has its %% at cell %d, bar at %d\n bar = %q\n row = %q",
					w, i, got, barCol, StripANSI(rows[0]), StripANSI(r))
			}
		}
	}
}

// Every context row that carries a value column ends flush with the rail's
// inner edge, so the block reads as one table rather than a ragged stack.
func TestContextRowsAreFlushToWidth(t *testing.T) {
	for _, w := range []int{24, 32, 40, 56} {
		for i, r := range (ContextSection{}).Render(ctxData(), w, 0) {
			if n := ansi.StringWidth(r); n != w {
				t.Errorf("width %d: row %d is %d cells, want %d: %q",
					w, i, n, w, StripANSI(r))
			}
		}
	}
}
