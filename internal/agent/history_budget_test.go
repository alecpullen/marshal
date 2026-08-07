package agent

import "testing"

func TestHistoryBudget_Adaptive(t *testing.T) {
	cases := []struct {
		name       string
		window     int
		configured int
		want       int
	}{
		{"128k window, no override -> 16000", 128000, 0, 16000},
		{"32k window clamps to 4000", 32000, 0, 4000},
		{"window <=32k clamps to 4000", 8000, 0, 4000},
		{"1M window clamps to 32000", 1_000_000, 0, 32000},
		{"explicit 9000 wins", 128000, 9000, 9000},
		{"explicit larger than cap clamps via cap not used", 128000, 50000, 50000}, // explicit wins regardless
		{"64k window -> 8000", 64000, 0, 8000},
		{"200k window -> 25000", 200000, 0, 25000},
		{"256k window -> 32000 (boundary)", 256000, 0, 32000},
		{"257k window clamps to 32000", 257000, 0, 32000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := historyBudget(tc.window, tc.configured)
			if got != tc.want {
				t.Fatalf("historyBudget(window=%d, configured=%d) = %d, want %d",
					tc.window, tc.configured, got, tc.want)
			}
		})
	}
}
