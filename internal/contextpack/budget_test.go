package contextpack

import "testing"

func TestBudgetForWindow(t *testing.T) {
	tests := []struct {
		name   string
		window int
		want   int
	}{
		{"unknown window falls back to the flat default", 0, DefaultMaxTokens},
		{"negative window falls back to the flat default", -1, DefaultMaxTokens},
		{"tiny window is held to a quarter, not the flat floor", 8192, 2048},
		{"16k window is held to a quarter", 16000, 4000},
		{"32k window sits exactly on the floor", 32000, minPackTokens},
		{"128k window gets an eighth", 128000, 16000},
		{"200k window gets an eighth", 200000, 25000},
		{"1M window clamps to the ceiling", 1000000, maxPackTokens},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BudgetForWindow(tt.window); got != tt.want {
				t.Errorf("BudgetForWindow(%d) = %d, want %d", tt.window, got, tt.want)
			}
		})
	}
}
