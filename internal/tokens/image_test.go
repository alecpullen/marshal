package tokens_test

import (
	"testing"

	"github.com/alec/marshal/internal/tokens"
)

func TestCountImageTokens_LowDetail(t *testing.T) {
	// Low detail is always 85 tokens regardless of dimensions.
	got := tokens.CountImageTokens(4096, 4096, tokens.ImageDetailLow)
	if got != 85 {
		t.Errorf("low detail: expected 85, got %d", got)
	}
}

func TestCountImageTokens_HighDetail(t *testing.T) {
	tests := []struct {
		name   string
		w, h   int
		detail tokens.ImageDetail
		want   int
	}{
		{
			// 512×512 image: 1 tile → 85 + 170*1 = 255
			name: "512x512",
			w:    512, h: 512,
			detail: tokens.ImageDetailHigh,
			want:   255,
		},
		{
			// 1024×1024: short side = 1024 > 768 → scale to 768×768 → 2×2 tiles → 85+170*4 = 765
			name: "1024x1024",
			w:    1024, h: 1024,
			detail: tokens.ImageDetailHigh,
			want:   765,
		},
		{
			// 300×200: short side = 200 < 768, no scale needed → ceil(300/512)*ceil(200/512) = 1×1 → 255
			name: "300x200",
			w:    300, h: 200,
			detail: tokens.ImageDetailHigh,
			want:   255,
		},
		{
			// 2048×4096: long side 4096 > 2048 → scale to 1024×2048;
			// short side 1024 > 768 → scale to 768×1536;
			// tiles: ceil(768/512)*ceil(1536/512) = 2*3 = 6 → 85+170*6 = 1105
			name: "2048x4096",
			w:    2048, h: 4096,
			detail: tokens.ImageDetailHigh,
			want:   1105,
		},
		{
			// auto behaves like high
			name: "auto_is_high",
			w:    512, h: 512,
			detail: tokens.ImageDetailAuto,
			want:   255,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tokens.CountImageTokens(tc.w, tc.h, tc.detail)
			if got != tc.want {
				t.Errorf("CountImageTokens(%d, %d, %s) = %d, want %d",
					tc.w, tc.h, tc.detail, got, tc.want)
			}
		})
	}
}

func TestCharHeuristic(t *testing.T) {
	c := tokens.CharHeuristic()
	// "hello" = 5 chars → ceil(5/4) = 2 tokens
	if got := c.Count("hello"); got != 2 {
		t.Errorf("Count(\"hello\") = %d, want 2", got)
	}

	msgs := []tokens.Message{
		{Role: "user", Content: "hello"},
	}
	// role(1) + content(2) + overhead(4) = 7, plus reply primer(3) = 10
	got := c.CountMessages(msgs)
	if got != 10 {
		t.Errorf("CountMessages = %d, want 10", got)
	}
}
