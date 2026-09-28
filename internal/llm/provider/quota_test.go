package provider

import (
	"net/http"
	"testing"
)

func TestQuotaFromHeadersFullSet(t *testing.T) {
	h := http.Header{}
	for k, v := range codexQuotaHeaders() {
		h.Set(k, v)
	}
	q := quotaFromHeaders(h)
	if q == nil {
		t.Fatal("quotaFromHeaders returned nil for a full header set")
	}
	if q.PlanType != "plus" {
		t.Errorf("PlanType = %q, want plus", q.PlanType)
	}
	if q.PrimaryUsedPercent != 50 {
		t.Errorf("PrimaryUsedPercent = %d, want 50", q.PrimaryUsedPercent)
	}
	if q.PrimaryResetAfterSecs != 14845 {
		t.Errorf("PrimaryResetAfterSecs = %d, want 14845", q.PrimaryResetAfterSecs)
	}
	if q.PrimaryWindowMinutes != 300 {
		t.Errorf("PrimaryWindowMinutes = %d, want 300", q.PrimaryWindowMinutes)
	}
	if q.SecondaryUsedPercent != 8 {
		t.Errorf("SecondaryUsedPercent = %d, want 8", q.SecondaryUsedPercent)
	}
	if q.SecondaryResetAfterSecs != 601645 {
		t.Errorf("SecondaryResetAfterSecs = %d, want 601645", q.SecondaryResetAfterSecs)
	}
	if q.SecondaryWindowMinutes != 10080 {
		t.Errorf("SecondaryWindowMinutes = %d, want 10080", q.SecondaryWindowMinutes)
	}
}

func TestQuotaFromHeadersTable(t *testing.T) {
	tests := []struct {
		name string
		hdrs map[string]string
		want *int // PrimaryUsedPercent, nil means expect a nil QuotaInfo
	}{
		{
			name: "no quota headers at all",
			hdrs: map[string]string{"Content-Type": "text/event-stream"},
			want: nil,
		},
		{
			name: "nil header map",
			hdrs: nil,
			want: nil,
		},
		{
			name: "only plan type is still quota",
			hdrs: map[string]string{"X-Codex-Plan-Type": "pro"},
			want: intPtr(0),
		},
		{
			name: "unparsable percent is skipped, not fatal",
			hdrs: map[string]string{
				"X-Codex-Primary-Used-Percent": "not-a-number",
				"X-Codex-Plan-Type":            "plus",
			},
			want: intPtr(0),
		},
		{
			name: "zero percent is a real value",
			hdrs: map[string]string{"X-Codex-Primary-Used-Percent": "0"},
			want: intPtr(0),
		},
		{
			name: "partial set keeps what arrived",
			hdrs: map[string]string{
				"X-Codex-Primary-Used-Percent":   "73",
				"X-Codex-Secondary-Used-Percent": "12",
			},
			want: intPtr(73),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var h http.Header
			if tc.hdrs != nil {
				h = http.Header{}
				for k, v := range tc.hdrs {
					h.Set(k, v)
				}
			}
			q := quotaFromHeaders(h)
			if tc.want == nil {
				if q != nil {
					t.Fatalf("quotaFromHeaders = %+v, want nil", q)
				}
				return
			}
			if q == nil {
				t.Fatal("quotaFromHeaders = nil, want a QuotaInfo")
			}
			if q.PrimaryUsedPercent != *tc.want {
				t.Errorf("PrimaryUsedPercent = %d, want %d", q.PrimaryUsedPercent, *tc.want)
			}
		})
	}
}

func TestQuotaFromHeadersPartialSetKeepsBoth(t *testing.T) {
	h := http.Header{}
	h.Set("X-Codex-Primary-Used-Percent", "73")
	h.Set("X-Codex-Secondary-Used-Percent", "12")
	q := quotaFromHeaders(h)
	if q == nil {
		t.Fatal("quotaFromHeaders = nil, want a QuotaInfo")
	}
	if q.PrimaryUsedPercent != 73 || q.SecondaryUsedPercent != 12 {
		t.Fatalf("got primary=%d secondary=%d, want 73/12", q.PrimaryUsedPercent, q.SecondaryUsedPercent)
	}
}

func intPtr(n int) *int { return &n }
