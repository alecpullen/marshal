package sidepanel

import (
	"reflect"
	"testing"
)

func TestFit(t *testing.T) {
	tests := []struct {
		name   string
		costs  []SectionCost
		budget int
		want   []RenderState
	}{
		{
			name:   "everything fits expanded",
			costs:  []SectionCost{{Priority: 0, Natural: 5, Clipped: 3}, {Priority: 1, Natural: 4}},
			budget: 20,
			want:   []RenderState{StateExpanded, StateExpanded},
		},
		{
			// 5+4 content + 1 blank = 10, budget 9. The highest priority
			// number (1) steps down; it has no clipped state so it goes
			// straight to one-line: 5+1+1 = 7.
			name:   "lowest priority collapses to one-line",
			costs:  []SectionCost{{Priority: 0, Natural: 5, Clipped: 3}, {Priority: 1, Natural: 4}},
			budget: 9,
			want:   []RenderState{StateExpanded, StateOneLine},
		},
		{
			// Both must shrink. Priority 1 → one-line (no clipped),
			// then priority 0 → clipped (3).
			name:   "clippable section uses its clipped state",
			costs:  []SectionCost{{Priority: 0, Natural: 8, Clipped: 3}, {Priority: 1, Natural: 4}},
			budget: 5,
			want:   []RenderState{StateClipped, StateOneLine},
		},
		{
			// Even all-one-line (1+1+1 blank = 3) exceeds budget 2, so the
			// highest priority number is dropped.
			name:   "drops when one-line still overflows",
			costs:  []SectionCost{{Priority: 0, Natural: 5}, {Priority: 1, Natural: 4}},
			budget: 2,
			want:   []RenderState{StateOneLine, StateDropped},
		},
		{
			name:   "render order does not determine collapse order",
			costs:  []SectionCost{{Priority: 5, Natural: 4}, {Priority: 0, Natural: 4}},
			budget: 6,
			want:   []RenderState{StateOneLine, StateExpanded},
		},
		{
			name:   "empty input",
			costs:  nil,
			budget: 10,
			want:   []RenderState{},
		},
		{
			// Never drop the last survivor, even at budget 0.
			name:   "always keeps one section",
			costs:  []SectionCost{{Priority: 0, Natural: 9}},
			budget: 0,
			want:   []RenderState{StateOneLine},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fit(tt.costs, tt.budget)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fit(%v, %d) = %v, want %v", tt.costs, tt.budget, got, tt.want)
			}
		})
	}
}

func TestFitIsDeterministic(t *testing.T) {
	costs := []SectionCost{
		{Priority: 0, Natural: 6, Clipped: 3},
		{Priority: 2, Natural: 5, Clipped: 2},
		{Priority: 1, Natural: 4},
		{Priority: 3, Natural: 7, Clipped: 4},
	}
	first := fit(costs, 14)
	for i := 0; i < 50; i++ {
		if got := fit(costs, 14); !reflect.DeepEqual(got, first) {
			t.Fatalf("iteration %d = %v, want %v", i, got, first)
		}
	}
}
