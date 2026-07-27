package sidepanel

import (
	"testing"
	"time"

	"marshal/internal/db"
)

func TestSparkline(t *testing.T) {
	tests := []struct {
		name   string
		values []int
		width  int
		want   string
	}{
		{"empty", nil, 8, ""},
		{"zero width", []int{1, 2}, 0, ""},
		{"flat series renders lowest", []int{5, 5, 5}, 3, "▁▁▁"},
		{"ascending", []int{0, 4, 8}, 3, "▁▄█"},
		{"truncates to width, keeping the tail", []int{0, 0, 0, 0, 8}, 2, "▁█"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sparkline(tt.values, tt.width); got != tt.want {
				t.Errorf("Sparkline(%v, %d) = %q, want %q", tt.values, tt.width, got, tt.want)
			}
		})
	}
}

func TestTelemetry(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	turns := []db.TurnMetricsRow{
		// most recent first
		{StartedAt: now.Add(-1 * time.Minute), PromptTokens: 1000, CompletionTokens: 200, CacheHits: 1},
		{StartedAt: now.Add(-3 * time.Minute), PromptTokens: 800, CompletionTokens: 200, CacheHits: 0},
		{StartedAt: now.Add(-5 * time.Minute), PromptTokens: 700, CompletionTokens: 100, CacheHits: 1},
	}
	got := Telemetry(turns, now)

	// (1200 + 1000 + 800) / 3 = 1000
	if got.AvgTokens != 1000 {
		t.Errorf("AvgTokens = %d, want 1000", got.AvgTokens)
	}
	// 3000 tokens over the 5 minutes since the oldest turn started = 600/min
	if got.BurnPerMin != 600 {
		t.Errorf("BurnPerMin = %d, want 600", got.BurnPerMin)
	}
	// 2 of 3 turns had a cache hit
	if got.CacheHitPct != 66 {
		t.Errorf("CacheHitPct = %d, want 66", got.CacheHitPct)
	}
	// Series is oldest-first for left-to-right rendering
	want := []int{800, 1000, 1200}
	for i, v := range want {
		if got.Series[i] != v {
			t.Errorf("Series = %v, want %v", got.Series, want)
			break
		}
	}
}

func TestTelemetryEmpty(t *testing.T) {
	got := Telemetry(nil, time.Now())
	if got.AvgTokens != 0 || got.BurnPerMin != 0 || got.CacheHitPct != 0 {
		t.Errorf("Telemetry(nil) = %+v, want zero", got)
	}
	if len(got.Series) != 0 {
		t.Errorf("Series = %v, want empty", got.Series)
	}
}

func TestTelemetrySingleTurnNoDivideByZero(t *testing.T) {
	now := time.Now()
	got := Telemetry([]db.TurnMetricsRow{
		{StartedAt: now, PromptTokens: 500, CompletionTokens: 100},
	}, now)
	if got.AvgTokens != 600 {
		t.Errorf("AvgTokens = %d, want 600", got.AvgTokens)
	}
	if got.BurnPerMin != 0 {
		t.Errorf("BurnPerMin = %d, want 0 (no elapsed time)", got.BurnPerMin)
	}
}
